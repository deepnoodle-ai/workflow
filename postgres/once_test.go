package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
)

const (
	chain = `{"steps": [
	  {"name": "a", "action": "x", "next": ["b"]},
	  {"name": "b", "action": "x", "next": ["c"]},
	  {"name": "c", "action": "x"}]}`
	napFirst = `{"steps": [
	  {"name": "nap", "sleep": "1h", "next": ["a"]},
	  {"name": "a", "action": "x"}]}`
)

// counting returns handlers that count their calls by run and return "ok".
func counting() (map[string]workflow.Handler, func(runID string) int) {
	var mu sync.Mutex
	calls := map[string]int{}
	h := func(_ context.Context, job workflow.Job) (any, error) {
		mu.Lock()
		calls[job.RunID]++
		mu.Unlock()
		return "ok", nil
	}
	return map[string]workflow.Handler{"action": h}, func(runID string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[runID]
	}
}

func runStatus(t *testing.T, s *Store, runID string) workflow.RunStatus {
	t.Helper()
	r, err := s.GetRun(context.Background(), runID)
	require.NoError(t, err)
	return r.Status
}

// runOnceWithin runs RunOnce and fails the test if it has not returned
// within d.
func runOnceWithin(t *testing.T, w *Worker, ctx context.Context, d time.Duration) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- w.RunOnce(ctx) }()
	select {
	case err := <-result:
		return err
	case <-time.After(d):
		t.Fatalf("RunOnce did not return within %s", d)
		return nil
	}
}

// TestRunOnceRunsDueWorkAndReturns: a job due at the start runs, and so do
// the steps it dispatches, so a chain finishes in one call. A timer due in
// an hour is not claimed.
func TestRunOnceRunsDueWorkAndReturns(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, chain, "chain")
	startRuns(t, s, napFirst, "later")
	handlers, calls := counting()
	w := &Worker{Store: s, Handlers: handlers}

	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "chain"))
	require.Equal(t, 3, calls("chain"))
	require.Equal(t, workflow.StatusRunning, runStatus(t, s, "later"))
	claims, leased := soleJob(t, s, "later")
	require.Equal(t, 0, claims)
	require.False(t, leased)
	require.Equal(t, 0, w.Active())
}

// TestRunOnceLeavesNewTimersForTheNextCall: a timer the call writes is not
// fired in it, even once due; the next call after its time fires it and
// runs the step after it.
func TestRunOnceLeavesNewTimersForTheNextCall(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, `{"steps": [
	  {"name": "a", "action": "x", "next": ["nap"]},
	  {"name": "nap", "sleep": "100ms", "next": ["b"]},
	  {"name": "b", "action": "x"}]}`, "r")
	handlers, calls := counting()
	w := &Worker{Store: s, Handlers: handlers}

	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, 1, calls("r"))
	require.Equal(t, workflow.StatusRunning, runStatus(t, s, "r"))

	time.Sleep(150 * time.Millisecond)
	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, 2, calls("r"))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "r"))
}

// TestRunOnceLeavesWorkAddedDuringTheCall: a run that another caller
// starts while RunOnce runs waits for the next call.
func TestRunOnceLeavesWorkAddedDuringTheCall(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "first")
	def := load(t, oneStep)
	handlers, calls := counting()
	inner := handlers["action"]
	handlers["action"] = func(ctx context.Context, job workflow.Job) (any, error) {
		if job.RunID == "first" {
			if _, err := s.Start(ctx, NewRun{ID: "late", Definition: def}); err != nil {
				return nil, err
			}
		}
		return inner(ctx, job)
	}
	w := &Worker{Store: s, Handlers: handlers}

	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "first"))
	require.Equal(t, 0, calls("late"))
	claims, _ := soleJob(t, s, "late")
	require.Equal(t, 0, claims)

	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "late"))
}

