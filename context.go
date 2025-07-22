package workflow

import (
	"context"
	"log/slog"
	"time"

	"github.com/deepnoodle-ai/workflow/script"
	"github.com/deepnoodle-ai/workflow/state"
)

// WorkflowContext is the enhanced context interface that provides direct access
// to path state and workflow utilities. This follows Temporal's pattern of 
// extending context.Context with workflow-specific capabilities.
type WorkflowContext interface {
	context.Context

	// Direct state access methods - easier path-local variable management
	GetVariable(key string) (value any, exists bool)
	SetVariable(key string, value any)
	DeleteVariable(key string)
	GetAllVariables() map[string]any

	// Input access methods - read-only access to workflow inputs
	GetInput(key string) (value any, exists bool)
	GetAllInputs() map[string]any

	// Workflow utilities with direct access
	GetLogger() *slog.Logger
	GetCompiler() script.Compiler
	
	// Path information
	GetPathID() string
	GetStepName() string
}

// workflowContext implements the WorkflowContext interface
type workflowContext struct {
	context.Context
	pathState  *PathLocalState
	logger     *slog.Logger
	compiler   script.Compiler
	pathID     string
	stepName   string
}

// NewWorkflowContext creates a new workflow context with direct state access
func NewWorkflowContext(ctx context.Context, pathState *PathLocalState, logger *slog.Logger, compiler script.Compiler, pathID, stepName string) WorkflowContext {
	return &workflowContext{
		Context:   ctx,
		pathState: pathState,
		logger:    logger,
		compiler:  compiler,
		pathID:    pathID,
		stepName:  stepName,
	}
}

// GetVariable retrieves a variable value from the path-local state
func (w *workflowContext) GetVariable(key string) (any, bool) {
	variables := w.pathState.GetVariables()
	value, exists := variables[key]
	return value, exists
}

// SetVariable sets a variable value in the path-local state
func (w *workflowContext) SetVariable(key string, value any) {
	w.pathState.SetVariable(key, value)
}

// DeleteVariable removes a variable from the path-local state
func (w *workflowContext) DeleteVariable(key string) {
	w.pathState.DeleteVariable(key)
}

// GetAllVariables returns a copy of all variables in the path-local state
func (w *workflowContext) GetAllVariables() map[string]any {
	return w.pathState.GetVariables()
}

// GetInput retrieves an input value from the workflow inputs
func (w *workflowContext) GetInput(key string) (any, bool) {
	inputs := w.pathState.GetInputs()
	value, exists := inputs[key]
	return value, exists
}

// GetAllInputs returns a copy of all workflow inputs
func (w *workflowContext) GetAllInputs() map[string]any {
	return w.pathState.GetInputs()
}

// GetLogger returns the logger for this workflow context
func (w *workflowContext) GetLogger() *slog.Logger {
	return w.logger
}

// GetCompiler returns the script compiler for this workflow context
func (w *workflowContext) GetCompiler() script.Compiler {
	return w.compiler
}

// GetPathID returns the current path ID
func (w *workflowContext) GetPathID() string {
	return w.pathID
}

// GetStepName returns the current step name
func (w *workflowContext) GetStepName() string {
	return w.stepName
}

// WorkflowWithTimeout creates a new workflow context with a timeout, preserving workflow-specific data
func WorkflowWithTimeout(parent WorkflowContext, timeout time.Duration) (WorkflowContext, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	
	// If parent is a workflow context, preserve its workflow-specific data
	if wc, ok := parent.(*workflowContext); ok {
		return &workflowContext{
			Context:   ctx,
			pathState: wc.pathState,
			logger:    wc.logger,
			compiler:  wc.compiler,
			pathID:    wc.pathID,
			stepName:  wc.stepName,
		}, cancel
	}
	
	// This shouldn't happen in normal workflow execution
	// Return a basic context that doesn't support workflow methods
	return &workflowContext{Context: ctx}, cancel
}

// WorkflowWithCancel creates a new workflow context with cancellation, preserving workflow-specific data
func WorkflowWithCancel(parent WorkflowContext) (WorkflowContext, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	
	// If parent is a workflow context, preserve its workflow-specific data
	if wc, ok := parent.(*workflowContext); ok {
		return &workflowContext{
			Context:   ctx,
			pathState: wc.pathState,
			logger:    wc.logger,
			compiler:  wc.compiler,
			pathID:    wc.pathID,
			stepName:  wc.stepName,
		}, cancel
	}
	
	// This shouldn't happen in normal workflow execution
	// Return a basic context that doesn't support workflow methods
	return &workflowContext{Context: ctx}, cancel
}

// Legacy context key approach - kept for backward compatibility
type ContextKey string

const (
	LoggerContextKey   ContextKey = "logger"
	StateContextKey    ContextKey = "state"
	CompilerContextKey ContextKey = "compiler"
)

func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, LoggerContextKey, logger)
}

func WithState(ctx context.Context, state state.State) context.Context {
	return context.WithValue(ctx, StateContextKey, state)
}

func WithCompiler(ctx context.Context, compiler script.Compiler) context.Context {
	return context.WithValue(ctx, CompilerContextKey, compiler)
}

func GetLoggerFromContext(ctx context.Context) (*slog.Logger, bool) {
	logger, ok := ctx.Value(LoggerContextKey).(*slog.Logger)
	return logger, ok
}

func GetStateFromContext(ctx context.Context) (state.State, bool) {
	state, ok := ctx.Value(StateContextKey).(state.State)
	return state, ok
}

func GetCompilerFromContext(ctx context.Context) (script.Compiler, bool) {
	compiler, ok := ctx.Value(CompilerContextKey).(script.Compiler)
	return compiler, ok
}
