//go:build e2e_realclaude

package realclaude

// #1634 — proof that the model a REAL claude announces reaches a connected client
// inside the daemon's OWN emitted frame.
//
// The path claude's init line travels is now complete: streamsup's parser mints a
// turnevent.ModelAnnounced (#1600), turnbridge.MapEvent maps it to
// protocol.TypeModelAnnounced (#1638), and interactiveTurnEmitterV2.Handle pushes
// it (#1638). Every test on that path is hermetic — each feeds a constructed
// turnevent.ModelAnnounced and reads what one link produced.
//
// # Why the existing live test does not discriminate
//
// TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel taps
// streamsup.Config.Stdout, which is UPSTREAM of the parser. It was green for the
// whole period when MapEvent dropped the variant and no frame existed at all, and
// it would stay green if #1638's two arms were reverted tomorrow. Reading the
// assertion off the daemon's own EMITTED frame, decoded at a connected fakephone,
// is the only form that discriminates.
//
// # What is pinned here, and what is not
//
// The job of this test is the PATH, not the value's provenance. That the value
// crosses byte-for-byte is already pinned hermetically at both tiers it passes
// through. What no test showed until this one is that a whole real daemon carries
// a real claude's announcement out to a client at all.
//
// So the frame's model gets two assertions of different fabric, and they are
// deliberately NOT independent mutants — see the A1/A2 note at each assertion.
// The same relationship the in-band model test documents and maintains.
//
// # Why the daemon runs at debug level
//
// AC-4 asserts an ABSENCE: the announced value reaches no daemon log. Both
// plausible leak sites for this value log at Debug — streamsup's
// emitModelAnnounced undecodable-line drop and cmd/pyry's Handle
// interactive_turn.unknown default — so at the shared spawner's LevelInfo neither
// record is ever emitted and their absence would prove nothing. Hence
// spawnBootstrapDaemonVerbose below. The child's own stderr is not a confound:
// mapStreamsupConfig leaves streamsup.Config.Stderr nil and the runner assigns
// that straight to cmd.Stderr, so claude's stderr is discarded and the buffer
// holds the daemon's own slog output only.
//
// # Evidence: the three runs behind AC-3
//
// Both #1638 arms fall through to an existing default when deleted, so each
// mutant compiles. Neither is reachable through `go test -overlay` — this test
// asserts against a SEPARATELY BUILT daemon binary and mutant 2 lives in
// cmd/pyry, which the test binary never compiles at all. The route is a mutant
// binary via ensurePyryBuilt's PYRY_E2E_BIN short-circuit:
//
//	go build -overlay=/abs/overlay.json -o /tmp/pyry-mutant ./cmd/pyry
//	PYRY_E2E_BIN=/tmp/pyry-mutant go test -tags e2e_realclaude -race -v \
//	  -run TestInteractiveStreamModelAnnouncedFrame ./internal/e2e/realclaude/
//
// Measured 2026-08-20, all three runs with -race, one live turn each.
//
// THE CURRENT TREE, PYRY_E2E_BIN UNSET — PASS in 5.47 s. One model_announced
// frame drained: model "claude-haiku-4-5-20251001", truncated=false,
// conversation_id "16340000-0000-4000-8000-000000000002" — the driving conv, as
// Handle's sup.CurrentConversation() predicts. Both AC-2 assertions green, and
// the resolved identifier appears nowhere in the daemon's debug-level stderr.
// The green run must be taken with PYRY_E2E_BIN UNSET; a green recorded against
// a stale mutant binary would be the worst possible outcome of this ticket.
//
// MUTANT 1 — MapEvent's `case turnevent.ModelAnnounced:` deleted, falling through
// to `default: return "", nil, false`. RED at AC-1 in 5.11 s: zero
// model_announced frames on an otherwise healthy turn (delta observed, closed at
// turn_state{idle}). The daemon logged the mutant's own signature —
//
//	level=DEBUG msg="relay: interactive-turn drop; no wire mapping"
//	  event=interactive_turn.unmapped kind=model_announced
//
// MUTANT 2 — Handle's `case turnevent.ModelAnnounced:` deleted, falling through
// to the interactive_turn.unknown Debug default. RED at AC-1 in 3.94 s, same
// shape, distinguished by its own record —
//
//	level=DEBUG msg="relay: interactive-turn drop; unknown event"
//	  event=interactive_turn.unknown kind=model_announced
//
// Both mutants RED in seconds rather than minutes because drainForAnnouncedModel
// returns at the turn's close rather than waiting for the frame. Mutant 2's
// target is the case inside interactiveTurnEmitterV2.Handle — the same file's
// eventKind has a case with the same name and it stays; deleting THAT one only
// changes a log field and this test would remain green.
//
// The green run also confirms the AC-4 trap is real rather than theoretical: the
// daemon's `spawning claude` record logs the argv verbatim, `--model haiku`
// included, so an AC-4 that searched announcedSpawnAlias would fail on every
// healthy daemon. Searching the frame's own value is what makes it an assertion.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -v \
//	  -run TestInteractiveStreamModelAnnouncedFrame ./internal/e2e/realclaude/
//
// It executes on any machine with claude credentials — the only skips are the
// package's standard absent-binary / absent-credentials guards. This package is
// behind the e2e_realclaude build tag, so `make check` never compiles it: read
// the count of executed tests, never the exit code.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state. Ticket-encoded: single-char-repeat UUID
// stems are exhausted in this package, so these follow the convention
// spawnBootstrapDaemonWithIdle's file established — collision-free by
// construction and valid UUIDv4 (version nibble 4, variant nibble 8).
// announcedBootstrapUUID is the bootstrap session's POOL id (pinned via
// seedBootstrapRegistry); announcedConvID is the driving conversation, bound to it
// via seedBoundConversation. Each test owns its own authenticated tempdir HOME, so
// literals never collide on disk; distinct values only keep cross-test confusion
// impossible.
const (
	announcedBootstrapUUID = "16340000-0000-4000-8000-000000000001"
	announcedConvID        = "16340000-0000-4000-8000-000000000002"
)

