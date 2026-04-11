package workflow

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/deepnoodle-ai/workflow/script"
)

var _ Context = &executionContext{}

// Context is the activity-facing extension of context.Context. It
// exposes the workflow inputs, the branch-local state map, progress
// and signal plumbing, and the small identity values an activity may
// want to log.
//
// The interface is idiomatic Go: property-style accessors (Logger,
// BranchID, StepName) rather than GetLogger/GetBranchID, and the
// variable store methods are the ones you'd expect on a map.
//
// All methods are safe for concurrent use.
type Context interface {
	context.Context

	// Inputs returns a read-only view over the workflow inputs for
	// this branch.
	Inputs() Inputs

	// Set writes a branch-local variable.
	Set(key string, value any)
	// Get returns a branch-local variable and whether it was present.
	Get(key string) (value any, exists bool)
	// Delete removes a branch-local variable.
	Delete(key string)
	// Keys returns the names of all branch-local variables in sorted
	// order.
	Keys() []string

	// Logger returns the slog.Logger configured on the execution,
	// scoped to this execution and branch.
	Logger() *slog.Logger
	// Compiler returns the script.Compiler configured on the
	// execution.
	Compiler() script.Compiler
	// BranchID returns the ID of the running branch.
	BranchID() string
	// StepName returns the name of the currently executing step.
	StepName() string

	// Wait durably parks the current branch until a signal is
	// delivered to topic, or until timeout elapses.
	//
	// # Behavior
	//
	// On the first invocation in a step, Wait registers a wait
	// state against the branch's checkpoint and returns a sentinel
	// error (waitUnwind) that the engine catches at the activity
	// boundary. The activity goroutine exits, the orchestrator
	// persists a checkpoint with BranchState.Wait set, and the
	// execution ends with Status=ExecutionStatusSuspended.
	//
	// When the consumer delivers a signal to the topic via the
	// execution's SignalStore and calls Resume, the engine re-enters
	// the same activity. On the second invocation, Wait sees the
	// delivered payload and returns it to the caller as the first
	// return value.
	//
	// # Replay safety
	//
	// Activities that call Wait MUST be replay-safe: any side
	// effect that runs before the Wait call will run again on the
	// resumed invocation. Use Context.History to memoize expensive
	// or non-idempotent work. The shape is:
	//
	//	func(ctx Context, p Params) (any, error) {
	//	    result, _ := ctx.History().RecordOrReplay("key", func() (any, error) {
	//	        // side-effecting work runs once, replays from cache
	//	    })
	//	    payload, err := ctx.Wait("topic", timeout)
	//	    // post-wait code runs once after the signal arrives
	//	}
	//
	// # Deadline behavior
	//
	// timeout is wall-clock and starts ticking from when Wait is
	// first called; the engine records an absolute deadline at
	// register time. On resume, the engine recomputes the remaining
	// time against the original deadline; a wait that has already
	// expired wakes immediately with ErrWaitTimeout.
	//
	// Routing a timeout to a step (instead of failing the activity)
	// requires a declarative WaitSignalConfig with OnTimeout set;
	// the imperative Context.Wait always surfaces ErrWaitTimeout to
	// the caller.
	//
	// # Custom Context implementations
	//
	// Test doubles or alternative Context implementations MUST
	// forward Wait to the engine's underlying implementation. Wait
	// depends on engine-internal branch state — the checkpoint, the
	// wait registry, the resume path — and a custom Context that
	// returns nil/error from Wait without touching that state will
	// silently break replay semantics. The workflowtest package is
	// the supported pattern for unit tests.
	Wait(topic string, timeout time.Duration) (any, error)
	// History returns the per-activity-invocation persisted cache.
	// Returns a process-local, non-persistent cache if called outside
	// of an activity invocation.
	History() *History
	// ReportProgress forwards a progress update to the configured
	// StepProgressStore, if any. No-op otherwise.
	ReportProgress(detail ProgressDetail)
}

// Inputs is a read-only view over workflow input values. It exists as
// a named type so we can grow it with typed accessors (GetString,
// GetInt) in the future without breaking the Context interface.
type Inputs struct {
	m map[string]any
}

// newInputs builds an Inputs from a snapshot map. The map is not
// copied — callers that need mutation safety must pass a copy.
func newInputs(m map[string]any) Inputs {
	return Inputs{m: m}
}

// NewInputsForTest builds an Inputs from a snapshot map for use by
// test helpers (e.g. workflowtest.FakeContext). The map is taken by
// reference; callers that need mutation safety must pass a copy.
func NewInputsForTest(m map[string]any) Inputs {
	return Inputs{m: m}
}

// Get returns the value of an input and whether it was present.
func (i Inputs) Get(key string) (any, bool) {
	v, ok := i.m[key]
	return v, ok
}

