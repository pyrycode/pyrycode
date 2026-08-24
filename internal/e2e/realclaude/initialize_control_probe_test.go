//go:build e2e_realclaude

package realclaude

// #1688 — what does a real claude answer when the daemon asks it to initialize?
//
// # Why a measurement and not a reading
//
// The daemon needs to publish claude's model list — concrete identifiers,
// display names, and the reasoning-effort levels each model supports — to
// connected clients, so a client can offer the models that actually exist
// instead of a hardcoded menu that goes stale. The list needs no credential and
// no HTTP endpoint: the child the daemon already supervises hands it over when
// asked, through a control_request with subtype "initialize" written on the
// held-open stdin.
//
// That was measured BY HAND, outside this repo, against claude 2.1.220 on
// 2026-08-21. Nothing in the tree records the request line claude accepts or the
// response shape, and three slices downstream (#1689's trigger, #1690's decoder,
// #1692's fake) decode against it. This file is the live run that fills #1701's
// record and commits the bytes.
//
// # One arrangement, and it is not a guess
//
// The request is written AFTER a completed turn. runSetModeChild writes its
// control request at exactly that point and got a control_response back on both
// of its measurement arms at 2.1.220. Whether the request is ALSO answered
// before the first user turn, and whether the round trip perturbs the live
// session, need a three-arm rig with a no-request control and are #1694's.
//
// # What passes
//
// A response carrying subtype:"error" is a refusal, and a recorded refusal is a
// PASSING outcome — the error text is the input to #1689. The run fails only
// where there is no artifact to commit: a spawn failure, zero stdout lines, or
// no control_response at all. Send the minimal request shape and DO NOT ITERATE
// against a live child; the wall-clock and token risk here is the run, not the
// typing.
//
// # What this file reuses, and the one thing it must not
//
// The driver is runSetModeChild minus the arm table: setModeRecorder,
// setModeWaitFor, setModeTurnLine, setModeResponseIDMatches and setModeScanMax
// are reused verbatim. #1595's verdict machinery — probeOutcome,
// setModeTurnWindows, setModeFieldMatches, setModeDirections — is deliberately
// NOT ported: it classifies measurement arms against control arms, and this
// ticket has one arm and no controls. inbandTapRecorder cannot serve here
// either; it retains no payloads and does not classify control_response at all.
//
// This file EXECS, so it correctly takes no finOfflineExecBans entry — that
// registry is keyed by filename and consumed by `for f := range
// finOfflineExecBans`, so the absence costs nothing. initControlScrubbed is what
// carries the credential guard here instead.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -v \
//	  -run TestRealClaude_InitializeControl ./internal/e2e/realclaude/
//
// Read the count of tests that executed, never the exit code: this package is
// behind the e2e_realclaude tag, `make check` never compiles it, and the suite
// exits 0 both on a build failure and on a full credentials skip.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	// A fresh EMPTY directory under the test's pinned $HOME, deliberately not a
	// git repo: less project context for claude to load, so the probe turn is
	// cheaper.
	initControlWorkdirName = "initialize-control-work"

	// The one probe turn. It exists only to reach the after-a-completed-turn
	// arrangement, so it is deliberately TOOL-FREE — unlike runSetModeChild's
	// Bash probe. A tool-free turn cannot stall on a permission prompt it can
	// never receive, which is why this run needs no
	// --dangerously-skip-permissions and cannot hit the `default`-posture hang
	// #1595 budgets two minutes for. Do not "make the probe use Bash like
	// #1595": that reintroduces the flag and hands the child unsandboxed tool
	// access for no measurement gain.
	initControlPrompt = "Reply with the single word: ready. Do not use any tools."

	// Cost. The model list claude reports is a property of the BINARY, not of
	// the model answering the probe turn.
	initControlModel = "claude-haiku-4-5"

	// Cost guard with headroom over the single assistant turn the probe needs. A
	// result line carrying subtype:"error_max_turns" lands in the fixture
	// plainly — raise and rerun.
	initControlMaxTurns = "4"

	// A correlation token, not a security token — the same reason
	// (*Runner).Interrupt mints its id from a monotonic counter rather than a
	// random source. A fixed literal keeps the committed fixture diffable.
	initControlRequestID = "initialize-control-1"
)

