package engine

import (
	"fmt"
	"regexp"
	"strings"
)

// Parameter expression pattern: $(inputs.X), $(state.X), $(steps.Y.result), $(path.X)
var paramExprPattern = regexp.MustCompile(`\$\(([^)]+)\)`)

// ResolveParameters resolves parameter expressions in a value.
// Supported expressions:
//   - $(inputs.X) → workflow input value
//   - $(state.X) or $(vars.X) → path variable value
//   - $(steps.Y.result) → step output (from result.Data)
//   - $(path.X.steps.Y) → step output from specific path
func ResolveParameters(value any, ctx *ResolutionContext) any {
	switch v := value.(type) {
	case string:
		return resolveString(v, ctx)
	case map[string]any:
		result := make(map[string]any)
		for k, val := range v {
			result[k] = ResolveParameters(val, ctx)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, val := range v {
			result[i] = ResolveParameters(val, ctx)
		}
		return result
	default:
		return value
	}
}

// ResolutionContext provides the data needed to resolve parameter expressions.
type ResolutionContext struct {
	Inputs      map[string]any // workflow inputs
	Variables   map[string]any // current path's variables
	StepOutputs map[string]any // current path's step outputs
	AllPaths    map[string]*PathOutputs
}

// PathOutputs holds outputs from a specific path.
type PathOutputs struct {
	Variables   map[string]any
	StepOutputs map[string]any
}

// resolveString resolves parameter expressions in a string.
func resolveString(s string, ctx *ResolutionContext) any {
	// Check if the entire string is a single expression: $(expr)
	if strings.HasPrefix(s, "$(") && strings.HasSuffix(s, ")") && strings.Count(s, "$(") == 1 {
		expr := s[2 : len(s)-1]
		value, found := resolveExpression(expr, ctx)
		if found {
			return value // Return the actual value (could be any type)
		}
		return s // Return original if not found
	}

	// Otherwise, do string interpolation
	result := paramExprPattern.ReplaceAllStringFunc(s, func(match string) string {
		expr := match[2 : len(match)-1]
		value, found := resolveExpression(expr, ctx)
		if !found {
			return match // Keep original if not found
		}
		return stringifyValue(value)
	})

	return result
}

// stringifyValue converts a value to a string representation.
func stringifyValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return toString(v)
	}
}

// resolveExpression resolves a single expression like "inputs.X" or "steps.Y.result".
func resolveExpression(expr string, ctx *ResolutionContext) (any, bool) {
	parts := strings.Split(expr, ".")
	if len(parts) == 0 {
		return nil, false
	}

	switch parts[0] {
	case "inputs":
		// $(inputs.X) → ctx.Inputs["X"]
		if len(parts) < 2 {
			return ctx.Inputs, true
		}
		return getNestedValue(ctx.Inputs, parts[1:])

	case "state", "vars", "variables":
		// $(state.X) → ctx.Variables["X"]
		if len(parts) < 2 {
			return ctx.Variables, true
		}
		return getNestedValue(ctx.Variables, parts[1:])

	case "steps":
		// $(steps.Y.result) → ctx.StepOutputs["Y"]["result"]
		if len(parts) < 2 {
			return ctx.StepOutputs, true
		}
		stepName := parts[1]
		stepOutput, ok := ctx.StepOutputs[stepName]
		if !ok {
			return nil, false
		}
		if len(parts) == 2 {
			return stepOutput, true
		}
		if outputMap, ok := stepOutput.(map[string]any); ok {
			return getNestedValue(outputMap, parts[2:])
		}
		return nil, false

	case "path":
		// $(path.X.steps.Y) → access step output from specific path
		if len(parts) < 2 || ctx.AllPaths == nil {
			return nil, false
		}
		pathID := parts[1]
		pathOutputs, ok := ctx.AllPaths[pathID]
		if !ok {
			return nil, false
		}
		if len(parts) == 2 {
			return pathOutputs.StepOutputs, true
		}
		if len(parts) >= 4 && parts[2] == "steps" {
			stepName := parts[3]
			stepOutput, ok := pathOutputs.StepOutputs[stepName]
			if !ok {
				return nil, false
			}
			if len(parts) == 4 {
				return stepOutput, true
			}
			if outputMap, ok := stepOutput.(map[string]any); ok {
				return getNestedValue(outputMap, parts[4:])
			}
		}
		if len(parts) >= 3 && parts[2] == "vars" {
			if len(parts) == 3 {
				return pathOutputs.Variables, true
			}
			return getNestedValue(pathOutputs.Variables, parts[3:])
		}
		return nil, false

	default:
		return nil, false
	}
}

// getNestedValue traverses a nested map structure.
func getNestedValue(m map[string]any, path []string) (any, bool) {
	if m == nil || len(path) == 0 {
		return nil, false
	}

	current := any(m)
	for _, key := range path {
		switch v := current.(type) {
		case map[string]any:
			val, ok := v[key]
			if !ok {
				return nil, false
			}
			current = val
		default:
			return nil, false
		}
	}
	return current, true
}

// toString converts a value to a string representation.
func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", val)
	}
}

// BuildResolutionContext creates a resolution context from execution state.
func BuildResolutionContext(
	inputs map[string]any,
	state *EngineExecutionState,
	pathID string,
) *ResolutionContext {
	ctx := &ResolutionContext{
		Inputs:      inputs,
		Variables:   make(map[string]any),
		StepOutputs: make(map[string]any),
		AllPaths:    make(map[string]*PathOutputs),
	}

	// Set current path's data
	if pathState := state.GetPathState(pathID); pathState != nil {
		ctx.Variables = pathState.Variables
		ctx.StepOutputs = pathState.StepOutputs
	}

	// Set all paths' data for cross-path references
	for pid, pathState := range state.PathStates {
		ctx.AllPaths[pid] = &PathOutputs{
			Variables:   pathState.Variables,
			StepOutputs: pathState.StepOutputs,
		}
	}

	return ctx
}
