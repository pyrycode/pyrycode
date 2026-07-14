package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/acpbridge"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// The proof-of-correctness capstone of epic #600 (ADR 027). One scripted ACP
// host drives the whole `pyry acp` surface — initialize → session/new →
// session/prompt + a scripted turn → a permission round-trip → session/cancel —
// over the real internal/acp transport, and asserts the emitted wire shape is the
// generic Zed/spec dialect, observing all six ADR 027 divergences in one session
// flow. Every surface it exercises has already landed and is unit-tested
// per-ticket; the capstone adds the *integration* (the divergences compose in one
// session over one transport) and a *dialect lock* (TestACPConformance_DialectLock)
// the per-ticket tests cannot provide.

// --- conformance harness ----------------------------------------------------

// conformanceHarness assembles the composed ACP transport plus a concurrent frame
// classifier — the scripted host. It mirrors serveACPWithPool's register-closure
// with the same handler constructors (a compile-time-checked mirror: a constructor
// signature change breaks this test), with two substitutions that let a scripted
// turn and a scripted permission modal drive the surface behind a sleeping fake
// claude that writes no JSONL and renders no PTY modal:
//
//   - the streams manager is built with dir == "" so newSessionHandler's internal
//     streams.start(id) is a no-op (the real outbound path is disabled), leaving
//     the scripted outbound (attachScriptedOutbound) the only producer/proxy; and
//   - session/prompt resolves to a newRecordingDeliverer(false), not the pool
//     session — so WriteUserTurn commits and the hold stays held until the scripted
//     TurnEnd resolves it (a real fake-claude session would fail delivery and
//     resolve the hold with an error first, breaking divergence 1).
//
// The real fake-claude pool still backs session/new / session/cancel, so the
// interactive-spawn argv proof (divergence 6 + cost) is genuine and one real
// session id ties the whole flow together.
type conformanceHarness struct {
	t        *testing.T
	tr       *acp.Transport
	holds    *promptHolds
	dlv      *recordingDeliverer
	logger   *slog.Logger
	argvFile string

	// In-memory transport pipes: the host writes requests to hostToAgentW; the
	// agent writes replies/notifications/outbound-requests to agentToHostW, drained
	// by the concurrent classifier reading agentToHostR.
	hostToAgentW *io.PipeWriter
	agentToHostW *io.PipeWriter
	agentToHostR *io.PipeReader

	runCtx    context.Context
	runCancel context.CancelFunc

	served     chan error    // tr.Serve goroutine
	poolErr    chan error    // pool.Run goroutine
	readerDone chan struct{} // classifier reader goroutine
	replies    chan []byte   // raw reply frames the classifier routes to the test

	// Scripted outbound, bound after session/new by attachScriptedOutbound.
	sink           *acpTurnStream
	kb             *syncKeystroker
	turnCh         chan tuidriver.Event
	permCh         chan tuidriver.Event
	producerCancel context.CancelFunc
	producerDone   chan struct{}
	drainDone      chan struct{}

	// Classifier shared state, guarded by mu (a leaf lock touched by the reader
	// goroutine and the test goroutine).
	mu             sync.Mutex
	notifications  [][]byte        // every session/update notification, in wire order
	permMethod     string          // the captured agent→client request method
	permParams     json.RawMessage // the captured session/request_permission params
	answerOptionID string          // the optionId the classifier auto-answers with
}

