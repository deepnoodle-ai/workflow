// Package runquery defines the backend-neutral read API for
// workflow runs. Operational UIs and dashboards consume this
// package and any QueueStore implementation that satisfies
// runquery.Store — avoiding a direct import of a concrete store
// package such as experimental/store/postgres.
//
// The types here are intentionally minimal and stdlib-only: a
// row shape (Run), a filter (RunFilter), a keyset cursor
// (RunCursor), two sentinel errors, and the Store interface
// itself. Concrete store packages implement runquery.Store on
// their *Store type.
package runquery

import (
	"context"
	"errors"
	"time"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// ErrRunNotFound is returned by Store.GetRun and Store.DeleteRun
// when no row matches the requested ID (within the given org
// scope).
var ErrRunNotFound = errors.New("runquery: run not found")

// ErrCannotDeleteRunning is returned by Store.DeleteRun when
// asked to delete a run whose status is worker.StatusRunning.
// Callers must cancel or wait for the run to complete before
// deleting it.
var ErrCannotDeleteRunning = errors.New("runquery: cannot delete a running run")

// Run is a read-side view of a workflow_runs row, suitable for
// operational dashboards and run management surfaces. It mirrors
// the queue-side worker.NewRun / worker.Claim but also exposes
// the lifecycle columns (status, attempt, timestamps) that only
// exist on persisted runs.
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

// RunFilter narrows a ListRuns or CountRuns query. Zero-value
// fields are ignored. Filter values are ANDed together.
type RunFilter struct {
	// WorkflowType matches exactly when non-empty.
	WorkflowType string

	// Status matches exactly when non-empty.
	Status worker.Status

	// ProjectID matches exactly when non-empty.
	ProjectID string

	// ParentRunID matches exactly when non-empty.
	ParentRunID string

	// InitiatedBy matches exactly when non-empty.
	InitiatedBy string

	// Metadata is evaluated with map containment: every key in
	// the filter must be present with an equal value on the row.
	// An empty map matches all rows.
	Metadata map[string]string

	// Cursor is the pagination anchor. Nil means "start from the
	// newest row."
	Cursor *RunCursor

	// Limit caps the number of rows returned. Zero means the
	// store's default page size.
	Limit int
}

// RunCursor is an opaque keyset pagination anchor. Consumers
// typically base64-encode it for API transport and decode back
// to this shape on the next call.
type RunCursor struct {
	CreatedAt time.Time
	ID        string
}

// Store is the backend-neutral read API for workflow runs.
// Implemented by concrete store packages (e.g., postgres).
//
// The orgID parameter scopes multi-tenant queries. Empty string
// matches rows with NULL org_id (single-tenant deployments);
// non-empty matches rows with that exact org_id.
type Store interface {
	// GetRun returns a single run by ID.
	GetRun(ctx context.Context, orgID, id string) (*Run, error)

	// ListRuns returns a page of runs matching filter, ordered
	// newest-first, along with a cursor for the next page. The
	// returned cursor is nil when no more rows exist.
	ListRuns(ctx context.Context, orgID string, filter RunFilter) ([]*Run, *RunCursor, error)

	// CountRuns returns the total number of rows matching filter.
	// The cursor field on filter is ignored.
	CountRuns(ctx context.Context, orgID string, filter RunFilter) (int, error)

	// DeleteRun removes a run row by ID. Running runs cannot be
	// deleted; ErrCannotDeleteRunning is returned in that case.
	DeleteRun(ctx context.Context, orgID, id string) error
}
