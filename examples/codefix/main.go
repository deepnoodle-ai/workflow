// Command codefix shows an agentic coding loop. An agent drafts a patch,
// the tests and the linter run in parallel, and a judge's verdict either
// opens a pull request or sends the run back to draft (at most three
// times, by max_visits). The draft step reads the last pass's failures as
// run_tests?.failures, which is nil on the first pass.
//
// In the output, look for the second pass: main/draft@2#1 and the steps
// after it, with the failing test the first pass reported.
//
//	go run ./examples/codefix
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

//go:embed workflow.json
var doc []byte

func main() {
	def, err := workflow.Load(doc, workflow.KindMap{"agent": nil, "action": nil})
	if err != nil {
		log.Fatal(err)
	}
	// Only draft writes drafts, and nothing else runs while it does.
	drafts := 0
	handle := func(ctx context.Context, job workflow.Job) (any, error) {
		switch job.Step {
		case "draft":
			drafts++
			fmt.Printf("%-30s failing_tests=%v\n", job.StepID, job.Params["failing_tests"])
			return map[string]any{"diff": fmt.Sprintf("patch v%d", drafts)}, nil
		case "run_tests":
			fmt.Println(job.StepID)
			if drafts == 1 {
				return map[string]any{"failures": []any{"TestSaveDraft"}}, nil
			}
			return map[string]any{"failures": []any{}}, nil
		case "judge":
			verdict := "ship"
			if failures := job.Params["failing_tests"].([]any); len(failures) > 0 {
				verdict = "revise"
			}
			fmt.Printf("%-30s verdict=%s\n", job.StepID, verdict)
			return map[string]any{"verdict": verdict}, nil
		case "open_pr":
			fmt.Printf("%-30s %v\n", job.StepID, job.Params["diff"])
		default:
			fmt.Println(job.StepID)
		}
		return map[string]any{}, nil
	}
	r := &local.Runner{Handlers: map[string]workflow.Handler{"agent": handle, "action": handle}}
	st, err := r.Run(context.Background(), "fix-1432", def,
		map[string]any{"issue": "Saving a draft crashes"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("run", st.Status)
}
