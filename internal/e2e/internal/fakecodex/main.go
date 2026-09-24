// Command fakecodex is a test-only stand-in for `codex app-server` at Codex
// 0.156.1, the version pyrycode targets. It speaks the app-server's wire —
// line-delimited JSON-RPC on stdin/stdout with no `jsonrpc` field — and
// answers the subset of the protocol pyrycode drives:
//
//	initialize        {codexHome, platformFamily, platformOs, userAgent}
//	initialized       notification, accepted silently
//	thread/start      a thread whose id the fake mints
//	thread/resume     the thread whose id was given (excludeTurns accepted)
//	turn/start        {turn}, then the turn streams turn/started, one
//	                  agent-message item (item/started,
//	                  item/agentMessage/delta, item/completed) and
//	                  turn/completed with status "completed"
//	turn/interrupt    {}, and the named running turn ends with
//	                  turn/completed status "interrupted"
//
// Any request other than initialize before initialize is refused, as the
// real server does. The fake exits 0 on stdin EOF. Field shapes follow the
// committed schema, internal/codexsup/codex_app_server_protocol.schemas.json.
//
// Configuration is env-only:
//
//	CODEX_HOME  echoed as initialize's codexHome. Default $HOME/.codex,
//	            the real server's default.
//
// Per-turn behaviour is selected by a marker anywhere in the text of a
// turn's input, so one process serves every kind of turn:
//
//	[fakecodex:approval]  before the agent message, the turn starts a
//	                      commandExecution item and sends one
//	                      item/commandExecution/requestApproval server
//	                      request. After the client's response it sends
//	                      serverRequest/resolved and completes the item:
//	                      "accept" or "acceptForSession" completes it as run
//	                      (status "completed", exitCode 0), any other
//	                      decision as "declined".
//	[fakecodex:hold]      after turn/started the turn waits until
//	                      turn/interrupt names it, so a test has a running
//	                      turn to interrupt.
//
// A turn without a marker never sends a server request.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	codexVersion   = "0.156.1"
	markerApproval = "[fakecodex:approval]"
	markerHold     = "[fakecodex:hold]"

	// fakeCommand is the command an approval turn asks to run.
	fakeCommand = "echo fakecodex"
)

// JSON-RPC error codes the fake answers with.
const (
	codeNotInitialized = -32002
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// handler answers one client request. A non-nil then runs after the
// response is written, so notifications it emits follow the response.
type handler func(s *server, params json.RawMessage) (result any, then func(), err *rpcError)

var requestHandlers = map[string]handler{
	"initialize":     (*server).initialize,
	"thread/start":   (*server).threadStart,
	"thread/resume":  (*server).threadResume,
	"turn/start":     (*server).turnStart,
	"turn/interrupt": (*server).turnInterrupt,
}

// acceptedNotifications are the client notifications the fake takes.
var acceptedNotifications = []string{"initialized"}

// emittedNotifications and serverRequests are every method the fake sends;
// the method-name test checks both against the schema.
var emittedNotifications = []string{
	"turn/started",
	"item/started",
	"item/agentMessage/delta",
	"item/completed",
	"serverRequest/resolved",
	"turn/completed",
}

var serverRequests = []string{"item/commandExecution/requestApproval"}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// message is one wire frame in either direction.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params any             `json:"params,omitempty"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// incoming is a frame read from stdin, params and result left raw.
type incoming struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

type server struct {
	codexHome   string
	initialized bool // main goroutine only

	writeMu sync.Mutex
	out     io.Writer
	werr    error

	mu       sync.Mutex
	turns    map[string]chan struct{}        // running turn id → closed on interrupt
	pending  map[string]chan json.RawMessage // server request id → the client's result
	requests int
}

func main() {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".codex")
		}
	}
	os.Exit(run(os.Stdin, os.Stdout, home))
}

// run serves frames from in until EOF, returning the process exit code.
func run(in io.Reader, out io.Writer, codexHome string) int {
	s := &server{
		codexHome: codexHome,
		out:       out,
		turns:     map[string]chan struct{}{},
		pending:   map[string]chan json.RawMessage{},
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var f incoming
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			fmt.Fprintf(os.Stderr, "fakecodex: skipping malformed frame: %v\n", err)
			continue
		}
		s.handle(f)
		if s.writeErr() != nil {
			fmt.Fprintf(os.Stderr, "fakecodex: write: %v\n", s.writeErr())
			return 1
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "fakecodex: read: %v\n", err)
		return 1
	}
	return 0
}

