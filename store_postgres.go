package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// PostgresStore implements ExecutionStore using PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

// PostgresStoreOptions contains configuration for PostgresStore.
type PostgresStoreOptions struct {
	DB *sql.DB
}

// NewPostgresStore creates a new PostgresStore.
func NewPostgresStore(opts PostgresStoreOptions) *PostgresStore {
	return &PostgresStore{
		db: opts.DB,
	}
}

// CreateSchema creates the workflow_executions table and indexes.
// This should be called during application setup.
func (s *PostgresStore) CreateSchema(ctx context.Context) error {
	schema := `
		CREATE TABLE IF NOT EXISTS workflow_executions (
			id             TEXT PRIMARY KEY,
			workflow_name  TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'pending',
			inputs         JSONB NOT NULL,
			outputs        JSONB,
			attempt        INTEGER NOT NULL DEFAULT 1,
			worker_id      TEXT,
			last_heartbeat TIMESTAMPTZ,
			dispatched_at  TIMESTAMPTZ,
			last_error     TEXT,
			checkpoint_id  TEXT,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			started_at     TIMESTAMPTZ,
			completed_at   TIMESTAMPTZ
		);

		CREATE INDEX IF NOT EXISTS idx_executions_status ON workflow_executions(status);
		CREATE INDEX IF NOT EXISTS idx_executions_workflow ON workflow_executions(workflow_name);
		CREATE INDEX IF NOT EXISTS idx_executions_stale_running ON workflow_executions(last_heartbeat)
			WHERE status = 'running';
		CREATE INDEX IF NOT EXISTS idx_executions_stale_pending ON workflow_executions(dispatched_at)
			WHERE status = 'pending' AND dispatched_at IS NOT NULL;
	`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Create persists a new execution record.
func (s *PostgresStore) Create(ctx context.Context, record *ExecutionRecord) error {
	inputsJSON, err := json.Marshal(record.Inputs)
	if err != nil {
		return fmt.Errorf("marshal inputs: %w", err)
	}

	var outputsJSON sql.NullString
	if record.Outputs != nil {
		data, err := json.Marshal(record.Outputs)
		if err != nil {
			return fmt.Errorf("marshal outputs: %w", err)
		}
		outputsJSON = sql.NullString{String: string(data), Valid: true}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflow_executions (
			id, workflow_name, status, inputs, outputs, attempt,
			worker_id, last_heartbeat, dispatched_at, last_error,
			checkpoint_id, created_at, started_at, completed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		record.ID,
		record.WorkflowName,
		record.Status,
		inputsJSON,
		outputsJSON,
		record.Attempt,
		nullString(record.WorkerID),
		nullTime(record.LastHeartbeat),
		nullTime(record.DispatchedAt),
		nullString(record.LastError),
		nullString(record.CheckpointID),
		record.CreatedAt,
		nullTime(record.StartedAt),
		nullTime(record.CompletedAt),
	)
	return err
}

// Get retrieves an execution record by ID.
func (s *PostgresStore) Get(ctx context.Context, id string) (*ExecutionRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, workflow_name, status, inputs, outputs, attempt,
			   worker_id, last_heartbeat, dispatched_at, last_error,
			   checkpoint_id, created_at, started_at, completed_at
		FROM workflow_executions
		WHERE id = $1
	`, id)

	return s.scanRecord(row)
}

// List retrieves execution records matching the filter.
func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]*ExecutionRecord, error) {
	query := `
		SELECT id, workflow_name, status, inputs, outputs, attempt,
			   worker_id, last_heartbeat, dispatched_at, last_error,
			   checkpoint_id, created_at, started_at, completed_at
		FROM workflow_executions
		WHERE 1=1
	`
	args := make([]any, 0)
	argIdx := 1

	if filter.WorkflowName != "" {
		query += fmt.Sprintf(" AND workflow_name = $%d", argIdx)
		args = append(args, filter.WorkflowName)
		argIdx++
	}

	if len(filter.Statuses) > 0 {
		query += fmt.Sprintf(" AND status = ANY($%d)", argIdx)
		statuses := make([]string, len(filter.Statuses))
		for i, s := range filter.Statuses {
			statuses[i] = string(s)
		}
		args = append(args, pq.Array(statuses))
		argIdx++
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, filter.Limit)
		argIdx++
	}

	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", argIdx)
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		record, err := s.scanRecordRows(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// ClaimExecution atomically updates status from pending to running if the
// current attempt matches. Returns false if status is not pending or attempt doesn't match.
func (s *PostgresStore) ClaimExecution(ctx context.Context, id string, workerID string, expectedAttempt int) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE workflow_executions
		SET status = 'running',
			worker_id = $2,
			started_at = NOW(),
			last_heartbeat = NOW()
		WHERE id = $1 AND attempt = $3 AND status = 'pending'
	`, id, workerID, expectedAttempt)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// CompleteExecution atomically updates to completed/failed status if the
// attempt matches. Returns false if attempt doesn't match (stale worker).
func (s *PostgresStore) CompleteExecution(ctx context.Context, id string, expectedAttempt int, status EngineExecutionStatus, outputs map[string]any, lastError string) (bool, error) {
	var outputsJSON sql.NullString
	if outputs != nil {
		data, err := json.Marshal(outputs)
		if err != nil {
			return false, fmt.Errorf("marshal outputs: %w", err)
		}
		outputsJSON = sql.NullString{String: string(data), Valid: true}
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE workflow_executions
		SET status = $2,
			outputs = $3,
			last_error = $4,
			completed_at = NOW()
		WHERE id = $1 AND attempt = $5 AND status = 'running'
	`, id, status, outputsJSON, nullString(lastError), expectedAttempt)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// MarkDispatched sets dispatched_at timestamp for dispatch mode tracking.
func (s *PostgresStore) MarkDispatched(ctx context.Context, id string, attempt int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_executions
		SET dispatched_at = NOW()
		WHERE id = $1 AND attempt = $2
	`, id, attempt)
	return err
}

// Heartbeat updates the last_heartbeat timestamp for liveness tracking.
func (s *PostgresStore) Heartbeat(ctx context.Context, id string, workerID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_executions
		SET last_heartbeat = NOW()
		WHERE id = $1 AND worker_id = $2 AND status = 'running'
	`, id, workerID)
	return err
}

// ListStaleRunning returns executions in running state with heartbeat older than cutoff.
func (s *PostgresStore) ListStaleRunning(ctx context.Context, cutoff time.Time) ([]*ExecutionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workflow_name, status, inputs, outputs, attempt,
			   worker_id, last_heartbeat, dispatched_at, last_error,
			   checkpoint_id, created_at, started_at, completed_at
		FROM workflow_executions
		WHERE status = 'running'
		  AND last_heartbeat < $1
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		record, err := s.scanRecordRows(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// ListStalePending returns executions in pending state with dispatched_at older than cutoff.
func (s *PostgresStore) ListStalePending(ctx context.Context, cutoff time.Time) ([]*ExecutionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workflow_name, status, inputs, outputs, attempt,
			   worker_id, last_heartbeat, dispatched_at, last_error,
			   checkpoint_id, created_at, started_at, completed_at
		FROM workflow_executions
		WHERE status = 'pending'
		  AND dispatched_at IS NOT NULL
		  AND dispatched_at < $1
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		record, err := s.scanRecordRows(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// Update updates an execution record.
func (s *PostgresStore) Update(ctx context.Context, record *ExecutionRecord) error {
	inputsJSON, err := json.Marshal(record.Inputs)
	if err != nil {
		return fmt.Errorf("marshal inputs: %w", err)
	}

	var outputsJSON sql.NullString
	if record.Outputs != nil {
		data, err := json.Marshal(record.Outputs)
		if err != nil {
			return fmt.Errorf("marshal outputs: %w", err)
		}
		outputsJSON = sql.NullString{String: string(data), Valid: true}
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE workflow_executions
		SET workflow_name = $2,
			status = $3,
			inputs = $4,
			outputs = $5,
			attempt = $6,
			worker_id = $7,
			last_heartbeat = $8,
			dispatched_at = $9,
			last_error = $10,
			checkpoint_id = $11,
			started_at = $12,
			completed_at = $13
		WHERE id = $1
	`,
		record.ID,
		record.WorkflowName,
		record.Status,
		inputsJSON,
		outputsJSON,
		record.Attempt,
		nullString(record.WorkerID),
		nullTime(record.LastHeartbeat),
		nullTime(record.DispatchedAt),
		nullString(record.LastError),
		nullString(record.CheckpointID),
		nullTime(record.StartedAt),
		nullTime(record.CompletedAt),
	)
	return err
}

// scanRecord scans a single row into an ExecutionRecord.
func (s *PostgresStore) scanRecord(row *sql.Row) (*ExecutionRecord, error) {
	var record ExecutionRecord
	var inputsJSON, outputsJSON []byte
	var workerID, lastError, checkpointID sql.NullString
	var lastHeartbeat, dispatchedAt, startedAt, completedAt sql.NullTime

	err := row.Scan(
		&record.ID,
		&record.WorkflowName,
		&record.Status,
		&inputsJSON,
		&outputsJSON,
		&record.Attempt,
		&workerID,
		&lastHeartbeat,
		&dispatchedAt,
		&lastError,
		&checkpointID,
		&record.CreatedAt,
		&startedAt,
		&completedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(inputsJSON, &record.Inputs); err != nil {
		return nil, fmt.Errorf("unmarshal inputs: %w", err)
	}
	if outputsJSON != nil {
		if err := json.Unmarshal(outputsJSON, &record.Outputs); err != nil {
			return nil, fmt.Errorf("unmarshal outputs: %w", err)
		}
	}

	record.WorkerID = workerID.String
	record.LastHeartbeat = lastHeartbeat.Time
	record.DispatchedAt = dispatchedAt.Time
	record.LastError = lastError.String
	record.CheckpointID = checkpointID.String
	record.StartedAt = startedAt.Time
	record.CompletedAt = completedAt.Time

	return &record, nil
}

// scanRecordRows scans a row from *sql.Rows into an ExecutionRecord.
func (s *PostgresStore) scanRecordRows(rows *sql.Rows) (*ExecutionRecord, error) {
	var record ExecutionRecord
	var inputsJSON, outputsJSON []byte
	var workerID, lastError, checkpointID sql.NullString
	var lastHeartbeat, dispatchedAt, startedAt, completedAt sql.NullTime

	err := rows.Scan(
		&record.ID,
		&record.WorkflowName,
		&record.Status,
		&inputsJSON,
		&outputsJSON,
		&record.Attempt,
		&workerID,
		&lastHeartbeat,
		&dispatchedAt,
		&lastError,
		&checkpointID,
		&record.CreatedAt,
		&startedAt,
		&completedAt,
	)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(inputsJSON, &record.Inputs); err != nil {
		return nil, fmt.Errorf("unmarshal inputs: %w", err)
	}
	if outputsJSON != nil {
		if err := json.Unmarshal(outputsJSON, &record.Outputs); err != nil {
			return nil, fmt.Errorf("unmarshal outputs: %w", err)
		}
	}

	record.WorkerID = workerID.String
	record.LastHeartbeat = lastHeartbeat.Time
	record.DispatchedAt = dispatchedAt.Time
	record.LastError = lastError.String
	record.CheckpointID = checkpointID.String
	record.StartedAt = startedAt.Time
	record.CompletedAt = completedAt.Time

	return &record, nil
}

// nullString converts a string to sql.NullString.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullTime converts a time.Time to sql.NullTime.
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// Verify PostgresStore implements ExecutionStore.
var _ ExecutionStore = (*PostgresStore)(nil)