const (
	// Hard kill for the one child, and the outer bound: the per-step waits below
	// can sum past it on a fully stalling run, in which case the deadline trips
	// and context_deadline_tripped records that rather than the run hanging.
	initControlChildBudget = 3 * time.Minute

	// The one probe turn. A tool-free turn lands in seconds.
	initControlTurnBudget = 90 * time.Second

	// Long enough that an absent control_response means absence, not impatience.
	// Same value and same reason as setModeControlBudget.
	initControlControlBudget = 45 * time.Second
)

// --- the request line ---------------------------------------------------------

// initControlRequest and initControlRequestInner are the wire shape of the one
// control line this probe writes on the child's held-open stdin.
//
// The inner struct carries ONLY `subtype`, and it is deliberately not
// setModeControlRequestInner: that type carries a `mode` field with no
// omitempty, so reusing it would emit "mode":"" and the sent line would stop
// being the minimal shape this ticket mandates. Adding omitempty to it instead
// would change what control_request_sent reads in #1595's four committed
// fixtures. A fresh two-field struct is the correct price; neither alternative
// is.
type initControlRequest struct {
	Type      string                  `json:"type"`       // "control_request"
	RequestID string                  `json:"request_id"` // locally-minted correlation id
	Request   initControlRequestInner `json:"request"`
}

type initControlRequestInner struct {
	Subtype string `json:"subtype"` // "initialize"
}

// initControlLine returns the single newline-terminated control line:
//
//	{"type":"control_request","request_id":"<id>","request":{"subtype":"initialize"}}
//
// Marshalled structured, never string-concatenated — the same one-physical-line
// invariant setModeControlLine holds, so the appended '\n' is the only raw
// newline in the envelope.
func initControlLine(requestID string) ([]byte, error) {
	b, err := json.Marshal(initControlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   initControlRequestInner{Subtype: "initialize"},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal initialize control request: %w", err)
	}
	return append(b, '\n'), nil
}

// --- the response read --------------------------------------------------------

// initControlSummary is the read of the recorded responses that fills
// initControlFixtureRecord's four response-shape fields.
type initControlSummary struct {
	subtype           string
	modelsPresent     bool
	modelsCount       int
	modelsEntryFields []string
}

