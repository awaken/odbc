// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !static && ((darwin && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64)))

package api

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
)

type nativeFunctions struct {
	SQLAllocHandle    func(SQLSMALLINT, SQLHANDLE, *SQLHANDLE) SQLRETURN
	SQLBindCol        func(SQLHSTMT, SQLUSMALLINT, SQLSMALLINT, SQLPOINTER, SQLLEN, *SQLLEN) SQLRETURN
	SQLBindParameter  func(SQLHSTMT, SQLUSMALLINT, SQLSMALLINT, SQLSMALLINT, SQLSMALLINT, SQLULEN, SQLSMALLINT, SQLPOINTER, SQLLEN, *SQLLEN) SQLRETURN
	SQLCancel         func(SQLHSTMT) SQLRETURN
	SQLCloseCursor    func(SQLHSTMT) SQLRETURN
	SQLDescribeCol    func(SQLHSTMT, SQLUSMALLINT, *SQLWCHAR, SQLSMALLINT, *SQLSMALLINT, *SQLSMALLINT, *SQLULEN, *SQLSMALLINT, *SQLSMALLINT) SQLRETURN
	SQLDescribeParam  func(SQLHSTMT, SQLUSMALLINT, *SQLSMALLINT, *SQLULEN, *SQLSMALLINT, *SQLSMALLINT) SQLRETURN
	SQLDisconnect     func(SQLHDBC) SQLRETURN
	SQLDriverConnect  func(SQLHDBC, SQLHWND, *SQLWCHAR, SQLSMALLINT, *SQLWCHAR, SQLSMALLINT, *SQLSMALLINT, SQLUSMALLINT) SQLRETURN
	SQLEndTran        func(SQLSMALLINT, SQLHANDLE, SQLSMALLINT) SQLRETURN
	SQLExecute        func(SQLHSTMT) SQLRETURN
	SQLFetch          func(SQLHSTMT) SQLRETURN
	SQLFreeHandle     func(SQLSMALLINT, SQLHANDLE) SQLRETURN
	SQLFreeStmt       func(SQLHSTMT, SQLUSMALLINT) SQLRETURN
	SQLGetData        func(SQLHSTMT, SQLUSMALLINT, SQLSMALLINT, SQLPOINTER, SQLLEN, *SQLLEN) SQLRETURN
	SQLGetDiagRec     func(SQLSMALLINT, SQLHANDLE, SQLSMALLINT, *SQLWCHAR, *SQLINTEGER, *SQLWCHAR, SQLSMALLINT, *SQLSMALLINT) SQLRETURN
	SQLNumParams      func(SQLHSTMT, *SQLSMALLINT) SQLRETURN
	SQLMoreResults    func(SQLHSTMT) SQLRETURN
	SQLNumResultCols  func(SQLHSTMT, *SQLSMALLINT) SQLRETURN
	SQLPrepare        func(SQLHSTMT, *SQLWCHAR, SQLINTEGER) SQLRETURN
	SQLRowCount       func(SQLHSTMT, *SQLLEN) SQLRETURN
	SQLSetEnvAttr     func(SQLHENV, SQLINTEGER, uintptr, SQLINTEGER) SQLRETURN
	SQLSetConnectAttr func(SQLHDBC, SQLINTEGER, uintptr, SQLINTEGER) SQLRETURN
}

var driverManager struct {
	sync.Once
	functions nativeFunctions
	handle    uintptr
	path      string
	err       error
}

// InitError initializes the native ODBC driver manager. It returns a stable,
// descriptive error instead of panicking when the library cannot be loaded or
// does not provide every required function.
func InitError() error {
	driverManager.Do(func() {
		path, handle, err := openDriverManager(driverManagerCandidates(), purego.Dlopen)
		if err != nil {
			driverManager.err = err
			return
		}

		var functions nativeFunctions
		if err = bindNativeFunctions(handle, &functions, bindFunction); err != nil {
			if closeErr := safeCloseLibrary(handle, purego.Dlclose); closeErr != nil {
				err = fmt.Errorf("%w; additionally failed to close %q: %v", err, path, closeErr)
			}
			driverManager.err = fmt.Errorf("ODBC driver manager %q is unusable: %w", path, err)
			return
		}

		driverManager.path = path
		driverManager.handle = handle
		driverManager.functions = functions
	})
	return driverManager.err
}

func driverManagerCandidates() []string {
	if path := os.Getenv(DriverManagerEnvironment); path != "" {
		return []string{path}
	}

	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/opt/homebrew/lib/libodbc.dylib",
			"/usr/local/lib/libodbc.dylib",
			"libodbc.dylib",
		}
	default:
		return []string{"libodbc.so.2", "libodbc.so"}
	}
}

type openLibraryFunc func(string, int) (uintptr, error)

