// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import "testing"

func TestInt64FitsInt32IncludesBoundaries(t *testing.T) {
	for _, value := range []int64{-0x80000000, -1, 0, 1, 0x7fffffff} {
		if !int64FitsInt32(value) {
			t.Errorf("int64FitsInt32(%d) = false", value)
		}
	}
	for _, value := range []int64{-0x80000001, 0x80000000} {
		if int64FitsInt32(value) {
			t.Errorf("int64FitsInt32(%d) = true", value)
		}
	}
}
