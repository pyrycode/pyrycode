package relay

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #1897 inbound attachment-upload interception fixtures ---

// Sentinel values chosen so a substring scan of the log buffer cannot match them
// incidentally. The split matters: the first three are the strings
// protocol.AttachmentChunkPayload's SECURITY block and docs/protocol-mobile.md
// § Attachments BAN from every wire message and every log line, and the id is the
// one string the declaring type marks safe to log (attachment_stored echoes it to
// the wire, so the stricter four-string ban some internal/attachments doc blocks
// assert is wrong about it).
const (
	atTestAttachmentID = "0f1a2b3c-4d5e-4f60-8a9b-1897cafe0001" // loggable; canonical UUIDv4
	// The conversation the fake daemon hosts, and one it does not. Both are
	// canonical UUIDv4s so the shape check downstream cannot be what separates
	// them — only registry membership can, which is the seam #2143 gates on. The
	// foreign one carries a scan sentinel because it must appear in NO log line:
	// unlike the attachment id, a conversation id is never shape-validated here.
	atTestConvID        = "0f1a2b3c-4d5e-4f60-8a9b-2143cafe0001"
	atTestForeignConvID = "0f1a2b3c-4d5e-4f60-8a9b-2143beef0002"
	atTestFilename      = "ZZ1897FILENAMEZZ.bin"              // NEVER logged, never replied
	atTestDigest        = "ZZ1897DIGESTZZ"                    // NEVER logged, never replied
	atTestData          = "ZZ1897CHUNKBYTESZZ"                // NEVER logged, never replied
	atTestHostPath      = "/var/ZZ1897HOSTPATHZZ/attachments" // NEVER logged, never replied
	atTestConnB         = "conn-1897-b"
	atTestChunkEnvID    = 8971
)

// attachmentCall captures one AttachmentIntake.Receive call. The whole decoded
// payload is retained rather than selected fields, so a test can prove the frame
// crossed intact instead of proving only that something crossed.
type attachmentCall struct {
	connID string
	// conversationID is what the HANDLER passed, recorded separately from the
	// chunk it came off so a row can prove the gate handed the seam the frame's
	// own destination rather than some other value it had to hand.
	conversationID string
	chunk          protocol.AttachmentChunkPayload
}

// fakeAttachmentIntake is a relay-side test double for AttachmentIntake. It
// records every call and returns a scripted (id, stored, err), which is what makes
// the three-way answer and the twelve-sentinel map addressable one row at a time —
// driving real bytes through attachments.Intake would be re-testing #1896.
//
// block, when non-nil, parks Receive until it is closed: the knob for proving the
// upload runs OFF the Run goroutine.
type fakeAttachmentIntake struct {
	mu       sync.Mutex
	storedID string
	stored   bool
	err      error
	calls    []attachmentCall
	released []string

	block <-chan struct{}
}

func (f *fakeAttachmentIntake) Receive(connID, conversationID string, chunk protocol.AttachmentChunkPayload) (string, bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, attachmentCall{connID: connID, conversationID: conversationID, chunk: chunk})
	block, storedID, stored, err := f.block, f.storedID, f.stored, f.err
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return storedID, stored, err
}

func (f *fakeAttachmentIntake) ReleaseConn(connID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, connID)
}

func (f *fakeAttachmentIntake) callSnapshot() []attachmentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]attachmentCall(nil), f.calls...)
}

func (f *fakeAttachmentIntake) releasedSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.released...)
}

