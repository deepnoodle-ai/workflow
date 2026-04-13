package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
	"github.com/deepnoodle-ai/workflow/experimental/worker/runquery"
)

// ErrRunNotFound is an alias for runquery.ErrRunNotFound so existing
// callers comparing against postgres.ErrRunNotFound keep working.
// New code should use runquery.ErrRunNotFound directly.
var ErrRunNotFound = runquery.ErrRunNotFound

// ErrCannotDeleteRunning is an alias for
// runquery.ErrCannotDeleteRunning.
var ErrCannotDeleteRunning = runquery.ErrCannotDeleteRunning

// Run aliases runquery.Run so callers can write postgres.Run
// during the transition. New code should use runquery.Run.
type Run = runquery.Run

// RunFilter aliases runquery.RunFilter.
type RunFilter = runquery.RunFilter

// RunCursor aliases runquery.RunCursor.
type RunCursor = runquery.RunCursor

// Compile-time check that *Store implements the read-side contract.
var _ runquery.Store = (*Store)(nil)

// runProjection is the column list used by GetRun and ListRuns. It
// must stay in lock-step with the Scan order in scanRun — adding or
// reordering a column requires touching both.
const runProjection = `id,
	COALESCE(org_id, ''), COALESCE(project_id, ''), COALESCE(parent_run_id, ''),
	workflow_type, status, credit_cost, COALESCE(initiated_by, ''), callback_url,
	spec, result, error_message, metadata,
	attempt, claimed_by,
	heartbeat_at, created_at, started_at, completed_at`

// GetRun returns a single run by ID, scoped to orgID. An empty
// orgID matches rows with NULL org_id (single-tenant). Returns
// runquery.ErrRunNotFound when no matching row exists.
func (s *Store) GetRun(ctx context.Context, orgID, id string) (*runquery.Run, error) {
	if id == "" {
		return nil, fmt.Errorf("postgres: GetRun: id is required")
	}
	var where string
	args := []any{id}
	if orgID == "" {
		where = "id = $1 AND org_id IS NULL"
	} else {
		where = "id = $1 AND org_id = $2"
		args = append(args, orgID)
	}
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s`,
		runProjection, s.t("workflow_runs"), where)
	row := s.pool.QueryRow(ctx, query, args...)
	r, err := scanRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, runquery.ErrRunNotFound
		}
		return nil, fmt.Errorf("postgres: get run %s: %w", id, err)
	}
	return r, nil
}

// ListRuns returns runs matching filter, ordered newest-first with
// keyset pagination. Returns the rows and a cursor to pass back on
// the next call; the cursor is nil when no more rows exist.
//
// orgID == "" lists runs with NULL org_id (single-tenant). Pass a
// real org ID for scoped B2B listings.
func (s *Store) ListRuns(ctx context.Context, orgID string, filter runquery.RunFilter) ([]*runquery.Run, *runquery.RunCursor, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	conds, args := s.runFilterConds(orgID, filter)
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	// Fetch limit+1 so we can decide whether there is a next page
	// without a second query.
	args = append(args, limit+1)
	query := fmt.Sprintf(`SELECT %s FROM %s %s ORDER BY created_at DESC, id DESC LIMIT $%d`,
		runProjection, s.t("workflow_runs"), where, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: list runs: %w", err)
	}
	defer rows.Close()

	out := make([]*runquery.Run, 0, limit)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("postgres: scan run: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var cursor *runquery.RunCursor
	if len(out) > limit {
		last := out[limit-1]
		cursor = &runquery.RunCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		out = out[:limit]
	}
	return out, cursor, nil
}

// CountRuns returns the total number of rows matching filter. The
// cursor field on filter is ignored: counts are over the entire
// filtered set, not a single page.
func (s *Store) CountRuns(ctx context.Context, orgID string, filter runquery.RunFilter) (int, error) {
	// Counts ignore the cursor.
	filter.Cursor = nil
	filter.Limit = 0
	conds, args := s.runFilterConds(orgID, filter)
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s %s`, s.t("workflow_runs"), where)
	var n int
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: count runs: %w", err)
	}
	return n, nil
}

