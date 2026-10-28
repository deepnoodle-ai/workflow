package workflow

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"
)

const (
	mainPath    = "main"
	failurePath = "failure"
)

// Apply advances a run by one event and returns the new state and the
// commands the host must carry out.
//
// Apply is pure and deterministic: it does no I/O, starts no goroutines and
// reads no clock except ev.At. It never modifies st. The same state and
// event always give the same result, and an event for a step that has
// already settled returns the state unchanged with no commands, so hosts may
// deliver events at least once.
//
// Apply returns an error only when the host sent something it should not
// have: an unvalidated definition, a state from another definition, an
// unknown StepID, or inputs that do not match the definition. The run's own
// failures are RunFailed commands.
func Apply(def *Definition, st *State, ev Event) (*State, []Command, error) {
	if def == nil || !def.validated {
		return st, nil, ErrNotValidated
	}
	if ev.At.IsZero() {
		return st, nil, fmt.Errorf("%w: At is required", ErrInvalidEvent)
	}
	started := st != nil && st.Status != ""
	if !started && ev.Kind != EventStart {
		return st, nil, ErrNotStarted
	}
	if started && st.Definition != def.hash {
		return st, nil, ErrDefinitionMismatch
	}

	// Time never runs backwards within a run: an event stamped earlier
	// than the last one applied is applied at that time, so settle times
	// follow entry times and timers are never scheduled early.
	at := ev.At.UTC()
	if started && st.UpdatedAt.After(at) {
		at = st.UpdatedAt
	}
	r := &run{def: def, at: at}
	switch ev.Kind {
	case EventStart:
		if started {
			return st, nil, nil
		}
		inputs, err := def.checkInputs(ev.Inputs)
		if err != nil {
			return st, nil, err
		}
		trigger, _, err := normalizeMap(ev.Trigger)
		if err != nil {
			return st, nil, fmt.Errorf("%w: trigger: %v", ErrInvalidEvent, err)
		}
		if def.EventSchema != nil {
			if problems := def.EventSchema.validate(orEmpty(trigger)); len(problems) > 0 {
				return st, nil, fmt.Errorf("%w: trigger does not match event_schema: %s",
					ErrInvalidEvent, strings.Join(problems, "; "))
			}
			trigger, _ = def.EventSchema.complete(orEmpty(trigger)).(map[string]any)
		}
		r.st = &State{
			Definition: def.hash,
			Status:     StatusRunning,
			Inputs:     inputs,
			Trigger:    trigger,
			StartedAt:  r.at,
			Paths:      map[string]*Path{},
			Nodes:      map[string]*Node{},
			Joins:      map[string][]string{},
		}
		r.st.Paths[mainPath] = newPath(mainPath, "")
		r.later(func() { r.enter(mainPath, def.Steps[0].Name) })

	case EventStepCompleted, EventStepFailed, EventTimerFired:
		if st.Status.Terminal() {
			return st, nil, nil
		}
		key, index, attempt, ok := ev.StepID.parse()
		if !ok {
			return st, nil, fmt.Errorf("%w: malformed step id %q", ErrInvalidEvent, ev.StepID)
		}
		n := st.Nodes[key]
		unit := index
		if n != nil && !n.Each {
			unit = 0
			if index >= 0 {
				n = nil
			}
		}
		if n == nil || unit < 0 || unit >= len(n.Units) || attempt > n.Units[unit].Attempt {
			return st, nil, fmt.Errorf("%w: %s", ErrUnknownStep, ev.StepID)
		}
		if u := n.Units[unit]; attempt < u.Attempt || u.Status != UnitRunning {
			return st, nil, nil // settled: a duplicate or late delivery
		}
		step := def.byName[n.Step]
		switch {
		case ev.Kind == EventStepFailed && ev.Error == nil:
			return st, nil, fmt.Errorf("%w: StepFailed needs an Error", ErrInvalidEvent)
		case ev.Kind == EventTimerFired && step.Kind != KindSleep && step.Timeout == 0:
			return st, nil, fmt.Errorf("%w: no timer is scheduled for %s", ErrInvalidEvent, ev.StepID)
		case ev.Kind != EventTimerFired && step.Kind == KindSleep:
			return st, nil, fmt.Errorf("%w: %s is a sleep; only TimerFired settles it", ErrInvalidEvent, ev.StepID)
		}
		r.st = st.clone()
		n = r.st.Nodes[key]
		switch ev.Kind {
		case EventStepCompleted:
			r.unitCompleted(n, unit, ev.Output)
		case EventStepFailed:
			e := *ev.Error
			r.unitFailedAttempt(n, unit, &e)
		case EventTimerFired:
			if step.Kind == KindSleep {
				r.unitCompleted(n, unit, nil)
			} else {
				r.emit(Command{Kind: CommandCancelStep, StepID: ev.StepID, Step: step.Name, StepKind: step.Kind, Index: n.unitIndex(unit)})
				r.unitFailedAttempt(n, unit, &StepError{
					Type:      ErrorTypeTimeout,
					Message:   fmt.Sprintf("step did not finish within %s", step.Timeout),
					Retryable: true,
				})
			}
		}

	case EventCancel:
		if st.Status.Terminal() {
			return st, nil, nil
		}
		r.st = st.clone()
		r.cancelAll()
		r.finish(StatusCancelled)
		r.emit(Command{Kind: CommandRunCancelled, Reason: ev.Reason, Index: -1})

	default:
		return st, nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, ev.Kind)
	}

	r.drain()
	r.st.UpdatedAt = r.at
	return r.st, r.cmds, nil
}

