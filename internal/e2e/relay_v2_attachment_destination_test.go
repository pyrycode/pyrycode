//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file drives the three attachment-destination flows #2144 names.
//
// ONE PROPERTY, stated once for all three tests below: the bytes land where the client
// said. #2142 published conversation_id on attachment_chunk and #2143 made
// handleAttachmentChunk file the upload under the conversation the chunks name,
// validated against the registry, instead of under the daemon-global follow-active
// cursor. Both ship unit-level proof at the intake and relay layers. What neither
// covers is the operator-visible shape: a chunk over a real Noise session and bytes
// on a real disk under a particular conversation.
//
// THE HELPERS ARE MOSTLY INHERITED. sendAttachmentChunk, nextAttachmentEnvelope and
// waitForDaemonEvent come from relay_v2_attachment_upload_test.go (#1898);
// dialHelloPhone, createConversationViaPhone and drainForReply from
// per_conversation_eviction_test.go (#2085). Neither file's code is edited from here —
// the one change made there is a doc-block pointer explaining why the resolve half
// could not ride #1898's transfer, and the four helpers below are the whole of what
// this file adds.
//
// A SMALLER FIXTURE THAN #1898's, deliberately. attachmentFixture cuts a multi-chunk
// transfer because reassembly ORDER is its subject; nothing here asserts anything
// about reassembly, so singleChunkAttachment below cuts one chunk and every run
// costs one frame.
//
// TWO CONVERSATIONS COME OFF THE WIRE, not from a second seeder. seedBoundConversation
// writes a single-row conversations.json and overwrites, so it cannot seed A and B —
// and it does not need to (the never-messaged run below, which needs exactly one row,
// uses it). handleAttachmentChunk's destination gate consults
// KnownConversation, which cmd/pyry/relay.go builds over a live w.convReg.Get rather
// than over the file loaded at startup, so a conversation minted after the daemon is
// running passes the gate exactly as a seeded one does. That is also why
// seedBoundConversation's "the row must exist before the daemon starts" constraint
// does not reach these tests.

