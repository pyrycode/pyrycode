//go:build e2e_realclaude

package realclaude

// Evidence capture for #2658 — one live turn that spawns one foreground
// general-purpose subagent under the daemon's OWN spawn flags, so the line claude
// opens the subagent with can be pinned against bytes it actually sent.
//
// # Why a second subagent capture
//
// parent_tool_use_v2.1.259.json holds a subagent turn, but it was taken before
// #2192 added --forward-subagent-text to the spawn argv. Under that flag claude
// 2.1.280 writes the subagent's DELEGATED PROMPT as a user line holding one text
// block, stamped with the spawning Agent call's parent_tool_use_id — a line the
// no-flag capture cannot contain. Every spawn surfaced it to clients as an
// unrecognized_message row (found by pyrycode-mobile#1076's live run). That fixture
// pins the no-flag shape for its own reader and is left alone; this one is new.
//
// # Staging
//
// #2191's staging, reused rather than re-derived: one turn, one child, a fresh
// non-git workdir holding ptucFiles, a subagent asked to read both. The reads give
// the subagent real tool work, so the reader can prove the drop does not take the
// subagent's own ToolStart/ToolUpdate frames with it. The child is spawned by
// streamsup.New, which adds --forward-subagent-text itself — the rig names only the
// model and the approval posture, so the capture carries the daemon's flags by
// construction and spawn_shape records them.
//
// Reused: ptucRecord and ptucCollect (the frames and the Agent-call bookkeeping),
// ptucAwaitTurn, ptucReadLine, ptucFiles, ptucMinAttributed, and the dropcap*
// recorder, redactor, scanner and child wait.
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine — the gate is the FIXTURE'S
// ABSENCE, for the reason TestRealClaude_ParentToolUseCapture's gate comment gives.
// To force a re-capture over an existing fixture:
//
//	PYRY_PROBE_SUBAGENT_PROMPT_CAPTURE=1 go test -tags e2e_realclaude -timeout 20m -v \
//	  -run '^TestRealClaude_SubagentPromptCapture$' ./internal/e2e/realclaude/

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

const spcEnableEnv = "PYRY_PROBE_SUBAGENT_PROMPT_CAPTURE"

// The version is spliced into the path; the streamsup reader pins the same one.
const (
	spcFixtureVersion = "2.1.280"
	spcFixturePath    = "testdata/subagent_prompt_v" + spcFixtureVersion + ".json"
)

const (
	spcTicket         = "2658"
	spcWorkdirName    = "spc-work"
	spcRecordName     = "spc-record.json"
	spcArtifactPrefix = "pyry-2658-capture-*"
	// sonnet for ptucModel's reason: a model that inlines two trivial reads
	// produces a green run with no subagent in it.
	spcModel = "sonnet"
	// A fixed literal in a per-test temp $HOME, distinct from every sibling probe's.
	spcSessionID = "2a6f0c3e-8d51-4b7a-a9e2-6570c1d4b8f3"
	// The flag this capture exists to be taken under.
	spcForwardFlag = "--forward-subagent-text"
)

// spcArgs omits --allowed-tools for ptucArgs's reason: the Agent tool must be
// reachable. --forward-subagent-text is NOT here because streamsup.New adds it.
var spcArgs = []string{"--model", spcModel, "--dangerously-skip-permissions"}

const spcSpawnShapeDelta = "#2191's YOLO shape (ptucArgs), with ONE difference stated rather than left to " +
	"be diffed: the runner now adds --forward-subagent-text itself (#2192), so this capture carries it " +
	"and #2191's does not. That flag is the whole reason this fixture exists."

const spcLimitations = "One conversation, one spawn shape, one claude version, one model (" + spcModel +
	"), one foreground general-purpose subagent, delegation requested EXPLICITLY. Background subagents, " +
	"nested subagents, other subagent types and cross-version stability are UNMEASURED. The delegated " +
	"prompt is rig-authored and carries no host content."

