package postgres

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/deepnoodle-ai/workflow/local"
)

// The conformance suite runs the same definitions, with the same scripted
// handler, on every host: a pure driver that calls workflow.Apply directly,
// local.Runner, and the postgres Store and Worker. Each host must reach the
// same outcome and dispatch the same step attempts. Timestamps differ
// between hosts and are not compared.

// conformanceHost runs a definition to the end with a handler.
type conformanceHost interface {
	name() string
	run(
		t *testing.T,
		def *workflow.Definition,
		input map[string]any,
		h workflow.Handler,
	) hostOutcome
}

// hostOutcome is what a host reports about a finished run.
type hostOutcome struct {
	State *workflow.State
	// Dispatched are the StepIDs of the host-kind attempts the host
	// dispatched, sorted.
	Dispatched []workflow.StepID
}

// runSummary is the part of a run's outcome every host must agree on.
type runSummary struct {
	Status        workflow.RunStatus
	Outputs       string // JSON, so number types do not matter
	Error         *errSummary
	FinallyErrors []errSummary
	Dispatched    []workflow.StepID
}

type errSummary struct {
	Step, StepID, Type, Message string
	OnFailure                   *errSummary
}

func summarizeErr(e *workflow.RunError) *errSummary {
	if e == nil {
		return nil
	}
	return &errSummary{
		Step:      e.Step,
		StepID:    string(e.StepID),
		Type:      e.Type,
		Message:   e.Message,
		OnFailure: summarizeErr(e.OnFailure),
	}
}

func summarize(t *testing.T, o hostOutcome) runSummary {
	t.Helper()
	outputs, err := json.Marshal(o.State.Outputs)
	require.NoError(t, err)
	s := runSummary{
		Status:     o.State.Status,
		Outputs:    string(outputs),
		Error:      summarizeErr(o.State.Error),
		Dispatched: o.Dispatched,
	}
	for _, e := range o.State.FinallyErrors {
		s.FinallyErrors = append(s.FinallyErrors, *summarizeErr(e))
	}
	return s
}

// conformanceCase is one definition, its input and what every host must
// report for it.
type conformanceCase struct {
	name  string
	doc   string
	input map[string]any
	// check inspects the outcome once, beyond agreement between hosts.
	check func(t *testing.T, s runSummary)
}

// runConformance runs every case on every host and requires the hosts to
// agree with the first.
func runConformance(
	t *testing.T, hosts []conformanceHost, cases []conformanceCase, h workflow.Handler,
) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			def := load(t, c.doc)
			var want runSummary
			for i, host := range hosts {
				got := summarize(t, host.run(t, def, c.input, h))
				if i == 0 {
					want = got
					if c.check != nil {
						c.check(t, got)
					}
					continue
				}
				require.Equal(t, want, got, "%s disagrees with %s", host.name(), hosts[0].name())
			}
		})
	}
}

// applyHost drives a run with workflow.Apply in a loop, in memory. It
// answers dispatches in the order they were made, fires timers only when
// nothing else is pending, and round-trips the state through JSON on every
// event, as a durable host stores and reloads it.
type applyHost struct{}

func (applyHost) name() string { return "apply" }

func (applyHost) run(
	t *testing.T, def *workflow.Definition, input map[string]any, h workflow.Handler,
) hostOutcome {
	t.Helper()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	var (
		queue      []workflow.Command
		timers     = map[workflow.StepID]workflow.Command{}
		dispatched []workflow.StepID
	)
	carryOut := func(cmds []workflow.Command) {
		for _, c := range cmds {
			switch c.Kind {
			case workflow.CommandDispatchStep:
				queue = append(queue, c)
				dispatched = append(dispatched, c.StepID)
			case workflow.CommandCancelStep:
				queue = slices.DeleteFunc(queue, func(q workflow.Command) bool {
					return q.StepID == c.StepID
				})
				delete(timers, c.StepID)
			case workflow.CommandScheduleTimer:
				timers[c.StepID] = c
			}
		}
	}
	st, cmds, err := workflow.Apply(def, nil,
		workflow.Event{At: now, Kind: workflow.EventStart, Input: input})
	require.NoError(t, err)
	carryOut(cmds)
	for i := 0; !st.Status.Terminal(); i++ {
		require.True(t, i < 10000, "run did not finish")
		st = roundTripState(t, st)
		var ev workflow.Event
		switch {
		case len(queue) > 0:
			c := queue[0]
			queue = queue[1:]
			delete(timers, c.StepID) // settled, so its timeout timer is obsolete
			if c.NotBefore.After(now) {
				now = c.NotBefore
			}
			out, err := h(context.Background(), workflow.Job{RunID: "apply", Claims: 1, Command: c})
			ev = workflow.Event{
				Kind:   workflow.EventStepCompleted,
				StepID: c.StepID,
				Output: out,
			}
			if err != nil {
				ev = workflow.Event{
					Kind:   workflow.EventStepFailed,
					StepID: c.StepID,
					Error:  workflow.StepErrorOf(err),
				}
			}
		case len(timers) > 0:
			var first *workflow.Command
			for _, c := range timers {
				if first == nil || c.NotBefore.Before(first.NotBefore) ||
					(c.NotBefore.Equal(first.NotBefore) && c.StepID < first.StepID) {
					first = &c
				}
			}
			delete(timers, first.StepID)
			if first.NotBefore.After(now) {
				now = first.NotBefore
			}
			ev = workflow.Event{Kind: workflow.EventTimerFired, StepID: first.StepID}
		default:
			t.Fatalf("run is stuck: status %s, nothing in flight", st.Status)
		}
		now = now.Add(time.Millisecond)
		ev.At = now
		st, cmds, err = workflow.Apply(def, st, ev)
		require.NoError(t, err)
		carryOut(cmds)
	}
	slices.Sort(dispatched)
	return hostOutcome{State: roundTripState(t, st), Dispatched: dispatched}
}

