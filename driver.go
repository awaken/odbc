// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package odbc implements database/sql driver to access data via odbc interface.
package odbc

import (
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/alexbrainman/odbc/api"
)

const (
	// DriverPoolModeNone indicates that ODBC connection pooling is disabled.
	DriverPoolModeNone DriverPoolMode = 0
	// DriverPoolModeBasic indicates that ODBC connection pooling is enabled.
	DriverPoolModeBasic DriverPoolMode = 1
	// DriverPoolModeFull indicates that pooling and relaxed connection matching are enabled.
	DriverPoolModeFull DriverPoolMode = 2
)

// DriverPoolMode describes the connection pooling capabilities enabled by the ODBC driver manager.
type DriverPoolMode int

var drv Driver

// Driver implements database/sql/driver.Driver through an ODBC environment.
type Driver struct {
	stats    handleStats
	h        api.SQLHENV // environment handle
	initOnce sync.Once
	initErr  error
	poolMode atomic.Int32
}

// Stats returns a synchronized snapshot of the native handles currently owned
// by d.
func (d *Driver) Stats() Stats {
	return d.stats.snapshot()
}

// PoolMode returns the connection pooling mode enabled during driver initialization.
func (d *Driver) PoolMode() DriverPoolMode {
	return DriverPoolMode(d.poolMode.Load())
}

// IsPooling reports whether connection pooling is enabled.
func (d *Driver) IsPooling() bool {
	return d.PoolMode() != DriverPoolModeNone
}

// IsFullPooling reports whether connection pooling uses relaxed connection matching.
func (d *Driver) IsFullPooling() bool {
	return d.PoolMode() == DriverPoolModeFull
}

func (d *Driver) setPoolMode(mode DriverPoolMode) {
	d.poolMode.Store(int32(mode))
}

// Close releases the ODBC environment handle owned by d.
func (d *Driver) Close() error {
	// TODO(brainman): who will call (*Driver).Close (to dispose all opened handles)?
	h := d.h
	if h == api.SQLHENV(api.SQL_NULL_HENV) {
		return nil
	}
	if err := releaseHandle(h, &d.stats); err != nil {
		return err
	}
	d.h = api.SQLHENV(api.SQL_NULL_HENV)
	return nil
}

func (d *Driver) initialize() error {
	d.initOnce.Do(func() {
		if err := api.InitError(); err != nil {
			d.initErr = fmt.Errorf("initialize ODBC: %w", err)
			return
		}
		d.initErr = d.initDriver()
	})
	return d.initErr
}

func (d *Driver) initDriver() error {

	//TODO: find a way to make this attribute changeable at runtime
	//Enable connection pooling (this should be executed before allocating the environment handle)
	ret, callErr := safeSQLCall("SQLSetEnvUIntPtrAttr(SQL_ATTR_CONNECTION_POOLING)", func() api.SQLRETURN {
		return api.SQLSetEnvUIntPtrAttr(api.SQLHENV(api.SQL_NULL_HENV), api.SQL_ATTR_CONNECTION_POOLING, api.SQL_CP_ONE_PER_HENV, api.SQL_IS_UINTEGER)
	})
	if callErr != nil {
		ret = api.SQL_ERROR
	}
	if IsError(ret) {
		d.setPoolMode(DriverPoolModeNone)
	} else {
		d.setPoolMode(DriverPoolModeBasic)
	}

	//Allocate environment handle
	var out api.SQLHANDLE
	in := api.SQLHANDLE(api.SQL_NULL_HANDLE)
	ret, callErr = safeSQLCall("SQLAllocHandle", func() api.SQLRETURN {
		return api.SQLAllocHandle(api.SQL_HANDLE_ENV, in, &out)
	})
	if callErr != nil {
		return callErr
	}
	if IsError(ret) {
		return NewError("SQLAllocHandle", api.SQLHENV(in))
	}
	d.h = api.SQLHENV(out)
	err := d.stats.updateHandleCount(api.SQL_HANDLE_ENV, 1)
	if err != nil {
		d.Close()
		return err
	}

	// will use ODBC v3
	ret, callErr = safeSQLCall("SQLSetEnvUIntPtrAttr(SQL_ATTR_ODBC_VERSION, SQL_OV_ODBC3)", func() api.SQLRETURN {
		return api.SQLSetEnvUIntPtrAttr(d.h, api.SQL_ATTR_ODBC_VERSION, api.SQL_OV_ODBC3, 0)
	})
	if callErr != nil {
		defer d.Close()
		return callErr
	}
	if IsError(ret) {
		defer d.Close()
		return NewError("SQLSetEnvUIntPtrAttr(SQL_ATTR_ODBC_VERSION, SQL_OV_ODBC3)", d.h)
	}

	if d.IsPooling() {
		//Set relaxed connection pool matching
		ret, callErr = safeSQLCall("SQLSetEnvUIntPtrAttr(SQL_ATTR_CP_MATCH)", func() api.SQLRETURN {
			return api.SQLSetEnvUIntPtrAttr(d.h, api.SQL_ATTR_CP_MATCH, api.SQL_CP_RELAXED_MATCH, api.SQL_IS_UINTEGER)
		})
		if callErr == nil && !IsError(ret) {
			d.setPoolMode(DriverPoolModeFull)
		}
	}

	//TODO: it would be nice if we could call "drv.SetMaxIdleConns(0)" here but from the docs it looks like
	//the user must call this function after db.Open

	return nil
}

func init() {
	sql.Register("odbc", &drv)
}