// TestRelayV2_AttachmentUploadOnNeverMessagedConversationResolves closes the loop on
// the flow #2143 made possible: an attachment added to a conversation nobody has
// messaged yet.
//
// The UPLOAD half of it is already asserted by TestRelayV2_AttachmentUploadMultiChunk,
// which since #2143 routes no turn before its transfer. THE NEW OBSERVABLE IS THE ACK.
// handlers.SendMessage validates the binding through SessionRouter.Route and only
// THEN calls resolveAttachments, which refuses the whole message with
// attachment.not_found if any named id fails attachments.ResolvePath beneath the
// conversation just validated. So an ack is reachable only when the resolver found
// this attachment under the conversation the chunks named — which a hand-built path
// assertion cannot show, because that addresses the directory itself while the
// resolver is what a real client depends on.
//
// IT REBUILDS THE PRECONDITION RATHER THAN RIDING #1898's TRANSFER, and the reason is
// mechanical rather than stylistic: that run ends by proving an absence, which means
// running fakephone's receive deadline out, and ReceiveBytes closes the conn on
// timeout. Nothing can be sent afterwards. A single-chunk transfer reproduces the
// precondition in a few lines and leaves the conn alive for the send this run is about.
//
// TWO PROPERTIES OF THE CONVERSATION, and only one of them is "never messaged".
// seedBoundConversation makes it one the daemon HOSTS, which is what the
// KnownConversation gate requires, and BOUND to the bootstrap pool id, which is what
// makes Route succeed so the ack can be about the resolver rather than about routing.
// What it deliberately does not do is route a turn: the follow-active cursor is empty
// for the whole run, which before #2143 refused this upload outright.
func TestRelayV2_AttachmentUploadOnNeverMessagedConversationResolves(t *testing.T) {
	const (
		initialUUID  = "21452145-2145-4145-8145-214521452145"
		knownConvID  = "21452146-2145-4145-8145-214521452146"
		attachmentID = "21452147-2145-4145-8145-214521452147"
		filename     = "pyry-e2e-2144-never.bin"
		mimeType     = "application/octet-stream"
		chunkEnvID   = uint64(21450)
		sendEnvID    = uint64(21451)
	)

	home := shortHome(t)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// The daemon loads conversations.json once at startup, so the row must exist
	// before it starts; the setup above is what created the instance directory this
	// writes into, and initialUUID is the bootstrap pool id
	// StartStreamInteractiveWithRelay pins via seedBootstrapRegistry.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h.Stop(t) })

	phone, send, recv := dialHelloPhone(t, home, fr, pubKey, payload.Token)

	file, digest := singleChunkAttachment(t)
	sendAttachmentChunk(t, phone, send, chunkEnvID, protocol.AttachmentChunkPayload{
		ConversationID: knownConvID,
		AttachmentID:   attachmentID,
		Index:          0,
		TotalChunks:    1,
		Filename:       filename,
		MimeType:       mimeType,
		Size:           int64(len(file)),
		SHA256:         digest,
		Data:           file,
	})

	stored := awaitOutcome(t, phone, recv, 20*time.Second,
		protocol.TypeAttachmentStored, protocol.TypeError)
	if stored.Type != protocol.TypeAttachmentStored {
		var ep protocol.ErrorPayload
		if err := json.Unmarshal(stored.Payload, &ep); err != nil {
			t.Fatalf("the upload was refused, and its error payload did not decode: %v", err)
		}
		t.Fatalf("the upload into the never-messaged conversation was refused with code %q; wanted %q. "+
			"Membership is the gate here, and no turn is routed in this run, so a refusal means the "+
			"destination is being resolved over something other than the id the chunk named",
			ep.Code, protocol.TypeAttachmentStored)
	}

	// ── The ack, and it is the whole assertion ──
	resolveEnv, err := json.Marshal(protocol.Envelope{
		ID:   sendEnvID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "m-2144-resolve",
			Text:           "e2e-2144-resolve\n",
			AttachmentIDs:  []string{attachmentID},
		}),
	})
	if err != nil {
		t.Fatalf("marshal the resolving send_message envelope: %v", err)
	}
	ciphertext, err := send.Encrypt(resolveEnv)
	if err != nil {
		t.Fatalf("seal the resolving send_message envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)

	ack := awaitOutcome(t, phone, recv, 20*time.Second, protocol.TypeAck, protocol.TypeError)
	if ack.Type == protocol.TypeError {
		var ep protocol.ErrorPayload
		if err := json.Unmarshal(ack.Payload, &ep); err != nil {
			t.Fatalf("the send_message was refused, and its error payload did not decode: %v", err)
		}
		t.Fatalf("the send_message naming the stored attachment was refused with code %q; wanted an ack. "+
			"%q means attachments.ResolvePath found nothing beneath the conversation the chunks named, "+
			"so bytes that are on disk are not reachable by the flow a real client uses",
			ep.Code, protocol.CodeAttachmentNotFound)
	}
	if ack.InReplyTo == nil || *ack.InReplyTo != sendEnvID {
		t.Errorf("ack in_reply_to = %v, want pointer to %d — the envelope of the send_message that "+
			"named the attachment", ack.InReplyTo, sendEnvID)
	}
}

// TestRelayV2_AttachmentUploadNamesConversationNotCursor is the A-B-A misfile: the
// flow that cost a real misfile before #2143, where a client sent in A, sent in B
// and returned to A, and its bytes followed the cursor to B.
//
// Two conversations are minted over the wire and one turn is routed into each, in
// order, so the follow-active cursor ends on B. An upload whose single chunk names
// A then completes, and the bytes must be under A.
//
// THE ACK IS THE WHOLE PRECONDITION AND THE TURN DRAIN IS NOT NEEDED.
// handlers.SendMessage calls SessionRouter.Route — the cursor's only writer — and
// only then enqueues and acks, so an observed ack already proves the cursor moved.
// Draining each turn to its terminal turn_state would buy nothing this run asserts
// and would spend two ~20s deadlines on it.
//
// THE SWEEP IS OVER THE WHOLE conversations/ TREE AND ITS CLAIM IS "EXACTLY ONE",
// which is the one non-obvious decision here. The acceptance criterion asks that no
// attachment file exist under B, and a sweep of B's own subtree would answer that
// — but it answers empty just as readily against a mistyped path, a broken walker,
// or a B directory that legitimately never exists, so the negative would carry no
// liveness control of its own. Sweeping the whole tree and asserting the single hit
// is A's makes the positive the walker's own proof, gives "nothing under B" a
// fortiori, and additionally catches a misfile under any third conversation.
func TestRelayV2_AttachmentUploadNamesConversationNotCursor(t *testing.T) {
	const (
		initialUUID  = "21442144-2144-4144-8144-214421442144"
		attachmentID = "21442145-2144-4144-8144-214421442145"
		filename     = "pyry-e2e-2144-aba.bin"
		mimeType     = "application/octet-stream"
		sendAEnvID   = uint64(21440)
		sendBEnvID   = uint64(21441)
		chunkEnvID   = uint64(21442)
	)

	home := shortHome(t)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h.Stop(t) })

	phone, send, recv := dialHelloPhone(t, home, fr, pubKey, payload.Token)

	convA := createConversationViaPhone(t, phone, send, recv, 2)
	convB := createConversationViaPhone(t, phone, send, recv, 3)
	if convA == convB {
		t.Fatalf("both create_conversation replies named %s; A and B must be distinct for the "+
			"cursor to be able to point at the wrong one", convA)
	}

	// A then B, strictly ordered, so the cursor ends on B — the destination this
	// run must NOT see the bytes reach.
	routeTurn(t, phone, send, recv, sendAEnvID, convA, "m-2144-a", "e2e-2144-route:a\n")
	routeTurn(t, phone, send, recv, sendBEnvID, convB, "m-2144-b", "e2e-2144-route:b\n")

	file, digest := singleChunkAttachment(t)
	sendAttachmentChunk(t, phone, send, chunkEnvID, protocol.AttachmentChunkPayload{
		ConversationID: convA,
		AttachmentID:   attachmentID,
		Index:          0,
		TotalChunks:    1,
		Filename:       filename,
		MimeType:       mimeType,
		Size:           int64(len(file)),
		SHA256:         digest,
		Data:           file,
	})

	stored := awaitOutcome(t, phone, recv, 20*time.Second,
		protocol.TypeAttachmentStored, protocol.TypeError)
	if stored.Type != protocol.TypeAttachmentStored {
		var ep protocol.ErrorPayload
		if err := json.Unmarshal(stored.Payload, &ep); err != nil {
			t.Fatalf("the upload was refused, and its error payload did not decode: %v", err)
		}
		t.Fatalf("the upload naming conversation A was refused with code %q (retryable=%v); wanted %q. "+
			"A wire-minted conversation must pass handleAttachmentChunk's KnownConversation gate, "+
			"which reads the live registry rather than the file loaded at startup",
			ep.Code, ep.Retryable, protocol.TypeAttachmentStored)
	}
	if stored.InReplyTo == nil || *stored.InReplyTo != chunkEnvID {
		t.Errorf("attachment_stored in_reply_to = %v, want pointer to %d — the envelope of the single "+
			"chunk that completed the transfer", stored.InReplyTo, chunkEnvID)
	}

	// ── The destination, both halves, from one sweep ──
	convRoot := filepath.Join(home, ".pyry", "test", "conversations")
	found, _ := attachmentFilesUnder(t, convRoot)
	wantRel := filepath.Join(convA, "attachments", attachmentID, filename)
	if len(found) != 1 || found[0] != wantRel {
		t.Fatalf("attachment files beneath conversations/ = %v, want exactly [%s]. The chunks named "+
			"conversation A while the follow-active cursor pointed at B (%s), so a hit under B is the "+
			"pre-#2143 misfile and a second hit anywhere is a stray copy", found, wantRel, convB)
	}

	got, err := os.ReadFile(filepath.Join(convRoot, wantRel))
	if err != nil {
		t.Fatalf("read the stored file: %v", err)
	}
	if !bytes.Equal(got, file) {
		t.Errorf("stored bytes mismatch: got %d bytes, want %d — the file under conversation A must be "+
			"the bytes this run sent", len(got), len(file))
	}
}

