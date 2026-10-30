package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/jackc/pgx/v5"
)

// ListRuns returns one page of the runs f selects, sorted by start time,
// newest first, then by ID. Pass page.Next as f.Cursor to read the next
// page; it is empty after the last. A run that starts while you page
// comes before the first page, so no run repeats or is skipped.
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
// transactions and returns the count so far with the error. A run that
// another transaction holds is left for the next call.
//
// Call it from your own schedule, such as a daily job; the store runs no
// retention of its own. A deleted run frees its ID: Start with that ID
// starts a new run, so retention also ends Start's duplicate check for
// those IDs. Definitions are kept.
func (s *Store) DeleteEndedRuns(ctx context.Context, before time.Time) (int, error) {
	return s.core.DeleteEndedRuns(ctx, s.backend, before)
}

func (b pgBackend) ListRuns(ctx context.Context, q hostcore.RunQuery) ([]RunSummary, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if len(q.Status) > 0 {
		where = append(where, "status = ANY("+arg(q.Status)+"::text[])")
	}
	if len(q.Metadata) > 0 {
		meta, err := json.Marshal(q.Metadata)
		if err != nil {
			return nil, err
		}
		where = append(where, "metadata @> "+arg(string(meta))+"::jsonb")
	}
	if !q.CreatedAfter.IsZero() {
		where = append(where, "created_at > "+arg(q.CreatedAfter))
	}
	if !q.CreatedBefore.IsZero() {
		where = append(where, "created_at < "+arg(q.CreatedBefore))
	}
	if !q.UpdatedAfter.IsZero() {
		where = append(where, "updated_at > "+arg(q.UpdatedAfter))
	}
	if q.After != nil {
		where = append(where,
			"(created_at, id) < ("+arg(q.After.CreatedAt)+"::timestamptz, "+arg(q.After.ID)+")")
	}

	query := `
		SELECT id, workflow, definition_hash, status, metadata, error,
		       created_at, updated_at, finished_at
		FROM {{schema}}.runs`
	if len(where) > 0 {
		query += "\n\t\tWHERE " + strings.Join(where, " AND ")
	}
	query += "\n\t\tORDER BY created_at DESC, id DESC LIMIT " + arg(q.Limit)

	rows, err := b.s.pool.Query(ctx, b.s.sql(query), args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RunSummary, error) {
		var (
			r      RunSummary
			status string
		)
		err := row.Scan(&r.ID, &r.Workflow, &r.DefinitionHash, &status, &r.Metadata, &r.Error,
			&r.CreatedAt, &r.UpdatedAt, &r.FinishedAt)
		r.Status = workflow.RunStatus(status)
		return r, err
	})
}

// deleteEndedSQL deletes a batch of ended runs; ON DELETE CASCADE deletes
// their steps, events and jobs. The selection has no ORDER BY, so a scan
// stops at the limit instead of sorting every match. = ANY(ARRAY(...))
// deletes by primary key; IN (...) would join against a scan of the whole
// table. SKIP LOCKED passes over a run another transaction holds. The
// statuses are those workflow.RunStatus.Terminal accepts; change both
// together.
const deleteEndedSQL = `
		DELETE FROM {{schema}}.runs WHERE id = ANY(ARRAY(
		    SELECT id FROM {{schema}}.runs
		    WHERE status IN ('completed', 'failed', 'cancelled')
		      AND updated_at < $1 AND finished_at < $1
		    LIMIT $2
		    FOR UPDATE SKIP LOCKED))`

func (b pgBackend) DeleteEndedRuns(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := b.s.pool.Exec(ctx, b.s.sql(deleteEndedSQL), before, limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
