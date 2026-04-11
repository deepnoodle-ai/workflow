package workflow

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestActivityHistoryRecordOrReplayAcrossResume is the headline test:
// an activity records expensive work via RecordOrReplay, then calls
// workflow.Wait. After the signal is delivered and the execution
// resumes, the cached work is NOT re-run — only uncached work (the
// part after the Wait) executes on the replay.
func TestActivityHistoryRecordOrReplayAcrossResume(t *testing.T) {
	const topic = "cb-test"

	var (
		planCalls   int32
		postCalls   int32
		reactCalls  int32
		invocations int32
	)

	agent := NewActivityFunction("agent", func(ctx Context, p map[string]any) (any, error) {
		atomic.AddInt32(&invocations, 1)
		history := ActivityHistory(ctx)

		plan, err := history.RecordOrReplay("plan", func() (any, error) {
			atomic.AddInt32(&planCalls, 1)
			return "the-plan", nil
		})
		if err != nil {
			return nil, err
		}

		_, err = history.RecordOrReplay("post-callback", func() (any, error) {
			atomic.AddInt32(&postCalls, 1)
			return nil, nil
		})
		if err != nil {
			return nil, err
		}

		reply, err := Wait(ctx, topic, time.Minute)
		if err != nil {
			return nil, err
		}

		// React runs after the wait — should only run once (on the
		// replay after the signal is delivered).
		atomic.AddInt32(&reactCalls, 1)
		return fmt.Sprintf("%s:%v", plan, reply), nil
	})

	wf, err := New(Options{
		Name: "history-agent",
		Steps: []*Step{
			{Name: "run", Activity: "agent", Store: "result"},
		},
		Outputs: []*Output{
			{Name: "result", Variable: "result"},
		},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()

	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{agent},
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
	require.Equal(t, int32(1), atomic.LoadInt32(&invocations))
	require.Equal(t, int32(1), atomic.LoadInt32(&planCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&postCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&reactCalls))

	// The checkpoint should carry the activity history.
	loaded, _ := cp.LoadCheckpoint(ctx, execID)
	ps := loaded.PathStates["main"]
	require.NotNil(t, ps.ActivityHistory)
	require.Equal(t, "the-plan", ps.ActivityHistory["plan"])
	require.Contains(t, ps.ActivityHistory, "post-callback")

	// Deliver the signal and resume.
	require.NoError(t, signals.Send(ctx, execID, topic, "reply-payload"))

	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{agent},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)
	res2, err := exec2.ExecuteOrResume(ctx, execID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res2.Status)

	// Critical: plan and post-callback ran only ONCE across the whole
	// cycle, even though the activity was invoked twice (once before
	// the wait, once on replay).
	require.Equal(t, int32(2), atomic.LoadInt32(&invocations),
		"activity should invoke twice total (initial + replay)")
	require.Equal(t, int32(1), atomic.LoadInt32(&planCalls),
		"plan should only run once — cached across replay")
	require.Equal(t, int32(1), atomic.LoadInt32(&postCalls),
		"post-callback should only run once — cached across replay")
	require.Equal(t, int32(1), atomic.LoadInt32(&reactCalls),
		"react runs once (only after the wait succeeds)")

	// Output should include cached plan and delivered signal payload.
	require.Equal(t, "the-plan:reply-payload", res2.Outputs["result"])

	// After the step advances, ActivityHistory should be cleared from
	// the checkpoint.
	loaded, _ = cp.LoadCheckpoint(ctx, execID)
	ps = loaded.PathStates["main"]
	require.Empty(t, ps.ActivityHistory,
		"history should be cleared when the step advances past the activity")
}

// TestActivityHistoryErrorNotCached: if fn returns an error, the value
// is NOT cached — a subsequent call runs fn again. This matches the
// PRD's semantics: caching is for successful results, not failures.
func TestActivityHistoryErrorNotCached(t *testing.T) {
	var calls int32
	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) {
		history := ActivityHistory(ctx)

		// First call fails.
		_, err := history.RecordOrReplay("work", func() (any, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				return nil, fmt.Errorf("transient")
			}
			return "ok", nil
		})
		if err != nil {
			// Retry — second call should re-run fn and cache the value.
			v, err := history.RecordOrReplay("work", func() (any, error) {
				atomic.AddInt32(&calls, 1)
				return "ok", nil
			})
			return v, err
		}
		return nil, nil
	})

	wf, err := New(Options{
		Name:  "history-error",
		Steps: []*Step{{Name: "run", Activity: "noop"}},
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
	require.Equal(t, ExecutionStatusCompleted, res.Status)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls),
		"fn should run twice because the first attempt errored")
}

// TestActivityHistoryScopedPerStep verifies that a RecordOrReplay key
// in one step does not leak into the next step.
func TestActivityHistoryScopedPerStep(t *testing.T) {
	var aCalls, bCalls int32

	stepA := NewActivityFunction("a", func(ctx Context, p map[string]any) (any, error) {
		history := ActivityHistory(ctx)
		_, err := history.RecordOrReplay("work", func() (any, error) {
			atomic.AddInt32(&aCalls, 1)
			return "a-result", nil
		})
		return "a-done", err
	})
	stepB := NewActivityFunction("b", func(ctx Context, p map[string]any) (any, error) {
		history := ActivityHistory(ctx)
		// Same key as step A — should NOT be cached, since history is
		// scoped per step.
		_, err := history.RecordOrReplay("work", func() (any, error) {
			atomic.AddInt32(&bCalls, 1)
			return "b-result", nil
		})
		return "b-done", err
	})

	wf, err := New(Options{
		Name: "history-scoped",
		Steps: []*Step{
			{Name: "a", Activity: "a", Next: []*Edge{{Step: "b"}}},
			{Name: "b", Activity: "b"},
		},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{stepA, stepB},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := exec.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, res.Status)
	require.Equal(t, int32(1), atomic.LoadInt32(&aCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&bCalls),
		"step B should run its own RecordOrReplay fn (no cross-step leakage)")
}

// TestActivityHistoryNoOpWithoutContext verifies that calling
// ActivityHistory on a context that isn't history-aware (or where
// the activity wasn't constructed with history plumbing) returns a
// no-op History that doesn't crash.
func TestActivityHistoryNoOpWithoutContext(t *testing.T) {
	// A bare executionContext without a History set should still
	// return a working no-op cache.
	ctx := &executionContext{Context: context.Background()}
	history := ActivityHistory(ctx)
	require.NotNil(t, history)

	var calls int32
	val, err := history.RecordOrReplay("key", func() (any, error) {
		atomic.AddInt32(&calls, 1)
		return "v", nil
	})
	require.NoError(t, err)
	require.Equal(t, "v", val)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

// TestActivityHistoryGetAndLen sanity-checks the introspection helpers.
func TestActivityHistoryGetAndLen(t *testing.T) {
	h := newHistory(nil, nil)
	require.Equal(t, 0, h.Len())

	_, err := h.RecordOrReplay("k1", func() (any, error) { return 1, nil })
	require.NoError(t, err)
	_, err = h.RecordOrReplay("k2", func() (any, error) { return 2, nil })
	require.NoError(t, err)

	require.Equal(t, 2, h.Len())
	v, ok := h.Get("k1")
	require.True(t, ok)
	require.Equal(t, 1, v)
	_, ok = h.Get("missing")
	require.False(t, ok)
}
