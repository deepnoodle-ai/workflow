// Command toolcall shows a workflow used as a tool. The child definition,
// research_company, declares a typed input and outputs with schemas and
// descriptions, which is all a tool definition needs: the program prints
// one as an LLM's tool list would take it.
//
// The host also registers a "workflow" step kind, so another definition
// can call the child as a step. Its KindSchemas returns the child's input
// schema as the step's params and its OutputSchema as the step's output,
// so Load type-checks the parent's reads of the child's outputs: look for
// the error on a misspelled field. The handler starts the child as its own
// run, returns ErrDeferred, and delivers the child's outputs to the parent
// with Run.Deliver when the child finishes.
//
//	go run ./examples/toolcall
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

var (
	//go:embed research.json
	researchDoc []byte
	//go:embed brief.json
	briefDoc []byte
)

// children are the workflows a "workflow" step may call, by name.
var children = map[string]*workflow.Definition{}

// kinds are the host's step kinds: agent, and workflow for a child run.
type kinds struct{}

func (kinds) Known(kind string) bool { return kind == "agent" || kind == "workflow" }

func (kinds) ValidateConfig(kind string, cfg map[string]any) error {
	if name, _ := cfg["workflow"].(string); kind == "workflow" && children[name] == nil {
		return fmt.Errorf("unknown workflow %q", cfg["workflow"])
	}
	return nil
}

func (kinds) Schemas(kind string, cfg map[string]any) (params, output *workflow.Schema, err error) {
	if kind == "workflow" {
		child := children[cfg["workflow"].(string)]
		return child.Input, child.OutputSchema(), nil
	}
	return nil, nil, nil
}

func main() {
	research, err := workflow.Load(researchDoc, workflow.KindMap{"agent": nil})
	if err != nil {
		log.Fatal(err)
	}
	children["research_company"] = research
	tool, _ := json.MarshalIndent(struct {
		Name         string           `json:"name"`
		Description  string           `json:"description"`
		InputSchema  *workflow.Schema `json:"input_schema"`
		OutputSchema *workflow.Schema `json:"output_schema"`
	}{research.Name, research.Description, research.Input, research.OutputSchema()}, "", "  ")
	fmt.Printf("== research_company as a tool\n%s\n\n", tool)

	typo := bytes.Replace(briefDoc, []byte("research.summary"), []byte("research.sumary"), 1)
	_, err = workflow.Load(typo, kinds{})
	fmt.Printf("== Load with research.sumary\n%v\n\n", err)

	brief, err := workflow.Load(briefDoc, kinds{})
	if err != nil {
		log.Fatal(err)
	}

	// runs holds every run by ID, so a child can deliver to its parent.
	// start holds the lock until the run is in runs, so nothing can look
	// it up too early.
	var mu sync.Mutex
	runs := map[string]*local.Run{}
	var runner *local.Runner
	start := func(id string, def *workflow.Definition, input map[string]any) (*local.Run, error) {
		mu.Lock()
		defer mu.Unlock()
		run, err := runner.Start(context.Background(), id, def, input)
		runs[id] = run
		return run, err
	}

	agent := func(ctx context.Context, job workflow.Job) (any, error) {
		params, _ := json.Marshal(job.Params)
		fmt.Printf("%-22s %-16s %s\n", job.RunID, job.StepID, params)
		if job.Step == "research" {
			return map[string]any{
				"summary":     "Acme makes anvils.",
				"competitors": []any{"Globex", "Initech"},
			}, nil
		}
		return map[string]any{}, nil
	}
	callWorkflow := func(ctx context.Context, job workflow.Job) (any, error) {
		childID := job.RunID + "/" + job.StepID.Unit() // a retry keeps it
		fmt.Printf("%-22s %-16s start run %s\n", job.RunID, job.StepID, childID)
		// The child does not use ctx, which ends when this handler returns.
		run, err := start(childID, children[job.Config["workflow"].(string)], job.Params)
		if err != nil {
			return nil, err
		}
		go func() {
			st, _ := run.Wait()
			ev := workflow.Event{
				Kind:   workflow.EventStepCompleted,
				StepID: job.StepID,
				Output: st.Outputs,
			}
			if st.Status != workflow.StatusCompleted {
				ev = workflow.Event{
					Kind:   workflow.EventStepFailed,
					StepID: job.StepID,
					Error: &workflow.StepError{
						Type:    "child_" + string(st.Status),
						Message: childID,
					},
				}
			}
			fmt.Printf("%-22s deliver %s to %s\n", childID, ev.Kind, job.StepID)
			mu.Lock()
			parent := runs[job.RunID]
			mu.Unlock()
			if err := parent.Deliver(ev); err != nil {
				log.Fatal(err)
			}
		}()
		return nil, workflow.ErrDeferred
	}
	runner = &local.Runner{
		Handlers: map[string]workflow.Handler{"agent": agent, "workflow": callWorkflow},
	}

	fmt.Println("== Run the brief")
	run, err := start("brief-1", brief, map[string]any{"company": "acme.com"})
	if err != nil {
		log.Fatal(err)
	}
	st, err := run.Wait()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("run", st.Status)
}
