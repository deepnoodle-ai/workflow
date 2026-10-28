package local_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/deepnoodle-ai/workflow/local"
)

var kinds = workflow.KindMap{"action": nil, "agent": nil}

func load(t *testing.T, doc string) *workflow.Definition {
	t.Helper()
	def, err := workflow.Load([]byte(doc), kinds)
	require.NoError(t, err)
	return def
}

func TestRunFanOutJoinEach(t *testing.T) {
	def := load(t, `{
	  "steps": [
	    {"name": "fork", "action": "split", "next": ["left", "right"]},
	    {"name": "left", "action": "work", "with": {"side": "left"}, "next": ["j"]},
	    {"name": "right", "action": "work", "with": {"side": "right"}, "next": ["j"]},
	    {"name": "j", "join": ["left", "right"], "next": ["each"]},
	    {"name": "each", "action": "work", "each": "concat([left], [right])", "with": {"side": "${{ item }}"}, "concurrency": 1}
	  ]
	}`)
	var running, peak atomic.Int32
	r := &local.Runner{Handlers: map[string]local.Handler{
		"action": func(ctx context.Context, c workflow.Command) (any, error) {
			n := running.Add(1)
			defer running.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			if c.Step == "fork" {
				return nil, nil
			}
			return "did " + c.Params["side"].(string), nil
		},
	}}
	st, err := r.Run(context.Background(), def, nil)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.Equal(t, map[string]any{"each": []any{"did did left", "did did right"}}, st.Outputs)
	require.Equal(t, int32(2), peak.Load(), "left and right ran in parallel")
}

func TestRetryAndErrors(t *testing.T) {
	def := load(t, `{"steps": [{"name": "flaky", "action": "x", "retry": {"max_attempts": 3, "backoff": "1ms"}}]}`)
	var calls atomic.Int32
	r := &local.Runner{Handlers: map[string]local.Handler{
		"action": func(ctx context.Context, c workflow.Command) (any, error) {
			if calls.Add(1) < 3 {
				return nil, errors.New("transient")
			}
			return "ok", nil
		},
	}}
	st, err := r.Run(context.Background(), def, nil)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.Equal(t, int32(3), calls.Load())

	// A non-retryable StepError is not retried; a panic fails the step.
	calls.Store(0)
	r.Handlers["action"] = func(ctx context.Context, c workflow.Command) (any, error) {
		calls.Add(1)
		return nil, &workflow.StepError{Type: "invalid", Message: "no"}
	}
	st, err = r.Run(context.Background(), def, nil)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusFailed, st.Status)
	require.Equal(t, int32(1), calls.Load())

	r.Handlers["action"] = func(ctx context.Context, c workflow.Command) (any, error) { panic("kaboom") }
	st, err = r.Run(context.Background(), def, nil)
	require.NoError(t, err)
	require.Equal(t, "panic", st.Error.Type)
}

func TestTimeoutCancelsHandler(t *testing.T) {
	def := load(t, `{"steps": [{"name": "slow", "action": "x", "timeout": "20ms"}]}`)
	cancelled := make(chan struct{})
	r := &local.Runner{Handlers: map[string]local.Handler{
		"action": func(ctx context.Context, c workflow.Command) (any, error) {
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		},
	}}
	st, err := r.Run(context.Background(), def, nil)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusFailed, st.Status)
	require.Equal(t, workflow.ErrorTypeTimeout, st.Error.Type)
	<-cancelled
}

func TestSleep(t *testing.T) {
	def := load(t, `{"steps": [{"name": "nap", "sleep": "10ms", "next": ["after"]}, {"name": "after", "set": "awake"}]}`)
	start := time.Now()
	st, err := (&local.Runner{}).Run(context.Background(), def, nil)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.True(t, time.Since(start) >= 10*time.Millisecond)
}

