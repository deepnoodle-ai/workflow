package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/deepnoodle-ai/workflow"
)

// LogActivity implements workflow.ActivityLogger.
func (s *Store) LogActivity(ctx context.Context, entry *workflow.ActivityLogEntry) error {
	if entry == nil {
		return fmt.Errorf("postgres: nil activity log entry")
	}
	params, err := json.Marshal(entry.Parameters)
	if err != nil {
		return fmt.Errorf("postgres: marshal activity parameters: %w", err)
	}
	var result []byte
	if entry.Result != nil {
		b, err := json.Marshal(entry.Result)
		if err != nil {
			return fmt.Errorf("postgres: marshal activity result: %w", err)
		}
		result = b
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO workflow_activity_log (
			id, execution_id, activity, step_name, branch_id,
			parameters, result, error, start_time, duration
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`,
		entry.ID,
		entry.ExecutionID,
		entry.Activity,
		entry.StepName,
		entry.BranchID,
		params,
		result,
		entry.Error,
		entry.StartTime,
		entry.Duration,
	)
	if err != nil {
		return fmt.Errorf("postgres: insert activity log %s: %w", entry.ID, err)
	}
	return nil
}

// GetActivityHistory implements workflow.ActivityLogger.
func (s *Store) GetActivityHistory(ctx context.Context, executionID string) ([]*workflow.ActivityLogEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, execution_id, activity, step_name, branch_id,
		       parameters, result, error, start_time, duration
		FROM workflow_activity_log
		WHERE execution_id = $1
		ORDER BY start_time ASC
	`, executionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: query activity log %s: %w", executionID, err)
	}
	defer rows.Close()

	var out []*workflow.ActivityLogEntry
	for rows.Next() {
		var (
			entry     workflow.ActivityLogEntry
			paramsRaw []byte
			resultRaw []byte
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
			&entry.StartTime,
			&entry.Duration,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan activity log: %w", err)
		}
		if len(paramsRaw) > 0 {
			if err := json.Unmarshal(paramsRaw, &entry.Parameters); err != nil {
				return nil, fmt.Errorf("postgres: unmarshal activity params: %w", err)
			}
		}
		if len(resultRaw) > 0 {
			var v any
			if err := json.Unmarshal(resultRaw, &v); err != nil {
				return nil, fmt.Errorf("postgres: unmarshal activity result: %w", err)
			}
			entry.Result = v
		}
		out = append(out, &entry)
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	return out, nil
}
