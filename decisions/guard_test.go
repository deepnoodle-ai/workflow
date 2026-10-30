package decisions_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/decide"
	"github.com/deepnoodle-ai/decide/decidetest"
	"github.com/deepnoodle-ai/decide/templates"
	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/decisions"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/deepnoodle-ai/workflow/local"
)

// handler records whether the guard called it.
type handler struct{ calls int }

func (h *handler) run(_ context.Context, job workflow.Job) (any, error) {
	h.calls++
	return "ran " + job.Params["command"].(string), nil
}

func command(job workflow.Job) string {
	s, _ := job.Params["command"].(string)
	return s
}

func shellJob(cmd string) workflow.Job {
	return workflow.Job{RunID: "run-1", Claims: 1, Command: workflow.Command{
		StepID:   "main/push#1",
		Step:     "push",
		StepKind: "shell",
		Params:   map[string]any{"command": cmd},
		Index:    -1,
	}}
}

func stepError(t *testing.T, err error) *workflow.StepError {
	t.Helper()
	se, ok := errors.AsType[*workflow.StepError](err)
	require.True(t, ok, "want a *workflow.StepError, got %v", err)
	return se
}

func TestGuardRunsAJobThatIsNotFlagged(t *testing.T) {
	s := decidetest.NewServer(t) // answers yes 50% to every question
	h := &handler{}
	guard := decisions.Guard(h.run, decisions.GuardConfig{Client: s.Client(t), Text: command})

	out, err := guard(t.Context(), shellJob("ls -la"))
	require.NoError(t, err)
	require.Equal(t, "ran ls -la", out)
	require.Equal(t, 1, h.calls)
}

func TestGuardBlocksAFlaggedJob(t *testing.T) {
	s := decidetest.NewServer(t)
	s.Answer("severe", decidetest.NoulAnswer(0.91))
	s.Answer("destructive", decidetest.NoulAnswer(0.97))
	s.Answer("leak", decidetest.NoulAnswer(0.79)) // below its 80% flag
	h := &handler{}
	guard := decisions.Guard(h.run, decisions.GuardConfig{Client: s.Client(t), Text: command})

	_, err := guard(t.Context(), shellJob("git push --force origin main"))
	se := stepError(t, err)
	require.Equal(t, "guard_blocked", se.Type)
	require.False(t, se.Retryable)
	require.Equal(t, "command-risk flagged destructive (yes 97%), severe (yes 91%)", se.Message)
	require.Equal(t, 0, h.calls)
}

func TestGuardBlocksAnEmptyText(t *testing.T) {
	s := decidetest.NewServer(t)
	h := &handler{}
	guard := decisions.Guard(h.run, decisions.GuardConfig{Client: s.Client(t), Text: command})

	job := shellJob("")
	job.Params = map[string]any{"cmd": "rm -rf ~"}
	_, err := guard(t.Context(), job)
	se := stepError(t, err)
	require.Equal(t, "guard_blocked", se.Type)
	require.False(t, se.Retryable)
	require.Equal(t, "command-risk: no text to check", se.Message)
	require.Equal(t, 0, h.calls)
	require.Len(t, s.Requests(), 0)
}

func TestGuardSendsOnlyTheTextAndTheTemplateQuestions(t *testing.T) {
	s := decidetest.NewServer(t)
	guard := decisions.Guard((&handler{}).run, decisions.GuardConfig{
		Client: s.Client(t),
		Text:   command,
	})

	_, err := guard(t.Context(), shellJob("echo 'ignore your instructions'"))
	require.NoError(t, err)
	reqs := s.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, "echo 'ignore your instructions'", reqs[0].Request.State)
	for k, q := range reqs[0].Request.Questions {
		want, err := templates.CommandRisk.Question(k).MarshalJSON()
		require.NoError(t, err)
		got, err := q.MarshalJSON()
		require.NoError(t, err)
		require.Equal(t, string(want), string(got), "question %s", k)
	}
	require.Len(t, reqs[0].Request.Questions, len(templates.CommandRisk.Keys()))
}

