package api

import (
	"syscall"
	"unsafe"
)

var procSQLGetInfoW = mododbc32.NewProc("SQLGetInfoW")

// SQLGetInfo calls the ODBC SQLGetInfoW catalog API.
func SQLGetInfo(connectionHandle SQLHDBC, infoType SQLUSMALLINT, value SQLPOINTER, length SQLSMALLINT, outLength *SQLSMALLINT) SQLRETURN {
	if procSQLGetInfoW.Find() != nil {
		return SQL_ERROR
	}
	r, _, _ := syscall.SyscallN(procSQLGetInfoW.Addr(), uintptr(connectionHandle), uintptr(infoType), uintptr(value), uintptr(length), uintptr(unsafe.Pointer(outLength)))
	return SQLRETURN(r)
}

var procSQLTablesW = mododbc32.NewProc("SQLTablesW")

// SQLTables calls the ODBC SQLTablesW catalog API.
func SQLTables(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT, types *SQLWCHAR, typesLength SQLSMALLINT) SQLRETURN {
	if procSQLTablesW.Find() != nil {
		return SQL_ERROR
	}
	r, _, _ := syscall.SyscallN(procSQLTablesW.Addr(), uintptr(statementHandle), uintptr(unsafe.Pointer(catalog)), uintptr(catalogLength), uintptr(unsafe.Pointer(schema)), uintptr(schemaLength), uintptr(unsafe.Pointer(table)), uintptr(tableLength), uintptr(unsafe.Pointer(types)), uintptr(typesLength))
	return SQLRETURN(r)
}

var procSQLColumnsW = mododbc32.NewProc("SQLColumnsW")

// SQLColumns calls the ODBC SQLColumnsW catalog API.
func SQLColumns(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT, column *SQLWCHAR, columnLength SQLSMALLINT) SQLRETURN {
	if procSQLColumnsW.Find() != nil {
		return SQL_ERROR
	}
	r, _, _ := syscall.SyscallN(procSQLColumnsW.Addr(), uintptr(statementHandle), uintptr(unsafe.Pointer(catalog)), uintptr(catalogLength), uintptr(unsafe.Pointer(schema)), uintptr(schemaLength), uintptr(unsafe.Pointer(table)), uintptr(tableLength), uintptr(unsafe.Pointer(column)), uintptr(columnLength))
	return SQLRETURN(r)
}

var procSQLPrimaryKeysW = mododbc32.NewProc("SQLPrimaryKeysW")

// SQLPrimaryKeys calls the ODBC SQLPrimaryKeysW catalog API.
func SQLPrimaryKeys(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT) SQLRETURN {
	if procSQLPrimaryKeysW.Find() != nil {
		return SQL_ERROR
	}
	r, _, _ := syscall.SyscallN(procSQLPrimaryKeysW.Addr(), uintptr(statementHandle), uintptr(unsafe.Pointer(catalog)), uintptr(catalogLength), uintptr(unsafe.Pointer(schema)), uintptr(schemaLength), uintptr(unsafe.Pointer(table)), uintptr(tableLength))
	return SQLRETURN(r)
}

var procSQLStatisticsW = mododbc32.NewProc("SQLStatisticsW")

// SQLStatistics calls the ODBC SQLStatisticsW catalog API.
func SQLStatistics(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT, unique SQLUSMALLINT, reserved SQLUSMALLINT) SQLRETURN {
	if procSQLStatisticsW.Find() != nil {
		return SQL_ERROR
	}
	r, _, _ := syscall.SyscallN(procSQLStatisticsW.Addr(), uintptr(statementHandle), uintptr(unsafe.Pointer(catalog)), uintptr(catalogLength), uintptr(unsafe.Pointer(schema)), uintptr(schemaLength), uintptr(unsafe.Pointer(table)), uintptr(tableLength), uintptr(unique), uintptr(reserved))
	return SQLRETURN(r)
}
