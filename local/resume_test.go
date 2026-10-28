package local_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/deepnoodle-ai/workflow/local"
)

// memStore is a Runner.Save that keeps the last state it saved as JSON, as
// a process would on disk. failAt makes that save (counting from 1) fail.
type memStore struct {
	mu     sync.Mutex
	data   []byte
	saves  int
	failAt int
}

var errDiskFull = errors.New("disk full")

func (m *memStore) save(_ context.Context, _ string, st *workflow.State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves++
	if m.saves == m.failAt {
		return errDiskFull
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	m.data = data
	return nil
}

// load decodes the last saved state, as a new process would.
func (m *memStore) load(t *testing.T) *workflow.State {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var st workflow.State
	require.NoError(t, json.Unmarshal(m.data, &st))
	return &st
}

// jobLog records every job a runner's handlers were called with.
type jobLog struct {
	mu   sync.Mutex
	jobs []workflow.Job
}

func (l *jobLog) add(job workflow.Job) {
	l.mu.Lock()
	l.jobs = append(l.jobs, job)
	l.mu.Unlock()
}

// claims lists each dispatched StepID with its Job.Claims, one entry per
// call, sorted: "main/e[2]#1:2".
func (l *jobLog) claims() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.jobs))
	for i, j := range l.jobs {
		out[i] = fmt.Sprintf("%s:%d", j.StepID, j.Claims)
	}
	sort.Strings(out)
	return out
}

// crashThenResume runs def with first until first calls crash, then resumes
// the saved state with a fresh Runner using second, and returns the final
// state and both runners' job logs.
func crashThenResume(t *testing.T, def *workflow.Definition,
	first func(ctx context.Context, job workflow.Job, crash func()) (any, error),
	second workflow.Handler,
	wantInFlight []workflow.StepID, wantStatus workflow.RunStatus,
) (*workflow.State, *jobLog, *jobLog) {
	t.Helper()
	store := &memStore{}
	ctx, crash := context.WithCancel(context.Background())
	defer crash()
	before, after := &jobLog{}, &jobLog{}
	r1 := &local.Runner{Save: store.save, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			before.add(job)
			return first(ctx, job, crash)
		},
	}}
	_, err := r1.Run(ctx, "run-1", def, nil)
	require.ErrorIs(t, err, context.Canceled)

	saved := store.load(t)
	require.Equal(t, wantStatus, saved.Status)
	require.Equal(t, wantInFlight, saved.InFlight())

	r2 := &local.Runner{Save: store.save, Handlers: map[string]workflow.Handler{
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			after.add(job)
			return second(ctx, job)
		},
	}}
	run, err := r2.Resume(context.Background(), "run-1", def, saved)
	require.NoError(t, err)
	st, err := run.Wait()
	require.NoError(t, err)
	return st, before, after
}

