//go:build e2e_realclaude

package realclaude

// #1595 — does a `set_permission_mode` control request land in-band on a stream
// claude is already reading?
//
// # The finding, measured 2026-08-19 against claude 2.1.220
//
// The mechanism IS wired under pyry's stream-json-in/stream-json-out invocation —
// the "callback not registered" error never appeared — but it is ONE-WAY.
//
//   - REVOKE works, on all three independent reads. The control_response was
//     `{"subtype":"success", …,"response":{"mode":"default"}}`; the next turn's
//     system/init line reported permissionMode `default` where the first had
//     reported `bypassPermissions`; and turn 2 was behaviourally GATED
//     (permission_denials 1, tool_result is_error true), matching the
//     control_default arm exactly. VERDICT: REVOCATION APPLIED.
//
//   - ENABLE is refused, and refused by design. The control_response was
//     `{"subtype":"error", …,"error":"Cannot set permission mode to
//     bypassPermissions because the session was not launched with
//     --dangerously-skip-permissions"}` — a THIRD error string, not either of the
//     two the ticket read out of the binary. init.permissionMode stayed `default`
//     across both turns and turn 2 stayed gated. So an escalation over the
//     daemon's stdin channel is NOT reachable on a child that was launched
//     without the flag: claude gates the escalation on the launch argv, not on
//     the control request. Turn 2 matched the control_default arm.
//     VERDICT: ESCALATION FAILED.
//
// What that means for #1596: bypass can be DROPPED in-band without a respawn, and
// cannot be ADDED in-band at all. A design that routes only YOLO true→false
// in-band and keeps the restart for false→true is supported by this measurement;
// one that routes both directions is not.
//
// The probe DOES discriminate on this argv, so AC 3's no-discrimination branch did
// not fire. #383's finding #2 — a `default`-launched child auto-approved Bash
// anyway — does not carry over here, and the reason is the argv: that spike ran
// with --permission-prompt-tool stdio, which it concluded short-circuits
// enforcement. Without it, the control_default arm is gated (1 denial) and the
// control_bypass arm is not (0 denials), which is what makes the behavioural read
// meaningful.
//
// The whole-value verdict is not perfectly stable run to run, which is worth
// knowing before reading a future run's output. An earlier identical run on the
// same day returned INCONCLUSIVE for `enable` for one probe-level reason: claude
// retried the tool after the denial, so turn 2's tool_use_names read [Bash Bash]
// against both controls' [Bash], and the whole-value comparison matched neither.
// Every gate-sensitive field matched control_default in that run too. The verdict
// is deliberately left as a strict whole-value comparison — dropping a field for
// looking redundant is how a probe stops discriminating — and setModeFieldMatches
// exists so an INCONCLUSIVE can be read rather than merely reported.
//
// # Why a measurement and not a reading
//
// pyry expresses the bypass posture as a spawn-time flag, so changing it on a
// running session means killing the child and relaunching it with a recomposed
// argv (Session.spawnArgs → sup.Restart). #1581 moved the model/effort case off
// that restart — claude takes `/model` and `/effort` as ordinary user turns on the
// held-open stream — but inBandDeliverable still routes any update carrying a YOLO
// to the restart, because no in-band form for the bypass posture had been measured.
//
// claude 2.1.220 carries a `set_permission_mode` control subtype on the same
// held-open stdin the daemon already writes interrupts to (streamsup's
// WriteInterrupt / marshalInterruptEnvelope). Reading that subtype out of the
// binary settles nothing: the binary also carries the string
// `set_permission_mode is not supported in this context (onSetPermissionMode
// callback not registered)`, so whether the handler is WIRED under pyry's
// stream-json-in/stream-json-out invocation is exactly what a live run has to say.
//
// This file measures and records. It adds no production writer for the subtype;
// #1596 decides whether one is warranted.
//
// SUPERSEDED as of #2066, and left standing as the record of what was true when
// this probe ran, like its two siblings (bypass_reescalation_probe_test.go,
// bypass_approval_argv_probe_test.go). Two claims above are now historical. The
// paragraph directly above: pyry no longer expresses the posture as a spawn-time
// flag CHANGE, and inBandDeliverable no longer routes a YOLO update to the restart
// — it reads permissionModeKnown, so all six storable postures go in band. And the
// enable arm's design conclusion under "# The finding" above ("one that routes
// both directions is not supported by this measurement") is the shape #2066 shipped:
// what the arm measured is that a child launched WITHOUT the flag refuses the
// escalation, which is still true and is precisely why #2065 put the flag on every
// argv first. The chain this probe started ran #1603 (the writer), #1604 (the
// revocation routed), #2060 (claude accepting a RE-escalation on a flag-launched
// child at 2.1.239), #2065 (the flag unconditional) and #2066 (the enable routed).
// The measurement below is unaffected; only the sentences about what pyry can do
// with it are.
//
// # Four children, one drive sequence
//
//	arm             | launch flag                     | control request sent
//	----------------+---------------------------------+----------------------------
//	revoke          | --dangerously-skip-permissions   | mode: "default"
//	enable          | (none)                           | mode: "bypassPermissions"
//	control_default | (none)                           | (none)
//	control_bypass  | --dangerously-skip-permissions   | (none)
//
// Every arm runs the identical sequence — spawn, probe turn 1, [control request],
// probe turn 2, close stdin — so the comparison is index-symmetric. The control
// arms omit only the bracketed step and still drive BOTH turns: a measurement
// arm's post-change read is its turn 2, so its control must also be a turn 2, or
// the verdict folds in a turn-index confound.
//
// #2041, #2060 and #2061 ride this same driver with their own arms, and every
// widening they brought is INERT here. #2060 added an optional third turn and a
// second control request between turns 2 and 3; setModeChildConfig.promptThree is
// empty on this measurement, so these four arms still drive exactly the two turns
// described above. #2061 added per-arm extra launch flags, a first control request
// movable ahead of turn 1, a stage-mark hook and an approval observation; all four
// are zero-valued here, so this measurement's argv, ordering and record are
// byte-for-byte what its committed 2.1.220 captures already record.
//
// # Why the echoed response is not the verdict
//
// #383 measured that a `default`-launched child auto-approved Bash anyway, at
// 2.1.199 and 2.1.220, even with --allowed-tools Read. "The tool still ran" is
// therefore not on its own evidence that a revocation failed. The verdict is a
// behavioural comparison of turn 2 against the two control arms, and the echoed
// `control_response` never enters it: an echoed `success` that still behaves like
// the bypass control is a FAILED revocation, and an escalation whose behaviour
// matches the bypass control is a successful escalation even if its
// `control_response` was an error. Where the two controls behave identically the
// probe discriminates nothing on this argv, and the run says so rather than
// reporting either outcome.
//
// # Argv, and its three deliberate divergences from the #383 spike
//
//   - NO --permission-prompt-tool stdio. The spike concluded that flag
//     short-circuits allowlist enforcement; pyry's invocation does not carry it.
//   - NO --allowed-tools. Same reason — pyry's stream path does not set it.
//   - --dangerously-skip-permissions, not --permission-mode bypassPermissions.
//     It is the flag claudeSettingsArgs actually emits, and the ticket's measured
//     baseline already records the two agreeing on init.permissionMode.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -v \
//	  -run TestRealClaude_SetPermissionMode ./internal/e2e/realclaude/
//
// The test PASSES on every recorded outcome, in the manner of
// TestRealClaude_PermissionProtocol_Spike: a supported mechanism and an
// unsupported one are both findings, and only an instrument that measured nothing
// is a failure.

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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// A fresh EMPTY directory under the test's pinned $HOME, deliberately not a
	// git repo: less project context for claude to load, so the turns are cheaper.
	setModeWorkdirName = "set-permission-mode-work"

	// The two probe prompts, shared by all four arms. They differ from each other
	// so turn 2 is a fresh request rather than one claude can answer with "I
	// already did that". Bash and `ls -la` mirror the #383 spike's probe on
	// purpose: it makes finding #2 directly comparable rather than a second
	// unknown.
	setModePromptOne = "Use the Bash tool to run `ls -la /` and report the first line of output."
	setModePromptTwo = "Use the Bash tool to run `ls -la /tmp` and report the first line of output."

	setModeModel = "claude-haiku-4-5"

	// A token bound with ~2x headroom over the four assistant turns two probes
	// need (the spike measured 2 per probe). A result line carrying
	// subtype:"error_max_turns" lands in the fixture plainly — raise and rerun.
	setModeMaxTurns = "8"
)

