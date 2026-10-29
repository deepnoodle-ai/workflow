package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/deepnoodle-ai/workflow/internal/hosttest"
	"github.com/deepnoodle-ai/workflow/internal/require"
	_ "modernc.org/sqlite"
)

// modernc runs the suite on modernc.org/sqlite, which ends a wait for the
// lock when its context ends. Its transactions start deferred, so the
// store's write-first rule is what keeps SQLITE_BUSY away.
var modernc = harness{
	name:   "modernc",
	driver: "sqlite",
	dsn:    "file:%s?_busy_timeout=5000&_journal_mode=WAL",
	caps:   hosttest.Caps{CancelEndsLockWait: true, BusyTimeout: 5 * time.Second},
}

// TestSuite runs the durable host suite on SQLite with modernc.
func TestSuite(t *testing.T) { hosttest.Run(t, modernc) }

// harness is the suite's view of SQLite on one driver: each Open is a new
// file.
type harness struct {
	name, driver string
	dsn          string // %s is the file
	caps         hosttest.Caps
}

func (h harness) Name() string        { return h.name }
func (h harness) Caps() hosttest.Caps { return h.caps }

// env is what the suite's Env holds: the store, its file, and the file's
// count of SQLITE_BUSY errors.
type env struct {
	s    *Store
	path string
	busy *busyCount
}

func store(e *hosttest.Env) *Store { return e.Handle.(*env).s }

func (h harness) openDB(t *testing.T, path string, busy *busyCount) *sql.DB {
	t.Helper()
	db, err := openCounted(h.driver, fmt.Sprintf(h.dsn, path), busy)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (h harness) store(
	t *testing.T, path string, busy *busyCount, kinds workflow.Kinds, opt hosttest.Options,
) *hosttest.Env {
	t.Helper()
	opts := []Option{WithLimits(opt.Limits)}
	if opt.NewRunID != nil {
		opts = append(opts, WithRunIDs(opt.NewRunID))
	}
	if fn := opt.OnFinished; fn != nil {
		opts = append(opts, WithOnFinishedSQL(func(ctx context.Context, _ SQLTx, f Finished) error {
			return fn(ctx, f)
		}))
	}
	if fn := opt.OnFinishedSQL; fn != nil {
		opts = append(opts, WithOnFinishedSQL(fn))
	}
	s, err := New(h.openDB(t, path, busy), kinds, opts...)
	require.NoError(t, err)
	return &hosttest.Env{Backend: s.backend, Store: s.core, Handle: &env{s: s, path: path, busy: busy}}
}

func (h harness) Open(t *testing.T, kinds workflow.Kinds, opt hosttest.Options) *hosttest.Env {
	t.Helper()
	busy := &busyCount{}
	// Registered first, so it runs last: after the test's workers stop.
	t.Cleanup(func() {
		if n := busy.n.Load(); n > 0 && !busy.held.Load() {
			t.Errorf("%d SQLITE_BUSY errors reached the store; with a busy timeout, none "+
				"should (PRD 007, UC-3)", n)
		}
	})
	e := h.store(t, filepath.Join(t.TempDir(), "workflow.db"), busy, kinds, opt)
	require.NoError(t, store(e).Migrate(context.Background()))
	return e
}

func (h harness) Reopen(
	t *testing.T, e *hosttest.Env, kinds workflow.Kinds, opt hosttest.Options,
) *hosttest.Env {
	t.Helper()
	return h.store(t, e.Handle.(*env).path, e.Handle.(*env).busy, kinds, opt)
}

func (harness) Jobs(t *testing.T, e *hosttest.Env, runID string) []hosttest.JobRow {
	t.Helper()
	s := store(e)
	rows, err := s.query(context.Background(), s.db, `
		SELECT id, step_id, kind, step_kind, claims, fence, coalesce(lease_owner, ''), lease_expires, run_at
		FROM {{p}}jobs WHERE run_id = ?1 ORDER BY step_id, kind`, runID)
	require.NoError(t, err)
	defer rows.Close()
	var jobs []hosttest.JobRow
	for rows.Next() {
		var (
			j     hosttest.JobRow
			lease sql.NullString
			runAt string
		)
		require.NoError(t, rows.Scan(&j.ID, &j.StepID, &j.Kind, &j.StepKind, &j.Claims, &j.Fence,
			&j.LeaseOwner, &lease, &runAt))
		j.RunAt, err = parseTime(runAt)
		require.NoError(t, err)
		if lease.Valid {
			j.LeaseExpires, err = parseTime(lease.String)
			require.NoError(t, err)
		}
		jobs = append(jobs, j)
	}
	require.NoError(t, rows.Err())
	return jobs
}

func (harness) Count(t *testing.T, e *hosttest.Env, table, runID string) int {
	t.Helper()
	s := store(e)
	col := "run_id"
	switch table {
	case "steps", "events", "jobs":
	case "runs":
		col = "id"
	case "definitions":
		require.Equal(t, "", runID, "definitions belong to no run")
	default:
		t.Fatalf("no table %q", table)
	}
	q := `SELECT count(*) FROM {{p}}` + table
	var args []any
	if runID != "" {
		q += ` WHERE ` + col + ` = ?1`
		args = append(args, runID)
	}
	var n int
	require.NoError(t, s.queryRow(context.Background(), s.db, q, args...).Scan(&n))
	return n
}

func (harness) ExpireLeases(t *testing.T, e *hosttest.Env, runID string) {
	t.Helper()
	s := store(e)
	_, err := s.exec(context.Background(), s.db,
		`UPDATE {{p}}jobs SET lease_expires = ?2 WHERE run_id = ?1 AND lease_expires IS NOT NULL`,
		runID, fmtTime(now().Add(-time.Second)))
	require.NoError(t, err)
}

func (harness) Backdate(t *testing.T, e *hosttest.Env, runID string, ago time.Duration) {
	t.Helper()
	s := store(e)
	_, err := s.exec(context.Background(), s.db, `UPDATE {{p}}jobs SET run_at = ?2 WHERE run_id = ?1`,
		runID, fmtTime(now().Add(-ago)))
	require.NoError(t, err)
}

// HoldRunLock writes to the run in a transaction it keeps open, which
// holds the database's write lock.
func (harness) HoldRunLock(t *testing.T, e *hosttest.Env, runID string) func() {
	t.Helper()
	s := store(e)
	e.Handle.(*env).busy.held.Store(true) // writes now wait past the busy timeout on purpose
	tx, err := s.db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	// Registered before the write, so a failed write still ends the
	// transaction and gives its connection back to the pool.
	release := sync.OnceFunc(func() { _ = tx.Rollback() })
	t.Cleanup(release)

	_, err = s.exec(context.Background(), tx,
		`UPDATE {{p}}runs SET event_seq = event_seq WHERE id = ?1`, runID)
	require.NoError(t, err)
	return release
}

func (harness) CallerTx(
	t *testing.T, e *hosttest.Env,
) (durable.SQLTx, hostcore.Tx, func() error, func() error) {
	t.Helper()
	s := store(e)
	tx, err := s.db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx, s.callerTx(tx), tx.Commit, tx.Rollback
}

func (harness) TxOn(e *hosttest.Env, tx durable.SQLTx) (hostcore.Tx, error) {
	return store(e).callerTx(tx), nil
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
	require.Equal(t, names(Worker{}, "core"), names(hosttest.Worker{}, "backend"))
}