// DeleteRun removes a run row by ID. Running runs cannot be
// deleted; the caller must cancel or wait for the run first.
//
// Implemented as a single DELETE ... RETURNING status so the check
// and the delete happen atomically. If RETURNING yields no row, we
// fall back to a cheap existence probe to distinguish "running"
// from "not found."
func (s *Store) DeleteRun(ctx context.Context, orgID, id string) error {
	if id == "" {
		return fmt.Errorf("postgres: DeleteRun: id is required")
	}
	var (
		delQuery string
		delArgs  []any
	)
	if orgID == "" {
		delQuery = fmt.Sprintf(`
			DELETE FROM %s
			WHERE id = $1 AND org_id IS NULL AND status <> $2
			RETURNING status
		`, s.t("workflow_runs"))
		delArgs = []any{id, string(worker.StatusRunning)}
	} else {
		delQuery = fmt.Sprintf(`
			DELETE FROM %s
			WHERE id = $1 AND org_id = $2 AND status <> $3
			RETURNING status
		`, s.t("workflow_runs"))
		delArgs = []any{id, orgID, string(worker.StatusRunning)}
	}
	var deletedStatus string
	err := s.pool.QueryRow(ctx, delQuery, delArgs...).Scan(&deletedStatus)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("postgres: delete run %s: %w", id, err)
	}
	// DELETE affected zero rows: either the run is missing or it is
	// running. Probe once to pick the right sentinel.
	var (
		probeQuery string
		probeArgs  []any
	)
	if orgID == "" {
		probeQuery = fmt.Sprintf(`SELECT status FROM %s WHERE id = $1 AND org_id IS NULL`, s.t("workflow_runs"))
		probeArgs = []any{id}
	} else {
		probeQuery = fmt.Sprintf(`SELECT status FROM %s WHERE id = $1 AND org_id = $2`, s.t("workflow_runs"))
		probeArgs = []any{id, orgID}
	}
	var status string
	if err := s.pool.QueryRow(ctx, probeQuery, probeArgs...).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return runquery.ErrRunNotFound
		}
		return fmt.Errorf("postgres: delete run %s: probe: %w", id, err)
	}
	if status == string(worker.StatusRunning) {
		return runquery.ErrCannotDeleteRunning
	}
	// Race: row existed and was not running when we probed, but the
	// DELETE missed it. It must have transitioned out from under us;
	// treat as not found.
	return runquery.ErrRunNotFound
}

// runFilterConds builds the WHERE clause for ListRuns / CountRuns.
// Returns the list of conditions and the corresponding positional
// args, starting at $1.
func (s *Store) runFilterConds(orgID string, f runquery.RunFilter) ([]string, []any) {
	var (
		conds []string
		args  []any
	)
	next := func() string {
		args = append(args, nil)
		return fmt.Sprintf("$%d", len(args))
	}
	setLast := func(v any) { args[len(args)-1] = v }

	if orgID == "" {
		conds = append(conds, "org_id IS NULL")
	} else {
		p := next()
		setLast(orgID)
		conds = append(conds, "org_id = "+p)
	}
	if f.WorkflowType != "" {
		p := next()
		setLast(f.WorkflowType)
		conds = append(conds, "workflow_type = "+p)
	}
	if f.Status != "" {
		p := next()
		setLast(string(f.Status))
		conds = append(conds, "status = "+p)
	}
	if f.ProjectID != "" {
		p := next()
		setLast(f.ProjectID)
		conds = append(conds, "project_id = "+p)
	}
	if f.ParentRunID != "" {
		p := next()
		setLast(f.ParentRunID)
		conds = append(conds, "parent_run_id = "+p)
	}
	if f.InitiatedBy != "" {
		p := next()
		setLast(f.InitiatedBy)
		conds = append(conds, "initiated_by = "+p)
	}
	if len(f.Metadata) > 0 {
		p := next()
		// JSONB containment match
		metaJSON, _ := marshalMetadata(f.Metadata)
		setLast(metaJSON)
		conds = append(conds, "metadata @> "+p+"::jsonb")
	}
	if f.Cursor != nil && !f.Cursor.CreatedAt.IsZero() {
		p1 := next()
		setLast(f.Cursor.CreatedAt)
		p2 := next()
		setLast(f.Cursor.ID)
		conds = append(conds, fmt.Sprintf("(created_at, id) < (%s, %s)", p1, p2))
	}
	return conds, args
}

// rowScanner is the subset of pgx.Row / pgx.Rows that scanRun needs.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(r rowScanner) (*runquery.Run, error) {
	var (
		run         runquery.Run
		metadataRaw []byte
		heartbeatAt *time.Time
		startedAt   *time.Time
		completedAt *time.Time
	)
	if err := r.Scan(
		&run.ID,
		&run.OrgID,
		&run.ProjectID,
		&run.ParentRunID,
		&run.WorkflowType,
		&run.Status,
		&run.CreditCost,
		&run.InitiatedBy,
		&run.CallbackURL,
		&run.Spec,
		&run.Result,
		&run.ErrorMessage,
		&metadataRaw,
		&run.Attempt,
		&run.ClaimedBy,
		&heartbeatAt,
		&run.CreatedAt,
		&startedAt,
		&completedAt,
	); err != nil {
		return nil, err
	}
	run.HeartbeatAt = heartbeatAt
	run.StartedAt = startedAt
	run.CompletedAt = completedAt
	meta, err := unmarshalMetadata(metadataRaw)
	if err != nil {
		return nil, err
	}
	run.Metadata = meta
	return &run, nil
}
