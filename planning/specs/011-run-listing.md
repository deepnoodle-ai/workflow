# Listing runs and deleting ended runs in the durable-host core

**Status:** Draft
**Author:** Curtis Myzie
**Date:** 2026-10-29
**Workflow:** Spec, then build in parallel. The design follows the PRD
closely; the queries and the migration need a second look.
**PRD:** [011-run-listing.md](../prds/011-run-listing.md)
**Builds on:** [007-sqlite-backend.md](007-sqlite-backend.md) (the
`hostcore` storage contract).

## Context

PRD 011 adds `ListRuns` and `DeleteEndedRuns` to `postgres.Store` and
`sqlite.Store`. Both backends share their policy in `internal/hostcore`
and implement a storage contract (`Backend`). This spec fixes the split
between the core and the backends, the two queries on each backend, the
cursor, the one migration, and the tests. The PRD holds the behaviour and
the reasons for the API; this spec does not repeat them.

## Goals

- One copy of the policy (input checks, limit, cursor, batch loop) in
  hostcore. Each backend adds two `Backend` methods and nothing else.
- A first page with no filter reads at most `Limit+1` index entries on
  both backends (checked with `EXPLAIN` in the PR, not in a test).
- `DeleteEndedRuns` holds no transaction for more than one batch of 500
  runs.
- Every PRD acceptance criterion is a suite test in `internal/hosttest`
  that both backends pass.

## Non-goals

- Any change to the root package or to Apply.
- A query planner of our own. Each filter maps to one SQL predicate.
- Tuning for a rare status on a very large table (see Tradeoffs).

## Proposal

### Types (package `durable`, re-exported by both backends)

```go
// RunFilter selects runs for ListRuns. A zero field selects every run.
type RunFilter struct {
    Status        []workflow.RunStatus // any of these
    Metadata      map[string]any       // each key equals this string value
    CreatedAfter  time.Time            // created_at > CreatedAfter
    CreatedBefore time.Time            // created_at < CreatedBefore
    UpdatedAfter  time.Time            // updated_at > UpdatedAfter
    Limit         int                  // 0: 100; above 1000: 1000
    Cursor        string               // RunPage.Next of the previous page
}

type RunPage struct {
    Runs []RunSummary // newest created_at first, then ID descending
    Next string       // "" when no run follows
}

type RunSummary struct {
    ID             string
    Workflow       string
    DefinitionHash string
    Status         workflow.RunStatus
    Metadata       map[string]any
    Error          *workflow.RunError // the run's State.Error
    CreatedAt      time.Time
    UpdatedAt      time.Time
    FinishedAt     *time.Time
}
```

`postgres/aliases.go` and `sqlite/aliases.go` add `type RunFilter =
durable.RunFilter` and the other two, as they do for `Run`.

### The core (`internal/hostcore/list.go`, new)

Two `Backend` methods:

```go
// ListRuns returns at most q.Limit runs that match q, after q.After when
// it is set, newest created_at first, then ID descending.
ListRuns(ctx context.Context, q RunQuery) ([]durable.RunSummary, error)
// DeleteEndedRuns deletes, in one transaction, at most limit runs whose
// status is terminal and whose updated_at and finished_at are before
// before, with their steps, events and jobs. It returns how many.
DeleteEndedRuns(ctx context.Context, before time.Time, limit int) (int, error)
```

`RunQuery` is the checked filter: `Status []string`, `Metadata
map[string]string`, the three times, `Limit int`, and `After *Position`
(`CreatedAt time.Time`, `ID string`). A backend never sees a raw cursor
or a non-string metadata value.

Two functions hold the policy, and each store method calls one:

```go
func ListRuns(ctx context.Context, b Backend, prefix string, f durable.RunFilter) (durable.RunPage, error)
func DeleteEndedRuns(ctx context.Context, b Backend, before time.Time) (int, error)
```

`ListRuns`:

1. Checks the filter. A negative `Limit` is an error. A non-string
   metadata value is an error that names the key:
   `postgres: list runs: metadata "tier": the value must be a string, not float64`.
   Map keys are sorted before the check so the error is deterministic.
2. Sets the limit: 0 becomes 100, above 1000 becomes 1000.
3. Decodes the cursor. A bad cursor is
   `postgres: list runs: invalid cursor`.
4. Asks the backend for `limit+1` rows. If it gets `limit+1`, it drops the
   last and encodes the cursor from the last row it keeps.

