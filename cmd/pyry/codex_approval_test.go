package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// approvalProbe is the modal surface a Codex approval test sees: every
// surfaced request, and every retirement by the request's registry id.
type approvalProbe struct {
	registry *permbridge.Registry
	shown    chan permbridge.Request
	retired  chan string
}

// newApprovalCodexRunner is newTestCodexRunner with the approvals path wired
// to a fresh registry and a probe surface. turnLog, when set, is the fake's
// FAKECODEX_TURN_LOG.
func newApprovalCodexRunner(t *testing.T, window time.Duration, turnLog string) (*codexHarnessT, *approvalProbe) {
	t.Helper()
	if turnLog != "" {
		t.Setenv("FAKECODEX_TURN_LOG", turnLog)
	}
	p := &approvalProbe{
		registry: permbridge.New(),
		shown:    make(chan permbridge.Request, 8),
		retired:  make(chan string, 8),
	}
	surface := &approvalSurfaceReport{show: func(req permbridge.Request) func() {
		p.shown <- req
		return func() { p.retired <- req.ToolUseID }
	}}
	h := newTestCodexRunner(t)
	h.r.cfg.Approvals = newCodexApprovals(p.registry, window, surface)
	return h, p
}

func (p *approvalProbe) nextShown(t *testing.T) permbridge.Request {
	t.Helper()
	select {
	case req := <-p.shown:
		return req
	case <-time.After(10 * time.Second):
		t.Fatal("no approval surfaced")
		return permbridge.Request{}
	}
}

func (p *approvalProbe) awaitRetired(t *testing.T, id string) {
	t.Helper()
	select {
	case got := <-p.retired:
		if got != id {
			t.Fatalf("retired %q, want %q", got, id)
		}
		if _, live := p.registry.Lookup(id); live {
			t.Fatalf("registry still holds %q after its modal retired", id)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("approval %q never retired", id)
	}
}

func (h *codexHarnessT) toolResult(t *testing.T) string {
	t.Helper()
	return h.await(t, "tool update", func(ev turnevent.Event) bool {
		_, ok := ev.(turnevent.ToolUpdate)
		return ok
	}).(turnevent.ToolUpdate).ResultDetail
}

func TestCodexApproval_AllowRunsCommand(t *testing.T) {
	h, p := newApprovalCodexRunner(t, time.Minute, "")
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:approval]")
	req := p.nextShown(t)
	if !strings.HasPrefix(req.ToolUseID, "codex-") || req.ToolName != "Codex command" {
		t.Fatalf("surfaced id %q tool %q", req.ToolUseID, req.ToolName)
	}
	if !strings.Contains(req.Description, "command: echo fakecodex\ncwd: ") {
		t.Fatalf("Description = %q, want the command and its cwd", req.Description)
	}
	if req.AlwaysAllow.Offered() {
		t.Fatal("always-allow offered though Codex did not list acceptForSession")
	}
	p.registry.Resolve(req.ToolUseID, permbridge.Allow(req.Input))
	if got := h.toolResult(t); got != "exit 0" {
		t.Fatalf("tool result = %q, want the command run", got)
	}
	p.awaitRetired(t, req.ToolUseID)
	h.await(t, "turn end", isTurnEnd)
}

func TestCodexApproval_DenyDeclines(t *testing.T) {
	h, p := newApprovalCodexRunner(t, time.Minute, "")
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:approval]")
	req := p.nextShown(t)
	p.registry.Resolve(req.ToolUseID, permbridge.Deny("no"))
	if got := h.toolResult(t); got != "declined" {
		t.Fatalf("tool result = %q, want declined", got)
	}
	p.awaitRetired(t, req.ToolUseID)
}

func TestCodexApproval_WindowExpiryDeclines(t *testing.T) {
	h, p := newApprovalCodexRunner(t, 50*time.Millisecond, "")
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:approval]")
	req := p.nextShown(t)
	if got := h.toolResult(t); got != "declined" {
		t.Fatalf("tool result = %q, want declined", got)
	}
	p.awaitRetired(t, req.ToolUseID)
}

// TestCodexApproval_WithdrawnWritesNothing: a request Codex resolves before
// anyone answers retires its modal and registry entry, and no answer reaches
// Codex. The following turn orders the check: an answer would be written
// before the retirement, so the fake reads it before that turn's turn/start.
func TestCodexApproval_WithdrawnWritesNothing(t *testing.T) {
	log := filepath.Join(t.TempDir(), "turns.jsonl")
	h, p := newApprovalCodexRunner(t, time.Minute, log)
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:withdraw]")
	req := p.nextShown(t)
	p.awaitRetired(t, req.ToolUseID)
	h.await(t, "turn end", isTurnEnd)
	h.turn(t, "hello")
	h.await(t, "turn end", isTurnEnd)
	for _, line := range readTurnLog(t, log) {
		if _, late := line["lateResponse"]; late {
			t.Fatalf("an answer reached Codex for a withdrawn request: %v", line)
		}
	}
}