// TestRelayV2_AttachmentUploadUnknownConversationRefused drives a chunk naming a
// conversation the daemon does not host and asserts the refusal writes nothing.
//
// ONE CHUNK IS THE WHOLE TRANSFER, and that is a property of the gate rather than a
// shortcut. handleAttachmentChunk validates the destination per FRAME, ahead of the
// AttachmentIntake seam, so a transfer naming an unusable conversation dies on its
// first chunk before a byte is admitted — which is what makes the disk sweep below
// both cheap and the honest thing to assert.
//
// THE WIRE CODE ALONE IS A WEAK ASSERTION, so three observations are pinned
// together. attachment.invalid_chunk answers five distinct causes: a payload decode
// failure, an absent conversation_id, this arm, the declaration arithmetic, and
// attachments.ErrUnknownUpload. A test pinning only the code stays green when the
// refusal actually came from this run's own declaration being malformed. The
// arm-specific witness is the daemon-authored reason rejectAttachmentChunk records
// — a constant chosen at the call site and never derived from the frame, identical
// on the wire across arms and separable only here. Pinning its PRESENCE is also
// what makes the sibling arm's absence non-vacuous: the same buffer provably holds
// this refusal's record.
//
// THE SWEEP RUNS AFTER THE ERROR FRAME, and the ordering is the guard. The refusal
// decision and the frame leave the same handler in that order, so a sweep placed
// before the frame could read the tree ahead of a write the daemon had not yet
// declined to make.
func TestRelayV2_AttachmentUploadUnknownConversationRefused(t *testing.T) {
	const (
		initialUUID   = "21432143-2143-4143-8143-214321432143"
		unknownConvID = "21432144-2143-4143-8143-214321432144"
		attachmentID  = "21432145-2143-4143-8143-214321432145"
		filename      = "pyry-e2e-2144-unknown.bin"
		mimeType      = "application/octet-stream"
		chunkEnvID    = uint64(21443)

		// The two destination arms' daemon-authored reasons, spelled as slog's
		// TextHandler renders them so a substring match cannot be satisfied by the
		// words appearing in some other record.
		reasonUnknown = `reason="conversation is not one this daemon hosts"`
		reasonAbsent  = `reason="conversation id is absent or empty"`
	)

	home := shortHome(t)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h.Stop(t) })

	phone, send, recv := dialHelloPhone(t, home, fr, pubKey, payload.Token)

	// NO conversation is seeded and none is minted, so unknownConvID is one the
	// daemon does not host. Its shape is deliberately VALID — 36 characters that
	// satisfy conversations.ValidID — so the refusal below is membership failing,
	// not shape: KnownConversation is a registry lookup and the gate this run
	// exercises is the registry's, not the id validator's.
	file, digest := singleChunkAttachment(t)
	sendAttachmentChunk(t, phone, send, chunkEnvID, protocol.AttachmentChunkPayload{
		ConversationID: unknownConvID,
		AttachmentID:   attachmentID,
		Index:          0,
		TotalChunks:    1,
		Filename:       filename,
		MimeType:       mimeType,
		Size:           int64(len(file)),
		SHA256:         digest,
		Data:           file,
	})

	// ── Observation 1: the wire ──
	refused := awaitOutcome(t, phone, recv, 20*time.Second,
		protocol.TypeAttachmentStored, protocol.TypeError)
	if refused.Type == protocol.TypeAttachmentStored {
		t.Fatal("the daemon answered attachment_stored for a conversation it does not host; the " +
			"destination gate admitted the chunk instead of refusing it")
	}
	var ep protocol.ErrorPayload
	if err := json.Unmarshal(refused.Payload, &ep); err != nil {
		t.Fatalf("the chunk was refused, and its error payload did not decode: %v", err)
	}
	if ep.Code != protocol.CodeAttachmentInvalidChunk {
		t.Errorf("refusal code = %q, want %q", ep.Code, protocol.CodeAttachmentInvalidChunk)
	}
	if refused.InReplyTo == nil || *refused.InReplyTo != chunkEnvID {
		t.Errorf("error in_reply_to = %v, want pointer to %d — the envelope of the chunk that "+
			"named the unknown conversation", refused.InReplyTo, chunkEnvID)
	}

	// ── Observation 2: the daemon's own refusal record, and which arm wrote it ──
	waitForDaemonEvent(t, h, "event=v2.attachment.chunk.refused", 10*time.Second)
	logs := h.Stderr.String()
	if !strings.Contains(logs, reasonUnknown) {
		t.Fatalf("the daemon recorded a chunk refusal without the unknown-conversation reason; the "+
			"refusal came from some other cause sharing %q, and every check below would be reading a "+
			"buffer this run has not pinned", protocol.CodeAttachmentInvalidChunk)
	}
	if strings.Contains(logs, reasonAbsent) {
		t.Error("the daemon recorded the ABSENT-conversation-id reason; this run sends a non-empty " +
			"conversation id, so the two gate arms are not separable in the log the way " +
			"rejectAttachmentChunk's contract states")
	}
	// The conversation id is logged on NO arm of this gate — it has not passed it —
	// and this tier is the only place that claim runs against the real handler.
	// Non-vacuous by construction: the positive pin above already proved this
	// buffer holds the refusal's own record.
	if strings.Contains(logs, unknownConvID) {
		t.Error("the daemon log names the refused conversation id; handleAttachmentChunk logs it on " +
			"no arm of the destination gate, because a client-supplied id that failed the gate is " +
			"not yet safe to log")
	}

	// ── Observation 3: nothing was written, anywhere under the instance ──
	instanceDir := filepath.Join(home, ".pyry", "test")
	found, walked := attachmentFilesUnder(t, instanceDir)
	if walked == 0 {
		t.Fatalf("the sweep of the instance directory visited no files at all, so its emptiness says "+
			"nothing about attachments; %s should at least hold the session and device registries",
			filepath.Join(".pyry", "test"))
	}
	if len(found) != 0 {
		t.Errorf("attachment files beneath the instance directory = %v, want none — the chunk was "+
			"refused before the intake seam, so no byte was ever admitted", found)
	}
}

