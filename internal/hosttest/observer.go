package hosttest

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// eventLog records observer events as "<event> <step>", with "(timer)"
// after the step name of a timer.
type eventLog struct {
	mu      sync.Mutex
	events  []string
	settled []Settlement
}

func (r *eventLog) add(event string, job workflow.Job) {
	name := job.Step
	if job.Kind == workflow.CommandScheduleTimer {
		name += "(timer)"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event+" "+name)
}

func (r *eventLog) observer() Observer {
	return Observer{
		Claimed: func(job workflow.Job) { r.add("claimed", job) },
		Settled: func(job workflow.Job, s Settlement) {
			r.add("settled:"+string(s.Outcome), job)
			r.mu.Lock()
			defer r.mu.Unlock()
			r.settled = append(r.settled, s)
		},
		Cancelled: func(job workflow.Job) { r.add("cancelled", job) },
		LeaseLost: func(job workflow.Job) { r.add("lease_lost", job) },
		GivenBack: func(job workflow.Job) { r.add("given_back", job) },
		Abandoned: func(job workflow.Job) { r.add("abandoned", job) },
		ClaimError: func(err error) {
			r.add("claim_error", workflow.Job{})
		},
	}
}

func (r *eventLog) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// sorted returns the events in order, for runs whose events may interleave.
func (r *eventLog) sorted() []string {
	out := r.get()
	slices.Sort(out)
	return out
}

func (r *eventLog) count(event string) int {
	n := 0
	for _, e := range r.get() {
		if e == event {
			n++
		}
	}
	return n
}

func testObserverSettled(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [
	  {"name": "work", "action": "x", "next": ["nap"]},
	  {"name": "nap", "sleep": "1ms", "next": ["boom"]},
	  {"name": "boom", "action": "x"}]}`)
	var rec eventLog
	stop := runWorker(t, &Worker{
		Store:    s,
		Observer: rec.observer(),
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.Step == "boom" {
					return nil, &workflow.StepError{Type: "broken", Message: "no\x00pe"}
				}
				time.Sleep(50 * time.Millisecond)
				return "done", nil
			},
		},
	})
	_, err := s.Start(context.Background(), NewRun{ID: "settled", Definition: def})
	require.NoError(t, err)
	waitStatus(t, s, "settled", workflow.StatusFailed)
	stop()

	require.Equal(t, []string{
		"claimed work", "settled:completed work",
		"claimed nap(timer)", "settled:fired nap(timer)",
		"claimed boom", "settled:failed boom",
	}, rec.get())
	require.True(t, rec.settled[0].Duration >= 50*time.Millisecond, rec.settled[0].Duration)
	require.Nil(t, rec.settled[0].Error)
	require.Equal(t, time.Duration(0), rec.settled[1].Duration)
	require.Equal(t, "broken", rec.settled[2].Error.Type)
	// The error the store recorded, with its NUL replaced.
	require.Equal(t, "no\uFFFDpe", rec.settled[2].Error.Message)
}

func testObserverSettledDeferred(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [{"name": "ask", "agent": "human"}]}`)
	var rec eventLog
	stop := runWorker(t, &Worker{
		Store:    s,
		Observer: rec.observer(),
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				return nil, workflow.ErrDeferred
			},
		},
	})
	_, err := s.Start(context.Background(), NewRun{ID: "ask", Definition: def})
	require.NoError(t, err)
	waitFor(t, "the deferred event", func() bool { return len(rec.get()) == 2 })
	stop()
	require.Equal(t, []string{"claimed ask", "settled:deferred ask"}, rec.get())
}

func testObserverSettledInvalidOutput(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
	var rec eventLog
	stop := runWorker(t, &Worker{
		Store:    s,
		Observer: rec.observer(),
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				return map[string]any{"score": math.NaN()}, nil
			},
		},
	})
	_, err := s.Start(context.Background(), NewRun{ID: "nan", Definition: def})
	require.NoError(t, err)
	waitStatus(t, s, "nan", workflow.StatusFailed)
	stop()
	require.Equal(t, []string{"claimed a", "settled:failed a"}, rec.get())
	require.Equal(t, workflow.ErrorTypeInvalidOutput, rec.settled[0].Error.Type)
}

