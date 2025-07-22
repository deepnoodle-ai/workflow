package workflow

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow/script"
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

// ExecutionStatus represents the execution status
type ExecutionStatus string

const (
	ExecutionStatusPending   ExecutionStatus = "pending"
	ExecutionStatusRunning   ExecutionStatus = "running"
	ExecutionStatusCompleted ExecutionStatus = "completed"
	ExecutionStatusFailed    ExecutionStatus = "failed"
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
	ScriptCompiler     script.Compiler
	ExecutionCallbacks ExecutionCallbacks
}

// Execution represents a simplified workflow execution with checkpointing
type Execution struct {
	workflow *Workflow

	// Unified state management - replaces scattered fields
	state *ExecutionState

	// Runtime path tracking (not checkpointed)
	activePaths   map[string]*Path
	pathSnapshots chan PathSnapshot

	// Path options template (reused for all paths)
	pathOptions PathOptions

	// Infrastructure dependencies
	activityLogger     ActivityLogger
	compiler           script.Compiler
	checkpointer       Checkpointer
	activities         map[string]Activity
	executionCallbacks ExecutionCallbacks
	adapter            *ExecutionAdapter

	// Logging and formatting
	logger    *slog.Logger
	formatter WorkflowFormatter

	// Single mutex for orchestration data
	mutex             sync.RWMutex
	doneWg            sync.WaitGroup
	started           bool
	checkpointCounter int
}

