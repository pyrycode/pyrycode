//go:build e2e_realclaude

package realclaude

// The declarations one live `pyry agent-run` probe stages a turn from — the run's
// FIFO name, the prompt that asks claude for the hold command, the staged command
// literal itself and the run's two env deltas — plus an offline trap on each.
//
// The two deltas were once one per runner path. #1348 deleted the terminal-driving
// runner and made the variable that chose between them a no-op, so the deltas are
// now identical in effect and each has its own live caller. Collapsing them is a
// follow-up refactor, not done here.
//
// This file reaches no verdict about pyry and takes no measurement. It ships
// declarations and their traps: no live run, no pyry spawn, no real claude, no ps
// exec, no FIFO, no record assembly, no credentials, no env gate, no t.Skip, no
// clock, no goroutine, no filesystem access of any kind.
//
//	go test -race -tags e2e_realclaude -run '^TestFinLiveStage' -v ./internal/e2e/realclaude/
//
// # The byte-equality trap these declarations exist to satisfy
//
// finOutcomeStagingGate's identity arm is byte
// equality over two opaque strings and admits nothing else:
//
//	if s.IssuedCommand != s.StagedCommand || s.StagedCommand == ""
//
// IssuedCommand is the model's VERBATIM input.command; StagedCommand is whatever
// the rig says it staged. Declaring the rig's internal shell form — or anything
// carrying a stray trailing `.`, a nonce that drifted, or a `;` the system prompt
// told the model not to emit — makes every CORRECTLY staged run report
// stage-command-not-staged, with the rig looking correct and one live claude turn
// spent per attempt. The same equality is load-bearing one layer down:
// finTranscriptFill only reads the trigger result when call.Command == staged
// (`finTranscriptFill`), so a mismatched literal ALSO silently zeroes
// TriggerFired, which is a second failure arm from the same defect. That is why
// these declarations ship with offline traps: everything that can be made to go
// red offline should be, rather than on a burned turn.
//
// # This file execs nothing, and the check is the symbol list
//
// An `exec.` grep reads clean here by construction — the import set is fmt,
// strings and testing — so it proves nothing. The rule is the symbols, and
// TestFinOfflineFilesReachNoExecHelper is what runs it.
// FORBIDDEN in this file, each because it execs, spawns, blocks or reads the
// operator's environment INSIDE a helper where no grep of this file would see it:
//
//   - spawnProbePyry, holdProbeFIFO — spawn pyry / create a real FIFO. That is
//     #1340's job and is explicitly out of scope here.
//   - probeProcessSnapshot, pinScanArgv, tdnScan — each execs `ps` internally.
//   - WithWorktree, WithWorktreeAuthenticated — filesystem setup and credentials;
//     nothing here needs either.
//   - t.TempDir() — yields an operator filesystem path, for the recorded reason at
//     `finOutcomeHoldCommand`. The fixture path below is a synthetic
//     const instead.
//   - os.Getenv, os.Environ, os.Setenv — nothing here reads or writes the process
//     environment at all any more. Until #1348 two traps in this file set an
//     ambient PYRY_USE_STREAMJSON to prove each delta named its own runner; that
//     variable now selects nothing and those traps were retired on 2026-08-16, so
//     the last environment write in this file went with them. THIS BAN IS A
//     CREDENTIAL GUARD, not tidiness: this rig's process environment carries
//     CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY, and a debugging session that
//     dumps the environment here would put both in a log.
//
// There is no ps flag to get wrong because there is no ps: no -E, no -Eww, no BSD
// `eww`. Those flags dump CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY.
//
// # No type is declared, and no captured bytes leave here
//
// The prompt and the command are captured-string class on a live run — the
// command embeds a t.TempDir()-derived path. This file writes no artifact, logs
// nothing, and formats no Detail at all, so trailDetail's reachMaxCommandBytes cap
// (`trailGateInput`) never applies here. Failure messages COMPARE the two
// strings and report LENGTHS, following TestFinTranscriptFill.
//
// No struct is declared, so finTranscriptReading's no-json-tags rule
// (`finTranscriptReading`) is satisfied structurally rather than by
// inspection — there is nothing here to tag. An edit that adds a type carrying
// either string inherits that rule.
//
// # Not env-gated, and must not become so
//
// Nothing here needs a Claude login. TestMain branches
// only on GO_TEST_HELPER_PROCESS and otherwise runs m.Run(), so a regression here
// is red under `make e2e-realclaude` on a credential-free machine. Note that
// `go vet` and `staticcheck` in `make check` run WITHOUT -tags e2e_realclaude, so
// neither analyses this file; `make e2e-realclaude` is the gate.

