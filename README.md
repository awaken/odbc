# Go ODBC driver

This package implements the `database/sql` driver interface for ODBC without
requiring CGO. It uses `odbc32.dll` on Windows and loads unixODBC dynamically
on supported macOS and Linux architectures.

Importing the package always registers the `odbc` database driver. Native ODBC
initialization is deferred until the first connection is opened. If the driver
manager is absent, cannot be loaded, or lacks a required function, opening the
connection returns a descriptive error; application startup continues.
Malformed lengths or metadata returned by native drivers are rejected as Go
errors instead of being used for unsafe buffer access. Connections marked bad
are also rejected from the `database/sql` idle pool.

ODBC `NUMERIC` and `DECIMAL` result columns are returned as `[]byte` containing
their character representation. This preserves the complete precision and
scale without converting through `float64`; scan into `[]byte` or `string` when
the exact value matters.

Set `ODBC_DRIVER_MANAGER_LIBRARY` to an explicit driver-manager library path.
Without it, the package checks standard unixODBC library names and Homebrew
locations. Builds using the `static` tag, and targets without a supported
dynamic-call implementation, retain driver registration but return an
unavailable error when ODBC is used.

A native ODBC driver manager and the database vendor's native ODBC driver are
still required for working connections. Go can recover loader errors, ODBC
error returns, and Go panics at the call boundary. An arbitrary native crash,
memory corruption, or indefinitely blocked native call cannot be isolated
inside the application process; use a separate worker process when that level
of fault isolation is required.

To get started using ODBC, see the [wiki](../../wiki) pages.

Live SQL Server and MySQL tests require the `odbc_integration` build tag and
their vendor driver and database service. The repository's `test-mssql` and
`test-mysql` Make targets supply that opt-in; ordinary `go test ./...` runs do
not start those integrations.
