package activities

import (
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/stretchr/testify/require"
)

func TestChildWorkflowActivity(t *testing.T) {
	t.Run("sync execution", func(t *testing.T) {
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

		greetAct := workflow.NewActivityFunction("greet", func(ctx workflow.Context, params map[string]any) (any, error) {
			ctx.SetVariable("greeting", "hello from child")
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
		result, err := activity.Execute(ctx, map[string]any{"workflow_name": "child", "sync": true})
		require.NoError(t, err)
		m := result.(map[string]any)
		require.Equal(t, true, m["success"])
		require.Equal(t, "completed", m["status"])
	})

	t.Run("async execution", func(t *testing.T) {
		wf, err := workflow.New(workflow.Options{
			Name:  "async-child",
			Steps: []*workflow.Step{{Name: "work", Activity: "work"}},
		})
		require.NoError(t, err)

		reg := workflow.NewMemoryWorkflowRegistry()
		reg.Register(wf)

		workAct := workflow.NewActivityFunction("work", func(ctx workflow.Context, params map[string]any) (any, error) {
			return "done", nil
		})

		executor, err := workflow.NewDefaultChildWorkflowExecutor(workflow.ChildWorkflowExecutorOptions{
			WorkflowRegistry: reg,
			Activities:       []workflow.Activity{workAct},
		})
		require.NoError(t, err)

		activity := NewChildWorkflowActivity(executor)
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"workflow_name": "async-child", "sync": false})
		require.NoError(t, err)
		m := result.(map[string]any)
		require.Equal(t, true, m["async"])
		require.NotEmpty(t, m["execution_id"])
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

	t.Run("workflow not found sync", func(t *testing.T) {
		executor, err := workflow.NewDefaultChildWorkflowExecutor(workflow.ChildWorkflowExecutorOptions{
			WorkflowRegistry: workflow.NewMemoryWorkflowRegistry(),
		})
		require.NoError(t, err)
		activity := NewChildWorkflowActivity(executor)
		ctx := newTestContext()
		_, err = activity.Execute(ctx, map[string]any{"workflow_name": "missing", "sync": true})
		require.Error(t, err)
		require.Contains(t, err.Error(), "child workflow execution failed")
	})
}
