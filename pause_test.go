package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestPauseExternalLiveBranch exercises the basic external-pause flow: a
// running execution is paused mid-run, exits cleanly with
// Status=Paused, then unpauses and resumes to completion.
func TestPauseExternalLiveBranch(t *testing.T) {
	// The activity blocks on a gate so the test can pause the branch
	// while it is running. After unpause, the gate releases.
	gate := make(chan struct{})
	var invocations int32
	blocking := NewActivityFunction("blocking", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&invocations, 1)
		select {
		case <-gate:
			return "done", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) {
		return "ok", nil
	})

	wf, err := New(Options{
		Name: "pause-external",
		Steps: []*Step{
			{
				Name:     "wait-here",
				Activity: "blocking",
				Next:     []*Edge{{Step: "after"}},
			},
			{
				Name:     "after",
				Activity: "noop",
			},
		},
	})
	require.NoError(t, err)

	cp := newSpikeMemoryCheckpointer()
	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{blocking, noop},
		Checkpointer: cp,
	})
	require.NoError(t, err)
	executionID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run the execution in a goroutine so we can pause it mid-run.
	var (
		res1   *ExecutionResult
		runErr error
		done   = make(chan struct{})
	)
	go func() {
		defer close(done)
		res1, runErr = exec1.Execute(ctx)
	}()

	// Wait for the blocking activity to start before pausing.
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&invocations) == 1
	}, 2*time.Second, 10*time.Millisecond, "activity should start")

	// Pause the main branch while the activity is running.
	require.NoError(t, exec1.PauseBranch("main", "operator investigation"))

	// Release the gate: the activity completes, the branch stores its
	// output, and at the next step boundary the branch observes the
	// pause flag and exits.
	close(gate)

	<-done
	require.NoError(t, runErr)
	require.NotNil(t, res1)
	require.Equal(t, ExecutionStatusPaused, res1.Status, "execution should end Paused")
	require.NotNil(t, res1.Suspension)
	require.Equal(t, SuspensionReasonPaused, res1.Suspension.Reason)
	require.Len(t, res1.Suspension.SuspendedBranches, 1)
	require.Equal(t, "main", res1.Suspension.SuspendedBranches[0].BranchID)
	require.Equal(t, "after", res1.Suspension.SuspendedBranches[0].StepName,
		"pause should park the branch at the next step boundary (after wait-here completed)")

	// Confirm the checkpoint carries the pause flag.
	loaded, err := cp.LoadCheckpoint(ctx, executionID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	ps, ok := loaded.BranchStates["main"]
	require.True(t, ok)
	require.True(t, ps.PauseRequested)
	require.Equal(t, "operator investigation", ps.PauseReason)
	require.Equal(t, ExecutionStatusPaused, ps.Status)

	// A fresh execution that Resumes without unpausing should still
	// see Status=Paused because the sticky flag re-parks the branch.
	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{blocking, noop},
		Checkpointer: cp,
		ExecutionID:  executionID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, executionID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusPaused, res2.Status,
		"resuming without clearing the pause flag should re-park")

	// Now unpause via the checkpoint helper and resume: the workflow
	// should complete.
	require.NoError(t, UnpauseBranchInCheckpoint(ctx, cp, executionID, "main"))

	exec3, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{blocking, noop},
		Checkpointer: cp,
		ExecutionID:  executionID,
	})
	require.NoError(t, err)
	res3, err := exec3.ExecuteOrResume(ctx, executionID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res3.Status,
		"resuming after unpause should complete")
}

