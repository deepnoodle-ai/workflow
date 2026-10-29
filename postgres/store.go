// Package postgres is a durable host for workflows on Postgres.
//
// No process holds a run. Each event advances a run in one short
// transaction: lock the run row, call [workflow.Apply], store the new state,
// upsert step records, and write the commands to an outbox (the jobs
// table). Workers claim jobs after commit, run the step handlers, and
// deliver the results as new events. Any worker can advance any run, a run
// can wait for days between events, and a crash loses nothing: an expired
// lease makes the job claimable again.
//
// Every *Tx method takes the caller's transaction, so an application can
// start a run, or deliver a step result, atomically with its own writes.
// The methods without Tx open their own transaction.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/deepnoodle-ai/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

var (
	ErrRunNotFound = errors.New("postgres: run not found")
	ErrRunExists   = errors.New("postgres: a run with this id exists for a different definition")
	// ErrUnstorable is returned by Start when its input or metadata hold
	// a value Postgres cannot store as jsonb: a NUL character (\u0000).
	ErrUnstorable = errors.New("postgres: value cannot be stored in Postgres")
	// ErrDefinitionUnavailable means this store cannot load a run's
	// definition: its kinds lack a kind the definition uses, or the
	// document no longer validates. It is a host configuration problem.
	// Start returns it and starts nothing; Deliver returns it; a Worker
	// gives the configuration MaxClaims leases to be fixed, then fails
	// the run with type definition_unavailable.
	ErrDefinitionUnavailable = errors.New("postgres: the run's definition cannot be loaded")
)

// ErrorTypeDefinitionUnavailable is the RunError type of a run a Worker
// failed because its definition could not be loaded.
const ErrorTypeDefinitionUnavailable = "definition_unavailable"

// EventAborted is the kind of the event log entry a Worker records when it
// fails a run that Apply cannot advance: its definition cannot be loaded,
// or a timer could not be fired MaxClaims times. It is never passed to
// Apply; Reason says why.
const EventAborted workflow.EventKind = "aborted"

// querier is a pgx.Tx or a pool.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var schemaRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Store persists runs and their outbox.
type Store struct {
	pool   *pgxpool.Pool
	kinds  workflow.Kinds
	schema string
	limits workflow.Limits

	onFinished func(ctx context.Context, tx pgx.Tx, f Finished) error
	newRunID   func() string

	mu   sync.Mutex
	defs map[string]*workflow.Definition
}

// Option configures a Store.
type Option func(*Store)

// WithSchema puts the tables in a schema other than "workflow".
func WithSchema(name string) Option { return func(s *Store) { s.schema = name } }

// WithLimits sets the state limits for every definition the store loads.
// The store loads each definition itself, from its document, so the limits
// a caller loaded NewRun.Definition with do not apply.
func WithLimits(l workflow.Limits) Option { return func(s *Store) { s.limits = l } }

// WithOnFinished calls fn in the transaction that moves a run to completed,
// failed or cancelled, once per run. Use it to record the outcome or enqueue
// a notification atomically with it. An error from fn rolls the event back,
// so return one only when the transaction itself failed; a worker retries
// its settle, and the event is applied once fn succeeds.
func WithOnFinished(fn func(ctx context.Context, tx pgx.Tx, f Finished) error) Option {
	return func(s *Store) { s.onFinished = fn }
}

// WithRunIDs sets how Start makes the ID of a run whose NewRun.ID is
// empty. The default is a UUIDv7 string, so IDs sort by start time.
func WithRunIDs(fn func() string) Option { return func(s *Store) { s.newRunID = fn } }

// Finished describes a run that has just reached a terminal status. A run
// with finally steps finishes once they are done.
type Finished struct {
	RunID   string
	Status  workflow.RunStatus // completed, failed or cancelled
	Outputs map[string]any     // completed
	Error   *workflow.RunError // failed
	Reason  string             // cancelled
	// FinallyErrors lists the finally steps that failed or did not run.
	// They do not change Status; alert on them, since cleanup did not
	// finish.
	FinallyErrors []*workflow.RunError
}

