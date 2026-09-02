//go:build e2e_realclaude

package realclaude

// #2041 — do `acceptEdits`, `dontAsk`, `plan` and `auto` switch a running child
// in-band at claude 2.1.239?
//
// # The finding, measured 2026-09-02 against claude 2.1.239
//
// ALL FOUR SWITCH. Each was accepted on a running child over the held-open stdin
// the daemon already writes interrupts to, each was acknowledged with
// `subtype:"success"` echoing the requested mode, and each was confirmed by the
// NEXT turn's system/init line reporting the new mode where the first had
// reported `default`. #1687's operator claim, taken against 2.1.220, reproduces
// at 2.1.239 across nineteen releases of drift.
//
//	mode          | model            | control_response      | init.permissionMode
//	--------------+------------------+-----------------------+---------------------
//	acceptEdits   | sonnet           | success / acceptEdits | [default acceptEdits]
//	dontAsk       | sonnet           | success / dontAsk     | [default dontAsk]
//	plan          | sonnet           | success / plan        | [default plan]
//	auto          | sonnet           | success / auto        | [default auto]
//	auto          | claude-haiku-4-5 | ERROR (below)         | [default default]
//
// `sonnet` is what the run's OWN model list selected as the first auto-capable
// model; `claude-haiku-4-5` as the first auto-INcapable one. Neither is a
// constant here — see runModeSwitchDiscovery.
//
//   - THE `auto` REFUSAL, VERBATIM, on a model publishing supportsAutoMode false:
//     `{"type":"control_response","response":{"subtype":"error",
//     "request_id":"set-permission-mode-auto_unsupported",
//     "error":"Cannot set permission mode to auto: auto mode unavailable for this
//     model"}}`
//     It is refused per MODEL, not per mechanism: the identical request on
//     `sonnet` succeeded. The refusal is correlated by request_id, arrives as an
//     ordinary control_response rather than a stream error, and leaves the
//     session's mode untouched — init stayed `default` across both turns. That is
//     what #1687 can tell a client, and it is why #1819's `supportsAutoMode` is
//     worth publishing: a client that greys the option out in advance never has to
//     surface this string.
//
//   - No mode was refused as UNKNOWN. In particular `dontAsk`, which appears in no
//     other capture in this repo, is a mode claude 2.1.239 accepts by name.
//
//   - The `set_permission_mode is not supported in this context
//     (onSetPermissionMode callback not registered)` string #1595 read out of the
//     binary did not appear on any arm, matching #1595's own result.
//
// What that means for #1687: all four are real wire vocabulary at this version,
// so the ticket stays "carry a string where a boolean sits" rather than becoming
// new machinery. `auto` is the one row needing a per-model gate, and the daemon
// already has the input for it.
//
// # Two things a later reader should not misread
//
//   - THE `plan` ARM'S TURN 2 ENDED IN `error_max_turns` (exit 1, `--max-turns 4`).
//     Switched into plan mode, claude answered a deliberately tool-free prompt by
//     reaching for tools — [Write ToolSearch ToolSearch ToolSearch]. That is
//     RECORDED, not repaired, and it does not touch the read: the success ack and
//     BOTH init lines had already landed when the bound tripped, and the second
//     reported `plan`. If a later run needs a clean turn 2 on that arm it wants a
//     higher modeSwitchMaxTurns, not a different conclusion.
//
//   - THE LIVE MODEL LIST HAS DRIFTED FROM THE COMMITTED 2.1.239 CAPTURE. This run
//     observed `claude-fable-5-1[1m]` where testdata/initialize_control_v2.1.239.json
//     records `claude-fable-5[1m]`. Same binary version, different published value
//     — which is the whole argument for selecting a model out of the run's own list
//     rather than out of a table. modeSwitchAutoCapablePreference is only an
//     ordering hint; a value it names that no longer exists is skipped, and the
//     fallback takes the first qualifying row in arrival order.
//
// # Why the arms are shaped the way they are
//
// #1595 needed behavioural control arms because "the tool still ran" is not
// evidence either way about a BYPASS change. This read is narrower and direct —
// the ack plus the next turn's init.permissionMode — so there are no control arms
// and the probe turns are tool-free. See modeSwitchPromptOne.
//
// The fifth arm differs from the fourth in ONE dimension, the model. That is what
// makes the refusal attributable to the model rather than to the mode, and it is
// why the model is a property of an arm rather than a package constant: reusing
// runSetModeChild's own `claude-haiku-4-5` for every arm would have measured
// `auto`'s refusal on a model that never supported auto and recorded it as "auto
// no longer switches in-band" — false, and false in the exact direction #1687
// would have acted on.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run TestRealClaude_InBandModeSwitch_Probe ./internal/e2e/realclaude/
//
// ~67 s, six children (five arms plus one turn-free discovery). The test PASSES on
// every recorded outcome — a mode claude refuses is a finding — and fails only
// where the instrument measured nothing. It adds no production writer and changes
// no production behaviour, the same boundary #1595 held.

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
	"strings"
	"testing"
	"time"
)

