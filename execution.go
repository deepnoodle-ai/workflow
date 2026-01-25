package workflow

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/engine"
	"github.com/deepnoodle-ai/workflow/stores"
	"go.jetify.com/typeid"
)

// NewExecutionID returns a new UUID for execution identification
func NewExecutionID() string {
	id, err := typeid.WithPrefix("exec")
	if err != nil {
		panic(err)
	}
	return id.String()
}

// ExecutionStatus represents the execution status.
type ExecutionStatus = domain.ExecutionStatus

const (
	ExecutionStatusPending   = domain.ExecutionStatusPending
	ExecutionStatusRunning   = domain.ExecutionStatusRunning
	ExecutionStatusWaiting   = domain.ExecutionStatusWaiting
	ExecutionStatusCompleted = domain.ExecutionStatusCompleted
	ExecutionStatusFailed    = domain.ExecutionStatusFailed
	ExecutionStatusCancelled = domain.ExecutionStatusCancelled
)

// ExecutionOptions configures a new execution
type ExecutionOptions struct {
	Workflow           *Workflow
	Inputs             map[string]any
	ActivityLogger     ActivityLogger
	Checkpointer       Checkpointer
	Logger             *slog.Logger
	Formatter          WorkflowFormatter
	ExecutionID        string
	Activities         []Activity
	ExecutionCallbacks ExecutionCallbacks
	Clock              Clock // Optional clock for testing
}

// Execution represents a workflow execution that uses the engine internally.
// This provides the simplified local execution API while using the same
// execution machinery as the distributed engine.
type Execution struct {
	id         string
	workflow   *Workflow
	inputs     map[string]any
	activities map[string]Activity

	// Engine and store
	engine *engine.Engine
	store  domain.Store

	// Cached results
	status  ExecutionStatus
	outputs map[string]any
	err     error

	// Infrastructure dependencies
	activityLogger     ActivityLogger
	checkpointer       Checkpointer
	executionCallbacks ExecutionCallbacks
	clock              Clock

	// Logging and formatting
	logger    *slog.Logger
	formatter WorkflowFormatter

	// Synchronization
	mutex   sync.RWMutex
	started bool
}

