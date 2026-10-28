// Package postgres is a durable host for workflows on Postgres.
//
// No process holds a run. Each event advances a run in one short
// transaction: lock the run row, call [workflow.Apply], store the new state,
// upsert step records, and write the commands to an outbox (the tasks
// table). Workers claim tasks after commit, run the step handlers, and
// deliver the results as new events. Any worker can advance any run, a run
// can wait for days between events, and a crash loses nothing: an expired
// lease makes the task claimable again.
//
// Every *Tx method takes the caller's transaction, so an application can
// start a run, or deliver a step result, atomically with its own writes.
// The methods without Tx open their own transaction.
package postgres

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

var (
	ErrRunNotFound = errors.New("postgres: run not found")
	ErrRunExists   = errors.New("postgres: a run with this id exists for a different definition")
)

var schemaRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Store persists runs and their outbox.
type Store struct {
	pool   *pgxpool.Pool
	kinds  workflow.Kinds
	schema string
	limits workflow.Limits

	onFinished func(ctx context.Context, tx pgx.Tx, f Finished) error

	mu   sync.Mutex
	defs map[string]*workflow.Definition
}

// Option configures a Store.
type Option func(*Store)

// WithSchema puts the tables in a schema other than "workflow".
func WithSchema(name string) Option { return func(s *Store) { s.schema = name } }

// WithLimits sets the state limits for every definition the store loads.
func WithLimits(l workflow.Limits) Option { return func(s *Store) { s.limits = l } }

// WithOnFinished calls fn in the transaction that moves a run to completed,
// failed or cancelled, once per run. Use it to record the outcome or enqueue
// a notification atomically with it. An error from fn rolls the event back,
// so return one only when the transaction itself failed; a worker retries
// its settle, and the event is applied once fn succeeds.
func WithOnFinished(fn func(ctx context.Context, tx pgx.Tx, f Finished) error) Option {
	return func(s *Store) { s.onFinished = fn }
}

// Finished describes a run that has just reached a terminal status.
type Finished struct {
	RunID   string
	Status  workflow.RunStatus // completed, failed or cancelled
	Outputs map[string]any     // completed
	Error   *workflow.RunError // failed
	Reason  string             // cancelled
}

