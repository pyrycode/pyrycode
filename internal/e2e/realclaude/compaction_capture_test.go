//go:build e2e_realclaude

package realclaude

// Evidence capture for #2229 — every stream-json line ONE live `/compact` turn
// puts on this surface, whether or not it looks like compaction.
//
// # What is unknown, and why that shapes the filter
//
// The `compacting` wire frame is fully built and has no producer. What claude
// sends is on record only as an SDK type declaration: a `system/status` line with
// status "compacting" then null (carrying compact_result/compact_error), and a
// `system/compact_boundary` line carrying compact_metadata. WHICH SEAM THOSE
// ARRIVE ON IS THE OPEN QUESTION. Parser.emitSystemSubtype maps `system` per
// subtype and an unmatched one is silently dropped, so a mapper planned before
// the bytes land is planned against a coin flip.
//
// CORRECTED 2026-09-08 (#2232), two errors in one sentence, and the correction
// leaves the historical point above standing. It carried a COUNT of that switch's
// arms — the arms are the one enumeration of the mapped set, no comment should
// restate the count, and this correction adds no replacement. And "falls through
// to emitUnrecognized" was never true of a `system` line: the match sits INSIDE
// consumeLine's ignoredLineTypes branch, so an unmatched subtype reaches that
// branch's silent debug drop and the surfaced tier stays structurally unreachable.
//
// So the record keeps EVERY line of the turn (AC 1) and marks the compaction ones
// by CONTENT rather than by envelope — see ccapMarkers. Filtering on a type or a
// subtype would presuppose the answer this capture exists to produce.
//
// Marking on marker KEYS rather than on the stem `compact` is also what keeps the
// `init` line out of the count: this package's committed testdata holds 114
// occurrences of that stem and every one is a slash-command NAME inside an init
// line's slash_commands inventory. TestCcapMarkersIgnoreTheSlashCommandInventory
// is that guard, offline.
//
// # Staging
//
// Two priming turns then `/compact`, on one child. `/compact` on an empty
// conversation is the likeliest way a no-compaction run happens for a RIG reason
// rather than a finding, so staged_context_tokens measures what was actually in
// context and ccapRecord.stagingVerdict names which reading a zero-hit run is.
//
// A slash command sent as ordinary message text IS honoured on this input path —
// #2138 drives the literal `/clear` through this runner and a real claude
// announces a conversation_reset. That precedent carries its own hazard in its
// header: whether such a turn ever CLOSES is unmeasured, "a slash command
// replying with nothing is a plausible shape". So the compact turn does not block
// on a `result`; ccapAwaitCompactTurn also ends on quiescence or on the budget,
// and every arm still lands the census.
//
// # What is deliberately NOT inherited
//
// #2089's tpcapHoldFIFO. It exists to hold a FOREGROUND Bash call open across a
// 30 s heartbeat tick; nothing here holds anything open. Two of that ticket's
// three repair legs were FIFO repairs, one of which hung a 20-minute gate run
// inside a sync.Once cleanup. Building it here would import the cost without the
// reason.
//
// Everything else is reused: dropcapRecorder, dropcapRedactor, dropcapScanner,
// dropcapMakeEntry, dropcapWaitForChild and parseOne all live in
// dropped_line_capture_test.go.
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine, and nothing else — the gate
// is the FIXTURE'S ABSENCE, argued at TestRealClaude_CompactionCapture. To force
// a re-capture at a new claude version, over an existing fixture:
//
//	PYRY_PROBE_COMPACTION_CAPTURE=1 go test -tags e2e_realclaude -timeout 20m -v \
//	  -run '^TestRealClaude_CompactionCapture$' ./internal/e2e/realclaude/
//
// Read WHICH skip: "fixture already exists" is the steady state, while a skip out
// of WithWorktreeAuthenticated means the machine has no claude login and the
// evidence was not produced.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// ccapEnableEnv FORCES a re-capture when the fixture already exists. It is not
// the gate — see the gate comment in TestRealClaude_CompactionCapture.
const ccapEnableEnv = "PYRY_PROBE_COMPACTION_CAPTURE"

// The version is spliced into the path rather than repeated, so the filename
// cannot drift from the release the record vouches for. The streamsup-side reader
// pins the same version from the other end and fixtureWorthy refuses to write
// under a mismatched name, so a claude upgrade is a loud instruction to re-capture
// rather than a fixture quietly describing another release.
const (
	ccapFixtureVersion = "2.1.259"
	ccapFixturePath    = "testdata/compaction_v" + ccapFixtureVersion + ".json"
)

// Every file-local identifier takes the ccap prefix, for the reason #1260's
// header gives: siblings add files to this package concurrently and a
// branch-overlap check does not catch a same-package identifier collision.
const (
	ccapTicket         = "2229"
	ccapWorkdirName    = "ccap-work"
	ccapRecordName     = "ccap-record.json"
	ccapArtifactPrefix = "pyry-2229-capture-*"
	ccapModel          = "haiku"
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from the
	// sibling probes' so a record can never be mistaken for one of theirs. It is
	// also the id claude echoes back, which is what makes dropcapRedactor's
	// session_id class able to catch it.
	ccapSessionID = "3f6a1d80-95c4-4e17-b2d8-7c0e5a9f4b13"
	// The priming range. Long enough to put real tokens in context, short enough
	// that two haiku turns stay cheap; the record measures what actually landed
	// rather than assuming this worked.
	ccapPrimeSpan = 300
)

