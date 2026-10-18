//go:build codex_capture

package codexsup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureEffort is set on every captured turn; the thread never inherits a default.
const captureEffort = "low"

// captureFrame is one line of a capture file: a server notification, or a
// server request with the answer the capture gave it.
type captureFrame struct {
	Kind   string          `json:"kind"` // "notification" or "request"
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Answer json.RawMessage `json:"answer,omitempty"`
}

type captureScenario struct {
	name   string
	prompt string
	// accept reports whether an approval request is answered with accept;
	// anything it rejects is declined.
	accept func(method string, params json.RawMessage) bool
	// interrupt interrupts the turn after its first agent text delta.
	interrupt bool
}

// TestCaptureLive records live Codex frames with the codex_capture build tag:
//
//	go test -tags codex_capture -run TestCaptureLive ./internal/codexsup/
//
// It needs PYRY_CODEX_CAPTURE_BIN (a Codex 0.156.1 binary) and
// PYRY_CODEX_CAPTURE_HOME (an isolated CODEX_HOME holding only a minimal
// config and the sign-in). It spends Codex allowance.
func TestCaptureLive(t *testing.T) {
	captureLive(t, false)
}

// TestCaptureLiveWebSearch records only a web-search turn:
//
//	go test -tags codex_capture -run '^TestCaptureLiveWebSearch$' -v ./internal/codexsup/
//
// It uses the same isolated-home environment guards as TestCaptureLive.
func TestCaptureLiveWebSearch(t *testing.T) {
	captureLive(t, true)
}

