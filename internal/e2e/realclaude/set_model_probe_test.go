//go:build e2e_realclaude

package realclaude

// #2279 — does claude 2.1.259 accept a `set_model` control request on the held-open
// stdin, and what does it answer?
//
// # Why a measurement and not a reading
//
// The daemon changes a live child's model by writing `/model <name>` onto stdin as
// an ordinary user turn, from Pool.deliverSettingsInBand. Replacing that with a
// control frame is what #2280 and #2281 are for, and both rest on one premise
// nothing here has ever tested: that the subtype exists on the wire at all.
// `git grep set_model` over this repo returned nothing before this file — no
// encoder, no fake-claude arm, no capture. The premise's only source was a published
// type definition, and this repo has already been burned by one of those: the same
// source promised a reasoning-effort field on system/init, and #2251 measured it
// absent from every init line ever captured here.
//
// A REFUSAL OF THE SUBTYPE IS A SUCCESSFUL OUTCOME for this file. What must not
// happen is a silent drop being mistaken for one, which is why every arm records the
// whole of what the child produced rather than only the lines it was looking for.
//
// # Five arms, one drive sequence
//
//	arm            | target token                | wire `model`
//	---------------+-----------------------------+-------------------------
//	accept         | a discovered model value    | that string
//	refuse         | setModelUnservable          | that string
//	reset_omitted  | setModelResetOmitted        | key ABSENT
//	reset_null     | setModelResetNull           | null
//	reset_default  | "default"                   | "default"
//
// Every arm launches pinned on setModelLaunchModel and drives two tool-free turns
// with the control request between them. The read is the ack PLUS turn 2's
// system/init `model`, because an ack alone cannot say whether anything changed and
// the init line reports a RESOLVED id rather than the alias sent. The capture keeps
// both strings rather than a boolean saying they matched — which of the two forms
// round-trips is one of the facts #2280 needs.
//
// Pinning the launch model is what makes the accept arm readable at all: a request
// naming the model already in use reports the same value whether it was applied or
// ignored. For the same reason the target is selected from the run's OWN model list
// and rejected when it merely ALIASES the launch model — `haiku` and
// `claude-haiku-4-5` resolve alike, and switching between them proves nothing.
//
// # What this rides, and what it deliberately does not rebuild
//
// runSetModeChild drives the child. It is not a supervisor tap: it builds its own
// argv, owns StdinPipe and StdoutPipe directly and splits lines with bufio.Scanner,
// which is the property the ticket's warning against inbandTapRecorder exists to
// enforce — a tap can only carry lines the production encoder knows how to write,
// and there is no set_model encoder. Riding it also supplies AC 4 outright:
// stdout_events, non_json_line_count, stderr_capture, exit_code, wait_error,
// stdin_write_errors, control_request_sent, control_request_id and
// control_response_request_id_matched are all already fields of setModeFixtureRecord.
// A copied rig would re-derive all nine plus a second route to packageDir — the one
// eleven finOfflineExecBans entries name writeSetModeFixture to fence off.
//
// The one thing the rig was missing is setModeChildConfig.controlLine, added here:
// the driver's own minter hardcodes subtype `set_permission_mode` and a `mode` field.
//
// # Reproduce
//
//	export CLAUDE_CODE_OAUTH_TOKEN=...
//	go test -tags e2e_realclaude -race -count=1 -timeout 30m -v \
//	  -run '^TestRealClaude_SetModelProbe$' ./internal/e2e/realclaude/
//
// Six children (five arms plus one turn-free discovery). The test PASSES on every
// recorded outcome; only an instrument that measured nothing fails.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	// The committed family. It shares no literal head with set_permission_mode_v,
	// permission_mode_switch_v, permission_protocol_v, dropped_lines_v,
	// initialize_control_v, bypass_*_v, compaction_v or effort_init_v;
	// TestSetModelFixtureName_StaysOutOfTheOtherTestdataGlobs asserts that rather
	// than leaving it a convention to remember. #1661 exists because fixture-name
	// families drift.
	setModelFamilyPrefix = "set_model"

	// A fresh EMPTY directory under the test's pinned $HOME, deliberately not a git
	// repo: no branch name and no file content can reach a payload, and claude loads
	// less project context so the turns are cheaper.
	setModelWorkdirName = "set-model-probe-work"

	// The pinned starting model, and #1595's. Switching AWAY from a pinned model is
	// what makes the accept arm's init-line read meaningful.
	setModelLaunchModel = "claude-haiku-4-5"

	// The refuse arm's target: shape-valid, and no published row carries it. The
	// live selector refuses to proceed if the run's own model list turns out to
	// publish it, since the arm would then measure an ACCEPTANCE under a name
	// claiming a refusal.
	setModelUnservable = "claude-no-such-model-2279"

	// Cost guard with ~2x headroom over the two assistant turns two tool-free probes
	// need. A result line carrying subtype:"error_max_turns" lands in the fixture
	// plainly — raise and rerun.
	setModelMaxTurns = "4"

	// The release this family is promotable at. A record from any other is refused
	// rather than written under a name claiming this one, so a claude upgrade is a
	// loud instruction to re-capture rather than a pin that quietly measures
	// something else. effortInitPromotable's rule.
	setModelFixtureVersion = "2.1.259"

	setModelArtifactPrefix = "pyry-set-model-"
	setModelRecordName     = "set_model_record.json"

	// Bounds on the two lists claude controls the length of. A line or a model list
	// that trips one is visible as a truncated value with its own flag rather than
	// as a silently unbounded record. effortInitKeyCap's shape and its reason.
	setModelKeyCap = 128
	setModelRowCap = 64

	// The two redaction classes this file adds beyond newDropcapRedactor's own
	// tempHome/artifactDir/workdir trio. See setModelBuildPass for why the binary
	// path is not optional.
	setModelClassClaudeBin    = "claude_binary_path"
	setModelClassOperatorHome = "operator_home_path"

	// FORCES a re-capture over an existing family. It is not the gate — see the
	// gate's own comment in TestRealClaude_SetModelProbe — and a run that never sets
	// it still captures whenever a capture is missing.
	setModelForceEnv = "PYRY_PROBE_SET_MODEL_RECAPTURE"
)

// The two target tokens that are not model names.
//
// setModeArm.targetMode is an opaque TARGET TOKEN whose wire meaning belongs to the
// minter, and the empty token already means "send no request at all" — which is what
// #1595's control arms rest on. Every arm here SENDS, so the two spellings that
// carry no model string still need a non-empty token, and these are it.
//
// The spellings carry `<`, `>`, `:` and a space, none of which modeSwitchModelValueOK
// admits. That is what makes a collision with a published model value structurally
// impossible rather than merely unlikely: a row spelled like one of these could never
// be selected as the accept arm's target in the first place.
// TestSetModelSentinels_CannotCollideWithAPublishedModelValue pins it, and
// setModelPickTarget rejects them by equality besides — different fabric, so neither
// check answers for the other.
const (
	setModelResetOmitted = "<reset: model key omitted>"
	setModelResetNull    = "<reset: model null>"
)

