package client

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/engine"
	"github.com/deepnoodle-ai/workflow/stores"
)

// LocalClient implements Client using an in-process engine.
// This is useful for development, testing, and simple deployments
// where you don't need distributed execution.
//
// Use LocalClient for:
//   - Development environments that mirror the production Client interface
//   - Running multiple workflows concurrently with async Submit/Wait patterns
//   - Integration tests that need the full Client interface
//
// For simple synchronous execution of a single workflow, use workflow.Execution instead.
type LocalClient struct {
	registry *workflow.Registry
	engine   *engine.Engine
	store    domain.Store
	logger   *slog.Logger

	mu      sync.RWMutex
	started bool
}

// LocalClientOptions configures a LocalClient.
type LocalClientOptions struct {
	Registry *workflow.Registry
	Logger   *slog.Logger
	Clock    workflow.Clock
}

// NewLocalClient creates a new local client backed by the registry.
// The client runs an in-process engine for workflow execution.
func NewLocalClient(opts LocalClientOptions) (*LocalClient, error) {
	if opts.Registry == nil {
		return nil, fmt.Errorf("registry is required")
	}

	logger := opts.Logger
	if logger == nil {
		logger = workflow.NewLogger()
	}

	// Create in-memory store
	store := stores.NewMemoryStore()

	// Build runners from registered activities
	runners := make(map[string]domain.Runner)
	for _, act := range opts.Registry.Activities() {
		if runnable, ok := act.(workflow.RunnableActivity); ok {
			runners[act.Name()] = runnable.Runner()
		} else {
			runners[act.Name()] = workflow.NewActivityRunner(act,
				workflow.WithActivityLogger(logger))
		}
	}

	// Create engine
	eng, err := engine.New(engine.Options{
		Store:         store,
		Logger:        logger,
		Runners:       runners,
		Mode:          engine.ModeEmbedded,
		WorkerID:      "local-client",
		MaxConcurrent: 10,
		PollInterval:  10 * time.Millisecond,
	})
	if err != nil {
		return nil, fmt.Errorf("create engine: %w", err)
	}

	return &LocalClient{
		registry: opts.Registry,
		engine:   eng,
		store:    store,
		logger:   logger,
	}, nil
}

// Start starts the local engine. Must be called before submitting workflows.
func (c *LocalClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return nil
	}

	if err := c.engine.Start(ctx); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}

	c.started = true
	return nil
}

// Stop gracefully shuts down the local engine.
func (c *LocalClient) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return nil
	}

	if err := c.engine.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown engine: %w", err)
	}

	c.started = false
	return nil
}

// Submit starts a new workflow execution.
func (c *LocalClient) Submit(ctx context.Context, wf *workflow.Workflow, inputs map[string]any) (string, error) {
	c.mu.RLock()
	started := c.started
	c.mu.RUnlock()

	if !started {
		// Auto-start if not started
		if err := c.Start(ctx); err != nil {
			return "", err
		}
	}

	handle, err := c.engine.Submit(ctx, engine.SubmitRequest{
		Workflow: wf,
		Inputs:   inputs,
	})
	if err != nil {
		return "", fmt.Errorf("submit workflow: %w", err)
	}

	return handle.ID, nil
}

// SubmitByName starts a workflow by its registered name.
func (c *LocalClient) SubmitByName(ctx context.Context, workflowName string, inputs map[string]any) (string, error) {
	wf, ok := c.registry.GetWorkflow(workflowName)
	if !ok {
		return "", fmt.Errorf("workflow %q not found in registry", workflowName)
	}
	return c.Submit(ctx, wf, inputs)
}

// Get retrieves the current status of an execution.
func (c *LocalClient) Get(ctx context.Context, id string) (*Status, error) {
	record, err := c.engine.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	return recordToStatus(record), nil
}

// Cancel requests cancellation of an execution.
func (c *LocalClient) Cancel(ctx context.Context, id string) error {
	return c.engine.Cancel(ctx, id)
}

// Wait blocks until the execution completes and returns the result.
func (c *LocalClient) Wait(ctx context.Context, id string) (*Result, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			record, err := c.engine.Get(ctx, id)
			if err != nil {
				return nil, err
			}

			switch record.Status {
			case domain.ExecutionStatusCompleted, domain.ExecutionStatusFailed, domain.ExecutionStatusCancelled:
				status := recordToStatus(record)

				// Extract outputs
				outputs := make(map[string]any)
				if record.Status == domain.ExecutionStatusCompleted {
					// Load state to get outputs
					state, err := engine.LoadState(record)
					if err == nil && state != nil {
						if mainPath := state.GetPathState("main"); mainPath != nil {
							outputs = mainPath.Variables
						}
					}
				}

				var duration time.Duration
				if !record.CompletedAt.IsZero() && !record.CreatedAt.IsZero() {
					duration = record.CompletedAt.Sub(record.CreatedAt)
				}

				return &Result{
					ID:           record.ID,
					WorkflowName: record.WorkflowName,
					Status:       status.Status,
					Outputs:      outputs,
					Error:        record.LastError,
					Duration:     duration,
				}, nil
			}
		}
	}
}

// List returns executions matching the filter.
func (c *LocalClient) List(ctx context.Context, filter ListFilter) ([]*Status, error) {
	domainFilter := domain.ExecutionFilter{
		WorkflowName: filter.WorkflowName,
		Limit:        filter.Limit,
		Offset:       filter.Offset,
	}

	// Convert states
	for _, s := range filter.States {
		domainFilter.Statuses = append(domainFilter.Statuses, domain.ExecutionStatus(s))
	}

	records, err := c.engine.List(ctx, domainFilter)
	if err != nil {
		return nil, err
	}

	statuses := make([]*Status, len(records))
	for i, r := range records {
		statuses[i] = recordToStatus(r)
	}

	return statuses, nil
}

func recordToStatus(record *domain.ExecutionRecord) *Status {
	var state ExecutionStatus
	switch record.Status {
	case domain.ExecutionStatusPending:
		state = ExecutionStatusPending
	case domain.ExecutionStatusRunning:
		state = ExecutionStatusRunning
	case domain.ExecutionStatusCompleted:
		state = ExecutionStatusCompleted
	case domain.ExecutionStatusFailed:
		state = ExecutionStatusFailed
	case domain.ExecutionStatusCancelled:
		state = ExecutionStatusCancelled
	default:
		state = ExecutionStatusPending
	}

	return &Status{
		ID:           record.ID,
		WorkflowName: record.WorkflowName,
		Status:       state,
		Error:        record.LastError,
		CreatedAt:    record.CreatedAt,
		CompletedAt:  record.CompletedAt,
	}
}

// Verify interface compliance
var _ Client = (*LocalClient)(nil)
