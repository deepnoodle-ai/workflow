package workflow

import "context"

// NullActivityLogger is a no-op implementation of ActivityLogger.
type NullActivityLogger struct{}

func NewNullActivityLogger() *NullActivityLogger {
	return &NullActivityLogger{}
}

func (l *NullActivityLogger) LogActivity(ctx context.Context, entry *ActivityLogEntry) error {
	return nil
}

func (l *NullActivityLogger) GetActivityHistory(ctx context.Context, executionID string) ([]*ActivityLogEntry, error) {
	return nil, nil
}
