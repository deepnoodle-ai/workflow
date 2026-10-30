package workflow

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// testKinds are the host kinds the tests use.
var testKinds = KindMap{
	"agent":  nil,
	"action": nil,
}

func mustLoad(t testing.TB, doc string) *Definition {
	t.Helper()
	def, err := Load([]byte(doc), testKinds)
	require.NoError(t, err)
	return def
}

// lenientJoins is a LoadOption that turns off the Load check that a
// join's named steps settle, so a test can reach the run-time check.
func lenientJoins(d *Definition) { d.lenientJoins = true }

// mustLoadLenient loads a definition whose joins may fail at run time.
func mustLoadLenient(t testing.TB, doc string) *Definition {
	t.Helper()
	def, err := Load([]byte(doc), testKinds, lenientJoins)
	require.NoError(t, err)
	return def
}

// result is what a fake handler returns for one dispatch.
type result struct {
	output any
	err    *StepError
}

// fakeHost drives a run the way a durable host would: every Apply result is
// round-tripped through JSON, as if stored and reloaded, before the next
// event.
type fakeHost struct {
	t       testing.TB
	def     *Definition
	st      *State
	now     time.Time
	pending map[StepID]Command // dispatched, not yet answered
	timers  map[StepID]Command
	all     []Command
	final   *Command
	handle  func(Command) result
}

func newHost(t testing.TB, def *Definition, handle func(Command) result) *fakeHost {
	return &fakeHost{
		t:       t,
		def:     def,
		now:     t0,
		handle:  handle,
		pending: map[StepID]Command{},
		timers:  map[StepID]Command{},
	}
}

func (h *fakeHost) apply(ev Event) []Command {
	h.t.Helper()
	if ev.At.IsZero() {
		ev.At = h.now
	}
	var before []byte
	if h.st != nil {
		before = stateBytes(h.t, h.st)
	}
	next, cmds, err := Apply(h.def, h.st, ev)
	require.NoError(h.t, err)
	if h.st != nil {
		// Apply never modifies its input.
		require.Equal(h.t, before, stateBytes(h.t, h.st))
	}
	h.st = roundTrip(h.t, next)
	checkPathRefs(h.t, h.st)
	for _, c := range cmds {
		h.all = append(h.all, c)
		switch c.Kind {
		case CommandDispatchStep:
			h.pending[c.StepID] = c
		case CommandCancelStep:
			delete(h.pending, c.StepID)
			delete(h.timers, c.StepID)
		case CommandScheduleTimer:
			h.timers[c.StepID] = c
			if c.StepKind == KindWait {
				// A wait's attempt has no dispatch: its timeout timer is
				// the one sign of it. The handler stands in for the
				// outside world that delivers the result.
				d := c
				d.Kind = CommandDispatchStep
				d.NotBefore = c.NotBefore.Add(-h.def.byName[c.Step].Timeout)
				h.pending[c.StepID] = d
			}
		case CommandRunCompleted, CommandRunFailed, CommandRunCancelled:
			c := c
			h.final = &c
		}
	}
	return cmds
}

func (h *fakeHost) start(input map[string]any) {
	h.apply(Event{Kind: EventStart, Input: input})
}

// deliver answers one pending dispatch.
func (h *fakeHost) deliver(id StepID) []Command {
	h.t.Helper()
	c, ok := h.pending[id]
	require.True(h.t, ok, "no pending dispatch %s", id)
	delete(h.pending, id)
	delete(h.timers, id) // the step's timeout timer, if any
	if c.NotBefore.After(h.now) {
		h.now = c.NotBefore
	}
	res := h.handle(c)
	if res.err != nil {
		return h.apply(Event{Kind: EventStepFailed, StepID: id, Error: res.err})
	}
	return h.apply(Event{Kind: EventStepCompleted, StepID: id, Output: res.output})
}

