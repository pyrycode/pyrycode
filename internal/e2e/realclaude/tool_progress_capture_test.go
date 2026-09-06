//go:build e2e_realclaude

package realclaude

// Evidence capture for #2089 — every tool_progress frame claude puts on this
// surface during ONE live turn that holds a FOREGROUND Bash call open.
//
// # Why a second probe rather than a parameter on #1260's
//
// Two deltas, and each is structural rather than cosmetic:
//
//   - #1260 caps its Bash call at BASH_DEFAULT_TIMEOUT_MS=5000, shorter than the
//     30 s heartbeat interval, so the call leaves the foreground before a tick can
//     land. Heartbeats are what a foreground call still in flight produces, so
//     this probe raises the cap well past the interval and holds the call open
//     itself. That is why the committed dropped_lines_v2.1.220.json holds no
//     tool_progress line at all.
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
// # What the first live run measured (2026-09-06, claude 2.1.259, haiku)
//
// Nothing about the surface — and the instrument could not say so. The staging
// then asked claude for `sleep 75` in the foreground; the turn ended in 9.11 s
// having emitted 13 lines and zero tool_progress frames, and the record kept only
// tool_progress frames, so there was no way to tell whether claude had shortened
// the sleep, requested a short tool timeout, backgrounded the call, or never run
// it. The zero-frame message advised raising the sleep, which was very likely the
// wrong advice.
//
// Two things changed as a result, and they are the reason this file looks the way
// it does:
//
//   - The duration is the RIG's, not the model's. `cat <fifo>` blocks until
//     tpcapHoldFIFO releases the write end, and the rendezvous is positive
//     evidence that a foreground call actually started. stagingVerdict turns that
//     into the three-way answer the first run lacked.
//   - The record carries a content-free census of everything else on the wire
//     (line_type_census, tool_calls, tool_result_errors), so a run that fires
//     nothing still says what claude did instead.
//
// Two facts read out of the 2.1.259 binary while diagnosing that run, both of
// which the ticket body had left open:
//
//   - The heartbeat is a setInterval at 30000 ms, so the FIRST tick lands at
//     t+30s. A call that leaves the foreground sooner can never produce one.
//   - The `CLAUDE_CODE_REMOTE`/`CLAUDE_CODE_CONTAINER_ID` guard wraps ONLY the
//     bash_progress/powershell_progress branch, in both emitters. The heartbeat,
//     subagent-retry and repl-call arms sit outside it, so they are not env-gated
//     and this surface can receive them. The residual variety's reachability is
//     still what unmarked_frames measures.
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
// `make e2e-realclaude` on an authenticated machine, and nothing else. Unlike its
// seven sibling probes this one is gated on the FIXTURE'S ABSENCE rather than on
// an env var, so the live gate the ticket is labelled for is what produces the
// evidence; it disarms as soon as the fixture exists. The gate comment in
// TestRealClaude_ToolProgressCapture argues that break.
//
// To force a re-capture at a new claude version, over an existing fixture:
//
//	PYRY_PROBE_TOOL_PROGRESS_CAPTURE=1 go test -tags e2e_realclaude -timeout 15m -v \
//	  -run '^TestRealClaude_ToolProgressCapture$' ./internal/e2e/realclaude/
//
// A skip still carries no signal about pyry's behaviour — but read WHICH skip:
// "fixture already exists" is the steady state, while a skip from
// WithWorktreeAuthenticated means the machine has no claude login and the
// evidence was not produced.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// tpcapEnableEnv FORCES a re-capture when the fixture already exists. It is not
// the gate — see the gate comment in TestRealClaude_ToolProgressCapture for why
// this probe arms itself instead of waiting to be asked.
const tpcapEnableEnv = "PYRY_PROBE_TOOL_PROGRESS_CAPTURE"

// tpcapFixturePath is where a good capture LANDS, in-repo, ready to `git add`.
// The streamsup-side reader (capturedToolProgressLines) names the same file
// through its own package constant and enforces the same version pin from the
// other end; the two are deliberately not shared, because that reader takes no
// path parameter by design.
const (
	tpcapFixtureVersion = "2.1.259"
	tpcapFixturePath    = "testdata/tool_progress_v" + tpcapFixtureVersion + ".json"
)