// TestRunOnceReturnsDuringAnEndlessLoop: a run that loops over a short
// sleep forever does not keep RunOnce from returning, even while another
// job runs long enough for its timers to come due.
func TestRunOnceReturnsDuringAnEndlessLoop(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, `{"steps": [
	  {"name": "a", "action": "x", "next": ["nap"]},
	  {"name": "nap", "sleep": "1ms", "next": ["a"]}]}`, "loop")
	startRuns(t, s, oneStep, "slow")
	handlers, calls := counting()
	inner := handlers["action"]
	handlers["action"] = func(ctx context.Context, job workflow.Job) (any, error) {
		if job.RunID == "slow" {
			time.Sleep(300 * time.Millisecond)
		}
		return inner(ctx, job)
	}
	w := &Worker{Store: s, Handlers: handlers}

	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, 1, calls("loop"))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "slow"))
	require.Equal(t, workflow.StatusRunning, runStatus(t, s, "loop"))
}

// TestRunOnceRespectsConcurrency: no more than Concurrency handlers run at
// once in one call, and every due job runs.
func TestRunOnceRespectsConcurrency(t *testing.T) {
	s := newStore(t)
	ids := []string{"r1", "r2", "r3", "r4", "r5", "r6"}
	startRuns(t, s, oneStep, ids...)
	var now, most atomic.Int32
	w := &Worker{
		Store:       s,
		Concurrency: 2,
		Handlers: map[string]workflow.Handler{
			"action": func(context.Context, workflow.Job) (any, error) {
				n := now.Add(1)
				defer now.Add(-1)
				for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
				}
				time.Sleep(30 * time.Millisecond)
				return "ok", nil
			},
		},
	}

	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, int32(2), most.Load())
	for _, id := range ids {
		require.Equal(t, workflow.StatusCompleted, runStatus(t, s, id))
	}
}

// TestRunOnceContextEndCountsTheClaim: when the caller's context ends,
// RunOnce gives the claim back at once, but it counts, so a step that
// always outlives the deadline fails as abandoned after MaxClaims calls.
func TestRunOnceContextEndCountsTheClaim(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "r")
	var calls atomic.Int32
	w := &Worker{
		Store:     s,
		MaxClaims: 2,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, _ workflow.Job) (any, error) {
				calls.Add(1)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	call := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		return runOnceWithin(t, w, ctx, 10*time.Second)
	}

	require.ErrorIs(t, call(), context.DeadlineExceeded)
	claims, leased := soleJob(t, s, "r")
	require.Equal(t, 1, claims)
	require.False(t, leased, "given back, so the next call can take it at once")

	require.ErrorIs(t, call(), context.DeadlineExceeded)
	require.NoError(t, call()) // claimed a third time: past MaxClaims
	require.Equal(t, int32(2), calls.Load())
	r, err := s.GetRun(context.Background(), "r")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusFailed, r.Status)
	require.Equal(t, "abandoned", r.State.Error.Type)
}

// TestShutdownWaitsForRunOnce: Shutdown waits for RunOnce's jobs, which
// Active counts. A job still running at the deadline is given back without
// counting, and RunOnce returns nil.
func TestShutdownWaitsForRunOnce(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "r")
	var started atomic.Int32
	w := &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, _ workflow.Job) (any, error) {
				started.Add(1)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	result := make(chan error, 1)
	go func() { result <- w.RunOnce(context.Background()) }()
	// Not Active: it counts the claim query too, before the job is held.
	waitUntil(t, "the handler runs", func() bool { return started.Load() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, w.Shutdown(ctx), context.DeadlineExceeded)
	select {
	case err := <-result:
		require.NoError(t, err)
	default:
		t.Fatal("Shutdown returned before RunOnce")
	}
	require.Equal(t, 0, w.Active())
	claims, leased := soleJob(t, s, "r")
	require.Equal(t, 0, claims)
	require.False(t, leased)
}

// TestRunOnceWithClaimingOff: after Shutdown, or while MayClaim returns
// false, RunOnce claims nothing and returns nil.
func TestRunOnceWithClaimingOff(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "r")
	handlers, calls := counting()

	paused := &Worker{Store: s, Handlers: handlers, MayClaim: func() bool { return false }}
	require.NoError(t, runOnceWithin(t, paused, context.Background(), 10*time.Second))

	stopped := &Worker{Store: s, Handlers: handlers}
	require.NoError(t, stopped.Shutdown(context.Background()))
	require.NoError(t, runOnceWithin(t, stopped, context.Background(), 10*time.Second))

	require.Equal(t, 0, calls("r"))
	claims, _ := soleJob(t, s, "r")
	require.Equal(t, 0, claims)
}

