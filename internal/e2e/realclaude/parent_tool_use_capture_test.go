//go:build e2e_realclaude

package realclaude

// Evidence capture for #2191 — one live turn that fans out to a SUBAGENT, so the
// `parent_tool_use_id` join can be proven against bytes claude actually sent.
//
// # Why this needs a live run at all
//
// No committed capture holds a subagent turn, and one appears to. 35 files under
// testdata/ carry the key across 239 occurrences and 227 read null; the 12 that do
// not are all in tool_progress_v2.1.259.json, and that turn ran one foreground Bash
// call and spawned nothing. ON A tool_progress LINE THE KEY MEANS SOMETHING ELSE —
// the tool call the heartbeat belongs to, where each heartbeat's own tool_use_id is
// the synthetic `…-heartbeat-N`. So the existing bytes cannot feed this mapping,
// and reading them as if they could would nest an ordinary Bash heartbeat
// underneath its own Bash row. streamsup's
// TestParser_ParentToolUseID_IsNotFedByToolProgress is that overload pinned from
// the other side; this file produces the bytes the POSITIVE half needs.
//
// # Staging
//
// One turn on one child, in a fresh empty workdir holding two rig-authored files.
// The prompt asks claude to launch ONE subagent and to have that subagent read both
// files — two tool calls inside the subagent, which is the AC's own minimum. The
// files exist so the reads are real work with a deterministic result and no access
// outside the workdir.
//
// A SUBAGENT'S TOOL CALLS ARE ALL THAT ARRIVE, and the record does not pretend
// otherwise: without --forward-subagent-text claude emits only the subagent's
// tool_use and tool_result blocks, never its text. That is why this ticket stops at
// the two tool frames and #2192 owns the flag.
//
// # What is deliberately NOT inherited
//
// #2089's tpcapHoldFIFO. It exists to hold a FOREGROUND Bash call open across a
// heartbeat tick; nothing here holds anything open, and two of that ticket's three
// repair legs were FIFO repairs. #2229's priming turns: this turn needs no staged
// context, only a reachable Agent tool.
//
// Everything else is reused: dropcapRecorder, dropcapRedactor, dropcapScanner,
// dropcapMakeEntry, dropcapWaitForChild and parseOne.
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine, and nothing else — the gate is
// the FIXTURE'S ABSENCE, argued at TestRealClaude_ParentToolUseCapture. To force a
// re-capture at a new claude version, over an existing fixture:
//
//	PYRY_PROBE_PARENT_TOOL_USE_CAPTURE=1 go test -tags e2e_realclaude -timeout 20m -v \
//	  -run '^TestRealClaude_ParentToolUseCapture$' ./internal/e2e/realclaude/
//
// Read WHICH skip: "fixture already exists" is the steady state, while a skip out of
// WithWorktreeAuthenticated means the machine has no claude login and the evidence
// was not produced.

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

// ptucEnableEnv FORCES a re-capture when the fixture already exists. It is not the
// gate — see the gate comment in TestRealClaude_ParentToolUseCapture.
const ptucEnableEnv = "PYRY_PROBE_PARENT_TOOL_USE_CAPTURE"

// The version is spliced into the path rather than repeated, so the filename cannot
// drift from the release the record vouches for. The streamsup-side reader pins the
// same version from the other end and fixtureWorthy refuses to write under a
// mismatched name, so a claude upgrade is a loud instruction to re-capture rather
// than a fixture quietly describing another release.
const (
	ptucFixtureVersion = "2.1.259"
	ptucFixturePath    = "testdata/parent_tool_use_v" + ptucFixtureVersion + ".json"
)

// Every file-local identifier takes the ptuc prefix, for the reason #1260's header
// gives: siblings add files to this package concurrently and a branch-overlap check
// does not catch a same-package identifier collision.
const (
	ptucTicket         = "2191"
	ptucWorkdirName    = "ptuc-work"
	ptucRecordName     = "ptuc-record.json"
	ptucArtifactPrefix = "pyry-2191-capture-*"
	// sonnet rather than the siblings' haiku: the turn's whole point is that claude
	// DELEGATES rather than doing the reads itself, and a model that inlines two
	// trivial reads produces a green run with no subagent in it.
	ptucModel = "sonnet"
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from the
	// sibling probes' so a record can never be mistaken for one of theirs. It is also
	// the id claude echoes back, which is what makes dropcapRedactor's session_id
	// class able to catch it.
	ptucSessionID = "6d21e8b4-0a37-4c95-9e13-58f7b0c2a4d6"
)

const (
	ptucTurnBudget  = 8 * time.Minute
	ptucRunExitWait = 30 * time.Second
	ptucPoll        = 500 * time.Millisecond
	// A subagent turn is quiet for long stretches while the child model works, so
	// the quiescence arm is generous — well past a tool round trip.
	ptucQuiet = 60 * time.Second
)

