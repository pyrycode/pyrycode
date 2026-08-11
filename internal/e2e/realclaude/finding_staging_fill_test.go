//go:build e2e_realclaude

package realclaude

// The fill: how the three transcript-side fields of finOutcomeStaging
// (`finOutcomeStaging`) are read out of a run's own JSONL, and the
// content guard that keeps the reading about the command the rig staged rather
// than about whichever Bash call the model happened to make first.
//
// This file reaches no verdict about pyry and takes no measurement. Everything
// here runs offline against a JSONL file the test writes under a t.TempDir()
// HOME: no live claude, no credentials, no daemon, no subject process, no FIFO,
// no process-table read, no env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestFinTranscript' -v ./internal/e2e/realclaude/
//
// # Three fields, and only three
//
// finOutcomeStaging holds eight. Three are readable from a transcript and this
// file fills exactly those: BashIssued, IssuedCommand, TriggerFired. The other
// five stay the caller's — StagedCommand is rig-authored, RendezvousDone comes
// from the FIFO, and PinScanErrored / PinMatchCount / PinWantCount come from the
// during-turn ps scan. finTranscriptReading is that boundary as a type: the
// composition returns a value from which the other five are unreachable, so
// "the fill neither reads nor invents them" is structural rather than asserted.
//
// # The defect this guards, on both sides
//
// probeWaitForBashToolUse returns the
// FIRST Bash tool_use regardless of input.command — a #1223 code-review SHOULD
// FIX that shipped unfixed, guarded caller-side by #1230
// (background_reach_probe_test.go:433-448) rather than by editing the shared
// rig. This file generalises that guard, and it guards a PAIR:
//
//   - Value-side: a decoy Bash call the model makes first captures the whole
//     measurement, and the gate answers stage-command-not-staged about a run that
//     staged correctly.
//   - Key-side: the trigger reading comes from probeWaitForToolResult(<id>), so a
//     fill that selects the staged call for its COMMAND but keeps the first
//     call's ID has moved the same defect one step downstream — the record then
//     says "the staged command's trigger fired" about the decoy's tool_result.
//
// So the scan's unit is the pair (tool_use id, command), never a bare command,
// and finTranscriptBashCall is that pair.
//
// # Nothing here normalises
//
// The gate compares IssuedCommand against StagedCommand as opaque bytes
// (finding_staging_gate_test.go:152-157). A fill that trimmed, unquoted or
// canonicalised what the model issued would make a real mismatch compare equal
// and reach the pass-through — defeating the arm the gate ranks first precisely
// so that "the model ran the wrong thing" is not filed under "our trigger is
// broken". The bytes encoding/json decoded are the bytes returned: no trimming,
// unquoting, splitting, shell-lexing, path resolution or execution anywhere on
// this path.
//
// # Reused, not rebuilt
//
// Every projection off an envelope is shipped and called, never re-derived:
// parseContentBlocks for the ordered blocks,
// probeToolUseInput + reachToolUseCommand
// (`reachToolUseCommand`) for the command, reachBackgroundHandle
// (:1051) for timedOutAfterMs, and both shipped waiters for the polling. The
// ordered scan across calls is the only new reading.

import (
	"encoding/json"
	"testing"
	"time"
)

// --- the readings ---------------------------------------------------------------

// finTranscriptBashCall is one Bash tool_use as the ordered scan found it.
//
// THE ID AND THE COMMAND TRAVEL TOGETHER, and that is the type's whole job: the
// command decides WHICH call the fill is about and the id is what the trigger
// reading is keyed off, so a producer that returned only the command would leave
// every downstream reading pointing at the first call.
//
// NO JSON TAGS, for the reason finOutcomeStaging states at itself
// (finding_staging_gate_test.go:141-157): Command is verbatim model output, and
// a tag is the first step toward publishing it into a public issue.
type finTranscriptBashCall struct{ ToolUseID, Command string }

