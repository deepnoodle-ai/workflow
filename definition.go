package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"time"
)

// Definition is a loaded workflow, made by [Load] or [LoadMap] and ready
// for [Apply].
//
// A Definition is safe for concurrent use. Its exported fields describe
// the loaded document for hosts to read; treat them as read-only. Apply
// relies on the checks Load made, so changing a field after Load is not
// supported. Load again, with [LoadOption]s for what the host sets.
type Definition struct {
	Name        string
	Description string
	// Input is the schema of the run's input, read as `input`. It
	// describes an object. Nil means the input is an untyped object.
	Input *Schema
	// Schemas are named types that input and output can refer to by name.
	Schemas map[string]*Schema
	Steps   []*Step
	// OnFailure steps run once, in order, when the run fails, and read
	// the failure as `error`.
	OnFailure []*Step
	// Finally steps run once, in order, when the run's outcome is decided,
	// whatever it is, and read it as `run`.
	Finally []*Step
	// Outputs are the declared run outputs, by name. Nil when the
	// definition declares none; see [Definition.OutputSchema].
	Outputs map[string]*Output

	// limits bounds what a run may keep in its state; see [WithLimits].
	limits Limits

	raw       map[string]any
	hash      string
	validated bool
	byName    map[string]*Step
	reach     map[string]map[string]bool // reach[a][b]: b is reachable from a
	outputs   *template
	// back holds every loop's back edges: edges from inside a loop to one
	// of its heads. Without them the graph is acyclic.
	back map[[2]string]bool
	// loopsOf lists the loops each step is in, outermost first.
	loopsOf map[string][]*loop
	// arrive[j][i] holds the steps from which a path can reach join j
	// without entering a head of the outermost i loops j is in. With i =
	// len(loopsOf[j]) that is within the join's current pass.
	arrive map[string][]map[string]bool
	// facts are what is known to have settled when each step runs.
	facts map[string]settled
	// keepVisits turns off dropping superseded visits, for tests that
	// check dropping changes nothing observable.
	keepVisits bool
	// lenientJoins turns off the Load check that a join's named steps
	// settle, for tests of the run-time check behind it.
	lenientJoins bool
	// requiredOutputs is the schema the outputs must fit, from
	// [RequireOutputs].
	requiredOutputs *Schema
}

// Limits bounds the size of a run's state. Outputs are kept in state because
// expressions read them, so a step whose output is too large fails with
// [ErrorTypeOutputTooLarge] instead of bloating every later transaction.
type Limits struct {
	// MaxOutputBytes caps one step output or set value, as JSON. Default
	// 256 KiB.
	MaxOutputBytes int
	// MaxStateBytes caps State.Bytes: the outputs, set values and each
	// items a run holds at once. Visits a loop has superseded are dropped
	// and no longer count. Default 8 MiB.
	MaxStateBytes int
}

const (
	defaultMaxOutputBytes = 256 << 10
	defaultMaxStateBytes  = 8 << 20
)

func (l Limits) maxOutput() int {
	if l.MaxOutputBytes > 0 {
		return l.MaxOutputBytes
	}
	return defaultMaxOutputBytes
}

func (l Limits) maxState() int {
	if l.MaxStateBytes > 0 {
		return l.MaxStateBytes
	}
	return defaultMaxStateBytes
}

// Output declares one run output: a template evaluated once every path
// has ended, and optionally the schema its value must match and a
// description, for hosts that publish a workflow's contract (a workflow
// exposed as a tool).
type Output struct {
	// Value is the template as written: `${{ review.body }}`, a literal,
	// or an object or list of templates.
	Value any
	// Schema is what the value must be. Load checks the template's type
	// against it, and Apply validates the value when the run completes.
	// Nil leaves the value unchecked.
	Schema      *Schema
	Description string
}

