package hostcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
)

// Store is the persistence policy: Start, Deliver and Abort in a
// transaction the backend opened, and the definition cache.
type Store struct {
	Kinds  workflow.Kinds
	Limits workflow.Limits
	// NewRunID makes the ID of a run whose NewRun.ID is empty. Nil: a
	// UUIDv7 string.
	NewRunID func() string
	// Prefix starts the store's own error messages: "postgres".
	Prefix string
	// randN returns a number in [0, n). Nil: math/rand/v2's Int64N.
	// Tests set it to pin the retry jitter.
	randN func(n int64) int64

	mu   sync.Mutex
	defs map[string]*workflow.Definition
}

// definitionReader is a Backend or a Tx.
type definitionReader interface {
	DefinitionDoc(ctx context.Context, hash string) ([]byte, error)
}

// Start starts a run in tx. Starting a run whose ID exists with the same
// definition returns its state, no commands and Applied false.
func (s *Store) Start(ctx context.Context, tx Tx, run durable.NewRun) (*durable.Result, error) {
	if run.Definition == nil || run.Definition.Hash() == "" {
		return nil, workflow.ErrNotValidated
	}
	def, err := s.own(run.Definition)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", durable.ErrDefinitionUnavailable, err)
	}
	if run.ID == "" {
		if run.ID = s.newRunID(); run.ID == "" {
			return nil, fmt.Errorf("%s: WithRunIDs returned an empty ID", s.Prefix)
		}
	}

	now := time.Now().UTC()
	ev := workflow.Event{At: now, Kind: workflow.EventStart, Input: run.Input}
	st, cmds, err := workflow.Apply(def, nil, ev)
	if err != nil {
		return nil, err
	}

	doc, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	if err := tx.InsertDefinition(ctx, def.Hash(), def.Name, doc); err != nil {
		return nil, fmt.Errorf("%s: store definition: %w", s.Prefix, err)
	}

	stateJSON, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	if hasNUL(stateJSON) {
		return nil, fmt.Errorf("%w: input contains a NUL character (\\u0000)", durable.ErrUnstorable)
	}
	meta := run.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	// Pass the encoded JSON, not the map: a database/sql driver without
	// pgx's type support takes only plain values.
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("%w: metadata: %v", durable.ErrUnstorable, err)
	}
	if hasNUL(metaJSON) {
		return nil, fmt.Errorf("%w: metadata contains a NUL character (\\u0000)", durable.ErrUnstorable)
	}

	// The run row is written whole here; persist adds the rest. A repeated
	// Start returns the existing run.
	inserted, err := tx.InsertRun(ctx, RunRow{
		ID:             run.ID,
		DefinitionHash: def.Hash(),
		Workflow:       def.Name,
		Status:         st.Status,
		State:          stateJSON,
		Error:          jsonOrNil(st.Error),
		Outputs:        jsonOrNil(st.Outputs),
		Metadata:       metaJSON,
		At:             now,
		FinishedAt:     st.FinishedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: insert run: %w", s.Prefix, err)
	}
	if !inserted {
		existing, hash, err := s.LockRun(ctx, tx, run.ID)
		if err != nil {
			return nil, err
		}
		if hash != def.Hash() {
			return nil, durable.ErrRunExists
		}
		return &durable.Result{RunID: run.ID, State: existing}, nil
	}

	if err := s.persist(ctx, tx, def, run.ID, nil, st, cmds, ev); err != nil {
		return nil, err
	}
	s.cache(def)
	return &durable.Result{RunID: run.ID, State: st, Commands: cmds, Applied: true}, nil
}

// Deliver applies an event in tx: it locks the run row, calls Apply, and
// stores the state, step records and jobs. Delivering an event twice is
// safe. A duplicate changes nothing, returns no commands and has Applied
// false.
func (s *Store) Deliver(
	ctx context.Context, tx Tx, runID string, ev workflow.Event,
) (*durable.Result, error) {
	st, hash, err := s.LockRun(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	return s.DeliverLocked(ctx, tx, runID, st, hash, Storable(ev))
}

// DeliverLocked applies an event to a run whose row tx has locked, with st
// and hash as LockRun read them. ev has been through Storable.
func (s *Store) DeliverLocked(
	ctx context.Context,
	tx Tx,
	runID string,
	st *workflow.State,
	hash string,
	ev workflow.Event,
) (*durable.Result, error) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	def, err := s.Definition(ctx, tx, hash)
	if err != nil {
		return nil, err
	}
	next, cmds, err := workflow.Apply(def, st, ev)
	if err != nil {
		return nil, err
	}
	if next == st {
		return &durable.Result{RunID: runID, State: st}, nil // nothing changed: a duplicate or late event
	}
	if err := s.persist(ctx, tx, def, runID, st, next, cmds, ev); err != nil {
		return nil, err
	}
	return &durable.Result{RunID: runID, State: next, Commands: cmds, Applied: true}, nil
}

