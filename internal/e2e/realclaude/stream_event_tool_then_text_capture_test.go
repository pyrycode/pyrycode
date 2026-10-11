//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

const tttPrompt = "Run this exact shell command with your tools: echo pyry1311. Then reply with one short sentence saying what it printed."
const tttFixturePrefix = "testdata/tool_then_text_stream_event_v"

type tttLoss struct {
	Lines    int `json:"lines_dropped_over_cap"`
	Bytes    int `json:"bytes_dropped_over_cap"`
	Partials int `json:"partials_dropped"`
	Tail     int `json:"unterminated_tail_bytes"`
}

type tttRecord struct {
	Ticket            string                `json:"ticket"`
	CapturedAt        string                `json:"captured_at"`
	ClaudeVersion     string                `json:"claude_version"`
	DaemonRevision    string                `json:"daemon_revision"`
	DaemonDirty       bool                  `json:"daemon_dirty"`
	MobileRevision    string                `json:"mobile_revision"`
	Assumptions       string                `json:"settings_assumptions"`
	SpawnShape        []string              `json:"spawn_shape"`
	Prompt            string                `json:"prompt"`
	Outcome           string                `json:"outcome"`
	RejectedRows      []int                 `json:"rejected_rows"`
	ToolStarts        int                   `json:"completed_tool_starts"`
	ToolCompletions   int                   `json:"tool_completions"`
	TextChunks        int                   `json:"text_chunks"`
	Rows              []json.RawMessage     `json:"ordered_stdout"`
	IdentityRedaction string                `json:"identity_redaction"`
	Loss              tttLoss               `json:"capture_loss"`
	Redaction         []dropcapSubstitution `json:"redaction"`
	ScanApplied       map[string]bool       `json:"credential_scan_applied"`
	ScanSkipped       []string              `json:"credential_scan_skipped"`
}

func (r tttRecord) complete() bool {
	if r.Loss != (tttLoss{}) || len(r.Rows) == 0 {
		return false
	}
	for _, row := range r.Rows {
		if !json.Valid(row) {
			return false
		}
	}
	return secapTypeField(r.Rows[len(r.Rows)-1]) == "result"
}

// tttRedactRows registers dynamic paths and identities before applying one shared
// substitution table. Raw rows retain their JSON types, key presence and order.
func tttRedactRows(rows []json.RawMessage, red *dropcapRedactor, scanner *dropcapScanner) []json.RawMessage {
	identities := map[string]string{secapSessionID: "$SESSION_ID"}
	var visit func(any, string)
	visit = func(value any, parentKey string) {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if s, ok := child.(string); ok && s != "" {
					if strings.HasPrefix(s, "/") && strings.Trim(s, "/") != "" {
						class, replacement := "dynamic_path", "$DYNAMIC_PATH"
						if key == "messaging_socket_path" {
							class, replacement = dropcapClassMessagingSocket, "$MESSAGING_SOCKET"
						}
						red.addPathClass(class, replacement, s)
						scanner.addDynamicPath(class, s)
					}
					if (key == "id" && (parentKey == "message" || value["type"] == "tool_use" || value["type"] == "control_response")) || key == "uuid" || key == "session_id" || key == "message_id" ||
						key == "parent_tool_use_id" || key == "tool_use_id" || key == "request_id" {
						if _, exists := identities[s]; !exists {
							identities[s] = fmt.Sprintf("$IDENTITY_%d", len(identities)+1)
						}
						value[key] = identities[s]
					}
				}
				visit(child, key)
			}
		case []any:
			for _, child := range value {
				visit(child, parentKey)
			}
		}
	}
	out := make([]json.RawMessage, len(rows))
	for i, row := range rows {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(row))
		decoder.UseNumber()
		out[i] = append([]byte(nil), row...)
		if decoder.Decode(&value) == nil {
			visit(value, "")
			if encoded, err := json.Marshal(value); err == nil {
				out[i] = encoded
			}
		}
	}
	for i, row := range out {
		out[i] = red.redact(row)
	}
	return out
}

