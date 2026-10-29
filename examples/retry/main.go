// Command retry shows how a workflow handles failure: retry with backoff,
// a timeout, catch, on_failure, and a finally step that records every
// outcome. It runs the same definition against four fake payment
// providers and prints every attempt.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

//go:embed workflow.json
var doc []byte

// A provider handles one charge attempt (1, 2, ...). key is the charge's
// idempotency key, the same for every attempt: a real provider returns the
// charge it already made for a key instead of charging the card again.
type provider func(ctx context.Context, attempt int, key string) (any, error)

// charges are the charges the fake providers made, by idempotency key.
var (
	mu      sync.Mutex
	charges = map[string]string{}
)

var providers = []struct {
	name   string
	charge provider
}{
	{"flaky: unavailable twice, then fine", flaky},
	{"slow: charges, then hangs past the timeout", slow},
	{"declined: caught and routed", declined},
	{"broken: the run fails", broken},
}

func flaky(ctx context.Context, attempt int, key string) (any, error) {
	if attempt < 3 {
		// Retryable, and listed in retry.errors: tried again after
		// 100ms, then 200ms.
		return nil, &workflow.StepError{
			Type:      "unavailable",
			Message:   "503 from provider",
			Retryable: true,
		}
	}
	return map[string]any{"id": "ch_1"}, nil
}

func slow(ctx context.Context, attempt int, key string) (any, error) {
	mu.Lock()
	id, charged := charges[key]
	if !charged {
		id = "ch_2"
		charges[key] = id // the card is charged ...
	}
	mu.Unlock()

	if attempt == 1 {
		<-ctx.Done() // ... but the reply is lost: the 300ms timeout cancels the attempt
		return nil, ctx.Err()
	}
	// The retry sends the same key, so the provider returns the charge
	// it made on attempt 1 instead of charging again.
	return map[string]any{"id": id}, nil
}

func declined(ctx context.Context, attempt int, key string) (any, error) {
	// Not retryable. catch sends the path to the declined step, which
	// reads the error from charge's value.
	return nil, &workflow.StepError{Type: "card_declined", Message: "insufficient funds"}
}

func broken(ctx context.Context, attempt int, key string) (any, error) {
	// Neither retried nor caught: the run fails and on_failure runs.
	return nil, &workflow.StepError{Type: "invalid_request", Message: "unknown customer"}
}

func main() {
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil})
	if err != nil {
		log.Fatal(err)
	}
	for i, p := range providers {
		fmt.Printf("== %s\n", p.name)
		start := time.Now()
		action := func(ctx context.Context, c workflow.Job) (any, error) {
			params, _ := json.Marshal(c.Params)
			fmt.Printf("   %5s  %-22s %s %s\n", time.Since(start).Round(10*time.Millisecond),
				c.StepID, c.Config["action"], params)
			if c.Step == "charge" {
				// The StepID changes with each attempt; its unit does not.
				// With the run ID, it is the provider's idempotency key.
				key := c.RunID + "/" + c.StepID.Unit()
				fmt.Printf("          idempotency key %s\n", key)
				return p.charge(ctx, c.Attempt, key)
			}
			return map[string]any{"ok": true}, nil
		}
		r := &local.Runner{Handlers: map[string]workflow.Handler{"action": action}}
		runID := fmt.Sprintf("order-%d", i+1)
		st, err := r.Run(context.Background(), runID, def, map[string]any{"customer": "cus_42"})
		if err != nil {
			log.Fatal(err)
		}
		if st.Error != nil {
			e := st.Error
			fmt.Printf("   run %s: %s %s: %s\n\n", st.Status, e.Step, e.Type, e.Message)
		} else {
			outputs, _ := json.Marshal(st.Outputs)
			fmt.Printf("   run %s, outputs %s\n\n", st.Status, outputs)
		}
	}
}
