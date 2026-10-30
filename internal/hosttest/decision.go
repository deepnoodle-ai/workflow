package hosttest

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// scriptedAnswers answers a decision job by each question's text, so every
// host sees the same answers.
func scriptedAnswers(job workflow.Job) (any, error) {
	out := map[string]any{}
	for _, q := range job.Params["questions"].([]any) {
		q := q.(map[string]any)
		id := q["id"].(string)
		switch q["question"] {
		case "Is this refund legitimate?":
			out[id] = 0.55
		case "Which team owns this?":
			out[id] = "engineering"
		case "Does the draft address every comment?":
			out[id] = 0.5
			if strings.Contains(q["subject"].(string), "@3") {
				out[id] = 0.95
			}
		case "Is it urgent?":
			// Not retryable, so the case does not wait out the backoff.
			return nil, &workflow.StepError{Type: "decision_failed", Message: "provider down"}
		default:
			out[id] = 0.9
		}
	}
	return out, nil
}

var decisionCases = []conformanceCase{
	{
		name: "unsure_goes_to_a_human",
		doc: `{"steps": [
		  {"name": "intake", "action": "x", "match": "first", "next": [
		    {"step": "approve", "when": "decide(\"Is this refund legitimate?\", intake.unit) > 0.9"},
		    {"step": "deny", "when": "decide(\"Is this refund legitimate?\", intake.unit) < 0.2"},
		    "human_review"]},
		  {"name": "approve", "action": "x"},
		  {"name": "deny", "action": "x"},
		  {"name": "human_review", "agent": "a"}]}`,
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusCompleted, s.Status)
			require.Equal(t, []workflow.StepID{
				"main/human_review#1", "main/intake#1", "main/intake.next#1",
			}, s.Dispatched)
		},
	},
	{
		name: "switch_on_choose",
		doc: `{"steps": [
		  {"name": "intake", "action": "x", "next": {
		    "switch": "choose(\"Which team owns this?\", intake, [\"billing\", \"engineering\", \"other\"])",
		    "cases": {"billing": "billing_q", "engineering": "eng_q", "other": "triage"}}},
		  {"name": "billing_q", "action": "x"},
		  {"name": "eng_q", "action": "x"},
		  {"name": "triage", "action": "x"}]}`,
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusCompleted, s.Status)
			require.Contains(t, s.Dispatched, workflow.StepID("main/eng_q#1"))
		},
	},
	{
		name: "revise_until_done",
		doc: `{"steps": [
		  {"name": "draft", "agent": "a", "max_visits": 5, "next": [
		    {"step": "draft", "when": "decide(\"Does the draft address every comment?\", draft.unit) < 0.9"},
		    {"step": "publish", "when": "decide(\"Does the draft address every comment?\", draft.unit) >= 0.9"}]},
		  {"name": "publish", "action": "x", "with": {"draft": "${{ draft.unit }}"}}],
		 "outputs": {"published": "${{ publish.params.draft }}"}}`,
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusCompleted, s.Status)
			require.Equal(t, `{"published":"main/draft@3"}`, s.Outputs)
			require.Contains(t, s.Dispatched, workflow.StepID("main/draft@3.next#1"))
		},
	},
	{
		name: "failed_decision_caught",
		doc: `{"steps": [
		  {"name": "intake", "action": "x", "retry": {"max_attempts": 4, "backoff": "1ms"},
		   "catch": "inbox", "match": "first", "next": [
		    {"step": "page", "when": "decide(\"Is it urgent?\", intake) > 0.8"}, "inbox"]},
		  {"name": "page", "action": "x"},
		  {"name": "inbox", "agent": "a", "with": {"why": "${{ intake?.error?.message }}"}}],
		 "outputs": {"why": "${{ inbox.params.why }}"}}`,
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusCompleted, s.Status)
			require.Equal(t, `{"why":"provider down"}`, s.Outputs)
			require.Contains(t, s.Dispatched, workflow.StepID("main/intake.next#1"))
			require.NotContains(t, s.Dispatched, workflow.StepID("main/intake.next#2"))
		},
	},
	{
		name: "decide_after_a_join",
		doc: `{"steps": [
		  {"name": "start", "action": "x", "next": ["tests", "lint"]},
		  {"name": "tests", "action": "x", "next": ["check"]},
		  {"name": "lint", "action": "x", "next": ["check"]},
		  {"name": "check", "join": ["tests", "lint"], "match": "first", "next": [
		    {"step": "ship", "when": "decide(\"Ready to merge?\", {\"tests\": tests.unit, \"lint\": lint.unit}) > 0.8"},
		    "fix"]},
		  {"name": "ship", "action": "x"},
		  {"name": "fix", "action": "x"}]}`,
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusCompleted, s.Status)
			require.Contains(t, s.Dispatched, workflow.StepID("main/check.next#1"))
			require.Contains(t, s.Dispatched, workflow.StepID("main/ship#1"))
		},
	},
	{
		name: "decide_after_a_sleep",
		doc: `{"steps": [
		  {"name": "nap", "sleep": "1ms", "match": "first", "next": [
		    {"step": "go", "when": "decide(\"Go on?\", input) > 0.5"}, "stop"]},
		  {"name": "go", "action": "x"},
		  {"name": "stop", "action": "x"}]}`,
		check: func(t *testing.T, s runSummary) {
			require.Equal(t, workflow.StatusCompleted, s.Status)
			require.Equal(t, []workflow.StepID{"main/go#1", "main/nap.next#1"}, s.Dispatched)
		},
	},
}

