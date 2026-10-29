package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need Postgres. Set WORKFLOW_PG_DSN, for example:
//
//	docker run -d -e POSTGRES_PASSWORD=pg -p 127.0.0.1:55441:5432 postgres:18
//	WORKFLOW_PG_DSN="postgres://postgres:pg@127.0.0.1:55441/postgres?sslmode=disable" go test ./...
//
// Each test uses its own schema.
const dsnEnv = "WORKFLOW_PG_DSN"

var kinds = workflow.KindMap{"agent": nil, "action": nil}

func newStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s not set", dsnEnv)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	schema := "t_" + uuid.NewV4().String()[:8]
	s, err := New(pool, kinds, WithSchema(schema))
	require.NoError(t, err)
	require.NoError(t, s.Migrate(context.Background()))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		pool.Close()
	})
	return s
}

func load(t *testing.T, doc string) *workflow.Definition {
	t.Helper()
	def, err := workflow.Load([]byte(doc), kinds)
	require.NoError(t, err)
	return def
}

func startWorker(t *testing.T, w *Worker) {
	t.Helper()
	if w.Poll == 0 {
		w.Poll = 20 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitStatus(t *testing.T, s *Store, runID string, want ...workflow.RunStatus) *Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		r, err := s.GetRun(context.Background(), runID)
		require.NoError(t, err)
		if slices.Contains(want, r.Status) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is %s, want %v", runID, r.Status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPRReviewOnPostgres(t *testing.T) {
	s := newStore(t)
	doc, err := os.ReadFile("testdata/pr_review.json")
	require.NoError(t, err)
	def := load(t, string(doc))

	var mu sync.Mutex
	statuses := []string{}
	handler := func(ctx context.Context, job workflow.Job) (any, error) {
		switch job.Step {
		case "correctness":
			return map[string]any{"items": []any{"real: nil deref", "style nit"}}, nil
		case "contracts":
			return map[string]any{"items": []any{"real: API break"}}, nil
		case "tests":
			return map[string]any{"items": []any{}}, nil
		case "verify":
			f := job.Params["finding"].(string)
			return map[string]any{"finding": f, "confirmed": strings.HasPrefix(f, "real")}, nil
		case "report":
			return map[string]any{
				"verdict": "REQUEST_CHANGES",
				"status":  "failure",
				"body":    "2 findings",
			}, nil
		}
		if job.Config["action"] == "github.commit.set_status" {
			mu.Lock()
			statuses = append(statuses, job.Params["state"].(string))
			mu.Unlock()
		}
		return map[string]any{"ok": true}, nil
	}
	for range 3 {
		startWorker(t, &Worker{
			Store: s,
			Handlers: map[string]workflow.Handler{
				"agent":  handler,
				"action": handler,
			},
		})
	}

	_, err = s.Start(context.Background(), NewRun{
		ID:         "pr-42-abc123",
		Definition: def,
		Input: map[string]any{"event": map[string]any{
			"repository":   map[string]any{"full_name": "deepnoodle-ai/workflow"},
			"pull_request": map[string]any{"number": 42, "head": map[string]any{"sha": "abc123"}},
		}},
		Metadata: map[string]any{"pr": 42},
	})
	require.NoError(t, err)
	r := waitStatus(t, s, "pr-42-abc123", workflow.StatusCompleted, workflow.StatusFailed)
	require.Equal(t, workflow.StatusCompleted, r.Status)
	require.Equal(t, []string{"pending", "failure"}, statuses)

	steps, err := s.Steps(context.Background(), "pr-42-abc123")
	require.NoError(t, err)
	require.Len(t, steps, 10)
	for _, st := range steps {
		require.Equal(t, StepCompleted, st.Status, st.StepID)
	}
	events, err := s.Events(context.Background(), "pr-42-abc123")
	require.NoError(t, err)
	require.Equal(t, workflow.EventStart, events[0].Kind)
	require.Len(t, events, 11)
}

const fanOutDoc = `{"steps": [
  {"name": "start", "action": "x", "next": ["r0", "r1", "r2", "r3", "r4", "r5", "r6", "r7"]},
  {"name": "r0", "agent": "a", "next": ["gather"]}, {"name": "r1", "agent": "a", "next": ["gather"]},
  {"name": "r2", "agent": "a", "next": ["gather"]}, {"name": "r3", "agent": "a", "next": ["gather"]},
  {"name": "r4", "agent": "a", "next": ["gather"]}, {"name": "r5", "agent": "a", "next": ["gather"]},
  {"name": "r6", "agent": "a", "next": ["gather"]}, {"name": "r7", "agent": "a", "next": ["gather"]},
  {"name": "gather", "join": ["r0", "r1", "r2", "r3", "r4", "r5", "r6", "r7"], "next": ["after"]},
  {"name": "after", "action": "x", "with": {"sum": "${{ r0 + r1 + r2 + r3 + r4 + r5 + r6 + r7 }}"}}
]}`

// TestConcurrentSiblingCompletions releases eight sibling completions at the
// same moment, from separate transactions, across many runs. The join must
// fire exactly once and see every sibling.
func TestConcurrentSiblingCompletions(t *testing.T) {
	s := newStore(t)
	def := load(t, fanOutDoc)
	var afterCalls atomic.Int32
	sums := sync.Map{}
	startWorker(t, &Worker{
		Store:       s,
		Concurrency: 16,
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				return nil, workflow.ErrDeferred
			},
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.Step == "after" {
					afterCalls.Add(1)
					sums.Store(job.RunID, job.Params["sum"])
				}
				return nil, nil
			},
		},
	})

	const runs = 12
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range runs {
		runID := fmt.Sprintf("fan-%d", i)
		_, err := s.Start(ctx, NewRun{ID: runID, Definition: def})
		require.NoError(t, err)
		wg.Go(func() {
			waitFor(t, "eight deferred siblings", func() bool {
				r, err := s.GetRun(ctx, runID)
				return err == nil && len(r.State.InFlight()) == 8 && countJobs(t, s, runID) == 0
			})
			r, _ := s.GetRun(ctx, runID)
			gate := make(chan struct{})
			var sib sync.WaitGroup
			for i, id := range r.State.InFlight() {
				sib.Go(func() {
					<-gate
					_, err := s.Deliver(ctx, runID, workflow.Event{
						Kind:   workflow.EventStepCompleted,
						StepID: id,
						Output: i + 1,
					})
					if err != nil {
						t.Errorf("deliver %s: %v", id, err)
					}
				})
			}
			close(gate)
			sib.Wait()
		})
	}
	wg.Wait()
	for i := range runs {
		id := fmt.Sprintf("fan-%d", i)
		r := waitStatus(t, s, id, workflow.StatusCompleted, workflow.StatusFailed)
		require.Equal(t, workflow.StatusCompleted, r.Status)
		require.Equal(t, 1, countSteps(t, s, r.ID, "after"), "the join fired once")
	}
	require.Equal(t, int32(runs), afterCalls.Load())
	sums.Range(func(_, v any) bool {
		require.Equal(t, 36.0, v) // 1+2+...+8
		return true
	})
}

