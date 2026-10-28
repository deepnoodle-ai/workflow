// Command saga shows compensation: an order reserves stock, charges the
// card and ships, and when a step fails the on_failure steps undo only
// what happened. They read reserve?.reservation_id and charge?.id, which
// are nil for a step that never succeeded, and skip the undo then.
//
// The charge's idempotency key is the run ID plus StepID.Unit(), which a
// retry keeps. In the first run the charge fails once and is retried: look
// for the same key on main/charge#1 and main/charge#2.
//
//	go run ./examples/saga
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

// Each scenario fails one step. The charge always fails its first attempt
// with a retryable error.
var scenarios = []struct{ name, failing string }{
	{"shipping is down", "ship"},
	{"the card is declined", "charge"},
	{"out of stock", "reserve"},
}

func main() {
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil})
	if err != nil {
		log.Fatal(err)
	}
	for i, sc := range scenarios {
		fmt.Printf("== %s\n", sc.name)
		handle := func(ctx context.Context, job workflow.Job) (any, error) {
			fmt.Printf("   %-18s", job.StepID)
			defer fmt.Println()
			switch job.Step {
			case "reserve":
				if sc.failing == "reserve" {
					fmt.Print(" failed: out of stock")
					return nil, &workflow.StepError{Type: "out_of_stock", Message: "no stock"}
				}
				return map[string]any{"reservation_id": "res_1"}, nil
			case "charge":
				fmt.Printf(" idempotency key %s/%s", job.RunID, job.StepID.Unit())
				if job.Attempt == 1 {
					fmt.Print(", failed: unavailable")
					return nil, &workflow.StepError{
						Type:      "unavailable",
						Message:   "503",
						Retryable: true,
					}
				}
				if sc.failing == "charge" {
					fmt.Print(", failed: declined")
					return nil, &workflow.StepError{Type: "card_declined", Message: "declined"}
				}
				return map[string]any{"id": "ch_1"}, nil
			case "ship":
				fmt.Print(" failed: carrier API down")
				return nil, &workflow.StepError{Type: "unavailable", Message: "carrier API down"}
			case "release":
				if job.Params["reservation"] == nil {
					fmt.Print(" nothing reserved")
					return nil, nil
				}
				fmt.Printf(" released %v", job.Params["reservation"])
			case "refund":
				if job.Params["charge"] == nil {
					fmt.Print(" nothing charged")
					return nil, nil
				}
				fmt.Printf(" refunded %v", job.Params["charge"])
			}
			return nil, nil
		}
		r := &local.Runner{Handlers: map[string]workflow.Handler{"action": handle}}
		st, err := r.Run(context.Background(), fmt.Sprintf("order-%d", i+1), def, nil)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("   run %s at %s\n\n", st.Status, st.Error.Step)
	}
}