// The arm names, which are also the fixture tokens. Read-only: the deterministic
// tests range this and add their own hostile literals rather than appending here.
const (
	setModelArmAccept       = "accept"
	setModelArmRefuse       = "refuse"
	setModelArmResetOmitted = "reset_omitted"
	setModelArmResetNull    = "reset_null"
	setModelArmResetDefault = "reset_default"
)

var setModelArmNames = []string{
	setModelArmAccept,
	setModelArmRefuse,
	setModelArmResetOmitted,
	setModelArmResetNull,
	setModelArmResetDefault,
}

// The preference list the selector intersects with the live rows. Order is cost
// first. It is only an ordering hint — a value it names that the live list no longer
// publishes is skipped, and the fallback takes the first qualifying row in arrival
// order. #2041 observed the live list drifting from the committed capture at the same
// binary version, which is the whole argument for selecting out of the run's own list.
var setModelTargetPreference = []string{"sonnet", "default", "opus"}

// --- the wire shapes -----------------------------------------------------------

// setModelRequestInner carries the `model` key as raw JSON so ONE type expresses all
// three spellings the type definition describes.
//
// omitempty on a json.RawMessage omits a zero-length value, so nil mints no key at
// all, []byte("null") mints an explicit null, and a marshalled string mints a string.
// A plain `Model string` cannot express the middle one: `Model: "null"` mints the
// four-character STRING "null", which is a different request, and a request the
// reset measurement would silently record as its own answer.
type setModelRequestInner struct {
	Subtype string          `json:"subtype"` // "set_model"
	Model   json.RawMessage `json:"model,omitempty"`
}

type setModelRequest struct {
	Type      string               `json:"type"`       // "control_request"
	RequestID string               `json:"request_id"` // locally-minted correlation id
	Request   setModelRequestInner `json:"request"`
}

// setModelControlLine mints the single newline-terminated control line for one
// target token, in the shape setModeChildConfig.controlLine takes.
//
// Marshalled structured, never string-concatenated, so the appended '\n' stays the
// only raw newline in the envelope — marshalInterruptEnvelope's one-physical-line
// invariant, which the driver relies on because it writes the result unframed. A
// model value carrying a quote or a backslash therefore escapes rather than breaking
// the frame.
func setModelControlLine(requestID, target string) ([]byte, error) {
	inner := setModelRequestInner{Subtype: "set_model"}
	switch target {
	case setModelResetOmitted:
		// nil: omitempty drops the key entirely.
	case setModelResetNull:
		inner.Model = json.RawMessage("null")
	default:
		v, err := json.Marshal(target)
		if err != nil {
			return nil, fmt.Errorf("marshal set_model target %q: %w", target, err)
		}
		inner.Model = json.RawMessage(v)
	}
	b, err := json.Marshal(setModelRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   inner,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal set_model control request: %w", err)
	}
	return append(b, '\n'), nil
}

// setModelSpelling names, in words, what one target token puts on the wire. It is
// recorded per arm so a reader of the capture never has to re-derive the spelling
// from the marshalled request beside it.
func setModelSpelling(target string) string {
	switch target {
	case setModelResetOmitted:
		return "the model key is absent"
	case setModelResetNull:
		return "the model key is present and null"
	default:
		return "the model key is present and holds a string"
	}
}

// setModelKeySent reports whether the request carries a `model` key at all. It is
// the one distinction the recorded model STRING cannot draw: the omitted and the
// null spellings both send no string, and only this separates them.
func setModelKeySent(target string) bool { return target != setModelResetOmitted }

// setModelSentString is the string that reached the wire, empty for both spellings
// that sent none.
func setModelSentString(target string) string {
	if target == setModelResetOmitted || target == setModelResetNull {
		return ""
	}
	return target
}

// --- the arms ------------------------------------------------------------------

// setModelArms builds the five arms for one discovered target.
//
// Built inline rather than appended to setModeArms, for effortInitArm's reason:
// that var is #1595's live 2.1.220 measurement, and an arm added there would join
// that run AND mint into its committed family.
//
// launchYOLO stays false on every arm. The bypass flag would make this a different
// spawn than the daemon's ordinary one, and nothing here measures a permission
// posture.
func setModelArms(target string) []setModeArm {
	return []setModeArm{
		{name: setModelArmAccept, targetMode: target},
		{name: setModelArmRefuse, targetMode: setModelUnservable},
		{name: setModelArmResetOmitted, targetMode: setModelResetOmitted},
		{name: setModelArmResetNull, targetMode: setModelResetNull},
		// A real published value at 2.1.239, resolving to claude-sonnet-5. Whether
		// claude reads it as "reset" or as "switch to the default model" is exactly
		// the ambiguity the three reset arms exist to separate, and the init line
		// beside the ack is what settles it.
		{name: setModelArmResetDefault, targetMode: "default"},
	}
}

// The two probe prompts, deliberately TOOL-FREE and carrying the run nonce.
//
// Tool-free because the read here is the ack plus the next turn's init.model, so
// behaviour is not measured and a Bash probe would buy nothing but stall risk and
// unsandboxed tool access on a child launched in `default` posture —
// modeSwitchPromptOne's argument. The nonce gives dropcapRedactor's prompt_nonce
// class a value the record actually contains, and keeps turn 2 from being a question
// claude can answer with "I already did that": a short-circuited turn is not the full
// turn after the send point that the init read needs.
func setModelPromptOne(nonce int64) string {
	return fmt.Sprintf("Reply with exactly the word READY and nothing else. Do not use any tools, "+
		"do not comment on the task, and do nothing else. run=%d", nonce)
}

func setModelPromptTwo(nonce int64) string {
	return fmt.Sprintf("Reply with exactly the word DONE and nothing else. Do not use any tools, "+
		"do not comment on the task, and do nothing else. run=%d", nonce)
}

// --- selecting the accept arm's target -----------------------------------------

// setModelResolve returns the resolved id the live list publishes for value,
// falling back to value itself when the list publishes no row for it.
//
// The fallback is what keeps the launch model comparable when it is absent from the
// list, and it is deliberately not the empty string: an empty resolution on both
// sides of a comparison would make an alias look DIFFERENT from the model it aliases,
// which is the exact confound setModelPickTarget exists to prevent.
func setModelResolve(resolved map[string]string, value string) string {
	if r := resolved[value]; r != "" {
		return r
	}
	return value
}