// New returns a Store. kinds validates every definition the store loads.
func New(pool *pgxpool.Pool, kinds workflow.Kinds, opts ...Option) (*Store, error) {
	s := &Store{
		pool:     pool,
		kinds:    kinds,
		schema:   "workflow",
		defs:     map[string]*workflow.Definition{},
		newRunID: func() string { return uuid.NewV7().String() },
	}
	for _, o := range opts {
		o(s)
	}
	if !schemaRE.MatchString(s.schema) {
		return nil, fmt.Errorf("postgres: invalid schema name %q", s.schema)
	}
	return s, nil
}

// migrations are the schema's versions, in order: migrations[i] takes the
// schema from version i to i+1. Append; never edit one that has shipped.
var migrations = []string{schemaSQL}

// Migrate brings the schema up to date. It is safe to call from every
// process at startup: migrations run in one transaction under an advisory
// lock, each at most once, and are recorded in schema_migrations. Migrate
// fails if the database is at a newer version than this package knows.
func (s *Store) Migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Transaction-scoped, so it is released on commit or rollback and
		// works through a transaction-mode pooler.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`,
			migrateLockKey(s.schema)); err != nil {
			return fmt.Errorf("postgres: migrate lock: %w", err)
		}

		if _, err := tx.Exec(ctx, s.sql(`
			CREATE SCHEMA IF NOT EXISTS {{schema}};
			CREATE TABLE IF NOT EXISTS {{schema}}.schema_migrations (
			    version    integer PRIMARY KEY,
			    applied_at timestamptz NOT NULL DEFAULT now()
			)`)); err != nil {
			return fmt.Errorf("postgres: migrate: %w", err)
		}

		var version int
		if err := tx.QueryRow(ctx,
			s.sql(`SELECT coalesce(max(version), 0) FROM {{schema}}.schema_migrations`)).
			Scan(&version); err != nil {
			return fmt.Errorf("postgres: migrate: %w", err)
		}
		if version > len(migrations) {
			return fmt.Errorf("postgres: schema %s is at version %d; this package knows %d",
				s.schema, version, len(migrations))
		}

		for v := version; v < len(migrations); v++ {
			if _, err := tx.Exec(ctx, s.sql(migrations[v])); err != nil {
				return fmt.Errorf("postgres: migration %d: %w", v+1, err)
			}
			if _, err := tx.Exec(ctx,
				s.sql(`INSERT INTO {{schema}}.schema_migrations (version) VALUES ($1)`), v+1); err != nil {
				return fmt.Errorf("postgres: migration %d: %w", v+1, err)
			}
		}
		return nil
	})
}

// migrateLockKey is the advisory lock that serializes Migrate per schema.
func migrateLockKey(schema string) int64 {
	h := fnv.New64a()
	h.Write([]byte("deepnoodle-ai/workflow/postgres.Migrate:" + schema))
	return int64(h.Sum64())
}

func (s *Store) sql(q string) string { return strings.ReplaceAll(q, "{{schema}}", s.schema) }

// NotifyChannel is the channel the store notifies when it writes jobs.
func (s *Store) NotifyChannel() string { return s.schema + "_jobs" }

// RunsChannel is the channel the store notifies, on commit, when a run's
// status changes. The payload is JSON: {"run_id": "...", "status": "failed"}.
// Notifications are best effort (a listener that is not connected misses
// them); read the run with GetRun, or use WithOnFinished for a guarantee.
func (s *Store) RunsChannel() string { return s.schema + "_runs" }

// Result is what an event did to a run.
type Result struct {
	// RunID is the run's ID: NewRun.ID, or the one Start generated.
	RunID string
	// State is the run's state after the event.
	State *workflow.State
	// Commands are what Apply produced. Dispatches, timers and step
	// cancellations are already in the outbox, committed with the state;
	// workers carry them out. Do not execute them again. Terminal commands
	// (RunCompleted, RunFailed, RunCancelled) are reported to
	// WithOnFinished and RunsChannel.
	Commands []workflow.Command
	// Applied reports whether the event changed the run. It is false for a
	// duplicate, for a result of an attempt that is no longer wanted (it
	// timed out, was cancelled or was retried), for an event on a finished
	// run, and for a Start of a run that already exists. An HTTP handler
	// that records an approval can answer "already decided" when it is
	// false.
	Applied bool
}

// NewRun describes a run to start.
type NewRun struct {
	// ID identifies the run. Derive it from what caused the run (for
	// example "pr-42-abc123" for a pull request event) to make starting
	// idempotent; empty generates one (see [WithRunIDs]).
	ID         string
	Definition *workflow.Definition
	// Input is the run's input, read as `input`. It is kept in the
	// run's state.
	Input    map[string]any
	Metadata map[string]any
}

