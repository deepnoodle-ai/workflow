package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// HTTPInput defines the input parameters for the HTTP activity
type HTTPInput struct {
	URL             string            `json:"url"`
	Method          string            `json:"method"` // GET, POST, PUT, DELETE, etc.
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`             // JSON string or plain text
	JSONPayload     map[string]any    `json:"json_payload"`     // Alternative to body for JSON
	Timeout         float64           `json:"timeout"`          // in seconds, default 30
	FollowRedirects bool              `json:"follow_redirects"` // default true
}

// HTTPOutput defines the output of the HTTP activity
type HTTPOutput struct {
	StatusCode    int               `json:"status_code"`
	Status        string            `json:"status"`
	Headers       map[string]string `json:"headers"`
	Body          string            `json:"body"`
	JSONResponse  map[string]any    `json:"json_response,omitempty"`
	Success       bool              `json:"success"`
	ContentLength int64             `json:"content_length"`
}

// HTTPActivity can be used to make HTTP requests
type HTTPActivity struct{}

func NewHTTPActivity() workflow.Activity {
	return workflow.NewTypedActivity(&HTTPActivity{})
}

func (a *HTTPActivity) Name() string {
	return "http"
}

func (a *HTTPActivity) Execute(ctx context.Context, params HTTPInput) (HTTPOutput, error) {
	if params.URL == "" {
		return HTTPOutput{}, fmt.Errorf("URL cannot be empty")
	}

	// Default values
	if params.Method == "" {
		params.Method = "GET"
	}
	if params.Timeout <= 0 {
		params.Timeout = 30
	}

	// Prepare request body
	var bodyReader io.Reader
	if params.JSONPayload != nil {
		jsonData, err := json.Marshal(params.JSONPayload)
		if err != nil {
			return HTTPOutput{}, fmt.Errorf("failed to marshal JSON payload: %w", err)
		}
		bodyReader = bytes.NewReader(jsonData)
	} else if params.Body != "" {
		bodyReader = strings.NewReader(params.Body)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(params.Method), params.URL, bodyReader)
	if err != nil {
		return HTTPOutput{}, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	if params.Headers != nil {
		for key, value := range params.Headers {
			req.Header.Set(key, value)
		}
	}

	// Set content type for JSON payload
	if params.JSONPayload != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: time.Duration(params.Timeout * float64(time.Second)),
	}

	// Handle redirect policy
	if !params.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	// Make the request
	resp, err := client.Do(req)
	if err != nil {
		return HTTPOutput{}, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return HTTPOutput{}, fmt.Errorf("failed to read response body: %w", err)
	}

	// Prepare output
	output := HTTPOutput{
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Body:          string(respBody),
		Success:       resp.StatusCode >= 200 && resp.StatusCode < 300,
		ContentLength: resp.ContentLength,
		Headers:       make(map[string]string),
	}

	// Copy response headers
	for key, values := range resp.Header {
		if len(values) > 0 {
			output.Headers[key] = values[0]
		}
	}

	// Try to parse JSON response
	if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		var jsonResp map[string]any
		if err := json.Unmarshal(respBody, &jsonResp); err == nil {
			output.JSONResponse = jsonResp
		}
	}

	return output, nil
}
