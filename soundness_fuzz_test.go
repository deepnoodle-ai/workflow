package workflow

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// This file holds the type system's promise (documentation/definitions.md,
// "The promise") against random definitions: a definition that passes Load,
// run with step outputs that match their schemas, never fails with an
// expression, invalid_params or invalid_output error, outside the cases
// TestOutsideThePromise pins.
//
// soundGen writes definitions the way an author might, reads and all: typed
// output schemas with required and optional fields, set steps and with:
// templates over upstream fields, when and switch routing, catch routes,
// each over list fields, bounded loops, forks and joins, on_failure,
// finally and outputs. Most reads are written carefully (?. and coalesce
// where a value may be nil), and some carelessly, so Load has something to
// reject. Whatever Load accepts is run.

// fschema is a schema as the generator models it, enough to write reads of
// it and to generate values that match it.
type fschema struct {
	kind  string // object, string, number, integer, boolean, enum, array
	enum  []string
	items *fschema
	props []fprop
	// nullable adds null to the type (or the enum).
	nullable bool
	// addl is the schema of an object's undeclared properties.
	addl *fschema
}

type fprop struct {
	name     string
	s        *fschema
	required bool
	// dflt is a default value, which makes the property never nil.
	dflt any
}

func (s *fschema) json() any {
	switch s.kind {
	case "enum":
		vals := make([]any, len(s.enum))
		for i, v := range s.enum {
			vals[i] = v
		}
		if s.nullable {
			vals = append(vals, nil)
		}
		return map[string]any{"enum": vals}
	case "array":
		return map[string]any{"type": s.typ("array"), "items": s.items.json()}
	case "object":
		props := map[string]any{}
		var req []any
		for _, p := range s.props {
			pj := p.s.json().(map[string]any)
			if p.dflt != nil {
				pj["default"] = p.dflt
			}
			props[p.name] = pj
			if p.required {
				req = append(req, p.name)
			}
		}
		out := map[string]any{"type": s.typ("object"), "properties": props}
		if len(req) > 0 {
			out["required"] = req
		}
		if s.addl != nil {
			out["additionalProperties"] = s.addl.json()
		}
		return out
	}
	return map[string]any{"type": s.typ(s.kind)}
}

func (s *fschema) typ(kind string) any {
	if s.nullable {
		return []any{kind, "null"}
	}
	return kind
}

// value returns a random value that matches s. Lists are short and may be
// empty, numbers stay small, and strings are words, so no value is one of
// the pinned exceptions on its own (the generator never indexes a list or
// calls int on a string).
func (s *fschema) value(rng *rand.Rand) any {
	if s.nullable && rng.Intn(4) == 0 {
		return nil
	}
	switch s.kind {
	case "string":
		return []string{"alpha", "Beta", "", "gamma delta"}[rng.Intn(4)]
	case "number":
		return []float64{0, 1, 2.5, -3, 7}[rng.Intn(5)]
	case "integer":
		return float64(rng.Intn(9) - 2)
	case "boolean":
		return rng.Intn(2) == 0
	case "enum":
		return s.enum[rng.Intn(len(s.enum))]
	case "array":
		out := make([]any, rng.Intn(4))
		for i := range out {
			out[i] = s.items.value(rng)
		}
		return out
	case "object":
		out := map[string]any{}
		for _, p := range s.props {
			if p.required || rng.Intn(2) == 0 {
				out[p.name] = p.s.value(rng)
			}
		}
		if s.addl != nil {
			for _, k := range []string{"alice", "bob"} {
				if rng.Intn(2) == 0 {
					out[k] = s.addl.value(rng)
				}
			}
		}
		return out
	}
	panic("unknown kind " + s.kind)
}

var (
	fieldNames = []string{"id", "count", "label", "tags", "ok", "mode", "score", "meta", "items", "note"}
	enumValues = []string{"red", "green", "blue", "amber"}
)

// readable is a value later steps may read: a step, input, or an
// expression over alternatives such as coalesce(b, c).
type readable struct {
	expr string
	s    *fschema // nil: untyped (never read field by field)
	// certain: the value is known to be set (the step ran on every path
	// to the reader), so a direct read is safe.
	certain bool
	// shapes: the value may be one of several object shapes (a caught
	// step on a path its catch converges with), so fields need ?.
	shapes bool
	each   bool // an each step: a list of item outputs
}

// ex is a generated expression and what the generator believes about it.
type ex struct {
	s       string
	kind    string // number, string, bool, list, enum, object
	nilable bool
}

type soundGen struct {
	rng   *rand.Rand
	n     int
	steps []any
	// schemas holds the output schema of every host step, for the handler.
	schemas map[string]*fschema
	// fails lists host steps whose first attempt fails; failIdx the each
	// item that fails.
	fails   map[string]bool
	failIdx map[string]int
	tails   map[string]bool // loop tails: again on the first visit only
	// careless is the chance, in percent, that a read skips a ?. or a
	// coalesce it needs. Zero for careful definitions.
	careless int
	scope    []readable
}

