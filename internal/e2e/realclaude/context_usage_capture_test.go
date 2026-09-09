//go:build e2e_realclaude

package realclaude

// #2287 — what does a real claude answer when the daemon asks it for the context
// usage breakdown?
//
// # Why a measurement and not a reading
//
// The daemon writes control requests on the child's held-open stdin today —
// WriteInterrupt, WritePermissionMode and WriteInitialize, all in
// internal/streamsup/envelope.go. Claude's Agent SDK declares a
// `get_context_usage` subtype whose response carries the breakdown the `/context`
// command draws: a token count per category, plus the system prompt sections, MCP
// tool costs and memory files behind it. `detail:"summary"` answers from the last
// response's usage and local estimates with no token-count API calls;
// `detail:"full"` counts each category through that API. THAT WAS READ FROM
// @anthropic-ai/claude-agent-sdk@0.3.263's sdk.d.ts on 2026-09-07, NOT MEASURED.
// The stem appears nowhere in this tree and the subtype has never been observed on
// the CLI transport this daemon actually speaks.
//
// Three things are unknown and expensive to guess wrong: whether the CLI's stdin
// control channel answers the subtype at all, what the two `detail` values differ
// by in content and in latency, and whether claude's own arithmetic agrees with the
// number the daemon already computes from the transcript and the model's window.
//
// #1688 and #1763 met the identical unknown for the `initialize` subtype and
// settled it the same way. This file follows initialize_control_probe_test.go: it
// hand-rolls its own request line rather than depending on a production writer,
// drives a full turn before the send points, records every arm, and treats a
// refusal as a passing outcome — because a recorded refusal is the reading this
// kind of run exists to take.
//
// THE PRODUCTION WRITER IS #2288 AND MUST NOT GATE THIS MEASUREMENT. cucapLine is
// this file's own, for that reason and no other.
//
// # One child, two send points
//
// AC 1 asks for one child driven through a completed turn, then a request at each
// `detail` value. That is ONE drive sequence with two send points on one held-open
// stdin, not two children: two children would measure two different sessions, and
// AC 2's comparison against contextwindow.Read needs the daemon's reading and
// claude's to be about the SAME session. Each arm mints its own request_id so the
// responses correlate.
//
// # What passes
//
// A response carrying subtype:"error" is a refusal, and a recorded refusal is a
// PASSING outcome — the error text is the input #2288 designs against. AN ARM THAT
// GOES UNANSWERED IS ALSO A PASSING OUTCOME, and it is the reading this run exists
// to take: whether the CLI answers this subtype at all is precisely the unknown.
// A tripped deadline is recorded too, and is a measurement rather than an artefact
// because cucapChildBudget strictly dominates the per-step waits it contains —
// TestCucapChildBudget_ExceedsThePerStepWaitSum is what enforces that.
//
// The run fails only on a broken instrument or a fail-closed refusal: a spawn or
// pipe failure, a marshal failure, a credential in the child's stderr, an arm that
// captured no stdout at all, or the deny-scan refusing the record. Send the minimal
// request shape and DO NOT ITERATE against a live child; the wall-clock and token
// risk here is the run, not the typing.
//
// # Where the bytes actually survive
//
// The record is written TWICE: to an os.MkdirTemp artifact directory outside every
// worktree, and to the in-repo fixture path when the promotion gate admits it.
// #2229's capture fired clean inside the dispatcher's own real-claude gate, wrote
// its fixture in-repo, printed "Commit it" — and lost the bytes, because that gate
// runs in a detached merge-only worktree it discards and never runs `git add`. Four
// sibling captures lost fixtures the same way. api_retry_capture_test.go closed the
// gap with a built-in double-write and its artifact copy is what a repair leg
// recovered; this probe defaults to the same. READ THE ARTIFACT PATH OUT OF THE LOG
// before concluding a firing run produced nothing.
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine, and nothing else — the gate is
// the FIXTURE'S ABSENCE, argued at TestRealClaude_ContextUsageCapture. To force a
// re-capture over an existing fixture:
//
//	PYRY_PROBE_CONTEXT_USAGE_CAPTURE=1 go test -tags e2e_realclaude -timeout 20m -v \
//	  -run '^TestRealClaude_ContextUsageCapture$' ./internal/e2e/realclaude/
//
// The TestCucap* tests below spawn nothing and must report PASS, not SKIP, on a
// machine with no claude and no credentials:
//
//	go test -tags e2e_realclaude -race -count=1 -v -run 'TestCucap' ./internal/e2e/realclaude/
//
// Read the count of tests that executed, never the exit code: this package is
// behind the e2e_realclaude tag, `make check` never compiles it, and the suite exits
// 0 both on a build failure and on a full credentials skip.

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
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/contextwindow"
)

// Every file-local identifier takes the `cucap` prefix, for the reason #1260's
// header gives: siblings add files to this package concurrently and a
// branch-overlap check does not catch a same-package identifier collision.

// cucapEnableEnv FORCES a re-capture when a fixture already exists. IT IS NOT THE
// GATE — see the gate comment in TestRealClaude_ContextUsageCapture.
const cucapEnableEnv = "PYRY_PROBE_CONTEXT_USAGE_CAPTURE"

// The fixture is matched by GLOB and NAMED from the observed version, which follows
// arcapFixtureGlob rather than #2229's compile-time version pin.
//
// That pin is right when the fixture is a reader's input, so a claude upgrade is a
// loud instruction to re-capture. HERE THE RECORD IS THE DELIVERABLE, and a version
// bump between authoring and the live run would turn this ticket's whole output into
// nothing promoted. So the arming gate globs any version and cucapFixturePath
// composes the name from what `claude --version` actually printed.
//
// internal/streamsup's contextUsageCaptureGlob is the reading end of the same
// pattern and carries the same argument.
const cucapFixtureGlob = "testdata/context_usage_v*.json"

const (
	cucapTicket         = "2287"
	cucapWorkdirName    = "context-usage-work"
	cucapRecordName     = "cucap-record.json"
	cucapArtifactPrefix = "pyry-2287-capture-*"

	// Cost. The context-usage breakdown claude reports is a property of the SESSION,
	// not of the model answering the probe turn.
	cucapModel = "claude-haiku-4-5"

	// Cost guard with headroom over the single tool-free probe turn. A result line
	// carrying subtype:"error_max_turns" lands in the fixture plainly — raise and
	// rerun.
	cucapMaxTurns = "4"

	// A fixed literal in a per-test temp $HOME, not a secret. It is what pins the
	// transcript's filename so AC 2's daemon-side reading can resolve the SAME
	// session claude answered about, and it is the id dropcapClassSessionID
	// substitutes. Distinct from every sibling probe's so a record can never be
	// mistaken for one of theirs.
	cucapSessionID = "3f7a1c62-9d84-4b05-8e13-7c6a2f0d9b41"
)

// cucapPromptOne is the single probe turn, and it is deliberately TOOL-FREE. A
// tool-free turn cannot stall on a permission prompt it can never receive, which is
// why this run needs no --dangerously-skip-permissions and hands an unsandboxed
// child no tool access at all. Do not "make the probe use Bash": that reintroduces
// the flag for no measurement gain.
//
// ONE turn, not two. The initialize family drives two because its send points sit
// BETWEEN them and it must observe what a further turn emits after the request.
// Nothing here reads a post-request line: both send points come after the same
// completed turn, and what they measure is the control_response. A second turn would
// buy tokens and no reading.
const cucapPromptOne = "Reply with the single word: ready. Do not use any tools."

const (
	// Hard kill for the child, and the outer bound. IT DOMINATES THE PER-STEP WAITS
	// BELOW: 300s against the 180s one turn wait plus two control waits can spend,
	// with 120s of residual for the steps the sum does not count — cmd.Start, the
	// transcript poll, the stdin close, cmd.Wait, the reader join and the inter-step
	// overhead.
	//
	// That ordering is what makes context_deadline_tripped a MEASUREMENT rather than
	// an artefact of the harness. Every per-step budget exists so an absence means
	// absence rather than impatience — cucapControlBudget's own doc says so — and a
	// deadline that can fire first voids that guarantee for exactly the reading this
	// file takes. TestCucapChildBudget_ExceedsThePerStepWaitSum enforces the relation,
	// deriving both sides from these constants rather than from literals.
	cucapChildBudget = 5 * time.Minute

	// The one probe turn. A tool-free turn lands in seconds.
	cucapTurnBudget = 90 * time.Second

	// Per arm, and long enough that an absent control_response means absence rather
	// than impatience. Same value and reason as setModeControlBudget and
	// initControlControlBudget — and it is charged TWICE, once per arm, which is
	// what the budget relation above accounts for.
	//
	// `detail:"full"` is documented to count each category through a token-count API
	// while `detail:"summary"` answers locally, so the full arm is the one most
	// likely to need the whole budget. If it is the arm that comes back unanswered,
	// round_trip_ms beside within_wait is what tells a later reader whether that was
	// silence or slowness.
	cucapControlBudget = 45 * time.Second

	// How long the transcript poll waits for <session-id>.jsonl to appear. Charged
	// AFTER the child has exited, so it costs no claude time; an absence here is
	// recorded rather than fatal.
	cucapTranscriptBudget = 20 * time.Second

	cucapTranscriptPoll = 25 * time.Millisecond
)

const (
	// cucapMaxVersionToken bounds what cucapFixturePath will splice into a filename.
	cucapMaxVersionToken = 32

	// cucapMaxStdoutLines bounds the recorded line count. The turn budget and
	// --max-turns bound a well-behaved child, but nothing bounds a looping one, and
	// stdout_events is uncapped by design for the reason poolRevokeFixtureRecord
	// gives — capping structured evidence destroys the artifact. So the CAP IS ON THE
	// COUNT AND THE DROP IS RECORDED: stdout_lines_dropped is a fact in the file, not
	// a silent truncation. One probe turn produces tens of lines; this is three
	// orders of magnitude of headroom, and reaching it is itself a finding.
	cucapMaxStdoutLines = 5000

	// cucapMaxNumericLeaves bounds the derived leaf map. The verbatim response is in
	// control_responses either way, so this map is a convenience over bytes that are
	// already recorded, and a pathological response must not be able to balloon the
	// file through it. Selection is by SORTED PATH before the cut, so which leaves
	// survive is deterministic rather than whatever Go's map iteration handed back.
	cucapMaxNumericLeaves = 512
)

// --- the arms -------------------------------------------------------------------

// cucapArm is one send point: the arm's identifier, which is what names it in the
// record and in internal/streamsup's pin, and the `detail` value its request
// carries.
//
// The two columns are SEPARATE even though today they are equal strings. The arm id
// is this repo's vocabulary — it appears in contextUsagePinnedShapes and in every
// failure message — while detail is claude's, read off the SDK's declaration. Folding
// them into one field would make a rename on either side silently rename the other.
type cucapArm struct {
	id     string
	detail string
}

// cucapArms is the single source of truth for the arm set. The send point is placed
// by ranging this table, never by a switch on an arm's id — a second spelling of an
// identifier declared here is what TestCucapArms_AreDistinct exists to prevent.
var cucapArms = []cucapArm{
	{id: "summary", detail: "summary"},
	{id: "full", detail: "full"},
}

// cucapArmWaitSum returns the largest total the drive sequence can spend in PER-STEP
// WAITS: the one turn wait, plus one control wait per declared arm.
//
// IT MIRRORS runCucapChild's SEQUENCE AND MUST BE GROWN WITH IT. Nothing couples the
// two — a second probe turn added to that driver and not to this sum leaves
// TestCucapChildBudget_ExceedsThePerStepWaitSum green over an arithmetic that no
// longer describes the run, which is the one way that test can pass while saying
// nothing.
//
// It counts ONLY the bounded waits. cmd.Start, the transcript poll, the stdin close,
// cmd.Wait, the reader join and the inter-step overhead are uncounted; the residual
// between this sum and cucapChildBudget is what covers them, and that residual is a
// judgement rather than something any assertion here demands.
func cucapArmWaitSum() time.Duration {
	return cucapTurnBudget + time.Duration(len(cucapArms))*cucapControlBudget
}

// --- the request line -------------------------------------------------------------

// cucapRequest and cucapRequestInner are the wire shape of the control lines this
// probe writes on the child's held-open stdin.
//
// The inner struct is a FRESH TYPE, deliberately neither setModeControlRequestInner
// nor initControlRequestInner. The first carries a `mode` field with no omitempty and
// the second carries no `detail` at all, so reusing either would either emit
// "mode":"" or drop the one field this ticket is about — and adding a field to either
// would change what control_request_sent reads in the committed fixtures those
// families already own. A fresh two-field struct is the correct price.
type cucapRequest struct {
	Type      string            `json:"type"`       // "control_request"
	RequestID string            `json:"request_id"` // locally-minted correlation id
	Request   cucapRequestInner `json:"request"`
}

type cucapRequestInner struct {
	Subtype string `json:"subtype"` // "get_context_usage"
	Detail  string `json:"detail"`  // "summary" or "full"
}

// cucapSubtype is the stem under measurement. It is spelled ONCE, here, because a
// second spelling is the one typo that would make every arm come back with an
// unrecognized-subtype error and read as a finding about claude.
const cucapSubtype = "get_context_usage"

