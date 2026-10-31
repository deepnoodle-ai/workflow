package workflow

import "time"

// Abort fails a run that its host cannot advance with Apply, such as one
// whose definition no longer loads. It needs no definition, so it runs
// neither on_failure nor finally steps. Every attempt in flight is
// cancelled and the run fails with err, which must not be nil, stamped
// at. Outputs and Reason are cleared, as for any failed run: a run aborted
// while its finally steps ran has neither completed nor been cancelled.
// It returns the new state and the commands for the host: a CancelStep
// for each attempt in flight (without StepKind, which needs the
// definition), then RunFailed. A run that has not started or has finished
// is returned unchanged, with no commands. Like Apply, it treats an at
// earlier than the state's UpdatedAt as UpdatedAt.
//
// Prefer delivering an event to Apply. Abort is the last resort of a host
// that has retried and given up; the postgres host uses it after
// MaxClaims.
func Abort(st *State, err *RunError, at time.Time) (*State, []Command) {
	if st == nil || st.Status == "" || st.Status.Terminal() {
		return st, nil
	}
	at = at.UTC()
	if at.Before(st.UpdatedAt) {
		at = st.UpdatedAt
	}

	next := st.clone()
	var cmds []Command
	for _, key := range sortedKeys(next.nodes) {
		n := next.nodes[key]
		if d := n.Decision; d != nil && d.Unit.Status == UnitRunning {
			d.Unit.Status = UnitCancelled
			cmds = append(cmds, Command{
				Kind:    CommandCancelStep,
				StepID:  makeDecisionID(key, d.Unit.Attempt),
				Step:    n.Step,
				Index:   -1,
				Attempt: d.Unit.Attempt,
			})
		}
		if a := n.Approval; a != nil && a.Unit.Status == UnitRunning {
			a.Unit.Status = UnitCancelled
			cmds = append(cmds, Command{
				Kind:    CommandCancelStep,
				StepID:  makeApprovalID(key, a.Unit.Attempt),
				Step:    n.Step,
				Index:   -1,
				Attempt: a.Unit.Attempt,
			})
		}
		if n.Status != nodeRunning {
			continue
		}
		for i, u := range n.Units {
			switch {
			case u.Status == UnitRunning && n.held(i):
				u.Status = UnitCancelled // settled for the host already
			case u.Status == UnitRunning:
				u.Status = UnitCancelled
				cmds = append(cmds, Command{
					Kind:    CommandCancelStep,
					StepID:  makeStepID(key, n.unitIndex(i), u.Attempt),
					Step:    n.Step,
					Index:   n.unitIndex(i),
					Attempt: u.Attempt,
				})
			case u.Status == UnitPending:
				u.Status = UnitSkipped
			}
		}
		n.Status, n.SettledAt = nodeCancelled, at
	}

	for _, p := range next.paths {
		if p.Status != pathEnded {
			p.Status, p.Ended = pathEnded, "cancelled"
		}
	}

	clear(next.joins)
	next.timeouts = nil
	e := *err
	e.At = at
	next.Status, next.Error, next.FinishedAt, next.UpdatedAt = StatusFailed, &e, at, at
	next.Outputs, next.Reason = nil, ""
	cmds = append(cmds, Command{
		Kind:          CommandRunFailed,
		Error:         next.Error,
		FinallyErrors: next.FinallyErrors,
		Index:         -1,
	})
	return next, cmds
}
