package keys

import (
	"crypto/ecdh"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	schemaVersion = 1
	algorithmName = "Noise_25519"
	filename      = "static_key.json"
)

// onDiskKey is the JSON envelope persisted at <baseDir>/<daemonName>/static_key.json.
// Field names and shapes are locked to docs/protocol-mobile.md § Static keys — binary side.
type onDiskKey struct {
	Version    int       `json:"version"`
	Algorithm  string    `json:"algorithm"`
	PrivateKey string    `json:"private_key"`
	PublicKey  string    `json:"public_key"`
	CreatedAt  time.Time `json:"created_at"`
}

// LoadOrCreate returns the StaticKey stored at <baseDir>/<daemonName>/static_key.json,
// generating and persisting a fresh keypair if the file does not exist.
//
// baseDir is typically <home>/.pyry; daemonName is the operator-supplied
// per-daemon label. daemonName is validated against the package's allowlist
// before any filesystem access — on rejection LoadOrCreate returns
// ErrInvalidDaemonName (wrapped) and does not touch the filesystem. The
// package does NOT validate baseDir; callers resolve home and trust the
// resulting parent path (the allowlist defends against daemonName injection,
// not against an attacker-controlled baseDir).
//
// On first run: parent directory created with mode 0700 if absent; keypair
// minted from crypto/rand via crypto/ecdh.X25519().GenerateKey; encoded as
// JSON per docs/protocol-mobile.md § Static keys — binary side; written
// atomically (sibling temp file in the parent dir → chmod 0600 → encode →
// Sync → Close → Rename).
//
// On subsequent runs: the file is read and decoded. Schema-version and
// algorithm constants are checked, base64 fields are decoded to exactly 32
// bytes, and the stored public_key is verified against the public point
// recomputed from private_key. Any mismatch returns ErrCorruptKeyFile
// wrapped with the file path; the error message NEVER contains the file
// contents. The file is NEVER overwritten on the existing-file path, even
// on validation failure — keys are bound to every paired device and silent
// regeneration would invalidate every pairing without operator awareness.
//
// LoadOrCreate is not safe for concurrent use against the same path;
// bootstrap runs once on daemon startup before any goroutines fan out.
//
// Before any read or write, the parent directory is required to exist
// with a mode that has no group/other bits set (ErrInsecureKeyDirMode
// otherwise); on the read path the file is required to be exactly mode
// 0600 (ErrInsecureKeyFileMode otherwise) and is opened with O_NOFOLLOW
// so a between-stat-and-open symlink swap cannot redirect the load.
func LoadOrCreate(baseDir, daemonName string) (*StaticKey, error) {
	if !validDaemonName(daemonName) {
		return nil, fmt.Errorf("keys: invalid daemon name %q: %w", daemonName, ErrInvalidDaemonName)
	}
	dir := filepath.Join(baseDir, daemonName)
	path := filepath.Join(dir, filename)

	if err := ensureSecureKeyDir(dir); err != nil {
		return nil, err
	}

	fi, err := os.Lstat(path)
	switch {
	case err == nil:
		if mode := fi.Mode().Perm(); mode != 0o600 {
			return nil, fmt.Errorf("keys: %s: mode %#o: %w", path, mode, ErrInsecureKeyFileMode)
		}
		raw, err := openSecureKeyFile(path)
		if err != nil {
			return nil, err
		}
		return parsePersisted(path, raw)
	case errors.Is(err, fs.ErrNotExist):
		return mintAndPersist(dir, path)
	default:
		return nil, fmt.Errorf("keys: stat %s: %w", path, err)
	}
}

// ensureSecureKeyDir guarantees that dir exists, is a directory, and has
// no group/other permission bits set. If dir is missing it is created at
// mode 0700 and re-stat'd to confirm the umask did not narrow (or some
// exotic filesystem widen) the requested mode. On any group/other bit set,
// or when the path exists but is not a directory, the function returns a
// wrapped ErrInsecureKeyDirMode naming the path and the observed mode in
// octal. No auto-chmod.
func ensureSecureKeyDir(dir string) error {
	fi, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			return fmt.Errorf("keys: mkdir %s: %w", dir, mkErr)
		}
		fi, err = os.Stat(dir)
		if err != nil {
			return fmt.Errorf("keys: re-stat %s: %w", dir, err)
		}
	} else if err != nil {
		return fmt.Errorf("keys: stat %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("keys: %s is not a directory: %w", dir, ErrInsecureKeyDirMode)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("keys: %s: mode %#o: %w", dir, mode, ErrInsecureKeyDirMode)
	}
	return nil
}