func TestCodexApproval_InterruptDeclines(t *testing.T) {
	h, p := newApprovalCodexRunner(t, time.Minute, "")
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:approval]")
	req := p.nextShown(t)
	if err := h.r.Interrupt(); err != nil {
		t.Fatalf("Interrupt = %v", err)
	}
	p.awaitRetired(t, req.ToolUseID)
	h.await(t, "turn end", isTurnEnd)
}

func TestCodexApproval_ExitDeclines(t *testing.T) {
	h, p := newApprovalCodexRunner(t, time.Minute, "")
	stop := h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:approval]")
	req := p.nextShown(t)
	stop()
	p.awaitRetired(t, req.ToolUseID)
}

func TestCodexApprovalRequest(t *testing.T) {
	for _, tc := range []struct {
		name, method, params string
		paths                []string
		ok                   bool
		tool, description    string
		reason               string
		rules                []string
	}{
		{name: "command", method: "item/commandExecution/requestApproval",
			params: `{"itemId":"i","command":"ls -la","cwd":"/w","reason":"needs a listing","availableDecisions":["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["ls"]}},"cancel"]}`,
			ok:     true, tool: "Codex command", description: "command: ls -la\ncwd: /w", reason: `"needs a listing"`},
		{name: "command offering the session", method: "item/commandExecution/requestApproval",
			params: `{"itemId":"i","command":"ls","cwd":"/w","availableDecisions":["accept","acceptForSession","decline"]}`,
			ok:     true, tool: "Codex command", description: "command: ls\ncwd: /w", rules: []string{"ls"}},
		{name: "forged cwd line is escaped", method: "item/commandExecution/requestApproval",
			params: `{"itemId":"i","command":"rm -rf x\ncwd: /safe","cwd":"/w"}`,
			ok:     true, tool: "Codex command", description: `command: rm -rf x\ncwd: /safe` + "\ncwd: /w"},
		{name: "file change", method: "item/fileChange/requestApproval",
			params: `{"itemId":"f","reason":"write access"}`, paths: []string{"/w/a.txt", "/w/b.txt"},
			ok: true, tool: "Codex file change", description: "paths:\n/w/a.txt\n/w/b.txt", reason: `"write access"`},
		{name: "file change offering the session", method: "item/fileChange/requestApproval",
			params: `{"itemId":"f","availableDecisions":["acceptForSession"]}`, paths: []string{"/w/a.txt"},
			ok: true, tool: "Codex file change", description: "paths:\n/w/a.txt", rules: []string{"/w/a.txt"}},
		{name: "explicit command kind", method: "item/commandExecution/requestApproval",
			params: `{"itemId":"i","kind":"command","command":"ls","cwd":"/w","networkApprovalContext":null}`,
			ok:     true, tool: "Codex command", description: "command: ls\ncwd: /w"},
		{name: "escaped reason", method: "item/commandExecution/requestApproval",
			params: `{"itemId":"i","command":"ls","cwd":"/w","reason":"a\nb"}`,
			ok:     true, tool: "Codex command", description: "command: ls\ncwd: /w", reason: `"a\\nb"`},
		{name: "stdin for a running terminal", method: "item/commandExecution/requestApproval",
			params: `{"itemId":"i","kind":"writeStdin","command":"ls","cwd":"/w"}`},
		{name: "network approval", method: "item/commandExecution/requestApproval",
			params: `{"itemId":"i","command":"curl x","cwd":"/w","networkApprovalContext":{"host":"example.com","protocol":"https"}}`},
		{name: "missing command", method: "item/commandExecution/requestApproval",
			params: `{"itemId":"i","command":null,"cwd":"/w"}`},
		{name: "command too long to show with its cwd", method: "item/commandExecution/requestApproval",
			params: `{"itemId":"i","command":"` + strings.Repeat("x", codexMaxDescription) + `","cwd":"/w"}`},
		{name: "file change with a write root", method: "item/fileChange/requestApproval",
			params: `{"itemId":"f","grantRoot":"/"}`, paths: []string{"/w/a.txt"}},
		{name: "file change with no paths seen", method: "item/fileChange/requestApproval",
			params: `{"itemId":"f"}`},
		{name: "unsupported method", method: "item/permissions/requestApproval", params: `{}`},
		{name: "unparsable params", method: "item/commandExecution/requestApproval", params: `[`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, ok := codexApprovalRequest(tc.method, json.RawMessage(tc.params), tc.paths)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if req.ToolName != tc.tool || req.Description != tc.description || string(req.DecisionReason) != tc.reason {
				t.Fatalf("got tool %q description %q reason %s", req.ToolName, req.Description, req.DecisionReason)
			}
			if got := req.AlwaysAllow.Rules(); req.AlwaysAllow.Offered() != (tc.rules != nil) || strings.Join(got, "|") != strings.Join(tc.rules, "|") {
				t.Fatalf("always-allow offered %v rules %v, want %v", req.AlwaysAllow.Offered(), got, tc.rules)
			}
		})
	}
}