func openDriverManager(candidates []string, openLibrary openLibraryFunc) (string, uintptr, error) {
	var failures []string
	for _, candidate := range candidates {
		handle, err := safeOpenLibrary(candidate, purego.RTLD_NOW|purego.RTLD_GLOBAL, openLibrary)
		if err == nil && handle != 0 {
			return candidate, handle, nil
		}
		if err == nil {
			err = errors.New("native loader returned a null handle")
		}
		failures = append(failures, fmt.Sprintf("%q: %v", candidate, err))
	}
	return "", 0, fmt.Errorf("ODBC driver manager is unavailable (%s)", strings.Join(failures, "; "))
}

func safeOpenLibrary(path string, mode int, openLibrary openLibraryFunc) (handle uintptr, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			handle = 0
			err = fmt.Errorf("native loader panicked: %v", recovered)
		}
	}()
	return openLibrary(path, mode)
}

type closeLibraryFunc func(uintptr) error

func safeCloseLibrary(handle uintptr, closeLibrary closeLibraryFunc) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("native loader panicked: %v", recovered)
		}
	}()
	return closeLibrary(handle)
}

type bindFunctionFunc func(uintptr, string, any) error

func bindNativeFunctions(handle uintptr, functions *nativeFunctions, bind bindFunctionFunc) error {
	required := []struct {
		name   string
		target any
	}{
		{"SQLAllocHandle", &functions.SQLAllocHandle},
		{"SQLBindCol", &functions.SQLBindCol},
		{"SQLBindParameter", &functions.SQLBindParameter},
		{"SQLCancel", &functions.SQLCancel},
		{"SQLCloseCursor", &functions.SQLCloseCursor},
		{"SQLDescribeColW", &functions.SQLDescribeCol},
		{"SQLDescribeParam", &functions.SQLDescribeParam},
		{"SQLDisconnect", &functions.SQLDisconnect},
		{"SQLDriverConnectW", &functions.SQLDriverConnect},
		{"SQLEndTran", &functions.SQLEndTran},
		{"SQLExecute", &functions.SQLExecute},
		{"SQLFetch", &functions.SQLFetch},
		{"SQLFreeHandle", &functions.SQLFreeHandle},
		{"SQLFreeStmt", &functions.SQLFreeStmt},
		{"SQLGetData", &functions.SQLGetData},
		{"SQLGetDiagRecW", &functions.SQLGetDiagRec},
		{"SQLNumParams", &functions.SQLNumParams},
		{"SQLMoreResults", &functions.SQLMoreResults},
		{"SQLNumResultCols", &functions.SQLNumResultCols},
		{"SQLPrepareW", &functions.SQLPrepare},
		{"SQLRowCount", &functions.SQLRowCount},
		{"SQLSetEnvAttr", &functions.SQLSetEnvAttr},
		{"SQLSetConnectAttrW", &functions.SQLSetConnectAttr},
	}
	for _, function := range required {
		if err := bind(handle, function.name, function.target); err != nil {
			return fmt.Errorf("resolve %s: %w", function.name, err)
		}
	}
	return nil
}

func bindFunction(handle uintptr, name string, target any) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("register native function: %v", recovered)
		}
	}()
	address, err := purego.Dlsym(handle, name)
	if err != nil {
		return err
	}
	purego.RegisterFunc(target, address)
	return nil
}

func SQLAllocHandle(handleType SQLSMALLINT, inputHandle SQLHANDLE, outputHandle *SQLHANDLE) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLAllocHandle(handleType, inputHandle, outputHandle)
}

func SQLBindCol(statementHandle SQLHSTMT, columnNumber SQLUSMALLINT, targetType SQLSMALLINT, targetValuePtr SQLPOINTER, bufferLength SQLLEN, vallen *SQLLEN) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLBindCol(statementHandle, columnNumber, targetType, targetValuePtr, bufferLength, vallen)
}

func SQLBindParameter(statementHandle SQLHSTMT, parameterNumber SQLUSMALLINT, inputOutputType SQLSMALLINT, valueType SQLSMALLINT, parameterType SQLSMALLINT, columnSize SQLULEN, decimalDigits SQLSMALLINT, parameterValue SQLPOINTER, bufferLength SQLLEN, ind *SQLLEN) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLBindParameter(statementHandle, parameterNumber, inputOutputType, valueType, parameterType, columnSize, decimalDigits, parameterValue, bufferLength, ind)
}

func SQLCancel(statementHandle SQLHSTMT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLCancel(statementHandle)
}

func SQLCloseCursor(statementHandle SQLHSTMT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLCloseCursor(statementHandle)
}

