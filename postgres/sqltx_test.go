package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// entTx has the method set of an Ent *Tx generated with the sql/execquery
// feature. It converts every argument as database/sql does for a driver
// without a converter of its own (lib/pq, for one), so the store must pass
// plain values.
type entTx struct {
	t  *testing.T
	tx *sql.Tx
}

func (e *entTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return e.tx.ExecContext(ctx, query, e.plain(args)...)
}

func (e *entTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return e.tx.QueryContext(ctx, query, e.plain(args)...)
}

func (e *entTx) Commit() error   { return e.tx.Commit() }
func (e *entTx) Rollback() error { return e.tx.Rollback() }

func (e *entTx) plain(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		v, err := driver.DefaultParameterConverter.ConvertValue(a)
		require.NoError(e.t, err, "argument $%d (%T) is not a plain value", i+1, a)
		out[i] = v
	}
	return out
}

// eachSQLTx runs test once with a *sql.Tx from pgx's stdlib driver and
// once with an entTx. begin opens a transaction on the store's pool; one
// left open is rolled back when the test ends.
func eachSQLTx(t *testing.T, test func(t *testing.T, s *Store, begin func() SQLTx)) {
	for _, shape := range []string{"sql.Tx", "ent.Tx"} {
		t.Run(shape, func(t *testing.T) {
			s := newStore(t)
			db := stdlib.OpenDBFromPool(s.pool)
			t.Cleanup(func() { _ = db.Close() })
			test(t, s, func() SQLTx {
				tx, err := db.BeginTx(context.Background(), nil)
				require.NoError(t, err)
				t.Cleanup(func() { _ = tx.Rollback() })
				if shape == "ent.Tx" {
					return &entTx{t: t, tx: tx}
				}
				return tx
			})
		})
	}
}

// createOrders makes the application's table.
func createOrders(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(), s.sql(`CREATE TABLE {{schema}}.orders (
	    id   text PRIMARY KEY,
	    done boolean NOT NULL DEFAULT false)`))
	require.NoError(t, err)
}

// orders lists the application's orders as "id" or "id done".
func orders(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), s.sql(`
		SELECT id || CASE WHEN done THEN ' done' ELSE '' END FROM {{schema}}.orders ORDER BY id`))
	require.NoError(t, err)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return ids
}

func appExec(t *testing.T, s *Store, tx SQLTx, query string, args ...any) {
	t.Helper()
	_, err := tx.ExecContext(context.Background(), s.sql(query), args...)
	require.NoError(t, err)
}

func jobStepIDs(t *testing.T, s *Store, runID string) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(),
		s.sql(`SELECT step_id FROM {{schema}}.jobs WHERE run_id = $1 ORDER BY step_id`), runID)
	require.NoError(t, err)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return ids
}

const approveThenShip = `{"steps": [
  {"name": "approve", "agent": "human", "next": ["ship"]},
  {"name": "ship", "action": "x", "with": {"by": "${{ approve.by }}"}}]}`

// TestStartSQLTxCommitsWithTheApplication writes an order and starts its
// run in one database/sql transaction. A rollback leaves neither, and no
// notification; a commit keeps both and notifies workers after it.
func TestStartSQLTxCommitsWithTheApplication(t *testing.T) {
	eachSQLTx(t, func(t *testing.T, s *Store, begin func() SQLTx) {
		ctx := context.Background()
		createOrders(t, s)
		def := load(t, approveThenShip)
		jobs := subscribe(t, s, s.NotifyChannel())
		run := NewRun{ID: "o-1", Definition: def, Metadata: map[string]any{"sku": "a-1"}}

		// Rolled back.
		tx := begin()
		appExec(t, s, tx, `INSERT INTO {{schema}}.orders (id) VALUES ('o-1')`)
		res, err := s.StartSQLTx(ctx, tx, run)
		require.NoError(t, err)
		require.True(t, res.Applied)
		require.NoError(t, tx.Rollback())
		require.Len(t, orders(t, s), 0)
		_, err = s.GetRun(ctx, "o-1")
		require.ErrorIs(t, err, ErrRunNotFound)
		for _, table := range []string{"jobs", "steps", "events"} {
			require.Equal(t, 0, countRows(t, s, table, "o-1"), table)
		}
		requireQuiet(t, jobs, "start rolled back")

		// Committed.
		tx = begin()
		appExec(t, s, tx, `INSERT INTO {{schema}}.orders (id) VALUES ('o-1')`)
		res, err = s.StartSQLTx(ctx, tx, run)
		require.NoError(t, err)
		require.True(t, res.Applied)
		require.Equal(t, "o-1", res.RunID)
		requireQuiet(t, jobs, "start not yet committed")
		require.NoError(t, tx.Commit())
		require.Equal(t, "o-1", requireNotified(t, jobs, "start committed").Payload)
		require.Equal(t, []string{"o-1"}, orders(t, s))
		r, err := s.GetRun(ctx, "o-1")
		require.NoError(t, err)
		require.Equal(t, map[string]any{"sku": "a-1"}, r.Metadata)
		require.Equal(t, []string{"main/approve#1"}, jobStepIDs(t, s, "o-1"))
		require.Equal(t, 1, countRows(t, s, "steps", "o-1"))
		require.Equal(t, 1, countRows(t, s, "events", "o-1"))

		// Started again: the same definition changes nothing; another one
		// is refused.
		tx = begin()
		res, err = s.StartSQLTx(ctx, tx, run)
		require.NoError(t, err)
		require.False(t, res.Applied)
		require.Len(t, res.Commands, 0)
		other := load(t, `{"steps": [{"name": "approve", "agent": "human"}]}`)
		_, err = s.StartSQLTx(ctx, tx, NewRun{ID: "o-1", Definition: other})
		require.ErrorIs(t, err, ErrRunExists)
	})
}

