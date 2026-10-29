package postgres

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

const oneStep = `{"steps": [{"name": "a", "action": "x"}]}`

// soleJob reads the claims of a run's only job, and whether it is leased.
func soleJob(t *testing.T, s *Store, runID string) (claims int, leased bool) {
	t.Helper()
	require.NoError(t, s.pool.QueryRow(context.Background(), s.sql(`
		SELECT claims, lease_owner IS NOT NULL FROM {{schema}}.jobs WHERE run_id = $1`),
		runID).Scan(&claims, &leased))
	return claims, leased
}

// goRun runs w on its own context, as an application should for Shutdown.
// The returned channel closes when Run returns nil.
func goRun(t *testing.T, w *Worker) <-chan struct{} {
	t.Helper()
	if w.Poll == 0 {
		w.Poll = 10 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, w.Run(ctx))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return done
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startRuns(t *testing.T, s *Store, doc string, ids ...string) {
	t.Helper()
	def := load(t, doc)
	for _, id := range ids {
		_, err := s.Start(context.Background(), NewRun{ID: id, Definition: def})
		require.NoError(t, err)
	}
}

// syncBuffer is a log destination that tests read while the worker writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// blockClaims locks the jobs table until the test ends, so a claim query
// hangs in Postgres, as one on a stalled connection would. It returns a
// check that reports whether a claim query waits on the lock.
func blockClaims(t *testing.T, s *Store) func() bool {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	_, err = tx.Exec(ctx, s.sql(`LOCK TABLE {{schema}}.jobs IN ACCESS EXCLUSIVE MODE`))
	require.NoError(t, err)
	return func() bool {
		var n int
		err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%' || $1 || '.jobs%'`,
			s.schema).Scan(&n)
		require.NoError(t, err)
		return n > 0
	}
}

// TestShutdownCancelsAHungClaim: a claim query that hangs is cancelled a
// second after the deadline, so Shutdown returns. The cancel is part of a
// normal shutdown, so Run logs no claim error.
func TestShutdownCancelsAHungClaim(t *testing.T) {
	s := newStore(t)
	claimWaits := blockClaims(t, s)
	var logs syncBuffer
	w := &Worker{
		Store:    s,
		Logger:   slog.New(slog.NewTextHandler(&logs, nil)),
		Handlers: map[string]workflow.Handler{"action": nil},
	}
	goRun(t, w)
	waitUntil(t, "a claim query hangs", claimWaits)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	begin := time.Now()
	require.ErrorIs(t, w.Shutdown(ctx), context.DeadlineExceeded)
	require.True(t, time.Since(begin) < 3*time.Second, time.Since(begin))
	require.True(t, time.Since(begin) >= time.Second, time.Since(begin))
	require.NotContains(t, logs.String(), "workflow worker: claim")
	// The claim query in progress counts as an active job.
	require.Contains(t, logs.String(), `msg="workflow worker: shutting down" active=1`)
}

func stepDoc(name string) string {
	return `{"steps": [{"name": "` + name + `", "action": "x"}]}`
}

// TestShutdownObserverEvents: a claim query that hangs on a table lock past
// the Shutdown deadline fires no observer event. The other cases run in the
// shared suite (internal/hosttest); this one needs a Postgres table lock.
func TestShutdownObserverEvents(t *testing.T) {
	ctx := context.Background()
	shortly := func() context.Context {
		c, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		t.Cleanup(cancel)
		return c
	}

	t.Run("hung claim query", func(t *testing.T) {
		s := newStore(t)
		claimWaits := blockClaims(t, s)
		var log eventLog
		w := &Worker{
			Store:    s,
			Observer: log.observer(),
			Handlers: map[string]workflow.Handler{"action": nil},
		}
		goRun(t, w)
		waitUntil(t, "a claim query hangs", claimWaits)
		require.ErrorIs(t, w.Shutdown(shortly()), context.DeadlineExceeded)
		require.Equal(t, 0, len(log.get()))
	})
}
