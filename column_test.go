// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/alexbrainman/odbc/api"
)

func bytesOf[T any](value *T) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(value)), int(unsafe.Sizeof(*value)))
}

func TestAuditColumnRetryFailures(t *testing.T) {
	f := auditNative(t)
	for _, mode := range []string{"call", "native", "length", "changed length"} {
		calls := 0
		_, err := newColumn(f.stmt.h, 0, func(api.SQLHSTMT, int, []uint16) (int, api.SQLSMALLINT, api.SQLULEN, api.SQLRETURN, error) {
			calls++
			if calls == 1 {
				return 200, api.SQL_VARCHAR, 4, api.SQL_SUCCESS_WITH_INFO, nil
			}
			switch mode {
			case "call":
				return 0, 0, 0, 0, context.Canceled
			case "native":
				return 0, 0, 0, api.SQL_ERROR, nil
			case "changed length":
				return 201, api.SQL_VARCHAR, 4, api.SQL_SUCCESS_WITH_INFO, nil
			default:
				return -1, 0, 0, api.SQL_SUCCESS, nil
			}
		})
		if err == nil || calls != 2 {
			t.Fatalf("%s retry: calls=%d, err=%v", mode, calls, err)
		}
	}
}

func TestAuditUnboundColumnErrors(t *testing.T) {
	f := auditNative(t)
	column := NewBindableColumn(&BaseColumn{}, api.SQL_C_CHAR, 8)
	column.IsVariableWidth = true
	for _, mode := range []int64{2, 3, 5, 6} {
		f.set(12, mode)
		value, err := column.Value(f.stmt.h, 0)
		if mode == 2 && (value != nil || err != nil) {
			t.Fatalf("NULL: %v, %v", value, err)
		}
		if mode == 3 && (err != nil || len(value.([]byte)) != 0) {
			t.Fatalf("empty text: %v, %v", value, err)
		}
		if mode >= 5 && err == nil {
			t.Errorf("invalid indicator mode %d accepted", mode)
		}
	}
	column.IsBound = true
	if _, err := column.Value(f.stmt.h, 0); err == nil {
		t.Error("bound column without an indicator accepted")
	}
	column.IsVariableWidth = false
	length := BufferLen(1)
	column.boundLen = &length
	if _, err := column.Value(f.stmt.h, 0); err == nil {
		t.Error("short fixed-width value accepted")
	}
}

