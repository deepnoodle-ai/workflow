package workflow

import (
	"errors"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestValidatePassesForValidWorkflow(t *testing.T) {
	wf, err := New(Options{
		Name: "valid",
		Steps: []*Step{
			{Name: "start", Activity: "a", Next: []*Edge{{Step: "end"}}},
			{Name: "end", Activity: "b"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, wf.Validate())
}

func TestValidateDetectsBadCatchReference(t *testing.T) {
	_, err := New(Options{
		Name: "bad-catch",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Catch: []*CatchConfig{
					{ErrorEquals: []string{"all"}, Next: "ghost"},
				},
			},
		},
	})
	require.Error(t, err)

	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	require.Len(t, ve.Problems, 1)
	require.Contains(t, ve.Problems[0].Message, `unknown step "ghost"`)
	require.True(t, errors.Is(err, ErrUnknownCatchTarget))
}

func TestValidateDetectsBadJoinBranch(t *testing.T) {
	_, err := New(Options{
		Name: "bad-join",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Next: []*Edge{
					{Step: "join", BranchName: "pathA"},
				},
			},
			{
				Name: "join",
				Join: &JoinConfig{
					Branches: []string{"pathA", "pathZ"}, // pathZ doesn't exist
				},
			},
		},
	})
	require.Error(t, err)

	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	// Should find pathZ as unknown
	found := false
	for _, p := range ve.Problems {
		if p.Step == "join" {
			require.Contains(t, p.Message, `unknown branch "pathZ"`)
			found = true
		}
	}
	require.True(t, found, "should report bad join branch")
	require.True(t, errors.Is(err, ErrUnknownJoinBranch))
}

func TestValidateReportsMultipleProblems(t *testing.T) {
	_, err := New(Options{
		Name: "multi-problems",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Catch: []*CatchConfig{
					{ErrorEquals: []string{"all"}, Next: "nonexistent"},
				},
				Retry: []*RetryConfig{{MaxRetries: -1}},
			},
		},
	})
	require.Error(t, err)

	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	require.GreaterOrEqual(t, len(ve.Problems), 2, "should find both retry and bad catch ref")
}

func TestValidateStepReachableViaCatch(t *testing.T) {
	// Reachability is no longer enforced — this still builds successfully.
	wf, err := New(Options{
		Name: "catch-reachable",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Catch: []*CatchConfig{
					{ErrorEquals: []string{"all"}, Next: "recovery"},
				},
			},
			{Name: "recovery", Activity: "b"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, wf.Validate())
}

func TestValidateRejectsDuplicateStepName(t *testing.T) {
	_, err := New(Options{
		Name: "dupe",
		Steps: []*Step{
			{Name: "a", Activity: "x"},
			{Name: "a", Activity: "y"},
		},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDuplicateStepName))
}

func TestValidateRejectsUnknownStartAt(t *testing.T) {
	_, err := New(Options{
		Name:    "bad-start",
		StartAt: "ghost",
		Steps: []*Step{
			{Name: "a", Activity: "x"},
		},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrUnknownStartStep))
}

func TestValidateAcceptsExplicitStartAt(t *testing.T) {
	wf, err := New(Options{
		Name:    "explicit-start",
		StartAt: "b",
		Steps: []*Step{
			{Name: "a", Activity: "x"},
			{Name: "b", Activity: "y"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "b", wf.Start().Name)
}

func TestValidateRejectsInvalidRetryConfig(t *testing.T) {
	_, err := New(Options{
		Name: "bad-retry",
		Steps: []*Step{
			{
				Name:     "a",
				Activity: "x",
				Retry: []*RetryConfig{
					{MaxRetries: -1},
				},
			},
		},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidRetryConfig))
}

func TestValidateRejectsModifierOnPauseStep(t *testing.T) {
	_, err := New(Options{
		Name: "bad-pause-retry",
		Steps: []*Step{
			{
				Name:  "gate",
				Pause: &PauseConfig{},
				Retry: []*RetryConfig{{MaxRetries: 1}},
				Next:  []*Edge{{Step: "done"}},
			},
			{Name: "done", Activity: "x"},
		},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidModifier))
}