// NewExecution creates a new simplified execution
func NewExecution(opts ExecutionOptions) (*Execution, error) {
	if opts.Workflow == nil {
		return nil, fmt.Errorf("workflow is required")
	}
	if len(opts.Activities) == 0 {
		return nil, fmt.Errorf("activities are required")
	}
	if opts.ScriptCompiler == nil {
		opts.ScriptCompiler = script.NewRisorScriptingEngine(script.DefaultRisorGlobals())
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

	// Determine input values from inputs map or defaults
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

	activities := make(map[string]Activity, len(opts.Activities))
	for _, activity := range opts.Activities {
		activities[activity.Name()] = activity
	}

	state := newExecutionState(opts.ExecutionID, opts.Workflow.Name(), inputs)

	execution := &Execution{
		workflow:           opts.Workflow,
		state:              state,
		activityLogger:     opts.ActivityLogger,
		checkpointer:       opts.Checkpointer,
		activePaths:        map[string]*Path{},
		pathSnapshots:      make(chan PathSnapshot, 100),
		activities:         activities,
		logger:             opts.Logger.With("execution_id", opts.ExecutionID),
		formatter:          opts.Formatter,
		compiler:           opts.ScriptCompiler,
		executionCallbacks: opts.ExecutionCallbacks,
	}
	execution.adapter = &ExecutionAdapter{execution: execution}

	// Set up path options template
	execution.pathOptions = PathOptions{
		Workflow:         opts.Workflow,
		ActivityRegistry: activities,
		Logger:           opts.Logger,
		Formatter:        opts.Formatter,
		Inputs:           copyMap(inputs),
		Variables:        copyMap(opts.Workflow.InitialState()),
		ActivityExecutor: execution.adapter,
		UpdatesChannel:   execution.pathSnapshots,
		ScriptCompiler:   opts.ScriptCompiler,
	}

	return execution, nil
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

// saveCheckpoint saves the current execution state
func (e *Execution) saveCheckpoint(ctx context.Context) error {
	e.checkpointCounter++
	checkpoint := e.state.ToCheckpoint()
	checkpoint.ID = fmt.Sprintf("%d", e.checkpointCounter)
	return e.checkpointer.SaveCheckpoint(ctx, checkpoint)
}

// loadCheckpoint loads execution state from the latest checkpoint
func (e *Execution) loadCheckpoint(ctx context.Context, priorExecutionID string) error {
	thisID := e.state.ID()

	// Load state from checkpoint
	checkpoint, err := e.checkpointer.LoadCheckpoint(ctx, priorExecutionID)
	if err != nil {
		return fmt.Errorf("failed to load checkpoint: %w", err)
	}
	e.state.FromCheckpoint(checkpoint)

	// Restore the execution ID
	e.state.SetID(thisID)

	lastStatus := e.state.GetStatus()

	// If the prior execution completed, there's nothing to do
	if lastStatus == ExecutionStatusCompleted {
		return nil
	}

	// Handle failed executions
	if lastStatus == ExecutionStatusFailed {
		// Reset failed paths for resumption
		if err := e.resetFailedPaths(); err != nil {
			return fmt.Errorf("failed to reset failed paths for resumption: %w", err)
		}

		originalErr := e.state.GetError()
		if originalErr != nil {
			e.logger.Info("resuming execution from failure", "original_error", originalErr.Error())
		}

		// Clear any previous error and reset status to running
		e.state.SetError(nil)
		e.state.SetStatus(ExecutionStatusRunning)
	}

	// Rebuild active paths for paths that should be running
	pathStates := e.state.GetPathStates()
	e.activePaths = make(map[string]*Path)
	for id, pathState := range pathStates {
		if pathState.Status == PathStatusRunning || pathState.Status == PathStatusPending {
			currentStep, ok := e.workflow.GetStep(pathState.CurrentStep)
			if !ok {
				return fmt.Errorf("step %q not found in workflow for path %s", pathState.CurrentStep, id)
			}
			// Restore path with its stored variables from checkpoint
			e.activePaths[id] = e.createPathWithVariables(id, currentStep, pathState.Variables)
		}
	}

	e.logger.Info("loaded execution from checkpoint",
		"status", e.state.GetStatus(),
		"paths", len(pathStates),
		"active_paths", len(e.activePaths),
		"path_counter", e.state.pathCounter)

	return nil
}

func (e *Execution) start() error {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	if e.started {
		return fmt.Errorf("execution already started")
	}
	e.started = true
	return nil
}

// Run the execution to completion.
func (e *Execution) Run(ctx context.Context) error {
	if err := e.start(); err != nil {
		return err
	}
	return e.run(ctx)
}

// Resume a previous execution from its last checkpoint.
func (e *Execution) Resume(ctx context.Context, priorExecutionID string) error {
	if err := e.start(); err != nil {
		return err
	}

	// Load from checkpoint first
	if err := e.loadCheckpoint(ctx, priorExecutionID); err != nil {
		return err
	}

	// Return early if already completed
	if e.state.GetStatus() == ExecutionStatusCompleted {
		e.logger.Info("execution already completed from checkpoint")
		return nil
	}

	// Continue with normal execution flow
	return e.run(ctx)
}

// run the workflow execution, blocking until completion or error
func (e *Execution) run(ctx context.Context) error {
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
		PathCount:    len(e.activePaths),
	})

	// Start execution paths
	if len(e.activePaths) == 0 {
		// Starting fresh - create initial path
		startStep := e.workflow.Start()
		e.runPaths(ctx, e.createPath("main", startStep))
	} else {
		// Resuming from checkpoint - restart active paths
		e.logger.Info("resuming execution from checkpoint", "active_paths", len(e.activePaths))
		for _, path := range e.activePaths {
			e.runPaths(ctx, path)
		}
	}

	// Process path snapshots
	var executionErr error
	for len(e.activePaths) > 0 && executionErr == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case snapshot := <-e.pathSnapshots:
			if err := e.processPathSnapshot(ctx, snapshot); err != nil {
				executionErr = err
				cancel() // cancel any other paths
			}
		}
	}

	// Wait for all paths to complete
	e.doneWg.Wait()

	// Check for failed paths
	failedIDs := e.state.GetFailedPathIDs()

	// Update final status
	finalErr := executionErr
	var finalStatus ExecutionStatus
	if len(failedIDs) > 0 {
		finalStatus = ExecutionStatusFailed
		if finalErr == nil {
			finalErr = fmt.Errorf("execution failed: %v", failedIDs)
		}
		e.logger.Error("execution failed", "failed_paths", failedIDs)
	} else {
		finalStatus = ExecutionStatusCompleted
		e.logger.Info("execution completed")

		// Extract workflow outputs from final path variables
		if err := e.extractWorkflowOutputs(); err != nil {
			e.logger.Error("failed to extract workflow outputs", "error", err)
			finalErr = err
			finalStatus = ExecutionStatusFailed
		}
	}
	e.state.SetFinished(finalStatus, time.Now(), finalErr)

	// Trigger workflow completion/failure callback
	endTime := time.Now()
	duration := endTime.Sub(e.state.GetStartTime())
	e.executionCallbacks.AfterWorkflowExecution(ctx, &WorkflowExecutionEvent{
		ExecutionID:  e.state.ID(),
		WorkflowName: e.workflow.Name(),
		Status:       finalStatus,
		StartTime:    e.state.GetStartTime(),
		EndTime:      endTime,
		Duration:     duration,
		Inputs:       copyMap(e.state.GetInputs()),
		Outputs:      copyMap(e.state.GetOutputs()),
		PathCount:    len(e.state.GetPathStates()),
		Error:        finalErr,
	})

	// Final checkpoint
	if checkpointErr := e.saveCheckpoint(ctx); checkpointErr != nil {
		e.logger.Error("failed to save final checkpoint", "error", checkpointErr)
	}

	return finalErr
}

