//go:build e2e_realclaude

package realclaude

// #2060 — a child launched WITH --dangerously-skip-permissions, dropped to
// `default` in-band, then asked back into `bypassPermissions` in the SAME child.
// Does claude 2.1.239 accept the re-escalation?
//
// # The finding, measured 2026-09-03 against claude 2.1.239
//
// IT ACCEPTS. #1686's hand observation reproduces, and this is its first committed
// capture. Four children, 56 s, model claude-haiku-4-5.
//
//	arm             | init.permissionMode, arrival order         | turn 1  | turn 2  | turn 3
//	----------------+-------------------------------------------+---------+---------+--------
//	reescalate      | [bypassPermissions default bypassPermissions] | ungated | GATED   | ungated
//	enable          | [default default default]                 | gated   | gated   | gated
//	control_default | [default default default]                 | gated   | gated   | gated
//	control_bypass  | [bypassPermissions ×3]                    | ungated | ungated | ungated
//
// "gated" is permission_denials 1 with tool_result.is_error true; "ungated" is 0
// and false. Those two fields are the ONLY discriminators — tool_use_names,
// tool_result_seen, result_subtype, result_is_error and result_observed read
// identically on both controls, which setModeFieldMatches reports per field.
//
//   - `reescalate`: RE-ESCALATION APPLIED. Turn 3 matched the control_bypass arm
//     exactly. Both requests were acked `subtype:"success"` echoing the mode asked
//     for, both correlated by request_id:
//     `{"subtype":"success","request_id":"set-permission-mode-reescalate","response":{"mode":"default"}}`
//     then `…-reescalate-2","response":{"mode":"bypassPermissions"}}`.
//
//     THE STRONGEST PART OF THIS READ IS TURN 2. One child went ungated → GATED →
//     ungated across the two requests, and its init line reported
//     bypassPermissions → default → bypassPermissions in step. The posture is
//     therefore confirmed at every stage of the sequence, behaviourally and by
//     echo, in the same process: the downgrade genuinely landed before the
//     re-escalation was asked for, so the re-escalation verdict is not vacuous.
//     reescalateDowngrade's gate reports landed=true off the init read, and the
//     behavioural read agrees with it independently.
//
//   - `enable`: ESCALATION FAILED, the #1595 negative reproducing at 2.1.239. Turn 2
//     matched control_default. The refusal is BYTE-IDENTICAL to the one #1595
//     recorded at 2.1.220, nineteen releases earlier: `Cannot set permission mode
//     to bypassPermissions because the session was not launched with
//     --dangerously-skip-permissions`. So the probe still discriminates on this
//     argv (AC 3) and the instrument is sound.
//
//   - The `onSetPermissionMode callback not registered` string #1595 read out of the
//     binary did not appear on any arm, matching #1595's and #2041's results.
//
// The two together settle the mechanism precisely: **claude gates the escalation on
// the LAUNCH ARGV, not on the session's current mode.** A child launched with the
// flag may be moved out of bypass and back in as often as it likes; a child
// launched without it can never get there. That is exactly the reading #1595's
// header inferred from the refusal's wording and could not test.
//
// WHAT IT DOES NOT SETTLE, and #1686 must not read into it: this measures CLAUDE,
// not pyry. See "WHAT THIS DOES NOT CHANGE" below. It is also a claude-version
// fact, not a guarantee — #1595 recorded the same caveat about the opposite
// direction and it held for nineteen releases, which is evidence, not a contract.
//
// Every arm ran clean: exit 0, no tripped deadline, no error_max_turns, 51-73
// stdout lines each. `reescalateMaxTurns` of "12" was not approached.
//
// # What #1595 measured, and what it did not
//
// #1595 measured, 2026-08-19 against 2.1.220, both arms committed under
// testdata/set_permission_mode_v2.1.220_*.json:
//
//   - `revoke` — launched WITH the flag, asked into `default`: REVOCATION APPLIED.
//   - `enable` — launched WITHOUT the flag, asked into `bypassPermissions`:
//     ESCALATION FAILED, refused with `Cannot set permission mode to
//     bypassPermissions because the session was not launched with
//     --dangerously-skip-permissions`.
//
// It did NOT measure the re-escalation: with the flag, down to `default`, then
// back up, in one child. `setModeArms` holds four arms and `setModeArm` carried a
// single targetMode, so no committed fixture anywhere records a two-request child.
// #1686 rests its entire case on a HAND run of that case, taken 2026-08-21 against
// 2.1.220 and never captured. There is no 2.1.220 baseline to diff against; this
// is the first committed measurement, taken at the installed claude.
//
// The hand observation was plausible before this run: #1595's refusal message
// names the LAUNCH ARGV, not the control request, and its header reads the gate
// that way — "claude gates the escalation on the launch argv, not on the control
// request". A child launched WITH the flag should therefore be allowed back up.
// The finding above is that measurement; expected and measured now agree, which
// they did not have to.
//
// # WHAT THIS DOES NOT CHANGE
//
// Whatever the verdict, the daemon's structural fail-safe is untouched. An in-band
// escalation is unreachable from pyry's own surface because permissionModeAllowed
// refuses `bypassPermissions` by NON-MEMBERSHIP, backing SetPermissionMode's
// contract; that allow-list is production code this ticket does not modify. claude
// ACCEPTING a re-escalation is not the daemon acquiring the ability to ask for one.
// #1686 decides whether anything is built on the answer.
//
// # Four children, one drive sequence
//
//	arm             | launch flag                    | control 1 | control 2          | read
//	----------------+--------------------------------+-----------+--------------------+------
//	reescalate      | --dangerously-skip-permissions  | default   | bypassPermissions  | turn 3
//	enable          | (none)                          | bypass…   | (none)             | turn 2
//	control_default | (none)                          | (none)    | (none)             | 2 and 3
//	control_bypass  | --dangerously-skip-permissions  | (none)    | (none)             | 2 and 3
//
// Every arm drives THREE turns — spawn, turn 1, [control 1], turn 2, [control 2],
// turn 3, close stdin — and the controls omit only the bracketed steps. That is
// forced by #1595's index-symmetry rule: a measurement arm's post-change read and
// its control must sit at the same turn index, so an arm read at turn 3 needs
// controls that HAVE a turn 3. `enable` keeps #1595's shape and is read at turn 2,
// which is what makes AC 3's discrimination check comparable to the committed
// 2.1.220 result.
//
// Because the two directions are read at different indices, the
// no-discrimination check runs PER DIRECTION at that direction's index rather than
// once up front the way #1595 does it: the controls can separate at one turn and
// collapse at another, and a single up-front check would report the wrong one.
//
// # Why the echoed response is not the verdict
//
// #1595's argument applies unchanged: an echoed `success` that still behaves like
// the bypass control is a failed change, and a change whose behaviour matches the
// bypass control succeeded even if its `control_response` was an error. The verdict
// is a whole-value comparison of probeOutcome; the echo and the init echo are
// recorded beside it and neither enters it. setModeFieldMatches renders the
// per-field breakdown so an INCONCLUSIVE can be read rather than merely reported —
// #1595's header records a run where a retried tool made turn 2's tool_use_names
// read [Bash Bash] and match neither control.
//
// # The downgrade gate — why a re-escalation verdict can be vacuous
//
// A child that never actually left bypass behaves like the bypass control either
// way, so a re-escalation verdict taken on it measures nothing. reescalateDowngrade
// confirms the downgrade landed BEFORE the second request, two ways: the init read
// (init line 0 reports `bypassPermissions`, line 1 — emitted by the turn AFTER the
// downgrade — reports `default`), falling back to a purely behavioural read when
// claude emitted fewer than two init lines. Where neither confirms, the run reports
// RE-ESCALATION NOT MEASURED with the reason. That is a recorded outcome, so the
// test still passes.
//
// # Argv
//
// Identical to #1595's, and the three deliberate divergences from the #383 spike
// documented in that header apply here verbatim: no --permission-prompt-tool, no
// --allowed-tools, and --dangerously-skip-permissions rather than
// --permission-mode bypassPermissions.
//
// Turns 1 and 2 reuse #1595's prompts verbatim for comparability. Turn 3 reads
// /usr — read-only, package-manager content, deliberately not $HOME, the workdir or
// /etc, because every turn's stdout is committed to a public repo.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run TestRealClaude_BypassReescalation ./internal/e2e/realclaude/
//
// Four children, twelve probe turns. The name prefix is deliberately neither
// TestRealClaude_SetPermissionMode nor TestRealClaude_InBandModeSwitch: both are
// documented reproduce filters for measurements that budgeted their own children,
// and sharing a prefix would sweep four more into them. The test PASSES on every
// recorded outcome and fails only where the instrument measured nothing.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// --- the arms ------------------------------------------------------------------

