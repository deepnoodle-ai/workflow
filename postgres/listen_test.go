package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestListenDoesNotTakeAPoolConnection runs a worker on a pool of one
// connection. The listener has its own connection, so the worker can still
// claim and settle, and when it stops nothing is left listening.
func TestListenDoesNotTakeAPoolConnection(t *testing.T) {
	base := newStore(t)
	cfg, err := pgxpool.ParseConfig(os.Getenv(dsnEnv))
	require.NoError(t, err)
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	defer pool.Close()
	s, err := New(pool, kinds, WithSchema(base.schema))
	require.NoError(t, err)

	ctx := context.Background()
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	w := &Worker{Store: s, Poll: time.Hour, Handlers: map[string]Handler{ // only notifications wake it
		"task": func(ctx context.Context, task Task) (any, error) { return "ok", nil },
	}}
	go func() { _ = w.Run(wctx); close(done) }()
	waitFor(t, "the worker to listen", func() bool {
		var n int
		err := base.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE query LIKE 'LISTEN%' || $1 || '%'`,
			s.NotifyChannel()).Scan(&n)
		return err == nil && n > 0
	})
	_, err = base.Start(ctx, NewRun{ID: "one-conn", Definition: load(t, `{"steps": [{"name": "a", "task": "x"}]}`)})
	require.NoError(t, err)
	waitStatus(t, base, "one-conn", workflow.StatusCompleted) // observe through another pool
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	var channels []string
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := pool.Query(qctx, `SELECT pg_listening_channels()`)
	require.NoError(t, err)
	channels, err = pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	require.Len(t, channels, 0)
}
