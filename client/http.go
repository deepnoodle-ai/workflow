package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// HTTPClient implements Client over HTTP.
type HTTPClient struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

// HTTPClientOptions configures an HTTPClient.
type HTTPClientOptions struct {
	BaseURL string
	Token   string
	Timeout time.Duration
}

// NewHTTPClient creates a new HTTP-based workflow client.
func NewHTTPClient(opts HTTPClientOptions) *HTTPClient {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &HTTPClient{
		baseURL: opts.BaseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		token: opts.Token,
	}
}

func (c *HTTPClient) Submit(ctx context.Context, wf *workflow.Workflow, inputs map[string]any) (string, error) {
	body := map[string]any{
		"workflow_name": wf.Name(),
		"inputs":        inputs,
	}

	var resp struct {
		ID string `json:"id"`
	}

	if err := c.post(ctx, "/executions", body, &resp); err != nil {
		return "", err
	}

	return resp.ID, nil
}

func (c *HTTPClient) Get(ctx context.Context, id string) (*Status, error) {
	var resp statusResponse
	if err := c.get(ctx, "/executions/"+id, &resp); err != nil {
		return nil, err
	}

	return &Status{
		ID:           resp.ID,
		WorkflowName: resp.WorkflowName,
		State:        State(resp.Status),
		CurrentStep:  resp.CurrentStep,
		Error:        resp.Error,
		CreatedAt:    resp.CreatedAt,
		StartedAt:    resp.StartedAt,
		CompletedAt:  resp.CompletedAt,
	}, nil
}

func (c *HTTPClient) Cancel(ctx context.Context, id string) error {
	return c.post(ctx, "/executions/"+id+"/cancel", nil, nil)
}

func (c *HTTPClient) Wait(ctx context.Context, id string) (*Result, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			status, err := c.Get(ctx, id)
			if err != nil {
				return nil, err
			}

			switch status.State {
			case StateCompleted, StateFailed, StateCancelled:
				// Fetch full result
				var resp resultResponse
				if err := c.get(ctx, "/executions/"+id+"/result", &resp); err != nil {
					return nil, err
				}

				var duration time.Duration
				if !status.CompletedAt.IsZero() && !status.StartedAt.IsZero() {
					duration = status.CompletedAt.Sub(status.StartedAt)
				}

				return &Result{
					ID:           status.ID,
					WorkflowName: status.WorkflowName,
					State:        status.State,
					Outputs:      resp.Outputs,
					Error:        status.Error,
					Duration:     duration,
				}, nil
			}
		}
	}
}

func (c *HTTPClient) List(ctx context.Context, filter ListFilter) ([]*Status, error) {
	path := "/executions"
	if filter.WorkflowName != "" {
		path += "?workflow=" + filter.WorkflowName
	}

	var resp []statusResponse
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, err
	}

	statuses := make([]*Status, len(resp))
	for i, r := range resp {
		statuses[i] = &Status{
			ID:           r.ID,
			WorkflowName: r.WorkflowName,
			State:        State(r.Status),
			CurrentStep:  r.CurrentStep,
			Error:        r.Error,
			CreatedAt:    r.CreatedAt,
			StartedAt:    r.StartedAt,
			CompletedAt:  r.CompletedAt,
		}
	}

	return statuses, nil
}

func (c *HTTPClient) get(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return err
	}

	return c.doRequest(req, result)
}

func (c *HTTPClient) post(ctx context.Context, path string, body, result any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.doRequest(req, result)
}

func (c *HTTPClient) doRequest(req *http.Request, result any) error {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("request failed: %s - %s", resp.Status, string(body))
	}

	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}

	return nil
}

type statusResponse struct {
	ID           string    `json:"id"`
	WorkflowName string    `json:"workflow_name"`
	Status       string    `json:"status"`
	CurrentStep  string    `json:"current_step"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
}

type resultResponse struct {
	Outputs map[string]any `json:"outputs"`
}

// Verify interface compliance
var _ Client = (*HTTPClient)(nil)
