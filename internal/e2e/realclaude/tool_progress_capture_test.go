//go:build e2e_realclaude

package realclaude

// Evidence capture for #2089 — every tool_progress frame claude puts on this
// surface during ONE live turn that holds a FOREGROUND Bash call open.
//
// # Why a second probe rather than a parameter on #1260's
//
// Two deltas, and each is structural rather than cosmetic:
//
//   - #1260 BACKGROUNDS its Bash call and caps it at BASH_DEFAULT_TIMEOUT_MS=5000,
//     shorter than the heartbeat interval. Heartbeats are what a foreground call
//     in flight produces, so this probe raises the cap well past the interval and
//     asks for a plain foreground sleep. That is why the committed
//     dropped_lines_v2.1.220.json holds no tool_progress line at all.
//   - #1260 records only lines the parser DROPPED (dropcapClassifyAll). A
//     marker-less tool_progress frame emits an Unrecognized, so it is not dropped,
//     so it would be filtered out of the record — and it is the single frame this
//     capture exists to catch. The filter here is the top-level TYPE, and the
//     parser's verdict rides along per frame as DATA rather than as a gate.
//
// Everything else is reused rather than reimplemented: dropcapRecorder (line
// splitting and the `result` turn boundary), dropcapRedactor, dropcapScanner,
// dropcapMakeEntry (payload encoding, base64 arm included), parseOne and
// dropcapWaitForChild all live in dropped_line_capture_test.go.
//
// # Redaction
//
// Inherited whole from #1260, and see tpcapRedactionRationale for what this type
// specifically can carry. The one field worth naming up front is
// repl_call.inner_tool_input, the only input-bearing field in tool_progress's
// declared schema.
//
// # Running it
//
//	PYRY_PROBE_TOOL_PROGRESS_CAPTURE=1 go test -tags e2e_realclaude -timeout 15m -v \
//	  -run '^TestRealClaude_ToolProgressCapture$' ./internal/e2e/realclaude/
//
// A skip is the normal `make e2e-realclaude` outcome and carries no signal.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// tpcapEnableEnv gates the live capture; it costs one claude turn plus the sleep.
const tpcapEnableEnv = "PYRY_PROBE_TOOL_PROGRESS_CAPTURE"

// Every file-local identifier takes the tpcap prefix, for the reason #1260's
// header gives: siblings add files to this package concurrently and a
// branch-overlap check does not catch a same-package identifier collision.
const (
	tpcapTicket         = "2089"
	tpcapWorkdirName    = "tpcap-work"
	tpcapRecordName     = "tpcap-record.json"
	tpcapArtifactPrefix = "pyry-2089-capture-*"
	tpcapModel          = "haiku"
	// Well past the reported ~30 s heartbeat interval, so the tool call cannot be
	// cut short before claude emits one. #1260's 5000 is what makes its capture
	// heartbeat-free.
	tpcapBashTimeoutMS = "180000"
	// Long enough for at least two heartbeats at the reported interval, short
	// enough to stay inside the turn budget with room for spawn and teardown.
	tpcapSleepSeconds = 75
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from
	// #1260's so a record can never be mistaken for the other probe's.
	tpcapSessionID = "0b3f9c21-7d54-4e8a-8c16-2f9a4d7b6e05"
)

const (
	tpcapTurnBudget  = 5 * time.Minute
	tpcapRunExitWait = 30 * time.Second
)

const (
	tpcapFired            = "fired"
	tpcapDidNotFire       = "did-not-fire"
	tpcapInstrumentBroken = "instrument-broken"
)

const (
	tpcapTerminatedResult = "result"
	tpcapTerminatedBudget = "budget"
)

// The marker names, spelled here as literals rather than imported from
// streamsup: the record is EVIDENCE about claude's wire shape, and a census keyed
// on the production constants would agree with the matcher by construction
// instead of measuring it.
const (
	tpcapMarkerHeartbeat    = "heartbeat"
	tpcapMarkerSubagentType = "subagent_type"
	tpcapMarkerReplCall     = "repl_call"
	tpcapMarkerNone         = "none"
)

var tpcapArgs = []string{"--model", tpcapModel, "--dangerously-skip-permissions"}

const tpcapSpawnShapeDelta = "The YOLO interactive shape, identical to #1260's — see dropcapSpawnShapeDelta " +
	"for what production's non-yolo spawn adds and what that implies for system/init. Nothing about the " +
	"tool_progress family is known to depend on the approval flags; that is UNMEASURED, not ruled out."

