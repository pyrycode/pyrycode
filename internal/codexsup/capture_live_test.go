//go:build codex_capture

package codexsup

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"
)

// TestCaptureLive records live Codex frames with the codex_capture build tag:
//
//	go test -tags codex_capture -run TestCaptureLive ./internal/codexsup/
//
// It needs PYRY_CODEX_CAPTURE_BIN (a Codex 0.156.1 binary) and
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