// Step is one node of the graph.
//
// A step's kind is the one key among the host's known kinds (agent, action,
// ...) or the control kinds join, set and sleep. For a host kind, that key and
// every key the library does not reserve go into Config, unevaluated. Only
// With is evaluated, at dispatch.
type Step struct {
	Name        string
	Description string

	// Kind and Config are resolved by Load.
	Kind   string
	Config map[string]any

	// Output is the schema of the step's output, from the definition's
	// `output:` key. A host may also supply one through KindSchemas.
	Output *Schema

	With            map[string]any
	Each            string
	Concurrency     int
	ContinueOnError bool

	// Join lists the steps a join step requires: each must have settled on
	// a path that arrived, and Load rejects one a path can skip. A join
	// always waits for every live path that can still reach it, named or
	// not.
	Join []string
	// Set is the value a set step produces. Strings may hold ${{ ... }}
	// templates.
	Set any
	// Sleep is how long a sleep step waits.
	Sleep time.Duration

	Next []Edge
	// Switch routes on one value instead of Next: `next: {switch, cases,
	// default}`.
	Switch    *Switch
	Match     string // "all" (default) or "first"
	Retry     *Retry
	Catch     []Catch
	Timeout   time.Duration
	MaxVisits int

	extra   map[string]any
	control []string // control-kind keys present on the step

	each   *program
	with   *template
	set    *template
	edges  []*program
	swtch  *program
	params *Schema // from KindSchemas
}

// Switch routes a path by one value: the first case whose key equals the
// value, else the default. Keys are compared with the value as a string,
// or as its JSON encoding for numbers and booleans.
type Switch struct {
	Expr  string
	Cases []Case // sorted by value
	// Default is where unmatched values go. An empty Default with
	// HasDefault ends the path.
	Default    []string
	HasDefault bool
}

// Case is one switch arm. Several targets fork the path.
type Case struct {
	Value   string
	Targets []string
}

// Edge is an outgoing edge. An edge with no When always matches.
type Edge struct {
	Step string
	When string
}

// Retry declares that a step may be dispatched again after a retryable
// failure. Nothing retries without it.
type Retry struct {
	// MaxAttempts counts the first attempt. 3 means up to two retries.
	MaxAttempts int
	// Backoff is the delay before the second attempt. Default 1s.
	Backoff time.Duration
	// MaxBackoff caps the delay. Default 5m.
	MaxBackoff time.Duration
	// Factor multiplies the delay after each attempt. Default 2.
	Factor float64
	// Errors limits retries to these error types. Empty retries any
	// retryable error.
	Errors []string
}

// Catch routes a step's terminal failure to another step instead of failing
// the run. The failed step's value becomes {"error": {...}}.
type Catch struct {
	// Errors limits the route to these error types. Empty catches any.
	Errors []string
	Next   string
}

// Control kinds the library executes itself.
const (
	KindJoin  = "join"
	KindSet   = "set"
	KindSleep = "sleep"
)

var reservedKeys = map[string]bool{
	"name": true, "description": true, "with": true, "each": true,
	"concurrency": true, "continue_on_error": true, "next": true,
	"match": true, "retry": true, "catch": true, "timeout": true, "output": true,
	"max_visits": true, KindJoin: true, KindSet: true, KindSleep: true,
}

// Load reads a definition from JSON and validates it against the host's
// step kinds: it resolves each step's kind, checks the graph, and compiles
// and type-checks every expression. It returns a *[ValidationError] listing
// every problem, not just the first. A duplicate key or invalid UTF-8 is a
// parse error. Hosts that author in YAML decode it with their YAML library
// and call [LoadMap].
func Load(data []byte, kinds Kinds, opts ...LoadOption) (*Definition, error) {
	var m map[string]any
	if err := jsonv2.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("workflow: parse: %w", err)
	}
	d, err := parseMap(m)
	if err != nil {
		return nil, err
	}
	for _, o := range opts {
		o(d)
	}
	if err := d.validate(kinds); err != nil {
		return nil, err
	}
	return d, nil
}

// LoadMap is [Load] for a decoded document, such as the output of a YAML
// decoder. The map is normalized through JSON first, so it loads with the
// same hash as the equivalent JSON.
func LoadMap(doc map[string]any, kinds Kinds, opts ...LoadOption) (*Definition, error) {
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("workflow: parse: %w", err)
	}
	return Load(data, kinds, opts...)
}

// LoadOption sets something the host decides about a definition, not its
// author. Options are not part of the document and do not change its hash.
type LoadOption func(*Definition)

