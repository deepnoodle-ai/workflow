package workflow

import (
	"fmt"
	"strings"
)

// Resolve returns the StepID an event names. id may be a StepID, returned
// as it is, or the name of a step (approve), so a caller that delivers a
// result needs only the run ID and the step name. A name resolves to:
//
//   - the StepID of the step's one running attempt;
//   - with no attempt running and the step's latest visit completed, the
//     StepID of its attempt, so Apply ignores the event as the duplicate
//     it is;
//   - ErrNotWaiting when the run has not reached the step, or when its
//     latest visit failed, timed out or was cancelled: the answer is not
//     taken, and the message says the step is no longer waiting;
//   - ErrInvalidEvent when more than one attempt is running, such as the
//     step on two parallel paths; the message lists their StepIDs;
//   - ErrUnknownStep when the definition has no such step.
//
// A host calls Resolve on the state it is about to pass to Apply, and
// applies and stores the event with the StepID it returns, so its event
// log holds full StepIDs. Apply itself accepts only a StepID.
func Resolve(def *Definition, st *State, id StepID) (StepID, error) {
	if def == nil || !def.validated {
		return "", ErrNotValidated
	}
	if id == "" || strings.ContainsAny(string(id), "/#") {
		return id, nil // a StepID, or malformed: Apply reports it
	}
	if def.byName[string(id)] == nil {
		return "", fmt.Errorf("%w: %s", ErrUnknownStep, id)
	}
	if st == nil || st.Status == "" {
		return id, nil // not started: Apply rejects the event
	}
	// A finished run keeps the latest visit of each step, so the rules
	// below hold for it too: a late answer to a wait that timed out is
	// refused, not ignored.
	if st.DefinitionHash != def.hash {
		return "", ErrDefinitionMismatch
	}

	var running []StepID
	var latest *nodeState
	for _, key := range sortedKeys(st.nodes) {
		n := st.nodes[key]
		if n.Step != string(id) {
			continue
		}
		latest = laterOf(latest, n)
		for i, u := range n.Units {
			if u.Status == UnitRunning {
				running = append(running, makeStepID(key, n.unitIndex(i), u.Attempt))
			}
		}
	}
	switch {
	case len(running) == 1:
		return running[0], nil
	case len(running) > 1:
		ids := make([]string, len(running))
		for i, r := range running {
			ids[i] = string(r)
		}
		return "", fmt.Errorf("%w: step %s has %d attempts running (%s); name one",
			ErrInvalidEvent, id, len(running), strings.Join(ids, ", "))
	case latest == nil:
		return "", fmt.Errorf("%w: the run has not reached step %s", ErrNotWaiting, id)
	case latest.Status == nodeCompleted:
		for i, u := range latest.Units {
			if u.Attempt > 0 {
				return makeStepID(latest.Key, latest.unitIndex(i), u.Attempt), nil
			}
		}
	}
	return "", fmt.Errorf("%w: step %s is no longer waiting (%s)", ErrNotWaiting, id, latest.Status)
}
