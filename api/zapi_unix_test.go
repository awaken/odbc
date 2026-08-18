// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !static && ((darwin && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64)))

package api

import (
	"errors"
	"os"
	"strings"
	"testing"
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
