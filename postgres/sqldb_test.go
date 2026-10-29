package postgres

import (
	"context"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSQLHookStoreReleasesItsDB checks that a store with only
// WithOnFinishedSQL closes its *sql.DB once nothing can reach it, so a
// process that makes many stores does not keep a goroutine for each.
func TestSQLHookStoreReleasesItsDB(t *testing.T) {
	// The pool never connects: New and the cleanup do not touch Postgres.
	pool, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:1/none")
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	before := sqlOpeners()
	for range 10 {
		_, err := New(pool, kinds, WithOnFinishedSQL(noSQLHook))
		require.NoError(t, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for sqlOpeners() > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d database/sql openers still run after the stores became unreachable; "+
				"want %d", sqlOpeners(), before)
		}
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
}

// sqlOpeners counts the goroutines that open connections for a *sql.DB:
// one for each *sql.DB that is not closed.
func sqlOpeners() int {
	var b strings.Builder
	_ = pprof.Lookup("goroutine").WriteTo(&b, 2)
	return strings.Count(b.String(), "database/sql.(*DB).connectionOpener(")
}

func noSQLHook(context.Context, SQLTx, Finished) error { return nil }
