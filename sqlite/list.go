package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
)

// ListRuns returns one page of the runs f selects, sorted by start time,
// newest first, then by ID. Pass page.Next as f.Cursor to read the next
// page; it is empty after the last. No run repeats, and a run that
// matches the filter for the whole listing is not skipped. Each page
// applies the filter when it is read, so a run that changes status while
// you page may be missed or may appear on a later page.
//
// UpdatedAfter selects runs; it does not sort them. A run that started
// yesterday and failed a minute ago comes after the runs that started
// today. A summary has no state: read it with GetRun.
func (s *Store) ListRuns(ctx context.Context, f RunFilter) (RunPage, error) {
	return s.core.ListRuns(ctx, s.backend, f)
}

// DeleteEndedRuns deletes the runs that ended (completed, failed or
// cancelled) before before, with their steps, events and jobs, and returns
// how many it deleted. It never deletes a run that has not ended. It
// deletes 500 runs per transaction; when ctx ends, it stops between
// transactions and returns the count so far with the error.
//
// Call it from your own schedule, such as a daily job; the store runs no
// retention of its own. A deleted run frees its ID: Start with that ID
// starts a new run, so retention also ends Start's duplicate check for
// those IDs. Definitions are kept.
func (s *Store) DeleteEndedRuns(ctx context.Context, before time.Time) (int, error) {
	return s.core.DeleteEndedRuns(ctx, s.backend, before)
}

func (b sqliteBackend) ListRuns(ctx context.Context, q hostcore.RunQuery) ([]RunSummary, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("?%d", len(args))
	}

	if len(q.Status) > 0 {
		ph := make([]string, len(q.Status))
		for i, st := range q.Status {
			ph[i] = arg(st)
		}
		where = append(where, "status IN ("+strings.Join(ph, ", ")+")")
	}
	keys := make([]string, 0, len(q.Metadata))
	for k := range q.Metadata {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		// json_each takes the key as a value, so no JSON path is built;
		// type 'text' keeps a number or a bool from matching.
		where = append(where, "EXISTS (SELECT 1 FROM json_each(metadata) WHERE key = "+
			arg(k)+" AND type = 'text' AND value = "+arg(q.Metadata[k])+")")
	}
	if !q.CreatedAfter.IsZero() {
		where = append(where, "created_at > "+arg(fmtTime(q.CreatedAfter)))
	}
	if !q.CreatedBefore.IsZero() {
		where = append(where, "created_at < "+arg(fmtTime(q.CreatedBefore)))
	}
	if !q.UpdatedAfter.IsZero() {
		where = append(where, "updated_at > "+arg(fmtTime(q.UpdatedAfter)))
	}
	if q.After != nil {
		where = append(where,
			"(created_at, id) < ("+arg(fmtTime(q.After.CreatedAt))+", "+arg(q.After.ID)+")")
	}

	query := `
		SELECT id, workflow, definition_hash, status, metadata, error,
		       created_at, updated_at, finished_at
		FROM {{p}}runs`
	if len(where) > 0 {
		query += "\n\t\tWHERE " + strings.Join(where, " AND ")
	}
	query += "\n\t\tORDER BY created_at DESC, id DESC LIMIT " + arg(q.Limit)

	rows, err := b.s.query(ctx, b.s.db, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunSummary
	for rows.Next() {
		var (
			r                RunSummary
			status, metadata string
			created, updated string
			runErr, finished sql.NullString
		)
		err := rows.Scan(&r.ID, &r.Workflow, &r.DefinitionHash, &status, &metadata, &runErr,
			&created, &updated, &finished)
		if err != nil {
			return nil, err
		}
		r.Status = workflow.RunStatus(status)
		if err := json.Unmarshal([]byte(metadata), &r.Metadata); err != nil {
			return nil, err
		}
		if runErr.Valid {
			if err := json.Unmarshal([]byte(runErr.String), &r.Error); err != nil {
				return nil, err
			}
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
		out = append(out, r)
	}
	return out, rows.Err()
}

// endedSel selects a batch of ended runs: ?1 the cutoff, ?2 the limit.
// The (status, updated_at) index serves it, and ORDER BY makes it total,
// so each statement of one transaction selects the same runs. The
// statuses are those workflow.RunStatus.Terminal accepts; change both
// together.
const endedSel = `
		SELECT id FROM {{p}}runs
		WHERE status IN ('completed', 'failed', 'cancelled')
		  AND updated_at < ?1 AND finished_at < ?1
		ORDER BY updated_at, id
		LIMIT ?2`

// DeleteEndedRuns deletes a batch in one transaction. SQLite does not
// enforce the schema's foreign keys here, so it deletes each table itself,
// children first. Its first statement is a write, so the transaction takes
// the write lock before it reads.
func (b sqliteBackend) DeleteEndedRuns(ctx context.Context, before time.Time, limit int) (int, error) {
	tx, err := b.s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }() // after Commit, a no-op

	cutoff := fmtTime(before)
	for _, table := range []string{"events", "steps", "jobs"} {
		q := `DELETE FROM {{p}}` + table + ` WHERE run_id IN (` + endedSel + `)`
		if _, err := b.s.exec(ctx, tx, q, cutoff, limit); err != nil {
			return 0, err
		}
	}
	res, err := b.s.exec(ctx, tx, `DELETE FROM {{p}}runs WHERE id IN (`+endedSel+`)`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}
