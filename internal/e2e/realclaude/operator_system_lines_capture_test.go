//go:build e2e_realclaude

package realclaude

// Evidence capture for #2255 — what claude's four unmapped operator-facing
// `system` lines actually look like on the wire, and which of them did not appear.
//
// # What is unknown, and why the record is shaped around absence
//
// emitSystemSubtype has no arm for `informational`, `local_command_output`,
// `notification` or `commands_changed`. Its default returns false, and the caller's
// ignoredLineTypes branch then drops the line silently — a `system` line cannot
// reach the surfaced unrecognized tier at all, so nothing today would report one of
// these arriving. Their field sets are on record only as documentation and as Agent
// SDK type declarations; no committed fixture in this repo has ever held one.
// #2256, #2257, #2258 and #2259 each map one of them, and systemTaskStartedLine's
// rule — the field set is exactly what the committed capture shows and nothing
// invented — leaves them nothing to work from until these bytes land.
//
// ABSENCE IS A RESULT HERE, not a failure, and that is the whole design pressure on
// this file. It is only a result when the record can tell "claude sent nothing" from
// "the trigger never fired", so every trigger ships a witness that it fired and only
// a subtype with a fired trigger and no line is recorded unobserved. A subtype whose
// trigger did not fire is recorded INCONCLUSIVE and blocks promotion: a fixture
// carrying it as "unobserved" would be exactly the confusion this ticket exists to
// remove.
//
// Two of the three triggers carry live risk the ticket names. Whether a
// UserPromptSubmit hook runs under --dangerously-skip-permissions is unmeasured, and
// #2138's header records that whether a slash-command turn ever CLOSES is unmeasured
// too. The first is why the hook writes its own witness; the second is why every
// slash-command phase waits on oslcapAwaitQuietTurn rather than on a `result`.
//
// `notification` has no known trigger. It is watched across the whole session and
// its record says exactly that, rather than naming a trigger that does not exist.
//
// # Does the line decode into streamLine? — and why no mirror is declared here
//
// A system/permission_denied line carries `message` as a STRING where streamLine
// declares *streamMessage, so encoding/json fails the whole line and the subtype
// needed consumePermissionDeniedLine to be mapped at all. Whether each of these four
// has that shape decides whether its mapping needs the same gate, so the record
// carries the answer per observed line.
//
// It is answered by the SHIPPED parser, through parseOne, and not by a local copy of
// streamLine. For a `system` line the mapping is exact. The two gates inside
// consumeLine's decode-failure branch are dropHarnessProseLine, which requires type
// `user`, and consumePermissionDeniedLine, which requires subtype
// `permission_denied`; neither can claim one of these four, so a line that fails the
// decode reaches emitUnrecognized unconditionally and one that decodes is dropped
// silently by the ignoredLineTypes branch. Zero events means it decoded; an
// Unrecognized at the undecodable site means it did not.
// TestOslcapDecodeVerdictMatchesTheShippedParser is that reasoning, executed.
//
// # What is deliberately NOT inherited
//
// #1260's FIFO rendezvous and #2089's tpcapHoldFIFO. Nothing here holds anything
// open, and two of #2089's three repair legs were FIFO repairs. #2247's
// tncapAwaitSubtype IS reused, for the one wait that looks for a subtype rather than
// for quiescence.
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine, and nothing else — the gate is
// the FIXTURE'S ABSENCE, argued at TestRealClaude_OperatorSystemLinesCapture. To
// force a re-capture at a new claude version, over an existing fixture:
//
//	PYRY_PROBE_OPERATOR_SYSTEM_LINES=1 go test -tags e2e_realclaude -timeout 20m -v \
//	  -run '^TestRealClaude_OperatorSystemLinesCapture$' ./internal/e2e/realclaude/
//
// Read WHICH skip: "fixture already exists" is the steady state, while a skip out of
// WithWorktreeAuthenticated means the machine has no claude login and the evidence
// was not produced. The TestOslcap* tests below run offline, with no claude and no
// gate.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// oslcapEnableEnv FORCES a re-capture when the fixture already exists. It is not the
// gate — see the gate comment in TestRealClaude_OperatorSystemLinesCapture.
const oslcapEnableEnv = "PYRY_PROBE_OPERATOR_SYSTEM_LINES"

// The version is spliced into the path rather than repeated, so the filename cannot
// drift from the release the record vouches for. fixtureWorthy refuses to write under
// a mismatched name, so a claude upgrade is a loud instruction to re-capture rather
// than a fixture quietly describing another release.
const (
	oslcapFixtureVersion = "2.1.259"
	oslcapFixturePath    = "testdata/operator_system_lines_v" + oslcapFixtureVersion + ".json"
)

// Every file-local identifier takes the oslcap prefix, for the reason #1260's header
// gives: siblings add files to this package concurrently and a branch-overlap check
// does not catch a same-package identifier collision. `opcap` would have been the
// obvious short form and is deliberately not used — it is a substring of `dropcap`,
// so it would make every identifier in this family un-greppable.
const (
	oslcapTicket         = "2255"
	oslcapWorkdirName    = "oslcap-work"
	oslcapRigDirName     = "oslcap-rig"
	oslcapSettingsName   = "settings.json"
	oslcapHookName       = "hook.sh"
	oslcapWitnessName    = "hook-witness.log"
	oslcapRecordName     = "oslcap-record.json"
	oslcapArtifactPrefix = "pyry-2255-capture-*"
	oslcapModel          = "haiku"
	// The project slash command written mid-session. A fixed name rather than a
	// nonce-bearing one, so a later system/init inventory can be searched for the
	// same literal; the per-run value lives in the command's BODY instead.
	oslcapCommandName = "oslcap-probe"
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from the
	// sibling captures' so a record can never be mistaken for one of theirs, and it
	// is the id claude echoes back, which is what makes dropcapRedactor's session_id
	// class able to catch it.
	oslcapSessionID = "b71e4f26-0d3a-4c58-9a17-6e2f8c05d419"
)

// The four subtypes. Spelled as literals rather than imported from streamsup,
// because the record is EVIDENCE about claude's wire shape and a census keyed on
// production constants would agree with them by construction instead of measuring
// anything.
const (
	oslcapSubtypeInformational      = "informational"
	oslcapSubtypeLocalCommandOutput = "local_command_output"
	oslcapSubtypeCommandsChanged    = "commands_changed"
	oslcapSubtypeNotification       = "notification"
)

// The rig's own strings. The marker is what the hook matches on, the reason is what
// it prints to stderr, and the reason is also the ONLY rig-authored string this
// capture could recognise inside a system/informational line.
const (
	oslcapBlockMarker = "PYRY-2255-BLOCKME"
	oslcapBlockReason = "blocked by the pyry 2255 capture rig: this prompt carries the rig marker"
	oslcapTokenPrefix = "OSLCAP-CMD-OK-"
	oslcapHookBlocked = "blocked"
	oslcapHookPassed  = "passed"
)

const (
	oslcapPhasePreamble     = "preamble"
	oslcapPhaseBlock        = "block"
	oslcapPhaseLiveness     = "liveness"
	oslcapPhaseInventory    = "inventory"
	oslcapPhaseCommand      = "command"
	oslcapPhaseLocalCommand = "local-command"
	oslcapPhaseSettle       = "settle"
)

const (
	oslcapFired            = "fired"
	oslcapDidNotFire       = "did-not-fire"
	oslcapInstrumentBroken = "instrument-broken"
)

const (
	oslcapTerminatedResult  = "result"
	oslcapTerminatedQuiet   = "quiescence"
	oslcapTerminatedBudget  = "budget"
	oslcapTerminatedSubtype = "subtype-observed"
	oslcapTerminatedUnsent  = "write-failed"
)

// The decode verdicts, decided by the shipped parser — see this file's header.
const (
	oslcapDecodes       = "decodes"
	oslcapDoesNotDecode = "does-not-decode"
)

// The JSON type of a `message` value, read from the raw bytes. A typed decode cannot
// answer this: the shapes worth naming are exactly the ones it rejects.
const (
	oslcapJSONAbsent  = "absent"
	oslcapJSONNull    = "null"
	oslcapJSONString  = "string"
	oslcapJSONNumber  = "number"
	oslcapJSONBoolean = "boolean"
	oslcapJSONObject  = "object"
	oslcapJSONArray   = "array"
	oslcapJSONInvalid = "invalid"
)

const (
	oslcapPoll           = 500 * time.Millisecond
	oslcapRunExitWait    = 30 * time.Second
	oslcapInitWait       = 60 * time.Second
	oslcapBlockBudget    = 75 * time.Second
	oslcapLivenessBudget = 2 * time.Minute
	oslcapCommandBudget  = 2 * time.Minute
	oslcapCostBudget     = 90 * time.Second
	oslcapInventoryWatch = 25 * time.Second
	oslcapSettleWatch    = 10 * time.Second
	// How long the stream must stay silent, after at least one line of a turn has
	// arrived, before the turn counts as over. This is the arm that exists because a
	// blocked prompt and a slash-command turn may never emit a `result` at all.
	oslcapQuiet = 20 * time.Second
)

// oslcapArgs is the YOLO interactive shape every capture in this family uses, plus
// the one flag this ticket adds. It is a function rather than a package var because
// the settings path is minted at runtime under the per-test temp $HOME.
func oslcapArgs(settingsPath string) []string {
	return []string{"--model", oslcapModel, "--dangerously-skip-permissions", "--settings", settingsPath}
}

const oslcapSpawnShapeDelta = "The YOLO interactive shape — see dropcapSpawnShapeDelta for what " +
	"production's non-yolo spawn adds and what that implies for system/init — PLUS a --settings file, " +
	"which no other capture in this family passes. That file is this ticket's entire informational " +
	"trigger: it declares one UserPromptSubmit command hook and nothing else, no permissions object and " +
	"no defaultMode, so it adds a hook and changes no posture. It reaches claude through " +
	"streamsup.Config.Args, not through internal/sessions, and it is scoped to THIS child: it is not the " +
	"operator's own settings file and cannot outlive the per-test temp $HOME. Whether the presence of a " +
	"hook alters anything else on this surface is UNMEASURED, not ruled out; settings_content and " +
	"hook_script_content carry exactly what was written."

const oslcapLimitations = "One session, one spawn shape, one claude version, one model (" + oslcapModel +
	"), and three triggers driven once each. A subtype recorded unobserved did not appear on THIS " +
	"session under THIS trigger, which is not the same as claude never sending it — read the subtype's " +
	"note, which names which reading its own row supports. Cross-version, cross-model and cross-surface " +
	"stability are UNMEASURED. The notification row supports no absence claim at all: nothing here " +
	"provokes it, because no trigger for it is known. Auto-provoked variants of the other three " +
	"(a hook on some other event, a local command other than the one driven, an inventory change from " +
	"outside the workdir) are UNMEASURED and may carry different fields."

const oslcapRedactionRationale = "Inherited whole from #1260 (see dropcapRedactionRationale): a fresh " +
	"non-git workdir under a per-test temp $HOME, rig-authored prompts, no os.Environ() read into the " +
	"record, the declared dropcapRedactor substitution table over every string, and dropcapScanner as a " +
	"fail-closed deny-scan over the marshalled record. " +
	"WHAT THIS CAPTURE SPECIFICALLY CAN CARRY, stated rather than left to be inferred by whoever decides " +
	"to paste this record into a public issue. FIRST, the local-command output is a usage and spend " +
	"report. On a session of four short turns under a fresh temp home the figures describe the rig, but " +
	"the surface is the operator's account and the line may name a plan or a billing window. It is KEPT, " +
	"because a redacted local-command output would not be evidence of the shape the mapping ticket has " +
	"to declare. SECOND, an informational line is hook feedback: on the happy path it echoes the rig's " +
	"own block reason, and on a failing path it can echo the shell's error naming the hook script, which " +
	"is why the temp-home class and oslcapUnredactedPathFields both cover it. THIRD, a commands-changed " +
	"line is a full slash-command inventory, the operator-derived class dropcapRedactionRationale " +
	"already names for system/init, in a second dress. FOURTH, per-message uuid, tool_use_id and " +
	"parent_tool_use_id values SURVIVE, as they do in every committed capture in this directory. " +
	"The hook script deliberately writes NEITHER the payload claude hands it NOR any environment " +
	"variable: it appends one fixed word per invocation, so the credential the child inherits cannot " +
	"reach the witness file through it."

// --- the hook rig ------------------------------------------------------------

// oslcapRig is the three files the informational trigger needs, plus the two
// contents the record carries so the settings claude actually read are legible after
// the fact rather than transcribed from this file.
type oslcapRig struct {
	Dir          string
	SettingsPath string
	ScriptPath   string
	WitnessPath  string
	Script       string
	Settings     string
}

