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
	pool, err := pgxpool.New(ctx, os.Getenv("WORKFLOW_PG_DSN"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS example_sqltx CASCADE`)
	defer pool.Exec(ctx, `DROP SCHEMA example_sqltx CASCADE`)
	kinds := workflow.KindMap{"agent": nil}
	store, _ := postgres.New(pool, kinds, postgres.WithSchema("example_sqltx"))
	_ = store.Migrate(ctx)
	_, _ = pool.Exec(ctx, `CREATE TABLE example_sqltx.orders (id text PRIMARY KEY)`)
	def, _ := workflow.Load([]byte(`{"steps": [{"name": "approve", "agent": "human"}]}`), kinds)

	db := stdlib.OpenDBFromPool(pool) // database/sql on the store's pool
	tx, _ := db.BeginTx(ctx, nil)
	_, _ = tx.ExecContext(ctx, `INSERT INTO example_sqltx.orders (id) VALUES ('order-1')`)
	res, err := store.StartSQLTx(ctx, tx, postgres.NewRun{ID: "order-1", Definition: def})
	if err != nil {
		log.Fatal(err)
	}
	_ = tx.Commit()
	fmt.Println(res.RunID, res.Applied, res.State.InFlight())
	// Output: order-1 true [main/approve#1]
}
