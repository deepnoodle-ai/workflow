package workflowtest_test

import (
	"errors"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/deepnoodle-ai/workflow/workflowtest"
)

func TestFakeContext_Defaults(t *testing.T) {
	var _ workflow.Context = (*workflowtest.FakeContext)(nil)

	fc := workflowtest.NewFakeContext(workflowtest.FakeContextOptions{})
	require.Equal(t, "fake-branch", fc.BranchID())
	require.Equal(t, "fake-step", fc.StepName())
	require.NotNil(t, fc.Logger())
	require.NotNil(t, fc.History())
	require.Equal(t, 0, fc.Inputs().Len())
}

func TestFakeContext_VariablesAndInputs(t *testing.T) {
	fc := workflowtest.NewFakeContext(workflowtest.FakeContextOptions{
		Inputs:    map[string]any{"name": "alice"},
		Variables: map[string]any{"counter": 1},
	})

	name, ok := fc.Inputs().Get("name")
	require.True(t, ok)
	require.Equal(t, "alice", name)

	counter, ok := fc.Get("counter")
	require.True(t, ok)
	require.Equal(t, 1, counter)

	fc.Set("counter", 2)
	counter, _ = fc.Get("counter")
	require.Equal(t, 2, counter)

	fc.Set("other", "x")
	require.Equal(t, []string{"counter", "other"}, fc.Keys())

	fc.Delete("other")
	_, ok = fc.Get("other")
	require.False(t, ok)
}

func TestFakeContext_Wait(t *testing.T) {
	boom := errors.New("boom")
	fc := workflowtest.NewFakeContext(workflowtest.FakeContextOptions{
		WaitFunc: func(topic string, timeout time.Duration) (any, error) {
			require.Equal(t, "approve", topic)
			return nil, boom
		},
	})

	_, err := fc.Wait("approve", time.Second)
	require.True(t, errors.Is(err, boom))
}

func TestFakeContext_ReportProgress(t *testing.T) {
	var reports []workflow.ProgressDetail
	fc := workflowtest.NewFakeContext(workflowtest.FakeContextOptions{
		OnProgress: func(d workflow.ProgressDetail) {
			reports = append(reports, d)
		},
	})

	fc.ReportProgress(workflow.ProgressDetail{Message: "step 1 of 2"})
	fc.ReportProgress(workflow.ProgressDetail{Message: "step 2 of 2"})

	require.Len(t, reports, 2)
	require.Equal(t, "step 1 of 2", reports[0].Message)
	require.Equal(t, "step 2 of 2", reports[1].Message)
}