import (
	"fmt"
	"strings"
	"testing"
)

// --- the run's FIFO name ----------------------------------------------------------

// finLiveStageFIFOName names this run's rendezvous FIFO. It is DISTINCT from every
// shipped one as a substring in both directions, for the reason shipped on
// reachFIFOName: a concurrently running sibling probe's `cat` must never satisfy
// this run's content match.
//
// Derived at 26d83b7 from `rg -n 'FIFOName *=|FIFOPath *=' internal/e2e/realclaude/`
// — eight taken names, and this one is disjoint from all eight both ways. Note the
// near miss the second alternative was added for: finLivePinFIFOPath
// (`finLivePinFIFOPath`) is a FIFOPath const, so the older FIFOName-only
// recipe misses it. RE-RUN the two-alternative recipe at your own HEAD before
// adding a ninth name — a blocker landing between refinement and implementation is
// how the seven-name list went stale in the first place.
// TestFinLiveStageFIFONameIsDisjointFromEveryShippedName traps the eight; it
// cannot trap a ninth.
const finLiveStageFIFOName = "fin-live-stage-hold"

// --- the prompts and the staged literal -------------------------------------------

// finLiveStageSystemPrompt is #1223's shipped system prompt, referenced rather
// than re-typed, so the bytes are identical by construction — "use the Bash tool
// exactly once, run the command verbatim, do NOT chain commands with && or ;, do
// NOT comment, and do NOT do anything else" (`probeSystemPrompt`).
//
// It is declared here because the consuming driver hands spawnProbePyry BOTH a
// prompt path and a system path (`spawnProbePyry`): declaring
// only the user half would leave #1340 to invent the other. It carries NO trap —
// an assertion that it equals its own definition would prove nothing, and saying
// so is more honest than shipping one.
const finLiveStageSystemPrompt = probeSystemPrompt

// finLiveStageCommand is the run's staged hold command: the BARE command the model
// is asked to emit verbatim, and the exact bytes the rig reports as
// finOutcomeStaging.StagedCommand. It is what the gate's identity arm
// (`finOutcomeStagingGate`) compares against claude's verbatim
// input.command, so it must be the model's side of the contract and not the rig's
// internal shell form.
//
// It splices probeHeldCommandName rather than re-typing "cat". #1340's liveness
// read calls probeHasCommand(…, probeHeldCommandName) (`runProbeRep`), so a rig
// that staged one verb while the liveness check looked for another would be a
// silent drift; the splice makes a rename a build break. That is a COUPLING —
// this rig's verb follows #1223's — and it was chosen rather than inherited:
// the alternative is that the staged literal and the liveness read drift apart
// with no symptom.
//
// It is NOT finOutcomeHoldCommand. That is a
// gate fixture, deliberately the wrong shape: its `sh -c … ; exit 0` carries a `;`
// into a string the model is asked to reproduce byte-for-byte, against a system
// prompt that forbids chaining.
func finLiveStageCommand(fifoPath string) string {
	return probeHeldCommandName + " " + fifoPath
}