func (g *soundGen) name(prefix string) string {
	g.n++
	return fmt.Sprintf("%s%d", prefix, g.n)
}

func (g *soundGen) chance(pct int) bool { return g.rng.Intn(100) < pct }

func (g *soundGen) add(step map[string]any) map[string]any {
	g.steps = append(g.steps, step)
	return step
}

// schema returns a random output schema: usually an object, sometimes a
// scalar, an enum or a list.
func (g *soundGen) schema(depth int) *fschema {
	switch r := g.rng.Intn(10); {
	case r < 6 && depth > 0:
		s := &fschema{kind: "object"}
		names := append([]string(nil), fieldNames...)
		g.rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
		for _, n := range names[:1+g.rng.Intn(4)] {
			p := fprop{name: n, s: g.schema(depth - 1), required: g.chance(60)}
			if !p.required && !p.s.nullable && p.s.kind != "object" && p.s.kind != "array" && g.chance(25) {
				p.dflt = p.s.value(g.rng)
			}
			s.props = append(s.props, p)
		}
		if g.chance(10) {
			s.addl = g.schema(depth - 1)
		}
		return s
	case r < 7:
		return &fschema{kind: "enum", enum: g.enum(), nullable: g.chance(15)}
	case r < 8 && depth > 0:
		items := g.schema(0)
		if g.chance(30) {
			items = &fschema{kind: "object", props: []fprop{{name: "id", s: &fschema{kind: "string"}, required: true},
				{name: "score", s: &fschema{kind: "number"}, required: g.chance(50)}}}
		}
		return &fschema{kind: "array", items: items}
	default:
		return &fschema{kind: []string{"string", "number", "integer", "boolean"}[g.rng.Intn(4)], nullable: g.chance(10)}
	}
}

func (g *soundGen) enum() []string {
	vals := append([]string(nil), enumValues...)
	g.rng.Shuffle(len(vals), func(i, j int) { vals[i], vals[j] = vals[j], vals[i] })
	return vals[:2+g.rng.Intn(2)]
}

// objectSchema is a schema that is always an object with a required
// field, for steps whose fields later steps read.
func (g *soundGen) objectSchema() *fschema {
	for {
		if s := g.schema(2); s.kind == "object" {
			return s
		}
	}
}

// read writes a read of a random leaf of r: a field path through objects,
// with ?. wherever the value so far may be nil.
func (g *soundGen) read(r readable) ex {
	cur := ex{s: r.expr, nilable: !r.certain || r.s.nullable}
	s := r.s
	sep := func(absent bool) string {
		if cur.nilable || r.shapes || absent {
			if !g.chance(g.careless) {
				return "?."
			}
		}
		return "."
	}
	if r.shapes && s.kind == "object" && g.chance(30) {
		// The error shape of a caught step, on a path where it may
		// also be the step's output.
		return ex{s: cur.s + sep(false) + "error?.message", kind: "string", nilable: true}
	}
	for s.kind == "object" && (len(s.props) > 0 || s.addl != nil) {
		if s.addl != nil && (len(s.props) == 0 || g.chance(40)) {
			// An undeclared property may be absent.
			cur.s += sep(true) + []string{"alice", "bob"}[g.rng.Intn(2)]
			cur.nilable = true
			s = s.addl
		} else {
			p := s.props[g.rng.Intn(len(s.props))]
			cur.s += sep(false) + p.name
			if r.shapes || (!p.required && p.dflt == nil) {
				cur.nilable = true
			}
			s = p.s
		}
		if s.nullable {
			cur.nilable = true
		}
		if g.chance(25) {
			break // stop at an object
		}
	}
	cur.kind = map[string]string{"string": "string", "number": "number", "integer": "number",
		"boolean": "bool", "enum": "enum", "array": "list", "object": "object"}[s.kind]
	if cur.kind == "enum" {
		// Keep the enum's values for comparisons.
		cur.s += "\x00" + strings.Join(s.enum, ",")
	}
	return cur
}

// splitEnum separates an enum read from its values.
func splitEnum(e ex) (ex, []string) {
	if i := strings.IndexByte(e.s, 0); i >= 0 {
		vals := strings.Split(e.s[i+1:], ",")
		e.s = e.s[:i]
		return e, vals
	}
	return e, nil
}

// settle wraps a value that may be nil in coalesce with a fallback of its
// kind, unless the generator is being careless.
func (g *soundGen) settle(e ex, vals []string) ex {
	if !e.nilable || g.chance(g.careless) {
		return e
	}
	fallback := map[string]string{"number": "0", "string": `""`, "bool": "false", "list": "[]", "object": "{}"}[e.kind]
	if e.kind == "enum" {
		fallback = fmt.Sprintf("%q", vals[0])
	}
	switch r := g.rng.Intn(20); {
	case r < 13:
		e.s = fmt.Sprintf("coalesce(%s, %s)", e.s, fallback)
	case r < 19:
		e.s = fmt.Sprintf("(%s || %s)", e.s, fallback)
	default:
		// try's fallback is for an error, not for nil: Load must
		// see that a nil read stays nil, so this is mostly rejected.
		e.s = fmt.Sprintf("try(%s, %s)", e.s, fallback)
	}
	e.nilable = false
	return e
}