const (
	// Hard kill per child. It is the outer bound: the per-step waits below can sum
	// past it on a fully stalling arm, in which case the deadline trips and the
	// fixture records that rather than the run hanging.
	setModeChildBudget = 4 * time.Minute

	// One probe turn. A healthy turn lands in seconds; the headroom is for a
	// `default`-mode child that blocks on an approval it can never receive.
	setModeTurnBudget = 2 * time.Minute

	// Long enough that an absent control_response means absence, not impatience.
	setModeControlBudget = 45 * time.Second

	setModePoll = 100 * time.Millisecond

	// Same 1 MiB cap and same reason as the spike's reader: the default 64 KiB is
	// too low for model output, and the bigger buffer is essentially free. A line
	// that exceeds it lands in scanner_error, so truncation is never silent.
	setModeScanMax = 1024 * 1024
)

// setModeNoDiscrimination is the verbatim sentence the run records, for BOTH
// directions, when the two control arms behave identically on turn 2. AC 3 names
// it explicitly: where the probe cannot separate the arms, the run reports this
// rather than a revocation or an escalation.
const setModeNoDiscrimination = "the behavioural probe does not discriminate on this argv"

// --- the arms ----------------------------------------------------------------

// setModeArm is one row of the table in the header comment. targetMode == ""
// marks a control arm: send no control request, drive both turns anyway.
type setModeArm struct {
	name       string
	launchYOLO bool
	targetMode string

	// secondTargetMode is the mode of a SECOND control request, written after turn
	// 2 and before turn 3. Empty means the arm sends only one, which is what both
	// existing construction sites get for free — each uses keyed fields.
	//
	// Additive rather than a []string replacing targetMode: only two sites build a
	// setModeArm and neither has to change for a new field, whereas replacing
	// targetMode would touch both plus every reader of RequestedMode. #2060 added it
	// for the re-escalation measurement, whose whole subject is two sequential
	// requests in one child.
	//
	// It is inert unless setModeChildConfig.promptThree is set: a second request is
	// only meaningful when a turn follows it to read the result off.
	secondTargetMode string

	// extraLaunchArgs are appended to the argv AFTER the launchYOLO branch below.
	// Nil on every arm before #2061, which is what keeps every committed capture's
	// argv byte-identical to what it already records.
	//
	// It exists because runSetModeChild builds its argv inline and branches only on
	// launchYOLO, so the ONLY two argvs reachable through this rig were the bare one
	// and the bare one plus --dangerously-skip-permissions. #2061's whole subject is
	// a third: the bypass flag beside production's four approval flags, which
	// permissionArgs composes and withApprovalArgs injects per spawn, and which no
	// capture in this package covers because withApprovalArgs returns early on
	// --dangerously-skip-permissions and so production never emits both.
	//
	// A []string rather than a typed flag set: this rig has no business knowing what
	// a permission flag is, and a caller measuring some other argv gets the axis for
	// free. Nothing here validates the contents — the caller owns them, and every
	// element on every arm today is a package constant or a path the caller minted.
	extraLaunchArgs []string

	// requestBeforeFirstTurn moves the FIRST control request ahead of turn 1,
	// instead of between turns 1 and 2. False on every arm before #2061.
	//
	// #2060 moved the SECOND request's position; this moves the first's, and the two
	// are independent knobs because the sequence they describe is. #2061 needs it to
	// ask whether anything can execute between launch and a downgrade landing —
	// #1686's design writes the downgrade immediately at spawn, and every request
	// this rig could write was positioned after a turn, so the window it opens was
	// unreachable through the driver.
	//
	// The send-and-WAIT is unchanged when it moves: the ack is awaited on
	// setModeControlBudget before turn 1 is written. That is deliberate, and is the
	// case most favourable to a design that wants the downgrade to have landed — an
	// ungated turn after an awaited ack is a real race window rather than an
	// artefact of not waiting, and an ack that never drew before turn 1 says the
	// request cannot be written that early at all.
	requestBeforeFirstTurn bool
}

var setModeArms = []setModeArm{
	{name: "revoke", launchYOLO: true, targetMode: "default"},
	{name: "enable", launchYOLO: false, targetMode: "bypassPermissions"},
	{name: "control_default", launchYOLO: false},
	{name: "control_bypass", launchYOLO: true},
}

// setModeDirection pairs a measurement arm with the two controls its turn-2
// behaviour is classified against.
type setModeDirection struct {
	arm            string
	appliedControl string // matching this control means the change took effect
	failedControl  string // matching this one means it did not
	appliedVerdict string
	failedVerdict  string
}

var setModeDirections = []setModeDirection{
	{
		arm:            "revoke",
		appliedControl: "control_default",
		failedControl:  "control_bypass",
		appliedVerdict: "REVOCATION APPLIED",
		failedVerdict:  "REVOCATION FAILED",
	},
	{
		arm:            "enable",
		appliedControl: "control_bypass",
		failedControl:  "control_default",
		appliedVerdict: "ESCALATION APPLIED",
		failedVerdict:  "ESCALATION FAILED",
	},
}

// --- the wire shapes ---------------------------------------------------------

// setModeControlRequest mirrors streamsup's controlRequest, whose doc comment
// notes it is kept structured for "future control subtypes". This is one of them.
// The inner struct carries the `mode` field the interrupt subtype has no use for.
type setModeControlRequest struct {
	Type      string                     `json:"type"`       // "control_request"
	RequestID string                     `json:"request_id"` // locally-minted correlation id
	Request   setModeControlRequestInner `json:"request"`
}

type setModeControlRequestInner struct {
	Subtype string `json:"subtype"` // "set_permission_mode"
	Mode    string `json:"mode"`
}

// setModeControlLine returns the single newline-terminated control line:
//
//	{"type":"control_request","request_id":"<id>",
//	 "request":{"subtype":"set_permission_mode","mode":"<mode>"}}
//
// Marshalled structured, never string-concatenated — the same one-physical-line
// invariant marshalInterruptEnvelope holds, so the appended '\n' is the only raw
// newline in the envelope.
func setModeControlLine(requestID, mode string) ([]byte, error) {
	b, err := json.Marshal(setModeControlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   setModeControlRequestInner{Subtype: "set_permission_mode", Mode: mode},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal set_permission_mode control request: %w", err)
	}
	return append(b, '\n'), nil
}

// setModeTurnLine returns the single newline-terminated user-turn line in pyry's
// production envelope shape. streamsup's marshalTurnEnvelope is unexported, so the
// shape is mirrored here — structured encoding, same discipline.
func setModeTurnLine(prompt string) ([]byte, error) {
	env := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": prompt}},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal user turn: %w", err)
	}
	return append(b, '\n'), nil
}

