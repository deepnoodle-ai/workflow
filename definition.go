package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Definition is a parsed workflow. Build one with [Parse] or [ParseMap], then
// call [Definition.Validate] before passing it to [Apply].
//
// A Definition is immutable after validation and safe for concurrent use.
type Definition struct {
	Name        string
	Description string
	Inputs      map[string]*Input
	Steps       []*Step
	OnFailure   []*Step
	Outputs     map[string]any

	// Limits bounds what a run may keep in its state. Hosts set it after
	// parsing; it is not part of the definition document. Zero values use
	// the defaults.
	Limits Limits

	raw       map[string]any
	hash      string
	validated bool
	byName    map[string]*Step
	reach     map[string]map[string]bool // reach[a][b]: b is reachable from a
	// forward is reach without loop back edges: where a path can get in
	// the current pass of any loop it is in.
	forward map[string]map[string]bool
	outputs *template
}

// Limits bounds the size of a run's state. Outputs are kept in state because
// expressions read them, so a step whose output is too large fails with
// [ErrorTypeOutputTooLarge] instead of bloating every later transaction.
type Limits struct {
	// MaxOutputBytes caps one step output, as JSON. Default 256 KiB.
	MaxOutputBytes int
	// MaxStateBytes caps the sum of all outputs and each items a run keeps.
	// Default 8 MiB.
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

// Input declares one workflow input.
type Input struct {
	// Type is one of string, number, integer, boolean, object, array, or
	// empty for any.
	Type        string
	Description string
	Default     any
	Required    bool
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

	// Kind and Config are resolved by Validate.
	Kind   string
	Config map[string]any

	With            map[string]any
	Each            string
	Concurrency     int
	ContinueOnError bool

	// Join lists the steps a join step waits for. Empty means every path
	// that can still reach the join.
	Join []string
	// Set is the value a set step produces. Strings may hold ${...}
	// templates.
	Set any
	// Sleep is how long a sleep step waits.
	Sleep time.Duration

	Next      []Edge
	Match     string // "all" (default) or "first"
	Retry     *Retry
	Catch     []Catch
	Timeout   time.Duration
	MaxVisits int

	extra   map[string]any
	control []string // control-kind keys present on the step

	each  *program
	with  *template
	set   *template
	edges []*program
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
	"match": true, "retry": true, "catch": true, "timeout": true,
	"max_visits": true, KindJoin: true, KindSet: true, KindSleep: true,
}

// Parse reads a definition from JSON. Hosts that author in YAML decode it
// with their YAML library and call [ParseMap].
func Parse(data []byte) (*Definition, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("workflow: parse: %w", err)
	}
	return parseMap(m)
}

// ParseMap reads a definition from a decoded document, such as the output of
// a YAML decoder. The map is normalized through JSON first.
func ParseMap(doc map[string]any) (*Definition, error) {
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("workflow: parse: %w", err)
	}
	return Parse(data)
}

// Hash identifies the definition's content. A [State] records the hash of
// the definition it started with, and Apply refuses a different one.
func (d *Definition) Hash() string { return d.hash }

// MarshalJSON returns the document the definition was parsed from.
func (d *Definition) MarshalJSON() ([]byte, error) { return json.Marshal(d.raw) }

// Step returns the step with the given name, including on_failure steps.
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
		case "inputs":
			d.Inputs = parseInputs(p, v)
		case "steps":
			d.Steps = parseSteps(p, key, v)
		case "on_failure":
			d.OnFailure = parseSteps(p, key, v)
			for i, s := range d.OnFailure {
				if s.Name == "" {
					s.Name = fmt.Sprintf("on_failure_%d", i+1)
				}
			}
		case "outputs":
			d.Outputs = p.obj("", key, v)
		default:
			p.add("", "unknown key %q", key)
		}
	}
	if err := p.err(); err != nil {
		return nil, err
	}
	return d, nil
}

func parseInputs(p *problems, v any) map[string]*Input {
	m := p.obj("", "inputs", v)
	out := make(map[string]*Input, len(m))
	for name, raw := range m {
		in := &Input{}
		spec, ok := raw.(map[string]any)
		if !ok {
			p.add("", "input %q must be an object", name)
			continue
		}
		for k, v := range spec {
			switch k {
			case "type":
				in.Type = p.str("", "inputs."+name+".type", v)
			case "description":
				in.Description = p.str("", "inputs."+name+".description", v)
			case "default":
				in.Default = v
			case "required":
				in.Required = p.boolean("", "inputs."+name+".required", v)
			default:
				p.add("", "input %q: unknown key %q", name, k)
			}
		}
		out[name] = in
	}
	return out
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
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
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
			s.Next = parseEdges(p, where, v)
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

func parseRetry(p *problems, where string, v any) *Retry {
	if n, ok := v.(float64); ok {
		return &Retry{MaxAttempts: int(n)}
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
	return len(r.Errors) == 0 || contains(r.Errors, errType)
}

func (c Catch) matches(errType string) bool {
	return len(c.Errors) == 0 || contains(c.Errors, errType)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
