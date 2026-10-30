// Command escalation pages the on-call engineers for an incident until
// someone acknowledges it. Each tier step forks to a page step and a wait
// step, so the wait is in place before anyone is paged. The wait has no
// handler: Run.Deliver sends it the ack by the step's name. Its timeout
// fails it with type timeout, and a catch routes to the next tier.
//
// It runs twice: the primary acks, then nobody acks until the manager.
// Look for the timeouts between the pages in the second run, and for
// who resolve reports as acked_by.
//
//	go run ./examples/escalation
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"time"

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
	escalate(def, "primary")
	escalate(def, "manager")
}

// escalate runs one incident in which only the acker tier answers.
func escalate(def *workflow.Definition, acker string) {
	fmt.Printf("== only the %s acks\n", acker)
	start := time.Now()
	paged := make(chan map[string]any, 3)
	handlers := map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			at := since(start)
			switch job.Config["action"] {
			case "pager.page":
				fmt.Printf("   %5s  %-36s paged %s\n", at, job.StepID, job.Params["to"])
				paged <- job.Params
			case "incident.resolve":
				fmt.Printf("   %5s  %-36s acked_by %s\n", at, job.StepID, job.Params["acked_by"])
			default:
				return nil, fmt.Errorf("unknown action %v", job.Config["action"])
			}
			return nil, nil
		},
	}
	r := &local.Runner{Handlers: handlers}
	input := map[string]any{"alert": "checkout p99 > 2s"}
	run, err := r.Start(context.Background(), "incident-"+acker, def, input)
	if err != nil {
		log.Fatal(err)
	}
	for {
		select {
		case page := <-paged:
			if page["to"] != acker {
				continue // nobody answers: the timeout fires
			}
			// A person acks, as through a webhook from the paging service.
			// The page carried the name of the step that waits for the ack.
			err := run.Deliver(workflow.Event{
				Kind:   workflow.EventStepCompleted,
				StepID: workflow.StepID(page["ack"].(string)),
				Output: map[string]any{"by": acker},
			})
			if err != nil {
				log.Fatal(err)
			}
		case <-run.Done():
			st, err := run.Wait()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("   run %s\n\n", st.Status)
			return
		}
	}
}

func since(start time.Time) time.Duration {
	return time.Since(start).Round(10 * time.Millisecond)
}
