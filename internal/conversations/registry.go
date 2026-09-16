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
	"unicode/utf8"
)

// registryFile is the on-disk envelope for ~/.pyry/conversations.json. The
// envelope shape (rather than a bare top-level array) reserves room for
// future top-level fields without a wire break.
type registryFile struct {
	Conversations []Conversation `json:"conversations"`

	// WorkspaceLabels maps a workspace's cwd — the exact string stored on a
	// conversation's Cwd — to the operator-set display name for that workspace
	// (#2206). Top-level rather than per-conversation because a label belongs to
	// the workspace, not to a thread: N conversations can share one cwd, and a
	// per-row copy would need N-way write fan-out and could disagree with itself.
	//
	// omitempty carries the same contract as Conversation.IsArchived's and
	// SystemPrompt's: "an absent key decodes as no labels, with no migration
	// step." On a map omitempty tests length rather than nilness, so a map that
	// was allocated and then emptied by clearing its last label omits the key
	// just as a never-allocated one does — which is what keeps a set-then-cleared
	// registry byte-identical to its pre-#2206 form.
	//
	// encoding/json sorts map keys on marshal, so this field needs no counterpart
	// to the Save-side sort applied to Conversations: output stays byte-identical
	// for the same logical content regardless of insertion order.
	WorkspaceLabels map[string]string `json:"workspace_labels,omitempty"`
}

// MaxSystemPromptBytes bounds Conversation.SystemPrompt, inclusive: a value of
// exactly this many bytes is accepted. Bytes rather than runes, because both the
// registry file and the wire frame that carries the value are byte-budgeted.
//
// The value has to fit inside a v2 application envelope (capped at 65519 bytes,
// docs/protocol-mobile.md § Application-envelope size cap) with room to spare
// for the rest of a payload, and this is far above any hand-written channel
// instruction. It is deliberately not an argv constraint — the prompt reaches
// claude through a file, never as a command-line value.
const MaxSystemPromptBytes = 8192

// Sentinel errors returned by Promote and SetSystemPrompt. Callers (CLI,
// wire-protocol layer) distinguish refusal cases via errors.Is and map to
// user-facing codes.
//
// Every sentinel is static and returned naked: no refusal path interpolates the
// rejected value, its length, or the conversation id, so a caller's log or wire
// reply cannot pick up a fragment of an operator's prompt from an error.
var (
	ErrConversationNotFound        = errors.New("conversations: conversation not found")
	ErrConversationAlreadyPromoted = errors.New("conversations: conversation already promoted")
	ErrPromotionNameInUse          = errors.New("conversations: promotion name already in use")
	ErrPromotionNameEmpty          = errors.New("conversations: promotion name is empty")
	ErrSystemPromptTooLong         = errors.New("conversations: system prompt exceeds the maximum byte length")
	ErrSystemPromptInvalidUTF8     = errors.New("conversations: system prompt is not valid UTF-8")
)