const (
	ptucFired            = "fired"
	ptucDidNotFire       = "did-not-fire"
	ptucInstrumentBroken = "instrument-broken"
)

const (
	ptucTerminatedResult = "result"
	ptucTerminatedQuiet  = "quiet"
	ptucTerminatedBudget = "budget"
)

// The two files the subagent reads, and the markers they hold. Rig-authored and
// content-free: the whole readable surface of this workdir is these two lines, so a
// subagent that reads both has still read nothing about the host.
var ptucFiles = map[string]string{
	"alpha.txt": "pyry-2191-alpha-marker\n",
	"beta.txt":  "pyry-2191-beta-marker\n",
}

// ptucArgs is the YOLO interactive shape, and it OMITS --allowed-tools entirely.
// That omission is load-bearing rather than convention: an allowlist that does not
// name the Agent tool makes the delegation this capture exists to record impossible,
// and one that does name it is a list to keep correct against claude's own naming.
// ask_user_question_capture_test.go's argv block states the same omission for the
// mirror-image reason.
var ptucArgs = []string{"--model", ptucModel, "--dangerously-skip-permissions"}

const ptucSpawnShapeDelta = "The YOLO interactive shape, identical to #1260's, #2089's and #2229's, " +
	"with ONE difference stated rather than left to be diffed: --allowed-tools is omitted entirely, so " +
	"the Agent/Task tool is reachable. Nothing about parent_tool_use_id is known to depend on the " +
	"approval flags; that is UNMEASURED, not ruled out."

const ptucLimitations = "One conversation, one spawn shape, one claude version, one model (" + ptucModel +
	"), one subagent, and delegation requested EXPLICITLY rather than chosen by claude for a task that " +
	"warranted it. NESTING DEPTH IS UNMEASURED HERE and deliberately so: a depth claude chooses is not " +
	"provokable on demand, so the verbatim-read property is proven against a synthesized line in " +
	"streamsup (TestParser_ParentToolUseID_ReadsInnerDepthVerbatim) rather than against these bytes. If " +
	"this turn DID nest, nested_spawn_observed says so and the frames are here — but nothing asserts on " +
	"it. Cross-version and cross-model stability are UNMEASURED. Subagent TEXT is absent by construction: " +
	"without --forward-subagent-text claude emits only the subagent's tool_use and tool_result blocks."

const ptucRedactionRationale = "Inherited whole from #1260 (see dropcapRedactionRationale): a fresh empty " +
	"non-git workdir under a per-test temp $HOME, rig-authored prompts, no os.Environ() read into the " +
	"record, the declared dropcapRedactor substitution table over every string, and dropcapScanner as a " +
	"fail-closed deny-scan over the marshalled record. " +
	"WHAT THIS CAPTURE SPECIFICALLY CAN CARRY. The readable surface of the workdir is TWO rig-authored " +
	"marker lines, so a subagent that read both files read nothing about the host; the workdir path " +
	"itself is a declared substitution class. Per-message uuid, tool_use_id and PARENT_TOOL_USE_ID " +
	"values SURVIVE, as they do in every committed capture in this directory — and here that is the " +
	"point rather than an inherited default: the join this fixture exists to prove is unreadable " +
	"without them. The session_id deny class covers the conversation id claude echoes back, which is " +
	"the rig's own literal."

