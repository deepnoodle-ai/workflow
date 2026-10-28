package workflow

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// prReview is the PR review workflow from the Mobius proposal, in JSON.
const prReview = `{
  "name": "Review pull request",
  "schemas": {
    "findings": {"type": "object", "required": ["items"],
                 "properties": {"items": {"type": "array", "items": {"type": "string"}}}},
    "verdict":  {"type": "object", "required": ["finding", "confirmed"],
                 "properties": {"finding": {"type": "string"}, "confirmed": {"type": "boolean"}}},
    "review":   {"type": "object", "required": ["verdict", "status", "body"],
                 "properties": {"verdict": {"enum": ["APPROVE", "REQUEST_CHANGES", "COMMENT"]},
                                "status":  {"enum": ["success", "failure"]},
                                "body":    {"type": "string"}}}
  },
  "event_schema": {"type": "object", "required": ["repository", "pull_request"], "properties": {
    "repository":   {"type": "object", "required": ["full_name"], "properties": {"full_name": {"type": "string"}}},
    "pull_request": {"type": "object", "required": ["number", "head"], "properties": {
      "number": {"type": "integer"},
      "head":   {"type": "object", "required": ["sha"], "properties": {"sha": {"type": "string"}}}}}}},
  "steps": [
    {"name": "pending", "action": "github.commit.set_status",
     "with": {"repo_full_name": "${{ event.repository.full_name }}", "sha": "${{ event.pull_request.head.sha }}",
              "state": "pending", "context": "Mobius"},
     "next": ["correctness", "contracts", "tests"]},

    {"name": "correctness", "agent": "reviewer", "model": "claude-opus-5-5",   "instructions": "...", "output": "findings", "next": ["gather"]},
    {"name": "contracts",   "agent": "reviewer", "model": "claude-sonnet-5-5", "instructions": "...", "output": "findings", "next": ["gather"]},
    {"name": "tests",       "agent": "reviewer", "model": "claude-haiku-5-5",  "instructions": "...", "output": "findings", "next": ["gather"]},

    {"name": "gather", "join": ["correctness", "contracts", "tests"], "next": ["verify"]},

    {"name": "verify", "each": "concat(correctness.items, contracts.items, tests.items)",
     "agent": "reviewer", "instructions": "Check this one finding against the code.",
     "with": {"finding": "${{ item }}"}, "output": "verdict",
     "match": "first",
     "next": [{"step": "report", "when": "any(verify, it.confirmed)"}, {"step": "approve"}]},

    {"name": "report", "agent": "reviewer", "model": "claude-opus-5-5",
     "instructions": "Write the review from the confirmed findings.",
     "with": {"confirmed": "${{ filter(verify, it.confirmed) }}"}, "output": "review", "next": ["post"]},

    {"name": "approve", "set": {"verdict": "APPROVE", "status": "success", "body": "No confirmed findings."},
     "next": ["post"]},

    {"name": "post", "action": "github.pull_request.create_review",
     "with": {"repo_full_name": "${{ event.repository.full_name }}", "pr_number": "${{ event.pull_request.number }}",
              "commit_sha": "${{ event.pull_request.head.sha }}",
              "event": "${{ coalesce(report, approve).verdict }}", "body": "${{ coalesce(report, approve).body }}"},
     "next": ["final"]},

    {"name": "final", "action": "github.commit.set_status",
     "with": {"repo_full_name": "${{ event.repository.full_name }}", "sha": "${{ event.pull_request.head.sha }}",
              "state": "${{ coalesce(report, approve).status }}", "context": "Mobius"}}
  ],
  "on_failure": [
    {"action": "github.commit.set_status",
     "with": {"repo_full_name": "${{ event.repository.full_name }}", "sha": "${{ event.pull_request.head.sha }}",
              "state": "error", "context": "Mobius", "description": "${{ error.message }}"}}
  ]
}`

var prTrigger = map[string]any{
	"action":       "opened",
	"repository":   map[string]any{"full_name": "deepnoodle-ai/mobius"},
	"pull_request": map[string]any{"number": 42, "head": map[string]any{"sha": "abc123"}},
}

// prHandler is a fake Mobius: reviewers find two, one and zero findings,
// and verify confirms findings whose text contains "real".
func prHandler(c Command) result {
	switch c.Step {
	case "correctness":
		return ok(map[string]any{"items": []any{"real: nil deref", "style nit"}})
	case "contracts":
		return ok(map[string]any{"items": []any{"real: API break"}})
	case "tests":
		return ok(map[string]any{"items": []any{}})
	case "verify":
		finding := c.Params["finding"].(string)
		return ok(map[string]any{"finding": finding, "confirmed": strings.HasPrefix(finding, "real")})
	case "report":
		n := len(c.Params["confirmed"].([]any))
		return ok(map[string]any{"verdict": "REQUEST_CHANGES", "status": "failure",
			"body": strings.Repeat("finding\n", n)})
	default:
		return ok(map[string]any{"ok": true})
	}
}