// Every file-local identifier takes the tpcap prefix, for the reason #1260's
// header gives: siblings add files to this package concurrently and a
// branch-overlap check does not catch a same-package identifier collision.
const (
	tpcapTicket         = "2089"
	tpcapWorkdirName    = "tpcap-work"
	tpcapFIFOName       = "tpcap-hold"
	tpcapRecordName     = "tpcap-record.json"
	tpcapArtifactPrefix = "pyry-2089-capture-*"
	tpcapModel          = "haiku"
	// Well past the hold below, so the tool call cannot be cut short before claude
	// emits a heartbeat. #1260's 5000 is what makes its capture heartbeat-free.
	// Note this only raises the DEFAULT: claude's own default is 120000 (KTe in
	// the 2.1.259 binary), and a timeout the MODEL requests wins over both, which
	// is why the prompt forbids requesting one.
	tpcapBashTimeoutMS = "180000"
	// Read out of the 2.1.259 binary, not from the report: the heartbeat is a
	// setInterval at `var Cct=30000` whose first tick therefore lands at t+30s.
	// Anything shorter than one tick cannot produce a frame no matter how well the
	// staging behaves.
	tpcapHeartbeatIntervalSeconds = 30
	// Two full ticks plus slack. The rig holds the call open for exactly this
	// long — see tpcapHoldFIFO for why the duration is the rig's decision and not
	// the model's.
	tpcapHoldSeconds = 75
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from
	// #1260's so a record can never be mistaken for the other probe's.
	tpcapSessionID = "0b3f9c21-7d54-4e8a-8c16-2f9a4d7b6e05"
)

const (
	tpcapTurnBudget  = 5 * time.Minute
	tpcapRunExitWait = 30 * time.Second
	tpcapHold        = tpcapHoldSeconds * time.Second
	// How long to wait for claude's `cat` to open the FIFO. Generous: it covers
	// model latency and the tool round-trip, and overrunning it is recorded as
	// "the foreground call never started", which is a measurement rather than a
	// flake.
	tpcapRendezvousWait = 90 * time.Second
	// How long TestTpcapHoldFIFO gives release() to return with no reader present.
	// Kept well under probeFIFOReleaseDeadline, which is release's own backstop:
	// the offline test must fail on a wakeup that does not take, not wait for the
	// backstop that merely stops it hanging.
	tpcapNoReaderReleaseBudget = 5 * time.Second
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
	"by-construction one above plus the deny-scan, and in this staging the only tool input is a `cat` of " +
	"the rig's own FIFO. A capture staged against a real workspace would need more."

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

	// The staging measurement, added 2026-09-06 after the first live run came
	// back with zero frames and no way to tell WHY. Without these a did-not-fire
	// record cannot distinguish "claude never ran the command" from "it ran but
	// was cut short" from "it ran the whole window and this surface simply does
	// not carry heartbeats" — and only the third is a finding about pyry.
	ForegroundCallObserved bool    `json:"foreground_call_observed"`
	ForegroundHeldSeconds  float64 `json:"foreground_call_held_seconds"`
	TurnSeconds            float64 `json:"turn_seconds"`

	// A content-free census of everything else on the wire: top-level types, the
	// tool NAMES claude chose (a closed vendor set, no inputs), and how many tool
	// results came back as errors. This is the half that says what claude did
	// instead when the staging fails.
	LineTypeCensus   map[string]int `json:"line_type_census"`
	ToolCalls        []string       `json:"tool_calls"`
	ToolResultErrors int            `json:"tool_result_errors"`
	UndecodedLines   int            `json:"undecoded_lines"`

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

// fixtureWorthy answers whether this record may be promoted to tpcapFixturePath,
// and names the reason when it may not.
//
// Only a capture that satisfies AC3 in full becomes the fixture. Every rejection
// below is a case where the record is still valuable EVIDENCE — it is written to
// the artifact dir either way, and the AC3 fatals print its counts — but would be
// a lie as the committed proof: a vacuous capture makes every assertion built on
// it pass without reading a byte claude sent, and a capture holding an unmarked
// frame is the finding this ticket routes back rather than a fixture to ship.
//
// The version arm is the producing half of the pin the streamsup reader enforces
// (toolProgressCaptureVersion). `claude --version` prints "<version> (Claude
// Code)", so the comparison is on the leading token. Refusing to write under the
// wrong name turns a claude upgrade into an instruction to repin, instead of a
// file whose census silently describes a different release.
func (rec *tpcapRecord) fixtureWorthy() (string, bool) {
	if rec.Outcome != tpcapFired {
		return fmt.Sprintf("outcome=%s", rec.Outcome), false
	}
	if rec.FrameCount == 0 {
		return "zero tool_progress frames — a vacuous fixture proves nothing", false
	}
	if rec.UnmarkedFrames > 0 {
		return fmt.Sprintf("%d frame(s) carried none of the three markers — the marker set is "+
			"insufficient and that is a finding to route back", rec.UnmarkedFrames), false
	}
	if rec.FramesReaching > 0 {
		return fmt.Sprintf("%d frame(s) still reach the unrecognized lane", rec.FramesReaching), false
	}
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != tpcapFixtureVersion {
		return fmt.Sprintf("claude_version %q is not the %s pinned in the fixture name — repin "+
			"tpcapFixtureVersion and toolProgressCaptureVersion together, then re-run",
			got, tpcapFixtureVersion), false
	}
	// The consumer refuses any encoding but json-string, and dropcapMakeEntry emits
	// base64 with an EMPTY payload for a frame that is not valid UTF-8. Promoting
	// one would land a fixture that reddens `make check` for every unrelated
	// ticket. Vanishingly unlikely for a type whose fields are identifiers and
	// counters — which is why this is a refusal to promote rather than an AC3
	// fatal: the record still holds the frame as evidence that it happened at all.
	for i, f := range rec.Frames {
		if f.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("frame %d is encoded %q, and capturedToolProgressLines reads only %q "+
				"— a non-UTF-8 frame carries no readable payload, so a fixture holding one would "+
				"fail the assertion it exists to feed", i, f.PayloadEncoding, dropcapEncodingJSONString), false
		}
	}
	return "", true
}

