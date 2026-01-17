package workflow

import (
	"context"
	"io"
	"log/slog"
	"math/rand"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/script"
	"github.com/deepnoodle-ai/wonton/assert"
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
		assert.NoError(t, err)
		assert.Equal(t, result, int64(84)) // Risor returns int64
	})

	t.Run("script expression returns string", func(t *testing.T) {
		result, err := path.evaluateParameterValue(ctx, "$(state.user_name)", "test-step", "param")
		assert.NoError(t, err)
		assert.Equal(t, result, "Alice")
	})

	t.Run("script expression returns complex value", func(t *testing.T) {
		result, err := path.evaluateParameterValue(ctx, "$(state.count)", "test-step", "param")
		assert.NoError(t, err)
		// Should return the actual integer value, not a string
		assert.Equal(t, result, int64(42)) // Risor returns int64
	})

	t.Run("template string with variable substitution", func(t *testing.T) {
		result, err := path.evaluateParameterValue(ctx, "${inputs.base_url}/users/${state.user_name}", "test-step", "param")
		assert.NoError(t, err)
		assert.Equal(t, result, "https://api.example.com/users/Alice")
	})

	t.Run("non-string parameter passes through unchanged", func(t *testing.T) {
		intParam := 123
		result, err := path.evaluateParameterValue(ctx, intParam, "test-step", "param")
		assert.NoError(t, err)
		assert.Equal(t, result, 123)

		boolParam := true
		result, err = path.evaluateParameterValue(ctx, boolParam, "test-step", "param")
		assert.NoError(t, err)
		assert.Equal(t, result, true)
	})

	t.Run("malformed script expression returns error", func(t *testing.T) {
		_, err := path.evaluateParameterValue(ctx, "$(1 + )", "test-step", "param")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to compile script expression")
	})

	t.Run("undefined variable in script returns error", func(t *testing.T) {
		_, err := path.evaluateParameterValue(ctx, "$(state.undefined_var)", "test-step", "param")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to evaluate script expression")
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
		assert.NoError(t, err)
		assert.Len(t, items, 3)
		assert.Equal(t, items[0], "one")
		assert.Equal(t, items[1], "two")
		assert.Equal(t, items[2], "three")
	})

	t.Run("direct any array", func(t *testing.T) {
		each := &Each{
			Items: []any{"string", 42, true},
		}
		items, err := path.resolveEachItems(ctx, each)
		assert.NoError(t, err)
		assert.Len(t, items, 3)
		assert.Equal(t, items[0], "string")
		assert.Equal(t, items[1], 42)
		assert.Equal(t, items[2], true)
	})

	t.Run("script expression evaluating to array", func(t *testing.T) {
		each := &Each{
			Items: "$(state.names)",
		}
		items, err := path.resolveEachItems(ctx, each)
		assert.NoError(t, err)
		assert.Len(t, items, 3)
		assert.Equal(t, items[0], "Alice")
		assert.Equal(t, items[1], "Bob")
		assert.Equal(t, items[2], "Charlie")
	})

	t.Run("script expression with range", func(t *testing.T) {
		each := &Each{
			Items: "$([1,2,3])",
		}
		items, err := path.resolveEachItems(ctx, each)
		assert.NoError(t, err)
		assert.Len(t, items, 3)
	})

	t.Run("invalid script expression returns error", func(t *testing.T) {
		each := &Each{
			Items: "$(invalid syntax",
		}
		_, err := path.resolveEachItems(ctx, each)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid script expression for 'each' block")
	})

	t.Run("single value to iterate over - script expression", func(t *testing.T) {
		each := &Each{
			Items: "$(42)", // Returns a number, not an array
		}
		items, err := path.resolveEachItems(ctx, each)
		assert.NoError(t, err)
		assert.Len(t, items, 1)
		assert.Equal(t, items[0], int64(42))
	})

	t.Run("single value to iterate over", func(t *testing.T) {
		each := &Each{
			Items: 42, // Neither string nor array
		}
		items, err := path.resolveEachItems(ctx, each)
		assert.NoError(t, err)
		assert.Len(t, items, 1)
		assert.Equal(t, items[0], 42)
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
		assert.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("simple boolean false", func(t *testing.T) {
		result, err := path.evaluateCondition(ctx, "false")
		assert.NoError(t, err)
		assert.False(t, result)
	})

	t.Run("script expression with state variables", func(t *testing.T) {
		result, err := path.evaluateCondition(ctx, "$(state.count > 3)")
		assert.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("script expression with input variables", func(t *testing.T) {
		result, err := path.evaluateCondition(ctx, "$(inputs.threshold < state.count)")
		assert.NoError(t, err)
		assert.True(t, result)
	})

	t.Run("script expression evaluating to false", func(t *testing.T) {
		result, err := path.evaluateCondition(ctx, "$(state.count > 10)")
		assert.NoError(t, err)
		assert.False(t, result)
	})

	t.Run("malformed expression returns error", func(t *testing.T) {
		_, err := path.evaluateCondition(ctx, "$(invalid syntax")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to compile condition")
	})

	t.Run("undefined variable returns error", func(t *testing.T) {
		_, err := path.evaluateCondition(ctx, "$(state.undefined_var > 0)")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to evaluate condition")
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
		assert.NotNil(t, config)
		assert.Equal(t, config.MaxRetries, 3)
		assert.Contains(t, config.ErrorEquals, "timeout")
	})

	t.Run("custom error matches exact type", func(t *testing.T) {
		permissionErr := NewWorkflowError("permission-denied", "access forbidden")
		config := path.findMatchingRetryConfig(permissionErr, retryConfigs)
		assert.NotNil(t, config)
		assert.Equal(t, config.MaxRetries, 1)
		assert.Contains(t, config.ErrorEquals, "permission-denied")
	})

	t.Run("activity failed error matches ErrorTypeAll config", func(t *testing.T) {
		activityErr := NewWorkflowError(ErrorTypeActivityFailed, "activity execution failed")
		config := path.findMatchingRetryConfig(activityErr, retryConfigs)
		assert.NotNil(t, config)
		assert.Equal(t, config.MaxRetries, 2)
		assert.Empty(t, config.ErrorEquals) // empty means ErrorTypeAll
	})

	t.Run("fatal error matches no config", func(t *testing.T) {
		fatalErr := NewWorkflowError(ErrorTypeFatal, "fatal system error")
		config := path.findMatchingRetryConfig(fatalErr, retryConfigs)
		assert.Nil(t, config)
	})

	t.Run("unmatched custom error returns nil", func(t *testing.T) {
		customErr := NewWorkflowError("unknown-error", "some unknown error")
		config := path.findMatchingRetryConfig(customErr, []*RetryConfig{
			{ErrorEquals: []string{"timeout"}, MaxRetries: 1},
		})
		assert.Nil(t, config)
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

		assert.NoError(t, err)
		assert.Len(t, pathSpecs, 2, "Should create paths for both matching edges")

		// Check that we got the right steps
		stepNames := make([]string, len(pathSpecs))
		for i, spec := range pathSpecs {
			stepNames[i] = spec.Step.Name
		}
		assert.Contains(t, stepNames, "step-a")
		assert.Contains(t, stepNames, "step-b")
		assert.NotContains(t, stepNames, "step-c")
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

		assert.NoError(t, err)
		assert.Len(t, pathSpecs, 1, "Should create path for only first matching edge")
		assert.Equal(t, pathSpecs[0].Step.Name, "step-a", "Should follow first matching edge")
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

		assert.NoError(t, err)
		assert.Len(t, pathSpecs, 1, "Should create path for only first edge")
		assert.Equal(t, pathSpecs[0].Step.Name, "step-a", "Should follow unconditional edge")
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

		assert.NoError(t, err)
		assert.Len(t, pathSpecs, 0, "Should create no paths when no edges match")
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

		assert.Equal(t, currentStep.GetEdgeMatchingStrategy(), EdgeMatchingAll,
			"Should default to EdgeMatchingAll")

		path := NewPath("test-path", currentStep, pathOpts)
		pathSpecs, err := path.handleBranching(ctx)

		assert.NoError(t, err)
		assert.Len(t, pathSpecs, 2, "Should create paths for all matching edges by default")
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

func (m *MockContext) Clock() Clock {
	return NewRealClock()
}

func (m *MockContext) GetExecutionID() string {
	return "test-execution"
}

func (m *MockContext) Now() time.Time {
	return m.Clock().Now()
}

func (m *MockContext) DeterministicID(prefix string) string {
	return prefix + "_test_id"
}

func (m *MockContext) Rand() *rand.Rand {
	return rand.New(rand.NewSource(12345))
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

		assert.NoError(t, err)
		assert.Len(t, result, 3, "Should return results for all 3 items")

		// Verify activity was called 3 times
		assert.Len(t, mockActivity.calls, 3, "Activity should be called once per item")

		// Verify each call had the correct parameters
		for _, call := range mockActivity.calls {
			assert.Equal(t, call["action"], "process", "Should preserve step parameters")
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
		assert.True(t, ok)
		assert.Equal(t, fruit, "mango")

		// Reset mock calls
		mockActivity.calls = nil
		result, err := path.executeStepEach(ctx, step)
		assert.NoError(t, err)
		assert.Len(t, result, 3, "Should return results for all 3 items")

		// Verify activity was called 3 times with correct 'as' parameter
		assert.Len(t, mockActivity.calls, 3, "Activity should be called once per item")

		expectedItems := []string{"apple", "banana", "cherry"}
		for i, call := range mockActivity.calls {
			assert.Equal(t, call["item"], expectedItems[i], "Should pass item as 'item' parameter")
		}

		// Original "fruit" variable should be restored
		fruit, ok = path.state.GetVariable("fruit")
		assert.True(t, ok)
		assert.Equal(t, fruit, "mango")

		// Verify the_results variable
		results, ok := path.state.GetVariable("the_results")
		assert.True(t, ok)
		assert.Equal(t, results, []any{"apple", "banana", "cherry"})
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
		assert.NoError(t, err)
		assert.Equal(t, result, catchErrorSentinel)

		// Should transition to the catch step
		assert.Equal(t, path.currentStep.Name, "step-a")

		// Should store error in path variables
		storedError, exists := path.state.GetVariable("last_error")
		assert.True(t, exists, "Error should be stored in variables")

		// Verify stored error structure
		errorOutput, ok := storedError.(ErrorOutput)
		assert.True(t, ok, "Stored error should be ErrorOutput type")
		assert.Equal(t, errorOutput.Error, ErrorTypeTimeout)
		assert.Equal(t, errorOutput.Cause, "operation timed out")
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
		assert.NoError(t, err)
		assert.Equal(t, result, catchErrorSentinel)

		// Should transition to the catch step
		assert.Equal(t, path.currentStep.Name, "step-b")

		// Should store error in path variables (without "state." prefix)
		storedError, exists := path.state.GetVariable("error_info")
		assert.True(t, exists, "Error should be stored in variables")

		errorOutput := storedError.(ErrorOutput)
		assert.Equal(t, errorOutput.Error, ErrorTypeActivityFailed)
		assert.Equal(t, errorOutput.Cause, "activity execution failed")
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
		assert.NoError(t, err)
		assert.Equal(t, result, catchErrorSentinel)

		// Should transition to the catch step
		assert.Equal(t, path.currentStep.Name, "step-c")

		// Should not store anything in variables
		_, exists := path.state.GetVariable("error_info")
		assert.False(t, exists, "No error should be stored when Store is not specified")
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
		assert.NoError(t, err)
		assert.Equal(t, result, catchErrorSentinel)

		// Should use first matching handler (step-a, not step-b)
		assert.Equal(t, path.currentStep.Name, "step-a")

		// Should store in timeout_error, not general_error
		_, timeoutExists := path.state.GetVariable("timeout_error")
		_, generalExists := path.state.GetVariable("general_error")
		assert.True(t, timeoutExists, "Should store in first matching handler")
		assert.False(t, generalExists, "Should not store in second handler")
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
		assert.Error(t, err)
		assert.Equal(t, err, activityErr)
		assert.Nil(t, result)

		// Should not change the current step
		assert.Equal(t, path.currentStep.Name, "current-step")
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
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "catch handler step \"non-existent-step\" not found")
		assert.Nil(t, result)

		// Should not change the current step
		assert.Equal(t, path.currentStep.Name, "current-step")
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
		assert.NoError(t, err)
		assert.Equal(t, result, catchErrorSentinel)

		// Should transition to the catch step
		assert.Equal(t, path.currentStep.Name, "step-a")

		// Should store custom error
		storedError, exists := path.state.GetVariable("permission_error")
		assert.True(t, exists)

		errorOutput := storedError.(ErrorOutput)
		assert.Equal(t, errorOutput.Error, "permission-denied")
		assert.Equal(t, errorOutput.Cause, "access forbidden")
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
		assert.Error(t, err)
		assert.Equal(t, err, fatalErr)
		assert.Nil(t, result)

		// Should not change the current step
		assert.Equal(t, path.currentStep.Name, "current-step")
	})
}
