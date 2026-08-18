// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql/driver"
	"errors"
	"testing"
)

func TestStmtRejectsBadConnection(t *testing.T) {
	connection := &Conn{h: 1}
	connection.invalidate()
	statement := &Stmt{c: connection, os: new(ODBCStmt)}
	if _, err := statement.Exec(nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("Exec error = %v; want %v", err, driver.ErrBadConn)
	}
	if _, err := statement.Query(nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("Query error = %v; want %v", err, driver.ErrBadConn)
	}
}
