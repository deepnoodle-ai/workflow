CREATE TABLE IF NOT EXISTS workflow_runs (
    id              TEXT PRIMARY KEY,
    spec            BLOB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued',
    attempt         INTEGER NOT NULL DEFAULT 0,
    claimed_by      TEXT NOT NULL DEFAULT '',
    heartbeat_at    TEXT,
    checkpoint      BLOB,
    result          BLOB,
    error_message   TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f+00:00', 'now')),
    started_at      TEXT,
    completed_at    TEXT,
    org_id          TEXT,
    project_id      TEXT,
    parent_run_id   TEXT,
    workflow_type   TEXT NOT NULL DEFAULT '',
    initiated_by    TEXT,
    credit_cost     INTEGER NOT NULL DEFAULT 0,
    callback_url    TEXT NOT NULL DEFAULT '',
    metadata        TEXT
);

CREATE INDEX IF NOT EXISTS workflow_runs_status_created
    ON workflow_runs (status, created_at);

CREATE INDEX IF NOT EXISTS workflow_runs_status_heartbeat
    ON workflow_runs (status, heartbeat_at);

CREATE INDEX IF NOT EXISTS workflow_runs_org_created
    ON workflow_runs (org_id, created_at);

CREATE TABLE IF NOT EXISTS workflow_step_progress (
    execution_id TEXT NOT NULL,
    step_name    TEXT NOT NULL,
    branch_id    TEXT NOT NULL,
    status       TEXT NOT NULL,
    activity     TEXT NOT NULL,
    attempt      INTEGER NOT NULL,
    detail       TEXT,
    started_at   TEXT,
    finished_at  TEXT,
    error        TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f+00:00', 'now')),
    PRIMARY KEY (execution_id, step_name, branch_id)
);

CREATE INDEX IF NOT EXISTS workflow_step_progress_execution
    ON workflow_step_progress (execution_id);

CREATE TABLE IF NOT EXISTS workflow_activity_log (
    id           TEXT PRIMARY KEY,
    execution_id TEXT NOT NULL,
    activity     TEXT NOT NULL,
    step_name    TEXT NOT NULL,
    branch_id    TEXT NOT NULL,
    parameters   TEXT,
    result       TEXT,
    error        TEXT NOT NULL DEFAULT '',
    start_time   TEXT NOT NULL,
    duration     REAL NOT NULL
);

CREATE INDEX IF NOT EXISTS workflow_activity_log_execution
    ON workflow_activity_log (execution_id, start_time);

CREATE TABLE IF NOT EXISTS workflow_events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id     TEXT NOT NULL,
    event_type TEXT NOT NULL,
    attempt    INTEGER NOT NULL DEFAULT 0,
    worker_id  TEXT NOT NULL DEFAULT '',
    step_name  TEXT NOT NULL DEFAULT '',
    payload    TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f+00:00', 'now'))
);

CREATE INDEX IF NOT EXISTS workflow_events_run
    ON workflow_events (run_id, seq);

CREATE TABLE IF NOT EXISTS workflow_triggers (
    id             TEXT PRIMARY KEY,
    parent_run_id  TEXT NOT NULL,
    child_spec     TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    attempts       INTEGER NOT NULL DEFAULT 0,
    error_message  TEXT NOT NULL DEFAULT '',
    child_run_id   TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f+00:00', 'now')),
    processed_at   TEXT
);

CREATE INDEX IF NOT EXISTS workflow_triggers_status
    ON workflow_triggers (status, created_at);

CREATE INDEX IF NOT EXISTS workflow_triggers_parent
    ON workflow_triggers (parent_run_id);

CREATE TABLE IF NOT EXISTS workflow_credit_ledger (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    run_id        TEXT NOT NULL,
    workflow_type TEXT NOT NULL DEFAULT '',
    amount        INTEGER NOT NULL,
    reason        TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f+00:00', 'now')),
    UNIQUE (run_id, reason)
);

CREATE INDEX IF NOT EXISTS workflow_credit_ledger_org
    ON workflow_credit_ledger (org_id);

CREATE TABLE IF NOT EXISTS workflow_webhooks (
    id           TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL,
    url          TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    payload      BLOB,
    status       TEXT NOT NULL DEFAULT 'pending',
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f+00:00', 'now')),
    delivered_at TEXT
);

CREATE INDEX IF NOT EXISTS workflow_webhooks_status
    ON workflow_webhooks (status, created_at);