const tpcapLimitations = "One turn, one spawn shape, one claude version, one model (" + tpcapModel + "), one " +
	"tool (Bash). Cross-version, cross-model and cross-tool stability are UNMEASURED. A variety absent from " +
	"marker_census did not appear in THIS turn, which is not the same as claude never emitting it: the " +
	"subagent-retry and repl-call varieties are not reachable from a foreground Bash call at all, so their " +
	"absence here is a property of the staging rather than of the surface. The load-bearing measurement is " +
	"the OTHER direction — unmarked_frames counts the residual bash/powershell-progress shape, whose " +
	"presence on this surface was the ticket's open question."

const tpcapRedactionRationale = "Inherited whole from #1260 (see dropcapRedactionRationale): a fresh empty " +
	"non-git workdir, a rig-authored prompt, no os.Environ() read into the record, the declared " +
	"dropcapRedactor substitution table over every string, and dropcapScanner as a fail-closed deny-scan " +
	"over the marshalled record. " +
	"WHAT THIS TYPE SPECIFICALLY CAN CARRY, named so a person deciding whether to paste this record into a " +
	"public issue is told rather than left to infer: every tool_progress field in the declared schema is an " +
	"identifier or a counter (tool_use_id, parent_tool_use_id, task_id, uuid, session_id, tool_name, " +
	"elapsed_time_seconds) EXCEPT ONE. repl_call.inner_tool_input is a tool's input verbatim. It is KEPT, " +
	"because a redacted marker payload would not be evidence of the shape; the defence for it is the " +
	"by-construction one above plus the deny-scan, and in this staging the only tool input is the rig's own " +
	"sleep command. A capture staged against a real workspace would need more."