// TestPauseBranchInCheckpointIdempotent confirms pause helper is a no-op
// against an already-paused branch and returns ErrBranchNotFound when the
// branch ID doesn't exist.
func TestPauseBranchInCheckpointIdempotent(t *testing.T) {
	cp := newSpikeMemoryCheckpointer()

	// Save a checkpoint with a single branch in Running state.
	ctx := context.Background()
	execID := "exec-test"
	initial := &Checkpoint{
		ID:           "cp1",
		ExecutionID:  execID,
		WorkflowName: "test",
		Status:       string(ExecutionStatusRunning),
		BranchStates: map[string]*BranchState{
			"main": {
				ID:          "main",
				Status:      ExecutionStatusRunning,
				CurrentStep: "step1",
				StepOutputs: map[string]any{},
				Variables:   map[string]any{},
			},
		},
	}
	require.NoError(t, cp.SaveCheckpoint(ctx, initial))

	// First pause: flag goes true.
	require.NoError(t, PauseBranchInCheckpoint(ctx, cp, execID, "main", "manual"))
	loaded, _ := cp.LoadCheckpoint(ctx, execID)
	require.True(t, loaded.BranchStates["main"].PauseRequested)
	require.Equal(t, "manual", loaded.BranchStates["main"].PauseReason)

	// Second pause: no-op (idempotent).
	require.NoError(t, PauseBranchInCheckpoint(ctx, cp, execID, "main", "manual"))

	// Unpause clears the flag.
	require.NoError(t, UnpauseBranchInCheckpoint(ctx, cp, execID, "main"))
	loaded, _ = cp.LoadCheckpoint(ctx, execID)
	require.False(t, loaded.BranchStates["main"].PauseRequested)
	require.Equal(t, "", loaded.BranchStates["main"].PauseReason)

	// Unknown branch returns ErrBranchNotFound.
	err := PauseBranchInCheckpoint(ctx, cp, execID, "nope", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBranchNotFound)

	// Unknown execution returns ErrNoCheckpoint.
	err = PauseBranchInCheckpoint(ctx, cp, "missing", "main", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoCheckpoint)
}

// TestPauseDeclarativeStep verifies that a `Pause` step parks the branch
// with Status=Paused, advances CurrentStep past the pause step, and
// resumes at the successor after unpause.
func TestPauseDeclarativeStep(t *testing.T) {
	var (
		beforeInvocations int32
		afterInvocations  int32
	)
	before := NewActivityFunction("before", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&beforeInvocations, 1)
		return "before-ok", nil
	})
	after := NewActivityFunction("after", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&afterInvocations, 1)
		return "after-ok", nil
	})

	wf, err := New(Options{
		Name: "pause-declarative",
		Steps: []*Step{
			{
				Name:     "before",
				Activity: "before",
				Next:     []*Edge{{Step: "gate"}},
			},
			{
				Name:  "gate",
				Pause: &PauseConfig{Reason: "awaiting approval"},
				Next:  []*Edge{{Step: "after"}},
			},
			{
				Name:     "after",
				Activity: "after",
			},
		},
	})
	require.NoError(t, err)

	cp := newSpikeMemoryCheckpointer()
	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{before, after},
		Checkpointer: cp,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusPaused, res1.Status, "should end Paused at the gate")
	require.NotNil(t, res1.Suspension)
	require.Equal(t, SuspensionReasonPaused, res1.Suspension.Reason)
	require.Equal(t, int32(1), atomic.LoadInt32(&beforeInvocations))
	require.Equal(t, int32(0), atomic.LoadInt32(&afterInvocations),
		"after step should not run before unpause")

	// CurrentStep should be "after" (already advanced past the gate).
	loaded, _ := cp.LoadCheckpoint(ctx, execID)
	ps := loaded.BranchStates["main"]
	require.Equal(t, "after", ps.CurrentStep,
		"pause step must advance CurrentStep past itself")
	require.True(t, ps.PauseRequested)
	require.Equal(t, "awaiting approval", ps.PauseReason)

	// Unpause and resume: after should execute.
	require.NoError(t, UnpauseBranchInCheckpoint(ctx, cp, execID, "main"))

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{before, after},
		Checkpointer: cp,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res2.Status)
	require.Equal(t, int32(1), atomic.LoadInt32(&beforeInvocations),
		"before step must not re-run on resume")
	require.Equal(t, int32(1), atomic.LoadInt32(&afterInvocations))
}