// typed returns an expression of kind want (number, string or bool) over a
// random upstream read, or a literal when nothing fits.
func (g *soundGen) typed(want string) ex {
	if want == "bool" && g.chance(15) {
		a, b := g.typed("bool"), g.typed("bool")
		op := []string{"&&", "||"}[g.rng.Intn(2)]
		return ex{s: fmt.Sprintf("(%s) %s (%s)", a.s, op, b.s), kind: "bool", nilable: a.nilable || b.nilable}
	}
	for try := 0; try < 6 && len(g.scope) > 0; try++ {
		r := g.scope[g.rng.Intn(len(g.scope))]
		if r.s == nil || (r.shapes && r.s.kind != "object" && !g.chance(g.careless)) {
			// A caught scalar step is a scalar or an error object:
			// nothing but passing it on is safe.
			continue
		}
		if r.each && want == "number" && g.chance(50) {
			// The items of an each step, any of which may be an error
			// with continue_on_error.
			it := "it != nil"
			if f := r.s.items; f.kind == "object" && len(f.props) > 0 && g.chance(70) {
				it = "it?.error == nil"
			}
			if f := r.s.items; f.kind == "object" && len(f.props) > 0 && g.chance(50) {
				p := f.props[g.rng.Intn(len(f.props))]
				it = fmt.Sprintf("it?.%s != nil", p.name)
			}
			return ex{s: fmt.Sprintf("len(filter(%s, %s))", r.expr, it), kind: "number"}
		}
		e, vals := splitEnum(g.read(r))
		if e.kind == "object" {
			continue
		}
		e = g.settle(e, vals)
		var out string
		switch want + "/" + e.kind {
		case "number/number":
			out = []string{"%s + 1", "%s * 2", "-%s"}[g.rng.Intn(3)]
		case "number/string", "number/list", "number/enum":
			out = "len(%s)"
		case "string/string":
			out = []string{"upper(%s)", `%s + "!"`, "lower(%s)"}[g.rng.Intn(3)]
		case "string/enum":
			out = `%s + "-x"`
		case "string/bool":
			out = `if(%s, "yes", "no")`
		case "bool/number":
			out = "%s > 2"
		case "bool/string":
			out = []string{"len(%s) > 1", `%s == "alpha"`}[g.rng.Intn(2)]
		case "bool/enum":
			out = "%s == " + fmt.Sprintf("%q", vals[g.rng.Intn(len(vals))])
		case "bool/bool":
			out = []string{"%s", "!%s", "%s && true"}[g.rng.Intn(3)]
		case "bool/list":
			out = "len(%s) > 0"
		default:
			continue
		}
		return ex{s: fmt.Sprintf(out, "("+e.s+")"), kind: want, nilable: e.nilable}
	}
	return ex{s: map[string]string{"number": "3", "string": `"lit"`, "bool": "true"}[want], kind: want}
}

func (g *soundGen) anyTyped() ex {
	return g.typed([]string{"number", "string", "bool"}[g.rng.Intn(3)])
}

// with returns a with: block of a few templates over upstream values.
func (g *soundGen) with(n int) map[string]any {
	w := map[string]any{}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("p%d", i)
		if g.chance(15) && len(g.scope) > 0 {
			// Pass a whole value on, nil or not.
			w[key] = "${{ " + g.scope[g.rng.Intn(len(g.scope))].expr + " }}"
			continue
		}
		e := g.anyTyped()
		if g.chance(20) {
			w[key] = "Value: ${{ " + e.s + " }}" // renders as text
		} else {
			w[key] = "${{ " + e.s + " }}"
		}
	}
	return w
}

// hostStep adds a host step with an output schema and a with: block that
// reads upstream values.
func (g *soundGen) hostStep(prefix string, s *fschema) map[string]any {
	name := g.name(prefix)
	step := map[string]any{"name": name}
	if g.chance(20) {
		// A host kind whose params schema Load checks with: against.
		step["num"] = "x"
		w := map[string]any{"n": "${{ " + g.settle(g.typed("number"), nil).s + " }}"}
		if g.chance(50) {
			w["s"] = "${{ " + g.settle(g.typed("string"), nil).s + " }}"
		}
		if g.chance(30) {
			w["b"] = "${{ " + g.settle(g.typed("bool"), nil).s + " }}"
		}
		step["with"] = w
	} else {
		step["action"] = "x"
		if g.chance(70) {
			step["with"] = g.with(1 + g.rng.Intn(3))
		}
	}
	if s != nil {
		step["output"] = s.json()
	}
	g.schemas[name] = s
	return g.add(step)
}

// setStep adds a set step whose fields are typed expressions, and returns
// the schema the generator infers for it.
func (g *soundGen) setStep() (map[string]any, *fschema) {
	name := g.name("set")
	s := &fschema{kind: "object"}
	val := map[string]any{}
	for i := 0; i < 1+g.rng.Intn(3); i++ {
		e := g.anyTyped()
		key := fmt.Sprintf("f%d", i)
		val[key] = "${{ " + e.s + " }}"
		kind := map[string]string{"number": "number", "string": "string", "bool": "boolean"}[e.kind]
		s.props = append(s.props, fprop{name: key, s: &fschema{kind: kind}, required: !e.nilable})
	}
	return g.add(map[string]any{"name": name, "set": val}), s
}

