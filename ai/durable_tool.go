package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Tool is the interface for tools that can be called by an agent.
// This is distinct from workflow activities - tools are invoked within
// an agent's execution loop, while activities are workflow steps.
type Tool interface {
	// Name returns the unique name of the tool.
	Name() string

	// Description returns a human-readable description for the LLM.
	Description() string

	// Schema returns the JSON schema for the tool's input parameters.
	Schema() *ToolSchema

	// Execute runs the tool with the given arguments.
	Execute(ctx context.Context, args map[string]any) (*ToolResult, error)
}

// ToolSchema defines the JSON schema for tool input parameters.
type ToolSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]*Property   `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
	Items      *ToolSchema            `json:"items,omitempty"`
	Enum       []string               `json:"enum,omitempty"`
}

// Property defines a single property in a tool schema.
type Property struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description,omitempty"`
	Enum        []string               `json:"enum,omitempty"`
	Items       *Property              `json:"items,omitempty"`
	Properties  map[string]*Property   `json:"properties,omitempty"`
	Required    []string               `json:"required,omitempty"`
	Default     any                    `json:"default,omitempty"`
}

// NewObjectSchema creates a schema for an object type.
func NewObjectSchema() *ToolSchema {
	return &ToolSchema{
		Type:       "object",
		Properties: make(map[string]*Property),
	}
}

// AddProperty adds a property to the schema and returns the schema for chaining.
func (s *ToolSchema) AddProperty(name string, prop *Property) *ToolSchema {
	if s.Properties == nil {
		s.Properties = make(map[string]*Property)
	}
	s.Properties[name] = prop
	return s
}

// AddRequired marks a property as required and returns the schema for chaining.
func (s *ToolSchema) AddRequired(names ...string) *ToolSchema {
	s.Required = append(s.Required, names...)
	return s
}

// StringProperty creates a string property.
func StringProperty(description string) *Property {
	return &Property{Type: "string", Description: description}
}

// IntegerProperty creates an integer property.
func IntegerProperty(description string) *Property {
	return &Property{Type: "integer", Description: description}
}

// NumberProperty creates a number property.
func NumberProperty(description string) *Property {
	return &Property{Type: "number", Description: description}
}

// BooleanProperty creates a boolean property.
func BooleanProperty(description string) *Property {
	return &Property{Type: "boolean", Description: description}
}

// ArrayProperty creates an array property.
func ArrayProperty(description string, items *Property) *Property {
	return &Property{Type: "array", Description: description, Items: items}
}

// EnumProperty creates a string property with enumerated values.
func EnumProperty(description string, values ...string) *Property {
	return &Property{Type: "string", Description: description, Enum: values}
}

// ToolFunc is a convenience type for creating tools from functions.
type ToolFunc struct {
	name        string
	description string
	schema      *ToolSchema
	execute     func(ctx context.Context, args map[string]any) (*ToolResult, error)
}

// NewToolFunc creates a new function-based tool.
func NewToolFunc(name, description string, schema *ToolSchema, fn func(ctx context.Context, args map[string]any) (*ToolResult, error)) *ToolFunc {
	return &ToolFunc{
		name:        name,
		description: description,
		schema:      schema,
		execute:     fn,
	}
}

func (t *ToolFunc) Name() string                                                    { return t.name }
func (t *ToolFunc) Description() string                                             { return t.description }
func (t *ToolFunc) Schema() *ToolSchema                                             { return t.schema }
func (t *ToolFunc) Execute(ctx context.Context, args map[string]any) (*ToolResult, error) { return t.execute(ctx, args) }

// DurableTool wraps a tool with caching for idempotency across recovery.
// When a tool is executed with the same callID, the cached result is returned
// instead of re-executing the tool. This ensures deterministic behavior across
// workflow recovery.
type DurableTool struct {
	tool    Tool
	mu      sync.RWMutex
	results map[string]cachedResult
}

type cachedResult struct {
	result *ToolResult
	err    error
}

// NewDurableTool wraps a tool with durability/idempotency support.
func NewDurableTool(tool Tool) *DurableTool {
	return &DurableTool{
		tool:    tool,
		results: make(map[string]cachedResult),
	}
}

func (d *DurableTool) Name() string        { return d.tool.Name() }
func (d *DurableTool) Description() string { return d.tool.Description() }
func (d *DurableTool) Schema() *ToolSchema { return d.tool.Schema() }

// Execute runs the tool, using cached results for repeated callIDs.
// The callID should be generated using ctx.DeterministicID() to ensure
// the same ID is generated on recovery.
func (d *DurableTool) Execute(ctx context.Context, callID string, args map[string]any) (*ToolResult, error) {
	// Check cache first
	d.mu.RLock()
	if cached, ok := d.results[callID]; ok {
		d.mu.RUnlock()
		return cached.result, cached.err
	}
	d.mu.RUnlock()

	// Execute the underlying tool
	result, err := d.tool.Execute(ctx, args)

	// Cache the result
	d.mu.Lock()
	d.results[callID] = cachedResult{result: result, err: err}
	d.mu.Unlock()

	return result, err
}

// ExecuteWithContext is a convenience method that implements the Tool interface.
// It extracts the callID from the args if present.
func (d *DurableTool) ExecuteWithContext(ctx context.Context, args map[string]any) (*ToolResult, error) {
	callID, ok := args["_call_id"].(string)
	if !ok {
		// No call ID, just execute without caching
		return d.tool.Execute(ctx, args)
	}

	// Remove the internal call ID from args before passing to tool
	argsClean := make(map[string]any)
	for k, v := range args {
		if k != "_call_id" {
			argsClean[k] = v
		}
	}

	return d.Execute(ctx, callID, argsClean)
}

// RestoreCache restores cached results from a checkpoint.
// This is used during recovery to restore the tool's state.
func (d *DurableTool) RestoreCache(cache map[string]any) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for callID, v := range cache {
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshal cached result for %s: %w", callID, err)
		}

		var cached cachedResult
		if err := json.Unmarshal(data, &cached.result); err != nil {
			return fmt.Errorf("unmarshal cached result for %s: %w", callID, err)
		}
		d.results[callID] = cached
	}
	return nil
}

// ExportCache exports cached results for checkpointing.
func (d *DurableTool) ExportCache() map[string]any {
	d.mu.RLock()
	defer d.mu.RUnlock()

	cache := make(map[string]any)
	for callID, cached := range d.results {
		cache[callID] = cached.result
	}
	return cache
}

// ClearCache removes all cached results.
func (d *DurableTool) ClearCache() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.results = make(map[string]cachedResult)
}

// Verify interface compliance.
var _ Tool = (*ToolFunc)(nil)
