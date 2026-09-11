// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !static && ((darwin && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64)))

package odbc

import (
	"database/sql"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexbrainman/odbc/api"
)

func TestDriverCloseRetainsFailedEnvironmentHandle(t *testing.T) {
	dsn := os.Getenv("ODBC_SQLITE_DSN")
	if dsn == "" {
		t.Skip("set ODBC_SQLITE_DSN to exercise native environment cleanup")
	}

	driver := new(Driver)
	connection, err := driver.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
		_ = driver.Close()
	})

	if err := driver.Close(); err == nil {
		t.Error("closing an environment with a live connection unexpectedly succeeded")
	}
	if driver.h == api.SQLHENV(api.SQL_NULL_HENV) {
		t.Error("failed close discarded the still-owned environment handle")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := driver.Close(); err != nil {
		t.Fatal(err)
	}
	if got := driver.Stats().EnvCount; got != 0 {
		t.Fatalf("environment handles = %d; want 0", got)
	}
}

func TestUnavailableDriverManagerDoesNotStopProcess(t *testing.T) {
	const helperEnvironment = "ODBC_UNAVAILABLE_HELPER"
	if os.Getenv(helperEnvironment) == "1" {
		database, err := sql.Open("odbc", "DSN=unavailable")
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		err = database.Ping()
		if err == nil {
			t.Fatal("Ping unexpectedly succeeded")
		}
		if !strings.Contains(err.Error(), "missing-driver-manager") {
			t.Fatalf("Ping error is %q; want unavailable library path", err)
		}
		return
	}

	missingLibrary := filepath.Join(t.TempDir(), "missing-driver-manager")
	command := osexec.Command(os.Args[0], "-test.run=^TestUnavailableDriverManagerDoesNotStopProcess$")
	command.Env = append(os.Environ(),
		helperEnvironment+"=1",
		api.DriverManagerEnvironment+"="+missingLibrary,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, output)
	}
}
