package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// publicationLock serializes every writer that can publish into one configured
// index. The kernel lock is intentionally kept in addition to generation CAS:
// the lock prevents cooperating CLI/MCP processes from doing wasted work,
// while CAS rejects stale publishers that did not participate in the lock.
type publicationLock struct {
	file *os.File
}

func acquirePublicationLock(ctx context.Context, databasePath string) (*publicationLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(absolute+".publication.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockPublicationFile(ctx, file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire index publication lock: %w", err)
	}
	return &publicationLock{file: file}, nil
}

func (lock *publicationLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unlockPublicationFile(lock.file)
	closeErr := lock.file.Close()
	lock.file = nil
	if unlockErr != nil {
		return fmt.Errorf("release index publication lock: %w", unlockErr)
	}
	return closeErr
}
