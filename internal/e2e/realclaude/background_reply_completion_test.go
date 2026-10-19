//go:build e2e_realclaude

package realclaude

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
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
	rig := newBackgroundReplyOperation(t, work)
	settings, _ := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": backgroundReplyQuote(rig.hook)}}}}}})
	recorder := newDropcapRecorder()
	argvHandler, _ := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: bin, WorkDir: work, SessionID: "35890000-0000-4000-8000-000000000001",
		Args: append(append([]string(nil), ptucArgs...), "--settings", string(settings)), Stdout: recorder, Logger: slog.New(argvHandler),
	})
	if err != nil {
		t.Fatal("create runner")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = runner.Run(ctx) }()
	t.Cleanup(func() {
		rig.release()
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
	token := rand.Text()
	prompt := "Use Agent exactly once with run_in_background=true. Tell the Agent: use Bash to execute " + backgroundReplyQuote(rig.path) + ", then reply with exactly pyry-3589-finished. Do not use other tools in the parent, do not wait for the Agent, and immediately reply pyry-3589-launched."
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		t.Fatal("write rig prompt")
	}
	deadline := time.Now().Add(180 * time.Second)
	releaseIndex := -1
	var lines []dropcapCaptured
	var caps dropcapCaps
	var launch, mainEnd, final, childStart, childResult int
	var parent string
	var witness *backgroundReplyExecution
	for time.Now().Before(deadline) {
		lines, caps = recorder.snapshot()
		witness = rig.witness()
		launch, mainEnd, final, childStart, childResult, parent = backgroundReplyBoundary(lines, token, witness)
		if mainEnd > launch && childStart > launch && releaseIndex < 0 && rig.held(witness) {
			releaseIndex, err = backgroundReplyRelease(recorder, filepath.Join(rig.dir, "release"), token)
			if err != nil {
				t.Fatal("release rig child")
			}
		}
		if backgroundReplyReleased(mainEnd, releaseIndex, childResult, final) && childStart >= 0 && caps.UnterminatedPartial == 0 {
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
	t.Logf("background completion: captured=%d launch=%d main_end=%d release_index=%d child_start=%d child_result=%d final=%d lost_lines=%d lost_bytes=%d partials=%d", len(lines), launch, mainEnd, releaseIndex, childStart, childResult, final, caps.LinesOverCap, caps.BytesOverCap, caps.PartialsDropped)
	if launch < 0 || mainEnd <= launch || childStart <= launch || childStart >= childResult || !backgroundReplyReleased(mainEnd, releaseIndex, childResult, final) || parent == "" || caps.LinesOverCap != 0 || caps.BytesOverCap != 0 || caps.PartialsDropped != 0 || caps.UnterminatedPartial != 0 {
		t.Fatal("genuine loss-free background completion boundary was not observed")
	}
	replay, indices, alias, err := backgroundReplyReplay(t, lines, parent, token, final, witness)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal("read candidate revision")
	}
	record := struct {
		ClaudeVersion    string                    `json:"claude_version"`
		Revision         string                    `json:"revision"`
		CapturedAt       string                    `json:"captured_at"`
		SourceIndices    []int                     `json:"source_indices"`
		CapturedLines    int                       `json:"captured_lines"`
		OmittedLines     int                       `json:"omitted_lines"`
		Loss             dropcapCaps               `json:"loss"`
		Replay           []string                  `json:"replay"`
		Provenance       string                    `json:"provenance"`
		ChildStart       int                       `json:"child_start"`
		ReleaseIndex     int                       `json:"release_index"`
		ChildResult      int                       `json:"child_result"`
		Launch           int                       `json:"launch"`
		MainEnd          int                       `json:"main_end"`
		Final            int                       `json:"final"`
		Parent           string                    `json:"parent"`
		ProducingSession string                    `json:"producing_session"`
		Execution        *backgroundReplyExecution `json:"execution"`
		OS               string                    `json:"os"`
	}{probeClaudeVersion(bin), strings.TrimSpace(string(revision)), time.Now().UTC().Format(time.RFC3339), indices, len(lines), len(lines) - len(replay), caps, replay, "physical hold witness joined by hook-provided tool ID; explicitly synthetic commands replace observed commands; stable aliased IDs; persistent production parser verified at every source index", childStart, releaseIndex, childResult, launch, mainEnd, final, alias(parent), alias("35890000-0000-4000-8000-000000000001"), &backgroundReplyExecution{Operation: alias(witness.Operation), Tool: alias(witness.Tool)}, runtime.GOOS}
	safeLines := backgroundReplyTestLines(replay)
	l, m, f, s, r, p := backgroundReplyBoundary(safeLines, "rig-release-observed", record.Execution)
	if l != launch || m != mainEnd || f != final || s != childStart || r != childResult || p != alias(parent) {
		t.Fatal("scrubbing changed execution correlation")
	}
	blob, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal("encode replay")
	}
	if strings.Contains(string(blob), token) || strings.Contains(string(blob), rig.operation) {
		t.Fatal("private rig identity retained")
	}
	scanner := newDropcapScanner(home, dir, work)
	if hits, _ := scanner.scan(blob); len(hits) != 0 {
		t.Fatal("publish deny-scan rejected replay")
	}
	paths := []string{filepath.Join(dir, "background_reply_completion.json"), backgroundReplyFixture}
	// Only the deny-scanned replay leaves the private capture directory. The
	// gate retains this copy after its disposable candidate worktree is removed.
	if evidenceDir := os.Getenv("PYRY_GATE_EVIDENCE_DIR"); evidenceDir != "" {
		if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
			t.Fatal("create durable replay directory")
		}
		paths = append(paths, filepath.Join(evidenceDir, "background_reply_completion.json"))
	}
	for _, path := range paths {
		if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
			t.Fatal("write safe replay")
		}
	}
	t.Logf("safe replay artifact: %s", filepath.Join(dir, "background_reply_completion.json"))
}

