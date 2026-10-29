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

// TestRunFinishedHookAndNotification reports each terminal transition once,
// in the transaction that makes it, and on RunsChannel after commit.
func TestRunFinishedHookAndNotification(t *testing.T) {
	base := newStore(t)
	var (
		mu       sync.Mutex
		finished []Finished
	)
	s, err := New(base.pool, kinds, WithSchema(base.schema),
		WithOnFinished(func(ctx context.Context, tx pgx.Tx, f Finished) error {
			// The hook runs inside the event's transaction.
			var status string
			if err := tx.QueryRow(ctx, base.sql(`SELECT status FROM {{schema}}.runs WHERE id = $1`),
				f.RunID).Scan(&status); err != nil {
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
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				return "done", nil
			},
		},
	})
	_, err = s.Start(ctx, NewRun{ID: "ok", Definition: def})
	require.NoError(t, err)
	_, err = s.Start(ctx,
		NewRun{
			ID:         "stopped",
			Definition: load(t, `{"steps": [{"name": "a", "agent": "x"}]}`),
		})
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

// A cancel delivered again, as a retrying caller may, does not stop the
// finally steps the first one started. CancelTx cancels in the caller's
// transaction.
func TestCancelTwiceKeepsCleanup(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "work", "agent": "x"}],
	  "finally": [{"name": "release", "agent": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "cancel-twice", Definition: def})
	require.NoError(t, err)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := s.CancelTx(ctx, tx, "cancel-twice", "operator")
		if err == nil && !res.Applied {
			t.Error("the first cancel applies")
		}
		return err
	})
	require.NoError(t, err)
	res, err := s.Cancel(ctx, "cancel-twice", "operator")
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Equal(t, workflow.StatusCancelling, res.State.Status)
	require.Equal(t, []workflow.StepID{"finally/release#1"}, res.State.InFlight())

	res, err = s.Deliver(ctx, "cancel-twice", workflow.Event{
		Kind:   workflow.EventCancel,
		Reason: "now",
		Force:  true,
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, workflow.StatusCancelled, res.State.Status)
	require.Len(t, res.State.FinallyErrors, 1)
}
