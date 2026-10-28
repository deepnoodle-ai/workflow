package postgres

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
)

// TestDeliverReportsApplied lets an HTTP handler tell a new approval from a
// retried one.
func TestDeliverReportsApplied(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "approve", "agent": "human"}]}`)
	ctx := context.Background()
	res, err := s.Start(ctx, NewRun{ID: "applied", Definition: def})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Len(t, res.Commands, 1)
	res, err = s.Start(ctx, NewRun{ID: "applied", Definition: def})
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Len(t, res.Commands, 0)

	ev := workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/approve#1", Output: "yes"}
	res, err = s.Deliver(ctx, "applied", ev)
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, workflow.StatusCompleted, res.State.Status)
	res, err = s.Deliver(ctx, "applied", ev)
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Equal(t, workflow.StatusCompleted, res.State.Status)

	res, err = s.Cancel(ctx, "applied", "too late")
	require.NoError(t, err)
	require.False(t, res.Applied)
}

// TestRunFinishedHookAndNotification reports each terminal transition once,
// in the transaction that makes it, and on RunsChannel after commit.
func TestRunFinishedHookAndNotification(t *testing.T) {
	base := newStore(t)
	var (
		mu       sync.Mutex
		finished []Finished
	)
	s, err := New(base.pool, kinds, WithSchema(base.schema), WithOnFinished(func(ctx context.Context, tx pgx.Tx, f Finished) error {
		// The hook runs inside the event's transaction.
		var status string
		if err := tx.QueryRow(ctx, base.sql(`SELECT status FROM {{schema}}.runs WHERE id = $1`), f.RunID).Scan(&status); err != nil {
			return err
		}
		if status != string(f.Status) {
			t.Errorf("hook sees status %s, want %s", status, f.Status)
		}
		mu.Lock()
		finished = append(finished, f)
		mu.Unlock()
		return nil
	}))
	require.NoError(t, err)

	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	require.NoError(t, err)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `LISTEN "`+s.RunsChannel()+`"`)
	require.NoError(t, err)

	def := load(t, `{"steps": [{"name": "a", "action": "x"}], "outputs": {"r": "${{ a }}"}}`)
	startWorker(t, &Worker{Store: s, Handlers: map[string]Handler{
		"action": func(ctx context.Context, job Job) (any, error) { return "done", nil },
	}})
	_, err = s.Start(ctx, NewRun{ID: "ok", Definition: def})
	require.NoError(t, err)
	_, err = s.Start(ctx, NewRun{ID: "stopped", Definition: load(t, `{"steps": [{"name": "a", "agent": "x"}]}`)})
	require.NoError(t, err)
	_, err = s.Cancel(ctx, "stopped", "no longer needed")
	require.NoError(t, err)
	waitStatus(t, s, "ok", workflow.StatusCompleted)

	got := map[string]string{}
	for len(got) < 4 {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		n, err := conn.WaitForNotification(wctx)
		cancel()
		require.NoError(t, err)
		var p struct {
			RunID  string `json:"run_id"`
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal([]byte(n.Payload), &p))
		got[p.RunID+":"+p.Status] = p.Status
	}
	require.Equal(t, map[string]string{"ok:running": "running", "ok:completed": "completed",
		"stopped:running": "running", "stopped:cancelled": "cancelled"}, got)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, finished, 2)
	byRun := map[string]Finished{}
	for _, f := range finished {
		byRun[f.RunID] = f
	}
	require.Equal(t, map[string]any{"r": "done"}, byRun["ok"].Outputs)
	require.Equal(t, workflow.StatusCancelled, byRun["stopped"].Status)
	require.Equal(t, "no longer needed", byRun["stopped"].Reason)
}

// TestFinallyOnPostgres runs a cancelled run's finally steps through the
// outbox and workers: the run is cancelling until they are done, and the
// hook sees the run finish once, with the cleanup that failed.
func TestFinallyOnPostgres(t *testing.T) {
	base := newStore(t)
	var (
		mu       sync.Mutex
		finished []Finished
	)
	s, err := New(base.pool, kinds, WithSchema(base.schema), WithOnFinished(func(ctx context.Context, tx pgx.Tx, f Finished) error {
		mu.Lock()
		finished = append(finished, f)
		mu.Unlock()
		return nil
	}))
	require.NoError(t, err)
	ctx := context.Background()
	def := load(t, `{
	  "steps": [{"name": "wait", "agent": "x"}],
	  "finally": [
	    {"name": "release", "action": "x", "with": {"status": "${{ run.status }}"}},
	    {"name": "notify", "action": "x", "with": {"released": "${{ release?.error == nil }}"}}]}`)
	_, err = s.Start(ctx, NewRun{ID: "fin", Definition: def})
	require.NoError(t, err)
	res, err := s.Cancel(ctx, "fin", "superseded")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelling, res.State.Status)
	run, err := s.GetRun(ctx, "fin")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelling, run.Status)

	var params []map[string]any
	startWorker(t, &Worker{Store: s, Handlers: map[string]Handler{
		"action": func(ctx context.Context, job Job) (any, error) {
			mu.Lock()
			params = append(params, job.Command.Params)
			mu.Unlock()
			if job.Command.Step == "release" {
				return nil, &workflow.StepError{Type: "locked", Message: "still held"}
			}
			return "ok", nil
		},
	}})
	run = waitStatus(t, s, "fin", workflow.StatusCancelled)
	require.Len(t, run.State.FinallyErrors, 1)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []map[string]any{{"status": "cancelled"}, {"released": false}}, params)
	require.Len(t, finished, 1)
	require.Equal(t, workflow.StatusCancelled, finished[0].Status)
	require.Equal(t, "superseded", finished[0].Reason)
	require.Len(t, finished[0].FinallyErrors, 1)
	require.Equal(t, "release", finished[0].FinallyErrors[0].Step)
	require.Equal(t, "locked", finished[0].FinallyErrors[0].Type)
}