const (
	// The committed family these captures form. It shares no literal head with
	// permission_protocol_v, dropped_lines_v, set_permission_mode_v,
	// initialize_control_v, ask_user_question_v or permission_mode_switch_v, which
	// is what keeps it out of every sweep in this package;
	// TestBypassReescalation_FixtureNamesAvoidRegressionGlobs asserts that rather
	// than leaving it a convention to remember.
	reescalateFamilyPrefix = "bypass_reescalation"

	// A fresh EMPTY directory under the test's pinned $HOME, deliberately not a git
	// repo: less project context for claude to load, so the turns are cheaper.
	reescalateWorkdirName = "bypass-reescalation-work"

	// The third probe turn. Turns 1 and 2 are setModePromptOne / setModePromptTwo
	// unchanged, so this measurement's first two turns are byte-identical to
	// #1595's and the arms stay comparable to the committed 2.1.220 captures.
	//
	// /usr is a disclosure decision, not an arbitrary one: every turn's stdout is
	// committed to a public repo, so the directory is read-only package-manager
	// content rather than $HOME, the workdir or /etc.
	reescalatePromptThree = "Use the Bash tool to run `ls -la /usr` and report the first line of output."

	// Three Bash probes at #1595's 4x-per-turn headroom. Set here rather than on
	// setModeMaxTurns, which is shared with two other measurements that drive two
	// turns. A result line carrying subtype:"error_max_turns" lands in the fixture
	// plainly — raise and rerun.
	reescalateMaxTurns = "12"

	// The launch posture every measurement arm here assumes, and the mode the
	// downgrade targets. Named so the gate below cannot drift from the arm table.
	reescalateBypassMode  = "bypassPermissions"
	reescalateDefaultMode = "default"

	// A cap on a subprocess-supplied string before it reaches a log line. An
	// init.permissionMode is a short word in every capture on record, but it
	// arrives from claude and nothing bounds it, and the gate's reason quotes it
	// verbatim into a run log that gets salvaged.
	reescalateModeQuoteCap = 64
)

