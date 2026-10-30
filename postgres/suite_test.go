package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/deepnoodle-ai/workflow/internal/hosttest"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// TestSuite runs the durable host suite on Postgres.
func TestSuite(t *testing.T) { hosttest.Run(t, harness{}) }

// TestSuiteSQLHook runs the suite again on stores whose only finish hook is
// WithOnFinishedSQL, the configuration for code that also runs on SQLite:
// every transaction the store opens goes through database/sql.
func TestSuiteSQLHook(t *testing.T) { hosttest.Run(t, harness{sqlHook: true}) }

// harness is the suite's view of Postgres: each Open is a new schema.
type harness struct {
	// sqlHook gives every store only a WithOnFinishedSQL hook: the
	// suite's OnFinished, or one that does nothing.
	sqlHook bool
}

func (h harness) Name() string {
	if h.sqlHook {
		return "postgres-sqlhook"
	}
	return "postgres"
}

// Caps: a run lock is a row lock, and a cancelled context ends a lock wait.
func (harness) Caps() hosttest.Caps {
	return hosttest.Caps{RowLocks: true, CancelEndsLockWait: true}
}

// pool opens a pool on the test database, or skips t.
func pool(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s not set", dsnEnv)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.MaxConns = maxConns
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	return p
}

// storeWith returns a Store on p and schema with the suite's options.
func (h harness) storeWith(
	t *testing.T, p *pgxpool.Pool, schema string, kinds workflow.Kinds, opt hosttest.Options,
) *Store {
	t.Helper()
	opts := []Option{WithSchema(schema), WithLimits(opt.Limits)}
	if opt.NewRunID != nil {
		opts = append(opts, WithRunIDs(opt.NewRunID))
	}
	if h.sqlHook && opt.OnFinishedSQL == nil {
		fn := opt.OnFinished
		opt.OnFinished, opt.OnFinishedSQL = nil, func(ctx context.Context, _ SQLTx, f Finished) error {
			if fn == nil {
				return nil
			}
			return fn(ctx, f)
		}
	}
	if fn := opt.OnFinished; fn != nil {
		opts = append(opts, WithOnFinished(func(ctx context.Context, _ pgx.Tx, f Finished) error {
			return fn(ctx, f)
		}))
	}
	if fn := opt.OnFinishedSQL; fn != nil {
		opts = append(opts, WithOnFinishedSQL(fn))
	}
	s, err := New(p, kinds, opts...)
	require.NoError(t, err)
	return s
}

// env is the suite's handle on s.
func env(s *Store) *hosttest.Env {
	return &hosttest.Env{Backend: s.backend, Store: s.core, Handle: s}
}

func store(e *hosttest.Env) *Store { return e.Handle.(*Store) }

func (h harness) Open(t *testing.T, kinds workflow.Kinds, opt hosttest.Options) *hosttest.Env {
	t.Helper()
	p := pool(t, 32)
	schema := "t_" + uuid.NewV4().String()[:8]
	s := h.storeWith(t, p, schema, kinds, opt)
	require.NoError(t, s.Migrate(context.Background()))
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		p.Close()
	})
	return env(s)
}

func (h harness) Reopen(
	t *testing.T, e *hosttest.Env, kinds workflow.Kinds, opt hosttest.Options,
) *hosttest.Env {
	t.Helper()
	p := pool(t, 16)
	t.Cleanup(p.Close)
	return env(h.storeWith(t, p, store(e).schema, kinds, opt))
}

func (harness) Jobs(t *testing.T, e *hosttest.Env, runID string) []hosttest.JobRow {
	t.Helper()
	s := store(e)
	rows, err := s.pool.Query(context.Background(), s.sql(`
		SELECT id, step_id, kind, step_kind, claims, fence, coalesce(lease_owner, ''), lease_expires, run_at
		FROM {{schema}}.jobs WHERE run_id = $1 ORDER BY step_id, kind`), runID)
	require.NoError(t, err)
	jobs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hosttest.JobRow, error) {
		var (
			j     hosttest.JobRow
			lease *time.Time
		)
		err := row.Scan(&j.ID, &j.StepID, &j.Kind, &j.StepKind, &j.Claims, &j.Fence,
			&j.LeaseOwner, &lease, &j.RunAt)
		if lease != nil {
			j.LeaseExpires = *lease
		}
		return j, err
	})
	require.NoError(t, err)
	return jobs
}

func (harness) Count(t *testing.T, e *hosttest.Env, table, runID string) int {
	t.Helper()
	s := store(e)
	switch table {
	case "definitions", "runs", "steps", "events", "jobs":
	default:
		t.Fatalf("no table %q", table)
	}
	col := "run_id"
	switch table {
	case "runs":
		col = "id"
	case "definitions":
		require.Equal(t, "", runID, "definitions belong to no run")
	}
	q := `SELECT count(*) FROM {{schema}}.` + table
	args := []any{}
	if runID != "" {
		q += ` WHERE ` + col + ` = $1`
		args = append(args, runID)
	}
	var n int
	require.NoError(t, s.pool.QueryRow(context.Background(), s.sql(q), args...).Scan(&n))
	return n
}

