package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestSharedNamesAreDurable pins the visible changes of moving the shared
// types to package durable (PRD 007, UC-7): %T names durable, the
// sentinels are durable's values with the workflow: prefix, and a
// durable.Observer is a Worker's Observer.
func TestSharedNamesAreDurable(t *testing.T) {
	require.Equal(t, "*durable.Result", fmt.Sprintf("%T", &Result{}))
	require.Equal(t, "durable.JobRef", fmt.Sprintf("%T", JobRef{}))
	for _, c := range []struct{ got, want error }{
		{ErrRunNotFound, durable.ErrRunNotFound},
		{ErrRunExists, durable.ErrRunExists},
		{ErrUnstorable, durable.ErrUnstorable},
		{ErrDefinitionUnavailable, durable.ErrDefinitionUnavailable},
		{ErrNotClaimable, durable.ErrNotClaimable},
		{ErrHardStop, durable.ErrHardStop},
	} {
		require.True(t, c.got == c.want, c.got)
		require.True(t, errors.Is(fmt.Errorf("wrapped: %w", c.got), c.want), c.got)
		require.Equal(t, "workflow:", c.got.Error()[:len("workflow:")])
	}
	metrics := durable.Observer{Settled: func(job workflow.Job, s durable.Settlement) {}}
	w := &Worker{Observer: metrics}
	require.True(t, w.Observer.Settled != nil)
}
