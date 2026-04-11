package workflow

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestSleepStepSuspendsAndResumes covers the basic durable-sleep
// cycle: a Sleep step hard-suspends the execution, the caller waits
// past WakeAt, and a fresh NewExecution instance resumes into the
// same ID and completes.
func TestSleepStepSuspendsAndResumes(t *testing.T) {
	const duration = 50 * time.Millisecond

	var afterInvocations int32
	after := NewActivityFunction("after", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&afterInvocations, 1)
		return "done", nil
	})

	wf, err := New(Options{
		Name: "sleep-basic",
		Steps: []*Step{
			{
				Name:  "nap",
				Sleep: &SleepConfig{Duration: duration},
				Next:  []*Edge{{Step: "after"}},
			},
			{
				Name:     "after",
				Activity: "after",
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, wf.Validate())

	cp := newSpikeMemoryCheckpointer()
	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{after},
		Checkpointer: cp,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status, "should hard-suspend on Sleep")
	require.NotNil(t, res1.Suspension)
	require.Equal(t, SuspensionReasonSleeping, res1.Suspension.Reason)
	require.Len(t, res1.Suspension.SuspendedBranches, 1)
	require.False(t, res1.Suspension.WakeAt.IsZero(), "WakeAt should be populated")
	require.Equal(t, int32(0), atomic.LoadInt32(&afterInvocations))

	// Checkpoint carries the sleep wait state.
	loaded, _ := cp.LoadCheckpoint(ctx, execID)
	ps := loaded.BranchStates["main"]
	require.NotNil(t, ps.Wait)
	require.Equal(t, WaitKindSleep, ps.Wait.Kind)
	require.False(t, ps.Wait.WakeAt.IsZero())
	require.Equal(t, duration, ps.Wait.Timeout)

	// Wait past the deadline, then resume: sleep should complete and
	// the successor step should run.
	time.Sleep(duration + 20*time.Millisecond)

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{after},
		Checkpointer: cp,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res2.Status)
	require.Equal(t, int32(1), atomic.LoadInt32(&afterInvocations))
}

// TestSleepResumeBeforeDeadlineReSuspends verifies that resuming a
// sleeping execution before the deadline causes it to re-suspend with
// the same absolute WakeAt — i.e., the clock is preserved across
// replays.
func TestSleepResumeBeforeDeadlineReSuspends(t *testing.T) {
	// Long enough duration that a prompt resume is still before the
	// deadline.
	const duration = 10 * time.Second

	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return "ok", nil })

	wf, err := New(Options{
		Name: "sleep-early-resume",
		Steps: []*Step{
			{Name: "nap", Sleep: &SleepConfig{Duration: duration}, Next: []*Edge{{Step: "after"}}},
			{Name: "after", Activity: "noop"},
		},
	})
	require.NoError(t, err)

	cp := newSpikeMemoryCheckpointer()
	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop},
		Checkpointer: cp,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)
	originalWakeAt := res1.Suspension.WakeAt

	// Resume promptly (well before WakeAt): should re-suspend at the
	// same absolute WakeAt.
	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop},
		Checkpointer: cp,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res2.Status, "should re-suspend before WakeAt")
	require.Equal(t, SuspensionReasonSleeping, res2.Suspension.Reason)
	require.True(t, res2.Suspension.WakeAt.Equal(originalWakeAt),
		"WakeAt should be preserved across replay (got %v, want %v)",
		res2.Suspension.WakeAt, originalWakeAt)
}

