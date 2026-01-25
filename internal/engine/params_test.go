package engine

import (
	"testing"
)

func TestResolveParameters_SimpleString(t *testing.T) {
	ctx := &ResolutionContext{
		Inputs:      map[string]any{"name": "Alice"},
		Variables:   map[string]any{"count": 42},
		StepOutputs: map[string]any{},
	}

	// No substitution needed
	result := ResolveParameters("hello world", ctx)
	if result != "hello world" {
		t.Errorf("expected 'hello world', got '%v'", result)
	}
}

func TestResolveParameters_InputSubstitution(t *testing.T) {
	ctx := &ResolutionContext{
		Inputs:      map[string]any{"name": "Alice", "age": 30},
		Variables:   map[string]any{},
		StepOutputs: map[string]any{},
	}

	// Single expression returns actual type
	result := ResolveParameters("$(inputs.name)", ctx)
	if result != "Alice" {
		t.Errorf("expected 'Alice', got '%v'", result)
	}

	result = ResolveParameters("$(inputs.age)", ctx)
	if result != 30 {
		t.Errorf("expected 30, got '%v'", result)
	}
}

func TestResolveParameters_VariableSubstitution(t *testing.T) {
	ctx := &ResolutionContext{
		Inputs:    map[string]any{},
		Variables: map[string]any{"status": "ready", "items": []string{"a", "b"}},
	}

	result := ResolveParameters("$(state.status)", ctx)
	if result != "ready" {
		t.Errorf("expected 'ready', got '%v'", result)
	}

	result = ResolveParameters("$(vars.status)", ctx)
	if result != "ready" {
		t.Errorf("expected 'ready', got '%v'", result)
	}
}

func TestResolveParameters_StepOutputSubstitution(t *testing.T) {
	ctx := &ResolutionContext{
		Inputs:    map[string]any{},
		Variables: map[string]any{},
		StepOutputs: map[string]any{
			"fetch": map[string]any{
				"data":   "fetched-data",
				"count":  10,
				"nested": map[string]any{"deep": "value"},
			},
		},
	}

	result := ResolveParameters("$(steps.fetch.data)", ctx)
	if result != "fetched-data" {
		t.Errorf("expected 'fetched-data', got '%v'", result)
	}

	result = ResolveParameters("$(steps.fetch.count)", ctx)
	if result != 10 {
		t.Errorf("expected 10, got '%v'", result)
	}

	// Nested access
	result = ResolveParameters("$(steps.fetch.nested.deep)", ctx)
	if result != "value" {
		t.Errorf("expected 'value', got '%v'", result)
	}
}

func TestResolveParameters_StringInterpolation(t *testing.T) {
	ctx := &ResolutionContext{
		Inputs:    map[string]any{"name": "World"},
		Variables: map[string]any{},
	}

	// String interpolation (multiple expressions or embedded)
	result := ResolveParameters("Hello $(inputs.name)!", ctx)
	if result != "Hello World!" {
		t.Errorf("expected 'Hello World!', got '%v'", result)
	}
}

func TestResolveParameters_MissingValue(t *testing.T) {
	ctx := &ResolutionContext{
		Inputs:    map[string]any{},
		Variables: map[string]any{},
	}

	// Missing value keeps original expression
	result := ResolveParameters("$(inputs.missing)", ctx)
	if result != "$(inputs.missing)" {
		t.Errorf("expected original expression, got '%v'", result)
	}
}

func TestResolveParameters_MapRecursion(t *testing.T) {
	ctx := &ResolutionContext{
		Inputs: map[string]any{"url": "https://example.com"},
	}

	params := map[string]any{
		"endpoint": "$(inputs.url)",
		"nested": map[string]any{
			"value": "$(inputs.url)/path",
		},
	}

	result := ResolveParameters(params, ctx).(map[string]any)

	if result["endpoint"] != "https://example.com" {
		t.Errorf("expected url, got '%v'", result["endpoint"])
	}

	nested := result["nested"].(map[string]any)
	if nested["value"] != "https://example.com/path" {
		t.Errorf("expected url/path, got '%v'", nested["value"])
	}
}

func TestResolveParameters_ArrayRecursion(t *testing.T) {
	ctx := &ResolutionContext{
		Inputs: map[string]any{"item": "value"},
	}

	params := []any{"$(inputs.item)", "static", "$(inputs.item)"}
	result := ResolveParameters(params, ctx).([]any)

	if result[0] != "value" {
		t.Errorf("expected 'value', got '%v'", result[0])
	}
	if result[1] != "static" {
		t.Errorf("expected 'static', got '%v'", result[1])
	}
}

func TestResolveParameters_PathReference(t *testing.T) {
	ctx := &ResolutionContext{
		Inputs:    map[string]any{},
		Variables: map[string]any{},
		AllPaths: map[string]*PathOutputs{
			"path-a": {
				Variables: map[string]any{"pathVar": "from-a"},
				StepOutputs: map[string]any{
					"step1": map[string]any{"result": "path-a-result"},
				},
			},
		},
	}

	result := ResolveParameters("$(path.path-a.steps.step1.result)", ctx)
	if result != "path-a-result" {
		t.Errorf("expected 'path-a-result', got '%v'", result)
	}
}

func TestBuildResolutionContext(t *testing.T) {
	state := NewEngineExecutionState()
	state.CreatePath("main", "step1", map[string]any{"var1": "val1"})
	state.StoreStepOutput("main", "step1", map[string]any{"output": "data"})

	inputs := map[string]any{"input1": "inputVal"}

	ctx := BuildResolutionContext(inputs, state, "main")

	if ctx.Inputs["input1"] != "inputVal" {
		t.Error("expected inputs to be set")
	}

	if ctx.Variables["var1"] != "val1" {
		t.Error("expected variables to be set")
	}

	if ctx.StepOutputs["step1"] == nil {
		t.Error("expected step outputs to be set")
	}

	if ctx.AllPaths["main"] == nil {
		t.Error("expected all paths to be set")
	}
}

func TestResolveParameters_NonStringPassthrough(t *testing.T) {
	ctx := &ResolutionContext{}

	// Numbers pass through
	result := ResolveParameters(42, ctx)
	if result != 42 {
		t.Errorf("expected 42, got '%v'", result)
	}

	// Booleans pass through
	result = ResolveParameters(true, ctx)
	if result != true {
		t.Errorf("expected true, got '%v'", result)
	}

	// Nil passes through
	result = ResolveParameters(nil, ctx)
	if result != nil {
		t.Errorf("expected nil, got '%v'", result)
	}
}
