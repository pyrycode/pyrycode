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
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
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
	h.r.cfg.Approvals = newCodexApprovals(p.registry, window, surface, nil)
	return h, p
}

func newQuestionCodexRunner(t *testing.T, window time.Duration, turnLog string) (*codexHarnessT, questionFixture) {
	t.Helper()
	if turnLog != "" {
		t.Setenv("FAKECODEX_TURN_LOG", turnLog)
	}
	f := newQuestionFixture(t, "codex-conversation", discardLogger())
	surface := &approvalSurfaceReport{show: f.bridge.Surface}
	h := newTestCodexRunner(t)
	h.r.cfg.Approvals = newCodexApprovals(f.perm, window, surface, nil)
	return h, f
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

func TestCodexQuestionRequest(t *testing.T) {
	valid := `{"threadId":"t","turnId":"turn","itemId":"item","isBlocking":true,"questions":[` +
		`{"id":"language","header":"Language","question":"Which language?","isOther":true,"isSecret":false,"options":[{"label":"Go","description":"Use Go"},{"label":"Rust","description":"Use Rust"}]},` +
		`{"id":"style","header":"Style","question":"Which style?","isOther":true,"isSecret":false,"options":[{"label":"Direct","description":"Keep it direct"},{"label":"Layered","description":"Add layers"}]}` +
		`]}`
	req, refs, ok := codexQuestionRequest(json.RawMessage(valid))
	if !ok {
		t.Fatal("valid Codex question request was rejected")
	}
	if req.ToolName != questionbridge.ToolName {
		t.Fatalf("ToolName = %q, want %q", req.ToolName, questionbridge.ToolName)
	}
	wantRefs := []codexQuestionRef{{id: "language", text: "Which language?"}, {id: "style", text: "Which style?"}}
	if fmt.Sprint(refs) != fmt.Sprint(wantRefs) {
		t.Fatalf("refs = %#v, want %#v", refs, wantRefs)
	}
	batch, ok := questionbridge.Parse(req.ToolName, req.Input)
	if !ok {
		t.Fatal("adapted request does not pass the shared parser")
	}
	want := []protocol.Question{
		{Text: "Which language?", Header: "Language", Options: []protocol.QuestionOption{{Label: "Go", Description: "Use Go"}, {Label: "Rust", Description: "Use Rust"}}, MultiSelect: false},
		{Text: "Which style?", Header: "Style", Options: []protocol.QuestionOption{{Label: "Direct", Description: "Keep it direct"}, {Label: "Layered", Description: "Add layers"}}, MultiSelect: false},
	}
	if fmt.Sprint(batch.Questions) != fmt.Sprint(want) {
		t.Fatalf("questions = %#v, want %#v", batch.Questions, want)
	}
	q := func(id, text string) string {
		return fmt.Sprintf(`{"id":%q,"header":"H","question":%q,"isOther":true,"isSecret":false,"options":[{"label":"a","description":"a"},{"label":"b","description":"b"}]}`, id, text)
	}
	fiveQuestions := `{"threadId":"t","turnId":"turn","itemId":"item","isBlocking":true,"questions":[` +
		q("1", "Q1") + `,` + q("2", "Q2") + `,` + q("3", "Q3") + `,` + q("4", "Q4") + `,` + q("5", "Q5") + `]}`
	fiveOptions := strings.Replace(valid,
		`{"label":"Go","description":"Use Go"},{"label":"Rust","description":"Use Rust"}`,
		`{"label":"1","description":"1"},{"label":"2","description":"2"},{"label":"3","description":"3"},{"label":"4","description":"4"},{"label":"5","description":"5"}`, 1)

	for _, tc := range []struct {
		name, params string
	}{
		{"malformed", `[`},
		{"no questions", `{"threadId":"t","turnId":"turn","itemId":"item","isBlocking":true,"questions":[]}`},
		{"five questions", fiveQuestions},
		{"missing required string", strings.Replace(valid, `"question":"Which language?",`, ``, 1)},
		{"missing flag", strings.Replace(valid, `"isSecret":false,`, ``, 1)},
		{"other disabled", strings.Replace(valid, `"isOther":true`, `"isOther":false`, 1)},
		{"secret", strings.Replace(valid, `"isSecret":false`, `"isSecret":true`, 1)},
		{"duplicate id", strings.Replace(valid, `"id":"style"`, `"id":"language"`, 1)},
		{"duplicate text", strings.Replace(valid, `"Which style?"`, `"Which language?"`, 1)},
		{"one option", strings.Replace(valid, `,{"label":"Rust","description":"Use Rust"}`, ``, 1)},
		{"five options", fiveOptions},
		{"missing option field", strings.Replace(valid, `"description":"Use Go"`, `"detail":"Use Go"`, 1)},
		{"displayed input over bound", strings.Replace(valid, "Use Go", strings.Repeat("x", 17000), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := codexQuestionRequest(json.RawMessage(tc.params)); ok {
				t.Fatal("ineligible Codex question request was accepted")
			}
		})
	}
}

func TestCodexQuestionResponse(t *testing.T) {
	refs := []codexQuestionRef{{id: "language", text: "Which language?"}, {id: "style", text: "Which style?"}}
	allow := permbridge.Allow(json.RawMessage(`{"questions":[],"answers":{"Which language?":"Go","Which style?":"custom"}}`))
	got := codexQuestionResponse(refs, allow)
	want := map[string]codexQuestionAnswer{"language": {Answers: []string{"Go"}}, "style": {Answers: []string{"custom"}}}
	if fmt.Sprint(got.Answers) != fmt.Sprint(want) {
		t.Fatalf("answers = %#v, want %#v", got.Answers, want)
	}
	for _, verdict := range []permbridge.Verdict{
		permbridge.Deny("no"),
		permbridge.Allow(json.RawMessage(`{"questions":[],"answers":{"Which language?":"Go"}}`)),
		permbridge.Allow(json.RawMessage(`{"questions":[],"answers":{"Which language?":["Go"],"Which style?":"custom"}}`)),
	} {
		if got := codexQuestionResponse(refs, verdict); len(got.Answers) != 0 {
			t.Fatalf("invalid verdict produced answers: %#v", got.Answers)
		}
	}
}

func TestCodexQuestion_AnswerRoundTrip(t *testing.T) {
	log := filepath.Join(t.TempDir(), "turns.jsonl")
	h, f := newQuestionCodexRunner(t, time.Minute, log)
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:question]")
	waitPush(t, f.pushed)
	shown := lastQuestionShown(t, f.bcast.pushes)
	if len(shown.Questions) != 2 || shown.Questions[0].Text != "Which language?" || shown.Questions[0].Header != "Language" || shown.Questions[0].MultiSelect {
		t.Fatalf("question_shown = %#v, want the ordered Codex batch", shown)
	}
	if got := shown.Questions[0].Options; len(got) != 2 || got[0].Label != "Go" || got[0].Description != "Use Go" {
		t.Fatalf("first options = %#v, want Codex labels and descriptions", got)
	}
	if !f.bridge.AnswerQuestion(shown.QuestionBatchID, []protocol.QuestionAnswerEntry{
		{QuestionIndex: 0, Values: []string{"Go"}},
		{QuestionIndex: 1, Values: []string{"a free-text style"}},
	}) {
		t.Fatal("AnswerQuestion did not consume the Codex batch")
	}
	waitPush(t, f.pushed)
	h.await(t, "turn end", isTurnEnd)
	if got := pushTypes(f.bcast.pushes); fmt.Sprint(got) != fmt.Sprint([]string{protocol.TypeQuestionShown, protocol.TypeQuestionDismissed}) {
		t.Fatalf("pushes = %v, want exactly question_shown then question_dismissed", got)
	}
	response := questionResponseFromLog(t, log)
	want := map[string]any{
		"language": map[string]any{"answers": []any{"Go"}},
		"style":    map[string]any{"answers": []any{"a free-text style"}},
	}
	if fmt.Sprint(response) != fmt.Sprint(want) {
		t.Fatalf("Codex response = %#v, want %#v", response, want)
	}
}