// NewExecution creates a new workflow execution.
func NewExecution(opts ExecutionOptions) (*Execution, error) {
	if opts.Workflow == nil {
		return nil, fmt.Errorf("workflow is required")
	}
	if len(opts.Activities) == 0 {
		return nil, fmt.Errorf("activities are required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.ActivityLogger == nil {
		opts.ActivityLogger = NewNullActivityLogger()
	}
	if opts.Checkpointer == nil {
		opts.Checkpointer = NewNullCheckpointer()
	}
	if opts.ExecutionID == "" {
		opts.ExecutionID = NewExecutionID()
	}
	if opts.ExecutionCallbacks == nil {
		opts.ExecutionCallbacks = &BaseExecutionCallbacks{}
	}
	if opts.Clock == nil {
		opts.Clock = NewRealClock()
	}

	// Validate and process inputs
	inputs := make(map[string]any, len(opts.Inputs))
	for _, input := range opts.Workflow.Inputs() {
		if v, ok := opts.Inputs[input.Name]; ok {
			inputs[input.Name] = v
		} else {
			if input.Default == nil {
				return nil, fmt.Errorf("input %q is required", input.Name)
			}
			inputs[input.Name] = input.Default
		}
	}
	for k := range opts.Inputs {
		if _, ok := inputs[k]; !ok {
			return nil, fmt.Errorf("unknown input %q", k)
		}
	}

	// Build activity map
	activities := make(map[string]Activity, len(opts.Activities))
	for _, activity := range opts.Activities {
		activities[activity.Name()] = activity
	}

	return &Execution{
		id:                 opts.ExecutionID,
		workflow:           opts.Workflow,
		inputs:             inputs,
		activities:         activities,
		status:             ExecutionStatusPending,
		activityLogger:     opts.ActivityLogger,
		checkpointer:       opts.Checkpointer,
		executionCallbacks: opts.ExecutionCallbacks,
		clock:              opts.Clock,
		logger:             opts.Logger.With("execution_id", opts.ExecutionID),
		formatter:          opts.Formatter,
	}, nil
}

// ID returns the execution ID
func (e *Execution) ID() string {
	return e.id
}

// Status returns the current execution status
func (e *Execution) Status() ExecutionStatus {
	e.mutex.RLock()
	defer e.mutex.RUnlock()
	return e.status
}

// GetOutputs returns the execution outputs
func (e *Execution) GetOutputs() map[string]any {
	e.mutex.RLock()
	defer e.mutex.RUnlock()
	return copyMap(e.outputs)
}

// Run executes the workflow to completion.
func (e *Execution) Run(ctx context.Context) error {
	e.mutex.Lock()
	if e.started {
		e.mutex.Unlock()
		return fmt.Errorf("execution already started")
	}
	e.started = true
	e.status = ExecutionStatusRunning
	e.mutex.Unlock()

	return e.run(ctx)
}

// Resume resumes a previous execution from its last checkpoint.
func (e *Execution) Resume(ctx context.Context, priorExecutionID string) error {
	e.mutex.Lock()
	if e.started {
		e.mutex.Unlock()
		return fmt.Errorf("execution already started")
	}
	e.started = true
	e.mutex.Unlock()

	// Load checkpoint from prior execution
	checkpoint, err := e.checkpointer.LoadCheckpoint(ctx, priorExecutionID)
	if err != nil {
		return fmt.Errorf("failed to load checkpoint: %w", err)
	}
	if checkpoint == nil {
		return fmt.Errorf("no checkpoint found for execution %q", priorExecutionID)
	}

	// If already completed, nothing to do
	if ExecutionStatus(checkpoint.Status) == ExecutionStatusCompleted {
		e.mutex.Lock()
		e.status = ExecutionStatusCompleted
		e.outputs = copyMap(checkpoint.Outputs)
		e.mutex.Unlock()
		e.logger.Info("execution already completed from checkpoint")
		return nil
	}

	// Continue with normal execution flow
	e.mutex.Lock()
	e.status = ExecutionStatusRunning
	e.mutex.Unlock()

	return e.run(ctx)
}

// run is the internal execution method.
func (e *Execution) run(ctx context.Context) error {
	startTime := time.Now()

	// Call BeforeWorkflowExecution callback
	e.executionCallbacks.BeforeWorkflowExecution(ctx, &WorkflowExecutionEvent{
		ExecutionID:  e.id,
		WorkflowName: e.workflow.Name(),
	})

	// Call BeforePathExecution for the main path
	e.executionCallbacks.BeforePathExecution(ctx, &PathExecutionEvent{
		ExecutionID:  e.id,
		WorkflowName: e.workflow.Name(),
		PathID:       "main",
	})

	// Create in-memory store
	e.store = stores.NewMemoryStore()

	// Create runners from activities
	runners := make(map[string]domain.Runner)
	for name, activity := range e.activities {
		runners[name] = NewActivityRunner(activity,
			WithClock(e.clock),
			WithActivityLogger(e.logger),
			WithExecutionCallbacks(e.executionCallbacks))
	}

	// Create engine in local mode
	eng, err := engine.New(engine.Options{
		Store:         e.store,
		Logger:        e.logger,
		Runners:       runners,
		Mode:          engine.ModeLocal,
		WorkerID:      "local-" + e.id,
		MaxConcurrent: 1, // Sequential execution for local mode
		PollInterval:  10 * time.Millisecond,
	})
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}
	e.engine = eng

	// Start the engine
	if err := e.engine.Start(ctx); err != nil {
		return fmt.Errorf("failed to start engine: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := e.engine.Shutdown(shutdownCtx); err != nil {
			e.logger.Warn("engine shutdown error", "error", err)
		}
	}()

	// Submit workflow
	handle, err := e.engine.Submit(ctx, engine.SubmitRequest{
		Workflow:    e.workflow,
		Inputs:      e.inputs,
		ExecutionID: e.id,
	})
	if err != nil {
		return fmt.Errorf("failed to submit workflow: %w", err)
	}

	// Wait for completion
	err = e.waitForCompletion(ctx, handle.ID)

	// Call path and workflow callbacks
	duration := time.Since(startTime)
	if err != nil {
		// Call OnPathFailure for the main path
		e.executionCallbacks.OnPathFailure(ctx, &PathExecutionEvent{
			ExecutionID:  e.id,
			WorkflowName: e.workflow.Name(),
			PathID:       "main",
			Duration:     duration,
			Error:        err,
		})
		e.executionCallbacks.OnWorkflowExecutionFailure(ctx, &WorkflowExecutionEvent{
			ExecutionID:  e.id,
			WorkflowName: e.workflow.Name(),
			Duration:     duration,
			Error:        err,
		})
	} else {
		// Call AfterPathExecution for the main path
		e.executionCallbacks.AfterPathExecution(ctx, &PathExecutionEvent{
			ExecutionID:  e.id,
			WorkflowName: e.workflow.Name(),
			PathID:       "main",
			Duration:     duration,
		})
		e.executionCallbacks.AfterWorkflowExecution(ctx, &WorkflowExecutionEvent{
			ExecutionID:  e.id,
			WorkflowName: e.workflow.Name(),
			Duration:     duration,
		})
	}

	return err
}

