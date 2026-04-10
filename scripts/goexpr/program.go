package goexpr

import (
	"fmt"
	"go/ast"
	"go/token"
	"math"
	"reflect"
	"strconv"
)

// Program is a compiled expression. Programs are immutable and safe for
// concurrent evaluation across goroutines.
type Program struct {
	source string
	root   ast.Expr
	funcs  map[string]any
}

// Source returns the original expression text.
func (p *Program) Source() string { return p.source }

// Run evaluates the program against env. env may be a map[string]any, a
// struct, or a pointer to a struct — identifier lookups resolve to map
// keys, struct fields, or bound methods (in that order of preference).
// Identifiers not found in env are then looked up against the engine's
// registered functions. The literals true, false, and nil are recognized
// directly. Any unsupported syntax node (slice expressions, type
// assertions, function literals, channel operations, etc.) returns an
// error wrapping ErrEvaluate.
func (p *Program) Run(env any) (any, error) {
	return p.eval(p.root, env)
}

func (p *Program) eval(node ast.Expr, env any) (any, error) {
	switch n := node.(type) {
	case *ast.BasicLit:
		return evalLiteral(n)
	case *ast.Ident:
		return evalIdent(n, env, p.funcs)
	case *ast.ParenExpr:
		return p.eval(n.X, env)
	case *ast.UnaryExpr:
		return p.evalUnary(n, env)
	case *ast.BinaryExpr:
		return p.evalBinary(n, env)
	case *ast.SelectorExpr:
		return p.evalSelector(n, env)
	case *ast.IndexExpr:
		return p.evalIndex(n, env)
	case *ast.CallExpr:
		return p.evalCall(n, env)
	}
	return nil, fmt.Errorf("%w: unsupported syntax %T", ErrEvaluate, node)
}

func evalLiteral(n *ast.BasicLit) (any, error) {
	switch n.Kind {
	case token.INT:
		i, err := strconv.ParseInt(n.Value, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvaluate, err)
		}
		return i, nil
	case token.FLOAT:
		f, err := strconv.ParseFloat(n.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvaluate, err)
		}
		return f, nil
	case token.STRING:
		s, err := strconv.Unquote(n.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvaluate, err)
		}
		return s, nil
	case token.CHAR:
		s, err := strconv.Unquote(n.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvaluate, err)
		}
		runes := []rune(s)
		if len(runes) != 1 {
			return nil, fmt.Errorf("%w: invalid char literal %q", ErrEvaluate, n.Value)
		}
		return int64(runes[0]), nil
	}
	return nil, fmt.Errorf("%w: unsupported literal kind %v", ErrEvaluate, n.Kind)
}

func evalIdent(n *ast.Ident, env any, funcs map[string]any) (any, error) {
	switch n.Name {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "nil":
		return nil, nil
	}
	if v, ok := lookupEnv(env, n.Name); ok {
		return v, nil
	}
	if fn, ok := funcs[n.Name]; ok {
		return fn, nil
	}
	return nil, fmt.Errorf("%w: undefined identifier %q", ErrEvaluate, n.Name)
}

// lookupEnv resolves a top-level identifier against env. env may be a
// map[string]any, any map with string keys, a struct, or a pointer to a
// struct. For structs, fields are preferred over methods when both match.
// Methods are returned as bound function values so they can be invoked by
// a CallExpr node.
func lookupEnv(env any, name string) (any, bool) {
	if env == nil {
		return nil, false
	}
	if m, ok := env.(map[string]any); ok {
		v, ok := m[name]
		return v, ok
	}
	rv := reflect.ValueOf(env)
	// Check methods on the original (possibly pointer) value first so
	// pointer-receiver methods are visible — but prefer struct fields if
	// both exist with the same name.
	orig := rv
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		if fv := rv.FieldByName(name); fv.IsValid() && fv.CanInterface() {
			return fv.Interface(), true
		}
		if mv := orig.MethodByName(name); mv.IsValid() {
			return mv.Interface(), true
		}
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			mv := rv.MapIndex(reflect.ValueOf(name))
			if mv.IsValid() {
				return mv.Interface(), true
			}
		}
	}
	return nil, false
}

