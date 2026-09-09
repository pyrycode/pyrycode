//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// #2251 — one capture of claude's system/init line taken with an effort ACTUALLY
// SET, on both of the daemon's effort paths, under production's approval spawn.
//
// # What was missing, and why the absence proved nothing
//
// 33 committed captures in this directory carry a decodable system/init payload,
// 58 init lines across six claude releases. Every one carries claude_code_version
// and permissionMode; NONE carries an effort key. That census cannot be read as
// "claude does not publish effort", because none of those runs ever set one:
//
//   - claudeSettingsArgs (internal/sessions) appends `--effort <level>` to the
//     launch argv, and since #2085 that is the ordinary path for a new
//     conversation. No capture at any version carries the flag.
//   - Pool.UpdateSettings (internal/sessions) writes `/effort <level>` as an
//     ordinary user turn. Measured once, 2026-08-19 at claude 2.1.220 — see
//     interactive_stream_inband_model_test.go's header, § "Effort has no such
//     observable" — and never re-read since.
//
// So this run sets one on BOTH paths, at two DIFFERENT levels, and records what
// the line carries. An absent effort key is a valid and useful result: #2252 then
// declares two fields rather than three, instead of declaring one claude does not
// send.
//
// # The two witnesses, and why they are the point
//
// A capture that finds no effort key is worthless unless it can also prove an
// effort was set, or "claude does not publish it" cannot be told from "nobody
// asked". The record therefore carries both witnesses beside the finding, in
// effort_capture: launch_flag_seen (the `--effort <A>` pair located in the
// RECORDED argv, adjacent — see effortInitLaunchFlagSeen for why adjacency rather
// than presence) and acknowledgement (claude's own text for the `/effort <B>`
// turn, read from THAT turn's window and not from the first assistant line in the
// capture).
//
// # What this rides, and what it deliberately does not rebuild
//
// runSetModeChild drives the child; bypassArgvApprovalArgs, bypassArgvSocket and
// bypassArgvServe supply production's approval argv and the stub socket that makes
// the init line report pyry_approve connected. Three turns, because claude emits
// one system/init line per turn (measured, #1582): a no-tool prompt, the bare
// `/effort <B>` text, and a second no-tool prompt. #2229's line-type census
// machinery is not rebuilt — nothing here needs a staged, detected turn.
//
// The prompts ban tool use. The approval socket is stood up so the SPAWN SHAPE is
// production's, not so an approval fires; what a tool-using turn on this argv does
// is bypass_approval_argv_probe_test.go's measurement and is already committed.
//
// # Reproduce
//
//	export CLAUDE_CODE_OAUTH_TOKEN=...
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run '^TestRealClaude_EffortInitCapture$' ./internal/e2e/realclaude/

const (
	// effortInitModel must publish supportsEffort. NOT haiku, and that needs no
	// discovery run: initialize_control_v2.1.239.json's models list publishes
	// supportsEffort true with all five levels on every non-haiku row (default,
	// sonnet, fable, opus) and neither key on both haiku rows.
	effortInitModel = "claude-sonnet-5"

	// The two levels, both drawn from the five that same response publishes, and
	// deliberately different — a second init line reporting the level the first one
	// was launched with would be unreadable.
	effortInitLaunchLevel = "low"
	effortInitInBandLevel = "high"

	// effortInitInBandPrompt is the BARE command with no surrounding prose: #2138
	// establishes that a message whose text is exactly the slash command is what a
	// real claude runs as one, and it is the form Pool.UpdateSettings writes.
	effortInitInBandPrompt = "/effort " + effortInitInBandLevel

	effortInitFamilyPrefix = "effort_init"
	effortInitArmName      = "sonnet_effort"

	// effortInitFixtureVersion is the release this capture is promotable at. A
	// record from any other release is refused rather than written under a name that
	// claims this one — ccapRecord.fixtureWorthy's rule, so a claude upgrade is a
	// loud instruction to re-capture rather than a pin that quietly measures
	// something else.
	effortInitFixtureVersion = "2.1.259"

	effortInitWorkdirName    = "effort-init-capture"
	effortInitMaxTurns       = "6"
	effortInitArtifactPrefix = "pyry-effort-init-"
	effortInitRecordName     = "effort_init_record.json"

	// effortInitAckCap bounds the one unbounded field this capture adds. The text is
	// model-composed, so a cap is the difference between recording an answer and
	// recording however much claude decided to say.
	effortInitAckCap = 4096

	// effortInitKeyCap bounds the recorded key list per init line. The observed sets
	// run 19 to 24 keys; the cap exists so a pathological line cannot make the record
	// unbounded, and a line that trips it is visible as a truncated list rather than
	// as a silent one.
	effortInitKeyCap = 128

	// effortInitClassRunLocal covers the two run-local temp paths this probe itself
	// mints. bypassArgvRedactor already substitutes them into the recorded ARGV with
	// informative placeholders; this class is the whole-record sweep behind that one,
	// so a path echoed anywhere else is covered too.
	effortInitClassRunLocal = "run_local_temp_path"

	// effortInitForceEnv FORCES a re-capture over an existing fixture. It is not the
	// gate — see the gate's own comment in TestRealClaude_EffortInitCapture — and a
	// run that never sets it still captures whenever the fixture is absent.
	effortInitForceEnv = "PYRY_PROBE_EFFORT_INIT_RECAPTURE"
)

