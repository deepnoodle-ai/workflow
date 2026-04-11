package workflow

import (
	"errors"
	"fmt"
	"strings"
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
