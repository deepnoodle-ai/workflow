package activities

import (
	"fmt"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/script"
)

// ScriptParams defines the parameters for the script activity
type ScriptParams struct {
	Code string `json:"code"`
}

// ScriptActivity executes a script.
type ScriptActivity struct{}

func NewScriptActivity() workflow.Activity {
	return workflow.NewTypedActivity(&ScriptActivity{})
}

func (a *ScriptActivity) Name() string {
	return "script"
}

func (a *ScriptActivity) Execute(ctx workflow.Context, params ScriptParams) (any, error) {
	if params.Code == "" {
		return nil, fmt.Errorf("missing 'code' parameter")
	}

	originalState := workflow.VariablesFromContext(ctx)
	inputs := workflow.InputsFromContext(ctx)

	result, err := script.ExecuteScript(ctx, ctx.GetCompiler(), params.Code, originalState, inputs)
	if err != nil {
		return nil, err
	}

	// Apply state changes
	patches := workflow.GeneratePatches(originalState, result.State)
	if len(patches) > 0 {
		workflow.ApplyPatches(ctx, patches)
	}

	return result.Value, nil
}
