package workflow

import (
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"slices"
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
		d.Input = d.resolveSchema(p, "", "input", d.Input)
		if in := d.Input.deref(); in != nil && (len(in.Type) != 1 || in.Type[0] != "object") {
			p.add("", "input: must be an object schema (type: object)")
		}
		d.Input.checkDefaults(p, "", "input")
	}
	d.requiredOutputs = d.resolveSchema(p, "", "required outputs", d.requiredOutputs)

	// Graph analysis needs every step to be one name: it is skipped when a
	// step has no name or shares one.
	namesOK := true
	all := append(append(append([]*Step{}, d.Steps...), d.OnFailure...), d.Finally...)
	for _, s := range all {
		switch {
		case s.Name == "":
			p.add("", "every step needs a name")
			namesOK = false
			continue
		case !identRE.MatchString(s.Name):
			p.add(s.Name,
				"name must be an identifier (letters, digits, underscore) so expressions can read it")
		case reservedGlobal(s.Name):
			p.add(s.Name, "name is reserved: it is an expression global or keyword (input, item, index, error, run, it, true, false, nil); choose another name")
		case reservedName(s.Name):
			p.add(s.Name, "name is reserved: it is an expression function, so expressions could not read the step; choose another name")
		}
		if d.byName[s.Name] != nil {
			p.add(s.Name, "duplicate step name")
			namesOK = false
			continue
		}
		d.byName[s.Name] = s
	}

	for _, s := range d.Steps {
		d.resolveKind(p, s, kinds, "")
	}
	for _, s := range d.OnFailure {
		d.resolveKind(p, s, kinds, "on_failure")
	}
	for _, s := range d.Finally {
		d.resolveKind(p, s, kinds, "finally")
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
		values := make(map[string]any, len(d.Outputs))
		for _, name := range sortedKeys(d.Outputs) {
			o := d.Outputs[name]
			values[name] = o.Value
			o.Schema = d.resolveSchema(p, "", "outputs."+name+".schema", o.Schema)
			o.Schema.checkDefaults(p, "", "outputs."+name+".schema")
		}
		t, err := compileTemplate(values)
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
		d.computeNamed()
		d.computeLoops()
		d.computeArrive()
		d.checkGraph(p)
		skip := d.checkReads(p)
		d.typecheck(p, skip)
		d.checkJoinsSettle(p)
	}

	if err := p.err(); err != nil {
		return err
	}
	d.validated = true
	return nil
}