// waitForCompletion polls for execution completion.
func (e *Execution) waitForCompletion(ctx context.Context, execID string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			e.mutex.Lock()
			e.status = ExecutionStatusCancelled
			e.mutex.Unlock()
			return ctx.Err()

		case <-ticker.C:
			record, err := e.engine.Get(ctx, execID)
			if err != nil {
				return fmt.Errorf("failed to get execution: %w", err)
			}

			switch record.Status {
			case domain.ExecutionStatusCompleted:
				outputs, err := e.extractOutputs(record)
				if err != nil {
					e.mutex.Lock()
					e.status = ExecutionStatusFailed
					e.err = err
					e.mutex.Unlock()

					// Save checkpoint
					if err := e.saveCheckpoint(ctx); err != nil {
						e.logger.Warn("failed to save checkpoint", "error", err)
					}
					return err
				}

				e.mutex.Lock()
				e.status = ExecutionStatusCompleted
				e.outputs = outputs
				e.mutex.Unlock()

				// Save checkpoint
				if err := e.saveCheckpoint(ctx); err != nil {
					e.logger.Warn("failed to save checkpoint", "error", err)
				}
				return nil

			case domain.ExecutionStatusFailed:
				e.mutex.Lock()
				e.status = ExecutionStatusFailed
				e.err = fmt.Errorf("%s", record.LastError)
				e.mutex.Unlock()

				// Save checkpoint
				if err := e.saveCheckpoint(ctx); err != nil {
					e.logger.Warn("failed to save checkpoint", "error", err)
				}
				return e.err

			case domain.ExecutionStatusCancelled:
				e.mutex.Lock()
				e.status = ExecutionStatusCancelled
				e.mutex.Unlock()
				return fmt.Errorf("execution cancelled")
			}
		}
	}
}

// extractOutputs extracts workflow outputs from the execution record.
// Uses the workflow's Output definitions to determine which variables to extract.
// Returns an error if any defined output variable is missing.
func (e *Execution) extractOutputs(record *domain.ExecutionRecord) (map[string]any, error) {
	// Load state to extract outputs based on workflow definition
	state, err := engine.LoadState(record)
	if err != nil {
		return nil, fmt.Errorf("failed to load state for outputs: %w", err)
	}

	// If no outputs defined, return empty map (not all step outputs)
	outputDefs := e.workflow.Outputs()
	if len(outputDefs) == 0 {
		return make(map[string]any), nil
	}

	outputs := make(map[string]any)
	for _, outputDef := range outputDefs {
		outputName := outputDef.Name
		variableName := outputDef.Variable
		if variableName == "" {
			variableName = outputName
		}

		// Determine which path to extract from
		targetPath := outputDef.Path
		if targetPath == "" {
			targetPath = "main"
		}

		// Find the target path
		pathState := state.GetPathState(targetPath)
		if pathState == nil {
			return nil, fmt.Errorf("workflow output variable %q not found (path %q does not exist)", variableName, targetPath)
		}

		// Extract the variable value
		value, exists := pathState.Variables[variableName]
		if !exists {
			return nil, fmt.Errorf("workflow output variable %q not found", variableName)
		}
		outputs[outputName] = value
	}

	return outputs, nil
}

// saveCheckpoint saves the current execution state.
func (e *Execution) saveCheckpoint(ctx context.Context) error {
	checkpoint := &Checkpoint{
		ID:           e.id + "-final",
		ExecutionID:  e.id,
		WorkflowName: e.workflow.Name(),
		Status:       string(e.status),
		Inputs:       copyMap(e.inputs),
		Outputs:      copyMap(e.outputs),
		CheckpointAt: time.Now(),
	}

	if e.err != nil {
		checkpoint.Error = e.err.Error()
	}

	return e.checkpointer.SaveCheckpoint(ctx, checkpoint)
}

// copyMap creates a shallow copy of a map.
func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
