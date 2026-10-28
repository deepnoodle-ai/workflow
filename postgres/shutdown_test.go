package postgres

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestShutdownKeepsAPermanentFailure stops the worker while a handler is
// running; the handler then fails permanently. That failure is the step's
// result: it is recorded, not given back for another worker to run.
func TestShutdownKeepsAPermanentFailure(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "charge", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "declined", Definition: def})
	require.NoError(t, err)

	wctx, stop := context.WithCancel(ctx)
	w := &Worker{Store: s, Poll: 10 * time.Millisecond, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			stop()
			<-ctx.Done()
			return nil, &workflow.StepError{Type: "card_declined", Message: "insufficient funds"}
		},
	}}
	require.NoError(t, w.Run(wctx))

	var again atomic.Int32
	startWorker(t, &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) { again.Add(1); return "charged", nil },
	}})
	r := waitStatus(t, s, "declined", workflow.StatusFailed, workflow.StatusCompleted)
	require.Equal(t, workflow.StatusFailed, r.Status)
	require.Equal(t, "card_declined", r.State.Error.Type)
	require.Equal(t, int32(0), again.Load())
}

// TestShutdownGivesBackAnInterruptedAttempt: a handler that fails with
// a retryable error during shutdown is given back, not failed.
func TestShutdownGivesBackAnInterruptedAttempt(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "fetch", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "interrupted", Definition: def})
	require.NoError(t, err)
	wctx, stop := context.WithCancel(ctx)
	w := &Worker{Store: s, Poll: 10 * time.Millisecond, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			stop()
			<-ctx.Done()
			return nil, errors.New("read tcp: use of closed network connection")
		},
	}}
	require.NoError(t, w.Run(wctx))
	startWorker(t, &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) { return "fetched", nil },
	}})
	r := waitStatus(t, s, "interrupted", workflow.StatusFailed, workflow.StatusCompleted)
	require.Equal(t, workflow.StatusCompleted, r.Status)
}

// TestSettleIsRetried fails the first attempts to deliver a result, as a
// connection blip would. The worker retries within its lease; the handler
// runs once.
func TestSettleIsRetried(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
	var calls, failures atomic.Int32
	startWorker(t, &Worker{Store: s, Lease: time.Second, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) { calls.Add(1); return "done", nil },
	}, settleHook: func(claimedJob) error {
		if failures.Add(1) <= 3 {
			return errors.New("connection reset by peer")
		}
		return nil
	}})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "blip", Definition: def})
	require.NoError(t, err)
	waitStatus(t, s, "blip", workflow.StatusCompleted)
	time.Sleep(1500 * time.Millisecond) // past the lease
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, int32(4), failures.Load())
}

// TestShutdownIsBoundedByTheLease holds the run's row lock so a finished
// result cannot be delivered, then stops the worker. Run returns once the
// lease has run out instead of waiting for the lock.
func TestShutdownIsBoundedByTheLease(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "locked", Definition: def})
	require.NoError(t, err)

	tx, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, s.sql(`SELECT 1 FROM {{schema}}.runs WHERE id = 'locked' FOR UPDATE`))
	require.NoError(t, err)

	finished := make(chan struct{})
	wctx, stop := context.WithCancel(ctx)
	w := &Worker{Store: s, Lease: 600 * time.Millisecond, Poll: 10 * time.Millisecond, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) { return "done", nil },
	}, handlerDone: func(claimedJob, any, error) { close(finished) }}
	done := make(chan struct{})
	go func() { _ = w.Run(wctx); close(done) }()
	<-finished
	stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run waited on the lock past the lease")
	}
}

// TestTimerIsGivenBackOnShutdown: a timer that cannot be delivered while the
// worker shuts down is released at once, without counting as a claim.
func TestTimerIsGivenBackOnShutdown(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "nap", "sleep": "10ms", "next": ["a"]}, {"name": "a", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "nap", Definition: def})
	require.NoError(t, err)
	var once sync.Once
	tried := make(chan struct{})
	wctx, stop := context.WithCancel(ctx)
	w := &Worker{Store: s, Lease: time.Minute, Poll: 10 * time.Millisecond, Handlers: map[string]workflow.Handler{"action": nil},
		settleHook: func(claimedJob) error { once.Do(func() { close(tried) }); return errors.New("connection refused") }}
	done := make(chan struct{})
	go func() { _ = w.Run(wctx); close(done) }()
	<-tried
	stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
	var (
		claims int
		owner  *string
	)
	require.NoError(t, s.pool.QueryRow(ctx, s.sql(`SELECT claims, lease_owner FROM {{schema}}.jobs WHERE run_id = 'nap'`)).
		Scan(&claims, &owner))
	require.Equal(t, 0, claims)
	require.Nil(t, owner)
}

// TestMaxClaimsAbandonsAStep claims a step that crashes its worker each
// time. Past MaxClaims the step fails with abandoned and the handler is not
// run again.
func TestMaxClaimsAbandonsAStep(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "crashy", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "crashy", Definition: def})
	require.NoError(t, err)
	dead := &Worker{Store: s, ID: "dead", Handlers: map[string]workflow.Handler{"action": nil}}
	dead.defaults()
	for range 2 {
		c, err := dead.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c, 1)
		expireLeases(t, s, "crashy")
	}
	var calls atomic.Int32
	startWorker(t, &Worker{Store: s, MaxClaims: 2, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) { calls.Add(1); return nil, nil },
	}})
	r := waitStatus(t, s, "crashy", workflow.StatusFailed, workflow.StatusCompleted)
	require.Equal(t, workflow.StatusFailed, r.Status)
	require.Equal(t, "abandoned", r.State.Error.Type)
	require.Equal(t, int32(0), calls.Load())
	require.Equal(t, 0, countJobs(t, s, "crashy"))
}

// TestRetryWithBackoff fails a step twice with a retryable error; it is
// retried after its backoff and completes.
func TestRetryWithBackoff(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "flaky", "action": "x", "retry": {"max_attempts": 3, "backoff": "150ms"}}]}`)
	var (
		mu    sync.Mutex
		times []time.Time
	)
	startWorker(t, &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
			if job.Attempt < 3 {
				return nil, &workflow.StepError{Type: "rate_limited", Message: "slow down", Retryable: true}
			}
			return "ok", nil
		},
	}})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "flaky", Definition: def})
	require.NoError(t, err)
	r := waitStatus(t, s, "flaky", workflow.StatusFailed, workflow.StatusCompleted)
	require.Equal(t, workflow.StatusCompleted, r.Status)
	mu.Lock()
	require.Len(t, times, 3)
	for i := 1; i < 3; i++ {
		require.True(t, times[i].Sub(times[i-1]) >= 140*time.Millisecond, times[i].Sub(times[i-1]))
	}
	mu.Unlock()
	steps, err := s.Steps(ctx, "flaky")
	require.NoError(t, err)
	require.Len(t, steps, 3)
	for i, st := range steps {
		require.Equal(t, i+1, st.Attempt)
		want := StepFailed
		if i == 2 {
			want = StepCompleted
		}
		require.Equal(t, want, st.Status, st.StepID)
	}
	require.NotNil(t, steps[1].NotBefore)
}
