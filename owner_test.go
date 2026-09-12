// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
)

func TestNativeOwnerDoesNotReplayStartedWork(t *testing.T) {
	c := &Conn{h: 1}
	_, err := runOwned(context.Background(), c, func() (int, error) {
		return 0, driver.ErrBadConn
	})
	if err == nil || errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("started operation can trigger database/sql retry: %v", err)
	}
	c.invalidate()
	_, err = runOwned(context.Background(), c, func() (int, error) {
		t.Fatal("invalid connection admitted work")
		return 0, nil
	})
	if !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("unused invalid connection cannot be replaced: %v", err)
	}
}

func TestNativeOwnerSnapshotsByteArguments(t *testing.T) {
	data := []byte{1, 2}
	args := []driver.Value{data, []byte{}, []byte(nil), "text"}
	owned := copyValues(args)
	data[0], args[3] = 9, "changed"
	if owned[0].([]byte)[0] != 1 || owned[3] != "text" {
		t.Fatal("native work retains caller-owned arguments")
	}
	if owned[1].([]byte) == nil || owned[2].([]byte) != nil {
		t.Fatal("argument snapshot changes empty/null byte semantics")
	}
}

func TestNativeOwnerAdmissionPolicy(t *testing.T) {
	for _, d := range []*Driver{{NativeLimit: -1}, {CloseTimeout: -1}} {
		if err := d.acquireNativeSlot(); err == nil {
			t.Error("invalid native policy accepted")
		}
	}
	d := &Driver{NativeLimit: 2}
	for range 2 {
		if err := d.acquireNativeSlot(); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.acquireNativeSlot(); !errors.Is(err, ErrNativeLimit) {
		t.Fatal(err)
	}
	d.releaseNativeSlot()
	if err := d.acquireNativeSlot(); err != nil {
		t.Fatal(err)
	}
	d.releaseNativeSlot()
	d.releaseNativeSlot()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.acquireNativeSlot(); err == nil {
		t.Error("closed driver accepted work")
	}
}

func TestNativeOwnerContainsGoPanics(t *testing.T) {
	c := &Conn{h: 1}
	_, err := nativeOperation(c, func() (int, error) { panic("fixture") })
	if !errors.Is(err, ErrNativePanic) || c.IsValid() {
		t.Fatalf("panic ownership: %v", err)
	}
}
