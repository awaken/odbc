// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows && (static || (!darwin && !linux) || (darwin && !amd64 && !arm64) || (linux && !386 && !amd64 && !arm && !arm64 && !loong64 && !ppc64le && !riscv64))

package odbc

import (
	"database/sql"
	"strings"
	"testing"
)

func TestUnavailableBuildRegistersDriver(t *testing.T) {
	database, err := sql.Open("odbc", "DSN=unavailable")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	err = database.Ping()
	if err == nil {
		t.Fatal("Ping unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "ODBC is unavailable") {
		t.Fatalf("Ping error is %q; want unavailable error", err)
	}
}