const spcRedactionRationale = "Inherited whole from #2191 (see ptucRedactionRationale) and #1260: per-test " +
	"temp $HOME, a fresh non-git workdir holding two rig-authored marker files, rig-authored prompts, the " +
	"dropcapRedactor substitution table over every string, and dropcapScanner as a fail-closed deny-scan " +
	"over the marshalled record and every decoded base64 payload. The delegated_prompts census adds key " +
	"NAMES and block TYPES only — no value leaves a frame outside the scanned payloads."

// spcPromptLine is one user line that opened a subagent: the facts the parser's
// rule depends on, as names only. The payload itself is in the frame at Index.
type spcPromptLine struct {
	Index           int      `json:"index"`
	ParentToolUseID string   `json:"parent_tool_use_id"`
	LineKeys        []string `json:"line_keys"`
	BlockTypes      []string `json:"block_types"`
}

// spcRecord is #2191's record plus the census this ticket pins. Embedding means
// ptucCollect fills the frames and Agent-call fields unchanged.
type spcRecord struct {
	ptucRecord
	DelegatedPrompts []spcPromptLine `json:"delegated_prompts"`
}

// spcDelegatedPrompt reports whether raw is a user line carrying a text block
// under a non-empty parent_tool_use_id, and its key names and block types when it
// is. A parent that is absent, null or not a string reads as main-thread, exactly
// as the shipped parser reads it.
func spcDelegatedPrompt(index int, raw []byte) (spcPromptLine, bool) {
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return spcPromptLine{}, false
	}
	var typ string
	if json.Unmarshal(keys["type"], &typ) != nil || typ != "user" {
		return spcPromptLine{}, false
	}
	facts := ptucReadLine(raw)
	if facts.Parent == "" {
		return spcPromptLine{}, false
	}
	var line struct {
		Message *struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &line) != nil || line.Message == nil {
		return spcPromptLine{}, false
	}
	out := spcPromptLine{Index: index, ParentToolUseID: facts.Parent}
	for _, b := range line.Message.Content {
		out.BlockTypes = append(out.BlockTypes, b.Type)
	}
	if !slices.Contains(out.BlockTypes, "text") {
		return spcPromptLine{}, false
	}
	for k := range keys {
		out.LineKeys = append(out.LineKeys, k)
	}
	sort.Strings(out.LineKeys)
	return out, true
}

// fixtureWorthy answers whether the record may be promoted to spcFixturePath. It
// shadows ptucRecord's, whose version arm names the other fixture's release.
func (rec *spcRecord) fixtureWorthy() (string, bool) {
	if rec.Outcome != ptucFired {
		return fmt.Sprintf("outcome=%s", rec.Outcome), false
	}
	if rec.AgentToolUseID == "" || rec.AgentCallCount != 1 {
		return fmt.Sprintf("%d Agent call(s), want exactly 1 — the reader pins one id", rec.AgentCallCount), false
	}
	if rec.AttributedFrames < ptucMinAttributed {
		return fmt.Sprintf("%d frames attributed to the Agent call, want at least %d", rec.AttributedFrames,
			ptucMinAttributed), false
	}
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != spcFixtureVersion {
		return fmt.Sprintf("claude_version %q is not the %s pinned in the fixture name — repin "+
			"spcFixtureVersion and subagentPromptCaptureVersion together, then re-run", got, spcFixtureVersion), false
	}
	if !slices.Contains(rec.SpawnShape, spcForwardFlag) {
		return fmt.Sprintf("spawn_shape lacks %s, so the line this capture exists for cannot be in it",
			spcForwardFlag), false
	}
	if len(rec.DelegatedPrompts) != 1 {
		return fmt.Sprintf("%d delegated-prompt line(s), want exactly 1 — if 0, claude did not send the "+
			"line under this flag and the ticket's premise needs re-reading", len(rec.DelegatedPrompts)), false
	}
	if rec.DelegatedPrompts[0].ParentToolUseID != rec.AgentToolUseID {
		return "the delegated-prompt line names a parent other than the Agent call", false
	}
	for _, f := range rec.Frames {
		if f.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("frame %d is encoded %q and the reader reads only %q", f.Index,
				f.PayloadEncoding, dropcapEncodingJSONString), false
		}
	}
	return "", true
}

