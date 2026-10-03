//go:build e2e_realclaude

package realclaude

// Evidence capture for #2728 — what a real claude does with a user line written
// to its stdin while a turn is running. Send now (#2725) delivers a queued message
// into the running turn; pyrycode today holds every queued message until the turn
// goes idle. This probe answers what the delivery and client-report tickets need
// to know first, across four arms:
//
//   - tool-boundary: the message is written during a long Bash call.
//   - no-tool: the message is written while a text-only answer streams, so there
//     is no tool boundary to fold at.
//   - after-last-tool: the message is written once the turn's only tool result
//     has appeared on stdout.
//   - back-to-back: two messages written together during a Bash call.
//
// # Staging
//
// claude is driven directly — the daemon adds nothing these questions need. Each
// arm is a fresh child under production's stream-json flag prefix (streamsup's
// buildArgs) plus --replay-user-messages, so the record also shows whether, and
// where, claude echoes a mid-turn line. Every write goes through streamsup.WriteTurn,
// production's own user-line encoder. The arms run concurrently, one child each.
//
// Reused: dropcapRecorder (verbatim stdout lines), dropcapRedactor (substitution
// table) and dropcapScanner (fail-closed deny-scan).
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine — the gate is the FIXTURE'S
// ABSENCE, so the gate's normal invocation captures it. To force a re-capture over
// an existing fixture:
//
//	PYRY_PROBE_MID_TURN_USER_CAPTURE=1 go test -tags e2e_realclaude -timeout 20m -v \
//	  -run '^TestRealClaude_MidTurnUserCapture$' ./internal/e2e/realclaude/

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

const mtuEnableEnv = "PYRY_PROBE_MID_TURN_USER_CAPTURE"

const (
	mtuTicket         = "2728"
	mtuModel          = "haiku"
	mtuRecordName     = "mtu-record.json"
	mtuArtifactPrefix = "pyry-2728-capture-*"
	mtuFixtureGlob    = "testdata/mid_turn_user_v*.json"
)

// The fixture is named with the claude version that produced it, so a later
// release's capture lands beside this one rather than over it.
func mtuFixturePath(version string) string {
	return "testdata/mid_turn_user_v" + version + ".json"
}

const (
	// mtuTriggerWait bounds the wait for an arm's write point to appear.
	mtuTriggerWait = 2 * time.Minute
	// mtuArmBudget bounds one arm from spawn to the end of its quiet window.
	mtuArmBudget = 4 * time.Minute
	// mtuQuiet is how long stdout must stay silent after a result line before the
	// arm ends. A second turn opened by the queued message starts inside it.
	mtuQuiet = 20 * time.Second
	mtuPoll  = 100 * time.Millisecond
	// mtuExitWait bounds the wait for the child to exit after stdin closes.
	mtuExitWait = 15 * time.Second
)

const (
	mtuFolded          = "folded"
	mtuSecondTurn      = "second-turn"
	mtuNotQuoted       = "not-quoted"
	mtuPartial         = "partial"
	mtuNoResult        = "no-result"
	mtuNoTrigger       = "no-trigger"
	mtuInstrumentBroke = "instrument-broken"
)

const mtuLimitations = "One claude version, one model (" + mtuModel + "), one run per arm. A verdict reads " +
	"the final result text for the written markers: not-quoted means the marker is absent from that text, " +
	"not that claude never read the message — the sequence and echoes are the other evidence. Write " +
	"positions are the stdout line count observed just before the write."

// mtuSpec is one arm: its opening turn, the marker(s) it writes mid-turn and the
// point in the stream it writes at.
type mtuSpec struct {
	name     string
	opening  string
	open     string // the marker the opening turn asks for
	markers  []string
	delay    time.Duration
	trigger  func(c dropcapCaptured) bool
	session  string
	triggerH string // the trigger, said in words for the record
}

func mtuMessage(marker string) string {
	return "One more thing: include the exact word " + marker + " in your final reply."
}

