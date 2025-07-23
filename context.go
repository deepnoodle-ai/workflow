package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/deepnoodle-ai/workflow/script"
)

var _ Context = &executionContext{}

// Context is a superset of of context.Context that provides access to workflow
// execution metadata and state.
type Context interface {

	// workflow.Context embeds the context.Context interface.
	context.Context

	// workflow.Context embeds the VariableContainer interface.
	VariableContainer

	// ListInputs returns a slice containing all input names.
	ListInputs() []string

	// GetInput returns the value of an input variable.
	GetInput(key string) (value any, exists bool)

	// GetLogger returns the logger.
	GetLogger() *slog.Logger

	// GetCompiler returns the script compiler.
	GetCompiler() script.Compiler

	// GetPathID returns the current execution path ID.
	GetPathID() string

	// GetStepName returns the current step name.
	GetStepName() string
}

// executionContext implements the workflow.Context interface.
type executionContext struct {
	context.Context
	*PathLocalState
	logger   *slog.Logger
	compiler script.Compiler
	pathID   string
	stepName string
}

type ExecutionContextOptions struct {
	PathLocalState *PathLocalState
	Logger         *slog.Logger
	Compiler       script.Compiler
	PathID         string
	StepName       string
}

// NewContext creates a new workflow context with direct state access
func NewContext(ctx context.Context, opts ExecutionContextOptions) *executionContext {
	return &executionContext{
		Context:        ctx,
		PathLocalState: opts.PathLocalState,
		logger:         opts.Logger,
		compiler:       opts.Compiler,
		pathID:         opts.PathID,
		stepName:       opts.StepName,
	}
}

// GetLogger returns the logger for this workflow context
func (w *executionContext) GetLogger() *slog.Logger {
	return w.logger
}

// GetCompiler returns the script compiler for this workflow context
func (w *executionContext) GetCompiler() script.Compiler {
	return w.compiler
}

// GetPathID returns the current path ID
func (w *executionContext) GetPathID() string {
	return w.pathID
}

// GetStepName returns the current step name
func (w *executionContext) GetStepName() string {
	return w.stepName
}

// WithTimeout creates a new workflow context with a timeout.
func WithTimeout(parent Context, timeout time.Duration) (Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)

	// If parent is a workflow context, preserve its workflow-specific data
	if wc, ok := parent.(*executionContext); ok {
		return &executionContext{
			Context:        ctx,
			PathLocalState: wc.PathLocalState,
			logger:         wc.logger,
			compiler:       wc.compiler,
			pathID:         wc.pathID,
			stepName:       wc.stepName,
		}, cancel
	}

	// This shouldn't happen in normal workflow execution
	// Return a basic context that doesn't support workflow methods
	return &executionContext{Context: ctx}, cancel
}

// WithCancel creates a new workflow context with cancellation.
func WithCancel(parent Context) (Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)

	// If parent is a workflow context, preserve its workflow-specific data
	if wc, ok := parent.(*executionContext); ok {
		return &executionContext{
			Context:        ctx,
			PathLocalState: wc.PathLocalState,
			logger:         wc.logger,
			compiler:       wc.compiler,
			pathID:         wc.pathID,
			stepName:       wc.stepName,
		}, cancel
	}

	// This shouldn't happen in normal workflow execution
	// Return a basic context that doesn't support workflow methods
	return &executionContext{Context: ctx}, cancel
}

// VariablesFromContext returns a map of all variables in the context. This is
// a copy. Any changes made to this map will not persist.
func VariablesFromContext(ctx Context) map[string]any {
	keys := ctx.ListVariables()
	variables := make(map[string]any, len(keys))
	for _, key := range keys {
		var found bool
		variables[key], found = ctx.GetVariable(key)
		if !found { // Should never happen
			panic(fmt.Errorf("variable %s not found in context", key))
		}
	}
	return variables
}

// InputsFromContext returns a map of all inputs in the context. This is a copy.
// Any changes made to this map will not persist.
func InputsFromContext(ctx Context) map[string]any {
	keys := ctx.ListInputs()
	inputs := make(map[string]any, len(keys))
	for _, key := range keys {
		var found bool
		inputs[key], found = ctx.GetInput(key)
		if !found { // Should never happen
			panic(fmt.Errorf("input %s not found in context", key))
		}
	}
	return inputs
}