// cucapRequestID mints the correlation id for one arm.
//
// A correlation token, not a security token — the same reason (*Runner).Interrupt
// mints its id from a monotonic counter rather than a random source. Fixed literals
// keep the committed fixture diffable across re-captures. PER ARM rather than one
// shared literal, which initControlRequestID deliberately is NOT: that family's arms
// are separate children, so there is no correlation ambiguity to resolve. Here both
// arms write on ONE stdin and both replies land on ONE stdout, so a shared id would
// make cucapResponsesFor attribute either reply to either arm.
func cucapRequestID(arm string) string { return "context-usage-" + arm }

// cucapLine returns the single newline-terminated control line for one arm:
//
//	{"type":"control_request","request_id":"<id>","request":{"subtype":"get_context_usage","detail":"<v>"}}
//
// Marshalled structured, never string-concatenated — the same one-physical-line
// invariant setModeControlLine and initControlLine hold, so the appended '\n' is the
// only raw newline in the envelope.
func cucapLine(requestID, detail string) ([]byte, error) {
	b, err := json.Marshal(cucapRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   cucapRequestInner{Subtype: cucapSubtype, Detail: detail},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s control request for detail %q: %w", cucapSubtype, detail, err)
	}
	return append(b, '\n'), nil
}

// --- reading the response without guessing its shape --------------------------

// cucapResponsesFor returns the recorded control_response lines whose request id is
// requestID, searching the two placements a control_response is known to use for it —
// top level and nested under `response`.
//
// internal/streamsup/parser.go's control_response arm records, as measured shape,
// that subtype and request_id arrive nested under `response` rather than carried at
// top level, and setModeResponseIDMatches already applies that discipline. This
// returns the LINES rather than a bool because both arms share one stdout: an arm's
// record must carry ITS reply and not the other's, which a bool cannot express.
//
// A line whose id matches NEITHER arm is not returned to anybody and stays in
// stdout_events, where it is still evidence. That is the honest outcome for a reply
// claude correlated to something this probe never sent.
func cucapResponsesFor(responses []json.RawMessage, requestID string) []json.RawMessage {
	if requestID == "" {
		return nil
	}
	var out []json.RawMessage
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
			out = append(out, raw)
		}
	}
	return out
}

