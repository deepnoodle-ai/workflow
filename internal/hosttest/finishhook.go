package hosttest

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// These tests are PRD 007's UC-5: one WithOnFinishedSQL hook resumes a
// parent run when its child run finishes, on every path that finishes a
// run, on every backend.

const (
	parentDoc = `{"steps": [{"name": "child", "agent": "spawn"}],
	  "outputs": {"child": "${{ child }}"}}`
	childDoc = `{"steps": [{"name": "work", "action": "x"}],
	  "outputs": {"result": "${{ work }}"}}`
	waitingChildDoc  = `{"steps": [{"name": "wait", "agent": "human"}]}`
	approvalChildDoc = `{"steps": [{"name": "approve", "signoff": "managers"}]}`
)

var hookKinds = workflow.KindMap{"agent": nil, "action": nil, "signoff": nil}

// resumer is the application in UC-5: a host kind starts a child run and
// records its parent, and the finish hook delivers the child's outcome to
// the parent's step in the same transaction.
type resumer struct {
	mu      sync.Mutex
	env     *Env
	parents map[string]parentStep // by child run ID
	txs     []durable.SQLTx       // the tx of each hook call
	fail    int                   // fail this many calls first
	badRef  bool                  // deliver to an attempt the parent never had
}

type parentStep struct {
	runID  string
	stepID workflow.StepID
}

func newResumer() *resumer { return &resumer{parents: map[string]parentStep{}} }

func (r *resumer) hook(ctx context.Context, tx durable.SQLTx, f durable.Finished) error {
	r.mu.Lock()
	r.txs = append(r.txs, tx)
	failing := r.fail > 0
	if failing {
		r.fail--
	}
	parent, ok := r.parents[f.RunID]
	env := r.env
	r.mu.Unlock()
	if failing {
		return errors.New("audit log insert failed")
	}
	if !ok {
		return nil
	}
	if r.badRef {
		parent.stepID = workflow.StepID(parent.stepID.Unit() + "#9")
	}
	ev := workflow.Event{Kind: workflow.EventStepCompleted, StepID: parent.stepID, Output: f.Outputs}
	if f.Status != workflow.StatusCompleted {
		ev = workflow.Event{Kind: workflow.EventStepFailed, StepID: parent.stepID,
			Error: &workflow.StepError{Type: "child_" + string(f.Status)}}
	}
	htx, err := env.h.TxOn(env, tx)
	if err != nil {
		return err
	}
	_, err = env.Store.Deliver(ctx, htx, parent.runID, ev)
	return err
}

func (r *resumer) calls() []durable.SQLTx {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]durable.SQLTx(nil), r.txs...)
}

// spawn is the parent's host kind: it starts the child run and hands off.
func (r *resumer) spawn(t *testing.T, childDef *workflow.Definition) workflow.Handler {
	return func(ctx context.Context, job workflow.Job) (any, error) {
		childID := "child-of-" + job.RunID
		r.mu.Lock()
		r.parents[childID] = parentStep{runID: job.RunID, stepID: job.StepID}
		env := r.env
		r.mu.Unlock()
		if _, err := env.Start(ctx, NewRun{ID: childID, Definition: childDef}); err != nil {
			return nil, err
		}
		return nil, workflow.ErrDeferred
	}
}

// openHooked opens a store whose only finish hook is r's.
func openHooked(t *testing.T, h Harness, r *resumer) *Env {
	t.Helper()
	e := openWith(t, h, hookKinds, Options{OnFinishedSQL: r.hook})
	r.mu.Lock()
	r.env = e
	r.mu.Unlock()
	return e
}

func done(context.Context, workflow.Job) (any, error) { return "done", nil }