// openSecureKeyFile reads path with O_NOFOLLOW so a symlink swap between
// the caller's mode check and this open cannot redirect to attacker-
// controlled bytes. On ELOOP (or any open/read error) the wrapped error
// names only the caller-supplied path — the resolved link target is
// deliberately not included so a hostile symlink cannot use the error
// message as an exfiltration channel.
func openSecureKeyFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("keys: open %s: %w", path, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("keys: read %s: %w", path, err)
	}
	return raw, nil
}

func parsePersisted(path string, raw []byte) (*StaticKey, error) {
	var d onDiskKey
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("keys: parse %s: %w", path, ErrCorruptKeyFile)
	}
	if d.Version != schemaVersion {
		return nil, fmt.Errorf("keys: %s: unsupported schema version: %w", path, ErrCorruptKeyFile)
	}
	if d.Algorithm != algorithmName {
		return nil, fmt.Errorf("keys: %s: unsupported algorithm: %w", path, ErrCorruptKeyFile)
	}
	privBytes, err := base64.StdEncoding.DecodeString(d.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("keys: %s: private_key base64 decode failed: %w", path, ErrCorruptKeyFile)
	}
	if len(privBytes) != 32 {
		return nil, fmt.Errorf("keys: %s: private_key wrong length: %w", path, ErrCorruptKeyFile)
	}
	pubBytes, err := base64.StdEncoding.DecodeString(d.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("keys: %s: public_key base64 decode failed: %w", path, ErrCorruptKeyFile)
	}
	if len(pubBytes) != 32 {
		return nil, fmt.Errorf("keys: %s: public_key wrong length: %w", path, ErrCorruptKeyFile)
	}
	if d.CreatedAt.IsZero() {
		return nil, fmt.Errorf("keys: %s: missing created_at: %w", path, ErrCorruptKeyFile)
	}
	priv, err := ecdh.X25519().NewPrivateKey(privBytes)
	if err != nil {
		return nil, fmt.Errorf("keys: %s: private_key invalid: %w", path, ErrCorruptKeyFile)
	}
	derivedPub := priv.PublicKey().Bytes()
	if subtle.ConstantTimeCompare(derivedPub, pubBytes) != 1 {
		return nil, fmt.Errorf("keys: %s: public_key does not match private_key: %w", path, ErrCorruptKeyFile)
	}
	var sk StaticKey
	copy(sk.priv[:], privBytes)
	copy(sk.pub[:], pubBytes)
	return &sk, nil
}

func mintAndPersist(dir, path string) (*StaticKey, error) {
	sk := newStaticKey()
	if err := writeStaticKey(dir, path, sk); err != nil {
		return nil, err
	}
	return sk, nil
}

// writeStaticKey persists sk atomically: parent dir created at 0700 if
// absent, temp file in the same dir chmod'd to 0600 before write to defeat
// umask, JSON-encoded, fsync'd, closed, then renamed into place. The rename
// is the commit point; on any earlier failure the partial state lives only
// in the temp file and the defer cleans it up.
func writeStaticKey(dir, path string, sk *StaticKey) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("keys: mkdir %s: %w", dir, err)
	}
	d := onDiskKey{
		Version:    schemaVersion,
		Algorithm:  algorithmName,
		PrivateKey: base64.StdEncoding.EncodeToString(sk.priv[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(sk.pub[:]),
		CreatedAt:  time.Now().UTC(),
	}
	body, err := json.Marshal(&d)
	if err != nil {
		return fmt.Errorf("keys: encode: %w", err)
	}
	f, err := os.CreateTemp(dir, ".static-key-*.tmp")
	if err != nil {
		return fmt.Errorf("keys: create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("keys: chmod temp: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("keys: write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("keys: fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("keys: close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("keys: rename to %s: %w", path, err)
	}
	return nil
}
