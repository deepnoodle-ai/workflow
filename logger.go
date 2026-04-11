package workflow

import (
	"log/slog"
	"os"
)

// NewJSONLogger returns a logger that writes to stdout in JSON format.
// It is provided as a convenience for simple consumers; any *slog.Logger
// passed via ExecutionOptions.Logger will be respected by the engine.
func NewJSONLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}