// ptucFrame is one line of the turn — every line, not only the attributed ones. The
// payload half comes from dropcapMakeEntry so the base64 arm for invalid UTF-8 is
// shared rather than re-derived.
//
// parent_tool_use_id is carried as the RAW SPELLING found on the line, so a reader
// can see null, absent and a value as three distinct things; the streamsup reader
// re-derives its own answer from the payload rather than trusting this column.
type ptucFrame struct {
	Index                   int      `json:"index"`
	Type                    string   `json:"type"`
	Subtype                 string   `json:"subtype,omitempty"`
	ParentToolUseID         string   `json:"parent_tool_use_id,omitempty"`
	ToolUseIDs              []string `json:"tool_use_ids,omitempty"`
	PayloadLenBytesCaptured int      `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int      `json:"payload_len_bytes"`
	PayloadEncoding         string   `json:"payload_encoding"`
	Payload                 string   `json:"payload,omitempty"`
	PayloadB64              string   `json:"payload_b64,omitempty"`
	EventsEmitted           int      `json:"events_emitted"`
}

type ptucRecord struct {
	Ticket          string   `json:"ticket"`
	ClaudeVersion   string   `json:"claude_version"`
	CapturedAt      string   `json:"captured_at"`
	IsCapture       bool     `json:"is_capture"`
	Model           string   `json:"model"`
	SpawnShape      []string `json:"spawn_shape"`
	SpawnShapeDelta string   `json:"spawn_shape_delta"`
	Workdir         string   `json:"workdir"`
	Prompts         []string `json:"prompts"`

	Outcome       string  `json:"outcome"`
	OutcomeDetail string  `json:"outcome_detail"`
	TerminatedOn  string  `json:"terminated_on"`
	TurnSeconds   float64 `json:"turn_seconds"`

	// THE MEASUREMENT. AgentToolUseID is what the streamsup reader pins; the two
	// counts beside it are what make a zero-hit run diagnosable rather than merely
	// red.
	AgentToolUseID      string   `json:"agent_tool_use_id"`
	AgentToolName       string   `json:"agent_tool_name"`
	AgentCallCount      int      `json:"agent_call_count"`
	AttributedFrames    int      `json:"attributed_frames"`
	SubagentToolCalls   []string `json:"subagent_tool_calls"`
	DistinctParentIDs   []string `json:"distinct_parent_ids"`
	NestedSpawnObserved bool     `json:"nested_spawn_observed"`

	LineTypeCensus map[string]int `json:"line_type_census"`
	ToolCalls      []string       `json:"tool_calls"`
	UndecodedLines int            `json:"undecoded_lines"`

	LinesCaptured int         `json:"lines_captured"`
	Frames        []ptucFrame `json:"frames"`

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

func (rec *ptucRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
}

// fixtureWorthy answers whether this record may be promoted to ptucFixturePath, and
// names the reason when it may not.
//
// Every rejection is a case where the record is still valuable EVIDENCE — it is
// written to the artifact dir either way — but would be a lie as the committed
// proof. #2229's fixtureWorthy is the shape; the arms are this ticket's own.
func (rec *ptucRecord) fixtureWorthy() (string, bool) {
	if rec.Outcome != ptucFired {
		return fmt.Sprintf("outcome=%s", rec.Outcome), false
	}
	if rec.AgentToolUseID == "" {
		return "no Agent call was observed — nothing for the reader to pin", false
	}
	if rec.AgentCallCount != 1 {
		return fmt.Sprintf("%d Agent calls, want exactly 1 — the reader pins ONE id, and a turn with "+
			"several makes 'the Agent call' ambiguous", rec.AgentCallCount), false
	}
	// The AC's own minimum: a subagent running at least two tools produces at least
	// two tool_use frames and their two results.
	if rec.AttributedFrames < ptucMinAttributed {
		return fmt.Sprintf("%d frames attributed to the Agent call, want at least %d (a subagent "+
			"running two tools) — a fixture below that does not prove the join it was taken for",
			rec.AttributedFrames, ptucMinAttributed), false
	}
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != ptucFixtureVersion {
		return fmt.Sprintf("claude_version %q is not the %s pinned in the fixture name — repin "+
			"ptucFixtureVersion and parentCaptureVersion together, then re-run", got, ptucFixtureVersion), false
	}
	// The reader refuses any encoding but json-string, and dropcapMakeEntry emits
	// base64 with an EMPTY payload for a frame that is not valid UTF-8. Promoting one
	// would redden `make check` for every unrelated ticket. A refusal to promote
	// rather than a fatal: the record still holds the frame as evidence.
	for _, f := range rec.Frames {
		if f.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("frame %d is encoded %q and the reader reads only %q — a non-UTF-8 "+
				"frame carries no readable payload, so a fixture holding one would fail the assertion "+
				"it exists to feed", f.Index, f.PayloadEncoding, dropcapEncodingJSONString), false
		}
	}
	return "", true
}

// ptucMinAttributed is AC 2's minimum, named once so the promotion refusal and the
// streamsup reader's own floor cannot drift apart: two tool calls inside the
// subagent, each producing a tool_use and a tool_result.
const ptucMinAttributed = 4

// ptucLineFacts is the slice of one line this probe reads: the spawning call it
// names, and the tool_use/tool_result ids it carries.
//
// SEPARATE FROM THE PARSER ON PURPOSE. The daemon's own decode is what the streamsup
// reader exercises; this one is the probe's independent bookkeeping, so a record
// saying "4 attributed frames" and a parser saying so are two measurements rather
// than one restated. A key absent or null yields "" here, exactly as the shipped
// converter does, and that agreement is the only thing the two share.
type ptucLineFacts struct {
	Parent     string
	ToolUses   []string
	ToolUseIDs []string
	ToolNames  []string
}

// ptucReadLine decodes the facts above off one raw line. Every failure yields a zero
// value rather than an error: an undecodable line is part of what the turn produced
// and is recorded as a frame, not dropped.
func ptucReadLine(raw []byte) ptucLineFacts {
	var line struct {
		Parent  json.RawMessage `json:"parent_tool_use_id"`
		Message *struct {
			Content []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		return ptucLineFacts{}
	}
	var facts ptucLineFacts
	var parent string
	if err := json.Unmarshal(line.Parent, &parent); err == nil {
		facts.Parent = parent
	}
	if line.Message == nil {
		return facts
	}
	for _, block := range line.Message.Content {
		switch block.Type {
		case "tool_use":
			facts.ToolUses = append(facts.ToolUses, block.ID)
			facts.ToolUseIDs = append(facts.ToolUseIDs, block.ID)
			facts.ToolNames = append(facts.ToolNames, block.Name)
		case "tool_result":
			facts.ToolUseIDs = append(facts.ToolUseIDs, block.ToolUseID)
		}
	}
	return facts
}

// ptucIsAgentTool reports whether a tool name is claude's subagent launcher.
//
// BOTH SPELLINGS, because the two are the same tool under two names across
// releases and a probe keyed on one would report "no Agent call" on a turn that
// made one — a did-not-fire verdict indistinguishable from claude declining to
// delegate. TestPtucAgentToolNamesCoverBothSpellings is that guard, offline.
func ptucIsAgentTool(name string) bool {
	return name == "Task" || name == "Agent"
}

// ptucPrompt asks for exactly one subagent doing exactly two reads. Every clause
// earns its place: the delegation must happen (claude inlining two trivial reads is
// the likeliest way this run produces no subagent at all), it must be ONE subagent
// (the reader pins one id), and it must run TWO tools (AC 2's minimum). The nonce
// gives dropcapRedactor's prompt_nonce class something to substitute.
func ptucPrompt(nonce int64) string {
	return fmt.Sprintf("Use the Task tool to launch exactly ONE general-purpose subagent. Give that "+
		"subagent this instruction verbatim: `Read the file alpha.txt and then read the file beta.txt "+
		"in the current working directory, then reply with the two marker strings you found.` Do not "+
		"read either file yourself — the subagent must perform both reads. Launch exactly one "+
		"subagent, use no other tools yourself, and when it reports back reply with the two markers "+
		"and nothing else. run=%d", nonce)
}