// New returns a Store. kinds validates every definition the store loads.
func New(pool *pgxpool.Pool, kinds workflow.Kinds, opts ...Option) (*Store, error) {
	s := &Store{pool: pool, kinds: kinds, schema: "workflow", defs: map[string]*workflow.Definition{}}
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
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLockKey(s.schema)); err != nil {
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
		if err := tx.QueryRow(ctx, s.sql(`SELECT coalesce(max(version), 0) FROM {{schema}}.schema_migrations`)).
			Scan(&version); err != nil {
			return fmt.Errorf("postgres: migrate: %w", err)
		}
		if version > len(migrations) {
			return fmt.Errorf("postgres: schema %s is at version %d; this package knows %d", s.schema, version, len(migrations))
		}
		for v := version; v < len(migrations); v++ {
			if _, err := tx.Exec(ctx, s.sql(migrations[v])); err != nil {
				return fmt.Errorf("postgres: migration %d: %w", v+1, err)
			}
			if _, err := tx.Exec(ctx, s.sql(`INSERT INTO {{schema}}.schema_migrations (version) VALUES ($1)`), v+1); err != nil {
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

// NotifyChannel is the channel the store notifies when it writes tasks.
func (s *Store) NotifyChannel() string { return s.schema + "_tasks" }

// RunsChannel is the channel the store notifies, on commit, when a run's
// status changes. The payload is JSON: {"run_id": "...", "status": "failed"}.
// Notifications are best effort (a listener that is not connected misses
// them); read the run with GetRun, or use WithOnFinished for a guarantee.
func (s *Store) RunsChannel() string { return s.schema + "_runs" }

// Result is what an event did to a run.
type Result struct {
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
	// ID identifies the run. Derive it from the trigger (for example
	// "pr-42-abc123") to make starting idempotent; empty generates one.
	ID         string
	Definition *workflow.Definition
	Inputs     map[string]any
	Trigger    map[string]any
	Metadata   map[string]any
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
	def := s.withLimits(run.Definition)
	if run.ID == "" {
		run.ID = newID()
	}
	now := time.Now().UTC()
	ev := workflow.Event{At: now, Kind: workflow.EventStart, Inputs: run.Inputs, Trigger: run.Trigger}
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
	meta := run.Metadata
	if meta == nil {
		meta = map[string]any{}
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
		run.ID, def.Hash(), def.Name, string(st.Status), stateJSON, jsonOrNil(st.Error), jsonOrNil(st.Outputs),
		meta, now, finished)
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
		return &Result{State: existing}, nil
	}
	if err := s.persist(ctx, tx, run.ID, nil, st, cmds, ev); err != nil {
		return nil, err
	}
	s.cache(def)
	return &Result{State: st, Commands: cmds, Applied: true}, nil
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
// row, calls Apply, and stores the state, step records and tasks. The
// commands in the result are already in the outbox; they run after the
// caller commits.
//
// Delivering an event twice is safe. A duplicate changes nothing, returns no
// commands and has Applied false.
func (s *Store) DeliverTx(ctx context.Context, tx pgx.Tx, runID string, ev workflow.Event) (*Result, error) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	st, hash, err := s.lockRun(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	def, err := s.definition(ctx, tx, hash)
	if err != nil {
		return nil, err
	}
	next, cmds, err := workflow.Apply(def, st, ev)
	if err != nil {
		return nil, err
	}
	if next == st {
		return &Result{State: st}, nil // nothing changed: a duplicate or late event
	}
	if err := s.persist(ctx, tx, runID, st, next, cmds, ev); err != nil {
		return nil, err
	}
	return &Result{State: next, Commands: cmds, Applied: true}, nil
}

// Cancel cancels a run in its own transaction. Applied is false if the run
// had already finished.
func (s *Store) Cancel(ctx context.Context, runID, reason string) (*Result, error) {
	return s.Deliver(ctx, runID, workflow.Event{Kind: workflow.EventCancel, Reason: reason})
}

func (s *Store) lockRun(ctx context.Context, tx pgx.Tx, runID string) (*workflow.State, string, error) {
	var (
		data []byte
		hash string
	)
	err := tx.QueryRow(ctx, s.sql(`SELECT state, definition_hash FROM {{schema}}.runs WHERE id = $1 FOR UPDATE`),
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

// withLimits returns the definition the store runs for def: the cached one
// for its hash, or a copy with the store's limits. Limits are not part of
// the document, so a definition loaded from the database by another process
// gets the store's limits; the one passed to Start must behave the same.
// The caller's Definition is not modified.
func (s *Store) withLimits(def *workflow.Definition) *workflow.Definition {
	s.mu.Lock()
	cached := s.defs[def.Hash()]
	s.mu.Unlock()
	if cached != nil {
		return cached
	}
	c := *def
	c.Limits = s.limits
	return &c
}

func (s *Store) cache(def *workflow.Definition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.defs[def.Hash()]; !ok {
		s.defs[def.Hash()] = def
	}
}

func (s *Store) definition(ctx context.Context, tx pgx.Tx, hash string) (*workflow.Definition, error) {
	s.mu.Lock()
	def := s.defs[hash]
	s.mu.Unlock()
	if def != nil {
		return def, nil
	}
	var doc []byte
	if err := tx.QueryRow(ctx, s.sql(`SELECT document FROM {{schema}}.definitions WHERE hash = $1`),
		hash).Scan(&doc); err != nil {
		return nil, fmt.Errorf("postgres: load definition %s: %w", hash, err)
	}
	def, err := workflow.Load(doc, s.kinds)
	if err != nil {
		return nil, err
	}
	def.Limits = s.limits
	s.cache(def)
	return def, nil
}

// persist writes the new state, the event, the step records and the outbox
// tasks. prev is nil for Start, whose run row StartTx has already inserted.
func (s *Store) persist(ctx context.Context, tx pgx.Tx, runID string, prev, next *workflow.State,
	cmds []workflow.Command, ev workflow.Event) (err error) {
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
		if status, output, stepErr := attemptOutcome(next, ev); status != "" {
			if _, err := tx.Exec(ctx, s.sql(`
				UPDATE {{schema}}.steps SET status = $3, output = $4, error = $5, settled_at = $6
				WHERE run_id = $1 AND step_id = $2 AND status = 'dispatched'`),
				runID, string(ev.StepID), status, jsonOrNil(output), jsonOrNil(stepErr), ev.At); err != nil {
				return fmt.Errorf("postgres: settle step: %w", err)
			}
		}
		if u := next.Unit(ev.StepID); u == nil || u.Status != workflow.UnitRunning {
			// The attempt settled, by whatever path: neither its step task
			// (a hand-off whose worker died before marking it done, or one
			// still running) nor its timeout timer is wanted any more.
			if _, err := tx.Exec(ctx, s.sql(`
				DELETE FROM {{schema}}.tasks WHERE run_id = $1 AND step_id = $2`),
				runID, string(ev.StepID)); err != nil {
				return fmt.Errorf("postgres: delete settled tasks: %w", err)
			}
		}
	}

	wroteTasks := false
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
			runAt := c.NotBefore
			if runAt.IsZero() || runAt.Before(ev.At) {
				runAt = ev.At
			}
			if _, err := tx.Exec(ctx, s.sql(`
				INSERT INTO {{schema}}.tasks (run_id, step_id, kind, step_kind, command, run_at)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (run_id, step_id, kind) DO NOTHING`),
				runID, string(c.StepID), kind, c.StepKind, cmdJSON, runAt); err != nil {
				return fmt.Errorf("postgres: insert task: %w", err)
			}
			wroteTasks = true
		case workflow.CommandRunCompleted, workflow.CommandRunFailed, workflow.CommandRunCancelled:
			if s.onFinished != nil {
				f := Finished{RunID: runID, Status: next.Status, Outputs: c.Outputs, Error: c.Error, Reason: c.Reason}
				if err := s.onFinished(ctx, tx, f); err != nil {
					return fmt.Errorf("postgres: on finished: %w", err)
				}
			}
		case workflow.CommandCancelStep:
			if _, err := tx.Exec(ctx, s.sql(`
				DELETE FROM {{schema}}.tasks WHERE run_id = $1 AND step_id = $2`), runID, string(c.StepID)); err != nil {
				return fmt.Errorf("postgres: delete task: %w", err)
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
		if _, err := tx.Exec(ctx, s.sql(`DELETE FROM {{schema}}.tasks WHERE run_id = $1`), runID); err != nil {
			return fmt.Errorf("postgres: clear tasks: %w", err)
		}
	}
	if prev == nil || prev.Status != next.Status {
		payload, _ := json.Marshal(map[string]string{"run_id": runID, "status": string(next.Status)})
		if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, s.RunsChannel(), string(payload)); err != nil {
			return fmt.Errorf("postgres: notify: %w", err)
		}
	}
	if wroteTasks {
		if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, s.NotifyChannel(), runID); err != nil {
			return fmt.Errorf("postgres: notify: %w", err)
		}
	}
	return nil
}

// attemptOutcome is what became of the attempt an applied event reports on,
// as the run sees it: the accepted output, or the error Apply recorded
// (which wins over a reported result it rejected). An attempt that failed
// and was retried is no longer in the state; its replacement carries the
// error.
func attemptOutcome(next *workflow.State, ev workflow.Event) (status string, output any, stepErr *workflow.StepError) {
	if u := next.Unit(ev.StepID); u != nil {
		switch u.Status {
		case workflow.UnitCompleted:
			return "completed", u.Output, nil
		case workflow.UnitFailed:
			return "failed", nil, u.Error
		}
		return "", nil, nil
	}
	if r := next.Unit(nextAttempt(ev.StepID)); r != nil && r.Error != nil {
		return "failed", nil, r.Error
	}
	switch ev.Kind {
	case workflow.EventStepFailed:
		return "failed", nil, ev.Error
	case workflow.EventTimerFired:
		return "failed", nil, &workflow.StepError{Type: workflow.ErrorTypeTimeout, Message: "step timed out"}
	}
	return "", nil, nil
}

// nextAttempt is the StepID of the attempt after id.
func nextAttempt(id workflow.StepID) workflow.StepID {
	s := string(id)
	i := strings.LastIndexByte(s, '#')
	if i < 0 {
		return ""
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return ""
	}
	return workflow.StepID(s[:i+1] + strconv.Itoa(n+1))
}

// updateRun stores a run's new state and returns its event sequence.
func (s *Store) updateRun(ctx context.Context, tx pgx.Tx, runID string, next *workflow.State, at time.Time) (int64, error) {
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
		runID, stateJSON, string(next.Status), jsonOrNil(next.Error), jsonOrNil(next.Outputs), finished, at,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("postgres: update run: %w", err)
	}
	return seq, nil
}

// Run is a run as stored.
type Run struct {
	ID         string
	Workflow   string
	Definition string
	Status     workflow.RunStatus
	State      *workflow.State
	Metadata   map[string]any
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
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
		&r.ID, &r.Workflow, &r.Definition, &status, &state, &r.Metadata, &r.CreatedAt, &r.UpdatedAt, &r.FinishedAt)
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

// StepRecord is one step attempt as stored.
type StepRecord struct {
	StepID       workflow.StepID
	Step         string
	Kind         string
	Index        int
	Attempt      int
	Status       string
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
			r  StepRecord
			id string
		)
		err := row.Scan(&id, &r.Step, &r.Kind, &r.Index, &r.Attempt, &r.Status, &r.Params, &r.Item,
			&r.Output, &r.Error, &r.NotBefore, &r.DispatchedAt, &r.SettledAt)
		r.StepID = workflow.StepID(id)
		return r, err
	})
}

// Events lists the events applied to a run, in order.
func (s *Store) Events(ctx context.Context, runID string) ([]workflow.Event, error) {
	rows, err := s.pool.Query(ctx, s.sql(`SELECT event FROM {{schema}}.events WHERE run_id = $1 ORDER BY seq`), runID)
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

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