// TestObserverCancelled: a run cancelled while its step runs, and a step
// that times out, fire Cancelled and not LeaseLost. The heartbeat that finds
// the job gone must run within a lease of the last one, or the claim counts
// as lost, so the leases leave room for a slow test runner.
func testObserverCancelled(t *testing.T, h Harness) {
	t.Run("run cancelled", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "slow", "action": "x"}]}`)
		var rec eventLog
		started := make(chan struct{})
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    600 * time.Millisecond,
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
		})
		ctx := context.Background()
		_, err := s.Start(ctx, NewRun{ID: "cancel-me", Definition: def})
		require.NoError(t, err)
		await(t, "the handler starts", started)
		_, err = s.Cancel(ctx, "cancel-me", "superseded")
		require.NoError(t, err)
		waitFor(t, "the cancelled event", func() bool { return len(rec.get()) == 2 })
		stop()
		require.Equal(t, []string{"claimed slow", "cancelled slow"}, rec.get())
	})

	t.Run("step timed out", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "slow", "action": "x", "timeout": "100ms"}]}`)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    600 * time.Millisecond,
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
		})
		_, err := s.Start(context.Background(), NewRun{ID: "timeout", Definition: def})
		require.NoError(t, err)
		waitStatus(t, s, "timeout", workflow.StatusFailed)
		waitFor(t, "the cancelled event", func() bool { return rec.count("cancelled slow") == 1 })
		stop()
		require.Equal(t, []string{
			"cancelled slow", "claimed slow", "claimed slow(timer)", "settled:fired slow(timer)",
		}, rec.sorted())
	})

	t.Run("result rejected", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
		ctx := context.Background()
		_, err := s.Start(ctx, NewRun{ID: "rejected", Definition: def})
		require.NoError(t, err)
		var rec eventLog
		w := &Worker{Store: s, Observer: rec.observer(), Handlers: map[string]workflow.Handler{"action": nil}}
		w.defaults()
		c, err := w.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c, 1)
		// An attempt the run never dispatched: Apply rejects it.
		w.settle(ctx, c[0], time.Now(), workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/a#2",
		}, w.Logger)
		require.Equal(t, []string{"cancelled a"}, rec.get())
		require.Equal(t, 0, countJobs(t, s, "rejected"))
	})
}