// blockUntilCrash parks a handler until the process "dies".
func blockUntilCrash(ctx context.Context) (any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A crash in the middle of an each with concurrency 2, after 2 of 5 items
// settled and 2 more were in flight: the resumed run dispatches the 2 in
// flight again with Claims 2, the last item once, and never the settled
// ones.
func TestResumeMidEach(t *testing.T) {
	def := load(t, `{"steps": [
	  {"name": "e", "action": "x", "each": "[0, 1, 2, 3, 4]", "concurrency": 2,
	   "with": {"i": "${{ item }}"}, "output": "number", "next": ["after"]},
	  {"name": "after", "action": "x", "with": {"sum": "${{ sum(e) }}"}}],
	  "outputs": {"e": "${{ e }}", "sum": "${{ after }}"}}`)
	var mu sync.Mutex
	blocked := 0
	work := func(job workflow.Job) (any, error) {
		if job.Step == "after" {
			return job.Params["sum"], nil
		}
		return job.Params["i"].(float64) * 10, nil
	}
	st, before, after := crashThenResume(t, def,
		func(ctx context.Context, job workflow.Job, crash func()) (any, error) {
			if job.Index < 2 {
				return work(job)
			}
			mu.Lock()
			blocked++
			if blocked == 2 {
				// Items 2 and 3 are in flight, so 0 and 1 settled.
				crash()
			}
			mu.Unlock()
			return blockUntilCrash(ctx)
		},
		func(ctx context.Context, job workflow.Job) (any, error) { return work(job) },
		[]workflow.StepID{"main/e[2]#1", "main/e[3]#1"}, workflow.StatusRunning)

	require.Equal(t, []string{"main/e[0]#1:1", "main/e[1]#1:1", "main/e[2]#1:1", "main/e[3]#1:1"}, before.claims())
	require.Equal(t, []string{"main/after#1:1", "main/e[2]#1:2", "main/e[3]#1:2", "main/e[4]#1:1"}, after.claims())
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.Equal(t, map[string]any{"e": []any{0.0, 10.0, 20.0, 30.0, 40.0}, "sum": 100.0}, st.Outputs)
}

// A crash in pass @2 of a bounded loop: the first pass and the steps before
// the loop are not dispatched again, the attempt in flight is dispatched
// again once with Claims 2, and the loop finishes its passes.
func TestResumeMidLoop(t *testing.T) {
	def := load(t, `{"steps": [
	  {"name": "start", "action": "x", "next": ["work"]},
	  {"name": "work", "action": "x", "max_visits": 5, "with": {"pass": "${{ coalesce(check?.pass, 0) + 1 }}"},
	   "output": {"type": "object", "required": ["pass"], "properties": {"pass": {"type": "number"}}}, "next": ["check"]},
	  {"name": "check", "action": "x", "with": {"pass": "${{ work.pass }}"},
	   "output": {"type": "object", "required": ["again", "pass"], "properties": {"again": {"type": "boolean"}, "pass": {"type": "number"}}},
	   "match": "first", "next": [{"step": "work", "when": "check.again"}, "done"]},
	  {"name": "done", "action": "x", "with": {"passes": "${{ check.pass }}"}}],
	  "outputs": {"passes": "${{ done }}"}}`)
	work := func(job workflow.Job) (any, error) {
		switch job.Step {
		case "work":
			return map[string]any{"pass": job.Params["pass"]}, nil
		case "check":
			pass := job.Params["pass"].(float64)
			return map[string]any{"again": pass < 3, "pass": pass}, nil
		case "done":
			return job.Params["passes"], nil
		}
		return nil, nil
	}
	st, before, after := crashThenResume(t, def,
		func(ctx context.Context, job workflow.Job, crash func()) (any, error) {
			if job.StepID == "main/work@2#1" {
				require.Equal(t, 2.0, job.Params["pass"])
				crash()
				return blockUntilCrash(ctx)
			}
			return work(job)
		},
		func(ctx context.Context, job workflow.Job) (any, error) {
			if job.StepID == "main/work@2#1" {
				require.Equal(t, 2.0, job.Params["pass"], "the resumed attempt has the same params")
			}
			return work(job)
		},
		[]workflow.StepID{"main/work@2#1"}, workflow.StatusRunning)

	require.Equal(t, []string{"main/check#1:1", "main/start#1:1", "main/work#1:1", "main/work@2#1:1"}, before.claims())
	require.Equal(t, []string{"main/check@2#1:1", "main/check@3#1:1", "main/done#1:1", "main/work@2#1:2", "main/work@3#1:1"}, after.claims())
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.Equal(t, map[string]any{"passes": 3.0}, st.Outputs)
}

// A crash while the run is completing, after the first of two finally
// steps settled: only the second is dispatched again, and the run
// completes with the outputs it had decided.
func TestResumeMidFinally(t *testing.T) {
	def := load(t, `{
	  "steps": [{"name": "work", "action": "x"}],
	  "finally": [
	    {"name": "release", "action": "x", "with": {"status": "${{ run.status }}"}},
	    {"name": "notify", "action": "x", "with": {"status": "${{ run.status }}"}}]}`)
	work := func(job workflow.Job) (any, error) {
		if job.Step != "work" {
			if job.Params["status"] != "completed" {
				return nil, &workflow.StepError{Type: "bad_status", Message: fmt.Sprint(job.Params["status"])}
			}
		}
		return job.Step + " done", nil
	}
	st, before, after := crashThenResume(t, def,
		func(ctx context.Context, job workflow.Job, crash func()) (any, error) {
			if job.Step == "notify" {
				crash()
				return blockUntilCrash(ctx)
			}
			return work(job)
		},
		func(ctx context.Context, job workflow.Job) (any, error) { return work(job) },
		[]workflow.StepID{"finally/notify#1"}, workflow.StatusCompleting)

	require.Equal(t, []string{"finally/notify#1:1", "finally/release#1:1", "main/work#1:1"}, before.claims())
	require.Equal(t, []string{"finally/notify#1:2"}, after.claims())
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.Equal(t, map[string]any{"work": "work done"}, st.Outputs)
	require.Len(t, st.FinallyErrors, 0)
}

// A Save that fails stops the run with its error before the commands of
// the unsaved state are carried out. Resuming the last state that was saved
// dispatches its work in flight again and completes.
func TestResumeAfterSaveFails(t *testing.T) {
	def := load(t, `{"steps": [
	  {"name": "a", "action": "x", "next": ["b"]},
	  {"name": "b", "action": "x", "next": ["c"]},
	  {"name": "c", "action": "x", "with": {"from": "${{ b }}"}}]}`)
	// Saves: 1 start (dispatches a), 2 a settled (dispatches b), 3 b
	// settled: fails, so c is never dispatched.
	store := &memStore{failAt: 3}
	before, after := &jobLog{}, &jobLog{}
	handler := func(log *jobLog) workflow.Handler {
		return func(ctx context.Context, job workflow.Job) (any, error) {
			log.add(job)
			if job.Step == "c" {
				return "c after " + job.Params["from"].(string), nil
			}
			return job.Step, nil
		}
	}
	r1 := &local.Runner{Save: store.save, Handlers: map[string]workflow.Handler{"action": handler(before)}}
	_, err := r1.Run(context.Background(), "run-1", def, nil)
	require.ErrorIs(t, err, errDiskFull)
	require.True(t, strings.Contains(err.Error(), "save state"), err.Error())
	require.Equal(t, []string{"main/a#1:1", "main/b#1:1"}, before.claims())

	saved := store.load(t)
	require.Equal(t, workflow.StatusRunning, saved.Status)
	require.Equal(t, []workflow.StepID{"main/b#1"}, saved.InFlight())

	r2 := &local.Runner{Save: store.save, Handlers: map[string]workflow.Handler{"action": handler(after)}}
	run, err := r2.Resume(context.Background(), "run-1", def, saved)
	require.NoError(t, err)
	st, err := run.Wait()
	require.NoError(t, err)
	require.Equal(t, []string{"main/b#1:2", "main/c#1:1"}, after.claims())
	require.Equal(t, workflow.StatusCompleted, st.Status)
	require.Equal(t, map[string]any{"c": "c after b"}, st.Outputs)
	require.Equal(t, workflow.StatusCompleted, store.load(t).Status, "the resumed run saved its end")
}
