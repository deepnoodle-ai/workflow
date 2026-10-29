package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

var kinds = workflow.KindMap{"agent": nil, "action": nil}

const oneStep = `{"steps": [{"name": "a", "action": "x"}]}`

func load(t *testing.T, doc string) *workflow.Definition {
	t.Helper()
	def, err := workflow.Load([]byte(doc), kinds)
	require.NoError(t, err)
	return def
}

// openFile opens a new file with the suite's modernc DSN.
func openFile(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	return modernc.openDB(t, path), path
}

// newStore returns a migrated store on a new file.
func newStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	db, _ := openFile(t)
	s, err := New(db, kinds, opts...)
	require.NoError(t, err)
	require.NoError(t, s.Migrate(context.Background()))
	return s
}

func waitStatus(t *testing.T, s *Store, runID string, want workflow.RunStatus) *Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := s.GetRun(context.Background(), runID)
		if err == nil && r.Status == want {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is not %s: %v", runID, want, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func done(context.Context, workflow.Job) (any, error) { return "done", nil }

// TestMisconfiguredDatabasesFailFast: Migrate and Worker.Run refuse a
// database with no busy timeout, and an in-memory database on a pool that
// can open a second connection, with the fix in the message (UC-6).
func TestMisconfiguredDatabasesFailFast(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, dsn string
		maxConns  int
		want      string
	}{
		{"no busy timeout", "file:" + filepath.Join(t.TempDir(), "a.db") + "?_journal_mode=WAL", 0,
			"_busy_timeout=5000"},
		{"in-memory on a pool", "file::memory:?_busy_timeout=5000", 0, "SetMaxOpenConns(1)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", c.dsn)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			s, err := New(db, kinds)
			require.NoError(t, err)
			err = s.Migrate(ctx)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), c.want), err.Error())
			err = (&Worker{Store: s}).Run(ctx)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), c.want), err.Error())
		})
	}

	t.Run("in-memory on one connection", func(t *testing.T) {
		db, err := sql.Open("sqlite", "file::memory:?_busy_timeout=5000")
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		db.SetMaxOpenConns(1)
		s, err := New(db, kinds)
		require.NoError(t, err)
		require.NoError(t, s.Migrate(ctx))
		_, err = s.Start(ctx, NewRun{ID: "mem", Definition: load(t, oneStep)})
		require.NoError(t, err)
	})
}

func TestVersionCheck(t *testing.T) {
	for v, want := range map[string]bool{
		"3.37.0": true, "3.54.1": true, "4.0.0": true,
		"3.36.9": false, "2.99.0": false, "": false, "x.y": false,
	} {
		require.Equal(t, want, atLeast(v, 3, 37), v)
	}
}

