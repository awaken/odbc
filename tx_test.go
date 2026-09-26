// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/alexbrainman/odbc/api"
)

func TestBeginFailureDoesNotLeaveActiveTransaction(t *testing.T) {
	wantErr := errors.New("cannot disable autocommit")
	testBeginErr = wantErr
	t.Cleanup(func() { testBeginErr = nil })

	connection := &Conn{h: 1}
	transaction, err := connection.Begin()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Begin error = %v; want %v", err, wantErr)
	}
	if transaction != nil {
		t.Fatalf("Begin transaction = %#v; want nil", transaction)
	}
	if connection.tx != nil {
		t.Fatalf("Begin retained failed transaction %#v", connection.tx)
	}
	if !connection.bad.Load() {
		t.Fatal("Begin did not mark the connection bad")
	}
}

func TestAuditTransactions(t *testing.T) {
	for _, commit := range []bool{true, false} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			f := auditNative(t)
			tx, err := f.conn.BeginTx(context.Background(), driver.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if again, err := f.conn.Begin(); err == nil || again != nil {
				t.Fatalf("nested transaction accepted: %v, %v", again, err)
			}
			want := int64(api.SQL_ROLLBACK)
			if commit {
				err = tx.Commit()
				want = int64(api.SQL_COMMIT)
			} else {
				err = tx.Rollback()
			}
			if err != nil || f.conn.tx != nil || !f.conn.IsValid() || f.get(21) != want {
				t.Fatalf("transaction completion: error=%v active=%v valid=%v action=%d", err, f.conn.tx != nil, f.conn.IsValid(), f.get(21))
			}
			if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "not in a transaction") {
				t.Fatalf("repeated completion: %v", err)
			}
		})
	}
	if _, err := new(Conn).begin(); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("begin on invalid connection: %v", err)
	}
	if err := (&Tx{c: new(Conn)}).Rollback(); !errors.Is(err, errNativeInvalid) {
		t.Fatalf("rollback on invalid connection: %v", err)
	}
}

func TestAuditTransactionNativeFailures(t *testing.T) {
	for _, stage := range []string{"begin", "end", "restore autocommit"} {
		t.Run(stage, func(t *testing.T) {
			f := auditNative(t)
			defer f.mode(nativeAutocommit, 0)
			var err error
			wantAPI := "SQLSetConnectUIntPtrAttr"
			if stage == "begin" {
				f.mode(nativeAutocommit, 2)
				_, err = f.conn.Begin()
			} else {
				tx, beginErr := f.conn.Begin()
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				if stage == "end" {
					f.set(17, 1)
					wantAPI = "SQLEndTran"
				} else {
					f.mode(nativeAutocommit, 2)
				}
				err = tx.Commit()
			}
			var diagnostic *Error
			if !errors.As(err, &diagnostic) || diagnostic.APIName != wantAPI || f.conn.IsValid() {
				t.Fatalf("native failure: error=%v valid=%v", err, f.conn.IsValid())
			}
			auditWaitClose(t, f.conn)
		})
	}
}

func TestAuditTransactionCleanupDeadline(t *testing.T) {
	f := auditNative(t)
	tx, err := f.conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	f.mode(nativeEndTran, 1)
	defer f.mode(nativeEndTran, 0)
	if err := tx.Commit(); !errors.Is(err, ErrCleanupPending) || f.conn.IsValid() {
		t.Fatalf("blocked commit: error=%v valid=%v", err, f.conn.IsValid())
	}
	f.mode(nativeEndTran, 0)
	auditWaitClose(t, f.conn)
}

func TestBeginTxValidatesContextAndOptionsBeforeNativeCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connection := &Conn{h: 1}
	if _, err := connection.BeginTx(ctx, driver.TxOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("BeginTx cancelled error = %v; want %v", err, context.Canceled)
	}

	tests := []struct {
		name    string
		options driver.TxOptions
		want    string
	}{
		{name: "isolation", options: driver.TxOptions{Isolation: 1}, want: "isolation level"},
		{name: "read only", options: driver.TxOptions{ReadOnly: true}, want: "read-only"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transaction, err := connection.BeginTx(context.Background(), test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BeginTx error = %v; want text %q", err, test.want)
			}
			if transaction != nil {
				t.Fatalf("BeginTx transaction = %#v; want nil", transaction)
			}
		})
	}
	if connection.tx != nil {
		t.Fatalf("BeginTx retained transaction %#v", connection.tx)
	}

	if _, err := new(Conn).BeginTx(context.Background(), driver.TxOptions{}); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("BeginTx bad connection error = %v; want %v", err, driver.ErrBadConn)
	}
}
