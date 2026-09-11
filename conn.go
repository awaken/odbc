// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/alexbrainman/odbc/api"
)

type Conn struct {
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
	if strings.IndexByte(dsn, 0) >= 0 {
		return nil, errors.New("ODBC connection string contains a NUL byte")
	}
	isAccess, err := odbcAccessDriver(dsn)
	if err != nil {
		return nil, err
	}
	if err := d.initialize(); err != nil {
		return nil, err
	}

	var out api.SQLHANDLE
	ret, callErr := safeSQLCall("SQLAllocHandle", func() api.SQLRETURN {
		return api.SQLAllocHandle(api.SQL_HANDLE_DBC, api.SQLHANDLE(d.h), &out)
	})
	if callErr != nil {
		return nil, callErr
	}
	if IsError(ret) {
		return nil, NewError("SQLAllocHandle", d.h)
	}
	h := api.SQLHDBC(out)
	d.stats.updateHandleCount(api.SQL_HANDLE_DBC, 1)

	b := api.StringToUTF16(dsn)
	ret, callErr = safeSQLCall("SQLDriverConnect", func() api.SQLRETURN {
		return api.SQLDriverConnect(h, 0,
			(*api.SQLWCHAR)(unsafe.Pointer(&b[0])), api.SQL_NTS,
			nil, 0, nil, api.SQL_DRIVER_NOPROMPT)
	})
	if callErr != nil {
		defer releaseHandle(h, &d.stats)
		return nil, callErr
	}
	if IsError(ret) {
		defer releaseHandle(h, &d.stats)
		return nil, NewError("SQLDriverConnect", h)
	}
	return &Conn{h: h, stats: &d.stats, isMSAccessDriver: isAccess}, nil
}

func (c *Conn) Close() (err error) {
	if c.h == api.SQLHDBC(api.SQL_NULL_HDBC) {
		return nil
	}
	if c.tx != nil {
		err = c.tx.Rollback()
	}
	h := c.h
	defer func() {
		c.h = api.SQLHDBC(api.SQL_NULL_HDBC)
		e := releaseHandle(h, c.stats)
		if err == nil {
			err = e
		}
	}()
	ret, callErr := safeSQLCall("SQLDisconnect", func() api.SQLRETURN {
		return api.SQLDisconnect(c.h)
	})
	if callErr != nil {
		return callErr
	}
	if IsError(ret) {
		return c.newError("SQLDisconnect", h)
	}
	return err
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
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	statement, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	stmt := statement.(*Stmt)
	result, operationErr := stmt.execContext(ctx, dargs)
	closeErr := stmt.Close()
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
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	statement, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	stmt := statement.(*Stmt)
	rows, operationErr := stmt.queryContext(ctx, dargs)
	closeErr := stmt.Close()
	if operationErr != nil {
		return nil, operationErr
	}
	if closeErr != nil {
		if rows != nil {
			_ = rows.Close()
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