// TestConcurrentMigrate: four handles on one file migrate at once. Each
// succeeds, and the migration is applied once. The file is opened once
// first: the first switch of a new file to WAL fails with SQLITE_BUSY when
// several connections open it at the same moment, before any statement of
// the store runs (the docs say to open it once first).
func TestConcurrentMigrate(t *testing.T) {
	db, path := openFile(t)
	require.NoError(t, db.Ping())
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		s, err := New(modernc.openDB(t, path), kinds)
		require.NoError(t, err)
		wg.Go(func() { errs[i] = s.Migrate(context.Background()) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	s, err := New(modernc.openDB(t, path), kinds)
	require.NoError(t, err)
	var n int
	require.NoError(t, s.queryRow(context.Background(), s.db,
		`SELECT count(*) FROM {{p}}schema_migrations`).Scan(&n))
	require.Equal(t, len(migrations), n)
}

// TestReadBeforeWriteFails: a store transaction that reads before its
// first write fails at once, so a break of the write-first rule fails every
// test instead of one run in a thousand under load.
func TestReadBeforeWriteFails(t *testing.T) {
	s := newStore(t)
	err := s.backend.Write(context.Background(), func(tx hostcore.Tx) error {
		_, err := tx.DefinitionDoc(context.Background(), "h")
		return err
	})
	require.ErrorIs(t, err, errReadFirst)
}

// TestTimeArgumentIsRefused: a statement with a time.Time argument fails,
// since a driver would format it in a layout that sorts wrongly.
func TestTimeArgumentIsRefused(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	at := time.Now()
	_, err := s.exec(ctx, s.db, `SELECT ?1`, at)
	require.ErrorIs(t, err, errTimeArg)
	_, err = s.query(ctx, s.db, `SELECT ?1`, &at)
	require.ErrorIs(t, err, errTimeArg)
	_, err = s.exec(ctx, s.db, `SELECT ?1`, fmtTime(at))
	require.NoError(t, err)
}

// TestTimesSortAsStored: fmtTime is fixed-width UTC, so text order is time
// order, and parseTime reads it back.
func TestTimesSortAsStored(t *testing.T) {
	a := time.Date(2026, 10, 29, 2, 17, 53, 999_999_000, time.FixedZone("x", -7*3600))
	b := a.Add(time.Microsecond)
	require.True(t, fmtTime(a) < fmtTime(b))
	require.Equal(t, "2026-10-29T09:17:53.999999Z", fmtTime(a))
	got, err := parseTime(fmtTime(a))
	require.NoError(t, err)
	require.True(t, got.Equal(a))
}

// TestClaimUsesItsIndex: the claim, as Claim runs it, searches jobs_claim
// for its queues; it does not scan the jobs table.
func TestClaimUsesItsIndex(t *testing.T) {
	s := newStore(t)
	at := fmtTime(now())
	in, queues := placeholders(6, []string{"timer", "step:agent", "step:action"})
	args := append([]any{"w", at, at, at, 8}, queues...)
	rows, err := s.query(context.Background(), s.db, "EXPLAIN QUERY PLAN "+claimSQL(in), args...)
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var (
			id, parent, unused int
			detail             string
		)
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	text := strings.Join(plan, "\n")
	require.True(t, strings.Contains(text, "INDEX workflow_jobs_claim"), text)
	require.False(t, strings.Contains(text, "SCAN workflow_jobs"), text)
}

// TestWithSchemaPrefixesTheTables: two stores with different prefixes in
// one file do not see each other's runs, and a bad name is refused.
func TestWithSchemaPrefixesTheTables(t *testing.T) {
	ctx := context.Background()
	db, _ := openFile(t)
	a, err := New(db, kinds)
	require.NoError(t, err)
	b, err := New(db, kinds, WithSchema("other"))
	require.NoError(t, err)
	require.NoError(t, a.Migrate(ctx))
	require.NoError(t, b.Migrate(ctx))
	_, err = a.Start(ctx, NewRun{ID: "r", Definition: load(t, oneStep)})
	require.NoError(t, err)
	_, err = b.GetRun(ctx, "r")
	require.ErrorIs(t, err, ErrRunNotFound)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM other_runs`).Scan(&n))
	require.Equal(t, 0, n)

	_, err = New(db, kinds, WithSchema("Bad-Name"))
	require.Error(t, err)
}

// TestAJobWrittenThroughTheStoreWakesItsWorker: a worker with a long Poll
// runs a step written through the same Store at once (UC-2).
func TestAJobWrittenThroughTheStoreWakesItsWorker(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &Worker{Store: s, Poll: time.Minute, Handlers: map[string]workflow.Handler{"action": done}}
	go func() { _ = w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond) // the worker's first claim finds nothing
	start := time.Now()
	_, err := s.Start(context.Background(), NewRun{ID: "woken", Definition: load(t, oneStep)})
	require.NoError(t, err)
	waitStatus(t, s, "woken", workflow.StatusCompleted)
	require.True(t, time.Since(start) < 2*time.Second, time.Since(start))
	require.NoError(t, w.Shutdown(context.Background()))
}

// TestStartSQLTxWithTheApplicationsWrites: a run starts in the
// application's transaction with its own row (UC-4). A rollback leaves
// neither; a commit keeps both, and the run's step runs.
func TestStartSQLTxWithTheApplicationsWrites(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.db.Exec(`CREATE TABLE orders (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	def := load(t, oneStep)

	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO orders (id) VALUES ('o-1')`)
	require.NoError(t, err)
	_, err = s.StartSQLTx(ctx, tx, NewRun{ID: "o-1", Definition: def})
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	_, err = s.GetRun(ctx, "o-1")
	require.ErrorIs(t, err, ErrRunNotFound)

	tx, err = s.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO orders (id) VALUES ('o-2')`)
	require.NoError(t, err)
	res, err := s.StartSQLTx(ctx, tx, NewRun{ID: "o-2", Definition: def})
	require.NoError(t, err)
	require.Len(t, res.Jobs(), 1)
	require.NoError(t, tx.Commit())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = (&Worker{Store: s, Handlers: map[string]workflow.Handler{"action": done}}).Run(ctx)
	}()
	waitStatus(t, s, "o-2", workflow.StatusCompleted)
	var orders int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM orders`).Scan(&orders))
	require.Equal(t, 1, orders)
}

// TestWorkerFieldsSetAfterACall: a Worker reads its fields at each call,
// so an Observer and a MayClaim gate set after a first call take effect.
func TestWorkerFieldsSetAfterACall(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "late", Definition: load(t, oneStep)})
	require.NoError(t, err)
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{"action": done}}
	require.Equal(t, 0, w.Active())

	var (
		claimed atomic.Int32
		open    atomic.Bool
	)
	w.Observer = Observer{Claimed: func(workflow.Job) { claimed.Add(1) }}
	w.MayClaim = open.Load
	require.NoError(t, w.RunOnce(ctx))
	require.Equal(t, int32(0), claimed.Load(), "the gate is closed")

	open.Store(true)
	require.NoError(t, w.RunOnce(ctx))
	require.Equal(t, int32(1), claimed.Load(), "the Observer set late sees the claim")
	r, err := s.GetRun(ctx, "late")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, r.Status)
}

// TestSharedErrorsAreDurable: the sentinels are durable's values, so
// errors.Is works across backends.
func TestSharedErrorsAreDurable(t *testing.T) {
	s := newStore(t)
	_, err := s.GetRun(context.Background(), "missing")
	require.True(t, errors.Is(fmt.Errorf("wrapped: %w", err), ErrRunNotFound))
	require.Equal(t, "*durable.Result", fmt.Sprintf("%T", &Result{}))
}

// TestAPanickingHookRollsBack: a finish hook that panics rolls its
// transaction back, so the run is not finished and the write lock is free.
func TestAPanickingHookRollsBack(t *testing.T) {
	s := newStore(t, WithOnFinishedSQL(func(context.Context, SQLTx, Finished) error {
		panic("hook bug")
	}))
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "p", Definition: load(t, oneStep)})
	require.NoError(t, err)
	func() {
		defer func() { require.Equal(t, "hook bug", recover()) }()
		_, _ = s.Cancel(ctx, "p", "stop")
	}()
	require.Equal(t, 0, s.db.Stats().InUse, "the transaction's connection is back")
	r, err := s.GetRun(ctx, "p")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusRunning, r.Status, "the cancel rolled back")
	wctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err = s.Start(wctx, NewRun{ID: "next", Definition: load(t, oneStep)})
	require.NoError(t, err, "the write lock is free")
}