// cucapSubtypeOf returns the first non-empty envelope `subtype` across the lines,
// searching THREE PLACEMENTS — top level, under `response`, and under
// `response.response`.
//
// The third is where a real initialize reply actually put its payload, measured
// against claude 2.1.239 on 2026-08-22, and a summariser reading only the first two
// levels reported a false absence against a reply carrying six models. The shape of
// THIS subtype's reply is the unknown, so all three are read rather than the one this
// author expects.
//
// internal/streamsup's contextUsageObservedSubtype is the reading end of the same
// search, written out independently there because that package cannot import this
// one — and that independence is what makes the pin a comparison rather than an
// agreement.
func cucapSubtypeOf(lines []json.RawMessage) string {
	for _, raw := range lines {
		var env struct {
			Subtype  string `json:"subtype"`
			Response struct {
				Subtype  string `json:"subtype"`
				Response struct {
					Subtype string `json:"subtype"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		for _, sub := range []string{env.Subtype, env.Response.Subtype, env.Response.Response.Subtype} {
			if sub != "" {
				return sub
			}
		}
	}
	return ""
}

// cucapLeaves walks one response line and returns every NUMERIC leaf in it, keyed by
// dotted path, along with how many were dropped by the cap and how many were skipped
// as unrepresentable.
//
// THE PAYLOAD'S KEY NAMES ARE THE UNKNOWN, so nothing here assumes them. A later
// slice declares its field set from this map rather than from this author's reading
// of a type declaration — which is the whole reason the ticket spends a live gate lap.
//
// VALUES ARE json.Number AND ARE STORED AS STRINGS, never as float64. A response
// carrying 1e999 decodes to +Inf, json.Marshal ERRORS on a non-finite float, and the
// whole capture would then abort as a broken instrument over data that is in fact
// claude's own answer. A json.Number round-trips the token verbatim and cannot be
// non-finite. A token that does not parse as a finite float64 is skipped and counted,
// so the omission is a recorded fact rather than a silent hole.
//
// THE KEYS ARE CLAUDE-AUTHORED AND ARE A LEAK SURFACE. A dotted path is built from
// the response's own JSON object keys, and this response is documented to describe
// MEMORY FILES, which live under the operator's home — a map claude keys by file path
// puts that path into a committed artifact as a KEY. cucapRedactRecord visits these
// keys for that reason, and the deny-scan reads the whole marshalled blob behind it.
// This is the one field in the record where claude chooses an identifier rather than
// filling a slot the rig named.
//
// TRUNCATION IS BY SORTED PATH, not by whatever Go's map iteration handed back: the
// walk collects every leaf, sorts, then cuts. An unsorted cut writes a different
// subset into the committed fixture on every run.
//
// There is deliberately NO string-leaf twin. It would double the record's
// claude-authored string surface for no measurement gain — control_responses already
// carries every byte verbatim and redacted, and that is the ground truth a later
// slice re-derives from.
func cucapLeaves(lines []json.RawMessage, max int) (leaves map[string]string, truncated, skipped int) {
	type leaf struct{ path, value string }
	var found []leaf

	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch n := v.(type) {
		case json.Number:
			// The finite check is over the PARSE, while the value stored is the
			// ORIGINAL TOKEN: parsing proves it is representable, and storing the token
			// keeps the record byte-faithful to what claude wrote rather than to Go's
			// re-rendering of it.
			f, err := n.Float64()
			if err != nil || math_IsInf(f) || math_IsNaN(f) {
				skipped++
				return
			}
			found = append(found, leaf{path: prefix, value: n.String()})
		case []any:
			for i, e := range n {
				walk(prefix+"["+strconv.Itoa(i)+"]", e)
			}
		case map[string]any:
			for k, e := range n {
				if prefix == "" {
					walk(k, e)
					continue
				}
				walk(prefix+"."+k, e)
			}
		}
	}

	for i, raw := range lines {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var doc any
		if err := dec.Decode(&doc); err != nil {
			// A line that does not decode is skipped rather than fatal: it is still in
			// control_responses verbatim, which is the evidence.
			continue
		}
		// The line index prefixes the path when an arm captured more than one reply,
		// so two lines' identically-named leaves stay distinguishable. The common case
		// is one line, and a bare path reads better than "[0].x" — so the prefix is
		// added only when it disambiguates something.
		prefix := ""
		if len(lines) > 1 {
			prefix = "line[" + strconv.Itoa(i) + "]"
		}
		walk(prefix, doc)
	}

	sort.Slice(found, func(i, j int) bool { return found[i].path < found[j].path })
	if max > 0 && len(found) > max {
		truncated = len(found) - max
		found = found[:max]
	}
	if len(found) == 0 {
		return nil, truncated, skipped
	}
	leaves = make(map[string]string, len(found))
	for _, l := range found {
		leaves[l.path] = l.value
	}
	return leaves, truncated, skipped
}

// math_IsInf and math_IsNaN are two-line locals rather than a math import.
//
// A NaN is the one float64 not equal to itself, and an infinity is the one whose
// magnitude exceeds every finite value. Both are three tokens, and adding an import
// to this file for them would be the larger edit. Named with the package they mirror
// so a reader recognises the semantics on sight.
func math_IsNaN(f float64) bool { return f != f }
func math_IsInf(f float64) bool {
	return f > 1.797693134862315708145274237317043567981e+308 || f < -1.797693134862315708145274237317043567981e+308
}

// The ordered candidate lists claude's own total and percentage are selected from.
//
// EACH ENTRY IS A PATH SUFFIX, matched case-insensitively against the dotted paths
// cucapLeaves produced, and the lists are ORDERED — the first suffix with any match
// wins, and among its matches the lexically smallest path wins. Determinism is the
// requirement: Go randomises map iteration, so "the first match" over a map answers
// differently between two runs on identical input, and a committed fixture must not
// change from run to run.
//
// THE LISTS ARE A GUESS AND THE RECORD SAYS SO. claude_total_source names the path a
// value actually came from and is EMPTY when nothing matched — which is itself a
// recorded measurement, not a failure. The authoritative field set is numeric_leaves
// beside the verbatim response; these two selections are a convenience over it, so a
// reader of the fixture can see the headline figure without re-deriving it. A later
// slice that finds the real key name reads it off numeric_leaves and ignores these.
var (
	cucapTotalPathSuffixes = []string{
		"total_tokens", "totaltokens", "total", "used_tokens", "usedtokens", "input_tokens",
	}
	cucapPercentPathSuffixes = []string{
		"percent_used", "percentused", "percentage", "percent", "used_percent", "usedpercent",
	}
)

// cucapSelect returns the first matching path and its value, scanning suffixes in
// order and breaking ties on the lexically smallest path. Both returns are empty when
// nothing matches.
func cucapSelect(leaves map[string]string, suffixes []string) (path, value string) {
	for _, suffix := range suffixes {
		best := ""
		for p := range leaves {
			if !strings.HasSuffix(strings.ToLower(p), suffix) {
				continue
			}
			if best == "" || p < best {
				best = p
			}
		}
		if best != "" {
			return best, leaves[best]
		}
	}
	return "", ""
}

// --- the daemon's own reading (AC 2) --------------------------------------------

// cucapWindowsFrom builds the model → context-window map contextwindow.Read joins
// on, from the LAST `result` line's own modelUsage.
//
// That is the same channel the daemon uses: internal/streamsup's decodeModelWindows
// reads modelUsage off a `result` line, because the transcript never carries a window
// and the two halves of the reading arrive on different channels. Rebuilding it here
// rather than importing that unexported helper keeps this probe independent of the
// package it is measuring for.
//
// THE LAST result LINE WINS, matching contextwindow.Read's own last-usage-wins rule.
// A non-positive window is dropped, because Read treats one as absent anyway and
// recording it would put a value in windows_observed that the join provably ignored.
//
// A nil return is legal and is exactly "nothing observed": Read then falls back to
// its default window, which is the pre-#2107 reading.
func cucapWindowsFrom(lines []json.RawMessage) map[string]int {
	var out map[string]int
	for _, raw := range lines {
		var env struct {
			Type       string `json:"type"`
			ModelUsage map[string]struct {
				ContextWindow int `json:"contextWindow"`
			} `json:"modelUsage"`
		}
		if err := json.Unmarshal(raw, &env); err != nil || env.Type != "result" {
			continue
		}
		if len(env.ModelUsage) == 0 {
			continue
		}
		next := make(map[string]int, len(env.ModelUsage))
		for model, usage := range env.ModelUsage {
			if usage.ContextWindow > 0 {
				next[model] = usage.ContextWindow
			}
		}
		if len(next) > 0 {
			out = next
		}
	}
	return out
}

// cucapPercent renders used/window as a percentage with two decimals, or "" when the
// window is not positive.
//
// THE EMPTY STRING IS THE POINT. contextwindow.Read reports WindowTokens 0 as its
// "no trustworthy window reading" signal — a used count above the resolved window
// DISPROVES it, and Read withholds the denominator rather than reporting one its own
// data contradicts. Dividing by it here would invent the denominator Read deliberately
// refused, and a fixture carrying "+Inf%" would be worse than one carrying nothing.
func cucapPercent(used, window int) string {
	if window <= 0 {
		return ""
	}
	return strconv.FormatFloat(100*float64(used)/float64(window), 'f', 2, 64)
}

// cucapTranscriptPath polls <home>/.claude/projects/*/ for <sessionID>.jsonl and
// returns the path and whether it appeared inside budget.
//
// LOCATED EMPIRICALLY rather than by recomputing claude's encoded-cwd slug.
// DefaultClaudeSessionsDir is the production reference for where claude writes
// <uuid>.jsonl, but the encoding rewrites both '/' and '.' and the child's cwd may be
// case-canonicalised, so recomputing it in a test is fragile —
// streamNewSessionTranscriptDir makes the same choice for the same reason.
//
// AN ABSENCE IS RECORDED, NEVER FATAL. Whether a pinned --session-id survives to the
// transcript's filename on this claude release is itself a thing this run measures; a
// probe that fataled on it would spend the tokens and report nothing.
func cucapTranscriptPath(home, sessionID string, budget time.Duration) (string, bool) {
	projects := filepath.Join(home, ".claude", "projects")
	target := sessionID + ".jsonl"
	deadline := time.Now().Add(budget)
	for {
		entries, _ := os.ReadDir(projects)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(projects, e.Name(), target)
			if _, err := os.Stat(path); err == nil {
				return path, true
			}
		}
		if !time.Now().Before(deadline) {
			return "", false
		}
		time.Sleep(cucapTranscriptPoll)
	}
}

// --- the record -------------------------------------------------------------------

// cucapArmRecord is one send point's whole measurement.
//
// Answered and WithinWait are DIFFERENT FACTS and neither derives the other.
// ControlResponses is snapshotted after the child exits, so a reply landing past the
// bounded wait is captured with the wait already false — "there are response bytes"
// and "the wait was satisfied" are two different things, and before #1722 taught this
// family the lesson only the first survived a run. Answered is len(ControlResponses)
// > 0 at snapshot time; WithinWait is the wait's OWN result and is not reconstructible
// from any other field, because a control_response carries no arrival time relative to
// a budget the harness chose.
//
// RoundTripMs is measured from the write to whichever of those two ended the wait, so
// it is a lower bound on a late reply rather than its true latency. It is what
// separates "claude was silent" from "claude was slow" on an unanswered arm, which is
// the reading `detail:"full"` is most likely to need.
//
// SendPointIndex anchors the arm in stdout_events, in the same index units
// turn_boundaries uses — setModeRecorder.add assigns both from the position a line
// lands at. It is read BEFORE the write, so nothing claude wrote in response to the
// request can fall before it.
type cucapArmRecord struct {
	Arm    string `json:"arm"`
	Detail string `json:"detail"`

	ControlRequestID   string            `json:"control_request_id"`
	ControlRequestSent json.RawMessage   `json:"control_request_sent"`
	ControlResponses   []json.RawMessage `json:"control_responses"`

	Answered         bool  `json:"answered"`
	WithinWait       bool  `json:"within_wait"`
	RequestIDMatched bool  `json:"request_id_matched"`
	RoundTripMs      int64 `json:"round_trip_ms"`
	SendPointIndex   int   `json:"send_point_index"`

	ResponseSubtype string `json:"response_subtype"`

	NumericLeaves          map[string]string `json:"numeric_leaves"`
	NumericLeavesTruncated int               `json:"numeric_leaves_truncated"`
	NumericLeavesSkipped   int               `json:"numeric_leaves_skipped"`

	ClaudeTotalSource   string `json:"claude_total_source"`
	ClaudeTotalValue    string `json:"claude_total_value"`
	ClaudePercentSource string `json:"claude_percent_source"`
	ClaudePercentValue  string `json:"claude_percent_value"`
}

// cucapRecord is the durable artifact this run commits, and the JSON contract
// internal/streamsup's pin and #2288's decoder read.
//
// IsCapture is `true` on every record this file writes and exists so a reader can
// refuse a hand-authored payload swapped in behind the provenance checks — the field
// compaction's own capture carries for that reason, and the one
// contextUsageRead fatals on.
//
// There is deliberately no `env` field, and there must never be one — the constraint
// is inherited from setModeFixtureRecord. The credential reaches the child through
// the environment while the argv carries none, so recording argv is safe and
// recording env would not be.
//
// ARGV RECORDS THE BINARY'S BASE NAME, NOT ITS PATH, which is a deliberate departure
// from the sibling captures. An absolute claude path is an operator path: under a
// home the redactor arms it rewrites to $HOME, but with HOME unset at launch
// operator_home arms nothing and the fixed /Users/ needle then refuses the whole
// record. The argv's evidentiary value is the FLAGS — where the binary happens to
// live says nothing about what claude was asked — so dropping the directory removes a
// whole class of refusal at zero measurement cost.
//
// DaemonPercent and ClaudePercentValue are the two sides AC 2 exists to put beside
// each other, and NOTHING HERE COMPUTES A VERDICT ON THEM. Whether the readings agree
// is the later slice's to say; a verdict computed in two places is a second source of
// truth. DaemonPercent is EMPTY when the daemon's own window reading was withheld —
// see cucapPercent — and an empty percentage is a recorded fact, not a gap.
//
// WindowsObserved is the join's INPUT, recorded so the daemon-side reading is
// auditable rather than asserted. A nil map means no `result` line carried a
// modelUsage, in which case contextwindow.Read fell back to its default window.
//
// StderrCapture is RAW here and capped inside cucapScanRecord, so the record in
// memory holds the raw string and the file on disk holds the capped one — #1702's
// design.
//
// A STRING-BEARING FIELD ADDED HERE MUST BE VISITED BY cucapRedactRecord, which
// rewrites every string, []string, json.RawMessage, []json.RawMessage and leaf-map
// KEY before the record reaches the writer. That pass visits by field rather than by
// reflection, so a new field escapes it silently. Redaction and
// CredentialScanApplied are that instruction's only exceptions, scoped to those two
// fields and never to "fields the author judges safe": their strings are the
// redactor's and the scanner's OWN VOCABULARY — class identifiers, the $-prefixed
// placeholders, the two environment-variable NAMES — never child output and never a
// path. Redaction is additionally assigned from what the pass RETURNS, so a pass
// visiting it would have to read a value that does not exist until it returns.
//
// AN EMPTY CENSUS IS NOT AN ABSENT ONE, and an ARMED-NOTHING SCAN IS NOT AN ABSENT
// ONE. dropcapRedactor.substitutions returns a NON-NIL EMPTY slice when nothing
// fired and dropcapScanner.applied a NON-NIL EMPTY map when nothing armed, while a
// record that never reached the fill site holds nil for both — so `[]`/`{}` say the
// pass ran and matched nothing, and `null` says it never ran. That distinction lives
// entirely in the marshalled bytes, and one `omitempty` on either tag erases it.
// There is no omitempty on any tag in this type.
//
// AN EMPTY CENSUS IS ALSO NOT A CLEAN ARTIFACT. `[]` says the redactor RAN; it says
// nothing about whether the file is free of operator paths, because a class armed
// with a wrong or empty value matches nothing and reports honestly. Redaction is an
// AUDIT TRAIL OVER THE REDACTOR; the deny-scan in cucapScanRecord is the fail-closed
// net that makes the second claim.
type cucapRecord struct {
	IsCapture bool `json:"is_capture"`

	ClaudeVersionRaw string `json:"claude_version_raw"`
	ClaudeVersion    string `json:"claude_version"`

	Argv    []string `json:"argv"`
	Prompts []string `json:"prompts"`

	Arms []cucapArmRecord `json:"arms"`

	TurnsCompleted     int               `json:"turns_completed"`
	StdoutEvents       []json.RawMessage `json:"stdout_events"`
	StdoutLinesDropped int               `json:"stdout_lines_dropped"`
	NonJSONLineCount   int               `json:"non_json_line_count"`
	TurnBoundaries     []int             `json:"turn_boundaries"`

	TranscriptFound    bool           `json:"transcript_found"`
	WindowsObserved    map[string]int `json:"windows_observed"`
	DaemonUsedTokens   int            `json:"daemon_used_tokens"`
	DaemonWindowTokens int            `json:"daemon_window_tokens"`
	DaemonPercent      string         `json:"daemon_percent"`
	DaemonReadError    string         `json:"daemon_read_error"`

	StdinWriteErrors       []string `json:"stdin_write_errors"`
	StderrCapture          string   `json:"stderr_capture"`
	ExitCode               int      `json:"exit_code"`
	WaitError              string   `json:"wait_error"`
	ContextDeadlineTripped bool     `json:"context_deadline_tripped"`
	DurationMs             int64    `json:"duration_ms"`
	ScannerError           string   `json:"scanner_error"`

	Redaction             []dropcapSubstitution `json:"redaction"`
	CredentialScanApplied map[string]bool       `json:"credential_scan_applied"`
}

// cucapAnsweredArms counts the arms whose request drew a correlated reply.
func (rec *cucapRecord) answeredArms() int {
	n := 0
	for _, arm := range rec.Arms {
		if arm.Answered {
			n++
		}
	}
	return n
}

// --- the promotion gate -----------------------------------------------------------

// cucapFixturePath composes the fixture name from the version claude actually
// printed, and refuses any token that cannot safely be one. It is arcapFixturePath's
// shape, and its reasoning transfers whole.
//
// The shape check is what stops a version string composing a path outside testdata/:
// it admits no separator and requires a leading digit, so every traversal and absolute
// form is rejected, and an "<unavailable: …>" fails at the first character rather than
// naming a fixture that vouches for nothing. The length cap is what stops a
// pathological version composing an absurd name.
//
// The version is CHILD-CONTROLLED — it is whatever `claude --version` wrote — so this
// is a genuine trust boundary and not a formality.
func cucapFixturePath(version string) (string, error) {
	token, _, _ := strings.Cut(strings.TrimSpace(version), " ")
	if token == "" {
		return "", fmt.Errorf("claude_version is empty, so the fixture could not be named for the " +
			"release it vouches for")
	}
	if len(token) > cucapMaxVersionToken {
		return "", fmt.Errorf("claude_version's leading token is %d bytes, over the %d cap",
			len(token), cucapMaxVersionToken)
	}
	if token[0] < '0' || token[0] > '9' {
		return "", fmt.Errorf("claude_version %q does not start with a digit, so it is not a version "+
			"(an unreadable one is recorded as \"<unavailable: …>\")", token)
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			c == '.' || c == '-'
		if !ok {
			return "", fmt.Errorf("claude_version %q carries a byte that may not appear in a filename",
				token)
		}
	}
	return "testdata/context_usage_v" + token + ".json", nil
}

// fixtureWorthy answers whether this record may be promoted in-repo, and names the
// reason when it may not.
//
// THE NON-VACUITY RULE IS THIS TICKET'S, and the difference from its siblings is the
// whole design. ccapRecord.fixtureWorthy refuses a record holding zero frames of its
// quarry; here a record where NO ARM WAS ANSWERED is the publishable result under AC
// 3 — whether the CLI answers this subtype at all is precisely the unknown — so
// refusing it would delete the branch this capture exists to measure. Same for an arm
// answered with subtype:"error": a refusal recorded verbatim is the input #2288
// designs its accepted shape against.
//
// What must be refused instead is a record that cannot support the claim its
// acceptance criteria make:
//
//   - No arm sent at all. That is a -run filter's partial, not a measurement.
//   - Zero stdout lines. There is nothing to capture and no session to compare.
//   - No completed turn. AC 1 asks for the requests to follow a COMPLETED turn, and
//     the two `detail` values differ in what they compute FROM the turn's usage, so a
//     record whose turn never closed measures a different send point than the one the
//     ticket describes.
//   - A version token that cannot name a file, which cucapFixturePath decides.
func (rec *cucapRecord) fixtureWorthy() (string, bool) {
	if len(rec.Arms) == 0 {
		return "no arm was driven, so this record is a filtered partial rather than a measurement — " +
			"AC 1 asks for a request at EACH detail value", false
	}
	if len(rec.StdoutEvents) == 0 {
		return "the child produced no stdout at all, so there is nothing captured to promote", false
	}
	if rec.TurnsCompleted == 0 {
		return "no probe turn completed, so the requests did not follow a COMPLETED turn as AC 1 " +
			"describes. The two detail values differ in what they compute from the turn's usage, so " +
			"the arms measured a different send point than the one this capture is about", false
	}
	if _, err := cucapFixturePath(rec.ClaudeVersion); err != nil {
		return err.Error(), false
	}
	return "", true
}

// --- redaction and the fail-closed scan ------------------------------------------

// cucapRedactRecord rewrites every claude-authored string in rec through red, in
// place, and returns the census of classes that fired.
//
// IT VISITS BY FIELD, NOT BY REFLECTION, so a field added to either record type
// escapes it silently — which is why both types' docs carry the instruction and this
// is where it is discharged.
//
// THE LEAF-MAP KEYS ARE VISITED, and that is this pass's one departure from the
// sibling family's shape. Every other string it rewrites sits in a slot the rig
// named; a leaf path is built from claude's own JSON object keys, and this response
// describes memory files under the operator's home. A rebuilt map is assigned rather
// than the keys mutated in place, because a Go map cannot have a key rewritten
// without deleting and reinserting, and doing that during a range is how two paths
// silently collapse into one.
//
// A COLLISION AFTER REDACTION IS REPORTED, NOT SILENT: two distinct paths that redact
// to one string would lose a leaf, so the surviving value is the lexically smaller
// path's and the count of lost leaves is added to NumericLeavesSkipped, which the
// record already publishes.
//
// Redaction and CredentialScanApplied are deliberately NOT visited — see cucapRecord's
// doc for the two reasons and the scope of that exception.
func cucapRedactRecord(red *dropcapRedactor, rec *cucapRecord) []dropcapSubstitution {
	rec.ClaudeVersionRaw = red.str(rec.ClaudeVersionRaw)
	rec.ClaudeVersion = red.str(rec.ClaudeVersion)
	rec.Argv = red.strs(rec.Argv)
	rec.Prompts = red.strs(rec.Prompts)

	for i := range rec.Arms {
		arm := &rec.Arms[i]
		arm.Arm = red.str(arm.Arm)
		arm.Detail = red.str(arm.Detail)
		arm.ControlRequestID = red.str(arm.ControlRequestID)
		if len(arm.ControlRequestSent) > 0 {
			arm.ControlRequestSent = json.RawMessage(red.redact(arm.ControlRequestSent))
		}
		arm.ControlResponses = cucapRedactRaws(red, arm.ControlResponses)
		arm.ResponseSubtype = red.str(arm.ResponseSubtype)
		arm.ClaudeTotalSource = red.str(arm.ClaudeTotalSource)
		arm.ClaudeTotalValue = red.str(arm.ClaudeTotalValue)
		arm.ClaudePercentSource = red.str(arm.ClaudePercentSource)
		arm.ClaudePercentValue = red.str(arm.ClaudePercentValue)

		if len(arm.NumericLeaves) > 0 {
			rebuilt := make(map[string]string, len(arm.NumericLeaves))
			// Sorted so the surviving entry on a post-redaction collision is
			// deterministic — the lexically smaller original path's — rather than
			// whatever Go's map iteration reached last.
			paths := make([]string, 0, len(arm.NumericLeaves))
			for p := range arm.NumericLeaves {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			lost := 0
			for _, p := range paths {
				key := red.str(p)
				if _, dup := rebuilt[key]; dup {
					lost++
					continue
				}
				rebuilt[key] = red.str(arm.NumericLeaves[p])
			}
			arm.NumericLeaves = rebuilt
			arm.NumericLeavesSkipped += lost
		}
	}

	rec.StdoutEvents = cucapRedactRaws(red, rec.StdoutEvents)
	rec.StdinWriteErrors = red.strs(rec.StdinWriteErrors)
	rec.StderrCapture = red.str(rec.StderrCapture)
	rec.WaitError = red.str(rec.WaitError)
	rec.ScannerError = red.str(rec.ScannerError)
	rec.DaemonReadError = red.str(rec.DaemonReadError)

	// WindowsObserved's keys are MODEL IDS, which are claude-authored text — the same
	// reason contextwindow.Usage refuses to carry a model field. They are rewritten
	// for consistency with the leaf map rather than because a model id has ever
	// carried a path; a rule that admits a judgement call about which claude-authored
	// strings are safe is the rule this pass exists to replace.
	if len(rec.WindowsObserved) > 0 {
		rebuilt := make(map[string]int, len(rec.WindowsObserved))
		models := make([]string, 0, len(rec.WindowsObserved))
		for m := range rec.WindowsObserved {
			models = append(models, m)
		}
		sort.Strings(models)
		for _, m := range models {
			key := red.str(m)
			if _, dup := rebuilt[key]; dup {
				continue
			}
			rebuilt[key] = rec.WindowsObserved[m]
		}
		rec.WindowsObserved = rebuilt
	}

	return red.substitutions()
}

// cucapRedactRaws returns a FRESH slice of FRESH byte slices, so nothing it does
// writes through the caller's values. Every log site that prints a response must read
// the RECORD's field rather than the pre-pass local for exactly that reason: the
// initialize family measured unredacted bytes reaching a run log past a correctly
// placed pass, because the log ranged the local.
func cucapRedactRaws(red *dropcapRedactor, in []json.RawMessage) []json.RawMessage {
	if in == nil {
		return nil
	}
	out := make([]json.RawMessage, 0, len(in))
	for _, raw := range in {
		out = append(out, json.RawMessage(red.redact(raw)))
	}
	return out
}

// cucapScanApplied returns s.applied() with every path class PRESENT, so a class the
// run armed with nothing is recorded as armed-nothing rather than missing.
//
// The two arming paths behave differently for an absent value, and only one behaves
// the way "an unset class arms nothing" suggests. addDynamic appends unconditionally,
// so a class handed "" lands as `false`. addDynamicPath goes through
// dropcapPathSpellings, which returns nil for "" — so NO needle is appended and
// applied, which builds its map by ranging the needles, carries no key for that class
// AT ALL. An omitted class reads exactly like a class nobody ever thought about,
// which is the outcome the field exists to prevent. operator_home has that hole
// whenever realHome is empty, so all four are closed here rather than only the ones
// that are certain.
//
// Only a MISSING key is added; an existing entry — true or false — is left alone. A
// completion that assigned false unconditionally would report every armed class as
// armed-nothing while still satisfying the presence check, and
// TestCucapScanApplied_RecordsAnArmedNothingClassForAnAbsentPath's workdir control is
// its sole red.
//
// applied returns a FRESH map per call, so this mutates a map it owns.
func cucapScanApplied(s dropcapScanner) map[string]bool {
	out := s.applied()
	for _, class := range []string{
		dropcapClassTempHome,
		dropcapClassOperatorHome,
		dropcapClassArtifactDir,
		dropcapClassWorkdir,
	} {
		if _, ok := out[class]; !ok {
			out[class] = false
		}
	}
	return out
}

// cucapScanRecord returns the exact bytes a write will put on disk, refusing the
// record outright when the deny-scan hits.
//
// IT MAKES NO FILESYSTEM CALL OF ANY KIND, and that is the mechanism rather than a
// coincidence of layout. This step is the sole producer of the bytes any writer puts
// on disk and the scan is INSIDE it, so no writer can hold a blob the scan has not
// passed; the first filesystem call anywhere on the path is strictly after this
// returns. A hit therefore leaves every target directory holding nothing — no record,
// no .tmp under any name, nothing written and then deleted.
//
// THE CAP LIVES HERE, NOT IN THE WRITER AND NOT IN THE CALLER. The bytes scanned are
// then exactly the bytes on disk, so a needle surviving only in the truncated tail is
// correctly not reported — the file cannot carry what the scan did not see, and the
// scan does not refuse over bytes the file will not carry. Capping in the caller would
// mutate the caller's record; capping in a writer would leave the scanned bytes longer
// than the disk bytes.
//
// rec is never mutated — the cap lands on a local copy. THE SHALLOW COPY IS SUFFICIENT
// ONLY BECAUSE THE SOLE MUTATION IS TO A string FIELD: `out := *rec` shares every
// slice header with the caller's record, so a future step capping a slice-valued field
// would be writing through the caller's backing array from behind a copy that looks
// defensive.
//
// # The refusal, and the two rules that bind every line added here
//
// On a hit: t.Fatalf naming the COUNT and the CLASS NAMES and nothing else. No
// excerpt, no record dump, no needle value, no byte offset. "Let me include the
// payload to help debug it" is exactly how a token reaches a salvaged run log and
// inverts the whole control.
//
// NEVER FORMAT THE SCANNER. newDropcapScanner stores CLAUDE_CODE_OAUTH_TOKEN and
// ANTHROPIC_API_KEY as needle values, so a dropcapScanner in scope is TWO LIVE
// CREDENTIALS IN A STRUCT — no %v, %+v, %#v or %q on the scanner, on a needle or on
// the needle slice, here or in any new row. cucapScrubbed does not catch it: that
// guard reads the CHILD's stderr, and this would be the harness's own output.
//
// t.Fatalf requires the test goroutine. This step is called directly from one — the
// stdout reader goroutine calls neither it nor any writer — so t.Helper() plus a
// direct call is the whole discipline. Do not call it from a goroutine.
func cucapScanRecord(t *testing.T, scanner dropcapScanner, rec *cucapRecord) []byte {
	t.Helper()

	out := *rec
	out.StderrCapture = capFixtureCapture(out.StderrCapture)

	data, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		t.Fatalf("#%s: marshal the record for claude_version %q: %v", cucapTicket, rec.ClaudeVersion, err)
	}
	hits, _ := scanner.scan(data)
	if len(hits) > 0 {
		t.Fatalf("#%s: deny-scan found %d denied class(es) in the record for claude_version %q: %v\n"+
			"NOTHING was written — no record, no .tmp, nothing under any target directory at all. "+
			"Extend the redactor's table with the named class and re-run one live capture. The "+
			"offending value is deliberately not printed, and neither is its offset: putting either in "+
			"a run log this pipeline salvages is exactly the exposure this scan exists to prevent",
			cucapTicket, len(hits), rec.ClaudeVersion, hits)
	}
	return data
}

// cucapWriteBlob writes already-scanned bytes to path, temp-file-then-rename so an
// interrupted run strands no half-written record under the target name for a later
// commit.
//
// 0600 on both copies, following api_retry_capture_test.go rather than the initialize
// family's 0644: the artifact copy lands in a world-readable temp root, and one mode
// for both is one fewer thing to get right per site.
//
// It returns an error rather than fataling. The in-repo promotion is best-effort by
// design — a read-only checkout must not destroy the artifact copy that already
// landed — and the caller decides which failures are fatal.
func cucapWriteBlob(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Base(dir), err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// --- the credential guard ---------------------------------------------------------

// cucapScrubbed t.Fatalf's when stderr contains the non-empty value of either
// credential variable WithWorktreeAuthenticated re-pins into the child's environment,
// and returns silently otherwise.
//
// The record this run writes is committed to a public repo, and an auth failure is
// exactly the condition that makes claude print a long message to stderr.
// capFixtureCapture bounds how much of it lands in the file — but A CAP IS NOT A
// REDACTION: 8 KiB of a credential-bearing message still commits the credential.
//
// Three details, each of which is the difference between a guard and a decoration:
//
//   - An UNSET variable is skipped, never compared against "": every string contains
//     the empty string, so comparing it would fail every run.
//   - It checks the RAW stderr rather than the capped copy. The raw is a superset, so
//     a token past the cap still fails the run — stricter and simpler than reasoning
//     about where the cut lands.
//   - Its message names the variable and prints NOTHING ELSE. An error message that
//     helpfully quotes the leak is the own-goal this guard exists to prevent.
//
// Plain strings.Contains, not crypto/subtle. Constant-time comparison defends a secret
// against a party that does not know it; the only party on the other side here is the
// claude binary, which was handed the token.
func cucapScrubbed(t *testing.T, stderr string) {
	t.Helper()
	for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if v := os.Getenv(name); v != "" && strings.Contains(stderr, v) {
			t.Fatalf("#%s: the child's stderr carries the value of %s; refusing to record it or to "+
				"print an excerpt of it. Nothing was written", cucapTicket, name)
		}
	}
}

// --- the driver ---------------------------------------------------------------------

// runCucapChild spawns one child under pyry's stream-json-in/stream-json-out argv,
// drives one probe turn, writes a get_context_usage request per declared arm, reads
// the daemon's own context-window figure for the same session, and returns the
// completed record.
//
// It t.Fatalf's ONLY for a broken instrument — a pipe or spawn failure, a marshal
// failure, a credential in the child's stderr, or zero stdout lines captured. Every
// other outcome is information and lands in a record field: a probe turn that never
// closed, an arm that went unanswered, one answered with subtype "error", a mismatched
// request_id, a stdin write error, an over-long line, a non-zero exit, a tripped
// deadline, an absent transcript, a failed contextwindow read.
//
// THE REDACTION PASS IS NOT RUN HERE. It belongs to the caller, which owns the
// redactor and the two write sites; running it here would leave this function
// returning a record whose redaction state depends on where the reader looks.
func runCucapChild(t *testing.T, claudeBin, home, workdir string, versionRaw, versionToken string) *cucapRecord {
	t.Helper()

	// The fixed stream-json prefix is streamsup's buildArgs'; the cost flags and the
	// pinned session id occupy the `base` slot that function appends after it. The id
	// is pinned so the transcript is resolvable BY NAME, which is what makes AC 2's
	// comparison about the same session claude answered for.
	argv := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", cucapModel,
		"--max-turns", cucapMaxTurns,
		"--session-id", cucapSessionID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), cucapChildBudget)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin, argv...)
	cmd.Dir = workdir

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("#%s: stdin pipe: %v", cucapTicket, err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("#%s: stdout pipe: %v", cucapTicket, err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("#%s: start claude: %v", cucapTicket, err)
	}

	// Single reader goroutine over the child's stdout. It exits on EOF — which follows
	// either the stdin close below or the context kill — and closes readerDone, so no
	// goroutine outlives its child. scannerErr is written only before that close and
	// read only after it; that ordering is the whole synchronisation for it. It calls
	// no t.Fatalf, which is what keeps that discipline true.
	rec := &setModeRecorder{}
	var scannerErr string
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		sc := bufio.NewScanner(stdoutPipe)
		sc.Buffer(make([]byte, 0, 64*1024), setModeScanMax)
		for sc.Scan() {
			rec.add(sc.Bytes())
		}
		if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
			scannerErr = err.Error()
		}
	}()

	var writeErrs []string
	writeLine := func(what string, line []byte) {
		if _, err := stdinPipe.Write(line); err != nil {
			writeErrs = append(writeErrs, fmt.Sprintf("%s: %v", what, err))
		}
	}

	turnOne, err := setModeTurnLine(cucapPromptOne)
	if err != nil {
		t.Fatalf("#%s: %v", cucapTicket, err)
	}

	writeLine("turn 1", turnOne)
	turnClosed := setModeWaitFor(rec.resultCount, 1, cucapTurnBudget)
	if !turnClosed {
		t.Logf("#%s: turn 1 produced no result line within %s, so the send points below do NOT follow "+
			"a completed turn. Recorded rather than fatal — turns_completed is what tells a reader, "+
			"and fixtureWorthy refuses to promote such a record. Continuing",
			cucapTicket, cucapTurnBudget)
	}

	// One arm per row of cucapArms, in order, on the one held-open stdin. The
	// per-arm request id is what keeps the two replies attributable to their own arm;
	// see cucapRequestID for why this family does not share one literal.
	arms := make([]cucapArmRecord, 0, len(cucapArms))
	for _, arm := range cucapArms {
		requestID := cucapRequestID(arm.id)
		line, err := cucapLine(requestID, arm.detail)
		if err != nil {
			t.Fatalf("#%s[%s]: %v", cucapTicket, arm.id, err)
		}

		// The anchor, read BEFORE the write and never after it: nothing claude writes
		// in RESPONSE to this request may fall before the anchor, and only a read taken
		// before the write guarantees that. The residual that remains runs the safe way
		// — a line arriving between this read and the write is counted inside the
		// window although it preceded the request.
		sendPointIndex := len(rec.snapshotLines())
		baseline := rec.controlResponseCount()

		t.Logf("#%s[%s]: writing control request: %s", cucapTicket, arm.id, bytes.TrimRight(line, "\n"))
		writeStart := time.Now()
		writeLine("control request "+arm.id, line)

		// The wait's own RESULT, kept rather than dropped into the log. ControlResponses
		// is snapshotted after cmd.Wait() below, so a reply arriving past this budget
		// still lands in the record — under a log line that already claimed absence.
		// within_wait is the only place that distinction survives the run.
		withinWait := setModeWaitFor(rec.controlResponseCount, baseline+1, cucapControlBudget)
		roundTrip := time.Since(writeStart)
		if !withinWait {
			t.Logf("#%s[%s]: no control_response within %s (recorded as absence, continuing) — an "+
				"unanswered send point is this run's measurement, not its failure",
				cucapTicket, arm.id, cucapControlBudget)
		}

		arms = append(arms, cucapArmRecord{
			Arm:                arm.id,
			Detail:             arm.detail,
			ControlRequestID:   requestID,
			ControlRequestSent: json.RawMessage(bytes.TrimRight(line, "\n")),
			WithinWait:         withinWait,
			RoundTripMs:        roundTrip.Milliseconds(),
			SendPointIndex:     sendPointIndex,
		})
	}

	if err := stdinPipe.Close(); err != nil {
		writeErrs = append(writeErrs, fmt.Sprintf("stdin close: %v", err))
	}
	waitErr := cmd.Wait()
	<-readerDone
	duration := time.Since(start)

	// AFTER the join, so a t.Fatalf here cannot strand the reader goroutine — and
	// BEFORE every consumer of stderr: before the zero-lines fatal, which prints up to
	// the cap into a run log this pipeline salvages, and before any write, because a
	// poisoned record must never reach disk at all, not even to be deleted afterwards.
	cucapScrubbed(t, stderrBuf.String())

	lines := rec.snapshotLines()
	if len(lines) == 0 {
		t.Fatalf("#%s: claude produced no stdout; there is nothing to capture\nstderr:\n%s\nwaitErr: %v",
			cucapTicket, truncateString(stderrBuf.String(), stderrFixtureCap), waitErr)
	}
	dropped := 0
	if len(lines) > cucapMaxStdoutLines {
		dropped = len(lines) - cucapMaxStdoutLines
		lines = lines[:cucapMaxStdoutLines]
	}

	// Per-arm reads, taken once the child has exited so a reply that landed past its
	// bounded wait is still attributed to its arm.
	responses := rec.snapshotControlResponses()
	for i := range arms {
		arm := &arms[i]
		arm.ControlResponses = cucapResponsesFor(responses, arm.ControlRequestID)
		arm.Answered = len(arm.ControlResponses) > 0
		arm.RequestIDMatched = setModeResponseIDMatches(arm.ControlResponses, arm.ControlRequestID)
		arm.ResponseSubtype = cucapSubtypeOf(arm.ControlResponses)
		arm.NumericLeaves, arm.NumericLeavesTruncated, arm.NumericLeavesSkipped =
			cucapLeaves(arm.ControlResponses, cucapMaxNumericLeaves)
		arm.ClaudeTotalSource, arm.ClaudeTotalValue = cucapSelect(arm.NumericLeaves, cucapTotalPathSuffixes)
		arm.ClaudePercentSource, arm.ClaudePercentValue = cucapSelect(arm.NumericLeaves, cucapPercentPathSuffixes)
	}

	// AC 2: the daemon's own reading of the SAME session, beside claude's. Nothing
	// here computes a verdict on whether they agree — that is the later slice's, and a
	// verdict computed in two places is a second source of truth.
	windows := cucapWindowsFrom(lines)
	transcriptPath, transcriptFound := cucapTranscriptPath(home, cucapSessionID, cucapTranscriptBudget)
	if !transcriptFound {
		t.Logf("#%s: no transcript named %s.jsonl appeared under the pinned $HOME within %s. Recorded "+
			"rather than fatal — whether a pinned --session-id survives to the transcript's filename "+
			"on this release is itself something this run measures",
			cucapTicket, cucapSessionID, cucapTranscriptBudget)
	}
	usage, readErr := contextwindow.Read(transcriptPath, windows)
	daemonReadErr := ""
	if readErr != nil {
		// The error text is recorded rather than dropped, and it is redacted with every
		// other string before the write: contextwindow.Read's error WRAPS THE FULL
		// TRANSCRIPT PATH, which is an operator path under the pinned $HOME.
		daemonReadErr = readErr.Error()
	}

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	waitErrStr := ""
	if waitErr != nil {
		waitErrStr = waitErr.Error()
	}

	return &cucapRecord{
		IsCapture: true,

		ClaudeVersionRaw: versionRaw,
		ClaudeVersion:    versionToken,

		// The BASE NAME, never claudeBin's directory — see cucapRecord's doc for why
		// that is a deliberate departure from the sibling captures.
		Argv:    append([]string{filepath.Base(claudeBin)}, argv...),
		Prompts: []string{cucapPromptOne},

		Arms: arms,

		TurnsCompleted:     rec.resultCount(),
		StdoutEvents:       lines,
		StdoutLinesDropped: dropped,
		NonJSONLineCount:   rec.nonJSONCount(),
		TurnBoundaries:     rec.snapshotBoundaries(),

		TranscriptFound:    transcriptFound,
		WindowsObserved:    windows,
		DaemonUsedTokens:   usage.UsedTokens,
		DaemonWindowTokens: usage.WindowTokens,
		DaemonPercent:      cucapPercent(usage.UsedTokens, usage.WindowTokens),
		DaemonReadError:    daemonReadErr,

		StdinWriteErrors: writeErrs,
		// RAW, deliberately not pre-truncated: cucapScanRecord caps its own local copy,
		// so the record in memory holds the raw string and the file on disk holds the
		// capped one.
		StderrCapture:          stderrBuf.String(),
		ExitCode:               exitCode,
		WaitError:              waitErrStr,
		ContextDeadlineTripped: errors.Is(ctx.Err(), context.DeadlineExceeded),
		DurationMs:             duration.Milliseconds(),
		ScannerError:           scannerErr,
	}
}

// --- the live capture -----------------------------------------------------------

// TestRealClaude_ContextUsageCapture drives one child, writes a get_context_usage
// request at each `detail` value, and writes the record. IT ASSERTS NOTHING ABOUT
// CLAUDE.
//
// Ordering is load-bearing in one place and it is the same trap
// TestRealClaude_APIRetryCapture records: newDropcapScanner reads
// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY via os.Getenv AS DENY NEEDLES, and
// WithWorktreeAuthenticated is what re-pins them into this process. Building the
// scanner FIRST yields an EMPTY needle, which dropcapScanner.scan reports as
// notApplied — silently skipped, never fatal — so the credential net would be OFF
// while every message still read green.
func TestRealClaude_ContextUsageCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE. `make e2e-realclaude` never sets a custom
	// PYRY_PROBE_* variable, so an env-armed probe skips on the ENV check BEFORE the
	// credential check, the live gate passes vacuously, and the fixture never lands —
	// CLAUDE.md § Testing's #1763 failure exactly, where a green gate and a spent
	// budget look identical whether the bytes landed or not. The variable below can
	// only FORCE a re-capture over an existing fixture.
	force := os.Getenv(cucapEnableEnv) == "1"
	if existing, _ := filepath.Glob(cucapFixtureGlob); len(existing) > 0 && !force {
		t.Skipf("#%s get_context_usage capture: %v already exists, so there is nothing to capture and "+
			"this costs no claude turn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 20m -v \\\n"+
			"    -run '^TestRealClaude_ContextUsageCapture$' ./internal/e2e/realclaude/",
			cucapTicket, existing, cucapEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials; MUST precede the scanner

	// Deliberately NOT t.TempDir(): #2229's capture fired inside the dispatcher's
	// gate-only worktree and lost its in-repo fixture when that worktree was
	// discarded. This directory is outside every worktree and is the copy that
	// survives such a run.
	artifactDir, err := os.MkdirTemp("", cucapArtifactPrefix)
	if err != nil {
		t.Fatalf("#%s: create artifact dir: %v", cucapTicket, err)
	}

	// A fresh EMPTY directory under the pinned $HOME, deliberately not a git repo: no
	// branch name and no file content can reach a payload, and less project context
	// makes the probe turn cheaper.
	workdir := filepath.Join(home, cucapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#%s: create workdir: %v", cucapTicket, err)
	}

	versionRaw, versionToken := captureClaudeVersion(t)
	t.Logf("#%s: claude version %q (token %q)", cucapTicket, versionRaw, versionToken)

	// The empty slot is fifoPath, and the emptiness is a fact this probe records
	// rather than an omission: it holds no FIFO. dropcapRedactor.add guards "" —
	// strings.ReplaceAll(s, "", x) would otherwise insert x between every character.
	//
	// The nonce must never be 0: FormatInt spells it "0", which is not empty, so the
	// guard passes it through and every zero digit in the record becomes $NONCE.
	nonce := time.Now().UnixNano()
	red := newDropcapRedactor(home, artifactDir, workdir, "", cucapSessionID, nonce)

	// The value carries the two credentials os.Getenv returned. NEVER %v IT — see
	// cucapScanRecord's doc. applied()'s map is the safe diagnostic.
	scanner := newDropcapScanner(home, artifactDir, workdir)

	t.Logf("#%s capture artifacts: %s", cucapTicket, red.str(artifactDir))

	record := runCucapChild(t, claudeBin, home, workdir, versionRaw, versionToken)

	// The redaction pass, then the census assigned FROM WHAT IT RETURNS, then the
	// arming census — all before the scan, and all before any log line that reads a
	// claude-authored field. Position alone does not protect a log site: the pass
	// assigns fresh values rather than writing through what it was handed, so every
	// site below must read the RECORD's own field. The initialize family measured
	// unredacted bytes reaching a run log past a correctly-placed pass because the log
	// ranged the pre-pass local.
	record.Redaction = cucapRedactRecord(red, record)
	record.CredentialScanApplied = cucapScanApplied(scanner)

	// The scan runs ONCE and both writes share its bytes, so the artifact copy and the
	// in-repo copy cannot differ and neither can escape the net.
	blob := cucapScanRecord(t, scanner, record)

	// The artifact copy is UNCONDITIONAL and is written FIRST. It is the copy that
	// survives a gate-only run, so a promotion refusal below must never be able to
	// leave the run with nothing on disk.
	artifactPath := filepath.Join(artifactDir, cucapRecordName)
	if err := cucapWriteBlob(artifactPath, blob); err != nil {
		t.Fatalf("#%s: writing the artifact record: %v", cucapTicket, err)
	}
	t.Logf("#%s: record written to %s", cucapTicket, red.str(artifactPath))

	// Never %+v the record into a log or a fatal: that moves capped child output out
	// of the bounded file and into an unbounded run log, the exact thing the cap exists
	// to prevent. The fields below are counts, bools and durations, plus per-arm
	// subtypes which are claude's declared vocabulary and are read POST-pass.
	t.Logf("#%s: %d line(s) recorded (%d dropped), %d non-JSON, turns_completed=%d, arms=%d "+
		"answered=%d, exit=%d, deadline_tripped=%v, scanner_error=%q, %s",
		cucapTicket, len(record.StdoutEvents), record.StdoutLinesDropped, record.NonJSONLineCount,
		record.TurnsCompleted, len(record.Arms), record.answeredArms(), record.ExitCode,
		record.ContextDeadlineTripped, record.ScannerError, time.Duration(record.DurationMs)*time.Millisecond)
	for _, arm := range record.Arms {
		t.Logf("#%s[%s]: detail=%q answered=%v within_wait=%v round_trip=%dms subtype=%q "+
			"id_matched=%v leaves=%d (truncated %d, skipped %d) total=%q@%q percent=%q@%q",
			cucapTicket, arm.Arm, arm.Detail, arm.Answered, arm.WithinWait, arm.RoundTripMs,
			arm.ResponseSubtype, arm.RequestIDMatched, len(arm.NumericLeaves),
			arm.NumericLeavesTruncated, arm.NumericLeavesSkipped,
			arm.ClaudeTotalValue, arm.ClaudeTotalSource, arm.ClaudePercentValue, arm.ClaudePercentSource)
		// record.ControlResponses, never the pre-pass locals: the pass builds fresh byte
		// slices, so ranging the record prints exactly the bytes the file carries.
		for i, resp := range arm.ControlResponses {
			t.Logf("#%s[%s]: control_response[%d] redacted: %s", cucapTicket, arm.Arm, i, resp)
		}
	}
	t.Logf("#%s: AC 2 — claude's reading beside the daemon's: transcript_found=%v windows_observed=%d "+
		"daemon_used=%d daemon_window=%d daemon_percent=%q daemon_read_error=%q",
		cucapTicket, record.TranscriptFound, len(record.WindowsObserved), record.DaemonUsedTokens,
		record.DaemonWindowTokens, record.DaemonPercent, record.DaemonReadError)

	// The same deny-scanned bytes, promoted in-repo so the run that produced them is
	// the run that commits them.
	if reason, ok := record.fixtureWorthy(); !ok {
		t.Logf("#%s: NOT promoted — %s. The artifact record above is the evidence; read it, then "+
			"re-run rather than hand-editing a fixture", cucapTicket, reason)
		return
	}
	fixturePath, err := cucapFixturePath(record.ClaudeVersion)
	if err != nil {
		// Unreachable while fixtureWorthy runs the same check, and kept because the two
		// are separate functions and a future edit to either could part them.
		t.Logf("#%s: NOT promoted — %v", cucapTicket, err)
		return
	}
	if err := cucapWriteBlob(fixturePath, blob); err != nil {
		t.Logf("#%s: in-repo promotion to %s failed (%v). The artifact copy stands and is what a "+
			"repair leg commits", cucapTicket, fixturePath, err)
		return
	}
	t.Logf("#%s: FIXTURE WRITTEN to %s — git add it, and fill internal/streamsup's "+
		"contextUsagePinnedShapes from the arms above IN THE SAME COMMIT. A fixture landing with an "+
		"empty pin FATALS `make check` by design. If this run was the dispatcher's gate-only "+
		"real-claude lap, this in-repo copy goes out with the worktree — recover %s instead",
		cucapTicket, fixturePath, red.str(artifactPath))
}

// --- the offline tests ------------------------------------------------------------
//
// Every test below spawns nothing, reads nothing off disk, and must report PASS — not
// SKIP — on a machine with no claude and no credentials. This file EXECS, so it
// correctly takes no finOfflineExecBans entry; that registry is keyed by filename and
// consumed by `for f := range finOfflineExecBans`, so the absence costs nothing.

// TestCucapArms_AreDistinct is the lock on the arm table. Every downstream identifier
// is minted from these two columns — the request id, the record's arm name, and
// internal/streamsup's pinned shape — so a duplicate id makes two arms write one
// request id and their replies become mutually attributable, while a duplicate detail
// makes the capture drive the same measurement twice under two names.
func TestCucapArms_AreDistinct(t *testing.T) {
	t.Parallel()

	if len(cucapArms) < 2 {
		t.Fatalf("#%s: cucapArms declares %d arm(s); AC 1 asks for a request at EACH detail value, "+
			"and with fewer than two there is no comparison to make", cucapTicket, len(cucapArms))
	}
	ids := map[string]bool{}
	details := map[string]bool{}
	for _, arm := range cucapArms {
		if arm.id == "" || arm.detail == "" {
			t.Errorf("#%s: an arm carries an empty column (%+v); the id names it in the record and in "+
				"internal/streamsup's pin, and the detail is what goes on the wire", cucapTicket, arm)
		}
		if ids[arm.id] {
			t.Errorf("#%s: arm id %q is declared twice, so both arms mint the request id %q and "+
				"cucapResponsesFor attributes either reply to either arm",
				cucapTicket, arm.id, cucapRequestID(arm.id))
		}
		if details[arm.detail] {
			t.Errorf("#%s: detail %q is declared twice, so the capture drives one measurement under "+
				"two names and the pin describes a comparison that never happened", cucapTicket, arm.detail)
		}
		ids[arm.id] = true
		details[arm.detail] = true
	}
}

// TestCucapChildBudget_ExceedsThePerStepWaitSum: the outer child deadline strictly
// exceeds the largest total the drive sequence can spend in per-step waits.
//
// BOTH SIDES ARE DERIVED FROM THE BUDGET CONSTANTS, never from literal durations.
// That is what keeps it true when a per-step budget moves — and it has to, because
// the set_permission_mode family carries the identical arithmetic UNFIXED (a 240s
// outer against a 285s sum) and a literal bound here would say nothing when this
// file's own budgets are next retuned.
func TestCucapChildBudget_ExceedsThePerStepWaitSum(t *testing.T) {
	t.Parallel()

	worst := cucapArmWaitSum()

	// The subject.
	if cucapChildBudget <= worst {
		t.Errorf("#%s: cucapChildBudget is %s while the drive sequence's per-step waits can spend %s, "+
			"so the outer deadline can fire before the waits it contains have expired. Every per-step "+
			"budget exists so that an ABSENCE means absence rather than impatience — "+
			"cucapControlBudget's own doc says so — and a deadline that trips first voids that "+
			"guarantee for exactly the reading this file takes: whether a send point goes unanswered. "+
			"Nothing else reddens when it happens, because context_deadline_tripped is a PASSING "+
			"recorded outcome on purpose", cucapTicket, cucapChildBudget, worst)
	}

	// Vacuity control A, and the sole red for a helper that returns zero, drops the
	// turn term, or charges one control wait instead of one per arm — each of which
	// makes the subject above pass for the wrong reason.
	if want := cucapTurnBudget + time.Duration(len(cucapArms))*cucapControlBudget; worst != want {
		t.Errorf("#%s: the per-step wait sum is %s, want %s — one turn wait plus one control wait per "+
			"declared arm, which is what the drive sequence spends. A sum smaller than the run's real "+
			"waits makes the subject check above pass against a deadline that still cannot dominate "+
			"them", cucapTicket, worst, want)
	}

	// Vacuity control B: the sum must actually GROW with the arm table. A helper that
	// hardcoded two control waits would satisfy A today and silently understate the
	// run the moment a third arm is declared.
	if got := cucapArmWaitSum() - cucapTurnBudget; got != time.Duration(len(cucapArms))*cucapControlBudget {
		t.Errorf("#%s: the sum's control term is %s over %d declared arm(s), so it does not scale with "+
			"cucapArms and a third arm would be charged nothing", cucapTicket, got, len(cucapArms))
	}
}

// TestCucapLine_CarriesTheSubtypeAndDetail pins the one line this probe puts on the
// wire. It decodes what cucapLine produced rather than comparing strings, so the
// assertion is about the SHAPE claude receives and not about Go's field ordering.
//
// The single-physical-line property is checked because the request travels on a
// line-delimited transport: an embedded newline would split one request into two
// unparseable fragments, and json.Marshal escapes any newline inside a value, so the
// appended one is the only raw newline that may exist.
func TestCucapLine_CarriesTheSubtypeAndDetail(t *testing.T) {
	t.Parallel()

	for _, arm := range cucapArms {
		t.Run(arm.id, func(t *testing.T) {
			t.Parallel()
			line, err := cucapLine(cucapRequestID(arm.id), arm.detail)
			if err != nil {
				t.Fatalf("#%s: %v", cucapTicket, err)
			}
			if n := bytes.Count(line, []byte("\n")); n != 1 || line[len(line)-1] != '\n' {
				t.Fatalf("#%s: the control line carries %d newline(s) and does not end in exactly one; "+
					"the transport is line-delimited, so an embedded newline splits one request into "+
					"two unparseable fragments", cucapTicket, n)
			}
			var got cucapRequest
			if err := json.Unmarshal(bytes.TrimRight(line, "\n"), &got); err != nil {
				t.Fatalf("#%s: the control line does not decode: %v", cucapTicket, err)
			}
			if got.Type != "control_request" {
				t.Errorf("#%s: type = %q, want %q", cucapTicket, got.Type, "control_request")
			}
			if got.Request.Subtype != cucapSubtype {
				t.Errorf("#%s: subtype = %q, want %q — a misspelling here makes every arm come back "+
					"with an unrecognized-subtype error and read as a finding about claude",
					cucapTicket, got.Request.Subtype, cucapSubtype)
			}
			if got.Request.Detail != arm.detail {
				t.Errorf("#%s: detail = %q, want %q — the two values differ in what claude computes, "+
					"so an arm sending the wrong one measures the other arm twice",
					cucapTicket, got.Request.Detail, arm.detail)
			}
			if got.RequestID != cucapRequestID(arm.id) {
				t.Errorf("#%s: request_id = %q, want %q; both arms write on ONE stdin, so a shared or "+
					"wrong id makes cucapResponsesFor attribute either reply to either arm",
					cucapTicket, got.RequestID, cucapRequestID(arm.id))
			}
		})
	}
}

// TestCucapResponsesFor_AttributesEachArmsOwnReply is the correlation lock, and it is
// the one piece of logic that a two-arm-on-one-stdin design needs and a one-arm probe
// does not. Both replies land on one stdout; if either arm could claim the other's,
// the record's per-arm subtypes, latencies and leaf maps would all describe the wrong
// request.
func TestCucapResponsesFor_AttributesEachArmsOwnReply(t *testing.T) {
	t.Parallel()

	summary := json.RawMessage(`{"type":"control_response","response":{"request_id":"context-usage-summary","subtype":"success"}}`)
	full := json.RawMessage(`{"type":"control_response","request_id":"context-usage-full","subtype":"error"}`)
	stray := json.RawMessage(`{"type":"control_response","response":{"request_id":"someone-elses","subtype":"success"}}`)
	all := []json.RawMessage{summary, full, stray}

	t.Run("the nested placement", func(t *testing.T) {
		t.Parallel()
		got := cucapResponsesFor(all, "context-usage-summary")
		if len(got) != 1 || !bytes.Equal(got[0], summary) {
			t.Errorf("#%s: matched %d line(s), want exactly the summary arm's; internal/streamsup's "+
				"parser records that request_id arrives nested under `response`, so a reader that "+
				"only looks at top level attributes nothing", cucapTicket, len(got))
		}
	})

	t.Run("the top-level placement", func(t *testing.T) {
		t.Parallel()
		got := cucapResponsesFor(all, "context-usage-full")
		if len(got) != 1 || !bytes.Equal(got[0], full) {
			t.Errorf("#%s: matched %d line(s), want exactly the full arm's", cucapTicket, len(got))
		}
	})

	t.Run("a reply correlated to nothing this probe sent is returned to nobody", func(t *testing.T) {
		t.Parallel()
		// The stray line stays in stdout_events, where it is still evidence. What it
		// must not do is land in an arm's record and be read as that arm's answer.
		for _, arm := range cucapArms {
			if got := cucapResponsesFor(all, cucapRequestID(arm.id)); len(got) > 1 {
				t.Errorf("#%s: arm %q matched %d lines; a reply carrying an id this probe never sent "+
					"was attributed to it, and its subtype would be recorded as that arm's answer",
					cucapTicket, arm.id, len(got))
			}
		}
	})

	t.Run("an empty request id matches nothing", func(t *testing.T) {
		t.Parallel()
		// Without this guard an arm whose id was never minted would claim every reply
		// that carries no request_id at all.
		if got := cucapResponsesFor(all, ""); len(got) != 0 {
			t.Errorf("#%s: an empty request id matched %d line(s), want 0", cucapTicket, len(got))
		}
	})
}

// TestCucapSubtypeOf_ReadsAllThreePlacements is the targeted check on the envelope
// read. The doubly-nested row is the load-bearing one and it is not hypothetical: a
// summariser reading only the first two levels reported a false absence against a live
// 2.1.239 reply carrying six models, which is what the third placement was added for.
//
// internal/streamsup's contextUsageObservedSubtype is an independent second copy of
// this search and carries its own table; that independence is what makes the pin a
// comparison rather than an agreement by construction.
func TestCucapSubtypeOf_ReadsAllThreePlacements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"top level", []string{`{"type":"control_response","subtype":"success"}`}, "success"},
		{"nested under response",
			[]string{`{"type":"control_response","response":{"subtype":"success"}}`}, "success"},
		{"nested under response.response, the deeper measured shape",
			[]string{`{"type":"control_response","response":{"response":{"subtype":"success"}}}`}, "success"},
		{"an error subtype is a recorded shape, not a failure",
			[]string{`{"type":"control_response","response":{"subtype":"error","error":"unrecognized subtype"}}`},
			"error"},
		{"no placement carries a subtype",
			[]string{`{"type":"control_response","response":{"error":"x"}}`}, ""},
		{"a line that is not JSON is skipped rather than fatal", []string{`not json`}, ""},
		{"no lines at all", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := make([]json.RawMessage, 0, len(tc.lines))
			for _, l := range tc.lines {
				lines = append(lines, json.RawMessage(l))
			}
			if got := cucapSubtypeOf(lines); got != tc.want {
				t.Errorf("#%s: cucapSubtypeOf = %q, want %q; this fills response_subtype, which "+
					"internal/streamsup's pin compares against a re-derivation of the same bytes",
					cucapTicket, got, tc.want)
			}
		})
	}
}

// TestCucapLeaves_WalksEveryNumericLeaf covers the reader that makes this capture
// useful without knowing the payload's shape.
//
// The non-finite row is the one that is about SAFETY rather than coverage: a response
// carrying 1e999 decodes to +Inf, json.Marshal ERRORS on a non-finite float, and a
// float64-valued map would abort the whole capture as a broken instrument over data
// that is in fact claude's own answer. Storing json.Number tokens makes that
// unreachable, and this row is what would redden if someone "simplified" the map to
// float64.
func TestCucapLeaves_WalksEveryNumericLeaf(t *testing.T) {
	t.Parallel()

	t.Run("nested objects and arrays are pathed", func(t *testing.T) {
		t.Parallel()
		line := json.RawMessage(`{"response":{"usage":{"total_tokens":1234,"per":[{"n":1},{"n":2}]},"ok":true,"name":"x"}}`)
		leaves, truncated, skipped := cucapLeaves([]json.RawMessage{line}, cucapMaxNumericLeaves)
		want := map[string]string{
			"response.usage.total_tokens": "1234",
			"response.usage.per[0].n":     "1",
			"response.usage.per[1].n":     "2",
		}
		if len(leaves) != len(want) {
			t.Fatalf("#%s: walked %d leaf/leaves %v, want %d %v — a walk that stops at the first "+
				"level records nothing a later slice can declare a field set from",
				cucapTicket, len(leaves), leaves, len(want), want)
		}
		for path, value := range want {
			if leaves[path] != value {
				t.Errorf("#%s: leaf %q = %q, want %q", cucapTicket, path, leaves[path], value)
			}
		}
		if truncated != 0 || skipped != 0 {
			t.Errorf("#%s: truncated=%d skipped=%d over a small well-formed response, want 0 and 0",
				cucapTicket, truncated, skipped)
		}
	})

	t.Run("a non-finite number is skipped and counted, never marshalled", func(t *testing.T) {
		t.Parallel()
		line := json.RawMessage(`{"a":1e999,"b":7}`)
		leaves, _, skipped := cucapLeaves([]json.RawMessage{line}, cucapMaxNumericLeaves)
		if skipped != 1 {
			t.Errorf("#%s: skipped=%d, want 1 — an unrepresentable number must be counted rather than "+
				"stored, because a float64 map carrying +Inf makes json.Marshal fail and aborts the "+
				"whole capture over data that is claude's own answer", cucapTicket, skipped)
		}
		if leaves["b"] != "7" {
			t.Errorf("#%s: the finite sibling leaf is %q, want %q; one bad number must not discard the "+
				"rest of the response", cucapTicket, leaves["b"], "7")
		}
		// The whole point: what came back is marshallable.
		if _, err := json.Marshal(leaves); err != nil {
			t.Errorf("#%s: the leaf map does not marshal (%v), so the record could never be written",
				cucapTicket, err)
		}
	})

	t.Run("a large token survives verbatim rather than through a float", func(t *testing.T) {
		t.Parallel()
		// A float64 round trip would render this as 1.2345678901234568e+18.
		line := json.RawMessage(`{"n":1234567890123456789}`)
		leaves, _, skipped := cucapLeaves([]json.RawMessage{line}, cucapMaxNumericLeaves)
		if skipped != 0 || leaves["n"] != "1234567890123456789" {
			t.Errorf("#%s: leaf n = %q (skipped %d), want the token verbatim; the record must be "+
				"byte-faithful to what claude wrote rather than to Go's re-rendering of it",
				cucapTicket, leaves["n"], skipped)
		}
	})

	t.Run("truncation is by sorted path and is counted", func(t *testing.T) {
		t.Parallel()
		line := json.RawMessage(`{"d":4,"a":1,"c":3,"b":2}`)
		leaves, truncated, _ := cucapLeaves([]json.RawMessage{line}, 2)
		if truncated != 2 {
			t.Errorf("#%s: truncated=%d, want 2 — a drop that is not counted is a silent truncation, "+
				"and a reader cannot tell it from a response that carried fewer leaves",
				cucapTicket, truncated)
		}
		// SORTED, not map-iteration order: an unsorted cut writes a different subset
		// into the committed fixture on every run.
		if len(leaves) != 2 || leaves["a"] != "1" || leaves["b"] != "2" {
			t.Errorf("#%s: kept %v, want the two lexically smallest paths; an unsorted cut makes the "+
				"committed artifact differ between two runs over identical input", cucapTicket, leaves)
		}
	})

	t.Run("multiple lines are prefixed so identical paths stay distinct", func(t *testing.T) {
		t.Parallel()
		one := json.RawMessage(`{"n":1}`)
		two := json.RawMessage(`{"n":2}`)
		leaves, _, _ := cucapLeaves([]json.RawMessage{one, two}, cucapMaxNumericLeaves)
		if len(leaves) != 2 {
			t.Errorf("#%s: two lines carrying the same path produced %d leaf/leaves %v, want 2 — "+
				"without the per-line prefix one reply silently overwrites the other",
				cucapTicket, len(leaves), leaves)
		}
	})

	t.Run("no lines and an undecodable line both yield nothing", func(t *testing.T) {
		t.Parallel()
		if leaves, _, _ := cucapLeaves(nil, cucapMaxNumericLeaves); leaves != nil {
			t.Errorf("#%s: no lines produced %v, want nil", cucapTicket, leaves)
		}
		if leaves, _, _ := cucapLeaves([]json.RawMessage{json.RawMessage(`not json`)}, cucapMaxNumericLeaves); leaves != nil {
			t.Errorf("#%s: an undecodable line produced %v, want nil — it is still in "+
				"control_responses verbatim, which is the evidence", cucapTicket, leaves)
		}
	})
}

// TestCucapSelect_IsDeterministicAndReportsItsSource covers the two convenience
// selections and, more importantly, what they must NOT do.
//
// Determinism is the requirement: Go randomises map iteration, so "the first match"
// over a map answers differently between two runs on identical input, and a committed
// fixture must not change from run to run.
func TestCucapSelect_IsDeterministicAndReportsItsSource(t *testing.T) {
	t.Parallel()

	t.Run("the earlier suffix in the list wins over a later one", func(t *testing.T) {
		t.Parallel()
		leaves := map[string]string{"a.input_tokens": "10", "b.total_tokens": "99"}
		path, value := cucapSelect(leaves, cucapTotalPathSuffixes)
		if path != "b.total_tokens" || value != "99" {
			t.Errorf("#%s: selected %q=%q, want b.total_tokens=99; the list is ORDERED and "+
				"total_tokens precedes input_tokens", cucapTicket, path, value)
		}
	})

	t.Run("ties within one suffix break on the lexically smallest path", func(t *testing.T) {
		t.Parallel()
		leaves := map[string]string{"z.total_tokens": "1", "a.total_tokens": "2"}
		// Repeated because a map-iteration-order bug is intermittent by nature: one
		// call can pass by luck, and the point of the sort is that no call can fail.
		for i := 0; i < 32; i++ {
			path, _ := cucapSelect(leaves, cucapTotalPathSuffixes)
			if path != "a.total_tokens" {
				t.Fatalf("#%s: selected %q on iteration %d, want a.total_tokens every time; a "+
					"selection that follows map iteration writes a different value into the "+
					"committed fixture on every run", cucapTicket, path, i)
			}
		}
	})

	t.Run("nothing matched reports empty rather than inventing a value", func(t *testing.T) {
		t.Parallel()
		path, value := cucapSelect(map[string]string{"x.y": "1"}, cucapTotalPathSuffixes)
		if path != "" || value != "" {
			t.Errorf("#%s: selected %q=%q over a response carrying no candidate key, want both empty. "+
				"The candidate lists are a GUESS and the record says so — an empty source is a "+
				"recorded measurement, and numeric_leaves beside the verbatim response is the "+
				"authoritative field set", cucapTicket, path, value)
		}
	})

	t.Run("the match is case-insensitive on the path", func(t *testing.T) {
		t.Parallel()
		if path, _ := cucapSelect(map[string]string{"usage.totalTokens": "5"}, cucapTotalPathSuffixes); path == "" {
			t.Errorf("#%s: a camelCase key matched nothing; the SDK spells its fields camelCase while "+
				"this repo's captures are snake_case, and which one the CLI puts on the wire is "+
				"exactly what is unmeasured", cucapTicket)
		}
	})
}

// TestCucapWindowsFrom_ReadsTheLastResultLine covers the input to AC 2's join.
//
// LAST WINS, matching contextwindow.Read's own last-usage-wins rule. A non-positive
// window is dropped because Read treats one as absent anyway, and recording it would
// put a value in windows_observed that the join provably ignored.
func TestCucapWindowsFrom_ReadsTheLastResultLine(t *testing.T) {
	t.Parallel()

	t.Run("the last result line wins", func(t *testing.T) {
		t.Parallel()
		lines := []json.RawMessage{
			json.RawMessage(`{"type":"result","modelUsage":{"m":{"contextWindow":100}}}`),
			json.RawMessage(`{"type":"assistant"}`),
			json.RawMessage(`{"type":"result","modelUsage":{"m":{"contextWindow":200}}}`),
		}
		got := cucapWindowsFrom(lines)
		if len(got) != 1 || got["m"] != 200 {
			t.Errorf("#%s: windows = %v, want {m:200} — contextwindow.Read reports the LATEST "+
				"usage-bearing entry, so a join built from an earlier line describes a different turn",
				cucapTicket, got)
		}
	})

	t.Run("a non-positive window is dropped", func(t *testing.T) {
		t.Parallel()
		lines := []json.RawMessage{
			json.RawMessage(`{"type":"result","modelUsage":{"m":{"contextWindow":0},"n":{"contextWindow":50}}}`),
		}
		got := cucapWindowsFrom(lines)
		if len(got) != 1 || got["n"] != 50 {
			t.Errorf("#%s: windows = %v, want {n:50}; contextwindow.Read treats a non-positive "+
				"observed value as absent, so recording one claims an input the join ignored",
				cucapTicket, got)
		}
	})

	t.Run("no result line yields nil, which is exactly nothing observed", func(t *testing.T) {
		t.Parallel()
		lines := []json.RawMessage{json.RawMessage(`{"type":"assistant"}`), json.RawMessage(`bad`)}
		if got := cucapWindowsFrom(lines); got != nil {
			t.Errorf("#%s: windows = %v, want nil — Read then falls back to its default window, "+
				"which is the reading the daemon takes when nothing was observed", cucapTicket, got)
		}
	})
}

// TestCucapPercent_WithholdsADisprovedDenominator is the sole red for a percentage
// computed over contextwindow.Read's "no trustworthy window" signal.
//
// Read reports WindowTokens 0 when the used count DISPROVES the resolved window — it
// withholds the denominator rather than reporting one its own data contradicts.
// Dividing by it here would invent exactly the denominator Read refused, and a fixture
// carrying "+Inf" would be worse than one carrying nothing.
func TestCucapPercent_WithholdsADisprovedDenominator(t *testing.T) {
	t.Parallel()

	if got := cucapPercent(50_000, 200_000); got != "25.00" {
		t.Errorf("#%s: cucapPercent(50000, 200000) = %q, want %q", cucapTicket, got, "25.00")
	}
	if got := cucapPercent(223_075, 0); got != "" {
		t.Errorf("#%s: a zero window produced %q, want the empty string. WindowTokens 0 is "+
			"contextwindow.Read's \"no trustworthy window reading\" report, not a denominator",
			cucapTicket, got)
	}
	if got := cucapPercent(1, -1); got != "" {
		t.Errorf("#%s: a negative window produced %q, want the empty string", cucapTicket, got)
	}
	// Equality is not a contradiction — a session exactly at its window is full — so it
	// keeps its window and reports 100.
	if got := cucapPercent(200_000, 200_000); got != "100.00" {
		t.Errorf("#%s: a full session reads %q, want %q", cucapTicket, got, "100.00")
	}
}

// TestCucapFixturePath_RefusesAVersionThatCouldComposeAPath is the trust boundary on
// the one child-controlled value that reaches a filesystem path. The version token is
// whatever `claude --version` wrote, so this is a genuine boundary and not a
// formality.
func TestCucapFixturePath_RefusesAVersionThatCouldComposeAPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		want    string
		refuse  bool
	}{
		{"an ordinary version", "2.1.259", "testdata/context_usage_v2.1.259.json", false},
		{"the raw form's trailing product name is cut", "2.1.259 (Claude Code)",
			"testdata/context_usage_v2.1.259.json", false},
		{"a traversal", "../../../etc/passwd", "", true},
		{"an absolute path", "/etc/passwd", "", true},
		{"a bare separator", "2.1/259", "", true},
		{"an unreadable version", "<unavailable: exec failed>", "", true},
		{"empty", "", "", true},
		{"whitespace only", "   ", "", true},
		{"over the length cap", strings.Repeat("1", cucapMaxVersionToken+1), "", true},
		{"a NUL byte", "2.1\x00.259", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := cucapFixturePath(tc.version)
			if tc.refuse {
				if err == nil {
					t.Errorf("#%s: cucapFixturePath(%q) = %q with no error; a version token is "+
						"child-controlled and this is what stops one composing a path outside "+
						"testdata/", cucapTicket, tc.version, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("#%s: cucapFixturePath(%q) returned %v, want %q", cucapTicket, tc.version, err, tc.want)
			}
			if got != tc.want {
				t.Errorf("#%s: cucapFixturePath(%q) = %q, want %q", cucapTicket, tc.version, got, tc.want)
			}
		})
	}
}

// cucapWorthyRecord returns the smallest record fixtureWorthy admits, so each row
// below mutates exactly one thing and is the sole red for its own refusal.
func cucapWorthyRecord() *cucapRecord {
	return &cucapRecord{
		IsCapture:      true,
		ClaudeVersion:  "2.1.259",
		Arms:           []cucapArmRecord{{Arm: "summary"}, {Arm: "full"}},
		TurnsCompleted: 1,
		StdoutEvents:   []json.RawMessage{json.RawMessage(`{"type":"result"}`)},
	}
}

// TestCucapFixtureWorthy_PromotesASilentRunAndRefusesAVacuousOne is the promotion
// gate, and the first two rows are the ticket.
//
// THE NON-VACUITY RULE IS NOT ccapRecord.fixtureWorthy's, and the difference is the
// whole design. That one refuses a record holding zero frames of its quarry; here a
// record where NO ARM WAS ANSWERED is the publishable result under AC 3 — whether the
// CLI answers this subtype at all is precisely the unknown — so refusing it would
// delete the branch this capture exists to measure. Same for an arm answered with
// subtype "error": a refusal recorded verbatim is what #2288 designs against.
func TestCucapFixtureWorthy_PromotesASilentRunAndRefusesAVacuousOne(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*cucapRecord)
		promote bool
	}{
		{"a run both arms answered", func(rec *cucapRecord) {
			for i := range rec.Arms {
				rec.Arms[i].Answered = true
				rec.Arms[i].ResponseSubtype = "success"
			}
		}, true},
		{"AC 3: a run NO arm answered is still published", func(*cucapRecord) {}, true},
		{"AC 3: an arm refused with subtype error is still published", func(rec *cucapRecord) {
			rec.Arms[0].Answered = true
			rec.Arms[0].ResponseSubtype = "error"
		}, true},
		{"a tripped deadline is a measurement, not a refusal", func(rec *cucapRecord) {
			rec.ContextDeadlineTripped = true
			rec.ExitCode = -1
		}, true},
		{"no arm driven at all is a filtered partial", func(rec *cucapRecord) {
			rec.Arms = nil
		}, false},
		{"no stdout captured", func(rec *cucapRecord) {
			rec.StdoutEvents = nil
		}, false},
		{"no probe turn completed", func(rec *cucapRecord) {
			rec.TurnsCompleted = 0
		}, false},
		{"a version that cannot name a file", func(rec *cucapRecord) {
			rec.ClaudeVersion = "<unavailable: exec failed>"
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := cucapWorthyRecord()
			tc.mutate(rec)
			reason, ok := rec.fixtureWorthy()
			if ok != tc.promote {
				t.Errorf("#%s: fixtureWorthy() ok = %v, want %v (reason %q)",
					cucapTicket, ok, tc.promote, reason)
			}
			if !ok && reason == "" {
				t.Errorf("#%s: fixtureWorthy() refused without naming a reason; the log line the run "+
					"prints would say nothing and an operator could not tell what to re-run",
					cucapTicket)
			}
			if ok && reason != "" {
				t.Errorf("#%s: fixtureWorthy() promoted but named reason %q; nothing reads it",
					cucapTicket, reason)
			}
		})
	}
}

// TestCucapRedactRecord_RewritesTheClaudeAuthoredLeafKeys is the sole red for a
// redaction pass that visits values and not KEYS.
//
// This is the departure from the sibling family's pass and the one finding the
// security review turned up: every other string this pass rewrites sits in a slot the
// rig named, but a leaf path is built from claude's OWN JSON object keys, and this
// response is documented to describe MEMORY FILES, which live under the operator's
// home. A map claude keys by file path would otherwise put that path into a committed
// public artifact as a key.
//
// The synthetic path constants are the redaction family's own, so this row depends on
// no environment and passes identically on an operator machine and in CI.
func TestCucapRedactRecord_RewritesTheClaudeAuthoredLeafKeys(t *testing.T) {
	t.Parallel()

	// Built by hand from the synthetic constants rather than through
	// newDropcapRedactor, which reads realHome and os.TempDir: those two make a row's
	// verdict depend on whose machine ran it.
	red := &dropcapRedactor{counts: map[string]int{}}
	red.addPathClass(dropcapClassOperatorHome, "$HOME", initControlOperatorHomeValue)
	red.resort()

	leaked := initControlOperatorHomeValue + "/.claude/CLAUDE.md"
	rec := &cucapRecord{
		Arms: []cucapArmRecord{{
			Arm: "summary",
			NumericLeaves: map[string]string{
				"memory_files." + leaked: "412",
				"usage.total_tokens":     "1234",
			},
			ClaudeTotalSource: "usage.total_tokens",
		}},
		StderrCapture:   "shim log under " + leaked,
		WindowsObserved: map[string]int{"m": 200_000},
	}

	census := cucapRedactRecord(red, rec)

	// The subject: no key may still carry the operator path.
	for path := range rec.Arms[0].NumericLeaves {
		if strings.Contains(path, initControlOperatorHomeValue) {
			t.Errorf("#%s: the leaf KEY %q still carries the operator home after the pass. Claude "+
				"chooses these keys — this response describes memory files, which live under the "+
				"operator's home — so a pass that rewrites values and not keys commits the path "+
				"under a field nobody thought to check", cucapTicket, path)
		}
	}
	// The vacuity control, and the sole red for a pass that simply emptied the map:
	// the surviving keys must still be the same two leaves, with their values intact.
	if len(rec.Arms[0].NumericLeaves) != 2 {
		t.Errorf("#%s: the leaf map holds %d entr(ies) after the pass, want 2; a pass that drops "+
			"leaves satisfies the subject above by deleting the evidence",
			cucapTicket, len(rec.Arms[0].NumericLeaves))
	}
	if rec.Arms[0].NumericLeaves["usage.total_tokens"] != "1234" {
		t.Errorf("#%s: the path-free leaf's value is %q, want %q; only the paths were meant to move",
			cucapTicket, rec.Arms[0].NumericLeaves["usage.total_tokens"], "1234")
	}
	if strings.Contains(rec.StderrCapture, initControlOperatorHomeValue) {
		t.Errorf("#%s: stderr_capture still carries the operator home; the pass visits by FIELD, so "+
			"a field it does not name escapes it silently", cucapTicket)
	}
	// AN EMPTY CENSUS IS NOT AN ABSENT ONE, and a non-empty one here is what says the
	// table actually fired rather than that the paths were never there.
	if len(census) == 0 {
		t.Errorf("#%s: the census is empty although the record carried the operator home twice; a "+
			"census that reports nothing over a record that needed rewriting is an audit trail that "+
			"lies", cucapTicket)
	}
}

// TestCucapScanRecord_RefusesAPlantedOperatorPath is AC 4's fail-closed net: a record
// carrying a planted value of an armed class is refused rather than written.
//
// IT EXERCISES THE SCANNER OVER THE MARSHALLED RECORD, NEVER cucapScanRecord's OWN
// FATAL — the sibling family's resolution for the identical shape. A t.Fatalf from
// that step takes the calling subtest down with it, there is no fake testing.TB in
// this package and testing.TB cannot be implemented outside testing, so nothing can
// assert on a refusal from inside one. The assertion is still on the DECIDING VALUE
// rather than a proxy: cucapScanRecord refuses iff len(hits) > 0.
//
// WHAT THIS DOES NOT PROVE: that cucapScanRecord CALLS scan. That is construction —
// the step is the sole producer of the write's bytes and the scan is inside it — and
// it is the one untestable link in the chain.
//
// The messages name class constants and the hits slice. They never print the planted
// value, the blob or the record, so the live path has no pattern here to inherit.
func TestCucapScanRecord_RefusesAPlantedOperatorPath(t *testing.T) {
	t.Parallel()

	// The FIXED half of the net only: it reads no environment, so its five needles are
	// armed identically on every machine and no row's verdict depends on whose it is.
	scanner := dropcapScanner{needles: dropcapFixedNeedles()}

	clean, err := json.MarshalIndent(cucapWorthyRecord(), "", "  ")
	if err != nil {
		t.Fatalf("#%s: marshal the unplanted record: %v", cucapTicket, err)
	}
	cleanHits, notApplied := scanner.scan(clean)

	// The vacuity controls, and they must Fatalf rather than skip: a property that
	// cannot discriminate is a broken instrument, not a passing test. Two distinct
	// failures, so two messages.
	if len(cleanHits) != 0 {
		t.Fatalf("#%s: the UNPLANTED record already hits %d class(es) %v, so the planted assertion "+
			"below cannot tell \"the plant was seen\" from \"this record always hits\"",
			cucapTicket, len(cleanHits), cleanHits)
	}
	if len(notApplied) != 0 {
		t.Fatalf("#%s: the offline scanner reports %d class(es) NOT APPLIED %v; every fixed needle is "+
			"exempt from the dropcapMinNeedle minimum by construction, so a skipped one means a fixed "+
			"needle was marked dynamic and the planted assertion below goes green-and-vacuous",
			cucapTicket, len(notApplied), notApplied)
	}

	// A FIXED deny class (/Users/), armed on every scanner and exempt from
	// dropcapMinNeedle by construction — a dynamic needle shorter than that minimum
	// arms nothing, so a row planting one is green and proves nothing.
	const planted = "/Users/synthetic-operator/Library/Logs/shim.log"

	// One plant site per row is enough, and structurally rather than as a budget: scan
	// reads the WHOLE marshalled blob, so field coverage comes for free. The redaction
	// pass is the opposite case — it visits fields BY NAME, which is why the row above
	// plants per field.
	t.Run("in the leaf map's claude-authored KEY", func(t *testing.T) {
		t.Parallel()
		rec := cucapWorthyRecord()
		rec.Arms[0].NumericLeaves = map[string]string{"memory_files." + planted: "412"}
		blob, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			t.Fatalf("#%s: marshal the planted record: %v", cucapTicket, err)
		}
		hits, _ := scanner.scan(blob)
		if !dropcapContains(hits, dropcapDenyUsers) {
			t.Errorf("#%s: hits = %v, want %q among them. An operator path planted in a leaf KEY "+
				"survived the deny-scan, so the record would be committed with it and the net behind "+
				"the redaction table is decorative for exactly the field claude controls",
				cucapTicket, hits, dropcapDenyUsers)
		}
	})

	t.Run("in stderr_capture", func(t *testing.T) {
		t.Parallel()
		rec := cucapWorthyRecord()
		// At the FRONT and well inside the cap, so the cap cannot move it into a
		// truncated tail.
		rec.StderrCapture = planted + ": child stderr the redaction table did not predict"
		blob, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			t.Fatalf("#%s: marshal the planted record: %v", cucapTicket, err)
		}
		hits, _ := scanner.scan(blob)
		if !dropcapContains(hits, dropcapDenyUsers) {
			t.Errorf("#%s: hits = %v, want %q among them", cucapTicket, hits, dropcapDenyUsers)
		}
	})

	t.Run("in the daemon read error, which wraps the full transcript path", func(t *testing.T) {
		t.Parallel()
		rec := cucapWorthyRecord()
		// Not hypothetical: contextwindow.Read's error wraps the path it failed to
		// open, and that path is under the pinned $HOME.
		rec.DaemonReadError = "contextwindow: open transcript: open " + planted + ": no such file"
		blob, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			t.Fatalf("#%s: marshal the planted record: %v", cucapTicket, err)
		}
		hits, _ := scanner.scan(blob)
		if !dropcapContains(hits, dropcapDenyUsers) {
			t.Errorf("#%s: hits = %v, want %q among them", cucapTicket, hits, dropcapDenyUsers)
		}
	})
}

// TestCucapScanApplied_RecordsAnArmedNothingClassForAnAbsentPath: with no artifact
// directory at the fill site, artifact_dir is PRESENT in the recorded map as
// armed-nothing rather than missing from it.
//
// It spawns nothing and reads nothing off disk. It lives in THIS file — the exec-ing
// one — because this is the fill site's file and this file correctly carries no
// finOfflineExecBans entry: a test calling newDropcapScanner from a banned file would
// leave that ban green while reading the environment one hop away.
//
// WHAT IT MUST NOT ASSERT, and why: three of the map's classes are
// environment-dependent. Both credential classes read os.Getenv and operator_home
// reads realHome, so those three flip between an operator machine and CI. Every
// assertion here is over a class fixed by a caller-passed value.
//
// FAILURE MESSAGES PRINT THE MAP, NEVER THE SCANNER. This test's scanner holds the
// operator's two live credentials exactly as the capture's does; the map's keys are
// declared vocabulary and its values are bools, so it is both safe and the diagnostic
// worth having.
func TestCucapScanApplied_RecordsAnArmedNothingClassForAnAbsentPath(t *testing.T) {
	t.Parallel()

	// Exactly the fill site's construction shape, with the middle parameter —
	// artifactDir — empty. The synthetic path constants are comfortably longer than
	// dropcapMinNeedle and dropcapPathSpellings only ATTEMPTS filepath.EvalSymlinks,
	// so a non-existent synthetic path is deterministic and touches no filesystem
	// state.
	scanner := newDropcapScanner(initControlTempHomeValue, "", initControlWorkdirValue)

	// The precheck, and it is this row's headline fact rather than a formality. If the
	// raw map already carried the class, the completion would be dead weight and the
	// subject below would pass for the wrong reason.
	raw := scanner.applied()
	if _, ok := raw[dropcapClassArtifactDir]; ok {
		t.Fatalf("#%s: newDropcapScanner with an EMPTY artifactDir already reports %q in applied() "+
			"(map %v), so addDynamicPath started appending a needle for an empty path. "+
			"cucapScanApplied is then dead weight and the assertions below settle nothing",
			cucapTicket, dropcapClassArtifactDir, raw)
	}

	got := cucapScanApplied(scanner)

	// Two checks and two messages: present-but-true and absent-entirely are different
	// defects, and a single combined assertion cannot say which happened.
	armed, ok := got[dropcapClassArtifactDir]
	if !ok {
		t.Fatalf("#%s: the recorded map omits %q entirely (got %v), so an artifact directory the run "+
			"never had reads exactly like a class nobody ever armed — which is the one outcome this "+
			"field exists to prevent", cucapTicket, dropcapClassArtifactDir, got)
	}
	if armed {
		t.Errorf("#%s: the recorded map reports %q as ARMED (got %v) although the construction hands "+
			"newDropcapScanner no artifact directory; a committed capture would then claim a needle "+
			"ran when none was ever built", cucapTicket, dropcapClassArtifactDir, got)
	}

	// The vacuity control, and the SOLE RED for a completion that assigns false
	// unconditionally — which would satisfy the subject above while destroying the
	// field's whole meaning.
	if !got[dropcapClassWorkdir] {
		t.Errorf("#%s: %q was handed a non-empty path and still reads armed-nothing (got %v), so the "+
			"completion overwrites entries instead of only adding MISSING ones, and every armed class "+
			"in a committed capture is reported as having armed nothing",
			cucapTicket, dropcapClassWorkdir, got)
	}
}
