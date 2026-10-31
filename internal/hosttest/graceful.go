package hosttest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

const oneStep = `{"steps": [{"name": "a", "action": "x"}]}`

// soleJob reads the claims of a run's only job, and whether it is leased.
func soleJob(t *testing.T, s *Env, runID string) (claims int, leased bool) {
	t.Helper()
	jobs := s.jobs(t, runID)
	require.Len(t, jobs, 1)
	return jobs[0].Claims, jobs[0].LeaseOwner != ""
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

func startRuns(t *testing.T, s *Env, doc string, ids ...string) {
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

// TestShutdownRecordsAndGivesBack: at Shutdown one step finishes before the
// deadline and is recorded; the other is still running at the deadline, is
// cancelled, and is given back without counting as a claim.
func testShutdownRecordsAndGivesBack(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "fast", "slow")
	finish := make(chan struct{})
	var cancelled atomic.Bool
	w := &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.RunID == "fast" {
					<-finish
					return "done", nil
				}
				<-ctx.Done()
				cancelled.Store(true)
				return nil, ctx.Err()
			},
		},
	}
	require.Equal(t, 0, w.Active())
	done := goRun(t, w)
	waitUntil(t, "both are active", func() bool { return w.Active() >= 2 })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- w.Shutdown(ctx) }()
	close(finish)
	require.ErrorIs(t, await(t, "Shutdown returns", result), context.DeadlineExceeded)
	<-done // Run returned nil

	require.Equal(t, 0, w.Active())
	require.True(t, cancelled.Load())
	r, err := s.GetRun(context.Background(), "fast")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, r.Status)
	claims, leased := soleJob(t, s, "slow")
	require.Equal(t, 0, claims)
	require.False(t, leased)
}

// TestShutdownReturnsNilWhenJobsFinish: every active job finishes before
// the deadline, so Shutdown returns nil as soon as they are recorded.
func testShutdownReturnsNilWhenJobsFinish(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "r")
	started := make(chan struct{})
	w := &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				close(started)
				time.Sleep(50 * time.Millisecond)
				return "done", nil
			},
		},
	}
	goRun(t, w)
	await(t, "the handler starts", started)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	begin := time.Now()
	require.NoError(t, w.Shutdown(ctx))
	require.True(t, time.Since(begin) < 5*time.Second, time.Since(begin))
	r, err := s.GetRun(context.Background(), "r")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, r.Status)
}

// TestShutdownClaimsNothingNew: a step that becomes due after Shutdown is
// called stays unclaimed while the active job finishes.
func testShutdownClaimsNothingNew(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "first")
	started, finish := make(chan struct{}), make(chan struct{})
	w := &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				close(started)
				<-finish
				return "done", nil
			},
		},
	}
	goRun(t, w)
	await(t, "the handler starts", started)
	result := make(chan error, 1)
	go func() { result <- w.Shutdown(context.Background()) }()
	waitUntil(t, "shutdown starts", w.isStopped)
	startRuns(t, s, oneStep, "second")
	time.Sleep(5 * w.Poll) // several chances to claim
	close(finish)
	require.NoError(t, await(t, "Shutdown returns", result))

	r, err := s.GetRun(context.Background(), "first")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, r.Status)
	claims, leased := soleJob(t, s, "second")
	require.Equal(t, 0, claims)
	require.False(t, leased)
}

