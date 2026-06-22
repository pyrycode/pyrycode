// Package modalbridge turns a detected tui-driver permission/trust modal into a
// typed, phone-facing protocol.ModalShownPayload and records it in an
// outstanding-modal registry keyed by a one-time modal_id nonce. The registry is
// the seam #717's inbound resolution half consumes to map an inbound option_id
// back to a surfaced modal and to reject stale/replayed answers.
//
// It is relay-free by construction. #717 intercepts modal_answer inside
// internal/relay and looks the nonce up here, so internal/relay imports this
// package — therefore this package MUST NOT import internal/relay (it would
// cycle). It imports only internal/protocol, internal/turnevent, and
// pkg/tuidriver (the typed ModalClass API only — never a raw-byte surface), so
// no claude-screen substrate literal enters this package and cmd/substrate-guard
// stays green; screenText arrives already rendered to plain text by the caller.
package modalbridge

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// Wire class strings surfaced on the modal_shown envelope. A plain string over a
// closed set (matching ModalShownPayload.Class), not a named enum. This slice
// introduces "permission" and "trust"; the exhaustive vocabulary is the
// producer's to finalize (docs/protocol-mobile.md § Modal).
const (
	classPermission = "permission"
	classTrust      = "trust"
)

// Trust-modal option ids. They have no exact PermissionOptionKind, so the
// internal PermissionRequest leaves Kind unset (Valid() is advisory here); only
// these ids reach the wire.
const (
	optProceed = "proceed"
	optExit    = "exit"
)

// maxPromptBytes bounds the Prompt field. A modal_shown is a *control* envelope
// and control frames are never dropped by the push queue (soft-overflow admit),
// so a pathological screen render must not be able to inflate an un-droppable
// frame. The body is grid-bounded by construction (a terminal render, KB-scale);
// this is the defensive backstop.
const maxPromptBytes = 4096

// titleByClass is the fixed, short, per-class modal title (distinct from the
// Prompt, which carries the rendered body). Single source of truth for the
// Title field; keyed by the wire class.
var titleByClass = map[string]string{
	classPermission: "Permission required",
	classTrust:      "Trust this folder?",
}

// denyByClass is the fail-safe default option per class: the DENY choice, not
// options[0]. For a remote permission/trust surface the phone's pre-highlighted
// default must fail safe, so a careless confirm denies rather than allows. This
// is UI pre-selection only (the human still confirms; #702 gates answering;
// #717 owns deny-on-timeout) and keeps DefaultOptionID ∈ Options[].ID by
// construction (the deny option is always in the set).
var denyByClass = map[string]string{
	classPermission: string(turnevent.PermissionOptionKindRejectOnce),
	classTrust:      optExit,
}

// Outstanding is one recorded surfaced modal. It holds at least the option list
// so #717 can map an inbound option_id against it; it carries no secret (the
// modal_id is an opaque correlation nonce, not a credential).
type Outstanding struct {
	ModalID         string
	Class           string
	Title           string
	Prompt          string
	Options         []protocol.ModalOption
	DefaultOptionID string
}

// Registry is the in-memory outstanding-modal store, keyed by modal_id. It is
// the one piece touched by two real goroutines — the surfacer goroutine
// (Record) and, in #717, the relay dispatch goroutine (Lookup/Resolve) — so it
// carries a sync.Mutex; it is NOT confined to one goroutine by convention. The
// mutex is a leaf lock: held only around O(1) map ops, never nested with any
// other lock.
type Registry struct {
	mu          sync.Mutex
	outstanding map[string]Outstanding
}

// New returns an empty Registry ready for use.
func New() *Registry {
	return &Registry{outstanding: make(map[string]Outstanding)}
}

