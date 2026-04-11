package workflow

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestInitialWaitConsumedOnceAcrossMultipleSleeps is a regression for
// the stale-deadline bug: a branch resumed from a checkpoint with a
// pending sleep must not reuse that deadline for a LATER sleep step.
// Before the fix, the branch carried p.initialWait for the entire run,
// so a second Sleep step would inherit the first step's WakeAt.
func TestInitialWaitConsumedOnceAcrossMultipleSleeps(t *testing.T) {
	const firstSleep = 30 * time.Millisecond
	const secondSleep = 500 * time.Millisecond

	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return "ok", nil })

	wf, err := New(Options{
		Name: "two-sleeps",
		Steps: []*Step{
			{Name: "nap1", Sleep: &SleepConfig{Duration: firstSleep}, Next: []*Edge{{Step: "nap2"}}},
			{Name: "nap2", Sleep: &SleepConfig{Duration: secondSleep}, Next: []*Edge{{Step: "done"}}},
			{Name: "done", Activity: "noop"},
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

	// First run: suspends on nap1.
	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)

	// Wait past nap1's deadline.
	time.Sleep(firstSleep + 10*time.Millisecond)

	// Resume: nap1 wakes, branch advances to nap2. nap2 should compute
	// a FRESH deadline from secondSleep, not reuse nap1's already-
	// expired WakeAt. Without the fix, the branch would incorrectly
	// advance all the way to the "done" step because the stale
	// deadline (from nap1) has passed.
	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop},
		Checkpointer: cp,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res2.Status,
		"second resume should re-suspend on nap2 with a fresh deadline")
	require.NotNil(t, res2.Suspension)
	require.Equal(t, SuspensionReasonSleeping, res2.Suspension.Reason)

	// The new WakeAt should be far enough in the future that it
	// reflects secondSleep, not a reuse of nap1's expired deadline.
	require.True(t, res2.Suspension.WakeAt.After(time.Now()),
		"nap2's WakeAt should be in the future (fresh sleep), got %v",
		res2.Suspension.WakeAt)
}

