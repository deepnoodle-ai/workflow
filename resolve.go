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
//   - with no attempt running, the StepID of an attempt that has settled,
//     so Apply ignores the event as the duplicate it is;
//   - ErrNotWaiting when no attempt of the step has started;
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
	if st == nil || st.Status == "" || st.Status.Terminal() {
		return id, nil // Apply rejects or ignores the event as it is
	}
	if st.DefinitionHash != def.hash {
		return "", ErrDefinitionMismatch
	}

	var running []StepID
	var settled StepID
	for _, key := range sortedKeys(st.nodes) {
		n := st.nodes[key]
		if n.Step != string(id) {
			continue
		}
		for i, u := range n.Units {
			switch {
			case u.Status == UnitRunning:
				running = append(running, makeStepID(key, n.unitIndex(i), u.Attempt))
			case u.Attempt > 0:
				settled = makeStepID(key, n.unitIndex(i), u.Attempt)
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
	case settled != "":
		return settled, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotWaiting, id)
}
