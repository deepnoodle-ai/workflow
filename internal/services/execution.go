// Package services provides application-level coordination for workflow operations.
package services

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// ExecutionRepository defines the execution operations needed by ExecutionService.
type ExecutionRepository interface {
	CreateExecution(ctx context.Context, record *workflow.ExecutionRecord) error
	GetExecution(ctx context.Context, id string) (*workflow.ExecutionRecord, error)
	UpdateExecution(ctx context.Context, record *workflow.ExecutionRecord) error
	ListExecutions(ctx context.Context, filter workflow.ExecutionFilter) ([]*workflow.ExecutionRecord, error)
}

// EventRepository defines the event operations needed by services.
type EventRepository interface {
	AppendEvent(ctx context.Context, event workflow.Event) error
}

// ExecutionService coordinates execution operations with event logging.
type ExecutionService struct {
	executions ExecutionRepository
	events     EventRepository
}

// ExecutionServiceOptions configures an ExecutionService.
type ExecutionServiceOptions struct {
	Executions ExecutionRepository
	Events     EventRepository
}

// NewExecutionService creates a new execution service.
func NewExecutionService(opts ExecutionServiceOptions) *ExecutionService {
	return &ExecutionService{
		executions: opts.Executions,
		events:     opts.Events,
	}
}

// Create persists a new execution record and logs a workflow_started event.
func (s *ExecutionService) Create(ctx context.Context, record *workflow.ExecutionRecord) error {
	if err := s.executions.CreateExecution(ctx, record); err != nil {
		return err
	}

	if s.events != nil {
		_ = s.events.AppendEvent(ctx, workflow.Event{
			ID:          workflow.NewExecutionID(),
			ExecutionID: record.ID,
			Timestamp:   time.Now(),
			Type:        workflow.EventWorkflowStarted,
			Data: map[string]any{
				"workflow_name": record.WorkflowName,
				"inputs":        record.Inputs,
			},
		})
	}

	return nil
}

// Get retrieves an execution by ID.
func (s *ExecutionService) Get(ctx context.Context, id string) (*workflow.ExecutionRecord, error) {
	return s.executions.GetExecution(ctx, id)
}

// Update updates an existing execution record.
// If the execution is completing (status changes to completed/failed/cancelled),
// logs the appropriate workflow event.
func (s *ExecutionService) Update(ctx context.Context, record *workflow.ExecutionRecord) error {
	// Get current state to detect status changes
	var prevStatus workflow.EngineExecutionStatus
	if s.events != nil {
		current, err := s.executions.GetExecution(ctx, record.ID)
		if err == nil {
			prevStatus = current.Status
		}
	}

	if err := s.executions.UpdateExecution(ctx, record); err != nil {
		return err
	}

	// Log events for status changes
	if s.events != nil && prevStatus != record.Status {
		var eventType workflow.EventType
		switch record.Status {
		case workflow.EngineStatusCompleted:
			eventType = workflow.EventWorkflowCompleted
		case workflow.EngineStatusFailed:
			eventType = workflow.EventWorkflowFailed
		}

		if eventType != "" {
			_ = s.events.AppendEvent(ctx, workflow.Event{
				ID:          workflow.NewExecutionID(),
				ExecutionID: record.ID,
				Timestamp:   time.Now(),
				Type:        eventType,
				Data: map[string]any{
					"status":  record.Status,
					"outputs": record.Outputs,
				},
				Error: record.LastError,
			})
		}
	}

	return nil
}

// List returns executions matching the filter.
func (s *ExecutionService) List(ctx context.Context, filter workflow.ExecutionFilter) ([]*workflow.ExecutionRecord, error) {
	return s.executions.ListExecutions(ctx, filter)
}

// Cancel requests cancellation of an execution.
func (s *ExecutionService) Cancel(ctx context.Context, id string) error {
	record, err := s.executions.GetExecution(ctx, id)
	if err != nil {
		return err
	}

	if record.Status == workflow.EngineStatusPending || record.Status == workflow.EngineStatusRunning {
		record.Status = workflow.EngineStatusCancelled
		record.LastError = "cancelled by user"
		record.CompletedAt = time.Now()
		return s.Update(ctx, record)
	}

	return nil
}