// conformanceFrame is the classify-by-shape view of one agent→host line, mirroring
// acp.handleLine's classification (method+id → request, method only → notification,
// id+result/error → reply).
type conformanceFrame struct {
	Method *string         `json:"method"`
	ID     json.RawMessage `json:"id"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// conformanceReply decodes a reply frame the classifier routes to the test.
type conformanceReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// conformanceNotif decodes a session/update notification for its addressed session
// and its variant discriminant.
type conformanceNotif struct {
	Method string `json:"method"`
	Params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string `json:"sessionUpdate"`
		} `json:"update"`
	} `json:"params"`
}

func newConformanceHarness(t *testing.T) *conformanceHarness {
	t.Helper()
	pool, argvFile, logger, _ := newFakeClaudePool(t)

	runCtx, runCancel := context.WithCancel(context.Background())

	poolErr := make(chan error, 1)
	go func() { poolErr <- pool.Run(runCtx) }()
	select {
	case <-pool.Ready():
	case err := <-poolErr:
		runCancel()
		t.Fatalf("pool.Run returned before ready: %v", err)
	case <-time.After(5 * time.Second):
		runCancel()
		t.Fatal("pool never became ready")
	}

	holds := newPromptHolds(logger)
	dlv := newRecordingDeliverer(false) // non-gated: delivery commits, the hold stays held
	// dir == "" disables the real outbound: newSessionHandler's streams.start(id)
	// no-ops, so the scripted outbound attached below is the only producer/proxy.
	streams := newACPTurnStreams(runCtx, pool, "", holds.end, logger)

	hostToAgentR, hostToAgentW := io.Pipe()
	agentToHostR, agentToHostW := io.Pipe()

	tr := acp.New(hostToAgentR, agentToHostW, logger)
	// Mirror serveACPWithPool's register closure (acp.go) with the same handler
	// constructors — the compile-time-checked mirror the harness doc describes.
	streams.attach(tr)
	registerHandshake(tr)
	tr.Register("session/new", newSessionHandler(pool, streams))
	tr.Register("session/load", loadSessionHandler(pool, streams))
	tr.Register("session/prompt", promptHandler(holds, func(sessions.SessionID) (promptDeliverer, error) {
		// Swap the real fake-claude session for a recording deliverer: a sleeping
		// shell script would fail WriteUserTurn and resolve the hold with an error
		// before the scripted TurnEnd could (breaking divergence 1).
		return dlv, nil
	}, logger))
	tr.Register("session/cancel", cancelSessionHandler(func(p json.RawMessage) (interrupter, error) {
		sup, err := resolveCancelTarget(pool, p)
		if err != nil {
			return nil, err
		}
		return sup, nil
	}))
	tr.Register("session/set_mode", setModeHandler)
	tr.Register("session/set_config_option", setConfigOptionHandler)

	h := &conformanceHarness{
		t:            t,
		tr:           tr,
		holds:        holds,
		dlv:          dlv,
		logger:       logger,
		argvFile:     argvFile,
		hostToAgentW: hostToAgentW,
		agentToHostW: agentToHostW,
		agentToHostR: agentToHostR,
		runCtx:       runCtx,
		runCancel:    runCancel,
		served:       make(chan error, 1),
		poolErr:      poolErr,
		readerDone:   make(chan struct{}),
		replies:      make(chan []byte, 8),
	}

	go func() { h.served <- tr.Serve(runCtx) }()
	go h.classify()

	// Safety net for a mid-test t.Fatal (shutdown never reached): cancel every
	// goroutine's ctx and close both pipe writers so nothing leaks. All three calls
	// are idempotent, so the normal ordered shutdown() may also run them.
	t.Cleanup(func() {
		h.runCancel()
		_ = h.hostToAgentW.Close()
		_ = h.agentToHostW.Close()
	})

	return h
}

// classify is the concurrent frame classifier — the scripted ACP host's read
// loop. An unbuffered io.Pipe blocks each agent write until read, so this
// continuous drain is mandatory: the producer's Notify and the proxy's Call write
// to agentToHostW asynchronously and would otherwise deadlock a send-all-then-read
// test.
func (h *conformanceHarness) classify() {
	defer close(h.readerDone)
	reader := bufio.NewReader(h.agentToHostR)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			h.dispatchFrame(line)
		}
		if err != nil {
			return // EOF on agentToHostW close (shutdown), or ErrClosedPipe (cleanup)
		}
	}
}

// dispatchFrame classifies one agent→host line and routes it: an inbound
// session/request_permission request is auto-answered; a session/update
// notification is recorded; a reply is handed to the test.
func (h *conformanceHarness) dispatchFrame(line []byte) {
	var f conformanceFrame
	if err := json.Unmarshal(line, &f); err != nil {
		return // not a JSON-RPC frame; ignore (the transport never emits one)
	}
	switch {
	case f.Method != nil && f.ID != nil:
		if *f.Method == methodSessionRequestPermission {
			h.answerPermission(f)
		}
	case f.Method != nil:
		if *f.Method == acpbridge.MethodSessionUpdate {
			h.mu.Lock()
			h.notifications = append(h.notifications, append([]byte(nil), line...))
			h.mu.Unlock()
		}
	case f.ID != nil && (f.Result != nil || f.Error != nil):
		h.replies <- append([]byte(nil), line...)
	}
}

// answerPermission captures the inbound session/request_permission request and
// auto-answers it with a selected outcome for answerOptionID, echoing the request
// id so the transport's routeResponse delivers it to the blocked proxy Call.
func (h *conformanceHarness) answerPermission(f conformanceFrame) {
	h.mu.Lock()
	h.permMethod = *f.Method
	h.permParams = append(json.RawMessage(nil), f.Params...)
	ans := h.answerOptionID
	h.mu.Unlock()

	resp := struct {
		Jsonrpc string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}{Jsonrpc: "2.0", ID: f.ID, Result: selectedResp(ans)}
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	// The Serve loop always reads hostToAgentR, so this write cannot block; the
	// test never calls send() concurrently with a permission round-trip, so there
	// is no contention on hostToAgentW (io.Pipe serialises writers regardless).
	_, _ = h.hostToAgentW.Write(append(b, '\n'))
}

// send writes one host→agent request frame (a trailing newline is appended).
func (h *conformanceHarness) send(frame string) {
	h.t.Helper()
	if _, err := io.WriteString(h.hostToAgentW, frame+"\n"); err != nil {
		h.t.Fatalf("write frame: %v", err)
	}
}

// readReply blocks for the next reply frame the classifier routes and decodes it.
func (h *conformanceHarness) readReply() conformanceReply {
	h.t.Helper()
	select {
	case line := <-h.replies:
		var r conformanceReply
		if err := json.Unmarshal(line, &r); err != nil {
			h.t.Fatalf("decode reply %q: %v", line, err)
		}
		return r
	case <-time.After(10 * time.Second):
		h.t.Fatal("timed out waiting for a reply frame")
		return conformanceReply{}
	}
}

// setAnswerOptionID sets the optionId the classifier auto-answers with. Guarded by
// mu so the classifier reads it race-free.
func (h *conformanceHarness) setAnswerOptionID(id string) {
	h.mu.Lock()
	h.answerOptionID = id
	h.mu.Unlock()
}

// capturedPermission returns the captured agent→client request method and the
// decoded session/request_permission params. Call after kb.waitRouted returns, so
// the capture (which happens-before the auto-answer, which happens-before the
// keystroke) is visible.
func (h *conformanceHarness) capturedPermission() (string, requestPermissionParams) {
	h.t.Helper()
	h.mu.Lock()
	method := h.permMethod
	raw := append(json.RawMessage(nil), h.permParams...)
	h.mu.Unlock()
	var p requestPermissionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		h.t.Fatalf("decode request_permission params %q: %v", raw, err)
	}
	return method, p
}

// snapshotNotifications returns a copy of every session/update notification
// recorded so far, in wire order.
func (h *conformanceHarness) snapshotNotifications() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.notifications)
}

// waitDelivered blocks until the prompt delivery goroutine has entered
// WriteUserTurn, which happens after holds.begin registers the hold — so the hold
// is guaranteed registered before a scripted TurnEnd tries to resolve it.
func (h *conformanceHarness) waitDelivered() {
	h.t.Helper()
	select {
	case <-h.dlv.entered:
	case <-time.After(5 * time.Second):
		h.t.Fatal("prompt delivery goroutine did not enter WriteUserTurn (hold not registered)")
	}
}

// attachScriptedOutbound binds the scripted outbound for id after session/new: a
// turn producer (scriptedSubscriber + the acpTurnStream sink) and a permission
// drain (scriptedSubscriber + the acpPermissionProxy, caller = the real transport).
// The producer rides its own cancel so phase D can join it before phase E feeds the
// sink directly.
func (h *conformanceHarness) attachScriptedOutbound(id string) {
	h.t.Helper()
	h.turnCh = make(chan tuidriver.Event)
	h.permCh = make(chan tuidriver.Event)
	h.kb = newSyncKeystroker()

	// Turn producer: onTurnEnd resolves the held session/prompt call via holds.end,
	// exactly the join #751 wires at the composition root.
	h.sink = newACPTurnStream(h.tr, id, func(r string) { h.holds.end(id, r) }, h.logger)
	turnSub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{h.turnCh}}
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: turnSub.subscribe,
		OnEvent:   h.sink.Handle,
		Logger:    h.logger,
	})
	if err != nil {
		h.t.Fatalf("turnbridge.New: %v", err)
	}
	producerCtx, producerCancel := context.WithCancel(h.runCtx)
	h.producerCancel = producerCancel
	h.producerDone = make(chan struct{})
	go func() { _ = prod.Run(producerCtx); close(h.producerDone) }()

	// Permission drain: the real transport is the permissionCaller, so the proxy's
	// Call goes over the wire to the classifier (which auto-answers).
	proxy := newACPPermissionProxy(h.tr, h.kb, id, generousTimeout, h.logger)
	permSub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{h.permCh}}
	h.drainDone = make(chan struct{})
	go func() { runPermissionModalStream(h.runCtx, permSub.subscribe, proxy); close(h.drainDone) }()
}

// stopProducer cancels and joins the turn producer. Idempotent — phase D stops it,
// and shutdown calls it again as a no-op.
func (h *conformanceHarness) stopProducer() {
	if h.producerCancel == nil {
		return
	}
	h.producerCancel()
	h.producerCancel = nil
	waitClosed(h.t, h.producerDone, "turn producer join")
}

// shutdown tears the harness down in order: stop the producer, EOF host stdin so
// Serve returns clean, cancel runCtx to stop the drain + pool, then close the
// agent writer so the classifier reader drains and joins — every goroutine joined
// before the test returns, so post-shutdown reads of shared state are race-free.
func (h *conformanceHarness) shutdown() {
	h.t.Helper()
	h.stopProducer()

	if err := h.hostToAgentW.Close(); err != nil {
		h.t.Fatalf("close host stdin: %v", err)
	}
	select {
	case err := <-h.served:
		if err != nil {
			h.t.Fatalf("Serve: want nil on host EOF, got %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Fatal("Serve did not return after host EOF")
	}

	h.runCancel()
	if h.drainDone != nil {
		waitClosed(h.t, h.drainDone, "permission drain join")
	}
	select {
	case <-h.poolErr:
	case <-time.After(10 * time.Second):
		h.t.Fatal("pool.Run did not return after cancel")
	}

	// Every writer to agentToHostW is joined; closing it now EOFs the reader.
	_ = h.agentToHostW.Close()
	waitClosed(h.t, h.readerDone, "classifier reader join")
}

// --- Test 1: full-session drive ---------------------------------------------

// TestACPConformance_FullSessionDrive drives one scripted host through one real
// session id, five phases, over the internal/acp transport against a deterministic
// fake claude — no live Zed, no network — and asserts the emitted wire shape is the
// generic ADR 027 dialect. It observes all six divergences composing in one flow:
// end-of-turn as the session/prompt return (1), permission as a blocking agent→client
// request (2), no stall/busy/queue frame on the wire (3, 4), no fs/* or terminal/*
// in initialize (5), one interactive claude per session (6).
func TestACPConformance_FullSessionDrive(t *testing.T) {
	t.Parallel()
	h := newConformanceHarness(t)

	// Phase A — handshake (divergence 5). The host offers fs+terminal; pyry's
	// initialize result requests no host filesystem or terminal capability.
	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true}}}`)
	initReply := h.readReply()
	if initReply.Error != nil {
		t.Fatalf("initialize: unexpected error %+v", *initReply.Error)
	}
	var initRes initializeResult
	if err := json.Unmarshal(initReply.Result, &initRes); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if initRes.ProtocolVersion != SupportedProtocolVersion {
		t.Errorf("protocolVersion = %d, want %d", initRes.ProtocolVersion, SupportedProtocolVersion)
	}
	for _, sub := range []string{`"fs"`, `"terminal"`, `"readTextFile"`, `"writeTextFile"`} {
		if bytes.Contains(initReply.Result, []byte(sub)) {
			t.Errorf("initialize result requests host capability %s (divergence 5 violation): %s", sub, initReply.Result)
		}
	}

	// Phase B — session (divergence 6 + cost invariant). session/new mints one id
	// and spawns exactly one interactive claude (--session-id <uuid>, no -p/--print).
	h.send(`{"jsonrpc":"2.0","id":2,"method":"session/new"}`)
	newReply := h.readReply()
	if newReply.Error != nil {
		t.Fatalf("session/new: unexpected error %+v", *newReply.Error)
	}
	var newRes newSessionResult
	if err := json.Unmarshal(newReply.Result, &newRes); err != nil {
		t.Fatalf("decode session/new result: %v", err)
	}
	id := newRes.SessionID
	if !sessions.ValidID(id) {
		t.Fatalf("session/new sessionId = %q, want a valid UUID", id)
	}
	assertPinnedModes(t, newRes.Modes)
	// The interactive-path / one-claude proof, reused verbatim (the #943 --settings
	// pair stripped so this keeps asserting the --session-id shape).
	if got := stripMCPSettingsPair(t, waitOneClaudeArgv(t, h.argvFile)); !slices.Equal(got, []string{"--session-id", id}) {
		t.Fatalf("claude argv = %v, want [--session-id %s] (interactive path, one per session)", got, id)
	}

	h.attachScriptedOutbound(id)

	// Phase C — permission round-trip (divergence 2). The host selects reject_once,
	// the 3rd option, so answer:3 proves index→digit is real (not "always 1").
	h.setAnswerOptionID("reject_once")
	h.permCh <- permissionShown()
	if got := h.kb.waitRouted(t); got != "answer:3" {
		t.Fatalf("permission routed %q, want answer:3 (reject_once is the 3rd option)", got)
	}
	method, params := h.capturedPermission()
	if method != methodSessionRequestPermission {
		t.Errorf("agent→client method = %q, want %q", method, methodSessionRequestPermission)
	}
	if params.SessionID != id {
		t.Errorf("request_permission sessionId = %q, want %q", params.SessionID, id)
	}
	wantOpts := []permissionOption{
		{OptionID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		{OptionID: "allow_always", Name: "Allow always", Kind: "allow_always"},
		{OptionID: "reject_once", Name: "Reject once", Kind: "reject_once"},
		{OptionID: "reject_always", Name: "Reject always", Kind: "reject_always"},
	}
	if !slices.Equal(params.Options, wantOpts) {
		t.Errorf("request_permission options = %+v, want %+v", params.Options, wantOpts)
	}
	for _, o := range params.Options {
		if !turnevent.PermissionOptionKind(o.Kind).Valid() {
			t.Errorf("option kind %q is not a valid ACP permission kind", o.Kind)
		}
	}

	// Phase D — turn + prompt return (divergences 1, 3; the generic dialect). A
	// held session/prompt, a scripted turn (thought, message, tool_call, a dropped
	// stall), then end-of-turn resolves the held call with stopReason end_turn.
	h.send(promptFrame(t, 3, id, "drive the turn"))
	h.waitDelivered() // the hold is registered before any turn event is fed
	for _, ev := range []tuidriver.Event{
		jsonlStreamEvent(streamEntry(t, "assistant", "m1", map[string]any{"type": "thinking", "thinking": "reasoning"})),
		jsonlStreamEvent(streamEntry(t, "assistant", "m2", map[string]any{"type": "text", "text": "hello"})),
		jsonlStreamEvent(streamEntry(t, "assistant", "m3", map[string]any{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{"file_path": "/tmp/x"}})),
		{Kind: tuidriver.EventKindStallDetected}, // divergence 3: dropped, never on the wire
		endOfTurnEvent(),
	} {
		h.turnCh <- ev
	}
	close(h.turnCh)

	turnReply := h.readReply()
	if turnReply.Error != nil {
		t.Fatalf("session/prompt turn: want stopReason result, got error %+v", *turnReply.Error)
	}
	if got := replyIntID(t, turnReply.ID); got != 3 {
		t.Errorf("turn reply id = %d, want 3 (the held request id)", got)
	}
	var turnRes promptResult
	if err := json.Unmarshal(turnReply.Result, &turnRes); err != nil {
		t.Fatalf("decode turn result: %v", err)
	}
	if turnRes.StopReason != "end_turn" {
		t.Errorf("stopReason = %q, want end_turn (divergence 1)", turnRes.StopReason)
	}

	h.stopProducer()

	// Reading the return above blocks until the reply frame is drained, which is
	// strictly after the three notification frames on the wire (the producer's
	// single Run goroutine emits notifications then onTurnEnd → holds.end). So the
	// notification set is complete here and must be exactly three — the generic
	// discriminants, no stall/busy/queue frame (divergences 1 and 3, non-vacuous).
	notifs := h.snapshotNotifications()
	wantDisc := []string{
		acpbridge.SessionUpdateAgentThoughtChunk,
		acpbridge.SessionUpdateAgentMessageChunk,
		acpbridge.SessionUpdateToolCall,
	}
	if len(notifs) != len(wantDisc) {
		t.Fatalf("emitted %d session/update notifications, want %d (no stall/busy/queue frame — divergence 3)", len(notifs), len(wantDisc))
	}
	for i, raw := range notifs {
		var n conformanceNotif
		if err := json.Unmarshal(raw, &n); err != nil {
			t.Fatalf("decode notification %d %q: %v", i, raw, err)
		}
		if n.Params.SessionID != id {
			t.Errorf("notification %d sessionId = %q, want %q", i, n.Params.SessionID, id)
		}
		if n.Params.Update.SessionUpdate != wantDisc[i] {
			t.Errorf("notification %d sessionUpdate = %q, want %q", i, n.Params.Update.SessionUpdate, wantDisc[i])
		}
	}

	// Phase E — cancel + cancelled return (divergence 1, cancelled). A fresh held
	// prompt, a session/cancel notification (no reply, actuates SendEsc best-effort
	// on the real supervisor), then — the producer already joined — the sink is fed
	// TurnEnd{cancelled} directly: the only way to produce a cancelled stopReason,
	// since the scripted JSONL path forces end_turn (turnbridge mapper).
	h.send(promptFrame(t, 4, id, "then cancel"))
	h.waitDelivered()
	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":%q}}`, id))
	h.sink.Handle(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled})

	cancelReply := h.readReply()
	if cancelReply.Error != nil {
		t.Fatalf("session/prompt cancel: want stopReason result, got error %+v", *cancelReply.Error)
	}
	if got := replyIntID(t, cancelReply.ID); got != 4 {
		t.Errorf("cancel reply id = %d, want 4 (the held request id)", got)
	}
	var cancelRes promptResult
	if err := json.Unmarshal(cancelReply.Result, &cancelRes); err != nil {
		t.Fatalf("decode cancel result: %v", err)
	}
	if cancelRes.StopReason != "cancelled" {
		t.Errorf("stopReason = %q, want cancelled", cancelRes.StopReason)
	}

	h.shutdown()
}

