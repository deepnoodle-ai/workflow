package activities

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPActivity(t *testing.T) {
	activity := NewHTTPActivity()
	require.Equal(t, "http", activity.Name())

	t.Run("empty url", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "URL cannot be empty")
	})

	t.Run("GET request", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "GET", r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status": "ok"}`))
		}))
		defer server.Close()

		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"url": server.URL})
		require.NoError(t, err)
		output := result.(HTTPOutput)
		require.Equal(t, 200, output.StatusCode)
		require.True(t, output.Success)
		require.Equal(t, "ok", output.JSONResponse["status"])
	})

	t.Run("POST with JSON payload", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "POST", r.Method)
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			w.WriteHeader(201)
			w.Write([]byte(`created`))
		}))
		defer server.Close()

		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"url": server.URL, "method": "POST", "json_payload": map[string]any{"key": "value"},
		})
		require.NoError(t, err)
		output := result.(HTTPOutput)
		require.Equal(t, 201, output.StatusCode)
		require.True(t, output.Success)
	})

	t.Run("POST with body string", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte(`ok`))
		}))
		defer server.Close()

		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"url": server.URL, "method": "POST", "body": "raw body content",
		})
		require.NoError(t, err)
		require.Equal(t, "ok", result.(HTTPOutput).Body)
	})

	t.Run("custom headers", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "my-token", r.Header.Get("Authorization"))
			w.WriteHeader(200)
			w.Write([]byte(`ok`))
		}))
		defer server.Close()

		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{
			"url": server.URL, "headers": map[string]string{"Authorization": "my-token"},
		})
		require.NoError(t, err)
	})

	t.Run("no follow redirects", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/other", http.StatusFound)
		}))
		defer server.Close()

		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"url": server.URL, "follow_redirects": false})
		require.NoError(t, err)
		require.Equal(t, 302, result.(HTTPOutput).StatusCode)
	})

	t.Run("4xx is not success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
			w.Write([]byte(`not found`))
		}))
		defer server.Close()

		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"url": server.URL})
		require.NoError(t, err)
		output := result.(HTTPOutput)
		require.False(t, output.Success)
		require.Equal(t, 404, output.StatusCode)
	})
}
