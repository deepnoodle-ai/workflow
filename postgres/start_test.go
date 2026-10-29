package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
)

// TestLimitsDoNotDependOnTheCache gives the same output to a run whose
// definition is cached (the process that started it) and to one whose
// definition is loaded from the database (another process). Both apply the
// store's limits.
func TestLimitsDoNotDependOnTheCache(t *testing.T) {
	base := newStore(t)
	limits := WithLimits(workflow.Limits{MaxOutputBytes: 10})
	warm, err := New(base.pool, kinds, WithSchema(base.schema), limits)
	require.NoError(t, err)
	def := load(t, `{"steps": [{"name": "a", "agent": "x"}]}`)
	ctx := context.Background()
	big := strings.Repeat("x", 100)
	for _, id := range []string{"warm", "cold"} {
		_, err := warm.Start(ctx, NewRun{ID: id, Definition: def})
		require.NoError(t, err)
	}
	require.Equal(t, workflow.Limits{}, def.Limits(), "the caller's definition is not modified")

	cold, err := New(base.pool, kinds, WithSchema(base.schema), limits)
	require.NoError(t, err)
	for id, s := range map[string]*Store{"warm": warm, "cold": cold} {
		res, err := s.Deliver(ctx, id, workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/a#1",
			Output: big,
		})
		require.NoError(t, err)
		require.Equal(t, workflow.StatusFailed, res.State.Status, id)
		require.Equal(t, workflow.ErrorTypeOutputTooLarge, res.State.Error.Type, id)
	}
}

func TestStartRejectsUnloadedDefinitions(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "nil"})
	require.ErrorIs(t, err, workflow.ErrNotValidated)
	_, err = s.Start(ctx, NewRun{ID: "raw", Definition: &workflow.Definition{Name: "raw"}})
	require.ErrorIs(t, err, workflow.ErrNotValidated)
	var n int
	require.NoError(t, s.pool.QueryRow(ctx, s.sql(`SELECT count(*) FROM {{schema}}.definitions`)).
		Scan(&n))
	require.Equal(t, 0, n)
	require.Equal(t, 0, s.core.Cached())
}

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