// stagingVerdict names WHICH of the three ways a zero-frame capture can happen
// actually happened, and it is the field the 2026-09-06 live run wanted and did
// not have: that run reported "zero frames out of 13 captured lines" and advised
// raising the sleep, when the turn had ended in 9 s and the real question — did a
// foreground Bash call ever run at all — was unanswerable from the record.
//
// Only the third case is evidence about pyry's surface. The first two are the rig
// failing to stage the thing it meant to measure, and saying so plainly is what
// stops the next reader from touching the marker set to fix a staging bug.
func (rec *tpcapRecord) stagingVerdict() string {
	switch {
	case !rec.ForegroundCallObserved:
		return fmt.Sprintf("claude never opened the FIFO within %s, so no foreground Bash call ever "+
			"started. The staging failed BEFORE the surface was exercised — read tool_calls and "+
			"line_type_census to see what claude did instead", tpcapRendezvousWait)
	case rec.ForegroundHeldSeconds < tpcapHeartbeatIntervalSeconds:
		return fmt.Sprintf("a foreground call started but the turn ended %.1fs later, short of the "+
			"%ds heartbeat interval, so the first tick never came. claude backgrounded it or cut it "+
			"short: the staging failed, not the surface",
			rec.ForegroundHeldSeconds, tpcapHeartbeatIntervalSeconds)
	default:
		return fmt.Sprintf("a foreground Bash call was held open %.1fs, spanning %d heartbeat "+
			"interval(s) of %ds, and NO tool_progress line arrived. The staging WORKED, so this is a "+
			"finding about the surface — heartbeats do not reach --output-format stream-json stdout "+
			"here — and it is to be routed back, not fixed by loosening the marker set",
			rec.ForegroundHeldSeconds,
			int(rec.ForegroundHeldSeconds)/tpcapHeartbeatIntervalSeconds,
			tpcapHeartbeatIntervalSeconds)
	}
}

// tpcapCensus counts what else was on the wire, content-free.
//
// Top-level types (with system subtypes, which is where a refusal or an error
// announces itself), the tool NAMES claude chose, and how many tool results came
// back as errors. Names are a closed vendor set rather than claude's prose, and
// they still go through the redactor: an MCP tool's name embeds its server's.
// Tool INPUTS are deliberately not collected — the names alone answer "did it
// call Bash at all", which is the question, and the inputs would widen what a
// did-not-fire record carries for no diagnostic gain.
func tpcapCensus(lines []dropcapCaptured, red *dropcapRedactor) (
	types map[string]int, toolCalls []string, resultErrors, undecoded int,
) {
	types = map[string]int{}
	toolCalls = []string{}
	for _, c := range lines {
		if !c.Decoded {
			undecoded++
			continue
		}
		key := c.Type
		if c.Subtype != "" {
			key = c.Type + "/" + c.Subtype
		}
		types[key]++

		// content is an array on assistant/user messages and a plain string on
		// some user turns, so it is taken as RawMessage and array-decoded
		// separately: a string content is a skip, not an error worth recording.
		var msg struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(c.Raw, &msg); err != nil || len(msg.Message.Content) == 0 {
			continue
		}
		var blocks []struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			IsError bool   `json:"is_error"`
		}
		if err := json.Unmarshal(msg.Message.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				toolCalls = append(toolCalls, red.str(b.Name))
			case "tool_result":
				if b.IsError {
					resultErrors++
				}
			}
		}
	}
	return types, toolCalls, resultErrors, undecoded
}