func (harness) ExpireLeases(t *testing.T, e *hosttest.Env, runID string) {
	t.Helper()
	s := store(e)
	_, err := s.pool.Exec(context.Background(), s.sql(`
		UPDATE {{schema}}.jobs SET lease_expires = now() - interval '1 second'
		WHERE run_id = $1`), runID)
	require.NoError(t, err)
}

func (harness) Backdate(t *testing.T, e *hosttest.Env, runID string, ago time.Duration) {
	t.Helper()
	s := store(e)
	_, err := s.pool.Exec(context.Background(), s.sql(`
		UPDATE {{schema}}.jobs SET run_at = now() - make_interval(secs => $2)
		WHERE run_id = $1`), runID, ago.Seconds())
	require.NoError(t, err)
}

func (harness) HoldRunLock(t *testing.T, e *hosttest.Env, runID string) func() {
	t.Helper()
	s := store(e)
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	// Registered before the lock, so a failed lock still returns the
	// connection: the pool's Close waits for it.
	release := sync.OnceFunc(func() { _ = tx.Rollback(ctx) })
	t.Cleanup(release)

	_, err = tx.Exec(ctx, s.sql(`SELECT 1 FROM {{schema}}.runs WHERE id = $1 FOR UPDATE`), runID)
	require.NoError(t, err)
	return release
}

func (harness) CallerTx(
	t *testing.T, e *hosttest.Env,
) (durable.SQLTx, hostcore.Tx, func() error, func() error) {
	t.Helper()
	s := store(e)
	db := stdlib.OpenDBFromPool(s.pool)
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("roll back the caller's transaction: %v", err)
		}
		_ = db.Close()
	})
	c, err := s.sqlConn(tx)
	require.NoError(t, err, fmt.Sprintf("%T", tx))
	return tx, s.txOn(c), tx.Commit, tx.Rollback
}

// suiteWorker is a Worker as the suite drives it.
type suiteWorker struct{ *Worker }

// Core syncs the Worker, as each of its methods does, and returns its
// shared worker.
func (w suiteWorker) Core() *hostcore.Worker {
	w.defaults()
	return w.core
}

// Worker returns a Worker whose sync takes sw's fields and hooks first,
// then runs Worker.sync, then writes the defaults it chose back to sw.
func (harness) Worker(sw *hosttest.Worker) hosttest.BackendWorker {
	w := &Worker{}
	w.defaults()
	sync := w.core.Sync
	w.core.Sync = func(c *hostcore.Worker, defaults func()) {
		// Write Store only if it changed: Run reads it outside the lock
		// that Sync runs under, and Active syncs while Run starts.
		var st *Store
		if sw.Store != nil {
			st = store(sw.Store)
		}
		if w.Store != st {
			w.Store = st
		}
		w.Handlers = sw.Handlers
		w.ID = sw.ID
		w.Concurrency = sw.Concurrency
		w.Lease = sw.Lease
		w.Poll = sw.Poll
		w.MaxClaims = sw.MaxClaims
		w.Logger = sw.Logger
		w.Observer = sw.Observer
		w.MayClaim = sw.MayClaim
		h := sw.Hooks()
		w.finishHook = h.Finish
		w.heartbeatHook = h.Heartbeat
		w.settleHook = h.Settle
		w.handlerDone = h.HandlerDone
		w.beforeClaim = h.BeforeClaim
		w.claimHook = h.Claim
		w.releaseHook = h.Release

		sync(c, defaults)

		// Write only what changed: a test may read these fields while the
		// worker runs.
		if sw.ID != w.ID {
			sw.ID = w.ID
		}
		if sw.Concurrency != w.Concurrency {
			sw.Concurrency = w.Concurrency
		}
		if sw.Lease != w.Lease {
			sw.Lease = w.Lease
		}
		if sw.Poll != w.Poll {
			sw.Poll = w.Poll
		}
		if sw.MaxClaims != w.MaxClaims {
			sw.MaxClaims = w.MaxClaims
		}
		if sw.Logger != w.Logger {
			sw.Logger = w.Logger
		}
	}
	return suiteWorker{w}
}

// TestSuiteWorkerHasTheWorkerFields: the suite's Worker has every field
// and test hook of Worker, so that harness.Worker can copy each one.
// ListenConfig is Postgres's own.
func TestSuiteWorkerHasTheWorkerFields(t *testing.T) {
	names := func(v any, skip ...string) []string {
		var out []string
		for f := range reflect.TypeOf(v).Fields() {
			if !slices.Contains(skip, f.Name) {
				out = append(out, f.Name)
			}
		}
		slices.Sort(out)
		return out
	}
	require.Equal(t, names(Worker{}, "ListenConfig", "core"), names(hosttest.Worker{}, "backend"))
}

func (harness) TxOn(e *hosttest.Env, tx durable.SQLTx) (hostcore.Tx, error) {
	s := store(e)
	c, err := s.sqlConn(tx)
	if err != nil {
		return nil, err
	}
	return s.txOn(c), nil
}
