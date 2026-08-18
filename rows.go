// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql/driver"
	"io"

	"github.com/alexbrainman/odbc/api"
)

type Rows struct {
	os *ODBCStmt
	c  *Conn
}

func (r *Rows) Columns() []string {
	names := make([]string, len(r.os.Cols))
	for i := 0; i < len(names); i++ {
		names[i] = r.os.Cols[i].Name()
	}
	return names
}

func (r *Rows) Next(dest []driver.Value) error {
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
		v, err := r.os.Cols[i].Value(r.os.h, i)
		if err != nil {
			r.c.invalidate()
			return err
		}
		dest[i] = v
	}
	return nil
}

func (r *Rows) Close() error {
	err := r.os.closeByRows()
	if err != nil {
		r.c.invalidate()
	}
	return err
}

func (r *Rows) HasNextResultSet() bool {
	return true
}

func (r *Rows) NextResultSet() error {
	ret, callErr := safeSQLCall("SQLMoreResults", func() api.SQLRETURN {
		return api.SQLMoreResults(r.os.h)
	})
	if callErr != nil {
		r.c.invalidate()
		return callErr
	}
	if ret == api.SQL_NO_DATA {
		return io.EOF
	}
	if IsError(ret) {
		return r.c.newError("SQLMoreResults", r.os.h)
	}

	err := r.os.BindColumns(r.c)
	if err != nil {
		return err
	}
	return nil
}
