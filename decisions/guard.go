package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/deepnoodle-ai/decide"
	"github.com/deepnoodle-ai/decide/templates"
	"github.com/deepnoodle-ai/workflow"
)

// GuardConfig configures [Guard].
type GuardConfig struct {
	// Client asks the questions. Required.
	Client *decide.Client
	// Template holds the questions and their flags. Default:
	// templates.CommandRisk, whose questions ask about a shell command; pick
	// a template that fits any other kind.
	Template *templates.Template
	// Text returns what the model judges for a job. "" blocks the job with
	// guard_blocked: the guard never runs a job it did not check. Default:
	// the JSON of the job's Config, Params and, for an each step, Item, or
	// "" when all three are empty.
	Text func(workflow.Job) string
	// OnFlag, if set, handles a flagged job in place of the guard_blocked
	// error, such as by queueing it for a person and returning
	// workflow.ErrDeferred. Guard returns what it returns.
	OnFlag func(ctx context.Context, job workflow.Job, v Verdict) (any, error)
}

// Verdict is what the model answered for one job.
type Verdict struct {
	Template string                   // the template's name, such as "command-risk"
	Model    string                   // the model that answered, as decide resolved it
	Answers  map[string]decide.Answer // every question's answer, by key
	Flagged  []string                 // the keys the template flags, sorted
}

// Guard returns a handler that asks a decision model about each job before
// it calls next. It asks every question of the template in one request,
// with the job's text as the request's state, and calls next only when the
// template flags no answer.
//
// A flagged job returns a *workflow.StepError of type "guard_blocked" that
// is not retryable, which a definition can route with catch, unless
// cfg.OnFlag handles it. When the guard cannot get a full verdict, the job
// does not run: it returns a StepError of type "guard_unavailable", which
// is retryable unless decide rejected the request for good (a 4xx other
// than 408 and 429, such as a bad API key or a text that is too large).
//
// Guard panics when next or cfg.Client is nil, or when the template flags
// no question.
func Guard(next workflow.Handler, cfg GuardConfig) workflow.Handler {
	if next == nil {
		panic("decisions: Guard with nil next")
	}
	if cfg.Client == nil {
		panic("decisions: Guard with nil Client")
	}
	t := cfg.Template
	if t == nil {
		t = templates.CommandRisk
	}
	keys := slices.Sorted(slices.Values(t.Keys()))
	if !slices.ContainsFunc(keys, func(k string) bool { return len(t.Flag(k)) > 0 }) {
		panic("decisions: template " + t.Name() + " flags no question")
	}
	text := cfg.Text
	if text == nil {
		text = defaultText
	}

	return func(ctx context.Context, job workflow.Job) (any, error) {
		s := text(job)
		if s == "" {
			msg := t.Name() + ": no text to check"
			return nil, &workflow.StepError{Type: "guard_blocked", Message: msg}
		}
		v, err := ask(ctx, cfg.Client, t, keys, s)
		if err != nil {
			return nil, &workflow.StepError{
				Type:      "guard_unavailable",
				Message:   t.Name() + " check failed: " + err.Error(),
				Retryable: !permanent(err),
			}
		}
		if len(v.Flagged) == 0 {
			return next(ctx, job)
		}
		if cfg.OnFlag != nil {
			return cfg.OnFlag(ctx, job, v)
		}
		return nil, &workflow.StepError{Type: "guard_blocked", Message: blocked(t, v)}
	}
}

// ask asks the template's questions about s and reads the verdict. Any
// answer that is missing, of the wrong type or invalid is an error, so a
// partial verdict never lets a job run. The call validates whatever the
// client's setting, and ask checks each answer again: an out-of-range
// probability must not read as "not flagged".
func ask(
	ctx context.Context, c *decide.Client, t *templates.Template, keys []string, s string,
) (Verdict, error) {
	req := decide.NewRequest(s)
	for _, k := range keys {
		req.Questions[k] = t.Question(k)
	}
	resp, err := c.SystemOne(ctx, req, decide.WithCallValidation(true))
	if err != nil {
		return Verdict{}, err
	}

	v := Verdict{Template: t.Name(), Model: resp.Model, Answers: map[string]decide.Answer{}}
	for _, k := range keys {
		a := resp.Answers[k]
		if err := check(req.Questions[k], a); err != nil {
			return Verdict{}, fmt.Errorf("answer for %s: %w", k, err)
		}
		v.Answers[k] = a
		if t.Flagged(k, a) {
			v.Flagged = append(v.Flagged, k)
		}
	}
	return v, nil
}

// check reports why a is not a valid answer to q. A question with no
// validator fails too, so an answer the guard cannot check never passes.
func check(q decide.Question, a decide.Answer) error {
	if a == nil {
		return errors.New("missing")
	}
	if a.AnswerType() != q.QuestionType() {
		return fmt.Errorf("is %s, want %s", a.AnswerType(), q.QuestionType())
	}
	v, ok := q.(decide.AnswerValidator)
	if !ok {
		return fmt.Errorf("cannot check a %s answer", a.AnswerType())
	}
	return v.ValidateAnswer(a)
}

// permanent reports whether decide rejected the request in a way that a
// retry of the same job cannot fix: a 4xx other than 408 and 429, which
// covers a bad key and a text that is too large, or a request the client
// refused before it sent it.
func permanent(err error) bool {
	if errors.Is(err, decide.ErrNoAPIKey) || errors.Is(err, decide.ErrInvalidRequest) {
		return true
	}
	ae, ok := errors.AsType[*decide.APIError](err)
	if !ok {
		return false
	}
	code := ae.StatusCode
	return code >= 400 && code < 500 &&
		code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
}

// defaultText is the JSON of what a handler reads from the job, or "" when
// the job has none of it, so that an empty job is blocked.
func defaultText(job workflow.Job) string {
	v := struct {
		Config map[string]any `json:"config,omitempty"`
		Params map[string]any `json:"params,omitempty"`
		Item   any            `json:"item,omitempty"`
	}{Config: job.Config, Params: job.Params}
	if job.Index >= 0 {
		v.Item = job.Item
	}
	if len(v.Config) == 0 && len(v.Params) == 0 && v.Item == nil {
		return ""
	}
	// Keep &, < and > as they are: escaped, they hide shell chaining and
	// redirection from the model.
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "" // blocks the job
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// blocked is the message of a guard_blocked error: the template, then
// each flagged question with its most likely answer.
func blocked(t *templates.Template, v Verdict) string {
	parts := make([]string, len(v.Flagged))
	for i, k := range v.Flagged {
		parts[i] = k + " (" + describe(v.Answers[k]) + ")"
	}
	return t.Name() + " flagged " + strings.Join(parts, ", ")
}

func describe(a decide.Answer) string {
	switch a := a.(type) {
	case *decide.NoulAnswer:
		if a.Noul >= 0.5 {
			return "yes " + percent(a.Noul)
		}
		return "no " + percent(1-a.Noul)
	case *decide.ChoiceAnswer:
		return a.Choice + " " + percent(a.Probabilities[a.Choice])
	case *decide.ScoreAnswer:
		return fmt.Sprintf("score %g", a.Score)
	}
	return a.AnswerType()
}

func percent(p float64) string {
	return fmt.Sprintf("%d%%", int(math.Round(p*100)))
}
