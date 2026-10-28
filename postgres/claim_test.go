package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
)

// TestClaimUsesItsIndex checks the claim reads each queue through
// jobs_claim, with a backlog of another kind and many future timers.
func TestClaimUsesItsIndex(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.Start(ctx,
		NewRun{
			ID:         "r",
			Definition: load(t, `{"steps": [{"name": "a", "agent": "x"}]}`),
		})
	require.NoError(t, err)
	_, err = s.pool.Exec(ctx, s.sql(`
		INSERT INTO {{schema}}.jobs (run_id, step_id, kind, step_kind, command, run_at)
		SELECT 'r', 'agent-' || g, 'step', 'agent', '{}', now() - interval '1 hour' FROM generate_series(1, 20000) g;
		INSERT INTO {{schema}}.jobs (run_id, step_id, kind, step_kind, command, run_at)
		SELECT 'r', 'timer-' || g, 'timer', 'agent', '{}', now() + interval '1 day' FROM generate_series(1, 20000) g;
		INSERT INTO {{schema}}.jobs (run_id, step_id, kind, step_kind, command, run_at)
		SELECT 'r', 'action-' || g, 'step', 'action', '{}', now() FROM generate_series(1, 10) g;
		ANALYZE {{schema}}.jobs`))
	require.NoError(t, err)

	tx, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+s.sql(claimSQL),
		8, "w", 30.0, []string{"timer", "step:action"})
	require.NoError(t, err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	plan := strings.Join(lines, "\n")
	require.True(t, strings.Contains(plan, "Index Scan using jobs_claim"), plan)
	require.False(t, strings.Contains(plan, "Seq Scan on jobs"), plan)
	for _, l := range lines {
		if strings.Contains(l, "Rows Removed by Filter") {
			t.Fatalf("the claim filtered rows it should not have read:\n%s", plan)
		}
	}
}