// Registry is the in-memory conversation list, guarded by a mutex. Construct
// via Load (cold-start or warm-start from disk); persist via Save. All methods
// are safe for concurrent use.
type Registry struct {
	// saveMu serializes the full Save sequence (snapshot → encode → fsync →
	// rename) so a later snapshot always renames later; an older snapshot can
	// never clobber a newer one on disk. It is deliberately separate from mu so
	// the slow disk write does not block concurrent reads/mutations. Lock order
	// is one-directional: saveMu → mu (Save takes saveMu, then briefly mu for
	// the snapshot copy), never the reverse.
	saveMu        sync.Mutex
	mu            sync.Mutex
	conversations []Conversation

	// workspaceLabels is the per-workspace display name map (#2206), guarded by
	// mu like conversations. Nil until the first SetWorkspaceLabel stores a
	// value, so a registry that never labels a workspace never allocates one.
	//
	// Save must copy this map inside its mu critical section, beside the
	// conversations copy. Encoding the live map is not merely a data race:
	// json.Marshal ranges over it, and a concurrent write during that range is a
	// fatal "concurrent map iteration and map write" throw that kills the
	// process. The copy must also be element-wise — a map header copy aliases the
	// same buckets, so a delete or an insert-triggered rehash during the encode
	// throws just the same. The shallow-copy reasoning that makes
	// Conversation.SessionHistory safe does not transfer: it holds only because
	// appends write at indices past the snapshot's length, and maps have no such
	// disjointness.
	workspaceLabels map[string]string
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
	return &Registry{conversations: rf.Conversations, workspaceLabels: rf.WorkspaceLabels}, nil
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
	// Hold saveMu across the whole snapshot→rename sequence so two overlapping
	// Save calls serialize: snapshot order == saveMu-acquire order == rename
	// order. The inner r.mu critical section below still guards the snapshot
	// copy against concurrent mutators. See the saveMu field doc for lock order.
	r.saveMu.Lock()
	defer r.saveMu.Unlock()

	r.mu.Lock()
	snapshot := make([]Conversation, len(r.conversations))
	copy(snapshot, r.conversations)
	// Copy the label map here too — see the workspaceLabels field doc for why
	// encoding the live map is a fatal throw rather than a reported race. The
	// copy is unconditional: an empty copy still omits the key, because
	// omitempty on a map tests length rather than nilness.
	labels := make(map[string]string, len(r.workspaceLabels))
	for cwd, label := range r.workspaceLabels {
		labels[cwd] = label
	}
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
	if err := enc.Encode(&registryFile{Conversations: snapshot, WorkspaceLabels: labels}); err != nil {
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
// equals the pointed-to value. When more than one field is set, they AND: an
// entry is returned only if it matches every non-nil field.
type ListFilter struct {
	IsPromoted *bool
	IsArchived *bool
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
		if f.IsArchived != nil && c.IsArchived != *f.IsArchived {
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

// SetArchived flips the durable archived flag of the conversation whose ID
// equals id: archived=true archives it, archived=false restores it to active.
// Returns true on hit, false on miss; on miss no field of any record is
// modified (AC: "miss for an unknown id, leaving the registry unmodified").
//
// It sets exactly one field — IsArchived — so id, cwd, name, promoted state,
// and session binding are structurally untouched. This is the deterministic
// enforcement of the ticket's "flips exactly one field" constraint: the
// #881 verb handler that calls this cannot get it wrong, because the method
// has no way to touch another field. The scan and mutation happen atomically
// under r.mu, so there is no find-then-mutate window a concurrent
// Create/Delete could redirect.
//
// The single archived bool both sets and clears — SetArchived(id, true) on an
// already-archived row returns true and leaves it archived (idempotent /
// symmetric toggle), so no separate Archive/Unarchive pair is needed.
//
// SetArchived does NOT call Save — disk persistence is the caller's concern,
// matching the Create / Update / Promote / Delete / RebindSession convention.
func (r *Registry) SetArchived(id ConversationID, archived bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.conversations {
		if r.conversations[i].ID == id {
			r.conversations[i].IsArchived = archived
			return true
		}
	}
	return false
}

// SetSystemPrompt sets the operator-set system prompt of the conversation whose
// ID equals id. It sets exactly one field — SystemPrompt — so id, cwd, name,
// promoted/archived state, and session binding are structurally untouched, the
// same guarantee SetArchived gives for its own field.
//
// The *string argument spans all three prompt states through one validated
// door, mirroring SetArchived's single argument that both sets and clears:
//
//   - nil          → the conversation returns to "no prompt" (the default).
//   - non-nil ""   → the explicitly-empty state.
//   - non-nil text → stored verbatim after validation.
//
// Refusals, each a static exported sentinel:
//
//   - ErrSystemPromptTooLong     — *prompt exceeds MaxSystemPromptBytes.
//   - ErrSystemPromptInvalidUTF8 — *prompt is not valid UTF-8.
//   - ErrConversationNotFound    — id is not present in the registry.
//
// On any refusal no field of any record is modified. Value validation runs
// before the lock (it reads only the caller's value), so the refusal ordering is
// value-first then identity — the same ordering Promote uses, where the empty-name
// check likewise precedes the not-found scan. Length is checked before UTF-8
// validity so a hostile oversize value is rejected in O(1) rather than after a
// full validity scan; a value that is both over-length and invalid therefore
// returns ErrSystemPromptTooLong.
//
// Invalid UTF-8 is refused rather than sanitized because encoding/json
// substitutes U+FFFD on marshal: such a value would not survive Save → Load
// unchanged, and the substitution can grow it past the bound after admission.
// Refusing at the door is what keeps "stored verbatim" and "round-trips
// unchanged" simultaneously true.
//
// The pointee is copied into a fresh local before its address is taken, so the
// stored pointer never aliases a caller-held variable — the same defensive idiom
// Promote uses for Name. Get and List keep copying records shallowly and sharing
// the stored pointer, exactly as they already do for Name.
//
// SetSystemPrompt does NOT call Save — disk persistence is the caller's concern,
// matching the Create / Update / Promote / Delete / RebindSession / SetArchived
// convention. It takes no logger, and nothing in this package logs a record
// field, so the value cannot leave the registry file by way of a log line.
func (r *Registry) SetSystemPrompt(id ConversationID, prompt *string) error {
	if prompt != nil {
		if len(*prompt) > MaxSystemPromptBytes {
			return ErrSystemPromptTooLong
		}
		if !utf8.ValidString(*prompt) {
			return ErrSystemPromptInvalidUTF8
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.conversations {
		if r.conversations[i].ID == id {
			if prompt == nil {
				r.conversations[i].SystemPrompt = nil
				return nil
			}
			p := *prompt
			r.conversations[i].SystemPrompt = &p
			return nil
		}
	}
	return ErrConversationNotFound
}

// SetLastContextUsage records the last context-window reading claude reported for
// the conversation whose ID equals id (#2460). Returns true on hit, false on miss;
// on a miss no field of any record is modified.
//
// It sets exactly one field — LastContextUsage — so id, cwd, name,
// promoted/archived state, system prompt and session binding are structurally
// untouched, the same guarantee SetArchived and SetSystemPrompt give for theirs.
// The scan and mutation happen atomically under r.mu, so there is no
// find-then-mutate window a concurrent Create/Delete could redirect.
//
// IT TAKES A VALUE, NOT A POINTER, which is the one place it deliberately departs
// from SetSystemPrompt's tri-state door. No producer of a reading ever clears one:
// a conversation that has never been reported on carries nil from creation, and
// once claude has answered there is always a last reading. A value argument makes
// "set it back to nil" unreachable rather than merely unused.
//
// LAST WRITE WINS, AND THAT IS THE INTENDED BEHAVIOUR. The two producers — the
// post-turn emitter arm and the on-demand request_context_usage flight — run on
// different goroutines and can settle seconds apart; both readings are valid "last
// reading" values and r.mu makes the write safe. Do not add AsOf comparison or any
// other ordering machinery here.
//
// AsOf is normalised to UTC so the encoded form is RFC 3339 with a Z offset:
// time.Time marshals with whatever offset it carries, so a caller-side .UTC()
// would be one forgettable step per producer. The instant is preserved — this
// changes the location, not the time.
//
// No value validation and no second byte bound. Model arrives from streamsup's
// decodeContextUsage, the single decoder both lanes share, which already caps it;
// the other three are decoded ints. No invalid-UTF-8 sentinel either, for
// SetWorkspaceLabel's stated reason: the string reached Go through encoding/json,
// which has already substituted U+FFFD, so the round-trip-fidelity hazard
// SetSystemPrompt refuses cannot arrive at this door.
//
// The value is copied into a fresh local before its address is taken, so the stored
// pointer never aliases a caller-held variable — the idiom Promote uses for Name
// and SetSystemPrompt for its prompt.
//
// SetLastContextUsage does NOT call Save — disk persistence is the caller's
// concern, matching the Create / Update / Promote / Delete / RebindSession /
// SetArchived / SetSystemPrompt / SetWorkspaceLabel convention. It takes no logger,
// and nothing in this package logs a record field, so a reading cannot leave the
// registry file by way of a log line.
func (r *Registry) SetLastContextUsage(id ConversationID, reading ContextUsageReading) bool {
	reading.AsOf = reading.AsOf.UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.conversations {
		if r.conversations[i].ID == id {
			stored := reading
			r.conversations[i].LastContextUsage = &stored
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

// WorkspaceLabel returns the operator-set display name stored for the workspace
// at cwd, and true if one is set (#2206). Comparison is byte-exact, the same
// contract Get gives for a conversation id: two paths differing only in a
// trailing separator, a trailing space, or case are distinct workspaces, and
// nothing here normalizes, resolves, joins, stats, or opens the key — at this
// layer a cwd is a map key and nothing more.
//
// It answers for one workspace at a time and deliberately does NOT hand back the
// map. A caller ranging over a shared map outside r.mu while another goroutine
// writes it is not a race the detector reports but a fatal "concurrent map
// iteration and map write" throw that kills the daemon, so the per-key signature
// makes the escape structurally impossible rather than forbidding it in prose.
// The consuming slices (#2208, #2210) call this once per conversation behind
// their own narrow interfaces, which *Registry satisfies structurally.
//
// A cleared label reads as absent, never as a present empty string; an
// explicitly-empty label reads as ("", true). The two states stay distinct.
func (r *Registry) WorkspaceLabel(cwd string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	label, ok := r.workspaceLabels[cwd]
	return label, ok
}

// SetWorkspaceLabel stores the display name for the workspace at cwd, or clears
// it (#2206). The nullable argument spans both states through one door, mirroring
// SetSystemPrompt's *string and SetArchived's single bool that both sets and
// clears:
//
//   - nil          → the key is deleted, so WorkspaceLabel reports absent rather
//     than a present empty string.
//   - non-nil ""   → the explicitly-empty label, stored and reported present.
//   - non-nil text → stored verbatim.
//
// It has no failure mode and returns nothing. In particular it does NOT check cwd
// against the conversation list — #2207's handler owns the not-found refusal —
// and, unlike its neighbour SetSystemPrompt, it does NOT validate the value.
// Non-blank and length bounds belong to the wire handler; this layer stores what
// it is given, under the key it is given. Any future caller (a CLI binding, a
// second verb) inherits an unvalidated door and must bring its own bounds: the
// two setters sit adjacent in this file with deliberately opposite contracts.
//
// Storing verbatim assumes a valid-UTF-8 label for round-trip fidelity, since
// encoding/json substitutes U+FFFD on marshal — the hazard SetSystemPrompt
// refuses with a sentinel. No sentinel here, because the wire path cannot produce
// the input: encoding/json performs the same substitution while decoding into a
// Go string, so whatever reaches a handler is already valid UTF-8.
//
// The pointee is copied into a fresh local before it is stored, so the map never
// aliases a caller-held variable — the defensive idiom Promote uses for Name and
// SetSystemPrompt for its prompt. The map is allocated lazily on the first store,
// which is also the path a registry freshly loaded from a pre-#2206 file takes:
// its map is nil, and assignment to a nil map panics.
//
// SetWorkspaceLabel does NOT call Save — disk persistence is the caller's
// concern, matching the Create / Update / Promote / Delete / RebindSession /
// SetArchived / SetSystemPrompt convention. It takes no logger, and nothing in
// this package logs a record field, so a label cannot leave the registry file by
// way of a log line.
func (r *Registry) SetWorkspaceLabel(cwd string, label *string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if label == nil {
		delete(r.workspaceLabels, cwd)
		return
	}
	if r.workspaceLabels == nil {
		r.workspaceLabels = make(map[string]string)
	}
	r.workspaceLabels[cwd] = *label
}
