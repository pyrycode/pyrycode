//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// TestRelayV2_AttachmentOfferedRoundTrip closes the loop this family was built
// for (#2166): a file appears on the host, a client is TOLD, and the client
// fetches it knowing nothing but what it was told.
//
// Every piece under it already exists and is covered on its own — the store
// (#2164), the MCP tool that drives it (#2168/#2169), the announcement's frame
// shape (#2082), and the whole retrieval leg (#2053/#2054, run end to end by
// TestRelayV2_AttachmentRetrieval). What has never run together is the join:
// until this ticket the minted id existed only in the control-socket reply that
// went back to claude, so a file claude produced reached a client as nothing at
// all.
//
// THE PROOF IS THAT THE ANNOUNCED ID IS THE ONLY THING THE FETCH USES. The
// request below names the id read off the attachment_offered frame, never the id
// control.AttachFile returned — those are the same string, and asserting it is
// one of the asks, but the FETCH must travel on the announced one or this run
// would prove a client can retrieve what it already knew. That is what makes
// this an end-to-end check for "no special-casing for an attachment the client
// did not upload": #2054 re-validates the id against the daemon's own registry
// regardless of what was announced, so the announced id travels exactly the path
// an uploader's own retrieval takes.
//
// THE FILE IS FILED THROUGH THE REAL VERB, not written onto disk the way
// TestRelayV2_AttachmentRetrieval writes its fixture. That is the one way this
// run is more expensive than its neighbour, and it is the point: the
// announcement is a property of the STORE, so a fixture written straight to disk
// would announce nothing. The test dials control.AttachFile — the same client
// `pyry mcp-files` calls — which is the shape
// TestRelayV2_StreamModalPermissionRoundTrip uses for control.Approve, and for
// that test's reason: driving the MCP child would add a routed turn and a second
// mechanism to this run's failure modes without testing anything this one does
// not.
//
// WHY THE IDS MUST LINE UP. fileAttacher derives its destination from the
// CALLING session: it refuses an id that is not live, refuses a live session
// bound to no conversation, and confines the path to that conversation's
// recorded workspace. seedBootstrapRegistry (called inside
// StartStreamInteractiveWithRelay) pins the bootstrap session to initialUUID,
// and seedBoundConversation writes a row binding that id with cwd set to home —
// so a file written under home is already inside the conversation's recorded
// workspace and the verb has every precondition it needs. No routed turn is
// required: the store reads its destination off the session and the fetch reads
// its conversation off the frame.
//
// THE HOST NAME CARRIES A SPACE, deliberately. attachments.Store sanitises the
// claude-authored name before writing it, so the stored leaf differs from the
// host leaf — which turns the "announced name and retrieved name are one string"
// assertion from a tautology into a check that both legs derive it from the
// stored path. A run using a name sanitisation leaves alone would pass with the
// two legs deriving the name two different ways.
func TestRelayV2_AttachmentOfferedRoundTrip(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		knownConvID = "44444444-4444-4444-8444-444444444444"
		// The name on the host, and the name the sanitiser rewrites: spaces are
		// outside SanitizeFilename's allowlist and become underscores.
		hostName   = "pyry 2166 offered.bin"
		storedName = "pyry_2166_offered.bin"
		fetchReqID = uint64(21660)
		// Generous by design — a hang-catcher, not a timing assumption. The
		// daemon reads a multi-chunk file off disk on each leg.
		replyDeadline = 15 * time.Second
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

	// The conversation must be in the registry the daemon loads at startup: the
	// store resolves its destination through it, and the fetch's KnownConversation
	// gate refuses an id it does not hold.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// Inside the conversation's recorded workspace, which is what confineFile
	// admits. A path outside it is refused with a static reason and nothing is
	// stored — so this placement is a precondition of the run, not decoration.
	hostPath := filepath.Join(home, hostName)
	if err := os.WriteFile(hostPath, file, 0o600); err != nil {
		t.Fatalf("write the host file: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

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

	// ── The store: the phone is attached and interactive BEFORE the file is
	// filed. The offer is live-only — there is no outstanding-offer registry and
	// no connect-time replay — so a phone that handshakes afterwards is never
	// told, and this ordering is load-bearing rather than incidental.
	attachCtx, attachCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer attachCancel()
	res, err := control.AttachFile(attachCtx, h.SocketPath, control.AttachFilePayload{
		SessionID: initialUUID,
		Path:      hostPath,
	})
	if err != nil {
		t.Fatalf("control.AttachFile: %v — the verb refused a file inside the conversation's "+
			"recorded workspace, filed by its own live session", err)
	}
	if res.AttachmentID == "" {
		t.Fatal("attachment.file answered with an empty id")
	}

	// ── The announcement ──
	var offer protocol.AttachmentOfferedPayload
	deadline := time.Now().Add(replyDeadline)
	for offer.AttachmentID == "" {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatal("no attachment_offered frame arrived after a successful store; a file that " +
				"lands must be announced, or a client can never learn the id")
		}
		if env.Type != protocol.TypeAttachmentOffered {
			continue // the bootstrap session's own pushes; classify after decrypt and read on
		}
		if err := json.Unmarshal(env.Payload, &offer); err != nil {
			t.Fatalf("decode attachment_offered payload: %v", err)
		}
		// An offer carries no in_reply_to: nothing solicits it. Asserting the
		// absence here is what stops a future producer from quietly correlating
		// it to the request that happened to trigger the store.
		if env.InReplyTo != nil {
			t.Errorf("attachment_offered carried in_reply_to = %v; nothing solicits this frame",
				*env.InReplyTo)
		}
	}

	if offer.ConversationID != knownConvID {
		t.Errorf("announced conversation_id = %q, want %q — the conversation the CALLING session "+
			"is bound to", offer.ConversationID, knownConvID)
	}
	if offer.AttachmentID != res.AttachmentID {
		t.Errorf("announced attachment_id = %q, but the verb minted %q; the announcement must name "+
			"the file that was actually stored", offer.AttachmentID, res.AttachmentID)
	}
	if offer.Filename != storedName {
		t.Errorf("announced filename = %q, want %q — the name the file was STORED under, which is "+
			"the sanitised form of the host name %q", offer.Filename, storedName, hostName)
	}

	// ── The fetch, on the announced id alone ──
	sendRequestAttachment(t, phone, send, fetchReqID, protocol.RequestAttachmentPayload{
		ConversationID: offer.ConversationID,
		AttachmentID:   offer.AttachmentID,
	})

	var stream []protocol.Envelope
	deadline = time.Now().Add(replyDeadline)
	for len(stream) < len(chunks) {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatalf("only %d of %d attachment_chunk frames arrived for the ANNOUNCED attachment; "+
				"a client that received an offer must be able to act on it", len(stream), len(chunks))
		}
		switch env.Type {
		case protocol.TypeAttachmentChunk:
			stream = append(stream, env)
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("the fetch was refused, and its error payload did not decode: %v", err)
			}
			t.Fatalf("the fetch of an ANNOUNCED attachment was refused with code %q (retryable=%v); "+
				"receiving an offer is not a capability and the id re-validates like any other",
				ep.Code, ep.Retryable)
		}
	}

	got, err := relay.ReassembleAttachment(stream, offer.AttachmentID, fetchReqID)
	if err != nil {
		t.Fatalf("reassemble the answered stream: %v", err)
	}
	if !bytes.Equal(got, file) {
		t.Errorf("reassembled %d bytes, want the host file's %d — a client acting on an offer must "+
			"recover the file exactly", len(got), len(file))
	}

	// AC-1's pin, and the reason this assertion lives at the END of a round trip
	// rather than inside a unit test: the two names are produced by two
	// independent legs — the announcer takes it from what Store wrote, the
	// retrieval stream from what ResolvePath found — and the claim is that they
	// are ONE string, not two derivations that happen to agree today.
	var chunk protocol.AttachmentChunkPayload
	if err := json.Unmarshal(stream[0].Payload, &chunk); err != nil {
		t.Fatalf("decode attachment_chunk payload: %v", err)
	}
	if chunk.Filename != offer.Filename {
		t.Errorf("the retrieval leg publishes filename %q while the announcement said %q; both name "+
			"the leaf of the stored path and must be the same string", chunk.Filename, offer.Filename)
	}

	// The never-log rule over the whole exchange, host name included: the
	// announcement introduces a second place a filename could leak, and the
	// § Attachments ban is not lifted by sanitising, so neither spelling may
	// appear.
	assertNoAttachmentLeakInLogs(t, h.Stderr.String(), offer.AttachmentID, storedName, digest, file)
	assertNoAttachmentLeakInLogs(t, h.Stderr.String(), offer.AttachmentID, hostName, digest, file)
}
