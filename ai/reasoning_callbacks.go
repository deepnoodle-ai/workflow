package ai

import (
	"context"
	"log/slog"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/domain"
)

// ReasoningCallbacks captures reasoning events during agent execution.
// It implements workflow.ExecutionCallbacks and emits events to an EventLog.
type ReasoningCallbacks struct {
	workflow.BaseExecutionCallbacks
	eventLog domain.EventLog
	logger   *slog.Logger
}

// ReasoningCallbacksOptions configures ReasoningCallbacks.
type ReasoningCallbacksOptions struct {
	// EventLog to emit reasoning events to.
	EventLog domain.EventLog

	// Logger for callback operations.
	Logger *slog.Logger
}

// NewReasoningCallbacks creates a new ReasoningCallbacks.
func NewReasoningCallbacks(opts ReasoningCallbacksOptions) *ReasoningCallbacks {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &ReasoningCallbacks{
		eventLog: opts.EventLog,
		logger:   logger,
	}
}

// BeforeActivityExecution logs the start of an activity.
func (r *ReasoningCallbacks) BeforeActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) {
	if r.eventLog == nil {
		return
	}

	// Only log for agent activities
	if event.ActivityName == "" {
		return
	}

	r.logger.Debug("agent activity starting",
		"activity", event.ActivityName,
		"step", event.StepName,
		"execution_id", event.ExecutionID)
}

// AfterActivityExecution logs the completion of an activity.
func (r *ReasoningCallbacks) AfterActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) {
	if r.eventLog == nil {
		return
	}

	if event.Error != nil {
		_ = r.eventLog.Append(ctx, domain.Event{
			ExecutionID: event.ExecutionID,
			Type:        EventAgentError,
			StepName:    event.StepName,
			PathID:      event.PathID,
			Error:       event.Error.Error(),
			Data: map[string]any{
				"activity_name": event.ActivityName,
				"duration_ms":   event.Duration.Milliseconds(),
			},
		})
		return
	}

	r.logger.Debug("agent activity completed",
		"activity", event.ActivityName,
		"step", event.StepName,
		"duration", event.Duration)
}

// RecordDecision records a high-level agent decision.
func (r *ReasoningCallbacks) RecordDecision(ctx context.Context, executionID, stepName, pathID string, decision DecisionRecord) error {
	if r.eventLog == nil {
		return nil
	}

	return r.eventLog.Append(ctx, domain.Event{
		ExecutionID: executionID,
		Type:        EventAgentDecision,
		StepName:    stepName,
		PathID:      pathID,
		Data: map[string]any{
			"decision":     decision.Decision,
			"rationale":    decision.Rationale,
			"alternatives": decision.Alternatives,
			"confidence":   decision.Confidence,
			"metadata":     decision.Metadata,
		},
	})
}

// RecordThinking records agent thinking/reasoning.
func (r *ReasoningCallbacks) RecordThinking(ctx context.Context, executionID, stepName, pathID string, thinking ThinkingRecord) error {
	if r.eventLog == nil {
		return nil
	}

	return r.eventLog.Append(ctx, domain.Event{
		ExecutionID: executionID,
		Type:        EventAgentThinking,
		StepName:    stepName,
		PathID:      pathID,
		Data: map[string]any{
			"content": thinking.Content,
			"turn":    thinking.Turn,
			"model":   thinking.Model,
		},
	})
}

// RecordToolCall records a tool call.
func (r *ReasoningCallbacks) RecordToolCall(ctx context.Context, executionID, stepName, pathID string, call ToolCallRecord) error {
	if r.eventLog == nil {
		return nil
	}

	return r.eventLog.Append(ctx, domain.Event{
		ExecutionID: executionID,
		Type:        EventAgentToolCall,
		StepName:    stepName,
		PathID:      pathID,
		Data: map[string]any{
			"call_id":   call.CallID,
			"tool_name": call.ToolName,
			"arguments": call.Arguments,
			"turn":      call.Turn,
		},
	})
}

// RecordToolResult records a tool result.
func (r *ReasoningCallbacks) RecordToolResult(ctx context.Context, executionID, stepName, pathID string, result ToolResultRecord) error {
	if r.eventLog == nil {
		return nil
	}

	return r.eventLog.Append(ctx, domain.Event{
		ExecutionID: executionID,
		Type:        EventAgentToolResult,
		StepName:    stepName,
		PathID:      pathID,
		Data: map[string]any{
			"call_id":   result.CallID,
			"tool_name": result.ToolName,
			"result":    result.Result,
			"duration":  result.Duration,
			"turn":      result.Turn,
		},
	})
}

// QueryReasoningEvents retrieves reasoning events for an execution.
type QueryReasoningEventsOptions struct {
	// ExecutionID to filter by.
	ExecutionID string

	// Types to filter by (empty = all reasoning types).
	Types []domain.EventType

	// Limit maximum results.
	Limit int
}

// QueryReasoningEvents retrieves reasoning events from the event log.
func (r *ReasoningCallbacks) QueryReasoningEvents(ctx context.Context, opts QueryReasoningEventsOptions) ([]domain.Event, error) {
	if r.eventLog == nil {
		return nil, nil
	}

	types := opts.Types
	if len(types) == 0 {
		types = []domain.EventType{
			EventAgentThinking,
			EventAgentToolCall,
			EventAgentToolResult,
			EventAgentDecision,
			EventAgentError,
		}
	}

	return r.eventLog.List(ctx, opts.ExecutionID, domain.EventFilter{
		Types: types,
		Limit: opts.Limit,
	})
}

// Verify interface compliance.
var _ workflow.ExecutionCallbacks = (*ReasoningCallbacks)(nil)