// oslcapShellQuote wraps s in single quotes, refusing any value that carries one.
//
// POSIX single quotes are the only quoting that makes a value fully literal, and
// they have no escape: a value carrying a quote closes the quoting and turns the
// remainder into shell source. There is no sanitising fallback here on purpose —
// every value this file quotes is either a compile-time constant or a path Go minted,
// so a refusal is a bug in the caller, not an input to be repaired.
func oslcapShellQuote(s string) (string, error) {
	if strings.Contains(s, "'") {
		return "", fmt.Errorf("oslcap: %d-byte value carries a single quote and cannot be safely "+
			"interpolated into shell source; the value is deliberately not printed", len(s))
	}
	return "'" + s + "'", nil
}

// oslcapHookScript renders the UserPromptSubmit hook.
//
// A hook is ARBITRARY CODE EXECUTION AS THE OPERATOR by design — writeMCPSettings'
// doc makes exactly that point about a file pyry hands claude as --settings — so the
// one place this file builds shell source enforces its own quoting rather than
// resting on today's inputs happening to be metacharacter-free.
//
// It writes one fixed word per invocation and nothing else. The payload claude hands
// it on stdin carries the prompt, the cwd and the session id, and the record needs
// none of that: the question is whether the hook ran and whether it blocked. It also
// reads no environment variable, so the credential the child inherits has no path
// into the witness file.
//
// Exit 2 is the blocking code for UserPromptSubmit and stderr is the reason. Any
// other non-zero code is non-blocking, so the prompt would run normally and the
// trigger would silently not fire — which is what
// TestOslcapHookScriptBlocksOnlyTheMarkedPrompt pins.
func oslcapHookScript(witnessPath, marker, reason string) (string, error) {
	witness, err := oslcapShellQuote(witnessPath)
	if err != nil {
		return "", fmt.Errorf("hook witness path: %w", err)
	}
	quotedMarker, err := oslcapShellQuote(marker)
	if err != nil {
		return "", fmt.Errorf("hook marker: %w", err)
	}
	quotedReason, err := oslcapShellQuote(reason)
	if err != nil {
		return "", fmt.Errorf("hook reason: %w", err)
	}
	return "#!/bin/sh\n" +
		"# Rig-authored by pyry's #2255 capture. Blocks ONLY a prompt carrying the rig\n" +
		"# marker, so later turns on the same child still run. Records one word per\n" +
		"# invocation and never the payload.\n" +
		"payload=$(cat)\n" +
		"case \"$payload\" in\n" +
		"  *" + quotedMarker + "*)\n" +
		"    printf '%s\\n' " + oslcapHookBlockedLiteral + " >> " + witness + "\n" +
		"    printf '%s\\n' " + quotedReason + " >&2\n" +
		"    exit 2\n" +
		"    ;;\n" +
		"esac\n" +
		"printf '%s\\n' " + oslcapHookPassedLiteral + " >> " + witness + "\n" +
		"exit 0\n", nil
}

// The two verdict words as shell literals. Constants rather than a call to
// oslcapShellQuote, because they are this file's own and quoting them at runtime
// would add an error path that can never be taken.
const (
	oslcapHookBlockedLiteral = "'" + oslcapHookBlocked + "'"
	oslcapHookPassedLiteral  = "'" + oslcapHookPassed + "'"
)

// oslcapSettingsJSON renders the settings file: one UserPromptSubmit command hook,
// and nothing else.
//
// Deliberately NOT mcpSettingsFile's payload. That file exists to suppress two
// startup dialogs on the PTY path; this is the stream-json path, where the sibling
// captures spawn with no settings at all and never see a dialog. Adding a hook and
// changing no posture is what keeps this capture's spawn comparable to theirs.
//
// The marshal error is structurally unreachable — every field is a string or a slice
// of them — and returning an empty object rather than swallowing it makes an
// impossible failure loud at TestOslcapSettingsFileIsTheShapeClaudeReads instead of
// silent at a live gate.
func oslcapSettingsJSON(scriptPath string) string {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type matcher struct {
		Hooks []hook `json:"hooks"`
	}
	type settings struct {
		Hooks map[string][]matcher `json:"hooks"`
	}
	blob, err := json.MarshalIndent(settings{
		Hooks: map[string][]matcher{
			"UserPromptSubmit": {{Hooks: []hook{{Type: "command", Command: scriptPath}}}},
		},
	}, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(blob) + "\n"
}

// oslcapWriteHookRig creates the rig directory and its three files.
//
// Modes are set with an explicit Chmod after each write rather than left to
// os.WriteFile's perm argument alone, which is masked by the process umask: the
// settings file is executable policy claude reads, and a mode that depends on the
// runner's umask is a mode no test can pin.
//
// The witness is created here, empty, rather than by the script's first append, for
// the same reason — an append-created file takes the CHILD's umask.
func oslcapWriteHookRig(dir string) (oslcapRig, error) {
	rig := oslcapRig{
		Dir:          dir,
		SettingsPath: filepath.Join(dir, oslcapSettingsName),
		ScriptPath:   filepath.Join(dir, oslcapHookName),
		WitnessPath:  filepath.Join(dir, oslcapWitnessName),
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return rig, fmt.Errorf("create the rig dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return rig, fmt.Errorf("chmod the rig dir: %w", err)
	}

	script, err := oslcapHookScript(rig.WitnessPath, oslcapBlockMarker, oslcapBlockReason)
	if err != nil {
		return rig, err
	}
	rig.Script = script
	rig.Settings = oslcapSettingsJSON(rig.ScriptPath)

	for _, f := range []struct {
		path    string
		content string
		mode    os.FileMode
	}{
		{rig.ScriptPath, rig.Script, 0o700},
		{rig.SettingsPath, rig.Settings, 0o600},
		{rig.WitnessPath, "", 0o600},
	} {
		if err := os.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
			return rig, fmt.Errorf("write %s: %w", filepath.Base(f.path), err)
		}
		if err := os.Chmod(f.path, f.mode); err != nil {
			return rig, fmt.Errorf("chmod %s: %w", filepath.Base(f.path), err)
		}
	}
	return rig, nil
}

// oslcapHookVerdicts reads the witness back, in order. A missing or empty file means
// the hook never ran, which is a measurement and not an error: it is precisely the
// "the trigger never fired" case the whole record is built to separate from "claude
// sent nothing".
func oslcapHookVerdicts(witnessPath string) []string {
	blob, err := os.ReadFile(witnessPath)
	if err != nil {
		return nil
	}
	return strings.Fields(string(blob))
}

// --- the rig's prompts and files ---------------------------------------------

// oslcapBlockPrompt carries the marker the hook matches on. The prose is otherwise a
// trivial arithmetic request, so a run where the hook does NOT fire produces a short
// ordinary turn rather than anything expensive, and the nonce gives
// dropcapRedactor's prompt_nonce class something to substitute.
func oslcapBlockPrompt(nonce int64) string {
	return fmt.Sprintf("Reply with the single word ACK and nothing else. Do not use any tools. "+
		"%s run=%d", oslcapBlockMarker, nonce)
}

// oslcapLivenessPrompt carries NO marker, so the hook passes it. Its answer is what
// establishes that the child survived the blocked turn — AC 1's "later turns on the
// same child still run" — and it is the discriminator that separates a dead child
// from an inventory claude never re-read.
func oslcapLivenessPrompt(nonce int64) string {
	return fmt.Sprintf("Reply with the single word OK and nothing else. Do not use any tools. "+
		"run=%d", nonce)
}

// oslcapLocalCommandCandidates are the local, read-only, output-producing slash
// commands this rig will drive, in preference order, and the choice is made from the
// SESSION'S OWN init inventory rather than assumed.
//
// MEASURED, not guessed. The ticket names `/cost`, and `/cost` is first here because
// of that. But dropped_lines_v2.1.220.json's init line carries a 46-command inventory
// that does NOT include it, while it does include `usage`, `context` and three other
// usage-shaped names — so on this surface, at that release, `/cost` was not a command
// the session knew. Hardcoding it would have driven a turn claude answers as prose,
// recorded local_command_output as unobserved with a trigger that could not fire, and
// spent a whole live gate to learn a name.
//
// Every candidate is read-only and side-effect-free. `model` and `config` are
// deliberately absent: they mutate session state, and a capture that changes the
// thing it is measuring is not a capture.
var oslcapLocalCommandCandidates = []string{"cost", "usage", "context", "status"}

// oslcapPickLocalCommand returns the first candidate the session's init inventory
// carries. When it carries none, the FIRST candidate is driven anyway and ok is
// false: attempting the ticket's own command and recording that the session did not
// know it is a finding to route back, where silently skipping the phase would leave
// the row unexplained.
func oslcapPickLocalCommand(inventory []string) (string, bool) {
	for _, name := range oslcapLocalCommandCandidates {
		if oslcapContains(inventory, name) {
			return name, true
		}
	}
	return oslcapLocalCommandCandidates[0], false
}

// oslcapSlashPrompt is the bare command, with no nonce and no surrounding prose:
// #2138 establishes that a message whose text is exactly the slash command is what a
// real claude runs as one.
func oslcapSlashPrompt(name string) string { return "/" + name }

// oslcapCommandPrompt invokes the project command written mid-session.
const oslcapCommandPrompt = "/" + oslcapCommandName

func oslcapCommandToken(nonce int64) string {
	return fmt.Sprintf("%s%d", oslcapTokenPrefix, nonce)
}

// oslcapCommandBody is the project slash command's file content. Answering it
// requires nothing but echoing a token, so the claude-side inventory witness costs
// one short turn and the reply carries no prose worth recording.
func oslcapCommandBody(token string) string {
	return "Reply with exactly the following token and nothing else. Do not use any tools, " +
		"do not explain, do not add punctuation.\n\n" + token + "\n"
}

// oslcapWriteCommandFile writes the project slash command into the workdir's
// .claude/commands, which is the inventory change the commands_changed trigger is.
// It returns the path and the byte count, both of which are the rig-side half of
// that trigger's witness.
func oslcapWriteCommandFile(workdir, token string) (string, int, error) {
	dir := filepath.Join(workdir, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, fmt.Errorf("create the commands dir: %w", err)
	}
	path := filepath.Join(dir, oslcapCommandName+".md")
	body := oslcapCommandBody(token)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", 0, fmt.Errorf("write the command file: %w", err)
	}
	return path, len(body), nil
}

// --- reading the wire --------------------------------------------------------

// oslcapDecodeVerdict answers AC 5's first half using the SHIPPED parser, and no
// mirror of streamLine is declared anywhere in this file.
//
// For a `system` line the mapping is exact, and this file's header carries the
// argument in full: the two gates inside consumeLine's decode-failure branch require
// type `user` and subtype `permission_denied` respectively, so neither can claim one
// of these four, and a line that fails the decode reaches emitUnrecognized
// unconditionally while one that decodes is dropped silently by the ignoredLineTypes
// branch.
func oslcapDecodeVerdict(t *testing.T, raw []byte) (string, bool) {
	t.Helper()
	for _, ev := range parseOne(t, string(raw)) {
		if u, ok := ev.(turnevent.Unrecognized); ok && u.Site == turnevent.UnrecognizedUndecodable {
			return oslcapDoesNotDecode, false
		}
	}
	return oslcapDecodes, true
}

// oslcapMessageJSONType answers AC 5's second half from the RAW bytes. A typed decode
// cannot: the shapes worth naming — a string where a struct is declared, an array, a
// bare number — are exactly the ones it rejects, and by the time it has failed there
// is nothing left to report.
func oslcapMessageJSONType(raw []byte) string {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return oslcapJSONInvalid
	}
	value, ok := envelope["message"]
	if !ok {
		return oslcapJSONAbsent
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return oslcapJSONInvalid
	}
	switch c := trimmed[0]; {
	case c == '{':
		return oslcapJSONObject
	case c == '[':
		return oslcapJSONArray
	case c == '"':
		return oslcapJSONString
	case c == 't' || c == 'f':
		return oslcapJSONBoolean
	case c == 'n':
		return oslcapJSONNull
	case c == '-' || (c >= '0' && c <= '9'):
		return oslcapJSONNumber
	default:
		return oslcapJSONInvalid
	}
}