// callCount is the synchronisation knob for the arms that reach the seam without
// emitting an outbound envelope — chiefly the accepted-but-incomplete one, whose
// whole point is that nothing is replied.
func (f *fakeAttachmentIntake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// startAttachmentConn stands up a manager with intake wired (nil for the unwired
// posture), drives one paired handshake, and returns the manager, the frames
// channel, the initiator's send/recv CipherStates, the recorder, the log buffer
// and the stop func. Every test here drives a real handshake and a real
// AEAD-sealed frame, so the interception is proven through dispatchAppFrame and
// the conn's appFrameWorker rather than by calling the handler directly.
func startAttachmentConn(t *testing.T, intake AttachmentIntake) (*V2SessionManager, chan protocol.RoutingEnvelope, *noise.CipherState, *noise.CipherState, *v2Recorder, *syncLogBuffer, func()) {
	t.Helper()
	return startAttachmentConnKnowing(t, intake, func(id string) bool { return id == atTestConvID })
}

// startAttachmentConnKnowing is startAttachmentConn with the membership seam under
// the test's control, which is what the #2143 gate rows need: production wires a
// conversations.Registry lookup here, and the two postures worth driving are "this
// one conversation is hosted" and "the seam is not wired at all".
func startAttachmentConnKnowing(t *testing.T, intake AttachmentIntake, known func(string) bool) (*V2SessionManager, chan protocol.RoutingEnvelope, *noise.CipherState, *noise.CipherState, *v2Recorder, *syncLogBuffer, func()) {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	logger, logBuf := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            logger,
		AttachmentIntake:  intake,
		KnownConversation: known,
	})
	t.Cleanup(stop)
	send, recv := openModalConn(t, mgr, frames, rec, respPub, v2TestConnID, []string{protocol.CapabilityInteractive})
	return mgr, frames, send, recv, rec, logBuf, stop
}

// chunkPayload builds a well-formed attachment_chunk body carrying all four
// never-log strings, so every log scan in this file runs against a frame that
// actually contained them.
func chunkPayload(index, total int) string {
	return chunkPayloadIn(atTestConvID, index, total)
}

// chunkPayloadIn is chunkPayload with the destination under the caller's control,
// for the rows that drive a conversation the daemon does not host.
func chunkPayloadIn(conversationID string, index, total int) string {
	p, err := json.Marshal(protocol.AttachmentChunkPayload{
		ConversationID: conversationID,
		AttachmentID:   atTestAttachmentID,
		Index:          index,
		TotalChunks:    total,
		Filename:       atTestFilename,
		MimeType:       "application/octet-stream",
		Size:           int64(len(atTestData)),
		SHA256:         atTestDigest,
		Data:           []byte(atTestData),
	})
	if err != nil {
		panic(err) // closed struct of strings/ints; cannot fail
	}
	return string(p)
}