// --- the arms -------------------------------------------------------------------

const (
	// The committed family these captures form. It shares no literal head with
	// permission_protocol_v (fixtureGlob), dropped_lines_v (dropcapFixtureGlob)
	// or set_permission_mode_v (setModeFamilyGlob), which is what keeps it out of
	// all three sweeps; TestModeSwitch_FixtureNamesAvoidRegressionGlobs asserts
	// that rather than leaving it a convention to remember.
	modeSwitchFamilyPrefix = "permission_mode_switch"

	// A fresh EMPTY directory under the test's pinned $HOME, deliberately not a
	// git repo: less project context for claude to load, so the turns are cheaper.
	modeSwitchWorkdirName = "permission-mode-switch-work"

	// The two probe prompts, shared by all five arms and deliberately TOOL-FREE,
	// which is where they diverge from setModePromptOne / setModePromptTwo.
	//
	// initControlPromptOne's doc block is the reason and it applies verbatim here:
	// a tool-free turn cannot stall on a permission prompt it can never receive,
	// and every arm below launches in `default` posture with no
	// --dangerously-skip-permissions. THE READ HERE IS THE ACK PLUS THE NEXT
	// TURN'S init.permissionMode, so behaviour is not measured and a Bash probe
	// would buy nothing but stall risk and unsandboxed tool access.
	//
	// They differ from each other so turn 2 is a fresh request rather than one
	// claude can answer with "I already did that" — a short-circuited turn is not
	// the full turn after the send point that the init read needs.
	modeSwitchPromptOne = "Reply with the single word: ready. Do not use any tools."
	modeSwitchPromptTwo = "Reply with the single word: done. Do not use any tools."

	// Cost guard with ~2x headroom over the two assistant turns two tool-free
	// probes need. A result line carrying subtype:"error_max_turns" lands in the
	// fixture plainly — raise and rerun.
	modeSwitchMaxTurns = "4"

	// The discovery child answers no turn at all, so one is enough to satisfy the
	// flag and the child spends no assistant tokens.
	modeSwitchDiscoveryMaxTurns = "1"

	// The model list claude reports is a property of the BINARY and the account,
	// not of the model answering a turn — initControlModel's doc block states
	// this — so the discovery child runs on the cheapest model regardless of what
	// it ends up selecting for the arms.
	modeSwitchDiscoveryModel = "claude-haiku-4-5"

	// A correlation token, not a security token, for the same reason
	// initControlRequestID is a fixed literal: it correlates a reply on a pipe
	// this process owns, and a random one would rewrite the committed captures on
	// every run.
	modeSwitchDiscoveryRequestID = "mode-switch-model-list-1"

	// An argv value longer than this is not a model name. The bound is a shape
	// constraint, not a claude fact: the longest value 2.1.239 publishes is 18
	// bytes.
	modeSwitchModelValueMax = 64
)

// Hard kill for the discovery child. It drives no turn, so the only bounded wait
// inside it is setModeControlBudget (45s) and this dominates it — which is what
// makes a tripped deadline mean a genuinely stalling claude rather than an
// artefact of the harness.
const modeSwitchDiscoveryBudget = 2 * time.Minute

const (
	// The two arms whose names are not their requested mode. auto_unsupported
	// requests `auto` like the arm above it and differs from it in ONE dimension,
	// the model — which is the whole point of the pair and the reason the model
	// is an arm property rather than a package constant.
	modeSwitchArmAuto            = "auto"
	modeSwitchArmAutoUnsupported = "auto_unsupported"
)

