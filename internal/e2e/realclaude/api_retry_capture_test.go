//go:build e2e_realclaude

package realclaude

// Evidence capture for #2262 — the verbatim payload of claude's system/api_retry
// line, or a recorded absence naming the staging that failed to provoke one.
//
// # What is unknown, and why the staging is the whole difficulty
//
// Parser.emitSystemSubtype has no api_retry arm: its default returns false and
// consumeLine's ignoredLineTypes branch then drops the line in silence, so
// nothing today would report one arriving. Nothing in this repo has ever seen
// one — the committed per-subtype census in dropped_lines_v2.1.220.json lists no
// system/api_retry, and the only api_retry bytes in the tree are a hand-built
// WIRE envelope under internal/protocol/testdata, which is evidence about that
// package and none at all about claude.
//
// An API retry cannot be provoked by a prompt. It needs claude's upstream call to
// fail, so this probe redirects ANTHROPIC_BASE_URL at a loopback listener the rig
// owns and answers every request with a retryable overload status. The turn is
// EXPECTED to fail; its result line is part of the evidence.
//
// # The two confusables, and why the classifier is an exact envelope match
//
// `agent_api_retry` is a boolean field on tool_progress frames — a subagent's
// retry flag, already read by consumeToolProgress, and the one confusable present
// in this tree. `system/control_request_progress` with status "api_retry" is a
// retry inside a control request and appears nowhere here. NEITHER IS THIS LINE.
// A record landing either under an api_retry name would feed an invented mapping,
// so arcapIsAPIRetry matches type=system AND subtype=api_retry and nothing else,
// both confusables are counted into their own census, and
// TestArcapClassifierRejectsBothConfusables proves it offline.
//
// # Outcomes, and the one departure from #1260's rule
//
// fired / did-not-fire / instrument-broken, with absence_claim_valid its own
// boolean. In #1260 only `fired` licenses an absence claim. HERE THE ABSENCE IS
// THE PUBLISHABLE RESULT, so absence_claim_valid is true whenever the staged
// listener saw at least one request — which is exactly what separates "the
// upstream call failed and claude said nothing" from "claude never reached the
// staged listener at all", and exactly what fixtureWorthy gates on. Nothing about
// claude's behaviour is fatal; only a broken instrument or a redaction failure is.
//
// # Running it, and where the bytes actually survive
//
// `make e2e-realclaude` on an authenticated machine, and nothing else — the gate
// is the FIXTURE'S ABSENCE, argued at TestRealClaude_APIRetryCapture. To force a
// re-capture over an existing fixture:
//
//	PYRY_PROBE_API_RETRY_CAPTURE=1 go test -tags e2e_realclaude -timeout 20m -v \
//	  -run '^TestRealClaude_APIRetryCapture$' ./internal/e2e/realclaude/
//
// #2229's capture fired clean inside the dispatcher's own real-claude gate, wrote
// its fixture in-repo, printed "Commit it" — and lost the bytes, because that gate
// runs in a detached merge-only worktree it discards and never runs `git add`.
// Four sibling captures lost fixtures the same way. So the record is written
// TWICE: to an os.MkdirTemp artifact directory outside any worktree, which is the
// copy that survives and the one #2236 recovered a sibling's bytes from, and to
// the in-repo fixture path when the promotion gate admits it. Read the artifact
// path out of the log before concluding a firing run produced nothing.
//
// The TestArcap* tests below run offline, with no claude and no listener beyond
// loopback.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// arcapEnableEnv FORCES a re-capture when a fixture already exists. It is not the
// gate — see the gate comment in TestRealClaude_APIRetryCapture.
const arcapEnableEnv = "PYRY_PROBE_API_RETRY_CAPTURE"

// The fixture is matched by GLOB and NAMED from the observed version, which is a
// deliberate break from #2229's compile-time version pin.
//
// That pin is right when the fixture is a reader's input: ccapRecord.fixtureWorthy
// refuses to promote under a mismatched name so a claude upgrade is a loud
// instruction to re-capture. Here the record IS the deliverable, and a version
// bump between authoring and the live run would turn this ticket's whole output
// into nothing promoted. So the arming gate globs any version and arcapFixturePath
// composes the name from what `claude --version` actually printed.
const arcapFixtureGlob = "testdata/api_retry_v*.json"

// Every file-local identifier takes the arcap prefix, for the reason #1260's
// header gives: siblings add files to this package concurrently and a
// branch-overlap check does not catch a same-package identifier collision.
const (
	arcapTicket         = "2262"
	arcapWorkdirName    = "arcap-work"
	arcapRecordName     = "arcap-record.json"
	arcapArtifactPrefix = "pyry-2262-capture-*"
	arcapModel          = "haiku"
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from every
	// sibling probe's so a record can never be mistaken for one of theirs, and it
	// is the id claude echoes back, which is what makes dropcapRedactor's
	// session_id class able to catch it.
	arcapSessionID = "8b52c7e1-4a0d-4f63-9c18-2e7d6b0a3f95"
)

// arcapEnvVar is the whole staging, in one variable. It appears nowhere else in
// this tree, so whether claude honours it on a subscription OAuth login rather
// than an API key is UNMEASURED here — a run whose listener sees zero requests is
// that measurement, not a failed ticket.
const arcapEnvVar = "ANTHROPIC_BASE_URL"

// What the staged listener answers, to every request and every path.
//
// 529 rather than 401/403: #2189 records that on claude 2.1.246+ the first two
// retries after an auth failure are QUIET, so a listener answering 401 is the one
// shape most likely to produce a silent zero. An overload status is the shape to
// stage. The body is rig-authored, in the vendor's error envelope, and carries no
// value read from anywhere.
const (
	arcapAnswerStatus = 529
	arcapAnswerBody   = `{"type":"error","error":{"type":"overloaded_error",` +
		`"message":"staged upstream failure (pyrycode #2262)"}}`
)

const (
	arcapTurnBudget    = 6 * time.Minute
	arcapRunExitWait   = 30 * time.Second
	arcapShutdownWait  = 5 * time.Second
	arcapPoll          = 500 * time.Millisecond
	arcapSpawnGraceMsg = "no live child"
	// How long the stream must stay silent, after at least one line has arrived,
	// before the turn counts as over. LONGER than #2229's 30 s on purpose: a retry
	// ladder is silent BETWEEN attempts, so a short quiet window would call the
	// stream finished in the gap between two retries and cut the capture short of
	// the very lines it exists to record.
	arcapQuiet = 90 * time.Second
	// What the turn must leave behind for its own teardown: cancel the runner and
	// wait it out (arcapRunExitWait), shut the listener down (arcapShutdownWait),
	// then collect, marshal, scan and write the record twice. Generous on purpose —
	// this reserve is the difference between a capture that lands and one whose
	// evidence dies with the killed binary.
	arcapDeadlineReserve = 3 * time.Minute
	// Below this there is not enough turn left to be worth spending the tokens on: a
	// retry ladder observed at 2.1.259 ran ten attempts over ~181 s, so a window
	// under two minutes cannot see one out.
	arcapMinTurnBudget = 2 * time.Minute
)

// The server's own bounds. A bare http.ListenAndServe with no timeouts is the
// gosec G114 shape and a real slow-loris vector even on loopback.
const (
	arcapReadHeaderTimeout = 5 * time.Second
	arcapReadTimeout       = 10 * time.Second
	arcapWriteTimeout      = 10 * time.Second
	arcapIdleTimeout       = 30 * time.Second
	arcapMaxHeaderBytes    = 1 << 20
)

const (
	arcapFired            = "fired"
	arcapDidNotFire       = "did-not-fire"
	arcapInstrumentBroken = "instrument-broken"
)

const (
	arcapTerminatedResult = "result"
	arcapTerminatedQuiet  = "quiescence"
	arcapTerminatedBudget = "budget"
)

// The JSON type names message_json_type takes. "absent" is a value rather than an
// empty string because AC 3 asks what the key carries "when it has one", and an
// absent key is a different observation from a present null.
const (
	arcapMsgAbsent      = "absent"
	arcapMsgNull        = "null"
	arcapMsgString      = "string"
	arcapMsgNumber      = "number"
	arcapMsgBool        = "bool"
	arcapMsgObject      = "object"
	arcapMsgArray       = "array"
	arcapMsgUndecodable = "<line-undecodable>"
)

// The confusable markers, spelled as literals rather than imported from
// production: the record is EVIDENCE about claude's wire shape, and a census keyed
// on production constants would agree with them by construction.
const (
	arcapConfusableAgentFlag  = "agent_api_retry"
	arcapConfusableStatus     = "status=api_retry"
	arcapConfusableControlReq = "subtype=control_request_progress"
)

// arcapMaxCensusKeys caps the distinct method+path keys the request census holds,
// and arcapMaxCensusPathLen caps one key's path. The peer chooses the path, so an
// uncapped map keyed by it is the one unbounded growth path in this record.
const (
	arcapMaxCensusKeys    = 32
	arcapMaxCensusPathLen = 128
)

// arcapMaxVersionToken caps the version token that composes a filename. The shape
// check below already excludes every separator; the cap is what stops a
// pathological version string composing an absurd name.
const arcapMaxVersionToken = 40

var arcapArgs = []string{"--model", arcapModel, "--dangerously-skip-permissions"}

const arcapSpawnShapeDelta = "The YOLO interactive shape, identical to #1260's, #2089's and " +
	"#2229's — see dropcapSpawnShapeDelta for what production's non-yolo spawn adds and what that " +
	"implies for system/init. It is kept rather than narrowed because a differing spawn shape would " +
	"be a confound in the evidence, and its blast radius is structurally smaller here than in any " +
	"sibling: with a 529-only upstream claude can never receive a model response, so it can never be " +
	"told to run a tool at all."

