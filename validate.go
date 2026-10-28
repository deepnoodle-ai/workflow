package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validate checks the definition against the host's step kinds, resolves
// each step's kind and compiles every expression. See [Load].
func (d *Definition) validate(kinds Kinds) error {
	p := &problems{}
	d.byName = map[string]*Step{}
	if len(d.Steps) == 0 {
		p.add("", "steps: a workflow needs at least one step")
	}
	d.resolveNamedSchemas(p)
	d.EventSchema = d.resolveSchema(p, "", d.EventSchema)
	for name, in := range d.Inputs {
		if !identRE.MatchString(name) {
			p.add("", "input %q: names must be identifiers", name)
		}
		in.Schema = d.resolveSchema(p, "", in.Schema)
	}
	for _, name := range sortedKeys(d.Inputs) {
		if in := d.Inputs[name]; in.Default != nil {
			for _, msg := range in.Schema.validate(in.Default) {
				p.add("", "input %q: default: %s", name, strings.TrimPrefix(msg, "value: "))
			}
		}
	}

	all := append(append([]*Step{}, d.Steps...), d.OnFailure...)
	for _, s := range all {
		switch {
		case s.Name == "":
			p.add("", "every step needs a name")
			continue
		case !identRE.MatchString(s.Name):
			p.add(s.Name, "name must be an identifier (letters, digits, underscore) so expressions can read it")
		case reservedName(s.Name):
			p.add(s.Name, "name is reserved for a global or function")
		}
		if d.byName[s.Name] != nil {
			p.add(s.Name, "duplicate step name")
			continue
		}
		d.byName[s.Name] = s
	}
	for _, s := range d.Steps {
		d.resolveKind(p, s, kinds, false)
	}
	for _, s := range d.OnFailure {
		d.resolveKind(p, s, kinds, true)
	}

	main := map[string]bool{}
	for _, s := range d.Steps {
		main[s.Name] = true
	}
	targetsOK := true
	for _, s := range d.Steps {
		seen := map[string]bool{}
		for _, e := range s.Next {
			if !main[e.Step] {
				p.add(s.Name, "next: unknown step %q", e.Step)
				targetsOK = false
			}
			if seen[e.Step] {
				p.add(s.Name, "next: step %q appears twice", e.Step)
			}
			seen[e.Step] = true
		}
		if s.Switch != nil {
			for _, target := range s.Switch.targets() {
				if !main[target] {
					p.add(s.Name, "next: unknown step %q", target)
					targetsOK = false
				}
			}
		}
		for _, c := range s.Catch {
			if !main[c.Next] {
				p.add(s.Name, "catch: unknown step %q", c.Next)
				targetsOK = false
			}
		}
		for _, j := range s.Join {
			if !main[j] {
				p.add(s.Name, "join: unknown step %q", j)
				targetsOK = false
			}
		}
	}

	if d.Outputs != nil {
		t, err := compileTemplate(map[string]any(d.Outputs))
		if err != nil {
			p.add("", "outputs: %v", err)
		} else {
			d.outputs = t
			for _, id := range t.idents() {
				if id != "inputs" && id != "event" && !main[id] {
					p.add("", "outputs: unknown name %q", id)
				}
			}
		}
	}

	if targetsOK && len(d.Steps) > 0 {
		d.reach = d.computeReach()
		d.forward = d.computeForward()
		d.checkGraph(p)
		skip := d.checkReads(p)
		d.typecheck(p, skip)
	}

	if err := p.err(); err != nil {
		return err
	}
	d.validated = true
	return nil
}