// tpcapPrompt stages the one turn. The nonce is carried so dropcapRedactor's
// prompt_nonce class has something to substitute.
//
// The command is `cat <fifo>` and NOT a sleep, which is the 2026-09-06 revision:
// the first live run asked for `sleep 75`, got a turn that ended in 9 s, and the
// record could not say whether claude had shortened the duration, requested a
// short tool timeout, backgrounded the call or never run it at all. A sleep puts
// the duration in the MODEL's hands. A FIFO read puts it in the rig's — `cat`
// blocks until tpcapHoldFIFO releases the write end, and the rendezvous is
// positive evidence that a foreground call actually started.
//
// The verbatim-command wording is bgIdlePrompt's, which is the shape already
// proven in this package to make claude run exactly this command and block on it
// (#1260 reuses it). Two clauses are added: run it in the FOREGROUND, since
// #1260's own capture is heartbeat-free precisely because its call does not stay
// there; and do not request a timeout, because a model-requested timeout beats
// both BASH_DEFAULT_TIMEOUT_MS and claude's own 120 s default.
func tpcapPrompt(fifoPath string, nonce int64) string {
	return fmt.Sprintf("Use the Bash tool exactly once to run this command verbatim: cat %s. "+
		"Run it in the foreground and wait for it to finish. Do not background it, do not pass a "+
		"timeout, do not chain it with && or ;, do not add any flags or redirections, do not "+
		"comment on it, and do nothing else. run=%d", fifoPath, nonce)
}

