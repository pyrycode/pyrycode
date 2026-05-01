package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// registryFile is the on-disk schema for ~/.pyry/<name>/sessions.json.
// Encoder/decoder is encoding/json with default lenient field handling — new
// per-session fields can be added in later phases without breaking old pyry.
type registryFile struct {
	Version  int             `json:"version"`
	Sessions []registryEntry `json:"sessions"`
}

type registryEntry struct {
	ID           SessionID `json:"id"`
	Label        string    `json:"label"`
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
	Bootstrap    bool      `json:"bootstrap,omitempty"`
}

// loadRegistry reads sessions.json from path. Returns (nil, nil) when the file
// is missing or empty — this is the cold-start signal that triggers fresh
// bootstrap generation. A malformed file is a hard error (operator must fix
// or remove).
func loadRegistry(path string) (*registryFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("registry: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var reg registryFile
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	return &reg, nil
}

// saveRegistryLocked writes the registry atomically: temp file in the same
// directory, fsync, rename into place. Caller MUST hold Pool.mu (write).
// MkdirAll on the parent directory is performed inside this function.
//
// os.Rename on the same filesystem is atomic on Linux and macOS — this is
// what guarantees that a SIGKILL during the write leaves the on-disk file as
// either the pre-update or post-update version, never partial JSON.
func saveRegistryLocked(path string, reg *registryFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("registry: mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".sessions-*.json.tmp")
	if err != nil {
		return fmt.Errorf("registry: create temp: %w", err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("registry: chmod temp: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(reg); err != nil {
		_ = f.Close()
		return fmt.Errorf("registry: encode: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("registry: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("registry: close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("registry: rename: %w", err)
	}
	return nil
}

// pickBootstrap returns the entry marked bootstrap=true, or nil if none.
// Tolerates a registry that contains entries we haven't materialized in 1.2a
// (e.g. a 1.1-written file with multiple sessions): we still find and use the
// bootstrap entry.
func pickBootstrap(reg *registryFile) *registryEntry {
	if reg == nil {
		return nil
	}
	for i := range reg.Sessions {
		if reg.Sessions[i].Bootstrap {
			return &reg.Sessions[i]
		}
	}
	return nil
}

// sortEntriesByCreatedAt sorts entries by CreatedAt then ID, giving the disk
// file a deterministic byte-content shape that does not depend on Go's
// randomized map iteration order. Required for the AC's idempotent-reload
// guarantee.
func sortEntriesByCreatedAt(entries []registryEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].CreatedAt.Before(entries[j].CreatedAt)
		}
		return entries[i].ID < entries[j].ID
	})
}
