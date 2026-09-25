package relay

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #2598 live workspace read fixtures ---

// The path and filename are sentinels a log scan cannot match incidentally;
// both are banned from every log line and every reject.
const (
	wfTestConvID   = "5b8e2f14-9c33-4a71-b0d6-2598cafe0001" // canonical UUIDv4
	wfTestMintedID = "6c9f3a25-0d44-4b82-a1e7-2598cafe0002" // what the fake reader "mints"
	wfTestPath     = "ZZ2598DIR/ZZ2598NOTE.md"
	wfTestFilename = "ZZ2598NOTE.md"
	wfTestReqEnvID = uint64(25980)
)

// readCall captures one WorkspaceFileRead call.
type readCall struct {
	conversationID string
	path           string
}

// fakeWorkspaceReader is a relay-side double for the WorkspaceFileRead seam. The
// real reader's confinement and markdown rule are cmd/pyry's to prove; here the
// seam answers a scripted (file, ok) so the handler's order and answers are
// addressable one at a time.
type fakeWorkspaceReader struct {
	mu    sync.Mutex
	file  WorkspaceFile
	ok    bool
	calls []readCall
}

func (f *fakeWorkspaceReader) read(conversationID, path string) (WorkspaceFile, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, readCall{conversationID: conversationID, path: path})
	return f.file, f.ok
}

func (f *fakeWorkspaceReader) callSnapshot() []readCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]readCall(nil), f.calls...)
}

// startWorkspaceReadConn stands up a manager with the membership gate and the
// reader seam wired (either may be nil) and drives one paired handshake, so
// every test goes through dispatchAppFrame and the conn's appFrameWorker.
func startWorkspaceReadConn(t *testing.T, knownConv func(string) bool, read func(string, string) (WorkspaceFile, bool)) (*openSession, *syncLogBuffer) {
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
		WorkspaceFileRead: read,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)
	return sess, logBuf
}

// sendReadWorkspaceFile seals one read_workspace_file envelope. The envelope ID
// is load-bearing: every answer correlates on it through in_reply_to.
func sendReadWorkspaceFile(t *testing.T, sess *openSession, envID uint64, payload string) {
	t.Helper()
	sess.frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      envID,
		Type:    protocol.TypeReadWorkspaceFile,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
}

func readPayload(conversationID, path string) string {
	p, err := json.Marshal(protocol.ReadWorkspaceFilePayload{ConversationID: conversationID, Path: path})
	if err != nil {
		panic(err) // closed struct of two strings; cannot fail
	}
	return string(p)
}

// TestV2Session_ReadWorkspaceFile_StreamsTheReadBytes: a found file is answered
// as a multi-chunk attachment_chunk stream correlated to the request, keyed on
// the id the reader minted and named by the resolved file's base name, and the
// seam is handed the frame's two fields unaltered.
func TestV2Session_ReadWorkspaceFile_StreamsTheReadBytes(t *testing.T) {
	t.Parallel()

	blob := patternBlob(2*protocol.MaxAttachmentChunkBytes + 7) // 3 chunks
	rd := &fakeWorkspaceReader{file: WorkspaceFile{AttachmentID: wfTestMintedID, Filename: wfTestFilename, Data: blob}, ok: true}
	sess, _ := startWorkspaceReadConn(t, knownOnly(wfTestConvID), rd.read)

	sendReadWorkspaceFile(t, sess, wfTestReqEnvID, readPayload(wfTestConvID, wfTestPath))

	envs := waitForReplies(t, sess, 3)
	got, err := ReassembleAttachment(envs, wfTestMintedID, wfTestReqEnvID)
	if err != nil {
		t.Fatalf("reassemble the answered stream: %v", err)
	}
	if string(got) != string(blob) {
		t.Errorf("reassembled %d bytes, want the read file's %d — byte for byte", len(got), len(blob))
	}
	for _, p := range decodeChunkPayloads(t, envs) {
		if p.Filename != wfTestFilename {
			t.Errorf("chunk filename = %q, want the resolved base name %q", p.Filename, wfTestFilename)
		}
	}

	calls := rd.callSnapshot()
	if len(calls) != 1 || calls[0] != (readCall{wfTestConvID, wfTestPath}) {
		t.Errorf("reader calls = %+v, want exactly one with (%q, %q)", calls, wfTestConvID, wfTestPath)
	}
}