// oslcapTopLevelKeys is the cheap half of what the mapping tickets need: which keys a
// line carried, without any of their values. Sorted, so two records of the same shape
// compare.
func oslcapTopLevelKeys(raw []byte) []string {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	keys := make([]string, 0, len(envelope))
	for k := range envelope {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// oslcapInitSlashCommands returns the first system/init line's slash-command
// inventory. It is the claude-side half of the /cost trigger's witness: a command
// absent from this list is one the session does not know, and a turn naming it would
// be answered as prose.
func oslcapInitSlashCommands(lines []dropcapCaptured) []string {
	for _, c := range lines {
		if !c.Decoded || c.Type != "system" || c.Subtype != "init" {
			continue
		}
		var init struct {
			SlashCommands []string `json:"slash_commands"`
		}
		if err := json.Unmarshal(c.Raw, &init); err != nil {
			continue
		}
		return init.SlashCommands
	}
	return nil
}

// oslcapAssistantLines counts the assistant lines in the half-open window [from, to).
// On the cost phase a zero is consistent with the command having been handled
// locally and a non-zero with the text having been answered as prose — carried as
// DATA rather than as a gate, because reading it as a gate would make the witness
// depend on the thing it is supposed to witness.
func oslcapAssistantLines(lines []dropcapCaptured, from, to int) int {
	n := 0
	for i := from; i < to && i < len(lines); i++ {
		if lines[i].Decoded && lines[i].Type == "assistant" {
			n++
		}
	}
	return n
}

// oslcapAssistantCarriesToken reports whether any assistant line from `from` onward
// carries the token. It is searched in the RAW bytes, before redaction, because the
// token embeds the nonce and dropcapRedactor would have substituted it away.
//
// Only the boolean reaches the record. The assistant text itself never does: the
// design does not need it, and widening what a committed public artefact carries for
// a value nothing reads is the same mistake the hook script declines to make with
// its payload.
func oslcapAssistantCarriesToken(lines []dropcapCaptured, from int, token string) bool {
	for i := from; i < len(lines); i++ {
		if lines[i].Decoded && lines[i].Type == "assistant" && bytes.Contains(lines[i].Raw, []byte(token)) {
			return true
		}
	}
	return false
}

// oslcapAwaitQuietTurn waits out a turn WITHOUT depending on it closing.
//
// Three of this rig's four sends are a blocked prompt or a slash command, and
// #2138's header is explicit that whether such a turn ever closes is unmeasured and
// that a command replying with nothing is a plausible shape; blocking on a `result`
// would hang on exactly the cases this capture exists to observe and land no record.
//
// sentAt is the captured-line count at the moment the turn went out, so the
// quiescence arm can require that the turn produced SOMETHING before calling the
// stream quiet — otherwise a turn claude has not started answering yet reads as one
// that has finished. quiet and budget are parameters rather than the constants
// directly so the three exits can be proved offline in milliseconds.
func oslcapAwaitQuietTurn(recorder *dropcapRecorder, wantResults, sentAt int, quiet, budget time.Duration) string {
	deadline := time.Now().Add(budget)
	lastGrowth := time.Now()
	last := sentAt
	for {
		lines, _ := recorder.snapshot()
		if ccapResultCount(lines) >= wantResults {
			return oslcapTerminatedResult
		}
		if len(lines) != last {
			last = len(lines)
			lastGrowth = time.Now()
		}
		if len(lines) > sentAt && time.Since(lastGrowth) >= quiet {
			return oslcapTerminatedQuiet
		}
		if time.Now().After(deadline) {
			return oslcapTerminatedBudget
		}
		time.Sleep(oslcapPoll)
	}
}

// oslcapAwaitInit polls for the session's system/init line, which carries the
// slash-command inventory the /cost witness reads. Waiting for it rather than
// snapshotting whenever the first send happens to finish is what keeps that witness
// from being a race.
func oslcapAwaitInit(recorder *dropcapRecorder, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		lines, _ := recorder.snapshot()
		if len(oslcapInitSlashCommands(lines)) > 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(oslcapPoll)
	}
}

// --- the record --------------------------------------------------------------

// oslcapPhase is one window of the session. Five sends on one child means a line's
// own bytes cannot say which trigger produced it, so attribution is by recorder
// index: FirstLineIndex is the captured-line count at the moment the phase began.
type oslcapPhase struct {
	Name           string  `json:"name"`
	Prompt         string  `json:"prompt,omitempty"`
	Sent           bool    `json:"sent"`
	FirstLineIndex int     `json:"first_line_index"`
	LineCount      int     `json:"line_count"`
	TerminatedOn   string  `json:"terminated_on"`
	Seconds        float64 `json:"seconds"`
}

// oslcapPhaseFor names the phase a captured line belongs to. A line before the first
// send is preamble and says so: system/init lands there, and attributing it to the
// first phase would claim the hook produced it.
func oslcapPhaseFor(phases []oslcapPhase, index int) string {
	name := oslcapPhasePreamble
	for _, p := range phases {
		if index >= p.FirstLineIndex {
			name = p.Name
			continue
		}
		break
	}
	return name
}

// oslcapFrame is one captured line of one of the four subtypes. The payload half
// comes from dropcapMakeEntry so the base64 arm for invalid UTF-8 is shared rather
// than re-derived.
//
// events_emitted is the SHIPPED parser's verdict, carried as DATA rather than as a
// filter. Read it WITH decode_verdict, because the two non-zero cases are opposites:
// on a line that decodes, a non-zero count means the drop set has already moved under
// this ticket and the subtype is mapped; on one that does not, the single event is the
// Unrecognized the decode failure produced, which is the same event decode_verdict was
// read from.
type oslcapFrame struct {
	Index                   int      `json:"index"`
	Phase                   string   `json:"phase"`
	Type                    string   `json:"type"`
	Subtype                 string   `json:"subtype"`
	DecodesIntoStreamLine   bool     `json:"decodes_into_stream_line"`
	DecodeVerdict           string   `json:"decode_verdict"`
	MessageJSONType         string   `json:"message_json_type"`
	Keys                    []string `json:"keys"`
	PayloadLenBytesCaptured int      `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int      `json:"payload_len_bytes"`
	PayloadEncoding         string   `json:"payload_encoding"`
	Payload                 string   `json:"payload,omitempty"`
	PayloadB64              string   `json:"payload_b64,omitempty"`
	EventsEmitted           int      `json:"events_emitted"`
}

// oslcapSubtype is AC 4's answer for ONE of the four, emitted for all four whether or
// not observed. The note is what a reader of the fixture acts on, and naming WHICH
// reading an unobserved row is is the whole point of this ticket.
type oslcapSubtype struct {
	Subtype        string `json:"subtype"`
	Observed       bool   `json:"observed"`
	LineCount      int    `json:"line_count"`
	FrameIndices   []int  `json:"frame_indices"`
	Trigger        string `json:"trigger"`
	TriggerWitness string `json:"trigger_witness"`
	TriggerFired   bool   `json:"trigger_fired"`
	Conclusive     bool   `json:"conclusive"`
	Note           string `json:"note"`
}

// finish fills Conclusive and Note from what has been measured. An empty Trigger
// marks the subtype that HAS no trigger, which is a different thing from one whose
// trigger failed and must never be collapsed with it.
func (s *oslcapSubtype) finish() {
	if s.FrameIndices == nil {
		s.FrameIndices = []int{}
	}
	s.Conclusive = s.Observed || s.TriggerFired
	switch {
	case s.Observed:
		s.Note = fmt.Sprintf("observed: %d line(s) at frame indices %v, carried verbatim in frames. "+
			"The field set to map is exactly what those payloads show and nothing else",
			s.LineCount, s.FrameIndices)
	case s.Trigger == "":
		s.Note = "unobserved, and NOT an absence claim: this subtype has no known trigger, so it was " +
			"watched across the whole session and nothing here provoked it. A mapping ticket must not " +
			"read this row as claude never sending one — it says only that nothing in this session did"
	case s.TriggerFired:
		s.Note = fmt.Sprintf("unobserved with the trigger WITNESSED as fired (%s): claude sent no such "+
			"line on this session, so this is a finding about claude and the mapping ticket drops the "+
			"subtype rather than inventing its fields", s.TriggerWitness)
	default:
		s.Note = fmt.Sprintf("INCONCLUSIVE: the trigger did not fire (%s), so this run says nothing "+
			"about claude either way. This is the rig failing to provoke the thing it meant to measure, "+
			"to be routed back and re-run — never read as an absence", s.TriggerWitness)
	}
}

type oslcapRecord struct {
	Ticket          string   `json:"ticket"`
	ClaudeVersion   string   `json:"claude_version"`
	CapturedAt      string   `json:"captured_at"`
	IsCapture       bool     `json:"is_capture"`
	Model           string   `json:"model"`
	SpawnShape      []string `json:"spawn_shape"`
	SpawnShapeDelta string   `json:"spawn_shape_delta"`
	Workdir         string   `json:"workdir"`
	Prompts         []string `json:"prompts"`

	// The rig files exactly as written, so the settings claude actually read are
	// legible after the fact rather than transcribed from this file.
	SettingsContent    string `json:"settings_content"`
	HookScriptContent  string `json:"hook_script_content"`
	CommandFileContent string `json:"command_file_content"`
	CommandFilePath    string `json:"command_file_path"`

	Outcome       string `json:"outcome"`
	OutcomeDetail string `json:"outcome_detail"`

	Phases   []oslcapPhase   `json:"phases"`
	Subtypes []oslcapSubtype `json:"subtypes"`

	// The trigger witnesses. Without these an unobserved row cannot tell "claude sent
	// nothing" from "the trigger never fired", and only the first is a result.
	InitObserved                bool     `json:"init_observed"`
	HookInvocations             int      `json:"hook_invocations"`
	HookBlockedCount            int      `json:"hook_blocked_count"`
	HookVerdicts                []string `json:"hook_verdicts"`
	ChildAliveAfterBlock        bool     `json:"child_alive_after_block"`
	SlashCommandCountAtInit     int      `json:"slash_command_count_at_init"`
	LocalCommandChosen          string   `json:"local_command_chosen"`
	LocalCommandCandidates      []string `json:"local_command_candidates"`
	LocalCommandInInventory     bool     `json:"local_command_in_inventory"`
	ProbeCommandInInitInventory bool     `json:"probe_command_in_init_inventory"`
	LocalCommandTurnLines       int      `json:"local_command_turn_lines"`
	LocalCommandTurnAssistant   int      `json:"local_command_turn_assistant_lines"`
	CommandFileBytes            int      `json:"command_file_bytes"`
	CustomCommandHonoured       bool     `json:"custom_command_honoured"`

	// A content-free census of everything else on the wire — AC 4.
	LineTypeCensus   map[string]int `json:"line_type_census"`
	ToolCalls        []string       `json:"tool_calls"`
	ToolResultErrors int            `json:"tool_result_errors"`
	UndecodedLines   int            `json:"undecoded_lines"`

	LinesCaptured        int           `json:"lines_captured"`
	FrameCount           int           `json:"frame_count"`
	Frames               []oslcapFrame `json:"frames"`
	UnredactedPathFields []string      `json:"unredacted_path_fields"`

	FixtureStaged      bool   `json:"fixture_staged"`
	FixtureStageDetail string `json:"fixture_stage_detail"`

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

// oslcapSeedRecord returns the record's RIG-AUTHORED half: every field whose value is
// a compile-time constant of this file. The live probe seeds from here and fills the
// measured fields in.
//
// It is a function so the offline deny-scan net scans EXACTLY the bytes the probe
// puts in the record rather than a second copy of the same literals. A copy drifts,
// and the drift is silent until a live turn is spent: #2247's first live lap died on
// a rationale that spelled out the very prefixes dropcapFixedNeedles searches for,
// which failed the write closed and produced nothing.
//
// Keep this constants-only. A field whose value depends on the run belongs at the
// call site, because the net cannot judge what it cannot know offline.
func oslcapSeedRecord() *oslcapRecord {
	return &oslcapRecord{
		Ticket:                 oslcapTicket,
		IsCapture:              true,
		Model:                  oslcapModel,
		SpawnShapeDelta:        oslcapSpawnShapeDelta,
		Prompts:                []string{},
		Phases:                 []oslcapPhase{},
		Subtypes:               []oslcapSubtype{},
		HookVerdicts:           []string{},
		LocalCommandCandidates: oslcapLocalCommandCandidates,
		Frames:                 []oslcapFrame{},
		UnredactedPathFields:   []string{},
		RedactionRationale:     oslcapRedactionRationale,
		CredentialScanSkipped:  []string{},
		Limitations:            oslcapLimitations,
	}
}

func (rec *oslcapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
}

// unfiredTriggers names the triggered subtypes whose trigger did not fire — the rig
// failures, and the only thing standing between this run and a promotable fixture
// once the version and encodings are right.
func (rec *oslcapRecord) unfiredTriggers() []string {
	out := []string{}
	for _, s := range rec.Subtypes {
		if s.Trigger != "" && !s.Conclusive {
			out = append(out, s.Subtype)
		}
	}
	return out
}

// fixtureWorthy answers whether this record may be promoted to oslcapFixturePath, and
// names the reason when it may not.
//
// THERE IS DELIBERATELY NO "ZERO FRAMES IS VACUOUS" RULE, which is this family's
// usual non-vacuity arm and would be wrong here. Absence IS the result this ticket
// commissions: a record whose three triggers all fired and which observed no line at
// all is a valid measurement, and #2256-#2259 drop their subtype on it rather than
// inventing fields. What replaces that arm is CONCLUSIVENESS — a triggered subtype
// whose trigger never fired says nothing about claude, and a fixture recording it as
// "unobserved" would be exactly the confusion this ticket exists to remove.
//
// The notification row is exempt from that rule and can never be conclusive: it has
// no trigger, so requiring it would refuse every capture this rig can produce.
//
// The version arm is the producing half of the pin the mapping tickets' readers will
// enforce; `claude --version` prints "<version> (Claude Code)", so the comparison is
// on the leading token, and an "<unavailable: ...>" fails it too.
func (rec *oslcapRecord) fixtureWorthy() (string, bool) {
	if rec.Outcome != oslcapFired {
		return fmt.Sprintf("outcome=%s", rec.Outcome), false
	}
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != oslcapFixtureVersion {
		return fmt.Sprintf("claude_version %q is not the %s pinned in the fixture name — repin "+
			"oslcapFixtureVersion, then re-run", got, oslcapFixtureVersion), false
	}
	if unfired := rec.unfiredTriggers(); len(unfired) > 0 {
		return fmt.Sprintf("%v had no line AND no witnessed trigger, so the record says nothing about "+
			"claude for them. Promoting it would publish a rig failure as an absence", unfired), false
	}
	// The mapping tickets' readers will read a json-string payload, and
	// dropcapMakeEntry emits base64 with an EMPTY payload for a frame that is not
	// valid UTF-8. Promoting one would hand them a frame with nothing to read.
	for _, f := range rec.Frames {
		if f.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("frame %d is encoded %q and the readers read only %q — a non-UTF-8 frame "+
				"carries no readable payload, so a fixture holding one would fail the assertion it "+
				"exists to feed", f.Index, f.PayloadEncoding, dropcapEncodingJSONString), false
		}
	}
	// The third redaction mechanism, and the only one this ticket adds. See
	// oslcapUnredactedPathFields for why the deny-scan alone does not cover it.
	if len(rec.UnredactedPathFields) > 0 {
		return fmt.Sprintf("a captured payload carries an absolute host path in %v after redaction. "+
			"Extend dropcapRedactor's table with the class that value belongs to and re-run; the FIELD "+
			"is named and the value deliberately is not, because printing it is the exposure this "+
			"refusal exists to prevent", rec.UnredactedPathFields), false
	}
	return "", true
}

// --- collecting and sweeping --------------------------------------------------

// oslcapCollect builds a frame for every captured line of the four subtypes, and for
// nothing else: the rest of the session is a content-free census, which is what keeps
// this fixture readable while still accounting for every line.
func oslcapCollect(t *testing.T, lines []dropcapCaptured, red *dropcapRedactor, phases []oslcapPhase) []oslcapFrame {
	t.Helper()
	out := []oslcapFrame{}
	for _, c := range lines {
		if !c.Decoded || c.Type != "system" || !oslcapContains(oslcapAllSubtypes(), c.Subtype) {
			continue
		}
		// The reason field is spent on this file's own classification, so it is passed
		// empty and dropped.
		entry := dropcapMakeEntry(c, "", red)
		verdict, decodes := oslcapDecodeVerdict(t, c.Raw)
		out = append(out, oslcapFrame{
			Index:                   entry.Index,
			Phase:                   oslcapPhaseFor(phases, entry.Index),
			Type:                    entry.Type,
			Subtype:                 entry.Subtype,
			DecodesIntoStreamLine:   decodes,
			DecodeVerdict:           verdict,
			MessageJSONType:         oslcapMessageJSONType(c.Raw),
			Keys:                    oslcapTopLevelKeys(c.Raw),
			PayloadLenBytesCaptured: entry.PayloadLenBytesCaptured,
			PayloadLenBytes:         entry.PayloadLenBytes,
			PayloadEncoding:         entry.PayloadEncoding,
			Payload:                 entry.Payload,
			PayloadB64:              entry.PayloadB64,
			EventsEmitted:           len(parseOne(t, string(c.Raw))),
		})
	}
	return out
}

func oslcapAllSubtypes() []string {
	return []string{
		oslcapSubtypeInformational,
		oslcapSubtypeLocalCommandOutput,
		oslcapSubtypeCommandsChanged,
		oslcapSubtypeNotification,
	}
}

// oslcapUnredactedPathFields is the third redaction mechanism and the only one this
// ticket adds, modelled on tncapUnredactedPathFields.
//
// The deny-scan's FIXED needles cover four known roots and its DYNAMIC ones cover
// this run's own paths; neither sees a path under some other root. #2251 found
// exactly that — claude's own messaging socket echoed into system/init, which is why
// dropcapClassMessagingSocket is added by the late adder rather than by the
// constructor. Two of this ticket's four subtypes are prose surfaces where such a
// value is plausible: a hook whose script fails prints an error naming its path, and
// a local command prints whatever it prints.
//
// It runs on the REDACTED payload, so it reports only what the declared table missed,
// and it names the FIELD and never the value.
func oslcapUnredactedPathFields(frames []oslcapFrame) []string {
	out := []string{}
	for _, f := range frames {
		// A base64 frame is skipped rather than decoded here: fixtureWorthy already
		// refuses those on the encoding alone, and decoding one would duplicate the
		// deny-scan's own base64 pass.
		if f.PayloadEncoding != dropcapEncodingJSONString || f.Payload == "" {
			continue
		}
		var doc any
		if err := json.Unmarshal([]byte(f.Payload), &doc); err != nil {
			continue
		}
		seen := map[string]bool{}
		oslcapWalkStrings(doc, "", func(where, value string) {
			if oslcapLooksAbsolutePath(value) {
				seen[where] = true
			}
		})
		for where := range seen {
			out = append(out, fmt.Sprintf("frame %d field %s", f.Index, where))
		}
	}
	sort.Strings(out)
	return out
}

// oslcapWalkStrings visits every string value in a decoded JSON document, naming its
// position as a dotted path with array indices spelled as segments.
func oslcapWalkStrings(v any, at string, hit func(where, value string)) {
	join := func(seg string) string {
		if at == "" {
			return seg
		}
		return at + "." + seg
	}
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			oslcapWalkStrings(val, join(k), hit)
		}
	case []any:
		for i, e := range t {
			oslcapWalkStrings(e, join(fmt.Sprintf("%d", i)), hit)
		}
	case string:
		hit(at, t)
	}
}

// oslcapLooksAbsolutePath is deliberately crude: a leading slash and at least two
// separators. A bare slash command such as the ones this rig sends carries one
// separator and is not flagged, and a value the redactor already substituted starts
// with the replacement's sigil rather than a slash. A false positive costs a named
// field in a refusal message; a false negative costs a host path in a public
// artefact, so the asymmetry is deliberate.
func oslcapLooksAbsolutePath(s string) bool {
	return strings.HasPrefix(s, "/") && strings.Count(s, "/") >= 2
}

func oslcapContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// --- the live capture --------------------------------------------------------

// TestRealClaude_OperatorSystemLinesCapture drives the three triggers on one child,
// watches the fourth subtype across the whole session, and writes the record. It
// asserts nothing about claude; the one thing it fails on is a trigger that did not
// fire, because that is the rig failing rather than a measurement.
//
// Ordering is load-bearing in two places, and each wrong order fails SILENTLY:
//
//   - newDropcapScanner reads CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY via
//     os.Getenv AS DENY NEEDLES, and WithWorktreeAuthenticated is what re-pins them
//     into this process. Building the scanner first yields an EMPTY needle, which
//     dropcapScanner.scan reports as notApplied — skipped, not fatal. The credential
//     net would be off while every message still read green.
//   - The record-writing cleanup is registered before anything below can fail, so
//     t.Cleanup's LIFO runs it last and a structural t.Fatalf still leaves the
//     evidence on disk.
func TestRealClaude_OperatorSystemLinesCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE, and that is a deliberate break from the
	// env-gated one-off probes in this package — AC 2. `make e2e-realclaude` never
	// sets a custom PYRY_PROBE_* variable, so an env gate skips on the ENV check
	// BEFORE the credential check, the live gate passes vacuously, and the fixture
	// never lands. That is CLAUDE.md § Testing's #1763 failure exactly: a green gate
	// and a spent budget look identical whether the bytes landed or not.
	force := os.Getenv(oslcapEnableEnv) == "1"
	if _, err := os.Stat(oslcapFixturePath); err == nil && !force {
		t.Skipf("#2255 operator-system-lines capture: the fixture %s already exists, so there is "+
			"nothing to capture and this costs no claude turn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 20m -v \\\n"+
			"    -run '^TestRealClaude_OperatorSystemLinesCapture$' ./internal/e2e/realclaude/",
			oslcapFixturePath, oslcapEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Deliberately NOT t.TempDir(): the operator needs the record after the test ends,
	// and this directory is the ONLY copy that survives a run in a worktree the
	// dispatcher discards.
	artifactDir, err := os.MkdirTemp("", oslcapArtifactPrefix)
	if err != nil {
		t.Fatalf("#2255: create artifact dir: %v", err)
	}

	// A fresh directory, deliberately not a git repo: no branch names and no file
	// contents can reach a payload. Its ONE non-empty inhabitant is the rig-authored
	// command file written mid-session, which is the inventory trigger.
	workdir := filepath.Join(home, oslcapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2255: create workdir: %v", err)
	}
	nonce := time.Now().UnixNano()

	// The empty slot is fifoPath, and the emptiness is a fact this probe records
	// rather than an omission: it holds no FIFO. dropcapRedactor.add guards "" —
	// strings.ReplaceAll(s, "", x) would otherwise insert x between every character.
	red := newDropcapRedactor(home, artifactDir, workdir, "", oslcapSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2255 capture artifacts: %s", red.str(artifactDir))

	rec := oslcapSeedRecord()
	rec.ClaudeVersion = probeClaudeVersion(claudeBin)
	rec.CapturedAt = time.Now().Format(time.RFC3339)
	rec.Workdir = red.str(workdir)
	rec.CredentialScanApplied = scanner.applied()
	rec.set(oslcapInstrumentBroken, "did not reach a classification point")
	t.Cleanup(func() { oslcapWriteRecord(t, artifactDir, red, scanner, rec) })

	rig, err := oslcapWriteHookRig(filepath.Join(home, oslcapRigDirName))
	if err != nil {
		rec.set(oslcapInstrumentBroken, "writing the hook rig failed, so the informational trigger "+
			"never existed and no claude was spawned: %v", red.str(err.Error()))
		return
	}
	rec.SettingsContent = red.str(rig.Settings)
	rec.HookScriptContent = red.str(rig.Script)

	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: oslcapSessionID,
		Args:      oslcapArgs(rig.SettingsPath),
		Stdout:    recorder,
		Logger:    slog.New(argvHandler),
	})
	if err != nil {
		rec.set(oslcapInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
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
		case <-time.After(oslcapRunExitWait):
			t.Errorf("#2255: streamsup.Run did not return within %s of cancel", oslcapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(oslcapInstrumentBroken, "no live child within %s: claude never spawned, so nothing was "+
			"on the wire to capture", dropcapSpawnWait)
		return
	}

	// send drives one turn and records its phase. Every phase waits on
	// oslcapAwaitQuietTurn rather than on a `result`, and the wanted result count is
	// read from the wire at send time rather than counted up: a phase that produces
	// no result at all must not leave every later phase waiting for a number that can
	// never arrive.
	send := func(name, prompt string, budget time.Duration) (int, bool) {
		lines, _ := recorder.snapshot()
		p := oslcapPhase{Name: name, Prompt: red.str(prompt), FirstLineIndex: len(lines)}
		start := time.Now()
		if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
			p.TerminatedOn = oslcapTerminatedUnsent
			rec.Phases = append(rec.Phases, p)
			rec.set(oslcapInstrumentBroken, "writing the %s turn failed, so its trigger was never "+
				"driven: %v", name, red.str(err.Error()))
			return p.FirstLineIndex, false
		}
		p.Sent = true
		rec.Prompts = append(rec.Prompts, red.str(prompt))
		p.TerminatedOn = oslcapAwaitQuietTurn(recorder, ccapResultCount(lines)+1, p.FirstLineIndex,
			oslcapQuiet, budget)
		p.Seconds = time.Since(start).Seconds()
		rec.Phases = append(rec.Phases, p)
		return p.FirstLineIndex, true
	}

	// --- preamble: the init line carries the slash-command inventory --------------
	// Waited for rather than read whenever the first send happens to finish, so the
	// /cost witness is a measurement and not a race.
	rec.InitObserved = oslcapAwaitInit(recorder, oslcapInitWait)

	// --- phase 1: the blocked prompt ---------------------------------------------
	if _, ok := send(oslcapPhaseBlock, oslcapBlockPrompt(nonce), oslcapBlockBudget); !ok {
		return
	}
	rec.HookVerdicts = oslcapHookVerdicts(rig.WitnessPath)
	rec.HookInvocations = len(rec.HookVerdicts)
	for _, v := range rec.HookVerdicts {
		if v == oslcapHookBlocked {
			rec.HookBlockedCount++
		}
	}

	// --- phase 2: liveness -------------------------------------------------------
	// AC 1's "later turns on the same child still run". An assistant line here is a
	// stronger signal than a `result`: it says the model answered, which is what
	// separates a child killed by the block from an inventory claude never re-read.
	livenessFrom, ok := send(oslcapPhaseLiveness, oslcapLivenessPrompt(nonce), oslcapLivenessBudget)
	if !ok {
		return
	}
	livenessLines, _ := recorder.snapshot()
	rec.ChildAliveAfterBlock = oslcapAssistantLines(livenessLines, livenessFrom, len(livenessLines)) > 0

	// --- phase 3: the inventory change -------------------------------------------
	token := oslcapCommandToken(nonce)
	commandPath, written, err := oslcapWriteCommandFile(workdir, token)
	if err != nil {
		rec.set(oslcapInstrumentBroken, "writing the project slash command failed, so the "+
			"commands_changed trigger never existed: %v", red.str(err.Error()))
		return
	}
	rec.CommandFileBytes = written
	rec.CommandFilePath = red.str(commandPath)
	rec.CommandFileContent = red.str(oslcapCommandBody(token))

	inventoryLines, _ := recorder.snapshot()
	inventory := oslcapPhase{
		Name: oslcapPhaseInventory, Sent: true, FirstLineIndex: len(inventoryLines),
	}
	inventoryStart := time.Now()
	if tncapAwaitSubtype(recorder, oslcapSubtypeCommandsChanged, oslcapInventoryWatch, tncapResultIsNotTheEnd) {
		inventory.TerminatedOn = oslcapTerminatedSubtype
	} else {
		inventory.TerminatedOn = oslcapTerminatedBudget
	}
	inventory.Seconds = time.Since(inventoryStart).Seconds()
	rec.Phases = append(rec.Phases, inventory)

	// --- phase 4: the custom command ---------------------------------------------
	// The claude-side half of the inventory witness. A rig-side file write establishes
	// only that the rig did its part; a slash command the session honours establishes
	// that the inventory was actually re-read, which is what an absence claim needs.
	commandFrom, ok := send(oslcapPhaseCommand, oslcapCommandPrompt, oslcapCommandBudget)
	if !ok {
		return
	}
	commandLines, _ := recorder.snapshot()
	rec.CustomCommandHonoured = oslcapAssistantCarriesToken(commandLines, commandFrom, token)

	// --- phase 5: the local command ----------------------------------------------
	// The command is chosen from the session's OWN inventory rather than assumed —
	// see oslcapLocalCommandCandidates for the measurement behind that.
	slashCommands := oslcapInitSlashCommands(commandLines)
	rec.SlashCommandCountAtInit = len(slashCommands)
	rec.LocalCommandChosen, rec.LocalCommandInInventory = oslcapPickLocalCommand(slashCommands)
	// Expected FALSE: the command file is written after init. A true here would mean
	// the inventory was already carrying it, which changes what the trigger measured.
	rec.ProbeCommandInInitInventory = oslcapContains(slashCommands, oslcapCommandName)

	costFrom, ok := send(oslcapPhaseLocalCommand, oslcapSlashPrompt(rec.LocalCommandChosen), oslcapCostBudget)
	if !ok {
		return
	}
	costLines, _ := recorder.snapshot()
	rec.LocalCommandTurnLines = len(costLines) - costFrom
	rec.LocalCommandTurnAssistant = oslcapAssistantLines(costLines, costFrom, len(costLines))

	// --- phase 6: settle ---------------------------------------------------------
	// notification has no trigger, so the only thing that can be done for it is to
	// keep watching until the session ends. This window is what stops the test
	// returning out from under a late one.
	settleLines, _ := recorder.snapshot()
	settle := oslcapPhase{Name: oslcapPhaseSettle, Sent: false, FirstLineIndex: len(settleLines)}
	settleStart := time.Now()
	if tncapAwaitSubtype(recorder, oslcapSubtypeNotification, oslcapSettleWatch, tncapResultIsNotTheEnd) {
		settle.TerminatedOn = oslcapTerminatedSubtype
	} else {
		settle.TerminatedOn = oslcapTerminatedBudget
	}
	settle.Seconds = time.Since(settleStart).Seconds()
	rec.Phases = append(rec.Phases, settle)

	// --- the record ---------------------------------------------------------------
	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	for i := range rec.Phases {
		end := len(lines)
		if i+1 < len(rec.Phases) {
			end = rec.Phases[i+1].FirstLineIndex
		}
		rec.Phases[i].LineCount = end - rec.Phases[i].FirstLineIndex
	}

	// tpcapCensus is #2089's and is CALLED rather than re-derived; the tpcap prefix
	// marks the file it was minted in, not private scope.
	// Re-read, so the record carries EVERY invocation rather than only those the blocked
	// turn produced: whether a slash-command turn fires a UserPromptSubmit hook at all is
	// itself unmeasured, and the full ordered list is the only place that shows.
	rec.HookVerdicts = oslcapHookVerdicts(rig.WitnessPath)
	rec.HookInvocations = len(rec.HookVerdicts)
	rec.HookBlockedCount = 0
	for _, v := range rec.HookVerdicts {
		if v == oslcapHookBlocked {
			rec.HookBlockedCount++
		}
	}

	rec.LineTypeCensus, rec.ToolCalls, rec.ToolResultErrors, rec.UndecodedLines = tpcapCensus(lines, red)
	rec.Frames = oslcapCollect(t, lines, red, rec.Phases)
	rec.FrameCount = len(rec.Frames)
	rec.UnredactedPathFields = oslcapUnredactedPathFields(rec.Frames)

	rec.Subtypes = oslcapSubtypeRecords(rec)
	if unfired := rec.unfiredTriggers(); len(unfired) > 0 {
		rec.set(oslcapDidNotFire, "%v had neither a captured line nor a witnessed trigger", unfired)
	} else {
		observed := []string{}
		for _, s := range rec.Subtypes {
			if s.Observed {
				observed = append(observed, fmt.Sprintf("%s=%d", s.Subtype, s.LineCount))
			}
		}
		rec.set(oslcapFired, "every triggered subtype is conclusive; observed %v out of %d captured line(s)",
			observed, rec.LinesCaptured)
	}

	// --- the one thing this test fails on -----------------------------------------
	// Counts, censuses and subtype names only. The frames themselves are in the record
	// the cleanup has already written; putting claude's bytes in CI output is precisely
	// the exposure the deny-scan exists to prevent.
	//
	// An unobserved subtype is NOT a failure — it is the result this ticket
	// commissions. A trigger that did not fire is, because the record then says
	// nothing about claude for that subtype and a fixture carrying it would publish a
	// rig failure as an absence.
	if unfired := rec.unfiredTriggers(); len(unfired) > 0 {
		t.Fatalf("#2255: %v had no captured line AND no witnessed trigger, so this run says nothing "+
			"about claude for them and the fixture was NOT promoted.\n"+
			"  hook: %d invocation(s) %v, %d blocked; child alive after the block: %v\n"+
			"  inventory: %d bytes written, custom command honoured: %v; init carried %d slash "+
			"command(s), the chosen local command %q among them: %v, the probe among them: %v\n"+
			"  local-command turn: %d line(s), %d of them assistant lines\n"+
			"  phases: %v\n"+
			"  line types: %v; tools called: %v; tool_result errors: %d; undecoded: %d\n"+
			"Read each subtype's note in the record: only a row whose trigger FIRED says anything "+
			"about claude, and the rest are the rig failing to provoke what it meant to measure",
			unfired, rec.HookInvocations, rec.HookVerdicts, rec.HookBlockedCount, rec.ChildAliveAfterBlock,
			rec.CommandFileBytes, rec.CustomCommandHonoured, rec.SlashCommandCountAtInit,
			rec.LocalCommandChosen, rec.LocalCommandInInventory, rec.ProbeCommandInInitInventory,
			rec.LocalCommandTurnLines, rec.LocalCommandTurnAssistant, rec.Phases, rec.LineTypeCensus, rec.ToolCalls,
			rec.ToolResultErrors, rec.UndecodedLines)
	}
}

// oslcapSubtypeRecords builds AC 4's answer for all four subtypes: what was observed,
// what was attempted, and how we know the attempt landed.
//
// Every witness here is a COUNT or a BOOLEAN the rig measured, never claude's prose.
// The /cost row deliberately does NOT fold cost_turn_assistant_lines into its
// trigger_fired: a zero is consistent with the command having been handled locally
// and a non-zero with the text having been answered as prose, but reading either as
// the gate would make the witness depend on the very thing it exists to witness.
func oslcapSubtypeRecords(rec *oslcapRecord) []oslcapSubtype {
	indices := map[string][]int{}
	for _, f := range rec.Frames {
		indices[f.Subtype] = append(indices[f.Subtype], f.Index)
	}
	subs := []oslcapSubtype{
		{
			Subtype: oslcapSubtypeInformational,
			Trigger: "a --settings file declaring one UserPromptSubmit command hook, which exits 2 with " +
				"a reason on stderr for a prompt carrying the rig marker and passes everything else",
			TriggerWitness: fmt.Sprintf("the hook script appended %d verdict(s) %v to its own witness "+
				"file, %d of them a block; the child answered the next unmarked turn: %v",
				rec.HookInvocations, rec.HookVerdicts, rec.HookBlockedCount, rec.ChildAliveAfterBlock),
			TriggerFired: rec.HookBlockedCount > 0,
		},
		{
			Subtype: oslcapSubtypeLocalCommandOutput,
			Trigger: "a user turn whose text is exactly the local slash command /" + rec.LocalCommandChosen +
				", chosen from " + fmt.Sprint(rec.LocalCommandCandidates) + " by what the session's own " +
				"init inventory carried",
			TriggerWitness: fmt.Sprintf("the command was in the session's own init inventory of %d: %v; "+
				"the turn was sent and produced %d line(s), %d of them assistant lines (a zero is "+
				"consistent with local handling and a non-zero with the text being answered as prose)",
				rec.SlashCommandCountAtInit, rec.LocalCommandInInventory, rec.LocalCommandTurnLines,
				rec.LocalCommandTurnAssistant),
			TriggerFired: rec.LocalCommandInInventory && rec.LocalCommandTurnLines > 0,
		},
		{
			Subtype: oslcapSubtypeCommandsChanged,
			Trigger: "a project slash command written into the workdir's .claude/commands mid-session, " +
				"then invoked",
			TriggerWitness: fmt.Sprintf("%d bytes written while the child was live, absent from the "+
				"init inventory beforehand (%v), and the later invocation was honoured: %v — which is "+
				"the claude-side half, since a file on disk alone says only that the rig did its part",
				rec.CommandFileBytes, rec.ProbeCommandInInitInventory, rec.CustomCommandHonoured),
			TriggerFired: rec.CommandFileBytes > 0 && rec.CustomCommandHonoured,
		},
		{
			// No Trigger, and that emptiness is what finish() reads to keep this row
			// out of the promotion rule. Naming a trigger that does not exist would
			// turn "nothing provoked it" into a false absence claim.
			Subtype: oslcapSubtypeNotification,
		},
	}
	for i := range subs {
		s := &subs[i]
		s.FrameIndices = indices[s.Subtype]
		s.LineCount = len(s.FrameIndices)
		s.Observed = s.LineCount > 0
		s.finish()
	}
	return subs
}

// --- writing the record ------------------------------------------------------

// oslcapSeal marshals the record as it stands and deny-scans the exact bytes it just
// produced, plus the decoded bytes of every base64 frame payload — those hide from a
// scan of the marshalled record, where they sit as base64.
//
// Split out so the property that matters can be asserted offline, with no live turn
// and no write anywhere near the repo. It returns the classes hit rather than
// deciding anything: the caller owns the fail-closed response, and the caller is the
// only place that knows what was about to be written.
func oslcapSeal(scanner dropcapScanner, rec *oslcapRecord) (blob []byte, hits []string, err error) {
	blob, err = json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	hits, _ = scanner.scan(blob)
	for _, f := range rec.Frames {
		if f.PayloadB64 == "" {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(f.PayloadB64)
		if derr != nil {
			return nil, nil, fmt.Errorf("frame %d: decode base64 payload for the scan: %w", f.Index, derr)
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	return blob, hits, nil
}

// oslcapWriteRecord marshals, deny-scans, writes, promotes and stages. #1260's
// fail-closed rule verbatim: on a hit NOTHING is written and the message names the
// CLASS only, never the matched value.
func oslcapWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *oslcapRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()

	// credential_scan_skipped is a list of CLASS NAMES whose needle was too short to
	// search for, which is the one thing that makes a silently-off credential net
	// visible after the fact. It is a property of the needle SET rather than of any
	// blob, so it is read once here and ships inside every blob sealed below.
	_, notApplied := scanner.scan(nil)
	rec.CredentialScanSkipped = notApplied

	// seal is called before EVERY write rather than once at the top, and that is the
	// whole point of it. Fields enter the record BETWEEN the writes:
	// fixture_stage_detail carries `git`'s combined output, and git prints repository
	// paths on failure — a path in nobody's substitution table, since dropcapRedactor
	// covers the temp $HOME, the artifact dir, the workdir and the session id, and not
	// the repo. Sealing once and then re-marshalling scans a blob that does not yet
	// hold those bytes, so they reach a public committed artefact having passed no
	// scan at all (#2247).
	seal := func(what string) []byte {
		t.Helper()
		blob, hits, err := oslcapSeal(scanner, rec)
		if err != nil {
			t.Fatalf("#2255: seal the %s: %v", what, err)
		}
		if len(hits) > 0 {
			t.Fatalf("#2255: deny-scan found %d denied class(es) in the %s about to be written: %v\n"+
				"THAT FILE WAS NOT WRITTEN, and neither is anything after it. Whatever this run had "+
				"already put on disk was sealed by this same scan before it was written, so nothing "+
				"unscanned is on disk. Extend dropcapRedactor's table with the named class and re-run "+
				"the capture. The offending value is deliberately not printed: putting it in CI output "+
				"is exactly the exposure this scan exists to prevent", len(hits), what, hits)
		}
		return blob
	}

	reason, worthy := rec.fixtureWorthy()
	if worthy {
		// What the FIXTURE can honestly say about its own staging, which is not whether
		// `git add` succeeded: staging can only run once the file exists, so its outcome
		// lands in the artifact-dir record rewritten at the end of this function.
		rec.FixtureStageDetail = "promoted; `git add` runs after this file is written, so its outcome " +
			"is in fixture_staged/fixture_stage_detail of the artifact-dir record, not here"
	} else {
		rec.FixtureStageDetail = "not promoted: " + reason
	}

	path := filepath.Join(dir, oslcapRecordName)
	blob := seal("record")
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2255: write record %s: %v", red.str(path), err)
		return
	}
	t.Logf("#2255 outcome=%s captured=%d frames=%d subtypes=%v hook=%v/%d-blocked alive_after_block=%v "+
		"local_cmd=%q in_inventory=%v lines=%d/%d-assistant command_honoured=%v line_types=%v tools=%v "+
		"scan_not_applied=%v\n  record: %s\n  %s",
		rec.Outcome, rec.LinesCaptured, rec.FrameCount, oslcapSummary(rec.Subtypes), rec.HookVerdicts,
		rec.HookBlockedCount, rec.ChildAliveAfterBlock, rec.LocalCommandChosen, rec.LocalCommandInInventory,
		rec.LocalCommandTurnLines, rec.LocalCommandTurnAssistant, rec.CustomCommandHonoured, rec.LineTypeCensus,
		rec.ToolCalls, notApplied, red.str(path), red.str(rec.OutcomeDetail))

	if !worthy {
		t.Logf("#2255: NOT promoted to %s — %s. The record above is the evidence; read it, then "+
			"re-run or route the finding back", oslcapFixturePath, reason)
		return
	}
	// The same sealed bytes, promoted in-repo so the run that produced them is the run
	// that lands them. A capture that still needs a human to copy a file out of a
	// tempdir is a capture #1763 says will not land.
	if err := os.WriteFile(oslcapFixturePath, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2255: write fixture %s: %v", oslcapFixturePath, red.str(err.Error()))
		return
	}

	// AC 3, and it runs only now that the file exists. `git add` on a path that is not
	// yet on disk fails with "pathspec did not match any files" — and the fixture being
	// absent is precisely the first-capture case this probe arms itself for, so staging
	// before the write would fail on every run that matters.
	rec.FixtureStaged, rec.FixtureStageDetail = oslcapStageFixture(red)
	// Re-seal so git's output is deny-scanned before it becomes part of any artefact,
	// and rewrite the record — the artifact-dir copy is where AC 3's evidence lives.
	if err := os.WriteFile(path, append(seal("record's staging outcome"), '\n'), 0o600); err != nil {
		t.Errorf("#2255: rewrite record %s with the staging outcome: %v", red.str(path), err)
		return
	}
	t.Logf("#2255: FIXTURE WRITTEN to %s (%d frame(s) across %v). git add: staged=%v — %s\n"+
		"  COMMIT IT. #2256, #2257, #2258 and #2259 each read one subtype's row out of this file, and "+
		"a row recorded unobserved-with-a-fired-trigger is an instruction to DROP that subtype rather "+
		"than to invent its fields. If the staging above ran in a worktree that gets discarded, the "+
		"record at the artifact path logged earlier is the copy that survives (#1763).",
		oslcapFixturePath, rec.FrameCount, oslcapSummary(rec.Subtypes), rec.FixtureStaged,
		rec.FixtureStageDetail)
}

// oslcapSummary renders the four rows as one short log field: subtype, whether it was
// observed and how many lines, or why its absence is what it is. Names and counts
// only — never a payload.
func oslcapSummary(subs []oslcapSubtype) []string {
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		switch {
		case s.Observed:
			out = append(out, fmt.Sprintf("%s=%d", s.Subtype, s.LineCount))
		case s.Trigger == "":
			out = append(out, s.Subtype+"=unobserved(no-trigger)")
		case s.TriggerFired:
			out = append(out, s.Subtype+"=unobserved(trigger-fired)")
		default:
			out = append(out, s.Subtype+"=INCONCLUSIVE")
		}
	}
	return out
}

// oslcapStageFixture runs `git add` on the fixture the caller has just written — AC 3,
// so the run that produces the artefact is the run that stages it rather than leaving
// a human to carry a file out of a tempdir.
//
// Fixed argv, no shell, and the one argument is a compile-time constant with `--`
// ahead of it so it can never be read as a flag; nothing claude emitted reaches it.
// The combined output goes through the redactor because git prints repository paths
// on failure.
//
// BEST-EFFORT AND NEVER FATAL. git being absent, or the working tree being somewhere
// staging means nothing, must not throw away a capture that cost live turns — the
// bytes are already in the working tree and in the artifact directory by this point.
// The outcome is recorded either way, because "did the staging happen" is exactly the
// question #2229 could not answer about itself afterwards.
func oslcapStageFixture(red *dropcapRedactor) (bool, string) {
	out, err := exec.Command("git", "add", "--", oslcapFixturePath).CombinedOutput()
	if err != nil {
		return false, red.str(fmt.Sprintf("git add failed (%v): %s. The fixture is written in-repo "+
			"regardless; stage and commit it by hand", err, strings.TrimSpace(string(out))))
	}
	return true, "staged with `git add`. A run in a worktree the dispatcher discards stages into an " +
		"index that goes with it, so the artifact-dir record remains the copy that survives"
}

// --- offline self-checks -----------------------------------------------------

// TestOslcapDecodeVerdictMatchesTheShippedParser runs offline and is the
// load-bearing guard behind the record's decodes_into_stream_line field.
//
// The rows ARE the argument in this file's header, executed against a real
// streamsup.Parser. The string-message row is the system/permission_denied shape
// that motivated the question; the object-with-string-content row is the subtler
// one, because streamMessage declares Content as a slice and a string there fails
// the whole line just as loudly.
//
// Non-vacuity is asserted rather than assumed: a mutant answering "decodes" for
// everything, or "does-not-decode" for everything, has to fail, so the table is
// checked for carrying both verdicts.
func TestOslcapDecodeVerdictMatchesTheShippedParser(t *testing.T) {
	t.Parallel()
	const head = `{"type":"system","subtype":"informational"`
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"no message key at all", head + `,"text":"hook feedback"}`, true},
		{"message null", head + `,"message":null}`, true},
		{
			"message is the object streamMessage declares",
			head + `,"message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
			true,
		},
		{"message is an object with an empty content array", head + `,"message":{"role":"user","content":[]}}`, true},
		{"message is an object with no content key", head + `,"message":{"role":"user"}}`, true},
		{
			// The system/permission_denied shape, and the reason this field exists.
			"message is a plain string",
			head + `,"message":"a hook blocked this prompt"}`,
			false,
		},
		{"message is a number", head + `,"message":123}`, false},
		{"message is a boolean", head + `,"message":true}`, false},
		{"message is an array", head + `,"message":["a"]}`, false},
		{
			// Subtler than the string case: the object decodes, its content does not.
			"message is an object whose content is a string",
			head + `,"message":{"role":"user","content":"plain prose"}}`,
			false,
		},
		{"the line is not JSON at all", head + `,`, false},
	}
	var sawDecodes, sawFails bool
	for _, tc := range tests {
		if tc.want {
			sawDecodes = true
		} else {
			sawFails = true
		}
	}
	if !sawDecodes || !sawFails {
		t.Fatalf("the table carries decodes=%v does-not-decode=%v; a table with one verdict "+
			"cannot catch a helper that always answers the other", sawDecodes, sawFails)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verdict, decodes := oslcapDecodeVerdict(t, []byte(tc.line))
			if decodes != tc.want {
				t.Errorf("oslcapDecodeVerdict() decodes = %v, want %v (verdict %q)", decodes, tc.want, verdict)
			}
			wantVerdict := oslcapDoesNotDecode
			if tc.want {
				wantVerdict = oslcapDecodes
			}
			if verdict != wantVerdict {
				t.Errorf("oslcapDecodeVerdict() verdict = %q, want %q", verdict, wantVerdict)
			}
		})
	}
}

