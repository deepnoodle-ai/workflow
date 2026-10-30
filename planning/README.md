# Planning

Design notes, kept for their reasoning. The code and the guides in
`documentation/` are the reference; where a note disagrees, the code wins.

Current, about the engine merged in #42:

- [proto/progression-engine-summary.md](proto/progression-engine-summary.md):
  a shareable overview of `Apply`, the host contract, typed definitions and
  the two hosts.
- [proto/flow-progression-findings.md](proto/flow-progression-findings.md):
  what the spike that built it found.
- [proto/typed-definitions.md](proto/typed-definitions.md): how expressions,
  routing and data are typed.

Current PRDs, about the postgres host on that engine:

- [prds/003-worker-drain.md](prds/003-worker-drain.md): stop a worker
  without cutting off its steps. Shipped in #66.
- [prds/004-sql-tx.md](prds/004-sql-tx.md): start, deliver and cancel runs
  in a `database/sql` transaction. Shipped in #62.
- [prds/005-worker-observer.md](prds/005-worker-observer.md): count and time
  what the worker does, and see how far behind it is. Shipped in #64.
- [prds/006-run-once-execute.md](prds/006-run-once-execute.md): run due work
  on demand with `RunOnce` and `Execute`. Shipped in #67.
- [prds/007-sqlite-backend.md](prds/007-sqlite-backend.md): run the durable
  host on SQLite in development and Postgres in production. Shipped in
  #73.
- [prds/010-wait-step.md](prds/010-wait-step.md): wait for a delivery
  with a built-in `wait` step. Approved.
- [prds/011-run-listing.md](prds/011-run-listing.md): list and filter runs,
  and delete ended runs, on postgres and sqlite. Approved.

Current PRDs, about modules beside the hosts:

- [prds/012-decision-guard.md](prds/012-decision-guard.md): check each
  job with a decision model before it runs. Proposed.

Current tech specs:

- [specs/003-worker-drain.md](specs/003-worker-drain.md): the worker's stop
  states, and how `Shutdown`, `MayClaim` and `Active` work. Shipped in #66.
- [specs/006-run-once-execute.md](specs/006-run-once-execute.md): the claim
  paths for `RunOnce` and `Execute`. Shipped in #67.
- [specs/007-sqlite-backend.md](specs/007-sqlite-backend.md): a shared
  durable-host core, and a SQLite backend on it. Shipped in #69 and #73.
- [specs/011-run-listing.md](specs/011-run-listing.md): the queries,
  cursor and migration for listing runs and deleting ended runs. Approved.

Historical, about the goroutine engine before #42 (`v0.0.5`): everything in
[review/](review), PRDs
[001](prds/001-reduce-consumer-boilerplate.md) and
[002](prds/002-signals-waits-pausing.md), and specs
[001](specs/001-reduce-consumer-boilerplate.md) and
[002](specs/002-worker-store-improvements.md). They describe activities,
checkpointers and signals, which no longer exist.
