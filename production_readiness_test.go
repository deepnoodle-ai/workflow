package workflow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestMultiWaitWithRecordOrReplay verifies that an activity which calls
// workflow.Wait twice (in sequence, on different topics) correctly
// pairs each wait with its signal across replays — provided each Wait
// call is wrapped in History.RecordOrReplay. This is the documented
// pattern; the test serves as both a regression and a worked example.
//
// Without RecordOrReplay wrapping, a second-wait suspension causes the
// activity to replay from the top and the first Wait call hits an
// empty store (signal already consumed) — undefined behavior. With
// RecordOrReplay wrapping, each Wait result is cached and replays
// short-circuit through the cache.
func TestMultiWaitWithRecordOrReplay(t *testing.T) {
	const topic1 = "first-callback"
	const topic2 = "second-callback"

	var invocations int32
	multiWait := NewActivityFunction("multi", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&invocations, 1)
		history := ActivityHistory(ctx)

		v1, err := history.RecordOrReplay("wait1", func() (any, error) {
			return Wait(ctx, topic1, time.Minute)
		})
		if err != nil {
			return nil, err
		}

		v2, err := history.RecordOrReplay("wait2", func() (any, error) {
			return Wait(ctx, topic2, time.Minute)
		})
		if err != nil {
			return nil, err
		}

		return fmt.Sprintf("%v|%v", v1, v2), nil
	})

	wf, err := New(Options{
		Name: "multi-wait",
		Steps: []*Step{
			{Name: "run", Activity: "multi", Store: "result"},
		},
		Outputs: []*Output{{Name: "result", Variable: "result"}},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()

	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{multiWait},
		Checkpointer: cp,
		SignalStore:  signals,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run 1: suspends on wait1
	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)
	require.Contains(t, res1.Suspension.Topics, topic1)

	// Deliver signal1.
	require.NoError(t, signals.Send(ctx, execID, topic1, "alpha"))

	// Run 2: replays activity, wait1 consumes signal1, wait2 unwinds.
	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{multiWait},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res2.Status,
		"should re-suspend on wait2 after consuming signal1")
	require.Contains(t, res2.Suspension.Topics, topic2)

	// Deliver signal2.
	require.NoError(t, signals.Send(ctx, execID, topic2, "beta"))

	// Run 3: full replay; wait1 returns cached "alpha"; wait2 consumes signal2.
	exec3, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{multiWait},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res3, err := exec3.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res3.Status)
	require.Equal(t, "alpha|beta", res3.Outputs["result"],
		"each wait must pair with its own signal across replays")
	require.Equal(t, int32(3), atomic.LoadInt32(&invocations),
		"activity should invoke 3 times: initial, replay-after-signal1, replay-after-signal2")
}

// TestPauseDuringActiveWait covers the pause-mid-activity race: the
// operator pauses a branch while an activity is running, the activity
// then unwinds via workflow.Wait. The branch must end up Paused (not
// Suspended) so the operator's intent is honored.
func TestPauseDuringActiveWait(t *testing.T) {
	gate := make(chan struct{})
	var invocations int32

	awaiter := NewActivityFunction("awaiter", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&invocations, 1)
		// Wait for the test to call PauseBranch, then unwind via Wait.
		<-gate
		return Wait(ctx, "topic", time.Minute)
	})

	wf, err := New(Options{
		Name: "pause-during-wait",
		Steps: []*Step{
			{Name: "await", Activity: "awaiter"},
		},
	})
	require.NoError(t, err)

	cp := newSpikeMemoryCheckpointer()
	exec, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter},
		Checkpointer: cp,
		SignalStore:  NewMemorySignalStore(),
	})
	require.NoError(t, err)
	execID := exec.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		res    *ExecutionResult
		runErr error
		done   = make(chan struct{})
	)
	go func() {
		defer close(done)
		res, runErr = exec.Execute(ctx)
	}()

	// Wait for the activity to start.
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&invocations) == 1
	}, 2*time.Second, 5*time.Millisecond, "activity should start")

	// Pause the branch while the activity is running.
	require.NoError(t, exec.PauseBranch("main", "operator wins over wait"))

	// Release the gate so the activity unwinds via Wait.
	close(gate)

	<-done
	require.NoError(t, runErr)
	require.NotNil(t, res)
	require.Equal(t, ExecutionStatusPaused, res.Status,
		"pause must win over a concurrent wait-unwind")
	require.Equal(t, SuspensionReasonPaused, res.Suspension.Reason)
	require.Equal(t, "operator wins over wait", res.Suspension.SuspendedBranches[0].PauseReason,
		"PauseReason should be surfaced on the SuspendedBranch")

	// Verify the checkpoint reflects Paused status, not Suspended,
	// and that no stale Wait state is persisted (the wait was dropped
	// because pause won the race).
	loaded, _ := cp.LoadCheckpoint(ctx, execID)
	ps := loaded.BranchStates["main"]
	require.Equal(t, ExecutionStatusPaused, ps.Status)
	require.True(t, ps.PauseRequested)
	require.Nil(t, ps.Wait, "wait state should be dropped when pause wins the race")
}

