package script

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Template is a parsed ${...} template. Templates are the only
// interpolation syntax in the workflow engine. A template may be:
//
//  1. Literal — contains no ${...} tokens. Eval returns the raw string.
//  2. Single-expression — the whole trimmed raw string is exactly one
//     ${expr}. Eval returns the typed value produced by the script
//     engine (preserving number, bool, array, map, etc.).
//  3. Interpolated — the raw string mixes literal text with one or
//     more ${expr} tokens. Eval returns a concatenated string, with
//     each expression value stringified via Value.String().
//
// This is "contextual type inference": callers wishing to pass a
// typed value (number, bool, slice) through a parameter write the
// whole value as a single ${...} token; callers building a string
// (URLs, messages, topics) interpolate tokens inside surrounding text
// and get a string back.
type Template struct {
	raw        string
	parts      []string // literal segments, interleaved with placeholders ("")
	scripts    []Script // compiled scripts, one per placeholder
	singleExpr bool     // raw (trimmed) is exactly one ${...} token
}

var templateExprRE = regexp.MustCompile(`\${([^}]+)}`)

// NewTemplate parses raw as a ${...} template and compiles every
// expression it contains against engine. Returns an error if any
// expression is syntactically malformed (unclosed brace) or fails to
// compile.
func NewTemplate(engine Compiler, raw string) (*Template, error) {
	if strings.Count(raw, "${") > strings.Count(raw, "}") {
		return nil, fmt.Errorf("unclosed template expression in string: %q", raw)
	}

	matches := templateExprRE.FindAllStringSubmatchIndex(raw, -1)
	if len(matches) == 0 {
		return &Template{raw: raw}, nil
	}

	var (
		parts   []string
		scripts []Script
		lastEnd int
	)
	for _, match := range matches {
		if match[0] > lastEnd {
			parts = append(parts, raw[lastEnd:match[0]])
		}
		expr := raw[match[2]:match[3]]
		compiled, err := engine.Compile(context.Background(), expr)
		if err != nil {
			return nil, fmt.Errorf("failed to compile template expression %q: %w", expr, err)
		}
		scripts = append(scripts, compiled)
		parts = append(parts, "") // placeholder
		lastEnd = match[1]
	}
	if lastEnd < len(raw) {
		parts = append(parts, raw[lastEnd:])
	}

	trimmed := strings.TrimSpace(raw)
	singleExpr := len(matches) == 1 &&
		strings.HasPrefix(trimmed, "${") &&
		strings.HasSuffix(trimmed, "}") &&
		templateExprRE.FindString(trimmed) == trimmed

	return &Template{
		raw:        raw,
		parts:      parts,
		scripts:    scripts,
		singleExpr: singleExpr,
	}, nil
}

// Eval evaluates the template. For literal templates the raw string
// is returned as-is. For single-expression templates the script's
// typed value is returned. For interpolated templates the result is
// the concatenated string with each expression stringified.
func (e *Template) Eval(ctx context.Context, globals map[string]any) (any, error) {
	if len(e.scripts) == 0 {
		return e.raw, nil
	}

	if e.singleExpr {
		result, err := e.scripts[0].Evaluate(ctx, globals)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate template expression: %w", err)
		}
		return result.Value(), nil
	}

	parts := make([]string, len(e.parts))
	copy(parts, e.parts)

	scriptIdx := 0
	for j := range parts {
		if parts[j] != "" {
			continue
		}
		result, err := e.scripts[scriptIdx].Evaluate(ctx, globals)
		if err != nil {
			return "", fmt.Errorf("failed to evaluate template expression: %w", err)
		}
		parts[j] = result.String()
		scriptIdx++
	}
	return strings.Join(parts, ""), nil
}

// EvalString evaluates the template and always returns a string. For
// single-expression templates this stringifies the typed value; use
// Eval instead when preserving the underlying type matters.
func (e *Template) EvalString(ctx context.Context, globals map[string]any) (string, error) {
	v, err := e.Eval(ctx, globals)
	if err != nil {
		return "", err
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	return fmt.Sprintf("%v", v), nil
}
