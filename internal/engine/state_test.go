package engine

import (
	"encoding/json"
	"testing"

	"github.com/deepnoodle-ai/workflow/domain"
)

func TestEngineExecutionState_NewAndLoad(t *testing.T) {
	// Create a new state
	state := NewEngineExecutionState()
	if state == nil {
		t.Fatal("NewEngineExecutionState returned nil")
	}

	if len(state.PathStates) != 0 {
		t.Errorf("expected empty PathStates, got %d", len(state.PathStates))
	}

	// Test LoadState with empty record
	record := &domain.ExecutionRecord{
		ID:     "test-exec",
		Inputs: map[string]any{"key": "value"},
	}

	loaded, err := LoadState(record)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}

	// Should create default "main" path
	if _, ok := loaded.PathStates["main"]; !ok {
		t.Error("expected 'main' path to be created")
	}
}

func TestEngineExecutionState_SaveAndLoad(t *testing.T) {
	state := NewEngineExecutionState()
	state.CreatePath("main", "step1", map[string]any{"var1": "val1"})
	state.StoreStepOutput("main", "step1", map[string]any{"result": "success"})
	state.StoreVariable("main", "processed", true)

	// Save to record
	record := &domain.ExecutionRecord{ID: "test-exec"}
	if err := state.Save(record); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	if len(record.StateData) == 0 {
		t.Error("expected StateData to be set")
	}

	// Load from record
	loaded, err := LoadState(record)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}

	// Verify path state
	pathState := loaded.GetPathState("main")
	if pathState == nil {
		t.Fatal("expected 'main' path state")
	}

	if pathState.CurrentStep != "step1" {
		t.Errorf("expected CurrentStep 'step1', got '%s'", pathState.CurrentStep)
	}

	if pathState.Variables["var1"] != "val1" {
		t.Errorf("expected var1='val1', got '%v'", pathState.Variables["var1"])
	}

	if pathState.Variables["processed"] != true {
		t.Errorf("expected processed=true, got '%v'", pathState.Variables["processed"])
	}

	stepOutput, ok := pathState.StepOutputs["step1"].(map[string]any)
	if !ok {
		t.Fatal("expected step output to be a map")
	}
	if stepOutput["result"] != "success" {
		t.Errorf("expected result='success', got '%v'", stepOutput["result"])
	}
}

func TestEngineExecutionState_PathOperations(t *testing.T) {
	state := NewEngineExecutionState()

	// Create path
	state.CreatePath("path-a", "stepA", map[string]any{"shared": "value"})
	state.CreatePath("path-b", "stepB", nil)

	// Verify paths exist
	pathA := state.GetPathState("path-a")
	pathB := state.GetPathState("path-b")

	if pathA == nil || pathB == nil {
		t.Fatal("expected both paths to exist")
	}

	if pathA.CurrentStep != "stepA" {
		t.Errorf("expected path-a step 'stepA', got '%s'", pathA.CurrentStep)
	}

	if pathA.Variables["shared"] != "value" {
		t.Errorf("expected inherited variable")
	}

	// Mark complete
	state.MarkPathComplete("path-a")
	pathA = state.GetPathState("path-a")
	if pathA.Status != domain.ExecutionStatusCompleted {
		t.Errorf("expected path-a to be completed, got %s", pathA.Status)
	}

	// Mark failed
	state.MarkPathFailed("path-b", "test error")
	pathB = state.GetPathState("path-b")
	if pathB.Status != domain.ExecutionStatusFailed {
		t.Errorf("expected path-b to be failed, got %s", pathB.Status)
	}
	if pathB.ErrorMessage != "test error" {
		t.Errorf("expected error message 'test error', got '%s'", pathB.ErrorMessage)
	}
}

func TestEngineExecutionState_AllPathsComplete(t *testing.T) {
	state := NewEngineExecutionState()
	state.CreatePath("main", "step1", nil)

	// Not complete yet
	if state.AllPathsComplete() {
		t.Error("expected AllPathsComplete to be false while running")
	}

	// Mark complete
	state.MarkPathComplete("main")
	if !state.AllPathsComplete() {
		t.Error("expected AllPathsComplete to be true after completing")
	}

	// Add another path
	state.CreatePath("path-2", "step2", nil)
	if state.AllPathsComplete() {
		t.Error("expected AllPathsComplete to be false with new running path")
	}

	state.MarkPathFailed("path-2", "error")
	if !state.AllPathsComplete() {
		t.Error("expected AllPathsComplete to be true (failed counts as complete)")
	}
}

