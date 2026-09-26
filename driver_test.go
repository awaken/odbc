package odbc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alexbrainman/odbc/api"
)

func TestAuditDriverPanicGuards(t *testing.T) {
	for _, tc := range []struct {
		name, call string
		nth        int32
		connect    bool
		fail       bool
		pool       DriverPoolMode
	}{
		{"pooling", "SQLSetEnvAttr", 1, false, false, DriverPoolModeNone},
		{"environment allocation", "SQLAllocHandle", 1, false, true, DriverPoolModeBasic},
		{"environment version", "SQLSetEnvAttr", 2, false, true, DriverPoolModeBasic},
		{"pool matching", "SQLSetEnvAttr", 3, false, false, DriverPoolModeBasic},
		{"connection allocation", "SQLAllocHandle", 1, true, true, DriverPoolModeFull},
		{"connection open", "SQLDriverConnect", 1, true, true, DriverPoolModeFull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = auditNative(t)
			d := new(Driver)
			if tc.connect {
				if err := d.initialize(); err != nil {
					t.Fatal(err)
				}
			}
			restore := auditCallPanic(t, tc.call, tc.nth)
			defer restore()
			var err error
			if tc.connect {
				var c any
				c, err = d.Open("DRIVER={Owned Test Fixture}")
				if c != nil {
					t.Errorf("failed connection was published: %v", c)
				}
			} else {
				err = d.initialize()
			}
			if (err != nil) != tc.fail || tc.fail && !strings.Contains(err.Error(), "owned audit native failure") || d.PoolMode() != tc.pool {
				t.Errorf("initialization panic: error=%v pool=%v", err, d.PoolMode())
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				err = d.Close()
				if !errors.Is(err, ErrCleanupPending) || time.Now().After(deadline) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if err != nil || d.Stats() != (Stats{}) {
				t.Fatalf("panic cleanup: error=%v handles=%+v", err, d.Stats())
			}
		})
	}
}

func TestAuditDriverInitialization(t *testing.T) {
	for _, tc := range []struct {
		name string
		attr int64
		mode DriverPoolMode
		fail bool
	}{
		{"full pooling", 0, DriverPoolModeFull, false},
		{"pooling unavailable", int64(api.SQL_ATTR_CONNECTION_POOLING), DriverPoolModeNone, false},
		{"matching unavailable", int64(api.SQL_ATTR_CP_MATCH), DriverPoolModeBasic, false},
		{"version rejected", int64(api.SQL_ATTR_ODBC_VERSION), DriverPoolModeBasic, true},
		{"allocation rejected", -1, DriverPoolModeBasic, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := auditNative(t)
			if tc.attr == -1 {
				f.mode(nativeAlloc, 2)
				defer f.mode(nativeAlloc, 0)
			} else {
				f.set(18, tc.attr)
				defer f.set(18, 0)
			}
			d := new(Driver)
			err := d.initialize()
			if (err != nil) != tc.fail || d.PoolMode() != tc.mode {
				t.Errorf("initialization: error=%v pooling=%v", err, d.PoolMode())
			}
			if again := d.initialize(); again != err {
				t.Errorf("initialization did not retain its result: %v, %v", err, again)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			if s := d.Stats(); s != (Stats{}) {
				t.Fatalf("initialization retained handles: %+v", s)
			}
		})
	}
}

func TestAuditDriverOpenFailures(t *testing.T) {
	for _, op := range []int32{nativeAlloc, nativeConnect} {
		t.Run(map[int32]string{nativeAlloc: "allocate", nativeConnect: "connect"}[op], func(t *testing.T) {
			f := auditNative(t)
			d := new(Driver)
			if err := d.initialize(); err != nil {
				t.Fatal(err)
			}
			f.mode(op, 2)
			c, err := d.Open("DRIVER={Owned Test Fixture}")
			f.mode(op, 0)
			if c != nil || err == nil {
				t.Fatalf("failed native open: connection=%v error=%v", c, err)
			}
			// Failed Open cleans up asynchronously; wait for slot release.
			deadline := time.Now().Add(2 * time.Second)
			for {
				err = d.Close()
				if !errors.Is(err, ErrCleanupPending) || time.Now().After(deadline) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if err != nil || d.Stats() != (Stats{}) {
				t.Fatalf("failed open cleanup: error=%v handles=%+v", err, d.Stats())
			}
		})
	}
}

func TestAuditConnectorCancellation(t *testing.T) {
	d := new(Driver)
	n, err := d.OpenConnector("DRIVER={Owned Test Fixture}")
	if err != nil || n.Driver() != d {
		t.Fatalf("connector: %v, %v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := n.Connect(ctx); c != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled connection: %v, %v", c, err)
	}
	if d.nativeSlots != nil || d.Stats() != (Stats{}) {
		t.Fatal("canceled connection acquired native resources")
	}
}