// --- the behavioural read ----------------------------------------------------

// probeOutcome is the behavioural read of ONE probe turn. Two arms "behave the
// same" iff their outcomes are equal, so every field here is a potential
// discriminator and none may be dropped for looking redundant: a denial shows up
// in PermissionDenials / ToolResultIsError, and a child that blocks on an
// approval it can never receive shows up as ResultObserved false.
type probeOutcome struct {
	ToolUseNames      []string `json:"tool_use_names"` // in arrival order
	ToolResultSeen    bool     `json:"tool_result_seen"`
	ToolResultIsError bool     `json:"tool_result_is_error"`
	PermissionDenials int      `json:"permission_denials"` // from the result trailer
	ResultSubtype     string   `json:"result_subtype"`
	ResultIsError     bool     `json:"result_is_error"`
	ResultObserved    bool     `json:"result_observed"` // false ⇒ the turn never closed
}

func (o probeOutcome) equal(other probeOutcome) bool {
	return slices.Equal(o.ToolUseNames, other.ToolUseNames) &&
		o.ToolResultSeen == other.ToolResultSeen &&
		o.ToolResultIsError == other.ToolResultIsError &&
		o.PermissionDenials == other.PermissionDenials &&
		o.ResultSubtype == other.ResultSubtype &&
		o.ResultIsError == other.ResultIsError &&
		o.ResultObserved == other.ResultObserved
}

func (o probeOutcome) String() string {
	return fmt.Sprintf("tools=%v tool_result=%v tool_result_error=%v denials=%d result=%q result_error=%v result_observed=%v",
		o.ToolUseNames, o.ToolResultSeen, o.ToolResultIsError, o.PermissionDenials,
		o.ResultSubtype, o.ResultIsError, o.ResultObserved)
}

// setModeProbeOutcome reduces one turn window to its behavioural read. closed
// reports whether the window ended on a `result` line.
func setModeProbeOutcome(window []json.RawMessage, closed bool) probeOutcome {
	out := probeOutcome{ResultObserved: closed}
	for _, ev := range window {
		var env struct {
			Type              string            `json:"type"`
			Subtype           string            `json:"subtype"`
			IsError           bool              `json:"is_error"`
			PermissionDenials []json.RawMessage `json:"permission_denials"`
			Message           struct {
				// RawMessage, not a typed slice: claude carries `content` as a
				// string on some lines, and a typed slice there would fail the
				// whole decode and take the sibling scalars down with it.
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(ev, &env); err != nil {
			continue
		}
		var blocks []struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			IsError bool   `json:"is_error"`
		}
		if len(env.Message.Content) > 0 {
			_ = json.Unmarshal(env.Message.Content, &blocks) // string content ⇒ no blocks
		}
		for _, blk := range blocks {
			switch blk.Type {
			case "tool_use":
				out.ToolUseNames = append(out.ToolUseNames, blk.Name)
			case "tool_result":
				out.ToolResultSeen = true
				if blk.IsError {
					out.ToolResultIsError = true
				}
			}
		}
		if env.Type == "result" {
			out.ResultSubtype = env.Subtype
			out.ResultIsError = env.IsError
			out.PermissionDenials = len(env.PermissionDenials)
		}
	}
	return out
}

// setModeTurnWindows slices the captured lines at the recorded `result` indices
// and reduces each window. A trailing window with no closing result is emitted
// with ResultObserved false — a turn that never closed is a legitimate outcome,
// not an omission.
func setModeTurnWindows(lines []json.RawMessage, boundaries []int) []probeOutcome {
	var out []probeOutcome
	start := 0
	for _, b := range boundaries {
		if b < start || b >= len(lines) {
			continue
		}
		out = append(out, setModeProbeOutcome(lines[start:b+1], true))
		start = b + 1
	}
	if start < len(lines) {
		out = append(out, setModeProbeOutcome(lines[start:], false))
	}
	return out
}

// setModeOutcomeAt returns the i-th turn outcome, or the zero value (which reads
// ResultObserved false) when the arm produced fewer windows than that.
func setModeOutcomeAt(outcomes []probeOutcome, i int) probeOutcome {
	if i >= 0 && i < len(outcomes) {
		return outcomes[i]
	}
	return probeOutcome{}
}

// setModeFieldMatches reports, field by field, which control a measurement arm's
// turn-2 outcome agrees with.
//
// It is a RECORDING of the comparison, not a second verdict, and it never feeds
// the verdict above — that one stays a whole-value comparison, because dropping a
// field for looking redundant is exactly how a probe stops discriminating. Its
// job is to make an INCONCLUSIVE actionable. Observed 2026-08-19: one run of
// `enable` came back INCONCLUSIVE only because claude retried the tool after the
// denial (tool_use_names [Bash Bash] against the controls' [Bash]), while every
// gate-sensitive field matched control_default; a rerun of the same code the same
// day classified it ESCALATION FAILED. Without this breakdown a reader cannot tell
// that case from a genuinely unclassifiable one.
func setModeFieldMatches(got, applied, failed probeOutcome, appliedName, failedName string) []string {
	rows := []struct{ name, gotV, appliedV, failedV string }{
		{"tool_use_names", fmt.Sprint(got.ToolUseNames), fmt.Sprint(applied.ToolUseNames), fmt.Sprint(failed.ToolUseNames)},
		{"tool_result_seen", fmt.Sprint(got.ToolResultSeen), fmt.Sprint(applied.ToolResultSeen), fmt.Sprint(failed.ToolResultSeen)},
		{"tool_result_is_error", fmt.Sprint(got.ToolResultIsError), fmt.Sprint(applied.ToolResultIsError), fmt.Sprint(failed.ToolResultIsError)},
		{"permission_denials", fmt.Sprint(got.PermissionDenials), fmt.Sprint(applied.PermissionDenials), fmt.Sprint(failed.PermissionDenials)},
		{"result_subtype", got.ResultSubtype, applied.ResultSubtype, failed.ResultSubtype},
		{"result_is_error", fmt.Sprint(got.ResultIsError), fmt.Sprint(applied.ResultIsError), fmt.Sprint(failed.ResultIsError)},
		{"result_observed", fmt.Sprint(got.ResultObserved), fmt.Sprint(applied.ResultObserved), fmt.Sprint(failed.ResultObserved)},
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var lean string
		switch {
		case r.appliedV == r.failedV:
			lean = "both controls agree — not a discriminator"
		case r.gotV == r.appliedV:
			lean = "matches " + appliedName
		case r.gotV == r.failedV:
			lean = "matches " + failedName
		default:
			lean = fmt.Sprintf("matches neither (%s=%s, %s=%s)", appliedName, r.appliedV, failedName, r.failedV)
		}
		out = append(out, fmt.Sprintf("%s=%s → %s", r.name, r.gotV, lean))
	}
	return out
}

// --- the stdout recorder -----------------------------------------------------

// setModeRecorder is the stdout sink: it retains every line verbatim and
// classifies each one as it arrives. Mutex-guarded because the reader goroutine
// appends while the test goroutine polls; that is a race under -race.
//
// It is deliberately NOT inbandTapRecorder: that one is wired into
// streamsup.Config.Stdout and carries its own line splitter and partial-line
// accumulator because os/exec hands it arbitrary byte chunks. Here the file owns
// cmd.StdoutPipe() directly, so bufio.Scanner does the splitting — exactly as
// TestRealClaude_PermissionProtocol_Spike already does.
type setModeRecorder struct {
	mu               sync.Mutex
	lines            []json.RawMessage
	results          int
	turnBoundaries   []int
	controlResponses []json.RawMessage
	initModes        []string
	nonJSON          int
}

// add records one complete stdout line.
//
// A line that is not valid JSON is retained as a JSON *string* rather than
// dropped: the fixture must stay parseable (a raw non-JSON byte run embedded in
// stdout_events would make the whole file invalid), and a line claude emitted is
// evidence whether or not this file can parse it. nonJSON counts them so a reader
// can tell the two encodings apart.
func (r *setModeRecorder) add(raw []byte) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return
	}
	line := make([]byte, len(trimmed))
	copy(line, trimmed)

	r.mu.Lock()
	defer r.mu.Unlock()

	if !json.Valid(line) {
		r.nonJSON++
		if quoted, err := json.Marshal(string(line)); err == nil {
			r.lines = append(r.lines, json.RawMessage(quoted))
		}
		return
	}
	idx := len(r.lines)
	r.lines = append(r.lines, json.RawMessage(line))

	var env struct {
		Type           string `json:"type"`
		Subtype        string `json:"subtype"`
		PermissionMode string `json:"permissionMode"`
	}
	// A decode failure is skipped silently, mirroring inbandTapRecorder.consume:
	// claude's stdout carries lines this file has no interest in, and one of them
	// must not abort the arm.
	if err := json.Unmarshal(line, &env); err != nil {
		return
	}
	switch {
	case env.Type == "system" && env.Subtype == "init":
		r.initModes = append(r.initModes, env.PermissionMode)
	case env.Type == "result":
		r.results++
		r.turnBoundaries = append(r.turnBoundaries, idx)
	case env.Type == "control_response":
		r.controlResponses = append(r.controlResponses, json.RawMessage(line))
	}
}

