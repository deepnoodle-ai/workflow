# Spec: Worker & Postgres Store Improvements

_Status: Draft_
_Created: 2026-04-12_
_Source: NoodleSpy spike branch `spike/workflow-v002-worker-pgstore` (research doc 025)_

---

## Goal

Make `experimental/worker` and `experimental/store/postgres` carry more of the
production workload so consumers can ship a workflow system with minimal glue
code. Concretely: eliminate the ~420-line adapter layer NoodleSpy needed to
bridge the library to its existing repos, and let any consumer drop their
parallel "workflows" table in favor of `workflow_runs` as the system of record.

The library is still a pure execution engine at its core. The experimental
submodules are where we own persistence, leasing, and reconciliation —
this spec is about making that ownership feel complete.

## Design Principles

1. **Experimental submodules can break.** `experimental/worker` and
   `experimental/store/postgres` are pre-1.0 by design. Now is the time to
   churn the interfaces; tagging happens after the dust settles.
2. **Don't bake consumer-specific domain into the library.** Org and project
   are the universal B2B partition pattern and earn first-class fields.
   Everything else goes in `Metadata`.
3. **Provide an escape hatch.** `Store.Pool()` lets consumers run any query
   the high-level API doesn't cover. The bar for adding a new high-level
   method is "many consumers will want this," not "one consumer needs it."
4. **Workers should be self-sufficient.** A Handler should receive everything
   it needs to run a workflow as inputs — no DB round-trips to fetch
   metadata, no manually constructed checkpointers, no lease wiring.
5. **Nullable means nullable.** Single-tenant deployments shouldn't invent
   sentinel org IDs. Empty string in the Go API ↔ NULL in the database.

---

## Phase 1: Quick Wins

These are independent, small, and unblock the rest. Each is a separate PR.

### 1.1 `WorkerID` on `Claim`

**File:** `experimental/worker/queue_store.go`

Add `WorkerID string` to `Claim`. The worker already has `w.cfg.WorkerID` in
scope when it constructs the claim in `claimBatch`; populate it there.

```go
type Claim struct {
    // ... existing fields ...
    WorkerID string // the worker that holds the lease
}
```

The postgres `ClaimQueued` already records `claimed_by` in the row; just
read it back into the returned `Claim`.

**Impact.** Handlers no longer need to round-trip the DB to fence their own
writes. Removes ~10 lines from a typical Handler.

---

### 1.2 Pass `*Claim` to `Complete` and `Heartbeat`

**File:** `experimental/worker/queue_store.go`, `worker.go`

Replace the `Lease`-only signatures with `*Claim`. The worker has the claim
in scope at every call site already.

```go
type QueueStore interface {
    // ...
    Heartbeat(ctx context.Context, claim *Claim) error
    Complete(ctx context.Context, claim *Claim, outcome Outcome) error
    // ...
}
```

Fencing is still on `(WorkerID, Attempt)` — the new signature is purely about
giving the store access to `OrgID`, `ProjectID`, `WorkflowType`, and
`CreditCost` without an out-of-band cache.

`Lease` as a type can either go away entirely or stay as an internal helper.
Recommend deleting it; it adds nothing once `*Claim` is the unit of currency.

**Impact.** Eliminates the `sync.Map` `runID → orgID` cache pattern in
consumer adapters. Future additions to `Claim` automatically reach all
fencing call sites without interface churn.

---

### 1.3 `WebhookDelivererFunc` adapter

**File:** `experimental/worker/webhooks.go`

```go
type WebhookDelivererFunc func(ctx context.Context, url string, payload []byte) error

func (f WebhookDelivererFunc) Deliver(ctx context.Context, url string, payload []byte) error {
    return f(ctx, url, payload)
}
```

Five lines. Matches the `HandlerFunc` pattern already in the package.

---

### 1.4 Worker-side ID generation for outbox values

**File:** `experimental/worker/worker.go`, `subsystems.go`

Add an `IDGenerator func() string` field to `Config`. Default to
`uuid.NewString()` (the worker package can take the UUID dep — it's already
implicit through stdlib `crypto/rand` if we want to avoid the dependency,
but `github.com/google/uuid` is fine for the experimental submodule).