// routeTurn sends one send_message on convID under envID and returns once its ack
// has arrived. It is the cursor-stamping precondition in its cheapest honest form:
// handlers.SendMessage validates the binding through SessionRouter.Route, the only
// writer of the follow-active cursor, and only then enqueues and acks — so the ack
// is already proof the cursor moved, and the turn's own delivery is not waited on.
//
// Deliberately not activateViaTurn, which drains to a terminal turn_state because
// its callers assert something about the turn. Nothing here does.
func routeTurn(t *testing.T, phone *fakephone.Client, send, recv *noise.CipherState,
	envID uint64, convID, messageID, text string) {
	t.Helper()
	raw, err := json.Marshal(protocol.Envelope{
		ID:   envID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      messageID,
			Text:           text,
		}),
	})
	if err != nil {
		t.Fatalf("marshal send_message envelope (conv=%s): %v", convID, err)
	}
	ct, err := send.Encrypt(raw)
	if err != nil {
		t.Fatalf("seal send_message envelope (conv=%s): %v", convID, err)
	}
	sendNoiseMsg(t, phone, ct)
	drainForReply(t, phone, recv, protocol.TypeAck, envID, 15*time.Second)
}

// awaitOutcome reads until an envelope of one of the wanted types arrives and
// returns it for the caller to judge. Every other type is skipped and read on:
// these tests route turns or share a conn with the structured stream, so
// assistant_delta and turn_state pushes are expected traffic rather than a surprise.
//
// EVERY CALLER PASSES protocol.TypeError ALONGSIDE THE TYPE IT WANTS, which is what
// keeps a refusal from being read as silence and timed out on. It returns the frame
// rather than asserting on it because its callers want opposite verdicts — one run
// here demands the error the others forbid — and reporting the observed types on
// timeout keeps "nothing arrived" distinguishable from "the wrong thing arrived".
//
// IT MUST NOT BE CALLED TO PROVE AN ABSENCE. Running it to its deadline runs
// fakephone's receive deadline out, and ReceiveBytes closes the conn on timeout, so
// the conn is dead to every later send — the constraint that keeps #1898's quiet
// window the last thing that run does.
func awaitOutcome(t *testing.T, phone *fakephone.Client, recv *noise.CipherState,
	timeout time.Duration, want ...string) protocol.Envelope {
	t.Helper()
	var seen []string
	deadline := time.Now().Add(timeout)
	for {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatalf("no %v arrived within %s; envelope types observed: %v", want, timeout, seen)
		}
		seen = append(seen, env.Type)
		for _, w := range want {
			if env.Type == w {
				return env
			}
		}
	}
}

