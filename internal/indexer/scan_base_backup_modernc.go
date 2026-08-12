//go:build !ck3_native || !cgo

package indexer

import (
	"context"
	"fmt"

	"modernc.org/sqlite"
)

type sqliteBackuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

// onlineBackupDatabase copies one coherent SQLite snapshot, including pages
// that are still resident in the source WAL. Copying only the main file can
// validate one generation and seed a different, older one.
func onlineBackupDatabase(ctx context.Context, src, dst string) error {
	return onlineBackupDatabaseWithOptions(ctx, src, dst, DefaultSQLiteReadOptions())
}

func onlineBackupDatabaseWithOptions(ctx context.Context, src, dst string, options SQLiteReadOptions) error {
	removeDatabaseSnapshot(dst)
	base, err := OpenReadOnlyWithOptions(src, options)
	if err != nil {
		return err
	}
	defer base.Close()
	conn, err := base.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	err = conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(sqliteBackuper)
		if !ok {
			return fmt.Errorf("SQLite driver does not support online backup")
		}
		backup, err := backuper.NewBackup(dst)
		if err != nil {
			return err
		}
		var stepErr error
		for more := true; more; {
			if err := ctx.Err(); err != nil {
				stepErr = err
				break
			}
			more, stepErr = backup.Step(256)
			if stepErr != nil {
				break
			}
		}
		finishErr := backup.Finish()
		if stepErr != nil {
			return stepErr
		}
		return finishErr
	})
	if err != nil {
		removeDatabaseSnapshot(dst)
		return err
	}
	return nil
}
