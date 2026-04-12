package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// ErrRunNotFound is returned by GetRun and DeleteRun when no row
// matches the requested ID (within the given org scope).
var ErrRunNotFound = errors.New("postgres: run not found")

// ErrCannotDeleteRunning is returned by DeleteRun when asked to
// delete a run whose status is StatusRunning. Callers must cancel
// or wait for the run to complete before deleting it.
var ErrCannotDeleteRunning = errors.New("postgres: cannot delete a running run")

// Run is a read-side view of a workflow_runs row. Returned from
// GetRun and ListRuns for operational dashboards and run management.
type Run struct {
	ID           string
	OrgID        string
	ProjectID    string
	ParentRunID  string
	WorkflowType string
	Status       worker.Status
	CreditCost   int
	InitiatedBy  string
	CallbackURL  string
	Spec         []byte
	Result       []byte
	ErrorMessage string
	Metadata     map[string]string
	Attempt      int
	ClaimedBy    string
	HeartbeatAt  *time.Time
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
}

// RunFilter narrows a ListRuns or CountRuns query. Zero-value fields
// are ignored. Filter values are ANDed together.
type RunFilter struct {
	// WorkflowType matches exactly when non-empty.
	WorkflowType string

	// Status matches exactly when non-empty.
	Status worker.Status

	// ProjectID matches exactly when non-empty. Empty means "any
	// project, including NULL."
	ProjectID string

	// ParentRunID matches exactly when non-empty.
	ParentRunID string

	// InitiatedBy matches exactly when non-empty.
	InitiatedBy string

	// Metadata is evaluated with JSONB containment (@>). Empty map
	// matches all rows.
	Metadata map[string]string

	// Cursor is the pagination anchor. Nil means "start from the
	// newest row."
	Cursor *RunCursor

	// Limit caps the number of rows returned. Zero means 50.
	Limit int
}

// RunCursor is an opaque keyset pagination anchor. Consumers
// typically base64-encode it for API transport and decode back to
// this shape on the next call.
type RunCursor struct {
	CreatedAt time.Time
	ID        string
}

// GetRun returns a single run by ID, scoped to orgID. An empty
// orgID matches rows with NULL org_id (single-tenant). Returns
// ErrRunNotFound when no matching row exists.
func (s *Store) GetRun(ctx context.Context, orgID, id string) (*Run, error) {
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
	query := fmt.Sprintf(`
		SELECT id,
		       COALESCE(org_id, ''), COALESCE(project_id, ''), COALESCE(parent_run_id, ''),
		       workflow_type, status, credit_cost, COALESCE(initiated_by, ''), callback_url,
		       spec, result, error_message, metadata,
		       attempt, claimed_by,
		       heartbeat_at, created_at, started_at, completed_at
		FROM %s
		WHERE %s
	`, s.t("workflow_runs"), where)
	row := s.pool.QueryRow(ctx, query, args...)
	r, err := scanRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRunNotFound
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
func (s *Store) ListRuns(ctx context.Context, orgID string, filter RunFilter) ([]*Run, *RunCursor, error) {
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
	query := fmt.Sprintf(`
		SELECT id,
		       COALESCE(org_id, ''), COALESCE(project_id, ''), COALESCE(parent_run_id, ''),
		       workflow_type, status, credit_cost, COALESCE(initiated_by, ''), callback_url,
		       spec, result, error_message, metadata,
		       attempt, claimed_by,
		       heartbeat_at, created_at, started_at, completed_at
		FROM %s
		%s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d
	`, s.t("workflow_runs"), where, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: list runs: %w", err)
	}
	defer rows.Close()

	out := make([]*Run, 0, limit)
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

	var cursor *RunCursor
	if len(out) > limit {
		last := out[limit-1]
		cursor = &RunCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		out = out[:limit]
	}
	return out, cursor, nil
}

// CountRuns returns the total number of rows matching filter. The
// cursor field on filter is ignored: counts are over the entire
// filtered set, not a single page.
func (s *Store) CountRuns(ctx context.Context, orgID string, filter RunFilter) (int, error) {
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
func (s *Store) DeleteRun(ctx context.Context, orgID, id string) error {
	if id == "" {
		return fmt.Errorf("postgres: DeleteRun: id is required")
	}
	var (
		checkQuery string
		checkArgs  []any
	)
	if orgID == "" {
		checkQuery = fmt.Sprintf(`SELECT status FROM %s WHERE id = $1 AND org_id IS NULL`, s.t("workflow_runs"))
		checkArgs = []any{id}
	} else {
		checkQuery = fmt.Sprintf(`SELECT status FROM %s WHERE id = $1 AND org_id = $2`, s.t("workflow_runs"))
		checkArgs = []any{id, orgID}
	}
	var status string
	if err := s.pool.QueryRow(ctx, checkQuery, checkArgs...).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("postgres: delete run %s: check: %w", id, err)
	}
	if status == string(worker.StatusRunning) {
		return ErrCannotDeleteRunning
	}
	delQuery := fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, s.t("workflow_runs"))
	if _, err := s.pool.Exec(ctx, delQuery, id); err != nil {
		return fmt.Errorf("postgres: delete run %s: %w", id, err)
	}
	return nil
}

// runFilterConds builds the WHERE clause for ListRuns / CountRuns.
// Returns the list of conditions and the corresponding positional
// args, starting at $1.
func (s *Store) runFilterConds(orgID string, f RunFilter) ([]string, []any) {
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

func scanRun(r rowScanner) (*Run, error) {
	var (
		run         Run
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
