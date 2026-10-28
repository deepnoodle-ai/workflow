package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// subscribe LISTENs on channel on a connection of its own and returns
// the notifications it receives.
func subscribe(t *testing.T, s *Store, channel string) <-chan *pgconn.Notification {
	t.Helper()
	conn, err := pgx.ConnectConfig(context.Background(), s.pool.Config().ConnConfig.Copy())
	require.NoError(t, err)
	_, err = conn.Exec(context.Background(), "LISTEN "+pgx.Identifier{channel}.Sanitize())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan *pgconn.Notification, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			n, err := conn.WaitForNotification(ctx)
			if err != nil {
				return
			}
			ch <- n
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = conn.Close(context.Background())
	})
	return ch
}

func requireQuiet(t *testing.T, ch <-chan *pgconn.Notification, what string) {
	t.Helper()
	select {
	case n := <-ch:
		t.Fatalf("%s: unexpected notification %q", what, n.Payload)
	case <-time.After(200 * time.Millisecond):
	}
}

func requireNotified(t *testing.T, ch <-chan *pgconn.Notification, what string) *pgconn.Notification {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no notification", what)
		return nil
	}
}

func countRows(t *testing.T, s *Store, table, runID string) int {
	t.Helper()
	var n int
	require.NoError(t, s.pool.QueryRow(context.Background(),
		s.sql(`SELECT count(*) FROM {{schema}}.`+table+` WHERE run_id = $1`), runID).Scan(&n))
	return n
}

// TestStartAndDeliverCommitWithTheApplication writes an application row
// and starts a run, then delivers an approval, each in one transaction
// with the application's write. A rollback leaves nothing, not even a
// notification to workers; a commit keeps everything and notifies workers
// only once it is done.
func TestStartAndDeliverCommitWithTheApplication(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, s.sql(`CREATE TABLE {{schema}}.approvals (
	    id     text PRIMARY KEY,
	    run_id text NOT NULL,
	    note   text NOT NULL)`))
	require.NoError(t, err)
	def := load(t, `{"steps": [
	  {"name": "approve", "agent": "human", "next": ["ship"]},
	  {"name": "ship", "action": "x", "with": {"by": "${{ approve.by }}"}}]}`)
	jobs := subscribe(t, s, s.NotifyChannel())
	errRollback := errors.New("the application changed its mind")
	appRow := func(tx pgx.Tx, id, note string) {
		_, err := tx.Exec(ctx, s.sql(`INSERT INTO {{schema}}.approvals (id, run_id, note) VALUES ($1, 'po-7', $2)`), id, note)
		require.NoError(t, err)
	}
	approvals := func() []string {
		rows, err := s.pool.Query(ctx, s.sql(`SELECT id FROM {{schema}}.approvals ORDER BY id`))
		require.NoError(t, err)
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err)
		return ids
	}

	// Start, rolled back.
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		appRow(tx, "po-7/requested", "approval requested")
		res, err := s.StartTx(ctx, tx, NewRun{ID: "po-7", Definition: def})
		require.NoError(t, err)
		require.True(t, res.Applied)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	require.Len(t, approvals(), 0)
	_, err = s.GetRun(ctx, "po-7")
	require.ErrorIs(t, err, ErrRunNotFound)
	for _, table := range []string{"jobs", "steps", "events"} {
		require.Equal(t, 0, countRows(t, s, table, "po-7"), table)
	}
	requireQuiet(t, jobs, "start rolled back")

	// Start, committed.
	tx, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	appRow(tx, "po-7/requested", "approval requested")
	res, err := s.StartTx(ctx, tx, NewRun{ID: "po-7", Definition: def})
	require.NoError(t, err)
	require.True(t, res.Applied)
	requireQuiet(t, jobs, "start not yet committed")
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, "po-7", requireNotified(t, jobs, "start committed").Payload)
	require.Equal(t, []string{"po-7/requested"}, approvals())
	require.Equal(t, 1, countRows(t, s, "jobs", "po-7"))
	require.Equal(t, 1, countRows(t, s, "steps", "po-7"))
	require.Equal(t, 1, countRows(t, s, "events", "po-7"))

	// A worker hands the approval step off. It has no handler for ship,
	// so ship's job stays in the outbox where the test can see it.
	startWorker(t, &Worker{Store: s, Handlers: map[string]workflow.Handler{
		"agent": func(ctx context.Context, job workflow.Job) (any, error) { return nil, workflow.ErrDeferred },
	}})
	waitFor(t, "approval handed off", func() bool { return countJobs(t, s, "po-7") == 0 })
	approved := workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/approve#1", Output: map[string]any{"by": "curtis"}}

	// The approval, rolled back.
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		appRow(tx, "po-7/approved", "approved by curtis")
		res, err := s.DeliverTx(ctx, tx, "po-7", approved)
		require.NoError(t, err)
		require.True(t, res.Applied)
		requireQuiet(t, jobs, "approval not yet committed")
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	requireQuiet(t, jobs, "approval rolled back")
	require.Equal(t, []string{"po-7/requested"}, approvals())
	r, err := s.GetRun(ctx, "po-7")
	require.NoError(t, err)
	require.Equal(t, []workflow.StepID{"main/approve#1"}, r.State.InFlight())
	steps, err := s.Steps(ctx, "po-7")
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.Equal(t, StepDispatched, steps[0].Status)
	require.Equal(t, 0, countJobs(t, s, "po-7"))
	require.Equal(t, 1, countRows(t, s, "events", "po-7"))

	// The approval, committed.
	tx, err = s.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	appRow(tx, "po-7/approved", "approved by curtis")
	res, err = s.DeliverTx(ctx, tx, "po-7", approved)
	require.NoError(t, err)
	require.True(t, res.Applied)
	requireQuiet(t, jobs, "approval not yet committed")
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, "po-7", requireNotified(t, jobs, "approval committed").Payload)
	require.Equal(t, []string{"po-7/approved", "po-7/requested"}, approvals())
	steps, err = s.Steps(ctx, "po-7")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	require.Equal(t, StepCompleted, steps[0].Status)
	require.Equal(t, workflow.StepID("main/ship#1"), steps[1].StepID)
	require.Equal(t, map[string]any{"by": "curtis"}, steps[1].Params)
	rows, err := s.pool.Query(ctx, s.sql(`SELECT step_id FROM {{schema}}.jobs WHERE run_id = 'po-7'`))
	require.NoError(t, err)
	left, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	require.Equal(t, []string{"main/ship#1"}, left)
	require.Equal(t, 2, countRows(t, s, "events", "po-7"))
}

