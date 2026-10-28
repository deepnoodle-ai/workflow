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
	"regexp"
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

	mu   sync.Mutex
	defs map[string]*workflow.Definition
}

// Option configures a Store.
type Option func(*Store)

// WithSchema puts the tables in a schema other than "workflow".
func WithSchema(name string) Option { return func(s *Store) { s.schema = name } }

// WithLimits sets the state limits for every definition the store loads.
func WithLimits(l workflow.Limits) Option { return func(s *Store) { s.limits = l } }

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

// Migrate creates the schema and tables if they do not exist.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, s.sql(schemaSQL))
	return err
}

func (s *Store) sql(q string) string { return strings.ReplaceAll(q, "{{schema}}", s.schema) }

// NotifyChannel is the channel the store notifies when it writes tasks.
func (s *Store) NotifyChannel() string { return s.schema + "_tasks" }

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
func (s *Store) Start(ctx context.Context, run NewRun) (*workflow.State, error) {
	var st *workflow.State
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		st, _, err = s.StartTx(ctx, tx, run)
		return err
	})
	return st, err
}

// StartTx starts a run in the caller's transaction. Starting a run whose ID
// exists with the same definition returns its state and no commands.
func (s *Store) StartTx(ctx context.Context, tx pgx.Tx, run NewRun) (*workflow.State, []workflow.Command, error) {
	def := run.Definition
	if run.ID == "" {
		run.ID = newID()
	}
	doc, err := json.Marshal(def)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, s.sql(`
		INSERT INTO {{schema}}.definitions (hash, name, document) VALUES ($1, $2, $3)
		ON CONFLICT (hash) DO NOTHING`), def.Hash(), def.Name, doc); err != nil {
		return nil, nil, fmt.Errorf("postgres: store definition: %w", err)
	}
	s.cache(def)

	now := time.Now().UTC()
	st, cmds, err := workflow.Apply(def, nil, workflow.Event{
		At: now, Kind: workflow.EventStart, Inputs: run.Inputs, Trigger: run.Trigger,
	})
	if err != nil {
		return nil, nil, err
	}
	stateJSON, err := json.Marshal(st)
	if err != nil {
		return nil, nil, err
	}
	meta := run.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	tag, err := tx.Exec(ctx, s.sql(`
		INSERT INTO {{schema}}.runs (id, definition_hash, workflow, status, state, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (id) DO NOTHING`),
		run.ID, def.Hash(), def.Name, string(st.Status), stateJSON, meta, now)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: insert run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		existing, hash, err := s.lockRun(ctx, tx, run.ID)
		if err != nil {
			return nil, nil, err
		}
		if hash != def.Hash() {
			return nil, nil, ErrRunExists
		}
		return existing, nil, nil
	}
	if err := s.persist(ctx, tx, run.ID, nil, st, cmds, workflow.Event{At: now, Kind: workflow.EventStart,
		Inputs: run.Inputs, Trigger: run.Trigger}); err != nil {
		return nil, nil, err
	}
	return st, cmds, nil
}

// Deliver applies an event in its own transaction.
func (s *Store) Deliver(ctx context.Context, runID string, ev workflow.Event) (*workflow.State, error) {
	var st *workflow.State
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		st, _, err = s.DeliverTx(ctx, tx, runID, ev)
		return err
	})
	return st, err
}

// DeliverTx applies an event in the caller's transaction: it locks the run
// row, calls Apply, and stores the state, step records and tasks. It
// returns the commands Apply produced, already written to the outbox.
//
// Delivering an event twice is safe. A duplicate changes nothing and returns
// no commands.
func (s *Store) DeliverTx(ctx context.Context, tx pgx.Tx, runID string, ev workflow.Event) (*workflow.State, []workflow.Command, error) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	st, hash, err := s.lockRun(ctx, tx, runID)
	if err != nil {
		return nil, nil, err
	}
	def, err := s.definition(ctx, tx, hash)
	if err != nil {
		return nil, nil, err
	}
	next, cmds, err := workflow.Apply(def, st, ev)
	if err != nil {
		return st, nil, err
	}
	if next == st {
		return st, nil, nil // nothing changed: a duplicate or late event
	}
	if err := s.persist(ctx, tx, runID, st, next, cmds, ev); err != nil {
		return nil, nil, err
	}
	return next, cmds, nil
}

// Cancel cancels a run in its own transaction.
func (s *Store) Cancel(ctx context.Context, runID, reason string) error {
	_, err := s.Deliver(ctx, runID, workflow.Event{Kind: workflow.EventCancel, Reason: reason})
	return err
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
	def, err := workflow.Parse(doc)
	if err != nil {
		return nil, err
	}
	def.Limits = s.limits
	if err := def.Validate(s.kinds); err != nil {
		return nil, err
	}
	s.cache(def)
	return def, nil
}

// persist writes the new state, the event, the step records and the outbox
// tasks. prev is nil for Start.
func (s *Store) persist(ctx context.Context, tx pgx.Tx, runID string, prev, next *workflow.State,
	cmds []workflow.Command, ev workflow.Event) error {
	stateJSON, err := json.Marshal(next)
	if err != nil {
		return err
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
		runID, stateJSON, string(next.Status), jsonOrNil(next.Error), jsonOrNil(next.Outputs), finished, ev.At,
	).Scan(&seq); err != nil {
		return fmt.Errorf("postgres: update run: %w", err)
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
		status, output, stepErr := "", any(nil), (*workflow.StepError)(nil)
		switch ev.Kind {
		case workflow.EventStepCompleted:
			status, output = "completed", ev.Output
		case workflow.EventStepFailed:
			status, stepErr = "failed", ev.Error
		case workflow.EventTimerFired:
			if u := next.Unit(ev.StepID); u != nil && u.Status == workflow.UnitCompleted {
				status = "completed" // a sleep
			}
		}
		// The run's view wins where Apply rejected a reported result.
		if u := next.Unit(ev.StepID); u != nil && u.Status == workflow.UnitFailed {
			status, output, stepErr = "failed", nil, u.Error
		}
		if status != "" {
			if _, err := tx.Exec(ctx, s.sql(`
				UPDATE {{schema}}.steps SET status = $3, output = $4, error = $5, settled_at = $6
				WHERE run_id = $1 AND step_id = $2 AND status = 'dispatched'`),
				runID, string(ev.StepID), status, jsonOrNil(output), jsonOrNil(stepErr), ev.At); err != nil {
				return fmt.Errorf("postgres: settle step: %w", err)
			}
		}
		if ev.Kind != workflow.EventTimerFired {
			// The attempt settled; its timeout timer is no longer needed.
			if _, err := tx.Exec(ctx, s.sql(`
				DELETE FROM {{schema}}.tasks WHERE run_id = $1 AND step_id = $2 AND kind = 'timer'`),
				runID, string(ev.StepID)); err != nil {
				return fmt.Errorf("postgres: delete timer: %w", err)
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
	if wroteTasks {
		if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, s.NotifyChannel(), runID); err != nil {
			return fmt.Errorf("postgres: notify: %w", err)
		}
	}
	return nil
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
