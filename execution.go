package workflow

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow/script"
)

// NewExecutionID returns a new opaque ID suitable for identifying an
// execution. Format: "exec_" followed by 16 bytes of base32-encoded
// entropy (26 chars, lowercased, no padding).
func NewExecutionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("workflow: reading entropy for execution ID: %w", err))
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return "exec_" + strings.ToLower(enc)
}

// ExecutionStatus represents the execution status
type ExecutionStatus string

const (
	ExecutionStatusPending ExecutionStatus = "pending"
	ExecutionStatusRunning ExecutionStatus = "running"
	// ExecutionStatusWaiting is for branches that are blocked mid-run on a
	// join — their goroutine is parked on an in-process channel and the
	// execution is still live. Waiting is not a terminal state.
	ExecutionStatusWaiting ExecutionStatus = "waiting"
	// ExecutionStatusSuspended is for branches hard-suspended on a durable
	// wait (signal-wait, durable sleep). Their goroutine has exited and
	// they only live in the checkpoint. The execution cannot make
	// progress without external input (signal, wall-clock). When all
	// active branches are suspended, the execution loop exits and the
	// execution's final status is Suspended.
	ExecutionStatusSuspended ExecutionStatus = "suspended"
	// ExecutionStatusPaused is for branches parked by an explicit pause —
	// either an external PauseBranch call or a declarative Pause step.
	// Unlike Suspended, a paused branch has no declared resumption
	// condition; an external actor must clear the flag via UnpauseBranch
	// before the branch can continue. Paused is reported independently
	// from Suspended in SuspensionInfo so operators can distinguish the
	// two when deciding what action to take.
	ExecutionStatusPaused    ExecutionStatus = "paused"
	ExecutionStatusCompleted ExecutionStatus = "completed"
	ExecutionStatusFailed    ExecutionStatus = "failed"
)

// ExecutionOption is a functional option for NewExecution.
type ExecutionOption func(*executionConfig)

// executionConfig collects all optional parameters. It is an internal
// implementation detail; consumers compose it through With* options.
type executionConfig struct {
	inputs              map[string]any
	activityLogger      ActivityLogger
	checkpointer        Checkpointer
	logger              *slog.Logger
	executionID         string
	scriptCompiler      script.Compiler
	executionCallbacks  ExecutionCallbacks
	stepProgressStore   StepProgressStore
	signalStore         SignalStore
}

// WithInputs sets the workflow input values for this execution. Values
// not present here fall back to the Input.Default declared on the
// workflow. Extra keys not declared on the workflow are rejected.
func WithInputs(m map[string]any) ExecutionOption {
	return func(c *executionConfig) { c.inputs = m }
}

// WithCheckpointer configures where checkpoint snapshots are saved.
// Defaults to a null checkpointer that discards everything.
func WithCheckpointer(cp Checkpointer) ExecutionOption {
	return func(c *executionConfig) { c.checkpointer = cp }
}

// WithSignalStore configures the signal delivery rendezvous used by
// workflow.Wait and declarative WaitSignal steps. Required for any
// workflow that uses signals.
func WithSignalStore(ss SignalStore) ExecutionOption {
	return func(c *executionConfig) { c.signalStore = ss }
}

// WithLogger sets the structured logger. Defaults to a discard logger.
func WithLogger(l *slog.Logger) ExecutionOption {
	return func(c *executionConfig) { c.logger = l }
}

// WithExecutionID sets a fixed execution ID. When omitted, a new ID is
// generated via NewExecutionID. Use this when your orchestration layer
// (queue, DB) needs to know the ID before NewExecution is called.
func WithExecutionID(id string) ExecutionOption {
	return func(c *executionConfig) { c.executionID = id }
}

// WithExecutionCallbacks installs lifecycle callbacks. Defaults to no-op.
func WithExecutionCallbacks(cb ExecutionCallbacks) ExecutionOption {
	return func(c *executionConfig) { c.executionCallbacks = cb }
}

// WithStepProgressStore configures a store that receives progress
// updates as steps transition between states. Calls are async and
// store latency does not affect execution speed.
func WithStepProgressStore(s StepProgressStore) ExecutionOption {
	return func(c *executionConfig) { c.stepProgressStore = s }
}

// WithActivityLogger configures where per-activity invocation logs
// are written. Defaults to a null logger.
func WithActivityLogger(al ActivityLogger) ExecutionOption {
	return func(c *executionConfig) { c.activityLogger = al }
}

// WithScriptCompiler overrides the default script compiler used to
// evaluate parameter templates and edge conditions. Defaults to the
// built-in expr compiler.
func WithScriptCompiler(sc script.Compiler) ExecutionOption {
	return func(c *executionConfig) { c.scriptCompiler = sc }
}

// ExecuteOption configures a single call to Execution.Execute.
type ExecuteOption func(*executeConfig)

type executeConfig struct {
	priorExecutionID string
}

// ResumeFrom tells Execute to load the checkpoint for priorID and
// resume. If no checkpoint is found, Execute proceeds with a fresh
// run — the semantics of the deleted RunOrResume.
func ResumeFrom(priorID string) ExecuteOption {
	return func(c *executeConfig) { c.priorExecutionID = priorID }
}

// Execution represents a simplified workflow execution with checkpointing
type Execution struct {
	workflow *Workflow

	// Unified state management - replaces scattered fields
	state *executionState

	// Runtime branch tracking (not checkpointed). activeBranches is
	// written by the orchestrator goroutine (runBranches,
	// processBranchSnapshot, loadCheckpoint) and read by external
	// callers of PauseBranch/UnpauseBranch. All reads and writes go
	// through the activeBranchesMu-protected helpers defined below so
	// concurrent external pause calls cannot race with orchestrator
	// mutations.
	activeBranchesMu sync.Mutex
	activeBranches   map[string]*branch
	branchSnapshots chan branchSnapshot

	// Path options template (reused for all branches)
	branchOptions branchOptions

	// Infrastructure dependencies
	activityLogger     ActivityLogger
	compiler           script.Compiler
	checkpointer       Checkpointer
	activities         map[string]Activity
	executionCallbacks ExecutionCallbacks
	signalStore        SignalStore
	adapter            *executionAdapter

	logger *slog.Logger

	// Step progress tracking
	stepProgressTracker *stepProgressTracker

	// Single mutex for orchestration data
	mutex             sync.RWMutex
	doneWg            sync.WaitGroup
	started           bool
	ran               bool // true once run() begins; distinguishes start() reuse from run() failure
	checkpointCounter int
	// checkpointMu serialises saveCheckpoint calls so concurrent
	// writers (activity goroutines under executeActivity + the
	// orchestrator goroutine under processBranchSnapshot) cannot race
	// on checkpointCounter or the underlying Checkpointer. Distinct
	// from mutex to avoid interacting with the existing RWMutex
	// protocol around activeBranches and started/ran.
	checkpointMu sync.Mutex
}