func testObserverLeaseLost(t *testing.T, h Harness) {
	t.Run("reclaimed", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
		ctx := context.Background()
		var rec eventLog
		started := make(chan struct{})
		stop := runWorker(t, &Worker{
			Store: s,
			Lease: 300 * time.Millisecond,
			// It must not reclaim the job itself once the lease is expired.
			// A poll could, and so could a wake-up left over from Start's
			// notification. One slot rules out both while the step runs.
			Poll:        time.Minute,
			Concurrency: 1,
			Observer:    rec.observer(),
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
		})
		_, err := s.Start(ctx, NewRun{ID: "reclaimed", Definition: def})
		require.NoError(t, err)
		await(t, "the handler starts", started)
		expireLeases(t, s, "reclaimed")
		other := &Worker{Store: s, ID: "other", Handlers: map[string]workflow.Handler{"action": nil}}
		other.defaults()
		c, err := other.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c, 1)
		waitFor(t, "the lost lease", func() bool { return len(rec.get()) == 2 })
		stop()
		require.Equal(t, []string{"claimed a", "lease_lost a"}, rec.get())
	})

	// Worker A is paused past its lease. Worker B claims the job and settles
	// it, which deletes the row, so A's next heartbeat finds no job. That is
	// a lost claim, not a cancel.
	t.Run("reclaimed and settled", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
		ctx := context.Background()
		const lease = 300 * time.Millisecond
		var (
			rec    eventLog
			pause  sync.Once
			paused = make(chan struct{})
			resume = make(chan struct{})
		)
		stop := runWorker(t, &Worker{
			Store:       s,
			Lease:       lease,
			Poll:        time.Minute,
			Concurrency: 1, // as above: it must not reclaim the job itself
			Observer:    rec.observer(),
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
			heartbeatHook: func() error {
				pause.Do(func() {
					close(paused)
					<-resume
				})
				return nil
			},
		})
		_, err := s.Start(ctx, NewRun{ID: "taken-over", Definition: def})
		require.NoError(t, err)
		await(t, "the handler pauses", paused)
		expireLeases(t, s, "taken-over")
		other := &Worker{Store: s, ID: "other", Handlers: map[string]workflow.Handler{"action": nil}}
		other.defaults()
		c, err := other.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c, 1)
		other.settle(ctx, c[0], time.Now(), workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: c[0].StepID,
			Output: "done",
		}, other.Logger)
		waitStatus(t, s, "taken-over", workflow.StatusCompleted)
		time.Sleep(lease) // A's own clock now says its lease ran out
		close(resume)
		waitFor(t, "the lost lease", func() bool { return len(rec.get()) == 2 })
		stop()
		require.Equal(t, []string{"claimed a", "lease_lost a"}, rec.get())
	})

	// The worker notices its lease ran out at about the moment the database
	// lets the job be claimed again. With a free slot it could reclaim the
	// job before the first claim ends, so its events would interleave. One
	// slot keeps them in order.
	t.Run("heartbeats fail", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:       s,
			Lease:       300 * time.Millisecond,
			Concurrency: 1,
			Observer:    rec.observer(),
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
			heartbeatHook: func() error { return errors.New("network unreachable") },
		})
		_, err := s.Start(context.Background(), NewRun{ID: "partitioned", Definition: def})
		require.NoError(t, err)
		waitFor(t, "the lost lease", func() bool { return rec.count("lease_lost a") >= 1 })
		stop()
		require.Equal(t, []string{"claimed a", "lease_lost a"}, rec.get()[:2])
	})

	// As above, one slot keeps a reclaim after the first claim's end.
	t.Run("settle misses the lease", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:       s,
			Lease:       200 * time.Millisecond,
			Concurrency: 1,
			Observer:    rec.observer(),
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					return "done", nil
				},
			},
			settleHook: func(claimedJob) error { return errors.New("connection reset") },
		})
		_, err := s.Start(context.Background(), NewRun{ID: "unsettled", Definition: def})
		require.NoError(t, err)
		waitFor(t, "the lost lease", func() bool { return rec.count("lease_lost a") >= 1 })
		stop()
		require.Equal(t, []string{"claimed a", "lease_lost a"}, rec.get()[:2])
	})
}

func testObserverGivenBack(t *testing.T, h Harness) {
	t.Run("step", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "fetch", "action": "x"}]}`)
		ctx := context.Background()
		_, err := s.Start(ctx, NewRun{ID: "interrupted", Definition: def})
		require.NoError(t, err)
		var rec eventLog
		wctx, stop := context.WithCancel(ctx)
		w := &Worker{
			Store:    s,
			Poll:     10 * time.Millisecond,
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					stop()
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
		}
		require.NoError(t, w.Run(wctx))
		require.Equal(t, []string{"claimed fetch", "given_back fetch"}, rec.get())
	})

	t.Run("timer", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "nap", "sleep": "10ms"}]}`)
		ctx := context.Background()
		_, err := s.Start(ctx, NewRun{ID: "nap", Definition: def})
		require.NoError(t, err)
		var rec eventLog
		wctx, stop := context.WithCancel(ctx)
		w := &Worker{
			Store:    s,
			Lease:    time.Minute,
			Poll:     10 * time.Millisecond,
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{"action": nil},
			settleHook: func(claimedJob) error {
				stop()
				return errors.New("connection refused")
			},
		}
		require.NoError(t, w.Run(wctx))
		require.Equal(t, []string{"claimed nap(timer)", "given_back nap(timer)"}, rec.get())
	})
}