// sendChunkFrame seals one attachment_chunk envelope under the initiator's send
// state and hands it to the manager's Frames channel. The envelope ID IS
// load-bearing here, unlike the question fixtures': attachment_stored correlates
// on it via in_reply_to.
func sendChunkFrame(t *testing.T, frames chan protocol.RoutingEnvelope, send *noise.CipherState, envID uint64, payload string) {
	t.Helper()
	frames <- sealAppFrameConn(t, send, v2TestConnID, protocol.Envelope{
		ID:      envID,
		Type:    protocol.TypeAttachmentChunk,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
}

// soleReply waits for exactly one noise_msg on the conn, decrypts it and returns
// the inner envelope. Fails if a second one shows up in the settling window, which
// is what pins "answered ONCE" rather than "answered at least once".
func soleReply(t *testing.T, rec *v2Recorder, recv *noise.CipherState) protocol.Envelope {
	t.Helper()
	msgs := waitForConnNoiseMsg(t, rec, v2TestConnID, 1)
	if len(msgs) != 1 {
		t.Fatalf("noise_msg count = %d, want exactly 1", len(msgs))
	}
	return decryptAppFrame(t, msgs[0], recv)
}

// decodeErrorPayload reads a TypeError reply's body.
func decodeErrorPayload(t *testing.T, env protocol.Envelope) protocol.ErrorPayload {
	t.Helper()
	if env.Type != protocol.TypeError {
		t.Fatalf("reply type = %q, want %q", env.Type, protocol.TypeError)
	}
	var p protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	return p
}

// TestV2Session_AttachmentChunk_AcceptedChunk_AnswersNothing is AC #2's negative
// half and the single most important row in this file: a chunk the intake accepts
// while the transfer waits for more is answered with NOTHING. attachments.Intake
// reports that as ("", false, nil) — it interprets ErrIncomplete itself — so a
// handler reading err != nil the usual way would answer one reject frame per chunk
// of a perfectly healthy upload. The frame still crosses the seam whole, with this
// conn's id.
func TestV2Session_AttachmentChunk_AcceptedChunk_AnswersNothing(t *testing.T) {
	t.Parallel()

	intake := &fakeAttachmentIntake{} // ("", false, nil)
	mgr, frames, send, _, rec, logBuf, stop := startAttachmentConn(t, intake)

	sendChunkFrame(t, frames, send, atTestChunkEnvID, chunkPayload(3, 9))
	waitForResolverCall(t, intake.callCount, 1, "Receive")
	// The handler has returned by the time this record lands, so no reply for
	// this frame can still be in flight.
	waitForLogContains(t, logBuf, "event=v2.attachment.chunk.accepted")

	if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
		t.Errorf("accepted-but-incomplete chunk drew %d noise_msg, want 0", len(msgs))
	}

	calls := intake.callSnapshot()
	if len(calls) != 1 {
		t.Fatalf("Receive calls = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.connID != v2TestConnID {
		t.Errorf("Receive connID = %q, want %q", got.connID, v2TestConnID)
	}
	if got.chunk.AttachmentID != atTestAttachmentID || got.chunk.Index != 3 || got.chunk.TotalChunks != 9 {
		t.Errorf("chunk ids = (%q, %d, %d), want (%q, 3, 9)",
			got.chunk.AttachmentID, got.chunk.Index, got.chunk.TotalChunks, atTestAttachmentID)
	}
	// The payload crossed whole: the declaration and the bytes, not just the ids.
	if got.chunk.Filename != atTestFilename || got.chunk.SHA256 != atTestDigest || string(got.chunk.Data) != atTestData {
		t.Errorf("chunk body did not cross intact: filename=%q sha256=%q len(data)=%d",
			got.chunk.Filename, got.chunk.SHA256, len(got.chunk.Data))
	}
	stop() // Run has exited; mgr.sessions is safe to read from this goroutine.
	s := mgr.sessions[v2TestConnID]
	if s == nil || s.State() != V2StateOpen {
		t.Error("session not left open after an accepted attachment_chunk")
	}
}

// TestV2Session_AttachmentChunk_CompletingChunk_AnswersStored is AC #2's positive
// half: the chunk whose arrival completes a transfer draws exactly one
// attachment_stored naming the attachment, correlated by in_reply_to to THAT
// chunk's envelope — which, since chunks may arrive in any order, is not one the
// client could have predicted. The id replied is the one the seam returned.
func TestV2Session_AttachmentChunk_CompletingChunk_AnswersStored(t *testing.T) {
	t.Parallel()

	intake := &fakeAttachmentIntake{storedID: atTestAttachmentID, stored: true}
	_, frames, send, recv, rec, _, _ := startAttachmentConn(t, intake)

	sendChunkFrame(t, frames, send, atTestChunkEnvID, chunkPayload(8, 9))
	reply := soleReply(t, rec, recv)

	if reply.Type != protocol.TypeAttachmentStored {
		t.Fatalf("reply type = %q, want %q", reply.Type, protocol.TypeAttachmentStored)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != atTestChunkEnvID {
		t.Errorf("in_reply_to = %v, want %d (the completing chunk's envelope)", reply.InReplyTo, atTestChunkEnvID)
	}
	var stored protocol.AttachmentStoredPayload
	if err := json.Unmarshal(reply.Payload, &stored); err != nil {
		t.Fatalf("decode attachment_stored payload: %v", err)
	}
	if stored.AttachmentID != atTestAttachmentID {
		t.Errorf("attachment_id = %q, want %q", stored.AttachmentID, atTestAttachmentID)
	}
	// The success frame is published as carrying that one id and nothing else.
	var raw map[string]any
	if err := json.Unmarshal(reply.Payload, &raw); err != nil {
		t.Fatalf("re-decode attachment_stored payload: %v", err)
	}
	if len(raw) != 1 {
		t.Errorf("attachment_stored payload keys = %v, want only attachment_id", raw)
	}
}

// TestV2Session_AttachmentChunk_SentinelsMapToWireCodes is AC #3. Every sentinel
// Intake.Receive can return gets its own row, plus an error matching no arm, and
// each asserts the dotted code, the STATIC message and the retryability
// docs/protocol-mobile.md § Error codes publishes. The two rows this ticket chose
// are named so the choice is pinned by name rather than by falling out of a
// default.
func TestV2Session_AttachmentChunk_SentinelsMapToWireCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want attachmentReject
	}{
		{"invalid declaration", attachments.ErrInvalidDeclaration, rejectInvalidChunk},
		{"total_chunks mismatch", attachments.ErrTotalChunksMismatch, rejectInvalidChunk},
		{"index out of range", attachments.ErrIndexOutOfRange, rejectInvalidChunk},
		{"duplicate index", attachments.ErrDuplicateIndex, rejectInvalidChunk},
		{"#1897 picks: expired transfer", attachments.ErrUnknownUpload, rejectInvalidChunk},
		{"size mismatch", attachments.ErrSizeMismatch, rejectIntegrityFailed},
		{"digest mismatch", attachments.ErrDigestMismatch, rejectIntegrityFailed},
		{"upload too large", attachments.ErrUploadTooLarge, rejectTooLarge},
		{"too many uploads", attachments.ErrTooManyUploads, rejectTooManyUploads},
		{"invalid id", attachments.ErrInvalidID, rejectStorageFailed},
		{"not contained", attachments.ErrNotContained, rejectStorageFailed},
		{"write failed", attachments.ErrWriteFailed, rejectStorageFailed},
		// Answered rather than dropped: a future sentinel this switch has not
		// learned about is exactly when a silent drop would be worst.
		{"unmapped error", errors.New("some future sentinel"), rejectStorageFailed},
		// Wrapped the way the package's own refusals are — with the host paths
		// EnsureDir and Store fold in — so errors.Is still reaches the sentinel
		// and the wrap's text still never reaches the wire.
		{"wrapped, host path inside", wrapLikeStorage(attachments.ErrWriteFailed), rejectStorageFailed},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			intake := &fakeAttachmentIntake{err: tt.err}
			_, frames, send, recv, rec, _, _ := startAttachmentConn(t, intake)

			sendChunkFrame(t, frames, send, atTestChunkEnvID, chunkPayload(0, 1))
			reply := soleReply(t, rec, recv)

			if reply.InReplyTo == nil || *reply.InReplyTo != atTestChunkEnvID {
				t.Errorf("in_reply_to = %v, want %d", reply.InReplyTo, atTestChunkEnvID)
			}
			got := decodeErrorPayload(t, reply)
			if got.Code != tt.want.code {
				t.Errorf("code = %q, want %q", got.Code, tt.want.code)
			}
			if got.Message != tt.want.message {
				t.Errorf("message = %q, want the static %q", got.Message, tt.want.message)
			}
			if got.Retryable != tt.want.retryable {
				t.Errorf("retryable = %v, want %v", got.Retryable, tt.want.retryable)
			}
		})
	}
}