// finLiveStagePrompt asks the model for exactly that command, inside backticks.
//
// It follows probePrompt's DELIMITED form — verb inside the backticks, the
// sentence's period outside them — with one change: the WHOLE command is
// interpolated, not just the path, so the prompt and the staged literal come
// from one source instead of being written twice.
// TestFinLiveStagePromptStagesTheDeclaredCommand recovers the command from this
// string by its delimiter, which is what makes that a second reading.
//
// Deliberately NOT bgIdlePrompt's form, where the sentence's period ABUTS the
// path the model must reproduce byte-for-byte and no delimiter exists to extract
// on.
//
// NO CACHE-BUSTER. probePrompt carries none and fired 7/7 live in #1223; no
// staleness has been observed on this path, so bgIdlePrompt's trailing run=%d is
// not adopted. If #1340 ever needs one it goes OUTSIDE the backticks — everything
// inside them is the byte-for-byte staged literal, and the extraction trap is what
// catches a nonce that drifted inside.
func finLiveStagePrompt(fifoPath string) string {
	return fmt.Sprintf("Use the Bash tool to run `%s`. Do nothing else.",
		finLiveStageCommand(fifoPath))
}

// --- the env delta ----------------------------------------------------------------

// finLiveStageEnvDelta is the environment this run adds to the `pyry agent-run`
// process. Two entries, and it is the first delta that must name both:
//
//   - BASH_DEFAULT_TIMEOUT_MS=5000 — the settled #1223 auto-background trigger,
//     measured live against claude 2.1.220 as firing on every run carrying it.
//     Both runners hand claude pyry's own environment verbatim, so setting it on
//     the parent is the whole plumbing story: no production change, no forwarding
//     list. Shipped siblings: reachEnvDelta,
//     tdnEnvDelta — neither names a runner.
//   - PYRY_USE_STREAMJSON=0 — named EXPLICITLY rather than left unset, because
//     reachRunnerPathFromEnv reads the ambient os.Getenv FIRST
//     (`reachRunnerPathFromEnv`) and only then lets the delta override
//     it, so an empty delta makes the downstream env-side runner reading a reading
//     of the OPERATOR'S SHELL. Shipped sibling: finRecordEnvDelta() — which
//     carries no trigger.
//
// The two entries are THIS FILE'S OWN LITERALS rather than a concatenation of the
// two shipped deltas: composing them would make this rig's environment silently
// follow two other tickets' edits. The trap asserts containment against both
// shipped identifiers instead, so an edit to either goes red here loudly.
//
// A FUNC, not a var, following finRecordEnvDelta:
// a package-level []string is mutable by every test in the package, and this one
// is read by two downstream tickets.
func finLiveStageEnvDelta() []string {
	return []string{"BASH_DEFAULT_TIMEOUT_MS=5000", "PYRY_USE_STREAMJSON=0"}
}

