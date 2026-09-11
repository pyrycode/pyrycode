//go:build e2e_realclaude

package realclaude

// Evidence capture for #2272 — what a live claude answers to the three MCP control
// verbs, with one server in the spawn's `--mcp-config` document deliberately broken.
//
// # What is unknown, and why a type declaration will not do
//
// Nothing in this repo has ever sent `control_request{subtype:"mcp_status"}`. The
// only MCP evidence in the tree is the `system/init` line's cheap `mcp_servers`
// list, which carries `{name, status}` and nothing else, recorded under healthy
// servers only — so no failed server has ever been observed from claude here.
//
// Two DECLARATIONS exist and they disagree. `@anthropic-ai/claude-agent-sdk`'s
// `sdk.d.ts` promises `{name, status, serverInfo?, error?, config?, scope?}`. The
// claude 2.1.259 binary's own bundled request schema declares `tools` and
// `capabilities` on top of those, and spells `status` as a five-value enum
// (connected / failed / needs-auth / pending / disabled). A decode arm written
// against either drops whatever the other one knows, and neither is a wire
// measurement. That is what this capture replaces.
//
// The same read is where the three request shapes come from, and it is the reason
// this probe does not have to guess them:
//
//	mcp_status     {subtype}
//	mcp_reconnect  {subtype, serverName}
//	mcp_toggle     {subtype, serverName, enabled}
//
// The field is spelled serverName, camelCase — not server_name. A guessed spelling
// would have produced an error reply and a fixture measuring the rig's guess rather
// than claude's contract. A schema read out of the shipped binary is a stronger
// source than sdk.d.ts and is still not the wire, which is why the record keeps
// every reply verbatim rather than only the fields either declaration names.
//
// # Which spawn arm, and why only one
//
// THE DOWNGRADED ARM ONLY: production's four approval flags, no bypass flag. That
// arm carries --mcp-config and --strict-mcp-config, so claude loads ONLY the
// document this probe wrote and the recorded server set is the rig's own three.
//
// The bypass arm was cut, and not for budget. With no --mcp-config there is no
// document to put a broken server in, so it cannot carry the second acceptance
// criterion at all. The servers it does report are whatever the operator has
// registered, seeded into the test HOME from their real config, so the fixture would
// describe one machine. And mcp_status reports a `config` per server, which on that
// arm means the operator's own command, argv and environment — an exposure that is
// certain there rather than merely possible. What a bypass session's server list
// should put on the wire belongs to #2275.
//
// # No user turn
//
// The working pre-turn arm in runInitControlChild proves the control surface accepts
// a request before any prompt. system/init is emitted per turn rather than at spawn,
// so this probe must not wait for it. Sending no turn keeps the permission-prompt
// tool unexercised and the blast radius at zero: a spawn that never prompts cannot
// reach a tool call.
//
// # What is reused, and what is deliberately not
//
// Reused whole: dropcapRecorder, dropcapRedactor, dropcapScanner, dropcapMakeEntry,
// dropcapWaitForChild, dropcapFixedNeedles and parseOne, all from
// dropped_line_capture_test.go, exactly as compaction_capture_test.go reuses them.
// The downgraded-arm spawn scaffolding — shortSocketPath, the stub listener, the
// hand-written document — follows effort_init_capture_test.go.
//
// NOT the initControlFixtureRecord family in initialize_control_writer_test.go: it
// predates the shared helpers and hand-rolls its own writer and scan.
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine, and nothing else — the gate is
// the FIXTURE'S ABSENCE, argued at TestRealClaude_MCPStatusCapture. To force a
// re-capture at a new claude version, over an existing fixture:
//
//	PYRY_PROBE_MCP_STATUS_CAPTURE=1 go test -tags e2e_realclaude -timeout 20m -v \
//	  -run '^TestRealClaude_MCPStatusCapture$' ./internal/e2e/realclaude/
//
// Read WHICH skip: "fixture already exists" is the steady state, while a skip out of
// WithWorktreeAuthenticated means the machine has no claude login and the evidence
// was not produced.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"context"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// mcapEnableEnv FORCES a re-capture when the fixture already exists. It is not the
// gate — see the gate comment in TestRealClaude_MCPStatusCapture.
const mcapEnableEnv = "PYRY_PROBE_MCP_STATUS_CAPTURE"

// The version is spliced into the path rather than repeated, so the filename cannot
// drift from the release the record vouches for. The streamsup-side reader pins the
// same version from the other end and fixtureWorthy refuses to write under a
// mismatched name, so a claude upgrade is a loud instruction to re-capture rather
// than a fixture quietly describing another release.
const (
	mcapFixtureVersion = "2.1.259"
	mcapFixturePath    = "testdata/mcp_status_v" + mcapFixtureVersion + ".json"
)

// Every file-local identifier takes the mcap prefix, for the reason #1260's header
// gives: siblings add files to this package concurrently and a branch-overlap check
// does not catch a same-package identifier collision.
const (
	mcapTicket         = "2272"
	mcapWorkdirName    = "mcap-work"
	mcapRecordName     = "mcap-record.json"
	mcapArtifactPrefix = "pyry-2272-capture-*"
	mcapModel          = "haiku"
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from the
	// sibling probes' so a record can never be mistaken for one of theirs. It is also
	// the id claude echoes back, which is what makes dropcapRedactor's session_id
	// class able to catch it.
	mcapSessionID = "8b21e4f7-6c3d-4a90-9e15-2d7f0a63c4b8"
)

// The server names the document registers. The first two are the production
// constants' own spellings (mcpServerName and mcpFilesServerName in cmd/pyry),
// transcribed rather than imported because those live in package main — the same
// price effort_init_capture_test.go and bypass_approval_argv_probe_test.go pay.
//
// The third is the deliberately broken one and takes a name no production document
// registers, so a reader of the fixture cannot mistake it for something the daemon
// ships. Its command is an absolute path under a temp directory that is never
// created: absolute so no PATH lookup can accidentally resolve it, and under a
// directory that DOES exist so the failure is "this file is not there" rather than
// "this whole tree is not there".
const (
	mcapApproveServer = "pyry_approve"
	mcapFilesServer   = "pyry_files"
	mcapBrokenServer  = "pyry_probe_absent"
	mcapBrokenCommand = "pyry-probe-command-that-does-not-exist"
)

// mcapApproveToolRef is approveToolRef's value, transcribed for the same reason the
// server names are. It is passed to --permission-prompt-tool so the argv is
// production's, and nothing in this capture ever invokes it: no turn is sent.
const mcapApproveToolRef = "mcp__" + mcapApproveServer + "__approve"

const (
	// Per control request. Each verb is awaited on its own budget so a verb claude
	// declines to answer costs one wait, not the whole run.
	mcapReplyBudget = 60 * time.Second
	mcapPoll        = 250 * time.Millisecond
	mcapRunExitWait = 30 * time.Second
)

const (
	mcapFired            = "fired"
	mcapDidNotFire       = "did-not-fire"
	mcapInstrumentBroken = "instrument-broken"
)

// How one control-request wait ended. A budget exit is DATA, not a failure: whether
// claude answers mcp_reconnect and mcp_toggle on this input path at all is one of
// the things this capture exists to find out.
const (
	mcapTerminatedResponse = "response"
	mcapTerminatedBudget   = "budget"
	mcapTerminatedWrite    = "write-error"
)

// Init is observed after the controls rather than awaited before them. The working
// initialize probe established that this stream-json path emits system/init per user
// turn, and this probe deliberately sends no turn.
const (
	mcapInitSeen       = "seen"
	mcapInitNotAwaited = "not-awaited"
)

const (
	mcapSubtypeStatus    = "mcp_status"
	mcapSubtypeReconnect = "mcp_reconnect"
	mcapSubtypeToggle    = "mcp_toggle"
)

// mcapClassRunLocal covers the paths this run mints and hands to claude: the built
// pyry binary, the mcp-config document, the stub socket and the absent command.
// Every one of them is a $TMPDIR path, and /var/folders/ plus /private/var/folders/
// are two of dropcapFixedNeedles' five fixed deny classes — so an unredacted one
// does not merely leak, it refuses the whole write.
const mcapClassRunLocal = "run_local_temp"

const mcapSpawnShapeDelta = "Production's DOWNGRADED arm, not the YOLO shape the dropcap/ccap captures " +
	"use: --permission-prompt-tool, --mcp-config, --strict-mcp-config and --permission-mode default, " +
	"with NO --dangerously-skip-permissions. That is deliberate and is the whole reason this record can " +
	"carry a broken server: --mcp-config is the only route by which a server this rig chose reaches a " +
	"spawn, and --strict-mcp-config is what stops the operator's own user- and project-scoped servers " +
	"loading beside it. The bypass arm carries neither flag, so it can carry no broken server and would " +
	"report one machine's inventory; it is #2275's question, not this record's."

const mcapLimitations = "One spawn, one spawn shape, one claude version, one model (" + mcapModel + "), " +
	"three servers, and NO user turn at all — the servers are read by pre-turn controls, never after " +
	"a tool call. Cross-version, cross-model and cross-arm stability are UNMEASURED. The broken server is " +
	"broken in ONE way, an absent stdio command; a server that starts and then fails its handshake, one " +
	"that needs auth, and every remote (HTTP/SSE) transport are UNMEASURED and may report different keys. " +
	"A key absent from servers[].keys did not appear on THIS reply, which is not the same as claude never " +
	"sending it — and the union across these three servers over-reports, because a key one server carried " +
	"is not one every server carries."

const mcapRedactionRationale = "Inherited whole from #1260 (see dropcapRedactionRationale): a fresh empty " +
	"non-git workdir under a per-test temp $HOME, no prompt at all, no os.Environ() read into the record, " +
	"the declared dropcapRedactor substitution table over every string, and dropcapScanner as a " +
	"fail-closed deny-scan over the marshalled record. " +
	"WHAT THIS CAPTURE SPECIFICALLY CAN CARRY, stated rather than left to be inferred by whoever decides " +
	"to paste this record into a public issue. FIRST, an mcp_status reply reports a `config` PER SERVER, " +
	"and a stdio server's config is its command, its argv and its environment — which is where an API key " +
	"lives. It is KEPT, because whether claude echoes the document back is precisely what #2275 has to " +
	"decode, and the defences for keeping it are that the three servers are rig-authored with no " +
	"environment of their own, that every run-local path they name is substituted under " +
	"" + mcapClassRunLocal + ", and that the deny-scan runs AHEAD of every filesystem call so a hit leaves " +
	"nothing half-written. SECOND, the server NAMES are kept unredacted: --strict-mcp-config means they " +
	"are this rig's three literals rather than an operator's inventory, which is exactly why the bypass " +
	"arm was cut. THIRD, the recorded --mcp-config document is the rig's own bytes, and the argv is " +
	"production's flag set; both name run-local paths and both go through the table. " +
	"FOURTH, stderr_capture is the child's own stderr, verbatim and free-form — claude's diagnostics, " +
	"including whatever it prints when it refuses the --mcp-config document, and the place an " +
	"authentication failure prints. It is KEPT because a claude that refused the document complains " +
	"NOWHERE ELSE, and a capture that cannot say why it failed is the defect #2307 was filed for. Its " +
	"defences are the same three: every string goes through the substitution table BEFORE it is " +
	"capped, so a truncation cannot strand half a path the table would otherwise have replaced; the " +
	"deny-scan runs over it ahead of every filesystem call, with both credential environment " +
	"variables live as needles; and it is bounded by capFixtureCapture at stderrFixtureCap. The bound " +
	"is named by symbol rather than spelled as a number here, so this prose cannot drift from the " +
	"value actually applied. THE CAP IS NOT A REDACTION — it bounds how much a reviewer must read, " +
	"and nothing else."

// --- the record ----------------------------------------------------------------

// mcapRequest is one control verb: what went out, under which id, and what came
// back. reply_indices are recorder indices into frames, so a reader can find the
// answering lines without re-deriving the correlation.
type mcapRequest struct {
	Subtype      string  `json:"subtype"`
	RequestID    string  `json:"request_id"`
	Sent         string  `json:"sent"`
	ReplyIndices []int   `json:"reply_indices"`
	TerminatedOn string  `json:"terminated_on"`
	WaitSeconds  float64 `json:"wait_seconds"`
	WriteError   string  `json:"write_error,omitempty"`
}