func mtuSpecs() []mtuSpec {
	toolOpening := func(marker string) string {
		return "Use the Bash tool exactly once to run `sleep 15 && echo slept`. When it finishes, reply in one " +
			"short paragraph, and include the exact word " + marker + " in that reply."
	}
	essay := "write an essay of about 800 words on the history of lighthouses, and end it with the exact word "
	return []mtuSpec{
		{
			name: "tool-boundary", open: "OPENING-TOOL-BOUNDARY", markers: []string{"MIDTURN-TOOL-BOUNDARY-1"},
			opening: toolOpening("OPENING-TOOL-BOUNDARY"), delay: 2 * time.Second, trigger: mtuIsToolUse,
			session: "7c2e9a41-5b3d-4f08-9e61-2d4a8b0c3f15", triggerH: "2s after the first assistant tool_use line",
		},
		{
			name: "no-tool", open: "OPENING-NO-TOOL", markers: []string{"MIDTURN-NO-TOOL-1"},
			opening: "Without using any tools, " + essay + "OPENING-NO-TOOL.",
			delay:   time.Second, trigger: mtuIsTextDelta,
			session: "1f8d3b62-0a7e-4c95-b2d4-6e9f1a5c7083", triggerH: "1s after the first content_block_delta stream_event",
		},
		{
			name: "after-last-tool", open: "OPENING-AFTER-LAST-TOOL", markers: []string{"MIDTURN-AFTER-LAST-TOOL-1"},
			opening: "Use the Bash tool exactly once to run `echo ready`. After it returns, use no more tools and " +
				essay + "OPENING-AFTER-LAST-TOOL.",
			trigger: mtuIsToolResult,
			session: "a94c0e57-3d16-4b8a-8f27-c5b0d2e6a914", triggerH: "on the first user tool_result line",
		},
		{
			name: "back-to-back", open: "OPENING-BACK-TO-BACK",
			markers: []string{"MIDTURN-BACK-TO-BACK-1", "MIDTURN-BACK-TO-BACK-2"},
			opening: toolOpening("OPENING-BACK-TO-BACK"), delay: 2 * time.Second, trigger: mtuIsToolUse,
			session: "5e0b7d93-8c24-4a61-9d3f-b17e4c2a8056", triggerH: "2s after the first assistant tool_use line, both written together",
		},
	}
}

// mtuStep is one entry of an arm's ordered sequence: a run of identical stdout
// lines, or one probe write.
type mtuStep struct {
	Type    string   `json:"type,omitempty"`
	Subtype string   `json:"subtype,omitempty"`
	Blocks  []string `json:"blocks,omitempty"`
	Count   int      `json:"count,omitempty"`
	Write   string   `json:"write,omitempty"`
}

// mtuEcho is a user line on stdout that carries a marker the probe wrote,
// verbatim after redaction.
type mtuEcho struct {
	Index   int      `json:"index"`
	Markers []string `json:"markers"`
	Line    string   `json:"line"`
}

type mtuResult struct {
	Index   int      `json:"index"`
	Subtype string   `json:"subtype"`
	Markers []string `json:"markers_in_result_text"`
}

type mtuArm struct {
	Name             string          `json:"name"`
	Trigger          string          `json:"trigger"`
	Opening          string          `json:"opening_prompt"`
	OpeningMarker    string          `json:"opening_marker"`
	Writes           []string        `json:"writes"`
	Sequence         []mtuStep       `json:"sequence"`
	Echoes           []mtuEcho       `json:"echoes"`
	ResultCount      int             `json:"result_count"`
	Results          []mtuResult     `json:"results"`
	FinalTextMarkers map[string]bool `json:"final_text_markers"`
	Verdict          string          `json:"verdict"`
	Note             string          `json:"note,omitempty"`
	LinesCaptured    int             `json:"lines_captured"`
	Seconds          float64         `json:"seconds"`
	Exit             string          `json:"exit"`

	writeAt []int // line count observed before each write, parallel to Writes
}

