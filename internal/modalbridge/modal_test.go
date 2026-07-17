package modalbridge

import (
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

func optionIDs(opts []protocol.ModalOption) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.ID
	}
	return out
}

func permissionOptionIDs(opts []turnevent.PermissionOption) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.ID
	}
	return out
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPermissionRequestForClass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		class     tuidriver.ModalClass
		wantOK    bool
		wantClass string
		wantIDs   []string
	}{
		{
			name:      "permission",
			class:     tuidriver.ModalClassPermission,
			wantOK:    true,
			wantClass: classPermission,
			wantIDs:   []string{"allow_once", "allow_always", "reject_once", "reject_always"},
		},
		{
			name:      "trust-folder",
			class:     tuidriver.ModalClassTrustFolder,
			wantOK:    true,
			wantClass: classTrust,
			wantIDs:   []string{"proceed", "exit"},
		},
		{name: "mcp", class: tuidriver.ModalClassMCP, wantOK: false},
		{name: "agents", class: tuidriver.ModalClassAgents, wantOK: false},
		{name: "slash-picker", class: tuidriver.ModalClassSlashPicker, wantOK: false},
		{name: "ask-user-question", class: tuidriver.ModalClassAskUserQuestion, wantOK: false},
		{name: "model-select", class: tuidriver.ModalClassModelSelect, wantOK: false},
		{name: "permissions-config", class: tuidriver.ModalClassPermissionsConfig, wantOK: false},
		{name: "unknown", class: tuidriver.ModalClassUnknown, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req, class, ok := PermissionRequestForClass(tt.class, "  body text  ")
			if ok != tt.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				if class != "" || len(req.Options) != 0 {
					t.Errorf("non-matched class should return zero values; got class=%q options=%v", class, req.Options)
				}
				return
			}
			if class != tt.wantClass {
				t.Errorf("class: got %q, want %q", class, tt.wantClass)
			}
			if got := permissionOptionIDs(req.Options); !slicesEqual(got, tt.wantIDs) {
				t.Errorf("option ids: got %v, want %v", got, tt.wantIDs)
			}
			if req.Title != "body text" {
				t.Errorf("Title (body): got %q, want trimmed %q", req.Title, "body text")
			}
			if req.RequestID != "" || req.ToolCallID != "" {
				t.Errorf("RequestID/ToolCallID must be empty; got %q/%q", req.RequestID, req.ToolCallID)
			}
		})
	}
}

func TestRecord_MintsAndStores(t *testing.T) {
	t.Parallel()
	reg := New()
	req, class, ok := PermissionRequestForClass(tuidriver.ModalClassPermission, "do something")
	if !ok {
		t.Fatal("permission class should map")
	}
	payload, err := reg.Record(req, class, "")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if payload.ModalID == "" {
		t.Fatal("ModalID must be non-empty")
	}
	if !conversations.ValidID(payload.ModalID) {
		t.Errorf("ModalID %q is not a canonical UUIDv4", payload.ModalID)
	}

	got, ok := reg.Lookup(payload.ModalID)
	if !ok {
		t.Fatalf("Lookup(%q): not found", payload.ModalID)
	}
	if got.ModalID != payload.ModalID {
		t.Errorf("Outstanding.ModalID: got %q, want %q", got.ModalID, payload.ModalID)
	}
	if !slicesEqual(optionIDs(got.Options), optionIDs(payload.Options)) {
		t.Errorf("Outstanding option ids %v != payload option ids %v",
			optionIDs(got.Options), optionIDs(payload.Options))
	}
	if got.DefaultOptionID != payload.DefaultOptionID {
		t.Errorf("Outstanding.DefaultOptionID %q != payload %q", got.DefaultOptionID, payload.DefaultOptionID)
	}

	if _, ok := reg.Lookup("nonexistent-id"); ok {
		t.Error("Lookup of an unrecorded id should return false")
	}
}

// TestRecord_StampsAndStoresConversationID is AC3 (stored-payload scoping,
// #1065): Record stamps conversation_id onto the returned payload AND stores it
// in the Outstanding, so Snapshot — the reconcile replay source — re-emits the
// same scoping key. This proves the STORED payload (not just the live one) is
// scoped, so a reconnect replay is scoped identically to initial delivery.
func TestRecord_StampsAndStoresConversationID(t *testing.T) {
	t.Parallel()
	reg := New()
	req, class, ok := PermissionRequestForClass(tuidriver.ModalClassPermission, "do something")
	if !ok {
		t.Fatal("permission class should map")
	}
	payload, err := reg.Record(req, class, "conv-X")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if payload.ConversationID != "conv-X" {
		t.Errorf("returned payload ConversationID: got %q, want %q", payload.ConversationID, "conv-X")
	}

	snap := reg.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("Snapshot len: got %d, want 1", len(snap))
	}
	if snap[0].ConversationID != "conv-X" {
		t.Errorf("stored (Snapshot) ConversationID: got %q, want %q — replay must carry the scope key", snap[0].ConversationID, "conv-X")
	}
}