// LockRun locks a run's row in tx and decodes its state.
func (s *Store) LockRun(
	ctx context.Context, tx Tx, runID string,
) (*workflow.State, string, error) {
	data, hash, err := tx.LockRun(ctx, runID)
	if err != nil {
		return nil, "", err
	}
	var st workflow.State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, "", fmt.Errorf("%s: decode state of run %s: %w", s.Prefix, runID, err)
	}
	return &st, hash, nil
}

func (s *Store) newRunID() string {
	if s.NewRunID != nil {
		return s.NewRunID()
	}
	return uuid.NewV7().String()
}

// own returns the definition the store runs for def: the cached one for
// its hash, or def's document loaded with the store's kinds and limits.
// The store loads every definition itself, so a run behaves the same in
// this process as in one that loads it from the database.
func (s *Store) own(def *workflow.Definition) (*workflow.Definition, error) {
	s.mu.Lock()
	cached := s.defs[def.Hash()]
	s.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	doc, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	return workflow.Load(doc, s.Kinds, workflow.WithLimits(s.Limits))
}

func (s *Store) cache(def *workflow.Definition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.defs == nil {
		s.defs = map[string]*workflow.Definition{}
	}
	if _, ok := s.defs[def.Hash()]; !ok {
		s.defs[def.Hash()] = def
	}
}

// Cached returns how many definitions the store has cached.
func (s *Store) Cached() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.defs)
}

// Definition returns the definition stored under hash, read through r. A
// database error is returned as is (it may be transient); a definition
// that is missing or does not load wraps ErrDefinitionUnavailable.
func (s *Store) Definition(
	ctx context.Context, r definitionReader, hash string,
) (*workflow.Definition, error) {
	s.mu.Lock()
	def := s.defs[hash]
	s.mu.Unlock()
	if def != nil {
		return def, nil
	}
	doc, err := r.DefinitionDoc(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("%s: load definition %s: %w", s.Prefix, hash, err)
	}
	if doc == nil {
		return nil, fmt.Errorf("%w: definition %s is not stored", durable.ErrDefinitionUnavailable, hash)
	}
	def, err = workflow.Load(doc, s.Kinds, workflow.WithLimits(s.Limits))
	if err != nil {
		return nil, fmt.Errorf("%w: definition %s: %v", durable.ErrDefinitionUnavailable, hash, err)
	}
	if def.Hash() != hash {
		return nil, fmt.Errorf("%w: definition %s loads with hash %s",
			durable.ErrDefinitionUnavailable, hash, def.Hash())
	}
	s.cache(def)
	return def, nil
}

