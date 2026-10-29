package postgres

import (
	"context"
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

// TestObserverRunOnceAndExecute: a claim by key that fails fires
// ClaimError, but not for a claim that the request's end cut off. The
// other cases run in the shared suite (internal/hosttest); this one needs
// a schema that was never migrated.
func TestObserverRunOnceAndExecute(t *testing.T) {
	ok := map[string]workflow.Handler{
		"action": func(context.Context, workflow.Job) (any, error) { return "ok", nil },
	}

	t.Run("ExecuteClaimError", func(t *testing.T) {
		s := newStore(t)
		missing, err := New(s.pool, kinds, WithSchema(s.schema+"_missing"))
		require.NoError(t, err)
		var rec eventLog
		w := &Worker{Store: missing, Observer: rec.observer(), Handlers: ok}
		ref := JobRef{RunID: "r", StepID: "main/a#1", StepKind: "action"}
		_, err = w.Execute(context.Background(), ref)
		require.Error(t, err)
		require.Equal(t, []string{"claim_error "}, rec.get())

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = w.Execute(ctx, ref)
		require.Error(t, err)
		require.Equal(t, []string{"claim_error "}, rec.get(), "no ClaimError for a cut-off claim")
	})
}