// WithLimits sets the state limits for runs of the definition. Zero
// values use the defaults.
func WithLimits(l Limits) LoadOption { return func(d *Definition) { d.limits = l } }

// RequireOutputs makes Load check that the run's outputs fit s: a host that
// runs a definition it did not write, such as one an agent generated for a
// step, passes the step's output schema ([Command.OutputSchema]) so that a
// definition whose outputs cannot fill the step fails to load, with
// problems the author can fix, instead of failing the step with
// invalid_output after the run. An output with a schema is typed by its
// schema, and one without by its value. A definition without outputs: is
// rejected, since its outputs are whichever steps its paths end at. What
// types cannot decide, such as an enum value read from an unchecked field,
// is left to the step's own output validation. A nil s checks nothing.
func RequireOutputs(s *Schema) LoadOption { return func(d *Definition) { d.requiredOutputs = s } }

// Limits returns the definition's state limits, as set by [WithLimits].
func (d *Definition) Limits() Limits { return d.limits }

// Hash identifies the definition's content. A [State] records the hash of
// the definition it started with, and Apply refuses a different one.
func (d *Definition) Hash() string { return d.hash }

// MarshalJSON returns the document the definition was parsed from.
func (d *Definition) MarshalJSON() ([]byte, error) { return json.Marshal(d.raw) }

// Step returns the step with the given name, including on_failure and
// finally steps.
func (d *Definition) Step(name string) *Step { return d.byName[name] }

func parseMap(m map[string]any) (*Definition, error) {
	canon, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("workflow: parse: %w", err)
	}
	sum := sha256.Sum256(canon)
	d := &Definition{raw: m, hash: hex.EncodeToString(sum[:])}

	p := &problems{}
	for key, v := range m {
		switch key {
		case "name":
			d.Name = p.str("", key, v)
		case "description":
			d.Description = p.str("", key, v)
		case "input":
			d.Input = parseSchema(p, "", key, v)
		case "schemas":
			m := p.obj("", key, v)
			d.Schemas = make(map[string]*Schema, len(m))
			for name, sv := range m {
				if basicTypes[name] {
					p.add("", "schemas: %q is a basic type name", name)
				}
				d.Schemas[name] = parseSchema(p, "", "schemas."+name, sv)
			}
		case "steps":
			d.Steps = parseSteps(p, key, v)
		case "on_failure":
			d.OnFailure = parseSteps(p, key, v)
			for i, s := range d.OnFailure {
				if s.Name == "" {
					s.Name = fmt.Sprintf("on_failure_%d", i+1)
				}
			}
		case "finally":
			d.Finally = parseSteps(p, key, v)
			for i, s := range d.Finally {
				if s.Name == "" {
					s.Name = fmt.Sprintf("finally_%d", i+1)
				}
			}
		case "outputs":
			d.Outputs = parseOutputs(p, v)
		default:
			p.add("", "unknown key %q", key)
		}
	}

	if err := p.err(); err != nil {
		return nil, err
	}
	return d, nil
}

// parseOutputs reads `outputs:`. An entry is a template (the short form),
// or an object with a value and optionally a schema and a description
// (the long form). An object with a `value` or `schema` key is the long
// form; a literal object with such a key is written as the long form's
// value.
func parseOutputs(p *problems, v any) map[string]*Output {
	m := p.obj("", "outputs", v)
	if m == nil {
		return nil
	}
	out := make(map[string]*Output, len(m))
	for _, name := range sortedKeys(m) {
		raw := m[name]
		o := &Output{Value: raw}
		long, ok := raw.(map[string]any)
		_, hasValue := long["value"]
		_, hasSchema := long["schema"]
		if ok && (hasValue || hasSchema) {
			at := "outputs." + name
			if !hasValue {
				p.add("", "%s: the long form needs a value", at)
			}
			o.Value = long["value"]
			for _, k := range sortedKeys(long) {
				switch k {
				case "value":
				case "schema":
					o.Schema = parseSchema(p, "", at+".schema", long[k])
				case "description":
					o.Description = p.str("", at+".description", long[k])
				default:
					p.add("", "%s: unknown key %q (the long form has value, schema and "+
						"description)", at, k)
				}
			}
		}
		out[name] = o
	}
	return out
}