// TestOslcapMessageJSONTypeReadsTheRawLine runs offline. The second half of AC 5 is
// the JSON type the `message` key carries, and the answer has to come from the raw
// bytes rather than from a decode: the shapes worth naming are exactly the ones a
// typed decode rejects.
func TestOslcapMessageJSONTypeReadsTheRawLine(t *testing.T) {
	t.Parallel()
	const head = `{"type":"system","subtype":"notification"`
	tests := []struct {
		name, line, want string
	}{
		{"no message key", head + `}`, oslcapJSONAbsent},
		{"null", head + `,"message":null}`, oslcapJSONNull},
		{"string", head + `,"message":"text"}`, oslcapJSONString},
		{"number", head + `,"message":12}`, oslcapJSONNumber},
		{"true", head + `,"message":true}`, oslcapJSONBoolean},
		{"false", head + `,"message":false}`, oslcapJSONBoolean},
		{"object", head + `,"message":{"role":"user"}}`, oslcapJSONObject},
		{"array", head + `,"message":[1,2]}`, oslcapJSONArray},
		{"leading whitespace before the value", head + `,"message":   "text"}`, oslcapJSONString},
		{"an undecodable line reports nothing rather than guessing", head + `,"message":`, oslcapJSONInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := oslcapMessageJSONType([]byte(tc.line)); got != tc.want {
				t.Errorf("oslcapMessageJSONType() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOslcapPickLocalCommandPrefersTheTicketsCommand runs offline.
//
// The rows are a measurement, not a preference. dropped_lines_v2.1.220.json's init
// line carries a 46-command inventory that does NOT include `cost` while it DOES
// include `usage` and `context`, so a rig that hardcoded the ticket's command name
// would have driven a turn claude answers as prose, recorded local_command_output as
// unobserved against a trigger that could not fire, and spent a live gate to learn a
// name. The last row is the one that keeps a no-candidate session legible rather than
// silent.
func TestOslcapPickLocalCommandPrefersTheTicketsCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		inventory []string
		want      string
		wantOK    bool
	}{
		{"the ticket's command wins when the session has it", []string{"usage", "cost", "context"}, "cost", true},
		{
			// The shape actually measured in this package's committed testdata.
			"falls to the next candidate when cost is absent",
			[]string{"clear", "compact", "usage", "context", "model"},
			"usage", true,
		},
		{"and to the one after that", []string{"clear", "context"}, "context", true},
		{"the last candidate is still a candidate", []string{"status"}, "status", true},
		{
			// Not a silent skip: the ticket's own command is still driven, and ok=false
			// is what makes local_command_output inconclusive rather than absent.
			"no candidate at all still drives the ticket's command and says so",
			[]string{"clear", "compact"},
			"cost", false,
		},
		{"an empty inventory is the same case", nil, "cost", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := oslcapPickLocalCommand(tc.inventory)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("oslcapPickLocalCommand(%v) = %q, %v; want %q, %v", tc.inventory, got, ok,
					tc.want, tc.wantOK)
			}
		})
	}

	t.Run("every candidate is read-only", func(t *testing.T) {
		t.Parallel()
		// A capture that changes the thing it is measuring is not a capture, and the
		// inventory holds several commands that mutate session state.
		for _, mutating := range []string{"model", "config", "clear", "compact", "effort", "fast"} {
			if oslcapContains(oslcapLocalCommandCandidates, mutating) {
				t.Errorf("%q is a candidate local command, but it mutates session state; the capture "+
					"would be measuring a session it had just changed", mutating)
			}
		}
	})
}

// TestOslcapHookScriptBlocksOnlyTheMarkedPrompt runs the REAL generated script under
// `sh`, offline, against two synthetic payloads.
//
// This is the deterministic net under a stochastic live turn, and it is the one
// offline test that would have paid for itself twice over on its own: a hook that
// blocks nothing, or blocks everything, is a defect a string comparison finds here
// and a spent live gate finds there. #2247's first live lap died on a rig-authored
// constant for want of exactly this kind of check.
//
// The `sh <path>` invocation is deliberate and never `sh -c <string>`: nothing is
// shell-interpreted from a string, and the script's own interpolated values are
// single-quoted by oslcapHookScript.
func TestOslcapHookScriptBlocksOnlyTheMarkedPrompt(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), oslcapRigDirName)
	rig, err := oslcapWriteHookRig(dir)
	if err != nil {
		t.Fatalf("oslcapWriteHookRig: %v", err)
	}

	run := func(t *testing.T, payload string) (int, string) {
		t.Helper()
		cmd := exec.Command("sh", rig.ScriptPath)
		cmd.Stdin = strings.NewReader(payload)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			return 0, stderr.String()
		case errors.As(err, &exitErr):
			return exitErr.ExitCode(), stderr.String()
		default:
			t.Fatalf("running the hook script: %v", err)
			return -1, ""
		}
	}

	// An unmarked prompt first, so the blocked run below cannot be the only
	// invocation and the witness file's ORDER is a real observation.
	t.Run("an unmarked prompt passes", func(t *testing.T) {
		code, stderr := run(t, `{"hook_event_name":"UserPromptSubmit","prompt":"what is 2+2"}`)
		if code != 0 {
			t.Errorf("exit code = %d, want 0; an unmarked prompt must not be blocked, or every later "+
				"turn on the child dies with the first one", code)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty on the passing arm", stderr)
		}
	})

	t.Run("a marked prompt is blocked with the rig's reason", func(t *testing.T) {
		code, stderr := run(t, `{"hook_event_name":"UserPromptSubmit","prompt":"say hi `+
			oslcapBlockMarker+`-12345"}`)
		if code != 2 {
			t.Errorf("exit code = %d, want 2; a UserPromptSubmit hook blocks on 2 and nothing else, so "+
				"any other code leaves the prompt to run normally", code)
		}
		if !strings.Contains(stderr, oslcapBlockReason) {
			t.Errorf("stderr = %q, want it to carry the rig's block reason; the reason is what a "+
				"system/informational line would echo, and it is the only rig-authored string this "+
				"capture can recognise in one", stderr)
		}
	})

	t.Run("the witness records both invocations in order", func(t *testing.T) {
		got := oslcapHookVerdicts(rig.WitnessPath)
		want := []string{oslcapHookPassed, oslcapHookBlocked}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("oslcapHookVerdicts() = %v, want %v; the witness is the whole reason an "+
				"unobserved system/informational can be told from a hook that never ran", got, want)
		}
	})
}

