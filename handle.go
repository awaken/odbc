// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"fmt"

	"github.com/alexbrainman/odbc/api"
)

func ToHandleAndType(handle interface{}) (h api.SQLHANDLE, ht api.SQLSMALLINT, err error) {
	switch v := handle.(type) {
	case api.SQLHENV:
		if v == api.SQLHENV(api.SQL_NULL_HANDLE) {
			ht = 0
		} else {
			ht = api.SQL_HANDLE_ENV
		}
		h = api.SQLHANDLE(v)
	case api.SQLHDBC:
		ht = api.SQL_HANDLE_DBC
		h = api.SQLHANDLE(v)
	case api.SQLHSTMT:
		ht = api.SQL_HANDLE_STMT
		h = api.SQLHANDLE(v)
	default:
		err = fmt.Errorf("unexpected handle type %T", v)
	}
	return h, ht, err
}

func releaseHandle(handle interface{}, stats *handleStats) error {
	h, ht, err := ToHandleAndType(handle)
	if err != nil {
		return err
	}
	ret, callErr := safeSQLCall("SQLFreeHandle", func() api.SQLRETURN {
		return api.SQLFreeHandle(ht, h)
	})
	if callErr != nil {
		return callErr
	}
	if ret == api.SQL_INVALID_HANDLE {
		return fmt.Errorf("SQLFreeHandle(%d, %d) returns SQL_INVALID_HANDLE", ht, h)
	}
	if IsError(ret) {
		return NewError("SQLFreeHandle", handle)
	}
	if stats == nil {
		return nil
	}
	return stats.updateHandleCount(ht, -1)
}
