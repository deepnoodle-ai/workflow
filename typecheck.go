package workflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

// KindSchemas is an optional interface for [Kinds]. A host that knows what
// a step kind takes and returns (an action's tool schema, an agent's
// structured output) implements it, and Validate then checks `with:`
// against the params schema and types expressions that read the step's
// output. Apply validates params at dispatch and outputs on completion.
//
// A step's own `output:` overrides the output schema returned here.
// Either schema may be nil.
type KindSchemas interface {
	Schemas(kind string, config map[string]any) (params, output *Schema, err error)
}

// settled is what is known to have settled when a step runs, as clauses:
// at least one step in each clause has settled. A one-step clause means
// that step has. Clauses let alternatives converge: after `report` or
// `approve`, the clause {approve, report} holds even though neither step
// is certain, and coalesce(report, approve) is known not to be nil.
type settled [][]string

const (
	maxClauseSize = 6
	maxClauses    = 64
)

func (f settled) has(name string) bool {
	for _, c := range f {
		if len(c) == 1 && c[0] == name {
			return true
		}
	}
	return false
}

// covers reports whether some clause lies within names.
func (f settled) covers(names map[string]bool) bool {
	for _, c := range f {
		all := true
		for _, n := range c {
			if !names[n] {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func (f settled) with(name string) settled {
	return minimize(append(append(settled{}, f...), []string{name}))
}

func conjoin(a, b settled) settled { return minimize(append(append(settled{}, a...), b...)) }

// disjoin is what holds when a or b holds.
func disjoin(a, b settled) settled {
	var out settled
	for _, x := range a {
		for _, y := range b {
			out = append(out, unionSorted(x, y))
		}
	}
	return minimize(out)
}

func unionSorted(a, b []string) []string {
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		seen[s] = true
	}
	return sortedKeys(seen)
}

// minimize drops duplicate and implied clauses and bounds the set.
func minimize(f settled) settled {
	sort.Slice(f, func(i, j int) bool {
		if len(f[i]) != len(f[j]) {
			return len(f[i]) < len(f[j])
		}
		return strings.Join(f[i], ",") < strings.Join(f[j], ",")
	})
	var out settled
	for _, c := range f {
		if len(c) > maxClauseSize || len(out) >= maxClauses {
			continue
		}
		implied := false
		for _, kept := range out {
			if subset(kept, c) {
				implied = true
				break
			}
		}
		if !implied {
			out = append(out, c)
		}
	}
	return out
}

func subset(a, b []string) bool {
	for _, x := range a {
		if !contains(b, x) {
			return false
		}
	}
	return true
}

// topoOrder lists reachable steps so that every step comes after its
// predecessors over forward edges.
func (d *Definition) topoOrder() []string {
	var post []string
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		seen[n] = true
		for _, m := range d.byName[n].successors() {
			if !seen[m] && !d.back[[2]string{n, m}] {
				walk(m)
			}
		}
		post = append(post, n)
	}
	walk(d.Steps[0].Name)
	for i, j := 0, len(post)-1; i < j; i, j = i+1, j-1 {
		post[i], post[j] = post[j], post[i]
	}
	return post
}

func (d *Definition) computeFacts(order []string) {
	preds := map[string][]string{}
	for _, s := range d.Steps {
		for _, m := range s.successors() {
			if !d.back[[2]string{s.Name, m}] && !contains(preds[m], s.Name) {
				preds[m] = append(preds[m], s.Name)
			}
		}
	}
	d.facts = map[string]settled{}
	for i, name := range order {
		var f settled
		if i > 0 {
			ps := preds[name]
			sort.Strings(ps)
			for j, p := range ps {
				out := d.facts[p].with(p)
				if j == 0 {
					f = out
				} else {
					f = disjoin(f, out)
				}
			}
		}
		if s := d.byName[name]; s.Kind == KindJoin {
			// A join that fires has seen every step it names.
			for _, n := range s.Join {
				f = conjoin(f, d.facts[n].with(n))
			}
		}
		d.facts[name] = f
	}
}

// typer holds the types Validate infers.
type typer struct {
	d        *Definition
	p        *problems
	skip     map[string]bool // steps whose reads already failed checkReads
	values   map[string]*expr.Type
	inputs   *expr.Type
	event    *expr.Type
	errValue *expr.Type
}

var runErrorType = expr.ObjectOf(map[string]*expr.Type{
	"step": expr.String(), "step_id": expr.String(), "type": expr.String(), "message": expr.String(),
})

var stepErrorValue = expr.ObjectOf(map[string]*expr.Type{
	"error": expr.ObjectOf(map[string]*expr.Type{"type": expr.String(), "message": expr.String()}),
})

func (d *Definition) typecheck(p *problems, skip map[string]bool) {
	order := d.topoOrder()
	d.computeFacts(order)
	t := &typer{d: d, p: p, skip: skip, values: map[string]*expr.Type{}}

	fields := map[string]*expr.Type{}
	for name, in := range d.Inputs {
		ft := in.Schema.exprType()
		if !in.Required && in.Default == nil {
			ft = ft.OrNil()
		}
		fields[name] = ft
	}
	t.inputs = expr.ObjectOf(fields)
	if d.Inputs == nil {
		t.inputs = expr.OpenObject(nil)
	}
	t.event = expr.Any
	if d.EventSchema != nil {
		t.event = d.EventSchema.exprType()
	}

	for _, name := range order {
		t.step(d.byName[name])
	}
	t.failure()
	if d.outputs != nil {
		env := t.allOptional()
		if typ, problems := t.template(d.outputs, env, nil); typ != nil {
			for _, msg := range problems {
				p.add("", "outputs: %s", msg)
			}
		}
	}
}

// valueType is what a step's name reads as once it completes.
func (t *typer) valueType(s *Step) *expr.Type {
	switch s.Kind {
	case KindJoin, KindSleep:
		return expr.Null()
	case KindSet:
		if v := t.values[s.Name]; v != nil {
			return v
		}
		return expr.Any
	}
	out := expr.Any
	if s.Output != nil {
		out = s.Output.exprType()
	}
	if s.Each != "" {
		if s.ContinueOnError {
			out = expr.Join(out, stepErrorValue)
		}
		return expr.ListOf(out)
	}
	return out
}

// readType is what reader sees for step x: a caught failure reads as
// {error: ...} on the paths the catch routes to.
func (t *typer) readType(x, reader *Step) *expr.Type {
	v := t.valueType(x)
	for _, c := range x.Catch {
		if c.Next == reader.Name || t.d.reach[c.Next][reader.Name] {
			return expr.Join(v, stepErrorValue)
		}
	}
	return v
}

// env builds the expression types a step sees. self adds the step's own
// value, for its edges.
func (t *typer) env(s *Step, self bool) (map[string]*expr.Type, settled) {
	fact := t.d.facts[s.Name]
	if self {
		fact = fact.with(s.Name)
	}
	env := map[string]*expr.Type{"inputs": t.inputs, "event": t.event}
	for _, x := range t.d.Steps {
		v := t.readType(x, s)
		switch {
		case fact.has(x.Name):
			env[x.Name] = v
		case t.d.reach[x.Name][s.Name] || (self && x == s):
			env[x.Name] = v.OrNil()
		default:
			env[x.Name] = expr.Null() // never settled here; checkReads reports reads
		}
	}
	return env, fact
}

func (t *typer) allOptional() map[string]*expr.Type {
	env := map[string]*expr.Type{"inputs": t.inputs, "event": t.event}
	for _, x := range t.d.Steps {
		env[x.Name] = t.valueType(x).OrNil()
	}
	return env
}

// funcTypes types the library's own functions. coalesce is known not to be
// nil when its arguments cover alternatives that one of which has settled.
func (t *typer) funcTypes(env map[string]*expr.Type, fact settled) []expr.CheckOption {
	names := map[*expr.Type]string{}
	for name, typ := range env {
		if typ != expr.Any {
			names[typ] = name
		}
	}
	coalesce := func(args []*expr.Type) (*expr.Type, error) {
		var out *expr.Type
		covered := map[string]bool{}
		nonNil := false
		for _, a := range args {
			if a == nil || a.Kind == expr.KindAny {
				return expr.Any, nil
			}
			if n, ok := names[a]; ok {
				covered[n] = true
			}
			if a.Kind == expr.KindNull {
				continue
			}
			if !a.Optional {
				nonNil = true
			}
			out = expr.Join(out, a.NonNil())
		}
		if out == nil {
			return expr.Null(), nil
		}
		if nonNil || fact.covers(covered) {
			return out.NonNil(), nil
		}
		return out.OrNil(), nil
	}
	concat := func(args []*expr.Type) (*expr.Type, error) {
		var elem *expr.Type
		for _, a := range args {
			switch {
			case a == nil || a.Kind == expr.KindAny:
				return expr.ListOf(expr.Any), nil
			case a.Kind == expr.KindNull:
			case a.Kind == expr.KindList:
				elem = expr.Join(elem, a.Elem)
			default:
				elem = expr.Join(elem, a.NonNil())
			}
		}
		if elem == nil {
			elem = expr.Any
		}
		return expr.ListOf(elem), nil
	}
	return []expr.CheckOption{expr.WithFuncType("coalesce", coalesce), expr.WithFuncType("concat", concat)}
}

func (t *typer) step(s *Step) {
	if t.skip[s.Name] {
		if s.Kind == KindSet {
			t.values[s.Name] = expr.Any
		}
		return
	}
	env, fact := t.env(s, false)
	opts := t.funcTypes(env, fact)

	withEnv := env
	if s.each != nil {
		typ, err := s.each.p.Check(env, opts...)
		t.report(s, "each", err)
		elem := expr.Any
		switch {
		case typ == nil || typ.Kind == expr.KindAny || typ.Kind == expr.KindNull:
		case typ.Kind == expr.KindList:
			if typ.Elem != nil {
				elem = typ.Elem
			}
		default:
			t.p.add(s.Name, "each must produce a list, got %s", typ)
		}
		withEnv = copyEnv(env)
		withEnv["item"], withEnv["index"] = elem, expr.Number()
		opts = t.funcTypes(withEnv, fact)
	}
	if s.with != nil {
		typ, problems := t.template(s.with, withEnv, opts)
		for _, msg := range problems {
			t.p.add(s.Name, "with: %s", msg)
		}
		if s.params != nil && typ != nil {
			var out []string
			assign(typ, s.params, "with", true, &out)
			for _, msg := range out {
				t.p.add(s.Name, "%s", msg)
			}
		}
	}
	if s.set != nil {
		typ, problems := t.template(s.set, env, opts)
		for _, msg := range problems {
			t.p.add(s.Name, "set: %s", msg)
		}
		t.values[s.Name] = typ
	}

	selfEnv, selfFact := t.env(s, true)
	selfOpts := t.funcTypes(selfEnv, selfFact)
	for i, prog := range s.edges {
		if prog == nil {
			continue
		}
		typ, err := prog.p.Check(selfEnv, selfOpts...)
		where := fmt.Sprintf("next %s: when", s.Next[i].Step)
		t.report(s, where, err)
		if err == nil && typ.Kind != expr.KindAny && (typ.Kind != expr.KindBool || typ.Optional) {
			t.p.add(s.Name, "%s must be a boolean, got %s", where, typ)
		}
	}
	if s.swtch != nil {
		typ, err := s.swtch.p.Check(selfEnv, selfOpts...)
		t.report(s, "next: switch", err)
		if err == nil {
			t.checkSwitch(s, typ)
		}
	}
}

func (t *typer) checkSwitch(s *Step, typ *expr.Type) {
	sw := s.Switch
	switch typ.Kind {
	case expr.KindAny:
		if !sw.HasDefault {
			t.p.add(s.Name, "next: switch on %s needs a default: its type is unknown", sw.Expr)
		}
		return
	case expr.KindString, expr.KindNumber, expr.KindBool:
	default:
		t.p.add(s.Name, "next: switch on %s: cannot switch on %s", sw.Expr, typ)
		return
	}
	if typ.Optional {
		t.p.add(s.Name, "next: switch on %s: the value may be nil; use coalesce(...) or switch on a value that is always set", sw.Expr)
	}
	values := typ.Enum
	if typ.Kind == expr.KindBool && len(values) == 0 {
		values = []any{false, true}
	}
	if len(values) == 0 {
		if !sw.HasDefault {
			t.p.add(s.Name, "next: switch on %s needs a default: %s has no fixed set of values", sw.Expr, typ)
		}
		return
	}
	keys := map[string]bool{}
	for _, v := range values {
		keys[switchKey(v)] = true
	}
	handled := map[string]bool{}
	for _, c := range sw.Cases {
		if !keys[c.Value] {
			t.p.add(s.Name, "next: case %q: %s is never %q (it is one of %s)", c.Value, sw.Expr, c.Value,
				strings.Join(sortedKeys(keys), ", "))
		}
		handled[c.Value] = true
	}
	var missing []string
	for k := range keys {
		if !handled[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 && !sw.HasDefault {
		t.p.add(s.Name, "next: switch on %s does not handle %s; add cases or a default", sw.Expr, strings.Join(missing, ", "))
	}
}

func switchKey(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	switch n := v.(type) {
	case int64:
		return fmt.Sprint(n)
	case float64:
		return fmt.Sprint(n)
	}
	return fmt.Sprint(v)
}

// failure checks on_failure steps. They see every step as maybe-nil,
// earlier on_failure steps as settled, and error.
func (t *typer) failure() {
	env := t.allOptional()
	env["error"] = runErrorType
	for _, s := range t.d.OnFailure {
		opts := t.funcTypes(env, nil)
		if s.with != nil {
			typ, problems := t.template(s.with, env, opts)
			for _, msg := range problems {
				t.p.add(s.Name, "with: %s", msg)
			}
			if s.params != nil && typ != nil {
				var out []string
				assign(typ, s.params, "with", true, &out)
				for _, msg := range out {
					t.p.add(s.Name, "%s", msg)
				}
			}
		}
		var v *expr.Type = expr.Any
		if s.set != nil {
			typ, problems := t.template(s.set, env, opts)
			for _, msg := range problems {
				t.p.add(s.Name, "set: %s", msg)
			}
			v = typ
		} else if s.Output != nil {
			v = s.Output.exprType()
		}
		env = copyEnv(env)
		env[s.Name] = v
	}
}

func (t *typer) report(s *Step, what string, err error) {
	var te *expr.TypeError
	if errors.As(err, &te) {
		for _, msg := range te.Problems {
			t.p.add(s.Name, "%s: %s", what, msg)
		}
	}
}

// template types a with:/set:/outputs: value.
func (t *typer) template(tm *template, env map[string]*expr.Type, opts []expr.CheckOption) (*expr.Type, []string) {
	var problems []string
	collect := func(err error) {
		var te *expr.TypeError
		if errors.As(err, &te) {
			problems = append(problems, te.Problems...)
		}
	}
	var walk func(*template) *expr.Type
	walk = func(tm *template) *expr.Type {
		switch tm.kind {
		case tmplSingle:
			typ, err := tm.single.Check(env, opts...)
			collect(err)
			if typ == nil {
				return expr.Any
			}
			return typ
		case tmplText:
			collect(tm.text.Check(env, opts...))
			return expr.String()
		case tmplObject:
			fields := make(map[string]*expr.Type, len(tm.obj))
			for _, k := range tm.keys {
				fields[k] = walk(tm.obj[k])
			}
			return expr.ObjectOf(fields)
		case tmplList:
			var elem *expr.Type
			for _, c := range tm.list {
				elem = expr.Join(elem, walk(c))
			}
			if elem == nil {
				elem = expr.Any
			}
			return expr.ListOf(elem)
		default:
			return literalType(tm.lit)
		}
	}
	return walk(tm), problems
}

func literalType(v any) *expr.Type {
	switch t := v.(type) {
	case nil:
		return expr.Null()
	case string:
		return &expr.Type{Kind: expr.KindString, Enum: []any{t}}
	case float64:
		return &expr.Type{Kind: expr.KindNumber, Enum: []any{t}}
	case bool:
		return &expr.Type{Kind: expr.KindBool, Enum: []any{t}}
	case map[string]any:
		fields := make(map[string]*expr.Type, len(t))
		for k, fv := range t {
			fields[k] = literalType(fv)
		}
		return expr.ObjectOf(fields)
	case []any:
		var elem *expr.Type
		for _, item := range t {
			elem = expr.Join(elem, literalType(item))
		}
		if elem == nil {
			elem = expr.Any
		}
		return expr.ListOf(elem)
	}
	return expr.Any
}

func copyEnv(env map[string]*expr.Type) map[string]*expr.Type {
	out := make(map[string]*expr.Type, len(env)+2)
	for k, v := range env {
		out[k] = v
	}
	return out
}

// assign reports where a value of type v does not fit schema s.
func assign(v *expr.Type, s *Schema, path string, required bool, out *[]string) {
	s = s.deref()
	if s == nil || v == nil || v.Kind == expr.KindAny {
		return
	}
	if v.Kind == expr.KindNull {
		if required && !s.nullable() {
			*out = append(*out, fmt.Sprintf("%s: is always nil", path))
		}
		return
	}
	if v.Optional && required && !s.nullable() {
		*out = append(*out, fmt.Sprintf("%s: may be nil, but the schema requires %s", path, strings.Join(s.Type, " or ")))
	}
	if len(s.Type) > 0 && !s.allows(schemaKind(v)) {
		*out = append(*out, fmt.Sprintf("%s: %s is not %s", path, v.NonNil(), strings.Join(s.Type, " or ")))
		return
	}
	if len(s.Enum) > 0 {
		for _, ev := range v.Enum {
			found := false
			for _, se := range s.Enum {
				if jsonEqual(ev, se) {
					found = true
				}
			}
			if !found {
				*out = append(*out, fmt.Sprintf("%s: %s is not one of %s", path, describe(ev), formatEnum(s.Enum)))
			}
		}
	}
	switch v.Kind {
	case expr.KindObject:
		for _, name := range sortedKeys(s.Properties) {
			fv, ok := v.Fields[name]
			if !ok {
				if s.isRequired(name) && !v.Open {
					*out = append(*out, fmt.Sprintf("%s: missing required %q", path, name))
				}
				continue
			}
			assign(fv, s.Properties[name], path+"."+name, s.isRequired(name), out)
		}
		for _, name := range sortedKeys(v.Fields) {
			if _, ok := s.Properties[name]; ok {
				continue
			}
			switch {
			case s.Additional != nil:
				assign(v.Fields[name], s.Additional, path+"."+name, true, out)
			case s.AdditionalSet:
				*out = append(*out, fmt.Sprintf("%s: unexpected %q", path, name))
			}
		}
	case expr.KindList:
		assign(v.Elem, s.Items, path+"[]", true, out)
	}
}

func schemaKind(v *expr.Type) string {
	switch v.Kind {
	case expr.KindBool:
		return "boolean"
	case expr.KindNumber:
		return "integer" // a number fits number and integer; Apply checks the value
	case expr.KindString:
		return "string"
	case expr.KindList:
		return "array"
	case expr.KindObject:
		return "object"
	}
	return "null"
}