// fire delivers the earliest timer.
func (h *fakeHost) fire() []Command {
	h.t.Helper()
	var first *Command
	for _, c := range h.timers {
		if first == nil || c.NotBefore.Before(first.NotBefore) ||
			(c.NotBefore.Equal(first.NotBefore) && c.StepID < first.StepID) {
			c := c
			first = &c
		}
	}
	require.NotNil(h.t, first, "no timer to fire")
	delete(h.timers, first.StepID)
	if first.NotBefore.After(h.now) {
		h.now = first.NotBefore
	}
	return h.apply(Event{Kind: EventTimerFired, StepID: first.StepID})
}

func (h *fakeHost) pendingIDs() []StepID {
	ids := make([]StepID, 0, len(h.pending))
	for id := range h.pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// runToEnd answers dispatches in an order chosen by rng (sorted when rng is
// nil) and fires timers when nothing else is pending.
func (h *fakeHost) runToEnd(rng *rand.Rand) {
	h.t.Helper()
	for i := 0; h.final == nil; i++ {
		require.True(h.t, i < 10000, "run did not finish")
		ids := h.pendingIDs()
		if len(ids) == 0 {
			if len(h.timers) == 0 {
				h.t.Fatalf("run is stuck: status %s, no work in flight", h.st.Status)
			}
			h.fire()
			continue
		}
		pick := ids[0]
		if rng != nil {
			pick = ids[rng.Intn(len(ids))]
		}
		h.deliver(pick)
	}
}

func (h *fakeHost) dispatched() []StepID {
	var ids []StepID
	for _, c := range h.all {
		if c.Kind == CommandDispatchStep {
			ids = append(ids, c.StepID)
		}
	}
	slices.Sort(ids)
	return ids
}

// stateBytes snapshots the complete persisted state for the mutation check.
// Calling the marshaler directly avoids a second JSON scan and byte copy by
// json.Marshal. roundTrip still exercises the encoding/json entry points.
func stateBytes(t testing.TB, st *State) []byte {
	t.Helper()
	data, err := st.MarshalJSON()
	require.NoError(t, err)
	return data
}

func roundTrip(t testing.TB, st *State) *State {
	t.Helper()
	data, err := json.Marshal(st)
	require.NoError(t, err)
	var out State
	require.NoError(t, json.Unmarshal(data, &out))
	return &out
}

// checkPathRefs checks that every path the state names is in it: dropping
// path records must not leave a parent, a waiting path or a running
// step's path behind, and every live path keeps its whole lineage, which
// a join's common ancestor is found on.
func checkPathRefs(t testing.TB, st *State) {
	t.Helper()
	for _, p := range st.paths {
		if p.Parent != "" {
			require.NotNil(t, st.paths[p.Parent], "path %s: parent %s", p.ID, p.Parent)
		}
		if p.Status == pathEnded || p.ID == failurePath || p.ID == finallyPath {
			continue
		}
		id := p.ID
		for st.paths[id].Parent != "" {
			id = st.paths[id].Parent
		}
		require.Equal(t, mainPath, id, "path %s: lineage", p.ID)
	}
	for join, ids := range st.joins {
		for _, id := range ids {
			require.NotNil(t, st.paths[id], "join %s: waiting path %s", join, id)
		}
	}
	for key, n := range st.nodes {
		if n.Status == nodeRunning {
			require.NotNil(t, st.paths[n.Path], "running node %s", key)
		}
	}
}

func mustJSON(t testing.TB, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}

func ok(output any) result { return result{output: output} }

func fail(typ, msg string, retryable bool) result {
	return result{err: &StepError{Type: typ, Message: msg, Retryable: retryable}}
}

func idsOf(cmds []Command, kind CommandKind) []string {
	var out []string
	for _, c := range cmds {
		if c.Kind == kind {
			out = append(out, string(c.StepID))
		}
	}
	return out
}

func fmtItems(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = fmt.Sprintf("item-%d", i)
	}
	return out
}