// extractWorkflowOutputs extracts workflow outputs from final path variables
func (e *Execution) extractWorkflowOutputs() error {
	pathStates := e.state.GetPathStates()
	outputs := e.workflow.Outputs()

	if len(outputs) == 0 {
		return nil
	}

	// Determine which path to use for output extraction
	var pathState *PathState
	var completedPaths []*PathState
	for _, pathState = range pathStates {
		if pathState.Status == PathStatusCompleted {
			completedPaths = append(completedPaths, pathState)
		}
	}
	if len(completedPaths) == 0 {
		return fmt.Errorf("no completed paths found")
	}
	if len(completedPaths) > 1 {
		// Think about how to handle this. Which path do we use?
		return fmt.Errorf("multiple paths completed, cannot extract outputs")
	}
	pathState = completedPaths[0]

	// Extract output values from path variables
	for _, outputDef := range outputs {
		outputName := outputDef.Name
		variableName := outputDef.Variable
		if variableName == "" {
			variableName = outputName
		}
		if value, exists := pathState.Variables[variableName]; exists {
			e.state.SetOutput(outputName, value)
			e.logger.Info("extracted workflow output",
				"output_name", outputName,
				"variable_name", variableName,
				"value", value)
		} else {
			e.logger.Warn("workflow output variable not found",
				"output_name", outputName,
				"variable_name", variableName)
			return fmt.Errorf("workflow output variable not found")
		}
	}
	return nil
}

// runPaths begins executing one or more new execution paths in goroutines.
// It does not wait for the paths to complete.
func (e *Execution) runPaths(ctx context.Context, paths ...*Path) {
	for _, path := range paths {
		pathID := path.ID()
		e.activePaths[pathID] = path
		startTime := time.Now()
		e.state.SetPathState(pathID, &PathState{
			ID:          pathID,
			Status:      PathStatusRunning,
			CurrentStep: path.CurrentStep().Name,
			StartTime:   startTime,
			StepOutputs: map[string]any{},
			Variables:   path.Variables(), // Store path's current variables
		})

		// Trigger path start callback
		e.executionCallbacks.BeforePathExecution(ctx, &PathExecutionEvent{
			ExecutionID:  e.state.ID(),
			WorkflowName: e.workflow.Name(),
			PathID:       pathID,
			Status:       PathStatusRunning,
			StartTime:    startTime,
			CurrentStep:  path.CurrentStep().Name,
			StepOutputs:  map[string]any{},
		})

		e.doneWg.Add(1)
		go func(p *Path) {
			defer e.doneWg.Done()
			p.Run(ctx)
		}(path)
	}
}

func (e *Execution) processPathSnapshot(ctx context.Context, snapshot PathSnapshot) error {
	if snapshot.Error != nil {
		e.state.UpdatePathState(snapshot.PathID, func(state *PathState) {
			state.Status = PathStatusFailed
			state.ErrorMessage = snapshot.Error.Error()
			state.EndTime = snapshot.EndTime
		})

		// Trigger path failure callback
		duration := snapshot.EndTime.Sub(snapshot.StartTime)
		pathState := e.state.GetPathStates()[snapshot.PathID]
		e.executionCallbacks.AfterPathExecution(ctx, &PathExecutionEvent{
			ExecutionID:  e.state.ID(),
			WorkflowName: e.workflow.Name(),
			PathID:       snapshot.PathID,
			Status:       PathStatusFailed,
			StartTime:    snapshot.StartTime,
			EndTime:      snapshot.EndTime,
			Duration:     duration,
			CurrentStep:  snapshot.StepName,
			StepOutputs:  copyMap(pathState.StepOutputs),
			Error:        snapshot.Error,
		})
		return snapshot.Error
	}

	// Store step output and update status
	e.state.UpdatePathState(snapshot.PathID, func(state *PathState) {
		state.StepOutputs[snapshot.StepName] = snapshot.StepOutput
		state.Status = snapshot.Status
		if snapshot.Status == PathStatusCompleted {
			state.EndTime = snapshot.EndTime
		}

		// Update path variables from the active path (if it still exists)
		if activePath, exists := e.activePaths[snapshot.PathID]; exists {
			state.Variables = activePath.Variables()
		}
	})

	// Remove completed path
	isCompleted := snapshot.Status == PathStatusCompleted || snapshot.Status == PathStatusFailed
	if isCompleted {
		delete(e.activePaths, snapshot.PathID)

		// Trigger path completion callback for successful completion
		if snapshot.Status == PathStatusCompleted {
			duration := snapshot.EndTime.Sub(snapshot.StartTime)
			pathState := e.state.GetPathStates()[snapshot.PathID]
			e.executionCallbacks.AfterPathExecution(ctx, &PathExecutionEvent{
				ExecutionID:  e.state.ID(),
				WorkflowName: e.workflow.Name(),
				PathID:       snapshot.PathID,
				Status:       PathStatusCompleted,
				StartTime:    snapshot.StartTime,
				EndTime:      snapshot.EndTime,
				Duration:     duration,
				CurrentStep:  snapshot.StepName,
				StepOutputs:  copyMap(pathState.StepOutputs),
			})
		}
	}

	// Create and execute new paths from branching
	if len(snapshot.NewPaths) > 0 {
		newPaths := make([]*Path, 0, len(snapshot.NewPaths))
		for _, pathSpec := range snapshot.NewPaths {
			pathID := e.state.NextPathID(snapshot.PathID)
			// Use the specific variables from the path spec (copied from parent path)
			newPath := e.createPathWithVariables(pathID, pathSpec.Step, pathSpec.Variables)
			newPaths = append(newPaths, newPath)
		}
		e.runPaths(ctx, newPaths...)
	}

	e.logger.Debug("path snapshot processed",
		"active_paths", len(e.activePaths),
		"completed_path", isCompleted,
		"new_paths", len(snapshot.NewPaths))

	return nil
}