func next(step map[string]any, edge any) {
	edges, _ := step["next"].([]any)
	step["next"] = append(edges, edge)
}

// exit is a step whose path continues into the next segment.
type exit func(to string)

func plainExit(step map[string]any) exit {
	return func(to string) { next(step, to) }
}

// segment appends one piece of graph and returns its first step's name and
// its exits. Its readables are added to scope as they become readable.
func (g *soundGen) segment(first bool) (string, []exit) {
	r := g.rng.Intn(100)
	if first {
		r = 0
	}
	switch {
	case r < 25:
		return g.plainSegment()
	case r < 35:
		step, s := g.setStep()
		g.scope = append(g.scope, readable{expr: step["name"].(string), s: s, certain: true})
		return step["name"].(string), []exit{plainExit(step)}
	case r < 50:
		return g.ifElseSegment()
	case r < 60:
		return g.switchSegment()
	case r < 72:
		return g.forkSegment()
	case r < 85:
		return g.eachSegment()
	default:
		return g.loopSegment()
	}
}

// plainSegment is a host step, maybe with a catch route to a handler that
// ends the path or rejoins it.
func (g *soundGen) plainSegment() (string, []exit) {
	s := g.schema(2)
	step := g.hostStep("a", s)
	name := step["name"].(string)
	if !g.chance(30) {
		g.scope = append(g.scope, readable{expr: name, s: s, certain: true})
		return name, []exit{plainExit(step)}
	}
	h := g.name("h")
	if g.chance(50) {
		step["catch"] = h
	} else {
		step["catch"] = []any{map[string]any{"errors": []any{"boom"}, "next": h}}
	}
	g.fails[name] = g.chance(50)
	msg := "${{ " + name + ".error.message }}"
	if g.chance(g.careless) {
		msg = "${{ " + name + ".id }}" // the error shape has no id
	}
	handler := g.add(map[string]any{"name": h, "set": map[string]any{"msg": msg}})
	exits := []exit{plainExit(step)}
	shapes := false
	if g.chance(50) {
		// The handler rejoins the main line, so downstream the step's
		// value is either its output or its error.
		exits = append(exits, plainExit(handler))
		shapes = true
	}
	g.scope = append(g.scope, readable{expr: name, s: s, certain: true, shapes: shapes})
	return name, exits
}

// ifElseSegment branches on a condition with match: first. Each branch is
// a host step; with the same schema, coalesce(b, c) is certain.
func (g *soundGen) ifElseSegment() (string, []exit) {
	cond := g.settle(g.typed("bool"), nil)
	head := g.add(map[string]any{"name": g.name("if"), "set": map[string]any{"go": "${{ " + cond.s + " }}"}})
	headName := head["name"].(string)
	s := g.objectSchema()
	b := g.hostStep("b", s)
	c := g.hostStep("c", s)
	head["match"] = "first"
	next(head, map[string]any{"step": b["name"], "when": headName + ".go"})
	if g.chance(80) {
		next(head, c["name"])
	} else {
		next(head, map[string]any{"step": c["name"], "when": "!" + headName + ".go"})
	}
	g.scope = append(g.scope,
		readable{expr: headName, s: &fschema{kind: "object", props: []fprop{{name: "go", s: &fschema{kind: "boolean"}, required: !cond.nilable}}}, certain: true},
		readable{expr: b["name"].(string), s: s, certain: g.chance(g.careless)},
		readable{expr: c["name"].(string), s: s, certain: g.chance(g.careless)},
		readable{expr: fmt.Sprintf("coalesce(%s, %s)", b["name"], c["name"]), s: s, certain: true},
	)
	return headName, []exit{plainExit(b), plainExit(c)}
}

// switchSegment routes on an enum: a host step that returns one, then a
// case step per value, or a default for some.
func (g *soundGen) switchSegment() (string, []exit) {
	vals := g.enum()
	es := &fschema{kind: "object", props: []fprop{{name: "mode", s: &fschema{kind: "enum", enum: vals}, required: true}}}
	head := g.hostStep("sw", es)
	headName := head["name"].(string)
	s := g.objectSchema()
	cases := map[string]any{}
	var exits []exit
	var alts []string
	for i, v := range vals {
		if i == len(vals)-1 && g.chance(40) {
			break
		}
		cs := g.hostStep("k", s)
		cases[v] = cs["name"]
		exits = append(exits, plainExit(cs))
		alts = append(alts, cs["name"].(string))
	}
	sw := map[string]any{"switch": headName + ".mode", "cases": cases}
	if len(cases) < len(vals) || g.chance(20) {
		d := g.hostStep("d", s)
		sw["default"] = d["name"]
		exits = append(exits, plainExit(d))
		alts = append(alts, d["name"].(string))
	}
	head["next"] = sw
	g.scope = append(g.scope, readable{expr: headName, s: es, certain: true})
	for _, a := range alts {
		g.scope = append(g.scope, readable{expr: a, s: s, certain: g.chance(g.careless)})
	}
	g.scope = append(g.scope, readable{expr: "coalesce(" + strings.Join(alts, ", ") + ")", s: s, certain: true})
	return headName, exits
}

