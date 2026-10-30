package hostcore

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestRunQueryLimit: 0 reads 100, a large limit reads 1000, and the
// backend is asked for one row more, to know whether a page follows.
func TestRunQueryLimit(t *testing.T) {
	s := &Store{Prefix: "test"}
	for _, c := range []struct{ in, want int }{{0, 101}, {1, 2}, {1000, 1001}, {5000, 1001}} {
		q, err := s.runQuery(durable.RunFilter{Limit: c.in})
		require.NoError(t, err)
		require.Equal(t, c.want, q.Limit, c.in)
	}
	_, err := s.runQuery(durable.RunFilter{Limit: -1})
	require.Error(t, err)
}

// TestCursorRoundTrip: the cursor a page makes decodes to the last run's
// position, to the microsecond.
func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 29, 1, 2, 3, 456789000, time.UTC)
	b := listBackend{rows: []durable.RunSummary{
		{ID: "b", CreatedAt: at.Add(time.Second)}, {ID: "a", CreatedAt: at}, {ID: "z"},
	}}
	s := &Store{Prefix: "test"}
	page, err := s.ListRuns(t.Context(), b, durable.RunFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Runs, 2)
	q, err := s.runQuery(durable.RunFilter{Cursor: page.Next})
	require.NoError(t, err)
	require.Equal(t, &Position{CreatedAt: at, ID: "a"}, q.After)
}

// TestCursorKeepsFractionalSeconds: a cursor taken after a row at
// 12:00:00.900 reads after exactly that time, so a row at 12:00:00.100
// comes on the next page instead of being skipped.
func TestCursorKeepsFractionalSeconds(t *testing.T) {
	late := time.Date(2026, 10, 29, 12, 0, 0, 900_000_000, time.UTC)
	early := time.Date(2026, 10, 29, 12, 0, 0, 100_000_000, time.UTC)
	b := listBackend{rows: []durable.RunSummary{
		{ID: "late", CreatedAt: late}, {ID: "early", CreatedAt: early},
	}}
	s := &Store{Prefix: "test"}
	page, err := s.ListRuns(t.Context(), b, durable.RunFilter{Limit: 1})
	require.NoError(t, err)
	q, err := s.runQuery(durable.RunFilter{Cursor: page.Next})
	require.NoError(t, err)
	require.Equal(t, late, q.After.CreatedAt)
	require.True(t, early.Before(q.After.CreatedAt), "the next page still reads the earlier row")
}

// listBackend returns its rows from ListRuns, up to the limit.
type listBackend struct {
	Backend
	rows []durable.RunSummary
}

func (b listBackend) ListRuns(_ context.Context, q RunQuery) ([]durable.RunSummary, error) {
	return b.rows[:min(q.Limit, len(b.rows))], nil
}
