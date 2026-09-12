# Changelog

## Unreleased

## v0.1.3 - 2026-09-12

- Remove Microsoft Access support, its integration test and the `go-ole` COM
  dependency. String parameters use the standard ODBC binding rules.
- Retain validation of malformed and conflicting connection-string attributes.

## v0.1.2 - 2026-09-12

- Run SQL Server test containers without TTY flags and remove them on exit.
  Set `ODBC_TEST_KEEP=1` to retain a container for inspection.
- Release Access COM references and results before COM uninitialization on the
  same OS thread; report database-close and temporary-directory cleanup errors.
- Print the expected statement count in SQL Server leak diagnostics.

Addresses F1199, F1203 and F1204. These changes affect test tooling.

## v0.1.1 - 2026-09-12

- Return context cancellation from connection startup, preparation, execution
  and row iteration while retaining ownership of unfinished native work.
- Bound public cleanup waits and native connection capacity. Canceled
  connections cannot return to the pool or replay work already started.
- Keep row destinations and argument buffers isolated from late native results.
- Unbind previous result columns before rebinding, including smaller result
  sets. Retain pinned buffers until native release is confirmed.

Addresses F1183, F1191, F1192 and F1196. `Driver.NativeLimit` defaults to 256
connections and `Driver.CloseTimeout` to five seconds. Configure both before
first use. An indefinitely blocked native call retains its capacity until
process exit. Native driver managers must export `SQLFreeStmt`.

## v0.1.0 - 2026-09-11

Initial release of Flower's ODBC fork.

- Support CGO-disabled builds through native Windows ODBC and dynamically loaded
  unixODBC on supported macOS and Linux architectures.
- Defer native initialization until the first connection. Return descriptive
  errors for missing libraries or functions; retain driver registration in
  unsupported and `static` builds.
- Validate native lengths and metadata, and discard connections after native
  call failures.
- Preserve `NUMERIC` and `DECIMAL` precision by returning their text as `[]byte`.
- Add bounded database readiness helpers and improve SQL Server test-proxy
  cancellation, error reporting, and cleanup.
- Allow setup time in readiness test budgets to avoid clock-boundary failures.

Requires Go 1.26 or newer. Working connections require a native ODBC driver
manager and the database vendor's ODBC driver.