// The arm names. Read-only: the deterministic tests range this and add their own
// hostile literals rather than appending here.
const (
	reescalateArmMeasure = "reescalate"
	reescalateArmEnable  = "enable"
	reescalateArmDefault = "control_default"
	reescalateArmBypass  = "control_bypass"
)

var reescalateArmNames = []string{
	reescalateArmMeasure,
	reescalateArmEnable,
	reescalateArmDefault,
	reescalateArmBypass,
}

// reescalateArm builds one arm inline, #2041's precedent: setModeArms is #1595's
// live 2.1.220 measurement and an arm appended there would join that run AND mint
// into the set_permission_mode_v* family.
func reescalateArm(name string) setModeArm {
	switch name {
	case reescalateArmMeasure:
		return setModeArm{
			name:             name,
			launchYOLO:       true,
			targetMode:       reescalateDefaultMode,
			secondTargetMode: reescalateBypassMode,
		}
	case reescalateArmEnable:
		// #1595's negative, unchanged: the arm that shows the probe still
		// discriminates at 2.1.239. One request, read at turn 2.
		return setModeArm{name: name, launchYOLO: false, targetMode: reescalateBypassMode}
	case reescalateArmBypass:
		return setModeArm{name: name, launchYOLO: true}
	default:
		return setModeArm{name: name, launchYOLO: false}
	}
}

// reescalateDirection pairs a measurement arm with the two controls its behaviour
// is classified against AND the turn index that comparison is made at.
//
// readTurn is what #1595's setModeDirection has no need for: there, both
// directions are read at turn 2. Here `reescalate`'s post-change read is turn 3
// and `enable`'s is turn 2, so the index travels with the row — a direction
// classified against a control at a different index folds in a turn-index confound,
// which is the confound the whole drive sequence is shaped to avoid.
type reescalateDirection struct {
	arm            string
	readTurn       int // 0-based index into ProbeOutcomes
	appliedControl string
	failedControl  string
	appliedVerdict string
	failedVerdict  string
}

var reescalateDirections = []reescalateDirection{
	{
		arm:            reescalateArmMeasure,
		readTurn:       2, // turn 3, after the second control request
		appliedControl: reescalateArmBypass,
		failedControl:  reescalateArmDefault,
		appliedVerdict: "RE-ESCALATION APPLIED",
		failedVerdict:  "RE-ESCALATION FAILED",
	},
	{
		arm:            reescalateArmEnable,
		readTurn:       1, // turn 2, #1595's shape
		appliedControl: reescalateArmBypass,
		failedControl:  reescalateArmDefault,
		appliedVerdict: "ESCALATION APPLIED",
		failedVerdict:  "ESCALATION FAILED",
	},
}

// reescalateNotMeasured is the verbatim phrase the run records when the downgrade
// could not be shown to have landed. AC 2 names it: a re-escalation verdict on a
// child that never left bypass is vacuous, so the run reports this instead.
const reescalateNotMeasured = "RE-ESCALATION NOT MEASURED"