// TestOslcapHookScriptRefusesAnUnquotableValue runs offline.
//
// oslcapHookScript builds SHELL SOURCE by interpolation, and claude executes what it
// builds as the operator — writeMCPSettings' doc makes exactly that point about a
// file pyry hands claude as --settings. Two of the three values are compile-time
// constants and the third is a path derived from TMPDIR, so nothing is exploitable
// today; the refusal is what keeps that a property of the code rather than of
// today's inputs.
func TestOslcapHookScriptRefusesAnUnquotableValue(t *testing.T) {
	t.Parallel()

	t.Run("a single quote in any position is refused", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ name, witness, marker, reason string }{
			{"in the witness path", "/tmp/it's-here", "M", "R"},
			{"in the marker", "W", "MARK'ER", "R"},
			{"in the reason", "W", "M", "the rig's reason"},
		} {
			if _, err := oslcapHookScript(tc.witness, tc.marker, tc.reason); err == nil {
				t.Errorf("%s: oslcapHookScript() returned no error; a value carrying a single quote "+
					"closes the quoting and turns the rest into shell source", tc.name)
			}
		}
	})

	t.Run("the script it does produce quotes all three", func(t *testing.T) {
		t.Parallel()
		script, err := oslcapHookScript("WITNESS", "MARKER", "REASON")
		if err != nil {
			t.Fatalf("oslcapHookScript: %v", err)
		}
		for _, want := range []string{"'WITNESS'", "'MARKER'", "'REASON'"} {
			if !strings.Contains(script, want) {
				t.Errorf("the generated script does not carry %s; an unquoted interpolation is the "+
					"whole hazard this test exists for.\n%s", want, script)
			}
		}
	})
}

