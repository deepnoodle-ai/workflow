package ai

import (
	"context"
	"sync/atomic"
	"testing"
)

// mockTool is a test tool that counts executions.
type mockTool struct {
	name        string
	execCount   atomic.Int32
	returnValue string
	returnErr   error
}

func (m *mockTool) Name() string        { return m.name }
func (m *mockTool) Description() string { return "A mock tool for testing" }
func (m *mockTool) Schema() *ToolSchema {
	return NewObjectSchema().
		AddProperty("input", StringProperty("Test input"))
}

func (m *mockTool) Execute(ctx context.Context, args map[string]any) (*ToolResult, error) {
	m.execCount.Add(1)
	if m.returnErr != nil {
		return nil, m.returnErr
	}
	return &ToolResult{
		Output:  m.returnValue,
		Success: true,
	}, nil
}

func TestDurableTool_CachesResults(t *testing.T) {
	mock := &mockTool{name: "test", returnValue: "result"}
	dt := NewDurableTool(mock)

	ctx := context.Background()
	args := map[string]any{"input": "test"}

	// First execution
	result1, err := dt.Execute(ctx, "call_1", args)
	if err != nil {
		t.Fatalf("first execution failed: %v", err)
	}
	if result1.Output != "result" {
		t.Errorf("expected 'result', got %s", result1.Output)
	}
	if mock.execCount.Load() != 1 {
		t.Errorf("expected 1 execution, got %d", mock.execCount.Load())
	}

	// Second execution with same call ID - should return cached result
	result2, err := dt.Execute(ctx, "call_1", args)
	if err != nil {
		t.Fatalf("second execution failed: %v", err)
	}
	if result2.Output != "result" {
		t.Errorf("expected 'result', got %s", result2.Output)
	}
	if mock.execCount.Load() != 1 {
		t.Errorf("expected still 1 execution (cached), got %d", mock.execCount.Load())
	}

	// Third execution with different call ID - should execute again
	result3, err := dt.Execute(ctx, "call_2", args)
	if err != nil {
		t.Fatalf("third execution failed: %v", err)
	}
	if result3.Output != "result" {
		t.Errorf("expected 'result', got %s", result3.Output)
	}
	if mock.execCount.Load() != 2 {
		t.Errorf("expected 2 executions, got %d", mock.execCount.Load())
	}
}

func TestDurableTool_ExportRestoreCache(t *testing.T) {
	mock := &mockTool{name: "test", returnValue: "cached_value"}
	dt := NewDurableTool(mock)

	ctx := context.Background()
	args := map[string]any{"input": "test"}

	// Execute to populate cache
	_, err := dt.Execute(ctx, "call_abc", args)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	// Export cache
	exported := dt.ExportCache()
	if len(exported) != 1 {
		t.Errorf("expected 1 cached item, got %d", len(exported))
	}

	// Create new DurableTool and restore cache
	mock2 := &mockTool{name: "test", returnValue: "new_value"}
	dt2 := NewDurableTool(mock2)

	if err := dt2.RestoreCache(exported); err != nil {
		t.Fatalf("restore cache failed: %v", err)
	}

	// Execute with same call ID - should return cached result, not execute
	result, err := dt2.Execute(ctx, "call_abc", args)
	if err != nil {
		t.Fatalf("execution after restore failed: %v", err)
	}
	if result.Output != "cached_value" {
		t.Errorf("expected cached 'cached_value', got %s", result.Output)
	}
	if mock2.execCount.Load() != 0 {
		t.Errorf("expected 0 executions (from cache), got %d", mock2.execCount.Load())
	}
}

func TestDurableTool_ClearCache(t *testing.T) {
	mock := &mockTool{name: "test", returnValue: "result"}
	dt := NewDurableTool(mock)

	ctx := context.Background()
	args := map[string]any{"input": "test"}

	// Execute to populate cache
	_, _ = dt.Execute(ctx, "call_1", args)
	if mock.execCount.Load() != 1 {
		t.Errorf("expected 1 execution, got %d", mock.execCount.Load())
	}

	// Clear cache
	dt.ClearCache()

	// Execute again - should not use cache
	_, _ = dt.Execute(ctx, "call_1", args)
	if mock.execCount.Load() != 2 {
		t.Errorf("expected 2 executions after clear, got %d", mock.execCount.Load())
	}
}

func TestDurableTool_ExecuteWithContext(t *testing.T) {
	mock := &mockTool{name: "test", returnValue: "result"}
	dt := NewDurableTool(mock)

	ctx := context.Background()

	// Without _call_id - should execute without caching
	args1 := map[string]any{"input": "test"}
	_, _ = dt.ExecuteWithContext(ctx, args1)
	_, _ = dt.ExecuteWithContext(ctx, args1)
	if mock.execCount.Load() != 2 {
		t.Errorf("expected 2 executions without call_id, got %d", mock.execCount.Load())
	}

	// With _call_id - should cache
	args2 := map[string]any{"input": "test", "_call_id": "call_xyz"}
	_, _ = dt.ExecuteWithContext(ctx, args2)
	_, _ = dt.ExecuteWithContext(ctx, args2)
	if mock.execCount.Load() != 3 {
		t.Errorf("expected 3 executions (1 cached), got %d", mock.execCount.Load())
	}
}

func TestToolFunc(t *testing.T) {
	executed := false
	tf := NewToolFunc("my_tool", "A test tool", NewObjectSchema(), func(ctx context.Context, args map[string]any) (*ToolResult, error) {
		executed = true
		return &ToolResult{Output: "done", Success: true}, nil
	})

	if tf.Name() != "my_tool" {
		t.Errorf("expected name 'my_tool', got %s", tf.Name())
	}
	if tf.Description() != "A test tool" {
		t.Errorf("expected description 'A test tool', got %s", tf.Description())
	}

	result, err := tf.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}
	if !executed {
		t.Error("tool function was not executed")
	}
	if result.Output != "done" {
		t.Errorf("expected output 'done', got %s", result.Output)
	}
}

func TestToolSchema_Builder(t *testing.T) {
	schema := NewObjectSchema().
		AddProperty("name", StringProperty("User's name")).
		AddProperty("age", IntegerProperty("User's age")).
		AddProperty("score", NumberProperty("User's score")).
		AddProperty("active", BooleanProperty("Is active")).
		AddProperty("tags", ArrayProperty("User tags", StringProperty("tag"))).
		AddProperty("status", EnumProperty("User status", "active", "inactive", "pending")).
		AddRequired("name", "age")

	if schema.Type != "object" {
		t.Errorf("expected type 'object', got %s", schema.Type)
	}
	if len(schema.Properties) != 6 {
		t.Errorf("expected 6 properties, got %d", len(schema.Properties))
	}
	if len(schema.Required) != 2 {
		t.Errorf("expected 2 required, got %d", len(schema.Required))
	}

	// Check property types
	if schema.Properties["name"].Type != "string" {
		t.Error("name should be string")
	}
	if schema.Properties["age"].Type != "integer" {
		t.Error("age should be integer")
	}
	if schema.Properties["score"].Type != "number" {
		t.Error("score should be number")
	}
	if schema.Properties["active"].Type != "boolean" {
		t.Error("active should be boolean")
	}
	if schema.Properties["tags"].Type != "array" {
		t.Error("tags should be array")
	}
	if len(schema.Properties["status"].Enum) != 3 {
		t.Error("status should have 3 enum values")
	}
}