const arcapLimitations = "One turn, one spawn shape, one claude version, one model (" + arcapModel +
	"), one staged failure status (" + "529" + "), and the upstream redirected by ONE environment " +
	"variable whose behaviour on a subscription OAuth login is itself unmeasured. A subtype absent " +
	"from line_type_census did not appear in THIS turn, which is not the same as claude never " +
	"sending it — and an absence here is weaker still than in a sibling capture, because the turn " +
	"was staged to FAIL and a failing turn's line set is not a normal turn's. Whether claude emits " +
	"system/api_retry on a retry that eventually SUCCEEDS is UNMEASURED: nothing here can stage one. " +
	"requests_seen is a count of upstream calls, not of retries: streamsup restarts a crashed child " +
	"and a broken upstream is exactly what crashes one, so it is only requests_seen ABOVE " +
	"spawns_observed that proves any single child called twice — which is why both numbers ship and " +
	"why staging_verdict reads them together."

const arcapRedactionRationale = "Inherited whole from #1260 (see dropcapRedactionRationale): a fresh " +
	"empty non-git workdir under a per-test temp $HOME, a rig-authored prompt, no os.Environ() read " +
	"into the record, the declared dropcapRedactor substitution table over every string, and " +
	"dropcapScanner as a fail-closed deny-scan over the marshalled record. " +
	"WHAT THIS STAGING SPECIFICALLY ADDS, stated rather than left to be inferred by whoever decides " +
	"to paste this record into a public issue. Redirecting " + arcapEnvVar + " means the operator's " +
	"LIVE OAuth bearer token is sent, in plaintext, to a listener this rig owns. That is a real " +
	"posture change from the TLS connection to the vendor's API it replaces, and it is irreducible: " +
	"TLS here would need a certificate claude would reject, and a closed port that never receives " +
	"the token also never yields requests_seen, which is this record's ONLY discriminator between " +
	"'the upstream call failed' and 'claude never arrived'. Three things bound it. The traffic never " +
	"leaves the loopback interface (loopback_only, asserted from the bound address rather than from " +
	"the string passed to Listen). The handler reads NOTHING — it never touches the request headers " +
	"and never reads the body, so no header or body value exists anywhere in this record to redact; " +
	"records_headers_or_bodies is that claim, and the server's ErrorLog is pinned to io.Discard so " +
	"net/http cannot log request-derived bytes into the gate's output either. And the deny-scan " +
	"arms on the token's own value as a needle over the whole marshalled record. " +
	"DELIBERATELY KEPT: top-level types and subtypes, claude's own structural fields, model names, " +
	"token counts, timestamps, the failing turn's error prose, and the method plus URL PATH of every " +
	"request the listener saw (the query string is dropped). system/init carries the operator's local " +
	"claude configuration inventory, which is kept for the reason dropcapRedactionRationale states " +
	"at length and is a description of one machine rather than of claude."

// --- the staged upstream -----------------------------------------------------

// arcapStaging is what AC 2 asks the record to name: what the upstream was
// redirected to, what the listener answered, and how many requests it saw. Without
// requests_seen a zero-api_retry record cannot tell a turn whose upstream call
// actually failed from a turn where claude never reached the staged listener, and
// only the first is evidence about the line.
type arcapStaging struct {
	EnvVar                 string         `json:"env_var"`
	BaseURL                string         `json:"base_url"`
	ListenAddr             string         `json:"listen_addr"`
	LoopbackOnly           bool           `json:"loopback_only"`
	AnsweredStatus         int            `json:"answered_status"`
	AnsweredBody           string         `json:"answered_body"`
	ForwardsRequests       bool           `json:"forwards_requests"`
	RecordsHeadersOrBodies bool           `json:"records_headers_or_bodies"`
	RequestsSeen           int            `json:"requests_seen"`
	RequestCensus          map[string]int `json:"request_census"`
	CensusKeysDropped      int            `json:"census_keys_dropped"`
	FirstRequestAt         string         `json:"first_request_at,omitempty"`
	LastRequestAt          string         `json:"last_request_at,omitempty"`
	ListenError            string         `json:"listen_error,omitempty"`
}

// arcapStage owns the loopback listener claude's API base is pointed at.
//
// The handler is deliberately the smallest thing that can answer: it records a
// method and a path, answers a fixed status and a fixed body, and touches neither
// r.Header nor r.Body. That is what makes records_headers_or_bodies a structural
// claim rather than a promise — there is no code path here that could put a header
// or a body byte into the record.
type arcapStage struct {
	ln  net.Listener
	srv *http.Server

	mu          sync.Mutex
	seen        int
	census      map[string]int
	dropped     int
	first, last time.Time
}

// arcapNewStage binds loopback and starts serving.
//
// The host literal is load-bearing rather than tidy. A ":0" typo binds every
// interface and puts the operator's live bearer token on the LAN, and nothing else
// in this design would notice; TestArcapStageBindsLoopbackOnly is that guard, and
// loopback_only in the record is computed from the ADDRESS THAT GOT BOUND rather
// than from the string passed here, so the record cannot restate the intent
// instead of measuring the outcome.
func arcapNewStage() (*arcapStage, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("binding the staged upstream on loopback: %w", err)
	}
	s := &arcapStage{ln: ln, census: map[string]int{}}
	s.srv = &http.Server{
		Handler:           http.HandlerFunc(s.serveHTTP),
		ReadHeaderTimeout: arcapReadHeaderTimeout,
		ReadTimeout:       arcapReadTimeout,
		WriteTimeout:      arcapWriteTimeout,
		IdleTimeout:       arcapIdleTimeout,
		MaxHeaderBytes:    arcapMaxHeaderBytes,
		// net/http's nil ErrorLog falls back to the standard logger, which under
		// `go test` writes into the captured output. What it logs on a malformed
		// request is derived from a connection whose headers carry the token.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *arcapStage) baseURL() string { return "http://" + s.ln.Addr().String() }

// serveHTTP answers every request identically and reads nothing from it.
func (s *arcapStage) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.note(r.Method, r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(arcapAnswerStatus)
	_, _ = io.WriteString(w, arcapAnswerBody)
}

// note counts one request. The query string never reaches here — r.URL.Path is
// the path alone — and the path itself is length-capped before it becomes a key.
func (s *arcapStage) note(method, path string) {
	if len(path) > arcapMaxCensusPathLen {
		path = path[:arcapMaxCensusPathLen] + "…"
	}
	key := method + " " + path

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.seen == 0 {
		s.first = now
	}
	s.last = now
	s.seen++
	if _, ok := s.census[key]; !ok && len(s.census) >= arcapMaxCensusKeys {
		s.dropped++
		return
	}
	s.census[key]++
}

// observe snapshots what the listener saw, plus the declared posture the record
// publishes. The census map is DEEP-COPIED: returning the handler's own map would
// hand the test goroutine a map live connections still write to, and json.Marshal
// reading it during the record write is a race under -race.
func (s *arcapStage) observe() arcapStaging {
	s.mu.Lock()
	defer s.mu.Unlock()
	census := make(map[string]int, len(s.census))
	for k, v := range s.census {
		census[k] = v
	}
	st := arcapStaging{
		EnvVar:                 arcapEnvVar,
		BaseURL:                s.baseURL(),
		ListenAddr:             s.ln.Addr().String(),
		LoopbackOnly:           arcapIsLoopbackAddr(s.ln.Addr().String()),
		AnsweredStatus:         arcapAnswerStatus,
		AnsweredBody:           arcapAnswerBody,
		ForwardsRequests:       false,
		RecordsHeadersOrBodies: false,
		RequestsSeen:           s.seen,
		RequestCensus:          census,
		CensusKeysDropped:      s.dropped,
	}
	if !s.first.IsZero() {
		st.FirstRequestAt = s.first.Format(time.RFC3339Nano)
		st.LastRequestAt = s.last.Format(time.RFC3339Nano)
	}
	return st
}

func (s *arcapStage) close(ctx context.Context) { _ = s.srv.Shutdown(ctx) }

// --- the spawn observer ------------------------------------------------------

// arcapSpawnLog is a slog.Handler that WRITES NOTHING and keeps two facts out of
// the runner's own "spawning claude" record: the FIRST argv, and HOW MANY spawns
// happened. It doubles as the explicit discard handler the runner needs, since a
// nil Config.Logger falls back to slog.Default() and would put the runner's
// lifecycle lines into CI output.
//
// THE COUNT IS LOAD-BEARING, and it is why this is not newDropcapArgvHandler.
// requests_seen alone cannot mean "claude retried": streamsup restarts a crashed
// child, and a broken upstream is exactly the condition that crashes one, so N
// requests could be N children each making a single attempt. Only
// requests_seen > spawns proves by pigeonhole that SOME child made more than one
// upstream call, which is the premise the strongest reading of an absence rests
// on. Without it, a respawn ladder would publish as a retry ladder.
type arcapSpawnLog struct {
	mu     *sync.Mutex
	argv   *[]string
	spawns *int
}

func newArcapSpawnLog() (slog.Handler, func() ([]string, int)) {
	var (
		mu     sync.Mutex
		argv   []string
		spawns int
	)
	h := arcapSpawnLog{mu: &mu, argv: &argv, spawns: &spawns}
	return h, func() ([]string, int) {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), argv...), spawns
	}
}

func (h arcapSpawnLog) Enabled(context.Context, slog.Level) bool { return true }

