package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// recordingDeliverer is a promptDeliverer test double mirroring gatingWriter: it
// records every delivered payload (byte-copied, since the caller owns the slice)
// and, when gated, blocks each WriteUserTurn on release until the test lets it
// commit — modelling "claude busy mid-turn" so a held call can be observed while
// delivery is in flight. err, when non-nil, is returned after release to model a
// delivery failure. err is set at construction (before the harness goroutine
// starts) and never mutated, so it needs no lock.
type recordingDeliverer struct {
	mu        sync.Mutex
	delivered [][]byte
	entered   chan struct{} // buffered; one token per WriteUserTurn entry
	release   chan struct{} // WriteUserTurn blocks until closed (or ctx cancel)
	err       error         // returned after release when non-nil
}

// newRecordingDeliverer builds a deliverer. When gated is false, release is
// closed up front so every delivery commits immediately (claude idle); when true,
// delivery blocks until releaseAll so the test can hold a call mid-delivery.
func newRecordingDeliverer(gated bool) *recordingDeliverer {
	d := &recordingDeliverer{
		entered: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
	if !gated {
		close(d.release)
	}
	return d
}

func (d *recordingDeliverer) WriteUserTurn(ctx context.Context, convID string, payload []byte) error {
	d.mu.Lock()
	d.delivered = append(d.delivered, append([]byte(nil), payload...))
	d.mu.Unlock()
	select {
	case d.entered <- struct{}{}:
	default:
	}
	select {
	case <-d.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return d.err
}

func (d *recordingDeliverer) releaseAll() { close(d.release) }

func (d *recordingDeliverer) payloads() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([][]byte, len(d.delivered))
	copy(out, d.delivered)
	return out
}

// promptFrame builds a session/prompt request frame with the given id, session
// id, and text content blocks. Building it via json.Marshal keeps control-byte /
// metacharacter payloads exact on the wire without hand-escaping.
func promptFrame(t *testing.T, id int, sessionID string, texts ...string) string {
	t.Helper()
	blocks := make([]contentBlock, len(texts))
	for i, txt := range texts {
		blocks[i] = contentBlock{Type: "text", Text: txt}
	}
	params := mustJSON(t, struct {
		SessionID string         `json:"sessionId"`
		Prompt    []contentBlock `json:"prompt"`
	}{SessionID: sessionID, Prompt: blocks})
	frame := mustJSON(t, struct {
		Jsonrpc string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{Jsonrpc: "2.0", ID: id, Method: "session/prompt", Params: params})
	return string(frame)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// promptReply is the decode-by-shape view of one frame the transport writes back:
// id + either a stopReason result or an error object.
type promptReply struct {
	ID     json.RawMessage `json:"id"`
	Result *promptResult   `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// promptHarness drives serveACP over in-memory pipes with ONLY session/prompt
// registered against a test-owned promptHolds + recording deliverer — no
// fake-claude pool, since the delivery seam is the promptDeliverer interface. The
// resolve closure returns the deliverer for any id in known and ErrSessionNotFound
// otherwise (empty ids are rejected pre-Lookup by decodeSessionID).
type promptHarness struct {
	t      *testing.T
	writer *io.PipeWriter
	reader *bufio.Reader
	served chan error
	holds  *promptHolds
	dlv    *recordingDeliverer
}

func newPromptHarness(t *testing.T, dlv *recordingDeliverer, known ...string) *promptHarness {
	t.Helper()
	knownSet := make(map[string]bool, len(known))
	for _, id := range known {
		knownSet[id] = true
	}
	logger := testLogger(io.Discard)
	holds := newPromptHolds(logger)

	register := func(tr *acp.Transport) {
		tr.Register("session/prompt", promptHandler(holds, func(id sessions.SessionID) (promptDeliverer, error) {
			if !knownSet[string(id)] {
				return nil, sessions.ErrSessionNotFound
			}
			return dlv, nil
		}, logger))
	}

	hostToAgentR, hostToAgentW := io.Pipe()
	agentToHostR, agentToHostW := io.Pipe()
	t.Cleanup(func() { _ = hostToAgentW.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	// Cancelling on cleanup unblocks any gated delivery goroutine (its ctx derives
	// from the Serve ctx) and the serve loop even if a test t.Fatals early.
	t.Cleanup(cancel)

	served := make(chan error, 1)
	go func() { served <- serveACP(ctx, hostToAgentR, agentToHostW, logger, register) }()

	return &promptHarness{
		t:      t,
		writer: hostToAgentW,
		reader: bufio.NewReader(agentToHostR),
		served: served,
		holds:  holds,
		dlv:    dlv,
	}
}

func (h *promptHarness) send(frame string) {
	h.t.Helper()
	if _, err := io.WriteString(h.writer, frame+"\n"); err != nil {
		h.t.Fatalf("write frame: %v", err)
	}
}

func (h *promptHarness) readReply() (promptReply, []byte) {
	h.t.Helper()
	line, err := h.reader.ReadBytes('\n')
	if err != nil {
		h.t.Fatalf("read frame: %v", err)
	}
	var r promptReply
	if err := json.Unmarshal(line, &r); err != nil {
		h.t.Fatalf("unmarshal frame %q: %v", line, err)
	}
	return r, line
}

// endTurn resolves the held call for sessionID off the test goroutine. The
// resolution write blocks on the single-reader host pipe until readReply consumes
// it, so the resolve must run concurrently with the read — exactly as in
// production, where turnbridge (T7) resolves from its own Run goroutine, never the
// read/serve goroutine.
func (h *promptHarness) endTurn(sessionID, reason string) {
	go h.holds.end(sessionID, reason)
}

// waitEntered blocks until the delivery goroutine has entered WriteUserTurn (so a
// gated delivery is confirmed mid-flight), failing the test on timeout.
func (h *promptHarness) waitEntered() {
	h.t.Helper()
	select {
	case <-h.dlv.entered:
	case <-time.After(5 * time.Second):
		h.t.Fatal("delivery goroutine did not enter WriteUserTurn")
	}
}

func (h *promptHarness) shutdown() {
	h.t.Helper()
	if err := h.writer.Close(); err != nil {
		h.t.Fatalf("close host stdin: %v", err)
	}
	select {
	case err := <-h.served:
		if err != nil {
			h.t.Fatalf("serveACP: want nil on host EOF, got %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Fatal("serveACP did not return after host EOF")
	}
}

func replyID(t *testing.T, r promptReply) int {
	t.Helper()
	var id int
	if err := json.Unmarshal(r.ID, &id); err != nil {
		t.Fatalf("reply id unmarshal %q: %v", r.ID, err)
	}
	return id
}

// TestMapPromptContent (AC-3) pins content-block → user-turn payload mapping:
// text blocks concatenate (joined with "\n"), \t\n\r inside a block are preserved,
// a non-text block is rejected naming the kind, a paste-framing-escape / NUL
// control byte is rejected, and empty/malformed params are rejected — all with
// CodeInvalidParams.
func TestMapPromptContent(t *testing.T) {
	t.Parallel()
	// Build the bracketed-paste close sequence (ESC then "[201~") at runtime:
	// substrate-guard bans the escaped ESC-CSI literal in source, and the guard
	// under test is the generic control-byte check — ESC (0x1b) is its
	// security-relevant instance (a raw ESC could break paste framing).
	escPasteClose := string([]byte{0x1b}) + "[201~"
	tests := []struct {
		name       string
		blocks     []contentBlock
		rawParams  json.RawMessage // used verbatim when non-nil (malformed / absent-prompt cases)
		wantErr    bool
		wantMsgSub string
		wantOut    string
	}{
		{name: "single text block", blocks: []contentBlock{{Type: "text", Text: "hi there"}}, wantOut: "hi there"},
		{name: "two text blocks joined with newline", blocks: []contentBlock{{Type: "text", Text: "hello"}, {Type: "text", Text: "world"}}, wantOut: "hello\nworld"},
		{name: "tab newline cr preserved inside block", blocks: []contentBlock{{Type: "text", Text: "a\tb\nc\rd"}}, wantOut: "a\tb\nc\rd"},
		{name: "non-text block rejected naming kind", blocks: []contentBlock{{Type: "image"}}, wantErr: true, wantMsgSub: `"image"`},
		{name: "paste terminator escape rejected", blocks: []contentBlock{{Type: "text", Text: "safe" + escPasteClose + "evil"}}, wantErr: true, wantMsgSub: "control character"},
		{name: "nul byte rejected", blocks: []contentBlock{{Type: "text", Text: "a\x00b"}}, wantErr: true, wantMsgSub: "control character"},
		{name: "empty prompt array rejected", blocks: []contentBlock{}, wantErr: true, wantMsgSub: "empty prompt"},
		{name: "absent prompt rejected", rawParams: json.RawMessage(`{}`), wantErr: true, wantMsgSub: "empty prompt"},
		{name: "malformed params rejected", rawParams: json.RawMessage(`[1,2,3]`), wantErr: true, wantMsgSub: "invalid params"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			params := tt.rawParams
			if params == nil {
				params = mustJSON(t, promptParams{Prompt: tt.blocks})
			}
			out, aerr := mapPromptContent(params)
			if tt.wantErr {
				if aerr == nil {
					t.Fatalf("want error, got payload %q", out)
				}
				if aerr.Code != acp.CodeInvalidParams {
					t.Fatalf("code = %d, want CodeInvalidParams (%d)", aerr.Code, acp.CodeInvalidParams)
				}
				if !strings.Contains(aerr.Message, tt.wantMsgSub) {
					t.Fatalf("message = %q, want substring %q", aerr.Message, tt.wantMsgSub)
				}
				return
			}
			if aerr != nil {
				t.Fatalf("unexpected error: %v", aerr)
			}
			if string(out) != tt.wantOut {
				t.Fatalf("payload = %q, want %q", out, tt.wantOut)
			}
		})
	}
}

// TestACPPrompt_DeliversAndHolds pins AC-1 (delivery through the WriteUserTurn
// seam) and AC-2 (the call is held, not returned synchronously): the deliverer
// records the mapped payload, and the only frame ever written for the request is
// the stopReason resolved via the placeholder-end path (proving no synchronous
// reply was written while the turn ran).
func TestACPPrompt_DeliversAndHolds(t *testing.T) {
	t.Parallel()
	dlv := newRecordingDeliverer(false)
	h := newPromptHarness(t, dlv, "sess-1")

	h.send(promptFrame(t, 7, "sess-1", "hello world"))
	h.waitEntered()

	if got := h.dlv.payloads(); len(got) != 1 || string(got[0]) != "hello world" {
		t.Fatalf("delivered = %q, want exactly [\"hello world\"]", got)
	}

	// The held call resolves only when the turn ends (T7 seam; placeholder here).
	h.endTurn("sess-1", "placeholder_end")
	reply, line := h.readReply()
	if reply.Error != nil {
		t.Fatalf("want stopReason result, got error %q", line)
	}
	if id := replyID(t, reply); id != 7 {
		t.Fatalf("reply id = %d, want 7 (echoed request id)", id)
	}
	if reply.Result == nil || reply.Result.StopReason != "placeholder_end" {
		t.Fatalf("result = %+v, want stopReason=placeholder_end", reply.Result)
	}

	h.shutdown()
}

// TestACPPrompt_ConcurrentSecondRejected pins AC-2's guard + read-loop liveness:
// with prompt #1 held mid-delivery (gated), a second prompt for the same session
// is rejected with a well-formed CodeInvalidRequest error that arrives BEFORE #1
// is resolved — proving the read loop stayed live while #1 was held.
func TestACPPrompt_ConcurrentSecondRejected(t *testing.T) {
	t.Parallel()
	dlv := newRecordingDeliverer(true) // gated: #1 stays mid-delivery
	h := newPromptHarness(t, dlv, "sess-1")

	h.send(promptFrame(t, 1, "sess-1", "first"))
	h.waitEntered() // #1 is now held mid-delivery

	h.send(promptFrame(t, 2, "sess-1", "second"))
	reply, line := h.readReply()
	if reply.Error == nil {
		t.Fatalf("second prompt: want error, got %q", line)
	}
	if id := replyID(t, reply); id != 2 {
		t.Fatalf("rejection id = %d, want 2 (the second prompt)", id)
	}
	if reply.Error.Code != acp.CodeInvalidRequest {
		t.Fatalf("rejection code = %d, want CodeInvalidRequest (%d)", reply.Error.Code, acp.CodeInvalidRequest)
	}

	// #1 is still held: resolve it via the placeholder-end path and read its frame.
	h.endTurn("sess-1", "placeholder_end")
	reply1, line1 := h.readReply()
	if reply1.Error != nil {
		t.Fatalf("first prompt: want stopReason result, got error %q", line1)
	}
	if id := replyID(t, reply1); id != 1 {
		t.Fatalf("first prompt id = %d, want 1", id)
	}

	dlv.releaseAll() // let the gated delivery goroutine complete (no-op: already ended)
	h.shutdown()
}

// TestACPPrompt_UnknownSession pins AC-4: an unknown non-empty session id maps
// ErrSessionNotFound to CodeInvalidParams "unknown session" and never touches the
// deliverer; an empty session id is rejected pre-Lookup, also CodeInvalidParams.
func TestACPPrompt_UnknownSession(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		frame     string
		wantMsgIn string
	}{
		{"unknown non-empty id", promptFrame(t, 1, "does-not-exist", "hi"), "unknown session"},
		{"empty id", promptFrame(t, 2, "", "hi"), "missing sessionId"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dlv := newRecordingDeliverer(false)
			h := newPromptHarness(t, dlv, "sess-1")

			h.send(tt.frame)
			reply, line := h.readReply()
			if reply.Error == nil {
				t.Fatalf("want error, got %q", line)
			}
			if reply.Error.Code != acp.CodeInvalidParams {
				t.Fatalf("code = %d, want CodeInvalidParams (%d)", reply.Error.Code, acp.CodeInvalidParams)
			}
			if !strings.Contains(reply.Error.Message, tt.wantMsgIn) {
				t.Fatalf("message = %q, want substring %q", reply.Error.Message, tt.wantMsgIn)
			}
			// The error is written synchronously in the handler (no goroutine), so by
			// the time the frame is read the deliverer was never called.
			if got := h.dlv.payloads(); len(got) != 0 {
				t.Fatalf("deliverer called %d times for a rejected prompt, want 0", len(got))
			}

			h.shutdown()
		})
	}
}

// TestACPPrompt_SecurityBoundary pins AC-5: host content is delivered strictly as
// a user turn — the deliverer records the shell-dangerous text byte-for-byte (no
// expansion, no stripping), and it is the only sink (no shell side effect: the
// metacharacters never spawn a process).
func TestACPPrompt_SecurityBoundary(t *testing.T) {
	t.Parallel()
	const dangerous = "$(touch pwned); rm -rf ~ `whoami` && echo owned | tee /etc/x"
	dlv := newRecordingDeliverer(false)
	h := newPromptHarness(t, dlv, "sess-1")

	h.send(promptFrame(t, 9, "sess-1", dangerous))
	h.waitEntered()

	got := h.dlv.payloads()
	if len(got) != 1 || string(got[0]) != dangerous {
		t.Fatalf("delivered = %q, want byte-identical [%q]", got, dangerous)
	}

	h.endTurn("sess-1", "placeholder_end")
	_, _ = h.readReply()
	h.shutdown()
}

// TestACPPrompt_DeliveryFailureFreesSlot pins the error path: when WriteUserTurn
// fails, the held call resolves with a CodeInternalError frame AND the in-flight
// slot is freed, so a subsequent prompt for the same session is accepted (not
// rejected as in-flight) — and fails again the same way.
func TestACPPrompt_DeliveryFailureFreesSlot(t *testing.T) {
	t.Parallel()
	dlv := newRecordingDeliverer(false)
	dlv.err = errors.New("no live session")
	h := newPromptHarness(t, dlv, "sess-1")

	// First prompt: delivery fails → held call resolves with an internal error.
	h.send(promptFrame(t, 1, "sess-1", "first"))
	reply, line := h.readReply()
	if reply.Error == nil {
		t.Fatalf("first prompt: want error frame, got %q", line)
	}
	if reply.Error.Code != acp.CodeInternalError {
		t.Fatalf("first prompt code = %d, want CodeInternalError (%d)", reply.Error.Code, acp.CodeInternalError)
	}

	// Second prompt for the same session: the slot was freed, so it is ACCEPTED
	// (delivered) rather than rejected as in-flight — proven by it reaching the
	// deliverer and failing with the same internal error, not CodeInvalidRequest.
	h.send(promptFrame(t, 2, "sess-1", "second"))
	reply2, line2 := h.readReply()
	if reply2.Error == nil {
		t.Fatalf("second prompt: want error frame, got %q", line2)
	}
	if reply2.Error.Code == acp.CodeInvalidRequest {
		t.Fatalf("second prompt rejected as in-flight (CodeInvalidRequest); the slot was not freed")
	}
	if reply2.Error.Code != acp.CodeInternalError {
		t.Fatalf("second prompt code = %d, want CodeInternalError (%d)", reply2.Error.Code, acp.CodeInternalError)
	}
	if got := h.dlv.payloads(); len(got) != 2 {
		t.Fatalf("deliverer called %d times, want 2 (both prompts accepted)", len(got))
	}

	h.shutdown()
}

// TestACPPrompt_ResolvesWithMappedStopReason pins AC-1/AC-2 at the wire level:
// resolving the held call on TurnEnd yields a stopReason RESULT (never an error
// frame) carrying the reason mapped from the neutral turn-end reason, across all
// five reasons. cancelled is exercised explicitly (AC-2): a cancelled turn
// resolves as a result, not a JSON-RPC error. The reason strings here are the
// literal ACP stopReasons — identical to the neutral values by design — so this
// pins the value the host observes on the wire, not just the seam call.
func TestACPPrompt_ResolvesWithMappedStopReason(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			dlv := newRecordingDeliverer(false)
			h := newPromptHarness(t, dlv, "sess-1")

			h.send(promptFrame(t, 7, "sess-1", "hello"))
			h.waitEntered()

			h.endTurn("sess-1", reason)
			reply, line := h.readReply()
			if reply.Error != nil {
				t.Fatalf("reason %q: want stopReason result, got error frame %q", reason, line)
			}
			if reply.Result == nil || reply.Result.StopReason != reason {
				t.Fatalf("reason %q: result = %+v, want stopReason=%q", reason, reply.Result, reason)
			}

			h.shutdown()
		})
	}
}

// TestACPPrompt_ExactlyOneResolution pins AC-3: a duplicate TurnEnd (a second
// holds.end for the same session, after the held call already resolved) is a
// no-op — it writes no second frame and does not panic. Proven by driving a
// fresh synchronous reply afterwards and asserting IT, not a stray duplicate, is
// the next frame on the wire.
func TestACPPrompt_ExactlyOneResolution(t *testing.T) {
	t.Parallel()
	dlv := newRecordingDeliverer(false)
	h := newPromptHarness(t, dlv, "sess-1")

	h.send(promptFrame(t, 1, "sess-1", "hello"))
	h.waitEntered()

	// First resolution: the single reply for id 1.
	h.endTurn("sess-1", "end_turn")
	reply, line := h.readReply()
	if reply.Error != nil {
		t.Fatalf("first resolution: want stopReason result, got error frame %q", line)
	}
	if id := replyID(t, reply); id != 1 {
		t.Fatalf("first reply id = %d, want 1", id)
	}

	// Duplicate TurnEnd: the entry was deleted by the first end, so this finds no
	// Responder and writes nothing (and does not panic). Safe to call synchronously
	// because it never reaches Reply — no blocking write on the single-reader pipe.
	h.holds.end("sess-1", "end_turn")

	// A fresh synchronous reply (unknown session → error) must be the very next
	// frame; had the duplicate end written a frame, we would read that instead.
	h.send(promptFrame(t, 2, "does-not-exist", "hi"))
	reply2, line2 := h.readReply()
	if id := replyID(t, reply2); id != 2 {
		t.Fatalf("next frame id = %d, want 2 (no stray duplicate frame from the second TurnEnd): %q", id, line2)
	}
	if reply2.Error == nil {
		t.Fatalf("second prompt: want error reply, got %q", line2)
	}

	h.shutdown()
}

// TestPromptHolds_EndNoPendingIsNoop pins AC-4: end on a session with no
// registered hold returns without panic and, having no Responder to resolve,
// writes nothing. A TurnEnd for a never-prompted session is thus handled without
// error.
func TestPromptHolds_EndNoPendingIsNoop(t *testing.T) {
	t.Parallel()
	holds := newPromptHolds(testLogger(io.Discard))
	// No begin: the session has no held call. end must be a silent no-op — a panic
	// or write would fail the test.
	holds.end("absent-session", "end_turn")
}
