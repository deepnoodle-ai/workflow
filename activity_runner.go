package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"log/slog"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
)

// ActivityRunner wraps a workflow.Activity to implement domain.Runner and domain.InlineExecutor.
// This adapter allows activities defined with the workflow.Activity interface to be executed
// by the engine in local mode.
type ActivityRunner struct {
	activity   Activity
	clock      Clock
	logger     *slog.Logger
	callbacks  ExecutionCallbacks
}

// NewActivityRunner creates a new ActivityRunner for the given activity.
func NewActivityRunner(activity Activity, opts ...ActivityRunnerOption) *ActivityRunner {
	r := &ActivityRunner{
		activity:  activity,
		clock:     NewRealClock(),
		logger:    NewLogger(),
		callbacks: &BaseExecutionCallbacks{},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ActivityRunnerOption configures an ActivityRunner.
type ActivityRunnerOption func(*ActivityRunner)

// WithClock sets the clock for the ActivityRunner.
func WithClock(clock Clock) ActivityRunnerOption {
	return func(r *ActivityRunner) {
		r.clock = clock
	}
}

// WithActivityLogger sets the logger for the ActivityRunner.
func WithActivityLogger(logger *slog.Logger) ActivityRunnerOption {
	return func(r *ActivityRunner) {
		r.logger = logger
	}
}

// WithExecutionCallbacks sets the execution callbacks for the ActivityRunner.
func WithExecutionCallbacks(callbacks ExecutionCallbacks) ActivityRunnerOption {
	return func(r *ActivityRunner) {
		r.callbacks = callbacks
	}
}

// ToSpec converts activity parameters to a TaskSpec for inline execution.
func (r *ActivityRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskSpec, error) {
	return &domain.TaskSpec{
		Type:  "inline",
		Input: params,
	}, nil
}

// ParseResult interprets the worker's result as activity output.
func (r *ActivityRunner) ParseResult(result *domain.TaskResult) (map[string]any, error) {
	if !result.Success {
		return nil, &ActivityError{Message: result.Error}
	}
	return result.Data, nil
}

// Execute runs the activity in-process and returns the result.
// This implements domain.InlineExecutor.
func (r *ActivityRunner) Execute(ctx context.Context, params map[string]any) (*domain.TaskResult, error) {
	startTime := time.Now()

	// Extract execution info from context
	execInfo, ok := ctx.Value(domain.ExecutionContextKey{}).(*domain.ExecutionInfo)
	if !ok {
		// Fall back to minimal context if not provided
		execInfo = &domain.ExecutionInfo{
			ExecutionID: "unknown",
			PathID:      "main",
			StepName:    "unknown",
			Inputs:      make(map[string]any),
			Variables:   make(map[string]any),
		}
	}

	// Call BeforeActivityExecution callback
	r.callbacks.BeforeActivityExecution(ctx, &ActivityExecutionEvent{
		ExecutionID:  execInfo.ExecutionID,
		PathID:       execInfo.PathID,
		StepName:     execInfo.StepName,
		ActivityName: r.activity.Name(),
		Parameters:   params,
	})

	// Create path local state for the activity
	pathState := NewPathLocalState(execInfo.Inputs, execInfo.Variables)

	// Create workflow context with all helpers
	wfCtx := &activityExecutionContext{
		Context:        ctx,
		PathLocalState: pathState,
		logger:         r.logger,
		pathID:         execInfo.PathID,
		stepName:       execInfo.StepName,
		executionID:    execInfo.ExecutionID,
		clock:          r.clock,
	}

	// Execute the activity
	result, err := r.activity.Execute(wfCtx, params)
	duration := time.Since(startTime)

	if err != nil {
		// Call OnActivityFailure callback
		r.callbacks.OnActivityFailure(ctx, &ActivityExecutionEvent{
			ExecutionID:  execInfo.ExecutionID,
			PathID:       execInfo.PathID,
			StepName:     execInfo.StepName,
			ActivityName: r.activity.Name(),
			Parameters:   params,
			Duration:     duration,
			Error:        err,
		})
		return &domain.TaskResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	// Call AfterActivityExecution callback
	r.callbacks.AfterActivityExecution(ctx, &ActivityExecutionEvent{
		ExecutionID:  execInfo.ExecutionID,
		PathID:       execInfo.PathID,
		StepName:     execInfo.StepName,
		ActivityName: r.activity.Name(),
		Parameters:   params,
		Duration:     duration,
		Result:       result,
	})

	// Convert result to map[string]any
	var data map[string]any
	switch v := result.(type) {
	case map[string]any:
		data = v
	case nil:
		data = nil
	default:
		// Wrap non-map results
		data = map[string]any{"result": result}
	}

	return &domain.TaskResult{
		Success: true,
		Data:    data,
	}, nil
}

// ActivityError represents an error from activity execution.
type ActivityError struct {
	Message string
}

func (e *ActivityError) Error() string {
	return e.Message
}

// activityExecutionContext implements workflow.Context for activity execution.
type activityExecutionContext struct {
	context.Context
	*PathLocalState
	logger      *slog.Logger
	pathID      string
	stepName    string
	executionID string
	clock       Clock
	idCounter   atomic.Uint64
	randSource  *rand.Rand
}

// Verify interface compliance.
var _ Context = (*activityExecutionContext)(nil)

// GetLogger returns the logger for this context.
func (c *activityExecutionContext) GetLogger() *slog.Logger {
	return c.logger
}

// GetPathID returns the current path ID.
func (c *activityExecutionContext) GetPathID() string {
	return c.pathID
}

// GetStepName returns the current step name.
func (c *activityExecutionContext) GetStepName() string {
	return c.stepName
}

// GetExecutionID returns the execution ID.
func (c *activityExecutionContext) GetExecutionID() string {
	return c.executionID
}

// Clock returns the clock for this context.
func (c *activityExecutionContext) Clock() Clock {
	if c.clock == nil {
		return NewRealClock()
	}
	return c.clock
}

// Now returns the current time from the context's clock.
func (c *activityExecutionContext) Now() time.Time {
	return c.Clock().Now()
}

// DeterministicID generates a deterministic ID based on execution context.
func (c *activityExecutionContext) DeterministicID(prefix string) string {
	h := sha256.New()
	h.Write([]byte(c.executionID))
	h.Write([]byte(c.pathID))
	h.Write([]byte(c.stepName))

	counter := c.idCounter.Add(1)
	if err := binary.Write(h, binary.BigEndian, counter); err != nil {
		panic(err)
	}

	hash := h.Sum(nil)
	// Use base32 encoding for readable IDs
	encoded := encodeBase32(hash[:10])
	return prefix + "_" + encoded
}

// Rand returns a deterministic random source seeded from the execution ID.
func (c *activityExecutionContext) Rand() *rand.Rand {
	if c.randSource == nil {
		h := sha256.Sum256([]byte(c.executionID + c.pathID))
		seed := int64(binary.BigEndian.Uint64(h[:8]))
		c.randSource = rand.New(rand.NewSource(seed))
	}
	return c.randSource
}

// encodeBase32 encodes bytes to lowercase base32.
func encodeBase32(data []byte) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	result := make([]byte, 0, (len(data)*8+4)/5)
	var buffer uint64
	var bitsLeft int

	for _, b := range data {
		buffer = (buffer << 8) | uint64(b)
		bitsLeft += 8
		for bitsLeft >= 5 {
			bitsLeft -= 5
			result = append(result, alphabet[(buffer>>bitsLeft)&0x1f])
		}
	}
	if bitsLeft > 0 {
		result = append(result, alphabet[(buffer<<(5-bitsLeft))&0x1f])
	}
	return string(result)
}

// Verify interface compliance for runners.
var _ domain.Runner = (*ActivityRunner)(nil)
var _ domain.InlineExecutor = (*ActivityRunner)(nil)