Populate `Trigger.ID` and `WebhookDelivery.ID` in `afterComplete` before
calling `InsertTriggers` / `EnqueueWebhook`.

The postgres store should honor a non-empty ID and only generate one when
the field is blank — this keeps existing behavior working and lets
consumers with custom ID schemes plug them in.

**Impact.** Consumer-side store adapters lose their `idGen` constructor
parameter. `InsertTriggers` and `EnqueueWebhook` become pure pass-throughs.

---

### 1.5 `Store.Pool()` escape hatch

**File:** `experimental/store/postgres/store.go`

```go
// Pool returns the underlying pgxpool.Pool for queries the high-level API
// does not cover. Consumers are responsible for not breaking the store's
// invariants (lease fencing, status transitions, etc.).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }
```

Document the invariants the store maintains so consumers know what they
can and can't do via raw queries.

**Impact.** Consumers can do dedup checks, custom analytics queries,
joined reads, and one-off migrations without forking the library or
adding methods that won't generalize.

---

### 1.6 Configurable table prefix

**File:** `experimental/store/postgres/store.go`, `schema.sql`

```go
postgres.New(pool,
    postgres.WithLogger(logger),
    postgres.WithTablePrefix("dn_"),
)
```

Default prefix: empty string, matching today's `workflow_runs` /
`workflow_events` / etc. naming. Consumers with collisions pick a prefix.

`schema.sql` becomes a Go template (or generated string) so prefix
substitution happens at migration time.

All `Store` queries reference table names through a helper that prepends
the prefix — there's no clean way around touching every query, but it's
mechanical.

**Impact.** Consumers migrating from existing schemas can pick a non-
colliding prefix. NoodleSpy's `workflow_events`/`workflow_triggers`/
`workflow_webhooks` collision goes away.

---

## Phase 2: Correctness Fix

### 2.1 `DeadLetterStale` triggers credit refunds inline

**Files:** `experimental/worker/queue_store.go`, `worker.go`

Change `DeadLetterStale` to return `[]DeadLetteredRun` with the metadata
needed for refund:

```go
type DeadLetteredRun struct {
    ID           string
    OrgID        string
    WorkflowType string
    CreditCost   int
}

DeadLetterStale(
    ctx context.Context,
    staleBefore time.Time,
    maxAttempts int,
    excludeIDs []string,
) ([]DeadLetteredRun, error)
```

In `reapOnce`, call `CreditStore.Refund` for each dead-lettered run before
emitting the dead-letter event.

**Impact.** Refunds happen immediately on dead-letter instead of waiting
up to 5 minutes for the reconcile loop. Closes a user-visible latency gap
and a class of "where did my credit go" bugs.

---

## Phase 3: Reconcile Cleanup

### 3.1 Move `ListFailedWithCredits` to `QueueStore`

**Files:** `experimental/worker/queue_store.go`, `credits.go`, `worker.go`

Remove `CreditStore.ListUnrefunded`. `CreditStore` becomes pure ledger
operations: `Debit`, `Refund`, `HasRefund`, `Balance`.

Add to `QueueStore`:

```go
type FailedRun struct {
    ID           string
    OrgID        string
    WorkflowType string
    CreditCost   int
}

ListFailedWithCredits(ctx context.Context, limit int) ([]FailedRun, error)
```

Update `reconcileLoop` to query `QueueStore.ListFailedWithCredits`, then
check `CreditStore.HasRefund` per row, then `Refund` if missing.

**Impact.** Cross-concern query goes away. Consumer credit adapters lose
their dependency on workflow repos. The reconcile loop owns the cross-
table logic where it belongs.

---

## Phase 4: Org & Project — Nullable Partitions and Worker Scoping

### 4.1 Nullable `OrgID`, new nullable `ProjectID`

**Files:** `experimental/worker/queue_store.go`,
`experimental/store/postgres/schema.sql`, `queue.go`

Add `ProjectID string` to `NewRun` and `Claim`. Both `OrgID` and `ProjectID`
are nullable in the database; the Go API uses empty string as the NULL
sentinel (no `*string`, no `sql.NullString` in the public surface).