func (h arcapSpawnLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "spawning claude" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.spawns++
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

func (h arcapSpawnLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h arcapSpawnLog) WithGroup(string) slog.Handler      { return h }

// --- the record --------------------------------------------------------------

// arcapFrame is one line of the turn — every line, not only the quarry. The
// payload half comes from dropcapMakeEntry so the base64 arm for invalid UTF-8 is
// shared rather than re-derived.
//
// decodes_into_stream_line and parser_undecodable are AC 3's answer measured
// TWICE with different fabric: the first from a local mirror of the unexported
// segmentation struct, the second from what the SHIPPED parser did with the same
// bytes. Carried side by side rather than reconciled, so a drift between them is
// visible in the record instead of resolved silently.
type arcapFrame struct {
	Index                   int      `json:"index"`
	Type                    string   `json:"type"`
	Subtype                 string   `json:"subtype,omitempty"`
	APIRetry                bool     `json:"api_retry"`
	Confusables             []string `json:"confusables,omitempty"`
	DecodesIntoStreamLine   bool     `json:"decodes_into_stream_line"`
	ParserUndecodable       bool     `json:"parser_undecodable"`
	MessageJSONType         string   `json:"message_json_type"`
	PayloadLenBytesCaptured int      `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int      `json:"payload_len_bytes"`
	PayloadEncoding         string   `json:"payload_encoding"`
	Payload                 string   `json:"payload,omitempty"`
	PayloadB64              string   `json:"payload_b64,omitempty"`
	EventsEmitted           int      `json:"events_emitted"`
}

type arcapRecord struct {
	Ticket          string   `json:"ticket"`
	ClaudeVersion   string   `json:"claude_version"`
	CapturedAt      string   `json:"captured_at"`
	IsCapture       bool     `json:"is_capture"`
	Model           string   `json:"model"`
	SpawnShape      []string `json:"spawn_shape"`
	SpawnShapeDelta string   `json:"spawn_shape_delta"`
	EnvDelta        []string `json:"env_delta"`
	Workdir         string   `json:"workdir"`
	Prompt          string   `json:"prompt"`

	Outcome           string  `json:"outcome"`
	OutcomeDetail     string  `json:"outcome_detail"`
	AbsenceClaimValid bool    `json:"absence_claim_valid"`
	StagingVerdict    string  `json:"staging_verdict"`
	TerminatedOn      string  `json:"terminated_on"`
	TurnSeconds       float64 `json:"turn_seconds"`

	// How many times the supervisor spawned claude during the capture, observed
	// from the runner's own log record. Beside requests_seen it is what separates a
	// retry ladder from a respawn ladder — see arcapSpawnLog.
	SpawnsObserved int `json:"spawns_observed"`

	Staging arcapStaging `json:"staging"`

	LinesCaptured            int            `json:"lines_captured"`
	Frames                   []arcapFrame   `json:"frames"`
	LineTypeCensus           map[string]int `json:"line_type_census"`
	APIRetryLineCount        int            `json:"api_retry_line_count"`
	ConfusableCensus         map[string]int `json:"confusable_census"`
	MessageJSONTypeCensus    map[string]int `json:"message_json_type_census"`
	StreamLineDecodeFailures int            `json:"stream_line_decode_failures"`
	ParserUndecodableLines   int            `json:"parser_undecodable_lines"`
	UndecodedLines           int            `json:"undecoded_lines"`

	Redaction               []dropcapSubstitution `json:"redaction"`
	RedactionRulesInstalled int                   `json:"redaction_rules_installed"`
	RedactionRationale      string                `json:"redaction_rationale"`
	CredentialScanApplied   map[string]bool       `json:"credential_scan_applied"`
	CredentialScanSkipped   []string              `json:"credential_scan_skipped"`
	CredentialScanNeedles   int                   `json:"credential_scan_needles"`

	Limitations string `json:"limitations"`

	LinesDroppedOverCap    int `json:"lines_dropped_over_cap"`
	BytesDroppedOverCap    int `json:"bytes_dropped_over_cap"`
	PartialsDropped        int `json:"partials_dropped"`
	BlankLines             int `json:"blank_lines"`
	UnterminatedPartialLen int `json:"unterminated_partial_len"`
}

// set fills the outcome and, with it, the absence claim.
//
// THE DEPARTURE FROM #1260 IS HERE. dropcapRecord.set licenses an absence claim
// only on `fired`, because there the absence was a by-product. Here the absence IS
// the deliverable (AC 1), so what licenses it is that the staging actually
// exercised the upstream — which is precisely requests_seen > 0, and precisely
// what distinguishes AC 2's two outcomes. An instrument-broken run never gets it,
// and that is the only outcome that cannot.
func (rec *arcapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
	rec.AbsenceClaimValid = outcome != arcapInstrumentBroken && rec.Staging.RequestsSeen > 0
}

// --- the pure half: classification, verdicts and the promotion gate ----------

// arcapStreamMessageMirror and arcapStreamLineMirror MIRROR streamsup's
// unexported streamLine/streamMessage. A mirror is the only way to run the same
// decode from another package, and it is honest only while it agrees with the
// original: TestArcapDecodeVerdictAgreesWithTheShippedParser is the drift alarm,
// comparing this decode against what the SHIPPED parser did with the same bytes.
type arcapStreamMessageMirror struct {
	ID      string            `json:"id"`
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type arcapStreamLineMirror struct {
	Type    string                    `json:"type"`
	Subtype string                    `json:"subtype"`
	Message *arcapStreamMessageMirror `json:"message"`
}

// arcapIsAPIRetry is the quarry test, and it is an EXACT envelope match on
// purpose. Matching the string `api_retry` anywhere in a line would catch a
// tool_progress frame's agent_api_retry flag and a control_request_progress
// status, neither of which is this line; a record landing either under an
// api_retry name would prove nothing and would feed an invented mapping.
func arcapIsAPIRetry(typ, subtype string) bool {
	return typ == "system" && subtype == "api_retry"
}

// arcapConfusables returns the sorted set of confusable markers one line carries.
// They are counted rather than ignored, so the record can say it SAW them and that
// they are not the quarry — an absence is sharper when the near-misses are named.
//
// The key rule walks at any depth, because agent_api_retry rides inside a
// tool_progress payload rather than on the envelope. The value rules match only
// where the named key maps DIRECTLY to the string, which is what keeps a bare
// array element or a prose mention out of the count.
func arcapConfusables(raw []byte) []string {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	seen := map[string]bool{}
	arcapWalkJSON(doc, func(marker string) { seen[marker] = true })
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

func arcapWalkJSON(v any, hit func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == arcapConfusableAgentFlag {
				hit(arcapConfusableAgentFlag)
			}
			if s, ok := val.(string); ok {
				if k == "status" && s == "api_retry" {
					hit(arcapConfusableStatus)
				}
				if k == "subtype" && s == "control_request_progress" {
					hit(arcapConfusableControlReq)
				}
			}
			arcapWalkJSON(val, hit)
		}
	case []any:
		for _, e := range t {
			arcapWalkJSON(e, hit)
		}
	}
}

// arcapDecodesIntoStreamLine answers AC 3's first half by running the same decode
// streamsup's consumeLine runs, against the mirror declared above. A false here is
// a line that never reaches sl.Type at all — the shape system/permission_denied
// turned out to have, and the shape that cost it a separate gate.
func arcapDecodesIntoStreamLine(raw []byte) bool {
	var sl arcapStreamLineMirror
	return json.Unmarshal(raw, &sl) == nil
}

// arcapMessageJSONType answers AC 3's second half. It reads the top level as raw
// values rather than into a typed struct, so it reports what the key CARRIES
// instead of what a decode target hoped for.
func arcapMessageJSONType(raw []byte) string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return arcapMsgUndecodable
	}
	v, ok := top["message"]
	if !ok {
		return arcapMsgAbsent
	}
	return arcapJSONValueType(v)
}

// arcapJSONValueType names one already-valid JSON value's type from its first
// significant byte, which is unambiguous in JSON.
func arcapJSONValueType(v json.RawMessage) string {
	trimmed := strings.TrimSpace(string(v))
	if trimmed == "" {
		return arcapMsgUndecodable
	}
	switch trimmed[0] {
	case '{':
		return arcapMsgObject
	case '[':
		return arcapMsgArray
	case '"':
		return arcapMsgString
	case 't', 'f':
		return arcapMsgBool
	case 'n':
		return arcapMsgNull
	default:
		return arcapMsgNumber
	}
}

// arcapIsLoopbackAddr answers from the address that GOT BOUND, never from the
// string passed to net.Listen — a record restating the rig's intent would agree
// with a ":0" typo instead of catching it.
func arcapIsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// arcapParserUndecodable reports the SHIPPED parser's verdict on one line: an
// Unrecognized at the undecodable site is emitted exactly where consumeLine's
// streamLine decode failed and neither dropHarnessProseLine nor
// consumePermissionDeniedLine absorbed the line.
func arcapParserUndecodable(events []turnevent.Event) bool {
	for _, ev := range events {
		if u, ok := ev.(turnevent.Unrecognized); ok && u.Site == turnevent.UnrecognizedUndecodable {
			return true
		}
	}
	return false
}

// arcapAwaitTurn waits out the turn WITHOUT depending on it closing. The upstream
// is staged to fail, so whether a `result` ever arrives is part of what is
// unknown; blocking on one would hang on exactly the case under test and land no
// census at all.
//
// sentAt is the captured-line count when the turn went out, so the quiescence arm
// can require that the turn produced SOMETHING before calling the stream quiet.
// quiet and budget are parameters rather than the constants directly so the three
// exits can be proved offline in milliseconds.
func arcapAwaitTurn(recorder *dropcapRecorder, sentAt int, quiet, budget time.Duration) string {
	deadline := time.Now().Add(budget)
	lastGrowth := time.Now()
	last := sentAt
	for {
		lines, _ := recorder.snapshot()
		for _, c := range lines {
			if c.Decoded && c.Type == "result" {
				return arcapTerminatedResult
			}
		}
		if len(lines) != last {
			last = len(lines)
			lastGrowth = time.Now()
		}
		if len(lines) > sentAt && time.Since(lastGrowth) >= quiet {
			return arcapTerminatedQuiet
		}
		if time.Now().After(deadline) {
			return arcapTerminatedBudget
		}
		time.Sleep(arcapPoll)
	}
}

// arcapTurnBudgetWithin sizes the turn against the TEST BINARY'S OWN DEADLINE, not
// against the turn alone. Returns the budget to spend and the reason to skip when
// there is not enough deadline left to spend anything.
//
// The failure this exists to stop is measured, and it is this probe's: on
// 2026-09-09 the live gate ran `go test -timeout 20m` over the whole package, this
// capture spent 182 s of it, and a sibling capture was still running when the
// binary's timeout fired. A binary killed by that timeout runs NO cleanups, so it
// records nothing at all — the tokens are spent and the evidence is lost, which for
// this probe means the ticket's entire deliverable. The package overview for
// initialize_control_probe_test.go closes by asking the next budget addition here
// to check the invocation's timeout rather than sum the per-step ceilings; this is
// that check.
//
// Skipping is the right arm rather than a shortened turn: a capture cut off
// mid-ladder publishes a weaker absence than the one already committed, and a
// forced re-capture is always re-runnable under a longer timeout.
func arcapTurnBudgetWithin(t *testing.T) (time.Duration, string) {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		// No -timeout at all, so nothing to be starved by and nothing to reserve for.
		return arcapTurnBudget, ""
	}
	return arcapBudgetFor(time.Until(deadline))
}

// arcapBudgetFor is the arithmetic half, split out because a *testing.T's deadline
// comes from the go command and cannot be set from inside a test.
func arcapBudgetFor(remaining time.Duration) (time.Duration, string) {
	spendable := remaining - arcapDeadlineReserve
	if spendable < arcapMinTurnBudget {
		return 0, fmt.Sprintf("the test binary's deadline leaves %s, and this capture needs a %s "+
			"turn plus %s to write its record; a binary killed by -timeout runs no cleanups and "+
			"records nothing, so the tokens would buy no evidence. Re-run with a longer -timeout",
			remaining.Round(time.Second), arcapMinTurnBudget, arcapDeadlineReserve)
	}
	return min(arcapTurnBudget, spendable), ""
}

// arcapFixturePath composes the fixture name from the version claude actually
// printed, and refuses any token that cannot safely be one.
//
// The shape check is what stops a version string composing a path outside
// testdata/: it admits no separator and requires a leading digit, so every
// traversal and absolute form is rejected, and an "<unavailable: …>" fails at the
// first character rather than naming a fixture that vouches for nothing. The
// length cap is what stops a pathological version composing an absurd name.
func arcapFixturePath(version string) (string, error) {
	token, _, _ := strings.Cut(strings.TrimSpace(version), " ")
	if token == "" {
		return "", fmt.Errorf("claude_version is empty, so the fixture could not be named for the " +
			"release it vouches for")
	}
	if len(token) > arcapMaxVersionToken {
		return "", fmt.Errorf("claude_version's leading token is %d bytes, over the %d cap",
			len(token), arcapMaxVersionToken)
	}
	if token[0] < '0' || token[0] > '9' {
		return "", fmt.Errorf("claude_version %q does not start with a digit, so it is not a "+
			"version (an unreadable one is recorded as \"<unavailable: …>\")", token)
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			c == '.' || c == '-'
		if !ok {
			return "", fmt.Errorf("claude_version %q carries a byte that may not appear in a "+
				"filename", token)
		}
	}
	return "testdata/api_retry_v" + token + ".json", nil
}

// stagingVerdict names WHICH reading a zero-api_retry run is — AC 2's whole
// demand. The request count is what separates them, and the one-request row is the
// reading a naive design folds into the finding: an absence there says the retry
// PATH never ran, which is a much weaker claim than claude retrying in silence.
func (rec *arcapRecord) stagingVerdict() string {
	switch {
	case rec.Staging.ListenError != "":
		return fmt.Sprintf("the staged upstream never bound (%s), so claude's API base was never "+
			"redirected and the surface was never exercised. This is an INSTRUMENT fault: read "+
			"outcome_detail, not the line census", rec.Staging.ListenError)
	case rec.Staging.RequestsSeen == 0:
		return fmt.Sprintf("claude never reached the staged listener: it saw ZERO requests while "+
			"%s named %s. Either claude does not honour that variable on this login shape — a "+
			"subscription OAuth login rather than an API key, which is unmeasured in this repo — "+
			"or it failed before its first upstream call. Its silence says NOTHING about "+
			"system/api_retry, and re-running will not change it",
			arcapEnvVar, rec.Staging.BaseURL)
	default:
		endpoint, repeats := rec.busiestEndpoint()
		if repeats <= rec.spawnFloor() {
			return fmt.Sprintf("the upstream failed and claude did not demonstrably retry it: the "+
				"listener saw %d request(s) answered %d, across %d spawn(s), and its busiest single "+
				"endpoint (%s) took %d of them — so NO single child provably called the SAME "+
				"endpoint twice. An absent system/api_retry here says the retry path never ran, NOT "+
				"that it runs without announcing itself, which is a strictly weaker result than this "+
				"capture was staged for. Three live readings: the staged status is not in claude's "+
				"retryable set; what looks like a ladder is the supervisor respawning a child that "+
				"died on the broken upstream; or the requests are DIFFERENT calls of one attempt, "+
				"since the listener answers every path alike and a token count followed by a "+
				"completion is two requests and no retry at all",
				rec.Staging.RequestsSeen, rec.Staging.AnsweredStatus, rec.SpawnsObserved,
				endpoint, repeats)
		}
		return fmt.Sprintf("claude RETRIED a failing upstream: the listener saw %d request(s) "+
			"answered %d, of which %d hit the SAME endpoint (%s), across %d spawn(s) — so by "+
			"pigeonhole at least one child called that one endpoint more than once — and the turn "+
			"ended on %s. The staging WORKED, so a zero here is a finding about claude, that it "+
			"retries without putting system/api_retry on the stream-json surface, and it is to be "+
			"routed back rather than fixed by loosening the classifier",
			rec.Staging.RequestsSeen, rec.Staging.AnsweredStatus, repeats, endpoint,
			rec.SpawnsObserved, rec.TerminatedOn)
	}
}

// busiestEndpoint names the single census key that took the most requests, and how
// many. It is the request count the finding may be read from, and the plain total
// is not.
//
// The distinction is the one #2299's review caught: the listener answers 529 to
// every path, so a total above the spawn count proves only that some child made
// more than one upstream CALL — one child asking a token-counting endpoint and then
// the messages endpoint would satisfy it while having retried nothing. Counting
// within a key is what makes the pigeonhole argument about a repeat of the same
// call. The observed 2.1.259 capture reads 12 POSTs to /v1/messages beside a single
// HEAD probe, and only the 12 licenses the claim.
//
// Ties break on the key name so the verdict prose is stable across runs, and a
// census truncated by arcapMaxCensusKeys can only UNDERSTATE the busiest key, which
// is the safe direction: it weakens the verdict, never strengthens it.
func (rec *arcapRecord) busiestEndpoint() (string, int) {
	best, most := "", 0
	for key, n := range rec.Staging.RequestCensus {
		if n > most || (n == most && key < best) {
			best, most = key, n
		}
	}
	if best == "" {
		return "none", 0
	}
	return best, most
}

// spawnFloor is the smallest number of children the requests could have been
// spread across. A record whose spawn log observed nothing still had to have one
// child for a request to exist, and treating that as zero would let a single
// request read as a retry.
func (rec *arcapRecord) spawnFloor() int {
	if rec.SpawnsObserved < 1 {
		return 1
	}
	return rec.SpawnsObserved
}

// fixtureWorthy answers whether this record may be promoted in-repo, and names the
// reason when it may not.
//
// THE NON-VACUITY RULE IS NOT ccapRecord.fixtureWorthy's, and the difference is
// the ticket. That one refuses a record holding zero frames of its quarry; here a
// zero-api_retry record IS the publishable result under AC 1, so refusing it would
// delete the whole did-not-fire branch. What must be refused instead is a run
// whose staged listener saw no request — a record that cannot tell a failed
// upstream call from a claude that never arrived.
func (rec *arcapRecord) fixtureWorthy() (string, bool) {
	if rec.Outcome != arcapFired && rec.Outcome != arcapDidNotFire {
		return fmt.Sprintf("outcome=%s — only a fired or a did-not-fire run is a published result",
			rec.Outcome), false
	}
	if !rec.Staging.LoopbackOnly {
		return fmt.Sprintf("the staged upstream bound %q, which is not loopback-only; a record "+
			"produced while the operator's credential was reachable off-box is not publishable",
			rec.Staging.ListenAddr), false
	}
	if rec.Staging.RequestsSeen == 0 {
		return "the staged listener saw ZERO requests, so this record cannot distinguish a failed " +
			"upstream call from a claude that never reached the staging — AC 2's two outcomes " +
			"would publish as one shared zero", false
	}
	if _, err := arcapFixturePath(rec.ClaudeVersion); err != nil {
		return err.Error(), false
	}
	// dropcapMakeEntry emits base64 with an EMPTY payload for a frame that is not
	// valid UTF-8. Promoting one as the quarry would hand the mapping ticket a
	// fixture with no readable line to replay. A NON-quarry frame in that state costs
	// the reader nothing and must not block an otherwise good record.
	for _, f := range rec.Frames {
		if f.APIRetry && f.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("the api_retry frame at index %d is encoded %q, so it carries no "+
				"readable payload for the mapping to declare its field set from", f.Index,
				f.PayloadEncoding), false
		}
	}
	return "", true
}

// --- the live capture --------------------------------------------------------

// arcapPrompt is rig-authored and trivial. The turn is staged to FAIL at the
// upstream, so what it asks for barely matters; the tool ban is what keeps the
// YOLO spawn shape's blast radius at zero, and the nonce gives dropcapRedactor's
// prompt_nonce class something to substitute.
func arcapPrompt(nonce int64) string {
	return fmt.Sprintf("Reply with the single word ok and nothing else. Do not use any tools, do "+
		"not comment on the task, and do nothing else. run=%d", nonce)
}

// TestRealClaude_APIRetryCapture stages a failing upstream, drives one turn, and
// writes the record. It asserts nothing about claude.
//
// Ordering is load-bearing in two places. newDropcapScanner reads
// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY via os.Getenv AS DENY NEEDLES, and
// WithWorktreeAuthenticated is what re-pins them into this process; building the
// scanner first yields an EMPTY needle, which dropcapScanner.scan reports as
// notApplied — silently skipped, not fatal — so the credential net would be off
// while every message still read green. And the cleanups are registered record ->
// stage -> runner, so LIFO tears down runner, then listener, then writes the
// record: the child is gone before the upstream it was pointed at disappears.
func TestRealClaude_APIRetryCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE. `make e2e-realclaude` never sets a custom
	// PYRY_PROBE_* variable, so an env-armed probe skips on the ENV check BEFORE the
	// credential check, the live gate passes vacuously, and the fixture never lands
	// — CLAUDE.md § Testing's #1763 failure exactly, where a green gate and a spent
	// budget look identical whether the bytes landed or not. The env variable below
	// can only FORCE a re-capture over an existing fixture.
	force := os.Getenv(arcapEnableEnv) == "1"
	if existing, _ := filepath.Glob(arcapFixtureGlob); len(existing) > 0 && !force {
		t.Skipf("#2262 api_retry capture: %v already exists, so there is nothing to capture and "+
			"this costs no claude turn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 20m -v \\\n"+
			"    -run '^TestRealClaude_APIRetryCapture$' ./internal/e2e/realclaude/",
			existing, arcapEnableEnv)
	}

	// Sized against the binary's deadline before a single token is spent, because a
	// capture killed by -timeout runs no cleanups and writes no record at all.
	budget, tooLate := arcapTurnBudgetWithin(t)
	if tooLate != "" {
		t.Skipf("#2262 api_retry capture: %s", tooLate)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Deliberately NOT t.TempDir(): #2229's capture fired inside the dispatcher's
	// gate-only worktree and lost its in-repo fixture when that worktree was
	// discarded. This directory is outside every worktree and is the copy that
	// survives such a run.
	artifactDir, err := os.MkdirTemp("", arcapArtifactPrefix)
	if err != nil {
		t.Fatalf("#2262: create artifact dir: %v", err)
	}

	// A fresh EMPTY directory, deliberately not a git repo: no branch names and no
	// file contents can reach a payload.
	workdir := filepath.Join(home, arcapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2262: create workdir: %v", err)
	}
	nonce := time.Now().UnixNano()

	// The empty slot is fifoPath, and the emptiness is a fact this probe records
	// rather than an omission: it holds no FIFO. dropcapRedactor.add guards "" —
	// strings.ReplaceAll(s, "", x) would otherwise insert x between every character.
	red := newDropcapRedactor(home, artifactDir, workdir, "", arcapSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2262 capture artifacts: %s", red.str(artifactDir))

	rec := &arcapRecord{
		Ticket:                arcapTicket,
		ClaudeVersion:         probeClaudeVersion(claudeBin),
		CapturedAt:            time.Now().Format(time.RFC3339),
		IsCapture:             true,
		Model:                 arcapModel,
		SpawnShapeDelta:       arcapSpawnShapeDelta,
		Workdir:               red.str(workdir),
		Frames:                []arcapFrame{},
		LineTypeCensus:        map[string]int{},
		ConfusableCensus:      map[string]int{},
		MessageJSONTypeCensus: map[string]int{},
		// Counted, not just listed. A redactor that was never built and one whose
		// table matched nothing both produce an EMPTY redaction array, and only these
		// counters tell them apart — which is what AC 4's "reports that both ran"
		// asks for.
		RedactionRulesInstalled: len(red.rules),
		RedactionRationale:      arcapRedactionRationale,
		CredentialScanApplied:   scanner.applied(),
		CredentialScanSkipped:   []string{},
		CredentialScanNeedles:   len(scanner.needles),
		Limitations:             arcapLimitations,
	}
	rec.set(arcapInstrumentBroken, "did not reach a classification point")

	// Registered before anything below can fail, so a structural t.Fatalf still
	// leaves the evidence on disk — #1260's ordering.
	t.Cleanup(func() { arcapWriteRecord(t, artifactDir, red, scanner, rec) })

	stage, err := arcapNewStage()
	if err != nil {
		rec.Staging = arcapStaging{EnvVar: arcapEnvVar, ListenError: red.str(err.Error())}
		rec.set(arcapInstrumentBroken, "the staged upstream never bound, so no turn was driven: %v",
			red.str(err.Error()))
		t.Fatalf("#2262: %s", rec.OutcomeDetail)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), arcapShutdownWait)
		defer cancel()
		stage.close(ctx)
	})
	rec.Staging = stage.observe()

	// The ONE-ENTRY env delta. Config.Env is additive — spawnAndWait composes
	// append(os.Environ(), env...) and os/exec's last-duplicate-wins rule makes this
	// override exactly one variable, leaving the operator's credentials and HOME
	// intact (spawnEnv's doc states the same precedence from the other end). The
	// value is COMPOSED BY THE RIG from its own listener's port, never harvested:
	// os.Environ() is not read in this file, and the two os.Getenv calls in the
	// package's scanner constructor take credential values as deny NEEDLES only.
	envDelta := []string{arcapEnvVar + "=" + stage.baseURL()}
	rec.EnvDelta = red.strs(envDelta)

	recorder := newDropcapRecorder()
	spawnLog, observeSpawns := newArcapSpawnLog()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: arcapSessionID,
		Args:      arcapArgs,
		Env:       envDelta,
		Stdout:    recorder,
		Logger:    slog.New(spawnLog),
	})
	if err != nil {
		rec.set(arcapInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
			red.str(err.Error()))
		t.Fatalf("#2262: %s", rec.OutcomeDetail)
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
		case <-time.After(arcapRunExitWait):
			t.Errorf("#2262: streamsup.Run did not return within %s of cancel", arcapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.Staging = stage.observe()
		rec.set(arcapInstrumentBroken, "%s within %s: claude never spawned, so nothing was on the "+
			"wire to capture", arcapSpawnGraceMsg, dropcapSpawnWait)
		t.Fatalf("#2262: %s", rec.OutcomeDetail)
	}

	prompt := arcapPrompt(nonce)
	rec.Prompt = red.str(prompt)
	// Read BEFORE the turn goes out, so the quiescence arm requires a line the turn
	// itself produced. claude has already emitted system/init by now, and a literal
	// zero here would let that pre-turn line satisfy the arm on its own.
	preTurn, _ := recorder.snapshot()
	turnStart := time.Now()
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.Staging = stage.observe()
		rec.set(arcapInstrumentBroken, "writing the turn envelope failed, so no turn was ever "+
			"driven: %v", red.str(err.Error()))
		t.Fatalf("#2262: %s", rec.OutcomeDetail)
	}
	rec.TerminatedOn = arcapAwaitTurn(recorder, len(preTurn), arcapQuiet, budget)
	rec.TurnSeconds = time.Since(turnStart).Seconds()

	argv, spawns := observeSpawns()
	rec.SpawnShape = red.strs(argv)
	rec.SpawnsObserved = spawns
	rec.Staging = stage.observe()
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	rec.Frames = arcapCollect(t, lines, red)
	arcapTally(rec)
	rec.StagingVerdict = rec.stagingVerdict()
	arcapClassifyOutcome(rec)

	// AC 5 / the dropcap rule: only a broken instrument is fatal. A did-not-fire is
	// a legitimate published result and leaves the gate green, because the record it
	// wrote IS the deliverable. Counts and verdicts only — claude's bytes are in the
	// record the cleanup has already written, and putting them in CI output is
	// precisely the exposure the deny-scan exists to prevent.
	if rec.Outcome == arcapInstrumentBroken {
		t.Fatalf("#2262: the staging never exercised claude's upstream, so this run supports NO "+
			"claim about system/api_retry — neither its bytes nor its absence.\n"+
			"  staging: %s\n"+
			"  requests_seen=%d census=%v lines=%d terminated_on=%s turn=%.1fs line types=%v\n"+
			"A RE-RUN WILL NOT FIX A ZERO-REQUEST OUTCOME: it means claude did not honour %s on "+
			"this login shape, which is a routing decision for a human and not a code defect. Read "+
			"the artifact record named above before concluding anything else",
			rec.StagingVerdict, rec.Staging.RequestsSeen, rec.Staging.RequestCensus,
			rec.LinesCaptured, rec.TerminatedOn, rec.TurnSeconds, rec.LineTypeCensus, arcapEnvVar)
	}
}

// arcapCollect builds a frame for EVERY line of the turn, marking the quarry
// rather than filtering to it — AC 1 asks for the whole turn's census beside the
// payload. An undecodable line still gets a frame: it is part of what the turn
// produced, and losing it would make the record disagree with lines_captured.
func arcapCollect(t *testing.T, lines []dropcapCaptured, red *dropcapRedactor) []arcapFrame {
	t.Helper()
	out := []arcapFrame{}
	for _, c := range lines {
		// The payload half, shared with #1260 so the base64 arm for invalid UTF-8 is
		// not re-derived. The reason field is spent on the classification below, so it
		// is passed empty.
		entry := dropcapMakeEntry(c, "", red)
		// One parse per line, read twice. Building a second parser for the same bytes
		// would answer identically and cost another allocation per captured line.
		events := parseOne(t, string(c.Raw))
		out = append(out, arcapFrame{
			Index:                   entry.Index,
			Type:                    entry.Type,
			Subtype:                 entry.Subtype,
			APIRetry:                arcapIsAPIRetry(c.Type, c.Subtype),
			Confusables:             arcapConfusables(c.Raw),
			DecodesIntoStreamLine:   arcapDecodesIntoStreamLine(c.Raw),
			ParserUndecodable:       arcapParserUndecodable(events),
			MessageJSONType:         arcapMessageJSONType(c.Raw),
			PayloadLenBytesCaptured: entry.PayloadLenBytesCaptured,
			PayloadLenBytes:         entry.PayloadLenBytes,
			PayloadEncoding:         entry.PayloadEncoding,
			Payload:                 entry.Payload,
			PayloadB64:              entry.PayloadB64,
			EventsEmitted:           len(events),
		})
	}
	return out
}

// arcapShape spells one line's envelope: the top-level type alone when there is no
// subtype, "type/subtype" when there is.
func arcapShape(typ, subtype string) string {
	if subtype == "" {
		return typ
	}
	return typ + "/" + subtype
}

// arcapTally rolls the frames into the record's censuses. The message-type census
// is keyed by SHAPE as well as by type, because AC 3's question is per subtype: it
// is which shape api_retry carries that decides the mapping's design, and a census
// of bare type names could not answer it.
func arcapTally(rec *arcapRecord) {
	for _, f := range rec.Frames {
		shape := arcapShape(f.Type, f.Subtype)
		if f.MessageJSONType == arcapMsgUndecodable {
			rec.UndecodedLines++
			shape = "<undecodable>"
		}
		rec.LineTypeCensus[shape]++
		rec.MessageJSONTypeCensus[shape+" message="+f.MessageJSONType]++
		if f.APIRetry {
			rec.APIRetryLineCount++
		}
		if !f.DecodesIntoStreamLine {
			rec.StreamLineDecodeFailures++
		}
		if f.ParserUndecodable {
			rec.ParserUndecodableLines++
		}
		for _, c := range f.Confusables {
			rec.ConfusableCensus[c]++
		}
	}
}

// arcapClassifyOutcome fills the outcome from what the staging and the turn
// produced. The zero-request arm is instrument-broken rather than an absence, and
// that distinction IS AC 2.
func arcapClassifyOutcome(rec *arcapRecord) {
	switch {
	case !rec.Staging.LoopbackOnly:
		rec.set(arcapInstrumentBroken, "the staged upstream did not bind loopback-only (%s), so it "+
			"was reachable off-box while carrying the operator's credential; nothing this run "+
			"produced may be published", rec.Staging.ListenAddr)
	case rec.LinesCaptured == 0:
		rec.set(arcapInstrumentBroken, "zero lines were captured from claude's stdout, so the "+
			"recorder observed nothing and no census exists")
	case rec.Staging.RequestsSeen == 0:
		rec.set(arcapInstrumentBroken, "the staged listener saw NO request; %s", rec.StagingVerdict)
	case rec.APIRetryLineCount == 0:
		rec.set(arcapDidNotFire, "the turn produced no system/api_retry line across %d captured "+
			"line(s); %s", rec.LinesCaptured, rec.StagingVerdict)
	default:
		rec.set(arcapFired, "%d system/api_retry line(s) across %d captured line(s), after %d "+
			"upstream request(s); terminated_on=%s", rec.APIRetryLineCount, rec.LinesCaptured,
			rec.Staging.RequestsSeen, rec.TerminatedOn)
	}
}

// arcapWriteRecord marshals, deny-scans and writes. #1260's fail-closed rule
// verbatim: on a hit NOTHING is written and the message names the CLASS only,
// never the matched value.
func arcapWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *arcapRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2262: marshal record: %v", err)
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
			t.Errorf("#2262: frame %d: decode base64 payload for the scan: %v", i, derr)
			return
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("#2262: deny-scan found %d denied class(es) still present in the record: %v\n"+
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
		t.Errorf("#2262: re-marshal record: %v", err)
		return
	}

	path := filepath.Join(dir, arcapRecordName)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2262: write record %s: %v", red.str(path), err)
		return
	}
	t.Logf("#2262 outcome=%s absence_claim_valid=%t terminated_on=%s turn=%.1fs requests_seen=%d "+
		"census=%v captured=%d api_retry=%d confusables=%v decode_failures=%d line_types=%v "+
		"message_types=%v scan_not_applied=%v\n  record: %s\n  %s\n  staging: %s",
		rec.Outcome, rec.AbsenceClaimValid, rec.TerminatedOn, rec.TurnSeconds,
		rec.Staging.RequestsSeen, rec.Staging.RequestCensus, rec.LinesCaptured,
		rec.APIRetryLineCount, rec.ConfusableCensus, rec.StreamLineDecodeFailures,
		rec.LineTypeCensus, rec.MessageJSONTypeCensus, notApplied, red.str(path),
		red.str(rec.OutcomeDetail), rec.StagingVerdict)

	// The same deny-scanned bytes, promoted in-repo so the run that produced them is
	// the run that lands them. A capture that still needs a human to copy a file out
	// of a tempdir is a capture #1763 says will not land — and #2229 is the case
	// where the in-repo write ALSO did not land, because the only firing run was the
	// dispatcher's gate in a worktree it discarded. Hence both writes, and hence the
	// artifact path named above being the recovery source.
	if reason, ok := rec.fixtureWorthy(); !ok {
		t.Logf("#2262: NOT promoted — %s. The record above is the evidence; read it, then re-run "+
			"or route the finding back", reason)
		return
	}
	fixturePath, err := arcapFixturePath(rec.ClaudeVersion)
	if err != nil {
		t.Logf("#2262: NOT promoted — %v", err)
		return
	}
	if err := os.WriteFile(fixturePath, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2262: write fixture %s: %v", fixturePath, red.str(err.Error()))
		return
	}
	t.Logf("#2262: FIXTURE WRITTEN to %s (outcome=%s, %d api_retry line(s), %d upstream "+
		"request(s)).\n"+
		"  Commit it — `git add %s` — in THIS ticket's PR. If this run was the dispatcher's "+
		"real-claude gate rather than a builder's branch checkout, the in-repo write above dies "+
		"with that worktree (#2229) and the surviving copy is the artifact record named earlier.\n"+
		"  If the outcome is %s, say so plainly in the closing comment: the mapping ticket "+
		"downstream needs a committed line to replay and cannot be built from an absence.",
		fixturePath, rec.Outcome, rec.APIRetryLineCount, rec.Staging.RequestsSeen, fixturePath,
		arcapDidNotFire)
}

// --- offline self-checks -----------------------------------------------------

// TestArcapClassifierRejectsBothConfusables is the load-bearing guard on the
// classifier. `agent_api_retry` is a tool_progress field present in this tree and
// `system/control_request_progress` with status "api_retry" is a control-request
// retry; a record landing either under an api_retry name would prove nothing and
// would feed an invented mapping.
func TestArcapClassifierRejectsBothConfusables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		line            string
		wantQuarry      bool
		wantConfusables []string
	}{
		{
			name:       "the quarry: an exact system/api_retry envelope",
			line:       `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10}`,
			wantQuarry: true,
		},
		{
			name: "a tool_progress frame's agent_api_retry flag is not the quarry",
			line: `{"type":"system","subtype":"tool_progress","tool_use_id":"t1",` +
				`"agent_api_retry":true}`,
			wantConfusables: []string{arcapConfusableAgentFlag},
		},
		{
			name: "a control_request_progress whose status reads api_retry is not the quarry",
			line: `{"type":"system","subtype":"control_request_progress","status":"api_retry"}`,
			wantConfusables: []string{
				arcapConfusableStatus, arcapConfusableControlReq,
			},
		},
		{
			name: "an assistant line naming api_retry in prose is neither",
			line: `{"type":"assistant","message":{"role":"assistant","content":` +
				`[{"type":"text","text":"an api_retry happened"}]}}`,
		},
		{
			name: "a nested agent_api_retry at depth still counts as the confusable",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result",` +
				`"content":{"agent_api_retry":false}}]}}`,
			wantConfusables: []string{arcapConfusableAgentFlag},
		},
		{
			name: "a result line is neither",
			line: `{"type":"result","subtype":"error_during_execution","is_error":true}`,
		},
		{
			name: "an undecodable line marks nothing rather than panicking",
			line: `{"type":"system",`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var envelope struct {
				Type    string `json:"type"`
				Subtype string `json:"subtype"`
			}
			_ = json.Unmarshal([]byte(tc.line), &envelope)
			if got := arcapIsAPIRetry(envelope.Type, envelope.Subtype); got != tc.wantQuarry {
				t.Errorf("arcapIsAPIRetry(%q, %q) = %v, want %v",
					envelope.Type, envelope.Subtype, got, tc.wantQuarry)
			}
			want := append([]string(nil), tc.wantConfusables...)
			sort.Strings(want)
			got := arcapConfusables([]byte(tc.line))
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("arcapConfusables() = %v, want %v", got, want)
			}
		})
	}
}