// OutputSchema describes the run's outputs as one JSON Schema object, for
// a host that publishes the workflow as a tool: a property per output,
// with the output's schema (unchecked, `{}`, when it has none) and its
// description. Every output is required: a completed run's outputs have
// every declared key, and a value that does not match its schema fails the
// run. It returns nil when the definition declares no outputs; such a
// run's outputs are the values of the steps its paths ended at.
func (d *Definition) OutputSchema() *Schema {
	if d.Outputs == nil {
		return nil
	}
	s := &Schema{
		Type:          []string{"object"},
		Properties:    map[string]*Schema{},
		AdditionalSet: true,
	}
	for _, name := range sortedKeys(d.Outputs) {
		o := d.Outputs[name]
		ps := &Schema{}
		if r := o.Schema.deref(); r != nil {
			ps = new(*r)
		}
		if o.Description != "" {
			ps.Description = o.Description
		}
		s.Properties[name] = ps
		s.Required = append(s.Required, name)
	}
	return s
}

func parseSteps(p *problems, key string, v any) []*Step {
	list, ok := v.([]any)
	if !ok {
		p.add("", "%s must be a list", key)
		return nil
	}
	steps := make([]*Step, 0, len(list))
	for i, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			p.add("", "%s[%d] must be an object", key, i)
			continue
		}
		steps = append(steps, parseStep(p, fmt.Sprintf("%s[%d]", key, i), m))
	}
	return steps
}

func parseStep(p *problems, where string, m map[string]any) *Step {
	s := &Step{extra: map[string]any{}}
	if name, ok := m["name"].(string); ok {
		s.Name = name
		where = name
	}

	for _, k := range sortedKeys(m) {
		v := m[k]
		switch k {
		case "name":
			s.Name = p.str(where, k, v)
		case "description":
			s.Description = p.str(where, k, v)
		case "with":
			s.With = p.obj(where, k, v)
		case "each":
			s.Each = p.str(where, k, v)
		case "concurrency":
			s.Concurrency = p.integer(where, k, v)
		case "continue_on_error":
			s.ContinueOnError = p.boolean(where, k, v)
		case "next":
			if m, ok := v.(map[string]any); ok {
				s.Switch = parseSwitch(p, where, m)
			} else {
				s.Next = parseEdges(p, where, v)
			}
		case "output":
			s.Output = parseSchema(p, where, k, v)
		case "match":
			s.Match = p.str(where, k, v)
		case "retry":
			s.Retry = parseRetry(p, where, v)
		case "catch":
			s.Catch = parseCatch(p, where, v)
		case "timeout":
			s.Timeout = p.duration(where, k, v)
		case "max_visits":
			s.MaxVisits = p.integer(where, k, v)
		case KindJoin:
			s.control = append(s.control, k)
			s.Join = p.strList(where, k, v)
		case KindSet:
			s.control = append(s.control, k)
			s.Set = v
		case KindSleep:
			s.control = append(s.control, k)
			s.Sleep = p.duration(where, k, v)
		default:
			s.extra[k] = v
		}
	}
	return s
}

func parseEdges(p *problems, where string, v any) []Edge {
	var list []any
	switch t := v.(type) {
	case string:
		return []Edge{{Step: t}}
	case []any:
		list = t
	default:
		p.add(where, "next must be a step name or a list")
		return nil
	}

	edges := make([]Edge, 0, len(list))
	for _, raw := range list {
		switch t := raw.(type) {
		case string:
			edges = append(edges, Edge{Step: t})
		case map[string]any:
			e := Edge{}
			for k, v := range t {
				switch k {
				case "step":
					e.Step = p.str(where, "next.step", v)
				case "when":
					e.When = p.str(where, "next.when", v)
				default:
					p.add(where, "next: unknown key %q", k)
				}
			}
			edges = append(edges, e)
		default:
			p.add(where, "next entries must be step names or {step, when}")
		}
	}
	return edges
}

