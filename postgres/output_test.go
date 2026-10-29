package postgres

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestUnstorableErrorsAreRecognized checks the worker's fallback: an error
// Postgres raises for a value it cannot store fails the step instead of
// leaving the job to be claimed again.
func TestUnstorableErrorsAreRecognized(t *testing.T) {
	s := newStore(t)
	_, err := s.pool.Exec(context.Background(), `SELECT $1::jsonb`, `"\u0000"`)
	require.Error(t, err)
	require.True(t, isUnstorable(err), err)
	_, err = s.pool.Exec(context.Background(), `SELECT 1/0`)
	require.True(t, isUnstorable(err)) // division by zero is class 22 too; never transient
	require.False(t, isUnstorable(errLeaseLost))
}
