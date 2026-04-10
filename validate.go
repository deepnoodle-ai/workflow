package workflow

import (
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

// Validate checks the workflow for structural problems: unreachable steps,
// invalid join configurations, and dangling catch handler references.
//
// Returns nil if the workflow is valid. Returns *ValidationError if problems
// are found. Call this at registration/startup time to fail fast.
//
// Validate does not check activity names. Activity mismatches surface
// immediately at runtime when the step executes, and validating them here
// would require passing activities before they're available.
func (w *Workflow) Validate() error {
	var problems []ValidationProblem

	// 1. Reachability: all steps reachable from start via BFS
	reachable := w.reachableSteps()
	for _, step := range w.steps {
		if !reachable[step.Name] {
			problems = append(problems, ValidationProblem{
				Step:    step.Name,
				Message: "unreachable from start step",
			})
		}
	}

	// 2. Join configuration validity
	for _, step := range w.steps {
		if step.Join == nil {
			continue
		}
		for _, path := range step.Join.Paths {
			if !w.pathExists(path) {
				problems = append(problems, ValidationProblem{
					Step:    step.Name,
					Message: fmt.Sprintf("join references unknown path %q", path),
				})
			}
		}
	}

	// 3. Catch handler next-step validity
	for _, step := range w.steps {
		for _, c := range step.Catch {
			if _, ok := w.stepsByName[c.Next]; !ok {
				problems = append(problems, ValidationProblem{
					Step:    step.Name,
					Message: fmt.Sprintf("catch handler references unknown step %q", c.Next),
				})
			}
		}
	}

	// 4. Pause step configuration validity. A pause step is a hold
	// gate with a single-choice successor; it must declare at least
	// one Next edge so the path has somewhere to go on unpause.
	for _, step := range w.steps {
		if step.Pause == nil {
			continue
		}
		if len(step.Next) == 0 {
			problems = append(problems, ValidationProblem{
				Step:    step.Name,
				Message: "pause: at least one Next edge is required",
			})
		}
	}

	// 5. WaitSignal configuration validity
	for _, step := range w.steps {
		ws := step.WaitSignal
		if ws == nil {
			continue
		}
		if ws.Topic == "" {
			problems = append(problems, ValidationProblem{
				Step:    step.Name,
				Message: "wait_signal: topic is required",
			})
		}
		if ws.Timeout <= 0 {
			problems = append(problems, ValidationProblem{
				Step:    step.Name,
				Message: "wait_signal: positive timeout is required",
			})
		}
		if ws.OnTimeout != "" {
			if _, ok := w.stepsByName[ws.OnTimeout]; !ok {
				problems = append(problems, ValidationProblem{
					Step:    step.Name,
					Message: fmt.Sprintf("wait_signal: OnTimeout target %q not found", ws.OnTimeout),
				})
			}
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// reachableSteps returns the set of step names reachable from the start step.
func (w *Workflow) reachableSteps() map[string]bool {
	reachable := make(map[string]bool)
	if w.start == nil {
		return reachable
	}

	queue := []*Step{w.start}
	reachable[w.start.Name] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		// Follow edges
		for _, edge := range current.Next {
			if !reachable[edge.Step] {
				if step, ok := w.stepsByName[edge.Step]; ok {
					reachable[edge.Step] = true
					queue = append(queue, step)
				}
			}
		}

		// Follow catch handler targets
		for _, c := range current.Catch {
			if !reachable[c.Next] {
				if step, ok := w.stepsByName[c.Next]; ok {
					reachable[c.Next] = true
					queue = append(queue, step)
				}
			}
		}

		// Follow wait_signal OnTimeout target
		if current.WaitSignal != nil && current.WaitSignal.OnTimeout != "" {
			if !reachable[current.WaitSignal.OnTimeout] {
				if step, ok := w.stepsByName[current.WaitSignal.OnTimeout]; ok {
					reachable[current.WaitSignal.OnTimeout] = true
					queue = append(queue, step)
				}
			}
		}
	}

	return reachable
}

// pathExists returns whether a named path is defined on any edge in the workflow.
func (w *Workflow) pathExists(name string) bool {
	for _, step := range w.steps {
		for _, edge := range step.Next {
			if edge.Path == name {
				return true
			}
		}
	}
	return false
}
