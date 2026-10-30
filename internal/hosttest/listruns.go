package hosttest

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// ListRuns reads one page of runs, as a backend's Store.ListRuns does.
func (e *Env) ListRuns(ctx context.Context, f durable.RunFilter) (durable.RunPage, error) {
	return e.Store.ListRuns(ctx, e.Backend, f)
}

// DeleteEndedRuns deletes ended runs, as a backend's Store.DeleteEndedRuns
// does.
func (e *Env) DeleteEndedRuns(ctx context.Context, before time.Time) (int, error) {
	return e.Store.DeleteEndedRuns(ctx, e.Backend, before)
}

// doneDoc completes at Start: its only step is a set.
const doneDoc = `{"steps": [{"name": "a", "set": 1}]}`

// failingDoc is in failing while its on_failure step runs.
const failingDoc = `{"steps": [{"name": "a", "action": "x"}],
 "on_failure": [{"name": "page", "action": "x"}]}`

// startRun starts a run and fails t on an error.
func startRun(t *testing.T, s *Env, id string, def *workflow.Definition, meta map[string]any) {
	t.Helper()
	res, err := s.Start(context.Background(), NewRun{ID: id, Definition: def, Metadata: meta})
	require.NoError(t, err)
	require.True(t, res.Applied, id)
}

// failStep fails the first attempt of step a.
func failStep(t *testing.T, s *Env, id string) {
	t.Helper()
	_, err := s.Deliver(context.Background(), id, workflow.Event{
		Kind:   workflow.EventStepFailed,
		StepID: "main/a#1",
		Error:  &workflow.StepError{Type: "boom", Message: "it broke"},
	})
	require.NoError(t, err)
}

// listIDs lists the IDs of one page.
func listIDs(t *testing.T, s *Env, f durable.RunFilter) []string {
	t.Helper()
	page, err := s.ListRuns(context.Background(), f)
	require.NoError(t, err)
	ids := []string{}
	for _, r := range page.Runs {
		ids = append(ids, r.ID)
	}
	return ids
}

// justAfter is a cutoff just after a run's last write, from the time the
// store recorded, so that the test does not depend on the test process's
// clock matching the one the store used.
func justAfter(t *testing.T, s *Env, runID string) time.Time {
	t.Helper()
	r, err := s.GetRun(context.Background(), runID)
	require.NoError(t, err)
	return r.UpdatedAt.Add(time.Microsecond)
}

// tick makes the next run's times strictly later than the last one's, at
// the microsecond both backends store.
func tick() { time.Sleep(2 * time.Millisecond) }

