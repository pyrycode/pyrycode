package main

import (
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// --- #2152 conversation system-prompt read: resolver + payload adapter ---

// sp builds the tri-state prompt pointer a fixture stores.
func sp(s string) *string { return &s }

// countingPromptReader is the spawnedPromptReader double. It answers a fixed map
// of session ids and COUNTS every call, because the claim "an unbound conversation
// never reaches the pool" is about calls and cannot be read off the returned
// values — a session spawned with no operator text reports exactly the "" a
// refusal reports.
type countingPromptReader struct {
	prompts map[sessions.SessionID]string
	calls   int
	// asked records the last id the pool was asked about, so a test can assert the
	// resolver spends the DAEMON's session id rather than the caller's string.
	asked sessions.SessionID
}

func (r *countingPromptReader) SystemPromptFor(id sessions.SessionID) (string, error) {
	r.calls++
	r.asked = id
	prompt, ok := r.prompts[id]
	if !ok {
		return "", sessions.ErrSessionNotFound
	}
	return prompt, nil
}

// promptFixtureRegistry seeds the four conversation shapes the resolver has to
// tell apart.
func promptFixtureRegistry(t *testing.T) *conversations.Registry {
	t.Helper()
	reg := &conversations.Registry{}
	for _, c := range []conversations.Conversation{
		// Bound to a live session, storing text.
		{ID: "conv-live", Cwd: "/tmp", CurrentSessionID: "sess-live", SystemPrompt: sp("Answer only in haiku.")},
		// Bound to a live session, storing the EXPLICITLY EMPTY prompt.
		{ID: "conv-empty", Cwd: "/tmp", CurrentSessionID: "sess-bare", SystemPrompt: sp("")},
		// Hosted, storing text, bound to NOTHING.
		{ID: "conv-unbound", Cwd: "/tmp", SystemPrompt: sp("Answer only in haiku.")},
		// Hosted, storing nothing, bound to a session the pool no longer holds.
		{ID: "conv-evicted", Cwd: "/tmp", CurrentSessionID: "sess-gone"},
	} {
		reg.Create(c)
	}
	return reg
}

// TestResolveConversationPrompt_KeepsTheTwoResolutionFailuresApart pins the
// resolver's central contract: the comma-ok means ONLY "this daemon does not host
// the conversation", and every other shortfall is a successful resolution whose
// spawnedWith is nil.
//
// Collapsing "hosted but running nothing" into the refusal is the plausible
// simplification, and it would suppress the stored value in exactly the case an
// operator most needs to see it — a conversation they have configured but not yet
// started.
func TestResolveConversationPrompt_KeepsTheTwoResolutionFailuresApart(t *testing.T) {
	t.Parallel()

	reader := &countingPromptReader{prompts: map[sessions.SessionID]string{
		"sess-live": "Answer only in haiku.",
		"sess-bare": "",
	}}
	reg := promptFixtureRegistry(t)

	cases := []struct {
		name            string
		convID          string
		wantOK          bool
		wantStored      *string
		wantSpawnedWith *string
	}{
		{
			name:            "bound to a live session: both halves resolve",
			convID:          "conv-live",
			wantOK:          true,
			wantStored:      sp("Answer only in haiku."),
			wantSpawnedWith: sp("Answer only in haiku."),
		},
		{
			name:            "an explicitly empty prompt against a session spawned with none",
			convID:          "conv-empty",
			wantOK:          true,
			wantStored:      sp(""),
			wantSpawnedWith: sp(""),
		},
		{
			name:            "hosted but bound to nothing: a SUCCESS with no session half",
			convID:          "conv-unbound",
			wantOK:          true,
			wantStored:      sp("Answer only in haiku."),
			wantSpawnedWith: nil,
		},
		{
			name:            "bound to a session the pool no longer holds: also a success",
			convID:          "conv-evicted",
			wantOK:          true,
			wantStored:      nil,
			wantSpawnedWith: nil,
		},
		{
			name:   "a conversation this daemon does not host: the ONLY refusal",
			convID: "conv-foreign",
			wantOK: false,
		},
		{
			name:   "an empty conversation id lands in the first guard",
			convID: "",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveConversationPrompt(reg, reader, tc.convID)
			if ok != tc.wantOK {
				t.Fatalf("resolveConversationPrompt(%q) ok = %v, want %v", tc.convID, ok, tc.wantOK)
			}
			if !ok {
				if got != (conversationPromptState{}) {
					t.Errorf("refused resolution returned %+v, want the zero state", got)
				}
				return
			}
			assertPromptPtr(t, "stored", got.stored, tc.wantStored)
			assertPromptPtr(t, "spawnedWith", got.spawnedWith, tc.wantSpawnedWith)
		})
	}
}

