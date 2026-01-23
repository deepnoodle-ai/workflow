package ai

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConversationState_AddMessages(t *testing.T) {
	conv := NewConversationState()

	// Add messages
	conv.AddUserMessage("Hello")
	conv.AddAssistantMessage("Hi there!")

	if len(conv.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(conv.Messages))
	}

	if conv.Messages[0].Role != RoleUser {
		t.Errorf("expected first message role to be user, got %s", conv.Messages[0].Role)
	}

	if conv.Messages[1].Role != RoleAssistant {
		t.Errorf("expected second message role to be assistant, got %s", conv.Messages[1].Role)
	}
}

func TestConversationState_LastMessage(t *testing.T) {
	conv := NewConversationState()

	// Empty conversation
	if conv.LastMessage() != nil {
		t.Error("expected nil for empty conversation")
	}

	// Add message
	conv.AddUserMessage("Hello")
	last := conv.LastMessage()
	if last == nil || last.Content != "Hello" {
		t.Error("expected last message to be 'Hello'")
	}

	// Add another
	conv.AddAssistantMessage("Hi!")
	last = conv.LastMessage()
	if last == nil || last.Content != "Hi!" {
		t.Error("expected last message to be 'Hi!'")
	}
}

func TestConversationState_LastAssistantMessage(t *testing.T) {
	conv := NewConversationState()

	// No assistant message
	if conv.LastAssistantMessage() != nil {
		t.Error("expected nil when no assistant message")
	}

	// Add user message
	conv.AddUserMessage("Hello")
	if conv.LastAssistantMessage() != nil {
		t.Error("expected nil when no assistant message")
	}

	// Add assistant message
	conv.AddAssistantMessage("First response")
	conv.AddUserMessage("Follow up")
	conv.AddAssistantMessage("Second response")

	last := conv.LastAssistantMessage()
	if last == nil || last.Content != "Second response" {
		t.Error("expected last assistant message to be 'Second response'")
	}
}

func TestConversationState_ToolCalls(t *testing.T) {
	conv := NewConversationState()

	// No pending tool calls
	if conv.HasPendingToolCalls() {
		t.Error("expected no pending tool calls")
	}

	// Add assistant message with tool calls
	msg := Message{
		Role:    RoleAssistant,
		Content: "Let me search for that",
		ToolCalls: []ToolCall{
			{ID: "call_1", Name: "search", Arguments: map[string]any{"query": "test"}},
		},
	}
	conv.AddMessage(msg)

	if !conv.HasPendingToolCalls() {
		t.Error("expected pending tool calls")
	}

	calls := conv.GetPendingToolCalls()
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}

	if calls[0].Name != "search" {
		t.Errorf("expected tool name 'search', got %s", calls[0].Name)
	}

	// Add tool result
	conv.AddToolResult(&ToolResult{
		CallID:  "call_1",
		Output:  "Search results...",
		Success: true,
	})

	// Note: HasPendingToolCalls checks the last assistant message, not whether
	// all tool calls have results. After adding the result, we still have the
	// same assistant message with tool calls in history.
	// The caller would typically add another assistant message after processing
	// tool results, which would then return false for HasPendingToolCalls.
}

func TestConversationState_Artifacts(t *testing.T) {
	conv := NewConversationState()

	// Get non-existent artifact
	_, exists := conv.GetArtifact("key")
	if exists {
		t.Error("expected artifact to not exist")
	}

	// Set artifact
	conv.SetArtifact("key", "value")
	val, exists := conv.GetArtifact("key")
	if !exists || val != "value" {
		t.Error("expected artifact to exist with value 'value'")
	}

	// Overwrite
	conv.SetArtifact("key", 123)
	val, exists = conv.GetArtifact("key")
	if !exists || val != 123 {
		t.Error("expected artifact to be 123")
	}
}