// run is the working set for one Apply call.
type run struct {
	def   *Definition
	st    *State
	at    time.Time
	cmds  []Command
	queue []func()
}

func (r *run) later(f func()) { r.queue = append(r.queue, f) }

func (r *run) emit(c Command) { r.cmds = append(r.cmds, c) }

// drain runs queued transitions, then fires joins and checks for
// completion, until nothing is left to do.
func (r *run) drain() {
	for {
		for len(r.queue) > 0 {
			f := r.queue[0]
			r.queue = r.queue[1:]
			f()
		}
		if r.st.Status != StatusRunning {
			return
		}
		if r.fireJoins() {
			continue
		}
		r.checkDone()
		if len(r.queue) == 0 {
			return
		}
	}
}

func newPath(id, parent string) *Path {
	return &Path{
		ID: id, Parent: parent, Status: PathActive,
		Visits: map[string]int{}, Scope: map[string]string{}, Since: map[string]bool{},
	}
}

func nodeKey(path, step string, visit int) string {
	if visit > 1 {
		return fmt.Sprintf("%s/%s@%d", path, step, visit)
	}
	return path + "/" + step
}

// live reports whether transitions on the path may still happen.
func (r *run) live(p *Path) bool {
	if p == nil || p.Status == PathEnded {
		return false
	}
	if p.ID == failurePath {
		return r.st.Status == StatusFailing
	}
	return r.st.Status == StatusRunning
}

// enter moves a path onto a step.
func (r *run) enter(pathID, stepName string) {
	p := r.st.Paths[pathID]
	if !r.live(p) {
		return
	}
	s := r.def.byName[stepName]
	if s.Kind == KindJoin {
		// Visits to a join are counted on the merged path when it fires.
		p.Status, p.Step, p.Node = PathWaiting, stepName, ""
		r.st.Joins[stepName] = insertSorted(r.st.Joins[stepName], pathID)
		return
	}
	visit := p.Visits[stepName] + 1
	if s.MaxVisits > 0 && visit > s.MaxVisits {
		r.failRun(&RunError{Step: stepName, Type: ErrorTypeMaxVisits,
			Message: fmt.Sprintf("step entered more than max_visits (%d) times", s.MaxVisits)})
		return
	}
	p.Visits[stepName] = visit
	if visit > 1 {
		// A new pass of a loop: what settled after this step last time
		// has not settled in this pass.
		delete(p.Since, stepName)
		for name := range r.def.forward[stepName] {
			delete(p.Since, name)
		}
	}
	key := nodeKey(pathID, stepName, visit)
	n := &Node{
		Key: key, Path: pathID, Step: stepName, Visit: visit,
		Failure: pathID == failurePath, Status: NodeRunning, EnteredAt: r.at,
	}
	r.st.Nodes[key] = n
	p.Status, p.Step, p.Node = PathActive, stepName, key

	switch s.Kind {
	case KindSet:
		v, err := s.set.eval(r.env(p, nil))
		if err != nil {
			r.failNode(n, "", exprError("set", err))
			return
		}
		v, size, err := normalize(v)
		if err != nil {
			r.failNode(n, "", asStepError(err))
			return
		}
		if serr := r.checkOutput(size); serr != nil {
			r.failNode(n, "", serr)
			return
		}
		if err := r.charge(size); err != nil {
			r.failNode(n, "", asStepError(err))
			return
		}
		r.completeNode(n, v)

	case KindSleep:
		n.Units = []*Unit{{Attempt: 1, Status: UnitRunning, DispatchedAt: r.at}}
		r.emit(Command{
			Kind: CommandScheduleTimer, StepID: makeStepID(key, -1, 1),
			Step: stepName, StepKind: KindSleep, Index: -1, Attempt: 1,
			NotBefore: r.at.Add(s.Sleep),
		})

	default:
		if s.each == nil {
			n.Units = []*Unit{{Status: UnitPending}}
			r.progress(n)
			return
		}
		v, err := s.each.eval(r.env(p, nil))
		if err != nil {
			r.failNode(n, "", exprError("each", err))
			return
		}
		items, size, err := normalizeList(v)
		if err == nil {
			err = r.charge(size)
		}
		if err != nil {
			r.failNode(n, "", asStepError(err))
			return
		}
		n.Each = true
		n.Units = make([]*Unit, len(items))
		for i, item := range items {
			n.Units[i] = &Unit{Item: item, Status: UnitPending}
		}
		r.progress(n)
	}
}

