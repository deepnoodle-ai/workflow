# Examples

Each is a short program with its definition beside it in a JSON file. Read
them in this order.

| Example | Shows | Run |
| --- | --- | --- |
| [apply](apply) | The engine with no runner: `Apply` per event, state stored as JSON between events, a fan-out completed out of order, a duplicate event ignored. | `go run ./examples/apply` |
| [typed](typed) | `Validate` catching a misspelled field, a switch case outside the enum, a read of a step that may not have run, and a parameter of the wrong type for a tool; `switch` routing; `coalesce` over alternatives; the runtime rejecting an output that does not match its schema. | `go run ./examples/typed` |
| [retry](retry) | Retry with backoff, a timeout, `catch` routing a failure to another step, and `on_failure` when nothing handles it. | `go run ./examples/retry` |
| [approval](../postgres/examples/approval) | The Postgres host: a step that waits for a person (`ErrDeferred`), the worker stopping while the run waits, the approval arriving through `Store.Deliver`, and a duplicate delivery. | `cd postgres && WORKFLOW_PG_DSN=... go run ./examples/approval` |
| [prreview](prreview) | The full PR review from the design: fan-out to three reviewers, a join, `each` with concurrency, and posting a review. | `go run ./examples/prreview` |
| [cli](cli) | A definition for the `workflow` CLI with shell steps. | `go run ./cmd/workflow run examples/cli/release.json -input version=1.0.0` |

For the approval example, any Postgres works; it creates and drops a schema
named `example_approval`:

```sh
docker run -d --name workflow-pg -e POSTGRES_PASSWORD=pg -p 127.0.0.1:55432:5432 postgres:18
cd postgres && WORKFLOW_PG_DSN="postgres://postgres:pg@127.0.0.1:55432/postgres?sslmode=disable" go run ./examples/approval
```