// TestClosedGateGivesBackALateClaim: the claim gate closes while a claim
// query is open. The jobs it returns are given back unrun, for Shutdown and
// for MayClaim alike, so Active() == 0 means the worker holds nothing. A
// hard stop does the same, and it does not cancel the claim query: rows
// that a cancelled query committed would be lost (#111).
func testClosedGateGivesBackALateClaim(t *testing.T, h Harness) {
	for _, gate := range []string{"Shutdown", "MayClaim", "hard stop"} {
		t.Run(gate, func(t *testing.T) {
			s := open(t, h)
			startRuns(t, s, oneStep, "late")
			var (
				open  atomic.Bool
				calls atomic.Int32
				once  sync.Once
			)
			open.Store(true)
			w := &Worker{
				Store:    s,
				MayClaim: open.Load,
				Handlers: map[string]workflow.Handler{
					"action": func(ctx context.Context, job workflow.Job) (any, error) {
						calls.Add(1)
						return "done", nil
					},
				},
			}
			shut := make(chan error, 1)
			closed := make(chan struct{})
			rctx, hardStop := context.WithCancel(context.Background())
			defer hardStop()
			var (
				claimed  bool  // the claim query returned without error
				claimErr error // its context once it returned
			)
			if gate == "hard stop" {
				// The stop lands just before the query, so the query runs
				// while it is in effect.
				var stopOnce sync.Once
				w.beforeClaim = func(context.Context) { stopOnce.Do(hardStop) }
			}
			w.claimHook = func(ctx context.Context) {
				once.Do(func() {
					switch gate {
					case "MayClaim":
						open.Store(false)
						close(closed)
					case "hard stop":
						claimed, claimErr = true, ctx.Err()
					default:
						go func() { shut <- w.Shutdown(context.Background()) }()
						waitUntil(t, "shutdown starts", w.isStopped)
					}
				})
			}
			switch gate {
			case "hard stop":
				done := make(chan error, 1)
				go func() { done <- w.Run(rctx) }()
				require.NoError(t, await(t, "Run returns", done))
				require.True(t, claimed, "the hard stop cut off the claim query")
				require.NoError(t, claimErr, "the hard stop cancelled the claim query")
				require.Equal(t, 0, w.Active())
			case "Shutdown":
				goRun(t, w)
				require.NoError(t, await(t, "Shutdown returns", shut))
				require.Equal(t, 0, w.Active())
			default:
				goRun(t, w)
				// The claim counts as active until its jobs are given back.
				// Each later poll counts for a moment too, before it finds
				// the gate closed, so Active is not checked again after.
				await(t, "the gate closes on a claim", closed)
				waitUntil(t, "the claim ends", func() bool { return w.Active() == 0 })
			}

			require.Equal(t, int32(0), calls.Load())
			claims, leased := soleJob(t, s, "late")
			require.Equal(t, 0, claims)
			require.False(t, leased)
		})
	}
}

// TestMayClaimPausesAndResumes: while MayClaim returns false, no step and
// no timer is claimed, and the job the worker holds finishes; once it
// returns true, claiming resumes without restarting Run.
func testMayClaimPausesAndResumes(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "held")
	var (
		open atomic.Bool
		once sync.Once
	)
	open.Store(true)
	started, finish := make(chan struct{}), make(chan struct{})
	w := &Worker{
		Store:    s,
		MayClaim: open.Load,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				once.Do(func() {
					close(started)
					<-finish
				})
				return "done", nil
			},
		},
	}
	goRun(t, w)
	await(t, "the handler starts", started)
	open.Store(false)
	startRuns(t, s, oneStep, "step")
	napThenA := `{"steps": [{"name": "nap", "sleep": "1ms", "next": ["a"]},
		{"name": "a", "action": "x"}]}`
	startRuns(t, s, napThenA, "timer")
	close(finish)
	waitStatus(t, s, "held", workflow.StatusCompleted)
	time.Sleep(5 * w.Poll) // several chances to claim
	for _, id := range []string{"step", "timer"} {
		claims, leased := soleJob(t, s, id)
		require.Equal(t, 0, claims, id)
		require.False(t, leased, id)
	}
	require.Equal(t, 0, w.Active())

	open.Store(true)
	waitStatus(t, s, "step", workflow.StatusCompleted)
	waitStatus(t, s, "timer", workflow.StatusCompleted)
}

// TestShutdownKeepsHeartbeating: a handler that runs longer than the lease
// while the worker shuts down keeps its claim. Another worker never runs
// it, and its result is recorded.
func testShutdownKeepsHeartbeating(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "long")
	var calls atomic.Int32
	started := make(chan struct{})
	w := &Worker{
		Store: s,
		Lease: 300 * time.Millisecond,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				calls.Add(1)
				close(started)
				sleep(ctx, time.Second)
				return "done", ctx.Err()
			},
		},
	}
	goRun(t, w)
	await(t, "the handler starts", started)
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				calls.Add(1)
				return "stolen", nil
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, w.Shutdown(ctx))
	waitStatus(t, s, "long", workflow.StatusCompleted)
	steps, err := s.Steps(context.Background(), "long")
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.Equal(t, any("done"), steps[0].Output)
	require.Equal(t, int32(1), calls.Load())
}

