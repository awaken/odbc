// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/alexbrainman/odbc/api"
)

// TODO(brainman): see if I could use SQLExecDirect anywhere

type ODBCStmt struct {
	h           api.SQLHSTMT
	Parameters  []Parameter
	Cols        []Column
	retiredCols [][]Column
	// locking/lifetime
	mu         sync.Mutex
	usedByStmt bool
	usedByRows bool
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

func (c *Conn) PrepareODBCStmt(query string) (*ODBCStmt, error) {
	var out api.SQLHANDLE
	ret, callErr := safeSQLCall("SQLAllocHandle", func() api.SQLRETURN {
		return api.SQLAllocHandle(api.SQL_HANDLE_STMT, api.SQLHANDLE(c.h), &out)
	})
	if callErr != nil {
		c.invalidate()
		return nil, callErr
	}
	if IsError(ret) {
		return nil, c.newError("SQLAllocHandle", c.h)
	}
	h := api.SQLHSTMT(out)
	err := drv.Stats.updateHandleCount(api.SQL_HANDLE_STMT, 1)
	if err != nil {
		defer releaseHandle(h)
		return nil, err
	}

	b := api.StringToUTF16(query)
	ret, callErr = safeSQLCall("SQLPrepare", func() api.SQLRETURN {
		return api.SQLPrepare(h, (*api.SQLWCHAR)(unsafe.Pointer(&b[0])), api.SQL_NTS)
	})
	if callErr != nil {
		defer releaseHandle(h)
		c.invalidate()
		return nil, callErr
	}
	if IsError(ret) {
		defer releaseHandle(h)
		return nil, c.newError("SQLPrepare", h)
	}
	ps, err := ExtractParameters(h)
	if err != nil {
		defer releaseHandle(h)
		c.invalidate()
		return nil, err
	}
	return &ODBCStmt{
		h:          h,
		Parameters: ps,
		usedByStmt: true,
	}, nil
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
	h := s.h
	s.h = api.SQLHSTMT(api.SQL_NULL_HSTMT)
	err := releaseHandle(h)
	if err != nil {
		// Keep every retained Go buffer pinned if the driver manager did not
		// confirm that the native statement handle was released.
		retainPinnedStatement(s)
		return err
	}
	for i := range s.Parameters {
		s.Parameters[i].unpin()
	}
	unpinColumns(s.Cols)
	for _, columns := range s.retiredCols {
		unpinColumns(columns)
	}
	s.Cols = nil
	s.retiredCols = nil
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
	if len(args) != len(s.Parameters) {
		return fmt.Errorf("wrong number of arguments %d, %d expected", len(args), len(s.Parameters))
	}
	if len(args) > 0 {
		retainPinnedStatement(s)
	}
	for i, a := range args {
		// this could be done in 2 steps:
		// 1) bind vars right after prepare;
		// 2) set their (vars) values here;
		// but rebinding parameters for every new parameter value
		// should be efficient enough for our purpose.
		if err := s.Parameters[i].BindValue(s.h, i, a, conn); err != nil {
			return err
		}
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
		s.retiredCols = append(s.retiredCols, s.Cols)
	}
	s.Cols = make([]Column, n)
	binding := true
	for i := range s.Cols {
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
