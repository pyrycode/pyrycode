//go:build e2e_realclaude

package realclaude

// TestRealClaudeMCPStatusAndReconnect is #2278: one inbound mcp_status_request and
// one inbound mcp_reconnect, driven from a paired phone through the daemon's own
// path to a live claude, asserting that the reconnect REACHED claude and changed
// what claude reports.
//
// # Why a live run when every leg is already covered
//
// Every MCP slice below this one is proven against the fake daemon or against
// #2272's committed capture. A fake answers whatever it is written to answer, and a
// replay is the same bytes whichever request produced them. Neither can show that a
// reconnect the daemon relays actually reaches claude. This is the run that
// separates a working actuator from one whose reply the daemon fabricated, and the
// load-bearing assertion is the CHANGED ROW: a frame restated from anything the
// daemon held would still report the broken server as failed.
//
// # Where the broken server comes from
//
// renderMCPServersConfig emits exactly pyry_approve and pyry_files, permissionArgs
// passes --strict-mcp-config (so no project or user .mcp.json can add a third), and
// the document is daemon-global and written once at startup. The shipped daemon has
// no injection point and this gate must not grow one — a test-only seam on the
// permission bridge's spawn path would be a production change made for a test's
// benefit. The route that needs no production change is the one
// startPermissionObserver already uses: this test binary is handed to the daemon as
// -pyry-claude, and when the daemon spawns it, runMCPConfigShim rewrites the
// --mcp-config document the daemon wrote and then BECOMES the real claude.
//
// # What it does NOT drive
//
// No per-device authorization arm. mcpActuatorV2.actuate gates on
// MayAnswerRemotePermission ahead of every seam and touches no child, so a live
// claude behind that gate cannot change its behaviour, and
// TestRelayV2_MCPActuationGatedAuditedAndAnsweredFresh already drives it with two
// real phones against one real daemon. #1987 declined the same arm for the same
// reason. No mcp_toggle: #2272's capture covers the verb and this ticket's ACs ask
// for status and reconnect.
//
// # Running it
//
//	go test -tags e2e_realclaude -count=1 -v \
//	  -run '^TestRealClaudeMCPStatusAndReconnect$' ./internal/e2e/realclaude/
//
// It costs one live claude turn and needs real credentials; a skip without them is
// the correct outcome and carries no signal. READ THE COUNT OF TESTS EXECUTED,
// NEVER THE EXIT CODE: with no credentials every test skips and exits 0, and when
// the package fails to build zero tests run and it still exits 0 through a shell
// wrapper. `make check` never compiles this package. Count the `=== RUN` lines.
//
// This slice captures nothing, so nothing needs `git add`ing beyond this file.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The environment contract between the test process and the shim it re-execs
// itself as. All three must be set for the shim to touch anything: the rewrite is
// inert unless a real binary, a command path and the enable flag are all present,
// so the shim cannot rewrite a document belonging to a daemon nobody meant to
// instrument.
const (
	mcpShimEnabled = "PYRY_MCP_SHIM"
	mcpShimRealBin = "PYRY_MCP_SHIM_REAL"
	mcpShimCommand = "PYRY_MCP_SHIM_COMMAND"
)

// The two production server names are those constants' own spellings (mcpServerName
// and mcpFilesServerName in cmd/pyry), transcribed rather than imported because
// those live in package main — the price mcp_status_capture_test.go and
// effort_init_capture_test.go already pay. A rename there does not reach these
// literals; the symptom is this gate's membership assertion failing, which is the
// right place for a reader to notice the document moved.
//
// mcpReconnectBrokenServer takes a name no production document registers, so a
// reader of a run log cannot mistake it for something the daemon ships. THIS
// CONSTANT IS THE ACTUATION TARGET AT EVERY USE — the key the shim writes, the row
// the assertions look up, and the server_name on the reconnect payload. It is never
// read back off a status frame and re-sent: feeding claude's own reported name back
// as the target would make "the row I broke is the row I repaired" circular, and
// would put a claude-authored string on an actuator, which MCPServerStatus,
// boundServerName and MCPReconnectPayload each separately forbid a consumer to do.
const (
	mcpReconnectApproveServer = "pyry_approve"
	mcpReconnectFilesServer   = "pyry_files"
	mcpReconnectBrokenServer  = "pyry_e2e_reconnect_target"
)

