package workflow

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// pauseDoc forks into two steps that a join merges; charge retries.
const pauseDoc = `{
  "steps": [
    {"name": "start", "action": "x", "next": ["left", "right"]},
    {"name": "left", "action": "x", "next": "j"},
    {"name": "right", "action": "x", "next": "j"},
    {"name": "j", "join": ["left", "right"], "next": "charge"},
    {"name": "charge", "action": "x", "timeout": "1m",
     "retry": {"max_attempts": 2, "backoff": "30s"}}
  ],
  "on_failure": [{"name": "report", "action": "x"}],
  "finally": [{"name": "release", "action": "x"}]
}`

func pauseHandler(failing bool) func(Command) result {
	return func(c Command) result {
		if failing && c.Step == "charge" {
			return fail("down", "payment API down", true)
		}
		return ok(nil)
	}
}

func TestPauseKeepsTheRunProgressing(t *testing.T) {
	h := newHost(t, mustLoad(t, pauseDoc), pauseHandler(false))
	h.start(nil)
	cmds := h.apply(Event{Kind: EventPause, Reason: "payment API down"})
	require.Equal(t, 0, len(cmds))
	require.Equal(t, StatusPaused, h.st.Status)
	require.Equal(t, "payment API down", h.st.Reason)
	require.False(t, h.st.Status.Terminal())
	require.Equal(t, RunStatus(""), h.st.Status.Outcome())

	// Results are recorded, the fork and the join run, and dispatches are
	// emitted: the host, not Apply, holds them.
	h.deliver("main/start#1")
	h.deliver("main/left#1")
	cmds = h.deliver("main.right/right#1")
	require.Equal(t, []string{"main/charge#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, StatusPaused, h.st.Status)
	out, err := Outstanding(h.def, h.st)
	require.NoError(t, err)
	require.Equal(t, []string{"main/charge#1"}, idsOf(out, CommandDispatchStep))

	cmds = h.apply(Event{Kind: EventResume})
	require.Equal(t, 0, len(cmds))
	require.Equal(t, StatusRunning, h.st.Status)
	require.Equal(t, "", h.st.Reason)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
}

func TestPauseAndResumeAreNoOpsOutsideTheirStatus(t *testing.T) {
	def := mustLoad(t, pauseDoc)
	_, _, err := Apply(def, nil, Event{At: t0, Kind: EventPause})
	require.True(t, errors.Is(err, ErrNotStarted))

	h := newHost(t, def, pauseHandler(false))
	h.start(nil)
	noop := func(ev Event) {
		t.Helper()
		ev.At = h.now
		next, cmds, err := Apply(def, h.st, ev)
		require.NoError(t, err)
		require.True(t, next == h.st, "%s on %s changed the state", ev.Kind, h.st.Status)
		require.Equal(t, 0, len(cmds))
	}

	noop(Event{Kind: EventResume})
	h.apply(Event{Kind: EventPause, Reason: "first"})
	noop(Event{Kind: EventPause, Reason: "second"})
	require.Equal(t, "first", h.st.Reason)

	h.apply(Event{Kind: EventCancel, Reason: "stop"})
	require.Equal(t, StatusCancelling, h.st.Status)
	noop(Event{Kind: EventPause})
	noop(Event{Kind: EventResume})
	h.runToEnd(nil)
	require.Equal(t, StatusCancelled, h.st.Status)
	noop(Event{Kind: EventPause})
	noop(Event{Kind: EventResume})
}

func TestCompletionWhilePausedEndsThePause(t *testing.T) {
	h := newHost(t, mustLoad(t, pauseDoc), pauseHandler(false))
	h.start(nil)
	h.deliver("main/start#1")
	h.deliver("main/left#1")
	h.deliver("main.right/right#1")
	h.apply(Event{Kind: EventPause, Reason: "look"})
	cmds := h.deliver("main/charge#1")

	// The outcome is decided: the finally steps start with no Resume.
	require.Equal(t, StatusCompleting, h.st.Status)
	require.Equal(t, "", h.st.Reason)
	require.Equal(t, []string{"finally/release#1"}, idsOf(cmds, CommandDispatchStep))
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
}

func TestFailureWhilePausedEndsThePause(t *testing.T) {
	h := newHost(t, mustLoad(t, pauseDoc), pauseHandler(true))
	h.start(nil)
	h.deliver("main/start#1")
	h.deliver("main/left#1")
	h.deliver("main.right/right#1")
	h.apply(Event{Kind: EventPause, Reason: "look"})

	// The first attempt fails while paused: its retry is dispatched, for
	// the host to hold.
	cmds := h.deliver("main/charge#1")
	require.Equal(t, []string{"main/charge#2"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, StatusPaused, h.st.Status)

	// The last attempt, one the host started before the pause, fails: the
	// run fails, and on_failure starts with no Resume.
	cmds = h.deliver("main/charge#2")
	require.Equal(t, StatusFailing, h.st.Status)
	require.Equal(t, "", h.st.Reason)
	require.Equal(t, "down", h.st.Error.Type)
	require.Equal(t, []string{"failure/report#1"}, idsOf(cmds, CommandDispatchStep))
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
}

// TestPauseStopsTheTimeoutClock: a timeout that fires while the run is
// paused uses up no attempt. Resume gives the attempt a new, full timeout.
func TestPauseStopsTheTimeoutClock(t *testing.T) {
	h := newHost(t, mustLoad(t, pauseDoc), pauseHandler(true))
	h.start(nil)
	h.deliver("main/start#1")
	h.deliver("main/left#1")
	h.deliver("main.right/right#1")
	h.deliver("main/charge#1") // fails: charge#2 waits for its backoff
	h.apply(Event{Kind: EventPause, Reason: "look"})

	// The host holds charge#2. Its timeout fires during the pause.
	cmds := h.fire()
	require.Equal(t, 0, len(cmds))
	require.Equal(t, StatusPaused, h.st.Status)
	require.Equal(t, UnitRunning, h.st.Unit("main/charge#2").Status)
	out, err := Outstanding(h.def, h.st)
	require.NoError(t, err)
	require.Equal(t, []string{"main/charge#2"}, idsOf(out, CommandDispatchStep))
	require.Equal(t, 0, len(idsOf(out, CommandScheduleTimer)))

	// A redelivered timer changes nothing.
	next, cmds, err := Apply(h.def, h.st,
		Event{At: h.now, Kind: EventTimerFired, StepID: "main/charge#2"})
	require.NoError(t, err)
	require.True(t, next == h.st)
	require.Equal(t, 0, len(cmds))

	// Resume an hour later: a new, full timeout from then.
	h.now = h.now.Add(time.Hour)
	cmds = h.apply(Event{Kind: EventResume})
	require.Equal(t, 1, len(cmds))
	require.Equal(t, CommandScheduleTimer, cmds[0].Kind)
	require.Equal(t, StepID("main/charge#2"), cmds[0].StepID)
	require.Equal(t, 2, cmds[0].Attempt)
	require.Equal(t, h.now.Add(time.Minute), cmds[0].NotBefore)
	out, err = Outstanding(h.def, h.st)
	require.NoError(t, err)
	require.Equal(t, h.now.Add(time.Minute), out[1].NotBefore)

	// That timer fails the attempt as usual: the last one, so the run
	// fails.
	h.fire()
	require.Equal(t, StatusFailing, h.st.Status)
	require.Equal(t, ErrorTypeTimeout, h.st.Error.Type)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
}

// TestPauseStopsADecisionsTimeoutClock: a decision's timeout is held like
// a step's, so a long pause does not use up its attempts.
func TestPauseStopsADecisionsTimeoutClock(t *testing.T) {
	q := map[string]any{"Is this refund legitimate?": 0.95}
	h := newHost(t, mustLoad(t, refundDoc), answers(q, body))
	h.start(nil)
	h.deliver("main/intake#1")
	h.apply(Event{Kind: EventPause})

	cmds := h.fire()
	require.Equal(t, 0, len(cmds))
	require.Equal(t, UnitRunning, h.st.Unit("main/intake.next#1").Status)
	out, err := Outstanding(h.def, h.st)
	require.NoError(t, err)
	require.Equal(t, []string{"main/intake.next#1"}, idsOf(out, CommandDispatchStep))
	require.Equal(t, 0, len(idsOf(out, CommandScheduleTimer)))

	h.now = h.now.Add(time.Hour)
	cmds = h.apply(Event{Kind: EventResume})
	require.Equal(t, 1, len(cmds))
	require.Equal(t, CommandScheduleTimer, cmds[0].Kind)
	require.Equal(t, KindDecision, cmds[0].StepKind)
	require.Equal(t, h.now.Add(time.Minute), cmds[0].NotBefore)
	out, err = Outstanding(h.def, h.st)
	require.NoError(t, err)
	require.Equal(t, h.now.Add(time.Minute), out[1].NotBefore)

	cmds = h.deliver("main/intake.next#1")
	require.Equal(t, []string{"main/approve#1"}, idsOf(cmds, CommandDispatchStep))
}

// TestASecondPauseKeepsTheReArmedDeadline: a timeout re-armed by the first
// Resume has a live timer, so a second pause and Resume neither add a
// timer nor move its deadline.
func TestASecondPauseKeepsTheReArmedDeadline(t *testing.T) {
	h := newHost(t, mustLoad(t, pauseDoc), pauseHandler(true))
	h.start(nil)
	h.deliver("main/start#1")
	h.deliver("main/left#1")
	h.deliver("main.right/right#1")
	h.deliver("main/charge#1")
	h.apply(Event{Kind: EventPause})
	h.fire() // charge#2's timeout, held
	h.now = h.now.Add(time.Hour)
	cmds := h.apply(Event{Kind: EventResume})
	deadline := h.now.Add(time.Minute)
	require.Equal(t, deadline, cmds[0].NotBefore)

	h.now = h.now.Add(10 * time.Second)
	h.apply(Event{Kind: EventPause})
	h.now = h.now.Add(10 * time.Second)
	cmds = h.apply(Event{Kind: EventResume})
	require.Equal(t, 0, len(cmds))
	out, err := Outstanding(h.def, h.st)
	require.NoError(t, err)
	require.Equal(t, []string{"main/charge#2"}, idsOf(out, CommandScheduleTimer))
	require.Equal(t, deadline, out[1].NotBefore)
}

// TestAHeldTimeoutGoesWithItsAttempt: the entry for a timeout held during
// a pause goes when its attempt settles, so it does not linger for the
// next attempt of the same unit.
func TestAHeldTimeoutGoesWithItsAttempt(t *testing.T) {
	h := newHost(t, mustLoad(t, `{"steps": [
	  {"name": "a", "action": "x", "timeout": "1m", "next": "b"},
	  {"name": "b", "action": "x"}]}`), pauseHandler(false))
	h.start(nil)
	h.apply(Event{Kind: EventPause})
	h.fire() // a's timeout, held
	require.Equal(t, 1, len(h.st.timeouts))

	// A handler the host started before the pause completes during it.
	cmds := h.deliver("main/a#1")
	require.Equal(t, []string{"main/b#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, StatusPaused, h.st.Status)
	require.Equal(t, 0, len(h.st.timeouts))
	cmds = h.apply(Event{Kind: EventResume})
	require.Equal(t, 0, len(cmds))
}

// TestASleepEndsDuringAPause: the sleep settles when its timer fires, and
// the next step is dispatched for the host to hold. A sleep is not a
// timeout, so nothing is re-armed.
func TestASleepEndsDuringAPause(t *testing.T) {
	h := newHost(t, mustLoad(t, `{"steps": [
	  {"name": "nap", "sleep": "1h", "next": "ship"},
	  {"name": "ship", "action": "x"}]}`), pauseHandler(false))
	h.start(nil)
	h.apply(Event{Kind: EventPause})
	cmds := h.fire()
	require.Equal(t, []string{"main/ship#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, StatusPaused, h.st.Status)
	require.Equal(t, 0, len(h.st.timeouts))
	require.Equal(t, 0, len(h.apply(Event{Kind: EventResume})))
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
}

func TestCancelAPausedRun(t *testing.T) {
	h := newHost(t, mustLoad(t, pauseDoc), pauseHandler(false))
	h.start(nil)
	h.apply(Event{Kind: EventPause, Reason: "look"})
	cmds := h.apply(Event{Kind: EventCancel, Reason: "give up"})
	require.Equal(t, []string{"main/start#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, []string{"finally/release#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, StatusCancelling, h.st.Status)
	require.Equal(t, "give up", h.st.Reason)
	h.runToEnd(nil)
	require.Equal(t, StatusCancelled, h.st.Status)
	require.Equal(t, "give up", h.final.Reason)
}

// TestPauseChangesNothingRandomGraphs pauses and resumes random graphs at
// random points. Apply advances a paused run as a running one, so every
// run ends in the same state with the same dispatches as without pauses.
func TestPauseChangesNothingRandomGraphs(t *testing.T) {
	t.Parallel()
	for graph := int64(200); graph < 200+int64(sized(8, 40)); graph++ {
		t.Run(fmt.Sprintf("graph_%d", graph), func(t *testing.T) {
			t.Parallel()
			def := randomWorkflow(t, graph)
			ref := newHost(t, def, deterministicHandler)
			ref.start(nil)
			ref.runToEnd(nil)

			rng := rand.New(rand.NewSource(graph))
			h := newHost(t, def, deterministicHandler)
			h.start(nil)
			for i := 0; h.final == nil; i++ {
				require.True(t, i < 10000, "run did not finish")
				if rng.Intn(3) == 0 {
					kind := EventPause
					if h.st.Status == StatusPaused {
						kind = EventResume
					}
					h.apply(Event{Kind: kind, Reason: "look"})
					continue
				}
				if ids := h.pendingIDs(); len(ids) > 0 {
					h.deliver(ids[0])
				} else {
					h.fire()
				}
			}
			require.Equal(t, mustJSON(t, ref.st), mustJSON(t, h.st), "graph %d", graph)
			require.Equal(t, ref.dispatched(), h.dispatched(), "graph %d", graph)
		})
	}
}