// setModelPickTarget returns a published model value the accept arm can switch TO,
// or "" when no row qualifies — a FINDING about claude's model list rather than an
// instrument failure.
//
// Three rules, and the middle one is the measurement rather than hygiene:
//
//   - the value must be usable, per modeSwitchModelValueOK. Note that #2041's
//     rationale for that check does NOT carry over: its selected value reaches
//     `--model`, where a leading `-` parses as a flag, whereas nothing selected here
//     reaches an argv at all — the launch model is a constant and this value goes
//     into a JSON-marshalled request body. What it buys on this path is the LENGTH
//     bound, which keeps a pathological published value from inflating a committed
//     capture, and a charset narrow enough to keep a path- or credential-shaped value
//     off the wire.
//   - the value must RESOLVE somewhere other than the launch model. An alias is the
//     trap: `haiku` and `claude-haiku-4-5` resolve alike, so a run switching between
//     them reports the same init model whether the change applied or not, and would
//     record a no-op as a success.
//   - the value must not be one of the reset sentinels. They cannot pass the shape
//     check either, and that redundancy is deliberate: one is an equality test and
//     the other a property of the charset, so neither answers for the other.
func setModelPickTarget(rows []modeSwitchAutoRow, resolved map[string]string,
	launchModel string, prefer []string) string {
	launchResolved := setModelResolve(resolved, launchModel)
	usable := func(value string) bool {
		if value == "" || value == setModelResetOmitted || value == setModelResetNull {
			return false
		}
		if !modeSwitchModelValueOK(value) {
			return false
		}
		return setModelResolve(resolved, value) != launchResolved
	}
	for _, name := range prefer {
		for _, r := range rows {
			if r.Value == name && usable(r.Value) {
				return r.Value
			}
		}
	}
	for _, r := range rows {
		if usable(r.Value) {
			return r.Value
		}
	}
	return ""
}

// setModelPublishedRows reduces the live list to the value/resolution pairs the
// capture records, capped. The raw reply is never recorded — it carries `account`
// and `pid` beside `models`, which is why runModeSwitchDiscovery derives the mapping
// rather than handing the payload out.
func setModelPublishedRows(rows []modeSwitchAutoRow, resolved map[string]string) ([]setModelRow, bool) {
	out := make([]setModelRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, setModelRow{Value: r.Value, ResolvedModel: resolved[r.Value]})
	}
	if len(out) > setModelRowCap {
		return out[:setModelRowCap], true
	}
	return out, false
}

// --- reading the capture -------------------------------------------------------

// setModelRow is one published model reduced to the pair this measurement needs.
type setModelRow struct {
	Value         string `json:"value"`
	ResolvedModel string `json:"resolved_model"`
}

// setModelInitLine is one system/init line's answer: its FULL sorted key set plus
// the values with an operator consumer.
//
// ModelPresent is separate from Model, and that separation is the measurement. A
// line carrying `"model": ""` and a line carrying no model key at all are different
// findings, and a bare string cannot tell them apart — effortInitLine's argument, on
// the key this ticket is about.
type setModelInitLine struct {
	Index             int      `json:"index"`
	Keys              []string `json:"keys"`
	KeysTruncated     bool     `json:"keys_truncated"`
	ClaudeCodeVersion string   `json:"claude_code_version"`
	PermissionMode    string   `json:"permission_mode"`
	Model             string   `json:"model"`
	ModelPresent      bool     `json:"model_present"`
}

// setModelObservation is the set_model_capture block on setModeFixtureRecord. No
// omitempty anywhere: every false and every empty string here is a finding, and
// model_key_sent false is exactly the value an omitempty bool would spell by
// ABSENCE — the trap modeSwitchAutoObservation's fields exist to avoid.
type setModelObservation struct {
	Spelling     string `json:"spelling"`
	ModelSent    string `json:"model_sent"`
	ModelKeySent bool   `json:"model_key_sent"`

	LaunchModel         string `json:"launch_model"`
	LaunchModelResolved string `json:"launch_model_resolved"`
	TargetModel         string `json:"target_model"`
	TargetModelResolved string `json:"target_model_resolved"`

	PublishedModels          []setModelRow `json:"published_models"`
	PublishedModelsTruncated bool          `json:"published_models_truncated"`

	InitLines []setModelInitLine `json:"init_lines"`

	// CredentialScanApplied is the deny-scan's per-class report, so "the net ran" is
	// a recorded fact rather than an absence — a needle read from an unset
	// environment variable is skipped silently, not fatally.
	CredentialScanApplied map[string]bool `json:"credential_scan_applied"`
}

// setModelSummarise reads every system/init line in events and returns, per line,
// its full sorted key set and the values with an operator consumer.
//
// It decodes into map[string]json.RawMessage rather than into a struct this file
// guessed, for effortInitSummarise's reason: the key set drifts across claude
// releases, and a named-field decode reports "absent" for a spelling it merely did
// not anticipate. The keys ARE half the measurement, so they are read as data.
//
// A retained non-JSON line is a JSON string in the record and fails the decode; it is
// skipped rather than aborting the scan of the lines after it. The Index is into the
// recorded events, so a reader can find the line a summary row describes.
func setModelSummarise(events []json.RawMessage) []setModelInitLine {
	out := []setModelInitLine{}
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
		if len(keys) > setModelKeyCap {
			keys, truncated = keys[:setModelKeyCap], true
		}
		model, present := fields["model"]
		out = append(out, setModelInitLine{
			Index:             i,
			Keys:              keys,
			KeysTruncated:     truncated,
			ClaudeCodeVersion: effortInitString(fields["claude_code_version"]),
			PermissionMode:    effortInitString(fields["permissionMode"]),
			Model:             effortInitString(model),
			ModelPresent:      present,
		})
	}
	return out
}

// setModelPromotable answers whether a record may be written into the committed
// family, and returns the refusal in words when it may not.
//
// Two rules, both about a fixture that would MISLEAD rather than merely disappoint.
// A record from another claude release committed under this name would let #2280
// build on a wire shape nobody looked at. And fewer than two init lines cannot answer
// the before/after read at all, since the whole comparison is one line from before
// the request and one from after it.
//
// `claude --version` prints "<version> (Claude Code)", so the comparison is on the
// leading token; an "<unavailable: ...>" version fails it too, which is correct — a
// record that could not read the release it was taken at cannot vouch for one.
func setModelPromotable(rec *setModeFixtureRecord, lines []setModelInitLine) (string, bool) {
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != setModelFixtureVersion {
		return fmt.Sprintf("the record was taken at claude %q but this family is pinned at %q; bump "+
			"setModelFixtureVersion and re-run", got, setModelFixtureVersion), false
	}
	if len(lines) < 2 {
		return fmt.Sprintf("the record carries %d system/init line(s); the read needs at least two — "+
			"one from before the set_model request and one from the turn after it", len(lines)), false
	}
	return "", true
}

// --- the fixture family --------------------------------------------------------