// Start starts a run in its own transaction.
func (s *Store) Start(ctx context.Context, run NewRun) (*Result, error) {
	var res *Result
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		res, err = s.StartTx(ctx, tx, run)
		return err
	})
	return res, err
}

// StartTx starts a run in the caller's transaction. The commands in the
// result are already in the outbox; they run after the caller commits.
// Starting a run whose ID exists with the same definition returns its state,
// no commands and Applied false.
func (s *Store) StartTx(ctx context.Context, tx pgx.Tx, run NewRun) (*Result, error) {
	if run.Definition == nil || run.Definition.Hash() == "" {
		return nil, workflow.ErrNotValidated
	}
	def, err := s.own(run.Definition)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDefinitionUnavailable, err)
	}
	if run.ID == "" {
		if run.ID = s.newRunID(); run.ID == "" {
			return nil, errors.New("postgres: WithRunIDs returned an empty ID")
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
	if _, err := tx.Exec(ctx, s.sql(`
		INSERT INTO {{schema}}.definitions (hash, name, document) VALUES ($1, $2, $3)
		ON CONFLICT (hash) DO NOTHING`), def.Hash(), def.Name, doc); err != nil {
		return nil, fmt.Errorf("postgres: store definition: %w", err)
	}

	stateJSON, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	if hasNUL(stateJSON) {
		return nil, fmt.Errorf("%w: input contains a NUL character (\\u0000)", ErrUnstorable)
	}
	meta := run.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	if data, err := json.Marshal(meta); err != nil {
		return nil, fmt.Errorf("%w: metadata: %v", ErrUnstorable, err)
	} else if hasNUL(data) {
		return nil, fmt.Errorf("%w: metadata contains a NUL character (\\u0000)", ErrUnstorable)
	}

	// The run row is written whole here; persist adds the rest. ON
	// CONFLICT makes a repeated Start return the existing run.
	var finished *time.Time
	if !st.FinishedAt.IsZero() {
		finished = &st.FinishedAt
	}
	tag, err := tx.Exec(ctx, s.sql(`
		INSERT INTO {{schema}}.runs (id, definition_hash, workflow, status, state, error, outputs, metadata,
		                             event_seq, created_at, updated_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $9, $10)
		ON CONFLICT (id) DO NOTHING`),
		run.ID, def.Hash(), def.Name, string(st.Status), stateJSON,
		jsonOrNil(st.Error), jsonOrNil(st.Outputs), meta, now, finished)
	if err != nil {
		return nil, fmt.Errorf("postgres: insert run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		existing, hash, err := s.lockRun(ctx, tx, run.ID)
		if err != nil {
			return nil, err
		}
		if hash != def.Hash() {
			return nil, ErrRunExists
		}
		return &Result{RunID: run.ID, State: existing}, nil
	}

	if err := s.persist(ctx, tx, def, run.ID, nil, st, cmds, ev); err != nil {
		return nil, err
	}
	s.cache(def)
	return &Result{RunID: run.ID, State: st, Commands: cmds, Applied: true}, nil
}

// Deliver applies an event in its own transaction.
func (s *Store) Deliver(ctx context.Context, runID string, ev workflow.Event) (*Result, error) {
	var res *Result
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		res, err = s.DeliverTx(ctx, tx, runID, ev)
		return err
	})
	return res, err
}

// DeliverTx applies an event in the caller's transaction: it locks the run
// row, calls Apply, and stores the state, step records and jobs. The
// commands in the result are already in the outbox; they run after the
// caller commits.
//
// Delivering an event twice is safe. A duplicate changes nothing, returns no
// commands and has Applied false.
func (s *Store) DeliverTx(
	ctx context.Context, tx pgx.Tx, runID string, ev workflow.Event,
) (*Result, error) {
	st, hash, err := s.lockRun(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	return s.deliverLocked(ctx, tx, runID, st, hash, ev)
}

// deliverLocked applies an event to a run whose row the transaction has
// locked, with st and hash as lockRun read them.
func (s *Store) deliverLocked(
	ctx context.Context, tx pgx.Tx, runID string, st *workflow.State, hash string, ev workflow.Event,
) (*Result, error) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	ev = storable(ev)
	def, err := s.definition(ctx, tx, hash)
	if err != nil {
		return nil, err
	}
	next, cmds, err := workflow.Apply(def, st, ev)
	if err != nil {
		return nil, err
	}
	if next == st {
		return &Result{RunID: runID, State: st}, nil // nothing changed: a duplicate or late event
	}
	if err := s.persist(ctx, tx, def, runID, st, next, cmds, ev); err != nil {
		return nil, err
	}
	return &Result{RunID: runID, State: next, Commands: cmds, Applied: true}, nil
}