// The two claude-authored status values this gate reasons about, transcribed from
// #2272's capture rather than from any Go constant — they are claude's vocabulary,
// not the daemon's, and no layer in this repo validates them.
const (
	mcpStatusConnected = "connected"
	mcpStatusFailed    = "failed"
)

const (
	// mcpStatusReplyBudget bounds ONE correlated round trip: phone → relay → the
	// daemon's control request on the child's stdin → back. Sized against the child
	// round trip it waits on rather than against a production timeout, because the
	// read path has none of its own.
	mcpStatusReplyBudget = 60 * time.Second

	// mcpStatusSettleBudget bounds the WHOLE poll to readiness. #2272 measured both
	// healthy servers reporting pending on the first read and connected on a later
	// one, so a single read is the wrong assertion; this is the total that loop may
	// spend before a stuck server fails loudly.
	mcpStatusSettleBudget = 150 * time.Second

	// mcpStatusPoll spaces the readiness reads. Each costs a real child round trip,
	// so this is deliberately coarser than a spin.
	mcpStatusPoll = 1 * time.Second

	// mcpReconnectReplyBudget MUST EXCEED mcpActuationTimeout (30s in
	// cmd/pyry/mcp_actuate_v2.go), which bounds the daemon's whole actuation — the
	// membership read, the actuation and the post-acknowledgement read, three child
	// round trips under one figure. Staying above it is what makes a deadline here
	// mean "the daemon never answered at all" rather than "we gave up while it was
	// still working": inside that window a refusal arrives as a correlated
	// TypeError, which drainForMCPStatusReply reports as the refusal it is. If the
	// production timeout is ever widened, widen this with it.
	mcpReconnectReplyBudget = 90 * time.Second
)

// mcpErrorLogCap bounds claude's own error prose before it is printed. The producer
// already caps it at 256 bytes, but that is the producer's promise and not this
// gate's to assume; a run log this pipeline salvages is not the place to find out
// what an uncapped claude can put there.
const mcpErrorLogCap = 512

func TestRealClaudeMCPStatusAndReconnect(t *testing.T) {
	// Absolute, and under a directory that DOES exist, so the initial failure mode
	// is "this file is not there" rather than "this whole tree is not there" — the
	// one mode #2272 measured (ENOENT from posix_spawn). t.TempDir removes the
	// symlink the repair plants; removing a symlink never reaches its target.
	brokenCommand := filepath.Join(t.TempDir(), "pyry-mcp-reconnect-target")
	h, convID := startMCPReconnectHarness(t, brokenCommand)

	// The bound child must be RUNNING before anything is read or actuated: the
	// status read is a control request written to a live child's stdin, and
	// boundServerName refuses a name that child does not currently report. One
	// tool-free turn is enough, and under this harness's downgraded spawn arm a
	// prompt that touches no tool raises no permission modal to answer.
	sealSendMessage(t, h.phone, h.initSend, 2, convID, "m-2",
		"Reply with the single word ready and nothing else. Do not use any tool.")
	drainForCompletedTurn(t, h.phone, h.initRecv, convID, perTurnReplyBudget)

	reqID := uint64(2)
	nextReqID := func() uint64 { reqID++; return reqID }

	// AC 1. Poll to readiness first — asserting a single read would redden on
	// pending, which is what both healthy servers report before they settle.
	settled := pollMCPStatusUntilHealthy(t, h, convID, nextReqID)
	broken := requireMCPServerRow(t, settled, mcpReconnectBrokenServer,
		"the deliberately broken server is missing from the status report; the shim never "+
			"rewrote the --mcp-config document, or claude dropped the entry instead of reporting it")
	if broken.Status != mcpStatusFailed {
		t.Fatalf("broken server %q reports status %q, want %q — the entry the shim planted was "+
			"supposed to name a command that does not exist, so a status other than failed means "+
			"the arrangement under test never existed",
			mcpReconnectBrokenServer, broken.Status, mcpStatusFailed)
	}
	if strings.TrimSpace(broken.Error) == "" {
		t.Fatalf("broken server %q reports %q with an EMPTY error — claude's own text is the half "+
			"of this measurement the daemon cannot author, so a failed row without it proves nothing",
			mcpReconnectBrokenServer, mcpStatusFailed)
	}
	t.Logf("broken server %q: status=%q scope=%q version=%q error=%q",
		mcpReconnectBrokenServer, broken.Status, broken.Scope, broken.Version,
		truncateString(broken.Error, mcpErrorLogCap))

	// Repair: put the built pyry binary where the broken entry points. The entry is
	// a clone of pyry_files' registration, so the reconnect brings this server up
	// exactly the way its twin came up. A symlink rather than a copy — it costs no
	// I/O and execs identically.
	if _, err := os.Lstat(brokenCommand); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the broken command path %q already exists (stat: %v) — the server would not have "+
			"been broken and this run measured two healthy twins", brokenCommand, err)
	}
	if err := os.Symlink(ensurePyryBuilt(t), brokenCommand); err != nil {
		t.Fatalf("repair the broken server's command: %v", err)
	}

	// AC 2. The answer to an accepted actuation is mcpActuatorV2.actuate's SECOND
	// read, taken after the child acknowledged — never the membership read that
	// preceded it. That is what makes the changed row attributable to claude.
	reconnectID := nextReqID()
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   reconnectID,
		Type: protocol.TypeMCPReconnect,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.MCPReconnectPayload{
			ConversationID: convID,
			ServerName:     mcpReconnectBrokenServer,
		}),
	})
	after := drainForMCPStatusReply(t, h, reconnectID, mcpReconnectReplyBudget)

	repaired := requireMCPServerRow(t, after, mcpReconnectBrokenServer,
		"the reconnect's post-acknowledgement report no longer lists the server it reconnected")
	if repaired.Status == mcpStatusFailed {
		t.Fatalf("after repairing its command and reconnecting it, server %q still reports %q "+
			"(error %q) — the reconnect did not reach claude, or claude did not re-run the command",
			mcpReconnectBrokenServer, repaired.Status, truncateString(repaired.Error, mcpErrorLogCap))
	}
	// A one-row or empty frame would satisfy the assertion above vacuously.
	for _, name := range []string{mcpReconnectApproveServer, mcpReconnectFilesServer} {
		requireMCPServerRow(t, after, name,
			"the post-reconnect report dropped one of the daemon's own servers, so it is not the "+
				"full snapshot the changed row was read from")
	}
	t.Logf("after reconnect, server %q: status=%q version=%q (was %q) — the reconnect reached claude",
		mcpReconnectBrokenServer, repaired.Status, repaired.Version, mcpStatusFailed)
}

