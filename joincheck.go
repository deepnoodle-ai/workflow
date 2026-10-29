package workflow

// checkJoinsSettle reports a join that names a step which may not have
// settled on any path that arrives when the join fires, such as a step on
// a branch taken only when a condition holds. Without this check the run
// fails at the join.
//
// For each named step j it looks for a step d that every arriving path has
// passed through (the start step at least) such that no execution from d
// delivers a path to the join without one through j. Like the type
// checker, it treats every condition as possibly true or false, on its own:
// a path whose conditional edges all fail ends there. It treats loop back
// edges as harmless, so a join inside a loop it accepts can still fail at
// run time.
func (d *Definition) checkJoinsSettle(p *problems) {
	if d.lenientJoins {
		return
	}

	preds := d.predecessors()
	for _, s := range d.Steps {
		if s.Kind != KindJoin || len(s.Join) == 0 {
			continue
		}

		// What every arriving path has settled, before the join's own
		// guarantee about the steps it names.
		var arrived settled
		have := false
		for _, pr := range preds[s.Name] {
			pf, ok := d.facts[pr]
			if !ok {
				continue
			}
			if out := pf.with(pr); !have {
				arrived, have = out, true
			} else {
				arrived = disjoin(arrived, out)
			}
		}

		candidates := []string{d.Steps[0].Name}
		for _, c := range arrived {
			if len(c) == 1 && c[0] != candidates[0] &&
				d.byName[c[0]] != nil && !d.isFinallyStep(c[0]) {
				candidates = append(candidates, c[0])
			}
		}
		for _, j := range s.Join {
			if arrived.has(j) {
				continue
			}
			a := &joinArrival{d: d, join: s.Name, named: j, memo: map[string]arrivalFacts{}}
			ok := false
			for _, c := range candidates {
				if !a.of(c).bad {
					ok = true
					break
				}
			}
			if !ok {
				p.add(s.Name, "join: %s may not have settled when the join fires (a path can arrive "+
					"without passing through it, for example when a condition skips it); name only steps "+
					"every arriving path settles, and read the others with ?.", j)
			}
		}
	}
}

// arrivalFacts describes the executions from a step, on a path that has
// not settled the named step yet.
type arrivalFacts struct {
	bad   bool // some execution delivers a path to the join, none through the named step
	avoid bool // some execution delivers no path to the join through the named step
	never bool // some execution delivers no path to the join at all
}

type joinArrival struct {
	d     *Definition
	join  string
	named string
	memo  map[string]arrivalFacts
}

func (a *joinArrival) of(name string) arrivalFacts {
	if f, ok := a.memo[name]; ok {
		return f
	}
	// In progress: only a cycle without back edges gets here, which the
	// graph checks rule out. Assume the best.
	a.memo[name] = arrivalFacts{}
	f := a.compute(name)
	a.memo[name] = f
	return f
}

func (a *joinArrival) compute(name string) arrivalFacts {
	switch {
	case name == a.join:
		return arrivalFacts{bad: true, avoid: true}
	case !a.d.reach[name][a.join]:
		return arrivalFacts{avoid: true, never: true}
	}

	s := a.d.byName[name]
	// The ways the step can go on, each a set of steps taken together.
	var alts [][]string
	edge := func(to string) string {
		if a.d.back[[2]string{name, to}] {
			return "" // a back edge starts a new pass: assume the best
		}
		return to
	}

	switch {
	case s.Switch != nil:
		for _, c := range s.Switch.Cases {
			alts = append(alts, mapTargets(c.Targets, edge))
		}
		if s.Switch.HasDefault {
			alts = append(alts, mapTargets(s.Switch.Default, edge))
		}
	case s.Match == "first":
		matched := false
		for _, e := range s.Next {
			alts = append(alts, []string{edge(e.Step)})
			if e.When == "" {
				matched = true
				break
			}
		}
		if !matched {
			alts = append(alts, nil) // no edge matches: the path ends
		}
	case len(s.Next) > 0:
		var always, maybe []string
		for _, e := range s.Next {
			if e.When == "" {
				always = append(always, edge(e.Step))
			} else {
				maybe = append(maybe, edge(e.Step))
			}
		}
		// No condition holding is one way to go on: with only the
		// unconditional edges, or none, which ends the path.
		alts = append(alts, always)
		for _, m := range maybe {
			alts = append(alts, append(append([]string{}, always...), m))
		}
	}
	for _, c := range s.Catch {
		alts = append(alts, []string{edge(c.Next)})
	}
	if len(alts) == 0 {
		return arrivalFacts{avoid: true, never: true} // the path ends here
	}

	var out arrivalFacts
	for _, alt := range alts {
		if len(alt) == 0 {
			out.avoid, out.never = true, true // the path ends here
			continue
		}
		f := a.together(name == a.named, alt)
		out.bad = out.bad || f.bad
		out.avoid = out.avoid || f.avoid
		out.never = out.never || f.never
	}
	return out
}

// together combines the steps one way of going on takes at once. settles
// says the named step has just settled, so every path that goes on carries
// it.
func (a *joinArrival) together(settles bool, steps []string) arrivalFacts {
	all := arrivalFacts{avoid: true, never: true}
	anyBad := false
	for _, to := range steps {
		if to == "" {
			return arrivalFacts{}
		}
		f := a.of(to)
		if settles {
			f = arrivalFacts{avoid: f.never, never: f.never}
		}
		all.avoid = all.avoid && f.avoid
		all.never = all.never && f.never
		anyBad = anyBad || f.bad
	}
	all.bad = all.avoid && anyBad
	return all
}

func mapTargets(targets []string, f func(string) string) []string {
	out := make([]string, len(targets))
	for i, t := range targets {
		out[i] = f(t)
	}
	return out
}
