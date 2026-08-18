// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import "testing"

func TestConnectionFailureState(t *testing.T) {
	for _, state := range []string{"08001", "08003", "08004", "08007", "08S01"} {
		if !isConnectionFailureState(state) {
			t.Errorf("isConnectionFailureState(%q) = false", state)
		}
	}
	for _, state := range []string{"", "01000", "22001", "HY000"} {
		if isConnectionFailureState(state) {
			t.Errorf("isConnectionFailureState(%q) = true", state)
		}
	}
}
