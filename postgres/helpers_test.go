package postgres

// Helpers for the tests that stay in this package. The shared suite
// (internal/hosttest) has its own.

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need Postgres. Set WORKFLOW_PG_DSN, for example:
//
//	docker run -d -e POSTGRES_PASSWORD=pg -p 127.0.0.1:55441:5432 postgres:18
//	WORKFLOW_PG_DSN="postgres://postgres:pg@127.0.0.1:55441/postgres?sslmode=disable" go test ./...
//
// Each test uses its own schema.
const dsnEnv = "WORKFLOW_PG_DSN"

var kinds = workflow.KindMap{"agent": nil, "action": nil}

func newStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s not set", dsnEnv)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	schema := "t_" + uuid.NewV4().String()[:8]
	s, err := New(pool, kinds, WithSchema(schema))
	require.NoError(t, err)
	require.NoError(t, s.Migrate(context.Background()))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		pool.Close()
	})
	return s
}

func load(t *testing.T, doc string) *workflow.Definition {
	t.Helper()
	def, err := workflow.Load([]byte(doc), kinds)
	require.NoError(t, err)
	return def
}

func startWorker(t *testing.T, w *Worker) {
	t.Helper()
	if w.Poll == 0 {
		w.Poll = 20 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitStatus(t *testing.T, s *Store, runID string, want ...workflow.RunStatus) *Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		r, err := s.GetRun(context.Background(), runID)
		require.NoError(t, err)
		if slices.Contains(want, r.Status) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is %s, want %v", runID, r.Status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countSteps(t *testing.T, s *Store, runID, step string) int {
	t.Helper()
	records, err := s.Steps(context.Background(), runID)
	require.NoError(t, err)
	n := 0
	for _, r := range records {
		if r.Step == step {
			n++
		}
	}
	return n
}

func countJobs(t *testing.T, s *Store, runID string) int {
	var n int
	require.NoError(t, s.pool.QueryRow(context.Background(),
		s.sql(`SELECT count(*) FROM {{schema}}.jobs WHERE run_id = $1`), runID).Scan(&n))
	return n
}

// runWorker runs a worker until the returned stop is called (or the test
// ends) and returns once it has stopped.
func runWorker(t *testing.T, w *Worker) (stop func()) {
	t.Helper()
	if w.Poll == 0 {
		w.Poll = 20 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop
}
