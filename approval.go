package workflow

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// KindApproval is the step kind of the jobs that ask a person to approve a
// held attempt of a step with approval. A host that registers it, with a
// handler under the same name, lets definitions use approval. No step can
// have this kind.
//
// An approval job carries the held job's Params and Config, and [Hold]:
// what it holds and why. Its output is {"approved": bool, "reason":
// string}, usually delivered later by StepID or by the step's name.
const KindApproval = "approval"

// Hold is what an approval job asks a person to approve: an attempt of the
// step that failed with one of the step's approval errors.
type Hold struct {
	Kind    string     `json:"kind"`    // the held job's step kind
	Attempt int        `json:"attempt"` // the held attempt
	Error   *StepError `json:"error"`   // what blocked it
}

// approvalState is a visit's approval job, once an attempt was held.
type approvalState struct {
	Unit Unit `json:"unit"` // the approval job: one attempt, its answer
	Hold Hold `json:"hold"`
	// Approved is set when the answer was yes: every later attempt of the
	// visit is approved, and none is held again.
	Approved bool `json:"approved,omitempty"`
	// Params are the held job's params, evaluated once at the hold and
	// shown to the person. The approval job and every approved attempt
	// carry these, never a new evaluation of with.
	Params map[string]any `json:"params,omitempty"`
}

func (a *approvalState) clone() *approvalState {
	if a == nil {
		return nil
	}
	c := *a
	if a.Hold.Error != nil {
		c.Hold.Error = new(*a.Hold.Error)
	}
	return &c
}

// approvalAnswer is the output schema of every approval job.
func approvalAnswer() *Schema {
	return &Schema{
		Type:     []string{"object"},
		Required: []string{"approved"},
		Properties: map[string]*Schema{
			"approved": {Type: []string{"boolean"}},
			"reason":   {Type: []string{"string"}},
		},
	}
}

// held reports whether unit i of the node is held: its attempt failed with
// one of the step's approval errors and no person has approved it. The
// unit stays running only to keep the visit open; for the host, the held
// attempt has settled.
func (n *nodeState) held(i int) bool {
	return n.Approval != nil && !n.Approval.Approved && n.Units[i].Status == UnitRunning
}

// hold holds the unit's failed attempt and dispatches the approval job in
// its place, with the params the next attempt would get, which it keeps:
// the approved job runs exactly what the person saw.
func (r *run) hold(n *nodeState, i int, err *StepError) {
	s := r.def.byName[n.Step]
	u := n.Units[i]
	cmd, derr := r.dispatchCommand(n, i, u.Attempt+1, time.Time{})
	if derr == nil {
		if cerr := r.charge(n, jsonSize(cmd.Params)); cerr != nil {
			derr = asStepError(cerr)
		}
	}
	if derr != nil {
		r.unitFailed(n, i, derr)
		r.progress(n)
		return
	}

	u.Error = err
	n.Approval = &approvalState{
		Unit:   Unit{Attempt: 1, Status: UnitRunning, DispatchedAt: r.at},
		Hold:   Hold{Kind: s.Kind, Attempt: u.Attempt, Error: err},
		Params: cmd.Params,
	}
	// The held attempt's step timeout is no longer wanted. A step timeout
	// that held the attempt has cancelled it already.
	held := makeStepID(n.Key, -1, u.Attempt)
	if !slices.ContainsFunc(r.cmds, func(c Command) bool {
		return c.Kind == CommandCancelStep && c.StepID == held
	}) {
		r.emit(Command{
			Kind:     CommandCancelStep,
			StepID:   held,
			Step:     s.Name,
			StepKind: s.Kind,
			Index:    -1,
			Attempt:  u.Attempt,
		})
	}
	dispatch, timer := r.approvalCommands(n)
	r.emit(dispatch)
	r.emit(timer)
}

// heldParams returns a copy of the params kept at the hold.
func (a *approvalState) heldParams() map[string]any {
	params, _, _ := normalizeMap(a.Params)
	return params
}

