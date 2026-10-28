// Command approval runs a deploy that waits for a human, backed by
// Postgres. The approval step hands off to people and returns ErrDeferred;
// the worker then stops, as if the service were redeployed. Nothing holds
// the run but its row. The approval arrives later through Store.Deliver,
// as from a webhook or an API call, and a new worker finishes the run.
//
//	WORKFLOW_PG_DSN=postgres://... go run ./examples/approval
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed workflow.json
var doc []byte

const schema = "example_approval"

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
	// Start clean each time, and leave nothing behind.
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		log.Fatal(err)
	}
	defer pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")

	kinds := workflow.KindMap{"action": nil, "approval": nil}
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

	// The handlers. approval asks people and returns ErrDeferred: the step
	// stays in flight in the run, with no worker holding it.
	asked := make(chan workflow.StepID, 1)
	handlers := map[string]workflow.Handler{
		"action": func(ctx context.Context, t workflow.Job) (any, error) {
			params, _ := json.Marshal(t.Params)
			fmt.Printf("worker: %-18s %s %s\n", t.StepID, t.Config["action"], params)
			if t.Step == "build" {
				return map[string]any{"image": "registry.example.com/app:" + t.Params["version"].(string)}, nil
			}
			return map[string]any{"ok": true}, nil
		},
		"approval": func(ctx context.Context, t workflow.Job) (any, error) {
			fmt.Printf("worker: %-18s asked %s to approve %s\n", t.StepID, t.Config["approval"], t.Params["image"])
			asked <- t.StepID
			return nil, workflow.ErrDeferred
		},
	}

	stop := runWorker(store, handlers)
	if _, err := store.Start(ctx, postgres.NewRun{
		ID: "deploy-1.4.0", Definition: def, Input: map[string]any{"version": "1.4.0"},
	}); err != nil {
		log.Fatal(err)
	}
	approval := <-asked
	stop()
	run, err := store.GetRun(ctx, "deploy-1.4.0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nworker stopped; run %s is %s, in flight %v\n\n", run.ID, run.Status, run.State.InFlight())

	// Some time later, someone approves. Deliver applies the event in one
	// transaction: it locks the run row, calls Apply, and writes the new
	// state and the deploy job.
	stop = runWorker(store, handlers)
	defer stop()
	approved := workflow.Event{Kind: workflow.EventStepCompleted, StepID: approval,
		Output: map[string]any{"approved": true, "by": "grace"}}
	res, err := store.Deliver(ctx, run.ID, approved)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("api:    grace approves (applied: %v)\n", res.Applied)
	// A webhook retried: the same event again changes nothing.
	if res, err = store.Deliver(ctx, run.ID, approved); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("api:    retried approval (applied: %v)\n", res.Applied)

	for !run.Status.Terminal() {
		time.Sleep(50 * time.Millisecond)
		if run, err = store.GetRun(ctx, run.ID); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("\nrun %s is %s, outputs %v\n\nsteps:\n", run.ID, run.Status, run.State.Outputs)
	steps, err := store.Steps(ctx, run.ID)
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range steps {
		fmt.Printf("  %-18s %-9s %s\n", s.StepID, s.StepKind, s.Status)
	}
	events, err := store.Events(ctx, run.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("events:")
	for _, e := range events {
		fmt.Printf("  %-15s %s\n", e.Kind, e.StepID)
	}
}

// runWorker runs a worker until the returned function is called.
func runWorker(store *postgres.Store, handlers map[string]workflow.Handler) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := &postgres.Worker{Store: store, Handlers: handlers, Poll: 100 * time.Millisecond}
		if err := w.Run(ctx); err != nil {
			log.Fatal(err)
		}
	}()
	return func() { cancel(); <-done }
}