// tpcapFrame is one captured tool_progress line. The payload half is built by
// dropcapMakeEntry so the base64 arm for invalid UTF-8 is shared rather than
// re-derived; the rest is this ticket's.
//
// events_emitted is the SHIPPED parser's verdict (parseOne), never a mirror of
// its tables — a frame with a non-zero count is one that reaches the unrecognized
// lane and puts a row in the operator's chat.
type tpcapFrame struct {
	Index                   int    `json:"index"`
	Type                    string `json:"type"`
	PayloadLenBytesCaptured int    `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int    `json:"payload_len_bytes"`
	PayloadEncoding         string `json:"payload_encoding"`
	Payload                 string `json:"payload,omitempty"`
	PayloadB64              string `json:"payload_b64,omitempty"`

	EventsEmitted int    `json:"events_emitted"`
	Marker        string `json:"marker"`
	Heartbeat     bool   `json:"heartbeat"`
	SubagentType  bool   `json:"subagent_type"`
	ReplCall      bool   `json:"repl_call"`
}

type tpcapRecord struct {
	Ticket          string   `json:"ticket"`
	ClaudeVersion   string   `json:"claude_version"`
	CapturedAt      string   `json:"captured_at"`
	IsCapture       bool     `json:"is_capture"`
	Model           string   `json:"model"`
	SpawnShape      []string `json:"spawn_shape"`
	SpawnShapeDelta string   `json:"spawn_shape_delta"`
	EnvDelta        []string `json:"env_delta"`
	Workdir         string   `json:"workdir"`
	Prompt          string   `json:"prompt"`

	Outcome       string `json:"outcome"`
	OutcomeDetail string `json:"outcome_detail"`
	TerminatedOn  string `json:"terminated_on"`

	LinesCaptured  int            `json:"lines_captured"`
	FrameCount     int            `json:"frame_count"`
	Frames         []tpcapFrame   `json:"frames"`
	MarkerCensus   map[string]int `json:"marker_census"`
	UnmarkedFrames int            `json:"unmarked_frames"`
	FramesReaching int            `json:"frames_reaching_unrecognized_lane"`

	Redaction             []dropcapSubstitution `json:"redaction"`
	RedactionRationale    string                `json:"redaction_rationale"`
	CredentialScanApplied map[string]bool       `json:"credential_scan_applied"`
	CredentialScanSkipped []string              `json:"credential_scan_skipped"`

	Limitations string `json:"limitations"`

	LinesDroppedOverCap    int `json:"lines_dropped_over_cap"`
	BytesDroppedOverCap    int `json:"bytes_dropped_over_cap"`
	PartialsDropped        int `json:"partials_dropped"`
	BlankLines             int `json:"blank_lines"`
	UnterminatedPartialLen int `json:"unterminated_partial_len"`
}

func (rec *tpcapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
}

// tpcapPrompt stages the one turn. The nonce is carried so dropcapRedactor's
// prompt_nonce class has something to substitute, and the wording forbids
// backgrounding explicitly: a backgrounded call returns immediately and produces
// no heartbeat, which is exactly how #1260's capture came back empty of this type.
func tpcapPrompt(nonce int64) string {
	return fmt.Sprintf(
		"Run exactly this command with the Bash tool, in the foreground: sleep %d. "+
			"Do NOT background it, do not change the duration, and do not run anything else. "+
			"Wait for it to finish, then reply with the single word done-%d.",
		tpcapSleepSeconds, nonce)
}

// TestRealClaude_ToolProgressCapture drives the turn and writes the record.
//
// Ordering below is load-bearing in one place: newDropcapScanner reads
// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY via os.Getenv AS DENY NEEDLES,
// and WithWorktreeAuthenticated is what re-pins them into this process's
// environment. Building the scanner first yields an EMPTY needle, which
// dropcapScanner.scan reports as notApplied — silently skipped, not fatal. The
// credential net would be off while every message still read green, so the
// scanner is built after the auth helper and the skipped classes are recorded in
// the record and logged.
func TestRealClaude_ToolProgressCapture(t *testing.T) {
	if os.Getenv(tpcapEnableEnv) != "1" {
		t.Skipf("#2089 tool_progress capture: skipped because %s != 1.\n"+
			"This is an EVIDENCE CAPTURE, not a regression gate — a skip here is the normal "+
			"`make e2e-realclaude` outcome and carries no signal about pyry's behaviour. It costs "+
			"one live claude turn holding a %ds foreground Bash call.\n"+
			"Run it explicitly:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 15m -v \\\n"+
			"    -run '^TestRealClaude_ToolProgressCapture$' ./internal/e2e/realclaude/",
			tpcapEnableEnv, tpcapSleepSeconds, tpcapEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Deliberately NOT t.TempDir(): the operator needs the record after the test
	// ends in order to commit it as the fixture.
	artifactDir, err := os.MkdirTemp("", tpcapArtifactPrefix)
	if err != nil {
		t.Fatalf("#2089: create artifact dir: %v", err)
	}

	// A fresh EMPTY directory, deliberately not a git repo: no branch names and no
	// file contents can reach a payload.
	workdir := filepath.Join(home, tpcapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2089: create workdir: %v", err)
	}
	nonce := time.Now().UnixNano()

	red := newDropcapRedactor(home, artifactDir, workdir, "", tpcapSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2089 capture artifacts: %s", red.str(artifactDir))

	rec := &tpcapRecord{
		Ticket:        tpcapTicket,
		ClaudeVersion: probeClaudeVersion(claudeBin),
		CapturedAt:    time.Now().Format(time.RFC3339),
		IsCapture:     true,
		Model:         tpcapModel,
		// A FIXED LITERAL, never harvested from os.Environ().
		EnvDelta:              []string{dropcapBashTimeoutEnv + "=" + tpcapBashTimeoutMS},
		SpawnShapeDelta:       tpcapSpawnShapeDelta,
		Workdir:               red.str(workdir),
		Frames:                []tpcapFrame{},
		MarkerCensus:          map[string]int{},
		RedactionRationale:    tpcapRedactionRationale,
		CredentialScanApplied: scanner.applied(),
		CredentialScanSkipped: []string{},
		Limitations:           tpcapLimitations,
	}
	rec.set(tpcapInstrumentBroken, "did not reach a classification point")

	// Registered before anything below can fail, so a structural t.Fatalf still
	// leaves the evidence on disk — #1260's ordering.
	t.Cleanup(func() { tpcapWriteRecord(t, artifactDir, red, scanner, rec) })

	// MUST precede the runner: Config.Env stays nil so the child inherits this
	// process's environment verbatim.
	t.Setenv(dropcapBashTimeoutEnv, tpcapBashTimeoutMS)

	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: tpcapSessionID,
		Args:      tpcapArgs,
		Stdout:    recorder,
		Logger:    slog.New(argvHandler),
	})
	if err != nil {
		rec.set(tpcapInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
			red.str(err.Error()))
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
		case <-time.After(tpcapRunExitWait):
			t.Errorf("#2089: streamsup.Run did not return within %s of cancel", tpcapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(tpcapInstrumentBroken, "no live child within %s: claude never spawned, so nothing "+
			"was on the wire to capture", dropcapSpawnWait)
		return
	}

	prompt := tpcapPrompt(nonce)
	rec.Prompt = red.str(prompt)
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.set(tpcapInstrumentBroken, "writing the turn envelope failed, so no turn was ever "+
			"driven: %v", red.str(err.Error()))
		return
	}

	select {
	case <-recorder.resultSeen:
		rec.TerminatedOn = tpcapTerminatedResult
	case <-time.After(tpcapTurnBudget):
		rec.TerminatedOn = tpcapTerminatedBudget
	}

	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	rec.Frames = tpcapCollect(t, lines, red)
	rec.FrameCount = len(rec.Frames)
	for _, f := range rec.Frames {
		rec.MarkerCensus[f.Marker]++
		if f.Marker == tpcapMarkerNone {
			rec.UnmarkedFrames++
		}
		if f.EventsEmitted != 0 {
			rec.FramesReaching++
		}
	}
	if rec.FrameCount == 0 {
		rec.set(tpcapDidNotFire, "the turn produced no tool_progress line at all")
	} else {
		rec.set(tpcapFired, "%d tool_progress frame(s), census %v", rec.FrameCount, rec.MarkerCensus)
	}

	// --- AC3 ------------------------------------------------------------------
	// Every message below reports counts and indices only. The frames themselves
	// are in the record the cleanup has already written; putting claude's bytes in
	// CI output is precisely the exposure the deny-scan exists to prevent.
	if rec.FrameCount == 0 {
		t.Fatalf("#2089: the turn recorded ZERO tool_progress frames out of %d captured lines "+
			"(terminated_on=%s). A capture recording none is vacuous — every assertion built on it "+
			"would pass without reading a byte claude sent. Raise tpcapSleepSeconds or re-check that "+
			"the call ran in the foreground before touching the marker set",
			rec.LinesCaptured, rec.TerminatedOn)
	}
	if rec.UnmarkedFrames > 0 {
		t.Fatalf("#2089: %d of %d tool_progress frames carried NONE of the three markers "+
			"(census %v). That is the residual bash/powershell-progress shape, which means the "+
			"un-gated emitter IS live on this surface and the marker set as specified is "+
			"insufficient. This is a finding to route back, not a hole to ship: the frames are in "+
			"the record, read their key set and re-refine",
			rec.UnmarkedFrames, rec.FrameCount, rec.MarkerCensus)
	}
	if rec.FramesReaching > 0 {
		t.Fatalf("#2089: %d of %d tool_progress frames still reach the unrecognized lane "+
			"(census %v). Every captured frame must be consumed emitting zero events",
			rec.FramesReaching, rec.FrameCount, rec.MarkerCensus)
	}
}

// tpcapCollect keeps every captured line of the top-level type, whatever the
// parser did with it. The filter is deliberately the TYPE and not the parser's
// verdict: filtering on "dropped" — dropcapClassifyAll's rule — would discard a
// marker-less frame, and that frame is the whole reason this capture exists.
func tpcapCollect(t *testing.T, lines []dropcapCaptured, red *dropcapRedactor) []tpcapFrame {
	t.Helper()
	out := []tpcapFrame{}
	for _, c := range lines {
		if !c.Decoded || c.Type != "tool_progress" {
			continue
		}
		// The payload half, shared with #1260 so the base64 arm for invalid UTF-8
		// is not re-derived here. The reason field it wants is spent below on the
		// marker, so it is passed empty and dropped.
		entry := dropcapMakeEntry(c, "", red)
		marker, hb, sub, repl := tpcapMarkers(c.Raw)
		out = append(out, tpcapFrame{
			Index:                   entry.Index,
			Type:                    entry.Type,
			PayloadLenBytesCaptured: entry.PayloadLenBytesCaptured,
			PayloadLenBytes:         entry.PayloadLenBytes,
			PayloadEncoding:         entry.PayloadEncoding,
			Payload:                 entry.Payload,
			PayloadB64:              entry.PayloadB64,
			EventsEmitted:           len(parseOne(t, string(c.Raw))),
			Marker:                  marker,
			Heartbeat:               hb,
			SubagentType:            sub,
			ReplCall:                repl,
		})
	}
	return out
}

// tpcapMarkers decodes the three markers out of one frame's raw bytes,
// independently of streamsup's matcher. Independence is the point: a census
// computed by calling the production decoder would agree with it by construction
// and could never report that the matcher is keyed on a field claude does not
// send.
//
// heartbeat is read through json.RawMessage and compared to the literal `true`,
// so a string "true" or a 1 is recorded as NOT a heartbeat — the same
// type-strictness the matcher applies, arrived at separately.
func tpcapMarkers(raw []byte) (marker string, heartbeat, subagentType, replCall bool) {
	var probe struct {
		Heartbeat    json.RawMessage  `json:"heartbeat"`
		SubagentType *json.RawMessage `json:"subagent_type"`
		ReplCall     *json.RawMessage `json:"repl_call"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return tpcapMarkerNone, false, false, false
	}
	heartbeat = string(probe.Heartbeat) == "true"
	subagentType = probe.SubagentType != nil && string(*probe.SubagentType) != "null"
	replCall = probe.ReplCall != nil && string(*probe.ReplCall) != "null"
	switch {
	case heartbeat:
		marker = tpcapMarkerHeartbeat
	case subagentType:
		marker = tpcapMarkerSubagentType
	case replCall:
		marker = tpcapMarkerReplCall
	default:
		marker = tpcapMarkerNone
	}
	return marker, heartbeat, subagentType, replCall
}