// tpcapHoldFIFO is holdProbeFIFO with the release exposed instead of pinned to
// t.Cleanup, because this probe has to let go MID-TEST: the whole measurement is
// how long a foreground Bash call stays open, and the turn cannot end until `cat`
// gets its EOF.
//
// rendezvous closes when a reader opens the FIFO. That reader is claude's `cat`,
// so the channel is the one unambiguous signal that a foreground tool call
// started — the signal the sleep staging had no equivalent of.
//
// release is idempotent and also runs from t.Cleanup, so a fatal between the
// rendezvous and the hold expiring still lets `cat` exit and the child reap.
//
// RELEASE IS BOUNDED AND REPORTS THE OPEN ERROR, WHICH IS THE 2026-09-06 REVISION
// AND NOT POLISH. The first draft copied holdProbeFIFO without the bound its
// source puts on exactly this wait, and swallowed the goroutine's open error. The
// live gate then hung here: `internal/e2e/realclaude` failed at the 20-minute
// go-test timeout with 939 tests passed and none failed, which is the shape of a
// suite-level crash rather than of a capture that found nothing. Both halves are
// restored below.
func tpcapHoldFIFO(t *testing.T, path string) (rendezvous <-chan struct{}, release func()) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("#2089: mkfifo %s: %v", path, err)
	}

	var (
		arrived  = make(chan struct{})
		released = make(chan struct{})
		done     = make(chan struct{})
		openErr  error // read only after done closes, which orders it
		once     sync.Once
	)
	go func() {
		defer close(done)
		// Blocks until a reader opens the FIFO — this IS the rendezvous.
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			// KEPT, not swallowed, which is holdProbeFIFO's behaviour. An
			// instrument fault here is otherwise recorded as
			// foreground_call_observed:false and reads as "claude never opened the
			// FIFO" — the rig blaming the model for its own failure.
			openErr = err
			return
		}
		close(arrived)
		<-released
		_ = f.Close()
	}()

	release = func() {
		once.Do(func() {
			close(released)
			select {
			case <-arrived:
				// The write end is open; the goroutine closes it, and that EOF is
				// what finally lets `cat` exit.
			default:
				// No reader ever arrived, so the goroutine is parked in
				// open(O_WRONLY). Opening the read end unblocks it — and the fd is
				// HELD until the goroutine has returned rather than closed at once.
				// A transient open-then-close is not enough everywhere: Linux
				// latches the reader-open (r_counter) and wakes the blocked writer,
				// while the BSD/XNU shape re-tests readers==0 after the wakeup and
				// parks again. Holding it open satisfies both. Harmless once the
				// rendezvous has fired: only the LAST writer closing sends EOF.
				if rf, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
					defer rf.Close()
				}
			}
			// Bounded, as holdProbeFIFO bounds the same wait. Blocking forever here
			// hangs `make e2e-realclaude` to the go-test timeout instead of
			// reporting that claude never ran the command, and a suite that dies
			// mid-run reports nothing about any test after it.
			select {
			case <-done:
				if openErr != nil {
					t.Errorf("#2089: the FIFO write end never opened: %v. This is an INSTRUMENT "+
						"fault, not evidence about claude: read it before the record's "+
						"foreground_call_observed, which reads false for this and for a turn that "+
						"never ran the command", openErr)
				}
			case <-time.After(probeFIFOReleaseDeadline):
				t.Errorf("#2089: the FIFO hold goroutine did not exit within %s of the release; the "+
					"write end may still be open and claude's `cat` may never see EOF",
					probeFIFOReleaseDeadline)
			}
		})
	}
	t.Cleanup(release)
	return arrived, release
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
	// THE GATE IS THE FIXTURE'S ABSENCE, AND THAT IS A DELIBERATE BREAK FROM THE
	// SEVEN SIBLING PROBES IN THIS PACKAGE.
	//
	// Each of those is env-gated unconditionally and writes to a tempdir for a
	// human to carry into the repo by hand. That shape is right for a one-off
	// instrument and wrong for this one. #2089's fixture is an ACCEPTANCE
	// CRITERION — three of the ticket's four ACs turn on it — and the ticket
	// carries needs-real-claude precisely so the live gate produces it. Under the
	// sibling shape `make e2e-realclaude` never sets the variable, so the probe
	// skips on the ENV check before it ever reaches the credential check, the live
	// gate passes vacuously, and the fixture never lands. That is CLAUDE.md
	// § Testing's #1763 failure exactly: a green gate and a spent budget look
	// identical whether the bytes landed or not.
	//
	// So the probe ARMS ITSELF while the fixture is absent and DISARMS once it
	// exists. On an authenticated machine the first `make e2e-realclaude` produces
	// it and every run after costs nothing; on this machine it still skips, but at
	// the CREDENTIAL check, which is the honest reason. The env var survives as a
	// FORCE, for re-capturing at a new claude version.
	force := os.Getenv(tpcapEnableEnv) == "1"
	if _, err := os.Stat(tpcapFixturePath); err == nil && !force {
		t.Skipf("#2089 tool_progress capture: the fixture %s already exists, so there is "+
			"nothing to capture and this costs no claude turn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 15m -v \\\n"+
			"    -run '^TestRealClaude_ToolProgressCapture$' ./internal/e2e/realclaude/",
			tpcapFixturePath, tpcapEnableEnv)
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
	fifoPath := filepath.Join(workdir, tpcapFIFOName)

	red := newDropcapRedactor(home, artifactDir, workdir, fifoPath, tpcapSessionID, nonce)
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

	// Before the runner, so the FIFO exists by the time claude reads the prompt,
	// and so its release cleanup is registered BEFORE the runner's cancel and
	// therefore runs AFTER it — #1260's ordering.
	rendezvous, releaseFIFO := tpcapHoldFIFO(t, fifoPath)

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

	prompt := tpcapPrompt(fifoPath, nonce)
	rec.Prompt = red.str(prompt)
	turnStart := time.Now()
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.set(tpcapInstrumentBroken, "writing the turn envelope failed, so no turn was ever "+
			"driven: %v", red.str(err.Error()))
		return
	}

	// Phase 1 — wait for claude's `cat` to open the FIFO. Nothing else in this rig
	// opens it, so that open is unambiguous evidence that a foreground Bash call
	// started. The turn ending first means the command never ran, and the line
	// census below is what says what claude did instead.
	select {
	case <-rendezvous:
		rec.ForegroundCallObserved = true
	case <-recorder.resultSeen:
	case <-time.After(tpcapRendezvousWait):
	}

	// Phase 2 — hold it open across at least two heartbeat ticks, then release so
	// `cat` gets EOF and the turn can finish. A turn that ends DURING the hold was
	// backgrounded or cut short; held_seconds is how far it got, and comparing it
	// against one tick is what separates a staging failure from a finding about
	// the surface.
	if rec.ForegroundCallObserved {
		heldFrom := time.Now()
		select {
		case <-time.After(tpcapHold):
		case <-recorder.resultSeen:
		}
		rec.ForegroundHeldSeconds = time.Since(heldFrom).Seconds()
	}
	releaseFIFO()

	select {
	case <-recorder.resultSeen:
		rec.TerminatedOn = tpcapTerminatedResult
	case <-time.After(tpcapTurnBudget):
		rec.TerminatedOn = tpcapTerminatedBudget
	}
	rec.TurnSeconds = time.Since(turnStart).Seconds()

	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	rec.LineTypeCensus, rec.ToolCalls, rec.ToolResultErrors, rec.UndecodedLines = tpcapCensus(lines, red)
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
		rec.set(tpcapDidNotFire, "the turn produced no tool_progress line at all; %s", rec.stagingVerdict())
	} else {
		rec.set(tpcapFired, "%d tool_progress frame(s), census %v", rec.FrameCount, rec.MarkerCensus)
	}

	// --- AC3 ------------------------------------------------------------------
	// Every message below reports counts and indices only. The frames themselves
	// are in the record the cleanup has already written; putting claude's bytes in
	// CI output is precisely the exposure the deny-scan exists to prevent.
	if rec.FrameCount == 0 {
		t.Fatalf("#2089: the turn recorded ZERO tool_progress frames out of %d captured lines "+
			"(terminated_on=%s, turn=%.1fs). A capture recording none is vacuous — every assertion "+
			"built on it would pass without reading a byte claude sent.\n"+
			"  staging: %s\n"+
			"  line types: %v; tools called: %v; tool_result errors: %d; undecoded: %d\n"+
			"Read the staging line FIRST: only the last case is a finding about pyry's surface, and "+
			"the other two are the rig failing to hold a foreground call open",
			rec.LinesCaptured, rec.TerminatedOn, rec.TurnSeconds, rec.stagingVerdict(),
			rec.LineTypeCensus, rec.ToolCalls, rec.ToolResultErrors, rec.UndecodedLines)
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
	t.Logf("#2089 outcome=%s terminated_on=%s turn=%.1fs fg_call=%v held=%.1fs captured=%d "+
		"frames=%d census=%v unmarked=%d reaching_lane=%d line_types=%v tools=%v tool_errors=%d "+
		"scan_not_applied=%v\n  record: %s\n  %s",
		rec.Outcome, rec.TerminatedOn, rec.TurnSeconds, rec.ForegroundCallObserved,
		rec.ForegroundHeldSeconds, rec.LinesCaptured, rec.FrameCount, rec.MarkerCensus,
		rec.UnmarkedFrames, rec.FramesReaching, rec.LineTypeCensus, rec.ToolCalls,
		rec.ToolResultErrors, notApplied, red.str(path), red.str(rec.OutcomeDetail))

	// The same deny-scanned bytes, promoted in-repo so the run that produced them
	// is the run that lands them. Writing the fixture here rather than leaving it
	// in the tempdir is the point of this probe's self-arming gate: a capture that
	// still needs a human to copy a file is a capture #1763 says will not land.
	if reason, ok := rec.fixtureWorthy(); !ok {
		t.Logf("#2089: NOT promoted to %s — %s. The record above is the evidence; read it, "+
			"then re-run or route the finding back", tpcapFixturePath, reason)
		return
	}
	if err := os.WriteFile(tpcapFixturePath, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2089: write fixture %s: %v", tpcapFixturePath, red.str(err.Error()))
		return
	}
	t.Logf("#2089: FIXTURE WRITTEN to %s (%d frames, census %v).\n"+
		"  Commit it — `git add %s` — and update consumeToolProgress's dated census to the "+
		"marker counts above; a capture that lands without its census leaves the docblock "+
		"describing a different measurement. An uncommitted capture is a capture that did not "+
		"happen (#1763).\n"+
		"  A run reaching this line at all means the fixture was absent or %s=1 forced a "+
		"re-capture, so this is a NEW claude release or a suspected shape change: re-read the "+
		"census before trusting the old one", tpcapFixturePath, rec.FrameCount, rec.MarkerCensus,
		tpcapFixturePath, tpcapEnableEnv)
}

