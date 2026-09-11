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