// effortInitLine is one system/init line's answer: its FULL sorted key set plus
// the three values with an operator consumer.
//
// EffortPresent is separate from Effort, and that separation is the measurement. A
// line carrying `"effort": ""` and a line carrying no effort key at all are
// different findings — #2252 declares a field for the first and must not for the
// second — and a bare string cannot tell them apart.
type effortInitLine struct {
	Index             int      `json:"index"`
	Keys              []string `json:"keys"`
	KeysTruncated     bool     `json:"keys_truncated"`
	ClaudeCodeVersion string   `json:"claude_code_version"`
	PermissionMode    string   `json:"permission_mode"`
	Effort            string   `json:"effort"`
	EffortPresent     bool     `json:"effort_present"`
}

// effortInitObservation is the effort_capture block on setModeFixtureRecord. No
// omitempty anywhere: every false and every empty string here is a finding.
type effortInitObservation struct {
	LaunchLevel         string `json:"launch_level"`
	InBandLevel         string `json:"inband_level"`
	LaunchFlagSeen      bool   `json:"launch_flag_seen"`
	InBandPrompt        string `json:"inband_prompt"`
	Acknowledgement     string `json:"acknowledgement"`
	AcknowledgementSeen bool   `json:"acknowledgement_seen"`

	// MCPServers is the init line's own mcp_servers value, verbatim and truncated —
	// the evidence that this capture ran under the approval spawn shape rather than
	// under a bare argv, which AC 1 requires and which nothing else in the record
	// states.
	MCPServers string `json:"mcp_servers"`

	InitLines []effortInitLine `json:"init_lines"`

	// CredentialScanApplied is the deny-scan's per-class report, so "the net ran" is
	// a recorded fact rather than an absence — a needle read from an unset
	// environment variable is skipped silently, not fatally.
	//
	// There is deliberately no substitution-COUNT table beside it, which
	// dropcapRecord does carry. The substitution here runs on the MARSHALLED record,
	// so counts describing it cannot be inside it; they are logged instead.
	CredentialScanApplied map[string]bool `json:"credential_scan_applied"`
}

// --- the fixture family --------------------------------------------------------

// effortInitFixtureName mints this family's name in bypassArgvFixtureName's shape:
// a literal prefix no input can reach, the version through versionSlug and the arm
// through modeSwitchNameToken, which maps every path metacharacter to `_` so no arm
// token can land a capture outside testdata/.
func effortInitFixtureName(versionToken, arm string) string {
	return fmt.Sprintf("%s_v%s_%s.json",
		effortInitFamilyPrefix, versionSlug(versionToken), modeSwitchNameToken(arm))
}

func effortInitFixturePath(t *testing.T, versionToken, arm string) string {
	t.Helper()
	return filepath.Join(packageDir(t), "testdata", effortInitFixtureName(versionToken, arm))
}

// effortInitFixtureRel is the same file relative to the package directory, which is
// the test binary's working directory. The absence GATE reads this one because it
// runs before any t.Helper has a testing.T's packageDir to mint from;
// TestEffortInitFixtureName_StaysOutOfTheOtherTestdataGlobs binds the two spellings
// so the gate cannot arm against a file the writer never writes.
var effortInitFixtureRel = filepath.Join("testdata",
	effortInitFixtureName(effortInitFixtureVersion, effortInitArmName))

// --- the arm and its turns -----------------------------------------------------

// effortInitArm builds the single arm, inline, as bypassArgvArm builds its five and
// for the same reason: setModeArms is #1595's live 2.1.220 measurement, and an arm
// appended there would join that run AND mint into its committed family.
//
// launchYOLO stays false and targetMode stays empty. The bypass flag would make
// this a different spawn than the daemon's ordinary one, and this measurement's
// second path is a user TURN rather than a set_permission_mode control frame.
func effortInitArm(mcpConfigPath string) setModeArm {
	return setModeArm{
		name: effortInitArmName,
		extraLaunchArgs: append(bypassArgvApprovalArgs(mcpConfigPath),
			"--effort", effortInitLaunchLevel),
	}
}

// The two probe turns that bracket the in-band one. Both ban tool use, so the
// blast radius of the approval spawn is zero, and both carry the run nonce —
// ccapPrimePrompt's shape. The nonce gives dropcapRedactor's prompt_nonce class a
// value that is actually in the record, and it keeps turn 3 from being a question
// claude can answer with "I already did that".
func effortInitPromptOne(nonce int64) string {
	return fmt.Sprintf("Reply with exactly the word READY and nothing else. Do not use any tools, "+
		"do not comment on the task, and do nothing else. run=%d", nonce)
}

func effortInitPromptThree(nonce int64) string {
	return fmt.Sprintf("Reply with exactly the word DONE and nothing else. Do not use any tools, "+
		"do not comment on the task, and do nothing else. run=%d", nonce)
}

// --- reading the capture -------------------------------------------------------

// effortInitLaunchFlagSeen reports whether argv carries `--effort <level>` as an
// ADJACENT pair.
//
// Adjacency rather than presence, and that is AC 2's witness rather than a
// nicety: `bypassArgvNames(argv, "--effort")` is true of an argv carrying the flag
// with some other level, and of one where the flag is last and has no value at
// all. Either reading turns an absent effort key back into the unanswerable
// question this whole capture exists to close.
func effortInitLaunchFlagSeen(argv []string, level string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--effort" && argv[i+1] == level {
			return true
		}
	}
	return false
}

