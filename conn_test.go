// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/alexbrainman/odbc/api"
)

func TestQueryContextStopsBeforeNativeCall(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := new(Conn).PrepareContext(cancelled, "select 1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareContext cancelled error = %v; want %v", err, context.Canceled)
	}
	if _, err := new(Conn).ExecContext(cancelled, "select 1", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecContext cancelled error = %v; want %v", err, context.Canceled)
	}
	if _, err := new(Conn).QueryContext(cancelled, "select 1", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryContext cancelled error = %v; want %v", err, context.Canceled)
	}

	connection := &Conn{h: 1}
	connection.invalidate()
	if _, err := connection.PrepareContext(context.Background(), "select 1"); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("PrepareContext bad connection error = %v; want %v", err, driver.ErrBadConn)
	}
	if _, err := connection.ExecContext(context.Background(), "select 1", nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("ExecContext bad connection error = %v; want %v", err, driver.ErrBadConn)
	}
	if _, err := connection.QueryContext(context.Background(), "select 1", nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("QueryContext bad connection error = %v; want %v", err, driver.ErrBadConn)
	}
}

func TestAuditNamedArgumentsRejected(t *testing.T) {
	c := new(Conn)
	s := &Stmt{c: c}
	args := []driver.NamedValue{{Name: "named", Value: 1}}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"connection execute", func() error { _, err := c.ExecContext(context.Background(), "fixture", args); return err }},
		{"connection query", func() error { _, err := c.QueryContext(context.Background(), "fixture", args); return err }},
		{"statement execute", func() error { _, err := s.ExecContext(context.Background(), args); return err }},
		{"statement query", func() error { _, err := s.QueryContext(context.Background(), args); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "Named Parameters") {
				t.Fatalf("named argument reached native admission: %v", err)
			}
		})
	}
}

func TestAuditConnectionLossDiagnostic(t *testing.T) {
	f := auditNative(t)
	f.set(16, 4)
	f.mode(nativeExecute, 2)
	defer f.mode(nativeExecute, 0)
	_, err := f.conn.ExecContext(context.Background(), "fixture", nil)
	var diagnostic *Error
	if !errors.As(err, &diagnostic) || len(diagnostic.Diag) != 1 || diagnostic.Diag[0].State != "08S01" || f.conn.IsValid() {
		t.Fatalf("connection loss diagnostic: error=%v valid=%v", err, f.conn.IsValid())
	}
	if errors.Is(err, driver.ErrBadConn) {
		t.Fatal("started failed work may be replayed by database/sql")
	}
	auditWaitClose(t, f.conn)
}

func TestAuditInternalAdmissionGuards(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := new(Conn)
	s := &Stmt{c: c, os: new(ODBCStmt)}
	for _, tc := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"prepare", func(ctx context.Context) error { _, err := c.prepareContext(ctx, "fixture"); return err }},
		{"execute", func(ctx context.Context) error { _, err := c.execContext(ctx, "fixture", nil); return err }},
		{"query", func(ctx context.Context) error { _, err := c.queryContext(ctx, "fixture", nil); return err }},
		{"statement execute", func(ctx context.Context) error { _, err := s.execContext(ctx, nil); return err }},
		{"statement query", func(ctx context.Context) error { _, err := s.queryContext(ctx, nil); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled work: %v", err)
			}
			if err := tc.run(context.Background()); !errors.Is(err, driver.ErrBadConn) {
				t.Fatalf("invalid connection: %v", err)
			}
		})
	}
	if _, err := c.prepare("fixture"); !errors.Is(err, driver.ErrBadConn) {
		t.Fatal(err)
	}
	if _, err := c.prepareODBCStmtContext(ctx, "fixture"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := c.prepareAllocatedODBCStmt(new(ODBCStmt), api.StringToUTF16("fixture")); !errors.Is(err, errNativeInvalid) {
		t.Fatal(err)
	}
}

