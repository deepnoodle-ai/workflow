package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestWaitSpike exercises the end-to-end durable-wait branch: an activity
// calls workflow.Wait, unwinds, the execution hard-suspends, a signal is
// delivered, and Resume causes the activity to re-run and complete.
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

	// Confirm the branch was parked on the right topic.
	branchStates := exec1.state.GetBranchStates()
	var parked *BranchState
	for _, ps := range branchStates {
		if ps.Status == ExecutionStatusSuspended {
			parked = ps
			break
		}
	}
	require.NotNil(t, parked, "expected a parked branch in checkpoint")
	require.NotNil(t, parked.Wait)
	require.Equal(t, WaitKindSignal, parked.Wait.Kind)
	require.Equal(t, topic, parked.Wait.Topic)
	require.Equal(t, "await", parked.CurrentStep)

	// --- Send a signal ---
	require.NoError(t, signals.Send(ctx, executionID, topic, "hello-from-callback"))

	// --- Second execution: a fresh NewExecution instance (simulating
	//     process death + restart) resumes into the same ID. ---
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

// TestWaitImperativeTimeoutNoCatch: an activity calls workflow.Wait with a
// short timeout, no signal arrives, and on replay past the deadline the
// wait returns ErrWaitTimeout. The step has no catch handler, so the
// execution ends in Failed with a timeout WorkflowError.
func TestWaitImperativeTimeoutNoCatch(t *testing.T) {
	const topic = "never-arrives"

	wf, err := New(Options{
		Name: "wait-timeout",
		Steps: []*Step{
			{
				Name:     "await",
				Activity: "awaiter",
				Store:    "reply",
			},
		},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()

	awaiter := NewActivityFunction("awaiter", func(ctx Context, params map[string]any) (any, error) {
		return Wait(ctx, topic, 10*time.Millisecond)
	})

	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter},
		Checkpointer: cp,
		SignalStore:  signals,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)

	// Wait past the deadline, then resume. The second invocation of Wait
	// should observe the expired deadline and return ErrWaitTimeout.
	time.Sleep(30 * time.Millisecond)

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)

	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusFailed, res2.Status,
		"timeout with no catch should fail the execution")
	require.NotNil(t, res2.Error)
}

// TestWaitImperativeTimeoutWithCatch: an activity's wait times out, and
// a catch handler on the step routes the branch to a recovery step. The
// execution completes through the recovery step.
func TestWaitImperativeTimeoutWithCatch(t *testing.T) {
	const topic = "nope"

	wf, err := New(Options{
		Name: "wait-timeout-catch",
		Steps: []*Step{
			{
				Name:     "await",
				Activity: "awaiter",
				Store:    "reply",
				Catch: []*CatchConfig{
					{ErrorEquals: []string{ErrorTypeTimeout}, Next: "recover", Store: "why"},
				},
			},
			{
				Name:     "recover",
				Activity: "recoverer",
				Store:    "result",
			},
		},
		Outputs: []*Output{{Name: "result", Variable: "result"}},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()

	awaiter := NewActivityFunction("awaiter", func(ctx Context, params map[string]any) (any, error) {
		return Wait(ctx, topic, 10*time.Millisecond)
	})
	recoverer := NewActivityFunction("recoverer", func(ctx Context, params map[string]any) (any, error) {
		return "recovered", nil
	})

	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter, recoverer},
		Checkpointer: cp,
		SignalStore:  signals,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)

	time.Sleep(30 * time.Millisecond)

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter, recoverer},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)

	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res2.Status)
	require.Equal(t, "recovered", res2.Outputs["result"])
}

