// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"math"
	"testing"

	"github.com/alexbrainman/odbc/api"
)

func TestStmtRejectsBadConnection(t *testing.T) {
	connection := &Conn{h: 1}
	connection.invalidate()
	statement := &Stmt{c: connection, os: new(ODBCStmt)}
	if _, err := statement.Exec(nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("Exec error = %v; want %v", err, driver.ErrBadConn)
	}
	if _, err := statement.Query(nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("Query error = %v; want %v", err, driver.ErrBadConn)
	}
	if _, err := statement.ExecContext(context.Background(), nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("ExecContext error = %v; want %v", err, driver.ErrBadConn)
	}
	if _, err := statement.QueryContext(context.Background(), nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("QueryContext error = %v; want %v", err, driver.ErrBadConn)
	}
}

func TestStmtContextMethodsStopBeforeNativeCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	statement := new(Stmt)
	if _, err := statement.ExecContext(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecContext error = %v; want %v", err, context.Canceled)
	}
	if _, err := statement.QueryContext(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryContext error = %v; want %v", err, context.Canceled)
	}
}

func TestStmtRowsAffectedValidity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		counts  []int64
		want    int64
		invalid bool
	}{
		{"zero", []int64{0}, 0, false},
		{"sum", []int64{1, 2, 3}, 6, false},
		{"maximum", []int64{0, math.MaxInt64}, math.MaxInt64, false},
		{"unknown", []int64{-1}, 0, true},
		{"mixed unknown", []int64{2, -1, 3}, 0, true},
		{"repeated unknown", []int64{-1, -1}, 0, true},
		{"overflow", []int64{math.MaxInt64, 1}, 0, true},
		{"wrapped positive", []int64{math.MaxInt64, math.MaxInt64, 3}, 0, true},
		{"unknown and overflow", []int64{-1, math.MaxInt64, 1}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, count := range tc.counts {
				if int64(api.SQLLEN(count)) != count {
					t.Skip("fixture count exceeds this platform's native SQLLEN")
				}
			}
			i := 0
			stmt := &Stmt{c: &Conn{h: 1}}
			result, err := stmt.execResults(&ODBCStmt{}, func(_ api.SQLHSTMT, count *api.SQLLEN) api.SQLRETURN {
				*count = api.SQLLEN(tc.counts[i])
				return api.SQL_SUCCESS
			}, func(api.SQLHSTMT) api.SQLRETURN {
				i++
				if i == len(tc.counts) {
					return api.SQL_NO_DATA
				}
				return api.SQL_SUCCESS
			})
			if err != nil || result == nil || i != len(tc.counts) || !stmt.c.IsValid() {
				t.Fatalf("execution should succeed and consume every result: result=%v err=%v consumed=%d", result, err, i)
			}
			for range 2 {
				count, err := result.RowsAffected()
				if (err != nil) != tc.invalid || count != tc.want {
					t.Errorf("RowsAffected = %d, %v; want %d with invalid=%t", count, err, tc.want, tc.invalid)
				}
			}
		})
	}
}

func TestStmtStopsResultsAfterInvalidation(t *testing.T) {
	s := &Stmt{c: &Conn{h: 1}}
	counts := 0
	_, err := s.execResults(&ODBCStmt{}, func(_ api.SQLHSTMT, n *api.SQLLEN) api.SQLRETURN {
		counts++
		*n = 1
		return api.SQL_SUCCESS
	}, func(api.SQLHSTMT) api.SQLRETURN {
		s.c.invalidate()
		if counts == 1 {
			return api.SQL_SUCCESS
		}
		return api.SQL_NO_DATA
	})
	if err == nil || counts != 1 {
		t.Fatalf("invalidated result traversal continued: count=%d err=%v", counts, err)
	}
}
