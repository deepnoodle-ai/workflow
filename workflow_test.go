package workflow

import (
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestWorkflowStepNames(t *testing.T) {
	wf, err := New(Options{
		Name: "test-workflow",
		Steps: []*Step{
			{Name: "step1"},
			{Name: "step2"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"step1", "step2"}, wf.StepNames())

	steps := wf.Steps()
	require.Len(t, steps, 2)
	require.Equal(t, "step1", steps[0].Name)
	require.Equal(t, "step2", steps[1].Name)
}

func TestWorkflowDescription(t *testing.T) {
	t.Run("with description", func(t *testing.T) {
		wf, err := New(Options{
			Name:        "test-workflow",
			Description: "A test workflow",
			Steps: []*Step{
				{Name: "step1"},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "A test workflow", wf.Description())
	})

	t.Run("without description", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow",
			Steps: []*Step{
				{Name: "step1"},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "", wf.Description())
	})
}

func TestInvalidWorkflows(t *testing.T) {
	t.Run("empty workflow", func(t *testing.T) {
		_, err := New(Options{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "workflow name required")
	})

	t.Run("no steps", func(t *testing.T) {
		_, err := New(Options{
			Name: "test-workflow",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "steps required")
	})

	t.Run("empty step name", func(t *testing.T) {
		_, err := New(Options{
			Name:  "test-workflow",
			Steps: []*Step{{Name: ""}},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "step name required")
	})
}
