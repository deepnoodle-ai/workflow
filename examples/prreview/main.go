// Command prreview runs the PR review workflow in-process with fake
// reviewers, and prints every step attempt as it is dispatched.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

//go:embed review.json
var review []byte

func main() {
	// Mobius would check each kind's config here: the agent exists, the
	// action is a known tool, the model is allowed.
	def, err := workflow.Load(review, workflow.KindMap{"agent": nil, "action": nil})
	if err != nil {
		log.Fatal(err)
	}

	agent := func(ctx context.Context, c workflow.Job) (any, error) {
		model, _ := c.Config["model"].(string)
		fmt.Printf("%-34s agent  %s %s\n", c.StepID, c.Config["agent"], model)
		switch c.Step {
		case "correctness":
			return map[string]any{"items": []any{"real: nil deref in handler.go", "style: long function"}}, nil
		case "contracts":
			return map[string]any{"items": []any{"real: removed exported field"}}, nil
		case "tests":
			return map[string]any{"items": []any{}}, nil
		case "verify":
			f := c.Params["finding"].(string)
			return map[string]any{"finding": f, "confirmed": strings.HasPrefix(f, "real")}, nil
		default: // report
			n := len(c.Params["confirmed"].([]any))
			return map[string]any{"verdict": "REQUEST_CHANGES", "status": "failure",
				"body": fmt.Sprintf("%d confirmed findings", n)}, nil
		}
	}
	action := func(ctx context.Context, c workflow.Job) (any, error) {
		params, _ := json.Marshal(c.Params)
		fmt.Printf("%-34s action %s %s\n", c.StepID, c.Config["action"], params)
		return map[string]any{"ok": true}, nil
	}

	st, err := (&local.Runner{Handlers: map[string]workflow.Handler{"agent": agent, "action": action}}).Run(
		context.Background(), "pr-42", def, map[string]any{
			// The host's convention for an event-triggered run: the
			// provider's payload under event, read as input.event.
			"event": map[string]any{
				"action":       "opened",
				"repository":   map[string]any{"full_name": "deepnoodle-ai/mobius"},
				"pull_request": map[string]any{"number": 42, "head": map[string]any{"sha": "abc123"}},
			},
			"meta": map[string]any{"type": "pull_request.opened", "source": "github"},
		})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("run", st.Status)
}
