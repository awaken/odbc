// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexbrainman/odbc/api"
)

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

func TestParameterRejectsTimestampYearOutsideODBCField(t *testing.T) {
	for _, year := range []int{-32769, 32768} {
		parameter := new(Parameter)
		err := parameter.BindValue(0, 0, time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC), new(Conn))
		if err == nil || !strings.Contains(err.Error(), "timestamp year") {
			t.Errorf("year %d error = %v; want timestamp year error", year, err)
		}
		if parameter.Data != nil || parameter.pinner != nil {
			t.Errorf("year %d retained an invalid binding", year)
		}
	}
}

func TestDescribeParameterFailurePolicy(t *testing.T) {
	diagnostic := func(states ...string) *Error {
		e := &Error{APIName: "SQLDescribeParam"}
		for _, state := range states {
			e.Diag = append(e.Diag, DiagRecord{State: state, Message: "fixture diagnostic"})
		}
		return e
	}
	for _, tc := range []struct {
		name     string
		err      error
		fallback bool
	}{
		{"unsupported function", diagnostic("IM001"), true},
		{"optional feature", diagnostic("HYC00"), true},
		{"legacy capability", diagnostic("S1C00"), true},
		{"all unsupported", diagnostic("IM001", "HYC00"), true},
		{"connection loss", diagnostic("08S01"), false},
		{"timeout", diagnostic("HYT00"), false},
		{"memory", diagnostic("HY001"), false},
		{"sequence", diagnostic("HY010"), false},
		{"parameter index", diagnostic("07009"), false},
		{"general failure", diagnostic("HY000"), false},
		{"mixed diagnostics", diagnostic("HYC00", "08S01"), false},
		{"reversed diagnostics", diagnostic("08S01", "HYC00"), false},
		{"no records", diagnostic(), false},
		{"wrong operation", &Error{APIName: "SQLGetDiagRec", Diag: []DiagRecord{{State: "HYC00"}}}, false},
		{"truncated diagnostics", fmt.Errorf("diagnostic limit: %w", diagnostic("HYC00")), false},
		{"joined failure", errors.Join(diagnostic("HYC00"), context.Canceled), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, diagnostics := 0, 0
			params, err := extractParameters(3, func(index int, p *Parameter) (api.SQLRETURN, error) {
				calls++
				p.SQLType, p.Size, p.Decimal = api.SQL_VARCHAR, 8, 0
				if index == 2 {
					// A failed native call may have already written part of its output.
					p.SQLType, p.Size, p.Decimal = api.SQL_WVARCHAR, 0, 7
					return api.SQL_ERROR, nil
				}
				if index == 3 {
					p.SQLType, p.Size = api.SQL_VARBINARY, 0
				}
				return api.SQL_SUCCESS, nil
			}, func() error { diagnostics++; return tc.err })
			if diagnostics != 1 {
				t.Errorf("diagnostic reads = %d; want 1", diagnostics)
			}
			if tc.fallback {
				if err != nil || len(params) != 3 || calls != 3 || !params[0].isDescribed || params[0].SQLType != api.SQL_VARCHAR || !reflect.DeepEqual(params[1], Parameter{}) || !params[2].isDescribed || params[2].SQLType != api.SQL_LONGVARBINARY {
					t.Fatalf("fallback parameters=%+v calls=%d err=%v", params, calls, err)
				}
			} else if params != nil || !errors.Is(err, tc.err) || calls != 2 {
				t.Fatalf("failed metadata accepted: parameters=%+v calls=%d err=%v", params, calls, err)
			}
		})
	}
}

func TestExtractParameterCallErrors(t *testing.T) {
	for _, direct := range []bool{false, true} {
		params, err := extractParameters(1, func(int, *Parameter) (api.SQLRETURN, error) {
			if direct {
				return 0, context.Canceled
			}
			return api.SQL_ERROR, nil
		}, func() error {
			if direct {
				t.Error("diagnostics requested after direct call failure")
			}
			return nil
		})
		if params != nil || err == nil || (direct && !errors.Is(err, context.Canceled)) {
			t.Fatalf("metadata call error: params=%v err=%v", params, err)
		}
	}
	params, err := extractParameters(0, func(int, *Parameter) (api.SQLRETURN, error) {
		t.Error("description requested for no parameters")
		return 0, nil
	}, nil)
	if params != nil || err != nil {
		t.Fatalf("no parameters: %v, %v", params, err)
	}
}

