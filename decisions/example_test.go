package decisions_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepnoodle-ai/decide"
	"github.com/deepnoodle-ai/decide/decidetest"
	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/decisions"
	"github.com/deepnoodle-ai/workflow/local"
)

// ExampleGuard guards a shell kind with decide's command-risk template.
// "ls -la" runs. "git push --force origin main" does not: the step fails
// with guard_blocked, which a definition can route with catch. A fake
// decide server stands in for the model here; in production, pass a
// client from decide.NewClient.
func ExampleGuard() {
	server, _ := decidetest.Start()
	defer server.Close()
	server.Respond(func(req *decide.Request) (*decide.Response, error) {
		risky := strings.Contains(req.State.(string), "--force")
		answers := map[string]decide.Answer{}
		for key := range req.Questions {
			answers[key] = decidetest.NoulAnswer(0.02)
		}
		if risky {
			answers["destructive"] = decidetest.NoulAnswer(0.97)
			answers["severe"] = decidetest.NoulAnswer(0.91)
		}
		return &decide.Response{Answers: answers}, nil
	})
	client, _ := server.NewClient()

	runShell := func(_ context.Context, job workflow.Job) (any, error) {
		return "ran " + job.Params["command"].(string), nil
	}
	shell := decisions.Guard(runShell, decisions.GuardConfig{
		Client: client,
		Text: func(job workflow.Job) string { // the command-risk questions judge a command
			s, _ := job.Params["command"].(string)
			return s
		},
	})

	for _, cmd := range []string{"ls -la", "git push --force origin main"} {
		job := workflow.Job{RunID: "run-1", Command: workflow.Command{
			StepID: "main/run#1",
			Params: map[string]any{"command": cmd},
		}}
		out, err := shell(context.Background(), job)
		if err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Println(out)
	}
	// Output:
	// ran ls -la
	// guard_blocked: command-risk flagged destructive (yes 97%), severe (yes 91%)
}

// ExampleHandler answers a definition's decide and choose calls with a
// decision model. The host registers the decision kind and one handler,
// and every definition can then ask. A fake decide server stands in for
// the model here; in production, pass a client from decide.NewClient.
func ExampleHandler() {
	server, _ := decidetest.Start()
	defer server.Close()
	server.Respond(func(req *decide.Request) (*decide.Response, error) {
		answers := map[string]decide.Answer{}
		for key, q := range req.Questions {
			switch q := q.(type) {
			case *decide.NoulQuestion:
				answers[key] = decidetest.NoulAnswer(0.96)
			case *decide.ChoiceQuestion:
				answers[key] = decidetest.ChoiceFor(q, 0.9, 0.1)
			}
		}
		return &decide.Response{Answers: answers}, nil
	})
	client, _ := server.NewClient()

	def, err := workflow.Load([]byte(`{
	  "steps": [
	    {"name": "intake", "action": "fetch",
	     "next": {
	       "switch": "choose(\"Which team owns this?\", intake.message, [\"billing\", \"engineering\"])",
	       "cases": {"billing": "refund", "engineering": "fix"}
	     }},
	    {"name": "refund", "action": "check", "match": "first", "next": [
	      {"step": "approve", "when": "decide(\"Is this refund legitimate?\", intake.message) > 0.9"},
	      "human_review"
	    ]},
	    {"name": "fix", "action": "open_bug"},
	    {"name": "approve", "action": "approve"},
	    {"name": "human_review", "action": "queue"}
	  ]
	}`), workflow.KindMap{"action": nil, workflow.KindDecision: nil})
	if err != nil {
		fmt.Println(err)
		return
	}
	action := func(_ context.Context, job workflow.Job) (any, error) {
		fmt.Println(job.Step)
		return map[string]any{"message": "The blender arrived in pieces."}, nil
	}
	runner := &local.Runner{Handlers: map[string]workflow.Handler{
		"action":              action,
		workflow.KindDecision: decisions.Handler(client),
	}}

	st, err := runner.Run(context.Background(), "refund-1", def, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(st.Status)
	// Output:
	// intake
	// refund
	// approve
	// completed
}