func (p *Program) evalUnary(n *ast.UnaryExpr, env any) (any, error) {
	v, err := p.eval(n.X, env)
	if err != nil {
		return nil, err
	}
	switch n.Op {
	case token.NOT:
		return !isTruthy(v), nil
	case token.SUB:
		if i, ok := toInt64(v); ok {
			return -i, nil
		}
		if f, ok := toFloat64(v); ok {
			return -f, nil
		}
		return nil, fmt.Errorf("%w: cannot negate %T", ErrEvaluate, v)
	case token.ADD:
		if _, ok := toInt64(v); ok {
			return v, nil
		}
		if _, ok := toFloat64(v); ok {
			return v, nil
		}
		return nil, fmt.Errorf("%w: cannot apply unary + to %T", ErrEvaluate, v)
	}
	return nil, fmt.Errorf("%w: unsupported unary operator %v", ErrEvaluate, n.Op)
}

func (p *Program) evalBinary(n *ast.BinaryExpr, env any) (any, error) {
	// Short-circuit logical operators: right-hand side is not evaluated
	// when the left-hand side is sufficient to determine the result.
	if n.Op == token.LAND || n.Op == token.LOR {
		lhs, err := p.eval(n.X, env)
		if err != nil {
			return nil, err
		}
		lt := isTruthy(lhs)
		if n.Op == token.LAND && !lt {
			return false, nil
		}
		if n.Op == token.LOR && lt {
			return true, nil
		}
		rhs, err := p.eval(n.Y, env)
		if err != nil {
			return nil, err
		}
		return isTruthy(rhs), nil
	}

	lhs, err := p.eval(n.X, env)
	if err != nil {
		return nil, err
	}
	rhs, err := p.eval(n.Y, env)
	if err != nil {
		return nil, err
	}
	return applyBinary(n.Op, lhs, rhs)
}

func applyBinary(op token.Token, lhs, rhs any) (any, error) {
	// String concatenation and comparison.
	if ls, lok := lhs.(string); lok {
		if rs, rok := rhs.(string); rok {
			switch op {
			case token.ADD:
				return ls + rs, nil
			case token.EQL:
				return ls == rs, nil
			case token.NEQ:
				return ls != rs, nil
			case token.LSS:
				return ls < rs, nil
			case token.GTR:
				return ls > rs, nil
			case token.LEQ:
				return ls <= rs, nil
			case token.GEQ:
				return ls >= rs, nil
			}
		}
	}

	// Equality across arbitrary comparable types.
	if op == token.EQL || op == token.NEQ {
		if eq, ok := looseEqual(lhs, rhs); ok {
			if op == token.NEQ {
				return !eq, nil
			}
			return eq, nil
		}
	}

	// Numeric operations: stay in int64 when both sides are integral,
	// promote to float64 otherwise.
	li, liOk := toInt64(lhs)
	ri, riOk := toInt64(rhs)
	if liOk && riOk {
		switch op {
		case token.ADD:
			return li + ri, nil
		case token.SUB:
			return li - ri, nil
		case token.MUL:
			return li * ri, nil
		case token.QUO:
			if ri == 0 {
				return nil, fmt.Errorf("%w: division by zero", ErrEvaluate)
			}
			return li / ri, nil
		case token.REM:
			if ri == 0 {
				return nil, fmt.Errorf("%w: modulo by zero", ErrEvaluate)
			}
			return li % ri, nil
		case token.LSS:
			return li < ri, nil
		case token.GTR:
			return li > ri, nil
		case token.LEQ:
			return li <= ri, nil
		case token.GEQ:
			return li >= ri, nil
		}
	}

	lf, lfOk := toFloat64(lhs)
	rf, rfOk := toFloat64(rhs)
	if lfOk && rfOk {
		switch op {
		case token.ADD:
			return lf + rf, nil
		case token.SUB:
			return lf - rf, nil
		case token.MUL:
			return lf * rf, nil
		case token.QUO:
			if rf == 0 {
				return nil, fmt.Errorf("%w: division by zero", ErrEvaluate)
			}
			return lf / rf, nil
		case token.REM:
			if rf == 0 {
				return nil, fmt.Errorf("%w: modulo by zero", ErrEvaluate)
			}
			return math.Mod(lf, rf), nil
		case token.LSS:
			return lf < rf, nil
		case token.GTR:
			return lf > rf, nil
		case token.LEQ:
			return lf <= rf, nil
		case token.GEQ:
			return lf >= rf, nil
		}
	}

	return nil, fmt.Errorf("%w: operator %v not supported for %T and %T", ErrEvaluate, op, lhs, rhs)
}

