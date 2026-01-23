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
	db     *sql.DB
	config StoreConfig
}

// PostgresStoreOptions contains configuration for PostgresStore.
type PostgresStoreOptions struct {
	DB     *sql.DB
	Config StoreConfig
}

// NewPostgresStore creates a new PostgresStore.
func NewPostgresStore(opts PostgresStoreOptions) *PostgresStore {
	config := opts.Config
	if config.HeartbeatInterval == 0 {
		config = DefaultStoreConfig()
	}
	return &PostgresStore{
		db:     opts.DB,
		config: config,
	}
}

// CreateSchema creates the database tables and indexes.
func (s *PostgresStore) CreateSchema(ctx context.Context) error {
	schema := `
		-- Workflow executions
		CREATE TABLE IF NOT EXISTS workflow_executions (
			id             TEXT PRIMARY KEY,
			workflow_name  TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'pending',
			inputs         JSONB NOT NULL,
			outputs        JSONB,
			current_step   TEXT,
			last_error     TEXT,
			checkpoint_id  TEXT,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			started_at     TIMESTAMPTZ,
			completed_at   TIMESTAMPTZ
		);

		CREATE INDEX IF NOT EXISTS idx_executions_status ON workflow_executions(status);
		CREATE INDEX IF NOT EXISTS idx_executions_workflow ON workflow_executions(workflow_name);

		-- Tasks (work items for workers)
		CREATE TABLE IF NOT EXISTS workflow_tasks (
			id              TEXT PRIMARY KEY,
			execution_id    TEXT NOT NULL REFERENCES workflow_executions(id),
			step_name       TEXT NOT NULL,
			activity_name   TEXT NOT NULL,
			attempt         INTEGER NOT NULL DEFAULT 1,
			status          TEXT NOT NULL DEFAULT 'pending',
			spec            JSONB NOT NULL,
			worker_id       TEXT,
			visible_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_heartbeat  TIMESTAMPTZ,
			result          JSONB,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			started_at      TIMESTAMPTZ,
			completed_at    TIMESTAMPTZ,
			UNIQUE(execution_id, step_name, attempt)
		);

		CREATE INDEX IF NOT EXISTS idx_tasks_claimable ON workflow_tasks(visible_at)
			WHERE status = 'pending';
		CREATE INDEX IF NOT EXISTS idx_tasks_stale ON workflow_tasks(last_heartbeat)
			WHERE status = 'running';
		CREATE INDEX IF NOT EXISTS idx_tasks_execution ON workflow_tasks(execution_id);
	`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// CreateExecution persists a new execution record.
func (s *PostgresStore) CreateExecution(ctx context.Context, record *ExecutionRecord) error {
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
			id, workflow_name, status, inputs, outputs,
			current_step, last_error, checkpoint_id,
			created_at, started_at, completed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		record.ID,
		record.WorkflowName,
		record.Status,
		inputsJSON,
		outputsJSON,
		nullString(record.CurrentStep),
		nullString(record.LastError),
		nullString(record.CheckpointID),
		record.CreatedAt,
		nullTime(record.StartedAt),
		nullTime(record.CompletedAt),
	)
	return err
}

// GetExecution retrieves an execution by ID.
func (s *PostgresStore) GetExecution(ctx context.Context, id string) (*ExecutionRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, workflow_name, status, inputs, outputs,
			   current_step, last_error, checkpoint_id,
			   created_at, started_at, completed_at
		FROM workflow_executions
		WHERE id = $1
	`, id)

	return s.scanExecution(row)
}

// UpdateExecution updates an existing execution record.
func (s *PostgresStore) UpdateExecution(ctx context.Context, record *ExecutionRecord) error {
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
			current_step = $6,
			last_error = $7,
			checkpoint_id = $8,
			started_at = $9,
			completed_at = $10
		WHERE id = $1
	`,
		record.ID,
		record.WorkflowName,
		record.Status,
		inputsJSON,
		outputsJSON,
		nullString(record.CurrentStep),
		nullString(record.LastError),
		nullString(record.CheckpointID),
		nullTime(record.StartedAt),
		nullTime(record.CompletedAt),
	)
	return err
}