// --- the downgrade gate ---------------------------------------------------------

// reescalateDowngrade reports whether the child is shown to have been in bypass at
// launch AND in `default` by the time the second control request was written, plus
// the read that settled it.
//
// Two paths, in order. The INIT read is primary: init lines are emitted per turn,
// so index 0 is the launch posture and index 1 is the posture the turn after the
// downgrade reported — #1595 recorded exactly `[bypassPermissions, default]` for
// its `revoke` arm. The BEHAVIOURAL read is the fallback for a child that emitted
// fewer than two init lines: turn 1 must match the bypass control and turn 2 the
// default control, with the controls actually separating at both indices. AC 2
// admits either read; neither is skippable in favour of assuming the downgrade took.
//
// arm, ctlDefault and ctlBypass are whole ProbeOutcomes slices rather than picked
// turns, so this function owns the indices it compares and a caller cannot pair a
// turn against a control at another one.
func reescalateDowngrade(initModes []string, arm, ctlDefault, ctlBypass []probeOutcome) (bool, string) {
	if len(initModes) >= 2 {
		if launch := initModes[0]; launch != reescalateBypassMode {
			return false, fmt.Sprintf("the child did not launch in %s — init line 0 reported %q, "+
				"so there was no bypass posture to drop and nothing to re-escalate",
				reescalateBypassMode, truncateString(launch, reescalateModeQuoteCap))
		}
		if after := initModes[1]; after != reescalateDefaultMode {
			return false, fmt.Sprintf("the downgrade did not land — the init line after it reported "+
				"%q, not %q, so the child was still in bypass when the re-escalation was asked for",
				truncateString(after, reescalateModeQuoteCap), reescalateDefaultMode)
		}
		return true, fmt.Sprintf("init.permissionMode went %q → %q across the downgrade",
			reescalateBypassMode, reescalateDefaultMode)
	}

	d1, b1 := setModeOutcomeAt(ctlDefault, 0), setModeOutcomeAt(ctlBypass, 0)
	d2, b2 := setModeOutcomeAt(ctlDefault, 1), setModeOutcomeAt(ctlBypass, 1)
	if d1.equal(b1) || d2.equal(b2) {
		return false, fmt.Sprintf("fewer than two system/init lines (%v) and %s at the turn the "+
			"behavioural fallback would read, so neither read can show the downgrade landed",
			initModes, setModeNoDiscrimination)
	}
	if got := setModeOutcomeAt(arm, 0); !got.equal(b1) {
		return false, fmt.Sprintf("fewer than two system/init lines (%v) and turn 1 did not match "+
			"the %s control, so the child is not shown to have started in bypass\n  measured: %s",
			initModes, reescalateArmBypass, got)
	}
	if got := setModeOutcomeAt(arm, 1); !got.equal(d2) {
		return false, fmt.Sprintf("fewer than two system/init lines (%v) and turn 2 did not match "+
			"the %s control, so the downgrade is not shown to have landed\n  measured: %s",
			initModes, reescalateArmDefault, got)
	}
	return true, fmt.Sprintf("no init read was available (%v); behaviourally, turn 1 matched the %s "+
		"control and turn 2 the %s control", initModes, reescalateArmBypass, reescalateArmDefault)
}

// --- the fixture family ----------------------------------------------------------

// reescalateFixtureName mints the filename for one arm. The prefix is a literal no
// input can reach — that is the whole reason a minted name cannot join a committed
// family — and the arm token goes through modeSwitchNameToken, which preserves case
// (versionSlug would lowercase control_default's neighbours into unreadability) and
// maps every path metacharacter to `_`, so a future contributor typing `a/b` into
// the arm table cannot land a capture outside testdata/.
func reescalateFixtureName(versionToken, arm string) string {
	return fmt.Sprintf("%s_v%s_%s.json",
		reescalateFamilyPrefix, versionSlug(versionToken), modeSwitchNameToken(arm))
}

func reescalateFixturePath(t *testing.T, versionToken, arm string) string {
	t.Helper()
	return filepath.Join(packageDir(t), "testdata", reescalateFixtureName(versionToken, arm))
}

// --- the live probe ---------------------------------------------------------------

