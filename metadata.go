package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"unsafe"

	"github.com/alexbrainman/odbc/api"
)

const (
	infoSearchEscape api.SQLUSMALLINT = 14
	infoDatabase     api.SQLUSMALLINT = 16
	infoDBMSName     api.SQLUSMALLINT = 17
	infoDBMSVersion  api.SQLUSMALLINT = 18
	infoQuote        api.SQLUSMALLINT = 29
)

// DBMSInfo describes the connected server, rather than its DSN or driver name.
type DBMSInfo struct {
	Name         string `json:"name" yaml:"name" xml:"name"`
	Version      string `json:"version" yaml:"version" xml:"version"`
	Database     string `json:"database" yaml:"database" xml:"database"`
	Quote        string `json:"quote" yaml:"quote" xml:"quote"`
	SearchEscape string `json:"searchEscape" yaml:"searchEscape" xml:"searchEscape"`
}

// Info reads server identity and identifier rules on the connection's native owner.
// Cancellation retires the connection; native buffers remain owned until return.
func (c *Conn) Info(ctx context.Context) (DBMSInfo, error) {
	return runOwned(ctx, c, func() (DBMSInfo, error) {
		var info DBMSInfo
		for _, v := range []struct {
			key  api.SQLUSMALLINT
			dest *string
		}{
			{infoDBMSName, &info.Name}, {infoDBMSVersion, &info.Version}, {infoDatabase, &info.Database},
			{infoQuote, &info.Quote}, {infoSearchEscape, &info.SearchEscape},
		} {
			if err := ctx.Err(); err != nil {
				return DBMSInfo{}, err
			}
			if !c.IsValid() {
				return DBMSInfo{}, errNativeInvalid
			}
			value, err := c.infoString(v.key)
			if err != nil {
				return DBMSInfo{}, err
			}
			*v.dest = value
		}
		return info, nil
	})
}

func (c *Conn) infoString(key api.SQLUSMALLINT) (string, error) {
	return readInfoString(func(buf []uint16, n *api.SQLSMALLINT) error {
		ret, err := safeSQLCall("SQLGetInfo", func() api.SQLRETURN {
			return api.SQLGetInfo(c.h, key, api.SQLPOINTER(unsafe.Pointer(&buf[0])), api.SQLSMALLINT(len(buf)*2), n)
		})
		if err != nil {
			c.invalidate()
			return err
		}
		if IsError(ret) {
			return c.newError("SQLGetInfo", c.h)
		}
		return nil
	})
}

// SQLGetInfoW reports both capacity and returned length in bytes.
func readInfoString(read func([]uint16, *api.SQLSMALLINT) error) (string, error) {
	buf := make([]uint16, 128)
	for {
		var n api.SQLSMALLINT
		if err := read(buf, &n); err != nil {
			return "", err
		}
		if n < 0 || n%2 != 0 || n > 32762 {
			return "", errors.New("odbc: invalid information length")
		}
		if int(n)/2 < len(buf) {
			return api.UTF16ToString(buf[:int(n)/2]), nil
		}
		buf = make([]uint16, int(n)/2+1)
	}
}

// CatalogFilter selects literal object names. Empty fields leave that qualifier
// unspecified; callers must resolve ambiguous unqualified results themselves.
type CatalogFilter struct {
	Catalog string `json:"catalog" yaml:"catalog" xml:"catalog"`
	Schema  string `json:"schema" yaml:"schema" xml:"schema"`
	Table   string `json:"table" yaml:"table" xml:"table"`
}

// CatalogKind selects an ODBC metadata result set.
type CatalogKind uint8

const (
	CatalogTables      CatalogKind = iota + 1 // Tables and views (SQLTables).
	CatalogColumns                            // Column definitions (SQLColumns).
	CatalogPrimaryKeys                        // Primary key columns (SQLPrimaryKeys).
	CatalogIndexes                            // Index columns and table statistics (SQLStatistics).
)

// CatalogRecord contains an ODBC catalog row keyed by its standard uppercase
// column names. Values are detached from native buffers and may be nil.
type CatalogRecord map[string]any