// TestPauseStepRejectsNamedEdge is a regression for the pause-handler
// dropping Edge.Path semantics. A pause step with a named next edge
// should fail loudly at runtime rather than silently ignoring the
// name.
func TestPauseStepRejectsNamedEdge(t *testing.T) {
	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return "ok", nil })

	wf, err := New(Options{
		Name: "pause-named-edge",
		Steps: []*Step{
			{
				Name:  "gate",
				Pause: &PauseConfig{},
				Next:  []*Edge{{Step: "after", BranchName: "branch-a"}},
			},
			{Name: "after", Activity: "noop"},
		},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{noop},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := exec.Execute(ctx)
	require.NoError(t, err)
	require.True(t, res.Failed(), "pause step with a named edge should fail")
	require.NotNil(t, res.Error)
	require.Contains(t, res.Error.Cause, "named edges")
}

// TestPauseExpiredSleepThawUnsticks is a regression for the thaw
// zero/zero bug: a sleep that was paused after expiring must be able
// to wake on resume. Before the fix, freeze set Remaining=0 and
// cleared WakeAt, and thaw's early-return on Remaining<=0 left the
// sleep permanently stuck.
func TestPauseExpiredSleepThawUnsticks(t *testing.T) {
	const duration = 20 * time.Millisecond

	var afterInvocations int32
	after := NewActivityFunction("after", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&afterInvocations, 1)
		return "done", nil
	})

	wf, err := New(Options{
		Name: "pause-expired-sleep",
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

	// First run: suspends on the sleep.
	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)

	// Wait PAST the deadline, THEN pause. At freeze time, the sleep
	// is already expired, so Remaining captured by
	// freezeSleepOnPause is zero.
	time.Sleep(duration + 30*time.Millisecond)
	require.NoError(t, PauseBranchInCheckpoint(ctx, cp, execID, "main", "test"))

	// Verify the frozen state is the pathological case: WakeAt=0,
	// Remaining=0.
	loaded, _ := cp.LoadCheckpoint(ctx, execID)
	ps := loaded.BranchStates["main"]
	require.True(t, ps.PauseRequested)
	require.True(t, ps.Wait.WakeAt.IsZero())
	require.Equal(t, time.Duration(0), ps.Wait.Remaining)

	// Unpause. The thaw must detect the zero/zero case and set
	// WakeAt = now so the sleep wakes immediately on resume.
	require.NoError(t, UnpauseBranchInCheckpoint(ctx, cp, execID, "main"))

	loaded, _ = cp.LoadCheckpoint(ctx, execID)
	ps = loaded.BranchStates["main"]
	require.False(t, ps.PauseRequested)
	require.False(t, ps.Wait.WakeAt.IsZero(),
		"thaw must restore a WakeAt even when Remaining was zero")

	// Resume: sleep wakes immediately, successor runs, execution
	// completes. Before the fix, this would re-suspend forever.
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

// TestPauseBranchConcurrentWithBranching is a stress regression for the
// activeBranches race between external PauseBranch and orchestrator
// mutations. It runs a workflow with many concurrent-branching branches
// and hammers PauseBranch while the orchestrator is actively spawning
// and removing branches from activeBranches. Before the fix, the data race
// detector flagged concurrent map read/write between pauseBranchLocked
// and runBranches/processBranchSnapshot.
func TestPauseBranchConcurrentWithBranching(t *testing.T) {
	const fanoutCount = 20

	// Each fanout child just sleeps briefly so the orchestrator has
	// work to churn through (branch snapshots, activeBranches mutations).
	worker := NewActivityFunction("worker", func(ctx Context, p map[string]any) (any, error) {
		time.Sleep(2 * time.Millisecond)
		return "ok", nil
	})

	steps := []*Step{
		{
			Name:     "fanout",
			Activity: "worker",
			Next:     make([]*Edge, fanoutCount),
		},
	}
	for i := 0; i < fanoutCount; i++ {
		name := fmt.Sprintf("child-%d", i)
		steps[0].Next[i] = &Edge{Step: name, BranchName: name}
		steps = append(steps, &Step{Name: name, Activity: "worker"})
	}

	wf, err := New(Options{Name: "race-stress", Steps: steps})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{worker},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run the execution in a goroutine so we can hammer PauseBranch
	// concurrently with the orchestrator's snapshot processing.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = exec.Execute(ctx)
	}()

	// Hammer PauseBranch on various branch IDs while the orchestrator is
	// creating/removing branches from activeBranches. Most calls will hit
	// ErrBranchNotFound (branch not yet created or already completed),
	// which is fine — we're stress-testing the map accesses, not
	// asserting on the pause outcomes.
	pauseDeadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(pauseDeadline) {
		for i := 0; i < fanoutCount; i++ {
			_ = exec.PauseBranch(fmt.Sprintf("child-%d", i), "stress")
			_ = exec.UnpauseBranch(fmt.Sprintf("child-%d", i))
		}
	}

	wg.Wait()
	// No explicit assertion — the race detector is the oracle. The
	// test passes if -race reports no concurrent map access.
}

// TestFinalStatusForcesFailedOnOrchestratorError verifies that an
// orchestrator-side error (e.g., a checkpoint save failure) forces
// the final status to Failed even when paused or suspended branches
// exist. Before the fix, the switch statement would silently classify
// such runs as Paused or Suspended, dropping the internal error.
func TestFinalStatusForcesFailedOnOrchestratorError(t *testing.T) {
	// A checkpointer that fails on every save. When any branch state
	// change triggers a save, processBranchSnapshot returns the error,
	// which becomes executionErr.
	failingCp := &failingCheckpointer{err: fmt.Errorf("disk full")}

	// Two branches: one will be paused, one will run and complete. The
	// pause triggers a save (fails); the completing branch's processing
	// also triggers a save (fails). Either way executionErr becomes
	// non-nil with a non-empty pausedIDs set.
	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) {
		return "ok", nil
	})

	wf, err := New(Options{
		Name: "final-status-err",
		Steps: []*Step{
			{
				Name:  "gate",
				Pause: &PauseConfig{},
				Next:  []*Edge{{Step: "after"}},
			},
			{Name: "after", Activity: "noop"},
		},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop},
		Checkpointer: failingCp,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := exec.Execute(ctx)
	require.NoError(t, err)
	// The result must report Failed because the orchestrator hit an
	// internal error (checkpoint save failed). The pause-state
	// classification must NOT override this.
	require.True(t, res.Failed(),
		"orchestrator error must force Failed status even with paused branches, got %s",
		res.Status)
	require.NotNil(t, res.Error)
}

// failingCheckpointer returns an error on every save.
type failingCheckpointer struct {
	err error
}

func (f *failingCheckpointer) SaveCheckpoint(ctx context.Context, cp *Checkpoint) error {
	return f.err
}
func (f *failingCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
	return nil, nil
}
func (f *failingCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	return nil
}