// spcPrompt asks for exactly one foreground general-purpose subagent doing two
// reads. Both tool spellings are named because claude 2.1.280 calls it Agent and
// older releases called it Task; the nonce feeds dropcapRedactor's prompt_nonce.
func spcPrompt(nonce int64) string {
	return fmt.Sprintf("Use the Agent tool (also called Task) exactly once, in the foreground, to launch "+
		"ONE general-purpose subagent. Give that subagent this instruction verbatim: `Read the file "+
		"alpha.txt and then read the file beta.txt in the current working directory, then reply with the "+
		"two marker strings you found.` Do not read either file yourself. Use no other tools yourself, "+
		"and when the subagent reports back reply with the two markers and nothing else. run=%d", nonce)
}

// TestRealClaude_SubagentPromptCapture is the capture — AC 1.
func TestRealClaude_SubagentPromptCapture(t *testing.T) {
	force := os.Getenv(spcEnableEnv) == "1"
	if _, err := os.Stat(spcFixturePath); err == nil && !force {
		t.Skipf("#2658 subagent prompt capture: the fixture %s already exists. Force a re-capture with "+
			"%s=1 go test -tags e2e_realclaude -timeout 20m -v -run '^TestRealClaude_SubagentPromptCapture$' "+
			"./internal/e2e/realclaude/", spcFixturePath, spcEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Not t.TempDir(): the record must outlive the gate's worktree.
	artifactDir, err := os.MkdirTemp("", spcArtifactPrefix)
	if err != nil {
		t.Fatalf("#2658: create artifact dir: %v", err)
	}
	workdir := filepath.Join(home, spcWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2658: create workdir: %v", err)
	}
	for name, body := range ptucFiles {
		if err := os.WriteFile(filepath.Join(workdir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("#2658: write %s: %v", name, err)
		}
	}
	nonce := time.Now().UnixNano()

	red := newDropcapRedactor(home, artifactDir, workdir, "", spcSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2658 capture artifacts: %s", red.str(artifactDir))

	prompt := spcPrompt(nonce)
	rec := &spcRecord{
		ptucRecord: ptucRecord{
			Ticket:                spcTicket,
			ClaudeVersion:         probeClaudeVersion(claudeBin),
			CapturedAt:            time.Now().Format(time.RFC3339),
			IsCapture:             true,
			Model:                 spcModel,
			SpawnShapeDelta:       spcSpawnShapeDelta,
			Workdir:               red.str(workdir),
			Prompts:               []string{red.str(prompt)},
			Frames:                []ptucFrame{},
			SubagentToolCalls:     []string{},
			DistinctParentIDs:     []string{},
			LineTypeCensus:        map[string]int{},
			RedactionRationale:    spcRedactionRationale,
			CredentialScanApplied: scanner.applied(),
			CredentialScanSkipped: []string{},
			Limitations:           spcLimitations,
		},
		DelegatedPrompts: []spcPromptLine{},
	}
	rec.set(ptucInstrumentBroken, "did not reach a classification point")

	// Registered before anything below can fail, so a structural failure still
	// leaves the evidence on disk. Promotion refusal is loud: no usable capture is
	// a failed gate, never a quiet skip.
	t.Cleanup(func() {
		spcWriteRecord(t, artifactDir, red, scanner, rec)
		if reason, ok := rec.fixtureWorthy(); !ok {
			t.Errorf("#2658: no usable capture — %s. The record in the artifact dir is the evidence",
				red.str(reason))
		}
	})

	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: spcSessionID,
		Args:      spcArgs,
		Stdout:    recorder,
		Logger:    slog.New(argvHandler),
	})
	if err != nil {
		rec.set(ptucInstrumentBroken, "streamsup.New failed: %v", red.str(err.Error()))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = runner.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(ptucRunExitWait):
			t.Errorf("#2658: streamsup.Run did not return within %s of cancel", ptucRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(ptucInstrumentBroken, "no live child within %s", dropcapSpawnWait)
		return
	}
	turnStart := time.Now()
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.set(ptucInstrumentBroken, "writing the turn failed: %v", red.str(err.Error()))
		return
	}
	rec.TerminatedOn = ptucAwaitTurn(recorder, 0, ptucQuiet, ptucTurnBudget)
	rec.TurnSeconds = time.Since(turnStart).Seconds()

	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	ptucCollect(t, &rec.ptucRecord, lines, red)
	for _, c := range lines {
		if pl, ok := spcDelegatedPrompt(c.Index, c.Raw); ok {
			pl.ParentToolUseID = red.str(pl.ParentToolUseID)
			rec.DelegatedPrompts = append(rec.DelegatedPrompts, pl)
		}
	}

	if rec.AttributedFrames == 0 {
		rec.set(ptucDidNotFire, "no line named a spawning Agent call: %d Agent call(s), tools run %v",
			rec.AgentCallCount, rec.ToolCalls)
		return
	}
	rec.set(ptucFired, "%d frame(s) attributed to Agent call %q; %d delegated-prompt line(s)",
		rec.AttributedFrames, rec.AgentToolUseID, len(rec.DelegatedPrompts))
}

// spcWriteRecord deny-scans, writes the record to the artifact dir, and promotes
// it in-repo when worthy — ptucWriteRecord's shape with this fixture's path.
func spcWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *spcRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()
	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2658: marshal record: %v", err)
		return
	}
	hits, notApplied := scanner.scan(blob)
	for i, f := range rec.Frames {
		if f.PayloadB64 == "" {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(f.PayloadB64)
		if derr != nil {
			t.Errorf("#2658: frame %d: decode base64 payload for the scan: %v", i, derr)
			return
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("#2658: deny-scan found %d denied class(es) in the record: %v. NOTHING was written. "+
			"Extend dropcapRedactor's table and re-run; the value is deliberately not printed", len(hits), hits)
	}
	rec.CredentialScanSkipped = notApplied
	blob, err = json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2658: re-marshal record: %v", err)
		return
	}

	path := filepath.Join(dir, spcRecordName)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2658: write record %s: %v", red.str(path), err)
		return
	}
	t.Logf("#2658 outcome=%s terminated_on=%s turn=%.1fs captured=%d agent_calls=%d agent_id=%q "+
		"attributed=%d delegated_prompts=%d types=%v scan_not_applied=%v\n  record: %s",
		rec.Outcome, rec.TerminatedOn, rec.TurnSeconds, rec.LinesCaptured, rec.AgentCallCount,
		rec.AgentToolUseID, rec.AttributedFrames, len(rec.DelegatedPrompts), rec.LineTypeCensus,
		notApplied, red.str(path))
	for _, pl := range rec.DelegatedPrompts {
		t.Logf("#2658 delegated prompt: frame %d keys %v blocks %v", pl.Index, pl.LineKeys, pl.BlockTypes)
	}

	if reason, ok := rec.fixtureWorthy(); !ok {
		t.Logf("#2658: NOT promoted to %s — %s", spcFixturePath, red.str(reason))
		return
	}
	if err := os.WriteFile(spcFixturePath, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2658: write fixture %s: %v", spcFixturePath, red.str(err.Error()))
		return
	}
	t.Logf("#2658: FIXTURE WRITTEN to %s. COMMIT IT — `git add %s` — and in the SAME commit set "+
		"subagentPromptPinnedAgentID in internal/streamsup/subagent_prompt_capture_test.go to %q. "+
		"An uncommitted capture is a capture that did not happen (#1763).",
		spcFixturePath, spcFixturePath, rec.AgentToolUseID)
}

