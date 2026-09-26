//go:build !windows && (static || (!darwin && !linux) || (darwin && !amd64 && !arm64) || (linux && !386 && !amd64 && !arm && !arm64 && !loong64 && !ppc64le && !riscv64))

package api

// SQLGetInfo is unavailable on this platform.
func SQLGetInfo(connectionHandle SQLHDBC, infoType SQLUSMALLINT, value SQLPOINTER, length SQLSMALLINT, outLength *SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

// SQLTables is unavailable on this platform.
func SQLTables(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT, types *SQLWCHAR, typesLength SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

// SQLColumns is unavailable on this platform.
func SQLColumns(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT, column *SQLWCHAR, columnLength SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

// SQLPrimaryKeys is unavailable on this platform.
func SQLPrimaryKeys(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT) SQLRETURN {
	return SQL_ERROR
}

// SQLStatistics is unavailable on this platform.
func SQLStatistics(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT, unique SQLUSMALLINT, reserved SQLUSMALLINT) SQLRETURN {
	return SQL_ERROR
}
