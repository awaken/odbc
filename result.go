// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"errors"
	"math"
)

var (
	// ErrRowsAffectedUnknown means at least one result did not report a row count.
	ErrRowsAffectedUnknown = errors.New("ODBC affected row count is unknown")
	// ErrRowsAffectedOverflow means the combined row count exceeds int64.
	ErrRowsAffectedOverflow = errors.New("ODBC affected row count overflows int64")
)

// Result reports successful execution separately from row-count availability.
type Result struct {
	rowCount int64
	unknown  bool
	overflow bool
}

func (r *Result) LastInsertId() (int64, error) {
	// TODO(brainman): implement (*Result).LastInsertId
	return 0, errors.New("not implemented")
}

// RowsAffected returns the sum of reported counts, or zero and an error when
// any count is unknown or the sum overflows. Both errors may be present.
func (r *Result) RowsAffected() (int64, error) {
	if r.unknown && r.overflow {
		return 0, errors.Join(ErrRowsAffectedUnknown, ErrRowsAffectedOverflow)
	}
	if r.unknown {
		return 0, ErrRowsAffectedUnknown
	}
	if r.overflow {
		return 0, ErrRowsAffectedOverflow
	}
	return r.rowCount, nil
}

func (r *Result) addRowCount(count int64) {
	if count < 0 {
		r.unknown = true
		return
	}
	if r.overflow {
		return
	}
	if count > math.MaxInt64-r.rowCount {
		r.overflow = true
		return
	}
	r.rowCount += count
}
