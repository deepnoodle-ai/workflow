package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"
	"github.com/deepnoodle-ai/workflow/domain"
)

func TestWorkflowStepNames(t *testing.T) {
	wf, err := New(Options{
		Name: "test-workflow",
		Steps: []*Step{
			{Name: "step1"},
			{Name: "step2"},
		},
	})
	assert.NoError(t, err)
	assert.Equal(t, wf.StepNames(), []string{"step1", "step2"})

	steps := wf.Steps()
	assert.Len(t, steps, 2)
	assert.Equal(t, steps[0].Name, "step1")
	assert.Equal(t, steps[1].Name, "step2")
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
		assert.NoError(t, err)
		assert.Equal(t, wf.Description(), "A test workflow")
	})

	t.Run("without description", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow",
			Steps: []*Step{
				{Name: "step1"},
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, wf.Description(), "")
	})
}

func TestLoadString(t *testing.T) {
	t.Run("valid workflow", func(t *testing.T) {
		yamlData := `
name: test-workflow
description: A test workflow
steps:
  - name: step1
  - name: step2
`
		wf, err := LoadString(yamlData)
		assert.NoError(t, err)
		assert.Equal(t, wf.Name(), "test-workflow")
		assert.Equal(t, wf.Description(), "A test workflow")
		assert.Len(t, wf.Steps(), 2)
	})

	t.Run("invalid yaml", func(t *testing.T) {
		yamlData := `
name: test-workflow
steps:
  - name: step1
    invalid: [unclosed
`
		_, err := LoadString(yamlData)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal workflow file")
	})

	t.Run("valid yaml but invalid workflow", func(t *testing.T) {
		yamlData := `
name: test-workflow
steps: []
`
		_, err := LoadString(yamlData)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "steps required")
	})
}

func TestLoadFile(t *testing.T) {
	t.Run("valid workflow file", func(t *testing.T) {
		// Create a temporary file
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "test-workflow.yaml")

		yamlContent := `
name: test-workflow
description: A test workflow from file
steps:
  - name: step1
  - name: step2
`
		err := os.WriteFile(filePath, []byte(yamlContent), 0644)
		assert.NoError(t, err)

		wf, err := LoadFile(filePath)
		assert.NoError(t, err)
		assert.Equal(t, wf.Name(), "test-workflow")
		assert.Equal(t, wf.Description(), "A test workflow from file")
		assert.Len(t, wf.Steps(), 2)
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := LoadFile("non-existent-file.yaml")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read workflow file")
	})

	t.Run("invalid yaml file", func(t *testing.T) {
		// Create a temporary file with invalid YAML
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "invalid.yaml")

		invalidYaml := `
name: test-workflow
steps:
  - name: step1
    invalid: [unclosed
`
		err := os.WriteFile(filePath, []byte(invalidYaml), 0644)
		assert.NoError(t, err)

		_, err = LoadFile(filePath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal workflow file")
	})

	t.Run("valid yaml but invalid workflow file", func(t *testing.T) {
		// Create a temporary file with valid YAML but invalid workflow
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "invalid-workflow.yaml")

		yamlContent := `
name: ""
steps:
  - name: step1
`
		err := os.WriteFile(filePath, []byte(yamlContent), 0644)
		assert.NoError(t, err)

		_, err = LoadFile(filePath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "workflow name required")
	})
}

func TestInvalidWorkflows(t *testing.T) {
	t.Run("empty workflow", func(t *testing.T) {
		_, err := New(Options{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "workflow name required")
	})

	t.Run("no steps", func(t *testing.T) {
		_, err := New(Options{
			Name: "test-workflow",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "steps required")
	})

	t.Run("empty step name", func(t *testing.T) {
		_, err := New(Options{
			Name:  "test-workflow",
			Steps: []*Step{{Name: ""}},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "step name required")
	})
}

func TestRun(t *testing.T) {
	t.Run("simple workflow completes successfully", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow",
			Steps: []*Step{
				{Name: "greet", Activity: "greet"},
			},
		})
		assert.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		result, err := Run(ctx, wf, nil,
			NewActivityFunction("greet", func(ctx Context, params map[string]any) (any, error) {
				return "hello", nil
			}),
		)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, result.Status, domain.ExecutionStatusCompleted)
	})

	t.Run("returns error for invalid workflow", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow",
			Steps: []*Step{
				{Name: "step", Activity: "missing"},
			},
		})
		assert.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// No activities provided
		result, err := Run(ctx, wf, nil)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "activities required")
	})
}

func TestRegistryRun(t *testing.T) {
	t.Run("runs registered workflow", func(t *testing.T) {
		wf, err := New(Options{
			Name: "greeting-workflow",
			Steps: []*Step{
				{Name: "greet", Activity: "greet"},
			},
		})
		assert.NoError(t, err)

		registry := NewRegistry()
		registry.MustRegisterWorkflow(wf)
		registry.MustRegisterActivity(NewActivityFunction("greet", func(ctx Context, params map[string]any) (any, error) {
			return "hello from registry", nil
		}))

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		result, err := registry.Run(ctx, "greeting-workflow", nil)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, result.Status, domain.ExecutionStatusCompleted)
	})

	t.Run("returns error for unknown workflow", func(t *testing.T) {
		registry := NewRegistry()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		result, err := registry.Run(ctx, "unknown-workflow", nil)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "workflow \"unknown-workflow\" not registered")
	})
}

func TestRegistryNewExecution(t *testing.T) {
	t.Run("creates execution for registered workflow", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow",
			Steps: []*Step{
				{Name: "test", Activity: "test"},
			},
		})
		assert.NoError(t, err)

		registry := NewRegistry()
		registry.MustRegisterWorkflow(wf)
		registry.MustRegisterActivity(NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
			return nil, nil
		}))

		execution, err := registry.NewExecution("test-workflow", ExecutionOptions{
			ExecutionID: "custom-id-123",
		})
		assert.NoError(t, err)
		assert.NotNil(t, execution)
		assert.Equal(t, execution.ID(), "custom-id-123")
	})

	t.Run("returns error for unknown workflow", func(t *testing.T) {
		registry := NewRegistry()

		execution, err := registry.NewExecution("unknown-workflow", ExecutionOptions{})
		assert.Error(t, err)
		assert.Nil(t, execution)
		assert.Contains(t, err.Error(), "workflow \"unknown-workflow\" not registered")
	})
}