func (r *setModeRecorder) snapshotLines() []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]json.RawMessage(nil), r.lines...)
}

func (r *setModeRecorder) resultCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.results
}

func (r *setModeRecorder) snapshotBoundaries() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.turnBoundaries...)
}

func (r *setModeRecorder) snapshotControlResponses() []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]json.RawMessage(nil), r.controlResponses...)
}

func (r *setModeRecorder) controlResponseCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.controlResponses)
}

func (r *setModeRecorder) snapshotInitModes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.initModes...)
}

func (r *setModeRecorder) nonJSONCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nonJSON
}

// setModeWaitFor polls count until it reaches want, reporting whether it got
// there within budget. Same idiom and same interval as inbandWaitResults.
func setModeWaitFor(count func() int, want int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if count() >= want {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(setModePoll)
	}
}

// --- the fixture -------------------------------------------------------------

// setModeSecondRequest records an arm's SECOND control request, and is nil on
// every arm that sent only one.
//
// A nested pointer rather than four flat omitempty fields beside their
// first-request twins, and the bool is the reason: ControlResponseIDMatched's
// FALSE is a finding — a reply that correlated to nothing — while an omitempty
// bool spells false by ABSENCE, the exact trap modeSwitchAutoObservation's fields
// exist to avoid. Nil says "no second request was sent"; a present value says all
// four fields are real, false included.
type setModeSecondRequest struct {
	RequestedMode            string          `json:"requested_mode"`
	ControlRequestID         string          `json:"control_request_id"`
	ControlRequestSent       json.RawMessage `json:"control_request_sent"`
	ControlResponseIDMatched bool            `json:"control_response_id_matched"`
}

// setModeFixtureRecord is the durable artifact. The JSON field names are the
// contract #1596 and any future re-measurement read.
//
// There is deliberately no `env` field: the credential reaches the child through
// the environment (WithWorktreeAuthenticated) and the argv carries none, so
// recording argv is safe and recording env would not be. stderr is truncated at
// the spike's 8 KiB cap so an auth failure cannot dump an unbounded
// credential-bearing message into a committed file.
type setModeFixtureRecord struct {
	ClaudeVersionRaw string `json:"claude_version_raw"`
	ClaudeVersion    string `json:"claude_version"`

	Arm            string   `json:"arm"`
	LaunchYOLOFlag bool     `json:"launch_yolo_flag"`
	Argv           []string `json:"argv"`
	Prompts        []string `json:"prompts"`

	RequestedMode                   string            `json:"requested_mode"`
	ControlRequestID                string            `json:"control_request_id"`
	ControlRequestSent              json.RawMessage   `json:"control_request_sent"`
	ControlResponses                []json.RawMessage `json:"control_responses"`
	ControlResponseRequestIDMatched bool              `json:"control_response_request_id_matched"`

	// SecondRequest is #2060's two-request arm and nil on every other. Both
	// requests' replies land in ControlResponses in arrival order regardless — this
	// records which line was SENT second and whether anything correlated to it.
	SecondRequest *setModeSecondRequest `json:"second_control_request,omitempty"`

	InitPermissionModes []string          `json:"init_permission_modes"`
	StdoutEvents        []json.RawMessage `json:"stdout_events"`
	NonJSONLineCount    int               `json:"non_json_line_count"`
	TurnBoundaries      []int             `json:"turn_boundaries"`
	ProbeOutcomes       []probeOutcome    `json:"probe_outcomes"`

	// Model is the value that reached `--model`. It is written on every arm,
	// #1595's four included: the model was a package constant when this record
	// was designed, and a capture that does not name it cannot be read against a
	// later one taken on a different model.
	Model string `json:"model"`

	// ModeSwitchAuto is #2041's observation of what the live model list published
	// for Model, and is nil on every arm that did not ask. Its inner fields carry
	// no omitempty on purpose — supports_auto_mode is exactly the field whose
	// false claude spells by ABSENCE, so a capture that omitted it would
	// reproduce the trap the measurement exists to avoid.
	ModeSwitchAuto *modeSwitchAutoObservation `json:"mode_switch_auto,omitempty"`

	// Approval is #2061's record of what reached its stub approval socket, sliced
	// by the drive sequence's stages, and is nil on every arm that ran with no such
	// socket. Its inner fields carry no omitempty for ModeSwitchAuto's reason: a
	// ZERO approval count is the finding on the arm whose bridge never came back,
	// and an omitempty int would spell that finding by ABSENCE.
	Approval *bypassArgvApproval `json:"approval,omitempty"`

	// EffortCapture is #2251's block and is nil on every arm that set no effort. Its
	// inner fields carry no omitempty for Approval's reason, sharpened by what this
	// one measures: the whole capture exists to make an ABSENT effort key mean
	// something, and an omitempty bool spelling launch_flag_seen false by absence
	// would leave the two witnesses unreadable in exactly the case they carry.
	//
	// Named effort_capture rather than effort so it cannot be mistaken for the init
	// line's own key of that name, which is the thing being measured and lives inside
	// init_lines.
	EffortCapture *effortInitObservation `json:"effort_capture,omitempty"`

	StdinWriteErrors       []string `json:"stdin_write_errors"`
	StderrCapture          string   `json:"stderr_capture"`
	ExitCode               int      `json:"exit_code"`
	WaitError              string   `json:"wait_error"`
	ContextDeadlineTripped bool     `json:"context_deadline_tripped"`
	DurationMs             int64    `json:"duration_ms"`
	ScannerError           string   `json:"scanner_error"`
}