// PermissionRequestForClass maps a detected modal class to the internal
// PermissionRequest the producer surfaces (AC1's "build an internal
// PermissionRequest"), the wire class string, and ok. ok is false for every
// class that is not permission/trust (AC1: those produce no modal_shown).
//
// This is the minimal fixed-option-set mapping (design option (a)): claude's
// permission options align with the four PermissionOptionKinds; trust maps to
// proceed/exit. Options are in claude's display order (allow-first). The
// rendered modal body (screenText, trimmed) becomes PermissionRequest.Title
// ("human-readable prompt text"); RequestID/ToolCallID stay empty — the modal_id
// minted in Record is the sole wire correlation key.
func PermissionRequestForClass(class tuidriver.ModalClass, screenText string) (turnevent.PermissionRequest, string, bool) {
	var (
		wireClass string
		options   []turnevent.PermissionOption
	)
	switch class {
	case tuidriver.ModalClassPermission:
		wireClass = classPermission
		options = []turnevent.PermissionOption{
			{ID: string(turnevent.PermissionOptionKindAllowOnce), Label: "Allow once", Kind: turnevent.PermissionOptionKindAllowOnce},
			{ID: string(turnevent.PermissionOptionKindAllowAlways), Label: "Allow always", Kind: turnevent.PermissionOptionKindAllowAlways},
			{ID: string(turnevent.PermissionOptionKindRejectOnce), Label: "Reject once", Kind: turnevent.PermissionOptionKindRejectOnce},
			{ID: string(turnevent.PermissionOptionKindRejectAlways), Label: "Reject always", Kind: turnevent.PermissionOptionKindRejectAlways},
		}
	case tuidriver.ModalClassTrustFolder:
		wireClass = classTrust
		options = []turnevent.PermissionOption{
			{ID: optProceed, Label: "Proceed"},
			{ID: optExit, Label: "Exit"},
		}
	default:
		return turnevent.PermissionRequest{}, "", false
	}
	req := turnevent.NewPermissionRequest("", "", strings.TrimSpace(screenText), options)
	return req, wireClass, true
}

// Record is the single nonce mint site (AC2). It builds the marshal-ready
// ModalShownPayload from req + wireClass, mints exactly one fresh modal_id,
// stamps it onto the payload, records the Outstanding under that id, and returns
// the id-stamped payload. The only error path is RNG failure (the caller drops
// the modal — never push an id-less payload). The payload build and the registry
// write are the one place a final payload is produced, honouring single-writer-
// nonce.
func (r *Registry) Record(req turnevent.PermissionRequest, wireClass string) (protocol.ModalShownPayload, error) {
	id, err := newModalID()
	if err != nil {
		return protocol.ModalShownPayload{}, err
	}
	p := buildPayload(req, wireClass)
	p.ModalID = id

	r.mu.Lock()
	r.outstanding[id] = Outstanding{
		ModalID:         id,
		Class:           p.Class,
		Title:           p.Title,
		Prompt:          p.Prompt,
		Options:         slices.Clone(p.Options),
		DefaultOptionID: p.DefaultOptionID,
	}
	r.mu.Unlock()
	return p, nil
}

// Lookup returns the Outstanding for modalID without removing it. It is #717's
// read seam (defined now, exercised by #717).
func (r *Registry) Lookup(modalID string) (Outstanding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.outstanding[modalID]
	return o, ok
}

// Resolve returns the Outstanding for modalID and removes it — the one-shot
// consumption #717 uses to answer-and-retire a modal. Defined now; #716 only
// needs Record/Lookup, but it belongs with the type's contract.
func (r *Registry) Resolve(modalID string) (Outstanding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.outstanding[modalID]
	if ok {
		delete(r.outstanding, modalID)
	}
	return o, ok
}

// buildPayload assembles the marshal-ready ModalShownPayload (sans modal_id) for
// a permission/trust modal: it maps each PermissionOption{ID,Label} to a wire
// ModalOption (dropping Kind — not on the wire), sets the fixed per-class Title,
// the defensively-bounded Prompt (the rendered body in req.Title), and the
// fail-safe deny DefaultOptionID. Record mints and stamps the ModalID.
func buildPayload(req turnevent.PermissionRequest, wireClass string) protocol.ModalShownPayload {
	opts := make([]protocol.ModalOption, len(req.Options))
	for i, o := range req.Options {
		opts[i] = protocol.ModalOption{ID: o.ID, Label: o.Label}
	}
	return protocol.ModalShownPayload{
		Class:           wireClass,
		Title:           titleByClass[wireClass],
		Prompt:          boundPrompt(req.Title),
		Options:         opts,
		DefaultOptionID: denyByClass[wireClass],
	}
}

// boundPrompt trims surrounding whitespace and caps the body at maxPromptBytes,
// backing up to a rune boundary so a multi-byte rune is never split.
func boundPrompt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxPromptBytes {
		return s
	}
	cut := maxPromptBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// newModalID returns a fresh UUIDv4-shaped nonce drawn from crypto/rand,
// mirroring conversations.NewID. 122 bits of entropy ⇒ opaque + unguessable —
// the security primitive #717 relies on to reject stale/replayed answers. NOT
// math/rand. Returns an error only when the system RNG fails.
func newModalID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
