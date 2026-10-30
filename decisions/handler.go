package decisions

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"

	"github.com/deepnoodle-ai/decide"
	"github.com/deepnoodle-ai/workflow"
)

// question is one question of a decision job, as the engine sends it.
type question struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"` // decide or choose
	Question string   `json:"question"`
	Subject  string   `json:"subject"`
	Options  []string `json:"options"`
}

// Handler returns the handler of the decision kind, [workflow.KindDecision].
// It answers the decide and choose calls in a step's next block with a
// decision model:
//
//	handlers[workflow.KindDecision] = decisions.Handler(client)
//
// It asks the questions that share a subject in one request, with the
// subject as the request's state, so a job whose questions are all about
// one subject makes one request; the requests for several subjects go
// out at the same time. A decide question is a noul question
// and its answer the probability of yes; a choose question is a choice
// question over its options and its answer the option the model chose.
//
// It checks the answers as [Guard] does. Any error is decide's, as it
// is: the engine retries every failed decision, up to 3 attempts. Do not
// wrap the handler in [Guard].
//
// Handler panics when client is nil.
func Handler(client *decide.Client) workflow.Handler {
	if client == nil {
		panic("decisions: Handler with nil client")
	}
	return func(ctx context.Context, job workflow.Job) (any, error) {
		var params struct {
			Questions []question `json:"questions"`
		}
		data, err := json.Marshal(job.Params)
		if err == nil {
			err = json.Unmarshal(data, &params)
		}
		if err != nil {
			return nil, fmt.Errorf("decisions: %s is not a decision job: %w", job.StepID, err)
		}

		bySubject := map[string][]question{}
		for _, q := range params.Questions {
			bySubject[q.Subject] = append(bySubject[q.Subject], q)
		}
		// The first error cancels the other requests: the retry asks
		// every subject again.
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			firstErr error
		)
		answers := make(map[string]any, len(params.Questions))
		for subject, qs := range bySubject {
			wg.Go(func() {
				got, err := answer(ctx, client, subject, qs)
				mu.Lock()
				defer mu.Unlock()
				if err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				maps.Copy(answers, got)
			})
		}
		wg.Wait()
		if firstErr != nil {
			return nil, firstErr
		}
		return answers, nil
	}
}

// answer asks qs about subject in one request and returns the answers by
// question ID.
func answer(
	ctx context.Context, c *decide.Client, subject string, qs []question,
) (map[string]any, error) {
	req := decide.NewRequest(subject)
	for _, q := range qs {
		switch q.Type {
		case "decide":
			req.Questions[q.ID] = decide.Noul(q.Question)
		case "choose":
			opts := make([]decide.ChoiceOption, len(q.Options))
			for i, o := range q.Options {
				opts[i] = decide.Option(o)
			}
			req.Questions[q.ID] = decide.Choice(q.Question, opts...)
		default:
			return nil, fmt.Errorf("decisions: question %s has type %q, want decide or choose",
				q.ID, q.Type)
		}
	}
	resp, err := c.SystemOne(ctx, req, decide.WithCallValidation(true))
	if err != nil {
		return nil, err
	}

	answers := make(map[string]any, len(qs))
	for _, q := range qs {
		if q.Type == "decide" {
			a, err := decide.AnswerAs[*decide.NoulAnswer](resp, q.ID)
			if err != nil {
				return nil, err
			}
			answers[q.ID] = a.Noul
			continue
		}
		a, err := decide.AnswerAs[*decide.ChoiceAnswer](resp, q.ID)
		if err != nil {
			return nil, err
		}
		answers[q.ID] = a.Choice
	}
	return answers, nil
}
