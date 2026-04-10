package workflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestWaitSpike exercises the end-to-end durable-wait target for the
// learning spike: an activity calls workflow.Wait, unwinds, the execution
// suspends, a signal is delivered, and Resume causes the activity to
// re-run and complete.
func TestWaitSpike(t *testing.T) {
	const topic = "cb-123"

	wf, err := New(Options{
		Name: "wait-spike",
		Steps: []*Step{
			{
				Name:     "await",
				Activity: "awaiter",
				Store:    "reply",
			},
		},
		Outputs: []*Output{
			{Name: "reply", Variable: "reply"},
		},
	})
	require.NoError(t, err)

	var invocations int32
	signals := NewMemorySignalStore()
	// Use our own in-package memory checkpointer to avoid importing workflowtest.
	cp := newSpikeMemoryCheckpointer()

	awaiter := NewActivityFunction("awaiter", func(ctx Context, params map[string]any) (any, error) {
		atomic.AddInt32(&invocations, 1)
		reply, err := Wait(ctx, topic, time.Minute)
		if err != nil {
			return nil, err
		}
		return reply, nil
	})

	// --- First run: should suspend on Wait ---
	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter},
		Checkpointer: cp,
		SignalStore:  signals,
	})
	require.NoError(t, err)

	executionID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.NotNil(t, res1)
	require.Equal(t, ExecutionStatusSuspended, res1.Status,
		"first run should hard-suspend on workflow.Wait")
	require.NotNil(t, res1.Suspension, "Suspension should be populated")
	require.Equal(t, SuspensionReasonWaitingSignal, res1.Suspension.Reason)
	require.Contains(t, res1.Suspension.Topics, topic)
	require.Equal(t, int32(1), atomic.LoadInt32(&invocations),
		"activity should run exactly once before suspension")

	// Confirm the path was parked on the right topic.
	pathStates := exec1.state.GetPathStates()
	var parked *PathState
	for _, ps := range pathStates {
		if ps.Status == ExecutionStatusSuspended {
			parked = ps
			break
		}
	}
	require.NotNil(t, parked, "expected a parked path in checkpoint")
	require.NotNil(t, parked.Wait)
	require.Equal(t, WaitKindSignal, parked.Wait.Kind)
	require.Equal(t, topic, parked.Wait.Topic)
	require.Equal(t, "await", parked.CurrentStep)

	// --- Send a signal ---
	require.NoError(t, signals.Send(ctx, executionID, topic, "hello-from-callback"))

	// --- Second execution: Resume using prior execution ID ---
	// Reuse the prior execution ID so the activity context sees the same
	// executionID that the signal was keyed on.
	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  executionID,
	})
	require.NoError(t, err)

	res2, err := exec2.ExecuteOrResume(ctx, executionID)
	require.NoError(t, err)
	require.NotNil(t, res2)
	require.Equal(t, ExecutionStatusCompleted, res2.Status,
		"resumed execution should complete after signal delivery")
	require.Equal(t, "hello-from-callback", res2.Outputs["reply"])
	require.Equal(t, int32(2), atomic.LoadInt32(&invocations),
		"activity should have re-run exactly once on resume (total = 2)")
}

// TestWaitSignalAlreadyPresent covers the cheap extra case: if the signal
// is delivered *before* Wait is called, the first call returns immediately
// and the activity only runs once.
func TestWaitSignalAlreadyPresent(t *testing.T) {
	const topic = "early-bird"

	wf, err := New(Options{
		Name: "wait-spike-early",
		Steps: []*Step{
			{
				Name:     "await",
				Activity: "awaiter",
				Store:    "reply",
			},
		},
		Outputs: []*Output{
			{Name: "reply", Variable: "reply"},
		},
	})
	require.NoError(t, err)

	var invocations int32
	signals := NewMemorySignalStore()

	awaiter := NewActivityFunction("awaiter", func(ctx Context, params map[string]any) (any, error) {
		atomic.AddInt32(&invocations, 1)
		return Wait(ctx, topic, time.Minute)
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:    wf,
		Activities:  []Activity{awaiter},
		SignalStore: signals,
	})
	require.NoError(t, err)

	// Pre-deliver the signal before the workflow starts.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, signals.Send(ctx, exec.ID(), topic, 42))

	res, err := exec.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res.Status)
	require.Equal(t, 42, res.Outputs["reply"])
	require.Equal(t, int32(1), atomic.LoadInt32(&invocations))
}

// --- spike-local memory checkpointer (so the test stays in-package) ---

type spikeMemoryCheckpointer struct {
	checkpoints map[string]*Checkpoint
}

func newSpikeMemoryCheckpointer() *spikeMemoryCheckpointer {
	return &spikeMemoryCheckpointer{checkpoints: map[string]*Checkpoint{}}
}

func (m *spikeMemoryCheckpointer) SaveCheckpoint(ctx context.Context, cp *Checkpoint) error {
	m.checkpoints[cp.ExecutionID] = cp
	return nil
}

func (m *spikeMemoryCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
	cp, ok := m.checkpoints[executionID]
	if !ok {
		return nil, nil
	}
	return cp, nil
}

func (m *spikeMemoryCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	delete(m.checkpoints, executionID)
	return nil
}
