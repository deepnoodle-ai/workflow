package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgBackend is the storage contract (hostcore.Backend) on the store's pool.
type pgBackend struct{ s *Store }

// pgTx is a hostcore.Tx on a transaction: the store's own, or the caller's
// pgx.Tx or SQLTx.
type pgTx struct {
	s *Store
	c conn
}

func (s *Store) txOn(c conn) pgTx { return pgTx{s: s, c: c} }

// Write opens a pgx transaction, or a database/sql one on the same pool
// when the store's only finish hook is WithOnFinishedSQL.
func (b pgBackend) Write(ctx context.Context, fn func(hostcore.Tx) error) error {
	if b.s.sqlDB == nil {
		return pgx.BeginFunc(ctx, b.s.pool, func(tx pgx.Tx) error {
			return fn(b.s.txOn(tx))
		})
	}
	tx, err := b.s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(b.s.txOn(sqlConn{tx: tx})); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// claimSQL leases due jobs: $1 limit, $2 worker, $3 lease seconds,
// $4 queues, $5 cutoff (NULL for none; least ignores NULL).
const claimSQL = `
		WITH due AS (
		    SELECT d.id FROM unnest($4::text[]) AS q(queue)
		    CROSS JOIN LATERAL (
		        SELECT id, run_at FROM {{schema}}.jobs
		        WHERE queue = q.queue AND run_at <= least(now(), $5::timestamptz)
		          AND (lease_expires IS NULL OR lease_expires < now())
		        ORDER BY run_at, id
		        LIMIT $1
		        FOR UPDATE SKIP LOCKED
		    ) d
		    ORDER BY d.run_at, d.id
		    LIMIT $1
		)
		UPDATE {{schema}}.jobs t
		SET lease_owner = $2, lease_expires = now() + make_interval(secs => $3),
		    claims = t.claims + 1, fence = t.fence + 1
		FROM due, {{schema}}.runs r
		WHERE t.id = due.id AND r.id = t.run_id
		RETURNING t.id, t.run_id, t.command, t.claims, t.fence, r.definition_hash`

// Claim leases up to req.Limit due jobs. It takes the earliest of each
// queue through the jobs_claim index, then the earliest of those.
func (b pgBackend) Claim(ctx context.Context, req hostcore.ClaimRequest) ([]hostcore.Claimed, error) {
	claimed := time.Now() // no later than the database's now()
	var cutoff *time.Time
	if !req.DueBy.IsZero() {
		cutoff = &req.DueBy
	}
	rows, err := b.s.pool.Query(ctx, b.s.sql(claimSQL),
		req.Limit, req.Owner, req.Lease.Seconds(), req.Queues, cutoff)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (hostcore.Claimed, error) {
		var t hostcore.Claimed
		err := row.Scan(&t.JobID, &t.RunID, &t.Command, &t.Claims, &t.Fence, &t.DefHash)
		t.At = claimed
		return t, err
	})
}

// claimKeySQL leases one job by its key if it is due and not leased:
// $1 run, $2 StepID, $3 kind, $4 worker, $5 lease seconds, $6 queues.
// It returns the same columns as claimSQL.
const claimKeySQL = `
		UPDATE {{schema}}.jobs t
		SET lease_owner = $4, lease_expires = now() + make_interval(secs => $5),
		    claims = t.claims + 1, fence = t.fence + 1
		FROM {{schema}}.runs r
		WHERE t.run_id = $1 AND t.step_id = $2 AND t.kind = $3 AND r.id = t.run_id
		  AND t.queue = ANY($6::text[]) AND t.run_at <= now()
		  AND (t.lease_expires IS NULL OR t.lease_expires < now())
		RETURNING t.id, t.run_id, t.command, t.claims, t.fence, r.definition_hash`

