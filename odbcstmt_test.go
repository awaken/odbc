// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !static && linux && (amd64 || arm64)

package odbc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexbrainman/odbc/api"
	"github.com/ebitengine/purego"
)

// nativeFixture uses only the explicitly selected, owned in-memory test driver.
type nativeFixture struct {
	mode       func(int32, int32)
	calls      func(int32) int32
	active     func(int32) int32
	columns    func(int32)
	violations func() int32
	reset      func()
}

func TestAuditNativePanicGuards(t *testing.T) {
	for _, tc := range []struct {
		name, call string
		setup      func(*testing.T, *auditODBC)
		run        func(*auditODBC) error
		invalid    bool
	}{
		{"allocate", "SQLAllocHandle", nil, func(f *auditODBC) error { _, err := f.conn.PrepareODBCStmt("fixture"); return err }, true},
		{"prepare", "SQLPrepare", nil, func(f *auditODBC) error { _, err := f.conn.PrepareODBCStmt("fixture"); return err }, true},
		{"execute", "SQLExecute", nil, func(f *auditODBC) error { return f.stmt.execute(f.conn) }, true},
		{"count columns", "SQLNumResultCols", nil, func(f *auditODBC) error { return f.stmt.BindColumns(f.conn) }, true},
		{"unbind", "SQLFreeStmt", func(t *testing.T, f *auditODBC) {
			if err := f.stmt.BindColumns(f.conn); err != nil {
				t.Fatal(err)
			}
		}, func(f *auditODBC) error { return f.stmt.BindColumns(f.conn) }, true},
		{"cancel", "SQLCancel", nil, func(f *auditODBC) error { return f.stmt.Cancel(f.conn) }, true},
		{"fetch", "SQLFetch", nil, func(f *auditODBC) error { return (&odbcRows{os: f.stmt, c: f.conn}).Next(nil) }, true},
		{"advance", "SQLMoreResults", nil, func(f *auditODBC) error { return (&odbcRows{os: f.stmt, c: f.conn}).Advance() }, true},
		{"skip update count", "SQLMoreResults", func(_ *testing.T, f *auditODBC) { f.cols(0) }, func(f *auditODBC) error { return f.stmt.BindColumns(f.conn) }, true},
		{"autocommit", "SQLSetConnectAttr", nil, func(f *auditODBC) error { _, err := f.conn.begin(); return err }, true},
		{"transaction", "SQLEndTran", func(t *testing.T, f *auditODBC) {
			if _, err := f.conn.begin(); err != nil {
				t.Fatal(err)
			}
		}, func(f *auditODBC) error { return f.conn.endTx(true) }, true},
		{"diagnostics", "SQLGetDiagRec", nil, func(f *auditODBC) error { return f.conn.newError("fixture", f.conn.h) }, false},
		{"bind parameter", "SQLBindParameter", func(t *testing.T, f *auditODBC) {
			f.stmt.Parameters = make([]Parameter, 1)
			if err := f.stmt.bind([]driver.Value{"old"}, f.conn); err != nil {
				t.Fatal(err)
			}
		}, func(f *auditODBC) error { return f.stmt.bind([]driver.Value{"replacement"}, f.conn) }, true},
		{"count parameters", "SQLNumParams", nil, func(f *auditODBC) error { _, err := f.conn.PrepareODBCStmt("fixture"); return err }, true},
		{"bind column", "SQLBindCol", nil, func(f *auditODBC) error { return f.stmt.BindColumns(f.conn) }, true},
		{"metadata information", "SQLGetInfo", nil, func(f *auditODBC) error { _, err := f.conn.Info(context.Background()); return err }, true},
		{"metadata catalog", "SQLTables", nil, func(f *auditODBC) error {
			_, err := f.conn.Catalog(context.Background(), CatalogTables, CatalogFilter{})
			return err
		}, true},
		{"fixed data", "SQLGetData", nil, func(f *auditODBC) error {
			c := NewBindableColumn(&BaseColumn{}, api.SQL_C_LONG, 4)
			_, err := c.Value(f.stmt.h, 0)
			return err
		}, false},
		{"variable data", "SQLGetData", nil, func(f *auditODBC) error {
			c := &NonBindableColumn{BaseColumn: &BaseColumn{CType: api.SQL_C_CHAR}}
			_, err := c.Value(f.stmt.h, 0)
			return err
		}, false},
		{"cursor close", "SQLCloseCursor", func(_ *testing.T, f *auditODBC) { f.stmt.markUsedByRows() }, func(f *auditODBC) error {
			return (&odbcRows{os: f.stmt, c: f.conn}).Close()
		}, true},
		{"disconnect", "SQLDisconnect", nil, func(f *auditODBC) error { return f.conn.closeNative() }, false},
		{"free handle", "SQLFreeHandle", nil, func(f *auditODBC) error { return releaseHandle(f.stmt.h, f.stmt.stats) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := auditNative(t)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			restore := auditCallPanic(t, tc.call, 1)
			defer restore()
			err := tc.run(f)
			if err == nil || !strings.Contains(err.Error(), "owned audit native failure") || f.conn.IsValid() == tc.invalid {
				t.Errorf("native panic: error=%v valid=%v; want invalid=%v", err, f.conn.IsValid(), tc.invalid)
			}
			if tc.name == "bind parameter" && len(f.stmt.Parameters[0].retiredPinners) != 1 {
				t.Error("failed rebinding released the previous native buffer")
			}
			// The injected panic happened before entering C. Cleanup can safely
			// call the original fixture while the one-shot wrapper remains installed.
			if err := f.conn.Close(); err != nil && !errors.Is(err, ErrCleanupPending) {
				t.Fatal(err)
			}
			auditWaitClose(t, f.conn)
		})
	}
}