// initControlSummarize returns the first non-empty `subtype` and the first
// non-null `models` array found across responses, with that array's entry count
// and the sorted union of the field names its entries carry.
//
// THREE PLACEMENTS ARE READ — top level, under `response`, and under
// `response.response` — and the third one is where a real reply actually put it.
// internal/streamsup/parser.go's control_response arm records, as measured
// shape, that subtype and request_id arrive nested under `response` rather than
// carried at top level, and setModeResponseIDMatches already applies that
// discipline to request_id. Measured here against claude 2.1.239 on 2026-08-22,
// the initialize reply nests ONE LEVEL DEEPER than that: `subtype` and
// `request_id` sit under `response` as the parser records, but the payload —
// `models`, `commands`, `agents`, `account`, `pid` and the rest — sits under
// `response.response`. A reader that stops at either of the first two levels
// reports a false absence, which is exactly what the first run of this file did
// before the third placement was added.
//
// PRESENCE IS DECIDED ON THE RAW BYTES, not on a decoded slice. Unmarshalling
// straight into a slice makes `"models":[]` and an absent `models` both arrive
// as nil, and the difference between "claude has no models to report" and
// "claude reported no models array" is exactly what #1690 needs. Where the
// second unmarshal fails, modelsPresent stays true with a zero count — the raw
// bytes are in control_responses verbatim either way.
//
// modelsEntryFields IS A UNION ACROSS ENTRIES, AND A UNION OVER-REPORTS: it
// names every field SOME entry carries, not every field EVERY entry carries. The
// by-hand 2026-08-21 table recorded a `Haiku` entry carrying neither
// supportsAutoMode nor supportedEffortLevels while the other four carried both.
// initControlFixtureRecord.ModelsEntryFields is a flat []string, so a union is
// the only shape that fits it — and #1690's decoder is designed against this
// field, so one that reads it as a per-entry guarantee nil-derefs on `Haiku`.
// The per-entry truth is preserved verbatim in control_responses; that is the
// ground truth and this summary is a convenience over it.
//
// The union is SORTED because Go's map iteration is randomised, and an unsorted
// union writes a different byte sequence into the committed fixture on every
// run.
func initControlSummarize(responses []json.RawMessage) initControlSummary {
	var out initControlSummary
	fields := make(map[string]bool)
	for _, raw := range responses {
		var env struct {
			Subtype  string          `json:"subtype"`
			Models   json.RawMessage `json:"models"`
			Response struct {
				Subtype  string          `json:"subtype"`
				Models   json.RawMessage `json:"models"`
				Response struct {
					Subtype string          `json:"subtype"`
					Models  json.RawMessage `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		for _, sub := range []string{env.Subtype, env.Response.Subtype, env.Response.Response.Subtype} {
			if out.subtype == "" {
				out.subtype = sub
			}
		}
		if out.modelsPresent {
			continue
		}
		var models json.RawMessage
		for _, cand := range []json.RawMessage{env.Models, env.Response.Models, env.Response.Response.Models} {
			if len(cand) > 0 && string(cand) != "null" {
				models = cand
				break
			}
		}
		if len(models) == 0 {
			continue
		}
		out.modelsPresent = true
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(models, &entries); err != nil {
			continue
		}
		out.modelsCount = len(entries)
		for _, entry := range entries {
			for name := range entry {
				fields[name] = true
			}
		}
	}
	for name := range fields {
		out.modelsEntryFields = append(out.modelsEntryFields, name)
	}
	slices.Sort(out.modelsEntryFields)
	return out
}

// --- the credential guard -----------------------------------------------------

// initControlScrubbed t.Fatalf's when stderr contains the non-empty value of
// either credential variable WithWorktreeAuthenticated re-pins into the child's
// environment, and returns silently otherwise.
//
// This is the FIRST fixture in the initialize_control_* family to carry real
// claude stderr, and the file it writes is committed to a public repo. An auth
// failure is exactly the condition that makes claude print a long message to
// stderr. stderrFixtureCap bounds how much of it lands in the file and #1700
// proves that bound — but A CAP IS NOT A REDACTION: 8 KiB of a
// credential-bearing message still commits the credential.
//
// Three details, each of which is the difference between a guard and a
// decoration:
//
//   - An UNSET variable is skipped, never compared against "": every string
//     contains the empty string, so comparing it would fail every run.
//   - It checks the RAW stderr rather than the capped copy. The raw is a
//     superset, so a token past the cap still fails the run — stricter and
//     simpler than reasoning about where the cut lands.
//   - Its message names the variable and prints NOTHING ELSE. An error message
//     that helpfully quotes the leak is the own-goal this guard exists to
//     prevent.
//
// Plain strings.Contains, not crypto/subtle. Constant-time comparison defends a
// secret against a party that does not know it; the only party on the other side
// here is the claude binary, which was handed the token.
func initControlScrubbed(t *testing.T, stderr string) {
	t.Helper()
	for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if v := os.Getenv(name); v != "" && strings.Contains(stderr, v) {
			t.Fatalf("#1688: the child's stderr carries the value of %s; refusing to record it "+
				"or to print an excerpt of it. Nothing was written to testdata/", name)
		}
	}
}

// --- the driver ---------------------------------------------------------------

// runInitControlChild spawns one child under pyry's stream-json-in/stream-json-out
// argv, drives one probe turn to completion, writes the initialize control
// request on the held-open stdin, reads the reply, writes the fixture and returns
// the completed record.
//
// It t.Fatalf's ONLY for a broken instrument — a pipe or spawn failure, a marshal
// failure, a credential in the child's stderr, or zero stdout lines captured.
// Every other outcome is information and lands in a fixture field: a probe turn
// that never closed, a control_response that never came, one that came with
// subtype "error", a mismatched request_id, a stdin write error, an over-long
// line, a non-zero exit, a tripped deadline.
func runInitControlChild(t *testing.T, claudeBin, workdir, versionRaw, versionToken string) *initControlFixtureRecord {
	t.Helper()

	// The fixed stream-json prefix is streamsup's buildArgs'; the two cost flags
	// occupy the `base` slot that function appends after it.
	argv := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", initControlModel,
		"--max-turns", initControlMaxTurns,
	}

	ctx, cancel := context.WithTimeout(context.Background(), initControlChildBudget)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin, argv...)
	cmd.Dir = workdir

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("#1688: stdin pipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("#1688: stdout pipe: %v", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("#1688: start claude: %v", err)
	}

	// Single reader goroutine over the child's stdout. It exits on EOF — which
	// follows either the stdin close below or the context kill — and closes
	// readerDone, so no goroutine outlives its child. scannerErr is written only
	// before that close and read only after it; that ordering is the whole
	// synchronisation for it.
	rec := &setModeRecorder{}
	var scannerErr string
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), setModeScanMax)
		for scanner.Scan() {
			rec.add(scanner.Bytes())
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			scannerErr = err.Error()
		}
	}()

	var writeErrs []string
	writeLine := func(what string, line []byte) {
		if _, err := stdinPipe.Write(line); err != nil {
			writeErrs = append(writeErrs, fmt.Sprintf("%s: %v", what, err))
		}
	}

	turn, err := setModeTurnLine(initControlPrompt)
	if err != nil {
		t.Fatalf("#1688: %v", err)
	}
	writeLine("probe turn", turn)
	if !setModeWaitFor(rec.resultCount, 1, initControlTurnBudget) {
		t.Logf("#1688: the probe turn produced no result line within %s, so the control request "+
			"below is NOT written after a completed turn and this capture is OFF the one "+
			"arrangement the ticket pins. Recorded rather than fatal — turn_boundaries stays "+
			"empty in the fixture, which is what tells #1694 so. Continuing", initControlTurnBudget)
	}

	controlLine, err := initControlLine(initControlRequestID)
	if err != nil {
		t.Fatalf("#1688: %v", err)
	}
	controlSent := json.RawMessage(bytes.TrimRight(controlLine, "\n"))
	t.Logf("#1688: writing control request: %s", controlSent)
	writeLine("control request", controlLine)
	if !setModeWaitFor(rec.controlResponseCount, 1, initControlControlBudget) {
		t.Logf("#1688: no control_response within %s (recorded as absence, continuing)",
			initControlControlBudget)
	}

	if err := stdinPipe.Close(); err != nil {
		writeErrs = append(writeErrs, fmt.Sprintf("stdin close: %v", err))
	}
	waitErr := cmd.Wait()
	<-readerDone
	duration := time.Since(start)

	// AFTER the join, so a t.Fatalf here cannot strand the reader goroutine —
	// and before every consumer of stderr: before the zero-lines fatal, which
	// prints up to stderrFixtureCap bytes into a run log this pipeline salvages,
	// and before the write, because a poisoned fixture must never reach disk at
	// all, not even to be deleted afterwards.
	initControlScrubbed(t, stderrBuf.String())

	lines := rec.snapshotLines()
	if len(lines) == 0 {
		t.Fatalf("#1688: claude produced no stdout; there is nothing to capture\nstderr:\n%s\nwaitErr: %v",
			truncateString(stderrBuf.String(), stderrFixtureCap), waitErr)
	}

	responses := rec.snapshotControlResponses()
	summary := initControlSummarize(responses)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	waitErrStr := ""
	if waitErr != nil {
		waitErrStr = waitErr.Error()
	}

	record := &initControlFixtureRecord{
		ClaudeVersionRaw: versionRaw,
		ClaudeVersion:    versionToken,

		Argv:    append([]string{claudeBin}, argv...),
		Prompts: []string{initControlPrompt},

		ControlRequestID:                initControlRequestID,
		ControlRequestSent:              controlSent,
		ControlResponses:                responses,
		ControlResponseSubtype:          summary.subtype,
		ControlResponseRequestIDMatched: setModeResponseIDMatches(responses, initControlRequestID),

		ModelsPresent:     summary.modelsPresent,
		ModelsCount:       summary.modelsCount,
		ModelsEntryFields: summary.modelsEntryFields,

		StdoutEvents:     lines,
		NonJSONLineCount: rec.nonJSONCount(),
		TurnBoundaries:   rec.snapshotBoundaries(),

		StdinWriteErrors: writeErrs,
		// RAW, deliberately not pre-truncated. writeInitControlFixture applies
		// capFixtureCapture to its own local copy — #1702's design, #1700's
		// proof — so the record in memory holds the raw string and the file on
		// disk holds the capped one. runSetModeChild truncates at its call site
		// because ITS writer has no cap; copying that here would duplicate the
		// bound in two places for no gain.
		StderrCapture:          stderrBuf.String(),
		ExitCode:               exitCode,
		WaitError:              waitErrStr,
		ContextDeadlineTripped: errors.Is(ctx.Err(), context.DeadlineExceeded),
		DurationMs:             duration.Milliseconds(),
		ScannerError:           scannerErr,
	}

	// packageDir is os.Getwd(), which under `go test` is this package's own
	// source directory — no untrusted component anywhere in it. Choosing dir is
	// the caller's job, which initControlFixtureName's doc hands over explicitly;
	// this is the call site that discharges it.
	path := writeInitControlFixture(t, filepath.Join(packageDir(t), "testdata"), record)

	// Never %+v the record into a log or a fatal message: that moves up to
	// stderrFixtureCap bytes of child output out of the bounded file and into an
	// unbounded run log, the exact thing the cap exists to prevent.
	t.Logf("#1688: %d line(s), %d non-JSON, %d control_response(s), subtype=%q, "+
		"models_present=%v models_count=%d models_entry_fields=%v, request_id matched=%v, "+
		"exit=%d, deadline_tripped=%v, scanner_error=%q, %s",
		len(lines), record.NonJSONLineCount, len(responses), record.ControlResponseSubtype,
		record.ModelsPresent, record.ModelsCount, record.ModelsEntryFields,
		record.ControlResponseRequestIDMatched, exitCode, record.ContextDeadlineTripped,
		record.ScannerError, duration.Round(time.Millisecond))
	for i, resp := range responses {
		t.Logf("#1688: control_response[%d] verbatim: %s", i, resp)
	}
	t.Logf("#1688: fixture written: %s", path)

	return record
}

// --- the tests ----------------------------------------------------------------

// TestRealClaude_InitializeControl_Capture drives one live child through the
// arrangement #1595 measured — spawn, one probe turn to completion, one
// control_request with subtype "initialize" on the held-open stdin — and commits
// what comes back.
//
// A response carrying subtype:"error" is a REFUSAL AND A PASSING OUTCOME: its
// error text is what #1689 designs the accepted shape against. A reviewer
// reading control_response_subtype == "error" in a green run is reading the
// measurement, not a bug. The run fails only where there is no artifact to
// commit, which is the single assertion below.
func TestRealClaude_InitializeControl_Capture(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials

	workdir := filepath.Join(home, initControlWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#1688: create workdir: %v", err)
	}

	versionRaw, versionToken := captureClaudeVersion(t)
	t.Logf("#1688: claude version %q (token %q)", versionRaw, versionToken)

	// No t.Parallel and no subtests: one child, one reader goroutine, one pinned
	// $HOME. The fixture is on disk before this returns, so the assertion below
	// runs against an artifact a human can already read.
	rec := runInitControlChild(t, claudeBin, workdir, versionRaw, versionToken)

	if len(rec.ControlResponses) == 0 {
		t.Fatalf("#1688: no control_response arrived within %s, so the round trip this ticket "+
			"exists to record did not happen and there is no shape for #1689/#1690/#1692 to "+
			"decode against. The written fixture holds %d stdout line(s) and is the evidence "+
			"for why; do NOT commit it", initControlControlBudget, len(rec.StdoutEvents))
	}
}

// TestInitControlSummarize_ReadsBothPlacements is the targeted check on the one
// piece of logic in this file that is not a live measurement. It spawns nothing
// and passes on a machine with no claude and no credentials.
//
// The doubly-nested row is the load-bearing one, and it is not hypothetical: a
// summariser reading only the first two levels reported models_present:false
// against a live 2.1.239 reply carrying six models, which is what the third
// placement was added for.
func TestInitControlSummarize_ReadsAllThreePlacements(t *testing.T) {
	t.Parallel()

	const models = `[{"value":"opus","displayName":"Opus"},{"value":"haiku"}]`
	all := initControlSummary{subtype: "success", modelsPresent: true, modelsCount: 2,
		modelsEntryFields: []string{"displayName", "value"}} // sorted, not insertion-ordered

	tests := []struct {
		name string
		resp string
		want initControlSummary
	}{
		{"top level", `{"type":"control_response","subtype":"success","models":` + models + `}`, all},
		{"nested under response", `{"type":"control_response","response":{"subtype":"success","models":` + models + `}}`, all},
		{"nested under response.response, the measured shape",
			`{"type":"control_response","response":{"subtype":"success","response":{"models":` + models + `}}}`, all},
		{"an empty array is present with zero entries", `{"type":"control_response","response":{"subtype":"error","models":[]}}`,
			initControlSummary{subtype: "error", modelsPresent: true}},
		{"no placement carries models", `{"type":"control_response","response":{"subtype":"error","error":"unrecognized"}}`,
			initControlSummary{subtype: "error"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := initControlSummarize([]json.RawMessage{json.RawMessage(tc.resp)}); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("#1688: summarised %+v, want %+v; every field here is POPULATED into the "+
					"committed record and read by #1690's decoder", got, tc.want)
			}
		})
	}
}