// setModelFixtureName mints this family's name in effortInitFixtureName's shape: a
// literal prefix no input can reach, the version through versionSlug and the arm
// through modeSwitchNameToken, which maps every path metacharacter to `_` so no arm
// token can land a capture outside testdata/.
func setModelFixtureName(versionToken, arm string) string {
	return fmt.Sprintf("%s_v%s_%s.json",
		setModelFamilyPrefix, versionSlug(versionToken), modeSwitchNameToken(arm))
}

func setModelFixturePath(t *testing.T, versionToken, arm string) string {
	t.Helper()
	return filepath.Join(packageDir(t), "testdata", setModelFixtureName(versionToken, arm))
}

// setModelFixtureRel is one arm's capture relative to the package directory, which
// is the test binary's working directory. The absence GATE reads these, because it
// runs before any t.Helper has a testing.T's packageDir to mint from;
// TestSetModelFixtureName_StaysOutOfTheOtherTestdataGlobs binds the two spellings so
// the gate cannot arm against a file the writer never writes.
func setModelFixtureRel(arm string) string {
	return filepath.Join("testdata", setModelFixtureName(setModelFixtureVersion, arm))
}

// --- the redaction pass --------------------------------------------------------

// setModelPass carries the redactor and the deny-scan across the two hooks
// runSetModeChild calls: fill runs before the record is marshalled, screen runs on
// the marshalled bytes. Both fire from the test goroutine after the child has exited,
// so the redactor's unsynchronised counters need no lock.
type setModelPass struct {
	red         *dropcapRedactor
	scanner     *dropcapScanner
	artifactDir string
	arm         string

	launchResolved string
	target         string
	targetResolved string
	published      []setModelRow
	pubTruncated   bool

	// Decided in fill, where the record is legible, and read in screen, which sees
	// only bytes. A refusal blocks the IN-REPO write alone: the artifact copy is
	// written regardless, or a run that measured the wrong release would leave
	// nothing to diagnose it with.
	promotable bool
	refusal    string
}

// fill records the observation and extends the redaction table with the two values
// only claude could supply.
func (p *setModelPass) fill(t *testing.T, rec *setModeFixtureRecord) {
	t.Helper()

	for _, sid := range effortInitValues(rec.StdoutEvents, "session_id") {
		p.red.addValueClass(dropcapClassSessionID, "$SESSION_ID", sid)
	}
	for _, sock := range effortInitValues(rec.StdoutEvents, "messaging_socket_path") {
		p.red.addPathClass(dropcapClassMessagingSocket, "$MESSAGING_SOCKET", sock)
		// The same value the table removes is the value the net looks for, so the
		// clean scan in screen is a check on the table rather than an agreement
		// with it.
		p.scanner.addDynamicPath(dropcapClassMessagingSocket, sock)
	}

	lines := setModelSummarise(rec.StdoutEvents)
	rec.SetModelCapture = &setModelObservation{
		Spelling:                 setModelSpelling(rec.RequestedMode),
		ModelSent:                setModelSentString(rec.RequestedMode),
		ModelKeySent:             setModelKeySent(rec.RequestedMode),
		LaunchModel:              setModelLaunchModel,
		LaunchModelResolved:      p.launchResolved,
		TargetModel:              p.target,
		TargetModelResolved:      p.targetResolved,
		PublishedModels:          p.published,
		PublishedModelsTruncated: p.pubTruncated,
		InitLines:                lines,
		// Taken AFTER the socket needle above, so the report describes the net that
		// actually ran on these bytes.
		CredentialScanApplied: p.scanner.applied(),
	}
	p.refusal, p.promotable = setModelPromotable(rec, lines)

	for _, line := range lines {
		t.Logf("#2279[%s]: init line %d: model=%q present=%v claude_code_version=%q permissionMode=%q",
			p.arm, line.Index, line.Model, line.ModelPresent, line.ClaudeCodeVersion, line.PermissionMode)
	}
	t.Logf("#2279[%s]: spelling %q, model_sent=%q, model_key_sent=%v",
		p.arm, rec.SetModelCapture.Spelling, rec.SetModelCapture.ModelSent, rec.SetModelCapture.ModelKeySent)
}

// screen redacts the marshalled record, runs the deny-scan, and decides what lands.
//
// The order is the whole guard. Nothing is written until the scan passes: not the
// fixture, not the artifact copy. On a hit the failure names the CLASSES and never
// the value — putting it in CI output is exactly the exposure the scan exists to
// prevent.
func (p *setModelPass) screen(t *testing.T, data []byte) ([]byte, bool) {
	t.Helper()

	out := p.red.redact(data)
	hits, notApplied := p.scanner.scan(out)
	if len(hits) > 0 {
		t.Errorf("#2279[%s]: the deny-scan found %d denied class(es) still present in the record: %v\n"+
			"NOTHING was written — not the fixture, not the artifact copy. Extend the redaction table "+
			"with the named class and re-run the capture. The offending value is deliberately not "+
			"printed", p.arm, len(hits), hits)
		return nil, false
	}

	// Written from the SAME screened bytes, and outside the worktree: the
	// dispatcher's real-claude gate runs from a detached worktree it then removes,
	// which is how #2229's fixture was lost. A copy taken before the redaction would
	// leave an un-redacted record in a directory that outlives the run.
	artifact := filepath.Join(p.artifactDir, p.arm+"_"+setModelRecordName)
	if err := os.WriteFile(artifact, append(out, '\n'), 0o600); err != nil {
		t.Errorf("#2279[%s]: write the artifact copy: %v", p.arm, p.red.str(err.Error()))
	}
	t.Logf("#2279[%s]: substitutions %+v; deny classes not applied %v\n  artifact copy: %s",
		p.arm, p.red.substitutions(), notApplied, artifact)

	if !p.promotable {
		t.Errorf("#2279[%s]: the in-repo fixture %s was NOT written: %s\nThe artifact copy above holds "+
			"the record, so the run is diagnosable and can be promoted by hand once the reason is "+
			"resolved", p.arm, setModelFixtureRel(p.arm), p.refusal)
		return nil, false
	}
	return out, true
}

// --- the live capture ----------------------------------------------------------

