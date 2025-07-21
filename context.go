package workflow

import (
	"context"
	"log/slog"

	"github.com/deepnoodle-ai/workflow/script"
	"github.com/deepnoodle-ai/workflow/state"
)

type ContextKey string

const (
	LoggerContextKey   ContextKey = "logger"
	StateContextKey    ContextKey = "state"
	CompilerContextKey ContextKey = "compiler"
)

func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, LoggerContextKey, logger)
}

func WithState(ctx context.Context, state state.State) context.Context {
	return context.WithValue(ctx, StateContextKey, state)
}

func WithCompiler(ctx context.Context, compiler script.Compiler) context.Context {
	return context.WithValue(ctx, CompilerContextKey, compiler)
}

func GetLoggerFromContext(ctx context.Context) (*slog.Logger, bool) {
	logger, ok := ctx.Value(LoggerContextKey).(*slog.Logger)
	return logger, ok
}

func GetStateFromContext(ctx context.Context) (state.State, bool) {
	state, ok := ctx.Value(StateContextKey).(state.State)
	return state, ok
}

func GetCompilerFromContext(ctx context.Context) (script.Compiler, bool) {
	compiler, ok := ctx.Value(CompilerContextKey).(script.Compiler)
	return compiler, ok
}
