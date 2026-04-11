package workflow

import (
	"errors"
	"testing"
	"time"

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

// --- Phase 2: binding validation ---

func bindingReg() *ActivityRegistry {
	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("a", func(ctx Context, params map[string]any) (any, error) {
		return nil, nil
	}))
	reg.MustRegister(ActivityFunc("b", func(ctx Context, params map[string]any) (any, error) {
		return nil, nil
	}))
	return reg
}

func TestBindingValidation_UnknownActivity(t *testing.T) {
	wf, err := New(Options{
		Name: "unknown-activity",
		Steps: []*Step{
			{Name: "start", Activity: "missing"},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrUnknownActivity))
}

func TestBindingValidation_BadParameterExpression(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-param",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Parameters: map[string]any{
					"value": "$(!!bogus!!)",
				},
			},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidExpression))
}

func TestBindingValidation_BadParameterTemplate(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-template",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Parameters: map[string]any{
					"msg": "hello ${!!bogus!!} world",
				},
			},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidTemplate))
}

func TestBindingValidation_BadEdgeCondition(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-cond",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Next: []*Edge{
					{Step: "end", Condition: "!!bogus!!"},
				},
			},
			{Name: "end", Activity: "b"},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidExpression))
}

func TestBindingValidation_EdgeConditionTrueFalseSkipsCompile(t *testing.T) {
	wf, err := New(Options{
		Name: "bool-cond",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Next: []*Edge{
					{Step: "end", Condition: "true"},
				},
			},
			{Name: "end", Activity: "b"},
		},
	})
	require.NoError(t, err)
	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.NoError(t, err)
}

func TestBindingValidation_WaitSignalTopicTemplate(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-topic",
		Steps: []*Step{
			{
				Name: "gate",
				WaitSignal: &WaitSignalConfig{
					Topic:   "approved-${!!bogus!!}",
					Timeout: time.Second,
				},
			},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(),
		WithScriptCompiler(newTestCompiler()),
		WithSignalStore(NewMemorySignalStore()),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidTemplate))
}

func TestBindingValidation_RejectsStatePrefixOnStepStore(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-store",
		Steps: []*Step{
			{Name: "start", Activity: "a", Store: "state.result"},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidStorePath))
}

func TestBindingValidation_RejectsStatePrefixOnCatchStore(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-catch-store",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "a",
				Catch: []*CatchConfig{
					{ErrorEquals: []string{"all"}, Next: "end", Store: "state.err"},
				},
			},
			{Name: "end", Activity: "b"},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidStorePath))
}

func TestBindingValidation_RejectsStatePrefixOnWaitStore(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-wait-store",
		Steps: []*Step{
			{
				Name: "gate",
				WaitSignal: &WaitSignalConfig{
					Topic:   "topic",
					Timeout: time.Second,
					Store:   "state.payload",
				},
			},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(),
		WithScriptCompiler(newTestCompiler()),
		WithSignalStore(NewMemorySignalStore()),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidStorePath))
}

func TestBindingValidation_RejectsStatePrefixOnOutputVariable(t *testing.T) {
	wf, err := New(Options{
		Name: "bad-output",
		Steps: []*Step{
			{Name: "start", Activity: "a"},
		},
		Outputs: []*Output{
			{Name: "final", Variable: "state.result"},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidStorePath))
}

func TestBindingValidation_CollectsMultipleProblems(t *testing.T) {
	wf, err := New(Options{
		Name: "many-problems",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "missing",
				Store:    "state.result",
				Parameters: map[string]any{
					"x": "$(!!bogus!!)",
				},
			},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.Error(t, err)

	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	require.GreaterOrEqual(t, len(ve.Problems), 3)
	require.True(t, errors.Is(err, ErrUnknownActivity))
	require.True(t, errors.Is(err, ErrInvalidExpression))
	require.True(t, errors.Is(err, ErrInvalidStorePath))
}

func TestBindingValidation_WaitSignalNoStoreWarnsNotErrors(t *testing.T) {
	// No SignalStore configured — binding validation must NOT fail the
	// build, only log a warning.
	wf, err := New(Options{
		Name: "wait-no-store",
		Steps: []*Step{
			{
				Name: "gate",
				WaitSignal: &WaitSignalConfig{
					Topic:   "approved",
					Timeout: time.Second,
				},
			},
		},
	})
	require.NoError(t, err)

	_, err = NewExecution(wf, bindingReg(), WithScriptCompiler(newTestCompiler()))
	require.NoError(t, err)
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
