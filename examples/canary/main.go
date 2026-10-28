// Command canary shows a poll loop. A deploy sends 10% of traffic to the
// new version, then checks its health every 30ms (a sleep step) until a
// check reports it stable, at most five times (max_visits), and only then
// sends it all traffic. A check that sees the
// error rate spike fails the run, and the on_failure step rolls the
// traffic back.
//
// It runs twice. In the output, look for the visits of health (@2, @3)
// and for failure/rollback#1 in the second run.
//
//	go run ./examples/canary
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
	// The error rate each health check sees, in each run.
	deploys := []struct {
		version string
		rates   []float64
	}{
		{"v2.4.0", []float64{0.004, 0.006, 0.005}},
		{"v2.5.0", []float64{0.004, 0.09}},
	}
	for _, d := range deploys {
		version, rates := d.version, d.rates
		fmt.Printf("== deploy %s\n", version)
		checks := 0 // one run's checks never overlap
		handle := func(ctx context.Context, job workflow.Job) (any, error) {
			switch job.Step {
			case "health":
				rate := rates[checks]
				checks++
				fmt.Printf("   %-18s error rate %.1f%%\n", job.StepID, 100*rate)
				if max := job.Params["max_error_rate"].(float64); rate > max {
					return nil, &workflow.StepError{Type: "unhealthy",
						Message: fmt.Sprintf("error rate %.1f%% is over %.1f%%", 100*rate, 100*max)}
				}
				return map[string]any{"stable": checks == 3}, nil
			case "rollback":
				fmt.Printf("   %-18s traffic %v%%: %v\n", job.StepID, job.Params["percent"], job.Params["reason"])
			default:
				fmt.Printf("   %-18s traffic %v%%\n", job.StepID, job.Params["percent"])
			}
			return map[string]any{}, nil
		}
		r := &local.Runner{Handlers: map[string]workflow.Handler{"action": handle}}
		st, err := r.Run(context.Background(), "deploy-"+version, def, map[string]any{"version": version})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("   run %s\n\n", st.Status)
	}
}
