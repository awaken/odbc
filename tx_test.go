// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"errors"
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
