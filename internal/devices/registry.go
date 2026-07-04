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

// readDevicesFile reads and decodes the on-disk device slice at path. A
// missing file (ENOENT) or a zero-byte file returns (nil, nil) — the
// cold-start / empty contract. Malformed JSON or any other read error
// returns (nil, wrapped error). Shared by Load (which constructs a fresh
// registry) and Reload (which reconciles into an existing one).
//
// SECURITY: the returned error wraps path only, never the file bytes — a
// corrupt devices.json may embed a token_hash, so echoing its contents into
// an error (and thence a log) would leak it.
func readDevicesFile(path string) ([]Device, error) {
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
	var rf registryFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	return rf.Devices, nil
}

// Load reads path. A missing file returns an empty *Registry with no error
// (cold start). A zero-byte file returns an empty *Registry with no error.
// Malformed JSON returns a wrapped error and a nil *Registry.
//
// The returned *Registry is independent of the on-disk file: subsequent Save
// calls re-encode from the in-memory slice; the file may move or be deleted
// between Load and Save without affecting in-memory state.
func Load(path string) (*Registry, error) {
	devs, err := readDevicesFile(path)
	if err != nil {
		return nil, err
	}
	return &Registry{devices: devs}, nil
}

// Reload reconciles the on-disk device set at path INTO the in-memory
// registry under r.mu, disk being authoritative for membership. It lets a
// device paired via `pyry pair` after daemon startup authenticate on its
// next handshake without a restart, and reflects a `pyry pair revoke` the
// same way (#782).
//
// Reconciliation is keyed on TokenHash: a device present in BOTH memory and
// disk keeps its in-memory struct — preserving the LastSeenAt bumps Validate
// makes (never persisted) and any in-flight push registration; the daemon is
// the sole writer of those fields, so memory is >= disk for them, and
// `pyry pair` has no verb that edits an existing device's fields under a
// stable TokenHash. A device only on disk is adopted (newly paired); a device
// only in memory is dropped (revoked). Membership after Reload == disk's exact
// set, so the accept set is never widened beyond what is on disk.
//
// A missing (ENOENT) or zero-byte file reconciles membership to empty (no
// error) — disk is authoritative and says none. Malformed JSON or any other
// read error returns a wrapped error and leaves the in-memory set UNCHANGED
// (fail closed): callers proceed to Validate against the retained set, so no
// loaded device is lost and no unpaired token becomes acceptable.
//
// Lock discipline mirrors Save: the disk read happens OUTSIDE r.mu (I/O off
// the lock); only the reconcile-and-assign takes r.mu. Reload never nests
// locks and never calls back into a locked path, so concurrent
// Reload/Validate/Save from different goroutines serialise safely at r.mu.
//
// SECURITY: Reload performs no logging and no token/hash/name handling; the
// only error it returns is readDevicesFile's path-only wrapped error. It runs
// BEFORE Validate, never inside it — Validate's contract is untouched.
func (r *Registry) Reload(path string) error {
	disk, err := readDevicesFile(path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.devices = reconcileDevices(r.devices, disk)
	return nil
}

// reconcileDevices returns disk's membership, substituting the in-memory
// struct for any disk device whose TokenHash also appears in memory (the
// survivor that carries the daemon's un-persisted LastSeenAt / push-
// registration mutations). Disk order is preserved — irrelevant, since Save
// re-sorts on write and lookups scan linearly. Pure: no locking, no I/O.
func reconcileDevices(memory, disk []Device) []Device {
	byHash := make(map[string]Device, len(memory))
	for _, d := range memory {
		byHash[d.TokenHash] = d
	}
	out := make([]Device, 0, len(disk))
	for _, d := range disk {
		if mem, ok := byHash[d.TokenHash]; ok {
			out = append(out, mem)
		} else {
			out = append(out, d)
		}
	}
	return out
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

// UpdatePushRegistration sets Platform, PushToken, and Name on the device
// whose TokenHash equals tokenHash. Returns true iff a matching device was
// found and mutated. Caller is responsible for persisting via Save.
//
// The Name overwrite is intentional: the protocol's
// register_push_token.device_name is the phone's current self-reported name,
// and pyrycode treats the phone as the source of truth (a user renaming
// their device in iOS Settings should propagate). The original pairing-time
// Name carries no protocol-level invariants.
//
// Concurrency: serialized under Registry.mu; safe to call from any goroutine.
func (r *Registry) UpdatePushRegistration(tokenHash, platform, pushToken, name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.devices {
		if r.devices[i].TokenHash == tokenHash {
			r.devices[i].Platform = platform
			r.devices[i].PushToken = pushToken
			r.devices[i].Name = name
			return true
		}
	}
	return false
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
