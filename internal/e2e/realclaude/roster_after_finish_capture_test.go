//go:build e2e_realclaude

package realclaude

// Evidence capture for #2525 — whether claude emits system/background_tasks_changed
// in the seconds and the turn AFTER a background task completes.
//
// # The question, and why the repo cannot already answer it
//
// The daemon synthesises no finish and never diffs rosters (emitBackgroundTaskRoster's
// rule, restated for the retention in cmd/pyry's sessionBackgroundTaskHold), so a
// client's task count only comes down if claude sends a roster line that omits the
// finished task. Desktop #1246 assumes it does; desktop #1558 and mobile #677/#678
// assume it does not. Exactly one of them is building against a real line.
//
// Four prior observations, all re-read at 43a52426, and none of them settles it:
//
//   - dropped_lines_v2.1.220.json — one roster carrying one entry, in the turn that
//     BACKGROUNDED the command. That staging holds the FIFO open on purpose, so the
//     task never completed inside it.
//   - task_notification_v2.1.259.json — the turn where the command does complete.
//     task_started fires, then task_notification with status completed, and the roster
//     never fires AT ALL — not after the completion and not at the start either.
//   - initialize_control_v2.1.239*.json — the only other rosters in the repo, and
//     their position turns on WHEN the initialize ask was sent. The two arms that
//     asked MID-SESSION each show one empty roster right after the control_response.
//     The arm that asked BEFORE THE FIRST TURN shows no roster at all across two
//     complete turns — and that is the ask position production runs.
//
// # Why the staging reads through three prompts, in this order
//
// streamsup.Runner's RequestInitializeOnSpawn writes one initialize control_request
// per spawned child, BEFORE that child's first turn. That is the one ask position in
// the repo never followed by a roster, so a capture enabling only it reproduces
// exactly the arm where nothing has ever appeared — and an absence recorded there
// cannot tell "the roster only ever answers a control request" from "claude never
// sends it", which is the distinction this probe exists to make.
//
// So after the task completes the probe reads through three prompts, and the ORDER is
// load-bearing:
//
//  1. a quiet window of at least rafcapQuietWindowFloor past the terminal-status line,
//     spent with nothing asked — so an UNPROMPTED roster cannot be mistaken for a
//     prompted one. This is the one wait that is a floor rather than a deadline.
//  2. one mid-session initialize, the ask position every roster in the repo follows.
//  3. one further ordinary turn, read through its own result line — because claude may
//     batch roster changes to a turn boundary, and a capture stopping at the terminal
//     status records an absence that is only an absence WITHIN one turn.
//
// # What is REUSED rather than forked, and why most of it carries another ticket's
// prefix
//
// tpcapHoldFIFO, tpcapCensus, bgIdlePrompt, dropcapRecorder, dropcapRedactor,
// dropcapScanner, dropcapMakeEntry, dropcapWaitForChild, newDropcapArgvHandler and
// parseOne are CALLED here. The prefix marks the file an identifier was minted in, per
// this package's branch-hygiene rule; it is not private scope, and re-deriving any of
// them would be the fork the ticket forbids. #2247's rig — the start-then-finish
// staging this reuses — is a SIBLING of this file and is not edited by it.
//
// Both initialize asks are ADDITIONS to that rig rather than settings to flip: the
// string does not occur in it at all. The precedents are applied_settings_test.go for
// the config field, interactive_stream_inband_model_test.go for the mid-session call,
// and initialize_control_probe_test.go for the reply's doubly-nested shape.
//
// # What this record deliberately does NOT keep
//
// A control_response's PAYLOAD. AC2 needs the ordering of rosters against control
// traffic, not the replies' contents, and an initialize reply is the operator's local
// claude configuration inventory — the model menu, the tool and MCP server names, cwd,
// apiKeySource. Keeping only the index, the request id and the reply subtype removes
// that exposure class from a committed public artefact instead of filtering it.
// TestRafcapControlResponsesKeepNoPayload pins that as a property of the record rather
// than a habit of its author.
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine, and nothing else. Like #2089's and
// #2247's, this probe is gated on the FIXTURE'S ABSENCE, so the live gate the ticket is
// labelled for is what produces the evidence; it disarms as soon as the fixture exists.
//
// To force a re-capture at a new claude version, over an existing fixture:
//
//	PYRY_PROBE_ROSTER_AFTER_FINISH_CAPTURE=1 go test -tags e2e_realclaude -timeout 20m -v \
//	  -run '^TestRealClaude_RosterAfterFinishCapture$' ./internal/e2e/realclaude/
//
// A skip carries no signal about claude's behaviour — but read WHICH skip: "fixture
// already exists" is the steady state, while a skip from WithWorktreeAuthenticated
// means the machine has no claude login and the evidence was not produced.

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
)

// rafcapEnableEnv FORCES a re-capture when the fixture already exists. It is NOT the
// gate — see the gate comment in TestRealClaude_RosterAfterFinishCapture.
const rafcapEnableEnv = "PYRY_PROBE_ROSTER_AFTER_FINISH_CAPTURE"

// rafcapFixturePath is where a good capture LANDS, in-repo, ready to commit. The
// streamsup-side reader names the same file through its own package constant and
// enforces the same version pin from the other end; the two are deliberately not
// shared, because that reader takes no path parameter by design.
const (
	rafcapFixtureVersion = "2.1.272"
	rafcapFixturePath    = "testdata/roster_after_finish_v" + rafcapFixtureVersion + ".json"
)

// Every file-local identifier takes the rafcap prefix, for the reason #1260's header
// gives: siblings add files to this package concurrently and a branch-overlap check
// does not catch a same-package identifier collision.
const (
	rafcapTicket         = "2525"
	rafcapWorkdirName    = "rafcap-work"
	rafcapFIFOName       = "rafcap-hold"
	rafcapRecordName     = "rafcap-record.json"
	rafcapArtifactPrefix = "pyry-2525-capture-*"
	rafcapModel          = "haiku"
	// #1260's value, deliberately. A five-second default is what makes the blocked
	// `cat` time out of the foreground and become a BACKGROUND TASK, which is the thing
	// that has to exist before it can complete. #2089 raises this to 180000 for the
	// opposite purpose — keeping a call in the foreground — and copying that value here
	// would stage a turn with no background task in it at all.
	rafcapBashTimeoutMS = "5000"
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from every
	// sibling probe's so a record can never be mistaken for one of theirs.
	rafcapSessionID = "9c4f2b18-7d35-4a60-8e12-6f0b93ad5c7e"
)

// The follow-on turn's prompt — AC1's third read. Deliberately trivial and
// rig-authored: it exists to produce a TURN BOUNDARY, not an answer, and anything that
// made claude think or call a tool would add lines to the census for no gain and put
// unpredictable content on the wire the redactor would then have to cover.
const rafcapFollowOnPrompt = "Reply with exactly the word: ok"

const (
	rafcapRendezvousWait = 90 * time.Second
	rafcapBackgroundWait = 100 * time.Second
	// How long to wait, after the FIFO is released, for the line carrying the task's
	// TERMINAL STATUS — whichever of task_updated and task_notification carries it on
	// this release, which is AC3's question and not something to assume. Does NOT end
	// at `result`: a background task outlives the turn that started it.
	rafcapTerminalWait = 90 * time.Second
	// THE QUIET WINDOW, and the one wait here that is a FLOOR rather than a deadline.
	// Spending it is what licenses the record to say a roster did not arrive
	// unprompted; returning early would record an absence nothing had waited for. The
	// margin over the floor is for clock granularity, so a window measured at 29.98s
	// can never read as short of AC1's 30.
	rafcapQuietWindowFloor = 30 * time.Second
	rafcapQuietWindow      = 35 * time.Second
	// How long to wait for a control_response to either initialize ask. Generous
	// against model latency; overrunning it is RECORDED (mid_session_ask_answered)
	// rather than fatal, because the other two prompts still ran.
	rafcapInitializeWait = 45 * time.Second
	// How long to wait for the follow-on turn's own `result`. Bounded, and the run
	// STOPS as soon as it is read: task_notification_v2.1.259.json records turn_seconds
	// 360.0 with terminated_on "budget", a probe that burned its whole turn budget with
	// its quarry in hand after 3.3 seconds, and it was one of five that pushed the live
	// gate past its deadline.
	rafcapFollowOnTurnWait = 120 * time.Second
	rafcapRunExitWait      = 30 * time.Second
	// dropcapRecorder exposes only a `result` signal, so every wait above polls its
	// snapshot. Teaching the recorder a second channel would fork a helper every probe
	// in this package shares.
	rafcapPoll = 250 * time.Millisecond
	// Deliberately larger than every phase wait summed, so a turn that runs each one to
	// its deadline still has headroom to end on `result`. Sized by
	// TestRafcapBudgetOutlastsItsPhases rather than by eye: a budget below that sum
	// leaves the final select with BOTH arms ready and terminated_on picked at random —
	// a record misreporting how its own turn ended.
	rafcapTurnBudget = 10 * time.Minute
)

const (
	rafcapOutcomeMeasured        = "measured"
	rafcapOutcomeStagingFailed   = "staging-failed"
	rafcapOutcomeInstrumentBroke = "instrument-broken"
)

const (
	rafcapTerminatedResult = "result"
	rafcapTerminatedBudget = "budget"
)

// The four verdicts, a CLOSED SET. The streamsup reader spells the same four as its
// own package constants and REFUSES ANYTHING OUTSIDE THEM, so a drift between the two
// spellings reddens `make check` at the reader rather than passing as an
// uninterpretable verdict. The two are deliberately not shared: a shared constant
// would make the reader agree with the probe by construction, which is the thing the
// five-standalone-readers rule exists to prevent.
const (
	rafcapVerdictNoClaim    = "task-did-not-complete"
	rafcapVerdictNone       = "none-after-terminal-status"
	rafcapVerdictStillLists = "roster-still-lists-the-finished-task"
	rafcapVerdictOmits      = "roster-omits-the-finished-task"
)

// The three prompts a roster can have followed, plus the one position that is not a
// prompt at all. A roster before the terminal status says nothing about a completion —
// dropped_lines_v2.1.220.json records exactly such a line, at the task's START.
const (
	rafcapPhasePreTerminal   = "before-terminal-status"
	rafcapPhaseQuietWindow   = "quiet-window"
	rafcapPhaseMidSessionAsk = "mid-session-initialize"
	rafcapPhaseFollowOnTurn  = "follow-on-turn"
)

// The subtypes, spelled as literals rather than imported from streamsup: the record is
// EVIDENCE about claude's wire shape, and a census keyed on the production constants
// would agree with the matcher by construction instead of measuring it.
const (
	rafcapRosterSubtype       = "background_tasks_changed"
	rafcapStartedSubtype      = "task_started"
	rafcapUpdatedSubtype      = "task_updated"
	rafcapNotificationSubtype = "task_notification"
)

// rafcapStatusCandidates are the two subtypes AC3 asks about by name: the daemon maps
// BOTH onto turnevent.BackgroundTaskUpdated while the desktop reads only one event
// type, so which of them carried the terminal status is a fact a client acts on.
var rafcapStatusCandidates = []string{rafcapUpdatedSubtype, rafcapNotificationSubtype}

// rafcapKeptSubtypes is every subtype whose payload this record retains.
var rafcapKeptSubtypes = []string{
	rafcapRosterSubtype, rafcapStartedSubtype, rafcapUpdatedSubtype, rafcapNotificationSubtype,
}

// rafcapTerminalTokens are the status values that mean a background task has ENDED.
// `completed` is the one observed in this repo (task_notification_v2.1.259.json); the
// other three are documented and staged by nothing here, so a record carrying one
// would be a measurement past what has ever been seen — which is why they are listed
// rather than assumed absent.
var rafcapTerminalTokens = []string{"completed", "failed", "stopped", "killed"}

var rafcapArgs = []string{"--model", rafcapModel, "--dangerously-skip-permissions"}

const rafcapSpawnShapeDelta = "The YOLO interactive shape, identical to #1260's, #2089's and #2247's — " +
	"see dropcapSpawnShapeDelta for what production's non-yolo spawn adds and what that implies for " +
	"system/init. ONE DELIBERATE ADDITION over those three: streamsup.Config.RequestInitializeOnSpawn is " +
	"set, which is the per-child initialize ask the interactive daemon's config mapping turns on in " +
	"production and which no capture probe in this package had enabled before. That ask lands BEFORE the " +
	"child's first turn, which is the one ask position in this repo never followed by a roster line."

