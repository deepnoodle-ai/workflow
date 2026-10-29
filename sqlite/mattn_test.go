//go:build sqlite_mattn

package sqlite

import (
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/hosttest"
	_ "github.com/mattn/go-sqlite3"
)

// mattn runs the suite on mattn/go-sqlite3 (cgo). It does not end a wait
// for the lock when its context ends, so a write waits up to the busy
// timeout; the DSN sets 1000 ms, as the docs advise for Shutdown deadlines.
// Its transactions start with BEGIN IMMEDIATE, as the docs advise for the
// application's own.
var mattn = harness{
	name:   "mattn",
	driver: "sqlite3",
	dsn:    "file:%s?_busy_timeout=1000&_journal_mode=WAL&_txlock=immediate",
	caps:   hosttest.Caps{BusyTimeout: time.Second},
}

// TestSuiteMattn runs the durable host suite on SQLite with mattn. Run it
// with CGO_ENABLED=1 go test -tags sqlite_mattn.
func TestSuiteMattn(t *testing.T) { hosttest.Run(t, mattn) }