func tttReplay(t *testing.T, rows []json.RawMessage) ([]turnevent.Event, []int) {
	t.Helper()
	var events []turnevent.Event
	rejected := []int{}
	index := 0
	parser := streamsup.NewParser(func(ev turnevent.Event) {
		events = append(events, ev)
		if _, ok := ev.(turnevent.Unrecognized); ok {
			rejected = append(rejected, index)
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i, row := range rows {
		index = i
		if _, err := parser.Write(append(append([]byte(nil), row...), '\n')); err != nil {
			t.Fatal("replay write failed")
		}
	}
	return events, rejected
}

func tttCapture(t *testing.T) {
	t.Helper()
	if fixtures, _ := filepath.Glob(tttFixturePrefix + "*.json"); len(fixtures) > 0 && os.Getenv(secapEnableEnv) != "1" {
		t.Skip("tool-then-text fixture exists; set PYRY_PROBE_STREAM_EVENT_CAPTURE=1 to recapture")
	}
	budget, reason := secapTurnBudgetWithin(t)
	if reason != "" {
		t.Fatal(reason)
	}
	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t)
	workdir := filepath.Join(home, "tool-then-text")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal("create empty capture workdir")
	}
	dir, err := os.MkdirTemp("", "pyry-2939-capture-")
	if err != nil {
		t.Fatal("create durable capture directory")
	}
	red := newDropcapRedactor(home, dir, workdir, "", secapSessionID, time.Now().UnixNano())
	scanner := newDropcapScanner(home, dir, workdir)
	rec := tttRecord{
		Ticket: "2939", CapturedAt: time.Now().UTC().Format(time.RFC3339),
		ClaudeVersion: probeClaudeVersion(claudeBin), Prompt: tttPrompt,
		MobileRevision: "not driven; historical failure at 5de4f4459",
		Assumptions:    "Historical mobile model/effort/permission are unknown. Isolated attempt uses no model or effort override, fresh HOME without user configuration, and --allowedTools Bash for the test-owned echo. Effective model/permission and effort key presence are retained in system/init; absent effort is unknown. This does not establish historical configuration equivalence.",
		Outcome:        "incomplete", RejectedRows: []int{}, Rows: []json.RawMessage{},
		IdentityRedaction: "Identity-valued JSON fields use one consistent $IDENTITY_N mapping; other string fields, including string-valued indices, retain their values.",
	}
	if revision, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		rec.DaemonRevision = strings.TrimSpace(string(revision))
	} else {
		t.Fatal("observe daemon revision")
	}
	if dirty, err := exec.Command("git", "status", "--porcelain").Output(); err == nil {
		rec.DaemonDirty = len(dirty) > 0
	} else {
		t.Fatal("observe daemon working tree state")
	}
	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	parser := streamsup.NewParser(func(turnevent.Event) {}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin, WorkDir: workdir, SessionID: secapSessionID,
		Args: []string{"--allowedTools", "Bash"}, Stdout: io.MultiWriter(parser, recorder),
		ControlParser: parser, RequestInitializeOnSpawn: true, Logger: slog.New(argvHandler),
	})
	if err != nil {
		t.Fatal("construct production capture runner")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runner.Run(ctx) // cancellation is the probe's deliberate shutdown
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(secapRunExitWait):
			t.Error("capture runner did not stop; refusing concurrent snapshot")
			return
		}
		lines, caps := recorder.snapshot()
		rec.Loss = tttLoss{caps.LinesOverCap, caps.BytesOverCap, caps.PartialsDropped, caps.UnterminatedPartial}
		for _, line := range lines {
			rec.Rows = append(rec.Rows, json.RawMessage(line.Raw))
			if line.Type == "result" {
				break
			}
		}
		rec.Rows = tttRedactRows(rec.Rows, red, &scanner)
		rec.SpawnShape = red.strs(argv())
		events, rejected := tttReplay(t, rec.Rows)
		rec.RejectedRows = rejected
		for _, ev := range events {
			switch ev := ev.(type) {
			case turnevent.ToolStart:
				rec.ToolStarts++
			case turnevent.ToolUpdate:
				if ev.Status == turnevent.ToolStatusCompleted {
					rec.ToolCompletions++
				}
			case turnevent.TextChunk:
				rec.TextChunks++
			}
		}
		if rec.complete() {
			rec.Outcome = "not_reproduced; ordered capture requires named mobile drive"
			if len(rec.RejectedRows) > 0 {
				rec.Outcome = "reproduced; inspect rejecting guards before production repair"
			}
		}
		tttWriteRecord(t, dir, red, scanner, &rec)
		if rec.ToolStarts != 1 || rec.ToolCompletions != 1 || rec.TextChunks == 0 {
			t.Errorf("scenario staging failed: starts=%d completions=%d text_chunks=%d", rec.ToolStarts, rec.ToolCompletions, rec.TextChunks)
		}
	})
	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		t.Fatal("no live child for capture")
	}
	if err := streamsup.WriteTurn(ctx, stdin, []byte(tttPrompt)); err != nil {
		t.Fatal("write exact tool-then-text prompt")
	}
	select {
	case <-recorder.resultSeen:
	case <-time.After(budget):
		t.Fatal("capture did not reach result")
	}
}

func tttWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *tttRecord) {
	t.Helper()
	rec.Redaction, rec.ScanApplied = red.substitutions(), scanner.applied()
	blob, err := json.Marshal(rec)
	if err != nil {
		t.Error("marshal ordered capture failed")
		return
	}
	hits, skipped := scanner.scan(blob)
	rec.ScanSkipped = skipped
	if len(hits) > 0 {
		t.Errorf("capture deny-scan rejected classes %v; nothing written", hits)
		return
	}
	blob, err = json.Marshal(rec)
	if err != nil {
		t.Error("marshal scan metadata failed")
		return
	}
	if hits, _ := scanner.scan(blob); len(hits) > 0 {
		t.Errorf("final capture deny-scan rejected classes %v", hits)
		return
	}
	path := filepath.Join(dir, "tool_then_text_record.json")
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Error("write durable recovery record failed")
		return
	}
	t.Logf("#2939 recovery=%s rows=%d rejected_rows=%v loss=%+v outcome=%s", path, len(rec.Rows), rec.RejectedRows, rec.Loss, rec.Outcome)
	if !rec.complete() {
		t.Error("incomplete or lossy capture; refusing fixture promotion")
		return
	}
	if rec.ToolStarts != 1 || rec.ToolCompletions != 1 || rec.TextChunks == 0 {
		t.Log("scenario was not staged; retaining recovery record without promoting a fixture")
		return
	}
	path, err = secapFixturePath(rec.ClaudeVersion)
	if err != nil {
		t.Error("unsafe Claude version; refusing fixture promotion")
		return
	}
	path = strings.Replace(path, secapFixturePrefix, tttFixturePrefix, 1)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Error("write scenario fixture failed")
		return
	}
	t.Logf("#2939 fixture=%s; commit this ordered capture", path)
}
