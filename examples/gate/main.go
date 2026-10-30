// Command gate deploys a build after a person approves it. The approve
// step is a built-in wait: no handler, no host kind. It finishes when a
// result is delivered to it by name, the way a webhook would, or fails
// with type timeout, which a catch routes to expired.
//
// build forks to approve and notify, so the wait is in place before
// notify asks anyone. The notify handler hands the run ID and the step
// name to a release manager; that is all the answer needs.
//
// It runs three times: approved (and delivered twice, as a webhook
// may), rejected, and never answered.
//
//	go run ./examples/gate
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
	gate(def, "approve", map[string]any{"ok": true, "by": "dana"})
	gate(def, "reject", map[string]any{"ok": false, "by": "sam"})
	gate(def, "ignore", nil)
}

// gate runs one deploy. answer is the release manager's reply, or nil for
// none.
func gate(def *workflow.Definition, name string, answer map[string]any) {
	fmt.Printf("== %s\n", name)
	asked := make(chan string, 1)
	hold := make(chan struct{}) // keeps deploy running until the replies are in
	handlers := map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			if job.Step == "deploy" {
				<-hold
			}
			fmt.Printf("   %-28s %s %v\n", job.StepID, job.Config["action"], job.Params)
			if job.Step == "notify" {
				asked <- job.RunID // in production: a chat message with a link
			}
			return nil, nil
		},
	}
	r := &local.Runner{Handlers: handlers}
	run, err := r.Start(context.Background(), "deploy-"+name, def, nil)
	if err != nil {
		log.Fatal(err)
	}

	runID := <-asked
	if answer != nil {
		// The webhook handler knows the run ID and the step name. It does
		// not know the path or the attempt, and does not need to.
		ev := workflow.Event{Kind: workflow.EventStepCompleted, StepID: "approve", Output: answer}
		fmt.Printf("   deliver to %s: approve %v\n", runID, answer)
		if err := run.Deliver(ev); err != nil {
			log.Fatal(err)
		}
		if name == "approve" {
			// A webhook may deliver twice. The second is a duplicate.
			fmt.Printf("   deliver again: err=%v\n", run.Deliver(ev))
		}
	}
	close(hold)

	st, err := run.Wait()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   run %s, outputs %v\n\n", st.Status, st.Outputs)
}
