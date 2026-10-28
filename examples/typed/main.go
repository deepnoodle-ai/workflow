// Command typed shows typed definitions. It makes one mistake at a time in
// a correct definition and prints what Validate reports, runs the correct
// one, and then shows Apply rejecting an agent output that does not match
// its schema.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

//go:embed workflow.json
var doc []byte

// kinds is the host's step kinds. It knows the github.comment tool's
// parameter schema, so Validate checks the comment step's with: block.
type kinds struct{}

var commentParams, _ = workflow.ParseSchema([]byte(`{"type": "object", "required": ["issue", "body"],
  "properties": {"issue": {"type": "integer"}, "body": {"type": "string"},
                 "label": {"enum": ["bug", "feature", "question"]}}}`))

func (kinds) Known(kind string) bool                      { return kind == "agent" || kind == "action" }
func (kinds) ValidateConfig(string, map[string]any) error { return nil }
func (kinds) Schemas(kind string, cfg map[string]any) (params, output *workflow.Schema, err error) {
	if kind == "action" && cfg["action"] == "github.comment" {
		return commentParams, nil, nil
	}
	return nil, nil, nil
}

var mistakes = []struct{ what, from, to string }{
	{"a misspelled field", `${classify.summary}`, `${classify.sumary}`},
	{"a switch case outside the enum", `"feature": "spec"`, `"feture": "spec"`},
	{"reading a step that may not have run", `${classify.summary}: ${coalesce(reproduce, spec, answer).body}`, `${reproduce.body}`},
	{"a parameter of the wrong type", `"issue": "${event.issue.number}"`, `"issue": "${event.issue.title}"`},
}

func main() {
	fmt.Println("== Validate catches mistakes before anything runs")
	for _, m := range mistakes {
		fmt.Printf("\n%s: %s -> %s\n", m.what, m.from, m.to)
		broken := bytes.Replace(doc, []byte(m.from), []byte(m.to), 1)
		def, err := workflow.Parse(broken)
		if err == nil {
			err = def.Validate(kinds{})
		}
		var ve *workflow.ValidationError
		if !errors.As(err, &ve) {
			log.Fatalf("expected a validation error, got %v", err)
		}
		for _, p := range ve.Problems {
			fmt.Printf("   step %s: %s\n", p.Step, p.Message)
		}
	}

	def, err := workflow.Parse(doc)
	if err != nil {
		log.Fatal(err)
	}
	if err := def.Validate(kinds{}); err != nil {
		log.Fatal(err)
	}
	trigger := map[string]any{"issue": map[string]any{"number": 7, "title": "Crash when saving a draft"}}

	fmt.Println("\n== The correct definition runs")
	st, err := run(def, trigger, map[string]any{"kind": "bug", "summary": "Saving a draft crashes"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("run %s, outputs %v\n", st.Status, st.Outputs)

	fmt.Println("\n== Apply checks outputs against their schemas at run time too")
	st, err = run(def, trigger, map[string]any{"kind": "bugfix", "summary": "Saving a draft crashes"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("run %s: %s %s: %s\n", st.Status, st.Error.Step, st.Error.Type, st.Error.Message)
}

// run runs def with fake agents. classify returns triage.
func run(def *workflow.Definition, trigger, triage map[string]any) (*workflow.State, error) {
	agent := func(ctx context.Context, c workflow.Command) (any, error) {
		if c.Step == "classify" {
			// A real host would ask the model for exactly this shape.
			schema, _ := json.Marshal(c.OutputSchema)
			fmt.Printf("%-24s agent %s, output schema %s\n", c.StepID, c.Config["agent"], schema)
			return triage, nil
		}
		fmt.Printf("%-24s agent %s\n", c.StepID, c.Config["agent"])
		return map[string]any{"body": "Reproduced on main; fix in progress."}, nil
	}
	action := func(ctx context.Context, c workflow.Command) (any, error) {
		params, _ := json.Marshal(c.Params)
		fmt.Printf("%-24s action %s %s\n", c.StepID, c.Config["action"], params)
		return map[string]any{"id": 1}, nil
	}
	r := &local.Runner{Handlers: map[string]local.Handler{"agent": agent, "action": action}}
	return r.Run(context.Background(), def, nil, trigger)
}
