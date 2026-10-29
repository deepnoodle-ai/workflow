-- Migration 1. Migrate applies each migration once, in order, under the
-- database's write lock, and records it in {{p}}schema_migrations. Change
-- the schema by appending a migration, never by editing one that has
-- shipped.
--
-- The tables match the postgres module's. Tables are STRICT, so a value
-- of the wrong type fails at the insert. JSON is TEXT. Times are TEXT in
-- the fixed UTC form 2006-01-02T15:04:05.000000Z, so text order is time
-- order.

-- Definitions are content-addressed. A run pins the hash it started with.
CREATE TABLE {{p}}definitions (
    hash       TEXT PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    document   TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

-- One row per run. state is the workflow.State; the other columns are
-- copies for querying.
CREATE TABLE {{p}}runs (
    id              TEXT PRIMARY KEY,
    definition_hash TEXT NOT NULL REFERENCES {{p}}definitions (hash),
    workflow        TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL,
    state           TEXT NOT NULL,
    error           TEXT,
    outputs         TEXT,
    metadata        TEXT NOT NULL DEFAULT '{}',
    event_seq       INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    finished_at     TEXT
) STRICT;
CREATE INDEX {{p}}runs_status_updated ON {{p}}runs (status, updated_at);

-- One row per step attempt, keyed by StepID: the run record people read.
CREATE TABLE {{p}}steps (
    run_id        TEXT NOT NULL REFERENCES {{p}}runs (id) ON DELETE CASCADE,
    step_id       TEXT NOT NULL,
    step          TEXT NOT NULL,
    kind          TEXT NOT NULL,
    item_index    INTEGER NOT NULL,
    attempt       INTEGER NOT NULL,
    status        TEXT NOT NULL, -- dispatched, completed, failed, cancelled
    params        TEXT,
    item          TEXT,
    output        TEXT,
    error         TEXT,
    not_before    TEXT,
    dispatched_at TEXT NOT NULL,
    settled_at    TEXT,
    PRIMARY KEY (run_id, step_id)
) STRICT;

-- The outbox. Apply's commands become jobs in the same transaction as the
-- state; workers claim them after commit. kind is step or timer.
-- AUTOINCREMENT: a job ID is never reused, so a fence can never match a
-- newer job, and "a row with this ID exists" always means the same job.
CREATE TABLE {{p}}jobs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id        TEXT NOT NULL REFERENCES {{p}}runs (id) ON DELETE CASCADE,
    step_id       TEXT NOT NULL,
    kind          TEXT NOT NULL,
    step_kind     TEXT NOT NULL, -- workers claim only kinds they handle
    command       TEXT NOT NULL,
    run_at        TEXT NOT NULL,
    -- claims counts claims toward MaxClaims; a graceful release gives one
    -- back. fence grows on every claim and never shrinks: only the holder
    -- of the latest claim can heartbeat, settle or release the job.
    claims        INTEGER NOT NULL DEFAULT 0,
    fence         INTEGER NOT NULL DEFAULT 0,
    lease_owner   TEXT,
    lease_expires TEXT,
    created_at    TEXT NOT NULL,
    -- What a worker claims by: every worker takes timers, and steps of the
    -- kinds it has handlers for.
    queue         TEXT NOT NULL GENERATED ALWAYS AS
                  (CASE WHEN kind = 'timer' THEN 'timer' ELSE 'step:' || step_kind END) STORED,
    UNIQUE (run_id, step_id, kind)
) STRICT;
CREATE INDEX {{p}}jobs_claim ON {{p}}jobs (queue, run_at, id);

-- Every event that changed a run, in order.
CREATE TABLE {{p}}events (
    run_id  TEXT NOT NULL REFERENCES {{p}}runs (id) ON DELETE CASCADE,
    seq     INTEGER NOT NULL,
    kind    TEXT NOT NULL,
    step_id TEXT,
    at      TEXT NOT NULL,
    event   TEXT NOT NULL,
    PRIMARY KEY (run_id, seq)
) STRICT;
