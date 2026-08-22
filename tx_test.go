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
