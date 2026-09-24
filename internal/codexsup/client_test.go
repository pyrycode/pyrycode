package codexsup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const schemaPath = "codex_app_server_protocol.schemas.json"

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "codexsup-test-*")
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

// schemaMethods maps each top-level message definition to the method names
// its oneOf arms allow, read from the committed schema.
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
		def := bundle.Definitions[name]
		if len(def.OneOf) == 0 {
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
	subset := func(group string, methods []string) {
		t.Helper()
		for _, m := range methods {
			if !groups[group][m] {
				t.Errorf("method %q is not in the schema's %s", m, group)
			}
		}
	}
	subset("ClientRequest", clientRequests)
	subset("ClientNotification", clientNotifications)
	equal := func(group string, methods []string) {
		t.Helper()
		subset(group, methods)
		listed := map[string]bool{}
		for _, m := range methods {
			listed[m] = true
		}
		for m := range groups[group] {
			if !listed[m] {
				t.Errorf("schema's %s has %q, which the package does not register", group, m)
			}
		}
	}
	equal("ServerRequest", serverRequests)
	equal("ServerNotification", serverNotifications)
}

// notes collects notifications in arrival order.
type notes chan [2]string

func (n notes) on(method string, params json.RawMessage) { n <- [2]string{method, string(params)} }

