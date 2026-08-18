// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
)

func TestQueryContextStopsBeforeNativeCall(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := new(Conn).QueryContext(cancelled, "select 1", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryContext cancelled error = %v; want %v", err, context.Canceled)
	}

	connection := &Conn{bad: true}
	if _, err := connection.QueryContext(context.Background(), "select 1", nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("QueryContext bad connection error = %v; want %v", err, driver.ErrBadConn)
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
	connection.bad = true
	if connection.IsValid() {
		t.Fatal("bad connection is valid")
	}
	connection.bad = false
	connection.h = 0
	if connection.IsValid() {
		t.Fatal("closed connection is valid")
	}
}
