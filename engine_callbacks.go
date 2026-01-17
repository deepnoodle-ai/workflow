package workflow

import "time"

// EngineCallbacks defines the callback interface for engine-level events.
// This allows users to integrate metrics (Prometheus, OpenTelemetry) without
// adding hard dependencies to the library.
type EngineCallbacks interface {
	// OnExecutionSubmitted is called when a new execution is submitted to the engine.
	OnExecutionSubmitted(id string, workflowName string)

	// OnExecutionStarted is called when an execution begins running.
	OnExecutionStarted(id string)

	// OnExecutionCompleted is called when an execution finishes (success or failure).
	OnExecutionCompleted(id string, duration time.Duration, err error)
}

// BaseEngineCallbacks provides a default implementation that does nothing.
// Embed this in your own callbacks to get a default implementation.
type BaseEngineCallbacks struct{}

func (b *BaseEngineCallbacks) OnExecutionSubmitted(id string, workflowName string) {
	// noop
}

func (b *BaseEngineCallbacks) OnExecutionStarted(id string) {
	// noop
}

func (b *BaseEngineCallbacks) OnExecutionCompleted(id string, duration time.Duration, err error) {
	// noop
}
