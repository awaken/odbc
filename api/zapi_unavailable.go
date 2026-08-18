// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows && (static || (!darwin && !linux) || (darwin && !amd64 && !arm64) || (linux && !386 && !amd64 && !arm && !arm64 && !loong64 && !ppc64le && !riscv64))

package api

import (
	"fmt"
	"runtime"
)

var unavailableError = fmt.Errorf("ODBC is unavailable on %s/%s in this build", runtime.GOOS, runtime.GOARCH)

// InitError reports that this target cannot dynamically load an ODBC driver
// manager. Importing and registering the database driver remains safe.
func InitError() error {
	return unavailableError
}

func SQLAllocHandle(SQLSMALLINT, SQLHANDLE, *SQLHANDLE) SQLRETURN {
	return SQL_ERROR
}

func SQLBindCol(SQLHSTMT, SQLUSMALLINT, SQLSMALLINT, SQLPOINTER, SQLLEN, *SQLLEN) SQLRETURN {
	return SQL_ERROR
}

func SQLBindParameter(SQLHSTMT, SQLUSMALLINT, SQLSMALLINT, SQLSMALLINT, SQLSMALLINT, SQLULEN, SQLSMALLINT, SQLPOINTER, SQLLEN, *SQLLEN) SQLRETURN {
	return SQL_ERROR
}

func SQLCancel(SQLHSTMT) SQLRETURN {
	return SQL_ERROR
}

func SQLCloseCursor(SQLHSTMT) SQLRETURN {
	return SQL_ERROR
}

func SQLDescribeCol(SQLHSTMT, SQLUSMALLINT, *SQLWCHAR, SQLSMALLINT, *SQLSMALLINT, *SQLSMALLINT, *SQLULEN, *SQLSMALLINT, *SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

func SQLDescribeParam(SQLHSTMT, SQLUSMALLINT, *SQLSMALLINT, *SQLULEN, *SQLSMALLINT, *SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

func SQLDisconnect(SQLHDBC) SQLRETURN {
	return SQL_ERROR
}

func SQLDriverConnect(SQLHDBC, SQLHWND, *SQLWCHAR, SQLSMALLINT, *SQLWCHAR, SQLSMALLINT, *SQLSMALLINT, SQLUSMALLINT) SQLRETURN {
	return SQL_ERROR
}

func SQLEndTran(SQLSMALLINT, SQLHANDLE, SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

func SQLExecute(SQLHSTMT) SQLRETURN {
	return SQL_ERROR
}

func SQLFetch(SQLHSTMT) SQLRETURN {
	return SQL_ERROR
}

func SQLFreeHandle(SQLSMALLINT, SQLHANDLE) SQLRETURN {
	return SQL_ERROR
}

func SQLGetData(SQLHSTMT, SQLUSMALLINT, SQLSMALLINT, SQLPOINTER, SQLLEN, *SQLLEN) SQLRETURN {
	return SQL_ERROR
}

func SQLGetDiagRec(SQLSMALLINT, SQLHANDLE, SQLSMALLINT, *SQLWCHAR, *SQLINTEGER, *SQLWCHAR, SQLSMALLINT, *SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

func SQLNumParams(SQLHSTMT, *SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

func SQLMoreResults(SQLHSTMT) SQLRETURN {
	return SQL_ERROR
}

func SQLNumResultCols(SQLHSTMT, *SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

func SQLPrepare(SQLHSTMT, *SQLWCHAR, SQLINTEGER) SQLRETURN {
	return SQL_ERROR
}

func SQLRowCount(SQLHSTMT, *SQLLEN) SQLRETURN {
	return SQL_ERROR
}

func SQLSetEnvAttr(SQLHENV, SQLINTEGER, SQLPOINTER, SQLINTEGER) SQLRETURN {
	return SQL_ERROR
}

func sqlSetEnvUIntPtrAttr(SQLHENV, SQLINTEGER, uintptr, SQLINTEGER) SQLRETURN {
	return SQL_ERROR
}

func SQLSetConnectAttr(SQLHDBC, SQLINTEGER, SQLPOINTER, SQLINTEGER) SQLRETURN {
	return SQL_ERROR
}

func sqlSetConnectUIntPtrAttr(SQLHDBC, SQLINTEGER, uintptr, SQLINTEGER) SQLRETURN {
	return SQL_ERROR
}