// TestOslcapSettingsFileIsTheShapeClaudeReads runs offline. The settings file is the
// entire informational trigger, and a shape claude does not read is a trigger that
// silently never fires — which this capture would then record as a hook that did not
// run, spending a live gate to learn a JSON key was wrong.
func TestOslcapSettingsFileIsTheShapeClaudeReads(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), oslcapRigDirName)
	rig, err := oslcapWriteHookRig(dir)
	if err != nil {
		t.Fatalf("oslcapWriteHookRig: %v", err)
	}

	blob, err := os.ReadFile(rig.SettingsPath)
	if err != nil {
		t.Fatalf("read the settings file: %v", err)
	}
	var settings struct {
		Hooks struct {
			UserPromptSubmit []struct {
				Hooks []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"UserPromptSubmit"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(blob, &settings); err != nil {
		t.Fatalf("decode the settings file: %v\n%s", err, blob)
	}
	matchers := settings.Hooks.UserPromptSubmit
	if len(matchers) != 1 || len(matchers[0].Hooks) != 1 {
		t.Fatalf("settings declare %d UserPromptSubmit matcher group(s); want exactly one holding one "+
			"hook.\n%s", len(matchers), blob)
	}
	if got := matchers[0].Hooks[0].Type; got != "command" {
		t.Errorf("hook type = %q, want %q", got, "command")
	}
	if got := matchers[0].Hooks[0].Command; got != rig.ScriptPath {
		t.Errorf("hook command = %q, want the script this rig wrote", got)
	}

	// Modes. The settings file is a file pyry-adjacent tooling hands claude as
	// executable policy, so anyone who can write it can run code as the operator;
	// writeMCPSettings makes the same point about integrity rather than secrecy.
	for _, tc := range []struct {
		name string
		path string
		want os.FileMode
	}{
		{"the rig directory", dir, 0o700},
		{"the settings file", rig.SettingsPath, 0o600},
		{"the hook script", rig.ScriptPath, 0o700},
		{"the witness file", rig.WitnessPath, 0o600},
	} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Errorf("%s: stat: %v", tc.name, err)
			continue
		}
		if got := info.Mode().Perm(); got != tc.want {
			t.Errorf("%s: mode = %04o, want %04o", tc.name, got, tc.want)
		}
	}
}