func countSteps(t *testing.T, s *Store, runID, step string) int {
	t.Helper()
	records, err := s.Steps(context.Background(), runID)
	require.NoError(t, err)
	n := 0
	for _, r := range records {
		if r.Step == step {
			n++
		}
	}
	return n
}

func countJobs(t *testing.T, s *Store, runID string) int {
	var n int
	require.NoError(t, s.pool.QueryRow(context.Background(),
		s.sql(`SELECT count(*) FROM {{schema}}.jobs WHERE run_id = $1`), runID).Scan(&n))
	return n
}

// TestDuplicateDeliveries delivers every completion three times at once.
func TestDuplicateDeliveries(t *testing.T) {
	s := newStore(t)
	def := load(t, fanOutDoc)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "dup", Definition: def})
	require.NoError(t, err)
	_, err = s.Deliver(ctx, "dup",
		workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/start#1"})
	require.NoError(t, err)
	r, err := s.GetRun(ctx, "dup")
	require.NoError(t, err)
	ids := r.State.InFlight()
	require.Len(t, ids, 8)

	var wg sync.WaitGroup
	for range 3 {
		for _, id := range ids {
			wg.Go(func() {
				_, err := s.Deliver(ctx, "dup", workflow.Event{
					Kind:   workflow.EventStepCompleted,
					StepID: id,
					Output: 1,
				})
				if err != nil {
					t.Errorf("deliver: %v", err)
				}
			})
		}
	}
	wg.Wait()
	r, err = s.GetRun(ctx, "dup")
	require.NoError(t, err)
	require.Equal(t, []workflow.StepID{"main/after#1"}, r.State.InFlight())
	events, err := s.Events(ctx, "dup")
	require.NoError(t, err)
	require.Len(t, events, 1+1+8, "duplicates are not recorded")
}

