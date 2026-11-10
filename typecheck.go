package workflow

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	risor "github.com/deepnoodle-ai/risor/v3"
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
// is certain.
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
	slices.SortFunc(f, func(a, b []string) int {
		return cmp.Or(cmp.Compare(len(a), len(b)), slices.Compare(a, b))
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
		if !slices.Contains(b, x) {
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
			if !slices.Contains(preds[m], s.Name) {
				preds[m] = append(preds[m], s.Name)
			}
		}
	}
	for _, ps := range preds {
		slices.Sort(ps)
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
	d      *Definition
	p      *problems
	skip   map[string]bool // steps whose reads already failed checkReads
	values map[string]risor.Type
	types  schemaTypes
	input  risor.Type
}

// Types are Risor's own, built with its structural type API. A type past
// the checker's limits is invalid, and the declaration that would use it
// reports why; Load bounds its schemas well below them.

// record is an exact record of required fields.
func record(fields map[string]risor.Type) risor.Type {
	fs := make([]risor.FieldType, 0, len(fields))
	for _, k := range sortedKeys(fields) {
		fs = append(fs, risor.FieldType{Key: k, Type: fields[k]})
	}
	return risor.ExactRecordType(fs...)
}

func isAny(t risor.Type) bool   { return t.Kind() == risor.KindAny }
func isUnion(t risor.Type) bool { return !isAny(t) && len(t.NonNull().Alternatives()) > 1 }

var errorShape = record(map[string]risor.Type{"type": risor.TypeString, "message": risor.TypeString})

var runErrorType = record(map[string]risor.Type{
	"step": risor.TypeString, "step_id": risor.TypeString,
	"type": risor.TypeString, "message": risor.TypeString,
})

// runType is the run global of finally steps: the outcome, and the run's
// error when it failed.
var runType = record(map[string]risor.Type{
	"status": risor.LiteralType(
		string(StatusCancelled), string(StatusCompleted), string(StatusFailed)),
	"error": risor.Nullable(runErrorType),
})

var stepErrorValue = record(map[string]risor.Type{"error": errorShape})

func (d *Definition) typecheck(p *problems, skip map[string]bool) {
	order := d.topoOrder()
	d.computeFacts(order)
	t := &typer{
		d:      d,
		p:      p,
		skip:   skip,
		values: map[string]risor.Type{},
		types:  schemaTypes{},
	}

	t.input = risor.MapType(risor.TypeAny)
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
		if typ, problems := t.template(d.outputs, sc.env, sc.notNull, false); true {
			for _, msg := range problems {
				p.add("", "outputs: %s", msg)
			}
			// Check each value against its declared schema the way with:
			// is checked against a kind's params.
			for _, name := range sortedKeys(d.Outputs) {
				if f, ok := typ.Field(name); ok && d.Outputs[name].Schema != nil {
					var out []string
					assign(f.Type, d.Outputs[name].Schema, "outputs."+name, true, &out)
					for _, msg := range out {
						p.add("", "%s", msg)
					}
				}
			}
			t.checkRequiredOutputs(typ)
		}
	}
	// Without outputs:, a run's outputs are whichever steps its paths end
	// at, which is no contract to check: a caller's schema needs declared
	// outputs.
	if req := d.requiredOutputs.deref(); d.outputs == nil && req != nil {
		what := "outputs that fit its schema"
		if len(req.Required) > 0 {
			what = strings.Join(req.Required, ", ")
		}
		p.add("", "outputs: the caller requires %s; declare outputs", what)
	}
}

// checkRequiredOutputs checks the outputs against the schema from
// RequireOutputs. typ is the outputs' type by value; an output with a
// schema has a value that fits it once the run completes, so its schema is
// its type.
func (t *typer) checkRequiredOutputs(byValue risor.Type) {
	if t.d.requiredOutputs == nil {
		return
	}

	fields := map[string]risor.Type{}
	for _, f := range byValue.Fields() {
		ft := f.Type
		if s := t.d.Outputs[f.Key].Schema; s != nil {
			ft = t.types.of(s)
		}
		fields[f.Key] = ft
	}

	var out []string
	assign(record(fields), t.d.requiredOutputs, "outputs", true, &out)
	for _, msg := range out {
		t.p.add("", "%s (required by the caller)", msg)
	}
}

