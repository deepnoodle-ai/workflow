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
	for _, name := range sortedKeys(d.Schemas) {
		d.Schemas[name].checkDefaults(p, "", "schemas."+name)
	}
	if d.Input != nil {
		d.Input = d.resolveSchema(p, "", d.Input)
		if in := d.Input.deref(); in != nil && (len(in.Type) != 1 || in.Type[0] != "object") {
			p.add("", "input: must be an object schema (type: object)")
		}
		d.Input.checkDefaults(p, "", "input")
	}

	// Graph analysis needs every step to be one name: it is skipped when a
	// step has no name or shares one.
	namesOK := true
	all := append(append([]*Step{}, d.Steps...), d.OnFailure...)
	for _, s := range all {
		switch {
		case s.Name == "":
			p.add("", "every step needs a name")
			namesOK = false
			continue
		case !identRE.MatchString(s.Name):
			p.add(s.Name, "name must be an identifier (letters, digits, underscore) so expressions can read it")
		case reservedName(s.Name):
			p.add(s.Name, "name is reserved for a global or function")
		}
		if d.byName[s.Name] != nil {
			p.add(s.Name, "duplicate step name")
			namesOK = false
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
		if s.Name != "" {
			main[s.Name] = true
		}
	}
	targetsOK := true
	target := func(s *Step, where, missing, name string) {
		switch {
		case name == "":
			p.add(s.Name, "%s: %s", where, missing)
			targetsOK = false
		case !main[name]:
			p.add(s.Name, "%s: unknown step %q", where, name)
			targetsOK = false
		}
	}
	// noRepeats rejects a target listed twice in one set of targets: the
	// path would fork onto the same step twice.
	noRepeats := func(s *Step, where string, targets []string) {
		seen := map[string]bool{}
		for _, t := range targets {
			if seen[t] && t != "" {
				p.add(s.Name, "%s: step %q appears twice", where, t)
			}
			seen[t] = true
		}
	}
	for _, s := range d.Steps {
		edges := make([]string, len(s.Next))
		for i, e := range s.Next {
			target(s, "next", "an edge is missing its step", e.Step)
			edges[i] = e.Step
		}
		noRepeats(s, "next", edges)
		if s.Switch != nil {
			for _, c := range s.Switch.Cases {
				for _, t := range c.Targets {
					target(s, "next", fmt.Sprintf("case %q is missing a step name", c.Value), t)
				}
				noRepeats(s, fmt.Sprintf("next: case %q", c.Value), c.Targets)
			}
			for _, t := range s.Switch.Default {
				target(s, "next", "default is missing a step name", t)
			}
			noRepeats(s, "next: default", s.Switch.Default)
		}
		for _, c := range s.Catch {
			target(s, "catch", "a route is missing its next step", c.Next)
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
				if id != "input" && !main[id] {
					p.add("", "outputs: unknown name %q", id)
				}
			}
		}
	}

	if namesOK && targetsOK && len(d.Steps) > 0 {
		d.reach = d.computeReach()
		d.computeLoops()
		d.computeArrive()
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
	s.Output.checkDefaults(p, s.Name, "output")
	s.params.checkDefaults(p, s.Name, "params")
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

// A loop is a set of steps that can all reach each other. Its heads are the
// steps a path enters it at: the start step, or a step with an edge from
// outside the loop. Its back edges are the edges from inside it to a head.
// Loops nest: what is still cyclic inside a loop once its back edges are
// removed are its inner loops. The structure depends only on the graph,
// never on the order steps or edges are listed in.
//
// A pass of a loop is what a path does between entering its heads. A path
// counts its passes by its visits to the heads (see [run.pass]).
type loop struct {
	heads []string // in step order
	steps map[string]bool
}

// computeLoops finds every loop, outermost first, and the back edges of
// each. Removing all back edges leaves the graph acyclic.
func (d *Definition) computeLoops() {
	preds := d.predecessors()
	start := d.Steps[0].Name
	d.back = map[[2]string]bool{}
	d.loopsOf = map[string][]*loop{}
	var split func(nodes []string)
	split = func(nodes []string) {
		for _, scc := range d.cyclicComponents(nodes) {
			in := map[string]bool{}
			for _, n := range scc {
				in[n] = true
			}
			l := &loop{steps: in}
			for _, n := range scc {
				entry := n == start
				for _, p := range preds[n] {
					entry = entry || !in[p]
				}
				if entry {
					l.heads = append(l.heads, n)
				}
			}
			if len(l.heads) == 0 {
				// Unreachable from the start step, which checkGraph
				// reports; any head keeps the analysis finite.
				l.heads = scc[:1]
			}
			for _, n := range scc {
				for _, m := range d.byName[n].successors() {
					if in[m] && contains(l.heads, m) {
						d.back[[2]string{n, m}] = true
					}
				}
				d.loopsOf[n] = append(d.loopsOf[n], l)
			}
			split(scc)
		}
	}
	names := make([]string, len(d.Steps))
	for i, s := range d.Steps {
		names[i] = s.Name
	}
	split(names)
}

// predecessors maps each step to the steps with an edge to it.
func (d *Definition) predecessors() map[string][]string {
	preds := map[string][]string{}
	for _, s := range d.Steps {
		for _, m := range s.successors() {
			if !contains(preds[m], s.Name) {
				preds[m] = append(preds[m], s.Name)
			}
		}
	}
	return preds
}

// cyclicComponents returns the strongly connected components of the graph
// on nodes, without back edges found so far, that contain a cycle. Each is
// in step order.
func (d *Definition) cyclicComponents(nodes []string) [][]string {
	in := map[string]bool{}
	for _, n := range nodes {
		in[n] = true
	}
	succ := func(n string) []string {
		var out []string
		for _, m := range d.byName[n].successors() {
			if in[m] && !d.back[[2]string{n, m}] {
				out = append(out, m)
			}
		}
		return out
	}
	// Tarjan's algorithm.
	index, low := map[string]int{}, map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var out [][]string
	var visit func(string)
	visit = func(n string) {
		index[n] = len(index) + 1
		low[n] = index[n]
		stack = append(stack, n)
		onStack[n] = true
		for _, m := range succ(n) {
			if index[m] == 0 {
				visit(m)
				low[n] = min(low[n], low[m])
			} else if onStack[m] {
				low[n] = min(low[n], index[m])
			}
		}
		if low[n] != index[n] {
			return
		}
		var scc []string
		for {
			m := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[m] = false
			scc = append(scc, m)
			if m == n {
				break
			}
		}
		if len(scc) > 1 || contains(succ(n), n) {
			out = append(out, scc)
		}
	}
	for _, n := range nodes {
		if index[n] == 0 {
			visit(n)
		}
	}
	order := map[string]int{}
	for i, s := range d.Steps {
		order[s.Name] = i
	}
	for _, scc := range out {
		sort.Slice(scc, func(i, j int) bool { return order[scc[i]] < order[scc[j]] })
	}
	return out
}

// computeArrive works out, for every join and every number i of its loops,
// outermost first, the steps from which a path can reach it without
// entering a head of those loops: without starting a new pass of them.
// Arriving at the join itself, even over a back edge, is fine; the join's
// own visits count its firings.
func (d *Definition) computeArrive() {
	preds := d.predecessors()
	d.arrive = map[string][]map[string]bool{}
	for _, s := range d.Steps {
		if s.Kind != KindJoin {
			continue
		}
		loops := d.loopsOf[s.Name]
		for i := 0; i <= len(loops); i++ {
			d.arrive[s.Name] = append(d.arrive[s.Name], d.reachesWithin(preds, s.Name, loops[:i]))
		}
	}
}

// reachesWithin returns the steps that can reach target without entering a
// head (other than target) of the given loops.
func (d *Definition) reachesWithin(preds map[string][]string, target string, loops []*loop) map[string]bool {
	heads := map[string]bool{}
	for _, l := range loops {
		for _, h := range l.heads {
			heads[h] = h != target
		}
	}
	seen := map[string]bool{}
	queue := []string{target}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if heads[n] {
			continue // entering n starts a new pass
		}
		for _, p := range preds[n] {
			if !seen[p] {
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	return seen
}

// commonLoops returns how many loops, outermost first, contain both steps.
func (d *Definition) commonLoops(a, b string) int {
	la, lb := d.loopsOf[a], d.loopsOf[b]
	n := 0
	for n < len(la) && n < len(lb) && la[n] == lb[n] {
		n++
	}
	return n
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
		for _, l := range d.loopsOf[s.Name] {
			if len(l.heads) > 1 {
				p.add(s.Name, "join is in a loop entered at more than one step (%s); "+
					"a loop with a join needs one entry step, so each pass starts in the same place",
					strings.Join(l.heads, ", "))
			}
		}
		preds := d.predecessors()
		for _, j := range s.Join {
			switch {
			case j == s.Name || !d.reach[j][s.Name]:
				p.add(s.Name, "join: step %s is not upstream of this join", j)
			case !d.reachesWithin(preds, s.Name, d.loopsOf[s.Name][:d.commonLoops(j, s.Name)])[j]:
				// Every route from j to the join starts a new pass of a
				// loop they are both in, so j never settles in the pass
				// the join fires in.
				p.add(s.Name, "join: step %s is only upstream of this join through the head of a loop; "+
					"it never settles in the same pass", j)
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
// set when it runs: input, item and index inside each, and steps
// upstream of the reader.
func (d *Definition) checkReads(p *problems) map[string]bool {
	skip := map[string]bool{}
	for _, s := range d.Steps {
		before := len(p.list)
		upstream := func(name string) bool { return d.reach[name][s.Name] }
		check := func(what string, ids []string, self, eachVars bool) {
			for _, id := range ids {
				switch {
				case id == "input":
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
				if id != "input" && id != "error" && d.byName[id] == nil {
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
