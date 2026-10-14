package apps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

type tombstone struct {
	AppID    string `json:"app_id"`
	Revision uint64 `json:"revision"`
}
type snapshot struct {
	ServerID   string      `json:"server_id"`
	Revision   uint64      `json:"revision"`
	Records    []Record    `json:"records"`
	Tombstones []tombstone `json:"tombstones"`
}

// Open accepts a canonical host identity and absolute private app root.
// Missing storage is empty; invalid storage is never repaired or rewritten.
// Observed states are normalized and committed before exposure; stored running
// never proves readiness. Intent, releases and safe error metadata are preserved.
func Open(root, serverID string) (*Registry, error) {
	return openRegistry(root, serverID, writeSnapshot)
}

func openRegistry(root, serverID string, persist func(string, snapshot) error) (*Registry, error) {
	if !validID(serverID) {
		return nil, ErrInvalidIdentity
	}
	if !filepath.IsAbs(root) {
		return nil, ErrInvalidStorage
	}
	var s snapshot
	data, e := os.ReadFile(filepath.Join(root, "registry.json"))
	if e != nil && !errors.Is(e, fs.ErrNotExist) {
		return nil, fmt.Errorf("apps: read registry: %w", e)
	}
	if e == nil {
		value, e := strictJSON(data)
		if e != nil || !validStorageSchema(value) {
			return nil, ErrInvalidStorage
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if e := d.Decode(&s); e != nil {
			return nil, ErrInvalidStorage
		}
		if !validID(s.ServerID) {
			return nil, ErrInvalidStorage
		}
		if s.ServerID != serverID {
			return nil, ErrForeignHost
		}
		if e := validateSnapshot(root, s); e != nil {
			return nil, e
		}
		sortSnapshot(&s)
	} else {
		s = snapshot{ServerID: serverID, Records: []Record{}, Tombstones: []tombstone{}}
	}
	if e := normalizeSnapshot(root, &s, persist); e != nil {
		return nil, e
	}
	return &Registry{root: root, snapshot: s, persist: persist}, nil
}

func validStorageSchema(value any) bool {
	fields, ok := storageObject(value, []string{"server_id", "revision", "records", "tombstones"})
	if !ok {
		return false
	}
	records, ok := fields["records"].([]any)
	if !ok {
		return false
	}
	dead, ok := fields["tombstones"].([]any)
	if !ok {
		return false
	}
	for _, value := range records {
		record, ok := storageObject(value, []string{"app_id", "manifest", "title", "desired", "state", "revision"}, "active_release", "pending_release", "last_error", "release_history", "publish_journal", "retirement_pending")
		if !ok || !validMetadataSchema(record) {
			return false
		}
		if pending, exists := record["retirement_pending"]; exists {
			if _, ok := pending.(bool); !ok {
				return false
			}
		}
		if lastError, exists := record["last_error"]; exists {
			if _, ok := storageObject(lastError, []string{"code", "message"}); !ok {
				return false
			}
		}
	}
	for _, value := range dead {
		if _, ok := storageObject(value, []string{"app_id", "revision"}); !ok {
			return false
		}
	}
	return true
}

// storageObject requires exact keys before a struct decoder can fold their case.
func storageObject(value any, required []string, optional ...string) (map[string]any, bool) {
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	for _, key := range required {
		if _, exists := fields[key]; !exists {
			return nil, false
		}
	}
	for key := range fields {
		if !slices.Contains(required, key) && !slices.Contains(optional, key) {
			return nil, false
		}
	}
	return fields, true
}

func validateSnapshot(root string, s snapshot) error {
	if s.Revision > maxRevision || len(s.Records) > maxApps {
		return ErrInvalidStorage
	}
	seen := map[string]bool{}
	check := func(id string, rev uint64) bool {
		if !validID(id) || seen[id] || rev < 1 || rev > s.Revision {
			return false
		}
		seen[id] = true
		return true
	}
	for _, r := range s.Records {
		if !check(r.AppID, r.Revision) || r.Manifest.AppID() != r.AppID || r.Manifest.ServerID() != s.ServerID || !validText(r.Title, 128, true) {
			return ErrInvalidStorage
		}
		if r.Desired != "available" && r.Desired != "stopped" {
			return ErrInvalidStorage
		}
		switch r.State {
		case "starting", "running", "stopped", "failed":
		default:
			return ErrInvalidStorage
		}
		if (r.ActiveRelease != "" && !validRelease(r.ActiveRelease)) || (r.PendingRelease != "" && !validRelease(r.PendingRelease)) || (r.State == "running" && r.ActiveRelease == "") {
			return ErrInvalidStorage
		}
		if !validMetadata(root, s.ServerID, r) {
			return ErrInvalidStorage
		}
		if r.LastError != nil && (!validErrorCode(r.LastError.Code) || !validText(r.LastError.Message, 160, true)) {
			return ErrInvalidStorage
		}
	}
	for _, dead := range s.Tombstones {
		if !check(dead.AppID, dead.Revision) {
			return ErrInvalidStorage
		}
	}
	return nil
}
func validErrorCode(code string) bool {
	switch code {
	case "protocol.unsupported", "app.invalid_request", "app.not_found", "app.release_changed", "app.unavailable", "app.limit_exceeded", "app.busy", "app.timeout", "app.io_failed", "apps.changed":
		return true
	}
	return false
}

var errSnapshotUncertain = errors.New("apps: registry durability uncertain")

type snapshotOps struct {
	sync   func(*os.File) error
	rename func(string, string) error
}

func (o snapshotOps) syncFile(f *os.File) error {
	if o.sync != nil {
		return o.sync(f)
	}
	return f.Sync()
}

func writeSnapshot(root string, s snapshot) error {
	return (snapshotOps{}).write(root, s)
}

func (o snapshotOps) write(root string, s snapshot) error {
	if e := os.MkdirAll(root, 0700); e != nil {
		return fmt.Errorf("apps: create root: %w", e)
	}
	if e := os.Chmod(root, 0700); e != nil {
		return fmt.Errorf("apps: protect root: %w", e)
	}
	dir, e := privateDir(root)
	if e != nil {
		return fmt.Errorf("apps: open registry directory: %w", e)
	}
	defer dir.Close()
	f, e := os.CreateTemp(root, ".registry-*.tmp")
	if e != nil {
		return fmt.Errorf("apps: create temporary registry: %w", e)
	}
	// Cleanup is best effort; the committed registry never depends on the temp name.
	defer func() { _ = os.Remove(f.Name()) }()
	if e = f.Chmod(0600); e == nil {
		e = json.NewEncoder(f).Encode(s)
	}
	if e == nil {
		e = o.syncFile(f)
	}
	closeErr := f.Close()
	if e != nil {
		return fmt.Errorf("apps: write registry: %w", e)
	}
	if closeErr != nil {
		return fmt.Errorf("apps: close registry: %w", closeErr)
	}
	rename := o.rename
	if rename == nil {
		rename = os.Rename
	}
	if e = rename(f.Name(), filepath.Join(root, "registry.json")); e != nil {
		return fmt.Errorf("apps: commit registry: %w", e)
	}
	// A failed directory sync cannot prove whether the rename survives a crash.
	// The caller must retain recovery evidence and forbid subsequent overwrites.
	if e = o.syncFile(dir); e != nil {
		return errors.Join(errSnapshotUncertain, fmt.Errorf("apps: sync registry directory: %w", e))
	}
	return nil
}