// progress dispatches pending units up to the step's concurrency and settles
// the node once every unit has settled.
func (r *run) progress(n *Node) {
	if n.Status != NodeRunning {
		return
	}
	s := r.def.byName[n.Step]
	limit := s.Concurrency
	if limit <= 0 {
		limit = len(n.Units)
	}
	running := 0
	for _, u := range n.Units {
		if u.Status == UnitRunning {
			running++
		}
	}
	for i, u := range n.Units {
		if running >= limit || n.Failing {
			break
		}
		if u.Status != UnitPending {
			continue
		}
		if err := r.dispatch(n, i, time.Time{}); err != nil {
			r.unitFailed(n, i, err)
			continue
		}
		running++
	}
	if running > 0 {
		return
	}
	for _, u := range n.Units {
		if u.Status == UnitPending && !n.Failing {
			return // unreachable: pending units are dispatched above
		}
	}
	if n.Failing {
		var failed *StepError
		var failedID StepID
		for i, u := range n.Units {
			switch {
			case u.Status == UnitPending:
				u.Status = UnitSkipped
			case u.Status == UnitFailed && failed == nil:
				failed = u.Error
				if u.Attempt > 0 {
					failedID = makeStepID(n.Key, n.unitIndex(i), u.Attempt)
				}
			}
		}
		r.failNode(n, failedID, failed)
		return
	}
	r.completeNode(n, nil)
}

// dispatch emits DispatchStep for the unit's next attempt.
func (r *run) dispatch(n *Node, i int, notBefore time.Time) *StepError {
	u := n.Units[i]
	cmd, err := r.dispatchCommand(n, i, u.Attempt+1, notBefore)
	if err != nil {
		return err
	}
	u.Attempt++
	u.Status = UnitRunning
	u.DispatchedAt = r.at
	u.NotBefore = notBefore
	r.emit(cmd)
	if s := r.def.byName[n.Step]; s.Timeout > 0 {
		r.emit(r.timeoutCommand(s, cmd))
	}
	return nil
}

func (r *run) dispatchCommand(n *Node, i, attempt int, notBefore time.Time) (Command, *StepError) {
	s := r.def.byName[n.Step]
	u := n.Units[i]
	var params map[string]any
	if s.with != nil {
		var extra map[string]any
		if n.Each {
			extra = map[string]any{"item": u.Item, "index": i}
		}
		v, err := s.with.eval(r.env(r.st.Paths[n.Path], extra))
		if err != nil {
			return Command{}, exprError("with", err)
		}
		m, _, err := normalizeMap(v.(map[string]any))
		if err != nil {
			return Command{}, exprError("with", err)
		}
		params = m
	}
	if s.params != nil {
		if problems := s.params.validate(orEmpty(params)); len(problems) > 0 {
			return Command{}, &StepError{Type: ErrorTypeInvalidParams,
				Message: "params do not match the step kind's schema: " + strings.Join(problems, "; ")}
		}
	}
	cmd := Command{
		Kind:         CommandDispatchStep,
		StepID:       makeStepID(n.Key, n.unitIndex(i), attempt),
		Step:         s.Name,
		StepKind:     s.Kind,
		Params:       params,
		Config:       maps.Clone(s.Config),
		Index:        n.unitIndex(i),
		Attempt:      attempt,
		NotBefore:    notBefore,
		OutputSchema: s.Output,
	}
	if n.Each {
		cmd.Item = u.Item
	}
	return cmd, nil
}

