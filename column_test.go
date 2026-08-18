// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"strings"
	"testing"

	"github.com/alexbrainman/odbc/api"
)

func TestBaseColumnValueRejectsInvalidBuffers(t *testing.T) {
	tests := []struct {
		name   string
		ctype  api.SQLSMALLINT
		buffer []byte
	}{
		{name: "empty integer", ctype: api.SQL_C_LONG},
		{name: "short integer", ctype: api.SQL_C_LONG, buffer: make([]byte, 3)},
		{name: "odd UTF-16", ctype: api.SQL_C_WCHAR, buffer: []byte{1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			column := &BaseColumn{CType: test.ctype}
			if _, err := column.Value(test.buffer); err == nil {
				t.Fatal("Value unexpectedly succeeded")
			}
		})
	}
}

func TestColumnBufferPointerAndEmptyBindBuffer(t *testing.T) {
	if pointer := columnBufferPointer(nil); pointer != nil {
		t.Fatalf("columnBufferPointer(nil) = %v; want nil", pointer)
	}
	buffer := []byte{1}
	if pointer := columnBufferPointer(buffer); pointer == nil {
		t.Fatal("columnBufferPointer(non-empty) returned nil")
	}
	column := &BindableColumn{BaseColumn: new(BaseColumn)}
	if bound, err := column.Bind(0, 2); err == nil || bound {
		t.Fatalf("Bind with empty buffer = %t, %v; want false and an error", bound, err)
	}
}

func TestBindableColumnValueRejectsInvalidLengths(t *testing.T) {
	tests := []struct {
		name   string
		length BufferLen
	}{
		{name: "data at execution", length: BufferLen(api.SQL_DATA_AT_EXEC)},
		{name: "unknown total", length: BufferLen(api.SQL_NO_TOTAL)},
		{name: "oversized", length: 9},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			column := NewBindableColumn(&BaseColumn{}, api.SQL_C_CHAR, 8)
			column.IsVariableWidth = true
			column.IsBound = true
			length := test.length
			column.boundLen = &length
			if _, err := column.Value(0, 0); err == nil {
				t.Fatalf("Value unexpectedly accepted length %d", length)
			}
		})
	}
}

func TestNewColumnRetriesWithTerminatorSpace(t *testing.T) {
	name := strings.Repeat("x", 200)
	calls := 0
	column, err := newColumn(1, 0, func(_ api.SQLHSTMT, _ int, buffer []uint16) (int, api.SQLSMALLINT, api.SQLULEN, api.SQLRETURN, error) {
		calls++
		if calls == 1 {
			if len(buffer) != initialColumnNameBufferLength {
				t.Errorf("initial buffer length = %d", len(buffer))
			}
			return len(name), api.SQL_VARCHAR, 20, api.SQL_SUCCESS_WITH_INFO, nil
		}
		if len(buffer) != len(name)+1 {
			t.Errorf("retry buffer length = %d; want %d", len(buffer), len(name)+1)
		}
		copy(buffer, api.StringToUTF16(name))
		return len(name), api.SQL_VARCHAR, 20, api.SQL_SUCCESS, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("describe calls = %d; want 2", calls)
	}
	if column.Name() != name {
		t.Fatalf("column name length = %d; want %d", len(column.Name()), len(name))
	}
}

func TestNewColumnRejectsInvalidNameLength(t *testing.T) {
	_, err := newColumn(1, 0, func(api.SQLHSTMT, int, []uint16) (int, api.SQLSMALLINT, api.SQLULEN, api.SQLRETURN, error) {
		return -1, api.SQL_VARCHAR, 20, api.SQL_SUCCESS, nil
	})
	if err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("newColumn error = %v; want negative length error", err)
	}
}
