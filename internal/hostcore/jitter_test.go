package hostcore

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

func maxRand(n int64) int64 { return n - 1 }

func TestRetryJitterPolicy(t *testing.T) {
	zero := func(int64) int64 { return 0 }
	require.Equal(t, 2*time.Second, retryJitter(10*time.Second, maxRand))
	require.Equal(t, time.Duration(0), retryJitter(10*time.Second, zero))
	require.Equal(t, time.Minute, retryJitter(time.Hour, maxRand))
	require.Equal(t, time.Duration(0), retryJitter(0, maxRand))

	// 100 runs with a 10s backoff spread across [10s, 12s].
	seen := map[time.Duration]bool{}
	for range 100 {
		d := 10*time.Second + retryJitter(10*time.Second, nil)
		require.True(t, d >= 10*time.Second && d <= 12*time.Second, d)
		seen[d] = true
	}
	require.True(t, len(seen) > 1, "every retry landed at the same instant")
}

// jobTx records the jobs persist inserts; persist calls no other method
// that this test reaches through the nil Tx.
type jobTx struct {
	Tx
	jobs map[string]time.Time
}

func (tx *jobTx) InsertEvent(context.Context, string, EventRow) error { return nil }
func (tx *jobTx) InsertStep(context.Context, string, StepRow) error   { return nil }
func (tx *jobTx) JobsWritten(context.Context, string) error           { return nil }

func (tx *jobTx) StatusChanged(context.Context, string, workflow.RunStatus) error {
	return nil
}

func (tx *jobTx) InsertJob(_ context.Context, _ string, j JobRow) error {
	tx.jobs[j.Kind+" "+string(j.StepID)] = j.RunAt
	return nil
}

func TestPersistJittersOnlyRetries(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cmds := []workflow.Command{
		{Kind: workflow.CommandDispatchStep, StepID: "first#1", Attempt: 1, NotBefore: at},
		{Kind: workflow.CommandDispatchStep, StepID: "now#2", Attempt: 2, NotBefore: at},
		{
			Kind:      workflow.CommandDispatchStep,
			StepID:    "flaky#2",
			Attempt:   2,
			NotBefore: at.Add(10 * time.Second),
		},
		{
			Kind:      workflow.CommandScheduleTimer,
			StepID:    "flaky#2",
			Attempt:   2,
			NotBefore: at.Add(40 * time.Second),
		},
		{
			Kind:      workflow.CommandScheduleTimer,
			StepID:    "nap#1",
			StepKind:  workflow.KindSleep,
			Attempt:   1,
			NotBefore: at.Add(5 * time.Second),
		},
	}
	s := &Store{Prefix: "test", randN: maxRand}
	tx := &jobTx{jobs: map[string]time.Time{}}
	next := &workflow.State{Status: workflow.StatusRunning}
	ev := workflow.Event{At: at, Kind: workflow.EventStart}
	require.NoError(t, s.persist(context.Background(), tx, nil, "r", nil, next, cmds, ev))

	require.Equal(t, map[string]time.Time{
		"step first#1":  {},                       // due now: no jitter
		"step now#2":    {},                       // retry with no backoff
		"step flaky#2":  at.Add(12 * time.Second), // 10s backoff + 20%
		"timer flaky#2": at.Add(42 * time.Second), // its timeout moves too
		"timer nap#1":   at.Add(5 * time.Second),  // sleeps are exact
	}, tx.jobs)
}