func backgroundReplyBoundary(lines []dropcapCaptured, token string, witness *backgroundReplyExecution) (launch, mainEnd, final, childStart, childResult int, parent string) {
	launch, mainEnd, final, childStart, childResult = -1, -1, -1, -1, -1
	if witness == nil || witness.Operation == "" || witness.Tool == "" {
		return
	}
	type frame struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Parent  string `json:"parent_tool_use_id"`
		Message struct {
			ID      string `json:"id"`
			Role    string `json:"role"`
			Content []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Text      string          `json:"text"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
				Input     struct {
					Background bool `json:"run_in_background"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	frames := make([]frame, len(lines))
	agents, tools := 0, 0
	for i, line := range lines {
		v := &frames[i]
		if json.Unmarshal(line.Raw, v) != nil || v.Type != "assistant" || v.Message.ID == "" || v.Message.Role != "assistant" {
			continue
		}
		for _, b := range v.Message.Content {
			if b.Type != "tool_use" {
				continue
			}
			if b.Name == "Agent" && v.Parent == "" {
				agents++
				if b.Input.Background && b.ID != "" {
					launch, parent = i, b.ID
				}
			}
			if b.ID == witness.Tool {
				tools++
			}
		}
	}
	if agents != 1 || launch < 0 || tools != 1 {
		launch, parent = -1, ""
		return
	}
	for i, v := range frames {
		for _, b := range v.Message.Content {
			if v.Type == "assistant" && v.Message.Role == "assistant" && v.Message.ID != "" && v.Parent == parent && b.Type == "tool_use" && b.Name == "Bash" && b.ID == witness.Tool && i > launch {
				childStart = i
			}
		}
	}
	if childStart < 0 {
		launch, parent = -1, ""
		return
	}
	for i, v := range frames {
		if v.Type == "result" && v.Subtype == "success" && v.Parent == "" && mainEnd < 0 {
			mainEnd = i
		}
		if v.Type == "user" && v.Parent == parent {
			for _, b := range v.Message.Content {
				var output string
				if b.Type == "tool_result" && b.ToolUseID == witness.Tool && !b.IsError && json.Unmarshal(b.Content, &output) == nil && strings.TrimSpace(output) == token && childResult < 0 {
					childResult = i
				}
			}
		}
		if v.Type != "assistant" || v.Message.ID == "" || v.Message.Role != "assistant" {
			continue
		}
		for _, b := range v.Message.Content {
			if parent != "" && v.Parent == parent {
				if b.Type == "text" && strings.TrimSpace(b.Text) == "pyry-3589-finished" && final < 0 {
					final = i
				}
			}
		}
	}
	return
}

// The recorder lock places release between complete source lines. A line already
// partially recorded is excluded from the post-release side of the boundary.
func backgroundReplyRelease(r *dropcapRecorder, path, token string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := r.seen
	if len(r.partial) != 0 {
		index++
	}
	return index, os.WriteFile(path, []byte(token+"\n"), 0o600)
}

func backgroundReplyReleased(mainEnd, releaseIndex, childResult, final int) bool {
	return mainEnd >= 0 && releaseIndex > mainEnd && childResult >= releaseIndex && final > childResult
}