// effortInitSummarise reads every system/init line in events and returns, per
// line, its full sorted key set and the three values with an operator consumer.
//
// It decodes into map[string]json.RawMessage rather than into a struct this file
// guessed, for bypassArgvInitMCPServers' reason: the key set drifts across claude
// releases — 19, 20, 22 and 24 keys are all committed here — and a named-field
// decode reports "absent" for a spelling it merely did not anticipate. The keys ARE
// the measurement, so they are read as data.
//
// A retained non-JSON line is a JSON string in the record and fails the decode; it
// is skipped rather than aborting the scan of the lines after it. The Index is into
// the recorded events, so a reader can find the line a summary row describes.
func effortInitSummarise(events []json.RawMessage) []effortInitLine {
	out := []effortInitLine{}
	for i, ev := range events {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(ev, &fields); err != nil {
			continue
		}
		if effortInitString(fields["type"]) != "system" || effortInitString(fields["subtype"]) != "init" {
			continue
		}
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		truncated := false
		if len(keys) > effortInitKeyCap {
			keys, truncated = keys[:effortInitKeyCap], true
		}
		effort, present := fields["effort"]
		out = append(out, effortInitLine{
			Index:             i,
			Keys:              keys,
			KeysTruncated:     truncated,
			ClaudeCodeVersion: effortInitString(fields["claude_code_version"]),
			PermissionMode:    effortInitString(fields["permissionMode"]),
			Effort:            effortInitString(effort),
			EffortPresent:     present,
		})
	}
	return out
}