func (d *Definition) resolveKind(p *problems, s *Step, kinds Kinds, failure bool) {
	var host []string
	for k := range s.extra {
		if kinds != nil && kinds.Known(k) {
			host = append(host, k)
		}
	}
	sort.Strings(host)
	switch n := len(s.control) + len(host); {
	case n == 0:
		p.add(s.Name, "no step kind: expected one of the host's kinds or join, set, sleep (keys: %s)",
			strings.Join(sortedKeys(s.extra), ", "))
		return
	case n > 1:
		p.add(s.Name, "more than one step kind: %s", strings.Join(append(append([]string{}, s.control...), host...), ", "))
		return
	case len(s.control) == 1:
		s.Kind = s.control[0]
		if len(s.extra) > 0 {
			p.add(s.Name, "unknown keys for a %s step: %s", s.Kind, strings.Join(sortedKeys(s.extra), ", "))
		}
	default:
		s.Kind = host[0]
		s.Config = make(map[string]any, len(s.extra))
		for k, v := range s.extra {
			s.Config[k] = v
		}
		if err := kinds.ValidateConfig(s.Kind, s.Config); err != nil {
			p.add(s.Name, "%s config: %v", s.Kind, err)
		}
		if ks, ok := kinds.(KindSchemas); ok {
			params, output, err := ks.Schemas(s.Kind, s.Config)
			if err != nil {
				p.add(s.Name, "%s schemas: %v", s.Kind, err)
			}
			s.params = params
			if s.Output == nil {
				s.Output = output
			}
		}
	}
	s.Output = d.resolveSchema(p, s.Name, s.Output)
	s.params = d.resolveSchema(p, s.Name, s.params)
	if s.Output != nil && !s.isHostKind() {
		p.add(s.Name, "output is not allowed on a %s step; its type comes from its value", s.Kind)
	}

	if !s.isHostKind() {
		for key, set := range map[string]bool{
			"with": s.With != nil, "each": s.Each != "", "retry": s.Retry != nil,
			"catch": s.Catch != nil, "timeout": s.Timeout != 0,
		} {
			if set {
				p.add(s.Name, "%s is not allowed on a %s step", key, s.Kind)
			}
		}
	}
	if s.Kind == KindSleep && s.Sleep <= 0 {
		p.add(s.Name, "sleep must be a positive duration")
	}
	if s.Each == "" && (s.Concurrency != 0 || s.ContinueOnError) {
		p.add(s.Name, "concurrency and continue_on_error need each")
	}
	if s.Concurrency < 0 {
		p.add(s.Name, "concurrency must not be negative")
	}
	if s.MaxVisits < 0 {
		p.add(s.Name, "max_visits must not be negative")
	}
	if s.Timeout < 0 {
		p.add(s.Name, "timeout must not be negative")
	}
	if s.Match != "" && s.Match != "all" && s.Match != "first" {
		p.add(s.Name, "match must be all or first")
	}
	if s.Switch != nil && s.Match != "" {
		p.add(s.Name, "match does not apply to a switch")
	}
	if r := s.Retry; r != nil {
		if r.MaxAttempts < 1 {
			p.add(s.Name, "retry.max_attempts must be at least 1")
		}
		if r.Backoff < 0 || r.MaxBackoff < 0 {
			p.add(s.Name, "retry backoff must not be negative")
		}
		if r.Factor != 0 && r.Factor < 1 {
			p.add(s.Name, "retry.factor must be at least 1")
		}
	}
	if failure {
		if s.Kind == KindJoin || s.Kind == KindSleep {
			p.add(s.Name, "on_failure steps cannot be %s steps", s.Kind)
		}
		if s.Next != nil || s.Switch != nil || s.Catch != nil || s.Each != "" {
			p.add(s.Name, "on_failure steps run in order and cannot use next, catch or each")
		}
	}

	if s.Each != "" {
		prog, err := compileProgram(s.Each)
		if err != nil {
			p.add(s.Name, "each: %v", err)
		}
		s.each = prog
	}
	if s.With != nil {
		t, err := compileTemplate(map[string]any(s.With))
		if err != nil {
			p.add(s.Name, "with: %v", err)
		}
		s.with = t
	}
	if s.Kind == KindSet {
		t, err := compileTemplate(s.Set)
		if err != nil {
			p.add(s.Name, "set: %v", err)
		}
		s.set = t
	}
	if s.Switch != nil && s.Switch.Expr != "" {
		prog, err := compileProgram(s.Switch.Expr)
		if err != nil {
			p.add(s.Name, "next: switch: %v", err)
		}
		s.swtch = prog
	}
	s.edges = make([]*program, len(s.Next))
	for i, e := range s.Next {
		if e.When == "" {
			continue
		}
		prog, err := compileProgram(e.When)
		if err != nil {
			p.add(s.Name, "next %s: when: %v", e.Step, err)
		}
		s.edges[i] = prog
	}
}

// successors returns the steps a step can move to: its edges and catch
// routes.
func (s *Step) successors() []string {
	out := s.targets()
	for _, c := range s.Catch {
		out = append(out, c.Next)
	}
	return out
}

// targets returns the steps a step can move to when it completes.
func (s *Step) targets() []string {
	out := make([]string, 0, len(s.Next)+len(s.Catch))
	for _, e := range s.Next {
		out = append(out, e.Step)
	}
	if s.Switch != nil {
		out = append(out, s.Switch.targets()...)
	}
	return out
}

func (d *Definition) computeReach() map[string]map[string]bool {
	reach := make(map[string]map[string]bool, len(d.Steps))
	for _, s := range d.Steps {
		seen := map[string]bool{}
		queue := s.successors()
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			if seen[n] {
				continue
			}
			seen[n] = true
			queue = append(queue, d.byName[n].successors()...)
		}
		reach[s.Name] = seen
	}
	return reach
}

