package postgres

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestUnloadableDefinitionFailsTheRun runs workers whose store does not know
// a kind the run's definition uses, a host configuration mistake. The
// worker does not run handlers for the run, gives the configuration a few
// leases to be fixed, then fails the run visibly and drops its jobs.
func TestUnloadableDefinitionFailsTheRun(t *testing.T) {
	for name, doc := range map[string]string{
		"step":  `{"steps": [{"name": "a", "action": "x", "next": ["b"]}, {"name": "b", "agent": "y"}]}`,
		"timer": `{"steps": [{"name": "nap", "sleep": "1ms", "next": ["b"]}, {"name": "b", "agent": "y"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			_, err := s.Start(ctx, NewRun{ID: name, Definition: load(t, doc)})
			require.NoError(t, err)

			// Another process, configured without the agent kind.
			cold, err := New(s.pool, workflow.KindMap{"action": nil}, WithSchema(s.schema))
			require.NoError(t, err)
			var calls atomic.Int32
			startWorker(t, &Worker{
				Store:     cold,
				Lease:     100 * time.Millisecond,
				MaxClaims: 3,
				Handlers: map[string]workflow.Handler{
					"action": func(ctx context.Context, job workflow.Job) (any, error) {
						calls.Add(1)
						return nil, nil
					},
				},
			})
			r := waitStatus(t, s, name, workflow.StatusFailed)
			require.Equal(t, ErrorTypeDefinitionUnavailable, r.State.Error.Type)
			require.Equal(t, int32(0), calls.Load())
			require.Equal(t, 0, countJobs(t, s, name))
			require.Len(t, r.State.InFlight(), 0)
			events, err := s.Events(ctx, name)
			require.NoError(t, err)
			require.Equal(t, EventAborted, events[len(events)-1].Kind)

			_, err = cold.Deliver(ctx, name, workflow.Event{Kind: workflow.EventCancel})
			require.ErrorIs(t, err, ErrDefinitionUnavailable)
		})
	}
}

// TestStartRejectsUnloadableDefinition: a store whose kinds lack a kind
// the definition uses starts nothing, and says why.
func TestStartRejectsUnloadableDefinition(t *testing.T) {
	s := newStore(t)
	cold, err := New(s.pool, workflow.KindMap{"action": nil}, WithSchema(s.schema))
	require.NoError(t, err)

	def := load(t, `{"steps": [{"name": "a", "agent": "y"}]}`)
	_, err = cold.Start(context.Background(), NewRun{ID: "cold", Definition: def})
	require.ErrorIs(t, err, ErrDefinitionUnavailable)
	require.Contains(t, err.Error(), "agent")

	_, err = s.GetRun(context.Background(), "cold")
	require.ErrorIs(t, err, ErrRunNotFound)
}

// TestTimerPastMaxClaimsFailsTheRun: a timer that keeps failing to fire
// is bounded by MaxClaims like a step.
func TestTimerPastMaxClaimsFailsTheRun(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "nap", "sleep": "1ms", "next": ["a"]}, {"name": "a", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "stuck-timer", Definition: def})
	require.NoError(t, err)
	var settles atomic.Int32
	startWorker(t, &Worker{
		Store:     s,
		Lease:     100 * time.Millisecond,
		MaxClaims: 2,
		Handlers:  map[string]workflow.Handler{"action": nil},
		settleHook: func(t claimedJob) error {
			settles.Add(1)
			return context.DeadlineExceeded // never delivered, as if the database timed out
		},
	})
	r := waitStatus(t, s, "stuck-timer", workflow.StatusFailed)
	require.Equal(t, "abandoned", r.State.Error.Type)
	require.Equal(t, 0, countJobs(t, s, "stuck-timer"))
	n := settles.Load()
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, n, settles.Load())
}