// effortInitString renders one raw value as a string: the decoded string when it is
// one, the compacted JSON otherwise. A non-string effort — a number, an object — is
// exactly the shape a hand-built decode target would get wrong, so it is recorded as
// what it is rather than dropped to "".
func effortInitString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// effortInitValues collects the distinct values of one key across every
// system/init line — the two claude-minted strings the redaction table cannot be
// built from at construction time, session_id and messaging_socket_path. Distinct
// because a three-turn child emits the key once per turn with the same value, and a
// duplicate rule buys nothing.
func effortInitValues(events []json.RawMessage, key string) []string {
	out := []string{}
	for _, line := range events {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			continue
		}
		if effortInitString(fields["type"]) != "system" || effortInitString(fields["subtype"]) != "init" {
			continue
		}
		if v := effortInitString(fields[key]); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// effortInitTurnWindow returns the lines of one 1-based turn. setModeRecorder marks
// a boundary at each result line, so turn N runs from the line after boundary N-1
// through boundary N inclusive. Out-of-order and out-of-range boundaries are skipped
// exactly as setModeTurnWindows skips them; a turn that never closed has no window
// and reads as nil.
func effortInitTurnWindow(lines []json.RawMessage, boundaries []int, turn int) []json.RawMessage {
	start, seen := 0, 0
	for _, b := range boundaries {
		if b < start || b >= len(lines) {
			continue
		}
		seen++
		if seen == turn {
			return lines[start : b+1]
		}
		start = b + 1
	}
	return nil
}

// effortInitAssistantText joins every assistant TEXT block in one turn's window.
//
// Text blocks only. A thinking block is model-composed reasoning rather than
// claude's answer, and it precedes the text block in every committed capture of
// this family — an extractor taking the first block would record the wrong thing
// and read as a green witness.
//
// The ok result is what keeps a turn that produced no assistant text at all
// distinguishable from one whose answer was empty; the record spells it as
// acknowledgement_seen.
func effortInitAssistantText(window []json.RawMessage, limit int) (string, bool) {
	var parts []string
	for _, line := range window {
		var env struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &env); err != nil || env.Type != "assistant" {
			continue
		}
		for _, block := range env.Message.Content {
			if block.Type == "text" && block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return truncateString(strings.Join(parts, "\n"), limit), true
}

// effortInitPromotable answers whether a record may be written under
// effortInitFixtureRel, and returns the refusal in words when it may not.
//
// Two rules, and both are about a fixture that would MISLEAD rather than merely
// disappoint. A record from another claude release committed under this name would
// silently re-point the streamsup pin at a line nobody looked at — the argument
// ccapRecord.fixtureWorthy makes. And fewer than two init lines cannot answer AC 1
// at all, since the whole comparison is one line before the in-band change and one
// after it.
//
// `claude --version` prints "<version> (Claude Code)", so the comparison is on the
// leading token; an "<unavailable: ...>" version fails it too, which is correct — a
// record that could not read the release it was taken at cannot vouch for one.
func effortInitPromotable(rec *setModeFixtureRecord, lines []effortInitLine) (string, bool) {
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != effortInitFixtureVersion {
		return fmt.Sprintf("the record was taken at claude %q but this family is pinned at %q; "+
			"bump effortInitFixtureVersion and the streamsup reader's effortInitCaptureVersion "+
			"together, then re-run", got, effortInitFixtureVersion), false
	}
	if len(lines) < 2 {
		return fmt.Sprintf("the record carries %d system/init line(s); AC 1 needs at least two — one "+
			"from the child launched with --effort %s and a later one after the in-band /effort %s",
			len(lines), effortInitLaunchLevel, effortInitInBandLevel), false
	}
	return "", true
}

// --- the redaction pass --------------------------------------------------------

// effortInitPass carries the redactor and the deny-scan across the two hooks
// runSetModeChild calls: fill runs before the record is marshalled, screen runs on
// the marshalled bytes. Both fire from the test goroutine after the child has
// exited, so the redactor's unsynchronised counters need no lock.
type effortInitPass struct {
	red         *dropcapRedactor
	scanner     *dropcapScanner
	artifactDir string

	// Decided in fill, where the record is legible, and read in screen, which sees
	// only bytes. A refusal blocks the IN-REPO write alone: the artifact copy is
	// written regardless, or a run that measured the wrong release would leave
	// nothing to diagnose it with.
	promotable bool
	refusal    string
}

// fill records the observation and extends the redaction table with the two values
// only claude could supply.
func (p *effortInitPass) fill(t *testing.T, rec *setModeFixtureRecord) {
	t.Helper()

	for _, sid := range effortInitValues(rec.StdoutEvents, "session_id") {
		p.red.addValueClass(dropcapClassSessionID, "$SESSION_ID", sid)
	}
	for _, sock := range effortInitValues(rec.StdoutEvents, "messaging_socket_path") {
		p.red.addPathClass(dropcapClassMessagingSocket, "$MESSAGING_SOCKET", sock)
		// The same value the table removes is the value the net looks for, so the
		// clean scan below is a check on the table rather than an agreement with it.
		p.scanner.addDynamicPath(dropcapClassMessagingSocket, sock)
	}

	lines := effortInitSummarise(rec.StdoutEvents)
	ack, ackSeen := effortInitAssistantText(
		effortInitTurnWindow(rec.StdoutEvents, rec.TurnBoundaries, 2), effortInitAckCap)
	servers, _ := bypassArgvInitMCPServers(rec.StdoutEvents)

	rec.EffortCapture = &effortInitObservation{
		LaunchLevel:         effortInitLaunchLevel,
		InBandLevel:         effortInitInBandLevel,
		LaunchFlagSeen:      effortInitLaunchFlagSeen(rec.Argv, effortInitLaunchLevel),
		InBandPrompt:        effortInitInBandPrompt,
		Acknowledgement:     ack,
		AcknowledgementSeen: ackSeen,
		MCPServers:          servers,
		InitLines:           lines,
		// Taken AFTER the socket needle above, so the report describes the net that
		// actually ran on these bytes.
		CredentialScanApplied: p.scanner.applied(),
	}
	p.refusal, p.promotable = effortInitPromotable(rec, lines)

	for _, line := range lines {
		t.Logf("#2251: init line %d: claude_code_version=%q permissionMode=%q effort=%q present=%v "+
			"keys=%v", line.Index, line.ClaudeCodeVersion, line.PermissionMode, line.Effort,
			line.EffortPresent, line.Keys)
	}
	t.Logf("#2251: launch_flag_seen=%v acknowledgement_seen=%v mcp_servers=%s\n  acknowledgement: %s",
		rec.EffortCapture.LaunchFlagSeen, ackSeen, servers, p.red.str(ack))
}

// screen redacts the marshalled record, runs the deny-scan, and decides what lands.
//
// The order is the whole guard. Nothing is written until the scan passes: not the
// fixture, not the artifact copy. On a hit the failure names the CLASSES and never
// the value — putting it in CI output is exactly the exposure the scan exists to
// prevent — which is dropcapWriteRecord's discipline, restated here because this
// record is a different shape and cannot go through that writer.
func (p *effortInitPass) screen(t *testing.T, data []byte) ([]byte, bool) {
	t.Helper()

	out := p.red.redact(data)
	hits, notApplied := p.scanner.scan(out)
	if len(hits) > 0 {
		t.Errorf("#2251: the deny-scan found %d denied class(es) still present in the record: %v\n"+
			"NOTHING was written — not the fixture, not the artifact copy. Extend the redaction "+
			"table with the named class and re-run the capture. The offending value is deliberately "+
			"not printed", len(hits), hits)
		return nil, false
	}

	// Written from the SAME screened bytes, and outside the worktree: the
	// dispatcher's real-claude gate runs from a detached worktree it then removes and
	// never runs `git add`, which is how #2229's fixture was lost. This copy is what
	// #2236 recovered that one from.
	artifact := filepath.Join(p.artifactDir, effortInitRecordName)
	if err := os.WriteFile(artifact, append(out, '\n'), 0o600); err != nil {
		t.Errorf("#2251: write the artifact copy: %v", p.red.str(err.Error()))
	}
	t.Logf("#2251: substitutions %+v; deny classes not applied %v\n  artifact copy: %s",
		p.red.substitutions(), notApplied, artifact)

	if !p.promotable {
		t.Errorf("#2251: the in-repo fixture %s was NOT written: %s\nThe artifact copy above holds "+
			"the record, so the run is diagnosable and can be promoted by hand once the reason is "+
			"resolved", effortInitFixtureRel, p.refusal)
		return nil, false
	}
	return out, true
}

// --- the live capture ----------------------------------------------------------

// TestRealClaude_EffortInitCapture drives one live child and writes the capture.
//
// Ordering is load-bearing in one place: newDropcapScanner reads
// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY through os.Getenv AS DENY NEEDLES,
// and WithWorktreeAuthenticated is what re-pins them into this process. Built
// first, the scanner takes an EMPTY needle, which scan reports as not-applied —
// silently skipped, never fatal. The credential net would be off while every
// message still read green.
//
// NOT t.Parallel(): WithWorktreeAuthenticated reaches t.Setenv.
//
// It PASSES on every recorded outcome. A child that refuses to launch on
// `--effort low` beside the four approval flags is a finding, an absent effort key
// is THE finding this ticket was opened to record, and a `/effort` turn claude
// answers with a refusal is a finding. Only an instrument that measured nothing
// fails: zero stdout lines (runSetModeChild's own fatal), a deny-scan hit, and a
// record that cannot answer AC 1.
func TestRealClaude_EffortInitCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE, deliberately, and not a PYRY_PROBE_*
	// variable. `make e2e-realclaude` sets no custom variable, so an env-gated probe
	// skips on the ENV check BEFORE the credential check: the live gate passes
	// vacuously and the fixture never lands. That is CLAUDE.md § Testing's #1763
	// failure exactly — a green gate and a spent budget look identical whether the
	// bytes landed or not. #2089 and #2229 both argue it in full.
	force := os.Getenv(effortInitForceEnv) == "1"
	if _, err := os.Stat(effortInitFixtureRel); err == nil && !force {
		t.Skipf("#2251: the fixture %s already exists, so there is nothing to capture and this "+
			"costs no claude turn.\nForce a re-capture (a new claude version, or a suspected shape "+
			"change) with:\n  %s=1 go test -tags e2e_realclaude -timeout 20m -v \\\n"+
			"    -run '^TestRealClaude_EffortInitCapture$' ./internal/e2e/realclaude/",
			effortInitFixtureRel, effortInitForceEnv)
	}

	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials; MUST precede the scanner
	pyryBin := ensurePyryBuilt(t)        // the binary the mcp-config's command names
	versionRaw, versionToken := captureClaudeVersion(t)
	t.Logf("#2251: claude version %q (token %q), model %s, --effort %s then /effort %s",
		versionRaw, versionToken, effortInitModel, effortInitLaunchLevel, effortInitInBandLevel)

	// A fresh empty directory, deliberately not a git repo: no branch name and no
	// file content can reach a payload. dropcapRedactionRationale's argument.
	workdir := filepath.Join(home, effortInitWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2251: create workdir: %v", err)
	}

	// Deliberately NOT t.TempDir(): the operator needs the record after the test
	// ends, and it must outlive a verification worktree's removal.
	artifactDir, err := os.MkdirTemp("", effortInitArtifactPrefix)
	if err != nil {
		t.Fatalf("#2251: create artifact dir: %v", err)
	}

	// BOUND BEFORE THE CONFIG IS WRITTEN AND BEFORE THE CHILD STARTS, so the mcp
	// server cannot race a not-yet-listening socket. shortSocketPath is reused rather
	// than reinvented: macOS caps a Unix socket path near 104 bytes and a path under
	// the long, authenticated pinned HOME can overrun it.
	socketPath := shortSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		// The path is test-owned and temporary, so naming it is safe and is the only
		// thing that makes an EADDRINUSE or a too-long path diagnosable.
		t.Fatalf("#2251: listen on the stub control socket %s: %v", socketPath, err)
	}
	sock := &bypassArgvSocket{}
	approvals := &bypassArgvApprovalLog{}
	sock.attach(approvals)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		// An accept loop rather than a single accept: control.Approve opens a fresh
		// connection per approval. This capture's prompts ask for no tool, so the
		// expected count is zero — but a server that is merely LISTENING is what makes
		// the init line report pyry_approve connected, and a zero count recorded
		// against a live socket is a stronger statement than no socket at all.
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			bypassArgvServe(t, conn, sock)
		}
	}()
	// A cleanup, so a t.Fatalf below still closes the listener and JOINS the
	// goroutine before the binary moves on — an un-joined server goroutine outliving
	// the test is what -race reports.
	t.Cleanup(func() {
		_ = ln.Close()
		<-serverDone
	})

	// Transcribed from renderMCPServersConfig rather than called: that lives in
	// package main. The pyry_approve entry alone, as bypassArgvArm's probe does —
	// a file-transfer server nothing here calls would add a process per spawn and
	// change no reading. Mode 0600: not secret, but an execution instruction claude
	// obeys, and a world-writable one in a shared temp directory is a footgun.
	cfgPath := filepath.Join(t.TempDir(), "mcp-approve.json")
	cfgDoc := fmt.Sprintf(`{"mcpServers":{"pyry_approve":{"command":%q,"args":["mcp-approve","-pyry-socket",%q]}}}`,
		pyryBin, socketPath)
	if err := os.WriteFile(cfgPath, []byte(cfgDoc), 0o600); err != nil {
		t.Fatalf("#2251: write the mcp-approve config %s: %v", cfgPath, err)
	}

	// A REAL nonce, never 0: newDropcapRedactor formats it with strconv.FormatInt, so
	// a zero installs "0" as a one-byte substitution rule and rewrites every zero
	// digit in the record. The prompts carry it, so the value the table substitutes is
	// one the record actually contains.
	nonce := time.Now().UnixNano()
	red := newDropcapRedactor(home, artifactDir, workdir, "", "", nonce)
	red.addPathClass(effortInitClassRunLocal, "$RUN_LOCAL_TEMP", cfgPath)
	red.addPathClass(effortInitClassRunLocal, "$RUN_LOCAL_TEMP", socketPath)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	pass := &effortInitPass{red: red, scanner: &scanner, artifactDir: artifactDir}
	t.Logf("#2251 capture artifacts: %s", red.str(artifactDir))

	cfg := setModeChildConfig{
		model:       effortInitModel,
		promptOne:   effortInitPromptOne(nonce),
		promptTwo:   effortInitInBandPrompt,
		promptThree: effortInitPromptThree(nonce),
		maxTurns:    effortInitMaxTurns,
		fixturePath: effortInitFixturePath,
		approval:    approvals.observe,
		// The narrow, informative pass over the recorded argv and stderr; the
		// whole-record sweep is pass.screen behind it.
		redactRunLocal: bypassArgvRedactor(cfgPath, socketPath),
		fillRecord:     pass.fill,
		screenFixture:  pass.screen,
	}

	rec := runSetModeChild(t, claudeBin, workdir, effortInitArm(cfgPath), versionRaw, versionToken, cfg)
	sock.attach(nil)

	if _, sawInit := bypassArgvInitMCPServers(rec.StdoutEvents); !sawInit {
		t.Logf("#2251: %s — no system/init line was emitted (exit=%d, deadline_tripped=%v, %d stdout "+
			"line(s)). The child did not launch on `--effort %s` beside the four approval flags, "+
			"which is itself the answer: that argv is not viable.\n  stderr: %s",
			bypassArgvLaunchFailed, rec.ExitCode, rec.ContextDeadlineTripped, len(rec.StdoutEvents),
			effortInitLaunchLevel, rec.StderrCapture)
		return
	}
	if !pass.promotable {
		return
	}
	t.Logf("#2251: FIXTURE WRITTEN to %s — COMMIT IT. A pipeline worktree is discarded when the run "+
		"ends, so a fixture a test merely writes does not survive:\n  git add %s\n"+
		"Then fill effortInitPins in internal/streamsup/effort_init_capture_test.go from this "+
		"record's effort_capture.init_lines IN THE SAME COMMIT — that reader fatals on a landed "+
		"fixture with an empty pin.", effortInitFixtureRel, effortInitFixtureRel)
}

