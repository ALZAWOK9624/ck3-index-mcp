//go:build !ck3_native || !cgo || !sqlite_fts5

package indexer

import (
	"database/sql/driver"

	_ "modernc.org/sqlite"
)

// newNativeConnector is nil in the default build: db.go falls back to
// sql.Open with the modernc driver, whose _pragma DSN handling applies the
// tuning pragmas to every pooled connection.
func newNativeConnector(dsn string, pragmas []sqlitePragma) driver.Connector {
	return nil
}

// sqlitePragmaQueryParam formats a PRAGMA for the modernc driver's DSN, which
// spells each pragma as _pragma=name=value.
func sqlitePragmaQueryParam(name, value string) string {
	return name + "=" + value
}
