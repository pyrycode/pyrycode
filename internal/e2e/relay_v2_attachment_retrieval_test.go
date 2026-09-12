//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// TestRelayV2_AttachmentRetrieval is the first end-to-end run of the retrieval
// leg (#2054) — the answer half of the transfer whose upload half
// TestRelayV2_AttachmentUploadMultiChunk covers. Every piece under it is
// unit-tested inside its own package: the chunker and the stream (#2053), the
// path resolver (#2037), and the dispatch arm that joins them; nothing until now
// has run the whole chain in one process, against a real daemon reading a real
// file off a real disk.
//
// TWO ASKS, ONE RUN, and they are the two halves of the ticket's last acceptance
// criterion:
//
//   - a request for a STORED attachment is answered with the file, and a client
//     reassembling the chunks recovers it byte for byte;
//   - a request for one that is NOT THERE is answered with attachment.not_found,
//     correlated to that request, serving no bytes and creating nothing on disk.
//
// THE FILE IS WRITTEN DIRECTLY ONTO THE HOST rather than uploaded first, and the
// choice is deliberate. The upload leg is #1898's subject and is already covered
// end to end; driving it here would make this run depend on it, and would cost a
// send_message round trip first — the upload's completing chunk resolves its
// conversation off the follow-active cursor, which is empty until a turn routes.
// Retrieval reads the conversation off the FRAME and validates it against the
// registry, so it needs no routed turn, and skipping one is what keeps this run
// short. What is written is exactly what the upload leg would have left: mode
// 0600, under the conversation's own attachments directory, named by the
// attachment id.
//
// THE FIXTURE IS SHARED WITH THE UPLOAD RUN (attachmentFixture) and is
// multi-chunk by construction. At or below the per-chunk bound the stream is one
// frame and "an ordered chunk stream a client reassembles" is unfalsifiable.
// relay.ReassembleAttachment is the receiver-contract oracle #2053 exported for
// exactly this assertion, so the claim is checked against the published contract
// rather than against a concatenation this test invented.
func TestRelayV2_AttachmentRetrieval(t *testing.T) {
	const (
		initialUUID  = "11111111-1111-4111-8111-111111111111"
		knownConvID  = "44444444-4444-4444-8444-444444444444"
		attachmentID = "55555555-5555-4555-8555-555555555555"
		// Canonically shaped and never stored: the reject arm must be reached by
		// the attachment half of the pair failing to resolve, not by a shape
		// check on the id.
		missingID     = "66666666-6666-4666-8666-666666666666"
		filename      = "pyry-e2e-2054.bin"
		storedReqID   = uint64(20540)
		missingReqID  = uint64(20541)
		replyDeadline = 15 * time.Second
	)

	file, chunks, digest := attachmentFixture(t)

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

	// The conversation must be in the registry the daemon loads at startup, or
	// KnownConversation refuses the request before any lookup — which is the
	// membership gate doing its job, and would make both asks below return the
	// same code for the wrong reason.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	storedDir := filepath.Join(home, ".pyry", "test", "conversations", knownConvID, "attachments", attachmentID)
	if err := os.MkdirAll(storedDir, 0o700); err != nil {
		t.Fatalf("create attachment directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storedDir, filename), file, 0o600); err != nil {
		t.Fatalf("write stored attachment: %v", err)
	}

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
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

	// ── Ask 1: the stored attachment ──
	sendRequestAttachment(t, phone, send, storedReqID, protocol.RequestAttachmentPayload{
		ConversationID: knownConvID,
		AttachmentID:   attachmentID,
	})

	var stream []protocol.Envelope
	deadline := time.Now().Add(replyDeadline)
	for len(stream) < len(chunks) {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatalf("only %d of %d attachment_chunk frames arrived for the stored attachment; the "+
				"daemon either refused the request or never streamed it", len(stream), len(chunks))
		}
		switch env.Type {
		case protocol.TypeAttachmentChunk:
			stream = append(stream, env)
		case protocol.TypeError:
			// The only errors reachable here are this verb's two, and the code IS
			// the diagnostic: not_found points at the membership gate or the
			// resolver, stream_aborted at the push path.
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("retrieval refused, and its error payload did not decode: %v", err)
			}
			t.Fatalf("retrieval of a STORED attachment refused with code %q (retryable=%v, in_reply_to=%v)",
				ep.Code, ep.Retryable, env.InReplyTo)
		}
		// Anything else is the bootstrap session's own push. Classify after
		// decrypt — which keeps the receive nonce in lockstep — and read on.
	}

	got, err := relay.ReassembleAttachment(stream, attachmentID, storedReqID)
	if err != nil {
		t.Fatalf("reassemble the answered stream: %v", err)
	}
	if !bytes.Equal(got, file) {
		t.Errorf("reassembled %d bytes, want the stored file's %d — a client reassembling the chunks "+
			"must recover the file exactly", len(got), len(file))
	}

	// ── Ask 2: an attachment that is not there ──
	sendRequestAttachment(t, phone, send, missingReqID, protocol.RequestAttachmentPayload{
		ConversationID: knownConvID,
		AttachmentID:   missingID,
	})

	var refusal protocol.Envelope
	deadline = time.Now().Add(replyDeadline)
	for refusal.Type == "" {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatal("no error frame arrived for the unknown attachment; a request that yields no bytes " +
				"must be answered, not dropped")
		}
		switch env.Type {
		case protocol.TypeError:
			refusal = env
		case protocol.TypeAttachmentChunk:
			t.Fatalf("an attachment frame answered the request for an attachment that is not there "+
				"(in_reply_to=%v)", env.InReplyTo)
		}
	}

	if refusal.InReplyTo == nil || *refusal.InReplyTo != missingReqID {
		t.Errorf("reject in_reply_to = %v, want pointer to %d — the request that asked, not the one "+
			"that was served", refusal.InReplyTo, missingReqID)
	}
	var ep protocol.ErrorPayload
	if err := json.Unmarshal(refusal.Payload, &ep); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if ep.Code != protocol.CodeAttachmentNotFound {
		t.Errorf("reject code = %q, want %q", ep.Code, protocol.CodeAttachmentNotFound)
	}
	if ep.Retryable {
		t.Error("reject retryable = true; attachment.not_found is published as not retryable")
	}
	// The static-message claim, checked against what a leak would actually
	// contain rather than against a fixed string: the requested id and the host
	// path are the two values a message derived from an error would carry.
	for what, needle := range map[string]string{
		"the requested attachment id": missingID,
		"the conversation id":         knownConvID,
		"a host path":                 home,
	} {
		if strings.Contains(ep.Message, needle) {
			t.Errorf("the reject message carries %s; § Attachments requires a static message that "+
				"echoes neither the requested id nor the resolved path", what)
		}
	}

	// Creating nothing on disk is a property of the reject path, not a courtesy:
	// a lookup that MkdirAll'd what it failed to find would leave an empty
	// directory behind for every traversal probe it refuses.
	missingDir := filepath.Join(home, ".pyry", "test", "conversations", knownConvID, "attachments", missingID)
	if _, err := os.Stat(missingDir); !os.IsNotExist(err) {
		t.Errorf("the refused request left %q behind (stat err = %v); a refusal must create nothing",
			filepath.Base(missingDir), err)
	}

	assertNoAttachmentLeakInLogs(t, h.Stderr.String(), attachmentID, filename, digest, file)
}

// sendRequestAttachment seals one request_attachment under envID and sends it.
// The envelope id is load-bearing: every answer this verb can give — the chunks
// and both rejects alike — correlates on it through in_reply_to, and the payload
// carries no request-id key.
func sendRequestAttachment(t *testing.T, phone *fakephone.Client, send *noise.CipherState, envID uint64, req protocol.RequestAttachmentPayload) {
	t.Helper()
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      envID,
		Type:    protocol.TypeRequestAttachment,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, req),
	})
	if err != nil {
		t.Fatalf("marshal request_attachment envelope %d: %v", envID, err)
	}
	ciphertext, err := send.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal request_attachment envelope %d: %v", envID, err)
	}
	sendNoiseMsg(t, phone, ciphertext)
}
