package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
)

// timeLayout is how every time is stored: fixed-width UTC with
// microseconds, so text order is time order.
const timeLayout = "2006-01-02T15:04:05.000000Z"

// now is the store's clock: the process's, in UTC, to the microsecond.
// Every process that shares the file is on one host, so they share it.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// fmtTime formats a time for a query. Every time the store binds goes
// through it: a driver formats a time.Time in its own layout (mattn writes
// "2026-10-29 02:17:53+00:00"), and a space sorts before the T, so such a
// bound would compare wrongly with every stored time of the same day.
func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseTime reads a time the store wrote with fmtTime.
func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// errTimeArg and errReadFirst are internal errors. They fail every test at
// once when a statement breaks the store's rules.
var (
	errTimeArg = errors.New("sqlite: internal error: a time.Time argument; " +
		"format it with fmtTime so that it sorts as stored")
	errReadFirst = errors.New("sqlite: internal error: a read before the transaction's first write; " +
		"it can fail with SQLITE_BUSY")
)

// querier is what a statement runs on: the pool, or a transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func checkArgs(args []any) error {
	for _, a := range args {
		switch a.(type) {
		case time.Time, *time.Time:
			return errTimeArg
		}
	}
	return nil
}

// exec runs one statement. Every statement of the store goes through exec
// or query, which refuse a time.Time argument.
func (s *Store) exec(ctx context.Context, q querier, query string, args ...any) (sql.Result, error) {
	if err := checkArgs(args); err != nil {
		return nil, err
	}
	return q.ExecContext(ctx, s.sql(query), args...)
}

func (s *Store) query(ctx context.Context, q querier, query string, args ...any) (*sql.Rows, error) {
	if err := checkArgs(args); err != nil {
		return nil, err
	}
	return q.QueryContext(ctx, s.sql(query), args...)
}

// row is the first row of a query. Scan returns sql.ErrNoRows for none and
// closes the rows, so the transaction can run its next statement.
type row struct {
	rows *sql.Rows
	err  error
}

func (s *Store) queryRow(ctx context.Context, q querier, query string, args ...any) row {
	rows, err := s.query(ctx, q, query, args...)
	return row{rows: rows, err: err}
}

func (r row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := r.rows.Scan(dest...); err != nil {
		return err
	}
	return r.rows.Close()
}

