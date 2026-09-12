// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/alexbrainman/odbc/api"
)

// TODO(brainman): see if I could use SQLExecDirect anywhere

type ODBCStmt struct {
	conn       *Conn
	freeErr    error
	h          api.SQLHSTMT
	stats      *handleStats
	Parameters []Parameter
	Cols       []Column
	// locking/lifetime
	mu            sync.Mutex
	usedByStmt    bool
	usedByRows    bool
	noMoreResults bool
}

var pinnedStatements = struct {
	sync.Mutex
	statements map[*ODBCStmt]struct{}
}{statements: make(map[*ODBCStmt]struct{})}

func retainPinnedStatement(statement *ODBCStmt) {
	pinnedStatements.Lock()
	pinnedStatements.statements[statement] = struct{}{}
	pinnedStatements.Unlock()
}

func releasePinnedStatement(statement *ODBCStmt) {
	pinnedStatements.Lock()
	delete(pinnedStatements.statements, statement)
	pinnedStatements.Unlock()
}

func (s *ODBCStmt) isUsedByRows() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usedByRows
}

func (s *ODBCStmt) markUsedByRows() {
	s.mu.Lock()
	s.usedByRows = true
	s.noMoreResults = false
	s.mu.Unlock()
}

func (s *ODBCStmt) markNoMoreResults() {
	s.mu.Lock()
	s.noMoreResults = true
	s.mu.Unlock()
}

func (c *Conn) PrepareODBCStmt(query string) (*ODBCStmt, error) {
	s, queryText, err := c.allocateODBCStmt(query)
	if err != nil {
		return nil, err
	}
	if err = c.prepareAllocatedODBCStmt(s, queryText); err != nil {
		c.closeFailedODBCStmt(s)
		return nil, err
	}
	return s, nil
}

func (c *Conn) prepareODBCStmtContext(ctx context.Context, query string) (*ODBCStmt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, queryText, err := c.allocateODBCStmt(query)
	if err != nil {
		return nil, err
	}
	_, err = runContextOperation(ctx, func() (struct{}, error) {
		return struct{}{}, c.prepareAllocatedODBCStmt(s, queryText)
	}, func() error {
		return s.Cancel(c)
	}, nil)
	if err != nil {
		c.closeFailedODBCStmt(s)
		return nil, err
	}
	return s, nil
}

func (c *Conn) allocateODBCStmt(query string) (*ODBCStmt, []uint16, error) {
	if strings.IndexByte(query, 0) >= 0 {
		return nil, nil, errors.New("ODBC query contains a NUL byte")
	}
	var out api.SQLHANDLE
	ret, callErr := safeSQLCall("SQLAllocHandle", func() api.SQLRETURN {
		return api.SQLAllocHandle(api.SQL_HANDLE_STMT, api.SQLHANDLE(c.h), &out)
	})
	if callErr != nil {
		c.invalidate()
		return nil, nil, callErr
	}
	if IsError(ret) {
		return nil, nil, c.newError("SQLAllocHandle", c.h)
	}
	h := api.SQLHSTMT(out)
	err := c.stats.updateHandleCount(api.SQL_HANDLE_STMT, 1)
	if err != nil {
		defer releaseHandle(h, c.stats)
		return nil, nil, err
	}
	s := &ODBCStmt{h: h, stats: c.stats, usedByStmt: true, conn: c}
	if c.statements == nil {
		c.statements = make(map[*ODBCStmt]struct{})
	}
	c.statements[s] = struct{}{}
	return s, api.StringToUTF16(query), nil
}

func (c *Conn) prepareAllocatedODBCStmt(s *ODBCStmt, queryText []uint16) error {
	if !c.IsValid() {
		return errNativeInvalid
	}
	ret, callErr := safeSQLCall("SQLPrepare", func() api.SQLRETURN {
		return api.SQLPrepare(s.h, (*api.SQLWCHAR)(unsafe.Pointer(&queryText[0])), api.SQL_NTS)
	})
	if callErr != nil {
		c.invalidate()
		return callErr
	}
	if IsError(ret) {
		return c.newError("SQLPrepare", s.h)
	}
	ps, err := readParameters(s.h, c.IsValid)
	if err != nil {
		c.invalidate()
		return err
	}
	s.Parameters = ps
	return nil
}

func (c *Conn) closeFailedODBCStmt(s *ODBCStmt) {
	if err := s.closeByStmt(); err != nil {
		c.invalidate()
	}
}

func (s *ODBCStmt) closeByStmt() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usedByStmt {
		defer func() { s.usedByStmt = false }()
		if !s.usedByRows {
			return s.releaseHandle()
		}
	}
	return nil
}

func (s *ODBCStmt) closeByRows() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usedByRows {
		defer func() { s.usedByRows = false }()
		if s.usedByStmt {
			// SQLMoreResults closes the cursor when it reports SQL_NO_DATA.
			if s.noMoreResults {
				return nil
			}
			ret, callErr := safeSQLCall("SQLCloseCursor", func() api.SQLRETURN {
				return api.SQLCloseCursor(s.h)
			})
			if callErr != nil {
				return callErr
			}
			if IsError(ret) {
				return NewError("SQLCloseCursor", s.h)
			}
			return nil
		} else {
			return s.releaseHandle()
		}
	}
	return nil
}

