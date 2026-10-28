package postgres

import (
	"context"
	"sync"
	"testing"
	"uuid"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestConcurrentMigrate starts many processes at once against an empty
// schema, as a Cloud Run deploy does. Every Migrate succeeds and the schema
// is migrated once.
func TestConcurrentMigrate(t *testing.T) {
	base := newStore(t)
	schema := "m_" + uuid.NewV4().String()[:8]
	t.Cleanup(func() { _, _ = base.pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for range 40 {
		wg.Go(func() {
			s, err := New(base.pool, kinds, WithSchema(schema))
			if err == nil {
				err = s.Migrate(context.Background())
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var versions int
	require.NoError(t, base.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM "+schema+".schema_migrations").Scan(&versions))
	require.Equal(t, len(migrations), versions)

	// Migrating again is a no-op.
	s, err := New(base.pool, kinds, WithSchema(schema))
	require.NoError(t, err)
	require.NoError(t, s.Migrate(context.Background()))
}
