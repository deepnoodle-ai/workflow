// Command retry shows how a workflow handles failure: retry with backoff,
// a timeout, catch, and on_failure. It runs the same definition against
// four fake payment providers and prints every attempt.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

//go:embed workflow.json
var doc []byte

// A provider handles one charge attempt (1, 2, ...).
type provider func(ctx context.Context, attempt int) (any, error)

var providers = []struct {
	name   string
	charge provider
}{
	{"flaky: unavailable twice, then fine", func(ctx context.Context, attempt int) (any, error) {
		if attempt < 3 {
			// Retryable, and listed in retry.errors: tried again after
			// 100ms, then 200ms.
			return nil, &workflow.StepError{Type: "unavailable", Message: "503 from provider", Retryable: true}
		}
		return map[string]any{"id": "ch_1"}, nil
	}},
	{"slow: hangs once, then fine", func(ctx context.Context, attempt int) (any, error) {
		if attempt == 1 {
			<-ctx.Done() // the 300ms timeout cancels the attempt
			return nil, ctx.Err()
		}
		return map[string]any{"id": "ch_2"}, nil
	}},
	{"declined: caught and routed", func(ctx context.Context, attempt int) (any, error) {
		// Not retryable. catch sends the path to the declined step, which
		// reads the error from charge's value.
		return nil, &workflow.StepError{Type: "card_declined", Message: "insufficient funds"}
	}},
	{"broken: the run fails", func(ctx context.Context, attempt int) (any, error) {
		// Neither retried nor caught: the run fails and on_failure runs.
		return nil, &workflow.StepError{Type: "invalid_request", Message: "unknown customer"}
	}},
}

func main() {
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil})
	if err != nil {
		log.Fatal(err)
	}
	for _, p := range providers {
		fmt.Printf("== %s\n", p.name)
		start := time.Now()
		action := func(ctx context.Context, c workflow.Command) (any, error) {
			params, _ := json.Marshal(c.Params)
			fmt.Printf("   %5s  %-22s %s %s\n", time.Since(start).Round(10*time.Millisecond),
				c.StepID, c.Config["action"], params)
			if c.Step == "charge" {
				return p.charge(ctx, c.Attempt)
			}
			return map[string]any{"ok": true}, nil
		}
		r := &local.Runner{Handlers: map[string]local.Handler{"action": action}}
		st, err := r.Run(context.Background(), def, map[string]any{"customer": "cus_42"})
		if err != nil {
			log.Fatal(err)
		}
		if st.Error != nil {
			fmt.Printf("   run %s: %s %s: %s\n\n", st.Status, st.Error.Step, st.Error.Type, st.Error.Message)
		} else {
			fmt.Printf("   run %s\n\n", st.Status)
		}
	}
}