// computeForward is reachability over the graph with loop back edges
// removed. A back edge is one a depth-first walk from the start step finds
// pointing at a step still on its stack; the walk follows steps and edges in
// declaration order, so the result is deterministic.
func (d *Definition) computeForward() map[string]map[string]bool {
	back := map[[2]string]bool{}
	state := map[string]int{} // 0 unseen, 1 on stack, 2 done
	var walk func(string)
	walk = func(n string) {
		state[n] = 1
		for _, m := range d.byName[n].successors() {
			switch state[m] {
			case 0:
				walk(m)
			case 1:
				back[[2]string{n, m}] = true
			}
		}
		state[n] = 2
	}
	for _, s := range d.Steps {
		if state[s.Name] == 0 {
			walk(s.Name)
		}
	}
	d.back = back
	fwd := make(map[string]map[string]bool, len(d.Steps))
	for _, s := range d.Steps {
		seen := map[string]bool{}
		queue := []string{s.Name}
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			for _, m := range d.byName[n].successors() {
				if back[[2]string{n, m}] || seen[m] {
					continue
				}
				seen[m] = true
				queue = append(queue, m)
			}
		}
		fwd[s.Name] = seen
	}
	return fwd
}

func (d *Definition) checkGraph(p *problems) {
	start := d.Steps[0].Name
	for _, s := range d.Steps[1:] {
		if !d.reach[start][s.Name] {
			p.add(s.Name, "unreachable from the start step %s", start)
		}
	}
	for _, s := range d.Steps {
		if s.Kind != KindJoin {
			continue
		}
		for _, j := range s.Join {
			if j == s.Name || !d.reach[j][s.Name] {
				p.add(s.Name, "join: step %s is not upstream of this join", j)
			}
		}
	}
	for _, scc := range d.cycles() {
		bounded := false
		for _, name := range scc {
			s := d.byName[name]
			if s.MaxVisits > 0 || s.Kind == KindSleep {
				bounded = true
				break
			}
		}
		if !bounded {
			p.add(scc[0], "cycle %s has no bound: give one of its steps max_visits or a sleep",
				strings.Join(scc, " -> "))
		}
	}
}

// cycles returns the strongly connected components that contain a cycle,
// each sorted by step order.
func (d *Definition) cycles() [][]string {
	order := map[string]int{}
	for i, s := range d.Steps {
		order[s.Name] = i
	}
	var out [][]string
	done := map[string]bool{}
	for _, s := range d.Steps {
		if done[s.Name] {
			continue
		}
		var scc []string
		for _, t := range d.Steps {
			if t.Name == s.Name || (d.reach[s.Name][t.Name] && d.reach[t.Name][s.Name]) {
				scc = append(scc, t.Name)
				done[t.Name] = true
			}
		}
		if len(scc) > 1 || d.reach[s.Name][s.Name] {
			sort.Slice(scc, func(i, j int) bool { return order[scc[i]] < order[scc[j]] })
			out = append(out, scc)
		}
	}
	return out
}

// checkReads verifies that every expression reads only names that can be
// set when it runs: inputs, event, item and index inside each, and steps
// upstream of the reader.
func (d *Definition) checkReads(p *problems) map[string]bool {
	skip := map[string]bool{}
	for _, s := range d.Steps {
		before := len(p.list)
		upstream := func(name string) bool { return d.reach[name][s.Name] }
		check := func(what string, ids []string, self, eachVars bool) {
			for _, id := range ids {
				switch {
				case id == "inputs" || id == "event":
				case (id == "item" || id == "index") && eachVars:
				case d.byName[id] == nil || d.isFailureStep(id):
					p.add(s.Name, "%s reads unknown name %q", what, id)
				case id == s.Name && self:
				case !upstream(id):
					p.add(s.Name, "%s reads %s, which cannot have settled when this step runs", what, id)
				}
			}
		}
		if s.each != nil {
			check("each", s.each.p.Identifiers(), false, false)
		}
		if s.with != nil {
			check("with", s.with.idents(), false, s.Each != "")
		}
		if s.set != nil {
			check("set", s.set.idents(), false, false)
		}
		for i, prog := range s.edges {
			if prog != nil {
				check(fmt.Sprintf("next %s: when", s.Next[i].Step), prog.p.Identifiers(), true, false)
			}
		}
		if s.swtch != nil {
			check("next: switch", s.swtch.p.Identifiers(), true, false)
		}
		if len(p.list) > before {
			skip[s.Name] = true
		}
	}
	for _, s := range d.OnFailure {
		check := func(what string, ids []string) {
			for _, id := range ids {
				if id != "inputs" && id != "event" && id != "error" && d.byName[id] == nil {
					p.add(s.Name, "%s reads unknown name %q", what, id)
				}
			}
		}
		if s.with != nil {
			check("with", s.with.idents())
		}
		if s.set != nil {
			check("set", s.set.idents())
		}
	}
	return skip
}

func (d *Definition) isFailureStep(name string) bool {
	for _, s := range d.OnFailure {
		if s.Name == name {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