// forkSegment forks into two or three host steps and joins them, naming
// the unconditional branches, or naming nothing.
func (g *soundGen) forkSegment() (string, []exit) {
	fork := g.hostStep("f", g.objectSchema())
	forkName := fork["name"].(string)
	g.scope = append(g.scope, readable{expr: forkName, s: g.schemas[forkName], certain: true})
	join := map[string]any{"name": g.name("j")}
	var named []any
	var branches []readable
	n := 2 + g.rng.Intn(2)
	conditional := false
	for i := 0; i < n; i++ {
		s := g.schema(2)
		b := g.hostStep("p", s)
		bn := b["name"].(string)
		next(b, join["name"])
		if i > 0 && g.chance(25) {
			cond := g.settle(g.typed("bool"), nil)
			next(fork, map[string]any{"step": bn, "when": cond.s})
			fork["match"] = "all"
			conditional = true
			branches = append(branches, readable{expr: bn, s: s, certain: g.chance(g.careless)})
			if g.chance(g.careless) {
				named = append(named, bn) // a join that can fire without it
			}
			continue
		}
		next(fork, bn)
		named = append(named, bn)
		branches = append(branches, readable{expr: bn, s: s, certain: true})
	}
	_ = conditional
	if g.chance(30) {
		join["join"] = []any{}
		for i := range branches {
			branches[i].certain = branches[i].certain && g.chance(g.careless)
		}
	} else {
		join["join"] = named
	}
	g.add(join)
	g.scope = append(g.scope, branches...)
	return forkName, []exit{plainExit(join)}
}

// eachSegment runs a host step for every element of an upstream list,
// reading item in with:.
func (g *soundGen) eachSegment() (string, []exit) {
	// Find a list to iterate, or make one.
	var src ex
	var items *fschema
	for try := 0; try < 6 && len(g.scope) > 0; try++ {
		r := g.scope[g.rng.Intn(len(g.scope))]
		if r.s == nil {
			continue
		}
		e, _ := splitEnum(g.read(r))
		if e.kind != "list" {
			continue
		}
		src, items = g.settle(e, nil), findList(r.s, e.s, r.expr)
		if items != nil {
			break
		}
	}
	if items == nil {
		src = ex{s: `["x", "y", "z"]`, kind: "list"}
		items = &fschema{kind: "string"}
	}
	s := g.schema(1)
	step := g.hostStep("e", s)
	name := step["name"].(string)
	step["each"] = src.s
	w, _ := step["with"].(map[string]any)
	if w == nil {
		w = map[string]any{}
	}
	w["item"], w["index"] = "${{ item }}", "${{ index }}"
	if items.kind == "object" {
		w["id"] = "${{ upper(item.id) }}"
		w["score"] = "${{ coalesce(item.score, 0) * 2 }}"
		if g.chance(g.careless) {
			w["score"] = "${{ item.score * 2 }}"
		}
	} else if items.kind == "string" {
		w["up"] = "${{ upper(item) }}"
	}
	step["with"] = w
	if c := g.rng.Intn(3); c > 0 {
		step["concurrency"] = c
	}
	if g.chance(40) {
		step["continue_on_error"] = true
		g.failIdx[name] = g.rng.Intn(3)
	}
	g.scope = append(g.scope, readable{expr: name, s: &fschema{kind: "array", items: s}, certain: true, each: true})
	return name, []exit{plainExit(step)}
}

// findList returns the item schema of the list a read reaches, by walking
// the field path in the read's text.
func findList(s *fschema, read, base string) *fschema {
	path := strings.TrimPrefix(read, base)
	if strings.HasPrefix(path, "coalesce(") {
		return nil
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '.' || r == '?' }) {
		if s.kind != "object" {
			return nil
		}
		var found *fschema
		for _, p := range s.props {
			if p.name == part {
				found = p.s
			}
		}
		if found == nil {
			return nil
		}
		s = found
	}
	if s.kind != "array" {
		return nil
	}
	return s.items
}

