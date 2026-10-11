// Package memorycapture persists immutable capture inputs and caller-reported
// attempt evidence. It does not execute agents or advance capture progress.
package memorycapture

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	ErrUnsafe   = errors.New("unsafe capture state")
	ErrCorrupt  = errors.New("corrupt capture state")
	ErrInvalid  = errors.New("invalid capture input")
	ErrMismatch = errors.New("capture batch mismatch")
)

// Identity uses the canonical vault path and exporter-owned conversation and
// transcript generation. A routing ID alone cannot identify a generation.
type Identity struct{ Vault, Conversation, Generation string }

// Message preserves exporter-owned row identity, text revision and attribution.
// Revision must change whenever text on the same stable row changes.
type Message struct{ ID, Revision, Text, Timestamp, Speaker, Agent, Session string }

// Payload is frozen at first publication. Metadata carries additional structured
// exporter input. ClosingEntry is independent of delivered and eligible text.
type Payload struct {
	Agent, Model                      string
	Messages                          []Message
	Metadata                          json.RawMessage
	LastTextEntry, LastDeliveredEntry uint64
	ClosingEntry                      *uint64
}

// Batch contains immutable input and a separately updated attempt-evidence union.
// All returned data is detached from storage and from other callers.
type Batch struct {
	ID           string
	Payload      Payload
	ChangedPaths []string
}

// Store holds a private state directory. Operations are safe across concurrent
// handles/processes; Close waits for this handle's active operations.
type Store struct {
	mu   sync.RWMutex
	home string
	dir  int
	hook func(string) error
}

type record struct {
	Version  int
	Identity Identity
	Batch    Batch
	Checksum string
}

func digest(v any) string {
	// Callers validate RawMessage first; all remaining fields have infallible
	// JSON encodings (strings, integers, slices and structs without marshalers).
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func recordName(id Identity) string { return digest(id) + ".json" }
func batchID(id Identity, p Payload) string {
	return digest(struct {
		Identity Identity
		Payload  Payload
	}{id, p})
}
func validIdentity(id Identity) bool {
	return filepath.IsAbs(id.Vault) && filepath.Clean(id.Vault) == id.Vault && id.Conversation != "" && id.Generation != "" && validUTF8(id.Vault, id.Conversation, id.Generation) && !strings.ContainsRune(id.Vault+id.Conversation+id.Generation, 0)
}
func validPayload(p Payload) bool {
	if p.Agent == "" || p.Model == "" || !validUTF8(p.Agent, p.Model) || len(p.Metadata) > 0 && (!json.Valid(p.Metadata) || !utf8.Valid(p.Metadata)) {
		return false
	}
	for _, m := range p.Messages {
		if m.ID == "" || m.Revision == "" || !validUTF8(m.ID, m.Revision, m.Text, m.Timestamp, m.Speaker, m.Agent, m.Session) {
			return false
		}
	}
	return true
}
func validUTF8(fields ...string) bool {
	for _, field := range fields {
		if !utf8.ValidString(field) {
			return false
		}
	}
	return true
}
func validPath(p string) bool {
	return p != "." && utf8.ValidString(p) && fs.ValidPath(p) && !strings.ContainsAny(p, "\\\x00")
}
func checksum(r record) string { r.Checksum = ""; return digest(r) }

// New opens state below home/.pyry/memory/capture without following symlinks.
// The caller supplies the account home directory, not the notes vault.
func New(home string) (*Store, error) {
	fd, err := openDirectory(home, true)
	if err != nil {
		return nil, fmt.Errorf("capture open: %w", err)
	}
	return &Store{home: home, dir: fd}, nil
}

// Publish durably publishes a payload or returns the existing pending batch.
// A nil batch accompanies every error, including ambiguous post-rename failures.
func (s *Store) Publish(ctx context.Context, id Identity, p Payload) (*Batch, error) {
	var result *Batch
	err := s.transaction(ctx, id, func(r *record) (*record, error) {
		if r != nil {
			result = &r.Batch
			return nil, nil
		}
		if !validPayload(p) {
			return nil, ErrInvalid
		}
		// Round-tripping freezes slices, raw JSON, and optional closure pointers.
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, ErrInvalid
		}
		var frozen Payload
		if err = json.Unmarshal(raw, &frozen); err != nil {
			return nil, ErrInvalid
		}
		next := &record{Version: 1, Identity: id, Batch: Batch{ID: batchID(id, frozen), Payload: frozen}}
		result = &next.Batch
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Pending returns a durable executable batch, or nil when none was published.
func (s *Store) Pending(ctx context.Context, id Identity) (*Batch, error) {
	var result *Batch
	err := s.transaction(ctx, id, func(r *record) (*record, error) {
		if r != nil {
			result = &r.Batch
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Report retains the union of changed relative note paths for any attempt,
// irrespective of execution success. Any invalid path rejects the entire update.
func (s *Store) Report(ctx context.Context, id Identity, batch string, paths []string) error {
	for _, p := range paths {
		if !validPath(p) {
			return ErrInvalid
		}
	}
	return s.transaction(ctx, id, func(r *record) (*record, error) {
		if r == nil || r.Batch.ID != batch {
			return nil, ErrMismatch
		}
		union := make(map[string]bool)
		for _, p := range r.Batch.ChangedPaths {
			union[p] = true
		}
		for _, p := range paths {
			union[p] = true
		}
		if len(union) == len(r.Batch.ChangedPaths) {
			return nil, nil
		}
		r.Batch.ChangedPaths = make([]string, 0, len(union))
		for p := range union {
			r.Batch.ChangedPaths = append(r.Batch.ChangedPaths, p)
		}
		sort.Strings(r.Batch.ChangedPaths)
		return r, nil
	})
}