const rafcapLimitations = "One turn plus one follow-on turn, one spawn shape, one claude version, one " +
	"model (" + rafcapModel + "), one tool (Bash), one terminal state. Cross-version, cross-model and " +
	"cross-tool stability are UNMEASURED. The status observed is whatever a `cat` reaching EOF produces; " +
	"the documented failed and stopped states are NOT staged and nothing here says what they carry. " +
	"THE QUIET WINDOW IS A FLOOR, NOT A CLAIM ABOUT BATCHING: a roster arriving past its end would be " +
	"attributed to whichever prompt followed, so read offset_seconds before concluding a line was " +
	"prompted. OFFSETS IN SECONDS ARE POLL-GRANULAR — dropcapRecorder does not timestamp lines and " +
	"teaching it to would fork a helper every probe here shares, so each line's time is when the poll " +
	"loop FIRST SAW it, within one tick. offset_lines is exact. A roster absent from this record did not " +
	"appear in THESE turns, which is not the same as claude never emitting it: this family's firing rate " +
	"is measured to vary per turn — dropped_lines_v2.1.220.json fired the roster at a task's start and " +
	"task_notification_v2.1.259.json fired it neither at the start nor at the finish."

const rafcapRedactionRationale = "Inherited whole from #1260 (see dropcapRedactionRationale): a fresh " +
	"empty non-git workdir, rig-authored prompts (bgIdlePrompt and a fixed follow-on line), a `cat " +
	"<fifo>` that produces no output, no os.Environ() read into the record, the declared dropcapRedactor " +
	"substitution table over every string, and dropcapScanner as a fail-closed deny-scan over the " +
	"marshalled record. " +
	"WHAT THIS PROBE SPECIFICALLY COULD HAVE CARRIED AND DOES NOT: the initialize control_response's " +
	"payload. This is the first capture in the family to send that request from inside a recording " +
	"probe, and the reply is the operator's local claude configuration inventory — the model menu, the " +
	"tool and MCP server names, the working directory, the api key source. AC2 needs the ORDERING of " +
	"roster lines against control traffic and not the replies' contents, so this record keeps a " +
	"control_response's index, request id and reply subtype and DISCARDS ITS PAYLOAD, which removes the " +
	"class rather than filtering it. " +
	"A third mechanism, inherited from #2247: rafcapUnredactedPathFields refuses to PROMOTE a record " +
	"whose kept frames still carry a value beginning with a path separator after redaction, and names " +
	"the FIELD rather than the value. task_notification is documented to carry output_file, a path on " +
	"the operator's host. A refusal costs one live turn; a promotion costs a public leak. The inherited " +
	"prefix classes are named here BY SYMBOL (dropcapFixedNeedles) and deliberately not spelled out, " +
	"because this string is itself written into the record and then deny-scanned: a rationale that " +
	"quotes a needle fails the scan it describes. " +
	"DELIBERATELY KEPT, because removing them would defeat the ticket: the roster's tasks array verbatim " +
	"(its description field is the rig's own `cat <fifo>` command line), claude's task and tool " +
	"identifiers, status tokens, the task-lifecycle payload structure and the whole-turn line-type census."

// --- the record --------------------------------------------------------------

