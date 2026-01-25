package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/task"
)

// TaskClient implements task operations over HTTP.
type TaskClient struct {
	baseURL    string
	httpClient *http.Client
	config     domain.StoreConfig
	token      string
}

// TaskClientOptions configures a TaskClient.
type TaskClientOptions struct {
	BaseURL    string
	HTTPClient *http.Client
	Config     domain.StoreConfig
	Token      string // Optional Bearer token for authentication
}

// NewTaskClient creates a new HTTP-based task client.
func NewTaskClient(opts TaskClientOptions) *TaskClient {
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}
	if opts.Config.HeartbeatInterval == 0 {
		opts.Config = domain.DefaultStoreConfig()
	}
	return &TaskClient{
		baseURL:    opts.BaseURL,
		httpClient: opts.HTTPClient,
		config:     opts.Config,
		token:      opts.Token,
	}
}

// setHeaders sets common headers for requests.
func (c *TaskClient) setHeaders(req *http.Request, workerID string) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("X-Worker-ID", workerID)
}

// CreateTask is not implemented for HTTP client (orchestrator creates tasks).
func (c *TaskClient) CreateTask(ctx context.Context, t *task.Record) error {
	return fmt.Errorf("CreateTask not supported over HTTP - tasks are created by the orchestrator")
}

// ClaimTask claims the next available task from the orchestrator.
// Returns nil if no tasks are available (server returns 204 No Content).
func (c *TaskClient) ClaimTask(ctx context.Context, workerID string) (*task.Claimed, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/tasks/claim", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req, workerID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	// 204 No Content means no tasks available
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("claim failed: status %d: %s", resp.StatusCode, string(body))
	}

	var claimed task.Claimed
	if err := json.NewDecoder(resp.Body).Decode(&claimed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &claimed, nil
}

// CompleteTask marks a task as completed.
func (c *TaskClient) CompleteTask(ctx context.Context, taskID, workerID string, result *task.Result) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/tasks/"+taskID+"/complete", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req, workerID)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return errors.New("task not owned by this worker")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("complete failed: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ReleaseTask returns a task to pending state for retry.
func (c *TaskClient) ReleaseTask(ctx context.Context, taskID, workerID string, retryAfter time.Duration) error {
	reqBody := ReleaseTaskRequest{RetryAfter: retryAfter}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/tasks/"+taskID+"/release", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req, workerID)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return errors.New("task not owned by this worker")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("release failed: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// HeartbeatTask updates the heartbeat timestamp for a running task.
func (c *TaskClient) HeartbeatTask(ctx context.Context, taskID, workerID string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/tasks/"+taskID+"/heartbeat", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req, workerID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return errors.New("task not owned by this worker")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("heartbeat failed: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetTask retrieves a task by ID.
func (c *TaskClient) GetTask(ctx context.Context, id string) (*task.Record, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/tasks/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("task %s not found", id)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get task failed: status %d: %s", resp.StatusCode, string(body))
	}

	var t task.Record
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &t, nil
}

// ListStaleTasks is not implemented for HTTP client (orchestrator handles reaping).
func (c *TaskClient) ListStaleTasks(ctx context.Context, cutoff time.Time) ([]*task.Record, error) {
	return nil, fmt.Errorf("ListStaleTasks not supported over HTTP - handled by orchestrator")
}

// ResetTask is not implemented for HTTP client (orchestrator handles reaping).
func (c *TaskClient) ResetTask(ctx context.Context, taskID string) error {
	return fmt.Errorf("ResetTask not supported over HTTP - handled by orchestrator")
}
