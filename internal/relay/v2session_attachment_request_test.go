package relay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #2054 inbound attachment-retrieval fixtures ---

// Sentinel values chosen so a substring scan of the log buffer cannot match them
// incidentally. The split is the one handleAttachmentChunk's fixtures already
// draw: the ids are what the declaring types mark loggable (after shape
// validation), and the filename, the digest, the content bytes and the host path
// are what docs/protocol-mobile.md § Attachments bans from every wire message and
// every log line.
const (
	raTestConvID     = "5b8e2f14-9c33-4a71-b0d6-2054cafe0001" // canonical UUIDv4
	raTestAttachID   = "6c9f3a25-0d44-4b82-a1e7-2054cafe0002" // canonical UUIDv4
	raTestFilename   = "ZZ2054FILENAMEZZ.bin"                 // NEVER logged, never replied
	raTestReqEnvID   = uint64(20540)
	raTestOtherEnvID = uint64(20541)
)

// resolveCall captures one AttachmentResolve call. Both arguments are retained
// rather than one, because the claim the membership-gate test makes is about
// which ids reach the resolver AT ALL.
type resolveCall struct {
	conversationID string
	attachmentID   string
}

// fakeAttachmentResolver is a relay-side test double for the AttachmentResolve
// seam. It records every call and answers a scripted (path, ok), which is what
// makes each no-bytes cause addressable one row at a time — driving real
// attachments.ResolvePath would be re-testing #2037 and would couple these rows
// to an on-disk instance directory layout.
type fakeAttachmentResolver struct {
	mu    sync.Mutex
	path  string
	ok    bool
	calls []resolveCall
}

func (f *fakeAttachmentResolver) resolve(conversationID, attachmentID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, resolveCall{conversationID: conversationID, attachmentID: attachmentID})
	return f.path, f.ok
}

func (f *fakeAttachmentResolver) callSnapshot() []resolveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]resolveCall(nil), f.calls...)
}

// startRetrievalConn stands up a manager with both retrieval seams wired (either
// may be nil for the unwired postures), drives one paired handshake and returns
// the open session plus its log buffer. Every test here drives a real handshake
// and a real AEAD-sealed frame, so the interception is proven through
// dispatchAppFrame and the conn's appFrameWorker rather than by calling the
// handler directly.
func startRetrievalConn(t *testing.T, knownConv func(string) bool, resolve func(string, string) (string, bool)) (*openSession, *syncLogBuffer) {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	logger, logBuf := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            logger,
		KnownConversation: knownConv,
		AttachmentResolve: resolve,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)
	return sess, logBuf
}

// knownOnly answers membership for exactly one conversation id, which is what
// makes "a conversation the registry does not know" a real contrast rather than
// a seam that says no to everything.
func knownOnly(id string) func(string) bool {
	return func(got string) bool { return got == id }
}

// sendRequestAttachment seals one request_attachment envelope under the
// initiator's send state and hands it to the manager. The envelope ID IS
// load-bearing: every answer — the chunks and both rejects alike — correlates on
// it through in_reply_to, and the payload carries no request-id key.
func sendRequestAttachment(t *testing.T, sess *openSession, envID uint64, payload string) {
	t.Helper()
	sess.frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      envID,
		Type:    protocol.TypeRequestAttachment,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
}

// requestPayload builds a well-formed request_attachment body naming both ids.
func requestPayload(conversationID, attachmentID string) string {
	p, err := json.Marshal(protocol.RequestAttachmentPayload{
		ConversationID: conversationID,
		AttachmentID:   attachmentID,
	})
	if err != nil {
		panic(err) // closed struct of two strings; cannot fail
	}
	return string(p)
}

// waitForReplies waits for n sealed frames on the conn, then holds a settling
// window open and fails if an n+1th arrives. That upper bound is the half that
// matters here: every reject claim in this file is "exactly one error frame AND
// no attachment frame", and a lower-bound wait alone cannot tell a correct
// refusal from one that also served bytes.
func waitForReplies(t *testing.T, sess *openSession, n int) []protocol.Envelope {
	t.Helper()
	msgs := waitForConnNoiseMsg(t, sess.rec, v2TestConnID, n)
	time.Sleep(150 * time.Millisecond)
	msgs = noiseMsgsForConn(t, sess.rec, v2TestConnID)
	if len(msgs) != n {
		t.Fatalf("sealed frame count = %d, want exactly %d", len(msgs), n)
	}
	// Decrypted in arrival order: the receive CipherState's nonce is in lockstep
	// with the sender's, so a frame skipped here desynchronises every later one.
	out := make([]protocol.Envelope, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, decryptAppFrame(t, m, sess.initRecv))
	}
	return out
}