// rafcapFrame is one captured background-task line. The payload half is built by
// dropcapMakeEntry so the base64 arm for invalid UTF-8 is shared rather than
// re-derived; the rest is this ticket's.
//
// events_emitted is the SHIPPED parser's verdict (parseOne), never a mirror of its
// tables — it rides along as data so a subtype the parser had started mapping is
// visible here rather than silently filtered out.
type rafcapFrame struct {
	Index                   int    `json:"index"`
	Type                    string `json:"type"`
	Subtype                 string `json:"subtype"`
	PayloadLenBytesCaptured int    `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int    `json:"payload_len_bytes"`
	PayloadEncoding         string `json:"payload_encoding"`
	Payload                 string `json:"payload,omitempty"`
	PayloadB64              string `json:"payload_b64,omitempty"`
	EventsEmitted           int    `json:"events_emitted"`
}

// rafcapRosterObs is AC2's answer for ONE system/background_tasks_changed line.
//
// Tasks is claude's array VERBATIM, out of the redacted payload, as json.RawMessage:
// decoding into a typed shape would drop every key this repo has not declared, which
// is the exact failure the family's declare-only-what-you-captured rule prevents.
type rafcapRosterObs struct {
	Index int             `json:"index"`
	Tasks json.RawMessage `json:"tasks"`
	// TasksDecoded is false when the payload held no readable `tasks` array — a shape
	// change worth recording rather than silently reporting as an empty roster.
	TasksDecoded      bool `json:"tasks_decoded"`
	TaskCount         int  `json:"task_count"`
	ListsFinishedTask bool `json:"lists_finished_task"`
	// OffsetLines is this line's index minus the terminal-status line's, so a strictly
	// positive value is "after the completion". OffsetSeconds is poll-granular; see
	// rafcapLimitations.
	OffsetLines   int     `json:"offset_lines"`
	OffsetSeconds float64 `json:"offset_seconds"`
	// AfterControlResponse is whether the IMMEDIATELY PRECEDING captured line was a
	// control_response — the discriminator between "the roster only ever answers a
	// control request" and "claude sends it unprompted", which point two client
	// tickets at different fixes.
	AfterControlResponse bool   `json:"after_control_response"`
	PrecedingLineType    string `json:"preceding_line_type"`
	// ResultsBefore places the line against the turns' `result` lines: 1 means it
	// arrived after the first turn ended and before the follow-on turn did.
	ResultsBefore int         `json:"results_before"`
	Phase         string      `json:"phase"`
	Frame         rafcapFrame `json:"frame"`
}

// rafcapControlObs is one control_response, kept WITHOUT ITS PAYLOAD. See this file's
// header and rafcapRedactionRationale: the ordering is what AC2 needs, and the reply's
// contents are the operator's configuration inventory.
type rafcapControlObs struct {
	Index     int    `json:"index"`
	RequestID string `json:"request_id"`
	Subtype   string `json:"subtype"`
	Phase     string `json:"phase"`
}

// rafcapStatusObs is AC3 for ONE candidate subtype. Emitted for BOTH whether or not
// they fired: an omitted subtype and one that fired without a status look identical to
// a later reader, and only one of those is a measurement.
type rafcapStatusObs struct {
	Subtype         string `json:"subtype"`
	Fired           bool   `json:"fired"`
	LineCount       int    `json:"line_count"`
	CarriedStatus   bool   `json:"carried_status"`
	FirstToken      string `json:"first_status_token"`
	FirstLocation   string `json:"first_status_location"`
	FirstTerminalAt int    `json:"first_terminal_line_index"`
	Note            string `json:"note"`
}

type rafcapRecord struct {
	Ticket          string   `json:"ticket"`
	ClaudeVersion   string   `json:"claude_version"`
	CapturedAt      string   `json:"captured_at"`
	IsCapture       bool     `json:"is_capture"`
	Model           string   `json:"model"`
	SpawnShape      []string `json:"spawn_shape"`
	SpawnShapeDelta string   `json:"spawn_shape_delta"`
	EnvDelta        []string `json:"env_delta"`
	Workdir         string   `json:"workdir"`
	Prompts         []string `json:"prompts"`

	Outcome       string `json:"outcome"`
	OutcomeDetail string `json:"outcome_detail"`
	TerminatedOn  string `json:"terminated_on"`

	// The staging measurement. Without these an empty rosters array cannot separate
	// "claude never ran the command" from "it ran but was never backgrounded" from "a
	// background task existed, was allowed to finish, and no roster came" — and only
	// the third is a finding about claude's surface.
	ForegroundCallObserved bool    `json:"foreground_call_observed"`
	BackgroundTaskObserved bool    `json:"background_task_observed"`
	FinishedTaskIDKnown    bool    `json:"finished_task_id_known"`
	TerminalStatusObserved bool    `json:"terminal_status_observed"`
	HeldSeconds            float64 `json:"fifo_held_seconds"`
	QuietWindowSeconds     float64 `json:"quiet_window_seconds"`
	MidSessionAskSent      bool    `json:"mid_session_ask_sent"`
	MidSessionAskAnswered  bool    `json:"mid_session_ask_answered"`
	FollowOnTurnObserved   bool    `json:"follow_on_turn_observed"`
	TurnSeconds            float64 `json:"turn_seconds"`

	// AC2's separate question: production's per-spawn ask is the one position in this
	// repo never followed by a roster, and whether that held here is a fact of its own
	// rather than something to read out of the rosters list.
	PerSpawnAskAnswered          bool `json:"per_spawn_ask_answered"`
	PerSpawnAskFollowedByRoster  bool `json:"per_spawn_ask_followed_by_roster"`
	PerSpawnControlResponseIndex int  `json:"per_spawn_control_response_index"`

	// AC3.
	TerminalStatusSubtype  string            `json:"terminal_status_subtype"`
	TerminalStatusToken    string            `json:"terminal_status_token"`
	TerminalStatusLocation string            `json:"terminal_status_location"`
	TerminalStatusIndex    int               `json:"terminal_status_line_index"`
	StatusCandidates       []rafcapStatusObs `json:"status_candidates"`

	// A content-free census of everything else on the wire, from tpcapCensus.
	LineTypeCensus   map[string]int `json:"line_type_census"`
	ToolCalls        []string       `json:"tool_calls"`
	ToolResultErrors int            `json:"tool_result_errors"`
	UndecodedLines   int            `json:"undecoded_lines"`

	LinesCaptured int `json:"lines_captured"`

	// AC2's answer.
	Verdict          string             `json:"verdict"`
	VerdictPrompt    string             `json:"verdict_prompt"`
	Rosters          []rafcapRosterObs  `json:"rosters"`
	ControlResponses []rafcapControlObs `json:"control_responses"`
	Frames           []rafcapFrame      `json:"frames"`

	UnredactedPathFields []string `json:"unredacted_path_fields"`

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

	// marks are the line indices each phase ended at, and they are what
	// rafcapPhaseFor attributes a roster to a prompt by. Unexported: they are an
	// instrument of this run rather than evidence about claude, and a reader given
	// them would be tempted to re-derive the attribution the record already states.
	markTerminal   int
	markQuietEnd   int
	markMidSession int
}

// rafcapSeedRecord returns the record's RIG-AUTHORED half: every field whose value is a
// compile-time constant of this file, rather than something claude, the clock or the
// filesystem produced. The live probe seeds its record from here and fills the measured
// fields in.
//
// It is a function so the offline deny-scan net can scan EXACTLY the bytes the probe
// puts in the record instead of a second copy of the same literals. A copy drifts, and
// the drift is silent until a live turn is spent: #2247's first live lap died on its
// rationale spelling out the very prefixes dropcapFixedNeedles searches for, which
// failed the scan closed and wrote nothing — 1400 s of gate and a real token spend for
// a defect that is a string comparison to find.
//
// Keep this constants-only. A field whose value depends on the run belongs at the call
// site, because the net cannot judge what it cannot know offline.
func rafcapSeedRecord() *rafcapRecord {
	return &rafcapRecord{
		Ticket:    rafcapTicket,
		IsCapture: true,
		Model:     rafcapModel,
		// A FIXED LITERAL, never harvested from os.Environ().
		EnvDelta:              []string{dropcapBashTimeoutEnv + "=" + rafcapBashTimeoutMS},
		SpawnShapeDelta:       rafcapSpawnShapeDelta,
		Prompts:               []string{},
		Rosters:               []rafcapRosterObs{},
		ControlResponses:      []rafcapControlObs{},
		Frames:                []rafcapFrame{},
		StatusCandidates:      []rafcapStatusObs{},
		UnredactedPathFields:  []string{},
		CredentialScanSkipped: []string{},
		RedactionRationale:    rafcapRedactionRationale,
		Limitations:           rafcapLimitations,
		Verdict:               rafcapVerdictNoClaim,
		// Line indices are non-negative, so -1 reads as "never observed" rather than as
		// the first line of the stream.
		TerminalStatusIndex:          -1,
		PerSpawnControlResponseIndex: -1,
	}
}

func (rec *rafcapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
}

// rafcapPhaseFor names the prompt a line at this index followed, from the marks
// recorded at each phase boundary.
//
// A line at or before the terminal-status line is pre-terminal and says nothing about
// a completion — dropped_lines_v2.1.220.json holds exactly such a roster, emitted at
// the task's START.
func (rec *rafcapRecord) rafcapPhaseFor(index int) string {
	switch {
	case index <= rec.markTerminal:
		return rafcapPhasePreTerminal
	case index < rec.markQuietEnd:
		return rafcapPhaseQuietWindow
	case index < rec.markMidSession:
		return rafcapPhaseMidSessionAsk
	default:
		return rafcapPhaseFollowOnTurn
	}
}

// fixtureWorthy answers whether this record may be promoted to rafcapFixturePath, and
// names the reason when it may not.
//
// THE PROMOTION RULE IS KEYED ON THE STAGING, NOT ON A ROSTER HAVING FIRED. This
// probe's likeliest real answer is an ABSENCE, and a rule that demanded a roster line
// would refuse the ticket's own expected result as vacuous. What makes an absence
// meaningful instead is that the staging worked: a task was backgrounded, it reached a
// terminal status, the quiet window was spent to its floor, and a whole further turn
// was read. A capture missing any of those records an absence nothing waited for.
//
// A mid-session ask that went UNANSWERED does not refuse. Its failure is itself
// recorded, and discarding a live turn over one of three prompts would throw away the
// other two prompts' evidence.
//
// The version arm is the producing half of the pin the streamsup reader enforces
// (rosterAfterFinishCaptureVersion). `claude --version` prints "<version> (Claude
// Code)", so the comparison is on the leading token. Refusing to write under the wrong
// name turns a claude upgrade into an instruction to repin, instead of a file whose
// verdict silently describes a different release.
func (rec *rafcapRecord) fixtureWorthy() (string, bool) {
	if !rec.ForegroundCallObserved || !rec.BackgroundTaskObserved {
		return "the staging failed before a background task existed: " + rec.stagingVerdict(), false
	}
	if !rec.TerminalStatusObserved || rec.Verdict == rafcapVerdictNoClaim {
		return "the staged task never reached a terminal status, so this record makes no claim about " +
			"the roster: " + rec.stagingVerdict(), false
	}
	if rec.QuietWindowSeconds < rafcapQuietWindowFloor.Seconds() {
		return fmt.Sprintf("the quiet window ran %.1fs, short of the %s floor — an absence nothing "+
			"waited for is not a measurement", rec.QuietWindowSeconds, rafcapQuietWindowFloor), false
	}
	if !rec.FollowOnTurnObserved {
		return "the follow-on turn never reached its own `result`, so any absence here is an absence " +
			"only WITHIN one turn — and claude batching roster changes to a turn boundary is precisely " +
			"what that read exists to rule out", false
	}
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != rafcapFixtureVersion {
		return fmt.Sprintf("claude_version %q is not the %s pinned in the fixture name — repin "+
			"rafcapFixtureVersion and rosterAfterFinishCaptureVersion together, then re-run",
			got, rafcapFixtureVersion), false
	}
	// The consumer refuses any encoding but json-string, and dropcapMakeEntry emits
	// base64 with an EMPTY payload for a frame that is not valid UTF-8. Promoting one
	// would land a fixture that reddens `make check` for every unrelated ticket.
	for _, f := range rec.Frames {
		if f.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("frame %d is encoded %q, and the streamsup reader reads only %q — a "+
				"non-UTF-8 frame carries no readable payload", f.Index, f.PayloadEncoding,
				dropcapEncodingJSONString), false
		}
	}
	// The third redaction mechanism. See rafcapRedactionRationale: a value the declared
	// table did not know and the deny-scan's fixed prefixes did not match stops the
	// promotion here rather than reaching a public artefact.
	if len(rec.UnredactedPathFields) > 0 {
		return fmt.Sprintf("a kept payload carries an absolute host path in %v after redaction. Extend "+
			"dropcapRedactor's table with the class that value belongs to and re-run; the FIELD is named "+
			"and the value deliberately is not, because printing it is the exposure this refusal exists "+
			"to prevent", rec.UnredactedPathFields), false
	}
	return "", true
}

// stagingVerdict names WHICH of the ways a no-claim capture can happen actually
// happened.
//
// Only the last case is evidence about claude's surface. The others are the rig
// failing to stage the thing it meant to measure, and saying so plainly is what stops
// the next reader from loosening the promotion rule to fix a staging bug.
func (rec *rafcapRecord) stagingVerdict() string {
	switch {
	case !rec.ForegroundCallObserved:
		return fmt.Sprintf("claude never opened the FIFO within %s, so no Bash call ever started. The "+
			"staging failed BEFORE any background task could exist — read tool_calls and "+
			"line_type_census to see what claude did instead", rafcapRendezvousWait)
	case !rec.BackgroundTaskObserved:
		return fmt.Sprintf("a Bash call started but no system/%s line arrived within %s, so the call was "+
			"never backgrounded and there was no background task to complete. BASH_DEFAULT_TIMEOUT_MS is "+
			"%s ms: claude kept the call in the foreground or requested a timeout of its own. The staging "+
			"failed, not the surface", rafcapStartedSubtype, rafcapBackgroundWait, rafcapBashTimeoutMS)
	case !rec.TerminalStatusObserved:
		return fmt.Sprintf("a background task was started and the FIFO was held %.1fs and then RELEASED, "+
			"but no %v line carrying a terminal status arrived within %s. The task's own completion was "+
			"never seen, so nothing here can say what followed one. The staging failed",
			rec.HeldSeconds, rafcapStatusCandidates, rafcapTerminalWait)
	default:
		return fmt.Sprintf("a background task completed (system/%s carried %q after a %.1fs hold), the "+
			"quiet window ran %.1fs, a mid-session initialize was sent (answered=%v) and a further turn "+
			"was read to its result. The staging WORKED, so the rosters array is a finding about claude's "+
			"surface and is to be transcribed, not explained away",
			rec.TerminalStatusSubtype, rec.TerminalStatusToken, rec.HeldSeconds, rec.QuietWindowSeconds,
			rec.MidSessionAskAnswered)
	}
}

// --- reading the lines -------------------------------------------------------

// rafcapStatusOf reports the status token one task line carries, and WHERE it was
// found.
//
// Both locations are real rather than defensive. At 2.1.259 task_notification carried
// a top-level `status`; at 2.1.220 task_updated's payload was {task_id, patch} with the
// change inside `patch`, and the live observation this ticket was filed from reports a
// task_updated carrying status completed. A reader of only one of the two would record
// a terminal status as absent on half the shapes claude has been seen to send.
//
// `patch` is taken as a generic value, not a declared shape: systemTaskUpdatedLine
// keeps it as json.RawMessage precisely because "whatever claude puts there" is the
// point, and a patch that is a string or a number is a shape change to notice rather
// than a decode to fail on.
func rafcapStatusOf(payload []byte) (token, location string) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return "", ""
	}
	if s, ok := rafcapStringField(obj, "status"); ok {
		return s, "top-level"
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(obj["patch"], &patch); err != nil {
		return "", ""
	}
	if s, ok := rafcapStringField(patch, "status"); ok {
		return s, "patch"
	}
	return "", ""
}

// rafcapStringField reads one key as a string, reporting absence and a non-string
// value identically: neither is a status token, and inventing one from a number would
// be a decode this family's rules forbid.
func rafcapStringField(obj map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := obj[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, s != ""
}

// rafcapIsTerminal answers whether a status token means the task has ENDED.
func rafcapIsTerminal(token string) bool {
	for _, t := range rafcapTerminalTokens {
		if token == t {
			return true
		}
	}
	return false
}

// rafcapRosterTasks pulls the `tasks` array out of a roster payload VERBATIM, plus the
// entry ids it lists.
//
// The array is kept as raw bytes rather than re-marshalled from a decoded shape: a
// typed target would drop every key this repo has not declared, and the whole point of
// the record is to carry what claude sent. The ids are read separately, only to answer
// whether the finished task is still listed.
func rafcapRosterTasks(payload []byte) (tasks json.RawMessage, ids []string, ok bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, nil, false
	}
	raw, present := obj["tasks"]
	if !present {
		return nil, nil, false
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return raw, nil, false
	}
	ids = make([]string, 0, len(entries))
	for _, e := range entries {
		if id, found := rafcapStringField(e, "task_id"); found {
			ids = append(ids, id)
		}
	}
	return raw, ids, true
}

// rafcapControlOf decodes a control_response line's ENVELOPE ONLY — its request id and
// reply subtype, through the doubly-nested shape initialize_control_probe_test.go
// documents (control_response → response → {subtype, request_id}).
//
// It deliberately returns no payload. See this file's header: the reply's contents are
// the operator's configuration inventory and AC2 needs only the ordering.
func rafcapControlOf(payload []byte) (requestID, subtype string) {
	var line struct {
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &line); err != nil {
		return "", ""
	}
	return line.Response.RequestID, line.Response.Subtype
}

// rafcapUnredactedPathFields returns the FIELD PATHS, never the values, at which a kept
// frame still carries a string beginning with '/' after redaction.
//
// Field names are claude's vocabulary and appear in nobody's data, so naming them is
// safe in a way naming a value never is — the same distinction the deny-scan draws when
// it reports a CLASS. It walks to any depth and through arrays, because
// task_notification's output_file is documented at the top level but nothing guarantees
// a host path stays where the docs put it.
//
// Scoped to EVERY kept frame rather than to one subtype: unlike #2247's companions,
// none of these payloads is already committed elsewhere in unredacted form, and the
// roster's own description field is the rig's `cat <fifo>` command line whose path is a
// declared redaction class.
func rafcapUnredactedPathFields(frames []rafcapFrame) []string {
	found := map[string]bool{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch n := v.(type) {
		case string:
			if strings.HasPrefix(n, "/") {
				found[prefix] = true
			}
		case []any:
			for _, e := range n {
				walk(prefix+"[]", e)
			}
		case map[string]any:
			for k, e := range n {
				child := k
				if prefix != "" {
					child = prefix + "." + k
				}
				walk(child, e)
			}
		}
	}
	for _, f := range frames {
		if f.Payload == "" {
			continue
		}
		var doc any
		if err := json.Unmarshal([]byte(f.Payload), &doc); err != nil {
			// An undecodable payload cannot be swept, and a promotion that silently
			// skipped it would be the hole this function exists to close.
			found[fmt.Sprintf("frame %d: payload does not decode, so it could not be swept", f.Index)] = true
			continue
		}
		walk("", doc)
	}
	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// rafcapStatusTable is AC3: for each candidate subtype, whether it fired in these turns
// and whether any of its lines carried a status token.
//
// EVERY candidate gets an entry whether or not it fired. An omitted subtype and one
// that fired carrying no status look identical to a later reader, and only one of those
// is a measurement.
func rafcapStatusTable(frames []rafcapFrame) []rafcapStatusObs {
	out := make([]rafcapStatusObs, 0, len(rafcapStatusCandidates))
	for _, subtype := range rafcapStatusCandidates {
		obs := rafcapStatusObs{Subtype: subtype, FirstTerminalAt: -1}
		for _, f := range frames {
			if f.Subtype != subtype {
				continue
			}
			obs.Fired = true
			obs.LineCount++
			token, location := rafcapStatusOf([]byte(f.Payload))
			if token == "" {
				continue
			}
			if !obs.CarriedStatus {
				obs.CarriedStatus = true
				obs.FirstToken = token
				obs.FirstLocation = location
			}
			if obs.FirstTerminalAt < 0 && rafcapIsTerminal(token) {
				obs.FirstTerminalAt = f.Index
			}
		}
		switch {
		case !obs.Fired:
			obs.Note = "did not fire in these turns; nothing here says whether claude uses this subtype " +
				"to report a completion, only that no line arrived to read one from"
		case !obs.CarriedStatus:
			obs.Note = "fired carrying NO status field, at the top level or inside patch"
		case obs.FirstTerminalAt < 0:
			obs.Note = "fired carrying a status that is not in the terminal set: " + obs.FirstToken
		default:
			obs.Note = "fired carrying the terminal status " + obs.FirstToken + " at " + obs.FirstLocation
		}
		out = append(out, obs)
	}
	return out
}

// rafcapDecideVerdict reads the verdict off the roster observations, from the closed
// set, and names the prompt it was read after.
//
// THE LAST POST-TERMINAL ROSTER DECIDES, because that is the state a client is left
// holding: a line that still listed the task followed by one that omitted it means the
// count did come down. The full ordered list is in the record either way, so nothing is
// hidden by the choice — it only decides which one word the two desktop tickets are
// told.
func rafcapDecideVerdict(terminalObserved bool, rosters []rafcapRosterObs) (verdict, prompt string) {
	if !terminalObserved {
		return rafcapVerdictNoClaim, ""
	}
	var last *rafcapRosterObs
	for i := range rosters {
		if rosters[i].OffsetLines > 0 {
			last = &rosters[i]
		}
	}
	if last == nil {
		return rafcapVerdictNone, ""
	}
	if last.ListsFinishedTask {
		return rafcapVerdictStillLists, last.Phase
	}
	return rafcapVerdictOmits, last.Phase
}

// --- waiting -----------------------------------------------------------------

// rafcapTimeline records when the poll loop FIRST SAW each line index.
//
// dropcapRecorder does not timestamp lines, and teaching it to would fork a helper
// every probe in this package shares. Every phase of this probe is a poll loop, so
// there is no gap in coverage; the granularity is one rafcapPoll tick and
// rafcapLimitations says so in the record.
//
// DELIBERATELY UNSYNCHRONISED, and touched only from the test goroutine. Stated here
// because a helper that looks shareable and is not is how a race gets added later.
type rafcapTimeline struct{ seen map[int]time.Time }

func newRafcapTimeline() *rafcapTimeline { return &rafcapTimeline{seen: map[int]time.Time{}} }

func (tl *rafcapTimeline) note(lines []dropcapCaptured, now time.Time) {
	for _, c := range lines {
		if _, ok := tl.seen[c.Index]; !ok {
			tl.seen[c.Index] = now
		}
	}
}

// offset returns the seconds between two line indices, and whether both were seen.
func (tl *rafcapTimeline) offset(from, to int) (float64, bool) {
	a, okA := tl.seen[from]
	b, okB := tl.seen[to]
	if !okA || !okB {
		return 0, false
	}
	return b.Sub(a).Seconds(), true
}

// rafcapAwait polls the recorder until satisfied returns true or the deadline passes,
// noting every newly-seen line into the timeline as it goes.
//
// It NEVER ends at the turn's `result`, and that is the single most consequential
// decision in this rig. Every wait here is for something that can outlive the turn it
// belongs to: a background task's terminal status by construction, and the two roster
// prompts by design. dropcapRecorder's resultSeen is closed via sync.Once, so a wait
// that took it as terminal would stay permanently ready — collapsing the whole wait to
// one snapshot() and then reporting the unspent time as a finding about claude's
// surface. That is the exact defect #2247's tncapResultIsNotTheEnd exists to name, and
// this rig declines to offer the other mode at all.
//
// The one wait that legitimately ends early is task_started, which fires inside its own
// turn; it is expressed as a predicate over the snapshot like everything else, and its
// deadline is short enough that spending it costs nothing worth a second mode.
func rafcapAwait(recorder *dropcapRecorder, tl *rafcapTimeline, within time.Duration,
	satisfied func([]dropcapCaptured) bool) bool {
	deadline := time.Now().Add(within)
	for {
		lines, _ := recorder.snapshot()
		tl.note(lines, time.Now())
		if satisfied(lines) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(rafcapPoll)
	}
}

// rafcapHasSubtype is satisfied once a system line of this subtype has been captured.
func rafcapHasSubtype(subtype string) func([]dropcapCaptured) bool {
	return func(lines []dropcapCaptured) bool {
		for _, c := range lines {
			if c.Decoded && c.Type == "system" && c.Subtype == subtype {
				return true
			}
		}
		return false
	}
}

// rafcapHasTerminalStatus is satisfied once EITHER candidate subtype has carried a
// terminal status token. Which one it was is AC3's question and is read off the frames
// afterwards; this predicate only decides when the quiet window may start.
func rafcapHasTerminalStatus(lines []dropcapCaptured) bool {
	_, _, _, found := rafcapFindTerminal(lines)
	return found
}

// rafcapFindTerminal returns the FIRST line of either candidate subtype carrying a
// terminal status: its index, its subtype and the token.
func rafcapFindTerminal(lines []dropcapCaptured) (index int, subtype, token string, found bool) {
	for _, c := range lines {
		if !c.Decoded || c.Type != "system" {
			continue
		}
		if c.Subtype != rafcapUpdatedSubtype && c.Subtype != rafcapNotificationSubtype {
			continue
		}
		if t, _ := rafcapStatusOf(c.Raw); rafcapIsTerminal(t) {
			return c.Index, c.Subtype, t, true
		}
	}
	return -1, "", "", false
}

// rafcapHasControlResponseAfter is satisfied once a control_response has been captured
// at or past this index — which is how the mid-session ask's own reply is told from the
// per-spawn ask's, the two being indistinguishable by type alone.
func rafcapHasControlResponseAfter(mark int) func([]dropcapCaptured) bool {
	return func(lines []dropcapCaptured) bool {
		for _, c := range lines {
			if c.Decoded && c.Type == "control_response" && c.Index >= mark {
				return true
			}
		}
		return false
	}
}

// rafcapHasResultCount is satisfied once n `result` lines have been captured.
//
// A COUNT rather than the recorder's resultSeen channel, because the follow-on turn's
// result is the SECOND one and that channel closes on the first — it cannot tell the
// two turns apart, and waiting on it would return instantly with the follow-on turn
// still running.
func rafcapHasResultCount(n int) func([]dropcapCaptured) bool {
	return func(lines []dropcapCaptured) bool {
		count := 0
		for _, c := range lines {
			if c.Decoded && c.Type == "result" {
				count++
			}
		}
		return count >= n
	}
}

// rafcapMark is the number of lines claude had emitted when it was called — the phase
// boundary rafcapPhaseFor attributes rosters by.
//
// Taken from the last captured line's Index+1 rather than from len(lines), because
// lines dropped at the capture cap are counted but not retained and a length would
// silently slide every later mark backwards.
func rafcapMark(recorder *dropcapRecorder) int {
	lines, _ := recorder.snapshot()
	if len(lines) == 0 {
		return 0
	}
	return lines[len(lines)-1].Index + 1
}

// --- the live capture --------------------------------------------------------

// TestRealClaude_RosterAfterFinishCapture drives the turns and writes the record.
//
// Ordering below is load-bearing in three places, and each wrong order fails SILENTLY
// rather than loudly:
//
//   - newDropcapScanner reads CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY via
//     os.Getenv AS DENY NEEDLES, and WithWorktreeAuthenticated is what re-pins them into
//     this process's environment. Building the scanner first yields an EMPTY needle,
//     which dropcapScanner.scan reports as notApplied — skipped, not fatal. The
//     credential net would be off while every message read green.
//   - The record-writing cleanup is registered FIRST in this body, so t.Cleanup's LIFO
//     runs it LAST and a structural t.Fatalf still leaves the evidence on disk.
//   - The FIFO hold is registered BEFORE the runner, so its release cleanup runs AFTER
//     the runner's cancel — the descendant reap kills the backgrounded `cat`, and
//     closing the last write end is the backstop if the reap missed.
func TestRealClaude_RosterAfterFinishCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE, AND THAT IS A DELIBERATE BREAK FROM THE
	// ENV-GATED PROBES IN THIS PACKAGE, copied from #2089's and #2247's.
	//
	// Under the env-gated shape `make e2e-realclaude` never sets the variable, so the
	// probe skips on the ENV check before it ever reaches the credential check, the live
	// gate passes vacuously, and the fixture never lands. That is CLAUDE.md § Testing's
	// #1763 failure exactly: a green gate and a spent budget look identical whether the
	// bytes landed or not. Three of this ticket's five acceptance criteria turn on the
	// fixture existing.
	//
	// So the probe ARMS ITSELF while the fixture is absent and DISARMS once it exists.
	// The env var survives as a FORCE, for re-capturing at a new claude version.
	force := os.Getenv(rafcapEnableEnv) == "1"
	if _, err := os.Stat(rafcapFixturePath); err == nil && !force {
		t.Skipf("#2525 roster-after-finish capture: the fixture %s already exists, so there is nothing "+
			"to capture and this costs no claude turn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 20m -v \\\n"+
			"    -run '^TestRealClaude_RosterAfterFinishCapture$' ./internal/e2e/realclaude/",
			rafcapFixturePath, rafcapEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Deliberately NOT t.TempDir(): the operator needs the record after the test ends in
	// order to commit it as the fixture, and this directory is also the ONLY copy that
	// survives a run in a worktree the dispatcher discards.
	artifactDir, err := os.MkdirTemp("", rafcapArtifactPrefix)
	if err != nil {
		t.Fatalf("#2525: create artifact dir: %v", err)
	}

	// A fresh EMPTY directory, deliberately not a git repo: no branch names and no file
	// contents can reach a payload.
	workdir := filepath.Join(home, rafcapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2525: create workdir: %v", err)
	}
	nonce := time.Now().UnixNano()
	fifoPath := filepath.Join(workdir, rafcapFIFOName)

	red := newDropcapRedactor(home, artifactDir, workdir, fifoPath, rafcapSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2525 capture artifacts: %s", red.str(artifactDir))

	rec := rafcapSeedRecord()
	rec.ClaudeVersion = probeClaudeVersion(claudeBin)
	rec.CapturedAt = time.Now().Format(time.RFC3339)
	rec.Workdir = red.str(workdir)
	rec.CredentialScanApplied = scanner.applied()
	rec.set(rafcapOutcomeInstrumentBroke, "did not reach a classification point")

	t.Cleanup(func() { rafcapWriteRecord(t, artifactDir, red, scanner, rec) })

	// MUST precede the runner: Config.Env stays nil so the child inherits this process's
	// environment verbatim.
	t.Setenv(dropcapBashTimeoutEnv, rafcapBashTimeoutMS)

	rendezvous, releaseFIFO := tpcapHoldFIFO(t, fifoPath)

	recorder := newDropcapRecorder()
	timeline := newRafcapTimeline()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: rafcapSessionID,
		Args:      rafcapArgs,
		Stdout:    recorder,
		Logger:    slog.New(argvHandler),
		// THE DELTA FROM #2247's RIG. This is production's per-spawn ask — the
		// interactive daemon's config mapping is the only thing that sets it — and it
		// lands BEFORE the child's first turn, which is the one ask position in this repo
		// never followed by a roster line. Staging it is what lets AC2 report separately
		// on that position rather than inferring it from the mid-session one.
		RequestInitializeOnSpawn: true,
	})
	if err != nil {
		rec.set(rafcapOutcomeInstrumentBroke, "streamsup.New failed, so no claude was ever spawned: %v",
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
		case <-time.After(rafcapRunExitWait):
			t.Errorf("#2525: streamsup.Run did not return within %s of cancel", rafcapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(rafcapOutcomeInstrumentBroke, "no live child within %s: claude never spawned, so nothing "+
			"was on the wire to capture", dropcapSpawnWait)
		return
	}

	// Phase 0 — the per-spawn ask's own reply, written by the runner before the first
	// turn. Recorded rather than waited on as a precondition: its ABSENCE is a finding
	// of its own (AC2 asks separately whether that response was followed by a roster),
	// and refusing to proceed without it would throw away the other two prompts.
	rec.PerSpawnAskAnswered = rafcapAwait(recorder, timeline, rafcapInitializeWait,
		rafcapHasControlResponseAfter(0))

	turnStart := time.Now()
	turnBudget := time.NewTimer(rafcapTurnBudget)
	defer turnBudget.Stop()

	prompt := bgIdlePrompt(fifoPath, nonce)
	rec.Prompts = red.strs([]string{prompt, rafcapFollowOnPrompt})
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.set(rafcapOutcomeInstrumentBroke, "writing the first turn envelope failed, so no turn was "+
			"ever driven: %v", red.str(err.Error()))
		return
	}

	// Phase 1 — wait for claude's `cat` to open the FIFO. Nothing else in this rig opens
	// it, so that open is unambiguous evidence a Bash call started.
	var heldFrom time.Time
	select {
	case <-rendezvous:
		rec.ForegroundCallObserved = true
		heldFrom = time.Now()
	case <-recorder.resultSeen:
	case <-time.After(rafcapRendezvousWait):
	}

	// Phase 2 — wait for claude to background the timed-out call. This is the task that
	// has to EXIST before it can complete, and its absence is a staging failure rather
	// than a finding.
	if rec.ForegroundCallObserved {
		rec.BackgroundTaskObserved = rafcapAwait(recorder, timeline, rafcapBackgroundWait,
			rafcapHasSubtype(rafcapStartedSubtype))
	}

	// Phase 3 — RELEASE. `cat` sees EOF and the background task completes.
	releaseFIFO()
	// Left at zero when the rendezvous never fired: nothing was ever held open, and a
	// duration measured from before the wait would read as a hold that happened.
	if rec.ForegroundCallObserved {
		rec.HeldSeconds = time.Since(heldFrom).Seconds()
	}

	// Phase 4 — the terminal-status line, from EITHER candidate subtype. Which one it
	// was is AC3 and is read off the frames below.
	rec.TerminalStatusObserved = rafcapAwait(recorder, timeline, rafcapTerminalWait,
		rafcapHasTerminalStatus)
	rec.markTerminal = rafcapMark(recorder)

	// Phase 5 — THE QUIET WINDOW. A floor, not a deadline: the predicate never
	// succeeds, so the wait runs its full duration with nothing asked of claude. That is
	// what licenses the record to say a roster did not arrive UNPROMPTED, and it must
	// precede the mid-session ask so an unprompted line cannot be mistaken for a
	// prompted one.
	quietFrom := time.Now()
	rafcapAwait(recorder, timeline, rafcapQuietWindow, func([]dropcapCaptured) bool { return false })
	rec.QuietWindowSeconds = time.Since(quietFrom).Seconds()
	rec.markQuietEnd = rafcapMark(recorder)

	// Phase 6 — one MID-SESSION initialize, the ask position every roster in this repo
	// follows. RequestInitialize rather than a second config field, for
	// interactive_stream_inband_model_test.go's reason: the config field fires once per
	// SPAWN and this ask has to land now, mid-session, after a completion.
	if err := runner.RequestInitialize(); err != nil {
		// Recorded, not fatal. The quiet window has already run and the follow-on turn
		// still can, so two of the three prompts survive an undeliverable ask.
		rec.set(rafcapOutcomeStagingFailed, "the mid-session initialize could not be delivered: %v",
			red.str(err.Error()))
	} else {
		rec.MidSessionAskSent = true
		rec.MidSessionAskAnswered = rafcapAwait(recorder, timeline, rafcapInitializeWait,
			rafcapHasControlResponseAfter(rec.markQuietEnd))
	}
	rec.markMidSession = rafcapMark(recorder)

	// Phase 7 — one further ordinary turn, read through its own `result`. claude may
	// batch roster changes to a turn boundary, so an absence that stops at the terminal
	// status is an absence only WITHIN one turn. The wait STOPS as soon as the second
	// result is read; nothing here burns a remaining budget.
	if err := streamsup.WriteTurn(ctx, stdin, []byte(rafcapFollowOnPrompt)); err != nil {
		rec.set(rafcapOutcomeStagingFailed, "writing the follow-on turn envelope failed: %v",
			red.str(err.Error()))
	} else {
		rec.FollowOnTurnObserved = rafcapAwait(recorder, timeline, rafcapFollowOnTurnWait,
			rafcapHasResultCount(2))
	}

	// `result` may well have landed during the waits above, and by then the budget may
	// have expired too. A single select over both would find both arms ready and pick at
	// random, so the already-closed resultSeen is checked first, non-blocking.
	select {
	case <-recorder.resultSeen:
		rec.TerminatedOn = rafcapTerminatedResult
	default:
		select {
		case <-recorder.resultSeen:
			rec.TerminatedOn = rafcapTerminatedResult
		case <-turnBudget.C:
			rec.TerminatedOn = rafcapTerminatedBudget
		}
	}
	rec.TurnSeconds = time.Since(turnStart).Seconds()

	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	timeline.note(lines, time.Now())
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	// tpcapCensus is #2089's and is CALLED rather than re-derived; the tpcap prefix
	// marks the file it was minted in, not private scope. See this file's header.
	rec.LineTypeCensus, rec.ToolCalls, rec.ToolResultErrors, rec.UndecodedLines = tpcapCensus(lines, red)
	rafcapCollect(t, rec, lines, timeline, red)

	rec.Verdict, rec.VerdictPrompt = rafcapDecideVerdict(rec.TerminalStatusObserved, rec.Rosters)
	rec.UnredactedPathFields = rafcapUnredactedPathFields(rec.Frames)
	if rec.Outcome != rafcapOutcomeStagingFailed {
		if _, worthy := rec.fixtureWorthy(); worthy {
			rec.set(rafcapOutcomeMeasured, "verdict=%s prompt=%q over %d roster line(s); %s",
				rec.Verdict, rec.VerdictPrompt, len(rec.Rosters), rec.stagingVerdict())
		} else {
			rec.set(rafcapOutcomeStagingFailed, "%s", rec.stagingVerdict())
		}
	}

	// --- AC4 ------------------------------------------------------------------
	// Counts, indices, censuses and field NAMES only. The frames themselves are in the
	// record the cleanup has already written; putting claude's bytes in CI output is
	// precisely the exposure the deny-scan exists to prevent.
	//
	// A ZERO-ROSTER RECORD IS NOT A FAILURE HERE, and that is the whole difference from
	// #2247's fatal. An absence after a completion is this ticket's most likely real
	// answer and the thing two client tickets are waiting on. What DOES fail is a
	// staging that never produced a completion to observe an absence after.
	if reason, worthy := rec.fixtureWorthy(); !worthy {
		t.Fatalf("#2525: the capture is not promotable — %s\n"+
			"  (terminated_on=%s, turn=%.1fs, captured=%d lines, verdict=%s)\n"+
			"  staging: fg_call=%v bg_task=%v held=%.1fs terminal=%v quiet=%.1fs mid_ask=%v/%v "+
			"follow_on=%v per_spawn_ask=%v\n"+
			"  line types: %v; tools called: %v; tool_result errors: %d; undecoded: %d\n"+
			"Read the staging line FIRST: only its last case is a finding about claude's surface, and "+
			"the others are the rig failing to background a command and let it finish. An absence of "+
			"roster lines is NOT one of the refusals — it is the expected answer and it promotes.",
			reason, rec.TerminatedOn, rec.TurnSeconds, rec.LinesCaptured, rec.Verdict,
			rec.ForegroundCallObserved, rec.BackgroundTaskObserved, rec.HeldSeconds,
			rec.TerminalStatusObserved, rec.QuietWindowSeconds, rec.MidSessionAskSent,
			rec.MidSessionAskAnswered, rec.FollowOnTurnObserved, rec.PerSpawnAskAnswered,
			rec.LineTypeCensus, rec.ToolCalls, rec.ToolResultErrors, rec.UndecodedLines)
	}
}

// rafcapCollect fills every measured field that is read off the captured lines: the
// kept frames, the roster observations in order, the control-response envelopes, the
// AC3 status table and the per-spawn ask's own answer.
//
// The filter is deliberately the TYPE and SUBTYPE rather than the parser's verdict:
// filtering on "dropped" would discard a subtype the parser had started mapping, and
// the verdict rides along per frame as data instead.
func rafcapCollect(t *testing.T, rec *rafcapRecord, lines []dropcapCaptured, tl *rafcapTimeline,
	red *dropcapRedactor) {
	t.Helper()

	kept := map[string]bool{}
	for _, s := range rafcapKeptSubtypes {
		kept[s] = true
	}

	terminalIndex, terminalSubtype, terminalToken, found := rafcapFindTerminal(lines)
	if found {
		rec.TerminalStatusIndex = terminalIndex
		rec.TerminalStatusSubtype = terminalSubtype
		rec.TerminalStatusToken = terminalToken
		_, rec.TerminalStatusLocation = rafcapStatusOf(rafcapRawAt(lines, terminalIndex))
	}

	// The finished task's id, off claude's OWN task_started line. It is the join key
	// for "does this roster still list the task that ended", and it is claude's value
	// rather than one this rig minted.
	var finishedID string
	for _, c := range lines {
		if c.Decoded && c.Type == "system" && c.Subtype == rafcapStartedSubtype {
			var obj map[string]json.RawMessage
			if json.Unmarshal(c.Raw, &obj) == nil {
				if id, ok := rafcapStringField(obj, "task_id"); ok {
					finishedID = id
					rec.FinishedTaskIDKnown = true
					break
				}
			}
		}
	}

	results := 0
	prevType := ""
	for _, c := range lines {
		if c.Decoded && c.Type == "control_response" {
			requestID, subtype := rafcapControlOf(c.Raw)
			// NO PAYLOAD. See this file's header and rafcapRedactionRationale: the
			// reply is the operator's configuration inventory, and AC2 needs only where
			// it sits in the stream. request_id and subtype are claude's own protocol
			// vocabulary, not the operator's data.
			rec.ControlResponses = append(rec.ControlResponses, rafcapControlObs{
				Index:     c.Index,
				RequestID: red.str(requestID),
				Subtype:   subtype,
				Phase:     rec.rafcapPhaseFor(c.Index),
			})
			if rec.PerSpawnControlResponseIndex < 0 {
				rec.PerSpawnControlResponseIndex = c.Index
			}
		}
		if c.Decoded && c.Type == "result" {
			results++
		}
		if !c.Decoded || c.Type != "system" || !kept[c.Subtype] {
			if c.Decoded {
				prevType = c.Type
			}
			continue
		}

		// The payload half, shared with #1260 so the base64 arm for invalid UTF-8 is not
		// re-derived here. The reason field it wants has no use in this record, so it is
		// passed empty and dropped.
		entry := dropcapMakeEntry(c, "", red)
		frame := rafcapFrame{
			Index:                   entry.Index,
			Type:                    entry.Type,
			Subtype:                 entry.Subtype,
			PayloadLenBytesCaptured: entry.PayloadLenBytesCaptured,
			PayloadLenBytes:         entry.PayloadLenBytes,
			PayloadEncoding:         entry.PayloadEncoding,
			Payload:                 entry.Payload,
			PayloadB64:              entry.PayloadB64,
			EventsEmitted:           len(parseOne(t, string(c.Raw))),
		}
		rec.Frames = append(rec.Frames, frame)

		if c.Subtype == rafcapRosterSubtype {
			// Read out of the REDACTED payload rather than out of c.Raw, so the tasks
			// array the record carries is the array the record describes.
			tasks, ids, ok := rafcapRosterTasks([]byte(entry.Payload))
			obs := rafcapRosterObs{
				Index:                c.Index,
				Tasks:                tasks,
				TasksDecoded:         ok,
				TaskCount:            len(ids),
				OffsetLines:          c.Index - rec.TerminalStatusIndex,
				AfterControlResponse: prevType == "control_response",
				PrecedingLineType:    prevType,
				ResultsBefore:        results,
				Phase:                rec.rafcapPhaseFor(c.Index),
				Frame:                frame,
			}
			if !found {
				// With no terminal status there is nothing to offset against, and a
				// difference against -1 would read as a real distance.
				obs.OffsetLines = 0
			} else if secs, both := tl.offset(terminalIndex, c.Index); both {
				obs.OffsetSeconds = secs
			}
			for _, id := range ids {
				if finishedID != "" && id == finishedID {
					obs.ListsFinishedTask = true
				}
			}
			rec.Rosters = append(rec.Rosters, obs)
			if rec.PerSpawnControlResponseIndex >= 0 &&
				c.Index > rec.PerSpawnControlResponseIndex && prevType == "control_response" &&
				rec.rafcapPhaseFor(c.Index) == rafcapPhasePreTerminal {
				rec.PerSpawnAskFollowedByRoster = true
			}
		}
		prevType = c.Type
	}

	rec.StatusCandidates = rafcapStatusTable(rec.Frames)
}

// rafcapRawAt returns the raw bytes of the line with this index, or nil.
func rafcapRawAt(lines []dropcapCaptured, index int) []byte {
	for _, c := range lines {
		if c.Index == index {
			return c.Raw
		}
	}
	return nil
}

// --- writing the record ------------------------------------------------------

// rafcapSeal marshals the record as it stands and deny-scans the exact bytes it just
// produced, plus the decoded bytes of every base64 frame payload — those hide from a
// scan of the marshalled record, where they sit as base64.
//
// Split out of rafcapWriteRecord's seal closure so the property that matters can be
// asserted offline, with no live turn and no write anywhere near the repo. It returns
// the classes hit rather than deciding anything: the caller owns the fail-closed
// response, and the caller is the only place that knows what was about to be written.
func rafcapSeal(scanner dropcapScanner, rec *rafcapRecord) (blob []byte, hits []string, err error) {
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

// rafcapWriteRecord marshals, deny-scans, writes, promotes and stages. #1260's
// fail-closed rule verbatim: on a hit NOTHING is written and the message names the
// CLASS only, never the matched value.
func rafcapWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner,
	rec *rafcapRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()

	// credential_scan_skipped is a list of CLASS NAMES whose needle was too short to
	// search for, which is the one thing that makes a silently-off credential net
	// visible after the fact. It is a property of the needle SET rather than of any
	// blob, so it is read once here and ships inside every blob sealed below.
	_, notApplied := scanner.scan(nil)
	rec.CredentialScanSkipped = notApplied

	// seal marshals the record AS IT STANDS and deny-scans the EXACT bytes about to be
	// written, fail-closed. It is called before every write rather than once at the top,
	// and that is the whole point of it: fields enter the record BETWEEN the writes —
	// fixture_stage_detail carries `git`'s combined output, and git prints repository
	// paths on failure, a path in nobody's substitution table.
	seal := func(what string) []byte {
		t.Helper()
		blob, hits, err := rafcapSeal(scanner, rec)
		if err != nil {
			t.Fatalf("#2525: seal the %s: %v", what, err)
		}
		if len(hits) > 0 {
			t.Fatalf("#2525: deny-scan found %d denied class(es) in the %s about to be written: %v\n"+
				"THAT FILE WAS NOT WRITTEN, and neither is anything after it. Whatever this run had "+
				"already put on disk was sealed by this same scan before it was written, so nothing "+
				"unscanned is on disk. Extend dropcapRedactor's table with the named class and re-run the "+
				"capture. The offending value is deliberately not printed: putting it in CI output is "+
				"exactly the exposure this scan exists to prevent", len(hits), what, hits)
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

	// AC4's unconditional half. This write happens on EVERY run, promotable or not, and
	// into a directory outside every worktree — it is the copy that survives a run the
	// dispatcher discards, and a refused capture is evidence worth keeping.
	path := filepath.Join(dir, rafcapRecordName)
	blob := seal("record")
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2525: write record %s: %v", red.str(path), err)
		return
	}
	t.Logf("#2525 outcome=%s verdict=%s prompt=%q terminated_on=%s turn=%.1fs fg=%v bg=%v held=%.1fs "+
		"terminal=%s/%s@%s quiet=%.1fs mid_ask=%v/%v follow_on=%v per_spawn=%v/%v captured=%d rosters=%d "+
		"frames=%d control_responses=%d scan_not_applied=%v\n  record: %s\n  %s",
		rec.Outcome, rec.Verdict, rec.VerdictPrompt, rec.TerminatedOn, rec.TurnSeconds,
		rec.ForegroundCallObserved, rec.BackgroundTaskObserved, rec.HeldSeconds, rec.TerminalStatusSubtype,
		rec.TerminalStatusToken, rec.TerminalStatusLocation, rec.QuietWindowSeconds, rec.MidSessionAskSent,
		rec.MidSessionAskAnswered, rec.FollowOnTurnObserved, rec.PerSpawnAskAnswered,
		rec.PerSpawnAskFollowedByRoster, rec.LinesCaptured, len(rec.Rosters), len(rec.Frames),
		len(rec.ControlResponses), notApplied, red.str(path), red.str(rec.OutcomeDetail))

	if !worthy {
		t.Logf("#2525: NOT promoted to %s — %s. The record above is the evidence; read it, then re-run "+
			"or route the finding back", rafcapFixturePath, reason)
		return
	}
	// The same sealed bytes, promoted in-repo so the run that produced them is the run
	// that lands them. A capture that still needs a human to copy a file out of a
	// tempdir is a capture #1763 says will not land.
	if err := os.WriteFile(rafcapFixturePath, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2525: write fixture %s: %v", rafcapFixturePath, red.str(err.Error()))
		return
	}

	// Staging runs only now that the file exists. `git add` on a path that is not yet on
	// disk fails with "pathspec did not match any files" — and the fixture being absent
	// is precisely the first-capture case this probe arms itself for.
	rec.FixtureStaged, rec.FixtureStageDetail = rafcapStageFixture(red)
	// Re-seal so git's output is deny-scanned before it becomes part of any artefact,
	// and rewrite the record — the artifact-dir copy is where that evidence lives.
	if err := os.WriteFile(path, append(seal("record's staging outcome"), '\n'), 0o600); err != nil {
		t.Errorf("#2525: rewrite record %s with the staging outcome: %v", red.str(path), err)
		return
	}
	t.Logf("#2525: FIXTURE WRITTEN to %s (verdict %s). git add: staged=%v — %s\n"+
		"  COMMIT IT, and in the SAME COMMIT:\n"+
		"    1. fill internal/streamsup/roster_after_finish_capture_test.go with exactly:\n"+
		"         var rosterAfterFinishPinnedVerdict = %q\n"+
		"    2. add one sentence to emitBackgroundTaskRoster's doc comment naming claude %s and this "+
		"verdict, TRANSCRIBED FROM THE RECORD and never from what this probe expected to see;\n"+
		"    3. cross-post the same finding on pyrycode/pyrycode-desktop#1246 and #1558, which currently "+
		"assume opposite answers — and if the verdict turned on the ask position, say so there.\n"+
		"  Bytes that land with an empty pin FATAL that reader rather than passing quietly, which is this "+
		"ticket's AC5 and the deterministic net behind #2229's lost capture. If the staging above ran in "+
		"a worktree that gets discarded, the record at the artifact path logged earlier is the copy that "+
		"survives.\n"+
		"  A run reaching this line at all means the fixture was absent or %s=1 forced a re-capture, so "+
		"this is a NEW claude release or a suspected shape change: re-read the verdict before trusting "+
		"the old one",
		rafcapFixturePath, rec.Verdict, rec.FixtureStaged, rec.FixtureStageDetail, rec.Verdict,
		rec.ClaudeVersion, rafcapEnableEnv)
}

// rafcapStageFixture runs `git add` on the fixture the caller has just written, so the
// run that produces the artefact is the run that stages it rather than leaving a human
// to carry a file out of a tempdir.
//
// Fixed argv, no shell, and the one argument is a compile-time constant with `--` ahead
// of it so it can never be read as a flag; nothing claude emitted reaches it. The
// combined output goes through the redactor because git prints repository paths on
// failure.
//
// BEST-EFFORT AND NEVER FATAL. git being absent, or the working tree being somewhere
// staging means nothing, must not throw away a capture that cost a live turn — the
// bytes are already in the working tree and in the artifact directory by this point.
func rafcapStageFixture(red *dropcapRedactor) (bool, string) {
	out, err := exec.Command("git", "add", "--", rafcapFixturePath).CombinedOutput()
	if err != nil {
		return false, red.str(fmt.Sprintf("git add failed (%v): %s. The fixture is written in-repo "+
			"regardless; stage and commit it by hand", err, strings.TrimSpace(string(out))))
	}
	return true, "staged with `git add`. A run in a worktree the dispatcher discards stages into an " +
		"index that goes with it, so the artifact-dir record remains the copy that survives"
}

// --- offline self-checks -----------------------------------------------------

// TestRafcapFixtureWorthyRefusesEveryBadCapture runs offline. fixtureWorthy is the only
// thing standing between a live run and a committed fixture, and each refusing arm
// below is a capture that would look green from outside — the record is written, the
// deny-scan passed, the log is cheerful — while proving nothing, proving something
// about the wrong claude, or carrying a host path into a public artefact.
//
// THE PROMOTING ARMS ARE THE POINT OF THIS TABLE. This probe's likeliest real answer is
// an ABSENCE, so a promotion rule keyed on a roster having fired would refuse the
// ticket's own expected result. The second arm is that case, and it must promote.
func TestRafcapFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	good := func() *rafcapRecord {
		return &rafcapRecord{
			ClaudeVersion:          rafcapFixtureVersion + " (Claude Code)",
			ForegroundCallObserved: true,
			BackgroundTaskObserved: true,
			TerminalStatusObserved: true,
			TerminalStatusSubtype:  rafcapNotificationSubtype,
			TerminalStatusToken:    "completed",
			QuietWindowSeconds:     rafcapQuietWindow.Seconds(),
			MidSessionAskSent:      true,
			MidSessionAskAnswered:  true,
			FollowOnTurnObserved:   true,
			Verdict:                rafcapVerdictNone,
			Frames: []rafcapFrame{
				{Index: 0, Subtype: rafcapStartedSubtype, PayloadEncoding: dropcapEncodingJSONString},
				{Index: 1, Subtype: rafcapNotificationSubtype, PayloadEncoding: dropcapEncodingJSONString},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*rafcapRecord)
		want   bool
	}{
		{"a good capture is promoted", func(*rafcapRecord) {}, true},
		{
			// THE ARM THIS TICKET'S PROMOTION RULE TURNS ON.
			"an absence after the terminal status is promoted, not refused as vacuous",
			func(r *rafcapRecord) { r.Verdict = rafcapVerdictNone; r.Rosters = nil },
			true,
		},
		{
			// A roster-bearing verdict is equally promotable; the rule is keyed on the
			// staging, not on which answer came back.
			"a roster that still lists the finished task is promoted",
			func(r *rafcapRecord) { r.Verdict = rafcapVerdictStillLists },
			true,
		},
		{
			// Recorded, not refused: two of the three prompts still ran, and discarding
			// a live turn over the third would throw their evidence away.
			"a mid-session ask that went unanswered is still promoted",
			func(r *rafcapRecord) { r.MidSessionAskAnswered = false },
			true,
		},
		{"bare version string, no suffix", func(r *rafcapRecord) { r.ClaudeVersion = rafcapFixtureVersion }, true},
		{"the command never ran", func(r *rafcapRecord) { r.ForegroundCallObserved = false }, false},
		{"it ran but was never backgrounded", func(r *rafcapRecord) { r.BackgroundTaskObserved = false }, false},
		{"the task never reached a terminal status", func(r *rafcapRecord) { r.TerminalStatusObserved = false }, false},
		{"no claim", func(r *rafcapRecord) { r.Verdict = rafcapVerdictNoClaim }, false},
		{
			"the quiet window was short of its floor",
			func(r *rafcapRecord) { r.QuietWindowSeconds = rafcapQuietWindowFloor.Seconds() - 1 },
			false,
		},
		{
			// An absence that stopped at the terminal status is an absence only WITHIN
			// one turn, and claude batching to a turn boundary is what that read rules out.
			"the follow-on turn never ended",
			func(r *rafcapRecord) { r.FollowOnTurnObserved = false },
			false,
		},
		{"a different claude release", func(r *rafcapRecord) { r.ClaudeVersion = "2.1.300 (Claude Code)" }, false},
		{"version unreadable", func(r *rafcapRecord) { r.ClaudeVersion = "<unavailable: exec failed>" }, false},
		{"version absent", func(r *rafcapRecord) { r.ClaudeVersion = "" }, false},
		{
			// The one shape this side would otherwise promote and the reading side
			// refuses: a frame that was not valid UTF-8 is recorded base64 with an empty
			// payload.
			"a base64 frame the consumer cannot read",
			func(r *rafcapRecord) { r.Frames[1].PayloadEncoding = dropcapEncodingBase64 },
			false,
		},
		{
			"an unredacted host path in a kept payload",
			func(r *rafcapRecord) { r.UnredactedPathFields = []string{"output_file"} },
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

// TestRafcapDecideVerdictReadsTheClosedSet runs offline. The verdict is the one word
// two desktop tickets will be told, and every arm below is a shape a real capture can
// produce.
func TestRafcapDecideVerdictReadsTheClosedSet(t *testing.T) {
	t.Parallel()
	roster := func(offset int, lists bool, phase string) rafcapRosterObs {
		return rafcapRosterObs{OffsetLines: offset, ListsFinishedTask: lists, Phase: phase}
	}
	tests := []struct {
		name       string
		terminal   bool
		rosters    []rafcapRosterObs
		want       string
		wantPrompt string
	}{
		{"the task never completed", false, nil, rafcapVerdictNoClaim, ""},
		{
			"no terminal status but rosters anyway is still no claim",
			false, []rafcapRosterObs{roster(4, false, rafcapPhaseQuietWindow)}, rafcapVerdictNoClaim, "",
		},
		{"nothing after the terminal status", true, nil, rafcapVerdictNone, ""},
		{
			// The 2.1.220 shape: a roster at the task's START says nothing about a
			// completion, and counting it would read a verdict off the wrong line.
			"only a pre-terminal roster",
			true, []rafcapRosterObs{roster(-9, true, rafcapPhasePreTerminal)}, rafcapVerdictNone, "",
		},
		{
			"a roster on the terminal line itself is not after it",
			true, []rafcapRosterObs{roster(0, true, rafcapPhasePreTerminal)}, rafcapVerdictNone, "",
		},
		{
			"one that still lists the finished task",
			true, []rafcapRosterObs{roster(4, true, rafcapPhaseQuietWindow)},
			rafcapVerdictStillLists, rafcapPhaseQuietWindow,
		},
		{
			"one that omits it",
			true, []rafcapRosterObs{roster(4, false, rafcapPhaseMidSessionAsk)},
			rafcapVerdictOmits, rafcapPhaseMidSessionAsk,
		},
		{
			// THE LAST POST-TERMINAL ROSTER DECIDES: a line that still listed the task
			// followed by one that omits it means the count DID come down, which is the
			// state a client is left holding.
			"the last one decides",
			true, []rafcapRosterObs{
				roster(4, true, rafcapPhaseQuietWindow),
				roster(61, false, rafcapPhaseFollowOnTurn),
			},
			rafcapVerdictOmits, rafcapPhaseFollowOnTurn,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, prompt := rafcapDecideVerdict(tc.terminal, tc.rosters)
			if got != tc.want || prompt != tc.wantPrompt {
				t.Errorf("rafcapDecideVerdict() = (%q, %q), want (%q, %q)", got, prompt, tc.want, tc.wantPrompt)
			}
		})
	}
}

// TestRafcapPhaseForNamesThePromptThatPrecededIt runs offline. "The roster answers a
// mid-session control request only" is a different instruction to a client than "claude
// sends it unprompted", and this arithmetic is the only thing that tells them apart.
func TestRafcapPhaseForNamesThePromptThatPrecededIt(t *testing.T) {
	t.Parallel()
	rec := &rafcapRecord{markTerminal: 20, markQuietEnd: 30, markMidSession: 40}
	for _, tc := range []struct {
		index int
		want  string
	}{
		{0, rafcapPhasePreTerminal},
		{19, rafcapPhasePreTerminal},
		{20, rafcapPhasePreTerminal},
		{21, rafcapPhaseQuietWindow},
		{29, rafcapPhaseQuietWindow},
		{30, rafcapPhaseMidSessionAsk},
		{39, rafcapPhaseMidSessionAsk},
		{40, rafcapPhaseFollowOnTurn},
		{99, rafcapPhaseFollowOnTurn},
	} {
		if got := rec.rafcapPhaseFor(tc.index); got != tc.want {
			t.Errorf("rafcapPhaseFor(%d) = %q, want %q", tc.index, got, tc.want)
		}
	}
}

// TestRafcapStatusOfFindsItInPatchAndAtTopLevel runs offline and is AC3's mechanism.
//
// Both locations are measured shapes, not defensive coding: 2.1.259's task_notification
// carried a top-level status, and 2.1.220's task_updated carried its change inside
// `patch`. A reader of only one would record a terminal status as absent on half the
// shapes claude has been seen to send — and the quiet window would then start at the
// wrong moment or not at all.
func TestRafcapStatusOfFindsItInPatchAndAtTopLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		payload        string
		wantToken      string
		wantLocation   string
		wantIsTerminal bool
	}{
		{
			name:           "the 2.1.259 task_notification shape",
			payload:        `{"type":"system","subtype":"task_notification","status":"completed","output_file":""}`,
			wantToken:      "completed",
			wantLocation:   "top-level",
			wantIsTerminal: true,
		},
		{
			name:           "a status inside patch, the 2.1.220 task_updated shape",
			payload:        `{"type":"system","subtype":"task_updated","task_id":"x","patch":{"status":"completed"}}`,
			wantToken:      "completed",
			wantLocation:   "patch",
			wantIsTerminal: true,
		},
		{
			// The literal 2.1.220 patch: a real update carrying no status at all.
			name:    "the 2.1.220 patch carries no status",
			payload: `{"type":"system","subtype":"task_updated","task_id":"x","patch":{"is_backgrounded":true}}`,
		},
		{
			name:         "a non-terminal status is found but does not end the task",
			payload:      `{"status":"running"}`,
			wantToken:    "running",
			wantLocation: "top-level",
		},
		{
			// systemTaskUpdatedLine keeps patch as a raw value precisely because
			// "whatever claude puts there" is the point; a string patch is a shape change
			// to notice, not a decode to fail on.
			name:    "a patch that is a string, not an object",
			payload: `{"task_id":"x","patch":"is_backgrounded"}`,
		},
		{"no status anywhere", `{"task_id":"x"}`, "", "", false},
		{"a non-string status is not a token", `{"status":7}`, "", "", false},
		{"a line that does not decode reports nothing rather than guessing", `not json`, "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			token, location := rafcapStatusOf([]byte(tc.payload))
			if token != tc.wantToken || location != tc.wantLocation {
				t.Errorf("rafcapStatusOf() = (%q, %q), want (%q, %q)", token, location,
					tc.wantToken, tc.wantLocation)
			}
			if got := rafcapIsTerminal(token); got != tc.wantIsTerminal {
				t.Errorf("rafcapIsTerminal(%q) = %v, want %v", token, got, tc.wantIsTerminal)
			}
		})
	}
}

// TestRafcapRosterTasksKeepsTheArrayVerbatim runs offline. AC2 asks for the `tasks`
// array verbatim, and "verbatim" is load-bearing: a typed decode would silently drop
// every key this repo has not declared, which is the failure the family's
// declare-only-what-you-captured rule exists to prevent downstream.
func TestRafcapRosterTasksKeepsTheArrayVerbatim(t *testing.T) {
	t.Parallel()

	t.Run("the 2.1.220 roster shape, with an undeclared key kept", func(t *testing.T) {
		t.Parallel()
		const payload = `{"type":"system","subtype":"background_tasks_changed","tasks":` +
			`[{"task_id":"bybi8g8i8","task_type":"local_bash","description":"cat $FIFO","invented":1}]}`
		tasks, ids, ok := rafcapRosterTasks([]byte(payload))
		if !ok {
			t.Fatal("a well-formed roster reported tasks_decoded=false")
		}
		if len(ids) != 1 || ids[0] != "bybi8g8i8" {
			t.Errorf("ids = %v, want the one task_id claude sent", ids)
		}
		if !strings.Contains(string(tasks), `"invented":1`) {
			t.Errorf("the kept array dropped a key claude sent: %s", tasks)
		}
	})

	t.Run("an empty array is a roster, not an absence", func(t *testing.T) {
		t.Parallel()
		tasks, ids, ok := rafcapRosterTasks([]byte(`{"tasks":[]}`))
		if !ok || len(ids) != 0 || string(tasks) != "[]" {
			t.Errorf("rafcapRosterTasks() = (%s, %v, %v), want an empty but decoded array", tasks, ids, ok)
		}
	})

	t.Run("a missing or unreadable tasks key says so", func(t *testing.T) {
		t.Parallel()
		for _, payload := range []string{`{"type":"system"}`, `not json`, `{"tasks":"nope"}`} {
			if _, _, ok := rafcapRosterTasks([]byte(payload)); ok {
				t.Errorf("payload %q reported tasks_decoded=true; a shape change must be recorded, not "+
					"reported as an empty roster", payload)
			}
		}
	})
}

// TestRafcapControlResponsesKeepNoPayload runs offline and pins this file's one
// deliberate security decision as a PROPERTY OF THE RECORD rather than a habit of its
// author.
//
// An initialize reply is the operator's local claude configuration inventory — the
// model menu, the tool and MCP server names, cwd, apiKeySource. AC2 needs where that
// reply sits in the stream and not what is in it, so the record keeps the envelope and
// discards the payload. The last assertion is the one that matters: the marshalled
// record must not contain the reply's bytes anywhere.
func TestRafcapControlResponsesKeepNoPayload(t *testing.T) {
	t.Parallel()
	const secret = "my-private-mcp-server"
	payload := `{"type":"control_response","response":{"subtype":"success","request_id":"2",` +
		`"response":{"mcp_servers":[{"name":"` + secret + `"}],"cwd":"/somewhere/private"}}}`

	requestID, subtype := rafcapControlOf([]byte(payload))
	if requestID != "2" || subtype != "success" {
		t.Fatalf("rafcapControlOf() = (%q, %q), want the doubly-nested request_id and subtype",
			requestID, subtype)
	}

	rec := rafcapSeedRecord()
	rec.ControlResponses = append(rec.ControlResponses, rafcapControlObs{
		Index: 3, RequestID: requestID, Subtype: subtype, Phase: rafcapPhaseMidSessionAsk,
	})
	blob, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}
	for _, leak := range []string{secret, "mcp_servers", "/somewhere/private"} {
		if strings.Contains(string(blob), leak) {
			t.Errorf("the record carries %q from an initialize reply. That reply is the operator's "+
				"configuration inventory and this record keeps only the envelope — growing it a payload "+
				"field reintroduces a class this ticket removed rather than filtered", leak)
		}
	}
}

// TestRafcapUnredactedPathFieldsNamesTheFieldNotTheValue runs offline. It is the third
// redaction mechanism, inherited from #2247 because task_notification is documented to
// carry output_file — a path on the operator's host.
//
// The last assertion is what makes it worth having rather than decorative: the returned
// strings must never contain the path itself, because a refusal message printing the
// value would be the exposure the refusal exists to prevent.
func TestRafcapUnredactedPathFieldsNamesTheFieldNotTheValue(t *testing.T) {
	t.Parallel()
	frame := func(payload string) []rafcapFrame {
		return []rafcapFrame{{Index: 3, Subtype: rafcapNotificationSubtype, Payload: payload}}
	}
	tests := []struct {
		name  string
		items []rafcapFrame
		want  []string
	}{
		{"a redacted path is not a finding", frame(`{"output_file":"$TEMP_HOME/out.txt"}`), []string{}},
		{"the 2.1.259 empty output_file is not a finding", frame(`{"output_file":"","status":"completed"}`), []string{}},
		{"a bare host path names its field", frame(`{"output_file":"/var/x/out.txt"}`), []string{"output_file"}},
		{"a path nested inside an object is caught at depth", frame(`{"usage":{"log":"/tmp/501/z"}}`), []string{"usage.log"}},
		{
			// The roster's own shape: its description is the rig's command line, and a
			// path there that redaction did not know must still stop the promotion.
			name: "a path inside the roster's tasks array is caught",
			items: []rafcapFrame{{Index: 1, Subtype: rafcapRosterSubtype,
				Payload: `{"tasks":[{"task_id":"a","description":"/bin/cat /elsewhere/fifo"}]}`}},
			want: []string{"tasks[].description"},
		},
		{
			name:  "a payload that cannot be decoded cannot be swept, and says so",
			items: frame(`not json`),
			want:  []string{"frame 3: payload does not decode, so it could not be swept"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := rafcapUnredactedPathFields(tc.items)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("rafcapUnredactedPathFields() = %v, want %v", got, tc.want)
			}
			for _, f := range tc.items {
				for _, v := range []string{"/var/x/out.txt", "/tmp/501/z", "/elsewhere/fifo"} {
					if strings.Contains(f.Payload, v) && strings.Contains(strings.Join(got, "|"), v) {
						t.Errorf("the result %v carries the VALUE %q; it must name the field only", got, v)
					}
				}
			}
		})
	}
}

// TestRafcapStatusTableRecordsADidNotFire runs offline and is AC3's other half: a
// candidate subtype that did not fire must be RECORDED as not having fired, not
// omitted. An omitted subtype and one that fired carrying no status look identical to a
// later reader, and only one of those is a measurement.
func TestRafcapStatusTableRecordsADidNotFire(t *testing.T) {
	t.Parallel()

	t.Run("an empty turn still answers for both candidates", func(t *testing.T) {
		t.Parallel()
		got := rafcapStatusTable(nil)
		if len(got) != len(rafcapStatusCandidates) {
			t.Fatalf("got %d entries, want %d — a candidate with no entry is a question AC3 asked and "+
				"this record did not answer", len(got), len(rafcapStatusCandidates))
		}
		for _, obs := range got {
			if obs.Fired || obs.CarriedStatus || !strings.Contains(obs.Note, "did not fire") {
				t.Errorf("%s: %+v, want a recorded absence saying so", obs.Subtype, obs)
			}
		}
	})

	t.Run("the 2.1.259 split: notification carries it, updated never fired", func(t *testing.T) {
		t.Parallel()
		frames := []rafcapFrame{
			{Index: 6, Subtype: rafcapStartedSubtype, Payload: `{"task_id":"x"}`},
			{Index: 7, Subtype: rafcapNotificationSubtype, Payload: `{"task_id":"x","status":"completed"}`},
		}
		byName := map[string]rafcapStatusObs{}
		for _, obs := range rafcapStatusTable(frames) {
			byName[obs.Subtype] = obs
		}
		notif := byName[rafcapNotificationSubtype]
		if !notif.Fired || !notif.CarriedStatus || notif.FirstToken != "completed" ||
			notif.FirstLocation != "top-level" || notif.FirstTerminalAt != 7 {
			t.Errorf("%s = %+v, want the terminal status found at the top level of line 7",
				rafcapNotificationSubtype, notif)
		}
		if updated := byName[rafcapUpdatedSubtype]; updated.Fired {
			t.Errorf("%s = %+v, want a recorded absence", rafcapUpdatedSubtype, updated)
		}
	})

	t.Run("a subtype that fired without a status says exactly that", func(t *testing.T) {
		t.Parallel()
		frames := []rafcapFrame{{Index: 15, Subtype: rafcapUpdatedSubtype,
			Payload: `{"task_id":"x","patch":{"is_backgrounded":true}}`}}
		got := rafcapStatusTable(frames)[0]
		if !got.Fired || got.CarriedStatus || got.FirstTerminalAt != -1 {
			t.Errorf("%+v, want fired with no status and no terminal line", got)
		}
		if !strings.Contains(got.Note, "NO status") {
			t.Errorf("note %q does not distinguish a fired-without-status from an absence", got.Note)
		}
	})
}

// TestRafcapAwaitOutlivesTheTurn runs offline against a recorder fed by hand, and it is
// the deterministic net under the one assumption that decides whether this probe
// catches anything at all.
//
// Every wait here is for something that can outlive the turn it belongs to. resultSeen
// is closed via sync.Once, so a wait that took it as terminal would stay permanently
// ready: the whole wait would collapse to one snapshot() and the unspent time would
// then be reported as a finding about claude's surface. That is #2247's
// tncapResultIsNotTheEnd defect, and this rig declines to offer the mode at all.
func TestRafcapAwaitOutlivesTheTurn(t *testing.T) {
	t.Parallel()

	const resultLine = `{"type":"result","subtype":"success"}` + "\n"
	rosterLine := fmt.Sprintf("{\"type\":\"system\",\"subtype\":%q,\"tasks\":[]}\n", rafcapRosterSubtype)
	// Three poll ticks, so a wait that runs to its deadline is unmistakably longer than
	// one that returns early.
	const within = 3 * rafcapPoll

	t.Run("a wait spends its deadline although the turn has ended", func(t *testing.T) {
		t.Parallel()
		r := newDropcapRecorder()
		if _, err := r.Write([]byte(resultLine)); err != nil {
			t.Fatalf("feed the result line: %v", err)
		}
		<-r.resultSeen // the turn is over before the wait even starts

		start := time.Now()
		if rafcapAwait(r, newRafcapTimeline(), within, rafcapHasSubtype(rafcapRosterSubtype)) {
			t.Fatal("reported a roster present although no such line was ever fed")
		}
		if elapsed := time.Since(start); elapsed < within {
			t.Errorf("the wait returned after %s, short of its %s deadline: `result` was treated as "+
				"terminal, so a wait that has to outlive the turn was never spent and an absence nobody "+
				"waited for would be recorded as a finding", elapsed, within)
		}
	})

	t.Run("a wait finds a line that lands after the result", func(t *testing.T) {
		t.Parallel()
		r := newDropcapRecorder()
		if _, err := r.Write([]byte(resultLine)); err != nil {
			t.Fatalf("feed the result line: %v", err)
		}
		<-r.resultSeen
		go func() {
			time.Sleep(rafcapPoll)
			_, _ = r.Write([]byte(rosterLine))
		}()
		if !rafcapAwait(r, newRafcapTimeline(), 40*rafcapPoll, rafcapHasSubtype(rafcapRosterSubtype)) {
			t.Error("missed a roster line that arrived after the turn's result, which is the ONLY order " +
				"this ticket's staging can produce one in")
		}
	})

	t.Run("the follow-on turn's result is the SECOND one", func(t *testing.T) {
		t.Parallel()
		r := newDropcapRecorder()
		if _, err := r.Write([]byte(resultLine)); err != nil {
			t.Fatalf("feed the first result: %v", err)
		}
		<-r.resultSeen
		if rafcapAwait(r, newRafcapTimeline(), within, rafcapHasResultCount(2)) {
			t.Fatal("reported two results after one was fed: resultSeen cannot tell the two turns " +
				"apart, which is why this predicate counts instead")
		}
		if _, err := r.Write([]byte(resultLine)); err != nil {
			t.Fatalf("feed the second result: %v", err)
		}
		if !rafcapAwait(r, newRafcapTimeline(), within, rafcapHasResultCount(2)) {
			t.Error("missed the follow-on turn's own result")
		}
	})

	t.Run("a control_response is matched only at or past the mark", func(t *testing.T) {
		t.Parallel()
		r := newDropcapRecorder()
		// Line index 0: the per-spawn ask's reply. A mid-session wait must not accept it.
		if _, err := r.Write([]byte(`{"type":"control_response","response":{"subtype":"success"}}` + "\n")); err != nil {
			t.Fatalf("feed the per-spawn reply: %v", err)
		}
		if rafcapAwait(r, newRafcapTimeline(), within, rafcapHasControlResponseAfter(5)) {
			t.Error("the per-spawn ask's own reply satisfied a wait for the mid-session one; the two " +
				"are indistinguishable by type and only the index separates them")
		}
		if !rafcapAwait(r, newRafcapTimeline(), within, rafcapHasControlResponseAfter(0)) {
			t.Error("missed the per-spawn ask's reply at index 0")
		}
	})
}

// TestRafcapTimelineRecordsFirstSight runs offline. offset_seconds is what tells a
// roster arriving immediately from one arriving thirty minutes later — desktop #1558's
// whole report — so the timeline must record FIRST sight and never overwrite it on a
// later poll.
func TestRafcapTimelineRecordsFirstSight(t *testing.T) {
	t.Parallel()
	tl := newRafcapTimeline()
	base := time.Now()
	tl.note([]dropcapCaptured{{Index: 0}, {Index: 1}}, base)
	// The same lines seen again, later: their first-sight times must not move.
	tl.note([]dropcapCaptured{{Index: 0}, {Index: 1}, {Index: 2}}, base.Add(10*time.Second))

	if secs, ok := tl.offset(0, 1); !ok || secs != 0 {
		t.Errorf("offset(0,1) = (%v, %v), want two lines seen in the same tick", secs, ok)
	}
	if secs, ok := tl.offset(1, 2); !ok || secs != 10 {
		t.Errorf("offset(1,2) = (%v, %v), want 10s — a re-noted line must keep its FIRST sight", secs, ok)
	}
	if _, ok := tl.offset(1, 99); ok {
		t.Error("offset reported a duration for a line never seen; an invented offset is worse than none")
	}
}

// TestRafcapQuietWindowMeetsItsFloor and TestRafcapBudgetOutlastsItsPhases pin the two
// pieces of arithmetic this rig's correctness rests on, offline.
//
// The floor is AC1's: an absence recorded over a window shorter than 30 s is an absence
// nothing waited for. The budget is the sibling failure — the phase waits run in
// sequence before the budget is ever consulted, so a budget smaller than their sum
// leaves the final select with BOTH arms ready and terminated_on picked at random, a
// record misreporting how its own turn ended.
func TestRafcapQuietWindowMeetsItsFloor(t *testing.T) {
	t.Parallel()
	if rafcapQuietWindow < rafcapQuietWindowFloor {
		t.Errorf("rafcapQuietWindow = %s, below AC1's %s floor", rafcapQuietWindow, rafcapQuietWindowFloor)
	}
}

func TestRafcapBudgetOutlastsItsPhases(t *testing.T) {
	t.Parallel()
	phases := rafcapRendezvousWait + rafcapBackgroundWait + rafcapTerminalWait + rafcapQuietWindow +
		2*rafcapInitializeWait + rafcapFollowOnTurnWait
	if rafcapTurnBudget <= phases {
		t.Errorf("rafcapTurnBudget = %s but the phase waits can spend %s before the budget is read: "+
			"raise the budget or shorten a phase, and do it deliberately", rafcapTurnBudget, phases)
	}
	t.Logf("turn budget %s, phases at most %s, margin %s", rafcapTurnBudget, phases, rafcapTurnBudget-phases)
}

// TestRafcapSealCatchesWhatEntersTheRecordAfterTheFirstScan runs offline.
//
// The record is marshalled more than once, and fields enter it BETWEEN the marshals:
// fixture_stage_detail carries `git`'s combined output, and git prints repository paths
// on failure. That path is in nobody's substitution table, so only the deny-scan can
// catch it — and scanning once at the top then re-marshalling scanned a blob that did
// not yet hold those bytes.
func TestRafcapSealCatchesWhatEntersTheRecordAfterTheFirstScan(t *testing.T) {
	t.Parallel()

	// The fixed half of the net, which needs no knowledge of the run that produced a
	// record — the same half that lets a committed capture be re-scanned forever.
	scanner := newDropcapScanner("", "", "")

	for _, tc := range []struct {
		name   string
		mutate func(*rafcapRecord)
		want   string
	}{
		{name: "a clean record seals with no hit", mutate: func(*rafcapRecord) {}},
		{
			name: "git's index-lock error reaches the seal",
			mutate: func(rec *rafcapRecord) {
				rec.FixtureStageDetail = "git add failed (exit status 128): fatal: Unable to create " +
					"'/Users/operator/src/pyrycode/.git/index.lock': File exists"
			},
			want: dropcapDenyUsers,
		},
		{
			name: "a linux repository path reaches the seal",
			mutate: func(rec *rafcapRecord) {
				rec.FixtureStageDetail = "git add failed: /home/operator/src/pyrycode/.git/index.lock"
			},
			want: dropcapDenyHome,
		},
		{
			// Frame payloads are scanned as DECODED bytes, because base64 hides them
			// from a scan of the marshalled record.
			name: "a base64 frame payload is scanned decoded",
			mutate: func(rec *rafcapRecord) {
				rec.Frames = []rafcapFrame{{
					Index:      0,
					Subtype:    rafcapNotificationSubtype,
					PayloadB64: base64.StdEncoding.EncodeToString([]byte(`{"output_file":"/Users/x/o.txt"}`)),
				}}
			},
			want: dropcapDenyUsers,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := rafcapSeedRecord()
			tc.mutate(rec)
			blob, hits, err := rafcapSeal(scanner, rec)
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			if tc.want == "" {
				if len(hits) > 0 {
					t.Fatalf("a clean record was refused, naming %v", hits)
				}
				return
			}
			if !rafcapContains(hits, tc.want) {
				t.Fatalf("the seal returned %v and not %q: these bytes enter the record after the first "+
					"marshal, so a scan that ran only at the top would carry them into a committed public "+
					"fixture unscanned", hits, tc.want)
			}
			// The blob is returned for the caller to write, and the caller refuses to
			// write it on a hit. What it must NOT do is be pre-sanitised here: the
			// fail-closed response belongs to the caller, which knows what was about to land.
			if len(blob) == 0 {
				t.Error("the seal returned no bytes alongside its hits")
			}
		})
	}
}

// TestRafcapRigAuthoredProseCarriesNoDenyNeedle is the net that would have saved
// #2247's first live lap, and it is deliberately of different fabric from the rule it
// enforces. "Do not quote a deny needle in prose the record carries" is advisory, it was
// followed carefully, and it was broken anyway — because the rationale's whole SUBJECT
// is the deny-scan, and the natural way to document a prefix list is to write the
// prefixes down.
//
// What happened there: the rationale spelled out all four path prefixes
// dropcapFixedNeedles searches for, the fail-closed scan hit four classes at once,
// nothing was written, and the probe fatalled — after a 360 s live turn inside a 1400 s
// gate lap that spent real tokens. The scan behaved exactly as designed; the bytes it
// refused were the rig's own, and the diagnosis is a string comparison.
//
// Scope is the RIG-AUTHORED surface only. A needle in claude's bytes is the live scan's
// to catch, after dropcapRedactor has had its turn.
func TestRafcapRigAuthoredProseCarriesNoDenyNeedle(t *testing.T) {
	t.Parallel()

	// The fixed half only. The dynamic needles are the live run's own paths, which no
	// offline test can know and which cannot appear in a compile-time constant.
	scanner := dropcapScanner{needles: dropcapFixedNeedles()}

	t.Run("the record's rig-authored seed", func(t *testing.T) {
		t.Parallel()
		blob, err := json.Marshal(rafcapSeedRecord())
		if err != nil {
			t.Fatalf("marshal the seed record: %v", err)
		}
		if hits, _ := scanner.scan(blob); len(hits) > 0 {
			t.Errorf("rafcapSeedRecord carries deny class(es) %v in its OWN constants, so the live probe "+
				"fails its write closed and produces nothing: name the prefixes by symbol "+
				"(dropcapFixedNeedles) rather than spelling them out", hits)
		}
	})

	t.Run("every stagingVerdict arm and the follow-on prompt", func(t *testing.T) {
		t.Parallel()
		// stagingVerdict is prose built at runtime and it lands in a record written on
		// the refused path — the path where evidence matters most and where a scan hit
		// would destroy the very evidence the arm exists to give.
		for _, tc := range []struct {
			name string
			rec  rafcapRecord
		}{
			{"claude never opened the FIFO", rafcapRecord{}},
			{"started but never backgrounded", rafcapRecord{ForegroundCallObserved: true}},
			{"never reached a terminal status", rafcapRecord{
				ForegroundCallObserved: true, BackgroundTaskObserved: true, HeldSeconds: 12.5,
			}},
			{"the staging worked", rafcapRecord{
				ForegroundCallObserved: true, BackgroundTaskObserved: true, TerminalStatusObserved: true,
				TerminalStatusSubtype: rafcapNotificationSubtype, TerminalStatusToken: "completed",
				HeldSeconds: 12.5, QuietWindowSeconds: 35,
			}},
		} {
			if hits, _ := scanner.scan([]byte(tc.rec.stagingVerdict())); len(hits) > 0 {
				t.Errorf("stagingVerdict arm %q carries deny class(es) %v", tc.name, hits)
			}
		}
		if hits, _ := scanner.scan([]byte(rafcapFollowOnPrompt)); len(hits) > 0 {
			t.Errorf("the follow-on prompt carries deny class(es) %v", hits)
		}
	})

	t.Run("the net reddens on a needle", func(t *testing.T) {
		t.Parallel()
		// Non-vacuity, established without mutating the file: append a needle to the one
		// field the real defect was in. If this arm passes, the two above prove nothing —
		// an empty needle list would make them green forever.
		rec := rafcapSeedRecord()
		rec.RedactionRationale += " and a stray /Users/ prefix spelled out in prose"
		blob, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal the seeded record: %v", err)
		}
		hits, _ := scanner.scan(blob)
		if !rafcapContains(hits, dropcapDenyUsers) {
			t.Errorf("scanning a seed record whose rationale carries that prefix did not report %q; "+
				"hits = %v, so the arms above are vacuous", dropcapDenyUsers, hits)
		}
	})
}

// rafcapContains is a local spelling of "is s in xs". It takes the file prefix like
// every other identifier here: siblings add files to this package concurrently and a
// branch-overlap check does not catch a same-package identifier collision, which is
// exactly the shape a generically-named helper would produce.
func rafcapContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