func captureLive(t *testing.T, webSearchOnly bool) {
	t.Helper()
	bin, home := os.Getenv("PYRY_CODEX_CAPTURE_BIN"), os.Getenv("PYRY_CODEX_CAPTURE_HOME")
	if bin == "" || home == "" {
		t.Skip("set PYRY_CODEX_CAPTURE_BIN and PYRY_CODEX_CAPTURE_HOME to capture live Codex frames")
	}
	cwd := t.TempDir()
	var (
		mu       sync.Mutex
		frames   []captureFrame
		scenario captureScenario
		// changePaths holds each started fileChange item's paths by item id;
		// the approval params carry no paths of their own. Guarded by mu.
		changePaths = map[string][]string{}
	)
	scenarios := []captureScenario{
		{name: "plain", prompt: "Reply with exactly the word: hello"},
		{name: "reasoning", prompt: "Think it through, then answer with the number only: what is 17 times 23?"},
		{name: "command_accepted", prompt: "Use your shell tool to run `touch accepted.txt` in the current directory, then reply done.",
			accept: func(method string, params json.RawMessage) bool {
				return method == methodCommandApproval && isExactCommand(params, "touch accepted.txt")
			}},
		{name: "command_declined", prompt: "Use your shell tool to run `touch declined.txt` in the current directory, then reply done.",
			accept: func(string, json.RawMessage) bool { return false }},
		// file_edit accepts no command, only a file change whose every path
		// sits under cwd. Called under mu.
		{name: "file_edit", prompt: "Create the file notes.txt containing the single line hi, using your file editing tool, then reply done.",
			accept: func(method string, params json.RawMessage) bool {
				var p struct {
					ItemID    string `json:"itemId"`
					GrantRoot string `json:"grantRoot"`
				}
				if method != methodFileChangeApproval || json.Unmarshal(params, &p) != nil || p.GrantRoot != "" {
					return false
				}
				paths, ok := changePaths[p.ItemID]
				return ok && allUnder(cwd, paths)
			}},
		{name: "interrupted", prompt: "Write the numbers from 1 to 300, one per line.", interrupt: true},
	}
	if webSearchOnly {
		scenarios = []captureScenario{{name: "web_search", prompt: "Use your web search tool to search for the official Go programming language release notes. Summarize the result in one sentence. Do not use shell commands or edit files."}}
	}

	notes := make(chan captureFrame, 4096)
	record := func(f captureFrame) {
		mu.Lock()
		frames = append(frames, f)
		mu.Unlock()
	}
	cfg := Config{
		Binary: bin, Dir: cwd, CodexHome: home, ClientVersion: "capture",
		OnNotification: func(method string, params json.RawMessage) {
			f := captureFrame{Kind: "notification", Method: method, Params: append(json.RawMessage(nil), params...)}
			if method == "item/started" {
				var p struct {
					Item struct {
						Type    string `json:"type"`
						ID      string `json:"id"`
						Changes []struct {
							Path string `json:"path"`
						} `json:"changes"`
					} `json:"item"`
				}
				if json.Unmarshal(params, &p) == nil && p.Item.Type == "fileChange" {
					paths := make([]string, 0, len(p.Item.Changes))
					for _, c := range p.Item.Changes {
						paths = append(paths, c.Path)
					}
					mu.Lock()
					changePaths[p.Item.ID] = paths
					mu.Unlock()
				}
			}
			record(f)
			notes <- f
		},
		OnServerRequest: func(req *ServerRequest) {
			mu.Lock()
			accept := scenario.accept != nil && scenario.accept(req.Method, req.Params)
			mu.Unlock()
			var answer any
			switch {
			case accept && (req.Method == methodCommandApproval || req.Method == methodFileChangeApproval):
				answer = map[string]string{"decision": "accept"}
			default:
				answer, _ = declineFor(req.Method)
			}
			raw, _ := json.Marshal(answer)
			record(captureFrame{Kind: "request", Method: req.Method, Params: append(json.RawMessage(nil), req.Params...), Answer: raw})
			if answer == nil {
				_ = req.Decline()
				return
			}
			_ = req.Respond(answer)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	c, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()
	t.Logf("codex %s", c.Version())

	if err := os.MkdirAll(captureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, sc := range scenarios {
		mu.Lock()
		scenario, frames = sc, nil
		mu.Unlock()
		captureTurn(ctx, t, c, cwd, sc, notes)
		mu.Lock()
		got := frames
		mu.Unlock()
		if webSearchOnly {
			writeFocusedWebSearchCapture(t, got, cwd, home)
			continue
		}
		writeCapture(t, sc.name, got, cwd, home)
	}
}

// writeFocusedWebSearchCapture keeps the complete recorded stream private and
// publishes only the scrubbed same-id web-search pair. The broad capture suite
// keeps its existing output behavior.
func writeFocusedWebSearchCapture(t *testing.T, frames []captureFrame, cwd, home string) string {
	t.Helper()
	private, err := os.CreateTemp("", "pyry-codex-web-search-private-*.jsonl")
	if err != nil {
		t.Fatal("create private web-search recording")
	}
	enc := json.NewEncoder(private)
	for _, frame := range frames {
		if err := enc.Encode(frame); err != nil {
			_ = private.Close()
			t.Fatal("retain private web-search recording")
		}
	}
	if err := private.Close(); err != nil {
		t.Fatal("close private web-search recording")
	}
	t.Logf("private web-search recording: %s", private.Name())
	pair := requireWebSearchPair(t, frames)
	writeCapture(t, "web_search", pair, cwd, home)
	if evidenceDir := os.Getenv("PYRY_GATE_EVIDENCE_DIR"); evidenceDir != "" {
		if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
			t.Fatal("create durable web-search evidence directory")
		}
		data, err := os.ReadFile(filepath.Join(captureDir, "web_search.jsonl"))
		if err != nil {
			t.Fatal("read scrubbed web-search pair")
		}
		if err := os.WriteFile(filepath.Join(evidenceDir, "web_search.jsonl"), data, 0o600); err != nil {
			t.Fatal("retain durable web-search pair")
		}
	}
	return private.Name()
}

// requireWebSearchPair rejects a capture that proves no completed web search.
// Its diagnostics contain counts and field presence, never peer text.
func requireWebSearchPair(t *testing.T, frames []captureFrame) []captureFrame {
	t.Helper()
	started := map[string]captureFrame{}
	for _, f := range frames {
		if f.Kind != "notification" || f.Method != "item/started" && f.Method != "item/completed" {
			continue
		}
		var p struct {
			Item struct {
				Type   string          `json:"type"`
				ID     string          `json:"id"`
				Query  string          `json:"query"`
				Action json.RawMessage `json:"action"`
			} `json:"item"`
		}
		if err := json.Unmarshal(f.Params, &p); err != nil {
			t.Fatal("invalid item JSON in web-search capture")
		}
		if p.Item.Type != "webSearch" || p.Item.ID == "" {
			continue
		}
		if f.Method == "item/started" {
			started[p.Item.ID] = f
		} else if start, ok := started[p.Item.ID]; ok {
			t.Logf("webSearch pair observed: completion query nonempty=%v, action populated=%v", p.Item.Query != "", len(p.Item.Action) > 0 && string(p.Item.Action) != "null")
			return []captureFrame{start, f}
		}
	}
	t.Fatalf("no webSearch started/completed pair: %d frames, %d webSearch starts", len(frames), len(started))
	return nil
}

// captureTurn starts a thread and runs one turn on it until turn/completed,
// then waits briefly for trailing notifications.
func captureTurn(ctx context.Context, t *testing.T, c *Client, cwd string, sc captureScenario, notes chan captureFrame) {
	t.Helper()
	threadParams := map[string]any{
		// untrusted prompts for every command outside Codex's read-only allowlist.
		"cwd": cwd, "sandbox": "read-only", "model": captureModel, "approvalPolicy": "untrusted",
	}
	if sc.name == "web_search" {
		threadParams["config"] = map[string]string{"web_search": "live"}
	}
	var tr threadResult
	if err := c.call(ctx, methodThreadStart, threadParams, &tr); err != nil {
		t.Fatalf("%s: thread/start: %v", sc.name, err)
	}
	if _, err := c.setThread(methodThreadStart, tr); err != nil {
		t.Fatal(err)
	}
	turnParams := map[string]any{
		"threadId": tr.Thread.ID, "model": captureModel, "effort": captureEffort, "summary": "detailed",
		"input": []map[string]string{{"type": "text", "text": sc.prompt}},
	}
	var res struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := c.call(ctx, methodTurnStart, turnParams, &res); err != nil {
		t.Fatalf("%s: turn/start: %v", sc.name, err)
	}
	interrupted := false
	deadline := time.After(4 * time.Minute)
	for {
		select {
		case f := <-notes:
			if sc.interrupt && !interrupted && f.Method == "item/agentMessage/delta" {
				interrupted = true
				if err := c.Interrupt(ctx, res.Turn.ID); err != nil {
					t.Errorf("%s: interrupt: %v", sc.name, err)
				}
			}
			if f.Method == "turn/completed" {
				drain(notes, 2*time.Second)
				return
			}
		case <-deadline:
			t.Fatalf("%s: no turn/completed", sc.name)
		}
	}
}

// drain waits for trailing notifications until quiet for d.
func drain(notes chan captureFrame, d time.Duration) {
	for {
		select {
		case <-notes:
		case <-time.After(d):
			return
		}
	}
}

var accountKeyPrefix = []string{"account", "user", "email", "org", "workspace"}

// writeCapture scrubs account identity, machine identity and local paths from
// frames and writes them as one JSON object per line. codexHome is replaced
// before cwd and the user's home: its path can hold both, and a thread's
// rollout path lives under it.
func writeCapture(t *testing.T, name string, frames []captureFrame, cwd, codexHome string) {
	t.Helper()
	userHome, _ := os.UserHomeDir()
	type replacement struct{ from, to string }
	var repl []replacement
	for _, r := range []replacement{{codexHome, "/capture/codexhome"}, {cwd, "/capture/cwd"}, {userHome, "/capture/home"}} {
		if r.from == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(r.from); err == nil && resolved != r.from {
			repl = append(repl, replacement{resolved, r.to})
		}
		repl = append(repl, r)
	}
	var b strings.Builder
	for _, f := range frames {
		for _, raw := range []*json.RawMessage{&f.Params, &f.Answer} {
			if len(*raw) == 0 {
				continue
			}
			var v any
			if err := json.Unmarshal(*raw, &v); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			scrubbed, _ := json.Marshal(scrub(v))
			*raw = scrubbed
		}
		line, _ := json.Marshal(f)
		s := string(line)
		for _, r := range repl {
			s = strings.ReplaceAll(s, r.from, r.to)
		}
		b.WriteString(emailPattern.ReplaceAllString(s, "redacted@example.invalid"))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(captureDir, name+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scrub replaces the value of every key naming an account, user, email,
// organisation or workspace identity, and of every machineKeys key.
func scrub(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			lk := strings.ToLower(k)
			hit := machineKeys[k]
			for _, p := range accountKeyPrefix {
				if strings.HasPrefix(lk, p) || strings.HasSuffix(lk, p+"id") {
					hit = true
				}
			}
			if _, isStr := val.(string); hit && isStr {
				x[k] = "redacted"
				continue
			}
			x[k] = scrub(val)
		}
	case []any:
		for i := range x {
			x[i] = scrub(x[i])
		}
	}
	return v
}

// Account bootstrap metadata must never reach either publishable output.
func TestFocusedWebSearchCaptureKeepsMetadataPrivate(t *testing.T) {
	oldCaptureDir := captureDir
	captureDir = t.TempDir()
	t.Cleanup(func() { captureDir = oldCaptureDir })
	evidenceDir := t.TempDir()
	t.Setenv("PYRY_GATE_EVIDENCE_DIR", evidenceDir)
	frames := []captureFrame{
		{Kind: "notification", Method: "account/rateLimits/updated", Params: json.RawMessage(`{"private":"private-account-sentinel"}`)},
		{Kind: "notification", Method: "item/started", Params: json.RawMessage(`{"item":{"type":"webSearch","id":"ws-1","query":"","action":null,"results":null}}`)},
		{Kind: "notification", Method: "item/completed", Params: json.RawMessage(`{"item":{"type":"webSearch","id":"ws-1","query":"query","action":{"type":"search","query":"query"},"results":null}}`)},
	}
	private := writeFocusedWebSearchCapture(t, frames, "/capture/cwd", "/capture/codexhome")
	t.Cleanup(func() { _ = os.Remove(private) })
	info, err := os.Stat(private)
	if err != nil || info.Mode().Perm() != 0o600 || strings.HasPrefix(private, captureDir+string(os.PathSeparator)) || strings.HasPrefix(private, evidenceDir+string(os.PathSeparator)) {
		t.Fatal("complete recording did not remain in a private file outside published outputs")
	}
	raw, err := os.ReadFile(private)
	if err != nil || !strings.Contains(string(raw), "private-account-sentinel") {
		t.Fatal("complete private recording was not retained")
	}
	fixture, err := os.ReadFile(filepath.Join(captureDir, "web_search.jsonl"))
	if err != nil || strings.Contains(string(fixture), "private-account-sentinel") || len(readFrames(t, filepath.Join(captureDir, "web_search.jsonl"))) != 2 {
		t.Fatal("fixture must contain only the web-search pair")
	}
	durable, err := os.ReadFile(filepath.Join(evidenceDir, "web_search.jsonl"))
	if err != nil || string(durable) != string(fixture) {
		t.Fatal("durable evidence did not retain exactly the scrubbed pair")
	}
}
