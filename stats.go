// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"fmt"
	"sync"

	"github.com/alexbrainman/odbc/api"
)

// Stats is a point-in-time snapshot of the native ODBC handles owned by a
// Driver. A snapshot does not change after it is returned and is safe to read
// concurrently with driver operations.
type Stats struct {
	// EnvCount is the number of allocated environment handles.
	EnvCount int
	// ConnCount is the number of allocated connection handles.
	ConnCount int
	// StmtCount is the number of allocated statement handles.
	StmtCount int
}

type handleStats struct {
	mu        sync.RWMutex
	envCount  int
	connCount int
	stmtCount int
}

func (s *handleStats) snapshot() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Stats{
		EnvCount:  s.envCount,
		ConnCount: s.connCount,
		StmtCount: s.stmtCount,
	}
}

func (s *handleStats) updateHandleCount(handleType api.SQLSMALLINT, change int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch handleType {
	case api.SQL_HANDLE_ENV:
		s.envCount += change
	case api.SQL_HANDLE_DBC:
		s.connCount += change
	case api.SQL_HANDLE_STMT:
		s.stmtCount += change
	default:
		return fmt.Errorf("unexpected handle type %d", handleType)
	}
	return nil
}
