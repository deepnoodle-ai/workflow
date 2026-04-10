package risor

import (
	"github.com/deepnoodle-ai/workflow"
)

// ScriptParams defines the parameters for the Risor script activity.
type ScriptParams struct {
	Code string `json:"code"`
}

// ScriptActivity executes a Risor script against the current path's state
// and inputs, capturing any state mutations back into the path.
type ScriptActivity struct{}

// NewScriptActivity returns the "script" activity, which runs Risor code
// with mutable access to the path's state. Use it by registering it in
// ExecutionOptions.Activities alongside a risor.Engine compiler.
func NewScriptActivity() workflow.Activity {
	return workflow.NewTypedActivity(&ScriptActivity{})
}

// Name returns the activity name used to reference it from step definitions.
func (a *ScriptActivity) Name() string {
	return "script"
}

// Execute runs the Risor code provided in params.Code, applies any state
// mutations back to the path, and returns the script's return value.
func (a *ScriptActivity) Execute(ctx workflow.Context, params ScriptParams) (any, error) {
	if params.Code == "" {
		return nil, workflow.NewWorkflowError(workflow.ErrorTypeFatal, "missing 'code' parameter")
	}

	compiler := ctx.GetCompiler()
	if _, ok := compiler.(*Engine); !ok {
		return nil, workflow.NewWorkflowError(workflow.ErrorTypeFatal,
			"script activity requires the Risor engine as ExecutionOptions.ScriptCompiler")
	}

	originalState := workflow.VariablesFromContext(ctx)
	inputs := workflow.InputsFromContext(ctx)

	result, err := ExecuteScript(ctx, compiler, params.Code, originalState, inputs)
	if err != nil {
		return nil, workflow.ClassifyError(err)
	}

	patches := workflow.GeneratePatches(originalState, result.State)
	if len(patches) > 0 {
		workflow.ApplyPatches(ctx, patches)
	}
	return result.Value, nil
}
