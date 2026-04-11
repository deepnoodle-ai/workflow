# Workflow

A Go library for defining and executing multi-step processes as
directed graphs. Conditional branching, parallel execution,
expression-driven templating, durable checkpointing, and
suspend/resume on signals or wall-clock waits.

Think of it like a lightweight hybrid of Temporal and AWS Step
Functions, with everything that doesn't belong inside an execution
engine pushed out to interfaces consumers implement.

Edge conditions and `${...}` parameter templates are evaluated by
[`github.com/deepnoodle-ai/expr`](https://github.com/deepnoodle-ai/expr),
a small zero-dependency expression evaluator with a Go-like syntax.
It is the only external dependency of the root module.

## Main concepts

| Concept        | Description                                                                            |
| -------------- | -------------------------------------------------------------------------------------- |
| **Workflow**   | A repeatable process defined as a directed graph of steps                              |
| **Step**       | A node in the graph — runs an activity, joins branches, sleeps, waits, or pauses      |
| **Activity**   | A function that performs the actual work                                               |
| **Edge**       | Defines flow between steps, optionally guarded by a condition                          |
| **Execution** | A single run of a workflow, with its own state                                         |
| **Branch**     | An independent execution thread with its own copy of state                             |
| **State**      | Branch-local mutable variables that activities read and write                          |
| **Runner**     | Production entry point that composes heartbeat, timeout, resume, and completion hooks |

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

The `Runner` is the recommended entry point for production code. It
composes heartbeating, default timeouts, resume-from-checkpoint, and
completion hooks. For one-shot scripts and tests, calling
`exec.Execute(ctx)` directly is fine.

## Going to production

The library is a pure execution engine. Storage, scheduling, signal
delivery, and worker leasing are the consumer's responsibility — the
library defines interfaces (`Checkpointer`, `StepProgressStore`,
`ActivityLogger`, `SignalStore`, `WorkflowRegistry`) and you wire
your own backends. The bundled `MemoryCheckpointer` and
`FileCheckpointer` are for development only.

See [`docs/production_checklist.md`](docs/production_checklist.md)
for the full punch list, and [`docs/suspension.md`](docs/suspension.md)
for the suspend / resume / replay-safety contract.

## Reference

- [`llms.txt`](llms.txt) — full API reference, including the JSON
  workflow format and the script-compiler interface.
- [`MIGRATION.md`](MIGRATION.md) — every breaking change between
  pre-v1 and v1, with before/after snippets.
- [`examples/`](examples/) — runnable example programs covering
  branching, joins, retries, child workflows, and more. See
  [`examples/signal_wait/`](examples/signal_wait),
  [`examples/durable_sleep/`](examples/durable_sleep), and
  [`examples/pause_unpause/`](examples/pause_unpause) for the
  suspend/resume primitives.