// persist writes the new state, the event, the step records and the outbox
// jobs. prev is nil for Start, whose run row Start has already inserted.
func (s *Store) persist(
	ctx context.Context,
	tx Tx,
	def *workflow.Definition,
	runID string,
	prev, next *workflow.State,
	cmds []workflow.Command,
	ev workflow.Event,
) (err error) {
	seq := int64(1) // Start inserted the run row
	if prev != nil {
		if seq, err = s.updateRun(ctx, tx, runID, next, ev.At); err != nil {
			return err
		}
	}

	evJSON, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := tx.InsertEvent(ctx, runID, EventRow{
		Seq:    seq,
		Kind:   ev.Kind,
		StepID: ev.StepID,
		At:     ev.At,
		Event:  evJSON,
	}); err != nil {
		return fmt.Errorf("%s: insert event: %w", s.Prefix, err)
	}

	// Settle the step record the event reports on.
	if ev.StepID != "" && prev != nil {
		if status, output, stepErr := AttemptOutcome(def, next, ev); status != "" {
			if err := tx.SettleStep(ctx, runID, StepSettle{
				StepID: ev.StepID,
				Status: status,
				Output: jsonOrNil(output),
				Error:  jsonOrNil(stepErr),
				At:     ev.At,
			}); err != nil {
				return fmt.Errorf("%s: settle step: %w", s.Prefix, err)
			}
		}
		if u := next.Unit(ev.StepID); u == nil || u.Status != workflow.UnitRunning {
			// The attempt settled, by whatever path: neither its step job
			// (a hand-off whose worker died before marking it done, or one
			// still running) nor its timeout timer is wanted any more.
			if err := tx.DeleteJobs(ctx, runID, ev.StepID); err != nil {
				return fmt.Errorf("%s: delete settled jobs: %w", s.Prefix, err)
			}
		}
	}

	wroteJobs := false
	// shift is the jitter added to each retry's job. The retry's timeout
	// timer moves by the same amount, so the jitter never eats into the
	// step's timeout.
	var shift map[workflow.StepID]time.Duration
	for _, c := range cmds {
		switch c.Kind {
		case workflow.CommandDispatchStep, workflow.CommandScheduleTimer:
			cmdJSON, err := json.Marshal(c)
			if err != nil {
				return err
			}
			kind := "step"
			if c.Kind == workflow.CommandScheduleTimer {
				kind = "timer"
			}
			if c.Kind == workflow.CommandDispatchStep || c.StepKind == workflow.KindSleep {
				if err := tx.InsertStep(ctx, runID, StepRow{
					StepID:       c.StepID,
					Step:         c.Step,
					Kind:         c.StepKind,
					Index:        c.Index,
					Attempt:      c.Attempt,
					Params:       jsonOrNil(c.Params),
					Item:         jsonOrNil(c.Item),
					NotBefore:    c.NotBefore,
					DispatchedAt: ev.At,
				}); err != nil {
					return fmt.Errorf("%s: insert step: %w", s.Prefix, err)
				}
			}

			// A job due now takes the store's clock, so skew between this
			// process and the database never delays it.
			var runAt time.Time
			if c.NotBefore.After(ev.At) {
				runAt = c.NotBefore
				if c.Kind == workflow.CommandDispatchStep && c.Attempt > 1 {
					if shift == nil {
						shift = map[workflow.StepID]time.Duration{}
					}
					shift[c.StepID] = retryJitter(c.NotBefore.Sub(ev.At), s.randN)
				}
				runAt = runAt.Add(shift[c.StepID])
			}
			if err := tx.InsertJob(ctx, runID, JobRow{
				StepID:   c.StepID,
				Kind:     kind,
				StepKind: c.StepKind,
				Command:  cmdJSON,
				RunAt:    runAt,
			}); err != nil {
				return fmt.Errorf("%s: insert job: %w", s.Prefix, err)
			}
			wroteJobs = true
		case workflow.CommandCancelStep:
			if err := tx.DeleteJobs(ctx, runID, c.StepID); err != nil {
				return fmt.Errorf("%s: delete job: %w", s.Prefix, err)
			}
			if err := tx.CancelSteps(ctx, runID, c.StepID, ev.At); err != nil {
				return fmt.Errorf("%s: cancel step: %w", s.Prefix, err)
			}
		}
	}

	if next.Status.Terminal() {
		// Timeout timers outlive the steps they guard; drop them.
		if err := tx.DeleteJobs(ctx, runID, ""); err != nil {
			return fmt.Errorf("%s: clear jobs: %w", s.Prefix, err)
		}
	}
	if err := s.finished(ctx, tx, runID, prev, next, cmds); err != nil {
		return err
	}
	if wroteJobs {
		if err := tx.JobsWritten(ctx, runID); err != nil {
			return fmt.Errorf("%s: notify: %w", s.Prefix, err)
		}
	}
	return nil
}

