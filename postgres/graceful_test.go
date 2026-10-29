package postgres

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

// TestShutdownRecordsAndGivesBack: at Shutdown one step finishes before the
// deadline and is recorded; the other is still running at the deadline, is
// cancelled, and is given back without counting as a claim.
func TestShutdownRecordsAndGivesBack(t *testing.T) {
	s := newStore(t)
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
	waitUntil(t, "both are active", func() bool { return w.Active() == 2 })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- w.Shutdown(ctx) }()
	close(finish)
	require.ErrorIs(t, <-result, context.DeadlineExceeded)
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
func TestShutdownReturnsNilWhenJobsFinish(t *testing.T) {
	s := newStore(t)
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
	<-started
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
func TestShutdownClaimsNothingNew(t *testing.T) {
	s := newStore(t)
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
	<-started
	result := make(chan error, 1)
	go func() { result <- w.Shutdown(context.Background()) }()
	waitUntil(t, "shutdown starts", w.isStopped)
	startRuns(t, s, oneStep, "second")
	time.Sleep(5 * w.Poll) // several chances to claim
	close(finish)
	require.NoError(t, <-result)

	r, err := s.GetRun(context.Background(), "first")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, r.Status)
	claims, leased := soleJob(t, s, "second")
	require.Equal(t, 0, claims)
	require.False(t, leased)
}

// TestClosedGateGivesBackALateClaim: the claim gate closes while a claim
// query is open. The jobs it returns are given back unrun, for Shutdown and
// for MayClaim alike, so Active() == 0 means the worker holds nothing.
func TestClosedGateGivesBackALateClaim(t *testing.T) {
	for _, gate := range []string{"Shutdown", "MayClaim"} {
		t.Run(gate, func(t *testing.T) {
			s := newStore(t)
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
			w.claimHook = func(context.Context) {
				once.Do(func() {
					if gate == "MayClaim" {
						open.Store(false)
						return
					}
					go func() { shut <- w.Shutdown(context.Background()) }()
					waitUntil(t, "shutdown starts", w.isStopped)
				})
			}
			goRun(t, w)
			if gate == "Shutdown" {
				require.NoError(t, <-shut)
			} else {
				time.Sleep(5 * w.Poll)
			}

			require.Equal(t, 0, w.Active())
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
func TestMayClaimPausesAndResumes(t *testing.T) {
	s := newStore(t)
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
	<-started
	open.Store(false)
	startRuns(t, s, oneStep, "step")
	startRuns(t, s, `{"steps": [{"name": "nap", "sleep": "1ms", "next": ["a"]}, {"name": "a", "action": "x"}]}`, "timer")
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
func TestShutdownKeepsHeartbeating(t *testing.T) {
	s := newStore(t)
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
	<-started
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

// TestShutdownIsBoundedByItsDeadline holds the run's row lock so a finished
// result cannot be recorded. With a one-minute lease, Shutdown still returns
// soon after its deadline, and the job is given back.
func TestShutdownIsBoundedByItsDeadline(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "locked")
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, s.sql(`SELECT 1 FROM {{schema}}.runs WHERE id = 'locked' FOR UPDATE`))
	require.NoError(t, err)

	finished := make(chan struct{})
	w := &Worker{
		Store: s,
		Lease: time.Minute,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				return "done", nil
			},
		},
		handlerDone: func(claimedJob, any, error) { close(finished) },
	}
	goRun(t, w)
	<-finished
	sctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	begin := time.Now()
	require.ErrorIs(t, w.Shutdown(sctx), context.DeadlineExceeded)
	require.True(t, time.Since(begin) < 3*time.Second, time.Since(begin))
	claims, leased := soleJob(t, s, "locked")
	require.Equal(t, 0, claims)
	require.False(t, leased)
}

// TestShutdownWithoutRun: Shutdown with no Run returns at once, and a Run
// that starts after it returns nil without claiming and logs a warning.
func TestShutdownWithoutRun(t *testing.T) {
	s := newStore(t)
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
}

// TestConcurrentShutdowns: two Shutdown calls wait together; the first
// deadline cuts off the active job for both.
func TestConcurrentShutdowns(t *testing.T) {
	s := newStore(t)
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
	<-started
	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- w.Shutdown(context.Background()) }()
	go func() { results <- w.Shutdown(short) }()
	errs := []error{<-results, <-results}
	require.True(t, errors.Is(errs[0], context.DeadlineExceeded) != errors.Is(errs[1], context.DeadlineExceeded), errs)
	require.Equal(t, 0, w.Active())
}

// TestHardStopDuringShutdown: cancelling Run's context while Shutdown waits
// is a hard stop. Shutdown reports it with ErrHardStop and a warning.
func TestHardStopDuringShutdown(t *testing.T) {
	s := newStore(t)
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
	<-started
	result := make(chan error, 1)
	go func() { result <- w.Shutdown(context.Background()) }()
	waitUntil(t, "shutdown starts", w.isStopped)
	hardStop()
	require.NoError(t, <-done)
	err := <-result
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
func TestShutdownRecordsLateResults(t *testing.T) {
	s := newStore(t)
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
	waitUntil(t, "both are active", func() bool { return w.Active() == 2 })
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
func TestShutdownLetsAWriteInProgressFinish(t *testing.T) {
	s := newStore(t)
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
	<-writing
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, w.Shutdown(ctx), context.DeadlineExceeded)
	r, err := s.GetRun(context.Background(), "slowwrite")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, r.Status)
}

// TestShutdownCancelsAHungClaim: a claim query that hangs is cancelled a
// second after the deadline, so Shutdown returns.
func TestShutdownCancelsAHungClaim(t *testing.T) {
	s := newStore(t)
	var once sync.Once
	hung := make(chan struct{})
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{"action": nil}}
	w.claimHook = func(ctx context.Context) {
		once.Do(func() {
			close(hung)
			<-ctx.Done()
		})
	}
	goRun(t, w)
	<-hung
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	begin := time.Now()
	require.ErrorIs(t, w.Shutdown(ctx), context.DeadlineExceeded)
	require.True(t, time.Since(begin) < 3*time.Second, time.Since(begin))
	require.True(t, time.Since(begin) >= time.Second, time.Since(begin))
}

// TestHardStopBeforeShutdown is the usual misuse: Run gets the context that
// SIGTERM cancels, so Run stops hard before the application calls Shutdown.
// Shutdown does not report a clean stop.
func TestHardStopBeforeShutdown(t *testing.T) {
	s := newStore(t)
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
	<-started
	sigterm()
	require.NoError(t, <-done)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.ErrorIs(t, w.Shutdown(ctx), ErrHardStop)
	require.Contains(t, logs.String(), "Run's context ended before Shutdown finished")
}
