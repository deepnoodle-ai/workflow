package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

// KindSchemas is an optional interface for [Kinds]. A host that knows what
// a step kind takes and returns (an action's tool schema, an agent's
// structured output) implements it, and Load then checks `with:`
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

// computeFacts solves, to a fixpoint over every edge (loops included), what
// has settled when each step runs. A step not yet reached by the iteration
// contributes nothing to a meet; facts only weaken as iteration goes on.
// Loop passes keep the values of earlier passes in scope, which is why back
// edges take part like any other edge.
func (d *Definition) computeFacts(order []string) {
	preds := map[string][]string{}
	for _, s := range d.Steps {
		for _, m := range s.successors() {
			if !contains(preds[m], s.Name) {
				preds[m] = append(preds[m], s.Name)
			}
		}
	}
	for _, ps := range preds {
		sort.Strings(ps)
	}
	facts := map[string]settled{order[0]: {}}
	for iter := 0; ; iter++ {
		if iter == 100 {
			// Did not converge: knowing nothing is always sound.
			for _, name := range order {
				facts[name] = settled{}
			}
			break
		}
		changed := false
		for _, name := range order[1:] {
			var f settled
			have := false
			for _, p := range preds[name] {
				pf, ok := facts[p]
				if !ok {
					continue
				}
				out := pf.with(p)
				if !have {
					f, have = out, true
				} else {
					f = disjoin(f, out)
				}
			}
			if !have {
				continue
			}
			if s := d.byName[name]; s.Kind == KindJoin {
				// A join that fires has seen every step it names.
				for _, n := range s.Join {
					f = conjoin(f, facts[n].with(n))
				}
			}
			if old, ok := facts[name]; !ok || !sameFacts(old, f) {
				facts[name] = f
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	d.facts = facts
}

func sameFacts(a, b settled) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.Join(a[i], ",") != strings.Join(b[i], ",") {
			return false
		}
	}
	return true
}

// typer holds the types Load infers.
type typer struct {
	d        *Definition
	p        *problems
	skip     map[string]bool // steps whose reads already failed checkReads
	values   map[string]*expr.Type
	types    schemaTypes
	input    *expr.Type
	errValue *expr.Type
}

var runErrorType = expr.ObjectOf(map[string]*expr.Type{
	"step": expr.String(), "step_id": expr.String(), "type": expr.String(), "message": expr.String(),
})

// runType is the run global of finally steps: the outcome, and the run's
// error when it failed.
var runType = expr.ObjectOf(map[string]*expr.Type{
	"status": {Kind: expr.KindString, Enum: []any{string(StatusCancelled), string(StatusCompleted), string(StatusFailed)}},
	"error":  runErrorType.OrNil(),
})

var stepErrorValue = expr.ObjectOf(map[string]*expr.Type{
	"error": expr.ObjectOf(map[string]*expr.Type{"type": expr.String(), "message": expr.String()}),
})

func (d *Definition) typecheck(p *problems, skip map[string]bool) {
	order := d.topoOrder()
	d.computeFacts(order)
	t := &typer{d: d, p: p, skip: skip, values: map[string]*expr.Type{}, types: schemaTypes{}}

	t.input = expr.OpenObject(nil)
	if d.Input != nil {
		t.input = t.types.of(d.Input)
	}

	t.inferSets(order)
	for _, name := range order {
		t.step(d.byName[name])
	}
	failure := t.failure()
	t.finally(failure)
	if d.outputs != nil {
		sc := t.endScope()
		if typ, problems := t.template(d.outputs, sc.env, t.funcTypes(sc)); typ != nil {
			for _, msg := range problems {
				p.add("", "outputs: %s", msg)
			}
			// Check each value against its declared schema the way with:
			// is checked against a kind's params.
			for _, name := range sortedKeys(d.Outputs) {
				if s := d.Outputs[name].Schema; s != nil && typ.Fields[name] != nil {
					var out []string
					assign(typ.Fields[name], s, "outputs."+name, true, &out)
					for _, msg := range out {
						p.add("", "%s", msg)
					}
				}
			}
		}
	}
}

// maxSetPasses bounds inferSets. Types settle in two passes, or three when
// a set step is read around a loop; one that is still changing grows
// without bound.
const maxSetPasses = 8

// inferSets types every set step before anything is checked. A step can
// read a set step that comes later in topological order, around a loop
// (a reads s through b → s → a), so set types are solved to a fixpoint:
// each starts as nil, the value before it ever settles, and set
// expressions are re-typed, with problems discarded, until none changes.
// Only set steps' types depend on other steps, so only they are re-typed.
// A type that keeps growing (s = {v: [s]}) is reported and left unchecked.
func (t *typer) inferSets(order []string) {
	var sets []*Step
	for _, name := range order {
		if s := t.d.byName[name]; s.Kind == KindSet {
			sets = append(sets, s)
		}
	}
	if len(sets) == 0 {
		return
	}
	real := t.p
	defer func() { t.p = real }()
	var changed []string
	for pass := 0; pass < maxSetPasses; pass++ {
		t.p = &problems{}
		changed = nil
		for _, s := range sets {
			old := t.values[s.Name]
			typ := expr.Any
			if !t.skip[s.Name] && s.set != nil {
				sc := t.env(s, false)
				typ, _ = t.template(s.set, sc.env, t.funcTypes(sc))
			}
			t.values[s.Name] = typ
			if old == nil || !reflect.DeepEqual(old, typ) {
				changed = append(changed, s.Name)
			}
		}
		if len(changed) == 0 {
			return
		}
	}
	for _, name := range changed {
		real.add(name, "set: its type does not settle: it depends on its own value through a loop")
		t.values[name] = expr.Any
	}
}

// valueType is what a step's name reads as once it completes. Each call
// returns a new top-level *Type: funcTypes tells steps apart by pointer.
func (t *typer) valueType(s *Step) *expr.Type {
	switch s.Kind {
	case KindJoin, KindSleep:
		return expr.Null()
	case KindSet:
		if v := t.values[s.Name]; v != nil {
			c := *v
			return &c
		}
		return expr.Null() // not yet inferred: see inferSets
	}
	out := expr.Any
	if s.Output != nil {
		c := *t.types.of(s.Output)
		out = &c
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
// {error: ...} on the paths the catch routes to. A reader that only a catch
// leads to sees only the error.
func (t *typer) readType(x, reader *Step) *expr.Type {
	leads := func(n string) bool { return n == reader.Name || t.d.reach[n][reader.Name] }
	v := t.valueType(x)
	caught := false
	for _, c := range x.Catch {
		caught = caught || leads(c.Next)
	}
	if !caught {
		return v
	}
	if x == reader {
		return expr.Join(v, stepErrorValue)
	}
	for _, n := range x.targets() {
		if leads(n) {
			return expr.Join(v, stepErrorValue)
		}
	}
	return stepErrorValue
}

// scope is the typed environment one expression sees.
type scope struct {
	env  map[string]*expr.Type
	fact settled
	// firm are the steps whose value is never nil once settled, so that
	// coalesce over settled alternatives is known not to be nil.
	firm map[string]bool
}

// env builds the expression types a step sees. self adds the step's own
// value, for its edges.
func (t *typer) env(s *Step, self bool) scope {
	fact := t.d.facts[s.Name]
	if self {
		fact = fact.with(s.Name)
	}
	env := map[string]*expr.Type{"input": t.input}
	firm := map[string]bool{}
	for _, x := range t.d.Steps {
		v := t.readType(x, s)
		firm[x.Name] = v.Kind != expr.KindNull && !v.Optional
		switch {
		case fact.has(x.Name):
			env[x.Name] = v
		case t.d.reach[x.Name][s.Name] || (self && x == s):
			env[x.Name] = v.OrNil()
		default:
			env[x.Name] = expr.Null() // never settled here; checkReads reports reads
		}
	}
	return scope{env: env, fact: fact, firm: firm}
}

// endScope is the environment for outputs. They are evaluated over every
// path's scope once all paths have ended, and at least one path ended
// normally at a step that can end a path, so what had settled at every such
// step has settled. A caught step may hold {error: ...}.
func (t *typer) endScope() scope {
	var fact settled
	have := false
	for _, s := range t.d.Steps {
		pf, ok := t.d.facts[s.Name]
		if !ok || !s.mayEnd() {
			continue
		}
		if out := pf.with(s.Name); !have {
			fact, have = out, true
		} else {
			fact = disjoin(fact, out)
		}
	}
	for _, name := range t.d.mustRun() {
		fact = fact.with(name)
	}
	return t.scopeWith(fact)
}

// mustRun lists the steps every completed run has settled. A run that
// completes has settled every step any of its paths passed through, so a
// step must have run unless some way through the graph ends every path
// without it. That is solved per step as a least fixpoint: a path at s can
// avoid x if s is not x and one of the target sets s can route to avoids x
// on every target. The empty set (the path ends) avoids everything.
func (d *Definition) mustRun() []string {
	var out []string
	for _, x := range d.Steps {
		if _, reachable := d.facts[x.Name]; !reachable {
			continue
		}
		avoid := map[string]bool{}
		for changed := true; changed; {
			changed = false
			for _, s := range d.Steps {
				if s == x || avoid[s.Name] {
					continue
				}
				for _, opt := range s.routes() {
					all := true
					for _, n := range opt {
						all = all && avoid[n]
					}
					if all {
						avoid[s.Name], changed = true, true
						break
					}
				}
			}
		}
		if !avoid[d.Steps[0].Name] {
			out = append(out, x.Name)
		}
	}
	return out
}

// routes lists target sets a path can move to from s, enough to decide
// avoidance: for a list with match all, only the edges that are always
// followed, since adding targets never helps avoid a step.
func (s *Step) routes() [][]string {
	var out [][]string
	for _, c := range s.Catch {
		out = append(out, []string{c.Next})
	}
	if s.Switch != nil {
		for _, c := range s.Switch.Cases {
			out = append(out, c.Targets)
		}
		if s.Switch.HasDefault {
			out = append(out, s.Switch.Default)
		}
		return out
	}
	if s.Match == "first" {
		for _, e := range s.Next {
			out = append(out, []string{e.Step})
			if e.When == "" {
				return out
			}
		}
		return append(out, nil)
	}
	var always []string
	for _, e := range s.Next {
		if e.When == "" {
			always = append(always, e.Step)
		}
	}
	return append(out, always)
}

func (t *typer) scopeWith(fact settled) scope {
	env := map[string]*expr.Type{"input": t.input}
	firm := map[string]bool{}
	for _, x := range t.d.Steps {
		v := t.valueType(x)
		if len(x.Catch) > 0 {
			v = expr.Join(v, stepErrorValue)
		}
		firm[x.Name] = v.Kind != expr.KindNull && !v.Optional
		if !fact.has(x.Name) {
			v = v.OrNil()
		}
		env[x.Name] = v
	}
	return scope{env: env, fact: fact, firm: firm}
}

// mayEnd reports whether a path can end at s: no edge need be followed.
func (s *Step) mayEnd() bool {
	if s.Switch != nil {
		for _, c := range s.Switch.Cases {
			if len(c.Targets) == 0 {
				return true
			}
		}
		return s.Switch.HasDefault && len(s.Switch.Default) == 0
	}
	for _, e := range s.Next {
		if e.When == "" {
			return false
		}
	}
	return true
}

// funcTypes types the library's own functions. coalesce is known not to be
// nil when its arguments cover alternatives that one of which has settled.
func (t *typer) funcTypes(sc scope) []expr.CheckOption {
	env, fact := sc.env, sc.fact
	names := map[*expr.Type]string{}
	for name, typ := range env {
		if typ != expr.Any && sc.firm[name] {
			names[typ] = name
		}
	}
	coalesce := func(args []*expr.Type) (*expr.Type, error) {
		var out *expr.Type
		covered := map[string]bool{}
		nonNil := false
		for _, a := range args {
			if a == nil || a.Kind == expr.KindAny && !a.IsUnion() {
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
			case a == nil || a.Kind == expr.KindAny && !a.IsUnion():
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
	sc := t.env(s, false)
	env := sc.env
	opts := t.funcTypes(sc)

	withEnv := env
	if s.each != nil {
		typ, err := s.each.p.Check(env, opts...)
		t.report(s, "each", err)
		elem := expr.Any
		switch {
		case typ.IsUnion():
			t.p.add(s.Name, "each must produce a list, got %s", typ)
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
		opts = t.funcTypes(scope{env: withEnv, fact: sc.fact, firm: sc.firm})
	}
	t.checkWith(s, withEnv, opts)
	if s.set != nil {
		typ, problems := t.template(s.set, env, opts)
		for _, msg := range problems {
			t.p.add(s.Name, "set: %s", msg)
		}
		t.values[s.Name] = typ
	}

	selfScope := t.env(s, true)
	selfEnv := selfScope.env
	selfOpts := t.funcTypes(selfScope)
	for i, prog := range s.edges {
		if prog == nil {
			continue
		}
		typ, err := prog.p.Check(selfEnv, selfOpts...)
		where := fmt.Sprintf("next %s: when", s.Next[i].Step)
		t.report(s, where, err)
		if err == nil && (typ.IsUnion() || typ.Kind != expr.KindAny && (typ.Kind != expr.KindBool || typ.Optional)) {
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

// checkWith types a step's with: and checks it against the kind's params
// schema. A step with no with: dispatches empty params, which must fit
// the schema too.
func (t *typer) checkWith(s *Step, env map[string]*expr.Type, opts []expr.CheckOption) {
	typ := expr.ObjectOf(nil)
	if s.with != nil {
		var problems []string
		typ, problems = t.template(s.with, env, opts)
		for _, msg := range problems {
			t.p.add(s.Name, "with: %s", msg)
		}
	}
	if s.params != nil && typ != nil {
		var out []string
		assign(typ, s.params, "with", true, &out)
		for _, msg := range out {
			t.p.add(s.Name, "%s", msg)
		}
	}
}

func (t *typer) checkSwitch(s *Step, typ *expr.Type) {
	sw := s.Switch
	if typ.IsUnion() {
		t.p.add(s.Name, "next: switch on %s: the value may be %s; switch on one kind", sw.Expr, typ)
		return
	}
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

// switchKey is the case a value routes to. It must encode values exactly
// as Switch.route does (JSON for anything but a string), or Load would
// accept a case that never matches.
func switchKey(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	data, _ := json.Marshal(v)
	return string(data)
}

// failure checks on_failure steps. They see every step as maybe-nil,
// earlier on_failure steps as settled, and error. It returns the type
// each on_failure step settles with.
func (t *typer) failure() map[string]*expr.Type {
	env := t.scopeWith(nil).env
	env["error"] = runErrorType
	types := map[string]*expr.Type{}
	for _, s := range t.d.OnFailure {
		v := t.sequenced(s, env)
		types[s.Name] = v
		env = copyEnv(env)
		env[s.Name] = v
	}
	return types
}

// finally checks finally steps. They run whatever the outcome, so every
// step and on_failure step may be nil. An earlier finally step has
// settled, but may have failed, which a later one reads as {error: ...}.
// They see run, not error.
func (t *typer) finally(failure map[string]*expr.Type) {
	env := t.scopeWith(nil).env
	for name, v := range failure {
		env[name] = v.OrNil()
	}
	env["run"] = runType
	for _, s := range t.d.Finally {
		v := t.sequenced(s, env)
		env = copyEnv(env)
		env[s.Name] = expr.Join(v, stepErrorValue)
	}
}

// sequenced checks an on_failure or finally step in env and returns the
// type of its value.
func (t *typer) sequenced(s *Step, env map[string]*expr.Type) *expr.Type {
	if t.skip[s.Name] {
		// Its reads are already reported; a set value is unchecked.
		if s.set == nil && s.Output != nil {
			return t.types.of(s.Output)
		}
		return expr.Any
	}
	opts := t.funcTypes(scope{env: env})
	t.checkWith(s, env, opts)
	v := expr.Any
	if s.set != nil {
		typ, problems := t.template(s.set, env, opts)
		for _, msg := range problems {
			t.p.add(s.Name, "set: %s", msg)
		}
		v = typ
	} else if s.Output != nil {
		v = t.types.of(s.Output)
	}
	return v
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

// assign reports where a value of type v does not fit schema s. What it
// cannot decide from types (a number that must be an integer, a string
// that must be in an enum) Apply checks at dispatch, as invalid_params.
func assign(v *expr.Type, s *Schema, path string, required bool, out *[]string) {
	s = s.deref()
	if s == nil || v == nil || v.Kind == expr.KindAny && !v.IsUnion() {
		return
	}
	if v.Kind == expr.KindNull {
		if required && !s.nullable() {
			*out = append(*out, fmt.Sprintf("%s: is always nil", path))
		}
		return
	}
	if v.Optional && required && !s.nullable() {
		*out = append(*out, fmt.Sprintf("%s: may be nil, but the schema requires %s", path, s.kinds()))
	}
	if v.IsUnion() {
		// The value may be any variant, so each must fit.
		for _, w := range v.Variants {
			assign(w.NonNil(), s, path, required, out)
		}
		return
	}
	if !s.allowsKind(schemaKind(v)) {
		*out = append(*out, fmt.Sprintf("%s: %s is not %s", path, v.NonNil(), s.kinds()))
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

// allowsKind reports whether some valid value has the JSON type typ, as
// schemaKind names it: the type allows it and, with an enum, some enum
// value has that kind.
func (s *Schema) allowsKind(typ string) bool {
	if !s.allows(typ) {
		return false
	}
	if len(s.Enum) == 0 {
		return true
	}
	for _, e := range s.enumValues() {
		if k := jsonType(e); k == typ || k == "number" && typ == "integer" {
			return true
		}
	}
	return false
}

// kinds describes what the schema allows, for messages.
func (s *Schema) kinds() string {
	if len(s.Enum) > 0 {
		return "one of " + formatEnum(s.Enum)
	}
	return strings.Join(s.Type, " or ")
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
