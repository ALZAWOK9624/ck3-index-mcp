package migrator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	rebaseLockName = "transaction.lock"
	// rebaseLockHeartbeatInterval is how often the holder refreshes its lock
	// file. rebaseLockStaleAfter must stay several intervals ahead so an
	// ordinary scheduling delay is never mistaken for a dead holder.
	rebaseLockHeartbeatInterval = 10 * time.Second
	rebaseLockStaleAfter        = 2 * time.Minute
)

// rebaseLockRecord is the on-disk lock content. It exists so an operator who
// finds a blocked transaction can tell which process is holding it instead of
// having to guess whether the lock is abandoned.
type rebaseLockRecord struct {
	PID         int    `json:"pid"`
	Host        string `json:"host"`
	Nonce       string `json:"nonce"`
	Operation   string `json:"operation"`
	AcquiredAt  string `json:"acquired_at"`
	HeartbeatAt string `json:"heartbeat_at"`
}

// RebaseTransactionLock is a cross-process mutex over one transaction
// directory. Atomic manifest replacement alone cannot stop two `migrate build`
// invocations from both passing the status check and then publishing
// contradictory end states, because the check and the publication sit at
// opposite ends of a long copy. Every operation that can change a transaction
// takes this lock first; the manifest revision compare-and-set in
// writeRebaseTransaction remains the last line of defence.
type RebaseTransactionLock struct {
	path      string
	operation string
	nonce     string
	stop      chan struct{}
	stopped   sync.Once
	done      chan struct{}
}

func acquireRebaseTransactionLock(root, id, operation string) (*RebaseTransactionLock, error) {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return nil, err
	}
	if operation == "plan" {
		dir, err = ensureRebaseDirectory(dir)
		if err != nil {
			return nil, err
		}
	} else if err := rebaseRequireRegularDirectory(dir); err != nil {
		// Never create a transaction directory as a side effect of locking:
		// an unknown id must fail rather than leave an empty stub behind.
		return nil, fmt.Errorf("rebase transaction %s: %w", id, err)
	}
	path := filepath.Join(dir, rebaseLockName)
	nonce, err := newRebaseLockNonce()
	if err != nil {
		return nil, err
	}
	lock := &RebaseTransactionLock{path: path, operation: operation, nonce: nonce, stop: make(chan struct{}), done: make(chan struct{})}
	if err := lock.claim(false); err != nil {
		return nil, err
	}
	go lock.heartbeat()
	return lock, nil
}