// modeSwitchArmNames is every arm this probe drives, and every arm token
// modeSwitchFixtureName can be asked for by the live test. Read-only: the
// deterministic tests range it and add their own hostile literals rather than
// appending here.
var modeSwitchArmNames = []string{
	"acceptEdits",
	"dontAsk",
	"plan",
	modeSwitchArmAuto,
	modeSwitchArmAutoUnsupported,
}

// modeSwitchTargetMode is the mode an arm asks claude to switch to. It is the arm
// name for every arm but auto_unsupported, whose name has to differ from `auto`
// so their two captures land in separate files.
func modeSwitchTargetMode(arm string) string {
	if arm == modeSwitchArmAutoUnsupported {
		return modeSwitchArmAuto
	}
	return arm
}

// The preference lists the selector intersects with the live rows. Order is cost
// first: sonnet before opus, the pinned haiku before the alias. Neither list is
// authoritative — a model absent from the live list is simply never selected, and
// the fallback below takes the first qualifying row in arrival order.
var (
	modeSwitchAutoCapablePreference   = []string{"sonnet", "default", "claude-fable-5-1[1m]", "opus"}
	modeSwitchAutoIncapablePreference = []string{"claude-haiku-4-5", "haiku"}
)

// --- the model-list observation --------------------------------------------------

// modeSwitchAutoRow is one entry of claude's published model list, reduced to the
// two things this measurement needs plus the one distinction the reduction would
// otherwise destroy.
//
// KeyPresent separates "published false" from "key absent". SUPPORTSAUTOMODE
// FALSE IS SPELLED BY ABSENCE at 2.1.239 — no row carries the literal `false`,
// the two haiku rows simply omit the key, and #1819's decode maps absent to
// false. A reader grepping the committed capture for `false` would find it only
// because this field writes it.
type modeSwitchAutoRow struct {
	Value            string `json:"value"`
	SupportsAutoMode bool   `json:"supports_auto_mode"`
	KeyPresent       bool   `json:"key_present"`
}

// modeSwitchAutoObservation is what one arm's capture records about the model it
// ran on. No field carries omitempty: an observation that dropped
// supports_auto_mode when it was false would reproduce the exact absence-means-
// false trap this type exists to make visible.
type modeSwitchAutoObservation struct {
	ModelListRequestID string              `json:"model_list_request_id"`
	Model              string              `json:"model"`
	RowPresent         bool                `json:"row_present"`
	SupportsAutoMode   bool                `json:"supports_auto_mode"`
	KeyPresent         bool                `json:"key_present"`
	Published          []modeSwitchAutoRow `json:"published"`
}

