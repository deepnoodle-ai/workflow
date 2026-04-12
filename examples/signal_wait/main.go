// Example: signal_wait
//
// Demonstrates durable signal waits. The workflow declares an approval
// gate via a WaitSignal step: when execution reaches the gate, the
// branch hard-suspends and the process (in production, the worker)
// exits. An external actor delivers a signal via the SignalStore, and
// a fresh Execution instance resumes from the checkpoint and runs to
// completion.
//
// The same pattern works for imperative ctx.Wait calls inside
// activities — see docs/suspension.md for the replay-safety contract.
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
	wf, err := workflow.New(workflow.Options{
		Name: "deploy-with-approval",
		Inputs: []*workflow.Input{
			{Name: "release", Type: "string", Default: "v1.2.3"},
		},
		Outputs: []*workflow.Output{
			{Name: "approval", Variable: "approval"},
		},
		Steps: []*workflow.Step{
			{
				Name:     "Announce",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Preparing to deploy ${inputs.release}, waiting for approval…",
				},
				Next: []*workflow.Edge{{Step: "Await Approval"}},
			},
			{
				Name: "Await Approval",
				WaitSignal: &workflow.WaitSignalConfig{
					// Topic is an expression template, resolved when the
					// step is entered and persisted in the checkpoint.
					Topic:   "approval-${inputs.release}",
					Timeout: 24 * time.Hour,
					Store:   "approval",
				},
				Next: []*workflow.Edge{{Step: "Deploy"}},
			},
			{
				Name:     "Deploy",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Deploying ${inputs.release} (approved by ${state.approval})",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Shared durable-ish infrastructure. In production both of these
	// would be backed by Postgres/Redis/etc.; here we use the in-memory
	// implementations bundled for development.
	cp := workflowtest.NewMemoryCheckpointer()
	signals := workflow.NewMemorySignalStore()

	reg := workflow.NewActivityRegistry()
	reg.MustRegister(activities.NewPrintActivity())

	// ---- Run 1: execution hard-suspends at the WaitSignal step. ----
	exec1, err := workflow.NewExecution(wf, reg,
		workflow.WithInputs(map[string]any{"release": "v1.2.3"}),
		workflow.WithCheckpointer(cp),
		workflow.WithSignalStore(signals),
	)
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
		log.Fatalf("expected execution to suspend, got status=%s", res1.Status)
	}
	fmt.Printf("Run 1: status=%s reason=%s\n", res1.Status, res1.WaitReason())
	fmt.Printf("       topics=%v\n", res1.Topics())
	if when, ok := res1.NextWakeAt(); ok {
		fmt.Printf("       timeout at=%s\n", when.Format(time.RFC3339))
	}

	// ---- Meanwhile, an operator (or a webhook handler, or anything
	// else subscribed to the suspension's topics) delivers the signal. ----
	topic := res1.Topics()[0]
	if err := signals.Send(context.Background(), execID, topic, "alice@example.com"); err != nil {
		log.Fatalf("send signal: %v", err)
	}
	fmt.Printf("\nDelivered signal to topic %q\n\n", topic)

	// ---- Run 2: a fresh Execution built from the same checkpointer
	// resumes from the prior execution ID and runs to completion. ----
	exec2, err := workflow.NewExecution(wf, reg,
		workflow.WithInputs(map[string]any{"release": "v1.2.3"}),
		workflow.WithCheckpointer(cp),
		workflow.WithSignalStore(signals),
		workflow.WithExecutionID(execID),
	)
	if err != nil {
		log.Fatal(err)
	}

	res2, err := runner.Run(context.Background(), exec2,
		workflow.WithResumeFrom(execID),
	)
	if err != nil {
		log.Fatalf("infrastructure error: %v", err)
	}
	if !res2.Completed() {
		log.Fatalf("expected completion, got status=%s err=%v", res2.Status, res2.Error)
	}
	fmt.Printf("Run 2: status=%s\n", res2.Status)
	if approval, ok := res2.OutputString("approval"); ok {
		fmt.Printf("       approval=%q\n", approval)
	}
}