// lockedAfterStart returns a handler that returns "done" once the test
// holds runID's lock, and lock, which waits for the handler to start,
// takes the lock and lets the handler return. The lock comes after the
// claim because on SQLite it blocks every write, the claim's too.
//
// Call lock after Run starts. If lock fails before it takes the lock, a
// cleanup that lock registers releases the handler. That cleanup runs
// before goRun's, which waits for Run and so for the handler.
func lockedAfterStart(
	t *testing.T, s *Env, runID string,
) (workflow.Handler, func() (release func())) {
	started, locked := make(chan struct{}), make(chan struct{})
	var once sync.Once
	handler := func(context.Context, workflow.Job) (any, error) {
		once.Do(func() { close(started) })
		<-locked
		return "done", nil
	}
	lock := func() func() {
		t.Helper()
		unlock := sync.OnceFunc(func() { close(locked) })
		t.Cleanup(unlock)
		await(t, "the handler starts", started)
		release := s.holdRunLock(t, runID)
		unlock()
		return release
	}
	return handler, lock
}

// TestShutdownIsBoundedByItsDeadline holds the run's lock so a finished
// result cannot be recorded. With a one-minute lease, Shutdown still returns
// soon after its deadline. With row locks the job is given back; where one
// lock covers the database the give-back waits on it too, and the job is
// left to its lease (see TestShutdownUnderAHeldLock for the bounds).
func testShutdownIsBoundedByItsDeadline(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "locked")
	ctx := context.Background()
	handler, lock := lockedAfterStart(t, s, "locked")

	finished := make(chan struct{})
	w := &Worker{
		Store:       s,
		Lease:       time.Minute,
		Handlers:    map[string]workflow.Handler{"action": handler},
		handlerDone: func(claimedJob, any, error) { close(finished) },
	}
	goRun(t, w)
	release := lock()
	defer release()
	await(t, "the handler returns", finished)
	sctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	begin := time.Now()
	require.ErrorIs(t, w.Shutdown(sctx), context.DeadlineExceeded)
	bound := 3 * time.Second
	if !s.caps().RowLocks {
		bound = shutdownBound(s.caps())
	}
	require.True(t, time.Since(begin) < bound, time.Since(begin))
	claims, leased := soleJob(t, s, "locked")
	if s.caps().RowLocks {
		require.Equal(t, 0, claims)
		require.False(t, leased)
	} else {
		require.Equal(t, 1, claims, "left to its lease")
		require.True(t, leased)
	}
}

// TestShutdownWithoutRun: Shutdown with no Run returns at once, and a Run
// that starts after it returns nil without claiming and logs a warning.
func testShutdownWithoutRun(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "idle")
	var logs syncBuffer
	w := &Worker{
		Store:    s,
		Logger:   slog.New(slog.NewTextHandler(&logs, nil)),
		Handlers: map[string]workflow.Handler{"action": nil},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, w.Shutdown(ctx))
	require.NoError(t, w.Run(ctx))
	require.Contains(t, logs.String(), "Run called after Shutdown")
	claims, leased := soleJob(t, s, "idle")
	require.Equal(t, 0, claims)
	require.False(t, leased)

	// A worker that has finished has nothing to cut off, whatever the
	// context: a done one does not turn the answer into its error.
	cancel()
	for range 20 {
		require.NoError(t, w.Shutdown(ctx))
	}
	require.NotContains(t, logs.String(), "deadline passed")
}

// TestConcurrentShutdowns: two Shutdown calls wait together; the first
// deadline cuts off the active job for both.
func testConcurrentShutdowns(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "r")
	started := make(chan struct{})
	w := &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	goRun(t, w)
	await(t, "the handler starts", started)
	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- w.Shutdown(context.Background()) }()
	go func() { results <- w.Shutdown(short) }()
	errs := []error{<-results, <-results}
	cutOff := func(err error) bool { return errors.Is(err, context.DeadlineExceeded) }
	require.True(t, cutOff(errs[0]) != cutOff(errs[1]), errs) // only the short one
	require.Equal(t, 0, w.Active())
}