// TestResumeFromSavedState stops a process mid-run, then resumes the saved
// state in a new runner. Settled steps do not run again.
func TestResumeFromSavedState(t *testing.T) {
	// b is an agent step so its handler can simulate the crash.
	def := load(t, `{"steps": [
	  {"name": "a", "action": "x", "next": ["b"]},
	  {"name": "b", "agent": "x", "next": ["c"]},
	  {"name": "c", "action": "x"}]}`)

	var mu sync.Mutex
	calls := map[string]int{}
	count := func(c workflow.Command) {
		mu.Lock()
		calls[c.Step]++
		mu.Unlock()
	}
	var saved []byte
	ctx, crash := context.WithCancel(context.Background())
	first := &local.Runner{
		Handlers: map[string]local.Handler{
			"action": func(ctx context.Context, c workflow.Command) (any, error) { count(c); return c.Step, nil },
			"agent": func(ctx context.Context, c workflow.Command) (any, error) {
				count(c)
				crash() // the process dies while b is running
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		Save: func(_ context.Context, st *workflow.State) error {
			data, err := json.Marshal(st)
			saved = data
			return err
		},
	}
	_, err := first.Run(ctx, def, nil)
	require.ErrorIs(t, err, context.Canceled)

	var st workflow.State
	require.NoError(t, json.Unmarshal(saved, &st))
	require.Equal(t, []workflow.StepID{"main/b#1"}, st.InFlight())

	second := &local.Runner{Handlers: map[string]local.Handler{
		"action": func(ctx context.Context, c workflow.Command) (any, error) { count(c); return c.Step, nil },
		"agent":  func(ctx context.Context, c workflow.Command) (any, error) { count(c); return "b again", nil },
	}}
	final, err := second.Resume(context.Background(), def, &st)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, final.Status)
	require.Equal(t, map[string]int{"a": 1, "b": 2, "c": 1}, calls)
}

// Cancelling the context with local.Cancel cancels the run: the handlers
// in flight are cancelled, and Run returns the cancelled state, saved.
func TestCancelRun(t *testing.T) {
	def := load(t, `{"steps": [
	  {"name": "fork", "action": "x", "next": ["a", "b"]},
	  {"name": "a", "action": "x"},
	  {"name": "b", "action": "x"}]}`)
	ctx, cancel := context.WithCancelCause(context.Background())
	var started, stopped sync.WaitGroup
	started.Add(2)
	stopped.Add(2)
	var mu sync.Mutex
	var saved []workflow.RunStatus
	r := &local.Runner{
		Handlers: map[string]local.Handler{"action": func(ctx context.Context, c workflow.Command) (any, error) {
			if c.Step == "fork" {
				return nil, nil
			}
			started.Done()
			<-ctx.Done()
			stopped.Done()
			return nil, ctx.Err()
		}},
		Save: func(ctx context.Context, st *workflow.State) error {
			require.NoError(t, ctx.Err(), "the final save gets a live context")
			mu.Lock()
			saved = append(saved, st.Status)
			mu.Unlock()
			return nil
		},
	}
	go func() {
		started.Wait()
		cancel(local.Cancel("no longer needed"))
	}()
	st, err := r.Run(ctx, def, nil)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelled, st.Status)
	stopped.Wait()
	require.Equal(t, workflow.StatusCancelled, saved[len(saved)-1])

	// A plain cancellation still pauses the run.
	ctx, stop := context.WithCancel(context.Background())
	started.Add(2)
	stopped.Add(2)
	go func() { started.Wait(); stop() }()
	st, err = r.Run(ctx, def, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, workflow.StatusRunning, st.Status)
}

// A cancelled run still runs its finally steps, with live contexts, before
// Run returns.
func TestCancelRunRunsFinally(t *testing.T) {
	def := load(t, `{
	  "steps": [{"name": "work", "action": "x"}],
	  "finally": [
	    {"name": "release", "action": "x", "with": {"status": "${{ run.status }}"}},
	    {"name": "notify", "action": "x"}]}`)
	ctx, cancel := context.WithCancelCause(context.Background())
	started := make(chan struct{})
	var mu sync.Mutex
	var cleanup []string
	var saved []workflow.RunStatus
	r := &local.Runner{
		Handlers: map[string]local.Handler{"action": func(ctx context.Context, c workflow.Command) (any, error) {
			if c.Step == "work" {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			require.NoError(t, ctx.Err(), "finally steps run with a live context")
			mu.Lock()
			defer mu.Unlock()
			cleanup = append(cleanup, c.Step)
			if c.Step == "release" {
				require.Equal(t, "cancelled", c.Params["status"])
				return nil, &workflow.StepError{Type: "locked", Message: "still held"}
			}
			return "ok", nil
		}},
		Save: func(ctx context.Context, st *workflow.State) error {
			mu.Lock()
			saved = append(saved, st.Status)
			mu.Unlock()
			return nil
		},
	}
	go func() {
		<-started
		cancel(local.Cancel("stop"))
	}()
	st, err := r.Run(ctx, def, nil)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelled, st.Status)
	require.Equal(t, "stop", st.Reason)
	require.Equal(t, []string{"release", "notify"}, cleanup)
	require.Len(t, st.FinallyErrors, 1)
	require.Equal(t, "locked", st.FinallyErrors[0].Type)
	require.Contains(t, saved, workflow.StatusCancelling)
	require.Equal(t, workflow.StatusCancelled, saved[len(saved)-1])
}