// testDecisionsOnEveryHost runs the decision use cases on the pure
// driver, local and the backend, which must agree.
func testDecisionsOnEveryHost(t *testing.T, h Harness) {
	runConformance(t, []conformanceHost{applyHost{}, localHost{}, backendHost{h}},
		decisionCases, scripted)
}

// testDecisionAskedOnceAndRecorded checks that a durable host asks each
// decision once, and that its step records show the question and the
// answer, which is the run's record of why it took a route.
func testDecisionAskedOnceAndRecorded(t *testing.T, h Harness) {
	s := open(t, h)
	var mu sync.Mutex
	asked := map[string]int{}
	handler := func(ctx context.Context, job workflow.Job) (any, error) {
		if job.StepKind == workflow.KindDecision {
			mu.Lock()
			asked[job.StepID.Unit()]++
			mu.Unlock()
		}
		return scripted(ctx, job)
	}
	for range 2 {
		startWorker(t, &Worker{
			Store: s,
			Handlers: map[string]workflow.Handler{
				"agent":               handler,
				"action":              handler,
				workflow.KindDecision: handler,
			},
		})
	}
	ctx := context.Background()
	res, err := s.Start(ctx, NewRun{Definition: load(t, decisionCases[0].doc)})
	require.NoError(t, err)
	r := waitStatus(t, s, res.RunID, workflow.StatusCompleted, workflow.StatusFailed)
	require.Equal(t, workflow.StatusCompleted, r.State.Status)
	mu.Lock()
	require.Equal(t, map[string]int{"main/intake.next": 1}, asked)
	mu.Unlock()

	records, err := s.Steps(ctx, res.RunID)
	require.NoError(t, err)
	var found bool
	for _, rec := range records {
		if rec.StepID != "main/intake.next#1" {
			continue
		}
		found = true
		require.Equal(t, workflow.KindDecision, rec.StepKind)
		require.Equal(t, "intake", rec.Step)
		require.Equal(t, StepCompleted, rec.Status)
		q := rec.Params["questions"].([]any)[0].(map[string]any)
		require.Equal(t, "Is this refund legitimate?", q["question"])
		require.Equal(t, map[string]any{q["id"].(string): 0.55}, rec.Output)
	}
	require.True(t, found, "no step record for the decision")
}