// TestParentResumesWhenAWorkerCompletesTheChild: a worker completes the
// child, and the hook, in the worker's own transaction (a *sql.Tx the store
// opened), completes the parent's step. An error from the hook rolls the
// child's finish back, and the worker's retry applies it.
func testParentResumesWhenAWorkerCompletesTheChild(t *testing.T, h Harness) {
	for _, failFirst := range []bool{false, true} {
		name := "first try"
		if failFirst {
			name = "after a hook error"
		}
		t.Run(name, func(t *testing.T) {
			r := newResumer()
			if failFirst {
				r.fail = 1
			}
			s := openHooked(t, h, r)
			startWorker(t, &Worker{
				Store: s,
				Handlers: map[string]workflow.Handler{
					"agent":  r.spawn(t, loadKinds(t, childDoc, hookKinds)),
					"action": done,
				},
			})
			_, err := s.Start(context.Background(), NewRun{ID: "parent", Definition: loadKinds(t, parentDoc, hookKinds)})
			require.NoError(t, err)

			p := waitStatus(t, s, "parent", workflow.StatusCompleted, workflow.StatusFailed)
			require.Equal(t, workflow.StatusCompleted, p.Status, "%v", p.State.Error)
			require.Equal(t, map[string]any{"child": map[string]any{"result": "done"}}, p.State.Outputs)
			c, err := s.GetRun(context.Background(), "child-of-parent")
			require.NoError(t, err)
			require.Equal(t, workflow.StatusCompleted, c.Status)

			txs := r.calls()
			want := 2 // the child, then the parent
			if failFirst {
				want = 3 // the child's first finish rolled back
			}
			require.Len(t, txs, want)
			for _, tx := range txs {
				_, ok := tx.(*sql.Tx)
				require.True(t, ok, "the hook got %T, not the store's *sql.Tx", tx)
			}
		})
	}
}

// TestARejectedHookDeliveryStillFinishesTheChild: the hook's delivery to
// the parent names an attempt the parent never had (an application bug),
// so the parent refuses it for good. The worker records the child's finish
// once, not again on each claim, and reports the outcome Apply recorded.
// The parent is left as it was: only the hook knows it is the parent.
func testARejectedHookDeliveryStillFinishesTheChild(t *testing.T, h Harness) {
	for _, c := range []struct {
		name, childDoc string
		output         any
		status         workflow.RunStatus
		settled        string
	}{
		{"completed", childDoc, "done", workflow.StatusCompleted, "settled:completed work"},
		{"output against its schema", `{"steps": [{"name": "work", "action": "x", "output": "string"}]}`,
			42, workflow.StatusFailed, "settled:failed work"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newResumer()
			r.badRef = true
			s := openHooked(t, h, r)
			var (
				calls atomic.Int32
				rec   eventLog
			)
			startWorker(t, &Worker{
				Store:    s,
				Observer: rec.observer(),
				Handlers: map[string]workflow.Handler{
					"agent": r.spawn(t, loadKinds(t, c.childDoc, hookKinds)),
					"action": func(context.Context, workflow.Job) (any, error) {
						calls.Add(1)
						return c.output, nil
					},
				},
			})
			_, err := s.Start(context.Background(), NewRun{ID: "parent", Definition: loadKinds(t, parentDoc, hookKinds)})
			require.NoError(t, err)

			waitFor(t, "the child's settle", func() bool { return rec.count(c.settled) == 1 })
			require.Equal(t, c.status, runStatus(t, s, "child-of-parent"))
			require.Equal(t, int32(1), calls.Load())
			require.Len(t, r.calls(), 1)
			if c.status == workflow.StatusFailed {
				var failed []*workflow.StepError
				for _, st := range rec.settledCopy() {
					if st.Outcome == OutcomeFailed {
						failed = append(failed, st.Error)
					}
				}
				require.Len(t, failed, 1)
				require.Equal(t, workflow.ErrorTypeInvalidOutput, failed[0].Type)
			}
			require.Equal(t, workflow.StatusRunning, runStatus(t, s, "parent"))
		})
	}
}