func roundTripState(t *testing.T, st *workflow.State) *workflow.State {
	t.Helper()
	data, err := json.Marshal(st)
	require.NoError(t, err)
	var out workflow.State
	require.NoError(t, json.Unmarshal(data, &out))
	return &out
}

// recorder wraps a handler and records the StepIDs it is called with.
type recorder struct {
	mu  sync.Mutex
	ids []workflow.StepID
}

func (r *recorder) wrap(h workflow.Handler) workflow.Handler {
	return func(ctx context.Context, job workflow.Job) (any, error) {
		r.mu.Lock()
		if !slices.Contains(r.ids, job.StepID) {
			r.ids = append(r.ids, job.StepID)
		}
		r.mu.Unlock()
		return h(ctx, job)
	}
}

func (r *recorder) sorted() []workflow.StepID {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.ids)
	slices.Sort(out)
	return out
}

// localHost runs the definition on local.Runner; its dispatches are the
// handler's calls.
type localHost struct{}

func (localHost) name() string { return "local" }

func (localHost) run(
	t *testing.T, def *workflow.Definition, input map[string]any, h workflow.Handler,
) hostOutcome {
	t.Helper()
	var rec recorder
	r := &local.Runner{
		Handlers: map[string]workflow.Handler{
			"agent":  rec.wrap(h),
			"action": rec.wrap(h),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := r.Run(ctx, "local", def, input)
	require.NoError(t, err)
	return hostOutcome{State: st, Dispatched: rec.sorted()}
}

// postgresHost runs the definition on a Store and two Workers; its
// dispatches are the store's step records of host kinds.
type postgresHost struct{}

func (postgresHost) name() string { return "postgres" }

func (postgresHost) run(
	t *testing.T, def *workflow.Definition, input map[string]any, h workflow.Handler,
) hostOutcome {
	t.Helper()
	s := newStore(t)
	for range 2 {
		startWorker(t, &Worker{
			Store:    s,
			Handlers: map[string]workflow.Handler{"agent": h, "action": h},
		})
	}
	ctx := context.Background()
	res, err := s.Start(ctx, NewRun{Definition: def, Input: input})
	require.NoError(t, err)
	r := waitStatus(t, s, res.RunID,
		workflow.StatusCompleted, workflow.StatusFailed, workflow.StatusCancelled)
	records, err := s.Steps(ctx, res.RunID)
	require.NoError(t, err)
	var ids []workflow.StepID
	for _, rec := range records {
		if rec.StepKind != workflow.KindSleep {
			ids = append(ids, rec.StepID)
		}
	}
	slices.Sort(ids)
	return hostOutcome{State: r.State, Dispatched: ids}
}

// scripted is the conformance handler: a pure function of the job, so
// every host sees the same results.
func scripted(_ context.Context, job workflow.Job) (any, error) {
	switch job.Step {
	case "work":
		// Item 1 fails its first attempt; the step's retry recovers it.
		if job.Index == 1 && job.Attempt == 1 {
			return nil, &workflow.StepError{
				Type:      "unavailable",
				Message:   "try again",
				Retryable: true,
			}
		}
		return map[string]any{"item": job.Params["item"], "attempt": job.Attempt}, nil
	case "charge":
		return nil, &workflow.StepError{Type: "declined", Message: "card declined"}
	case "review":
		// Approve the third draft.
		return map[string]any{"approved": strings.Contains(job.StepID.Unit(), "@3")}, nil
	case "explode":
		return nil, &workflow.StepError{Type: "fatal", Message: "boom"}
	case "cleanup":
		return nil, &workflow.StepError{Type: "cleanup_failed", Message: "bucket locked"}
	}
	return map[string]any{"step": job.Step, "unit": job.StepID.Unit(), "params": job.Params}, nil
}

var conformanceCases = []conformanceCase{
	{
		name: "fan_out_join_each",
		doc: `{"steps": [
		  {"name": "start", "action": "x", "next": ["left", "right"]},
		  {"name": "left", "agent": "a", "with": {"side": "left"}, "next": ["gather"]},
		  {"name": "right", "agent": "a", "with": {"side": "right"}, "next": ["gather"]},
		  {"name": "gather", "join": ["left", "right"], "next": ["work"]},
		  {"name": "work", "action": "x", "each": "input.items", "concurrency": 2,
		   "with": {"item": "${{ item }}", "from": "${{ left.params.side }}"},
		   "retry": {"max_attempts": 3, "backoff": "1ms"}, "timeout": "1m"}],
		 "outputs": {"work": "${{ work }}", "sides": "${{ [left.params.side, right.params.side] }}"}}`,
		input: map[string]any{"items": []any{"a", "b", "c", "d"}},
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusCompleted, s.Status)
			require.Contains(t, s.Dispatched, workflow.StepID("main/work[1]#2"))
			require.Len(t, s.Dispatched, 3+5) // start, left, right; four items, one retried
		},
	},
	{
		name: "catch_route",
		doc: `{"steps": [
		  {"name": "charge", "action": "x", "catch": [{"errors": ["declined"], "next": "apologize"}], "next": ["ship"]},
		  {"name": "ship", "action": "x"},
		  {"name": "apologize", "agent": "a", "with": {"why": "${{ charge.error.message }}"}}],
		 "outputs": {"why": "${{ apologize.params.why }}", "shipped": "${{ ship != nil }}"}}`,
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusCompleted, s.Status)
			require.Equal(t, `{"shipped":false,"why":"card declined"}`, s.Outputs)
		},
	},
	{
		name: "bounded_loop",
		doc: `{"steps": [
		  {"name": "draft", "agent": "a", "max_visits": 5, "next": ["nap"]},
		  {"name": "nap", "sleep": "1ms", "next": ["review"]},
		  {"name": "review", "agent": "a", "match": "first", "next": [
		    {"step": "draft", "when": "!review.approved"}, {"step": "publish"}]},
		  {"name": "publish", "action": "x", "with": {"draft": "${{ draft.unit }}"}}],
		 "outputs": {"published": "${{ publish.params.draft }}"}}`,
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusCompleted, s.Status)
			require.Equal(t, `{"published":"main/draft@3"}`, s.Outputs)
			require.Len(t, s.Dispatched, 3+3+1)
		},
	},
	{
		name: "failure_and_finally",
		doc: `{"steps": [
		  {"name": "prepare", "action": "x", "next": ["explode"]},
		  {"name": "explode", "action": "x", "retry": {"max_attempts": 3, "backoff": "1ms", "errors": ["unavailable"]}}],
		 "on_failure": [{"name": "page", "agent": "a", "with": {"why": "${{ error.message }}"}}],
		 "finally": [
		   {"name": "audit", "action": "x", "with": {"status": "${{ run.status }}"}},
		   {"name": "cleanup", "action": "x"}]}`,
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusFailed, s.Status)
			require.Equal(t, "fatal", s.Error.Type)
			require.Equal(t, "main/explode#1", s.Error.StepID)
			require.Len(t, s.FinallyErrors, 1)
			require.Equal(t, "cleanup_failed", s.FinallyErrors[0].Type)
			require.Equal(t, []workflow.StepID{"failure/page#1", "finally/audit#1", "finally/cleanup#1",
				"main/explode#1", "main/prepare#1"}, s.Dispatched)
		},
	},
}

// TestSameDefinitionSameResultOnEveryHost is the conformance suite: every
// definition reaches the same outcome, with the same dispatches, on the
// pure driver, local and postgres.
func TestSameDefinitionSameResultOnEveryHost(t *testing.T) {
	if os.Getenv(dsnEnv) == "" {
		t.Skipf("%s not set", dsnEnv)
	}
	runConformance(t, []conformanceHost{applyHost{}, localHost{}, postgresHost{}},
		conformanceCases, scripted)
}