// TestExpiredLeaseIsReclaimed simulates a worker that claims a step and
// dies. Its lease expires, another worker runs the step, and the dead
// worker's late result is fenced out.
func TestExpiredLeaseIsReclaimed(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "a", "action": "x", "next": ["b"]}, {"name": "b", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "crash", Definition: def})
	require.NoError(t, err)

	dead := &Worker{
		Store:    s,
		ID:       "dead",
		Lease:    200 * time.Millisecond,
		Handlers: map[string]workflow.Handler{"action": nil},
	}
	dead.defaults()
	claimed, err := dead.claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, workflow.StepID("main/a#1"), claimed[0].StepID)

	var runsOfA atomic.Int32
	startWorker(t, &Worker{
		Store: s,
		ID:    "alive",
		Lease: time.Second,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.Step == "a" {
					runsOfA.Add(1)
					require.Equal(t, 2, job.Claims)
				}
				return job.Step, nil
			},
		},
	})
	r := waitStatus(t, s, "crash", workflow.StatusCompleted)
	require.Equal(t, map[string]any{"b": "b"}, r.State.Outputs)
	require.Equal(t, int32(1), runsOfA.Load())

	// The dead worker comes back and reports: fenced out, nothing changes.
	dead.settle(ctx, claimed[0], time.Now(), workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "main/a#1",
		Output: "stale",
	}, dead.Logger)
	events, err := s.Events(ctx, "crash")
	require.NoError(t, err)
	require.Len(t, events, 3)
}

// TestCancelStopsRunningHandler cancels a run while a handler is running.
// The heartbeat notices the job is gone and cancels the handler.
func TestCancelStopsRunningHandler(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "slow", "action": "x"}],
	  "on_failure": [{"action": "x", "with": {"never": true}}]}`)
	started := make(chan struct{})
	stopped := make(chan error, 1)
	startWorker(t, &Worker{
		Store: s,
		Lease: 150 * time.Millisecond,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				close(started)
				<-ctx.Done()
				stopped <- ctx.Err()
				return nil, ctx.Err()
			},
		},
	})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "cancel-me", Definition: def})
	require.NoError(t, err)
	<-started
	_, err = s.Cancel(ctx, "cancel-me", "superseded")
	require.NoError(t, err)
	select {
	case err := <-stopped:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("handler was not cancelled")
	}
	r := waitStatus(t, s, "cancel-me", workflow.StatusCancelled)
	require.Nil(t, r.State.Error)
	steps, err := s.Steps(ctx, "cancel-me")
	require.NoError(t, err)
	require.Len(t, steps, 1, "on_failure did not run")
	require.Equal(t, StepCancelled, steps[0].Status)
	require.Equal(t, 0, countJobs(t, s, "cancel-me"))
}

func TestTimersSleepAndTimeout(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [
	  {"name": "nap", "sleep": "200ms", "next": ["wait_for_human"]},
	  {"name": "wait_for_human", "agent": "approver", "timeout": "300ms"}]}`)
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				return nil, workflow.ErrDeferred
			},
		},
	})
	ctx := context.Background()
	start := time.Now()
	_, err := s.Start(ctx, NewRun{ID: "timers", Definition: def})
	require.NoError(t, err)
	r := waitStatus(t, s, "timers", workflow.StatusFailed)
	require.True(t, time.Since(start) >= 500*time.Millisecond)
	require.Equal(t, workflow.ErrorTypeTimeout, r.State.Error.Type)
	steps, err := s.Steps(ctx, "timers")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	require.Equal(t, StepCompleted, steps[0].Status)
	require.Equal(t, "sleep", steps[0].StepKind)
	// A timed-out attempt is recorded as failed with the timeout error.
	require.Equal(t, StepFailed, steps[1].Status)
	require.Equal(t, workflow.ErrorTypeTimeout, steps[1].Error.Type)
}

