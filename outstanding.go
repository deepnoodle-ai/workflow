package workflow

// Outstanding returns the commands for the work a run has in flight: a
// DispatchStep (plus its timeout timer) for every running attempt, a
// ScheduleTimer for every sleep, and only the timeout timer for a wait. It is what Apply emitted for that work,
// rebuilt from the state.
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
			if s.Timeout > 0 {
				out = append(out, r.timeoutCommand(s, cmd))
			}
		}
	}
	return out, nil
}
