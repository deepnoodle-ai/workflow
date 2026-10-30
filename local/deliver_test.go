package local_test

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/deepnoodle-ai/workflow/local"
)

// waitFor fails the test if ch is not closed or sent on in time: the
// run deadlocked.
func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("deadlock: %s", what)
		panic("unreachable")
	}
}

// A handler that delivers after the run stopped gets ErrNotRunning, and
// the run still ends.
func TestDeliverFromHandlerAfterRunStopped(t *testing.T) {
	def := load(t, `{"steps": [
	  {"name": "fork", "action": "x", "next": ["fail", "slow"]},
	  {"name": "fail", "action": "x"},
	  {"name": "slow", "action": "x"}]}`)
	runc := make(chan *local.Run, 1)
	late := make(chan error, 1)
	slowCalled := make(chan struct{})
	r := &local.Runner{
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				switch job.Step {
				case "fail":
					<-slowCalled
					return nil, &workflow.StepError{Type: "boom", Message: "boom"}
				case "slow":
					// The run fails and stops while this handler runs.
					close(slowCalled)
					<-ctx.Done()
					late <- (<-runc).Cancel("too late")
				}
				return nil, nil
			},
		},
	}
	run, err := r.Start(context.Background(), "run", def, nil)
	require.NoError(t, err)
	runc <- run

	require.ErrorIs(t, waitFor(t, late, "Cancel from a handler"), local.ErrNotRunning)
	waitFor(t, run.Done(), "Wait")
	st, err := run.Wait()
	require.NoError(t, err)
	require.Equal(t, workflow.StatusFailed, st.Status)
}

// Deliver returns once the state it produced is saved, and returns the
// error of a save that fails.
func TestDeliverReturnsAfterSave(t *testing.T) {
	def := load(t, `{"steps": [
	  {"name": "ask", "wait": true, "timeout": "1h", "next": ["after"]},
	  {"name": "after", "wait": true, "timeout": "1h"}]}`)
	store := &memStore{}
	r := &local.Runner{Save: store.save}
	run, err := r.Start(context.Background(), "run", def, nil)
	require.NoError(t, err)

	err = run.Deliver(workflow.Event{Kind: workflow.EventStepCompleted, StepID: "ask"})
	require.NoError(t, err)
	require.Equal(t, []workflow.StepID{"main/after#1"}, store.load(t).InFlight(),
		"the delivered result was saved before Deliver returned")

	store.mu.Lock()
	store.failAt = store.saves + 1
	store.mu.Unlock()
	err = run.Deliver(workflow.Event{Kind: workflow.EventStepCompleted, StepID: "after"})
	require.ErrorIs(t, err, errDiskFull)
	_, err = run.Wait()
	require.ErrorIs(t, err, errDiskFull)
}

// A step whose result is delivered while its handler still runs has
// settled: the handler's context is cancelled.
func TestDeliverCancelsTheSettledHandler(t *testing.T) {
	def := load(t, `{"steps": [
	  {"name": "a", "action": "x", "next": ["b"]},
	  {"name": "b", "wait": true, "timeout": "1h"}]}`)
	started := make(chan workflow.StepID, 1)
	cancelled := make(chan struct{})
	r := &local.Runner{
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				started <- job.StepID
				<-ctx.Done()
				close(cancelled)
				return nil, ctx.Err()
			},
		},
	}
	run, err := r.Start(context.Background(), "run", def, nil)
	require.NoError(t, err)
	id := waitFor(t, started, "a's handler")

	err = run.Deliver(workflow.Event{Kind: workflow.EventStepCompleted, StepID: id})
	require.NoError(t, err)
	waitFor(t, cancelled, "a's handler context cancelled while the run waits on b")

	err = run.Deliver(workflow.Event{Kind: workflow.EventStepCompleted, StepID: "b"})
	require.NoError(t, err)
	st, err := run.Wait()
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, st.Status)
}

// Resume rejects what Apply would: a missing or unstarted state, a
// definition not made by Load, and a state from another definition, even
// for a finished run.
func TestResumeRejectsBadInput(t *testing.T) {
	def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
	other := load(t, `{"steps": [{"name": "b", "action": "x"}]}`)
	r := &local.Runner{
		Handlers: map[string]workflow.Handler{
			"action": func(context.Context, workflow.Job) (any, error) { return nil, nil },
		},
	}
	done, err := r.Run(context.Background(), "run", def, nil)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, done.Status)

	ctx := context.Background()
	_, err = r.Resume(ctx, "run", def, nil)
	require.ErrorIs(t, err, workflow.ErrNotStarted)
	_, err = r.Resume(ctx, "run", def, &workflow.State{})
	require.ErrorIs(t, err, workflow.ErrNotStarted)
	_, err = r.Resume(ctx, "run", &workflow.Definition{}, done)
	require.ErrorIs(t, err, workflow.ErrNotValidated)
	_, err = r.Resume(ctx, "run", other, done)
	require.ErrorIs(t, err, workflow.ErrDefinitionMismatch)
	_, err = workflow.Outstanding(other, done)
	require.ErrorIs(t, err, workflow.ErrDefinitionMismatch)
}
