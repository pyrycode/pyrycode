package conversations

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// registryFile is the on-disk envelope for ~/.pyry/conversations.json. The
// envelope shape (rather than a bare top-level array) reserves room for
// future top-level fields without a wire break.
type registryFile struct {
	Conversations []Conversation `json:"conversations"`
}

// Sentinel errors returned by Promote. Callers (CLI, wire-protocol layer)
// distinguish refusal cases via errors.Is and map to user-facing codes.
var (
	ErrConversationNotFound        = errors.New("conversations: conversation not found")
	ErrConversationAlreadyPromoted = errors.New("conversations: conversation already promoted")
	ErrPromotionNameInUse          = errors.New("conversations: promotion name already in use")
	ErrPromotionNameEmpty          = errors.New("conversations: promotion name is empty")
)

// Registry is the in-memory conversation list, guarded by a mutex. Construct
// via Load (cold-start or warm-start from disk); persist via Save. All methods
// are safe for concurrent use.
type Registry struct {
	mu            sync.Mutex
	conversations []Conversation
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
	return &Registry{conversations: rf.Conversations}, nil
}

// Save writes the registry atomically: temp file in filepath.Dir(path) at
// mode 0600, fsync, rename into place. Parent directory is created with mode
// 0700 if missing. Returns a wrapped error on any step failure; on failure
// the pre-existing target file (if any) is left untouched (rename is the
// commit point).
//
// Entries are sorted by LastUsedAt then ID before serialization to guarantee
// byte-identical output for the same logical content.
func (r *Registry) Save(path string) error {
	r.mu.Lock()
	snapshot := make([]Conversation, len(r.conversations))
	copy(snapshot, r.conversations)
	r.mu.Unlock()

	sort.SliceStable(snapshot, func(i, j int) bool {
		if !snapshot[i].LastUsedAt.Equal(snapshot[j].LastUsedAt) {
			return snapshot[i].LastUsedAt.Before(snapshot[j].LastUsedAt)
		}
		return snapshot[i].ID < snapshot[j].ID
	})

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("registry: mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".conversations-*.json.tmp")
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
	if err := enc.Encode(&registryFile{Conversations: snapshot}); err != nil {
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

// Create appends c to the in-memory list. Caller owns uniqueness — Create
// does not validate that c.ID is unique, well-formed, or non-empty.
func (r *Registry) Create(c Conversation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conversations = append(r.conversations, c)
}

// Get returns the first conversation whose ID equals id, and true if one was
// found. Comparison is byte-exact.
func (r *Registry) Get(id ConversationID) (Conversation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conversations {
		if c.ID == id {
			return c, true
		}
	}
	return Conversation{}, false
}

// ListFilter narrows the result of List. A nil pointer field means "no filter
// on this field"; a non-nil pointer matches entries whose corresponding field
// equals the pointed-to value.
type ListFilter struct {
	IsPromoted *bool
}

// List returns a copy of the in-memory conversation list, optionally narrowed
// by filter. Callers may mutate the returned slice and its elements without
// affecting registry state.
//
// The variadic shape is for ergonomics, not for AND-composition: when more
// than one ListFilter is supplied, only filter[0] is consulted.
func (r *Registry) List(filter ...ListFilter) []Conversation {
	var f ListFilter
	if len(filter) > 0 {
		f = filter[0]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Conversation, 0, len(r.conversations))
	for _, c := range r.conversations {
		if f.IsPromoted != nil && c.IsPromoted != *f.IsPromoted {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Update locates the conversation with matching id, invokes fn with a pointer
// to that entry under the registry lock, and returns true. On miss, returns
// false and does not invoke fn.
//
// fn runs with r.mu held. fn MUST NOT call back into the registry — sync.Mutex
// is non-reentrant, and any Registry method would deadlock. fn MUST NOT retain
// the *Conversation pointer past return: the slice may be reallocated by a
// future Create. fn may read and mutate any field; the registry does not
// validate post-mutation state (e.g., does not reject a flip that duplicates
// another entry's ID).
func (r *Registry) Update(id ConversationID, fn func(*Conversation)) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.conversations {
		if r.conversations[i].ID == id {
			fn(&r.conversations[i])
			return true
		}
	}
	return false
}

// RebindSession re-points the conversation currently bound to oldID at newID,
// recording oldID in SessionHistory. Returns true iff a conversation was
// rebound. The scan and mutation happen atomically under r.mu, so there is no
// find-then-update window a concurrent Create/Delete could redirect.
//
//   - hit  → CurrentSessionID = newID; SessionHistory = append(SessionHistory, oldID)
//   - miss → no mutation, false (the rotated session is owned by no conversation)
//
// An empty oldID returns false immediately without scanning: an unbound
// conversation carries CurrentSessionID == "" (the unset sentinel) and must
// NEVER be swept into a rebind by a stray empty-id call. This is a
// data-integrity guard at the primitive boundary, not the primary eviction
// defense — that lives at the call site, which only rebinds on a /clear
// rotation (where NewID is non-empty). Precondition (caller-guaranteed on the
// rotation path): oldID and newID are non-empty and distinct.
//
// First match wins, mirroring Get/Update: a session id binds exactly one
// conversation (set once at creation), so the first match is the only match;
// pathological duplicates rebind the first only — deterministic and documented.
//
// RebindSession does NOT call Save — disk persistence is the caller's concern,
// matching the Create / Update / Promote / Delete convention.
func (r *Registry) RebindSession(oldID, newID string) bool {
	if oldID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.conversations {
		if r.conversations[i].CurrentSessionID == oldID {
			r.conversations[i].CurrentSessionID = newID
			r.conversations[i].SessionHistory = append(r.conversations[i].SessionHistory, oldID)
			return true
		}
	}
	return false
}

// Delete removes the conversation whose ID equals id. Returns true on hit,
// false on miss. Mutex-guarded; safe for concurrent use alongside the other
// Registry methods.
//
// Delete does NOT call Save — disk persistence is the caller's concern,
// matching the Create / Update / Promote convention.
func (r *Registry) Delete(id ConversationID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.conversations {
		if r.conversations[i].ID == id {
			r.conversations = append(r.conversations[:i], r.conversations[i+1:]...)
			return true
		}
	}
	return false
}

// Promote flips the conversation with id to promoted state and sets its
// display name to a non-nil pointer to name. Returns one of the exported
// sentinels on refusal:
//
//   - ErrConversationNotFound        — id is not present in the registry.
//   - ErrConversationAlreadyPromoted — target already has IsPromoted == true.
//   - ErrPromotionNameInUse          — another *promoted* conversation already
//     uses name (case-sensitive byte-exact comparison; unpromoted
//     conversations do not participate in the uniqueness check).
//   - ErrPromotionNameEmpty          — name is empty or contains only
//     whitespace.
//
// Validation, uniqueness scan, and mutation all happen under r.mu so a
// concurrent second Promote with the same name cannot slip through. On any
// refusal the registry is left untouched and no field of any record is
// modified. Persistence is the caller's responsibility — Promote does not
// call Save, matching the Create / Update convention.
func (r *Registry) Promote(id ConversationID, name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrPromotionNameEmpty
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := -1
	for i := range r.conversations {
		if r.conversations[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrConversationNotFound
	}
	if r.conversations[idx].IsPromoted {
		return ErrConversationAlreadyPromoted
	}
	for i := range r.conversations {
		if i == idx {
			continue
		}
		c := &r.conversations[i]
		if !c.IsPromoted {
			continue
		}
		if c.Name != nil && *c.Name == name {
			return ErrPromotionNameInUse
		}
	}
	n := name
	r.conversations[idx].IsPromoted = true
	r.conversations[idx].Name = &n
	return nil
}
