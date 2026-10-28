package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// visitOf is the visit a StepID names: 1, or n for @n.
func visitOf(id workflow.StepID) int {
	unit := id.Unit()
	at := strings.LastIndexByte(unit, '@')
	if at < 0 {
		return 1
	}
	n, _ := strconv.Atoi(unit[at+1:])
	return n
}

// TestLongPollLoopKeepsStateBounded runs a poll and sleep loop for 50
// passes, each poll returning about 20 KiB, under limits that hold only
// three such outputs. The run completes because superseded visits leave
// the state. The steps and events tables still keep every pass, and a late
// duplicate completion of a dropped visit changes nothing.
func TestLongPollLoopKeepsStateBounded(t *testing.T) {
	const (
		passes   = 50
		maxState = 64 << 10
	)
	base := newStore(t)
	s := storeOn(t, base, kinds,
		WithLimits(workflow.Limits{MaxOutputBytes: 32 << 10, MaxStateBytes: maxState}))
	def := load(t, `{"steps": [
	  {"name": "poll", "action": "check", "max_visits": 60, "match": "first", "next": [
	    {"step": "done", "when": "poll.done"}, {"step": "nap"}]},
	  {"name": "nap", "sleep": "1ms", "next": ["poll"]},
	  {"name": "done", "action": "finish", "with": {"passes": "${{ poll.pass }}"}}]}`)
	ctx := context.Background()
	blob := strings.Repeat("x", 20<<10)

	var lateApplied, lateErr atomic.Value
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.Step == "done" {
					return job.Params["passes"], nil
				}
				pass := visitOf(job.StepID)
				if pass == 10 {
					// A late duplicate of an early pass, which the run has
					// dropped from its state.
					res, err := s.Deliver(ctx, job.RunID, workflow.Event{
						Kind:   workflow.EventStepCompleted,
						StepID: "main/poll@2#1",
						Output: map[string]any{"done": true, "pass": 2, "blob": blob},
					})
					if err != nil {
						lateErr.Store(err.Error())
					} else {
						lateApplied.Store(res.Applied)
					}
				}
				return map[string]any{"done": pass >= passes, "pass": pass, "blob": blob}, nil
			},
		},
	})

	_, err := s.Start(ctx, NewRun{ID: "poll", Definition: def})
	require.NoError(t, err)
	var peak int
	var r *Run
	for {
		r, err = s.GetRun(ctx, "poll")
		require.NoError(t, err)
		peak = max(peak, r.State.Bytes)
		if r.Status.Terminal() {
			break
		}
		waitFor(t, "the run to advance", func() bool {
			next, err := s.GetRun(ctx, "poll")
			return err == nil && !next.UpdatedAt.Equal(r.UpdatedAt)
		})
	}
	require.Equal(t, workflow.StatusCompleted, r.Status, "%v", r.State.Error)
	require.Equal(t, map[string]any{"done": float64(passes)}, r.State.Outputs)
	require.True(t, peak <= maxState, "state reached %d bytes", peak)
	require.True(t, peak >= 20<<10, "state held a poll output: %d bytes", peak)
	require.Nil(t, lateErr.Load())
	require.Equal(t, false, lateApplied.Load(), "the late duplicate was applied")

	// The history keeps every pass, in order.
	want := []workflow.StepID{}
	for pass := 1; pass <= passes; pass++ {
		suffix := ""
		if pass > 1 {
			suffix = fmt.Sprintf("@%d", pass)
		}
		want = append(want, workflow.StepID("main/poll"+suffix+"#1"))
		if pass < passes {
			want = append(want, workflow.StepID("main/nap"+suffix+"#1"))
		}
	}
	want = append(want, "main/done#1")
	steps, err := s.Steps(ctx, "poll")
	require.NoError(t, err)
	var got []workflow.StepID
	for _, st := range steps {
		require.Equal(t, StepCompleted, st.Status, st.StepID)
		got = append(got, st.StepID)
	}
	require.Equal(t, want, got)
	events, err := s.Events(ctx, "poll")
	require.NoError(t, err)
	require.Equal(t, workflow.EventStart, events[0].Kind)
	got = got[:0]
	for _, ev := range events[1:] {
		got = append(got, ev.StepID)
	}
	require.Equal(t, want, got, "one event per pass, in seq order")
}