// TestOslcapFixtureWorthyRefusesEveryBadCapture runs offline. fixtureWorthy is the
// only thing standing between a live run and a committed fixture, and each refusing
// row is a capture that looks green from outside — the record is written, the
// deny-scan passed, the log is cheerful — while proving nothing, proving something
// about a different claude, or claiming an absence the rig never earned.
//
// The promoting rows are the ones that state this ticket's difference from its
// siblings. A record holding ZERO frames is promoted when all three triggered
// subtypes fired: that is a valid absence result for all three, and #2256-#2259 drop
// their subtype on it rather than inventing fields. And `notification` unobserved
// never blocks promotion, because it has no trigger and can never be conclusive —
// requiring it would refuse every capture this rig can produce.
func TestOslcapFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	triggered := func(subtype string, observed, fired bool) oslcapSubtype {
		s := oslcapSubtype{
			Subtype:        subtype,
			Observed:       observed,
			Trigger:        "a trigger",
			TriggerWitness: "a witness",
			TriggerFired:   fired,
		}
		s.finish()
		return s
	}
	watched := func(observed bool) oslcapSubtype {
		s := oslcapSubtype{Subtype: oslcapSubtypeNotification, Observed: observed}
		s.finish()
		return s
	}
	good := func() *oslcapRecord {
		return &oslcapRecord{
			Outcome:       oslcapFired,
			ClaudeVersion: oslcapFixtureVersion + " (Claude Code)",
			Frames: []oslcapFrame{
				{Index: 3, Subtype: oslcapSubtypeInformational, PayloadEncoding: dropcapEncodingJSONString},
			},
			Subtypes: []oslcapSubtype{
				triggered(oslcapSubtypeInformational, true, true),
				triggered(oslcapSubtypeLocalCommandOutput, false, true),
				triggered(oslcapSubtypeCommandsChanged, false, true),
				watched(false),
			},
			UnredactedPathFields: []string{},
		}
	}
	tests := []struct {
		name   string
		mutate func(*oslcapRecord)
		want   bool
	}{
		{"a good capture is promoted", func(*oslcapRecord) {}, true},
		{"bare version string, no suffix", func(r *oslcapRecord) { r.ClaudeVersion = oslcapFixtureVersion }, true},
		{
			// The row that states this family's difference: absence IS the result.
			"zero frames, every trigger fired, is a valid absence record",
			func(r *oslcapRecord) {
				r.Frames = []oslcapFrame{}
				r.Subtypes[0] = triggered(oslcapSubtypeInformational, false, true)
			},
			true,
		},
		{"never fired", func(r *oslcapRecord) { r.Outcome = oslcapDidNotFire }, false},
		{"instrument broken", func(r *oslcapRecord) { r.Outcome = oslcapInstrumentBroken }, false},
		{"a different claude release", func(r *oslcapRecord) { r.ClaudeVersion = "2.1.260 (Claude Code)" }, false},
		{"version unreadable", func(r *oslcapRecord) { r.ClaudeVersion = "<unavailable: exec failed>" }, false},
		{"version absent", func(r *oslcapRecord) { r.ClaudeVersion = "" }, false},
		{
			"a base64 frame the readers cannot read",
			func(r *oslcapRecord) { r.Frames[0].PayloadEncoding = dropcapEncodingBase64 },
			false,
		},
		{
			// The rig-failure row, and the reason this refusal exists at all.
			"a triggered subtype whose trigger did not fire is inconclusive",
			func(r *oslcapRecord) { r.Subtypes[1] = triggered(oslcapSubtypeLocalCommandOutput, false, false) },
			false,
		},
		{
			"a host path surviving redaction stops the promotion",
			func(r *oslcapRecord) { r.UnredactedPathFields = []string{"frame 3 field hook.command"} },
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

	t.Run("notification unobserved never blocks promotion", func(t *testing.T) {
		t.Parallel()
		rec := good()
		rec.Subtypes[3] = watched(false)
		if _, ok := rec.fixtureWorthy(); !ok {
			t.Error("a capture whose only unobserved-and-inconclusive subtype is the one with NO " +
				"trigger was refused; that rule would refuse every capture this rig can produce")
		}
	})
}

// TestOslcapSubtypeVerdictSeparatesRigFailureFromFinding runs offline.
//
// The note is what a reader of the fixture acts on, and AC 4's whole demand is that
// it names WHICH reading an unobserved subtype is. Exactly one of the four arms is a
// finding about claude; confusing it with the others is how a mapping ticket invents
// fields for a subtype nobody ever provoked.
func TestOslcapSubtypeVerdictSeparatesRigFailureFromFinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		sub             oslcapSubtype
		wantConclusive  bool
		wantFinding     bool
		wantRigFailure  bool
		wantNoTriggerly bool
	}{
		{
			name:           "observed",
			sub:            oslcapSubtype{Subtype: "s", Observed: true, LineCount: 2, Trigger: "t", TriggerFired: true},
			wantConclusive: true,
		},
		{
			name:           "unobserved with a fired trigger is the finding",
			sub:            oslcapSubtype{Subtype: "s", Trigger: "t", TriggerWitness: "w", TriggerFired: true},
			wantConclusive: true,
			wantFinding:    true,
		},
		{
			name:           "unobserved with a trigger that did not fire is a rig failure",
			sub:            oslcapSubtype{Subtype: "s", Trigger: "t", TriggerWitness: "w"},
			wantRigFailure: true,
		},
		{
			name:            "unobserved with no trigger at all is neither",
			sub:             oslcapSubtype{Subtype: oslcapSubtypeNotification},
			wantNoTriggerly: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sub := tc.sub
			sub.finish()
			if sub.Conclusive != tc.wantConclusive {
				t.Errorf("Conclusive = %v, want %v", sub.Conclusive, tc.wantConclusive)
			}
			if sub.Note == "" {
				t.Fatal("Note is empty; a reader of the fixture would have nothing to act on")
			}
			if got := strings.Contains(sub.Note, "finding about claude"); got != tc.wantFinding {
				t.Errorf("note reads as a finding about claude = %v, want %v\n  got: %s",
					got, tc.wantFinding, sub.Note)
			}
			if got := strings.Contains(sub.Note, "INCONCLUSIVE"); got != tc.wantRigFailure {
				t.Errorf("note reads as a rig failure = %v, want %v\n  got: %s",
					got, tc.wantRigFailure, sub.Note)
			}
			if got := strings.Contains(sub.Note, "no known trigger"); got != tc.wantNoTriggerly {
				t.Errorf("note reads as the no-trigger case = %v, want %v\n  got: %s",
					got, tc.wantNoTriggerly, sub.Note)
			}
		})
	}
}