// finTranscriptReading is the three transcript-side fields of finOutcomeStaging,
// and only those three. The caller assigns them onto its own record at the call
// site, in the open — there is deliberately no merge method and this type is
// deliberately not a finOutcomeStaging, so the five caller-side fields are not
// reachable from anything the fill returns.
//
// NO JSON TAGS, same rule and same reason as above: IssuedCommand is verbatim
// model output.
type finTranscriptReading struct {
	// BashIssued records THAT a Bash call was issued, IssuedCommand what it was.
	// The two are separate readings and must stay separate: collapsing them —
	// reporting "no call matched the staged command" as "no call was issued" —
	// makes a false statement about a run where the model demonstrably issued one.
	BashIssued    bool
	IssuedCommand string
	// TriggerFired is a claim ABOUT THE STAGED COMMAND, read from the selected
	// call's tool_result and from timedOutAfterMs alone. Zero-values to false,
	// which is a failure arm — the safe direction.
	TriggerFired bool
}

// --- the fill ---------------------------------------------------------------------

// finTranscriptScanBash returns every Bash tool_use in the transcript, in file
// order, id paired with command. Two tool_use blocks on one assistant line are
// two entries, in block order: parseContentBlocks returns the blocks in order.
//
// A block whose input fails to decode still COUNTS AS A CALL, with its command
// read as "" — probeToolUseInput returns (nil, false, "") on a bad envelope and
// reachToolUseCommand(nil) returns "". That is the honest reading: a call was
// issued and what it was is unreadable. "" never equals a non-empty staged
// command, so such a block is a fallback candidate but never a match.
func finTranscriptScanBash(t *testing.T, workdir, sessionID string) []finTranscriptBashCall {
	t.Helper()
	var calls []finTranscriptBashCall
	for _, e := range ReadJSONL(t, workdir, sessionID) {
		if e.Kind != "assistant" {
			continue
		}
		blocks, err := parseContentBlocks(e.Raw)
		if err != nil {
			// Per-line skip, as both shipped waiters do.
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" || b.Name != "Bash" || b.ID == "" {
				continue
			}
			input, _, _ := probeToolUseInput(e.Raw, b.ID)
			calls = append(calls, finTranscriptBashCall{
				ToolUseID: b.ID,
				Command:   reachToolUseCommand(input),
			})
		}
	}
	return calls
}

// finTranscriptSelect is THE CONTENT GUARD: the call whose command is exactly
// the staged one, else the first call. ok is false only for an empty slice.
//
// Byte equality, the same rule the gate's identity arm applies — this function
// does not soften it and neither may a caller. The fallback to calls[0] is
// deliberate and deterministic: when nothing matched, the reading is still "a
// call was issued", and reporting the first call's command keeps that row
// distinguishable at the reading level from a block whose input was unreadable.
//
// Pure over its input and takes no *testing.T, which is what lets the guard be
// proven — and its removal be graded — without a file on disk.
func finTranscriptSelect(calls []finTranscriptBashCall, staged string) (finTranscriptBashCall, bool) {
	if len(calls) == 0 {
		return finTranscriptBashCall{}, false
	}
	for _, c := range calls {
		if c.Command == staged {
			return c, true
		}
	}
	return calls[0], true
}

// finTranscriptSelectBash waits for a Bash call to exist, then for the staged
// one to appear, to ONE shared deadline. It returns the selection and whether
// any Bash call was issued at all.
//
// The shipped waiter answers the second return and nothing else. Its id is bound
// inside the if statement so it is UNREFERENCEABLE below: the first-match id is
// not merely unused downstream, there is no name for it there. That is the
// key-side guard made structural.
//
// The poll loop is not redundant with the waiter. probeWaitForBashToolUse
// returns as soon as ANY Bash tool_use lands, and an envelope "lags the
// subprocess by a couple of seconds", so a single scan at that moment answers on
// flush ORDER rather than on content: a decoy that flushed first would produce
// stage-command-not-staged on a run that staged correctly — the same wrong
// verdict the first-match defect produces, reached by another route. One shared
// deadline keeps the whole selection bounded by timeout rather than by 2x it.
// Offline the file is complete before the call, so the loop returns on its first
// pass.
func finTranscriptSelectBash(t *testing.T, workdir, sessionID, staged string,
	timeout time.Duration) (finTranscriptBashCall, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	if id, _ := probeWaitForBashToolUse(t, workdir, sessionID, time.Until(deadline)); id == "" {
		return finTranscriptBashCall{}, false
	}
	for {
		// The selection's own ok is discarded on purpose: the wait above has already
		// established that a Bash call exists, and THAT is the BashIssued reading. A
		// scan that momentarily saw none would yield the zero call, which reads as a
		// call whose command is unreadable — the same honest direction as an
		// undecodable input, and never as "no call was issued".
		call, _ := finTranscriptSelect(finTranscriptScanBash(t, workdir, sessionID), staged)
		if call.Command == staged || !time.Now().Before(deadline) {
			return call, true
		}
		time.Sleep(probePollInterval)
	}
}

