package client_test

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/client"
	"github.com/deepnoodle-ai/wonton/assert"
)

func TestLocalClient(t *testing.T) {
	t.Run("submit and wait for completion", func(t *testing.T) {
		registry := workflow.NewRegistry()

		// Register activity
		registry.MustRegisterActivity(workflow.NewActivityFunction("greet", func(ctx workflow.Context, params map[string]any) (any, error) {
			name := params["name"].(string)
			return "Hello, " + name + "!", nil
		}))

		// Register workflow
		wf, err := workflow.New(workflow.Options{
			Name: "greeting-workflow",
			Steps: []*workflow.Step{
				{
					Name:       "greet",
					Activity:   "greet",
					Parameters: map[string]any{"name": "$(inputs.name)"},
					Store:      "greeting",
				},
			},
			Outputs: []*workflow.Output{
				{Name: "greeting", Variable: "greeting"},
			},
		})
		assert.NoError(t, err)
		registry.MustRegisterWorkflow(wf)

		// Create local client
		localClient, err := client.NewLocalClient(client.LocalClientOptions{
			Registry: registry,
		})
		assert.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Submit workflow
		id, err := localClient.Submit(ctx, wf, map[string]any{"name": "World"})
		assert.NoError(t, err)
		assert.NotEmpty(t, id)

		// Wait for result
		result, err := localClient.Wait(ctx, id)
		assert.NoError(t, err)
		assert.Equal(t, result.Status, client.ExecutionStatusCompleted)

		// Stop client
		err = localClient.Stop(ctx)
		assert.NoError(t, err)
	})

	t.Run("submit by name", func(t *testing.T) {
		registry := workflow.NewRegistry()

		registry.MustRegisterActivity(workflow.NewActivityFunction("echo", func(ctx workflow.Context, params map[string]any) (any, error) {
			return params["message"], nil
		}))

		wf, _ := workflow.New(workflow.Options{
			Name: "echo-workflow",
			Steps: []*workflow.Step{
				{Name: "echo", Activity: "echo", Parameters: map[string]any{"message": "$(inputs.msg)"}},
			},
		})
		registry.MustRegisterWorkflow(wf)

		localClient, err := client.NewLocalClient(client.LocalClientOptions{
			Registry: registry,
		})
		assert.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Submit by name
		id, err := localClient.SubmitByName(ctx, "echo-workflow", map[string]any{"msg": "test"})
		assert.NoError(t, err)
		assert.NotEmpty(t, id)

		// Get status
		status, err := localClient.Get(ctx, id)
		assert.NoError(t, err)
		assert.Equal(t, status.WorkflowName, "echo-workflow")

		localClient.Stop(ctx)
	})

	t.Run("submit unknown workflow by name fails", func(t *testing.T) {
		registry := workflow.NewRegistry()

		localClient, err := client.NewLocalClient(client.LocalClientOptions{
			Registry: registry,
		})
		assert.NoError(t, err)

		ctx := context.Background()
		_, err = localClient.SubmitByName(ctx, "nonexistent", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")

		localClient.Stop(ctx)
	})

	t.Run("list executions", func(t *testing.T) {
		registry := workflow.NewRegistry()

		registry.MustRegisterActivity(workflow.NewActivityFunction("noop", func(ctx workflow.Context, params map[string]any) (any, error) {
			return nil, nil
		}))

		wf, _ := workflow.New(workflow.Options{
			Name:  "list-test",
			Steps: []*workflow.Step{{Name: "s", Activity: "noop"}},
		})
		registry.MustRegisterWorkflow(wf)

		localClient, err := client.NewLocalClient(client.LocalClientOptions{
			Registry: registry,
		})
		assert.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Submit a few workflows
		localClient.Submit(ctx, wf, nil)
		localClient.Submit(ctx, wf, nil)

		// Wait a bit for them to complete
		time.Sleep(100 * time.Millisecond)

		// List all
		statuses, err := localClient.List(ctx, client.ListFilter{})
		assert.NoError(t, err)
		assert.True(t, len(statuses) >= 2)

		// List by workflow name
		statuses, err = localClient.List(ctx, client.ListFilter{
			WorkflowName: "list-test",
		})
		assert.NoError(t, err)
		assert.True(t, len(statuses) >= 2)

		localClient.Stop(ctx)
	})
}