```go
type NewRun struct {
    ID           string
    Spec         []byte
    OrgID        string // empty = NULL
    ProjectID    string // empty = NULL
    ParentRunID  string // empty = NULL
    WorkflowType string
    InitiatedBy  string
    CreditCost   int
    CallbackURL  string
    Metadata     map[string]string // arbitrary, JSONB-backed
}

type Claim struct {
    ID           string
    Spec         []byte
    Attempt      int
    WorkerID     string
    OrgID        string
    ProjectID    string
    ParentRunID  string
    WorkflowType string
    InitiatedBy  string
    CreditCost   int
    CallbackURL  string
    CreatedAt    time.Time
    Metadata     map[string]string
}
```

Naming rationale for `ProjectID`: the org→project pattern is the dominant
B2B partition shape (GCP projects, Linear teams, Notion workspaces, GitHub
repos-within-org). The library does not enforce or interpret it — purely
stores, indexes, and filters. Consumers whose product calls it something
else (workspace, team, board) store their internal ID in the column and
rename it in their UI layer.

`ParentRunID` earns a real column because child workflows are already a
library concept (the trigger outbox).

`Metadata` is the catch-all for anything else: tenant-specific tags,
correlation IDs, feature flags, etc. JSONB column with a GIN index for
containment queries.

DDL:

```sql
ALTER TABLE workflow_runs
    ALTER COLUMN org_id DROP NOT NULL,
    ALTER COLUMN org_id DROP DEFAULT;

ALTER TABLE workflow_runs
    ADD COLUMN IF NOT EXISTS project_id    TEXT,
    ADD COLUMN IF NOT EXISTS parent_run_id TEXT,
    ADD COLUMN IF NOT EXISTS metadata      JSONB;

CREATE INDEX IF NOT EXISTS workflow_runs_org_project_created
    ON workflow_runs (org_id, project_id, created_at DESC)
    WHERE project_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS workflow_runs_parent_created
    ON workflow_runs (parent_run_id, created_at DESC)
    WHERE parent_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS workflow_runs_metadata_gin
    ON workflow_runs USING GIN (metadata);
```

The Go layer converts `""` ↔ `NULL` at the storage boundary. Consumers
never see `*string`.

---

### 4.2 Worker scoping via `ClaimFilter`

**Files:** `experimental/worker/queue_store.go`, `worker.go`,
`experimental/store/postgres/queue.go`

Workers can restrict which queued runs they claim. Operational use cases:
dedicated tenant pools, project-level resource isolation, geographic or
compliance affinity, priority lanes.

```go
type ClaimFilter struct {
    OrgID     string // empty = any (including NULL)
    ProjectID string // empty = any (including NULL)
}

type QueueStore interface {
    ClaimQueued(ctx context.Context, workerID string, filter ClaimFilter) (*Claim, error)
    // ...
}

type Config struct {
    // ...
    // ClaimFilter optionally restricts which queued runs this worker
    // will claim. Zero value matches any run. Multiple workers with
    // different filters can share a queue for tenant or project isolation.
    ClaimFilter ClaimFilter
}
```

Postgres implementation:

```sql
SELECT ... FROM workflow_runs
WHERE status = 'queued'
  AND ($1 = '' OR org_id     = $1)
  AND ($2 = '' OR project_id = $2)
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT 1
```

Add `(org_id, project_id, status, created_at)` index to keep filtered claim
latency flat under load.

**Semantic note.** Empty filter means "any value, including NULL." This is
the right default for the common single-pool case. There is intentionally
no way through the high-level filter to say "only NULL orgs" or "only
non-NULL orgs" — consumers with that need go through `Pool()`.

**Future extension (do not build now).** A slice of filters would let one
worker claim from multiple orgs but not all. Defer until a real consumer
asks; running multiple worker instances with single filters covers the
gap in the meantime.

---

## Phase 5: HandlerContext — Pre-fenced Stores

### 5.1 Replace `Handler.Handle(ctx, *Claim)` with `Handle(ctx, *HandlerContext)`

**Files:** `experimental/worker/handler.go`, `worker.go`

