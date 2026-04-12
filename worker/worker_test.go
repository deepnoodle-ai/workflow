package worker_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/worker"
	"github.com/deepnoodle-ai/workflow/worker/memstore"
)

func TestWorker_RunsHandlerAndPersistsCompletion(t *testing.T) {
	store := memstore.New()
	if err := store.Enqueue(context.Background(), worker.NewRun{
		ID:   "run-1",
		Spec: []byte(`{"hello":"world"}`),
	}); err != nil {
		t.Fatal(err)
	}

	handled := make(chan *worker.Claim, 1)
	handler := worker.HandlerFunc(func(_ context.Context, c *worker.Claim) worker.Outcome {
		handled <- c
		return worker.Outcome{
			Status: worker.StatusCompleted,
			Result: []byte(`{"ok":true}`),
		}
	})

	w, err := worker.New(worker.Config{
		QueueStore:        store,
		Handler:           handler,
		PollInterval:      20 * time.Millisecond,
		HeartbeatInterval: 40 * time.Millisecond,
		StaleAfter:        200 * time.Millisecond,
		ReaperInterval:    50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	w.Notify()

	select {
	case c := <-handled:
		if c.ID != "run-1" || c.Attempt != 1 {
			t.Fatalf("unexpected claim: %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked within timeout")
	}

	// Wait for the completion to land in the store.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := store.Snapshot()[`run-1`]
		if snap.Status == worker.StatusCompleted {
			if string(snap.Result) != `{"ok":true}` {
				t.Fatalf("unexpected result: %q", snap.Result)
			}
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not reach completed status")
}

func TestWorker_HandlerPanicBecomesFailed(t *testing.T) {
	store := memstore.New()
	_ = store.Enqueue(context.Background(), worker.NewRun{ID: "crash"})

	w, err := worker.New(worker.Config{
		QueueStore:        store,
		Handler:           worker.HandlerFunc(func(_ context.Context, _ *worker.Claim) worker.Outcome { panic("boom") }),
		PollInterval:      20 * time.Millisecond,
		HeartbeatInterval: 40 * time.Millisecond,
		StaleAfter:        200 * time.Millisecond,
		ReaperInterval:    50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	w.Notify()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := store.Snapshot()["crash"]
		if snap.Status == worker.StatusFailed {
			if snap.ErrorMessage == "" {
				t.Fatalf("expected error message, got empty")
			}
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not reach failed status")
}

func TestWorker_ReaperReclaimsStale(t *testing.T) {
	store := memstore.New()
	// Freeze the store's clock so we can write a stale heartbeat.
	frozen := time.Now()
	store.SetClock(func() time.Time { return frozen.Add(-10 * time.Minute) })

	_ = store.Enqueue(context.Background(), worker.NewRun{ID: "stale"})

	// Claim the run under a bogus worker ID so its heartbeat is
	// "very old" relative to the reaper's current clock.
	claim, err := store.ClaimQueued(context.Background(), "ghost-worker")
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	// Reset store clock back to now for subsequent operations.
	store.SetClock(time.Now)

	var calls atomic.Int32
	handler := worker.HandlerFunc(func(_ context.Context, _ *worker.Claim) worker.Outcome {
		calls.Add(1)
		return worker.Outcome{Status: worker.StatusCompleted}
	})

	w, err := worker.New(worker.Config{
		QueueStore:        store,
		Handler:           handler,
		PollInterval:      20 * time.Millisecond,
		HeartbeatInterval: 40 * time.Millisecond,
		StaleAfter:        100 * time.Millisecond,
		ReaperInterval:    30 * time.Millisecond,
		MaxAttempts:       3,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("handler was not invoked after reaper reclaim")
	}

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exited with %v", err)
	}
}

func TestNew_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  worker.Config
	}{
		{"no store", worker.Config{Handler: worker.HandlerFunc(func(context.Context, *worker.Claim) worker.Outcome { return worker.Outcome{} })}},
		{"no handler", worker.Config{QueueStore: memstore.New()}},
		{"bad stale", worker.Config{
			QueueStore:        memstore.New(),
			Handler:           worker.HandlerFunc(func(context.Context, *worker.Claim) worker.Outcome { return worker.Outcome{} }),
			HeartbeatInterval: 30 * time.Second,
			StaleAfter:        10 * time.Second,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := worker.New(tc.cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