// TestPauseFreezesSignalWaitDeadline verifies that pausing a branch
// that's parked on a signal wait freezes the wait clock — the pause
// duration must not consume the wait's timeout budget. Symmetric to
// the same behavior for sleeps.
func TestPauseFreezesSignalWaitDeadline(t *testing.T) {
	const topic = "frozen-callback"
	const timeout = 100 * time.Millisecond

	awaiter := NewActivityFunction("awaiter", func(ctx Context, p map[string]any) (any, error) {
		return Wait(ctx, topic, timeout)
	})

	wf, err := New(Options{
		Name: "pause-freezes-signal",
		Steps: []*Step{
			{Name: "await", Activity: "awaiter", Store: "reply"},
		},
		Outputs: []*Output{{Name: "reply", Variable: "reply"}},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()

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

	// Run 1: suspends on the signal wait.
	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)
	originalDeadline := res1.Suspension.WakeAt

	// Pause the suspended branch.
	require.NoError(t, PauseBranchInCheckpoint(ctx, cp, execID, "main", "freezing"))

	// Verify the wait was frozen (WakeAt cleared, Remaining > 0).
	loaded, _ := cp.LoadCheckpoint(ctx, execID)
	ps := loaded.BranchStates["main"]
	require.True(t, ps.PauseRequested)
	require.NotNil(t, ps.Wait)
	require.Equal(t, WaitKindSignal, ps.Wait.Kind)
	require.True(t, ps.Wait.WakeAt.IsZero(), "paused signal-wait should clear WakeAt")
	require.Greater(t, ps.Wait.Remaining, time.Duration(0), "Remaining should be populated")

	// Sleep PAST the original timeout. If the clock were ticking,
	// the wait would now have expired.
	time.Sleep(timeout + 50*time.Millisecond)

	// Unpause: WakeAt should be rebased to now + remaining, NOT the
	// original (now-expired) deadline.
	require.NoError(t, UnpauseBranchInCheckpoint(ctx, cp, execID, "main"))
	loaded, _ = cp.LoadCheckpoint(ctx, execID)
	ps = loaded.BranchStates["main"]
	require.False(t, ps.PauseRequested)
	require.False(t, ps.Wait.WakeAt.IsZero(), "unpause should restore WakeAt")
	require.True(t, ps.Wait.WakeAt.After(originalDeadline),
		"rebased deadline should be later than the original (pause duration > timeout)")

	// Deliver the signal and resume — should complete because the
	// pause-frozen wait still has time on its budget.
	require.NoError(t, signals.Send(ctx, execID, topic, "delivered"))

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
	require.Equal(t, ExecutionStatusCompleted, res2.Status,
		"signal arriving after pause-thaw should still resolve the wait")
	require.Equal(t, "delivered", res2.Outputs["reply"])
}

// TestConcurrentSignalDeliveryStress fans out N branches each waiting on
// a unique signal, then delivers all N signals concurrently from
// many goroutines. Verifies that with the race detector, signal
// delivery and consumption don't race, and that every branch receives
// exactly its own signal.
func TestConcurrentSignalDeliveryStress(t *testing.T) {
	const fanout = 16

	steps := []*Step{
		{
			Name:     "fanout",
			Activity: "noop",
			Next:     make([]*Edge, fanout),
		},
	}
	for i := 0; i < fanout; i++ {
		name := fmt.Sprintf("waiter-%d", i)
		steps[0].Next[i] = &Edge{Step: name, BranchName: name}
		steps = append(steps, &Step{
			Name:     name,
			Activity: "waiter",
			Store:    "result",
		})
	}

	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return "ok", nil })
	waiter := NewActivityFunction("waiter", func(ctx Context, p map[string]any) (any, error) {
		topic := fmt.Sprintf("topic-%s", ctx.GetBranchID())
		return Wait(ctx, topic, time.Minute)
	})

	wf, err := New(Options{Name: "concurrent-signals", Steps: steps})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()

	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop, waiter},
		Checkpointer: cp,
		SignalStore:  signals,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First run: all waiter branches suspend.
	res1, err := exec1.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res1.Status)
	require.Len(t, res1.Suspension.SuspendedBranches, fanout)

	// Concurrently deliver all signals.
	var wg sync.WaitGroup
	for i := 0; i < fanout; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			topic := fmt.Sprintf("topic-waiter-%d", i)
			payload := fmt.Sprintf("payload-%d", i)
			require.NoError(t, signals.Send(ctx, execID, topic, payload))
		}()
	}
	wg.Wait()

	// Resume.
	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop, waiter},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res2.Status)

	// Each branch should have received exactly its own signal payload.
	branchStates := exec2.state.GetBranchStates()
	for i := 0; i < fanout; i++ {
		branchID := fmt.Sprintf("waiter-%d", i)
		ps, ok := branchStates[branchID]
		require.True(t, ok, "branch %s missing", branchID)
		require.Equal(t, ExecutionStatusCompleted, ps.Status)
		require.Equal(t, fmt.Sprintf("payload-%d", i), ps.Variables["result"])
	}
}