// finLiveStageStreamEnvDelta is the environment a run on the HEADLESS stream-json
// path adds to the `pyry agent-run` process. The same two keys the delta above
// names, with the runner selector flipped:
//
//   - BASH_DEFAULT_TIMEOUT_MS=5000 — the settled #1223 auto-background trigger,
//     unchanged and carried for the same reason: both runners hand claude pyry's
//     own environment verbatim, so setting it on the parent is the whole plumbing
//     story.
//   - PYRY_USE_STREAMJSON=1 — the exact string runAgentRun dispatches
//     runAgentRunStreamRunner on (cmd/pyry/`runAgentRun`). Named EXPLICITLY
//     for exactly the reason its =0 sibling is, and that reason SURVIVES THE FLIP
//     unchanged: reachRunnerPathFromEnv reads the ambient os.Getenv FIRST
//     (`reachRunnerPathFromEnv`) and only then lets the delta override
//     it, so an empty delta makes the downstream env-side runner reading a reading
//     of the OPERATOR'S SHELL. Only the value differs.
//
// SEPARATE LITERALS, NEVER DERIVED FROM finLiveStageEnvDelta(). Not a clone with
// one entry replaced, not an append, not a wrap: the property bought is that an
// edit to either delta cannot silently change the other, and every derivation
// destroys exactly that. Drift between the two is caught by assertion instead —
// the trap below asserts containment against the shipped identifiers and asserts
// finRecordEnvDelta()'s PYRY_USE_STREAMJSON=0 is ABSENT here.
//
// A FUNC, not a var, for the reason the sibling's doc gives above: a package-level
// []string is mutable by every test in the package.
//
// # Choosing this delta chooses a permission posture
//
// Recorded HERE because here is where the choice is made; finLiveRunStage carries
// the same paragraph for the reader who arrives from the driver's side.
// runAgentRunStreamRunner is the stream path's sole production caller and passes
// yolo=true (`runAgentRunStreamRunner`), which emits
// --dangerously-skip-permissions (`permissionArgs`). The ptyrunner default
// instead trust-marks the workdir and writes a per-spawn deny-default settings
// JSON (`runAgentRunPty`). A caller passing THIS delta stages its live turn
// under the first posture.
//
// On the tool surface the repo's own recorded position is relayed rather than a
// fresh claim asserted: `buildStreamRunnerClaudeArgs` records --allowed-tools as the
// authoritative tool gate under YOLO, with the blast radius bounded by it rather
// than by the trust dialog — and this rig passes --allowed-tools=Bash
// (spawnProbePyry). So the flip changes the
// gate's MECHANISM; on the repo's position it does not change its WIDTH. That is
// not a claim of equivalence, and it is not a claim that the stream path is
// ungated.
//
// NEITHER POSTURE IS EXERCISED BY ANYTHING SHIPPED HERE. This file stages no turn
// and #1349 has no live caller; #1353 owns the first live spawn under this delta.
func finLiveStageStreamEnvDelta() []string {
	return []string{"BASH_DEFAULT_TIMEOUT_MS=5000", "PYRY_USE_STREAMJSON=1"}
}

// --- the fixture ------------------------------------------------------------------

// finLiveStageFixtureFIFOPath is SYNTHETIC and never a real path. It is passed to
// the two formatters above and to nothing else: never opened, stat'd,
// canonicalised, joined or executed, so path traversal, TOCTOU and symlink
// following are structurally inapplicable rather than merely unaddressed — the
// same posture finLivePinFIFOPath carries.
//
// It must not come from t.TempDir() or os.Getenv: either would put an operator
// filesystem path into a test file for nothing, the recorded reason at
// `finOutcomeHoldCommand`. The live path comes from #1340, which
// joins finLiveStageFIFOName onto its own t.TempDir(). The name is SPLICED here so
// a rename tracks.
const finLiveStageFixtureFIFOPath = "/tmp/pyry-fin-live-stage/" + finLiveStageFIFOName

// --- the second reading -----------------------------------------------------------

// finLiveStageCommandFromPrompt returns the text between the prompt's only two
// backticks. ok is false unless EXACTLY two are present, which is what makes the
// delimiter unambiguous rather than merely conventional.
//
// This is the independent recovery the prompt/literal trap turns on: the producing
// side is a fmt.Sprintf and this side is a delimiter scan, so the two do not share
// an implementation. A hand-copied second literal in the test would prove nothing.
//
// It fails toward the SAFE direction. A caller that ignored ok and used the zero
// string would compare "" against the staged command and reach
// finOutcomeCommandNotStaged via the `|| StagedCommand == ""` guard
// (`finOutcomeStagingGate`) — a failure arm, never the pass-through.
func finLiveStageCommandFromPrompt(prompt string) (string, bool) {
	if strings.Count(prompt, "`") != 2 {
		return "", false
	}
	first := strings.Index(prompt, "`")
	last := strings.LastIndex(prompt, "`")
	return prompt[first+1 : last], true
}

// --- the traps ------------------------------------------------------------------

