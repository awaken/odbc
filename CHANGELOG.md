# Changelog

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