const (
	ccapPrimeTurns    = 2
	ccapPrimeBudget   = 4 * time.Minute
	ccapCompactBudget = 4 * time.Minute
	ccapRunExitWait   = 30 * time.Second
	ccapPoll          = 500 * time.Millisecond
	// How long the stream must stay silent, after at least one line of the compact
	// turn has arrived, before the turn counts as over. This is the arm that exists
	// because a slash-command turn may never emit a `result` at all.
	ccapQuiet = 30 * time.Second
)

const (
	ccapFired            = "fired"
	ccapDidNotFire       = "did-not-fire"
	ccapInstrumentBroken = "instrument-broken"
)

const (
	ccapTerminatedResult = "result"
	ccapTerminatedQuiet  = "quiescence"
	ccapTerminatedBudget = "budget"
)

var ccapArgs = []string{"--model", ccapModel, "--dangerously-skip-permissions"}

const ccapSpawnShapeDelta = "The YOLO interactive shape, identical to #1260's and #2089's — see " +
	"dropcapSpawnShapeDelta for what production's non-yolo spawn adds and what that implies for " +
	"system/init. It is kept rather than narrowed because a differing spawn shape would be a confound " +
	"in the evidence. Nothing about compaction is known to depend on the approval flags; that is " +
	"UNMEASURED, not ruled out."

const ccapLimitations = "One conversation, one spawn shape, one claude version, one model (" + ccapModel +
	"), and compaction provoked EXPLICITLY by `/compact` rather than by reaching a context threshold. " +
	"Auto-compaction's lines are UNMEASURED and may not be the same set: nothing here stages a context " +
	"large enough to trip it. Cross-version and cross-model stability are UNMEASURED. A shape absent " +
	"from compaction_shapes did not appear in THIS turn, which is not the same as claude never sending it."

const ccapRedactionRationale = "Inherited whole from #1260 (see dropcapRedactionRationale): a fresh empty " +
	"non-git workdir under a per-test temp $HOME, rig-authored prompts, no os.Environ() read into the " +
	"record, the declared dropcapRedactor substitution table over every string, and dropcapScanner as a " +
	"fail-closed deny-scan over the marshalled record. " +
	"WHAT THIS CAPTURE SPECIFICALLY CAN CARRY, stated rather than left to be inferred by whoever decides " +
	"to paste this record into a public issue. FIRST, a compact_result is a MODEL-AUTHORED SUMMARY OF THE " +
	"WHOLE CONVERSATION, which is a larger free-text surface than any sibling capture holds — #2089's " +
	"single content-bearing field was one tool's input. It is KEPT, because a redacted summary would not " +
	"be evidence of the shape, and the defence for it is that the conversation being summarised is two " +
	"rig-authored prompts asking for integers, plus the by-construction list above and the deny-scan. A " +
	"capture staged against a real workspace would need more. SECOND, per-message uuid, tool_use_id and " +
	"parent_tool_use_id values SURVIVE, as they do in every committed capture in this directory; the " +
	"session_id deny class covers the conversation id claude echoes back, which is the rig's own literal."

