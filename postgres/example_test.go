package postgres_test

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// On SIGTERM, the worker stops claiming, records the step that finishes
// before the deadline, and gives back the one that does not. The next
// worker runs that step at once, without waiting for its lease.
func ExampleWorker_Shutdown() {
	store, def, cleanup := exampleSetup() // a store in its own schema; a one-step workflow
	defer cleanup()
	ctx := context.Background()
	for _, id := range []string{"fast", "slow"} {
		_, _ = store.Start(ctx, postgres.NewRun{ID: id, Definition: def})
	}

	w := &postgres.Worker{Store: store, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			if job.RunID == "slow" {
				<-ctx.Done() // still running at the deadline
				return nil, ctx.Err()
			}
			time.Sleep(100 * time.Millisecond)
			return "done", nil
		},
	}}
	go w.Run(context.Background()) // cancel this context only for a hard stop

	sigs, stop := signal.NotifyContext(ctx, syscall.SIGTERM)
	defer stop()
	waitFor(func() bool { return w.Active() == 2 })
	sendSIGTERM() // as the platform does on a deploy
	<-sigs.Done()
	sctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	fmt.Println("shutdown:", w.Shutdown(sctx))
	printStatus(store, "fast", "slow")

	next := &postgres.Worker{Store: store, Handlers: map[string]workflow.Handler{
		"action": func(context.Context, workflow.Job) (any, error) { return "done", nil },
	}}
	go next.Run(ctx)
	waitFor(func() bool { return status(store, "slow") == workflow.StatusCompleted })
	printStatus(store, "slow")
	_ = next.Shutdown(ctx)
	// Output:
	// shutdown: context deadline exceeded
	// fast: completed
	// slow: running
	// slow: completed
}

// exampleSetup connects to WORKFLOW_PG_DSN and makes a store in a schema of
// its own, which cleanup drops.
func exampleSetup() (*postgres.Store, *workflow.Definition, func()) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("WORKFLOW_PG_DSN"))
	if err != nil {
		panic(err)
	}
	kinds := workflow.KindMap{"action": nil}
	schema := fmt.Sprintf("example_%d", time.Now().UnixNano())
	store, err := postgres.New(pool, kinds, postgres.WithSchema(schema))
	if err != nil {
		panic(err)
	}
	if err := store.Migrate(ctx); err != nil {
		panic(err)
	}
	def, err := workflow.Load([]byte(`{"steps": [{"name": "work", "action": "x"}]}`), kinds)
	if err != nil {
		panic(err)
	}
	return store, def, func() {
		_, _ = pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		pool.Close()
	}
}

func sendSIGTERM() {
	p, err := os.FindProcess(os.Getpid())
	if err == nil {
		err = p.Signal(syscall.SIGTERM)
	}
	if err != nil {
		panic(err)
	}
}

func waitFor(cond func() bool) {
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			panic("timed out")
		}
	}
}

func status(store *postgres.Store, runID string) workflow.RunStatus {
	r, err := store.GetRun(context.Background(), runID)
	if err != nil {
		panic(err)
	}
	return r.Status
}

func printStatus(store *postgres.Store, ids ...string) {
	for _, id := range ids {
		fmt.Printf("%s: %s\n", id, status(store, id))
	}
}
