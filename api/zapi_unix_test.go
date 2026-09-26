// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !static && ((darwin && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64)))

package api

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ebitengine/purego"
)

func TestOpenDriverManagerContinuesAfterFailure(t *testing.T) {
	var attempted []string
	path, handle, err := openDriverManager([]string{"missing", "available"}, func(path string, mode int) (uintptr, error) {
		attempted = append(attempted, path)
		if path == "missing" {
			return 0, errors.New("not found")
		}
		return 42, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "available" || handle != 42 {
		t.Fatalf("openDriverManager returned %q, %d; want available, 42", path, handle)
	}
	if strings.Join(attempted, ",") != "missing,available" {
		t.Fatalf("attempted %q; want missing,available", attempted)
	}
}

func TestAuditUnavailableDriverManager(t *testing.T) {
	base := os.Getenv("FLOWER_AUDIT_TMP")
	if base == "" {
		t.Skip("repository audit temporary directory not selected")
	}
	dir, err := os.MkdirTemp(base, "odbc-loader-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if driverManager.handle != 0 {
		t.Skip("another test initialized a native manager")
	}
	// Each test gets a fresh initialization attempt; no native call is active.
	reset := func() {
		if driverManager.handle != 0 {
			if err := purego.Dlclose(driverManager.handle); err != nil {
				t.Error(err)
			}
			driverManager.handle = 0
		}
		driverManager.Once = sync.Once{}
		driverManager.functions = nativeFunctions{}
		driverManager.path, driverManager.err = "", nil
	}
	reset()
	t.Cleanup(reset)
	t.Setenv(DriverManagerEnvironment, filepath.Join(dir, "absent-owned-driver.so"))
	err = InitError()
	if err == nil || !strings.Contains(err.Error(), "unavailable") || InitError() != err {
		t.Fatalf("missing manager initialization: %v", err)
	}
	// Every wrapper must stop before dereferencing its arguments or function.
	for _, tc := range []struct {
		name string
		call func() SQLRETURN
	}{
		{"allocate", func() SQLRETURN { return SQLAllocHandle(0, 0, nil) }},
		{"bind column", func() SQLRETURN { return SQLBindCol(0, 0, 0, nil, 0, nil) }},
		{"bind parameter", func() SQLRETURN { return SQLBindParameter(0, 0, 0, 0, 0, 0, 0, nil, 0, nil) }},
		{"cancel", func() SQLRETURN { return SQLCancel(0) }},
		{"close cursor", func() SQLRETURN { return SQLCloseCursor(0) }},
		{"describe column", func() SQLRETURN { return SQLDescribeCol(0, 0, nil, 0, nil, nil, nil, nil, nil) }},
		{"describe parameter", func() SQLRETURN { return SQLDescribeParam(0, 0, nil, nil, nil, nil) }},
		{"disconnect", func() SQLRETURN { return SQLDisconnect(0) }},
		{"connect", func() SQLRETURN { return SQLDriverConnect(0, 0, nil, 0, nil, 0, nil, 0) }},
		{"end transaction", func() SQLRETURN { return SQLEndTran(0, 0, 0) }},
		{"execute", func() SQLRETURN { return SQLExecute(0) }},
		{"fetch", func() SQLRETURN { return SQLFetch(0) }},
		{"free handle", func() SQLRETURN { return SQLFreeHandle(0, 0) }},
		{"free statement", func() SQLRETURN { return SQLFreeStmt(0, 0) }},
		{"get data", func() SQLRETURN { return SQLGetData(0, 0, 0, nil, 0, nil) }},
		{"diagnostics", func() SQLRETURN { return SQLGetDiagRec(0, 0, 0, nil, nil, nil, 0, nil) }},
		{"parameters", func() SQLRETURN { return SQLNumParams(0, nil) }},
		{"next results", func() SQLRETURN { return SQLMoreResults(0) }},
		{"columns", func() SQLRETURN { return SQLNumResultCols(0, nil) }},
		{"prepare", func() SQLRETURN { return SQLPrepare(0, nil, 0) }},
		{"row count", func() SQLRETURN { return SQLRowCount(0, nil) }},
		{"environment attribute", func() SQLRETURN { return SQLSetEnvUIntPtrAttr(0, 0, 0, 0) }},
		{"connection attribute", func() SQLRETURN { return SQLSetConnectUIntPtrAttr(0, 0, 0, 0) }},
		{"environment pointer attribute", func() SQLRETURN { return SQLSetEnvAttr(0, 0, nil, 0) }},
		{"connection pointer attribute", func() SQLRETURN { return SQLSetConnectAttr(0, 0, nil, 0) }},
		{"information", func() SQLRETURN { return SQLGetInfo(0, 0, nil, 0, nil) }},
		{"tables", func() SQLRETURN { return SQLTables(0, nil, 0, nil, 0, nil, 0, nil, 0) }},
		{"columns metadata", func() SQLRETURN { return SQLColumns(0, nil, 0, nil, 0, nil, 0, nil, 0) }},
		{"primary keys", func() SQLRETURN { return SQLPrimaryKeys(0, nil, 0, nil, 0, nil, 0) }},
		{"statistics", func() SQLRETURN { return SQLStatistics(0, nil, 0, nil, 0, nil, 0, 0, 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ret := tc.call(); ret != SQL_ERROR {
				t.Fatalf("missing manager returned %d; want SQL_ERROR", ret)
			}
		})
	}
	if path := os.Getenv("FLOWER_ODBC_INCOMPLETE_LIBRARY"); path != "" {
		reset()
		t.Setenv(DriverManagerEnvironment, path)
		if err := InitError(); err == nil || !strings.Contains(err.Error(), "resolve SQLExecute") || driverManager.handle != 0 {
			t.Fatalf("incomplete library was published: error=%v handle=%v", err, driverManager.handle)
		}
	}
}

func TestAuditDriverManagerCandidates(t *testing.T) {
	t.Setenv(DriverManagerEnvironment, "")
	got := driverManagerCandidates()
	want := "libodbc.so.2,libodbc.so"
	if runtime.GOOS == "darwin" {
		want = "/opt/homebrew/lib/libodbc.dylib,/usr/local/lib/libodbc.dylib,libodbc.dylib"
	}
	if strings.Join(got, ",") != want {
		t.Fatalf("default candidates: %v", got)
	}
	t.Setenv(DriverManagerEnvironment, "owned fixture")
	if got := driverManagerCandidates(); len(got) != 1 || got[0] != "owned fixture" {
		t.Fatalf("explicit candidate not exclusive: %v", got)
	}
}

func TestAuditNativeLoaderFailures(t *testing.T) {
	for _, stage := range []string{"success", "error", "panic"} {
		t.Run("close "+stage, func(t *testing.T) {
			want := errors.New("owned loader failure")
			err := safeCloseLibrary(42, func(h uintptr) error {
				if h != 42 {
					t.Errorf("wrong library handle: %v", h)
				}
				if stage == "panic" {
					panic(want)
				}
				if stage == "error" {
					return want
				}
				return nil
			})
			if (err == nil) != (stage == "success") || stage == "error" && !errors.Is(err, want) || stage == "panic" && !strings.Contains(err.Error(), want.Error()) {
				t.Fatalf("loader close outcome: %v", err)
			}
		})
	}
	path := os.Getenv("FLOWER_ODBC_TEST_LIBRARY")
	if path == "" || os.Getenv(DriverManagerEnvironment) != path {
		t.Skip("owned ODBC test library not selected")
	}
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		t.Fatal(err)
	}
	defer purego.Dlclose(h)
	var call func(SQLHSTMT) SQLRETURN
	if err := bindFunction(h, "AuditAbsentSymbol", &call); err == nil || call != nil {
		t.Fatalf("missing function accepted: %v", err)
	}
	var wrongTarget int
	if err := bindFunction(h, "SQLExecute", &wrongTarget); err == nil || !strings.Contains(err.Error(), "register native function") {
		t.Fatalf("invalid function target: %v", err)
	}
}

func TestOpenDriverManagerReportsEveryFailure(t *testing.T) {
	_, _, err := openDriverManager([]string{"first", "second"}, func(path string, mode int) (uintptr, error) {
		return 0, errors.New("not found")
	})
	if err == nil {
		t.Fatal("openDriverManager unexpectedly succeeded")
	}
	for _, path := range []string{"first", "second"} {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error %q does not mention %q", err, path)
		}
	}
}

func TestOpenDriverManagerRejectsNullHandle(t *testing.T) {
	_, _, err := openDriverManager([]string{"broken"}, func(path string, mode int) (uintptr, error) {
		return 0, nil
	})
	if err == nil || !strings.Contains(err.Error(), "null handle") {
		t.Fatalf("openDriverManager error is %v; want null handle failure", err)
	}
}

func TestOpenDriverManagerRecoversLoaderPanic(t *testing.T) {
	_, _, err := openDriverManager([]string{"broken"}, func(path string, mode int) (uintptr, error) {
		panic("loader failure")
	})
	if err == nil || !strings.Contains(err.Error(), "loader failure") {
		t.Fatalf("openDriverManager error is %v; want recovered loader failure", err)
	}
}

func TestBindNativeFunctionsStopsAtMissingSymbol(t *testing.T) {
	var names []string
	err := bindNativeFunctions(42, &nativeFunctions{}, func(handle uintptr, name string, target any) error {
		names = append(names, name)
		if name == "SQLExecute" {
			return errors.New("missing symbol")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "SQLExecute") {
		t.Fatalf("bindNativeFunctions error is %v; want SQLExecute failure", err)
	}
	if names[len(names)-1] != "SQLExecute" {
		t.Fatalf("last attempted symbol is %q; want SQLExecute", names[len(names)-1])
	}
}

func TestNativeDriverManagerEnvironmentLifecycle(t *testing.T) {
	if os.Getenv("ODBC_NATIVE_TEST") != "1" {
		t.Skip("set ODBC_NATIVE_TEST=1 to exercise the installed ODBC driver manager")
	}
	if err := InitError(); err != nil {
		t.Fatal(err)
	}

	var environment SQLHANDLE
	ret := SQLAllocHandle(SQL_HANDLE_ENV, SQL_NULL_HANDLE, &environment)
	if ret != SQL_SUCCESS && ret != SQL_SUCCESS_WITH_INFO {
		t.Fatalf("SQLAllocHandle returned %d", ret)
	}
	defer SQLFreeHandle(SQL_HANDLE_ENV, environment)

	ret = SQLSetEnvUIntPtrAttr(SQLHENV(environment), SQL_ATTR_ODBC_VERSION, SQL_OV_ODBC3, 0)
	if ret != SQL_SUCCESS && ret != SQL_SUCCESS_WITH_INFO {
		t.Fatalf("SQLSetEnvAttr returned %d", ret)
	}
}
