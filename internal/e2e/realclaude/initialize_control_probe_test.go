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
// # Three arms, one drive sequence (#1763)
//
// #1688 measured ONE send point — after a completed turn — and committed the
// bytes. What that could not settle is WHICH send points are answered: the daemon
// would rather ask once at spawn than pay for a turn first, and #1689's trigger is
// placed on whichever answer the measurement returns. #1763 drives one child per
// row of initControlArms:
//
//	arm                  | send point                | control request
//	---------------------+---------------------------+----------------
//	before_first_turn    | before turn 1's line      | written
//	after_completed_turn | between the two turns     | written
//	control_no_request   | between the two turns     | (none)
//
// EVERY ARM DRIVES A FULL TURN AFTER ITS SEND POINT, the control included. Claude
// emits `system`/`init` per turn rather than at spawn, so "no further init line
// after the request" observed without driving a further turn is empty by
// construction rather than a measurement — runSetModeChild records this in its own
// words and drives turn 2 on its control arms for exactly that reason. The control
// arm writes nothing and still reads an anchor, at the equivalent point its drive
// sequence reaches, which is what makes the three windows comparable and what
// #1764's cross-arm comparison subtracts.
//
// The send point is placed by the arm's two columns rather than by a switch on its
// id: initControlArms is the single source of truth for the arm set, and a second
// spelling of an identifier here is what its distinctness lock exists to prevent.
//
// The recorded arm is the send point the run INTENDED. It is not a claim that the
// turns completed: a probe turn can produce no result line inside its budget, in
// which case the log below says so and turn_boundaries is what tells a reader
// which turns closed.
//
// # What passes
//
// A response carrying subtype:"error" is a refusal, and a recorded refusal is a
// PASSING outcome — the error text is the input to #1689. AN ARM THAT GOES
// UNANSWERED IS ALSO A PASSING OUTCOME, and it is the reading this run exists to
// take: whether a pre-turn ask is answered at all is the unknown, so an unanswered
// before_first_turn is the measurement rather than a failed run. #1688's fatal on
// an absent control_response is deleted for that reason, along with the one-arm
// test that carried it — which since #1722 also wrote the same filename this run's
// after_completed_turn arm writes.
//
// The run fails only on a broken instrument or a fail-closed refusal: a spawn
// failure, an arm that captured no stdout at all, or the writer's deny-scan
// refusing a record. Send the minimal request shape and DO NOT ITERATE against a
// live child; the wall-clock and token risk here is the run, not the typing.
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
//	  -run 'TestRealClaude_InitializeControl|TestInitControlSummarize_|TestInitControlChildBudget_|TestInitControlScanApplied_' \
//	  ./internal/e2e/realclaude/
//
// The three non-live tests in this file — the summariser's table, the child
// budget's (TestInitControlChildBudget_...) and the arming completion's
// (TestInitControlScanApplied_...) — spawn nothing and must report PASS, not SKIP,
// on a machine with no claude and no credentials.
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

	// The two probe turns, shared by all three arms. They exist only to place the
	// send point inside a drive sequence, so both are deliberately TOOL-FREE —
	// unlike runSetModeChild's Bash probes. A tool-free turn cannot stall on a
	// permission prompt it can never receive, which is why this run needs no
	// --dangerously-skip-permissions and cannot hit the `default`-posture hang
	// #1595 budgets two minutes for. Do not "make the probes use Bash like #1595":
	// that reintroduces the flag and hands three unsandboxed children tool access
	// for no measurement gain.
	//
	// THEY DIFFER FROM EACH OTHER so turn 2 is a fresh request rather than one
	// claude can answer with "I already did that" — setModePromptOne and
	// setModePromptTwo carry the same reason. A turn claude short-circuits is not
	// the "full turn after the send point" every arm owes its window.
	initControlPromptOne = "Reply with the single word: ready. Do not use any tools."
	initControlPromptTwo = "Reply with the single word: done. Do not use any tools."

	// Cost. The model list claude reports is a property of the BINARY, not of
	// the model answering the probe turn.
	initControlModel = "claude-haiku-4-5"

	// Cost guard with ~2x headroom over the two assistant turns two tool-free
	// probes need. Unchanged by #1763's second turn, which is what that headroom
	// was already sized for. A result line carrying subtype:"error_max_turns"
	// lands in the fixture plainly — raise and rerun.
	initControlMaxTurns = "4"

	// A correlation token, not a security token — the same reason
	// (*Runner).Interrupt mints its id from a monotonic counter rather than a
	// random source. A fixed literal keeps the committed fixture diffable, and it
	// stays ONE literal across #1763's arms rather than becoming per-arm: the two
	// writing arms are separate children, so there is no correlation ambiguity to
	// resolve and a per-arm id would only make the artifacts harder to diff.
	initControlRequestID = "initialize-control-1"
)

