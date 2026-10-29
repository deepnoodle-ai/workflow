package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// The suite's harness opens every database through a counting connector,
// so it sees each error the driver returns to the store: a connection, a
// statement, a row, a transaction or a commit. PRD 007's UC-3 says no
// SQLITE_BUSY reaches a caller or a log; a test fails at its end when one
// did (see harness.Open).

// busyCount counts SQLITE_BUSY errors on one file, across its handles.
type busyCount struct {
	n atomic.Int64
	// held is set by HoldRunLock: the test holds the write lock on purpose,
	// so writes that wait past the busy timeout are expected.
	held atomic.Bool
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked")
}

func (c *busyCount) see(err error) error {
	if isBusy(err) {
		c.n.Add(1)
	}
	return err
}

// openCounted opens dsn on the named driver, with every error counted.
func openCounted(driverName, dsn string, c *busyCount) (*sql.DB, error) {
	probe, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	d := probe.Driver()
	_ = probe.Close()
	var conn driver.Connector = dsnConnector{dsn: dsn, d: d}
	if dc, ok := d.(driver.DriverContext); ok {
		if conn, err = dc.OpenConnector(dsn); err != nil {
			return nil, err
		}
	}
	return sql.OpenDB(countingConnector{Connector: conn, c: c}), nil
}

type dsnConnector struct {
	dsn string
	d   driver.Driver
}

func (k dsnConnector) Connect(context.Context) (driver.Conn, error) { return k.d.Open(k.dsn) }
func (k dsnConnector) Driver() driver.Driver                        { return k.d }

type countingConnector struct {
	driver.Connector
	c *busyCount
}

func (k countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	cn, err := k.Connector.Connect(ctx)
	if err != nil {
		return nil, k.c.see(err)
	}
	return &countingConn{Conn: cn, c: k.c}, nil
}

// countingConn has every optional interface database/sql uses, so the
// driver's behaviour does not change; each one delegates, or reports that
// the driver lacks it.
type countingConn struct {
	driver.Conn
	c *busyCount
}

func (x *countingConn) Prepare(q string) (driver.Stmt, error) {
	st, err := x.Conn.Prepare(q)
	if err != nil {
		return nil, x.c.see(err)
	}
	return &countingStmt{Stmt: st, c: x.c}, nil
}

func (x *countingConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	p, ok := x.Conn.(driver.ConnPrepareContext)
	if !ok {
		return x.Prepare(q)
	}
	st, err := p.PrepareContext(ctx, q)
	if err != nil {
		return nil, x.c.see(err)
	}
	return &countingStmt{Stmt: st, c: x.c}, nil
}

func (x *countingConn) ExecContext(
	ctx context.Context, q string, args []driver.NamedValue,
) (driver.Result, error) {
	e, ok := x.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	r, err := e.ExecContext(ctx, q, args)
	return r, x.c.see(err)
}

func (x *countingConn) QueryContext(
	ctx context.Context, q string, args []driver.NamedValue,
) (driver.Rows, error) {
	qr, ok := x.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := qr.QueryContext(ctx, q, args)
	if err != nil {
		return nil, x.c.see(err)
	}
	return &countingRows{Rows: rows, c: x.c}, nil
}

func (x *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var (
		tx  driver.Tx
		err error
	)
	if b, ok := x.Conn.(driver.ConnBeginTx); ok {
		tx, err = b.BeginTx(ctx, opts)
	} else {
		tx, err = x.Conn.Begin() //nolint:staticcheck // the driver has no BeginTx
	}
	if err != nil {
		return nil, x.c.see(err)
	}
	return &countingTx{Tx: tx, c: x.c}, nil
}

func (x *countingConn) Ping(ctx context.Context) error {
	if p, ok := x.Conn.(driver.Pinger); ok {
		return x.c.see(p.Ping(ctx))
	}
	return nil
}

func (x *countingConn) ResetSession(ctx context.Context) error {
	if r, ok := x.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (x *countingConn) IsValid() bool {
	if v, ok := x.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (x *countingConn) CheckNamedValue(nv *driver.NamedValue) error {
	if c, ok := x.Conn.(driver.NamedValueChecker); ok {
		return c.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

type countingStmt struct {
	driver.Stmt
	c *busyCount
}

func (s *countingStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	e, ok := s.Stmt.(driver.StmtExecContext)
	if !ok {
		return nil, errors.New("sqlite test: the driver's statement has no ExecContext")
	}
	r, err := e.ExecContext(ctx, args)
	return r, s.c.see(err)
}

func (s *countingStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, errors.New("sqlite test: the driver's statement has no QueryContext")
	}
	rows, err := q.QueryContext(ctx, args)
	if err != nil {
		return nil, s.c.see(err)
	}
	return &countingRows{Rows: rows, c: s.c}, nil
}

type countingRows struct {
	driver.Rows
	c *busyCount
}

func (r *countingRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err != io.EOF {
		r.c.see(err)
	}
	return err
}

type countingTx struct {
	driver.Tx
	c *busyCount
}

func (t *countingTx) Commit() error   { return t.c.see(t.Tx.Commit()) }
func (t *countingTx) Rollback() error { return t.c.see(t.Tx.Rollback()) }

// TestBusyErrorsAreCounted: the counting connector sees a SQLITE_BUSY that
// a write gets while another connection holds the write lock, so the
// suite's check would catch one.
func TestBusyErrorsAreCounted(t *testing.T) {
	busy := &busyCount{}
	db := harness{driver: "sqlite", dsn: "file:%s?_busy_timeout=50"}.openDB(
		t, t.TempDir()+"/busy.db", busy)
	_, err := db.Exec(`CREATE TABLE t (x INTEGER)`)
	require.NoError(t, err)
	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO t VALUES (1)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO t VALUES (2)`)
	require.True(t, isBusy(err), err)
	require.Equal(t, int64(1), busy.n.Load())
}