func TestEngineExecutionState_HasFailedPaths(t *testing.T) {
	state := NewEngineExecutionState()
	state.CreatePath("main", "step1", nil)

	if state.HasFailedPaths() {
		t.Error("expected no failed paths initially")
	}

	state.MarkPathComplete("main")
	if state.HasFailedPaths() {
		t.Error("expected no failed paths after completion")
	}

	state.CreatePath("path-2", "step2", nil)
	state.MarkPathFailed("path-2", "error")
	if !state.HasFailedPaths() {
		t.Error("expected HasFailedPaths to be true")
	}
}

func TestEngineExecutionState_JoinReady(t *testing.T) {
	state := NewEngineExecutionState()

	// Create paths
	state.CreatePath("path-a", "step1", nil)
	state.CreatePath("path-b", "step2", nil)
	state.CreatePath("path-c", "step3", nil)

	// Complete path-a and path-b
	state.MarkPathComplete("path-a")
	state.MarkPathComplete("path-b")

	// path-c arrives at join
	joinConfig := &domain.JoinConfig{
		Paths: []string{"path-a", "path-b", "path-c"},
	}
	state.AddPathToJoin("join-step", "path-c", joinConfig)

	// Check if join is ready
	if !state.IsJoinReady("join-step") {
		t.Error("expected join to be ready (path-a and path-b complete)")
	}
}

func TestEngineExecutionState_JoinNotReady(t *testing.T) {
	state := NewEngineExecutionState()

	// Create paths
	state.CreatePath("path-a", "step1", nil)
	state.CreatePath("path-b", "step2", nil)

	// Only complete path-a
	state.MarkPathComplete("path-a")
	// path-b is still running

	// path-b arrives at join (but path-a is required)
	joinConfig := &domain.JoinConfig{
		Paths: []string{"path-a", "path-b"},
	}
	state.AddPathToJoin("join-step", "path-b", joinConfig)

	// Join should be ready since path-a (the only other required path) is complete
	if !state.IsJoinReady("join-step") {
		t.Error("expected join to be ready")
	}
}

func TestEngineExecutionState_GetCompletedOutputs(t *testing.T) {
	state := NewEngineExecutionState()
	state.CreatePath("main", "step1", nil)
	state.StoreStepOutput("main", "step1", map[string]any{"result": "value1"})
	state.StoreStepOutput("main", "step2", map[string]any{"data": "value2"})
	state.MarkPathComplete("main")

	outputs := state.GetCompletedOutputs()

	// Should have the last step's output merged
	if outputs["data"] != "value2" {
		t.Errorf("expected data='value2', got '%v'", outputs["data"])
	}

	// Should have prefixed outputs
	if outputs["main.step1"] == nil {
		t.Error("expected main.step1 output")
	}
	if outputs["main.step2"] == nil {
		t.Error("expected main.step2 output")
	}
}

func TestEngineExecutionState_GeneratePathID(t *testing.T) {
	state := NewEngineExecutionState()

	// Named path
	id1 := state.GeneratePathID("custom-name")
	if id1 != "custom-name" {
		t.Errorf("expected 'custom-name', got '%s'", id1)
	}

	// Generated paths
	id2 := state.GeneratePathID("")
	id3 := state.GeneratePathID("")

	if id2 == id3 {
		t.Error("expected unique generated path IDs")
	}
}

func TestEngineExecutionState_JSONSerialization(t *testing.T) {
	state := NewEngineExecutionState()
	state.CreatePath("main", "step1", map[string]any{"key": "value"})
	state.StoreStepOutput("main", "step1", map[string]any{"output": 123})
	state.PathCounter = 5

	// Marshal
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	// Unmarshal
	var loaded EngineExecutionState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if loaded.PathCounter != 5 {
		t.Errorf("expected PathCounter=5, got %d", loaded.PathCounter)
	}

	pathState := loaded.PathStates["main"]
	if pathState == nil {
		t.Fatal("expected main path state")
	}

	if pathState.Variables["key"] != "value" {
		t.Errorf("expected key='value', got '%v'", pathState.Variables["key"])
	}
}