// ListExecutions returns executions matching the filter.
func (s *PostgresStore) ListExecutions(ctx context.Context, filter ExecutionFilter) ([]*ExecutionRecord, error) {
	query := `
		SELECT id, workflow_name, status, inputs, outputs,
			   current_step, last_error, checkpoint_id,
			   created_at, started_at, completed_at
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
		for i, st := range filter.Statuses {
			statuses[i] = string(st)
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
		record, err := s.scanExecutionRows(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// CreateTask creates a new task.
func (s *PostgresStore) CreateTask(ctx context.Context, task *TaskRecord) error {
	specJSON, err := json.Marshal(task.Spec)
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}

	var resultJSON sql.NullString
	if task.Result != nil {
		data, err := json.Marshal(task.Result)
		if err != nil {
			return fmt.Errorf("marshal result: %w", err)
		}
		resultJSON = sql.NullString{String: string(data), Valid: true}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflow_tasks (
			id, execution_id, step_name, activity_name, attempt, status,
			spec, worker_id, visible_at, last_heartbeat,
			result, created_at, started_at, completed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		task.ID,
		task.ExecutionID,
		task.StepName,
		task.ActivityName,
		task.Attempt,
		task.Status,
		specJSON,
		nullString(task.WorkerID),
		task.VisibleAt,
		nullTime(task.LastHeartbeat),
		resultJSON,
		task.CreatedAt,
		nullTime(task.StartedAt),
		nullTime(task.CompletedAt),
	)
	return err
}

// ClaimTask atomically claims the next available task.
func (s *PostgresStore) ClaimTask(ctx context.Context, workerID string) (*ClaimedTask, error) {
	leaseInterval := fmt.Sprintf("%d seconds", int(s.config.LeaseTimeout.Seconds()))

	var task ClaimedTask
	var specJSON []byte

	err := s.db.QueryRowContext(ctx, `
		UPDATE workflow_tasks
		SET status = 'running',
			worker_id = $1,
			started_at = NOW(),
			last_heartbeat = NOW()
		WHERE id = (
			SELECT id FROM workflow_tasks
			WHERE status = 'pending' AND visible_at <= NOW()
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, execution_id, step_name, activity_name, attempt, spec
	`, workerID).Scan(&task.ID, &task.ExecutionID, &task.StepName, &task.ActivityName, &task.Attempt, &specJSON)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim task: %w", err)
	}

	if err := json.Unmarshal(specJSON, &task.Spec); err != nil {
		return nil, fmt.Errorf("unmarshal spec: %w", err)
	}

	task.HeartbeatInterval = s.config.HeartbeatInterval
	_ = leaseInterval // Used in query above
	task.LeaseExpiresAt = time.Now().Add(s.config.LeaseTimeout)

	return &task, nil
}

// CompleteTask marks a task as completed.
func (s *PostgresStore) CompleteTask(ctx context.Context, taskID string, workerID string, result *TaskResult) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	status := TaskStatusCompleted
	if !result.Success {
		status = TaskStatusFailed
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE workflow_tasks
		SET status = $3,
			result = $4,
			completed_at = NOW()
		WHERE id = $1 AND worker_id = $2 AND status = 'running'
	`, taskID, workerID, status, resultJSON)
	if err != nil {
		return err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("task %s not found or not owned by worker %s", taskID, workerID)
	}
	return nil
}

// ReleaseTask returns a task to pending state for retry.
func (s *PostgresStore) ReleaseTask(ctx context.Context, taskID string, workerID string, retryAfter time.Duration) error {
	delayInterval := fmt.Sprintf("%d seconds", int(retryAfter.Seconds()))

	res, err := s.db.ExecContext(ctx, `
		UPDATE workflow_tasks
		SET status = 'pending',
			worker_id = NULL,
			visible_at = NOW() + $3::interval,
			attempt = attempt + 1,
			last_heartbeat = NULL,
			started_at = NULL
		WHERE id = $1 AND worker_id = $2
	`, taskID, workerID, delayInterval)
	if err != nil {
		return err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("task %s not found or not owned by worker %s", taskID, workerID)
	}
	return nil
}

// HeartbeatTask updates the heartbeat timestamp.
func (s *PostgresStore) HeartbeatTask(ctx context.Context, taskID string, workerID string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE workflow_tasks
		SET last_heartbeat = NOW()
		WHERE id = $1 AND worker_id = $2 AND status = 'running'
	`, taskID, workerID)
	if err != nil {
		return err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("task %s not found or not owned by worker %s", taskID, workerID)
	}
	return nil
}

// GetTask retrieves a task by ID.
func (s *PostgresStore) GetTask(ctx context.Context, id string) (*TaskRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, execution_id, step_name, activity_name, attempt, status,
			   spec, worker_id, visible_at, last_heartbeat,
			   result, created_at, started_at, completed_at
		FROM workflow_tasks
		WHERE id = $1
	`, id)

	return s.scanTask(row)
}