// TestSpcDelegatedPromptCensus runs offline and guards the census the promotion
// gate counts. A census that missed the line would refuse every good capture; one
// that counted main-thread text would promote a turn proving nothing.
func TestSpcDelegatedPromptCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		line       string
		want       bool
		wantKeys   []string
		wantBlocks []string
	}{
		{
			name:       "a parent-stamped text line is the delegated prompt",
			line:       `{"type":"user","parent_tool_use_id":"toolu_agent","message":{"content":[{"type":"text","text":"x"}]}}`,
			want:       true,
			wantKeys:   []string{"message", "parent_tool_use_id", "type"},
			wantBlocks: []string{"text"},
		},
		{name: "a main-thread text line is not",
			line: `{"type":"user","parent_tool_use_id":null,"message":{"content":[{"type":"text","text":"x"}]}}`},
		{name: "a non-string parent reads as main-thread",
			line: `{"type":"user","parent_tool_use_id":7,"message":{"content":[{"type":"text","text":"x"}]}}`},
		{name: "a subagent tool_result line is not",
			line: `{"type":"user","parent_tool_use_id":"toolu_agent","message":{"content":[{"type":"tool_result","tool_use_id":"t"}]}}`},
		{name: "a subagent assistant text line is not",
			line: `{"type":"assistant","parent_tool_use_id":"toolu_agent","message":{"content":[{"type":"text","text":"x"}]}}`},
		{name: "an undecodable line is not", line: `{not json`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := spcDelegatedPrompt(3, []byte(tc.line))
			if ok != tc.want {
				t.Fatalf("spcDelegatedPrompt ok = %v, want %v", ok, tc.want)
			}
			if !ok {
				return
			}
			if got.Index != 3 || got.ParentToolUseID != "toolu_agent" ||
				!slices.Equal(got.LineKeys, tc.wantKeys) || !slices.Equal(got.BlockTypes, tc.wantBlocks) {
				t.Errorf("got %+v, want index 3, parent toolu_agent, keys %v, blocks %v",
					got, tc.wantKeys, tc.wantBlocks)
			}
		})
	}
}

