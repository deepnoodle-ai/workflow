package decisions_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/deepnoodle-ai/decide"
	"github.com/deepnoodle-ai/decide/decidetest"
	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/decisions"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// decisionJob is a decision job with the given questions, shaped as the
// engine sends it after a JSON round trip.
func decisionJob(questions ...map[string]any) workflow.Job {
	list := make([]any, len(questions))
	for i, q := range questions {
		list[i] = q
	}
	return workflow.Job{RunID: "run-1", Claims: 1, Command: workflow.Command{
		StepID:   "main/intake.next#1",
		Step:     "intake",
		StepKind: workflow.KindDecision,
		Params:   map[string]any{"questions": list},
		Index:    -1,
	}}
}

var (
	legit = map[string]any{
		"id":       "a1",
		"type":     "decide",
		"question": "Is this refund legitimate?",
		"subject":  "The blender arrived in pieces.",
	}
	team = map[string]any{
		"id":       "b2",
		"type":     "choose",
		"question": "Which team owns this?",
		"subject":  "The blender arrived in pieces.",
		"options":  []any{"billing", "engineering"},
	}
	ready = map[string]any{
		"id":       "c3",
		"type":     "decide",
		"question": "Is this change ready to merge?",
		"subject":  `{"diff":"+x","failures":[]}`,
	}
)

func TestHandlerAsksTheQuestionsOfASubjectInOneRequest(t *testing.T) {
	s := decidetest.NewServer(t)
	s.Answer("a1", decidetest.NoulAnswer(0.93))
	s.Answer("b2", decidetest.ChoiceAnswer(map[string]float64{"billing": 0.2, "engineering": 0.8}))
	s.Answer("c3", decidetest.NoulAnswer(0.4))
	handler := decisions.Handler(s.Client(t))

	out, err := handler(t.Context(), decisionJob(ready, team, legit))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a1": 0.93, "b2": "engineering", "c3": 0.4}, out)

	reqs := s.Requests() // one per subject, sent at the same time
	require.Len(t, reqs, 2)
	first, second := reqs[0].Request, reqs[1].Request
	if len(first.Questions) == 1 {
		first, second = second, first
	}
	require.Equal(t, any("The blender arrived in pieces."), first.State)
	require.Len(t, first.Questions, 2)
	require.Equal(t, decide.Question(decide.Noul("Is this refund legitimate?")), first.Questions["a1"])
	choice := decide.Choice("Which team owns this?",
		decide.Option("billing"), decide.Option("engineering"))
	require.Equal(t, decide.Question(choice), first.Questions["b2"])
	require.Equal(t, any(`{"diff":"+x","failures":[]}`), second.State) // as is, not re-encoded
	require.Len(t, second.Questions, 1)
}

func TestHandlerReportsDecidesError(t *testing.T) {
	s := decidetest.NewServer(t)
	s.FailNext(http.StatusServiceUnavailable)
	handler := decisions.Handler(s.Client(t, decide.WithMaxRetries(0)))

	_, err := handler(t.Context(), decisionJob(legit))
	ae, ok := errors.AsType[*decide.APIError](err)
	require.True(t, ok, "want a *decide.APIError, got %v", err)
	require.Equal(t, http.StatusServiceUnavailable, ae.StatusCode)
}

func TestHandlerCancelsTheOtherRequestsOnAnError(t *testing.T) {
	s := decidetest.NewServer(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // runs before the server closes
	s.Respond(func(req *decide.Request) (*decide.Response, error) {
		if req.State == legit["subject"] {
			<-release // never answers while the handler waits
		}
		return nil, &decide.APIError{StatusCode: http.StatusBadRequest}
	})
	handler := decisions.Handler(s.Client(t, decide.WithMaxRetries(0)))

	_, err := handler(t.Context(), decisionJob(legit, ready))
	ae, ok := errors.AsType[*decide.APIError](err)
	require.True(t, ok, "want the failed request's *decide.APIError, got %v", err)
	require.Equal(t, http.StatusBadRequest, ae.StatusCode)
}

func TestHandlerFailsOnAMissingAnswer(t *testing.T) {
	s := decidetest.NewServer(t)
	s.Respond(func(*decide.Request) (*decide.Response, error) {
		return &decide.Response{Answers: map[string]decide.Answer{}}, nil
	})
	handler := decisions.Handler(s.Client(t))

	_, err := handler(t.Context(), decisionJob(legit))
	ae, ok := errors.AsType[*decide.AnswerError](err)
	require.True(t, ok, "want a *decide.AnswerError, got %v", err)
	require.Equal(t, decide.ReasonMissingAnswer, ae.Reason)
}

func TestHandlerAnswersOnlyChooseQuestions(t *testing.T) {
	s := decidetest.NewServer(t) // chooses the first option
	handler := decisions.Handler(s.Client(t))

	out, err := handler(t.Context(), decisionJob(team))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"b2": "billing"}, out)
}

func TestHandlerChecksTheAnswersWhateverTheClientSetting(t *testing.T) {
	s := decidetest.NewServer(t)
	s.Answer("b2", decidetest.ChoiceAnswer(map[string]float64{"sales": 1}))
	handler := decisions.Handler(s.Client(t, decide.WithValidation(false)))

	_, err := handler(t.Context(), decisionJob(team))
	ae, ok := errors.AsType[*decide.AnswerError](err)
	require.True(t, ok, "want a *decide.AnswerError, got %v", err)
	require.Equal(t, decide.ReasonProbabilityKeys, ae.Reason)
}

func TestHandlerRejectsAnUnknownQuestionType(t *testing.T) {
	s := decidetest.NewServer(t)
	handler := decisions.Handler(s.Client(t))
	score := map[string]any{"id": "d4", "type": "score", "question": "How urgent?", "subject": "x"}

	_, err := handler(t.Context(), decisionJob(score))
	require.Error(t, err)
	require.Equal(t, `decisions: question d4 has type "score", want decide or choose`, err.Error())
	require.Len(t, s.Requests(), 0)
}

func TestHandlerPanicsWithoutAClient(t *testing.T) {
	require.Panics(t, func() { decisions.Handler(nil) })
}
