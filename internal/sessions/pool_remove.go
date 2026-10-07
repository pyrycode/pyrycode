package sessions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// JSONLPolicy controls how Pool.Remove handles a session's on-disk JSONL
// transcript file. The zero value (JSONLLeave) preserves the 1.1d-A1 (#94)
// behaviour: the JSONL is untouched.
type JSONLPolicy uint8

const (
	// JSONLLeave leaves the JSONL on disk untouched. Default (zero value).
	JSONLLeave JSONLPolicy = iota
	// JSONLArchive moves the JSONL to <pyry-data-dir>/archived-sessions/<uuid>.jsonl.
	JSONLArchive
	// JSONLPurge deletes the JSONL.
	JSONLPurge
)

// RemoveOptions extends Pool.Remove with disposition policy. The zero value
// behaves identically to the 1.1d-A1 (#94) Pool.Remove: terminate the child,
// drop the registry entry, leave the JSONL on disk.
type RemoveOptions struct {
	// JSONL selects the on-disk JSONL disposition policy. Zero value is
	// JSONLLeave.
	JSONL JSONLPolicy
}

// Remove terminates the named session's claude process (if running), drops
// its registry entry, and applies opts.JSONL to the on-disk transcript file.
//
// opts.JSONL == JSONLLeave (zero value): JSONL untouched (1.1d-A1 behaviour).
// opts.JSONL == JSONLArchive: mv <claudeSessionsDir>/<uuid>.jsonl into
//
//	<pyry-data-dir>/archived-sessions/<uuid>.jsonl. Subdir is auto-created.
//	Errors (wrapping fs.ErrExist) if the destination already exists.
//	Source-absent is a no-op.
//
// opts.JSONL == JSONLPurge: rm <claudeSessionsDir>/<uuid>.jsonl. Source-absent
//
//	is a no-op (per AC: intent is "ensure the file is gone").
//
// Returns ErrSessionNotFound for an unknown id, ErrCannotRemoveBootstrap
// for the bootstrap entry, or ctx.Err() if termination is cancelled. On
// any of those error paths, the in-memory pool, on-disk sessions.json,
// and the JSONL on disk are byte-identical to their prior state — the
// disposition policy is applied AFTER the registry-remove + persist
// completes successfully.
//
// Returns only after the child has exited (modulo ctx cancellation).
//
// Ordering — delete-then-dispose-then-evict. Pool.mu is taken (write) for
// the in-memory delete + saveLocked + JSONL disposition (single critical
// section, single observable transition), then released BEFORE calling
// sess.Evict. The alternative (holding p.mu across Evict) deadlocks:
// Session.transitionTo calls Pool.persist, which reacquires p.mu.
//
// On saveLocked failure, the in-memory delete is rolled back so the disk
// and memory remain consistent (mirrors Pool.Rename); JSONL is untouched
// and the child is not terminated. On disposition failure, the registry
// entry stays removed (already persisted), the disposition error is
// returned, and the child is still terminated via Evict. If both
// disposition and Evict fail, the disposition error wins (it is the new
// failure mode this signature introduces, and the more actionable one).
//
// Lifecycle goroutine after Remove: close(sess.removedCh) signals the
// session's Run loop to exit promptly. Once Evict drives active→evicted
// (or the session is already evicted), Run parks in runEvicted, observes
// the closed removedCh, and returns nil — never through the shared
// errgroup, so no other session or the relay leg is torn down. The
// goroutine and everything it captures are released at Remove time rather
// than surviving until pool shutdown (the bounded-cost note from #94 no
// longer applies).
func (p *Pool) Remove(ctx context.Context, id SessionID, opts RemoveOptions) error {
	p.mu.Lock()
	sess, ok := p.sessions[id]
	if !ok {
		entry, dormant := p.dormant[id]
		if dormant {
			if entry.Bootstrap {
				p.mu.Unlock()
				return ErrCannotRemoveBootstrap
			}
			delete(p.dormant, id)
			if err := p.saveLocked(); err != nil {
				p.dormant[id] = entry
				p.mu.Unlock()
				return err
			}
			disposeErr := p.disposeJSONLLocked(id, opts.JSONL)
			p.mu.Unlock()
			if p.registryPath != "" && ValidID(string(id)) {
				_ = os.Remove(filepath.Join(filepath.Dir(p.registryPath), "session-settings", string(id)+".json"))
				_ = os.Remove(filepath.Join(sessionPromptsDirFor(p.registryPath), string(id)+".txt"))
			}
			return disposeErr
		}
		p.mu.Unlock()
		return ErrSessionNotFound
	}
	if sess.bootstrap {
		p.mu.Unlock()
		return ErrCannotRemoveBootstrap
	}
	delete(p.sessions, id)
	// The other half of #2448's invariant, at the one site that deletes a live
	// session: a removal is final, so the id must not also be sitting in the
	// dormant map waiting to be written back on the next save. materialise
	// retired it when the session was registered, so this is a no-op on every
	// reachable path — it is here because the removal's finality is this
	// function's claim to make, not a property borrowed from a distant call site.
	// Nothing to roll back below: saveLocked writes a live id from its session
	// either way, so restoring p.sessions[id] restores the file unchanged.
	delete(p.dormant, id)
	if err := p.saveLocked(); err != nil {
		p.sessions[id] = sess
		p.mu.Unlock()
		return err
	}
	disposeErr := p.disposeJSONLLocked(id, opts.JSONL)
	p.mu.Unlock()

	// Signal the lifecycle goroutine to exit. Placed past the saveLocked
	// rollback branch (a rolled-back, still-registered session keeps its
	// goroutine) and off p.mu, next to the already-off-lock Evict call.
	// Closing a write-once channel needs no lock; the close is level-
	// triggered, so ordering it before Evict is safe — the removal is
	// observed the instant Run reaches runEvicted, whether Evict drives the
	// active→evicted transition or the session is already parked there.
	// Single-close is structural: Remove is single-shot per id (a second
	// Remove finds no p.sessions[id] under p.mu and returns before this).
	close(sess.removedCh)

	evictErr := sess.Evict(ctx)

	// Remove the per-session MCP-enable settings file (#943). Runs after Evict
	// returns — the child is confirmed dead, so no backoff respawn can race the
	// removal into re-reading a deleted path. Best-effort in the sense that a
	// failure does not fail the Remove, but no longer optional: since #1518 the
	// file lives in the daemon data dir, where no reaper collects it, so this
	// removal is what keeps the on-disk set bounded by live sessions.
	if sess.settingsPath != "" {
		_ = os.Remove(sess.settingsPath)
	}

	// And this session's appended system-prompt file, on the same terms (#2150).
	// #2093's daemon-scoped file was deliberately NOT removed here — it served
	// every other live session — but this one is this session's alone and carries
	// the operator's own text, so leaving it would leave that text in the data
	// dir past the conversation that owns it.
	if sess.systemPromptPath != "" {
		_ = os.Remove(sess.systemPromptPath)
	}

	if disposeErr != nil {
		return disposeErr
	}
	return evictErr
}