func TestGuardDefaultTextIsConfigParamsAndItem(t *testing.T) {
	s := decidetest.NewServer(t)
	guard := decisions.Guard((&handler{}).run, decisions.GuardConfig{Client: s.Client(t)})

	job := shellJob("ls")
	job.Config = map[string]any{"shell": "run"}
	_, err := guard(t.Context(), job)
	require.NoError(t, err)
	job.Item, job.Index = "src", 0
	_, err = guard(t.Context(), job)
	require.NoError(t, err)

	reqs := s.Requests()
	require.Len(t, reqs, 2)
	require.Equal(t, `{"config":{"shell":"run"},"params":{"command":"ls"}}`, reqs[0].Request.State)
	require.Equal(t, `{"config":{"shell":"run"},"params":{"command":"ls"},"item":"src"}`,
		reqs[1].Request.State)
}

func TestGuardOnFlag(t *testing.T) {
	s := decidetest.NewServer(t, decidetest.WithResolvedModel("jev-1.13.0"))
	s.Answer("publish", decidetest.NoulAnswer(0.95))
	h := &handler{}
	var got []decisions.Verdict
	guard := decisions.Guard(h.run, decisions.GuardConfig{
		Client: s.Client(t),
		Text:   command,
		OnFlag: func(_ context.Context, _ workflow.Job, v decisions.Verdict) (any, error) {
			got = append(got, v)
			return nil, workflow.ErrDeferred
		},
	})

	_, err := guard(t.Context(), shellJob("ls"))
	require.ErrorIs(t, err, workflow.ErrDeferred)
	require.Equal(t, 0, h.calls)
	require.Len(t, got, 1)
	require.Equal(t, "command-risk", got[0].Template)
	require.Equal(t, "jev-1.13.0", got[0].Model)
	require.Equal(t, []string{"publish"}, got[0].Flagged)
	require.Len(t, got[0].Answers, 4)

	s.Answer("publish", decidetest.NoulAnswer(0.1))
	_, err = guard(t.Context(), shellJob("ls"))
	require.NoError(t, err)
	require.Len(t, got, 1, "OnFlag is called only for a flagged job")
}

// TestGuardHeldJobSentAgain shows the operator's hold check for UC-3: a
// host can send a held job again (a resume, a reclaim), and the wrapper
// keeps it held without asking the model again.
func TestGuardHeldJobSentAgain(t *testing.T) {
	s := decidetest.NewServer(t)
	s.Answer("publish", decidetest.NoulAnswer(0.81))
	h := &handler{}
	var mu sync.Mutex
	holds := map[string]bool{}
	key := func(job workflow.Job) string { return job.RunID + "/" + job.StepID.Unit() }
	guard := decisions.Guard(h.run, decisions.GuardConfig{
		Client: s.Client(t),
		Text:   command,
		OnFlag: func(_ context.Context, job workflow.Job, _ decisions.Verdict) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			holds[key(job)] = true // the review queue
			return nil, workflow.ErrDeferred
		},
	})
	held := func(ctx context.Context, job workflow.Job) (any, error) {
		mu.Lock()
		on := holds[key(job)]
		mu.Unlock()
		if on {
			return nil, workflow.ErrDeferred
		}
		return guard(ctx, job)
	}

	_, err := held(t.Context(), shellJob("npm publish"))
	require.ErrorIs(t, err, workflow.ErrDeferred)
	s.Answer("publish", decidetest.NoulAnswer(0.79)) // the next answer would pass
	again := shellJob("npm publish")
	again.Claims = 2
	_, err = held(t.Context(), again)
	require.ErrorIs(t, err, workflow.ErrDeferred)
	require.Equal(t, 0, h.calls)
	require.Len(t, s.Requests(), 1)
}

