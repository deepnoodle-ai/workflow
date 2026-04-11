// Example: runner
//
// Demonstrates the Runner, which manages execution lifecycle: timeouts,
// heartbeats, crash recovery (resume-or-run), and completion hooks.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

func main() {
	wf, err := workflow.New(workflow.Options{
		Name: "data-pipeline",
		Outputs: []*workflow.Output{
			{Name: "record_count", Variable: "record_count"},
		},
		State: map[string]any{},
		Steps: []*workflow.Step{
			{
				Name:     "Fetch Records",
				Activity: "fetch",
				Store:    "record_count",
				Next:     []*workflow.Edge{{Step: "Process"}},
			},
			{
				Name:     "Process",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Processed ${state.record_count} records",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Simulate a data-fetching activity
	fetchActivity := workflow.TypedActivityFunc(
		"fetch",
		func(ctx workflow.Context, params map[string]any) (int, error) {
			time.Sleep(100 * time.Millisecond) // simulate work
			return 42, nil
		},
	)

	// Create the execution with standard options
	reg := workflow.NewActivityRegistry()
	reg.MustRegister(fetchActivity)
	reg.MustRegister(activities.NewPrintActivity())

	exec, err := workflow.NewExecution(wf, reg)
	if err != nil {
		log.Fatal(err)
	}

	// The Runner adds lifecycle management on top of the execution
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	runner := workflow.NewRunner(
		workflow.WithRunnerLogger(logger),
		workflow.WithDefaultTimeout(30*time.Second),
	)

	result, err := runner.Run(ctx(), exec,
		// Heartbeat proves liveness — useful with distributed workers
		workflow.WithHeartbeat(&workflow.HeartbeatConfig{
			Interval: 5 * time.Second,
			Func: func(ctx context.Context) error {
				// In production: renew a distributed lease here
				return nil
			},
		}),

		// Completion hook produces follow-up workflow descriptors
		workflow.WithCompletionHook(func(ctx context.Context, result *workflow.ExecutionResult) ([]workflow.FollowUpSpec, error) {
			count, _ := result.Outputs["record_count"].(int)
			if count > 10 {
				return []workflow.FollowUpSpec{{
					WorkflowName: "generate-report",
					Inputs:       map[string]any{"record_count": count},
				}}, nil
			}
			return nil, nil
		}),
	)
	if err != nil {
		log.Fatalf("Infrastructure error: %v", err)
	}

	fmt.Printf("Status:    %s\n", result.Status)
	fmt.Printf("Duration:  %s\n", result.Timing.Duration)
	fmt.Printf("Records:   %v\n", result.Outputs["record_count"])

	if len(result.FollowUps) > 0 {
		fmt.Printf("Follow-up: %s\n", result.FollowUps[0].WorkflowName)
	}
}

func ctx() context.Context {
	return context.Background()
}
