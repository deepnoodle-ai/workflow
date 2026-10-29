package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
	"github.com/jackc/pgx/v5"
)

// WithOnFinishedSQL is [WithOnFinished] for a run that finishes in a
// database/sql transaction: StartSQLTx, DeliverSQLTx or CancelSQLTx applied
// its last event. fn gets the caller's own tx value, so it can assert it
// back to its type. A worker finishes runs in a pgx.Tx and calls the
// WithOnFinished hook, so New requires that hook too; have both call one
// function.
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
