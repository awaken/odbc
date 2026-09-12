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

Context-aware connection startup, prepare, execution, transaction startup and row iteration return
on cancellation. Each connection has one native owner. Cancellation invalidates
the connection; its owner waits for the operation and `SQLCancel` before freeing
handles or unpinning buffers. A late result cannot write the caller's row slice
or return the connection to the pool. Cancellation does not prove that database
effects were rolled back, and the driver does not request replay of started work.
`OpenConnector` validates connection syntax before I/O; `Connect` retains native
initialization and startup ownership even when their calls ignore cancellation.

Connection, statement, row and transaction cleanup waits are bounded by
`Driver.CloseTimeout` (default five seconds). `ErrCleanupPending` means resources
remain owned; repeated connection close reports the eventual cleanup result.
A canceled connection returns pending cleanup immediately. Handles with an
unconfirmed release retain their buffers and are not retried or reused.

`Driver.NativeLimit` bounds open plus quarantined connections (default 256 per
Driver). Exhaustion returns `ErrNativeLimit` before native allocation. Configure
both fields before the first Open and keep the SQL pool within that limit. To
customize the driver registered as `odbc`, register a separately configured
`Driver` under another name and select that name in `sql.Open`. Healthy worker
stop/restart uses ordinary cancellation and fresh pool connections. A native
call that never returns retains its capacity until process termination.

These lifetime guarantees apply to the `database/sql/driver` interfaces. Raw
`ODBCStmt`, parameter, column and `api` helpers are low-level driver components;
callers using them directly must serialize access and retain native resources.

Reused queries explicitly call `SQLFreeStmt(SQL_UNBIND)` before replacing column
buffers, including when column counts shrink. Failed unbinds retain the old
pins; partial binding failures retain the current generation until confirmed
handle release. An ODBC driver manager must export `SQLFreeStmt`.

To get started using ODBC, see the [wiki](../../wiki) pages.

Live SQL Server and MySQL tests require the `odbc_integration` build tag and
their vendor driver and database service. The repository's `test-mssql` and
`test-mysql` Make targets supply that opt-in; ordinary `go test ./...` runs do
not start those integrations.

SQL Server Make test targets run without a terminal and remove their test
container on exit, including failure. Use `make ODBC_TEST_KEEP=1 test-mssql`
(or the FreeTDS/race target) to retain it for inspection; remove it afterward.
The separate database container remains controlled by the start/stop targets.

Microsoft Access is unsupported. The fork has no Access-specific parameter
handling, integration tests or COM setup dependency.

The SQL Server test proxy reports the first unexpected connection error to its
owning test and joins all proxy work during cleanup. Dials have a five-second
limit. Pausing cancels and joins the current connections; restarting uses a new
context. The loopback-only `TestMSSQLProxy*` checks require no database or DSN:

```sh
go test -tags=static,odbc_integration -run '^TestMSSQLProxy' .
```

Readiness and database creation use one `ODBC_READY_TIMEOUT` budget (default
120 seconds) and an `ODBC_COMMAND_TIMEOUT` limit per Docker inspection, log or
creation command (default 10 seconds). Both accept 1–3600 seconds. GNU `timeout`
or `gtimeout` is required; a timed-out command gets one additional second before
forced termination. These limits begin after the container launch command.

A stopped container or database-creation error fails the attempt. An existing
database succeeds without retry. Diagnostics retain at most the last 4 KiB of
command output. `DB_NAME` is a plain ASCII SQL identifier: letters or underscore
first, then letters, digits or underscores, up to 128 characters for SQL Server
or 64 for MySQL. Quoted SQL fragments are not database names.
