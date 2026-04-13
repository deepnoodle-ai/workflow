# Workflow

A Go library for defining and running multi-step processes as
directed graphs. You get conditional branching, parallel execution,
expression-driven templates, durable checkpointing, and the ability
to suspend and resume on signals or wall-clock waits.

Think of it like a lightweight hybrid of Temporal and AWS Step
Functions. The execution engine is all that lives inside; everything
that doesn't belong inside an engine — storage, queues, leasing — is
an interface you implement however you like.

Edge conditions and `${...}` parameter templates are evaluated by
[`github.com/deepnoodle-ai/expr`](https://github.com/deepnoodle-ai/expr),
a small zero-dependency expression evaluator with a Go-like syntax.
It's the only external dependency of the root module.

## Main concepts

| Concept       | Description                                                                       |
| ------------- | --------------------------------------------------------------------------------- |
| **Workflow**  | A repeatable process defined as a directed graph of steps                         |
| **Step**      | A node in the graph — runs an activity, joins branches, sleeps, waits, or pauses  |
| **Activity**  | A function that performs the actual work                                          |
| **Edge**      | Defines flow between steps, optionally guarded by a condition                     |
| **Execution** | A single run of a workflow, with its own state                                    |
| **Branch**    | An independent execution thread with its own copy of state                        |
| **State**     | Branch-local mutable variables that activities read and write                     |
| **Runner**    | Convenience entry point that composes heartbeat, timeout, resume, and hooks       |

## Quick example

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

func main() {
	attempt := 0

	myOperation := func(ctx workflow.Context, _ map[string]any) (string, error) {
		attempt++
		if attempt < 3 {
			return "", fmt.Errorf("service is temporarily unavailable")
		}
		return "SUCCESS", nil
	}

	wf, err := workflow.New(workflow.Options{
		Name: "demo",
		Steps: []*workflow.Step{
			{
				Name:     "Call My Operation",
				Activity: "my_operation",
				Store:    "result",
				Retry:    []*workflow.RetryConfig{{MaxRetries: 2}},
				Next:     []*workflow.Edge{{Step: "Finish"}},
			},
			{
				Name:     "Finish",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Workflow completed. Result: ${state.result}",
				},
			},
		},
		Outputs: []*workflow.Output{
			{Name: "result", Variable: "result"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	reg := workflow.NewActivityRegistry()
	reg.MustRegister(
		workflow.TypedActivityFunc("my_operation", myOperation),
		activities.NewPrintActivity(),
	)

	exec, err := workflow.NewExecution(wf, reg)
	if err != nil {
		log.Fatal(err)
	}

	runner := workflow.NewRunner()
	result, err := runner.Run(context.Background(), exec)
	if err != nil {
		log.Fatal(err)
	}
	if !result.Completed() {
		log.Fatalf("execution did not complete: %v", result.Error)
	}

	if got, ok := result.OutputString("result"); ok {
		fmt.Println("final result:", got)
	}
}
```

For one-shot scripts and tests, calling `exec.Execute(ctx)` directly
is perfectly fine. The `Runner` is the convenient wrapper when you
also want heartbeating, default timeouts, resume-from-checkpoint, and
completion hooks.

## Bring your own storage

The library is a pure execution engine — it doesn't ship a database,
a queue, or a UI, and it never will. Instead it defines small
interfaces (`Checkpointer`, `StepProgressStore`, `ActivityLogger`,
`SignalStore`, `WorkflowRegistry`) and you wire up whatever backends
fit your stack. The bundled `MemoryCheckpointer` and
`FileCheckpointer` are great for development and tests.

If you'd rather not start from scratch, the `experimental/`
submodules give you a head start:

- [`experimental/worker/`](experimental/worker/) — a queue-backed
  durable worker with claim loop, heartbeat, reaper, and credit
  reconciliation. Handlers receive a `*HandlerContext` carrying the
  current `Claim` plus pre-fenced stores. The
  [`runquery`](experimental/worker/runquery/) subpackage exposes a
  backend-neutral read API (`GetRun`, `ListRuns`, `CountRuns`,
  `DeleteRun`) so dashboards can stay storage-agnostic.
- [`experimental/store/postgres/`](experimental/store/postgres/) —
  pgx-backed persistence that implements every store interface plus
  `runquery.Store`. Schema namespace is configurable via
  `WithSchema(...)` so it can live alongside other tables.
- [`experimental/store/sqlite/`](experimental/store/sqlite/) — the
  same surface backed by `database/sql`. Single-writer, perfect for
  dev and single-process deployments.

These submodules have their own `go.mod`, so the root module stays
stdlib-only. Their APIs are still being shaped — expect some churn.

## Where to look next

- [`documentation/`](documentation/) — friendly user guides for
  activities, branching, checkpointing, expressions, signals, sleep,
  pause, state management, testing, and more.
- [`llms.txt`](llms.txt) — the full API reference, including the JSON
  workflow format and the script-compiler interface.
- [`docs/worker.md`](docs/worker.md) and
  [`docs/postgres.md`](docs/postgres.md) — guides for the experimental
  worker and Postgres store.
- [`docs/suspension.md`](docs/suspension.md) — the suspend / resume /
  replay-safety contract.
- [`MIGRATION.md`](MIGRATION.md) — every breaking change between
  pre-v1 and v1, with before/after snippets.
- [`examples/`](examples/) — runnable example programs covering
  branching, joins, retries, child workflows, and the suspend/resume
  primitives ([`signal_wait`](examples/signal_wait),
  [`durable_sleep`](examples/durable_sleep),
  [`pause_unpause`](examples/pause_unpause)).
