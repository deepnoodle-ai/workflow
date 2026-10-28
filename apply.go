package workflow

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

const (
	mainPath    = "main"
	failurePath = "failure"
	finallyPath = "finally"
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
// have: a definition Load did not make (ErrNotValidated), a state from
// another definition (ErrDefinitionMismatch), an event for a run that has
// not started (ErrNotStarted), a malformed event such as one without At
// (ErrInvalidEvent), an unknown StepID (ErrUnknownStep), or an input that
// does not match the definition (ErrInvalidInput). The run's own failures
// are RunFailed commands.
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
	if started && st.DefinitionHash != def.hash {
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
		input, err := def.checkInput(ev.Input)
		if err != nil {
			return st, nil, err
		}
		r.st = &State{
			DefinitionHash: def.hash,
			Status:         StatusRunning,
			Input:          input,
			StartedAt:      r.at,
			paths:          map[string]*pathState{},
			nodes:          map[string]*nodeState{},
			joins:          map[string][]string{},
		}
		r.st.paths[mainPath] = newPath(mainPath, "")
		r.later(func() { r.enter(mainPath, def.Steps[0].Name) })

	case EventStepCompleted, EventStepFailed, EventTimerFired:
		if st.Status.Terminal() {
			return st, nil, nil
		}
		key, index, attempt, ok := ev.StepID.parse()
		if !ok {
			return st, nil, fmt.Errorf("%w: malformed step id %q", ErrInvalidEvent, ev.StepID)
		}
		n := st.nodes[key]
		if n == nil && st.dropped(def, key) {
			return st, nil, nil // a superseded visit: its results are no longer wanted
		}
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
			return st, nil, fmt.Errorf("%w: %s is a sleep; only TimerFired settles it",
				ErrInvalidEvent, ev.StepID)
		}

		r.st = st.clone()
		n = r.st.nodes[key]
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
				r.emit(Command{
					Kind:     CommandCancelStep,
					StepID:   ev.StepID,
					Step:     step.Name,
					StepKind: step.Kind,
					Index:    n.unitIndex(unit),
				})
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
		cleaning := st.paths[finallyPath] != nil
		if cleaning && !ev.Force {
			// The outcome is decided and the cleanup runs to the end.
			return st, nil, nil
		}
		r.st = st.clone()
		if cleaning {
			r.stopFinally(ev.Reason)
			break
		}
		r.cancelAll()
		r.st.Reason = ev.Reason
		r.skipFinally = ev.Force
		r.conclude(StatusCancelled)

	default:
		return st, nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, ev.Kind)
	}

	r.drain()
	r.collect()
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
	// values caches scopeValue.
	values map[string]any
	// skipFinally is set by a forced cancel: the run finishes without its
	// finally steps.
	skipFinally bool
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

func newPath(id, parent string) *pathState {
	return &pathState{
		ID: id, Parent: parent, Status: pathActive,
		Visits: map[string]int{}, Scope: map[string]string{}, Settled: map[string][]int{},
	}
}

func nodeKey(path, step string, visit int) string {
	if visit > 1 {
		return fmt.Sprintf("%s/%s@%d", path, step, visit)
	}
	return path + "/" + step
}

// live reports whether transitions on the path may still happen.
func (r *run) live(p *pathState) bool {
	if p == nil || p.Status == pathEnded {
		return false
	}
	switch p.ID {
	case failurePath:
		return r.st.Status == StatusFailing
	case finallyPath:
		return !r.st.Status.Terminal()
	}
	return r.st.Status == StatusRunning
}

