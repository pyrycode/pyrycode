//go:build e2e_realclaude

package realclaude

// Evidence capture for #1260 — the verbatim payload of every stream-json line
// the daemon drops today on the interactive surface.
//
// This file is NOT a regression gate and changes no drop behaviour. It stages
// ONE live turn that backgrounds a Bash command, records claude's stdout
// UPSTREAM of the parser, and asks the shipped parser which of those lines it
// drops. #1261-#1264 build their mapping from the captured bytes.
//
// # Where it taps, and why that is structural
//
// cmd/pyry/`newStreamRunnerFactory` installs streamsup.NewParser(...) as
// scfg.Stdout. dropcapRecorder takes that exact slot on an in-process
// streamsup.Runner, so "upstream of the parser" is a fact about the wiring
// rather than an argument. #1240's bgIdleRecordTurn is the WRONG recorder for
// this question: it reads decrypted phone frames, downstream of the parser, so
// every dropped line is absent from it by construction and the zero would read
// as a measured absence.
//
// The Unrecognized lane cannot carry these bytes either: an ignored type
// returns at `benignRateLimitStatus` before emitUnrecognized is reached, and
// truncateRaw caps at 16 KiB AND runs strings.ToValidUTF8(…, ""), which deletes
// invalid UTF-8 while reporting only the length cap.
//
// # What "dropped" means here
//
// Zero events emitted by a real streamsup.Parser (via parseOne). The parser is
// documented turn-stateless (`maxTaskRosterEntries`), which is the licence to
// classify each line with a fresh parser. Nothing in this file reads
// streamsup's unexported tables.
//
// # Redaction — the boundary is claude stdout -> record -> committed fixture ->
// public issue comment
//
// Two mechanisms of different fabric. dropcapRedactor is a declared
// substitution table applied to EVERY string that enters the record (payloads,
// but also fifoLiveOutcome.Path/Detail, spawn_shape, outcome_detail and every
// t.Logf). dropcapScanner is a fail-closed deny-scan over the whole marshalled
// record: on a hit nothing is written and the message names the CLASS only,
// never the matched value. os.Environ() is never called in this file; env_delta
// is a fixed literal (#1223's rule).
//
// # Outcomes
//
// fired / did-not-fire / instrument-broken. Only `fired` licenses an absence
// claim, and absence_claim_valid is carried as its own boolean. A did-not-fire
// run is a legitimate published result, not a failed test. Nothing about
// claude's behaviour is fatal; only a broken instrument or a redaction failure
// is.
//
// # Running it
//
//	PYRY_PROBE_DROPPED_LINE_CAPTURE=1 go test -tags e2e_realclaude -timeout 15m -v \
//	  -run '^TestRealClaude_DroppedLineCapture$' ./internal/e2e/realclaude/
//
// A skip is the normal `make e2e-realclaude` outcome and carries no signal. The
// TestDropcap* tests below run offline, with no claude and no gate.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// dropcapEnableEnv gates the live capture. `make e2e-realclaude` runs the whole
// package glob and this probe costs one claude turn.
const dropcapEnableEnv = "PYRY_PROBE_DROPPED_LINE_CAPTURE"

// Every file-local identifier takes the dropcap prefix: siblings add files to
// this package concurrently and a branch-overlap check does NOT catch a
// same-package identifier collision (it surfaces only once both are on main).
const (
	dropcapTicket         = "1260"
	dropcapFIFOName       = "dropcap-hold"
	dropcapWorkdirName    = "dropcap-work"
	dropcapRecordName     = "dropcap-record.json"
	dropcapArtifactPrefix = "pyry-1260-capture-*"
	dropcapModel          = "haiku"
	dropcapBashTimeoutEnv = "BASH_DEFAULT_TIMEOUT_MS"
	dropcapBashTimeoutMS  = "5000"
	// A fixed literal in a per-test temp $HOME, not a secret.
	dropcapSessionID = "6d1c0f7a-12a0-4c0a-9b2e-0d5a7c3f1e42"
	// The committed capture. Glob, because the version suffix moves.
	dropcapFixtureGlob = "testdata/dropped_lines_v*.json"
)

const (
	dropcapTurnBudget  = 3 * time.Minute
	dropcapSpawnWait   = 60 * time.Second
	dropcapSpawnPoll   = 100 * time.Millisecond
	dropcapRunExitWait = 30 * time.Second
)

// dropcapMaxPartial caps the partial-line accumulator — same value and same
// reason as streamsup's defaultMaxParseBuf. This is claude's
// stdout read into the parent's memory, and the partial is the only unbounded
// accumulator in the design. dropcapMaxCaptureBytes caps the total; past it
// WHOLE lines are dropped and counted, never truncated, because AC3 forbids a
// truncated payload. Draining continues past both — an unconsumed stdout would
// block claude.
const (
	dropcapMaxPartial      = 4 << 20
	dropcapMaxCaptureBytes = 8 << 20
)

// The three outcomes. Only dropcapFired licenses an absence claim.
const (
	dropcapFired            = "fired"
	dropcapDidNotFire       = "did-not-fire"
	dropcapInstrumentBroken = "instrument-broken"
)

const (
	dropcapTerminatedResult = "result"
	dropcapTerminatedBudget = "budget"
)

// Why a captured line emitted zero events. Decided by the shipped parser, never
// by a mirror of its tables.
const (
	dropcapReasonIgnoredType = "ignored-line-type"
	dropcapReasonUserBlock   = "user-block-suppressed"
	dropcapReasonEmptyMsg    = "empty-message"
	dropcapReasonUndecodable = "undecodable-line"
)

// payload_encoding is written on EVERY entry. A payload stored as a JSON string
// is re-encoded by the container, and Go's encoder replaces invalid UTF-8 with
// U+FFFD — the same silent mutation truncateRaw performs. So invalid UTF-8 goes
// to base64 instead and says so.
const (
	dropcapEncodingJSONString = "json-string"
	dropcapEncodingBase64     = "base64"
)

// The subtypes #1247 observed on the HEADLESS surface. Naming them explicitly
// is what makes a later absence distinguishable from a measurement that never
// looked.
var dropcapExpectedSubtypes = []string{
	"task_started", "task_updated", "background_tasks_changed", "task_notification",
}

// dropcapArgs is everything this probe adds; buildArgs supplies the fixed
// --input-format/--output-format/--verbose prefix and the id flag.
var dropcapArgs = []string{"--model", dropcapModel, "--dangerously-skip-permissions"}

const dropcapSpawnShapeDelta = "This is the YOLO interactive shape. Production's stream path adds, via " +
	"withApprovalArgs (cmd/pyry/streamsup_runner.go:116) and internal/sessions.claudeSettingsArgs, a " +
	"--permission-prompt-tool / --mcp-config pair on NON-yolo spawns plus a per-session --settings. " +
	"This capture passes none of them: it uses --dangerously-skip-permissions, a real production shape " +
	"(the YOLO session bit, internal/sessions/session.go) and precisely the arm on which withApprovalArgs " +
	"injects nothing. Consequence: the --mcp-config server would appear in system/init's mcp_servers / " +
	"tools, so this capture's system/init is the yolo variant. Whether the flag alters the system/task_* " +
	"family is UNMEASURED — do not read parity into it."

const dropcapLimitations = "One turn, one spawn shape, one claude version, one model (" + dropcapModel + "). " +
	"Cross-version and cross-surface stability are UNMEASURED. The census is a census of THIS turn: a " +
	"subtype listed in expected_absent did not appear here, which is not the same as claude never " +
	"emitting it. #1218 measured one subtype's rate as surface-dependent (ptyrunner emits zero " +
	"system/thinking_tokens, streamrunner ~10/turn), so model- and surface-dependence are live " +
	"possibilities, not ruled-out ones."

const dropcapRedactionRationale = "The primary defence is by construction: the workdir is a fresh empty " +
	"directory with NO git repo (no branch names, no file contents), the prompt is rig-authored " +
	"(bgIdlePrompt), the Bash command is `cat <fifo>` which produces no output before it is backgrounded, " +
	"and os.Environ() is never read into the record. On top of that, dropcapRedactor substitutes a " +
	"declared table of path/identifier classes into EVERY string that enters the record, and " +
	"dropcapScanner is a fail-closed deny-scan over the whole marshalled record. " +
	"bgIdleRedact (interactive_background_idle_probe_test.go:824) is NOT sufficient here: it substitutes " +
	"one value (the operator's home) into a SUMMARY that turnbridge/outbound.go had already capped at 200 " +
	"runes, so the exposure was structurally bounded before redaction ran. Here the input is an uncapped " +
	"raw payload that can carry cwd, tool output, file contents, branch names and prompt text, and it " +
	"passes through unsummarised; a single home substitution would leave the temp $HOME path, the " +
	"workdir, the session UUID and any tool output untouched. " +
	"DELIBERATELY KEPT, because removing them would defeat the ticket: top-level types and subtypes, " +
	"claude's own structural fields, tool names, model names, token counts, timestamps, the task-lifecycle " +
	"payload structure and its human-readable strings, and the harness nudge text. " +
	"ALSO KEPT, AND OPERATOR-DERIVED — named separately because it is the largest operator-specific class " +
	"in this file and the one a person deciding whether to paste the record into a public issue has to be " +
	"told about: system/init carries the operator's local claude configuration inventory. Concretely, the " +
	"MCP server names, the tool list (which includes each MCP server's tool names), the slash-command " +
	"names, the skill names, the subagent names in `agents`, `plugins`, `capabilities`, `output_style`, " +
	"`apiKeySource`, `permissionMode`, and the analytics_disabled / product_feedback_disabled flags. None " +
	"of it is a credential, and it is kept because system/init's payload is exactly what #1261-#1264 have " +
	"to map. It is nonetheless a description of one machine's setup rather than of claude, so whether to " +
	"publish it is the operator's call — which is the point of stating it here instead of leaving a reader " +
	"to notice it."

// --- the recorder ------------------------------------------------------------

