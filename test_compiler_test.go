package workflow

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/deepnoodle-ai/workflow/script"
)

// testCompiler is a tiny expression evaluator used by workflow tests that
// need the path layer to compile and evaluate expressions without pulling in
// a real scripting engine. It supports the small subset of syntax required
// by the tests:
//
//   - dotted identifier paths: foo.bar.baz
//   - integer literals: 42
//   - string literals: "text" or 'text'
//   - array literals of simple elements: [1, 2, 3]
//   - binary operators: + - * / > < >= <= == !=
//
// It deliberately does not handle parentheses, function calls, or nested
// expressions. Anything outside this grammar returns a compile error.
type testCompiler struct{}

// NewTestCompiler returns the package's test stub compiler. It is exported
// only through this test file so that external test packages
// (package workflow_test) can reach it the same way the internal test
// package does.
func NewTestCompiler() script.Compiler { return testCompiler{} }

func newTestCompiler() script.Compiler { return testCompiler{} }

func (testCompiler) Compile(ctx context.Context, code string) (script.Script, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("empty expression")
	}
	node, err := parseTestExpr(code)
	if err != nil {
		return nil, fmt.Errorf("invalid expression %q: %w", code, err)
	}
	return &testScript{node: node}, nil
}

type testScript struct {
	node testNode
}

func (s *testScript) Evaluate(ctx context.Context, globals map[string]any) (script.Value, error) {
	v, err := s.node.eval(globals)
	if err != nil {
		return nil, err
	}
	return &testValue{v: v}, nil
}

type testValue struct{ v any }

func (t *testValue) Value() any            { return t.v }
func (t *testValue) Items() ([]any, error) { return script.EachValue(t.v) }
func (t *testValue) String() string        { return fmt.Sprintf("%v", t.v) }
func (t *testValue) IsTruthy() bool        { return script.IsTruthyValue(t.v) }

type testNode interface {
	eval(globals map[string]any) (any, error)
}

type literalNode struct{ v any }

func (l literalNode) eval(map[string]any) (any, error) { return l.v, nil }

type arrayNode struct{ items []testNode }

func (a arrayNode) eval(globals map[string]any) (any, error) {
	result := make([]any, len(a.items))
	for i, item := range a.items {
		v, err := item.eval(globals)
		if err != nil {
			return nil, err
		}
		result[i] = v
	}
	return result, nil
}

type pathNode struct{ segments []string }

func (p pathNode) eval(globals map[string]any) (any, error) {
	var current any = globals
	for i, seg := range p.segments {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("undefined variable %q: not a map at segment %d", strings.Join(p.segments, "."), i)
		}
		v, ok := m[seg]
		if !ok {
			return nil, fmt.Errorf("undefined variable %q", strings.Join(p.segments, "."))
		}
		current = v
	}
	return current, nil
}

type binaryNode struct {
	op       string
	lhs, rhs testNode
}

func (b binaryNode) eval(globals map[string]any) (any, error) {
	lv, err := b.lhs.eval(globals)
	if err != nil {
		return nil, err
	}
	rv, err := b.rhs.eval(globals)
	if err != nil {
		return nil, err
	}
	if b.op == "&&" || b.op == "||" {
		lb := script.IsTruthyValue(lv)
		rb := script.IsTruthyValue(rv)
		if b.op == "&&" {
			return lb && rb, nil
		}
		return lb || rb, nil
	}
	// Equality works across types (string-string, numeric-numeric, bool-bool).
	if b.op == "==" || b.op == "!=" {
		if lf, lok := toFloat(lv); lok {
			if rf, rok := toFloat(rv); rok {
				eq := lf == rf
				if b.op == "!=" {
					eq = !eq
				}
				return eq, nil
			}
		}
		eq := lv == rv
		if b.op == "!=" {
			eq = !eq
		}
		return eq, nil
	}
	lf, lok := toFloat(lv)
	rf, rok := toFloat(rv)
	if !lok || !rok {
		return nil, fmt.Errorf("non-numeric operand in %v %s %v", lv, b.op, rv)
	}
	switch b.op {
	case "+":
		return lf + rf, nil
	case "-":
		return lf - rf, nil
	case "*":
		return lf * rf, nil
	case "/":
		if rf == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		return lf / rf, nil
	case ">":
		return lf > rf, nil
	case "<":
		return lf < rf, nil
	case ">=":
		return lf >= rf, nil
	case "<=":
		return lf <= rf, nil
	}
	return nil, fmt.Errorf("unsupported operator %q", b.op)
}