func TestDeferredStepCompletesThroughDeliver(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [
	  {"name": "approve", "agent": "approver", "next": ["ship"]},
	  {"name": "ship", "action": "x", "with": {"by": "${{ approve.by }}"}}]}`)
	shipped := make(chan string, 1)
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				return nil, workflow.ErrDeferred
			},
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				shipped <- job.Params["by"].(string)
				return nil, nil
			},
		},
	})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "approval", Definition: def})
	require.NoError(t, err)
	waitFor(t, "approval job handed off", func() bool { return countJobs(t, s, "approval") == 0 })

	// Days later, a person approves: the application delivers the result
	// in its own transaction.
	_, err = s.Deliver(ctx, "approval", workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "main/approve#1",
		Output: map[string]any{"by": "curtis"},
	})
	require.NoError(t, err)
	require.Equal(t, "curtis", <-shipped)
	waitStatus(t, s, "approval", workflow.StatusCompleted)
}

func TestStartIsIdempotent(t *testing.T) {
	s := newStore(t)
	a := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
	b := load(t, `{"steps": [{"name": "b", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "once", Definition: a})
	require.NoError(t, err)
	_, err = s.Start(ctx, NewRun{ID: "once", Definition: a})
	require.NoError(t, err)
	require.Equal(t, 1, countJobs(t, s, "once"))
	_, err = s.Start(ctx, NewRun{ID: "once", Definition: b})
	require.True(t, errors.Is(err, ErrRunExists))

	_, err = s.Deliver(ctx, "missing", workflow.Event{Kind: workflow.EventCancel})
	require.ErrorIs(t, err, ErrRunNotFound)
}

// TestDefinitionsLoadFromTheDatabase shows a fresh process (a new Store with
// an empty cache) advancing a run another process started.
func TestDefinitionsLoadFromTheDatabase(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "a", "action": "x", "next": ["b"]}, {"name": "b", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "handoff", Definition: def})
	require.NoError(t, err)

	other, err := New(s.pool, kinds, WithSchema(s.schema))
	require.NoError(t, err)
	_, err = other.Deliver(ctx, "handoff",
		workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/a#1"})
	require.NoError(t, err)
	r, err := other.GetRun(ctx, "handoff")
	require.NoError(t, err)
	require.Equal(t, []workflow.StepID{"main/b#1"}, r.State.InFlight())
}

// TestGracefulShutdownDoesNotCountAsAClaim stops workers mid-step more times
// than MaxClaims allows; the step still completes.
func TestGracefulShutdownDoesNotCountAsAClaim(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "long", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "deploys", Definition: def})
	require.NoError(t, err)
	for range 4 {
		started := make(chan struct{})
		wctx, stop := context.WithCancel(ctx)
		w := &Worker{
			Store:     s,
			MaxClaims: 2,
			Poll:      10 * time.Millisecond,
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
		}
		done := make(chan struct{})
		go func() {
			_ = w.Run(wctx)
			close(done)
		}()
		<-started
		stop()
		<-done
	}
	startWorker(t, &Worker{
		Store:     s,
		MaxClaims: 2,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				return "done", nil
			},
		},
	})
	r := waitStatus(t, s, "deploys", workflow.StatusCompleted, workflow.StatusFailed)
	require.Equal(t, workflow.StatusCompleted, r.Status)
}

