package devices

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// registryFile is the on-disk envelope for ~/.pyry/<name>/devices.json. The
// envelope shape (rather than a bare top-level array) reserves room for
// future top-level fields (schema version, push-token registration metadata)
// without a wire break.
type registryFile struct {
	Devices []Device `json:"devices"`
}

// Registry is the in-memory device list, guarded by a mutex. Construct via
// Load (cold-start or warm-start from disk); persist via Save. All methods
// are safe for concurrent use.
type Registry struct {
	mu      sync.Mutex
	devices []Device
}

// Load reads path. A missing file returns an empty *Registry with no error
// (cold start). A zero-byte file returns an empty *Registry with no error.
// Malformed JSON returns a wrapped error and a nil *Registry.
//
// The returned *Registry is independent of the on-disk file: subsequent Save
// calls re-encode from the in-memory slice; the file may move or be deleted
// between Load and Save without affecting in-memory state.
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Registry{}, nil
		}
		return nil, fmt.Errorf("registry: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return &Registry{}, nil
	}
	var rf registryFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	return &Registry{devices: rf.Devices}, nil
}

// Save writes the registry atomically: temp file in filepath.Dir(path) at
// mode 0600, fsync, rename into place. Parent directory is created with mode
// 0700 if missing. Returns a wrapped error on any step failure; on failure
// the pre-existing target file (if any) is left untouched (rename is the
// commit point).
//
// Entries are sorted by PairedAt then Name before serialization to guarantee
// byte-identical output for the same logical content.
func (r *Registry) Save(path string) error {
	r.mu.Lock()
	snapshot := make([]Device, len(r.devices))
	copy(snapshot, r.devices)
	r.mu.Unlock()

	sort.SliceStable(snapshot, func(i, j int) bool {
		if !snapshot[i].PairedAt.Equal(snapshot[j].PairedAt) {
			return snapshot[i].PairedAt.Before(snapshot[j].PairedAt)
		}
		return snapshot[i].Name < snapshot[j].Name
	})

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("registry: mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".devices-*.json.tmp")
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
	if err := enc.Encode(&registryFile{Devices: snapshot}); err != nil {
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

// Add appends d to the in-memory list. Caller owns uniqueness — Add does not
// validate that d.Name or d.TokenHash is unique within the registry.
func (r *Registry) Add(d Device) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.devices = append(r.devices, d)
}

// Remove deletes the first device whose Name equals name. Returns true iff a
// device was removed; false if no entry matched. Comparison is byte-exact.
func (r *Registry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, d := range r.devices {
		if d.Name == name {
			r.devices = append(r.devices[:i], r.devices[i+1:]...)
			return true
		}
	}
	return false
}

// List returns a copy of the in-memory device list. Callers may mutate the
// returned slice and its elements without affecting registry state.
func (r *Registry) List() []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Device, len(r.devices))
	copy(out, r.devices)
	return out
}

// FindByTokenHash returns the device whose TokenHash equals hash, and true if
// one was found. Comparison is byte-exact; constant-time comparison is not
// required at the hash↔hash boundary (VerifyToken owns the plain↔hash
// boundary).
func (r *Registry) FindByTokenHash(hash string) (Device, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.devices {
		if d.TokenHash == hash {
			return d, true
		}
	}
	return Device{}, false
}
