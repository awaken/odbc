// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/alexbrainman/odbc/api"
)

type Tx struct {
	c *Conn
}

var _ driver.ConnBeginTx = (*Conn)(nil)

var testBeginErr error // used during tests

func (c *Conn) setAutoCommitAttr(a uintptr) error {
	if testBeginErr != nil {
		return testBeginErr
	}
	ret, callErr := safeSQLCall("SQLSetConnectUIntPtrAttr", func() api.SQLRETURN {
		return api.SQLSetConnectUIntPtrAttr(c.h, api.SQL_ATTR_AUTOCOMMIT, a, api.SQL_IS_UINTEGER)
	})
	if callErr != nil {
		return callErr
	}
	if IsError(ret) {
		return c.newError("SQLSetConnectUIntPtrAttr", c.h)
	}
	return nil
}

func (c *Conn) Begin() (driver.Tx, error) {
	return c.begin()
}

// BeginTx starts a transaction after validating ctx and the requested options.
// ODBC transactions currently support only the default isolation level and
// read-write mode. database/sql rolls the transaction back if ctx is cancelled.
func (c *Conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Isolation != driver.IsolationLevel(0) {
		return nil, fmt.Errorf("odbc: transaction isolation level %d is not supported", opts.Isolation)
	}
	if opts.ReadOnly {
		return nil, errors.New("odbc: read-only transactions are not supported")
	}
	return c.begin()
}

func (c *Conn) begin() (driver.Tx, error) {
	if !c.IsValid() {
		return nil, driver.ErrBadConn
	}
	if c.tx != nil {
		return nil, errors.New("already in a transaction")
	}
	if err := c.setAutoCommitAttr(api.SQL_AUTOCOMMIT_OFF); err != nil {
		c.invalidate()
		return nil, err
	}
	c.tx = &Tx{c: c}
	return c.tx, nil
}

func (c *Conn) endTx(commit bool) error {
	if c.tx == nil {
		return errors.New("not in a transaction")
	}
	var howToEnd api.SQLSMALLINT
	if commit {
		howToEnd = api.SQL_COMMIT
	} else {
		howToEnd = api.SQL_ROLLBACK
	}
	ret, callErr := safeSQLCall("SQLEndTran", func() api.SQLRETURN {
		return api.SQLEndTran(api.SQL_HANDLE_DBC, api.SQLHANDLE(c.h), howToEnd)
	})
	if callErr != nil {
		c.invalidate()
		return callErr
	}
	if IsError(ret) {
		c.invalidate()
		return c.newError("SQLEndTran", c.h)
	}
	c.tx = nil
	err := c.setAutoCommitAttr(api.SQL_AUTOCOMMIT_ON)
	if err != nil {
		c.invalidate()
		return err
	}
	return nil
}

func (tx *Tx) Commit() error {
	return tx.c.endTx(true)
}

func (tx *Tx) Rollback() error {
	return tx.c.endTx(false)
}