// NewExecution creates a new execution for the given workflow and
// activity registry. Every configurable knob is a functional option.
func NewExecution(wf *Workflow, reg *ActivityRegistry, opts ...ExecutionOption) (*Execution, error) {
	if wf == nil {
		return nil, fmt.Errorf("workflow: workflow is required")
	}
	if reg == nil {
		return nil, fmt.Errorf("workflow: activity registry is required")
	}

	cfg := &executionConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.scriptCompiler == nil {
		cfg.scriptCompiler = DefaultScriptCompiler()
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.activityLogger == nil {
		cfg.activityLogger = NewNullActivityLogger()
	}
	if cfg.checkpointer == nil {
		cfg.checkpointer = NewNullCheckpointer()
	}
	if cfg.executionID == "" {
		cfg.executionID = NewExecutionID()
	}
	if cfg.executionCallbacks == nil {
		cfg.executionCallbacks = &BaseExecutionCallbacks{}
	}

	// Binding-level validation: activity references, templates,
	// expressions, and store-path shape. Runs after defaults are
	// applied so the compiler and logger are always present.
	if err := wf.validateBinding(reg, cfg.scriptCompiler, cfg.signalStore != nil, cfg.logger); err != nil {
		return nil, err
	}

	// Determine input values from inputs map or defaults.
	inputs := make(map[string]any, len(cfg.inputs))
	for _, input := range wf.Inputs() {
		if v, ok := cfg.inputs[input.Name]; ok {
			inputs[input.Name] = v
		} else {
			if input.Default == nil {
				return nil, fmt.Errorf("input %q is required", input.Name)
			}
			inputs[input.Name] = input.Default
		}
	}
	for k := range cfg.inputs {
		if _, ok := inputs[k]; !ok {
			return nil, fmt.Errorf("unknown input %q", k)
		}
	}

	activities := reg.asMap()
	state := newExecutionState(cfg.executionID, wf.Name(), inputs)

	execution := &Execution{
		workflow:           wf,
		state:              state,
		activityLogger:     cfg.activityLogger,
		checkpointer:       cfg.checkpointer,
		activeBranches:     map[string]*branch{},
		branchSnapshots:    make(chan branchSnapshot, 100),
		activities:         activities,
		logger:             cfg.logger.With("execution_id", cfg.executionID),
		compiler:           cfg.scriptCompiler,
		executionCallbacks: cfg.executionCallbacks,
		signalStore:        cfg.signalStore,
	}
	execution.adapter = &executionAdapter{execution: execution}

	// Wire step progress tracker if a store is configured.
	if cfg.stepProgressStore != nil {
		tracker := newStepProgressTracker(cfg.executionID, cfg.stepProgressStore, execution.logger)
		execution.stepProgressTracker = tracker
		chain := NewCallbackChain(execution.executionCallbacks, tracker)
		execution.executionCallbacks = chain
	}

	// Set up branch options template. ExecutionID is populated per-call in
	// createBranch* from e.state.ID() so that a resumed execution whose ID
	// was restored from a checkpoint sees the right value.
	execution.branchOptions = branchOptions{
		Workflow:         wf,
		ActivityRegistry: activities,
		Logger:           cfg.logger,
		Inputs:           copyMap(inputs),
		Variables:        copyMap(wf.InitialState()),
		activityExecutor: execution.adapter,
		UpdatesChannel:   execution.branchSnapshots,
		ScriptCompiler:   cfg.scriptCompiler,
		SignalStore:      cfg.signalStore,
	}

	return execution, nil
}

// --- activeBranches helpers (mutex-protected) ---

// addActiveBranch registers a running branch under activeBranchesMu.
func (e *Execution) addActiveBranch(branchID string, p *branch) {
	e.activeBranchesMu.Lock()
	defer e.activeBranchesMu.Unlock()
	e.activeBranches[branchID] = p
}

// removeActiveBranch removes a branch under activeBranchesMu.
func (e *Execution) removeActiveBranch(branchID string) {
	e.activeBranchesMu.Lock()
	defer e.activeBranchesMu.Unlock()
	delete(e.activeBranches, branchID)
}

// getActiveBranch looks up a running branch by ID under activeBranchesMu.
func (e *Execution) getActiveBranch(branchID string) (*branch, bool) {
	e.activeBranchesMu.Lock()
	defer e.activeBranchesMu.Unlock()
	p, ok := e.activeBranches[branchID]
	return p, ok
}

// activeBranchCount returns the number of running branches under
// activeBranchesMu. Used by the orchestrator loop condition and by
// callbacks that report branch counts.
func (e *Execution) activeBranchCount() int {
	e.activeBranchesMu.Lock()
	defer e.activeBranchesMu.Unlock()
	return len(e.activeBranches)
}

// activeBranchesSnapshot returns a slice of the current active branches
// suitable for iteration without holding activeBranchesMu. Used by the
// resume branch to hand off branches to runBranches.
func (e *Execution) activeBranchesSnapshot() []*branch {
	e.activeBranchesMu.Lock()
	defer e.activeBranchesMu.Unlock()
	out := make([]*branch, 0, len(e.activeBranches))
	for _, p := range e.activeBranches {
		out = append(out, p)
	}
	return out
}

// resetActiveBranches reinitialises the active branches map under
// activeBranchesMu. Used by loadCheckpoint.
func (e *Execution) resetActiveBranches() {
	e.activeBranchesMu.Lock()
	defer e.activeBranchesMu.Unlock()
	e.activeBranches = make(map[string]*branch)
}

// ID returns the execution ID
func (e *Execution) ID() string {
	return e.state.ID()
}

// Status returns the current execution status
func (e *Execution) Status() ExecutionStatus {
	return e.state.GetStatus()
}

// GetOutputs returns the current execution outputs
func (e *Execution) GetOutputs() map[string]any {
	return e.state.GetOutputs()
}

// saveCheckpoint saves the current execution state. Safe to call
// concurrently from the orchestrator goroutine and from activity
// goroutines; calls are serialised via checkpointMu so writers cannot
// race on the counter or the backing Checkpointer.
func (e *Execution) saveCheckpoint(ctx context.Context) error {
	e.checkpointMu.Lock()
	defer e.checkpointMu.Unlock()
	e.checkpointCounter++
	checkpoint := e.state.ToCheckpoint()
	checkpoint.ID = fmt.Sprintf("%d", e.checkpointCounter)
	return e.checkpointer.SaveCheckpoint(ctx, checkpoint)
}

// loadCheckpoint loads execution state from the latest checkpoint.
//
// The checkpoint's execution ID is preserved as the execution's identity.
// Callers that need to resume into a specific ID should pass it via
// ExecutionOptions.ExecutionID when constructing the execution; that ID must
// match the checkpoint's ID. Rotating the ID on resume would silently break
// SignalStore lookups keyed on (executionID, topic).
func (e *Execution) loadCheckpoint(ctx context.Context, priorExecutionID string) error {
	// Load state from checkpoint
	checkpoint, err := e.checkpointer.LoadCheckpoint(ctx, priorExecutionID)
	if err != nil {
		return fmt.Errorf("loading checkpoint: %w", err)
	}
	if checkpoint == nil {
		return fmt.Errorf("%w: execution %q", ErrNoCheckpoint, priorExecutionID)
	}
	e.state.FromCheckpoint(checkpoint)

	// Preserve the checkpoint's execution ID so signals keyed on
	// (executionID, topic) remain discoverable across resumes.

	lastStatus := e.state.GetStatus()

	// If the prior execution completed, there's nothing to do
	if lastStatus == ExecutionStatusCompleted {
		return nil
	}

	// Handle failed executions
	if lastStatus == ExecutionStatusFailed {
		// Reset failed branches for resumption
		if err := e.resetFailedBranches(); err != nil {
			return fmt.Errorf("failed to reset failed branches for resumption: %w", err)
		}

		originalErr := e.state.GetError()
		if originalErr != nil {
			e.logger.Info("resuming execution from failure", "original_error", originalErr.Error())
		}

		// Clear any previous error and reset status to running
		e.state.SetError(nil)
		e.state.SetStatus(ExecutionStatusRunning)
	}

	// Rebuild active branches for branches that should be running. Suspended
	// and Paused branches rejoin the run loop too: a suspended branch can
	// replay its activity and either consume a pending signal or
	// re-suspend; a paused branch immediately re-parks at its first
	// step boundary unless UnpauseBranch has cleared the flag prior
	// to the Resume call.
	branchStates := e.state.GetBranchStates()
	e.resetActiveBranches()
	for id, branchState := range branchStates {
		switch branchState.Status {
		case ExecutionStatusRunning, ExecutionStatusPending, ExecutionStatusWaiting, ExecutionStatusSuspended, ExecutionStatusPaused:
			currentStep, ok := e.workflow.GetStep(branchState.CurrentStep)
			if !ok {
				return fmt.Errorf("step %q not found in workflow for branch %s", branchState.CurrentStep, id)
			}
			// Restore branch with its stored variables from checkpoint
			e.addActiveBranch(id, e.createBranchWithVariables(id, currentStep, branchState.Variables))
		}
	}

	e.logger.Info("loaded execution from checkpoint",
		"status", e.state.GetStatus(),
		"branches", len(branchStates),
		"active_paths", e.activeBranchCount(),
		"path_counter", e.state.pathCounter)

	return nil
}

func (e *Execution) start() error {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	if e.started {
		return ErrAlreadyStarted
	}
	e.started = true
	return nil
}

// Execute runs the workflow and returns a structured ExecutionResult.
//
// By default, Execute starts a fresh run. Pass ResumeFrom(priorID) to
// resume from a previous execution's checkpoint. If ResumeFrom is set
// and no checkpoint exists for priorID, Execute proceeds with a fresh
// run.
//
// An error return means the execution could not be attempted
// (infrastructure failure). When error is nil, result is non-nil and
// contains the execution outcome — including workflow-level failures,
// which are represented in result.Error rather than the error return.
func (e *Execution) Execute(ctx context.Context, opts ...ExecuteOption) (*ExecutionResult, error) {
	cfg := &executeConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	err := e.runExecution(ctx, cfg.priorExecutionID)
	return e.buildResult(err)
}

// runExecution is the internal run-or-resume implementation.
func (e *Execution) runExecution(ctx context.Context, priorExecutionID string) error {
	e.ran = false

	if priorExecutionID != "" {
		err := e.resumeFromCheckpoint(ctx, priorExecutionID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrNoCheckpoint) {
			return err
		}
		// No checkpoint found — fall through to a fresh run.
	}

	if err := e.start(); err != nil {
		return err
	}
	return e.run(ctx)
}

// resumeFromCheckpoint loads the prior checkpoint, marks the execution
// as started, and runs it to completion. Returns ErrNoCheckpoint if no
// checkpoint exists for priorExecutionID.
func (e *Execution) resumeFromCheckpoint(ctx context.Context, priorExecutionID string) error {
	// Load checkpoint FIRST, before marking as started.
	// This way a failed load (e.g., no checkpoint) leaves the execution
	// object clean for a subsequent fresh run.
	if err := e.loadCheckpoint(ctx, priorExecutionID); err != nil {
		return err
	}

	// Return early if already completed.
	if e.state.GetStatus() == ExecutionStatusCompleted {
		e.logger.Info("execution already completed from checkpoint")
		e.mutex.Lock()
		e.started = true
		e.mutex.Unlock()
		return nil
	}

	if err := e.start(); err != nil {
		return err
	}
	return e.run(ctx)
}

func (e *Execution) buildResult(runErr error) (*ExecutionResult, error) {
	// If the execution was never started, this is an infrastructure error.
	if !e.started {
		return nil, runErr
	}

	// If start() succeeded on a prior call but run() never executed in this
	// call, the error is an infrastructure failure (e.g., "already started").
	if runErr != nil && !e.ran {
		return nil, runErr
	}

	result := &ExecutionResult{
		WorkflowName: e.workflow.Name(),
		Status:       e.state.GetStatus(),
		Outputs:      e.state.GetOutputs(),
		Timing: ExecutionTiming{
			StartedAt:  e.state.GetStartTime(),
			FinishedAt: e.state.GetEndTime(),
		},
	}

	// If execution returned an error but didn't reach a terminal state
	// (e.g., context canceled during run), classify it as failed.
	if runErr != nil && result.Status != ExecutionStatusCompleted && result.Status != ExecutionStatusFailed {
		result.Status = ExecutionStatusFailed
		result.Error = ClassifyError(runErr)
		if result.Timing.FinishedAt.IsZero() {
			result.Timing.FinishedAt = time.Now()
		}
	} else if result.Status == ExecutionStatusFailed && runErr != nil {
		result.Error = ClassifyError(runErr)
		if result.Timing.FinishedAt.IsZero() {
			result.Timing.FinishedAt = time.Now()
		}
	}

	// Populate SuspensionInfo for dormant terminations (hard-suspended
	// on a wait, or paused) so the consumer can schedule resume
	// without re-reading the checkpoint.
	if result.Status == ExecutionStatusSuspended || result.Status == ExecutionStatusPaused {
		result.Suspension = e.buildSuspensionInfo()
	}

	result.Timing.Duration = result.Timing.FinishedAt.Sub(result.Timing.StartedAt)

	return result, nil
}

// buildSuspensionInfo collects the suspension state of every hard-
// suspended or paused branch into a SuspensionInfo. Returns nil if no
// branches are in a dormant state.
//
// Dominant-reason precedence when multiple branches are dormant for
// different reasons: Paused > Sleeping > WaitingSignal. Operators care
// most about "someone has to unpause this"; wall-clock wakeups are
// next; signal waits are the most passive.
func (e *Execution) buildSuspensionInfo() *SuspensionInfo {
	branchStates := e.state.GetBranchStates()
	info := &SuspensionInfo{}
	topicSet := map[string]struct{}{}
	reasonRank := map[SuspensionReason]int{
		SuspensionReasonWaitingSignal: 1,
		SuspensionReasonSleeping:      2,
		SuspensionReasonPaused:        3,
	}
	for _, ps := range branchStates {
		if ps.Status != ExecutionStatusSuspended && ps.Status != ExecutionStatusPaused {
			continue
		}
		sp := SuspendedBranch{
			BranchID:   ps.ID,
			StepName: ps.CurrentStep,
		}
		switch ps.Status {
		case ExecutionStatusPaused:
			sp.Reason = SuspensionReasonPaused
			sp.PauseReason = ps.PauseReason
		case ExecutionStatusSuspended:
			if ps.Wait != nil {
				switch ps.Wait.Kind {
				case WaitKindSignal:
					sp.Reason = SuspensionReasonWaitingSignal
					sp.Topic = ps.Wait.Topic
					if ps.Wait.Topic != "" {
						topicSet[ps.Wait.Topic] = struct{}{}
					}
				case WaitKindSleep:
					sp.Reason = SuspensionReasonSleeping
				}
				sp.WakeAt = ps.Wait.WakeAt
				if !ps.Wait.WakeAt.IsZero() && (info.WakeAt.IsZero() || ps.Wait.WakeAt.Before(info.WakeAt)) {
					info.WakeAt = ps.Wait.WakeAt
				}
			}
		}
		if reasonRank[sp.Reason] > reasonRank[info.Reason] {
			info.Reason = sp.Reason
		}
		info.SuspendedBranches = append(info.SuspendedBranches, sp)
	}
	if len(info.SuspendedBranches) == 0 {
		return nil
	}
	for t := range topicSet {
		info.Topics = append(info.Topics, t)
	}
	return info
}

// run the workflow execution, blocking until completion or error
func (e *Execution) run(ctx context.Context) error {
	e.ran = true
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Set initial running status and start time
	e.state.SetStatus(ExecutionStatusRunning)
	if e.state.GetStartTime().IsZero() {
		e.state.SetTiming(time.Now(), time.Time{})
	}

	// Trigger workflow start callback
	e.executionCallbacks.BeforeWorkflowExecution(ctx, &WorkflowExecutionEvent{
		ExecutionID:  e.state.ID(),
		WorkflowName: e.workflow.Name(),
		Status:       e.state.GetStatus(),
		StartTime:    e.state.GetStartTime(),
		Inputs:       copyMap(e.state.GetInputs()),
		PathCount:    e.activeBranchCount(),
	})

	// Start execution branches
	if e.activeBranchCount() == 0 {
		// Starting fresh - create initial branch
		startStep := e.workflow.Start()
		e.runBranches(ctx, e.createBranch("main", startStep))
	} else {
		// Resuming from checkpoint - restart active branches
		resumingBranches := e.activeBranchesSnapshot()
		e.logger.Info("resuming execution from checkpoint", "active_paths", len(resumingBranches))
		for _, branch := range resumingBranches {
			e.runBranches(ctx, branch)
		}
	}

	// Process branch snapshots
	var executionErr error
	for e.activeBranchCount() > 0 && executionErr == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case snapshot := <-e.branchSnapshots:
			if err := e.processBranchSnapshot(ctx, snapshot); err != nil {
				executionErr = err
				cancel() // cancel any other branches
			}
		}
	}

	// Wait for all branches to complete
	e.doneWg.Wait()

	endTime := time.Now()
	duration := endTime.Sub(e.state.GetStartTime())

	// Check for failed branches
	failedIDs := e.state.GetFailedBranchIDs()

	// Check for branches hard-suspended on a durable wait (signal/sleep).
	suspendedIDs := e.state.GetSuspendedBranchIDs()

	// Check for branches paused by an explicit pause trigger.
	pausedIDs := e.state.GetPausedBranchIDs()

	// Update final status. Precedence: Failed > Paused > Suspended >
	// Completed. Paused outranks Suspended because a paused branch
	// requires explicit operator action to clear, while a suspended
	// branch has a declared resumption trigger (signal or wall-clock).
	//
	// An orchestrator-side error (e.g., a checkpoint save failure in
	// processBranchSnapshot) forces Failed regardless of branch-level
	// state — we must never silently drop an internal error by
	// reporting Paused/Suspended/Completed.
	finalErr := executionErr
	var finalStatus ExecutionStatus
	switch {
	case finalErr != nil && len(failedIDs) == 0:
		// Orchestrator-side failure with no per-branch failure recorded
		// (e.g., checkpoint save error). Classify as Failed.
		finalStatus = ExecutionStatusFailed
		e.logger.Error("execution failed",
			"error", finalErr,
			"paused_paths", pausedIDs,
			"suspended_paths", suspendedIDs)
	case len(failedIDs) > 0:
		finalStatus = ExecutionStatusFailed
		if finalErr == nil {
			finalErr = fmt.Errorf("execution failed: %v", failedIDs)
		}
		e.logger.Error("execution failed", "failed_paths", failedIDs, "error", finalErr)
	case len(pausedIDs) > 0:
		// Execution is dormant on an explicit pause. Do not extract
		// outputs, do not mark failed. Caller clears the pause via
		// UnpauseBranch and resumes.
		finalStatus = ExecutionStatusPaused
		e.logger.Info("execution paused",
			"paused_paths", pausedIDs,
			"suspended_paths", suspendedIDs,
			"duration", duration)
	case len(suspendedIDs) > 0:
		// Execution is dormant: one or more branches are parked on a durable
		// wait. Do not extract outputs, do not mark failed. Caller resumes
		// when an external trigger (signal, wall-clock) arrives.
		finalStatus = ExecutionStatusSuspended
		e.logger.Info("execution suspended",
			"suspended_paths", suspendedIDs,
			"duration", duration)
	default:
		finalStatus = ExecutionStatusCompleted
		// Extract workflow outputs from final branch variables
		if err := e.extractWorkflowOutputs(); err != nil {
			e.logger.Error("failed to extract workflow outputs", "error", err)
			finalErr = err
			finalStatus = ExecutionStatusFailed
		}
		e.logger.Info("execution completed",
			"outputs", e.state.GetOutputs(),
			"duration", duration)
	}
	e.state.SetFinished(finalStatus, time.Now(), finalErr)

	// Trigger workflow completion/failure callback
	e.executionCallbacks.AfterWorkflowExecution(ctx, &WorkflowExecutionEvent{
		ExecutionID:  e.state.ID(),
		WorkflowName: e.workflow.Name(),
		Status:       finalStatus,
		StartTime:    e.state.GetStartTime(),
		EndTime:      endTime,
		Duration:     duration,
		Inputs:       e.state.GetInputs(),
		Outputs:      e.state.GetOutputs(),
		PathCount:    len(e.state.GetBranchStates()),
		Error:        finalErr,
	})

	// Final checkpoint
	if checkpointErr := e.saveCheckpoint(ctx); checkpointErr != nil {
		e.logger.Error("failed to save final checkpoint", "error", checkpointErr)
	}

	return finalErr
}