func TestAuditChunkedColumnDiagnostics(t *testing.T) {
	f := auditNative(t)
	column := &NonBindableColumn{BaseColumn: &BaseColumn{CType: api.SQL_C_CHAR}}
	if bound, err := column.Bind(f.stmt.h, 0); bound || err != nil {
		t.Fatalf("unbounded column unexpectedly bound: %t, %v", bound, err)
	}
	for _, mode := range []int64{7, 8} {
		f.set(12, mode)
		value, err := column.Value(f.stmt.h, 0)
		if mode == 7 && (value != nil || err != nil) {
			t.Fatalf("NULL with warning: %v, %v", value, err)
		}
		if mode == 8 && err == nil {
			t.Error("negative length with warning accepted")
		}
	}
	f.set(12, 4)
	for _, diagnostic := range []int64{1, 2, 3} {
		f.set(16, diagnostic)
		if _, err := column.Value(f.stmt.h, 0); err == nil {
			t.Errorf("diagnostic failure mode %d accepted", diagnostic)
		}
		// Start each stream at its first chunk.
		if err := f.stmt.Exec(nil, f.conn); err != nil {
			t.Fatal(err)
		}
	}
	f.set(16, 0)
	column.CType = api.SQL_C_WCHAR
	value, err := column.Value(f.stmt.h, 0)
	if err != nil || len([]rune(string(value.([]byte)))) != 1025 {
		t.Fatalf("chunked wide value: %v, %v", value, err)
	}
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
	for _, tc := range []struct {
		ctype api.SQLSMALLINT
		size  int
	}{{api.SQL_C_CHAR, 5}, {api.SQL_C_WCHAR, 10}} {
		column, err := NewVariableWidthColumn(&BaseColumn{}, tc.ctype, 4)
		if err != nil {
			t.Fatal(err)
		}
		if got := column.(*BindableColumn).Size; got != tc.size {
			t.Errorf("ctype %d buffer size = %d; want %d", tc.ctype, got, tc.size)
		}
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

func TestAuditMultibyteTextRoundTrip(t *testing.T) {
	f := auditNative(t)
	f.set(0, int64(api.SQL_VARCHAR))
	f.set(1, 4) // Four characters occupy eight UTF-8 bytes.
	f.set(12, 1)
	rows, err := f.conn.QueryContext(context.Background(), "owned four-character text fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		t.Fatal(err)
	}
	got, ok := values[0].([]byte)
	if !ok || string(got) != "éééé" {
		t.Errorf("text bytes=%q (%T); want complete UTF-8 text %q", values[0], values[0], "éééé")
	}
}

func TestAuditColumnConversions(t *testing.T) {
	for _, tc := range []struct {
		ctype   api.SQLSMALLINT
		sqltype api.SQLSMALLINT
		data    []byte
		want    any
	}{
		{api.SQL_C_BIT, 0, []byte{1}, true},
		{api.SQL_C_LONG, 0, bytesOf(new(int32)), int32(0)},
		{api.SQL_C_SBIGINT, 0, bytesOf(new(int64)), int64(0)},
		{api.SQL_C_DOUBLE, 0, bytesOf(new(float64)), float64(0)},
		{api.SQL_C_BINARY, 0, []byte{0, 1}, []byte{0, 1}},
		{api.SQL_C_CHAR, 0, []byte("abc"), []byte("abc")},
		{api.SQL_C_WCHAR, 0, []byte{}, []byte{}},
		{api.SQL_C_GUID, 0, bytesOf(&api.SQLGUID{Data1: 0x01234567, Data2: 0x89ab, Data3: 0xcdef, Data4: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}), "01234567-89ab-cdef-0102-030405060708"},
		{api.SQL_C_DATE, 0, bytesOf(&api.SQL_DATE_STRUCT{Year: 2024, Month: 2, Day: 29}), time.Date(2024, 2, 29, 0, 0, 0, 0, time.Local)},
	} {
		got, err := (&BaseColumn{CType: tc.ctype, SQLType: tc.sqltype}).Value(tc.data)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("C type %d: got=%v want=%v err=%v", tc.ctype, got, tc.want, err)
		}
	}
	if _, err := (&BaseColumn{CType: -999}).Value(nil); err == nil {
		t.Error("unsupported C type accepted")
	}
	for _, data := range [][]uint16{{0xd83d, 0xde00}, {0xd800}, {0xdc00}, {0xd800, 'x'}} {
		if got, want := string(utf16toutf8(data)), string(utf16.Decode(data)); got != want {
			t.Errorf("UTF-16=%x got=%q want=%q", data, got, want)
		}
	}
	if err := validateODBCTime(0, -1, 0, 0); err == nil {
		t.Error("invalid minute accepted")
	}
	if err := validateColumnNameLength(maxColumnNameLength + 1); err == nil {
		t.Error("oversized column name accepted")
	}
}

func TestAuditColumnTypeMetadata(t *testing.T) {
	for _, typ := range []api.SQLSMALLINT{api.SQL_BIT, api.SQL_TINYINT, api.SQL_SMALLINT, api.SQL_INTEGER, api.SQL_BIGINT, api.SQL_NUMERIC, api.SQL_DECIMAL, api.SQL_FLOAT, api.SQL_REAL, api.SQL_DOUBLE, api.SQL_TYPE_TIMESTAMP, api.SQL_TYPE_DATE, api.SQL_TYPE_TIME, api.SQL_SS_TIME2, api.SQL_GUID, api.SQL_CHAR, api.SQL_VARCHAR, api.SQL_WCHAR, api.SQL_WVARCHAR, api.SQL_BINARY, api.SQL_VARBINARY, api.SQL_LONGVARCHAR, api.SQL_WLONGVARCHAR, api.SQL_SS_XML, api.SQL_LONGVARBINARY, -999} {
		col, err := newColumn(0, 0, func(_ api.SQLHSTMT, _ int, name []uint16) (int, api.SQLSMALLINT, api.SQLULEN, api.SQLRETURN, error) {
			copy(name, []uint16{'x', 0})
			return 1, typ, 12, api.SQL_SUCCESS, nil
		})
		if typ == -999 {
			if err == nil {
				t.Error("unknown SQL type accepted")
			}
			continue
		}
		if err != nil || col.Name() != "x" {
			t.Errorf("SQL type %d: column=%v error=%v", typ, col, err)
		}
	}
	if _, err := NewVariableWidthColumn(&BaseColumn{}, -999, 1); err == nil {
		t.Error("unknown variable-width C type accepted")
	}
	for _, retry := range []bool{false, true} {
		calls := 0
		_, err := newColumn(0, 0, func(_ api.SQLHSTMT, _ int, name []uint16) (int, api.SQLSMALLINT, api.SQLULEN, api.SQLRETURN, error) {
			calls++
			if retry && calls == 1 {
				return 200, api.SQL_VARCHAR, 1, api.SQL_SUCCESS_WITH_INFO, nil
			}
			return 0, 0, 0, api.SQL_ERROR, errors.New("owned description failure")
		})
		if err == nil {
			t.Error("description failure accepted")
		}
	}
}

func TestAuditVariableWidthReads(t *testing.T) {
	for _, mode := range []int64{1, 2, 3, 4, 5, 6} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := auditNative(t)
			f.set(12, mode)
			col := &NonBindableColumn{&BaseColumn{CType: api.SQL_C_CHAR}}
			value, err := col.Value(f.stmt.h, 0)
			if mode >= 5 {
				if err == nil {
					t.Error("invalid native length accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == 2 {
				if value != nil {
					t.Errorf("NULL became %v", value)
				}
				return
			}
			want := "éééé"
			if mode == 3 {
				want = ""
			}
			if mode == 4 {
				want = strings.Repeat("x", 2050)
			}
			data, ok := value.([]byte)
			if !ok || data == nil || string(data) != want {
				t.Errorf("mode %d read %v; want %q", mode, value, want)
			}
		})
	}
}
