//go:build ck3_native && cgo && sqlite_fts5

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
extern int sqlite3_backup_remaining(sqlite3_backup *p);
extern int sqlite3_backup_pagecount(sqlite3_backup *p);

// The step loop deliberately stays in Go. Driving it from C meant a Go context
// could not interrupt a copy once it started, and a source held busy by a
// writer retried forever; it also charged every healthy batch a fixed sleep,
// which on a multi-gigabyte cache is minutes of pure waiting.
typedef struct {
	sqlite3 *src;
	sqlite3 *dst;
	sqlite3_backup *backup;
} gh_backup_handle;

static int gh_backup_open(const char *src, const char *dst, gh_backup_handle *h, char *errmsgOut, int errmsgLen)
{
	h->src = 0;
	h->dst = 0;
	h->backup = 0;
	int rc = sqlite3_open_v2(src, &h->src, SQLITE_OPEN_READONLY | SQLITE_OPEN_URI, 0);
	if (rc != SQLITE_OK) {
		if (h->src) {
			snprintf(errmsgOut, errmsgLen, "%s", sqlite3_errmsg(h->src));
			sqlite3_close(h->src);
			h->src = 0;
		} else {
			snprintf(errmsgOut, errmsgLen, "cannot open source: %d", rc);
		}
		return rc;
	}
	// The Go retry loop owns contention timing and cancellation. A native busy
	// timeout would make one sqlite3_backup_step call uninterruptible for up to
	// five seconds, defeating the context checks around it.
	sqlite3_busy_timeout(h->src, 0);
	rc = sqlite3_open_v2(dst, &h->dst, SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_URI, 0);
	if (rc != SQLITE_OK) {
		if (h->dst) {
			snprintf(errmsgOut, errmsgLen, "%s", sqlite3_errmsg(h->dst));
			sqlite3_close(h->dst);
			h->dst = 0;
		} else {
			snprintf(errmsgOut, errmsgLen, "cannot open destination: %d", rc);
		}
		sqlite3_close(h->src);
		h->src = 0;
		return rc;
	}
	sqlite3_busy_timeout(h->dst, 0);
	h->backup = sqlite3_backup_init(h->dst, "main", h->src, "main");
	if (!h->backup) {
		rc = sqlite3_errcode(h->dst);
		snprintf(errmsgOut, errmsgLen, "%s", sqlite3_errmsg(h->dst));
		sqlite3_close(h->dst);
		sqlite3_close(h->src);
		h->dst = 0;
		h->src = 0;
		return rc;
	}
	return SQLITE_OK;
}

static int gh_backup_step(gh_backup_handle *h, int pages)
{
	return sqlite3_backup_step(h->backup, pages);
}

static int gh_backup_remaining(gh_backup_handle *h)
{
	return h->backup ? sqlite3_backup_remaining(h->backup) : 0;
}

static int gh_backup_pagecount(gh_backup_handle *h)
{
	return h->backup ? sqlite3_backup_pagecount(h->backup) : 0;
}

// gh_backup_finish completes the copy. gh_backup_close is idempotent so the
// Go side can defer it on every exit path, including cancellation.
static int gh_backup_finish(gh_backup_handle *h, char *errmsgOut, int errmsgLen)
{
	int rc = SQLITE_OK;
	if (h->backup) {
		rc = sqlite3_backup_finish(h->backup);
		h->backup = 0;
		if (rc != SQLITE_OK && h->dst) {
			snprintf(errmsgOut, errmsgLen, "%s", sqlite3_errmsg(h->dst));
		}
	}
	return rc;
}

static void gh_backup_close(gh_backup_handle *h)
{
	if (h->backup) {
		sqlite3_backup_finish(h->backup);
		h->backup = 0;
	}
	if (h->dst) {
		sqlite3_close(h->dst);
		h->dst = 0;
	}
	if (h->src) {
		sqlite3_close(h->src);
		h->src = 0;
	}
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"
)

const (
	// backupPagesPerStep is one batch of the copy. It bounds how long a step
	// holds the source read lock, which is what decides how quickly a cancel
	// takes effect.
	backupPagesPerStep = 256
	// backupMaxBusyWait bounds a source that stays locked. Without it a writer
	// that never lets go turns the copy into an infinite retry loop.
	backupMaxBusyWait = 60 * time.Second
	// SQLITE_OK is not proof that the copy is converging: a source under
	// continuous writes can repeatedly restart the backup. Bound time without
	// a new high-water mark in copied pages as well as explicit BUSY waits.
	backupMaxNoProgress = 60 * time.Second
	backupMinBackoff    = time.Millisecond
	backupMaxBackoff    = 250 * time.Millisecond
)

const (
	nativeSQLiteOK     = 0
	nativeSQLiteBusy   = 5
	nativeSQLiteLocked = 6
	nativeSQLiteDone   = 101
)