// TestRealClaude_BypassReescalation_Probe drives four live children through the
// identical three-turn sequence — one carrying two sequential set_permission_mode
// requests, one carrying #1595's single escalation request, two of them controls
// launched in the target posture from the start — and records per arm the verbatim
// control_responses, the init.permissionMode echoes, and the behavioural read of
// each turn.
//
// It PASSES on every recorded outcome. A refused re-escalation is a finding, an
// unmeasurable one is a finding, and only an instrument that measured nothing is a
// failure — which runSetModeChild raises for itself.
func TestRealClaude_BypassReescalation_Probe(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials

	workdir := filepath.Join(home, reescalateWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2060: create workdir: %v", err)
	}

	versionRaw, versionToken := captureClaudeVersion(t)
	t.Logf("#2060: claude version %q (token %q)", versionRaw, versionToken)

	// One config for all four arms: they differ only in the launch flag and the
	// control requests, and every other dimension is genuinely the same. promptThree
	// is set on the controls too — that is what gives them the turn 3 the
	// measurement arm's read is classified against.
	cfg := setModeChildConfig{
		model:       setModeModel,
		promptOne:   setModePromptOne,
		promptTwo:   setModePromptTwo,
		promptThree: reescalatePromptThree,
		maxTurns:    reescalateMaxTurns,
		fixturePath: reescalateFixturePath,
	}

	// Sequential, no t.Parallel: at most one child and one reader goroutine exist at
	// a time, and the four arms share one pinned $HOME.
	records := make(map[string]*setModeFixtureRecord, len(reescalateArmNames))
	for _, name := range reescalateArmNames {
		arm := reescalateArm(name)
		t.Run(name, func(t *testing.T) {
			records[name] = runSetModeChild(t, claudeBin, workdir, arm, versionRaw, versionToken, cfg)
			t.Logf("#2060[%s]: capture: %s", name, reescalateFixtureName(versionToken, name))
		})
	}

	var missing []string
	for _, name := range reescalateArmNames {
		if records[name] == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Logf("#2060: cross-arm verdict UNAVAILABLE — arm(s) %v produced no record (a -run "+
			"filter, or an instrument failure above). Not computing a verdict from a missing control.",
			missing)
		return
	}

	outcomes := func(name string) []probeOutcome { return records[name].ProbeOutcomes }
	at := func(name string, i int) probeOutcome { return setModeOutcomeAt(outcomes(name), i) }

	for i := 0; i < 3; i++ {
		t.Logf("#2060: control_default turn %d: %s", i+1, at(reescalateArmDefault, i))
		t.Logf("#2060: control_bypass  turn %d: %s", i+1, at(reescalateArmBypass, i))
	}

	// The echoes are recorded alongside the verdicts, as separate rows. Neither
	// enters one. An absent control_response is logged rather than raised: AC 1
	// forbids the echo from deciding anything, so its absence does not blind the
	// instrument the way it would a measurement whose read IS the ack.
	for _, name := range []string{reescalateArmMeasure, reescalateArmEnable} {
		r := records[name]
		t.Logf("#2060: %s: requested %q then %q, %d control_response(s), first request_id matched=%v",
			name, r.RequestedMode, reescalateSecondMode(r), len(r.ControlResponses),
			r.ControlResponseRequestIDMatched)
		if r.SecondRequest != nil {
			t.Logf("#2060: %s: second request_id matched=%v", name, r.SecondRequest.ControlResponseIDMatched)
		}
		if len(r.ControlResponses) == 0 {
			t.Logf("#2060: %s: NO control_response was observed", name)
		}
		for i, resp := range r.ControlResponses {
			t.Logf("#2060: %s: control_response[%d] verbatim: %s", name, i, resp)
		}
		if len(r.InitPermissionModes) == 0 {
			t.Logf("#2060: %s: NO system/init line was observed at all", name)
		} else {
			t.Logf("#2060: %s: init.permissionMode in arrival order: %v", name, r.InitPermissionModes)
		}
	}

	// AC 2, checked before the measurement arm is classified at all.
	landed, why := reescalateDowngrade(records[reescalateArmMeasure].InitPermissionModes,
		outcomes(reescalateArmMeasure), outcomes(reescalateArmDefault), outcomes(reescalateArmBypass))
	t.Logf("#2060: downgrade gate: landed=%v — %s", landed, why)

	for _, dir := range reescalateDirections {
		if dir.arm == reescalateArmMeasure && !landed {
			t.Logf("#2060: %s VERDICT: %s — %s", dir.arm, reescalateNotMeasured, why)
			continue
		}

		applied := at(dir.appliedControl, dir.readTurn)
		failed := at(dir.failedControl, dir.readTurn)

		// AC 5, evaluated per direction at ITS read index rather than once up front:
		// the controls can separate at one turn and collapse at another.
		if applied.equal(failed) {
			t.Logf("#2060: %s VERDICT: %s (the two controls produced identical turn-%d behaviour, "+
				"so no behavioural read can separate the postures there)",
				dir.arm, setModeNoDiscrimination, dir.readTurn+1)
			continue
		}

		got := at(dir.arm, dir.readTurn)
		var verdict string
		switch {
		case got.equal(applied):
			verdict = fmt.Sprintf("%s — turn %d matches the %s control",
				dir.appliedVerdict, dir.readTurn+1, dir.appliedControl)
		case got.equal(failed):
			verdict = fmt.Sprintf("%s — turn %d matches the %s control",
				dir.failedVerdict, dir.readTurn+1, dir.failedControl)
		default:
			verdict = fmt.Sprintf("INCONCLUSIVE — turn %d matched neither the %s nor the %s control",
				dir.readTurn+1, dir.appliedControl, dir.failedControl)
		}
		t.Logf("#2060: %s VERDICT: %s\n  measured: %s", dir.arm, verdict, got)
		for _, row := range setModeFieldMatches(got, applied, failed, dir.appliedControl, dir.failedControl) {
			t.Logf("#2060: %s field: %s", dir.arm, row)
		}
	}
}