// TestWaitCancelDuringWait: an activity calls workflow.Wait with a
// context that's already cancelled. Wait observes ctx.Err() and returns
// immediately with that error.
func TestWaitCancelDuringWait(t *testing.T) {
	wf, err := New(Options{
		Name: "wait-cancel",
		Steps: []*Step{
			{Name: "await", Activity: "awaiter"},
		},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()

	awaiter := NewActivityFunction("awaiter", func(ctx Context, params map[string]any) (any, error) {
		return Wait(ctx, "any", time.Minute)
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:    wf,
		Activities:  []Activity{awaiter},
		SignalStore: signals,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := exec.Execute(ctx)
	// A cancelled context at the top of Execute may surface as a bare
	// ctx.Err() before any branch-state bookkeeping happens. Either way,
	// the execution must not complete successfully.
	if err != nil {
		require.True(t, errors.Is(err, context.Canceled))
		return
	}
	require.NotEqual(t, ExecutionStatusCompleted, res.Status,
		"cancelled execution must not complete successfully")
}

// TestWaitSignalDeclarativeStep exercises the declarative WaitSignal step
// end-to-end including Risor-templated topics that depend on branch state.
func TestWaitSignalDeclarativeStep(t *testing.T) {
	wf, err := New(Options{
		Name: "wait-signal-decl",
		State: map[string]any{
			"request_id": "req-42",
		},
		Steps: []*Step{
			{
				Name: "wait",
				WaitSignal: &WaitSignalConfig{
					Topic:   "callback-${state.request_id}",
					Timeout: time.Minute,
					Store:   "reply",
				},
			},
		},
		Outputs: []*Output{
			{Name: "reply", Variable: "reply"},
		},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()

	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return nil, nil })},
		Checkpointer: cp,
		SignalStore:  signals,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)
	require.NotNil(t, res1.Suspension)
	require.Contains(t, res1.Suspension.Topics, "callback-req-42")

	// Deliver the signal on the resolved topic and resume.
	require.NoError(t, signals.Send(ctx, execID, "callback-req-42", map[string]any{"ok": true}))

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return nil, nil })},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)

	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res2.Status)
	require.Equal(t, map[string]any{"ok": true}, res2.Outputs["reply"])
}

// TestWaitSignalDeclarativeOnTimeoutRouting: a declarative WaitSignal
// step with OnTimeout routes to the timeout successor when the deadline
// passes, instead of failing the step.
func TestWaitSignalDeclarativeOnTimeoutRouting(t *testing.T) {
	wf, err := New(Options{
		Name: "wait-signal-on-timeout",
		Steps: []*Step{
			{
				Name: "wait",
				WaitSignal: &WaitSignalConfig{
					Topic:     "nobody-home",
					Timeout:   10 * time.Millisecond,
					OnTimeout: "timed_out",
				},
			},
			{
				Name:     "timed_out",
				Activity: "mark_timeout",
				Store:    "outcome",
			},
		},
		Outputs: []*Output{
			{Name: "outcome", Variable: "outcome"},
		},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()

	mark := NewActivityFunction("mark_timeout", func(ctx Context, p map[string]any) (any, error) {
		return "timed-out", nil
	})

	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{mark},
		Checkpointer: cp,
		SignalStore:  signals,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)

	time.Sleep(25 * time.Millisecond)

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{mark},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)

	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res2.Status,
		"OnTimeout route should produce a successful terminal status")
	require.Equal(t, "timed-out", res2.Outputs["outcome"])
}

// TestWaitMultiBranch: one branch waits on a signal while a sibling branch
// runs to completion. The execution ends Suspended because the waiting
// branch is still parked.
func TestWaitMultiBranch(t *testing.T) {
	const topic = "sibling-callback"

	wf, err := New(Options{
		Name: "multi-branch-wait",
		Steps: []*Step{
			{
				Name:     "fanout",
				Activity: "noop",
				Next: []*Edge{
					{Step: "quick", BranchName: "quick"},
					{Step: "slow", BranchName: "slow"},
				},
			},
			{
				Name:     "quick",
				Activity: "noop",
			},
			{
				Name:     "slow",
				Activity: "awaiter",
			},
		},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()

	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return "ok", nil })
	awaiter := NewActivityFunction("awaiter", func(ctx Context, p map[string]any) (any, error) {
		return Wait(ctx, topic, time.Minute)
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:    wf,
		Activities:  []Activity{noop, awaiter},
		SignalStore: signals,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := exec.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res.Status,
		"execution should suspend while the slow branch is parked")
	require.NotNil(t, res.Suspension)
	require.Len(t, res.Suspension.SuspendedBranches, 1)
	require.Equal(t, topic, res.Suspension.SuspendedBranches[0].Topic)

	// The quick branch should have completed before suspension.
	var quickCompleted bool
	for _, ps := range exec.state.GetBranchStates() {
		if ps.ID == "quick" && ps.Status == ExecutionStatusCompleted {
			quickCompleted = true
		}
	}
	require.True(t, quickCompleted, "quick branch should have completed before suspension")
}

// TestWaitRetryBypass: a step with an aggressive retry configuration
// should not retry a wait-unwind. The activity runs exactly once
// pre-suspension.
func TestWaitRetryBypass(t *testing.T) {
	wf, err := New(Options{
		Name: "retry-bypass",
		Steps: []*Step{
			{
				Name:     "await",
				Activity: "awaiter",
				Retry: []*RetryConfig{
					{ErrorEquals: []string{ErrorTypeAll}, MaxRetries: 5, BaseDelay: time.Millisecond},
				},
			},
		},
	})
	require.NoError(t, err)

	var invocations int32
	signals := NewMemorySignalStore()

	awaiter := NewActivityFunction("awaiter", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&invocations, 1)
		return Wait(ctx, "no-signal", time.Minute)
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:    wf,
		Activities:  []Activity{awaiter},
		SignalStore: signals,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := exec.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res.Status)
	require.Equal(t, int32(1), atomic.LoadInt32(&invocations),
		"wait-unwind must not consume retry budget")
}

