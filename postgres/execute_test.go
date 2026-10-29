package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// start starts one run and returns its job references.
func start(t *testing.T, s *Store, doc, id string) []JobRef {
	t.Helper()
	res, err := s.Start(context.Background(), NewRun{ID: id, Definition: load(t, doc)})
	require.NoError(t, err)
	return res.Jobs()
}

// jobState reads a job's claims and fence by its reference.
func jobState(t *testing.T, s *Store, ref JobRef) (claims int, fence int64) {
	t.Helper()
	require.NoError(t, s.pool.QueryRow(context.Background(), s.sql(`
		SELECT claims, fence FROM {{schema}}.jobs
		WHERE run_id = $1 AND step_id = $2 AND kind = $3`),
		ref.RunID, string(ref.StepID), ref.kind()).Scan(&claims, &fence))
	return claims, fence
}

// TestExecuteRunsAJobAndReturnsTheNext: Execute runs one job and returns
// the jobs its result wrote; pushing those finishes the run.
func TestExecuteRunsAJobAndReturnsTheNext(t *testing.T) {
	s := newStore(t)
	refs := start(t, s, `{"steps": [
	  {"name": "a", "action": "x", "next": ["b"]},
	  {"name": "b", "action": "x"}]}`, "r")
	require.Equal(t, []JobRef{{RunID: "r", StepID: "main/a#1", StepKind: "action"}}, refs)
	handlers, calls := counting()
	w := &Worker{Store: s, Handlers: handlers}

	ex, err := w.Execute(context.Background(), refs[0])
	require.NoError(t, err)
	require.True(t, ex.Settled)
	require.Equal(t, []JobRef{{RunID: "r", StepID: "main/b#1", StepKind: "action"}}, ex.Jobs)

	ex, err = w.Execute(context.Background(), ex.Jobs[0])
	require.NoError(t, err)
	require.True(t, ex.Settled)
	require.Len(t, ex.Jobs, 0)
	require.Equal(t, 2, calls("r"))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "r"))
	require.Equal(t, 0, w.Active())
}

// TestExecuteTwiceRunsOnce: a task delivered twice at once runs its
// handler once. The other call finds the job leased or gone.
func TestExecuteTwiceRunsOnce(t *testing.T) {
	s := newStore(t)
	for i := range 10 {
		id := "r" + string(rune('0'+i))
		ref := start(t, s, oneStep, id)[0]
		var calls atomic.Int32
		w := &Worker{
			Store: s,
			Handlers: map[string]workflow.Handler{
				"action": func(context.Context, workflow.Job) (any, error) {
					calls.Add(1)
					time.Sleep(20 * time.Millisecond)
					return "ok", nil
				},
			},
		}
		var (
			wg      sync.WaitGroup
			settled atomic.Int32
		)
		for range 2 {
			wg.Go(func() {
				ex, err := w.Execute(context.Background(), ref)
				if err != nil && !errors.Is(err, ErrNotClaimable) {
					t.Errorf("Execute: %v", err)
				}
				if ex.Settled {
					settled.Add(1)
				}
			})
		}
		wg.Wait()
		require.Equal(t, int32(1), calls.Load(), id)
		require.Equal(t, int32(1), settled.Load(), id)
		require.Equal(t, workflow.StatusCompleted, runStatus(t, s, id))
	}
}

// TestExecuteAJobThatIsGone: a job already settled is nothing to do.
func TestExecuteAJobThatIsGone(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	handlers, calls := counting()
	w := &Worker{Store: s, Handlers: handlers}
	_, err := w.Execute(context.Background(), ref)
	require.NoError(t, err)

	ex, err := w.Execute(context.Background(), ref)
	require.NoError(t, err)
	require.False(t, ex.Settled)
	require.Equal(t, 1, calls("r"))

	ex, err = w.Execute(context.Background(), JobRef{RunID: "nope", StepID: "main/a#1"})
	require.NoError(t, err)
	require.False(t, ex.Settled)
}

