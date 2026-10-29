# Start, deliver and cancel runs in a database/sql transaction

Status: Draft
PRD PR: #TBD  Implementation PR: #TBD  Updated: 2026-10-28
Issues: #52

## Problem

A Go developer keeps their application data in Postgres and reaches it
through `database/sql`: an ORM such as Ent, sqlc's `database/sql` output,
or pgx's `stdlib` driver. They want two writes to commit together:

- create an order and start the order's run;
- mark an approval as done and deliver the approval step's result.

`StartTx`, `DeliverTx` and `CancelTx` take a `pgx.Tx`. A `database/sql`
transaction cannot become a `pgx.Tx`, so these writes cannot be atomic
today. The workaround in #52 is an application outbox plus fixed run IDs:
write the record and an outbox row, then a relay calls `Start` or
`Deliver`, which are safe to repeat. That works, but the developer builds
and runs a relay, and each run starts one hop later. If we do nothing,
every `database/sql` user who wants the guarantee the package advertises
("start a run atomically with its own writes") builds that relay.

## Context

**Our code.** `postgres/store.go` has `StartTx`, `DeliverTx` and
`CancelTx`. Each takes the caller's `pgx.Tx` and passes it to internal
helpers (`lockRun`, `deliverLocked`, `persist`, `updateRun`, `finished`,
`definition`). These helpers use two methods only: `Exec` and `QueryRow`.
They use no `CopyFrom`, no batches, no `pgtype` values and no prepared
statements. One call reads `RowsAffected` from `Exec`. They compare errors
with `pgx.ErrNoRows`. Arguments are strings, integers, `[]byte` for jsonb,
`*time.Time`, and one `map[string]any` for jsonb metadata. Scans read into
`[]byte`, `string` and `int64`. The worker (`worker.go`) calls the same
helpers with its own `pgx.Tx`.

One place needs a `pgx.Tx` itself: the `WithOnFinished` hook has the type
`func(ctx, pgx.Tx, Finished) error`, and the store calls it in the
transaction that finishes a run. A cancel, or a delivery that ends a run,
finishes the run in the caller's transaction.

**pgx stdlib.** `stdlib.OpenDBFromPool(pool)` gives a `*sql.DB` on the
same `pgxpool.Pool` the store uses. The stdlib driver accepts every
argument type pgx accepts (`CheckNamedValue` returns nil), so arguments
encode the same on both paths. It returns jsonb columns as `[]byte`. There
is no way to get a `pgx.Tx` or `*pgx.Conn` from a `*sql.Tx`:
`database/sql` exposes the driver connection on `*sql.Conn.Raw` only.

**Ent.** The generated `*ent.Tx` does not expose its `*sql.Tx`. With the
`sql/execquery` codegen feature, `*ent.Tx` gets
`ExecContext(ctx, query, args...) (sql.Result, error)` and
`QueryContext(ctx, query, args...) (*sql.Rows, error)`, which run on the
transaction. It already has `Commit() error` and `Rollback() error`. It
has no `QueryRowContext`. Without the feature, an Ent user must begin a
`*sql.Tx` and build a second Ent client over it with
`entsql.Conn{ExecQuerier: tx}`.

