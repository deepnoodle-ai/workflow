package engine

import (
	"strings"

	"github.com/deepnoodle-ai/workflow/domain"
)

// NextStepInfo represents information about a next step to execute.
type NextStepInfo struct {
	StepName  string            // name of the next step
	PathID    string            // path ID (same as current or new)
	Variables map[string]any    // variables for the new path
	IsNewPath bool              // whether this creates a new path
}

// EvaluateNextSteps evaluates the edges from a completed step and determines next steps.
// Returns the list of next steps to execute, or an empty list if execution should end.
func EvaluateNextSteps(
	step domain.StepDefinition,
	state *EngineExecutionState,
	pathID string,
	ctx *ResolutionContext,
) ([]NextStepInfo, error) {
	// Get edges from step
	stepWithEdges, ok := step.(domain.StepWithEdges)
	if !ok {
		// Step doesn't have edges - no next steps
		return nil, nil
	}

	edges := stepWithEdges.NextEdges()
	if len(edges) == 0 {
		// No outgoing edges - path is complete
		return nil, nil
	}

	// Get edge matching strategy
	strategy := stepWithEdges.GetEdgeMatchingStrategy()

	// Evaluate conditions and collect matching edges
	var matchingEdges []*domain.StepEdge
	for _, edge := range edges {
		if edge.Condition == "" {
			// Unconditional edge always matches
			matchingEdges = append(matchingEdges, edge)
		} else {
			// Evaluate condition
			match, err := evaluateCondition(edge.Condition, ctx)
			if err != nil {
				// Log error but continue
				continue
			}
			if match {
				matchingEdges = append(matchingEdges, edge)
			}
		}

		// If using "first" strategy and we found a match, stop here
		if strategy == domain.EdgeMatchingFirst && len(matchingEdges) > 0 {
			break
		}
	}

	if len(matchingEdges) == 0 {
		// No matching edges - path is complete
		return nil, nil
	}

	// Get current path variables for inheritance
	currentPath := state.GetPathState(pathID)
	currentVars := make(map[string]any)
	if currentPath != nil {
		for k, v := range currentPath.Variables {
			currentVars[k] = v
		}
	}

	// Build next step infos
	var nextSteps []NextStepInfo
	for _, edge := range matchingEdges {
		info := NextStepInfo{
			StepName:  edge.Step,
			Variables: copyMap(currentVars),
		}

		if edge.Path != "" {
			// Named path - create new path
			info.PathID = edge.Path
			info.IsNewPath = true
		} else if len(matchingEdges) > 1 {
			// Multiple edges without path names - generate path IDs
			info.PathID = state.GeneratePathID("")
			info.IsNewPath = true
		} else {
			// Single edge, same path
			info.PathID = pathID
			info.IsNewPath = false
		}

		nextSteps = append(nextSteps, info)
	}

	return nextSteps, nil
}

// evaluateCondition evaluates a condition expression.
// Currently supports simple comparisons like "state.x == 'value'" or "inputs.y > 10".
// Also supports compound conditions with && (AND) and || (OR).
func evaluateCondition(condition string, ctx *ResolutionContext) (bool, error) {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true, nil
	}

	// Handle && (AND) - split and evaluate both parts
	if strings.Contains(condition, "&&") {
		parts := strings.SplitN(condition, "&&", 2)
		if len(parts) == 2 {
			left, err := evaluateCondition(strings.TrimSpace(parts[0]), ctx)
			if err != nil {
				return false, err
			}
			if !left {
				return false, nil // Short-circuit: if left is false, result is false
			}
			return evaluateCondition(strings.TrimSpace(parts[1]), ctx)
		}
	}

	// Handle || (OR) - split and evaluate both parts
	if strings.Contains(condition, "||") {
		parts := strings.SplitN(condition, "||", 2)
		if len(parts) == 2 {
			left, err := evaluateCondition(strings.TrimSpace(parts[0]), ctx)
			if err != nil {
				return false, err
			}
			if left {
				return true, nil // Short-circuit: if left is true, result is true
			}
			return evaluateCondition(strings.TrimSpace(parts[1]), ctx)
		}
	}

	// Handle == comparison
	if strings.Contains(condition, "==") {
		parts := strings.SplitN(condition, "==", 2)
		if len(parts) == 2 {
			left := resolveConditionValue(strings.TrimSpace(parts[0]), ctx)
			right := resolveConditionValue(strings.TrimSpace(parts[1]), ctx)
			return compareValues(left, right, "=="), nil
		}
	}

	// Handle != comparison
	if strings.Contains(condition, "!=") {
		parts := strings.SplitN(condition, "!=", 2)
		if len(parts) == 2 {
			left := resolveConditionValue(strings.TrimSpace(parts[0]), ctx)
			right := resolveConditionValue(strings.TrimSpace(parts[1]), ctx)
			return !compareValues(left, right, "=="), nil
		}
	}

	// Handle > comparison
	if strings.Contains(condition, ">=") {
		parts := strings.SplitN(condition, ">=", 2)
		if len(parts) == 2 {
			left := resolveConditionValue(strings.TrimSpace(parts[0]), ctx)
			right := resolveConditionValue(strings.TrimSpace(parts[1]), ctx)
			return compareValues(left, right, ">="), nil
		}
	}
	if strings.Contains(condition, ">") {
		parts := strings.SplitN(condition, ">", 2)
		if len(parts) == 2 {
			left := resolveConditionValue(strings.TrimSpace(parts[0]), ctx)
			right := resolveConditionValue(strings.TrimSpace(parts[1]), ctx)
			return compareValues(left, right, ">"), nil
		}
	}

	// Handle < comparison
	if strings.Contains(condition, "<=") {
		parts := strings.SplitN(condition, "<=", 2)
		if len(parts) == 2 {
			left := resolveConditionValue(strings.TrimSpace(parts[0]), ctx)
			right := resolveConditionValue(strings.TrimSpace(parts[1]), ctx)
			return compareValues(left, right, "<="), nil
		}
	}
	if strings.Contains(condition, "<") {
		parts := strings.SplitN(condition, "<", 2)
		if len(parts) == 2 {
			left := resolveConditionValue(strings.TrimSpace(parts[0]), ctx)
			right := resolveConditionValue(strings.TrimSpace(parts[1]), ctx)
			return compareValues(left, right, "<"), nil
		}
	}

	// Handle boolean expressions
	if strings.ToLower(condition) == "true" {
		return true, nil
	}
	if strings.ToLower(condition) == "false" {
		return false, nil
	}

	// Try to resolve as a boolean variable
	value := resolveConditionValue(condition, ctx)
	if b, ok := value.(bool); ok {
		return b, nil
	}

	// Non-nil/non-empty values are truthy
	return value != nil && value != "" && value != 0, nil
}