func (s *server) handle(f incoming) {
	switch {
	case len(f.ID) > 0 && f.Method != "":
		s.handleRequest(f)
	case f.Method != "":
		// Client notifications: initialized is the only one, and it needs
		// no reply. Unknown ones are ignored.
	case len(f.ID) > 0:
		s.mu.Lock()
		ch, ok := s.pending[string(f.ID)]
		delete(s.pending, string(f.ID))
		s.mu.Unlock()
		if ok {
			ch <- f.Result
		}
	}
}

func (s *server) handleRequest(f incoming) {
	h, ok := requestHandlers[f.Method]
	if !ok {
		s.send(message{ID: f.ID, Error: &rpcError{codeMethodNotFound, "method not found: " + f.Method}})
		return
	}
	if !s.initialized && f.Method != "initialize" {
		s.send(message{ID: f.ID, Error: &rpcError{codeNotInitialized, "Not initialized"}})
		return
	}
	result, then, rerr := h(s, f.Params)
	if rerr != nil {
		s.send(message{ID: f.ID, Error: rerr})
		return
	}
	s.send(message{ID: f.ID, Result: result})
	if then != nil {
		then()
	}
}

// send writes one frame; the first write error is kept for run to report.
func (s *server) send(m message) {
	line, err := json.Marshal(m)
	if err != nil {
		line, _ = json.Marshal(message{ID: m.ID, Error: &rpcError{-32603, err.Error()}})
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.werr == nil {
		_, s.werr = s.out.Write(append(line, '\n'))
	}
}

func (s *server) writeErr() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.werr
}

func (s *server) notify(method string, params any) {
	s.send(message{Method: method, Params: params})
}

func (s *server) initialize(json.RawMessage) (any, func(), *rpcError) {
	s.initialized = true
	platformOS := runtime.GOOS
	if platformOS == "darwin" {
		platformOS = "macos"
	}
	return map[string]any{
		"codexHome":      s.codexHome,
		"platformFamily": "unix",
		"platformOs":     platformOS,
		"userAgent":      "pyry_fakecodex/" + codexVersion,
	}, nil, nil
}

type threadParams struct {
	ThreadID       string `json:"threadId"`
	Cwd            string `json:"cwd"`
	ApprovalPolicy any    `json:"approvalPolicy"`
	ExcludeTurns   bool   `json:"excludeTurns"`
}

func (s *server) threadStart(raw json.RawMessage) (any, func(), *rpcError) {
	var p threadParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, nil, err
	}
	return threadResponse(newID(), p), nil, nil
}

func (s *server) threadResume(raw json.RawMessage) (any, func(), *rpcError) {
	var p threadParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, nil, err
	}
	if p.ThreadID == "" {
		return nil, nil, &rpcError{codeInvalidParams, "threadId is required"}
	}
	return threadResponse(p.ThreadID, p), nil, nil
}

// threadResponse is the ThreadStartResponse / ThreadResumeResponse shape,
// every required field filled.
func threadResponse(id string, p threadParams) map[string]any {
	cwd := p.Cwd
	if !filepath.IsAbs(cwd) {
		cwd, _ = os.Getwd()
	}
	var policy any = "on-request"
	if p.ApprovalPolicy != nil {
		policy = p.ApprovalPolicy
	}
	now := time.Now().Unix()
	return map[string]any{
		"approvalPolicy":    policy,
		"approvalsReviewer": "user",
		"cwd":               cwd,
		"model":             "fake-model",
		"modelProvider":     "openai",
		"sandbox":           map[string]any{"type": "readOnly"},
		"thread": map[string]any{
			"id":            id,
			"sessionId":     id,
			"cliVersion":    codexVersion,
			"createdAt":     now,
			"updatedAt":     now,
			"cwd":           cwd,
			"ephemeral":     false,
			"modelProvider": "openai",
			"preview":       "",
			"projectId":     nil,
			"source":        "appServer",
			"status":        map[string]any{"type": "idle"},
			"turns":         []any{},
		},
	}
}

type turnStartParams struct {
	ThreadID string `json:"threadId"`
	Input    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"input"`
}

func (s *server) turnStart(raw json.RawMessage) (any, func(), *rpcError) {
	var p turnStartParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, nil, err
	}
	var text strings.Builder
	for _, in := range p.Input {
		if in.Type == "text" {
			text.WriteString(in.Text)
		}
	}
	turnID := newID()
	interrupt := make(chan struct{})
	s.mu.Lock()
	s.turns[turnID] = interrupt
	s.mu.Unlock()
	t := &turn{s: s, threadID: p.ThreadID, id: turnID, interrupt: interrupt}
	return map[string]any{"turn": turnObject(turnID, "inProgress")},
		func() { go t.run(text.String()) }, nil
}

func (s *server) turnInterrupt(raw json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		TurnID string `json:"turnId"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	ch, ok := s.turns[p.TurnID]
	delete(s.turns, p.TurnID)
	s.mu.Unlock()
	if !ok {
		return nil, nil, &rpcError{codeInvalidParams, "no running turn " + p.TurnID}
	}
	return map[string]any{}, func() { close(ch) }, nil
}