func TestPRReviewEndToEnd(t *testing.T) {
	def := mustLoad(t, prReview)
	h := newHost(t, def, prHandler)
	h.start(nil, prTrigger)

	// The first command is the pending status, with with: evaluated.
	require.Len(t, h.pendingIDs(), 1)
	first := h.pending["main/pending#1"]
	require.Equal(t, "action", first.StepKind)
	require.Equal(t, "github.commit.set_status", first.Config["action"])
	require.Equal(t, map[string]any{
		"repo_full_name": "deepnoodle-ai/mobius", "sha": "abc123", "state": "pending", "context": "Mobius",
	}, first.Params)
	require.Equal(t, -1, first.Index)

	// Fan out to three reviewers on child paths.
	h.deliver("main/pending#1")
	require.Equal(t, []StepID{"main.contracts/contracts#1", "main.correctness/correctness#1", "main.tests/tests#1"}, h.pendingIDs())
	require.Equal(t, "claude-opus-5-5", h.pending["main.correctness/correctness#1"].Config["model"])

	// The join waits for all three; verify fans out per finding on the
	// merged main path.
	h.deliver("main.tests/tests#1")
	h.deliver("main.correctness/correctness#1")
	require.Equal(t, []StepID{"main.contracts/contracts#1"}, h.pendingIDs())
	h.deliver("main.contracts/contracts#1")
	require.Equal(t, []StepID{"main/verify[0]#1", "main/verify[1]#1", "main/verify[2]#1"}, h.pendingIDs())
	v2 := h.pending["main/verify[2]#1"]
	require.Equal(t, "real: API break", v2.Item)
	require.Equal(t, 2, v2.Index)
	require.Equal(t, map[string]any{"finding": "real: API break"}, v2.Params)

	h.runToEnd(nil)
	require.Equal(t, CommandRunCompleted, h.final.Kind)
	require.Equal(t, StatusCompleted, h.st.Status)

	// report saw the two confirmed findings; post used its verdict.
	var post, final Command
	for _, c := range h.all {
		switch c.Step {
		case "post":
			post = c
		case "final":
			final = c
		}
	}
	require.Equal(t, "REQUEST_CHANGES", post.Params["event"])
	require.Equal(t, float64(42), post.Params["pr_number"])
	require.Equal(t, "failure", final.Params["state"])
	require.Equal(t, map[string]any{"final": map[string]any{"ok": true}}, h.final.Outputs)

	// Nothing ran twice.
	seen := map[StepID]bool{}
	for _, id := range h.dispatched() {
		require.False(t, seen[id], "dispatched twice: %s", id)
		seen[id] = true
	}
	require.Len(t, seen, 10)
}

func TestPRReviewNoFindingsApproves(t *testing.T) {
	def := mustLoad(t, prReview)
	h := newHost(t, def, func(c Command) result {
		if c.Step == "correctness" || c.Step == "contracts" || c.Step == "tests" {
			return ok(map[string]any{"items": []any{}})
		}
		return prHandler(c)
	})
	h.start(nil, prTrigger)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	var post Command
	for _, c := range h.all {
		if c.Step == "post" {
			post = c
		}
		require.NotEqual(t, "report", c.Step)
	}
	require.Equal(t, "APPROVE", post.Params["event"])
	require.Equal(t, "No confirmed findings.", post.Params["body"])
}

func TestPRReviewFailureRunsOnFailure(t *testing.T) {
	def := mustLoad(t, prReview)
	h := newHost(t, def, func(c Command) result {
		if c.Step == "contracts" {
			return fail("model_error", "reviewer crashed", false)
		}
		return prHandler(c)
	})
	h.start(nil, prTrigger)
	h.deliver("main/pending#1")
	h.deliver("main.correctness/correctness#1")
	cmds := h.deliver("main.contracts/contracts#1")

	// The failure cancels the reviewer still in flight and starts on_failure.
	require.Equal(t, []string{"main.tests/tests#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, []string{"failure/on_failure_1#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, StatusFailing, h.st.Status)
	onf := h.pending["failure/on_failure_1#1"]
	require.Equal(t, "error", onf.Params["state"])
	require.Equal(t, "reviewer crashed", onf.Params["description"])

	// A late result from the cancelled reviewer is ignored.
	_, late, err := Apply(def, h.st, Event{At: t0, Kind: EventStepCompleted, StepID: "main.tests/tests#1"})
	require.NoError(t, err)
	require.Empty(t, late)

	h.runToEnd(nil)
	require.Equal(t, CommandRunFailed, h.final.Kind)
	require.Equal(t, "contracts", h.final.Error.Step)
	require.Equal(t, StepID("main.contracts/contracts#1"), h.final.Error.StepID)
	require.Equal(t, "model_error", h.final.Error.Type)
	require.Nil(t, h.final.Error.OnFailure)
}

func TestPRReviewShuffledCompletions(t *testing.T) {
	def := mustLoad(t, prReview)
	ref := newHost(t, def, prHandler)
	ref.start(nil, prTrigger)
	ref.runToEnd(nil)
	want := mustJSON(t, ref.st)

	for seed := int64(0); seed < 200; seed++ {
		h := newHost(t, def, prHandler)
		h.start(nil, prTrigger)
		h.runToEnd(rand.New(rand.NewSource(seed)))
		require.Equal(t, want, mustJSON(t, h.st), "seed %d", seed)
		require.Equal(t, ref.dispatched(), h.dispatched(), "seed %d", seed)
	}
}
