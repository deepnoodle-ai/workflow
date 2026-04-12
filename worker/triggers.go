package worker

import (
	"context"
	"time"
)

// TriggerStatus is the processing status of a workflow trigger.
type TriggerStatus string

const (
	TriggerPending    TriggerStatus = "pending"
	TriggerProcessing TriggerStatus = "processing"
	TriggerCompleted  TriggerStatus = "completed"
	TriggerFailed     TriggerStatus = "failed"
)

// Trigger represents a pending child workflow to enqueue, written
// via the transactional outbox pattern. The worker persists triggers
// returned in Outcome.Triggers and processes them asynchronously.
type Trigger struct {
	ID           string
	ParentRunID  string
	ChildSpec    NewRun
	Status       TriggerStatus
	Attempts     int
	ErrorMessage string
	ChildRunID   string
	CreatedAt    time.Time
	ProcessedAt  time.Time
}

// TriggerStore persists and processes workflow triggers using the
// transactional outbox pattern. Method names are prefixed to avoid
// collisions when a single store struct implements multiple interfaces.
type TriggerStore interface {
	InsertTriggers(ctx context.Context, triggers []Trigger) error
	ListPendingTriggers(ctx context.Context, limit int) ([]Trigger, error)
	MarkTriggerProcessing(ctx context.Context, id string) error
	MarkTriggerCompleted(ctx context.Context, id string, childRunID string) error
	IncrementTriggerAttempts(ctx context.Context, id string, errMsg string) error
	MarkTriggerFailed(ctx context.Context, id string, errMsg string) error
}
