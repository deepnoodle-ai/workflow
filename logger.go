package workflow

import (
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
)

// NewLogger returns a logger that writes to stdout with colorized output if
// stdout is a terminal.
func NewLogger() *slog.Logger {
	return slog.New(tint.NewHandler(os.Stdout, &tint.Options{
		Level:      slog.LevelInfo,
		TimeFormat: time.RFC3339,
		NoColor:    !isatty.IsTerminal(os.Stdout.Fd()),
	}))
}

// NewJSONLogger returns a logger that writes to stdout in JSON format.
func NewJSONLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}