// Catalog returns catalog rows for filter. It uses the same serialization,
// cancellation and cleanup rules as ordinary SQL queries, without executing SQL.
func (c *Conn) Catalog(ctx context.Context, kind CatalogKind, filter CatalogFilter) ([]CatalogRecord, error) {
	if kind < CatalogTables || kind > CatalogIndexes {
		return nil, errors.New("odbc: invalid catalog kind")
	}
	for _, v := range []string{filter.Catalog, filter.Schema, filter.Table} {
		if strings.IndexByte(v, 0) >= 0 {
			return nil, errors.New("odbc: catalog name contains a nul byte")
		}
	}
	return runOwned(ctx, c, func() (records []CatalogRecord, err error) {
		if kind == CatalogTables || kind == CatalogColumns {
			escape, err := c.infoString(infoSearchEscape)
			if err != nil {
				return nil, err
			}
			fields := []*string{&filter.Schema, &filter.Table}
			if kind == CatalogTables {
				fields = append(fields, &filter.Catalog)
			}
			for _, v := range fields {
				if *v, err = catalogPattern(*v, escape); err != nil {
					return nil, err
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !c.IsValid() {
			return nil, errNativeInvalid
		}
		s, _, err := c.allocateODBCStmt("")
		if err != nil {
			return nil, err
		}

		defer func() {
			if closeErr := s.closeByStmt(); closeErr != nil {
				c.invalidate()
				err = errors.Join(err, closeErr)
			}
		}()
		return runContextOperation(ctx, func() (records []CatalogRecord, err error) {
			a, b, t := catalogText(filter.Catalog), catalogText(filter.Schema), catalogText(filter.Table)
			ptr := func(v []uint16) *api.SQLWCHAR {
				if len(v) == 0 {
					return nil
				}
				return (*api.SQLWCHAR)(unsafe.Pointer(&v[0]))
			}
			ret, callErr := safeSQLCall("ODBC catalog", func() api.SQLRETURN {
				switch kind {
				case CatalogTables:
					return api.SQLTables(s.h, ptr(a), api.SQL_NTS, ptr(b), api.SQL_NTS, ptr(t), api.SQL_NTS, nil, 0)
				case CatalogColumns:
					return api.SQLColumns(s.h, ptr(a), api.SQL_NTS, ptr(b), api.SQL_NTS, ptr(t), api.SQL_NTS, nil, 0)
				case CatalogPrimaryKeys:
					return api.SQLPrimaryKeys(s.h, ptr(a), api.SQL_NTS, ptr(b), api.SQL_NTS, ptr(t), api.SQL_NTS)
				default:
					return api.SQLStatistics(s.h, ptr(a), api.SQL_NTS, ptr(b), api.SQL_NTS, ptr(t), api.SQL_NTS, 1, 1)
				}
			})
			if callErr != nil {
				c.invalidate()
				return nil, callErr
			}
			if IsError(ret) {
				return nil, c.newError("ODBC catalog", s.h)
			}
			cursor := &odbcRows{os: s, c: c}
			if err = cursor.BindColumns(); err != nil {
				return nil, err
			}
			return readCatalog(cursor)
		}, func() error { return s.Cancel(c) }, nil)
	})
}

func catalogText(s string) []uint16 {
	if s == "" {
		return nil
	}
	return api.StringToUTF16(s)
}

func catalogPattern(name, escape string) (string, error) {
	if escape == "" {
		if strings.ContainsAny(name, "%_") {
			return "", fmt.Errorf("odbc: driver cannot escape catalog patterns")
		}
		return name, nil
	}
	return strings.NewReplacer(escape, escape+escape, "%", escape+"%", "_", escape+"_").Replace(name), nil
}

func readCatalog(cursor rowsCursor) ([]CatalogRecord, error) {
	names := cursor.Columns()
	var records []CatalogRecord
	for {
		values := make([]driver.Value, len(names))
		if err := cursor.Next(values); err != nil {
			if err == io.EOF {
				return records, nil
			}
			return nil, err
		}
		record := make(CatalogRecord, len(names))
		for i, value := range copyValues(values) {
			record[strings.ToUpper(names[i])] = value
		}
		records = append(records, record)
	}
}