// reescalateSecondMode renders an arm's second requested mode for a log line,
// naming the absence rather than printing an empty string.
func reescalateSecondMode(r *setModeFixtureRecord) string {
	if r.SecondRequest == nil {
		return "(no second request)"
	}
	return r.SecondRequest.RequestedMode
}

// --- the deterministic half -------------------------------------------------------

// TestBypassReescalation_FixtureNamesAvoidRegressionGlobs is the offline half: no
// name reescalateFixtureName can mint joins a committed fixture family, every
// pattern making that claim can still match something, every minted name stays a
// plain component directly inside testdata/, and the four arms mint four distinct
// names.
//
// It reuses modeSwitchNamePattern and modeSwitchAnchor rather than declaring a
// fourth copy of that table: poolRevokeNamePattern, modeSwitchNamePattern and this
// use are the same three fields with the same anchoring rule, and a copy is a
// definition that drifts. setModeAdversarialVersionTokens is shared for the same
// reason — its own doc block says a file adding a token there widens every check at
// once, and that is the point.
//
// No subprocess and no credentials: it passes on a machine with no claude at all.
func TestBypassReescalation_FixtureNamesAvoidRegressionGlobs(t *testing.T) {
	t.Parallel()

	// One token per family whose head could plausibly arrive as a version token,
	// on top of the shared adversarial list.
	tokens := append(append([]string(nil), setModeAdversarialVersionTokens...),
		"set_permission_mode", "dropped_lines", "permission_mode_switch",
		"initialize_control", "ask_user_question")

	// The other input dimension. The version token is slugged and the ARM token is
	// sanitised separately, so the arm is the half a future contributor can walk out
	// of testdata/ by typing a string into the arm table. These are this test's own
	// literals and must not be added to reescalateArmNames, which the live probe
	// ranges to build its children.
	arms := append(append([]string(nil), reescalateArmNames...),
		"a/b", "..", "../..", "/abs", "", "set_permission_mode")

	// Every committed family in this package. A capture of this measurement joining
	// any of them is read by a test asserting findings about a different argv, or
	// overwrites committed evidence while every test stays green.
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
			hazard: "that is #1595's committed record of the in-band revocation wire format, and a " +
				"live run here writing there overwrites it while every test stays green",
		},
		{
			glob:          dropcapFixtureGlob,
			underTestdata: true,
			controls:      []string{"dropped_lines_v0.0.0.json"},
			hazard:        "the dropped-line capture's fixture sweep would read this record as one of its own",
		},
		{
			glob:          initControlArmFixtureGlob,
			underTestdata: true,
			controls:      []string{"initialize_control_v0.0.0_before_first_turn.json"},
			hazard:        "#1688's initialize captures are swept per arm and asserted about a turn-free child",
		},
		{
			glob:          askQuestionFixtureGlob,
			underTestdata: true,
			controls:      []string{"ask_user_question_v0.0.0.json"},
			hazard:        "the ask_user_question reader would parse this record as one of its captures",
		},
		{
			// Built from #2041's own prefix constant rather than a literal, so the
			// day that family is renamed this row follows it instead of going
			// silently vacuous.
			glob:          "testdata/" + modeSwitchFamilyPrefix + "_v*_*.json",
			underTestdata: true,
			controls:      []string{modeSwitchFixtureName("0.0.0", "acceptEdits")},
			hazard: "#2041's committed 2.1.239 mode-switch captures would be joined by a measurement " +
				"of a different argv, and a live run here could overwrite one",
		},
	}

	wantDir := filepath.Join(packageDir(t), "testdata")

	t.Run("no minted name joins a committed family", func(t *testing.T) {
		t.Parallel()

		for _, token := range tokens {
			for _, arm := range arms {
				base := reescalateFixtureName(token, arm)
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

	// Without this the negative assertions above pass identically against a typo'd
	// pattern constant, a mis-anchored row, or a glob that matches nothing at all.
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
					t.Errorf("control %q does not match %q; the row asserts nothing, either because "+
						"the pattern is wrong or because underTestdata=%v anchors it against the "+
						"wrong string", subject, p.glob, p.underTestdata)
				}
			}
		}
	})

	t.Run("every minted path stays directly inside testdata", func(t *testing.T) {
		t.Parallel()

		for _, token := range tokens {
			for _, arm := range arms {
				path := reescalateFixturePath(t, token, arm)
				if dir := filepath.Dir(path); dir != wantDir {
					t.Errorf("token %q arm %q: fixture path %q resolves outside %q",
						token, arm, path, wantDir)
				}
			}
		}
	})

	t.Run("the four arms mint four distinct names", func(t *testing.T) {
		t.Parallel()

		seen := make(map[string]string, len(reescalateArmNames))
		for _, arm := range reescalateArmNames {
			name := reescalateFixtureName("2.1.239", arm)
			if prev, dup := seen[name]; dup {
				t.Errorf("arms %q and %q both mint fixture name %q; one arm's evidence would "+
					"overwrite the other's", prev, arm, name)
			}
			seen[name] = arm
		}
		if len(seen) != len(reescalateArmNames) {
			t.Errorf("got %d distinct names for %d arms", len(seen), len(reescalateArmNames))
		}
	})
}