// singleChunkAttachment builds a file that cuts into exactly one chunk, returning
// the bytes and the lowercase-hex sha256 of them.
//
// THE SIZE IS DERIVED FROM protocol.MaxAttachmentChunkBytes rather than written as
// a literal, so it stays below the bound structurally if the bound ever moves. The
// count is then recomputed with the RECEIVER's own division-then-remainder form and
// asserted to be 1 — never (size + bound - 1) / bound, which wraps — because the
// receiver checks total_chunks as an EQUALITY, so a fixture that quietly cut into
// two would be refused invalid_chunk and both callers would blame the destination
// gate for a declaration fault.
//
// The fill is POSITION-DEPENDENT and deterministic, not random: it makes a
// reordering or off-by-one visible in the byte comparison, and it keeps a failure
// reproducible from the test alone. The digest is taken over the same buffer the
// payload carries, so the declaration and the bytes share provenance and a
// generator bug cannot produce a self-consistent green.
func singleChunkAttachment(t *testing.T) (file []byte, digest string) {
	t.Helper()

	size := protocol.MaxAttachmentChunkBytes / 4
	file = make([]byte, size)
	for i := range file {
		file[i] = byte(i*11 + 5)
	}

	totalChunks := size / protocol.MaxAttachmentChunkBytes
	if size%protocol.MaxAttachmentChunkBytes != 0 {
		totalChunks++
	}
	totalChunks = max(totalChunks, 1)
	if totalChunks != 1 {
		t.Fatalf("fixture of %d bytes cuts into %d chunks at a bound of %d; both callers declare a "+
			"SINGLE-chunk transfer and that declaration would be refused",
			size, totalChunks, protocol.MaxAttachmentChunkBytes)
	}

	sum := sha256.Sum256(file)
	return file, hex.EncodeToString(sum[:])
}