// TestDeliverSQLTxCommitsWithTheApplication marks an approval done and
// delivers the approval step's result in one database/sql transaction.
func TestDeliverSQLTxCommitsWithTheApplication(t *testing.T) {
	eachSQLTx(t, func(t *testing.T, s *Store, begin func() SQLTx) {
		ctx := context.Background()
		createOrders(t, s)
		_, err := s.Start(ctx, NewRun{ID: "po-7", Definition: load(t, approveThenShip)})
		require.NoError(t, err)
		_, err = s.pool.Exec(ctx, s.sql(`INSERT INTO {{schema}}.orders (id) VALUES ('po-7')`))
		require.NoError(t, err)
		jobs := subscribe(t, s, s.NotifyChannel())
		approved := workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/approve#1",
			Output: map[string]any{"by": "curtis"},
		}
		markDone := `UPDATE {{schema}}.orders SET done = true WHERE id = 'po-7'`

		// Rolled back.
		tx := begin()
		appExec(t, s, tx, markDone)
		res, err := s.DeliverSQLTx(ctx, tx, "po-7", approved)
		require.NoError(t, err)
		require.True(t, res.Applied)
		require.NoError(t, tx.Rollback())
		requireQuiet(t, jobs, "approval rolled back")
		require.Equal(t, []string{"po-7"}, orders(t, s))
		steps, err := s.Steps(ctx, "po-7")
		require.NoError(t, err)
		require.Len(t, steps, 1)
		require.Equal(t, StepDispatched, steps[0].Status)
		require.Equal(t, []string{"main/approve#1"}, jobStepIDs(t, s, "po-7"))
		require.Equal(t, 1, countRows(t, s, "events", "po-7"))

		// Committed.
		tx = begin()
		appExec(t, s, tx, markDone)
		res, err = s.DeliverSQLTx(ctx, tx, "po-7", approved)
		require.NoError(t, err)
		require.True(t, res.Applied)
		requireQuiet(t, jobs, "approval not yet committed")
		require.NoError(t, tx.Commit())
		require.Equal(t, "po-7", requireNotified(t, jobs, "approval committed").Payload)
		require.Equal(t, []string{"po-7 done"}, orders(t, s))
		steps, err = s.Steps(ctx, "po-7")
		require.NoError(t, err)
		require.Len(t, steps, 2)
		require.Equal(t, StepCompleted, steps[0].Status)
		require.Equal(t, map[string]any{"by": "curtis"}, steps[1].Params)
		require.Equal(t, []string{"main/ship#1"}, jobStepIDs(t, s, "po-7"))

		// A duplicate changes nothing; an unknown run is not found.
		tx = begin()
		res, err = s.DeliverSQLTx(ctx, tx, "po-7", approved)
		require.NoError(t, err)
		require.False(t, res.Applied)
		_, err = s.DeliverSQLTx(ctx, tx, "po-8", approved)
		require.ErrorIs(t, err, ErrRunNotFound)
	})
}