// expectNoReply holds a settling window open and fails if anything is sealed on
// the conn. The inert posture's whole claim.
func expectNoReply(t *testing.T, sess *openSession) {
	t.Helper()
	time.Sleep(250 * time.Millisecond)
	if msgs := noiseMsgsForConn(t, sess.rec, v2TestConnID); len(msgs) != 0 {
		t.Fatalf("sealed frame count = %d, want 0 — the frame must be consumed and answered with nothing", len(msgs))
	}
}

// assertRejectFrame pins one refusal whole: type, code, static message,
// retryability and correlation. Checking the message string rather than only the
// code is what stops a future edit from deriving it from an error value.
func assertRejectFrame(t *testing.T, env protocol.Envelope, wantCode, wantMsg string, wantRetryable bool, wantInReplyTo uint64) {
	t.Helper()
	p := decodeErrorPayload(t, env)
	if p.Code != wantCode {
		t.Errorf("reject code = %q, want %q", p.Code, wantCode)
	}
	if p.Message != wantMsg {
		t.Errorf("reject message = %q, want the static %q", p.Message, wantMsg)
	}
	if p.Retryable != wantRetryable {
		t.Errorf("reject retryable = %v, want %v (the value docs/protocol-mobile.md § Error codes publishes for %s)",
			p.Retryable, wantRetryable, wantCode)
	}
	if env.InReplyTo == nil || *env.InReplyTo != wantInReplyTo {
		t.Errorf("reject in_reply_to = %v, want pointer to %d", env.InReplyTo, wantInReplyTo)
	}
}

// TestV2Session_RequestAttachment_StreamsTheStoredFile is AC #1: a request naming
// a known conversation and a stored attachment is answered with that file's bytes
// as an ordered chunk stream correlated to the request, and a client reassembling
// them recovers the file exactly.
//
// THE FIXTURE IS DELIBERATELY MULTI-CHUNK. At or below the per-chunk bound the
// stream is one frame, and "ordered" and "reassembled in index order" are then
// unfalsifiable — the same trap attachmentFixture's doc names on the upload side.
// ReassembleAttachment is the oracle #2053 exported for exactly this assertion,
// so the claim is checked against the published receiver contract rather than
// against a bespoke concatenation this test invented.
func TestV2Session_RequestAttachment_StreamsTheStoredFile(t *testing.T) {
	t.Parallel()

	blob := patternBlob(2*protocol.MaxAttachmentChunkBytes + 7) // 3 chunks
	res := &fakeAttachmentResolver{path: writeAttachmentFile(t, raTestFilename, blob), ok: true}
	sess, _ := startRetrievalConn(t, knownOnly(raTestConvID), res.resolve)

	sendRequestAttachment(t, sess, raTestReqEnvID, requestPayload(raTestConvID, raTestAttachID))

	envs := waitForReplies(t, sess, 3)
	got, err := ReassembleAttachment(envs, raTestAttachID, raTestReqEnvID)
	if err != nil {
		t.Fatalf("reassemble the answered stream: %v", err)
	}
	if len(got) != len(blob) || string(got) != string(blob) {
		t.Errorf("reassembled %d bytes, want the stored file's %d — byte for byte", len(got), len(blob))
	}

	calls := res.callSnapshot()
	if len(calls) != 1 {
		t.Fatalf("resolver calls = %d, want exactly 1", len(calls))
	}
	if calls[0].conversationID != raTestConvID || calls[0].attachmentID != raTestAttachID {
		t.Errorf("resolver called with (%q, %q), want (%q, %q) — the ids the frame named, unaltered",
			calls[0].conversationID, calls[0].attachmentID, raTestConvID, raTestAttachID)
	}
}