// TestReescalateDowngrade_RefusesAVacuousReEscalationVerdict pins AC 2's gate: the
// re-escalation verdict is only meaningful on a child shown to have LEFT bypass
// before the second request, because one that never left behaves like the bypass
// control either way.
//
// The rows that matter most are the ones where the gate must say NO despite
// everything looking healthy — a child whose init never reported `default`, and a
// child with no usable init read whose turn 1 did not match the bypass control (so
// the flag never took, and what looks like a re-escalation is really an escalation
// on a `default`-launched child, which is #1595's already-answered question).
func TestReescalateDowngrade_RefusesAVacuousReEscalationVerdict(t *testing.T) {
	t.Parallel()

	// Two clearly-separated behavioural reads standing in for a gated turn and an
	// ungated one. Their exact fields do not matter; that they are UNEQUAL does.
	gated := probeOutcome{ToolUseNames: []string{"Bash"}, ToolResultSeen: true,
		ToolResultIsError: true, PermissionDenials: 1, ResultSubtype: "success", ResultObserved: true}
	free := probeOutcome{ToolUseNames: []string{"Bash"}, ToolResultSeen: true,
		ResultSubtype: "success", ResultObserved: true}

	// Three-turn slices for the two controls: bypass is ungated throughout, default
	// is gated throughout.
	ctlBypass := []probeOutcome{free, free, free}
	ctlDefault := []probeOutcome{gated, gated, gated}

	tests := []struct {
		name       string
		initModes  []string
		arm        []probeOutcome
		ctlDefault []probeOutcome
		ctlBypass  []probeOutcome
		want       bool
	}{
		{
			name:      "the init read #1595 recorded for revoke",
			initModes: []string{"bypassPermissions", "default", "bypassPermissions"},
			arm:       []probeOutcome{free, gated, free},
			want:      true,
		},
		{
			name: "the init read wins over a behavioural read that disagrees",
			// A downgrade confirmed by init still counts when turn 2's whole-value
			// comparison is spoiled — #1595's header records a retried tool doing
			// exactly that. The gate must not turn a known flake into NOT MEASURED.
			initModes: []string{"bypassPermissions", "default"},
			arm:       []probeOutcome{free, {ToolUseNames: []string{"Bash", "Bash"}}, free},
			want:      true,
		},
		{
			name:      "the downgrade never landed: init stayed in bypass",
			initModes: []string{"bypassPermissions", "bypassPermissions"},
			arm:       []probeOutcome{free, free, free},
			want:      false,
		},
		{
			name:      "the child never launched in bypass",
			initModes: []string{"default", "default"},
			arm:       []probeOutcome{gated, gated, gated},
			want:      false,
		},
		{
			name:      "a mode neither literal, reported after the downgrade",
			initModes: []string{"bypassPermissions", "acceptEdits"},
			arm:       []probeOutcome{free, free, free},
			want:      false,
		},
		{
			name:      "behavioural fallback: no init lines, turn 1 bypass-like and turn 2 default-like",
			initModes: nil,
			arm:       []probeOutcome{free, gated, free},
			want:      true,
		},
		{
			name:      "behavioural fallback with one init line only",
			initModes: []string{"bypassPermissions"},
			arm:       []probeOutcome{free, gated, free},
			want:      true,
		},
		{
			name: "behavioural fallback refuses a child that never behaved like bypass",
			// The trap this row exists for: turn 2 looks downgraded, but turn 1 was
			// gated too, so the flag never took and this is #1595's `enable` case
			// wearing a re-escalation's name.
			initModes: nil,
			arm:       []probeOutcome{gated, gated, gated},
			want:      false,
		},
		{
			name:      "behavioural fallback refuses a turn 2 that stayed bypass-like",
			initModes: nil,
			arm:       []probeOutcome{free, free, free},
			want:      false,
		},
		{
			name:       "behavioural fallback refuses controls that do not discriminate",
			initModes:  nil,
			arm:        []probeOutcome{free, free, free},
			ctlDefault: []probeOutcome{free, free, free},
			want:       false,
		},
		{
			name:      "an arm that produced no turns at all",
			initModes: nil,
			arm:       nil,
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			def, byp := tc.ctlDefault, tc.ctlBypass
			if def == nil {
				def = ctlDefault
			}
			if byp == nil {
				byp = ctlBypass
			}

			got, reason := reescalateDowngrade(tc.initModes, tc.arm, def, byp)
			if got != tc.want {
				t.Errorf("reescalateDowngrade(...) = %v, want %v (reason: %s)", got, tc.want, reason)
			}
			// A gate whose reason is empty cannot report AC 2's "with the reason",
			// and an unexplained NOT MEASURED costs a whole live run to diagnose.
			if reason == "" {
				t.Error("reason is empty; the run has nothing to report for this outcome")
			}
		})
	}
}

