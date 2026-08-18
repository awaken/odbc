// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"strings"
	"testing"

	"github.com/alexbrainman/odbc/api"
)

func TestSafeSQLCallReturnsValue(t *testing.T) {
	ret, err := safeSQLCall("SQLTest", func() api.SQLRETURN {
		return api.SQL_SUCCESS_WITH_INFO
	})
	if err != nil {
		t.Fatal(err)
	}
	if ret != api.SQL_SUCCESS_WITH_INFO {
		t.Fatalf("safeSQLCall returned %d; want %d", ret, api.SQL_SUCCESS_WITH_INFO)
	}
}

func TestSafeSQLCallRecoversPanic(t *testing.T) {
	ret, err := safeSQLCall("SQLTest", func() api.SQLRETURN {
		panic("native failure")
	})
	if ret != api.SQL_ERROR {
		t.Fatalf("safeSQLCall returned %d; want %d", ret, api.SQL_ERROR)
	}
	if err == nil || !strings.Contains(err.Error(), "native failure") {
		t.Fatalf("safeSQLCall error is %v; want recovered native failure", err)
	}
}