// extractWorkflowOutputs extracts workflow outputs from final branch variables.
func (e *Execution) extractWorkflowOutputs() error {
	branchStates := e.state.GetBranchStates()
	outputs := e.workflow.Outputs()

	for _, outputDef := range outputs {
		outputName := outputDef.Name
		variableName := outputDef.Variable
		if variableName == "" {
			variableName = outputName
		}

		targetBranch := outputDef.Branch
		if targetBranch == "" {
			targetBranch = "main"
		}

		branchState, found := branchStates[targetBranch]
		if !found {
			return fmt.Errorf("output branch %q not found for output %q", targetBranch, outputName)
		}

		if value, exists := getNestedField(branchState.Variables, variableName); exists {
			e.state.SetOutput(outputName, value)
		} else {
			return fmt.Errorf("workflow output variable %q not found in branch %q", variableName, targetBranch)
		}
	}
	return nil
}

// runBranches begins executing one or more new execution branches in goroutines.
// It does not wait for the branches to complete.
func (e *Execution) runBranches(ctx context.Context, branches ...*branch) {
	for _, br := range branches {
		branchID := br.ID()
		e.addActiveBranch(branchID, br)
		startTime := time.Now()

		// Preserve prior BranchState fields (step outputs, pending Wait,
		// pause flag, activity history) when a resumed branch is being
		// restarted. A freshly-created branch has no prior state, so
		// this collapses to the initial set.
		existing := e.state.GetBranchStates()[branchID]
		var (
			stepOutputs         map[string]any
			pendingWait         *WaitState
			priorStart          time.Time
			pauseRequested      bool
			pauseReason         string
			activityHistory     map[string]any
			activityHistoryStep string
		)
		if existing != nil {
			stepOutputs = existing.StepOutputs
			pendingWait = existing.Wait
			priorStart = existing.StartTime
			pauseRequested = existing.PauseRequested
			pauseReason = existing.PauseReason
			activityHistory = existing.ActivityHistory
			activityHistoryStep = existing.ActivityHistoryStep
		}
		if stepOutputs == nil {
			stepOutputs = map[string]any{}
		}
		if priorStart.IsZero() {
			priorStart = startTime
		}

		e.state.SetBranchState(branchID, &BranchState{
			ID:                  branchID,
			Status:              ExecutionStatusRunning,
			CurrentStep:         br.CurrentStep().Name,
			StartTime:           priorStart,
			StepOutputs:         stepOutputs,
			Variables:           br.Variables(), // Store branch's current variables
			Wait:                pendingWait,
			PauseRequested:      pauseRequested,
			PauseReason:         pauseReason,
			ActivityHistory:     activityHistory,
			ActivityHistoryStep: activityHistoryStep,
		})

		// Trigger branch start callback
		e.executionCallbacks.BeforeBranchExecution(ctx, &BranchExecutionEvent{
			ExecutionID:  e.state.ID(),
			WorkflowName: e.workflow.Name(),
			BranchID:       branchID,
			Status:       ExecutionStatusRunning,
			StartTime:    startTime,
			CurrentStep:  br.CurrentStep().Name,
			StepOutputs:  map[string]any{},
		})

		e.doneWg.Add(1)
		go func(p *branch) {
			defer e.doneWg.Done()
			p.Run(ctx)
		}(br)
	}
}

