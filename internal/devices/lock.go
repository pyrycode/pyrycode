package devices

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// DefaultLockWait is the bounded acquisition wait for callers with no reason to
// pick their own. A caller on a request path (the daemon's push-token handler)
// should choose a tighter bound rather than inherit this one.
const DefaultLockWait = 5 * time.Second

// lockPollInterval is how long a blocked acquirer sleeps between non-blocking
// flock attempts. Nothing in the design is sensitive to the exact value: it is
// small enough that a millisecond-scale wait still gets several attempts, and
// coarse enough that DefaultLockWait costs a few hundred cheap syscalls.
const lockPollInterval = 10 * time.Millisecond

// ErrLockBusy reports that the lock was not acquired within the caller's wait.
// Callers match it with errors.Is and map it to their own refusal surface —
// wire-code mapping belongs to the consumer, not to this primitive.
var ErrLockBusy = errors.New("devices: lock busy")

// WithLock runs fn while holding an exclusive lock, derived from devicesPath,
// that excludes other OS processes. The lock is acquired before fn runs and
// released after it returns, so a caller's whole read-modify-write — Load, the
// mutation, and Save's rename — is one critical section. `pyry pair` and the
// daemon are separate processes, so Registry.mu cannot serialize them and a
// lost update silently erases whichever writer commits first.
//
// fn's error is returned verbatim, never wrapped, so a caller's errors.Is
// against its own sentinels keeps working. Every other error class is this
// package's own: an acquisition that exhausts wait returns ErrLockBusy and
// never runs fn.
//
// fn runs with the file lock held. It MUST NOT call WithLock again on the same
// path: a nested call opens a second file description, and flock(2) contends
// per description rather than per process, so the inner call would contend with
// its own caller and burn wait before returning ErrLockBusy. WithLock is the
// sole acquirer in this package — Load, Save, and Reload do not take the lock,
// which is what keeps the file-lock-then-Registry.mu ordering the only one
// reachable. fn SHOULD keep the region short: peers wait on it.
//
// The lock lives on a sibling file, devicesPath + ".lock", and never on
// devices.json itself. A flock attaches to the open file description, i.e. to
// the inode, and Save commits by renaming a temp file over the target, which
// installs a new inode at that path; a second acquirer opening devices.json
// after that rename would get the new inode and acquire its own independent
// lock while the first holder still believed it excluded everyone. Mutual
// exclusion would be broken by the exact operation the lock exists to protect.
// The sidecar's inode is never replaced. It is created on demand and never
// deleted — unlinking it races acquisition, since a holder can be unlinked out
// from under a waiter and both then proceed. A leftover zero-byte lock file is
// the intended steady state.
//
// The lock fd is never written to; the file's only role is to own a stable
// inode, and O_RDWR is the conventional flock open mode rather than a need.
// Do not stash the holder's pid or a timestamp in it for debugging: that both
// races (a waiter that has just acquired would truncate under a reader) and
// puts an information surface next to a credential store.
//
// Deriving the lock path from devicesPath also isolates instances — two
// -pyry-name values yield two sidecars, which never contend.
//
// The kernel releases a flock when the fd closes, including on process exit or
// SIGKILL, so there is no stale lock to clean up and no lock-file TTL. A
// process killed mid-region leaves devices.json either pre- or post-rename,
// never partial, because Save is already atomic.
//
// SECURITY: every error returned here names the lock path and nothing else,
// inheriting the rule stated on readDevicesFile — a corrupt devices.json can
// carry a token_hash in a decode error, so no error or log surface next to this
// file may echo its bytes. WithLock never opens, reads, or decodes the registry
// itself, so the leak is structurally out of reach here; the rule is stated
// because callers add error paths that do sit beside decoded data.
func WithLock(devicesPath string, wait time.Duration, fn func() error) error {
	lockPath := devicesPath + ".lock"

	// Acquisition precedes the caller's on-disk read, so on a first-ever
	// `pyry pair` the instance directory does not exist yet — it is created
	// later by identity.LoadOrCreate and by Save's own mkdir. Same mode as
	// Save uses, against the same directory.
	dir := filepath.Dir(lockPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("devices: mkdir %s: %w", dir, err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("devices: open lock %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()

	// The first attempt is unconditional, so an uncontended acquire costs no
	// sleep and wait <= 0 still gets one try.
	fd := int(f.Fd())
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		// EWOULDBLOCK == EAGAIN on darwin and linux alike, so one check
		// covers both spellings. Any other errno is terminal.
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("devices: flock %s: %w", lockPath, err)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("devices: acquire lock %s: %w", lockPath, ErrLockBusy)
		}
		time.Sleep(lockPollInterval)
	}
	// Defers are LIFO, so the unlock runs before the close above. Close alone
	// would release the lock; the explicit unlock states the intent.
	defer func() { _ = syscall.Flock(fd, syscall.LOCK_UN) }()

	return fn()
}