// TestReescalateArms_CarryTheShapeTheDirectionsClassify pins the arm table against
// the direction table. They are two literals that have to agree, and a
// disagreement is silent: an arm without a second target mode still runs, still
// writes a fixture and still gets classified — against a re-escalation that never
// happened.
func TestReescalateArms_CarryTheShapeTheDirectionsClassify(t *testing.T) {
	t.Parallel()

	measure := reescalateArm(reescalateArmMeasure)
	if !measure.launchYOLO {
		t.Error("the measurement arm must launch WITH --dangerously-skip-permissions; " +
			"a re-escalation on a child that never had bypass is #1595's already-answered question")
	}
	if measure.targetMode != reescalateDefaultMode || measure.secondTargetMode != reescalateBypassMode {
		t.Errorf("the measurement arm requests %q then %q, want %q then %q",
			measure.targetMode, measure.secondTargetMode, reescalateDefaultMode, reescalateBypassMode)
	}

	// #1595's negative, AC 3: without the launch flag and asking straight up.
	enable := reescalateArm(reescalateArmEnable)
	if enable.launchYOLO || enable.targetMode != reescalateBypassMode || enable.secondTargetMode != "" {
		t.Errorf("the %s arm must reproduce #1595's negative — no launch flag, one request for %q; "+
			"got launchYOLO=%v targetMode=%q secondTargetMode=%q",
			reescalateArmEnable, reescalateBypassMode, enable.launchYOLO,
			enable.targetMode, enable.secondTargetMode)
	}

	// A control that sends a request is not a control.
	for _, name := range []string{reescalateArmDefault, reescalateArmBypass} {
		ctl := reescalateArm(name)
		if ctl.targetMode != "" || ctl.secondTargetMode != "" {
			t.Errorf("control arm %q sends a control request (%q, %q); it would no longer be the "+
				"posture the measurement arms are classified against",
				name, ctl.targetMode, ctl.secondTargetMode)
		}
	}
	if reescalateArm(reescalateArmBypass).launchYOLO == reescalateArm(reescalateArmDefault).launchYOLO {
		t.Error("the two controls launch in the same posture, so no comparison against them " +
			"can discriminate anything")
	}

	// Every direction must name arms that exist, or the live test classifies a nil
	// record against a missing control.
	known := make(map[string]bool, len(reescalateArmNames))
	for _, n := range reescalateArmNames {
		known[n] = true
	}
	for _, dir := range reescalateDirections {
		for _, n := range []string{dir.arm, dir.appliedControl, dir.failedControl} {
			if !known[n] {
				t.Errorf("direction %q names arm %q, which is not in reescalateArmNames", dir.arm, n)
			}
		}
		if dir.readTurn < 0 || dir.readTurn > 2 {
			t.Errorf("direction %q reads turn index %d; the drive sequence produces three turns",
				dir.arm, dir.readTurn)
		}
	}
}