const (
	// Hard kill per child, and the outer bound. IT DOMINATES THE PER-STEP WAITS
	// BELOW: 300s against the 225s an arm that drives two turns and waits for a
	// control_response can spend, with 75s of residual for the steps the sum does
	// not count — cmd.Start, the stdin close, cmd.Wait, the reader join and the
	// inter-step overhead.
	//
	// That ordering is what makes context_deadline_tripped a MEASUREMENT rather
	// than an artefact of the harness. Every per-step budget exists so an absence
	// means absence rather than impatience — initControlControlBudget's own doc
	// says so — and a deadline that can fire first voids that guarantee for exactly
	// the reading this family takes. Now that it cannot, a trip means a genuinely
	// stalling claude. #1763 raised it from 3 minutes for the second turn;
	// TestInitControlChildBudget_ExceedsEveryArmsPerStepWaitSum is what enforces the
	// relation, deriving both sides from these constants rather than from literals.
	initControlChildBudget = 5 * time.Minute

	// One probe turn, and every arm drives two of them. A tool-free turn lands in
	// seconds.
	initControlTurnBudget = 90 * time.Second

	// Long enough that an absent control_response means absence, not impatience.
	// Same value and same reason as setModeControlBudget.
	initControlControlBudget = 45 * time.Second
)

// initControlArmWaitSum returns the largest total arm's drive sequence can spend in
// PER-STEP WAITS: the two turn waits every arm drives, plus the control wait on an
// arm that sends a request.
//
// IT MIRRORS runInitControlChild's SEQUENCE AND MUST BE GROWN WITH IT. Nothing
// couples the two — a third turn added to that driver and not to this sum leaves
// TestInitControlChildBudget_ExceedsEveryArmsPerStepWaitSum green over an
// arithmetic that no longer describes the run, which is the one way that test can
// pass while saying nothing.
//
// It counts ONLY the bounded waits. cmd.Start, the stdin close, cmd.Wait, the
// reader join and the inter-step overhead are uncounted; the residual between this
// sum and initControlChildBudget is what covers them, and that residual is a
// judgement rather than something any assertion here demands.
func initControlArmWaitSum(arm initControlArm) time.Duration {
	sum := 2 * initControlTurnBudget
	if arm.sendsRequest {
		sum += initControlControlBudget
	}
	return sum
}

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

// --- the arming census ----------------------------------------------------------

// initControlScanPathClasses names the classes newDropcapScanner arms through
// addDynamicPath, and it is a SECOND, INDEPENDENT COPY of that constructor's
// addDynamicPath calls rather than a read of them. That is deliberate, and the
// drift costs differ in the two directions, so both are worth stating: a class
// added to that constructor and not here VANISHES from the recorded map whenever
// its value is empty — which is the exact defect this list exists to close for
// artifact_dir — while a name here that no scanner arms writes a `false` key for a
// class that does not exist. TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath's
// staleness control binds three of the four names; operator_home is carved out
// there for a reason its message gives.
//
// The two credential classes and the five fixed literals are DELIBERATELY ABSENT.
// They cannot vanish: addDynamic appends its needle unconditionally, so an unset
// CLAUDE_CODE_OAUTH_TOKEN lands as `false` rather than as no key at all, and the
// fixed needles are authoring-time literals that are never empty.
var initControlScanPathClasses = []string{
	dropcapClassTempHome,
	dropcapClassOperatorHome,
	dropcapClassArtifactDir,
	dropcapClassWorkdir,
}

// initControlScanApplied returns s.applied() with every path class PRESENT — the
// completion #1747 exists for, and it belongs here at the fill site rather than in
// dropcapScanner, which is shared with the dropcap family and whose record shape is
// already committed.
//
// The two arming paths behave differently for an absent value, and only one of them
// behaves the way "an unset credential arms nothing" suggests. addDynamic appends
// unconditionally, so a class handed "" lands as `false`. addDynamicPath goes
// through dropcapPathSpellings, which returns nil for "" — so NO needle is
// appended, and applied, which builds its map by ranging the needles, carries no
// key for that class AT ALL. This run hands newDropcapScanner no artifact directory,
// so artifact_dir would vanish from the record outright: an omitted class reads
// exactly like a class nobody ever thought about, which is the outcome the field
// exists to prevent. operator_home has the identical hole whenever realHome is
// empty, so all four are closed here rather than only the one that is certain.
//
// Only a MISSING key is added. An existing entry — true or false — is left alone: a
// completion that assigned false unconditionally would report every armed class as
// armed-nothing while still satisfying the artifact_dir check, and the workdir
// control in TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath
// is its sole red.
//
// applied returns a FRESH map per call, so this mutates a map it owns and no
// caller's value is shared. Do not take a defensive second copy.
func initControlScanApplied(s dropcapScanner) map[string]bool {
	out := s.applied()
	for _, class := range initControlScanPathClasses {
		if _, ok := out[class]; !ok {
			out[class] = false
		}
	}
	return out
}

// --- the driver ---------------------------------------------------------------