type nativeBackupOperations struct {
	step      func() int
	finish    func() (int, string)
	remaining func() int
	pageCount func() int
	close     func()
	cleanup   func()
	sleep     func(context.Context, time.Duration) error
	now       func() time.Time
	afterStep func(int)
}

type nativeBackupTestHooks struct {
	afterStep func(int)
	sleep     func(context.Context, time.Duration) error
}

// onlineBackupDatabase copies one coherent SQLite snapshot, including pages
// that are still resident in the source WAL. Under the native build the copy
// runs through libsqlite3's own backup API instead of modernc's wrapper.
func onlineBackupDatabase(ctx context.Context, src, dst string) error {
	return onlineBackupDatabaseWithOptions(ctx, src, dst, DefaultSQLiteReadOptions())
}

func onlineBackupDatabaseWithOptions(ctx context.Context, src, dst string, options SQLiteReadOptions) error {
	return onlineBackupDatabaseWithOptionsAndHooks(ctx, src, dst, options, nativeBackupTestHooks{})
}

func onlineBackupDatabaseWithOptionsAndHooks(ctx context.Context, src, dst string, options SQLiteReadOptions, hooks nativeBackupTestHooks) error {
	removeDatabaseSnapshot(dst)
	if err := ctx.Err(); err != nil {
		return err
	}
	cSrc := C.CString(src)
	cDst := C.CString(dst)
	defer C.free(unsafe.Pointer(cSrc))
	defer C.free(unsafe.Pointer(cDst))

	var handle C.gh_backup_handle
	var errBuf [512]C.char
	if rc := C.gh_backup_open(cSrc, cDst, &handle, &errBuf[0], C.int(len(errBuf))); rc != C.SQLITE_OK {
		removeDatabaseSnapshot(dst)
		return errors.New(C.GoString(&errBuf[0]))
	}
	if hooks.sleep == nil {
		hooks.sleep = sleepWithContext
	}
	return runNativeBackupLoop(ctx, nativeBackupOperations{
		step: func() int {
			return int(C.gh_backup_step(&handle, C.int(backupPagesPerStep)))
		},
		finish: func() (int, string) {
			rc := C.gh_backup_finish(&handle, &errBuf[0], C.int(len(errBuf)))
			return int(rc), C.GoString(&errBuf[0])
		},
		remaining: func() int { return int(C.gh_backup_remaining(&handle)) },
		pageCount: func() int { return int(C.gh_backup_pagecount(&handle)) },
		close:     func() { C.gh_backup_close(&handle) },
		cleanup:   func() { removeDatabaseSnapshot(dst) },
		sleep:     hooks.sleep,
		now:       time.Now,
		afterStep: hooks.afterStep,
	})
}

func runNativeBackupLoop(ctx context.Context, operations nativeBackupOperations) (resultErr error) {
	completed := false
	// Keep close and cleanup in one defer so Windows always releases both
	// sqlite handles before attempting to delete the incomplete destination.
	defer func() {
		operations.close()
		if !completed {
			operations.cleanup()
		}
	}()

	backoff := backupMinBackoff
	var busySince time.Time
	progressAt := operations.now()
	mostPagesCopied := -1
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rc := operations.step()
		if operations.afterStep != nil {
			operations.afterStep(rc)
		}
		switch rc {
		case nativeSQLiteOK:
			// A batch was copied and more remain. This is the healthy path and
			// must not wait: at 256 pages a sleep here is paid once per MiB.
			busySince = time.Time{}
			backoff = backupMinBackoff
			pagesCopied := operations.pageCount() - operations.remaining()
			if pagesCopied > mostPagesCopied {
				mostPagesCopied = pagesCopied
				progressAt = operations.now()
			} else if stalled := operations.now().Sub(progressAt); stalled > backupMaxNoProgress {
				return fmt.Errorf("backup made no progress for %s", stalled.Round(time.Second))
			}
		case nativeSQLiteDone:
			if frc, message := operations.finish(); frc != nativeSQLiteOK {
				return fmt.Errorf("backup finish failed (%d): %s", frc, message)
			}
			completed = true
			return nil
		case nativeSQLiteBusy, nativeSQLiteLocked:
			now := operations.now()
			if busySince.IsZero() {
				busySince = now
			}
			if waited := now.Sub(busySince); waited > backupMaxBusyWait {
				return fmt.Errorf("backup source stayed locked for %s", waited.Round(time.Second))
			}
			if err := operations.sleep(ctx, backoff); err != nil {
				return err
			}
			if backoff *= 2; backoff > backupMaxBackoff {
				backoff = backupMaxBackoff
			}
		default:
			return fmt.Errorf("backup step failed (%d)", rc)
		}
	}
}

// sleepWithContext waits, but stops early when the caller gives up.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
