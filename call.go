// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"fmt"

	"github.com/alexbrainman/odbc/api"
)

func safeSQLCall(apiName string, call func() api.SQLRETURN) (ret api.SQLRETURN, err error) {
	ret = api.SQL_ERROR
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%s panicked while calling the native ODBC driver manager: %v", apiName, recovered)
		}
	}()
	ret = call()
	return ret, nil
}