func TestConversationState_Clone(t *testing.T) {
	conv := NewConversationState()
	conv.SystemPrompt = "You are a helpful assistant"
	conv.Model = "claude-3"
	conv.AddUserMessage("Hello")
	conv.AddAssistantMessage("Hi!")
	conv.SetArtifact("count", 42)

	// Clone
	clone := conv.Clone()

	// Verify clone
	if clone.SystemPrompt != conv.SystemPrompt {
		t.Error("system prompt not cloned")
	}
	if clone.Model != conv.Model {
		t.Error("model not cloned")
	}
	if len(clone.Messages) != len(conv.Messages) {
		t.Error("messages not cloned")
	}

	// Modify original
	conv.AddUserMessage("Another message")
	conv.SetArtifact("count", 100)

	// Clone should be independent
	if len(clone.Messages) != 2 {
		t.Error("clone messages should be independent")
	}
	val, _ := clone.GetArtifact("count")
	// After JSON round-trip, int becomes float64
	valFloat, ok := val.(float64)
	if !ok {
		t.Errorf("expected float64, got %T", val)
	}
	if valFloat != 42 {
		t.Errorf("clone artifacts should be independent, got %v", valFloat)
	}
}

func TestConversationState_JSONSerialization(t *testing.T) {
	conv := NewConversationState()
	conv.SystemPrompt = "Test prompt"
	conv.Model = "gpt-4"
	conv.TotalTokens = 150
	conv.AddUserMessage("Hello")
	conv.AddAssistantMessage("Hi there!")
	conv.SetArtifact("data", map[string]any{"nested": "value"})

	// Marshal
	data, err := json.Marshal(conv)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Unmarshal
	var restored ConversationState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Verify
	if restored.SystemPrompt != conv.SystemPrompt {
		t.Error("system prompt not preserved")
	}
	if restored.Model != conv.Model {
		t.Error("model not preserved")
	}
	if restored.TotalTokens != conv.TotalTokens {
		t.Error("total tokens not preserved")
	}
	if len(restored.Messages) != len(conv.Messages) {
		t.Error("messages not preserved")
	}
}

func TestMessage_Helpers(t *testing.T) {
	// Test NewUserMessage
	userMsg := NewUserMessage("Test content")
	if userMsg.Role != RoleUser || userMsg.Content != "Test content" {
		t.Error("NewUserMessage failed")
	}
	if userMsg.Timestamp.IsZero() {
		t.Error("expected timestamp to be set")
	}

	// Test NewAssistantMessage
	assistantMsg := NewAssistantMessage("Response")
	if assistantMsg.Role != RoleAssistant || assistantMsg.Content != "Response" {
		t.Error("NewAssistantMessage failed")
	}

	// Test NewSystemMessage
	systemMsg := NewSystemMessage("System instruction")
	if systemMsg.Role != RoleSystem || systemMsg.Content != "System instruction" {
		t.Error("NewSystemMessage failed")
	}

	// Test NewToolResultMessage
	result := &ToolResult{CallID: "call_1", Output: "Result", Success: true}
	toolMsg := NewToolResultMessage(result)
	if toolMsg.Role != RoleTool || toolMsg.ToolResult != result {
		t.Error("NewToolResultMessage failed")
	}
}

func TestToolCall_Fields(t *testing.T) {
	call := ToolCall{
		ID:        "call_123",
		Name:      "search",
		Arguments: map[string]any{"query": "test", "limit": 10},
	}

	// Test JSON serialization
	data, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var restored ToolCall
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if restored.ID != call.ID || restored.Name != call.Name {
		t.Error("tool call not preserved")
	}
	if restored.Arguments["query"] != "test" {
		t.Error("arguments not preserved")
	}
}

func TestToolResult_Fields(t *testing.T) {
	// Success result
	success := &ToolResult{
		CallID:  "call_1",
		Output:  "Result data",
		Success: true,
	}

	// Error result
	errResult := &ToolResult{
		CallID:  "call_2",
		Error:   "Something went wrong",
		Success: false,
	}

	// Test JSON serialization
	data, _ := json.Marshal(success)
	var restoredSuccess ToolResult
	_ = json.Unmarshal(data, &restoredSuccess)

	if restoredSuccess.CallID != success.CallID || !restoredSuccess.Success {
		t.Error("success result not preserved")
	}

	data, _ = json.Marshal(errResult)
	var restoredErr ToolResult
	_ = json.Unmarshal(data, &restoredErr)

	if restoredErr.Error != errResult.Error || restoredErr.Success {
		t.Error("error result not preserved")
	}
}

func TestMessage_Timestamp(t *testing.T) {
	before := time.Now()
	msg := NewUserMessage("Test")
	after := time.Now()

	if msg.Timestamp.Before(before) || msg.Timestamp.After(after) {
		t.Error("timestamp should be between before and after")
	}
}
