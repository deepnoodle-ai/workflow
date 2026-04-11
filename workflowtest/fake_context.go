package workflowtest

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/script"
)

// FakeContextOptions configures a FakeContext. Every field is
// optional; any field left zero gets a sensible default so test code
// can construct a FakeContext with {} and still have a usable Context.
type FakeContextOptions struct {
	// Inputs is the initial workflow inputs.
	Inputs map[string]any
	// Variables is the initial branch-local state.
	Variables map[string]any
	// Logger overrides the default discard logger.
	Logger *slog.Logger
	// Compiler overrides the default compiler. When nil, callers of
	// Compiler() receive nil — tests that exercise template
	// evaluation should always set this.
	Compiler script.Compiler
	// BranchID is the value returned by Context.BranchID. Defaults
	// to "fake-branch".
	BranchID string
	// StepName is the value returned by Context.StepName. Defaults
	// to "fake-step".
	StepName string
	// WaitFunc, when non-nil, is invoked by Context.Wait. Tests that
	// exercise wait semantics supply their own function; otherwise
	// Wait returns (nil, nil).
	WaitFunc func(topic string, timeout time.Duration) (any, error)
	// OnProgress, when non-nil, receives every ProgressDetail passed
	// to Context.ReportProgress. Tests can inspect the slice to
	// verify their activity reported progress correctly.
	OnProgress func(detail workflow.ProgressDetail)
}

// FakeContext is a workflow.Context implementation for consumer tests
// that want to unit-test activity code without constructing a full
// Execution. It is concurrent-safe and exposes the same surface as
// the engine-backed context.
type FakeContext struct {
	ctx        context.Context
	mu         sync.RWMutex
	inputs     map[string]any
	variables  map[string]any
	logger     *slog.Logger
	compiler   script.Compiler
	branchID   string
	stepName   string
	history    *workflow.History
	waitFunc   func(topic string, timeout time.Duration) (any, error)
	onProgress func(detail workflow.ProgressDetail)
}

// NewFakeContext builds a FakeContext from the given options. The
// returned FakeContext satisfies workflow.Context.
func NewFakeContext(opts FakeContextOptions) *FakeContext {
	inputs := make(map[string]any, len(opts.Inputs))
	for k, v := range opts.Inputs {
		inputs[k] = v
	}
	vars := make(map[string]any, len(opts.Variables))
	for k, v := range opts.Variables {
		vars[k] = v
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	branchID := opts.BranchID
	if branchID == "" {
		branchID = "fake-branch"
	}
	stepName := opts.StepName
	if stepName == "" {
		stepName = "fake-step"
	}
	return &FakeContext{
		ctx:        context.Background(),
		inputs:     inputs,
		variables:  vars,
		logger:     logger,
		compiler:   opts.Compiler,
		branchID:   branchID,
		stepName:   stepName,
		history:    workflow.NewHistoryForTest(),
		waitFunc:   opts.WaitFunc,
		onProgress: opts.OnProgress,
	}
}

// ---- context.Context ----

func (f *FakeContext) Deadline() (time.Time, bool) { return f.ctx.Deadline() }
func (f *FakeContext) Done() <-chan struct{}       { return f.ctx.Done() }
func (f *FakeContext) Err() error                  { return f.ctx.Err() }
func (f *FakeContext) Value(key any) any           { return f.ctx.Value(key) }

// ---- workflow.Context ----

func (f *FakeContext) Inputs() workflow.Inputs {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]any, len(f.inputs))
	for k, v := range f.inputs {
		out[k] = v
	}
	return workflow.NewInputsForTest(out)
}

func (f *FakeContext) Set(key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.variables[key] = value
}

func (f *FakeContext) Get(key string) (any, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	v, ok := f.variables[key]
	return v, ok
}

func (f *FakeContext) Delete(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.variables, key)
}

func (f *FakeContext) Keys() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	keys := make([]string, 0, len(f.variables))
	for k := range f.variables {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (f *FakeContext) Logger() *slog.Logger       { return f.logger }
func (f *FakeContext) Compiler() script.Compiler  { return f.compiler }
func (f *FakeContext) BranchID() string           { return f.branchID }
func (f *FakeContext) StepName() string           { return f.stepName }
func (f *FakeContext) History() *workflow.History { return f.history }

func (f *FakeContext) Wait(topic string, timeout time.Duration) (any, error) {
	if f.waitFunc != nil {
		return f.waitFunc(topic, timeout)
	}
	return nil, nil
}

func (f *FakeContext) ReportProgress(detail workflow.ProgressDetail) {
	if f.onProgress != nil {
		f.onProgress(detail)
	}
}

// SetContext replaces the underlying context.Context. Useful for
// tests that need to exercise cancellation or deadlines.
func (f *FakeContext) SetContext(ctx context.Context) { f.ctx = ctx }
