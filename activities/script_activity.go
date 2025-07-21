package activities

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
)

// ScriptActivity handles script execution (replaces "script" step type)
type ScriptActivity struct{}

func (a *ScriptActivity) Name() string {
	return "script"
}

func (a *ScriptActivity) Execute(ctx context.Context, params map[string]any) (any, error) {
	script, ok := params["code"].(string)
	if !ok || script == "" {
		return nil, fmt.Errorf("missing 'code' parameter")
	}

	stateReader, ok := workflow.GetStateFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing state reader in context")
	}

	compiler, ok := workflow.GetCompilerFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing compiler in context")
	}

	globals := map[string]any{
		"inputs": stateReader.GetInputs(),
		"state":  stateReader.GetVariables(),
	}

	// Compile the script using the engine
	compiledScript, err := compiler.Compile(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("failed to compile script: %w", err)
	}

	// Execute the compiled script
	result, err := compiledScript.Evaluate(ctx, globals)
	if err != nil {
		return nil, fmt.Errorf("failed to execute script: %w", err)
	}
	return result.Value(), nil
}