func TestCodexQuestion_NoAnswerTerminalsReturnEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window time.Duration
		act    func(*codexHarnessT, questionFixture, string)
	}{
		{name: "refused", window: time.Minute, act: func(_ *codexHarnessT, f questionFixture, id string) { f.bridge.RefuseQuestion(id) }},
		{name: "window expiry", window: 2 * time.Second},
		{name: "interrupt", window: time.Minute, act: func(h *codexHarnessT, _ questionFixture, _ string) { _ = h.r.Interrupt() }},
		{name: "teardown", window: time.Minute, act: func(h *codexHarnessT, _ questionFixture, _ string) { h.r.BeginTeardown() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "turns.jsonl")
			h, f := newQuestionCodexRunner(t, tc.window, log)
			h.run(t)
			h.bound(t, nil)
			h.turn(t, "[fakecodex:question]")
			waitPush(t, f.pushed)
			if tc.act != nil {
				shown := lastQuestionShown(t, f.bcast.pushes)
				tc.act(h, f, shown.QuestionBatchID)
			}
			waitPush(t, f.pushed)
			h.await(t, "turn end", isTurnEnd)
			if got := questionResponseFromLog(t, log); len(got) != 0 {
				t.Fatalf("Codex response = %#v, want empty answers", got)
			}
			if got := pushTypes(f.bcast.pushes); fmt.Sprint(got) != fmt.Sprint([]string{protocol.TypeQuestionShown, protocol.TypeQuestionDismissed}) {
				t.Fatalf("pushes = %v, want exactly question_shown then question_dismissed", got)
			}
		})
	}
}