// runInitControlChild spawns one child under pyry's stream-json-in/stream-json-out
// argv, drives arm's two-turn sequence with the send point placed where that arm's
// columns say, reads whatever came back, writes the fixture and returns the
// completed record BESIDE THE PATH IT WAS WRITTEN TO.
//
// The arm arrives as a PARAMETER, positioned after workdir the way runSetModeChild
// takes its own; the local initControlProbedArm() call it replaced is the change
// that doc reserved, and both that helper and the `probed` column went with it.
//
// THE SECOND RETURN VALUE IS THE WRITTEN PATH, which the writer already mints and
// returns and which this driver used to drop into a log line.
// TestRealClaude_InitializeControl_SendPointArms' write-set assertion consumes it.
// Do NOT put it on initControlFixtureRecord instead: it is an absolute path under
// the operator's pinned $HOME, so a field would commit an operator path into a
// public artifact and put a new string-bearing field in front of the redaction pass
// and the deny-scan for no gain.
//
// It t.Fatalf's ONLY for a broken instrument or a fail-closed refusal — a pipe or
// spawn failure, a marshal failure, a credential in the child's stderr, zero stdout
// lines captured, or the writer's deny-scan refusing the record. Every other
// outcome is information and lands in a fixture field: a probe turn that never
// closed, a control_response that never came, one that came with subtype "error", a
// mismatched request_id, a stdin write error, an over-long line, a non-zero exit, a
// tripped deadline. #1688's fatal on an absent control_response is deliberately
// gone: an unanswered send point is the measurement #1763 exists to take.
//
// SINCE #1733 IT TAKES A REDACTOR RATHER THAN THREE MORE PATH STRINGS. The
// alternative — operatorHome, tempHome and tempDir threaded in beside the workdir
// already here — makes this signature eight positional parameters, six of them
// strings, and puts the four-argument construction at two sites instead of one. A
// transposition of operatorHome and tempHome is silent (both install rules, both
// produce a placeholder, just the wrong one) and no offline test can see it,
// because every offline row builds its own redactor. One construction site is one
// place to get that order right. It also leaves the live loop free to mint a fresh
// redactor per arm, WHICH IT MUST: dropcapRedactor's counters are unlocked and its
// census accumulates across every call, so a shared redactor would both race and
// report one arm's substitutions against another's.
//
// SINCE #1747 IT TAKES A SCANNER TOO, and it arrives by the same route and for the
// same reason: one construction site is one place to get newDropcapScanner's
// parameter order right, and that site is where the pinned $HOME — which plays
// tempHome — is in hand. Unlike newInitControlRedactor, newDropcapScanner reads
// realHome and os.Getenv ITSELF, so the call site needs neither in hand. The live
// loop builds ONE and shares it across the three arms; the paragraph below is why
// that is safe where sharing a redactor is not.
//
// A SCANNER IS NOT A REDACTOR, and a reader will otherwise guess this asymmetry the
// wrong way round. dropcapRedactor accumulates unlocked counters across every call,
// which is why a fresh one per arm is mandatory. dropcapScanner is append-only
// during construction and read-only afterwards — addDynamic and addDynamicPath are
// pointer-receiver and run only inside newDropcapScanner, while applied and scan
// are value receivers that read the needles and allocate their own results — so a
// scanner shared across arms would neither race nor carry one arm's state into
// another's record. Do not copy the redactor's per-arm rule to it for a reason that
// does not apply, and do not read this as licence to share a redactor.
//
// THE SCANNER VALUE IS CREDENTIAL-BEARING: NEVER FORMAT IT. newDropcapScanner reads
// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY and stores those values in the
// needles, so a dropcapScanner in scope is TWO LIVE CREDENTIALS IN A STRUCT — and
// before #1747 no such value existed anywhere in this file. A %v, %+v, %#v or %q on
// the scanner, on one of its needles, or on the needle slice prints sk-ant-… into a
// run log this pipeline salvages. initControlScrubbed does NOT catch it: that guard
// reads the CHILD's stderr, and this would be the harness's own output. This
// instruction is the only guard, which is why it is written here at the site rather
// than only in the ticket.
//
// applied's MAP is safe to print, and it is the diagnostic worth printing — a
// reader who knows only "the scanner is dangerous" writes a failure message with
// nothing in it. Its keys are declared vocabulary (the two environment-variable
// NAMES, the dropcapClass* constants, the deny-class identifiers) and its values
// are bools; applied keys by the needle's class and never by its value, so no
// needle, path or credential can reach a key. The dropcap file states this rule for
// its own site and dropcapWriteRecord already takes a scanner by value under it;
// this carries the same rule to the second site rather than inventing one.
//
// Handing this driver the finished map[string]bool instead of the scanner would
// keep the credential values out of it entirely, and that is rejected deliberately:
// #1748 needs the SCANNER here, to scan the bytes the writer produced, and splitting
// the two would move the parameter twice.
func runInitControlChild(t *testing.T, claudeBin, workdir string, arm initControlArm,
	red *dropcapRedactor, scanner dropcapScanner, versionRaw, versionToken string) (*initControlFixtureRecord, string) {
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
		t.Fatalf("#1763[%s]: stdin pipe: %v", arm.id, err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("#1763[%s]: stdout pipe: %v", arm.id, err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("#1763[%s]: start claude: %v", arm.id, err)
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

	turnOne, err := setModeTurnLine(initControlPromptOne)
	if err != nil {
		t.Fatalf("#1763[%s]: %v", arm.id, err)
	}
	turnTwo, err := setModeTurnLine(initControlPromptTwo)
	if err != nil {
		t.Fatalf("#1763[%s]: %v", arm.id, err)
	}

	var (
		controlSent    json.RawMessage
		requestID      string
		sendPointIndex int
		withinWait     bool
	)

	// THE SEND POINT: one closure, called from exactly one of the two ifs below, so
	// the two placements cannot drift apart. It is what arm.sendPointAfterFirstTurn
	// positions and what arm.sendsRequest decides the content of — never a switch on
	// arm.id, which would be a second spelling of an identifier initControlArms
	// already declares.
	sendPoint := func() {
		// #1762: the send-point anchor, read HERE rather than beside the record
		// literal below, and read UNCONDITIONALLY — the control arm reads it too, at
		// the equivalent point its drive sequence reaches, which is what makes the
		// three arms' windows comparable. Nothing claude writes in RESPONSE to the
		// request may fall before it, and only a read taken before the write
		// guarantees that; reading it afterwards inverts the residual. The residual
		// that remains runs the safe way — a line arriving between this read and the
		// write is counted inside the window although it preceded the request.
		//
		// len(rec.snapshotLines()) is the accessor because setModeRecorder has no
		// cheaper one, and adding one changes a type the set_permission_mode family
		// shares.
		sendPointIndex = len(rec.snapshotLines())
		if !arm.sendsRequest {
			// A control arm stops here: no request id, no line, no wait.
			// ControlRequestID stays "", ControlRequestSent stays nil,
			// ControlResponseWithinWait stays false — and false is HONEST rather than
			// ambiguous, because no wait ran and so none was satisfied. A reader
			// separates "the wait expired" from "no wait ran" by `arm` and by
			// control_request_sent: null. DO NOT ADD A PRESENCE FLAG OR A FOURTH
			// STATE; the record's SendPointIndex paragraph bans exactly this move one
			// field away, for the same reason.
			return
		}
		requestID = initControlRequestID
		controlLine, err := initControlLine(requestID)
		if err != nil {
			t.Fatalf("#1763[%s]: %v", arm.id, err)
		}
		controlSent = json.RawMessage(bytes.TrimRight(controlLine, "\n"))
		t.Logf("#1763[%s]: writing control request: %s", arm.id, controlSent)
		writeLine("control request", controlLine)
		// The wait's own RESULT, kept rather than dropped into the log. Snapshotting
		// ControlResponses happens after cmd.Wait() below, so a response arriving
		// past this budget still lands in that field — under a log line that already
		// claimed absence. control_response_within_wait is the only place that
		// distinction survives the run; see its paragraph on
		// initControlFixtureRecord.
		withinWait = setModeWaitFor(rec.controlResponseCount, 1, initControlControlBudget)
		if !withinWait {
			t.Logf("#1763[%s]: no control_response within %s (recorded as absence, continuing) — "+
				"an unanswered send point is this run's measurement, not its failure",
				arm.id, initControlControlBudget)
		}
	}

	if !arm.sendPointAfterFirstTurn {
		sendPoint()
	}
	writeLine("turn 1", turnOne)
	if !setModeWaitFor(rec.resultCount, 1, initControlTurnBudget) {
		t.Logf("#1763[%s]: turn 1 produced no result line within %s, so whatever this arm's "+
			"sequence reaches next is NOT reached from a completed turn. Recorded rather than "+
			"fatal — turn_boundaries is what tells a reader which turns closed. Continuing",
			arm.id, initControlTurnBudget)
	}
	if arm.sendPointAfterFirstTurn {
		sendPoint()
	}

	// Turn 2 is driven unconditionally, on the control arm too. It is what makes
	// recording an absent init line legitimate: claude emits `system`/`init` per turn
	// rather than at spawn, so a window read without a further turn is empty by
	// construction rather than measured. runSetModeChild drives its control arms'
	// turn 2 for the same reason.
	//
	// The baseline is READ rather than hardcoded to 2, copied from that driver: it is
	// what keeps this wait correct when turn 1 produced no result line inside its own
	// budget.
	baseline := rec.resultCount()
	writeLine("turn 2", turnTwo)
	if !setModeWaitFor(rec.resultCount, baseline+1, initControlTurnBudget) {
		t.Logf("#1763[%s]: turn 2 produced no result line within %s (recorded rather than fatal, "+
			"continuing) — turn_boundaries is what tells a reader which turns closed",
			arm.id, initControlTurnBudget)
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
		t.Fatalf("#1763[%s]: claude produced no stdout; there is nothing to capture\nstderr:\n%s\nwaitErr: %v",
			arm.id, truncateString(stderrBuf.String(), stderrFixtureCap), waitErr)
	}

	// #1762: ONE anchor and ONE slice expression, so the two window reads cannot
	// disagree about which window they measured and no line is counted both before
	// the send point and inside it. Deliberately NOT the difference of two
	// snapshotInitModes() calls: those take the lock separately, and a line landing
	// between them is attributed to the wrong side of the anchor.
	window := initControlReadWindow(lines, sendPointIndex)

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

		Arm: arm.id,

		Argv:    append([]string{claudeBin}, argv...),
		Prompts: []string{initControlPromptOne, initControlPromptTwo},

		ControlRequestID:       requestID,
		ControlRequestSent:     controlSent,
		ControlResponses:       responses,
		ControlResponseSubtype: summary.subtype,
		// setModeResponseIDMatches already returns false for an empty id without a
		// branch of its own, which is what the control arm reaches here.
		ControlResponseRequestIDMatched: setModeResponseIDMatches(responses, requestID),
		ControlResponseWithinWait:       withinWait,

		ModelsPresent:     summary.modelsPresent,
		ModelsCount:       summary.modelsCount,
		ModelsEntryFields: summary.modelsEntryFields,

		StdoutEvents:     lines,
		NonJSONLineCount: rec.nonJSONCount(),
		TurnBoundaries:   rec.snapshotBoundaries(),

		SendPointIndex:                sendPointIndex,
		AfterSendPointSystemInitCount: window.systemInitCount,
		AfterSendPointResultTrailers:  window.resultTrailers,

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

		// #1747: assigned HERE, in the literal, from the scanner the capture site
		// built. The value is available at literal time — unlike Redaction, which is
		// assigned below from what the redaction pass returns — and the sibling
		// family's construction-site `CredentialScanApplied: scanner.applied()` is the
		// precedent. Not in writeInitControlFixture, whose `out := *rec` copy must go
		// on receiving a record it only copies.
		//
		// initControlScanApplied rather than scanner.applied(): this run hands the
		// constructor no artifact directory, and the raw call omits that class's key
		// outright rather than recording it as armed-nothing. See that helper.
		CredentialScanApplied: initControlScanApplied(scanner),
	}

	// #1733: the redaction pass. It sits between the record literal and the write,
	// so writeInitControlFixture — whose `out := *rec` copy and StderrCapture cap
	// are #1729's byte-identity subject — is unchanged and still receives a record
	// it only copies.
	//
	// Position alone does NOT protect the two log sites below that would otherwise
	// leak what the fixture no longer carries — the loop over the captured
	// control_responses, and the summary line that logs scanner_error. Both land in
	// a run log this pipeline salvages, and both must read the RECORD'S OWN fields
	// to see the redacted bytes: the pass assigns fresh values rather than writing
	// through what it was handed, so scanner_error is safe because the summary line
	// reads record.ScannerError, and the response loop is safe only because it
	// ranges record.ControlResponses. Ranging the pre-pass local `responses` there
	// printed the unredacted bytes past a correctly placed pass — measured on the
	// first cut of this slice, and the reason the placement claim is stated per log
	// site rather than per position.
	//
	// #1731: the census is assigned ONTO THE RECORD, and HERE is where — at the
	// fill site, from what the pass RETURNS. Not inside redactInitControlRecord,
	// which reports the census and stores nothing and whose
	// TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical is the sole
	// red for a pass that stores it; and not inside writeInitControlFixture, which
	// takes an `out := *rec` copy and must go on receiving a record it only
	// copies. The sibling family's dropcapWriteRecord assigns in ITS writer and is
	// the counter-example here, not the model; the precedent to follow is that
	// same family's construction-site `CredentialScanApplied: scanner.applied()`.
	//
	// The log line carries class names, replacements and counts only, never a
	// value, so it cannot leak one — and that clause now constrains the COMMITTED
	// FILE as well as a salvaged run log, because since #1731 the same values go
	// both places. It is still not a widening of the "never %+v the record" rule:
	// the census is not child output. It reads the record's own field for the
	// reason the two log sites above do.
	record.Redaction = redactInitControlRecord(red, record)
	t.Logf("#1733[%s]: redaction applied: %+v", arm.id, record.Redaction)

	// packageDir is os.Getwd(), which under `go test` is this package's own
	// source directory — no untrusted component anywhere in it. Choosing dir is
	// the caller's job, which initControlFixtureName's doc hands over explicitly;
	// this is the call site that discharges it.
	//
	// #1748: the scanner this driver already holds goes with it. The writer runs the
	// deny-scan over the marshalled record BEFORE its first filesystem call, so a
	// value #1733's table did not predict aborts the run with no fixture written —
	// where it previously wrote one. A reviewer who sees that has found a gap in the
	// table, not a bug in the writer. It also means every log site below is reached
	// only after the scan passed.
	path := writeInitControlFixture(t, filepath.Join(packageDir(t), "testdata"), scanner, record)

	// Never %+v the record into a log or a fatal message: that moves up to
	// stderrFixtureCap bytes of child output out of the bounded file and into an
	// unbounded run log, the exact thing the cap exists to prevent. NEVER FORMAT THE
	// SCANNER either — it is two live credentials in a struct, and this driver holds
	// one; see the paragraph above.
	//
	// arm, within_wait and the two window counts are safe to add for the reason
	// claude_version is: none is child output. Do not widen this further toward the
	// record's other fields.
	t.Logf("#1763[%s]: %d line(s), %d non-JSON, %d control_response(s), within_wait=%v, "+
		"subtype=%q, models_present=%v models_count=%d models_entry_fields=%v, "+
		"request_id matched=%v, send_point_index=%d, window init=%d trailers=%d, "+
		"exit=%d, deadline_tripped=%v, scanner_error=%q, %s",
		record.Arm, len(lines), record.NonJSONLineCount, len(responses),
		record.ControlResponseWithinWait, record.ControlResponseSubtype,
		record.ModelsPresent, record.ModelsCount, record.ModelsEntryFields,
		record.ControlResponseRequestIDMatched, record.SendPointIndex,
		record.AfterSendPointSystemInitCount, len(record.AfterSendPointResultTrailers),
		exitCode, record.ContextDeadlineTripped,
		record.ScannerError, duration.Round(time.Millisecond))
	// record.ControlResponses, never the pre-pass local `responses`. The pass
	// builds a fresh slice of fresh byte slices — initControlRedactRaws allocates
	// and redact returns bytes.ReplaceAll's result — so nothing it does writes
	// through the local, and a loop ranging it prints exactly the bytes the
	// fixture no longer carries. Redacted rather than verbatim since #1733.
	for i, resp := range record.ControlResponses {
		t.Logf("#1763[%s]: control_response[%d] redacted: %s", arm.id, i, resp)
	}
	t.Logf("#1763[%s]: fixture written: %s", arm.id, path)

	return record, path
}

// --- the tests ----------------------------------------------------------------

// TestRealClaude_InitializeControl_SendPointArms drives ONE LIVE CHILD PER ROW of
// initControlArms through the identical two-turn sequence — the control request
// written before the first user turn, written after a completed turn, and not
// written at all — and commits one artifact per arm.
//
// It PASSES ON EVERY RECORDED OUTCOME. A response carrying subtype:"error" is a
// refusal and its error text is what #1689 designs the accepted shape against; an
// arm that goes UNANSWERED is the measurement this run exists to take, since
// whether a pre-turn `initialize` is answered at all is precisely the unknown. A
// tripped deadline is likewise recorded rather than fatal: since #1763 raised
// initControlChildBudget past every arm's per-step wait sum, a trip can only mean a
// genuinely stalling claude, which is itself a measurement. The run fails only on a
// broken instrument or a fail-closed refusal.
//
// It REPLACES #1688's one-arm TestRealClaude_InitializeControl_Capture, and the
// deletion is a correctness requirement rather than an economy: since #1722
// writeInitControlFixture mints its path from
// initControlArmFixtureName(rec.ClaudeVersion, rec.Arm), so that test and this
// run's after_completed_turn arm write the SAME filename — two live children racing
// for one path, last writer wins, nothing red anywhere.
//
// NO CROSS-ARM VERDICT IS COMPUTED HERE. Subtracting the control arm's window from
// the two measurement arms' is #1764's, and a verdict computed in two places is a
// second source of truth.
func TestRealClaude_InitializeControl_SendPointArms(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials

	workdir := filepath.Join(home, initControlWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#1763: create workdir: %v", err)
	}

	versionRaw, versionToken := captureClaudeVersion(t)
	t.Logf("#1763: claude version %q (token %q)", versionRaw, versionToken)

	// #1747's net, built HERE for the redactor's reason: one construction site is one
	// place to get the parameter order right, and this is where `home` — which plays
	// tempHome — is in hand. This constructor reads realHome and os.Getenv itself, so
	// unlike the line above this site names neither.
	//
	// THE EMPTY SLOT IS THE SECOND PARAMETER, artifactDir, and the emptiness is the
	// fact the record is recording rather than an omission to tidy up. The signature
	// is newDropcapScanner(tempHome, artifactDir, workdir): passing workdir into the
	// middle slot would arm artifact_dir with the workdir path and leave workdir
	// unarmed, and MINTING a directory to fill it would report an arming that never
	// happened. initControlScanApplied is what keeps the empty class PRESENT in the
	// record as armed-nothing instead of missing from it.
	//
	// ONE SCANNER, SHARED ACROSS THE THREE ARMS, and built OUTSIDE the loop. That is
	// safe for the reason runInitControlChild's doc gives — a scanner is append-only
	// during construction and read-only afterwards — and it is deliberately NOT the
	// rule the redactor below follows. Do not read either one as licence for the
	// other.
	//
	// The value carries the two credentials os.Getenv returned. Never %v it — see
	// runInitControlChild's doc.
	scanner := newDropcapScanner(home, "", workdir)

	// Sequential t.Run, no t.Parallel at any level: one child, one reader goroutine
	// and one pinned $HOME at a time, exactly as runSetModeChild's four arms already
	// share theirs. Each arm's fixture is on disk before its subtest returns, so the
	// write-set check below runs against artifacts a human can already read.
	paths := make(map[string]string, len(initControlArms))
	for _, arm := range initControlArms {
		t.Run(arm.id, func(t *testing.T) {
			// #1733's table, built HERE — INSIDE the loop, FRESH PER ARM, and that is
			// mandatory rather than tidy: dropcapRedactor's counters are unlocked and
			// its census accumulates across every call, so one shared across three
			// arms would both race under -race and commit one arm's substitution
			// counts into another arm's artifact. A census that misreports which
			// classes fired is an audit trail that lies.
			//
			// realHome and os.TempDir() are legitimate at this site and nowhere else
			// in the family: this file execs and correctly carries no
			// finOfflineExecBans entry, while the file the construction lives in bans
			// both by name — and a file that named either could not honestly carry
			// that ban entry.
			//
			// Do NOT re-trim os.TempDir()'s trailing slash: #1732 moved that trim
			// inside the construction, where it is the one permitted normalisation.
			// realHome may be empty when HOME was unset at launch; add drops an
			// empty-valued rule, so this site needs no guard of its own.
			red := newInitControlRedactor(realHome, home, workdir, os.TempDir())

			record, path := runInitControlChild(t, claudeBin, workdir, arm, red, scanner,
				versionRaw, versionToken)
			paths[arm.id] = path

			// One per-arm summary line at the top level; the driver already logs the
			// rest. It names the arm's two behaviour columns so a reader can check the
			// artifact against the sequence that produced it without reading the
			// table. Nothing here is child output, and nothing here formats the
			// scanner.
			t.Logf("#1763[%s]: send point after first turn=%v, request written=%v, "+
				"within_wait=%v, send_point_index=%d",
				arm.id, arm.sendPointAfterFirstTurn, arm.sendsRequest,
				record.ControlResponseWithinWait, record.SendPointIndex)
		})
	}

	// AC 5's write-set check, over the paths the run ACTUALLY wrote.
	//
	// A partial run cannot answer a set claim, so it reports UNAVAILABLE instead of
	// asserting one: a -run filter or an instrument fatal in one subtest leaves that
	// arm with no path, and a filtered run is not "one suite run".
	// TestRealClaude_SetPermissionMode_InBandProbe's `missing` guard is the
	// precedent, and computing a set claim from a partial run is how a green run
	// comes to mean nothing.
	var missing []string
	for _, arm := range initControlArms {
		if paths[arm.id] == "" {
			missing = append(missing, arm.id)
		}
	}
	if len(missing) > 0 {
		t.Logf("#1763: write-set check UNAVAILABLE — arm(s) %v produced no written path (a -run "+
			"filter, or an instrument fatal in a subtest above). Not computing a set claim "+
			"from a partial run", missing)
		return
	}

	// The claim is the three checks together: the guard above says every declared arm
	// produced a path, the equality says each path is the one the namer mints for
	// THAT arm, and the distinctness says no two arms landed on one file. That is
	// exactly len(initControlArms) files, one per arm, and no other.
	wantDir := filepath.Join(packageDir(t), "testdata")
	seen := make(map[string]string, len(initControlArms))
	for _, arm := range initControlArms {
		got := paths[arm.id]
		want := filepath.Join(wantDir, initControlArmFixtureName(versionToken, arm.id))
		if got != want {
			t.Errorf("#1763: arm %q wrote %q, want %q; #1764's initControlArmFixtureGlob is what "+
				"reads this set back, and it matches only what the namer mints — so a path the "+
				"namer did not mint is either missed entirely or read under the wrong version",
				arm.id, got, want)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("#1763: arms %q and %q both wrote %q; one live child wrote straight over "+
				"the other's capture, last writer wins, and the run reports three arms measured "+
				"with two fixtures on disk", prev, arm.id, got)
		}
		seen[got] = arm.id
	}
}

// TestInitControlChildBudget_ExceedsEveryArmsPerStepWaitSum is #1763's AC 3: the
// outer child deadline strictly exceeds the largest total any declared arm's drive
// sequence can spend in per-step waits.
//
// BOTH SIDES ARE DERIVED FROM THE BUDGET CONSTANTS, never from literal durations.
// That is what keeps it true when a per-step budget moves — and it has to, because
// the set_permission_mode family carries the identical arithmetic unfixed (a 240s
// outer against a 285s sum) and a literal bound here would say nothing when this
// family's own budgets are next retuned.
//
// It spawns nothing, reads nothing off disk and passes on a machine with no claude
// and no credentials. TestInitControlSummarize_ReadsAllThreePlacements is the
// precedent for an offline test living in this exec-ing file.
func TestInitControlChildBudget_ExceedsEveryArmsPerStepWaitSum(t *testing.T) {
	t.Parallel()

	var (
		worst    time.Duration
		worstArm string
	)
	for _, arm := range initControlArms {
		if sum := initControlArmWaitSum(arm); sum > worst {
			worst, worstArm = sum, arm.id
		}
	}

	// The subject.
	if initControlChildBudget <= worst {
		t.Errorf("#1763: initControlChildBudget is %s while arm %q's per-step waits can spend "+
			"%s, so the outer deadline can fire before the waits it contains have expired. "+
			"Every per-step budget exists so that an ABSENCE means absence rather than "+
			"impatience — initControlControlBudget's own doc says so — and a deadline that "+
			"trips first voids that guarantee for exactly the reading this family takes: "+
			"whether a send point goes unanswered. Nothing else reddens when it happens, "+
			"because context_deadline_tripped is a PASSING recorded outcome on purpose",
			initControlChildBudget, worstArm, worst)
	}

	// Vacuity control A, and the sole red for a helper that returns zero, counts one
	// turn, or drops the control term — each of which makes the subject above pass
	// for the wrong reason.
	if want := 2*initControlTurnBudget + initControlControlBudget; worst != want {
		t.Errorf("#1763: the largest per-step wait sum over initControlArms is %s, want %s — "+
			"two turn waits plus the control wait, which is what a writing arm's drive "+
			"sequence spends. A sum smaller than the run's real waits makes the subject check "+
			"above pass against a deadline that still cannot dominate them", worst, want)
	}

	// Vacuity control B, and the sole red both for a helper that ignores sendsRequest
	// and for a table that has lost its control arm — the arm whose window #1764
	// subtracts, and the one whose sequence still drives two full turns.
	control := 2 * initControlTurnBudget
	found := false
	for _, arm := range initControlArms {
		if initControlArmWaitSum(arm) == control {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("#1763: no declared arm sums to %s — two turn waits and no control wait. Either "+
			"initControlArmWaitSum ignores sendsRequest and charges every arm the control "+
			"budget, or initControlArms no longer declares a no-request control arm at all; "+
			"without one there is nothing for a cross-arm comparison to subtract", control)
	}
}

// TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath is #1747's
// third criterion: with no artifact directory at the fill site, artifact_dir is
// PRESENT in the recorded map as armed-nothing rather than missing from it.
//
// It spawns nothing, reads nothing off disk and passes on a machine with no claude
// and no credentials. It lives in THIS file — the exec-ing one — because this is
// the fill site's file and this file correctly carries no finOfflineExecBans entry.
// The record, writer and redaction files each ban os.Getenv, os.Environ and
// os.LookupEnv BY AST NAME IN THAT FILE, so a test calling newDropcapScanner from
// one of them would leave the ban green while reading the environment one hop away
// — the "different fabric" caveat those entries already state. Green-but-dishonest
// is not where this belongs. TestInitControlSummarize_ReadsAllThreePlacements and
// TestInitControlChildBudget_ExceedsEveryArmsPerStepWaitSum are the precedents for
// an offline test living here; the cite this replaced named
// TestInitControlProbedArm_..., which #1763 deleted with the `probed` column.
//
// The scanner is built over the family's existing synthetic path constants rather
// than fresh literals. Both are comfortably longer than dropcapMinNeedle, and
// dropcapPathSpellings only ATTEMPTS filepath.EvalSymlinks, so a non-existent
// synthetic path is deterministic and touches no filesystem state.
//
// WHAT IT MUST NOT ASSERT, and why: three of the map's classes are
// environment-dependent. Both credential classes read os.Getenv and operator_home
// reads realHome, so those three flip between an operator machine and CI. Every
// assertion here is over a class fixed by a caller-passed value.
//
// The empty-needle half is already green elsewhere and is NOT re-commissioned here:
// TestDropcapRedactionAndDenyScan's "a short dynamic needle is skipped and
// reported, never matched" subtest pins that a needle with an empty value is
// skipped and reported false. It builds its needles BY HAND and never goes through
// newDropcapScanner, so it proves the addDynamic half only and says nothing about
// the missing-key behaviour this row exists for.
//
// FAILURE MESSAGES PRINT THE MAP, NEVER THE SCANNER. This test's scanner holds the
// operator's two live credentials exactly as the capture's does; the map's keys are
// declared vocabulary and its values are bools, so it is both safe and the
// diagnostic worth having. See runInitControlChild's doc.
func TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath(t *testing.T) {
	t.Parallel()

	// Exactly the fill site's construction: tempHome and workdir in hand, nothing
	// for artifactDir, the middle parameter.
	scanner := newDropcapScanner(initControlTempHomeValue, "", initControlWorkdirValue)

	// The precheck, and it is this row's headline fact rather than a formality. If
	// the raw map already carried the class, the completion below would be dead
	// weight and the subject check would pass for the wrong reason.
	raw := scanner.applied()
	if _, ok := raw[dropcapClassArtifactDir]; ok {
		t.Fatalf("#1747: newDropcapScanner with an EMPTY artifactDir already reports %q in "+
			"applied() (map %v), so addDynamicPath started appending a needle for an empty "+
			"path — dropcapPathSpellings no longer returns nil for \"\". initControlScanApplied "+
			"is then dead weight and the assertions below settle nothing about the missing-key "+
			"behaviour they exist for", dropcapClassArtifactDir, raw)
	}

	got := initControlScanApplied(scanner)

	// Two checks and two messages: present-but-true and absent-entirely are
	// different defects, and a single combined assertion cannot say which happened.
	armed, ok := got[dropcapClassArtifactDir]
	if !ok {
		t.Fatalf("#1747: the recorded map omits %q entirely (got %v), so an artifact directory "+
			"the run never had reads exactly like a class nobody ever armed — which is the one "+
			"outcome this field exists to prevent", dropcapClassArtifactDir, got)
	}
	if armed {
		t.Errorf("#1747: the recorded map reports %q as ARMED (got %v) although the fill site "+
			"hands newDropcapScanner no artifact directory; a committed capture would then "+
			"claim a needle ran when none was ever built", dropcapClassArtifactDir, got)
	}

	// The vacuity control, and the SOLE RED for a completion that assigns false
	// unconditionally — which would satisfy the subject above while destroying the
	// field's whole meaning.
	if !got[dropcapClassWorkdir] {
		t.Errorf("#1747: %q was handed a non-empty path and still reads armed-nothing (got %v), "+
			"so the completion overwrites entries instead of only adding MISSING ones and every "+
			"armed class in a committed capture is reported as having armed nothing",
			dropcapClassWorkdir, got)
	}

	// The staleness control: for a scanner built with all three path parameters
	// non-empty, every name in the list must be a class some scanner actually arms.
	// SOLE red for a renamed or mistyped entry, which would otherwise write a false
	// key for a class that does not exist.
	//
	// operator_home is carved out WITH ITS REASON rather than left silent: its value
	// is realHome, a package-level os.Getenv("HOME") read no test can control, so
	// binding it here would fail on a legitimately-configured machine launched with
	// HOME unset. newDropcapScanner is the declaration a reader checks for that one
	// name.
	full := newDropcapScanner(initControlTempHomeValue, initControlTempDirValue, initControlWorkdirValue).applied()
	for _, class := range initControlScanPathClasses {
		if class == dropcapClassOperatorHome {
			continue
		}
		if _, ok := full[class]; !ok {
			t.Errorf("#1747: initControlScanPathClasses names %q, which a scanner built with every "+
				"path parameter non-empty does not arm (it armed %v); a name no scanner arms puts "+
				"a false key for a non-existent class into every committed capture. %q is the one "+
				"name deliberately not checked here — its value is realHome, an os.Getenv(\"HOME\") "+
				"read no test can control", class, full, dropcapClassOperatorHome)
		}
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