// ListStaleTasks returns tasks that haven't heartbeated since the cutoff.
func (s *PostgresStore) ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*TaskRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, execution_id, step_name, activity_name, attempt, status,
			   spec, worker_id, visible_at, last_heartbeat,
			   result, created_at, started_at, completed_at
		FROM workflow_tasks
		WHERE status = 'running' AND last_heartbeat < $1
	`, heartbeatCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*TaskRecord
	for rows.Next() {
		task, err := s.scanTaskRows(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// ResetTask resets a task to pending state for recovery.
func (s *PostgresStore) ResetTask(ctx context.Context, taskID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_tasks
		SET status = 'pending',
			worker_id = NULL,
			visible_at = NOW(),
			attempt = attempt + 1,
			last_heartbeat = NULL,
			started_at = NULL
		WHERE id = $1
	`, taskID)
	return err
}

// scanExecution scans a single row into an ExecutionRecord.
func (s *PostgresStore) scanExecution(row *sql.Row) (*ExecutionRecord, error) {
	var record ExecutionRecord
	var inputsJSON, outputsJSON []byte
	var currentStep, lastError, checkpointID sql.NullString
	var startedAt, completedAt sql.NullTime

	err := row.Scan(
		&record.ID,
		&record.WorkflowName,
		&record.Status,
		&inputsJSON,
		&outputsJSON,
		&currentStep,
		&lastError,
		&checkpointID,
		&record.CreatedAt,
		&startedAt,
		&completedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("execution not found")
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

	record.CurrentStep = currentStep.String
	record.LastError = lastError.String
	record.CheckpointID = checkpointID.String
	record.StartedAt = startedAt.Time
	record.CompletedAt = completedAt.Time

	return &record, nil
}

// scanExecutionRows scans a row from *sql.Rows into an ExecutionRecord.
func (s *PostgresStore) scanExecutionRows(rows *sql.Rows) (*ExecutionRecord, error) {
	var record ExecutionRecord
	var inputsJSON, outputsJSON []byte
	var currentStep, lastError, checkpointID sql.NullString
	var startedAt, completedAt sql.NullTime

	err := rows.Scan(
		&record.ID,
		&record.WorkflowName,
		&record.Status,
		&inputsJSON,
		&outputsJSON,
		&currentStep,
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

	record.CurrentStep = currentStep.String
	record.LastError = lastError.String
	record.CheckpointID = checkpointID.String
	record.StartedAt = startedAt.Time
	record.CompletedAt = completedAt.Time

	return &record, nil
}

// scanTask scans a single row into a TaskRecord.
func (s *PostgresStore) scanTask(row *sql.Row) (*TaskRecord, error) {
	var task TaskRecord
	var specJSON, resultJSON []byte
	var workerID sql.NullString
	var lastHeartbeat, startedAt, completedAt sql.NullTime

	err := row.Scan(
		&task.ID,
		&task.ExecutionID,
		&task.StepName,
		&task.ActivityName,
		&task.Attempt,
		&task.Status,
		&specJSON,
		&workerID,
		&task.VisibleAt,
		&lastHeartbeat,
		&resultJSON,
		&task.CreatedAt,
		&startedAt,
		&completedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task not found")
		}
		return nil, err
	}

	if err := json.Unmarshal(specJSON, &task.Spec); err != nil {
		return nil, fmt.Errorf("unmarshal spec: %w", err)
	}
	if resultJSON != nil {
		if err := json.Unmarshal(resultJSON, &task.Result); err != nil {
			return nil, fmt.Errorf("unmarshal result: %w", err)
		}
	}

	task.WorkerID = workerID.String
	task.LastHeartbeat = lastHeartbeat.Time
	task.StartedAt = startedAt.Time
	task.CompletedAt = completedAt.Time

	return &task, nil
}

// scanTaskRows scans a row from *sql.Rows into a TaskRecord.
func (s *PostgresStore) scanTaskRows(rows *sql.Rows) (*TaskRecord, error) {
	var task TaskRecord
	var specJSON, resultJSON []byte
	var workerID sql.NullString
	var lastHeartbeat, startedAt, completedAt sql.NullTime

	err := rows.Scan(
		&task.ID,
		&task.ExecutionID,
		&task.StepName,
		&task.ActivityName,
		&task.Attempt,
		&task.Status,
		&specJSON,
		&workerID,
		&task.VisibleAt,
		&lastHeartbeat,
		&resultJSON,
		&task.CreatedAt,
		&startedAt,
		&completedAt,
	)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(specJSON, &task.Spec); err != nil {
		return nil, fmt.Errorf("unmarshal spec: %w", err)
	}
	if resultJSON != nil {
		if err := json.Unmarshal(resultJSON, &task.Result); err != nil {
			return nil, fmt.Errorf("unmarshal result: %w", err)
		}
	}

	task.WorkerID = workerID.String
	task.LastHeartbeat = lastHeartbeat.Time
	task.StartedAt = startedAt.Time
	task.CompletedAt = completedAt.Time

	return &task, nil
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