func (r *run) timeoutCommand(s *Step, dispatch Command) Command {
	start := r.at
	if dispatch.NotBefore.After(start) {
		start = dispatch.NotBefore
	}
	return Command{
		Kind: CommandScheduleTimer, StepID: dispatch.StepID, Step: s.Name,
		StepKind: s.Kind, Index: dispatch.Index, Attempt: dispatch.Attempt,
		NotBefore: start.Add(s.Timeout),
	}
}

func (r *run) unitCompleted(n *Node, i int, output any) {
	v, size, err := normalize(output)
	if err != nil {
		r.unitFailedAttempt(n, i, &StepError{Type: ErrorTypeInvalidOutput, Message: err.Error()})
		return
	}
	if s := r.def.byName[n.Step]; s.Output != nil {
		if problems := s.Output.validate(v); len(problems) > 0 {
			r.unitFailedAttempt(n, i, &StepError{Type: ErrorTypeInvalidOutput,
				Message: "output does not match the step's output schema: " + strings.Join(problems, "; ")})
			return
		}
		v = s.Output.complete(v)
	}
	if err := r.checkOutput(size); err != nil {
		r.unitFailedAttempt(n, i, err)
		return
	}
	if err := r.charge(size); err != nil {
		r.unitFailedAttempt(n, i, asStepError(err))
		return
	}
	u := n.Units[i]
	u.Status, u.Output, u.Error = UnitCompleted, v, nil
	r.progress(n)
}

// unitFailedAttempt handles a failed attempt: retry it if the step allows,
// otherwise settle the unit as failed.
func (r *run) unitFailedAttempt(n *Node, i int, err *StepError) {
	s := r.def.byName[n.Step]
	u := n.Units[i]
	if rt := s.Retry; rt != nil && err.Retryable && u.Attempt < rt.MaxAttempts && rt.matches(err.Type) {
		u.Error = err
		if derr := r.dispatch(n, i, r.at.Add(rt.backoff(u.Attempt))); derr == nil {
			return
		} else {
			err = derr
		}
	}
	r.unitFailed(n, i, err)
	r.progress(n)
}

func (r *run) unitFailed(n *Node, i int, err *StepError) {
	u := n.Units[i]
	u.Status, u.Error = UnitFailed, err
	if !(n.Each && r.def.byName[n.Step].ContinueOnError) {
		n.Failing = true
	}
}

// checkOutput applies MaxOutputBytes to one step's value.
func (r *run) checkOutput(size int) *StepError {
	if size > r.def.Limits.maxOutput() {
		return &StepError{Type: ErrorTypeOutputTooLarge, Message: fmt.Sprintf(
			"output is %d bytes; the limit is %d", size, r.def.Limits.maxOutput())}
	}
	return nil
}

// charge adds output bytes to the run's total.
func (r *run) charge(size int) error {
	if r.st.Bytes+size > r.def.Limits.maxState() {
		return &StepError{Type: ErrorTypeStateTooLarge, Message: fmt.Sprintf(
			"run state would hold %d bytes of outputs; the limit is %d", r.st.Bytes+size, r.def.Limits.maxState())}
	}
	r.st.Bytes += size
	return nil
}

func (r *run) completeNode(n *Node, value any) {
	n.Status, n.SettledAt = NodeCompleted, r.at
	if len(n.Units) == 0 {
		n.Output = value
	}
	p := r.st.Paths[n.Path]
	p.Scope[n.Step] = n.Key
	p.Since[n.Step] = true
	if n.Failure {
		r.later(func() { r.nextFailureStep(n) })
		return
	}
	r.later(func() { r.route(p.ID, n.Key) })
}