// TestV2Session_RequestAttachment_EveryNoBytesCauseAnswersOneCode is AC #3, and
// the one code IS the security property: an unknown conversation and an unknown
// attachment must be indistinguishable on the wire, or the verb becomes a
// path-existence oracle for a traversal probe. So every row asserts the WHOLE
// refusal — code, static message and retryable — not just the code, and asserts
// that exactly one frame came back so no row can be passing while also serving
// bytes.
func TestV2Session_RequestAttachment_EveryNoBytesCauseAnswersOneCode(t *testing.T) {
	t.Parallel()

	// A path whose file does not exist: the "resolves but cannot then be read"
	// cause, which is a PRE-EMISSION failure inside StreamAttachment and therefore
	// takes this code rather than the abort one.
	missing := filepath.Join(t.TempDir(), raTestFilename)

	tests := []struct {
		name     string
		known    func(string) bool
		resolved string
		ok       bool
		payload  string
	}{
		{
			// A type mismatch rather than garbled bytes, so the failure is the
			// decode itself and not a truncated frame the transport would have
			// rejected first.
			name:    "payload does not decode",
			known:   knownOnly(raTestConvID),
			ok:      true,
			payload: `{"conversation_id":42,"attachment_id":"` + raTestAttachID + `"}`,
		},
		{
			name:    "absent conversation_id",
			known:   knownOnly(raTestConvID),
			ok:      true,
			payload: `{"attachment_id":"` + raTestAttachID + `"}`,
		},
		{
			name:    "empty conversation_id",
			known:   knownOnly(raTestConvID),
			ok:      true,
			payload: requestPayload("", raTestAttachID),
		},
		{
			name:    "unknown conversation",
			known:   knownOnly(raTestConvID),
			ok:      true,
			payload: requestPayload("7d0a4b36-1e55-4c93-b2f8-2054cafe0003", raTestAttachID),
		},
		{
			// The empty component hazard RequestAttachmentPayload's doc names:
			// filepath.Join(dir, "", "") is dir, so an unchecked empty id addresses
			// the conversation directory root rather than erroring.
			name:    "absent attachment_id",
			known:   knownOnly(raTestConvID),
			ok:      true,
			payload: `{"conversation_id":"` + raTestConvID + `"}`,
		},
		{
			name:    "empty attachment_id",
			known:   knownOnly(raTestConvID),
			ok:      true,
			payload: requestPayload(raTestConvID, ""),
		},
		{
			// One sentinel behind this bool covers the unknown id, the
			// non-canonical id and the id resolving outside the conversation's
			// directory alike, so the handler cannot branch on what it must not
			// distinguish.
			name:    "resolver answers no path",
			known:   knownOnly(raTestConvID),
			ok:      false,
			payload: requestPayload(raTestConvID, raTestAttachID),
		},
		{
			name:     "resolves but the file cannot be read",
			known:    knownOnly(raTestConvID),
			resolved: missing,
			ok:       true,
			payload:  requestPayload(raTestConvID, raTestAttachID),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := &fakeAttachmentResolver{path: tt.resolved, ok: tt.ok}
			sess, _ := startRetrievalConn(t, tt.known, res.resolve)

			sendRequestAttachment(t, sess, raTestReqEnvID, tt.payload)

			envs := waitForReplies(t, sess, 1)
			if envs[0].Type == protocol.TypeAttachmentChunk {
				t.Fatal("a request that yields no bytes was answered with an attachment frame")
			}
			assertRejectFrame(t, envs[0], protocol.CodeAttachmentNotFound, msgAttachmentNotFound, false, raTestReqEnvID)
		})
	}
}

// TestV2Session_RequestAttachment_MembershipGateFiresBeforeTheResolver is AC #2's
// second half, and it is the one assertion a reordering kills. The reject code is
// identical either way, so the wire cannot tell the two orders apart — the
// resolver's CALL COUNT is what distinguishes "validated the conversation first"
// from "resolved a path built out of the client's ids and then refused".
//
// attachments.ResolvePath's doc block states the precondition this discharges:
// the conversation must already have been validated against the registry, and "a
// caller that gets it wrong defeats every check below". This handler is that
// caller, so the check being present is not enough — it must be FIRST.
func TestV2Session_RequestAttachment_MembershipGateFiresBeforeTheResolver(t *testing.T) {
	t.Parallel()

	res := &fakeAttachmentResolver{path: writeAttachmentFile(t, raTestFilename, patternBlob(64)), ok: true}
	sess, _ := startRetrievalConn(t, knownOnly(raTestConvID), res.resolve)

	// A canonically-shaped id the registry does not know — so nothing but the
	// membership gate can be what refuses it.
	sendRequestAttachment(t, sess, raTestReqEnvID, requestPayload("8e1b5c47-2f66-4d04-93a9-2054cafe0004", raTestAttachID))

	envs := waitForReplies(t, sess, 1)
	assertRejectFrame(t, envs[0], protocol.CodeAttachmentNotFound, msgAttachmentNotFound, false, raTestReqEnvID)

	if calls := res.callSnapshot(); len(calls) != 0 {
		t.Errorf("resolver was called %d time(s) with %v; a request naming a conversation the registry "+
			"does not know must perform NO lookup built from the client's ids", len(calls), calls)
	}
}