type turn struct {
	s         *server
	threadID  string
	id        string
	interrupt chan struct{}
	items     int
}

// run streams the turn; it returns early, completing the turn as
// interrupted, when turn/interrupt closes t.interrupt.
func (t *turn) run(text string) {
	t.s.notify("turn/started", map[string]any{"threadId": t.threadID, "turn": turnObject(t.id, "inProgress")})
	if strings.Contains(text, markerHold) {
		<-t.interrupt
		t.complete("interrupted")
		return
	}
	if strings.Contains(text, markerApproval) && !t.approval() {
		t.complete("interrupted")
		return
	}
	msg := t.itemID("msg")
	reply := "fakecodex reply"
	t.s.notify("item/started", t.itemParams("startedAtMs", agentMessage(msg, "")))
	t.s.notify("item/agentMessage/delta", map[string]any{
		"threadId": t.threadID, "turnId": t.id, "itemId": msg, "delta": reply,
	})
	t.s.notify("item/completed", t.itemParams("completedAtMs", agentMessage(msg, reply)))
	t.s.mu.Lock()
	delete(t.s.turns, t.id)
	t.s.mu.Unlock()
	t.complete("completed")
}

// approval runs the command item and its approval round-trip. It reports
// false when the turn was interrupted while waiting for the decision.
func (t *turn) approval() bool {
	item := t.itemID("cmd")
	cwd, _ := os.Getwd()
	t.s.notify("item/started", t.itemParams("startedAtMs", commandItem(item, cwd, "inProgress", nil)))

	t.s.mu.Lock()
	t.s.requests++
	reqID := json.RawMessage(fmt.Sprintf(`"fakecodex-approval-%d"`, t.s.requests))
	decision := make(chan json.RawMessage, 1)
	t.s.pending[string(reqID)] = decision
	t.s.mu.Unlock()

	t.s.send(message{ID: reqID, Method: "item/commandExecution/requestApproval", Params: map[string]any{
		"threadId":    t.threadID,
		"turnId":      t.id,
		"itemId":      item,
		"startedAtMs": time.Now().UnixMilli(),
		"command":     fakeCommand,
		"cwd":         cwd,
	}})
	var result json.RawMessage
	select {
	case result = <-decision:
	case <-t.interrupt:
		t.s.mu.Lock()
		delete(t.s.pending, string(reqID))
		t.s.mu.Unlock()
		return false
	}
	t.s.notify("serverRequest/resolved", map[string]any{"threadId": t.threadID, "requestId": reqID})

	var r struct {
		Decision any `json:"decision"`
	}
	_ = json.Unmarshal(result, &r)
	done := commandItem(item, cwd, "declined", nil)
	if r.Decision == "accept" || r.Decision == "acceptForSession" {
		exit := 0
		done = commandItem(item, cwd, "completed", &exit)
		done["aggregatedOutput"] = "fakecodex\n"
	}
	t.s.notify("item/completed", t.itemParams("completedAtMs", done))
	return true
}

func (t *turn) complete(status string) {
	t.s.notify("turn/completed", map[string]any{"threadId": t.threadID, "turn": turnObject(t.id, status)})
}

func (t *turn) itemID(kind string) string {
	t.items++
	return fmt.Sprintf("%s-%s-%d", kind, t.id, t.items)
}

// itemParams is the ItemStartedNotification / ItemCompletedNotification
// shape; stamp names the timestamp field each requires.
func (t *turn) itemParams(stamp string, item map[string]any) map[string]any {
	return map[string]any{
		"threadId": t.threadID,
		"turnId":   t.id,
		"item":     item,
		stamp:      time.Now().UnixMilli(),
	}
}

func turnObject(id, status string) map[string]any {
	return map[string]any{"id": id, "items": []any{}, "status": status}
}

func agentMessage(id, text string) map[string]any {
	return map[string]any{"type": "agentMessage", "id": id, "text": text}
}

func commandItem(id, cwd, status string, exitCode *int) map[string]any {
	item := map[string]any{
		"type":           "commandExecution",
		"id":             id,
		"command":        fakeCommand,
		"commandActions": []any{},
		"cwd":            cwd,
		"status":         status,
	}
	if exitCode != nil {
		item["exitCode"] = *exitCode
	}
	return item
}

func decodeParams(raw json.RawMessage, into any) *rpcError {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return &rpcError{codeInvalidParams, err.Error()}
	}
	return nil
}

// newID mints a random UUID-formatted id.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