// finTranscriptTriggerFired reports whether the trigger fired for ONE tool_use
// id: timedOutAfterMs present on that id's tool_result. A nil result is the
// deadline expiring with the call still open, which IS the did-not-fire signal
// (background_trigger_probe_test.go:790-793).
//
// Two deliberate omissions:
//
//   - The handle-present boolean is not conjoined. The reading is from
//     timedOutAfterMs alone; a conjunction would make it depend on a second field.
//   - tool_use.input.run_in_background is not consulted. It corroborates the
//     MODEL-SET path (docs/knowledge/codebase/1223.md:87-88) — the exact path this
//     probe must exclude — so admitting it as a second trigger signal would count
//     a run that never exercised the expiry lever.
//
// Presence, not value, is the signal: reachBackgroundHandle hands the number
// back as a string, so "5000" and a hypothetical "0" both read as fired. #1223
// measured 5000 on the expiry path and absence otherwise.
func finTranscriptTriggerFired(t *testing.T, workdir, sessionID, toolUseID string,
	timeout time.Duration) bool {
	t.Helper()
	raw := probeWaitForToolResult(t, workdir, sessionID, toolUseID, timeout)
	if raw == nil {
		return false
	}
	_, timedOut, _ := reachBackgroundHandle(raw)
	return timedOut != ""
}

// finTranscriptFill is the composition: it fills exactly the three
// transcript-side fields and never fails a test. Like the gate, an instrument
// reading is a datum, not a reason to abort a turn.
//
// The deadlines are parameters rather than the live constants because both
// waiters poll to expiry before returning empty: a live caller passes
// probeToolUseDeadline / probeToolResultDeadline, and an offline row passes
// milliseconds for a result already on disk.
//
// The command comparison in step 3 is the same string comparison the gate
// performs, in a second place, for a DIFFERENT question, and both must stay. The
// gate's decides the verdict; this one decides what the fill is entitled to
// claim. TriggerFired is a claim about the staged command — reading it off a
// call that is not the staged command would put a behavioural claim about the
// wrong process into the record, and the gate would never consult it anyway
// because its identity arm returns first. false is the zero value the record's
// own comment names as the safe direction. Live, it also drops a pointless
// 45-second wait on a run that has already lost.
func finTranscriptFill(t *testing.T, workdir, sessionID, staged string,
	toolUseTimeout, resultTimeout time.Duration) finTranscriptReading {
	t.Helper()
	call, issued := finTranscriptSelectBash(t, workdir, sessionID, staged, toolUseTimeout)
	if !issued {
		return finTranscriptReading{}
	}
	r := finTranscriptReading{BashIssued: true, IssuedCommand: call.Command}
	if call.Command == staged {
		r.TriggerFired = finTranscriptTriggerFired(t, workdir, sessionID, call.ToolUseID, resultTimeout)
	}
	return r
}

// --- fixtures ---------------------------------------------------------------------

// finTranscriptTestDeadline is what every row passes for both waiter deadlines.
// probePollInterval is 200 ms, so a waiter that finds nothing costs one sleep
// instead of the live 30 s / 45 s.
const finTranscriptTestDeadline = 10 * time.Millisecond

// The three commands the rows issue. All stand-ins: nothing here is parsed,
// resolved or executed, so no fixture needs a real path — and one carrying a
// real path would put an operator filesystem path into a test file for nothing
// (finding_staging_gate_test.go:370-375).
const (
	// finTranscriptDecoyCommand is the Bash call the model makes that the rig never
	// staged — the first-match defect's payload.
	finTranscriptDecoyCommand = "echo a Bash call the rig never staged"
	// finTranscriptAwkwardCommand is AC5's round-trip subject: leading and trailing
	// whitespace, embedded double quotes and a backslash. A fill that trims,
	// unquotes or canonicalises loses byte equality against it.
	finTranscriptAwkwardCommand = `  sh -c "printf %s \"held\"; exit 0"  `
)