// modeSwitchAutoRows reads the `models` array out of an initialize reply and
// returns one row per entry, plus whether an array was found at all.
//
// THREE PLACEMENTS ARE READ — top level, under `response`, and under
// `response.response` — and the third is where a real reply actually puts it.
// initControlSummarize carries the measurement and the reason; a reader that
// stops at either of the first two reports a false absence.
//
// The `found` return is decided on the RAW BYTES, so `"models":[]` and an absent
// key stay distinguishable: unmarshalling straight into a slice collapses both to
// nil, and "claude reported an empty list" is a different finding from "claude
// reported no list".
func modeSwitchAutoRows(responses []json.RawMessage) (rows []modeSwitchAutoRow, found bool) {
	for _, raw := range responses {
		var env struct {
			Models   json.RawMessage `json:"models"`
			Response struct {
				Models   json.RawMessage `json:"models"`
				Response struct {
					Models json.RawMessage `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
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
		found = true

		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(models, &entries); err != nil {
			return nil, true
		}
		for _, entry := range entries {
			var row modeSwitchAutoRow
			if v, ok := entry["value"]; ok {
				// A non-string `value` leaves Value empty, which the selector
				// then rejects along with every other unusable shape.
				_ = json.Unmarshal(v, &row.Value)
			}
			if v, ok := entry["supportsAutoMode"]; ok && string(v) != "null" {
				row.KeyPresent = true
				_ = json.Unmarshal(v, &row.SupportsAutoMode)
			}
			rows = append(rows, row)
		}
		return rows, true
	}
	return nil, false
}

// modeSwitchModelValueOK reports whether value may reach `--model`.
//
// THIS IS THE ONE PLACE A VALUE READ OUT OF ONE CHILD'S STDOUT BECOMES ANOTHER
// CHILD'S ARGV, and the hazard is not shell quoting — exec.CommandContext takes an
// arg slice and no shell is involved. It is that a value beginning with `-` is a
// FLAG to claude's own parser: a row whose value read `--dangerously-skip-
// permissions` would turn `--model <value>` into a valueless `--model` followed by
// a bypass flag, launching the child in the one posture every arm's finding
// assumes it is not in. The probe would pass while recording something false,
// which is the same class of failure the ticket's Technical Notes warn about in
// its other form.
//
// The charset is deliberately narrow and still admits every value 2.1.239
// publishes, brackets included (`claude-fable-5[1m]`). A row that fails is skipped
// during selection and still recorded in Published, so the rejection is visible
// rather than silent.
func modeSwitchModelValueOK(value string) bool {
	if value == "" || len(value) > modeSwitchModelValueMax {
		return false
	}
	if value[0] == '-' {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("._:@/+[]-", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// modeSwitchPickModel returns the first usable model publishing supportsAutoMode
// == want, preferring the named values in order and falling back to arrival
// order. It returns "" when no row qualifies, which is a FINDING about claude's
// model list rather than an instrument failure.
func modeSwitchPickModel(rows []modeSwitchAutoRow, want bool, prefer []string) string {
	usable := func(r modeSwitchAutoRow) bool {
		return r.SupportsAutoMode == want && modeSwitchModelValueOK(r.Value)
	}
	for _, name := range prefer {
		for _, r := range rows {
			if r.Value == name && usable(r) {
				return r.Value
			}
		}
	}
	for _, r := range rows {
		if usable(r) {
			return r.Value
		}
	}
	return ""
}

// modeSwitchObservation builds the record one arm carries about its own model.
func modeSwitchObservation(rows []modeSwitchAutoRow, model string) *modeSwitchAutoObservation {
	obs := &modeSwitchAutoObservation{
		ModelListRequestID: modeSwitchDiscoveryRequestID,
		Model:              model,
		Published:          rows,
	}
	for _, r := range rows {
		if r.Value == model {
			obs.RowPresent = true
			obs.SupportsAutoMode = r.SupportsAutoMode
			obs.KeyPresent = r.KeyPresent
			break
		}
	}
	return obs
}

// --- the discovery child ----------------------------------------------------------

// runModeSwitchDiscovery spawns ONE extra child, writes a single `initialize`
// control request on its held-open stdin, reads the reply and returns the
// published model rows. It drives no probe turn, so it spends no assistant
// tokens.
//
// THE RAW REPLY IS NEITHER RECORDED NOR LOGGED. initControlSummarize's doc block
// records that this payload carries `account` and `pid` beside `models`, and
// these captures are committed to a public repo; only the derived value /
// supportsAutoMode / key-presence triples cross out of here. #1688's committed
// captures are where the reply's full shape already lives.
//
// It t.Fatalf's when the instrument measured nothing — no child, no
// control_response, or a reply carrying no models array — and it does so BEFORE
// any arm spends a token.
func runModeSwitchDiscovery(t *testing.T, claudeBin, workdir string) []modeSwitchAutoRow {
	t.Helper()

	argv := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", modeSwitchDiscoveryModel,
		"--max-turns", modeSwitchDiscoveryMaxTurns,
	}

	ctx, cancel := context.WithTimeout(context.Background(), modeSwitchDiscoveryBudget)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin, argv...)
	cmd.Dir = workdir

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("#2041[discovery]: stdin pipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("#2041[discovery]: stdout pipe: %v", err)
	}
	// Captured rather than inherited: an inherited stderr would print an auth
	// failure's message — the one most likely to carry a credential — straight
	// into a run log that gets salvaged. It is scrubbed before it is ever shown.
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("#2041[discovery]: start claude: %v", err)
	}

	// Single reader goroutine, closed over readerDone, so none outlives the child.
	rec := &setModeRecorder{}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), setModeScanMax)
		for scanner.Scan() {
			rec.add(scanner.Bytes())
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			t.Logf("#2041[discovery]: scanner: %v", err)
		}
	}()

	line, err := initControlLine(modeSwitchDiscoveryRequestID)
	if err != nil {
		t.Fatalf("#2041[discovery]: %v", err)
	}
	if _, err := stdinPipe.Write(line); err != nil {
		t.Fatalf("#2041[discovery]: write initialize request: %v", err)
	}
	if !setModeWaitFor(rec.controlResponseCount, 1, setModeControlBudget) {
		t.Logf("#2041[discovery]: no control_response within %s", setModeControlBudget)
	}
	if err := stdinPipe.Close(); err != nil {
		t.Logf("#2041[discovery]: stdin close: %v", err)
	}
	waitErr := cmd.Wait()
	<-readerDone

	// Scrub before the raw stderr can reach a failure message below.
	initControlScrubbed(t, stderrBuf.String())

	responses := rec.snapshotControlResponses()
	if len(responses) == 0 {
		t.Fatalf("#2041[discovery]: claude returned no control_response; the model list is what "+
			"selects every arm's model, so there is nothing to measure\nstderr:\n%s\nwaitErr: %v",
			truncateString(stderrBuf.String(), stderrFixtureCap), waitErr)
	}
	rows, found := modeSwitchAutoRows(responses)
	if !found {
		t.Fatalf("#2041[discovery]: %d control_response(s) carried no models array at any of the "+
			"three placements; refusing to select a model the run never observed", len(responses))
	}
	if len(rows) == 0 {
		t.Fatalf("#2041[discovery]: claude published an EMPTY models array; there is no model to " +
			"run an arm on")
	}

	for _, r := range rows {
		t.Logf("#2041[discovery]: model %q supportsAutoMode=%v (key present=%v, usable as argv=%v)",
			r.Value, r.SupportsAutoMode, r.KeyPresent, modeSwitchModelValueOK(r.Value))
	}
	return rows
}

// --- the fixture family -----------------------------------------------------------

// modeSwitchNameToken makes one token safe to join into a filename: everything
// outside [A-Za-z0-9_-] becomes `_`, and the result is capped.
//
// It is NOT versionSlug, and the difference is the case fold. versionSlug
// lowercases, which would mint permission_mode_switch_v2.1.239_acceptedits.json
// and cost the reader the one token that says which mode a capture is about.
//
// The ARM is the half that needs this. The version token arrives through
// versionSlug, which already maps `/` to `_`; the arm token arrives from a table
// a future contributor extends by typing a string, and `a/b` or `/abs` there
// would put a capture outside testdata/ — silently, since the writer creates no
// directory for it and the mkdir above is for testdata/ alone. Sanitising rather
// than rejecting is deliberate: containment is a property of the NAME, and a
// namer that can be asked for any string and always answers with a plain
// component is simpler to hold than one whose callers must validate first.
func modeSwitchNameToken(token string) string {
	var b strings.Builder
	for i := 0; i < len(token) && i < 32; i++ {
		c := token[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// modeSwitchFixtureName mints the filename for one arm. The arm token keeps the
// five arms independently recoverable — and the auto pair especially, since those
// two differ only in the model and their names are the only thing separating
// their captures.
func modeSwitchFixtureName(versionToken, arm string) string {
	return fmt.Sprintf("%s_v%s_%s.json",
		modeSwitchFamilyPrefix, versionSlug(versionToken), modeSwitchNameToken(arm))
}

func modeSwitchFixturePath(t *testing.T, versionToken, arm string) string {
	t.Helper()
	return filepath.Join(packageDir(t), "testdata", modeSwitchFixtureName(versionToken, arm))
}

// --- the live probe ---------------------------------------------------------------

// TestRealClaude_InBandModeSwitch_Probe drives five live children through the
// identical two-turn sequence, each carrying exactly one `set_permission_mode`
// control request between the turns, and records per arm the model it ran on,
// what the live model list published about that model, claude's verbatim
// control_response, and the init.permissionMode echo that followed.
//
// THE NAME PREFIX IS DELIBERATELY NOT TestRealClaude_SetPermissionMode.
// set-permission-mode-inband-probe.md documents `-run TestRealClaude_SetPermissionMode`
// as #1595's reproduce command, and sharing the prefix would sweep five more
// children into a run that budgeted four.
//
// It PASSES on every recorded outcome — a mode claude refuses is a finding — and
// fails only where the instrument measured nothing: no child, no control_response,
// or no init line.
func TestRealClaude_InBandModeSwitch_Probe(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials

	workdir := filepath.Join(home, modeSwitchWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2041: create workdir: %v", err)
	}

	versionRaw, versionToken := captureClaudeVersion(t)
	t.Logf("#2041: claude version %q (token %q)", versionRaw, versionToken)

	rows := runModeSwitchDiscovery(t, claudeBin, workdir)

	capable := modeSwitchPickModel(rows, true, modeSwitchAutoCapablePreference)
	incapable := modeSwitchPickModel(rows, false, modeSwitchAutoIncapablePreference)
	t.Logf("#2041: selected auto-capable model %q, auto-INcapable model %q", capable, incapable)

	// Live selection rather than a configured constant is what makes the ticket's
	// named failure impossible rather than merely unlikely: an `auto` refusal can
	// never be recorded against a model that never supported auto, because the
	// model was chosen out of the run's own list.
	if capable == "" {
		t.Logf("#2041: FINDING — no model in claude's live list publishes supportsAutoMode true " +
			"and carries an argv-usable value. No arm was driven: every arm below is defined " +
			"against a model that supports auto, and running them on one that does not is the " +
			"exact false reading this probe exists to prevent.")
		return
	}

	for _, name := range modeSwitchArmNames {
		model := capable
		if name == modeSwitchArmAutoUnsupported {
			if incapable == "" {
				t.Logf("#2041: FINDING — no model publishes supportsAutoMode false, so the "+
					"%s arm has no model to run on and was skipped. The contrast this arm "+
					"exists to draw is not available at this claude version.", name)
				continue
			}
			model = incapable
		}

		arm := setModeArm{
			name:       name,
			launchYOLO: false, // pyry's stream argv carries no --dangerously-skip-permissions
			targetMode: modeSwitchTargetMode(name),
		}
		cfg := setModeChildConfig{
			model:       model,
			promptOne:   modeSwitchPromptOne,
			promptTwo:   modeSwitchPromptTwo,
			maxTurns:    modeSwitchMaxTurns,
			fixturePath: modeSwitchFixturePath,
			autoMode:    modeSwitchObservation(rows, model),
		}

		t.Run(name, func(t *testing.T) {
			rec := runSetModeChild(t, claudeBin, workdir, arm, versionRaw, versionToken, cfg)

			t.Logf("#2041[%s]: requested %q on model %q (published supportsAutoMode=%v, "+
				"row present=%v, key present=%v)",
				name, arm.targetMode, model, cfg.autoMode.SupportsAutoMode,
				cfg.autoMode.RowPresent, cfg.autoMode.KeyPresent)

			// The two instrument failures, checked AFTER the fixture is written so
			// the evidence lands either way, and reported with Errorf so the
			// remaining arms still run and still land theirs.
			if len(rec.ControlResponses) == 0 {
				t.Errorf("#2041[%s]: no control_response was read; the instrument measured "+
					"nothing about this mode", name)
			}
			for i, resp := range rec.ControlResponses {
				t.Logf("#2041[%s]: control_response[%d] verbatim: %s", name, i, resp)
			}
			if len(rec.InitPermissionModes) == 0 {
				t.Errorf("#2041[%s]: no system/init line was seen; there is no post-change "+
					"permissionMode read", name)
			} else {
				t.Logf("#2041[%s]: init.permissionMode in arrival order: %v (last=%q, requested=%q, "+
					"request_id matched=%v)",
					name, rec.InitPermissionModes,
					rec.InitPermissionModes[len(rec.InitPermissionModes)-1],
					rec.RequestedMode, rec.ControlResponseRequestIDMatched)
			}
			t.Logf("#2041[%s]: capture: %s", name, modeSwitchFixtureName(versionToken, name))
		})
	}
}

// --- the deterministic half ----------------------------------------------------

// modeSwitchNamePattern is one committed-fixture family modeSwitchFixtureName's
// output must stay out of, paired with controls proving the pattern can still
// return true. Shaped after poolRevokeNamePattern, including the reason each
// field exists.
type modeSwitchNamePattern struct {
	glob string

	// underTestdata selects the string the glob is evaluated against, and it is
	// the field deciding whether a row asserts anything at all. fixtureGlob and
	// dropcapFixtureGlob are evaluated by their owning tests against paths
	// relative to the package directory, so they can only ever match a
	// testdata/-prefixed subject; setModeFamilyGlob names base names and matches
	// no prefixed subject at all.
	underTestdata bool

	// controls are BASE names of the shape this glob was written for; the anchor
	// below adds the prefix where the row needs one.
	//
	// Synthetic literals, never committed filenames: a control written as the one
	// capture that exists today reddens spuriously the day that capture is
	// retaken at a new version, and the cheap repair for a spurious red is to
	// weaken the control.
	controls []string

	// hazard is what a match would cost, for the failure message.
	hazard string
}

// modeSwitchAnchor returns the string p's glob is evaluated against. The single
// anchoring point in this file: both the negative assertions and the controls
// call it, which is what keeps a mis-anchored row from going silently vacuous.
func modeSwitchAnchor(p modeSwitchNamePattern, base string) string {
	if p.underTestdata {
		return filepath.Join("testdata", base)
	}
	return base
}

// TestModeSwitch_FixtureNamesAvoidRegressionGlobs is AC 3's deterministic half:
// no name modeSwitchFixtureName can mint joins a committed fixture family, every
// pattern making that claim can still match something, and every minted name
// stays a plain component directly inside testdata/.
//
// The three families and what a match would cost:
//
//   - fixtureGlob — TestRealClaude_PermissionProtocol_RegressionFixtures sweeps it
//     and asserts over every match that no stdout_events entry has type
//     control_request, which is the exact thing this ticket exists to record. That
//     family already uses these four mode names as its suffixes, so
//     permission_protocol_v2.1.239_acceptEdits.json is the NATURAL name and it
//     reddens the gate on every capture.
//   - setModeFamilyGlob — #1595's committed record of the in-band revocation wire
//     format. A live run of this file writing there overwrites committed evidence
//     while every test stays green.
//   - dropcapFixtureGlob — the dropped-line capture's own sweep.
//
// No subprocess and no credentials: it passes on a machine with no claude at all.
func TestModeSwitch_FixtureNamesAvoidRegressionGlobs(t *testing.T) {
	t.Parallel()

	// setModeAdversarialVersionTokens is the list AC 3 names — the tokens
	// TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs already
	// uses, shared rather than copied so the two lists cannot drift apart. The
	// two extras are a deliberate superset: that namer must clear ONE family
	// whose head could arrive as a version token, this one must clear THREE, so
	// it carries one such token per family.
	tokens := make([]string, 0, len(setModeAdversarialVersionTokens)+2)
	tokens = append(tokens, setModeAdversarialVersionTokens...)
	tokens = append(tokens, "set_permission_mode", "dropped_lines")

	// The other input dimension. The version token is slugged and the ARM token
	// is not, so the arm is the half a future contributor can walk out of
	// testdata/ by typing a string into the arm table. These are this test's own
	// literals and must not be added to modeSwitchArmNames, which the live probe
	// ranges to build its children.
	arms := make([]string, 0, len(modeSwitchArmNames)+6)
	arms = append(arms, modeSwitchArmNames...)
	arms = append(arms, "a/b", "..", "../..", "/abs", "", "set_permission_mode")

	patterns := []modeSwitchNamePattern{
		{
			glob:          fixtureGlob,
			underTestdata: true,
			controls:      []string{"permission_protocol_v0.0.0_acceptEdits.json"},
			hazard: "TestRealClaude_PermissionProtocol_RegressionFixtures sweeps that glob and " +
				"asserts no stdout_events entry is a control_request, which is precisely what " +
				"every capture here records",
		},
		{
			glob:     setModeFamilyGlob,
			controls: []string{"set_permission_mode_v0.0.0_revoke.json"},
			hazard: "that is #1595's committed record of the in-band revocation wire format, and " +
				"a live run here writing there overwrites it while every test stays green",
		},
		{
			glob:          dropcapFixtureGlob,
			underTestdata: true,
			controls:      []string{"dropped_lines_v0.0.0.json"},
			hazard:        "the dropped-line capture's fixture sweep would read a mode-switch record as one of its own",
		},
	}

	wantDir := filepath.Join(packageDir(t), "testdata")

	t.Run("no minted name joins a committed family", func(t *testing.T) {
		t.Parallel()

		for _, token := range tokens {
			for _, arm := range arms {
				base := modeSwitchFixtureName(token, arm)
				for _, p := range patterns {
					subject := modeSwitchAnchor(p, base)
					matched, err := filepath.Match(p.glob, subject)
					if err != nil {
						t.Fatalf("filepath.Match(%q, %q): %v; a malformed pattern constant makes "+
							"every comparison in this file meaningless", p.glob, subject, err)
					}
					if matched {
						t.Errorf("token %q arm %q mints %q, which matches %q: %s",
							token, arm, base, p.glob, p.hazard)
					}
				}
			}
		}
	})

	// Without this the three negative assertions above pass identically against a
	// typo'd pattern constant, a mis-anchored row, or a glob that matches nothing
	// at all.
	t.Run("every pattern can still match its own family", func(t *testing.T) {
		t.Parallel()

		for _, p := range patterns {
			if len(p.controls) == 0 {
				t.Errorf("pattern %q carries no control, so its negative assertion proves nothing", p.glob)
				continue
			}
			for _, control := range p.controls {
				subject := modeSwitchAnchor(p, control)
				matched, err := filepath.Match(p.glob, subject)
				if err != nil {
					t.Fatalf("filepath.Match(%q, %q): %v", p.glob, subject, err)
				}
				if !matched {
					t.Errorf("control %q does not match %q; the row asserts nothing, either "+
						"because the pattern is wrong or because underTestdata=%v anchors it "+
						"against the wrong string", subject, p.glob, p.underTestdata)
				}
			}
		}
	})

	t.Run("every minted path stays directly inside testdata", func(t *testing.T) {
		t.Parallel()

		for _, token := range tokens {
			for _, arm := range arms {
				path := modeSwitchFixturePath(t, token, arm)
				if dir := filepath.Dir(path); dir != wantDir {
					t.Errorf("token %q arm %q: fixture path %q resolves outside %q",
						token, arm, path, wantDir)
				}
			}
		}
	})

	// The arm token is what keeps the five arms independently recoverable. A
	// collision would silently overwrite one arm's evidence with another's — and
	// the auto pair is the one that matters, since those two differ ONLY in the
	// model and their names are the only thing separating their captures.
	t.Run("the five arms mint five distinct names", func(t *testing.T) {
		t.Parallel()

		seen := make(map[string]string, len(modeSwitchArmNames))
		for _, arm := range modeSwitchArmNames {
			name := modeSwitchFixtureName("2.1.239", arm)
			if prev, dup := seen[name]; dup {
				t.Errorf("arms %q and %q both mint fixture name %q; one arm's evidence would "+
					"overwrite the other's", prev, arm, name)
			}
			seen[name] = arm
		}
		if len(seen) != len(modeSwitchArmNames) {
			t.Errorf("got %d distinct names for %d arms", len(seen), len(modeSwitchArmNames))
		}
	})
}

// TestModeSwitchModelValueOK_RejectsAFlagShapedModelValue pins the one gate a
// value read out of a child's stdout passes through before it becomes another
// child's argv element.
//
// The hazard is NOT shell quoting — exec.CommandContext takes an arg slice and no
// shell is involved. It is that a value beginning with `-` is a FLAG to claude's
// own parser: a models row whose `value` read `--dangerously-skip-permissions`
// would turn `--model <value>` into a valueless `--model` followed by a bypass
// flag, launching the child in the one posture every arm's finding assumes it is
// not in. The probe would then pass while recording something false, which is the
// same class of failure the ticket's own Technical Notes warn about.
func TestModeSwitchModelValueOK_RejectsAFlagShapedModelValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		// Every value claude 2.1.239 publishes must survive, or the probe cannot
		// run at all. claude-fable-5[1m] is the awkward one and the reason the
		// charset admits brackets.
		{"published default", "default", true},
		{"published sonnet", "sonnet", true},
		{"published opus", "opus", true},
		// Both spellings: the committed 2.1.239 capture records the first and the
		// 2026-09-02 live run observed the second. Same binary version, drifted
		// value — see the header. Either must survive.
		{"fable as the committed capture spells it", "claude-fable-5[1m]", true},
		{"fable as the live 2.1.239 list spells it", "claude-fable-5-1[1m]", true},
		{"published haiku alias", "haiku", true},
		{"published haiku pinned", "claude-haiku-4-5", true},

		{"the bypass flag itself", "--dangerously-skip-permissions", false},
		{"a short flag", "-x", false},
		{"a lone dash", "-", false},
		{"empty", "", false},
		{"a space smuggles a second token", "sonnet --dangerously-skip-permissions", false},
		{"a newline", "sonnet\n--verbose", false},
		{"a NUL", "sonnet\x00", false},
		{"over the length bound", strings.Repeat("a", 65), false},
		{"exactly at the length bound", strings.Repeat("a", 64), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := modeSwitchModelValueOK(tc.value); got != tc.want {
				t.Errorf("modeSwitchModelValueOK(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
