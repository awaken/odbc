// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import "context"

type contextResult[T any] struct {
	value T
	err   error
}

// runContextOperation gives a selected context cancellation precedence over
// native and cleanup errors. It waits for the native call after requesting
// cancellation so its handle and pinned buffers remain valid until return.
func runContextOperation[T any](ctx context.Context, operation func() (T, error), cancel func() error, discard func(T)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	resultChan := make(chan contextResult[T], 1)
	go func() {
		value, err := callOperation(operation)
		resultChan <- contextResult[T]{value: value, err: err}
	}()

	select {
	case result := <-resultChan:
		return result.value, result.err
	case <-ctx.Done():
		_ = cancel()
		result := <-resultChan
		if result.err == nil && discard != nil {
			discard(result.value)
		}
		return zero, ctx.Err()
	}
}