func (b pgBackend) ClaimKey(
	ctx context.Context, ref durable.JobRef, req hostcore.ClaimRequest,
) (*hostcore.Claimed, error) {
	claimed := time.Now() // no later than the database's now()
	var t hostcore.Claimed
	err := b.s.pool.QueryRow(ctx, b.s.sql(claimKeySQL),
		ref.RunID, string(ref.StepID), hostcore.JobKind(ref), req.Owner, req.Lease.Seconds(), req.Queues).
		Scan(&t.JobID, &t.RunID, &t.Command, &t.Claims, &t.Fence, &t.DefHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.At = claimed
	return &t, nil
}

func (b pgBackend) JobStatus(ctx context.Context, ref durable.JobRef) (hostcore.JobStatus, error) {
	var (
		j     hostcore.JobStatus
		lease *time.Time
	)
	err := b.s.pool.QueryRow(ctx, b.s.sql(`
		SELECT queue, run_at, lease_expires, now() FROM {{schema}}.jobs
		WHERE run_id = $1 AND step_id = $2 AND kind = $3`),
		ref.RunID, string(ref.StepID), hostcore.JobKind(ref)).Scan(&j.Queue, &j.RunAt, &lease, &j.Now)
	if errors.Is(err, pgx.ErrNoRows) {
		return hostcore.JobStatus{}, nil
	}
	if err != nil {
		return hostcore.JobStatus{}, err
	}
	j.Found = true
	if lease != nil {
		j.LeaseExpires = *lease
	}
	return j, nil
}

func (b pgBackend) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := b.s.pool.QueryRow(ctx, `SELECT now()`).Scan(&now)
	return now, err
}

func (b pgBackend) Renew(ctx context.Context, l hostcore.Lease, d time.Duration) error {
	return b.s.fenced(ctx, b.s.pool, l, `
		UPDATE {{schema}}.jobs SET lease_expires = now() + make_interval(secs => $3)
		WHERE id = $1 AND fence = $2`, d.Seconds())
}

func (b pgBackend) Release(ctx context.Context, l hostcore.Lease, refund bool) error {
	refunded := 0
	if refund {
		refunded = 1
	}
	return b.s.fenced(ctx, b.s.pool, l, `
		UPDATE {{schema}}.jobs SET lease_owner = NULL, lease_expires = NULL, claims = claims - $3
		WHERE id = $1 AND fence = $2`, refunded)
}

func (b pgBackend) Postpone(ctx context.Context, l hostcore.Lease, d time.Duration) error {
	return b.s.fenced(ctx, b.s.pool, l, `
		UPDATE {{schema}}.jobs
		SET lease_owner = NULL, lease_expires = NULL, run_at = now() + make_interval(secs => $3)
		WHERE id = $1 AND fence = $2`, d.Seconds())
}