// TestRealClaude_SetModelProbe drives five live children and writes the captures.
//
// Ordering is load-bearing in one place: newDropcapScanner reads
// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY through os.Getenv AS DENY NEEDLES,
// and WithWorktreeAuthenticated is what re-pins them into this process. Built first,
// the scanner takes an EMPTY needle, which scan reports as not-applied — silently
// skipped, never fatal. The credential net would be off while every message read
// green.
//
// NOT t.Parallel(): WithWorktreeAuthenticated reaches t.Setenv, and the five arms
// share one pinned $HOME.
//
// It PASSES on every recorded outcome. A refusal of the subtype is the finding this
// ticket was opened to record, an ack that changes no init model is a finding, and a
// control_response that never arrives is a finding — distinguishable from a silent
// drop because the record carries every stdout line the child produced. Only an
// instrument that measured nothing fails: zero stdout lines (runSetModeChild's own
// fatal), a deny-scan hit, and a record that cannot answer the before/after read.
func TestRealClaude_SetModelProbe(t *testing.T) {
	// THE GATE IS THE FAMILY'S ABSENCE, deliberately, and not a PYRY_PROBE_*
	// variable. `make e2e-realclaude` sets no custom variable, so an env-gated probe
	// skips on the ENV check BEFORE the credential check: the live gate passes
	// vacuously and no fixture ever lands. That is CLAUDE.md § Testing's #1763
	// failure exactly — a green gate and a spent budget look identical whether the
	// bytes landed or not. #2089 and #2229 both argue it in full.
	//
	// The whole family gates together. A partial family is the failure mode worth
	// preventing: five arms answer one question between them, and four captures plus
	// one absence is not a weaker answer but an unreadable one.
	force := os.Getenv(setModelForceEnv) == "1"
	if !force {
		var missing []string
		for _, arm := range setModelArmNames {
			if _, err := os.Stat(setModelFixtureRel(arm)); err != nil {
				missing = append(missing, arm)
			}
		}
		if len(missing) == 0 {
			t.Skipf("#2279: all %d captures of the %s_v%s family already exist, so there is nothing "+
				"to capture and this costs no claude turn.\nForce a re-capture (a new claude version, "+
				"or a suspected shape change) with:\n  %s=1 go test -tags e2e_realclaude -timeout 30m "+
				"-v \\\n    -run '^TestRealClaude_SetModelProbe$' ./internal/e2e/realclaude/",
				len(setModelArmNames), setModelFamilyPrefix, versionSlug(setModelFixtureVersion),
				setModelForceEnv)
		}
		t.Logf("#2279: capturing — %d of %d arms missing: %v", len(missing), len(setModelArmNames), missing)
	}

	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials; MUST precede the scanner
	versionRaw, versionToken := captureClaudeVersion(t)
	t.Logf("#2279: claude version %q (token %q), launching every arm pinned on --model %s",
		versionRaw, versionToken, setModelLaunchModel)

	workdir := filepath.Join(home, setModelWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2279: create workdir: %v", err)
	}

	// Deliberately NOT t.TempDir(): the operator needs the records after the test
	// ends, and they must outlive a verification worktree's removal.
	artifactDir, err := os.MkdirTemp("", setModelArtifactPrefix)
	if err != nil {
		t.Fatalf("#2279: create artifact dir: %v", err)
	}

	// One turn-free discovery child, before any arm spends a token. Selecting out of
	// the run's OWN list rather than out of a table is what makes an alias-switch
	// impossible rather than merely unlikely.
	rows, resolved := runModeSwitchDiscovery(t, claudeBin, workdir)
	launchResolved := setModelResolve(resolved, setModelLaunchModel)
	target := setModelPickTarget(rows, resolved, setModelLaunchModel, setModelTargetPreference)
	if target == "" {
		t.Logf("#2279: FINDING — claude's live model list publishes no usable value resolving "+
			"anywhere other than the launch model %q (resolved %q). No arm was driven: the accept "+
			"arm is defined against a model the account can serve AND that differs from the one in "+
			"use, and running it on an alias would record a no-op as a success.",
			setModelLaunchModel, launchResolved)
		return
	}
	if resolved[setModelUnservable] != "" {
		t.Logf("#2279: FINDING — claude's live model list PUBLISHES %q, the value the refuse arm "+
			"sends as unservable. No arm was driven: that arm would measure an acceptance and "+
			"record it under a name claiming a refusal. Pick a different setModelUnservable and "+
			"re-run.", setModelUnservable)
		return
	}
	targetResolved := setModelResolve(resolved, target)
	published, pubTruncated := setModelPublishedRows(rows, resolved)
	t.Logf("#2279: launch %q resolves to %q; accept arm targets %q resolving to %q",
		setModelLaunchModel, launchResolved, target, targetResolved)

	// A REAL nonce, never 0: newDropcapRedactor formats it with strconv.FormatInt, so
	// a zero installs "0" as a one-byte substitution rule and rewrites every zero
	// digit in the record. The prompts carry it, so the value the table substitutes
	// is one the record actually contains.
	nonce := time.Now().UnixNano()
	promptOne, promptTwo := setModelPromptOne(nonce), setModelPromptTwo(nonce)

	for _, arm := range setModelArms(target) {
		t.Run(arm.name, func(t *testing.T) {
			pass := setModelBuildPass(t, home, artifactDir, workdir, claudeBin, nonce, arm.name)
			pass.launchResolved = launchResolved
			pass.target = target
			pass.targetResolved = targetResolved
			pass.published = published
			pass.pubTruncated = pubTruncated

			cfg := setModeChildConfig{
				model:         setModelLaunchModel,
				promptOne:     promptOne,
				promptTwo:     promptTwo,
				maxTurns:      setModelMaxTurns,
				fixturePath:   setModelFixturePath,
				controlLine:   setModelControlLine,
				fillRecord:    pass.fill,
				screenFixture: pass.screen,
			}
			rec := runSetModeChild(t, claudeBin, workdir, arm, versionRaw, versionToken, cfg)

			for i, resp := range rec.ControlResponses {
				t.Logf("#2279[%s]: control_response[%d] verbatim: %s", arm.name, i, resp)
			}
			if len(rec.ControlResponses) == 0 {
				t.Logf("#2279[%s]: NO control_response was observed. Every stdout line the child "+
					"produced is in the capture, so a silent drop is distinguishable from a reply "+
					"in a shape this probe did not anticipate.", arm.name)
			}
			t.Logf("#2279[%s]: request_id matched=%v", arm.name, rec.ControlResponseRequestIDMatched)
			if pass.promotable {
				t.Logf("#2279[%s]: FIXTURE WRITTEN to %s — COMMIT IT. A pipeline worktree is "+
					"discarded when the run ends, so a fixture a test merely writes does not "+
					"survive:\n  git add %s", arm.name, setModelFixtureRel(arm.name),
					setModelFixtureRel(arm.name))
			}
		})
	}
}

// setModelBuildPass builds one arm's redaction table and deny-scan.
//
// THE CLAUDE BINARY PATH IS NOT AN OPTIONAL CLASS. dropcapDenyUsers is the fixed
// literal `/Users/`, resolveClaudeBin returns an absolute path that on this
// operator's machine reads `/Users/<name>/.local/bin/claude`, and runSetModeChild
// records the argv with that path at element zero. Ten committed captures in this
// directory already carry the string, from families that predate the deny-scan and
// never ran one. Without this class the screen would refuse EVERY arm, write
// nothing, and burn five live children to land no capture at all.
//
// It is substituted with a named placeholder rather than dropped, so the recorded
// argv still reads as an argv and stays diffable against those siblings. This is a
// redaction OF THE OPERATOR'S MACHINE, not of a credential: initControlScrubbed
// remains the credential guard and runs first, unconditionally, inside the driver.
func setModelBuildPass(t *testing.T, home, artifactDir, workdir, claudeBin string,
	nonce int64, arm string) *setModelPass {
	t.Helper()

	red := newDropcapRedactor(home, artifactDir, workdir, "", "", nonce)
	red.addPathClass(setModelClassClaudeBin, "$CLAUDE_BIN", claudeBin)
	red.addPathClass(setModelClassOperatorHome, "$OPERATOR_HOME", realHome)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	return &setModelPass{red: red, scanner: &scanner, artifactDir: artifactDir, arm: arm}
}