// Storable makes an event's values storable in jsonb, so that applying it
// cannot fail for an encoding reason after a handler did its work. A
// completion whose output cannot be stored (a value JSON cannot encode,
// such as NaN, or a NUL character, which jsonb rejects) becomes a failure
// with type invalid_output: the result is permanent, and running the
// handler again would only repeat it. A stored output is the normalized
// form Apply sees. NUL characters in error messages and reasons are
// replaced with U+FFFD. Every backend applies these rules, so a run that
// works on one works on the others.
func Storable(ev workflow.Event) workflow.Event {
	switch ev.Kind {
	case workflow.EventStepCompleted:
		if ev.Output == nil {
			break
		}
		data, err := json.Marshal(ev.Output)
		switch {
		case err != nil:
			return invalidOutput(ev, "output cannot be encoded as JSON: "+err.Error())
		case hasNUL(data):
			return invalidOutput(ev, `output contains a NUL character (\u0000), which Postgres cannot store`)
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return invalidOutput(ev, "output cannot be decoded as JSON: "+err.Error())
		}
		ev.Output = v
	case workflow.EventStepFailed:
		if ev.Error != nil {
			e := *ev.Error
			e.Type, e.Message = cleanNUL(e.Type), cleanNUL(e.Message)
			ev.Error = &e
		}
	case workflow.EventCancel:
		ev.Reason = cleanNUL(ev.Reason)
	}
	return ev
}

func invalidOutput(ev workflow.Event, msg string) workflow.Event {
	ev.Kind, ev.Output = workflow.EventStepFailed, nil
	ev.Error = &workflow.StepError{Type: workflow.ErrorTypeInvalidOutput, Message: msg}
	return ev
}

func cleanNUL(s string) string { return strings.ReplaceAll(s, "\x00", "�") }

// hasNUL reports whether encoded JSON holds a \u0000 escape, which jsonb
// rejects. Backslashes occur only inside strings, as escapes, so skipping
// each escape as a unit finds exactly the NUL characters.
func hasNUL(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' || i+1 >= len(data) {
			continue
		}
		if data[i+1] == 'u' && i+5 < len(data) && string(data[i+2:i+6]) == "0000" {
			return true
		}
		i++ // skip the escaped character
	}
	return false
}

// AttemptOutcome is what became of the attempt an applied event reports on,
// as the run sees it: the accepted output, or the error Apply recorded
// (which wins over a reported result it rejected). An attempt that failed
// and was retried is no longer in the state; its replacement carries the
// error. An attempt whose unit has left the state altogether (a visit
// superseded in the same Apply) settles as the event says, so its record
// never stays dispatched.
func AttemptOutcome(
	def *workflow.Definition, next *workflow.State, ev workflow.Event,
) (status durable.StepStatus, output any, stepErr *workflow.StepError) {
	if u := next.Unit(ev.StepID); u != nil {
		switch u.Status {
		case workflow.UnitCompleted:
			return durable.StepCompleted, u.Output, nil
		case workflow.UnitFailed:
			return durable.StepFailed, nil, u.Error
		case workflow.UnitCancelled:
			return durable.StepCancelled, nil, nil
		}
		return "", nil, nil
	}
	if r := next.Unit(nextAttempt(ev.StepID)); r != nil && r.Error != nil {
		return durable.StepFailed, nil, r.Error
	}
	switch ev.Kind {
	case workflow.EventStepCompleted:
		return durable.StepCompleted, ev.Output, nil
	case workflow.EventStepFailed:
		return durable.StepFailed, nil, ev.Error
	case workflow.EventTimerFired:
		if st := def.Step(ev.StepID.Step()); st != nil && st.Kind == workflow.KindSleep {
			return durable.StepCompleted, nil, nil
		}
		return durable.StepFailed, nil, &workflow.StepError{
			Type:      workflow.ErrorTypeTimeout,
			Message:   "step timed out",
			Retryable: true,
		}
	}
	return "", nil, nil
}

// nextAttempt is the StepID of the attempt after id.
func nextAttempt(id workflow.StepID) workflow.StepID {
	n := id.Attempt()
	if n == 0 {
		return ""
	}
	return workflow.StepID(id.Unit() + "#" + strconv.Itoa(n+1))
}

