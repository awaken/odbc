// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
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