func (e *Execution) processBranchSnapshot(ctx context.Context, snapshot branchSnapshot) error {
	if snapshot.Error != nil {
		e.state.UpdateBranchState(snapshot.BranchID, func(state *BranchState) {
			state.Status = ExecutionStatusFailed
			state.ErrorMessage = snapshot.Error.Error()
			state.EndTime = snapshot.EndTime
		})

		// Trigger branch failure callback
		duration := snapshot.EndTime.Sub(snapshot.StartTime)
		branchState := e.state.GetBranchStates()[snapshot.BranchID]
		e.executionCallbacks.AfterBranchExecution(ctx, &BranchExecutionEvent{
			ExecutionID:  e.state.ID(),
			WorkflowName: e.workflow.Name(),
			BranchID:       snapshot.BranchID,
			Status:       ExecutionStatusFailed,
			StartTime:    snapshot.StartTime,
			EndTime:      snapshot.EndTime,
			Duration:     duration,
			CurrentStep:  snapshot.StepName,
			StepOutputs:  copyMap(branchState.StepOutputs),
			Error:        snapshot.Error,
		})
		return snapshot.Error
	}

	// Handle join requests
	if snapshot.joinRequest != nil {
		return e.processJoinRequest(ctx, snapshot)
	}

	// Handle wait requests: branch parking on a durable wait (signal/sleep).
	if snapshot.waitRequest != nil {
		e.state.UpdateBranchState(snapshot.BranchID, func(state *BranchState) {
			state.Status = ExecutionStatusSuspended
			state.CurrentStep = snapshot.waitRequest.StepName
			state.Wait = snapshot.waitRequest.Wait
			state.EndTime = snapshot.EndTime
			if activeBranch, exists := e.getActiveBranch(snapshot.BranchID); exists {
				state.Variables = activeBranch.Variables()
			}
		})
		// Hard-suspend: remove from active branches so the run loop exits
		// once no running branches remain.
		e.removeActiveBranch(snapshot.BranchID)
		// Checkpoint the parked state synchronously so resume can find it.
		if err := e.saveCheckpoint(ctx); err != nil {
			e.logger.Error("failed to save wait checkpoint", "error", err)
			return err
		}
		return nil
	}

	// Handle pause requests: branch parking due to a pause trigger
	// (external PauseBranch or declarative Pause step). The branch's
	// pause flag stays set across the checkpoint so a subsequent
	// Resume re-parks the branch until UnpauseBranch clears it.
	if snapshot.pauseRequest != nil {
		e.state.UpdateBranchState(snapshot.BranchID, func(state *BranchState) {
			state.Status = ExecutionStatusPaused
			state.CurrentStep = snapshot.pauseRequest.StepName
			state.PauseRequested = true
			state.PauseReason = snapshot.pauseRequest.Reason
			state.EndTime = snapshot.EndTime
			if activeBranch, exists := e.getActiveBranch(snapshot.BranchID); exists {
				state.Variables = activeBranch.Variables()
			}
		})
		e.removeActiveBranch(snapshot.BranchID)
		if err := e.saveCheckpoint(ctx); err != nil {
			e.logger.Error("failed to save pause checkpoint", "error", err)
			return err
		}
		return nil
	}

	// Store step output and update status
	e.state.UpdateBranchState(snapshot.BranchID, func(state *BranchState) {
		state.StepOutputs[snapshot.StepName] = snapshot.StepOutput
		state.Status = snapshot.Status
		if snapshot.Status == ExecutionStatusCompleted {
			state.EndTime = snapshot.EndTime
		}
		// Advancing past a wait clears any pending wait state on the branch.
		state.Wait = nil
		// Advancing past a step clears the activity history — no
		// cross-step leakage per FR-16. The step-name scope check in
		// executeActivity is the primary correctness guarantee; this
		// clear keeps checkpoints from accumulating stale history.
		state.ActivityHistory = nil
		state.ActivityHistoryStep = ""

		// Update branch variables from the active branch (if it still exists)
		if activeBranch, exists := e.getActiveBranch(snapshot.BranchID); exists {
			state.Variables = activeBranch.Variables()
		}
	})

	// Remove completed or failed branches, but keep waiting branches
	isCompleted := snapshot.Status == ExecutionStatusCompleted || snapshot.Status == ExecutionStatusFailed

	if isCompleted {
		e.removeActiveBranch(snapshot.BranchID)

		// When a branch completes, check if any joins can now proceed
		if snapshot.Status == ExecutionStatusCompleted {
			if err := e.checkAndResumeJoins(ctx); err != nil {
				return err
			}
		}

		// Trigger branch completion callback for successful completion
		if snapshot.Status == ExecutionStatusCompleted {
			duration := snapshot.EndTime.Sub(snapshot.StartTime)
			branchState := e.state.GetBranchStates()[snapshot.BranchID]
			e.executionCallbacks.AfterBranchExecution(ctx, &BranchExecutionEvent{
				ExecutionID:  e.state.ID(),
				WorkflowName: e.workflow.Name(),
				BranchID:       snapshot.BranchID,
				Status:       ExecutionStatusCompleted,
				StartTime:    snapshot.StartTime,
				EndTime:      snapshot.EndTime,
				Duration:     duration,
				CurrentStep:  snapshot.StepName,
				StepOutputs:  copyMap(branchState.StepOutputs),
			})
		}
	}

	// Create and execute new branches from branching
	if len(snapshot.NewBranches) > 0 {
		newBranches := make([]*branch, 0, len(snapshot.NewBranches))
		for _, pathSpec := range snapshot.NewBranches {
			branchID, err := e.state.GenerateBranchID(snapshot.BranchID, pathSpec.Name)
			if err != nil {
				return fmt.Errorf("failed to generate branch ID: %w", err)
			}
			// Use the specific variables from the branch spec (copied from parent branch)
			newBranch := e.createBranchWithVariables(branchID, pathSpec.Step, pathSpec.Variables)
			newBranches = append(newBranches, newBranch)
		}
		e.runBranches(ctx, newBranches...)
	}

	e.logger.Debug("branch snapshot processed",
		"active_paths", e.activeBranchCount(),
		"completed_path", isCompleted,
		"new_paths", len(snapshot.NewBranches))

	return nil
}