// TestFinLiveStagePromptStagesTheDeclaredCommand is AC1: the staged literal is
// byte-identical to what the prompt asks the model to emit.
//
// The command is recovered FROM THE PROMPT STRING by its delimiter and compared to
// the declared literal — a second, independent reading. The producing side is a
// fmt.Sprintf; the recovering side is a delimiter scan, so the two do not share an
// implementation. A hand-copied second literal here would prove nothing, which is
// the whole reason finLiveStageCommandFromPrompt exists.
func TestFinLiveStagePromptStagesTheDeclaredCommand(t *testing.T) {
	path := finLiveStageFixtureFIFOPath
	prompt := finLiveStagePrompt(path)
	staged := finLiveStageCommand(path)

	got, ok := finLiveStageCommandFromPrompt(prompt)
	if !ok {
		t.Fatalf("the declared prompt carries no single backticked span (%d backtick(s) in a "+
			"%d-byte prompt); the delimiter is what makes the extraction below a second "+
			"reading rather than a hand-copied literal",
			strings.Count(prompt, "`"), len(prompt))
	}
	// Compared, never printed: both operands are captured-string class on a live
	// run (`finOutcomeStaging`). Lengths only, following TestFinTranscriptFill —
	// the habit has to survive contact with the first caller that passes a real
	// path.
	if got != staged {
		t.Fatalf("the prompt's backticked command differs from the declared staged literal "+
			"(got %d byte(s) from the prompt, want %d); neither string is printed. Every "+
			"correctly-staged live run would report %s against this mismatch",
			len(got), len(staged), finOutcomeCommandNotStaged)
	}

	// The extractor is not a pass-through. Without this, an extractor that
	// returned its whole argument would satisfy the round-trip above whenever the
	// two happened to agree.
	if len(got) >= len(prompt) || got == prompt {
		t.Errorf("the extraction returned the whole prompt (%d byte(s)) rather than the "+
			"delimited span (%d byte(s)): a pass-through would make the round-trip above "+
			"vacuous", len(prompt), len(got))
	}

	// Exactly one backticked span, so the delimiter is unambiguous rather than
	// merely conventional.
	if n := strings.Count(prompt, "`"); n != 2 {
		t.Errorf("the declared prompt carries %d backtick(s), want exactly 2 — one span, "+
			"unambiguously delimited", n)
	}
	for _, tc := range []struct {
		name   string
		prompt string
	}{
		// bgIdlePrompt's undelimited form:
		// no backtick at all, and the sentence period abuts the path.
		{name: "no delimiter", prompt: "Use the Bash tool to run cat /tmp/x/hold. Do nothing else."},
		{name: "one backtick", prompt: "Use the Bash tool to run `cat /tmp/x/hold. Do nothing else."},
		{name: "three backticks", prompt: "Use the Bash tool to run `cat` /tmp/x/`hold. Do nothing else."},
	} {
		if _, ok := finLiveStageCommandFromPrompt(tc.prompt); ok {
			t.Errorf("%s: the extractor accepted a prompt with %d backtick(s); it must accept "+
				"exactly 2, so a delimiter that drifted is red here rather than on a burned "+
				"turn", tc.name, strings.Count(tc.prompt, "`"))
		}
	}

	// The command carries no backtick of its own — otherwise the delimiter is
	// ambiguous and the extraction above is accidental.
	if strings.Contains(staged, "`") {
		t.Errorf("the staged command contains a backtick, which makes the prompt's delimiter "+
			"ambiguous (command is %d byte(s); it is not printed)", len(staged))
	}

	// Delimited, not punctuated. This is the property that separates probePrompt's
	// form, where the sentence period falls OUTSIDE the backticks, from
	// bgIdlePrompt's, where a trailing `.` abuts text the model must reproduce
	// byte-for-byte.
	if strings.HasSuffix(staged, ".") {
		t.Errorf("the staged command ends with a period (%d byte(s), not printed): the "+
			"model would have to reproduce the sentence's punctuation for the gate's byte "+
			"equality to hold", len(staged))
	}

	// It derives from the run's path, so both sides come from one source rather
	// than being written twice.
	if !strings.Contains(staged, finLiveStageFixtureFIFOPath) {
		t.Errorf("the staged command does not contain the FIFO path it was formatted from "+
			"(%d byte(s) against a %d-byte path): the prompt and the literal must be "+
			"produced from one source", len(staged), len(finLiveStageFixtureFIFOPath))
	}

	// It is a BARE command. probeSystemPrompt tells the model "do NOT chain
	// commands with && or ;", so a staged literal carrying one asks the model to
	// break the instruction whose verbatim echo the byte equality compares.
	// finOutcomeHoldCommand's `sh -c … ; exit 0` shape
	// (`finOutcomeHoldCommand`) is the thing being avoided here — it is a
	// gate fixture, deliberately the wrong shape, and is NOT asserted against:
	// staged != finOutcomeHoldCommand is trivially true and would prove nothing.
	for _, bad := range []string{";", "&&", "|", "sh -c"} {
		if strings.Contains(staged, bad) {
			t.Errorf("the staged command contains %q, so it is not the bare command the "+
				"system prompt asks for (command is %d byte(s), not printed)", bad, len(staged))
		}
	}
}