// TestHardStopDuringShutdown: cancelling Run's context while Shutdown waits
// is a hard stop. Shutdown reports it with ErrHardStop and a warning.
func testHardStopDuringShutdown(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "r")
	var logs syncBuffer
	started := make(chan struct{})
	w := &Worker{
		Store:  s,
		Poll:   10 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	rctx, hardStop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(rctx) }()
	await(t, "the handler starts", started)
	result := make(chan error, 1)
	go func() { result <- w.Shutdown(context.Background()) }()
	waitUntil(t, "shutdown starts", w.isStopped)
	hardStop()
	require.NoError(t, await(t, "Run returns", done))
	err := await(t, "Shutdown returns", result)
	require.ErrorIs(t, err, ErrHardStop)
	require.False(t, errors.Is(err, context.DeadlineExceeded), err)
	require.Contains(t, logs.String(), "Run's context ended before Shutdown finished")
	claims, leased := soleJob(t, s, "r")
	require.Equal(t, 0, claims)
	require.False(t, leased)
}

// TestShutdownRecordsLateResults: a handler that ignores its context and
// returns after the deadline still has its result recorded; a hand-off that
// returns after the deadline still has its job deleted.
func testShutdownRecordsLateResults(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "result", "handoff")
	w := &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				<-ctx.Done()
				time.Sleep(200 * time.Millisecond) // past the deadline
				if job.RunID == "handoff" {
					return nil, workflow.ErrDeferred
				}
				return "late", nil
			},
		},
	}
	goRun(t, w)
	waitUntil(t, "both are active", func() bool { return w.Active() >= 2 })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, w.Shutdown(ctx), context.DeadlineExceeded)

	r, err := s.GetRun(context.Background(), "result")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, r.Status)
	require.Equal(t, 0, countJobs(t, s, "handoff"))
}

// TestShutdownLetsAWriteInProgressFinish: a write that is in progress when
// the deadline passes is not cancelled at once. It ends 300 ms later, within
// its second, and the result is recorded.
func testShutdownLetsAWriteInProgressFinish(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "slowwrite")
	var once sync.Once
	writing := make(chan struct{})
	w := &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				return "done", nil
			},
		},
	}
	w.settleHook = func(claimedJob) error {
		once.Do(func() {
			close(writing)
			waitUntil(t, "the deadline passes", w.cutDone)
			time.Sleep(300 * time.Millisecond)
		})
		return nil
	}
	goRun(t, w)
	await(t, "the settle starts", writing)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, w.Shutdown(ctx), context.DeadlineExceeded)
	r, err := s.GetRun(context.Background(), "slowwrite")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, r.Status)
}

// TestHardStopBeforeShutdown is the usual misuse: Run gets the context that
// SIGTERM cancels, so Run stops hard before the application calls Shutdown.
// Shutdown does not report a clean stop.
func testHardStopBeforeShutdown(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "r")
	var logs syncBuffer
	started := make(chan struct{})
	w := &Worker{
		Store:  s,
		Poll:   10 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	sigCtx, sigterm := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(sigCtx) }()
	await(t, "the handler starts", started)
	sigterm()
	require.NoError(t, await(t, "Run returns", done))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.ErrorIs(t, w.Shutdown(ctx), ErrHardStop)
	require.Contains(t, logs.String(), "Run's context ended before Shutdown finished")
	// The hard stop holds for the life of the worker.
	require.ErrorIs(t, w.Shutdown(ctx), ErrHardStop)
}

