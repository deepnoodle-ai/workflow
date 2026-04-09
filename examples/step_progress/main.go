// Example: step_progress
//
// Demonstrates step progress tracking. The library automatically tracks
// step state transitions and sends them to a consumer-provided store.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

// logProgressStore prints each step transition to stdout.
// In production, this would write to a database or cache.
type logProgressStore struct {
	mu      sync.Mutex
	updates []workflow.StepProgress
}

func (s *logProgressStore) UpdateStepProgress(_ context.Context, executionID string, p workflow.StepProgress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, p)

	detail := ""
	if p.Detail != nil {
		detail = fmt.Sprintf(" (%s)", p.Detail.Message)
	}
	truncatedID := executionID
	if len(executionID) >= 8 {
		truncatedID = executionID[:8]
	}
	fmt.Printf("  [%s] step=%q status=%s attempt=%d%s\n",
		truncatedID, p.StepName, p.Status, p.Attempt, detail)
	return nil
}

func main() {
	wf, err := workflow.New(workflow.Options{
		Name:  "multi-step-pipeline",
		State: map[string]any{},
		Steps: []*workflow.Step{
			{
				Name:     "Extract",
				Activity: "extract",
				Store:    "data",
				Next:     []*workflow.Edge{{Step: "Transform"}},
			},
			{
				Name:     "Transform",
				Activity: "transform",
				Next:     []*workflow.Edge{{Step: "Load"}},
			},
			{
				Name:     "Load",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Loaded: ${state.data}",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	store := &logProgressStore{}

	exec, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow: wf,
		Activities: []workflow.Activity{
			workflow.NewTypedActivityFunction("extract",
				func(ctx workflow.Context, params map[string]any) (string, error) {
					time.Sleep(50 * time.Millisecond)
					return "42 records", nil
				},
			),
			workflow.NewTypedActivityFunction("transform",
				func(ctx workflow.Context, params map[string]any) (any, error) {
					// Report intra-activity progress
					workflow.ReportProgress(ctx, workflow.ProgressDetail{
						Message: "Transforming batch 1 of 2",
					})
					time.Sleep(50 * time.Millisecond)

					workflow.ReportProgress(ctx, workflow.ProgressDetail{
						Message: "Transforming batch 2 of 2",
						Data:    map[string]any{"batch": 2, "total": 2},
					})
					time.Sleep(50 * time.Millisecond)
					return nil, nil
				},
			),
			activities.NewPrintActivity(),
		},
		// Wire up the store — the library handles the rest
		StepProgressStore: store,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Step progress updates:")
	result, err := exec.Execute(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	// Allow async dispatches to complete
	time.Sleep(50 * time.Millisecond)

	fmt.Printf("\nResult: %s (%s)\n", result.Status, result.Timing.Duration)
	fmt.Printf("Total updates received: %d\n", len(store.updates))
}
