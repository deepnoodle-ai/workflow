-- schema.sql is a Go text/template. The Store executes it against
-- the configured schema (default "public") and runs the result as
-- a single exec. Every table and index reference uses {{.Schema}}
-- so the store can coexist with a consumer's existing tables.

CREATE SCHEMA IF NOT EXISTS {{.Schema}};

-- workflow_runs is the durable queue and state table for runs
-- managed by the worker package. One row per run; the checkpoint
-- blob lives in the checkpoint column.
CREATE TABLE IF NOT EXISTS {{.Schema}}.workflow_runs (
    id              TEXT PRIMARY KEY,
    spec            BYTEA NOT NULL,
    status          TEXT NOT NULL,
    attempt         INTEGER NOT NULL DEFAULT 0,
    claimed_by      TEXT NOT NULL DEFAULT '',
    heartbeat_at    TIMESTAMPTZ,
    checkpoint      BYTEA,
    result          BYTEA,
    error_message   TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    org_id          TEXT,
    project_id      TEXT,
    parent_run_id   TEXT,
    workflow_type   TEXT NOT NULL DEFAULT '',
    initiated_by    TEXT,
    credit_cost     INTEGER NOT NULL DEFAULT 0,
    callback_url    TEXT NOT NULL DEFAULT '',
    metadata        JSONB
);

-- v0.0.3 -> v0.0.4 upgrade path. Existing tables from v0.0.3 had
-- org_id TEXT NOT NULL DEFAULT '', initiated_by TEXT NOT NULL DEFAULT '',
-- and lacked project_id / parent_run_id / metadata. This block drops
-- the NOT NULLs so empty-string sentinels can become real NULLs,
-- adds the new columns, then rewrites any pre-existing '' sentinels
-- to NULL. Every statement is idempotent, so Migrate() is safe to
-- call repeatedly.
ALTER TABLE {{.Schema}}.workflow_runs ALTER COLUMN org_id DROP NOT NULL;
ALTER TABLE {{.Schema}}.workflow_runs ALTER COLUMN org_id DROP DEFAULT;
ALTER TABLE {{.Schema}}.workflow_runs ALTER COLUMN initiated_by DROP NOT NULL;
ALTER TABLE {{.Schema}}.workflow_runs ALTER COLUMN initiated_by DROP DEFAULT;
ALTER TABLE {{.Schema}}.workflow_runs ADD COLUMN IF NOT EXISTS project_id    TEXT;
ALTER TABLE {{.Schema}}.workflow_runs ADD COLUMN IF NOT EXISTS parent_run_id TEXT;
ALTER TABLE {{.Schema}}.workflow_runs ADD COLUMN IF NOT EXISTS metadata      JSONB;

-- Rewrite '' sentinels from v0.0.3 to NULL so the new read API
-- (GetRun, ListRuns, DeleteRun) can find them via "org_id IS NULL"
-- and the partial indexes (WHERE org_id IS NOT NULL) cover them
-- correctly. New rows never insert '' for these columns.
UPDATE {{.Schema}}.workflow_runs SET org_id       = NULL WHERE org_id       = '';
UPDATE {{.Schema}}.workflow_runs SET initiated_by = NULL WHERE initiated_by = '';

-- Claim loop orders queued runs by created_at.
CREATE INDEX IF NOT EXISTS workflow_runs_status_created
    ON {{.Schema}}.workflow_runs (status, created_at);

-- Reaper scans running runs by heartbeat age.
CREATE INDEX IF NOT EXISTS workflow_runs_status_heartbeat
    ON {{.Schema}}.workflow_runs (status, heartbeat_at);

-- Org- and project-scoped listing queries.
CREATE INDEX IF NOT EXISTS workflow_runs_org_created
    ON {{.Schema}}.workflow_runs (org_id, created_at DESC)
    WHERE org_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS workflow_runs_org_type_created
    ON {{.Schema}}.workflow_runs (org_id, workflow_type, created_at DESC)
    WHERE org_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS workflow_runs_org_project_created
    ON {{.Schema}}.workflow_runs (org_id, project_id, created_at DESC)
    WHERE project_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS workflow_runs_parent_created
    ON {{.Schema}}.workflow_runs (parent_run_id, created_at DESC)
    WHERE parent_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS workflow_runs_metadata_gin
    ON {{.Schema}}.workflow_runs USING GIN (metadata);

