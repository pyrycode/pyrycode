package protocol

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// TestAttachmentChunkPayload_Upload_RoundTrip pins the CLIENT → DAEMON leg: the
// first chunk of a two-chunk upload, on a plain envelope with no in_reply_to.
// The absent in_reply_to is what makes this fixture structurally different from
// its retrieval sibling rather than merely differently-valued.
//
// filename carries a character encoding/json escapes, and that is the point of
// the fixture rather than decoration: the committed bytes read
// "report \u003cdraft\u003e.pdf", the one place in this package's testdata where
// HTML escaping is visible — and that escaping is the property the whole of
// MaxAttachmentChunkBytes' arithmetic rests on.
//
// index is 0 deliberately. A populated fixture only earns omitempty coverage on
// a key whose value is that key's own zero value, and index is the only such key
// here — measured against all nine mutants, not predicted: index's omitempty is
// the one that reddens this test. That coverage does not survive regeneration
// (regenerate the fixture under the mutant and this round trip goes green
// again), so attachment_chunk_zero.json's byte guards are what hold every key.
//
// conversation_id is the field this leg exists to show (#2142). It is the upload
// leg that gives the field meaning — the client names where the bytes belong —
// so this is the fixture a reader reaches for to learn its shape, and the value
// is the lowercase UUIDv4 § Attachments publishes rather than one of the short
// legacy ids older fixtures in this package carry. It is the same conversation
// request_attachment.json names, so the family's fixtures describe one
// conversation rather than three unrelated ones.
//
// The data blob is a handful of synthetic ASCII bytes. The fixtures pin the
// encoding at small size; TestAttachmentChunkPayload_FitV2EnvelopeCap measures a
// chunk filled to the bound. Neither one alone covers both.
func TestAttachmentChunkPayload_Upload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "attachment_chunk_upload.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeAttachmentChunk {
		t.Errorf("Type: got %q, want %q", env.Type, TypeAttachmentChunk)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got pointer to %d, want nil on the client → daemon leg", *env.InReplyTo)
	}

	var payload AttachmentChunkPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if want := "9d4e7a21-8c05-4f3b-b6e2-1a7c9e30d5f4"; payload.ConversationID != want {
		t.Errorf("ConversationID: got %q, want %q — the upload leg is where this field means something", payload.ConversationID, want)
	}
	if want := "3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67"; payload.AttachmentID != want {
		t.Errorf("AttachmentID: got %q, want %q", payload.AttachmentID, want)
	}
	if payload.Index != 0 {
		t.Errorf("Index: got %d, want 0", payload.Index)
	}
	if payload.TotalChunks != 2 {
		t.Errorf("TotalChunks: got %d, want 2", payload.TotalChunks)
	}
	// The escaped-and-unescaped pair: the wire carries \u003c, the decoded value
	// carries '<'. Both halves are asserted, because either one alone would go on
	// passing if the encoder stopped escaping.
	if want := "report <draft>.pdf"; payload.Filename != want {
		t.Errorf("Filename: got %q, want %q", payload.Filename, want)
	}
	if want := []byte(`"filename":"report \u003cdraft\u003e.pdf"`); !bytes.Contains(canonical(t, raw), want) {
		t.Errorf("fixture must carry the HTML-escaped filename %s, got: %s", want, raw)
	}
	if want := "application/pdf"; payload.MimeType != want {
		t.Errorf("MimeType: got %q, want %q", payload.MimeType, want)
	}
	if payload.Size != 54 {
		t.Errorf("Size: got %d, want 54 (both chunks of the whole file)", payload.Size)
	}
	if want := "b78d66ba036a61e9f2c5b781d0077270bfafa7e94e0a8bfd5910493a57a76902"; payload.SHA256 != want {
		t.Errorf("SHA256: got %q, want %q", payload.SHA256, want)
	}
	if want := []byte("attachment upload, chunk 0\n"); !bytes.Equal(payload.Data, want) {
		t.Errorf("Data: got %q, want %q", payload.Data, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestAttachmentChunkPayload_Retrieval_RoundTrip pins the DAEMON → CLIENT leg:
// the second chunk of a two-chunk retrieval, on an envelope carrying
// in_reply_to. Retrieval answers the request verb #2052 declares
// (TypeRequestAttachment), so it takes the response shape every other daemon →
// client frame in this testdata takes (conversations.json carries in_reply_to;
// send_message.json does not).
//
// The in_reply_to assertion is the only thing distinguishing this test from its
// upload sibling. Without it "both directions" would be two value sets over one
// shape. What is pinned here is the ENVELOPE a daemon → client chunk rides, and
// the question this comment used to leave open — whether and how the retrieval
// verb correlates — is settled: correlation rides in_reply_to and the request
// payload carries no request-id key (#2052). The 91 below is not an arbitrary
// number any more either. It is the envelope id of the committed
// request_attachment.json, so the two fixtures describe one retrieval, and
// TestRequestAttachmentPayload_RoundTrip asserts the same value from the other
// end.
//
// conversation_id RIDES THIS LEG EMPTY, and that is committed in bytes here
// rather than stated in prose (#2142). The field is meaningful on the upload leg
// only: a retrieval chunk is correlated by in_reply_to to a request_attachment
// that already named the conversation, so the client knows it and the daemon
// echoes nothing — the same reason attachment_stored and history_page carry no
// conversation_id either. The tie above is what makes the emptiness checkable
// rather than merely asserted: envelope 91 IS request_attachment.json, whose
// payload names 9d4e7a21-8c05-4f3b-b6e2-1a7c9e30d5f4, so a reader can see the
// conversation this chunk belongs to without the chunk naming it.
func TestAttachmentChunkPayload_Retrieval_RoundTrip(t *testing.T) {
	raw := readFixture(t, "attachment_chunk_retrieval.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeAttachmentChunk {
		t.Errorf("Type: got %q, want %q", env.Type, TypeAttachmentChunk)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 91 {
		t.Errorf("InReplyTo: got %v, want pointer to 91", env.InReplyTo)
	}

	var payload AttachmentChunkPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "" {
		t.Errorf("ConversationID: got %q, want empty — the retrieval leg emits it empty and a receiver ignores it, "+
			"because in_reply_to already correlates this chunk to a request that named the conversation",
			payload.ConversationID)
	}
	if want := "7c1d5e92-4a30-4b8f-9e21-6d4c3b0a8f55"; payload.AttachmentID != want {
		t.Errorf("AttachmentID: got %q, want %q", payload.AttachmentID, want)
	}
	if payload.Index != 1 {
		t.Errorf("Index: got %d, want 1", payload.Index)
	}
	if payload.TotalChunks != 2 {
		t.Errorf("TotalChunks: got %d, want 2", payload.TotalChunks)
	}
	if want := "screenshot.png"; payload.Filename != want {
		t.Errorf("Filename: got %q, want %q", payload.Filename, want)
	}
	if want := "image/png"; payload.MimeType != want {
		t.Errorf("MimeType: got %q, want %q", payload.MimeType, want)
	}
	if payload.Size != 60 {
		t.Errorf("Size: got %d, want 60 (both chunks of the whole file)", payload.Size)
	}
	if want := "bf848ca98a786db9fe841b727fa49abab5364b097f88f43dba6eb515ff701e22"; payload.SHA256 != want {
		t.Errorf("SHA256: got %q, want %q", payload.SHA256, want)
	}
	if want := []byte("attachment retrieval, chunk 1\n"); !bytes.Equal(payload.Data, want) {
		t.Errorf("Data: got %q, want %q", payload.Data, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestAttachmentChunkPayload_ZeroValue_RoundTrip pins the encoding of every
// field's zero value, which is what AttachmentChunkPayload's no-omitempty rule
// actually asserts.
//
// The nine byte guards, not the round trip, are what make "explicit zero, not
// elided" checkable at all. The fixtures here are generated by marshalling
// through this package, so an omitempty added to a field would elide its key
// from the regenerated fixture and from the decode alike and the round trip
// would go on passing — the guards stay red across that regeneration. Measured
// across all nine mutants, re-run rather than extended by analogy when #2142
// added the ninth key: as committed, index and conversation_id also redden a
// directional fixture and the other seven redden here alone; once the fixtures
// are regenerated under the mutant, all nine rest on this test alone.
//
// conversation_id joining index in that first group is a property of the
// retrieval fixture rather than a coincidence. A populated fixture earns
// omitempty coverage only on a key whose value is that key's own zero value, and
// the retrieval leg commits this key EMPTY on purpose (#2142) — so
// attachment_chunk_retrieval.json holds it the way attachment_chunk_upload.json
// holds index's 0.
//
// total_chunks: 0 contradicts AttachmentChunkPayload's documented >= 1 on
// purpose. This is a frame no producer will ever emit; it exists for the
// encoding, not the scenario, exactly as model_list_zero.json's single all-zero
// entry does.
func TestAttachmentChunkPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "attachment_chunk_zero.json")

	// All nine wire keys, because no field carries omitempty and this is the
	// fixture AttachmentChunkPayload's doc means by "pin the full shape".
	for _, want := range []string{
		`"conversation_id":""`,
		`"attachment_id":""`,
		`"index":0`,
		`"total_chunks":0`,
		`"filename":""`,
		`"mime_type":""`,
		`"size":0`,
		`"sha256":""`,
		`"data":null`,
	} {
		if !bytes.Contains(canonical(t, raw), []byte(want)) {
			t.Errorf("fixture must carry %s explicitly at its zero value, got: %s", want, raw)
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeAttachmentChunk {
		t.Errorf("Type: got %q, want %q", env.Type, TypeAttachmentChunk)
	}

	var payload AttachmentChunkPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "")
	}
	if payload.AttachmentID != "" {
		t.Errorf("AttachmentID: got %q, want %q", payload.AttachmentID, "")
	}
	if payload.Index != 0 {
		t.Errorf("Index: got %d, want 0", payload.Index)
	}
	if payload.TotalChunks != 0 {
		t.Errorf("TotalChunks: got %d, want 0", payload.TotalChunks)
	}
	if payload.Filename != "" {
		t.Errorf("Filename: got %q, want %q", payload.Filename, "")
	}
	if payload.MimeType != "" {
		t.Errorf("MimeType: got %q, want %q", payload.MimeType, "")
	}
	if payload.Size != 0 {
		t.Errorf("Size: got %d, want 0", payload.Size)
	}
	if payload.SHA256 != "" {
		t.Errorf("SHA256: got %q, want %q", payload.SHA256, "")
	}
	// nil marshals to null while []byte{} marshals to "": the fixture commits to
	// null, so the decode side has to pin nil specifically.
	if payload.Data != nil {
		t.Errorf("Data: got %q, want nil", payload.Data)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// attachmentSHA256HexLen mirrors AttachmentChunkPayload.SHA256's declared
// "always 64 hex characters" — a contract in prose with no validator behind it,
// copied here for the same reason the capInput* block copies the bridge's
// caps: the fill below has to trace to a bound recorded in shipped code rather
// than to a number chosen inside this test.
//
// It stays a TEST-LOCAL mirror rather than becoming a fifth exported constant
// beside a hex validator. #1770, the slice that added the receiver's digest
// comparison in attachments.Accumulator's Assemble, is the one that would have
// written that validator and declined: an uppercase, truncated, non-hex or
// empty claim already loses the exact-equality comparison there and is an
// integrity reject, so a shape check would add a production file in this
// package for a check that changes no outcome — and one gated on the claim's
// shape would be an integrity opt-out the sender controls.
const attachmentSHA256HexLen = 64

// TestAttachmentChunkPayload_FitV2EnvelopeCap fills a chunk to
// MaxAttachmentChunkBytes with every metadata field simultaneously at its
// worst case for JSON escaping, and proves the serialised envelope still fits
// under the v2 application-envelope cap. Per-field bounds do not compose into
// an envelope guarantee on their own, so this is measured rather than argued —
// TestToolUsePayload_FitV2EnvelopeCap's shape and its statement.
//
// The fill is '<', not 'a': SetEscapeHTML is on by default, so one such input
// byte costs six on the wire and an 'a' fill under-reports by over 5x. The
// constant's own comment budgets 4591 B of metadata against 60000 B of base64
// data, leaving ~900 B spare; this test measures the real total and asserts the
// one thing that matters, that it lands under the cap.
//
// EVERY FIELD MUST APPEAR IN THE FILL, and this test cannot tell you when one
// does not. It measures whatever payload it is handed, so a field added to the
// struct and forgotten here leaves the test green while re-proving the previous
// frame — #2142 added conversation_id and named that exactly: the guard that
// greens whether or not the work is done. When a field lands on this type, its
// fill line lands in the same edit, and the logged total is read against the
// constant's table rather than assumed to have moved.
//
// Two fills deliberately exceed what a contract-respecting frame can carry, and
// both are cheap against that headroom:
//
//   - sha256 is 64 '<', not 64 hex characters. Hex never escapes, so the
//     contract-respecting worst case is 64 B and this costs 384 B. The extra
//     320 B buys a proof that holds for a length-respecting but non-hex value,
//     which is exactly what an inbound attacker sends.
//   - index, total_chunks and size are at the widest decimal their declared Go
//     types admit, even though the contract says Index >= 0, TotalChunks >= 1
//     and Size is a byte length. Nothing enforces those yet, and the declared
//     type IS the bound recorded in shipped code. A negative size here is the
//     ceiling, not a mistake.
//
// The metadata bounds this fills to are DECLARED HERE AND ENFORCED NOWHERE, so
// this is a proof about frames that respect them: #1741 makes them true inbound,
// #1897 and #2053 outbound. Until then a frame violating them exceeds the cap and
// the transport drops it. FOUR FIELDS UNDER THREE CONSTANTS since #2142, because
// MaxAttachmentIDBytes budgets both ids; conversation_id is the field #1741
// predates, so nothing inbound reads its bound at all — #2143 gates that field on
// registry membership, which is not a length check.
//
// Neither the Logf nor the failure message prints the marshalled bytes — lengths
// only, as the tool_use precedent does. A 64 KB dump is unreadable in CI, and
// Data is the field AttachmentChunkPayload marks content-bearing and NEVER
// LOGGED; #1744 enforces that rule and will copy whatever this test does.
func TestAttachmentChunkPayload_FitV2EnvelopeCap(t *testing.T) {
	fill := func(n int) string { return strings.Repeat("<", n) }

	payload := AttachmentChunkPayload{
		// Both id-shaped fields fill to the one constant that budgets them:
		// MaxAttachmentIDBytes bounds two fields on this frame since #2142, not
		// one, and no second constant was minted for the conversation id.
		ConversationID: fill(MaxAttachmentIDBytes),
		AttachmentID:   fill(MaxAttachmentIDBytes),
		Index:          math.MinInt,
		TotalChunks:    math.MinInt,
		Filename:       fill(MaxAttachmentFilenameBytes),
		MimeType:       fill(MaxAttachmentMimeTypeBytes),
		Size:           math.MinInt64,
		SHA256:         fill(attachmentSHA256HexLen),
		Data:           bytes.Repeat([]byte{0xFF}, MaxAttachmentChunkBytes),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	// Worst-case envelope too: max-uint64 on all three ids, payload_encrypted
	// present, and a ts at full nanosecond width. payload_encrypted is a byte
	// ceiling and not a scenario — IsKnownAppType rejects an inbound frame
	// carrying it — but 25 bytes is cheaper than the argument. EventID is set for
	// the same ceiling reason and is NOT a claim that this frame rides the event
	// ring: at 45000 raw bytes a frame the ring retains is a per-conversation
	// memory multiplier rather than a per-frame cap, and ring membership is
	// #1744's call.
	maxID := ^uint64(0)
	inReplyTo := ^uint64(0)
	eventID := ^uint64(0)
	env := Envelope{
		ID:               maxID,
		Type:             TypeAttachmentChunk,
		TS:               time.Date(2026, 8, 25, 10, 33, 18, 123456789, time.UTC),
		Payload:          body,
		InReplyTo:        &inReplyTo,
		EventID:          &eventID,
		PayloadEncrypted: true,
	}
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	t.Logf("attachment_chunk at full bounds: %d B, %.1f%% of the %d-byte v2 application-envelope cap",
		len(out), float64(len(out))/float64(maxV2AppEnvelope)*100, maxV2AppEnvelope)
	if len(out) >= maxV2AppEnvelope {
		t.Errorf("serialised envelope: got %d B, want < %d B", len(out), maxV2AppEnvelope)
	}
}

// TestAttachmentStoredPayload_RoundTrip pins the upload leg's success reply
// against the committed fixture: the envelope type, the correlation that rides
// in_reply_to, the one payload key and its value.
//
// THE FIXTURE'S in_reply_to IS THE CONTRACT, not an arbitrary number. It points
// at the envelope of the chunk WHOSE ARRIVAL COMPLETED THE TRANSFER, and the
// value chosen here makes that checkable rather than merely stated: the transfer
// is the two-chunk upload of attachment_chunk_upload.json, whose committed chunk
// 0 rode envelope 812 — so this frame answers 814, the uncommitted chunk 1, and
// NOT the 812 a reader would reach for. § Attachments publishes that chunks may
// arrive in any order, so the completing chunk is whichever one closed the set
// and a client cannot predict which of its envelope ids that will be. That is
// exactly why attachment_id also rides the payload: in_reply_to says which frame
// this answers, the payload says which transfer it concludes, and only the second
// is something the client chose and can look up.
//
// InReplyTo is asserted NON-NIL, which is the structural half of the reply
// classification cmd/pyry/relay_guard_test.go's excludedTypes records. Its
// sibling TestAttachmentChunkPayload_Upload_RoundTrip asserts nil on the same
// leg, so the pair says the chunk is not a reply and this frame is — a
// difference in shape rather than in values.
//
// The attachment_id is the one attachment_chunk_upload.json carries, so the two
// fixtures describe ONE transfer rather than two unrelated ones. It is also a
// canonical id under the shape docs/protocol-mobile.md § Attachments now
// publishes — 36 bytes, lowercase hex, dashes at 8/13/18/23, '4' at 14, 'a' at
// 19 — so the published rule appears in committed bytes and not only in prose.
func TestAttachmentStoredPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "attachment_stored.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeAttachmentStored {
		t.Errorf("Type: got %q, want %q", env.Type, TypeAttachmentStored)
	}
	if env.InReplyTo == nil {
		t.Errorf("InReplyTo: got nil, want the completing chunk's envelope id — this frame is a reply")
	} else if *env.InReplyTo != 814 {
		t.Errorf("InReplyTo: got pointer to %d, want pointer to 814", *env.InReplyTo)
	}

	var payload AttachmentStoredPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if want := "3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67"; payload.AttachmentID != want {
		t.Errorf("AttachmentID: got %q, want %q", payload.AttachmentID, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestAttachmentStoredPayload_WireKeys pins the payload's COMPLETE set of wire
// keys, so a later field cannot be added without this failing. It is the
// machine-checked form of the frame's central omission — no host path, no
// directory component, no on-disk filename — rather than a property a reviewer
// has to notice: any of those arriving as a field reddens here by construction.
//
// It is load-bearing beyond TestAttachmentStoredPayload_RoundTrip for a reason
// that round trip cannot cover. An added field breaks the byte comparison there
// too, but only until somebody regenerates the fixture; regenerate it under the
// same mutant and the round trip goes green again while this stays red. That is
// the argument TestAttachmentChunkPayload_Upload_RoundTrip records for its own
// coverage not surviving regeneration, applied to the key set instead of to
// omitempty.
//
// The assertion is two-sided on purpose — every expected key present AND no
// unexpected key — because a one-sided containment check is what lets an added
// field through, and that is the whole mutant class this test exists for.
func TestAttachmentStoredPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(AttachmentStoredPayload{AttachmentID: "3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"attachment_id": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the payload's key set is fixed at %v, and this frame must expose no host path, no directory component and no on-disk filename", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s", k, b)
		}
	}
}

// TestRequestAttachmentPayload_RoundTrip pins the retrieval REQUEST against its
// committed fixture: the envelope type, the absent in_reply_to, and both ids.
//
// It is also the only test in this package that pins the wire STRING
// "request_attachment". None of the three registries in compat_test.go can — all
// three key on the Go symbol, so renaming the constant's value moves through them
// consistently and reddens none, measured by mutant on #1895's sibling.
//
// THE ENVELOPE ID IS THE CONTRACT, not an arbitrary number. It is 91, which is
// exactly the in_reply_to the committed attachment_chunk_retrieval.json carries,
// and the attachment_id is that fixture's. So the two files describe ONE
// retrieval — request, then the chunk answering it — and the correlation decision
// this frame records in prose (it rides the envelope, so the payload carries no
// request-id key) appears in committed bytes. That is the tie
// attachment_stored.json made for the upload leg, run the other way.
//
// InReplyTo is asserted NIL, which is the structural half of the classification
// cmd/pyry/relay_guard_test.go's excludedTypes records: this frame is a request,
// not a reply, and the correlation runs from it rather than to it. Its sibling
// TestAttachmentStoredPayload_RoundTrip asserts non-nil on the same tie, so the
// pair is a difference in shape rather than in values.
func TestRequestAttachmentPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "request_attachment.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRequestAttachment {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRequestAttachment)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got pointer to %d, want nil — this frame is a request, not a reply", *env.InReplyTo)
	}
	// The value the retrieval chunk answers. Asserted here so the pair cannot
	// drift into two unrelated fixtures.
	if env.ID != 91 {
		t.Errorf("ID: got %d, want 91 (the in_reply_to attachment_chunk_retrieval.json carries)", env.ID)
	}

	var payload RequestAttachmentPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if want := "9d4e7a21-8c05-4f3b-b6e2-1a7c9e30d5f4"; payload.ConversationID != want {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, want)
	}
	if want := "7c1d5e92-4a30-4b8f-9e21-6d4c3b0a8f55"; payload.AttachmentID != want {
		t.Errorf("AttachmentID: got %q, want %q", payload.AttachmentID, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestRequestAttachmentPayload_WireKeys pins the payload's COMPLETE set of wire
// keys, so a later field cannot be added without this failing. It is the
// machine-checked form of the frame's central omission — NO REQUEST-ID KEY,
// because correlation rides the envelope's in_reply_to for the reasons
// AttachmentStoredPayload records — rather than a property a reviewer has to
// notice: a request_id, a nonce or a token arriving as a field reddens here by
// construction, and the retrieval fixture that already landed would then be
// describing a different scheme.
//
// It is load-bearing beyond the round trip above for the reason that round trip
// cannot cover. An added field breaks the byte comparison there too, but only
// until somebody regenerates the fixture; regenerate it under the same mutant and
// the round trip goes green again while this stays red.
//
// The assertion is two-sided on purpose — every expected key present AND no
// unexpected key — because a one-sided containment check is what lets an added
// field through, and that is the whole mutant class this test exists for.
func TestRequestAttachmentPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(RequestAttachmentPayload{
		ConversationID: "9d4e7a21-8c05-4f3b-b6e2-1a7c9e30d5f4",
		AttachmentID:   "7c1d5e92-4a30-4b8f-9e21-6d4c3b0a8f55",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"conversation_id": true, "attachment_id": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the payload's key set is fixed at %v, and this frame must carry no request-id key — correlation rides the envelope's in_reply_to", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s", k, b)
		}
	}
}

// TestRequestAttachmentPayload_ZeroValue_KeysPresent is the omitempty pin, and
// the reason it exists separately from both tests above is that omitempty elides
// a key only at its zero value: both of those marshal non-empty ids, so an
// omitempty added to either field leaves them entirely green — including the
// key-set check, which would still see the keys it expects.
//
// A marshalled zero value rather than a second fixture, for
// AttachmentStoredPayload's reason: the payload is flat, so one all-zero struct
// reaches every key and the extra file a nested shape would need buys nothing.
//
// The zero value is also the frame RequestAttachmentPayload's doc block warns
// about. Every key is optional to encoding/json, so a truncated or hostile
// payload decodes to exactly this — two empty strings, no error — which is why
// the block requires a consumer to resolve nothing from them rather than joining
// them into a path.
func TestRequestAttachmentPayload_ZeroValue_KeysPresent(t *testing.T) {
	b, err := json.Marshal(RequestAttachmentPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	for _, want := range []string{`"conversation_id":""`, `"attachment_id":""`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("zero payload must carry %s explicitly, got: %s", want, b)
		}
	}
}

// TestAttachmentStoredPayload_ZeroValue_KeysPresent is the omitempty pin. It is a
// marshalled zero value rather than a second fixture because this payload is
// flat: one all-zero struct reaches every key, so the extra file a nested shape
// would need buys nothing.
//
// It has to exist separately from both tests above. omitempty elides a key only
// at its zero value, and both of those marshal a non-empty id, so an omitempty
// added to the field leaves each of them entirely green — including the key-set
// check, which would still see the key it expects.
func TestAttachmentStoredPayload_ZeroValue_KeysPresent(t *testing.T) {
	b, err := json.Marshal(AttachmentStoredPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	if want := `"attachment_id":""`; !bytes.Contains(b, []byte(want)) {
		t.Errorf("zero payload must carry %s explicitly, got: %s", want, b)
	}
}

// TestAttachmentOfferedPayload_RoundTrip pins the announcement frame's wire
// bytes, and it is the ONLY test in this package that can catch a typo in the
// wire STRING. All three registries in compat_test.go — TestIsKnownAppType's
// cases, v2OnlyTypes and TestTypeConstants_V1V2Partition's list — key on the Go
// symbol TypeAttachmentOffered and never re-derive the literal it holds, so
// mutating "attachment_offered" moves consistently through all three and reddens
// none of them. That was confirmed by mutant run on TypeAttachmentStored rather
// than assumed.
//
// The two nil assertions are the frame's shape, not decoration. This is an
// UNSOLICITED PUSH: nothing solicits it, so there is no request envelope for
// in_reply_to to name — the difference from AttachmentStoredPayload, which is
// outbound-only too but correlates to the chunk that completed the transfer.
// Whether a producer stamps an event_id is #2083's call and not a property of the
// declared shape, so the fixture omits it and this pins that the fixture does.
//
// The three payload values are PAIRWISE DISTINCT on purpose. roundTripEnvelope
// compares canonical bytes, so a field-reordering mutant re-encodes identically
// and passes green whenever the two swapped keys share a value.
func TestAttachmentOfferedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "attachment_offered.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeAttachmentOffered {
		t.Errorf("Type: got %q, want %q", env.Type, TypeAttachmentOffered)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got pointer to %d, want nil — this frame is an unsolicited push, not a reply", *env.InReplyTo)
	}
	if env.EventID != nil {
		t.Errorf("EventID: got pointer to %d, want nil — whether a producer stamps one is #2083's, not part of the declared shape", *env.EventID)
	}

	var payload AttachmentOfferedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if want := "9d4e7a21-8c05-4f3b-b6e2-1a7c9e30d5f4"; payload.ConversationID != want {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, want)
	}
	if want := "b8e0c374-2f61-4a95-8d0e-5c37a91b6e28"; payload.AttachmentID != want {
		t.Errorf("AttachmentID: got %q, want %q", payload.AttachmentID, want)
	}
	if want := "quarterly-summary.png"; payload.Filename != want {
		t.Errorf("Filename: got %q, want %q", payload.Filename, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestAttachmentOfferedPayload_WireKeys pins the payload's COMPLETE set of wire
// keys, so a later field cannot be added without this failing. It is the
// machine-checked form of the frame's central omission — NO turn_id, the field a
// reader reaches for first — rather than a property a reviewer has to notice: a
// turn id, a message id or a mime type arriving as a field reddens here by
// construction.
//
// It is load-bearing beyond the round trip above for the reason that round trip
// cannot cover. An added field breaks the byte comparison there too, but only
// until somebody regenerates the fixture; regenerate it under the same mutant and
// the round trip goes green again while this stays red.
//
// The assertion is two-sided on purpose — every expected key present AND no
// unexpected key — because a one-sided containment check is what lets an added
// field through, and that is the whole mutant class this test exists for.
func TestAttachmentOfferedPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(AttachmentOfferedPayload{
		ConversationID: "9d4e7a21-8c05-4f3b-b6e2-1a7c9e30d5f4",
		AttachmentID:   "b8e0c374-2f61-4a95-8d0e-5c37a91b6e28",
		Filename:       "quarterly-summary.png",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"conversation_id": true, "attachment_id": true, "filename": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the payload's key set is fixed at %v, and this frame must carry no turn_id — no turn id is reachable from the lane it is emitted on", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s", k, b)
		}
	}
}

// TestAttachmentOfferedPayload_ZeroValue_KeysPresent is the omitempty pin, and
// the reason it exists separately from both tests above is that omitempty elides
// a key only at its zero value: both of those marshal non-empty values, so an
// omitempty added to any of the three fields leaves them entirely green —
// including the key-set check, which would still see the keys it expects.
//
// A marshalled zero value rather than a second fixture, for
// AttachmentStoredPayload's reason: the payload is flat, so one all-zero struct
// reaches every key and the extra file a nested shape would need buys nothing.
//
// The zero value is also the frame AttachmentOfferedPayload's doc block warns
// about. Every key is optional to encoding/json, so a truncated or hostile
// payload decodes to exactly this — three empty strings, no error — which is why
// the block requires a consumer to resolve nothing from them rather than joining
// them into a path.
func TestAttachmentOfferedPayload_ZeroValue_KeysPresent(t *testing.T) {
	b, err := json.Marshal(AttachmentOfferedPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	for _, want := range []string{`"conversation_id":""`, `"attachment_id":""`, `"filename":""`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("zero payload must carry %s explicitly, got: %s", want, b)
		}
	}
}

// TestReadWorkspaceFilePayload_WireKeys pins the payload's complete key set in
// both directions, and that both keys survive at the zero value (no omitempty).
// A request-id key, a nonce or a token added later reddens here: correlation
// rides the envelope's in_reply_to.
func TestReadWorkspaceFilePayload_WireKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    ReadWorkspaceFilePayload
	}{
		{"populated", ReadWorkspaceFilePayload{ConversationID: "9d4e7a21-8c05-4f3b-b6e2-1a7c9e30d5f4", Path: "notes/plan.md"}},
		{"zero", ReadWorkspaceFilePayload{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.p)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("unmarshal payload into key set: %v", err)
			}
			want := map[string]bool{"conversation_id": true, "path": true}
			for k := range got {
				if !want[k] {
					t.Errorf("unexpected wire key %q, want exactly %v", k, want)
				}
			}
			for k := range want {
				if _, ok := got[k]; !ok {
					t.Errorf("missing wire key %q, got: %s", k, b)
				}
			}
		})
	}
}