// ptucResultCount counts `result` lines — the turn boundary, polled rather than
// awaited on the recorder's once-closing channel.
func ptucResultCount(lines []dropcapCaptured) int {
	n := 0
	for _, c := range lines {
		if c.Decoded && c.Type == "result" {
			n++
		}
	}
	return n
}

// ptucAwaitTurn waits out the turn WITHOUT depending on it closing, #2229's shape
// and for its reason: a run that hangs waiting for a `result` lands no census at
// all, and a census is what makes a did-not-fire run diagnosable.
//
// quiet and budget are parameters rather than the constants directly so the three
// exits can be proved offline in milliseconds.
func ptucAwaitTurn(recorder *dropcapRecorder, sentAt int, quiet, budget time.Duration) string {
	deadline := time.Now().Add(budget)
	lastGrowth := time.Now()
	last := sentAt
	for {
		lines, _ := recorder.snapshot()
		if ptucResultCount(lines) >= 1 {
			return ptucTerminatedResult
		}
		if len(lines) != last {
			last = len(lines)
			lastGrowth = time.Now()
		}
		if len(lines) > sentAt && time.Since(lastGrowth) >= quiet {
			return ptucTerminatedQuiet
		}
		if time.Now().After(deadline) {
			return ptucTerminatedBudget
		}
		time.Sleep(ptucPoll)
	}
}