// TestV2Session_ReadWorkspaceFile_EveryRefusalAnswersOneCode: every cause is the
// same static, non-retryable attachment.not_found with exactly one frame, and
// the membership gate runs BEFORE the reader — which the reader's call count,
// not the identical wire answer, is what distinguishes. No refusal logs the
// requested path or the requested conversation id.
func TestV2Session_ReadWorkspaceFile_EveryRefusalAnswersOneCode(t *testing.T) {
	t.Parallel()

	const unknownConv = "7d0a4b36-1e55-4c93-b2f8-2598cafe0003"
	tests := []struct {
		name      string
		known     func(string) bool
		readerOK  bool
		payload   string
		wantCalls int
	}{
		{
			name:     "payload does not decode",
			known:    knownOnly(wfTestConvID),
			readerOK: true,
			payload:  `{"conversation_id":42,"path":"` + wfTestPath + `"}`,
		},
		{
			name:     "unknown conversation",
			known:    knownOnly(wfTestConvID),
			readerOK: true,
			payload:  readPayload(unknownConv, wfTestPath),
		},
		{
			name:     "empty conversation id",
			known:    knownOnly(wfTestConvID),
			readerOK: true,
			payload:  readPayload("", wfTestPath),
		},
		{
			name:     "nil membership gate refuses everything",
			readerOK: true,
			payload:  readPayload(wfTestConvID, wfTestPath),
		},
		{
			// One bool covers wrong extension, empty workspace, missing file,
			// out-of-tree path, non-regular file and over the bound alike.
			name:      "reader refuses",
			known:     knownOnly(wfTestConvID),
			payload:   readPayload(wfTestConvID, wfTestPath),
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rd := &fakeWorkspaceReader{
				file: WorkspaceFile{AttachmentID: wfTestMintedID, Filename: wfTestFilename, Data: []byte("# note")},
				ok:   tt.readerOK,
			}
			sess, logBuf := startWorkspaceReadConn(t, tt.known, rd.read)

			sendReadWorkspaceFile(t, sess, wfTestReqEnvID, tt.payload)

			envs := waitForReplies(t, sess, 1)
			if envs[0].Type == protocol.TypeAttachmentChunk {
				t.Fatal("a refused read was answered with an attachment frame")
			}
			assertRejectFrame(t, envs[0], protocol.CodeAttachmentNotFound, msgAttachmentNotFound, false, wfTestReqEnvID)
			if n := len(rd.callSnapshot()); n != tt.wantCalls {
				t.Errorf("reader calls = %d, want %d — the membership gate must fire before the reader", n, tt.wantCalls)
			}

			logs := logBuf.String()
			if !strings.Contains(logs, "v2.workspace_file.refused") {
				t.Fatalf("no refusal record in the captured log, so the bans below prove nothing.\nlogs:\n%s", logs)
			}
			for what, needle := range map[string]string{
				"requested path":     wfTestPath,
				"requested filename": wfTestFilename,
				"conversation id":    wfTestConvID,
				"unknown conv id":    unknownConv,
			} {
				if strings.Contains(logs, needle) {
					t.Errorf("the refusal log carries the %s", what)
				}
			}
		})
	}
}

// TestV2Session_ReadWorkspaceFile_NilReaderIsInertButConsumed: an unwired
// daemon parses nothing, asks nothing and answers nothing — not even
// dispatch.Route's unknown-type error.
func TestV2Session_ReadWorkspaceFile_NilReaderIsInertButConsumed(t *testing.T) {
	t.Parallel()
	sess, _ := startWorkspaceReadConn(t, knownOnly(wfTestConvID), nil)
	sendReadWorkspaceFile(t, sess, wfTestReqEnvID, readPayload(wfTestConvID, wfTestPath))
	expectNoReply(t, sess)
}

// TestV2Session_ReadWorkspaceFile_SuccessLogCarriesNoContent: the success path
// logs the minted id and nothing the read produced or the client named.
func TestV2Session_ReadWorkspaceFile_SuccessLogCarriesNoContent(t *testing.T) {
	t.Parallel()

	blob := []byte("ZZ2598CONTENTBYTESZZ")
	rd := &fakeWorkspaceReader{file: WorkspaceFile{AttachmentID: wfTestMintedID, Filename: wfTestFilename, Data: blob}, ok: true}
	sess, logBuf := startWorkspaceReadConn(t, knownOnly(wfTestConvID), rd.read)

	sendReadWorkspaceFile(t, sess, wfTestReqEnvID, readPayload(wfTestConvID, wfTestPath))
	envs := waitForReplies(t, sess, 1)
	if _, err := ReassembleAttachment(envs, wfTestMintedID, wfTestReqEnvID); err != nil {
		t.Fatalf("the run must have streamed the file, or the scan below is vacuous: %v", err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "v2.workspace_file.served") || !strings.Contains(logs, wfTestMintedID) {
		t.Fatalf("the served record with the minted id is absent.\nlogs:\n%s", logs)
	}
	for what, needle := range map[string]string{
		"requested path":        wfTestPath,
		"filename":              wfTestFilename,
		"conversation id":       wfTestConvID,
		"content bytes (ascii)": string(blob),
		"content bytes (slog)":  decimalSlice(blob),
	} {
		if strings.Contains(logs, needle) {
			t.Errorf("the captured log carries the %s", what)
		}
	}
}
