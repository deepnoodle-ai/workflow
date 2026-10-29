package postgres

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestWorkerFieldsSetAfterACall: a Worker reads its fields at each call,
// so an Observer and a MayClaim gate set after a first call take effect.
func TestWorkerFieldsSetAfterACall(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "late")
	ctx := context.Background()
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"action": func(context.Context, workflow.Job) (any, error) { return "done", nil },
	}}
	require.Equal(t, 0, w.Active())

	var (
		claimed atomic.Int32
		open    atomic.Bool
	)
	w.Observer = Observer{Claimed: func(workflow.Job) { claimed.Add(1) }}
	w.MayClaim = open.Load
	require.NoError(t, w.RunOnce(ctx))
	require.Equal(t, int32(0), claimed.Load(), "the gate is closed")
	require.Equal(t, workflow.StatusRunning, runStatus(t, s, "late"))

	open.Store(true)
	require.NoError(t, w.RunOnce(ctx))
	require.Equal(t, int32(1), claimed.Load(), "the Observer set late sees the claim")
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "late"))
}
