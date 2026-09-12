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
	"os"
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

const (
	nativeAlloc = iota + 1
	nativePrepare
	nativeExecute
	nativeFetch
	nativeMore
	nativeCancel
	nativeCursor
	nativeFree
	nativeDisconnect
	nativeEndTran
	nativeAutocommit
	nativeUnbind
	nativeDescribe
	nativeBind
	nativeNumCols
	nativeGetData
	nativeConnect
)

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