// Abort fails a run that Apply cannot advance, in the transaction that
// locked it, without its definition: every attempt in flight is cancelled,
// the run is failed with runErr, its jobs are dropped, and the event log,
// the status notification and the finish hook record it. A finished run is
// left alone.
func (s *Store) Abort(
	ctx context.Context,
	tx Tx,
	runID string,
	st *workflow.State,
	runErr *workflow.RunError,
	at time.Time,
) error {
	if st.Status.Terminal() {
		return nil
	}

	next, cmds := workflow.Abort(st, runErr, at)
	seq, err := s.updateRun(ctx, tx, runID, next, at)
	if err != nil {
		return err
	}

	ev := workflow.Event{At: at, Kind: durable.EventAborted, Reason: cleanNUL(runErr.Error())}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := tx.InsertEvent(ctx, runID, EventRow{
		Seq:    seq,
		Kind:   ev.Kind,
		StepID: runErr.StepID,
		At:     at,
		Event:  evJSON,
	}); err != nil {
		return fmt.Errorf("%s: insert event: %w", s.Prefix, err)
	}

	if err := tx.DeleteJobs(ctx, runID, ""); err != nil {
		return fmt.Errorf("%s: clear jobs: %w", s.Prefix, err)
	}
	if err := tx.CancelSteps(ctx, runID, "", at); err != nil {
		return fmt.Errorf("%s: cancel steps: %w", s.Prefix, err)
	}
	return s.finished(ctx, tx, runID, st, next, cmds)
}

// finished reports a status change and calls the finish hook for a
// terminal command.
func (s *Store) finished(ctx context.Context, tx Tx, runID string, prev, next *workflow.State,
	cmds []workflow.Command) error {
	if prev == nil || prev.Status != next.Status {
		if err := tx.StatusChanged(ctx, runID, next.Status); err != nil {
			return fmt.Errorf("%s: notify: %w", s.Prefix, err)
		}
	}
	for _, c := range cmds {
		switch c.Kind {
		case workflow.CommandRunCompleted, workflow.CommandRunFailed, workflow.CommandRunCancelled:
			f := durable.Finished{
				RunID:         runID,
				Status:        next.Status,
				Outputs:       c.Outputs,
				Error:         c.Error,
				Reason:        c.Reason,
				FinallyErrors: c.FinallyErrors,
			}
			if err := tx.OnFinished(ctx, f); err != nil {
				return fmt.Errorf("%s: on finished: %w", s.Prefix, err)
			}
		}
	}
	return nil
}

// updateRun stores a run's new state and returns its event sequence.
func (s *Store) updateRun(
	ctx context.Context, tx Tx, runID string, next *workflow.State, at time.Time,
) (int64, error) {
	stateJSON, err := json.Marshal(next)
	if err != nil {
		return 0, err
	}
	seq, err := tx.UpdateRun(ctx, runID, RunUpdate{
		Status:     next.Status,
		State:      stateJSON,
		Error:      jsonOrNil(next.Error),
		Outputs:    jsonOrNil(next.Outputs),
		FinishedAt: next.FinishedAt,
		At:         at,
	})
	if err != nil {
		return 0, fmt.Errorf("%s: update run: %w", s.Prefix, err)
	}
	return seq, nil
}

// IsPermanent reports whether Apply refused an event for good: the run
// will never accept it.
func IsPermanent(err error) bool {
	for _, target := range []error{
		workflow.ErrUnknownStep, workflow.ErrInvalidEvent, workflow.ErrNotStarted,
		workflow.ErrDefinitionMismatch, workflow.ErrNotValidated,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// jsonOrNil encodes v, or returns nil (SQL NULL) for a nil value.
func jsonOrNil(v any) []byte {
	switch t := v.(type) {
	case nil:
		return nil
	case *workflow.StepError:
		if t == nil {
			return nil
		}
	case *workflow.RunError:
		if t == nil {
			return nil
		}
	case map[string]any:
		if t == nil {
			return nil
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// The durable hosts add up to retryJitterPercent of a retry's backoff to
// the time its job is due, at most retryJitterMax. Runs that fail together
// then retry spread out, not at the same instant. Apply stays exact: the
// jitter only makes a job later than the engine's NotBefore, never earlier.
const (
	retryJitterPercent = 20
	retryJitterMax     = time.Minute
)

// retryJitter returns a random extra delay in [0, min(20% of backoff, 1m)].
func retryJitter(backoff time.Duration, randN func(int64) int64) time.Duration {
	limit := min(backoff/100*retryJitterPercent, retryJitterMax)
	if limit <= 0 {
		return 0
	}
	if randN == nil {
		randN = rand.Int64N
	}
	return time.Duration(randN(int64(limit) + 1))
}