`DeleteEndedRuns` loops: it calls the backend with `deleteBatch = 500`,
adds the count, and stops when a batch returns fewer than 500 runs. It
checks `ctx.Err()` before each batch and returns the count so far with
the error.

**The cursor** is the last row's `created_at` and ID: base64url (no
padding) of the JSON `{"t":"<RFC 3339, microseconds, UTC>","id":"<id>"}`.
The time is the value read from the database, which both backends hold
to the microsecond, so it compares equal to the row on the next read. The
cursor encodes position only, not the filter. A cursor used with another
filter starts after the same position, which is well defined.

### Postgres

Migration 2, appended to `migrations` in `postgres/store.go`:

```sql
CREATE INDEX IF NOT EXISTS runs_created ON {{schema}}.runs (created_at, id);
```

`ListRuns` builds one statement. Each set field adds one predicate:

```sql
SELECT id, workflow, definition_hash, status, metadata, error,
       created_at, updated_at, finished_at
FROM {{schema}}.runs
WHERE status = ANY($1)                -- Status
  AND metadata @> $2                  -- Metadata: {"key": "value", ...}
  AND created_at > $3 AND created_at < $4
  AND updated_at > $5
  AND (created_at, id) < ($6, $7)     -- After
ORDER BY created_at DESC, id DESC
LIMIT $8
```

With no filter, or a status filter, the planner scans `runs_created`
backward and stops at the limit. With a metadata filter it uses the GIN
index `runs_metadata`, then a top-N sort of the matches.

`DeleteEndedRuns` deletes in one statement; `ON DELETE CASCADE` removes
the steps, events and jobs:

```sql
DELETE FROM {{schema}}.runs WHERE id IN (
    SELECT id FROM {{schema}}.runs
    WHERE status IN ('completed', 'failed', 'cancelled')
      AND updated_at < $1 AND finished_at < $1
    ORDER BY updated_at LIMIT $2
    FOR UPDATE SKIP LOCKED)
```

`runs_status_updated` serves the inner query. `SKIP LOCKED` passes over a
run that a `Deliver` holds at that moment; the next call deletes it. The
cascades use the primary keys of `steps` and `events` and the unique key
of `jobs`, which all start with `run_id`.

### SQLite

Migration 2, appended to `migrations` in `sqlite/sqlite.go`:

```sql
CREATE INDEX IF NOT EXISTS {{p}}runs_created ON {{p}}runs (created_at, id);
```

`ListRuns` is the postgres statement with three changes. Times are the
fixed-width text that `fmtTime` writes, so text order is time order.
Status is `status IN (?, ?, ...)`. Each metadata key adds:

```sql
EXISTS (SELECT 1 FROM json_each(metadata)
        WHERE key = ? AND type = 'text' AND value = ?)
```

`json_each` takes the key as a value, so no JSON path is built and a key
with a quote or a dot needs no escaping. `type = 'text'` keeps a number or
a bool from matching. Row values (`(created_at, id) < (?, ?)`) are in
SQLite since 3.15; both drivers bundle a newer one with JSON built in.

`DeleteEndedRuns` runs in one `Write` transaction. SQLite does not
enforce foreign keys here (schema.sql), so it deletes each table itself,
children first, with the same selection each time:

```sql
-- sel: SELECT id FROM {{p}}runs WHERE status IN ('completed','failed','cancelled')
--      AND updated_at < ?1 AND finished_at < ?1 ORDER BY updated_at, id LIMIT ?2
DELETE FROM {{p}}events WHERE run_id IN (sel);
DELETE FROM {{p}}steps  WHERE run_id IN (sel);
DELETE FROM {{p}}jobs   WHERE run_id IN (sel);
DELETE FROM {{p}}runs   WHERE id     IN (sel);  -- its count is the result
```

SQLite has one writer, and the transaction holds it, so `sel` returns the
same rows in all four statements. `ORDER BY updated_at, id` makes the
selection total.

### Store methods

Each store gets two methods next to `GetRun`, with the docs the PRD asks
for (order by start time; `UpdatedAfter` selects and does not sort;
retention frees the IDs; the batch size):

```go
func (s *Store) ListRuns(ctx context.Context, f RunFilter) (RunPage, error)
func (s *Store) DeleteEndedRuns(ctx context.Context, before time.Time) (int, error)
```

`internal/parity`'s `store` interface gets both lines.

### Tests (`internal/hosttest`, added to `list.go`)

