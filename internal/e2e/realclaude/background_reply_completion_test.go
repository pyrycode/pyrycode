//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const backgroundReplyFixture = "testdata/background_reply_completion.json"

// This scenario always runs under the live gate. Raw stdout stays in the private
// artifact directory; the published replay contains only rig-authored values.
func TestRealClaude_BackgroundReplyCompletion(t *testing.T) {
	bin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t)
	dir, err := os.MkdirTemp("", "pyry-3589-private-")
	if err != nil {
		t.Fatal("create private artifacts")
	}
	work := filepath.Join(home, "background-reply-rig")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal("create rig directory")
	}
	recorder := newDropcapRecorder()
	argvHandler, _ := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: bin, WorkDir: work, SessionID: "35890000-0000-4000-8000-000000000001",
		Args: ptucArgs, Stdout: recorder, Logger: slog.New(argvHandler),
	})
	if err != nil {
		t.Fatal("create runner")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = runner.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(ptucRunExitWait):
			t.Error("runner shutdown timed out")
		}
	})
	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		t.Fatal("runner did not start")
	}
	prompt := "Use Agent exactly once with run_in_background=true. Tell the Agent: use Bash to run `while [ ! -f release ]; do sleep 0.1; done; printf pyry-3589-released`, then reply with exactly pyry-3589-finished. Do not use other tools in the parent, do not wait for the Agent, and immediately reply pyry-3589-launched."
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		t.Fatal("write rig prompt")
	}
	deadline := time.Now().Add(180 * time.Second)
	released := false
	var lines []dropcapCaptured
	var caps dropcapCaps
	var launch, mainEnd, final, childStart int
	var parent string
	for time.Now().Before(deadline) {
		lines, caps = recorder.snapshot()
		launch, mainEnd, final, childStart, parent = backgroundReplyBoundary(lines)
		if mainEnd >= 0 && !released {
			if err := os.WriteFile(filepath.Join(work, "release"), []byte("release\n"), 0o600); err != nil {
				t.Fatal("release rig child")
			}
			released = true
		}
		if released && final > mainEnd && childStart >= 0 && caps.UnterminatedPartial == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Preserve original evidence privately even on a failed observation.
	var raw strings.Builder
	for _, line := range lines {
		raw.Write(line.Raw)
		raw.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout-private.jsonl"), []byte(raw.String()), 0o600); err != nil {
		t.Fatal("retain private stdout")
	}
	t.Logf("background completion: captured=%d launch=%d main_end=%d child_start=%d final=%d lost_lines=%d lost_bytes=%d partials=%d", len(lines), launch, mainEnd, childStart, final, caps.LinesOverCap, caps.BytesOverCap, caps.PartialsDropped)
	if launch < 0 || mainEnd <= launch || childStart <= launch || childStart >= final || final <= mainEnd || parent == "" || caps.LinesOverCap != 0 || caps.BytesOverCap != 0 || caps.PartialsDropped != 0 || caps.UnterminatedPartial != 0 {
		t.Fatal("genuine loss-free background completion boundary was not observed")
	}
	// Replay the observed final envelope through the production parser. It must
	// carry its own completion, independent of a task status or parent result.
	var complete bool
	p := streamsup.NewParser(func(ev turnevent.Event) {
		if text, ok := ev.(turnevent.TextChunk); ok {
			b, _ := json.Marshal(text)
			var fields map[string]any
			_ = json.Unmarshal(b, &fields)
			complete = text.ParentToolCallID == parent && fields["TextComplete"] == true
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := p.Write(append(append([]byte(nil), lines[final].Raw...), '\n')); err != nil {
		t.Fatal("parse captured final")
	}
	if !complete {
		t.Fatal("observed final envelope did not carry intrinsic completion")
	}
	// Retain exact discriminators, source indices and event ordering, replacing
	// all model-authored content/identifiers with fixed replay values.
	replay := []string{
		`{"type":"assistant","message":{"id":"launch","role":"assistant","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
		`{"type":"result","subtype":"success"}`,
		`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"final","role":"assistant","content":[{"type":"text","text":"final reply"}]}}`,
	}
	revision, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal("read candidate revision")
	}
	record := struct {
		ClaudeVersion     string      `json:"claude_version"`
		Revision          string      `json:"revision"`
		CapturedAt        string      `json:"captured_at"`
		SourceIndices     []int       `json:"source_indices"`
		CapturedLines     int         `json:"captured_lines"`
		OmittedLines      int         `json:"omitted_lines"`
		Loss              dropcapCaps `json:"loss"`
		Replay            []string    `json:"replay"`
		Provenance        string      `json:"provenance"`
		ChildStart        int         `json:"child_start"`
		ReleasedAfterMain bool        `json:"released_after_main"`
	}{probeClaudeVersion(bin), strings.TrimSpace(string(revision)), time.Now().UTC().Format(time.RFC3339), []int{launch, mainEnd, final}, len(lines), len(lines) - 3, caps, replay, "rig-authored allowlist replay of observed assistant/tool_use, result/success, attributed assistant/text; private original retained separately; IDs and prose replaced; unrelated frames omitted", childStart, released}
	blob, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal("encode replay")
	}
	scanner := newDropcapScanner(home, dir, work)
	if hits, _ := scanner.scan(blob); len(hits) != 0 {
		t.Fatal("publish deny-scan rejected replay")
	}
	for _, path := range []string{filepath.Join(dir, "background_reply_completion.json"), backgroundReplyFixture} {
		if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
			t.Fatal("write safe replay")
		}
	}
	t.Logf("safe replay artifact: %s", filepath.Join(dir, "background_reply_completion.json"))
}

func backgroundReplyBoundary(lines []dropcapCaptured) (launch, mainEnd, final, childStart int, parent string) {
	launch, mainEnd, final, childStart = -1, -1, -1, -1
	for i, line := range lines {
		var v struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Parent  string `json:"parent_tool_use_id"`
			Message struct {
				ID      string `json:"id"`
				Role    string `json:"role"`
				Content []struct {
					Type  string `json:"type"`
					ID    string `json:"id"`
					Name  string `json:"name"`
					Text  string `json:"text"`
					Input struct {
						Background bool   `json:"run_in_background"`
						Command    string `json:"command"`
					} `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line.Raw, &v) != nil {
			continue
		}
		if v.Type == "result" && v.Subtype == "success" && mainEnd < 0 {
			mainEnd = i
		}
		if v.Type != "assistant" || v.Message.ID == "" || v.Message.Role != "assistant" {
			continue
		}
		for _, b := range v.Message.Content {
			if b.Type == "tool_use" && b.Name == "Agent" && b.Input.Background && v.Parent == "" && launch < 0 {
				launch, parent = i, b.ID
			}
			if parent != "" && v.Parent == parent {
				if b.Type == "tool_use" && b.Name == "Bash" && strings.Contains(b.Input.Command, "release") {
					childStart = i
				}
				if b.Type == "text" && strings.TrimSpace(b.Text) == "pyry-3589-finished" {
					final = i
				}
			}
		}
	}
	return
}
