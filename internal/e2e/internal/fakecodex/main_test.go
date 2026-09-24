package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// schemaPath is the committed 0.156.1 bundle, relative to this package.
const schemaPath = "../../../codexsup/codex_app_server_protocol.schemas.json"

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakecodex-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeBin = filepath.Join(dir, "fakecodex")
	out, err := exec.Command("go", "build", "-o", fakeBin,
		"github.com/pyrycode/pyrycode/internal/e2e/internal/fakecodex").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "go build fakecodex: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// schemaMethods maps each of the four top-level message definitions to the
// method names its oneOf arms allow.
func schemaMethods(t *testing.T) map[string]map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var bundle struct {
		Definitions map[string]struct {
			OneOf []struct {
				Properties struct {
					Method struct {
						Enum []string `json:"enum"`
					} `json:"method"`
				} `json:"properties"`
			} `json:"oneOf"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	groups := map[string]map[string]bool{}
	for _, name := range []string{"ClientRequest", "ClientNotification", "ServerRequest", "ServerNotification"} {
		def, ok := bundle.Definitions[name]
		if !ok || len(def.OneOf) == 0 {
			t.Fatalf("schema has no %s definition", name)
		}
		groups[name] = map[string]bool{}
		for _, arm := range def.OneOf {
			for _, m := range arm.Properties.Method.Enum {
				groups[name][m] = true
			}
		}
	}
	return groups
}

func TestMethodNamesInSchema(t *testing.T) {
	groups := schemaMethods(t)
	check := func(group string, methods []string) {
		t.Helper()
		for _, m := range methods {
			if !groups[group][m] {
				t.Errorf("method %q is not in the schema's %s definition", m, group)
			}
		}
	}
	var answered []string
	for m := range requestHandlers {
		answered = append(answered, m)
	}
	check("ClientRequest", answered)
	check("ClientNotification", acceptedNotifications)
	check("ServerNotification", emittedNotifications)
	check("ServerRequest", serverRequests)
}

// frame is one decoded JSON-RPC line; raw fields stay undecoded until a test
// asks for them.
type frame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type fakeProc struct {
	t       *testing.T
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	frames  chan frame
	groups  map[string]map[string]bool
	nextID  int
	home    string
	stopped bool
}

func startFake(t *testing.T) *fakeProc {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(fakeBin)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fakecodex: %v", err)
	}
	p := &fakeProc{t: t, cmd: cmd, stdin: stdin, frames: make(chan frame, 64), groups: schemaMethods(t), home: home}
	go func() {
		defer close(p.frames)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			var f frame
			if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
				t.Errorf("fake wrote a non-JSON line %q: %v", sc.Text(), err)
				continue
			}
			if strings.Contains(sc.Text(), `"jsonrpc"`) {
				t.Errorf("fake wrote a jsonrpc field: %s", sc.Text())
			}
			p.frames <- f
		}
	}()
	t.Cleanup(func() {
		if !p.stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return p
}

// write sends one raw frame, checking any method it carries against the
// schema group a client frame of that shape belongs to.
func (p *fakeProc) write(v map[string]any) {
	p.t.Helper()
	if m, ok := v["method"].(string); ok {
		group := "ClientNotification"
		if _, hasID := v["id"]; hasID {
			group = "ClientRequest"
		}
		if !p.groups[group][m] {
			p.t.Errorf("test sent %q, absent from the schema's %s", m, group)
		}
	}
	line, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		p.t.Fatalf("write to fake: %v", err)
	}
}

// read returns the fake's next frame, checking any method against the
// server-side schema group its shape belongs to.
func (p *fakeProc) read() frame {
	p.t.Helper()
	select {
	case f, ok := <-p.frames:
		if !ok {
			p.t.Fatal("fake closed stdout")
		}
		if f.Method != "" {
			group := "ServerNotification"
			if len(f.ID) > 0 {
				group = "ServerRequest"
			}
			if !p.groups[group][f.Method] {
				p.t.Errorf("fake sent %q, absent from the schema's %s", f.Method, group)
			}
		}
		return f
	case <-time.After(5 * time.Second):
		p.t.Fatal("timed out waiting for a frame from the fake")
		return frame{}
	}
}

// call sends a request and returns its response frame; the fake must answer
// before it emits anything else.
func (p *fakeProc) call(method string, params any) frame {
	p.t.Helper()
	p.nextID++
	id := p.nextID
	p.write(map[string]any{"id": id, "method": method, "params": params})
	f := p.read()
	if f.Method != "" || string(f.ID) != fmt.Sprint(id) {
		p.t.Fatalf("%s: want the response to id %d, got %+v", method, id, f)
	}
	return f
}

func (p *fakeProc) result(method string, params any, into any) {
	p.t.Helper()
	f := p.call(method, params)
	if f.Error != nil {
		p.t.Fatalf("%s: error %d %s", method, f.Error.Code, f.Error.Message)
	}
	if err := json.Unmarshal(f.Result, into); err != nil {
		p.t.Fatalf("%s result %s: %v", method, f.Result, err)
	}
}

func (p *fakeProc) initialize() {
	p.t.Helper()
	var r map[string]any
	p.result("initialize", map[string]any{"clientInfo": map[string]any{"name": "pyry-test", "version": "0"}}, &r)
	p.write(map[string]any{"method": "initialized"})
}

func (p *fakeProc) startThread() string {
	p.t.Helper()
	var r struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	p.result("thread/start", map[string]any{"approvalPolicy": "on-request", "sandbox": "read-only"}, &r)
	if r.Thread.ID == "" {
		p.t.Fatal("thread/start minted no thread id")
	}
	return r.Thread.ID
}

func (p *fakeProc) startTurn(threadID, text string) string {
	p.t.Helper()
	var r struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	p.result("turn/start", map[string]any{
		"threadId": threadID,
		"input":    []any{map[string]any{"type": "text", "text": text}},
	}, &r)
	if r.Turn.ID == "" || r.Turn.Status != "inProgress" {
		p.t.Fatalf("turn/start result: %+v", r.Turn)
	}
	return r.Turn.ID
}

// expect reads the next frame and requires it to carry method.
func (p *fakeProc) expect(method string) frame {
	p.t.Helper()
	f := p.read()
	if f.Method != method {
		p.t.Fatalf("want %s, got %+v", method, f)
	}
	return f
}

// stop closes stdin and requires the fake to exit 0.
func (p *fakeProc) stop() {
	p.t.Helper()
	p.stopped = true
	_ = p.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			p.t.Fatalf("fake exit on stdin EOF: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		p.t.Fatal("fake did not exit on stdin EOF")
	}
}

type turnParams struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
}

type itemParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Item     struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Text     string `json:"text"`
		Status   string `json:"status"`
		ExitCode *int   `json:"exitCode"`
	} `json:"item"`
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// expectAgentMessage reads the item/started, delta, item/completed triple for
// one agent message in turnID.
func (p *fakeProc) expectAgentMessage(turnID string) {
	p.t.Helper()
	started := decode[itemParams](p.t, p.expect("item/started").Params)
	if started.Item.Type != "agentMessage" || started.TurnID != turnID || started.Item.ID == "" {
		p.t.Fatalf("item/started: %+v", started)
	}
	delta := decode[struct {
		ItemID string `json:"itemId"`
		TurnID string `json:"turnId"`
		Delta  string `json:"delta"`
	}](p.t, p.expect("item/agentMessage/delta").Params)
	if delta.ItemID != started.Item.ID || delta.TurnID != turnID || delta.Delta == "" {
		p.t.Fatalf("delta: %+v", delta)
	}
	done := decode[itemParams](p.t, p.expect("item/completed").Params)
	if done.Item.ID != started.Item.ID || done.Item.Text != delta.Delta {
		p.t.Fatalf("item/completed: %+v", done)
	}
}

func (p *fakeProc) expectTurnCompleted(turnID, status string) {
	p.t.Helper()
	c := decode[turnParams](p.t, p.expect("turn/completed").Params)
	if c.Turn.ID != turnID || c.Turn.Status != status {
		p.t.Fatalf("turn/completed: want %s %s, got %+v", turnID, status, c)
	}
}

func TestInitialize(t *testing.T) {
	p := startFake(t)
	var r struct {
		CodexHome string `json:"codexHome"`
		UserAgent string `json:"userAgent"`
	}
	p.result("initialize", map[string]any{"clientInfo": map[string]any{"name": "pyry-test", "version": "0"}}, &r)
	if r.CodexHome != p.home {
		t.Errorf("codexHome = %q, want CODEX_HOME %q", r.CodexHome, p.home)
	}
	if !strings.Contains(r.UserAgent, codexVersion) {
		t.Errorf("userAgent %q carries no version %s", r.UserAgent, codexVersion)
	}
	p.write(map[string]any{"method": "initialized"})
	p.stop()
}

func TestRequestErrors(t *testing.T) {
	p := startFake(t)
	if f := p.call("thread/start", map[string]any{}); f.Error == nil {
		t.Errorf("thread/start before initialize: want an error, got %s", f.Result)
	}
	p.initialize()
	// Written raw: write would reject a method the schema lacks.
	if _, err := io.WriteString(p.stdin, `{"id":99,"method":"no/such/method","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if f := p.read(); f.Error == nil || f.Error.Code != -32601 {
		t.Errorf("unknown method: want error -32601, got %+v", f)
	}
	if f := p.call("turn/interrupt", map[string]any{"threadId": "t", "turnId": "nope"}); f.Error == nil {
		t.Errorf("interrupt of a turn not running: want an error, got %s", f.Result)
	}
	p.stop()
}

func TestThreadStartAndResume(t *testing.T) {
	p := startFake(t)
	p.initialize()
	id := p.startThread()
	if other := p.startThread(); other == id {
		t.Errorf("two thread/start calls minted the same id %q", id)
	}
	var r struct {
		Thread struct {
			ID    string `json:"id"`
			Turns []any  `json:"turns"`
		} `json:"thread"`
	}
	p.result("thread/resume", map[string]any{"threadId": id, "excludeTurns": true}, &r)
	if r.Thread.ID != id {
		t.Errorf("thread/resume id = %q, want %q", r.Thread.ID, id)
	}
	p.stop()
}

func TestTurnStreamsAgentMessage(t *testing.T) {
	p := startFake(t)
	p.initialize()
	thread := p.startThread()
	turn := p.startTurn(thread, "hello")
	s := decode[turnParams](t, p.expect("turn/started").Params)
	if s.ThreadID != thread || s.Turn.ID != turn || s.Turn.Status != "inProgress" {
		t.Fatalf("turn/started: %+v", s)
	}
	p.expectAgentMessage(turn)
	p.expectTurnCompleted(turn, "completed")
	p.stop()
}

func TestTurnInterrupt(t *testing.T) {
	p := startFake(t)
	p.initialize()
	thread := p.startThread()
	turn := p.startTurn(thread, "wait "+markerHold)
	p.expect("turn/started")
	f := p.call("turn/interrupt", map[string]any{"threadId": thread, "turnId": turn})
	if f.Error != nil {
		t.Fatalf("turn/interrupt: %+v", f.Error)
	}
	p.expectTurnCompleted(turn, "interrupted")
	p.stop()
}

func TestTurnApproval(t *testing.T) {
	for _, tc := range []struct {
		decision string
		status   string
	}{
		{"decline", "declined"},
		{"accept", "completed"},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			p := startFake(t)
			p.initialize()
			thread := p.startThread()
			turn := p.startTurn(thread, "run it "+markerApproval)
			p.expect("turn/started")

			cmd := decode[itemParams](t, p.expect("item/started").Params)
			if cmd.Item.Type != "commandExecution" || cmd.Item.Status != "inProgress" {
				t.Fatalf("command item/started: %+v", cmd)
			}
			req := p.expect("item/commandExecution/requestApproval")
			if len(req.ID) == 0 {
				t.Fatal("approval request carries no id")
			}
			ap := decode[struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
				ItemID   string `json:"itemId"`
			}](t, req.Params)
			if ap.ThreadID != thread || ap.TurnID != turn || ap.ItemID != cmd.Item.ID {
				t.Fatalf("approval params: %+v", ap)
			}
			p.write(map[string]any{"id": req.ID, "result": map[string]any{"decision": tc.decision}})

			res := decode[struct {
				ThreadID  string          `json:"threadId"`
				RequestID json.RawMessage `json:"requestId"`
			}](t, p.expect("serverRequest/resolved").Params)
			if res.ThreadID != thread || string(res.RequestID) != string(req.ID) {
				t.Fatalf("serverRequest/resolved: %+v, request id %s", res, req.ID)
			}
			done := decode[itemParams](t, p.expect("item/completed").Params)
			if done.Item.ID != cmd.Item.ID || done.Item.Status != tc.status {
				t.Fatalf("command item/completed: want status %s, got %+v", tc.status, done.Item)
			}
			if tc.status == "completed" && (done.Item.ExitCode == nil || *done.Item.ExitCode != 0) {
				t.Fatalf("accepted command: want exitCode 0, got %v", done.Item.ExitCode)
			}
			p.expectAgentMessage(turn)
			p.expectTurnCompleted(turn, "completed")
			p.stop()
		})
	}
}
