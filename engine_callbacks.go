package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/engine"
)

// Engine callbacks - re-exported from internal/engine for backwards compatibility.
//
// Callbacks allow integration with metrics systems (Prometheus, OpenTelemetry)
// and observability tools without adding hard dependencies to the library.
// Implement EngineCallbacks to receive notifications about execution lifecycle
// events such as submission, start, and completion.
//
// For new code, prefer importing internal/engine directly.

// EngineCallbacks defines the callback interface for engine-level events.
type EngineCallbacks = engine.Callbacks

// BaseEngineCallbacks provides a default implementation that does nothing.
type BaseEngineCallbacks = engine.BaseCallbacks
