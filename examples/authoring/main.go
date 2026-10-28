// Command authoring shows a typed definition as a guardrail for an agent
// that writes workflows. A fake model proposes a definition with
// mistakes; workflow.Load is the compiler, and its ValidationError
// problems go back to the model as feedback until the definition loads.
// Only then does it run.
//
// Look at each round's problems: Load reports every problem it finds at
// once, and the model's fix for one of them makes a new mistake, which
// Load catches in round 2.
//
//	go run ./examples/authoring
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

// draft is the model's first proposal.
//
//go:embed workflow.json
var draft []byte

// revise is the fake model: it reads the problems and rewrites the
// definition. A real host would send the problems back to an LLM.
func revise(def []byte, problems []workflow.Problem) []byte {
	for _, p := range problems {
		switch {
		case strings.Contains(p.Message, `"sumary"`):
			def = bytes.Replace(def, []byte(`classify.sumary`), []byte(`classify.summary`), 1)
		case strings.Contains(p.Message, "always followed"):
			// It meant "page if urgent, and draft a reply either way".
			def = bytes.Replace(def, []byte(`"next": [
        { "step": "page_oncall"`), []byte(`"match": "all",
      "next": [
        { "step": "page_oncall"`), 1)
		case strings.Contains(p.Message, "page_oncall may not have settled"):
			// It drops page_oncall from the join, but then reads the
			// incident without the ?. the message asked for.
			def = bytes.Replace(def, []byte(`"join": ["draft", "page_oncall"]`),
				[]byte(`"join": ["draft"]`), 1)
			def = bytes.Replace(def, []byte(`"body": "${{ draft.body }}"`),
				[]byte(`"body": "${{ draft.body }}", "incident": "${{ page_oncall.incident }}"`), 1)
		case strings.Contains(p.Message, "use ?."):
			def = bytes.Replace(def, []byte(`page_oncall.incident`), []byte(`page_oncall?.incident`), 1)
		}
	}
	return def
}

func main() {
	kinds := workflow.KindMap{"agent": nil, "action": nil}
	doc := draft
	var def *workflow.Definition
	for round := 1; def == nil; round++ {
		var err error
		def, err = workflow.Load(doc, kinds)
		ve, ok := errors.AsType[*workflow.ValidationError](err)
		if err == nil {
			fmt.Printf("== round %d: loads\n\n", round)
			continue
		} else if !ok {
			log.Fatal(err) // not JSON at all
		}
		fmt.Printf("== round %d: rejected\n", round)
		for _, p := range ve.Problems {
			fmt.Printf("   step %s: %s\n", p.Step, p.Message)
		}
		doc = revise(doc, ve.Problems)
	}

	handler := func(ctx context.Context, job workflow.Job) (any, error) {
		params, _ := json.Marshal(job.Params)
		fmt.Printf("   %-30s %s\n", job.StepID, params)
		switch job.Step {
		case "classify":
			return map[string]any{"urgent": true, "summary": "Checkout is down"}, nil
		case "page_oncall":
			return map[string]any{"incident": "INC-12"}, nil
		case "draft":
			return map[string]any{"body": "We are on it."}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	r := &local.Runner{Handlers: map[string]workflow.Handler{"agent": handler, "action": handler}}
	st, err := r.Run(context.Background(), "ticket-7", def,
		map[string]any{"ticket": "I can't check out"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   run %s\n", st.Status)
}
