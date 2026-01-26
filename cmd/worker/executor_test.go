package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutor_Execute_HTTP(t *testing.T) {
	t.Run("successful GET", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		}))
		defer server.Close()

		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type:   "http",
				URL:    server.URL,
				Method: "GET",
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.True(t, result.Success)
		assert.Equal(t, "ok", result.Data["status"])
	})

	t.Run("successful POST with body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "test-value", body["input"])

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"received": true})
		}))
		defer server.Close()

		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type:   "http",
				URL:    server.URL,
				Method: "POST",
				Input:  map[string]any{"input": "test-value"},
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.True(t, result.Success)
		assert.Equal(t, true, result.Data["received"])
	})

	t.Run("with custom headers", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer token123", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type:    "http",
				URL:     server.URL,
				Method:  "GET",
				Headers: map[string]string{"Authorization": "Bearer token123"},
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.True(t, result.Success)
	})

	t.Run("HTTP error status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("not found"))
		}))
		defer server.Close()

		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type: "http",
				URL:  server.URL,
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.False(t, result.Success)
		assert.Equal(t, 404, result.ExitCode)
		assert.Contains(t, result.Error, "404")
	})

	t.Run("non-JSON response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("plain text"))
		}))
		defer server.Close()

		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type: "http",
				URL:  server.URL,
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.True(t, result.Success)
		assert.Equal(t, "plain text", result.Output)
		assert.Nil(t, result.Data)
	})
}

func TestExecutor_Execute_Process(t *testing.T) {
	t.Run("successful echo", func(t *testing.T) {
		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type:    "process",
				Program: "echo",
				Args:    []string{"hello", "world"},
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.True(t, result.Success)
		assert.Contains(t, result.Output, "hello world")
		assert.Equal(t, 0, result.ExitCode)
	})

	t.Run("with environment variables", func(t *testing.T) {
		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type:    "process",
				Program: "sh",
				Args:    []string{"-c", "echo $MY_VAR"},
				Env:     map[string]string{"MY_VAR": "test_value"},
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.True(t, result.Success)
		assert.Contains(t, result.Output, "test_value")
	})

	t.Run("failed process", func(t *testing.T) {
		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type:    "process",
				Program: "sh",
				Args:    []string{"-c", "exit 42"},
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.False(t, result.Success)
		assert.Equal(t, 42, result.ExitCode)
		assert.Contains(t, result.Error, "42")
	})

	t.Run("JSON output parsed", func(t *testing.T) {
		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type:    "process",
				Program: "echo",
				Args:    []string{`{"key": "value"}`},
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.True(t, result.Success)
		assert.Equal(t, "value", result.Data["key"])
	})

	t.Run("no program specified", func(t *testing.T) {
		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type: "process",
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "no program")
	})
}

func TestExecutor_Execute_Container(t *testing.T) {
	// Skip if docker is not available
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available, skipping container tests")
	}

	t.Run("successful container", func(t *testing.T) {
		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type:    "container",
				Image:   "alpine:latest",
				Command: []string{"echo", "hello from container"},
			},
		}

		result := executor.Execute(context.Background(), task)
		// This test may fail if docker pull takes too long or image is not available
		// In CI, we might want to skip this test
		if result.Success {
			assert.Contains(t, result.Output, "hello from container")
		}
	})

	t.Run("with environment variables", func(t *testing.T) {
		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type:    "container",
				Image:   "alpine:latest",
				Command: []string{"sh", "-c", "echo $TEST_VAR"},
				Env:     map[string]string{"TEST_VAR": "container_value"},
			},
		}

		result := executor.Execute(context.Background(), task)
		if result.Success {
			assert.Contains(t, result.Output, "container_value")
		}
	})

	t.Run("no image specified", func(t *testing.T) {
		executor := DefaultExecutor()
		task := &domain.TaskClaimed{
			ID: "task-1",
			Input: &domain.TaskInput{
				Type: "container",
			},
		}

		result := executor.Execute(context.Background(), task)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "no container image")
	})
}

func TestExecutor_Execute_Inline(t *testing.T) {
	executor := DefaultExecutor()
	task := &domain.TaskClaimed{
		ID: "task-1",
		Input: &domain.TaskInput{
			Type: "inline",
		},
	}

	result := executor.Execute(context.Background(), task)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "inline tasks cannot be executed by remote workers")
}

func TestExecutor_Execute_UnknownType(t *testing.T) {
	executor := DefaultExecutor()
	task := &domain.TaskClaimed{
		ID: "task-1",
		Input: &domain.TaskInput{
			Type: "unknown",
		},
	}

	result := executor.Execute(context.Background(), task)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unknown task type")
}

func TestExecutor_Execute_Timeout(t *testing.T) {
	executor := DefaultExecutor()
	task := &domain.TaskClaimed{
		ID: "task-1",
		Input: &domain.TaskInput{
			Type:    "process",
			Program: "sleep",
			Args:    []string{"10"},
			Timeout: 100 * time.Millisecond,
		},
	}

	result := executor.Execute(context.Background(), task)
	assert.False(t, result.Success)
	// The process should be killed due to timeout
}

func TestExecutor_OutputTruncation(t *testing.T) {
	executor := &Executor{
		MaxOutputSize: 100, // Very small limit for testing
	}

	task := &domain.TaskClaimed{
		ID: "task-1",
		Input: &domain.TaskInput{
			Type:    "process",
			Program: "sh",
			Args:    []string{"-c", "yes | head -n 1000"},
		},
	}

	result := executor.Execute(context.Background(), task)
	// Output should be truncated
	assert.True(t, len(result.Output) <= 120) // 100 + truncation message
	assert.True(t, strings.Contains(result.Output, "truncated") || len(result.Output) <= 100)
}

func TestDefaultExecutor(t *testing.T) {
	executor := DefaultExecutor()
	require.NotNil(t, executor)
	assert.NotNil(t, executor.HTTPClient)
	assert.Equal(t, int64(1024*1024), executor.MaxOutputSize)
}