// TestRealClaude_ParentToolUseCapture is the capture — AC 1.
func TestRealClaude_ParentToolUseCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE, and that is a deliberate break from the
	// env-gated one-off probes in this package. #2089's header argues it in full and
	// the reasoning is identical here: `make e2e-realclaude` never sets a custom
	// PYRY_PROBE_* variable, so an env gate skips on the ENV check BEFORE the
	// credential check, the live gate passes vacuously, and the fixture never lands.
	// That is CLAUDE.md § Testing's #1763 failure exactly — a green gate and a spent
	// budget look identical whether the bytes landed or not.
	force := os.Getenv(ptucEnableEnv) == "1"
	if _, err := os.Stat(ptucFixturePath); err == nil && !force {
		t.Skipf("#2191 parent_tool_use capture: the fixture %s already exists, so there is nothing "+
			"to capture and this costs no claude turn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 20m -v \\\n"+
			"    -run '^TestRealClaude_ParentToolUseCapture$' ./internal/e2e/realclaude/",
			ptucFixturePath, ptucEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Deliberately NOT t.TempDir(): the operator needs the record after the test
	// ends, and #2089's fixture survived its gate's worktree removal only because the
	// record was written outside it. #2229's did not survive, which is why this is
	// stated at every probe rather than assumed.
	artifactDir, err := os.MkdirTemp("", ptucArtifactPrefix)
	if err != nil {
		t.Fatalf("#2191: create artifact dir: %v", err)
	}

	// A fresh directory, deliberately not a git repo, holding only the two marker
	// files the subagent is asked to read: no branch names and no unrelated file
	// contents can reach a payload.
	workdir := filepath.Join(home, ptucWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2191: create workdir: %v", err)
	}
	for name, body := range ptucFiles {
		if err := os.WriteFile(filepath.Join(workdir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("#2191: write %s: %v", name, err)
		}
	}
	nonce := time.Now().UnixNano()

	// The empty slot is fifoPath, and the emptiness is a fact this probe records
	// rather than an omission: it holds no FIFO. dropcapRedactor.add guards "" —
	// strings.ReplaceAll(s, "", x) would otherwise insert x between every character.
	red := newDropcapRedactor(home, artifactDir, workdir, "", ptucSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2191 capture artifacts: %s", red.str(artifactDir))

	prompt := ptucPrompt(nonce)
	rec := &ptucRecord{
		Ticket:                ptucTicket,
		ClaudeVersion:         probeClaudeVersion(claudeBin),
		CapturedAt:            time.Now().Format(time.RFC3339),
		IsCapture:             true,
		Model:                 ptucModel,
		SpawnShapeDelta:       ptucSpawnShapeDelta,
		Workdir:               red.str(workdir),
		Prompts:               []string{red.str(prompt)},
		Frames:                []ptucFrame{},
		SubagentToolCalls:     []string{},
		DistinctParentIDs:     []string{},
		LineTypeCensus:        map[string]int{},
		RedactionRationale:    ptucRedactionRationale,
		CredentialScanApplied: scanner.applied(),
		CredentialScanSkipped: []string{},
		Limitations:           ptucLimitations,
	}
	rec.set(ptucInstrumentBroken, "did not reach a classification point")

	// Registered before anything below can fail, so a structural t.Fatalf still
	// leaves the evidence on disk — #1260's ordering.
	t.Cleanup(func() { ptucWriteRecord(t, artifactDir, red, scanner, rec) })

	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: ptucSessionID,
		Args:      ptucArgs,
		Stdout:    recorder,
		Logger:    slog.New(argvHandler),
	})
	if err != nil {
		rec.set(ptucInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
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
		case <-time.After(ptucRunExitWait):
			t.Errorf("#2191: streamsup.Run did not return within %s of cancel", ptucRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(ptucInstrumentBroken, "no live child within %s: claude never spawned, so nothing was "+
			"on the wire to capture", dropcapSpawnWait)
		return
	}

	turnStart := time.Now()
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.set(ptucInstrumentBroken, "writing the turn failed, so the surface was never exercised: %v",
			red.str(err.Error()))
		return
	}
	rec.TerminatedOn = ptucAwaitTurn(recorder, 0, ptucQuiet, ptucTurnBudget)
	rec.TurnSeconds = time.Since(turnStart).Seconds()

	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	ptucCollect(t, rec, lines, red)

	if rec.AttributedFrames == 0 {
		rec.set(ptucDidNotFire, "no line named a spawning Agent call: %d Agent call(s) observed, "+
			"tools run %v", rec.AgentCallCount, rec.ToolCalls)
	} else {
		rec.set(ptucFired, "%d frame(s) attributed to Agent call %q; subagent tools %v",
			rec.AttributedFrames, rec.AgentToolUseID, rec.SubagentToolCalls)
	}

	// Counts and indices only. The frames themselves are in the record the cleanup
	// has already written; putting claude's bytes in CI output is precisely the
	// exposure the deny-scan exists to prevent.
	if rec.AttributedFrames < ptucMinAttributed {
		t.Fatalf("#2191: the turn produced %d frame(s) naming a spawning Agent call, want at least "+
			"%d (terminated_on=%s, turn=%.1fs, %d line(s)). A capture recording fewer is vacuous, and "+
			"committing it would hand the streamsup reader a fixture that proves nothing.\n"+
			"  Agent calls observed: %d (id %q, tool %q); tools run: %v; line types: %v; undecoded: %d\n"+
			"Read the Agent-call count FIRST: zero means claude did the reads itself instead of "+
			"delegating, which is a RIG failure (tighten ptucPrompt or raise the model), not a finding "+
			"about parent_tool_use_id",
			rec.AttributedFrames, ptucMinAttributed, rec.TerminatedOn, rec.TurnSeconds,
			rec.LinesCaptured, rec.AgentCallCount, rec.AgentToolUseID, rec.AgentToolName,
			rec.ToolCalls, rec.LineTypeCensus, rec.UndecodedLines)
	}
}

// ptucCollect builds a frame for EVERY line of the turn and fills the record's
// measurement fields from them — not only the attributed ones. An undecodable line
// still gets a frame: it is part of what the turn produced, and losing it would make
// the record disagree with lines_captured for no stated reason.
func ptucCollect(t *testing.T, rec *ptucRecord, lines []dropcapCaptured, red *dropcapRedactor) {
	t.Helper()
	toolNames := map[string]bool{}
	subagentTools := map[string]bool{}
	parents := map[string]bool{}
	// The id of every Agent call, so a frame naming one can be told from a frame
	// naming an ordinary call — which is what makes nested_spawn_observed meaningful.
	agentIDs := map[string]bool{}

	for _, c := range lines {
		facts := ptucReadLine(c.Raw)
		entry := dropcapMakeEntry(c, "", red)
		frame := ptucFrame{
			Index:                   entry.Index,
			Type:                    entry.Type,
			Subtype:                 entry.Subtype,
			ParentToolUseID:         red.str(facts.Parent),
			ToolUseIDs:              red.strs(facts.ToolUseIDs),
			PayloadLenBytesCaptured: entry.PayloadLenBytesCaptured,
			PayloadLenBytes:         entry.PayloadLenBytes,
			PayloadEncoding:         entry.PayloadEncoding,
			Payload:                 entry.Payload,
			PayloadB64:              entry.PayloadB64,
			EventsEmitted:           len(parseOne(t, string(c.Raw))),
		}
		rec.Frames = append(rec.Frames, frame)
		if !c.Decoded {
			rec.UndecodedLines++
			rec.LineTypeCensus["<undecodable>"]++
			continue
		}
		key := c.Type
		if c.Subtype != "" {
			key = c.Type + "/" + c.Subtype
		}
		rec.LineTypeCensus[key]++

		for i, name := range facts.ToolNames {
			toolNames[name] = true
			if !ptucIsAgentTool(name) {
				continue
			}
			rec.AgentCallCount++
			// The FIRST Agent call is the one the reader pins. A turn with several is
			// refused promotion by fixtureWorthy rather than silently pinned to one.
			if rec.AgentToolUseID == "" {
				rec.AgentToolUseID = facts.ToolUses[i]
				rec.AgentToolName = name
			}
			agentIDs[facts.ToolUses[i]] = true
		}
		if facts.Parent == "" {
			continue
		}
		parents[facts.Parent] = true
		for _, name := range facts.ToolNames {
			subagentTools[name] = true
		}
	}

	// Attribution is counted in a SECOND pass, because the Agent call's own tool_use
	// line arrives before the frames naming it and a single pass would miss any that
	// preceded the id being learned.
	for _, f := range rec.Frames {
		if f.ParentToolUseID != "" && f.ParentToolUseID == red.str(rec.AgentToolUseID) {
			rec.AttributedFrames++
		}
	}
	// A parent id that is itself an Agent call the subagent made is the nesting case.
	// Recorded, never asserted on: ptucLimitations states why.
	for p := range parents {
		if p != rec.AgentToolUseID && agentIDs[p] {
			rec.NestedSpawnObserved = true
		}
		rec.DistinctParentIDs = append(rec.DistinctParentIDs, red.str(p))
	}
	for name := range toolNames {
		rec.ToolCalls = append(rec.ToolCalls, name)
	}
	for name := range subagentTools {
		rec.SubagentToolCalls = append(rec.SubagentToolCalls, name)
	}
	sort.Strings(rec.DistinctParentIDs)
	sort.Strings(rec.ToolCalls)
	sort.Strings(rec.SubagentToolCalls)
	rec.AgentToolUseID = red.str(rec.AgentToolUseID)
}

// ptucWriteRecord deny-scans the marshalled record, writes it to the artifact dir,
// and promotes it in-repo when it is worth committing. #2229's ccapWriteRecord is
// the shape; nothing about the scan is weakened here.
func ptucWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *ptucRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2191: marshal record: %v", err)
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
			t.Errorf("#2191: frame %d: decode base64 payload for the scan: %v", i, derr)
			return
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("#2191: deny-scan found %d denied class(es) still present in the record: %v\n"+
			"NOTHING was written — not the record, not the fixture. Extend dropcapRedactor's table "+
			"with the named class and re-run the capture. The offending value is deliberately not "+
			"printed: putting it in CI output is exactly the exposure this scan exists to prevent",
			len(hits), hits)
	}
	rec.CredentialScanSkipped = notApplied

	// Re-marshal so credential_scan_skipped ships in the written bytes. Its value is
	// a list of CLASS NAMES the scan could not apply, which is the one thing that
	// makes a silently-off credential net visible after the fact.
	blob, err = json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2191: re-marshal record: %v", err)
		return
	}

	path := filepath.Join(dir, ptucRecordName)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2191: write record %s: %v", red.str(path), err)
		return
	}
	t.Logf("#2191 outcome=%s terminated_on=%s turn=%.1fs captured=%d agent_calls=%d agent_id=%q "+
		"attributed=%d subagent_tools=%v nested=%v types=%v scan_not_applied=%v\n  record: %s\n  %s",
		rec.Outcome, rec.TerminatedOn, rec.TurnSeconds, rec.LinesCaptured, rec.AgentCallCount,
		rec.AgentToolUseID, rec.AttributedFrames, rec.SubagentToolCalls, rec.NestedSpawnObserved,
		rec.LineTypeCensus, notApplied, red.str(path), red.str(rec.OutcomeDetail))

	// The same deny-scanned bytes, promoted in-repo so the run that produced them is
	// the run that lands them. A capture that still needs a human to copy a file out
	// of a tempdir is a capture #1763 says will not land.
	if reason, ok := rec.fixtureWorthy(); !ok {
		t.Logf("#2191: NOT promoted to %s — %s. The record above is the evidence; read it, then "+
			"re-run or route the finding back", ptucFixturePath, reason)
		return
	}
	if err := os.WriteFile(ptucFixturePath, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2191: write fixture %s: %v", ptucFixturePath, red.str(err.Error()))
		return
	}
	t.Logf("#2191: FIXTURE WRITTEN to %s (Agent call %q, %d attributed frame(s)).\n"+
		"  COMMIT IT — `git add %s` — and in the SAME commit set parentPinnedAgentID in "+
		"internal/streamsup/parent_tool_use_capture_test.go to %q and change nothing else: that "+
		"reader FATALS on a present fixture with an empty pin, which is what stops the bytes landing "+
		"unpinned. An uncommitted capture is a capture that did not happen (#1763), and a gate-only "+
		"lap runs no `git add` of its own (#2229 lost its fixture exactly that way) — the record "+
		"above survives in the artifact dir if this one does.",
		ptucFixturePath, rec.AgentToolUseID, rec.AttributedFrames, ptucFixturePath, rec.AgentToolUseID)
}