// Operation numbers match the owned native fixture.
const (
	nativeAlloc = iota + 1
	nativePrepare
	nativeExecute
	nativeFetch
	nativeMore
	nativeCancel
	nativeCursor
	nativeFree
	nativeDisconnect
	nativeEndTran
	nativeAutocommit
	nativeUnbind
	nativeDescribe
	nativeBind
	nativeNumCols
	nativeGetData
	nativeConnect
)

// auditODBC is supplied only by the Linux owned-fixture tests.
type auditODBC struct {
	conn   *Conn
	stmt   *ODBCStmt
	set    func(int32, int64)
	get    func(int32) int64
	mode   func(int32, int32)
	calls  func(int32) int32
	active func(int32) int32
	cols   func(int32)
}

func (f *auditODBC) waitActive(t *testing.T, op int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for f.active(op) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("operation %d did not start", op)
		}
		time.Sleep(time.Millisecond)
	}
}

var auditOpenODBC func(*testing.T) *auditODBC

// Supplied only by the test overlay; ordinary builds keep native calls intact.
var auditWrapSQL func(string, func()) (func(), error)

func auditCallPanic(t *testing.T, name string, nth int32) func() {
	t.Helper()
	if auditWrapSQL == nil {
		t.Skip("test-only native injection overlay not selected")
	}
	var calls atomic.Int32
	restore, err := auditWrapSQL(name, func() {
		if calls.Add(1) == nth {
			panic("owned audit native failure")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return restore
}

func auditNative(t *testing.T) *auditODBC {
	t.Helper()
	if auditOpenODBC == nil {
		t.Skip("owned audit native fixture is unavailable on this target")
	}
	return auditOpenODBC(t)
}

// auditWaitClose verifies that invalidation eventually releases native children.
func auditWaitClose(t *testing.T, c *Conn) {
	t.Helper()
	select {
	case <-c.nativeOwner().closeDone:
		if err := c.nativeOwner().closeErr; err != nil {
			t.Fatalf("native cleanup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native cleanup did not finish")
	}
	if s := c.driver.Stats(); s.ConnCount != 0 || s.StmtCount != 0 {
		t.Fatalf("native children retained after cleanup: %+v", s)
	}
}

func TestAuditParameterNullBytes(t *testing.T) {
	f := auditNative(t)
	f.stmt.Parameters = make([]Parameter, 1)
	p := &f.stmt.Parameters[0]
	for _, tc := range []struct {
		name   string
		value  any
		length int64
	}{
		{"nil", nil, int64(api.SQL_NULL_DATA)},
		{"typed nil bytes", []byte(nil), int64(api.SQL_NULL_DATA)},
		{"empty bytes", []byte{}, 0},
		{"binary bytes", []byte{0, 1, 0}, 3},
		{"empty text", "", 0},
		{"embedded NUL text", "a\x00b", 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := p.BindValue(f.stmt.h, 0, tc.value, f.conn); err != nil {
				t.Fatal(err)
			}
			if got := f.get(5); got != tc.length {
				t.Errorf("native length indicator=%d; want %d", got, tc.length)
			}
		})
	}
}

func TestAuditParameterTimestampScale(t *testing.T) {
	f := auditNative(t)
	f.stmt.Parameters = make([]Parameter, 1)
	p := &f.stmt.Parameters[0]
	for _, scale := range []int16{0, 3, 7, 9} {
		p.isDescribed, p.SQLType, p.Decimal = true, api.SQL_TYPE_TIMESTAMP, api.SQLSMALLINT(scale)
		value := time.Date(2026, 9, 26, 12, 30, 0, 0, time.UTC)
		if err := p.BindValue(f.stmt.h, 0, value, f.conn); err != nil {
			t.Fatal(err)
		}
		if got := f.get(6); got != int64(scale) {
			t.Errorf("described timestamp scale %d changed to %d", scale, got)
		}
	}
}

func TestAuditNegativeParameterCount(t *testing.T) {
	params, err := extractParameters(-1, func(int, *Parameter) (api.SQLRETURN, error) {
		t.Fatal("invalid metadata must not describe parameters")
		return api.SQL_ERROR, nil
	}, nil)
	if err == nil || params != nil {
		t.Errorf("invalid native parameter count was accepted: params=%v error=%v", params, err)
	}
}

func TestAuditParameterTypeBindings(t *testing.T) {
	f := auditNative(t)
	f.stmt.Parameters = make([]Parameter, 1)
	p := &f.stmt.Parameters[0]
	for _, tc := range []struct {
		value any
		ctype api.SQLSMALLINT
	}{
		{int64(42), api.SQL_C_LONG},
		{int64(1) << 40, api.SQL_C_SBIGINT},
		{false, api.SQL_C_BIT}, {true, api.SQL_C_BIT},
		{float64(1.25), api.SQL_C_DOUBLE},
		{time.Date(2026, 9, 26, 10, 20, 30, 123000000, time.UTC), api.SQL_C_TYPE_TIMESTAMP},
		{"ab", api.SQL_C_WCHAR}, {strings.Repeat("x", 4000), api.SQL_C_WCHAR},
		{make([]byte, 8000), api.SQL_C_BINARY},
	} {
		if err := p.BindValue(f.stmt.h, 0, tc.value, f.conn); err != nil {
			t.Fatal(err)
		}
		if got := f.get(8); got != int64(tc.ctype) {
			t.Errorf("%T bound as %d; want %d", tc.value, got, tc.ctype)
		}
	}
	p.isDescribed, p.SQLType = true, api.SQL_VARBINARY
	if err := p.BindValue(f.stmt.h, 0, []byte{1}, f.conn); err != nil || f.get(9) != int64(api.SQL_VARBINARY) {
		t.Fatalf("described binary: %v", err)
	}
	p.SQLType = api.SQL_VARCHAR
	if err := p.BindValue(f.stmt.h, 0, "abc", f.conn); err != nil || f.get(9) != int64(api.SQL_VARCHAR) {
		t.Fatalf("described text: %v", err)
	}
	old := p.pinner
	f.set(10, 1)
	if err := p.BindValue(f.stmt.h, 0, "retry", f.conn); err == nil || len(p.retiredPinners) != 1 || p.retiredPinners[0] != old {
		t.Fatalf("failed binding lost old native ownership: %v", err)
	}
	f.set(10, 0)
	if err := p.BindValue(f.stmt.h, 0, "success", f.conn); err != nil || len(p.retiredPinners) != 0 {
		t.Fatalf("successful replacement retained old binding: %v", err)
	}
	if err := p.BindValue(f.stmt.h, 0, struct{}{}, f.conn); err == nil {
		t.Error("unsupported input accepted")
	}
	if ptr := p.StoreStrLen_or_IndPtr(5); *ptr != 5 {
		t.Error("indicator store failed")
	}
}

// Metadata failures must not publish a partly described parameter list.
func TestAuditParameterMetadataFailures(t *testing.T) {
	f := auditNative(t)
	f.set(2, 1)
	params, err := ExtractParameters(f.stmt.h)
	if err != nil || len(params) != 1 || !params[0].isDescribed {
		t.Fatalf("valid metadata: %v, %v", params, err)
	}
	f.set(13, int64(api.SQL_ERROR))
	if _, err := ExtractParameters(f.stmt.h); err == nil {
		t.Error("parameter-count failure was ignored")
	}
	f.set(13, 0)
	f.set(14, int64(api.SQL_ERROR))
	if _, err := ExtractParameters(f.stmt.h); err == nil {
		t.Error("parameter-description failure was ignored")
	}
	f.set(14, 0)
	checks := 0
	_, err = readParameters(f.stmt.h, func() bool { checks++; return checks == 1 })
	if !errors.Is(err, errNativeInvalid) {
		t.Fatalf("invalidation between metadata calls: %v", err)
	}
	for _, kind := range []api.SQLSMALLINT{api.SQL_VARCHAR, api.SQL_WVARCHAR} {
		params, err := extractParameters(1, func(_ int, p *Parameter) (api.SQLRETURN, error) {
			p.SQLType = kind
			return api.SQL_SUCCESS, nil
		}, nil)
		want := api.SQL_LONGVARCHAR
		if kind == api.SQL_WVARCHAR {
			want = api.SQL_WLONGVARCHAR
		}
		if err != nil || len(params) != 1 || params[0].SQLType != api.SQLSMALLINT(want) {
			t.Fatalf("unbounded text metadata: %v, %v", params, err)
		}
	}
	f.stmt.Parameters = make([]Parameter, 1)
	f.set(10, 1)
	if err := f.stmt.Parameters[0].BindValue(f.stmt.h, 0, "first binding fails", f.conn); err == nil {
		t.Error("first binding failure was ignored")
	}
	f.set(10, 0)
}