-- Keyset pagination on the run list.
CREATE INDEX IF NOT EXISTS workflow_runs_created_at_id
    ON {{.Schema}}.workflow_runs (created_at DESC, id DESC);

-- workflow_step_progress is a derived observability table written
-- from workflow.StepProgressStore callbacks. One row per
-- (execution_id, step_name, branch_id); the latest update wins.
CREATE TABLE IF NOT EXISTS {{.Schema}}.workflow_step_progress (
    execution_id TEXT NOT NULL,
    step_name    TEXT NOT NULL,
    branch_id    TEXT NOT NULL,
    status       TEXT NOT NULL,
    activity     TEXT NOT NULL,
    attempt      INTEGER NOT NULL,
    detail       JSONB,
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    error        TEXT NOT NULL DEFAULT '',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (execution_id, step_name, branch_id)
);

CREATE INDEX IF NOT EXISTS workflow_step_progress_execution
    ON {{.Schema}}.workflow_step_progress (execution_id);

-- workflow_activity_log is the append-only activity operation log
-- written from workflow.ActivityLogger callbacks.
CREATE TABLE IF NOT EXISTS {{.Schema}}.workflow_activity_log (
    id           TEXT PRIMARY KEY,
    execution_id TEXT NOT NULL,
    activity     TEXT NOT NULL,
    step_name    TEXT NOT NULL,
    branch_id    TEXT NOT NULL,
    parameters   JSONB,
    result       JSONB,
    error        TEXT NOT NULL DEFAULT '',
    start_time   TIMESTAMPTZ NOT NULL,
    duration     DOUBLE PRECISION NOT NULL
);

CREATE INDEX IF NOT EXISTS workflow_activity_log_execution
    ON {{.Schema}}.workflow_activity_log (execution_id, start_time);

-- workflow_events is an append-only event stream for real-time
-- progress tracking (SSE) and observability.
CREATE TABLE IF NOT EXISTS {{.Schema}}.workflow_events (
    seq        BIGSERIAL PRIMARY KEY,
    run_id     TEXT NOT NULL,
    event_type TEXT NOT NULL,
    attempt    INTEGER NOT NULL DEFAULT 0,
    worker_id  TEXT NOT NULL DEFAULT '',
    step_name  TEXT NOT NULL DEFAULT '',
    payload    JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS workflow_events_run
    ON {{.Schema}}.workflow_events (run_id, seq);

-- workflow_triggers implements the transactional outbox pattern for
-- durable workflow chaining.
CREATE TABLE IF NOT EXISTS {{.Schema}}.workflow_triggers (
    id             TEXT PRIMARY KEY,
    parent_run_id  TEXT NOT NULL,
    child_spec     JSONB NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    attempts       INTEGER NOT NULL DEFAULT 0,
    error_message  TEXT NOT NULL DEFAULT '',
    child_run_id   TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS workflow_triggers_status
    ON {{.Schema}}.workflow_triggers (status, created_at);

CREATE INDEX IF NOT EXISTS workflow_triggers_parent
    ON {{.Schema}}.workflow_triggers (parent_run_id);

-- workflow_credit_ledger tracks credit debits and refunds per run.
-- The (run_id, reason) unique constraint ensures idempotency.
CREATE TABLE IF NOT EXISTS {{.Schema}}.workflow_credit_ledger (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    run_id        TEXT NOT NULL,
    workflow_type TEXT NOT NULL DEFAULT '',
    amount        INTEGER NOT NULL,
    reason        TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (run_id, reason)
);

CREATE INDEX IF NOT EXISTS workflow_credit_ledger_org
    ON {{.Schema}}.workflow_credit_ledger (org_id);

-- workflow_webhooks tracks durable webhook delivery state.
CREATE TABLE IF NOT EXISTS {{.Schema}}.workflow_webhooks (
    id           TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL,
    url          TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    payload      JSONB,
    status       TEXT NOT NULL DEFAULT 'pending',
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS workflow_webhooks_status
    ON {{.Schema}}.workflow_webhooks (status, created_at);