// TestExecuteALiveLeaseElsewhere: Execute does not claim a job another
// worker holds, and leaves its claims and fence as they were.
func TestExecuteALiveLeaseElsewhere(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	handlers, calls := counting()
	other := &Worker{Store: s, Handlers: handlers}
	other.defaults()
	held, err := other.claim(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, held, 1)

	w := &Worker{Store: s, Handlers: handlers}
	_, err = w.Execute(context.Background(), ref)
	require.ErrorIs(t, err, ErrNotClaimable)
	require.True(t, strings.Contains(err.Error(), "another worker holds its lease"), err.Error())
	claims, fence := jobState(t, s, ref)
	require.Equal(t, 1, claims)
	require.Equal(t, int64(1), fence)
	require.Equal(t, 0, calls("r"))
}

// TestExecuteAnExpiredLease: Execute claims a job whose lease ran out, and
// the old holder's late result is fenced out.
func TestExecuteAnExpiredLease(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	output := func(out string) map[string]workflow.Handler {
		return map[string]workflow.Handler{
			"action": func(context.Context, workflow.Job) (any, error) { return out, nil },
		}
	}
	stale := &Worker{Store: s, Handlers: output("stale")}
	stale.defaults()
	held, err := stale.claim(context.Background(), 1)
	require.NoError(t, err)
	expireLeases(t, s, "r")

	w := &Worker{Store: s, Handlers: output("fresh")}
	ex, err := w.Execute(context.Background(), ref)
	require.NoError(t, err)
	require.True(t, ex.Settled)

	o := stale.process(context.Background(), held[0])
	require.False(t, o.settled)
	steps, err := s.Steps(context.Background(), "r")
	require.NoError(t, err)
	require.Equal(t, "fresh", steps[0].Output)
}

// TestExecuteAJobNotDueOrNotHandled: Execute claims neither a timer that
// is not due nor a step it has no handler for.
func TestExecuteAJobNotDueOrNotHandled(t *testing.T) {
	s := newStore(t)
	timer := start(t, s, napFirst, "nap")[0]
	require.True(t, timer.Timer)
	require.True(t, timer.RunAt.After(time.Now().Add(59*time.Minute)), timer.RunAt)
	step := start(t, s, oneStep, "step")[0]
	handlers, _ := counting()
	agents := &Worker{Store: s, Handlers: map[string]workflow.Handler{"agent": handlers["action"]}}

	_, err := agents.Execute(context.Background(), timer)
	require.ErrorIs(t, err, ErrNotClaimable)
	require.True(t, strings.Contains(err.Error(), "due at"), err.Error())
	_, err = agents.Execute(context.Background(), step)
	require.ErrorIs(t, err, ErrNotClaimable)
	require.True(t, strings.Contains(err.Error(), `no handler for step kind "action"`),
		err.Error())
	for _, ref := range []JobRef{timer, step} {
		claims, _ := jobState(t, s, ref)
		require.Equal(t, 0, claims)
	}
}

// TestExecuteContextEndCountsTheClaim: a request that ends while the
// handler runs gives the claim back but counts it, so a step that always
// outlives the request fails as abandoned after MaxClaims tasks.
func TestExecuteContextEndCountsTheClaim(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	w := &Worker{
		Store:     s,
		MaxClaims: 2,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, _ workflow.Job) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	execute := func() (Execution, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		return w.Execute(ctx, ref)
	}

	_, err := execute()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	claims, leased := soleJob(t, s, "r")
	require.Equal(t, 1, claims)
	require.False(t, leased)

	_, err = execute()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	ex, err := execute() // claimed a third time: past MaxClaims
	require.NoError(t, err)
	require.True(t, ex.Settled)
	r, err := s.GetRun(context.Background(), "r")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusFailed, r.Status)
	require.Equal(t, "abandoned", r.State.Error.Type)
}

// TestExecuteHandOff: a handler that hands the step off settles the job;
// the step stays in flight.
func TestExecuteHandOff(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"action": func(context.Context, workflow.Job) (any, error) {
			return nil, workflow.ErrDeferred
		},
	}}

	ex, err := w.Execute(context.Background(), ref)
	require.NoError(t, err)
	require.True(t, ex.Settled)
	require.Len(t, ex.Jobs, 0)
	require.Equal(t, workflow.StatusRunning, runStatus(t, s, "r"))
}