// TestSpcFixtureWorthyRefusesEveryBadCapture runs offline and pins the promotion
// gate, one row per refusal arm, as TestPtucFixtureWorthyRefusesEveryBadCapture.
func TestSpcFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	good := func() *spcRecord {
		return &spcRecord{
			ptucRecord: ptucRecord{
				Outcome:          ptucFired,
				ClaudeVersion:    spcFixtureVersion + " (Claude Code)",
				SpawnShape:       []string{"--verbose", spcForwardFlag, "--model", spcModel},
				AgentToolUseID:   "toolu_agent",
				AgentCallCount:   1,
				AttributedFrames: ptucMinAttributed,
				Frames:           []ptucFrame{{Index: 0, PayloadEncoding: dropcapEncodingJSONString}},
			},
			DelegatedPrompts: []spcPromptLine{{Index: 1, ParentToolUseID: "toolu_agent"}},
		}
	}
	if reason, ok := good().fixtureWorthy(); !ok {
		t.Fatalf("a good record was refused: %s — every row below mutates it", reason)
	}
	tests := []struct {
		name   string
		break_ func(*spcRecord)
	}{
		{"did not fire", func(r *spcRecord) { r.Outcome = ptucDidNotFire }},
		{"no Agent call", func(r *spcRecord) { r.AgentToolUseID = "" }},
		{"two Agent calls", func(r *spcRecord) { r.AgentCallCount = 2 }},
		{"one tool short", func(r *spcRecord) { r.AttributedFrames = ptucMinAttributed - 1 }},
		{"the other fixture's claude", func(r *spcRecord) { r.ClaudeVersion = ptucFixtureVersion + " (Claude Code)" }},
		{"spawned without the flag", func(r *spcRecord) { r.SpawnShape = []string{"--verbose"} }},
		{"no delegated-prompt line", func(r *spcRecord) { r.DelegatedPrompts = nil }},
		{"two delegated-prompt lines", func(r *spcRecord) {
			r.DelegatedPrompts = append(r.DelegatedPrompts, r.DelegatedPrompts[0])
		}},
		{"prompt under another parent", func(r *spcRecord) { r.DelegatedPrompts[0].ParentToolUseID = "toolu_x" }},
		{"a frame the reader cannot decode", func(r *spcRecord) {
			r.Frames[0].PayloadEncoding = dropcapEncodingBase64
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := good()
			tc.break_(rec)
			if reason, ok := rec.fixtureWorthy(); ok || reason == "" {
				t.Errorf("fixtureWorthy = (%q, %v), want a named refusal", reason, ok)
			}
		})
	}
}