// dataDir returns the per-instance pyry data directory — the parent of the
// registry path. Empty when persistence is disabled (test-only mode).
func (p *Pool) dataDir() string {
	if p.registryPath == "" {
		return ""
	}
	return filepath.Dir(p.registryPath)
}

// disposeJSONLLocked applies policy to the named session's on-disk JSONL.
// Caller MUST hold p.mu (write).
//
// Returns nil for JSONLLeave or when claudeSessionsDir is empty (test or
// disabled JSONL plumbing). For JSONLArchive without a registryPath the
// data-dir is unresolvable and an error is returned — production callers
// always set RegistryPath; this guard exists so a misconfigured test fails
// loudly rather than archiving into a relative path.
//
// Source-absent is a success no-op for both archive and purge (intent is
// "move it if there is one" / "ensure it's gone"). Archive errors when the
// destination already exists; the error wraps fs.ErrExist so a future CLI
// can offer a --force UX with errors.Is.
func (p *Pool) disposeJSONLLocked(id SessionID, policy JSONLPolicy) error {
	if policy == JSONLLeave {
		return nil
	}
	if p.claudeSessionsDir == "" {
		return nil
	}
	src := filepath.Join(p.claudeSessionsDir, string(id)+".jsonl")
	switch policy {
	case JSONLPurge:
		err := os.Remove(src)
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	case JSONLArchive:
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("sessions: archive stat source: %w", err)
		}
		dataDir := p.dataDir()
		if dataDir == "" {
			return errors.New("sessions: archive requires a registry path")
		}
		archiveDir := filepath.Join(dataDir, "archived-sessions")
		dst := filepath.Join(archiveDir, string(id)+".jsonl")
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("sessions: archive destination exists: %s: %w", dst, fs.ErrExist)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("sessions: archive stat destination: %w", err)
		}
		if err := os.MkdirAll(archiveDir, 0o700); err != nil {
			return fmt.Errorf("sessions: archive mkdir: %w", err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("sessions: archive rename: %w", err)
		}
		return nil
	default:
		// Forward-compat: unknown future policy ≡ Leave.
		return nil
	}
}