func TestAuditInvalidStatementWork(t *testing.T) {
	f := auditNative(t)
	f.stmt.Parameters = make([]Parameter, 1)
	f.conn.invalidate()
	if err := f.stmt.bind([]driver.Value{"owned"}, f.conn); !errors.Is(err, errNativeInvalid) {
		t.Fatalf("binding on invalid connection: %v", err)
	}
	if err := f.stmt.execute(f.conn); !errors.Is(err, errNativeInvalid) {
		t.Fatalf("execution on invalid connection: %v", err)
	}
	if p := &f.stmt.Parameters[0]; p.Data != nil || p.pinner != nil {
		t.Fatal("invalid connection acquired parameter buffers")
	}
	if err := f.conn.Close(); err != nil && !errors.Is(err, ErrCleanupPending) {
		t.Fatal(err)
	}
	auditWaitClose(t, f.conn)
}

func openNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	path := os.Getenv("FLOWER_ODBC_TEST_LIBRARY")
	if path == "" {
		t.Skip("owned ODBC test library not selected")
	}
	if os.Getenv(api.DriverManagerEnvironment) != path {
		t.Fatal("ODBC library must match the selected test fixture")
	}
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		t.Fatal(err)
	}
	f := new(nativeFixture)
	for name, target := range map[string]any{
		"TestODBCMode": &f.mode, "TestODBCCalls": &f.calls, "TestODBCActive": &f.active,
		"TestODBCColumns": &f.columns, "TestODBCViolations": &f.violations, "TestODBCReset": &f.reset,
	} {
		purego.RegisterLibFunc(target, h, name)
	}
	f.reset()
	t.Cleanup(func() {
		for op := int32(nativeAlloc); op <= nativeConnect; op++ {
			if f.active(op) != 0 {
				t.Errorf("native operation %d remains active", op)
				return // Never release memory still used by a native call.
			}
		}
		if n := f.violations(); n != 0 {
			t.Errorf("native lifetime violations: %d", n)
		}
		f.reset()
		_ = purego.Dlclose(h)
	})
	return f
}

