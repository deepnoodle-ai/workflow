package script

import (
	"context"
	"errors"
)

// ErrNoScriptCompiler is returned by NoopCompiler.Compile when a workflow
// tries to evaluate a script expression, template, or condition without a
// scripting engine configured.
var ErrNoScriptCompiler = errors.New(
	"scripting not configured: import " +
		"github.com/deepnoodle-ai/workflow/scripts/risor or " +
		"github.com/deepnoodle-ai/workflow/scripts/expr and set " +
		"ExecutionOptions.ScriptCompiler",
)

// NoopCompiler is a Compiler that returns ErrNoScriptCompiler on Compile.
// It is the default when ExecutionOptions.ScriptCompiler is nil, letting
// workflows that do not use conditions, templates, or script expressions
// run without pulling in a scripting engine dependency.
type NoopCompiler struct{}

func (NoopCompiler) Compile(ctx context.Context, code string) (Script, error) {
	return nil, ErrNoScriptCompiler
}
