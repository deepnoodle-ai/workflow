package worker

import (
	"context"
	"time"
)

// WebhookDelivery represents a pending or completed webhook
// notification for a workflow run.
type WebhookDelivery struct {
	ID          string
	RunID       string
	URL         string
	EventType   string // "workflow.completed", "workflow.failed", etc.
	Payload     []byte
	Status      string // "pending", "delivered", "failed"
	Attempts    int
	LastError   string
	CreatedAt   time.Time
	DeliveredAt time.Time
}

// WebhookStore persists and manages webhook delivery state. Method
// names are prefixed to avoid collisions when a single store struct
// implements multiple interfaces.
type WebhookStore interface {
	EnqueueWebhook(ctx context.Context, delivery *WebhookDelivery) error
	ListPendingWebhooks(ctx context.Context, limit int) ([]*WebhookDelivery, error)
	MarkWebhookDelivered(ctx context.Context, id string) error
	IncrementWebhookAttempts(ctx context.Context, id string, lastError string) error
	MarkWebhookFailed(ctx context.Context, id string, errMsg string) error
}

// WebhookDeliverer performs the actual HTTP delivery of a webhook
// payload. The consumer provides an implementation backed by their
// HTTP client of choice.
type WebhookDeliverer interface {
	Deliver(ctx context.Context, url string, payload []byte) error
}