// wrapLikeStorage reproduces the shape attachments.Store's rename leg produces —
// a sentinel wrapped around a host path — so the wrapped row above measures what
// actually crosses the seam rather than a bare sentinel.
func wrapLikeStorage(sentinel error) error {
	return &pathWrappedError{sentinel: sentinel}
}

type pathWrappedError struct{ sentinel error }

func (e *pathWrappedError) Error() string {
	return e.sentinel.Error() + ": rename into " + atTestHostPath
}
func (e *pathWrappedError) Unwrap() error { return e.sentinel }

// TestV2Session_AttachmentChunk_DecodeFailure_Rejected pins that a payload which
// does not decode is REJECTED rather than tolerated: the seam is not called at
// all, and the client is answered attachment.invalid_chunk.
//
// The fixture is a TYPE MISMATCH inside otherwise-valid JSON, not garbled bytes:
// protocol.Envelope.Payload is a json.RawMessage, so json.Marshal validates it and
// a harness cannot put unparsable bytes on the wire (#1984 measured this). It is
// also ordered deliberately — attachment_id decodes cleanly BEFORE total_chunks
// fails — so the case exercises the partial-population hazard rather than an
// already-empty payload.
//
// THE BINDING ASSERTION IS THE CALL COUNT, not the value received: a tolerant
// `_ = json.Unmarshal` would hand the seam a populated attachment_id and a
// zero-valued declaration, which an assertion inspecting only what crossed could
// not tell from the real thing.
func TestV2Session_AttachmentChunk_DecodeFailure_Rejected(t *testing.T) {
	t.Parallel()

	intake := &fakeAttachmentIntake{}
	_, frames, send, recv, rec, logBuf, _ := startAttachmentConn(t, intake)

	bad := `{"attachment_id":"` + atTestAttachmentID + `","total_chunks":"not-a-number","filename":"` + atTestFilename + `"}`
	sendChunkFrame(t, frames, send, atTestChunkEnvID, bad)
	reply := soleReply(t, rec, recv)

	if n := intake.callCount(); n != 0 {
		t.Errorf("Receive called %d times on an undecodable payload, want 0", n)
	}
	got := decodeErrorPayload(t, reply)
	if got.Code != protocol.CodeAttachmentInvalidChunk {
		t.Errorf("code = %q, want %q", got.Code, protocol.CodeAttachmentInvalidChunk)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != atTestChunkEnvID {
		t.Errorf("in_reply_to = %v, want %d", reply.InReplyTo, atTestChunkEnvID)
	}
	// No attachment id in the reject record: the decode is what failed, so a
	// partially-populated id would attribute refused bytes to a transfer. And
	// nothing about the failure itself is echoed — encoding/json quotes offending
	// input into its error string, and those bytes are remote-authored.
	waitForLogContains(t, logBuf, "event=v2.attachment.chunk.decode_err")
	for _, banned := range []string{atTestAttachmentID, atTestFilename, "not-a-number"} {
		if strings.Contains(logBuf.String(), banned) {
			t.Errorf("decode-failure log carries %q; it must carry neither the payload nor a partial id", banned)
		}
	}
}

// TestV2Session_AttachmentChunk_UnusableConversation_RefusedBeforeTheSeam is
// #2143's gate. Empty, foreign and unwired-seam all answer the SAME published
// upload code, and none of them reaches the intake.
//
// THE BINDING ASSERTION IS THE CALL COUNT. A build that validated after Receive —
// or that fell back to the follow-active cursor on a miss — would still emit a
// reject and still pass an assertion that only read the reply, while having filed
// the bytes somewhere. Zero calls is what makes this a gate.
//
// THE CODE IS NOT storage_failed, and asserting the negative is the point: that
// row is published for a VERIFIED attachment the host could not write, and
// answering it here would keep the upload leg blaming the host for a claim the
// client got wrong. One code across all three rows is also deliberate — two would
// make the upload leg the conversation-existence oracle the retrieval leg closes.
func TestV2Session_AttachmentChunk_UnusableConversation_RefusedBeforeTheSeam(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// known is the membership seam. A nil one is the unwired posture, which
		// must fail CLOSED rather than waving every conversation through.
		known      func(string) bool
		payload    string
		wantReason string
	}{
		{
			name:       "conversation id is absent",
			known:      func(id string) bool { return id == atTestConvID },
			payload:    chunkPayloadIn("", 0, 1),
			wantReason: "conversation id is absent or empty",
		},
		{
			name:       "conversation is not one this daemon hosts",
			known:      func(id string) bool { return id == atTestConvID },
			payload:    chunkPayloadIn(atTestForeignConvID, 0, 1),
			wantReason: "conversation is not one this daemon hosts",
		},
		{
			// Fail-closed: a daemon that wired no membership seam hosts nothing
			// this handler can prove, so it files nothing.
			name:       "membership seam is unwired",
			known:      nil,
			payload:    chunkPayload(0, 1),
			wantReason: "conversation is not one this daemon hosts",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			intake := &fakeAttachmentIntake{}
			_, frames, send, recv, rec, logBuf, _ := startAttachmentConnKnowing(t, intake, tt.known)

			sendChunkFrame(t, frames, send, atTestChunkEnvID, tt.payload)
			reply := soleReply(t, rec, recv)

			if n := intake.callCount(); n != 0 {
				t.Errorf("Receive called %d times for an unusable destination, want 0: "+
					"the refusal must precede the seam, not follow it", n)
			}
			got := decodeErrorPayload(t, reply)
			if got.Code != protocol.CodeAttachmentInvalidChunk {
				t.Errorf("code = %q, want %q", got.Code, protocol.CodeAttachmentInvalidChunk)
			}
			if got.Code == protocol.CodeAttachmentStorageFailed {
				t.Errorf("code = %q: nothing was verified and nothing was written, so the host "+
					"is not what failed", got.Code)
			}
			if got.Retryable {
				t.Errorf("retryable = true, want false: the same frames reproduce this refusal")
			}
			if got.Message != msgAttachmentInvalidChunk {
				t.Errorf("message = %q, want the static %q", got.Message, msgAttachmentInvalidChunk)
			}
			if reply.InReplyTo == nil || *reply.InReplyTo != atTestChunkEnvID {
				t.Errorf("in_reply_to = %v, want %d", reply.InReplyTo, atTestChunkEnvID)
			}

			// One wire code, three daemon-authored reasons: indistinguishable to
			// a client, diagnosable to an operator.
			waitForLogContains(t, logBuf, "reason="+strconv.Quote(tt.wantReason))
			// The conversation id is never shape-validated here, so it appears in
			// no record on any arm — the log-injection shape § Attachments
			// forbids. atTestConvID is excluded: the unwired row legitimately
			// carries a hosted id nothing rejected it for.
			if strings.Contains(logBuf.String(), atTestForeignConvID) {
				t.Errorf("refusal log names the requested conversation id, which this handler "+
					"validates for membership and never for shape: %q", logBuf.String())
			}
		})
	}
}