// setModeFixtureName mints the filename for one arm. The arm token keeps the two
// directions independently recoverable (AC 4) — the spike's writeFixture embeds
// only the version, so two directions written through it would collide.
//
// The `set_permission_mode_` prefix also keeps the name out of BOTH testdata
// globs in this package; TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs
// asserts that deterministically rather than leaving it a convention to remember.
func setModeFixtureName(versionToken, arm string) string {
	return fmt.Sprintf("set_permission_mode_v%s_%s.json", versionSlug(versionToken), arm)
}

func setModeFixturePath(t *testing.T, versionToken, arm string) string {
	t.Helper()
	return filepath.Join(packageDir(t), "testdata", setModeFixtureName(versionToken, arm))
}

// writeSetModeFixture writes rec through a temp file and a rename, so an
// interrupted run cannot leave a half-written fixture behind for a later commit.
//
// fixturePath mints the destination. It is a PARAMETER rather than a second
// writer function beside this one, and that is a security property rather than a
// style choice: this name is on eleven finOfflineExecBans lists as the identifier
// that fences an offline file off from the committed testdata/, and a sibling
// writeXFixture would be a second route to packageDir that every one of those
// entries silently fails to cover.
// screen is setModeChildConfig.screenFixture, threaded through rather than reached
// for: this writer is package-level and takes no config. A nil screen writes the
// marshalled record unchanged, which is every caller before #2251. A screen that
// returns false writes NOTHING and this function returns an empty path — the
// caller decides what to say about a refusal, because only it knows what the
// screen was looking for.
func writeSetModeFixture(t *testing.T, rec *setModeFixtureRecord,
	fixturePath func(t *testing.T, versionToken, arm string) string,
	screen func(t *testing.T, data []byte) ([]byte, bool)) string {
	t.Helper()
	dir := filepath.Join(packageDir(t), "testdata")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("#1595: mkdir testdata: %v", err)
	}
	path := fixturePath(t, rec.ClaudeVersion, rec.Arm)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("#1595: marshal fixture for arm %q: %v", rec.Arm, err)
	}
	if screen != nil {
		screened, ok := screen(t, data)
		if !ok {
			return ""
		}
		data = screened
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatalf("#1595: write fixture tmp: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("#1595: rename fixture: %v", err)
	}
	return path
}

// --- the driver --------------------------------------------------------------

// runSetModeChild spawns one child, drives the sequence in the header comment,
// writes the arm's fixture and returns the completed record.
//
// It t.Fatalf's only for a broken instrument — a spawn failure, a marshal failure,
// or an arm that captured ZERO stdout lines (a fixture with no events records
// nothing). Every other outcome is information and lands in a fixture field: a
// turn that never closed, a control_response that never came, one that came with
// subtype "error", a mismatched request_id, an absent init line, a stdin write
// error, a non-zero exit, a tripped deadline, a scanner error.
// setModeChildConfig carries what a CALLER varies across measurements while the
// drive sequence stays fixed. #1595's four arms all take the same one, built from
// this file's constants; #2041's five take one per arm, because its whole subject
// is a per-arm model.
//
// It exists because runSetModeChild hardcoded four things a second measurement
// must vary — the model, the two prompts, the turn bound, and the fixture family
// its writer mints into — and copying the driver to change them would have
// duplicated the recorder, the reader goroutine and the wait discipline along
// with them.
type setModeChildConfig struct {
	// model reaches `--model` verbatim. A caller deriving it from anything a
	// subprocess said must shape-check it first: a value beginning with `-`
	// parses as a FLAG, not as this flag's value. See modeSwitchModelValueOK.
	model string

	// promptOne and promptTwo are the two probe turns. They must differ from each
	// other, so turn 2 is a fresh request rather than one claude can answer with
	// "I already did that".
	promptOne string
	promptTwo string

	// promptThree is an OPTIONAL third probe turn, driven after the second control
	// request. Empty means the child stops after turn 2, which is what #1595's and
	// #2041's callers get.
	//
	// It exists so a two-request arm's post-change read can sit at a turn of its
	// own. The header's index-symmetry rule says a measurement arm's read and its
	// control must share a turn index, so a measurement reading turn 3 needs
	// controls that HAVE a turn 3; #2060's control arms set this and send no
	// request at all.
	promptThree string

	maxTurns string

	// fixturePath mints the arm's destination, and is what keeps two measurements
	// in separate committed families.
	fixturePath func(t *testing.T, versionToken, arm string) string

	// autoMode is recorded verbatim into the arm's fixture and read by nothing
	// here. nil on a measurement that asked claude nothing about its model list.
	autoMode *modeSwitchAutoObservation

	// mark is called once after each COMPLETED step of the drive sequence, with one
	// of the setModeStage* constants. nil on every caller before #2061 and
	// nil-checked at every site, so it costs an existing measurement nothing.
	//
	// It exists so a caller can attribute an event this rig knows nothing about to
	// the turn that produced it. #2061 watches a stub approval socket claude reaches
	// over a SEPARATE channel — nothing on it passes through this file — and its
	// verdict is not "how many approvals in total" but "was the bridge consulted on
	// the post-downgrade turn". A total cannot answer that; a snapshot at each stage
	// boundary can, because the driver is strictly serialised and waits for each
	// turn's result line before writing the next.
	//
	// Marks fire from the TEST goroutine only, so an observer needs no lock against
	// this file — though #2061's does need one against its own accept loop.
	//
	// controlResponses is this rig's OWN running count at that moment, handed over
	// rather than left to be re-derived. An observer cannot recover it from the
	// record: the record is written once at the end, and "was the request acked
	// BEFORE turn 1" is a question about a moment, not about a total. Reading the
	// stdout order instead would rest on whether claude emits its system/init line
	// at spawn or per turn, which is exactly the kind of assumption a measurement
	// ticket must not build a verdict on.
	mark func(stage string, controlResponses int)

	// approval, when non-nil, is called ONCE just before the record is built, and
	// its result stored verbatim on the record. nil on every caller before #2061.
	//
	// A closure rather than a value like autoMode, and the difference is when the
	// observation completes: autoMode is known before the child starts, while an
	// observation OF the child is only complete after it has exited. Calling it here
	// rather than filling the field after runSetModeChild returns keeps
	// writeSetModeFixture the single writer — a caller that filled the field
	// afterwards would have to write the fixture a second time.
	approval func() *bypassArgvApproval

	// redactRunLocal, when non-nil, is applied to every RECORDED argv token and to
	// the stderr capture. It never touches what is EXECUTED. nil before #2061.
	//
	// #2061 hands claude two run-local temp paths on the argv — an mcp-config and a
	// Unix socket — and both would otherwise be committed. /var/folders/ and
	// /private/var/folders/ are two of dropcapFixedNeedles' five fixed deny classes,
	// so committing one cuts against this package's own grain; and a fresh random
	// path per run makes the captures undiffable, in a measurement whose whole
	// subject is the argv. It is a redaction of RUN-LOCAL NOISE, not of a
	// credential: initControlScrubbed below is the credential guard and runs first,
	// unconditionally, whether or not this is set.
	redactRunLocal func(string) string

	// fillRecord, when non-nil, is called with the completed record after it is
	// built and BEFORE it is marshalled, so a caller can record an observation only
	// the finished record can produce. nil before #2251.
	//
	// It exists because approval above cannot serve: that closure is handed no
	// record, and #2251's observation — which init lines claude emitted, what each
	// carried, and which turn's assistant text acknowledged the in-band change — is
	// read out of the very fields runSetModeChild has just assembled.
	//
	// It may MUTATE the record. That is the point, and it is safe here for the same
	// reason approval's timing is: nothing has read the record yet, and
	// writeSetModeFixture is still the only thing that will write it.
	fillRecord func(t *testing.T, rec *setModeFixtureRecord)

	// screenFixture, when non-nil, is the last pass over the MARSHALLED record on
	// its way to testdata/: it returns the bytes to write and a verdict, and false
	// writes nothing at all. nil before #2251.
	//
	// It sees bytes rather than fields because a redaction applied field by field
	// leaks whatever field its author forgot, and this record carries claude's
	// verbatim stdout. redactRunLocal above is the narrow, informative pass over the
	// argv; this is the whole-record sweep and the deny-scan behind it, and #2251's
	// AC 5 is the requirement that a scan hit leave nothing on disk.
	//
	// It is a knob on the CONFIG and a parameter to the writer, rather than a second
	// writer function: that one is the single fenced route to packageDir, named on
	// eleven finOfflineExecBans lists, and a sibling would be a route none of them
	// cover.
	screenFixture func(t *testing.T, data []byte) ([]byte, bool)
}