// checkAndResumeJoins checks all active joins to see if they can now proceed
func (e *Execution) checkAndResumeJoins(ctx context.Context) error {
	allJoinStates := e.state.GetAllJoinStates()

	for stepName, joinState := range allJoinStates {
		if e.state.IsJoinReady(stepName) {
			if err := e.processJoinCompletion(ctx, stepName, joinState.WaitingPathID); err != nil {
				return err
			}
		}
	}
	return nil
}

// processJoinRequest handles a join request from a branch
func (e *Execution) processJoinRequest(ctx context.Context, snapshot branchSnapshot) error {
	joinReq := snapshot.joinRequest
	stepName := joinReq.StepName

	e.logger.Debug("processing join request",
		"step_name", stepName,
		"path_id", snapshot.BranchID,
		"join_config", joinReq.Config)

	// Add branch to join state as the waiting branch
	e.state.AddBranchToJoin(stepName, snapshot.BranchID, joinReq.Config, joinReq.Variables, joinReq.StepOutputs)

	// Mark branch as waiting at join (but keep it active)
	e.state.UpdateBranchState(snapshot.BranchID, func(state *BranchState) {
		state.Status = ExecutionStatusWaiting
		state.EndTime = snapshot.EndTime
		state.Variables = joinReq.Variables
	})

	// Check if join is ready to proceed immediately
	if e.state.IsJoinReady(stepName) {
		// This branch can proceed immediately
		return e.processJoinCompletion(ctx, stepName, snapshot.BranchID)
	}

	// Path will continue waiting
	e.logger.Debug("branch waiting for other branches to complete",
		"step_name", stepName,
		"waiting_path", snapshot.BranchID)

	return nil
}

