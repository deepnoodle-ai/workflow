package postgres

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestAPanickingSQLHookRollsBack: on a store whose only hook is
// WithOnFinishedSQL, a hook that panics rolls its database/sql transaction
// back, as pgx.BeginFunc does for a pgx.Tx: the run is not finished and the
// connection goes back to the pool.
func TestAPanickingSQLHookRollsBack(t *testing.T) {
	base := newStore(t)
	s, err := New(base.pool, kinds, WithSchema(base.schema),
		WithOnFinishedSQL(func(context.Context, SQLTx, Finished) error { panic("hook bug") }))
	require.NoError(t, err)
	ctx := context.Background()
	_, err = s.Start(ctx, NewRun{ID: "p", Definition: load(t, `{"steps": [{"name": "a", "action": "x"}]}`)})
	require.NoError(t, err)
	func() {
		defer func() { require.Equal(t, "hook bug", recover()) }()
		_, _ = s.Cancel(ctx, "p", "stop")
	}()
	require.Equal(t, 0, s.sqlDB.Stats().InUse, "the transaction's connection is back")
	r, err := s.GetRun(ctx, "p")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusRunning, r.Status, "the cancel rolled back")
}
