package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexbrainman/odbc/api"
)

type observedMetadataContext struct {
	ctx     context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *observedMetadataContext) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c *observedMetadataContext) Done() <-chan struct{}       { return c.ctx.Done() }
func (c *observedMetadataContext) Err() error {
	err := c.ctx.Err()
	if err == nil {
		c.once.Do(func() { close(c.checked) })
	}
	return err
}
func (c *observedMetadataContext) Value(key any) any { return c.ctx.Value(key) }

func TestInfoString(t *testing.T) {
	for _, want := range []string{"", `"`, "PostgreSQL", strings.Repeat("界", 256), "db😀"} {
		got, err := readInfoString(func(buf []uint16, n *api.SQLSMALLINT) error {
			encoded := api.StringToUTF16(want)
			*n = api.SQLSMALLINT((len(encoded) - 1) * 2)
			copy(buf, encoded)
			return nil
		})
		if err != nil || got != want {
			t.Fatalf("info=%q: %v", got, err)
		}
	}
	for _, length := range []api.SQLSMALLINT{-1, 3, 32764} {
		if _, err := readInfoString(func(_ []uint16, n *api.SQLSMALLINT) error { *n = length; return nil }); err == nil {
			t.Fatalf("invalid length %d accepted", length)
		}
	}
	want := errors.New("read failed")
	if _, err := readInfoString(func([]uint16, *api.SQLSMALLINT) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestCatalogPatterns(t *testing.T) {
	for _, tc := range []struct {
		name, escape, want string
		fail               bool
	}{
		{"orders", "", "orders", false}, {"orders_2026", "", "", true},
		{"a_b%", "\\", `a\_b\%`, false}, {`a\b`, "\\", `a\\b`, false},
	} {
		got, err := catalogPattern(tc.name, tc.escape)
		if got != tc.want || (err != nil) != tc.fail {
			t.Fatalf("pattern=%q: %v", got, err)
		}
	}
	if catalogText("") != nil {
		t.Fatal("empty qualifier must be unspecified")
	}
	if got := api.UTF16ToString(catalogText("orders")); got != "orders" {
		t.Fatal(got)
	}
}

func TestCatalogRecords(t *testing.T) {
	value := []byte("name")
	cursor := &rowsTestCursor{columns: []string{"column_name", "nullable"}, sets: [][][]driver.Value{{{value, nil}}}}
	rows, err := readCatalog(cursor)
	if err != nil || len(rows) != 1 || rows[0]["NULLABLE"] != nil {
		t.Fatalf("rows=%v: %v", rows, err)
	}
	value[0] = 'X'
	if string(rows[0]["COLUMN_NAME"].([]byte)) != "name" {
		t.Fatal("catalog retained borrowed bytes")
	}
	want := errors.New("fetch failed")
	cursor.fetchErr = want
	if rows, err = readCatalog(cursor); rows != nil || !errors.Is(err, want) {
		t.Fatalf("rows=%v: %v", rows, err)
	}
}

func TestMetadataAdmission(t *testing.T) {
	c := new(Conn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Info(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.Catalog(ctx, CatalogTables, CatalogFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.Catalog(context.Background(), 0, CatalogFilter{}); err == nil {
		t.Fatal("invalid kind accepted")
	}
	if _, err := c.Catalog(context.Background(), CatalogTables, CatalogFilter{Table: "a\x00b"}); err == nil {
		t.Fatal("nul accepted")
	}
}

func TestAuditMetadataNative(t *testing.T) {
	f := auditNative(t)
	info, err := f.conn.Info(context.Background())
	if err != nil || info != (DBMSInfo{Name: "Fixture DBMS", Version: "03.45.00", Database: "catalog", Quote: "[", SearchEscape: `\`}) {
		t.Fatalf("information=%+v: %v", info, err)
	}
	filter := CatalogFilter{Catalog: "catalog_1", Schema: "sales%", Table: "a_b"}
	for _, kind := range []CatalogKind{CatalogTables, CatalogColumns, CatalogPrimaryKeys, CatalogIndexes} {
		rows, err := f.conn.Catalog(context.Background(), kind, filter)
		if err != nil || len(rows) != 1 || rows[0]["X"] != int32(42) {
			t.Fatalf("catalog %d=%v: %v", kind, rows, err)
		}
		if f.get(24) != int64(kind) || f.get(25) != 0 || f.conn.driver.Stats().StmtCount != 1 {
			t.Fatalf("catalog %d ABI or cleanup: kind=%d invalid=%d stats=%+v", kind, f.get(24), f.get(25), f.conn.driver.Stats())
		}
	}
	if rows, err := f.conn.Catalog(context.Background(), CatalogPrimaryKeys, CatalogFilter{}); err != nil || len(rows) != 1 {
		t.Fatalf("unqualified catalog=%v: %v", rows, err)
	}
}

func TestAuditMetadataFailures(t *testing.T) {
	filter := CatalogFilter{Catalog: "catalog_1", Schema: "sales%", Table: "a_b"}
	for _, tc := range []struct {
		name string
		run  func(*auditODBC) error
	}{
		{"information", func(f *auditODBC) error { f.set(22, 1); _, err := f.conn.Info(context.Background()); return err }},
		{"information length", func(f *auditODBC) error { f.set(22, 2); _, err := f.conn.Info(context.Background()); return err }},
		{"catalog information", func(f *auditODBC) error {
			f.set(22, 1)
			_, err := f.conn.Catalog(context.Background(), CatalogTables, filter)
			return err
		}},
		{"pattern escape", func(f *auditODBC) error {
			f.set(22, 3)
			_, err := f.conn.Catalog(context.Background(), CatalogTables, CatalogFilter{Table: "a_b"})
			return err
		}},
		{"allocation", func(f *auditODBC) error {
			f.mode(nativeAlloc, 2)
			defer f.mode(nativeAlloc, 0)
			_, err := f.conn.Catalog(context.Background(), CatalogPrimaryKeys, filter)
			return err
		}},
		{"catalog", func(f *auditODBC) error {
			f.set(23, 1)
			_, err := f.conn.Catalog(context.Background(), CatalogTables, filter)
			return err
		}},
		{"binding", func(f *auditODBC) error {
			f.mode(nativeBind, 2)
			defer f.mode(nativeBind, 0)
			_, err := f.conn.Catalog(context.Background(), CatalogPrimaryKeys, filter)
			return err
		}},
		{"fetch", func(f *auditODBC) error {
			f.mode(nativeFetch, 2)
			defer f.mode(nativeFetch, 0)
			_, err := f.conn.Catalog(context.Background(), CatalogPrimaryKeys, filter)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := auditNative(t)
			if err := tc.run(f); err == nil {
				t.Fatal("metadata failure was ignored")
			}
		})
	}
}

func TestAuditMetadataCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, *Conn) error
		op   int32
	}{
		{"information", func(ctx context.Context, c *Conn) error { _, err := c.Info(ctx); return err }, nativeConnect},
		{"catalog", func(ctx context.Context, c *Conn) error {
			_, err := c.Catalog(ctx, CatalogTables, CatalogFilter{})
			return err
		}, nativeAlloc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := auditNative(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.mode(nativeConnect, 1)
			t.Cleanup(func() { f.mode(nativeConnect, 0) })
			done := make(chan error, 1)
			go func() { done <- tc.run(ctx, f.conn) }()
			f.waitActive(t, nativeConnect)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("metadata call did not return on cancellation")
			}
			atReturn := f.calls(tc.op)
			f.mode(nativeConnect, 0)
			auditWaitClose(t, f.conn)
			if extra := f.calls(tc.op) - atReturn; extra != 0 {
				t.Fatalf("started %d native operations after cancellation", extra)
			}
		})
	}

	t.Run("catalog native call", func(t *testing.T) {
		f := auditNative(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.mode(nativePrepare, 1)
		t.Cleanup(func() { f.mode(nativePrepare, 0) })
		done := make(chan error, 1)
		go func() {
			_, err := f.conn.Catalog(ctx, CatalogPrimaryKeys, CatalogFilter{})
			done <- err
		}()
		f.waitActive(t, nativePrepare)
		cancel()
		deadline := time.Now().Add(100 * time.Millisecond)
		for f.calls(nativeCancel) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("catalog cancellation did not reach the statement")
			}
			time.Sleep(time.Millisecond)
		}
		f.mode(nativePrepare, 0)
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("catalog call did not return on cancellation")
		}
		auditWaitClose(t, f.conn)
	})

	t.Run("owner wait", func(t *testing.T) {
		f := auditNative(t)
		f.mode(nativeConnect, 1)
		t.Cleanup(func() { f.mode(nativeConnect, 0) })
		first := make(chan error, 1)
		go func() { _, err := f.conn.Info(context.Background()); first <- err }()
		f.waitActive(t, nativeConnect)

		ctx, cancel := context.WithCancel(context.Background())
		observed := &observedMetadataContext{ctx: ctx, checked: make(chan struct{})}
		second := make(chan error, 1)
		go func() { _, err := f.conn.Info(observed); second <- err }()
		select {
		case <-observed.checked:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("queued metadata call did not check its context")
		}
		atCancel := f.calls(nativeConnect)
		cancel()
		select {
		case err := <-second:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("queued cancellation: %v", err)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("queued metadata call did not return on cancellation")
		}
		if calls := f.calls(nativeConnect); calls != atCancel {
			t.Fatalf("queued cancellation reached the driver: calls=%d, want %d", calls, atCancel)
		}

		f.mode(nativeConnect, 0)
		if err := <-first; err != nil {
			t.Fatalf("owning metadata call: %v", err)
		}
	})
}

func TestAuditMetadataConnectionClose(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Conn) error
		op   int32
	}{
		{"information", func(c *Conn) error { _, err := c.Info(context.Background()); return err }, nativeConnect},
		{"catalog", func(c *Conn) error {
			_, err := c.Catalog(context.Background(), CatalogTables, CatalogFilter{})
			return err
		}, nativeAlloc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := auditNative(t)
			f.mode(nativeConnect, 1)
			t.Cleanup(func() { f.mode(nativeConnect, 0) })
			done := make(chan error, 1)
			go func() { done <- tc.run(f.conn) }()
			f.waitActive(t, nativeConnect)
			if err := f.conn.Close(); !errors.Is(err, ErrCleanupPending) {
				t.Fatalf("blocked close: %v", err)
			}
			atClose := f.calls(tc.op)
			f.mode(nativeConnect, 0)
			select {
			case err := <-done:
				if !errors.Is(err, errNativeInvalid) {
					t.Fatalf("closed connection: %v", err)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("metadata call did not stop after close")
			}
			auditWaitClose(t, f.conn)
			if extra := f.calls(tc.op) - atClose; extra != 0 {
				t.Fatalf("started %d native operations after close", extra)
			}
		})
	}
}