func testObserverAbandoned(t *testing.T, h Harness) {
	t.Run("step", func(t *testing.T) {
		s := open(t, h)
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
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:     s,
			MaxClaims: 2,
			Observer:  rec.observer(),
			Handlers:  map[string]workflow.Handler{"action": nil},
		})
		waitStatus(t, s, "crashy", workflow.StatusFailed)
		stop()
		require.Equal(t, []string{"claimed crashy", "abandoned crashy"}, rec.get())
	})

	t.Run("timer", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "nap", "sleep": "1ms"}]}`)
		_, err := s.Start(context.Background(), NewRun{ID: "stuck", Definition: def})
		require.NoError(t, err)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:      s,
			Lease:      300 * time.Millisecond, // failing the run commits within it
			MaxClaims:  2,
			Observer:   rec.observer(),
			Handlers:   map[string]workflow.Handler{"action": nil},
			settleHook: func(claimedJob) error { return context.DeadlineExceeded },
		})
		waitStatus(t, s, "stuck", workflow.StatusFailed)
		stop()
		require.Equal(t, []string{
			"abandoned nap(timer)",
			"claimed nap(timer)", "claimed nap(timer)", "claimed nap(timer)",
			"lease_lost nap(timer)", "lease_lost nap(timer)",
		}, rec.sorted())
	})

	// A definition the store cannot load postpones the claim with no ending
	// event, then fails the run after MaxClaims. Failing the run must commit
	// within the claim's lease, so the lease leaves room for a slow database.
	t.Run("definition unavailable", func(t *testing.T) {
		s := open(t, h)
		def := load(t, `{"steps": [{"name": "a", "agent": "y"}]}`)
		_, err := s.Start(context.Background(), NewRun{ID: "cold", Definition: def})
		require.NoError(t, err)
		cold, err := newOn(t, s, workflow.KindMap{"action": nil})
		require.NoError(t, err)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:     cold,
			Lease:     200 * time.Millisecond,
			MaxClaims: 3,
			Observer:  rec.observer(),
			Handlers:  map[string]workflow.Handler{"agent": nil},
		})
		waitStatus(t, s, "cold", workflow.StatusFailed)
		stop()
		require.Equal(t, []string{"claimed a", "claimed a", "claimed a", "abandoned a"}, rec.get())
	})
}

// TestObserverDeferredResultArrivesFirst: a hand-off whose result comes back
// through Store.Deliver before the worker marks the job done is still a
// deferred settle, not a cancel.
func testObserverDeferredResultArrivesFirst(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [{"name": "ask", "agent": "human"}]}`)
	var rec eventLog
	stop := runWorker(t, &Worker{
		Store:    s,
		Observer: rec.observer(),
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				_, err := s.Deliver(ctx, job.RunID, workflow.Event{
					Kind:   workflow.EventStepCompleted,
					StepID: job.StepID,
					Output: "approved",
				})
				if err != nil {
					return nil, err
				}
				return nil, workflow.ErrDeferred
			},
		},
	})
	_, err := s.Start(context.Background(), NewRun{ID: "fast", Definition: def})
	require.NoError(t, err)
	waitStatus(t, s, "fast", workflow.StatusCompleted)
	waitFor(t, "the deferred event", func() bool { return len(rec.get()) == 2 })
	stop()
	require.Equal(t, []string{"claimed ask", "settled:deferred ask"}, rec.get())
}

