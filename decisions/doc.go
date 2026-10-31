// Package decisions connects workflows to decision models through
// [github.com/deepnoodle-ai/decide]. A decision model answers typed
// questions, such as "could this shell command destroy work?", with
// probabilities instead of text.
//
// [Handler] answers the decide and choose calls of a definition's edge
// conditions. Register it under the decision kind:
//
//	handlers[workflow.KindDecision] = decisions.Handler(client)
//
// [Guard] wraps a step handler and asks a decision model about each job
// before it runs. A job that the model flags does not run:
//
//	"shell": decisions.Guard(runShell, decisions.GuardConfig{
//		Client: client, // a *decide.Client for Jev, Clef or GPT-6 Luna
//		Text: func(job workflow.Job) string {
//			s, _ := job.Params["command"].(string)
//			return s
//		},
//	}),
//
// A step with approval holds a blocked job for a person instead of failing
// it. Register a handler for the approval kind that asks the person and
// returns workflow.ErrDeferred; deliver the answer, {"approved": true},
// to the step's name. The engine runs the same job again with
// job.Approved set, and the guard runs it without a check:
//
//	{"name": "deploy", "shell": "deploy", "with": {"command": "${{ plan.command }}"},
//	 "approval": {"errors": ["guard_blocked"], "timeout": "24h"}}
//
// A guard is host policy: the definitions do not change, and it covers
// every run, including workflows that an agent writes. It is one layer of
// defense, not a sandbox. Keep least privilege, sandboxes and credentials
// scoped to the job.
// Do not wrap the decision handler in a guard: a flagged or failed check
// on a decision job fails the step the decision leaves.
//
// This package is its own module, so the workflow module keeps its one
// dependency, expr.
package decisions