// TestTpcapFixtureWorthyRefusesEveryBadCapture runs offline. fixtureWorthy is the
// only thing standing between a live run and a committed fixture, and each arm
// below is a capture that would look green from outside — the record is written,
// the deny-scan passed, the log is cheerful — while proving nothing or proving
// something about the wrong claude.
func TestTpcapFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	good := func() *tpcapRecord {
		frames := make([]tpcapFrame, 3)
		for i := range frames {
			frames[i] = tpcapFrame{Index: i, Type: "tool_progress", PayloadEncoding: dropcapEncodingJSONString}
		}
		return &tpcapRecord{
			Outcome:       tpcapFired,
			ClaudeVersion: tpcapFixtureVersion + " (Claude Code)",
			FrameCount:    len(frames),
			Frames:        frames,
		}
	}
	tests := []struct {
		name   string
		mutate func(*tpcapRecord)
		want   bool
	}{
		{"a good capture is promoted", func(*tpcapRecord) {}, true},
		{"bare version string, no suffix", func(r *tpcapRecord) { r.ClaudeVersion = tpcapFixtureVersion }, true},
		{"never fired", func(r *tpcapRecord) { r.Outcome = tpcapDidNotFire }, false},
		{"instrument broken", func(r *tpcapRecord) { r.Outcome = tpcapInstrumentBroken }, false},
		{"vacuous: zero frames", func(r *tpcapRecord) { r.FrameCount = 0 }, false},
		{"an unmarked frame is a finding, not a fixture", func(r *tpcapRecord) { r.UnmarkedFrames = 1 }, false},
		{"a frame still reaching the lane", func(r *tpcapRecord) { r.FramesReaching = 1 }, false},
		{"a different claude release", func(r *tpcapRecord) { r.ClaudeVersion = "2.1.260 (Claude Code)" }, false},
		{"version unreadable", func(r *tpcapRecord) { r.ClaudeVersion = "<unavailable: exec failed>" }, false},
		{"version absent", func(r *tpcapRecord) { r.ClaudeVersion = "" }, false},
		{
			// The one shape this side would otherwise promote and the reading side
			// refuses: a frame that was not valid UTF-8 is recorded base64 with an
			// empty payload, and capturedToolProgressLines fatals on the encoding.
			"a base64 frame the consumer cannot read",
			func(r *tpcapRecord) { r.Frames[1].PayloadEncoding = dropcapEncodingBase64 },
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := good()
			tc.mutate(rec)
			reason, ok := rec.fixtureWorthy()
			if ok != tc.want {
				t.Errorf("fixtureWorthy() ok = %v, want %v (reason %q)", ok, tc.want, reason)
			}
			if !ok && reason == "" {
				t.Error("fixtureWorthy() refused without naming a reason; the log line would say nothing")
			}
			if ok && reason != "" {
				t.Errorf("fixtureWorthy() promoted but named reason %q", reason)
			}
		})
	}
}