func TestCodexQuestion_WithdrawnWritesNothing(t *testing.T) {
	log := filepath.Join(t.TempDir(), "turns.jsonl")
	h, f := newQuestionCodexRunner(t, time.Minute, log)
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:question-withdraw]")
	waitPush(t, f.pushed)
	waitPush(t, f.pushed)
	h.await(t, "turn end", isTurnEnd)
	h.turn(t, "following turn")
	h.await(t, "turn end", isTurnEnd)
	if got := pushTypes(f.bcast.pushes); fmt.Sprint(got) != fmt.Sprint([]string{protocol.TypeQuestionShown, protocol.TypeQuestionDismissed}) {
		t.Fatalf("pushes = %v, want exactly question_shown then question_dismissed", got)
	}
	for _, line := range readTurnLog(t, log) {
		if _, answered := line["questionResponse"]; answered {
			t.Fatalf("withdrawn request received a response: %#v", line)
		}
		if _, late := line["lateResponse"]; late {
			t.Fatalf("withdrawn request received a late response: %#v", line)
		}
	}
}

func TestCodexQuestion_ControlledProcessTeardownReturnsEmpty(t *testing.T) {
	log := filepath.Join(t.TempDir(), "turns.jsonl")
	h, f := newQuestionCodexRunner(t, time.Minute, log)
	stop := h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:question]")
	waitPush(t, f.pushed)
	stop()
	waitPush(t, f.pushed)
	if got := pushTypes(f.bcast.pushes); fmt.Sprint(got) != fmt.Sprint([]string{protocol.TypeQuestionShown, protocol.TypeQuestionDismissed}) {
		t.Fatalf("pushes = %v, want exactly question_shown then question_dismissed", got)
	}
	if got := questionResponseFromLog(t, log); len(got) != 0 {
		t.Fatalf("Codex response = %#v, want empty answers", got)
	}
}

func questionResponseFromLog(t *testing.T, path string) map[string]any {
	t.Helper()
	for _, line := range readTurnLog(t, path) {
		response, ok := line["questionResponse"].(map[string]any)
		if !ok {
			continue
		}
		answers, ok := response["answers"].(map[string]any)
		if !ok {
			t.Fatalf("question response has no answers object: %#v", response)
		}
		return answers
	}
	t.Fatal("turn log has no question response")
	return nil
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
		Approvals: newCodexApprovals(registry, time.Minute, surface, nil),
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
