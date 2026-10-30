package local_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/deepnoodle-ai/workflow/local"
)

// pauseRig runs pauseFetchDef. The first fetch attempt fails while the
// run is paused, so its retry is dispatched during the pause.
type pauseRig struct {
	mu      sync.Mutex
	calls   []workflow.StepID
	started chan struct{} // fetch is running
	release chan struct{} // let fetch finish
	held    chan struct{} // charge#2 is dispatched while paused
	once    sync.Once
	store   memStore
}

func newPauseRig() *pauseRig {
	return &pauseRig{
		started: make(chan struct{}),
		release: make(chan struct{}),
		held:    make(chan struct{}),
	}
}

func (p *pauseRig) runner() *local.Runner {
	return &local.Runner{
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				p.mu.Lock()
				p.calls = append(p.calls, job.StepID)
				p.mu.Unlock()
				if job.StepID != "main/fetch#1" {
					return nil, nil
				}
				close(p.started)
				<-p.release
				return nil, &workflow.StepError{Type: "down", Message: "down", Retryable: true}
			},
		},
		Save: func(ctx context.Context, runID string, st *workflow.State) error {
			if st.Status == workflow.StatusPaused &&
				slices.Contains(st.InFlight(), "main/fetch#2") {
				p.once.Do(func() { close(p.held) })
			}
			return p.store.save(ctx, runID, st)
		},
	}
}

func (p *pauseRig) called() []workflow.StepID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

// pauseDuringRetry starts a run whose fetch fails while paused, and waits
// until its retry is dispatched and past its backoff.
func (p *pauseRig) pauseDuringRetry(t *testing.T, def *workflow.Definition) *local.Run {
	t.Helper()
	run, err := p.runner().Start(t.Context(), "r", def, nil)
	require.NoError(t, err)
	<-p.started
	require.NoError(t, run.Pause("look"))
	close(p.release)
	<-p.held
	time.Sleep(50 * time.Millisecond) // well past the 10ms backoff
	require.Equal(t, []workflow.StepID{"main/fetch#1"}, p.called())
	return run
}

func TestPauseHoldsARetryUntilResume(t *testing.T) {
	def, err := workflow.Load([]byte(pauseFetchDef), kinds)
	require.NoError(t, err)
	p := newPauseRig()
	run := p.pauseDuringRetry(t, def)

	require.NoError(t, run.Resume())
	st, err := run.Wait()
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.Equal(t, []workflow.StepID{"main/fetch#1", "main/fetch#2", "main/charge#1",
		"finally/release#1"}, p.called())
	require.ErrorIs(t, run.Pause("late"), local.ErrNotRunning)
	require.ErrorIs(t, run.Resume(), local.ErrNotRunning)
}

func TestCancelEndsTheHold(t *testing.T) {
	def, err := workflow.Load([]byte(pauseFetchDef), kinds)
	require.NoError(t, err)
	p := newPauseRig()
	run := p.pauseDuringRetry(t, def)

	require.NoError(t, run.Cancel("give up"))
	st, err := run.Wait()
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelled, st.Status)
	require.Equal(t, "give up", st.Reason)
	// The held retry never called its handler; the finally step did.
	require.Equal(t, []workflow.StepID{"main/fetch#1", "finally/release#1"}, p.called())
}

func TestResumeFromASavedPausedRunStaysPaused(t *testing.T) {
	def, err := workflow.Load([]byte(pauseFetchDef), kinds)
	require.NoError(t, err)
	p := newPauseRig()
	ctx, stop := context.WithCancel(t.Context())
	run, err := p.runner().Start(ctx, "r", def, nil)
	require.NoError(t, err)
	<-p.started
	require.NoError(t, run.Pause("look"))
	close(p.release)
	<-p.held
	stop() // the process stops while the run is paused
	_, err = run.Wait()
	require.ErrorIs(t, err, context.Canceled)

	saved := p.store.load(t)
	require.Equal(t, workflow.StatusPaused, saved.Status)
	again := &pauseRig{started: p.started, release: p.release, held: make(chan struct{})}
	run, err = again.runner().Resume(t.Context(), "r", def, saved)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 0, len(again.called()))

	require.NoError(t, run.Resume())
	st, err := run.Wait()
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.Equal(t, []workflow.StepID{"main/fetch#2", "main/charge#1", "finally/release#1"},
		again.called())
}

// pauseFetchDef retries fetch after a short backoff.
const pauseFetchDef = `{
  "steps": [
    {"name": "fetch", "action": "x", "next": "charge",
     "retry": {"max_attempts": 2, "backoff": "10ms"}},
    {"name": "charge", "action": "x"}
  ],
  "finally": [{"name": "release", "action": "x"}]
}`