// ccapFrame is one line of the compact turn — every line, not only the compaction
// ones. The payload half comes from dropcapMakeEntry so the base64 arm for invalid
// UTF-8 is shared rather than re-derived.
//
// events_emitted is the SHIPPED parser's verdict (parseOne), carried as DATA
// rather than as a filter: a non-zero count on a compaction line is that line
// reaching the unrecognized lane and putting a row in the operator's chat today.
type ccapFrame struct {
	Index                   int      `json:"index"`
	Type                    string   `json:"type"`
	Subtype                 string   `json:"subtype,omitempty"`
	Compaction              bool     `json:"compaction"`
	Markers                 []string `json:"markers,omitempty"`
	PayloadLenBytesCaptured int      `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int      `json:"payload_len_bytes"`
	PayloadEncoding         string   `json:"payload_encoding"`
	Payload                 string   `json:"payload,omitempty"`
	PayloadB64              string   `json:"payload_b64,omitempty"`
	EventsEmitted           int      `json:"events_emitted"`
}

type ccapRecord struct {
	Ticket          string   `json:"ticket"`
	ClaudeVersion   string   `json:"claude_version"`
	CapturedAt      string   `json:"captured_at"`
	IsCapture       bool     `json:"is_capture"`
	Model           string   `json:"model"`
	SpawnShape      []string `json:"spawn_shape"`
	SpawnShapeDelta string   `json:"spawn_shape_delta"`
	Workdir         string   `json:"workdir"`
	Prompts         []string `json:"prompts"`

	Outcome       string `json:"outcome"`
	OutcomeDetail string `json:"outcome_detail"`
	TerminatedOn  string `json:"terminated_on"`

	// The staging measurement. Without it a zero-hit record cannot tell "claude
	// declined to compact" from "the rig never staged a compactable conversation",
	// and only the first is a finding — AC 5.
	PrimingTurnsCompleted int     `json:"priming_turns_completed"`
	PrimingAssistantLines int     `json:"priming_assistant_lines"`
	StagedContextTokens   int     `json:"staged_context_tokens"`
	CompactTurnSent       bool    `json:"compact_turn_sent"`
	CompactTurnSeconds    float64 `json:"compact_turn_seconds"`

	// Content-free censuses: the priming half by count only, the compact turn's own
	// types beside the frames that carry them.
	PreCompactLineCensus map[string]int `json:"pre_compact_line_census"`
	LineTypeCensus       map[string]int `json:"line_type_census"`
	ToolCalls            []string       `json:"tool_calls"`
	UndecodedLines       int            `json:"undecoded_lines"`

	LinesCaptured       int            `json:"lines_captured"`
	Frames              []ccapFrame    `json:"frames"`
	CompactionLineCount int            `json:"compaction_line_count"`
	CompactionShapes    []string       `json:"compaction_shapes"`
	MarkerCensus        map[string]int `json:"marker_census"`

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

func (rec *ccapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
}

// fixtureWorthy answers whether this record may be promoted to ccapFixturePath,
// and names the reason when it may not.
//
// Every rejection is a case where the record is still valuable EVIDENCE — it is
// written to the artifact dir either way — but would be a lie as the committed
// proof. The version arm is the producing half of the pin the streamsup reader
// enforces; `claude --version` prints "<version> (Claude Code)", so the comparison
// is on the leading token, and an "<unavailable: ...>" fails it too.
func (rec *ccapRecord) fixtureWorthy() (string, bool) {
	if rec.Outcome != ccapFired {
		return fmt.Sprintf("outcome=%s", rec.Outcome), false
	}
	if rec.CompactionLineCount == 0 {
		return "zero compaction lines — a vacuous fixture proves nothing", false
	}
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != ccapFixtureVersion {
		return fmt.Sprintf("claude_version %q is not the %s pinned in the fixture name — repin "+
			"ccapFixtureVersion and compactionCaptureVersion together, then re-run", got, ccapFixtureVersion), false
	}
	// The reader refuses any encoding but json-string, and dropcapMakeEntry emits
	// base64 with an EMPTY payload for a frame that is not valid UTF-8. Promoting
	// one would redden `make check` for every unrelated ticket. This is a refusal to
	// promote rather than a fatal: the record still holds the frame as evidence.
	for _, f := range rec.Frames {
		if f.Compaction && f.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("compaction frame %d is encoded %q and the reader reads only %q — a "+
				"non-UTF-8 frame carries no readable payload, so a fixture holding one would fail the "+
				"assertion it exists to feed", f.Index, f.PayloadEncoding, dropcapEncodingJSONString), false
		}
	}
	return "", true
}

// stagingVerdict names WHICH reading a zero-compaction run is — AC 5.
//
// Only the last case is evidence about claude. The first two are the rig failing
// to stage the thing it meant to measure, and saying so plainly is what stops the
// next reader from loosening the marker set to fix a staging bug.
func (rec *ccapRecord) stagingVerdict() string {
	switch {
	case !rec.CompactTurnSent:
		return "the `/compact` turn never went out, so the surface was never exercised. This is an " +
			"INSTRUMENT fault: read outcome_detail, not the marker set"
	case rec.StagedContextTokens == 0 || rec.PrimingAssistantLines == 0:
		return fmt.Sprintf("the rig never staged a compactable conversation: %d priming turn(s) "+
			"completed, %d assistant line(s), %d tokens measured in context. `/compact` had nothing to "+
			"work on, so its silence says nothing about claude — read pre_compact_line_census and "+
			"tool_calls to see what the priming turns actually did",
			rec.PrimingTurnsCompleted, rec.PrimingAssistantLines, rec.StagedContextTokens)
	default:
		return fmt.Sprintf("claude DECLINED to compact: the rig staged %d tokens across %d priming "+
			"turn(s), sent `/compact`, and the turn ended on %s having produced %d line(s) and no "+
			"compaction marker. The staging WORKED, so this is a finding about claude on this input "+
			"path — most likely that an explicit `/compact` is refused below some context threshold — "+
			"and it is to be routed back, not fixed by loosening the marker set",
			rec.StagedContextTokens, rec.PrimingTurnsCompleted, rec.TerminatedOn, len(rec.Frames))
	}
}

// --- classification ----------------------------------------------------------

// ccapMarkerKeys are JSON KEYS whose presence at any depth marks a compaction
// line, spelled as literals rather than imported from anywhere: the record is
// EVIDENCE about claude's wire shape, and a census keyed on production constants
// would agree with them by construction instead of measuring anything.
var ccapMarkerKeys = []string{
	"compact_boundary", "compact_metadata", "compact_result", "compact_error",
	"pre_tokens", "post_tokens",
}

// ccapMarkerValues are key/value pairs, for the shapes that announce themselves in
// a VALUE. They match only where the named key maps DIRECTLY to the string, which
// is what keeps an init line's slash_commands inventory — an array of bare strings
// including "compact" — out of the count.
var ccapMarkerValues = []struct{ key, value string }{
	{"status", "compacting"},
	{"subtype", "compact_boundary"},
	{"type", "compact_boundary"},
}

// ccapMarkers returns the sorted set of compaction markers one line carries; empty
// means the line is not a compaction line. A value-rule marker is spelled
// "key=value" so a reader of the census can tell it from a key-rule one.
func ccapMarkers(raw []byte) []string {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	seen := map[string]bool{}
	ccapWalkJSON(doc, func(marker string) { seen[marker] = true })
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func ccapWalkJSON(v any, hit func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			for _, mk := range ccapMarkerKeys {
				if k == mk {
					hit(mk)
				}
			}
			if s, ok := val.(string); ok {
				for _, r := range ccapMarkerValues {
					if k == r.key && s == r.value {
						hit(r.key + "=" + r.value)
					}
				}
			}
			ccapWalkJSON(val, hit)
		}
	case []any:
		for _, e := range t {
			ccapWalkJSON(e, hit)
		}
	}
}

// ccapShape spells one line's envelope the way the reader pins it: the top-level
// type alone when there is no subtype, "type/subtype" when there is. That
// distinction IS the measurement — AC 2 asks for the subtype a compaction line
// carried "or the absence of one".
func ccapShape(typ, subtype string) string {
	if subtype == "" {
		return typ
	}
	return typ + "/" + subtype
}

// ccapContextTokens reads how much context one assistant line reports, which is
// the concrete measure of whether the priming turns staged anything worth
// compacting. Cached input counts: they are context, and on a short conversation
// they are most of it.
func ccapContextTokens(raw []byte) int {
	var m struct {
		Message struct {
			Usage struct {
				Input         int `json:"input_tokens"`
				CacheCreation int `json:"cache_creation_input_tokens"`
				CacheRead     int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0
	}
	u := m.Message.Usage
	return u.Input + u.CacheCreation + u.CacheRead
}

// ccapCensus counts what was on the wire, content-free: top-level types with their
// system subtypes, and the tool NAMES claude chose. Names are a closed vendor set
// rather than claude's prose and still go through the redactor, because an MCP
// tool's name embeds its server's. Tool INPUTS are deliberately not collected —
// all three prompts forbid tool use, so the NAMES alone answer the only question
// worth asking, which is whether claude ran one anyway.
func ccapCensus(lines []dropcapCaptured, red *dropcapRedactor) (types map[string]int, tools []string, undecoded int) {
	types = map[string]int{}
	tools = []string{}
	for _, c := range lines {
		if !c.Decoded {
			undecoded++
			continue
		}
		types[ccapShape(c.Type, c.Subtype)]++
		var msg struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		// content is an array on assistant/user messages and a plain string on some
		// user turns, so it is taken as RawMessage and array-decoded separately: a
		// string content is a skip, not an error worth recording.
		if err := json.Unmarshal(c.Raw, &msg); err != nil || len(msg.Message.Content) == 0 {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(msg.Message.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" {
				tools = append(tools, red.str(b.Name))
			}
		}
	}
	return types, tools, undecoded
}

// --- turn driving ------------------------------------------------------------

func ccapResultCount(lines []dropcapCaptured) int {
	n := 0
	for _, c := range lines {
		if c.Decoded && c.Type == "result" {
			n++
		}
	}
	return n
}

// ccapAwaitResults polls for the Nth `result` line. dropcapRecorder's resultSeen
// channel closes ONCE, on the first one, so it cannot mark a boundary in a
// three-turn conversation; polling snapshot() is what generalises it without
// touching the shared recorder.
func ccapAwaitResults(recorder *dropcapRecorder, want int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		lines, _ := recorder.snapshot()
		if ccapResultCount(lines) >= want {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(ccapPoll)
	}
}

// ccapAwaitCompactTurn waits out the `/compact` turn WITHOUT depending on it
// closing. #2138's header is explicit that whether a slash-command turn ever
// closes on its own is unmeasured and that a command replying with nothing is a
// plausible shape; blocking on a `result` would hang on exactly the case under
// test and land no census at all.
//
// sentAt is the captured-line count at the moment the turn went out, so the
// quiescence arm can require that the turn produced SOMETHING before calling the
// stream quiet — otherwise a turn claude has not started answering yet reads as
// one that has finished.
//
// quiet and budget are parameters rather than the constants directly so the three
// exits can be proved offline in milliseconds; the live call site passes ccapQuiet
// and ccapCompactBudget.
func ccapAwaitCompactTurn(recorder *dropcapRecorder, wantResults, sentAt int, quiet, budget time.Duration) string {
	deadline := time.Now().Add(budget)
	lastGrowth := time.Now()
	last := sentAt
	for {
		lines, _ := recorder.snapshot()
		if ccapResultCount(lines) >= wantResults {
			return ccapTerminatedResult
		}
		if len(lines) != last {
			last = len(lines)
			lastGrowth = time.Now()
		}
		if len(lines) > sentAt && time.Since(lastGrowth) >= quiet {
			return ccapTerminatedQuiet
		}
		if time.Now().After(deadline) {
			return ccapTerminatedBudget
		}
		time.Sleep(ccapPoll)
	}
}

// ccapPrimePrompt stages context worth compacting. Integers because the output is
// long, deterministic and entirely rig-authored — a compact_result summarising
// THIS conversation summarises nothing but the rig, which is what makes keeping
// the summary in the record defensible. The nonce gives dropcapRedactor's
// prompt_nonce class something to substitute; the tool ban is what keeps the YOLO
// spawn shape's blast radius at zero.
func ccapPrimePrompt(from, to int, nonce int64) string {
	return fmt.Sprintf("Write the integers from %d to %d, one per line, and nothing else. Do not use "+
		"any tools, do not comment on the task, and do nothing else. run=%d", from, to, nonce)
}

// ccapCompactPrompt is the bare command, with no nonce and no surrounding prose:
// #2138 establishes that a message whose text is exactly the slash command is what
// a real claude runs as one.
const ccapCompactPrompt = "/compact"

// TestRealClaude_CompactionCapture drives the conversation and writes the record.
//
// Ordering is load-bearing in one place: newDropcapScanner reads
// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY via os.Getenv AS DENY NEEDLES, and
// WithWorktreeAuthenticated is what re-pins them into this process. Building the
// scanner first yields an EMPTY needle, which dropcapScanner.scan reports as
// notApplied — silently skipped, not fatal. The credential net would be off while
// every message still read green, so the scanner is built after the auth helper and
// the skipped classes ship in the record.
func TestRealClaude_CompactionCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE, and that is a deliberate break from the
	// env-gated one-off probes in this package. #2089's header argues it in full and
	// the reasoning is identical here: `make e2e-realclaude` never sets a custom
	// PYRY_PROBE_* variable, so an env gate skips on the ENV check BEFORE the
	// credential check, the live gate passes vacuously, and the fixture never lands.
	// That is CLAUDE.md § Testing's #1763 failure exactly — a green gate and a spent
	// budget look identical whether the bytes landed or not.
	force := os.Getenv(ccapEnableEnv) == "1"
	if _, err := os.Stat(ccapFixturePath); err == nil && !force {
		t.Skipf("#2229 compaction capture: the fixture %s already exists, so there is nothing to "+
			"capture and this costs no claude turn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 20m -v \\\n"+
			"    -run '^TestRealClaude_CompactionCapture$' ./internal/e2e/realclaude/",
			ccapFixturePath, ccapEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Deliberately NOT t.TempDir(): the operator needs the record after the test
	// ends, and #2089's fixture survived its gate's worktree removal only because
	// the record was written outside it.
	artifactDir, err := os.MkdirTemp("", ccapArtifactPrefix)
	if err != nil {
		t.Fatalf("#2229: create artifact dir: %v", err)
	}

	// A fresh EMPTY directory, deliberately not a git repo: no branch names and no
	// file contents can reach a payload — or a compact_result.
	workdir := filepath.Join(home, ccapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2229: create workdir: %v", err)
	}
	nonce := time.Now().UnixNano()

	// The empty slot is fifoPath, and the emptiness is a fact this probe records
	// rather than an omission: it holds no FIFO. dropcapRedactor.add guards "" —
	// strings.ReplaceAll(s, "", x) would otherwise insert x between every character.
	red := newDropcapRedactor(home, artifactDir, workdir, "", ccapSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2229 capture artifacts: %s", red.str(artifactDir))

	rec := &ccapRecord{
		Ticket:                ccapTicket,
		ClaudeVersion:         probeClaudeVersion(claudeBin),
		CapturedAt:            time.Now().Format(time.RFC3339),
		IsCapture:             true,
		Model:                 ccapModel,
		SpawnShapeDelta:       ccapSpawnShapeDelta,
		Workdir:               red.str(workdir),
		Prompts:               []string{},
		Frames:                []ccapFrame{},
		CompactionShapes:      []string{},
		MarkerCensus:          map[string]int{},
		RedactionRationale:    ccapRedactionRationale,
		CredentialScanApplied: scanner.applied(),
		CredentialScanSkipped: []string{},
		Limitations:           ccapLimitations,
	}
	rec.set(ccapInstrumentBroken, "did not reach a classification point")

	// Registered before anything below can fail, so a structural t.Fatalf still
	// leaves the evidence on disk — #1260's ordering.
	t.Cleanup(func() { ccapWriteRecord(t, artifactDir, red, scanner, rec) })

	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: ccapSessionID,
		Args:      ccapArgs,
		Stdout:    recorder,
		Logger:    slog.New(argvHandler),
	})
	if err != nil {
		rec.set(ccapInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
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
		case <-time.After(ccapRunExitWait):
			t.Errorf("#2229: streamsup.Run did not return within %s of cancel", ccapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(ccapInstrumentBroken, "no live child within %s: claude never spawned, so nothing was "+
			"on the wire to capture", dropcapSpawnWait)
		return
	}

	// --- staging: prime the conversation ---------------------------------------
	for turn := 0; turn < ccapPrimeTurns; turn++ {
		prompt := ccapPrimePrompt(turn*ccapPrimeSpan+1, (turn+1)*ccapPrimeSpan, nonce)
		rec.Prompts = append(rec.Prompts, red.str(prompt))
		if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
			rec.set(ccapInstrumentBroken, "writing priming turn %d failed: %v", turn+1, red.str(err.Error()))
			return
		}
		if !ccapAwaitResults(recorder, turn+1, ccapPrimeBudget) {
			break
		}
		rec.PrimingTurnsCompleted = turn + 1
	}

	// Measure what the priming actually put in context BEFORE the compact turn adds
	// to it. The maximum over the priming assistant lines is the conversation's high
	// water mark, which is what `/compact` would be acting on.
	primed, _ := recorder.snapshot()
	for _, c := range primed {
		if !c.Decoded || c.Type != "assistant" {
			continue
		}
		rec.PrimingAssistantLines++
		if n := ccapContextTokens(c.Raw); n > rec.StagedContextTokens {
			rec.StagedContextTokens = n
		}
	}
	rec.PreCompactLineCensus, rec.ToolCalls, rec.UndecodedLines = ccapCensus(primed, red)
	primedLines := len(primed)

	// --- the compact turn ------------------------------------------------------
	rec.Prompts = append(rec.Prompts, ccapCompactPrompt)
	turnStart := time.Now()
	if err := streamsup.WriteTurn(ctx, stdin, []byte(ccapCompactPrompt)); err != nil {
		rec.set(ccapInstrumentBroken, "writing the `/compact` turn failed, so the surface was never "+
			"exercised: %v", red.str(err.Error()))
		return
	}
	rec.CompactTurnSent = true
	rec.TerminatedOn = ccapAwaitCompactTurn(recorder, rec.PrimingTurnsCompleted+1, primedLines,
		ccapQuiet, ccapCompactBudget)
	rec.CompactTurnSeconds = time.Since(turnStart).Seconds()

	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	// AC 1: every line of the turn, not only the ones matching a compaction
	// pattern. primedLines is the cut, so the fixture holds the compact turn whole
	// while the priming half stays a count in pre_compact_line_census.
	turnLines := lines
	if primedLines <= len(lines) {
		turnLines = lines[primedLines:]
	}
	rec.LineTypeCensus, _, _ = ccapCensus(turnLines, red)
	rec.Frames = ccapCollect(t, turnLines, red)

	shapes := map[string]bool{}
	for _, f := range rec.Frames {
		if !f.Compaction {
			continue
		}
		rec.CompactionLineCount++
		shapes[ccapShape(f.Type, f.Subtype)] = true
		for _, m := range f.Markers {
			rec.MarkerCensus[m]++
		}
	}
	for s := range shapes {
		rec.CompactionShapes = append(rec.CompactionShapes, s)
	}
	sort.Strings(rec.CompactionShapes)

	if rec.CompactionLineCount == 0 {
		rec.set(ccapDidNotFire, "the `/compact` turn produced no compaction line at all; %s", rec.stagingVerdict())
	} else {
		rec.set(ccapFired, "%d compaction line(s) across shapes %v, markers %v",
			rec.CompactionLineCount, rec.CompactionShapes, rec.MarkerCensus)
	}

	// --- AC 5 -------------------------------------------------------------------
	// Counts and indices only. The frames themselves are in the record the cleanup
	// has already written; putting claude's bytes in CI output is precisely the
	// exposure the deny-scan exists to prevent.
	if rec.CompactionLineCount == 0 {
		t.Fatalf("#2229: the `/compact` turn recorded ZERO compaction lines out of %d turn line(s) "+
			"(terminated_on=%s, turn=%.1fs). A capture recording none is vacuous, and committing it "+
			"would hand #2227 and #2228 a fixture that proves nothing.\n"+
			"  staging: %s\n"+
			"  turn line types: %v; priming line types: %v; tools called: %v; undecoded: %d\n"+
			"Read the staging line FIRST: only its last case is a finding about claude, and the other "+
			"two are the rig failing to stage a compactable conversation",
			len(rec.Frames), rec.TerminatedOn, rec.CompactTurnSeconds, rec.stagingVerdict(),
			rec.LineTypeCensus, rec.PreCompactLineCensus, rec.ToolCalls, rec.UndecodedLines)
	}
}

// ccapCollect builds a frame for EVERY line of the turn, marking the compaction
// ones rather than filtering to them — AC 1. An undecodable line still gets a
// frame: it is part of what the turn produced, and losing it would make the record
// disagree with lines_captured for no stated reason.
func ccapCollect(t *testing.T, lines []dropcapCaptured, red *dropcapRedactor) []ccapFrame {
	t.Helper()
	out := []ccapFrame{}
	for _, c := range lines {
		// The payload half, shared with #1260 so the base64 arm for invalid UTF-8 is
		// not re-derived. The reason field is spent on the markers below, so it is
		// passed empty and dropped.
		entry := dropcapMakeEntry(c, "", red)
		markers := ccapMarkers(c.Raw)
		out = append(out, ccapFrame{
			Index:                   entry.Index,
			Type:                    entry.Type,
			Subtype:                 entry.Subtype,
			Compaction:              len(markers) > 0,
			Markers:                 markers,
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

// ccapWriteRecord marshals, deny-scans and writes. #1260's fail-closed rule
// verbatim: on a hit NOTHING is written and the message names the CLASS only,
// never the matched value.
func ccapWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *ccapRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2229: marshal record: %v", err)
		return
	}
	hits, notApplied := scanner.scan(blob)
	// A base64 payload hides its bytes from a scan of the marshalled record, so the
	// decoded bytes are scanned too.
	for i, f := range rec.Frames {
		if f.PayloadB64 == "" {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(f.PayloadB64)
		if derr != nil {
			t.Errorf("#2229: frame %d: decode base64 payload for the scan: %v", i, derr)
			return
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("#2229: deny-scan found %d denied class(es) still present in the record: %v\n"+
			"NOTHING was written — not the record, not the fixture. Extend dropcapRedactor's table with "+
			"the named class and re-run the capture. The offending value is deliberately not printed: "+
			"putting it in CI output is exactly the exposure this scan exists to prevent", len(hits), hits)
	}
	rec.CredentialScanSkipped = notApplied

	// Re-marshal so credential_scan_skipped ships in the written bytes. Its value is
	// a list of CLASS NAMES the scan could not apply, which is the one thing that
	// makes a silently-off credential net visible after the fact.
	blob, err = json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2229: re-marshal record: %v", err)
		return
	}

	path := filepath.Join(dir, ccapRecordName)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2229: write record %s: %v", red.str(path), err)
		return
	}
	t.Logf("#2229 outcome=%s terminated_on=%s turn=%.1fs primed_turns=%d primed_assistant=%d "+
		"staged_tokens=%d captured=%d turn_lines=%d compaction=%d shapes=%v markers=%v tools=%v "+
		"scan_not_applied=%v\n  record: %s\n  %s",
		rec.Outcome, rec.TerminatedOn, rec.CompactTurnSeconds, rec.PrimingTurnsCompleted,
		rec.PrimingAssistantLines, rec.StagedContextTokens, rec.LinesCaptured, len(rec.Frames),
		rec.CompactionLineCount, rec.CompactionShapes, rec.MarkerCensus, rec.ToolCalls, notApplied,
		red.str(path), red.str(rec.OutcomeDetail))

	// The same deny-scanned bytes, promoted in-repo so the run that produced them is
	// the run that lands them. A capture that still needs a human to copy a file out
	// of a tempdir is a capture #1763 says will not land.
	if reason, ok := rec.fixtureWorthy(); !ok {
		t.Logf("#2229: NOT promoted to %s — %s. The record above is the evidence; read it, then "+
			"re-run or route the finding back", ccapFixturePath, reason)
		return
	}
	if err := os.WriteFile(ccapFixturePath, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2229: write fixture %s: %v", ccapFixturePath, red.str(err.Error()))
		return
	}
	t.Logf("#2229: FIXTURE WRITTEN to %s (%d compaction line(s), shapes %v).\n"+
		"  Commit it — `git add %s` — and in the SAME commit fill compactionPinnedShapes in "+
		"internal/streamsup/compaction_capture_test.go with those shapes and delete nothing else: "+
		"that reader FATALS on a present fixture with an empty pin, which is what stops the bytes "+
		"landing unpinned. An uncommitted capture is a capture that did not happen (#1763).",
		ccapFixturePath, rec.CompactionLineCount, rec.CompactionShapes, ccapFixturePath)
}

// TestCcapMarkersIgnoreTheSlashCommandInventory runs offline and is the
// load-bearing guard on the classifier.
//
// This package's committed testdata holds 114 occurrences of the stem `compact`
// and every one of them is a slash-command NAME inside an `init` line's
// slash_commands inventory. A classifier keyed on that stem, or one that applied
// its value rules to bare array elements, would report the first line of every
// turn as a compaction line and hand #2227 and #2228 a fixture whose count is
// entirely noise. The first row is that mutant's witness.
func TestCcapMarkersIgnoreTheSlashCommandInventory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want []string
	}{
		{
			name: "an init line's slash_commands inventory is not a compaction line",
			line: `{"type":"system","subtype":"init","session_id":"s","slash_commands":` +
				`["clear","compact","compact_boundary","status","help"],"model":"haiku"}`,
		},
		{
			name: "an ordinary assistant line is not a compaction line",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"compact this"}]}}`,
		},
		{
			name: "a result line naming compaction in prose is not a compaction line",
			line: `{"type":"result","subtype":"success","result":"I compacted the status of nothing"}`,
		},
		{
			name: "status compacting, on whatever envelope it arrives",
			line: `{"type":"system","subtype":"status","status":"compacting","session_id":"s"}`,
			want: []string{"status=compacting"},
		},
		{
			name: "the ending status line announces itself by key, with status null",
			line: `{"type":"system","subtype":"status","status":null,"compact_result":"a summary",` +
				`"compact_error":null}`,
			want: []string{"compact_error", "compact_result"},
		},
		{
			name: "a compact_boundary, by subtype value and by nested metadata keys",
			line: `{"type":"system","subtype":"compact_boundary","compact_metadata":` +
				`{"trigger":"manual","pre_tokens":41000,"post_tokens":9000,"duration_ms":812}}`,
			want: []string{"compact_metadata", "post_tokens", "pre_tokens", "subtype=compact_boundary"},
		},
		{
			name: "a top-level compact_boundary type, the shape emitSystemSubtype would never see",
			line: `{"type":"compact_boundary","trigger":"auto"}`,
			want: []string{"type=compact_boundary"},
		},
		{
			name: "an undecodable line marks nothing rather than panicking",
			line: `{"type":"system",`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ccapMarkers([]byte(tc.line))
			if len(got) != len(tc.want) || (len(got) > 0 && strings.Join(got, ",") != strings.Join(tc.want, ",")) {
				t.Errorf("ccapMarkers() = %v, want %v", got, tc.want)
			}
			if (len(got) > 0) != (len(tc.want) > 0) {
				t.Errorf("compaction verdict = %v, want %v", len(got) > 0, len(tc.want) > 0)
			}
		})
	}
}