// TestHardStopDuringAClaim: Run's context ends during Shutdown while a claim
// query is open. Run can then return by its stopped-and-idle check instead
// of its select, and Shutdown must still report the hard stop.
func testHardStopDuringAClaim(t *testing.T, h Harness) {
	s := open(t, h)
	var once sync.Once
	inClaim, resume := make(chan struct{}), make(chan struct{})
	w := &Worker{
		Store:    s,
		Poll:     10 * time.Millisecond,
		Handlers: map[string]workflow.Handler{"action": nil},
	}
	w.claimHook = func(context.Context) {
		once.Do(func() {
			close(inClaim)
			<-resume
		})
	}
	rctx, hardStop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(rctx) }()
	await(t, "a claim is in progress", inClaim)
	result := make(chan error, 1)
	go func() { result <- w.Shutdown(context.Background()) }()
	waitUntil(t, "shutdown starts", w.isStopped)
	hardStop()
	close(resume)
	require.NoError(t, await(t, "Run returns", done))
	require.ErrorIs(t, await(t, "Shutdown returns", result), ErrHardStop)
}

// TestActiveCountsAClaimInProgress: the application closes the gate while a
// claim query is open. Active() counts that query until its jobs are given
// back, so waiting for Active() == 0 means the worker holds no claim.
func testActiveCountsAClaimInProgress(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "racing")
	var (
		open atomic.Bool
		once sync.Once
	)
	open.Store(true)
	closed, checked := make(chan struct{}), make(chan struct{})
	w := &Worker{
		Store:    s,
		MayClaim: open.Load,
		Handlers: map[string]workflow.Handler{"action": nil},
	}
	w.claimHook = func(context.Context) {
		once.Do(func() {
			open.Store(false) // the application closes the gate
			close(closed)
			<-checked
		})
	}
	goRun(t, w)
	await(t, "the gate closes", closed)
	n := w.Active()
	close(checked)
	require.Equal(t, 1, n)

	waitUntil(t, "no job is active", func() bool { return w.Active() == 0 })
	claims, leased := soleJob(t, s, "racing")
	require.Equal(t, 0, claims)
	require.False(t, leased)
}

// TestShutdownGivesBackALateBatchInParallel: Shutdown is called while a
// claim query returns several jobs, and every give-back hangs. They run in
// parallel, so Shutdown returns within its deadline plus 2 s, not plus one
// second per job.
func testShutdownGivesBackALateBatchInParallel(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "b1", "b2", "b3", "b4", "b5")
	var once sync.Once
	shut := make(chan error, 1)
	begin := make(chan time.Time, 1)
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{"action": nil}}
	w.claimHook = func(context.Context) {
		once.Do(func() {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				begin <- time.Now()
				shut <- w.Shutdown(ctx)
			}()
			waitUntil(t, "shutdown starts", w.isStopped)
		})
	}
	w.releaseHook = func(ctx context.Context) { <-ctx.Done() } // a database that hangs
	goRun(t, w)
	require.ErrorIs(t, await(t, "Shutdown returns", shut), context.DeadlineExceeded)
	took := time.Since(<-begin)
	require.True(t, took < 200*time.Millisecond+2*time.Second, took)
}

func stepDoc(name string) string {
	return `{"steps": [{"name": "` + name + `", "action": "x"}]}`
}