func (r *run) failNode(n *Node, id StepID, err *StepError) {
	n.Status, n.Error, n.SettledAt = NodeFailed, err, r.at
	s := r.def.byName[n.Step]
	if n.Failure {
		r.st.Error.OnFailure = &RunError{Step: s.Name, StepID: id, Type: err.Type, Message: err.Message, At: r.at}
		r.finish(StatusFailed)
		r.emit(Command{Kind: CommandRunFailed, Error: r.st.Error, Index: -1})
		return
	}
	for _, c := range s.Catch {
		if c.matches(err.Type) {
			p := r.st.Paths[n.Path]
			p.Scope[n.Step] = n.Key
			p.Since[n.Step] = true
			target := c.Next
			r.later(func() { r.enter(p.ID, target) })
			return
		}
	}
	r.failRun(&RunError{Step: s.Name, StepID: id, Type: err.Type, Message: err.Message})
}

// route follows a settled node's edges.
func (r *run) route(pathID, key string) {
	p := r.st.Paths[pathID]
	if !r.live(p) || p.Node != key {
		return
	}
	s := r.def.byName[p.Step]
	env := r.env(p, nil)
	var targets []string
	if s.swtch != nil {
		v, err := s.swtch.eval(env)
		if err != nil {
			r.failRun(&RunError{Step: s.Name, Type: ErrorTypeExpression, Message: fmt.Sprintf("next: switch: %v", err)})
			return
		}
		var ok bool
		if targets, ok = s.Switch.route(v); !ok {
			r.failRun(&RunError{Step: s.Name, Type: ErrorTypeSwitch,
				Message: fmt.Sprintf("no case for %s = %s and no default", s.Switch.Expr, describe(v))})
			return
		}
	}
	for i, e := range s.Next {
		if prog := s.edges[i]; prog != nil {
			v, err := prog.eval(env)
			if err != nil {
				r.failRun(&RunError{Step: s.Name, Type: ErrorTypeExpression,
					Message: fmt.Sprintf("next %s: when: %v", e.Step, err)})
				return
			}
			b, isBool := v.(bool)
			if !isBool {
				r.failRun(&RunError{Step: s.Name, Type: ErrorTypeExpression,
					Message: fmt.Sprintf("next %s: when must be a boolean, got %s", e.Step, describe(v))})
				return
			}
			if !b {
				continue
			}
		}
		targets = append(targets, e.Step)
		if s.Match == "first" {
			break
		}
	}
	switch len(targets) {
	case 0:
		p.Status, p.Ended = PathEnded, "end"
	case 1:
		r.enter(p.ID, targets[0])
	default:
		p.Status, p.Ended, p.Node = PathEnded, "forked", ""
		p.Children = p.Children[:0]
		for _, t := range targets {
			child := newPath(r.allocPath(p.ID+"."+t), p.ID)
			p.Children = append(p.Children, child.ID)
			child.Visits = maps.Clone(p.Visits)
			child.Scope = maps.Clone(p.Scope)
			child.Since = maps.Clone(p.Since)
			r.st.Paths[child.ID] = child
			target := t
			r.later(func() { r.enter(child.ID, target) })
		}
	}
}

func (r *run) allocPath(base string) string {
	if r.st.Paths[base] == nil {
		return base
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s~%d", base, i)
		if r.st.Paths[id] == nil {
			return id
		}
	}
}

// fireJoins fires every join that no live path can still reach.
func (r *run) fireJoins() bool {
	fired := false
	for _, step := range sortedKeys(r.st.Joins) {
		arrivals := r.st.Joins[step]
		if len(arrivals) == 0 || r.canStillArrive(step) {
			continue
		}
		r.fireJoin(step, arrivals)
		fired = true
		if r.st.Status != StatusRunning {
			return true
		}
	}
	return fired
}

func (r *run) canStillArrive(join string) bool {
	for _, p := range r.st.Paths {
		if p.Status == PathEnded || p.ID == failurePath {
			continue
		}
		if p.Status == PathWaiting && p.Step == join {
			continue
		}
		if r.def.forward[p.Step][join] {
			return true
		}
	}
	return false
}

