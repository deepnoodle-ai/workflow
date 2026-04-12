// Package sqlite provides a SQLite-backed Store that implements the
// same persistence interfaces as the postgres package: QueueStore,
// Checkpointer, StepProgressStore, ActivityLogger, and the subsystem
// stores (EventStore, TriggerStore, CreditStore, WebhookStore).
//
// The Store uses [database/sql] and is driver-agnostic. The consumer
// registers their preferred SQLite driver (e.g., modernc.org/sqlite
// or github.com/mattn/go-sqlite3) and passes an open [*sql.DB].
//
// Because SQLite is single-writer, claim serialization uses immediate
// transactions rather than FOR UPDATE SKIP LOCKED. This makes the
// store suitable for development, testing, and single-process
// deployments — not for distributed worker fleets.
package sqlite