// TestTpcapHoldOutlastsTheHeartbeatInterval runs offline. Every constant it
// checks is one whose mis-setting produces a capture that looks like "claude does
// not emit these" while actually measuring the rig — which is how #1260's capture
// came back with no frames of this type, and how #2089's own first live run
// (2026-09-06) came back with none.
func TestTpcapHoldOutlastsTheHeartbeatInterval(t *testing.T) {
	t.Parallel()

	// Two ticks, not one: a hold of exactly one interval races the tick it is
	// waiting for, and a capture that sometimes records nothing is worse than one
	// that never does.
	if tpcapHoldSeconds < 2*tpcapHeartbeatIntervalSeconds {
		t.Errorf("tpcapHoldSeconds = %d but the heartbeat interval is %d s: the rig would release "+
			"the FIFO before two ticks could land, so a zero-frame capture would say nothing about "+
			"whether claude emits them", tpcapHoldSeconds, tpcapHeartbeatIntervalSeconds)
	}

	// The tool call must not be timed out from under the hold. This only covers
	// the DEFAULT; a model-requested timeout still wins, which is why tpcapPrompt
	// forbids requesting one and why stagingVerdict reports the held duration
	// rather than assuming it.
	timeoutMS, err := strconv.Atoi(tpcapBashTimeoutMS)
	if err != nil {
		t.Fatalf("tpcapBashTimeoutMS = %q is not an integer: %v", tpcapBashTimeoutMS, err)
	}
	if timeoutMS <= tpcapHoldSeconds*1000 {
		t.Errorf("tpcapBashTimeoutMS = %d ms but the rig holds the call open for %d s (%d ms): the "+
			"call would be cut before the hold ends and the capture would measure the timeout, "+
			"not claude", timeoutMS, tpcapHoldSeconds, tpcapHoldSeconds*1000)
	}

	// The rendezvous wait bounds how long claude has to START the call. Shorter
	// than the hold and a slow-but-correct turn would be recorded as "never ran".
	if tpcapRendezvousWait < tpcapHold {
		t.Errorf("tpcapRendezvousWait = %s is shorter than the hold %s: a turn that merely started "+
			"slowly would be recorded as one that never ran the command", tpcapRendezvousWait, tpcapHold)
	}
}