// dropcapCaptured is one complete line of claude's stdout, verbatim. Raw
// excludes the '\n' delimiter and nothing else; it is captured BEFORE any
// redaction so the substitution counts describe a known input.
type dropcapCaptured struct {
	Index   int
	Raw     []byte
	Type    string
	Subtype string
	Decoded bool
}

// dropcapCaps is the non-silent accounting behind the two caps plus the two
// line shapes that are not payloads.
type dropcapCaps struct {
	LinesOverCap        int
	BytesOverCap        int
	PartialsDropped     int
	BlankLines          int
	UnterminatedPartial int
}

// dropcapRecorder is an io.Writer wired into streamsup.Config.Stdout, where
// production installs the parser. It accumulates, splits on '\n', keeps each
// complete line's raw bytes, and closes resultSeen on the first `result` line —
// the turn boundary the test waits on.
//
// Mutex-guarded because os/exec drives Stdout from one internal copier
// goroutine while the test goroutine reads the accumulated lines; that is a
// race under -race (probeSyncBuffer is
// the local precedent). It does NOT forward to a live parser: the turn needs
// none, and classification runs later on the test goroutine, where parseOne's
// *testing.T is legal to use.
type dropcapRecorder struct {
	mu      sync.Mutex
	partial []byte
	lines   []dropcapCaptured
	seen    int
	bytes   int
	caps    dropcapCaps

	// maxPartial caps the accumulator. Carried as a per-recorder field rather
	// than read from the constant so a test can shrink it without racing a
	// shared global — the same reasoning, and the same shape, as the parser's
	// own maxBuf (`defaultMaxParseBuf` in parser.go).
	maxPartial int
	// skipUntilNewline is set when the accumulator was discarded at maxPartial.
	// The TAIL of that over-long line is a FRAGMENT, not a line: appending it
	// would put a partial payload in the record with nothing at the entry saying
	// so, which is the mutation AC3 forbids going unstated. Cleared at the next
	// '\n', where the next real line begins.
	skipUntilNewline bool

	resultOnce sync.Once
	resultSeen chan struct{}
}

var _ io.Writer = (*dropcapRecorder)(nil)

func newDropcapRecorder() *dropcapRecorder {
	return &dropcapRecorder{maxPartial: dropcapMaxPartial, resultSeen: make(chan struct{})}
}

func (r *dropcapRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.partial = append(r.partial, b...)
	rest := r.partial
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		if r.skipUntilNewline {
			// The tail of a line whose head was already discarded. The discard is
			// counted in PartialsDropped; the fragment is not recorded.
			r.skipUntilNewline = false
		} else {
			r.consume(rest[:i])
		}
		rest = rest[i+1:]
	}
	if len(rest) > r.maxPartial {
		r.caps.PartialsDropped++
		r.skipUntilNewline = true
		rest = nil
	}
	// Copy so the (possibly large) backing array is released, mirroring
	// Parser.Write.
	r.partial = append([]byte(nil), rest...)
	return len(b), nil
}

// consume records one complete line. Called under r.mu.
func (r *dropcapRecorder) consume(line []byte) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		// consumeLine returns on a blank line before any mapping, so it is not a
		// dropped payload. Counted rather than silently skipped.
		r.caps.BlankLines++
		return
	}

	var sl struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	decoded := json.Unmarshal(trimmed, &sl) == nil

	idx := r.seen
	r.seen++

	// A line that would cross the budget is dropped WHOLE: individual payloads
	// are never truncated (AC3), so the cap drops entire lines and says how many.
	if r.bytes+len(line) > dropcapMaxCaptureBytes {
		r.caps.LinesOverCap++
		r.caps.BytesOverCap += len(line)
	} else {
		r.lines = append(r.lines, dropcapCaptured{
			Index:   idx,
			Raw:     append([]byte(nil), line...),
			Type:    sl.Type,
			Subtype: sl.Subtype,
			Decoded: decoded,
		})
		r.bytes += len(line)
	}

	if decoded && sl.Type == "result" {
		r.resultOnce.Do(func() { close(r.resultSeen) })
	}
}

// snapshot returns a copy of what has been captured so far, plus the cap
// accounting. The unterminated partial is reported by LENGTH only: it is an
// incomplete line, not a line, and it is data that the stream ended mid-line.
func (r *dropcapRecorder) snapshot() ([]dropcapCaptured, dropcapCaps) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]dropcapCaptured, len(r.lines))
	copy(out, r.lines)
	caps := r.caps
	caps.UnterminatedPartial = len(r.partial)
	return out, caps
}

// --- redaction ---------------------------------------------------------------

// The substitution classes. class is a NAME, never the value: writing the
// matched value into the record would put the very string being redacted into
// the fixture.
const (
	dropcapClassTempHome     = "temp_home"
	dropcapClassOperatorHome = "operator_home"
	dropcapClassTempDir      = "temp_dir"
	dropcapClassArtifactDir  = "artifact_dir"
	dropcapClassWorkdir      = "workdir"
	dropcapClassFIFOPath     = "fifo_path"
	dropcapClassSessionID    = "session_id"
	dropcapClassNonce        = "prompt_nonce"
)

// dropcapSubstitution declares one applied class. count sits with the value it
// describes, which is AC3's "the file saying so at the point where it happened".
type dropcapSubstitution struct {
	Class       string `json:"class"`
	Replacement string `json:"replacement"`
	Count       int    `json:"count"`
}

type dropcapRule struct {
	class       string
	value       string
	replacement string
}

// dropcapRedactor applies a declared table longest-value-first, so a shorter
// path cannot shadow a longer one that contains it.
//
// All redaction runs on the test goroutine (during the run for t.Logf, and
// after the turn for the record), so the counters need no lock.
type dropcapRedactor struct {
	rules  []dropcapRule
	counts map[string]int
}

// dropcapSlugSeparators are the characters claude's project-slug encoding
// rewrites to '-'. MEASURED, not guessed: the 2026-08-02 capture's system/init
// carried memory_paths.auto = <home>/.claude/projects/<workdir with every '/'
// and '_' rewritten to '-'>/memory/, which the path table did not know and the
// deny-scan's slash-bearing needles could not see. '.' is included because the
// same encoding flattens dotted directory names.
const dropcapSlugSeparators = "/_."

// dropcapSlug returns the project-slug spelling of a path.
func dropcapSlug(path string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(dropcapSlugSeparators, r) {
			return '-'
		}
		return r
	}, path)
}

// dropcapPathSpellings enumerates the spellings of one path a payload can carry:
// the path itself, its filepath.EvalSymlinks form (on macOS the temp root
// resolves /var/… -> /private/var/…, and agentrun.ResolveWorkdir performs the
// same mapping, per streamsup Config's WorkDir), and the project-slug encoding of
// each. Duplicates and empties are dropped, so a path whose forms coincide
// contributes one spelling, not four.
func dropcapPathSpellings(path string) []string {
	if path == "" {
		return nil
	}
	forms := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		forms = append(forms, resolved)
	}
	out := []string{}
	for _, f := range forms {
		for _, spelling := range []string{f, dropcapSlug(f)} {
			if spelling != "" && !dropcapContains(out, spelling) {
				out = append(out, spelling)
			}
		}
	}
	return out
}

// newDropcapRedactor builds the table: one rule per spelling of each path class,
// all sharing the class's name and replacement.
//
// The empty-value guard is load-bearing, not defensive noise:
// strings.ReplaceAll(s, "", x) inserts x between EVERY character
// (bgIdleRedact is the same lesson).
func newDropcapRedactor(tempHome, artifactDir, workdir, fifoPath, sessionID string, nonce int64) *dropcapRedactor {
	r := &dropcapRedactor{counts: map[string]int{}}
	addPath := func(class, replacement, path string) {
		for _, spelling := range dropcapPathSpellings(path) {
			r.add(class, replacement, spelling)
		}
	}
	addPath(dropcapClassFIFOPath, "$FIFO", fifoPath)
	addPath(dropcapClassWorkdir, "$WORKDIR", workdir)
	addPath(dropcapClassArtifactDir, "$ARTIFACT_DIR", artifactDir)
	addPath(dropcapClassTempHome, "$TEMP_HOME", tempHome)
	addPath(dropcapClassOperatorHome, "$HOME", realHome)
	addPath(dropcapClassTempDir, "$TMPDIR", strings.TrimSuffix(os.TempDir(), "/"))
	r.add(dropcapClassSessionID, "$SESSION_ID", sessionID)
	r.add(dropcapClassNonce, "$NONCE", strconv.FormatInt(nonce, 10))

	sort.SliceStable(r.rules, func(i, j int) bool {
		return len(r.rules[i].value) > len(r.rules[j].value)
	})
	return r
}

func (r *dropcapRedactor) add(class, replacement, value string) {
	if value == "" {
		return
	}
	for _, existing := range r.rules {
		if existing.value == value {
			return
		}
	}
	r.rules = append(r.rules, dropcapRule{class: class, value: value, replacement: replacement})
}

// redact substitutes every rule into b, counting each application by class.
func (r *dropcapRedactor) redact(b []byte) []byte {
	for _, rule := range r.rules {
		n := bytes.Count(b, []byte(rule.value))
		if n == 0 {
			continue
		}
		r.counts[rule.class] += n
		b = bytes.ReplaceAll(b, []byte(rule.value), []byte(rule.replacement))
	}
	return b
}

// str is redact for a string field. Every field assigned into the record, every
// t.Logf and both fifoLiveOutcome values go through it — fifoLiveOutcome carries
// the FIFO path in BOTH Path and Detail (`fifoLiveRead` fills them),
// so those two fields leak an absolute path with no payload involved.
func (r *dropcapRedactor) str(s string) string { return string(r.redact([]byte(s))) }

func (r *dropcapRedactor) strs(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = r.str(s)
	}
	return out
}

func (r *dropcapRedactor) outcome(o fifoLiveOutcome) *fifoLiveOutcome {
	o.Path = r.str(o.Path)
	o.Detail = r.str(o.Detail)
	return &o
}

