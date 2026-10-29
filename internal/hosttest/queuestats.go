package hosttest

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestQueueStats counts due jobs across two step kinds and timers: a job
// under a live lease and a future timer are not due, a job whose lease
// expired is.
func testQueueStats(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	stats, err := s.QueueStats(ctx)
	require.NoError(t, err)
	require.NotNil(t, stats)
	require.Len(t, stats, 0)

	start := func(id, doc string) {
		_, err := s.Start(ctx, NewRun{ID: id, Definition: load(t, doc)})
		require.NoError(t, err)
	}
	start("claimed", `{"steps": [{"name": "a", "action": "x"}]}`)
	start("expired", `{"steps": [{"name": "a", "action": "x"}]}`)
	w := &Worker{Store: s, Handlers: map[string]workflow.Handler{"action": nil}}
	w.defaults()
	c, err := w.claim(ctx, 2)
	require.NoError(t, err)
	require.Len(t, c, 2)
	expireLeases(t, s, "expired")

	start("old", `{"steps": [{"name": "a", "agent": "x"}]}`)
	start("new", `{"steps": [{"name": "a", "agent": "x"}]}`)
	start("asleep", `{"steps": [{"name": "nap", "sleep": "1h"}]}`)
	s.backdate(t, "old", 10*time.Second)

	stats, err = s.QueueStats(ctx)
	require.NoError(t, err)
	require.Len(t, stats, 3)
	require.Equal(t, workflow.CommandDispatchStep, stats[0].Kind)
	require.Equal(t, "action", stats[0].StepKind)
	require.Equal(t, 1, stats[0].Due)
	require.True(t, stats[0].OldestDue > 0, stats[0].OldestDue)

	require.Equal(t, workflow.CommandDispatchStep, stats[1].Kind)
	require.Equal(t, "agent", stats[1].StepKind)
	require.Equal(t, 2, stats[1].Due)
	require.True(t, stats[1].OldestDue >= 10*time.Second, stats[1].OldestDue)
	require.True(t, stats[1].OldestDue < time.Minute, stats[1].OldestDue)

	require.Equal(t, QueueStat{Kind: workflow.CommandScheduleTimer}, stats[2])
}
