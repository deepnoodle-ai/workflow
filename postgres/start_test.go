package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
)

// TestCallerRollbackLeavesNothing starts a run and delivers a result in the
// caller's transactions, then rolls them back.
func TestCallerRollbackLeavesNothing(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "a", "action": "x", "next": ["b"]}, {"name": "b", "action": "x"}]}`)
	ctx := context.Background()
	errRollback := errors.New("the application's own write failed")

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := s.StartTx(ctx, tx, NewRun{ID: "rolled-back", Definition: def})
		require.NoError(t, err)
		require.True(t, res.Applied)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	_, err = s.GetRun(ctx, "rolled-back")
	require.ErrorIs(t, err, ErrRunNotFound)
	require.Equal(t, 0, countJobs(t, s, "rolled-back"))

	_, err = s.Start(ctx, NewRun{ID: "kept", Definition: def})
	require.NoError(t, err)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := s.DeliverTx(ctx, tx, "kept",
			workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/a#1"})
		require.NoError(t, err)
		require.True(t, res.Applied)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	r, err := s.GetRun(ctx, "kept")
	require.NoError(t, err)
	require.Equal(t, []workflow.StepID{"main/a#1"}, r.State.InFlight())
	events, err := s.Events(ctx, "kept")
	require.NoError(t, err)
	require.Len(t, events, 1)
	steps, err := s.Steps(ctx, "kept")
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.Equal(t, StepDispatched, steps[0].Status)
	var stepJobs []string
	query := s.sql(`SELECT step_id FROM {{schema}}.jobs WHERE run_id = 'kept'`)
	rows, err := s.pool.Query(ctx, query)
	require.NoError(t, err)
	stepJobs, err = pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	require.Equal(t, []string{"main/a#1"}, stepJobs)
}