// Cancel cancels a run in its own transaction. Applied is false if the run
// had already finished, or was already cancelled: a cancel decides the
// outcome once, and the finally steps it started run to the end. To stop
// them too, deliver a cancel event with Force.
func (s *Store) Cancel(ctx context.Context, runID, reason string) (*Result, error) {
	return s.Deliver(ctx, runID, workflow.Event{Kind: workflow.EventCancel, Reason: reason})
}

// CancelTx is [Store.Cancel] in the caller's transaction.
func (s *Store) CancelTx(ctx context.Context, tx pgx.Tx, runID, reason string) (*Result, error) {
	return s.DeliverTx(ctx, tx, runID, workflow.Event{Kind: workflow.EventCancel, Reason: reason})
}

func (s *Store) lockRun(
	ctx context.Context, tx pgx.Tx, runID string,
) (*workflow.State, string, error) {
	var (
		data []byte
		hash string
	)
	err := tx.QueryRow(ctx,
		s.sql(`SELECT state, definition_hash FROM {{schema}}.runs WHERE id = $1 FOR UPDATE`),
		runID).Scan(&data, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrRunNotFound
	}
	if err != nil {
		return nil, "", err
	}
	var st workflow.State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, "", fmt.Errorf("postgres: decode state of run %s: %w", runID, err)
	}
	return &st, hash, nil
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
	return workflow.Load(doc, s.kinds, workflow.WithLimits(s.limits))
}

func (s *Store) cache(def *workflow.Definition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.defs[def.Hash()]; !ok {
		s.defs[def.Hash()] = def
	}
}

// definition returns the definition stored under hash. A database error is
// returned as is (it may be transient); a definition that is missing or does
// not load wraps ErrDefinitionUnavailable.
func (s *Store) definition(
	ctx context.Context, q querier, hash string,
) (*workflow.Definition, error) {
	s.mu.Lock()
	def := s.defs[hash]
	s.mu.Unlock()
	if def != nil {
		return def, nil
	}
	var doc []byte
	err := q.QueryRow(ctx, s.sql(`SELECT document FROM {{schema}}.definitions WHERE hash = $1`), hash).
		Scan(&doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: definition %s is not stored", ErrDefinitionUnavailable, hash)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: load definition %s: %w", hash, err)
	}
	def, err = workflow.Load(doc, s.kinds, workflow.WithLimits(s.limits))
	if err != nil {
		return nil, fmt.Errorf("%w: definition %s: %v", ErrDefinitionUnavailable, hash, err)
	}
	if def.Hash() != hash {
		return nil, fmt.Errorf("%w: definition %s loads with hash %s",
			ErrDefinitionUnavailable, hash, def.Hash())
	}
	s.cache(def)
	return def, nil
}

