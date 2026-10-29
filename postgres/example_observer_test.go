package postgres_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExampleObserver counts the worker's events, as a metrics counter would,
// and reads the lag of a queue that no worker serves.
func ExampleObserver() {
	ctx := context.Background()
	store, done := observerStore(ctx, "example_observer")
	defer done()

	var mu sync.Mutex
	counts := map[string]int{}
	count := func(event string) { mu.Lock(); counts[event]++; mu.Unlock() }
	w := &postgres.Worker{
		Store:    store,
		Handlers: map[string]workflow.Handler{"action": observerDeploy},
		Observer: postgres.Observer{
			Claimed: func(workflow.Job) { count("claimed") },
			Settled: func(_ workflow.Job, s postgres.Settlement) { count(string(s.Outcome)) },
		},
	}
	observerRunToEnd(ctx, w, `{"steps": [
	  {"name": "build", "action": "make", "next": ["ship"]},
	  {"name": "ship", "action": "upload"}]}`)
	fmt.Println(counts)

	// A review step waits for an agent worker that is not running.
	observerStart(ctx, store, "review", `{"steps": [{"name": "review", "agent": "reviewer"}]}`)
	stats, _ := store.QueueStats(ctx)
	for _, q := range stats {
		fmt.Println(q.Kind, q.StepKind, q.Due)
	}
	// Output:
	// map[claimed:2 completed:1 failed:1]
	// dispatch_step agent 1
}

func observerDeploy(ctx context.Context, job workflow.Job) (any, error) {
	if job.Step == "ship" {
		return nil, &workflow.StepError{Type: "unavailable", Message: "registry is down"}
	}
	return "built", nil
}

var observerKinds = workflow.KindMap{"action": nil, "agent": nil}

// observerStore returns a migrated store in a fresh schema, and a function
// that drops the schema.
func observerStore(ctx context.Context, schema string) (*postgres.Store, func()) {
	pool, err := pgxpool.New(ctx, os.Getenv("WORKFLOW_PG_DSN"))
	if err != nil {
		panic(err)
	}
	drop := func() { _, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE") }
	drop()
	store, err := postgres.New(pool, observerKinds, postgres.WithSchema(schema))
	if err != nil {
		panic(err)
	}
	if err := store.Migrate(ctx); err != nil {
		panic(err)
	}
	return store, func() {
		drop()
		pool.Close()
	}
}

func observerStart(ctx context.Context, store *postgres.Store, id, doc string) {
	def, err := workflow.Load([]byte(doc), observerKinds)
	if err != nil {
		panic(err)
	}
	if _, err := store.Start(ctx, postgres.NewRun{ID: id, Definition: def}); err != nil {
		panic(err)
	}
}

// observerRunToEnd starts a run, runs w until the run finishes, then stops w.
func observerRunToEnd(ctx context.Context, w *postgres.Worker, doc string) {
	wctx, stop := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = w.Run(wctx)
	}()
	observerStart(ctx, w.Store, "example", doc)
	for {
		run, err := w.Store.GetRun(ctx, "example")
		if err != nil {
			panic(err)
		}
		if run.Status.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	<-stopped
}