// TestTpcapHoldFIFO runs offline and exercises the mechanism the whole capture
// now rests on, with a plain os.File standing in for claude's `cat`.
//
// The second case is the one worth having: when NO reader ever arrives the write
// goroutine is parked in open(O_WRONLY), and a release that simply waited for it
// would block forever. In the live gate that is not a failed capture, it is a
// hung `make e2e-realclaude` — strictly worse than the zero-frame run this
// revision is fixing.
func TestTpcapHoldFIFO(t *testing.T) {
	t.Parallel()

	t.Run("a reader trips the rendezvous and EOF waits for the release", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), tpcapFIFOName)
		rendezvous, release := tpcapHoldFIFO(t, path)

		readDone := make(chan error, 1)
		go func() {
			f, err := os.Open(path) // the stand-in for claude's `cat`
			if err != nil {
				readDone <- err
				return
			}
			defer f.Close()
			_, err = io.ReadAll(f) // blocks until the write end closes
			readDone <- err
		}()

		select {
		case <-rendezvous:
		case <-time.After(10 * time.Second):
			t.Fatal("rendezvous never fired though a reader opened the FIFO; the live probe would " +
				"record every good capture as one where the foreground call never started")
		}

		// The read must still be blocked: that block IS the held-open tool call.
		select {
		case err := <-readDone:
			t.Fatalf("the reader finished before the release (err=%v); nothing would hold claude's "+
				"Bash call open across a heartbeat tick", err)
		case <-time.After(200 * time.Millisecond):
		}

		release()
		select {
		case err := <-readDone:
			if err != nil {
				t.Errorf("reader: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the reader never saw EOF after the release; the turn could never end")
		}

		release() // idempotent: t.Cleanup calls it again
	})

	t.Run("release returns when no reader ever arrives", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), tpcapFIFOName)
		_, release := tpcapHoldFIFO(t, path)

		done := make(chan struct{})
		go func() {
			defer close(done)
			release()
		}()
		// Deliberately WELL UNDER probeFIFOReleaseDeadline. release now bounds its
		// own wait, so a broken wakeup would eventually return anyway — with a
		// t.Errorf, after a 10 s stall the live gate pays on every capture. Waiting
		// less than the deadline is what keeps this a test of the wakeup working
		// rather than of the backstop firing.
		select {
		case <-done:
		case <-time.After(tpcapNoReaderReleaseBudget):
			t.Fatalf("release did not return within %s with no reader present. The write goroutine "+
				"is parked in open(O_WRONLY) and the wakeup did not take: a transient read-open is "+
				"not enough on the BSD/XNU shape, which re-tests readers==0 after the wakeup, so the "+
				"read fd must stay open until the goroutine exits. Unfixed, the live gate hangs to "+
				"the go-test timeout instead of reporting that claude never ran the command — which "+
				"is how #2089's 2026-09-06 gate run died", tpcapNoReaderReleaseBudget)
		}
	})
}

// TestTpcapStagingVerdictSeparatesRigFailureFromFinding runs offline. The verdict
// string is what a reader of a failed live gate acts on, and the one case that
// must never be confused with the others is the third: a call held open across
// the interval with no frame is evidence about claude's surface, while the first
// two are the rig failing to stage anything at all.
func TestTpcapStagingVerdictSeparatesRigFailureFromFinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rec         tpcapRecord
		wantFinding bool
	}{
		{
			name: "the command never ran",
			rec:  tpcapRecord{ForegroundCallObserved: false},
		},
		{
			name: "started but cut short before the first tick",
			rec:  tpcapRecord{ForegroundCallObserved: true, ForegroundHeldSeconds: 9},
		},
		{
			name: "held just under one interval",
			rec: tpcapRecord{
				ForegroundCallObserved: true,
				ForegroundHeldSeconds:  tpcapHeartbeatIntervalSeconds - 0.1,
			},
		},
		{
			name: "held past the interval and still nothing came",
			rec: tpcapRecord{
				ForegroundCallObserved: true,
				ForegroundHeldSeconds:  tpcapHoldSeconds,
			},
			wantFinding: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.rec.stagingVerdict()
			if got == "" {
				t.Fatal("stagingVerdict() is empty; the zero-frame fatal would name no cause")
			}
			isFinding := strings.Contains(got, "finding about the surface")
			if isFinding != tc.wantFinding {
				t.Errorf("stagingVerdict() reads as a surface finding = %v, want %v\n  got: %s",
					isFinding, tc.wantFinding, got)
			}
			if !tc.wantFinding && !strings.Contains(got, "staging") {
				t.Errorf("a rig failure must say so; got: %s", got)
			}
		})
	}
}