// persist writes the new state, the event, the step records and the outbox
// jobs. prev is nil for Start, whose run row StartTx has already inserted.
func (s *Store) persist(
	ctx context.Context,
	tx pgx.Tx,
	def *workflow.Definition,
	runID string,
	prev, next *workflow.State,
	cmds []workflow.Command,
	ev workflow.Event,
) (err error) {
	seq := int64(1) // StartTx inserted the run row
	if prev != nil {
		if seq, err = s.updateRun(ctx, tx, runID, next, ev.At); err != nil {
			return err
		}
	}

	evJSON, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, s.sql(`
		INSERT INTO {{schema}}.events (run_id, seq, kind, step_id, at, event) VALUES ($1, $2, $3, $4, $5, $6)`),
		runID, seq, string(ev.Kind), nullString(string(ev.StepID)), ev.At, evJSON); err != nil {
		return fmt.Errorf("postgres: insert event: %w", err)
	}

	// Settle the step record the event reports on.
	if ev.StepID != "" && prev != nil {
		if status, output, stepErr := attemptOutcome(def, next, ev); status != "" {
			if _, err := tx.Exec(ctx, s.sql(`
				UPDATE {{schema}}.steps SET status = $3, output = $4, error = $5, settled_at = $6
				WHERE run_id = $1 AND step_id = $2 AND status = 'dispatched'`),
				runID, string(ev.StepID), string(status), jsonOrNil(output), jsonOrNil(stepErr),
				ev.At); err != nil {
				return fmt.Errorf("postgres: settle step: %w", err)
			}
		}
		if u := next.Unit(ev.StepID); u == nil || u.Status != workflow.UnitRunning {
			// The attempt settled, by whatever path: neither its step job
			// (a hand-off whose worker died before marking it done, or one
			// still running) nor its timeout timer is wanted any more.
			if _, err := tx.Exec(ctx, s.sql(`
				DELETE FROM {{schema}}.jobs WHERE run_id = $1 AND step_id = $2`),
				runID, string(ev.StepID)); err != nil {
				return fmt.Errorf("postgres: delete settled jobs: %w", err)
			}
		}
	}

	wroteJobs := false
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
				var notBefore *time.Time
				if !c.NotBefore.IsZero() {
					notBefore = &c.NotBefore
				}
				if _, err := tx.Exec(ctx, s.sql(`
					INSERT INTO {{schema}}.steps
					    (run_id, step_id, step, kind, item_index, attempt, status, params, item, not_before, dispatched_at)
					VALUES ($1, $2, $3, $4, $5, $6, 'dispatched', $7, $8, $9, $10)
					ON CONFLICT (run_id, step_id) DO NOTHING`),
					runID, string(c.StepID), c.Step, c.StepKind, c.Index, c.Attempt,
					jsonOrNil(c.Params), jsonOrNil(c.Item), notBefore, ev.At); err != nil {
					return fmt.Errorf("postgres: insert step: %w", err)
				}
			}

			// A job due now takes the database's clock, so skew between
			// this process and Postgres never delays it.
			var runAt *time.Time
			if c.NotBefore.After(ev.At) {
				runAt = &c.NotBefore
			}
			if _, err := tx.Exec(ctx, s.sql(`
				INSERT INTO {{schema}}.jobs (run_id, step_id, kind, step_kind, command, run_at)
				VALUES ($1, $2, $3, $4, $5, coalesce($6, now()))
				ON CONFLICT (run_id, step_id, kind) DO NOTHING`),
				runID, string(c.StepID), kind, c.StepKind, cmdJSON, runAt); err != nil {
				return fmt.Errorf("postgres: insert job: %w", err)
			}
			wroteJobs = true
		case workflow.CommandCancelStep:
			if _, err := tx.Exec(ctx, s.sql(`
				DELETE FROM {{schema}}.jobs WHERE run_id = $1 AND step_id = $2`),
				runID, string(c.StepID)); err != nil {
				return fmt.Errorf("postgres: delete job: %w", err)
			}
			if _, err := tx.Exec(ctx, s.sql(`
				UPDATE {{schema}}.steps SET status = 'cancelled', settled_at = $3
				WHERE run_id = $1 AND step_id = $2 AND status = 'dispatched'`),
				runID, string(c.StepID), ev.At); err != nil {
				return fmt.Errorf("postgres: cancel step: %w", err)
			}
		}
	}

	if next.Status.Terminal() {
		// Timeout timers outlive the steps they guard; drop them.
		if _, err := tx.Exec(ctx,
			s.sql(`DELETE FROM {{schema}}.jobs WHERE run_id = $1`), runID); err != nil {
			return fmt.Errorf("postgres: clear jobs: %w", err)
		}
	}
	if err := s.finished(ctx, tx, runID, prev, next, cmds); err != nil {
		return err
	}
	if wroteJobs {
		if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, s.NotifyChannel(), runID); err != nil {
			return fmt.Errorf("postgres: notify: %w", err)
		}
	}
	return nil
}