// enter moves a path onto a step.
func (r *run) enter(pathID, stepName string) {
	p := r.st.paths[pathID]
	if !r.live(p) {
		return
	}
	s := r.def.byName[stepName]
	if s.Kind == KindJoin {
		// Visits to a join are counted on the merged path when it fires.
		p.Status, p.Step, p.Node = pathWaiting, stepName, ""
		r.st.joins[stepName] = insertSorted(r.st.joins[stepName], pathID)
		return
	}
	visit := p.Visits[stepName] + 1
	if s.MaxVisits > 0 && visit > s.MaxVisits {
		r.failRun(&RunError{Step: stepName, Type: ErrorTypeMaxVisits,
			Message: fmt.Sprintf("step entered more than max_visits (%d) times", s.MaxVisits)})
		return
	}
	p.Visits[stepName] = visit
	key := nodeKey(pathID, stepName, visit)
	n := &nodeState{
		Key: key, Path: pathID, Step: stepName, Visit: visit,
		Failure: pathID == failurePath, Finally: pathID == finallyPath,
		Status: nodeRunning, EnteredAt: r.at,
	}
	r.st.nodes[key] = n
	p.Status, p.Step, p.Node = pathActive, stepName, key

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
		if err := r.charge(n, size); err != nil {
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
			err = r.charge(n, size)
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
func (r *run) progress(n *nodeState) {
	if n.Status != nodeRunning {
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

	if n.Failing && running > 0 {
		// The node is certain to fail: the items in flight can no
		// longer change that, so stop them now instead of waiting.
		// Their late results find the units cancelled and are ignored.
		r.cancelUnits(n)
		running = 0
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
func (r *run) dispatch(n *nodeState, i int, notBefore time.Time) *StepError {
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

func (r *run) dispatchCommand(
	n *nodeState, i, attempt int, notBefore time.Time,
) (Command, *StepError) {
	s := r.def.byName[n.Step]
	u := n.Units[i]
	var params map[string]any
	if s.with != nil {
		var extra map[string]any
		if n.Each {
			extra = map[string]any{"item": u.Item, "index": i}
		}
		v, err := s.with.eval(r.env(r.st.paths[n.Path], extra))
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

func (r *run) unitCompleted(n *nodeState, i int, output any) {
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
	if err := r.charge(n, size); err != nil {
		r.unitFailedAttempt(n, i, asStepError(err))
		return
	}
	u := n.Units[i]
	u.Status, u.Output, u.Error = UnitCompleted, v, nil
	r.progress(n)
}

// unitFailedAttempt handles a failed attempt: retry it if the step allows,
// otherwise settle the unit as failed.
func (r *run) unitFailedAttempt(n *nodeState, i int, err *StepError) {
	s := r.def.byName[n.Step]
	u := n.Units[i]
	if rt := s.Retry; rt != nil && err.Retryable &&
		u.Attempt < rt.MaxAttempts && rt.matches(err.Type) {
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

func (r *run) unitFailed(n *nodeState, i int, err *StepError) {
	u := n.Units[i]
	u.Status, u.Error = UnitFailed, err
	if !(n.Each && r.def.byName[n.Step].ContinueOnError) {
		n.Failing = true
	}
}

// checkOutput applies MaxOutputBytes to one step's value.
func (r *run) checkOutput(size int) *StepError {
	if size > r.def.limits.maxOutput() {
		return &StepError{Type: ErrorTypeOutputTooLarge, Message: fmt.Sprintf(
			"output is %d bytes; the limit is %d", size, r.def.limits.maxOutput())}
	}
	return nil
}

// charge adds bytes the node keeps to the run's total. Over the limit, it
// first drops the visits nothing can read any more.
func (r *run) charge(n *nodeState, size int) error {
	if r.st.Bytes+size > r.def.limits.maxState() {
		r.collect()
	}
	if r.st.Bytes+size > r.def.limits.maxState() {
		return &StepError{
			Type: ErrorTypeStateTooLarge,
			Message: fmt.Sprintf("run state would hold %d bytes of outputs; the limit is %d",
				r.st.Bytes+size, r.def.limits.maxState()),
		}
	}
	r.st.Bytes += size
	n.Bytes += size
	return nil
}

// collect drops the step visits that nothing can read any more, and the
// bytes they held, so that a loop keeps only its latest passes.
//
// Expressions, joins and outputs read node values only through path
// scopes, and the default outputs through a path's current node. So a
// settled node is kept when it is a path's current node, when a path that
// has not ended has it in scope, or when it is the highest visit of its
// step in any scope (what outputs and on_failure read, since a union of
// scopes keeps the highest visit). Anything else has been superseded by a
// newer visit of the same step and is unreadable: a path only ever
// replaces a scope entry with a newer visit, and ended paths are read
// only through that union. The scope entries of ended paths that pointed
// at dropped nodes go too, so every scope entry names a node.
//
// What is kept depends only on the state, never on event order.
func (r *run) collect() {
	if r.def.keepVisits {
		return
	}

	keep := map[string]bool{}
	top := map[string]int{}
	for _, p := range r.st.paths {
		if p.Node != "" {
			keep[p.Node] = true
		}
		for name, key := range p.Scope {
			if p.Status != pathEnded {
				keep[key] = true
			}
			top[name] = max(top[name], r.st.nodes[key].Visit)
		}
	}
	for _, p := range r.st.paths {
		for name, key := range p.Scope {
			if r.st.nodes[key].Visit == top[name] {
				keep[key] = true
			}
		}
	}

	for key, n := range r.st.nodes {
		if n.Status != nodeRunning && !keep[key] {
			r.st.Bytes -= n.Bytes
			delete(r.st.nodes, key)
		}
	}
	for _, p := range r.st.paths {
		for name, key := range p.Scope {
			if r.st.nodes[key] == nil {
				delete(p.Scope, name)
			}
		}
	}
}

func (r *run) completeNode(n *nodeState, value any) {
	n.Status, n.SettledAt = nodeCompleted, r.at
	if len(n.Units) == 0 {
		n.Output = value
	}
	p := r.st.paths[n.Path]
	p.Scope[n.Step] = n.Key
	p.Settled[n.Step] = r.pass(p, r.def.loopsOf[n.Step], "")
	switch {
	case n.Failure:
		r.later(func() { r.nextFailureStep(n) })
	case n.Finally:
		r.later(func() { r.nextFinallyStep(n) })
	default:
		r.later(func() { r.route(p.ID, n.Key) })
	}
}

func (r *run) failNode(n *nodeState, id StepID, err *StepError) {
	n.Status, n.Error, n.SettledAt = nodeFailed, err, r.at
	s := r.def.byName[n.Step]

	if n.Failure {
		r.st.Error.OnFailure = &RunError{
			Step:    s.Name,
			StepID:  id,
			Type:    err.Type,
			Message: err.Message,
			At:      r.at,
		}
		r.endPath(failurePath)
		r.conclude(StatusFailed)
		return
	}
	if n.Finally {
		// A failed finally step is reported, not fatal: the rest still
		// run, and read its value as {error: ...}.
		r.st.paths[n.Path].Scope[n.Step] = n.Key
		r.st.FinallyErrors = append(r.st.FinallyErrors,
			&RunError{Step: s.Name, StepID: id, Type: err.Type, Message: err.Message, At: r.at})
		r.later(func() { r.nextFinallyStep(n) })
		return
	}

	for _, c := range s.Catch {
		if c.matches(err.Type) {
			p := r.st.paths[n.Path]
			p.Scope[n.Step] = n.Key
			p.Settled[n.Step] = r.pass(p, r.def.loopsOf[n.Step], "")
			target := c.Next
			r.later(func() { r.enter(p.ID, target) })
			return
		}
	}
	r.failRun(&RunError{Step: s.Name, StepID: id, Type: err.Type, Message: err.Message})
}

// route follows a settled node's edges.
func (r *run) route(pathID, key string) {
	p := r.st.paths[pathID]
	if !r.live(p) || p.Node != key {
		return
	}

	s := r.def.byName[p.Step]
	env := r.env(p, nil)
	var targets []string
	if s.swtch != nil {
		v, err := s.swtch.eval(env)
		if err != nil {
			r.failRun(&RunError{Step: s.Name, Type: ErrorTypeExpression,
				Message: fmt.Sprintf("next: switch: %v", err)})
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
		p.Status, p.Ended = pathEnded, "end"
	case 1:
		r.enter(p.ID, targets[0])
	default:
		p.Status, p.Ended, p.Node = pathEnded, "forked", ""
		p.Children = p.Children[:0]
		for _, t := range targets {
			child := newPath(r.allocPath(p.ID+"."+t), p.ID)
			p.Children = append(p.Children, child.ID)
			child.Visits = maps.Clone(p.Visits)
			child.Scope = maps.Clone(p.Scope)
			child.Settled = maps.Clone(p.Settled)
			r.st.paths[child.ID] = child
			target := t
			r.later(func() { r.enter(child.ID, target) })
		}
	}
}

func (r *run) allocPath(base string) string {
	if r.st.paths[base] == nil {
		return base
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s~%d", base, i)
		if r.st.paths[id] == nil {
			return id
		}
	}
}

// fireJoins fires every join cohort that no live path can still join.
//
// The paths waiting at a join are grouped into cohorts by their pass of
// the loops the join is in; a join outside every loop has one cohort. A
// cohort fires once no live path can still arrive at the join in that
// pass. Whether a path can is decided from where it is and which pass it
// is in, and it only ever goes from "can" to "cannot", so the arrivals of
// every cohort, and the order cohorts fire in, do not depend on the order
// events arrive in.
func (r *run) fireJoins() bool {
	fired := false
	for _, step := range sortedKeys(r.st.joins) {
		for _, c := range r.cohorts(step) {
			if r.canStillArrive(step, c.pass, c.paths) {
				continue
			}
			r.fireJoin(step, c.paths, c.pass)
			fired = true
			if r.st.Status != StatusRunning {
				return true
			}
		}
	}
	return fired
}

type cohort struct {
	pass  []int
	paths []string
}

// cohorts groups the paths waiting at a join by pass, earliest first.
func (r *run) cohorts(join string) []cohort {
	loops := r.def.loopsOf[join]
	var out []cohort
	for _, id := range r.st.joins[join] {
		w := r.pass(r.st.paths[id], loops, "")
		i, found := slices.BinarySearchFunc(out, w, func(c cohort, w []int) int {
			return comparePass(c.pass, w)
		})
		if found {
			out[i].paths = append(out[i].paths, id)
			continue
		}
		out = slices.Insert(out, i, cohort{pass: w, paths: []string{id}})
	}
	return out
}

// pass returns a path's pass of each loop: how many times its lineage
// entered the loop's heads. A join's visits count its firings, so for a
// path waiting at join firing, it is the pass the path will be in once
// that join fires.
func (r *run) pass(p *pathState, loops []*loop, firing string) []int {
	out := make([]int, len(loops))
	for i, l := range loops {
		for _, h := range l.heads {
			out[i] += p.Visits[h]
			if h == firing {
				out[i]++
			}
		}
	}
	return out
}

// comparePass orders passes lexicographically, outermost loop first.
func comparePass(a, b []int) int {
	c, _ := diffPass(a, b)
	return c
}

// diffPass compares passes and returns the first loop they differ in, or
// len(a) when they are equal.
func diffPass(a, b []int) (cmp, at int) {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1, i
			}
			return 1, i
		}
	}
	return 0, len(a)
}

// canStillArrive reports whether a live path outside the cohort can still
// arrive at the join in the cohort's pass. Passes only grow, so a path in a
// later pass cannot. A path in the same pass, or behind it from loop i
// inward, can if it can reach the join without entering a head of the
// loops outside i, where its pass already matches. (In the same pass that
// is every loop of the join.)
func (r *run) canStillArrive(join string, pass []int, cohort []string) bool {
	loops := r.def.loopsOf[join]
	for _, p := range r.st.paths {
		if p.Status == pathEnded || p.ID == failurePath || p.ID == finallyPath {
			continue
		}
		firing := ""
		if p.Status == pathWaiting {
			if p.Step == join && slices.Contains(cohort, p.ID) {
				continue
			}
			firing = p.Step
		}
		if c, at := diffPass(r.pass(p, loops, firing), pass); c <= 0 && r.def.arrive[join][at][p.Step] {
			return true
		}
	}
	return false
}

func (r *run) fireJoin(join string, arrivals []string, pass []int) {
	var rest []string
	for _, id := range r.st.joins[join] {
		if !slices.Contains(arrivals, id) {
			rest = append(rest, id)
		}
	}
	if len(rest) == 0 {
		delete(r.st.joins, join)
	} else {
		r.st.joins[join] = rest
	}
	s := r.def.byName[join]

	scope := map[string]string{}
	settled := map[string][]int{}
	visits := map[string]int{}
	for _, id := range arrivals {
		p := r.st.paths[id]
		r.mergeScope(scope, p.Scope)
		for k, v := range p.Settled {
			// The latest pass wins: if any arrival settled k in the
			// current pass of some loops, so did the latest.
			if cur, ok := settled[k]; !ok || comparePass(cur, v) < 0 {
				settled[k] = v
			}
		}
		for k, v := range p.Visits {
			visits[k] = max(visits[k], v)
		}
	}

	// Continue on the common ancestor when the arrivals account for every
	// path its last fork created (see covers). Otherwise continue on a new
	// child of it, so that two joins that each merge part of one fork never
	// share a path. Both tests depend only on the arrivals, not on event
	// order.
	target := r.commonAncestor(arrivals)
	if !slices.Contains(arrivals, target) {
		anc := r.st.paths[target]
		if anc.Status == pathEnded && anc.Ended == "forked" && r.covers(arrivals, anc.Children) {
			for k, v := range anc.Visits {
				visits[k] = max(visits[k], v)
			}
		} else {
			fresh := newPath(r.allocPath(target+"."+join), target)
			for _, id := range arrivals {
				fresh.Absorbed = append(fresh.Absorbed, r.cover(id)...)
			}
			slices.Sort(fresh.Absorbed)
			fresh.Absorbed = dedupe(fresh.Absorbed)
			r.st.paths[fresh.ID] = fresh
			target = fresh.ID
		}
	}
	for _, id := range arrivals {
		if id != target {
			p := r.st.paths[id]
			p.Status, p.Ended, p.Step = pathEnded, "joined", ""
		}
	}
	mp := r.st.paths[target]
	mp.Status, mp.Ended = pathActive, ""
	mp.Scope, mp.Visits, mp.Settled = scope, visits, settled

	// A named step counts if it settled in this pass of every loop it
	// shares with the join. Passes of loops the join is not in do not
	// matter: a join after a loop sees what settled in any of its passes.
	var missing []string
	for _, name := range s.Join {
		at, ok := settled[name]
		if ok {
			common := r.def.commonLoops(name, join)
			ok = comparePass(at[:common], pass[:common]) == 0
		}
		if !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		mp.Step = join
		r.failRun(&RunError{Step: join, Type: ErrorTypeJoin, Message: fmt.Sprintf(
			"no path that arrived at the join settled %s in this pass", strings.Join(missing, ", "))})
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
	n := &nodeState{
		Key:       key,
		Path:      target,
		Step:      join,
		Visit:     visit,
		Status:    nodeRunning,
		EnteredAt: r.at,
	}
	r.st.nodes[key] = n
	mp.Step, mp.Node = join, key
	r.completeNode(n, nil)
}

// cover returns the paths a path stands for: itself and every path a join
// merged into it, directly or through earlier merges. It deliberately
// leaves out the path's ancestors: a descendant of a fork stands for its
// own branch, not for the fork.
func (r *run) cover(id string) []string {
	return append([]string{id}, r.st.paths[id].Absorbed...)
}

// covers reports whether the arrivals account for every child: a child is
// accounted for when an arrival stands for it, or when it forked and every
// path it forked into is accounted for in turn. Each path stands in at most
// one join firing, so no two firings can both account for one fork; which
// of them fires first cannot decide which continues on the fork's path.
func (r *run) covers(arrivals, children []string) bool {
	covered := map[string]bool{}
	for _, id := range arrivals {
		for _, c := range r.cover(id) {
			covered[c] = true
		}
	}
	var accounted func(id string) bool
	accounted = func(id string) bool {
		if covered[id] {
			return true
		}
		p := r.st.paths[id]
		if p.Status != pathEnded || p.Ended != "forked" || len(p.Children) == 0 {
			return false
		}
		for _, c := range p.Children {
			if !accounted(c) {
				return false
			}
		}
		return true
	}
	for _, c := range children {
		if !accounted(c) {
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
		a, b := r.st.nodes[cur], r.st.nodes[v]
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
			id = r.st.paths[id].Parent
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
	for _, p := range r.st.paths {
		switch p.Status {
		case pathActive:
			return
		case pathWaiting:
			waiting = append(waiting, p.Step)
		}
	}
	if len(waiting) > 0 {
		slices.Sort(waiting)
		r.failRun(&RunError{Step: waiting[0], Type: ErrorTypeJoin, Message: fmt.Sprintf(
			"joins %s wait on each other and can never fire", strings.Join(dedupe(waiting), ", "))})
		return
	}

	var outputs map[string]any
	if r.def.outputs != nil {
		env := r.env(&pathState{Scope: r.unionScope()}, nil)
		v, err := r.def.outputs.eval(env)
		if err == nil {
			outputs, _, err = normalizeMap(v.(map[string]any))
		}
		if err != nil {
			r.failRun(&RunError{Type: ErrorTypeExpression, Message: fmt.Sprintf("outputs: %v", err)})
			return
		}
		if serr := r.checkOutputs(outputs); serr != nil {
			r.failRun(serr)
			return
		}
	} else {
		// Without declared outputs, a run's result is the value of the last
		// step on every path that ended normally.
		outputs = map[string]any{}
		for _, id := range sortedKeys(r.st.paths) {
			p := r.st.paths[id]
			if p.Ended == "end" && p.Node != "" {
				outputs[p.Step] = r.value(r.st.nodes[p.Node])
			}
		}
	}
	r.st.Outputs = outputs
	r.conclude(StatusCompleted)
}

// checkOutputs validates the run's outputs against their declared schemas
// and fills absent declared properties with nil, as step outputs are. Every
// mismatch is reported, in name order.
func (r *run) checkOutputs(outputs map[string]any) *RunError {
	var bad []string
	for _, name := range sortedKeys(r.def.Outputs) {
		s := r.def.Outputs[name].Schema
		if s == nil {
			continue
		}
		before := len(bad)
		s.validateAt(outputs[name], "outputs."+name, &bad)
		if len(bad) == before {
			outputs[name] = s.complete(outputs[name])
		}
	}
	if len(bad) > 10 {
		bad = append(bad[:10], fmt.Sprintf("and %d more", len(bad)-10))
	}
	if len(bad) > 0 {
		return &RunError{Type: ErrorTypeInvalidOutput,
			Message: "outputs do not match their schemas: " + strings.Join(bad, "; ")}
	}
	return nil
}

// unionScope merges the scopes of every path, as mergeScope does.
func (r *run) unionScope() map[string]string {
	scope := map[string]string{}
	for _, id := range sortedKeys(r.st.paths) {
		if id != failurePath && id != finallyPath {
			r.mergeScope(scope, r.st.paths[id].Scope)
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
		r.conclude(StatusFailed)
		return
	}
	r.st.Status = StatusFailing
	fp := newPath(failurePath, "")
	fp.Scope = scope
	r.st.paths[failurePath] = fp
	first := r.def.OnFailure[0].Name
	r.later(func() { r.enter(failurePath, first) })
}

func (r *run) nextFailureStep(n *nodeState) {
	if r.st.Status != StatusFailing {
		return
	}
	for i, s := range r.def.OnFailure {
		if s.Name == n.Step && i+1 < len(r.def.OnFailure) {
			r.enter(failurePath, r.def.OnFailure[i+1].Name)
			return
		}
	}
	r.endPath(failurePath)
	r.conclude(StatusFailed)
}

func (r *run) endPath(id string) {
	p := r.st.paths[id]
	p.Status, p.Ended = pathEnded, "end"
}

// conclude settles the run's outcome once nothing else can change it:
// every path ended, or the run failed and its on_failure steps are done,
// or it was cancelled. Without finally steps the run finishes now.
// Otherwise its status says it is finishing and the finally steps start,
// over every value any path settled; the run finishes when they are done.
func (r *run) conclude(outcome RunStatus) {
	if r.skipFinally {
		for _, s := range r.def.Finally {
			r.st.FinallyErrors = append(r.st.FinallyErrors, &RunError{Step: s.Name, Type: ErrorTypeCancelled,
				Message: "did not run: the run was cancelled with force", At: r.at})
		}
	}
	if len(r.def.Finally) == 0 || r.skipFinally {
		r.finish(outcome)
		r.emitFinished()
		return
	}
	r.st.Status = finishing(outcome)
	fp := newPath(finallyPath, "")
	for _, id := range sortedKeys(r.st.paths) {
		r.mergeScope(fp.Scope, r.st.paths[id].Scope)
	}
	r.st.paths[finallyPath] = fp
	first := r.def.Finally[0].Name
	r.later(func() { r.enter(finallyPath, first) })
}

// nextFinallyStep moves on after a finally step settled, completed or
// failed, and finishes the run after the last.
func (r *run) nextFinallyStep(n *nodeState) {
	p := r.st.paths[finallyPath]
	if !r.live(p) {
		return
	}
	for i, s := range r.def.Finally {
		if s.Name == n.Step && i+1 < len(r.def.Finally) {
			r.enter(finallyPath, r.def.Finally[i+1].Name)
			return
		}
	}
	r.endPath(finallyPath)
	r.finish(r.st.Status.Outcome())
	r.emitFinished()
}

// stopFinally handles a cancel while finally steps run: the step in flight
// is cancelled, the rest never run, each is reported in FinallyErrors, and
// the run finishes with the outcome it already had.
func (r *run) stopFinally(reason string) {
	p := r.st.paths[finallyPath]
	why := "the run was cancelled while finally steps ran"
	if reason != "" {
		why += ": " + reason
	}
	current := -1
	for i, s := range r.def.Finally {
		if s.Name == p.Step {
			current = i
		}
	}
	if n := r.st.nodes[p.Node]; n != nil && n.Status == nodeRunning {
		var id StepID
		if len(n.Units) == 1 && n.Units[0].Attempt > 0 {
			id = makeStepID(n.Key, -1, n.Units[0].Attempt)
		}
		r.cancelUnits(n)
		n.Status, n.SettledAt = nodeCancelled, r.at
		r.st.FinallyErrors = append(r.st.FinallyErrors,
			&RunError{Step: n.Step, StepID: id, Type: ErrorTypeCancelled, Message: why, At: r.at})
	}
	for _, s := range r.def.Finally[current+1:] {
		r.st.FinallyErrors = append(r.st.FinallyErrors,
			&RunError{Step: s.Name, Type: ErrorTypeCancelled, Message: "did not run: " + why, At: r.at})
	}
	p.Status, p.Ended = pathEnded, "cancelled"
	r.finish(r.st.Status.Outcome())
	r.emitFinished()
}

// emitFinished emits the terminal command for a run that has finished.
func (r *run) emitFinished() {
	c := Command{Index: -1, FinallyErrors: r.st.FinallyErrors}
	switch r.st.Status {
	case StatusCompleted:
		c.Kind, c.Outputs = CommandRunCompleted, r.st.Outputs
	case StatusFailed:
		c.Kind, c.Error = CommandRunFailed, r.st.Error
	default:
		c.Kind, c.Reason = CommandRunCancelled, r.st.Reason
	}
	r.emit(c)
}

// cancelUnits cancels the node's units in flight and skips its pending
// ones.
func (r *run) cancelUnits(n *nodeState) {
	s := r.def.byName[n.Step]
	for i, u := range n.Units {
		switch u.Status {
		case UnitRunning:
			r.emit(Command{Kind: CommandCancelStep, StepID: makeStepID(n.Key, n.unitIndex(i), u.Attempt),
				Step: s.Name, StepKind: s.Kind, Index: n.unitIndex(i), Attempt: u.Attempt})
			u.Status = UnitCancelled
		case UnitPending:
			u.Status = UnitSkipped
		}
	}
}

// cancelAll cancels every unit in flight and ends every path.
func (r *run) cancelAll() {
	for _, key := range sortedKeys(r.st.nodes) {
		n := r.st.nodes[key]
		if n.Status != nodeRunning {
			continue
		}
		r.cancelUnits(n)
		n.Status, n.SettledAt = nodeCancelled, r.at
	}
	for _, p := range r.st.paths {
		if p.Status != pathEnded {
			p.Status, p.Ended = pathEnded, "cancelled"
		}
	}
	clear(r.st.joins)
}

func (r *run) finish(status RunStatus) {
	r.st.Status = status
	r.st.FinishedAt = r.at
}

// env builds the expression globals for a path.
func (r *run) env(p *pathState, extra map[string]any) map[string]any {
	env := make(map[string]any, len(r.def.byName)+4)
	env["input"] = orEmpty(r.st.Input)
	// Every step is a name; one that has not settled on this path is nil,
	// so `report?.verdict` and coalesce(report, approve) work.
	for name := range r.def.byName {
		env[name] = nil
	}
	for name, key := range p.Scope {
		env[name] = r.scopeValue(key)
	}
	if p.ID == failurePath && r.st.Error != nil {
		env["error"] = r.st.Error.value()
	}
	if p.ID == finallyPath {
		env["run"] = r.runValue()
	}
	maps.Copy(env, extra)
	return env
}

// scopeValue is value for a node in scope. Nodes in scope have settled and
// no longer change, so the value is built once per Apply: an each step's
// list is not rebuilt for every expression that could read it.
func (r *run) scopeValue(key string) any {
	if v, ok := r.values[key]; ok {
		return v
	}
	v := r.value(r.st.nodes[key])
	if r.values == nil {
		r.values = map[string]any{}
	}
	r.values[key] = v
	return v
}

// value is what expressions read for a settled node.
func (r *run) value(n *nodeState) any {
	switch {
	case n.Status == nodeFailed:
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

// runValue is what finally steps read as run: the outcome, and the error
// only when the run failed.
func (r *run) runValue() map[string]any {
	outcome := r.st.Status.Outcome()
	v := map[string]any{"status": string(outcome), "error": nil}
	if outcome == StatusFailed && r.st.Error != nil {
		v["error"] = r.st.Error.value()
	}
	return v
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
		return nil, 0, &StepError{Type: ErrorTypeExpression,
			Message: fmt.Sprintf("each: expected a list, got %T", n)}
	}
	return list, size, nil
}

// checkInput normalizes a run's input, validates it against the input
// schema and fills in defaults.
func (d *Definition) checkInput(in map[string]any) (map[string]any, error) {
	out, _, err := normalizeMap(in)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	out = orEmpty(out)
	if d.Input == nil {
		return out, nil
	}
	if problems := d.Input.validateAs(out, "input"); len(problems) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidInput, strings.Join(problems, "; "))
	}
	out, _ = d.Input.complete(out).(map[string]any)
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
