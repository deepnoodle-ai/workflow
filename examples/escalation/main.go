// Command escalation pages the on-call engineers for an incident until
// someone acknowledges it. Each tier pages, then waits in a wait step, with
// no handler, for an ack that Run.Deliver addresses by the step's name. Its
// timeout fails it with type timeout, and a catch routes to the next tier.
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
	paged := make(chan string, 3)
	handlers := map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			at := since(start)
			if job.Step == "resolve" {
				fmt.Printf("   %5s  %-24s acked_by %s\n", at, job.StepID, job.Params["acked_by"])
				return nil, nil
			}
			fmt.Printf("   %5s  %-24s paged %s\n", at, job.StepID, job.Params["to"])
			paged <- job.Params["to"].(string)
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
		case who := <-paged:
			if who != acker {
				continue // nobody answers: the timeout fires
			}
			// A person reads the page and acks, as through a webhook from
			// the paging service. By then the wait is in place.
			time.Sleep(20 * time.Millisecond)
			err := run.Deliver(workflow.Event{
				Kind:   workflow.EventStepCompleted,
				StepID: workflow.StepID("ack_" + who),
				Output: map[string]any{"by": who},
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
