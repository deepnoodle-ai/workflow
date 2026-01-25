package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deepnoodle-ai/workflow/domain"
)

// ActivityLogger logs activities to files.
type ActivityLogger struct {
	directory string
}

// NewActivityLogger creates a new file-based activity logger.
func NewActivityLogger(directory string) *ActivityLogger {
	return &ActivityLogger{directory: directory}
}

func (l *ActivityLogger) executionActivityLogPath(executionID string) string {
	return filepath.Join(l.directory, fmt.Sprintf("%s.jsonl", executionID))
}

// GetActivityHistory returns the activity history for an execution.
func (l *ActivityLogger) GetActivityHistory(ctx context.Context, executionID string) ([]*domain.ActivityLogEntry, error) {
	filePath := l.executionActivityLogPath(executionID)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var entries []*domain.ActivityLogEntry
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var entry domain.ActivityLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, &entry)
	}
	return entries, nil
}

// LogActivity logs an activity entry.
func (l *ActivityLogger) LogActivity(ctx context.Context, entry *domain.ActivityLogEntry) error {
	jsonData, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	filePath := l.executionActivityLogPath(entry.ExecutionID)
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write([]byte(string(jsonData) + "\n")); err != nil {
		return err
	}
	return f.Sync()
}

// Verify interface compliance.
var _ domain.ActivityLogger = (*ActivityLogger)(nil)