// --- offline self-checks -------------------------------------------------------
//
// Everything below runs with no claude, no credentials and no subprocess:
//
//	go test -tags e2e_realclaude -race -count=1 -run TestEffortInit ./internal/e2e/realclaude/

// TestEffortInitArm_CarriesTheProductionApprovalShapeAndTheEffortFlag pins the
// argv AC 1 asks for: production's four approval flags, `--effort <A>`, and NO
// bypass flag. The bypass row is the one that would silently invalidate the
// capture — a child launched with --dangerously-skip-permissions is not the
// daemon's ordinary spawn, and its init line would report a different posture.
func TestEffortInitArm_CarriesTheProductionApprovalShapeAndTheEffortFlag(t *testing.T) {
	t.Parallel()

	arm := effortInitArm("/tmp/does-not-exist/mcp.json")
	if arm.launchYOLO {
		t.Errorf("launchYOLO is true; this capture measures the ordinary approval spawn, not bypass")
	}
	if arm.targetMode != "" {
		t.Errorf("targetMode = %q, want empty; this measurement's second path is a user turn, "+
			"not a set_permission_mode control frame", arm.targetMode)
	}
	for _, flag := range []string{"--permission-prompt-tool", "--mcp-config", "--strict-mcp-config",
		"--permission-mode", "--effort"} {
		if !bypassArgvNames(arm.extraLaunchArgs, flag) {
			t.Errorf("extraLaunchArgs is missing %s: %q", flag, arm.extraLaunchArgs)
		}
	}
	if bypassArgvNames(arm.extraLaunchArgs, "--dangerously-skip-permissions") {
		t.Errorf("extraLaunchArgs carries the bypass flag: %q", arm.extraLaunchArgs)
	}
	if !effortInitLaunchFlagSeen(arm.extraLaunchArgs, effortInitLaunchLevel) {
		t.Errorf("--effort %s is not adjacent in %q", effortInitLaunchLevel, arm.extraLaunchArgs)
	}
	if effortInitLaunchLevel == effortInitInBandLevel {
		t.Errorf("the launch level and the in-band level are both %q; AC 1 requires two DIFFERENT "+
			"levels, or the second init line cannot be told from the first", effortInitLaunchLevel)
	}
}