// TestArcapDecodeVerdictAgreesWithTheShippedParser is the drift alarm on the
// streamLine mirror. AC 3's whole point is that WHICH SHAPE api_retry has decides
// the mapping's design, so a mirror quietly disagreeing with the production struct
// would misreport exactly the thing the record exists to establish.
//
// The comparison is restricted to the family where the shipped verdict is
// UNAMBIGUOUS. consumeLine's failure branch absorbs two shapes before
// emitUnrecognized — a harness-authored `user` line, via dropHarnessProseLine, and
// system/permission_denied, via consumePermissionDeniedLine — so for those a
// missing Unrecognized does not prove the decode succeeded. system/api_retry is
// outside both, which is what makes the mirror's answer about the quarry checkable
// at all.
func TestArcapDecodeVerdictAgreesWithTheShippedParser(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		line       string
		wantDecode bool
	}{
		{
			name:       "a plain system line with no message key decodes",
			line:       `{"type":"system","subtype":"api_retry","attempt":2,"error":"overloaded"}`,
			wantDecode: true,
		},
		{
			name:       "a system line whose message is an object decodes",
			line:       `{"type":"system","subtype":"api_retry","message":{"role":"x","content":[]}}`,
			wantDecode: true,
		},
		{
			name:       "a system line whose message is null decodes",
			line:       `{"type":"system","subtype":"api_retry","message":null}`,
			wantDecode: true,
		},
		{
			// The permission_denied shape, on a subtype the gate does NOT absorb, so
			// the shipped parser's undecodable verdict is unambiguous here.
			name:       "message as a STRING fails the whole line, as it does for permission_denied",
			line:       `{"type":"system","subtype":"api_retry","message":"retrying"}`,
			wantDecode: false,
		},
		{
			name:       "a non-string subtype fails the whole line",
			line:       `{"type":"system","subtype":7}`,
			wantDecode: false,
		},
		{
			name:       "a result line decodes",
			line:       `{"type":"result","subtype":"error_during_execution","is_error":true}`,
			wantDecode: true,
		},
		{
			name:       "bytes that are not JSON at all fail",
			line:       `{"type":"system",`,
			wantDecode: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := arcapDecodesIntoStreamLine([]byte(tc.line)); got != tc.wantDecode {
				t.Errorf("arcapDecodesIntoStreamLine() = %v, want %v", got, tc.wantDecode)
			}
			shipped := arcapParserUndecodable(parseOne(t, tc.line))
			if shipped == tc.wantDecode {
				t.Errorf("the shipped parser reports undecodable=%v while the mirror reports "+
					"decodes=%v; the mirror has drifted from streamsup's streamLine and the "+
					"record's AC 3 answer would be wrong", shipped, tc.wantDecode)
			}
		})
	}
}