The biggest single ergonomic improvement in the spec. Today every consumer
constructs a `DBCheckpointer`, wires up `StepProgressCallbacks`, and passes
them into `workflow.Execute()` themselves. The worker already has all the
inputs — it should hand the Handler a fully-configured context.

```go
type HandlerContext struct {
    Claim          *Claim
    Checkpointer   workflow.Checkpointer       // pre-fenced on this lease
    ProgressStore  workflow.StepProgressStore  // pre-fenced on this lease
    ActivityLogger workflow.ActivityLogger     // optional
    SignalStore    workflow.SignalStore        // optional
}

type Handler interface {
    Handle(ctx context.Context, hc *HandlerContext) Outcome
}

type HandlerFunc func(ctx context.Context, hc *HandlerContext) Outcome
func (f HandlerFunc) Handle(ctx context.Context, hc *HandlerContext) Outcome {
    return f(ctx, hc)
}
```

The postgres store gains constructors that produce pre-fenced wrappers:

```go
func (s *Store) NewCheckpointer(claim *Claim) workflow.Checkpointer
func (s *Store) NewStepProgressStore(claim *Claim) workflow.StepProgressStore
func (s *Store) NewActivityLogger(claim *Claim) workflow.ActivityLogger
```

Each wrapper carries the `(WorkerID, Attempt)` from the claim and fences
every write. Lease loss surfaces as `ErrLeaseLost` from the underlying
store call.

Worker wiring (in `execute`):

```go
hc := &HandlerContext{
    Claim:          claim,
    Checkpointer:   w.cfg.Store.NewCheckpointer(claim),
    ProgressStore:  w.cfg.Store.NewStepProgressStore(claim),
    ActivityLogger: w.cfg.Store.NewActivityLogger(claim),
}
outcome := w.safeHandle(runCtx, hc)
```

The Handler becomes a pure "given a workflow def + inputs, run them"
function. No persistence wiring in consumer code.

For consumers using a non-postgres store, they implement the same
factory methods on their own store type.

**Impact.** Removes ~250 lines of `DBCheckpointer` / `StepProgressCallbacks`
boilerplate from a typical consumer. This is the change that makes the
worker package feel like a framework instead of a parts kit.

---

## Phase 6: Read-Side Run API

### 6.1 `Run` type

**File:** `experimental/store/postgres/runs.go` (new)

```go
type Run struct {
    ID           string
    OrgID        string
    ProjectID    string
    ParentRunID  string
    WorkflowType string
    Status       worker.Status
    CreditCost   int
    InitiatedBy  string
    CallbackURL  string
    Spec         []byte
    Result       []byte
    ErrorMessage string
    CurrentStep  string
    Metadata     map[string]string
    Attempt      int
    ClaimedBy    string
    HeartbeatAt  *time.Time
    CreatedAt    time.Time
    StartedAt    *time.Time
    CompletedAt  *time.Time
}
```

**Deliberate omissions vs research doc 025:**

- No `StepProgress []byte` denormalized blob. The existing
  `workflow_step_progress` table is the single source of truth. Consumers
  who want a per-run summary join against it or use `Pool()`.

### 6.2 `RunFilter` and `RunCursor`

```go
type RunFilter struct {
    WorkflowType string            // empty = all types
    Status       worker.Status     // empty = all statuses
    ProjectID    string            // empty = all projects (incl NULL)
    ParentRunID  string            // empty = all parents
    InitiatedBy  string            // empty = all initiators
    Metadata     map[string]string // JSONB containment match
    Cursor       *RunCursor        // nil = start from newest
    Limit        int               // 0 = default (50)
}

type RunCursor struct {
    CreatedAt time.Time
    ID        string
}
```

Keyset pagination on `(created_at DESC, id DESC)`. Cursor is opaque to
consumers; recommend they base64-encode it for API transport.

### 6.3 Read methods on `Store`