// TestV2Session_RequestAttachment_NilResolverIsInertButConsumed pins the unwired
// posture: no reply of any kind, and — the half that is easy to lose — no
// protocol.unsupported either, which is what proves the frame was CONSUMED by the
// interception rather than falling through to dispatch.Route. Mirrors the nil
// AttachmentIntake behaviour, and buys the same property: an unwired daemon
// performs zero parsing of remote-authored bytes.
func TestV2Session_RequestAttachment_NilResolverIsInertButConsumed(t *testing.T) {
	t.Parallel()

	sess, logBuf := startRetrievalConn(t, knownOnly(raTestConvID), nil)

	sendRequestAttachment(t, sess, raTestReqEnvID, requestPayload(raTestConvID, raTestAttachID))

	expectNoReply(t, sess)
	if logs := logBuf.String(); !strings.Contains(logs, "v2.attachment.request.inert") {
		t.Errorf("no inert record in the log; a silently dropped frame and a deliberately inert one "+
			"are indistinguishable without it.\nlogs:\n%s", logs)
	}
}

// TestV2Session_RequestAttachment_NilKnownConversationRefuses is the fail-safe
// half of the gate: a daemon with a resolver wired and no membership seam must
// refuse every request rather than serve it. Nil means "no conversation is
// addressable", never "every conversation is" — the same reading its sole
// existing reader, handleRequestSnapshot, takes.
func TestV2Session_RequestAttachment_NilKnownConversationRefuses(t *testing.T) {
	t.Parallel()

	res := &fakeAttachmentResolver{path: writeAttachmentFile(t, raTestFilename, patternBlob(64)), ok: true}
	sess, _ := startRetrievalConn(t, nil, res.resolve)

	sendRequestAttachment(t, sess, raTestReqEnvID, requestPayload(raTestConvID, raTestAttachID))

	envs := waitForReplies(t, sess, 1)
	assertRejectFrame(t, envs[0], protocol.CodeAttachmentNotFound, msgAttachmentNotFound, false, raTestReqEnvID)
	if calls := res.callSnapshot(); len(calls) != 0 {
		t.Errorf("resolver was called %d time(s) with a nil membership seam", len(calls))
	}
}

// TestV2Session_RequestAttachment_PushFailureIsStreamAborted is AC #4's first
// half: a retrieval that fails once emission has begun is terminated with the
// published abort code, never with a further attachment frame.
//
// THE FIXTURE DEVIATES FROM PRODUCTION ON PURPOSE, and the deviation is what
// isolates the branch. Push fails only when the conn's push queue is gone, and in
// production the queue disappears WITH the session, so the abort frame is usually
// undeliverable and the branch is best-effort. Removing the queue while the
// session stays open reproduces the Push failure deterministically and leaves the
// reply path (forwardToRun → Run's seal) intact, so the frame this branch owes
// can actually be observed. That is why the proof lives here and not in the e2e
// run.
//
// The failure lands at index 0, which is answered stream_aborted where not_found
// would also have been defensible — the correct trade, since stream_aborted is
// the retryable code and a torn-down conn is a retryable condition.
func TestV2Session_RequestAttachment_PushFailureIsStreamAborted(t *testing.T) {
	t.Parallel()

	res := &fakeAttachmentResolver{path: writeAttachmentFile(t, raTestFilename, patternBlob(64)), ok: true}
	sess, _ := startRetrievalConn(t, knownOnly(raTestConvID), res.resolve)

	sess.mgr.pushMu.Lock()
	delete(sess.mgr.queues, v2TestConnID)
	sess.mgr.pushMu.Unlock()

	sendRequestAttachment(t, sess, raTestReqEnvID, requestPayload(raTestConvID, raTestAttachID))

	envs := waitForReplies(t, sess, 1)
	if envs[0].Type == protocol.TypeAttachmentChunk {
		t.Fatal("an abandoned stream was answered with a further attachment frame; the abort is an " +
			"error envelope and never a second attachment frame")
	}
	assertRejectFrame(t, envs[0], protocol.CodeAttachmentStreamAborted, msgAttachmentStreamAborted, true, raTestReqEnvID)
}

