package engine

import (
	"testing"

	"github.com/deepnoodle-ai/workflow/domain"
)

// mockStep implements domain.StepWithEdges for testing
type mockStep struct {
	name         string
	activity     string
	params       map[string]any
	edges        []*domain.StepEdge
	joinConfig   *domain.JoinConfig
	storeVar     string
	edgeStrategy domain.EdgeMatchingStrategy
}

func (s *mockStep) StepName() string                                { return s.name }
func (s *mockStep) ActivityName() string                            { return s.activity }
func (s *mockStep) StepParameters() map[string]any                  { return s.params }
func (s *mockStep) NextEdges() []*domain.StepEdge                   { return s.edges }
func (s *mockStep) JoinConfig() *domain.JoinConfig                  { return s.joinConfig }
func (s *mockStep) StoreVariable() string                           { return s.storeVar }
func (s *mockStep) GetRetryConfigs() []*domain.RetryConfig          { return nil }
func (s *mockStep) GetCatchConfigs() []*domain.CatchConfig          { return nil }
func (s *mockStep) GetEdgeMatchingStrategy() domain.EdgeMatchingStrategy {
	if s.edgeStrategy == "" {
		return domain.EdgeMatchingAll
	}
	return s.edgeStrategy
}

func TestEvaluateNextSteps_NoEdges(t *testing.T) {
	step := &mockStep{
		name:  "end-step",
		edges: nil,
	}

	state := NewEngineExecutionState()
	state.CreatePath("main", "end-step", nil)
	ctx := &ResolutionContext{}

	nextSteps, err := EvaluateNextSteps(step, state, "main", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(nextSteps) != 0 {
		t.Errorf("expected 0 next steps, got %d", len(nextSteps))
	}
}

func TestEvaluateNextSteps_UnconditionalEdge(t *testing.T) {
	step := &mockStep{
		name: "step1",
		edges: []*domain.StepEdge{
			{Step: "step2"},
		},
	}

	state := NewEngineExecutionState()
	state.CreatePath("main", "step1", nil)
	ctx := &ResolutionContext{}

	nextSteps, err := EvaluateNextSteps(step, state, "main", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(nextSteps) != 1 {
		t.Fatalf("expected 1 next step, got %d", len(nextSteps))
	}

	if nextSteps[0].StepName != "step2" {
		t.Errorf("expected step 'step2', got '%s'", nextSteps[0].StepName)
	}

	if nextSteps[0].PathID != "main" {
		t.Errorf("expected path 'main', got '%s'", nextSteps[0].PathID)
	}

	if nextSteps[0].IsNewPath {
		t.Error("expected IsNewPath to be false")
	}
}

func TestEvaluateNextSteps_NamedPath(t *testing.T) {
	step := &mockStep{
		name: "branch",
		edges: []*domain.StepEdge{
			{Step: "pathA-step", Path: "path-a"},
		},
	}

	state := NewEngineExecutionState()
	state.CreatePath("main", "branch", nil)
	ctx := &ResolutionContext{}

	nextSteps, err := EvaluateNextSteps(step, state, "main", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(nextSteps) != 1 {
		t.Fatalf("expected 1 next step, got %d", len(nextSteps))
	}

	if nextSteps[0].PathID != "path-a" {
		t.Errorf("expected path 'path-a', got '%s'", nextSteps[0].PathID)
	}

	if !nextSteps[0].IsNewPath {
		t.Error("expected IsNewPath to be true")
	}
}

func TestEvaluateNextSteps_MultipleEdges(t *testing.T) {
	step := &mockStep{
		name: "fork",
		edges: []*domain.StepEdge{
			{Step: "pathA-step", Path: "path-a"},
			{Step: "pathB-step", Path: "path-b"},
		},
	}

	state := NewEngineExecutionState()
	state.CreatePath("main", "fork", nil)
	ctx := &ResolutionContext{}

	nextSteps, err := EvaluateNextSteps(step, state, "main", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(nextSteps) != 2 {
		t.Fatalf("expected 2 next steps, got %d", len(nextSteps))
	}

	// Both should be new paths
	for _, ns := range nextSteps {
		if !ns.IsNewPath {
			t.Errorf("expected IsNewPath for '%s'", ns.PathID)
		}
	}
}

func TestEvaluateNextSteps_ConditionalEdge(t *testing.T) {
	step := &mockStep{
		name: "decision",
		edges: []*domain.StepEdge{
			{Step: "yes-step", Condition: "state.answer == 'yes'"},
			{Step: "no-step", Condition: "state.answer == 'no'"},
		},
	}

	state := NewEngineExecutionState()
	state.CreatePath("main", "decision", map[string]any{"answer": "yes"})
	ctx := BuildResolutionContext(nil, state, "main")

	nextSteps, err := EvaluateNextSteps(step, state, "main", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(nextSteps) != 1 {
		t.Fatalf("expected 1 next step, got %d", len(nextSteps))
	}

	if nextSteps[0].StepName != "yes-step" {
		t.Errorf("expected 'yes-step', got '%s'", nextSteps[0].StepName)
	}
}

func TestEvaluateNextSteps_EdgeMatchingFirst(t *testing.T) {
	step := &mockStep{
		name:         "decision",
		edgeStrategy: domain.EdgeMatchingFirst,
		edges: []*domain.StepEdge{
			{Step: "first-step"},  // unconditional
			{Step: "second-step"}, // also unconditional
		},
	}

	state := NewEngineExecutionState()
	state.CreatePath("main", "decision", nil)
	ctx := &ResolutionContext{}

	nextSteps, err := EvaluateNextSteps(step, state, "main", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(nextSteps) != 1 {
		t.Fatalf("expected 1 next step with EdgeMatchingFirst, got %d", len(nextSteps))
	}

	if nextSteps[0].StepName != "first-step" {
		t.Errorf("expected 'first-step', got '%s'", nextSteps[0].StepName)
	}
}

func TestEvaluateCondition_Equality(t *testing.T) {
	ctx := &ResolutionContext{
		Variables: map[string]any{"status": "active"},
	}

	result, err := evaluateCondition("state.status == 'active'", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result {
		t.Error("expected true for state.status == 'active'")
	}

	result, err = evaluateCondition("state.status == 'inactive'", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result {
		t.Error("expected false for state.status == 'inactive'")
	}
}

func TestEvaluateCondition_Inequality(t *testing.T) {
	ctx := &ResolutionContext{
		Variables: map[string]any{"count": 5},
	}

	result, err := evaluateCondition("state.count != 0", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result {
		t.Error("expected true for state.count != 0")
	}
}

func TestEvaluateCondition_NumericComparison(t *testing.T) {
	ctx := &ResolutionContext{
		Variables: map[string]any{"value": 10},
	}

	tests := []struct {
		condition string
		expected  bool
	}{
		{"state.value > 5", true},
		{"state.value > 10", false},
		{"state.value >= 10", true},
		{"state.value < 15", true},
		{"state.value <= 10", true},
		{"state.value < 10", false},
	}

	for _, tc := range tests {
		result, err := evaluateCondition(tc.condition, ctx)
		if err != nil {
			t.Fatalf("unexpected error for '%s': %v", tc.condition, err)
		}
		if result != tc.expected {
			t.Errorf("expected %v for '%s', got %v", tc.expected, tc.condition, result)
		}
	}
}

func TestEvaluateCondition_BooleanLiteral(t *testing.T) {
	ctx := &ResolutionContext{}

	result, err := evaluateCondition("true", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result {
		t.Error("expected true for 'true'")
	}

	result, err = evaluateCondition("false", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result {
		t.Error("expected false for 'false'")
	}
}

func TestEvaluateCondition_EmptyCondition(t *testing.T) {
	ctx := &ResolutionContext{}

	result, err := evaluateCondition("", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result {
		t.Error("expected true for empty condition")
	}
}

func TestEvaluateCondition_StepOutput(t *testing.T) {
	ctx := &ResolutionContext{
		StepOutputs: map[string]any{
			"check": map[string]any{"valid": true},
		},
	}

	result, err := evaluateCondition("steps.check.valid == true", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result {
		t.Error("expected true")
	}
}

func TestEvaluateCondition_StepOutputString(t *testing.T) {
	ctx := &ResolutionContext{
		StepOutputs: map[string]any{
			"start": map[string]any{"route": "B"},
		},
	}

	// Test condition that should NOT match
	result, err := evaluateCondition("steps.start.route == 'A'", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result {
		t.Error("expected false for route == 'A' when route is 'B'")
	}

	// Test condition that should match
	result, err = evaluateCondition("steps.start.route == 'B'", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result {
		t.Error("expected true for route == 'B' when route is 'B'")
	}
}

func TestEvaluateNextSteps_ConditionalEdges(t *testing.T) {
	step := &mockStep{
		name:         "start",
		edgeStrategy: domain.EdgeMatchingFirst,
		edges: []*domain.StepEdge{
			{Step: "pathA", Condition: "steps.start.route == 'A'"},
			{Step: "pathB", Condition: "steps.start.route == 'B'"},
		},
	}

	state := NewEngineExecutionState()
	state.CreatePath("main", "start", nil)
	// Store step output BEFORE evaluating next steps
	state.StoreStepOutput("main", "start", map[string]any{"route": "B"})

	ctx := BuildResolutionContext(nil, state, "main")

	nextSteps, err := EvaluateNextSteps(step, state, "main", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(nextSteps) != 1 {
		t.Fatalf("expected 1 next step, got %d", len(nextSteps))
	}

	if nextSteps[0].StepName != "pathB" {
		t.Errorf("expected 'pathB', got '%s'", nextSteps[0].StepName)
	}
}

func TestCopyMap(t *testing.T) {
	original := map[string]any{"a": 1, "b": "two"}
	copied := copyMap(original)

	if copied["a"] != 1 {
		t.Error("expected a=1")
	}

	// Modify original
	original["a"] = 99

	// Copy should be unaffected
	if copied["a"] != 1 {
		t.Error("expected copy to be independent")
	}
}

func TestCopyMap_Nil(t *testing.T) {
	copied := copyMap(nil)
	if copied == nil {
		t.Error("expected empty map, not nil")
	}
	if len(copied) != 0 {
		t.Error("expected empty map")
	}
}
