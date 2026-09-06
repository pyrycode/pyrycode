//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_AttachmentUploadMultiChunk is the first end-to-end run of the
// inbound attachment-upload leg. Every slice of it is unit-tested inside its own
// package — chunk accumulation and the integrity checks, the admission bounds,
// filename sanitisation, directory resolution, the byte write, the in-flight
// registry, the intake driver, and the v2 dispatch arm that joins them — and
// nothing until now has run the whole chain in one process.
//
// THE FAILURE THIS EXISTS TO CATCH is the quiet one: every layer accepts the
// chunk stream, the phone is told attachment_stored, and no bytes reach the host.
// A reply and a file are two observations and only the second is evidence, so
// this run makes both.
//
// One transfer, four observations (the ticket's acceptance criteria):
//
//   - AC-1 an attachment_stored whose in_reply_to is the envelope of the chunk
//     that COMPLETED the transfer and whose payload names the run's own id;
//   - AC-2 the stored file's bytes equal the sent bytes concatenated in INDEX
//     order;
//   - AC-3 exactly one attachment_stored for the whole transfer, and the chunk
//     that left it waiting drew no frame at all;
//   - AC-4 covered by the standard gate — the e2e build tag is part of make check.
//
// THE CHUNKS ARE SENT IN REVERSE INDEX ORDER, index 1 then index 0, and that is
// the one non-obvious decision here. It costs nothing and makes two assertions
// non-vacuous that are otherwise unfalsifiable:
//
//   - sent in order, "the chunk that completed the transfer" and "the chunk with
//     the highest index" name the same frame, so AC-1's in_reply_to cannot tell a
//     correct implementation from one correlating to the last index arrived;
//   - arrival order and index order then differ, so AC-2's "in index order" kills
//     a receiver that appends rather than addressing by index.
//
// It is a conforming client, not an edge case: attachments.Intake's admission
// fork admits on FIRST TO ARRIVE and explicitly never on index == 0, and
// docs/protocol-mobile.md § Attachments publishes any-order arrival — deliberately
// weaker than debug_bundle_chunk's strict seq, which is the neighbouring rule a
// reader would wrongly copy.
//
// NO TURN IS ROUTED FIRST, and that absence is #2143's whole point. Until then
// attachments.Intake resolved the destination over the follow-active cursor, which
// only a successful send_message route stamps, so this run had to drive one send
// and wait for its ack before a single chunk could be stored. The chunks name their
// conversation now, so the upload stands alone — which is the operator flow that
// was impossible before: attaching to a conversation nobody has messaged yet.
//
// seedBoundConversation below STAYS, and it is a different precondition: it makes
// the conversation one the daemon HOSTS, which is what handleAttachmentChunk's
// KnownConversation gate requires. Membership, not routing.
func TestRelayV2_AttachmentUploadMultiChunk(t *testing.T) {
	const (
		initialUUID  = "11111111-1111-4111-8111-111111111111"
		knownConvID  = "22222222-2222-4222-8222-222222222222"
		attachmentID = "33333333-3333-4333-8333-333333333333"
		filename     = "pyry-e2e-1898.bin"
		mimeType     = "application/octet-stream"
		// The completing chunk is index 0, sent SECOND — so these two ids are what
		// makes AC-1's in_reply_to discriminating.
		chunk0EnvID = uint64(18980)
		chunk1EnvID = uint64(18981)
	)

	file, chunks, digest := attachmentFixture(t)

	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind the conversation to the bootstrap session so send_message routes rather
	// than answering server.binary_offline. The daemon loads conversations.json
	// once at startup, so the row must exist before it starts; the pair above is
	// what created the instance directory this writes into. boundSessionID must
	// equal the bootstrap pool id, which StartStreamInteractiveWithRelay pins to
	// initialUUID via seedBootstrapRegistry.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// This starter rather than StartInWithEnv for two properties it already
	// carries: it seeds the bootstrap registry at initialUUID, and it passes
	// -pyry-verbose, which puts the Debug-level chunk.accepted record on the
	// captured stderr the barrier below reads.
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server")
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

	// ── The transfer: the non-completing chunk, a barrier, then the completer ──
	declare := func(index int) protocol.AttachmentChunkPayload {
		return protocol.AttachmentChunkPayload{
			ConversationID: knownConvID,
			AttachmentID:   attachmentID,
			Index:          index,
			TotalChunks:    len(chunks),
			Filename:       filename,
			MimeType:       mimeType,
			Size:           int64(len(file)),
			SHA256:         digest,
			Data:           chunks[index],
		}
	}

	sendAttachmentChunk(t, phone, send, chunk1EnvID, declare(1))

	// THE BARRIER, and what turns AC-3's "answered with no frame at all" into an
	// assertion rather than a race. handleAttachmentChunk writes this record on the
	// accepted-but-incomplete arm and then RETURNS, so once it lands no reply for
	// that frame can still be in flight. Sending both chunks and checking afterwards
	// could not tell "answered nothing" from "answered late".
	waitForDaemonEvent(t, h, "event=v2.attachment.chunk.accepted", 10*time.Second)

	sendAttachmentChunk(t, phone, send, chunk0EnvID, declare(0))

	// ── AC-1: exactly the success reply, correlated to the COMPLETING chunk ──
	var stored protocol.Envelope
	storedDeadline := time.Now().Add(15 * time.Second)
	for stored.Type == "" {
		env, ok := nextAttachmentEnvelope(t, phone, recv, storedDeadline)
		if !ok {
			t.Fatal("no attachment_stored arrived for the completed transfer; the completing chunk " +
				"either never reached the intake or did not complete it")
		}
		switch env.Type {
		case protocol.TypeAttachmentStored:
			stored = env
		case protocol.TypeError:
			// The only errors reachable in this phase are attachment.* rejects, and
			// the code IS the diagnostic. Since #2143 invalid_chunk carries TWO
			// causes: the declaration arithmetic, or handleAttachmentChunk's
			// destination gate refusing the conversation the chunks named — check
			// seedBoundConversation first, since that is what makes knownConvID one
			// the daemon hosts. storage_failed is the host write itself (EnsureDir or
			// Store) and no longer says anything about the follow-active cursor, which
			// this path stopped reading. integrity_failed is the digest or the
			// assembled length.
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("upload refused, and its error payload did not decode: %v", err)
			}
			t.Fatalf("upload refused with code %q (retryable=%v, in_reply_to=%v); wanted %q",
				ep.Code, ep.Retryable, env.InReplyTo, protocol.TypeAttachmentStored)
		}
		// Nothing else should be on the wire: no turn is routed in this run, so the
		// turn_state / assistant_delta pushes that used to arrive here have no
		// producer. Classify after decrypt anyway — which keeps the receive nonce in
		// lockstep — and read on.
	}

	if stored.InReplyTo == nil || *stored.InReplyTo != chunk0EnvID {
		t.Errorf("attachment_stored in_reply_to = %v, want pointer to %d — the envelope of the chunk "+
			"that COMPLETED the transfer (index 0, sent second), not the highest index (%d)",
			stored.InReplyTo, chunk0EnvID, chunk1EnvID)
	}
	var storedPayload protocol.AttachmentStoredPayload
	if err := json.Unmarshal(stored.Payload, &storedPayload); err != nil {
		t.Fatalf("decode attachment_stored payload: %v", err)
	}
	if storedPayload.AttachmentID != attachmentID {
		t.Errorf("attachment_stored attachment_id = %q, want %q (the id this run chose)",
			storedPayload.AttachmentID, attachmentID)
	}

	// ── AC-2: the bytes are on the host, under the conversation the chunks NAMED ──
	//
	// The path is built from knownConvID — the conversation every chunk of this
	// transfer carried, and the only thing that could have put the bytes there,
	// since no turn was ever routed and the follow-active cursor is empty for the
	// whole run. Before #2143 that emptiness refused the completing chunk outright.
	dir := filepath.Join(home, ".pyry", "test", "conversations", knownConvID, "attachments", attachmentID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("attachment directory: %v — the reply said stored and the host has no such directory, "+
			"which is exactly the failure this run exists to catch", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		// attachments.Store's temp pattern is dot-prefixed and SanitizeFilename never
		// returns a leading '.', so a second entry means a leaked temporary file.
		t.Fatalf("attachment directory holds %d entries %v, want exactly 1", len(entries), names)
	}
	if got := entries[0].Name(); got != filename {
		t.Errorf("stored file name = %q, want %q (a plain-ASCII name is inside SanitizeFilename's "+
			"allowlist and passes through unchanged)", got, filename)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatalf("stat stored file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("stored file mode = %#o, want 0600 — these are the user's own private file bytes", perm)
	}
	got, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(got, file) {
		t.Errorf("stored bytes mismatch: got %d bytes, want %d — the assembled file must equal the "+
			"chunks concatenated in INDEX order, which is not the order they were sent",
			len(got), len(file))
	}

	// ── AC-3: exactly one attachment_stored, and no chunk drew a reject ──
	//
	// Wire side: a bounded quiet window after the success reply. A second stored
	// would be emitted by the same handler in the same instant, so this bounds
	// scheduling latency rather than a real delay.
	quietDeadline := time.Now().Add(1500 * time.Millisecond)
	for {
		env, ok := nextAttachmentEnvelope(t, phone, recv, quietDeadline)
		if !ok {
			break
		}
		switch env.Type {
		case protocol.TypeAttachmentStored:
			t.Errorf("a SECOND attachment_stored arrived (in_reply_to=%v); exactly one answers the "+
				"whole transfer", env.InReplyTo)
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			_ = json.Unmarshal(env.Payload, &ep)
			t.Errorf("an error frame arrived after the transfer completed: code %q (in_reply_to=%v)",
				ep.Code, env.InReplyTo)
		}
	}

	// Log side, different fabric: the daemon records every refusal it answers, so a
	// reject frame for the non-completing chunk would leave this record behind even
	// if the frame itself were lost. Non-vacuous by construction — the same buffer
	// already yielded chunk.accepted at the barrier, so it is provably live.
	logs := h.Stderr.String()
	if strings.Contains(logs, "event=v2.attachment.chunk.refused") {
		t.Error("the daemon refused a chunk of this transfer; the chunk that left the transfer " +
			"waiting for more must be answered with no frame at all")
	}

	assertNoAttachmentLeakInLogs(t, logs, attachmentID, filename, digest, file)
}

// attachmentFixture builds the file this run uploads and cuts it into chunks the
// published stride, returning the whole file, its chunks in index order, and the
// lowercase-hex sha256 of the whole file.
//
// THE SIZE IS STRICTLY GREATER THAN protocol.MaxAttachmentChunkBytes, which is the
// input condition the multi-chunk claim rests on. Equality would be a trap: the
// receiver requires total_chunks == max(1, ceil(size / bound)) as an EQUALITY, and
// at exactly the bound that is 1 — so the obvious "one chunk's worth" fixture
// proves nothing about a multi-chunk transfer.
//
// The count is COMPUTED from the constant with the receiver's own ceiling form
// rather than hardcoded to 2, and guarded below. A future move of the constant then
// reddens loudly instead of quietly degenerating this run to a single chunk.
//
// The fill is POSITION-DEPENDENT so any reorder or off-by-one in reassembly changes
// the concatenation; a constant fill would let AC-2 pass against a receiver that
// appended in arrival order. A distinctive ASCII sentinel is embedded inside chunk
// 0 for the never-log scan — see assertNoAttachmentLeakInLogs for why its offset
// and length are both multiples of three.
func attachmentFixture(t *testing.T) (file []byte, chunks [][]byte, digest string) {
	t.Helper()

	size := protocol.MaxAttachmentChunkBytes + 1234
	file = make([]byte, size)
	for i := range file {
		file[i] = byte(i*7 + 3)
	}
	copy(file[attachmentSentinelOffset:], attachmentByteSentinel)

	// The receiver's own arithmetic, in its own form: division-then-remainder, then
	// the published max(1, …). Never (size + bound - 1) / bound, which wraps.
	totalChunks := size / protocol.MaxAttachmentChunkBytes
	if size%protocol.MaxAttachmentChunkBytes != 0 {
		totalChunks++
	}
	totalChunks = max(totalChunks, 1)
	if totalChunks < 2 {
		t.Fatalf("fixture of %d bytes cuts into %d chunk(s) at a bound of %d; this run must be "+
			"MULTI-chunk", size, totalChunks, protocol.MaxAttachmentChunkBytes)
	}

	// Every chunk but the last carries exactly the bound; the last carries the
	// remainder. The receiver checks the COUNT and not the stride, but a client that
	// cuts elsewhere is refused whenever its count differs, so this run sends the
	// published stride.
	chunks = make([][]byte, totalChunks)
	for i := range chunks {
		lo := i * protocol.MaxAttachmentChunkBytes
		chunks[i] = file[lo:min(lo+protocol.MaxAttachmentChunkBytes, size)]
	}

	// Over the WHOLE file and over the same buffer the chunks are cut from, so the
	// declaration and the payload share provenance and a generator bug cannot make a
	// self-consistent green. hex.EncodeToString is already lowercase, the canonical
	// form § Attachments compares for exact equality.
	sum := sha256.Sum256(file)
	return file, chunks, hex.EncodeToString(sum[:])
}

const (
	// attachmentByteSentinel is a distinctive ASCII run embedded in the uploaded
	// bytes so a leak of the file content into the daemon log is greppable in a form
	// the base64 needle cannot spell. 23 bytes, inside the window scanned below.
	attachmentByteSentinel = "ZZ1898ATTACHMENTBYTESZZ"

	// attachmentSentinelOffset and attachmentSentinelWindow are both MULTIPLES OF
	// THREE, and that is load-bearing rather than tidy: base64 encodes independent
	// 3-byte groups, so the encoding of file[offset:offset+window] appears verbatim
	// inside the encoding of any enclosing slice only when the group boundary aligns.
	// Off a boundary the needle could never match and the scan would be vacuous.
	// Both chunk 0 and the assembled file start at offset 0, so one aligned window
	// covers each.
	attachmentSentinelOffset = 1002
	attachmentSentinelWindow = 48
)

// assertNoAttachmentLeakInLogs checks the never-log rule end to end against the
// REAL intake. protocol.AttachmentChunkPayload's SECURITY block is the declaring
// contract — "Log the attachment id, the index and the total; never the bytes, and
// never a raw filename" — docs/protocol-mobile.md § Attachments adds the declared
// digest, and handleAttachmentChunk's doc extends the ban to the host path, because
// attachments.EnsureDir's and attachments.Store's refusals wrap one.
//
// The unit tier checks this against a FAKE intake that builds no path and touches
// no filesystem; this is the only place the assembled path runs with the real one.
//
// THE POSITIVE PIN COMES FIRST and is what stops every negative below greening
// against an empty buffer. The attachment id is LOGGABLE by the declaring contract
// — attachment_stored echoes it to the wire — so its presence is both the honest
// expectation and the liveness control.
//
// THE BYTES NEED MORE THAN ONE NEEDLE. A []byte renders as base64 through
// encoding/json, as a bracketed decimal slice through slog's default handler, and
// as neither when passed as a string; a single-form needle greens against the other
// two. All three renderings of one aligned window are scanned. The residual is a
// leak in some fourth form none of them spells.
func assertNoAttachmentLeakInLogs(t *testing.T, logs, attachmentID, filename, digest string, file []byte) {
	t.Helper()

	if !strings.Contains(logs, attachmentID) {
		t.Fatalf("the daemon log names no attachment id, so the scan below would pass against an "+
			"empty buffer; expected records for %q", attachmentID)
	}

	window := file[attachmentSentinelOffset : attachmentSentinelOffset+attachmentSentinelWindow]
	for _, banned := range []struct{ what, needle string }{
		{"the client filename", filename},
		{"the declared digest", digest},
		{"the attachment bytes (raw)", attachmentByteSentinel},
		{"the attachment bytes (base64)", base64.StdEncoding.EncodeToString(window)},
		{"the attachment bytes (decimal slice)", strings.Trim(fmt.Sprint(window), "[]")},
	} {
		if strings.Contains(logs, banned.needle) {
			t.Errorf("the daemon log carries %s, which the never-log rule bans", banned.what)
		}
	}
}

// sendAttachmentChunk seals one attachment_chunk under envID and sends it.
func sendAttachmentChunk(t *testing.T, phone *fakephone.Client, send *noise.CipherState, envID uint64, chunk protocol.AttachmentChunkPayload) {
	t.Helper()
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      envID,
		Type:    protocol.TypeAttachmentChunk,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, chunk),
	})
	if err != nil {
		t.Fatalf("marshal attachment_chunk envelope (index %d): %v", chunk.Index, err)
	}
	ciphertext, err := send.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal attachment_chunk envelope (index %d): %v", chunk.Index, err)
	}
	sendNoiseMsg(t, phone, ciphertext)
}