// mcapFrame is one line of the child's stdout — every line, not only the replies.
// The payload half comes from dropcapMakeEntry so the base64 arm for invalid UTF-8
// is shared rather than re-derived.
//
// request_id is read from the LINE'S OWN BYTES rather than from the rig's
// bookkeeping, so a frame is self-describing: a reader can pair replies with
// requests using nothing but the record.
//
// events_emitted is the SHIPPED parser's verdict (parseOne), carried as DATA rather
// than as a filter: a non-zero count on a control_response is that line reaching a
// mapped lane and putting a row in the operator's chat today.
type mcapFrame struct {
	Index                   int    `json:"index"`
	Type                    string `json:"type"`
	Subtype                 string `json:"subtype,omitempty"`
	RequestID               string `json:"request_id,omitempty"`
	PayloadLenBytesCaptured int    `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int    `json:"payload_len_bytes"`
	PayloadEncoding         string `json:"payload_encoding"`
	Payload                 string `json:"payload,omitempty"`
	PayloadB64              string `json:"payload_b64,omitempty"`
	EventsEmitted           int    `json:"events_emitted"`
}

// mcapServer is one entry of the mcp_status reply's mcpServers array — AC 3.
//
// keys is the EXACT key set claude sent, and key_types maps each key to its value's
// JSON type. Types rather than values, because the question a decode arm asks is
// "what shape do I have to accept", and the values are still present verbatim in the
// frame the entry came from. Both are derived from the entry's own bytes rather than
// from a struct this file declared: unmarshalling into a declared struct would
// report the keys THIS AUTHOR expected, which is the failure mode the whole ticket
// exists to avoid.
type mcapServer struct {
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Error    string            `json:"error,omitempty"`
	Keys     []string          `json:"keys"`
	KeyTypes map[string]string `json:"key_types"`
	// The credential surface, kept deliberately — see mcapRedactionRationale. Held
	// redacted and as raw bytes rather than decoded, because its shape is a union over
	// four transports and decoding it here would pick one.
	Config json.RawMessage `json:"config,omitempty"`
}

type mcapRecord struct {
	Ticket          string   `json:"ticket"`
	ClaudeVersion   string   `json:"claude_version"`
	CapturedAt      string   `json:"captured_at"`
	IsCapture       bool     `json:"is_capture"`
	Model           string   `json:"model"`
	SpawnShape      []string `json:"spawn_shape"`
	SpawnShapeDelta string   `json:"spawn_shape_delta"`
	Workdir         string   `json:"workdir"`

	// AC 1's "alongside the spawn's argv and its --mcp-config document". The document
	// is the bytes handed to claude, redacted; the names are its three keys in the
	// order this file registers them, so a reader need not parse the document to know
	// which entry was the broken one.
	MCPConfigDocument string   `json:"mcp_config_document"`
	MCPConfigServers  []string `json:"mcp_config_servers"`
	BrokenServer      string   `json:"broken_server"`

	Outcome       string `json:"outcome"`
	OutcomeDetail string `json:"outcome_detail"`

	// The cheap list, recorded beside the rich one so #2275 can compare what the two
	// surfaces say about the same three servers. Raw, because whether 2.1.259 spells it
	// as an array of objects or something else is part of what is being recorded.
	InitSeen bool `json:"init_seen"`
	// Init is observational context only. "not-awaited" means no init arrived while
	// the three request waits ran; it is never a prerequisite for sending them.
	InitWait       string          `json:"init_wait"`
	InitMCPServers json.RawMessage `json:"init_mcp_servers,omitempty"`

	Requests []mcapRequest `json:"requests"`

	// Where the mcpServers array was found — one of three placements, named rather than
	// assumed. A reader that stops at the wrong depth reports a false absence.
	StatusReplyLocation string       `json:"status_reply_location"`
	ServerCount         int          `json:"server_count"`
	Servers             []mcapServer `json:"servers"`
	ServerKeyUnion      []string     `json:"server_key_union"`

	// Every one of these is written by mcapFillCapture and by nothing else, on every
	// terminating path. NO omitempty on any of them: `{}` and `[]` say the fill RAN and
	// the recorder held nothing, `null` says it never ran — and telling those apart is
	// the entire reading the seven records of 2026-09-09 could not support.
	LinesCaptured  int            `json:"lines_captured"`
	LineTypeCensus map[string]int `json:"line_type_census"`
	UndecodedLines int            `json:"undecoded_lines"`
	Frames         []mcapFrame    `json:"frames"`

	// The child's stderr, accumulated across every respawn streamsup performs — so a
	// crash loop shows up here as repetition rather than as a single message. Redacted
	// through the shared table and capped at stderrFixtureCap, in that order, by
	// mcapFillCapture; see its comment for why the order is a security property.
	StderrCapture string `json:"stderr_capture"`

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

func (rec *mcapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
}

// fixtureWorthy answers whether this record may be promoted to mcapFixturePath, and
// names the reason when it may not.
//
// Every rejection is a case where the record is still valuable EVIDENCE — it is
// written to the artifact dir either way — but would be a lie as the committed
// proof. The version arm is the producing half of the pin the streamsup reader
// enforces; `claude --version` prints "<version> (Claude Code)", so the comparison
// is on the leading token, and an "<unavailable: ...>" fails it too.
//
// The broken-server arm is the one that is specific to this ticket. A reply that
// reported only the two healthy servers would still have keys to pin, and would
// still be silently missing the entire second acceptance criterion — the failure
// path is the half no existing capture in this tree has ever observed.
func (rec *mcapRecord) fixtureWorthy() (string, bool) {
	if rec.Outcome != mcapFired {
		return fmt.Sprintf("outcome=%s", rec.Outcome), false
	}
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != mcapFixtureVersion {
		return fmt.Sprintf("claude_version %q is not the %s pinned in the fixture name — repin "+
			"mcapFixtureVersion and mcpStatusCaptureVersion together, then re-run", got,
			mcapFixtureVersion), false
	}
	if rec.ServerCount == 0 {
		return "the mcp_status reply reported zero servers — a vacuous fixture proves nothing", false
	}
	required := map[string]bool{
		mcapSubtypeStatus: false, mcapSubtypeReconnect: false, mcapSubtypeToggle: false,
	}
	requestIDs := map[string]bool{}
	for _, request := range rec.Requests {
		if _, ok := required[request.Subtype]; !ok {
			continue
		}
		if request.RequestID == "" || requestIDs[request.RequestID] {
			return fmt.Sprintf("%s has a missing or duplicate request id %q", request.Subtype,
				request.RequestID), false
		}
		requestIDs[request.RequestID] = true
		if request.WriteError != "" || request.TerminatedOn != mcapTerminatedResponse ||
			len(request.ReplyIndices) == 0 {
			return fmt.Sprintf("%s did not retain a successful write and correlated reply", request.Subtype), false
		}
		matched := false
		for _, index := range request.ReplyIndices {
			for _, frame := range rec.Frames {
				if frame.Index == index && frame.RequestID == request.RequestID && frame.Subtype == "success" {
					matched = true
				}
			}
		}
		if !matched {
			return fmt.Sprintf("%s reply indices do not name a correlated success frame", request.Subtype), false
		}
		required[request.Subtype] = true
	}
	for subtype, seen := range required {
		if !seen {
			return fmt.Sprintf("no complete correlated %s request and reply", subtype), false
		}
	}
	sawBroken := false
	for _, s := range rec.Servers {
		if len(s.Keys) == 0 {
			return fmt.Sprintf("server %q was recorded with an empty key set, so the record "+
				"contradicts itself and no decode arm can be written against it", s.Name), false
		}
		if s.Name == mcapBrokenServer {
			sawBroken = true
			if s.Status == "" || s.Error == "" {
				return "the deliberately broken server lacks observed status or error", false
			}
		}
	}
	if !sawBroken {
		return fmt.Sprintf("the reply reported %d server(s) and none of them is %q — the deliberately "+
			"broken entry is the half of this measurement no capture in this tree already has, so a "+
			"record without it is not the proof this ticket owes", rec.ServerCount, mcapBrokenServer), false
	}
	// The reader refuses any encoding but json-string, and dropcapMakeEntry emits
	// base64 with an EMPTY payload for a frame that is not valid UTF-8. Promoting one
	// would redden `make check` for every unrelated ticket. This is a refusal to
	// promote rather than a fatal: the record still holds the frame as evidence.
	for _, f := range rec.Frames {
		if f.RequestID != "" && f.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("reply frame %d is encoded %q and the reader reads only %q — a "+
				"non-UTF-8 reply carries no readable payload, so a fixture holding one would fail the "+
				"assertion it exists to feed", f.Index, f.PayloadEncoding, dropcapEncodingJSONString), false
		}
	}
	return "", true
}

// --- the wire ------------------------------------------------------------------

// mcapControlRequest is the wire shape of every control line this probe writes on
// the child's held-open stdin.
//
// Request is a map rather than a struct because the three verbs carry three
// different field sets and a struct wide enough for all of them would emit
// "serverName":"" on the bare mcp_status shape. Go marshals map keys in sorted
// order, so the bytes are deterministic run to run, which is what makes the recorded
// `sent` value diffable.
type mcapControlRequest struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id"`
	Request   map[string]any `json:"request"`
}

// mcapControlLine returns one newline-terminated control line. extra carries the
// verb's own fields — serverName, enabled — and a nil map yields the bare shape.
//
// Marshalled structured, never string-concatenated, so the appended '\n' is the only
// raw newline in the envelope: the one-physical-line invariant the child's reader
// depends on.
func mcapControlLine(subtype, requestID string, extra map[string]any) ([]byte, error) {
	inner := map[string]any{"subtype": subtype}
	for k, v := range extra {
		if k == "subtype" {
			return nil, fmt.Errorf("mcap: %s: extra must not override subtype", subtype)
		}
		inner[k] = v
	}
	b, err := json.Marshal(mcapControlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   inner,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s control request: %w", subtype, err)
	}
	return append(b, '\n'), nil
}

// mcapDriveRequests sends the three MCP controls without waiting for system/init.
// Before reconnect it polls status until the rig-owned healthy server is connected;
// every poll remains in the record with its own id and bounded reply outcome.
// Reply timing is injectable so startup silence is proved offline without sleeping.
// A write failure stops later sends because the pipe is no longer trustworthy.
func mcapDriveRequests(stdin io.Writer, recorder *dropcapRecorder, red *dropcapRedactor,
	budget, poll time.Duration) ([]mcapRequest, error) {
	requests := make([]mcapRequest, 0, 4)
	send := func(subtype string, extra map[string]any, replyBudget time.Duration) (mcapRequest, error) {
		requestID := fmt.Sprintf("req_%s_%d", subtype, len(requests)+1)
		line, err := mcapControlLine(subtype, requestID, extra)
		if err != nil {
			return mcapRequest{}, fmt.Errorf("build %s control line: %w", subtype, err)
		}
		entry := mcapRequest{Subtype: subtype, RequestID: requestID, Sent: red.str(string(line))}
		start := time.Now()
		if _, err := stdin.Write(line); err != nil {
			entry.WriteError = red.str(err.Error())
			entry.TerminatedOn = mcapTerminatedWrite
			entry.WaitSeconds = time.Since(start).Seconds()
			requests = append(requests, entry)
			return entry, fmt.Errorf("write %s control request: %w", subtype, err)
		}
		entry.ReplyIndices, entry.TerminatedOn = mcapAwait(recorder, requestID, replyBudget, poll)
		entry.WaitSeconds = time.Since(start).Seconds()
		requests = append(requests, entry)
		return entry, nil
	}

	status, err := send(mcapSubtypeStatus, nil, budget)
	if err != nil {
		return requests, err
	}
	if status.TerminatedOn != mcapTerminatedResponse {
		return requests, fmt.Errorf("await %s readiness: initial mcp_status ended on %s",
			mcapApproveServer, status.TerminatedOn)
	}
	readinessDeadline := time.Now().Add(budget)
	for !mcapStatusReports(recorder, status.RequestID, mcapApproveServer, "connected") {
		remaining := time.Until(readinessDeadline)
		if remaining <= 0 {
			return requests, fmt.Errorf("await %s readiness: server did not report connected within %s",
				mcapApproveServer, budget)
		}
		if poll > 0 {
			delay := min(poll, remaining)
			time.Sleep(delay)
		}
		remaining = time.Until(readinessDeadline)
		if remaining <= 0 {
			return requests, fmt.Errorf("await %s readiness: server did not report connected within %s",
				mcapApproveServer, budget)
		}
		status, err = send(mcapSubtypeStatus, nil, remaining)
		if err != nil {
			return requests, err
		}
		if status.TerminatedOn != mcapTerminatedResponse {
			return requests, fmt.Errorf("await %s readiness: mcp_status ended on %s",
				mcapApproveServer, status.TerminatedOn)
		}
	}

	if _, err := send(mcapSubtypeReconnect,
		map[string]any{"serverName": mcapApproveServer}, budget); err != nil {
		return requests, err
	}
	if _, err := send(mcapSubtypeToggle,
		map[string]any{"serverName": mcapBrokenServer, "enabled": false}, budget); err != nil {
		return requests, err
	}
	return requests, nil
}

// mcapStatusReports reads the correlated status reply directly from the recorder.
// It intentionally checks the wire response rather than elapsed time: reconnect is
// sent only after the fixed test-owned server actually reports the requested state.
func mcapStatusReports(recorder *dropcapRecorder, requestID, serverName, wantStatus string) bool {
	lines, _ := recorder.snapshot()
	for _, line := range lines {
		if !line.Decoded || mcapResponseRequestID(line.Raw) != requestID {
			continue
		}
		servers, _, ok := mcapServersFrom(line.Raw)
		if !ok {
			continue
		}
		for _, server := range servers {
			var state struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			}
			if json.Unmarshal(server, &state) == nil && state.Name == serverName && state.Status == wantStatus {
				return true
			}
		}
	}
	return false
}