// --- harness ----------------------------------------------------------------

// startMCPReconnectHarness is startStreamModalResolutionHarness with the observer
// hook startObservedPermissionHarness already exposes: configure runs immediately
// before the daemon spawns, seeds the shim's environment (spawnPermissionDaemon
// forks with os.Environ(), and streamsup passes it down to claude) and returns this
// test binary as the daemon's -pyry-claude.
//
// permissionDaemonModel rather than the larger askQuestionCaptureModel: this gate
// drives no tool call, so nothing here needs the model the question gates argue for.
func startMCPReconnectHarness(t *testing.T, brokenCommand string) (*perConvHarness, string) {
	t.Helper()
	h, convID, _ := startObservedPermissionHarness(t, permissionDaemonModel, false, func(realBin string) string {
		t.Logf("claude_version=%s", stdioPermissionSafeLabel(stdioPermissionClaudeVersion(realBin)))
		t.Setenv(mcpShimEnabled, "1")
		t.Setenv(mcpShimRealBin, realBin)
		t.Setenv(mcpShimCommand, brokenCommand)
		return os.Args[0]
	})
	return h, convID
}

// --- the shim ---------------------------------------------------------------

// runMCPConfigShim is invoked by TestMain only when this test binary stands in
// front of the real claude. It adds one server to the daemon's own --mcp-config
// document and execs claude; it manufactures no control message and reads no stream.
//
// IT WRITES NOTHING TO STDOUT, EVER — not a log line, not a diagnostic, not before
// the exec. It is running as the daemon's claude child, whose stdout is exclusively
// the stream-json the parser reads; a stray write corrupts that stream and presents
// as a parse failure with no visible connection to its cause. runMCPFiles states the
// same rule for the same reason. Diagnostics go to stderr, which the harness tees.
func runMCPConfigShim() int {
	realBin := os.Getenv(mcpShimRealBin)
	if realBin == "" || sameResolvedPath(realBin, os.Args[0]) {
		fmt.Fprintf(os.Stderr, "mcp shim: %s must name a binary other than this one\n", mcpShimRealBin)
		return 81
	}
	// An entry pointing the document at this binary would start with
	// PYRY_MCP_SHIM=1 still in its inherited environment, so claude launching that
	// "MCP server" would re-enter this function and exec another claude — the
	// 2026-05-16 fork bomb in a new costume. ensurePyryBuilt carries the same guard.
	command := os.Getenv(mcpShimCommand)
	if command != "" && sameResolvedPath(command, os.Args[0]) {
		fmt.Fprintln(os.Stderr, "mcp shim: refusing to register this binary as an MCP server")
		return 82
	}
	if path, ok := mcpShimConfigPath(os.Args[1:]); ok && command != "" {
		if err := mcpShimInjectServer(path, command); err != nil {
			fmt.Fprintf(os.Stderr, "mcp shim: %v\n", err)
			return 83
		}
	}
	// THE ARGV IS PASSED VERBATIM — nothing appended, nothing reordered.
	// mcpStatusEligible requires --strict-mcp-config plus exactly ONE --mcp-config
	// whose value equals the daemon's own path, so an argv this shim "helpfully"
	// extended would make the child ineligible and every status read and actuation
	// would refuse, which reads as the feature failing rather than the rig.
	if err := syscall.Exec(realBin, append([]string{realBin}, os.Args[1:]...), os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "mcp shim: exec %s: %v\n", realBin, err)
		return 84
	}
	return 0 // unreachable: a successful Exec replaces this process image.
}