// TestRunOnceReturnsAClaimError: a claim that fails ends the call with
// its error.
func TestRunOnceReturnsAClaimError(t *testing.T) {
	s := newStore(t)
	missing, err := New(s.pool, kinds, WithSchema(s.schema+"_missing")) // never migrated
	require.NoError(t, err)
	handlers, _ := counting()
	w := &Worker{Store: missing, Handlers: handlers, Logger: slog.New(slog.DiscardHandler)}

	err = runOnceWithin(t, w, context.Background(), 10*time.Second)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "does not exist"), err.Error())
}

// TestRunOnceDoesNotListen: RunOnce opens no LISTEN connection, so a
// ListenConfig that cannot connect changes nothing.
func TestRunOnceDoesNotListen(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "r")
	cfg, err := pgx.ParseConfig("postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	var logs syncBuffer
	handlers, _ := counting()
	w := &Worker{
		Store:        s,
		Handlers:     handlers,
		ListenConfig: cfg,
		Logger:       slog.New(slog.NewTextHandler(&logs, nil)),
	}

	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "r"))
	require.False(t, strings.Contains(logs.String(), "listen"), logs.String())
}

// TestRunOnceKeepsGoingAfterAJobError: a job whose result cannot be
// recorded does not stop the call. The other due job still runs, and
// RunOnce returns the first job error once nothing is in flight.
func TestRunOnceKeepsGoingAfterAJobError(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "bad", "good") // claimed in this order
	broken := errors.New("settle broke")
	handlers, calls := counting()
	w := &Worker{
		Store:       s,
		Handlers:    handlers,
		Concurrency: 1,
		Lease:       300 * time.Millisecond,
		Logger:      slog.New(slog.DiscardHandler),
		settleHook: func(t claimedJob) error {
			if t.RunID == "bad" {
				return broken
			}
			return nil
		},
	}

	require.ErrorIs(t, runOnceWithin(t, w, context.Background(), 10*time.Second), broken)
	require.Equal(t, 1, calls("good"))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "good"))
	// The bad job was still due once its lease ran out, so the call may
	// have claimed it again, up to MaxClaims.
	require.GreaterOrEqual(t, calls("bad"), 1)
}

// TestRunOnceLostLeaseIsNotAnError: a job whose claim is gone when its
// result arrives (here, its run was cancelled) is not an error.
func TestRunOnceLostLeaseIsNotAnError(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "r")
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			if _, err := s.Cancel(ctx, job.RunID, "stop"); err != nil {
				return nil, err
			}
			return "ok", nil
		},
	}}

	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, workflow.StatusCancelled, runStatus(t, s, "r"))
}

// breakDefinitions returns a store over s's tables whose cache holds only
// the definitions of keep, and makes loading any other definition fail
// with a database error (not ErrDefinitionUnavailable).
func breakDefinitions(t *testing.T, s *Store, keep ...string) *Store {
	t.Helper()
	ctx := context.Background()
	fresh, err := New(s.pool, kinds, WithSchema(s.schema))
	require.NoError(t, err)
	for _, runID := range keep {
		r, err := s.GetRun(ctx, runID)
		require.NoError(t, err)
		_, err = fresh.core.Definition(ctx, fresh.backend, r.DefinitionHash)
		require.NoError(t, err)
	}
	_, err = s.pool.Exec(ctx,
		s.sql(`ALTER TABLE {{schema}}.definitions RENAME COLUMN document TO broken`))
	require.NoError(t, err)
	return fresh
}

