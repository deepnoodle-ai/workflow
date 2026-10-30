package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
	"github.com/jackc/pgx/v5"
)

// WithOnFinishedSQL calls fn in the database/sql transaction that moves a
// run to completed, failed or cancelled, once per run. fn gets the
// caller's own tx value when StartSQLTx, DeliverSQLTx or CancelSQLTx
// applied the run's last event, so it can assert it back to its type.
//
// A store with WithOnFinishedSQL and no [WithOnFinished] opens its own
// transactions (a worker's settle and abort, Start, Deliver and Cancel)
// through database/sql on its own pool, so fn runs on every path, with
// the *sql.Tx the store opened. The sqlite module has the same option,
// so one hook serves both backends. Such a store refuses StartTx,
// DeliverTx and CancelTx, whose pgx.Tx the hook cannot take.
//
// With both hooks, a run that finishes in a pgx.Tx calls WithOnFinished,
// and one that finishes in a database/sql transaction calls fn.
func WithOnFinishedSQL(fn func(ctx context.Context, tx SQLTx, f Finished) error) Option {
	return func(s *Store) { s.onFinishedSQL = fn }
}

// StartSQLTx is [Store.StartTx] in the caller's database/sql transaction.
func (s *Store) StartSQLTx(ctx context.Context, tx SQLTx, run NewRun) (*Result, error) {
	c, err := s.sqlConn(tx)
	if err != nil {
		return nil, err
	}
	return s.core.Start(ctx, s.txOn(c), run)
}

// DeliverSQLTx is [Store.DeliverTx] in the caller's database/sql
// transaction.
func (s *Store) DeliverSQLTx(
	ctx context.Context, tx SQLTx, runID string, ev workflow.Event,
) (*Result, error) {
	c, err := s.sqlConn(tx)
	if err != nil {
		return nil, err
	}
	return s.core.Deliver(ctx, s.txOn(c), runID, ev)
}

// CancelSQLTx is [Store.CancelTx] in the caller's database/sql transaction.
func (s *Store) CancelSQLTx(ctx context.Context, tx SQLTx, runID, reason string) (*Result, error) {
	return s.DeliverSQLTx(ctx, tx, runID,
		workflow.Event{Kind: workflow.EventCancel, Reason: reason})
}

// PauseSQLTx is [Store.PauseTx] in the caller's database/sql transaction.
func (s *Store) PauseSQLTx(ctx context.Context, tx SQLTx, runID, reason string) (*Result, error) {
	return s.DeliverSQLTx(ctx, tx, runID,
		workflow.Event{Kind: workflow.EventPause, Reason: reason})
}

// ResumeSQLTx is [Store.ResumeTx] in the caller's database/sql transaction.
func (s *Store) ResumeSQLTx(ctx context.Context, tx SQLTx, runID string) (*Result, error) {
	return s.DeliverSQLTx(ctx, tx, runID, workflow.Event{Kind: workflow.EventResume})
}

// sqlConn wraps tx, or refuses it before any statement runs: a store with
// a WithOnFinished hook could finish a run in tx, and that hook needs a
// pgx.Tx.
func (s *Store) sqlConn(tx SQLTx) (conn, error) {
	if s.onFinished != nil && s.onFinishedSQL == nil {
		return nil, errors.New("postgres: the store has WithOnFinished, whose hook needs " +
			"a pgx.Tx; add WithOnFinishedSQL to use a database/sql transaction")
	}
	return sqlConn{tx: tx}, nil
}

// allowPgxTx refuses a pgx.Tx, before any statement runs, on a store whose
// only finish hook is WithOnFinishedSQL: a run could finish in tx, and
// that hook needs a database/sql transaction.
func (s *Store) allowPgxTx() error {
	if s.sqlDB != nil {
		return errors.New("postgres: the store has only WithOnFinishedSQL, whose hook needs a " +
			"database/sql transaction; use StartSQLTx, DeliverSQLTx or CancelSQLTx, " +
			"or add WithOnFinished for a pgx.Tx")
	}
	return nil
}

// callOnFinished calls the hook for the kind of transaction tx is.
func (s *Store) callOnFinished(ctx context.Context, tx conn, f Finished) error {
	switch tx := tx.(type) {
	case sqlConn:
		return s.onFinishedSQL(ctx, tx.tx, f)
	case pgx.Tx:
		return s.onFinished(ctx, tx, f)
	}
	return fmt.Errorf("postgres: finished a run outside a transaction (%T)", tx)
}
