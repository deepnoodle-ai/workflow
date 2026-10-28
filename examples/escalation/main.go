// Command escalation pages the on-call engineers for an incident until
// someone acknowledges it. Each page step hands off to a person and
// returns workflow.ErrDeferred, so it stays in flight until the ack
// arrives through Run.Deliver. Its timeout fails it with type timeout,
// and a catch on timeout routes to the next tier.
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
	def, err := workflow.Load(doc, workflow.KindMap{"page": nil, "action": nil})
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
	paged := make(chan workflow.Job, 3)
	handlers := map[string]workflow.Handler{
		"page": func(ctx context.Context, job workflow.Job) (any, error) {
			fmt.Printf("   %5s  %-24s paged %s\n", since(start), job.StepID, job.Config["page"])
			paged <- job
			return nil, workflow.ErrDeferred // the ack arrives later, if at all
		},
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			fmt.Printf("   %5s  %-24s acked_by %s\n", since(start), job.StepID, job.Params["acked_by"])
			return map[string]any{"ok": true}, nil
		},
	}
	r := &local.Runner{Handlers: handlers}
	input := map[string]any{"event": map[string]any{"alert": "checkout p99 > 2s"}}
	run, err := r.Start(context.Background(), "incident-"+acker, def, input)
	if err != nil {
		log.Fatal(err)
	}
	for {
		select {
		case job := <-paged:
			if job.Config["page"] != acker {
				continue // nobody answers: the timeout fires
			}
			// A person acks, as through a webhook from the paging service.
			err := run.Deliver(workflow.Event{
				Kind:   workflow.EventStepCompleted,
				StepID: job.StepID,
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