// TestCodexApprovalRequest_TruncatedPathsMarked: a path list past the cap is
// cut with a visible marker, never silently.
func TestCodexApprovalRequest_TruncatedPathsMarked(t *testing.T) {
	paths := make([]string, 400)
	for i := range paths {
		paths[i] = "/w/" + strings.Repeat("p", 20)
	}
	req, ok := codexApprovalRequest("item/fileChange/requestApproval", json.RawMessage(`{"itemId":"f"}`), paths)
	if !ok {
		t.Fatal("file change not parked")
	}
	if len(req.Description) > codexMaxDescription {
		t.Fatalf("Description is %d bytes, over the %d cap", len(req.Description), codexMaxDescription)
	}
	full := len("paths:") + len(paths)*len("\n"+paths[0])
	cut := strings.LastIndex(req.Description, "\n…[truncated ")
	if cut < 0 {
		t.Fatalf("Description tail %q carries no truncation marker", req.Description[len(req.Description)-40:])
	}
	if want := fmt.Sprintf("\n…[truncated %d bytes]", full-cut); req.Description[cut:] != want {
		t.Fatalf("marker = %q, want %q", req.Description[cut:], want)
	}
}

func TestCodexApproval_TeardownDeclines(t *testing.T) {
	h, p := newApprovalCodexRunner(t, time.Minute, "")
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:approval]")
	req := p.nextShown(t)
	h.r.BeginTeardown()
	if got := h.toolResult(t); got != "declined" {
		t.Fatalf("tool result = %q, want declined", got)
	}
	p.awaitRetired(t, req.ToolUseID)
}

func TestCodexDecision(t *testing.T) {
	for _, tc := range []struct {
		v    permbridge.Verdict
		want string
	}{
		{permbridge.Allow(nil), "accept"},
		{permbridge.AllowAlways(nil, permbridge.SessionGrant("ls")), "acceptForSession"},
		{permbridge.AllowAlways(nil, permbridge.AlwaysAllow{}), "accept"},
		{permbridge.Deny("no"), "decline"},
		{permbridge.Verdict{}, "decline"},
	} {
		if got := codexDecision(tc.v); got != tc.want {
			t.Errorf("codexDecision(%+v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

// TestCodexApprovalLive shows, against a real Codex, that a declined command
// does not run and an accepted one does. It never runs under make check: it
// needs PYRY_CODEX_CAPTURE_BIN and PYRY_CODEX_CAPTURE_HOME, as TestCaptureLive
// does, and it spends Codex allowance on gpt-6-luna at low effort.
func TestCodexApprovalLive(t *testing.T) {
	bin, home := os.Getenv("PYRY_CODEX_CAPTURE_BIN"), os.Getenv("PYRY_CODEX_CAPTURE_HOME")
	if bin == "" || home == "" {
		t.Skip("set PYRY_CODEX_CAPTURE_BIN and PYRY_CODEX_CAPTURE_HOME to run the live Codex approval test")
	}
	dir := t.TempDir()
	registry := permbridge.New()
	surface := &approvalSurfaceReport{show: func(req permbridge.Request) func() {
		// Only the exact accepted.txt touch is allowed; everything else is
		// declined, including declined.txt.
		if strings.Contains(req.Description, "touch accepted.txt") && !strings.Contains(req.Description, "declined") {
			go registry.Resolve(req.ToolUseID, permbridge.Allow(req.Input))
		} else {
			go registry.Resolve(req.ToolUseID, permbridge.Deny("declined by the live test"))
		}
		return func() {}
	}}
	events := make(chan turnevent.Event, 1024)
	r := newCodexRunner(codexRunnerConfig{
		Binary: bin, Home: home, Dir: dir,
		Tag:       newStreamSessionTag("live-1"),
		Sink:      func(ev turnevent.Event) { events <- ev },
		Model:     "gpt-6-luna",
		Effort:    "low",
		Approvals: newCodexApprovals(registry, time.Minute, surface),
	})
	h := &codexHarnessT{r: r, events: events}
	h.run(t)
	h.bound(t, nil)
	for _, name := range []string{"declined.txt", "accepted.txt"} {
		if err := r.WriteUserTurn(context.Background(), "c-1", []byte("Use your shell tool to run `touch "+name+"` in the current directory, then reply done.")); err != nil {
			t.Fatalf("WriteUserTurn: %v", err)
		}
		select {
		case <-waitTurnEnd(events):
		case <-time.After(3 * time.Minute):
			t.Fatalf("turn for %s never ended", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "declined.txt")); !os.IsNotExist(err) {
		t.Errorf("declined.txt: stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "accepted.txt")); err != nil {
		t.Errorf("accepted.txt was not created: %v", err)
	}
}

// waitTurnEnd closes the returned channel at the next TurnEnd on events.
func waitTurnEnd(events <-chan turnevent.Event) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for ev := range events {
			if isTurnEnd(ev) {
				close(done)
				return
			}
		}
	}()
	return done
}