func (p *Program) evalSelector(n *ast.SelectorExpr, env any) (any, error) {
	recv, err := p.eval(n.X, env)
	if err != nil {
		return nil, err
	}
	return selectField(recv, n.Sel.Name)
}

func selectField(recv any, name string) (any, error) {
	if recv == nil {
		return nil, fmt.Errorf("%w: cannot access %q on nil", ErrEvaluate, name)
	}
	if m, ok := recv.(map[string]any); ok {
		v, ok := m[name]
		if !ok {
			return nil, fmt.Errorf("%w: key %q not found", ErrEvaluate, name)
		}
		return v, nil
	}
	rv := reflect.ValueOf(recv)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%w: cannot select %q on map with non-string keys", ErrEvaluate, name)
		}
		mv := rv.MapIndex(reflect.ValueOf(name))
		if !mv.IsValid() {
			return nil, fmt.Errorf("%w: key %q not found", ErrEvaluate, name)
		}
		return mv.Interface(), nil
	case reflect.Struct:
		fv := rv.FieldByName(name)
		if !fv.IsValid() {
			return nil, fmt.Errorf("%w: field %q not found on %T", ErrEvaluate, name, recv)
		}
		return fv.Interface(), nil
	case reflect.Pointer:
		if rv.IsNil() {
			return nil, fmt.Errorf("%w: cannot access %q on nil pointer", ErrEvaluate, name)
		}
		return selectField(rv.Elem().Interface(), name)
	}
	return nil, fmt.Errorf("%w: cannot select %q on %T", ErrEvaluate, name, recv)
}

func (p *Program) evalIndex(n *ast.IndexExpr, env any) (any, error) {
	recv, err := p.eval(n.X, env)
	if err != nil {
		return nil, err
	}
	idx, err := p.eval(n.Index, env)
	if err != nil {
		return nil, err
	}
	return indexValue(recv, idx)
}

func indexValue(recv, idx any) (any, error) {
	if recv == nil {
		return nil, fmt.Errorf("%w: cannot index nil", ErrEvaluate)
	}
	if m, ok := recv.(map[string]any); ok {
		key, ok := idx.(string)
		if !ok {
			return nil, fmt.Errorf("%w: map index must be string, got %T", ErrEvaluate, idx)
		}
		v, ok := m[key]
		if !ok {
			return nil, fmt.Errorf("%w: key %q not found", ErrEvaluate, key)
		}
		return v, nil
	}
	rv := reflect.ValueOf(recv)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		i, ok := toInt64(idx)
		if !ok {
			return nil, fmt.Errorf("%w: index must be integer, got %T", ErrEvaluate, idx)
		}
		if i < 0 || int(i) >= rv.Len() {
			return nil, fmt.Errorf("%w: index %d out of range [0, %d)", ErrEvaluate, i, rv.Len())
		}
		return rv.Index(int(i)).Interface(), nil
	case reflect.String:
		i, ok := toInt64(idx)
		if !ok {
			return nil, fmt.Errorf("%w: index must be integer, got %T", ErrEvaluate, idx)
		}
		runes := []rune(rv.String())
		if i < 0 || int(i) >= len(runes) {
			return nil, fmt.Errorf("%w: index %d out of range [0, %d)", ErrEvaluate, i, len(runes))
		}
		return string(runes[i]), nil
	case reflect.Map:
		kv := reflect.ValueOf(idx)
		if !kv.Type().AssignableTo(rv.Type().Key()) {
			if !kv.Type().ConvertibleTo(rv.Type().Key()) {
				return nil, fmt.Errorf("%w: cannot use %T as map key %v", ErrEvaluate, idx, rv.Type().Key())
			}
			kv = kv.Convert(rv.Type().Key())
		}
		mv := rv.MapIndex(kv)
		if !mv.IsValid() {
			return nil, fmt.Errorf("%w: key %v not found", ErrEvaluate, idx)
		}
		return mv.Interface(), nil
	}
	return nil, fmt.Errorf("%w: cannot index %T", ErrEvaluate, recv)
}

