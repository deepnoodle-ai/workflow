package postgres

import (
	"flag"
	"os"
	"testing"
)

// TestMain skips the Example tests when WORKFLOW_PG_DSN is unset: examples
// cannot skip themselves, and every example here needs Postgres.
func TestMain(m *testing.M) {
	flag.Parse()
	if os.Getenv(dsnEnv) == "" {
		_ = flag.Set("test.skip", "^Example")
	}
	os.Exit(m.Run())
}