// sameResolvedPath reports whether two paths name the same file after
// absolutisation. ensurePyryBuilt's self-reference check, extracted so the shim's
// two guards share one spelling.
func sameResolvedPath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

// mcpShimConfigPath returns the value of the single --mcp-config flag in args, in
// both spellings claude accepts. Absent is NOT an error: the daemon probes the
// binary in ways that carry no document (--version), and a spawn on the bypass arm
// carries none either. Those pass through untouched, and the gate's own status
// assertion is what keeps a run that never saw a document from passing vacuously.
func mcpShimConfigPath(args []string) (string, bool) {
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			return args[i+1], true
		}
		if value, ok := strings.CutPrefix(arg, "--mcp-config="); ok {
			return value, true
		}
	}
	return "", false
}

// mcpShimInjectServer adds one server to the document at path: a clone of the
// pyry_files registration with its command repointed at a path that does not exist
// yet. Repairing it is then putting the pyry binary there, and the reconnect brings
// the server up the way its twin came up — which keeps the failure to the one mode
// #2272 measured, an absent stdio command.
//
// THE DOCUMENT IS REWRITTEN AS RAW MESSAGES, never through a local twin of
// mcpServerSpec. A typed round trip silently drops every key the local type does not
// model: today the shapes match, but the day renderMCPServersConfig grows a field
// this shim would strip it from BOTH production entries and the gate would stay
// green while measuring a degraded document. Existing entries are copied through
// byte for byte; only the new one is synthesised.
//
// The rewrite is IN PLACE, at the daemon's own path, which is load-bearing rather
// than convenient: mcpStatusEligible compares the argv's --mcp-config value against
// the daemon's MCPStatusConfigPath, so a second document with the argv swapped to
// match would make every read and actuation refuse. Re-entering on a child respawn
// is a no-op — the key is already there.
func mcpShimInjectServer(path, command string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read mcp config: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode mcp config: %w", err)
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(doc["mcpServers"], &servers); err != nil {
		return fmt.Errorf("decode mcpServers: %w", err)
	}
	if _, done := servers[mcpReconnectBrokenServer]; done {
		return nil
	}
	twin, ok := servers[mcpReconnectFilesServer]
	if !ok {
		return fmt.Errorf("mcp config has no %q entry to clone", mcpReconnectFilesServer)
	}
	var spec struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal(twin, &spec); err != nil {
		return fmt.Errorf("decode %q entry: %w", mcpReconnectFilesServer, err)
	}
	spec.Command = command
	clone, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode cloned entry: %w", err)
	}
	servers[mcpReconnectBrokenServer] = clone
	if doc["mcpServers"], err = json.Marshal(servers); err != nil {
		return fmt.Errorf("encode mcpServers: %w", err)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode mcp config: %w", err)
	}
	// The daemon created this file at 0600 via os.CreateTemp and it already exists,
	// so the mode argument is a statement of what must remain true rather than the
	// mechanism that makes it so.
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("write mcp config: %w", err)
	}
	return nil
}

// --- reading the status -----------------------------------------------------