func (p *Program) evalCall(n *ast.CallExpr, env any) (any, error) {
	name, fn, err := p.resolveCallable(n.Fun, env)
	if err != nil {
		return nil, err
	}
	args := make([]any, len(n.Args))
	for i, a := range n.Args {
		v, err := p.eval(a, env)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return callFunction(name, fn, args)
}

// resolveCallable finds the function value for a call target. It supports
// bare identifiers (looked up in env, then in the registered builtins) and
// selector expressions (e.g. `state.user.Greet`), which resolve to bound
// methods on structs/pointers or callable values stored inside maps.
func (p *Program) resolveCallable(fun ast.Expr, env any) (string, any, error) {
	switch f := fun.(type) {
	case *ast.Ident:
		if v, ok := lookupEnv(env, f.Name); ok {
			return f.Name, v, nil
		}
		if v, ok := p.funcs[f.Name]; ok {
			return f.Name, v, nil
		}
		return "", nil, fmt.Errorf("%w: unknown function %q", ErrEvaluate, f.Name)
	case *ast.SelectorExpr:
		recv, err := p.eval(f.X, env)
		if err != nil {
			return "", nil, err
		}
		fn, err := resolveMethod(recv, f.Sel.Name)
		if err != nil {
			return "", nil, err
		}
		return f.Sel.Name, fn, nil
	}
	return "", nil, fmt.Errorf("%w: unsupported call target %T", ErrEvaluate, fun)
}

// resolveMethod looks up a callable attribute `name` on recv. For structs
// and pointers it returns a bound method value; for map[string]any (or any
// map with string keys) it returns the stored value; for map-like types
// with other key kinds it fails. Missing methods return a descriptive error.
func resolveMethod(recv any, name string) (any, error) {
	if recv == nil {
		return nil, fmt.Errorf("%w: cannot call %q on nil", ErrEvaluate, name)
	}
	if m, ok := recv.(map[string]any); ok {
		if v, ok := m[name]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("%w: %q not found", ErrEvaluate, name)
	}
	rv := reflect.ValueOf(recv)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, fmt.Errorf("%w: cannot call %q on nil pointer", ErrEvaluate, name)
	}
	if mv := rv.MethodByName(name); mv.IsValid() {
		return mv.Interface(), nil
	}
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
		if mv := rv.MethodByName(name); mv.IsValid() {
			return mv.Interface(), nil
		}
	}
	switch rv.Kind() {
	case reflect.Struct:
		if fv := rv.FieldByName(name); fv.IsValid() && fv.CanInterface() {
			return fv.Interface(), nil
		}
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			mv := rv.MapIndex(reflect.ValueOf(name))
			if mv.IsValid() {
				return mv.Interface(), nil
			}
		}
	}
	return nil, fmt.Errorf("%w: method %q not found on %T", ErrEvaluate, name, recv)
}