// The tool_use ids the rows use. Ids are printable in a failure message —
// unlike either command — so they are what an assertion names.
const (
	finTranscriptDecoyID  = "toolu_decoy"
	finTranscriptStagedID = "toolu_staged"
)

// finTranscriptUserTextLine is a user line carrying no tool_result at all, for
// the row whose transcript holds no Bash call.
const finTranscriptUserTextLine = `{"type":"user","message":{"content":[{"type":"text","text":"nothing was run"}]}}`

// finTranscriptBashBlock builds one Bash tool_use content block.
// run_in_background is a REQUEST-side flag the fill must not consult; a row sets
// it to prove the fill ignores it.
func finTranscriptBashBlock(toolUseID, command string, runInBackground bool) map[string]any {
	input := map[string]any{"command": command}
	if runInBackground {
		input["run_in_background"] = true
	}
	return map[string]any{"type": "tool_use", "id": toolUseID, "name": "Bash", "input": input}
}

// finTranscriptAssistantLine marshals content blocks into one assistant line.
//
// MARSHALLED, never concatenated: a command carrying whitespace, quotes and a
// backslash has to reach the file byte-exactly, and a hand-escaped literal that
// was subtly wrong would make the round-trip row prove nothing while still
// passing.
func finTranscriptAssistantLine(t *testing.T, blocks ...map[string]any) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type":    "assistant",
		"message": map[string]any{"content": blocks},
	})
	if err != nil {
		t.Fatalf("marshal assistant line: %v", err)
	}
	return string(line)
}

// finTranscriptResultLine builds the tool_result user line, with toolUseResult
// as a SIBLING of message (background_reach_probe_test.go:1412-1421).
// timedOutAfterMs is omitted when the argument is "" — the model-set path — and
// emitted as a JSON number otherwise, which is the shape reachBackgroundHandle
// decodes.
func finTranscriptResultLine(t *testing.T, toolUseID, backgroundTaskID, timedOutAfterMs string) string {
	t.Helper()
	result := map[string]any{"backgroundTaskId": backgroundTaskID, "interrupted": false}
	if timedOutAfterMs != "" {
		result["timedOutAfterMs"] = json.Number(timedOutAfterMs)
	}
	line, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{"content": []map[string]any{
			{"type": "tool_result", "tool_use_id": toolUseID},
		}},
		"toolUseResult": result,
	})
	if err != nil {
		t.Fatalf("marshal tool_result line: %v", err)
	}
	return string(line)
}

// finTranscriptStagedCaller is the five caller-side fields at their STAGED
// values, so the only thing that differs between a pass-through row and a
// failure row is what the fill read. A caller-side outcome appearing in this
// file therefore means the fill wrote a field it does not own.
func finTranscriptStagedCaller(staged string) finOutcomeStaging {
	return finOutcomeStaging{
		StagedCommand:  staged,
		RendezvousDone: true,
		PinScanErrored: false,
		PinMatchCount:  1,
		PinWantCount:   1,
	}
}

// --- tests --------------------------------------------------------------------------