// maxSetPasses bounds inferSets. Types settle in two passes, or three when
// a set or expr step is read around a loop; one that is still changing grows
// without bound.
const maxSetPasses = 8

// inferSets types every set and expr step before anything is checked. A
// step can read one that comes later in topological order, around a loop
// (a reads s through b → s → a), so their types are solved to a fixpoint:
// each starts as null, the value before it ever settles, and their
// expressions are re-typed, with problems discarded, until none changes.
// Only these steps' types depend on other steps, so only they are re-typed.
// A type that keeps growing (s = {v: [s]}) is reported and left unchecked.
func (t *typer) inferSets(order []string) {
	var sets []*Step
	for _, name := range order {
		if s := t.d.byName[name]; s.computes() {
			sets = append(sets, s)
		}
	}
	if len(sets) == 0 {
		return
	}

	real := t.p
	defer func() { t.p = real }()
	var changed []string
	for range maxSetPasses {
		t.p = &problems{}
		changed = nil
		for _, s := range sets {
			old, seen := t.values[s.Name]
			typ := risor.TypeAny
			if !t.skip[s.Name] && s.value != nil {
				sc := t.env(s, false)
				typ, _ = t.template(s.value, sc.env, sc.notNull, false)
			}
			t.values[s.Name] = typ
			if !seen || !old.Equal(typ) {
				changed = append(changed, s.Name)
			}
		}
		if len(changed) == 0 {
			return
		}
	}

	for _, name := range changed {
		real.add(name, "%s: its type does not settle: it depends on its own value through a loop",
			t.d.byName[name].Kind)
		t.values[name] = risor.TypeAny
	}
}

// valueType is what a step's name reads as once it completes.
func (t *typer) valueType(s *Step) risor.Type {
	switch s.Kind {
	case KindJoin, KindSleep:
		return risor.TypeNull
	case KindSet, KindExpr:
		if v, ok := t.values[s.Name]; ok {
			return v
		}
		return risor.TypeNull // not yet inferred: see inferSets
	}
	out := risor.TypeAny
	if s.Output != nil {
		out = t.types.of(s.Output)
	}
	if s.Each != "" {
		if s.ContinueOnError {
			out = risor.Join(out, stepErrorValue)
		}
		return risor.ListType(out)
	}
	return out
}

// readType is what reader sees for step x: a caught failure reads as
// {error: ...} on the paths the catch routes to. A reader that only a catch
// leads to sees only the error.
func (t *typer) readType(x, reader *Step) risor.Type {
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
		return risor.Join(v, stepErrorValue)
	}
	if slices.ContainsFunc(x.targets(), leads) {
		return risor.Join(v, stepErrorValue)
	}
	return stepErrorValue
}

// scope is the typed environment one expression sees.
type scope struct {
	env  map[string]risor.Type
	fact settled
	// notNull are the clauses of fact whose every step settles with a
	// value that is never null: one of them is not null when it is read.
	// Each becomes a Risor host fact (someNotNull).
	notNull settled
}

// notNull keeps the clauses of fact of two steps or more whose every step
// is firm: a step that settled reads as a value that is never null. A
// clause with a step that may settle as null (a join, a nullable output,
// an untyped step) says nothing about null.
func notNull(fact settled, firm map[string]bool) settled {
	var out settled
	for _, c := range fact {
		ok := len(c) > 1
		for _, n := range c {
			ok = ok && firm[n]
		}
		if ok {
			out = append(out, c)
		}
	}
	return out
}

// firmType reports whether a settled step's value is never null.
func firmType(v risor.Type) bool { return !v.IsNullable() && v.Kind() != risor.KindNever }