```go
var ErrRunNotFound = errors.New("postgres: run not found")

func (s *Store) GetRun(ctx context.Context, orgID, id string) (*Run, error)
func (s *Store) ListRuns(ctx context.Context, orgID string, filter RunFilter) ([]*Run, *RunCursor, error)
func (s *Store) CountRuns(ctx context.Context, orgID string, filter RunFilter) (int, error)
func (s *Store) DeleteRun(ctx context.Context, orgID, id string) error
```

`orgID == ""` is a valid scope (single-tenant); the store passes it through
to the WHERE clause as `org_id IS NULL`.

`DeleteRun` fails if the run is `StatusRunning`. Caller must cancel/complete
it first. (We may want a `CancelRun` method too — flag for follow-up.)

**Scope discipline.** This API exists to support operational dashboards and
run management. Document explicitly that complex analytics queries (date
ranges, aggregations, full-text search on errors) should use `Pool()`
directly. The bar for adding a new high-level read method is "many
consumers will want this," not "one consumer needs it."

---

## Phase 7: Tag and Publish

### 7.1 Published submodule tags

After Phases 1–6 land and stabilize:

```
git tag experimental/worker/v0.1.0
git tag experimental/store/postgres/v0.1.0
git push --tags
```

Consumers can drop their `replace` directives and pin versions normally.

The `v0.1.0` (not `v1.0.0`) signal stays — we've just done a round of
breaking changes and shouldn't pretend the API is stable yet.

---

## Out of Scope

These came up in discussion and are deliberately not in this spec:

- **Multi-filter worker scoping.** A single `ClaimFilter` per worker
  handles the 90% case. Slice-of-filters waits for a real ask.
- **Library-side enforcement of org/project scoping.** The library stores
  and indexes them but does not enforce cross-tenant isolation, project-
  level rate limits, or project-level credit accounting. Consumer concern.
- **Webhook SSRF protection and HMAC signing.** Genuinely consumer-specific.
  `WebhookDelivererFunc` (1.3) is the integration point.
- **Dedup queries like `ExistsActiveForSpec`.** Consumer-specific JSONB
  predicates. `Pool()` (1.5) is the integration point.
- **`CancelRun` method.** Probably wanted, but design needs its own pass —
  graceful vs. forced, in-flight vs. queued, lease semantics.
- **Replacing the bundled SQLite store.** Same interfaces apply; bring it
  in line in a follow-up.

---

## Sequencing

Phases are roughly ordered by dependency and risk:

1. **Phase 1** (quick wins) — independent PRs, ship in any order. Low risk.
2. **Phase 2** (dead-letter refunds) — depends on no other phase. Closes a
   correctness gap; do it early.
3. **Phase 3** (reconcile cleanup) — independent. Cleanup after Phase 2.
4. **Phase 4** (org/project + ClaimFilter) — schema migration; coordinate
   with anyone running the postgres store in production.
5. **Phase 5** (HandlerContext) — biggest UX win, breaks `Handler` interface.
   Land after Phase 4 so the new `Claim` shape is in place.
6. **Phase 6** (read-side API) — depends on Phase 4 (Run shape mirrors
   Claim). Ship after Phase 5 stabilizes.
7. **Phase 7** (tag) — only after the above settles.

Estimated total: ~400 lines added to the library, ~2,000 lines deleted from
a typical consumer. Net win for everyone.

---

## Open Questions

1. **UUID dependency.** Phase 1.4 wants a default ID generator. Take
   `github.com/google/uuid` as a dep on `experimental/worker`, or roll our
   own with `crypto/rand`? Lean: take the dep, it's experimental.
2. **`Lease` type fate.** Phase 1.2 makes `*Claim` the unit of fencing.
   Delete `Lease` entirely, or keep it as an internal helper? Lean: delete.
3. **Metadata column type.** `JSONB map[string]string` or `JSONB []byte`
   (consumer-controlled marshaling)? Lean: `map[string]string` — covers
   the common case, escape hatch via `Pool()` for richer shapes.
4. **`workflow.SignalStore` on `HandlerContext`.** Worth pre-wiring, or
   leave to consumers? Lean: pre-wire; signals are a library concept.
5. **Read API location.** Methods on `*postgres.Store` directly, or a
   `RunReader` interface that the store implements? Lean: methods on
   `Store`, add an interface later if a second backend appears.