// processJoinCompletion handles completion of a join when all required branches have arrived
func (e *Execution) processJoinCompletion(ctx context.Context, stepName string, triggeringBranchID string) error {
	joinState := e.state.GetJoinState(stepName)
	if joinState == nil {
		return fmt.Errorf("join state not found for step %q", stepName)
	}

	e.logger.Info("join completed, resuming waiting branch",
		"step_name", stepName,
		"waiting_path", joinState.WaitingPathID)

	// Get the step to continue from
	step, ok := e.workflow.GetStep(stepName)
	if !ok {
		return fmt.Errorf("join step %q not found in workflow", stepName)
	}

	// Merge state from completed required branches (already handles branch mappings and nested fields)
	mergedVariables, err := e.mergeJoinedBranchState(joinState)
	if err != nil {
		return fmt.Errorf("failed to merge joined branch state: %w", err)
	}

	// Find the waiting branch
	waitingPathID := joinState.WaitingPathID
	continuingBranch, exists := e.getActiveBranch(waitingPathID)
	if !exists {
		return fmt.Errorf("waiting branch %q not found in active branches", waitingPathID)
	}

	// Update the waiting branch's variables with merged state
	for key, value := range mergedVariables {
		continuingBranch.state.Set(key, value)
	}

	// Update branch state to show it's running again
	e.state.UpdateBranchState(waitingPathID, func(state *BranchState) {
		state.Status = ExecutionStatusRunning
		state.Variables = mergedVariables
		state.EndTime = time.Time{} // Clear end time since branch is continuing
	})

	// Remove join state as it's now processed
	e.state.RemoveJoinState(stepName)

	// Handle next steps from the join step for the continuing branch
	newBranchSpecs, err := e.evaluateJoinNextSteps(ctx, step, mergedVariables)
	if err != nil {
		return fmt.Errorf("failed to evaluate next steps for join %q: %w", stepName, err)
	}

	// Resume the continuing branch with the next step(s)
	if len(newBranchSpecs) == 1 && newBranchSpecs[0].Name == "" {
		// Single unnamed branch - continue with the same branch
		continuingBranch.currentStep = newBranchSpecs[0].Step
		e.logger.Debug("continuing branch with next step",
			"path_id", waitingPathID,
			"next_step", newBranchSpecs[0].Step.Name)

		// Send a signal to resume the branch execution
		continuingBranch.resumeFromJoin <- struct{}{}

	} else if len(newBranchSpecs) > 0 {
		// Multiple branches or named branches - complete current branch and create new ones
		e.state.UpdateBranchState(waitingPathID, func(state *BranchState) {
			state.Status = ExecutionStatusCompleted
			state.EndTime = time.Now()
		})
		e.removeActiveBranch(waitingPathID)

		// Create new branches for branching
		newBranches := make([]*branch, 0, len(newBranchSpecs))
		for _, pathSpec := range newBranchSpecs {
			branchID, err := e.state.GenerateBranchID(waitingPathID, pathSpec.Name)
			if err != nil {
				return fmt.Errorf("failed to generate branch ID for joined branch: %w", err)
			}
			newBranch := e.createBranchWithVariables(branchID, pathSpec.Step, pathSpec.Variables)
			newBranches = append(newBranches, newBranch)
		}
		e.runBranches(ctx, newBranches...)
	} else {
		// No next steps - mark the continuing branch as completed
		e.state.UpdateBranchState(waitingPathID, func(state *BranchState) {
			state.Status = ExecutionStatusCompleted
			state.EndTime = time.Now()
		})
		e.removeActiveBranch(waitingPathID)
	}

	return nil
}