// TestFinTranscriptSelect proves the content guard over synthetic pairs: no
// file, no waiter, no clock. The rows are scenarios rather than shapes, and the
// third is the one AC2's mutation kills.
func TestFinTranscriptSelect(t *testing.T) {
	staged := finOutcomeHoldCommand
	decoy := finTranscriptBashCall{ToolUseID: finTranscriptDecoyID, Command: finTranscriptDecoyCommand}
	wanted := finTranscriptBashCall{ToolUseID: finTranscriptStagedID, Command: staged}
	other := finTranscriptBashCall{ToolUseID: "toolu_other", Command: "cat /some/other/path"}

	tests := []struct {
		name  string
		calls []finTranscriptBashCall
		want  finTranscriptBashCall
		wantO bool
	}{
		{
			name:  "no Bash call at all is the only not-ok",
			calls: nil,
		},
		{
			name:  "one call that is not the staged one is still a call that was issued",
			calls: []finTranscriptBashCall{decoy},
			want:  decoy,
			wantO: true,
		},
		{
			name:  "a decoy first and the staged call second selects the second",
			calls: []finTranscriptBashCall{decoy, wanted},
			want:  wanted,
			wantO: true,
		},
		{
			name:  "the staged call first and a decoy second selects the first",
			calls: []finTranscriptBashCall{wanted, decoy},
			want:  wanted,
			wantO: true,
		},
		{
			name:  "two calls and neither matches falls back to the first, deterministically",
			calls: []finTranscriptBashCall{decoy, other},
			want:  decoy,
			wantO: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := finTranscriptSelect(tc.calls, staged)
			if ok != tc.wantO {
				t.Errorf("ok: got %t, want %t", ok, tc.wantO)
			}
			if got.ToolUseID != tc.want.ToolUseID {
				t.Errorf("selected tool_use id: got %q, want %q — the id is what the trigger "+
					"reading is keyed off, so selecting the right command under the wrong id "+
					"moves the first-match defect one step downstream",
					got.ToolUseID, tc.want.ToolUseID)
			}
			// Commands are compared, never printed: both are captured-string class
			// (finding_staging_gate_test.go:141-157).
			if got.Command != tc.want.Command {
				t.Errorf("selected command differs from the row's expectation (got %d byte(s), "+
					"want %d); neither string is printed", len(got.Command), len(tc.want.Command))
			}
		})
	}
}

// finTranscriptFillCase is one transcript, the command the rig staged, and what
// the gate must answer once the fill's three fields are assigned onto an
// otherwise fully-staged caller record.
type finTranscriptFillCase struct {
	name   string
	lines  []string
	staged string
	// The reading itself, asserted alongside the verdict: two rows can share an
	// outcome value while making different statements about the run.
	wantBashIssued bool
	wantCommand    string
	want           string
}

func finTranscriptFillCases(t *testing.T) []finTranscriptFillCase {
	t.Helper()
	staged := finOutcomeHoldCommand
	return []finTranscriptFillCase{
		{
			name: "a decoy first and the staged call second",
			lines: []string{
				finTranscriptAssistantLine(t,
					finTranscriptBashBlock(finTranscriptDecoyID, finTranscriptDecoyCommand, false)),
				// The decoy's result carries the handle WITHOUT timedOutAfterMs, so a fill
				// that kept the first call's id reads the trigger as never fired.
				finTranscriptResultLine(t, finTranscriptDecoyID, "bg_decoy", ""),
				finTranscriptAssistantLine(t,
					finTranscriptBashBlock(finTranscriptStagedID, staged, false)),
				finTranscriptResultLine(t, finTranscriptStagedID, "bg_staged", "5000"),
			},
			staged:         staged,
			wantBashIssued: true,
			wantCommand:    staged,
			want:           finOutcomeReadyToClassify,
		},
		{
			name: "a decoy Bash call and nothing else",
			lines: []string{
				finTranscriptAssistantLine(t,
					finTranscriptBashBlock(finTranscriptDecoyID, finTranscriptDecoyCommand, false)),
				finTranscriptResultLine(t, finTranscriptDecoyID, "bg_decoy", "5000"),
			},
			staged: staged,
			// BashIssued stays TRUE. A fill that collapsed the two readings would answer
			// stage-no-bash-call about a run where the model demonstrably issued one.
			wantBashIssued: true,
			wantCommand:    finTranscriptDecoyCommand,
			want:           finOutcomeCommandNotStaged,
		},
		{
			name: "no Bash call was issued at all",
			lines: []string{
				finTranscriptAssistantLine(t, map[string]any{"type": "text", "text": "nothing to run"}),
				finTranscriptUserTextLine,
			},
			staged:         staged,
			wantBashIssued: false,
			wantCommand:    "",
			want:           finOutcomeNoBashCall,
		},
		{
			name: "two tool_use blocks on one assistant line, the staged one second",
			lines: []string{
				finTranscriptAssistantLine(t,
					finTranscriptBashBlock(finTranscriptDecoyID, finTranscriptDecoyCommand, false),
					// run_in_background is present and must not be admitted as a second trigger
					// signal: it corroborates the model-set path, which is the path this probe
					// excludes.
					finTranscriptBashBlock(finTranscriptStagedID, staged, true)),
				// The DECOY's result carries timedOutAfterMs; the staged call's does not. A
				// fill keyed off the first call's id reads 5000 and answers ready-to-classify.
				finTranscriptResultLine(t, finTranscriptDecoyID, "bg_decoy", "5000"),
				finTranscriptResultLine(t, finTranscriptStagedID, "bg_staged", ""),
			},
			staged:         staged,
			wantBashIssued: true,
			wantCommand:    staged,
			want:           finOutcomeTriggerDidNotFire,
		},
		{
			name: "a staged command carrying whitespace, quotes and a backslash",
			lines: []string{
				finTranscriptAssistantLine(t,
					finTranscriptBashBlock(finTranscriptStagedID, finTranscriptAwkwardCommand, false)),
				finTranscriptResultLine(t, finTranscriptStagedID, "bg_staged", "5000"),
			},
			staged:         finTranscriptAwkwardCommand,
			wantBashIssued: true,
			wantCommand:    finTranscriptAwkwardCommand,
			want:           finOutcomeReadyToClassify,
		},
		{
			name: "the staged call with no tool_result at all",
			lines: []string{
				finTranscriptAssistantLine(t,
					finTranscriptBashBlock(finTranscriptStagedID, staged, false)),
			},
			staged:         staged,
			wantBashIssued: true,
			wantCommand:    staged,
			want:           finOutcomeTriggerDidNotFire,
		},
	}
}

