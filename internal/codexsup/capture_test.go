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
	touchAccepted := func(want string) func(string, json.RawMessage) bool {
		return func(method string, params json.RawMessage) bool {
			return method == methodCommandApproval && strings.Contains(string(params), want)
		}
	}
	scenarios := []captureScenario{
		{name: "plain", prompt: "Reply with exactly the word: hello"},
		{name: "reasoning", prompt: "Think it through, then answer with the number only: what is 17 times 23?"},
		{name: "command_accepted", prompt: "Use your shell tool to run `touch accepted.txt` in the current directory, then reply done.",
			accept: touchAccepted("touch accepted.txt")},
		{name: "command_declined", prompt: "Use your shell tool to run `touch declined.txt` in the current directory, then reply done.",
			accept: func(string, json.RawMessage) bool { return false }},
		{name: "file_edit", prompt: "Create the file notes.txt containing the single line hi, using your file editing tool, then reply done.",
			accept: func(method string, params json.RawMessage) bool {
				var p struct {
					GrantRoot string `json:"grantRoot"`
				}
				_ = json.Unmarshal(params, &p)
				return method == methodFileChangeApproval && p.GrantRoot == "" ||
					method == methodCommandApproval && strings.Contains(string(params), "notes.txt")
			}},
		{name: "interrupted", prompt: "Write the numbers from 1 to 300, one per line.", interrupt: true},
	}

	var (
		mu       sync.Mutex
		frames   []captureFrame
		scenario captureScenario
	)
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
		writeCapture(t, sc.name, got, cwd)
	}
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
)

// writeCapture scrubs account identity and local paths from frames and writes
// them as one JSON object per line.
func writeCapture(t *testing.T, name string, frames []captureFrame, cwd string) {
	t.Helper()
	home, _ := os.UserHomeDir()
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
		s := strings.ReplaceAll(string(line), cwd, "/capture/cwd")
		if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
			s = strings.ReplaceAll(s, resolved, "/capture/cwd")
		}
		if home != "" {
			s = strings.ReplaceAll(s, home, "/capture/home")
		}
		b.WriteString(emailPattern.ReplaceAllString(s, "redacted@example.invalid"))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(captureDir, name+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scrub replaces the value of every key naming an account, user, email,
// organisation or workspace identity.
func scrub(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			lk := strings.ToLower(k)
			hit := false
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