// approvalCommands builds the approval job's DispatchStep and its deadline
// from the state alone.
func (r *run) approvalCommands(n *nodeState) (dispatch, timer Command) {
	s := r.def.byName[n.Step]
	a := n.Approval
	hold := a.Hold
	if hold.Error != nil {
		hold.Error = new(*hold.Error)
	}
	dispatch = Command{
		Kind:         CommandDispatchStep,
		StepID:       makeApprovalID(n.Key, a.Unit.Attempt),
		Step:         s.Name,
		StepKind:     KindApproval,
		Params:       a.heldParams(),
		Config:       maps.Clone(s.Config),
		Index:        -1,
		Attempt:      a.Unit.Attempt,
		OutputSchema: approvalAnswer(),
		Hold:         &hold,
	}
	timer = Command{
		Kind:      CommandScheduleTimer,
		StepID:    dispatch.StepID,
		Step:      s.Name,
		StepKind:  KindApproval,
		Index:     -1,
		Attempt:   dispatch.Attempt,
		NotBefore: a.Unit.DispatchedAt.Add(s.Approval.Timeout),
	}
	return dispatch, timer
}

// approvalEvent applies a result or the deadline of a running approval
// job.
func (r *run) approvalEvent(n *nodeState, ev Event) {
	switch ev.Kind {
	case EventStepCompleted:
		r.approvalCompleted(n, ev.Output)
	case EventStepFailed:
		e := *ev.Error
		r.approvalBroken(n, &e)
	case EventTimerFired:
		if r.st.Status == StatusPaused {
			// Held like a step's timeout: re-armed at Resume.
			r.holdTimeout(ev.StepID)
			return
		}
		a := n.Approval
		timeout := shortDuration(r.def.byName[n.Step].Approval.Timeout)
		r.emit(Command{
			Kind:     CommandCancelStep,
			StepID:   ev.StepID,
			Step:     n.Step,
			StepKind: KindApproval,
			Index:    -1,
			Attempt:  a.Unit.Attempt,
		})
		a.Unit.Status = UnitFailed
		a.Unit.Error = &StepError{
			Type:    ErrorTypeTimeout,
			Message: "no answer within " + timeout,
		}
		r.refuse(n, "not approved within "+timeout)
	}
}

// approvalCompleted checks the answer: yes runs the held job again,
// approved; no fails the step as blocked.
func (r *run) approvalCompleted(n *nodeState, output any) {
	a := n.Approval
	v, size, err := normalize(output)
	if err != nil {
		r.approvalBroken(n, &StepError{Type: ErrorTypeInvalidOutput, Message: err.Error()})
		return
	}
	if problems := approvalAnswer().validate(v); len(problems) > 0 {
		r.approvalBroken(n, &StepError{
			Type:    ErrorTypeInvalidOutput,
			Message: "the answer is not valid: " + strings.Join(problems, "; "),
		})
		return
	}
	if serr := r.checkOutput(size); serr != nil {
		r.approvalBroken(n, serr)
		return
	}
	if err := r.charge(n, size); err != nil {
		r.approvalBroken(n, asStepError(err))
		return
	}

	a.Unit.Status, a.Unit.Output = UnitCompleted, v
	answer := v.(map[string]any)
	if answer["approved"] != true {
		why := "rejected"
		if reason, _ := answer["reason"].(string); reason != "" {
			why += ": " + reason
		}
		r.refuse(n, why)
		return
	}
	a.Approved = true
	n.Units[0].Error = nil
	if derr := r.dispatch(n, 0, time.Time{}); derr != nil {
		r.unitFailed(n, 0, derr)
		r.progress(n)
	}
}

// refuse fails a held step because a person said no or did not answer:
// with the type of the error that blocked it, so its catch routes it as
// before, and not retryable.
func (r *run) refuse(n *nodeState, why string) {
	blocked := n.Approval.Hold.Error
	r.unitFailed(n, 0, &StepError{
		Type:    blocked.Type,
		Message: blocked.Message + "; " + why,
	})
	r.progress(n)
}

// approvalBroken fails a held step whose approval job failed: no person
// was asked, so the held job does not run.
func (r *run) approvalBroken(n *nodeState, err *StepError) {
	a := n.Approval
	a.Unit.Status, a.Unit.Error = UnitFailed, err
	r.unitFailed(n, 0, &StepError{
		Type:    ErrorTypeApprovalFailed,
		Message: "approval job failed: " + err.Error(),
	})
	r.progress(n)
}

// cancelApproval stops an approval job in flight.
func (r *run) cancelApproval(n *nodeState) {
	a := n.Approval
	if a == nil || a.Unit.Status != UnitRunning {
		return
	}
	r.emit(Command{
		Kind:     CommandCancelStep,
		StepID:   makeApprovalID(n.Key, a.Unit.Attempt),
		Step:     n.Step,
		StepKind: KindApproval,
		Index:    -1,
		Attempt:  a.Unit.Attempt,
	})
	a.Unit.Status = UnitCancelled
}

// shortDuration formats d without zero minutes and seconds: 24h, not
// 24h0m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}
