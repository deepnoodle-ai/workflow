package postgres

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/hostcore"
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
	// A paused run whose held step jobs are due before every other job.
	_, err = s.Start(ctx,
		NewRun{ID: "p", Definition: load(t, `{"steps": [{"name": "a", "action": "x"}]}`)})
	require.NoError(t, err)
	_, err = s.Pause(ctx, "p", "look")
	require.NoError(t, err)
	_, err = s.pool.Exec(ctx, s.sql(`
		INSERT INTO {{schema}}.jobs (run_id, step_id, kind, step_kind, command, run_at)
		SELECT 'p', 'held-' || g, 'step', 'action', '{}', now() - interval '2 hours'
		FROM generate_series(1, 5) g;`))
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

	// Run claims with no cutoff; RunOnce with the time it started.
	for _, cutoff := range []*time.Time{nil, new(time.Now())} {
		tx, err := s.pool.Begin(ctx)
		require.NoError(t, err)
		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+s.sql(claimSQL),
			8, "w", 30.0, []string{"timer", "step:action"}, cutoff)
		require.NoError(t, err)
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err)
		require.NoError(t, tx.Rollback(ctx))
		plan := strings.Join(lines, "\n")
		require.True(t, strings.Contains(plan, "Index Scan using jobs_claim"), plan)
		require.False(t, strings.Contains(plan, "Seq Scan on jobs"), plan)
		// The claim reads past the paused run's six held jobs (its first
		// step and the five above), and no other row.
		for _, l := range lines {
			_, n, found := strings.Cut(l, "Rows Removed by Filter: ")
			if !found {
				continue
			}
			if removed, err := strconv.Atoi(strings.TrimSpace(n)); err != nil || removed > 6 {
				t.Fatalf("the claim filtered rows it should not have read:\n%s", plan)
			}
		}
	}

	// The held jobs are not claimed.
	claimed, err := s.backend.Claim(ctx, hostcore.ClaimRequest{
		Owner:  "w",
		Queues: []string{"step:action"},
		Limit:  20,
		Lease:  time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 10) // r's action jobs
	for _, c := range claimed {
		require.Equal(t, "r", c.RunID)
	}
}