// TestOslcapAwaitQuietTurnDoesNotDependOnAResult runs offline against a real
// dropcapRecorder fed by hand.
//
// The quiescence row is the load-bearing one. Three of this rig's five sends are a
// blocked prompt or a slash command, and #2138's header records that whether such a
// turn ever closes on its own is unmeasured — "a slash command replying with nothing
// is a plausible shape". A wait that only ended on a `result` would hang on exactly
// the cases this capture exists to observe and land no record at all. The budget row
// is the backstop under that, and it requires that a turn which has produced NOTHING
// is not mistaken for one that has gone quiet.
func TestOslcapAwaitQuietTurnDoesNotDependOnAResult(t *testing.T) {
	t.Parallel()
	const quiet = 150 * time.Millisecond
	tests := []struct {
		name  string
		feed  string
		want  string
		grace time.Duration
	}{
		{
			name:  "a result ends the turn",
			feed:  "{\"type\":\"assistant\"}\n{\"type\":\"result\",\"subtype\":\"success\"}\n",
			want:  oslcapTerminatedResult,
			grace: 5 * time.Second,
		},
		{
			name:  "lines then silence ends the turn without a result",
			feed:  "{\"type\":\"system\",\"subtype\":\"local_command_output\"}\n",
			want:  oslcapTerminatedQuiet,
			grace: 5 * time.Second,
		},
		{
			name:  "a turn that never produces a line ends on the budget, not on quiescence",
			feed:  "",
			want:  oslcapTerminatedBudget,
			grace: 400 * time.Millisecond,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := newDropcapRecorder()
			if tc.feed != "" {
				if _, err := recorder.Write([]byte(tc.feed)); err != nil {
					t.Fatalf("feeding the recorder: %v", err)
				}
			}
			if got := oslcapAwaitQuietTurn(recorder, 1, 0, quiet, tc.grace); got != tc.want {
				t.Errorf("oslcapAwaitQuietTurn() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOslcapPhaseAttributionUsesTheLineWindows runs offline.
//
// A frame's phase is what tells #2256-#2259 which trigger produced the line they are
// mapping, and it is derived from the recorder's index windows rather than from any
// content — five sends on one child means a line's own bytes cannot say which turn
// it belongs to. The preamble row matters: system/init lands before the first send,
// and attributing it to the first phase would claim the hook produced it.
func TestOslcapPhaseAttributionUsesTheLineWindows(t *testing.T) {
	t.Parallel()
	phases := []oslcapPhase{
		{Name: oslcapPhaseBlock, FirstLineIndex: 4},
		{Name: oslcapPhaseLiveness, FirstLineIndex: 9},
		{Name: oslcapPhaseLocalCommand, FirstLineIndex: 20},
	}
	tests := []struct {
		name  string
		index int
		want  string
	}{
		{"before the first send", 0, oslcapPhasePreamble},
		{"the last line before the first send", 3, oslcapPhasePreamble},
		{"the first line of the first phase", 4, oslcapPhaseBlock},
		{"inside the first phase", 8, oslcapPhaseBlock},
		{"the boundary belongs to the later phase", 9, oslcapPhaseLiveness},
		{"inside a middle phase", 19, oslcapPhaseLiveness},
		{"the last phase runs to the end", 999, oslcapPhaseLocalCommand},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := oslcapPhaseFor(phases, tc.index); got != tc.want {
				t.Errorf("oslcapPhaseFor(%d) = %q, want %q", tc.index, got, tc.want)
			}
		})
	}

	t.Run("no phases at all attributes everything to the preamble", func(t *testing.T) {
		t.Parallel()
		if got := oslcapPhaseFor(nil, 7); got != oslcapPhasePreamble {
			t.Errorf("oslcapPhaseFor on an empty phase list = %q, want %q", got, oslcapPhasePreamble)
		}
	})
}

// TestOslcapUnredactedPathFieldsNamesTheFieldNotTheValue runs offline.
//
// This is the third redaction mechanism and the only one this ticket adds, modelled
// on tncapUnredactedPathFields. The deny-scan's fixed needles cover four known roots
// and its dynamic ones cover this run's own paths; neither sees a path under some
// other root. #2251 found exactly that — claude's own messaging socket echoed into
// system/init — and two of this ticket's four subtypes are prose surfaces where such
// a value is plausible: a hook whose script fails prints an error naming its path,
// and a local command prints whatever it prints.
//
// The report carries the FIELD and never the value, because printing the value is
// the exposure the refusal exists to prevent.
func TestOslcapUnredactedPathFieldsNamesTheFieldNotTheValue(t *testing.T) {
	t.Parallel()
	frame := func(index int, payload string) oslcapFrame {
		return oslcapFrame{Index: index, PayloadEncoding: dropcapEncodingJSONString, Payload: payload}
	}
	t.Run("a surviving absolute path is reported by field", func(t *testing.T) {
		t.Parallel()
		got := oslcapUnredactedPathFields([]oslcapFrame{
			frame(7, `{"type":"system","subtype":"informational","text":"/opt/claude/hooks/x.sh: not found"}`),
		})
		if len(got) != 1 {
			t.Fatalf("oslcapUnredactedPathFields() = %v, want exactly one report", got)
		}
		if !strings.Contains(got[0], "text") || !strings.Contains(got[0], "7") {
			t.Errorf("report %q names neither the field nor the frame index", got[0])
		}
		if strings.Contains(got[0], "/opt/claude") {
			t.Errorf("report %q carries the VALUE; naming it is the exposure this sweep prevents", got[0])
		}
	})

	t.Run("a nested path is found and its position named", func(t *testing.T) {
		t.Parallel()
		got := oslcapUnredactedPathFields([]oslcapFrame{
			frame(1, `{"commands":[{"name":"cost"},{"name":"probe","source":"/etc/claude/cmd.md"}]}`),
		})
		if len(got) != 1 || !strings.Contains(got[0], "source") {
			t.Fatalf("oslcapUnredactedPathFields() = %v, want one report naming the nested field", got)
		}
	})

	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"a redacted path is not a finding", `{"cwd":"$WORKDIR/.claude/commands/probe.md"}`},
		{"a bare slash command is not a path", `{"command":"/cost"}`},
		{"prose with no leading slash is not a path", `{"text":"see the docs at code.claude.com/docs"}`},
		{"an empty payload reports nothing", `{}`},
		{"an undecodable payload reports nothing rather than panicking", `{"text":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := oslcapUnredactedPathFields([]oslcapFrame{frame(0, tc.payload)}); len(got) != 0 {
				t.Errorf("oslcapUnredactedPathFields() = %v, want none", got)
			}
		})
	}

	t.Run("a base64 frame is skipped rather than mis-read", func(t *testing.T) {
		t.Parallel()
		f := oslcapFrame{Index: 2, PayloadEncoding: dropcapEncodingBase64, PayloadB64: "L1VzZXJzL3g="}
		if got := oslcapUnredactedPathFields([]oslcapFrame{f}); len(got) != 0 {
			t.Errorf("oslcapUnredactedPathFields() = %v on a base64 frame; fixtureWorthy already "+
				"refuses those on the encoding alone, and decoding one here would duplicate the "+
				"deny-scan's own base64 pass", got)
		}
	})
}

// TestOslcapRigAuthoredProseCarriesNoDenyNeedle runs offline and is #2247's lesson
// applied before it can cost anything.
//
// Every string this file authors ends up inside a record that dropcapScanner seals
// FAIL-CLOSED: on a hit nothing is written at all, not the record and not the
// fixture. A deny needle spelled out in this file's own prose therefore destroys the
// evidence of the very run that would have produced it — #2247's first live lap died
// exactly that way, on a rationale that spelled out the prefixes
// dropcapFixedNeedles searches for, and it cost a full gate run and a real token
// spend to find a defect a string comparison finds here.
func TestOslcapRigAuthoredProseCarriesNoDenyNeedle(t *testing.T) {
	t.Parallel()

	// The fixed half only. The dynamic needles are the live run's own paths, which no
	// offline test can know and which cannot appear in a compile-time constant.
	scanner := dropcapScanner{needles: dropcapFixedNeedles()}

	t.Run("the record's rig-authored seed", func(t *testing.T) {
		t.Parallel()
		blob, err := json.Marshal(oslcapSeedRecord())
		if err != nil {
			t.Fatalf("marshal the seed record: %v", err)
		}
		if hits, _ := scanner.scan(blob); len(hits) > 0 {
			t.Errorf("oslcapSeedRecord carries deny class(es) %v in its OWN constants, so the live "+
				"probe fails its write closed and produces nothing: name the prefixes by symbol "+
				"(dropcapFixedNeedles) rather than spelling them out", hits)
		}
	})

	t.Run("every subtype note arm", func(t *testing.T) {
		t.Parallel()
		for _, sub := range []oslcapSubtype{
			{Subtype: "s", Observed: true, LineCount: 1, Trigger: "t", TriggerWitness: "w", TriggerFired: true},
			{Subtype: "s", Trigger: "t", TriggerWitness: "w", TriggerFired: true},
			{Subtype: "s", Trigger: "t", TriggerWitness: "w"},
			{Subtype: oslcapSubtypeNotification},
		} {
			sub.finish()
			if hits, _ := scanner.scan([]byte(sub.Note)); len(hits) > 0 {
				t.Errorf("the note for observed=%v fired=%v carries deny class(es) %v",
					sub.Observed, sub.TriggerFired, hits)
			}
		}
	})

	t.Run("the rig files this probe writes", func(t *testing.T) {
		t.Parallel()
		// A placeholder path, so the test's own input carries no needle and the only
		// thing under scan is this file's constants.
		script, err := oslcapHookScript("WITNESS", oslcapBlockMarker, oslcapBlockReason)
		if err != nil {
			t.Fatalf("oslcapHookScript: %v", err)
		}
		for name, content := range map[string]string{
			"the hook script":       script,
			"the settings file":     oslcapSettingsJSON("SCRIPT"),
			"the command file body": oslcapCommandBody("TOKEN"),
			"the block prompt":      oslcapBlockPrompt(1),
			"the liveness prompt":   oslcapLivenessPrompt(1),
		} {
			if hits, _ := scanner.scan([]byte(content)); len(hits) > 0 {
				t.Errorf("%s carries deny class(es) %v", name, hits)
			}
		}
	})

	t.Run("the net reddens on a needle", func(t *testing.T) {
		t.Parallel()
		// Non-vacuity, established without mutating the file: append a needle to the
		// one field the measured defect was in. If this arm passes, the three above
		// prove nothing — an empty needle list would make them green forever.
		rec := oslcapSeedRecord()
		rec.RedactionRationale += " and a stray /Users/ prefix spelled out in prose"
		blob, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal the seeded record: %v", err)
		}
		hits, _ := scanner.scan(blob)
		if !oslcapContains(hits, dropcapDenyUsers) {
			t.Errorf("scanning a seed record whose rationale carries that prefix did not report %q; "+
				"hits = %v, so the arms above are vacuous", dropcapDenyUsers, hits)
		}
	})
}
