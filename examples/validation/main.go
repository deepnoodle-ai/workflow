// Example: validation
//
// Demonstrates using Validate() to catch structural problems in a
// workflow definition at startup time rather than at runtime.
package main

import (
	"errors"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
)

func main() {
	// This workflow has intentional problems:
	// - "orphaned-step" is unreachable from the start step
	// - The catch handler references a non-existent step "missing-handler"
	wf, err := workflow.New(workflow.Options{
		Name: "broken-workflow",
		Steps: []*workflow.Step{
			{
				Name:     "Start",
				Activity: "do-work",
				Catch: []*workflow.CatchConfig{
					{
						ErrorEquals: []string{workflow.ErrorTypeAll},
						Next:        "missing-handler",
					},
				},
			},
			{
				Name:     "orphaned-step",
				Activity: "print",
				Parameters: map[string]any{
					"message": "This step can never be reached",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Validate catches structural issues before any execution starts.
	// Call this at registration or startup time to fail fast.
	err = wf.Validate()
	if err == nil {
		fmt.Println("Workflow is valid!")
		return
	}

	// Extract the structured validation error for details
	var validationErr *workflow.ValidationError
	if errors.As(err, &validationErr) {
		fmt.Printf("Found %d problems:\n", len(validationErr.Problems))
		for _, p := range validationErr.Problems {
			fmt.Printf("  - %s\n", p)
		}
	}
}
