package workflow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// captureStore records all step progress updates for assertions.
type captureStore struct {
	mu      sync.Mutex
	updates []StepProgress
}

func (s *captureStore) UpdateStepProgress(ctx context.Context, executionID string, progress StepProgress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, progress)
	return nil
}

func (s *captureStore) getUpdates() []StepProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]StepProgress, len(s.updates))
	copy(cp, s.updates)
	return cp
}

func TestStepProgressTrackingLifecycle(t *testing.T) {
	store := &captureStore{}

	wf, err := New(Options{
		Name: "progress-test",
		Steps: []*Step{
			{Name: "step-1", Activity: "work", Next: []*Edge{{Step: "step-2"}}},
			{Name: "step-2", Activity: "work"},
		},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Activities: []Activity{
			NewActivityFunction("work", func(ctx Context, params map[string]any) (any, error) {
				return "done", nil
			}),
		},
		StepProgressStore: store,
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)

	// Wait for async dispatches to complete
	var updates []StepProgress
	require.Eventually(t, func() bool {
		updates = store.getUpdates()
		return len(updates) >= 4
	}, 500*time.Millisecond, 10*time.Millisecond,
		"should have at least: step-1 running, step-1 completed, step-2 running, step-2 completed")

	// Verify we see the right status transitions for step-1 (no ordering assumptions)
	var step1Updates []StepProgress
	for _, u := range updates {
		if u.StepName == "step-1" {
			step1Updates = append(step1Updates, u)
		}
	}
	require.GreaterOrEqual(t, len(step1Updates), 2)

	var hasRunning, hasCompleted, hasAttempt1 bool
	for _, u := range step1Updates {
		if u.Status == StepStatusRunning {
			hasRunning = true
		}
		if u.Status == StepStatusCompleted {
			hasCompleted = true
		}
		if u.Attempt == 1 {
			hasAttempt1 = true
		}
	}
	require.True(t, hasRunning, "step-1 should have a running update")
	require.True(t, hasCompleted, "step-1 should have a completed update")
	require.True(t, hasAttempt1, "step-1 should have attempt 1")
}

func TestStepProgressReportProgressDetail(t *testing.T) {
	store := &captureStore{}

	wf, err := New(Options{
		Name:  "detail-test",
		Steps: []*Step{{Name: "long-step", Activity: "slow"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Activities: []Activity{
			NewActivityFunction("slow", func(ctx Context, params map[string]any) (any, error) {
				ReportProgress(ctx, ProgressDetail{
					Message: "Halfway there",
					Data:    map[string]any{"pct": 50},
				})
				return "done", nil
			}),
		},
		StepProgressStore: store,
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)

	var detail *ProgressDetail
	require.Eventually(t, func() bool {
		for _, u := range store.getUpdates() {
			if u.Detail != nil && u.Detail.Message == "Halfway there" {
				detail = u.Detail
				return true
			}
		}
		return false
	}, 500*time.Millisecond, 10*time.Millisecond,
		"should have received progress detail update")
	require.Equal(t, 50, detail.Data["pct"])
}

func TestReportProgressNoopWithoutStore(t *testing.T) {
	// When no StepProgressStore is configured, ReportProgress is a silent no-op
	wf, err := New(Options{
		Name:  "noop-test",
		Steps: []*Step{{Name: "step", Activity: "work"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Activities: []Activity{
			NewActivityFunction("work", func(ctx Context, params map[string]any) (any, error) {
				// Should not panic even without a store
				ReportProgress(ctx, ProgressDetail{Message: "hello"})
				return nil, nil
			}),
		},
		// No StepProgressStore set
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
}