// TestEffortInitLaunchFlagSeen_RequiresTheLevelAdjacentToTheFlag pins the witness
// AC 2 rests on. A bare `bypassArgvNames(argv, "--effort")` would report the flag
// present on an argv carrying `--effort` with some OTHER level, which is exactly
// the reading that turns an absent `effort` key back into an unanswerable question.
func TestEffortInitLaunchFlagSeen_RequiresTheLevelAdjacentToTheFlag(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"adjacent", []string{"--model", "sonnet", "--effort", "low"}, true},
		{"last pair", []string{"--effort", "low"}, true},
		{"flag with no value", []string{"--model", "sonnet", "--effort"}, false},
		{"a different level", []string{"--effort", "high"}, false},
		{"level present but not after the flag", []string{"low", "--effort", "high"}, false},
		{"no flag at all", []string{"--model", "sonnet"}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := effortInitLaunchFlagSeen(tc.argv, "low"); got != tc.want {
				t.Errorf("effortInitLaunchFlagSeen(%q, %q) = %v, want %v", tc.argv, "low", got, tc.want)
			}
		})
	}
}

// TestEffortInitSummarise_ReadsEachInitLinesKeysAndTheThreePinnedValues pins the
// summariser over hand-built lines, the absent-`effort` case included.
//
// The absent case is the one that must not read as an empty VALUE: a line that
// carries `"effort": ""` and a line that carries no such key are two different
// findings, and #2252 declares a field for one of them and not the other.
func TestEffortInitSummarise_ReadsEachInitLinesKeysAndTheThreePinnedValues(t *testing.T) {
	t.Parallel()

	events := []json.RawMessage{
		json.RawMessage(`{"type":"assistant","message":{"content":[]}}`),
		json.RawMessage(`{"type":"system","subtype":"init","claude_code_version":"2.1.259",` +
			`"permissionMode":"default","cwd":"/w"}`),
		json.RawMessage(`{"type":"system","subtype":"thinking_tokens","effort":"trap"}`),
		json.RawMessage(`"a retained non-JSON line"`),
		json.RawMessage(`{"type":"system","subtype":"init","claude_code_version":"2.1.259",` +
			`"permissionMode":"default","effort":"high"}`),
		json.RawMessage(`{"type":"system","subtype":"init","effort":""}`),
	}

	got := effortInitSummarise(events)
	if len(got) != 3 {
		t.Fatalf("summarised %d init line(s), want 3: %+v", len(got), got)
	}

	if want := []string{"claude_code_version", "cwd", "permissionMode", "subtype", "type"}; !slices.Equal(got[0].Keys, want) {
		t.Errorf("line 0 keys = %q, want %q (sorted, every key, nothing dropped)", got[0].Keys, want)
	}
	if got[0].Index != 1 {
		t.Errorf("line 0 index = %d, want 1 — the index is into the RECORDED events, so a reader "+
			"can find the line the summary describes", got[0].Index)
	}
	if got[0].ClaudeCodeVersion != "2.1.259" || got[0].PermissionMode != "default" {
		t.Errorf("line 0 = %+v, want claude_code_version 2.1.259 and permissionMode default", got[0])
	}
	if got[0].EffortPresent || got[0].Effort != "" {
		t.Errorf("line 0 reports effort %q present=%v; it carries no such key", got[0].Effort, got[0].EffortPresent)
	}

	if !got[1].EffortPresent || got[1].Effort != "high" {
		t.Errorf("line 1 = %+v, want effort high present", got[1])
	}
	if !got[2].EffortPresent || got[2].Effort != "" {
		t.Errorf("line 2 = %+v, want an effort key present with an EMPTY value — a present-but-empty "+
			"key and an absent key are different findings", got[2])
	}
}

