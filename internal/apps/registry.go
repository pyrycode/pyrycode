package apps

import (
	"errors"
	"reflect"
	"sort"
	"sync"
)

const maxRevision uint64 = 9007199254740991
const maxApps = 256

var (
	ErrInvalidIdentity  = errors.New("apps: invalid identity")
	ErrInvalidStorage   = errors.New("apps: invalid storage")
	ErrConflict         = errors.New("apps: conflicting registration")
	ErrNotFound         = errors.New("apps: registration not found")
	ErrRemoved          = errors.New("apps: identity removed")
	ErrCapacity         = errors.New("apps: registration capacity reached")
	ErrExhausted        = errors.New("apps: revision exhausted")
	ErrInvalidLifecycle = errors.New("apps: invalid lifecycle")
	ErrStale            = errors.New("apps: obsolete observation")
)

// LastError holds a safe lifecycle failure, never process output or file paths.
type LastError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Record is a committed registration. Empty release strings mean unset.
// Manifest updates preserve all committed display and lifecycle fields.
type Record struct {
	AppID          string          `json:"app_id"`
	Manifest       Manifest        `json:"manifest"`
	Title          string          `json:"title"`
	Desired        string          `json:"desired"`
	State          string          `json:"state"`
	ActiveRelease  string          `json:"active_release,omitempty"`
	PendingRelease string          `json:"pending_release,omitempty"`
	LastError      *LastError      `json:"last_error,omitempty"`
	Revision       uint64          `json:"revision"`
	ReleaseHistory []Release       `json:"release_history,omitempty"`
	PublishJournal *PublishJournal `json:"publish_journal,omitempty"`
	// RetirementPending bars restoration and writers until all prior publication
	// writers are confirmed retired. It survives owner replacement.
	RetirementPending bool `json:"retirement_pending,omitempty"`
}

func (r Record) recoveryPending() bool { return r.RetirementPending || r.PublishJournal != nil }

// Registry serializes durable mutations and readers. Open one owner per root;
// concurrent independent owners or processes writing the same root are unsupported.
type Registry struct {
	mu        sync.Mutex
	root      string
	snapshot  snapshot
	persist   func(string, snapshot) error
	uncertain error // Post-rename durability failure bars writes until recovery.
	consumers map[*func(Change)]struct{}
	owner     *Owner
}

// List returns detached records in app_id order and their committed host revision.
func (r *Registry) List() ([]Record, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := cloneSnapshot(r.snapshot)
	return s.Records, s.Revision
}

// Register persists a caller-minted local identity; equal manifests are no-ops.
func (r *Registry) Register(data []byte) (bool, error) { return r.change("register", "", data, nil) }

// UpdateManifest replaces only the manifest of an existing matching identity.
func (r *Registry) UpdateManifest(appID string, data []byte) (bool, error) {
	return r.change("update", appID, data, nil)
}

// Remove tombstones the identity permanently, retaining all source/build/data files.
// An owner joins writers/recovery first; unresolved recovery rejects removal.
// Removing an existing tombstone again is a no-op.
func (r *Registry) Remove(appID string) (bool, error) {
	r.mu.Lock()
	o := r.owner
	r.mu.Unlock()
	if o != nil {
		return o.removeRegistration(appID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.snapshot.Records {
		if record.AppID == appID && record.recoveryPending() {
			return false, ErrConflict
		}
	}
	return r.changeLocked("remove", appID, nil, nil)
}
func (r *Registry) change(action, appID string, data []byte, update *lifecycleUpdate) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changeLocked(action, appID, data, update)
}

// changeLocked validates, persists and notifies with the registry mutex held.
func (r *Registry) changeLocked(action, appID string, data []byte, update *lifecycleUpdate) (bool, error) {
	if r.uncertain != nil {
		return false, r.uncertain
	}
	if update != nil {
		if update.ctx != nil && update.ctx.Err() != nil {
			return false, update.ctx.Err()
		}
		if !validID(update.serverID) {
			return false, ErrInvalidIdentity
		}
		if update.serverID != r.snapshot.ServerID {
			return false, ErrForeignHost
		}
	}
	var manifest Manifest
	if action == "register" || action == "update" {
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
	if update != nil && update.expected != 0 && update.expected != r.snapshot.Records[index].Revision {
		return false, ErrStale
	}
	if index >= 0 && (action == "register" || action == "update") {
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
	next := cloneSnapshot(r.snapshot)
	if update != nil {
		if update.metadata != nil {
			if e := update.metadata(&next.Records[index]); e != nil {
				return false, e
			}
			next.Records[index] = cloneRecord(next.Records[index])
		} else if update.observation == nil {
			applyLifecycle(&next.Records[index], update.fields)
		} else {
			record := &next.Records[index]
			o := update.observation
			if o.Desired != "" {
				record.Desired = o.Desired
			}
			if o.State != "" {
				record.State = o.State
				record.LastError = o.LastError
			}
			if update.expected == 0 && o.Desired == "available" {
				record.State = normalizedState(*record)
				record.LastError = nil
			}
			*record = cloneRecord(*record)
		}
		if e := validateSnapshot(r.root, next); e != nil {
			if update.metadata != nil {
				return false, ErrInvalidStorage
			}
			return false, ErrInvalidLifecycle
		}
		if reflect.DeepEqual(next.Records[index], r.snapshot.Records[index]) {
			update.result = cloneRecord(next.Records[index])
			return false, nil
		}
	}
	if r.snapshot.Revision == maxRevision {
		return false, ErrExhausted
	}
	next.Revision++
	switch action {
	case "register":
		next.Records = append(next.Records, Record{AppID: appID, Manifest: manifest, Title: manifest.Title(), Desired: "available", State: "stopped", Revision: next.Revision})
	case "update":
		next.Records[index].Manifest = manifest
		next.Records[index].Revision = next.Revision
	case "lifecycle":
		next.Records[index].Revision = next.Revision
	case "remove":
		next.Records = append(next.Records[:index], next.Records[index+1:]...)
		next.Tombstones = append(next.Tombstones, tombstone{AppID: appID, Revision: next.Revision})
	}
	sortSnapshot(&next)
	if e := r.persist(r.root, next); e != nil {
		if errors.Is(e, errSnapshotUncertain) {
			r.uncertain = e
		}
		return false, e
	}
	r.snapshot = next
	if update != nil {
		update.result = cloneRecord(next.Records[index])
	}
	r.notify(appID)
	return true, nil
}
func cloneSnapshot(s snapshot) snapshot {
	s.Records = append([]Record{}, s.Records...)
	s.Tombstones = append([]tombstone{}, s.Tombstones...)
	for i := range s.Records {
		s.Records[i] = cloneRecord(s.Records[i])
	}
	return s
}
func sortSnapshot(s *snapshot) {
	sort.Slice(s.Records, func(i, j int) bool { return s.Records[i].AppID < s.Records[j].AppID })
	sort.Slice(s.Tombstones, func(i, j int) bool { return s.Tombstones[i].AppID < s.Tombstones[j].AppID })
}