func toFloat(v any) (float64, bool) {
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

// parseTestExpr parses the limited grammar supported by testCompiler.
// Precedence (lowest to highest): ||, &&, comparison, additive, multiplicative.
func parseTestExpr(s string) (testNode, error) {
	s = strings.TrimSpace(s)
	// Logical operators first (lowest precedence).
	for _, op := range []string{"||", "&&"} {
		if idx := findOp(s, op); idx >= 0 {
			return parseBinary(s, idx, len(op), op)
		}
	}
	// Comparison operators.
	for _, op := range []string{">=", "<=", "==", "!="} {
		if idx := findOp(s, op); idx >= 0 {
			return parseBinary(s, idx, len(op), op)
		}
	}
	for _, op := range []string{">", "<"} {
		if idx := findOp(s, op); idx >= 0 {
			return parseBinary(s, idx, len(op), op)
		}
	}
	// Arithmetic operators.
	for _, op := range []string{"+", "-", "*", "/"} {
		if idx := findOp(s, op); idx >= 0 {
			return parseBinary(s, idx, len(op), op)
		}
	}
	return parseAtom(s)
}

func parseBinary(s string, idx, oplen int, op string) (testNode, error) {
	lhs, err := parseTestExpr(strings.TrimSpace(s[:idx]))
	if err != nil {
		return nil, err
	}
	rhs, err := parseTestExpr(strings.TrimSpace(s[idx+oplen:]))
	if err != nil {
		return nil, err
	}
	return binaryNode{op: op, lhs: lhs, rhs: rhs}, nil
}

// findOp finds an operator in s, ignoring operators inside [ ] and quotes,
// and skipping characters that are part of other operators (e.g. "<" inside "<=").
func findOp(s string, op string) int {
	depth := 0
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
			continue
		case '[':
			depth++
			continue
		case ']':
			depth--
			continue
		}
		if depth > 0 {
			continue
		}
		if i+len(op) > len(s) {
			continue
		}
		if s[i:i+len(op)] != op {
			continue
		}
		// Avoid matching the "<" in "<=" or ">" in ">=" when caller asked for the single-char form.
		if len(op) == 1 && (op == "<" || op == ">" || op == "=" || op == "!") {
			if i+1 < len(s) && s[i+1] == '=' {
				continue
			}
		}
		return i
	}
	return -1
}

func parseAtom(s string) (testNode, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty atom")
	}
	// Array literal
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return arrayNode{}, nil
		}
		parts := strings.Split(inner, ",")
		items := make([]testNode, 0, len(parts))
		for _, part := range parts {
			node, err := parseAtom(strings.TrimSpace(part))
			if err != nil {
				return nil, err
			}
			items = append(items, node)
		}
		return arrayNode{items: items}, nil
	}
	// String literal
	if (strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`)) ||
		(strings.HasPrefix(s, `'`) && strings.HasSuffix(s, `'`)) {
		return literalNode{v: s[1 : len(s)-1]}, nil
	}
	// Int literal
	if n, err := strconv.Atoi(s); err == nil {
		return literalNode{v: n}, nil
	}
	// Float literal
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return literalNode{v: f}, nil
	}
	// Bool literal
	if s == "true" {
		return literalNode{v: true}, nil
	}
	if s == "false" {
		return literalNode{v: false}, nil
	}
	// Dotted identifier path
	if isIdentifierPath(s) {
		return pathNode{segments: strings.Split(s, ".")}, nil
	}
	return nil, fmt.Errorf("unrecognized atom %q", s)
}

func isIdentifierPath(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for i, r := range part {
			if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')) {
				return false
			}
		}
	}
	return true
}
