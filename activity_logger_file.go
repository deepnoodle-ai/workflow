package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileActivityLogger logs activities to files.
type FileActivityLogger struct {
	directory string
}

// NewFileActivityLogger creates a new file-based activity logger.
func NewFileActivityLogger(directory string) *FileActivityLogger {
	return &FileActivityLogger{directory: directory}
}

func (l *FileActivityLogger) executionActivityLogPath(executionID string) string {
	return filepath.Join(l.directory, fmt.Sprintf("%s.jsonl", executionID))
}

func (l *FileActivityLogger) GetActivityHistory(ctx context.Context, executionID string) ([]*ActivityLogEntry, error) {
	filePath := l.executionActivityLogPath(executionID)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var entries []*ActivityLogEntry
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var entry ActivityLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, &entry)
	}
	return entries, nil
}

func (l *FileActivityLogger) LogActivity(ctx context.Context, entry *ActivityLogEntry) error {
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
var _ ActivityLogger = (*FileActivityLogger)(nil)
