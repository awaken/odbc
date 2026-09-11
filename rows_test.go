// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"testing"
)

func TestRowsCloseAfterFinalResultSet(t *testing.T) {
	s := &ODBCStmt{usedByStmt: true}
	s.markUsedByRows()
	s.markNoMoreResults()

	if err := s.closeByRows(); err != nil {
		t.Fatalf("close rows after SQLMoreResults returned SQL_NO_DATA: %v", err)
	}
	if s.isUsedByRows() {
		t.Fatal("rows still own the statement after close")
	}
}

func TestRowsPreparedFinalResultSet(t *testing.T) {
	dsn := os.Getenv("ODBC_SQLITE_DSN")
	if dsn == "" {
		t.Skip("set ODBC_SQLITE_DSN to exercise final-result cleanup")
	}

	db, err := sql.Open("odbc", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	stmtCount := db.Driver().(*Driver).Stats().StmtCount

	stmt, err := db.Prepare("select 1")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()

	for i := 0; i < 2; i++ {
		rows, err := stmt.Query()
		if err != nil {
			t.Fatalf("query %d: %v", i, err)
		}

		if i == 0 {
			if !rows.Next() {
				t.Fatalf("query %d first row: %v", i, rows.Err())
			}
			var value int
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			if value != 1 {
				t.Fatalf("query %d value = %d; want 1", i, value)
			}
			if rows.Next() {
				t.Fatalf("query %d returned an unexpected second row", i)
			}
		}
		if rows.NextResultSet() {
			t.Fatalf("query %d returned an unexpected result set", i)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("query %d final result-set cleanup: %v", i, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("query %d close: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if got := db.Driver().(*Driver).Stats().StmtCount; got != stmtCount {
		t.Fatalf("statement handles = %d; want %d", got, stmtCount)
	}
}

// A finite cursor drives database/sql through the real Rows lifecycle.
type rowsTestCursor struct {
	sets       [][][]driver.Value
	set, row   int
	columns    []string
	advances   int
	bindings   int
	closed     int
	advanceErr error
	fetchErr   error
	bindErr    error
	closeErr   error
}

func (c *rowsTestCursor) Columns() []string { return c.columns }
func (c *rowsTestCursor) Next(dest []driver.Value) error {
	if c.fetchErr != nil {
		return c.fetchErr
	}
	if c.set >= len(c.sets) || c.row >= len(c.sets[c.set]) {
		return io.EOF
	}
	copy(dest, c.sets[c.set][c.row])
	c.row++
	return nil
}
func (c *rowsTestCursor) Advance() error {
	c.advances++
	if c.advanceErr != nil {
		return c.advanceErr
	}
	c.set++
	c.row = 0
	if c.set >= len(c.sets) {
		return io.EOF
	}
	return nil
}
func (c *rowsTestCursor) BindColumns() error {
	c.bindings++
	if c.bindErr != nil {
		return c.bindErr
	}
	width := 1
	if len(c.sets[c.set]) != 0 {
		width = len(c.sets[c.set][0])
	}
	c.columns = make([]string, width)
	for i := range c.columns {
		c.columns[i] = "value"
	}
	return nil
}
func (c *rowsTestCursor) Close() error { c.closed++; return c.closeErr }

type rowsTestConnector struct{ cursor *rowsTestCursor }

func (c rowsTestConnector) Connect(context.Context) (driver.Conn, error) {
	return rowsTestConn{cursor: c.cursor}, nil
}
func (rowsTestConnector) Driver() driver.Driver { return rowsTestDriver{} }

type rowsTestDriver struct{}

func (rowsTestDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type rowsTestConn struct{ cursor *rowsTestCursor }

func (rowsTestConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (rowsTestConn) Close() error { return nil }
func (rowsTestConn) Begin() (driver.Tx, error) { return nil, errors.New("unused") }
func (c rowsTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &Rows{rowsCursor: c.cursor}, nil
}

func TestRowsFinalEOFAutomaticallyCloses(t *testing.T) {
	cursor := &rowsTestCursor{sets: [][][]driver.Value{{{int64(42)}}}, columns: []string{"value"}}
	db := sql.OpenDB(rowsTestConnector{cursor: cursor})
	db.SetMaxOpenConns(1)
	defer db.Close()
	rows, err := db.QueryContext(context.Background(), "local fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("first row: %v", rows.Err())
	}
	var value int64
	if err := rows.Scan(&value); err != nil || value != 42 {
		t.Fatalf("value=%d, error=%v", value, err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("final fetch: %v", rows.Err())
	}
	if cursor.closed != 1 || cursor.advances != 1 || db.Stats().InUse != 0 {
		t.Fatalf("final EOF retained resources: close=%d, lookahead=%d, connections in use=%d", cursor.closed, cursor.advances, db.Stats().InUse)
	}
}

func TestRowsResultSetLookahead(t *testing.T) {
	for _, early := range []bool{false, true} {
		name := "fetch-to-end"
		if early {
			name = "skip-unread-rows"
		}
		t.Run(name, func(t *testing.T) {
			cursor := &rowsTestCursor{
				sets: [][][]driver.Value{
					{{int64(1)}, {int64(2)}},
					{},
					{{int64(3), int64(4)}},
				},
				columns: []string{"first"},
			}
			db := sql.OpenDB(rowsTestConnector{cursor: cursor})
			defer db.Close()
			rows, err := db.QueryContext(context.Background(), "local fixture")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if !rows.Next() {
				t.Fatal(rows.Err())
			}
			if !early {
				if !rows.Next() || rows.Next() {
					t.Fatalf("first set: %v", rows.Err())
				}
				columns, err := rows.Columns()
				if err != nil || len(columns) != 1 || columns[0] != "first" || cursor.advances != 1 || cursor.bindings != 0 {
					t.Fatalf("lookahead changed metadata: %v, %v, advances=%d, bindings=%d", columns, err, cursor.advances, cursor.bindings)
				}
			}
			if !rows.NextResultSet() || cursor.advances != 1 || cursor.bindings != 1 {
				t.Fatalf("second set advanced twice: %v, advances=%d, bindings=%d", rows.Err(), cursor.advances, cursor.bindings)
			}
			if rows.Next() || !rows.NextResultSet() {
				t.Fatalf("empty set: %v", rows.Err())
			}
			columns, err := rows.Columns()
			if err != nil || len(columns) != 2 || !rows.Next() {
				t.Fatalf("third set: columns=%v, error=%v, rows error=%v", columns, err, rows.Err())
			}
			var a, b int64
			if err := rows.Scan(&a, &b); err != nil || a != 3 || b != 4 {
				t.Fatalf("third set row=%d,%d; %v", a, b, err)
			}
			if rows.Next() || rows.NextResultSet() || rows.Err() != nil || cursor.advances != 3 || cursor.bindings != 2 || cursor.closed != 1 || db.Stats().InUse != 0 {
				t.Fatalf("final cleanup: %v, advances=%d, bindings=%d, closed=%d, in use=%d", rows.Err(), cursor.advances, cursor.bindings, cursor.closed, db.Stats().InUse)
			}
		})
	}
}

func TestRowsCursorFailures(t *testing.T) {
	failure := errors.New("cursor operation failed")
	for _, stage := range []string{"fetch", "lookahead", "binding", "close"} {
		t.Run(stage, func(t *testing.T) {
			cursor := &rowsTestCursor{sets: [][][]driver.Value{{{int64(1)}}, {{int64(2)}}}, columns: []string{"value"}}
			switch stage {
			case "fetch":
				cursor.fetchErr = failure
			case "lookahead":
				cursor.advanceErr = failure
			case "binding":
				cursor.bindErr = failure
			case "close":
				cursor.sets = cursor.sets[:1]
				cursor.closeErr = failure
			}
			db := sql.OpenDB(rowsTestConnector{cursor: cursor})
			defer db.Close()
			rows, err := db.QueryContext(context.Background(), "local fixture")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if stage != "fetch" && !rows.Next() {
				t.Fatal(rows.Err())
			}
			if rows.Next() {
				t.Fatal("unexpected extra row")
			}
			if stage == "binding" && rows.NextResultSet() {
				t.Fatal("failed binding was accepted")
			}
			if !errors.Is(rows.Err(), failure) || cursor.closed != 1 || db.Stats().InUse != 0 {
				t.Fatalf("failure=%v; close=%d, in use=%d", rows.Err(), cursor.closed, db.Stats().InUse)
			}
		})
	}
}

func TestRowsCachedAdvanceConsumedOnce(t *testing.T) {
	cursor := &rowsTestCursor{sets: [][][]driver.Value{{}, {{int64(7)}}}, columns: []string{"value"}}
	rows := &Rows{rowsCursor: cursor}
	values := make([]driver.Value, 1)
	for i := 0; i < 2; i++ {
		if err := rows.Next(values); err != io.EOF || !rows.HasNextResultSet() || cursor.advances != 1 {
			t.Fatalf("cached lookahead %d: %v, advance=%d", i, err, cursor.advances)
		}
	}
	if err := rows.NextResultSet(); err != nil || cursor.advances != 1 {
		t.Fatalf("consume lookahead: %v, advances=%d", err, cursor.advances)
	}
	if err := rows.Next(values); err != nil || values[0] != int64(7) {
		t.Fatalf("next set row: %v, %v", values, err)
	}
	if err := rows.NextResultSet(); err != io.EOF || rows.HasNextResultSet() {
		t.Fatalf("early final advance: %v", err)
	}
	for i := 0; i < 2; i++ {
		if rows.Next(values) != io.EOF || rows.NextResultSet() != io.EOF || rows.Close() != nil || rows.HasNextResultSet() || cursor.advances != 2 || cursor.closed != 1 {
			t.Fatal("completed cursor performed another native operation")
		}
	}
}
