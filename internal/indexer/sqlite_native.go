//go:build ck3_native && cgo

package indexer

import (
	"context"
	"database/sql"
	"database/sql/driver"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// Building with this file requires TWO build tags: -tags "ck3_native
// sqlite_fts5". The second one is consumed by github.com/mattn/go-sqlite3
// itself; without it its bundled amalgamation is compiled without FTS5 and
// the search_fts/script_text_fts virtual tables fail to open.
//
// The sqlite3.c amalgamation is compiled in by cgo; no DLL or runtime
// dependency is added.

var nativeDriver = &sqlite3.SQLiteDriver{}

func init() {
	// Register the native driver under the same name modernc uses so the rest
	// of the package opens it unchanged.
	sql.Register("sqlite", nativeDriver)
}

// sqlitePragmaQueryParam formats a PRAGMA for the native driver's DSN. mattn
// does not understand a generic _pragma parameter, so the pragmas are applied
// by nativeConnector instead; this helper exists only to keep the DSN builder
// honest and is not used for mattn DSNs.
func sqlitePragmaQueryParam(name, value string) string {
	return name + "(" + value + ")"
}

// nativeConnector replicates modernc's _pragma semantics on top of mattn:
// every connection database/sql opens runs the tuning pragmas before it is
// handed out, with the values of the Open call that created this connector.
type nativeConnector struct {
	dsn     string
	pragmas []sqlitePragma
}

func newNativeConnector(dsn string, pragmas []sqlitePragma) driver.Connector {
	return &nativeConnector{dsn: dsn, pragmas: pragmas}
}

func (c *nativeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := nativeDriver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	execer, ok := conn.(driver.ExecerContext)
	if !ok {
		conn.Close()
		return nil, sql.ErrConnDone
	}
	for _, pragma := range c.pragmas {
		if _, err := execer.ExecContext(ctx, "PRAGMA "+pragma.name+"="+pragma.value, nil); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func (c *nativeConnector) Driver() driver.Driver {
	return nativeDriver
}
