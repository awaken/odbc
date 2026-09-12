// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"io"
	"sync/atomic"

	"github.com/alexbrainman/odbc/api"
)

// rowsCursor isolates native fetch, result advancement and column binding.
type rowsCursor interface {
	Columns() []string
	Next([]driver.Value) error
	Advance() error
	BindColumns() error
	Close() error
}

// Rows streams result sets and releases the cursor when the final set ends.
type Rows struct {
	c          *Conn
	ctx        context.Context
	names      atomic.Pointer[[]string]
	pending    atomic.Bool
	rowsCursor rowsCursor
	nextReady  bool
	done       bool
	closed     bool
}

// Columns returns the current result set's column names.
func (r *Rows) Columns() []string {
	if r.c != nil {
		return append([]string(nil), (*r.names.Load())...)
	}
	return r.rowsCursor.Columns()
}

func (r *Rows) attachOwner(c *Conn, ctx context.Context) {
	r.c, r.ctx = c, ctx
	r.publishState()
}

func (r *Rows) publishState() {
	names := r.rowsCursor.Columns()
	r.names.Store(&names)
	r.pending.Store(!r.closed && !r.done)
}

func (r *Rows) cancel() error {
	return r.rowsCursor.(*odbcRows).os.Cancel(r.c)
}

// Next reads a row, probing the next set once when the current set ends.
func (r *Rows) Next(dest []driver.Value) error {
	if r.c == nil {
		return r.next(dest)
	}
	n := len(dest)
	values, err := runOwned(r.ctx, r.c, func() ([]driver.Value, error) {
		return runContextOperation(r.ctx, func() ([]driver.Value, error) {
			values := make([]driver.Value, n)
			err := r.next(values)
			r.publishState()
			return values, err
		}, r.cancel, nil)
	})
	if err == nil {
		copy(dest, values)
	}
	return err
}

func (r *Rows) next(dest []driver.Value) error {
	if r.closed || r.done || r.nextReady {
		return io.EOF
	}
	err := r.rowsCursor.Next(dest)
	if err == io.EOF {
		if err = r.advance(); err == nil {
			return io.EOF
		}
	}
	if err != nil {
		r.done = true
	}
	return err
}

// Close releases the cursor once, including after a cached result-set advance.
func (r *Rows) Close() error {
	if r.c == nil {
		return r.closeNative()
	}
	return r.c.closeResource(r.closeNative)
}

func (r *Rows) closeNative() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.pending.Store(false)
	return r.rowsCursor.Close()
}

type odbcRows struct {
	os *ODBCStmt
	c  *Conn
}

func (r *odbcRows) Columns() []string {
	names := make([]string, len(r.os.Cols))
	for i := 0; i < len(names); i++ {
		names[i] = r.os.Cols[i].Name()
	}
	return names
}

func (r *odbcRows) Next(dest []driver.Value) error {
	if !r.c.IsValid() {
		return errNativeInvalid
	}
	ret, callErr := safeSQLCall("SQLFetch", func() api.SQLRETURN {
		return api.SQLFetch(r.os.h)
	})
	if callErr != nil {
		r.c.invalidate()
		return callErr
	}
	if ret == api.SQL_NO_DATA {
		return io.EOF
	}
	if IsError(ret) {
		return r.c.newError("SQLFetch", r.os.h)
	}
	for i := range dest {
		if !r.c.IsValid() {
			return errNativeInvalid
		}
		v, err := r.os.Cols[i].Value(r.os.h, i)
		if err != nil {
			r.c.invalidate()
			return err
		}
		dest[i] = v
	}
	return nil
}

func (r *odbcRows) Close() error {
	err := r.os.closeByRows()
	if err != nil {
		r.c.invalidate()
	}
	return err
}

// HasNextResultSet reports the lookahead established by Next at fetch EOF.
// Before EOF the cursor remains active; probing must not discard unread rows.
func (r *Rows) HasNextResultSet() bool {
	if r.c != nil {
		return r.pending.Load() && r.c.IsValid()
	}
	return !r.closed && !r.done
}

// NextResultSet consumes the cached advance, or discards an unfinished set.
func (r *Rows) NextResultSet() error {
	if r.c == nil {
		return r.nextResultSet()
	}
	_, err := runOwned(r.ctx, r.c, func() (struct{}, error) {
		return runContextOperation(r.ctx, func() (struct{}, error) {
			err := r.nextResultSet()
			r.publishState()
			return struct{}{}, err
		}, r.cancel, nil)
	})
	return err
}

func (r *Rows) nextResultSet() error {
	if r.closed || r.done {
		return io.EOF
	}
	if !r.nextReady {
		if err := r.advance(); err != nil {
			return err
		}
	}
	if err := r.rowsCursor.BindColumns(); err != nil {
		r.done = true
		return err
	}
	r.nextReady = false
	return nil
}

// advance preserves the current column metadata until NextResultSet binds the new set.
func (r *Rows) advance() error {
	if err := r.rowsCursor.Advance(); err != nil {
		r.done = true
		return err
	}
	r.nextReady = true
	return nil
}

func (r *odbcRows) Advance() error {
	if !r.c.IsValid() {
		return errNativeInvalid
	}
	ret, callErr := safeSQLCall("SQLMoreResults", func() api.SQLRETURN {
		return api.SQLMoreResults(r.os.h)
	})
	if callErr != nil {
		r.c.invalidate()
		return callErr
	}
	if ret == api.SQL_NO_DATA {
		r.os.markNoMoreResults()
		return io.EOF
	}
	if IsError(ret) {
		return r.c.newError("SQLMoreResults", r.os.h)
	}

	return nil
}

func (r *odbcRows) BindColumns() error {
	return r.os.BindColumns(r.c)
}