// TestCancelSQLTxCommitsWithTheApplication deletes an order and cancels its
// run in one database/sql transaction.
func TestCancelSQLTxCommitsWithTheApplication(t *testing.T) {
	eachSQLTx(t, func(t *testing.T, s *Store, begin func() SQLTx) {
		ctx := context.Background()
		createOrders(t, s)
		_, err := s.Start(ctx, NewRun{ID: "o-2", Definition: load(t, approveThenShip)})
		require.NoError(t, err)
		_, err = s.pool.Exec(ctx, s.sql(`INSERT INTO {{schema}}.orders (id) VALUES ('o-2')`))
		require.NoError(t, err)
		runs := subscribe(t, s, s.RunsChannel())
		deleteOrder := `DELETE FROM {{schema}}.orders WHERE id = 'o-2'`

		// Rolled back.
		tx := begin()
		appExec(t, s, tx, deleteOrder)
		res, err := s.CancelSQLTx(ctx, tx, "o-2", "order deleted")
		require.NoError(t, err)
		require.True(t, res.Applied)
		require.Equal(t, workflow.StatusCancelled, res.State.Status)
		require.NoError(t, tx.Rollback())
		requireQuiet(t, runs, "cancel rolled back")
		require.Equal(t, []string{"o-2"}, orders(t, s))
		r, err := s.GetRun(ctx, "o-2")
		require.NoError(t, err)
		require.Equal(t, workflow.StatusRunning, r.Status)
		require.Equal(t, []string{"main/approve#1"}, jobStepIDs(t, s, "o-2"))

		// Committed.
		tx = begin()
		appExec(t, s, tx, deleteOrder)
		res, err = s.CancelSQLTx(ctx, tx, "o-2", "order deleted")
		require.NoError(t, err)
		require.True(t, res.Applied)
		requireQuiet(t, runs, "cancel not yet committed")
		require.NoError(t, tx.Commit())
		require.Equal(t, `{"run_id":"o-2","status":"cancelled"}`,
			requireNotified(t, runs, "cancel committed").Payload)
		require.Len(t, orders(t, s), 0)
		r, err = s.GetRun(ctx, "o-2")
		require.NoError(t, err)
		require.Equal(t, workflow.StatusCancelled, r.Status)
		require.Equal(t, "order deleted", r.State.Reason)
		require.Len(t, jobStepIDs(t, s, "o-2"), 0)

		// A second cancel changes nothing.
		tx = begin()
		res, err = s.CancelSQLTx(ctx, tx, "o-2", "again")
		require.NoError(t, err)
		require.False(t, res.Applied)
	})
}

// TestOnFinishedSQLRunsInTheCallersTransaction: a run that finishes in a
// database/sql transaction calls WithOnFinishedSQL there, with the caller's
// own tx; one that finishes in a pgx transaction calls WithOnFinished.
func TestOnFinishedSQLRunsInTheCallersTransaction(t *testing.T) {
	eachSQLTx(t, func(t *testing.T, base *Store, begin func() SQLTx) {
		ctx := context.Background()
		_, err := base.pool.Exec(ctx, base.sql(`CREATE TABLE {{schema}}.outcomes (
		    run_id text PRIMARY KEY,
		    status text NOT NULL,
		    hook   text NOT NULL)`))
		require.NoError(t, err)
		record := func(exec func(q string, args ...any) error, f Finished, hook string) error {
			return exec(base.sql(`INSERT INTO {{schema}}.outcomes VALUES ($1, $2, $3)`),
				f.RunID, string(f.Status), hook)
		}
		var (
			mu     sync.Mutex
			hookTx SQLTx
		)
		s, err := New(base.pool, kinds, WithSchema(base.schema),
			WithOnFinished(func(ctx context.Context, tx pgx.Tx, f Finished) error {
				return record(func(q string, args ...any) error {
					_, err := tx.Exec(ctx, q, args...)
					return err
				}, f, "pgx")
			}),
			WithOnFinishedSQL(func(ctx context.Context, tx SQLTx, f Finished) error {
				mu.Lock()
				hookTx = tx
				mu.Unlock()
				return record(func(q string, args ...any) error {
					_, err := tx.ExecContext(ctx, q, args...)
					return err
				}, f, "sql")
			}))
		require.NoError(t, err)
		outcomes := func() []string {
			rows, err := s.pool.Query(ctx, s.sql(`
				SELECT run_id || ' ' || status || ' ' || hook FROM {{schema}}.outcomes ORDER BY run_id`))
			require.NoError(t, err)
			out, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err)
			return out
		}
		def := load(t, `{"steps": [{"name": "approve", "agent": "human"}]}`)
		for _, id := range []string{"r-1", "r-2"} {
			_, err := s.Start(ctx, NewRun{ID: id, Definition: def})
			require.NoError(t, err)
		}

		// Rolled back: the hook ran, with the caller's tx, and its write
		// went with the rollback.
		tx := begin()
		_, err = s.CancelSQLTx(ctx, tx, "r-1", "withdrawn")
		require.NoError(t, err)
		mu.Lock()
		require.True(t, hookTx == tx, "the hook got %T, not the caller's tx", hookTx)
		mu.Unlock()
		require.NoError(t, tx.Rollback())
		require.Len(t, outcomes(), 0)

		// Committed.
		tx = begin()
		_, err = s.CancelSQLTx(ctx, tx, "r-1", "withdrawn")
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		require.Equal(t, []string{"r-1 cancelled sql"}, outcomes())

		// A run that finishes in a pgx transaction calls the pgx hook.
		_, err = s.Cancel(ctx, "r-2", "withdrawn")
		require.NoError(t, err)
		require.Equal(t, []string{"r-1 cancelled sql", "r-2 cancelled pgx"}, outcomes())
	})
}

