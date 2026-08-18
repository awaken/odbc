// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package api

import (
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
)

var windowsDriverManager struct {
	sync.Once
	err error
}

// InitError loads odbc32.dll and resolves all functions required by this
// package. It converts loader failures and Go panics into ordinary errors.
func InitError() error {
	windowsDriverManager.Do(func() {
		windowsDriverManager.err = initializeWindowsDriverManager()
	})
	return windowsDriverManager.err
}

func initializeWindowsDriverManager() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("ODBC driver manager initialization panicked: %v", recovered)
		}
	}()

	if err = mododbc32.Load(); err != nil {
		return fmt.Errorf("load ODBC driver manager odbc32.dll: %w", err)
	}
	required := []struct {
		name string
		proc *windows.LazyProc
	}{
		{"SQLAllocHandle", procSQLAllocHandle},
		{"SQLBindCol", procSQLBindCol},
		{"SQLBindParameter", procSQLBindParameter},
		{"SQLCancel", procSQLCancel},
		{"SQLCloseCursor", procSQLCloseCursor},
		{"SQLDescribeColW", procSQLDescribeColW},
		{"SQLDescribeParam", procSQLDescribeParam},
		{"SQLDisconnect", procSQLDisconnect},
		{"SQLDriverConnectW", procSQLDriverConnectW},
		{"SQLEndTran", procSQLEndTran},
		{"SQLExecute", procSQLExecute},
		{"SQLFetch", procSQLFetch},
		{"SQLFreeHandle", procSQLFreeHandle},
		{"SQLGetData", procSQLGetData},
		{"SQLGetDiagRecW", procSQLGetDiagRecW},
		{"SQLNumParams", procSQLNumParams},
		{"SQLMoreResults", procSQLMoreResults},
		{"SQLNumResultCols", procSQLNumResultCols},
		{"SQLPrepareW", procSQLPrepareW},
		{"SQLRowCount", procSQLRowCount},
		{"SQLSetEnvAttr", procSQLSetEnvAttr},
		{"SQLSetConnectAttrW", procSQLSetConnectAttrW},
	}
	for _, function := range required {
		if err = function.proc.Find(); err != nil {
			return fmt.Errorf("resolve ODBC function %s: %w", function.name, err)
		}
	}
	return nil
}