func (r *run) fireJoin(join string, arrivals []string) {
	delete(r.st.Joins, join)
	s := r.def.byName[join]

	scope := map[string]string{}
	since := map[string]bool{}
	visits := map[string]int{}
	for _, id := range arrivals {
		p := r.st.Paths[id]
		r.mergeScope(scope, p.Scope)
		for k := range p.Since {
			since[k] = true
		}
		for k, v := range p.Visits {
			visits[k] = max(visits[k], v)
		}
	}

	// Continue on the common ancestor when the arrivals account for every
	// path its last fork created. Otherwise continue on a new child of it,
	// so that two joins that each merge part of one fork never share a
	// path. Both tests depend only on the arrivals, not on event order.
	target := r.commonAncestor(arrivals)
	if !contains(arrivals, target) {
		anc := r.st.Paths[target]
		if anc.Status == PathEnded && anc.Ended == "forked" && r.covers(arrivals, anc.Children) {
			for k, v := range anc.Visits {
				visits[k] = max(visits[k], v)
			}
		} else {
			fresh := newPath(r.allocPath(target+"."+join), target)
			for _, id := range arrivals {
				fresh.Absorbed = append(fresh.Absorbed, r.cover(id)...)
			}
			sort.Strings(fresh.Absorbed)
			fresh.Absorbed = dedupe(fresh.Absorbed)
			r.st.Paths[fresh.ID] = fresh
			target = fresh.ID
		}
	}
	for _, id := range arrivals {
		if id != target {
			p := r.st.Paths[id]
			p.Status, p.Ended, p.Step = PathEnded, "joined", ""
		}
	}
	mp := r.st.Paths[target]
	mp.Status, mp.Ended = PathActive, ""
	mp.Scope, mp.Visits, mp.Since = scope, visits, since

	var missing []string
	for _, name := range s.Join {
		if !since[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		mp.Step = join
		r.failRun(&RunError{Step: join, Type: ErrorTypeJoin, Message: fmt.Sprintf(
			"no path that can still reach the join settled %s", strings.Join(missing, ", "))})
		return
	}

	visit := visits[join] + 1
	if s.MaxVisits > 0 && visit > s.MaxVisits {
		mp.Step = join
		r.failRun(&RunError{Step: join, Type: ErrorTypeMaxVisits,
			Message: fmt.Sprintf("step entered more than max_visits (%d) times", s.MaxVisits)})
		return
	}
	mp.Visits[join] = visit
	key := nodeKey(target, join, visit)
	n := &Node{Key: key, Path: target, Step: join, Visit: visit, Status: NodeRunning, EnteredAt: r.at}
	r.st.Nodes[key] = n
	mp.Step, mp.Node = join, key
	r.completeNode(n, nil)
}

// cover returns the paths a path stands for: its lineage and every path
// merged into it or into an ancestor.
func (r *run) cover(id string) []string {
	var out []string
	for id != "" {
		p := r.st.Paths[id]
		out = append(out, id)
		out = append(out, p.Absorbed...)
		id = p.Parent
	}
	return out
}

// covers reports whether every child is covered by some arrival.
func (r *run) covers(arrivals, children []string) bool {
	covered := map[string]bool{}
	for _, id := range arrivals {
		for _, c := range r.cover(id) {
			covered[c] = true
		}
	}
	for _, c := range children {
		if !covered[c] {
			return false
		}
	}
	return true
}

// mergeScope merges src into dst. Where both name a step, the later visit
// wins; equal visits on different paths are ordered by node key so the
// result does not depend on event order.
func (r *run) mergeScope(dst, src map[string]string) {
	for k, v := range src {
		cur, ok := dst[k]
		if !ok {
			dst[k] = v
			continue
		}
		a, b := r.st.Nodes[cur], r.st.Nodes[v]
		if b.Visit > a.Visit || (b.Visit == a.Visit && v > cur) {
			dst[k] = v
		}
	}
}

// commonAncestor returns the deepest path that is an ancestor of, or equal
// to, every given path.
func (r *run) commonAncestor(ids []string) string {
	lineage := func(id string) []string {
		var out []string
		for id != "" {
			out = append(out, id)
			id = r.st.Paths[id].Parent
		}
		return out
	}
	sets := make([]map[string]bool, len(ids))
	for i, id := range ids {
		sets[i] = map[string]bool{}
		for _, a := range lineage(id) {
			sets[i][a] = true
		}
	}
	for _, cand := range lineage(ids[0]) {
		all := true
		for _, set := range sets[1:] {
			if !set[cand] {
				all = false
				break
			}
		}
		if all {
			return cand
		}
	}
	return mainPath
}

// checkDone completes the run when every path has ended, and fails it when
// the only paths left wait at joins that can never fire.
func (r *run) checkDone() {
	var waiting []string
	for _, p := range r.st.Paths {
		switch p.Status {
		case PathActive:
			return
		case PathWaiting:
			waiting = append(waiting, p.Step)
		}
	}
	if len(waiting) > 0 {
		sort.Strings(waiting)
		r.failRun(&RunError{Step: waiting[0], Type: ErrorTypeJoin, Message: fmt.Sprintf(
			"joins %s wait on each other and can never fire", strings.Join(dedupe(waiting), ", "))})
		return
	}

	var outputs map[string]any
	if r.def.outputs != nil {
		env := r.env(&Path{Scope: r.unionScope()}, nil)
		v, err := r.def.outputs.eval(env)
		if err == nil {
			outputs, _, err = normalizeMap(v.(map[string]any))
		}
		if err != nil {
			r.failRun(&RunError{Type: ErrorTypeExpression, Message: fmt.Sprintf("outputs: %v", err)})
			return
		}
	} else {
		// Without declared outputs, a run's result is the value of the last
		// step on every path that ended normally.
		outputs = map[string]any{}
		for _, id := range sortedKeys(r.st.Paths) {
			p := r.st.Paths[id]
			if p.Ended == "end" && p.Node != "" {
				outputs[p.Step] = r.value(r.st.Nodes[p.Node])
			}
		}
	}
	r.st.Outputs = outputs
	r.finish(StatusCompleted)
	r.emit(Command{Kind: CommandRunCompleted, Outputs: outputs, Index: -1})
}

// unionScope merges the scopes of every path, as mergeScope does.
func (r *run) unionScope() map[string]string {
	scope := map[string]string{}
	for _, id := range sortedKeys(r.st.Paths) {
		if id != failurePath {
			r.mergeScope(scope, r.st.Paths[id].Scope)
		}
	}
	return scope
}

func (r *run) failRun(err *RunError) {
	if r.st.Status != StatusRunning {
		return
	}
	err.At = r.at
	r.st.Error = err
	scope := r.unionScope()
	r.cancelAll()
	if len(r.def.OnFailure) == 0 {
		r.finish(StatusFailed)
		r.emit(Command{Kind: CommandRunFailed, Error: err, Index: -1})
		return
	}
	r.st.Status = StatusFailing
	fp := newPath(failurePath, "")
	fp.Scope = scope
	r.st.Paths[failurePath] = fp
	first := r.def.OnFailure[0].Name
	r.later(func() { r.enter(failurePath, first) })
}

func (r *run) nextFailureStep(n *Node) {
	if r.st.Status != StatusFailing {
		return
	}
	for i, s := range r.def.OnFailure {
		if s.Name == n.Step && i+1 < len(r.def.OnFailure) {
			r.enter(failurePath, r.def.OnFailure[i+1].Name)
			return
		}
	}
	r.st.Paths[failurePath].Status = PathEnded
	r.st.Paths[failurePath].Ended = "end"
	r.finish(StatusFailed)
	r.emit(Command{Kind: CommandRunFailed, Error: r.st.Error, Index: -1})
}

// cancelAll cancels every unit in flight and ends every path.
func (r *run) cancelAll() {
	for _, key := range sortedKeys(r.st.Nodes) {
		n := r.st.Nodes[key]
		if n.Status != NodeRunning {
			continue
		}
		s := r.def.byName[n.Step]
		for i, u := range n.Units {
			switch u.Status {
			case UnitRunning:
				r.emit(Command{Kind: CommandCancelStep, StepID: makeStepID(key, n.unitIndex(i), u.Attempt),
					Step: s.Name, StepKind: s.Kind, Index: n.unitIndex(i), Attempt: u.Attempt})
				u.Status = UnitCancelled
			case UnitPending:
				u.Status = UnitSkipped
			}
		}
		n.Status, n.SettledAt = NodeCancelled, r.at
	}
	for _, p := range r.st.Paths {
		if p.Status != PathEnded {
			p.Status, p.Ended = PathEnded, "cancelled"
		}
	}
	clear(r.st.Joins)
}

func (r *run) finish(status RunStatus) {
	r.st.Status = status
	r.st.FinishedAt = r.at
}

// env builds the expression globals for a path.
func (r *run) env(p *Path, extra map[string]any) map[string]any {
	env := make(map[string]any, len(r.def.byName)+4)
	env["inputs"] = orEmpty(r.st.Inputs)
	env["event"] = orEmpty(r.st.Trigger)
	// Every step is a name; one that has not settled on this path is nil,
	// so `report?.verdict` and coalesce(report, approve) work.
	for name := range r.def.byName {
		env[name] = nil
	}
	for name, key := range p.Scope {
		env[name] = r.value(r.st.Nodes[key])
	}
	if p.ID == failurePath && r.st.Error != nil {
		env["error"] = r.st.Error.value()
	}
	maps.Copy(env, extra)
	return env
}

// value is what expressions read for a settled node.
func (r *run) value(n *Node) any {
	switch {
	case n.Status == NodeFailed:
		return map[string]any{"error": n.Error.value()}
	case n.Each:
		out := make([]any, len(n.Units))
		for i, u := range n.Units {
			if u.Status == UnitFailed {
				out[i] = map[string]any{"error": u.Error.value()}
			} else {
				out[i] = u.Output
			}
		}
		return out
	case len(n.Units) == 1:
		return n.Units[0].Output
	default:
		return n.Output
	}
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func exprError(where string, err error) *StepError {
	return &StepError{Type: ErrorTypeExpression, Message: fmt.Sprintf("%s: %v", where, err)}
}

func asStepError(err error) *StepError {
	if se, ok := err.(*StepError); ok {
		return se
	}
	return &StepError{Type: ErrorTypeExpression, Message: err.Error()}
}

// normalize round-trips a value through JSON so that what Apply keeps is
// exactly what a host will load back, and returns its encoded size.
func normalize(v any) (any, int, error) {
	if v == nil {
		return nil, 0, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, 0, err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, 0, err
	}
	return out, len(data), nil
}

func normalizeMap(m map[string]any) (map[string]any, int, error) {
	if m == nil {
		return nil, 0, nil
	}
	v, size, err := normalize(m)
	if err != nil {
		return nil, 0, err
	}
	return v.(map[string]any), size, nil
}

func normalizeList(v any) ([]any, int, error) {
	n, size, err := normalize(v)
	if err != nil {
		return nil, 0, &StepError{Type: ErrorTypeExpression, Message: "each: " + err.Error()}
	}
	if n == nil {
		return nil, 0, nil
	}
	list, ok := n.([]any)
	if !ok {
		return nil, 0, &StepError{Type: ErrorTypeExpression, Message: fmt.Sprintf("each: expected a list, got %T", n)}
	}
	return list, size, nil
}

func (d *Definition) checkInputs(in map[string]any) (map[string]any, error) {
	out, _, err := normalizeMap(in)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInputs, err)
	}
	if out == nil {
		out = map[string]any{}
	}
	if d.Inputs == nil {
		return out, nil
	}
	var bad []string
	for _, name := range sortedKeys(out) {
		if d.Inputs[name] == nil {
			bad = append(bad, fmt.Sprintf("unknown input %q", name))
		}
	}
	for _, name := range sortedKeys(d.Inputs) {
		spec := d.Inputs[name]
		v, ok := out[name]
		if !ok || v == nil {
			if spec.Required {
				bad = append(bad, fmt.Sprintf("input %q is required", name))
			}
			out[name] = nil
			if spec.Default != nil {
				out[name] = spec.Schema.complete(spec.Default)
			}
			continue
		}
		problems := spec.Schema.validate(v)
		for _, msg := range problems {
			bad = append(bad, fmt.Sprintf("input %q: %s", name, strings.TrimPrefix(msg, "value: ")))
		}
		if len(problems) == 0 {
			out[name] = spec.Schema.complete(v)
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidInputs, strings.Join(bad, "; "))
	}
	return out, nil
}

func dedupe(list []string) []string {
	out := list[:0:0]
	for i, s := range list {
		if i == 0 || s != list[i-1] {
			out = append(out, s)
		}
	}
	return out
}
