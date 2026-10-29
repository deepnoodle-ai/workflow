package hosttest

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestRetryDoesNotRepeatACharge: a charge handler writes to a table keyed
// by the run ID and the step's unit, then hangs past the step's timeout on
// its first attempt. The retry runs, finds the charge, and returns it. One
// charge per run, and a second run of the same definition charges again.
func testRetryDoesNotRepeatACharge(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	// charges is the application's table of charges, keyed by idempotency
	// key; a charge records the attempt that made it.
	var (
		mu      sync.Mutex
		charges = map[string]int{}
	)
	def := load(t, `{"steps": [
	  {"name": "charge", "action": "payments.charge", "timeout": "200ms",
	   "retry": {"max_attempts": 2, "backoff": "10ms"}, "next": ["receipt"]},
	  {"name": "receipt", "action": "email.receipt", "with": {"charge": "${{ charge.key }}"}}],
	  "outputs": {"charge": "${{ charge }}", "receipt": "${{ receipt }}"}}`)

	var hung atomic.Int32
	startWorker(t, &Worker{
		Store: s,
		Lease: 300 * time.Millisecond,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.Step == "receipt" {
					return job.Params["charge"], nil
				}
				key := job.RunID + "/" + job.StepID.Unit()
				mu.Lock()
				if _, ok := charges[key]; !ok {
					charges[key] = job.Attempt
				}
				mu.Unlock()
				if job.Attempt == 1 {
					// The provider charged; the response never came back.
					select {
					case <-ctx.Done():
						hung.Add(1)
					case <-time.After(10 * time.Second):
					}
					return nil, ctx.Err()
				}
				mu.Lock()
				attempt := charges[key]
				mu.Unlock()
				return map[string]any{"key": key, "attempt": attempt}, nil
			},
		},
	})

	for _, id := range []string{"order-1", "order-2"} {
		_, err := s.Start(ctx, NewRun{ID: id, Definition: def})
		require.NoError(t, err)
	}
	for _, id := range []string{"order-1", "order-2"} {
		r := waitStatus(t, s, id, workflow.StatusCompleted, workflow.StatusFailed)
		require.Equal(t, workflow.StatusCompleted, r.Status, "%v", r.State.Error)
		require.Equal(t, map[string]any{
			"charge":  map[string]any{"key": id + "/main/charge", "attempt": 1.0},
			"receipt": id + "/main/charge",
		}, r.State.Outputs)
		steps, err := s.Steps(ctx, id)
		require.NoError(t, err)
		require.Len(t, steps, 3)
		require.Equal(t, workflow.StepID("main/charge#1"), steps[0].StepID)
		require.Equal(t, StepFailed, steps[0].Status)
		require.Equal(t, workflow.ErrorTypeTimeout, steps[0].Error.Type)
		require.Equal(t, workflow.StepID("main/charge#2"), steps[1].StepID)
		require.Equal(t, StepCompleted, steps[1].Status)
	}
	// Each first attempt is stopped once its heartbeat finds the job gone.
	waitFor(t, "both first attempts stopped", func() bool { return hung.Load() == 2 })

	mu.Lock()
	keys := slices.Sorted(maps.Keys(charges))
	mu.Unlock()
	require.Equal(t, []string{"order-1/main/charge", "order-2/main/charge"}, keys,
		"one charge per run")
}

// fakePaymentProvider stands in for an external service that honours
// idempotency keys: a second request with a key it has seen returns the
// first charge.
type fakePaymentProvider struct {
	mu       sync.Mutex
	requests int
	charges  map[string]string // idempotency key -> charge ID
}

func (p *fakePaymentProvider) charge(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests++
	if id, ok := p.charges[key]; ok {
		return id
	}
	id := "ch_" + uuid.NewV4().String()[:8]
	p.charges[key] = id
	return id
}