// TestSleepPauseFreezesClock covers FR-19: when a sleeping branch is
// paused, the sleep clock freezes and the pause duration does not
// count against sleep time. On unpause, WakeAt is rebased.
func TestSleepPauseFreezesClock(t *testing.T) {
	const duration = 200 * time.Millisecond

	var afterInvocations int32
	after := NewActivityFunction("after", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&afterInvocations, 1)
		return "done", nil
	})

	wf, err := New(Options{
		Name: "sleep-pause",
		Steps: []*Step{
			{Name: "nap", Sleep: &SleepConfig{Duration: duration}, Next: []*Edge{{Step: "after"}}},
			{Name: "after", Activity: "after"},
		},
	})
	require.NoError(t, err)

	cp := newSpikeMemoryCheckpointer()
	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{after},
		Checkpointer: cp,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First run: enter sleep, suspend.
	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)
	originalWakeAt := res1.Suspension.WakeAt

	// Pause the branch via the checkpoint helper (branch is not loaded).
	require.NoError(t, PauseBranchInCheckpoint(ctx, cp, execID, "main", "investigate"))

	// Verify the WakeAt is cleared and Remaining is populated.
	loaded, _ := cp.LoadCheckpoint(ctx, execID)
	ps := loaded.BranchStates["main"]
	require.True(t, ps.PauseRequested)
	require.True(t, ps.Wait.WakeAt.IsZero(), "paused sleep should have cleared WakeAt")
	require.Greater(t, ps.Wait.Remaining, time.Duration(0), "Remaining should be populated")

	// Sleep longer than the original sleep duration. If the clock is
	// frozen, the sleep should NOT have completed.
	time.Sleep(duration + 50*time.Millisecond)

	// Unpause — WakeAt is rebased to now + remaining.
	require.NoError(t, UnpauseBranchInCheckpoint(ctx, cp, execID, "main"))

	loaded, _ = cp.LoadCheckpoint(ctx, execID)
	ps = loaded.BranchStates["main"]
	require.False(t, ps.PauseRequested)
	require.False(t, ps.Wait.WakeAt.IsZero(), "unpause should restore WakeAt")
	require.Equal(t, time.Duration(0), ps.Wait.Remaining)
	require.True(t, ps.Wait.WakeAt.After(originalWakeAt),
		"unpause should extend WakeAt past the original deadline (pause duration > sleep duration)")

	// Resume: the branch should re-suspend on the rebased wait because
	// the sleep hasn't actually elapsed yet.
	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{after},
		Checkpointer: cp,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res2.Status, "should re-suspend on rebased wait")
	require.Equal(t, int32(0), atomic.LoadInt32(&afterInvocations))

	// Wait past the new deadline then resume to completion.
	time.Sleep(duration + 50*time.Millisecond)
	exec3, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{after},
		Checkpointer: cp,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res3, err := exec3.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res3.Status)
	require.Equal(t, int32(1), atomic.LoadInt32(&afterInvocations))
}

// TestSleepStepValidation verifies the sleep step is rejected at
// Validate time when Duration is zero or negative.
func TestSleepStepValidation(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-sleep",
		Steps: []*Step{
			{Name: "nap", Sleep: &SleepConfig{Duration: 0}, Next: []*Edge{{Step: "after"}}},
			{Name: "after", Activity: "noop"},
		},
	})
	require.NoError(t, err)
	err = wf.Validate()
	require.Error(t, err)
}

// TestSleepWaitStateJSONRoundTrip confirms the new Remaining field
// round-trips cleanly and is omitted from JSON when zero.
func TestSleepWaitStateJSONRoundTrip(t *testing.T) {
	ws := &WaitState{
		Kind:      WaitKindSleep,
		Timeout:   time.Second,
		Remaining: 500 * time.Millisecond,
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)

	var restored WaitState
	require.NoError(t, json.Unmarshal(data, &restored))
	require.Equal(t, WaitKindSleep, restored.Kind)
	require.Equal(t, 500*time.Millisecond, restored.Remaining)
	require.True(t, restored.WakeAt.IsZero())

	// Zero Remaining is omitted (additive field stays backward-compat).
	ws.Remaining = 0
	ws.WakeAt = time.Now()
	data, err = json.Marshal(ws)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"remaining"`)
}

// TestSleepPastDeadlineImmediateWake verifies that if Resume happens
// after WakeAt has already passed, the sleep handler returns
// immediately and the branch advances without re-suspending.
func TestSleepPastDeadlineImmediateWake(t *testing.T) {
	const duration = 20 * time.Millisecond

	var afterInvocations int32
	after := NewActivityFunction("after", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&afterInvocations, 1)
		return "done", nil
	})

	wf, err := New(Options{
		Name: "sleep-immediate",
		Steps: []*Step{
			{Name: "nap", Sleep: &SleepConfig{Duration: duration}, Next: []*Edge{{Step: "after"}}},
			{Name: "after", Activity: "after"},
		},
	})
	require.NoError(t, err)

	cp := newSpikeMemoryCheckpointer()
	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{after},
		Checkpointer: cp,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = exec1.Execute(ctx)
	require.NoError(t, err)

	// Sleep well past the deadline.
	time.Sleep(duration + 100*time.Millisecond)

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{after},
		Checkpointer: cp,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res.Status)
	require.Equal(t, int32(1), atomic.LoadInt32(&afterInvocations))
}
