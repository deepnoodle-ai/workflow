package hosttest

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

func loadKinds(t *testing.T, doc string, k workflow.Kinds) *workflow.Definition {
	t.Helper()
	def, err := workflow.Load([]byte(doc), k)
	require.NoError(t, err)
	return def
}

var (
	deployKinds = workflow.KindMap{"action": nil, "signoff": nil}
	// oldKinds is the configuration of a worker the deploy has not reached:
	// it does not know the signoff kind.
	oldKinds = workflow.KindMap{"action": nil}
)

const deployDoc = `{"steps": [
  {"name": "build", "action": "ci.build", "next": ["approve"]},
  {"name": "approve", "signoff": "release-managers", "next": ["ship"]},
  {"name": "ship", "action": "k8s.rollout"}],
 "on_failure": [{"name": "page", "action": "pager.page"}],
 "finally": [{"name": "audit", "action": "audit.log"}]}`

// jobClaims reads a run's job for a StepID: its claims, and whether it is
// leased.
func jobClaims(
	t *testing.T, s *Env, runID string, id workflow.StepID,
) (claims int, leased bool, found bool) {
	t.Helper()
	j, ok := s.job(t, runID, id, "step")
	return j.Claims, j.LeaseOwner != "", ok
}

// TestRollingDeployRecoversWhenAWorkerAppears extends
// TestUnloadableDefinitionFailsTheRun: a run is started by a process that
// knows the signoff kind, and a worker the deploy has not reached claims
// its first job. That worker cannot load the definition, so it runs
// nothing and postpones the job. A correctly configured worker then
// appears, within MaxClaims, and the run completes; the job's claim count
// carries over.
func testRollingDeployRecoversWhenAWorkerAppears(t *testing.T, h Harness) {
	base := open(t, h)
	s := storeOn(t, base, deployKinds)
	ctx := context.Background()

	var oldCalls atomic.Int32
	oldCtx, stopOld := context.WithCancel(ctx)
	oldDone := make(chan struct{})
	// The old worker postpones the job by its lease, so a long lease keeps
	// it from claiming the job again while the test looks. The test then
	// backdates the job instead of waiting the postponement out.
	old := &Worker{
		Store:     storeOn(t, base, oldKinds),
		ID:        "old",
		Lease:     time.Minute,
		MaxClaims: 5,
		Poll:      20 * time.Millisecond,
		Handlers: map[string]workflow.Handler{
			"action": func(context.Context, workflow.Job) (any, error) {
				oldCalls.Add(1)
				return nil, nil
			},
		},
	}
	go func() {
		defer close(oldDone)
		_ = old.Run(oldCtx)
	}()
	t.Cleanup(func() {
		stopOld()
		<-oldDone
	})

	_, err := s.Start(ctx, NewRun{ID: "deploy", Definition: loadKinds(t, deployDoc, deployKinds)})
	require.NoError(t, err)
	waitFor(t, "the old worker to postpone the build job", func() bool {
		claims, leased, found := jobClaims(t, s, "deploy", "main/build#1")
		return found && claims == 1 && !leased
	})
	// The deploy finishes: the old instance goes away, a new one starts
	// with an empty cache.
	stopOld()
	await(t, "the old worker stops", oldDone)
	s.backdate(t, "deploy", 0) // the postponement passes

	var buildClaims atomic.Int32
	handler := func(ctx context.Context, job workflow.Job) (any, error) {
		if job.Step == "build" {
			buildClaims.Store(int32(job.Claims))
		}
		return job.Step, nil
	}
	startWorker(t, &Worker{
		Store:     storeOn(t, base, deployKinds),
		ID:        "new",
		Lease:     time.Minute, // each step settles within it
		MaxClaims: 5,
		Handlers:  map[string]workflow.Handler{"action": handler, "signoff": handler},
	})

	r := waitStatus(t, s, "deploy", workflow.StatusCompleted, workflow.StatusFailed)
	require.Equal(t, workflow.StatusCompleted, r.Status, "%v", r.State.Error)
	require.Equal(t, int32(0), oldCalls.Load(), "the old worker ran no handler")
	require.Equal(t, int32(2), buildClaims.Load(), "the postponed claim counts toward MaxClaims")
	steps, err := s.Steps(ctx, "deploy")
	require.NoError(t, err)
	var ids []string
	for _, st := range steps {
		require.Equal(t, StepCompleted, st.Status, st.StepID)
		ids = append(ids, string(st.StepID))
	}
	want := []string{"main/build#1", "main/approve#1", "main/ship#1", "finally/audit#1"}
	require.Equal(t, want, ids)
	events, err := s.Events(ctx, "deploy")
	require.NoError(t, err)
	for _, ev := range events {
		require.NotEqual(t, EventAborted, ev.Kind)
	}
}

// TestRollingDeployThatNeverLandsAbortsTheRun extends
// TestUnloadableDefinitionFailsTheRun: when no correctly configured worker
// appears within MaxClaims, the run fails with definition_unavailable
// without running its on_failure or finally steps (they need the
// definition), and the attempt that was dispatched is recorded cancelled.
func testRollingDeployThatNeverLandsAbortsTheRun(t *testing.T, h Harness) {
	base := open(t, h)
	s := storeOn(t, base, deployKinds)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "stuck", Definition: loadKinds(t, deployDoc, deployKinds)})
	require.NoError(t, err)

	var oldCalls atomic.Int32
	startWorker(t, &Worker{
		Store:     storeOn(t, base, oldKinds),
		Lease:     50 * time.Millisecond,
		MaxClaims: 3,
		Handlers: map[string]workflow.Handler{
			"action": func(context.Context, workflow.Job) (any, error) {
				oldCalls.Add(1)
				return nil, nil
			},
		},
	})

	r := waitStatus(t, s, "stuck", workflow.StatusFailed, workflow.StatusCompleted)
	require.Equal(t, workflow.StatusFailed, r.Status)
	require.Equal(t, ErrorTypeDefinitionUnavailable, r.State.Error.Type)
	require.Nil(t, r.State.Error.OnFailure)
	require.Len(t, r.State.FinallyErrors, 0)
	require.Equal(t, int32(0), oldCalls.Load())
	require.Equal(t, 0, countJobs(t, s, "stuck"))

	events, err := s.Events(ctx, "stuck")
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, workflow.EventStart, events[0].Kind)
	require.Equal(t, EventAborted, events[1].Kind)

	steps, err := s.Steps(ctx, "stuck")
	require.NoError(t, err)
	require.Len(t, steps, 1, "no on_failure or finally step was dispatched")
	require.Equal(t, workflow.StepID("main/build#1"), steps[0].StepID)
	require.Equal(t, StepCancelled, steps[0].Status)
	for _, st := range steps {
		require.False(t, strings.HasPrefix(string(st.StepID), "failure/") ||
			strings.HasPrefix(string(st.StepID), "finally/"), st.StepID)
	}

	// The configured store reads the failed run and changes nothing.
	res, err := s.Cancel(ctx, "stuck", "too late")
	require.NoError(t, err)
	require.False(t, res.Applied)
}