// TestParentResumesWhenTheChildIsCancelled: Cancel finishes the child, and
// the hook fails the parent's step with type child_cancelled.
func testParentResumesWhenTheChildIsCancelled(t *testing.T, h Harness) {
	r := newResumer()
	s := openHooked(t, h, r)
	child := loadKinds(t, waitingChildDoc, hookKinds)
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.Step == "child" {
					return r.spawn(t, child)(ctx, job)
				}
				return nil, workflow.ErrDeferred // the child waits for a person
			},
		},
	})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "parent", Definition: loadKinds(t, parentDoc, hookKinds)})
	require.NoError(t, err)
	waitFor(t, "the child waits", func() bool {
		steps, err := s.Steps(ctx, "child-of-parent")
		return err == nil && len(steps) == 1 && s.count(t, "jobs", "child-of-parent") == 0
	})

	_, err = s.Cancel(ctx, "child-of-parent", "superseded")
	require.NoError(t, err)
	p := waitStatus(t, s, "parent", workflow.StatusFailed)
	require.Equal(t, "child_cancelled", p.State.Error.Type)
}

// TestParentResumesWhenAWorkerAbortsTheChild: a worker whose kinds lack the
// child's step kind fails the child as definition_unavailable, and its
// store's hook fails the parent's step with type child_failed.
func testParentResumesWhenAWorkerAbortsTheChild(t *testing.T, h Harness) {
	r := newResumer()
	s := openHooked(t, h, r)
	cold := s.reopen(t, workflow.KindMap{"agent": nil, "action": nil}, Options{OnFinishedSQL: r.hook})
	startWorker(t, &Worker{
		Store:    s,
		Handlers: map[string]workflow.Handler{"agent": r.spawn(t, loadKinds(t, approvalChildDoc, hookKinds))},
	})
	startWorker(t, &Worker{
		Store:     cold,
		Lease:     100 * time.Millisecond,
		MaxClaims: 2,
		Handlers:  map[string]workflow.Handler{"signoff": done},
	})
	_, err := s.Start(context.Background(), NewRun{ID: "parent", Definition: loadKinds(t, parentDoc, hookKinds)})
	require.NoError(t, err)

	waitFor(t, "the child starts", func() bool {
		_, err := s.GetRun(context.Background(), "child-of-parent")
		return err == nil
	})
	c := waitStatus(t, s, "child-of-parent", workflow.StatusFailed)
	require.Equal(t, ErrorTypeDefinitionUnavailable, c.State.Error.Type)
	p := waitStatus(t, s, "parent", workflow.StatusFailed)
	require.Equal(t, "child_failed", p.State.Error.Type)
}

// TestParentResumesInTheCallersTransaction: a child that finishes in the
// application's transaction (DeliverSQLTx) gets the caller's own tx value
// in the hook, and the parent's resume commits with it.
func testParentResumesInTheCallersTransaction(t *testing.T, h Harness) {
	r := newResumer()
	s := openHooked(t, h, r)
	child := loadKinds(t, waitingChildDoc, hookKinds)
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.Step == "child" {
					return r.spawn(t, child)(ctx, job)
				}
				return nil, workflow.ErrDeferred
			},
		},
	})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "parent", Definition: loadKinds(t, parentDoc, hookKinds)})
	require.NoError(t, err)
	waitFor(t, "the child waits", func() bool {
		steps, err := s.Steps(ctx, "child-of-parent")
		return err == nil && len(steps) == 1 && s.count(t, "jobs", "child-of-parent") == 0
	})

	tx, htx, commit, _ := h.CallerTx(t, s)
	_, err = s.Store.Deliver(ctx, htx, "child-of-parent",
		workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/wait#1", Output: "approved"})
	require.NoError(t, err)
	require.NoError(t, commit())

	txs := r.calls()
	require.Len(t, txs, 2) // the child, then the parent, both in tx
	for _, got := range txs {
		require.True(t, got == tx, "the hook got %T, not the caller's tx", got)
	}
	p := waitStatus(t, s, "parent", workflow.StatusCompleted)
	require.Equal(t, map[string]any{"child": map[string]any{"wait": "approved"}}, p.State.Outputs)
}
