# Examples

Each is a short program with its definition beside it in a JSON file. Read
them in this order.

| Example | Shows | Run |
| --- | --- | --- |
| [apply](apply) | The engine with no runner: `Apply` per event, state stored as JSON between events, a fan-out completed out of order, a duplicate event ignored. | `go run ./examples/apply` |
| [typed](typed) | `Load` catching a misspelled field, a switch case outside the enum, a read of a step that may not have run, and a parameter of the wrong type for a tool; `switch` routing; `coalesce` over alternatives; the runtime rejecting an output that does not match its schema. | `go run ./examples/typed` |
| [retry](retry) | Retry with backoff, a timeout, `catch` routing a failure to another step, `on_failure` when nothing handles it, a `finally` step that runs whatever the outcome, and an output with a schema. | `go run ./examples/retry` |
| [loop](loop) | A poll loop: `sleep` between polls, `match: first` to leave when done, `max_visits` as the bound. Each poll is a new visit (`@2`, `@3`) and the run's state (`State.Bytes`) stays the same size. | `go run ./examples/loop` |
| [approval](../postgres/examples/approval) | The Postgres host: a `wait` step for a person, the worker stopping while the run waits, the approval delivered by step name through `Store.Deliver`, and a duplicate delivery. | `cd postgres && WORKFLOW_PG_DSN=... go run ./examples/approval` |

## Patterns

Each shows one pattern built from the pieces above, in a few dozen lines.

| Example | Shows | Run |
| --- | --- | --- |
| [prreview](prreview) | A PR review: fan-out to three reviewers, a join, `each` with a concurrency cap, and posting a review. | `go run ./examples/prreview` |
| [decide](decide) | Decision edges: `decide` in two `when` edges approves, denies or sends a refund to a person, for one ask; a fake decision handler stands in for the model. | `go run ./examples/decide` |
| [codefix](codefix) | An agent loop: draft, test and lint in parallel, join, and a `switch` on a judge's verdict that loops back (`@2`), bounded by `max_visits`; the draft reads the last pass's failures. | `go run ./examples/codefix` |
| [mapreduce](mapreduce) | `each` over 20 documents with `concurrency: 4` and `continue_on_error`; a `set` step splits successes from a dead-letter list with `filter`. | `go run ./examples/mapreduce` |
| [saga](saga) | Compensation: `on_failure` steps read `reserve?.reservation_id` and `charge?.id` to undo only what succeeded; a retried charge keeps its idempotency key. | `go run ./examples/saga` |
| [canary](canary) | A health poll loop with `sleep` and `max_visits`; an error spike fails the run and `on_failure` rolls back. | `go run ./examples/canary` |
| [gate](gate) | An approval gate with a built-in `wait` step: no handler, the answer delivered by step name, and a `catch` on timeout. | `go run ./examples/gate` |
| [escalation](escalation) | Each tier pages, then waits for an ack in a `wait` step delivered by name through `Run.Deliver`; its `timeout` with a `catch` on timeout escalates to the next tier. | `go run ./examples/escalation` |
| [toolcall](toolcall) | A workflow as a tool: its `Input` and `OutputSchema()` as a tool definition, and a host `workflow` kind that runs it as a child run, type-checked at Load through `KindSchemas`. | `go run ./examples/toolcall` |
| [authoring](authoring) | `Load` as a compiler for an agent that writes workflows: its problems go back to the model until the definition loads. | `go run ./examples/authoring` |
| [planner](planner) | An agent extends the run as it goes: each plan is a workflow the next step runs as a child, loaded with `RequireOutputs` against the step's output schema. A plan that does not fit is caught and sent back to the planner. | `go run ./examples/planner` |
| [checkpoint](checkpoint) | A CLI made durable with no database: `Save` writes the state to a file, and the next invocation resumes the step in flight without repeating finished ones. | `go run ./examples/checkpoint` |
| [replay](replay) | A time-travel debugger on `Apply` alone: replay a run's events one at a time, and reorder parallel completions to get the same result. | `go run ./examples/replay` |
| [modelreplay](modelreplay) | Test a new decision model before you ship it: record a batch of runs with model A, replay each through `Apply` with model B answering the decisions, and report the runs that would take another route. | `go run ./examples/modelreplay` |
| [supersede](../postgres/examples/supersede) | A new push cancels the PR's older runs and starts the new one in one transaction (`CancelTx`, `StartTx`); the old run's `finally` step reports it superseded. | `cd postgres && WORKFLOW_PG_DSN=... go run ./examples/supersede` |

For the Postgres examples, any Postgres works; each creates and drops its own
schema:

```sh
docker run -d --name workflow-pg -e POSTGRES_PASSWORD=pg -p 127.0.0.1:55432:5432 postgres:18
cd postgres && WORKFLOW_PG_DSN="postgres://postgres:pg@127.0.0.1:55432/postgres?sslmode=disable" go run ./examples/approval
```
