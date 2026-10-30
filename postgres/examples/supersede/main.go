// Command supersede checks each commit pushed to a pull request, and
// cancels the check of an older commit when a newer one arrives. The host
// does it in one transaction: find the PR's running runs by metadata,
// cancel them with Store.CancelTx, and start the new run with
// Store.StartTx. Either all of it commits or none of it does.
//
// Look for the old run's finally step posting "superseded" while the new
// run's tests pass.
//
//	WORKFLOW_PG_DSN=postgres://... go run ./examples/supersede
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed workflow.json
var doc []byte

const schema = "example_supersede"

func main() {
	ctx := context.Background()
	dsn := os.Getenv("WORKFLOW_PG_DSN")
	if dsn == "" {
		log.Fatal("set WORKFLOW_PG_DSN to a Postgres database the example may create a schema in")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		log.Fatal(err)
	}
	defer pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")

	kinds := workflow.KindMap{"action": nil}
	store, err := postgres.New(pool, kinds, postgres.WithSchema(schema))
	if err != nil {
		log.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		log.Fatal(err)
	}
	def, err := workflow.Load(doc, kinds)
	if err != nil {
		log.Fatal(err)
	}

	testing := make(chan struct{}, 1)
	action := func(ctx context.Context, job workflow.Job) (any, error) {
		sha := job.Params["sha"]
		switch job.Step {
		case "tests":
			fmt.Printf("worker: tests for %s started\n", sha)
			if sha == "aaa111" {
				testing <- struct{}{}
				<-ctx.Done() // a slow suite, stopped when its run is cancelled
				return nil, ctx.Err()
			}
			fmt.Printf("worker: tests for %s passed\n", sha)
		case "status":
			outcome := job.Params["outcome"]
			if outcome == "cancelled" {
				outcome = "superseded"
			}
			fmt.Printf("worker: posted %s for %s\n", outcome, sha)
		}
		return map[string]any{"ok": true}, nil
	}
	wctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	defer func() {
		stop()
		<-done
	}()
	go func() {
		defer close(done)
		w := &postgres.Worker{
			Store:    store,
			Handlers: map[string]workflow.Handler{"action": action},
			Poll:     50 * time.Millisecond,
			Lease:    300 * time.Millisecond,
		}
		if err := w.Run(wctx); err != nil {
			log.Fatal(err)
		}
	}()

	push(ctx, pool, store, def, "aaa111")
	<-testing
	push(ctx, pool, store, def, "bbb222")

	for _, id := range []string{"pr-42-aaa111", "pr-42-bbb222"} {
		run, err := store.GetRun(ctx, id)
		for err == nil && !run.Status.Terminal() {
			time.Sleep(50 * time.Millisecond)
			run, err = store.GetRun(ctx, id)
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("run %s: %s %s\n", run.ID, run.Status, run.State.Reason)
	}
}

// push handles a new commit on PR 42 in one transaction.
func push(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *postgres.Store,
	def *workflow.Definition,
	sha string,
) {
	var old []string
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Serialize pushes to one PR, so two at once cannot both miss the
		// other's new run.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(42)"); err != nil {
			return err
		}
		// The store has no query API for runs yet, so read its runs table
		// directly. The GIN index on runs.metadata serves @>. A paused run
		// is superseded too, or it would stay paused for ever.
		rows, err := tx.Query(ctx,
			"SELECT id FROM "+schema+".runs WHERE metadata @> $1::jsonb"+
				" AND status IN ('running', 'paused')",
			`{"pr": 42}`)
		if err != nil {
			return err
		}
		old, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range old {
			if _, err := store.CancelTx(ctx, tx, id, "superseded by "+sha); err != nil {
				return err
			}
		}
		_, err = store.StartTx(ctx, tx, postgres.NewRun{
			ID:         "pr-42-" + sha,
			Definition: def,
			Input:      map[string]any{"sha": sha},
			Metadata:   map[string]any{"pr": 42, "sha": sha},
		})
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("host:   push %s: cancelled %v, started pr-42-%s\n", sha, old, sha)
}
