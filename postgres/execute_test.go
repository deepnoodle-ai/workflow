package postgres

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// start starts one run and returns its job references.
func start(t *testing.T, s *Store, doc, id string) []JobRef {
	t.Helper()
	res, err := s.Start(context.Background(), NewRun{ID: id, Definition: load(t, doc)})
	require.NoError(t, err)
	return res.Jobs()
}

// jobState reads a job's claims and fence by its reference.
func jobState(t *testing.T, s *Store, ref JobRef) (claims int, fence int64) {
	t.Helper()
	require.NoError(t, s.pool.QueryRow(context.Background(), s.sql(`
		SELECT claims, fence FROM {{schema}}.jobs
		WHERE run_id = $1 AND step_id = $2 AND kind = $3`),
		ref.RunID, string(ref.StepID), hostcore.JobKind(ref)).Scan(&claims, &fence))
	return claims, fence
}

// TestExecuteReportsADefinitionLoadError: Execute returns a database error
// that kept it from loading the job's definition, so the task is retried;
// the job stays under its lease and its handler does not run.
func TestExecuteReportsADefinitionLoadError(t *testing.T) {
	s := newStore(t)
	ref := start(t, s, oneStep, "r")[0]
	fresh := breakDefinitions(t, s)
	handlers, calls := counting()
	w := &Worker{Store: fresh, Handlers: handlers, Logger: slog.New(slog.DiscardHandler)}

	ex, err := w.Execute(context.Background(), ref)
	require.Error(t, err)
	require.False(t, ex.Settled)
	require.False(t, errors.Is(err, ErrNotClaimable), err.Error())
	require.False(t, errors.Is(err, ErrDefinitionUnavailable), err.Error())
	require.True(t, strings.Contains(err.Error(), "load definition"), err.Error())
	require.Equal(t, 0, calls("r"))
	claims, leased := soleJob(t, s, "r")
	require.Equal(t, 1, claims)
	require.True(t, leased, "left under its lease")
}
