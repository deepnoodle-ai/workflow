package postgres

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

func TestObserverSettled(t *testing.T) {
	s := newStore(t)
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

func TestObserverSettledDeferred(t *testing.T) {
	s := newStore(t)
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

func TestObserverSettledInvalidOutput(t *testing.T) {
	s := newStore(t)
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
// that times out, fire Cancelled and not LeaseLost.
func TestObserverCancelled(t *testing.T) {
	t.Run("run cancelled", func(t *testing.T) {
		s := newStore(t)
		def := load(t, `{"steps": [{"name": "slow", "action": "x"}]}`)
		var rec eventLog
		started := make(chan struct{})
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    150 * time.Millisecond,
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
		<-started
		_, err = s.Cancel(ctx, "cancel-me", "superseded")
		require.NoError(t, err)
		waitFor(t, "the cancelled event", func() bool { return len(rec.get()) == 2 })
		stop()
		require.Equal(t, []string{"claimed slow", "cancelled slow"}, rec.get())
	})

	t.Run("step timed out", func(t *testing.T) {
		s := newStore(t)
		def := load(t, `{"steps": [{"name": "slow", "action": "x", "timeout": "100ms"}]}`)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    150 * time.Millisecond,
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
		s := newStore(t)
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

func TestObserverLeaseLost(t *testing.T) {
	t.Run("reclaimed", func(t *testing.T) {
		s := newStore(t)
		def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
		ctx := context.Background()
		var rec eventLog
		started := make(chan struct{})
		stop := runWorker(t, &Worker{
			Store: s,
			Lease: 300 * time.Millisecond,
			// Only a notification wakes it, so it does not reclaim the job
			// itself once the lease is expired.
			Poll:     time.Minute,
			Observer: rec.observer(),
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
		<-started
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
		s := newStore(t)
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
			Store:    s,
			Lease:    lease,
			Poll:     time.Minute,
			Observer: rec.observer(),
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
		<-paused
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

	t.Run("heartbeats fail", func(t *testing.T) {
		s := newStore(t)
		def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    300 * time.Millisecond,
			Observer: rec.observer(),
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

	t.Run("settle misses the lease", func(t *testing.T) {
		s := newStore(t)
		def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    200 * time.Millisecond,
			Observer: rec.observer(),
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

func TestObserverGivenBack(t *testing.T) {
	t.Run("step", func(t *testing.T) {
		s := newStore(t)
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
		s := newStore(t)
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

func TestObserverAbandoned(t *testing.T) {
	t.Run("step", func(t *testing.T) {
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
		s := newStore(t)
		def := load(t, `{"steps": [{"name": "nap", "sleep": "1ms"}]}`)
		_, err := s.Start(context.Background(), NewRun{ID: "stuck", Definition: def})
		require.NoError(t, err)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:      s,
			Lease:      100 * time.Millisecond,
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
	// event, then fails the run after MaxClaims.
	t.Run("definition unavailable", func(t *testing.T) {
		s := newStore(t)
		def := load(t, `{"steps": [{"name": "a", "agent": "y"}]}`)
		_, err := s.Start(context.Background(), NewRun{ID: "cold", Definition: def})
		require.NoError(t, err)
		cold, err := New(s.pool, workflow.KindMap{"action": nil}, WithSchema(s.schema))
		require.NoError(t, err)
		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:     cold,
			Lease:     50 * time.Millisecond,
			MaxClaims: 3,
			Observer:  rec.observer(),
			Handlers:  map[string]workflow.Handler{"agent": nil},
		})
		waitStatus(t, s, "cold", workflow.StatusFailed)
		stop()
		require.Equal(t, []string{"claimed a", "claimed a", "claimed a", "abandoned a"}, rec.get())
	})
}

func TestObserverClaimError(t *testing.T) {
	s := newStore(t)
	missing, err := New(s.pool, kinds, WithSchema(s.schema+"_missing"))
	require.NoError(t, err)
	var rec eventLog
	stop := runWorker(t, &Worker{
		Store:    missing,
		Poll:     10 * time.Millisecond,
		Observer: rec.observer(),
		Handlers: map[string]workflow.Handler{"action": nil},
	})
	waitFor(t, "a claim error", func() bool { return len(rec.get()) >= 2 })
	stop()
	for _, e := range rec.get() {
		require.Equal(t, "claim_error ", e)
	}
}

// TestObserverDeferredResultArrivesFirst: a hand-off whose result comes back
// through Store.Deliver before the worker marks the job done is still a
// deferred settle, not a cancel.
func TestObserverDeferredResultArrivesFirst(t *testing.T) {
	s := newStore(t)
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
// handler still runs, so a heartbeat finds the job gone. Under a live lease
// the hand-off counts as Settled; after the lease it is a lost claim.
func TestObserverDeferredJobGone(t *testing.T) {
	const lease = 300 * time.Millisecond
	handoff := func(s *Store, delivered chan<- struct{}) workflow.Handler {
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
			return nil, workflow.ErrDeferred
		}
	}

	t.Run("lease live", func(t *testing.T) {
		s := newStore(t)
		var rec eventLog
		delivered := make(chan struct{})
		stop := runWorker(t, &Worker{
			Store:    s,
			Lease:    lease,
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{"agent": handoff(s, delivered)},
		})
		def := load(t, `{"steps": [{"name": "ask", "agent": "human"}]}`)
		_, err := s.Start(context.Background(), NewRun{ID: "fast", Definition: def})
		require.NoError(t, err)
		waitFor(t, "the deferred event", func() bool { return len(rec.get()) == 2 })
		stop()
		require.Equal(t, []string{"claimed ask", "settled:deferred ask"}, rec.get())
	})

	t.Run("after the lease", func(t *testing.T) {
		s := newStore(t)
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
			Handlers: map[string]workflow.Handler{"agent": handoff(s, delivered)},
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
		<-paused
		<-delivered
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
func TestObserverJobGoneOnWrite(t *testing.T) {
	const lease = 300 * time.Millisecond
	expired := func() time.Time { return time.Now().Add(-lease) }
	cases := []struct {
		name  string
		end   func(w *Worker, c claimedJob)
		event string
	}{
		{"settle, lease live", func(w *Worker, c claimedJob) {
			w.settle(context.Background(), c, c.claimed, completed(c), w.Logger)
		}, "cancelled a"},
		{"finish, lease live", func(w *Worker, c claimedJob) {
			ctx, cancel := context.WithDeadline(context.Background(), c.claimed.Add(lease))
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
			c.claimed = expired()
			w.release(c)
		}, "lease_lost a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
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

// TestObserverNoClaimErrorOnShutdown stops a worker whose claim waits on a
// table lock. The claim fails because of the shutdown, which is not a claim
// error.
func TestObserverNoClaimErrorOnShutdown(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, s.sql(`LOCK TABLE {{schema}}.jobs IN ACCESS EXCLUSIVE MODE`))
	require.NoError(t, err)

	var rec eventLog
	stop := runWorker(t, &Worker{
		Store:    s,
		Observer: rec.observer(),
		Handlers: map[string]workflow.Handler{"action": nil},
	})
	waitFor(t, "a claim waiting for the lock", func() bool {
		var n int
		require.NoError(t, s.pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_locks WHERE NOT granted AND relation = $1::regclass`,
			s.schema+".jobs").Scan(&n))
		return n > 0
	})
	stop()
	require.Len(t, rec.get(), 0)
}

// TestObserverDatabaseErrorsLoseTheLease causes the database errors that
// make a worker leave a job to its lease.
func TestObserverDatabaseErrorsLoseTheLease(t *testing.T) {
	t.Run("load definition", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		_, err := s.Start(ctx, NewRun{ID: "a", Definition: load(t, `{"steps": [{"name": "a", "action": "x"}]}`)})
		require.NoError(t, err)
		// A store with an empty cache, whose definitions table is gone.
		fresh, err := New(s.pool, kinds, WithSchema(s.schema))
		require.NoError(t, err)
		_, err = s.pool.Exec(ctx, s.sql(`ALTER TABLE {{schema}}.definitions RENAME TO definitions_away`))
		require.NoError(t, err)

		var rec eventLog
		stop := runWorker(t, &Worker{
			Store:    fresh,
			Lease:    200 * time.Millisecond,
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{"action": nil},
		})
		waitFor(t, "the lost lease", func() bool { return rec.count("lease_lost a") >= 1 })
		stop()
		require.Equal(t, []string{"claimed a", "lease_lost a"}, rec.get()[:2])
	})

	t.Run("give back", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		_, err := s.Start(ctx, NewRun{ID: "a", Definition: load(t, `{"steps": [{"name": "a", "action": "x"}]}`)})
		require.NoError(t, err)
		var rec eventLog
		wctx, stop := context.WithCancel(ctx)
		w := &Worker{
			Store:    s,
			Poll:     time.Minute, // no claim between the rename and the stop
			Observer: rec.observer(),
			Handlers: map[string]workflow.Handler{
				"action": func(hctx context.Context, job workflow.Job) (any, error) {
					_, err := s.pool.Exec(ctx, s.sql(`ALTER TABLE {{schema}}.jobs RENAME TO jobs_away`))
					if err != nil {
						return nil, err
					}
					stop()
					<-hctx.Done()
					return nil, hctx.Err()
				},
			},
		}
		require.NoError(t, w.Run(wctx))
		require.Equal(t, []string{"claimed a", "lease_lost a"}, rec.get())
	})
}