// replyIntID decodes a numeric JSON-RPC reply id.
func replyIntID(t *testing.T, id json.RawMessage) int {
	t.Helper()
	var n int
	if err := json.Unmarshal(id, &n); err != nil {
		t.Fatalf("decode reply id %q: %v", id, err)
	}
	return n
}

// --- Test 2: dialect lock ---------------------------------------------------

// TestACPConformance_DialectLock is the generic-vs-opencode-alias lock the
// per-ticket tests cannot be: they compare emitted frames against these same
// constants, so renaming a constant's VALUE to an opencode alias would not fail
// them. Here the wants are literal ADR 027 strings, so any drift of a constant's
// value fails HERE. Pure — no goroutines, no harness.
func TestACPConformance_DialectLock(t *testing.T) {
	t.Parallel()

	// session/update method + the four generic variant discriminants (ADR 027 §
	// "ACP taxonomy reference"): agent_message_chunk / agent_thought_chunk /
	// tool_call / tool_call_update — not opencode aliases.
	if acpbridge.MethodSessionUpdate != "session/update" {
		t.Errorf("MethodSessionUpdate = %q, want session/update", acpbridge.MethodSessionUpdate)
	}
	for _, d := range []struct{ got, want string }{
		{acpbridge.SessionUpdateAgentMessageChunk, "agent_message_chunk"},
		{acpbridge.SessionUpdateAgentThoughtChunk, "agent_thought_chunk"},
		{acpbridge.SessionUpdateToolCall, "tool_call"},
		{acpbridge.SessionUpdateToolCallUpdate, "tool_call_update"},
	} {
		if d.got != d.want {
			t.Errorf("session/update discriminant = %q, want %q", d.got, d.want)
		}
	}

	// stopReason values (== turnevent.TurnEndReason).
	for _, r := range []struct {
		got  turnevent.TurnEndReason
		want string
	}{
		{turnevent.TurnEndReasonEndTurn, "end_turn"},
		{turnevent.TurnEndReasonMaxTokens, "max_tokens"},
		{turnevent.TurnEndReasonMaxTurnRequests, "max_turn_requests"},
		{turnevent.TurnEndReasonRefusal, "refusal"},
		{turnevent.TurnEndReasonCancelled, "cancelled"},
	} {
		if string(r.got) != r.want {
			t.Errorf("stopReason = %q, want %q", r.got, r.want)
		}
	}

	// permission-option kinds (== turnevent.PermissionOptionKind), each Valid().
	for _, k := range []struct {
		got  turnevent.PermissionOptionKind
		want string
	}{
		{turnevent.PermissionOptionKindAllowOnce, "allow_once"},
		{turnevent.PermissionOptionKindAllowAlways, "allow_always"},
		{turnevent.PermissionOptionKindRejectOnce, "reject_once"},
		{turnevent.PermissionOptionKindRejectAlways, "reject_always"},
	} {
		if string(k.got) != k.want {
			t.Errorf("permission-option kind = %q, want %q", k.got, k.want)
		}
		if !k.got.Valid() {
			t.Errorf("permission-option kind %q is not Valid()", k.got)
		}
	}

	// The one agent→client method pyry issues is session/request_permission — the
	// only one it needs (divergence 5: never fs/* or terminal/*).
	if methodSessionRequestPermission != "session/request_permission" {
		t.Errorf("methodSessionRequestPermission = %q, want session/request_permission", methodSessionRequestPermission)
	}

	// The eight client→agent method names pyry registers are the ADR 027 spec names
	// (initialize, authenticate, session/new, session/load, session/prompt,
	// session/cancel, session/set_mode, session/set_config_option). They have no
	// named constants to lock here — they are registered as literals and driven
	// verbatim over the wire by TestACPConformance_FullSessionDrive (initialize,
	// session/new, session/prompt, session/cancel) and the per-ticket handshake /
	// session tests (authenticate, session/load, session/set_mode,
	// session/set_config_option), which are their drift guard.
}
