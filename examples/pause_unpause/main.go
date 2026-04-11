// Example: pause_unpause
//
// Demonstrates operator-driven pause/unpause. The workflow includes a
// declarative Pause step that acts as a manual approval gate. When the
// branch hits the gate it hard-suspends with Status=Paused. An operator
// later calls UnpauseBranchInCheckpoint to clear the flag, and a fresh
// Execution resumes and runs to completion.
//
// The same UnpauseBranchInCheckpoint helper works for externally-paused
// branches (operator calls exec.PauseBranch while the execution is
// running).
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/workflowtest"
)

func main() {
	wf, err := workflow.New(workflow.Options{
		Name: "deploy-with-gate",
		Steps: []*workflow.Step{
			{
				Name:     "Build",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🔨 Build complete, ready to ship.",
				},
				Next: []*workflow.Edge{{Step: "Deploy Gate"}},
			},
			{
				Name: "Deploy Gate",
				Pause: &workflow.PauseConfig{
					Reason: "awaiting operator sign-off",
				},
				Next: []*workflow.Edge{{Step: "Deploy"}},
			},
			{
				Name:     "Deploy",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🚀 Deploying to production.",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	cp := workflowtest.NewMemoryCheckpointer()
	reg := workflow.NewActivityRegistry()
	reg.MustRegister(activities.NewPrintActivity())

	// ---- Run 1: hits the Pause step, hard-suspends. ----
	exec1, err := workflow.NewExecution(wf, reg, workflow.WithCheckpointer(cp))
	if err != nil {
		log.Fatal(err)
	}
	execID := exec1.ID()

	runner := workflow.NewRunner()
	res1, err := runner.Run(context.Background(), exec1)
	if err != nil {
		log.Fatalf("infrastructure error: %v", err)
	}
	if !res1.Paused() {
		log.Fatalf("expected paused, got status=%s", res1.Status)
	}
	fmt.Printf("\nRun 1: status=%s\n", res1.Status)
	for _, b := range res1.Suspension.SuspendedBranches {
		fmt.Printf("       branch=%q step=%q reason=%q\n",
			b.BranchID, b.StepName, b.PauseReason)
	}

	// ---- An operator clears the pause flag in the checkpoint. This
	// does not load or run the execution; it mutates the persisted
	// state directly. Use this from a UI/CLI in a separate process. ----
	fmt.Println("\n[operator clears pause flag]")
	if err := workflow.UnpauseBranchInCheckpoint(
		context.Background(), cp, execID, "main",
	); err != nil {
		log.Fatalf("unpause: %v", err)
	}

	// ---- Run 2: fresh Execution resumes from the checkpoint. The
	// branch was parked at the "Deploy" step (the Pause step was
	// consumed when it fired), so execution continues there. ----
	exec2, err := workflow.NewExecution(wf, reg,
		workflow.WithCheckpointer(cp),
		workflow.WithExecutionID(execID),
	)
	if err != nil {
		log.Fatal(err)
	}
	res2, err := runner.Run(context.Background(), exec2, workflow.WithResumeFrom(execID))
	if err != nil {
		log.Fatalf("infrastructure error: %v", err)
	}
	if !res2.Completed() {
		log.Fatalf("expected completion, got status=%s err=%v", res2.Status, res2.Error)
	}
	fmt.Printf("\nRun 2: status=%s\n", res2.Status)
}
