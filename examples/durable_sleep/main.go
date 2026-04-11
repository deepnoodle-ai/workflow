// Example: durable_sleep
//
// Demonstrates a durable Sleep step. Unlike time.Sleep inside an
// activity, a Sleep step hard-suspends the execution: the goroutines
// exit, the checkpoint records an absolute WakeAt, and the host
// process can die and restart without losing the deadline. On resume,
// the branch wakes as soon as the wall clock passes WakeAt.
//
// This example uses a very short duration so it runs end-to-end in
// under a second. In production, consumers enqueue a delayed resume
// job at result.NextWakeAt() and call runner.Run again when it fires.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/workflowtest"
)

func main() {
	const cooldown = 200 * time.Millisecond

	wf, err := workflow.New(workflow.Options{
		Name: "cool-down",
		Steps: []*workflow.Step{
			{
				Name:     "Start",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Beginning cool-down period…",
				},
				Next: []*workflow.Edge{{Step: "Cool Down"}},
			},
			{
				Name:  "Cool Down",
				Sleep: &workflow.SleepConfig{Duration: cooldown},
				Next:  []*workflow.Edge{{Step: "Finish"}},
			},
			{
				Name:     "Finish",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Cool-down complete, resuming work.",
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

	// ---- Run 1: hard-suspends on the Sleep step. ----
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
	if !res1.NeedsResume() {
		log.Fatalf("expected suspended, got status=%s", res1.Status)
	}

	wakeAt, ok := res1.NextWakeAt()
	if !ok {
		log.Fatal("expected a populated WakeAt on a sleeping result")
	}
	fmt.Printf("Run 1: status=%s reason=%s\n", res1.Status, res1.WaitReason())
	fmt.Printf("       wake at=%s (in %s)\n",
		wakeAt.Format(time.RFC3339Nano), time.Until(wakeAt).Round(time.Millisecond))

	// In production, a scheduler would enqueue a delayed job to fire
	// at wakeAt and run the resume path below. Here we just sleep.
	time.Sleep(time.Until(wakeAt) + 10*time.Millisecond)

	// ---- Run 2: resumes, sleep is already past its deadline, wakes
	// immediately and runs the Finish step. ----
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
	fmt.Printf("Run 2: status=%s duration=%s\n", res2.Status, res2.Timing.Duration.Round(time.Millisecond))
}