// TestV2Session_RequestAttachment_CarriesNoBannedStrings is the never-log rule as
// an assertion. The loggable set is conn id, attachment id and counts; the
// filename, the digest, the content bytes and the host path are banned from every
// log line and every wire message alike.
//
// THE CONTENT BYTES ARE CHECKED IN TWO RENDERINGS, which is #1898's finding: a
// []byte reaches a log through encoding/json as base64 and through slog's default
// handler as a bracketed decimal slice, and never as raw ASCII — so a
// single-form needle greens against a leak in the other form. The POSITIVE PIN on
// the attachment id is what stops all of the negatives from greening against an
// empty buffer instead of a genuinely clean one.
func TestV2Session_RequestAttachment_CarriesNoBannedStrings(t *testing.T) {
	t.Parallel()

	// A distinctive run of bytes, and a host path whose DIRECTORY components are
	// the daemon's own layout — the part no frame and no log line may disclose.
	blob := []byte("ZZ2054CONTENTBYTESZZ")
	dir := t.TempDir()
	path := filepath.Join(dir, raTestFilename)
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("write attachment fixture: %v", err)
	}
	res := &fakeAttachmentResolver{path: path, ok: true}
	sess, logBuf := startRetrievalConn(t, knownOnly(raTestConvID), res.resolve)

	sendRequestAttachment(t, sess, raTestReqEnvID, requestPayload(raTestConvID, raTestAttachID))
	envs := waitForReplies(t, sess, 1)
	if _, err := ReassembleAttachment(envs, raTestAttachID, raTestReqEnvID); err != nil {
		t.Fatalf("the run must have actually streamed the file, or every scan below is vacuous: %v", err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, raTestAttachID) {
		t.Fatalf("the loggable attachment id is absent from the captured log, so the bans below prove "+
			"nothing about a live buffer.\nlogs:\n%s", logs)
	}
	banned := map[string]string{
		"filename":              raTestFilename,
		"host path":             path,
		"content bytes (ascii)": string(blob),
		"content bytes (slog)":  decimalSlice(blob),
	}
	for what, needle := range banned {
		if strings.Contains(logs, needle) {
			t.Errorf("the captured log carries the %s, which § Attachments bans", what)
		}
	}
}

// decimalSlice renders b the way slog's TextHandler renders a []byte attribute —
// the second of the two forms #1898 found a single needle cannot cover. Written
// out rather than inferred from a handler so the needle stays readable.
func decimalSlice(b []byte) string {
	parts := make([]string, 0, len(b))
	for _, c := range b {
		parts = append(parts, fmt.Sprintf("%d", c))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// TestV2Session_RequestAttachment_SecondRequestIsAnsweredSeparately pins that two
// retrievals over one conn separate cleanly: each answer names its own request's
// envelope id, which is the whole of the correlation contract now that the
// payload carries no request-id key. A handler correlating to a remembered "last
// request" passes every other test in this file and fails this one.
func TestV2Session_RequestAttachment_SecondRequestIsAnsweredSeparately(t *testing.T) {
	t.Parallel()

	blob := patternBlob(128)
	res := &fakeAttachmentResolver{path: writeAttachmentFile(t, raTestFilename, blob), ok: true}
	sess, _ := startRetrievalConn(t, knownOnly(raTestConvID), res.resolve)

	sendRequestAttachment(t, sess, raTestReqEnvID, requestPayload(raTestConvID, raTestAttachID))
	sendRequestAttachment(t, sess, raTestOtherEnvID, requestPayload(raTestConvID, raTestAttachID))

	envs := waitForReplies(t, sess, 2)
	for _, want := range []uint64{raTestReqEnvID, raTestOtherEnvID} {
		got, err := ReassembleAttachment(envs, raTestAttachID, want)
		if err != nil {
			t.Fatalf("reassemble the answer to request %d: %v", want, err)
		}
		if string(got) != string(blob) {
			t.Errorf("request %d recovered %d bytes, want %d", want, len(got), len(blob))
		}
	}
}