func SQLDescribeCol(statementHandle SQLHSTMT, columnNumber SQLUSMALLINT, columnName *SQLWCHAR, bufferLength SQLSMALLINT, nameLengthPtr *SQLSMALLINT, dataTypePtr *SQLSMALLINT, columnSizePtr *SQLULEN, decimalDigitsPtr *SQLSMALLINT, nullablePtr *SQLSMALLINT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLDescribeCol(statementHandle, columnNumber, columnName, bufferLength, nameLengthPtr, dataTypePtr, columnSizePtr, decimalDigitsPtr, nullablePtr)
}

func SQLDescribeParam(statementHandle SQLHSTMT, parameterNumber SQLUSMALLINT, dataTypePtr *SQLSMALLINT, parameterSizePtr *SQLULEN, decimalDigitsPtr *SQLSMALLINT, nullablePtr *SQLSMALLINT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLDescribeParam(statementHandle, parameterNumber, dataTypePtr, parameterSizePtr, decimalDigitsPtr, nullablePtr)
}

func SQLDisconnect(connectionHandle SQLHDBC) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLDisconnect(connectionHandle)
}

func SQLDriverConnect(connectionHandle SQLHDBC, windowHandle SQLHWND, inConnectionString *SQLWCHAR, stringLength1 SQLSMALLINT, outConnectionString *SQLWCHAR, bufferLength SQLSMALLINT, stringLength2Ptr *SQLSMALLINT, driverCompletion SQLUSMALLINT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLDriverConnect(connectionHandle, windowHandle, inConnectionString, stringLength1, outConnectionString, bufferLength, stringLength2Ptr, driverCompletion)
}

func SQLEndTran(handleType SQLSMALLINT, handle SQLHANDLE, completionType SQLSMALLINT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLEndTran(handleType, handle, completionType)
}

func SQLExecute(statementHandle SQLHSTMT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLExecute(statementHandle)
}

func SQLFetch(statementHandle SQLHSTMT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLFetch(statementHandle)
}

func SQLFreeHandle(handleType SQLSMALLINT, handle SQLHANDLE) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLFreeHandle(handleType, handle)
}

func SQLFreeStmt(statementHandle SQLHSTMT, option SQLUSMALLINT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLFreeStmt(statementHandle, option)
}

func SQLGetData(statementHandle SQLHSTMT, colOrParamNum SQLUSMALLINT, targetType SQLSMALLINT, targetValuePtr SQLPOINTER, bufferLength SQLLEN, vallen *SQLLEN) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLGetData(statementHandle, colOrParamNum, targetType, targetValuePtr, bufferLength, vallen)
}

func SQLGetDiagRec(handleType SQLSMALLINT, handle SQLHANDLE, recNumber SQLSMALLINT, sqlState *SQLWCHAR, nativeErrorPtr *SQLINTEGER, messageText *SQLWCHAR, bufferLength SQLSMALLINT, textLengthPtr *SQLSMALLINT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLGetDiagRec(handleType, handle, recNumber, sqlState, nativeErrorPtr, messageText, bufferLength, textLengthPtr)
}

func SQLNumParams(statementHandle SQLHSTMT, parameterCountPtr *SQLSMALLINT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLNumParams(statementHandle, parameterCountPtr)
}

func SQLMoreResults(statementHandle SQLHSTMT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLMoreResults(statementHandle)
}

func SQLNumResultCols(statementHandle SQLHSTMT, columnCountPtr *SQLSMALLINT) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLNumResultCols(statementHandle, columnCountPtr)
}

func SQLPrepare(statementHandle SQLHSTMT, statementText *SQLWCHAR, textLength SQLINTEGER) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLPrepare(statementHandle, statementText, textLength)
}

func SQLRowCount(statementHandle SQLHSTMT, rowCountPtr *SQLLEN) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLRowCount(statementHandle, rowCountPtr)
}

func SQLSetEnvAttr(environmentHandle SQLHENV, attribute SQLINTEGER, valuePtr SQLPOINTER, stringLength SQLINTEGER) SQLRETURN {
	return sqlSetEnvUIntPtrAttr(environmentHandle, attribute, uintptr(valuePtr), stringLength)
}

func sqlSetEnvUIntPtrAttr(environmentHandle SQLHENV, attribute SQLINTEGER, valuePtr uintptr, stringLength SQLINTEGER) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLSetEnvAttr(environmentHandle, attribute, valuePtr, stringLength)
}

func SQLSetConnectAttr(connectionHandle SQLHDBC, attribute SQLINTEGER, valuePtr SQLPOINTER, stringLength SQLINTEGER) SQLRETURN {
	return sqlSetConnectUIntPtrAttr(connectionHandle, attribute, uintptr(valuePtr), stringLength)
}

func sqlSetConnectUIntPtrAttr(connectionHandle SQLHDBC, attribute SQLINTEGER, valuePtr uintptr, stringLength SQLINTEGER) SQLRETURN {
	if InitError() != nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLSetConnectAttr(connectionHandle, attribute, valuePtr, stringLength)
}
