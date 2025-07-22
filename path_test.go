package workflow

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/deepnoodle-ai/workflow/script"
	"github.com/stretchr/testify/require"
)

func TestTemplateParameterEvaluation(t *testing.T) {
	// Create a path with test data
	workflow := &Workflow{name: "test"}
	step := &Step{Name: "test-step"}

	pathOpts := PathOptions{
		Workflow: workflow,
		Variables: map[string]any{
			"user_name": "Alice",
			"count":     42,
			"items":     []string{"a", "b", "c"},
		},
		Inputs: map[string]any{
			"base_url": "https://api.example.com",
		},
		ScriptCompiler:   script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		UpdatesChannel:   make(chan PathSnapshot, 1),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		ActivityRegistry: map[string]Activity{},
	}
	path := NewPath("test-path", step, pathOpts)

	ctx := context.Background()

	t.Run("script expression returns actual value", func(t *testing.T) {
		result, err := path.evaluateParameterValue(ctx, "$(state.count * 2)", "test-step", "param")
		require.NoError(t, err)
		require.Equal(t, int64(84), result) // Risor returns int64
	})

	t.Run("script expression returns string", func(t *testing.T) {
		result, err := path.evaluateParameterValue(ctx, "$(state.user_name)", "test-step", "param")
		require.NoError(t, err)
		require.Equal(t, "Alice", result)
	})

	t.Run("script expression returns complex value", func(t *testing.T) {
		result, err := path.evaluateParameterValue(ctx, "$(state.count)", "test-step", "param")
		require.NoError(t, err)
		// Should return the actual integer value, not a string
		require.Equal(t, int64(42), result) // Risor returns int64
	})

	t.Run("template string with variable substitution", func(t *testing.T) {
		result, err := path.evaluateParameterValue(ctx, "${inputs.base_url}/users/${state.user_name}", "test-step", "param")
		require.NoError(t, err)
		require.Equal(t, "https://api.example.com/users/Alice", result)
	})

	t.Run("non-string parameter passes through unchanged", func(t *testing.T) {
		intParam := 123
		result, err := path.evaluateParameterValue(ctx, intParam, "test-step", "param")
		require.NoError(t, err)
		require.Equal(t, 123, result)

		boolParam := true
		result, err = path.evaluateParameterValue(ctx, boolParam, "test-step", "param")
		require.NoError(t, err)
		require.Equal(t, true, result)
	})

	t.Run("malformed script expression returns error", func(t *testing.T) {
		_, err := path.evaluateParameterValue(ctx, "$(1 + )", "test-step", "param")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to compile script expression")
	})

	t.Run("undefined variable in script returns error", func(t *testing.T) {
		_, err := path.evaluateParameterValue(ctx, "$(state.undefined_var)", "test-step", "param")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to evaluate script expression")
	})
}

func TestEachBlockItemResolution(t *testing.T) {
	// Create a path with test data
	workflow := &Workflow{name: "test"}
	step := &Step{Name: "test-step"}

	pathOpts := PathOptions{
		Workflow: workflow,
		Variables: map[string]any{
			"names":   []string{"Alice", "Bob", "Charlie"},
			"numbers": []any{1, 2, 3, 4, 5},
		},
		Inputs:           map[string]any{},
		ScriptCompiler:   script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		UpdatesChannel:   make(chan PathSnapshot, 1),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		ActivityRegistry: map[string]Activity{},
	}
	path := NewPath("test-path", step, pathOpts)

	ctx := context.Background()

	t.Run("direct string array", func(t *testing.T) {
		each := &Each{
			Items: []string{"one", "two", "three"},
		}
		items, err := path.resolveEachItems(ctx, each)
		require.NoError(t, err)
		require.Len(t, items, 3)
		require.Equal(t, "one", items[0])
		require.Equal(t, "two", items[1])
		require.Equal(t, "three", items[2])
	})

	t.Run("direct any array", func(t *testing.T) {
		each := &Each{
			Items: []any{"string", 42, true},
		}
		items, err := path.resolveEachItems(ctx, each)
		require.NoError(t, err)
		require.Len(t, items, 3)
		require.Equal(t, "string", items[0])
		require.Equal(t, 42, items[1])
		require.Equal(t, true, items[2])
	})

	t.Run("script expression evaluating to array", func(t *testing.T) {
		each := &Each{
			Items: "$(state.names)",
		}
		items, err := path.resolveEachItems(ctx, each)
		require.NoError(t, err)
		require.Len(t, items, 3)
		require.Equal(t, "Alice", items[0])
		require.Equal(t, "Bob", items[1])
		require.Equal(t, "Charlie", items[2])
	})

	t.Run("script expression with range", func(t *testing.T) {
		each := &Each{
			Items: "$([1,2,3])",
		}
		items, err := path.resolveEachItems(ctx, each)
		require.NoError(t, err)
		require.Len(t, items, 3)
	})

	t.Run("invalid script expression returns error", func(t *testing.T) {
		each := &Each{
			Items: "$(invalid syntax",
		}
		_, err := path.resolveEachItems(ctx, each)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid script expression for 'each' block")
	})

	t.Run("single value to iterate over - script expression", func(t *testing.T) {
		each := &Each{
			Items: "$(42)", // Returns a number, not an array
		}
		items, err := path.resolveEachItems(ctx, each)
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, int64(42), items[0])
	})

	t.Run("single value to iterate over", func(t *testing.T) {
		each := &Each{
			Items: 42, // Neither string nor array
		}
		items, err := path.resolveEachItems(ctx, each)
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, 42, items[0])
	})
}