// resetFailedPaths resets failed paths for resumption by finding the last successful step
func (e *Execution) resetFailedPaths() error {
	// Find failed paths and reset them
	for pathID, pathState := range e.state.GetPathStates() {
		if pathState.Status == PathStatusFailed {
			// Find the step that was running when it failed
			var currentStep *Step
			var ok bool

			if pathState.CurrentStep != "" {
				// Try to restart from the step that failed
				currentStep, ok = e.workflow.GetStep(pathState.CurrentStep)
				if !ok {
					// If the current step is not found, try to find a suitable restart point
					e.logger.Warn("failed step not found in workflow, attempting to find restart point",
						"path_id", pathID, "failed_step", pathState.CurrentStep)
					currentStep = e.findRestartStep(pathState)
				}
			}

			if currentStep == nil {
				// If we can't find a restart point, start from the beginning
				e.logger.Warn("could not find restart point for failed path, restarting from beginning",
					"path_id", pathID)
				currentStep = e.workflow.Start()
			}

			// Reset path state for resumption
			pathState.Status = PathStatusPending
			pathState.ErrorMessage = ""
			pathState.CurrentStep = currentStep.Name

			// Recreate the execution path
			e.activePaths[pathID] = e.createPath(pathID, currentStep)

			e.logger.Info("reset failed path for resumption",
				"path_id", pathID,
				"restart_step", currentStep.Name)
		}
	}

	return nil
}

// findRestartStep attempts to find a suitable step to restart from based on completed step outputs
func (e *Execution) findRestartStep(pathState *PathState) *Step {
	// Find the last successfully completed step by checking step outputs
	var lastCompletedStep *Step

	for stepName := range pathState.StepOutputs {
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

// createPath creates a new path using the options pattern
func (e *Execution) createPath(id string, step *Step) *Path {
	opts := e.pathOptions
	opts.UpdatesChannel = e.pathSnapshots // Set the updates channel for this path
	return NewPath(id, step, opts)
}

// createPathWithVariables creates a new path with specific variables (used for branching)
func (e *Execution) createPathWithVariables(id string, step *Step, variables map[string]any) *Path {
	opts := e.pathOptions
	opts.Variables = variables            // Use provided variables instead of initial state
	opts.UpdatesChannel = e.pathSnapshots // Set the updates channel for this path
	return NewPath(id, step, opts)
}

// executeActivity implements simple activity execution with logging and checkpointing
func (e *Execution) executeActivity(ctx context.Context, stepName, pathID string, activity Activity, params map[string]any, pathState *PathLocalState) (any, error) {
	// Create context with the path-local state
	ctx = WithLogger(ctx, e.logger)
	ctx = WithState(ctx, pathState)
	ctx = WithCompiler(ctx, e.compiler)

	// Trigger activity start callback
	startTime := time.Now()
	activityEvent := &ActivityExecutionEvent{
		ExecutionID:  e.state.ID(),
		WorkflowName: e.workflow.Name(),
		PathID:       pathID,
		StepName:     stepName,
		ActivityName: activity.Name(),
		Parameters:   copyMap(params),
		StartTime:    startTime,
	}
	e.executionCallbacks.BeforeActivityExecution(ctx, activityEvent)

	// Execute the activity
	result, err := activity.Execute(ctx, params)
	endTime := time.Now()
	duration := endTime.Sub(startTime)

	// Update activity event with results
	activityEvent.Result = result
	activityEvent.EndTime = endTime
	activityEvent.Duration = duration
	activityEvent.Error = err
	e.executionCallbacks.AfterActivityExecution(ctx, activityEvent)

	// Log the activity
	logEntry := &ActivityLogEntry{
		ExecutionID: e.state.ID(),
		StepName:    stepName,
		PathID:      pathID,
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
