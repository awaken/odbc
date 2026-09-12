// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultNativeLimit bounds each Driver's open and quarantined connections.
	DefaultNativeLimit = 256
	// DefaultCloseTimeout bounds public native cleanup waits.
	DefaultCloseTimeout = 5 * time.Second
)

var (
	// ErrNativeLimit means no connection can be admitted until an owner closes.
	ErrNativeLimit = errors.New("odbc: native connection limit reached")
	// ErrCleanupPending means native resources are still owned and unavailable.
	ErrCleanupPending = errors.New("odbc: native cleanup is incomplete")
	// ErrNativePanic reports a recovered panic in an owned driver operation.
	ErrNativePanic   = errors.New("odbc: native operation panicked")
	errNativeInvalid = errors.New("odbc: connection invalidated during native work")
)

// connOwner serializes native work, including cleanup after public cancellation.
// Its gate is released only after the operation and any SQLCancel have returned.
type connOwner struct {
	gate      chan struct{}
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
	abandoned atomic.Bool
}

func (c *Conn) nativeOwner() *connOwner {
	c.ownerOnce.Do(func() {
		c.owner = &connOwner{gate: make(chan struct{}, 1), closeDone: make(chan struct{})}
		c.owner.gate <- struct{}{}
	})
	return c.owner
}

func (c *Conn) cleanupTimeout() time.Duration {
	if c.closeTimeout > 0 {
		return c.closeTimeout
	}
	return DefaultCloseTimeout
}

// runOwned keeps all native state private after a canceled public call returns.
// A late successful value is reclaimed by connection cleanup, never published.
func runOwned[T any](ctx context.Context, c *Conn, operation func() (T, error)) (T, error) {
	return runNative(ctx, c, operation, false)
}

// Opening owns its reserved slot before a connection handle exists.
func runNative[T any](ctx context.Context, c *Conn, operation func() (T, error), opening bool) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if c.bad.Load() || !opening && !c.IsValid() {
		return zero, driver.ErrBadConn
	}
	o := c.nativeOwner()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-o.gate:
	}
	if err := ctx.Err(); err != nil {
		o.gate <- struct{}{}
		return zero, err
	}
	if c.bad.Load() || !opening && !c.IsValid() {
		o.gate <- struct{}{}
		return zero, driver.ErrBadConn
	}
	result := make(chan contextResult[T], 1)
	go func() {
		defer func() { o.gate <- struct{}{} }()
		value, err := nativeOperation(c, operation)
		// Once work starts, ErrBadConn could trigger an unsafe database/sql
		// replay. Only the admission checks above may return that sentinel.
		if errors.Is(err, driver.ErrBadConn) {
			err = errNativeInvalid
		}
		result <- contextResult[T]{value: value, err: err}
	}()
	select {
	case r := <-result:
		if c.bad.Load() && c.driver != nil {
			c.requestClose()
		}
		return r.value, r.err
	case <-ctx.Done():
		c.invalidate()
		o.abandoned.Store(true)
		c.requestClose()
		return zero, ctx.Err()
	}
}

func nativeOperation[T any](c *Conn, operation func() (T, error)) (value T, err error) {
	value, err = callOperation(operation)
	if errors.Is(err, ErrNativePanic) {
		c.invalidate()
	}
	return value, err
}

func callOperation[T any](operation func() (T, error)) (value T, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", ErrNativePanic, p)
		}
	}()
	return operation()
}

// requestClose creates at most one cleanup waiter per admitted connection.
func (c *Conn) requestClose() *connOwner {
	c.invalidate()
	o := c.nativeOwner()
	o.closeOnce.Do(func() {
		go func() {
			<-o.gate
			_, o.closeErr = nativeOperation(c, func() (struct{}, error) {
				return struct{}{}, c.closeNative()
			})
			if o.closeErr == nil && c.driver != nil {
				c.driver.releaseNativeSlot()
			}
			close(o.closeDone)
			o.gate <- struct{}{}
		}()
	})
	return o
}

func (c *Conn) closeResource(operation func() error) error {
	if !c.IsValid() {
		o := c.requestClose()
		select {
		case <-o.closeDone:
			if o.closeErr == nil {
				return nil
			}
			return errors.Join(ErrCleanupPending, o.closeErr)
		default:
			return ErrCleanupPending
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cleanupTimeout())
	defer cancel()
	_, err := runOwned(ctx, c, func() (struct{}, error) { return struct{}{}, operation() })
	if errors.Is(err, context.DeadlineExceeded) {
		c.nativeOwner().abandoned.Store(true)
		c.requestClose()
		return ErrCleanupPending
	}
	return err
}

// copyValues owns the only mutable driver.Value representation across return.
func copyValues(args []driver.Value) []driver.Value {
	values := append([]driver.Value(nil), args...)
	for i, value := range values {
		if b, ok := value.([]byte); ok && b != nil {
			values[i] = append([]byte{}, b...)
		}
	}
	return values
}

func (d *Driver) acquireNativeSlot() error {
	d.nativeMu.Lock()
	defer d.nativeMu.Unlock()
	if d.nativeClosed {
		return errors.New("odbc: driver is closed")
	}
	if d.nativeSlots == nil {
		limit := d.NativeLimit
		if limit == 0 {
			limit = DefaultNativeLimit
		}
		if limit < 1 || d.CloseTimeout < 0 {
			return errors.New("odbc: invalid native limit or close timeout")
		}
		d.nativeSlots = make(chan struct{}, limit)
	}
	select {
	case d.nativeSlots <- struct{}{}:
		return nil
	default:
		return ErrNativeLimit
	}
}

func (d *Driver) releaseNativeSlot() {
	d.nativeMu.Lock()
	<-d.nativeSlots
	d.nativeMu.Unlock()
}
