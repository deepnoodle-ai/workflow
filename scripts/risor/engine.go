// Package risor provides a Risor-backed implementation of the workflow
// script.Compiler interface. Consumers wire it up via
// ExecutionOptions.ScriptCompiler to enable Risor expressions in workflow
// conditions, templates, and parameter interpolation.
package risor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	risor "github.com/deepnoodle-ai/risor/v2"
	"github.com/deepnoodle-ai/risor/v2/pkg/bytecode"
	"github.com/deepnoodle-ai/risor/v2/pkg/object"
	"github.com/deepnoodle-ai/workflow/script"
)

// Engine is a Risor-backed script.Compiler.
type Engine struct {
	globals map[string]any
}

// NewEngine returns a Risor-backed Engine that compiles source code into
// scripts evaluated against the provided globals. Globals typically include
// the allowed Risor builtins plus workflow-provided "state" and "inputs"
// placeholders; see DefaultGlobals for a sensible baseline.
//
// The provided globals map is shallow-copied so callers can safely mutate
// their own map after construction without affecting the engine.
func NewEngine(globals map[string]any) *Engine {
	copied := make(map[string]any, len(globals))
	for name, value := range globals {
		copied[name] = value
	}
	return &Engine{globals: copied}
}

// Compile implements script.Compiler.
func (e *Engine) Compile(ctx context.Context, code string) (script.Script, error) {
	compiled, err := risor.Compile(ctx, code,
		risor.WithEnv(e.globals),
		risor.WithSyntax(risor.BasicScripting),
	)
	if err != nil {
		return nil, err
	}
	return &compiledScript{engine: e, code: compiled}, nil
}

type compiledScript struct {
	engine *Engine
	code   *bytecode.Code
}

func (s *compiledScript) Evaluate(ctx context.Context, globals map[string]any) (script.Value, error) {
	combined := make(map[string]any, len(s.engine.globals)+len(globals))
	for name, value := range s.engine.globals {
		combined[name] = value
	}
	for name, value := range globals {
		combined[name] = value
	}
	result, err := risor.Run(ctx, s.code,
		risor.WithEnv(combined),
		risor.WithRawResult(),
		risor.WithSyntax(risor.BasicScripting),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate risor script: %w", err)
	}
	return &scriptValue{obj: result.(object.Object)}, nil
}

// scriptValue wraps a Risor object.Object as a script.Value.
type scriptValue struct {
	obj object.Object
}

func (v *scriptValue) Value() any {
	switch o := v.obj.(type) {
	case *object.String:
		return o.Value()
	case *object.Int:
		return o.Value()
	case *object.Float:
		return o.Value()
	case *object.Bool:
		return o.Value()
	case *object.Time:
		return o.Value()
	case *object.NilType:
		return nil
	case *object.List:
		result := make([]any, 0, len(o.Value()))
		for _, item := range o.Value() {
			result = append(result, risorObjectToGo(item))
		}
		return result
	case *object.Map:
		result := make(map[string]any, len(o.Value()))
		for key, value := range o.Value() {
			result[key] = risorObjectToGo(value)
		}
		return result
	default:
		return o.Inspect()
	}
}

func (v *scriptValue) IsTruthy() bool {
	switch o := v.obj.(type) {
	case *object.Bool:
		return o.Value()
	case *object.Int:
		return o.Value() != 0
	case *object.Float:
		return o.Value() != 0.0
	case *object.List:
		return len(o.Value()) > 0
	case *object.Map:
		return len(o.Value()) > 0
	case *object.String:
		val := o.Value()
		return val != "" && strings.ToLower(val) != "false"
	default:
		return o.IsTruthy()
	}
}

func (v *scriptValue) Items() ([]any, error) {
	return script.EachValue(v.Value())
}

func (v *scriptValue) String() string {
	switch o := v.obj.(type) {
	case *object.String:
		return o.Value()
	case *object.Int:
		return fmt.Sprintf("%d", o.Value())
	case *object.Float:
		return fmt.Sprintf("%g", o.Value())
	case *object.Bool:
		return fmt.Sprintf("%t", o.Value())
	case *object.Time:
		return o.Value().Format(time.RFC3339)
	case *object.NilType:
		return ""
	case *object.List:
		items := make([]string, 0, len(o.Value()))
		for _, item := range o.Value() {
			items = append(items, fmt.Sprintf("%v", item))
		}
		return strings.Join(items, "\n\n")
	case *object.Map:
		m := o.Value()
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		items := make([]string, 0, len(keys))
		for _, k := range keys {
			items = append(items, fmt.Sprintf("%s: %v", k, m[k]))
		}
		return strings.Join(items, "\n\n")
	case fmt.Stringer:
		return o.String()
	default:
		return fmt.Sprintf("%v", v.obj)
	}
}