// mcapResponseRequestID returns the request_id one line carries, or empty.
//
// THREE PLACEMENTS ARE READ — top level, under `response`, and under
// `response.response`. streamsup's control_response arm records, as measured shape,
// that subtype and request_id arrive nested under `response` rather than at top
// level; #1688 measured the initialize reply's PAYLOAD nesting one level deeper
// still. Reading all three costs one struct and is the difference between pairing a
// reply with its request and reporting a false absence.
func mcapResponseRequestID(raw []byte) string {
	var env struct {
		RequestID string `json:"request_id"`
		Response  struct {
			RequestID string `json:"request_id"`
			Response  struct {
				RequestID string `json:"request_id"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	for _, id := range []string{env.RequestID, env.Response.RequestID, env.Response.Response.RequestID} {
		if id != "" {
			return id
		}
	}
	return ""
}

// mcapResponseSubtype reads the response outcome at the same three placements as
// mcapResponseRequestID. The top-level recorder cannot see a nested success/error
// subtype, but fixture promotion must reject an unsupported-command error reply.
func mcapResponseSubtype(raw []byte) string {
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
		return ""
	}
	for _, subtype := range []string{env.Subtype, env.Response.Subtype, env.Response.Response.Subtype} {
		if subtype != "" {
			return subtype
		}
	}
	return ""
}

// mcapAwait polls the recorder for lines answering requestID and returns their
// recorder indices plus how the wait ended.
//
// It does NOT block on a `result` line and never could: no turn is sent, so no
// result is ever produced. The budget arm is therefore the ordinary exit for a verb
// claude declines to answer, not an error path — which is why it is recorded per
// request rather than fataled on.
//
// budget and poll are parameters rather than the constants directly so both exits
// are provable offline in milliseconds; the live call sites pass mcapReplyBudget and
// mcapPoll.
func mcapAwait(recorder *dropcapRecorder, requestID string, budget, poll time.Duration) ([]int, string) {
	deadline := time.Now().Add(budget)
	for {
		var hits []int
		lines, _ := recorder.snapshot()
		for _, c := range lines {
			if c.Decoded && mcapResponseRequestID(c.Raw) == requestID {
				hits = append(hits, c.Index)
			}
		}
		if len(hits) > 0 {
			return hits, mcapTerminatedResponse
		}
		if time.Now().After(deadline) {
			return nil, mcapTerminatedBudget
		}
		time.Sleep(poll)
	}
}

// --- reading the reply -----------------------------------------------------------

// The three placements mcapServersFrom searches, named so the record can say which
// one held the array rather than leaving a reader to guess.
const (
	mcapLocTop      = "mcpServers"
	mcapLocResponse = "response.mcpServers"
	mcapLocNested   = "response.response.mcpServers"
)

// mcapServersFrom locates the mcpServers array in one reply and returns its entries
// undecoded, with the placement that held it.
//
// PRESENCE IS DECIDED ON THE RAW BYTES, not on a decoded slice: unmarshalling
// straight into a slice makes `"mcpServers":[]` and an absent key both arrive as
// nil, and "claude reported no servers" is a different finding from "claude sent no
// such key". Entries stay json.RawMessage so the keys are read from claude's bytes
// rather than from a struct this file declared.
func mcapServersFrom(raw []byte) ([]json.RawMessage, string, bool) {
	var env struct {
		MCPServers json.RawMessage `json:"mcpServers"`
		Response   struct {
			MCPServers json.RawMessage `json:"mcpServers"`
			Response   struct {
				MCPServers json.RawMessage `json:"mcpServers"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "", false
	}
	candidates := []struct {
		where string
		value json.RawMessage
	}{
		{mcapLocTop, env.MCPServers},
		{mcapLocResponse, env.Response.MCPServers},
		{mcapLocNested, env.Response.Response.MCPServers},
	}
	for _, cand := range candidates {
		if len(cand.value) == 0 || string(cand.value) == "null" {
			continue
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(cand.value, &entries); err != nil {
			return nil, "", false
		}
		return entries, cand.where, true
	}
	return nil, "", false
}

// The JSON type names key_types records. Spelled here rather than borrowed from
// encoding/json's reflection vocabulary, because they describe the WIRE and a
// reader of the fixture should not have to know Go to read them.
const (
	mcapTypeObject  = "object"
	mcapTypeArray   = "array"
	mcapTypeString  = "string"
	mcapTypeNumber  = "number"
	mcapTypeBool    = "boolean"
	mcapTypeNull    = "null"
	mcapTypeUnknown = "unknown"
)

// mcapJSONType classifies one value by its first byte, which is enough because the
// bytes came out of encoding/json and are therefore well-formed. A null is reported
// as null rather than collapsed into absence: "claude sent this key with no value"
// and "claude did not send this key" are different facts and only the key set can
// tell them apart.
func mcapJSONType(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return mcapTypeUnknown
	}
	switch trimmed[0] {
	case '{':
		return mcapTypeObject
	case '[':
		return mcapTypeArray
	case '"':
		return mcapTypeString
	case 't', 'f':
		return mcapTypeBool
	case 'n':
		return mcapTypeNull
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return mcapTypeNumber
	default:
		return mcapTypeUnknown
	}
}

// mcapServerShape reads one mcpServers entry into the record — AC 2 and AC 3.
//
// Keys come from a map[string]json.RawMessage, so they are what claude sent. name,
// status and error are pulled out separately because they are the three fields a
// human reading the fixture looks for first; every one of them is a plain string in
// both declarations, and a non-string arriving there is left out of the named field
// while staying visible in key_types. The error text is kept as claude wrote it,
// redacted — that text is the second acceptance criterion.
//
// Key NAMES are not redacted. They are the measurement itself, they are chosen from
// claude's own fixed vocabulary rather than from anything the operator typed, and
// substituting inside one would corrupt the thing being recorded. The deny-scan
// still sees them, so a key name that somehow carried a path would refuse the write
// rather than pass quietly.
func mcapServerShape(entry json.RawMessage, red *dropcapRedactor) (mcapServer, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entry, &fields); err != nil {
		return mcapServer{}, fmt.Errorf("decode mcpServers entry: %w", err)
	}
	out := mcapServer{Keys: make([]string, 0, len(fields)), KeyTypes: map[string]string{}}
	str := func(key string) string {
		raw, ok := fields[key]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return red.str(s)
	}
	for k, v := range fields {
		out.Keys = append(out.Keys, k)
		out.KeyTypes[k] = mcapJSONType(v)
	}
	sort.Strings(out.Keys)
	out.Name = str("name")
	out.Status = str("status")
	out.Error = str("error")
	if cfg, ok := fields["config"]; ok {
		out.Config = json.RawMessage(red.redact(append([]byte(nil), cfg...)))
	}
	return out, nil
}

// mcapCollect builds a frame for EVERY captured line, not only the replies — AC 1
// asks for the reply lines verbatim, and a record that dropped everything else would
// not be able to say what else the child volunteered while answering. An undecodable
// line still gets a frame: losing it would make the record disagree with
// lines_captured for no stated reason.
func mcapCollect(t *testing.T, lines []dropcapCaptured, red *dropcapRedactor) []mcapFrame {
	t.Helper()
	out := []mcapFrame{}
	for _, c := range lines {
		// The payload half, shared with #1260 so the base64 arm for invalid UTF-8 is not
		// re-derived. The reason field is unused here, so it is passed empty and dropped.
		entry := dropcapMakeEntry(c, "", red)
		out = append(out, mcapFrame{
			Index:                   entry.Index,
			Type:                    entry.Type,
			Subtype:                 mcapResponseSubtype(c.Raw),
			RequestID:               mcapResponseRequestID(c.Raw),
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

// mcapFillCapture takes everything the instrument holds into the record: the line
// count, the cap accounting, the census, the frames and the child's stderr.
//
// IT IS THE ONLY WRITER OF THOSE FIELDS, and mcapPersist calls it, so every
// terminating path reports what was actually held. Before #2307 the fill sat inline
// below the init wait, and the returns above it each wrote a record whose
// lines_captured, census and frames were still the zero values it was constructed
// with. Seven gate runs read as "claude printed nothing" when the record could not
// tell that from "claude printed lines, none of them system/init".
//
// EVERY FIELD IS ASSIGNED, NEVER APPENDED, so the call is idempotent re-derivation
// from the recorder and the buffer, which are the sources of truth. The happy path
// calls it inline as well, because the server extraction reads rec.Frames; persist
// then re-runs it over a snapshot that may hold later lines, and more frames is more
// evidence rather than a disagreement — Requests[].ReplyIndices are recorder indices
// and are stable.
//
// REDACTION RUNS BEFORE THE CAP, and the order is a security property rather than a
// tidiness one. red.str over the whole string lets every run-local path match its
// substitution rule; capping first can cut a path mid-way, leaving a fragment the
// table no longer matches while it still carries one of dropcapFixedNeedles' fixed
// deny literals — which refuses the entire write, losing the record to a truncation
// artifact rather than to anything the capture observed. Reversed, the cut can strand
// nothing worse than half of the rig's own placeholder.
//
// The cap is applied HERE rather than on a local copy at write time, as
// cucapScanRecord does: mcapWriteRecord marshals rec itself, so capping there would
// leave the record in memory and the file on disk disagreeing about a field a reader
// is told the record carries.
//
// It takes a *testing.T for parseOne and so must never be called from a goroutine.
// parseOne's own t.Fatalf is unreachable — streamsup.Parser.Write always returns a nil
// error — which is what makes this safe to call from mcapPersist's cleanup, where a
// fatal would skip every cleanup registered earlier.
func mcapFillCapture(t *testing.T, rec *mcapRecord, recorder *dropcapRecorder,
	stderr *probeSyncBuffer, red *dropcapRedactor) {
	t.Helper()
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial
	rec.LineTypeCensus, rec.UndecodedLines = mcapCensus(lines)
	rec.Frames = mcapCollect(t, lines, red)
	rec.StderrCapture = capFixtureCapture(red.str(string(stderr.Bytes())))
}

// mcapCensus counts what was on the wire, content-free: top-level types with their
// subtypes. No tool census, because no turn is sent and no tool can run.
func mcapCensus(lines []dropcapCaptured) (map[string]int, int) {
	types := map[string]int{}
	undecoded := 0
	for _, c := range lines {
		if !c.Decoded {
			undecoded++
			continue
		}
		key := c.Type
		if c.Subtype != "" {
			key = c.Type + "/" + c.Subtype
		}
		types[key]++
	}
	return types, undecoded
}

// --- the writer ------------------------------------------------------------------

// mcapWriteRecord marshals, deny-scans and only then writes — AC 4.
//
// THE SCAN RUNS AHEAD OF EVERY FILESYSTEM CALL, which is what makes the refusal
// total rather than tidy: on a hit NOTHING is written, not the record and not the
// fixture, so there is no half-written file for a later reader to find. That
// ordering is the whole acceptance criterion, and it is why this returns an error
// instead of calling t.Fatalf the way ccapWriteRecord does — a writer that fatals
// cannot be handed a hostile record and asked what it left on disk.
//
// #1260's rule verbatim on the message: it names the CLASS only, never the matched
// value. An error that helpfully quotes the leak is the own-goal the scan exists to
// prevent.
//
// fixtureReason names why the record was not promoted, and is empty when it was.
func mcapWriteRecord(dir, fixturePath string, red *dropcapRedactor, scanner dropcapScanner, rec *mcapRecord) (
	recordPath, fixtureReason string, err error) {
	rec.Redaction = red.substitutions()

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal record: %w", err)
	}
	hits, notApplied := scanner.scan(blob)
	// A base64 payload hides its bytes from a scan of the marshalled record, so the
	// decoded bytes are scanned too.
	for _, f := range rec.Frames {
		if f.PayloadB64 == "" {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(f.PayloadB64)
		if derr != nil {
			return "", "", fmt.Errorf("frame %d: decode base64 payload for the scan: %w", f.Index, derr)
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		// Deduped before it is counted: scan already collapses per class within one call,
		// but the base64 pass above appends across calls, so a class hit in two payloads
		// would otherwise be reported as two classes.
		sort.Strings(hits)
		classes := mcapUnique(hits)
		return "", "", fmt.Errorf("deny-scan found %d denied class(es) still present in the record: %v; "+
			"NOTHING was written — not the record, not the fixture. Extend dropcapRedactor's table with "+
			"the named class and re-run the capture. The offending value is deliberately not printed: "+
			"putting it in output is exactly the exposure this scan exists to prevent",
			len(classes), classes)
	}
	rec.CredentialScanSkipped = notApplied

	// Re-marshal so credential_scan_skipped ships in the written bytes. Its value is a
	// list of CLASS NAMES the scan could not apply, which is the one thing that makes a
	// silently-off credential net visible after the fact.
	blob, err = json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("re-marshal record: %w", err)
	}

	recordPath = filepath.Join(dir, mcapRecordName)
	if err := os.WriteFile(recordPath, append(blob, '\n'), 0o600); err != nil {
		return "", "", fmt.Errorf("write record: %w", err)
	}

	// The same deny-scanned bytes, promoted in-repo so the run that produced them is
	// the run that lands them. A capture that still needs a human to copy a file out of
	// a tempdir is a capture #1763 says will not land.
	if reason, ok := rec.fixtureWorthy(); !ok {
		return recordPath, reason, nil
	}
	if err := os.WriteFile(fixturePath, append(blob, '\n'), 0o600); err != nil {
		return recordPath, "", fmt.Errorf("write fixture %s: %w", fixturePath, err)
	}
	return recordPath, "", nil
}

// mcapUnique collapses a sorted slice, so a class hit by two needles is named once.
func mcapUnique(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// --- the live capture --------------------------------------------------------------

// TestRealClaude_MCPStatusCapture spawns one claude on production's downgraded arm
// with a three-server document, sends the three MCP control verbs, and writes the
// record.
//
// Ordering is load-bearing in one place: newDropcapScanner reads
// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY via os.Getenv AS DENY NEEDLES, and
// WithWorktreeAuthenticated is what re-pins them into this process. Building the
// scanner first yields an EMPTY needle, which dropcapScanner.scan reports as
// notApplied — silently skipped, not fatal. The credential net would be off while
// every message still read green, so the scanner is built after the auth helper and
// the skipped classes ship in the record.
//
// NOT t.Parallel(): WithWorktreeAuthenticated reaches t.Setenv.
func TestRealClaude_MCPStatusCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE, and that is a deliberate break from the
	// env-gated one-off probes in this package. #2089 and #2229 argue it in full and
	// the reasoning is identical here: `make e2e-realclaude` never sets a custom
	// PYRY_PROBE_* variable, so an env gate skips on the ENV check BEFORE the
	// credential check, the live gate passes vacuously, and the fixture never lands.
	// That is CLAUDE.md § Testing's #1763 failure exactly — a green gate and a spent
	// budget look identical whether the bytes landed or not.
	force := os.Getenv(mcapEnableEnv) == "1"
	if _, err := os.Stat(mcapFixturePath); err == nil && !force {
		t.Skipf("#2272 mcp_status capture: the fixture %s already exists, so there is nothing to "+
			"capture and this costs no claude spawn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 20m -v \\\n"+
			"    -run '^TestRealClaude_MCPStatusCapture$' ./internal/e2e/realclaude/",
			mcapFixturePath, mcapEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner
	pyryBin := ensurePyryBuilt(t)        // the binary the two production entries name

	// Deliberately NOT t.TempDir(): the operator needs the record after the test ends,
	// and the dispatcher's real-claude gate verifies from a detached worktree it then
	// removes. #2229's in-repo fixture went out with that worktree and its record,
	// written here, is what survived to land the bytes later.
	artifactDir, err := os.MkdirTemp("", mcapArtifactPrefix)
	if err != nil {
		t.Fatalf("#2272: create artifact dir: %v", err)
	}

	// A fresh EMPTY directory, deliberately not a git repo: no branch names and no file
	// contents can reach a payload.
	workdir := filepath.Join(home, mcapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2272: create workdir: %v", err)
	}

	// BOUND BEFORE THE DOCUMENT IS WRITTEN AND BEFORE THE CHILD STARTS, so neither
	// production server can race a not-yet-listening socket. shortSocketPath is reused
	// rather than reinvented: macOS caps a Unix socket path near 104 bytes and a path
	// under the long pinned HOME can overrun it.
	//
	// NOTHING IS EXPECTED TO DIAL IT. runMCPApprove and runMCPFiles both serve MCP over
	// stdio and reach the control socket only when a tool is CALLED, and no turn is
	// sent here. The listener exists because the only committed evidence that a real
	// claude reports pyry_approve as `connected` was recorded against a listening
	// socket, and a second accidentally-broken server would be a confound in a capture
	// whose entire point is one deliberately broken one. Accept-and-close rather than a
	// served protocol, for the same reason.
	socketPath := shortSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		// The path is test-owned and temporary, so naming it is safe and is the only thing
		// that makes an EADDRINUSE or a too-long path diagnosable.
		t.Fatalf("#2272: listen on the stub control socket %s: %v", socketPath, err)
	}
	listenerDone := make(chan struct{})
	go func() {
		defer close(listenerDone)
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	// A cleanup, so a t.Fatalf below still closes the listener and JOINS the goroutine
	// before the binary moves on — an un-joined goroutine outliving the test is what
	// -race reports.
	t.Cleanup(func() {
		_ = ln.Close()
		<-listenerDone
	})

	// The document. Transcribed from renderMCPServersConfig rather than called: that
	// lives in package main. Mode 0600 — not secret, but an execution instruction
	// claude obeys, and a world-writable one in a shared temp directory is a footgun.
	runLocal := t.TempDir()
	brokenCommand := filepath.Join(runLocal, mcapBrokenCommand)
	cfgPath := filepath.Join(runLocal, "mcp-servers.json")
	cfgDoc := mcapConfigDocument(pyryBin, socketPath, brokenCommand)
	if err := os.WriteFile(cfgPath, []byte(cfgDoc), 0o600); err != nil {
		t.Fatalf("#2272: write the mcp-config document %s: %v", cfgPath, err)
	}
	// Belt and braces on the one input that must NOT resolve: t.TempDir is fresh, so
	// the path cannot exist — but a capture whose broken server turned out to be
	// runnable would be silently measuring nothing.
	if _, err := os.Stat(brokenCommand); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("#2272: the deliberately absent command %s resolves (stat err %v); the third server "+
			"would not be broken and the capture would measure two healthy servers", brokenCommand, err)
	}

	// A REAL nonce, never 0: newDropcapRedactor formats it with strconv.FormatInt, so a
	// zero installs "0" as a one-byte substitution rule and rewrites every zero digit in
	// the record.
	nonce := time.Now().UnixNano()
	// The empty slot is fifoPath, and the emptiness is a fact this probe records rather
	// than an omission: it holds no FIFO.
	red := newDropcapRedactor(home, artifactDir, workdir, "", mcapSessionID, nonce)
	for _, p := range []string{pyryBin, filepath.Dir(pyryBin), runLocal, cfgPath, socketPath, brokenCommand} {
		red.addPathClass(mcapClassRunLocal, "$RUN_LOCAL_TEMP", p)
	}
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2272 capture artifacts: %s", red.str(artifactDir))

	rec := &mcapRecord{
		Ticket:                mcapTicket,
		ClaudeVersion:         probeClaudeVersion(claudeBin),
		CapturedAt:            time.Now().Format(time.RFC3339),
		IsCapture:             true,
		Model:                 mcapModel,
		SpawnShapeDelta:       mcapSpawnShapeDelta,
		Workdir:               red.str(workdir),
		MCPConfigDocument:     red.str(cfgDoc),
		MCPConfigServers:      []string{mcapApproveServer, mcapFilesServer, mcapBrokenServer},
		BrokenServer:          mcapBrokenServer,
		Requests:              []mcapRequest{},
		Servers:               []mcapServer{},
		ServerKeyUnion:        []string{},
		Frames:                []mcapFrame{},
		RedactionRationale:    mcapRedactionRationale,
		CredentialScanApplied: scanner.applied(),
		CredentialScanSkipped: []string{},
		Limitations:           mcapLimitations,
	}
	rec.set(mcapInstrumentBroken, "did not reach a classification point")

	// BOTH BEFORE THE CLEANUP THAT READS THEM. mcapPersist fills the record from these
	// two, so they have to exist by the time it is registered — which is also why the
	// registration cannot move any earlier.
	//
	// probeSyncBuffer rather than a bytes.Buffer, and the mutex is load-bearing on one
	// specific branch. Cleanups run LIFO, so the run's cancel-and-join below normally
	// executes first and os/exec has joined its stderr copier before the fill reads the
	// buffer — but that join is bounded by mcapRunExitWait, and its timeout branch
	// reports and lets persist run with the copier possibly still appending.
	//
	// ONE buffer for the whole run, not one per spawn: streamsup.Config's Stderr reaches
	// cmd.Stderr at EVERY respawn, so a crash loop accumulates here as repetition, which
	// is itself the evidence.
	recorder := newDropcapRecorder()
	var stderrBuf probeSyncBuffer

	// Registered before anything below can fail, so a structural t.Fatalf still leaves
	// the evidence on disk — #1260's ordering.
	t.Cleanup(func() {
		mcapPersist(t, artifactDir, mcapFixturePath, recorder, &stderrBuf, red, scanner, rec)
	})

	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: mcapSessionID,
		Args:      mcapArgs(cfgPath),
		Stdout:    recorder,
		// Left nil before #2307, which discards it: a claude that refused the three-server
		// --mcp-config document wrote its complaint into a closed pipe and the record could
		// not say why the capture failed.
		Stderr: &stderrBuf,
		Logger: slog.New(argvHandler),
	})
	if err != nil {
		rec.set(mcapInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
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
		case <-time.After(mcapRunExitWait):
			t.Errorf("#2272: streamsup.Run did not return within %s of cancel", mcapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(mcapInstrumentBroken, "no live child within %s: claude never spawned, so nothing was "+
			"on the wire to capture", dropcapSpawnWait)
		return
	}
	rec.SpawnShape = red.strs(argv())

	// The control surface is live as soon as stdin is available. Do not wait for
	// system/init: this stream-json path emits it with a user turn, and this probe sends
	// no user turn by design.
	rec.Requests, err = mcapDriveRequests(stdin, recorder, red, mcapReplyBudget, mcapPoll)
	if err != nil {
		rec.set(mcapInstrumentBroken, "%v", red.str(err.Error()))
		return
	}
	rec.InitWait = mcapInitNotAwaited
	if raw, ok := mcapInitServers(recorder); ok {
		rec.InitSeen = true
		rec.InitWait = mcapInitSeen
		rec.InitMCPServers = json.RawMessage(red.redact(raw))
	}

	// The server extraction below reads rec.Frames, which is why the fill is called here
	// as well as from mcapPersist. Both call the same function and it assigns rather than
	// appends, so persist's later re-derivation over a fuller snapshot is a refresh, not
	// a double count.
	mcapFillCapture(t, rec, recorder, &stderrBuf, red)

	statusID := ""
	for _, r := range rec.Requests {
		if r.Subtype == mcapSubtypeStatus {
			statusID = r.RequestID
		}
	}
	union := map[string]bool{}
	for _, f := range rec.Frames {
		if f.RequestID != statusID || f.PayloadEncoding != dropcapEncodingJSONString {
			continue
		}
		entries, where, ok := mcapServersFrom([]byte(f.Payload))
		if !ok {
			continue
		}
		rec.StatusReplyLocation = where
		for _, e := range entries {
			server, serr := mcapServerShape(e, red)
			if serr != nil {
				rec.set(mcapInstrumentBroken, "decoding an mcpServers entry failed: %v",
					red.str(serr.Error()))
				return
			}
			rec.Servers = append(rec.Servers, server)
			for _, k := range server.Keys {
				union[k] = true
			}
		}
	}
	rec.ServerCount = len(rec.Servers)
	for k := range union {
		rec.ServerKeyUnion = append(rec.ServerKeyUnion, k)
	}
	sort.Strings(rec.ServerKeyUnion)

	if rec.ServerCount == 0 {
		rec.set(mcapDidNotFire, "the mcp_status request produced no server list at all; %s",
			mcapStatusVerdict(rec))
	} else {
		rec.set(mcapFired, "%d server(s) reported at %s, key union %v", rec.ServerCount,
			rec.StatusReplyLocation, rec.ServerKeyUnion)
	}

	// Counts and names only. The frames themselves are in the record the cleanup has
	// already written; putting claude's bytes in CI output is precisely the exposure
	// the deny-scan exists to prevent.
	if rec.ServerCount == 0 {
		t.Fatalf("#2272: the mcp_status request recorded ZERO servers out of %d captured line(s). A "+
			"capture recording none is vacuous, and committing it would hand #2275 a fixture that "+
			"proves nothing.\n  %s\n  line types: %v; undecoded: %d\n  requests: %s",
			rec.LinesCaptured, mcapStatusVerdict(rec), rec.LineTypeCensus, rec.UndecodedLines,
			mcapRequestSummary(rec))
	}
}

// mcapStatusVerdict names WHICH reading a zero-server run is.
//
// Only the last case is evidence about claude. The others are the rig failing to
// exercise the thing it meant to measure, and saying so plainly is what stops the
// next reader from loosening the reply search to fix a spawn bug.
func mcapStatusVerdict(rec *mcapRecord) string {
	var status *mcapRequest
	for i := range rec.Requests {
		if rec.Requests[i].Subtype == mcapSubtypeStatus {
			status = &rec.Requests[i]
		}
	}
	switch {
	case status == nil:
		return "the mcp_status request never went out, so the surface was never exercised. This is an " +
			"INSTRUMENT fault: read outcome_detail, not the reply search"
	case status.WriteError != "":
		return "the mcp_status request failed to reach the child's stdin (" + status.WriteError +
			"), so the surface was never exercised. This is an INSTRUMENT fault"
	case status.TerminatedOn == mcapTerminatedBudget:
		return "claude answered NOTHING carrying that request id within the budget. Either this claude " +
			"does not serve mcp_status on the stream-json input path, or it answers under an id shape " +
			"mcapResponseRequestID does not find — check init_mcp_servers and the frames for a reply " +
			"that arrived unpaired before concluding the verb is unsupported"
	default:
		return "claude ANSWERED the mcp_status request and the reply carried no mcpServers array at " +
			"any of the three placements mcapServersFrom searches. That is a finding about claude — " +
			"most likely a different payload shape than either declaration promises — and it is to be " +
			"routed back, not fixed by relaxing the search"
	}
}

// mcapRequestSummary is the one-line, content-free digest of the three verbs for a
// failure message: which went out, how each wait ended, how many lines answered it.
func mcapRequestSummary(rec *mcapRecord) string {
	parts := make([]string, 0, len(rec.Requests))
	for _, r := range rec.Requests {
		parts = append(parts, fmt.Sprintf("%s→%s(%d reply line(s), %.1fs)", r.Subtype, r.TerminatedOn,
			len(r.ReplyIndices), r.WaitSeconds))
	}
	if len(parts) == 0 {
		return "none sent"
	}
	return strings.Join(parts, " ")
}

// mcapArgs is production's DOWNGRADED arm, transcribed from permissionArgs (package
// main, so it cannot be called) with the model flag in front. buildArgs supplies the
// fixed --input-format/--output-format/--verbose prefix and the id flag.
//
// --strict-mcp-config is non-negotiable here and for a reason beyond production's:
// without it claude also loads the operator's user- and project-scoped servers, and
// this record would describe one machine's inventory instead of the rig's three
// servers. No --dangerously-skip-permissions: that is the arm that carries no
// document at all.
func mcapArgs(mcpConfigPath string) []string {
	return []string{
		"--model", mcapModel,
		"--permission-prompt-tool", mcapApproveToolRef,
		"--mcp-config", mcpConfigPath,
		"--strict-mcp-config",
		"--permission-mode", "default",
	}
}

// mcapConfigDocument renders the three-server --mcp-config document: the two
// production entries as renderMCPServersConfig writes them, plus one whose command
// does not exist.
//
// Marshalled through encoding/json rather than fmt-formatted, so a path containing a
// quote or a backslash produces a valid document rather than a syntax error claude
// would reject wholesale — which would break the capture in a way that looks like a
// finding about MCP.
func mcapConfigDocument(pyryBin, socketPath, brokenCommand string) string {
	type spec struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	doc := struct {
		MCPServers map[string]spec `json:"mcpServers"`
	}{MCPServers: map[string]spec{
		mcapApproveServer: {Command: pyryBin, Args: []string{"mcp-approve", "-pyry-socket", socketPath}},
		mcapFilesServer:   {Command: pyryBin, Args: []string{"mcp-files", "-pyry-socket", socketPath}},
		// Empty argv, so the ONLY thing wrong with this entry is that its command is not
		// there. A bad flag would confound "claude could not start it" with "it started and
		// refused".
		mcapBrokenServer: {Command: brokenCommand, Args: []string{}},
	}}
	b, err := json.Marshal(doc)
	if err != nil {
		// Unreachable: a fixed-shape struct of strings always marshals. Returning the error
		// text as the document makes the failure loud at claude's own parser rather than
		// silently writing an empty file.
		return fmt.Sprintf("mcp-config marshal failed: %v", err)
	}
	return string(b)
}

// mcapInitServers returns the first system/init line's mcp_servers value verbatim.
// Raw rather than decoded: whether this release spells it as an array of objects or
// as something else is part of what is being recorded, and a reader that decoded it
// into a declared shape would report its own expectation.
func mcapInitServers(recorder *dropcapRecorder) (json.RawMessage, bool) {
	lines, _ := recorder.snapshot()
	for _, c := range lines {
		if !c.Decoded || c.Type != "system" || c.Subtype != "init" {
			continue
		}
		var env struct {
			MCPServers json.RawMessage `json:"mcp_servers"`
		}
		if err := json.Unmarshal(c.Raw, &env); err != nil || len(env.MCPServers) == 0 {
			continue
		}
		return env.MCPServers, true
	}
	return nil, false
}

// mcapPersist is the thin *testing.T shell over mcapWriteRecord: the writer returns
// errors so AC 4 can hand it a hostile record and inspect the directory, and this
// turns those errors into the run's verdict.
//
// t.Errorf, NOT t.Fatalf, and that is not a softening. This runs from a t.Cleanup,
// and a Fatalf there exits the goroutine mid-cleanup, so every cleanup registered
// EARLIER — here, the stub listener's close-and-join — is skipped and leaks. There
// is nothing left to abort by the time this runs, so failing the test and returning
// is the whole of what a fatal would buy.
//
// IT FILLS BEFORE IT WRITES, which is #2307's first criterion. This is the one place
// every terminating path reaches — the cleanup is registered before anything below it
// can fail — so putting the fill here is a guarantee rather than a fill statement per
// return that the next return forgets. It also has to be here rather than inside
// mcapWriteRecord: mcapCollect needs a *testing.T, and the writer is deliberately
// *testing.T-free so it can be handed a hostile record and asked what it left on disk.
//
// The fill precedes the marshal, so stderr_capture goes through the FIRST marshal and
// the deny-scan sees it. A field that only reached the record after the scan would
// ship unscanned.
func mcapPersist(t *testing.T, dir, fixturePath string, recorder *dropcapRecorder, stderr *probeSyncBuffer,
	red *dropcapRedactor, scanner dropcapScanner, rec *mcapRecord) {
	t.Helper()
	mcapFillCapture(t, rec, recorder, stderr, red)
	recordPath, fixtureReason, err := mcapWriteRecord(dir, fixturePath, red, scanner, rec)
	if err != nil {
		t.Errorf("#2272: %v", red.str(err.Error()))
		return
	}
	t.Logf("#2272 outcome=%s init_seen=%v captured=%d servers=%d location=%s key_union=%v "+
		"scan_not_applied=%v\n  requests: %s\n  record: %s\n  %s",
		rec.Outcome, rec.InitSeen, rec.LinesCaptured, rec.ServerCount, rec.StatusReplyLocation,
		rec.ServerKeyUnion, rec.CredentialScanSkipped, mcapRequestSummary(rec), red.str(recordPath),
		red.str(rec.OutcomeDetail))
	if fixtureReason != "" {
		t.Logf("#2272: NOT promoted to %s — %s. The record above is the evidence; read it, then "+
			"re-run or route the finding back", fixturePath, fixtureReason)
		return
	}
	t.Logf("#2272: FIXTURE WRITTEN to %s (%d server(s), key union %v).\n"+
		"  COMMIT IT — `git add %s` — and in the SAME commit fill mcpStatusPinnedServerKeys in "+
		"internal/streamsup/mcp_status_capture_test.go with that union: that reader FATALS on a "+
		"present fixture with an empty pin, which is what stops the bytes landing unpinned.\n"+
		"  A pipeline worktree is DISCARDED when the run ends, so a fixture a test merely writes "+
		"does not survive (#1763, #2229). The record at the path above is written outside the "+
		"worktree and is what the bytes can be recovered from if this run was the gate's.",
		fixturePath, rec.ServerCount, rec.ServerKeyUnion, fixturePath)
}

// --- offline self-checks -----------------------------------------------------------
//
// Everything below runs with no claude, no credentials and no subprocess:
//
//	go test -tags e2e_realclaude -race -count=1 -run TestMcap ./internal/e2e/realclaude/
//
// None constructs a fixture-worthy record. Writer tests inject fixture paths under
// t.TempDir so offline coverage cannot alter the repository fixture.

// TestMcapControlLineCarriesEachVerbsOwnFields pins the three request shapes read
// out of the claude 2.1.259 binary's own bundled schema.
//
// The camelCase row is the one that matters. `serverName`, not `server_name`: the
// wrong spelling would produce an error reply, and a capture recording claude's
// complaint about a malformed request would look exactly like a capture recording
// claude's answer — same green run, same committed bytes, an entirely different
// measurement.
func TestMcapControlLineCarriesEachVerbsOwnFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		subtype   string
		extra     map[string]any
		wantInner map[string]any
	}{
		{
			name:      "mcp_status carries subtype and nothing else",
			subtype:   mcapSubtypeStatus,
			wantInner: map[string]any{"subtype": mcapSubtypeStatus},
		},
		{
			name:      "mcp_reconnect names the server, camelCase",
			subtype:   mcapSubtypeReconnect,
			extra:     map[string]any{"serverName": mcapApproveServer},
			wantInner: map[string]any{"subtype": mcapSubtypeReconnect, "serverName": mcapApproveServer},
		},
		{
			name:    "mcp_toggle names the server and the desired state",
			subtype: mcapSubtypeToggle,
			extra:   map[string]any{"serverName": mcapBrokenServer, "enabled": false},
			wantInner: map[string]any{"subtype": mcapSubtypeToggle, "serverName": mcapBrokenServer,
				"enabled": false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line, err := mcapControlLine(tc.subtype, "req_1", tc.extra)
			if err != nil {
				t.Fatalf("mcapControlLine() error: %v", err)
			}
			if got := strings.Count(string(line), "\n"); got != 1 || !strings.HasSuffix(string(line), "\n") {
				t.Errorf("the envelope is not exactly one physical line: %d newline(s) in %q", got, line)
			}
			var got struct {
				Type      string         `json:"type"`
				RequestID string         `json:"request_id"`
				Request   map[string]any `json:"request"`
			}
			if err := json.Unmarshal(line, &got); err != nil {
				t.Fatalf("the emitted line does not decode as JSON: %v", err)
			}
			if got.Type != "control_request" || got.RequestID != "req_1" {
				t.Errorf("envelope = {type:%q, request_id:%q}, want {control_request, req_1}",
					got.Type, got.RequestID)
			}
			if len(got.Request) != len(tc.wantInner) {
				t.Errorf("request carries %d field(s) %v, want %d %v", len(got.Request), got.Request,
					len(tc.wantInner), tc.wantInner)
			}
			for k, want := range tc.wantInner {
				if got.Request[k] != want {
					t.Errorf("request[%q] = %v, want %v", k, got.Request[k], want)
				}
			}
			// The snake_case spelling is what a reader who trusted sdk.d.ts's prose rather
			// than the schema would have written; nothing may emit it.
			if _, bad := got.Request["server_name"]; bad {
				t.Error("request carries server_name; the schema spells it serverName")
			}
		})
	}
}

type mcapTestWriter func([]byte) (int, error)

func (write mcapTestWriter) Write(p []byte) (int, error) { return write(p) }

// TestMcapDriveRequestsDoesNotWaitForInit is the offline guard on #2360's repaired
// send point. An empty recorder is the exact startup-silence state that used to hold
// the probe in its pre-request init wait until it returned without sending anything.
func TestMcapDriveRequestsDoesNotWaitForInit(t *testing.T) {
	t.Parallel()
	recorder := newDropcapRecorder()
	statusReplies := 0
	var sentLines []string
	write := mcapTestWriter(func(line []byte) (int, error) {
		sentLines = append(sentLines, string(line))
		var request mcapControlRequest
		if err := json.Unmarshal(line, &request); err != nil {
			return 0, err
		}
		subtype, _ := request.Request["subtype"].(string)
		response := map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": request.RequestID,
			},
		}
		if subtype == mcapSubtypeStatus {
			status := "pending"
			if statusReplies > 0 {
				status = "connected"
			}
			statusReplies++
			response["response"].(map[string]any)["response"] = map[string]any{
				"mcpServers": []map[string]any{
					{"name": mcapApproveServer, "status": status},
					{"name": mcapBrokenServer, "status": "failed", "error": "command not found"},
				},
			}
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return 0, err
		}
		if _, err := recorder.Write(append(encoded, '\n')); err != nil {
			return 0, err
		}
		return len(line), nil
	})
	requests, err := mcapDriveRequests(write, recorder,
		newDropcapRedactor("", "", "", "", "", mcapTestNonce), time.Second, 0)
	if err != nil {
		t.Fatalf("mcapDriveRequests() error: %v", err)
	}
	if len(requests) != 4 {
		t.Fatalf("mcapDriveRequests() returned %d requests, want 4", len(requests))
	}

	wantSubtypes := []string{
		mcapSubtypeStatus, mcapSubtypeStatus, mcapSubtypeReconnect, mcapSubtypeToggle,
	}
	seenIDs := map[string]bool{}
	for i, wantSubtype := range wantSubtypes {
		got := requests[i]
		if got.Subtype != wantSubtype {
			t.Errorf("request %d subtype = %q, want %q", i, got.Subtype, wantSubtype)
		}
		if got.RequestID == "" || seenIDs[got.RequestID] {
			t.Errorf("request %d id = %q, want a distinct non-empty id", i, got.RequestID)
		}
		seenIDs[got.RequestID] = true
		if got.Sent != sentLines[i] {
			t.Errorf("request %d sent = %q, want exact stdin line %q", i, got.Sent, sentLines[i])
		}
		if got.TerminatedOn != mcapTerminatedResponse || len(got.ReplyIndices) != 1 {
			t.Errorf("request %d outcome = %q/%v, want one correlated response",
				i, got.TerminatedOn, got.ReplyIndices)
		}
	}
	if !strings.Contains(requests[2].Sent, `"serverName":"`+mcapApproveServer+`"`) {
		t.Errorf("reconnect line = %q, want the ready test-owned server", requests[2].Sent)
	}
	if !strings.Contains(requests[3].Sent, `"serverName":"`+mcapBrokenServer+`"`) {
		t.Errorf("toggle line = %q, want the deliberately broken diagnostic server", requests[3].Sent)
	}
}

func TestMcapDriveRequestsRefusesReconnectUntilHealthy(t *testing.T) {
	t.Parallel()
	recorder := newDropcapRecorder()
	write := mcapTestWriter(func(line []byte) (int, error) {
		var request mcapControlRequest
		if err := json.Unmarshal(line, &request); err != nil {
			return 0, err
		}
		subtype, _ := request.Request["subtype"].(string)
		if subtype != mcapSubtypeStatus {
			t.Errorf("sent %s before %s reported connected", subtype, mcapApproveServer)
		}
		response := map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": request.RequestID,
				"response": map[string]any{
					"mcpServers": []map[string]any{
						{"name": mcapApproveServer, "status": "pending"},
						{"name": mcapBrokenServer, "status": "failed", "error": "command not found"},
					},
				},
			},
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return 0, err
		}
		if _, err := recorder.Write(append(encoded, '\n')); err != nil {
			return 0, err
		}
		return len(line), nil
	})

	requests, err := mcapDriveRequests(write, recorder,
		newDropcapRedactor("", "", "", "", "", mcapTestNonce), 5*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("mcapDriveRequests() reconnected before the healthy server reported connected")
	}
	if len(requests) == 0 {
		t.Fatal("mcapDriveRequests() retained no status request while waiting for readiness")
	}
	for _, request := range requests {
		if request.Subtype != mcapSubtypeStatus {
			t.Errorf("retained %s request before readiness, want only status diagnostics", request.Subtype)
		}
	}
}

