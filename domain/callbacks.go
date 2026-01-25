package domain

import "time"

// Callbacks defines the callback interface for engine-level events.
// This allows users to integrate metrics (Prometheus, OpenTelemetry) without
// adding hard dependencies to the library.
type Callbacks interface {
	// OnExecutionSubmitted is called when a new execution is submitted to the engine.
	OnExecutionSubmitted(id string, workflowName string)

	// OnExecutionStarted is called when an execution begins running.
	OnExecutionStarted(id string)

	// OnExecutionCompleted is called when an execution finishes (success or failure).
	OnExecutionCompleted(id string, duration time.Duration, err error)
}

// BaseCallbacks provides a default implementation that does nothing.
// Embed this in your own callbacks to get a default implementation.
type BaseCallbacks struct{}

func (b *BaseCallbacks) OnExecutionSubmitted(id string, workflowName string) {
	// noop
}

func (b *BaseCallbacks) OnExecutionStarted(id string) {
	// noop
}

func (b *BaseCallbacks) OnExecutionCompleted(id string, duration time.Duration, err error) {
	// noop
}
