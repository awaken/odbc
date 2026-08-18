// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql/driver"
	"errors"
	"sync"

	"github.com/alexbrainman/odbc/api"
)

type Stmt struct {
	c     *Conn
	query string
	os    *ODBCStmt
	mu    sync.Mutex
}

func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	os, err := c.PrepareODBCStmt(query)
	if err != nil {
		return nil, err
	}
	return &Stmt{c: c, os: os, query: query}, nil
}

func (s *Stmt) NumInput() int {
	if s.os == nil {
		return -1
	}
	return len(s.os.Parameters)
}

func (s *Stmt) Close() error {
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
	if s.os == nil {
		return nil, errors.New("Stmt is closed")
	}
	if !s.c.IsValid() {
		return nil, driver.ErrBadConn
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.os.usedByRows {
		if err := s.os.closeByStmt(); err != nil {
			s.c.invalidate()
			return nil, err
		}
		s.os = nil
		os, err := s.c.PrepareODBCStmt(s.query)
		if err != nil {
			return nil, err
		}
		s.os = os
	}
	err := s.os.Exec(args, s.c)
	if err != nil {
		return nil, err
	}
	var sumRowCount int64
	for {
		var c api.SQLLEN
		ret, callErr := safeSQLCall("SQLRowCount", func() api.SQLRETURN {
			return api.SQLRowCount(s.os.h, &c)
		})
		if callErr != nil {
			s.c.invalidate()
			return nil, callErr
		}
		if IsError(ret) {
			return nil, s.c.newError("SQLRowCount", s.os.h)
		}
		sumRowCount += int64(c)
		ret, callErr = safeSQLCall("SQLMoreResults", func() api.SQLRETURN {
			return api.SQLMoreResults(s.os.h)
		})
		if callErr != nil {
			s.c.invalidate()
			return nil, callErr
		}
		if ret == api.SQL_NO_DATA {
			break
		}
		if IsError(ret) {
			return nil, s.c.newError("SQLMoreResults", s.os.h)
		}
	}
	return &Result{rowCount: sumRowCount}, nil
}

func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.os == nil {
		return nil, errors.New("Stmt is closed")
	}
	if !s.c.IsValid() {
		return nil, driver.ErrBadConn
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.os.usedByRows {
		if err := s.os.closeByStmt(); err != nil {
			s.c.invalidate()
			return nil, err
		}
		s.os = nil
		os, err := s.c.PrepareODBCStmt(s.query)
		if err != nil {
			return nil, err
		}
		s.os = os
	}
	err := s.os.Exec(args, s.c)
	if err != nil {
		return nil, err
	}
	err = s.os.BindColumns(s.c)
	if err != nil {
		return nil, err
	}
	s.os.usedByRows = true // now both Stmt and Rows refer to it
	return &Rows{os: s.os, c: s.c}, nil
}