// TestV2Session_AttachmentChunk_HandsTheSeamTheNamedConversation is the gate's
// positive half, and it is what stops the rows above from passing under a build
// that simply refuses everything. The destination the seam receives is read off
// the RECORDED CALL rather than off the chunk it came with, so a handler that
// passed some other conversation — the follow-active cursor, say — reddens here
// even though the frame that crossed is intact.
func TestV2Session_AttachmentChunk_HandsTheSeamTheNamedConversation(t *testing.T) {
	t.Parallel()

	intake := &fakeAttachmentIntake{storedID: atTestAttachmentID, stored: true}
	_, frames, send, recv, rec, _, _ := startAttachmentConn(t, intake)

	sendChunkFrame(t, frames, send, atTestChunkEnvID, chunkPayload(0, 1))
	if got := soleReply(t, rec, recv); got.Type != protocol.TypeAttachmentStored {
		t.Fatalf("reply type = %q, want %q", got.Type, protocol.TypeAttachmentStored)
	}

	calls := intake.callSnapshot()
	if len(calls) != 1 {
		t.Fatalf("Receive called %d times, want 1", len(calls))
	}
	if calls[0].conversationID != atTestConvID {
		t.Errorf("Receive conversationID = %q, want %q (the id the frame named)",
			calls[0].conversationID, atTestConvID)
	}
	if calls[0].chunk.ConversationID != atTestConvID {
		t.Errorf("the chunk crossed with conversation_id %q, want %q",
			calls[0].chunk.ConversationID, atTestConvID)
	}
}