// TestArcapMessageJSONTypeNamesEveryShape covers all seven values. The string row
// is the one that matters: it is the shape that cost system/permission_denied its
// own gate, and which shape api_retry has decides the mapping's whole design.
func TestArcapMessageJSONTypeNamesEveryShape(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, line, want string }{
		{"absent", `{"type":"system","subtype":"api_retry"}`, arcapMsgAbsent},
		{"null", `{"type":"system","message":null}`, arcapMsgNull},
		{"string", `{"type":"system","message":"retrying in 4s"}`, arcapMsgString},
		{"number", `{"type":"system","message":4}`, arcapMsgNumber},
		{"bool", `{"type":"system","message":true}`, arcapMsgBool},
		{"object", `{"type":"system","message":{"role":"assistant"}}`, arcapMsgObject},
		{"array", `{"type":"system","message":[]}`, arcapMsgArray},
		{"the line does not decode at all", `{"type":`, arcapMsgUndecodable},
		{"a top-level array is not an object", `[1,2,3]`, arcapMsgUndecodable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := arcapMessageJSONType([]byte(tc.line)); got != tc.want {
				t.Errorf("arcapMessageJSONType() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestArcapStagingVerdictSeparatesEveryReading. AC 2's whole demand is that a turn
// whose upstream call actually failed and a turn where claude never reached the
// listener are two distinct published outcomes rather than one shared zero — and
// the one-request row is the third reading a naive design folds into the finding.
func TestArcapStagingVerdictSeparatesEveryReading(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rec         arcapRecord
		wantFinding bool
		wantPhrase  string
	}{
		{
			name:       "the listener never bound",
			rec:        arcapRecord{Staging: arcapStaging{ListenError: "bind failed"}},
			wantPhrase: "INSTRUMENT",
		},
		{
			name:       "claude never reached the staged listener",
			rec:        arcapRecord{Staging: arcapStaging{RequestsSeen: 0}},
			wantPhrase: "never reached",
		},
		{
			name: "the upstream failed once and claude did not retry",
			rec: arcapRecord{Staging: arcapStaging{
				RequestsSeen:  1,
				RequestCensus: map[string]int{"POST /v1/messages": 1},
			}},
			wantPhrase: "did not demonstrably retry",
		},
		{
			// THE ROW A REQUEST COUNT ALONE WOULD GET WRONG. Three requests across
			// three spawns is the supervisor restarting a child that died on the broken
			// upstream, not one child retrying — and publishing it as the finding would
			// claim claude retries in silence on evidence that shows no retry at all.
			name: "three requests across three spawns is a respawn ladder, not a retry ladder",
			rec: arcapRecord{
				SpawnsObserved: 3,
				Staging: arcapStaging{
					RequestsSeen:  3,
					RequestCensus: map[string]int{"POST /v1/messages": 3},
				},
			},
			wantPhrase: "across 3 spawn(s)",
		},
		{
			// THE ROW A SPAWN-ADJUSTED COUNT STILL GETS WRONG, and the reason the
			// verdict counts within a key. Two requests from ONE child, to two
			// DIFFERENT endpoints, is a token count followed by a completion: two
			// upstream calls of a single attempt, and no retry anywhere. Reading the
			// total against the spawn count would publish it as the finding.
			name: "two endpoints from one child is one attempt, not a retry",
			rec: arcapRecord{
				SpawnsObserved: 1,
				Staging: arcapStaging{
					RequestsSeen: 2,
					RequestCensus: map[string]int{
						"POST /v1/messages":              1,
						"POST /v1/messages/count_tokens": 1,
					},
				},
				TerminatedOn: arcapTerminatedResult,
			},
			wantPhrase: "DIFFERENT calls of one attempt",
		},
		{
			// The shape the 2.1.259 capture actually took: one child, a single HEAD
			// connectivity probe, and a ladder of POSTs to one endpoint. Only the
			// repeats within that one key license the claim.
			name: "claude retried a failing upstream and said nothing on the stream",
			rec: arcapRecord{
				SpawnsObserved: 1,
				Staging: arcapStaging{
					RequestsSeen: 13,
					RequestCensus: map[string]int{
						"POST /v1/messages": 12,
						"HEAD /api/hello":   1,
					},
				},
				TerminatedOn: arcapTerminatedResult,
			},
			wantFinding: true,
			wantPhrase:  "SAME endpoint (POST /v1/messages)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.rec.stagingVerdict()
			if got == "" {
				t.Fatal("stagingVerdict() is empty; a zero-api_retry record would name no cause")
			}
			isFinding := strings.Contains(got, "finding about claude")
			if isFinding != tc.wantFinding {
				t.Errorf("stagingVerdict() reads as a finding about claude = %v, want %v\n  got: %s",
					isFinding, tc.wantFinding, got)
			}
			if tc.wantPhrase != "" && !strings.Contains(got, tc.wantPhrase) {
				t.Errorf("stagingVerdict() must contain %q; got: %s", tc.wantPhrase, got)
			}
		})
	}
}

// TestArcapTurnBudgetRespectsTheBinaryDeadline. The regression this guards is
// measured and it is this probe's own: on 2026-09-09 the live gate ran the package
// under `-timeout 20m`, this capture spent 182 s of it, and a sibling capture was
// still running when the binary's timeout fired. A binary killed that way runs no
// cleanups and writes no record, so the tokens buy nothing.
//
// The deadline cannot be set on a *testing.T from a test, so the arithmetic is
// proved through arcapBudgetFor and the wiring is left to the one-line caller.
func TestArcapTurnBudgetRespectsTheBinaryDeadline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		remaining time.Duration
		want      time.Duration
		wantSkip  bool
	}{
		{
			name:      "a whole 20-minute invocation leaves room for the full turn",
			remaining: 20 * time.Minute,
			want:      arcapTurnBudget,
		},
		{
			// The turn shrinks rather than overrunning: 8 minutes left, 3 reserved for
			// teardown and the record write, so 5 to spend and not the 6 it wants.
			name:      "a part-spent invocation shortens the turn to what is left",
			remaining: 8 * time.Minute,
			want:      8*time.Minute - arcapDeadlineReserve,
		},
		{
			name:      "too little left to see a retry ladder out, so nothing is spent",
			remaining: 4 * time.Minute,
			wantSkip:  true,
		},
		{
			name:      "a deadline already passed never starts a turn",
			remaining: -time.Second,
			wantSkip:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, tooLate := arcapBudgetFor(tc.remaining)
			if (tooLate != "") != tc.wantSkip {
				t.Fatalf("arcapBudgetFor(%s) skip = %q, want skip = %v", tc.remaining, tooLate,
					tc.wantSkip)
			}
			if tc.wantSkip {
				return
			}
			if got != tc.want {
				t.Errorf("arcapBudgetFor(%s) = %s, want %s", tc.remaining, got, tc.want)
			}
			if got > tc.remaining-arcapDeadlineReserve {
				t.Errorf("arcapBudgetFor(%s) = %s, which leaves under the %s the record write "+
					"needs; a binary killed by -timeout records nothing", tc.remaining, got,
					arcapDeadlineReserve)
			}
		})
	}
}