// TestFinLiveStageFIFONameIsDisjointFromEveryShippedName is AC2: this run's FIFO
// name collides with no shipped one, as a substring in BOTH directions.
//
// The rule and its reason are shipped on reachFIFOName: a concurrently running
// sibling probe's `cat` must never satisfy this run's content match.
//
// The rows reference the shipped constants BY IDENTIFIER. All eight are file-local
// consts in this package under the same e2e_realclaude build tag, so the reference
// compiles and a rename breaks the build rather than rotting silently. A table
// that re-typed the eight string values would be exactly the hand-copied list this
// criterion exists to replace. (The row LABELS are strings for the failure message
// only; every compared value comes from an identifier.)
//
// THE LIMIT, stated rather than overclaimed: this traps a collision at the time of
// writing and any later edit to one of the eight. It cannot catch a NINTH name
// added elsewhere afterwards, which is why re-running
// `rg -n 'FIFOName *=|FIFOPath *=' internal/e2e/realclaude/` stays in the loop —
// note the two alternatives: finLivePinFIFOPath is a FIFOPath const, and a
// FIFOName-only recipe misses it.
//
// finLivePinFIFOPath is compared AS SHIPPED. It is a path
// (/tmp/pyry-fin-live-pin/live-pin-hold), not a bare name, and a name disjoint
// from the whole path is disjoint from its basename — no basename splitting. Note
// also that live-pin-hold is synthetic: #1338's file spawns nothing and creates
// no FIFO, as finding_live_pin_test.go's own header records, so it cannot
// collide in a live process table. It is in the taken set for the textual rule
// alone.
func TestFinLiveStageFIFONameIsDisjointFromEveryShippedName(t *testing.T) {
	taken := []struct {
		ident string
		value string
	}{
		{ident: "probeFIFOName", value: probeFIFOName},
		{ident: "reachFIFOName", value: reachFIFOName},
		{ident: "tdnFIFOName", value: tdnFIFOName},
		{ident: "fifoLiveFIFOName", value: fifoLiveFIFOName},
		{ident: "trailRigFIFOName", value: trailRigFIFOName},
		{ident: "finStageFIFOName", value: finStageFIFOName},
		{ident: "bgIdleFIFOName", value: bgIdleFIFOName},
		{ident: "finLivePinFIFOPath", value: finLivePinFIFOPath},
	}

	// A row deleted during an edit must be red rather than silently narrowing the
	// sweep.
	if len(taken) != 8 {
		t.Fatalf("the taken set has %d row(s), want 8 — re-derive with "+
			"`rg -n 'FIFOName *=|FIFOPath *=' internal/e2e/realclaude/` and restore the row",
			len(taken))
	}

	// NO EMPTY-NAME GUARD, deliberately. strings.Contains(s, "") is true, so an
	// accidentally empty finLiveStageFIFOName fails the second direction on every
	// row — the safe direction. A guard that special-cased it would silence
	// exactly the case that should shout.
	for _, tc := range taken {
		if strings.Contains(finLiveStageFIFOName, tc.value) {
			t.Errorf("%s = %q is a substring of finLiveStageFIFOName = %q: a concurrent %s "+
				"probe's `cat` could satisfy this run's content match",
				tc.ident, tc.value, finLiveStageFIFOName, tc.ident)
		}
		if strings.Contains(tc.value, finLiveStageFIFOName) {
			t.Errorf("finLiveStageFIFOName = %q is a substring of %s = %q: this run's `cat` "+
				"could satisfy the %s probe's content match",
				finLiveStageFIFOName, tc.ident, tc.value, tc.ident)
		}
	}
}