// TestShutdownObserverEvents: each Shutdown path fires Claimed, then exactly
// one ending event, and no ClaimError: a step recorded before the deadline,
// one given back at it, a late result, a result after the lease, a result
// that cannot be recorded, a late claim batch, a hung claim query, and a
// hard stop of Run.
func testShutdownObserverEvents(t *testing.T, h Harness) {
	ctx := context.Background()
	shortly := func() context.Context {
		c, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		t.Cleanup(cancel)
		return c
	}

	t.Run("deadline", func(t *testing.T) {
		s := open(t, h)
		startRuns(t, s, stepDoc("fast"), "fast")
		startRuns(t, s, stepDoc("slow"), "slow")
		startRuns(t, s, stepDoc("late"), "late")
		var log eventLog
		w := &Worker{
			Store:    s,
			Observer: log.observer(),
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					switch job.Step {
					case "fast":
						return "done", nil
					case "slow":
						<-ctx.Done()
						return nil, ctx.Err()
					}
					<-ctx.Done()
					time.Sleep(100 * time.Millisecond) // a result after the deadline
					return "late", nil
				},
			},
		}
		goRun(t, w)
		waitUntil(t, "fast settled, two running", func() bool {
			return log.count("settled:completed fast") == 1 && w.Active() >= 2
		})
		require.ErrorIs(t, w.Shutdown(shortly()), context.DeadlineExceeded)
		require.Equal(t, []string{
			"claimed fast", "claimed late", "claimed slow",
			"given_back slow", "settled:completed fast", "settled:completed late",
		}, log.sorted())
	})

	t.Run("result after the lease", func(t *testing.T) {
		const lease = 600 * time.Millisecond
		s := open(t, h)
		startRuns(t, s, stepDoc("stubborn"), "stubborn")
		var log eventLog
		started := make(chan struct{})
		w := &Worker{
			Store:       s,
			Lease:       lease,
			Observer:    log.observer(),
			Concurrency: 1,
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					close(started)
					<-ctx.Done()
					// No heartbeat after the deadline: the lease runs out
					// before the handler returns.
					time.Sleep(lease + 400*time.Millisecond)
					return "late", nil
				},
			},
		}
		goRun(t, w)
		await(t, "the handler starts", started)
		require.ErrorIs(t, w.Shutdown(shortly()), context.DeadlineExceeded)
		// Not given back: the claim was over, and it counts.
		require.Equal(t, []string{"claimed stubborn", "lease_lost stubborn"}, log.get())
		claims, _ := soleJob(t, s, "stubborn")
		require.Equal(t, 1, claims)
	})

	t.Run("unrecorded result", func(t *testing.T) {
		s := open(t, h)
		startRuns(t, s, stepDoc("locked"), "locked")
		handler, lock := lockedAfterStart(t, s, "locked")
		var log eventLog
		finished := make(chan struct{})
		w := &Worker{
			Store:    s,
			Lease:    time.Minute,
			Observer: log.observer(),
			// One slot, so no claim query runs while the lock is held: one
			// that waits on it past the busy timeout is a ClaimError.
			Concurrency: 1,
			Handlers:    map[string]workflow.Handler{"action": handler},
			handlerDone: func(claimedJob, any, error) { close(finished) },
		}
		goRun(t, w)
		release := lock()
		defer release()
		await(t, "the handler returns", finished)
		require.ErrorIs(t, w.Shutdown(shortly()), context.DeadlineExceeded)
		// Where one lock covers the database, the give-back waits on it
		// too: the claim is left to its lease.
		end := "given_back locked"
		if !s.caps().RowLocks {
			end = "lease_lost locked"
		}
		require.Equal(t, []string{"claimed locked", end}, log.get())
	})

	t.Run("late batch", func(t *testing.T) {
		s := open(t, h)
		startRuns(t, s, stepDoc("b1"), "b1")
		startRuns(t, s, stepDoc("b2"), "b2")
		var (
			log  eventLog
			once sync.Once
		)
		shut := make(chan error, 1)
		w := &Worker{
			Store:    s,
			Observer: log.observer(),
			Handlers: map[string]workflow.Handler{"action": nil},
		}
		w.claimHook = func(context.Context) {
			once.Do(func() {
				go func() { shut <- w.Shutdown(ctx) }()
				waitUntil(t, "shutdown starts", w.isStopped)
			})
		}
		goRun(t, w)
		require.NoError(t, await(t, "Shutdown returns", shut))
		require.Equal(t,
			[]string{"claimed b1", "claimed b2", "given_back b1", "given_back b2"}, log.sorted())
	})

	t.Run("hard stop", func(t *testing.T) {
		s := open(t, h)
		startRuns(t, s, stepDoc("cut"), "cut")
		var log eventLog
		started := make(chan struct{})
		w := &Worker{
			Store:    s,
			Poll:     10 * time.Millisecond,
			Observer: log.observer(),
			// One slot: a claim query beside the step could take the job
			// after its give-back and give it back again, two more events.
			// GracefulShutdownDoesNotCountAsAClaim covers that path.
			Concurrency: 1,
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
		}
		rctx, hardStop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- w.Run(rctx) }()
		await(t, "the handler starts", started)
		hardStop()
		require.NoError(t, await(t, "Run returns", done))
		require.ErrorIs(t, w.Shutdown(ctx), ErrHardStop)
		require.Equal(t, []string{"claimed cut", "given_back cut"}, log.get())
	})
}