// TestShutdownWaitsForExecute: on a worker that only calls Execute, Active
// counts the job and Shutdown waits for it, then returns nil when it
// finishes before the deadline.
func TestShutdownWaitsForExecute(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	finish := make(chan struct{})
	var started atomic.Int32
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"action": func(context.Context, workflow.Job) (any, error) {
			started.Add(1)
			<-finish
			return "ok", nil
		},
	}}
	executed := make(chan Execution, 1)
	go func() {
		ex, err := w.Execute(context.Background(), ref)
		require.NoError(t, err)
		executed <- ex
	}()
	// Not Active: it counts the claim query too, before the job is held.
	waitUntil(t, "the handler runs", func() bool { return started.Load() == 1 })

	shutdown := make(chan error, 1)
	go func() { shutdown <- w.Shutdown(context.Background()) }()
	select {
	case <-shutdown:
		t.Fatal("Shutdown returned while Execute ran")
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	require.NoError(t, <-shutdown)
	require.True(t, (<-executed).Settled)
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "r"))
}

// TestShutdownCutsOffExecute: a handler still running at the Shutdown
// deadline is given back without counting, and Execute returns an error.
func TestShutdownCutsOffExecute(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	var started atomic.Int32
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, _ workflow.Job) (any, error) {
			started.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}}
	result := make(chan error, 1)
	go func() {
		_, err := w.Execute(context.Background(), ref)
		result <- err
	}()
	waitUntil(t, "the handler runs", func() bool { return started.Load() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, w.Shutdown(ctx), context.DeadlineExceeded)
	select {
	case err := <-result:
		require.ErrorIs(t, err, errShuttingDown)
	default:
		t.Fatal("Shutdown returned before Execute")
	}
	claims, leased := soleJob(t, s, "r")
	require.Equal(t, 0, claims)
	require.False(t, leased)
}

// TestExecuteWithClaimingOff: after Shutdown, or while MayClaim returns
// false, Execute claims nothing and returns ErrNotClaimable.
func TestExecuteWithClaimingOff(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	handlers, calls := counting()

	paused := &Worker{Store: s, Handlers: handlers, MayClaim: func() bool { return false }}
	_, err := paused.Execute(context.Background(), ref)
	require.ErrorIs(t, err, ErrNotClaimable)

	stopped := &Worker{Store: s, Handlers: handlers}
	require.NoError(t, stopped.Shutdown(context.Background()))
	_, err = stopped.Execute(context.Background(), ref)
	require.ErrorIs(t, err, ErrNotClaimable)

	require.Equal(t, 0, calls("r"))
	claims, _ := soleJob(t, s, "r")
	require.Equal(t, 0, claims)
}

// TestExecuteConcurrently: many Execute calls on one Worker, as an HTTP
// server makes them, each run their own job. Run with -race.
func TestExecuteConcurrently(t *testing.T) {
	s := newStore(t)
	var refs []JobRef
	for i := range 16 {
		refs = append(refs, start(t, s, chain, "r"+string(rune('a'+i)))...)
	}
	handlers, _ := counting()
	w := &Worker{Store: s, Handlers: handlers}

	var wg sync.WaitGroup
	var execute func(ref JobRef)
	execute = func(ref JobRef) {
		ex, err := w.Execute(context.Background(), ref)
		if err != nil {
			t.Errorf("Execute %s: %v", ref.StepID, err)
			return
		}
		for _, next := range ex.Jobs {
			wg.Go(func() { execute(next) })
		}
	}
	for _, ref := range refs {
		wg.Go(func() { execute(ref) })
	}
	wg.Wait()
	for _, ref := range refs {
		require.Equal(t, workflow.StatusCompleted, runStatus(t, s, ref.RunID))
	}
}