// TestObserverDeferredJobGone delivers a deferred step's result while its
// handler still runs, so a heartbeat finds the job gone and cancels the
// handler. Under a live lease the handler's return decides: ErrDeferred is
// Settled, and the cancel error is Cancelled. After the lease the claim is
// lost. The lease leaves room for a slow test runner, as in
// TestObserverCancelled.
func testObserverDeferredJobGone(t *testing.T, h Harness) {
	const lease = 600 * time.Millisecond
	handoff := func(s *Env, delivered chan<- struct{}, deferred bool) workflow.Handler {
		return func(ctx context.Context, job workflow.Job) (any, error) {
			_, err := s.Deliver(context.WithoutCancel(ctx), job.RunID, workflow.Event{
				Kind:   workflow.EventStepCompleted,
				StepID: job.StepID,
				Output: "approved",
			})
			if err != nil {
				return nil, err
			}
			close(delivered)
			<-ctx.Done() // the heartbeat finds the job gone
			if !deferred {
				return nil, ctx.Err()
			}
			return nil, workflow.ErrDeferred
		}
	}

	t.Run("lease live", func(t *testing.T) {
		s := open(t, h)
		var rec eventLog
		delivered := make(chan struct{})
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    lease,
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{"agent": handoff(s, delivered, true)},
		})
		def := load(t, `{"steps": [{"name": "ask", "agent": "human"}]}`)
		_, err := s.Start(context.Background(), NewRun{ID: "fast", Definition: def})
		require.NoError(t, err)
		waitFor(t, "the deferred event", func() bool { return len(rec.get()) == 2 })
		stop()
		require.Equal(t, []string{"claimed ask", "settled:deferred ask"}, rec.get())
	})

	t.Run("lease live, cancel error", func(t *testing.T) {
		s := open(t, h)
		var rec eventLog
		delivered := make(chan struct{})
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    lease,
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{"agent": handoff(s, delivered, false)},
		})
		def := load(t, `{"steps": [{"name": "ask", "agent": "human"}]}`)
		_, err := s.Start(context.Background(), NewRun{ID: "fast", Definition: def})
		require.NoError(t, err)
		waitFor(t, "the cancelled event", func() bool { return len(rec.get()) == 2 })
		stop()
		require.Equal(t, []string{"claimed ask", "cancelled ask"}, rec.get())
	})

	t.Run("after the lease", func(t *testing.T) {
		s := open(t, h)
		var (
			rec       eventLog
			pause     sync.Once
			paused    = make(chan struct{})
			resume    = make(chan struct{})
			delivered = make(chan struct{})
		)
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    lease,
			Poll:     time.Minute,
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{"agent": handoff(s, delivered, true)},
			heartbeatHook: func() error {
				pause.Do(func() {
					close(paused)
					<-resume
				})
				return nil
			},
		})
		def := load(t, `{"steps": [{"name": "ask", "agent": "human"}]}`)
		_, err := s.Start(context.Background(), NewRun{ID: "paused", Definition: def})
		require.NoError(t, err)
		await(t, "the handler pauses", paused)
		await(t, "the result is delivered", delivered)
		time.Sleep(lease) // the worker's own clock now says its lease ran out
		close(resume)
		waitFor(t, "the lost lease", func() bool { return len(rec.get()) == 2 })
		stop()
		require.Equal(t, []string{"claimed ask", "lease_lost ask"}, rec.get())
	})
}

// TestObserverJobGoneOnWrite claims a step, removes its job the way
// Store.Deliver does, and then settles, finishes or gives back the claim.
// A write under a live lease finds the job gone: Cancelled, or Settled
// (deferred) for a hand-off. Past the lease, another claim may have settled
// the job: LeaseLost. Settle and finish run under a deadline at the lease's
// end, so past it their write does not start at all.
func testObserverJobGoneOnWrite(t *testing.T, h Harness) {
	const lease = 300 * time.Millisecond
	expired := func() time.Time { return time.Now().Add(-lease) }
	cases := []struct {
		name  string
		end   func(w *Worker, c claimedJob)
		event string
	}{
		{"settle, lease live", func(w *Worker, c claimedJob) {
			w.settle(context.Background(), c, c.At, completed(c), w.Logger)
		}, "cancelled a"},
		{"finish, lease live", func(w *Worker, c claimedJob) {
			ctx, cancel := context.WithDeadline(context.Background(), c.At.Add(lease))
			defer cancel()
			w.finishDeferred(ctx, c, w.Logger) // a hand-off: its result came first
		}, "settled:deferred a"},
		{"give back, lease live", func(w *Worker, c claimedJob) {
			w.release(c)
		}, "cancelled a"},
		{"settle, after the lease", func(w *Worker, c claimedJob) {
			w.settle(context.Background(), c, expired(), completed(c), w.Logger)
		}, "lease_lost a"},
		{"finish, after the lease", func(w *Worker, c claimedJob) {
			ctx, cancel := context.WithDeadline(context.Background(), expired().Add(lease))
			defer cancel()
			w.finishDeferred(ctx, c, w.Logger)
		}, "lease_lost a"},
		{"give back, after the lease", func(w *Worker, c claimedJob) {
			c.At = expired()
			w.release(c)
		}, "lease_lost a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t, h)
			ctx := context.Background()
			def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
			_, err := s.Start(ctx, NewRun{ID: "gone", Definition: def})
			require.NoError(t, err)
			var rec eventLog
			w := &Worker{
				Store:    s,
				Lease:    lease,
				Observer: rec.observer(),
				Handlers: map[string]workflow.Handler{"action": nil},
			}
			w.defaults()
			c, err := w.claim(ctx, 1)
			require.NoError(t, err)
			require.Len(t, c, 1)
			_, err = s.Deliver(ctx, "gone", completed(c[0]))
			require.NoError(t, err)

			tc.end(w, c[0])
			require.Equal(t, []string{tc.event}, rec.get())
		})
	}
}

