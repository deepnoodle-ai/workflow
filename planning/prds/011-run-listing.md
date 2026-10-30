# List runs and delete ended runs on the durable hosts

Status: Approved
PRD PR: #92  Implementation PR: TBD  Updated: 2026-10-29
Tech spec: [specs/011-run-listing.md](../specs/011-run-listing.md)

## Problem

A Go developer runs the durable host (postgres or sqlite) in production.
They build an admin page or an ops script, and they need three things:

- "Show the failed runs from the last hour."
- "Find the run for customer 42."
- "Keep 30 days of history."

The Store can read one run by ID (`GetRun`, `Steps`, `Events`). It cannot
find runs. To list runs, the developer writes SQL against the `runs`
table, which is internal: its columns, indexes and JSON encoding differ
between postgres and sqlite, and a migration can change them.

Nothing deletes a run. The `runs`, `steps` and `events` tables grow with
every run and every attempt, forever. To delete old runs, the developer
writes SQL again. On sqlite they must also know that foreign keys are not
enforced (schema.sql says so), so a `DELETE FROM runs` leaves the run's
steps, events and jobs behind. A delete that misses a filter can remove a
run that is still running.

README lists "Listing, searching and retaining runs in postgres and
sqlite" under "Not yet".

## Context

**Our code.** Both backends share their policy in `internal/hostcore` and
implement a storage contract (`Backend`, `Tx`). `GetRun` reads one row of
`runs`. The `runs` table already has the columns to filter on: `status`,
`metadata`, `created_at`, `updated_at`, `finished_at`. Postgres indexes
`(status, updated_at)` and `metadata` (GIN, `jsonb_path_ops`). Sqlite
indexes `(status, updated_at)` only. A terminal run is never written
again: Apply ignores events on it, so its `updated_at` is its
`finished_at`. Postgres cascades a run's delete to `steps`, `jobs` and
`events`. Sqlite does not; the code must delete them.

**Comparable products.**

| | Listing | Retention |
|---|---|---|
| Temporal | `ListWorkflowExecutions` with a visibility query (status, type, start and close time, search attributes), page size and an opaque page token. | A namespace setting. The server deletes closed runs after the period. |
| Hatchet | A list-runs API with status, a time window, and key-value "additional metadata" filters; limit and offset. | A server setting. A background job deletes old runs. |
| Inngest | Runs listed by function, status and time, mostly in the dashboard. | Fixed by plan. The platform deletes old runs. |

All three filter on the same three things: status, time and a
user-attached key-value. All three run retention as a server job. We are
a library with no server and no background loop of our own, so retention
is a method the application calls from its own schedule. Temporal's
opaque token matches what we want: stable pages while new runs start.
Hatchet's offset paging repeats or skips rows when new runs start.

**What developers do today.** Hand-written SQL against internal tables,
different for each backend, with no guard against deleting a live run.

## Users and job

Primary: a developer who runs the durable host in production and builds an
admin page, an ops script, or a retention cron job. The job: find runs by
status, customer and time, and keep history bounded, with the same code on
postgres and sqlite and without SQL against internal tables.

## Concepts

- **Run summary** (`RunSummary`): one run as a list shows it: ID,
  workflow, status, metadata, error, and the three times. It has no state.
  Read the full run with `GetRun`. A summary's `Error` is the run's
  `State.Error`; `Run` has no `Error` field of its own.
- **Cursor**: an opaque string that says where the next page starts. Pass
  it back unchanged. Empty means the first page; an empty `Next` means no
  more runs.
- **Ended run**: a run whose status is terminal (completed, failed,
  cancelled). It is the only kind of run that `DeleteEndedRuns` deletes.

## Use cases

Each use case is a suite test in `internal/hosttest`, run by both
backends.

### UC-1: Show the failed runs from the last hour

```go
page, err := store.ListRuns(ctx, postgres.RunFilter{
    Status:       []workflow.RunStatus{workflow.StatusFailed},
    UpdatedAfter: time.Now().Add(-time.Hour),
    Limit:        50,
})
for _, r := range page.Runs {
    fmt.Println(r.ID, r.Status, r.Error.Message)
}
```

Acceptance:
- [ ] Only runs with status `failed` and `updated_at` after the time are
      returned, newest `created_at` first, at most 50. The list is sorted
      by start time: `UpdatedAfter` selects runs, it does not sort them.
      A run that started yesterday and failed a minute ago comes after
      runs that started today. The `ListRuns` doc and hosts.md say so.
- [ ] Each `RunSummary` carries the run's `Error`; `State` does not exist
      on it.
- [ ] A filter with two statuses returns runs of either status.
- [ ] An empty `Status` returns runs of every status.

### UC-2: Find the run for customer 42

