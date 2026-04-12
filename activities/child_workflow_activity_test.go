package activities

import (
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestChildWorkflowActivity(t *testing.T) {
	t.Run("runs registered child workflow", func(t *testing.T) {
		wf, err := workflow.New(workflow.Options{
			Name: "child",
			Steps: []*workflow.Step{
				{Name: "greet", Activity: "greet"},
			},
			Outputs: []*workflow.Output{
				{Name: "greeting", Variable: "greeting"},
			},
		})
		require.NoError(t, err)

		reg := workflow.NewMemoryWorkflowRegistry()
		reg.Register(wf)

		greetAct := workflow.ActivityFunc("greet", func(ctx workflow.Context, params map[string]any) (any, error) {
			ctx.Set("greeting", "hello from child")
			return "hello", nil
		})

		executor, err := workflow.NewDefaultChildWorkflowExecutor(workflow.ChildWorkflowExecutorOptions{
			WorkflowRegistry: reg,
			Activities:       []workflow.Activity{greetAct},
		})
		require.NoError(t, err)

		activity := NewChildWorkflowActivity(executor)
		require.Equal(t, "workflow.child", activity.Name())

		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"workflow_name": "child"})
		require.NoError(t, err)
		m := result.(map[string]any)
		require.Equal(t, true, m["success"])
		require.Equal(t, "completed", m["status"])
	})

	t.Run("missing workflow_name", func(t *testing.T) {
		executor, err := workflow.NewDefaultChildWorkflowExecutor(workflow.ChildWorkflowExecutorOptions{
			WorkflowRegistry: workflow.NewMemoryWorkflowRegistry(),
		})
		require.NoError(t, err)
		activity := NewChildWorkflowActivity(executor)
		ctx := newTestContext()
		_, err = activity.Execute(ctx, map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "workflow_name")
	})

	t.Run("workflow not found", func(t *testing.T) {
		executor, err := workflow.NewDefaultChildWorkflowExecutor(workflow.ChildWorkflowExecutorOptions{
			WorkflowRegistry: workflow.NewMemoryWorkflowRegistry(),
		})
		require.NoError(t, err)
		activity := NewChildWorkflowActivity(executor)
		ctx := newTestContext()
		_, err = activity.Execute(ctx, map[string]any{"workflow_name": "missing"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "child workflow execution failed")
	})
}