// TestPtucAgentToolNamesCoverBothSpellings runs offline and guards the one
// classification this probe makes.
//
// The subagent launcher answers to `Task` and to `Agent` across releases. A probe
// keyed on one spelling reports "no Agent call" on a turn that made one — and that
// verdict is INDISTINGUISHABLE from claude declining to delegate, so the run would
// be re-staged and re-run against a rig fault that is not there. The negative rows
// are what keep the predicate from becoming "any tool at all".
func TestPtucAgentToolNamesCoverBothSpellings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"Task", true},
		{"Agent", true},
		{"Read", false},
		{"Bash", false},
		{"", false},
		{"task", false}, // claude's tool names are capitalised; a lowercase match would be a guess
	} {
		if got := ptucIsAgentTool(tc.name); got != tc.want {
			t.Errorf("ptucIsAgentTool(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPtucReadLineSeparatesTheThreeSpellingsOfAbsent runs offline and is the guard
// on the probe's own bookkeeping.
//
// The record's attributed_frames is a SECOND measurement beside the parser's, which
// is only worth having if it is right. All three no-value spellings — the key
// absent, an explicit null, and a non-string — must read as "", exactly as the
// shipped converter does; a probe that read null as the string "null" would report
// every main-thread frame as attributed and a vacuous capture would promote.
func TestPtucReadLineSeparatesTheThreeSpellingsOfAbsent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		line       string
		wantParent string
		wantTools  []string
	}{
		{"absent", `{"type":"assistant","message":{"content":[]}}`, "", nil},
		{"explicit null", `{"type":"assistant","parent_tool_use_id":null,"message":{"content":[]}}`, "", nil},
		{"non-string", `{"type":"assistant","parent_tool_use_id":7,"message":{"content":[]}}`, "", nil},
		{"undecodable line", `{not json`, "", nil},
		{
			name: "a spawned tool_use names its parent and its tool",
			line: `{"type":"assistant","parent_tool_use_id":"toolu_agent","message":{"content":` +
				`[{"type":"tool_use","id":"toolu_child","name":"Read"}]}}`,
			wantParent: "toolu_agent",
			wantTools:  []string{"Read"},
		},
		{
			name: "a main-thread Agent call names no parent",
			line: `{"type":"assistant","parent_tool_use_id":null,"message":{"content":` +
				`[{"type":"tool_use","id":"toolu_agent","name":"Task"}]}}`,
			wantParent: "",
			wantTools:  []string{"Task"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts := ptucReadLine([]byte(tc.line))
			if facts.Parent != tc.wantParent {
				t.Errorf("Parent = %q, want %q", facts.Parent, tc.wantParent)
			}
			if len(facts.ToolNames) != len(tc.wantTools) {
				t.Fatalf("ToolNames = %v, want %v", facts.ToolNames, tc.wantTools)
			}
			for i := range tc.wantTools {
				if facts.ToolNames[i] != tc.wantTools[i] {
					t.Errorf("ToolNames[%d] = %q, want %q", i, facts.ToolNames[i], tc.wantTools[i])
				}
			}
		})
	}
}

// TestPtucFixtureWorthyRefusesEveryBadCapture runs offline and pins the promotion
// gate, one row per refusal arm.
//
// A capture that promotes when it should not is the failure that costs a whole
// downstream ticket: the bytes land, the pin is filled from them, and `make check`
// then asserts against a fixture recording nothing. Every arm below is a way that
// could happen.
func TestPtucFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	good := func() *ptucRecord {
		return &ptucRecord{
			Outcome:          ptucFired,
			ClaudeVersion:    ptucFixtureVersion + " (Claude Code)",
			AgentToolUseID:   "toolu_agent",
			AgentCallCount:   1,
			AttributedFrames: ptucMinAttributed,
			Frames: []ptucFrame{
				{Index: 0, PayloadEncoding: dropcapEncodingJSONString},
			},
		}
	}
	if reason, ok := good().fixtureWorthy(); !ok {
		t.Fatalf("a good record was refused: %s — every row below is a MUTATION of it, so a "+
			"baseline that already fails proves nothing", reason)
	}
	tests := []struct {
		name   string
		break_ func(*ptucRecord)
	}{
		{"did not fire", func(r *ptucRecord) { r.Outcome = ptucDidNotFire }},
		{"instrument broken", func(r *ptucRecord) { r.Outcome = ptucInstrumentBroken }},
		{"no Agent call", func(r *ptucRecord) { r.AgentToolUseID = "" }},
		{"two Agent calls make the pin ambiguous", func(r *ptucRecord) { r.AgentCallCount = 2 }},
		{"one tool short of the minimum", func(r *ptucRecord) { r.AttributedFrames = ptucMinAttributed - 1 }},
		{"a different claude", func(r *ptucRecord) { r.ClaudeVersion = "2.1.100 (Claude Code)" }},
		{"an unreadable version", func(r *ptucRecord) { r.ClaudeVersion = "<unavailable: boom>" }},
		{"a frame the reader cannot decode", func(r *ptucRecord) {
			r.Frames[0].PayloadEncoding = dropcapEncodingBase64
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := good()
			tc.break_(rec)
			reason, ok := rec.fixtureWorthy()
			if ok {
				t.Fatalf("fixtureWorthy promoted a record that %s — the fixture would land "+
					"recording nothing", tc.name)
			}
			if reason == "" {
				t.Errorf("refused with an empty reason; the operator reads this to know whether to " +
					"re-run or route the finding back")
			}
		})
	}
}

