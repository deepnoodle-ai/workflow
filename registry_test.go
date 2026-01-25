package workflow

import (
	"testing"

	"github.com/deepnoodle-ai/wonton/assert"
)

func TestRegistry(t *testing.T) {
	t.Run("register and retrieve workflow", func(t *testing.T) {
		registry := NewRegistry()

		wf, err := New(Options{
			Name: "test-workflow",
			Steps: []*Step{
				{Name: "step1", Activity: "act1"},
			},
		})
		assert.NoError(t, err)

		err = registry.RegisterWorkflow(wf)
		assert.NoError(t, err)

		retrieved, ok := registry.GetWorkflow("test-workflow")
		assert.True(t, ok)
		assert.Equal(t, retrieved.Name(), "test-workflow")
	})

	t.Run("register and retrieve activity", func(t *testing.T) {
		registry := NewRegistry()

		act := NewActivityFunction("test-activity", func(ctx Context, params map[string]any) (any, error) {
			return "result", nil
		})

		err := registry.RegisterActivity(act)
		assert.NoError(t, err)

		retrieved, ok := registry.GetActivity("test-activity")
		assert.True(t, ok)
		assert.Equal(t, retrieved.Name(), "test-activity")
	})

	t.Run("duplicate workflow registration fails", func(t *testing.T) {
		registry := NewRegistry()

		wf, _ := New(Options{
			Name:  "duplicate",
			Steps: []*Step{{Name: "s", Activity: "a"}},
		})

		err := registry.RegisterWorkflow(wf)
		assert.NoError(t, err)

		err = registry.RegisterWorkflow(wf)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
	})

	t.Run("duplicate activity registration fails", func(t *testing.T) {
		registry := NewRegistry()

		act := NewActivityFunction("duplicate", func(ctx Context, params map[string]any) (any, error) {
			return nil, nil
		})

		err := registry.RegisterActivity(act)
		assert.NoError(t, err)

		err = registry.RegisterActivity(act)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
	})

	t.Run("list workflows and activities", func(t *testing.T) {
		registry := NewRegistry()

		wf1, _ := New(Options{Name: "wf1", Steps: []*Step{{Name: "s", Activity: "a"}}})
		wf2, _ := New(Options{Name: "wf2", Steps: []*Step{{Name: "s", Activity: "a"}}})

		act1 := NewActivityFunction("act1", func(ctx Context, params map[string]any) (any, error) { return nil, nil })
		act2 := NewActivityFunction("act2", func(ctx Context, params map[string]any) (any, error) { return nil, nil })

		registry.MustRegisterWorkflow(wf1)
		registry.MustRegisterWorkflow(wf2)
		registry.MustRegisterActivity(act1)
		registry.MustRegisterActivity(act2)

		workflows := registry.Workflows()
		assert.Len(t, workflows, 2)

		activities := registry.Activities()
		assert.Len(t, activities, 2)

		wfNames := registry.WorkflowNames()
		assert.Len(t, wfNames, 2)

		actNames := registry.ActivityNames()
		assert.Len(t, actNames, 2)
	})

	t.Run("clear removes all registrations", func(t *testing.T) {
		registry := NewRegistry()

		wf, _ := New(Options{Name: "wf", Steps: []*Step{{Name: "s", Activity: "a"}}})
		act := NewActivityFunction("act", func(ctx Context, params map[string]any) (any, error) { return nil, nil })

		registry.MustRegisterWorkflow(wf)
		registry.MustRegisterActivity(act)

		assert.Len(t, registry.Workflows(), 1)
		assert.Len(t, registry.Activities(), 1)

		registry.Clear()

		assert.Len(t, registry.Workflows(), 0)
		assert.Len(t, registry.Activities(), 0)
	})

	t.Run("get non-existent returns false", func(t *testing.T) {
		registry := NewRegistry()

		_, ok := registry.GetWorkflow("nonexistent")
		assert.False(t, ok)

		_, ok = registry.GetActivity("nonexistent")
		assert.False(t, ok)
	})
}