// TestArcapFixtureWorthyRefusesEveryBadCapture. The non-vacuity rule here is NOT
// ccapRecord.fixtureWorthy's: that one refuses a record holding zero frames of its
// quarry, while here a zero-api_retry record IS the publishable result under AC 1.
// What must be refused instead is a run whose staged listener saw no request. The
// two accepted rows are the ones that would break AC 1 if they were refused.
func TestArcapFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	good := func() *arcapRecord {
		return &arcapRecord{
			Outcome:           arcapFired,
			ClaudeVersion:     "2.1.259 (Claude Code)",
			APIRetryLineCount: 1,
			Staging:           arcapStaging{RequestsSeen: 3, LoopbackOnly: true},
			Frames: []arcapFrame{
				{Index: 0, Type: "system", Subtype: "init", PayloadEncoding: dropcapEncodingJSONString},
				{Index: 1, Type: "system", Subtype: "api_retry", APIRetry: true,
					PayloadEncoding: dropcapEncodingJSONString},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*arcapRecord)
		want   bool
	}{
		{"a fired capture is promoted", func(*arcapRecord) {}, true},
		{"bare version string, no suffix", func(r *arcapRecord) { r.ClaudeVersion = "2.1.259" }, true},
		{
			// AC 1: a recorded absence is a legitimate published result. Refusing this
			// row would make the whole did-not-fire branch unpublishable.
			"a did-not-fire with requests seen is promoted",
			func(r *arcapRecord) {
				r.Outcome = arcapDidNotFire
				r.APIRetryLineCount = 0
				r.Frames = r.Frames[:1]
			},
			true,
		},
		{
			"a base64 NON-quarry frame is not a reason to refuse",
			func(r *arcapRecord) { r.Frames[0].PayloadEncoding = dropcapEncodingBase64 },
			true,
		},
		{"instrument broken", func(r *arcapRecord) { r.Outcome = arcapInstrumentBroken }, false},
		{
			"the staged listener saw no request",
			func(r *arcapRecord) { r.Staging.RequestsSeen = 0 },
			false,
		},
		{
			"the listener did not bind loopback-only",
			func(r *arcapRecord) { r.Staging.LoopbackOnly = false },
			false,
		},
		{"version unreadable", func(r *arcapRecord) { r.ClaudeVersion = "<unavailable: exec failed>" }, false},
		{"version absent", func(r *arcapRecord) { r.ClaudeVersion = "" }, false},
		{
			"a base64 api_retry frame carries no readable payload to map",
			func(r *arcapRecord) { r.Frames[1].PayloadEncoding = dropcapEncodingBase64 },
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

// TestArcapFixturePathRefusesAnUnusableVersion. The fixture NAME is composed from
// claude's own output, so the shape check is what stops a version string composing
// a path outside testdata/ — and what stops an "<unavailable: …>" naming a fixture
// that vouches for nothing.
func TestArcapFixturePathRefusesAnUnusableVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"the ordinary shape", "2.1.259 (Claude Code)", "testdata/api_retry_v2.1.259.json"},
		{"a bare version", "2.1.259", "testdata/api_retry_v2.1.259.json"},
		{"surrounding whitespace", "  2.1.259 (Claude Code)\n", "testdata/api_retry_v2.1.259.json"},
		{"empty", "", ""},
		{"unavailable", "<unavailable: exec: \"claude\": not found>", ""},
		{"a path separator", "2.1/../../etc", ""},
		{"a leading dot", "..259", ""},
		{"absurdly long", strings.Repeat("9", arcapMaxVersionToken+1), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := arcapFixturePath(tc.version)
			if tc.want == "" {
				if err == nil {
					t.Errorf("arcapFixturePath(%q) = %q with no error; want a refusal", tc.version, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("arcapFixturePath(%q) returned %v; want %q", tc.version, err, tc.want)
			}
			if got != tc.want {
				t.Errorf("arcapFixturePath(%q) = %q, want %q", tc.version, got, tc.want)
			}
			if matched, _ := filepath.Match(arcapFixtureGlob, got); !matched {
				t.Errorf("%q does not match the arming glob %q, so a written fixture would never "+
					"disarm the probe", got, arcapFixtureGlob)
			}
		})
	}
}

// TestArcapAwaitTurnDoesNotDependOnAResult runs offline against a real
// dropcapRecorder fed by hand. The turn here is staged to FAIL, so whether it ever
// emits a `result` is exactly what is unknown; a wait that only ended on one would
// hang on the case under test and land no census at all.
func TestArcapAwaitTurnDoesNotDependOnAResult(t *testing.T) {
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
			want:  arcapTerminatedResult,
			grace: 5 * time.Second,
		},
		{
			name:  "lines then silence ends the turn without a result",
			feed:  "{\"type\":\"system\",\"subtype\":\"api_retry\",\"attempt\":1}\n",
			want:  arcapTerminatedQuiet,
			grace: 5 * time.Second,
		},
		{
			name:  "a turn that never produces a line ends on the budget, not on quiescence",
			feed:  "",
			want:  arcapTerminatedBudget,
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
			if got := arcapAwaitTurn(recorder, 0, quiet, tc.grace); got != tc.want {
				t.Errorf("arcapAwaitTurn() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestArcapStageBindsLoopbackOnly is the guard on the one typo that would publish
// the operator's live bearer token to the LAN. `net.Listen("tcp", ":0")` differs
// from the correct call by four characters, binds every interface, and nothing
// else in this design would notice.
func TestArcapStageBindsLoopbackOnly(t *testing.T) {
	t.Parallel()
	stage, err := arcapNewStage()
	if err != nil {
		t.Fatalf("arcapNewStage: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), arcapShutdownWait)
		defer cancel()
		stage.close(ctx)
	})

	host, _, err := net.SplitHostPort(stage.ln.Addr().String())
	if err != nil {
		t.Fatalf("splitting the bound address: %v", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("the staged upstream bound %q, which is not a loopback address: the operator's "+
			"live bearer token would be reachable off-box", host)
	}
	if got := stage.observe(); !got.LoopbackOnly {
		t.Error("loopback_only is false on a loopback bind; the record's own claim is computed " +
			"from the bound address and must agree with it")
	}
	if !strings.HasPrefix(stage.baseURL(), "http://"+host) {
		t.Errorf("baseURL() = %q does not name the bound host %q", stage.baseURL(), host)
	}
}

// TestArcapStageAnswersRetryableAndReadsNothing drives the listener end to end
// over loopback. The last assertion is AC 4's: a request is sent carrying a
// distinctive header AND a distinctive body, and neither may appear anywhere in
// the marshalled observation.
func TestArcapStageAnswersRetryableAndReadsNothing(t *testing.T) {
	t.Parallel()
	stage, err := arcapNewStage()
	if err != nil {
		t.Fatalf("arcapNewStage: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), arcapShutdownWait)
		defer cancel()
		stage.close(ctx)
	})

	const secretHeader = "arcap-header-must-not-be-recorded"
	const secretBody = "arcap-body-must-not-be-recorded"
	req, err := http.NewRequest(http.MethodPost, stage.baseURL()+"/v1/messages?beta=true",
		strings.NewReader(secretBody))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secretHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("calling the staged upstream: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != arcapAnswerStatus {
		t.Errorf("status = %d, want %d — an auth status is the shape most likely to produce a "+
			"silent zero, which is why this one is an overload", resp.StatusCode, arcapAnswerStatus)
	}
	if string(body) != arcapAnswerBody {
		t.Errorf("body = %q, want %q", body, arcapAnswerBody)
	}

	got := stage.observe()
	if got.RequestsSeen != 1 {
		t.Errorf("requests_seen = %d, want 1 — this count is the record's only discriminator "+
			"between a failed upstream call and claude never arriving", got.RequestsSeen)
	}
	if n := got.RequestCensus["POST /v1/messages"]; n != 1 {
		t.Errorf("request_census = %v, want one POST /v1/messages; the query string must be "+
			"dropped from the key", got.RequestCensus)
	}
	if got.FirstRequestAt == "" || got.LastRequestAt == "" {
		t.Error("the request timestamps are empty after a request was served")
	}

	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the observation: %v", err)
	}
	for _, needle := range []string{secretHeader, secretBody} {
		if strings.Contains(string(blob), needle) {
			t.Errorf("the observation carries %q; the handler must read neither headers nor "+
				"bodies, and this record is a public artefact", needle)
		}
	}
}

// TestArcapStageCapsTheRequestCensus. The peer chooses the path, so an uncapped
// map keyed by it is the one unbounded growth path in this record.
func TestArcapStageCapsTheRequestCensus(t *testing.T) {
	t.Parallel()
	stage, err := arcapNewStage()
	if err != nil {
		t.Fatalf("arcapNewStage: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), arcapShutdownWait)
		defer cancel()
		stage.close(ctx)
	})

	const over = arcapMaxCensusKeys + 5
	for i := 0; i < over; i++ {
		stage.note(http.MethodGet, fmt.Sprintf("/p/%d", i))
	}
	stage.note(http.MethodGet, "/"+strings.Repeat("x", arcapMaxCensusPathLen*2))

	got := stage.observe()
	if len(got.RequestCensus) > arcapMaxCensusKeys {
		t.Errorf("request_census holds %d keys, over the %d cap", len(got.RequestCensus),
			arcapMaxCensusKeys)
	}
	if got.CensusKeysDropped == 0 {
		t.Error("census_keys_dropped is 0 after overflowing the cap; a silent truncation reads " +
			"as a complete census")
	}
	if got.RequestsSeen != over+1 {
		t.Errorf("requests_seen = %d, want %d — the cap must drop KEYS, never requests",
			got.RequestsSeen, over+1)
	}
	for k := range got.RequestCensus {
		if len(k) > arcapMaxCensusPathLen+len("GET ")+len("…") {
			t.Errorf("census key %q exceeds the path cap", k)
		}
	}
}
