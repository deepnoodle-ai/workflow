package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
)

// LogActivity implements workflow.ActivityLogger.
func (s *Store) LogActivity(ctx context.Context, entry *workflow.ActivityLogEntry) error {
	if entry == nil {
		return fmt.Errorf("sqlite: nil activity log entry")
	}
	params, err := json.Marshal(entry.Parameters)
	if err != nil {
		return fmt.Errorf("sqlite: marshal activity parameters: %w", err)
	}
	var result []byte
	if entry.Result != nil {
		b, err := json.Marshal(entry.Result)
		if err != nil {
			return fmt.Errorf("sqlite: marshal activity result: %w", err)
		}
		result = b
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflow_activity_log (
			id, execution_id, activity, step_name, branch_id,
			parameters, result, error, start_time, duration
		) VALUES (?,?,?,?,?,?,?,?,?,?)
	`,
		entry.ID,
		entry.ExecutionID,
		entry.Activity,
		entry.StepName,
		entry.BranchID,
		params,
		result,
		entry.Error,
		formatTime(entry.StartTime),
		entry.Duration,
	)
	if err != nil {
		return fmt.Errorf("sqlite: insert activity log %s: %w", entry.ID, err)
	}
	return nil
}

// GetActivityHistory implements workflow.ActivityLogger.
func (s *Store) GetActivityHistory(ctx context.Context, executionID string) ([]*workflow.ActivityLogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, execution_id, activity, step_name, branch_id,
		       parameters, result, error, start_time, duration
		FROM workflow_activity_log
		WHERE execution_id = ?
		ORDER BY start_time ASC
	`, executionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: query activity log %s: %w", executionID, err)
	}
	defer rows.Close()

	var out []*workflow.ActivityLogEntry
	for rows.Next() {
		var (
			entry     workflow.ActivityLogEntry
			paramsRaw sql.NullString
			resultRaw sql.NullString
			startTime string
		)
		if err := rows.Scan(
			&entry.ID,
			&entry.ExecutionID,
			&entry.Activity,
			&entry.StepName,
			&entry.BranchID,
			&paramsRaw,
			&resultRaw,
			&entry.Error,
			&startTime,
			&entry.Duration,
		); err != nil {
			return nil, fmt.Errorf("sqlite: scan activity log: %w", err)
		}
		entry.StartTime = parseTime(startTime)
		if paramsRaw.Valid && paramsRaw.String != "" {
			if err := json.Unmarshal([]byte(paramsRaw.String), &entry.Parameters); err != nil {
				return nil, fmt.Errorf("sqlite: unmarshal activity params: %w", err)
			}
		}
		if resultRaw.Valid && resultRaw.String != "" {
			var v any
			if err := json.Unmarshal([]byte(resultRaw.String), &v); err != nil {
				return nil, fmt.Errorf("sqlite: unmarshal activity result: %w", err)
			}
			entry.Result = v
		}
		out = append(out, &entry)
	}
	return out, rows.Err()
}