func completed(c claimedJob) workflow.Event {
	return workflow.Event{Kind: workflow.EventStepCompleted, StepID: c.StepID, Output: "done"}
}

// TestObserverRunOnceAndExecute: a job that RunOnce or Execute claims fires
// Claimed, then exactly one ending event, as under Run. A request that ends
// mid-job fires GivenBack once, or nothing more if the handler ignores it
// and its result is recorded.
func testObserverRunOnceAndExecute(t *testing.T, h Harness) {
	const twoSteps = `{"steps": [
	  {"name": "a", "action": "x", "next": ["b"]},
	  {"name": "b", "action": "x"}]}`
	ok := map[string]workflow.Handler{
		"action": func(context.Context, workflow.Job) (any, error) { return "ok", nil },
	}

	t.Run("RunOnce", func(t *testing.T) {
		s := open(t, h)
		startRuns(t, s, twoSteps, "r")
		var rec eventLog
		w := &Worker{Store: s, Observer: rec.observer(), Handlers: ok}
		require.NoError(t, w.RunOnce(context.Background()))
		require.Equal(t, []string{
			"claimed a", "settled:completed a", "claimed b", "settled:completed b",
		}, rec.get())
	})

	t.Run("Execute", func(t *testing.T) {
		s := open(t, h)
		ref := start(t, s, oneStep, "r")[0]
		var rec eventLog
		w := &Worker{Store: s, Observer: rec.observer(), Handlers: ok}
		_, err := w.Execute(context.Background(), ref)
		require.NoError(t, err)
		_, err = w.Execute(context.Background(), ref) // gone: no claim
		require.NoError(t, err)
		require.Equal(t, []string{"claimed a", "settled:completed a"}, rec.get())
	})

	for _, call := range []string{"RunOnce", "Execute"} {
		run := func(w *Worker, ctx context.Context, ref JobRef) {
			if call == "RunOnce" {
				_ = w.RunOnce(ctx)
				return
			}
			_, _ = w.Execute(ctx, ref)
		}

		// The handler ends the request itself, so the request always ends
		// while the handler runs, however slow the claim was.
		t.Run(call+"RequestEnds", func(t *testing.T) {
			s := open(t, h)
			ref := start(t, s, oneStep, "r")[0]
			var rec eventLog
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := &Worker{Store: s, Observer: rec.observer(), Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, _ workflow.Job) (any, error) {
					cancel()
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}}
			run(w, ctx, ref)
			require.Equal(t, []string{"claimed a", "given_back a"}, rec.get())
		})

		t.Run(call+"RequestEndsHandlerIgnoresIt", func(t *testing.T) {
			s := open(t, h)
			ref := start(t, s, oneStep, "r")[0]
			var rec eventLog
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := &Worker{Store: s, Observer: rec.observer(), Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, _ workflow.Job) (any, error) {
					cancel()
					<-ctx.Done()
					time.Sleep(50 * time.Millisecond)
					return "late", nil
				},
			}}
			run(w, ctx, ref)
			require.Equal(t, []string{"claimed a", "settled:completed a"}, rec.get())
		})
	}
}
