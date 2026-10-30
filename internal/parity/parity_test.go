package parity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/postgres"
	"github.com/deepnoodle-ai/workflow/sqlite"
)

// store has every method both stores share.
type store interface {
	Migrate(ctx context.Context) error
	Start(ctx context.Context, run durable.NewRun) (*durable.Result, error)
	Deliver(ctx context.Context, runID string, ev workflow.Event) (*durable.Result, error)
	Cancel(ctx context.Context, runID, reason string) (*durable.Result, error)
	Pause(ctx context.Context, runID, reason string) (*durable.Result, error)
	Resume(ctx context.Context, runID string) (*durable.Result, error)
	StartSQLTx(ctx context.Context, tx durable.SQLTx, run durable.NewRun) (*durable.Result, error)
	DeliverSQLTx(
		ctx context.Context, tx durable.SQLTx, runID string, ev workflow.Event,
	) (*durable.Result, error)
	CancelSQLTx(ctx context.Context, tx durable.SQLTx, runID, reason string) (*durable.Result, error)
	PauseSQLTx(ctx context.Context, tx durable.SQLTx, runID, reason string) (*durable.Result, error)
	ResumeSQLTx(ctx context.Context, tx durable.SQLTx, runID string) (*durable.Result, error)
	GetRun(ctx context.Context, runID string) (*durable.Run, error)
	Steps(ctx context.Context, runID string) ([]durable.StepRecord, error)
	Events(ctx context.Context, runID string) ([]workflow.Event, error)
	QueueStats(ctx context.Context) ([]durable.QueueStat, error)
	ListRuns(ctx context.Context, f durable.RunFilter) (durable.RunPage, error)
	DeleteEndedRuns(ctx context.Context, before time.Time) (int, error)
}

// worker has every method both workers share.
type worker interface {
	Run(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Active() int
	RunOnce(ctx context.Context) error
	Execute(ctx context.Context, ref durable.JobRef) (durable.Execution, error)
}

var (
	_ store  = (*postgres.Store)(nil)
	_ store  = (*sqlite.Store)(nil)
	_ worker = (*postgres.Worker)(nil)
	_ worker = (*sqlite.Worker)(nil)

	// One hook, one option name, both backends.
	resume = func(context.Context, durable.SQLTx, durable.Finished) error { return nil }
	_      = postgres.WithOnFinishedSQL(resume)
	_      = sqlite.WithOnFinishedSQL(resume)
)

// TestOneObserverForBothWorkers: one durable.Observer value is accepted by
// both workers.
func TestOneObserverForBothWorkers(t *testing.T) {
	metrics := durable.Observer{Settled: func(workflow.Job, durable.Settlement) {}}
	p := &postgres.Worker{Observer: metrics}
	s := &sqlite.Worker{Observer: metrics}
	if p.Observer.Settled == nil || s.Observer.Settled == nil {
		t.Fatal("the Observer was not kept")
	}
}

// TestSharedErrorsMatchAcrossBackends: each shared error is one value, so
// errors.Is holds across backends and with durable.
func TestSharedErrorsMatchAcrossBackends(t *testing.T) {
	for _, c := range []struct{ pg, sq, d error }{
		{postgres.ErrRunNotFound, sqlite.ErrRunNotFound, durable.ErrRunNotFound},
		{postgres.ErrRunExists, sqlite.ErrRunExists, durable.ErrRunExists},
		{postgres.ErrUnstorable, sqlite.ErrUnstorable, durable.ErrUnstorable},
		{
			postgres.ErrDefinitionUnavailable,
			sqlite.ErrDefinitionUnavailable,
			durable.ErrDefinitionUnavailable,
		},
		{postgres.ErrNotClaimable, sqlite.ErrNotClaimable, durable.ErrNotClaimable},
		{postgres.ErrHardStop, sqlite.ErrHardStop, durable.ErrHardStop},
	} {
		if !errors.Is(c.sq, c.pg) || !errors.Is(c.pg, c.sq) || !errors.Is(c.sq, c.d) {
			t.Errorf("%v: the backends' values differ", c.d)
		}
	}
}
