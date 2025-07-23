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
				{Step: "step-a", Condition: "state.value > 10"}, // matches (15 > 10)
				{Step: "step-b", Condition: "state.value < 20"}, // matches (15 < 20)
				{Step: "step-c", Condition: "state.value > 20"}, // doesn't match (15 > 20)
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
				{Step: "step-a", Condition: "state.value > 10"}, // matches first (15 > 10)
				{Step: "step-b", Condition: "state.value < 20"}, // would also match but should be skipped
				{Step: "step-c", Condition: "state.value > 20"}, // doesn't match
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
				{Step: "step-a"}, // unconditional, matches first
				{Step: "step-b", Condition: "state.value < 20"}, // would match but should be skipped
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
				{Step: "step-a", Condition: "state.value > 20"}, // doesn't match
				{Step: "step-b", Condition: "state.value < 10"}, // doesn't match
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
				{Step: "step-a", Condition: "state.value > 10"}, // matches
				{Step: "step-b", Condition: "state.value < 20"}, // matches
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

// MockActivity for testing executeStepEach
type MockActivity struct {
	calls []map[string]any
}

func (m *MockActivity) Name() string {
	return "mock-activity"
}

func (m *MockActivity) Execute(ctx Context, params map[string]any) (any, error) {
	m.calls = append(m.calls, copyMap(params))
	// Return the item parameter for easy verification
	if item, ok := params["item"]; ok {
		return item, nil
	}
	return "executed", nil
}

// MockContext for testing
type MockContext struct {
	context.Context
	variables map[string]any
}

func (m *MockContext) SetVariable(key string, value any) {
	if m.variables == nil {
		m.variables = make(map[string]any)
	}
	m.variables[key] = value
}

func (m *MockContext) DeleteVariable(key string) {
	delete(m.variables, key)
}

func (m *MockContext) ListVariables() []string {
	var keys []string
	for k := range m.variables {
		keys = append(keys, k)
	}
	return keys
}

func (m *MockContext) GetVariable(key string) (any, bool) {
	v, ok := m.variables[key]
	return v, ok
}

func (m *MockContext) ListInputs() []string {
	return []string{}
}

func (m *MockContext) GetInput(key string) (any, bool) {
	return nil, false
}

func (m *MockContext) GetLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (m *MockContext) GetCompiler() script.Compiler {
	return script.NewRisorScriptingEngine(script.DefaultRisorGlobals())
}

func (m *MockContext) GetPathID() string {
	return "test-path"
}

func (m *MockContext) GetStepName() string {
	return "test-step"
}

func TestExecuteStepEach(t *testing.T) {
	// Create test workflow and step
	workflow := &Workflow{name: "test"}
	mockActivity := &MockActivity{}

	pathOpts := PathOptions{
		Workflow: workflow,
		Variables: map[string]any{
			"options": []string{"apple", "banana", "cherry"},
			"fruit":   "mango",
		},
		Inputs: map[string]any{},
		ActivityRegistry: map[string]Activity{
			"test-activity": mockActivity,
		},
		ScriptCompiler:   script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		UpdatesChannel:   make(chan PathSnapshot, 1),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		ActivityExecutor: &MockActivityExecutor{},
	}

	ctx := context.Background()

	t.Run("basic each execution with string array", func(t *testing.T) {
		step := &Step{
			Name:     "test-step",
			Activity: "test-activity",
			Each: &Each{
				Items: []string{"item1", "item2", "item3"},
			},
			Parameters: map[string]interface{}{
				"action": "process",
			},
		}

		path := NewPath("test-path", step, pathOpts)

		// Reset mock calls
		mockActivity.calls = nil

		result, err := path.executeStepEach(ctx, step)

		require.NoError(t, err)
		require.Len(t, result, 3, "Should return results for all 3 items")

		// Verify activity was called 3 times
		require.Len(t, mockActivity.calls, 3, "Activity should be called once per item")

		// Verify each call had the correct parameters
		for _, call := range mockActivity.calls {
			require.Equal(t, "process", call["action"], "Should preserve step parameters")
		}
	})

	t.Run("each execution with 'as' parameter", func(t *testing.T) {
		step := &Step{
			Name:     "test-step",
			Activity: "test-activity",
			Each: &Each{
				Items: "$(state.options)",
				As:    "fruit",
			},
			Parameters: map[string]interface{}{
				"item": "$(state.fruit)",
			},
			Store: "the_results",
		}

		path := NewPath("test-path", step, pathOpts)

		// Verify original "fruit" variable
		fruit, ok := path.state.GetVariable("fruit")
		require.True(t, ok)
		require.Equal(t, "mango", fruit)

		// Reset mock calls
		mockActivity.calls = nil
		result, err := path.executeStepEach(ctx, step)
		require.NoError(t, err)
		require.Len(t, result, 3, "Should return results for all 3 items")

		// Verify activity was called 3 times with correct 'as' parameter
		require.Len(t, mockActivity.calls, 3, "Activity should be called once per item")

		expectedItems := []string{"apple", "banana", "cherry"}
		for i, call := range mockActivity.calls {
			require.Equal(t, expectedItems[i], call["item"], "Should pass item as 'item' parameter")
		}

		// Original "fruit" variable should be restored
		fruit, ok = path.state.GetVariable("fruit")
		require.True(t, ok)
		require.Equal(t, "mango", fruit)

		// Verify the_results variable
		results, ok := path.state.GetVariable("the_results")
		require.True(t, ok)
		require.Equal(t, []any{"apple", "banana", "cherry"}, results)
	})
}

