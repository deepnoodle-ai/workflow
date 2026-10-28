// Command loop polls a deploy until it is done, sleeping between polls.
// max_visits bounds the loop: a deploy still rolling after five polls
// fails the run instead of polling forever.
//
// In the output, each poll is a new visit of the same step, so its StepID
// shows @2, @3, @4. State.Bytes stays flat: a new visit replaces the old
// one's output, so the run's state does not grow with the loop.
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
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil})
	if err != nil {
		log.Fatal(err)
	}

	var st *workflow.State // the latest state, from Save
	polls := 0
	action := func(ctx context.Context, job workflow.Job) (any, error) {
		fmt.Printf("%-16s state %d bytes\n", job.StepID, st.Bytes)
		if job.Step != "poll" {
			return map[string]any{"ok": true}, nil
		}
		polls++
		status := "rolling"
		if polls == 4 {
			status = "done"
		}
		return map[string]any{"status": status, "ready": fmt.Sprintf("%d of 4 replicas", polls)}, nil
	}

	r := &local.Runner{
		Handlers: map[string]workflow.Handler{"action": action},
		// Save is called with each new state before its commands run.
		Save: func(_ context.Context, _ string, s *workflow.State) error {
			st = s
			return nil
		},
	}
	final, err := r.Run(context.Background(), "deploy-api", def, map[string]any{"service": "api"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("run", final.Status, final.Outputs)
}
