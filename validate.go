package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/deepnoodle-ai/workflow/script"
)

// ValidationProblem describes a single structural issue in a workflow.
type ValidationProblem struct {
	// Step is the name of the step where the problem was found.
	// Empty for workflow-level problems.
	Step string

	// Message describes the problem.
	Message string

	// Err is the sentinel error associated with this problem, if any.
	// Callers can use errors.Is against the enclosing *ValidationError
	// to test for specific problem classes (ErrDuplicateStepName, etc.).
	Err error
}

func (p ValidationProblem) String() string {
	if p.Step != "" {
		return fmt.Sprintf("step %q: %s", p.Step, p.Message)
	}
	return p.Message
}

// ValidationError contains all problems found during validation.
type ValidationError struct {
	Problems []ValidationProblem
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "workflow validation failed (%d problems):", len(e.Problems))
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  - %s", p)
	}
	return b.String()
}

// Is reports whether err matches any sentinel attached to one of the
// contained problems. This makes errors.Is(err, ErrDuplicateStepName)
// work against a ValidationError containing a duplicate-name problem.
func (e *ValidationError) Is(target error) bool {
	for _, p := range e.Problems {
		if p.Err != nil && errors.Is(p.Err, target) {
			return true
		}
	}
	return false
}

// Validate checks the workflow for structural problems.
//
// Structural validation does not consult the activity registry or the
// script compiler — those binding-level checks run at NewExecution
// time. Validate collects every problem it finds into a
// *ValidationError rather than failing on the first one.
//
// This runs automatically as part of workflow.New. It is also exposed
// for tools (editors, linters) that want to validate a workflow
// without constructing one.
func (w *Workflow) Validate() error {
	var problems []ValidationProblem
	add := func(step, msg string, sentinel error) {
		problems = append(problems, ValidationProblem{
			Step:    step,
			Message: msg,
			Err:     sentinel,
		})
	}

	// 1. Edge targets, branch name uniqueness, reserved names.
	usedBranchNames := map[string]bool{}
	for _, step := range w.steps {
		for _, edge := range step.Next {
			if _, ok := w.stepsByName[edge.Step]; !ok {
				add(step.Name,
					fmt.Sprintf("edge destination %q not found", edge.Step),
					ErrUnknownEdgeTarget)
			}
			if edge.BranchName == "" {
				continue
			}
			if edge.BranchName == "main" {
				add(step.Name,
					fmt.Sprintf("branch name 'main' is reserved (edge to %q)", edge.Step),
					ErrReservedBranchName)
				continue
			}
			if usedBranchNames[edge.BranchName] {
				add(step.Name,
					fmt.Sprintf("duplicate branch name %q", edge.BranchName),
					ErrDuplicateBranchName)
				continue
			}
			usedBranchNames[edge.BranchName] = true
		}
	}

	// 2. Step kind exclusivity.
	for _, step := range w.steps {
		var kinds []string
		if step.Activity != "" {
			kinds = append(kinds, "activity")
		}
		if step.Join != nil {
			kinds = append(kinds, "join")
		}
		if step.WaitSignal != nil {
			kinds = append(kinds, "wait_signal")
		}
		if step.Sleep != nil {
			kinds = append(kinds, "sleep")
		}
		if step.Pause != nil {
			kinds = append(kinds, "pause")
		}
		if len(kinds) > 1 {
			add(step.Name,
				fmt.Sprintf("conflicting step kinds %v — a step is exactly one of: activity, join, wait_signal, sleep, pause", kinds),
				ErrInvalidStepKind)
		}
	}

	// 3. Modifier validity — retry/catch only on activity or wait_signal
	// steps. Pause/sleep/join cannot fail in a way a retry or catch could
	// meaningfully handle.
	for _, step := range w.steps {
		isActivityOrWait := step.Activity != "" || step.WaitSignal != nil
		if !isActivityOrWait {
			if len(step.Retry) > 0 {
				add(step.Name, "retry is only valid on activity or wait_signal steps", ErrInvalidModifier)
			}
			if len(step.Catch) > 0 {
				add(step.Name, "catch is only valid on activity or wait_signal steps", ErrInvalidModifier)
			}
		}
	}

	// 4. Join configuration validity.
	for _, step := range w.steps {
		if step.Join == nil {
			continue
		}
		for _, branch := range step.Join.Branches {
			if !w.branchExists(branch) {
				add(step.Name,
					fmt.Sprintf("join references unknown branch %q", branch),
					ErrUnknownJoinBranch)
			}
		}
	}

	// 5. Catch handler next-step validity.
	for _, step := range w.steps {
		for _, c := range step.Catch {
			if _, ok := w.stepsByName[c.Next]; !ok {
				add(step.Name,
					fmt.Sprintf("catch handler references unknown step %q", c.Next),
					ErrUnknownCatchTarget)
			}
		}
	}

	// 6. Pause step configuration validity.
	for _, step := range w.steps {
		if step.Pause == nil {
			continue
		}
		if len(step.Next) == 0 {
			add(step.Name, "pause: at least one Next edge is required", ErrInvalidStepKind)
		}
	}

	// 7. Sleep configuration validity.
	for _, step := range w.steps {
		if step.Sleep == nil {
			continue
		}
		if step.Sleep.Duration <= 0 {
			add(step.Name, "sleep: positive Duration is required", ErrInvalidSleepConfig)
		}
	}

	// 8. WaitSignal configuration validity.
	for _, step := range w.steps {
		ws := step.WaitSignal
		if ws == nil {
			continue
		}
		if ws.Topic == "" {
			add(step.Name, "wait_signal: topic is required", ErrInvalidWaitConfig)
		}
		if ws.Timeout <= 0 {
			add(step.Name, "wait_signal: positive timeout is required", ErrInvalidWaitConfig)
		}
		if ws.OnTimeout != "" {
			if _, ok := w.stepsByName[ws.OnTimeout]; !ok {
				add(step.Name,
					fmt.Sprintf("wait_signal: OnTimeout target %q not found", ws.OnTimeout),
					ErrInvalidWaitConfig)
			}
		}
	}

	// 9. Retry configuration sanity.
	for _, step := range w.steps {
		for i, rc := range step.Retry {
			if rc == nil {
				continue
			}
			if rc.MaxRetries < 0 {
				add(step.Name,
					fmt.Sprintf("retry[%d]: MaxRetries must be >= 0", i),
					ErrInvalidRetryConfig)
			}
			if rc.BaseDelay < 0 || rc.MaxDelay < 0 {
				add(step.Name,
					fmt.Sprintf("retry[%d]: delays must be >= 0", i),
					ErrInvalidRetryConfig)
			}
			if rc.MaxDelay > 0 && rc.BaseDelay > rc.MaxDelay {
				add(step.Name,
					fmt.Sprintf("retry[%d]: BaseDelay (%s) > MaxDelay (%s)", i, rc.BaseDelay, rc.MaxDelay),
					ErrInvalidRetryConfig)
			}
			if rc.BackoffRate < 0 {
				add(step.Name,
					fmt.Sprintf("retry[%d]: BackoffRate must be >= 0", i),
					ErrInvalidRetryConfig)
			}
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// branchExists returns whether a named branch is defined on any edge.
func (w *Workflow) branchExists(name string) bool {
	for _, step := range w.steps {
		for _, edge := range step.Next {
			if edge.BranchName == name {
				return true
			}
		}
	}
	return false
}

// validateBinding runs binding-level checks that require access to the
// activity registry and script compiler. These cannot run inside
// workflow.New because the registry and compiler are bound at
// NewExecution time.
//
// Checks performed:
//  1. Activity references resolve in the registry.
//  2. Parameter templates ("${...}") compile against the given compiler.
//  3. Edge condition expressions compile.
//  4. WaitSignalConfig.Topic templates compile.
//  5. Step.Store, WaitSignalConfig.Store, CatchConfig.Store, and
//     Output.Variable reject any "state." prefix.
//  6. Warn — do not error — if any step uses WaitSignalConfig and no
//     SignalStore is configured.
//
// All problems are collected into a single *ValidationError.
func (w *Workflow) validateBinding(reg *ActivityRegistry, compiler script.Compiler, hasSignalStore bool, logger *slog.Logger) error {
	var problems []ValidationProblem
	add := func(step, msg string, sentinel error) {
		problems = append(problems, ValidationProblem{
			Step:    step,
			Message: msg,
			Err:     sentinel,
		})
	}

	ctx := context.Background()

	// Helper: compile a parameter value recursively, flagging any
	// ${...} template that fails to parse.
	var checkParamValue func(stepName, paramName string, value any)
	checkParamValue = func(stepName, paramName string, value any) {
		switch v := value.(type) {
		case map[string]any:
			for k, vv := range v {
				checkParamValue(stepName, paramName+"."+k, vv)
			}
		case []any:
			for i, vv := range v {
				checkParamValue(stepName, fmt.Sprintf("%s[%d]", paramName, i), vv)
			}
		case string:
			checkParamString(ctx, compiler, stepName, paramName, v, add)
		}
	}

	usesWaitSignal := false

	// 1. Activity references.
	for _, step := range w.steps {
		if step.Activity == "" {
			continue
		}
		if _, ok := reg.Get(step.Activity); !ok {
			add(step.Name,
				fmt.Sprintf("unknown activity %q", step.Activity),
				ErrUnknownActivity)
		}
	}

	// 2. Parameter templates.
	for _, step := range w.steps {
		for name, value := range step.Parameters {
			checkParamValue(step.Name, name, value)
		}
	}

	// 3. Edge condition expressions (raw script expressions).
	for _, step := range w.steps {
		for i, edge := range step.Next {
			if edge.Condition == "" {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(edge.Condition)) {
			case "true", "false":
				continue
			}
			if _, err := compiler.Compile(ctx, edge.Condition); err != nil {
				add(step.Name,
					fmt.Sprintf("edge[%d] condition %q: %v", i, edge.Condition, err),
					ErrInvalidExpression)
			}
		}
	}

	// 4. WaitSignalConfig.Topic template compiles.
	for _, step := range w.steps {
		ws := step.WaitSignal
		if ws == nil {
			continue
		}
		usesWaitSignal = true
		if ws.Topic != "" {
			if _, err := script.NewTemplate(compiler, ws.Topic); err != nil {
				add(step.Name,
					fmt.Sprintf("wait_signal topic %q: %v", ws.Topic, err),
					ErrInvalidTemplate)
			}
		}
	}

	// 5. Store fields reject "state." prefix.
	for _, step := range w.steps {
		if hasStatePrefix(step.Store) {
			add(step.Name,
				fmt.Sprintf("store %q must be a bare variable name, not a %q path", step.Store, "state."),
				ErrInvalidStorePath)
		}
		if step.WaitSignal != nil && hasStatePrefix(step.WaitSignal.Store) {
			add(step.Name,
				fmt.Sprintf("wait_signal store %q must be a bare variable name", step.WaitSignal.Store),
				ErrInvalidStorePath)
		}
		for i, c := range step.Catch {
			if hasStatePrefix(c.Store) {
				add(step.Name,
					fmt.Sprintf("catch[%d] store %q must be a bare variable name", i, c.Store),
					ErrInvalidStorePath)
			}
		}
	}
	for _, out := range w.outputs {
		if hasStatePrefix(out.Variable) {
			add("",
				fmt.Sprintf("output %q variable %q must be a bare variable name", out.Name, out.Variable),
				ErrInvalidStorePath)
		}
	}

	// 6. Warn (do not error) if WaitSignal is used without a SignalStore.
	if usesWaitSignal && !hasSignalStore && logger != nil {
		logger.Warn("workflow uses wait_signal steps but no SignalStore is configured; signals cannot be delivered",
			"workflow", w.name)
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// hasStatePrefix reports whether s begins with the reserved "state."
// prefix that Store fields must not carry.
func hasStatePrefix(s string) bool {
	return strings.HasPrefix(s, "state.")
}

// checkParamString compiles a single parameter string value, reporting
// any template parse failure as a ValidationProblem.
func checkParamString(ctx context.Context, compiler script.Compiler, stepName, paramName, value string, add func(step, msg string, sentinel error)) {
	_ = ctx
	if strings.Contains(value, "${") {
		if _, err := script.NewTemplate(compiler, value); err != nil {
			add(stepName,
				fmt.Sprintf("parameter %q: %v", paramName, err),
				ErrInvalidTemplate)
		}
	}
}
