// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"sync"
	"testing"

	"github.com/alexbrainman/odbc/api"
)

func TestDriverStatsSnapshot(t *testing.T) {
	driver := new(Driver)
	for _, handleType := range []api.SQLSMALLINT{
		api.SQL_HANDLE_ENV,
		api.SQL_HANDLE_DBC,
		api.SQL_HANDLE_STMT,
	} {
		if err := driver.stats.updateHandleCount(handleType, 1); err != nil {
			t.Fatal(err)
		}
	}

	got := driver.Stats()
	if got.EnvCount != 1 || got.ConnCount != 1 || got.StmtCount != 1 {
		t.Fatalf("Stats() = %#v; want one handle of each type", got)
	}

	if err := driver.stats.updateHandleCount(api.SQL_HANDLE_STMT, 1); err != nil {
		t.Fatal(err)
	}
	if got.StmtCount != 1 {
		t.Fatalf("previous snapshot changed to %#v", got)
	}
	if current := driver.Stats(); current.StmtCount != 2 {
		t.Fatalf("current Stats() = %#v; want two statements", current)
	}
}

func TestDriverStatsSnapshotConcurrent(t *testing.T) {
	driver := new(Driver)
	var waitGroup sync.WaitGroup
	for range 32 {
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			for range 100 {
				if err := driver.stats.updateHandleCount(api.SQL_HANDLE_STMT, 1); err != nil {
					t.Error(err)
				}
				if err := driver.stats.updateHandleCount(api.SQL_HANDLE_STMT, -1); err != nil {
					t.Error(err)
				}
			}
		}()
		go func() {
			defer waitGroup.Done()
			for range 100 {
				_ = driver.Stats().StmtCount
			}
		}()
	}
	waitGroup.Wait()

	if got := driver.Stats().StmtCount; got != 0 {
		t.Fatalf("StmtCount = %d; want 0", got)
	}
}

func TestDriverPoolModeConcurrent(t *testing.T) {
	driver := new(Driver)
	var waitGroup sync.WaitGroup
	for i := range 32 {
		mode := DriverPoolMode(i % 3)
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			driver.setPoolMode(mode)
		}()
		go func() {
			defer waitGroup.Done()
			_ = driver.PoolMode()
			_ = driver.IsPooling()
			_ = driver.IsFullPooling()
		}()
	}
	waitGroup.Wait()

	driver.setPoolMode(DriverPoolModeFull)
	if driver.PoolMode() != DriverPoolModeFull || !driver.IsPooling() || !driver.IsFullPooling() {
		t.Fatal("full pooling mode was not retained")
	}
}
