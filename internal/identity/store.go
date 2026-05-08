package identity

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreate returns the ServerID stored at path, generating and persisting
// a fresh one if path does not exist.
//
// The caller is responsible for resolving the absolute path (typically
// ~/.pyry/server-id from config); LoadOrCreate operates on absolute paths so
// tests can use t.TempDir().
//
// On first run (path does not exist), the parent directory is created with
// mode 0700 if missing, a fresh UUIDv4 is minted via NewServerID, written
// atomically (sibling temp file + rename) with mode 0600 and a single
// trailing newline, and returned.
//
// On subsequent runs (path exists), the file is read and validated via
// ParseServerID. A single trailing newline is tolerated; any other deviation
// from canonical UUIDv4 form returns an error matching ErrInvalidServerID
// via errors.Is. The file is NEVER overwritten on the existing-file path,
// even on validation failure — paired devices bind their tokens to a
// specific server-id, and silently regenerating would invalidate every
// pairing without operator awareness. Errors include the path for operator
// diagnostics; file contents are not included to avoid future log-leak risk.
//
// LoadOrCreate is not safe for concurrent use against the same path;
// bootstrap runs once on daemon startup before any goroutines fan out.
// Two pyry processes sharing a HOME directory is a misconfiguration
// outside this loader's contract.
func LoadOrCreate(path string) (ServerID, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		return parsePersisted(path, raw)
	case errors.Is(err, fs.ErrNotExist):
		return mintAndPersist(path)
	default:
		return "", fmt.Errorf("identity: read %s: %w", path, err)
	}
}

func parsePersisted(path string, raw []byte) (ServerID, error) {
	s := strings.TrimSuffix(string(raw), "\n")
	id, err := ParseServerID(s)
	if err != nil {
		return "", fmt.Errorf("identity: parse %s: %w", path, err)
	}
	return id, nil
}

func mintAndPersist(path string) (ServerID, error) {
	id := NewServerID()
	if err := writeServerID(path, id); err != nil {
		return "", err
	}
	return id, nil
}

func writeServerID(path string, id ServerID) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("identity: mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".server-id-*.tmp")
	if err != nil {
		return fmt.Errorf("identity: create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("identity: chmod temp: %w", err)
	}
	if _, err := f.Write([]byte(string(id) + "\n")); err != nil {
		_ = f.Close()
		return fmt.Errorf("identity: write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("identity: fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("identity: close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("identity: rename to %s: %w", path, err)
	}
	return nil
}
