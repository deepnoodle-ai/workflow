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
    completed_at   TIMESTAMPTZ
);

-- Claim loop orders queued runs by created_at.
CREATE INDEX IF NOT EXISTS workflow_runs_status_created
    ON workflow_runs (status, created_at);

-- Reaper scans running runs by heartbeat age.
CREATE INDEX IF NOT EXISTS workflow_runs_status_heartbeat
    ON workflow_runs (status, heartbeat_at);

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
