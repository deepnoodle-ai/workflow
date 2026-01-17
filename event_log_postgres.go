package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
)

// PostgresEventLog implements EventLog using PostgreSQL.
type PostgresEventLog struct {
	db *sql.DB
}

// PostgresEventLogOptions contains configuration for PostgresEventLog.
type PostgresEventLogOptions struct {
	DB *sql.DB
}

// NewPostgresEventLog creates a new PostgresEventLog.
func NewPostgresEventLog(opts PostgresEventLogOptions) *PostgresEventLog {
	return &PostgresEventLog{
		db: opts.DB,
	}
}

// CreateSchema creates the workflow_events table and indexes.
// This should be called during application setup.
func (l *PostgresEventLog) CreateSchema(ctx context.Context) error {
	schema := `
		CREATE TABLE IF NOT EXISTS workflow_events (
			id            TEXT PRIMARY KEY,
			execution_id  TEXT NOT NULL,
			timestamp     TIMESTAMPTZ NOT NULL,
			type          TEXT NOT NULL,
			step_name     TEXT,
			path_id       TEXT,
			attempt       INTEGER,
			data          JSONB,
			error         TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_events_execution ON workflow_events(execution_id, timestamp);
		CREATE INDEX IF NOT EXISTS idx_events_type ON workflow_events(type, timestamp);
	`
	_, err := l.db.ExecContext(ctx, schema)
	return err
}

// Append adds an event to the log.
func (l *PostgresEventLog) Append(ctx context.Context, event Event) error {
	var dataJSON sql.NullString
	if event.Data != nil {
		data, err := json.Marshal(event.Data)
		if err != nil {
			return fmt.Errorf("marshal event data: %w", err)
		}
		dataJSON = sql.NullString{String: string(data), Valid: true}
	}

	_, err := l.db.ExecContext(ctx, `
		INSERT INTO workflow_events (
			id, execution_id, timestamp, type, step_name, path_id, attempt, data, error
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		event.ID,
		event.ExecutionID,
		event.Timestamp,
		event.Type,
		nullString(event.StepName),
		nullString(event.PathID),
		nullInt(event.Attempt),
		dataJSON,
		nullString(event.Error),
	)
	return err
}

// List retrieves events for an execution matching the filter.
func (l *PostgresEventLog) List(ctx context.Context, executionID string, filter EventFilter) ([]Event, error) {
	query := `
		SELECT id, execution_id, timestamp, type, step_name, path_id, attempt, data, error
		FROM workflow_events
		WHERE execution_id = $1
	`
	args := []any{executionID}
	argIdx := 2

	if len(filter.Types) > 0 {
		query += fmt.Sprintf(" AND type = ANY($%d)", argIdx)
		types := make([]string, len(filter.Types))
		for i, t := range filter.Types {
			types[i] = string(t)
		}
		args = append(args, pq.Array(types))
		argIdx++
	}

	if !filter.After.IsZero() {
		query += fmt.Sprintf(" AND timestamp > $%d", argIdx)
		args = append(args, filter.After)
		argIdx++
	}

	if !filter.Before.IsZero() {
		query += fmt.Sprintf(" AND timestamp < $%d", argIdx)
		args = append(args, filter.Before)
		argIdx++
	}

	query += " ORDER BY timestamp ASC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, filter.Limit)
	}

	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var stepName, pathID, errorStr, dataJSON sql.NullString
		var attempt sql.NullInt64

		err := rows.Scan(
			&event.ID,
			&event.ExecutionID,
			&event.Timestamp,
			&event.Type,
			&stepName,
			&pathID,
			&attempt,
			&dataJSON,
			&errorStr,
		)
		if err != nil {
			return nil, err
		}

		event.StepName = stepName.String
		event.PathID = pathID.String
		event.Attempt = int(attempt.Int64)
		event.Error = errorStr.String

		if dataJSON.Valid && dataJSON.String != "" {
			if err := json.Unmarshal([]byte(dataJSON.String), &event.Data); err != nil {
				return nil, fmt.Errorf("unmarshal event data: %w", err)
			}
		}

		events = append(events, event)
	}
	return events, rows.Err()
}

// nullInt converts an int to sql.NullInt64.
func nullInt(i int) sql.NullInt64 {
	if i == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(i), Valid: true}
}

// Verify PostgresEventLog implements EventLog.
var _ EventLog = (*PostgresEventLog)(nil)