// refusedTx fails the test if the store runs a statement on it.
type refusedTx struct{ t *testing.T }

func (r refusedTx) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	r.t.Fatal("the store ran a statement")
	return nil, nil
}

func (r refusedTx) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	r.t.Fatal("the store ran a statement")
	return nil, nil
}

func (refusedTx) Commit() error   { return errors.New("not a transaction") }
func (refusedTx) Rollback() error { return errors.New("not a transaction") }

// TestOnFinishedNeedsTheRightTransaction: a store with only the pgx hook
// refuses database/sql transactions before it runs a statement, since it
// could finish a run there. A store with only the database/sql hook is
// accepted (#58) and refuses a pgx.Tx the same way.
func TestOnFinishedNeedsTheRightTransaction(t *testing.T) {
	base := newStore(t)
	ctx := context.Background()
	s, err := New(base.pool, kinds, WithSchema(base.schema),
		WithOnFinished(func(context.Context, pgx.Tx, Finished) error { return nil }))
	require.NoError(t, err)
	tx := refusedTx{t: t}
	def := load(t, `{"steps": [{"name": "approve", "agent": "human"}]}`)
	_, startErr := s.StartSQLTx(ctx, tx, NewRun{ID: "r", Definition: def})
	_, deliverErr := s.DeliverSQLTx(ctx, tx, "r", workflow.Event{Kind: workflow.EventCancel})
	_, cancelErr := s.CancelSQLTx(ctx, tx, "r", "withdrawn")
	for _, err := range []error{startErr, deliverErr, cancelErr} {
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "add WithOnFinishedSQL"), err.Error())
	}

	sqlOnly, err := New(base.pool, kinds, WithSchema(base.schema),
		WithOnFinishedSQL(func(context.Context, SQLTx, Finished) error { return nil }))
	require.NoError(t, err)
	var none pgx.Tx // nil: any statement on it would panic
	_, startErr = sqlOnly.StartTx(ctx, none, NewRun{ID: "r", Definition: def})
	_, deliverErr = sqlOnly.DeliverTx(ctx, none, "r", workflow.Event{Kind: workflow.EventCancel})
	_, cancelErr = sqlOnly.CancelTx(ctx, none, "r", "withdrawn")
	for _, err := range []error{startErr, deliverErr, cancelErr} {
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "add WithOnFinished"), err.Error())
	}
}

// TestDeliverSQLTxLoadsTheDefinition: a process that did not start the run
// (a fresh store, with nothing cached) reads the run's definition in the
// caller's database/sql transaction. A definition that is not stored is
// unavailable.
func TestDeliverSQLTxLoadsTheDefinition(t *testing.T) {
	eachSQLTx(t, func(t *testing.T, s *Store, begin func() SQLTx) {
		ctx := context.Background()
		_, err := s.Start(ctx, NewRun{ID: "po-7", Definition: load(t, approveThenShip)})
		require.NoError(t, err)
		fresh := func() *Store {
			f, err := New(s.pool, kinds, WithSchema(s.schema))
			require.NoError(t, err)
			return f
		}
		approved := workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/approve#1",
			Output: map[string]any{"by": "curtis"},
		}

		// Missing: the transaction drops the definition (and the foreign
		// key that guards it), then rolls both back.
		tx := begin()
		appExec(t, s, tx, `ALTER TABLE {{schema}}.runs DROP CONSTRAINT runs_definition_hash_fkey`)
		appExec(t, s, tx, `DELETE FROM {{schema}}.definitions`)
		_, err = fresh().DeliverSQLTx(ctx, tx, "po-7", approved)
		require.ErrorIs(t, err, ErrDefinitionUnavailable)
		require.Contains(t, err.Error(), "is not stored")
		require.NoError(t, tx.Rollback())

		// Found.
		tx = begin()
		res, err := fresh().DeliverSQLTx(ctx, tx, "po-7", approved)
		require.NoError(t, err)
		require.True(t, res.Applied)
		require.Equal(t, []workflow.StepID{"main/ship#1"}, res.State.InFlight())
		require.NoError(t, tx.Commit())
		require.Equal(t, []string{"main/ship#1"}, jobStepIDs(t, s, "po-7"))
	})
}