// assertPromptPtr compares two tri-state prompt pointers, distinguishing nil from
// a pointer to "" — which is the whole distinction this ticket exists to preserve
// and which a plain equality check on the pointees would erase.
func assertPromptPtr(t *testing.T, field string, got, want *string) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = pointer to %q, want nil — nil and an explicitly empty value are different states", field, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want a pointer to %q", field, *want)
	case want != nil && *got != *want:
		t.Errorf("%s = %q, want %q", field, *got, *want)
	}
}

// TestResolveConversationPrompt_UnboundConversationNeverReachesThePool is the #678
// isolation enforcement point, asserted as a claim about CALLS because the values
// cannot carry it: a conversation bound to nothing must not reach a pool lookup at
// all, so no path here can fall through to a shared session.
//
// The counter is also what makes the unknown-conversation row meaningful — a
// resolver that looked the session up first and refused afterwards would return
// identical values while having touched the pool with a caller-supplied string.
func TestResolveConversationPrompt_UnboundConversationNeverReachesThePool(t *testing.T) {
	t.Parallel()

	reg := promptFixtureRegistry(t)

	for _, convID := range []string{"conv-unbound", "conv-foreign", ""} {
		reader := &countingPromptReader{}
		if _, _ = resolveConversationPrompt(reg, reader, convID); reader.calls != 0 {
			t.Errorf("resolveConversationPrompt(%q) consulted the pool %d time(s), want 0 — an unbound or unknown conversation must be refused before any session lookup",
				convID, reader.calls)
		}
	}
}

// TestResolveConversationPrompt_SpendsTheDaemonsSessionID pins that the id handed
// to the pool comes off the resolved registry record and is never the caller's
// string. It is the seam's stated security property, and the failure it guards
// against — passing convID through — type-checks perfectly.
func TestResolveConversationPrompt_SpendsTheDaemonsSessionID(t *testing.T) {
	t.Parallel()

	reader := &countingPromptReader{prompts: map[sessions.SessionID]string{"sess-live": "Answer only in haiku."}}
	if _, ok := resolveConversationPrompt(promptFixtureRegistry(t), reader, "conv-live"); !ok {
		t.Fatalf("resolveConversationPrompt(conv-live) refused")
	}
	if reader.asked != "sess-live" {
		t.Errorf("pool was asked about %q, want %q — the session id is daemon-authored, read off the resolved record, never the caller's conversation id",
			reader.asked, "sess-live")
	}
}

// TestResolveConversationPrompt_DoesNotAliasTheRegistrysPointer pins that the
// stored value is a COPY of the pointee rather than the registry's own pointer.
// Registry.Get copies the record shallowly, so the field it returns aliases
// registry-held memory — the hazard SetSystemPrompt's block flags for anything
// projecting the field onto a wire payload.
func TestResolveConversationPrompt_DoesNotAliasTheRegistrysPointer(t *testing.T) {
	t.Parallel()

	reg := promptFixtureRegistry(t)
	stored, ok := reg.Get("conv-live")
	if !ok {
		t.Fatalf("fixture conv-live missing")
	}
	got, ok := resolveConversationPrompt(reg, &countingPromptReader{}, "conv-live")
	if !ok {
		t.Fatalf("resolveConversationPrompt(conv-live) refused")
	}
	if got.stored == stored.SystemPrompt {
		t.Errorf("the resolved stored prompt IS the registry's own pointer; it must be a copy of the pointee, so nothing downstream retains registry-held memory")
	}
	if got.stored == nil || *got.stored != *stored.SystemPrompt {
		t.Errorf("the copy does not carry the stored value")
	}
}