// TestV2Session_AttachmentChunk_NilIntake_ConsumedNotReplied pins the unwired
// posture: the frame is still INTERCEPTED — so it never reaches dispatch.Route and
// never draws its unknown-type error reply — but nothing is decoded and nothing is
// answered. This is what leaves every non-production construction site behaving
// unchanged.
func TestV2Session_AttachmentChunk_NilIntake_ConsumedNotReplied(t *testing.T) {
	t.Parallel()

	mgr, frames, send, _, rec, logBuf, stop := startAttachmentConn(t, nil)

	sendChunkFrame(t, frames, send, atTestChunkEnvID, chunkPayload(0, 1))
	waitForLogContains(t, logBuf, "event=v2.attachment.chunk.inert")

	if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
		t.Errorf("attachment_chunk with no intake drew %d noise_msg, want 0 (consumed, not answered)", len(msgs))
	}
	stop() // Run has exited; mgr.sessions is safe to read from this goroutine.
	if s := mgr.sessions[v2TestConnID]; s == nil || s.State() != V2StateOpen {
		t.Error("session not open after an inert attachment_chunk")
	}
	// An unwired daemon parses no remote-authored bytes at all, which is the
	// property the nil guard buys ahead of the decode.
	if strings.Contains(logBuf.String(), atTestAttachmentID) {
		t.Error("inert path logged the attachment id; it must decode nothing")
	}
}

