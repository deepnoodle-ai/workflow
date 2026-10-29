package postgres_test

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// An application writes an order and starts its run in one database/sql
// transaction: both commit, or neither does.
func ExampleStore_StartSQLTx() {
	ctx := context.Background()
	pool, store, def, done := setupOrders(ctx)
	defer done()
	db := stdlib.OpenDBFromPool(pool) // database/sql on the store's pool

	tx, err := db.BeginTx(ctx, nil)
	fatalIf(err)
	defer tx.Rollback() // a no-op after Commit
	_, err = tx.ExecContext(ctx, `INSERT INTO example_sqltx.orders (id) VALUES ('order-1')`)
	fatalIf(err)
	res, err := store.StartSQLTx(ctx, tx, postgres.NewRun{ID: "order-1", Definition: def})
	fatalIf(err)
	fatalIf(tx.Commit()) // the order and its run, or neither
	fmt.Println(res.RunID, res.Applied, res.State.InFlight())
	// Output: order-1 true [main/approve#1]
}

// setupOrders returns a pool, a store in a fresh schema example_sqltx with
// an orders table, a definition with one step, and a func that drops the
// schema and closes the pool.
func setupOrders(
	ctx context.Context,
) (*pgxpool.Pool, *postgres.Store, *workflow.Definition, func()) {
	pool, err := pgxpool.New(ctx, os.Getenv("WORKFLOW_PG_DSN"))
	fatalIf(err)
	_, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS example_sqltx CASCADE;
		CREATE SCHEMA example_sqltx;
		CREATE TABLE example_sqltx.orders (id text PRIMARY KEY)`)
	fatalIf(err)
	kinds := workflow.KindMap{"agent": nil}
	store, err := postgres.New(pool, kinds, postgres.WithSchema("example_sqltx"))
	fatalIf(err)
	fatalIf(store.Migrate(ctx))
	def, err := workflow.Load([]byte(`{"steps": [{"name": "approve", "agent": "human"}]}`), kinds)
	fatalIf(err)
	done := func() {
		_, _ = pool.Exec(ctx, `DROP SCHEMA example_sqltx CASCADE`)
		pool.Close()
	}
	return pool, store, def, done
}

func fatalIf(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