// TestSystemPromptStatus_ComparesTheCollapsedStoredValue is the trap this ticket
// turns on, and the reason the verdict is computed here rather than inferred from
// the pointer's presence.
//
// The registry stores three states while Pool.SystemPromptFor collapses two of
// them to "" — so a conversation holding an EXPLICITLY EMPTY prompt whose session
// spawned with no operator text matches, and reporting it as differing would tell
// an operator a session is stale that is running exactly what they stored. The
// row that fails under `st.stored != nil` as a proxy for "has bytes" is the second
// one; the row that fails under a nil-check on spawnedWith that treats a pointer
// to "" as absent is the fourth.
func TestSystemPromptStatus_ComparesTheCollapsedStoredValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		state conversationPromptState
		want  string
	}{
		{
			name:  "no session at all: nothing to compare against",
			state: conversationPromptState{stored: sp("Answer only in haiku.")},
			want:  protocol.SystemPromptStatusNoSession,
		},
		{
			name:  "no prompt stored, session spawned with none",
			state: conversationPromptState{spawnedWith: sp("")},
			want:  protocol.SystemPromptStatusMatches,
		},
		{
			name:  "an EXPLICITLY EMPTY prompt, session spawned with none: the collapse",
			state: conversationPromptState{stored: sp(""), spawnedWith: sp("")},
			want:  protocol.SystemPromptStatusMatches,
		},
		{
			name:  "the same text on both sides",
			state: conversationPromptState{stored: sp("Answer only in haiku."), spawnedWith: sp("Answer only in haiku.")},
			want:  protocol.SystemPromptStatusMatches,
		},
		{
			name:  "no prompt stored, session spawned WITH text: the session predates a clear",
			state: conversationPromptState{spawnedWith: sp("Answer only in haiku.")},
			want:  protocol.SystemPromptStatusDiffers,
		},
		{
			name:  "an explicitly empty prompt against a session spawned with text",
			state: conversationPromptState{stored: sp(""), spawnedWith: sp("Answer only in haiku.")},
			want:  protocol.SystemPromptStatusDiffers,
		},
		{
			name:  "stored text against a session spawned with none: the edit is not live yet",
			state: conversationPromptState{stored: sp("Answer only in haiku."), spawnedWith: sp("")},
			want:  protocol.SystemPromptStatusDiffers,
		},
		{
			name:  "two different texts",
			state: conversationPromptState{stored: sp("Answer only in haiku."), spawnedWith: sp("Answer only in limericks.")},
			want:  protocol.SystemPromptStatusDiffers,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := systemPromptStatus(tc.state); got != tc.want {
				t.Errorf("systemPromptStatus(%+v) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

// TestSystemPromptFor_ShapesTheResolversAnswer pins the adapter: the stored value
// travels to the payload untouched, the verdict is computed, and the comma-ok
// crosses unchanged.
//
// The stored-with-no-session row is the independence check — a "no_session"
// verdict must not suppress the stored value, which is the case a client renders
// as "configured, applies when you next start".
func TestSystemPromptFor_ShapesTheResolversAnswer(t *testing.T) {
	t.Parallel()

	states := map[string]conversationPromptState{
		"conv-live":    {stored: sp("Answer only in haiku."), spawnedWith: sp("Answer only in haiku.")},
		"conv-drifted": {stored: sp("Answer only in haiku."), spawnedWith: sp("Answer only in limericks.")},
		"conv-unbound": {stored: sp("Answer only in haiku.")},
		"conv-quiet":   {},
	}
	seam := systemPromptFor(func(convID string) (conversationPromptState, bool) {
		st, ok := states[convID]
		return st, ok
	})
	if seam == nil {
		t.Fatalf("systemPromptFor returned nil for a non-nil resolver")
	}

	cases := []struct {
		convID string
		want   protocol.SystemPromptPayload
	}{
		{"conv-live", protocol.SystemPromptPayload{SystemPrompt: sp("Answer only in haiku."), SessionPromptStatus: protocol.SystemPromptStatusMatches}},
		{"conv-drifted", protocol.SystemPromptPayload{SystemPrompt: sp("Answer only in haiku."), SessionPromptStatus: protocol.SystemPromptStatusDiffers}},
		{"conv-unbound", protocol.SystemPromptPayload{SystemPrompt: sp("Answer only in haiku."), SessionPromptStatus: protocol.SystemPromptStatusNoSession}},
		{"conv-quiet", protocol.SystemPromptPayload{SessionPromptStatus: protocol.SystemPromptStatusNoSession}},
	}
	for _, tc := range cases {
		got, ok := seam(tc.convID)
		if !ok {
			t.Errorf("systemPromptFor(...)(%q) refused; the resolver answers it, so the adapter dropped the comma-ok", tc.convID)
			continue
		}
		assertPromptPtr(t, tc.convID+" system_prompt", got.SystemPrompt, tc.want.SystemPrompt)
		if got.SessionPromptStatus != tc.want.SessionPromptStatus {
			t.Errorf("systemPromptFor(...)(%q) status = %q, want %q", tc.convID, got.SessionPromptStatus, tc.want.SessionPromptStatus)
		}
	}

	if got, ok := seam("conv-foreign"); ok {
		t.Errorf("systemPromptFor(...)(%q) = (%+v, true); an id the resolver refuses must refuse here too, so internal/relay can answer its constant reply", "conv-foreign", got)
	}
}

// TestSystemPromptFor_NilResolverBuildsNoSeam pins the structural nil rule
// runConfigFor states: the decision is made at BUILD time, before any closure
// exists, so no path can invoke a nil resolver. A wrapper returned here would be
// non-nil even with nothing behind it and would silently defeat internal/relay's
// nil ⇒ constant-reply contract.
func TestSystemPromptFor_NilResolverBuildsNoSeam(t *testing.T) {
	t.Parallel()

	if seam := systemPromptFor(nil); seam != nil {
		t.Errorf("systemPromptFor(nil) returned a non-nil seam; the foreground/v1 wiring must leave the field nil rather than wrap nothing")
	}
}