// TestPauseMultiBranch: one branch paused while a sibling branch runs to
// completion. The execution ends Paused because the sibling completed
// but the paused branch is still parked.
func TestPauseMultiBranch(t *testing.T) {
	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) {
		return "ok", nil
	})

	wf, err := New(Options{
		Name: "pause-multipath",
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
				Name:  "slow",
				Pause: &PauseConfig{Reason: "wait for operator"},
				Next:  []*Edge{{Step: "slow-next"}},
			},
			{
				Name:     "slow-next",
				Activity: "noop",
			},
		},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{noop},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := exec.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusPaused, res.Status,
		"execution should end Paused while the slow branch is parked")
	require.NotNil(t, res.Suspension)
	require.Equal(t, SuspensionReasonPaused, res.Suspension.Reason)
	require.Len(t, res.Suspension.SuspendedBranches, 1)
	require.Equal(t, "slow", res.Suspension.SuspendedBranches[0].BranchID)

	// The quick branch should have completed before the execution ended.
	var quickCompleted bool
	for _, ps := range exec.state.GetBranchStates() {
		if ps.ID == "quick" && ps.Status == ExecutionStatusCompleted {
			quickCompleted = true
		}
	}
	require.True(t, quickCompleted, "quick branch should have completed")
}

// TestPauseBranchNotFound confirms PauseBranch/UnpauseBranch return
// ErrBranchNotFound for unknown branch IDs.
func TestPauseBranchNotFound(t *testing.T) {
	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) {
		return "ok", nil
	})
	wf, err := New(Options{
		Name:  "pause-err",
		Steps: []*Step{{Name: "step1", Activity: "noop"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{noop},
	})
	require.NoError(t, err)

	err = exec.PauseBranch("nope", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBranchNotFound)

	err = exec.UnpauseBranch("nope")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBranchNotFound)
}

// TestPauseClearedBeforeBoundary verifies that pausing then unpausing
// a branch before it hits a step boundary results in the branch continuing
// normally — the race is a benign no-op.
func TestPauseClearedBeforeBoundary(t *testing.T) {
	// The activity synchronizes with the test: signals it's started,
	// then waits for a release signal, then completes.
	started := make(chan struct{})
	release := make(chan struct{})
	activity := NewActivityFunction("sync", func(ctx Context, p map[string]any) (any, error) {
		close(started)
		select {
		case <-release:
			return "ok", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})

	wf, err := New(Options{
		Name: "pause-cleared",
		Steps: []*Step{
			{Name: "only", Activity: "sync"},
		},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{activity},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	var res *ExecutionResult
	var runErr error
	go func() {
		defer wg.Done()
		res, runErr = exec.Execute(ctx)
	}()

	// Wait for the activity to start.
	<-started

	// Pause + unpause before the step boundary is hit.
	require.NoError(t, exec.PauseBranch("main", "changed my mind"))
	require.NoError(t, exec.UnpauseBranch("main"))

	// Release the activity and let the branch complete.
	close(release)
	wg.Wait()

	require.NoError(t, runErr)
	require.Equal(t, ExecutionStatusCompleted, res.Status,
		"unpause before boundary should let the branch complete normally")
}

// TestPauseStateJSONRoundTrip verifies the PauseRequested/PauseReason
// fields on BranchState round-trip cleanly through JSON.
func TestPauseStateJSONRoundTrip(t *testing.T) {
	original := &BranchState{
		ID:             "main",
		Status:         ExecutionStatusPaused,
		CurrentStep:    "gate",
		StepOutputs:    map[string]any{},
		Variables:      map[string]any{},
		PauseRequested: true,
		PauseReason:    "manual hold",
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored BranchState
	require.NoError(t, json.Unmarshal(data, &restored))
	require.True(t, restored.PauseRequested)
	require.Equal(t, "manual hold", restored.PauseReason)
	require.Equal(t, ExecutionStatusPaused, restored.Status)
}

// TestPauseStepValidationRequiresNext verifies that Workflow.Validate
// flags a pause step with no Next edges.
func TestPauseStepValidationRequiresNext(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-pause",
		Steps: []*Step{
			{Name: "start", Activity: "noop", Next: []*Edge{{Step: "gate"}}},
			{Name: "gate", Pause: &PauseConfig{}},
		},
	})
	require.NoError(t, err, "New should accept the workflow (validation is explicit)")

	err = wf.Validate()
	require.Error(t, err)
	var verr *ValidationError
	require.True(t, errors.As(err, &verr))
	var found bool
	for _, p := range verr.Problems {
		if p.Step == "gate" && p.Message != "" {
			found = true
		}
	}
	require.True(t, found, "expected a pause-related validation problem, got: %v", verr.Problems)
}
