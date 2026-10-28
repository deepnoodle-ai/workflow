package postgres

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

func expireLeases(t *testing.T, s *Store, runID string) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(), s.sql(`
		UPDATE {{schema}}.jobs SET lease_expires = now() - interval '1 second' WHERE run_id = $1`), runID)
	require.NoError(t, err)
}

// TestReleaseDoesNotRestoreAStaleFence: W1 stalls past its lease, W2 claims
// the job and then shuts down gracefully, giving the claim back. W1 must
// still be fenced out.
func TestReleaseDoesNotRestoreAStaleFence(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "a", "action": "x", "next": ["b"]}, {"name": "b", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "fence", Definition: def})
	require.NoError(t, err)

	w1 := &Worker{
		Store:    s,
		ID:       "w1",
		Handlers: map[string]workflow.Handler{"action": nil},
	}
	w1.defaults()
	c1, err := w1.claim(ctx, 1)
	require.NoError(t, err)
	require.Len(t, c1, 1)
	expireLeases(t, s, "fence")

	w2 := &Worker{
		Store:    s,
		ID:       "w2",
		Handlers: map[string]workflow.Handler{"action": nil},
	}
	w2.defaults()
	c2, err := w2.claim(ctx, 1)
	require.NoError(t, err)
	require.Len(t, c2, 1)
	w2.release(c2[0])

	w1.settle(ctx, c1[0], time.Now(), workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "main/a#1",
		Output: "stale",
	}, w1.Logger)
	events, err := s.Events(ctx, "fence")
	require.NoError(t, err)
	require.Len(t, events, 1, "the stale worker's result was applied")
	require.Equal(t, 1, countJobs(t, s, "fence"))
}

// TestHeartbeatNoticesReclaim: while a handler runs past its lease, another
// worker claims the job. The heartbeat finds the fence moved, cancels the
// handler and drops its result.
func TestHeartbeatNoticesReclaim(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
	ctx := context.Background()
	started := make(chan struct{})
	stopped := make(chan error, 1)
	startWorker(t, &Worker{
		Store: s,
		ID:    "slow",
		Lease: 300 * time.Millisecond,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				close(started)
				<-ctx.Done()
				stopped <- ctx.Err()
				return "late", nil
			},
		},
	})
	_, err := s.Start(ctx, NewRun{ID: "reclaim", Definition: def})
	require.NoError(t, err)
	<-started

	other := &Worker{
		Store:    s,
		ID:       "other",
		Lease:    time.Minute,
		Handlers: map[string]workflow.Handler{"action": nil},
	}
	other.defaults()
	waitFor(t, "another worker claims the job", func() bool {
		expireLeases(t, s, "reclaim")
		c, err := other.claim(ctx, 1)
		return err == nil && len(c) == 1
	})
	select {
	case err := <-stopped:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the handler was not cancelled")
	}
	time.Sleep(100 * time.Millisecond)
	events, err := s.Events(ctx, "reclaim")
	require.NoError(t, err)
	require.Len(t, events, 1, "the reclaimed worker's result was applied")
}

// TestHeartbeatFailureCancelsTheHandler partitions a worker from the
// database: its heartbeats fail. Once its lease has run out another worker
// may own the job, so the handler is cancelled.
func TestHeartbeatFailureCancelsTheHandler(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
	ctx := context.Background()
	var beats atomic.Int32
	stopped := make(chan time.Duration, 1)
	startWorker(t, &Worker{
		Store: s,
		Lease: 300 * time.Millisecond,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				start := time.Now()
				select {
				case <-ctx.Done():
					stopped <- time.Since(start)
				case <-time.After(10 * time.Second):
				}
				return nil, ctx.Err()
			},
		},
		heartbeatHook: func() error {
			beats.Add(1)
			return errors.New("network unreachable")
		},
	})
	_, err := s.Start(ctx, NewRun{ID: "partitioned", Definition: def})
	require.NoError(t, err)
	select {
	case d := <-stopped:
		require.True(t, d >= 250*time.Millisecond, d)
		require.True(t, beats.Load() >= 2)
	case <-time.After(5 * time.Second):
		t.Fatal("the handler kept running without a lease")
	}
}
