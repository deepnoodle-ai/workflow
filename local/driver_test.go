package local

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

func load(t *testing.T, doc string) *workflow.Definition {
	t.Helper()
	def, err := workflow.Load([]byte(doc), workflow.KindMap{"task": nil})
	require.NoError(t, err)
	return def
}

// A step that finishes before its timeout leaves no timer behind, and a
// duplicate event is not saved again.
func TestSettledStepDropsTimerAndDuplicateIsNotSaved(t *testing.T) {
	def := load(t, `{"steps": [
	  {"name": "a", "task": "x", "timeout": "1h", "next": ["b"]},
	  {"name": "b", "task": "x", "timeout": "1h"}]}`)
	var d *driver
	var saves atomic.Int32
	aTimerAtB := true
	r := &Runner{
		Save: func(context.Context, *workflow.State) error { saves.Add(1); return nil },
		Handlers: map[string]Handler{"task": func(ctx context.Context, c workflow.Command) (any, error) {
			if c.Step == "b" {
				d.mu.Lock()
				aTimerAtB = d.timers["main/a#1"] != nil
				d.mu.Unlock()
				// A duplicate delivery of a's result, applied before b's.
				d.events <- workflow.Event{At: d.now(), Kind: workflow.EventStepCompleted, StepID: "main/a#1", Output: "again"}
			}
			return c.Step, nil
		}},
	}
	d = r.newDriver(def)
	st, cmds, err := workflow.Apply(def, nil, workflow.Event{At: d.now(), Kind: workflow.EventStart})
	require.NoError(t, err)
	st, err = d.loop(context.Background(), st, cmds, false)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.False(t, aTimerAtB, "a's timeout timer was dropped when a settled")
	require.Equal(t, 0, len(d.timers))
	require.Equal(t, 0, len(d.cancels))
	require.Equal(t, int32(3), saves.Load(), "start, a, b; not the duplicate")
}