// pollMCPStatusUntilHealthy reads status on fresh request ids until BOTH of the
// daemon's own servers report connected, and returns that settled report.
//
// Polling rather than one read is #2272's own precedent: its capture holds two
// mcp_status replies from a single spawn that differ on exactly this, both servers
// pending on the first and connected on the second. Readiness belongs to the control
// replies — there is no init event to wait on, because that event is emitted per
// user turn and would make the wait circular.
func pollMCPStatusUntilHealthy(t *testing.T, h *perConvHarness, convID string, nextReqID func() uint64) protocol.MCPStatusPayload {
	t.Helper()
	deadline := time.Now().Add(mcpStatusSettleBudget)
	for {
		id := nextReqID()
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
			ID:      id,
			Type:    protocol.TypeMCPStatusRequest,
			TS:      time.Now().UTC(),
			Payload: mustJSON(t, protocol.MCPStatusRequestPayload{ConversationID: convID}),
		})
		report := drainForMCPStatusReply(t, h, id, mcpStatusReplyBudget)

		approve, approveOK := mcpServerRow(report, mcpReconnectApproveServer)
		files, filesOK := mcpServerRow(report, mcpReconnectFilesServer)
		if approveOK && filesOK &&
			approve.Status == mcpStatusConnected && files.Status == mcpStatusConnected {
			t.Logf("both daemon servers connected after %d status read(s); the report carries %d server(s)",
				id-2, len(report.Servers))
			return report
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q and %q did not both reach %q within %s (last read: %q=%q, %q=%q) — the "+
				"daemon's own servers never came up, so a broken third one proves nothing",
				mcpReconnectApproveServer, mcpReconnectFilesServer, mcpStatusConnected, mcpStatusSettleBudget,
				mcpReconnectApproveServer, approve.Status, mcpReconnectFilesServer, files.Status)
		}
		time.Sleep(mcpStatusPoll)
	}
}

// mcpServerRow returns the report's row for name. Name equality only: this is the
// same exact-membership rule boundServerName applies, and for its reason — the
// names are claude's to spell and no shape guess may stand in for equality.
func mcpServerRow(report protocol.MCPStatusPayload, name string) (protocol.MCPServerStatus, bool) {
	for _, server := range report.Servers {
		if server.Name == name {
			return server, true
		}
	}
	return protocol.MCPServerStatus{}, false
}

// requireMCPServerRow is mcpServerRow with the absence turned into a fatal carrying
// the caller's reading of what an absent row would mean. It logs the names the
// report DID carry through %q: they are claude-authored bytes, nothing on this path
// strips terminal escapes, and the pipeline salvages run logs.
func requireMCPServerRow(t *testing.T, report protocol.MCPStatusPayload, name, why string) protocol.MCPServerStatus {
	t.Helper()
	if row, ok := mcpServerRow(report, name); ok {
		return row
	}
	present := make([]string, 0, len(report.Servers))
	for _, server := range report.Servers {
		present = append(present, fmt.Sprintf("%q", server.Name))
	}
	t.Fatalf("no row named %q in the status report — %s; the report carries %d row(s): %s",
		name, why, len(report.Servers), strings.Join(present, ", "))
	return protocol.MCPServerStatus{}
}

// drainForMCPStatusReply is drainForReply specialised to this pair of verbs, and the
// specialisation is the point: the generic drain SKIPS a TypeError, so a refused
// status read or a refused actuation would present here as a deadline — "claude
// never answered" — when the truth is that the daemon refused us before any child
// was touched. Both answers ride Envelope.InReplyTo, so a correlated TypeError is
// unambiguously ours and is reported with the code the relay chose.
//
// The frame loop is the package's standing one: read binary→phone frames in receive
// order, decrypt EVERY noise_msg to keep the sequential receive nonce in sync, and
// skip non-noise_msg control frames WITHOUT decrypting. Interleaved acks, turn_state
// and deltas are decrypted and skipped in order.
func drainForMCPStatusReply(t *testing.T, h *perConvHarness, reqID uint64, timeout time.Duration) protocol.MCPStatusPayload {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no mcp_status reply to request %d within %s", reqID, timeout)
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the t.Fatalf above
			}
			t.Fatalf("phone receive (awaiting mcp_status for request %d): %v", reqID, err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (mcp_status drain): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // does not advance the receive nonce
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (mcp_status drain): %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (mcp_status drain): %v", err)
		}
		if env.InReplyTo == nil || *env.InReplyTo != reqID {
			continue // an ack, a broadcast, or an earlier reply
		}
		switch env.Type {
		case protocol.TypeError:
			var e protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &e); err != nil {
				t.Fatalf("decode error payload correlated to request %d: %v", reqID, err)
			}
			t.Fatalf("request %d was REFUSED with code %q (retryable=%v) — the daemon answered, so "+
				"this is a rejected conversation, an unhosted id, an ineligible child or a refused "+
				"actuation, not a claude that stayed silent", reqID, e.Code, e.Retryable)
		case protocol.TypeMCPStatus:
			var report protocol.MCPStatusPayload
			if err := json.Unmarshal(env.Payload, &report); err != nil {
				t.Fatalf("decode mcp_status payload for request %d: %v", reqID, err)
			}
			return report
		}
	}
}

