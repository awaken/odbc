// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/alexbrainman/odbc/api"
)

type Conn struct {
	driver           *Driver
	closeTimeout     time.Duration
	ownerOnce        sync.Once
	owner            *connOwner
	statements       map[*ODBCStmt]struct{}
	connected        bool
	h                api.SQLHDBC
	stats            *handleStats
	tx               *Tx
	bad              atomic.Bool
	isMSAccessDriver bool
}

var _ driver.ConnPrepareContext = (*Conn)(nil)
var _ driver.ExecerContext = (*Conn)(nil)
var _ driver.QueryerContext = (*Conn)(nil)
var _ driver.Validator = (*Conn)(nil)
var _ driver.DriverContext = (*Driver)(nil)

var errODBCAttributes = errors.New("malformed ODBC connection string attributes")

// odbcAccessDriver reads only DRIVER, honoring braced values and escaped closing
// braces. Equal case-insensitive duplicates are accepted; conflicting values are
// rejected before connecting. Named DSNs alone do not identify an Access driver.
func odbcAccessDriver(dsn string) (bool, error) {
	var driverName string
	seen := false
	for {
		dsn = strings.TrimLeft(dsn, " \t\r\n;")
		if dsn == "" {
			break
		}
		key, rest, ok := strings.Cut(dsn, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.Contains(key, ";") {
			return false, errODBCAttributes
		}
		rest = strings.TrimLeft(rest, " \t\r\n")
		var value string
		if strings.HasPrefix(rest, "{") {
			var b strings.Builder
			i, closed := 1, false
			for i < len(rest) {
				if rest[i] == '}' {
					if i+1 < len(rest) && rest[i+1] == '}' {
						b.WriteByte('}')
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				b.WriteByte(rest[i])
				i++
			}
			if !closed {
				return false, errODBCAttributes
			}
			value = b.String()
			dsn = strings.TrimLeft(rest[i:], " \t\r\n")
			if dsn != "" && dsn[0] != ';' {
				return false, errODBCAttributes
			}
		} else {
			value, dsn, _ = strings.Cut(rest, ";")
		}
		if strings.EqualFold(key, "DRIVER") {
			value = strings.TrimSpace(value)
			if seen && !strings.EqualFold(driverName, value) {
				return false, errors.New("conflicting ODBC DRIVER attributes")
			}
			driverName, seen = value, true
		}
	}
	return strings.EqualFold(driverName, "Microsoft Access Driver (*.mdb)") ||
		strings.EqualFold(driverName, "Microsoft Access Driver (*.mdb, *.accdb)"), nil
}

// Open connects using dsn. DRIVER names alone select Access parameter handling;
// conflicting duplicate DRIVER attributes and malformed attribute syntax fail
// before a connection is opened. Attribute validation errors omit dsn values.
func (d *Driver) Open(dsn string) (driver.Conn, error) {
	connector, err := d.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return connector.Connect(context.Background())
}

// OpenConnector validates dsn without native I/O. Connect owns initialization
// and connection startup across cancellation of the caller's context.
func (d *Driver) OpenConnector(dsn string) (driver.Connector, error) {
	if strings.IndexByte(dsn, 0) >= 0 {
		return nil, errors.New("ODBC connection string contains a NUL byte")
	}
	isAccess, err := odbcAccessDriver(dsn)
	if err != nil {
		return nil, err
	}
	return &connConnector{driver: d, dsn: dsn, isAccess: isAccess}, nil
}

type connConnector struct {
	driver   *Driver
	dsn      string
	isAccess bool
}

func (n *connConnector) Driver() driver.Driver { return n.driver }

func (n *connConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := n.driver.acquireNativeSlot(); err != nil {
		return nil, err
	}
	c := &Conn{stats: &n.driver.stats, driver: n.driver, closeTimeout: n.driver.CloseTimeout, isMSAccessDriver: n.isAccess}
	_, err := runNative(ctx, c, func() (struct{}, error) {
		return struct{}{}, c.openNative(n.dsn)
	}, true)
	if err != nil {
		c.requestClose()
		return nil, err
	}
	return c, nil
}

func (c *Conn) openNative(dsn string) error {
	d := c.driver
	if err := d.initialize(); err != nil {
		return err
	}
	if c.bad.Load() {
		return errNativeInvalid
	}

	var out api.SQLHANDLE
	ret, callErr := safeSQLCall("SQLAllocHandle", func() api.SQLRETURN {
		return api.SQLAllocHandle(api.SQL_HANDLE_DBC, api.SQLHANDLE(d.h), &out)
	})
	if callErr != nil {
		return callErr
	}
	if IsError(ret) {
		return NewError("SQLAllocHandle", d.h)
	}
	c.h = api.SQLHDBC(out)
	d.stats.updateHandleCount(api.SQL_HANDLE_DBC, 1)
	if c.bad.Load() {
		return errNativeInvalid
	}

	b := api.StringToUTF16(dsn)
	ret, callErr = safeSQLCall("SQLDriverConnect", func() api.SQLRETURN {
		return api.SQLDriverConnect(c.h, 0,
			(*api.SQLWCHAR)(unsafe.Pointer(&b[0])), api.SQL_NTS,
			nil, 0, nil, api.SQL_DRIVER_NOPROMPT)
	})
	if callErr != nil {
		return callErr
	}
	if IsError(ret) {
		return NewError("SQLDriverConnect", c.h)
	}
	c.connected = true
	return nil
}

// Close bounds public waiting; incomplete native work keeps its handles owned.
func (c *Conn) Close() error {
	if c.h == api.SQLHDBC(api.SQL_NULL_HDBC) {
		return nil
	}
	o := c.requestClose()
	select {
	case <-o.closeDone:
		return o.closeErr
	default:
	}
	if o.abandoned.Load() {
		return ErrCleanupPending
	}
	timer := time.NewTimer(c.cleanupTimeout())
	defer timer.Stop()
	select {
	case <-o.closeDone:
		return o.closeErr
	case <-timer.C:
		o.abandoned.Store(true)
		return ErrCleanupPending
	}
}

func (c *Conn) closeNative() (err error) {
	if c.h == api.SQLHDBC(api.SQL_NULL_HDBC) {
		return nil
	}
	if !c.connected {
		return releaseHandle(c.h, c.stats)
	}
	if c.tx != nil {
		if err = c.endTx(false); err != nil {
			return err
		}
	}
	for statement := range c.statements {
		statement.mu.Lock()
		err = statement.releaseHandle()
		statement.usedByRows, statement.usedByStmt = false, false
		statement.mu.Unlock()
		if err != nil {
			return err
		}
	}
	h := c.h
	ret, callErr := safeSQLCall("SQLDisconnect", func() api.SQLRETURN {
		return api.SQLDisconnect(c.h)
	})
	if callErr != nil {
		return callErr
	}
	if IsError(ret) {
		return c.newError("SQLDisconnect", h)
	}
	return releaseHandle(h, c.stats)
}

// IsValid reports whether the connection can safely return to database/sql's
// idle pool.
func (c *Conn) IsValid() bool {
	return !c.bad.Load() && c.h != api.SQLHDBC(api.SQL_NULL_HDBC)
}

func (c *Conn) invalidate() {
	c.bad.Store(true)
}

func (c *Conn) newError(apiName string, handle interface{}) error {
	err := NewError(apiName, handle)
	var diagnosticError *Error
	if errors.As(err, &diagnosticError) && diagnosticError.connectionFailure() {
		c.invalidate()
	}
	return err
}

// ExecContext prepares and executes query, interrupting the native ODBC
// operation when ctx is cancelled.
func (c *Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	dargs = copyValues(dargs)
	return runOwned(ctx, c, func() (driver.Result, error) { return c.execContext(ctx, query, dargs) })
}

func (c *Conn) execContext(ctx context.Context, query string, dargs []driver.Value) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	statement, err := c.prepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	stmt := statement.(*Stmt)
	result, operationErr := stmt.execContext(ctx, dargs)
	closeErr := stmt.closeNative()
	if operationErr != nil {
		return nil, operationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return result, nil
}

// QueryContext prepares and executes query, interrupting the native ODBC
// operation when ctx is cancelled.
func (c *Conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	dargs = copyValues(dargs)
	return runOwned(ctx, c, func() (driver.Rows, error) { return c.queryContext(ctx, query, dargs) })
}

func (c *Conn) queryContext(ctx context.Context, query string, dargs []driver.Value) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	statement, err := c.prepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	stmt := statement.(*Stmt)
	rows, operationErr := stmt.queryContext(ctx, dargs)
	closeErr := stmt.closeNative()
	if operationErr != nil {
		return nil, operationErr
	}
	if closeErr != nil {
		if rows != nil {
			_ = rows.(*Rows).closeNative()
		}
		return nil, closeErr
	}
	return rows, nil
}

// namedValueToValue is a utility function that converts a driver.NamedValue into a driver.Value.
// Source:
// https://github.com/golang/go/blob/03ac39ce5e6af4c4bca58b54d5b160a154b7aa0e/src/database/sql/ctxutil.go#L137-L146
func namedValueToValue(named []driver.NamedValue) ([]driver.Value, error) {
	dargs := make([]driver.Value, len(named))
	for n, param := range named {
		if len(param.Name) > 0 {
			return nil, errors.New("sql: driver does not support the use of Named Parameters")
		}
		dargs[n] = param.Value
	}
	return dargs, nil
}
