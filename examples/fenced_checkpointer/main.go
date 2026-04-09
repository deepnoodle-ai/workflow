// Example: fenced_checkpointer
//
// Demonstrates WithFencing, which wraps a checkpointer with a pre-save
// lease check. This prevents stale workers from overwriting checkpoint
// state after losing their distributed lease.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/workflowtest"
)

// simulatedLease mimics a distributed lease manager.
type simulatedLease struct {
	mu   sync.Mutex
	held bool
}

func (l *simulatedLease) check(_ context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return fmt.Errorf("worker lost lease")
	}
	return nil
}

func (l *simulatedLease) revoke() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = false
}

func main() {
	wf, err := workflow.New(workflow.Options{
		Name:  "fenced-workflow",
		State: map[string]any{},
		Steps: []*workflow.Step{
			{
				Name:     "Step 1",
				Activity: "do-work",
				Store:    "result",
				Next:     []*workflow.Edge{{Step: "Step 2"}},
			},
			{
				Name:     "Step 2",
				Activity: "do-work",
				Store:    "result2",
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	lease := &simulatedLease{held: true}
	stepCount := 0

	// Wrap the checkpointer with a fence check.
	// Before each checkpoint save, the lease is validated.
	fenced := workflow.WithFencing(
		workflowtest.NewMemoryCheckpointer(),
		lease.check,
	)

	exec, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:     wf,
		Checkpointer: fenced,
		Activities: []workflow.Activity{
			workflow.NewActivityFunction("do-work",
				func(ctx workflow.Context, params map[string]any) (any, error) {
					stepCount++
					if stepCount == 2 {
						// Simulate losing the lease mid-execution
						lease.revoke()
						fmt.Println("Lease revoked after step 1!")
					}
					return "done", nil
				},
			),
			activities.NewPrintActivity(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	result, err := exec.Execute(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	if result.Failed() {
		fmt.Printf("Execution failed: %s\n", result.Error.Cause)

		// Check if the failure was a fence violation
		if errors.Is(result.Error, workflow.ErrFenceViolation) {
			fmt.Println("Worker should stop and let the new lease holder take over.")
		}
		return
	}

	fmt.Printf("Completed: %v\n", result.Outputs)
}