// TestPtucAwaitTurnDoesNotDependOnAResult runs offline and proves the three exits.
//
// The budget arm is the one that matters: a subagent turn that never closes must
// still land a census, because a probe that hangs produces no evidence at all and a
// 20-minute gate slot is spent for nothing.
func TestPtucAwaitTurnDoesNotDependOnAResult(t *testing.T) {
	t.Parallel()
	t.Run("a result ends it", func(t *testing.T) {
		t.Parallel()
		rec := newDropcapRecorder()
		_, _ = rec.Write([]byte(`{"type":"result","subtype":"success"}` + "\n"))
		if got := ptucAwaitTurn(rec, 0, time.Hour, time.Hour); got != ptucTerminatedResult {
			t.Errorf("got %q, want %q", got, ptucTerminatedResult)
		}
	})
	t.Run("quiescence ends it when a result never comes", func(t *testing.T) {
		t.Parallel()
		rec := newDropcapRecorder()
		_, _ = rec.Write([]byte(`{"type":"assistant","message":{"content":[]}}` + "\n"))
		if got := ptucAwaitTurn(rec, 0, time.Millisecond, time.Hour); got != ptucTerminatedQuiet {
			t.Errorf("got %q, want %q", got, ptucTerminatedQuiet)
		}
	})
	t.Run("the budget ends it when nothing ever arrives", func(t *testing.T) {
		t.Parallel()
		// sentAt equals the line count, so the quiescence arm cannot fire: it requires
		// the turn to have produced something. Only the budget can end this one.
		rec := newDropcapRecorder()
		if got := ptucAwaitTurn(rec, 0, time.Millisecond, 10*time.Millisecond); got != ptucTerminatedBudget {
			t.Errorf("got %q, want %q", got, ptucTerminatedBudget)
		}
	})
}
