package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync/atomic"
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

	// GetExecutionID returns the current execution ID.
	GetExecutionID() string

	// Clock returns the clock for this context. Used for timer operations.
	// Default is RealClock. Use FakeClock in tests.
	Clock() Clock

	// Now returns the current time from the context's clock.
	// Prefer this over time.Now() in workflow code for testability.
	Now() time.Time

	// DeterministicID generates a deterministic ID based on execution ID,
	// path ID, and step name. Safe to use across recovery.
	// Prefer this over uuid.New() in workflow code.
	DeterministicID(prefix string) string

	// Rand returns a deterministic random source seeded from the execution ID.
	// Prefer this over rand.* in workflow code for reproducibility.
	Rand() *rand.Rand
}

// executionContext implements the workflow.Context interface.
type executionContext struct {
	context.Context
	*PathLocalState
	logger      *slog.Logger
	compiler    script.Compiler
	pathID      string
	stepName    string
	executionID string
	clock       Clock
	idCounter   atomic.Uint64
	randSource  *rand.Rand
}

type ExecutionContextOptions struct {
	PathLocalState *PathLocalState
	Logger         *slog.Logger
	Compiler       script.Compiler
	PathID         string
	StepName       string
	ExecutionID    string
	Clock          Clock
}

// NewContext creates a new workflow context with direct state access
func NewContext(ctx context.Context, opts ExecutionContextOptions) *executionContext {
	clock := opts.Clock
	if clock == nil {
		clock = NewRealClock()
	}
	return &executionContext{
		Context:        ctx,
		PathLocalState: opts.PathLocalState,
		logger:         opts.Logger,
		compiler:       opts.Compiler,
		pathID:         opts.PathID,
		stepName:       opts.StepName,
		executionID:    opts.ExecutionID,
		clock:          clock,
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

// GetExecutionID returns the current execution ID
func (w *executionContext) GetExecutionID() string {
	return w.executionID
}

// Clock returns the clock for this context
func (w *executionContext) Clock() Clock {
	if w.clock == nil {
		return NewRealClock()
	}
	return w.clock
}

// Now returns the current time from the context's clock.
func (w *executionContext) Now() time.Time {
	return w.Clock().Now()
}

// DeterministicID generates a deterministic ID based on execution ID,
// path ID, and step name. Safe to use across recovery.
func (w *executionContext) DeterministicID(prefix string) string {
	// Hash execution ID + path ID + step name + counter
	h := sha256.New()
	h.Write([]byte(w.executionID))
	h.Write([]byte(w.pathID))
	h.Write([]byte(w.stepName))

	counter := w.idCounter.Add(1)
	if err := binary.Write(h, binary.BigEndian, counter); err != nil {
		// This should never fail for sha256.Hash
		panic(err)
	}

	hash := h.Sum(nil)
	encoded := base32.StdEncoding.EncodeToString(hash[:10])
	return fmt.Sprintf("%s_%s", prefix, strings.ToLower(encoded))
}

// Rand returns a deterministic random source seeded from the execution ID.
func (w *executionContext) Rand() *rand.Rand {
	if w.randSource == nil {
		// Seed from execution ID for deterministic sequence
		h := sha256.Sum256([]byte(w.executionID + w.pathID))
		seed := int64(binary.BigEndian.Uint64(h[:8]))
		w.randSource = rand.New(rand.NewSource(seed))
	}
	return w.randSource
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
			executionID:    wc.executionID,
			clock:          wc.clock,
		}, cancel
	}

	// This shouldn't happen in normal workflow execution
	// Return a basic context that doesn't support workflow methods
	return &executionContext{Context: ctx, clock: NewRealClock()}, cancel
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
			executionID:    wc.executionID,
			clock:          wc.clock,
		}, cancel
	}

	// This shouldn't happen in normal workflow execution
	// Return a basic context that doesn't support workflow methods
	return &executionContext{Context: ctx, clock: NewRealClock()}, cancel
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