func (f *nativeFixture) connection(t *testing.T) *Conn {
	t.Helper()
	d := &Driver{CloseTimeout: 20 * time.Millisecond}
	raw, err := d.Open("DRIVER={Owned Test Fixture}")
	if err != nil {
		t.Fatal(err)
	}
	c := raw.(*Conn)
	t.Cleanup(func() {
		err := c.Close()
		if errors.Is(err, ErrCleanupPending) {
			select {
			case <-c.nativeOwner().closeDone:
				err = c.nativeOwner().closeErr
			case <-time.After(2 * time.Second):
				t.Error("fixture native cleanup did not complete")
				return
			}
		}
		if err != nil {
			t.Error(err)
			return
		}
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func TestODBCColumnGenerationReleasesOldPins(t *testing.T) {
	f := openNativeFixture(t)
	c := f.connection(t)
	raw, err := c.Prepare("fixture")
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Stmt)
	defer s.Close()
	var previous []*BindableColumn
	for _, n := range []int32{3, 1, 2, 1, 3, 1} {
		f.columns(n)
		rows, err := s.Query(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, old := range previous {
			if old.pinner != nil {
				t.Error("obsolete column remains pinned after replacement")
			}
		}
		previous = nil
		for _, col := range s.os.Cols {
			previous = append(previous, col.(*BindableColumn))
		}
		values := make([]driver.Value, n)
		if err := rows.Next(values); err != nil || values[0] != int32(42) {
			_ = rows.Close()
			t.Fatalf("fetch: %v, %v", values, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.calls(nativeUnbind); n != 5 {
		t.Errorf("native unbind calls = %d, want 5", n)
	}
}

func TestODBCColumnFailurePins(t *testing.T) {
	for _, op := range []int32{nativeUnbind, nativeBind, nativeDescribe} {
		t.Run(map[int32]string{nativeUnbind: "unbind", nativeBind: "bind", nativeDescribe: "describe"}[op], func(t *testing.T) {
			f := openNativeFixture(t)
			c := f.connection(t)
			raw, err := c.Prepare("fixture")
			if err != nil {
				t.Fatal(err)
			}
			s := raw.(*Stmt)
			defer s.Close()
			if err := s.os.BindColumns(c); err != nil {
				t.Fatal(err)
			}
			old := s.os.Cols[0].(*BindableColumn)
			f.mode(op, 2)
			if err := s.os.BindColumns(c); err == nil || c.IsValid() {
				t.Fatalf("binding failure: %v, valid=%t", err, c.IsValid())
			}
			if (old.pinner != nil) != (op == nativeUnbind) {
				t.Fatal("old pin lifetime disagrees with native unbind result")
			}
			var current *BindableColumn
			if op == nativeBind {
				current = s.os.Cols[0].(*BindableColumn)
				if current.pinner == nil {
					t.Fatal("failed native bind released its buffer early")
				}
			}
			if err := s.os.BindColumns(c); !errors.Is(err, driver.ErrBadConn) {
				t.Fatalf("invalid connection reused: %v", err)
			}
			f.mode(op, 0)
			if err := s.Close(); err != nil && !errors.Is(err, ErrCleanupPending) {
				t.Fatalf("invalid connection cleanup: %v", err)
			}
			<-c.nativeOwner().closeDone
			if old.pinner != nil || (current != nil && current.pinner != nil) {
				t.Fatal("confirmed handle release retained pins")
			}
		})
	}
}

func (f *nativeFixture) waitActive(t *testing.T, op int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for f.active(op) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("operation %d did not start", op)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestODBCNativeContextDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   int32
		run  func(context.Context, *Conn) error
	}{
		{"prepare", nativePrepare, func(ctx context.Context, c *Conn) error {
			s, err := c.PrepareContext(ctx, "fixture")
			if s != nil {
				_ = s.Close()
			}
			return err
		}},
		{"allocation", nativeAlloc, func(ctx context.Context, c *Conn) error {
			s, err := c.PrepareContext(ctx, "fixture")
			if s != nil {
				_ = s.Close()
			}
			return err
		}},
		{"execute", nativeExecute, func(ctx context.Context, c *Conn) error {
			_, err := c.ExecContext(ctx, "fixture", nil)
			return err
		}},
		{"query", nativeBind, func(ctx context.Context, c *Conn) error {
			r, err := c.QueryContext(ctx, "fixture", nil)
			if r != nil {
				_ = r.Close()
			}
			return err
		}},
		{"begin", nativeAutocommit, func(ctx context.Context, c *Conn) error {
			tx, err := c.BeginTx(ctx, driver.TxOptions{})
			if tx != nil {
				_ = tx.Rollback()
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := openNativeFixture(t)
			c := f.connection(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.mode(tc.op, 1)
			f.mode(nativeCancel, 1)
			done := make(chan error, 1)
			go func() { done <- tc.run(ctx, c) }()
			f.waitActive(t, tc.op)
			cancel()
			returned := false
			select {
			case err := <-done:
				returned = true
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancel result: %v", err)
				}
			case <-time.After(100 * time.Millisecond):
				t.Error("context return waits for an unresponsive native call")
			}
			if f.calls(nativeFree) != 0 || f.calls(nativeDisconnect) != 0 {
				t.Error("live native resources were freed before completion")
			}
			f.mode(tc.op, 0)
			f.mode(nativeCancel, 0)
			if !returned {
				<-done
			}
		})
	}
}

func TestODBCNativeRowsContextDeadline(t *testing.T) {
	for _, op := range []int32{nativeFetch, nativeMore} {
		t.Run(map[int32]string{nativeFetch: "fetch", nativeMore: "result set"}[op], func(t *testing.T) {
			f := openNativeFixture(t)
			c := f.connection(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw, err := c.QueryContext(ctx, "fixture", nil)
			if err != nil {
				t.Fatal(err)
			}
			rows := raw.(*Rows)
			defer rows.Close()
			f.mode(op, 1)
			done := make(chan error, 1)
			values := []driver.Value{"unchanged"}
			go func() {
				if op == nativeFetch {
					done <- rows.Next(values)
				} else {
					done <- rows.NextResultSet()
				}
			}()
			f.waitActive(t, op)
			cancel()
			returned := false
			select {
			case err := <-done:
				returned = true
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancel result: %v", err)
				}
			case <-time.After(100 * time.Millisecond):
				t.Error("row iteration ignores its query context")
			}
			f.mode(op, 0)
			if !returned {
				<-done
			}
			if returned {
				<-c.nativeOwner().closeDone
				if values[0] != "unchanged" {
					t.Error("late native fetch wrote caller-owned values")
				}
			}
		})
	}
}

func TestODBCNativeQuarantineOwnsAdmission(t *testing.T) {
	f := openNativeFixture(t)
	d := &Driver{NativeLimit: 1, CloseTimeout: 20 * time.Millisecond}
	raw, err := d.Open("DRIVER={Owned Test Fixture}")
	if err != nil {
		t.Fatal(err)
	}
	c := raw.(*Conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.mode(nativeExecute, 1)
	f.mode(nativeCancel, 1)
	done := make(chan error, 1)
	go func() { _, err := c.ExecContext(ctx, "fixture", nil); done <- err }()
	f.waitActive(t, nativeExecute)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("cancel result: %v", err)
	}
	f.waitActive(t, nativeCancel)
	if _, err := d.Open("DRIVER={Owned Test Fixture}"); !errors.Is(err, ErrNativeLimit) {
		t.Errorf("quarantined connection released its slot: %v", err)
	}
	if err := d.Close(); !errors.Is(err, ErrCleanupPending) {
		t.Errorf("parent environment close: %v", err)
	}
	if c.IsValid() {
		t.Error("quarantined connection remains valid")
	}
	if err := c.Close(); !errors.Is(err, ErrCleanupPending) {
		t.Errorf("pending close: %v", err)
	}
	f.mode(nativeExecute, 0)
	deadline := time.Now().Add(time.Second)
	for f.active(nativeExecute) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.calls(nativeFree) != 0 || f.calls(nativeDisconnect) != 0 {
		t.Error("blocked SQLCancel lost native ownership")
	}
	f.mode(nativeCancel, 0)
	<-c.nativeOwner().closeDone
	if err := c.Close(); err != nil {
		t.Error(err)
	}
	raw, err = d.Open("DRIVER={Owned Test Fixture}")
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Error(err)
	}
	if err := d.Close(); err != nil {
		t.Error(err)
	}
}

func TestODBCNativeEnvironmentCloseBound(t *testing.T) {
	f := openNativeFixture(t)
	d := &Driver{CloseTimeout: 20 * time.Millisecond}
	raw, err := d.Open("DRIVER={Owned Test Fixture}")
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	f.mode(nativeFree, 1)
	done := make(chan error, 1)
	go func() { done <- d.Close() }()
	f.waitActive(t, nativeFree)
	if err := <-done; !errors.Is(err, ErrCleanupPending) {
		t.Errorf("close result: %v", err)
	}
	f.mode(nativeFree, 0)
	<-d.closeDone
	if err := d.Close(); err != nil {
		t.Error(err)
	}
}

type nativeConnector struct{ d *Driver }

func (n nativeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if d, ok := any(n.d).(driver.DriverContext); ok {
		connector, err := d.OpenConnector("DRIVER={Owned Test Fixture}")
		if err != nil {
			return nil, err
		}
		return connector.Connect(ctx)
	}
	return n.d.Open("DRIVER={Owned Test Fixture}")
}

func (n nativeConnector) Driver() driver.Driver { return n.d }

func TestODBCNativeDatabaseSQLCancellation(t *testing.T) {
	for _, kind := range []string{"exec", "direct rows", "prepared rows", "transaction rows"} {
		t.Run(kind, func(t *testing.T) {
			f := openNativeFixture(t)
			d := &Driver{NativeLimit: 1, CloseTimeout: 20 * time.Millisecond}
			db := sql.OpenDB(nativeConnector{d})
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var row *sql.Rows
			var stmt *sql.Stmt
			var tx *sql.Tx
			var err error
			op := int32(nativeFetch)
			if kind == "exec" {
				op = nativeExecute
			} else if kind == "prepared rows" {
				stmt, err = db.PrepareContext(ctx, "fixture")
				if err == nil {
					row, err = stmt.QueryContext(ctx)
				}
			} else if kind == "transaction rows" {
				tx, err = db.BeginTx(ctx, nil)
				if err == nil {
					row, err = tx.QueryContext(ctx, "fixture")
				}
			} else {
				row, err = db.QueryContext(ctx, "fixture")
			}
			if err != nil {
				t.Fatal(err)
			}
			f.mode(op, 1)
			done := make(chan error, 1)
			go func() {
				if kind == "exec" {
					_, err := db.ExecContext(ctx, "fixture")
					done <- err
				} else {
					if row.Next() {
						done <- errors.New("canceled fetch returned a row")
						return
					}
					done <- row.Err()
				}
			}()
			f.waitActive(t, op)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("database/sql cancellation: %v", err)
				}
			case <-time.After(200 * time.Millisecond):
				t.Error("database/sql retained the canceled caller")
			}
			if f.calls(nativeDisconnect) != 0 {
				t.Error("pool freed an active native connection")
			}
			if row != nil {
				_ = row.Close()
			}
			if stmt != nil {
				_ = stmt.Close()
			}
			if tx != nil {
				_ = tx.Rollback()
			}
			_ = db.Close()
			f.mode(op, 0)
			deadline := time.Now().Add(2 * time.Second)
			for d.Stats().ConnCount != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if d.Stats().ConnCount != 0 {
				t.Fatal("pool native cleanup did not finish")
			}
			for {
				err := d.Close()
				if !errors.Is(err, ErrCleanupPending) || time.Now().After(deadline) {
					if err != nil {
						t.Error(err)
					}
					break
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestODBCNativeCancelFailureRetainsPins(t *testing.T) {
	f := openNativeFixture(t)
	c := f.connection(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw, err := c.QueryContext(ctx, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := raw.(*Rows)
	col := r.rowsCursor.(*odbcRows).os.Cols[0].(*BindableColumn)
	f.mode(nativeFetch, 1)
	f.mode(nativeCancel, 2)
	done := make(chan error, 1)
	go func() { done <- r.Next(make([]driver.Value, 1)) }()
	f.waitActive(t, nativeFetch)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
	if col.pinner == nil {
		t.Error("failed cancellation unpinned a live fetch buffer")
	}
	f.mode(nativeFetch, 0)
	<-c.nativeOwner().closeDone
	if col.pinner != nil {
		t.Error("completed cleanup retained fetch pins")
	}
}

func TestODBCNativeCommitAfterCleanupFails(t *testing.T) {
	f := openNativeFixture(t)
	c := f.connection(t)
	tx, err := c.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Error("a rolled-back transaction reported a successful commit")
	}
}

func TestODBCNativeConnectContext(t *testing.T) {
	f := openNativeFixture(t)
	d := &Driver{NativeLimit: 1, CloseTimeout: 20 * time.Millisecond}
	db := sql.OpenDB(nativeConnector{d})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.mode(nativeConnect, 1)
	done := make(chan error, 1)
	go func() { _, err := db.ExecContext(ctx, "fixture"); done <- err }()
	f.waitActive(t, nativeConnect)
	cancel()
	returned := false
	select {
	case err := <-done:
		returned = true
		if !errors.Is(err, context.Canceled) {
			t.Errorf("connection cancellation: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("database/sql waits for canceled native connection startup")
	}
	if f.calls(nativeFree) != 0 {
		t.Error("connection startup lost handle ownership")
	}
	f.mode(nativeConnect, 0)
	if !returned {
		<-done
	}
	_ = db.Close()
	deadline := time.Now().Add(time.Second)
	for {
		err := d.Close()
		if !errors.Is(err, ErrCleanupPending) || time.Now().After(deadline) {
			if err != nil {
				t.Error(err)
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
}

func TestODBCNativeFreeFailureKeepsOwner(t *testing.T) {
	f := openNativeFixture(t)
	d := &Driver{NativeLimit: 1, CloseTimeout: 20 * time.Millisecond}
	raw, err := d.Open("DRIVER={Owned Test Fixture}")
	if err != nil {
		t.Fatal(err)
	}
	c := raw.(*Conn)
	statement, err := c.Prepare("fixture")
	if err != nil {
		t.Fatal(err)
	}
	s := statement.(*Stmt)
	rows, err := s.Query(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	os := s.os
	col := os.Cols[0].(*BindableColumn)
	f.mode(nativeFree, 2)
	if err := s.Close(); err == nil {
		t.Error("failed handle release succeeded")
	}
	<-c.nativeOwner().closeDone
	if col.pinner == nil || f.calls(nativeFree) != 1 {
		t.Error("unconfirmed release unpinned or retried a handle")
	}
	if _, err := d.Open("DRIVER={Owned Test Fixture}"); !errors.Is(err, ErrNativeLimit) {
		t.Errorf("unconfirmed release lost admission ownership: %v", err)
	}
	// The fixture guarantees this injected error kept the handle alive. Reclaim
	// only this known in-memory handle after the production owner has stopped.
	f.mode(nativeFree, 0)
	if err := releaseHandle(os.h, os.stats); err != nil {
		t.Fatal(err)
	}
	unpinColumns(os.Cols)
	releasePinnedStatement(os)
	delete(c.statements, os)
	if err := c.closeNative(); err != nil {
		t.Fatal(err)
	}
	d.releaseNativeSlot()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestODBCNativeConcurrentClose(t *testing.T) {
	f := openNativeFixture(t)
	c := f.connection(t)
	f.mode(nativeDisconnect, 1)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range cap(errs) {
		wg.Go(func() { errs <- c.Close() })
	}
	f.waitActive(t, nativeDisconnect)
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrCleanupPending) {
			t.Errorf("concurrent close: %v", err)
		}
	}
	if f.calls(nativeDisconnect) != 1 {
		t.Error("concurrent close repeated native cleanup")
	}
	f.mode(nativeDisconnect, 0)
	<-c.nativeOwner().closeDone
}

func TestODBCNativeCloseBound(t *testing.T) {
	for _, op := range []int32{nativeCursor, nativeFree, nativeDisconnect, nativeEndTran} {
		t.Run(map[int32]string{nativeCursor: "cursor", nativeFree: "statement", nativeDisconnect: "connection", nativeEndTran: "rollback"}[op], func(t *testing.T) {
			f := openNativeFixture(t)
			c := f.connection(t)
			var closeResource func() error
			if op == nativeCursor || op == nativeFree {
				s, err := c.Prepare("fixture")
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				closeResource = s.Close
				if op == nativeCursor {
					rows, err := s.Query(nil)
					if err != nil {
						t.Fatal(err)
					}
					closeResource = rows.Close
				}
			} else {
				if op == nativeEndTran {
					if _, err := c.Begin(); err != nil {
						t.Fatal(err)
					}
				}
				closeResource = c.Close
			}
			f.mode(op, 1)
			done := make(chan error, 1)
			go func() { done <- closeResource() }()
			f.waitActive(t, op)
			returned := false
			select {
			case err := <-done:
				returned = true
				if err == nil {
					t.Error("incomplete cleanup reported success")
				}
			case <-time.After(100 * time.Millisecond):
				t.Error("public close waits indefinitely for the native call")
			}
			f.mode(op, 0)
			if !returned {
				<-done
			}
		})
	}
}

func init() {
	auditOpenODBC = func(t *testing.T) *auditODBC {
		t.Helper()
		f := openNativeFixture(t)
		h, err := purego.Dlopen(os.Getenv("FLOWER_ODBC_TEST_LIBRARY"), purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = purego.Dlclose(h) })
		if _, err := purego.Dlsym(h, "AuditSet"); err != nil {
			t.Skip("audit extension of owned fixture was not selected")
		}
		a := &auditODBC{conn: f.connection(t), mode: f.mode, calls: f.calls, active: f.active, cols: f.columns}
		purego.RegisterLibFunc(&a.set, h, "AuditSet")
		purego.RegisterLibFunc(&a.get, h, "AuditGet")
		raw, err := a.conn.Prepare("owned audit fixture")
		if err != nil {
			t.Fatal(err)
		}
		a.stmt = raw.(*Stmt).os
		t.Cleanup(func() { _ = raw.Close() })
		return a
	}
}

// A failed native operation must propagate and leave reusable statements usable.
func TestAuditStatementNativeFailures(t *testing.T) {
	for _, op := range []int32{nativeAlloc, nativePrepare, nativeExecute, nativeNumCols, nativeMore} {
		name := map[int32]string{nativeAlloc: "allocate", nativePrepare: "prepare", nativeExecute: "execute", nativeNumCols: "column count", nativeMore: "more results"}[op]
		t.Run(name, func(t *testing.T) {
			f := openNativeFixture(t)
			c := f.connection(t)
			t.Cleanup(func() { f.mode(op, 0) })
			if op == nativeAlloc || op == nativePrepare {
				f.mode(op, 2)
				if _, err := c.Prepare("owned failure"); err == nil {
					t.Fatal("prepare failure was ignored")
				}
				if _, err := c.PrepareContext(context.Background(), "owned failure"); err == nil {
					t.Fatal("context prepare failure was ignored")
				}
				if _, err := c.ExecContext(context.Background(), "owned failure", nil); err == nil {
					t.Fatal("connection execution ignored prepare failure")
				}
				if _, err := c.QueryContext(context.Background(), "owned failure", nil); err == nil {
					t.Fatal("connection query ignored prepare failure")
				}
				return
			}
			raw, err := c.Prepare("owned failure")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			stmt := raw.(*Stmt)
			f.mode(op, 2)
			if op != nativeNumCols {
				if _, err := stmt.Exec(nil); err == nil {
					t.Error("native execution failure was ignored")
				}
				if _, err := stmt.ExecContext(context.Background(), nil); err == nil {
					t.Error("context execution failure was ignored")
				}
			}
			if op != nativeMore {
				if _, err := stmt.Query(nil); err == nil {
					t.Error("native query failure was ignored")
				}
				if _, err := stmt.QueryContext(context.Background(), nil); err == nil {
					t.Error("context query failure was ignored")
				}
			}
			f.mode(op, 0)
			result, err := stmt.Exec(nil)
			if err != nil {
				t.Fatal(err)
			}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				t.Fatalf("recovery result: %d, %v", count, err)
			}
		})
	}
}

func TestAuditStatementReusedWithRows(t *testing.T) {
	f := auditNative(t)
	raw, err := f.conn.Prepare("owned reusable statement")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	stmt := raw.(*Stmt)
	for _, withContext := range []bool{false, true} {
		rows, err := stmt.Query(nil)
		if err != nil {
			t.Fatal(err)
		}
		old := stmt.os
		if withContext {
			_, err = stmt.ExecContext(context.Background(), nil)
		} else {
			_, err = stmt.Exec(nil)
		}
		if err != nil || stmt.os == old {
			t.Fatalf("reuse retained an active row handle: %v", err)
		}
		values := make([]driver.Value, 1)
		if err := rows.Next(values); err != nil || values[0] != int32(42) {
			t.Fatalf("old cursor lost its row: %v, %v", values, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Exec(nil); err == nil {
		t.Error("closed statement executed")
	}
	if _, err := stmt.Query(nil); err == nil {
		t.Error("closed statement queried")
	}
}

func TestAuditBindingAndFetchFailures(t *testing.T) {
	f := auditNative(t)
	if err := f.stmt.Exec([]driver.Value{1}, f.conn); err == nil {
		t.Error("unexpected arguments accepted")
	}
	f.stmt.Parameters = make([]Parameter, 1)
	if err := f.stmt.Exec([]driver.Value{struct{}{}}, f.conn); err == nil {
		t.Error("unsupported argument accepted")
	}
	if err := f.stmt.Exec([]driver.Value{int64(7)}, f.conn); err != nil {
		t.Fatal(err)
	}
	f.set(15, int64(api.SQL_NO_DATA))
	if err := f.stmt.Exec([]driver.Value{int64(7)}, f.conn); err != nil {
		t.Fatalf("empty execution: %v", err)
	}
	f.set(15, 0)
	f.set(0, int64(api.SQL_LONGVARBINARY))
	f.cols(2)
	if err := f.stmt.BindColumns(f.conn); err != nil {
		t.Fatal(err)
	}
	for _, col := range f.stmt.Cols {
		if _, ok := col.(*NonBindableColumn); !ok {
			t.Fatal("unbounded result was bound")
		}
	}
	f.mode(nativeGetData, 2)
	if _, err := f.stmt.Cols[0].Value(f.stmt.h, 0); err == nil {
		t.Error("get-data failure was ignored")
	}
	bound := NewBindableColumn(&BaseColumn{}, api.SQL_C_LONG, 4)
	if _, err := bound.Value(f.stmt.h, 0); err == nil {
		t.Error("unbound fixed get-data failure was ignored")
	}
	f.mode(nativeGetData, 0)
	cursor := &odbcRows{os: f.stmt, c: f.conn}
	f.mode(nativeFetch, 2)
	if err := cursor.Next(make([]driver.Value, 2)); err == nil {
		t.Error("fetch failure was ignored")
	}
	f.mode(nativeFetch, 0)
	f.mode(nativeMore, 2)
	if err := cursor.Advance(); err == nil {
		t.Error("result-advance failure was ignored")
	}
	f.mode(nativeMore, 0)
	f.cols(-1)
	if err := f.stmt.BindColumns(f.conn); err == nil {
		t.Error("negative column count accepted")
	}
}

func TestAuditZeroColumnResults(t *testing.T) {
	for _, leading := range []bool{true, false} {
		t.Run(map[bool]string{true: "leading update count", false: "intermediate update count"}[leading], func(t *testing.T) {
			f := auditNative(t)
			if leading {
				f.set(11, 1)
				var columns func(int32)
				h, err := purego.Dlopen(os.Getenv("FLOWER_ODBC_TEST_LIBRARY"), purego.RTLD_NOW|purego.RTLD_LOCAL)
				if err != nil {
					t.Fatal(err)
				}
				defer purego.Dlclose(h)
				purego.RegisterLibFunc(&columns, h, "TestODBCColumns")
				columns(0)
			} else {
				f.set(11, 2)
			}
			rows, err := f.conn.QueryContext(context.Background(), "owned batch with an update count and a rowset", nil)
			if err != nil {
				t.Fatalf("row-producing batch failed before its rows: %v", err)
			}
			defer rows.Close()
			seen := 0
			for {
				if len(rows.Columns()) != 0 {
					values := make([]driver.Value, len(rows.Columns()))
					if err := rows.Next(values); err != nil || values[0] != int32(42) {
						t.Fatalf("rowset value=%v error=%v", values, err)
					}
					seen++
				}
				err := rows.(driver.RowsNextResultSet).NextResultSet()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("update count interrupted result traversal: %v", err)
				}
			}
			want := 2
			if leading {
				want = 1
			}
			if seen != want {
				t.Errorf("rowsets=%d; want %d", seen, want)
			}
		})
	}
	t.Run("final update count", func(t *testing.T) {
		f := auditNative(t)
		f.cols(0)
		if err := f.stmt.BindColumns(f.conn); !errors.Is(err, io.EOF) {
			t.Fatalf("final update count = %v; want EOF", err)
		}
	})
	t.Run("failed update advance", func(t *testing.T) {
		f := auditNative(t)
		f.cols(0)
		f.mode(nativeMore, 2)
		if err := f.stmt.BindColumns(f.conn); err == nil {
			t.Fatal("failed update-count advance succeeded")
		}
	})
}

func TestAuditCanceledColumnAdvance(t *testing.T) {
	if auditWrapSQL == nil {
		t.Skip("test-only native injection overlay not selected")
	}
	for _, cancelAt := range []string{"SQLNumResultCols", "SQLMoreResults"} {
		t.Run(cancelAt, func(t *testing.T) {
			f := auditNative(t)
			f.cols(0)
			f.set(11, 1)
			afterCancel := 0
			for _, name := range []string{"SQLNumResultCols", "SQLMoreResults"} {
				restore, err := auditWrapSQL(name, func() {
					if !f.conn.IsValid() {
						afterCancel++
					}
					if name == cancelAt {
						f.conn.invalidate()
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				defer restore()
			}
			if err := f.stmt.BindColumns(f.conn); !errors.Is(err, errNativeInvalid) {
				t.Errorf("canceled result traversal returned %v", err)
			}
			if afterCancel != 0 {
				t.Errorf("started %d native calls after cancellation", afterCancel)
			}
		})
	}
}