// loopSegment is a bounded loop: a head that reads its tail's last pass,
// a body, and a tail that goes back once.
func (g *soundGen) loopSegment() (string, []exit) {
	tailName := g.name("t")
	ts := &fschema{kind: "object", props: []fprop{
		{name: "again", s: &fschema{kind: "boolean"}, required: true},
		{name: "count", s: &fschema{kind: "number"}, required: true}}}
	// The head reads the tail's previous pass, which the first pass has
	// not run.
	g.scope = append(g.scope, readable{expr: tailName, s: ts, certain: g.chance(g.careless)})
	head := g.hostStep("lh", g.schema(2))
	head["max_visits"] = 4
	headName := head["name"].(string)
	g.scope[len(g.scope)-1].certain = false
	g.scope = append(g.scope, readable{expr: headName, s: g.schemas[headName], certain: true})
	body := g.hostStep("lb", g.schema(2))
	next(head, body["name"])
	g.scope = append(g.scope, readable{expr: body["name"].(string), s: g.schemas[body["name"].(string)], certain: true})
	tail := map[string]any{"name": tailName, "action": "x", "output": ts.json(), "match": "first",
		"with": map[string]any{"prev": "${{ coalesce(" + tailName + "?.count, 0) + 1 }}"}}
	g.schemas[tailName] = ts
	g.tails[tailName] = true
	g.add(tail)
	next(body, tailName)
	next(tail, map[string]any{"step": headName, "when": tailName + ".again"})
	// Past the loop, the tail has run.
	for i := range g.scope {
		if g.scope[i].expr == tailName {
			g.scope[i].certain = true
		}
	}
	return headName, []exit{plainExit(tail)}
}

// soundDoc builds a random definition.
func soundDoc(seed int64) (map[string]any, *soundGen) {
	g := &soundGen{
		rng: rand.New(rand.NewSource(seed)), schemas: map[string]*fschema{},
		fails: map[string]bool{}, failIdx: map[string]int{}, tails: map[string]bool{},
	}
	if g.chance(50) {
		g.careless = 4 + g.rng.Intn(6)
	}
	input := &fschema{kind: "object", props: []fprop{
		{name: "user", s: &fschema{kind: "string"}, required: true},
		{name: "limit", s: &fschema{kind: "number"}, dflt: float64(3)},
		{name: "flag", s: &fschema{kind: "boolean"}},
		{name: "labels", s: &fschema{kind: "array", items: &fschema{kind: "string"}}},
	}}
	g.scope = append(g.scope, readable{expr: "input", s: input, certain: true})

	var exits []exit
	for i, n := 0, 3+g.rng.Intn(5); i < n; i++ {
		first, out := g.segment(i == 0)
		for _, e := range exits {
			e(first)
		}
		exits = out
	}
	doc := map[string]any{"input": input.json(), "steps": g.steps}

	// Steps outside the main line see every main step as maybe-unset.
	main := g.scope
	g.scope = nil
	for _, r := range main {
		r.certain = r.expr == "input" || g.chance(g.careless)
		g.scope = append(g.scope, r)
	}
	if g.chance(50) {
		// A step that fails the run, for on_failure to handle.
		for _, s := range g.steps {
			step := s.(map[string]any)
			if _, host := g.schemas[step["name"].(string)]; host && step["catch"] == nil && !g.tails[step["name"].(string)] && g.chance(30) {
				g.fails[step["name"].(string)] = true
				break
			}
		}
	}
	if g.chance(60) {
		doc["on_failure"] = g.cleanup("of", "error", &fschema{kind: "object", props: []fprop{
			{name: "message", s: &fschema{kind: "string"}, required: true},
			{name: "type", s: &fschema{kind: "string"}, required: true},
			{name: "step", s: &fschema{kind: "string"}, required: true}}})
	}
	if g.chance(60) {
		doc["finally"] = g.cleanup("fin", "run", &fschema{kind: "object", props: []fprop{
			{name: "status", s: &fschema{kind: "string"}, required: true},
			{name: "error", s: &fschema{kind: "object", props: []fprop{
				{name: "message", s: &fschema{kind: "string"}, required: true}}}}}})
	}
	if g.chance(70) {
		g.scope = nil
		for _, r := range main {
			// outputs see a step as set only if no run completes
			// without it; the generator does not know that, so it
			// guesses.
			r.certain = r.expr == "input" || (r.certain && g.chance(50)) || g.chance(g.careless)
			g.scope = append(g.scope, r)
		}
		outputs := map[string]any{}
		for i := 0; i < 1+g.rng.Intn(3); i++ {
			e := g.anyTyped()
			if g.chance(50) {
				outputs[fmt.Sprintf("o%d", i)] = "${{ " + e.s + " }}"
				continue
			}
			kind := map[string]string{"number": "number", "string": "string", "bool": "boolean"}[e.kind]
			var schema any = kind
			if e.nilable {
				schema = map[string]any{"type": []any{kind, "null"}}
			}
			outputs[fmt.Sprintf("o%d", i)] = map[string]any{"value": "${{ " + e.s + " }}", "schema": schema}
		}
		doc["outputs"] = outputs
	}
	return doc, g
}

// cleanup returns on_failure or finally steps that read the global (error
// or run) and upstream values. A cleanup step may fail, and a later one
// may read it.
func (g *soundGen) cleanup(prefix, global string, gs *fschema) []any {
	saved := g.scope
	defer func() { g.scope = saved }()
	g.scope = append(append([]readable(nil), saved...), readable{expr: global, s: gs, certain: true})
	start := len(g.steps)
	for i := 0; i < 1+g.rng.Intn(2); i++ {
		if g.chance(30) {
			step, s := g.setStep()
			g.scope = append(g.scope, readable{expr: step["name"].(string), s: s, certain: true})
			continue
		}
		s := g.objectSchema()
		step := g.hostStep(prefix, s)
		name := step["name"].(string)
		certain := true
		if prefix == "fin" && g.chance(30) {
			// A failed finally step is read as nil by the next.
			g.fails[name] = true
			certain = g.chance(g.careless)
		}
		g.scope = append(g.scope, readable{expr: name, s: s, certain: certain})
	}
	out := append([]any(nil), g.steps[start:]...)
	g.steps = g.steps[:start]
	return out
}

