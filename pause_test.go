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

// TestPauseExternalLivePath exercises the basic external-pause flow: a
// running execution is paused mid-run, exits cleanly with
// Status=Paused, then unpauses and resumes to completion.
func TestPauseExternalLivePath(t *testing.T) {
	// The activity blocks on a gate so the test can pause the path
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

	// Pause the main path while the activity is running.
	require.NoError(t, exec1.PausePath("main", "operator investigation"))

	// Release the gate: the activity completes, the path stores its
	// output, and at the next step boundary the path observes the
	// pause flag and exits.
	close(gate)

	<-done
	require.NoError(t, runErr)
	require.NotNil(t, res1)
	require.Equal(t, ExecutionStatusPaused, res1.Status, "execution should end Paused")
	require.NotNil(t, res1.Suspension)
	require.Equal(t, SuspensionReasonPaused, res1.Suspension.Reason)
	require.Len(t, res1.Suspension.SuspendedPaths, 1)
	require.Equal(t, "main", res1.Suspension.SuspendedPaths[0].PathID)
	require.Equal(t, "after", res1.Suspension.SuspendedPaths[0].StepName,
		"pause should park the path at the next step boundary (after wait-here completed)")

	// Confirm the checkpoint carries the pause flag.
	loaded, err := cp.LoadCheckpoint(ctx, executionID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	ps, ok := loaded.PathStates["main"]
	require.True(t, ok)
	require.True(t, ps.PauseRequested)
	require.Equal(t, "operator investigation", ps.PauseReason)
	require.Equal(t, ExecutionStatusPaused, ps.Status)

	// A fresh execution that Resumes without unpausing should still
	// see Status=Paused because the sticky flag re-parks the path.
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
	require.NoError(t, UnpausePathInCheckpoint(ctx, cp, executionID, "main"))

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

// TestPausePathInCheckpointIdempotent confirms pause helper is a no-op
// against an already-paused path and returns ErrPathNotFound when the
// path ID doesn't exist.
func TestPausePathInCheckpointIdempotent(t *testing.T) {
	cp := newSpikeMemoryCheckpointer()

	// Save a checkpoint with a single path in Running state.
	ctx := context.Background()
	execID := "exec-test"
	initial := &Checkpoint{
		ID:           "cp1",
		ExecutionID:  execID,
		WorkflowName: "test",
		Status:       string(ExecutionStatusRunning),
		PathStates: map[string]*PathState{
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
	require.NoError(t, PausePathInCheckpoint(ctx, cp, execID, "main", "manual"))
	loaded, _ := cp.LoadCheckpoint(ctx, execID)
	require.True(t, loaded.PathStates["main"].PauseRequested)
	require.Equal(t, "manual", loaded.PathStates["main"].PauseReason)

	// Second pause: no-op (idempotent).
	require.NoError(t, PausePathInCheckpoint(ctx, cp, execID, "main", "manual"))

	// Unpause clears the flag.
	require.NoError(t, UnpausePathInCheckpoint(ctx, cp, execID, "main"))
	loaded, _ = cp.LoadCheckpoint(ctx, execID)
	require.False(t, loaded.PathStates["main"].PauseRequested)
	require.Equal(t, "", loaded.PathStates["main"].PauseReason)

	// Unknown path returns ErrPathNotFound.
	err := PausePathInCheckpoint(ctx, cp, execID, "nope", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPathNotFound)

	// Unknown execution returns ErrNoCheckpoint.
	err = PausePathInCheckpoint(ctx, cp, "missing", "main", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoCheckpoint)
}

// TestPauseDeclarativeStep verifies that a `Pause` step parks the path
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
	ps := loaded.PathStates["main"]
	require.Equal(t, "after", ps.CurrentStep,
		"pause step must advance CurrentStep past itself")
	require.True(t, ps.PauseRequested)
	require.Equal(t, "awaiting approval", ps.PauseReason)

	// Unpause and resume: after should execute.
	require.NoError(t, UnpausePathInCheckpoint(ctx, cp, execID, "main"))

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

// TestPauseMultiPath: one path paused while a sibling path runs to
// completion. The execution ends Paused because the sibling completed
// but the paused path is still parked.
func TestPauseMultiPath(t *testing.T) {
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
					{Step: "quick", Path: "quick"},
					{Step: "slow", Path: "slow"},
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
		"execution should end Paused while the slow path is parked")
	require.NotNil(t, res.Suspension)
	require.Equal(t, SuspensionReasonPaused, res.Suspension.Reason)
	require.Len(t, res.Suspension.SuspendedPaths, 1)
	require.Equal(t, "slow", res.Suspension.SuspendedPaths[0].PathID)

	// The quick path should have completed before the execution ended.
	var quickCompleted bool
	for _, ps := range exec.state.GetPathStates() {
		if ps.ID == "quick" && ps.Status == ExecutionStatusCompleted {
			quickCompleted = true
		}
	}
	require.True(t, quickCompleted, "quick path should have completed")
}

// TestPausePathNotFound confirms PausePath/UnpausePath return
// ErrPathNotFound for unknown path IDs.
func TestPausePathNotFound(t *testing.T) {
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

	err = exec.PausePath("nope", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPathNotFound)

	err = exec.UnpausePath("nope")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPathNotFound)
}

// TestPauseClearedBeforeBoundary verifies that pausing then unpausing
// a path before it hits a step boundary results in the path continuing
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
	require.NoError(t, exec.PausePath("main", "changed my mind"))
	require.NoError(t, exec.UnpausePath("main"))

	// Release the activity and let the path complete.
	close(release)
	wg.Wait()

	require.NoError(t, runErr)
	require.Equal(t, ExecutionStatusCompleted, res.Status,
		"unpause before boundary should let the path complete normally")
}

// TestPauseStateJSONRoundTrip verifies the PauseRequested/PauseReason
// fields on PathState round-trip cleanly through JSON.
func TestPauseStateJSONRoundTrip(t *testing.T) {
	original := &PathState{
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

	var restored PathState
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
