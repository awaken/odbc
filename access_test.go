// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package odbc

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

func TestAccessMemo(t *testing.T) {
	tmpdir, err := os.MkdirTemp("../../../tmp", "odbc-access-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(tmpdir); err != nil {
			t.Errorf("remove Access directory: %v", err)
		}
	})

	dbfilename := filepath.Join(tmpdir, "db.mdb")
	createAccessDB(t, dbfilename)

	db, err := sql.Open("odbc", fmt.Sprintf("DRIVER={Microsoft Access Driver (*.mdb)};DBQ=%s;", dbfilename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close Access database: %v", err)
		}
	})

	err = db.Ping()
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec("create table mytable (m memo)")
	if err != nil {
		t.Fatal(err)
	}
	for s := ""; len(s) < 1000; s += "0123456789" {
		_, err = db.Exec("insert into mytable (m) values (?)", s)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func createAccessDB(t *testing.T, dbfilename string) {
	t.Helper()
	// COM initialization and releases must stay on the same OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := ole.CoInitialize(0)
	if err != nil {
		// S_FALSE also acquires an initialization reference that must be released.
		const comAlreadyInitialized = 1
		if e, ok := errors.AsType[*ole.OleError](err); !ok || e.Code() != comAlreadyInitialized {
			t.Fatal(err)
		}
	}
	defer ole.CoUninitialize()

	unk, err := oleutil.CreateObject("adox.catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer unk.Release()
	cat, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Release()
	result, err := oleutil.CallMethod(cat, "create", fmt.Sprintf("provider=microsoft.jet.oledb.4.0;data source=%s;", dbfilename))
	if result != nil {
		defer func() {
			if err := result.Clear(); err != nil {
				t.Errorf("clear Access result: %v", err)
			}
		}()
	}
	if err != nil {
		t.Fatal(err)
	}
}