// TestCrashAfterTheSideEffect: a worker charges, then dies before it
// settles. Its lease runs out, another worker reclaims the job (Claims 2)
// and runs the handler again with the same idempotency key. The provider
// charges once and the next step is dispatched once, even when the dead
// worker comes back and reports.
func testCrashAfterTheSideEffect(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	def := load(t, `{"steps": [
	  {"name": "charge", "action": "payments.charge", "next": ["ship"]},
	  {"name": "ship", "action": "warehouse.ship", "with": {"charge": "${{ charge }}"}}]}`)
	_, err := s.Start(ctx, NewRun{ID: "crash-charge", Definition: def})
	require.NoError(t, err)

	provider := &fakePaymentProvider{charges: map[string]string{}}
	var (
		mu       sync.Mutex
		claims   []int
		attempts []int
		ships    []any
	)
	handler := func(ctx context.Context, job workflow.Job) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		if job.Step == "ship" {
			ships = append(ships, job.Params["charge"])
			return "shipped", nil
		}
		claims = append(claims, job.Claims)
		attempts = append(attempts, job.Attempt)
		return provider.charge(job.RunID + "/" + job.StepID.Unit()), nil
	}

	dead := &Worker{
		Store:    s,
		ID:       "dead",
		Lease:    200 * time.Millisecond,
		Handlers: map[string]workflow.Handler{"action": handler},
	}
	dead.defaults()
	claimed, err := dead.claim(ctx, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	output, err := dead.call(ctx, claimed[0])
	require.NoError(t, err)
	// The worker dies here: no settle, no heartbeat.

	startWorker(t, &Worker{
		Store:    s,
		ID:       "alive",
		Lease:    time.Second,
		Handlers: map[string]workflow.Handler{"action": handler},
	})
	r := waitStatus(t, s, "crash-charge", workflow.StatusCompleted, workflow.StatusFailed)
	require.Equal(t, workflow.StatusCompleted, r.Status, "%v", r.State.Error)

	// The dead worker comes back and reports its result: fenced out.
	dead.settle(ctx, claimed[0], time.Now(), workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: claimed[0].StepID,
		Output: output,
	}, dead.Logger)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []int{1, 2}, claims)
	require.Equal(t, []int{1, 1}, attempts, "a reclaim is the same attempt")
	provider.mu.Lock()
	require.Equal(t, 2, provider.requests)
	require.Len(t, provider.charges, 1, "one external charge")
	provider.mu.Unlock()
	require.Equal(t, []any{output}, ships, "the next step ran once, with the one charge")
	require.Equal(t, 1, countSteps(t, s, "crash-charge", "ship"))
	events, err := s.Events(ctx, "crash-charge")
	require.NoError(t, err)
	require.Len(t, events, 3, "start, charge, ship")
}

// TestDeliverBeforeTheHandlerDefers: a handler delivers its own result
// (a callback that arrives before the handler returns) and then returns
// ErrDeferred. The step completes once, its job is gone, and the same
// callback delivered again after a restart changes nothing.
func testDeliverBeforeTheHandlerDefers(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	def := load(t, `{"steps": [
	  {"name": "ask", "agent": "remote", "next": ["ship"]},
	  {"name": "ship", "action": "x", "with": {"answer": "${{ ask.answer }}"}}]}`)
	callback := func(id workflow.StepID) workflow.Event {
		return workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: id,
			Output: map[string]any{"answer": 42},
		}
	}
	var (
		asks    atomic.Int32
		applied atomic.Bool
		mu      sync.Mutex
		shipped []any
	)
	startWorker(t, &Worker{
		Store: s,
		Lease: 300 * time.Millisecond,
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				asks.Add(1)
				res, err := s.Deliver(ctx, job.RunID, callback(job.StepID))
				if err != nil {
					return nil, err
				}
				applied.Store(res.Applied)
				return nil, workflow.ErrDeferred
			},
			// ship is handed off too, so the run stays running and any job
			// left behind is visible.
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				mu.Lock()
				shipped = append(shipped, job.Params["answer"])
				mu.Unlock()
				return nil, workflow.ErrDeferred
			},
		},
	})
	_, err := s.Start(ctx, NewRun{ID: "early-callback", Definition: def})
	require.NoError(t, err)
	waitFor(t, "ship handed off and no jobs left", func() bool {
		mu.Lock()
		n := len(shipped)
		mu.Unlock()
		return n == 1 && countJobs(t, s, "early-callback") == 0
	})
	require.True(t, applied.Load())
	require.Equal(t, int32(1), asks.Load())

	r, err := s.GetRun(ctx, "early-callback")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusRunning, r.Status)
	require.Equal(t, []workflow.StepID{"main/ship#1"}, r.State.InFlight())
	steps, err := s.Steps(ctx, "early-callback")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	require.Equal(t, StepCompleted, steps[0].Status)
	require.Equal(t, map[string]any{"answer": 42.0}, steps[0].Output)
	require.Equal(t, StepDispatched, steps[1].Status)

	// The callback arrives again, as after a restart of the service that
	// sent it.
	res, err := s.Deliver(ctx, "early-callback", callback("main/ask#1"))
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Len(t, res.Commands, 0)
	require.Equal(t, 0, countJobs(t, s, "early-callback"))
	require.Equal(t, 1, countSteps(t, s, "early-callback", "ship"))
	events, err := s.Events(ctx, "early-callback")
	require.NoError(t, err)
	require.Len(t, events, 2, "start, ask")
	mu.Lock()
	require.Equal(t, []any{42.0}, shipped)
	mu.Unlock()
}