// mergeJoinedBranchState stores each branch's variables under specified keys and returns the merged result
func (e *Execution) mergeJoinedBranchState(joinState *JoinState) (map[string]any, error) {
	// Get all branch states
	branchStates := e.state.GetBranchStates()

	// Collect variables from required completed branches
	var requiredBranches []string
	if len(joinState.Config.Branches) > 0 {
		// Use specified branches
		requiredBranches = joinState.Config.Branches
	} else {
		// Use all completed branches except the waiting branch
		for branchID, branchState := range branchStates {
			if branchID != joinState.WaitingPathID && branchState.Status == ExecutionStatusCompleted {
				requiredBranches = append(requiredBranches, branchID)
			}
		}
	}

	if len(requiredBranches) == 0 {
		return nil, fmt.Errorf("no required branches found for join")
	}

	// Create the merged variables map
	mergedVariables := make(map[string]any)

	// First, handle default branch mappings for required branches without explicit mappings
	processedBranches := make(map[string]bool)
	if joinState.Config.BranchMappings != nil {
		for mappingKey, destination := range joinState.Config.BranchMappings {
			branchID, variableName := e.parseBranchMapping(mappingKey)

			// Check if this branch is required and completed
			branchState, exists := branchStates[branchID]
			if !exists || branchState.Status != ExecutionStatusCompleted {
				continue // Skip if branch doesn't exist or isn't completed
			}

			// Skip if this branch is not in the required branches list
			if !e.isBranchRequired(branchID, requiredBranches) {
				continue
			}

			if variableName == "" {
				// Store entire branch state (current behavior): "branchID": "destination"
				pathVariables := copyMap(branchState.Variables)
				setNestedField(mergedVariables, destination, pathVariables)
			} else {
				// Extract specific variable: "branchID.variable": "destination"
				if value, exists := getNestedField(branchState.Variables, variableName); exists {
					setNestedField(mergedVariables, destination, value)
				}
				// Note: If variable doesn't exist, we silently skip it
			}

			processedBranches[branchID] = true
		}
	}

	// Handle any required branches that don't have explicit mappings (use branch ID as destination)
	for _, branchID := range requiredBranches {
		if processedBranches[branchID] {
			continue // Already processed
		}

		branchState, exists := branchStates[branchID]
		if !exists || branchState.Status != ExecutionStatusCompleted {
			continue
		}

		// Use branch ID as destination for unmapped branches
		pathVariables := copyMap(branchState.Variables)
		setNestedField(mergedVariables, branchID, pathVariables)
		processedBranches[branchID] = true
	}

	if len(processedBranches) == 0 {
		return nil, fmt.Errorf("no completed required branches found for join")
	}

	return mergedVariables, nil
}

// parseBranchMapping parses a branch mapping key into branchID and optional variable name
// Examples: "pathA" -> ("pathA", ""), "pathA.result" -> ("pathA", "result")
func (e *Execution) parseBranchMapping(mappingKey string) (branchID, variableName string) {
	if !strings.Contains(mappingKey, ".") {
		return mappingKey, ""
	}

	parts := strings.SplitN(mappingKey, ".", 2)
	if len(parts) != 2 {
		return mappingKey, ""
	}

	return parts[0], parts[1]
}

// isBranchRequired checks if a branchID is in the list of required branches
func (e *Execution) isBranchRequired(branchID string, requiredBranches []string) bool {
	for _, required := range requiredBranches {
		if required == branchID {
			return true
		}
	}
	return false
}

// evaluateJoinNextSteps evaluates the next steps from a join step
func (e *Execution) evaluateJoinNextSteps(ctx context.Context, step *Step, mergedVariables map[string]any) ([]branchSpec, error) {
	edges := step.Next
	if len(edges) == 0 {
		return nil, nil // No outgoing edges means execution is complete
	}

	// Create a temporary branch state for condition evaluation
	branchOptions := e.branchOptions
	branchOptions.Variables = mergedVariables
	tempBranch := newBranch("temp", step, branchOptions)

	// Get the edge matching strategy for this step
	strategy := step.GetEdgeMatchingStrategy()

	// Evaluate conditions and collect matching edges
	var matchingEdges []*Edge
	for _, edge := range edges {
		if edge.Condition == "" {
			matchingEdges = append(matchingEdges, edge)
		} else {
			match, err := tempBranch.evaluateCondition(ctx, edge.Condition)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate condition %q in join step %q: %w",
					edge.Condition, step.Name, err)
			}
			if match {
				matchingEdges = append(matchingEdges, edge)
			}
		}

		// If using "first" strategy and we found a match, stop here
		if strategy == EdgeMatchingFirst && len(matchingEdges) > 0 {
			break
		}
	}

	// Create branch specs for each matching edge
	var pathSpecs []branchSpec
	for _, edge := range matchingEdges {
		nextStep, ok := e.workflow.GetStep(edge.Step)
		if !ok {
			return nil, fmt.Errorf("next step not found: %s", edge.Step)
		}
		pathSpecs = append(pathSpecs, branchSpec{
			Step:      nextStep,
			Variables: copyMap(mergedVariables),
			Name:      edge.BranchName,
		})
	}
	return pathSpecs, nil
}

// resetFailedBranches resets failed branches for resumption by finding the last successful step
func (e *Execution) resetFailedBranches() error {
	// Find failed branches and reset them
	for branchID, branchState := range e.state.GetBranchStates() {
		if branchState.Status == ExecutionStatusFailed {
			// Find the step that was running when it failed
			var currentStep *Step
			var ok bool

			if branchState.CurrentStep != "" {
				// Try to restart from the step that failed
				currentStep, ok = e.workflow.GetStep(branchState.CurrentStep)
				if !ok {
					// If the current step is not found, try to find a suitable restart point
					e.logger.Warn("failed step not found in workflow, attempting to find restart point",
						"path_id", branchID, "failed_step", branchState.CurrentStep)
					currentStep = e.findRestartStep(branchState)
				}
			}

			if currentStep == nil {
				// If we can't find a restart point, start from the beginning
				e.logger.Warn("could not find restart point for failed branch, restarting from beginning",
					"path_id", branchID)
				currentStep = e.workflow.Start()
			}

			// Reset branch state for resumption
			branchState.Status = ExecutionStatusPending
			branchState.ErrorMessage = ""
			branchState.CurrentStep = currentStep.Name

			// Recreate the execution branch
			e.addActiveBranch(branchID, e.createBranch(branchID, currentStep))

			e.logger.Info("reset failed branch for resumption",
				"path_id", branchID,
				"restart_step", currentStep.Name)
		}
	}

	return nil
}