**River** (riverqueue). The client is generic over its transaction type:
`river.NewClient(riverpgxv5.New(pool), cfg)` gives
`InsertTx(ctx, pgx.Tx, ...)`, and
`river.NewClient(riverdatabasesql.New(db), cfg)` gives
`InsertTx(ctx, *sql.Tx, ...)`. Each driver has its own generated query
code. The `database/sql` driver had bugs that the pgx driver did not have
(riverqueue/river#478, #630, #882). It has no LISTEN unless the caller
also gives it a pgx pool.

**gue** (vgarvardt/gue). Versions 1 to 5 had an `adapter` package: one
interface and wrappers for pgx v4, pgx v5, lib/pq and go-pg. Callers
wrapped their transaction before each call. Version 6 removed every
adapter and takes `*sql.Tx` directly.

**What these show.** A second full driver (River) is the "move to
`database/sql`" option, which the brief rules out. Caller-side adapters
(gue v5) add a wrap at every call, and gue removed them. The store's
transaction path uses a small subset of pgx, so a narrow internal
adapter is enough. No tech spec is needed: the change is an adapter at
the boundary, not a new mechanism.

## Users and job

Primary user: a Go developer whose application data goes through Ent on
Postgres with pgx's stdlib driver. We optimize their call site. The job:
"commit my record and the run change together, so that a rollback leaves
neither".

Secondary users: developers with a plain `*sql.Tx` (sqlc, hand-written
SQL). The same methods serve them with no extra step.

## Concepts

- **SQL transaction** (`postgres.SQLTx`): a `database/sql` transaction on
  the store's Postgres database. A `*sql.Tx` satisfies it, and so does an
  Ent `*Tx` generated with `sql/execquery`. It is the `database/sql`
  counterpart of the `pgx.Tx` that `StartTx` takes. Nothing else is new:
  `Result`, `NewRun` and the sentinels are the same on both paths.

## Use cases

### UC-1: Create an order and start its run (Ent)

```go
tx, err := client.Tx(ctx)                 // *ent.Tx, sql/execquery enabled
order, err := tx.Order.Create().SetSKU(sku).Save(ctx)
res, err := store.StartSQLTx(ctx, tx, postgres.NewRun{
    ID: fmt.Sprintf("order-%d", order.ID), Definition: def, Input: input,
})
err = tx.Commit()                          // or tx.Rollback()
```

Acceptance:
- [ ] After `Commit`, the order row and the run both exist. A worker picks
      up the run's first job. `NotifyChannel` fires after the commit, not
      before.
- [ ] After `Rollback`, no order row, no run, no event, no step record and
      no job exist, and no notification fires.
- [ ] A second `StartSQLTx` with the same ID and definition returns
      `Applied` false. With a different definition it returns
      `ErrRunExists`. These match `StartTx`.

### UC-2: Record an approval and deliver its result

```go
tx, err := db.BeginTx(ctx, nil)            // *sql.Tx on pgx stdlib
_, err = tx.ExecContext(ctx, `UPDATE approvals SET done = true WHERE id = $1`, id)
res, err := store.DeliverSQLTx(ctx, tx, runID, workflow.Event{
    Kind: workflow.EventStepCompleted, StepID: stepID, Output: out,
})
err = tx.Commit()
```

Acceptance:
- [ ] After `Commit`, the approval is done, the step record is completed,
      and the next step's job is in the outbox.
- [ ] After `Rollback`, the approval is not done, the step stays
      dispatched, the event log has no new entry, and no job was written.
- [ ] A duplicate delivery returns `Applied` false. An unknown run ID
      returns an error for which `errors.Is(err, postgres.ErrRunNotFound)`
      is true.

### UC-3: Delete a record and cancel its run

```go
tx, err := client.Tx(ctx)
err = tx.Order.DeleteOneID(id).Exec(ctx)
res, err := store.CancelSQLTx(ctx, tx, runID, "order deleted")
err = tx.Commit()
```

Acceptance:
- [ ] After `Commit`, the order is gone and the run is cancelled. Its
      queued jobs are gone. `RunsChannel` reports the change after commit.
- [ ] After `Rollback`, the order and the running run are both as before.
- [ ] A second cancel returns `Applied` false, as `CancelTx` does.

### UC-4: Record the outcome when a run finishes in a SQL transaction

A developer records outcomes with `WithOnFinished`. A cancel in UC-3
finishes the run in their SQL transaction, which has no `pgx.Tx`. They add
the `database/sql` form of the hook:

```go
store, err := postgres.New(pool, kinds,
    postgres.WithOnFinished(func(ctx context.Context, tx pgx.Tx, f postgres.Finished) error {
        _, err := tx.Exec(ctx, insertOutcome, f.RunID, f.Status)
        return err
    }),
    postgres.WithOnFinishedSQL(func(ctx context.Context, tx postgres.SQLTx, f postgres.Finished) error {
        _, err := tx.ExecContext(ctx, insertOutcome, f.RunID, f.Status)
        return err
    }))
```

Acceptance:
- [ ] A run that finishes in a SQL transaction calls the `WithOnFinishedSQL`
      hook once, inside that transaction. A rollback undoes the hook's writes.
- [ ] A run that finishes in a worker calls the `WithOnFinished` hook, as
      today.
- [ ] A store with `WithOnFinished` and without `WithOnFinishedSQL` returns
      an error from `StartSQLTx`, `DeliverSQLTx` and `CancelSQLTx` before it
      runs any statement. The message names `WithOnFinishedSQL`.
- [ ] `New` returns an error for `WithOnFinishedSQL` without
      `WithOnFinished`. The message says a worker finishes runs with a
      `pgx.Tx`.

## Requirements

- R-1: `Store.StartSQLTx(ctx, tx SQLTx, run NewRun) (*Result, error)`,
  `Store.DeliverSQLTx(ctx, tx SQLTx, runID string, ev workflow.Event)
  (*Result, error)` and `Store.CancelSQLTx(ctx, tx SQLTx, runID, reason
  string) (*Result, error)` behave as `StartTx`, `DeliverTx` and `CancelTx`
  do. They return the same `Result` and the same sentinel errors
  (`ErrRunNotFound`, `ErrRunExists`, `ErrUnstorable`,
  `ErrDefinitionUnavailable`, `workflow.ErrNotValidated`).
- R-2: `SQLTx` is this interface:

  ```go
  type SQLTx interface {
      ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
      QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
      Commit() error
      Rollback() error
  }
  ```

  The store never calls `Commit` or `Rollback`. Its doc says so, and says
  why they are there: a `*sql.DB` or an Ent `*Client` runs each statement
  in its own transaction, and these two methods keep them from compiling.
- R-3: The store writes nothing outside the caller's transaction. It never
  commits, rolls back or opens a second connection. On an error the caller
  rolls back, as with the `pgx.Tx` methods.
- R-4: `WithOnFinishedSQL(fn func(ctx context.Context, tx SQLTx, f Finished)
  error)` behaves as UC-4 says. The `Finished` value is the same as the
  `pgx.Tx` hook gets.
- R-5: The supported driver is pgx's stdlib (`github.com/jackc/pgx/v5/stdlib`).
  The doc says so. A transaction on a database that is not Postgres fails at
  the store's first statement, and the call returns that database's error.
  The store writes nothing that can survive the caller's rollback.
- R-6: Every existing method, option and type keeps its signature and
  behaviour. All existing tests pass unchanged.
- R-7: Tests run UC-1 to UC-4 against Postgres through a `*sql.Tx` from
  pgx stdlib, and through a test type with the method set of an Ent `*Tx`
  (UC-1, UC-3). Each covers commit and rollback.
- R-8: An `Example` test of 10 to 20 lines shows UC-1 with a `*sql.Tx` and
  has an `// Output:` comment. It runs in CI, which sets `WORKFLOW_PG_DSN`.
  `go test` without `WORKFLOW_PG_DSN` still passes.
- R-9: README, `llms.txt` and `documentation/hosts.md` list the three
  methods, `SQLTx`, `WithOnFinishedSQL`, the stdlib requirement, and the
  Ent `sql/execquery` note. hosts.md shows how one `pgxpool.Pool` serves
  both the store and `database/sql` (`stdlib.OpenDBFromPool`). The package
  doc in `store.go` mentions the SQL forms.

## Not in scope

- Moving the store or the worker to `database/sql`. The SQLite track (#53)
  owns backend structure, and Postgres stays on pgx.
- Drivers other than pgx stdlib, such as lib/pq. lib/pq encodes `[]byte`
  and maps differently. We do not test it, so we do not promise it.
- `database/sql` forms of `Start`, `Deliver`, `Cancel`, `GetRun`, `Steps`
  and `Events`. They open their own transaction or read outside one, so the
  pool the store already has serves them.
- A `database/sql` form of `Migrate`. It runs at startup on the store's pool.
- Putting Ent in the module's dependencies, even for tests. A test type
  with Ent's method set proves the fit.

## Decisions

- **New `*SQLTx` methods that take a small interface.** The Ent call site is
  `store.StartSQLTx(ctx, tx, run)` with the Ent transaction itself: no
  wrapper, no raw `*sql.Tx`. A `*sql.Tx` fits the same way. The pgx methods
  stay as they are. Rejected, option 1 as #52 states it (public adapters
  and an interface parameter on `StartTx`): every call site wraps its
  transaction (`store.StartTx(ctx, postgres.SQL(tx), run)`), changing
  `StartTx`'s parameter type breaks code that uses the method as a value,
  and gue dropped exactly this design in v6. Rejected, option 2 as stated
  (a `*sql.Tx` parameter): Ent's `*Tx` hides its `*sql.Tx`, so an Ent user
  must begin the transaction outside Ent and build a second client over it.
- **Ent needs the `sql/execquery` feature.** It adds `ExecContext` and
  `QueryContext` to `*ent.Tx`. It is one codegen flag, and without it the
  call does not compile, so nobody is surprised at run time. Rejected: an
  interface without `QueryContext`. The store must read the run row it
  locks, and `database/sql` returns rows only as `*sql.Rows`.
- **`Commit` and `Rollback` are in `SQLTx`, though the store never calls
  them.** With a `*sql.DB`, each statement commits alone. The run lock
  would end at once and the store's writes would not be atomic. The
  compiler now rejects that mistake. Rejected: a run-time check, which
  costs a query on every call and cannot see the difference reliably.
- **A second hook, `WithOnFinishedSQL`, and fail fast without it.** A
  `database/sql` transaction cannot give the existing hook its `pgx.Tx`.
  The store must not skip a hook that promises "once per run, in the
  finishing transaction". So a store with the pgx hook refuses the SQL
  methods until it also has the SQL hook. It refuses at every call, not
  only when a run finishes, so the first test finds it. Rejected: a fake
  `pgx.Tx` over `database/sql` (its `Conn()`, `CopyFrom` and pgx scan rules
  would fail in surprising ways). Rejected: one new hook with a write-only
  argument that both paths can give (existing hooks read in the
  transaction, and it would add a third way to hook a finish). Rejected:
  leave hook users out of this feature (a cancel is the most common way a
  run finishes in an application's transaction).
- **No probe for "is this Postgres".** Every statement on this path is
  Postgres-only: `$n` parameters on schema-qualified tables, `FOR UPDATE`,
  jsonb and `pg_notify`. A SQLite or MySQL transaction fails at the first
  statement, and the error names the table or the syntax. Rejected: a
  `SELECT version()` probe. It costs a round trip on every call to catch a
  mistake that the first test run catches. River and gue rely on the type
  in the same way.
- **pgx stdlib only.** It encodes arguments as pgx does, so both paths
  store the same bytes and the same tests prove both. Rejected: promise
  lib/pq. It would need driver-neutral arguments and its own test matrix.
- **The Example test runs only with `WORKFLOW_PG_DSN`.** An example cannot
  skip, and every other test in the module needs Postgres too. The
  implementation keeps `go test` green without it, for example with a
  `TestMain` that leaves examples out when the variable is unset. Rejected:
  an example without `// Output:`, which CI compiles but never runs.

## Success

An Ent user does UC-1, UC-2 and UC-3 with one call each and no outbox or
relay. The rollback tests prove that nothing survives a rollback.

Must not get worse: the pgx methods and the worker behave and perform as
before, and their tests pass unchanged. The module adds no dependency
(`stdlib` is a package of `pgx/v5`, which it already requires).

## Risks and open questions

- **Divergence between the two paths.** River's `database/sql` driver had
  bugs its pgx driver did not. We reduce this risk with one implementation:
  the internal helpers take a narrow interface (`Exec`, `QueryRow`), and the
  SQL path is an adapter of about 50 lines. `pgx.Tx` satisfies that
  interface as it is, so `worker.go` does not change. The adapter maps
  `sql.ErrNoRows` to the error the helpers check. Not blocking.
- **Merge conflicts.** Other tracks change `store.go` now. This track
  changes the parameter type of the internal helpers (a word per
  signature) and adds the new methods at the end of the Tx section. It
  moves no code. Not blocking.
- **Open rows on one connection.** `database/sql` cannot run a statement
  while a `*sql.Rows` of the same transaction is open. The adapter closes
  each result before it returns. The tests cover this, because every call
  reads and then writes. Not blocking.
