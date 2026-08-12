//go:build ck3_native && cgo

package indexer

/*
#cgo CFLAGS: -O2

#include <stdio.h>
#include <stdlib.h>

// Opaque declarations only: the sqlite3 symbols are resolved against the
// amalgamation compiled inside github.com/mattn/go-sqlite3, which this build
// links anyway. Declaring them here instead of including the header keeps the
// cgo preamble free of module-cache include paths.

typedef struct sqlite3 sqlite3;
typedef struct sqlite3_backup sqlite3_backup;

#define SQLITE_OK 0
#define SQLITE_DONE 101
#define SQLITE_BUSY 5
#define SQLITE_LOCKED 6
#define SQLITE_OPEN_READONLY 0x00000001
#define SQLITE_OPEN_READWRITE 0x00000002
#define SQLITE_OPEN_CREATE 0x00000004
#define SQLITE_OPEN_URI 0x00000040

extern int sqlite3_open_v2(const char *filename, sqlite3 **ppDb, int flags, const char *zVfs);
extern int sqlite3_close(sqlite3 *db);
extern int sqlite3_busy_timeout(sqlite3 *db, int ms);
extern int sqlite3_errcode(sqlite3 *db);
extern const char *sqlite3_errmsg(sqlite3 *db);
extern sqlite3_backup *sqlite3_backup_init(sqlite3 *pDest, const char *zDestName, sqlite3 *pSource, const char *zSourceName);
extern int sqlite3_backup_step(sqlite3_backup *p, int nPage);
extern int sqlite3_backup_finish(sqlite3_backup *p);
extern void sqlite3_sleep(int ms);

// gh_sqlite_backup copies the coherent online snapshot of src into dst using
// the SQLite backup API, which carries WAL-resident pages the same way
// modernc's Backup does. Returns an errcode (SQLITE_OK on success); on error
// the message is left in *errmsgOut (caller provides the buffer).
static int gh_sqlite_backup(const char *src, const char *dst, char *errmsgOut, int errmsgLen)
{
	sqlite3 *sdb = 0;
	sqlite3 *ddb = 0;
	int rc = sqlite3_open_v2(src, &sdb, SQLITE_OPEN_READONLY | SQLITE_OPEN_URI, 0);
	if (rc != SQLITE_OK) {
		if (sdb) {
			snprintf(errmsgOut, errmsgLen, "%s", sqlite3_errmsg(sdb));
			sqlite3_close(sdb);
		} else {
			snprintf(errmsgOut, errmsgLen, "cannot open source: %d", rc);
		}
		return rc;
	}
	sqlite3_busy_timeout(sdb, 5000);
	rc = sqlite3_open_v2(dst, &ddb, SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_URI, 0);
	if (rc != SQLITE_OK) {
		if (ddb) {
			snprintf(errmsgOut, errmsgLen, "%s", sqlite3_errmsg(ddb));
		} else {
			snprintf(errmsgOut, errmsgLen, "cannot open destination: %d", rc);
		}
		sqlite3_close(sdb);
		return rc;
	}
	sqlite3_backup *backup = sqlite3_backup_init(ddb, "main", sdb, "main");
	if (!backup) {
		rc = sqlite3_errcode(ddb);
		snprintf(errmsgOut, errmsgLen, "%s", sqlite3_errmsg(ddb));
		sqlite3_close(ddb);
		sqlite3_close(sdb);
		return rc;
	}
	for (;;) {
		rc = sqlite3_backup_step(backup, 256);
		if (rc == SQLITE_OK || rc == SQLITE_BUSY || rc == SQLITE_LOCKED) {
			sqlite3_sleep(10);
			continue;
		}
		break;
	}
	if (rc != SQLITE_DONE) {
		snprintf(errmsgOut, errmsgLen, "backup step failed (%d)", rc);
		sqlite3_backup_finish(backup);
		sqlite3_close(ddb);
		sqlite3_close(sdb);
		return rc;
	}
	rc = sqlite3_backup_finish(backup);
	if (rc != SQLITE_OK) {
		snprintf(errmsgOut, errmsgLen, "backup finish failed (%d)", rc);
	}
	sqlite3_close(ddb);
	sqlite3_close(sdb);
	return rc;
}
*/
import "C"

import (
	"context"
	"errors"
	"unsafe"
)

// onlineBackupDatabase copies one coherent SQLite snapshot, including pages
// that are still resident in the source WAL. Under the native build the copy
// runs through libsqlite3's own backup API instead of modernc's wrapper.
func onlineBackupDatabase(ctx context.Context, src, dst string) error {
	return onlineBackupDatabaseWithOptions(ctx, src, dst, DefaultSQLiteReadOptions())
}

func onlineBackupDatabaseWithOptions(ctx context.Context, src, dst string, options SQLiteReadOptions) error {
	removeDatabaseSnapshot(dst)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	cSrc := C.CString(src)
	cDst := C.CString(dst)
	defer C.free(unsafe.Pointer(cSrc))
	defer C.free(unsafe.Pointer(cDst))
	var errBuf [512]C.char
	rc := C.gh_sqlite_backup(cSrc, cDst, &errBuf[0], C.int(len(errBuf)))
	if rc != C.SQLITE_OK {
		removeDatabaseSnapshot(dst)
		return errors.New(C.GoString(&errBuf[0]))
	}
	return nil
}