// announcedSpawnAlias is the bare family alias the daemon is spawned with, and it
// is ONE constant used four ways: the value after --model in
// spawnBootstrapDaemonVerbose's argv, the key looked up in inbandModelTargets, the
// comparand in A2's inequality, and the alias named in both failure messages.
//
// Four literals could drift apart silently and leave the inequality comparing the
// frame against a string the daemon was never spawned with — a vacuous pass. One
// constant makes that impossible.
const announcedSpawnAlias = "haiku"

func TestInteractiveStreamModelAnnouncedFrame(t *testing.T) {
	runParallel(t)
	// Parallel only through runParallel; see liveHome.
	claudeBin := resolveClaudeBin(t) // t.Skip when claude is not on PATH
	home := liveHome(t)              // t.Skip when there are no credentials

	// Isolated workdir under the authenticated HOME. Load-bearing twice: it
	// guarantees the fresh-daemon state (empty claude sessions dir) AND sidesteps
	// the shared-session-folder collision (#828) by construction.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#1634: mkdir workdir: %v", err)
	}

	// Pin the stream-json interactive runner BEFORE the daemon spawns —
	// resolveConfigPath reads <home>/.pyry/config.json once at startup. This is a
	// PIN, not a requirement: selectInteractiveRunner has mapped the empty default
	// to the stream runner since #1348 and the isolated HOME carries no config
	// file, so the line is redundant today. Keep it anyway — the default has
	// already moved once, and the failure mode it guards against is a silent
	// multi-minute drain timeout on a live run.
	writeStreamInteractiveConfig(t, home)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("#1634: paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("#1634: decode server static pubkey: %v", err)
	}

	// Seed the deterministic bootstrap id + the driving conversation binding BEFORE
	// the daemon starts (the registry is loaded once at startup, no reload).
	seedBootstrapRegistry(t, home, announcedBootstrapUUID)
	seedBoundConversation(t, home, announcedConvID, announcedBootstrapUUID, workdir)

	d := spawnBootstrapDaemonVerbose(t, home, workdir, claudeBin, relayURL)
	t.Cleanup(func() { d.stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("#1634: phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeInteractive(t, phone, pubKey, payload.Token)

	sequence := uint64(1)
	wire := scenariohelpers.Wire{Send: func(kind string, payload any) uint64 {
		sequence++
		sealEnvelope(t, phone, initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
		return sequence
	}, Next: scenariohelpers.Receive(t, phone, initRecv)}
	scenariohelpers.ModelAnnouncement(t, wire, announcedConvID, fmt.Sprintf("Reply with a single short word. run=%d", time.Now().UnixNano()), announcedSpawnAlias, announcedTargetFor(t, announcedSpawnAlias), perTurnReplyBudget, d.stderr.String)

}

// announcedTargetFor returns the identifier the package's MEASURED
// inbandModelTargets table resolves alias to. A1's comparand is sourced from that
// table rather than a freshly typed literal so the two live tests that care about
// this claude-VERSION fact keep exactly one row to update between them.
//
// A missing row is a broken instrument, not a condition to skip on: the alias this
// file spawns with is a compile-time constant, so the row's absence means someone
// removed it from under a live consumer.
func announcedTargetFor(t *testing.T, alias string) string {
	t.Helper()
	for _, tgt := range inbandModelTargets {
		if tgt.alias == alias {
			return tgt.resolvedID
		}
	}
	t.Fatalf("#1634: no inbandModelTargets row for the spawn alias %q (table: %v); A1 has no comparand — "+
		"restore the row rather than inlining a literal here", alias, inbandModelTargets)
	return ""
}

// logContextAround returns a bounded window of s around a match, for the AC-4
// failure message. The daemon's stderr runs to tens of KiB at debug level, so the
// whole buffer would bury the finding it is reporting.
func logContextAround(s string, idx, matchLen int) string {
	const pad = 120
	start := idx - pad
	if start < 0 {
		start = 0
	}
	end := idx + matchLen + pad
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// --- helpers (local to this file; distinct names to avoid same-package collision) ---

// spawnBootstrapDaemonVerbose is spawnBootstrapDaemon (harness_daemon_test.go)
// with its sole delta being -pyry-verbose, so the daemon logs at slog.LevelDebug
// and AC-4's absence assertion runs against a haystack that would actually carry
// the leak it looks for.
//
// The flag sits BEFORE the -- separator, which is what routes it to pyry rather
// than to claude; cmd/pyry's args_test.go table pins both halves of that
// independently (the row for {"-pyry-verbose", "--", "--model", "sonnet"} routes
// it to pyry, the row without -- routes it to claude).
//
// Why a near-copy rather than a variadic option on the shared spawner: the
// variadic is ~25 lines smaller and cannot change existing call sites, but it
// restructures the argv assembly inside a function thirteen live tests depend on,
// and no hermetic gate covers this package — `make check` never compiles it, so a
// restructure error would surface only as thirteen failing live runs.
// spawnBootstrapDaemonWithIdle established this precedent for exactly that reason.
// Everything else is reused same-package: bootstrapDaemon, shortSocketPath,
// ensurePyryBuilt, lockedBuffer, waitForReady.
func spawnBootstrapDaemonVerbose(t *testing.T, home, workdir, claudeBin, relayURL string) *bootstrapDaemon {
	t.Helper()
	bin := ensurePyryBuilt(t) // builds with real HOME (warm cache); runs with isolated HOME
	socket := shortSocketPath(t)
	stderr := &lockedBuffer{}

	args := []string{
		"-pyry-socket=" + socket,
		"-pyry-name=test",
		"-pyry-claude=" + claudeBin,
		"-pyry-idle-timeout=0",
		"-pyry-workdir=" + workdir,
		"-pyry-relay=" + relayURL,
		"-pyry-verbose",
		"--",
		"--model", announcedSpawnAlias,
		"--dangerously-skip-permissions",
	}
	cmd := exec.Command(bin, args...)
	// HOME is set explicitly rather than inherited, so the harness also runs under
	// t.Parallel, where nothing pins it process-wide; the credential is inherited.
	// Add the relay switches.
	cmd.Env = homeEnv(home, "PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1")
	// DEBUG tee + AC-4's haystack. At -pyry-verbose the buffer grows with the
	// daemon's own Debug-level records for the life of one turn — bounded in
	// practice by the turn, the same buffer spawnBootstrapDaemonWithIdle already
	// fills at Info.
	cmd.Stderr = stderr // Primary daemon logs stay in memory.

	if err := cmd.Start(); err != nil {
		t.Fatalf("realclaude: pyry start: %v", err)
	}
	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	d := &bootstrapDaemon{socketPath: socket, cmd: cmd, doneCh: doneCh, stderr: stderr}
	if err := d.waitForReady(10 * time.Second); err != nil {
		t.Fatalf("realclaude: daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}
	return d
}

// drainForAnnouncedModel is a lean fork of drainForCompletedTurn
// (interactive_stream_liveness_test.go): same M1(non-empty assistant_delta)→
// M2(terminal turn_state{idle}) milestones and the same in-order noise-decrypt
// discipline (the receive nonce is sequential, so every noise_msg MUST be
// decrypted in arrival order or the CipherState desyncs; a non-noise_msg control
// frame such as a rekey is skipped WITHOUT decrypting). Like
// drainForResumedTurnText it drops the shared drain's two sentinel arms rather
// than re-implementing them per fork.
//
// The behavioural delta: it records the FIRST protocol.TypeModelAnnounced payload
// it sees and returns it, with whether one was seen, when the turn closes.
//
// It returns at the turn's CLOSE rather than waiting for the frame, and that is
// load-bearing for the mutation evidence: under either mutant no frame is ever
// emitted, so a drain that waited for one would burn its full timeout on every red
// run — the difference between AC-3 costing one live-suite budget and costing
// three.
//
// The model_announced arm carries NO conversation filter: it takes the first frame
// of that type whatever its ConversationID and logs the id it got. Handle derives
// that id from sup.CurrentConversation(), so it is the driving conv in practice,
// but asserting it is outside every AC and a filter that guessed wrong would hang
// the drain instead of failing loudly. The turn_state arm keeps its convID filter —
// that one is the turn boundary.
func drainForAnnouncedModel(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) (protocol.ModelAnnouncedPayload, bool) {
	t.Helper()
	return scenariohelpers.AnnouncedTurn(t, scenariohelpers.Receive(t, phone, cs), convID, timeout)
}