func (s *ODBCStmt) releaseHandle() error {
	if s.freeErr != nil {
		return s.freeErr
	}
	h := s.h
	if h == api.SQLHSTMT(api.SQL_NULL_HSTMT) {
		return nil
	}
	err := releaseHandle(h, s.stats)
	if err != nil {
		// Keep every retained Go buffer pinned if the driver manager did not
		// confirm that the native statement handle was released.
		retainPinnedStatement(s)
		s.freeErr = err
		return err
	}
	s.h = api.SQLHSTMT(api.SQL_NULL_HSTMT)
	if s.conn != nil {
		delete(s.conn.statements, s)
	}
	for i := range s.Parameters {
		s.Parameters[i].unpin()
	}
	unpinColumns(s.Cols)
	s.Cols = nil
	releasePinnedStatement(s)
	return nil
}

func unpinColumns(columns []Column) {
	for _, column := range columns {
		if pinnable, ok := column.(interface{ unpin() }); ok {
			pinnable.unpin()
		}
	}
}

var testingIssue5 bool // used during tests

func (s *ODBCStmt) Exec(args []driver.Value, conn *Conn) error {
	if err := s.bind(args, conn); err != nil {
		return err
	}
	return s.execute(conn)
}

func (s *ODBCStmt) bind(args []driver.Value, conn *Conn) error {
	if len(args) != len(s.Parameters) {
		return fmt.Errorf("wrong number of arguments %d, %d expected", len(args), len(s.Parameters))
	}
	if len(args) > 0 {
		retainPinnedStatement(s)
	}
	for i, a := range args {
		if !conn.IsValid() {
			return errNativeInvalid
		}
		// this could be done in 2 steps:
		// 1) bind vars right after prepare;
		// 2) set their (vars) values here;
		// but rebinding parameters for every new parameter value
		// should be efficient enough for our purpose.
		if err := s.Parameters[i].BindValue(s.h, i, a, conn); err != nil {
			return err
		}
	}
	return nil
}

func (s *ODBCStmt) execute(conn *Conn) error {
	if !conn.IsValid() {
		return errNativeInvalid
	}
	if testingIssue5 {
		time.Sleep(10 * time.Microsecond)
	}
	ret, callErr := safeSQLCall("SQLExecute", func() api.SQLRETURN {
		return api.SQLExecute(s.h)
	})
	if callErr != nil {
		conn.invalidate()
		return callErr
	}
	if ret == api.SQL_NO_DATA {
		// success but no data to report
		return nil
	}
	if IsError(ret) {
		return conn.newError("SQLExecute", s.h)
	}
	return nil
}

func (s *ODBCStmt) BindColumns(conn *Conn) error {
	if !conn.IsValid() {
		return driver.ErrBadConn
	}
	// count columns
	var n api.SQLSMALLINT
	ret, callErr := safeSQLCall("SQLNumResultCols", func() api.SQLRETURN {
		return api.SQLNumResultCols(s.h, &n)
	})
	if callErr != nil {
		conn.invalidate()
		return callErr
	}
	if IsError(ret) {
		return conn.newError("SQLNumResultCols", s.h)
	}
	if n < 1 {
		return errors.New("Stmt did not create a result set")
	}
	// fetch column descriptions
	if len(s.Cols) > 0 {
		// Keep old buffers pinned until the driver confirms every binding is
		// removed, including trailing columns absent from the next result set.
		ret, callErr := safeSQLCall("SQLFreeStmt(SQL_UNBIND)", func() api.SQLRETURN {
			return api.SQLFreeStmt(s.h, api.SQL_UNBIND)
		})
		if callErr != nil {
			conn.invalidate()
			return callErr
		}
		if ret != api.SQL_SUCCESS && ret != api.SQL_SUCCESS_WITH_INFO {
			conn.invalidate()
			return conn.newError("SQLFreeStmt(SQL_UNBIND)", s.h)
		}
		unpinColumns(s.Cols)
	}
	s.Cols = make([]Column, n)
	binding := true
	for i := range s.Cols {
		if !conn.IsValid() {
			return errNativeInvalid
		}
		c, err := NewColumn(s.h, i)
		if err != nil {
			conn.invalidate()
			return err
		}
		s.Cols[i] = c
		// Once we found one non-bindable column, we will not bind the rest.
		// http://www.easysoft.com/developer/languages/c/odbc-tutorial-fetching-results.html
		// ... One common restriction is that SQLGetData may only be called on columns after the last bound column. ...
		if !binding {
			continue
		}
		if _, ok := s.Cols[i].(*BindableColumn); ok {
			retainPinnedStatement(s)
		}
		bound, err := s.Cols[i].Bind(s.h, i)
		if err != nil {
			conn.invalidate()
			return err
		}
		if !bound {
			binding = false
		}
	}
	return nil
}

func (s *ODBCStmt) Cancel(conn *Conn) error {
	ret, callErr := safeSQLCall("SQLCancel", func() api.SQLRETURN {
		return api.SQLCancel(s.h)
	})
	if callErr != nil {
		conn.invalidate()
		return callErr
	}
	if IsError(ret) {
		return conn.newError("SQLCancel", s.h)
	}

	return nil
}
