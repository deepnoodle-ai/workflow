package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/sqlite"
	_ "modernc.org/sqlite" // or github.com/mattn/go-sqlite3, as "sqlite3"
)

// Example runs a two-step workflow on a SQLite file: a worker runs each
// step, and the run completes with its output.
func Example() {
	dir, _ := os.MkdirTemp("", "workflow")
	defer os.RemoveAll(dir)
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(dir, "dev.db")+"?_busy_timeout=5000&_journal_mode=WAL")
	defer db.Close()

	ctx := context.Background()
	kinds := workflow.KindMap{"action": nil}
	store, _ := sqlite.New(db, kinds)
	_ = store.Migrate(ctx)
	w := &sqlite.Worker{Store: store, Handlers: map[string]workflow.Handler{
		"action": func(_ context.Context, job workflow.Job) (any, error) { return job.Step + " done", nil },
	}}

	def, _ := workflow.Load([]byte(`{"steps": [
	  {"name": "build", "action": "ci", "next": ["ship"]}, {"name": "ship", "action": "deploy"}],
	  "outputs": {"shipped": "${{ ship }}"}}`), kinds)
	_, _ = store.Start(ctx, sqlite.NewRun{ID: "order-42", Definition: def})
	_ = w.RunOnce(ctx) // runs build, then the ship step it dispatches
	run, _ := store.GetRun(ctx, "order-42")
	fmt.Println(run.Status, run.State.Outputs["shipped"])
	// Output: completed ship done
}