// until returns the methods received up to and including the first frame
// whose method is last, plus that frame's params.
func (n notes) until(t *testing.T, last string) ([]string, string) {
	t.Helper()
	var got []string
	for {
		select {
		case f := <-n:
			got = append(got, f[0])
			if f[0] == last {
				return got, f[1]
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s; got %v", last, got)
		}
	}
}

func startFake(t *testing.T, cfg Config) *Client {
	t.Helper()
	cfg.Binary = fakeBin
	cfg.Dir = t.TempDir()
	cfg.CodexHome = t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	return c
}

func ctx5(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestHandshakeAndStop(t *testing.T) {
	c := startFake(t, Config{})
	if got := c.Version(); got != "0.156.1" {
		t.Errorf("Version() = %q, want 0.156.1", got)
	}
	if err := c.Stop(ctx5(t)); err != nil {
		t.Errorf("Stop: %v", err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}
	if _, err := c.StartThread(ctx5(t)); !errors.Is(err, ErrExited) {
		t.Errorf("StartThread after Stop: %v, want ErrExited", err)
	}
}

func TestStartValidatesConfig(t *testing.T) {
	if _, err := Start(ctx5(t), Config{Binary: fakeBin, Dir: t.TempDir()}); err == nil {
		t.Error("Start without CodexHome succeeded")
	}
}

func TestThreadTurnNotificationsInOrder(t *testing.T) {
	n := make(notes, 64)
	c := startFake(t, Config{OnNotification: n.on})
	id, err := c.StartThread(ctx5(t))
	if err != nil || id == "" || c.ThreadID() != id {
		t.Fatalf("StartThread = %q, %v; ThreadID %q", id, err, c.ThreadID())
	}
	if _, err := c.StartTurn(ctx5(t), TurnInput{Text: "hi", Model: "gpt-6-luna", Effort: "low"}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	got, _ := n.until(t, "turn/completed")
	want := []string{"turn/started", "item/started", "item/agentMessage/delta", "item/completed", "turn/completed"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("notifications = %v, want %v", got, want)
	}
}

func TestStartTurnWithoutThread(t *testing.T) {
	c := startFake(t, Config{})
	if _, err := c.StartTurn(ctx5(t), TurnInput{Text: "hi"}); !errors.Is(err, ErrNoThread) {
		t.Errorf("StartTurn = %v, want ErrNoThread", err)
	}
}

func TestResumeThread(t *testing.T) {
	first := startFake(t, Config{})
	id, err := first.StartThread(ctx5(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Stop(ctx5(t))
	second := startFake(t, Config{})
	if err := second.ResumeThread(ctx5(t), id); err != nil {
		t.Fatalf("ResumeThread: %v", err)
	}
	if second.ThreadID() != id {
		t.Errorf("ThreadID() = %q, want %q", second.ThreadID(), id)
	}
}

func TestInterrupt(t *testing.T) {
	n := make(notes, 64)
	c := startFake(t, Config{OnNotification: n.on})
	if _, err := c.StartThread(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	turn, err := c.StartTurn(ctx5(t), TurnInput{Text: "[fakecodex:hold]"})
	if err != nil {
		t.Fatal(err)
	}
	n.until(t, "turn/started")
	if err := c.Interrupt(ctx5(t), turn); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if _, params := n.until(t, "turn/completed"); !strings.Contains(params, `"status":"interrupted"`) {
		t.Errorf("turn/completed params = %s, want status interrupted", params)
	}
}

// approvalOutcome runs an approval turn and returns the command item's final
// status.
func approvalOutcome(t *testing.T, onRequest func(*ServerRequest)) string {
	t.Helper()
	n := make(notes, 64)
	c := startFake(t, Config{OnNotification: n.on, OnServerRequest: onRequest})
	if _, err := c.StartThread(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartTurn(ctx5(t), TurnInput{Text: "[fakecodex:approval]"}); err != nil {
		t.Fatal(err)
	}
	n.until(t, "serverRequest/resolved")
	_, params := n.until(t, "item/completed")
	var p struct {
		Item struct{ Status string } `json:"item"`
	}
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		t.Fatal(err)
	}
	return p.Item.Status
}

func TestApprovalDeclinedByDefault(t *testing.T) {
	if got := approvalOutcome(t, nil); got != "declined" {
		t.Errorf("item status = %q, want declined", got)
	}
}

func TestApprovalAnsweredLater(t *testing.T) {
	reqs := make(chan *ServerRequest, 1)
	go func() {
		r := <-reqs
		if r.Method != methodCommandApproval {
			t.Errorf("method = %q", r.Method)
		}
		_ = r.Respond(map[string]string{"decision": "accept"})
	}()
	if got := approvalOutcome(t, func(r *ServerRequest) { reqs <- r }); got != "completed" {
		t.Errorf("item status = %q, want completed", got)
	}
}

func TestProcessDiesOnItsOwn(t *testing.T) {
	c := startFake(t, Config{})
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after the process died")
	}
	if c.Err() == nil {
		t.Error("Err() = nil after a kill")
	}
}

// peer is an in-memory app-server. Every frame the client writes is checked
// against the schema's client groups and delivered on frames, except
// initialize, which is answered with userAgent.
type peer struct {
	t       *testing.T
	groups  map[string]map[string]bool
	toPeer  *io.PipeReader
	toCli   *io.PipeWriter
	frames  chan map[string]json.RawMessage
	exitErr chan error
}

func startPeer(t *testing.T, userAgent string, cfg Config) (*Client, *peer) {
	t.Helper()
	cliR, peerW := io.Pipe()
	peerR, cliW := io.Pipe()
	p := &peer{t: t, groups: schemaMethods(t), toPeer: peerR, toCli: peerW,
		frames: make(chan map[string]json.RawMessage, 16), exitErr: make(chan error, 1)}
	go p.read(userAgent)
	c := newClient(cfg, cliR, cliW, func() error { return <-p.exitErr }, func() { p.exit(errors.New("killed")) })
	if err := c.handshake(ctx5(t)); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { p.exit(nil); <-c.Done() })
	return c, p
}

func (p *peer) read(userAgent string) {
	sc := bufio.NewScanner(p.toPeer)
	for sc.Scan() {
		var f map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			p.t.Errorf("client wrote non-JSON %q", sc.Text())
			continue
		}
		var method string
		_ = json.Unmarshal(f["method"], &method)
		if method != "" {
			group := "ClientNotification"
			if _, ok := f["id"]; ok {
				group = "ClientRequest"
			}
			if !p.groups[group][method] {
				p.t.Errorf("client sent %q, absent from the schema's %s", method, group)
			}
		}
		if method == methodInitialize {
			p.send(fmt.Sprintf(`{"id":%s,"result":{"codexHome":"/h","platformFamily":"unix","platformOs":"macos","userAgent":%q}}`, f["id"], userAgent))
			continue
		}
		p.frames <- f
	}
}

func (p *peer) send(line string) { _, _ = io.WriteString(p.toCli, line+"\n") }

// exit ends the peer as a process exit would: its output closes, then wait
// reports err.
func (p *peer) exit(err error) {
	_ = p.toCli.Close()
	_ = p.toPeer.Close()
	select {
	case p.exitErr <- err:
	default:
	}
}

func (p *peer) next(method string) map[string]json.RawMessage {
	p.t.Helper()
	for {
		select {
		case f := <-p.frames:
			var m string
			_ = json.Unmarshal(f["method"], &m)
			if m == method {
				return f
			}
		case <-time.After(5 * time.Second):
			p.t.Fatalf("timed out waiting for %s", method)
		}
	}
}

func TestVersionFromPlatformUserAgent(t *testing.T) {
	c, _ := startPeer(t, "pyrycode/0.156.1 (Mac OS 15.5.0; arm64) iTerm.app/3.5", Config{})
	if c.Version() != "0.156.1" {
		t.Errorf("Version() = %q", c.Version())
	}
}

func TestRequestParamShapes(t *testing.T) {
	c, p := startPeer(t, "codex/0.156.1", Config{})
	go func() {
		f := p.next(methodThreadResume)
		p.send(fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":"th-1"}}}`, f["id"]))
		if got := string(f["params"]); !strings.Contains(got, `"excludeTurns":true`) || !strings.Contains(got, `"threadId":"th-1"`) {
			t.Errorf("thread/resume params = %s", got)
		}
		f = p.next(methodTurnStart)
		p.send(fmt.Sprintf(`{"id":%s,"result":{"turn":{"id":"tu-1"}}}`, f["id"]))
		want := `{"threadId":"th-1","input":[{"type":"text","text":"go"}],"model":"gpt-6-luna","effort":"low"}`
		if got := string(f["params"]); got != want {
			t.Errorf("turn/start params = %s, want %s", got, want)
		}
	}()
	if err := c.ResumeThread(ctx5(t), "th-1"); err != nil {
		t.Fatal(err)
	}
	turn, err := c.StartTurn(ctx5(t), TurnInput{Text: "go", Model: "gpt-6-luna", Effort: "low"})
	if err != nil || turn != "tu-1" {
		t.Fatalf("StartTurn = %q, %v", turn, err)
	}
}

func TestDefaultDeclines(t *testing.T) {
	_, p := startPeer(t, "codex/0.156.1", Config{})
	want := map[string]string{
		methodCommandApproval:     `{"decision":"decline"}`,
		methodFileChangeApproval:  `{"decision":"decline"}`,
		methodPermissionsApproval: `{"permissions":{}}`,
		methodApplyPatchApproval:  `{"decision":{"denied":{"rejection":"` + declineRejection + `"}}}`,
		methodExecCommandApproval: `{"decision":{"denied":{"rejection":"` + declineRejection + `"}}}`,
	}
	methods := append([]string(nil), serverRequests...)
	sort.Strings(methods)
	for i, m := range methods {
		p.send(fmt.Sprintf(`{"id":"s%d","method":%q,"params":{}}`, i, m))
		var f map[string]json.RawMessage
		for f == nil || f["method"] != nil { // skip the initialized notification
			select {
			case f = <-p.frames:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s: no answer", m)
			}
		}
		if string(f["id"]) != fmt.Sprintf(`"s%d"`, i) {
			t.Fatalf("%s: answer id %s", m, f["id"])
		}
		if w, ok := want[m]; ok {
			if string(f["result"]) != w || f["error"] != nil {
				t.Errorf("%s: result %s error %s, want %s", m, f["result"], f["error"], w)
			}
		} else if f["error"] == nil || f["result"] != nil {
			t.Errorf("%s: want a JSON-RPC error, got result %s", m, f["result"])
		}
	}
}

func TestPendingCallFailsOnExit(t *testing.T) {
	c, p := startPeer(t, "codex/0.156.1", Config{})
	go func() {
		p.next(methodThreadStart)
		p.exit(errors.New("exit status 3"))
	}()
	_, err := c.StartThread(ctx5(t))
	if !errors.Is(err, ErrExited) || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("StartThread = %v, want ErrExited with the exit error", err)
	}
	<-c.Done()
	if c.Err() == nil || !strings.Contains(c.Err().Error(), "exit status 3") {
		t.Errorf("Err() = %v", c.Err())
	}
}

// TestSignedIn: only a null or absent account with requiresOpenaiAuth true
// reads as signed out.
func TestSignedIn(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		want         bool
	}{
		{"chatgpt account", `{"account":{"type":"chatgpt","email":"a@example.invalid","planType":"plus"},"requiresOpenaiAuth":true}`, true},
		{"api key account", `{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`, true},
		{"null account, auth required", `{"account":null,"requiresOpenaiAuth":true}`, false},
		{"absent account, auth required", `{"requiresOpenaiAuth":true}`, false},
		{"null account, no auth required", `{"account":null,"requiresOpenaiAuth":false}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := startPeer(t, "codex_cli_rs/0.156.1", Config{})
			type answer struct {
				ok  bool
				err error
			}
			got := make(chan answer, 1)
			go func() {
				ok, err := c.SignedIn(ctx5(t))
				got <- answer{ok, err}
			}()
			f := p.next(methodAccountRead)
			if string(f["params"]) != "{}" {
				t.Errorf("account/read params = %s, want {}", f["params"])
			}
			p.send(fmt.Sprintf(`{"id":%s,"result":%s}`, f["id"], tc.result))
			a := <-got
			if a.err != nil || a.ok != tc.want {
				t.Fatalf("SignedIn = %v, %v; want %v", a.ok, a.err, tc.want)
			}
		})
	}
}

// TestSignedInAgainstFake: the fake is signed in by default and signed out
// under FAKECODEX_SIGNED_OUT.
func TestSignedInAgainstFake(t *testing.T) {
	if ok, err := startFake(t, Config{}).SignedIn(ctx5(t)); err != nil || !ok {
		t.Fatalf("default fake: SignedIn = %v, %v; want true", ok, err)
	}
	t.Setenv("FAKECODEX_SIGNED_OUT", "1")
	t.Setenv("FAKECODEX_VERSION", "0.155.0-alpha.3")
	c := startFake(t, Config{})
	if ok, err := c.SignedIn(ctx5(t)); err != nil || ok {
		t.Fatalf("signed-out fake: SignedIn = %v, %v; want false", ok, err)
	}
	if c.Version() != "0.155.0-alpha.3" {
		t.Fatalf("Version() = %q, want 0.155.0-alpha.3", c.Version())
	}
}