// substitutions returns the applied classes, deterministically ordered.
func (r *dropcapRedactor) substitutions() []dropcapSubstitution {
	byClass := map[string]string{}
	for _, rule := range r.rules {
		byClass[rule.class] = rule.replacement
	}
	out := []dropcapSubstitution{}
	for class, count := range r.counts {
		if count == 0 {
			continue
		}
		out = append(out, dropcapSubstitution{Class: class, Replacement: byClass[class], Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out
}

// --- the deny-scan -----------------------------------------------------------

// dropcapMinNeedle is the minimum length of a DYNAMIC needle — one whose value
// is not known at authoring time (a credential read from the environment, a
// runtime path). bytes.Contains(x, []byte("")) is ALWAYS true and a two-byte
// credential matches nearly every payload, so a short dynamic needle inverts the
// instrument. Such a needle is skipped and REPORTED as not applied: an arm that
// silently did not run must never read as an arm that ran and found nothing.
//
// Fixed literals below (sk-ant-, /Users/, …) are deliberately chosen at
// authoring time and are exempt — the hazard is an unknown value, not a short
// one, and applying the minimum to them would disable the whole net.
const dropcapMinNeedle = 16

// A deny class is a NAME, and it must not CONTAIN its own needle. That is not
// style: credential_scan_applied is a map keyed by class and it ships inside the
// record, so a class spelled "sk-ant-prefix" makes the scan hit its own metadata
// on every run — a fail-closed net that can never pass is as useless as one that
// never fires, and it reports the instrument's own breakage as a finding.
// Measured, not predicted: the first live capture failed exactly this way.
// TestDropcapDenyClassNamesDoNotCarryTheirNeedle is the guard.
const (
	dropcapDenySkAnt = "anthropic-key-prefix"
	dropcapDenyUsers = "users-path-prefix"
	dropcapDenyHome  = "home-path-prefix"
	dropcapDenyVarF  = "var-folders-prefix"
	dropcapDenyPVarF = "private-var-folders-prefix"
)

type dropcapNeedle struct {
	class string
	value string
	// dynamic marks a needle whose value came from the environment or from a
	// runtime path, and is therefore subject to dropcapMinNeedle.
	dynamic bool
}

type dropcapScanner struct{ needles []dropcapNeedle }

// newDropcapScanner builds the net. Credential VALUES are read via os.Getenv
// solely as needles: they are never stored in a record field, never logged and
// never written. That is the one deliberate exception to this file's
// "os.Environ() is never called" rule, and it is greppable on purpose.
func newDropcapScanner(tempHome, artifactDir, workdir string) dropcapScanner {
	s := dropcapScanner{}
	s.addDynamic("CLAUDE_CODE_OAUTH_TOKEN", os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"))
	s.addDynamic("ANTHROPIC_API_KEY", os.Getenv("ANTHROPIC_API_KEY"))
	s.addDynamicPath(dropcapClassTempHome, tempHome)
	s.addDynamicPath(dropcapClassOperatorHome, realHome)
	s.addDynamicPath(dropcapClassArtifactDir, artifactDir)
	s.addDynamicPath(dropcapClassWorkdir, workdir)
	s.needles = append(s.needles, dropcapFixedNeedles()...)
	return s
}

// dropcapFixedNeedles is the version-independent half of the net: it needs no
// knowledge of the run that produced a record, which is what lets the offline
// fixture validation re-scan a COMMITTED capture forever.
func dropcapFixedNeedles() []dropcapNeedle {
	return []dropcapNeedle{
		{class: dropcapDenySkAnt, value: "sk-ant-"},
		{class: dropcapDenyUsers, value: "/Users/"},
		{class: dropcapDenyHome, value: "/home/"},
		{class: dropcapDenyPVarF, value: "/private/var/folders/"},
		{class: dropcapDenyVarF, value: "/var/folders/"},
	}
}

func (s *dropcapScanner) addDynamic(class, value string) {
	s.needles = append(s.needles, dropcapNeedle{class: class, value: value, dynamic: true})
}

// addDynamicPath adds every spelling of path, plus every SEGMENT of those
// spellings that is at least dropcapMinNeedle bytes long.
//
// The segments are what makes the net separator-independent, and they are not
// speculative: the 2026-08-02 capture leaked the workdir through claude's
// project-slug encoding, which no slash-bearing needle could see. A segment
// (the per-user $TMPDIR component, the per-test directory name) survives any
// re-spelling that keeps it contiguous, so it catches a mangling this file has
// not measured — which is the whole point of a net behind a table.
func (s *dropcapScanner) addDynamicPath(class, path string) {
	for _, spelling := range dropcapPathSpellings(path) {
		s.addDynamic(class, spelling)
		for _, seg := range strings.FieldsFunc(spelling, func(r rune) bool {
			return r == '/' || r == '-'
		}) {
			if len(seg) >= dropcapMinNeedle {
				s.addDynamic(class, seg)
			}
		}
	}
}

// scan reports the classes still present in b, and the classes whose needle was
// too short to search for. Each literal is checked RAW and in its
// JSON-string-escaped form: the record embeds payloads inside JSON strings,
// where encoding/json rewrites each of the three HTML metacharacters (ampersand,
// less-than, greater-than) into a six-byte backslash-u escape, so a needle
// carrying one is ABSENT in raw form and present only in escaped form.
func (s dropcapScanner) scan(b []byte) (hits, notApplied []string) {
	seenHit := map[string]bool{}
	seenSkip := map[string]bool{}
	for _, n := range s.needles {
		if n.dynamic && len(n.value) < dropcapMinNeedle {
			if !seenSkip[n.class] {
				seenSkip[n.class] = true
				notApplied = append(notApplied, n.class)
			}
			continue
		}
		if n.value == "" {
			continue
		}
		if bytes.Contains(b, []byte(n.value)) || bytes.Contains(b, []byte(dropcapJSONEscape(n.value))) {
			if !seenHit[n.class] {
				seenHit[n.class] = true
				hits = append(hits, n.class)
			}
		}
	}
	sort.Strings(hits)
	sort.Strings(notApplied)
	return hits, notApplied
}

// applied reports, per class, whether the needle actually ran — so
// credential_scan_applied:false is a recorded fact rather than an absence.
func (s dropcapScanner) applied() map[string]bool {
	out := map[string]bool{}
	for _, n := range s.needles {
		ok := !(n.dynamic && len(n.value) < dropcapMinNeedle)
		if prev, seen := out[n.class]; !seen || (prev && !ok) {
			out[n.class] = ok
		}
	}
	return out
}

// dropcapJSONEscape returns s as it appears INSIDE a JSON string, quotes
// stripped.
func dropcapJSONEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

// --- the record --------------------------------------------------------------

type dropcapEntry struct {
	Index                   int    `json:"index"`
	Type                    string `json:"type"`
	Subtype                 string `json:"subtype,omitempty"`
	Reason                  string `json:"reason"`
	PayloadLenBytesCaptured int    `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int    `json:"payload_len_bytes"`
	PayloadEncoding         string `json:"payload_encoding"`
	Payload                 string `json:"payload,omitempty"`
	PayloadB64              string `json:"payload_b64,omitempty"`
}

// dropcapNudge is AC2's user/text block. Payload is the ISOLATED single-block
// user line, so the offline pin test can replay it through the shipped parser.
type dropcapNudge struct {
	Observed               bool   `json:"observed"`
	MatchesShippedConstant bool   `json:"matches_shipped_constant"`
	BlockCount             int    `json:"block_count"`
	LineIndex              int    `json:"line_index"`
	PayloadEncoding        string `json:"payload_encoding,omitempty"`
	Payload                string `json:"payload,omitempty"`
	PayloadB64             string `json:"payload_b64,omitempty"`
}

type dropcapRecord struct {
	Ticket          string   `json:"ticket"`
	ClaudeVersion   string   `json:"claude_version"`
	CapturedAt      string   `json:"captured_at"`
	IsCapture       bool     `json:"is_capture"`
	Model           string   `json:"model"`
	SpawnShape      []string `json:"spawn_shape"`
	SpawnShapeDelta string   `json:"spawn_shape_delta"`
	EnvDelta        []string `json:"env_delta"`
	Workdir         string   `json:"workdir"`
	FIFOPath        string   `json:"fifo_path"`
	Prompt          string   `json:"prompt"`

	Outcome           string           `json:"outcome"`
	OutcomeDetail     string           `json:"outcome_detail"`
	AbsenceClaimValid bool             `json:"absence_claim_valid"`
	PreRendezvousRead *fifoLiveOutcome `json:"pre_rendezvous_read"`
	TurnEndRead       *fifoLiveOutcome `json:"turn_end_read"`
	RendezvousAt      *string          `json:"rendezvous_at"`
	TerminatedOn      string           `json:"terminated_on"`

	LinesCaptured          int            `json:"lines_captured"`
	DroppedLines           []dropcapEntry `json:"dropped_lines"`
	Census                 map[string]int `json:"census"`
	ExpectedAbsent         []string       `json:"expected_absent"`
	RateLimitEventObserved bool           `json:"rate_limit_event_observed"`
	HarnessNudge           dropcapNudge   `json:"harness_nudge"`

	Redaction             []dropcapSubstitution `json:"redaction"`
	RedactionRationale    string                `json:"redaction_rationale"`
	CredentialScanApplied map[string]bool       `json:"credential_scan_applied"`

	Limitations string `json:"limitations"`

	LinesDroppedOverCap    int `json:"lines_dropped_over_cap"`
	BytesDroppedOverCap    int `json:"bytes_dropped_over_cap"`
	PartialsDropped        int `json:"partials_dropped"`
	BlankLines             int `json:"blank_lines"`
	UnterminatedPartialLen int `json:"unterminated_partial_len"`
}

func (rec *dropcapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
	rec.AbsenceClaimValid = outcome == dropcapFired
}

// --- the argv observer -------------------------------------------------------

// dropcapArgvHandler is a slog.Handler that WRITES NOTHING and keeps only the
// argv from the runner's own "spawning claude" record (logged by `Restart`).
// spawn_shape is therefore OBSERVED from production's buildArgs output rather
// than transcribed into this file, where it could drift from the shape it claims
// to measure. It doubles as the explicit discard handler the runner needs:
// Config.Logger == nil falls back to slog.Default() (streamsup's `Run`), which
// would put the runner's lifecycle lines into CI output.
type dropcapArgvHandler struct {
	mu   *sync.Mutex
	argv *[]string
}

func newDropcapArgvHandler() (slog.Handler, func() []string) {
	var (
		mu   sync.Mutex
		argv []string
	)
	h := dropcapArgvHandler{mu: &mu, argv: &argv}
	return h, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), argv...)
	}
}

func (h dropcapArgvHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h dropcapArgvHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "spawning claude" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(*h.argv) > 0 {
		return nil
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != "args" {
			return true
		}
		if v, ok := a.Value.Any().([]string); ok {
			*h.argv = append([]string(nil), v...)
		}
		return false
	})
	return nil
}

func (h dropcapArgvHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h dropcapArgvHandler) WithGroup(string) slog.Handler      { return h }

// --- the live capture --------------------------------------------------------

// TestRealClaude_DroppedLineCapture stages one live interactive turn that
// backgrounds a Bash command and records, verbatim, every line the shipped
// parser drops. It asserts nothing about claude.
func TestRealClaude_DroppedLineCapture(t *testing.T) {
	if os.Getenv(dropcapEnableEnv) != "1" {
		t.Skipf("#1260 dropped-line capture: skipped because %s != 1.\n"+
			"This is an EVIDENCE CAPTURE, not a regression gate — a skip here is the normal "+
			"`make e2e-realclaude` outcome and carries no signal about pyry's behaviour. It "+
			"costs one live claude turn.\n"+
			"Run it explicitly:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 15m -v \\\n"+
			"    -run '^TestRealClaude_DroppedLineCapture$' ./internal/e2e/realclaude/",
			dropcapEnableEnv, dropcapEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials

	// Deliberately NOT t.TempDir(): the operator needs the record after the test
	// ends in order to commit it as the fixture.
	artifactDir, err := os.MkdirTemp("", dropcapArtifactPrefix)
	if err != nil {
		t.Fatalf("#1260: create artifact dir: %v", err)
	}

	// A fresh EMPTY directory, deliberately not a git repo: no branch names and
	// no file contents can reach a payload (§ redaction, by construction).
	workdir := filepath.Join(home, dropcapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#1260: create workdir: %v", err)
	}
	fifoPath := filepath.Join(workdir, dropcapFIFOName)
	nonce := time.Now().UnixNano()

	red := newDropcapRedactor(home, artifactDir, workdir, fifoPath, dropcapSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#1260 capture artifacts: %s", red.str(artifactDir))

	rec := &dropcapRecord{
		Ticket:          dropcapTicket,
		ClaudeVersion:   probeClaudeVersion(claudeBin),
		CapturedAt:      time.Now().Format(time.RFC3339),
		IsCapture:       true,
		Model:           dropcapModel,
		SpawnShapeDelta: dropcapSpawnShapeDelta,
		// A FIXED LITERAL, never harvested from os.Environ() — see the header.
		EnvDelta:              []string{dropcapBashTimeoutEnv + "=" + dropcapBashTimeoutMS},
		Workdir:               red.str(workdir),
		FIFOPath:              red.str(fifoPath),
		Census:                map[string]int{},
		DroppedLines:          []dropcapEntry{},
		ExpectedAbsent:        []string{},
		RedactionRationale:    dropcapRedactionRationale,
		CredentialScanApplied: scanner.applied(),
		Limitations:           dropcapLimitations,
	}
	rec.set(dropcapInstrumentBroken, "did not reach a classification point")

	// Registered FIRST in this body so t.Cleanup's LIFO runs it LAST of ours: the
	// resulting order is runner ctx cancelled -> FIFO released -> record written,
	// and a structural t.Fatalf below still leaves the evidence on disk.
	t.Cleanup(func() { dropcapWriteRecord(t, artifactDir, red, scanner, rec) })

	// MUST precede the runner: Config.Env stays nil so cmd.Env is nil and the
	// child inherits this process's environment verbatim (streamsup's `Run`).
	t.Setenv(dropcapBashTimeoutEnv, dropcapBashTimeoutMS)

	rendezvous := holdProbeFIFO(t, fifoPath)

	// The control read. After holdProbeFIFO (the path must exist, else the read
	// fails on the Lstat arm) and before the turn, so the only correct answer is
	// no-reader; anything else means the instrument does not discriminate here.
	preRead := fifoLiveRead(fifoPath)
	rec.PreRendezvousRead = red.outcome(preRead)

	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: dropcapSessionID,
		Args:      dropcapArgs,
		Stdout:    recorder,
		Logger:    slog.New(argvHandler),
	})
	if err != nil {
		rec.set(dropcapInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
			red.str(err.Error()))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = runner.Run(ctx)
	}()
	// Registered after holdProbeFIFO, so it runs BEFORE the FIFO release: the
	// descendant reap on ctx cancel (streamsup's `Run`) kills the backgrounded
	// `cat`, and closing the last write end is the backstop if the reap missed.
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(dropcapRunExitWait):
			t.Errorf("#1260: streamsup.Run did not return within %s of cancel", dropcapRunExitWait)
		}
	})

	// The rendezvous is stamped on its own goroutine into a BUFFERED(1) channel,
	// never into rec: a direct field write would race the test goroutine under
	// -race. The buffer is what keeps the STAMPER from parking on its send when
	// the turn ends before the rendezvous fires and the read below takes its
	// default arm. It is not what unblocks the receive: `rendezvous` is closed by
	// holdProbeFIFO's HOLD goroutine once its open(O_WRONLY) returns, which the
	// helper's cleanup arranges by opening the read end non-blockingly
	// (`holdProbeFIFO`) — and which does not happen at
	// all if that open errored, leaving this goroutine parked on the receive for
	// the rest of the binary. One parked, non-writing goroutine is the bounded
	// residue; a racing field write would not be.
	rendezvousAt := make(chan time.Time, 1)
	go func() {
		<-rendezvous
		rendezvousAt <- time.Now()
	}()

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(dropcapInstrumentBroken, "no live child within %s: claude never spawned, so nothing "+
			"was on the wire to capture", dropcapSpawnWait)
		return
	}

	prompt := bgIdlePrompt(fifoPath, nonce)
	rec.Prompt = red.str(prompt)
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.set(dropcapInstrumentBroken, "writing the turn envelope failed, so no turn was ever "+
			"driven: %v", red.str(err.Error()))
		return
	}

	select {
	case <-recorder.resultSeen:
		rec.TerminatedOn = dropcapTerminatedResult
	case <-time.After(dropcapTurnBudget):
		rec.TerminatedOn = dropcapTerminatedBudget
	}

	// Taken in the test body, before any cleanup runs, so it is never taken after
	// holdProbeFIFO releases the write end — a read after that would be measuring
	// a `cat` the release itself killed.
	turnEndRead := fifoLiveRead(fifoPath)
	rec.TurnEndRead = red.outcome(turnEndRead)

	select {
	case ts := <-rendezvousAt:
		at := ts.Format(time.RFC3339Nano)
		rec.RendezvousAt = &at
	default:
	}

	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	rec.DroppedLines = dropcapClassifyAll(t, lines, red)
	rec.Census = dropcapCensus(rec.DroppedLines)
	rec.ExpectedAbsent = dropcapExpectedAbsent(rec.Census)
	rec.RateLimitEventObserved = rec.Census["rate_limit_event"] > 0
	rec.HarnessNudge = dropcapFindNudge(t, lines, red)

	dropcapClassifyOutcome(rec, preRead, turnEndRead)
}

// dropcapWaitForChild polls Runner.Stdin() until a child is live. WriteTurn maps
// a nil writer to ErrNoLiveChild, so this is the pre-spawn
// window the turn has to poll through.
func dropcapWaitForChild(runner *streamsup.Runner) io.Writer {
	deadline := time.Now().Add(dropcapSpawnWait)
	for {
		if w := runner.Stdin(); w != nil {
			return w
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		time.Sleep(dropcapSpawnPoll)
	}
}

// dropcapClassifyOutcome fills the outcome. Only `fired` licenses an absence
// claim: the probe holds the FIFO's only write end for the whole window, so
// `cat` cannot have FINISHED; reader-present at turn end excludes its having
// been KILLED; and the pre-read control is what makes reader-present
// non-vacuous.
func dropcapClassifyOutcome(rec *dropcapRecord, pre, turnEnd fifoLiveOutcome) {
	switch {
	case pre.Verdict != fifoLiveNoReader:
		rec.set(dropcapInstrumentBroken, "the pre-turn liveness read returned %q although no reader "+
			"had ever opened the FIFO; the read is not discriminating in this rig, and one pinned to "+
			"%q would answer the turn-end read the same way", pre.Verdict, fifoLiveReaderPresent)
	case rec.LinesCaptured == 0:
		rec.set(dropcapInstrumentBroken, "zero lines were captured from claude's stdout, so the "+
			"recorder observed nothing and no census exists")
	case rec.RendezvousAt == nil:
		rec.set(dropcapDidNotFire, "the FIFO rendezvous never fired: claude never started the Bash "+
			"command, so no backgrounding occurred and this run supports no absence claim")
	case turnEnd.Verdict != fifoLiveReaderPresent:
		rec.set(dropcapDidNotFire, "the command started but the turn-end liveness read returned %q, "+
			"so nothing held the read end when the turn ended: the command was not left running in "+
			"the background and this run supports no absence claim", turnEnd.Verdict)
	default:
		rec.set(dropcapFired, "a Bash command was left running in the background: the rendezvous "+
			"fired and a process still held the FIFO's read end at turn end (terminated_on=%s). "+
			"The per-subtype census and expected_absent are valid for THIS turn", rec.TerminatedOn)
	}
}

// --- classification ----------------------------------------------------------

// dropcapClassifyAll asks the SHIPPED parser which captured lines it drops.
// "Zero events emitted" and "dropped" are the same predicate by construction:
// emitUnrecognized is reached only AFTER the ignored-type return at
// `benignRateLimitStatus`, so a line the parser tolerates emits nothing and a line it does
// not recognise emits a turnevent.Unrecognized.
//
// A fresh parser per line is licensed by the documented turn-statelessness
// (`maxTaskRosterEntries`): the only cross-line state is the partial-line buffer,
// which a complete line never uses.
func dropcapClassifyAll(t *testing.T, lines []dropcapCaptured, red *dropcapRedactor) []dropcapEntry {
	t.Helper()
	out := []dropcapEntry{}
	for _, c := range lines {
		reason, dropped := dropcapClassify(t, c)
		if !dropped {
			continue
		}
		out = append(out, dropcapMakeEntry(c, reason, red))
	}
	return out
}

// dropcapClassify reports why a line was dropped, or dropped == false when the
// parser mapped or surfaced it.
func dropcapClassify(t *testing.T, c dropcapCaptured) (reason string, dropped bool) {
	t.Helper()
	if !c.Decoded {
		// Data about claude, not a broken instrument. The shipped parser surfaces
		// such a line as Unrecognized{undecodable} rather than dropping it, which
		// is why it carries its own reason and never inflates a system/* count.
		return dropcapReasonUndecodable, true
	}
	if len(parseOne(t, string(c.Raw))) != 0 {
		return "", false
	}
	switch c.Type {
	case "user":
		return dropcapReasonUserBlock, true
	case "assistant":
		return dropcapReasonEmptyMsg, true
	default:
		return dropcapReasonIgnoredType, true
	}
}

// dropcapMakeEntry builds one record entry. payload_len_bytes_captured versus
// payload_len_bytes makes any length change from redaction visible AT the entry
// where it happened; no payload is ever truncated, and nothing is ever stripped
// of invalid bytes.
func dropcapMakeEntry(c dropcapCaptured, reason string, red *dropcapRedactor) dropcapEntry {
	e := dropcapEntry{
		Index:                   c.Index,
		Type:                    c.Type,
		Subtype:                 c.Subtype,
		Reason:                  reason,
		PayloadLenBytesCaptured: len(c.Raw),
	}
	payload := red.redact(append([]byte(nil), c.Raw...))
	e.PayloadLenBytes = len(payload)
	if utf8.Valid(payload) {
		e.PayloadEncoding = dropcapEncodingJSONString
		e.Payload = string(payload)
		return e
	}
	// Go's encoder replaces invalid UTF-8 with U+FFFD inside a JSON string, so
	// base64 is the only lossless container for these bytes.
	e.PayloadEncoding = dropcapEncodingBase64
	e.PayloadB64 = base64.StdEncoding.EncodeToString(payload)
	return e
}

// dropcapCensusKey keys the per-subtype census. An undecodable line has no type
// to report, so it gets its own bucket rather than an empty "/" key.
func dropcapCensusKey(e dropcapEntry) string {
	if e.Reason == dropcapReasonUndecodable {
		return "<undecodable>"
	}
	if e.Subtype == "" {
		return e.Type
	}
	return e.Type + "/" + e.Subtype
}

func dropcapCensus(entries []dropcapEntry) map[string]int {
	census := map[string]int{}
	for _, e := range entries {
		census[dropcapCensusKey(e)]++
	}
	return census
}

// dropcapExpectedAbsent names which of the four #1247 subtypes had count 0, so a
// later absence is distinguishable from a measurement that never looked.
func dropcapExpectedAbsent(census map[string]int) []string {
	absent := []string{}
	for _, s := range dropcapExpectedSubtypes {
		if census["system/"+s] == 0 {
			absent = append(absent, s)
		}
	}
	return absent
}

// dropcapFindNudge runs the block-level pass AC2 needs, independently of the
// line classification: every `user` line's content is decoded and each `text`
// block recorded verbatim.
//
// Whether the block byte-matched the shipped harnessNoOutputNudge is decided by
// the same parser, never by a copy of the constant. The block is re-wrapped into
// an ISOLATED single-block user line (its own bytes verbatim) and fed to
// parseOne:
//
//   - an Unrecognized{Site: user_block, Kind: "text"} present => it did NOT match
//   - absent => it matched, and this is the SECOND confirmed observation
//     harnessNoOutputNudge's doc comment names as the thing
//     that promotes the constant to a set with a pin test.
func dropcapFindNudge(t *testing.T, lines []dropcapCaptured, red *dropcapRedactor) dropcapNudge {
	t.Helper()
	var nudge dropcapNudge
	for _, c := range lines {
		if !c.Decoded || c.Type != "user" {
			continue
		}
		var sl struct {
			Message *struct {
				Content []json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(c.Raw), &sl); err != nil || sl.Message == nil {
			continue
		}
		for _, raw := range sl.Message.Content {
			var block struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &block); err != nil || block.Type != "text" {
				continue
			}
			nudge.BlockCount++
			if nudge.Observed {
				continue
			}
			nudge.Observed = true
			nudge.LineIndex = c.Index
			isolated := dropcapIsolateUserBlock(raw)
			nudge.MatchesShippedConstant = !dropcapHasUserBlockUnrecognized(parseOne(t, string(isolated)))
			payload := red.redact(isolated)
			if utf8.Valid(payload) {
				nudge.PayloadEncoding = dropcapEncodingJSONString
				nudge.Payload = string(payload)
			} else {
				nudge.PayloadEncoding = dropcapEncodingBase64
				nudge.PayloadB64 = base64.StdEncoding.EncodeToString(payload)
			}
		}
	}
	return nudge
}

// dropcapIsolateUserBlock wraps one content block, byte-for-byte, in a minimal
// user line so the parser's verdict is unambiguous per block.
func dropcapIsolateUserBlock(block json.RawMessage) []byte {
	out := []byte(`{"type":"user","message":{"role":"user","content":[`)
	out = append(out, block...)
	return append(out, ']', '}', '}')
}

func dropcapHasUserBlockUnrecognized(events []turnevent.Event) bool {
	for _, ev := range events {
		if u, ok := ev.(turnevent.Unrecognized); ok && u.Site == turnevent.UnrecognizedUserBlock {
			return true
		}
	}
	return false
}

// --- writing the record ------------------------------------------------------

// dropcapB64Payload is one payload the record carries base64-encoded, plus the
// name of the field holding it. bytes.Contains cannot see through base64, so
// every one of these has to be decoded before the deny-scan can search it.
//
// It exists so there is ONE enumeration of those fields. The nudge lives outside
// dropped_lines, and a scan loop written over the entries alone silently omits
// it — which is exactly what happened here until code review caught it.
type dropcapB64Payload struct {
	where string
	b64   string
}

// decode returns the bytes to scan. A decode error is deliberately not fatal and
// not skipped: DecodeString returns what it managed before the error, and those
// bytes are as publishable as a clean decode's. The offline fixture validation
// reports the malformed encoding separately.
func (p dropcapB64Payload) decode() []byte {
	raw, _ := base64.StdEncoding.DecodeString(p.b64)
	return raw
}

func dropcapBase64Payloads(rec *dropcapRecord) []dropcapB64Payload {
	out := []dropcapB64Payload{}
	for _, e := range rec.DroppedLines {
		if e.PayloadEncoding == dropcapEncodingBase64 {
			out = append(out, dropcapB64Payload{where: fmt.Sprintf("entry %d", e.Index), b64: e.PayloadB64})
		}
	}
	if rec.HarnessNudge.PayloadEncoding == dropcapEncodingBase64 {
		out = append(out, dropcapB64Payload{where: "harness_nudge", b64: rec.HarnessNudge.PayloadB64})
	}
	return out
}

// dropcapWriteRecord is the fail-closed boundary. It marshals the (already
// substituted) record, runs the deny-scan over the WHOLE blob plus every
// base64 payload's decoded bytes, and only then writes at 0600 and logs.
//
// On a hit: t.Fatalf, and NO file is written — not the record, not the fixture.
// The message names the CLASS only. It must never print the payload, the matched
// value or the surrounding bytes; "let me include the raw payload to help debug
// it" is exactly how a token reaches CI output and inverts the whole control. An
// operator's fix is to extend the substitution table with the named class and
// re-run — one live turn, which is the correct price for not publishing a
// credential.
//
// Test output cannot be un-written, so the summary log is emitted only after the
// scan has passed.
func dropcapWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *dropcapRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#1260: marshal record: %v", err)
		return
	}
	hits, notApplied := scanner.scan(blob)
	// A base64 payload hides its bytes from a scan of the marshalled record, so
	// the decoded bytes are scanned too — for EVERY base64 payload the record
	// carries, which is the entries AND the harness nudge. The nudge sits outside
	// dropped_lines, so a loop over the entries alone would let a nudge block with
	// invalid UTF-8 take the base64 branch and bypass this net entirely.
	for _, p := range dropcapBase64Payloads(rec) {
		if h, _ := scanner.scan(p.decode()); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("#1260: deny-scan found %d denied class(es) still present in the record: %v\n"+
			"  located at: %v\n"+
			"NOTHING was written — not the record, not the fixture. Extend dropcapRedactor's table "+
			"with the named class and re-run the capture. The offending value is deliberately not "+
			"printed: putting it in CI output is exactly the exposure this scan exists to prevent",
			len(hits), hits, dropcapLocateHits(scanner, rec))
	}

	path := filepath.Join(dir, dropcapRecordName)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#1260: write record %s: %v", red.str(path), err)
		return
	}
	t.Logf("#1260 outcome=%s absence_claim_valid=%t terminated_on=%s captured=%d dropped=%d "+
		"census=%v expected_absent=%v nudge_observed=%t scan_not_applied=%v\n  record: %s\n  %s",
		rec.Outcome, rec.AbsenceClaimValid, rec.TerminatedOn, rec.LinesCaptured,
		len(rec.DroppedLines), rec.Census, rec.ExpectedAbsent, rec.HarnessNudge.Observed,
		notApplied, red.str(path), red.str(rec.OutcomeDetail))
}

// dropcapLocateHits narrows a hit to a CLASS plus an entry index — the two
// things the spec permits the failure message to carry. It re-scans each entry
// alone, each base64 payload's decoded bytes alone (the entries' and the
// nudge's), and the record frame (every field except the entries) alone, so an
// operator learns where to extend the table without any value being printed.
//
// The frame arm is not decorative: the first live capture's only hit was in the
// record's OWN metadata, not in a payload at all.
func dropcapLocateHits(scanner dropcapScanner, rec *dropcapRecord) []string {
	located := []string{}
	for _, e := range rec.DroppedLines {
		blob, err := json.Marshal(e)
		if err != nil {
			continue
		}
		if h, _ := scanner.scan(blob); len(h) > 0 {
			located = append(located, fmt.Sprintf("entry %d (%s) %v", e.Index, dropcapCensusKey(e), h))
		}
	}
	for _, p := range dropcapBase64Payloads(rec) {
		if h, _ := scanner.scan(p.decode()); len(h) > 0 {
			located = append(located, fmt.Sprintf("%s (base64 payload, decoded) %v", p.where, h))
		}
	}
	frame := *rec
	frame.DroppedLines = nil
	frame.HarnessNudge.Payload = ""
	frame.HarnessNudge.PayloadB64 = ""
	if blob, err := json.Marshal(frame); err == nil {
		if h, _ := scanner.scan(blob); len(h) > 0 {
			located = append(located, fmt.Sprintf("record frame (provenance/census/redaction fields, "+
				"no payload involved) %v", h))
		}
	}
	if len(located) == 0 {
		located = append(located, "not localised: the hit is in the harness_nudge json-string payload, "+
			"the one field the frame scan above blanks")
	}
	return located
}

// --- offline self-checks -----------------------------------------------------

// TestDropcapDenyClassNamesDoNotCarryTheirNeedle is the guard for a self-
// inverting net. credential_scan_applied ships inside the record keyed by class,
// so a class whose NAME contains its own needle makes the scan hit its own
// metadata on every run: it can never pass, and its failure is indistinguishable
// from a real leak. Measured, not predicted — the first live capture failed this
// way on a class spelled "sk-ant-prefix".
func TestDropcapDenyClassNamesDoNotCarryTheirNeedle(t *testing.T) {
	t.Parallel()
	scanner := dropcapScanner{needles: dropcapFixedNeedles()}
	blob, err := json.Marshal(scanner.applied())
	if err != nil {
		t.Fatalf("marshal credential_scan_applied: %v", err)
	}
	if hits, _ := scanner.scan(blob); len(hits) > 0 {
		t.Errorf("the deny-scan hits its own class-name map (%v): a class is a NAME and must not "+
			"contain the value it searches for, or the net can never pass", hits)
	}
	for _, n := range dropcapFixedNeedles() {
		if strings.Contains(n.class, n.value) {
			t.Errorf("class %q contains its own needle; rename it", n.class)
		}
	}
}

// TestDropcapRecorderSplitsLinesVerbatim is the check that stops an inverting
// failure: an instrument that mis-splits produces a wrong absence census, and
// the census is the whole deliverable. Runs offline.
func TestDropcapRecorderSplitsLinesVerbatim(t *testing.T) {
	t.Parallel()

	lines := []string{
		`{"type":"system","subtype":"init","cwd":"/tmp/x"}`,
		`{"type":"system","subtype":"thinking_tokens","text":"caf` + "\xc3\xa9" + `"}`,
		`{"type":"rate_limit_event"}`,
		`not json at all`,
	}
	// A blank line between two real lines, and a trailing line with NO newline.
	stream := lines[0] + "\n" + lines[1] + "\n\n" + lines[2] + "\n" + lines[3] + "\n" +
		`{"type":"assistant","message":null}`

	chunkings := []struct {
		name string
		size int
	}{
		{"one byte at a time", 1},
		{"three bytes", 3},
		{"seventeen bytes", 17},
		{"whole stream", len(stream)},
	}

	for _, ch := range chunkings {
		t.Run(ch.name, func(t *testing.T) {
			t.Parallel()
			r := newDropcapRecorder()
			for i := 0; i < len(stream); i += ch.size {
				end := i + ch.size
				if end > len(stream) {
					end = len(stream)
				}
				if n, err := r.Write([]byte(stream[i:end])); err != nil || n != end-i {
					t.Fatalf("Write returned (%d, %v); want (%d, nil)", n, err, end-i)
				}
			}
			got, caps := r.snapshot()
			if len(got) != len(lines) {
				t.Fatalf("captured %d lines; want %d — a mis-split changes the census", len(got), len(lines))
			}
			for i, want := range lines {
				if string(got[i].Raw) != want {
					t.Errorf("line %d = %q; want %q — the capture must be byte-identical and in order",
						i, got[i].Raw, want)
				}
				if got[i].Index != i {
					t.Errorf("line %d carries index %d; stream order is what the census is built on", i, got[i].Index)
				}
			}
			if caps.BlankLines != 1 {
				t.Errorf("blank_lines = %d; want 1 — a blank line is not a dropped payload, but "+
					"skipping it silently would hide a mis-split", caps.BlankLines)
			}
			// The final line has no trailing newline: it is an incomplete line, not
			// a line, and its length is reported rather than guessed at.
			if caps.UnterminatedPartial != len(`{"type":"assistant","message":null}`) {
				t.Errorf("unterminated partial = %d bytes; want %d", caps.UnterminatedPartial,
					len(`{"type":"assistant","message":null}`))
			}
			if got[3].Decoded {
				t.Error("a non-JSON line was recorded as decoded; it must reach the undecodable arm")
			}
			select {
			case <-r.resultSeen:
				t.Error("resultSeen closed although the stream carried no `result` line")
			default:
			}
		})
	}
}

// TestDropcapRecorderClosesOnResult pins the turn boundary the live test waits
// on, including that it fires exactly once.
func TestDropcapRecorderClosesOnResult(t *testing.T) {
	t.Parallel()
	r := newDropcapRecorder()
	if _, err := r.Write([]byte(`{"type":"result","subtype":"success"}` + "\n" +
		`{"type":"result","subtype":"success"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case <-r.resultSeen:
	default:
		t.Fatal("resultSeen not closed after a `result` line; the live turn would run to budget")
	}
}

// TestDropcapRecorderDiscardsAnOverlongLineWhole pins the discard as TOTAL. Past
// maxPartial the head of a line is gone, and the tail arriving after the next
// '\n' is a FRAGMENT: recording it would add an entry that no field labels as
// partial — the silent mutation AC3 forbids. Unobserved in practice
// (partials_dropped is 0 in the committed capture, and reaching it needs a
// newline-free line over 4 MiB), pinned because an instrument that quietly
// reclassifies a fragment as a line misstates the census.
func TestDropcapRecorderDiscardsAnOverlongLineWhole(t *testing.T) {
	t.Parallel()
	r := newDropcapRecorder()
	r.maxPartial = 64 // per-recorder, so shrinking it races no shared global

	const good = `{"type":"system","subtype":"init"}`
	// The head of one enormous line: over the cap with no newline in sight, so
	// the accumulator is discarded.
	if _, err := r.Write([]byte(strings.Repeat("x", 4*r.maxPartial))); err != nil {
		t.Fatalf("Write head: %v", err)
	}
	// Its tail, then a complete line behind it.
	if _, err := r.Write([]byte(`","done":true}` + "\n" + good + "\n")); err != nil {
		t.Fatalf("Write tail: %v", err)
	}

	lines, caps := r.snapshot()
	if caps.PartialsDropped != 1 {
		t.Errorf("partials_dropped = %d; want 1 — the discard must be counted, never silent",
			caps.PartialsDropped)
	}
	if len(lines) != 1 {
		t.Fatalf("captured %d line(s); want exactly the one COMPLETE line — the tail of a discarded "+
			"line is a fragment, not a payload", len(lines))
	}
	if string(lines[0].Raw) != good {
		t.Errorf("captured %q; want %q", lines[0].Raw, good)
	}
	if lines[0].Index != 0 {
		t.Errorf("index = %d; a fragment must not consume a stream index it never had", lines[0].Index)
	}
}

// TestDropcapClassification pins "dropped" against the SHIPPED parser. The
// control rows are load-bearing: without a row that must NOT be dropped, every
// zero-event assertion passes vacuously on a mis-wired classifier.
func TestDropcapClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		line       string
		wantReason string
		wantDrop   bool
		why        string
	}{
		{
			name: "system/init", line: `{"type":"system","subtype":"init","session_id":"s"}`,
			wantReason: dropcapReasonIgnoredType, wantDrop: true,
			why: "CORRECTED (#1600): the subtype IS one streamsup.emitSystemSubtype maps now — it " +
				"becomes a turnevent.ModelAnnounced. The verdict is unchanged and is derived rather " +
				"than declared: THIS line carries no `model`, which is emitModelAnnounced's empty " +
				"gate, so the shipped parser emits nothing for it and dropcapClassify's default arm " +
				"still reads it as a drop. Adding a model to this fixture flips it, which is what " +
				"makes the row a pin on the gate rather than on the subtype",
		},
		{
			name: "system/thinking_tokens", line: `{"type":"system","subtype":"thinking_tokens"}`,
			wantReason: dropcapReasonIgnoredType, wantDrop: true,
			why: "the highest-rate ignored subtype",
		},
		{
			name: "rate_limit_event", line: `{"type":"rate_limit_event"}`,
			wantReason: dropcapReasonIgnoredType, wantDrop: true,
			why: "CORRECTED (#1404): no longer the second ignoredLineTypes member — the type is " +
				"MAPPED now, from its own arm in streamsup's consumeLine. The verdict is unchanged " +
				"and is derived rather than declared: this line carries no decodable rate_limit_info, " +
				"which is emitRateLimit's rung 3, so the shipped parser makes no rate-limit claim for " +
				"it and dropcapClassify's default arm still reads it as a drop",
		},
		{
			name: "user with no mappable block", line: `{"type":"user","message":{"role":"user","content":[]}}`,
			wantReason: dropcapReasonUserBlock, wantDrop: true,
			why: "emitUser iterated zero blocks, so the line reached a client as nothing",
		},
		{
			name: "assistant with no mappable block", line: `{"type":"assistant","message":{"id":"m","content":[]}}`,
			wantReason: dropcapReasonEmptyMsg, wantDrop: true,
			why: "emitAssistant emits nothing for zero blocks",
		},
		{
			name: "undecodable line", line: `{"type":"system",`,
			wantReason: dropcapReasonUndecodable, wantDrop: true,
			why: "no type was ever read, so it gets its own bucket rather than an empty key",
		},
		{
			name: "result is mapped", line: `{"type":"result","subtype":"success"}`,
			why: "the control that keeps the zero-event rows from passing vacuously",
		},
		{
			name: "assistant text is mapped",
			line: `{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"hi"}]}}`,
			why:  "a mapped block reaches the client, so it is not a drop",
		},
		{
			name: "an unsuppressed user text block is surfaced",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"not the nudge"}]}}`,
			why: "the block-level suppression is byte-exact and narrow: every OTHER user/text " +
				"block still surfaces as Unrecognized, so it is visible and not dropped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := dropcapCaptured{Raw: []byte(tt.line)}
			var sl struct {
				Type    string `json:"type"`
				Subtype string `json:"subtype"`
			}
			c.Decoded = json.Unmarshal([]byte(tt.line), &sl) == nil
			c.Type, c.Subtype = sl.Type, sl.Subtype

			reason, dropped := dropcapClassify(t, c)
			if dropped != tt.wantDrop {
				t.Fatalf("dropped = %t; want %t — %s", dropped, tt.wantDrop, tt.why)
			}
			if dropped && reason != tt.wantReason {
				t.Errorf("reason = %q; want %q — %s", reason, tt.wantReason, tt.why)
			}
		})
	}
}

// TestDropcapRedactionAndDenyScan covers the two mechanisms and, critically, the
// arms where each one could invert.
func TestDropcapRedactionAndDenyScan(t *testing.T) {
	t.Parallel()

	const (
		fakeHome    = "/Users/fakeoperator"
		fakeTmp     = "/private/var/folders/zz/T/pyry-1260-capture-000"
		fakeWork    = "/Users/fakeoperator/tmp-home/dropcap-work"
		fakeSession = "6d1c0f7a-12a0-4c0a-9b2e-0d5a7c3f1e42"
		fakeToken   = "sk-ant-oat01-THIS-IS-NOT-A-REAL-TOKEN"
	)
	const fakeNonce int64 = 1754049600000000000

	t.Run("substitutions are declared with counts and never carry their value", func(t *testing.T) {
		t.Parallel()
		red := newDropcapRedactor(fakeHome+"/tmp-home", fakeTmp, fakeWork,
			fakeWork+"/"+dropcapFIFOName, fakeSession, fakeNonce)

		in := fmt.Sprintf(`{"cwd":%q,"fifo":%q,"session_id":%q,"run":%d,"artifact":%q}`,
			fakeWork, fakeWork+"/"+dropcapFIFOName, fakeSession, fakeNonce, fakeTmp)
		out := string(red.redact([]byte(in)))

		for _, leaked := range []string{fakeWork, fakeSession, fakeTmp, strconv.FormatInt(fakeNonce, 10)} {
			if strings.Contains(out, leaked) {
				t.Errorf("redacted output still carries a substituted value; the table did not apply")
			}
		}
		subs := red.substitutions()
		if len(subs) == 0 {
			t.Fatal("no substitutions declared although several applied — an undeclared substitution " +
				"is an unreviewable one")
		}
		for _, s := range subs {
			if s.Count <= 0 {
				t.Errorf("class %q declared with count %d; a declaration without a count says nothing",
					s.Class, s.Count)
			}
			// class is a NAME. A class carrying its own matched value would put the
			// very string being redacted back into the record.
			for _, secret := range []string{fakeHome, fakeWork, fakeSession, fakeTmp} {
				if strings.Contains(s.Class, secret) {
					t.Errorf("class %q carries a redacted value", s.Class)
				}
			}
		}
		// The longest-first ordering: the FIFO path lives under the workdir, which
		// lives under the temp home. A shorter class winning would leave the tail
		// of the longer path in the record.
		if strings.Contains(out, dropcapFIFOName) {
			t.Errorf("the FIFO basename survived: a shorter path class shadowed the longer one")
		}
	})

	t.Run("a fifoLiveOutcome leaks a path with no payload involved", func(t *testing.T) {
		t.Parallel()
		red := newDropcapRedactor(fakeHome+"/tmp-home", fakeTmp, fakeWork,
			fakeWork+"/"+dropcapFIFOName, fakeSession, fakeNonce)
		fifoPath := fakeWork + "/" + dropcapFIFOName
		got := red.outcome(fifoLiveOutcome{
			Verdict: fifoLiveNoReader,
			Detail:  fmt.Sprintf("lstat %s: no such file", fifoPath),
			Path:    fifoPath,
		})
		if strings.Contains(got.Path, fakeWork) || strings.Contains(got.Detail, fakeWork) {
			t.Errorf("fifoLiveOutcome Path/Detail were not substituted; that pair publishes an "+
				"absolute path under the temp $HOME with no payload involved (got %q / %q)",
				got.Path, got.Detail)
		}
	})

	t.Run("the net is not decorative", func(t *testing.T) {
		t.Parallel()
		// Redaction deliberately handed nothing: the scan alone must catch it.
		scanner := dropcapScanner{needles: dropcapFixedNeedles()}
		blob := []byte(fmt.Sprintf(`{"payload":%q,"cwd":%q}`, fakeToken, fakeHome))
		hits, _ := scanner.scan(blob)
		if !dropcapContains(hits, dropcapDenySkAnt) {
			t.Errorf("hits = %v; the sk-ant- literal survived redaction and the scan did not report "+
				"it — the deny-scan is decorative", hits)
		}
		if !dropcapContains(hits, dropcapDenyUsers) {
			t.Errorf("hits = %v; want the home-path prefix class", hits)
		}
	})

	t.Run("the JSON-escaped form is caught as well as the raw", func(t *testing.T) {
		t.Parallel()
		// encoding/json escapes the ampersand inside a string, so this needle is
		// absent in raw form and present only in its backslash-u escaped form.
		const needle = "/Users/fake&operator/secret-directory"
		scanner := dropcapScanner{needles: []dropcapNeedle{{class: "escaped_path", value: needle, dynamic: true}}}
		blob, err := json.Marshal(map[string]string{"payload": needle})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if bytes.Contains(blob, []byte(needle)) {
			t.Fatalf("the fixture no longer exercises the escaped form: %s", blob)
		}
		hits, _ := scanner.scan(blob)
		if !dropcapContains(hits, "escaped_path") {
			t.Errorf("hits = %v; the escaped form survived the scan, so any payload-embedded "+
				"literal carrying <, > or & would publish unnoticed", hits)
		}
	})

	t.Run("a short dynamic needle is skipped and reported, never matched", func(t *testing.T) {
		t.Parallel()
		scanner := dropcapScanner{needles: []dropcapNeedle{
			{class: "empty_credential", value: "", dynamic: true},
			{class: "short_credential", value: "ab", dynamic: true},
		}}
		hits, notApplied := scanner.scan([]byte(`{"payload":"anything at all"}`))
		if len(hits) != 0 {
			t.Errorf("hits = %v; a needle shorter than %d bytes must be SKIPPED — "+
				`bytes.Contains(x, []byte("")) is always true, so it would match everything`,
				hits, dropcapMinNeedle)
		}
		for _, class := range []string{"empty_credential", "short_credential"} {
			if !dropcapContains(notApplied, class) {
				t.Errorf("notApplied = %v; want %q — an arm that silently did not run reads as an "+
					"arm that ran and found nothing", notApplied, class)
			}
		}
		applied := dropcapScanner{needles: []dropcapNeedle{
			{class: "empty_credential", value: "", dynamic: true},
		}}.applied()
		if applied["empty_credential"] {
			t.Error("credential_scan_applied reported true for a needle that was never searched for")
		}
	})

	// The two arms below are a MEASURED failure, not a predicted one: the
	// 2026-08-02 capture's system/init carried memory_paths.auto holding the
	// workdir with every '/' and '_' rewritten to '-'. The table did not know
	// that spelling and the deny-scan's slash-bearing needles could not see it,
	// so a per-user $TMPDIR component would have reached a committed fixture.
	t.Run("the project-slug spelling of a path is substituted", func(t *testing.T) {
		t.Parallel()
		red := newDropcapRedactor(fakeHome+"/tmp-home", fakeTmp, fakeWork,
			fakeWork+"/"+dropcapFIFOName, fakeSession, fakeNonce)
		slug := dropcapSlug(fakeWork)
		if slug == fakeWork {
			t.Fatalf("the fixture no longer exercises the slug form: %q", slug)
		}
		out := string(red.redact([]byte(`{"memory_paths":{"auto":"/x/.claude/projects/` + slug + `/memory/"}}`)))
		if strings.Contains(out, slug) {
			t.Errorf("the slug spelling survived redaction: %q", out)
		}
	})

	t.Run("the deny-scan sees a slug-mangled path a slash-bearing needle cannot", func(t *testing.T) {
		t.Parallel()
		scanner := dropcapScanner{}
		scanner.addDynamicPath(dropcapClassWorkdir, fakeWork)
		slug := dropcapSlug(fakeWork)
		blob := []byte(`{"memory_paths":{"auto":"` + slug + `"}}`)
		if bytes.Contains(blob, []byte(fakeWork)) {
			t.Fatalf("the fixture still carries the raw path, so the row proves nothing")
		}
		hits, _ := scanner.scan(blob)
		if !dropcapContains(hits, dropcapClassWorkdir) {
			t.Errorf("hits = %v; a re-spelling of the workdir passed the net, which is how a "+
				"per-user $TMPDIR component reaches a committed fixture", hits)
		}
	})

	t.Run("every base64 payload is inside the net's reach, the nudge included", func(t *testing.T) {
		t.Parallel()
		// base64 is opaque to bytes.Contains, so such a payload is searchable only
		// once decoded. The nudge lives OUTSIDE dropped_lines, and a nudge block
		// carrying invalid UTF-8 takes the base64 branch in dropcapFindNudge — so
		// a scan loop written over the entries alone skips a reachable payload,
		// not a theoretical one. That omission was real until code review caught
		// it, which is why the enumeration is one function with one test.
		rec := &dropcapRecord{
			DroppedLines: []dropcapEntry{
				{Index: 4, PayloadEncoding: dropcapEncodingJSONString, Payload: `{"type":"system"}`},
				{Index: 7, PayloadEncoding: dropcapEncodingBase64,
					PayloadB64: base64.StdEncoding.EncodeToString([]byte(`{"k":"` + fakeToken + `"}`))},
			},
			HarnessNudge: dropcapNudge{
				Observed:        true,
				PayloadEncoding: dropcapEncodingBase64,
				PayloadB64:      base64.StdEncoding.EncodeToString([]byte(`{"text":"` + fakeHome + `/x"}`)),
			},
		}
		blob, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		scanner := dropcapScanner{needles: dropcapFixedNeedles()}
		if hits, _ := scanner.scan(blob); len(hits) != 0 {
			t.Fatalf("hits = %v scanning the record undecoded; the row proves nothing unless base64 "+
				"really does hide these needles from a scan of the blob", hits)
		}

		payloads := dropcapBase64Payloads(rec)
		if len(payloads) != 2 {
			t.Fatalf("dropcapBase64Payloads returned %d payload(s); want the entry AND the nudge",
				len(payloads))
		}
		got := []string{}
		for _, p := range payloads {
			h, _ := scanner.scan(p.decode())
			got = append(got, h...)
		}
		// One class per source: sk-ant- is the entry's, /Users/ is the nudge's, so
		// both present is proof both sources were enumerated.
		for _, want := range []string{dropcapDenySkAnt, dropcapDenyUsers} {
			if !dropcapContains(got, want) {
				t.Errorf("hits = %v; want %q — a base64 payload the enumeration misses bypasses the "+
					"fail-closed net entirely", got, want)
			}
		}
	})

	t.Run("a fixed literal is exempt from the minimum", func(t *testing.T) {
		t.Parallel()
		// sk-ant- and /Users/ are seven bytes. Applying the dynamic minimum to them
		// would disable the whole net while reporting it as "not applied".
		applied := dropcapScanner{needles: dropcapFixedNeedles()}.applied()
		for _, class := range []string{dropcapDenySkAnt, dropcapDenyUsers, dropcapDenyHome} {
			if !applied[class] {
				t.Errorf("fixed class %q reported as not applied; the minimum is for needles whose "+
					"value is unknown at authoring time, not for short ones", class)
			}
		}
	})
}

func dropcapContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestDropcapFixtureIsACapture validates the COMMITTED capture. The missing-file
// arm is deliberately fatal: a committed ticket with no capture is exactly the
// failure this ticket exists to prevent, and the deny-scan arm is the one that
// keeps running forever — it turns "a future re-capture must not commit an
// operator's home path" from a rule someone remembers into a check that fails.
//
// That arm runs twice, over the raw file AND over every base64 payload decoded,
// because bytes.Contains cannot see through base64 and the live write path
// deliberately routes invalid UTF-8 there. It is not redundant with the live
// scan: the FIXED needles are the version-independent half of the net, and the
// case they exist for is a re-capture taken on another operator's machine, where
// the live run's dynamic needles were a different set.
func TestDropcapFixtureIsACapture(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob(dropcapFixtureGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", dropcapFixtureGlob, err)
	}
	if len(matches) == 0 {
		t.Fatalf("no capture committed at %s. #1260 exists to replace invented fixtures with "+
			"captured bytes, so this file cannot be hand-written. Produce one with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 15m -v \\\n"+
			"    -run '^TestRealClaude_DroppedLineCapture$' ./internal/e2e/realclaude/\n"+
			"then copy the logged dropcap-record.json verbatim (cp, byte-preserving) into "+
			"internal/e2e/realclaude/testdata/dropped_lines_v<claude-version>.json",
			dropcapFixtureGlob, dropcapEnableEnv)
	}

	scanner := dropcapScanner{needles: dropcapFixedNeedles()}
	scanner.addDynamic("CLAUDE_CODE_OAUTH_TOKEN", os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"))
	scanner.addDynamic("ANTHROPIC_API_KEY", os.Getenv("ANTHROPIC_API_KEY"))

	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			blob, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if hits, _ := scanner.scan(blob); len(hits) > 0 {
				t.Fatalf("the committed capture carries %d denied class(es): %v. The offending "+
					"value is deliberately not printed. Re-capture with an extended "+
					"dropcapRedactor table", len(hits), hits)
			}

			var rec dropcapRecord
			if err := json.Unmarshal(blob, &rec); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			if !rec.IsCapture {
				t.Error("is_capture is false; a fixture that is not byte-for-byte captured must be " +
					"labelled constructed and name the capture it was derived from")
			}
			for _, kv := range [][2]string{
				{"ticket", rec.Ticket}, {"claude_version", rec.ClaudeVersion},
				{"captured_at", rec.CapturedAt}, {"model", rec.Model},
				{"outcome", rec.Outcome}, {"spawn_shape_delta", rec.SpawnShapeDelta},
				{"redaction_rationale", rec.RedactionRationale}, {"limitations", rec.Limitations},
			} {
				if strings.TrimSpace(kv[1]) == "" {
					t.Errorf("provenance key %q is empty; the fixture must carry its provenance", kv[0])
				}
			}
			if len(rec.SpawnShape) == 0 {
				t.Error("spawn_shape is empty; AC1 names the argv as part of the record")
			}
			if rec.AbsenceClaimValid != (rec.Outcome == dropcapFired) {
				t.Errorf("absence_claim_valid = %t with outcome %q; only %q licenses an absence claim",
					rec.AbsenceClaimValid, rec.Outcome, dropcapFired)
			}

			total := 0
			for _, n := range rec.Census {
				total += n
			}
			if total != len(rec.DroppedLines) {
				t.Errorf("census totals %d but dropped_lines holds %d entries; a census that does "+
					"not account for every entry misstates the counts", total, len(rec.DroppedLines))
			}

			for _, e := range rec.DroppedLines {
				switch e.PayloadEncoding {
				case dropcapEncodingJSONString:
					if e.Reason == dropcapReasonUndecodable {
						continue // by definition not valid JSON; that is the datum
					}
					var probe struct {
						Type string `json:"type"`
					}
					if err := json.Unmarshal([]byte(e.Payload), &probe); err != nil {
						t.Errorf("entry %d payload is not valid JSON (%v); no payload is truncated, "+
							"so a broken one means the capture was mutated", e.Index, err)
						continue
					}
					if probe.Type == "" {
						t.Errorf("entry %d payload carries no top-level type", e.Index)
					}
				case dropcapEncodingBase64:
					if _, err := base64.StdEncoding.DecodeString(e.PayloadB64); err != nil {
						t.Errorf("entry %d payload_b64 does not decode: %v", e.Index, err)
					}
					// Its BYTES are scanned below, not here: base64 is opaque to the
					// blob scan above.
				default:
					t.Errorf("entry %d payload_encoding = %q; want %q or %q", e.Index,
						e.PayloadEncoding, dropcapEncodingJSONString, dropcapEncodingBase64)
				}
			}

			// The blob scan above is bytes.Contains over the file, which cannot see
			// through base64 — so without this pass the check goes blind on exactly
			// the encoding the live write path deliberately decodes. It is not
			// redundant with that live scan: the FIXED needles are the
			// version-independent half of the net, the half built to catch a
			// re-capture taken on ANOTHER operator's machine, where the live run's
			// dynamic needles were a different set entirely.
			for _, p := range dropcapBase64Payloads(&rec) {
				if hits, _ := scanner.scan(p.decode()); len(hits) > 0 {
					t.Fatalf("%s in the committed capture decodes to %d denied class(es): %v. "+
						"The offending value is deliberately not printed. Re-capture with an "+
						"extended dropcapRedactor table", p.where, len(hits), hits)
				}
			}

			// The rig-authored frame prose is a verbatim copy of the constants that
			// produced it — pinned, not assumed. AC3/AC4 put the provenance, the
			// spawn-shape delta, the redaction rationale (including what is
			// deliberately KEPT) and the limitations in the RECORD, so a constant
			// that gains a sentence while the committed record keeps the old one
			// leaves the fixture making a claim the code no longer makes. This
			// fails until the two agree.
			//
			// Which also fixes the way they are allowed to be made to agree. These
			// three fields are compile-time literals, never claude's bytes, so
			// propagating a constant into the committed record is a mechanical
			// substitution and this check is what verifies it landed exactly. The
			// PAYLOADS carry no such licence: they are captured bytes, editing one
			// is inventing evidence, and the only way to change one is a fresh
			// capture.
			for _, kv := range [][3]string{
				{"spawn_shape_delta", rec.SpawnShapeDelta, dropcapSpawnShapeDelta},
				{"redaction_rationale", rec.RedactionRationale, dropcapRedactionRationale},
				{"limitations", rec.Limitations, dropcapLimitations},
			} {
				if kv[1] != kv[2] {
					t.Errorf("%s in the committed capture is not the constant that writes it. "+
						"Either the fixture predates a change to the constant, or it was edited "+
						"away from it; propagate the constant into the fixture (or re-capture)",
						kv[0])
				}
			}

			// The pin harnessNoOutputNudge's doc comment asks
			// for: a second confirmed payload, replayed through the shipped parser.
			if rec.HarnessNudge.Observed && rec.HarnessNudge.PayloadEncoding == dropcapEncodingJSONString {
				if !rec.HarnessNudge.MatchesShippedConstant {
					t.Logf("the captured user/text block did NOT match harnessNoOutputNudge — that "+
						"is drift, and the Unrecognized row is the safe failure direction "+
						"(line_index=%d)", rec.HarnessNudge.LineIndex)
					return
				}
				if dropcapHasUserBlockUnrecognized(parseOne(t, rec.HarnessNudge.Payload)) {
					t.Errorf("the captured nudge block is recorded as matching the shipped constant, " +
						"but replaying it through the parser surfaces an Unrecognized{user_block}; " +
						"the record and the parser disagree")
				}
			}
		})
	}
}