// TestRunOnceReportsADefinitionLoadError: a job whose definition cannot be
// read for a database error stays under its lease; the other due job still
// runs, and RunOnce returns the load error.
func TestRunOnceReportsADefinitionLoadError(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "good")
	startRuns(t, s, `{"steps": [{"name": "b", "action": "x"}]}`, "bad")
	fresh := breakDefinitions(t, s, "good")
	handlers, calls := counting()
	w := &Worker{
		Store:       fresh,
		Handlers:    handlers,
		Concurrency: 1,
		Logger:      slog.New(slog.DiscardHandler),
	}

	err := runOnceWithin(t, w, context.Background(), 10*time.Second)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrDefinitionUnavailable), err.Error())
	require.True(t, strings.Contains(err.Error(), "load definition"), err.Error())
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "good"))
	require.Equal(t, 0, calls("bad"))
	claims, leased := soleJob(t, s, "bad")
	require.Equal(t, 1, claims)
	require.True(t, leased, "left under its lease")
}

// TestOverlappingRunOnceCallsEachHaveTheirConcurrency: Concurrency caps
// each call, so two overlapping calls run up to twice as many jobs.
func TestOverlappingRunOnceCallsEachHaveTheirConcurrency(t *testing.T) {
	s := newStore(t)
	ids := []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"}
	startRuns(t, s, oneStep, ids...)
	var now, most atomic.Int32
	w := &Worker{
		Store:       s,
		Concurrency: 2,
		Handlers: map[string]workflow.Handler{
			"action": func(context.Context, workflow.Job) (any, error) {
				n := now.Add(1)
				defer now.Add(-1)
				for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
				}
				// Hold the slot until both calls fill theirs, or a while.
				for deadline := time.Now().Add(300 * time.Millisecond); now.Load() < 4 &&
					time.Now().Before(deadline); time.Sleep(time.Millisecond) {
				}
				return "ok", nil
			},
		},
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { require.NoError(t, w.RunOnce(context.Background())) })
	}
	wg.Wait()
	require.Equal(t, int32(4), most.Load())
	for _, id := range ids {
		require.Equal(t, workflow.StatusCompleted, runStatus(t, s, id))
	}
}

// TestClaimIsExclusiveAcrossRunRunOnceAndExecute: a Run worker, repeated
// RunOnce calls and Execute calls that deliver every task twice all work
// the same runs at once. Each step attempt runs exactly once.
func TestClaimIsExclusiveAcrossRunRunOnceAndExecute(t *testing.T) {
	s := newStore(t)
	var (
		mu    sync.Mutex
		ran   = map[string]int{} // run + StepID
		tasks []JobRef
	)
	handlers := map[string]workflow.Handler{
		"action": func(_ context.Context, job workflow.Job) (any, error) {
			mu.Lock()
			ran[job.RunID+" "+string(job.StepID)]++
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			return "ok", nil
		},
	}
	var ids []string
	for i := range 24 {
		id := fmt.Sprintf("r%02d", i)
		ids = append(ids, id)
		tasks = append(tasks, start(t, s, chain, id)...)
	}
	done := func() bool {
		for _, id := range ids {
			if runStatus(t, s, id) != workflow.StatusCompleted {
				return false
			}
		}
		return true
	}

	goRun(t, &Worker{Store: s, Handlers: handlers, Concurrency: 4})
	drainer := &Worker{Store: s, Handlers: handlers, Concurrency: 4}
	pusher := &Worker{Store: s, Handlers: handlers}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for !done() {
				require.NoError(t, drainer.RunOnce(context.Background()))
			}
		})
	}
	var execute func(ref JobRef)
	execute = func(ref JobRef) {
		ex, err := pusher.Execute(context.Background(), ref)
		if err != nil && !errors.Is(err, ErrNotClaimable) {
			t.Errorf("Execute %s %s: %v", ref.RunID, ref.StepID, err)
		}
		for _, next := range ex.Jobs {
			for range 2 { // every task delivered twice
				wg.Go(func() { execute(next) })
			}
		}
	}
	for _, ref := range tasks {
		for range 2 {
			wg.Go(func() { execute(ref) })
		}
	}
	wg.Wait()
	waitUntil(t, "every run completes", done)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, ran, len(ids)*3)
	for attempt, n := range ran {
		require.Equal(t, 1, n, attempt)
	}
}
