package apps

import (
	"errors"
	"sort"
	"sync"
)

const maxRevision uint64 = 9007199254740991
const maxApps = 256

var (
	ErrInvalidIdentity = errors.New("apps: invalid identity")
	ErrInvalidStorage  = errors.New("apps: invalid storage")
	ErrConflict        = errors.New("apps: conflicting registration")
	ErrNotFound        = errors.New("apps: registration not found")
	ErrRemoved         = errors.New("apps: identity removed")
	ErrCapacity        = errors.New("apps: registration capacity reached")
	ErrExhausted       = errors.New("apps: revision exhausted")
)

// LastError holds a safe lifecycle failure, never process output or file paths.
type LastError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Record is a committed registration. Empty release strings mean unset.
// Manifest updates preserve all committed display and lifecycle fields.
type Record struct {
	AppID          string     `json:"app_id"`
	Manifest       Manifest   `json:"manifest"`
	Title          string     `json:"title"`
	Desired        string     `json:"desired"`
	State          string     `json:"state"`
	ActiveRelease  string     `json:"active_release,omitempty"`
	PendingRelease string     `json:"pending_release,omitempty"`
	LastError      *LastError `json:"last_error,omitempty"`
	Revision       uint64     `json:"revision"`
}

// Registry serializes durable mutations and readers. Open one owner per root;
// concurrent independent owners or processes writing the same root are unsupported.
type Registry struct {
	mu       sync.Mutex
	root     string
	snapshot snapshot
	persist  func(string, snapshot) error
}

// List returns detached records in app_id order and their committed host revision.
func (r *Registry) List() ([]Record, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := cloneSnapshot(r.snapshot)
	return s.Records, s.Revision
}

// Register persists a caller-minted local identity; equal manifests are no-ops.
func (r *Registry) Register(data []byte) (bool, error) { return r.change("register", "", data) }

// UpdateManifest replaces only the manifest of an existing matching identity.
func (r *Registry) UpdateManifest(appID string, data []byte) (bool, error) {
	return r.change("update", appID, data)
}

// Remove tombstones the identity permanently, retaining all source/build/data files.
// Removing an existing tombstone again is a no-op.
func (r *Registry) Remove(appID string) (bool, error) { return r.change("remove", appID, nil) }
func (r *Registry) change(action, appID string, data []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var manifest Manifest
	if action != "remove" {
		m, e := ValidateManifest(data, r.snapshot.ServerID)
		if e != nil {
			return false, e
		}
		manifest = m
		if action == "update" && appID != m.AppID() {
			return false, ErrInvalidIdentity
		}
		appID = m.AppID()
	}
	if !validID(appID) {
		return false, ErrInvalidIdentity
	}
	for _, dead := range r.snapshot.Tombstones {
		if dead.AppID == appID {
			if action == "remove" {
				return false, nil
			}
			return false, ErrRemoved
		}
	}
	index := -1
	for i, record := range r.snapshot.Records {
		if record.AppID == appID {
			index = i
			break
		}
	}
	if index < 0 && action != "register" {
		return false, ErrNotFound
	}
	if index >= 0 && action != "remove" {
		if r.snapshot.Records[index].Manifest == manifest {
			return false, nil
		}
		if action == "register" {
			return false, ErrConflict
		}
	}
	if index < 0 && len(r.snapshot.Records) >= maxApps {
		return false, ErrCapacity
	}
	if r.snapshot.Revision == maxRevision {
		return false, ErrExhausted
	}
	next := cloneSnapshot(r.snapshot)
	next.Revision++
	switch action {
	case "register":
		next.Records = append(next.Records, Record{AppID: appID, Manifest: manifest, Title: manifest.Title(), Desired: "available", State: "stopped", Revision: next.Revision})
	case "update":
		next.Records[index].Manifest = manifest
		next.Records[index].Revision = next.Revision
	case "remove":
		next.Records = append(next.Records[:index], next.Records[index+1:]...)
		next.Tombstones = append(next.Tombstones, tombstone{AppID: appID, Revision: next.Revision})
	}
	sortSnapshot(&next)
	if e := r.persist(r.root, next); e != nil {
		return false, e
	}
	r.snapshot = next
	return true, nil
}
func cloneSnapshot(s snapshot) snapshot {
	s.Records = append([]Record{}, s.Records...)
	s.Tombstones = append([]tombstone{}, s.Tombstones...)
	for i := range s.Records {
		if s.Records[i].LastError != nil {
			copy := *s.Records[i].LastError
			s.Records[i].LastError = &copy
		}
	}
	return s
}
func sortSnapshot(s *snapshot) {
	sort.Slice(s.Records, func(i, j int) bool { return s.Records[i].AppID < s.Records[j].AppID })
	sort.Slice(s.Tombstones, func(i, j int) bool { return s.Tombstones[i].AppID < s.Tombstones[j].AppID })
}
