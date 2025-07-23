package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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
		require.NoError(t, err)
		require.Equal(t, "test-workflow", wf.Name())
		require.Equal(t, "A test workflow", wf.Description())
		require.Len(t, wf.Steps(), 2)
	})

	t.Run("invalid yaml", func(t *testing.T) {
		yamlData := `
name: test-workflow
steps:
  - name: step1
    invalid: [unclosed
`
		_, err := LoadString(yamlData)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to unmarshal workflow file")
	})

	t.Run("valid yaml but invalid workflow", func(t *testing.T) {
		yamlData := `
name: test-workflow
steps: []
`
		_, err := LoadString(yamlData)
		require.Error(t, err)
		require.Contains(t, err.Error(), "steps required")
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
		require.NoError(t, err)

		wf, err := LoadFile(filePath)
		require.NoError(t, err)
		require.Equal(t, "test-workflow", wf.Name())
		require.Equal(t, "A test workflow from file", wf.Description())
		require.Len(t, wf.Steps(), 2)
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := LoadFile("non-existent-file.yaml")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read workflow file")
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
		require.NoError(t, err)

		_, err = LoadFile(filePath)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to unmarshal workflow file")
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
		require.NoError(t, err)

		_, err = LoadFile(filePath)
		require.Error(t, err)
		require.Contains(t, err.Error(), "workflow name required")
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