// TestEffortInitAssistantText_ReadsTheEffortTurnAndNotTheOneBeforeIt pins AC 2's
// second witness.
//
// Turn 1's text is a decoy on purpose: an extractor that took the FIRST assistant
// text in the whole capture would record it and read as a green acknowledgement,
// so the absent-`effort` finding would rest on a witness for the wrong turn. The
// thinking block is a second decoy — it is model-composed reasoning, not claude's
// answer, and it precedes the text block in every committed capture of this family.
func TestEffortInitAssistantText_ReadsTheEffortTurnAndNotTheOneBeforeIt(t *testing.T) {
	t.Parallel()

	lines := []json.RawMessage{
		json.RawMessage(`{"type":"system","subtype":"init"}`),
		json.RawMessage(`{"type":"assistant","message":{"content":[{"type":"text","text":"turn one answer"}]}}`),
		json.RawMessage(`{"type":"result","subtype":"success"}`),
		json.RawMessage(`{"type":"system","subtype":"init"}`),
		json.RawMessage(`{"type":"assistant","message":{"content":[` +
			`{"type":"thinking","thinking":"deciding"},{"type":"text","text":"Effort set to high."}]}}`),
		json.RawMessage(`{"type":"result","subtype":"success"}`),
	}
	boundaries := []int{2, 5}

	got, ok := effortInitAssistantText(effortInitTurnWindow(lines, boundaries, 2), effortInitAckCap)
	if !ok {
		t.Fatalf("no acknowledgement found in turn 2's window")
	}
	if got != "Effort set to high." {
		t.Errorf("acknowledgement = %q, want turn 2's text block", got)
	}

	// A turn that produced no assistant text at all reads as absent, never as "".
	// The record spells that with acknowledgement_seen, so a refusal and a missing
	// turn stay distinguishable.
	if text, ok := effortInitAssistantText(effortInitTurnWindow(lines, boundaries, 3), effortInitAckCap); ok {
		t.Errorf("turn 3 does not exist, yet an acknowledgement %q was reported", text)
	}
}

