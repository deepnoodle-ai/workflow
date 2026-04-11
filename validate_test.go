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

func TestValidateDetectsUnreachableStep(t *testing.T) {
	wf, err := New(Options{
		Name: "unreachable",
		Steps: []*Step{
			{Name: "start", Activity: "a"},
			// "orphan" is defined but no edge leads to it
			{Name: "orphan", Activity: "b"},
		},
	})
	require.NoError(t, err)

	err = wf.Validate()
	require.Error(t, err)

	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	require.Len(t, ve.Problems, 1)
	require.Equal(t, "orphan", ve.Problems[0].Step)
	require.Contains(t, ve.Problems[0].Message, "unreachable")
}

func TestValidateDetectsBadCatchReference(t *testing.T) {
	wf, err := New(Options{
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
	require.NoError(t, err)

	err = wf.Validate()
	require.Error(t, err)

	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	require.Len(t, ve.Problems, 1)
	require.Contains(t, ve.Problems[0].Message, `unknown step "ghost"`)
}

func TestValidateDetectsBadJoinBranch(t *testing.T) {
	wf, err := New(Options{
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
	require.NoError(t, err)

	err = wf.Validate()
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
}

func TestValidateReportsMultipleProblems(t *testing.T) {
	wf, err := New(Options{
		Name: "multi-problems",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Catch: []*CatchConfig{
					{ErrorEquals: []string{"all"}, Next: "nonexistent"},
				},
			},
			{Name: "island", Activity: "b"}, // unreachable
		},
	})
	require.NoError(t, err)

	err = wf.Validate()
	require.Error(t, err)

	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	require.GreaterOrEqual(t, len(ve.Problems), 2, "should find both unreachable step and bad catch ref")
}

func TestValidateStepReachableViaCatch(t *testing.T) {
	// A step only reachable via a catch handler should be considered reachable
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
	require.NoError(t, wf.Validate(), "recovery step should be reachable via catch")
}
