package runners

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ContainerRunner Tests

func TestContainerRunner_ToSpec(t *testing.T) {
	r := &ContainerRunner{
		Image:   "myimage:latest",
		Command: []string{"python", "script.py"},
	}

	params := map[string]any{
		"string_param": "hello",
		"int_param":    42,
		"nested_param": map[string]any{"key": "value"},
	}

	spec, err := r.ToSpec(context.Background(), params)
	require.NoError(t, err)

	assert.Equal(t, "container", spec.Type)
	assert.Equal(t, "myimage:latest", spec.Image)
	assert.Equal(t, []string{"python", "script.py"}, spec.Command)
	assert.Equal(t, "hello", spec.Env["string_param"])
	assert.Equal(t, "42", spec.Env["int_param"])
	assert.JSONEq(t, `{"key":"value"}`, spec.Env["nested_param"])
	assert.Equal(t, params, spec.Input)
}

func TestContainerRunner_ParseResult(t *testing.T) {
	r := &ContainerRunner{Image: "test"}

	t.Run("success with data", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Data:    map[string]any{"result": "ok"},
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		assert.Equal(t, "ok", data["result"])
	})

	t.Run("success with JSON output", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Output:  `{"computed": 123}`,
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		assert.Equal(t, float64(123), data["computed"])
	})

	t.Run("success with non-JSON output", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Output:  "plain text output",
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		assert.Equal(t, "plain text output", data["output"])
	})

	t.Run("failure", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: false,
			Error:   "container crashed",
		}
		_, err := r.ParseResult(result)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "container crashed")
	})
}

// ProcessRunner Tests

func TestProcessRunner_ToSpec(t *testing.T) {
	r := &ProcessRunner{
		Program: "python",
		Args:    []string{"-c", "print('hello')"},
		Dir:     "/tmp",
	}

	params := map[string]any{
		"input": "test",
		"count": 5,
	}

	spec, err := r.ToSpec(context.Background(), params)
	require.NoError(t, err)

	assert.Equal(t, "process", spec.Type)
	assert.Equal(t, "python", spec.Program)
	assert.Equal(t, []string{"-c", "print('hello')"}, spec.Args)
	assert.Equal(t, "/tmp", spec.Dir)
	assert.Equal(t, "test", spec.Env["input"])
	assert.Equal(t, "5", spec.Env["count"])
	assert.Equal(t, params, spec.Input)
}

func TestProcessRunner_ParseResult(t *testing.T) {
	r := &ProcessRunner{Program: "echo"}

	t.Run("success with data", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Data:    map[string]any{"status": "done"},
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		assert.Equal(t, "done", data["status"])
	})

	t.Run("success with JSON output", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Output:  `{"value": true}`,
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		assert.Equal(t, true, data["value"])
	})

	t.Run("success with non-JSON output", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Output:  "hello world",
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		assert.Equal(t, "hello world", data["output"])
	})

	t.Run("failure", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success:  false,
			Error:    "process failed",
			ExitCode: 1,
		}
		_, err := r.ParseResult(result)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "process failed")
		assert.Contains(t, err.Error(), "exit 1")
	})
}

func TestProcessRunner_Execute(t *testing.T) {
	t.Run("successful echo", func(t *testing.T) {
		r := &ProcessRunner{
			Program: "echo",
			Args:    []string{"hello"},
		}
		result, err := r.Execute(context.Background(), nil)
		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Data["output"], "hello")
	})

	t.Run("with params as env vars", func(t *testing.T) {
		r := &ProcessRunner{
			Program: "sh",
			Args:    []string{"-c", "echo $TEST_VAR"},
		}
		result, err := r.Execute(context.Background(), map[string]any{"TEST_VAR": "test_value"})
		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Data["output"], "test_value")
	})

	t.Run("failed command", func(t *testing.T) {
		r := &ProcessRunner{
			Program: "sh",
			Args:    []string{"-c", "exit 1"},
		}
		result, err := r.Execute(context.Background(), nil)
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Equal(t, 1, result.ExitCode)
	})

	t.Run("JSON output", func(t *testing.T) {
		r := &ProcessRunner{
			Program: "echo",
			Args:    []string{`{"key": "value"}`},
		}
		result, err := r.Execute(context.Background(), nil)
		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, "value", result.Data["key"])
	})
}

// HTTPRunner Tests