// TestMcapControlLineRefusesToOverrideItsOwnSubtype guards the one way a caller
// could send a verb the record then mislabels: an `extra` map carrying `subtype`
// would win the merge silently, and the recorded request would name one verb while
// the wire carried another.
func TestMcapControlLineRefusesToOverrideItsOwnSubtype(t *testing.T) {
	t.Parallel()
	if _, err := mcapControlLine(mcapSubtypeStatus, "req_1",
		map[string]any{"subtype": mcapSubtypeToggle}); err == nil {
		t.Fatal("mcapControlLine() accepted a subtype override; the record would name one verb while " +
			"the wire carried another")
	}
}

// TestMcapResponseRequestIDReadsAllThreePlacements is the guard on reply
// correlation. The response.response row is the load-bearing one: it is where a real
// initialize reply was measured to put its payload, one level deeper than streamsup's
// control_response arm records for subtype/request_id. A reader stopping earlier
// would pair no reply with any request, and mcapAwait would then report every verb
// as unanswered — a rig bug that reads exactly like the finding "this claude does
// not serve these verbs".
func TestMcapResponseRequestIDReadsAllThreePlacements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want string
	}{
		{"top level", `{"type":"control_response","request_id":"req_1"}`, "req_1"},
		{"under response", `{"type":"control_response","response":{"request_id":"req_1"}}`, "req_1"},
		{
			name: "under response.response, where a measured reply puts its payload",
			line: `{"type":"control_response","response":{"subtype":"success","response":` +
				`{"request_id":"req_1"}}}`,
			want: "req_1",
		},
		{"an ordinary line carries none", `{"type":"system","subtype":"init"}`, ""},
		{"an undecodable line yields none rather than panicking", `{"type":`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mcapResponseRequestID([]byte(tc.line)); got != tc.want {
				t.Errorf("mcapResponseRequestID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMcapResponseSubtypeReadsAllThreePlacements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want string
	}{
		{"top level", `{"subtype":"success"}`, "success"},
		{"under response", `{"response":{"subtype":"error"}}`, "error"},
		{"under response.response", `{"response":{"response":{"subtype":"success"}}}`, "success"},
		{"absent", `{"type":"control_response"}`, ""},
		{"undecodable", `{"response":`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mcapResponseSubtype([]byte(tc.line)); got != tc.want {
				t.Errorf("mcapResponseSubtype() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMcapAwaitEndsOnAReplyOrOnItsBudget runs offline against a real
// dropcapRecorder fed by hand.
//
// The second row is the one that matters. No turn is sent by this probe, so no
// `result` line is ever produced and there is no turn boundary to wait on: the
// budget IS the ordinary exit for a verb claude declines to answer. A wait that
// could only end on a reply would hang the whole capture on the first unsupported
// verb and land no record at all.
func TestMcapAwaitEndsOnAReplyOrOnItsBudget(t *testing.T) {
	t.Parallel()
	const poll = 10 * time.Millisecond
	tests := []struct {
		name      string
		feed      string
		budget    time.Duration
		wantHits  int
		wantEnded string
	}{
		{
			name:      "a reply carrying the id ends the wait",
			feed:      "{\"type\":\"control_response\",\"response\":{\"request_id\":\"req_1\"}}\n",
			budget:    5 * time.Second,
			wantHits:  1,
			wantEnded: mcapTerminatedResponse,
		},
		{
			name: "a reply under another id does not",
			feed: "{\"type\":\"control_response\",\"response\":{\"request_id\":\"req_9\"}}\n" +
				"{\"type\":\"system\",\"subtype\":\"init\"}\n",
			budget:    200 * time.Millisecond,
			wantEnded: mcapTerminatedBudget,
		},
		{
			name:      "a verb claude never answers ends on the budget, not on a hang",
			feed:      "",
			budget:    200 * time.Millisecond,
			wantEnded: mcapTerminatedBudget,
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
			hits, ended := mcapAwait(recorder, "req_1", tc.budget, poll)
			if ended != tc.wantEnded {
				t.Errorf("mcapAwait() ended on %q, want %q", ended, tc.wantEnded)
			}
			if len(hits) != tc.wantHits {
				t.Errorf("mcapAwait() returned %d index(es) %v, want %d", len(hits), hits, tc.wantHits)
			}
		})
	}
}

// TestMcapServersFromReadsAllThreePlacements guards the reply search. An empty array
// must read as FOUND: "claude reported no servers" is a finding about claude and
// "claude sent no such key" is a finding about the payload shape, and collapsing
// them would send the next reader down the wrong diagnosis — mcapStatusVerdict's
// last two cases are exactly that distinction.
func TestMcapServersFromReadsAllThreePlacements(t *testing.T) {
	t.Parallel()
	const entry = `{"name":"pyry_approve","status":"connected"}`
	tests := []struct {
		name      string
		line      string
		wantCount int
		wantWhere string
		wantFound bool
	}{
		{"top level", `{"mcpServers":[` + entry + `]}`, 1, mcapLocTop, true},
		{"under response", `{"response":{"mcpServers":[` + entry + `]}}`, 1, mcapLocResponse, true},
		{
			name:      "under response.response, where a measured reply puts its payload",
			line:      `{"response":{"subtype":"success","response":{"mcpServers":[` + entry + `]}}}`,
			wantCount: 1, wantWhere: mcapLocNested, wantFound: true,
		},
		{
			name:      "an empty array is found, not absent",
			line:      `{"response":{"response":{"mcpServers":[]}}}`,
			wantCount: 0, wantWhere: mcapLocNested, wantFound: true,
		},
		{"a reply carrying no such key", `{"response":{"subtype":"success"}}`, 0, "", false},
		{"an explicit null is not a report", `{"response":{"mcpServers":null}}`, 0, "", false},
		{"an undecodable line reports absence", `{"response":`, 0, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entries, where, found := mcapServersFrom([]byte(tc.line))
			if found != tc.wantFound {
				t.Errorf("mcapServersFrom() found = %v, want %v", found, tc.wantFound)
			}
			if where != tc.wantWhere {
				t.Errorf("mcapServersFrom() where = %q, want %q", where, tc.wantWhere)
			}
			if len(entries) != tc.wantCount {
				t.Errorf("mcapServersFrom() returned %d entries, want %d", len(entries), tc.wantCount)
			}
		})
	}
}

// TestMcapServerShapeRecordsTheKeysClaudeSentAndTheirTypes is AC 3's offline proof.
//
// The entry below carries a key NEITHER declaration promises (`somethingNew`) and
// omits one both do (`scope`), which is the whole point: a shape derived by
// unmarshalling into a declared struct would report the author's expectation in both
// directions — silently dropping the unknown key and inventing the missing one.
func TestMcapServerShapeRecordsTheKeysClaudeSentAndTheirTypes(t *testing.T) {
	t.Parallel()
	red := newDropcapRedactor("", "", "", "", "", 1)
	const entry = `{"name":"pyry_probe_absent","status":"failed",` +
		`"error":"spawn ENOENT","config":{"command":"/nowhere/x","args":[],"env":{}},` +
		`"tools":[],"serverInfo":null,"retries":3,"enabled":true,"somethingNew":{"a":1}}`

	got, err := mcapServerShape(json.RawMessage(entry), red)
	if err != nil {
		t.Fatalf("mcapServerShape() error: %v", err)
	}
	wantKeys := []string{"config", "enabled", "error", "name", "retries", "serverInfo",
		"somethingNew", "status", "tools"}
	if strings.Join(got.Keys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("keys = %v, want %v (sorted, exactly what the entry carried)", got.Keys, wantKeys)
	}
	wantTypes := map[string]string{
		"name": mcapTypeString, "status": mcapTypeString, "error": mcapTypeString,
		"config": mcapTypeObject, "tools": mcapTypeArray, "serverInfo": mcapTypeNull,
		"retries": mcapTypeNumber, "enabled": mcapTypeBool, "somethingNew": mcapTypeObject,
	}
	for k, want := range wantTypes {
		if got.KeyTypes[k] != want {
			t.Errorf("key_types[%q] = %q, want %q", k, got.KeyTypes[k], want)
		}
	}
	if got.Name != mcapBrokenServer || got.Status != "failed" {
		t.Errorf("name/status = %q/%q, want %q/failed", got.Name, got.Status, mcapBrokenServer)
	}
	// AC 2: the error text as claude sent it. A summarised or dropped one would leave
	// the failure path recorded as a status with no cause.
	if got.Error != "spawn ENOENT" {
		t.Errorf("error = %q, want the text claude sent", got.Error)
	}
	if len(got.Config) == 0 {
		t.Error("config was dropped; it is the surface #2275 has to decode and AC 4 has to screen")
	}
}

// TestMcapServerShapeSurvivesAValueTypeItDidNotExpect: a `status` that is not a
// string must not silently become an empty status while the key vanishes. The key
// set and key_types are the record's ground truth, and they must still describe the
// entry even where the convenience field cannot be filled.
func TestMcapServerShapeSurvivesAValueTypeItDidNotExpect(t *testing.T) {
	t.Parallel()
	red := newDropcapRedactor("", "", "", "", "", 1)
	got, err := mcapServerShape(json.RawMessage(`{"name":"x","status":{"code":7}}`), red)
	if err != nil {
		t.Fatalf("mcapServerShape() error: %v", err)
	}
	if got.Status != "" {
		t.Errorf("status = %q, want empty; a non-string value must not be coerced", got.Status)
	}
	if got.KeyTypes["status"] != mcapTypeObject || len(got.Keys) != 2 {
		t.Errorf("keys %v / key_types %v lost the status key that the entry carried",
			got.Keys, got.KeyTypes)
	}
}

// TestMcapWriteRecordRefusesACredentialShapedConfigAndLeavesNoFile is AC 4.
//
// The two rows differ in ONE byte sequence: whether the recorded server config
// carries an `sk-ant-` value. Without the control row a writer that refused
// unconditionally would pass this test while landing no capture ever; without the
// credential row the scan could be entirely absent and the write still succeed. The
// directory listing is the assertion rather than the returned error, because
// "wrote it and then removed it" is a different and much worse design that a
// non-nil error alone cannot rule out.
func TestMcapWriteRecordRefusesACredentialShapedConfigAndLeavesNoFile(t *testing.T) {
	t.Parallel()
	// NEVER newDropcapScanner here: it reads os.Getenv twice and realHome, so a table
	// built through it is green or red depending on whose machine runs it. The fixed
	// half of the net is version-independent and carries the sk-ant- literal this test
	// is about.
	scanner := dropcapScanner{needles: dropcapFixedNeedles()}
	record := func(config string) *mcapRecord {
		return &mcapRecord{
			Ticket:        mcapTicket,
			IsCapture:     true,
			ClaudeVersion: mcapFixtureVersion + " (Claude Code)",
			// Deliberately NOT fired, so no row here can promote a fixture as a side effect.
			Outcome:       mcapInstrumentBroken,
			OutcomeDetail: "offline writer table",
			Servers: []mcapServer{{
				Name: mcapBrokenServer, Status: "failed",
				Keys: []string{"config", "name", "status"},
				KeyTypes: map[string]string{"config": mcapTypeObject, "name": mcapTypeString,
					"status": mcapTypeString},
				Config: json.RawMessage(config),
			}},
		}
	}
	tests := []struct {
		name      string
		config    string
		wantWrite bool
	}{
		{
			name:      "a config with no credential in it is written",
			config:    `{"command":"pyry","args":["mcp-approve"],"env":{}}`,
			wantWrite: true,
		},
		{
			name:   "a config carrying an api key fails the write",
			config: `{"command":"pyry","args":[],"env":{"ANTHROPIC_API_KEY":"sk-ant-api03-DEADBEEF"}}`,
		},
		{
			name:   "the credential hides one level deeper and is still caught",
			config: `{"command":"pyry","args":["--token","sk-ant-oat01-DEADBEEF"],"env":{}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			red := newDropcapRedactor("", "", "", "", "", 1)
			path, reason, err := mcapWriteRecord(dir, filepath.Join(dir, "fixture.json"), red, scanner,
				record(tc.config))

			entries, rerr := os.ReadDir(dir)
			if rerr != nil {
				t.Fatalf("reading the target directory back: %v", rerr)
			}
			if !tc.wantWrite {
				if err == nil {
					t.Fatal("mcapWriteRecord() accepted a record whose server config carries a " +
						"credential-shaped value")
				}
				if len(entries) != 0 {
					t.Errorf("the refusal left %d file(s) in the target directory; the scan must run "+
						"AHEAD of every filesystem call so nothing is half-written", len(entries))
				}
				// The message names the class, never the value — an error quoting the leak is
				// the own-goal the scan exists to prevent.
				if strings.Contains(err.Error(), "sk-ant-") {
					t.Error("the refusal message quotes the matched value")
				}
				return
			}
			if err != nil {
				t.Fatalf("mcapWriteRecord() refused a clean record: %v", err)
			}
			if len(entries) != 1 || path == "" {
				t.Fatalf("a clean record wrote %d file(s), path %q; want exactly one", len(entries), path)
			}
			if reason == "" {
				t.Error("a record that is not fired was promoted to the fixture; only a fired capture " +
					"at the pinned version may become the committed proof")
			}
		})
	}
}

// TestMcapFixtureWorthyRefusesEveryBadCapture runs offline. fixtureWorthy is the only
// thing standing between a live run and a committed fixture, and each row below is a
// capture that looks green from outside — the record is written, the deny-scan
// passed, the log is cheerful — while proving nothing, or proving something about a
// different claude.
//
// The missing-broken-server row is this ticket's own: a reply listing only the two
// healthy servers still has keys to pin and is still missing the entire failure path,
// which is the half no capture in this tree already has.
func TestMcapFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	good := func() *mcapRecord {
		return &mcapRecord{
			Outcome:       mcapFired,
			ClaudeVersion: mcapFixtureVersion + " (Claude Code)",
			ServerCount:   2,
			Servers: []mcapServer{
				{Name: mcapApproveServer, Status: "connected", Keys: []string{"name", "status"}},
				{Name: mcapBrokenServer, Status: "failed", Error: "command not found",
					Keys: []string{"error", "name", "status"}},
			},
			Requests: []mcapRequest{
				{Subtype: mcapSubtypeStatus, RequestID: "req_mcp_status_1",
					ReplyIndices: []int{1}, TerminatedOn: mcapTerminatedResponse},
				{Subtype: mcapSubtypeReconnect, RequestID: "req_mcp_reconnect_2",
					ReplyIndices: []int{2}, TerminatedOn: mcapTerminatedResponse},
				{Subtype: mcapSubtypeToggle, RequestID: "req_mcp_toggle_3",
					ReplyIndices: []int{3}, TerminatedOn: mcapTerminatedResponse},
			},
			Frames: []mcapFrame{
				{Index: 0, Type: "system", Subtype: "init", PayloadEncoding: dropcapEncodingJSONString},
				{Index: 1, Type: "control_response", Subtype: "success", RequestID: "req_mcp_status_1",
					PayloadEncoding: dropcapEncodingJSONString},
				{Index: 2, Type: "control_response", Subtype: "success", RequestID: "req_mcp_reconnect_2",
					PayloadEncoding: dropcapEncodingJSONString},
				{Index: 3, Type: "control_response", Subtype: "success", RequestID: "req_mcp_toggle_3",
					PayloadEncoding: dropcapEncodingJSONString},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*mcapRecord)
		want   bool
	}{
		{"a good capture is promoted", func(*mcapRecord) {}, true},
		{"bare version string, no suffix", func(r *mcapRecord) { r.ClaudeVersion = mcapFixtureVersion }, true},
		{
			// A non-UTF-8 line that is not a reply costs the reader nothing, so it must not
			// block a fixture that is otherwise good.
			"a base64 non-reply frame is not a reason to refuse",
			func(r *mcapRecord) { r.Frames[0].PayloadEncoding = dropcapEncodingBase64 },
			true,
		},
		{"never fired", func(r *mcapRecord) { r.Outcome = mcapDidNotFire }, false},
		{"instrument broken", func(r *mcapRecord) { r.Outcome = mcapInstrumentBroken }, false},
		{"vacuous: zero servers", func(r *mcapRecord) { r.ServerCount = 0 }, false},
		{"a different claude release", func(r *mcapRecord) { r.ClaudeVersion = "2.1.260 (Claude Code)" }, false},
		{"version unreadable", func(r *mcapRecord) { r.ClaudeVersion = "<unavailable: exec failed>" }, false},
		{"version absent", func(r *mcapRecord) { r.ClaudeVersion = "" }, false},
		{
			"a server recorded with no keys at all",
			func(r *mcapRecord) { r.Servers[1].Keys = nil },
			false,
		},
		{
			"the deliberately broken server never appeared in the reply",
			func(r *mcapRecord) { r.Servers = r.Servers[:1] },
			false,
		},
		{
			"the broken server has no observed status",
			func(r *mcapRecord) { r.Servers[1].Status = "" },
			false,
		},
		{
			"the broken server has no observed error",
			func(r *mcapRecord) { r.Servers[1].Error = "" },
			false,
		},
		{
			"one of the three verbs was never sent",
			func(r *mcapRecord) { r.Requests = r.Requests[:2] },
			false,
		},
		{
			"two verbs reused one request id",
			func(r *mcapRecord) { r.Requests[2].RequestID = r.Requests[1].RequestID },
			false,
		},
		{
			"a request write failed",
			func(r *mcapRecord) { r.Requests[1].WriteError = "broken pipe" },
			false,
		},
		{
			"a request exhausted its reply budget",
			func(r *mcapRecord) { r.Requests[1].TerminatedOn = mcapTerminatedBudget },
			false,
		},
		{
			"a request recorded no correlated reply",
			func(r *mcapRecord) { r.Requests[1].ReplyIndices = nil },
			false,
		},
		{
			"a reply index names a frame carrying another request id",
			func(r *mcapRecord) { r.Requests[1].ReplyIndices = []int{3} },
			false,
		},
		{
			"an unsupported-command error reply is diagnostic only",
			func(r *mcapRecord) { r.Frames[2].Subtype = "error" },
			false,
		},
		{
			// The one shape this side would otherwise promote and the reading side refuses:
			// the streamsup reader fatals on any encoding but json-string.
			"a base64 reply frame the reader cannot read",
			func(r *mcapRecord) { r.Frames[1].PayloadEncoding = dropcapEncodingBase64 },
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

// TestMcapConfigDocumentRegistersThreeServersOneOfWhichCannotRun pins AC 2's
// document: the two production entries claude is meant to see, plus one whose
// command does not exist and whose argv is empty — so the ONLY thing wrong with it
// is the missing binary. A bad flag instead would confound "claude could not start
// it" with "it started and refused", and the recorded error text is the measurement.
func TestMcapConfigDocumentRegistersThreeServersOneOfWhichCannotRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	broken := filepath.Join(dir, mcapBrokenCommand)
	doc := mcapConfigDocument("/opt/pyry", "/tmp/s.sock", broken)

	var got struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("the document does not decode as JSON (claude would reject it wholesale): %v", err)
	}
	if len(got.MCPServers) != 3 {
		t.Fatalf("the document registers %d server(s) %v, want 3", len(got.MCPServers), got.MCPServers)
	}
	for _, name := range []string{mcapApproveServer, mcapFilesServer, mcapBrokenServer} {
		if _, ok := got.MCPServers[name]; !ok {
			t.Errorf("the document does not register %q", name)
		}
	}
	if cmd := got.MCPServers[mcapApproveServer].Command; cmd != "/opt/pyry" {
		t.Errorf("%s command = %q, want the built pyry binary", mcapApproveServer, cmd)
	}
	if args := got.MCPServers[mcapFilesServer].Args; len(args) != 3 || args[0] != "mcp-files" {
		t.Errorf("%s args = %v, want the mcp-files client-flag pair", mcapFilesServer, args)
	}
	entry := got.MCPServers[mcapBrokenServer]
	if entry.Command != broken || len(entry.Args) != 0 {
		t.Errorf("%s = {command:%q, args:%v}, want the absent command and an empty argv",
			mcapBrokenServer, entry.Command, entry.Args)
	}
	if _, err := os.Stat(entry.Command); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the deliberately absent command resolves (stat err %v); the third server would "+
			"not be broken", err)
	}
}

// TestMcapArgsIsProductionsDowngradedArm pins the spawn shape. The bypass row is the
// one that would silently invalidate the whole capture: --dangerously-skip-permissions
// is the arm that carries NO --mcp-config at all, so a child launched with it would
// load the operator's own servers and report a different machine's inventory under
// this ticket's name.
func TestMcapArgsIsProductionsDowngradedArm(t *testing.T) {
	t.Parallel()
	args := mcapArgs("/tmp/mcp.json")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--permission-prompt-tool", mcapApproveToolRef, "--mcp-config",
		"/tmp/mcp.json", "--strict-mcp-config", "--permission-mode", "default", "--model", mcapModel} {
		if !strings.Contains(joined, want) {
			t.Errorf("mcapArgs() is missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Errorf("mcapArgs() carries the bypass flag, which is the arm that loads no document: %v", args)
	}
}

// TestMcapStatusVerdictSeparatesRigFailureFromFinding: the verdict string is what a
// reader of a failed live gate acts on, and only its last case is evidence about
// claude. Confusing an instrument fault with a finding is how a spawn bug gets
// "fixed" by relaxing the reply search until something matches.
func TestMcapStatusVerdictSeparatesRigFailureFromFinding(t *testing.T) {
	t.Parallel()
	status := func(ended, writeErr string) []mcapRequest {
		return []mcapRequest{{Subtype: mcapSubtypeStatus, RequestID: "req_1", TerminatedOn: ended,
			WriteError: writeErr}}
	}
	tests := []struct {
		name        string
		rec         mcapRecord
		wantFinding bool
		wantText    string
	}{
		{name: "the request never went out during startup silence", rec: mcapRecord{InitSeen: false},
			wantText: "request never went out"},
		{
			name:     "the request failed to reach stdin",
			rec:      mcapRecord{InitSeen: false, Requests: status(mcapTerminatedBudget, "broken pipe")},
			wantText: "failed to reach",
		},
		{
			name:     "claude answered nothing under that id despite startup silence",
			rec:      mcapRecord{InitSeen: false, Requests: status(mcapTerminatedBudget, "")},
			wantText: "does not serve",
		},
		{
			name:        "claude answered without init and the reply carried no server list",
			rec:         mcapRecord{InitSeen: false, Requests: status(mcapTerminatedResponse, "")},
			wantFinding: true,
			wantText:    "finding about claude",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mcapStatusVerdict(&tc.rec)
			if got == "" {
				t.Fatal("mcapStatusVerdict() is empty; the zero-server fatal would name no cause")
			}
			isFinding := strings.Contains(got, "finding about claude")
			if isFinding != tc.wantFinding {
				t.Errorf("verdict reads as a finding about claude = %v, want %v\n  got: %s",
					isFinding, tc.wantFinding, got)
			}
			if !strings.Contains(got, tc.wantText) {
				t.Errorf("verdict = %q, want it to contain %q", got, tc.wantText)
			}
			if !tc.wantFinding && !strings.Contains(got, "INSTRUMENT") &&
				!strings.Contains(got, "does not serve") {
				t.Errorf("a rig failure or an unsupported verb must say which; got: %s", got)
			}
		})
	}
}

// mcapTestNonce is a multi-digit stand-in for the live run's timestamp.
//
// NOT 1, which the older offline tables pass. newDropcapRedactor installs the nonce
// as a plain substring rule, so a one-byte nonce rewrites every matching digit
// anywhere in the record — harmless where nothing is asserted on digits, and a
// silent corrupter of the byte-exact cap assertions below.
const mcapTestNonce = 987654321

// TestMcapFillCaptureDescribesWhatTheRecorderHeld is AC 1's offline proof.
//
// EACH ROW ASSERTS THE BEFORE SIDE AS WELL AS THE AFTER SIDE. The bug this ticket
// fixes is a record carrying the zero values it was constructed with, so a test that
// only checked the filled values would pass just as happily against a record that
// was already full — and would prove nothing about the fill having run.
//
// The empty-recorder row carries the distinction the seven broken records could not
// make. mcapCensus returns a NON-NIL empty map and mcapCollect a NON-NIL empty slice,
// and neither field is tagged omitempty, so `{}` and `[]` in the written bytes say
// the pass ran and observed nothing while `null` says it never ran at all. That is
// exactly the reading no record in this family has ever supported.
func TestMcapFillCaptureDescribesWhatTheRecorderHeld(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		feed         string
		stderr       string
		wantLines    int
		wantCensus   map[string]int
		wantUndecode int
		wantBlank    int
	}{
		{
			name: "a mixed stream: decodable, undecodable and blank lines",
			feed: "{\"type\":\"system\",\"subtype\":\"init\"}\n" +
				"{\"type\":\"control_response\",\"response\":{\"request_id\":\"req_1\"}}\n" +
				"this is not json\n" +
				"\n",
			stderr:       "claude: failed to start MCP server pyry_probe_absent",
			wantLines:    3,
			wantCensus:   map[string]int{"system/init": 1, "control_response": 1},
			wantUndecode: 1,
			wantBlank:    1,
		},
		{
			name:       "an empty recorder still reports that the pass RAN",
			wantCensus: map[string]int{},
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
			var stderr probeSyncBuffer
			if tc.stderr != "" {
				if _, err := stderr.Write([]byte(tc.stderr)); err != nil {
					t.Fatalf("feeding the stderr buffer: %v", err)
				}
			}
			rec := &mcapRecord{}

			// The before side. These are the values every record in the failing family
			// carried, and asserting them here is what makes the after side evidence.
			if rec.LinesCaptured != 0 || rec.UndecodedLines != 0 || rec.StderrCapture != "" {
				t.Fatalf("the fresh record is not at its zero values (%d line(s), %d undecoded, "+
					"stderr %d byte(s)); the after side would prove nothing",
					rec.LinesCaptured, rec.UndecodedLines, len(rec.StderrCapture))
			}
			if rec.LineTypeCensus != nil || rec.Frames != nil {
				t.Fatal("the fresh record already carries a census or frames; `null` is what says the " +
					"pass never ran and this row could not tell that from `{}`")
			}

			mcapFillCapture(t, rec, recorder, &stderr, newDropcapRedactor("", "", "", "", "",
				mcapTestNonce))

			if rec.LinesCaptured != tc.wantLines {
				t.Errorf("lines_captured = %d, want %d", rec.LinesCaptured, tc.wantLines)
			}
			if len(rec.Frames) != tc.wantLines {
				t.Errorf("frames holds %d entry/entries, want %d — one per captured line, so the "+
					"record cannot disagree with its own line count", len(rec.Frames), tc.wantLines)
			}
			if rec.UndecodedLines != tc.wantUndecode {
				t.Errorf("undecoded_lines = %d, want %d", rec.UndecodedLines, tc.wantUndecode)
			}
			if rec.BlankLines != tc.wantBlank {
				t.Errorf("blank_lines = %d, want %d", rec.BlankLines, tc.wantBlank)
			}
			if rec.StderrCapture != tc.stderr {
				t.Errorf("stderr_capture = %q, want %q", rec.StderrCapture, tc.stderr)
			}
			// Non-nil even when empty: `{}` and `[]` say the pass ran, `null` says it did not.
			if rec.LineTypeCensus == nil || rec.Frames == nil {
				t.Fatalf("the fill left census %v / frames %v as nil, so the written record would "+
					"read `null` and be indistinguishable from one that never reached the fill",
					rec.LineTypeCensus, rec.Frames)
			}
			if len(rec.LineTypeCensus) != len(tc.wantCensus) {
				t.Errorf("line_type_census = %v, want %v", rec.LineTypeCensus, tc.wantCensus)
			}
			for k, want := range tc.wantCensus {
				if rec.LineTypeCensus[k] != want {
					t.Errorf("line_type_census[%q] = %d, want %d", k, rec.LineTypeCensus[k], want)
				}
			}
		})
	}
}

// TestMcapStderrIsRedactedBeforeItIsCapped pins the ORDER of the two passes, which is
// a security property rather than a tidiness one.
//
// The over_cap_path row places a run-local path so that the cap falls INSIDE it, past
// the "/var/folders/" prefix. Capping first leaves a fragment the substitution table
// no longer matches, so the redaction silently does not fire — while that fragment
// still carries one of dropcapFixedNeedles' fixed deny literals, which refuses the
// whole write. The record would then be lost to a truncation artifact rather than to
// anything the capture observed. Redacting first replaces the path whole, and the cap
// can afterwards cut nothing worse than the rig's own placeholder in half.
//
// The placeholder assertion is what keeps the row non-vacuous: without it an
// implementation that dropped the field entirely would pass every absence check here.
func TestMcapStderrIsRedactedBeforeItIsCapped(t *testing.T) {
	t.Parallel()
	// A fixed fake rather than a real temp path, so the row measures the ordering on
	// every machine instead of whatever $TMPDIR happens to be. Its shape is a real
	// one: "/var/folders/" is a fixed deny needle.
	const runLocal = "/var/folders/zz/pyry-2307-fixed/T/pyry-bin"
	// The cut lands 20 bytes into the path — past "/var/folders/" (13 bytes), so a
	// cap-first fragment still trips the deny needle.
	const splitAt = 20

	tests := []struct {
		name       string
		stderr     string
		wantLen    int
		wantSubstr string
	}{
		{
			name:       "a short capture is neither cut nor left holding the path",
			stderr:     "spawn " + runLocal + ": no such file or directory",
			wantLen:    len("spawn $RUN_LOCAL_TEMP: no such file or directory"),
			wantSubstr: "$RUN_LOCAL_TEMP",
		},
		{
			name:       "the cap falls inside the path, and the path is already gone by then",
			stderr:     strings.Repeat("e", stderrFixtureCap-splitAt) + runLocal + " boom",
			wantSubstr: "$RUN_LOCAL_TEMP",
		},
		{
			name:    "an over-cap capture with nothing to redact is still bounded",
			stderr:  strings.Repeat("q", stderrFixtureCap+4096),
			wantLen: stderrFixtureCap,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			red := newDropcapRedactor("", "", "", "", "", mcapTestNonce)
			red.addPathClass(mcapClassRunLocal, "$RUN_LOCAL_TEMP", runLocal)

			var stderr probeSyncBuffer
			if _, err := stderr.Write([]byte(tc.stderr)); err != nil {
				t.Fatalf("feeding the stderr buffer: %v", err)
			}
			rec := &mcapRecord{}
			mcapFillCapture(t, rec, newDropcapRecorder(), &stderr, red)
			got := rec.StderrCapture

			if len(got) > stderrFixtureCap {
				t.Errorf("stderr_capture is %d bytes, over the %d cap; an unbounded free-text field "+
					"is how a capture stops being reviewable", len(got), stderrFixtureCap)
			}
			if tc.wantLen > 0 && len(got) != tc.wantLen {
				t.Errorf("stderr_capture is %d bytes, want exactly %d", len(got), tc.wantLen)
			}
			if tc.wantSubstr != "" && !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("stderr_capture does not carry %q, so the substitution never fired and "+
					"every absence check below is vacuous", tc.wantSubstr)
			}
			if strings.Contains(got, runLocal) {
				t.Error("stderr_capture carries the run-local path verbatim")
			}
			// The class name only, never the matched value: this is the fixed deny literal a
			// cap-first implementation would leave behind as a fragment.
			if strings.Contains(got, "/var/folders/") {
				t.Error("stderr_capture still carries a fixed deny-scan literal, so the capture was " +
					"cut before it was redacted and the whole record would be refused")
			}
		})
	}
}

// TestMcapPersistFillsARecordThatNeverReachedTheHappyPath is AC 1's end-to-end
// offline proof, and the one that fails if the fill is moved back off the path
// everything goes through.
//
// The record below is shaped like a non-fixture-worthy live diagnostic: outcome
// instrument-broken with every capture field untouched before persistence. The
// assertions are on the bytes READ BACK FROM DISK rather than on the in-memory
// record, because the file is the artifact an operator diagnoses from and a record
// filled in memory after the marshal would be no use at all.
//
// Not fixture-worthy by construction — the outcome is not `fired` — so no row here
// can promote a fixture as a side effect.
func TestMcapPersistFillsARecordThatNeverReachedTheHappyPath(t *testing.T) {
	t.Parallel()
	for _, fixtureExists := range []bool{false, true} {
		fixtureExists := fixtureExists
		t.Run(fmt.Sprintf("fixture_exists=%v", fixtureExists), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fixturePath := filepath.Join(dir, "fixture.json")
			const sentinel = "{\"valid_existing_fixture\":true}\n"
			if fixtureExists {
				if err := os.WriteFile(fixturePath, []byte(sentinel), 0o600); err != nil {
					t.Fatalf("seeding fixture: %v", err)
				}
			}

			recorder := newDropcapRecorder()
			if _, err := recorder.Write([]byte(
				"{\"type\":\"system\",\"subtype\":\"init\"}\n" +
					"{\"type\":\"stream_event\"}\n" +
					"still not json\n")); err != nil {
				t.Fatalf("feeding the recorder: %v", err)
			}
			var stderr probeSyncBuffer
			const complaint = "claude: MCP server pyry_probe_absent exited with code 127"
			if _, err := stderr.Write([]byte(complaint)); err != nil {
				t.Fatalf("feeding the stderr buffer: %v", err)
			}

			rec := &mcapRecord{Ticket: mcapTicket, IsCapture: true, Model: mcapModel}
			rec.set(mcapInstrumentBroken, "requests did not produce a fixture-worthy response")
			rec.InitWait = mcapInitNotAwaited

			// NEVER newDropcapScanner: it reads os.Getenv twice and realHome, so a record built
			// through it is green or red depending on whose machine runs it.
			mcapPersist(t, dir, fixturePath, recorder, &stderr,
				newDropcapRedactor("", "", "", "", "", mcapTestNonce),
				dropcapScanner{needles: dropcapFixedNeedles()}, rec)

			blob, err := os.ReadFile(filepath.Join(dir, mcapRecordName))
			if err != nil {
				t.Fatalf("the record was not written at all: %v", err)
			}
			var got struct {
				Outcome        string         `json:"outcome"`
				InitWait       string         `json:"init_wait"`
				LinesCaptured  int            `json:"lines_captured"`
				UndecodedLines int            `json:"undecoded_lines"`
				LineTypeCensus map[string]int `json:"line_type_census"`
				Frames         []struct {
					Index int    `json:"index"`
					Type  string `json:"type"`
				} `json:"frames"`
				StderrCapture string `json:"stderr_capture"`
			}
			if err := json.Unmarshal(blob, &got); err != nil {
				t.Fatalf("the written record does not decode: %v", err)
			}

			if got.Outcome != mcapInstrumentBroken || got.InitWait != mcapInitNotAwaited {
				t.Errorf("outcome/init_wait = %q/%q, want %q/%q; the fill must not overwrite the verdict "+
					"the terminating path already reached", got.Outcome, got.InitWait, mcapInstrumentBroken,
					mcapInitNotAwaited)
			}
			if got.LinesCaptured != 3 {
				t.Errorf("lines_captured = %d, want 3 — a non-worthy path still has to say what the "+
					"recorder held", got.LinesCaptured)
			}
			if got.UndecodedLines != 1 {
				t.Errorf("undecoded_lines = %d, want 1", got.UndecodedLines)
			}
			if got.LineTypeCensus == nil {
				t.Error("line_type_census is null, which is the zero value the failing records carried; a " +
					"census that RAN and observed nothing reads `{}`")
			}
			if got.LineTypeCensus["system/init"] != 1 || got.LineTypeCensus["stream_event"] != 1 {
				t.Errorf("line_type_census = %v, want one system/init and one stream_event",
					got.LineTypeCensus)
			}
			if len(got.Frames) != 3 {
				t.Fatalf("frames holds %d entry/entries, want 3", len(got.Frames))
			}
			if got.StderrCapture != complaint {
				t.Errorf("stderr_capture = %q, want the child's complaint; a claude that refused the "+
					"--mcp-config document says so ONLY here", got.StderrCapture)
			}
			fixtureBlob, err := os.ReadFile(fixturePath)
			if fixtureExists {
				if err != nil {
					t.Fatalf("existing fixture was removed: %v", err)
				}
				if string(fixtureBlob) != sentinel {
					t.Errorf("existing fixture was overwritten: got %q, want %q", fixtureBlob, sentinel)
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("non-worthy record created a fixture or returned the wrong error: bytes=%q err=%v",
					fixtureBlob, err)
			}
		})
	}
}