// TestValidateRejectsConflictingStepKinds verifies that the validator
// rejects a step with multiple "kinds" set (activity + sleep, etc.).
// The runtime would silently dispatch by handler precedence, which is
// always a programmer error.
func TestValidateRejectsConflictingStepKinds(t *testing.T) {
	cases := []struct {
		name string
		step *Step
	}{
		{
			name: "activity + sleep",
			step: &Step{Name: "bad", Activity: "x", Sleep: &SleepConfig{Duration: time.Second}, Next: []*Edge{{Step: "after"}}},
		},
		{
			name: "activity + pause",
			step: &Step{Name: "bad", Activity: "x", Pause: &PauseConfig{}, Next: []*Edge{{Step: "after"}}},
		},
		{
			name: "wait_signal + sleep",
			step: &Step{
				Name:       "bad",
				WaitSignal: &WaitSignalConfig{Topic: "t", Timeout: time.Second},
				Sleep:      &SleepConfig{Duration: time.Second},
				Next:       []*Edge{{Step: "after"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Options{
				Name: "conflict",
				Steps: []*Step{
					tc.step,
					{Name: "after", Activity: "noop"},
				},
			})
			require.Error(t, err, "should reject conflicting kinds")
			require.Contains(t, err.Error(), "conflicting step kinds")
			require.True(t, errors.Is(err, ErrInvalidStepKind))
		})
	}
}

// TestSignalStoreContextCancelled verifies the in-memory signal store
// honors a cancelled context on both Send and Receive.
func TestSignalStoreContextCancelled(t *testing.T) {
	store := NewMemorySignalStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.Send(ctx, "exec", "topic", "payload")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	_, err = store.Receive(ctx, "exec", "topic")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

// TestSuspendedBranchExposesPauseReason verifies that the result's
// SuspendedBranch entry surfaces PauseReason for paused branches.
func TestSuspendedBranchExposesPauseReason(t *testing.T) {
	wf, err := New(Options{
		Name: "expose-reason",
		Steps: []*Step{
			{
				Name:  "gate",
				Pause: &PauseConfig{Reason: "needs human review"},
				Next:  []*Edge{{Step: "after"}},
			},
			{Name: "after", Activity: "noop"},
		},
	})
	require.NoError(t, err)

	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return "ok", nil })
	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{noop},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := exec.Execute(ctx)
	require.NoError(t, err)
	require.True(t, res.Paused())
	require.Len(t, res.Suspension.SuspendedBranches, 1)
	require.Equal(t, "needs human review", res.Suspension.SuspendedBranches[0].PauseReason)
}