// findRestartStep attempts to find a suitable step to restart from based on completed step outputs
func (e *Execution) findRestartStep(branchState *BranchState) *Step {
	// Find the last successfully completed step by checking step outputs
	var lastCompletedStep *Step

	for stepName := range branchState.StepOutputs {
		if step, ok := e.workflow.GetStep(stepName); ok {
			// This step completed successfully, it could be a restart point
			// Check if it has next steps
			if len(step.Next) > 0 {
				// Find the first next step that exists in the workflow
				for _, edge := range step.Next {
					if nextStep, exists := e.workflow.GetStep(edge.Step); exists {
						return nextStep
					}
				}
			}
			lastCompletedStep = step
		}
	}

	return lastCompletedStep
}

// createBranch creates a new branch using the options pattern
func (e *Execution) createBranch(id string, step *Step) *branch {
	opts := e.branchOptions
	opts.UpdatesChannel = e.branchSnapshots // Set the updates channel for this branch
	opts.ExecutionID = e.state.ID()
	return newBranch(id, step, opts)
}

// createBranchWithVariables creates a new branch with specific variables (used for branching)
func (e *Execution) createBranchWithVariables(id string, step *Step, variables map[string]any) *branch {
	opts := e.branchOptions
	opts.Variables = variables            // Use provided variables instead of initial state
	opts.UpdatesChannel = e.branchSnapshots // Set the updates channel for this branch
	opts.ExecutionID = e.state.ID()
	// Carry the branch's pending wait state forward so declarative
	// WaitSignal steps can reuse the original deadline on replay.
	if ps, ok := e.state.GetBranchStates()[id]; ok && ps != nil {
		if ps.Wait != nil {
			waitCopy := *ps.Wait
			opts.InitialWait = &waitCopy
		}
		// Seed the runtime pause flag from the checkpoint. A paused
		// branch reconstructed from a checkpoint with PauseRequested=true
		// re-parks at its first step boundary until UnpauseBranch is
		// called.
		opts.InitialPauseRequested = ps.PauseRequested
		opts.InitialPauseReason = ps.PauseReason
	}
	return newBranch(id, step, opts)
}

// executeActivity implements simple activity execution with logging and checkpointing
func (e *Execution) executeActivity(ctx context.Context, stepName, branchID string, activity Activity, params map[string]any, branchState *BranchLocalState) (any, error) {
	// If this branch is being replayed from a wait-unwind checkpoint, pass
	// the pending WaitState through so workflow.Wait can reuse the
	// original deadline instead of restarting the clock. Also seed
	// the activity history from the checkpointed BranchState — but
	// only if it is owned by the current step. A step mismatch means
	// the branch has advanced since the history was written (possibly
	// racing ahead of the orchestrator's clear), so we start fresh.
	var pendingWait *WaitState
	var historySeed map[string]any
	if ps, ok := e.state.GetBranchStates()[branchID]; ok && ps != nil {
		if ps.Wait != nil {
			waitCopy := *ps.Wait
			pendingWait = &waitCopy
		}
		if ps.ActivityHistoryStep == stepName {
			historySeed = copyMap(ps.ActivityHistory)
		}
	}

	// Build the activity history with a commit callback that
	// persists each mutation into BranchState. Writing through
	// UpdateBranchState keeps the history durable across wait-unwind
	// replays — if the activity records a value and then unwinds,
	// the checkpoint captures the value so the replay can read it.
	// The commit also updates ActivityHistoryStep so the scope check
	// above matches on subsequent replays of the same step.
	history := newHistory(historySeed, func(snapshot map[string]any) {
		e.state.UpdateBranchState(branchID, func(state *BranchState) {
			state.ActivityHistory = snapshot
			state.ActivityHistoryStep = stepName
		})
	})

	// Create enhanced WorkflowContext with direct state access
	workflowCtx := NewContext(ctx, ExecutionContextOptions{
		BranchLocalState:  branchState,
		Logger:          e.logger,
		Compiler:        e.compiler,
		BranchID:          branchID,
		StepName:        stepName,
		ExecutionID:     e.state.ID(),
		SignalStore:     e.signalStore,
		PendingWait:     pendingWait,
		ActivityHistory: history,
	})

	// Inject progress reporter if step progress tracking is configured
	if e.stepProgressTracker != nil {
		workflowCtx.progressReporter = func(detail ProgressDetail) {
			e.stepProgressTracker.reportProgress(ctx, stepName, branchID, detail)
		}
	}

	// Trigger activity start callback
	startTime := time.Now()
	activityEvent := &ActivityExecutionEvent{
		ExecutionID:  e.state.ID(),
		WorkflowName: e.workflow.Name(),
		BranchID:       branchID,
		StepName:     stepName,
		ActivityName: activity.Name(),
		Parameters:   copyMap(params),
		StartTime:    startTime,
	}
	e.executionCallbacks.BeforeActivityExecution(workflowCtx, activityEvent)

	// Execute the activity with the enhanced WorkflowContext
	result, err := activity.Execute(workflowCtx, params)
	endTime := time.Now()
	duration := endTime.Sub(startTime)

	// Wait-unwind is a suspension, not a failure. Skip activity logging
	// and checkpointing on the unwind branch: the orchestrator will emit a
	// single authoritative checkpoint from processBranchSnapshot when the
	// waitRequest snapshot is processed, and the activity logger should
	// not see the unwind as an error entry. The AfterActivityExecution
	// callback is also skipped so consumers don't observe a dangling
	// half-completed activity — the activity will be replayed in full on
	// resume and the callback pair will fire then. Return the sentinel
	// unchanged so branch.Run can detect and park the branch.
	if isWaitUnwind(err) {
		return nil, err
	}

	// Update activity event with results
	activityEvent.Result = result
	activityEvent.EndTime = endTime
	activityEvent.Duration = duration
	activityEvent.Error = err
	e.executionCallbacks.AfterActivityExecution(workflowCtx, activityEvent)

	// Log the activity
	logEntry := &ActivityLogEntry{
		ExecutionID: e.state.ID(),
		StepName:    stepName,
		BranchID:      branchID,
		Activity:    activity.Name(),
		Parameters:  params,
		Result:      result,
		StartTime:   startTime,
		Duration:    duration.Seconds(),
	}

	if err != nil {
		logEntry.Error = err.Error()
	}

	e.mutex.Lock()
	defer e.mutex.Unlock()

	// Log activity execution
	if logErr := e.activityLogger.LogActivity(ctx, logEntry); logErr != nil {
		e.logger.Error("failed to log activity", "error", logErr)
		return nil, logErr
	}

	// Checkpoint after activity execution
	if checkpointErr := e.saveCheckpoint(ctx); checkpointErr != nil {
		e.logger.Error("failed to save checkpoint", "error", checkpointErr)
		return nil, checkpointErr
	}

	return result, err
}