// TestResultJobs: Start, Deliver and Cancel report the jobs they wrote,
// timers included; a duplicate Start reports none. A JobRef survives JSON.
func TestResultJobs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	def := load(t, `{"steps": [
	  {"name": "ask", "agent": "human", "timeout": "1h", "next": ["ship"]},
	  {"name": "ship", "action": "x"}],
	 "finally": [{"name": "cleanup", "action": "x"}]}`)

	res, err := s.Start(ctx, NewRun{ID: "r", Definition: def})
	require.NoError(t, err)
	refs := res.Jobs()
	require.Len(t, refs, 2)
	require.Equal(t, JobRef{RunID: "r", StepID: "main/ask#1", StepKind: "agent"},
		JobRef{RunID: refs[0].RunID, StepID: refs[0].StepID, StepKind: refs[0].StepKind})
	require.False(t, refs[0].Timer)
	require.True(t, refs[1].Timer, "the timeout timer")
	require.Equal(t, refs[0].StepID, refs[1].StepID)
	require.True(t, refs[1].RunAt.After(time.Now().Add(59*time.Minute)), refs[1].RunAt)

	data, err := json.Marshal(refs[1])
	require.NoError(t, err)
	var back JobRef
	require.NoError(t, json.Unmarshal(data, &back))
	require.True(t, back.RunAt.Equal(refs[1].RunAt))
	back.RunAt = refs[1].RunAt
	require.Equal(t, refs[1], back)

	again, err := s.Start(ctx, NewRun{ID: "r", Definition: def})
	require.NoError(t, err)
	require.Len(t, again.Jobs(), 0)

	res, err = s.Deliver(ctx, "r", workflow.Event{
		Kind: workflow.EventStepCompleted, StepID: "main/ask#1", Output: "yes",
	})
	require.NoError(t, err)
	require.Len(t, res.Jobs(), 1)
	require.Equal(t, workflow.StepID("main/ship#1"), res.Jobs()[0].StepID)

	res, err = s.Cancel(ctx, "r", "stop")
	require.NoError(t, err)
	require.Len(t, res.Jobs(), 1)
	require.Equal(t, workflow.StepID("finally/cleanup#1"), res.Jobs()[0].StepID)
}

// TestExecuteReportsADefinitionLoadError: Execute returns a database error
// that kept it from loading the job's definition, so the task is retried;
// the job stays under its lease and its handler does not run.
func TestExecuteReportsADefinitionLoadError(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	fresh := breakDefinitions(t, s)
	handlers, calls := counting()
	w := &Worker{Store: fresh, Handlers: handlers, Logger: slog.New(slog.DiscardHandler)}

	ex, err := w.Execute(context.Background(), ref)
	require.Error(t, err)
	require.False(t, ex.Settled)
	require.False(t, errors.Is(err, ErrNotClaimable), err.Error())
	require.False(t, errors.Is(err, ErrDefinitionUnavailable), err.Error())
	require.True(t, strings.Contains(err.Error(), "load definition"), err.Error())
	require.Equal(t, 0, calls("r"))
	claims, leased := soleJob(t, s, "r")
	require.Equal(t, 1, claims)
	require.True(t, leased, "left under its lease")
}

// TestExecuteAnEmptyJobRef: a JobRef without a run or a StepID, such as a
// task body that does not match its JSON, is an error, not nothing to do.
func TestExecuteAnEmptyJobRef(t *testing.T) {
	s := newStore(t)
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{"action": nil}}
	for _, ref := range []JobRef{{}, {RunID: "r"}, {StepID: "main/a#1"}} {
		_, err := w.Execute(context.Background(), ref)
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "needs a JobRef"), err.Error())
	}
	var ref JobRef
	require.NoError(t, json.Unmarshal([]byte(`{"runId": "r", "stepId": "main/a#1"}`), &ref))
	_, err := w.Execute(context.Background(), ref)
	require.Error(t, err, "a body with other field names")
}

