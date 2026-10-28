package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
)

func timerJobs(t *testing.T, s *Store, runID string) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(),
		s.sql(`SELECT step_id FROM {{schema}}.jobs WHERE run_id = $1 AND kind = 'timer' ORDER BY step_id`), runID)
	require.NoError(t, err)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return ids
}

// TestTimeoutRacesCompletionOnEachItems runs an each over four deferred
// items with a timeout and a retry, and delivers every result itself:
// item 0's timeout and completion at once, from separate transactions;
// item 1 times out on its first attempt and completes on its second; item
// 2 fails permanently, which cancels item 3. Whichever of item 0's events
// wins, exactly one is applied and the run ends the same way.
func TestTimeoutRacesCompletionOnEachItems(t *testing.T) {
	const races = 8
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "work", "agent": "x", "each": "[0, 1, 2, 3]", "with": {"i": "${{ item }}"},
	  "timeout": "1h", "retry": {"max_attempts": 2, "backoff": "1ms", "errors": ["timeout"]}}]}`)
	startWorker(t, &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"agent": func(context.Context, workflow.Job) (any, error) { return nil, workflow.ErrDeferred },
	}})
	ctx := context.Background()
	deliver := func(runID string, ev workflow.Event) *Result {
		t.Helper()
		res, err := s.Deliver(ctx, runID, ev)
		require.NoError(t, err)
		return res
	}
	completed := func(id workflow.StepID, out any) workflow.Event {
		return workflow.Event{Kind: workflow.EventStepCompleted, StepID: id, Output: out}
	}
	fired := func(id workflow.StepID) workflow.Event {
		return workflow.Event{Kind: workflow.EventTimerFired, StepID: id}
	}

	var finals []string
	timerWon := 0
	for i := 0; i < races; i++ {
		runID := fmt.Sprintf("race-%d", i)
		_, err := s.Start(ctx, NewRun{ID: runID, Definition: def})
		require.NoError(t, err)

		// Item 0: its timeout and its completion at the same moment.
		gate := make(chan struct{})
		var wg sync.WaitGroup
		applied := make([]bool, 2)
		for j, ev := range []workflow.Event{fired("main/work[0]#1"), completed("main/work[0]#1", "r0")} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				res, err := s.Deliver(ctx, runID, ev)
				if err != nil {
					t.Errorf("deliver %s: %v", ev.Kind, err)
					return
				}
				applied[j] = res.Applied
			}()
		}
		close(gate)
		wg.Wait()
		require.True(t, applied[0] != applied[1], "exactly one of the racing events applies: %v", applied)
		if applied[0] {
			// The timeout won; the retry completes item 0.
			timerWon++
			require.True(t, deliver(runID, completed("main/work[0]#2", "r0")).Applied)
		}

		// Item 1: times out on #1, completes on #2.
		require.True(t, deliver(runID, fired("main/work[1]#1")).Applied)
		require.True(t, deliver(runID, completed("main/work[1]#2", "r1")).Applied)
		require.Equal(t, []string{"main/work[2]#1", "main/work[3]#1"}, timerJobs(t, s, runID),
			"settled attempts leave no timer behind")

		// Item 2 fails permanently, cancelling item 3.
		res := deliver(runID, workflow.Event{Kind: workflow.EventStepFailed, StepID: "main/work[2]#1",
			Error: &workflow.StepError{Type: "fatal", Message: "item 2 is broken"}})
		require.True(t, res.Applied)
		require.Equal(t, workflow.StatusFailed, res.State.Status)
		require.Equal(t, 0, countJobs(t, s, runID))

		steps, err := s.Steps(ctx, runID)
		require.NoError(t, err)
		got := map[workflow.StepID]string{}
		for _, st := range steps {
			got[st.StepID] = string(st.Status)
			if st.Error != nil {
				got[st.StepID] += " " + st.Error.Type
			}
		}
		want := map[workflow.StepID]string{
			"main/work[0]#1": "completed",
			"main/work[1]#1": "failed timeout",
			"main/work[1]#2": "completed",
			"main/work[2]#1": "failed fatal",
			"main/work[3]#1": "cancelled",
		}
		if applied[0] {
			want["main/work[0]#1"] = "failed timeout"
			want["main/work[0]#2"] = "completed"
		}
		require.Equal(t, want, got, runID)

		// The final state, without the attempt numbers the race decides.
		r, err := s.GetRun(ctx, runID)
		require.NoError(t, err)
		units := []string{}
		for item := 0; item < 4; item++ {
			u := r.State.Unit(workflow.StepID(fmt.Sprintf("main/work[%d]#2", item)))
			if u == nil {
				u = r.State.Unit(workflow.StepID(fmt.Sprintf("main/work[%d]#1", item)))
			}
			require.NotNil(t, u, "item %d", item)
			out, _ := u.Output.(string)
			units = append(units, string(u.Status)+" "+out)
		}
		final, err := json.Marshal(map[string]any{
			"status": r.Status, "error": summarizeErr(r.State.Error), "outputs": r.State.Outputs,
			"in_flight": r.State.InFlight(), "units": units,
		})
		require.NoError(t, err)
		finals = append(finals, string(final))
	}
	t.Logf("the timeout won %d of %d races", timerWon, races)
	for _, f := range finals[1:] {
		require.Equal(t, finals[0], f)
	}
	require.Contains(t, finals[0], `"units":["completed r0","completed r1","failed ","cancelled "]`)
}