// fenced runs a write on job l.JobID at fence l.Fence ($1 and $2) and, when
// it changes no row, says why (rule 1 of the storage contract). The check
// is a second statement, so under READ COMMITTED it sees a delete that
// committed while the write waited for the row; a job ID is never reused,
// so a missing job stays missing.
func (s *Store) fenced(ctx context.Context, q conn, l hostcore.Lease, query string, args ...any) error {
	tag, err := q.Exec(ctx, s.sql(query), append([]any{l.JobID, l.Fence}, args...)...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	err = q.QueryRow(ctx, s.sql(`SELECT EXISTS (SELECT 1 FROM {{schema}}.jobs WHERE id = $1)`),
		l.JobID).Scan(&exists)
	if err == nil && !exists {
		return hostcore.ErrJobGone
	}
	// A failed check is a lost lease as well: like any database error that
	// ends a claim, it is LeaseLost.
	return hostcore.ErrLeaseLost
}

// Unstorable reports whether Postgres rejected a value: a data exception
// (class 22, such as an invalid jsonb escape) or a program limit (class 54,
// such as a jsonb too large).
func (pgBackend) Unstorable(err error) bool { return isUnstorable(err) }

func (b pgBackend) DefinitionDoc(ctx context.Context, hash string) ([]byte, error) {
	return b.s.definitionDoc(ctx, b.s.pool, hash)
}

func (s *Store) definitionDoc(ctx context.Context, q conn, hash string) ([]byte, error) {
	var doc []byte
	err := q.QueryRow(ctx, s.sql(`SELECT document FROM {{schema}}.definitions WHERE hash = $1`), hash).
		Scan(&doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return doc, err
}

func (t pgTx) LockRun(ctx context.Context, runID string) ([]byte, string, error) {
	var (
		data []byte
		hash string
	)
	err := t.c.QueryRow(ctx,
		t.s.sql(`SELECT state, definition_hash FROM {{schema}}.runs WHERE id = $1 FOR UPDATE`),
		runID).Scan(&data, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrRunNotFound
	}
	return data, hash, err
}

func (t pgTx) DefinitionDoc(ctx context.Context, hash string) ([]byte, error) {
	return t.s.definitionDoc(ctx, t.c, hash)
}

func (t pgTx) InsertDefinition(ctx context.Context, hash, name string, doc []byte) error {
	_, err := t.c.Exec(ctx, t.s.sql(`
		INSERT INTO {{schema}}.definitions (hash, name, document) VALUES ($1, $2, $3)
		ON CONFLICT (hash) DO NOTHING`), hash, name, doc)
	return err
}

func (t pgTx) InsertRun(ctx context.Context, r hostcore.RunRow) (bool, error) {
	tag, err := t.c.Exec(ctx, t.s.sql(`
		INSERT INTO {{schema}}.runs (id, definition_hash, workflow, status, state, error, outputs, metadata,
		                             event_seq, created_at, updated_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $9, $10)
		ON CONFLICT (id) DO NOTHING`),
		r.ID, r.DefinitionHash, r.Workflow, string(r.Status), r.State,
		nullJSON(r.Error), nullJSON(r.Outputs), r.Metadata, r.At, nullTime(r.FinishedAt))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (t pgTx) UpdateRun(ctx context.Context, runID string, u hostcore.RunUpdate) (int64, error) {
	var seq int64
	err := t.c.QueryRow(ctx, t.s.sql(`
		UPDATE {{schema}}.runs
		SET state = $2, status = $3, error = $4, outputs = $5, finished_at = $6,
		    updated_at = $7, event_seq = event_seq + 1
		WHERE id = $1
		RETURNING event_seq`),
		runID, u.State, string(u.Status), nullJSON(u.Error), nullJSON(u.Outputs),
		nullTime(u.FinishedAt), u.At,
	).Scan(&seq)
	return seq, err
}

func (t pgTx) InsertEvent(ctx context.Context, runID string, e hostcore.EventRow) error {
	_, err := t.c.Exec(ctx, t.s.sql(`
		INSERT INTO {{schema}}.events (run_id, seq, kind, step_id, at, event) VALUES ($1, $2, $3, $4, $5, $6)`),
		runID, e.Seq, string(e.Kind), nullString(string(e.StepID)), e.At, e.Event)
	return err
}

func (t pgTx) InsertStep(ctx context.Context, runID string, s hostcore.StepRow) error {
	_, err := t.c.Exec(ctx, t.s.sql(`
		INSERT INTO {{schema}}.steps
		    (run_id, step_id, step, kind, item_index, attempt, status, params, item, not_before, dispatched_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'dispatched', $7, $8, $9, $10)
		ON CONFLICT (run_id, step_id) DO NOTHING`),
		runID, string(s.StepID), s.Step, s.Kind, s.Index, s.Attempt,
		nullJSON(s.Params), nullJSON(s.Item), nullTime(s.NotBefore), s.DispatchedAt)
	return err
}

func (t pgTx) SettleStep(ctx context.Context, runID string, s hostcore.StepSettle) error {
	_, err := t.c.Exec(ctx, t.s.sql(`
		UPDATE {{schema}}.steps SET status = $3, output = $4, error = $5, settled_at = $6
		WHERE run_id = $1 AND step_id = $2 AND status = 'dispatched'`),
		runID, string(s.StepID), string(s.Status), nullJSON(s.Output), nullJSON(s.Error), s.At)
	return err
}

func (t pgTx) CancelSteps(ctx context.Context, runID string, stepID workflow.StepID, at time.Time) error {
	if stepID == "" {
		_, err := t.c.Exec(ctx, t.s.sql(`
			UPDATE {{schema}}.steps SET status = 'cancelled', settled_at = $2
			WHERE run_id = $1 AND status = 'dispatched'`), runID, at)
		return err
	}
	_, err := t.c.Exec(ctx, t.s.sql(`
		UPDATE {{schema}}.steps SET status = 'cancelled', settled_at = $3
		WHERE run_id = $1 AND step_id = $2 AND status = 'dispatched'`),
		runID, string(stepID), at)
	return err
}

func (t pgTx) InsertJob(ctx context.Context, runID string, j hostcore.JobRow) error {
	// A job due now takes the database's clock: coalesce($6, now()).
	_, err := t.c.Exec(ctx, t.s.sql(`
		INSERT INTO {{schema}}.jobs (run_id, step_id, kind, step_kind, command, run_at)
		VALUES ($1, $2, $3, $4, $5, coalesce($6, now()))
		ON CONFLICT (run_id, step_id, kind) DO NOTHING`),
		runID, string(j.StepID), j.Kind, j.StepKind, j.Command, nullTime(j.RunAt))
	return err
}

func (t pgTx) DeleteJobs(ctx context.Context, runID string, stepID workflow.StepID) error {
	if stepID == "" {
		_, err := t.c.Exec(ctx, t.s.sql(`DELETE FROM {{schema}}.jobs WHERE run_id = $1`), runID)
		return err
	}
	_, err := t.c.Exec(ctx, t.s.sql(`
		DELETE FROM {{schema}}.jobs WHERE run_id = $1 AND step_id = $2`),
		runID, string(stepID))
	return err
}

func (t pgTx) DeleteJob(ctx context.Context, l hostcore.Lease) error {
	return t.s.fenced(ctx, t.c, l, `DELETE FROM {{schema}}.jobs WHERE id = $1 AND fence = $2`)
}

func (t pgTx) JobsWritten(ctx context.Context, runID string) error {
	_, err := t.c.Exec(ctx, `SELECT pg_notify($1, $2)`, t.s.NotifyChannel(), runID)
	return err
}

func (t pgTx) StatusChanged(ctx context.Context, runID string, status workflow.RunStatus) error {
	payload, _ := json.Marshal(map[string]string{
		"run_id": runID,
		"status": string(status),
	})
	_, err := t.c.Exec(ctx, `SELECT pg_notify($1, $2)`, t.s.RunsChannel(), string(payload))
	return err
}

func (t pgTx) OnFinished(ctx context.Context, f durable.Finished) error {
	if t.s.onFinished == nil && t.s.onFinishedSQL == nil {
		return nil
	}
	return t.s.callOnFinished(ctx, t.c, f)
}

func (b pgBackend) GetRun(ctx context.Context, runID string) (*durable.Run, error) {
	var (
		r      Run
		status string
		state  []byte
	)
	err := b.s.pool.QueryRow(ctx, b.s.sql(`
		SELECT id, workflow, definition_hash, status, state, metadata, created_at, updated_at, finished_at
		FROM {{schema}}.runs WHERE id = $1`), runID).Scan(
		&r.ID, &r.Workflow, &r.DefinitionHash, &status, &state, &r.Metadata,
		&r.CreatedAt, &r.UpdatedAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Status = workflow.RunStatus(status)
	if err := json.Unmarshal(state, &r.State); err != nil {
		return nil, err
	}
	return &r, nil
}

func (b pgBackend) Steps(ctx context.Context, runID string) ([]durable.StepRecord, error) {
	rows, err := b.s.pool.Query(ctx, b.s.sql(`
		SELECT step_id, step, kind, item_index, attempt, status, params, item, output, error,
		       not_before, dispatched_at, settled_at
		FROM {{schema}}.steps WHERE run_id = $1 ORDER BY dispatched_at, step_id`), runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (StepRecord, error) {
		var (
			r          StepRecord
			id, status string
		)
		err := row.Scan(&id, &r.Step, &r.StepKind, &r.Index, &r.Attempt, &status,
			&r.Params, &r.Item, &r.Output, &r.Error, &r.NotBefore, &r.DispatchedAt,
			&r.SettledAt)
		r.StepID, r.Status = workflow.StepID(id), StepStatus(status)
		return r, err
	})
}

func (b pgBackend) Events(ctx context.Context, runID string) ([]workflow.Event, error) {
	rows, err := b.s.pool.Query(ctx,
		b.s.sql(`SELECT event FROM {{schema}}.events WHERE run_id = $1 ORDER BY seq`), runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (workflow.Event, error) {
		var ev workflow.Event
		err := row.Scan(&ev)
		return ev, err
	})
}

// isUnstorable reports whether Postgres rejected a value: a data exception
// (class 22, such as an invalid jsonb escape) or a program limit (class 54,
// such as a jsonb too large).
func isUnstorable(err error) bool {
	pe, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (strings.HasPrefix(pe.Code, "22") || strings.HasPrefix(pe.Code, "54"))
}

// nullJSON passes encoded JSON, or SQL NULL for nil.
func nullJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}

// nullTime passes t, or SQL NULL for the zero time.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return &t
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
