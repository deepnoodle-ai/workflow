package workflow

import (
	"context"
	"time"
)

// ExecutionCallbacks defines the callback interface for workflow execution events
type ExecutionCallbacks interface {
	// Workflow-level callbacks
	BeforeWorkflowExecution(ctx context.Context, event *WorkflowExecutionEvent)
	AfterWorkflowExecution(ctx context.Context, event *WorkflowExecutionEvent)

	// Path-level callbacks
	BeforePathExecution(ctx context.Context, event *PathExecutionEvent)
	AfterPathExecution(ctx context.Context, event *PathExecutionEvent)

	// Activity-level callbacks
	BeforeActivityExecution(ctx context.Context, event *ActivityExecutionEvent)
	AfterActivityExecution(ctx context.Context, event *ActivityExecutionEvent)
}

// WorkflowExecutionEvent provides context for workflow-level execution events
type WorkflowExecutionEvent struct {
	ExecutionID  string
	WorkflowName string
	Status       ExecutionStatus
	StartTime    time.Time
	EndTime      time.Time
	Duration     time.Duration
	Inputs       map[string]any
	Outputs      map[string]any
	PathCount    int
	Error        error
}

// PathExecutionEvent provides context for path-level execution events
type PathExecutionEvent struct {
	ExecutionID  string
	WorkflowName string
	PathID       string
	Status       PathStatus
	StartTime    time.Time
	EndTime      time.Time
	Duration     time.Duration
	CurrentStep  string
	StepOutputs  map[string]any
	Error        error
}

// ActivityExecutionEvent provides context for activity execution events
type ActivityExecutionEvent struct {
	ExecutionID  string
	WorkflowName string
	PathID       string
	StepName     string
	ActivityName string
	Parameters   map[string]any
	Result       any
	StartTime    time.Time
	EndTime      time.Time
	Duration     time.Duration
	Error        error
}

// BaseExecutionCallbacks provides a default implementation that does nothing
type BaseExecutionCallbacks struct{}

func (n *BaseExecutionCallbacks) BeforeWorkflowExecution(ctx context.Context, event *WorkflowExecutionEvent) {
	// noop
}

func (n *BaseExecutionCallbacks) AfterWorkflowExecution(ctx context.Context, event *WorkflowExecutionEvent) {
	// noop
}

func (n *BaseExecutionCallbacks) BeforePathExecution(ctx context.Context, event *PathExecutionEvent) {
	// noop
}

func (n *BaseExecutionCallbacks) AfterPathExecution(ctx context.Context, event *PathExecutionEvent) {
	// noop
}

func (n *BaseExecutionCallbacks) BeforeActivityExecution(ctx context.Context, event *ActivityExecutionEvent) {
	// noop
}

func (n *BaseExecutionCallbacks) AfterActivityExecution(ctx context.Context, event *ActivityExecutionEvent) {
	// noop
}

// NewBaseExecutionCallbacks creates a new no-op callbacks implementation.
// Embed this in your own callbacks to get a default implementation that does nothing.
func NewBaseExecutionCallbacks() ExecutionCallbacks {
	return &BaseExecutionCallbacks{}
}

// CallbackChain allows chaining multiple callback implementations
type CallbackChain struct {
	callbacks []ExecutionCallbacks
}

// NewCallbackChain creates a new callback chain
func NewCallbackChain(callbacks ...ExecutionCallbacks) *CallbackChain {
	return &CallbackChain{callbacks: callbacks}
}

// Add adds a callback to the chain
func (c *CallbackChain) Add(callback ExecutionCallbacks) {
	c.callbacks = append(c.callbacks, callback)
}

func (c *CallbackChain) BeforeWorkflowExecution(ctx context.Context, event *WorkflowExecutionEvent) {
	for _, callback := range c.callbacks {
		callback.BeforeWorkflowExecution(ctx, event)
	}
}

func (c *CallbackChain) AfterWorkflowExecution(ctx context.Context, event *WorkflowExecutionEvent) {
	for _, callback := range c.callbacks {
		callback.AfterWorkflowExecution(ctx, event)
	}
}

func (c *CallbackChain) BeforePathExecution(ctx context.Context, event *PathExecutionEvent) {
	for _, callback := range c.callbacks {
		callback.BeforePathExecution(ctx, event)
	}
}

func (c *CallbackChain) AfterPathExecution(ctx context.Context, event *PathExecutionEvent) {
	for _, callback := range c.callbacks {
		callback.AfterPathExecution(ctx, event)
	}
}

func (c *CallbackChain) BeforeActivityExecution(ctx context.Context, event *ActivityExecutionEvent) {
	for _, callback := range c.callbacks {
		callback.BeforeActivityExecution(ctx, event)
	}
}

func (c *CallbackChain) AfterActivityExecution(ctx context.Context, event *ActivityExecutionEvent) {
	for _, callback := range c.callbacks {
		callback.AfterActivityExecution(ctx, event)
	}
}