- `ListRunsFiltersByStatusAndTime` (UC-1): failed, completed and running
  runs; status sets; the three time bounds, using `CreatedAt` and
  `UpdatedAt` read back with `GetRun`; `Error` is set on a failed run.
- `ListRunsFiltersByMetadata` (UC-2): `"42"` matches; `42`, `true` and a
  missing key do not; two keys; a non-string value fails and names the
  key.
- `ListRunsPagesWithoutRepeats` (UC-3): 300 runs, pages of 7, a new run
  started and an old one cancelled between pages; each of the 300 appears
  once. `Limit` 0 and 5000; a negative limit; a bad cursor.
- `DeleteEndedRunsKeepsLiveRuns` (UC-4): an old running run, a run in
  `failing` (on_failure step held by a deferred handler), and an ended run
  after the cutoff all stay.
- `DeleteEndedRunsRemovesRecords` (UC-4): more than 500 ended runs, so the
  loop runs twice; `GetRun` returns `ErrRunNotFound`, `Steps` and
  `Events` are empty; a timer job left by an ended run is gone; a second
  call returns 0; the definition is still stored.
- `DeleteEndedRunsFreesTheID` (UC-4): Start, finish, delete, Start with
  the same ID gives `Applied` true and a new `CreatedAt`.

The cutoff comes from times read back from the store, not from a fake
clock: finish run A, read its `FinishedAt`, finish run B later, delete
before a time between them. No harness change is needed.

An `Example` in `sqlite/example_test.go` lists failed runs and deletes
ended ones, on an in-memory database, so CI runs it without Postgres.

## Alternatives considered

- **One generic query method on `Backend` taking SQL fragments.** The
  core would build the WHERE clause once. Rejected: the two dialects
  differ in every predicate (`ANY` against `IN`, `@>` against
  `json_each`, time types), so the core would hold dialect code, which
  the 007 contract keeps in the backends.
- **Deleting in one statement per call, with no batches.** Simplest.
  Rejected: a first run against a year of history holds one transaction
  over millions of rows, blocks SQLite's only writer for its length, and
  bloats Postgres's WAL in one burst.
- **A `(status, created_at)` index instead of `(created_at, id)`.** It
  serves a status filter better but not the unfiltered first page, and a
  page needs the ID in the key for the tie-break. One index for the
  common page is enough until someone shows a slow status query.
- **The cursor as the run ID only, looked up on the next call.** Shorter.
  Rejected: if that run is deleted between pages, the position is lost.

## Tradeoffs and consequences

- **The index build locks the runs table.** `Migrate` runs in a
  transaction, so Postgres cannot build the index `CONCURRENTLY`. On a
  large `runs` table, Start and every Deliver wait for the build. Because
  the migration says `IF NOT EXISTS`, an operator with a large table can
  run `CREATE INDEX CONCURRENTLY runs_created ON <schema>.runs (created_at,
  id)` before the upgrade, and the migration then does nothing. hosts.md
  says so.
- **One more index entry per run row version.** Every update of a run
  already changes `status` or `updated_at`, which are indexed, so Postgres
  writes new index entries for it today; the new index adds one more per
  update. Jobs and steps, which claim and settle write most, are not
  touched.
- **A rare status on a huge table is slow.** `status = 'failed'` with no
  other filter scans `runs_created` backward until it finds enough failed
  runs. With 1% failures and a page of 50, that is about 5000 index
  entries: fine. With 0.001% it is a long scan. A `UpdatedAfter` bound
  does not help this plan. We accept it until a user reports it; the fix
  is a `(status, created_at, id)` index in a later migration.
- **SQLite metadata filters scan.** There is no JSON index on SQLite. A
  metadata filter reads the runs in `created_at` order and checks each
  one. Acceptable at the scale SQLite runs at.
- **`SKIP LOCKED` can leave a run for the next call.** The count is then
  lower than the number of runs that matched. The doc says the count is
  the number deleted, not the number that matched.

## Rollout

Additive: two methods, three types, one migration per backend, applied
by `Migrate` as usual. The README "Not yet" line and the llms.txt words
go in the implementation PR, with the hosts.md section.

## Open questions

- **Does the planner pick `runs_created` with a status filter?** Leaning
  yes on Postgres 16 for common statuses. The implementer runs `EXPLAIN`
  on 100k runs in the shared container and puts the plan in the PR. If
  it sorts instead, the fix is the same `(status, created_at, id)` index,
  which would replace `runs_created` before release.