// env builds the expression types a step sees. self adds the step's own
// value, for its edges.
func (t *typer) env(s *Step, self bool) scope {
	fact := t.d.facts[s.Name]
	if self {
		fact = fact.with(s.Name)
	}
	env := map[string]risor.Type{"input": t.input}
	firm := map[string]bool{}
	for _, x := range t.d.Steps {
		v := t.readType(x, s)
		if self && x == s {
			// Edges run only after the step completes: a caught failure
			// goes to its catch target instead, so here the step is never
			// {error: ...}.
			v = t.valueType(s)
		}
		firm[x.Name] = firmType(v)
		switch {
		case fact.has(x.Name):
			env[x.Name] = v
		case t.d.reach[x.Name][s.Name] || (self && x == s):
			env[x.Name] = risor.Nullable(v)
		default:
			env[x.Name] = risor.TypeNull // never settled here; checkReads reports reads
		}
	}
	return scope{env: env, fact: fact, notNull: notNull(fact, firm)}
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
	env := map[string]risor.Type{"input": t.input}
	firm := map[string]bool{}
	for _, x := range t.d.Steps {
		v := t.valueType(x)
		if len(x.Catch) > 0 {
			v = risor.Join(v, stepErrorValue)
		}
		firm[x.Name] = firmType(v)
		if !fact.has(x.Name) {
			v = risor.Nullable(v)
		}
		env[x.Name] = v
	}
	return scope{env: env, fact: fact, notNull: notNull(fact, firm)}
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

// check types one program in env with Risor's checker and compiles it in
// the same pass. The program's names are declared in a child of the base
// environment, each with its type. edge allows decide and choose, which
// only a step's next block may use. It returns the result type, or the
// problems. A program that checks keeps the compiled program: the names it
// reads are the ones Apply binds.
func check(prog *program, env map[string]risor.Type, facts settled, edge bool) (risor.Type, []string) {
	if !edge {
		if calls := prog.x.Calls(fnDecide, fnChoose); len(calls) > 0 {
			return risor.TypeAny, []string{calls[0].Name + " can only be used in next: when or switch"}
		}
	}
	base := baseEnv
	if edge {
		base = edgeEnv
	}
	var decls []risor.Decl
	read := map[string]bool{}
	for _, name := range prog.names() {
		if typ, ok := env[name]; ok {
			decls = append(decls, risor.VarType(name, typ))
			read[name] = true
		}
	}
	// A host fact helps only a program that reads every step it names, and
	// may name only variables the environment declares.
	for _, c := range facts {
		all := true
		for _, n := range c {
			all = all && read[n]
		}
		if all {
			decls = append(decls, risor.SomeNotNull(c...))
		}
	}
	renv, err := base.With(decls...)
	if err != nil {
		return risor.TypeAny, []string{err.Error()}
	}
	compiled, res, err := renv.CheckCompile(prog.x)
	if err != nil {
		var re *risor.Error
		if !errors.As(err, &re) {
			return risor.TypeAny, []string{err.Error()}
		}
		refs := prog.x.Refs()
		msgs := make([]string, len(re.Diagnostics))
		for i, d := range re.Diagnostics {
			msgs[i] = checkText(prog.src, d, env, refs)
		}
		return risor.TypeAny, msgs
	}
	prog.p = compiled
	return res.ResultType, nil
}

// isBool reports whether t is exactly bool: never null, one kind.
func isBool(t risor.Type) bool { return t.Kind() == risor.KindBool }

func (t *typer) step(s *Step) {
	if t.skip[s.Name] {
		if s.computes() {
			t.values[s.Name] = risor.TypeAny
		}
		return
	}
	sc := t.env(s, false)
	env := sc.env

	withEnv := env
	if s.each != nil {
		typ, problems := check(s.each, env, sc.notNull, false)
		t.report(s, "each", problems)
		elem := risor.TypeAny
		switch k := typ.NonNull().Kind(); {
		case len(problems) > 0, k == risor.KindAny, k == risor.KindNever:
		case k == risor.KindList:
			elem, _ = typ.Elem()
		default:
			t.p.add(s.Name, "each must produce a list, got %s", typ)
		}
		withEnv = copyEnv(env)
		withEnv["item"], withEnv["index"] = elem, risor.TypeInt
	}
	t.checkWith(s, withEnv, sc.notNull)
	if s.value != nil {
		typ, problems := t.template(s.value, env, sc.notNull, false)
		for _, msg := range problems {
			t.p.add(s.Name, "%s: %s", s.Kind, msg)
		}
		t.values[s.Name] = typ
	}

	selfScope := t.env(s, true)
	selfEnv := selfScope.env
	failed := map[string]bool{} // edges whose expression has a problem
	for i, prog := range s.edges {
		if prog == nil {
			continue
		}
		typ, problems := check(prog, selfEnv, selfScope.notNull, true)
		where := fmt.Sprintf("next %s: when", s.Next[i].Step)
		t.report(s, where, problems)
		failed[where] = len(problems) > 0
		switch {
		case len(problems) > 0:
		case (typ.Kind() == risor.KindFloat || typ.Kind() == risor.KindInt) &&
			len(prog.x.Calls(fnDecide)) > 0:
			t.p.add(s.Name, "%s: decide returns the probability of yes (0 to 1); compare it, "+
				"for example decide(...) > 0.8", where)
		case !isAny(typ) && !isBool(typ):
			t.p.add(s.Name, "%s must be a boolean, got %s", where, typ)
		}
	}
	if s.swtch != nil {
		typ, problems := check(s.swtch, selfEnv, selfScope.notNull, true)
		t.report(s, "next: switch", problems)
		failed["next: switch"] = len(problems) > 0
		if len(problems) == 0 {
			t.checkSwitch(s, typ)
		}
	}
	t.checkSubjects(s, selfEnv, selfScope.notNull, failed)
}

// checkSubjects types each decision's subject alone. Every decision in a
// next block is asked before routing, so a guard in the condition, such
// as `x != null && decide("q", x.body) > 0.5`, does not protect it: the run
// evaluates the subject without it. An edge whose whole expression has a
// problem is reported once, as that problem.
func (t *typer) checkSubjects(s *Step, env map[string]risor.Type, facts settled, failed map[string]bool) {
	for _, c := range s.decisions {
		if failed[c.where] {
			continue
		}
		_, problems := check(c.subject, env, facts, true)
		for _, msg := range problems {
			t.p.add(s.Name, "%s: %s(%q, ...): subject: %s; every decision is asked before "+
				"routing, so a guard in the condition does not protect its subject: use "+
				"optional access (intake.body?.text) or pass the whole value",
				c.where, c.fn, c.question, msg)
		}
	}
}

// checkWith types a step's with: and checks it against the kind's params
// schema. A step with no with: dispatches empty params, which must fit
// the schema too.
func (t *typer) checkWith(s *Step, env map[string]risor.Type, facts settled) {
	typ := record(nil)
	if s.with != nil {
		var problems []string
		typ, problems = t.template(s.with, env, facts, false)
		for _, msg := range problems {
			t.p.add(s.Name, "with: %s", msg)
		}
	}
	if s.params != nil {
		var out []string
		assign(typ, s.params, "with", true, &out)
		for _, msg := range out {
			t.p.add(s.Name, "%s", msg)
		}
	}
}

func (t *typer) checkSwitch(s *Step, typ risor.Type) {
	sw := s.Switch
	if isUnion(typ) {
		t.p.add(s.Name, "next: switch on %s: the value may be %s; switch on one kind", sw.Expr, typ)
		return
	}
	kind := typ.NonNull().Kind()
	switch kind {
	case risor.KindAny:
		if !sw.HasDefault {
			t.p.add(s.Name, "next: switch on %s needs a default: its type is unknown", sw.Expr)
		}
		return
	case risor.KindString, risor.KindInt, risor.KindFloat, risor.KindBool:
	default:
		t.p.add(s.Name, "next: switch on %s: cannot switch on %s", sw.Expr, typ)
		return
	}
	if typ.IsNullable() {
		t.p.add(s.Name, "next: switch on %s: the value may be null; use ?? or switch on a value that is always set", sw.Expr)
	}

	// Risor's literal types are strings only: a number has no fixed set of
	// values, even one read from an enum.
	var values []any
	switch kind {
	case risor.KindBool:
		values = []any{false, true}
	case risor.KindString:
		for _, l := range typ.Literals() {
			values = append(values, l)
		}
	}
	if len(values) == 0 {
		if !sw.HasDefault {
			t.p.add(s.Name, "next: switch on %s needs a default: %s has no fixed set of values",
				sw.Expr, typ)
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
			t.p.add(s.Name, "next: case %q: %s is never %q (it is one of %s)",
				c.Value, sw.Expr, c.Value, strings.Join(sortedKeys(keys), ", "))
		}
		handled[c.Value] = true
	}

	var missing []string
	for k := range keys {
		if !handled[k] {
			missing = append(missing, k)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 && !sw.HasDefault {
		t.p.add(s.Name, "next: switch on %s does not handle %s; add cases or a default",
			sw.Expr, strings.Join(missing, ", "))
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

// failure checks on_failure steps. They see every step as maybe-null,
// earlier on_failure steps as settled, and error. It returns the type
// each on_failure step settles with.
func (t *typer) failure() map[string]risor.Type {
	env := t.scopeWith(nil).env
	env["error"] = runErrorType
	types := map[string]risor.Type{}
	for _, s := range t.d.OnFailure {
		v := t.sequenced(s, env)
		types[s.Name] = v
		env = copyEnv(env)
		env[s.Name] = v
	}
	return types
}

// finally checks finally steps. They run whatever the outcome, so every
// step and on_failure step may be null. An earlier finally step has
// settled, but may have failed, which a later one reads as {error: ...}.
// They see run, not error.
func (t *typer) finally(failure map[string]risor.Type) {
	env := t.scopeWith(nil).env
	for name, v := range failure {
		env[name] = risor.Nullable(v)
	}
	env["run"] = runType
	for _, s := range t.d.Finally {
		v := t.sequenced(s, env)
		env = copyEnv(env)
		env[s.Name] = risor.Join(v, stepErrorValue)
	}
}

// sequenced checks an on_failure or finally step in env and returns the
// type of its value.
func (t *typer) sequenced(s *Step, env map[string]risor.Type) risor.Type {
	if t.skip[s.Name] {
		// Its reads are already reported; a set or expr value is unchecked.
		if s.value == nil && s.Output != nil {
			return t.types.of(s.Output)
		}
		return risor.TypeAny
	}
	t.checkWith(s, env, nil)
	v := risor.TypeAny
	if s.value != nil {
		typ, problems := t.template(s.value, env, nil, false)
		for _, msg := range problems {
			t.p.add(s.Name, "%s: %s", s.Kind, msg)
		}
		v = typ
	} else if s.Output != nil {
		v = t.types.of(s.Output)
	}
	return v
}

func (t *typer) report(s *Step, what string, problems []string) {
	for _, msg := range problems {
		t.p.add(s.Name, "%s: %s", what, msg)
	}
}

// template types a with:/set:/outputs: value.
func (t *typer) template(
	tm *template, env map[string]risor.Type, facts settled, edge bool,
) (risor.Type, []string) {
	var problems []string
	var walk func(*template) risor.Type
	walk = func(tm *template) risor.Type {
		switch tm.kind {
		case tmplSingle:
			typ, msgs := check(tm.prog, env, facts, edge)
			problems = append(problems, msgs...)
			return typ
		case tmplText:
			_, msgs := check(tm.prog, env, facts, edge)
			problems = append(problems, msgs...)
			return risor.TypeString
		case tmplObject:
			fields := make(map[string]risor.Type, len(tm.obj))
			for _, k := range tm.keys {
				fields[k] = walk(tm.obj[k])
			}
			return record(fields)
		case tmplList:
			elem := risor.TypeNever
			for _, c := range tm.list {
				elem = risor.Join(elem, walk(c))
			}
			return risor.ListType(elem)
		default:
			return literalType(tm.lit)
		}
	}
	return walk(tm), problems
}

// literalType is the type of a JSON value as Risor types the same literal
// (F1 3.4): a string is its literal set, a number is int when it is whole
// (and at most 2^53 - 1), and the empty list is never[].
func literalType(v any) risor.Type {
	switch x := v.(type) {
	case nil:
		return risor.TypeNull
	case string:
		return risor.LiteralType(x)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) <= 1<<53-1 {
			return risor.TypeInt
		}
		return risor.TypeFloat
	case bool:
		return risor.TypeBool
	case map[string]any:
		fields := make(map[string]risor.Type, len(x))
		for k, fv := range x {
			fields[k] = literalType(fv)
		}
		return record(fields)
	case []any:
		elem := risor.TypeNever
		for _, item := range x {
			elem = risor.Join(elem, literalType(item))
		}
		return risor.ListType(elem)
	}
	return risor.TypeAny
}

func copyEnv(env map[string]risor.Type) map[string]risor.Type {
	out := make(map[string]risor.Type, len(env)+2)
	maps.Copy(out, env)
	return out
}

// assign reports where a value of type v does not fit schema s. What it
// cannot decide from types (a number that must be an integer, a string
// that must be in an enum) Apply checks at dispatch, as invalid_params.
func assign(v risor.Type, s *Schema, path string, required bool, out *[]string) {
	s = s.deref()
	if s == nil || isAny(v) || v.Kind() == risor.KindNever {
		return
	}

	if v.Kind() == risor.KindNull {
		if required && !s.nullable() {
			*out = append(*out, fmt.Sprintf("%s: is always nil", path))
		}
		return
	}
	if v.IsNullable() && required && !s.nullable() {
		msg := fmt.Sprintf("%s: may be nil, but the schema requires %s", path, s.kinds())
		*out = append(*out, msg)
	}
	v = v.NonNull()
	if isUnion(v) {
		// The value may be any variant, so each must fit.
		for _, w := range v.NonNull().Alternatives() {
			assign(w, s, path, required, out)
		}
		return
	}
	if !s.allowsKind(schemaKind(v)) {
		*out = append(*out, fmt.Sprintf("%s: %s is not %s", path, v, s.kinds()))
		return
	}

	if len(s.Enum) > 0 {
		for _, ev := range v.Literals() {
			found := false
			for _, se := range s.Enum {
				if jsonEqual(ev, se) {
					found = true
				}
			}
			if !found {
				*out = append(*out, fmt.Sprintf("%s: %s is not one of %s",
					path, describe(ev), formatEnum(s.Enum)))
			}
		}
	}

	switch v.Kind() {
	case risor.KindRecord, risor.KindUnion:
		assignObject(v, s, path, out)
	case risor.KindList:
		elem, _ := v.Elem()
		assign(elem, s.Items, path+"[]", true, out)
	}
}

// assignObject is assign for a record. A record whose rest is typed
// (additionalProperties: a schema) may have any other key, of the rest's
// type, or lack it; one whose rest is any is checked only for the fields
// it lists; an exact record has only its fields.
func assignObject(v risor.Type, s *Schema, path string, out *[]string) {
	elem, _ := v.Rest()
	open := !v.IsExact()
	typed := open && !isAny(elem)

	// Every declared property, and every required name, declared or not.
	names := sortedKeys(s.Properties)
	for _, name := range s.Required {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)

	for _, name := range names {
		ps := s.Properties[name]
		required := s.isRequired(name)
		f, ok := v.Field(name)
		switch {
		case !ok && typed:
			if required {
				*out = append(*out, fmt.Sprintf("%s: may lack required %q", path, name))
			} else if ps != nil {
				assign(elem, ps, path+"."+name, false, out)
			}
			continue
		case !ok:
			if required && !open {
				*out = append(*out, fmt.Sprintf("%s: missing required %q", path, name))
			}
			continue
		case required && f.Optional:
			*out = append(*out, fmt.Sprintf("%s: required %q may be absent", path, name))
			if ps == nil {
				continue // one message for an undeclared name
			}
		}
		if ps != nil {
			assign(f.Type, ps, path+"."+name, required, out)
			continue
		}
		// A required name that is not declared must not be null; its
		// type is checked against additionalProperties below.
		if f.Type.IsNullable() && !isAny(f.Type) && (s.Additional == nil || s.Additional.nullable()) {
			*out = append(*out, fmt.Sprintf("%s.%s: may be nil, but it is required", path, name))
		}
	}

	for _, f := range v.Fields() {
		if _, ok := s.Properties[f.Key]; ok {
			continue
		}
		switch {
		case s.Additional != nil:
			assign(f.Type, s.Additional, path+"."+f.Key, true, out)
		case s.AdditionalSet:
			*out = append(*out, fmt.Sprintf("%s: unexpected %q", path, f.Key))
		}
	}

	if !typed {
		return
	}
	switch {
	case s.Additional != nil:
		assign(elem, s.Additional, path+".*", true, out)
	case s.AdditionalSet:
		*out = append(*out, fmt.Sprintf("%s: may have properties the schema does not allow", path))
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

// schemaKind names the JSON type of a type of one kind.
func schemaKind(v risor.Type) string {
	switch v.Kind() {
	case risor.KindBool:
		return "boolean"
	case risor.KindInt, risor.KindFloat:
		// A number fits number and integer; Apply checks the value. An int
		// is always an integer, and a float may be one.
		return "integer"
	case risor.KindString:
		return "string"
	case risor.KindList:
		return "array"
	case risor.KindRecord, risor.KindUnion:
		return "object"
	}
	return "null"
}

// checkText is diagText with a suggestion for a misspelled field: the
// engine finds the read the diagnostic belongs to (from Risor's analysis of
// the program's reads) and the record's fields from its own types.
func checkText(src string, d risor.Diagnostic, env map[string]risor.Type, refs []risor.Ref) string {
	msg := diagText(src, d)
	if d.Span == nil || d.Code != "no-field" {
		return msg
	}
	at := spanText(src, *d.Span)
	for _, r := range refs {
		for k, step := range r.Path {
			if step.Key != at {
				continue
			}
			recv, ok := refType(env, r, k)
			if k := recv.NonNull().Kind(); !ok || k != risor.KindRecord && k != risor.KindUnion {
				continue
			}
			if _, ok := recv.Field(at); ok {
				continue
			}
			var keys []string
			for _, f := range recv.Fields() {
				keys = append(keys, f.Key)
			}
			read := r.Name
			for _, p := range r.Path[:k+1] {
				read += "." + p.Key
			}
			msg = "`" + read + "`: " + d.Message
			if near := nearest(at, keys); near != "" {
				msg += fmt.Sprintf(" (did you mean %q?)", near)
			}
			return msg
		}
	}
	return msg
}

// refType is the type of the read r after its first n steps, or false
// when the types do not say.
func refType(env map[string]risor.Type, r risor.Ref, n int) (risor.Type, bool) {
	t, ok := env[r.Name]
	if !ok {
		return risor.Type{}, false
	}
	for _, step := range r.Path[:n] {
		t = t.NonNull()
		if isAny(t) {
			return t, true
		}
		if k := t.Kind(); k != risor.KindRecord && k != risor.KindUnion {
			return risor.Type{}, false
		}
		if f, ok := t.Field(step.Key); ok {
			t = f.Type
		} else if rest, _ := t.Rest(); !t.IsExact() {
			t = rest
		} else {
			return risor.Type{}, false
		}
	}
	return t, true
}

// nearest is the name closest to s by edit distance, if it is close.
func nearest(s string, names []string) string {
	best, bestD := "", 3
	for _, n := range names {
		if d := editDistance(s, n); d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
