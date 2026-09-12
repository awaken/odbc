// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows && (static || (!darwin && !linux) || (darwin && !amd64 && !arm64) || (linux && !386 && !amd64 && !arm && !arm64 && !loong64 && !ppc64le && !riscv64))

package api

import "testing"

func TestUnavailableBackendReturnsErrors(t *testing.T) {
	if err := InitError(); err == nil {
		t.Fatal("InitError unexpectedly succeeded")
	}
	var handle SQLHANDLE
	if ret := SQLAllocHandle(SQL_HANDLE_ENV, SQL_NULL_HANDLE, &handle); ret != SQL_ERROR {
		t.Fatalf("SQLAllocHandle returned %d; want %d", ret, SQL_ERROR)
	}
	if ret := SQLFreeStmt(SQL_NULL_HSTMT, SQL_UNBIND); ret != SQL_ERROR {
		t.Fatalf("SQLFreeStmt returned %d; want %d", ret, SQL_ERROR)
	}
}