// tpcapWriteRecord marshals, deny-scans and writes. #1260's fail-closed rule
// verbatim: on a hit NOTHING is written and the message names the CLASS only,
// never the matched value.
func tpcapWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *tpcapRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2089: marshal record: %v", err)
		return
	}
	hits, notApplied := scanner.scan(blob)
	// A base64 payload hides its bytes from a scan of the marshalled record, so
	// the decoded bytes are scanned too.
	for i, f := range rec.Frames {
		if f.PayloadB64 == "" {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(f.PayloadB64)
		if derr != nil {
			t.Errorf("#2089: frame %d: decode base64 payload for the scan: %v", i, derr)
			return
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("#2089: deny-scan found %d denied class(es) still present in the record: %v\n"+
			"NOTHING was written — not the record, not the fixture. Extend dropcapRedactor's table "+
			"with the named class and re-run the capture. The offending value is deliberately not "+
			"printed: putting it in CI output is exactly the exposure this scan exists to prevent",
			len(hits), hits)
	}
	rec.CredentialScanSkipped = notApplied

	// Re-marshal so credential_scan_skipped ships in the written bytes. Its value
	// is a list of CLASS NAMES the scan could not apply, which is the one thing
	// that makes a silently-off credential net visible after the fact.
	blob, err = json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2089: re-marshal record: %v", err)
		return
	}

	path := filepath.Join(dir, tpcapRecordName)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2089: write record %s: %v", red.str(path), err)
		return
	}
	t.Logf("#2089 outcome=%s terminated_on=%s captured=%d frames=%d census=%v unmarked=%d "+
		"reaching_lane=%d scan_not_applied=%v\n  record: %s\n  %s",
		rec.Outcome, rec.TerminatedOn, rec.LinesCaptured, rec.FrameCount, rec.MarkerCensus,
		rec.UnmarkedFrames, rec.FramesReaching, notApplied, red.str(path), red.str(rec.OutcomeDetail))
}

// TestTpcapBashTimeoutOutlastsTheSleep runs offline. A BASH_DEFAULT_TIMEOUT_MS
// shorter than the staged sleep kills the tool call before the heartbeat
// interval elapses, which is how #1260's capture came back with no frames of
// this type — a failure that looks like "claude does not emit these" rather than
// like a mis-set constant.
func TestTpcapBashTimeoutOutlastsTheSleep(t *testing.T) {
	t.Parallel()
	timeoutMS, err := strconv.Atoi(tpcapBashTimeoutMS)
	if err != nil {
		t.Fatalf("tpcapBashTimeoutMS = %q is not an integer: %v", tpcapBashTimeoutMS, err)
	}
	sleepMS := tpcapSleepSeconds * 1000
	if timeoutMS <= sleepMS {
		t.Errorf("tpcapBashTimeoutMS = %d ms but the staged sleep is %d s (%d ms): the tool call "+
			"would be killed before it finishes and the capture would measure the timeout, not claude",
			timeoutMS, tpcapSleepSeconds, sleepMS)
	}
}