```go
page, err := store.ListRuns(ctx, postgres.RunFilter{
    Metadata: map[string]any{"customer_id": "42"},
})
```

Acceptance:
- [ ] Returns only runs whose metadata has the string `customer_id`
      equal to `"42"`. A run with `customer_id: 42` (a number) or
      `customer_id: true` does not match.
- [ ] Two keys must both match.
- [ ] A filter value that is not a string (a number, bool, object, array
      or nil) returns an error that names the key, and reads no rows.

### UC-3: Page through many runs while new runs start

```go
filter := postgres.RunFilter{Limit: 100}
for {
    page, err := store.ListRuns(ctx, filter)
    // use page.Runs
    if page.Next == "" {
        break
    }
    filter.Cursor = page.Next
}
```

Acceptance:
- [ ] With many runs (the suite uses 300 and pages of 7) and runs
      started between page reads, the pages together hold each of the
      runs that existed at the first read exactly once.
- [ ] A run that changes status between pages does not move between
      pages (order is by `created_at`, then ID, which never change).
- [ ] `Limit` 0 means 100. A `Limit` above 1000 is read as 1000.
- [ ] A cursor that the store did not produce returns an error and no
      runs.

### UC-4: Keep 30 days of history

```go
n, err := store.DeleteEndedRuns(ctx, time.Now().Add(-30*24*time.Hour))
log.Printf("deleted %d runs", n)
```

Acceptance:
- [ ] Deletes every run with a terminal status whose `updated_at` and
      `finished_at` are both before the cutoff, and returns how many.
- [ ] Deletes the steps, events and jobs of each deleted run, on both
      backends. `GetRun`, `Steps` and `Events` then return
      `ErrRunNotFound` or nothing.
- [ ] Never deletes a run whose status is not terminal, however old,
      including `completing`, `failing` and `cancelling`.
- [ ] Deletes in batches of 500 runs, one transaction each, so a large
      backlog does not hold one long transaction. A context that ends
      stops between batches; the count so far comes back with the error.
      A batch that deletes fewer than 500 runs does not end the call; a
      batch that deletes none does. A run that a write holds at that
      moment is skipped until the next call.
- [ ] Calling it again with the same cutoff deletes nothing and returns 0.
- [ ] Definitions are kept.
- [ ] After a run is deleted, `Start` with the same ID starts a new run
      (`Applied` is true). Retention ends Start's duplicate check for
      those IDs. hosts.md says so in one sentence.

### UC-5: Same code on sqlite

Acceptance:
- [ ] `internal/parity` declares `ListRuns` and `DeleteEndedRuns` in its
      shared store interface, and both stores compile against it.
- [ ] UC-1 to UC-4 pass on both backends from one suite.

## Requirements

- R-1: `ListRuns(ctx, RunFilter) (RunPage, error)` on `postgres.Store` and
  `sqlite.Store`, with one implementation of the policy in hostcore.
- R-2: `RunFilter` fields: `Status []RunStatus`, `Metadata
  map[string]any`, `CreatedAfter`, `CreatedBefore`, `UpdatedAfter
  time.Time`, `Limit int`, `Cursor string`. A zero field filters nothing;
  an empty `Status` slice is a zero field. Time bounds are strict (`>`
  and `<`). A negative `Limit` is an error.
- R-3: `RunPage` is `{Runs []RunSummary; Next string}`. `RunSummary`,
  `RunFilter` and `RunPage` live in package `durable`, and each backend
  re-exports them, like `Run`.
- R-4: Status is compared as a string. A status the store has never
  stored, such as one a later release adds, needs no change here.
- R-5: `DeleteEndedRuns(ctx, before time.Time) (int, error)` on both
  stores, with the batch loop in hostcore.
- R-6: A schema migration is added only where a query needs an index
  (see Decisions). Migrations append to each backend's `migrations`.
- R-7: An `Example` test shows listing and retention. hosts.md gets a
  "Listing and deleting runs" section that says the library runs no
  retention loop and the application calls `DeleteEndedRuns` on its own
  schedule. llms.txt and README document both methods, and the "Not yet"
  line and its llms.txt words go.

## Not in scope

- Listing on the local host. Its runs live in the application's memory.
- A count API, full-text search, arbitrary SQL filters, sorting options.
  The brief rules them out; the three filters cover the use cases.
- A filter on the workflow name. No use case needs it now, and it is a
  new field later without breaking callers.
- `ListRunsTx` or `DeleteEndedRunsSQLTx`. Reads and retention need no
  caller transaction.
- A retention option or loop in the Store. The application owns the
  schedule.
- Deleting definitions. They are content-addressed, shared by runs and
  small. A definition with no runs left stays; that is harmless.
- Deleting child runs with their parent. A child is a separate run with
  its own status and its own retention.

## Decisions

