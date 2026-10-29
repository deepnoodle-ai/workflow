package hosttest

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// Option configures a store that a test opens with newOn or storeOn, as a
// backend's own options do.
type Option func(*Options)

// WithLimits sets the state limits for every definition the store loads.
func WithLimits(l workflow.Limits) Option { return func(o *Options) { o.Limits = l } }

// WithRunIDs sets how Start makes the ID of a run whose NewRun.ID is empty.
func WithRunIDs(fn func() string) Option { return func(o *Options) { o.NewRunID = fn } }

// WithOnFinished sets the store's finish hook.
func WithOnFinished(fn func(ctx context.Context, f Finished) error) Option {
	return func(o *Options) { o.OnFinished = fn }
}

// storeOn returns another store on base's database with its own kinds,
// options and an empty definition cache, as another process would have.
func storeOn(t *testing.T, base *Env, k workflow.Kinds, opts ...Option) *Env {
	t.Helper()
	var o Options
	for _, opt := range opts {
		opt(&o)
	}
	return base.reopen(t, k, o)
}

// newOn is storeOn in the shape of a backend's New.
func newOn(t *testing.T, base *Env, k workflow.Kinds, opts ...Option) (*Env, error) {
	t.Helper()
	return storeOn(t, base, k, opts...), nil
}

// jobs lists a run's jobs.
func (e *Env) jobs(t *testing.T, runID string) []JobRow {
	t.Helper()
	return e.h.Jobs(t, e, runID)
}

// job returns a run's job of kind ("step" or "timer") for StepID id.
func (e *Env) job(t *testing.T, runID string, id workflow.StepID, kind string) (JobRow, bool) {
	t.Helper()
	for _, j := range e.jobs(t, runID) {
		if j.StepID == id && j.Kind == kind {
			return j, true
		}
	}
	return JobRow{}, false
}

func (e *Env) count(t *testing.T, table, runID string) int {
	t.Helper()
	return e.h.Count(t, e, table, runID)
}

func (e *Env) backdate(t *testing.T, runID string, ago time.Duration) {
	t.Helper()
	e.h.Backdate(t, e, runID, ago)
}

func (e *Env) holdRunLock(t *testing.T, runID string) (release func()) {
	t.Helper()
	return e.h.HoldRunLock(t, e, runID)
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