func parseSwitch(p *problems, where string, m map[string]any) *Switch {
	sw := &Switch{}
	targets := func(key string, v any) []string {
		switch t := v.(type) {
		case string:
			return []string{t}
		case []any:
			return p.strList(where, key, t)
		}
		p.add(where, "%s must be a step name or a list of them", key)
		return nil
	}

	for _, k := range sortedKeys(m) {
		v := m[k]
		switch k {
		case "switch":
			sw.Expr = p.str(where, "next.switch", v)
		case "cases":
			cases := p.obj(where, "next.cases", v)
			for _, value := range sortedKeys(cases) {
				sw.Cases = append(sw.Cases, Case{
					Value:   value,
					Targets: targets("next.cases."+value, cases[value]),
				})
			}
		case "default":
			sw.HasDefault = true
			sw.Default = targets("next.default", v)
		default:
			p.add(where, "next: unknown key %q (a switch has switch, cases and default)", k)
		}
	}

	if sw.Expr == "" {
		p.add(where, "next: a switch needs a switch expression")
	}
	return sw
}

func parseRetry(p *problems, where string, v any) *Retry {
	if _, ok := v.(float64); ok {
		return &Retry{MaxAttempts: p.integer(where, "retry", v)}
	}
	m := p.obj(where, "retry", v)
	r := &Retry{}
	for k, v := range m {
		switch k {
		case "max_attempts":
			r.MaxAttempts = p.integer(where, "retry.max_attempts", v)
		case "backoff":
			r.Backoff = p.duration(where, "retry.backoff", v)
		case "max_backoff":
			r.MaxBackoff = p.duration(where, "retry.max_backoff", v)
		case "factor":
			f, ok := v.(float64)
			if !ok {
				p.add(where, "retry.factor must be a number")
			}
			r.Factor = f
		case "errors":
			r.Errors = p.strList(where, "retry.errors", v)
		default:
			p.add(where, "retry: unknown key %q", k)
		}
	}
	return r
}

func parseCatch(p *problems, where string, v any) []Catch {
	if s, ok := v.(string); ok {
		return []Catch{{Next: s}}
	}
	list, ok := v.([]any)
	if !ok {
		p.add(where, "catch must be a step name or a list")
		return nil
	}
	out := make([]Catch, 0, len(list))
	for _, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			p.add(where, "catch entries must be {errors, next}")
			continue
		}
		c := Catch{}
		for k, v := range m {
			switch k {
			case "errors":
				c.Errors = p.strList(where, "catch.errors", v)
			case "next":
				c.Next = p.str(where, "catch.next", v)
			default:
				p.add(where, "catch: unknown key %q", k)
			}
		}
		out = append(out, c)
	}
	return out
}

// targets returns every step a switch can route to.
func (sw *Switch) targets() []string {
	var out []string
	for _, c := range sw.Cases {
		out = append(out, c.Targets...)
	}
	return append(out, sw.Default...)
}

// route returns the targets for a value, and whether any arm matched.
func (sw *Switch) route(v any) ([]string, bool) {
	key, ok := v.(string)
	if !ok {
		data, _ := json.Marshal(v)
		key = string(data)
	}
	for _, c := range sw.Cases {
		if c.Value == key {
			return c.Targets, true
		}
	}
	return sw.Default, sw.HasDefault
}

func (s *Step) isHostKind() bool {
	return s.Kind != KindJoin && s.Kind != KindSet && s.Kind != KindSleep
}

// backoff returns the delay before attempt+1.
func (r *Retry) backoff(attempt int) time.Duration {
	d := r.Backoff
	if d <= 0 {
		d = time.Second
	}
	maxD := r.MaxBackoff
	if maxD <= 0 {
		maxD = 5 * time.Minute
	}
	factor := r.Factor
	if factor < 1 {
		factor = 2
	}
	for i := 1; i < attempt; i++ {
		d = time.Duration(float64(d) * factor)
		if d >= maxD {
			return maxD
		}
	}
	return min(d, maxD)
}

func (r *Retry) matches(errType string) bool {
	return len(r.Errors) == 0 || slices.Contains(r.Errors, errType)
}

func (c Catch) matches(errType string) bool {
	return len(c.Errors) == 0 || slices.Contains(c.Errors, errType)
}