// resolveConditionValue resolves a value reference in a condition.
func resolveConditionValue(expr string, ctx *ResolutionContext) any {
	expr = strings.TrimSpace(expr)

	// String literal
	if (strings.HasPrefix(expr, "'") && strings.HasSuffix(expr, "'")) ||
		(strings.HasPrefix(expr, "\"") && strings.HasSuffix(expr, "\"")) {
		return expr[1 : len(expr)-1]
	}

	// Numeric literal
	if isNumeric(expr) {
		return parseNumber(expr)
	}

	// Boolean literal
	if strings.ToLower(expr) == "true" {
		return true
	}
	if strings.ToLower(expr) == "false" {
		return false
	}

	// Null/nil literal
	if strings.ToLower(expr) == "null" || strings.ToLower(expr) == "nil" {
		return nil
	}

	// Expression reference (inputs.x, state.y, steps.z.result)
	value, found := resolveExpression(expr, ctx)
	if found {
		return value
	}

	return expr
}

// isNumeric checks if a string is a numeric literal.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	hasDecimal := false
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if c == '.' && !hasDecimal {
			hasDecimal = true
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseNumber parses a numeric string.
func parseNumber(s string) any {
	if strings.Contains(s, ".") {
		var f float64
		for i, c := range s {
			if c == '-' && i == 0 {
				continue
			}
			if c == '.' {
				continue
			}
			f = f*10 + float64(c-'0')
		}
		return f
	}
	var n int64
	negative := false
	for i, c := range s {
		if c == '-' && i == 0 {
			negative = true
			continue
		}
		n = n*10 + int64(c-'0')
	}
	if negative {
		n = -n
	}
	return n
}

// compareValues compares two values with the given operator.
func compareValues(left, right any, op string) bool {
	// Handle nil comparisons
	if left == nil && right == nil {
		return op == "==" || op == ">=" || op == "<="
	}
	if left == nil || right == nil {
		return op == "!="
	}

	// Try numeric comparison
	leftNum, leftOk := toFloat64(left)
	rightNum, rightOk := toFloat64(right)
	if leftOk && rightOk {
		switch op {
		case "==":
			return leftNum == rightNum
		case ">":
			return leftNum > rightNum
		case "<":
			return leftNum < rightNum
		case ">=":
			return leftNum >= rightNum
		case "<=":
			return leftNum <= rightNum
		}
	}

	// String comparison
	leftStr := toString(left)
	rightStr := toString(right)
	switch op {
	case "==":
		return leftStr == rightStr
	case ">":
		return leftStr > rightStr
	case "<":
		return leftStr < rightStr
	case ">=":
		return leftStr >= rightStr
	case "<=":
		return leftStr <= rightStr
	}

	return false
}

// toFloat64 converts a value to float64 if possible.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// copyMap creates a shallow copy of a map.
func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return make(map[string]any)
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
