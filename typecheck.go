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
	values map[string]*typ
	types  schemaTypes
	input  *typ
}

var runErrorType = mustParseType("{message: string, step: string, step_id: string, type: string}")

// runType is the run global of finally steps: the outcome, and the run's
// error when it failed.
var runType = tRecord(map[string]*typ{
	"status": tLit(string(StatusCancelled), string(StatusCompleted), string(StatusFailed)),
	"error":  runErrorType.orNull(),
})

var stepErrorValue = mustParseType("{error: {message: string, type: string}}")

func (d *Definition) typecheck(p *problems, skip map[string]bool) {
	order := d.topoOrder()
	d.computeFacts(order)
	t := &typer{
		d:      d,
		p:      p,
		skip:   skip,
		values: map[string]*typ{},
		types:  schemaTypes{},
	}

	t.input = mustParseType("map<any>")
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
		if typ, problems := t.template(d.outputs, sc.env, false); typ != nil {
			for _, msg := range problems {
				p.add("", "outputs: %s", msg)
			}
			// Check each value against its declared schema the way with:
			// is checked against a kind's params.
			for _, name := range sortedKeys(d.Outputs) {
				if s := d.Outputs[name].Schema; s != nil && typ.rec.fields[name].t != nil {
					var out []string
					assign(typ.rec.fields[name].t, s, "outputs."+name, true, &out)
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
func (t *typer) checkRequiredOutputs(byValue *typ) {
	if t.d.requiredOutputs == nil {
		return
	}

	fields := make(map[string]*typ, len(byValue.rec.fields))
	for name, f := range byValue.rec.fields {
		ft := f.t
		if s := t.d.Outputs[name].Schema; s != nil {
			ft = t.types.of(s)
		}
		fields[name] = ft
	}

	var out []string
	assign(tRecord(fields), t.d.requiredOutputs, "outputs", true, &out)
	for _, msg := range out {
		t.p.add("", "%s (required by the caller)", msg)
	}
}

// maxSetPasses bounds inferSets. Types settle in two passes, or three when
// a set step is read around a loop; one that is still changing grows
// without bound.
const maxSetPasses = 8

// inferSets types every set step before anything is checked. A step can
// read a set step that comes later in topological order, around a loop
// (a reads s through b → s → a), so set types are solved to a fixpoint:
// each starts as null, the value before it ever settles, and set
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
	for range maxSetPasses {
		t.p = &problems{}
		changed = nil
		for _, s := range sets {
			old := t.values[s.Name]
			typ := tAny
			if !t.skip[s.Name] && s.set != nil {
				sc := t.env(s, false)
				typ, _ = t.template(s.set, sc.env, false)
			}
			t.values[s.Name] = typ
			if old == nil || !sameType(old, typ) {
				changed = append(changed, s.Name)
			}
		}
		if len(changed) == 0 {
			return
		}
	}

	for _, name := range changed {
		real.add(name, "set: its type does not settle: it depends on its own value through a loop")
		t.values[name] = tAny
	}
}

// valueType is what a step's name reads as once it completes.
func (t *typer) valueType(s *Step) *typ {
	switch s.Kind {
	case KindJoin, KindSleep:
		return tNull()
	case KindSet:
		if v := t.values[s.Name]; v != nil {
			return v
		}
		return tNull() // not yet inferred: see inferSets
	}
	out := tAny
	if s.Output != nil {
		out = t.types.of(s.Output)
	}
	if s.Each != "" {
		if s.ContinueOnError {
			out = joinTypes(out, stepErrorValue)
		}
		return tList(out)
	}
	return out
}

// readType is what reader sees for step x: a caught failure reads as
// {error: ...} on the paths the catch routes to. A reader that only a catch
// leads to sees only the error.
func (t *typer) readType(x, reader *Step) *typ {
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
		return joinTypes(v, stepErrorValue)
	}
	if slices.ContainsFunc(x.targets(), leads) {
		return joinTypes(v, stepErrorValue)
	}
	return stepErrorValue
}

// scope is the typed environment one expression sees.
type scope struct {
	env  map[string]*typ
	fact settled
}

// env builds the expression types a step sees. self adds the step's own
// value, for its edges.
func (t *typer) env(s *Step, self bool) scope {
	fact := t.d.facts[s.Name]
	if self {
		fact = fact.with(s.Name)
	}
	env := map[string]*typ{"input": t.input}
	for _, x := range t.d.Steps {
		v := t.readType(x, s)
		if self && x == s {
			// Edges run only after the step completes: a caught failure
			// goes to its catch target instead, so here the step is never
			// {error: ...}.
			v = t.valueType(s)
		}
		switch {
		case fact.has(x.Name):
			env[x.Name] = v
		case t.d.reach[x.Name][s.Name] || (self && x == s):
			env[x.Name] = v.orNull()
		default:
			env[x.Name] = tNull() // never settled here; checkReads reports reads
		}
	}
	return scope{env: env, fact: fact}
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
	env := map[string]*typ{"input": t.input}
	for _, x := range t.d.Steps {
		v := t.valueType(x)
		if len(x.Catch) > 0 {
			v = joinTypes(v, stepErrorValue)
		}
		if !fact.has(x.Name) {
			v = v.orNull()
		}
		env[x.Name] = v
	}
	return scope{env: env, fact: fact}
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

// check types one program in env with Risor's checker. The program's
// names are declared with their types written as type text, and the result
// type is read back from text. edge allows decide and choose, which only a
// step's next block may use. It returns the result type, or nil and the
// problems.
func check(prog *program, env map[string]*typ, edge bool) (*typ, []string) {
	if !edge {
		if calls := prog.x.Calls(fnDecide, fnChoose); len(calls) > 0 {
			return nil, []string{calls[0].Name + " can only be used in next: when or switch"}
		}
	}
	base := baseEnv
	if edge {
		base = edgeEnv
	}
	var decls []risor.Decl
	for _, name := range prog.names() {
		if typ, ok := env[name]; ok {
			decls = append(decls, risor.Var(name, typ.String()))
		}
	}
	renv, err := base.With(decls...)
	if err != nil {
		return nil, []string{err.Error()}
	}
	res, err := renv.Check(prog.x)
	if err != nil {
		var re *risor.Error
		if !errors.As(err, &re) {
			return nil, []string{err.Error()}
		}
		refs := prog.x.Refs()
		msgs := make([]string, len(re.Diagnostics))
		for i, d := range re.Diagnostics {
			msgs[i] = checkText(prog.src, d, env, refs)
		}
		return nil, msgs
	}
	typ, err := parseType(res.Type)
	if err != nil {
		return nil, []string{err.Error()}
	}
	return typ, nil
}

// isBool reports whether t is exactly bool: never null, one kind.
func isBool(t *typ) bool { return !t.any && t.bool_ && t.alts() == 1 && !t.null }

func (t *typer) step(s *Step) {
	if t.skip[s.Name] {
		if s.Kind == KindSet {
			t.values[s.Name] = tAny
		}
		return
	}
	sc := t.env(s, false)
	env := sc.env

	withEnv := env
	if s.each != nil {
		typ, problems := check(s.each, env, false)
		t.report(s, "each", problems)
		elem := tAny
		if typ != nil && !typ.any && !typ.isNever() && !typ.isNull() {
			switch base := typ.nonNull(); {
			case base.union() || base.list == nil:
				t.p.add(s.Name, "each must produce a list, got %s", typ)
			default:
				elem = base.list
			}
		}
		withEnv = copyEnv(env)
		withEnv["item"], withEnv["index"] = elem, tInt()
	}
	t.checkWith(s, withEnv)
	if s.set != nil {
		typ, problems := t.template(s.set, env, false)
		for _, msg := range problems {
			t.p.add(s.Name, "set: %s", msg)
		}
		t.values[s.Name] = typ
	}

	selfEnv := t.env(s, true).env
	failed := map[string]bool{} // edges whose expression has a problem
	for i, prog := range s.edges {
		if prog == nil {
			continue
		}
		typ, problems := check(prog, selfEnv, true)
		where := fmt.Sprintf("next %s: when", s.Next[i].Step)
		t.report(s, where, problems)
		failed[where] = len(problems) > 0
		switch {
		case len(problems) > 0:
		case typ.num != numNone && typ.alts() == 1 && !typ.null &&
			len(prog.x.Calls(fnDecide)) > 0:
			t.p.add(s.Name, "%s: decide returns the probability of yes (0 to 1); compare it, "+
				"for example decide(...) > 0.8", where)
		case !typ.any && !isBool(typ):
			t.p.add(s.Name, "%s must be a boolean, got %s", where, typ)
		}
	}
	if s.swtch != nil {
		typ, problems := check(s.swtch, selfEnv, true)
		t.report(s, "next: switch", problems)
		failed["next: switch"] = len(problems) > 0
		if len(problems) == 0 {
			t.checkSwitch(s, typ)
		}
	}
	t.checkSubjects(s, selfEnv, failed)
}

// checkSubjects types each decision's subject alone. Every decision in a
// next block is asked before routing, so a guard in the condition, such
// as `x != null && decide("q", x.body) > 0.5`, does not protect it: the run
// evaluates the subject without it. An edge whose whole expression has a
// problem is reported once, as that problem.
func (t *typer) checkSubjects(s *Step, env map[string]*typ, failed map[string]bool) {
	for _, c := range s.decisions {
		if failed[c.where] {
			continue
		}
		_, problems := check(c.subject, env, true)
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
func (t *typer) checkWith(s *Step, env map[string]*typ) {
	typ := tRecord(nil)
	if s.with != nil {
		var problems []string
		typ, problems = t.template(s.with, env, false)
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

func (t *typer) checkSwitch(s *Step, typ *typ) {
	sw := s.Switch
	if typ.union() {
		t.p.add(s.Name, "next: switch on %s: the value may be %s; switch on one kind", sw.Expr, typ)
		return
	}
	base := typ.nonNull()
	switch {
	case typ.any:
		if !sw.HasDefault {
			t.p.add(s.Name, "next: switch on %s needs a default: its type is unknown", sw.Expr)
		}
		return
	case base.str || base.num != numNone || base.bool_:
	default:
		t.p.add(s.Name, "next: switch on %s: cannot switch on %s", sw.Expr, typ)
		return
	}
	if typ.null {
		t.p.add(s.Name, "next: switch on %s: the value may be null; use ?? or switch on a value that is always set", sw.Expr)
	}

	// Risor's literal types are strings only: a number has no fixed set of
	// values, even one read from an enum.
	var values []any
	switch {
	case base.bool_:
		values = []any{false, true}
	case base.str:
		for _, l := range base.lits {
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
func (t *typer) failure() map[string]*typ {
	env := t.scopeWith(nil).env
	env["error"] = runErrorType
	types := map[string]*typ{}
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
func (t *typer) finally(failure map[string]*typ) {
	env := t.scopeWith(nil).env
	for name, v := range failure {
		env[name] = v.orNull()
	}
	env["run"] = runType
	for _, s := range t.d.Finally {
		v := t.sequenced(s, env)
		env = copyEnv(env)
		env[s.Name] = joinTypes(v, stepErrorValue)
	}
}

// sequenced checks an on_failure or finally step in env and returns the
// type of its value.
func (t *typer) sequenced(s *Step, env map[string]*typ) *typ {
	if t.skip[s.Name] {
		// Its reads are already reported; a set value is unchecked.
		if s.set == nil && s.Output != nil {
			return t.types.of(s.Output)
		}
		return tAny
	}
	t.checkWith(s, env)
	v := tAny
	if s.set != nil {
		typ, problems := t.template(s.set, env, false)
		for _, msg := range problems {
			t.p.add(s.Name, "set: %s", msg)
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
func (t *typer) template(tm *template, env map[string]*typ, edge bool) (*typ, []string) {
	var problems []string
	var walk func(*template) *typ
	walk = func(tm *template) *typ {
		switch tm.kind {
		case tmplSingle:
			typ, msgs := check(tm.prog, env, edge)
			problems = append(problems, msgs...)
			if typ == nil {
				return tAny
			}
			return typ
		case tmplText:
			_, msgs := check(tm.prog, env, edge)
			problems = append(problems, msgs...)
			return tString()
		case tmplObject:
			fields := make(map[string]*typ, len(tm.obj))
			for _, k := range tm.keys {
				fields[k] = walk(tm.obj[k])
			}
			return tRecord(fields)
		case tmplList:
			elem := tNever
			for _, c := range tm.list {
				elem = joinTypes(elem, walk(c))
			}
			return tList(elem)
		default:
			return literalType(tm.lit)
		}
	}
	return walk(tm), problems
}

// literalType is the type of a JSON value as Risor types the same literal
// (F1 3.4): a string is its literal set, a number is int when it is whole
// (and at most 2^53 - 1), and the empty list is never[].
func literalType(v any) *typ {
	switch x := v.(type) {
	case nil:
		return tNull()
	case string:
		return tLit(x)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) <= 1<<53-1 {
			return tInt()
		}
		return tFloat()
	case bool:
		return tBool()
	case map[string]any:
		fields := make(map[string]*typ, len(x))
		for k, fv := range x {
			fields[k] = literalType(fv)
		}
		return tRecord(fields)
	case []any:
		elem := tNever
		for _, item := range x {
			elem = joinTypes(elem, literalType(item))
		}
		return tList(elem)
	}
	return tAny
}

func copyEnv(env map[string]*typ) map[string]*typ {
	out := make(map[string]*typ, len(env)+2)
	maps.Copy(out, env)
	return out
}

// variants splits a type into one type per kind, null left out.
func (t *typ) variants() []*typ {
	var out []*typ
	if t.bool_ {
		out = append(out, tBool())
	}
	if t.num != numNone {
		out = append(out, &typ{num: t.num})
	}
	if t.str {
		out = append(out, &typ{str: true, lits: t.lits})
	}
	if t.list != nil {
		out = append(out, tList(t.list))
	}
	if t.rec != nil {
		out = append(out, &typ{rec: t.rec})
	}
	return out
}

// assign reports where a value of type v does not fit schema s. What it
// cannot decide from types (a number that must be an integer, a string
// that must be in an enum) Apply checks at dispatch, as invalid_params.
func assign(v *typ, s *Schema, path string, required bool, out *[]string) {
	s = s.deref()
	if s == nil || v == nil || v.any || v.isNever() {
		return
	}

	if v.isNull() {
		if required && !s.nullable() {
			*out = append(*out, fmt.Sprintf("%s: is always nil", path))
		}
		return
	}
	if v.null && required && !s.nullable() {
		msg := fmt.Sprintf("%s: may be nil, but the schema requires %s", path, s.kinds())
		*out = append(*out, msg)
	}
	v = v.nonNull()
	if v.union() {
		// The value may be any variant, so each must fit.
		for _, w := range v.variants() {
			assign(w, s, path, required, out)
		}
		return
	}
	if !s.allowsKind(schemaKind(v)) {
		*out = append(*out, fmt.Sprintf("%s: %s is not %s", path, v, s.kinds()))
		return
	}

	if len(s.Enum) > 0 {
		for _, ev := range v.lits {
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

	switch {
	case v.rec != nil:
		assignObject(v.rec, s, path, out)
	case v.list != nil:
		assign(v.list, s.Items, path+"[]", true, out)
	}
}

// assignObject is assign for a record. A record whose rest is typed
// (additionalProperties: a schema) may have any other key, of the rest's
// type, or lack it; one whose rest is any is checked only for the fields
// it lists; an exact record has only its fields.
func assignObject(v *record, s *Schema, path string, out *[]string) {
	elem := v.rest
	typed := elem != nil && !elem.any
	open := elem != nil

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
		f, ok := v.fields[name]
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
		case required && f.optional:
			*out = append(*out, fmt.Sprintf("%s: required %q may be absent", path, name))
			if ps == nil {
				continue // one message for an undeclared name
			}
		}
		if ps != nil {
			assign(f.t, ps, path+"."+name, required, out)
			continue
		}
		// A required name that is not declared must not be null; its
		// type is checked against additionalProperties below.
		if f.t.hasNull() && !f.t.any && (s.Additional == nil || s.Additional.nullable()) {
			*out = append(*out, fmt.Sprintf("%s.%s: may be nil, but it is required", path, name))
		}
	}

	for _, name := range sortedKeys(v.fields) {
		if _, ok := s.Properties[name]; ok {
			continue
		}
		switch {
		case s.Additional != nil:
			assign(v.fields[name].t, s.Additional, path+"."+name, true, out)
		case s.AdditionalSet:
			*out = append(*out, fmt.Sprintf("%s: unexpected %q", path, name))
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
func schemaKind(v *typ) string {
	switch {
	case v.bool_:
		return "boolean"
	case v.num != numNone:
		// A number fits number and integer; Apply checks the value. An int
		// is always an integer, and a float may be one.
		return "integer"
	case v.str:
		return "string"
	case v.list != nil:
		return "array"
	case v.rec != nil:
		return "object"
	}
	return "null"
}

// checkText is diagText with what the engine knows and Risor's diagnostic
// does not say: Risor gives a check diagnostic a code and a span, but not
// the type it found, so the engine finds the read the span belongs to
// (from Risor's analysis of the program's reads) and types it itself.
func checkText(src string, d risor.Diagnostic, env map[string]*typ, refs []risor.Ref) string {
	if d.Span == nil {
		return diagText(src, d)
	}
	at := spanText(src, *d.Span)
	switch d.Code {
	case "no-field":
		for _, r := range refs {
			for k, step := range r.Path {
				if step.Key != at {
					continue
				}
				recv := refType(env, r, k)
				if recv == nil || recv.rec == nil {
					continue
				}
				if _, ok := recv.rec.fields[at]; ok {
					continue
				}
				read := r.Name
				for _, p := range r.Path[:k+1] {
					read += "." + p.Key
				}
				msg := fmt.Sprintf("`%s`: no field %q in %s", read, at, recv.nonNull())
				if near := nearest(at, sortedKeys(recv.rec.fields)); near != "" {
					msg += fmt.Sprintf(" (did you mean %q?)", near)
				}
				return msg
			}
		}
		return fmt.Sprintf("no field %q", at)
	case "kind", "maybe-null", "result-mismatch":
		for _, r := range refs {
			if r.Span == *d.Span {
				if t := refType(env, r, len(r.Path)); t != nil {
					return diagText(src, d) + " (it is " + t.String() + ")"
				}
			}
		}
	}
	return diagText(src, d)
}

// refType is the type of the read r after its first n steps, or nil when
// the types do not say.
func refType(env map[string]*typ, r risor.Ref, n int) *typ {
	t := env[r.Name]
	if t == nil {
		return nil
	}
	for _, step := range r.Path[:n] {
		t = t.nonNull()
		if t.any {
			return t
		}
		if t.rec == nil || t.union() {
			return nil
		}
		if f, ok := t.rec.fields[step.Key]; ok {
			t = f.t
		} else if t.rec.rest != nil {
			t = t.rec.rest
		} else {
			return nil
		}
	}
	return t
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
