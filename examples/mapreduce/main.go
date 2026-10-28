// Command mapreduce shows fan-out over a list. An each step extracts a
// total from 20 documents, at most four at a time (concurrency: 4). With
// continue_on_error, an item that fails after its retries becomes
// {error: ...} instead of failing the step, and a set step splits the
// results into successes and a dead-letter list with filter.
//
// In the output, the in-flight count never passes 4, doc-05 succeeds on
// its retry, and doc-07 and doc-13 end in the dead-letter list.
//
//	go run ./examples/mapreduce
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
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
	var inFlight atomic.Int32
	extract := func(ctx context.Context, job workflow.Job) (any, error) {
		name := job.Params["doc"].(string)
		fmt.Printf("start %-22s in flight %d\n", job.StepID, inFlight.Add(1))
		defer inFlight.Add(-1)
		time.Sleep(20 * time.Millisecond)
		switch {
		case name == "doc-07" || name == "doc-13":
			return nil, fmt.Errorf("%s: no total found", name) // retryable, fails twice
		case name == "doc-05" && job.Attempt == 1:
			return nil, fmt.Errorf("%s: rate limited", name)
		}
		return map[string]any{"doc": name, "total": float64(10 * (job.Index + 1))}, nil
	}

	var docs []any
	for i := 1; i <= 20; i++ {
		docs = append(docs, fmt.Sprintf("doc-%02d", i))
	}
	r := &local.Runner{Handlers: map[string]workflow.Handler{"action": extract}}
	st, err := r.Run(context.Background(), "invoices", def, map[string]any{"docs": docs})
	if err != nil {
		log.Fatal(err)
	}
	outputs, _ := json.MarshalIndent(st.Outputs, "", "  ")
	fmt.Printf("\nrun %s, outputs %s\n", st.Status, outputs)
}