// resolveKind resolves a step's kind and checks its keys. phase is
// "on_failure" or "finally" for those lists' steps, which run in order.
func (d *Definition) resolveKind(p *problems, s *Step, kinds Kinds, phase string) {
	var host []string
	for k := range s.extra {
		if kinds != nil && kinds.Known(k) {
			host = append(host, k)
		}
	}
	slices.Sort(host)

	switch n := len(s.control) + len(host); {
	case n == 0:
		p.add(s.Name, "no step kind: expected one of the host's kinds or join, set, sleep (keys: %s)",
			strings.Join(sortedKeys(s.extra), ", "))
		return
	case n > 1:
		p.add(s.Name, "more than one step kind: %s",
			strings.Join(append(append([]string{}, s.control...), host...), ", "))
		return
	case len(s.control) == 1:
		s.Kind = s.control[0]
		if len(s.extra) > 0 {
			p.add(s.Name, "unknown keys for a %s step: %s", s.Kind, strings.Join(sortedKeys(s.extra), ", "))
		}
	case host[0] == KindDecision:
		p.add(s.Name, "kind %q is reserved for decide and choose; give the host's kind another name",
			KindDecision)
		return
	default:
		s.Kind = host[0]
		s.Config = make(map[string]any, len(s.extra))
		maps.Copy(s.Config, s.extra)
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

	s.Output = d.resolveSchema(p, s.Name, "output", s.Output)
	s.params = d.resolveSchema(p, s.Name, "params schema", s.params)
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

	if s.Match == "" {
		// [{step: b, when: x}, {step: c}] reads as if/else, but without
		// match: first both edges are followed when x holds.
		conditional := ""
		for _, e := range s.Next {
			if e.When != "" && conditional == "" {
				conditional = e.Step
			} else if e.When == "" && conditional != "" {
				p.add(s.Name, "next: the edge to %s has no when and follows the conditional edge to %s, "+
					"so it is always followed, not an else; set match: first for if/else, or match: all "+
					"to follow both", e.Step, conditional)
				break
			}
		}
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
	if phase != "" {
		if s.Kind == KindJoin || s.Kind == KindSleep {
			p.add(s.Name, "%s steps cannot be %s steps", phase, s.Kind)
		}
		if s.Next != nil || s.Switch != nil || s.Catch != nil || s.Each != "" {
			p.add(s.Name, "%s steps run in order and cannot use next, catch or each", phase)
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
	s.compileDecisions(p)
	if len(s.decisions) > 0 && (kinds == nil || !kinds.Known(KindDecision)) {
		p.add(s.Name, "next: %s needs the decision kind; register a decision handler with the host",
			s.decisions[0].fn)
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

// computeNamed finds, for each step with more than one target, the targets
// whose branch a join names on the way back to the step: before the branch
// returns to the step, it reaches a join that can reach the step and that
// names a step on the branch. Load checks that a named step settles on an
// arriving path whenever the join fires, except across loop back edges
// (see checkJoinsSettle), so that branch is the one that usually comes back
// through it. A fork stays on such a branch (see carrier).
func (d *Definition) computeNamed() {
	d.named = map[[2]string]bool{}
	for _, s := range d.Steps {
		targets := s.targets()
		if len(targets) < 2 {
			continue
		}
		for _, t := range targets {
			branch := map[string]bool{}
			queue := []string{t}
			for len(queue) > 0 {
				n := queue[0]
				queue = queue[1:]
				if n == s.Name || branch[n] {
					continue
				}
				branch[n] = true
				queue = append(queue, d.byName[n].successors()...)
			}
			for n := range branch {
				j := d.byName[n]
				if j.Kind != KindJoin || !d.reach[n][s.Name] {
					continue
				}
				for _, name := range j.Join {
					if branch[name] {
						d.named[[2]string{s.Name, t}] = true
					}
				}
			}
		}
	}
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

	// d.loops lists a loop before those downstream of it (topoLoops) and
	// before those inside it, which split finds next.
	var split func(nodes []string)
	split = func(nodes []string) {
		sccs := d.topoLoops(nodes, d.cyclicComponents(nodes))
		for _, scc := range sccs {
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
					if in[m] && slices.Contains(l.heads, m) {
						d.back[[2]string{n, m}] = true
					}
				}
				d.loopsOf[n] = append(d.loopsOf[n], l)
			}
			d.loops = append(d.loops, l)
			split(scc)
		}
	}

	names := make([]string, len(d.Steps))
	for i, s := range d.Steps {
		names[i] = s.Name
	}
	split(names)
}

// topoLoops orders the loops cyclicComponents found among nodes so that
// each comes before those it can reach, over the same edges. Loops neither
// reaches are ordered by their first step name, so the order does not
// depend on the order of the steps in the document.
func (d *Definition) topoLoops(nodes []string, sccs [][]string) [][]string {
	in := map[string]bool{}
	for _, n := range nodes {
		in[n] = true
	}
	comp := map[string]int{}
	for i, scc := range sccs {
		for _, n := range scc {
			comp[n] = i
		}
	}

	// reaches[i][j]: loop j is downstream of loop i.
	reaches := make([]map[int]bool, len(sccs))
	for i, scc := range sccs {
		reaches[i] = map[int]bool{}
		seen := map[string]bool{}
		queue := slices.Clone(scc)
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			for _, m := range d.byName[n].successors() {
				if !in[m] || d.back[[2]string{n, m}] || seen[m] {
					continue
				}
				seen[m] = true
				queue = append(queue, m)
				if j, ok := comp[m]; ok && j != i {
					reaches[i][j] = true
				}
			}
		}
	}

	name := func(i int) string { return slices.Min(sccs[i]) }
	placed := make([]bool, len(sccs))
	out := make([][]string, 0, len(sccs))
	for len(out) < len(sccs) {
		next := -1
		for j := range sccs {
			if placed[j] {
				continue
			}
			ready := true
			for i := range sccs {
				if !placed[i] && i != j && reaches[i][j] {
					ready = false
				}
			}
			if ready && (next < 0 || name(j) < name(next)) {
				next = j
			}
		}
		placed[next] = true
		out = append(out, sccs[next])
	}
	return out
}

// predecessors maps each step to the steps with an edge to it.
func (d *Definition) predecessors() map[string][]string {
	preds := map[string][]string{}
	for _, s := range d.Steps {
		for _, m := range s.successors() {
			if !slices.Contains(preds[m], s.Name) {
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
		if len(scc) > 1 || slices.Contains(succ(n), n) {
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
		slices.SortFunc(scc, func(a, b string) int { return cmp.Compare(order[a], order[b]) })
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
func (d *Definition) reachesWithin(
	preds map[string][]string, target string, loops []*loop,
) map[string]bool {
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
					"a loop with a join needs one entry step, so each pass starts in the "+
					"same place",
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
			slices.SortFunc(scc, func(a, b string) int { return cmp.Compare(order[a], order[b]) })
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
				case d.byName[id] == nil || d.isFailureStep(id) || d.isFinallyStep(id):
					p.add(s.Name, "%s reads unknown name %q", what, id)
				case id == s.Name && self:
				case !upstream(id):
					p.add(s.Name, "%s reads %s, which cannot have settled when this step runs",
						what, id)
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
				what := fmt.Sprintf("next %s: when", s.Next[i].Step)
				check(what, prog.p.Identifiers(), true, false)
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
		before := len(p.list)
		check := func(what string, ids []string) {
			for _, id := range ids {
				if id != "input" && id != "error" && (d.byName[id] == nil || d.isFinallyStep(id)) {
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
		if len(p.list) > before {
			skip[s.Name] = true
		}
	}

	// Finally steps read the outcome as run, any step of steps and
	// on_failure, and the finally steps before them.
	for i, s := range d.Finally {
		before := len(p.list)
		check := func(what string, ids []string) {
			for _, id := range ids {
				switch {
				case id == "input" || id == "run":
				case id == "error":
					p.add(s.Name,
						"%s reads error, which finally steps do not have; the run's error is run.error", what)
				case d.byName[id] == nil:
					p.add(s.Name, "%s reads unknown name %q", what, id)
				case d.isFinallyStep(id) && !d.finallyBefore(id, i):
					p.add(s.Name, "%s reads %s, which has not run when this step runs", what, id)
				}
			}
		}
		if s.with != nil {
			check("with", s.with.idents())
		}
		if s.set != nil {
			check("set", s.set.idents())
		}
		if len(p.list) > before {
			skip[s.Name] = true
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

func (d *Definition) isFinallyStep(name string) bool {
	return d.finallyBefore(name, len(d.Finally))
}

// finallyBefore reports whether name is one of the first n finally steps.
func (d *Definition) finallyBefore(name string, n int) bool {
	for _, s := range d.Finally[:n] {
		if s.Name == name {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