// TestExecuteADefinitionThatCannotLoad: as in Run, a job whose definition
// the store cannot load is postponed below MaxClaims, and its run fails
// with definition_unavailable at MaxClaims. Execute reports each step.
func TestExecuteADefinitionThatCannotLoad(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	agentsOnly, err := New(s.pool, workflow.KindMap{"agent": nil}, WithSchema(s.schema))
	require.NoError(t, err)
	handlers, calls := counting()
	w := &Worker{
		Store:     agentsOnly, // its kinds lack "action", so the definition does not load
		Handlers:  handlers,
		MaxClaims: 2,
		Logger:    slog.New(slog.DiscardHandler),
	}

	_, err = w.Execute(context.Background(), ref)
	require.ErrorIs(t, err, ErrDefinitionUnavailable)
	claims, leased := soleJob(t, s, "r")
	require.Equal(t, 1, claims)
	require.False(t, leased, "postponed, not held")
	_, err = w.Execute(context.Background(), ref)
	require.ErrorIs(t, err, ErrNotClaimable, "postponed: not due yet")

	_, err = s.pool.Exec(context.Background(),
		s.sql(`UPDATE {{schema}}.jobs SET run_at = now() WHERE run_id = 'r'`)) // time passes
	require.NoError(t, err)
	ex, err := w.Execute(context.Background(), ref)
	require.NoError(t, err)
	require.True(t, ex.Settled)
	r, err := s.GetRun(context.Background(), "r")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusFailed, r.Status)
	require.Equal(t, ErrorTypeDefinitionUnavailable, r.State.Error.Type)
	require.Equal(t, 0, calls("r"))
}

// TestARequestThatEndsKeepsTheLease: a handler that ignores its context
// runs on after its request ends. Its heartbeat keeps the lease until it
// returns, so for more than a Lease after the request ends no other claim
// starts the step beside it. Then its result is recorded.
func TestARequestThatEndsKeepsTheLease(t *testing.T) {
	const lease = 300 * time.Millisecond
	for _, call := range []string{"RunOnce", "Execute"} {
		t.Run(call, func(t *testing.T) {
			s := newStore(t)
			ref := start(t, s, oneStep, "r")[0]
			var starts atomic.Int32
			release := make(chan struct{})
			handlers := map[string]workflow.Handler{
				"action": func(context.Context, workflow.Job) (any, error) {
					if starts.Add(1) == 1 {
						<-release // ignores its context
					}
					return "ok", nil
				},
			}
			w := &Worker{Store: s, Lease: lease, Handlers: handlers}
			ctx, cancel := context.WithCancel(context.Background())
			returned := make(chan error, 1)
			go func() {
				if call == "RunOnce" {
					returned <- w.RunOnce(ctx)
					return
				}
				_, err := w.Execute(ctx, ref)
				returned <- err
			}()
			waitUntil(t, "the handler runs", func() bool { return starts.Load() == 1 })
			cancel() // the request ends

			other := &Worker{Store: s, Lease: lease, Handlers: handlers}
			for end := time.Now().Add(3 * lease); time.Now().Before(end); {
				_, err := other.Execute(context.Background(), ref)
				require.ErrorIs(t, err, ErrNotClaimable)
				time.Sleep(20 * time.Millisecond)
			}
			require.Equal(t, int32(1), starts.Load())

			close(release)
			err := <-returned
			if call == "RunOnce" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "r"))
			require.Equal(t, int32(1), starts.Load())
		})
	}
}

// TestShutdownStopsARequestsLease: the heartbeat that outlives a request
// still stops at a Shutdown deadline. Once the lease runs out another
// worker claims the step, though the first handler has not returned (and
// Shutdown still waits for it).
func TestShutdownStopsARequestsLease(t *testing.T) {
	const lease = 300 * time.Millisecond
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	var starts atomic.Int32
	release := make(chan struct{})
	handlers := map[string]workflow.Handler{
		"action": func(context.Context, workflow.Job) (any, error) {
			if starts.Add(1) == 1 {
				<-release // ignores its context
			}
			return "ok", nil
		},
	}
	w := &Worker{Store: s, Lease: lease, Handlers: handlers}
	returned := make(chan error, 1)
	go func() {
		_, err := w.Execute(context.Background(), ref)
		returned <- err
	}()
	waitUntil(t, "the handler runs", func() bool { return starts.Load() == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	shutdown := make(chan error, 1)
	go func() { shutdown <- w.Shutdown(ctx) }()

	other := &Worker{Store: s, Lease: lease, Handlers: handlers}
	waitUntil(t, "another worker runs the step", func() bool {
		ex, err := other.Execute(context.Background(), ref)
		return err == nil && ex.Settled
	})
	require.Equal(t, int32(2), starts.Load())
	close(release)
	// Its lease ran out before it returned: its result is not recorded.
	require.ErrorIs(t, <-returned, context.DeadlineExceeded)
	require.ErrorIs(t, <-shutdown, context.DeadlineExceeded)
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "r"))
}
