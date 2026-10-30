// Package decisions connects workflows to decision models through
// [github.com/deepnoodle-ai/decide]. A decision model answers typed
// questions, such as "could this shell command destroy work?", with
// probabilities instead of text.
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
// A guard is host policy: the definitions do not change, and it covers
// every run, including workflows that an agent writes. It is one layer of
// defense, not a sandbox. Keep least privilege, sandboxes and credentials
// scoped to the job.
//
// This package is its own module, so the workflow module keeps its two
// dependencies.
package decisions