// attachmentFilesUnder walks root and answers every regular file sitting beneath an
// "attachments" path component, plus the total number of regular files the walk
// visited. Paths are relative to root, which keeps a failure message legible
// without printing a host prefix into CI output.
//
// filesWalked IS THE VACUITY GUARD for a sweep with no positive hit. An empty
// answer means "no attachment file" only once the walk is known to have visited
// something; a mistyped or absent root gives the same empty slice, so a caller with
// nothing legitimate to find asserts filesWalked > 0 instead.
//
// filepath.WalkDir does NOT follow symlinks, and that is the behaviour this wants:
// a link pointing out of the instance tree is reported as the entry it is rather
// than traversed, so a sweep for "what did the daemon write" cannot be turned into
// a reader of arbitrary host paths. Dot-prefixed entries are skipped because
// attachments.Store's temp pattern is dot-prefixed while SanitizeFilename never
// returns a leading dot, so a leaked temporary is not a stored attachment.
//
// A root that does not exist answers empty rather than failing: a conversation that
// was never written to has no directory, and that is an answer rather than an error.
func attachmentFilesUnder(t *testing.T, root string) (attachmentFiles []string, filesWalked int) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		filesWalked++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		for _, segment := range strings.Split(rel, string(filepath.Separator)) {
			if segment == "attachments" {
				attachmentFiles = append(attachmentFiles, rel)
				break
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("sweep for attachment files: %v", err)
	}
	sort.Strings(attachmentFiles)
	return attachmentFiles, filesWalked
}