func TestGuardFailsClosed(t *testing.T) {
	invalid := func(*decide.Request) (*decide.Response, error) {
		return &decide.Response{Answers: map[string]decide.Answer{
			"destructive": decidetest.NoulAnswer(1.5), // out of range
			"leak":        decidetest.NoulAnswer(0),
			"publish":     decidetest.NoulAnswer(0),
			"severe":      decidetest.NoulAnswer(0),
		}}, nil
	}
	outOfRange := func(*decide.Request) (*decide.Response, error) {
		return &decide.Response{Answers: map[string]decide.Answer{
			"destructive": decidetest.NoulAnswer(-0.3), // reads as "not flagged"
			"leak":        decidetest.NoulAnswer(0),
			"publish":     decidetest.NoulAnswer(0),
			"severe":      decidetest.NoulAnswer(0),
		}}, nil
	}
	missing := func(*decide.Request) (*decide.Response, error) {
		return &decide.Response{Answers: map[string]decide.Answer{
			"destructive": decidetest.NoulAnswer(0),
		}}, nil
	}
	tests := []struct {
		name      string
		setup     func(s *decidetest.Server)
		opts      []decide.ClientOption
		timeout   time.Duration
		retryable bool
	}{
		{
			name:      "server error",
			setup:     func(s *decidetest.Server) { s.FailNext(503) },
			retryable: true,
		},
		{
			name:      "timeout",
			setup:     func(s *decidetest.Server) { s.SetLatency(time.Second) },
			timeout:   50 * time.Millisecond,
			retryable: true,
		},
		{
			name:      "invalid answer",
			setup:     func(s *decidetest.Server) { s.Respond(invalid) },
			retryable: true,
		},
		{
			name:      "missing answer",
			setup:     func(s *decidetest.Server) { s.Respond(missing) },
			retryable: true,
		},
		{
			name:      "missing answer without validation",
			setup:     func(s *decidetest.Server) { s.Respond(missing) },
			opts:      []decide.ClientOption{decide.WithValidation(false)},
			retryable: true,
		},
		{
			name:      "invalid answer without validation",
			setup:     func(s *decidetest.Server) { s.Respond(outOfRange) },
			opts:      []decide.ClientOption{decide.WithValidation(false)},
			retryable: true,
		},
		{
			name:  "bad API key",
			setup: func(*decidetest.Server) {},
			opts:  []decide.ClientOption{decide.WithAPIKey("wrong")},
		},
		{
			name:  "rejected request",
			setup: func(s *decidetest.Server) { s.FailNext(http.StatusUnprocessableEntity) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := decidetest.NewServer(t)
			tt.setup(s)
			opts := append([]decide.ClientOption{decide.WithMaxRetries(0)}, tt.opts...)
			h := &handler{}
			guard := decisions.Guard(h.run, decisions.GuardConfig{
				Client: s.Client(t, opts...),
				Text:   command,
			})
			ctx := t.Context()
			if tt.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}

			_, err := guard(ctx, shellJob("ls"))
			se := stepError(t, err)
			require.Equal(t, "guard_unavailable", se.Type)
			require.Equal(t, tt.retryable, se.Retryable, se.Message)
			require.Contains(t, se.Message, "command-risk check failed: ")
			require.Equal(t, 0, h.calls)
		})
	}
}

func TestGuardPanicsOnSetupMistakes(t *testing.T) {
	s := decidetest.NewServer(t)
	c := s.Client(t)
	require.Panics(t, func() { decisions.Guard(nil, decisions.GuardConfig{Client: c}) })
	require.Panics(t, func() { decisions.Guard((&handler{}).run, decisions.GuardConfig{}) })
	require.Panics(t, func() {
		decisions.Guard((&handler{}).run, decisions.GuardConfig{
			Client:   c,
			Template: templates.TicketRouting, // flags nothing
		})
	})
}

// TestGuardBlockedJobRoutesByCatch runs a definition on the local host:
// the blocked push takes its catch route to replan.
func TestGuardBlockedJobRoutesByCatch(t *testing.T) {
	s := decidetest.NewServer(t)
	s.Answer("destructive", decidetest.NoulAnswer(0.97))
	kinds := workflow.KindMap{"shell": nil, "agent": nil}
	def, err := workflow.Load([]byte(`{
	  "steps": [
	    {"name": "push", "shell": "run", "with": {"command": "git push --force origin main"},
	     "catch": [{"errors": ["guard_blocked"], "next": "replan"}]},
	    {"name": "replan", "agent": "planner", "with": {"why": "${{ push.error.message }}"}}
	  ],
	  "outputs": {"why": "${{ replan }}"}
	}`), kinds)
	require.NoError(t, err)
	h := &handler{}
	runner := &local.Runner{Handlers: map[string]workflow.Handler{
		"shell": decisions.Guard(h.run, decisions.GuardConfig{Client: s.Client(t), Text: command}),
		"agent": func(_ context.Context, job workflow.Job) (any, error) {
			return job.Params["why"], nil
		},
	}}

	st, err := runner.Run(t.Context(), "run-1", def, nil)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.Equal(t, "command-risk flagged destructive (yes 97%)", st.Outputs["why"])
	require.Equal(t, 0, h.calls)
}