func TestAuditContextPositionalBinding(t *testing.T) {
	f := auditNative(t)
	f.set(2, 1)
	args := []driver.NamedValue{{Ordinal: 1, Value: "owned"}}
	result, err := f.conn.ExecContext(context.Background(), "fixture", args)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("affected rows: count=%d error=%v", count, err)
	}
	if f.get(5) != 10 || f.get(8) != int64(api.SQL_C_WCHAR) {
		t.Fatalf("positional text binding: length=%d type=%d", f.get(5), f.get(8))
	}
	r, err := f.conn.QueryContext(context.Background(), "fixture", args)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]driver.Value, 1)
	if err := r.Next(values); err != nil || values[0] != int32(42) {
		t.Errorf("positional query: values=%v error=%v", values, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if result, err := f.conn.ExecContext(context.Background(), "fixture", nil); result != nil || err == nil {
		t.Fatalf("missing execution argument: result=%v error=%v", result, err)
	}
	if rows, err := f.conn.QueryContext(context.Background(), "fixture", nil); rows != nil || err == nil {
		t.Fatalf("missing query argument: rows=%v error=%v", rows, err)
	}
	if f.conn.driver.Stats().StmtCount != 1 || !f.conn.IsValid() {
		t.Fatalf("positional execution leaked resources: %+v", f.conn.driver.Stats())
	}
}

func TestRejectsEmbeddedNULBeforeNativeCall(t *testing.T) {
	if _, err := new(Driver).Open("DSN=valid\x00DSN=ignored"); err == nil {
		t.Fatal("Open unexpectedly accepted a NUL-containing connection string")
	}
	if _, err := new(Conn).PrepareODBCStmt("select 1\x00select 2"); err == nil {
		t.Fatal("PrepareODBCStmt unexpectedly accepted a NUL-containing query")
	}
}

func TestConnectionValidity(t *testing.T) {
	if err := new(Conn).Close(); err != nil {
		t.Fatalf("Close on closed connection: %v", err)
	}
	connection := &Conn{h: 1}
	if !connection.IsValid() {
		t.Fatal("open connection is invalid")
	}
	connection.bad.Store(true)
	if connection.IsValid() {
		t.Fatal("bad connection is valid")
	}
	connection.bad.Store(false)
	connection.h = 0
	if connection.IsValid() {
		t.Fatal("closed connection is valid")
	}
}

func TestConnectionInvalidationConcurrent(t *testing.T) {
	connection := &Conn{h: 1}
	var waitGroup sync.WaitGroup
	for i := 0; i < 32; i++ {
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			connection.invalidate()
		}()
		go func() {
			defer waitGroup.Done()
			_ = connection.IsValid()
		}()
	}
	waitGroup.Wait()
	if connection.IsValid() {
		t.Fatal("invalidated connection is valid")
	}
}

func TestODBCDriverAttributes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dsn     string
		invalid bool
	}{
		{name: "braced driver", dsn: "DRIVER={SQLite3}"},
		{name: "unbraced driver", dsn: "driver=SQLite3"},
		{name: "case and spacing", dsn: " driver = {sqlite3} ; Database=fixture"},
		{name: "other driver", dsn: "DRIVER={SQLite3};Database=fixture"},
		{name: "named DSN", dsn: "DSN=fixture"},
		{name: "empty", dsn: " ; ; "},
		{name: "unrelated unbraced value", dsn: "Description=DRIVER={Other};DRIVER={SQLite3}"},
		{name: "unrelated braced value", dsn: "Description={note;DRIVER={Other}}};DRIVER={SQLite3}"},
		{name: "driver-looking key", dsn: "NotDriver={Other};DRIVER={SQLite3}"},
		{name: "escaped driver brace", dsn: "DRIVER={Other}}Driver}"},
		{name: "equal duplicates", dsn: "DRIVER={SQLite3};driver=sqlite3"},
		{name: "conflicting duplicates", dsn: "DRIVER={SQLite3};DRIVER={Other}", invalid: true},
		{name: "reversed conflicting duplicates", dsn: "DRIVER={Other};DRIVER={SQLite3}", invalid: true},
		{name: "unclosed brace", dsn: "DRIVER={SQLite3", invalid: true},
		{name: "trailing characters", dsn: "DRIVER={SQLite3}suffix", invalid: true},
		{name: "missing equals", dsn: "DSN;DRIVER={SQLite3}", invalid: true},
		{name: "empty key", dsn: "={SQLite3}", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connector, err := new(Driver).OpenConnector(tc.dsn)
			if (connector == nil) != tc.invalid || (err != nil) != tc.invalid {
				t.Fatalf("connector=%v err=%v; want invalid=%t", connector, err, tc.invalid)
			}
		})
	}
}

func TestODBCBadAttributesBeforeOpen(t *testing.T) {
	for _, dsn := range []string{"DRIVER={SQLite3}trailing", "DRIVER={SQLite3};DRIVER={Other}"} {
		d := new(Driver)
		conn, err := d.Open(dsn)
		if conn != nil || err == nil || d.h != 0 || strings.Contains(err.Error(), "SQLite3") {
			t.Fatalf("invalid attributes reached native open: conn=%v err=%v handle=%v", conn, err, d.h)
		}
	}
}
