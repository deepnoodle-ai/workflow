package script

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

type Template struct {
	raw    string
	parts  []string
	codes  []Script
	engine Compiler
}

func NewTemplate(engine Compiler, raw string) (*Template, error) {
	e := &Template{
		raw:    raw,
		engine: engine,
	}

	// First validate that all ${...} expressions are properly closed
	openCount := strings.Count(raw, "${")
	closeCount := strings.Count(raw, "}")
	if openCount > closeCount {
		return nil, fmt.Errorf("unclosed template expression in string: %q", raw)
	}

	if openCount == 0 {
		// No template variables, just return the raw string
		return e, nil
	}

	// Compile all ${...} expressions into Risor code
	re := regexp.MustCompile(`\${([^}]+)}`)
	matches := re.FindAllStringSubmatchIndex(raw, -1)

	if len(matches) == 0 {
		// No template variables, just return the raw string
		return e, nil
	}

	var lastEnd int
	var parts []string
	var codes []Script
	for _, match := range matches {
		// Add the text before the match
		if match[0] > lastEnd {
			parts = append(parts, raw[lastEnd:match[0]])
		}

		// Extract and compile the code inside ${...}
		expr := raw[match[2]:match[3]]

		script, err := engine.Compile(context.Background(), expr)
		if err != nil {
			return nil, fmt.Errorf("failed to compile template expression %q: %w", expr, err)
		}

		codes = append(codes, script)
		parts = append(parts, "") // Placeholder for the evaluated result
		lastEnd = match[1]
	}

	// Add any remaining text after the last match
	if lastEnd < len(raw) {
		parts = append(parts, raw[lastEnd:])
	}

	return &Template{
		raw:   raw,
		parts: parts,
		codes: codes,
	}, nil
}

func (e *Template) Eval(ctx context.Context, globals map[string]any) (string, error) {
	if len(e.codes) == 0 {
		// No template variables, return the raw string
		return e.raw, nil
	}

	// Make a copy of parts since we'll modify it
	parts := make([]string, len(e.parts))
	copy(parts, e.parts)

	// Evaluate each code block and replace the corresponding placeholder
	for _, code := range e.codes {
		result, err := code.Evaluate(ctx, globals)
		if err != nil {
			return "", fmt.Errorf("failed to evaluate template expression: %w", err)
		}
		// Find the next empty placeholder and replace it
		for j := range parts {
			if parts[j] == "" {
				parts[j] = result.String()
				break
			}
		}
	}

	// Join all parts to create the final string
	return strings.Join(parts, ""), nil
}
