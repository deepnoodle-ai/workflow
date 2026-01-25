package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/engine"
)

// Engine callbacks - re-exported from internal/engine for backwards compatibility.
// New code should use internal/engine directly.

// EngineCallbacks defines the callback interface for engine-level events.
type EngineCallbacks = engine.Callbacks

// BaseEngineCallbacks provides a default implementation that does nothing.
type BaseEngineCallbacks = engine.BaseCallbacks
