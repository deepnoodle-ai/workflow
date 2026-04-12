-- workflow_runs is the durable queue and state table for runs
-- managed by the worker package. One row per run; the checkpoint
-- blob lives in the checkpoint column.
CREATE TABLE IF NOT EXISTS workflow_runs (
    id             TEXT PRIMARY KEY,
    spec           BYTEA NOT NULL,
    status         TEXT NOT NULL,
    attempt        INTEGER NOT NULL DEFAULT 0,
    claimed_by     TEXT NOT NULL DEFAULT '',
    heartbeat_at   TIMESTAMPTZ,
    checkpoint     BYTEA,
    result         BYTEA,
    error_message  TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at     TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ,
    org_id         TEXT NOT NULL DEFAULT '',
    workflow_type  TEXT NOT NULL DEFAULT '',
    initiated_by   TEXT NOT NULL DEFAULT '',
    credit_cost    INTEGER NOT NULL DEFAULT 0,
    callback_url   TEXT NOT NULL DEFAULT ''
);

-- Upgrade path: add business columns to existing tables.
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS org_id TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS workflow_type TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS initiated_by TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS credit_cost INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS callback_url TEXT NOT NULL DEFAULT '';

-- Claim loop orders queued runs by created_at.
CREATE INDEX IF NOT EXISTS workflow_runs_status_created
    ON workflow_runs (status, created_at);

-- Reaper scans running runs by heartbeat age.
CREATE INDEX IF NOT EXISTS workflow_runs_status_heartbeat
    ON workflow_runs (status, heartbeat_at);

-- Org-scoped queries.
CREATE INDEX IF NOT EXISTS workflow_runs_org_created
    ON workflow_runs (org_id, created_at);

CREATE INDEX IF NOT EXISTS workflow_runs_org_type_created
    ON workflow_runs (org_id, workflow_type, created_at);

-- workflow_step_progress is a derived observability table written
-- from workflow.StepProgressStore callbacks. One row per
-- (execution_id, step_name, branch_id); the latest update wins.
CREATE TABLE IF NOT EXISTS workflow_step_progress (
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
    ON workflow_step_progress (execution_id);

-- workflow_activity_log is the append-only activity operation log
-- written from workflow.ActivityLogger callbacks.
CREATE TABLE IF NOT EXISTS workflow_activity_log (
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
    ON workflow_activity_log (execution_id, start_time);

-- workflow_events is an append-only event stream for real-time
-- progress tracking (SSE) and observability.
CREATE TABLE IF NOT EXISTS workflow_events (
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
    ON workflow_events (run_id, seq);

-- workflow_triggers implements the transactional outbox pattern for
-- durable workflow chaining.
CREATE TABLE IF NOT EXISTS workflow_triggers (
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
    ON workflow_triggers (status, created_at);

CREATE INDEX IF NOT EXISTS workflow_triggers_parent
    ON workflow_triggers (parent_run_id);

-- workflow_credit_ledger tracks credit debits and refunds per run.
-- The (run_id, reason) unique constraint ensures idempotency.
CREATE TABLE IF NOT EXISTS workflow_credit_ledger (
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
    ON workflow_credit_ledger (org_id);

-- workflow_webhooks tracks durable webhook delivery state.
CREATE TABLE IF NOT EXISTS workflow_webhooks (
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
    ON workflow_webhooks (status, created_at);