// TestFinLiveStageDeltasCarryTheTrigger is what survives of the two runner-naming
// traps #1348 made vacuous, and it keeps the half of them that can still fail.
//
// WHAT WAS REMOVED AND WHY. Until 2026-08-16 pyry chose between a terminal-driving
// runner and the stream-json runner on PYRY_USE_STREAMJSON, and the two traps that
// used to live here asserted that each staging delta named the runner it intended.
// #1348 deleted the terminal runner and made the variable a no-op that nothing
// reads. Those assertions then compared a test-local string formatter against
// itself: they could not fail, and they ran green on every pass while proving
// nothing. Deleted rather than repaired, because the distinction they existed to
// police no longer exists.
//
// WHAT IS KEPT, and it is not tidiness. Both deltas must still carry the settled
// #1223 auto-background trigger, and each must carry that and nothing else. The
// named failure mode is live on the surviving path: a third entry of
// CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1 would SUPPRESS the very trigger the rig
// depends on, measured in #1223 as `Exit code 143 / Command timed out after 5s`
// with the command dead, and every run would then report a did-not-fire outcome
// and burn a turn. That guard is runner-agnostic and outlived the runner.
//
// The two deltas are now identical in effect, since their only difference was the
// dead selector. Collapsing them into one is a follow-up refactor with live
// callers to update, deliberately not done here: this change removes assertions
// that cannot fail and changes nothing that runs.
func TestFinLiveStageDeltasCarryTheTrigger(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delta []string
	}{
		{"pty-era delta", finLiveStageEnvDelta()},
		{"stream delta", finLiveStageStreamEnvDelta()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Asserted against the shipped identifier rather than a re-typed
			// string, so an edit to reachEnvDelta goes red here pointing at the
			// shared assumption instead of drifting apart silently.
			for _, want := range reachEnvDelta {
				if !finLiveStageDeltaHas(tc.delta, want) {
					t.Errorf("the delta does not carry reachEnvDelta's %q: that is the settled "+
						"#1223 trigger, and without it the auto-background trigger never fires", want)
				}
			}
			if len(tc.delta) != 2 {
				t.Errorf("the delta has %d entry(ies) %v, want exactly 2: a third that suppressed "+
					"the trigger would make every run report %s",
					len(tc.delta), tc.delta, finOutcomeTriggerDidNotFire)
			}
		})
	}
}

// finLiveStageDeltaHas reports whether the delta carries an exact KEY=VALUE entry.
// Exact, never a prefix match: PYRY_USE_STREAMJSON=0 and PYRY_USE_STREAMJSON=1 are
// opposite instructions and a prefix test would accept either.
func finLiveStageDeltaHas(delta []string, entry string) bool {
	for _, kv := range delta {
		if kv == entry {
			return true
		}
	}
	return false
}