// TestListRunsFiltersByStatusAndTime: a status set, the time bounds, the
// newest-first order and the summary's fields (PRD 011, UC-1).
func testListRunsFiltersByStatusAndTime(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	step, done := load(t, oneStep), load(t, doneDoc)

	startRun(t, s, "r1", step, nil)
	tick()
	startRun(t, s, "c1", done, map[string]any{"team": "a"})
	tick()
	startRun(t, s, "f1", step, nil)
	failStep(t, s, "f1")
	tick()
	startRun(t, s, "r2", step, nil)
	tick()
	_, err := s.Deliver(ctx, "r1",
		workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/a#1", Output: "ok"})
	require.NoError(t, err)

	read := func(id string) *durable.Run {
		r, err := s.GetRun(ctx, id)
		require.NoError(t, err)
		return r
	}
	c1, f1, r2 := read("c1"), read("f1"), read("r2")

	require.Equal(t, []string{"r2", "f1", "c1", "r1"}, listIDs(t, s, durable.RunFilter{}))

	page, err := s.ListRuns(ctx, durable.RunFilter{Status: []workflow.RunStatus{workflow.StatusFailed}})
	require.NoError(t, err)
	require.Len(t, page.Runs, 1)
	got := page.Runs[0]
	require.Equal(t, "f1", got.ID)
	require.Equal(t, workflow.StatusFailed, got.Status)
	require.NotNil(t, got.Error)
	require.Equal(t, f1.State.Error.Message, got.Error.Message)
	require.Equal(t, f1.State.Error.Type, got.Error.Type)
	require.Equal(t, f1.DefinitionHash, got.DefinitionHash)
	require.Equal(t, f1.CreatedAt, got.CreatedAt)
	require.Equal(t, f1.UpdatedAt, got.UpdatedAt)
	require.NotNil(t, got.FinishedAt)
	require.Equal(t, "", page.Next)

	page, err = s.ListRuns(ctx, durable.RunFilter{Status: []workflow.RunStatus{workflow.StatusCompleted}})
	require.NoError(t, err)
	require.Equal(t, 2, len(page.Runs))
	require.Equal(t, "c1", page.Runs[0].ID)
	require.Equal(t, map[string]any{"team": "a"}, page.Runs[0].Metadata)
	require.Nil(t, page.Runs[0].Error)

	both := []workflow.RunStatus{workflow.StatusFailed, workflow.StatusRunning}
	require.Equal(t, []string{"r2", "f1"}, listIDs(t, s, durable.RunFilter{Status: both}))
	// A status no run has, such as one a later release adds, matches none.
	require.Equal(t, []string{},
		listIDs(t, s, durable.RunFilter{Status: []workflow.RunStatus{"paused"}}))

	require.Equal(t, []string{"r2", "f1"},
		listIDs(t, s, durable.RunFilter{CreatedAfter: c1.CreatedAt}))
	require.Equal(t, []string{"c1", "r1"},
		listIDs(t, s, durable.RunFilter{CreatedBefore: f1.CreatedAt}))
	require.Equal(t, []string{"f1"},
		listIDs(t, s, durable.RunFilter{CreatedAfter: c1.CreatedAt, CreatedBefore: r2.CreatedAt}))
	// r1 changed last but started first: UpdatedAfter selects, it does not
	// sort.
	require.Equal(t, []string{"r1"}, listIDs(t, s, durable.RunFilter{UpdatedAfter: r2.UpdatedAt}))
	require.Equal(t, []string{"r2", "r1"},
		listIDs(t, s, durable.RunFilter{UpdatedAfter: f1.UpdatedAt}))
}

// TestListRunsFiltersByMetadata: string values match by key at the top
// level; a number or a bool with the same text does not; other filter
// values are an error that names the key (PRD 011, UC-2).
func testListRunsFiltersByMetadata(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	def := load(t, doneDoc)

	for _, r := range []struct {
		id   string
		meta map[string]any
	}{
		{"str", map[string]any{"customer_id": "42"}},
		{"num", map[string]any{"customer_id": 42}},
		{"one", map[string]any{"customer_id": "1", "vip": "true"}},
		{"bool", map[string]any{"customer_id": true, "vip": true}},
		{"nested", map[string]any{"x": map[string]any{"customer_id": "42"}}},
		{"none", nil},
		{"gold", map[string]any{"customer_id": "42", "tier": "gold"}},
		{"odd", map[string]any{`a."b`: "x"}},
	} {
		startRun(t, s, r.id, def, r.meta)
		tick()
	}

	require.Equal(t, []string{"gold", "str"},
		listIDs(t, s, durable.RunFilter{Metadata: map[string]any{"customer_id": "42"}}))
	require.Equal(t, []string{"gold"}, listIDs(t, s,
		durable.RunFilter{Metadata: map[string]any{"customer_id": "42", "tier": "gold"}}))
	require.Equal(t, []string{"one"},
		listIDs(t, s, durable.RunFilter{Metadata: map[string]any{"vip": "true"}}))
	require.Equal(t, []string{},
		listIDs(t, s, durable.RunFilter{Metadata: map[string]any{"customer_id": "1.0"}}))
	require.Equal(t, []string{"odd"},
		listIDs(t, s, durable.RunFilter{Metadata: map[string]any{`a."b`: "x"}}))

	for _, v := range []any{42, true, nil, map[string]any{"a": "b"}, []any{"42"}} {
		_, err := s.ListRuns(ctx, durable.RunFilter{Metadata: map[string]any{"customer_id": v}})
		require.Error(t, err, fmt.Sprint(v))
		require.Contains(t, err.Error(), `"customer_id"`)
	}
}

// TestListRunsPagesWithoutRepeats: pages read with the cursor hold each
// run once while runs start and change status between reads (PRD 011,
// UC-3).
func testListRunsPagesWithoutRepeats(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	def := load(t, oneStep)

	const n = 300
	for i := range n {
		startRun(t, s, fmt.Sprintf("p%03d", i), def, nil)
	}

	seen := map[string]int{}
	filter := durable.RunFilter{Limit: 7}
	pages := 0
	for {
		page, err := s.ListRuns(ctx, filter)
		require.NoError(t, err)
		require.True(t, len(page.Runs) <= 7)
		for _, r := range page.Runs {
			seen[r.ID]++
		}
		pages++
		if page.Next == "" {
			break
		}
		// A new run starts and an old one ends before the next read.
		startRun(t, s, fmt.Sprintf("new%03d", pages), def, nil)
		_, err = s.Cancel(ctx, fmt.Sprintf("p%03d", pages), "test")
		require.NoError(t, err)
		filter.Cursor = page.Next
	}
	require.Equal(t, (n+6)/7, pages)
	require.Equal(t, n, len(seen))
	for i := range n {
		require.Equal(t, 1, seen[fmt.Sprintf("p%03d", i)], i)
	}

	page, err := s.ListRuns(ctx, durable.RunFilter{})
	require.NoError(t, err)
	require.Equal(t, 100, len(page.Runs))
	require.NotEqual(t, "", page.Next)
	page, err = s.ListRuns(ctx, durable.RunFilter{Limit: 5000})
	require.NoError(t, err)
	require.Equal(t, n+pages-1, len(page.Runs))
	require.Equal(t, "", page.Next)

	_, err = s.ListRuns(ctx, durable.RunFilter{Limit: -1})
	require.Error(t, err)
	for _, c := range []string{"!", "e30", base64.RawURLEncoding.EncodeToString([]byte(`{"id":"x"}`))} {
		page, err := s.ListRuns(ctx, durable.RunFilter{Cursor: c})
		require.Error(t, err, c)
		require.Contains(t, err.Error(), "invalid cursor")
		require.Len(t, page.Runs, 0)
	}
}

// TestDeleteEndedRunsKeepsLiveRuns: a run that has not ended stays,
// however old, as does a run that ended after the cutoff (PRD 011, UC-4).
func testDeleteEndedRunsKeepsLiveRuns(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()

	startRun(t, s, "old", load(t, doneDoc), nil)
	startRun(t, s, "running", load(t, oneStep), nil)
	startRun(t, s, "failing", load(t, failingDoc), nil)
	failStep(t, s, "failing")
	waitStatus(t, s, "failing", workflow.StatusFailing)
	cutoff := justAfter(t, s, "old")
	tick()
	startRun(t, s, "late", load(t, doneDoc), nil)

	n, err := s.DeleteEndedRuns(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = s.GetRun(ctx, "old")
	require.ErrorIs(t, err, ErrRunNotFound)
	for _, id := range []string{"running", "failing", "late"} {
		_, err := s.GetRun(ctx, id)
		require.NoError(t, err, id)
	}
	require.True(t, s.count(t, "jobs", "failing") > 0, "the failing run keeps its job")

	n, err = s.DeleteEndedRuns(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

// TestDeleteEndedRunsRemovesRecords: deleted runs lose their steps, events
// and jobs on every backend, the batches add up, and definitions stay
// (PRD 011, UC-4).
func testDeleteEndedRunsRemovesRecords(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	step, done := load(t, oneStep), load(t, doneDoc)

	startRun(t, s, "stepped", step, nil)
	_, err := s.Deliver(ctx, "stepped",
		workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/a#1", Output: "ok"})
	require.NoError(t, err)
	startRun(t, s, "cancelled", step, nil)
	_, err = s.Cancel(ctx, "cancelled", "test")
	require.NoError(t, err)
	require.True(t, s.count(t, "steps", "stepped") > 0)
	require.True(t, s.count(t, "events", "stepped") > 0)

	const n = hostcore.DeleteBatch + 3
	for i := range n - 2 {
		startRun(t, s, fmt.Sprintf("d%03d", i), done, nil)
	}
	cutoff := justAfter(t, s, fmt.Sprintf("d%03d", n-3))

	deleted, err := s.DeleteEndedRuns(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, n, deleted)
	for _, table := range []string{"runs", "steps", "events", "jobs"} {
		require.Equal(t, 0, s.count(t, table, ""), table)
	}
	steps, err := s.Steps(ctx, "stepped")
	require.NoError(t, err)
	require.Len(t, steps, 0)
	events, err := s.Events(ctx, "stepped")
	require.NoError(t, err)
	require.Len(t, events, 0)
	require.Equal(t, 2, s.count(t, "definitions", ""))

	// A context that has ended deletes nothing more.
	startRun(t, s, "after", done, nil)
	ended, cancel := context.WithCancel(ctx)
	cancel()
	deleted, err = s.DeleteEndedRuns(ended, justAfter(t, s, "after"))
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 0, deleted)
	require.Equal(t, 1, s.count(t, "runs", "after"))
}

// TestDeleteEndedRunsSkipsAHeldRun: a run another transaction holds is
// left for the next call, and the call still deletes every other ended
// run, across batches (PRD 011, UC-4). SQLite has one writer, so a held
// lock blocks the whole call there instead; the test is Postgres only.
func testDeleteEndedRunsSkipsAHeldRun(t *testing.T, h Harness) {
	if !h.Caps().RowLocks {
		t.Skip("a run lock blocks every write on this backend")
	}
	s := open(t, h)
	ctx := context.Background()
	done := load(t, doneDoc)

	startRun(t, s, "held", done, nil)
	const n = hostcore.DeleteBatch + 10
	for i := range n {
		startRun(t, s, fmt.Sprintf("d%03d", i), done, nil)
	}
	cutoff := justAfter(t, s, fmt.Sprintf("d%03d", n-1))

	release := s.holdRunLock(t, "held")
	deleted, err := s.DeleteEndedRuns(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, n, deleted)
	require.Equal(t, 1, s.count(t, "runs", ""))
	release()

	deleted, err = s.DeleteEndedRuns(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
}

// TestDeleteEndedRunsFreesTheID: after its run is deleted, an ID starts a
// new run; retention ends Start's duplicate check (PRD 011, UC-4).
func testDeleteEndedRunsFreesTheID(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	def := load(t, doneDoc)

	startRun(t, s, "webhook-1", def, nil)
	res, err := s.Start(ctx, NewRun{ID: "webhook-1", Definition: def})
	require.NoError(t, err)
	require.False(t, res.Applied, "a duplicate while the run exists")
	first, err := s.GetRun(ctx, "webhook-1")
	require.NoError(t, err)

	n, err := s.DeleteEndedRuns(ctx, first.UpdatedAt.Add(time.Microsecond))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	tick()

	startRun(t, s, "webhook-1", def, nil)
	second, err := s.GetRun(ctx, "webhook-1")
	require.NoError(t, err)
	require.True(t, second.CreatedAt.After(first.CreatedAt))
}