// TestFinTranscriptFill drives the composition end to end through a real JSONL
// the test writes at the session's own path, and asserts BOTH the reading and
// the verdict the gate reaches from it.
func TestFinTranscriptFill(t *testing.T) {
	reached := make(map[string]bool, len(finOutcomeValues()))
	for _, tc := range finTranscriptFillCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			workdir := WithWorktree(t)
			writeFixtureLines(t, workdir, testSessionID, tc.lines...)

			r := finTranscriptFill(t, workdir, testSessionID, tc.staged,
				finTranscriptTestDeadline, finTranscriptTestDeadline)

			if r.BashIssued != tc.wantBashIssued {
				t.Errorf("BashIssued: got %t, want %t — \"no call matched the staged command\" and "+
					"\"no call was issued\" are separate readings and collapsing them makes a "+
					"false statement about the run", r.BashIssued, tc.wantBashIssued)
			}
			// Compared, never printed: both operands are captured-string class
			// (finding_staging_gate_test.go:141-157). Lengths only.
			if r.IssuedCommand != tc.wantCommand {
				t.Errorf("IssuedCommand differs from the row's expectation (got %d byte(s), want "+
					"%d); neither string is printed — a fill that trims, unquotes or "+
					"canonicalises loses byte equality here", len(r.IssuedCommand), len(tc.wantCommand))
			}

			s := finTranscriptStagedCaller(tc.staged)
			s.BashIssued = r.BashIssued
			s.IssuedCommand = r.IssuedCommand
			s.TriggerFired = r.TriggerFired

			got := finOutcomeStagingGate(s)
			if got.Value != tc.want {
				t.Errorf("gate value: got %q, want %q (TriggerFired=%t)", got.Value, tc.want, r.TriggerFired)
			}
			reached[got.Value] = true
		})
	}

	for _, v := range []string{
		finOutcomeNoBashCall,
		finOutcomeCommandNotStaged,
		finOutcomeTriggerDidNotFire,
		finOutcomeReadyToClassify,
	} {
		if !reached[v] {
			t.Errorf("no row produced %s, so the fill's route to it is unproven — a row silently "+
				"retargeted by an edit leaves it untested rather than red", v)
		}
	}
	// The three caller-side outcomes, in the other direction. Every row's caller
	// fields come from finTranscriptStagedCaller unchanged, so one of these
	// appearing means the fill wrote a field it does not own.
	for _, v := range []string{
		finOutcomeRendezvousIncomplete,
		finOutcomePinScanErrored,
		finOutcomePinCountUnexpected,
	} {
		if reached[v] {
			t.Errorf("a row produced %s, which is decided by a caller-side field the fill neither "+
				"reads nor fills — the fill has written outside the three transcript-side "+
				"fields it owns", v)
		}
	}
}