// TestEffortInitRedaction_RemovesTheMessagingSocketPathTheScannerWouldCatch is
// AC 5's extension, and it asserts BOTH directions on the same bytes.
//
// One direction alone is vacuous, and not hypothetically: dropcapScanner skips a
// dynamic needle shorter than dropcapMinNeedle and REPORTS it as not-applied
// rather than failing, so a test that only scanned the redacted bytes would pass
// with the net switched off. The un-redacted scan is what proves the needle runs,
// and scanner.applied() is what proves it was not skipped.
func TestEffortInitRedaction_RemovesTheMessagingSocketPathTheScannerWouldCatch(t *testing.T) {
	t.Parallel()

	const (
		sockPath  = "/tmp/cc-socks/79504.sock"
		sessionID = "8f1d0c2e-4a6b-4c1d-9e3f-2b7a5d6c8e90"
	)
	fakeHome := t.TempDir()
	workdir := filepath.Join(fakeHome, "work")
	line := fmt.Sprintf(`{"type":"system","subtype":"init","cwd":%q,"session_id":%q,`+
		`"messaging_socket_path":%q}`, workdir, sessionID, sockPath)

	red := newDropcapRedactor(fakeHome, "", workdir, "", "", time.Now().UnixNano())
	red.addValueClass(dropcapClassSessionID, "$SESSION_ID", sessionID)
	red.addPathClass(dropcapClassMessagingSocket, "$MESSAGING_SOCKET", sockPath)

	scanner := newDropcapScanner(fakeHome, "", workdir)
	scanner.addDynamicPath(dropcapClassMessagingSocket, sockPath)
	if applied := scanner.applied(); !applied[dropcapClassMessagingSocket] {
		t.Fatalf("the %s needle was skipped as too short, so the clean scan below would pass with "+
			"the net off", dropcapClassMessagingSocket)
	}
	if hits, _ := scanner.scan([]byte(line)); !slices.Contains(hits, dropcapClassMessagingSocket) {
		t.Fatalf("the scanner does not see %q in the UN-redacted line (hits %v); it cannot then "+
			"vouch for its absence from the redacted one", sockPath, hits)
	}

	out := string(red.redact([]byte(line)))
	for _, kept := range []struct{ class, value string }{
		{dropcapClassMessagingSocket, sockPath},
		{dropcapClassSessionID, sessionID},
		{dropcapClassWorkdir, workdir},
	} {
		if strings.Contains(out, kept.value) {
			t.Errorf("the redacted line still carries a value of class %s", kept.class)
		}
	}
	if !strings.Contains(out, "$MESSAGING_SOCKET") {
		t.Errorf("no $MESSAGING_SOCKET placeholder in the redacted line: %s", out)
	}
	if hits, _ := scanner.scan([]byte(out)); len(hits) > 0 {
		t.Errorf("the redacted line still trips the deny-scan: %v", hits)
	}

	var sawSocketClass bool
	for _, s := range red.substitutions() {
		if s.Class == dropcapClassMessagingSocket && s.Count > 0 {
			sawSocketClass = true
		}
	}
	if !sawSocketClass {
		t.Errorf("the record's redaction table does not report the %s class as applied; a "+
			"substitution the file does not declare is one a reader cannot check",
			dropcapClassMessagingSocket)
	}
}

// TestEffortInitFixtureName_StaysOutOfTheOtherTestdataGlobs keeps this family's
// name from being swept up by a sibling reader's glob, and vice versa. Both
// existing readers discover their captures by pattern, and a name that matches one
// of them would be decoded through the wrong record shape — which yields zero
// values rather than a failure.
func TestEffortInitFixtureName_StaysOutOfTheOtherTestdataGlobs(t *testing.T) {
	t.Parallel()

	name := effortInitFixtureName(effortInitFixtureVersion, effortInitArmName)
	for _, glob := range []string{
		"set_permission_mode_v*.json",
		"bypass_approval_argv_v*.json",
		"bypass_reescalation_v*.json",
		"permission_mode_switch_v*.json",
		"initialize_control_v*.json",
		"compaction_v*.json",
	} {
		matched, err := filepath.Match(glob, name)
		if err != nil {
			t.Fatalf("match %s: %v", glob, err)
		}
		if matched {
			t.Errorf("%s matches another family's glob %s", name, glob)
		}
	}
	if want := filepath.Join("testdata", name); effortInitFixtureRel != want {
		t.Errorf("effortInitFixtureRel = %q but the minter produces %q; the absence gate and the "+
			"writer must agree on one path or the capture arms against a file it never writes",
			effortInitFixtureRel, want)
	}
}

// TestEffortInitPromotable_RefusesWhatCannotAnswerTheQuestion pins the in-repo
// promotion rule. A record that measured another claude release, or that carries
// fewer than the two init lines AC 1 requires, must not land under a name that
// claims otherwise — ccapRecord.fixtureWorthy's argument, and the reason a claude
// upgrade is a loud instruction to re-capture rather than a quietly wrong pin.
func TestEffortInitPromotable_RefusesWhatCannotAnswerTheQuestion(t *testing.T) {
	t.Parallel()

	two := []effortInitLine{{Index: 1}, {Index: 9}}
	cases := []struct {
		name    string
		version string
		lines   []effortInitLine
		want    bool
	}{
		{"the pinned version with two init lines", effortInitFixtureVersion + " (Claude Code)", two, true},
		{"a bare version string", effortInitFixtureVersion, two, true},
		{"another release", "2.1.260 (Claude Code)", two, false},
		{"an unreadable version", "<unavailable: exec failed>", two, false},
		{"one init line", effortInitFixtureVersion, two[:1], false},
		{"no init line at all", effortInitFixtureVersion, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &setModeFixtureRecord{ClaudeVersion: tc.version}
			reason, ok := effortInitPromotable(rec, tc.lines)
			if ok != tc.want {
				t.Errorf("effortInitPromotable = (%q, %v), want ok=%v", reason, ok, tc.want)
			}
			if !ok && reason == "" {
				t.Errorf("a refusal with no reason leaves an operator nothing to act on")
			}
		})
	}
}