// TestCcapFixtureWorthyRefusesEveryBadCapture runs offline. fixtureWorthy is the
// only thing standing between a live run and a committed fixture, and each row
// below is a capture that looks green from outside — the record is written, the
// deny-scan passed, the log is cheerful — while proving nothing, or proving
// something about a different claude.
func TestCcapFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	good := func() *ccapRecord {
		return &ccapRecord{
			Outcome:             ccapFired,
			ClaudeVersion:       ccapFixtureVersion + " (Claude Code)",
			CompactionLineCount: 2,
			Frames: []ccapFrame{
				{Index: 0, Type: "assistant", PayloadEncoding: dropcapEncodingJSONString},
				{Index: 1, Type: "system", Subtype: "status", Compaction: true, PayloadEncoding: dropcapEncodingJSONString},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*ccapRecord)
		want   bool
	}{
		{"a good capture is promoted", func(*ccapRecord) {}, true},
		{"bare version string, no suffix", func(r *ccapRecord) { r.ClaudeVersion = ccapFixtureVersion }, true},
		{
			// A non-UTF-8 line that is NOT a compaction line costs the reader nothing,
			// so it must not block a fixture that is otherwise good.
			"a base64 non-compaction frame is not a reason to refuse",
			func(r *ccapRecord) { r.Frames[0].PayloadEncoding = dropcapEncodingBase64 },
			true,
		},
		{"never fired", func(r *ccapRecord) { r.Outcome = ccapDidNotFire }, false},
		{"instrument broken", func(r *ccapRecord) { r.Outcome = ccapInstrumentBroken }, false},
		{"vacuous: zero compaction lines", func(r *ccapRecord) { r.CompactionLineCount = 0 }, false},
		{"a different claude release", func(r *ccapRecord) { r.ClaudeVersion = "2.1.260 (Claude Code)" }, false},
		{"version unreadable", func(r *ccapRecord) { r.ClaudeVersion = "<unavailable: exec failed>" }, false},
		{"version absent", func(r *ccapRecord) { r.ClaudeVersion = "" }, false},
		{
			// The one shape this side would otherwise promote and the reading side
			// refuses: capturedCompactionFrames fatals on any encoding but json-string.
			"a base64 compaction frame the reader cannot read",
			func(r *ccapRecord) { r.Frames[1].PayloadEncoding = dropcapEncodingBase64 },
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
}

// TestCcapStagingVerdictSeparatesRigFailureFromFinding runs offline. The verdict
// string is what a reader of a failed live gate acts on, and AC 5's whole demand
// is that it names WHICH reading a zero-compaction run is. Only the last row is
// evidence about claude; confusing it with the others is how a rig bug gets
// "fixed" by loosening the marker set.
func TestCcapStagingVerdictSeparatesRigFailureFromFinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rec         ccapRecord
		wantFinding bool
	}{
		{
			name: "the compact turn never went out",
			rec:  ccapRecord{CompactTurnSent: false},
		},
		{
			name: "the priming turns staged nothing measurable",
			rec:  ccapRecord{CompactTurnSent: true, PrimingTurnsCompleted: 2, PrimingAssistantLines: 2},
		},
		{
			name: "assistant lines came back but reported no context at all",
			rec:  ccapRecord{CompactTurnSent: true, PrimingTurnsCompleted: 2, StagedContextTokens: 0},
		},
		{
			name: "the staging worked and claude produced nothing",
			rec: ccapRecord{
				CompactTurnSent: true, PrimingTurnsCompleted: 2, PrimingAssistantLines: 2,
				StagedContextTokens: 41000, TerminatedOn: ccapTerminatedResult,
			},
			wantFinding: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.rec.stagingVerdict()
			if got == "" {
				t.Fatal("stagingVerdict() is empty; the zero-compaction fatal would name no cause")
			}
			isFinding := strings.Contains(got, "finding about claude")
			if isFinding != tc.wantFinding {
				t.Errorf("stagingVerdict() reads as a finding about claude = %v, want %v\n  got: %s",
					isFinding, tc.wantFinding, got)
			}
			if !tc.wantFinding && !strings.Contains(got, "never staged") && !strings.Contains(got, "INSTRUMENT") {
				t.Errorf("a rig failure must say so; got: %s", got)
			}
		})
	}
}

// TestCcapAwaitCompactTurnDoesNotDependOnAResult runs offline against a real
// dropcapRecorder fed by hand.
//
// The quiescence row is the one that matters. #2138's header records that whether
// a slash-command turn ever closes on its own is unmeasured and that a command
// replying with nothing is a plausible shape, so a wait that only ended on a
// `result` would hang on exactly the case this capture exists to observe, and land
// no census at all. The budget row is the backstop under that.
func TestCcapAwaitCompactTurnDoesNotDependOnAResult(t *testing.T) {
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
			want:  ccapTerminatedResult,
			grace: 5 * time.Second,
		},
		{
			name:  "lines then silence ends the turn without a result",
			feed:  "{\"type\":\"system\",\"subtype\":\"status\",\"status\":\"compacting\"}\n",
			want:  ccapTerminatedQuiet,
			grace: 5 * time.Second,
		},
		{
			name:  "a turn that never produces a line ends on the budget, not on quiescence",
			feed:  "",
			want:  ccapTerminatedBudget,
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
			got := ccapAwaitCompactTurn(recorder, 1, 0, quiet, tc.grace)
			if got != tc.want {
				t.Errorf("ccapAwaitCompactTurn() = %q, want %q", got, tc.want)
			}
		})
	}
}
