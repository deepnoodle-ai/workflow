CREATE SCHEMA IF NOT EXISTS {{schema}};

-- Definitions are content-addressed. A run pins the hash it started with.
CREATE TABLE IF NOT EXISTS {{schema}}.definitions (
    hash       text PRIMARY KEY,
    name       text NOT NULL DEFAULT '',
    document   jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One row per run. state is the workflow.State; the other columns are
-- copies for querying.
CREATE TABLE IF NOT EXISTS {{schema}}.runs (
    id              text PRIMARY KEY,
    definition_hash text NOT NULL REFERENCES {{schema}}.definitions (hash),
    workflow        text NOT NULL DEFAULT '',
    status          text NOT NULL,
    state           jsonb NOT NULL,
    error           jsonb,
    outputs         jsonb,
    metadata        jsonb NOT NULL DEFAULT '{}',
    event_seq       bigint NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);
CREATE INDEX IF NOT EXISTS runs_status_updated ON {{schema}}.runs (status, updated_at);
CREATE INDEX IF NOT EXISTS runs_metadata ON {{schema}}.runs USING gin (metadata jsonb_path_ops);

-- One row per step attempt, keyed by StepID: the run record people read.
CREATE TABLE IF NOT EXISTS {{schema}}.steps (
    run_id        text NOT NULL REFERENCES {{schema}}.runs (id) ON DELETE CASCADE,
    step_id       text NOT NULL,
    step          text NOT NULL,
    kind          text NOT NULL,
    item_index    integer NOT NULL,
    attempt       integer NOT NULL,
    status        text NOT NULL, -- dispatched, completed, failed, cancelled
    params        jsonb,
    item          jsonb,
    output        jsonb,
    error         jsonb,
    not_before    timestamptz,
    dispatched_at timestamptz NOT NULL,
    settled_at    timestamptz,
    PRIMARY KEY (run_id, step_id)
);

-- The outbox. Apply's commands become tasks in the same transaction as the
-- state; workers claim them after commit. kind is step or timer.
CREATE TABLE IF NOT EXISTS {{schema}}.tasks (
    id            bigserial PRIMARY KEY,
    run_id        text NOT NULL REFERENCES {{schema}}.runs (id) ON DELETE CASCADE,
    step_id       text NOT NULL,
    kind          text NOT NULL,
    command       jsonb NOT NULL,
    run_at        timestamptz NOT NULL,
    claims        integer NOT NULL DEFAULT 0,
    lease_owner   text,
    lease_expires timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, step_id, kind)
);
CREATE INDEX IF NOT EXISTS tasks_due ON {{schema}}.tasks (run_at, id);

-- Every event that changed a run, in order.
CREATE TABLE IF NOT EXISTS {{schema}}.events (
    run_id  text NOT NULL REFERENCES {{schema}}.runs (id) ON DELETE CASCADE,
    seq     bigint NOT NULL,
    kind    text NOT NULL,
    step_id text,
    at      timestamptz NOT NULL,
    event   jsonb NOT NULL,
    PRIMARY KEY (run_id, seq)
);
