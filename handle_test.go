package odbc

import (
	"strings"
	"testing"

	"github.com/alexbrainman/odbc/api"
)

func TestAuditHandleValidation(t *testing.T) {
	for _, tc := range []struct {
		handle any
		typeID api.SQLSMALLINT
	}{
		{api.SQLHENV(0), 0},
		{api.SQLHENV(1), api.SQL_HANDLE_ENV},
		{api.SQLHDBC(1), api.SQL_HANDLE_DBC},
		{api.SQLHSTMT(1), api.SQL_HANDLE_STMT},
	} {
		_, kind, err := ToHandleAndType(tc.handle)
		if err != nil || kind != tc.typeID {
			t.Errorf("handle %T: type=%v error=%v", tc.handle, kind, err)
		}
	}
	for _, h := range []any{nil, 1, "not a native handle"} {
		if _, _, err := ToHandleAndType(h); err == nil {
			t.Errorf("accepted invalid handle %T", h)
		}
		if err := releaseHandle(h, nil); err == nil {
			t.Errorf("released invalid handle %T", h)
		}
		if err := NewError("fixture", h); err == nil || !strings.Contains(err.Error(), "unexpected handle type") {
			t.Errorf("diagnostics for invalid handle %T: %v", h, err)
		}
	}
}

func TestAuditNativeHandleRelease(t *testing.T) {
	f := auditNative(t)
	var h api.SQLHANDLE
	if ret := api.SQLAllocHandle(api.SQL_HANDLE_STMT, api.SQLHANDLE(f.conn.h), &h); IsError(ret) {
		t.Fatalf("allocate standalone fixture handle: %d", ret)
	}
	defer func() {
		f.set(19, 0)
		if h != 0 {
			if err := releaseHandle(api.SQLHSTMT(h), nil); err != nil {
				t.Error(err)
			}
		}
	}()
	for _, ret := range []api.SQLRETURN{api.SQL_ERROR, api.SQL_INVALID_HANDLE} {
		f.set(19, int64(ret))
		if err := releaseHandle(api.SQLHSTMT(h), nil); err == nil {
			t.Errorf("native free failure %d was ignored", ret)
		}
	}
	f.set(19, 0)
	if err := releaseHandle(api.SQLHSTMT(h), nil); err != nil {
		t.Fatal(err)
	}
	h = 0
}