func TestRecord_NonceUniqueness(t *testing.T) {
	t.Parallel()
	reg := New()
	req, class, _ := PermissionRequestForClass(tuidriver.ModalClassPermission, "x")

	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		payload, err := reg.Record(req, class, "")
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		if _, dup := seen[payload.ModalID]; dup {
			t.Fatalf("duplicate modal_id minted: %q", payload.ModalID)
		}
		seen[payload.ModalID] = struct{}{}
	}
	if len(seen) != n {
		t.Errorf("got %d distinct ids, want %d", len(seen), n)
	}
}

func TestRecord_PayloadInvariant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		class       tuidriver.ModalClass
		wantClass   string
		wantIDs     []string
		wantTitle   string
		wantDefault string // the DENY option, not options[0]
	}{
		{
			name:        "permission",
			class:       tuidriver.ModalClassPermission,
			wantClass:   classPermission,
			wantIDs:     []string{"allow_once", "allow_always", "reject_once", "reject_always"},
			wantTitle:   "Permission required",
			wantDefault: "reject_once",
		},
		{
			name:        "trust",
			class:       tuidriver.ModalClassTrustFolder,
			wantClass:   classTrust,
			wantIDs:     []string{"proceed", "exit"},
			wantTitle:   "Trust this folder?",
			wantDefault: "exit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := New()
			req, class, ok := PermissionRequestForClass(tt.class, "body")
			if !ok {
				t.Fatal("class should map")
			}
			payload, err := reg.Record(req, class, "")
			if err != nil {
				t.Fatalf("Record: %v", err)
			}
			if payload.Class != tt.wantClass {
				t.Errorf("Class: got %q, want %q", payload.Class, tt.wantClass)
			}
			if payload.Title != tt.wantTitle {
				t.Errorf("Title: got %q, want %q", payload.Title, tt.wantTitle)
			}
			if len(payload.Options) == 0 {
				t.Fatal("Options must be non-empty")
			}
			if got := optionIDs(payload.Options); !slicesEqual(got, tt.wantIDs) {
				t.Errorf("ordered option ids: got %v, want %v", got, tt.wantIDs)
			}
			// DefaultOptionID is the deny option AND is one of Options[].ID.
			if payload.DefaultOptionID != tt.wantDefault {
				t.Errorf("DefaultOptionID: got %q, want deny option %q", payload.DefaultOptionID, tt.wantDefault)
			}
			found := false
			for _, o := range payload.Options {
				if o.ID == payload.DefaultOptionID {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("DefaultOptionID %q is not one of Options[].ID %v", payload.DefaultOptionID, optionIDs(payload.Options))
			}
			// Fail-safe: the default must NOT be the first (allow/proceed) option.
			if payload.Options[0].ID == payload.DefaultOptionID {
				t.Errorf("DefaultOptionID equals options[0] (%q) — not fail-safe", payload.DefaultOptionID)
			}
		})
	}
}

func TestRecord_PromptTrimmedAndPlain(t *testing.T) {
	t.Parallel()
	reg := New()
	req, class, _ := PermissionRequestForClass(tuidriver.ModalClassPermission, "\n  a plain prompt body  \n")
	payload, err := reg.Record(req, class, "")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if payload.Prompt != "a plain prompt body" {
		t.Errorf("Prompt: got %q, want trimmed %q", payload.Prompt, "a plain prompt body")
	}
	// 0x1b is the ESC byte; a plain-text body carries no control/ANSI bytes.
	if strings.ContainsRune(payload.Prompt, 0x1b) {
		t.Error("Prompt contains an ESC control byte; want plain text only")
	}
}