func TestPathConditionEvaluation(t *testing.T) {
	// Create a path with test data
	workflow := &Workflow{name: "test"}
	step := &Step{Name: "test-step"}

	path := NewPath("test-path", step, PathOptions{
		Workflow:         workflow,
		Variables:        map[string]any{"count": 5, "enabled": true},
		Inputs:           map[string]any{"threshold": 3},
		ScriptCompiler:   script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		UpdatesChannel:   make(chan PathSnapshot, 1),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		ActivityRegistry: map[string]Activity{},
	})

	ctx := context.Background()

	t.Run("simple boolean true", func(t *testing.T) {
		result, err := path.evaluateCondition(ctx, "true")
		require.NoError(t, err)
		require.True(t, result)
	})

	t.Run("simple boolean false", func(t *testing.T) {
		result, err := path.evaluateCondition(ctx, "false")
		require.NoError(t, err)
		require.False(t, result)
	})

	t.Run("script expression with state variables", func(t *testing.T) {
		result, err := path.evaluateCondition(ctx, "$(state.count > 3)")
		require.NoError(t, err)
		require.True(t, result)
	})

	t.Run("script expression with input variables", func(t *testing.T) {
		result, err := path.evaluateCondition(ctx, "$(inputs.threshold < state.count)")
		require.NoError(t, err)
		require.True(t, result)
	})

	t.Run("script expression evaluating to false", func(t *testing.T) {
		result, err := path.evaluateCondition(ctx, "$(state.count > 10)")
		require.NoError(t, err)
		require.False(t, result)
	})

	t.Run("malformed expression returns error", func(t *testing.T) {
		_, err := path.evaluateCondition(ctx, "$(invalid syntax")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to compile condition")
	})

	t.Run("undefined variable returns error", func(t *testing.T) {
		_, err := path.evaluateCondition(ctx, "$(state.undefined_var > 0)")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to evaluate condition")
	})
}

func TestRetryConfigurationMatching(t *testing.T) {
	// Create a path for testing retry config matching
	workflow := &Workflow{name: "test"}
	step := &Step{Name: "test-step"}

	path := NewPath("test-path", step, PathOptions{
		Workflow:         workflow,
		Variables:        map[string]any{},
		Inputs:           map[string]any{},
		UpdatesChannel:   make(chan PathSnapshot, 1),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		ActivityRegistry: map[string]Activity{},
	})

	retryConfigs := []*RetryConfig{
		{
			ErrorEquals: []string{"timeout"},
			MaxRetries:  3,
		},
		{
			ErrorEquals: []string{"permission-denied"},
			MaxRetries:  1,
		},
		{
			ErrorEquals: []string{}, // empty means ErrorTypeAll
			MaxRetries:  2,
		},
	}

	t.Run("timeout error matches timeout config", func(t *testing.T) {
		timeoutErr := NewWorkflowError(ErrorTypeTimeout, "operation timed out")
		config := path.findMatchingRetryConfig(timeoutErr, retryConfigs)
		require.NotNil(t, config)
		require.Equal(t, 3, config.MaxRetries)
		require.Contains(t, config.ErrorEquals, "timeout")
	})

	t.Run("custom error matches exact type", func(t *testing.T) {
		permissionErr := NewWorkflowError("permission-denied", "access forbidden")
		config := path.findMatchingRetryConfig(permissionErr, retryConfigs)
		require.NotNil(t, config)
		require.Equal(t, 1, config.MaxRetries)
		require.Contains(t, config.ErrorEquals, "permission-denied")
	})

	t.Run("activity failed error matches ErrorTypeAll config", func(t *testing.T) {
		activityErr := NewWorkflowError(ErrorTypeActivityFailed, "activity execution failed")
		config := path.findMatchingRetryConfig(activityErr, retryConfigs)
		require.NotNil(t, config)
		require.Equal(t, 2, config.MaxRetries)
		require.Empty(t, config.ErrorEquals) // empty means ErrorTypeAll
	})

	t.Run("fatal error matches no config", func(t *testing.T) {
		fatalErr := NewWorkflowError(ErrorTypeFatal, "fatal system error")
		config := path.findMatchingRetryConfig(fatalErr, retryConfigs)
		require.Nil(t, config)
	})

	t.Run("unmatched custom error returns nil", func(t *testing.T) {
		customErr := NewWorkflowError("unknown-error", "some unknown error")
		config := path.findMatchingRetryConfig(customErr, []*RetryConfig{
			{ErrorEquals: []string{"timeout"}, MaxRetries: 1},
		})
		require.Nil(t, config)
	})
}