// The stages setModeChildConfig.mark names, in drive order. Each fires AFTER that
// step completed — after the turn's result line was awaited, or after the control
// request's response was.
//
// setModeStagePreTurnControl and setModeStageControlOne are two different points
// and not one renamed: an arm setting requestBeforeFirstTurn marks the former and
// never the latter, so an observer can tell a downgrade written before turn 1 from
// one written after it without being told which arm it is watching.
const (
	setModeStageStart          = "start"            // child spawned, nothing written yet
	setModeStagePreTurnControl = "pre_turn_control" // the before-turn-1 request settled
	setModeStageTurnOne        = "turn_1"
	setModeStageControlOne     = "control_1"
	setModeStageTurnTwo        = "turn_2"
	setModeStageControlTwo     = "control_2"
	setModeStageTurnThree      = "turn_3"
)

func runSetModeChild(t *testing.T, claudeBin, workdir string, arm setModeArm,
	versionRaw, versionToken string, cfg setModeChildConfig) *setModeFixtureRecord {
	t.Helper()

	if cfg.model == "" {
		t.Fatalf("#1595[%s]: no model configured; the child would launch on claude's default "+
			"and the record would name a model the run never chose", arm.name)
	}

	argv := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", cfg.model,
		"--max-turns", cfg.maxTurns,
	}
	if arm.launchYOLO {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	// Last, so the bypass flag keeps the position every committed capture records
	// and a diff against one of those shows the extras as a pure suffix.
	argv = append(argv, arm.extraLaunchArgs...)

	ctx, cancel := context.WithTimeout(context.Background(), setModeChildBudget)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin, argv...)
	cmd.Dir = workdir

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("#1595[%s]: stdin pipe: %v", arm.name, err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("#1595[%s]: stdout pipe: %v", arm.name, err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("#1595[%s]: start claude: %v", arm.name, err)
	}

	// Single reader goroutine over the child's stdout. It exits on EOF — which
	// follows either the graceful stdin close below or the context kill — and
	// closes readerDone, so no goroutine outlives its child. scannerErr is written
	// only before that close and read only after it.
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

	turnOne, err := setModeTurnLine(cfg.promptOne)
	if err != nil {
		t.Fatalf("#1595[%s]: %v", arm.name, err)
	}
	turnTwo, err := setModeTurnLine(cfg.promptTwo)
	if err != nil {
		t.Fatalf("#1595[%s]: %v", arm.name, err)
	}

	mark := func(stage string) {
		if cfg.mark != nil {
			cfg.mark(stage, rec.controlResponseCount())
		}
	}
	mark(setModeStageStart)

	var (
		controlSent json.RawMessage
		requestID   string
	)
	// Lifted into a closure so arm.requestBeforeFirstTurn can call it from either
	// of two positions with an identical body, budget and correlation recording.
	// Copying it to a second site is how the two positions would silently drift.
	sendFirstRequest := func() {
		if arm.targetMode == "" {
			return
		}
		// A correlation token, not a security token — the same reason
		// (*Runner).Interrupt mints its id from a monotonic counter. A fixed
		// per-arm id keeps the fixture diffable.
		requestID = "set-permission-mode-" + arm.name
		line, err := setModeControlLine(requestID, arm.targetMode)
		if err != nil {
			t.Fatalf("#1595[%s]: %v", arm.name, err)
		}
		controlSent = json.RawMessage(bytes.TrimRight(line, "\n"))
		t.Logf("#1595[%s]: writing control request: %s", arm.name, controlSent)
		writeLine("control request", line)
		if !setModeWaitFor(rec.controlResponseCount, 1, setModeControlBudget) {
			t.Logf("#1595[%s]: no control_response within %s (recorded as absence, continuing)",
				arm.name, setModeControlBudget)
		}
	}

	// #2061's ordering knob. The stage mark fires even on an arm that sends nothing
	// here, so an observer's window boundaries exist on every arm and a control's
	// "zero approvals before turn 1" is a read rather than a missing stage.
	if arm.requestBeforeFirstTurn {
		sendFirstRequest()
	}
	mark(setModeStagePreTurnControl)

	writeLine("turn 1", turnOne)
	if !setModeWaitFor(rec.resultCount, 1, setModeTurnBudget) {
		t.Logf("#1595[%s]: turn 1 produced no result line within %s (recorded, continuing)",
			arm.name, setModeTurnBudget)
	}
	mark(setModeStageTurnOne)

	if !arm.requestBeforeFirstTurn {
		sendFirstRequest()
	}
	mark(setModeStageControlOne)

	// Turn 2 is driven unconditionally, on the control arms too. It is what makes
	// recording an absent init line legitimate under AC 2: the init line is
	// emitted per turn rather than at spawn, so "no init after the control
	// request" observed without driving a further turn would be empty by
	// construction.
	baseline := rec.resultCount()
	writeLine("turn 2", turnTwo)
	if !setModeWaitFor(rec.resultCount, baseline+1, setModeTurnBudget) {
		t.Logf("#1595[%s]: turn 2 produced no result line within %s (recorded, continuing)",
			arm.name, setModeTurnBudget)
	}
	mark(setModeStageTurnTwo)

	// The optional second control request and third turn, #2060's addition. Both are
	// inert on a caller leaving promptThree empty, which is every caller before it.
	// The second request goes BETWEEN turns 2 and 3 for the same reason the first
	// goes between turns 1 and 2: the read that follows a posture change has to be a
	// full turn, since the init line is emitted per turn rather than at spawn.
	var secondRequest *setModeSecondRequest
	if cfg.promptThree != "" {
		if arm.secondTargetMode != "" {
			// Minted from the arm name the same way the first request is, rather
			// than derived from requestID: that one is empty on an arm sending no
			// FIRST request, and deriving would silently mint a bare "-2".
			requestIDTwo := "set-permission-mode-" + arm.name + "-2"
			line, err := setModeControlLine(requestIDTwo, arm.secondTargetMode)
			if err != nil {
				t.Fatalf("#1595[%s]: %v", arm.name, err)
			}
			sent := json.RawMessage(bytes.TrimRight(line, "\n"))
			t.Logf("#1595[%s]: writing second control request: %s", arm.name, sent)

			// baseline+1, never an absolute 2: an unsolicited control_response
			// earlier in this child would otherwise satisfy the wait before claude
			// answered this request at all.
			baseline := rec.controlResponseCount()
			writeLine("control request 2", line)
			if !setModeWaitFor(rec.controlResponseCount, baseline+1, setModeControlBudget) {
				t.Logf("#1595[%s]: no second control_response within %s (recorded as absence, continuing)",
					arm.name, setModeControlBudget)
			}
			secondRequest = &setModeSecondRequest{
				RequestedMode:      arm.secondTargetMode,
				ControlRequestID:   requestIDTwo,
				ControlRequestSent: sent,
			}
		}
		mark(setModeStageControlTwo)

		turnThree, err := setModeTurnLine(cfg.promptThree)
		if err != nil {
			t.Fatalf("#1595[%s]: %v", arm.name, err)
		}
		baseline := rec.resultCount()
		writeLine("turn 3", turnThree)
		if !setModeWaitFor(rec.resultCount, baseline+1, setModeTurnBudget) {
			t.Logf("#1595[%s]: turn 3 produced no result line within %s (recorded, continuing)",
				arm.name, setModeTurnBudget)
		}
		mark(setModeStageTurnThree)
	}

	if err := stdinPipe.Close(); err != nil {
		writeErrs = append(writeErrs, fmt.Sprintf("stdin close: %v", err))
	}
	waitErr := cmd.Wait()
	<-readerDone
	duration := time.Since(start)

	// A cap is not a redaction. An auth failure is exactly the condition that
	// makes claude print a long message to stderr, stderrFixtureCap bounds how
	// much of it lands in the file, and 8 KiB of a credential-bearing message
	// still commits the credential to a public repo. This guard fatals BEFORE any
	// bytes are written and is inert when neither variable is set, so it cannot
	// misfire on a machine with no token. initialize_control_probe_test.go added
	// it for its own family after this file shipped; #2041's captures are the
	// second family here to carry claude stderr and the call covers both.
	//
	// Scrub before the raw stderr can reach a failure message below. The guard
	// refuses to record a credential OR to print an excerpt of one, and the
	// no-stdout fatal underneath prints stderr through truncateString — a cap,
	// by the same argument this comment already makes. The trigger is the exact
	// condition named above: credentials present, an auth failure, a child that
	// dies producing no stdout, landing the token in a run log. Ordering is the
	// whole guard; runModeSwitchDiscovery scrubs before its own fatal for this
	// reason.
	initControlScrubbed(t, stderrBuf.String())

	lines := rec.snapshotLines()
	if len(lines) == 0 {
		t.Fatalf("#1595[%s]: claude produced no stdout; there is nothing to capture\nstderr:\n%s\nwaitErr: %v",
			arm.name, truncateString(stderrBuf.String(), stderrFixtureCap), waitErr)
	}

	boundaries := rec.snapshotBoundaries()
	responses := rec.snapshotControlResponses()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	waitErrStr := ""
	if waitErr != nil {
		waitErrStr = waitErr.Error()
	}
	// Correlated here rather than at send time: the reply had not arrived yet then.
	if secondRequest != nil {
		secondRequest.ControlResponseIDMatched = setModeResponseIDMatches(responses, secondRequest.ControlRequestID)
	}
	prompts := []string{cfg.promptOne, cfg.promptTwo}
	if cfg.promptThree != "" {
		prompts = append(prompts, cfg.promptThree)
	}

	// Applied to what is RECORDED, never to what was executed — the child above ran
	// on the real argv and its real stderr already went through initControlScrubbed.
	recordedArgv := append([]string{claudeBin}, argv...)
	recordedStderr := stderrBuf.String()
	if cfg.redactRunLocal != nil {
		redacted := make([]string, 0, len(recordedArgv))
		for _, tok := range recordedArgv {
			redacted = append(redacted, cfg.redactRunLocal(tok))
		}
		recordedArgv = redacted
		recordedStderr = cfg.redactRunLocal(recordedStderr)
	}
	var approval *bypassArgvApproval
	if cfg.approval != nil {
		approval = cfg.approval()
	}

	record := &setModeFixtureRecord{
		ClaudeVersionRaw: versionRaw,
		ClaudeVersion:    versionToken,

		Arm:            arm.name,
		LaunchYOLOFlag: arm.launchYOLO,
		Argv:           recordedArgv,
		Prompts:        prompts,
		Model:          cfg.model,
		ModeSwitchAuto: cfg.autoMode,
		Approval:       approval,

		RequestedMode:                   arm.targetMode,
		ControlRequestID:                requestID,
		ControlRequestSent:              controlSent,
		ControlResponses:                responses,
		ControlResponseRequestIDMatched: setModeResponseIDMatches(responses, requestID),
		SecondRequest:                   secondRequest,

		InitPermissionModes: rec.snapshotInitModes(),
		StdoutEvents:        lines,
		NonJSONLineCount:    rec.nonJSONCount(),
		TurnBoundaries:      boundaries,
		ProbeOutcomes:       setModeTurnWindows(lines, boundaries),

		StdinWriteErrors:       writeErrs,
		StderrCapture:          truncateString(recordedStderr, stderrFixtureCap),
		ExitCode:               exitCode,
		WaitError:              waitErrStr,
		ContextDeadlineTripped: errors.Is(ctx.Err(), context.DeadlineExceeded),
		DurationMs:             duration.Milliseconds(),
		ScannerError:           scannerErr,
	}

	if cfg.fillRecord != nil {
		cfg.fillRecord(t, record)
	}
	path := writeSetModeFixture(t, record, cfg.fixturePath, cfg.screenFixture)
	if path == "" {
		path = "<not written: the caller's screen refused these bytes>"
	}
	t.Logf("#1595[%s]: model %q, %d line(s), init modes %v, %d control_response(s), exit=%d, deadline_tripped=%v, %s",
		arm.name, cfg.model, len(lines), record.InitPermissionModes, len(responses), exitCode,
		record.ContextDeadlineTripped, duration.Round(time.Millisecond))
	for i, resp := range responses {
		t.Logf("#1595[%s]: control_response[%d]: %s", arm.name, i, resp)
	}
	for i, out := range record.ProbeOutcomes {
		t.Logf("#1595[%s]: turn %d: %s", arm.name, i+1, out)
	}
	t.Logf("#1595[%s]: fixture written: %s", arm.name, path)

	return record
}