// --- the shim's own logic, offline ------------------------------------------

// TestMCPShimConfigPath pins both spellings claude accepts and the absent case,
// which must stay a pass-through rather than an error — a --version probe carries
// no document.
func TestMCPShimConfigPath(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{"separate", []string{"--model", "haiku", "--mcp-config", "/tmp/d.json"}, "/tmp/d.json", true},
		{"joined", []string{"--mcp-config=/tmp/d.json", "--strict-mcp-config"}, "/tmp/d.json", true},
		{"absent", []string{"--version"}, "", false},
		{"dangling", []string{"--mcp-config"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mcpShimConfigPath(tc.args)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("mcpShimConfigPath(%v) = %q,%v; want %q,%v", tc.args, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestMCPShimInjectServerPreservesUnmodelledKeys is the regression guard for this
// slice's security-review finding: rewriting the document through a local twin of
// mcpServerSpec would drop every key that twin does not model, stripping it from
// BOTH production entries while the live gate stayed green against a degraded
// document. "env" stands in for whatever field renderMCPServersConfig grows next.
func TestMCPShimInjectServerPreservesUnmodelledKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	const doc = `{"mcpServers":{` +
		`"pyry_approve":{"command":"/opt/pyry","args":["mcp-approve"],"env":{"K":"V"}},` +
		`"pyry_files":{"command":"/opt/pyry","args":["mcp-files","-pyry-socket","/s.sock"]}},` +
		`"otherTopLevel":true}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mcpShimInjectServer(path, "/nowhere/pyry"); err != nil {
		t.Fatalf("mcpShimInjectServer: %v", err)
	}

	after := readMCPShimDoc(t, path)
	if got := string(after["otherTopLevel"]); got != "true" {
		t.Fatalf("unmodelled TOP-LEVEL key did not survive the rewrite: %q", got)
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(after["mcpServers"], &servers); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(servers[mcpReconnectApproveServer]), `"env"`) {
		t.Fatalf("unmodelled per-entry key was stripped from %q: %s",
			mcpReconnectApproveServer, servers[mcpReconnectApproveServer])
	}
	// The clone carries pyry_files' argv and the broken command, so repairing the
	// path brings it up the way its twin came up.
	var clone struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal(servers[mcpReconnectBrokenServer], &clone); err != nil {
		t.Fatalf("the shim planted no %q entry: %v", mcpReconnectBrokenServer, err)
	}
	if clone.Command != "/nowhere/pyry" {
		t.Fatalf("clone command = %q, want the broken path", clone.Command)
	}
	if want := []string{"mcp-files", "-pyry-socket", "/s.sock"}; !slices.Equal(clone.Args, want) {
		t.Fatalf("clone args = %v, want %q's own %v", clone.Args, mcpReconnectFilesServer, want)
	}

	// Idempotent: a child respawn re-enters the shim and must not append a second
	// entry or re-nest the document.
	if err := mcpShimInjectServer(path, "/somewhere/else"); err != nil {
		t.Fatalf("second inject: %v", err)
	}
	again := readMCPShimDoc(t, path)
	if !bytes.Equal(again["mcpServers"], after["mcpServers"]) {
		t.Fatalf("re-entering the shim rewrote the document:\n%s\n%s", after["mcpServers"], again["mcpServers"])
	}
}

// TestMCPShimInjectServerRefusesWithoutTwin fails loudly rather than planting an
// entry it cannot clone: a document without pyry_files means the production
// registration moved, and guessing an argv here would measure a different failure.
func TestMCPShimInjectServerRefusesWithoutTwin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"pyry_approve":{"command":"/opt/pyry"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mcpShimInjectServer(path, "/nowhere/pyry"); err == nil {
		t.Fatalf("mcpShimInjectServer accepted a document with no %q entry to clone", mcpReconnectFilesServer)
	}
}

func readMCPShimDoc(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}