// nullText passes encoded JSON as TEXT, or NULL for nil.
func nullText(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

// nullTime passes a time, or NULL for the zero time.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return fmtTime(t)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// sqliteBackend is the storage contract (hostcore.Backend) on the store's
// database.
type sqliteBackend struct{ s *Store }

// Write runs fn in a transaction the store opens. Every write callback in
// the core starts with a write (LockRun, InsertDefinition or DeleteJob),
// so the transaction takes the write lock under the busy handler before it
// reads, whatever the DSN's _txlock. sqliteTx fails a read that comes
// first.
func (b sqliteBackend) Write(ctx context.Context, fn func(hostcore.Tx) error) error {
	tx, err := b.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&sqliteTx{s: b.s, tx: tx, own: true}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// claimed reads the columns a claim returns.
type claimRow struct {
	hostcore.Claimed
	runAt string
}

func scanClaims(rows *sql.Rows, at time.Time) ([]claimRow, error) {
	defer rows.Close()
	var out []claimRow
	for rows.Next() {
		var (
			c       claimRow
			command string
			hash    sql.NullString
		)
		if err := rows.Scan(&c.JobID, &c.RunID, &command, &c.Claims, &c.Fence, &c.runAt, &hash); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(command), &c.Command); err != nil {
			return nil, fmt.Errorf("sqlite: decode job %d: %w", c.JobID, err)
		}
		c.DefHash, c.At = hash.String, at
		out = append(out, c)
	}
	return out, rows.Err()
}

// placeholders returns n numbered placeholders from ?from, and the queues
// as arguments.
func placeholders(from int, queues []string) (string, []any) {
	ph := make([]string, len(queues))
	args := make([]any, len(queues))
	for i, q := range queues {
		ph[i] = fmt.Sprintf("?%d", from+i)
		args[i] = q
	}
	return strings.Join(ph, ", "), args
}

// dueBy is the claim's due bound: now, or an earlier DueBy.
func dueBy(at time.Time, req hostcore.ClaimRequest) time.Time {
	if !req.DueBy.IsZero() && req.DueBy.Before(at) {
		return req.DueBy
	}
	return at
}

// claimSQL leases due jobs: ?1 worker, ?2 lease expiry, ?3 due bound,
// ?4 now, ?5 limit, and the queues as the placeholders in queues (from ?6).
func claimSQL(queues string) string {
	return `
		UPDATE {{p}}jobs
		SET lease_owner = ?1, lease_expires = ?2, claims = claims + 1, fence = fence + 1
		WHERE id IN (
		    SELECT id FROM {{p}}jobs
		    WHERE queue IN (` + queues + `) AND run_at <= ?3
		      AND (lease_expires IS NULL OR lease_expires < ?4)
		    ORDER BY run_at, id
		    LIMIT ?5)
		RETURNING id, run_id, command, claims, fence, run_at,
		    (SELECT r.definition_hash FROM {{p}}runs r WHERE r.id = {{p}}jobs.run_id)`
}

// Claim leases due jobs in one statement. One writer at a time makes two
// claims exclusive without SKIP LOCKED. RETURNING has no order, so the
// claims are sorted by run_at, then ID.
func (b sqliteBackend) Claim(ctx context.Context, req hostcore.ClaimRequest) ([]hostcore.Claimed, error) {
	at := now()
	in, queues := placeholders(6, req.Queues)
	args := append([]any{
		req.Owner, fmtTime(at.Add(req.Lease)), fmtTime(dueBy(at, req)), fmtTime(at), req.Limit,
	}, queues...)
	rows, err := b.s.query(ctx, b.s.db, claimSQL(in), args...)
	if err != nil {
		return nil, err
	}
	claims, err := scanClaims(rows, at)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(claims, func(a, b claimRow) int {
		return cmp.Or(strings.Compare(a.runAt, b.runAt), cmp.Compare(a.JobID, b.JobID))
	})
	out := make([]hostcore.Claimed, len(claims))
	for i, c := range claims {
		out[i] = c.Claimed
	}
	return out, nil
}

func (b sqliteBackend) ClaimKey(
	ctx context.Context, ref durable.JobRef, req hostcore.ClaimRequest,
) (*hostcore.Claimed, error) {
	at := now()
	in, queues := placeholders(7, req.Queues)
	args := append([]any{
		ref.RunID, string(ref.StepID), hostcore.JobKind(ref), fmtTime(at), req.Owner, fmtTime(at.Add(req.Lease)),
	}, queues...)
	rows, err := b.s.query(ctx, b.s.db, `
		UPDATE {{p}}jobs
		SET lease_owner = ?5, lease_expires = ?6, claims = claims + 1, fence = fence + 1
		WHERE run_id = ?1 AND step_id = ?2 AND kind = ?3 AND queue IN (`+in+`)
		  AND run_at <= ?4 AND (lease_expires IS NULL OR lease_expires < ?4)
		RETURNING id, run_id, command, claims, fence, run_at,
		    (SELECT r.definition_hash FROM {{p}}runs r WHERE r.id = {{p}}jobs.run_id)`, args...)
	if err != nil {
		return nil, err
	}
	claims, err := scanClaims(rows, at)
	if err != nil || len(claims) == 0 {
		return nil, err
	}
	return &claims[0].Claimed, nil
}

func (b sqliteBackend) JobStatus(ctx context.Context, ref durable.JobRef) (hostcore.JobStatus, error) {
	var (
		j            hostcore.JobStatus
		runAt        string
		leaseExpires sql.NullString
	)
	err := b.s.queryRow(ctx, b.s.db, `
		SELECT queue, run_at, lease_expires FROM {{p}}jobs
		WHERE run_id = ?1 AND step_id = ?2 AND kind = ?3`,
		ref.RunID, string(ref.StepID), hostcore.JobKind(ref)).Scan(&j.Queue, &runAt, &leaseExpires)
	if errors.Is(err, sql.ErrNoRows) {
		return hostcore.JobStatus{}, nil
	}
	if err != nil {
		return hostcore.JobStatus{}, err
	}
	j.Found, j.Now = true, now()
	if j.RunAt, err = parseTime(runAt); err != nil {
		return hostcore.JobStatus{}, err
	}
	if leaseExpires.Valid {
		if j.LeaseExpires, err = parseTime(leaseExpires.String); err != nil {
			return hostcore.JobStatus{}, err
		}
	}
	return j, nil
}

func (sqliteBackend) Now(context.Context) (time.Time, error) { return now(), nil }

// Renew, Release and Postpone run the write and rule 1's check in one
// transaction, under the write lock.
func (b sqliteBackend) Renew(ctx context.Context, l hostcore.Lease, d time.Duration) error {
	return b.fenced(ctx, l, `UPDATE {{p}}jobs SET lease_expires = ?3 WHERE id = ?1 AND fence = ?2`,
		fmtTime(now().Add(d)))
}

func (b sqliteBackend) Release(ctx context.Context, l hostcore.Lease, refund bool) error {
	refunded := 0
	if refund {
		refunded = 1
	}
	return b.fenced(ctx, l, `
		UPDATE {{p}}jobs SET lease_owner = NULL, lease_expires = NULL, claims = claims - ?3
		WHERE id = ?1 AND fence = ?2`, refunded)
}

func (b sqliteBackend) Postpone(ctx context.Context, l hostcore.Lease, d time.Duration) error {
	return b.fenced(ctx, l, `
		UPDATE {{p}}jobs SET lease_owner = NULL, lease_expires = NULL, run_at = ?3
		WHERE id = ?1 AND fence = ?2`, fmtTime(now().Add(d)))
}

func (b sqliteBackend) fenced(ctx context.Context, l hostcore.Lease, query string, args ...any) error {
	return b.Write(ctx, func(htx hostcore.Tx) error {
		return htx.(*sqliteTx).fenced(ctx, l, query, args...)
	})
}

// Unstorable is false: the core's storage rules (storable, the NUL checks)
// refuse what Postgres would, and SQLite refuses nothing more.
func (sqliteBackend) Unstorable(error) bool { return false }

func (b sqliteBackend) DefinitionDoc(ctx context.Context, hash string) ([]byte, error) {
	return b.s.definitionDoc(ctx, b.s.db, hash)
}

func (s *Store) definitionDoc(ctx context.Context, q querier, hash string) ([]byte, error) {
	var doc string
	err := s.queryRow(ctx, q, `SELECT document FROM {{p}}definitions WHERE hash = ?1`, hash).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(doc), nil
}

func (b sqliteBackend) GetRun(ctx context.Context, runID string) (*durable.Run, error) {
	var (
		r                       durable.Run
		status, state, metadata string
		created, updated        string
		finished                sql.NullString
	)
	err := b.s.queryRow(ctx, b.s.db, `
		SELECT id, workflow, definition_hash, status, state, metadata, created_at, updated_at, finished_at
		FROM {{p}}runs WHERE id = ?1`, runID).Scan(
		&r.ID, &r.Workflow, &r.DefinitionHash, &status, &state, &metadata, &created, &updated, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Status = workflow.RunStatus(status)
	if err := json.Unmarshal([]byte(state), &r.State); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(metadata), &r.Metadata); err != nil {
		return nil, err
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if r.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, err
	}
	if r.FinishedAt, err = optTime(finished); err != nil {
		return nil, err
	}
	return &r, nil
}

func optTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// optJSON decodes a nullable JSON column into v, leaving v alone for NULL.
func optJSON(s sql.NullString, v any) error {
	if !s.Valid {
		return nil
	}
	return json.Unmarshal([]byte(s.String), v)
}

func (b sqliteBackend) Steps(ctx context.Context, runID string) ([]durable.StepRecord, error) {
	rows, err := b.s.query(ctx, b.s.db, `
		SELECT step_id, step, kind, item_index, attempt, status, params, item, output, error,
		       not_before, dispatched_at, settled_at
		FROM {{p}}steps WHERE run_id = ?1 ORDER BY dispatched_at, step_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []durable.StepRecord
	for rows.Next() {
		var (
			r                             durable.StepRecord
			id, status, dispatched        string
			params, item, output, stepErr sql.NullString
			notBefore, settled            sql.NullString
		)
		if err := rows.Scan(&id, &r.Step, &r.StepKind, &r.Index, &r.Attempt, &status,
			&params, &item, &output, &stepErr, &notBefore, &dispatched, &settled); err != nil {
			return nil, err
		}
		r.StepID, r.Status = workflow.StepID(id), durable.StepStatus(status)
		for _, c := range []struct {
			s sql.NullString
			v any
		}{{params, &r.Params}, {item, &r.Item}, {output, &r.Output}, {stepErr, &r.Error}} {
			if err := optJSON(c.s, c.v); err != nil {
				return nil, err
			}
		}
		if r.DispatchedAt, err = parseTime(dispatched); err != nil {
			return nil, err
		}
		if r.NotBefore, err = optTime(notBefore); err != nil {
			return nil, err
		}
		if r.SettledAt, err = optTime(settled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (b sqliteBackend) Events(ctx context.Context, runID string) ([]workflow.Event, error) {
	rows, err := b.s.query(ctx, b.s.db,
		`SELECT event FROM {{p}}events WHERE run_id = ?1 ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []workflow.Event
	for rows.Next() {
		var (
			data string
			ev   workflow.Event
		)
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (b sqliteBackend) QueueStats(ctx context.Context) ([]durable.QueueStat, error) {
	at := fmtTime(now())
	rows, err := b.s.query(ctx, b.s.db, `
		SELECT queue,
		       count(CASE WHEN due THEN 1 END),
		       min(CASE WHEN due THEN run_at END)
		FROM (SELECT queue, run_at,
		             run_at <= ?1 AND (lease_expires IS NULL OR lease_expires < ?1) AS due
		      FROM {{p}}jobs)
		GROUP BY queue ORDER BY queue`, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := []durable.QueueStat{}
	t := now()
	for rows.Next() {
		var (
			q      durable.QueueStat
			queue  string
			oldest sql.NullString
		)
		if err := rows.Scan(&queue, &q.Due, &oldest); err != nil {
			return nil, err
		}
		if oldest.Valid {
			o, err := parseTime(oldest.String)
			if err != nil {
				return nil, err
			}
			q.OldestDue = max(t.Sub(o), 0)
		}
		if kind, ok := strings.CutPrefix(queue, "step:"); ok {
			q.Kind, q.StepKind = workflow.CommandDispatchStep, kind
		} else {
			q.Kind = workflow.CommandScheduleTimer
		}
		stats = append(stats, q)
	}
	return stats, rows.Err()
}

// sqliteTx is a hostcore.Tx on a transaction: the store's own (own), or
// the caller's SQLTx.
type sqliteTx struct {
	s     *Store
	tx    durable.SQLTx
	own   bool // the store opened tx: it must write before it reads
	wrote bool // a write has run in tx
}

// callerTx wraps the application's transaction. Its first write may come
// after the application's own reads; the docs say to open it with
// _txlock=immediate.
func (s *Store) callerTx(tx SQLTx) *sqliteTx { return &sqliteTx{s: s, tx: tx} }

func (t *sqliteTx) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	t.wrote = true
	return t.s.exec(ctx, t.tx, query, args...)
}

// writeRow runs a write with RETURNING and reads its first row.
func (t *sqliteTx) writeRow(ctx context.Context, query string, args ...any) row {
	t.wrote = true
	return t.s.queryRow(ctx, t.tx, query, args...)
}

// readRow runs a read. In the store's own transaction it fails before the
// first write, which would read without the write lock.
func (t *sqliteTx) readRow(ctx context.Context, query string, args ...any) row {
	if t.own && !t.wrote {
		return row{err: errReadFirst}
	}
	return t.s.queryRow(ctx, t.tx, query, args...)
}

// fenced runs a write on job l.JobID at fence l.Fence (?1 and ?2) and,
// when it changes no row, says why (rule 1 of the storage contract). The
// check runs after the write, under the same write lock.
func (t *sqliteTx) fenced(ctx context.Context, l hostcore.Lease, query string, args ...any) error {
	res, err := t.exec(ctx, query, append([]any{l.JobID, l.Fence}, args...)...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		return nil
	}
	var exists bool
	err = t.readRow(ctx, `SELECT EXISTS (SELECT 1 FROM {{p}}jobs WHERE id = ?1)`, l.JobID).Scan(&exists)
	if err == nil && !exists {
		return hostcore.ErrJobGone
	}
	return hostcore.ErrLeaseLost
}

// LockRun takes the write lock and reads the run in one statement: an
// UPDATE that changes nothing, with RETURNING.
func (t *sqliteTx) LockRun(ctx context.Context, runID string) ([]byte, string, error) {
	var state, hash string
	err := t.writeRow(ctx, `
		UPDATE {{p}}runs SET event_seq = event_seq WHERE id = ?1
		RETURNING state, definition_hash`, runID).Scan(&state, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrRunNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return []byte(state), hash, nil
}

func (t *sqliteTx) DefinitionDoc(ctx context.Context, hash string) ([]byte, error) {
	var doc string
	err := t.readRow(ctx, `SELECT document FROM {{p}}definitions WHERE hash = ?1`, hash).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(doc), nil
}

func (t *sqliteTx) InsertDefinition(ctx context.Context, hash, name string, doc []byte) error {
	_, err := t.exec(ctx, `
		INSERT INTO {{p}}definitions (hash, name, document, created_at) VALUES (?1, ?2, ?3, ?4)
		ON CONFLICT (hash) DO NOTHING`, hash, name, string(doc), fmtTime(now()))
	return err
}

func (t *sqliteTx) InsertRun(ctx context.Context, r hostcore.RunRow) (bool, error) {
	res, err := t.exec(ctx, `
		INSERT INTO {{p}}runs (id, definition_hash, workflow, status, state, error, outputs, metadata,
		                       event_seq, created_at, updated_at, finished_at)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, 1, ?9, ?9, ?10)
		ON CONFLICT (id) DO NOTHING`,
		r.ID, r.DefinitionHash, r.Workflow, string(r.Status), string(r.State),
		nullText(r.Error), nullText(r.Outputs), string(r.Metadata), fmtTime(r.At), nullTime(r.FinishedAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (t *sqliteTx) UpdateRun(ctx context.Context, runID string, u hostcore.RunUpdate) (int64, error) {
	var seq int64
	err := t.writeRow(ctx, `
		UPDATE {{p}}runs
		SET state = ?2, status = ?3, error = ?4, outputs = ?5, finished_at = ?6,
		    updated_at = ?7, event_seq = event_seq + 1
		WHERE id = ?1
		RETURNING event_seq`,
		runID, string(u.State), string(u.Status), nullText(u.Error), nullText(u.Outputs),
		nullTime(u.FinishedAt), fmtTime(u.At)).Scan(&seq)
	return seq, err
}

func (t *sqliteTx) InsertEvent(ctx context.Context, runID string, e hostcore.EventRow) error {
	_, err := t.exec(ctx, `
		INSERT INTO {{p}}events (run_id, seq, kind, step_id, at, event) VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		runID, e.Seq, string(e.Kind), nullString(string(e.StepID)), fmtTime(e.At), string(e.Event))
	return err
}

func (t *sqliteTx) InsertStep(ctx context.Context, runID string, s hostcore.StepRow) error {
	_, err := t.exec(ctx, `
		INSERT INTO {{p}}steps
		    (run_id, step_id, step, kind, item_index, attempt, status, params, item, not_before, dispatched_at)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'dispatched', ?7, ?8, ?9, ?10)
		ON CONFLICT (run_id, step_id) DO NOTHING`,
		runID, string(s.StepID), s.Step, s.Kind, s.Index, s.Attempt,
		nullText(s.Params), nullText(s.Item), nullTime(s.NotBefore), fmtTime(s.DispatchedAt))
	return err
}

func (t *sqliteTx) SettleStep(ctx context.Context, runID string, s hostcore.StepSettle) error {
	_, err := t.exec(ctx, `
		UPDATE {{p}}steps SET status = ?3, output = ?4, error = ?5, settled_at = ?6
		WHERE run_id = ?1 AND step_id = ?2 AND status = 'dispatched'`,
		runID, string(s.StepID), string(s.Status), nullText(s.Output), nullText(s.Error), fmtTime(s.At))
	return err
}

func (t *sqliteTx) CancelSteps(ctx context.Context, runID string, stepID workflow.StepID, at time.Time) error {
	if stepID == "" {
		_, err := t.exec(ctx, `
			UPDATE {{p}}steps SET status = 'cancelled', settled_at = ?2
			WHERE run_id = ?1 AND status = 'dispatched'`, runID, fmtTime(at))
		return err
	}
	_, err := t.exec(ctx, `
		UPDATE {{p}}steps SET status = 'cancelled', settled_at = ?3
		WHERE run_id = ?1 AND step_id = ?2 AND status = 'dispatched'`,
		runID, string(stepID), fmtTime(at))
	return err
}

func (t *sqliteTx) InsertJob(ctx context.Context, runID string, j hostcore.JobRow) error {
	at := now()
	runAt := j.RunAt
	if runAt.IsZero() {
		runAt = at
	}
	_, err := t.exec(ctx, `
		INSERT INTO {{p}}jobs (run_id, step_id, kind, step_kind, command, run_at, created_at)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)
		ON CONFLICT (run_id, step_id, kind) DO NOTHING`,
		runID, string(j.StepID), j.Kind, j.StepKind, string(j.Command), fmtTime(runAt), fmtTime(at))
	return err
}

func (t *sqliteTx) DeleteJobs(ctx context.Context, runID string, stepID workflow.StepID) error {
	if stepID == "" {
		_, err := t.exec(ctx, `DELETE FROM {{p}}jobs WHERE run_id = ?1`, runID)
		return err
	}
	_, err := t.exec(ctx, `DELETE FROM {{p}}jobs WHERE run_id = ?1 AND step_id = ?2`,
		runID, string(stepID))
	return err
}

func (t *sqliteTx) DeleteJob(ctx context.Context, l hostcore.Lease) error {
	return t.fenced(ctx, l, `DELETE FROM {{p}}jobs WHERE id = ?1 AND fence = ?2`)
}

// JobsWritten wakes this store's workers at once, before commit. A worker
// that wakes runs its claim, which waits on the write lock until this
// transaction ends, then sees the new jobs. Other processes see them at
// their next Poll.
func (t *sqliteTx) JobsWritten(context.Context, string) error {
	t.s.hub.signal()
	return nil
}

// StatusChanged does nothing: SQLite has no notifications. Use the finish
// hook.
func (*sqliteTx) StatusChanged(context.Context, string, workflow.RunStatus) error { return nil }

func (t *sqliteTx) OnFinished(ctx context.Context, f durable.Finished) error {
	if t.s.onFinishedSQL == nil {
		return nil
	}
	return t.s.onFinishedSQL(ctx, t.tx, f)
}