// --- offline self-checks -------------------------------------------------------
//
// Everything below runs with no claude, no credentials and no subprocess:
//
//	go test -tags e2e_realclaude -race -count=1 -run TestSetModel ./internal/e2e/realclaude/

// TestSetModelControlLine_MintsTheThreeResetSpellings is the assertion the whole
// third acceptance criterion rests on.
//
// The omitted and the null spellings are the pair that must not collapse. A `Model
// string` field with omitempty cannot express the second — `Model: "null"` mints the
// four-character STRING "null", a different request — and the reset measurement
// would then record one spelling's answer under the other's name with nothing red.
// So the assertions are on the marshalled BYTES rather than on a decoded struct: a
// decode into a target this file wrote would agree with whatever this file got wrong.
func TestSetModelControlLine_MintsTheThreeResetSpellings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "the model key is absent",
			target: setModelResetOmitted,
			want:   `{"type":"control_request","request_id":"r","request":{"subtype":"set_model"}}`,
		},
		{
			name:   "the model key is an explicit null",
			target: setModelResetNull,
			want:   `{"type":"control_request","request_id":"r","request":{"subtype":"set_model","model":null}}`,
		},
		{
			name:   "the model key is the string default",
			target: "default",
			want:   `{"type":"control_request","request_id":"r","request":{"subtype":"set_model","model":"default"}}`,
		},
		{
			name:   "the model key is an ordinary model value",
			target: "claude-fable-5[1m]",
			want: `{"type":"control_request","request_id":"r","request":{"subtype":"set_model",` +
				`"model":"claude-fable-5[1m]"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := setModelControlLine("r", tc.target)
			if err != nil {
				t.Fatalf("setModelControlLine(%q): %v", tc.target, err)
			}
			if want := tc.want + "\n"; string(got) != want {
				t.Errorf("setModelControlLine(%q) =\n  %s\nwant\n  %s", tc.target, got, want)
			}
		})
	}

	// The one-physical-line invariant, on the value most likely to break it. The
	// driver writes the result unframed, so an unescaped newline inside the model
	// value would split one request into two lines and claude would read the tail as
	// a second, malformed frame.
	hostile, err := setModelControlLine("r", "a\"b\\c\nd")
	if err != nil {
		t.Fatalf("setModelControlLine on a hostile value: %v", err)
	}
	if n := strings.Count(string(hostile), "\n"); n != 1 {
		t.Errorf("the minted line carries %d newlines, want exactly the trailing one: %q", n, hostile)
	}
	var round setModelRequest
	if err := json.Unmarshal(hostile[:len(hostile)-1], &round); err != nil {
		t.Fatalf("the minted line does not round-trip: %v", err)
	}
	if string(round.Request.Model) != `"a\"b\\c\nd"` {
		t.Errorf("the model value did not escape: %s", round.Request.Model)
	}
}

// TestSetModelSpellingHelpers_SeparateTheTwoValuelessSpellings pins the one
// distinction the recorded model STRING cannot draw. Both reset spellings send no
// string, so model_sent is empty for each; only model_key_sent tells them apart, and
// a capture that lost it could not say which of the two it measured.
func TestSetModelSpellingHelpers_SeparateTheTwoValuelessSpellings(t *testing.T) {
	t.Parallel()

	if setModelSentString(setModelResetOmitted) != "" || setModelSentString(setModelResetNull) != "" {
		t.Errorf("a reset sentinel leaked into model_sent: omitted=%q null=%q",
			setModelSentString(setModelResetOmitted), setModelSentString(setModelResetNull))
	}
	if setModelKeySent(setModelResetOmitted) {
		t.Errorf("the omitted spelling reports a model key was sent")
	}
	if !setModelKeySent(setModelResetNull) {
		t.Errorf("the null spelling reports NO model key was sent; it sends one, holding null")
	}
	if setModelSpelling(setModelResetOmitted) == setModelSpelling(setModelResetNull) {
		t.Errorf("both valueless spellings render as %q; a reader cannot tell the arms apart",
			setModelSpelling(setModelResetOmitted))
	}
	if got := setModelSentString("sonnet"); got != "sonnet" {
		t.Errorf("model_sent for an ordinary target = %q, want the value itself", got)
	}
}

// TestSetModelSentinels_CannotCollideWithAPublishedModelValue is the suspenders to
// setModelPickTarget's belt, and deliberately different fabric: that one is an
// equality test the selector performs, this is a property of the charset. A sentinel
// that could pass modeSwitchModelValueOK would be selectable as the accept arm's
// target, and the minter would then read a real model as a reset instruction.
func TestSetModelSentinels_CannotCollideWithAPublishedModelValue(t *testing.T) {
	t.Parallel()

	for _, sentinel := range []string{setModelResetOmitted, setModelResetNull} {
		if modeSwitchModelValueOK(sentinel) {
			t.Errorf("the sentinel %q passes modeSwitchModelValueOK, so a published row spelled "+
				"like it would be selectable and the minter would read a real model as a reset",
				sentinel)
		}
	}
	if setModelResetOmitted == setModelResetNull {
		t.Errorf("both sentinels are %q; the two spellings would be one arm", setModelResetOmitted)
	}
	// The refuse arm's value must stay a plausible model name — it goes on the wire
	// as one — while being none of the above.
	if !modeSwitchModelValueOK(setModelUnservable) {
		t.Errorf("setModelUnservable %q is not a shape-valid model value; claude would refuse it "+
			"for its shape rather than for being unservable, which is a different finding",
			setModelUnservable)
	}
}

// TestSetModelPickTarget_RefusesAnAliasOfTheLaunchModel pins the selection rule the
// accept arm's readability rests on.
//
// The alias row is the case that matters: `haiku` and `claude-haiku-4-5` resolve
// alike, so a run that switched between them would report the same init model
// whether the change applied or not, and would record a no-op as a success.
func TestSetModelPickTarget_RefusesAnAliasOfTheLaunchModel(t *testing.T) {
	t.Parallel()

	rows := []modeSwitchAutoRow{
		{Value: "haiku"},
		{Value: "claude-haiku-4-5"},
		{Value: "sonnet"},
		{Value: "opus"},
	}
	resolved := map[string]string{
		"haiku":            "claude-haiku-4-5",
		"claude-haiku-4-5": "claude-haiku-4-5",
		"sonnet":           "claude-sonnet-5",
		"opus":             "claude-opus-5",
	}

	cases := []struct {
		name     string
		rows     []modeSwitchAutoRow
		resolved map[string]string
		prefer   []string
		want     string
	}{
		{"prefers the named value", rows, resolved, []string{"sonnet", "opus"}, "sonnet"},
		{"falls back to arrival order", rows, resolved, nil, "sonnet"},
		{"skips a preference the list no longer publishes", rows, resolved,
			[]string{"claude-fable-5-1[1m]", "opus"}, "opus"},
		{"refuses an alias of the launch model", []modeSwitchAutoRow{{Value: "haiku"}},
			resolved, nil, ""},
		{"refuses the launch model itself", []modeSwitchAutoRow{{Value: "claude-haiku-4-5"}},
			resolved, nil, ""},
		{"refuses a value that is not argv-shaped", []modeSwitchAutoRow{{Value: "--dangerously-skip-permissions"}},
			map[string]string{"--dangerously-skip-permissions": "x"}, nil, ""},
		{"refuses a reset sentinel", []modeSwitchAutoRow{{Value: setModelResetNull}},
			map[string]string{setModelResetNull: "claude-sonnet-5"}, nil, ""},
		{"an unresolved row is compared on its own spelling", []modeSwitchAutoRow{{Value: "sonnet"}},
			map[string]string{}, nil, "sonnet"},
		{"an unresolved row equal to the launch model is refused",
			[]modeSwitchAutoRow{{Value: setModelLaunchModel}}, map[string]string{}, nil, ""},
		{"an empty list", nil, resolved, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := setModelPickTarget(tc.rows, tc.resolved, setModelLaunchModel, tc.prefer)
			if got != tc.want {
				t.Errorf("setModelPickTarget = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSetModelSummarise_SeparatesAnAbsentModelKeyFromAnEmptyOne pins the summariser
// over hand-built lines.
//
// The absent case must not read as an empty VALUE: a line carrying `"model": ""` and
// a line carrying no such key are two different findings about whether claude
// publishes the field at all. The non-init `system` line is a decoy on purpose — a
// summariser keying on `type` alone would swallow it and report a model this run
// never saw.
func TestSetModelSummarise_SeparatesAnAbsentModelKeyFromAnEmptyOne(t *testing.T) {
	t.Parallel()

	events := []json.RawMessage{
		json.RawMessage(`{"type":"assistant","message":{"content":[]}}`),
		json.RawMessage(`{"type":"system","subtype":"init","claude_code_version":"2.1.259",` +
			`"permissionMode":"default","model":"claude-haiku-4-5","cwd":"/w"}`),
		json.RawMessage(`{"type":"system","subtype":"thinking_tokens","model":"trap"}`),
		json.RawMessage(`"a retained non-JSON line"`),
		json.RawMessage(`{"type":"system","subtype":"init","model":""}`),
		json.RawMessage(`{"type":"system","subtype":"init","permissionMode":"default"}`),
	}

	got := setModelSummarise(events)
	if len(got) != 3 {
		t.Fatalf("summarised %d init line(s), want 3: %+v", len(got), got)
	}

	if want := []string{"claude_code_version", "cwd", "model", "permissionMode", "subtype", "type"}; !slices.Equal(got[0].Keys, want) {
		t.Errorf("line 0 keys = %q, want %q (sorted, every key, nothing dropped)", got[0].Keys, want)
	}
	if got[0].Index != 1 {
		t.Errorf("line 0 index = %d, want 1 — the index is into the RECORDED events, so a reader "+
			"can find the line the summary describes", got[0].Index)
	}
	if !got[0].ModelPresent || got[0].Model != "claude-haiku-4-5" {
		t.Errorf("line 0 = %+v, want model claude-haiku-4-5 present", got[0])
	}
	if got[0].ClaudeCodeVersion != "2.1.259" || got[0].PermissionMode != "default" {
		t.Errorf("line 0 = %+v, want claude_code_version 2.1.259 and permissionMode default", got[0])
	}

	if !got[1].ModelPresent || got[1].Model != "" {
		t.Errorf("line 1 = %+v, want a model key PRESENT with an empty value", got[1])
	}
	if got[2].ModelPresent || got[2].Model != "" {
		t.Errorf("line 2 = %+v, want NO model key at all — a present-but-empty key and an absent "+
			"key are different findings", got[2])
	}
}

// TestSetModelFixtureName_StaysOutOfTheOtherTestdataGlobs keeps this family's names
// from being swept up by a sibling reader's glob, and keeps every name a plain
// component inside testdata/.
//
// The adversarial tokens are the shared list rather than a second copied literal:
// versionSlug leaves `.` and `-` intact, so `..` survives slugging, which is why the
// containment assertion is not redundant with the glob one.
func TestSetModelFixtureName_StaysOutOfTheOtherTestdataGlobs(t *testing.T) {
	t.Parallel()

	globs := []string{
		fixtureGlob, dropcapFixtureGlob,
		"testdata/set_permission_mode_v*.json",
		"testdata/permission_mode_switch_v*.json",
		"testdata/bypass_approval_argv_v*.json",
		"testdata/bypass_reescalation_v*.json",
		"testdata/initialize_control_v*.json",
		"testdata/compaction_v*.json",
		"testdata/effort_init_v*.json",
	}
	wantDir := filepath.Join(packageDir(t), "testdata")

	for _, token := range setModeAdversarialVersionTokens {
		for _, arm := range setModelArmNames {
			path := setModelFixturePath(t, token, arm)
			base := filepath.Base(path)
			if dir := filepath.Dir(path); dir != wantDir {
				t.Errorf("token %q arm %q: fixture path %q resolves outside %q", token, arm, path, wantDir)
			}
			rel := filepath.Join("testdata", base)
			for _, glob := range globs {
				matched, err := filepath.Match(glob, rel)
				if err != nil {
					t.Fatalf("filepath.Match(%q, %q): %v", glob, rel, err)
				}
				if matched {
					t.Errorf("token %q arm %q: fixture name %q matches glob %q; it would be swept "+
						"into a test that asserts findings about a different measurement",
						token, arm, base, glob)
				}
			}
		}
	}

	// The arm token is what keeps the five spellings independently recoverable. A
	// collision would silently overwrite one arm's answer with another's.
	seen := make(map[string]string, len(setModelArmNames))
	for _, arm := range setModelArmNames {
		name := setModelFixtureName(setModelFixtureVersion, arm)
		if prev, dup := seen[name]; dup {
			t.Errorf("arms %q and %q both mint %q; one answer would overwrite the other", prev, arm, name)
		}
		seen[name] = arm
		// The absence GATE and the WRITER must agree on one path, or the capture
		// arms against a file it never writes and re-runs forever.
		if want := filepath.Join("testdata", name); setModelFixtureRel(arm) != want {
			t.Errorf("setModelFixtureRel(%q) = %q but the minter produces %q", arm,
				setModelFixtureRel(arm), want)
		}
	}
}

// TestSetModelPromotable_RefusesWhatCannotAnswerTheQuestion pins the in-repo
// promotion rule. A record that measured another claude release, or that carries
// fewer than the two init lines the before/after read needs, must not land under a
// name claiming otherwise — a claude upgrade is a loud instruction to re-capture
// rather than a quietly wrong premise for #2280.
func TestSetModelPromotable_RefusesWhatCannotAnswerTheQuestion(t *testing.T) {
	t.Parallel()

	two := []setModelInitLine{{Index: 1}, {Index: 9}}
	cases := []struct {
		name    string
		version string
		lines   []setModelInitLine
		want    bool
	}{
		{"the pinned version with two init lines", setModelFixtureVersion + " (Claude Code)", two, true},
		{"a bare version string", setModelFixtureVersion, two, true},
		{"another release", "2.1.260 (Claude Code)", two, false},
		{"an unreadable version", "<unavailable: exec failed>", two, false},
		{"one init line", setModelFixtureVersion, two[:1], false},
		{"no init line at all", setModelFixtureVersion, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &setModeFixtureRecord{ClaudeVersion: tc.version}
			reason, ok := setModelPromotable(rec, tc.lines)
			if ok != tc.want {
				t.Errorf("setModelPromotable = (%q, %v), want ok=%v", reason, ok, tc.want)
			}
			if !ok && reason == "" {
				t.Errorf("a refusal with no reason leaves an operator nothing to act on")
			}
		})
	}
}

// TestSetModelRedaction_RemovesTheClaudeBinaryPathTheScannerWouldCatch is the
// regression guard for the finding that would otherwise have cost a whole live run.
//
// dropcapDenyUsers is the fixed literal `/Users/`, resolveClaudeBin returns a path
// under it, and runSetModeChild records that path at argv[0]. Without the class this
// test pins, the screen refuses every arm and lands nothing.
//
// BOTH directions are asserted on the same bytes, and one alone is vacuous: the
// un-redacted scan is what proves the needle fires, so a table that stopped
// substituting reddens here rather than quietly re-arming the refusal.
//
// It goes through setModelBuildPass rather than assembling its own redactor, and
// that is the point rather than convenience: the class registration IS the subject,
// and a test that built its own table would stay green with the line deleted from the
// one table the live run actually uses. setModelBuildPass reaches no exec and no
// skip, so it settles here with no claude and no credentials.
func TestSetModelRedaction_RemovesTheClaudeBinaryPathTheScannerWouldCatch(t *testing.T) {
	// NOT t.Parallel(): newDropcapScanner inside setModelBuildPass reads the two
	// credential variables, and a parallel sibling reaching t.Setenv would race it.
	const claudeBin = "/Users/someone/.local/bin/claude"
	fakeHome := t.TempDir()
	workdir := filepath.Join(fakeHome, "work")
	record := fmt.Sprintf(`{"argv":[%q,"--model","claude-haiku-4-5"],"cwd":%q}`, claudeBin, workdir)

	pass := setModelBuildPass(t, fakeHome, "", workdir, claudeBin, time.Now().UnixNano(), "redaction")

	if hits, _ := pass.scanner.scan([]byte(record)); !dropcapContains(hits, dropcapDenyUsers) {
		t.Fatalf("the scanner does not see %q in the UN-redacted record (hits %v); it cannot then "+
			"vouch for its absence from the redacted one", claudeBin, hits)
	}

	out := string(pass.red.redact([]byte(record)))
	if strings.Contains(out, claudeBin) {
		t.Errorf("the redacted record still carries the claude binary path, so the screen would "+
			"refuse every arm and the run would land nothing")
	}
	if !strings.Contains(out, "$CLAUDE_BIN") {
		t.Errorf("no $CLAUDE_BIN placeholder in the redacted record, so the argv stops reading as "+
			"an argv: %s", out)
	}
	if hits, _ := pass.scanner.scan([]byte(out)); len(hits) > 0 {
		t.Errorf("the redacted record still trips the deny-scan: %v", hits)
	}

	var sawClass bool
	for _, s := range pass.red.substitutions() {
		if s.Class == setModelClassClaudeBin && s.Count > 0 {
			sawClass = true
		}
	}
	if !sawClass {
		t.Errorf("the redaction table does not report the %s class as applied; a substitution the "+
			"record does not declare is one a reader cannot check", setModelClassClaudeBin)
	}
}

// TestSetModelArms_CoverEveryArmNameAndSpellingExactlyOnce binds the three tables
// that have to agree: the arm-name list the absence gate walks, the arms the live
// test drives, and the three wire spellings the third acceptance criterion asks for.
//
// They are separate lists because they serve different callers, and a spelling
// dropped from one while the others stayed whole is the drift worth catching: the
// gate would stop demanding a capture the run still writes, or demand one it never
// does.
func TestSetModelArms_CoverEveryArmNameAndSpellingExactlyOnce(t *testing.T) {
	t.Parallel()

	arms := setModelArms("sonnet")
	if len(arms) != len(setModelArmNames) {
		t.Fatalf("setModelArms builds %d arm(s) but setModelArmNames lists %d; the absence gate and "+
			"the run would disagree on what a complete family is", len(arms), len(setModelArmNames))
	}
	seenName := map[string]bool{}
	seenSpelling := map[string]int{}
	for i, arm := range arms {
		if arm.name != setModelArmNames[i] {
			t.Errorf("arm %d is %q but setModelArmNames has %q at that position", i, arm.name,
				setModelArmNames[i])
		}
		if seenName[arm.name] {
			t.Errorf("arm %q appears twice; one capture would overwrite the other", arm.name)
		}
		seenName[arm.name] = true
		if arm.targetMode == "" {
			t.Errorf("arm %q carries an EMPTY target token, which runSetModeChild reads as "+
				"'send no request at all'; every arm here sends one", arm.name)
		}
		if arm.launchYOLO {
			t.Errorf("arm %q sets launchYOLO; nothing here measures a permission posture and the "+
				"bypass flag would make this a different spawn than the daemon's ordinary one",
				arm.name)
		}
		seenSpelling[setModelSpelling(arm.targetMode)]++
	}
	if len(seenSpelling) != 3 {
		t.Errorf("the five arms cover %d wire spelling(s), want 3: %v", len(seenSpelling), seenSpelling)
	}
	for _, want := range []string{setModelResetOmitted, setModelResetNull} {
		var found bool
		for _, arm := range arms {
			if arm.targetMode == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no arm sends the %q spelling", setModelSpelling(want))
		}
	}
}
