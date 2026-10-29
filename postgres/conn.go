package postgres

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// conn is what the store's statements run on: the caller's transaction,
// or the pool for a read outside one. pgx.Tx and *pgxpool.Pool satisfy it
// as they are; sqlConn adapts a database/sql transaction.
//
// The contract: Exec reports the rows affected (RowsAffected on the tag);
// QueryRow returns a row whose Scan reports no row as pgx.ErrNoRows; neither
// leaves a result open. Keep it this small. The SQLite backend (#53) reuses
// it and sqlConn, with the pgx result types swapped for neutral ones.
type conn interface {
	Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, query string, args ...any) pgx.Row
}

// sqlConn runs the store's statements on a database/sql transaction.
type sqlConn struct{ tx SQLTx }

func (c sqlConn) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	res, err := c.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	// A tag in Postgres's form, "<verb> <rows>", so RowsAffected reads n.
	return pgconn.NewCommandTag("EXEC " + strconv.FormatInt(n, 10)), nil
}

func (c sqlConn) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	rows, err := c.tx.QueryContext(ctx, query, args...)
	return sqlRow{rows: rows, err: err}
}

// sqlRow is the first row of a database/sql query. Scan closes the rows,
// so the transaction can run its next statement.
type sqlRow struct {
	rows *sql.Rows
	err  error
}

func (r sqlRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	if err := r.rows.Scan(dest...); err != nil {
		return err
	}
	return r.rows.Close()
}