// TestFailingOnFinishedRollsTheFinishBack: the WithOnFinished hook fails
// the first time. The event that finishes the run (the last finally step's
// result) rolls back with it; the worker retries its settle and the hook
// succeeds once, after every finally step has settled. RunsChannel reports
// the run's end once.
func TestFailingOnFinishedRollsTheFinishBack(t *testing.T) {
	base := newStore(t)
	var (
		mu       sync.Mutex
		order    []string
		finished []Finished
	)
	s, err := New(base.pool, kinds, WithSchema(base.schema), WithOnFinished(func(ctx context.Context, tx pgx.Tx, f Finished) error {
		var open int
		if err := tx.QueryRow(ctx, base.sql(`SELECT count(*) FROM {{schema}}.steps WHERE run_id = $1 AND status = 'dispatched'`),
			f.RunID).Scan(&open); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		finished = append(finished, f)
		order = append(order, fmt.Sprintf("finished#%d open=%d", len(finished), open))
		if len(finished) == 1 {
			return errors.New("audit log insert failed")
		}
		return nil
	}))
	require.NoError(t, err)
	ctx := context.Background()
	runs := subscribe(t, s, s.RunsChannel())

	def := load(t, `{
	  "steps": [{"name": "work", "action": "x"}],
	  "outputs": {"r": "${{ work }}"},
	  "finally": [{"name": "release", "action": "x"}, {"name": "notify", "action": "x"}]}`)
	startWorker(t, &Worker{Store: s, Lease: time.Second, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			mu.Lock()
			order = append(order, job.Step)
			mu.Unlock()
			if job.Step == "release" {
				return nil, &workflow.StepError{Type: "locked", Message: "still held"}
			}
			return "ok", nil
		},
	}})
	_, err = s.Start(ctx, NewRun{ID: "hook-retry", Definition: def})
	require.NoError(t, err)
	r := waitStatus(t, s, "hook-retry", workflow.StatusCompleted, workflow.StatusFailed, workflow.StatusCancelled)
	require.Equal(t, workflow.StatusCompleted, r.Status)

	var statuses []string
	for len(statuses) < 3 {
		n := requireNotified(t, runs, "run status")
		var p struct {
			RunID  string `json:"run_id"`
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal([]byte(n.Payload), &p))
		require.Equal(t, "hook-retry", p.RunID)
		statuses = append(statuses, p.Status)
	}
	requireQuiet(t, runs, "after the run finished")
	require.Equal(t, []string{"running", "completing", "completed"}, statuses)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"work", "release", "notify", "finished#1 open=0", "finished#2 open=0"}, order)
	require.Len(t, finished, 2)
	f := finished[1]
	require.Equal(t, workflow.StatusCompleted, f.Status)
	require.Equal(t, map[string]any{"r": "ok"}, f.Outputs)
	require.Len(t, f.FinallyErrors, 1)
	require.Equal(t, "release", f.FinallyErrors[0].Step)
	require.Equal(t, "locked", f.FinallyErrors[0].Type)
	events, err := s.Events(ctx, "hook-retry")
	require.NoError(t, err)
	require.Len(t, events, 4, "start, work, release, notify: the rolled-back finish is not recorded")
}