func TestHTTPRunner_ToSpec(t *testing.T) {
	r := &HTTPRunner{
		URL:     "https://api.example.com/endpoint",
		Method:  "PUT",
		Headers: map[string]string{"Authorization": "Bearer token"},
	}

	params := map[string]any{
		"data": "test",
	}

	spec, err := r.ToSpec(context.Background(), params)
	require.NoError(t, err)

	assert.Equal(t, "http", spec.Type)
	assert.Equal(t, "https://api.example.com/endpoint", spec.URL)
	assert.Equal(t, "PUT", spec.Method)
	assert.Equal(t, "Bearer token", spec.Headers["Authorization"])
	assert.Equal(t, "application/json", spec.Headers["Content-Type"])
	assert.JSONEq(t, `{"data":"test"}`, spec.Body)
	assert.Equal(t, params, spec.Input)
}

func TestHTTPRunner_ToSpec_DefaultMethod(t *testing.T) {
	r := &HTTPRunner{
		URL: "https://api.example.com",
	}

	spec, err := r.ToSpec(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "POST", spec.Method)
}

func TestHTTPRunner_ParseResult(t *testing.T) {
	r := &HTTPRunner{URL: "http://test"}

	t.Run("success with data", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Data:    map[string]any{"response": "ok"},
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		assert.Equal(t, "ok", data["response"])
	})

	t.Run("success with JSON output", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Output:  `{"items": [1, 2, 3]}`,
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		items, ok := data["items"].([]any)
		require.True(t, ok)
		assert.Len(t, items, 3)
	})

	t.Run("success with non-JSON output", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Output:  "plain response",
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		assert.Equal(t, "plain response", data["output"])
	})

	t.Run("failure", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: false,
			Error:   "connection timeout",
		}
		_, err := r.ParseResult(result)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "connection timeout")
	})
}

func TestHTTPRunner_Execute(t *testing.T) {
	t.Run("successful POST", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			var body map[string]any
			err := json.NewDecoder(r.Body).Decode(&body)
			require.NoError(t, err)
			assert.Equal(t, "test", body["input"])

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "success"})
		}))
		defer server.Close()

		r := &HTTPRunner{URL: server.URL}
		result, err := r.Execute(context.Background(), map[string]any{"input": "test"})
		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, "success", result.Data["status"])
	})

	t.Run("with custom headers", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		r := &HTTPRunner{
			URL:     server.URL,
			Headers: map[string]string{"Authorization": "Bearer secret"},
		}
		result, err := r.Execute(context.Background(), nil)
		require.NoError(t, err)
		assert.True(t, result.Success)
	})

	t.Run("HTTP error status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
		}))
		defer server.Close()

		r := &HTTPRunner{URL: server.URL}
		result, err := r.Execute(context.Background(), nil)
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "500")
	})

	t.Run("connection error", func(t *testing.T) {
		r := &HTTPRunner{
			URL:     "http://localhost:99999",
			Timeout: "100ms",
		}
		result, err := r.Execute(context.Background(), nil)
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "request failed")
	})

	t.Run("non-JSON response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("plain text response"))
		}))
		defer server.Close()

		r := &HTTPRunner{URL: server.URL}
		result, err := r.Execute(context.Background(), nil)
		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, "plain text response", result.Data["output"])
	})
}

// InlineRunner Tests

func TestInlineRunner_ToSpec(t *testing.T) {
	r := &InlineRunner{
		Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return nil, nil
		},
	}

	params := map[string]any{"key": "value"}
	spec, err := r.ToSpec(context.Background(), params)
	require.NoError(t, err)

	assert.Equal(t, "inline", spec.Type)
	assert.Equal(t, params, spec.Input)
}

func TestInlineRunner_ParseResult(t *testing.T) {
	r := &InlineRunner{}

	t.Run("success", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: true,
			Data:    map[string]any{"result": "computed"},
		}
		data, err := r.ParseResult(result)
		require.NoError(t, err)
		assert.Equal(t, "computed", data["result"])
	})

	t.Run("failure", func(t *testing.T) {
		result := &domain.TaskOutput{
			Success: false,
			Error:   "function panicked",
		}
		_, err := r.ParseResult(result)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "function panicked")
	})
}

func TestInlineRunner_Execute(t *testing.T) {
	t.Run("successful execution", func(t *testing.T) {
		r := &InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				return map[string]any{
					"doubled": params["value"].(float64) * 2,
				}, nil
			},
		}
		result, err := r.Execute(context.Background(), map[string]any{"value": float64(21)})
		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, float64(42), result.Data["doubled"])
	})

	t.Run("function error", func(t *testing.T) {
		r := &InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				return nil, assert.AnError
			},
		}
		result, err := r.Execute(context.Background(), nil)
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.NotEmpty(t, result.Error)
	})

	t.Run("context cancellation", func(t *testing.T) {
		r := &InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(1 * time.Second):
					return map[string]any{"result": "done"}, nil
				}
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result, err := r.Execute(ctx, nil)
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "canceled")
	})
}

// Interface compliance tests are already in runners.go