func TestEdgeMatchingStrategies(t *testing.T) {
	// Create test workflow steps
	stepA := &Step{Name: "step-a"}
	stepB := &Step{Name: "step-b"}
	stepC := &Step{Name: "step-c"}

	// Test workflow with steps
	workflow := &Workflow{
		name: "test-workflow",
		stepsByName: map[string]*Step{
			"step-a": stepA,
			"step-b": stepB,
			"step-c": stepC,
		},
	}

	// Create path options
	pathOpts := PathOptions{
		Workflow: workflow,
		Variables: map[string]any{
			"value": 15,
		},
		Inputs:           map[string]any{},
		ScriptCompiler:   script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		UpdatesChannel:   make(chan PathSnapshot, 10),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		ActivityRegistry: map[string]Activity{},
	}

	ctx := context.Background()

	t.Run("EdgeMatchingAll strategy follows all matching edges", func(t *testing.T) {
		// Step with multiple matching conditions using "all" strategy (default)
		currentStep := &Step{
			Name:                 "current-step",
			EdgeMatchingStrategy: EdgeMatchingAll,
			Next: []*Edge{
				{Step: "step-a", Condition: "state.value > 10"},  // matches (15 > 10)
				{Step: "step-b", Condition: "state.value < 20"},  // matches (15 < 20)
				{Step: "step-c", Condition: "state.value > 20"},  // doesn't match (15 > 20)
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)
		pathSpecs, err := path.handleBranching(ctx)

		require.NoError(t, err)
		require.Len(t, pathSpecs, 2, "Should create paths for both matching edges")
		
		// Check that we got the right steps
		stepNames := make([]string, len(pathSpecs))
		for i, spec := range pathSpecs {
			stepNames[i] = spec.Step.Name
		}
		require.Contains(t, stepNames, "step-a")
		require.Contains(t, stepNames, "step-b")
		require.NotContains(t, stepNames, "step-c")
	})

	t.Run("EdgeMatchingFirst strategy follows only first matching edge", func(t *testing.T) {
		// Step with multiple matching conditions using "first" strategy
		currentStep := &Step{
			Name:                 "current-step",
			EdgeMatchingStrategy: EdgeMatchingFirst,
			Next: []*Edge{
				{Step: "step-a", Condition: "state.value > 10"},  // matches first (15 > 10)
				{Step: "step-b", Condition: "state.value < 20"},  // would also match but should be skipped
				{Step: "step-c", Condition: "state.value > 20"},  // doesn't match
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)
		pathSpecs, err := path.handleBranching(ctx)

		require.NoError(t, err)
		require.Len(t, pathSpecs, 1, "Should create path for only first matching edge")
		require.Equal(t, "step-a", pathSpecs[0].Step.Name, "Should follow first matching edge")
	})

	t.Run("EdgeMatchingFirst with unconditional edge first", func(t *testing.T) {
		// Step with unconditional edge first
		currentStep := &Step{
			Name:                 "current-step",
			EdgeMatchingStrategy: EdgeMatchingFirst,
			Next: []*Edge{
				{Step: "step-a"},                                 // unconditional, matches first
				{Step: "step-b", Condition: "state.value < 20"},  // would match but should be skipped
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)
		pathSpecs, err := path.handleBranching(ctx)

		require.NoError(t, err)
		require.Len(t, pathSpecs, 1, "Should create path for only first edge")
		require.Equal(t, "step-a", pathSpecs[0].Step.Name, "Should follow unconditional edge")
	})

	t.Run("EdgeMatchingFirst with no matches", func(t *testing.T) {
		// Step with no matching conditions
		currentStep := &Step{
			Name:                 "current-step",
			EdgeMatchingStrategy: EdgeMatchingFirst,
			Next: []*Edge{
				{Step: "step-a", Condition: "state.value > 20"},  // doesn't match
				{Step: "step-b", Condition: "state.value < 10"},  // doesn't match
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)
		pathSpecs, err := path.handleBranching(ctx)

		require.NoError(t, err)
		require.Len(t, pathSpecs, 0, "Should create no paths when no edges match")
	})

	t.Run("Default strategy is EdgeMatchingAll", func(t *testing.T) {
		// Step with no explicit strategy (should default to "all")
		currentStep := &Step{
			Name: "current-step",
			// EdgeMatchingStrategy not set - should default to "all"
			Next: []*Edge{
				{Step: "step-a", Condition: "state.value > 10"},  // matches
				{Step: "step-b", Condition: "state.value < 20"},  // matches
			},
		}

		require.Equal(t, EdgeMatchingAll, currentStep.GetEdgeMatchingStrategy(), 
			"Should default to EdgeMatchingAll")

		path := NewPath("test-path", currentStep, pathOpts)
		pathSpecs, err := path.handleBranching(ctx)

		require.NoError(t, err)
		require.Len(t, pathSpecs, 2, "Should create paths for all matching edges by default")
	})
}