// TestV2Session_AttachmentChunk_TeardownReleasesConn is AC #4: a conn torn down
// with uploads in flight releases them at the drop, so the daemon-wide capacity
// returns without waiting for the idle window. Nil-intake teardown is the second
// row — closeWith must not panic on the unwired posture.
func TestV2Session_AttachmentChunk_TeardownReleasesConn(t *testing.T) {
	t.Parallel()

	intake := &fakeAttachmentIntake{}
	mgr, frames, send, _, rec, logBuf, stop := startAttachmentConn(t, intake)

	sendChunkFrame(t, frames, send, atTestChunkEnvID, chunkPayload(0, 4))
	waitForResolverCall(t, intake.callCount, 1, "Receive")
	waitForLogContains(t, logBuf, "event=v2.attachment.chunk.accepted")

	if got := intake.releasedSnapshot(); len(got) != 0 {
		t.Fatalf("ReleaseConn called %v before teardown, want none", got)
	}
	// Drive the conn through closeWith — the single teardown point every drop
	// funnels into — by way of the protocol-mismatch arm a phone-sent noise_resp
	// takes. Any other route into closeWith would exercise the same cluster.
	tearDownConn(t, frames)
	waitForReleased(t, intake, 1)
	waitForCloseCode(t, rec, v2TestConnID, uint16(StatusProtocolMismatch))
	stop() // Run has exited; mgr.sessions is safe to read from this goroutine.

	got := intake.releasedSnapshot()
	if len(got) != 1 || got[0] != v2TestConnID {
		t.Errorf("ReleaseConn calls = %v, want exactly [%q]", got, v2TestConnID)
	}
	if s := mgr.sessions[v2TestConnID]; s != nil {
		t.Error("session still registered after teardown")
	}
}

// tearDownConn drives the conn into closeWith via the protocol-mismatch arm.
func tearDownConn(t *testing.T, frames chan protocol.RoutingEnvelope) {
	t.Helper()
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseResp, []byte("x"))
}

// TestV2Session_AttachmentChunk_NilIntake_TeardownDoesNotPanic is the nil-seam
// half of AC #4's guard: closeWith runs its release unconditionally, so the nil
// check has to be there and not merely assumed.
func TestV2Session_AttachmentChunk_NilIntake_TeardownDoesNotPanic(t *testing.T) {
	t.Parallel()

	_, frames, _, _, rec, _, stop := startAttachmentConn(t, nil)
	tearDownConn(t, frames)
	waitForCloseCode(t, rec, v2TestConnID, uint16(StatusProtocolMismatch))
	stop()
}

func waitForReleased(t *testing.T, f *fakeAttachmentIntake, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.releasedSnapshot()) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("ReleaseConn not called %d time(s) within the deadline; got %v", n, f.releasedSnapshot())
}