- **Names: `ListRuns` and `DeleteEndedRuns`.** They sit next to `GetRun`
  and say what they touch. "Ended" is the plain word for terminal, and hosts.md
  already uses it ("the run ended"). Rejected: `Runs` (reads like a field, and `Steps(runID)`
  already means "the records of one run"), `SearchRuns` (promises text
  search), `PruneRuns` and `PurgeRuns` (do not say which runs), `Retain`
  (says the opposite of what it does), `DeleteRuns` (suggests it can
  delete any run).
- **Order is newest `created_at` first, then ID; the cursor encodes
  both.** `created_at` and ID never change, so a page boundary never
  moves. Rejected: order by `updated_at` (a run that progresses jumps
  between pages, which breaks UC-3), offset paging (repeats or skips when
  runs start).
- **Time filters: `CreatedAfter`, `CreatedBefore`, `UpdatedAfter`.**
  `UpdatedAfter` answers "failed in the last hour" (for an ended run,
  `updated_at` is its end time) and "what changed since my last poll".
  The created pair selects a window. Rejected: a `FinishedAfter` field
  (one more name for what `UpdatedAfter` already gives on ended runs),
  `UpdatedBefore` (no use case).
- **Metadata filter values are strings only.** Every key in the filter
  must be present at the top level of the run's metadata with an equal
  string value. Any other filter value is an error that names the key.
  Reason: the smallest rule, the same on both backends, and it covers
  "customer 42". Postgres serves it with its GIN index (`@>`). The field
  stays `map[string]any`, like `NewRun.Metadata`, so numbers and bools
  can be allowed later without a breaking change. Rejected: all scalars
  (on sqlite, `json_extract` gives 1 for `true`, so a bool filter would
  match a number), all scalars with a JSON type check (more SQL and more
  tests for a need nobody has shown), full JSON containment (sqlite
  cannot match it without a lot of SQL), and `map[string]string` (a later
  widening would break callers).
- **A summary type, not `Run` with a nil `State`.** Reason: `Run.State` is
  non-nil today, and callers dereference it; a nil there would panic far
  from the cause. The summary carries `Error`, because a failed-runs page
  needs the message, but not `Outputs` or `State`, which can be large.
- **One index migration per backend: `runs (created_at, id)`.** Reason:
  without it every page sorts the whole matching set. Postgres's existing
  GIN index serves metadata, and its `(status, updated_at)` index serves
  `DeleteEndedRuns`. Sqlite gets the same `created_at` index; it has no
  metadata index, and a scan is acceptable at sqlite's scale. The tech
  spec confirms each query plan.
- **Retention deletes a run when its status is terminal and both
  `updated_at` and `finished_at` are before the cutoff, in batches of 500
  runs.** For an ended run the two times are equal, so the second check
  costs nothing, lets the existing `(status, updated_at)` index serve the
  query, and keeps the result correct if an ended run is ever written
  again: such a write can only make the delete remove fewer runs. The
  batch size is a constant, documented in hosts.md so that operators can
  estimate the lock time. Reason: no config knob without a user who needs
  it. Each batch is its own transaction, so the call can run against a
  live database.
- **A deleted run frees its ID.** Start's duplicate check covers only the
  runs that exist. We document this and do not keep a tombstone table.
  Reason: a tombstone table is new storage that grows forever, which is
  the problem retention solves. An application that needs a longer
  duplicate window keeps runs longer.
- **The delete includes jobs.** The store already drops a run's jobs when
  the run ends, so an ended run holds none today. The delete covers jobs
  anyway, so that it never leaves rows behind on sqlite if that changes.
  A worker that settles a deleted job finds `ErrJobGone`, which it already
  handles.
- **No new sentinel error.** A bad cursor or a bad metadata value returns
  a plain error with the store's prefix. Reason: a caller has nothing to
  do but fix the input. Add a sentinel when someone needs to branch on it.
- **Paused runs need nothing.** If the pause-resume track adds
  `StatusPaused`, a status filter on `paused` works, and `DeleteEndedRuns`
  skips paused runs because they are not terminal.

## Success

- An admin page lists, filters and pages runs with no SQL against our
  tables, on both backends.
- A cron job keeps the tables bounded, and no running run is ever deleted.
- What must not get worse: claim and settle speed. The new index adds one
  write per run start, not per step; the tech spec checks this.

## Risks and open questions

- **Delete under load.** A batch takes the rows' locks. A run being
  delivered to at the same moment waits for the batch or sees
  `ErrRunNotFound`. Both are correct; the fixed batch size keeps the wait
  short. Not blocking.
- **Idempotent starts across the retention window.** An application that
  derives run IDs from webhooks and keeps 30 days of runs starts the work
  again for a webhook that comes back after 31 days. hosts.md says so;
  the application picks a retention period longer than its redelivery
  window. Not blocking.
