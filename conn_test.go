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