// setModeResponseIDMatches reports whether any control_response correlates to
// requestID. claude may carry the id at the top level or inside `response`, so
// both are checked; a reply that carries neither is a recorded non-match, not an
// error.
func setModeResponseIDMatches(responses []json.RawMessage, requestID string) bool {
	if requestID == "" {
		return false
	}
	for _, raw := range responses {
		var env struct {
			RequestID string `json:"request_id"`
			Response  struct {
				RequestID string `json:"request_id"`
			} `json:"response"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.RequestID == requestID || env.Response.RequestID == requestID {
			return true
		}
	}
	return false
}

// --- the tests ---------------------------------------------------------------

// TestRealClaude_SetPermissionMode_InBandProbe drives four live children through
// the identical two-turn sequence — two of them carrying a `set_permission_mode`
// control request on the held-open stdin, two of them controls launched in the
// target posture from the start — and records, per arm, claude's verbatim
// control_response, the init.permissionMode echoes that followed, and the
// behavioural read of each turn.
//
// It PASSES on every recorded outcome. The verdict is logged, not asserted: a
// supported mechanism and an unsupported one are both findings, and the durable
// artifact is the fixture set plus
// docs/knowledge/features/set-permission-mode-inband-probe.md.
func TestRealClaude_SetPermissionMode_InBandProbe(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials

	workdir := filepath.Join(home, setModeWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#1595: create workdir: %v", err)
	}

	versionRaw, versionToken := captureClaudeVersion(t)
	t.Logf("#1595: claude version %q (token %q)", versionRaw, versionToken)

	// Sequential, no t.Parallel: at most one child and one reader goroutine exist
	// at a time, and the four arms share one pinned $HOME.
	// One config for all four arms: this measurement's arms differ only in the
	// launch flag and the requested mode, and the model is genuinely the same for
	// each. #2041 is the measurement that needs one per arm.
	cfg := setModeChildConfig{
		model:       setModeModel,
		promptOne:   setModePromptOne,
		promptTwo:   setModePromptTwo,
		maxTurns:    setModeMaxTurns,
		fixturePath: setModeFixturePath,
	}

	records := make(map[string]*setModeFixtureRecord, len(setModeArms))
	for _, arm := range setModeArms {
		t.Run(arm.name, func(t *testing.T) {
			records[arm.name] = runSetModeChild(t, claudeBin, workdir, arm, versionRaw, versionToken, cfg)
		})
	}

	var missing []string
	for _, arm := range setModeArms {
		if records[arm.name] == nil {
			missing = append(missing, arm.name)
		}
	}
	if len(missing) > 0 {
		t.Logf("#1595: cross-arm verdict UNAVAILABLE — arm(s) %v produced no record "+
			"(a -run filter, or an instrument failure above). Not computing a verdict "+
			"from a missing control.", missing)
		return
	}

	// Turn 2 only, on every arm: it is the measurement arms' post-change read, so
	// the controls must be read at the same turn index or the comparison folds in
	// a turn-index confound.
	turn2 := func(name string) probeOutcome { return setModeOutcomeAt(records[name].ProbeOutcomes, 1) }

	ctlDefault, ctlBypass := turn2("control_default"), turn2("control_bypass")
	t.Logf("#1595: control_default turn 2: %s", ctlDefault)
	t.Logf("#1595: control_bypass  turn 2: %s", ctlBypass)

	// The echoed control_response and the init echo are recorded alongside the
	// verdict, as separate rows. Neither enters it.
	for _, dir := range setModeDirections {
		r := records[dir.arm]
		t.Logf("#1595: %s: requested mode %q, %d control_response(s), request_id matched=%v",
			dir.arm, r.RequestedMode, len(r.ControlResponses), r.ControlResponseRequestIDMatched)
		if len(r.ControlResponses) == 0 {
			t.Logf("#1595: %s: NO control_response was observed", dir.arm)
		}
		for i, resp := range r.ControlResponses {
			t.Logf("#1595: %s: control_response[%d] verbatim: %s", dir.arm, i, resp)
		}
		if len(r.InitPermissionModes) == 0 {
			t.Logf("#1595: %s: NO system/init line was observed at all", dir.arm)
		} else {
			t.Logf("#1595: %s: init.permissionMode in arrival order: %v (last=%q, requested=%q)",
				dir.arm, r.InitPermissionModes,
				r.InitPermissionModes[len(r.InitPermissionModes)-1], r.RequestedMode)
		}
	}

	// AC 3's explicit branch, checked FIRST. Given #383 finding #2 this is a live
	// possibility rather than a defensive nicety, and where it fires neither a
	// revocation nor an escalation may be reported.
	if ctlDefault.equal(ctlBypass) {
		for _, dir := range setModeDirections {
			t.Logf("#1595: %s VERDICT: %s (control_default and control_bypass produced "+
				"identical turn-2 behaviour, so no behavioural read can separate the postures here)",
				dir.arm, setModeNoDiscrimination)
		}
		return
	}

	for _, dir := range setModeDirections {
		got := turn2(dir.arm)
		applied, failed := turn2(dir.appliedControl), turn2(dir.failedControl)
		var verdict string
		switch {
		case got.equal(applied):
			verdict = fmt.Sprintf("%s — turn 2 matches the %s control", dir.appliedVerdict, dir.appliedControl)
		case got.equal(failed):
			verdict = fmt.Sprintf("%s — turn 2 matches the %s control", dir.failedVerdict, dir.failedControl)
		default:
			verdict = fmt.Sprintf("INCONCLUSIVE — turn 2 matched neither the %s nor the %s control",
				dir.appliedControl, dir.failedControl)
		}
		t.Logf("#1595: %s VERDICT: %s\n  measured: %s", dir.arm, verdict, got)
		for _, row := range setModeFieldMatches(got, applied, failed, dir.appliedControl, dir.failedControl) {
			t.Logf("#1595: %s field: %s", dir.arm, row)
		}
	}
}

// setModeAdversarialVersionTokens is what `claude --version` might plausibly emit,
// plus the shapes that could smuggle a name into another glob or out of testdata/.
// versionSlug leaves `.` and `-` intact, so `..` survives slugging — which is why
// the containment assertions these feed are not redundant with the glob ones.
//
// It is a package-level var rather than a literal inside the test below because
// #2041's namer must clear the same three families over the SAME tokens, and a
// second copied literal is a list that drifts. A file adding a token here widens
// both checks at once; that is the point. Read-only — never append to it from a
// test, since several t.Parallel tests range it.
var setModeAdversarialVersionTokens = []string{
	"2.1.220",
	"2.1.220 (Claude Code)",
	"2_1_220",
	"2.1.220-beta.1",
	"permission_protocol",
	"..",
	"../..",
	"",
	strings.Repeat("9", 64),
}

// TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs asserts that
// every filename setModeFixturePath mints, for every arm and for adversarial
// version tokens, is matched by NEITHER fixtureGlob NOR dropcapFixtureGlob — and
// that it stays a plain component inside testdata/.
//
// This is the deterministic half of AC 4's "the existing regression test still
// passes unchanged". TestRealClaude_PermissionProtocol_RegressionFixtures globs
// testdata/permission_protocol_v*_*.json and parses the trailing token as the
// EXPECTED init.permissionMode, so a fixture named into that family would be
// swept in and asserted about a different argv. A name check settles that without
// a live run: that test's behaviour is a pure function of which filenames exist.
//
// No subprocess and no credentials — it passes on a machine with no claude at all.
func TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs(t *testing.T) {
	tokens := setModeAdversarialVersionTokens
	globs := []string{fixtureGlob, dropcapFixtureGlob}
	wantDir := filepath.Join(packageDir(t), "testdata")

	for _, token := range tokens {
		for _, arm := range setModeArms {
			path := setModeFixturePath(t, token, arm.name)
			base := filepath.Base(path)

			if dir := filepath.Dir(path); dir != wantDir {
				t.Errorf("token %q arm %q: fixture path %q resolves outside %q",
					token, arm.name, path, wantDir)
			}
			// The globs are relative to the package dir, so match against the
			// same relative shape they are evaluated with.
			rel := filepath.Join("testdata", base)
			for _, glob := range globs {
				matched, err := filepath.Match(glob, rel)
				if err != nil {
					t.Fatalf("filepath.Match(%q, %q): %v", glob, rel, err)
				}
				if matched {
					t.Errorf("token %q arm %q: fixture name %q matches glob %q; it would be "+
						"swept into a test that asserts findings about a different argv",
						token, arm.name, base, glob)
				}
			}
		}
	}

	// The arm token is what keeps the two directions independently recoverable
	// (AC 4). A collision would silently overwrite one direction with the other.
	seen := make(map[string]string, len(setModeArms))
	for _, arm := range setModeArms {
		name := setModeFixtureName("2.1.220", arm.name)
		if prev, dup := seen[name]; dup {
			t.Errorf("arms %q and %q both mint fixture name %q; one direction would "+
				"overwrite the other", prev, arm.name, name)
		}
		seen[name] = arm.name
	}
}