// MockActivityExecutor for testing
type MockActivityExecutor struct{}

func (m *MockActivityExecutor) ExecuteActivity(ctx context.Context, stepName, pathID string, activity Activity, params map[string]interface{}, pathState *PathLocalState) (interface{}, error) {
	// Create a mock context that implements the workflow Context interface
	mockCtx := &MockContext{Context: ctx}
	return activity.Execute(mockCtx, params)
}

func TestExecuteCatchHandler(t *testing.T) {
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
		Workflow:         workflow,
		Variables:        map[string]any{},
		Inputs:           map[string]any{},
		ScriptCompiler:   script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		UpdatesChannel:   make(chan PathSnapshot, 10),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		ActivityRegistry: map[string]Activity{},
	}

	t.Run("successful catch handler execution with timeout error", func(t *testing.T) {
		// Step with catch configuration for timeout errors
		currentStep := &Step{
			Name: "current-step",
			Catch: []*CatchConfig{
				{
					ErrorEquals: []string{ErrorTypeTimeout},
					Next:        "step-a",
					Store:       "state.last_error",
				},
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)

		// Create a timeout error
		timeoutErr := NewWorkflowError(ErrorTypeTimeout, "operation timed out")

		result, err := path.executeCatchHandler(currentStep, timeoutErr)

		// Should return catchErrorSentinel (successful catch handling)
		require.NoError(t, err)
		require.Equal(t, catchErrorSentinel, result)

		// Should transition to the catch step
		require.Equal(t, "step-a", path.currentStep.Name)

		// Should store error in path variables
		storedError, exists := path.state.GetVariable("last_error")
		require.True(t, exists, "Error should be stored in variables")

		// Verify stored error structure
		errorOutput, ok := storedError.(ErrorOutput)
		require.True(t, ok, "Stored error should be ErrorOutput type")
		require.Equal(t, ErrorTypeTimeout, errorOutput.Error)
		require.Equal(t, "operation timed out", errorOutput.Cause)
	})

	t.Run("successful catch handler execution with activity failed error", func(t *testing.T) {
		// Step with catch configuration for activity failed errors
		currentStep := &Step{
			Name: "current-step",
			Catch: []*CatchConfig{
				{
					ErrorEquals: []string{ErrorTypeActivityFailed},
					Next:        "step-b",
					Store:       "error_info",
				},
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)

		// Create an activity failed error
		activityErr := NewWorkflowError(ErrorTypeActivityFailed, "activity execution failed")

		result, err := path.executeCatchHandler(currentStep, activityErr)

		// Should return catchErrorSentinel (successful catch handling)
		require.NoError(t, err)
		require.Equal(t, catchErrorSentinel, result)

		// Should transition to the catch step
		require.Equal(t, "step-b", path.currentStep.Name)

		// Should store error in path variables (without "state." prefix)
		storedError, exists := path.state.GetVariable("error_info")
		require.True(t, exists, "Error should be stored in variables")

		errorOutput := storedError.(ErrorOutput)
		require.Equal(t, ErrorTypeActivityFailed, errorOutput.Error)
		require.Equal(t, "activity execution failed", errorOutput.Cause)
	})

	t.Run("catch handler without error storage", func(t *testing.T) {
		// Step with catch configuration that doesn't store errors
		currentStep := &Step{
			Name: "current-step",
			Catch: []*CatchConfig{
				{
					ErrorEquals: []string{ErrorTypeAll},
					Next:        "step-c",
					// No Store field
				},
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)

		// Create any error
		someErr := NewWorkflowError(ErrorTypeActivityFailed, "some error occurred")

		result, err := path.executeCatchHandler(currentStep, someErr)

		// Should return catchErrorSentinel (successful catch handling)
		require.NoError(t, err)
		require.Equal(t, catchErrorSentinel, result)

		// Should transition to the catch step
		require.Equal(t, "step-c", path.currentStep.Name)

		// Should not store anything in variables
		_, exists := path.state.GetVariable("error_info")
		require.False(t, exists, "No error should be stored when Store is not specified")
	})

	t.Run("multiple catch handlers - first match wins", func(t *testing.T) {
		// Step with multiple catch configurations
		currentStep := &Step{
			Name: "current-step",
			Catch: []*CatchConfig{
				{
					ErrorEquals: []string{ErrorTypeTimeout},
					Next:        "step-a",
					Store:       "timeout_error",
				},
				{
					ErrorEquals: []string{ErrorTypeAll},
					Next:        "step-b",
					Store:       "general_error",
				},
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)

		// Create a timeout error (should match first handler)
		timeoutErr := NewWorkflowError(ErrorTypeTimeout, "timeout occurred")

		result, err := path.executeCatchHandler(currentStep, timeoutErr)

		// Should return catchErrorSentinel
		require.NoError(t, err)
		require.Equal(t, catchErrorSentinel, result)

		// Should use first matching handler (step-a, not step-b)
		require.Equal(t, "step-a", path.currentStep.Name)

		// Should store in timeout_error, not general_error
		_, timeoutExists := path.state.GetVariable("timeout_error")
		_, generalExists := path.state.GetVariable("general_error")
		require.True(t, timeoutExists, "Should store in first matching handler")
		require.False(t, generalExists, "Should not store in second handler")
	})

	t.Run("no matching catch handler returns original error", func(t *testing.T) {
		// Step with catch configuration for timeout errors only
		currentStep := &Step{
			Name: "current-step",
			Catch: []*CatchConfig{
				{
					ErrorEquals: []string{ErrorTypeTimeout},
					Next:        "step-a",
				},
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)

		// Create an activity failed error (doesn't match timeout)
		activityErr := NewWorkflowError(ErrorTypeActivityFailed, "activity failed")

		result, err := path.executeCatchHandler(currentStep, activityErr)

		// Should return the original error
		require.Error(t, err)
		require.Equal(t, activityErr, err)
		require.Nil(t, result)

		// Should not change the current step
		require.Equal(t, "current-step", path.currentStep.Name)
	})

	t.Run("catch handler with non-existent next step returns error", func(t *testing.T) {
		// Step with catch configuration pointing to non-existent step
		currentStep := &Step{
			Name: "current-step",
			Catch: []*CatchConfig{
				{
					ErrorEquals: []string{ErrorTypeAll},
					Next:        "non-existent-step",
				},
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)

		// Create any error
		someErr := NewWorkflowError(ErrorTypeActivityFailed, "some error")

		result, err := path.executeCatchHandler(currentStep, someErr)

		// Should return an error about missing step
		require.Error(t, err)
		require.Contains(t, err.Error(), "catch handler step \"non-existent-step\" not found")
		require.Nil(t, result)

		// Should not change the current step
		require.Equal(t, "current-step", path.currentStep.Name)
	})

	t.Run("custom error type matching", func(t *testing.T) {
		// Step with catch configuration for custom error type
		currentStep := &Step{
			Name: "current-step",
			Catch: []*CatchConfig{
				{
					ErrorEquals: []string{"permission-denied"},
					Next:        "step-a",
					Store:       "permission_error",
				},
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)

		// Create a custom error type
		customErr := NewWorkflowError("permission-denied", "access forbidden")

		result, err := path.executeCatchHandler(currentStep, customErr)

		// Should return catchErrorSentinel
		require.NoError(t, err)
		require.Equal(t, catchErrorSentinel, result)

		// Should transition to the catch step
		require.Equal(t, "step-a", path.currentStep.Name)

		// Should store custom error
		storedError, exists := path.state.GetVariable("permission_error")
		require.True(t, exists)

		errorOutput := storedError.(ErrorOutput)
		require.Equal(t, "permission-denied", errorOutput.Error)
		require.Equal(t, "access forbidden", errorOutput.Cause)
	})

	t.Run("fatal error does not match ErrorTypeAll", func(t *testing.T) {
		// Step with catch configuration for all errors
		currentStep := &Step{
			Name: "current-step",
			Catch: []*CatchConfig{
				{
					ErrorEquals: []string{ErrorTypeAll},
					Next:        "step-a",
				},
			},
		}

		path := NewPath("test-path", currentStep, pathOpts)

		// Create a fatal error (should not match ErrorTypeAll)
		fatalErr := NewWorkflowError(ErrorTypeFatal, "fatal system error")

		result, err := path.executeCatchHandler(currentStep, fatalErr)

		// Should return the original error (no match)
		require.Error(t, err)
		require.Equal(t, fatalErr, err)
		require.Nil(t, result)

		// Should not change the current step
		require.Equal(t, "current-step", path.currentStep.Name)
	})
}