// soundHandler answers a dispatch with a value that matches the step's
// schema, chosen by the seed and the unit so every order sees the same
// value, and fails the attempts the generator marked.
func (g *soundGen) handler(seed int64) func(Command) result {
	return func(c Command) result {
		if g.fails[c.Step] && c.Attempt == 1 {
			return fail("boom", "injected", false)
		}
		if idx, ok := g.failIdx[c.Step]; ok && c.Index == idx {
			return fail("boom", "injected item", false)
		}
		s := g.schemas[c.Step]
		if s == nil {
			return ok(nil)
		}
		h := fnv.New64a()
		fmt.Fprint(h, seed, c.StepID.Unit())
		v := s.value(rand.New(rand.NewSource(int64(h.Sum64()))))
		if g.tails[c.Step] {
			v.(map[string]any)["again"] = !laterVisit(c)
		}
		return ok(v)
	}
}

// promiseBroken lists the errors in st the promise rules out.
func promiseBroken(st *State) []string {
	bad := func(typ string) bool {
		return typ == ErrorTypeExpression || typ == ErrorTypeInvalidParams || typ == ErrorTypeInvalidOutput
	}
	var out []string
	for e := st.Error; e != nil; e = e.OnFailure {
		if bad(e.Type) {
			out = append(out, fmt.Sprintf("run error %s: %s: %s", e.Step, e.Type, e.Message))
		}
	}
	for _, e := range st.FinallyErrors {
		if bad(e.Type) {
			out = append(out, fmt.Sprintf("finally %s: %s: %s", e.Step, e.Type, e.Message))
		}
	}
	for _, key := range sortedKeys(st.nodes) {
		n := st.nodes[key]
		if n.Error != nil && bad(n.Error.Type) {
			out = append(out, fmt.Sprintf("step %s: %s: %s", key, n.Error.Type, n.Error.Message))
		}
		for i, u := range n.Units {
			if u.Error != nil && bad(u.Error.Type) {
				out = append(out, fmt.Sprintf("unit %s[%d]: %s: %s", key, i, u.Error.Type, u.Error.Message))
			}
		}
	}
	return out
}

// runChecked runs def to the end in an order chosen by rng, checking the
// state after every event, since a caught step error is gone from the
// final state.
func runChecked(t *testing.T, def *Definition, handle func(Command) result, input map[string]any, rng *rand.Rand) (*fakeHost, []string) {
	t.Helper()
	h := newHost(t, def, handle)
	seen := map[string]bool{}
	var broken []string
	check := func() {
		for _, b := range promiseBroken(h.st) {
			if !seen[b] {
				seen[b] = true
				broken = append(broken, b)
			}
		}
	}
	h.start(input)
	check()
	for i := 0; h.final == nil; i++ {
		require.True(t, i < 10000, "run did not finish")
		ids := h.pendingIDs()
		if len(ids) == 0 {
			require.True(t, len(h.timers) > 0, "run is stuck: status %s", h.st.Status)
			h.fire()
		} else {
			h.deliver(ids[rng.Intn(len(ids))])
		}
		check()
	}
	return h, broken
}

var soundKinds = schemaKinds{
	"action": {nil, nil},
	"num": {mustParse(`{"type": "object", "required": ["n"], "properties": {
	  "n": {"type": "number"}, "s": {"type": "string"}, "b": {"type": "boolean"}}}`), nil},
}

func mustParse(doc string) *Schema {
	s, err := ParseSchema([]byte(doc))
	if err != nil {
		panic(err)
	}
	return s
}