// storable makes an event's values storable in jsonb, so that applying it
// cannot fail for an encoding reason after a handler did its work. A
// completion whose output cannot be stored (a value JSON cannot encode,
// such as NaN, or a NUL character, which jsonb rejects) becomes a failure
// with type invalid_output: the result is permanent, and running the
// handler again would only repeat it. A stored output is the normalized
// form Apply sees. NUL characters in error messages and reasons are
// replaced with U+FFFD.
func storable(ev workflow.Event) workflow.Event {
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

func cleanNUL(s string) string { return strings.ReplaceAll(s, "\x00", "\uFFFD") }

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

// attemptOutcome is what became of the attempt an applied event reports on,
// as the run sees it: the accepted output, or the error Apply recorded
// (which wins over a reported result it rejected). An attempt that failed
// and was retried is no longer in the state; its replacement carries the
// error. An attempt whose unit has left the state altogether (a visit
// superseded in the same Apply) settles as the event says, so its record
// never stays dispatched.
func attemptOutcome(
	def *workflow.Definition, next *workflow.State, ev workflow.Event,
) (status StepStatus, output any, stepErr *workflow.StepError) {
	if u := next.Unit(ev.StepID); u != nil {
		switch u.Status {
		case workflow.UnitCompleted:
			return StepCompleted, u.Output, nil
		case workflow.UnitFailed:
			return StepFailed, nil, u.Error
		case workflow.UnitCancelled:
			return StepCancelled, nil, nil
		}
		return "", nil, nil
	}
	if r := next.Unit(nextAttempt(ev.StepID)); r != nil && r.Error != nil {
		return StepFailed, nil, r.Error
	}
	switch ev.Kind {
	case workflow.EventStepCompleted:
		return StepCompleted, ev.Output, nil
	case workflow.EventStepFailed:
		return StepFailed, nil, ev.Error
	case workflow.EventTimerFired:
		if st := def.Step(ev.StepID.Step()); st != nil && st.Kind == workflow.KindSleep {
			return StepCompleted, nil, nil
		}
		return StepFailed, nil, &workflow.StepError{
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

// abort fails a run that Apply cannot advance, in the transaction that
// locked it, without its definition: every attempt in flight is cancelled,
// the run is failed with runErr, its jobs are dropped, and the event log,
// RunsChannel and WithOnFinished record it. A finished run is left alone.
func (s *Store) abort(
	ctx context.Context,
	tx pgx.Tx,
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

	ev := workflow.Event{At: at, Kind: EventAborted, Reason: cleanNUL(runErr.Error())}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, s.sql(`
		INSERT INTO {{schema}}.events (run_id, seq, kind, step_id, at, event) VALUES ($1, $2, $3, $4, $5, $6)`),
		runID, seq, string(ev.Kind), nullString(string(runErr.StepID)), at, evJSON); err != nil {
		return fmt.Errorf("postgres: insert event: %w", err)
	}

	if _, err := tx.Exec(ctx,
		s.sql(`DELETE FROM {{schema}}.jobs WHERE run_id = $1`), runID); err != nil {
		return fmt.Errorf("postgres: clear jobs: %w", err)
	}
	if _, err := tx.Exec(ctx, s.sql(`
		UPDATE {{schema}}.steps SET status = 'cancelled', settled_at = $2
		WHERE run_id = $1 AND status = 'dispatched'`), runID, at); err != nil {
		return fmt.Errorf("postgres: cancel steps: %w", err)
	}
	return s.finished(ctx, tx, runID, st, next, cmds)
}

// finished notifies RunsChannel of a status change and calls the
// WithOnFinished hook for a terminal command.
func (s *Store) finished(ctx context.Context, tx pgx.Tx, runID string, prev, next *workflow.State,
	cmds []workflow.Command) error {
	if prev == nil || prev.Status != next.Status {
		payload, _ := json.Marshal(map[string]string{"run_id": runID, "status": string(next.Status)})
		if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`,
			s.RunsChannel(), string(payload)); err != nil {
			return fmt.Errorf("postgres: notify: %w", err)
		}
	}
	if s.onFinished == nil {
		return nil
	}
	for _, c := range cmds {
		switch c.Kind {
		case workflow.CommandRunCompleted, workflow.CommandRunFailed, workflow.CommandRunCancelled:
			f := Finished{
				RunID:         runID,
				Status:        next.Status,
				Outputs:       c.Outputs,
				Error:         c.Error,
				Reason:        c.Reason,
				FinallyErrors: c.FinallyErrors,
			}
			if err := s.onFinished(ctx, tx, f); err != nil {
				return fmt.Errorf("postgres: on finished: %w", err)
			}
		}
	}
	return nil
}

// updateRun stores a run's new state and returns its event sequence.
func (s *Store) updateRun(
	ctx context.Context, tx pgx.Tx, runID string, next *workflow.State, at time.Time,
) (int64, error) {
	stateJSON, err := json.Marshal(next)
	if err != nil {
		return 0, err
	}
	var finished *time.Time
	if !next.FinishedAt.IsZero() {
		finished = &next.FinishedAt
	}
	var seq int64
	if err := tx.QueryRow(ctx, s.sql(`
		UPDATE {{schema}}.runs
		SET state = $2, status = $3, error = $4, outputs = $5, finished_at = $6,
		    updated_at = $7, event_seq = event_seq + 1
		WHERE id = $1
		RETURNING event_seq`),
		runID, stateJSON, string(next.Status), jsonOrNil(next.Error), jsonOrNil(next.Outputs),
		finished, at,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("postgres: update run: %w", err)
	}
	return seq, nil
}

// Run is a run as stored.
type Run struct {
	ID       string
	Workflow string
	// DefinitionHash is the hash of the definition the run started with.
	DefinitionHash string
	Status         workflow.RunStatus
	State          *workflow.State
	Metadata       map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
	FinishedAt     *time.Time
}

// GetRun reads a run.
func (s *Store) GetRun(ctx context.Context, runID string) (*Run, error) {
	var (
		r      Run
		status string
		state  []byte
	)
	err := s.pool.QueryRow(ctx, s.sql(`
		SELECT id, workflow, definition_hash, status, state, metadata, created_at, updated_at, finished_at
		FROM {{schema}}.runs WHERE id = $1`), runID).Scan(
		&r.ID, &r.Workflow, &r.DefinitionHash, &status, &state, &r.Metadata,
		&r.CreatedAt, &r.UpdatedAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Status = workflow.RunStatus(status)
	if err := json.Unmarshal(state, &r.State); err != nil {
		return nil, err
	}
	return &r, nil
}

// StepStatus is what became of a step attempt.
type StepStatus string

const (
	// StepDispatched: the attempt was dispatched and has not settled.
	StepDispatched StepStatus = "dispatched"
	StepCompleted  StepStatus = "completed"
	StepFailed     StepStatus = "failed"
	// StepCancelled: the run no longer wanted the attempt, because it was
	// cancelled or failed while the attempt was in flight. An attempt that
	// timed out is failed, with type timeout.
	StepCancelled StepStatus = "cancelled"
)

// StepRecord is one step attempt as stored. Unlike the run's state, which
// keeps only what the run can still read, the steps table keeps every
// attempt: it is the run's history.
type StepRecord struct {
	StepID       workflow.StepID
	Step         string
	StepKind     string // "agent", "action", ...: the step's kind
	Index        int
	Attempt      int
	Status       StepStatus
	Params       map[string]any
	Item         any
	Output       any
	Error        *workflow.StepError
	NotBefore    *time.Time
	DispatchedAt time.Time
	SettledAt    *time.Time
}

// Steps lists a run's step attempts in dispatch order.
func (s *Store) Steps(ctx context.Context, runID string) ([]StepRecord, error) {
	rows, err := s.pool.Query(ctx, s.sql(`
		SELECT step_id, step, kind, item_index, attempt, status, params, item, output, error,
		       not_before, dispatched_at, settled_at
		FROM {{schema}}.steps WHERE run_id = $1 ORDER BY dispatched_at, step_id`), runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (StepRecord, error) {
		var (
			r          StepRecord
			id, status string
		)
		err := row.Scan(&id, &r.Step, &r.StepKind, &r.Index, &r.Attempt, &status, &r.Params, &r.Item,
			&r.Output, &r.Error, &r.NotBefore, &r.DispatchedAt, &r.SettledAt)
		r.StepID, r.Status = workflow.StepID(id), StepStatus(status)
		return r, err
	})
}

// Events lists the events applied to a run, in order.
func (s *Store) Events(ctx context.Context, runID string) ([]workflow.Event, error) {
	rows, err := s.pool.Query(ctx,
		s.sql(`SELECT event FROM {{schema}}.events WHERE run_id = $1 ORDER BY seq`), runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (workflow.Event, error) {
		var ev workflow.Event
		err := row.Scan(&ev)
		return ev, err
	})
}

func jsonOrNil(v any) any {
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

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
