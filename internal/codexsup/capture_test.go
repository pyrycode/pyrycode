package codexsup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureModel and captureEffort are set on every captured turn; the thread
// never inherits a default.
const (
	captureModel  = "gpt-6-luna"
	captureEffort = "low"
)

// captureDir holds the live frames TestCaptureLive records. The translator
// tests replay them; #2609 reuses the tool items.
var captureDir = filepath.Join("testdata", "capture")

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

// TestCaptureLive records live Codex frames. It never runs under make check:
// it needs PYRY_CODEX_CAPTURE_BIN (a Codex 0.156.1 binary) and
// PYRY_CODEX_CAPTURE_HOME (an isolated CODEX_HOME holding only a minimal
// config and the sign-in). It spends Codex allowance.
func TestCaptureLive(t *testing.T) {
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
		writeCapture(t, sc.name, got, cwd, home)
	}
}

// isExactCommand reports whether a command approval asks for exactly want:
// one parsed action equal to want, run bare or through a login shell's -lc.
// A chained command parses as one action holding the whole chain, so it fails.
func isExactCommand(params json.RawMessage, want string) bool {
	var p struct {
		Command        string `json:"command"`
		CommandActions []struct {
			Command string `json:"command"`
		} `json:"commandActions"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.CommandActions) != 1 || p.CommandActions[0].Command != want {
		return false
	}
	return p.Command == want || strings.HasSuffix(p.Command, " -lc '"+want+"'")
}

// allUnder reports whether every path, relative paths taken from root, sits
// under root. An empty change set is not accepted.
func allUnder(root string, paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, resolved)
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		p = filepath.Clean(p)
		under := false
		for _, r := range roots {
			if rel, err := filepath.Rel(r, p); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
				under = true
			}
		}
		if !under {
			return false
		}
	}
	return true
}

// captureTurn starts a thread and runs one turn on it until turn/completed,
// then waits briefly for trailing notifications.
func captureTurn(ctx context.Context, t *testing.T, c *Client, cwd string, sc captureScenario, notes chan captureFrame) {
	t.Helper()
	threadParams := map[string]any{
		// The granular policy needs the experimentalApi capability, which the
		// handshake does not declare; untrusted prompts for every command that
		// is not on Codex's read-only allowlist.
		"cwd": cwd, "sandbox": "read-only", "model": captureModel, "approvalPolicy": "untrusted",
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

var (
	emailPattern     = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	accountKeyPrefix = []string{"account", "user", "email", "org", "workspace"}
	// machineKeys name the machine rather than an account: remoteControl's
	// hostname and installation id.
	machineKeys = map[string]bool{"serverName": true, "installationId": true}
)

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

// TestCaptureFixturesScrubbed fails when a committed capture holds a local
// path, an email or a machine identity, so a recapture's scrub is checked as
// code rather than by a manual grep.
func TestCaptureFixturesScrubbed(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(captureDir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no capture files: %v", err)
	}
	leaks := []string{"/private/", "/Users/", "-Users-", "/home/", "/var/folders/"}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(data)
		for _, l := range leaks {
			if strings.Contains(s, l) {
				t.Errorf("%s: contains local path fragment %q", f, l)
			}
		}
		for _, m := range emailPattern.FindAllString(s, -1) {
			if m != "redacted@example.invalid" {
				t.Errorf("%s: contains email %q", f, m)
			}
		}
		for k := range machineKeys {
			re := regexp.MustCompile(`"` + k + `":"([^"]*)"`)
			for _, m := range re.FindAllStringSubmatch(s, -1) {
				if m[1] != "redacted" {
					t.Errorf("%s: %s = %q, want redacted", f, k, m[1])
				}
			}
		}
	}
}

func TestCaptureApprovalPredicates(t *testing.T) {
	cmd := func(command, action string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{
			"command": command, "commandActions": []map[string]string{{"command": action, "type": "unknown"}},
		})
		return raw
	}
	for _, tc := range []struct {
		name   string
		params json.RawMessage
		want   bool
	}{
		{"shell wrapped", cmd("/bin/zsh -lc 'touch accepted.txt'", "touch accepted.txt"), true},
		{"bare", cmd("touch accepted.txt", "touch accepted.txt"), true},
		{"chained", cmd("/bin/zsh -lc 'rm -rf x && touch accepted.txt'", "rm -rf x && touch accepted.txt"), false},
		{"action mismatch", cmd("/bin/zsh -lc 'touch accepted.txt'", "touch other.txt"), false},
		{"command mismatch", cmd("/bin/zsh -lc 'curl x'", "touch accepted.txt"), false},
	} {
		if got := isExactCommand(tc.params, "touch accepted.txt"); got != tc.want {
			t.Errorf("isExactCommand %s = %v, want %v", tc.name, got, tc.want)
		}
	}

	root := t.TempDir()
	for _, tc := range []struct {
		name  string
		paths []string
		want  bool
	}{
		{"relative", []string{"notes.txt"}, true},
		{"absolute under", []string{filepath.Join(root, "sub", "notes.txt")}, true},
		{"escape", []string{"../notes.txt"}, false},
		{"root itself", []string{root}, false},
		{"outside", []string{"/etc/passwd"}, false},
		{"one outside", []string{"notes.txt", "/etc/passwd"}, false},
		{"empty", nil, false},
	} {
		if got := allUnder(root, tc.paths); got != tc.want {
			t.Errorf("allUnder %s = %v, want %v", tc.name, got, tc.want)
		}
	}
}
