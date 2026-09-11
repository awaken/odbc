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

func TestODBCDriverAttributeIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dsn     string
		access  bool
		invalid bool
	}{
		{name: "legacy Access", dsn: "DRIVER={Microsoft Access Driver (*.mdb)}", access: true},
		{name: "modern Access", dsn: "DRIVER={Microsoft Access Driver (*.mdb, *.accdb)};DBQ=fixture", access: true},
		{name: "unbraced Access", dsn: "driver=Microsoft Access Driver (*.mdb)", access: true},
		{name: "case and spacing", dsn: " driver = {microsoft access driver (*.mdb)} ; DBQ=fixture", access: true},
		{name: "other driver", dsn: "DRIVER={SQLite3};Database=fixture"},
		{name: "named DSN", dsn: "DSN=fixture"},
		{name: "empty", dsn: " ; ; "},
		{name: "unrelated unbraced value", dsn: "Description=DRIVER={Microsoft Access Driver (*.mdb)};DRIVER={SQLite3}"},
		{name: "unrelated braced value", dsn: "Description={note;DRIVER={Microsoft Access Driver (*.mdb)}}};DRIVER={SQLite3}"},
		{name: "driver-looking key", dsn: "NotDriver={Microsoft Access Driver (*.mdb)};DRIVER={SQLite3}"},
		{name: "driver name suffix", dsn: "DRIVER={Microsoft Access Driver lookalike}"},
		{name: "escaped driver brace", dsn: "DRIVER={Other}}Driver}"},
		{name: "equal duplicates", dsn: "DRIVER={Microsoft Access Driver (*.mdb)};driver=MICROSOFT ACCESS DRIVER (*.mdb)", access: true},
		{name: "conflicting duplicates", dsn: "DRIVER={SQLite3};DRIVER={Microsoft Access Driver (*.mdb)}", invalid: true},
		{name: "reversed conflicting duplicates", dsn: "DRIVER={Microsoft Access Driver (*.mdb)};DRIVER={SQLite3}", invalid: true},
		{name: "unclosed brace", dsn: "DRIVER={Microsoft Access Driver (*.mdb)", invalid: true},
		{name: "trailing characters", dsn: "DRIVER={SQLite3}suffix", invalid: true},
		{name: "missing equals", dsn: "DSN;DRIVER={SQLite3}", invalid: true},
		{name: "empty key", dsn: "={SQLite3}", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access, err := odbcAccessDriver(tc.dsn)
			if access != tc.access || (err != nil) != tc.invalid {
				t.Fatalf("Access=%t err=%v; want Access=%t invalid=%t", access, err, tc.access, tc.invalid)
			}
		})
	}
}

func TestODBCBadAttributesBeforeOpen(t *testing.T) {
	for _, dsn := range []string{"DRIVER={SQLite3}trailing", "DRIVER={SQLite3};DRIVER={Microsoft Access Driver (*.mdb)}"} {
		d := new(Driver)
		conn, err := d.Open(dsn)
		if conn != nil || err == nil || d.h != 0 || strings.Contains(err.Error(), "SQLite3") {
			t.Fatalf("invalid attributes reached native open: conn=%v err=%v handle=%v", conn, err, d.h)
		}
	}
}
