package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// dropSecondaryIndexes removes every explicitly declared index on the main
// schema and returns a closure that puts back exactly what it took away.
//
// Refilling a table row by row through sixty-odd live B-trees costs far more
// than sorting each index once at the end; reset() already relies on that,
// which is why a clean scan bulk loads first and calls CreateIndexes after.
// Staged publication performs the same whole-table refill and was the one path
// that kept every index live throughout.
//
// The caller is expected to be inside a write transaction. SQLite keeps DDL
// transactional, so a rollback restores the indexes with the rest of the
// publication, and a crash mid-transaction is recovered the same way.
func dropSecondaryIndexes(ctx context.Context, conn *sql.Conn) (func(context.Context) error, error) {
	// sql IS NOT NULL excludes the indexes SQLite creates for UNIQUE and
	// INTEGER PRIMARY KEY constraints. Those are not droppable, are not
	// row-by-row maintenance the copy can avoid, and naming one here would
	// fail the statement outright.
	rows, err := conn.QueryContext(ctx, `SELECT name, sql FROM sqlite_master
		WHERE type='index' AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, fmt.Errorf("list publication indexes: %w", err)
	}
	type declaredIndex struct{ name, ddl string }
	var declared []declaredIndex
	for rows.Next() {
		var index declaredIndex
		if err := rows.Scan(&index.name, &index.ddl); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read publication index: %w", err)
		}
		if strings.TrimSpace(index.ddl) == "" {
			continue
		}
		declared = append(declared, index)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list publication indexes: %w", err)
	}
	for _, index := range declared {
		if _, err := conn.ExecContext(ctx, `DROP INDEX IF EXISTS `+quoteSQLiteIdentifier(index.name)); err != nil {
			return nil, fmt.Errorf("drop publication index %s: %w", index.name, err)
		}
	}
	return func(restoreCtx context.Context) error {
		// Replaying the captured DDL restores the live database exactly as it
		// was found, including any index a migration added that this binary's
		// canonical list no longer declares.
		for _, index := range declared {
			if _, err := conn.ExecContext(restoreCtx, index.ddl); err != nil {
				return fmt.Errorf("restore publication index %s: %w", index.name, err)
			}
		}
		// The staged generation may have introduced tables this live database
		// had no indexes for, so converge on the canonical set as well. Every
		// statement is IF NOT EXISTS, making this a no-op in the common case.
		for _, statement := range indexStmts {
			if _, err := conn.ExecContext(restoreCtx, statement); err != nil {
				return fmt.Errorf("restore canonical publication index: %w", err)
			}
		}
		return nil
	}, nil
}
