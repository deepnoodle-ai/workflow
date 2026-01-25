package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/engine"
)

// Engine callbacks provide integration points for metrics systems (Prometheus,
// OpenTelemetry) and observability tools without adding hard dependencies.
// Implement EngineCallbacks to receive notifications about execution lifecycle
// events such as submission, start, and completion.

// EngineCallbacks defines the callback interface for engine-level events.
type EngineCallbacks = engine.Callbacks

// BaseEngineCallbacks provides a default implementation that does nothing.
type BaseEngineCallbacks = engine.BaseCallbacks
