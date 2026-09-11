// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/alexbrainman/odbc/api"
)

func bytesOf[T any](value *T) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(value)), int(unsafe.Sizeof(*value)))
}

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

func TestBaseColumnValuePreservesEmbeddedNUL(t *testing.T) {
	buffer := make([]byte, 6)
	binary.NativeEndian.PutUint16(buffer[0:2], 'a')
	binary.NativeEndian.PutUint16(buffer[2:4], 0)
	binary.NativeEndian.PutUint16(buffer[4:6], 'b')

	value, err := (&BaseColumn{CType: api.SQL_C_WCHAR}).Value(buffer)
	if err != nil {
		t.Fatal(err)
	}
	bytes, ok := value.([]byte)
	if !ok || string(bytes) != "a\x00b" {
		t.Fatalf("wide value = %T(%q); want %q", value, value, "a\x00b")
	}
}

func TestBaseColumnValueRejectsInvalidTemporalFields(t *testing.T) {
	tests := []struct {
		name   string
		column BaseColumn
		buffer []byte
	}{
		{
			name:   "timestamp month",
			column: BaseColumn{CType: api.SQL_C_TYPE_TIMESTAMP},
			buffer: bytesOf(&api.SQL_TIMESTAMP_STRUCT{Year: 2026, Month: 13, Day: 1}),
		},
		{
			name:   "timestamp fraction",
			column: BaseColumn{CType: api.SQL_C_TYPE_TIMESTAMP},
			buffer: bytesOf(&api.SQL_TIMESTAMP_STRUCT{Year: 2026, Month: 1, Day: 1, Fraction: 1_000_000_000}),
		},
		{
			name:   "date day",
			column: BaseColumn{CType: api.SQL_C_DATE},
			buffer: bytesOf(&api.SQL_DATE_STRUCT{Year: 2026, Month: 2, Day: 30}),
		},
		{
			name:   "time hour",
			column: BaseColumn{CType: api.SQL_C_TIME},
			buffer: bytesOf(&api.SQL_TIME_STRUCT{Hour: 24}),
		},
		{
			name:   "time2 fraction",
			column: BaseColumn{SQLType: api.SQL_SS_TIME2, CType: api.SQL_C_BINARY},
			buffer: bytesOf(&api.SQL_SS_TIME2_STRUCT{Fraction: 1_000_000_000}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if value, err := test.column.Value(test.buffer); err == nil {
				t.Fatalf("Value = %v; want invalid temporal field error", value)
			}
		})
	}
}

func TestODBCRejectsUnrepresentableSeconds(t *testing.T) {
	for _, second := range []api.SQLUSMALLINT{59, 60, 61} {
		for _, tc := range []struct {
			name   string
			column BaseColumn
			buffer []byte
		}{
			{"timestamp", BaseColumn{CType: api.SQL_C_TYPE_TIMESTAMP}, bytesOf(&api.SQL_TIMESTAMP_STRUCT{Year: 2016, Month: 12, Day: 31, Hour: 23, Minute: 59, Second: second, Fraction: 999_999_999})},
			{"time", BaseColumn{CType: api.SQL_C_TIME}, bytesOf(&api.SQL_TIME_STRUCT{Hour: 23, Minute: 59, Second: second})},
			{"time2", BaseColumn{SQLType: api.SQL_SS_TIME2, CType: api.SQL_C_BINARY}, bytesOf(&api.SQL_SS_TIME2_STRUCT{Hour: 23, Minute: 59, Second: second, Fraction: 999_999_999})},
		} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, second), func(t *testing.T) {
				value, err := tc.column.Value(tc.buffer)
				if second > 59 {
					if value != nil || err == nil || !strings.Contains(err.Error(), "not representable") {
						t.Fatalf("unrepresentable second %d converted to %v, error=%v", second, value, err)
					}
					return
				}
				got, ok := value.(time.Time)
				if err != nil || !ok || got.Hour() != 23 || got.Minute() != 59 || got.Second() != 59 || got.Day() != map[string]int{"timestamp": 31, "time": 1, "time2": 1}[tc.name] {
					t.Fatalf("representable boundary = %v, %v", value, err)
				}
				if tc.name != "time" && got.Nanosecond() != 999_999_999 {
					t.Fatalf("fraction lost: %v", got)
				}
			})
		}
	}
}

func TestODBCTemporalValidationAcceptsBoundaries(t *testing.T) {
	if err := validateODBCDate(2024, 2, 29); err != nil {
		t.Fatal(err)
	}
	if err := validateODBCTime(0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := validateODBCTime(23, 59, 59, 999_999_999); err != nil {
		t.Fatal(err)
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

func TestNewColumnPreservesExactNumericAsText(t *testing.T) {
	column, err := newColumn(1, 0, func(_ api.SQLHSTMT, _ int, buffer []uint16) (int, api.SQLSMALLINT, api.SQLULEN, api.SQLRETURN, error) {
		copy(buffer, api.StringToUTF16("amount"))
		return len("amount"), api.SQL_DECIMAL, 38, api.SQL_SUCCESS, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	numeric, ok := column.(*NonBindableColumn)
	if !ok {
		t.Fatalf("decimal column type = %T; want *NonBindableColumn", column)
	}
	if numeric.CType != api.SQL_C_CHAR {
		t.Fatalf("decimal C type = %d; want SQL_C_CHAR", numeric.CType)
	}

	want := []byte("-12345678901234567890.123456789012345678")
	got, err := numeric.BaseColumn.Value(want)
	if err != nil {
		t.Fatal(err)
	}
	bytes, ok := got.([]byte)
	if !ok || string(bytes) != string(want) {
		t.Fatalf("decimal value = %T(%v); want exact bytes %q", got, got, want)
	}
}

func TestNonBindableColumnPreservesEmptyValue(t *testing.T) {
	dsn := os.Getenv("ODBC_SQLITE_DSN")
	if dsn == "" {
		t.Skip("set ODBC_SQLITE_DSN to exercise empty long values")
	}

	driverManager := new(Driver)
	dc, err := driverManager.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn := dc.(*Conn)
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
		if err := driverManager.Close(); err != nil {
			t.Error(err)
		}
	})
	exec := func(query string) {
		statement, err := conn.Prepare(query)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := statement.(driver.StmtExecContext).ExecContext(context.Background(), nil); err != nil {
			statement.Close()
			t.Fatal(err)
		}
		if err := statement.Close(); err != nil {
			t.Fatal(err)
		}
	}
	exec("create temp table empty_value_test (a varchar(2048), b varchar(2048))")
	exec("insert into empty_value_test values ('', null)")

	statement, err := conn.Prepare("select a, b from empty_value_test")
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	result, err := statement.(driver.StmtQueryContext).QueryContext(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	rows := result.(*Rows)
	for i, column := range rows.rowsCursor.(*odbcRows).os.Cols {
		if _, ok := column.(*NonBindableColumn); !ok {
			t.Fatalf("column %d = %T; want *NonBindableColumn", i, column)
		}
	}
	values := make([]driver.Value, 2)
	if err := rows.Next(values); err != nil {
		t.Fatal(err)
	}
	empty, ok := values[0].([]byte)
	if !ok {
		t.Fatalf("empty text = %T; want []byte", values[0])
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty text = %#v; want a non-nil empty slice", empty)
	}
	if values[1] != nil {
		t.Fatalf("NULL text = %#v; want nil", values[1])
	}
}