// TestWaitCatchBypass: a step with a catch-all handler should not route
// a wait-unwind to the handler. The execution should suspend.
func TestWaitCatchBypass(t *testing.T) {
	wf, err := New(Options{
		Name: "catch-bypass",
		Steps: []*Step{
			{
				Name:     "await",
				Activity: "awaiter",
				Catch: []*CatchConfig{
					{ErrorEquals: []string{ErrorTypeAll}, Next: "handle"},
				},
			},
			{
				Name:     "handle",
				Activity: "noop",
			},
		},
	})
	require.NoError(t, err)

	var handleCalled int32
	signals := NewMemorySignalStore()

	awaiter := NewActivityFunction("awaiter", func(ctx Context, p map[string]any) (any, error) {
		return Wait(ctx, "nope", time.Minute)
	})
	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&handleCalled, 1)
		return nil, nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:    wf,
		Activities:  []Activity{awaiter, noop},
		SignalStore: signals,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := exec.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res.Status)
	require.Equal(t, int32(0), atomic.LoadInt32(&handleCalled),
		"catch handler must not run for a wait-unwind")
}

// TestSignalStoreFIFO verifies MemorySignalStore preserves per-topic
// FIFO ordering and exactly-once consumption.
func TestSignalStoreFIFO(t *testing.T) {
	store := NewMemorySignalStore()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, store.Send(ctx, "exec", "topic", i))
	}
	// Also queue on a different topic; shouldn't interfere.
	require.NoError(t, store.Send(ctx, "exec", "other", "x"))

	for i := 0; i < 3; i++ {
		sig, err := store.Receive(ctx, "exec", "topic")
		require.NoError(t, err)
		require.NotNil(t, sig, fmt.Sprintf("signal %d must exist", i))
		require.Equal(t, i, sig.Payload)
	}
	// Drained.
	sig, err := store.Receive(ctx, "exec", "topic")
	require.NoError(t, err)
	require.Nil(t, sig)
}

// TestWaitStateJSONRoundTrip ensures WaitState survives a checkpoint
// round-trip via JSON and that unknown kinds are rejected on unmarshal.
func TestWaitStateJSONRoundTrip(t *testing.T) {
	ws := newSignalWait("topic-a", 30*time.Second)
	data, err := json.Marshal(ws)
	require.NoError(t, err)

	var decoded WaitState
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, WaitKindSignal, decoded.Kind)
	require.Equal(t, "topic-a", decoded.Topic)
	require.Equal(t, 30*time.Second, decoded.Timeout)

	// Reject unknown kinds.
	err = json.Unmarshal([]byte(`{"kind":"nonsense"}`), &decoded)
	require.Error(t, err)
}

// --- in-package helpers ---

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

// LoadCheckpoint returns a deep copy so callers cannot mutate the
// stored checkpoint — otherwise a resumed execution would share
// BranchState maps with any other test code that previously loaded the
// same checkpoint, and wholly-unrelated mutations could bleed across.
func (m *spikeMemoryCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
	cp, ok := m.checkpoints[executionID]
	if !ok {
		return nil, nil
	}
	return deepCopyCheckpointForTests(cp), nil
}

func (m *spikeMemoryCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	delete(m.checkpoints, executionID)
	return nil
}

// deepCopyCheckpointForTests round-trips a checkpoint through JSON to
// produce a fully isolated copy, mirroring workflowtest.MemoryCheckpointer.
func deepCopyCheckpointForTests(cp *Checkpoint) *Checkpoint {
	data, err := json.Marshal(cp)
	if err != nil {
		panic("spikeMemoryCheckpointer: marshal failed: " + err.Error())
	}
	var out Checkpoint
	if err := json.Unmarshal(data, &out); err != nil {
		panic("spikeMemoryCheckpointer: unmarshal failed: " + err.Error())
	}
	return &out
}
