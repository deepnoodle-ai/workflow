package workflow

import "time"

// Abort fails a run that its host cannot advance with Apply, such as one
// whose definition no longer loads. It needs no definition, so it runs
// neither on_failure nor finally steps. Every attempt in flight is
// cancelled and the run fails with err, stamped at. It returns the new
// state and the commands for the host: a CancelStep for each attempt in
// flight (without StepKind, which needs the definition), then RunFailed.
// A finished run is returned unchanged, with no commands. Like Apply, it
// treats an at earlier than the state's UpdatedAt as UpdatedAt.
//
// Prefer delivering an event to Apply. Abort is the last resort of a host
// that has retried and given up; the postgres host uses it after
// MaxClaims.
func Abort(st *State, err *RunError, at time.Time) (*State, []Command) {
	if st == nil || st.Status.Terminal() {
		return st, nil
	}
	if at.Before(st.UpdatedAt) {
		at = st.UpdatedAt
	}
	next := st.clone()
	var cmds []Command
	for _, key := range sortedKeys(next.nodes) {
		n := next.nodes[key]
		if n.Status != nodeRunning {
			continue
		}
		for i, u := range n.Units {
			switch u.Status {
			case UnitRunning:
				u.Status = UnitCancelled
				cmds = append(cmds, Command{Kind: CommandCancelStep, StepID: makeStepID(key, n.unitIndex(i), u.Attempt),
					Step: n.Step, Index: n.unitIndex(i)})
			case UnitPending:
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
	e := *err
	e.At = at
	next.Status, next.Error, next.FinishedAt, next.UpdatedAt = StatusFailed, &e, at, at
	cmds = append(cmds, Command{Kind: CommandRunFailed, Error: next.Error, Index: -1})
	return next, cmds
}
