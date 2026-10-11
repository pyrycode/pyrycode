//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const tttPrompt = "Run this exact shell command with your tools: echo pyry1311. Then reply with one short sentence saying what it printed."
const tttFixturePrefix = "testdata/tool_then_text_wire_stream_event_v"

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
	ReplayMatches     bool                  `json:"retained_replay_matches_original"`
	RejectedRows      []int                 `json:"rejected_rows"`
	ToolStarts        int                   `json:"completed_tool_starts"`
	ToolCompletions   int                   `json:"tool_completions"`
	TextChunks        int                   `json:"text_chunks"`
	Rows              []string              `json:"ordered_stdout"`
	IdentityRedaction string                `json:"identity_redaction"`
	Loss              tttLoss               `json:"capture_loss"`
	Redaction         []dropcapSubstitution `json:"redaction"`
	ScanApplied       map[string]bool       `json:"credential_scan_applied"`
	ScanSkipped       []string              `json:"credential_scan_skipped"`
}

func (r tttRecord) complete() bool {
	if !r.ReplayMatches || r.Loss != (tttLoss{}) || len(r.Rows) == 0 {
		return false
	}
	for _, row := range r.Rows {
		if !json.Valid([]byte(row)) {
			return false
		}
	}
	return secapTypeField([]byte(r.Rows[len(r.Rows)-1])) == "result"
}

// tttRedactRows edits only string-token spans. Duplicate keys, numeric spellings,
// whitespace and invalid trailing bytes remain intact for rejection replay.
func tttRedactRows(rows []string, red *dropcapRedactor, scanner *dropcapScanner) []string {
	identities := map[string]string{secapSessionID: "$SESSION_ID"}
	type span struct {
		start, end int
		value      string
		identity   bool
	}
	spans := make([][]span, len(rows))
	for i, row := range rows {
		decoder := json.NewDecoder(strings.NewReader(row))
		decoder.UseNumber()
		read := func(identity bool, key string) (json.Token, error) {
			before := int(decoder.InputOffset())
			token, err := decoder.Token()
			if value, ok := token.(string); err == nil && ok {
				end := int(decoder.InputOffset())
				start := before + strings.IndexByte(row[before:end], '"')
				spans[i] = append(spans[i], span{start, end, value, identity})
				if identity && value != "" {
					if _, exists := identities[value]; !exists {
						identities[value] = fmt.Sprintf("$IDENTITY_%d", len(identities)+1)
					}
				}
				if strings.HasPrefix(value, "/") && strings.Trim(value, "/") != "" {
					class, replacement := "dynamic_path", "$DYNAMIC_PATH"
					if key == "messaging_socket_path" {
						class, replacement = dropcapClassMessagingSocket, "$MESSAGING_SOCKET"
					}
					red.addPathClass(class, replacement, value)
					scanner.addDynamicPath(class, value)
				}
			}
			return token, err
		}
		var walk func(string, bool) error
		walk = func(parent string, identity bool) error {
			token, err := read(identity, parent)
			if err != nil {
				return err
			}
			switch token {
			case json.Delim('{'):
				for decoder.More() {
					token, err := read(parent == "wire_tool_inputs", "")
					if err != nil {
						return err
					}
					key := token.(string) // object keys returned by Token are strings
					identity := key == "id" || key == "uuid" || key == "session_id" || key == "message_id" ||
						key == "parent_tool_use_id" || key == "tool_use_id" || key == "request_id"
					if err := walk(key, identity); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
			case json.Delim('['):
				for decoder.More() {
					if err := walk(parent, identity); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
			}
			return err
		}
		_ = walk("", false) // malformed input stays malformed; deny-scan gates recovery
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		var retained strings.Builder
		offset := 0
		for _, span := range spans[i] {
			value := span.value
			if span.identity && value != "" {
				value = identities[value]
			} else {
				value = red.str(value)
			}
			retained.WriteString(row[offset:span.start])
			if value == span.value {
				retained.WriteString(row[span.start:span.end])
			} else {
				encoded, _ := json.Marshal(value) // a string always marshals
				retained.Write(encoded)
			}
			offset = span.end
		}
		retained.WriteString(row[offset:])
		out[i] = retained.String()
	}
	return out
}

func tttReplay(t *testing.T, rows []string) ([]turnevent.Event, []int) {
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
		if _, err := parser.Write(append([]byte(row), '\n')); err != nil {
			t.Fatal("replay write failed")
		}
	}
	return events, rejected
}

func tttRejections(events []turnevent.Event) []string {
	out := []string{}
	for _, ev := range events {
		if ev, ok := ev.(turnevent.Unrecognized); ok {
			out = append(out, fmt.Sprintf("%s/%s/%t", ev.Site, ev.Kind, ev.Truncated))
		}
	}
	return out
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
		Outcome:        "incomplete", RejectedRows: []int{}, Rows: []string{},
		IdentityRedaction: "Identity-valued JSON fields and wire_tool_inputs keys use one consistent $IDENTITY_N mapping. String-token edits preserve duplicate keys, order, index shapes and trailing bytes.",
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
	liveRejections := []string{}
	ended := false
	parser := streamsup.NewParser(func(ev turnevent.Event) {
		if !ended {
			liveRejections = append(liveRejections, tttRejections([]turnevent.Event{ev})...)
		}
		if _, ok := ev.(turnevent.TurnEnd); ok {
			ended = true
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
			rec.Rows = append(rec.Rows, string(line.Raw))
			if line.Type == "result" {
				break
			}
		}
		events, rejected := tttReplay(t, rec.Rows)
		rec.RejectedRows = rejected // census of original ordered wire rows
		rec.Rows = tttRedactRows(rec.Rows, red, &scanner)
		rec.SpawnShape = red.strs(argv())
		retainedEvents, retainedRejected := tttReplay(t, rec.Rows)
		rec.ReplayMatches = reflect.DeepEqual(rejected, retainedRejected) &&
			reflect.DeepEqual(tttRejections(events), tttRejections(retainedEvents)) &&
			reflect.DeepEqual(liveRejections, tttRejections(events))
		if !rec.ReplayMatches {
			t.Error("original live/replay and retained rejection censuses differ; refusing promotion")
		}
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
