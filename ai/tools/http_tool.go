package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/deepnoodle-ai/workflow/ai"
)

// HTTPTool makes HTTP requests.
type HTTPTool struct {
	client       *http.Client
	allowedHosts []string // Empty means all hosts allowed
}

// HTTPToolOptions configures HTTPTool.
type HTTPToolOptions struct {
	// Client is an optional custom HTTP client.
	Client *http.Client

	// Timeout for requests (default: 30 seconds).
	Timeout time.Duration

	// AllowedHosts restricts which hosts can be accessed.
	AllowedHosts []string
}

// NewHTTPTool creates a new HTTP tool.
func NewHTTPTool(opts HTTPToolOptions) *HTTPTool {
	client := opts.Client
	if client == nil {
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}

	return &HTTPTool{
		client:       client,
		allowedHosts: opts.AllowedHosts,
	}
}

func (t *HTTPTool) Name() string {
	return "http_request"
}

func (t *HTTPTool) Description() string {
	return "Make an HTTP request to a URL"
}

func (t *HTTPTool) Schema() *ai.ToolSchema {
	return ai.NewObjectSchema().
		AddProperty("url", ai.StringProperty("The URL to request")).
		AddProperty("method", ai.EnumProperty("HTTP method", "GET", "POST", "PUT", "DELETE", "PATCH")).
		AddProperty("headers", &ai.Property{
			Type:        "object",
			Description: "HTTP headers to include",
		}).
		AddProperty("body", ai.StringProperty("Request body (for POST/PUT/PATCH)")).
		AddProperty("json_body", &ai.Property{
			Type:        "object",
			Description: "JSON body (automatically sets Content-Type)",
		}).
		AddRequired("url")
}

func (t *HTTPTool) Execute(ctx context.Context, args map[string]any) (*ai.ToolResult, error) {
	url, ok := args["url"].(string)
	if !ok {
		return &ai.ToolResult{
			Error:   "url is required and must be a string",
			Success: false,
		}, nil
	}

	method, _ := args["method"].(string)
	if method == "" {
		method = "GET"
	}

	// Build request body
	var bodyReader io.Reader
	headers := make(http.Header)

	if headersMap, ok := args["headers"].(map[string]any); ok {
		for k, v := range headersMap {
			if s, ok := v.(string); ok {
				headers.Set(k, s)
			}
		}
	}

	if jsonBody, ok := args["json_body"]; ok && jsonBody != nil {
		jsonBytes, err := json.Marshal(jsonBody)
		if err != nil {
			return &ai.ToolResult{
				Error:   fmt.Sprintf("failed to marshal JSON body: %v", err),
				Success: false,
			}, nil
		}
		bodyReader = bytes.NewReader(jsonBytes)
		headers.Set("Content-Type", "application/json")
	} else if body, ok := args["body"].(string); ok && body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return &ai.ToolResult{
			Error:   fmt.Sprintf("failed to create request: %v", err),
			Success: false,
		}, nil
	}

	// Set headers
	for k, v := range headers {
		req.Header[k] = v
	}

	// Check allowed hosts
	if len(t.allowedHosts) > 0 {
		allowed := false
		for _, host := range t.allowedHosts {
			if req.URL.Host == host {
				allowed = true
				break
			}
		}
		if !allowed {
			return &ai.ToolResult{
				Error:   fmt.Sprintf("host %q not in allowed hosts", req.URL.Host),
				Success: false,
			}, nil
		}
	}

	// Make request
	resp, err := t.client.Do(req)
	if err != nil {
		return &ai.ToolResult{
			Error:   fmt.Sprintf("request failed: %v", err),
			Success: false,
		}, nil
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ai.ToolResult{
			Error:   fmt.Sprintf("failed to read response: %v", err),
			Success: false,
		}, nil
	}

	// Build result
	result := map[string]any{
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"body":        string(respBody),
	}

	// Try to parse as JSON
	var jsonResp any
	if err := json.Unmarshal(respBody, &jsonResp); err == nil {
		result["json"] = jsonResp
	}

	// Add response headers
	respHeaders := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			respHeaders[k] = v[0]
		}
	}
	result["headers"] = respHeaders

	resultJSON, _ := json.Marshal(result)

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	return &ai.ToolResult{
		Output:  string(resultJSON),
		Success: success,
	}, nil
}

// Verify interface compliance.
var _ ai.Tool = (*HTTPTool)(nil)