// nextAttachmentEnvelope reads and decrypts the next daemon→phone application
// envelope, answering ok=false once deadline passes. It is the run's ONLY reader:
// each decrypt advances the receive CipherState exactly once, so every frame must
// be taken in arrival order and a second reader desyncs the nonce into a decrypt
// failure that reads like a daemon bug.
//
// It answers rather than Fatals on a timeout, unlike readInnerFrame, so each caller
// can name the milestone that failed instead of reporting a bare receive timeout on
// the most important precondition in the run.
func nextAttachmentEnvelope(t *testing.T, phone *fakephone.Client, recv *noise.CipherState, deadline time.Time) (protocol.Envelope, bool) {
	t.Helper()
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Envelope{}, false
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				return protocol.Envelope{}, false
			}
			t.Fatalf("phone receive: %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // a transport-level frame: no envelope, and it advances no nonce
		}
		return decryptInnerEnvelope(t, inner, recv), true
	}
}

// waitForDaemonEvent polls the captured daemon log until event appears.
//
// IT NEVER PRINTS THE BUFFER. This harness runs the daemon at slog.LevelDebug via
// -pyry-verbose, so its stderr is far chattier than an Info-level spec's, and
// interpolating the whole of it into a failure message would publish every record
// the daemon wrote — pairing material included — into CI output. The missing event
// name is the diagnostic.
func waitForDaemonEvent(t *testing.T, h *Harness, event string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if strings.Contains(h.Stderr.String(), event) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never logged %q within %s; the chunk was neither accepted nor "+
				"refused, so it did not reach handleAttachmentChunk", event, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