// TestPromiseRandomDefinitions generates definitions, loads them, and runs
// every one that loads in several completion orders. None may fail with
// an expression, invalid_params or invalid_output error.
func TestPromiseRandomDefinitions(t *testing.T) {
	const definitions = 1500
	loaded, completed, failed := 0, 0, 0
	problems := map[string]int{}
	features := map[string]int{}
	for seed := int64(0); seed < definitions; seed++ {
		doc, g := soundDoc(seed)
		data, err := json.Marshal(doc)
		require.NoError(t, err)
		def, err := Load(data, soundKinds)
		if err != nil {
			problems[firstProblem(err)]++
			continue
		}
		loaded++
		for _, f := range soundFeatures {
			if strings.Contains(string(data), f) {
				features[f]++
			}
		}
		inputs := []map[string]any{{"user": "u"}, {"user": "v", "limit": 10, "flag": true, "labels": []any{"a", "b"}}}
		for order := int64(0); order < 4; order++ {
			h, broken := runChecked(t, def, g.handler(seed), inputs[order%2], rand.New(rand.NewSource(seed*31+order)))
			if len(broken) > 0 {
				t.Fatalf("seed %d order %d: validated but broke the promise:\n  %s\ndefinition:\n%s",
					seed, order, strings.Join(broken, "\n  "), data)
			}
			if order == 0 {
				switch h.st.Status {
				case StatusCompleted:
					completed++
				case StatusFailed:
					failed++
				}
			}
		}
	}
	t.Logf("%d definitions: %d loaded (%d completed, %d failed by injected errors)", definitions, loaded, completed, failed)
	if testing.Verbose() {
		var top []string
		for p, n := range problems {
			top = append(top, fmt.Sprintf("%5d %s", n, p))
		}
		sort.Sort(sort.Reverse(sort.StringSlice(top)))
		if len(top) > 12 {
			top = top[:12]
		}
		t.Logf("most common Load problems:\n%s", strings.Join(top, "\n"))
	}
	t.Logf("loaded definitions with each feature: %v", features)
	for _, f := range soundFeatures {
		require.True(t, features[f]*100 >= loaded*5, "only %d loaded definitions have %s", features[f], f)
	}
	require.True(t, loaded*100 >= definitions*30, "only %d of %d definitions loaded", loaded, definitions)
	require.True(t, completed*100 >= loaded*30, "only %d of %d loaded runs completed", completed, loaded)
}

// TestPromiseCheckSeesCaughtErrors: the check in runChecked finds an
// expression error even when a catch routes it and the run completes, using
// a case outside the promise (an untyped step).
func TestPromiseCheckSeesCaughtErrors(t *testing.T) {
	def, err := Load([]byte(`{"steps": [
	  {"name": "a", "action": "x", "next": ["b"]},
	  {"name": "b", "action": "x", "with": {"n": "${{ a.k + 1 }}"}, "catch": "h"},
	  {"name": "h", "set": "${{ b.error.message }}"}]}`), soundKinds)
	require.NoError(t, err)
	h, broken := runChecked(t, def, func(Command) result { return ok(map[string]any{}) }, nil, rand.New(rand.NewSource(1)))
	require.Equal(t, StatusCompleted, h.st.Status)
	require.True(t, len(broken) > 0, "the caught error was not seen")
	require.Contains(t, broken[0], "b: expression")
}

// TestEmptyListFallbackKeepsItemType was found by
// TestPromiseRandomDefinitions (seed 26687 of a longer sweep): an empty
// list literal joined with a typed list, the usual fallback for a list that
// may be nil, must keep the items typed, so Load rejects reads of fields
// that do not exist or may be nil.
func TestEmptyListFallbackKeepsItemType(t *testing.T) {
	tags := `{"name": "a", "action": "x", "output": {"type": "object", "properties": {"tags": {"type": "array",
	  "items": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}, "score": {"type": "number"}}}}}}, "next": ["s"]}`
	outs := map[string]any{"a": map[string]any{"tags": []any{map[string]any{"id": "x"}}}}
	for name, doc := range map[string]string{
		"coalesce-misspelled": `{"steps": [` + tags + `, {"name": "s", "set": "${{ map(coalesce(a.tags, []), it.nope) }}"}]}`,
		"or-misspelled":       `{"steps": [` + tags + `, {"name": "s", "set": "${{ map(a.tags || [], it.nope) }}"}]}`,
		"if-misspelled":       `{"steps": [` + tags + `, {"name": "s", "set": "${{ map(if(true, a.tags, []), it.nope) }}"}]}`,
		"concat-misspelled":   `{"steps": [` + tags + `, {"name": "s", "set": "${{ map(concat(coalesce(a.tags, [{\"id\": \"y\"}]), []), it.nope) }}"}]}`,
		"each-optional-field": `{"steps": [` + strings.Replace(tags, `"next": ["s"]`, `"next": ["e"]`, 1) + `,
		  {"name": "e", "action": "x", "each": "coalesce(a.tags, [])", "with": {"v": "${{ item.score * 2 }}"}}]}`,
	} {
		got := soundRun(t, doc, nil, outs, nil)
		if !strings.HasPrefix(got, "VALIDATE") && !strings.HasPrefix(got, "completed") {
			t.Errorf("%s: validated but %s", name, got)
		}
	}
}

// soundFeatures are keys whose presence the test counts among loaded
// definitions, so a generator change that stops any of them loading fails.
var soundFeatures = []string{`"switch"`, `"each"`, `"continue_on_error"`, `"max_visits"`, `"catch"`,
	`"join"`, `"on_failure"`, `"finally"`, `"outputs"`, `"num"`, `"set"`, `"when"`, `?.`, `coalesce(`}

// firstProblem is the first Load problem with step names and quoted
// values blanked, for counting what Load rejects most.
func firstProblem(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "\n  "); i >= 0 {
		msg = msg[i+3:]
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if i := strings.Index(msg, ": "); i >= 0 && strings.HasPrefix(msg, "step ") {
		msg = msg[i+2:]
	}
	if len(msg) > 90 {
		msg = msg[:90]
	}
	return msg
}