func (lock *RebaseTransactionLock) claim(reclaiming bool) error {
	file, err := os.OpenFile(lock.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		writeErr := lock.write(file, time.Now().UTC())
		if writeErr != nil {
			_ = os.Remove(lock.path)
			return writeErr
		}
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	if reclaiming {
		return fmt.Errorf("rebase transaction lock %s was taken by another process while it was being reclaimed", filepath.Base(lock.path))
	}
	record, readErr := readRebaseLockRecord(lock.path)
	if readErr != nil {
		return fmt.Errorf("rebase transaction is locked and its lock file is unreadable (%v); remove %s only if no migrate command is running", readErr, lock.path)
	}
	if !rebaseLockAbandoned(record) {
		return fmt.Errorf("rebase transaction is locked by %s pid %d running %q since %s; wait for it to finish or remove %s if that process is gone",
			record.Host, record.PID, record.Operation, record.AcquiredAt, lock.path)
	}
	if record.Nonce == "" {
		return fmt.Errorf("rebase transaction lock %s predates nonce ownership; remove it manually only if no migrate command is running", filepath.Base(lock.path))
	}
	// Move the exact abandoned generation aside before retrying O_EXCL. A
	// nonce mismatch after the rename means the path went through an ABA
	// replacement between inspection and reclaim; restore that holder instead
	// of deleting it.
	if err := quarantineAbandonedRebaseLock(lock.path, record, lock.nonce); err != nil {
		return err
	}
	return lock.claim(true)
}

func (lock *RebaseTransactionLock) write(file *os.File, now time.Time) error {
	record := rebaseLockRecord{
		PID: os.Getpid(), Host: rebaseLockHost(), Operation: lock.operation,
		Nonce:      lock.nonce,
		AcquiredAt: now.Format(time.RFC3339Nano), HeartbeatAt: now.Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(record)
	if err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// heartbeat keeps the lock file young while a long build, validation, or
// promotion runs. Without it a multi-hour build would look abandoned.
func (lock *RebaseTransactionLock) heartbeat() {
	defer close(lock.done)
	ticker := time.NewTicker(rebaseLockHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-lock.stop:
			return
		case now := <-ticker.C:
			lock.refresh(now.UTC())
		}
	}
}

func (lock *RebaseTransactionLock) refresh(now time.Time) {
	record, err := readRebaseLockRecord(lock.path)
	if err != nil || record.PID != os.Getpid() || record.Host != rebaseLockHost() || record.Nonce != lock.nonce {
		// Another holder owns the file now. Do not overwrite it: releasing is
		// still safe because Release re-checks ownership as well.
		return
	}
	record.HeartbeatAt = now.Format(time.RFC3339Nano)
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	_ = os.WriteFile(lock.path, append(data, '\n'), 0o600)
}

// Release removes the lock only when this process still owns it.
func (lock *RebaseTransactionLock) Release() error {
	if lock == nil {
		return nil
	}
	lock.stopped.Do(func() {
		close(lock.stop)
		<-lock.done
	})
	record, err := readRebaseLockRecord(lock.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.PID != os.Getpid() || record.Host != rebaseLockHost() || record.Nonce != lock.nonce {
		return fmt.Errorf("rebase transaction lock is no longer owned by this process")
	}
	if err := os.Remove(lock.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func newRebaseLockNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate rebase transaction lock nonce: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func quarantineAbandonedRebaseLock(path string, expected rebaseLockRecord, claimantNonce string) error {
	quarantine := path + ".stale-" + expected.Nonce + "-" + claimantNonce
	if err := os.Rename(path, quarantine); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rebase transaction lock changed while it was being reclaimed")
		}
		return err
	}
	moved, err := readRebaseLockRecord(quarantine)
	if err != nil || moved.Nonce != expected.Nonce {
		restoreErr := os.Rename(quarantine, path)
		if restoreErr != nil {
			return fmt.Errorf("rebase transaction lock changed during reclaim and could not be restored: read=%v restore=%v", err, restoreErr)
		}
		return fmt.Errorf("rebase transaction lock changed while it was being reclaimed")
	}
	if err := os.Remove(quarantine); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func readRebaseLockRecord(path string) (rebaseLockRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rebaseLockRecord{}, err
	}
	var record rebaseLockRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return rebaseLockRecord{}, err
	}
	return record, nil
}

func rebaseLockAbandoned(record rebaseLockRecord) bool {
	stamp := record.HeartbeatAt
	if stamp == "" {
		stamp = record.AcquiredAt
	}
	beat, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		// An unparsable timestamp cannot prove the holder is alive, but it also
		// cannot prove it is gone. Only reclaim when the recorded process is on
		// this host and demonstrably not running.
		return record.Host == rebaseLockHost() && !rebaseProcessAlive(record.PID)
	}
	if time.Since(beat) <= rebaseLockStaleAfter {
		return false
	}
	if record.Host != rebaseLockHost() {
		// A stale heartbeat from another machine is still not ours to clear:
		// the shared filesystem may simply be slow.
		return false
	}
	return !rebaseProcessAlive(record.PID)
}

func rebaseLockHost() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown-host"
	}
	return name
}

// rebaseProcessAlive is deliberately conservative: anything it cannot prove
// dead is reported as alive, so an uncertain probe never unlocks a running
// migration.
func rebaseProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		// Windows reports a missing process here; POSIX never fails.
		return false
	}
	defer func() { _ = process.Release() }()
	err = process.Signal(syscall.Signal(0))
	switch {
	case err == nil:
		return true
	case errors.Is(err, os.ErrProcessDone):
		return false
	default:
		// Permission errors and platforms without signal support leave the
		// question open; treat the holder as alive.
		return true
	}
}
