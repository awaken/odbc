// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"

	"github.com/alexbrainman/odbc/api"
)

type Stmt struct {
	c      *Conn
	query  string
	os     *ODBCStmt
	mu     sync.Mutex
	inputs int
}

var _ driver.StmtExecContext = (*Stmt)(nil)
var _ driver.StmtQueryContext = (*Stmt)(nil)

func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	return runOwned(context.Background(), c, func() (driver.Stmt, error) { return c.prepare(query) })
}

func (c *Conn) prepare(query string) (driver.Stmt, error) {
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	os, err := c.PrepareODBCStmt(query)
	if err != nil {
		return nil, err
	}
	return &Stmt{c: c, os: os, query: query, inputs: len(os.Parameters)}, nil
}

// PrepareContext prepares query and interrupts the native ODBC operation when
// ctx is cancelled.
func (c *Conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return runOwned(ctx, c, func() (driver.Stmt, error) { return c.prepareContext(ctx, query) })
}

func (c *Conn) prepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	os, err := c.prepareODBCStmtContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &Stmt{c: c, os: os, query: query, inputs: len(os.Parameters)}, nil
}

func (s *Stmt) NumInput() int {
	return s.inputs
}

func (s *Stmt) Close() error {
	return s.c.closeResource(s.closeNative)
}

func (s *Stmt) closeNative() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.os == nil {
		return errors.New("Stmt is already closed")
	}
	ret := s.os.closeByStmt()
	s.os = nil
	if ret != nil {
		s.c.invalidate()
	}
	return ret
}

func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
	args = copyValues(args)
	return runOwned(context.Background(), s.c, func() (driver.Result, error) { return s.execNative(args) })
}

func (s *Stmt) execNative(args []driver.Value) (driver.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepareForUse(s.c.PrepareODBCStmt); err != nil {
		return nil, err
	}
	return s.exec(s.os, args)
}

// ExecContext executes a prepared statement and interrupts its native ODBC
// operation when ctx is cancelled.
func (s *Stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	dargs = copyValues(dargs)
	return runOwned(ctx, s.c, func() (driver.Result, error) { return s.execContext(ctx, dargs) })
}

func (s *Stmt) execContext(ctx context.Context, args []driver.Value) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.prepareForUse(func(query string) (*ODBCStmt, error) {
		return s.c.prepareODBCStmtContext(ctx, query)
	}); err != nil {
		return nil, err
	}
	os := s.os
	if err := os.bind(args, s.c); err != nil {
		return nil, err
	}
	return runContextOperation(ctx, func() (driver.Result, error) {
		return s.execBound(os)
	}, func() error {
		return os.Cancel(s.c)
	}, nil)
}

func (s *Stmt) prepareForUse(prepare func(string) (*ODBCStmt, error)) error {
	if s.os == nil {
		return errors.New("Stmt is closed")
	}
	if !s.c.IsValid() {
		return driver.ErrBadConn
	}
	if !s.os.isUsedByRows() {
		return nil
	}
	if err := s.os.closeByStmt(); err != nil {
		s.c.invalidate()
		return err
	}
	s.os = nil
	os, err := prepare(s.query)
	if err != nil {
		return err
	}
	s.os = os
	return nil
}

func (s *Stmt) exec(os *ODBCStmt, args []driver.Value) (driver.Result, error) {
	if err := os.Exec(args, s.c); err != nil {
		return nil, err
	}
	return s.execBoundResults(os)
}

func (s *Stmt) execBound(os *ODBCStmt) (driver.Result, error) {
	if err := os.execute(s.c); err != nil {
		return nil, err
	}
	return s.execBoundResults(os)
}

func (s *Stmt) execBoundResults(os *ODBCStmt) (driver.Result, error) {
	return s.execResults(os, api.SQLRowCount, api.SQLMoreResults)
}

func (s *Stmt) execResults(os *ODBCStmt, rowCount func(api.SQLHSTMT, *api.SQLLEN) api.SQLRETURN, moreResults func(api.SQLHSTMT) api.SQLRETURN) (driver.Result, error) {
	result := new(Result)
	for {
		if !s.c.IsValid() {
			return nil, errNativeInvalid
		}
		var c api.SQLLEN
		ret, callErr := safeSQLCall("SQLRowCount", func() api.SQLRETURN {
			return rowCount(os.h, &c)
		})
		if callErr != nil {
			s.c.invalidate()
			return nil, callErr
		}
		if IsError(ret) {
			return nil, s.c.newError("SQLRowCount", os.h)
		}
		result.addRowCount(int64(c))
		ret, callErr = safeSQLCall("SQLMoreResults", func() api.SQLRETURN {
			return moreResults(os.h)
		})
		if callErr != nil {
			s.c.invalidate()
			return nil, callErr
		}
		if ret == api.SQL_NO_DATA {
			break
		}
		if IsError(ret) {
			return nil, s.c.newError("SQLMoreResults", os.h)
		}
	}
	return result, nil
}

func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	args = copyValues(args)
	return runOwned(context.Background(), s.c, func() (driver.Rows, error) { return s.queryNative(args) })
}

func (s *Stmt) queryNative(args []driver.Value) (driver.Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepareForUse(s.c.PrepareODBCStmt); err != nil {
		return nil, err
	}
	rows, err := s.queryStatement(s.os, args)
	if err == nil {
		rows.(*Rows).attachOwner(s.c, context.Background())
	}
	return rows, err
}

// QueryContext executes a prepared query and interrupts its native ODBC
// operation when ctx is cancelled.
func (s *Stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	dargs = copyValues(dargs)
	return runOwned(ctx, s.c, func() (driver.Rows, error) { return s.queryContext(ctx, dargs) })
}

func (s *Stmt) queryContext(ctx context.Context, args []driver.Value) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.prepareForUse(func(query string) (*ODBCStmt, error) {
		return s.c.prepareODBCStmtContext(ctx, query)
	}); err != nil {
		return nil, err
	}
	os := s.os
	if err := os.bind(args, s.c); err != nil {
		return nil, err
	}
	rows, err := runContextOperation(ctx, func() (driver.Rows, error) {
		return s.queryBound(os)
	}, func() error {
		return os.Cancel(s.c)
	}, func(rows driver.Rows) {
		_ = rows.(*Rows).closeNative()
	})
	if err == nil {
		rows.(*Rows).attachOwner(s.c, ctx)
	}
	return rows, err
}

func (s *Stmt) queryStatement(os *ODBCStmt, args []driver.Value) (driver.Rows, error) {
	if err := os.Exec(args, s.c); err != nil {
		return nil, err
	}
	return s.queryBoundResults(os)
}

func (s *Stmt) queryBound(os *ODBCStmt) (driver.Rows, error) {
	if err := os.execute(s.c); err != nil {
		return nil, err
	}
	return s.queryBoundResults(os)
}

func (s *Stmt) queryBoundResults(os *ODBCStmt) (driver.Rows, error) {
	err := os.BindColumns(s.c)
	if err != nil {
		return nil, err
	}
	os.markUsedByRows() // now both Stmt and Rows refer to it
	return &Rows{rowsCursor: &odbcRows{os: os, c: s.c}}, nil
}