type mtuRecord struct {
	Ticket                string                `json:"ticket"`
	ClaudeVersion         string                `json:"claude_version"`
	CapturedAt            string                `json:"captured_at"`
	Model                 string                `json:"model"`
	Argv                  []string              `json:"argv"`
	Arms                  []mtuArm              `json:"arms"`
	Redaction             []dropcapSubstitution `json:"redaction"`
	CredentialScanApplied map[string]bool       `json:"credential_scan_applied"`
	CredentialScanSkipped []string              `json:"credential_scan_skipped"`
	Limitations           string                `json:"limitations"`
}

// mtuArgv is production's buildArgs prefix plus the flags this capture needs.
func mtuArgv(session string) []string {
	return []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--forward-subagent-text",
		"--replay-user-messages",
		"--model", mtuModel,
		"--dangerously-skip-permissions",
		"--session-id", session,
	}
}

// TestRealClaude_MidTurnUserCapture is the capture — AC 1 to 4.
func TestRealClaude_MidTurnUserCapture(t *testing.T) {
	existing, _ := filepath.Glob(mtuFixtureGlob)
	if len(existing) > 0 && os.Getenv(mtuEnableEnv) != "1" {
		t.Skipf("#2728 mid-turn user capture: %v already exists. Force a re-capture with %s=1", existing,
			mtuEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Not t.TempDir(): the record must outlive the gate's worktree.
	artifactDir, err := os.MkdirTemp("", mtuArtifactPrefix)
	if err != nil {
		t.Fatalf("#2728: create artifact dir: %v", err)
	}
	specs := mtuSpecs()
	red := newDropcapRedactor(home, artifactDir, "", "", specs[0].session, time.Now().UnixNano())
	scanner := newDropcapScanner(home, artifactDir, "")
	for _, s := range specs[1:] {
		red.addValueClass(dropcapClassSessionID, "$SESSION_ID", s.session)
	}
	for _, id := range mtuAccountIDs(home) {
		red.addValueClass("account_id", "$ACCOUNT_ID", id)
		scanner.addDynamic("account_id", id)
	}

	rec := &mtuRecord{
		Ticket:                mtuTicket,
		ClaudeVersion:         probeClaudeVersion(claudeBin),
		CapturedAt:            time.Now().Format(time.RFC3339),
		Model:                 mtuModel,
		Argv:                  red.strs(mtuArgv(specs[0].session)),
		CredentialScanApplied: scanner.applied(),
		CredentialScanSkipped: []string{},
		Limitations:           mtuLimitations,
	}

	arms := make([]mtuArm, len(specs))
	var wg sync.WaitGroup
	for i, s := range specs {
		workdir := filepath.Join(home, "mtu-"+s.name)
		if err := os.MkdirAll(workdir, 0o700); err != nil {
			t.Fatalf("#2728: create workdir: %v", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			arms[i] = mtuRunArm(claudeBin, workdir, s)
		}()
	}
	wg.Wait()

	// Redaction runs here, on the test goroutine, because the redactor's counters
	// are unlocked.
	for i := range arms {
		for j := range arms[i].Echoes {
			arms[i].Echoes[j].Line = red.str(arms[i].Echoes[j].Line)
		}
		arms[i].Note = red.str(arms[i].Note)
		arms[i].Exit = red.str(arms[i].Exit)
		t.Logf("#2728 arm %s: verdict=%s results=%d echoes=%d lines=%d %.1fs note=%q", arms[i].Name,
			arms[i].Verdict, arms[i].ResultCount, len(arms[i].Echoes), arms[i].LinesCaptured, arms[i].Seconds,
			arms[i].Note)
	}
	rec.Arms = arms
	mtuWriteRecord(t, artifactDir, red, scanner, rec)
}

// mtuRunArm spawns one child, writes the opening turn, waits for the arm's
// trigger, writes the mid-turn message(s), and reads until a quiet window after
// a result or the budget. It runs off the test goroutine, so it reports through
// the arm rather than through t. The result is named because the deferred join
// fills it after every return.
func mtuRunArm(claudeBin, workdir string, s mtuSpec) (arm mtuArm) {
	arm = mtuArm{
		Name: s.name, Trigger: s.triggerH, Opening: s.opening, OpeningMarker: s.open,
		Writes: []string{}, Echoes: []mtuEcho{}, Results: []mtuResult{}, FinalTextMarkers: map[string]bool{},
	}
	start := time.Now()
	defer func() { arm.Seconds = time.Since(start).Seconds() }()

	ctx, cancel := context.WithTimeout(context.Background(), mtuArmBudget+mtuExitWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBin, mtuArgv(s.session)...)
	cmd.Dir = workdir
	recorder := newDropcapRecorder()
	cmd.Stdout = recorder
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		arm.Verdict, arm.Note = mtuInstrumentBroke, "stdin pipe: "+err.Error()
		return arm
	}
	if err := cmd.Start(); err != nil {
		arm.Verdict, arm.Note = mtuInstrumentBroke, "start claude: "+err.Error()
		return arm
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	defer func() {
		_ = stdin.Close()
		select {
		case err := <-waitDone:
			arm.Exit = mtuExitString(err)
		case <-time.After(mtuExitWait):
			cancel()
			arm.Exit = "killed after stdin close: " + mtuExitString(<-waitDone)
		}
		lines, _ := recorder.snapshot()
		mtuSummarise(&arm, lines, s)
		if arm.Verdict == "" {
			arm.Verdict = mtuVerdict(arm, s.markers)
		}
		if arm.Verdict == mtuNoResult && stderr.Len() > 0 {
			arm.Note = "stderr: " + mtuTail(stderr.String(), 400)
		}
	}()

	if err := streamsup.WriteTurn(ctx, stdin, []byte(s.opening)); err != nil {
		arm.Verdict, arm.Note = mtuInstrumentBroke, "write opening turn: "+err.Error()
		return arm
	}
	deadline := start.Add(mtuArmBudget)

	if !mtuAwait(recorder, start.Add(mtuTriggerWait), waitDone, func(lines []dropcapCaptured) bool {
		for _, c := range lines {
			if s.trigger(c) {
				return true
			}
		}
		return false
	}) {
		arm.Verdict, arm.Note = mtuNoTrigger, "the trigger point never appeared on stdout"
		return arm
	}
	time.Sleep(s.delay)
	for _, m := range s.markers {
		lines, _ := recorder.snapshot()
		if err := streamsup.WriteTurn(ctx, stdin, []byte(mtuMessage(m))); err != nil {
			arm.Verdict, arm.Note = mtuInstrumentBroke, "write mid-turn message: "+err.Error()
			return arm
		}
		arm.Writes = append(arm.Writes, m)
		arm.writeAt = append(arm.writeAt, len(lines))
	}

	// Wait for a result, then for stdout to go quiet, so a second turn is caught.
	seen, lastChange := 0, time.Now()
	mtuAwait(recorder, deadline, waitDone, func(lines []dropcapCaptured) bool {
		if len(lines) != seen {
			seen, lastChange = len(lines), time.Now()
		}
		for _, c := range lines {
			if c.Type == "result" {
				return time.Since(lastChange) >= mtuQuiet
			}
		}
		return false
	})
	return arm
}

// mtuAwait polls the recorder until done reports true, the deadline passes or
// the child exits; it reports whether done fired. A child that exits early
// hands its wait result back so the caller's deferred join still reads it.
func mtuAwait(r *dropcapRecorder, deadline time.Time, waitDone chan error, done func([]dropcapCaptured) bool) bool {
	for {
		lines, _ := r.snapshot()
		if done(lines) {
			return true
		}
		select {
		case err := <-waitDone:
			lines, _ = r.snapshot()
			waitDone <- err
			return done(lines)
		default:
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(mtuPoll)
	}
}

// mtuLineFacts is what the sequence and the triggers read from one line.
type mtuLineFacts struct {
	Type    string
	Subtype string
	Blocks  []string
	Result  string
}

func mtuReadLine(c dropcapCaptured) mtuLineFacts {
	f := mtuLineFacts{Type: c.Type, Subtype: c.Subtype}
	if !c.Decoded {
		f.Type = "<undecodable>"
		return f
	}
	var line struct {
		Event *struct {
			Type string `json:"type"`
		} `json:"event"`
		Message *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Result string `json:"result"`
	}
	_ = json.Unmarshal(c.Raw, &line)
	f.Result = line.Result
	if c.Type == "stream_event" && line.Event != nil {
		f.Subtype = line.Event.Type
	}
	if line.Message != nil {
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line.Message.Content, &blocks) == nil {
			for _, b := range blocks {
				f.Blocks = append(f.Blocks, b.Type)
			}
		} else if len(line.Message.Content) > 0 && line.Message.Content[0] == '"' {
			f.Blocks = []string{"<string>"}
		}
	}
	return f
}

func mtuIsToolUse(c dropcapCaptured) bool {
	f := mtuReadLine(c)
	return f.Type == "assistant" && mtuHas(f.Blocks, "tool_use")
}

func mtuIsToolResult(c dropcapCaptured) bool {
	f := mtuReadLine(c)
	return f.Type == "user" && mtuHas(f.Blocks, "tool_result")
}

func mtuIsTextDelta(c dropcapCaptured) bool {
	return c.Type == "stream_event" && mtuReadLine(c).Subtype == "content_block_delta"
}

func mtuHas(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// mtuSummarise fills the sequence, echoes and results from the captured lines.
func mtuSummarise(arm *mtuArm, lines []dropcapCaptured, s mtuSpec) {
	arm.LinesCaptured = len(lines)
	all := append([]string{s.open}, s.markers...)
	arm.Sequence = mtuSequence(lines, arm.Writes, arm.writeAt)
	var last string
	for _, c := range lines {
		f := mtuReadLine(c)
		switch f.Type {
		case "user":
			if found := mtuMarkersIn(string(c.Raw), all); len(found) > 0 {
				arm.Echoes = append(arm.Echoes, mtuEcho{Index: c.Index, Markers: found, Line: string(c.Raw)})
			}
		case "result":
			arm.Results = append(arm.Results, mtuResult{Index: c.Index, Subtype: f.Subtype,
				Markers: mtuMarkersIn(f.Result, all)})
			last = f.Result
		}
	}
	arm.ResultCount = len(arm.Results)
	for _, m := range all {
		arm.FinalTextMarkers[m] = strings.Contains(last, m)
	}
}

// mtuSequence lists every line's type in order, consecutive identical entries
// collapsed into one with a count, and each write placed before the line whose
// index equals the count observed when it was written.
func mtuSequence(lines []dropcapCaptured, writes []string, writeAt []int) []mtuStep {
	out := []mtuStep{}
	w := 0
	flush := func(upTo int) {
		for ; w < len(writes) && writeAt[w] <= upTo; w++ {
			out = append(out, mtuStep{Write: writes[w]})
		}
	}
	for _, c := range lines {
		flush(c.Index)
		f := mtuReadLine(c)
		step := mtuStep{Type: f.Type, Subtype: f.Subtype, Blocks: f.Blocks, Count: 1}
		if n := len(out); n > 0 && out[n-1].Write == "" && out[n-1].Type == step.Type &&
			out[n-1].Subtype == step.Subtype && strings.Join(out[n-1].Blocks, ",") == strings.Join(step.Blocks, ",") {
			out[n-1].Count++
			continue
		}
		out = append(out, step)
	}
	flush(int(^uint(0) >> 1))
	return out
}

func mtuMarkersIn(text string, markers []string) []string {
	found := []string{}
	for _, m := range markers {
		if strings.Contains(text, m) {
			found = append(found, m)
		}
	}
	return found
}

// mtuVerdict names what happened to the mid-turn message(s), read from the
// result lines.
func mtuVerdict(arm mtuArm, markers []string) string {
	switch {
	case len(arm.Writes) == 0:
		return mtuNoTrigger
	case arm.ResultCount == 0:
		return mtuNoResult
	case arm.ResultCount > 1:
		return mtuSecondTurn
	}
	quoted := 0
	for _, m := range markers {
		if arm.FinalTextMarkers[m] {
			quoted++
		}
	}
	switch quoted {
	case len(markers):
		return mtuFolded
	case 0:
		return mtuNotQuoted
	}
	return mtuPartial
}

func mtuExitString(err error) string {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return "exit 0"
	case errors.As(err, &ee):
		return fmt.Sprintf("exit %d", ee.ExitCode())
	}
	return err.Error()
}

func mtuTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// mtuAccountIDs reads the account identifiers WithWorktreeAuthenticated seeded
// into the temp home's .claude.json, so the redactor and the deny-scan know them.
// Absent on the API-key path, where nothing is seeded.
func mtuAccountIDs(home string) []string {
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return nil
	}
	var cfg struct {
		UserID       string `json:"userID"`
		OAuthAccount struct {
			AccountUUID      string `json:"accountUuid"`
			EmailAddress     string `json:"emailAddress"`
			OrganizationUUID string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return nil
	}
	out := []string{}
	for _, v := range []string{cfg.UserID, cfg.OAuthAccount.AccountUUID, cfg.OAuthAccount.EmailAddress,
		cfg.OAuthAccount.OrganizationUUID} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

var mtuVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// fixtureWorthy answers whether the record may be promoted in-repo, and the
// fixture's version when it may. A no-result arm is still worthy: the ticket
// records a timeout as that arm's answer. An arm that never wrote measured
// nothing.
func (rec *mtuRecord) fixtureWorthy() (string, string, bool) {
	version, _, _ := strings.Cut(rec.ClaudeVersion, " ")
	if !mtuVersionRE.MatchString(version) {
		return "", fmt.Sprintf("claude_version %q does not start with a release number", rec.ClaudeVersion), false
	}
	want := mtuSpecs()
	if len(rec.Arms) != len(want) {
		return "", fmt.Sprintf("%d arm(s), want %d", len(rec.Arms), len(want)), false
	}
	for i, a := range rec.Arms {
		if a.Name != want[i].name {
			return "", fmt.Sprintf("arm %d is %q, want %q", i, a.Name, want[i].name), false
		}
		if a.Verdict == mtuNoTrigger || a.Verdict == mtuInstrumentBroke {
			return "", fmt.Sprintf("arm %s: %s (%s)", a.Name, a.Verdict, a.Note), false
		}
		if len(a.Writes) != len(want[i].markers) {
			return "", fmt.Sprintf("arm %s wrote %d message(s), want %d", a.Name, len(a.Writes),
				len(want[i].markers)), false
		}
	}
	return version, "", true
}

// mtuWriteRecord deny-scans, writes the record to the artifact dir, and
// promotes it in-repo when worthy. A deny-scan hit writes nothing.
func mtuWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *mtuRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()
	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("#2728: marshal record: %v", err)
	}
	hits, notApplied := scanner.scan(blob)
	if len(hits) > 0 {
		t.Fatalf("#2728: deny-scan found %d denied class(es) in the record: %v. NOTHING was written. "+
			"Extend the redaction table and re-run; the value is deliberately not printed", len(hits), hits)
	}
	rec.CredentialScanSkipped = append(rec.CredentialScanSkipped, notApplied...)
	if blob, err = json.MarshalIndent(rec, "", "  "); err != nil {
		t.Fatalf("#2728: re-marshal record: %v", err)
	}
	blob = append(blob, '\n')

	path := filepath.Join(dir, mtuRecordName)
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("#2728: write record %s: %v", red.str(path), err)
	}
	t.Logf("#2728 record: %s", red.str(path))

	version, reason, ok := rec.fixtureWorthy()
	if !ok {
		t.Errorf("#2728: no usable capture — %s. The record in the artifact dir is the evidence",
			red.str(reason))
		return
	}
	fixture := mtuFixturePath(version)
	if err := os.WriteFile(fixture, blob, 0o600); err != nil {
		t.Fatalf("#2728: write fixture %s: %v", fixture, red.str(err.Error()))
	}
	// Best-effort, as rafcapStageFixture: the bytes are already in the tree and
	// in the artifact dir.
	if out, err := exec.Command("git", "add", "--", fixture).CombinedOutput(); err != nil {
		t.Logf("#2728: git add %s failed (%v): %s", fixture, err, red.str(strings.TrimSpace(string(out))))
	}
	t.Logf("#2728: FIXTURE WRITTEN to %s. COMMIT IT — an uncommitted capture is a capture that did not "+
		"happen (#1763).", fixture)
}

// --- offline self-checks -----------------------------------------------------

func mtuLine(index int, raw string) dropcapCaptured {
	var sl struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	decoded := json.Unmarshal([]byte(raw), &sl) == nil
	return dropcapCaptured{Index: index, Raw: []byte(raw), Type: sl.Type, Subtype: sl.Subtype, Decoded: decoded}
}

// TestMtuSequence pins the collapse and the write placement: a write is shown
// before the first line it did not see, and runs of the same line collapse.
func TestMtuSequence(t *testing.T) {
	t.Parallel()
	lines := []dropcapCaptured{
		mtuLine(0, `{"type":"system","subtype":"init"}`),
		mtuLine(1, `{"type":"stream_event","event":{"type":"content_block_delta"}}`),
		mtuLine(2, `{"type":"stream_event","event":{"type":"content_block_delta"}}`),
		mtuLine(3, `{"type":"assistant","message":{"content":[{"type":"tool_use"}]}}`),
		mtuLine(4, `{"type":"user","message":{"content":"MIDTURN-X-1"}}`),
		mtuLine(5, `{"type":"result","subtype":"success","result":"ok"}`),
	}
	got := mtuSequence(lines, []string{"MIDTURN-X-1", "MIDTURN-X-2"}, []int{2, 9})
	want := []mtuStep{
		{Type: "system", Subtype: "init", Count: 1},
		{Type: "stream_event", Subtype: "content_block_delta", Count: 1},
		{Write: "MIDTURN-X-1"},
		{Type: "stream_event", Subtype: "content_block_delta", Count: 1},
		{Type: "assistant", Blocks: []string{"tool_use"}, Count: 1},
		{Type: "user", Blocks: []string{"<string>"}, Count: 1},
		{Type: "result", Subtype: "success", Count: 1},
		{Write: "MIDTURN-X-2"},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("sequence:\n got %s\nwant %s", gotJSON, wantJSON)
	}

	collapsed := mtuSequence(lines[1:3], nil, nil)
	if len(collapsed) != 1 || collapsed[0].Count != 2 {
		t.Errorf("two identical lines should collapse to one step of count 2, got %+v", collapsed)
	}
}

// TestMtuSummariseFindsEchoesAndResults pins the echo census: a user line
// carrying a written marker is an echo, a tool_result is not, and the final text
// is the last result's.
func TestMtuSummariseFindsEchoesAndResults(t *testing.T) {
	t.Parallel()
	s := mtuSpec{open: "OPENING-X", markers: []string{"MIDTURN-X-1"}}
	lines := []dropcapCaptured{
		mtuLine(0, `{"type":"user","message":{"content":[{"type":"text","text":"say OPENING-X"}]},"isReplay":true}`),
		mtuLine(1, `{"type":"user","message":{"content":[{"type":"tool_result","content":"slept"}]}}`),
		mtuLine(2, `{"type":"user","message":{"content":[{"type":"text","text":"MIDTURN-X-1"}]}}`),
		mtuLine(3, `{"type":"result","subtype":"success","result":"OPENING-X"}`),
		mtuLine(4, `{"type":"result","subtype":"success","result":"MIDTURN-X-1"}`),
	}
	arm := mtuArm{FinalTextMarkers: map[string]bool{}, Writes: []string{"MIDTURN-X-1"}, writeAt: []int{2}}
	mtuSummarise(&arm, lines, s)
	if len(arm.Echoes) != 2 || arm.Echoes[0].Index != 0 || arm.Echoes[1].Index != 2 {
		t.Errorf("echoes = %+v, want lines 0 and 2", arm.Echoes)
	}
	if arm.ResultCount != 2 || !arm.FinalTextMarkers["MIDTURN-X-1"] || arm.FinalTextMarkers["OPENING-X"] {
		t.Errorf("results=%d final=%v, want 2 results and only the mid-turn marker in the final text",
			arm.ResultCount, arm.FinalTextMarkers)
	}
	if v := mtuVerdict(arm, s.markers); v != mtuSecondTurn {
		t.Errorf("verdict = %s, want %s", v, mtuSecondTurn)
	}
}

func TestMtuVerdict(t *testing.T) {
	t.Parallel()
	markers := []string{"M1", "M2"}
	tests := []struct {
		name    string
		writes  int
		results int
		final   map[string]bool
		want    string
	}{
		{"no write", 0, 1, nil, mtuNoTrigger},
		{"no result", 2, 0, nil, mtuNoResult},
		{"two results", 2, 2, nil, mtuSecondTurn},
		{"both quoted", 2, 1, map[string]bool{"M1": true, "M2": true}, mtuFolded},
		{"one quoted", 2, 1, map[string]bool{"M1": true}, mtuPartial},
		{"none quoted", 2, 1, map[string]bool{}, mtuNotQuoted},
	}
	for _, tc := range tests {
		arm := mtuArm{Writes: make([]string, tc.writes), ResultCount: tc.results, FinalTextMarkers: tc.final}
		if got := mtuVerdict(arm, markers); got != tc.want {
			t.Errorf("%s: verdict = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// TestMtuFixtureWorthy pins the promotion gate, one row per refusal.
func TestMtuFixtureWorthy(t *testing.T) {
	t.Parallel()
	good := func() *mtuRecord {
		rec := &mtuRecord{ClaudeVersion: "2.1.280 (Claude Code)"}
		for _, s := range mtuSpecs() {
			rec.Arms = append(rec.Arms, mtuArm{Name: s.name, Verdict: mtuFolded, Writes: s.markers})
		}
		return rec
	}
	if version, reason, ok := good().fixtureWorthy(); !ok || version != "2.1.280" {
		t.Fatalf("a good record was refused or misversioned: %q %q", version, reason)
	}
	noResult := good()
	noResult.Arms[1].Verdict = mtuNoResult
	if _, reason, ok := noResult.fixtureWorthy(); !ok {
		t.Errorf("a no-result arm is an answer, not a refusal: %s", reason)
	}
	tests := []struct {
		name   string
		break_ func(*mtuRecord)
	}{
		{"unreadable version", func(r *mtuRecord) { r.ClaudeVersion = "<unavailable: exec>" }},
		{"missing arm", func(r *mtuRecord) { r.Arms = r.Arms[:3] }},
		{"arms out of order", func(r *mtuRecord) { r.Arms[0], r.Arms[1] = r.Arms[1], r.Arms[0] }},
		{"no trigger", func(r *mtuRecord) { r.Arms[2].Verdict = mtuNoTrigger }},
		{"instrument broke", func(r *mtuRecord) { r.Arms[0].Verdict = mtuInstrumentBroke }},
		{"one of two writes", func(r *mtuRecord) { r.Arms[3].Writes = r.Arms[3].Writes[:1] }},
	}
	for _, tc := range tests {
		rec := good()
		tc.break_(rec)
		if _, reason, ok := rec.fixtureWorthy(); ok || reason == "" {
			t.Errorf("%s: fixtureWorthy = (%q, %v), want a named refusal", tc.name, reason, ok)
		}
	}
}