func TestRecord_PromptBounded(t *testing.T) {
	t.Parallel()
	reg := New()
	// A multi-byte rune body well over the cap: truncation must land on a rune
	// boundary (valid UTF-8) and stay within maxPromptBytes.
	body := strings.Repeat("é", maxPromptBytes) // 2 bytes each ⇒ 2*maxPromptBytes bytes
	req, class, _ := PermissionRequestForClass(tuidriver.ModalClassPermission, body)
	payload, err := reg.Record(req, class, "")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(payload.Prompt) > maxPromptBytes {
		t.Errorf("Prompt len %d exceeds cap %d", len(payload.Prompt), maxPromptBytes)
	}
	for i, r := range payload.Prompt {
		if r == 0xFFFD {
			t.Fatalf("Prompt has a replacement rune at byte %d — truncation split a rune", i)
		}
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	reg := New()
	req, class, _ := PermissionRequestForClass(tuidriver.ModalClassTrustFolder, "trust?")
	payload, err := reg.Record(req, class, "")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, ok := reg.Resolve(payload.ModalID)
	if !ok {
		t.Fatalf("Resolve(%q): not found", payload.ModalID)
	}
	if got.ModalID != payload.ModalID {
		t.Errorf("resolved ModalID: got %q, want %q", got.ModalID, payload.ModalID)
	}
	// One-shot: a second Resolve and a Lookup both miss after consumption.
	if _, ok := reg.Resolve(payload.ModalID); ok {
		t.Error("second Resolve should miss after one-shot consumption")
	}
	if _, ok := reg.Lookup(payload.ModalID); ok {
		t.Error("Lookup should miss after Resolve removed the entry")
	}
}

func TestSnapshot_Empty(t *testing.T) {
	t.Parallel()
	if got := New().Snapshot(); len(got) != 0 {
		t.Errorf("empty registry: got %d payloads, want 0", len(got))
	}
}

func TestSnapshot_ResolvedModalDoesNotResurface(t *testing.T) {
	t.Parallel()
	reg := New()
	req, class, _ := PermissionRequestForClass(tuidriver.ModalClassPermission, "do something")
	payload, err := reg.Record(req, class, "")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, ok := reg.Resolve(payload.ModalID); !ok {
		t.Fatalf("Resolve(%q): not found", payload.ModalID)
	}
	if got := reg.Snapshot(); len(got) != 0 {
		t.Errorf("after Resolve: got %d payloads, want 0 (retired modals must not re-surface)", len(got))
	}
}

func TestSnapshot_SingleOutstandingRoundTrips(t *testing.T) {
	t.Parallel()
	reg := New()
	req, class, _ := PermissionRequestForClass(tuidriver.ModalClassPermission, "do something")
	payload, err := reg.Record(req, class, "")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := reg.Snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d payloads, want 1", len(got))
	}
	// Equivalent to the payload Record returned: same id and same content.
	p := got[0]
	if p.ModalID != payload.ModalID {
		t.Errorf("ModalID: got %q, want %q", p.ModalID, payload.ModalID)
	}
	if p.Class != payload.Class {
		t.Errorf("Class: got %q, want %q", p.Class, payload.Class)
	}
	if p.Title != payload.Title {
		t.Errorf("Title: got %q, want %q", p.Title, payload.Title)
	}
	if p.Prompt != payload.Prompt {
		t.Errorf("Prompt: got %q, want %q", p.Prompt, payload.Prompt)
	}
	if p.DefaultOptionID != payload.DefaultOptionID {
		t.Errorf("DefaultOptionID: got %q, want %q", p.DefaultOptionID, payload.DefaultOptionID)
	}
	if !slicesEqual(optionIDs(p.Options), optionIDs(payload.Options)) {
		t.Errorf("option ids: got %v, want %v", optionIDs(p.Options), optionIDs(payload.Options))
	}
}

func TestSnapshot_MultipleOutstandingKeyedByID(t *testing.T) {
	t.Parallel()
	reg := New()
	permReq, permClass, _ := PermissionRequestForClass(tuidriver.ModalClassPermission, "perm")
	perm, err := reg.Record(permReq, permClass, "")
	if err != nil {
		t.Fatalf("Record permission: %v", err)
	}
	trustReq, trustClass, _ := PermissionRequestForClass(tuidriver.ModalClassTrustFolder, "trust")
	trust, err := reg.Record(trustReq, trustClass, "")
	if err != nil {
		t.Fatalf("Record trust: %v", err)
	}

	// Reconcile by modal_id, never by position: map-walk order is unspecified.
	byID := make(map[string]protocol.ModalShownPayload)
	for _, p := range reg.Snapshot() {
		byID[p.ModalID] = p
	}
	if len(byID) != 2 {
		t.Fatalf("got %d distinct payloads, want 2", len(byID))
	}
	if got, ok := byID[perm.ModalID]; !ok {
		t.Errorf("permission modal %q missing from snapshot", perm.ModalID)
	} else if got.Class != classPermission {
		t.Errorf("permission modal Class: got %q, want %q", got.Class, classPermission)
	}
	if got, ok := byID[trust.ModalID]; !ok {
		t.Errorf("trust modal %q missing from snapshot", trust.ModalID)
	} else if got.Class != classTrust {
		t.Errorf("trust modal Class: got %q, want %q", got.Class, classTrust)
	}
}

func TestSnapshot_PureReadLeavesStateUndisturbed(t *testing.T) {
	t.Parallel()
	reg := New()
	req, class, _ := PermissionRequestForClass(tuidriver.ModalClassPermission, "do something")
	payload, err := reg.Record(req, class, "")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := reg.Snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d payloads, want 1", len(got))
	}
	// Mutating the returned Options must not corrupt registry state (proves the clone).
	got[0].Options[0] = protocol.ModalOption{ID: "tampered", Label: "tampered"}

	o, ok := reg.Lookup(payload.ModalID)
	if !ok {
		t.Fatalf("Lookup(%q) after Snapshot: not found", payload.ModalID)
	}
	if o.Options[0].ID == "tampered" {
		t.Error("mutating the snapshot's Options corrupted registry state (Options not cloned)")
	}
	// Enumeration retired nothing: the modal is still one-shot resolvable.
	if _, ok := reg.Resolve(payload.ModalID); !ok {
		t.Error("Resolve after Snapshot: modal was unexpectedly retired")
	}
}
