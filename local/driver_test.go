package local

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

func load(t *testing.T, doc string) *workflow.Definition {
	t.Helper()
	def, err := workflow.Load([]byte(doc), workflow.KindMap{"action": nil})
	require.NoError(t, err)
	return def
}

// A step that finishes before its timeout leaves no timer behind, and a
// duplicate event is not saved again.
func TestSettledStepDropsTimerAndDuplicateIsNotSaved(t *testing.T) {
	def := load(t, `{"steps": [
	  {"name": "a", "action": "x", "timeout": "1h", "next": ["b"]},
	  {"name": "b", "action": "x", "timeout": "1h"}]}`)
	var d *driver
	var saves atomic.Int32
	aTimerAtB := true
	r := &Runner{
		Save: func(context.Context, string, *workflow.State) error { saves.Add(1); return nil },
		Handlers: map[string]workflow.Handler{"action": func(ctx context.Context, c workflow.Job) (any, error) {
			if c.Step == "b" {
				d.mu.Lock()
				aTimerAtB = d.timers["main/a#1"] != nil
				d.mu.Unlock()
				// A duplicate delivery of a's result, applied before b's.
				d.run.events <- request{ev: workflow.Event{At: time.Now(), Kind: workflow.EventStepCompleted, StepID: "main/a#1", Output: "again"}}
			}
			return c.Step, nil
		}},
	}
	d = r.newDriver(def, "run")
	st, cmds, err := workflow.Apply(def, nil, workflow.Event{At: time.Now(), Kind: workflow.EventStart})
	require.NoError(t, err)
	st, err = d.start(context.Background(), st, cmds, false, 1).Wait()
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.False(t, aTimerAtB, "a's timeout timer was dropped when a settled")
	require.Equal(t, 0, len(d.timers))
	require.Equal(t, 0, len(d.cancels))
	require.Equal(t, int32(3), saves.Load(), "start, a, b; not the duplicate")
}
