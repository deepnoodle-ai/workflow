package workflow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
		Workflow: wf,
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

	// Give async dispatches time to complete
	time.Sleep(50 * time.Millisecond)

	updates := store.getUpdates()
	// Should have at least: step-1 running, step-1 completed, step-2 running, step-2 completed
	require.GreaterOrEqual(t, len(updates), 4)

	// Verify we see the right status transitions for step-1
	var step1Updates []StepProgress
	for _, u := range updates {
		if u.StepName == "step-1" {
			step1Updates = append(step1Updates, u)
		}
	}
	require.GreaterOrEqual(t, len(step1Updates), 2)
	require.Equal(t, StepStatusRunning, step1Updates[0].Status)
	require.Equal(t, StepStatusCompleted, step1Updates[len(step1Updates)-1].Status)
	require.Equal(t, 1, step1Updates[0].Attempt)
}

func TestStepProgressReportProgressDetail(t *testing.T) {
	store := &captureStore{}

	wf, err := New(Options{
		Name:  "detail-test",
		Steps: []*Step{{Name: "long-step", Activity: "slow"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		Workflow: wf,
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

	time.Sleep(50 * time.Millisecond)

	updates := store.getUpdates()
	// Find the update with our progress detail
	var found bool
	for _, u := range updates {
		if u.Detail != nil && u.Detail.Message == "Halfway there" {
			require.Equal(t, 50, u.Detail.Data["pct"])
			found = true
			break
		}
	}
	require.True(t, found, "should have received progress detail update")
}

func TestReportProgressNoopWithoutStore(t *testing.T) {
	// When no StepProgressStore is configured, ReportProgress is a silent no-op
	wf, err := New(Options{
		Name:  "noop-test",
		Steps: []*Step{{Name: "step", Activity: "work"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		Workflow: wf,
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
