// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"errors"
	"testing"
)

func TestRunContextOperationReturnsCompletedResult(t *testing.T) {
	cancelCalled := false
	value, err := runContextOperation(context.Background(), func() (int, error) {
		return 42, nil
	}, func() error {
		cancelCalled = true
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != 42 {
		t.Fatalf("value = %d; want 42", value)
	}
	if cancelCalled {
		t.Fatal("cancel called for a completed operation")
	}
}

func TestRunContextOperationStopsBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	operationCalled := false
	cancelCalled := false
	value, err := runContextOperation(ctx, func() (int, error) {
		operationCalled = true
		return 42, nil
	}, func() error {
		cancelCalled = true
		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want %v", err, context.Canceled)
	}
	if value != 0 {
		t.Fatalf("value = %d; want zero", value)
	}
	if operationCalled || cancelCalled {
		t.Fatalf("operation called = %t, cancel called = %t; want neither", operationCalled, cancelCalled)
	}
}

func TestRunContextOperationCancelsWaitsAndDiscards(t *testing.T) {
	ctx, cancelContext := context.WithCancel(context.Background())
	nativeStarted := make(chan struct{})
	nativeRelease := make(chan struct{})
	discarded := make(chan int, 1)

	go func() {
		<-nativeStarted
		cancelContext()
	}()

	cancelCalled := false
	value, err := runContextOperation(ctx, func() (int, error) {
		close(nativeStarted)
		<-nativeRelease
		return 42, nil
	}, func() error {
		cancelCalled = true
		close(nativeRelease)
		return errors.New("native cancellation diagnostic")
	}, func(value int) {
		discarded <- value
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want %v", err, context.Canceled)
	}
	if value != 0 {
		t.Fatalf("value = %d; want zero", value)
	}
	if !cancelCalled {
		t.Fatal("native cancellation was not requested")
	}
	if got := <-discarded; got != 42 {
		t.Fatalf("discarded value = %d; want 42", got)
	}
}
