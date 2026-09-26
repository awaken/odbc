//go:build !static && ((darwin && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64)))

package api

// SQLGetInfo calls the ODBC SQLGetInfoW catalog API.
func SQLGetInfo(connectionHandle SQLHDBC, infoType SQLUSMALLINT, value SQLPOINTER, length SQLSMALLINT, outLength *SQLSMALLINT) SQLRETURN {
	if InitError() != nil || driverManager.functions.SQLGetInfo == nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLGetInfo(connectionHandle, infoType, value, length, outLength)
}

// SQLTables calls the ODBC SQLTablesW catalog API.
func SQLTables(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT, types *SQLWCHAR, typesLength SQLSMALLINT) SQLRETURN {
	if InitError() != nil || driverManager.functions.SQLTables == nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLTables(statementHandle, catalog, catalogLength, schema, schemaLength, table, tableLength, types, typesLength)
}

// SQLColumns calls the ODBC SQLColumnsW catalog API.
func SQLColumns(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT, column *SQLWCHAR, columnLength SQLSMALLINT) SQLRETURN {
	if InitError() != nil || driverManager.functions.SQLColumns == nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLColumns(statementHandle, catalog, catalogLength, schema, schemaLength, table, tableLength, column, columnLength)
}

// SQLPrimaryKeys calls the ODBC SQLPrimaryKeysW catalog API.
func SQLPrimaryKeys(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT) SQLRETURN {
	if InitError() != nil || driverManager.functions.SQLPrimaryKeys == nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLPrimaryKeys(statementHandle, catalog, catalogLength, schema, schemaLength, table, tableLength)
}

// SQLStatistics calls the ODBC SQLStatisticsW catalog API.
func SQLStatistics(statementHandle SQLHSTMT, catalog *SQLWCHAR, catalogLength SQLSMALLINT, schema *SQLWCHAR, schemaLength SQLSMALLINT, table *SQLWCHAR, tableLength SQLSMALLINT, unique SQLUSMALLINT, reserved SQLUSMALLINT) SQLRETURN {
	if InitError() != nil || driverManager.functions.SQLStatistics == nil {
		return SQL_ERROR
	}
	return driverManager.functions.SQLStatistics(statementHandle, catalog, catalogLength, schema, schemaLength, table, tableLength, unique, reserved)
}
