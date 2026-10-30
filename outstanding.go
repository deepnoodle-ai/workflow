package workflow

import "fmt"

// Outstanding returns the commands for the work a run has in flight: a
// DispatchStep (plus its timeout timer) for every running attempt, a
// decision job's included, a ScheduleTimer for every sleep, and only the
// timeout timer for a wait. A timeout that fired while the run was paused
// is not rebuilt until Resume re-arms it. It is what Apply emitted for that
// work, rebuilt from the state.
//
// A durable host that stores commands in an outbox does not need it. An
// in-process driver resuming from a saved state does, and so does a host
// that wants to re-drive work it may have lost. Issuing a command twice is
// safe: results are idempotent by StepID.
func Outstanding(def *Definition, st *State) ([]Command, error) {
	if def == nil || !def.validated {
		return nil, ErrNotValidated
	}
	if st == nil || st.Status == "" || st.Status.Terminal() {
		return nil, nil
	}
	if st.DefinitionHash != def.hash {
		return nil, ErrDefinitionMismatch
	}

	var out []Command
	for _, key := range sortedKeys(st.nodes) {
		n := st.nodes[key]
		if d := n.Decision; d != nil && d.Unit.Status == UnitRunning {
			r := &run{def: def, st: st, at: d.Unit.DispatchedAt}
			dispatch, timer, rerr := r.decisionCommands(n, st.paths[n.Path], d.Unit.Attempt,
				d.Unit.NotBefore)
			if rerr != nil {
				return nil, fmt.Errorf("%s: %s", rerr.Step, rerr.Message)
			}
			out = append(out, dispatch)
			if t, held := st.timeouts[string(dispatch.StepID)]; !held {
				out = append(out, timer)
			} else if !t.IsZero() {
				timer.NotBefore = t
				out = append(out, timer)
			}
		}
		if n.Status != nodeRunning {
			continue
		}
		s := def.byName[n.Step]
		for i, u := range n.Units {
			if u.Status != UnitRunning {
				continue
			}
			r := &run{def: def, st: st, at: u.DispatchedAt}
			if s.Kind == KindSleep {
				out = append(out, Command{
					Kind:      CommandScheduleTimer,
					StepID:    makeStepID(key, -1, u.Attempt),
					Step:      s.Name,
					StepKind:  KindSleep,
					Index:     -1,
					Attempt:   u.Attempt,
					NotBefore: u.DispatchedAt.Add(s.Sleep),
				})
				continue
			}
			cmd, err := r.dispatchCommand(n, i, u.Attempt, u.NotBefore)
			if err != nil {
				return nil, err
			}
			if s.Kind != KindWait {
				out = append(out, cmd)
			}
			if s.Timeout == 0 {
				continue
			}
			timer := r.timeoutCommand(s, cmd)
			if d, held := st.timeouts[string(cmd.StepID)]; held {
				// Fired during a pause (zero), or re-armed by Resume.
				if d.IsZero() {
					continue
				}
				timer.NotBefore = d
			}
			out = append(out, timer)
		}
	}
	return out, nil
}