// TestV2Session_AttachmentChunk_CarriesNoBannedStrings is AC #5, run across all
// three answer shapes at once — accepted, stored and refused — because the ban is
// the path's, not one arm's. It scans the whole captured log and every emitted
// wire frame for the bytes, the raw filename, the declared digest and a host path,
// and separately pins that the storage_failed reply carries the STATIC message
// § Error codes mandates.
//
// The refusal arm's error is wrapped around a host path exactly as
// attachments.Store's rename leg wraps one, which is what makes the "err is never
// logged, not merely never replied" rule measurable: interpolating the error into
// the reject record would redden this test and nothing else.
func TestV2Session_AttachmentChunk_CarriesNoBannedStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		intake    *fakeAttachmentIntake
		waitEvent string
	}{
		{"accepted", &fakeAttachmentIntake{}, "event=v2.attachment.chunk.accepted"},
		{"stored", &fakeAttachmentIntake{storedID: atTestAttachmentID, stored: true}, "event=v2.attachment.stored"},
		{"refused", &fakeAttachmentIntake{err: wrapLikeStorage(attachments.ErrWriteFailed)}, "event=v2.attachment.chunk.refused"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, frames, send, recv, rec, logBuf, _ := startAttachmentConn(t, tt.intake)
			sendChunkFrame(t, frames, send, atTestChunkEnvID, chunkPayload(0, 1))
			waitForLogContains(t, logBuf, tt.waitEvent)

			// Three spellings of the chunk bytes, because a slog record and a
			// wire frame render them differently and a single needle would let
			// one of the two through: the raw ASCII, the base64 encoding/json
			// gives a []byte, and the decimal slice fmt gives one. Without the
			// last of the three, `"data", chunk.Data` in a log call would pass
			// this test.
			banned := []string{
				atTestFilename,
				atTestDigest,
				atTestHostPath,
				atTestData,
				base64.StdEncoding.EncodeToString([]byte(atTestData)),
				fmt.Sprintf("%v", []byte(atTestData)),
			}
			for _, b := range banned {
				if strings.Contains(logBuf.String(), b) {
					t.Errorf("log carries banned string %q on the %s path", b, tt.name)
				}
			}
			for _, msg := range noiseMsgsForConn(t, rec, v2TestConnID) {
				inner := decryptAppFrame(t, msg, recv)
				frame, err := json.Marshal(inner)
				if err != nil {
					t.Fatalf("re-marshal reply: %v", err)
				}
				for _, b := range banned {
					if strings.Contains(string(frame), b) {
						t.Errorf("wire reply carries banned string %q on the %s path", b, tt.name)
					}
				}
				if inner.Type == protocol.TypeError {
					if got := decodeErrorPayload(t, inner).Message; got != msgAttachmentStorageFailed {
						t.Errorf("storage_failed message = %q, want the static %q", got, msgAttachmentStorageFailed)
					}
				}
			}
			// The attachment id IS loggable — the declaring type says so and
			// attachment_stored echoes it to the wire — so assert it positively
			// rather than leaving "no banned strings" to pass vacuously against a
			// handler that logged nothing at all.
			if !strings.Contains(logBuf.String(), atTestAttachmentID) {
				t.Errorf("log carries no attachment id on the %s path; the scan above would pass vacuously", tt.name)
			}
		})
	}
}

// TestV2Session_AttachmentChunk_RunsOffRun is AC #1's second half. A completing
// chunk hashes and writes up to the per-upload byte bound, which is the work #1491
// had to move off Run for handleDebugBundleRequest — so this pins that an upload
// parked inside Receive does not stall a SECOND conn's frames. If the handler ran
// inline in dispatchAppFrame, conn B's request would sit behind conn A's upload.
func TestV2Session_AttachmentChunk_RunsOffRun(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	intake := &fakeAttachmentIntake{block: release}

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	logger, _ := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            logger,
		AttachmentIntake:  intake,
		KnownConversation: func(id string) bool { return id == atTestConvID },
	})
	t.Cleanup(stop)

	aSend, _ := openModalConn(t, mgr, frames, rec, respPub, v2TestConnID, []string{protocol.CapabilityInteractive})
	bSend, bRecv := openModalConn(t, mgr, frames, rec, respPub, atTestConnB, []string{protocol.CapabilityInteractive})

	// Conn A parks inside Receive.
	frames <- sealAppFrameConn(t, aSend, v2TestConnID, protocol.Envelope{
		ID:      atTestChunkEnvID,
		Type:    protocol.TypeAttachmentChunk,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(chunkPayload(0, 2)),
	})
	waitForResolverCall(t, intake.callCount, 1, "Receive")

	// Conn B is answered while A is still parked. B's frame is one whose payload
	// cannot decode, so its handler refuses it BEFORE reaching the shared seam —
	// which is what keeps this a test of the Run loop's availability rather than
	// of the fake's own blocking.
	frames <- sealAppFrameConn(t, bSend, atTestConnB, protocol.Envelope{
		ID:      atTestChunkEnvID + 1,
		Type:    protocol.TypeAttachmentChunk,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"attachment_id":"b","total_chunks":"not-a-number"}`),
	})
	msgs := waitForConnNoiseMsg(t, rec, atTestConnB, 1)
	reply := decryptAppFrame(t, msgs[0], bRecv)
	if got := decodeErrorPayload(t, reply).Code; got != protocol.CodeAttachmentInvalidChunk {
		t.Errorf("conn B code = %q, want %q", got, protocol.CodeAttachmentInvalidChunk)
	}

	close(release)
}