// TestResumeFromCheckpointMultipleSignalWaitsInSameExecution: a
// fan-out of three branches, each suspends on its own signal. Signals
// are delivered out of order. Each resume picks the right branch's
// payload and the workflow completes when all three signals have
// been delivered.
func TestResumeFromCheckpointMultipleSignalWaitsInSameExecution(t *testing.T) {
	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return "ok", nil })
	awaiter := NewActivityFunction("awaiter", func(ctx Context, p map[string]any) (any, error) {
		// Topic embeds the branch ID so each branch waits on its own.
		return Wait(ctx, fmt.Sprintf("t-%s", ctx.GetBranchID()), time.Minute)
	})

	wf, err := New(Options{
		Name: "multi-wait-fanout",
		Steps: []*Step{
			{
				Name:     "fanout",
				Activity: "noop",
				Next: []*Edge{
					{Step: "wait1", BranchName: "p1"},
					{Step: "wait2", BranchName: "p2"},
					{Step: "wait3", BranchName: "p3"},
				},
			},
			{Name: "wait1", Activity: "awaiter"},
			{Name: "wait2", Activity: "awaiter"},
			{Name: "wait3", Activity: "awaiter"},
		},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()
	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop, awaiter},
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
	require.Len(t, res1.Suspension.SuspendedBranches, 3)

	// Deliver only signal for p2 first. Resume — should re-suspend
	// because p1 and p3 are still waiting.
	require.NoError(t, signals.Send(ctx, execID, "t-p2", "two"))

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop, awaiter},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusSuspended, res2.Status)
	// Two branches still suspended (p1, p3).
	require.Len(t, res2.Suspension.SuspendedBranches, 2)

	// Deliver the rest.
	require.NoError(t, signals.Send(ctx, execID, "t-p1", "one"))
	require.NoError(t, signals.Send(ctx, execID, "t-p3", "three"))

	exec3, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop, awaiter},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res3, err := exec3.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res3.Status)
}

// TestFreezeThawWaitHelpersAreIdempotent exercises freezeWaitOnPause
// and thawWaitOnUnpause directly on a synthetic BranchState: a freeze
// captures Remaining and clears WakeAt; a second freeze is a no-op
// (does not re-compute a smaller Remaining); a thaw rebases WakeAt
// to now + Remaining.
func TestFreezeThawWaitHelpersAreIdempotent(t *testing.T) {
	// Construct a branch state with a sleep wait whose WakeAt is in
	// the future, then freeze + thaw it manually.
	now := time.Now()
	future := now.Add(time.Hour)
	state := &BranchState{
		Wait: &WaitState{
			Kind:    WaitKindSleep,
			Timeout: time.Hour,
			WakeAt:  future,
		},
	}

	freezeWaitOnPause(state, now)
	require.True(t, state.Wait.WakeAt.IsZero(), "freeze should clear WakeAt")
	require.Equal(t, time.Hour, state.Wait.Remaining)

	// Idempotent: a second freeze does not re-compute.
	freezeWaitOnPause(state, now.Add(time.Second))
	require.Equal(t, time.Hour, state.Wait.Remaining)

	// Thaw rebases.
	resumeAt := now.Add(time.Minute)
	thawWaitOnUnpause(state, resumeAt)
	require.False(t, state.Wait.WakeAt.IsZero())
	require.Equal(t, time.Duration(0), state.Wait.Remaining)
	require.True(t, state.Wait.WakeAt.Equal(resumeAt.Add(time.Hour)))
}

// TestRecordOrReplayWrappingWaitCachesValueOnSuccess proves that
// RecordOrReplay correctly caches a Wait result on the resume that
// receives the signal — i.e. the Wait sentinel doesn't poison the
// cache.
func TestRecordOrReplayWrappingWaitCachesValueOnSuccess(t *testing.T) {
	const topic = "single-wait"
	var fnCalls int32

	wrapped := NewActivityFunction("wrapped", func(ctx Context, p map[string]any) (any, error) {
		history := ActivityHistory(ctx)
		return history.RecordOrReplay("wait", func() (any, error) {
			atomic.AddInt32(&fnCalls, 1)
			return Wait(ctx, topic, time.Minute)
		})
	})

	wf, err := New(Options{
		Name: "wait-wrapped",
		Steps: []*Step{
			{Name: "run", Activity: "wrapped", Store: "result"},
		},
		Outputs: []*Output{{Name: "result", Variable: "result"}},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()

	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{wrapped},
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
	require.Equal(t, int32(1), atomic.LoadInt32(&fnCalls), "fn runs once on first invocation")

	require.NoError(t, signals.Send(ctx, execID, topic, "ok"))

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{wrapped},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res2.Status)
	require.Equal(t, "ok", res2.Outputs["result"])
	require.Equal(t, int32(2), atomic.LoadInt32(&fnCalls),
		"fn runs twice total: initial + replay-after-signal")
}
