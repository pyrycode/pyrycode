package protocol

import (
	"encoding/json"
	"testing"
)

// The conversation every fixture in this file names. One value, so the request
// and the page it is answered with describe ONE walk rather than two unrelated
// frames.
const historyFixtureConvID = "3f8b1c04-9d27-4e5a-b6c1-2e9f70d8a413"

// TestRequestHistoryPayload_RoundTrip pins the ask: the FIRST page of a walk,
// which is the one a client writes before any other and the one whose empty
// cursor carries a meaning ("start at the newest") that an omitted key would
// leave to inference.
//
// InReplyTo is asserted NIL, the structural half of the classification
// cmd/pyry/relay_guard_test.go's excludedTypes records: this frame is a request,
// so correlation runs FROM it. Its envelope id is the in_reply_to the committed
// history_page.json carries, which is what ties the two fixtures into one walk —
// the request_attachment.json / attachment_chunk_retrieval.json pair, run again.
func TestRequestHistoryPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "request_history.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRequestHistory {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRequestHistory)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got pointer to %d, want nil — this frame is a request, not a reply", *env.InReplyTo)
	}
	if env.ID != 140 {
		t.Errorf("ID: got %d, want 140 (the in_reply_to history_page.json carries)", env.ID)
	}

	var payload RequestHistoryPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != historyFixtureConvID {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, historyFixtureConvID)
	}
	// Empty, and that is the fixture's point: an empty cursor asks for the newest
	// page. A walk's first ask has nothing to echo back yet.
	if payload.Cursor != "" {
		t.Errorf("Cursor: got %q, want empty — the first ask starts at the newest entry", payload.Cursor)
	}
	if payload.Limit != 50 {
		t.Errorf("Limit: got %d, want 50", payload.Limit)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestHistoryPagePayload_RoundTrip pins a NON-TERMINAL page: entries, a usable
// cursor, and at_start false. The one shape of the three that a client must ask
// again after.
//
// The in_reply_to assertion is what makes this the answer to the committed
// request_history.json rather than a page of unknown provenance, and it is the
// machine-checked form of the correlation decision — a page rides the envelope,
// so the payload carries no request id and no conversation id.
func TestHistoryPagePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "history_page.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeHistoryPage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeHistoryPage)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 140 {
		t.Errorf("InReplyTo: got %v, want pointer to 140 (request_history.json's envelope id)", env.InReplyTo)
	}

	var payload HistoryPagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.AtStart {
		t.Error("AtStart: got true, want false — this page is not terminal")
	}
	// A non-terminal page must hand back something to ask with. The pairing is
	// asserted from both sides here and in the two terminal tests below, so a
	// fixture carrying both a cursor and at_start would redden.
	if payload.Cursor == "" {
		t.Error("Cursor: got empty, want a usable cursor — a non-terminal page must be walkable")
	}
	if got, want := len(payload.Entries), 2; got != want {
		t.Fatalf("Entries: got %d, want %d", got, want)
	}
	// NEWEST-FIRST, asserted as strictly descending durable ids rather than as two
	// remembered numbers, so a fixture regenerated in the wrong order reddens for
	// the reason that matters instead of on an unrelated value.
	for i := 1; i < len(payload.Entries); i++ {
		if payload.Entries[i-1].ID <= payload.Entries[i].ID {
			t.Errorf("Entries not newest-first: entry %d has id %d, entry %d has id %d",
				i-1, payload.Entries[i-1].ID, i, payload.Entries[i].ID)
		}
	}
	first := payload.Entries[0]
	if first.ID != 412 {
		t.Errorf("Entries[0].ID: got %d, want 412", first.ID)
	}
	// The durable per-conversation entry id, NOT an event_id: the ring's ids are
	// per-process and do not survive a restart, this one is on disk.
	if first.Type != TypeAssistantDelta {
		t.Errorf("Entries[0].Type: got %q, want %q", first.Type, TypeAssistantDelta)
	}
	if first.TS.IsZero() {
		t.Error("Entries[0].TS: got the zero time, want the fixture's timestamp")
	}
	// Carried opaquely: the entry's payload is the stored frame's bytes and
	// nothing in this package decodes it. Asserted as decodable-as-an-object so a
	// fixture that lost the raw bytes to a string reddens.
	var body map[string]json.RawMessage
	if err := json.Unmarshal(first.Payload, &body); err != nil {
		t.Fatalf("Entries[0].Payload does not decode as an object: %v", err)
	}
	if _, ok := body["text"]; !ok {
		t.Error("Entries[0].Payload: no text key — the stored frame's bytes did not survive")
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestHistoryPagePayload_AtStartWithEntries_RoundTrip pins the page shape most
// likely to be got wrong: TERMINAL AND CARRYING ENTRIES.
//
// It exists because the tempting client implementation — "stop when a page comes
// back empty" — reads this page as ordinary and asks again, and the tempting
// daemon implementation reports at_start only on an empty page. Both are wrong
// against internal/history's Page, whose contract is that a walk terminates on
// at_start and never on an empty Entries. Pinned in bytes so neither reading can
// be arrived at from the fixtures.
func TestHistoryPagePayload_AtStartWithEntries_RoundTrip(t *testing.T) {
	raw := readFixture(t, "history_page_at_start_entries.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeHistoryPage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeHistoryPage)
	}
	if env.InReplyTo == nil {
		t.Error("InReplyTo: got nil, want a pointer — a page is correlated to its ask")
	}

	var payload HistoryPagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !payload.AtStart {
		t.Error("AtStart: got false, want true — this page reached the start of the log")
	}
	if len(payload.Entries) != 1 {
		t.Fatalf("Entries: got %d, want 1 — a terminal page may still carry entries", len(payload.Entries))
	}
	// The two are never both meaningful: Cursor is empty whenever AtStart is set,
	// so there is nothing to ask again with and nothing to ask again for.
	if payload.Cursor != "" {
		t.Errorf("Cursor: got %q, want empty — a terminal page hands back nothing to walk with", payload.Cursor)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestHistoryPagePayload_AtStartEmpty_RoundTrip pins the third and last shape:
// terminal with NO entries — the answer to the ask after a page that filled
// exactly at the log's first entry.
//
// The empty list is written as [] in the committed bytes, not as an omitted key
// and not as null. A nil slice and an empty slice do not decode alike for a
// client whose array type is non-optional, so the form is pinned rather than
// left to whatever the producer happens to emit.
func TestHistoryPagePayload_AtStartEmpty_RoundTrip(t *testing.T) {
	raw := readFixture(t, "history_page_at_start.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeHistoryPage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeHistoryPage)
	}

	var payload HistoryPagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !payload.AtStart {
		t.Error("AtStart: got false, want true")
	}
	if len(payload.Entries) != 0 {
		t.Errorf("Entries: got %d, want 0", len(payload.Entries))
	}
	if payload.Cursor != "" {
		t.Errorf("Cursor: got %q, want empty", payload.Cursor)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestHistoryPagePayload_NilEntries_EncodesAsEmptyArray is the OTHER half of the
// empty-list pin, and it is the half the fixture above cannot cover.
//
// The fixture proves that [] decodes and survives a round trip. It cannot prove
// what a PRODUCER emits from a page it never filled: a nil Entries marshals as
// null without MarshalJSON, and null fails the decode of a client whose array
// type is non-optional. So this asserts from a zero value — the shape a producer
// reaches when a conversation has no log at all.
//
// The assertion reads the OUTER key's raw bytes directly. Reaching an entry to
// check the array would force it non-empty and the assertion would go vacuous
// against exactly the mutant it exists for.
func TestHistoryPagePayload_NilEntries_EncodesAsEmptyArray(t *testing.T) {
	b, err := json.Marshal(HistoryPagePayload{AtStart: true})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal to key map: %v", err)
	}
	raw, ok := got["entries"]
	if !ok {
		t.Fatal("entries key absent — an empty page must still name the key")
	}
	if string(raw) != "[]" {
		t.Errorf("entries: got %s, want [] — a nil slice must not reach the wire as null", raw)
	}
}

// TestRequestHistoryPayload_WireKeys pins the ask's COMPLETE key set, two-sided:
// every expected key present AND no unexpected key. A one-sided containment
// check is what lets an added field through, which is the whole mutant class
// this test exists for.
//
// It is the machine-checked form of the frame's central omission — NO REQUEST-ID
// KEY, because correlation rides the envelope's in_reply_to — rather than a
// property a reviewer has to notice. It is load-bearing beyond the round trip
// above for the reason that round trip cannot cover: an added field breaks the
// byte comparison there too, but only until somebody regenerates the fixture.
//
// Marshalled from a ZERO VALUE, so it doubles as the no-omitempty pin: all three
// keys must be present on a payload that sets none of them. An omitempty added
// later for tidiness would silently change the wire, and reddens here instead.
func TestRequestHistoryPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(RequestHistoryPayload{})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	assertWireKeys(t, b, "conversation_id", "cursor", "limit")
}

// TestHistoryPagePayload_WireKeys pins the page's key set on the same two-sided
// rule. Here the central omission is a different one: NO conversation_id. The
// page is correlated by in_reply_to and echoes nothing back from the request, so
// a conversation id arriving as a field reddens by construction.
func TestHistoryPagePayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(HistoryPagePayload{})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	assertWireKeys(t, b, "entries", "cursor", "at_start")
}

// TestHistoryEntry_WireKeys pins the entry's key set, which is the one that must
// MIRROR internal/history's Entry key for key. A key added here without a
// counterpart in the log is a field no producer can fill, and a key renamed here
// silently breaks the mapping the producer (#2116) writes.
//
// event_id is absent deliberately and this is where that is checked: an entry's
// id is the DURABLE per-conversation one from the log, not the per-process ring
// id, and the two must never be confused by a client reducing a page through the
// timeline it already has.
func TestHistoryEntry_WireKeys(t *testing.T) {
	b, err := json.Marshal(HistoryEntry{})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	assertWireKeys(t, b, "id", "type", "payload", "ts")
}

// assertWireKeys checks a marshalled payload's key set exactly: every wanted key
// present, and nothing else. Two-sided on purpose — see the callers.
func assertWireKeys(t *testing.T, b []byte, want ...string) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal to key map: %v", err)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q", k)
		}
	}
	wanted := make(map[string]bool, len(want))
	for _, k := range want {
		wanted[k] = true
	}
	for k := range got {
		if !wanted[k] {
			t.Errorf("unexpected wire key %q — the key set is the published contract", k)
		}
	}
}