// TestShutdownKeepsFinishedWork stops the worker just as a handler
// finishes. A result or a hand-off is recorded, not run again by the next
// worker.
func TestShutdownKeepsFinishedWork(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "build", "action": "x", "next": ["ask"]}, {"name": "ask", "agent": "human"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "shutdown", Definition: def})
	require.NoError(t, err)

	var calls atomic.Int32
	for _, want := range []int{1, 0} { // build's job, then ask's
		wctx, stop := context.WithCancel(ctx)
		w := &Worker{
			Store: s,
			Poll:  10 * time.Millisecond,
			Handlers: map[string]workflow.Handler{
				"action": func(context.Context, workflow.Job) (any, error) {
					calls.Add(1)
					stop()
					return "built", nil
				},
				"agent": func(context.Context, workflow.Job) (any, error) {
					calls.Add(1)
					stop()
					return nil, workflow.ErrDeferred
				},
			},
		}
		require.NoError(t, w.Run(wctx))
		require.Equal(t, want, countJobs(t, s, "shutdown"))
	}
	// A third worker finds nothing to claim, even after all leases expire.
	expireLeases(t, s, "shutdown")
	third := &Worker{
		Store:    s,
		Handlers: map[string]workflow.Handler{"action": nil, "agent": nil},
	}
	third.defaults()
	claimed, err := third.claim(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, claimed)
	require.Equal(t, int32(2), calls.Load())
	r, err := s.GetRun(ctx, "shutdown")
	require.NoError(t, err)
	require.Equal(t, []workflow.StepID{"main/ask#1"}, r.State.InFlight())
}

// TestWorkersWithDifferentHandlers splits step kinds across workers. Each
// claims only the kinds it can run.
func TestWorkersWithDifferentHandlers(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [
	  {"name": "think", "agent": "a", "next": ["act"]},
	  {"name": "act", "action": "x", "next": ["think_again"]},
	  {"name": "think_again", "agent": "a", "next": ["wrap"]},
	  {"name": "wrap", "action": "x"}]}`)
	var agents, actions atomic.Int32
	for i := range 2 {
		startWorker(t, &Worker{
			Store: s,
			ID:    fmt.Sprintf("agents-%d", i),
			Handlers: map[string]workflow.Handler{
				"agent": func(ctx context.Context, job workflow.Job) (any, error) {
					agents.Add(1)
					return "thought", nil
				},
			},
		})
		startWorker(t, &Worker{
			Store: s,
			ID:    fmt.Sprintf("actions-%d", i),
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					actions.Add(1)
					return "acted", nil
				},
			},
		})
	}
	ctx := context.Background()
	for i := range 10 {
		_, err := s.Start(ctx, NewRun{ID: fmt.Sprintf("split-%d", i), Definition: def})
		require.NoError(t, err)
	}
	for i := range 10 {
		id := fmt.Sprintf("split-%d", i)
		r := waitStatus(t, s, id, workflow.StatusCompleted, workflow.StatusFailed)
		require.Equal(t, workflow.StatusCompleted, r.Status, "%v", r.State.Error)
	}
	require.Equal(t, int32(20), agents.Load())
	require.Equal(t, int32(20), actions.Load())
}

// TestDeferredFinishIsRetried fails the first attempt to mark a deferred
// job done. The worker retries; the handler does not run again.
func TestDeferredFinishIsRetried(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "ask", "agent": "human"}]}`)
	var calls, failures atomic.Int32
	stop := runWorker(t, &Worker{
		Store: s,
		Lease: 300 * time.Millisecond,
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				calls.Add(1)
				return nil, workflow.ErrDeferred
			},
		},
		finishHook: func() error {
			if failures.Add(1) <= 3 {
				return errors.New("connection reset")
			}
			return nil
		},
	})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "ask", Definition: def})
	require.NoError(t, err)
	waitFor(t, "deferred job marked done", func() bool { return countJobs(t, s, "ask") == 0 })
	stop()
	require.Equal(t, 0, countJobs(t, s, "ask"))
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, int32(4), failures.Load())
}