// Keys returns the input names in sorted order.
func (i Inputs) Keys() []string {
	keys := make([]string, 0, len(i.m))
	for k := range i.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Len returns the number of inputs.
func (i Inputs) Len() int { return len(i.m) }

// ToMap returns a copy of the inputs as a plain map. Mutating the
// returned map does not affect the underlying execution state.
func (i Inputs) ToMap() map[string]any {
	out := make(map[string]any, len(i.m))
	for k, v := range i.m {
		out[k] = v
	}
	return out
}

// executionContext implements the workflow.Context interface.
type executionContext struct {
	context.Context
	*BranchLocalState
	logger           *slog.Logger
	compiler         script.Compiler
	branchID         string
	stepName         string
	executionID      string
	signalStore      SignalStore
	pendingWait      *WaitState
	history          *History
	progressReporter func(detail ProgressDetail) // nil when no store is configured
}

type ExecutionContextOptions struct {
	BranchLocalState *BranchLocalState
	Logger           *slog.Logger
	Compiler         script.Compiler
	BranchID         string
	StepName         string
	ExecutionID      string
	SignalStore      SignalStore
	// PendingWait is the wait state the branch was parked on before the
	// current activity invocation, if any. Set by the engine when a
	// checkpoint is being replayed so workflow.Wait can reuse the
	// original deadline.
	PendingWait *WaitState
	// ActivityHistory is the per-activity-invocation persisted cache
	// for this step. Non-nil only when the engine is running an
	// activity; nil for handler contexts that don't execute activity
	// code.
	ActivityHistory *History
}

// NewContext creates a new workflow context with direct state access.
func NewContext(ctx context.Context, opts ExecutionContextOptions) *executionContext {
	return &executionContext{
		Context:          ctx,
		BranchLocalState: opts.BranchLocalState,
		logger:           opts.Logger,
		compiler:         opts.Compiler,
		branchID:         opts.BranchID,
		stepName:         opts.StepName,
		executionID:      opts.ExecutionID,
		signalStore:      opts.SignalStore,
		pendingWait:      opts.PendingWait,
		history:          opts.ActivityHistory,
	}
}

// Inputs returns a read-only view over this branch's inputs.
func (w *executionContext) Inputs() Inputs {
	if w.BranchLocalState == nil {
		return Inputs{}
	}
	return newInputs(w.BranchLocalState.inputsSnapshot())
}

// Logger returns the logger for this workflow context.
func (w *executionContext) Logger() *slog.Logger { return w.logger }

// Compiler returns the script compiler for this workflow context.
func (w *executionContext) Compiler() script.Compiler { return w.compiler }

// BranchID returns the current branch ID.
func (w *executionContext) BranchID() string { return w.branchID }

// StepName returns the current step name.
func (w *executionContext) StepName() string { return w.stepName }

// ReportProgress forwards the progress detail to the configured
// StepProgressStore, if any.
func (w *executionContext) ReportProgress(detail ProgressDetail) {
	if w.progressReporter != nil {
		w.progressReporter(detail)
	}
}

// History returns the per-activity-invocation persisted cache for
// this step. If the context was not constructed with one (e.g. it
// belongs to a handler, not an activity), a process-local,
// non-persistent cache is returned so callers never need a nil check.
func (w *executionContext) History() *History {
	if w.history == nil {
		return newHistory(nil, nil)
	}
	return w.history
}

// internal accessors for the signal and wait subsystems. They are not
// part of the exported Context interface but let wait.go reach the
// plumbing without re-opening the struct.
func (w *executionContext) signalStoreInternal() SignalStore { return w.signalStore }
func (w *executionContext) executionIDInternal() string      { return w.executionID }
func (w *executionContext) pendingWaitInternal() *WaitState  { return w.pendingWait }

// WithTimeout creates a new workflow context with a timeout.
func WithTimeout(parent Context, timeout time.Duration) (Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)

	if wc, ok := parent.(*executionContext); ok {
		return &executionContext{
			Context:          ctx,
			BranchLocalState: wc.BranchLocalState,
			logger:           wc.logger,
			compiler:         wc.compiler,
			branchID:         wc.branchID,
			stepName:         wc.stepName,
			executionID:      wc.executionID,
			signalStore:      wc.signalStore,
			pendingWait:      wc.pendingWait,
			history:          wc.history,
			progressReporter: wc.progressReporter,
		}, cancel
	}

	return &executionContext{Context: ctx}, cancel
}

// WithCancel creates a new workflow context with cancellation.
func WithCancel(parent Context) (Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)

	if wc, ok := parent.(*executionContext); ok {
		return &executionContext{
			Context:          ctx,
			BranchLocalState: wc.BranchLocalState,
			logger:           wc.logger,
			compiler:         wc.compiler,
			branchID:         wc.branchID,
			stepName:         wc.stepName,
			executionID:      wc.executionID,
			signalStore:      wc.signalStore,
			pendingWait:      wc.pendingWait,
			history:          wc.history,
			progressReporter: wc.progressReporter,
		}, cancel
	}

	return &executionContext{Context: ctx}, cancel
}
