//go:build e2e_realclaude

package realclaude

// #1622 — proof that a bypass revocation issued through pyry's OWN Pool reaches a
// real claude child on the stream the daemon already holds open, and does not tear
// that child down.
//
// # What was already proven, and what was not
//
// #1595 proved the BYTES: a set_permission_mode control request carrying
// mode "default", hand-written onto a running child's stdin, drops the bypass
// posture with no respawn (docs/knowledge/features/set-permission-mode-inband-probe.md).
// That test owned its four children through exec.CommandContext and wrote the
// control line itself.
//
// #1604 built the COMPOSED PATH — Pool.UpdateSettings → inBandDeliverable →
// Pool.deliverSettingsInBand → Runner.SetPermissionMode (Runner.RevokeBypass
// until #2043 collapsed the pair) — and proved it hermetically
// through a fake runner. That proves the daemon ASKS. Nothing proved pyry emits
// those bytes, on the stream it already holds open, to a real claude.
//
// The gap matters because the failure is silent in both directions. A child that
// keeps auto-approving after a revocation is indistinguishable in the daemon log
// from one that was never in bypass; and delivery is fire-and-forget —
// deliverSettingsInBand logs a failed posture send at Info and swallows it, so
// UpdateSettings returns nil either way. This file makes that swallow a readable
// field (see newRevokeLogRecorder) rather than silence.
//
// # Scope boundary
//
// This asserts that the revocation REACHED the child and that nothing was torn
// down. It does NOT claim behavioural enforcement — an echoed permission mode is
// claude's report of its own posture, not proof the posture is enforced. No
// behavioural probe, no control arms, no committed fixture: those are the sibling
// ticket that consumes this harness. It changes no production code.
//
// # Where the bypass comes from, and why that is the whole measurement
//
// revokeBaseArgs is EMPTY. The child's bypass posture comes from the STORED
// settings — a seeded registry entry carrying yolo:true, which Pool.New loads via
// pickBootstrap and canonicalises into the bypass mode. Both shortcuts the ticket
// names would yield a run that measures a child nobody revoked: a stored posture
// already equal to the update returns at UpdateSettings' no-change check having
// delivered nothing, and a base argv carrying --dangerously-skip-permissions makes
// operatorBypass(base) true, which since #2065 tells the daemon this escalation is
// the OPERATOR's and may not be walked back at all. The seam gets TWO guards rather than a
// code-reading argument — instrument check B before any child is spawned, and
// instrument check C after turn 1.
//
// # Where it taps
//
// streamsup's spawnAndWait assigns cmd.Stdout = r.cfg.Stdout, so
// streamsup.Config.Stdout IS the child's raw stdout sink and carries every line
// claude writes, control_response included; in production the turnevent parser is
// what occupies that slot, which is what "upstream of the parser" means.
// (docs/knowledge/codebase/1595.md explains #1595's divergence from #1582 as
// needing "the raw control_response, which that seam sits downstream of". Read
// against merged code that parenthetical is wrong. #1595's actual reason, recorded
// earlier in its own note, is that it needed four exec.CommandContext children
// with hand-built argv, which a Pool-spawned child cannot give. That note is
// read-only, so the correction is recorded here.)
//
// # The response cannot be correlated by id
//
// Runner.SetPermissionMode mints its request_id through nextControlID, an unexported
// per-runner atomic counter. The test cannot read it, and setModeResponseIDMatches
// is therefore not reusable here. Hard-coding "1" on the reasoning that this is
// the runner's first control request would pin a private counter's start value as
// a test contract. Correlation is by ARRIVAL WINDOW — a baseline taken before
// UpdateSettings, strictly greater after — and the responses are logged verbatim
// so a reader can check the id, the subtype and the echoed mode by eye.
//
// # Evidence: which assertion carries which proof
//
// The four assertions split into two pairs, and neither pair is redundant: each
// pair is the SOLE red for one mutant. Measured 2026-08-19 against claude 2.1.220,
// all three runs with -race. Mutants were applied through
// `go test -overlay=<abs>/overlay.json`, so no mutated source was ever written into
// the worktree. Every measured red set below equals the one predicted for it.
//
// THE CURRENT TREE — all four green.
//
//	init permissionModes [bypassPermissions default], control_responses 1
//	(baseline 0), spawns 1, pid 74313 -> 74313, 2 results, 5.85 s.
//	control_response verbatim:
//	  {"type":"control_response","response":{"subtype":"success",
//	   "request_id":"1","response":{"mode":"default"}}}
//
// TWO init lines, not three. The revocation is a control request rather than a
// turn, so it emits no init line of its own — the model test measured three
// because /model IS an ordinary user turn. Measured on all three runs, and it is
// why the assertions read first-and-last rather than a fixed index: do not assert
// len(modes) == 2.
//
// The ack carried request_id "1", i.e. this WAS the runner's first control
// request. That is recorded as an observation and deliberately not asserted:
// nextControlID is an unexported per-runner atomic counter, and pinning its start
// value would make a private implementation detail a test contract.
//
// M1 — the `update.YOLO != nil && !*update.YOLO` clause dropped from
// Pool.deliverSettingsInBand, so UpdateSettings takes the in-band branch and
// writes NOTHING. A1, A2 RED; A3, A4 green.
//
//	init permissionModes [bypassPermissions bypassPermissions],
//	control_responses 0, spawns 1, pid 75109 -> 75109, 2 results, 50.03 s.
//
// Nothing was written and nothing was torn down — which is what earns A1 and A2
// against a tree that does nothing.
//
// M2 — inBandDeliverable returns false for a revoke (the pre-#1604 shape), so
// UpdateSettings falls through to sup.Restart(newArgs). A1, A3, A4 RED; A2 GREEN.
//
//	init permissionModes [bypassPermissions default],
//	control_responses 0, spawns 2, pid 76488 -> 76586, 2 results, 50.57 s.
//
// This is the row that earns the assertion set. The respawned child reported
// `default` off its recomposed bypass-free argv, so A2 passed on a tree that
// delivered no revocation at all: A2 alone cannot tell a delivered revocation from
// a respawn under a rebuilt argv, and A1, A3 and A4 are what separate them.
//
// Both red runs took ~50 s against the green run's 5.85 s: no control_response
// ever arrives, so the tolerated wait below burns its full revokeControlBudget.
// That is the wait working, not a hang. On M2 it is inbandSendTurn's
// ErrNoLiveChild retry and its resend-after path that carry turn 2 across the
// respawn, which is why those two behaviours are reused rather than simplified
// away.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -v -count=1 \
//	  -run TestInteractiveStream_InBandBypassRevoke ./internal/e2e/realclaude/
//
// It executes on any machine with claude credentials — the only skips are the
// package's standard absent-binary / absent-credentials guards. Read the count of
// tests that EXECUTED, never the exit code: a build failure and a full credentials
// skip both exit 0.
//
// # The other two arms in this file
//
// The header above is #1622's and describes the REVOCATION arm. Two later arms
// share its rig — seedBypassRegistry, revokeTap, newRevokeLogRecorder — and each
// carries its own doc block rather than extending this one, because each seeds a
// different posture and measures a different transition:
//
//   - TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows (#2064,
//     extended by #2065): the SPAWN-time write and the gate it arms.
//   - TestInteractiveStream_InBandBypassEscalate_LiveChildReportsBypassMode
//     (#2066): the default→bypass ESCALATION, this arm's mirror image. It is the
//     one measurement the hermetic suite cannot make, because fakeclaude echoes
//     any mode it is asked for.
//
// Run all three with -run 'TestInteractiveStream_' and the same flags.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	// A fresh EMPTY directory under the test's pinned $HOME, deliberately not a git
	// repo: less project context for claude to load, so the turns are cheaper. Same
	// reason as the model test's workdir.
	revokeWorkdirName = "inband-bypass-revoke-work"

	// The two prompts. They MUST NOT provoke a tool call, and that is load-bearing
	// rather than tidy: after the revocation the child is in `default` mode with no
	// --permission-prompt-tool wired and a Config.Stdout occupied by a recorder that
	// answers nothing, so a tool-using turn 2 would block on an approval that can
	// never arrive, burn inbandTurnBudget and die in inbandSendTurn complaining
	// about a missing result rather than about permissions. They are this file's own
	// consts rather than inbandPromptOne / inbandPromptTwo, whose file has no reason
	// to keep them tool-free.
	revokePromptOne = "Reply with the single word: one."
	revokePromptTwo = "Reply with the single word: two."
)

// revokeBaseArgs is EMPTY, and that is the whole point of AC 2. The model test's
// inbandBaseArgs is []string{"--dangerously-skip-permissions"}; copied here it lands
// in the SETTINGS-FREE base, which is what operatorBypass reads, so
// RunnerConfig.OperatorBypass comes out true — and since #2065 that means the daemon
// may not walk this child back at all, making the revocation unperformable rather
// than merely undone by a recompose. The bypass posture on this run comes from the
// seeded registry entry and from nowhere else, which is also what keeps the
// escalation traceable to exactly one place.
//
// No --model and no --max-turns either: the run is two one-word turns on claude's
// own machine default, and every extra flag is another thing the recompose has to
// be reasoned about.
var revokeBaseArgs []string

// revokeControlBudget is the wait for claude's ack of the revocation. #1595
// measured 45 s adequate for the same control request on the same claude version
// (its setModeControlBudget), long enough that an absent response means absence
// rather than impatience.
const revokeControlBudget = 45 * time.Second

// The two exact message literals newRevokeLogRecorder discriminates on. The
// second carries a `sessions: ` prefix, which the model test's spawn counter —
// discriminating on an exact match against "spawning claude" alone — drops on the
// floor. It is the record AC 3 exists to make readable.
const (
	revokeSpawnMessage        = "spawning claude"
	revokeNotDeliveredMessage = "sessions: in-band settings command not delivered"
)

// --- the stdout tap ----------------------------------------------------------

// revokeTap is the streamsup.Config.Stdout sink: the model test's line splitter in
// front of #1595's field capture. It adds a splitter and NOTHING else.
//
// The two halves exist already and neither can be used where the other lives.
// setModeRecorder.add takes a whole line — it was fed by a bufio.Scanner over a
// cmd.StdoutPipe() — so it is not an io.Writer and cannot sit in Config.Stdout;
// inbandTapRecorder.Write is precisely that splitter, but hard-wired to its own
// field set (init MODELS and a result count), which answers none of what AC 3
// asks for. Embedding gets every accessor promoted for free —
// snapshotControlResponses, controlResponseCount, snapshotInitModes, resultCount,
// snapshotLines, nonJSONCount — and the promoted resultCount is what makes
// *revokeTap satisfy inbandResultCounter with no adapter. Re-deriving the
// control_response classifier that already exists in this package is what the
// ticket forbids.
//
// LOCK ORDER IS OUTER → INNER, ALWAYS: Write holds splitMu and calls add, which
// takes setModeRecorder's own mutex. No path goes the other way. The outer field
// is named splitMu rather than mu so a reader is never guessing which one a
// selector resolves to.
//
// setModeRecorder retains every line verbatim in its `lines` slice, unboundedly.
// This ticket writes no fixture, so that retention is unused. It is ACCEPTED
// rather than trimmed: reusing add unmodified is worth more than the bytes, the
// volume is one short two-turn session (the same shape #1595 already ran through
// this type), and maxPartial still bounds the only accumulator an adversarial
// single line can grow.
type revokeTap struct {
	*setModeRecorder

	// splitMu guards the four splitter fields below and nothing else.
	splitMu sync.Mutex
	partial []byte
	// dropped counts accumulator discards at maxPartial. Reported via t.Logf; a
	// non-zero value does not fail the test but tells a reader the record of init
	// lines has a hole.
	dropped int
	// skipUntilNewline is set when the accumulator was discarded: the TAIL of that
	// over-long line is a fragment, not a line, and decoding it would be decoding
	// half a JSON object. Cleared at the next '\n'.
	skipUntilNewline bool

	maxPartial int
}

func newRevokeTap() *revokeTap {
	return &revokeTap{setModeRecorder: &setModeRecorder{}, maxPartial: inbandMaxPartial}
}

// Write NEVER returns a non-nil error: this writer is an exec.Cmd's stdout sink,
// and an error there aborts os/exec's copy and can wedge the child. The discipline
// is byte-for-byte inbandTapRecorder.Write's.
func (r *revokeTap) Write(b []byte) (int, error) {
	r.splitMu.Lock()
	defer r.splitMu.Unlock()

	r.partial = append(r.partial, b...)
	rest := r.partial
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		if r.skipUntilNewline {
			r.skipUntilNewline = false
		} else {
			r.add(rest[:i])
		}
		rest = rest[i+1:]
	}
	if len(rest) > r.maxPartial {
		r.dropped++
		r.skipUntilNewline = true
		rest = nil
	}
	// Copy so the (possibly large) backing array is released, mirroring
	// Parser.Write.
	r.partial = append([]byte(nil), rest...)
	return len(b), nil
}

func (r *revokeTap) droppedCount() int {
	r.splitMu.Lock()
	defer r.splitMu.Unlock()
	return r.dropped
}

// --- the log recorder --------------------------------------------------------

// revokeLogHandler is a slog.Handler that WRITES NOTHING, counts spawns, and
// retains the daemon's own account of a failed in-band delivery. The model test's
// newInbandSpawnCounter covers only the first half: it drops every record but
// "spawning claude", including deliverSettingsInBand's Info record, which is
// exactly what AC 3 exists to surface.
//
// Dropping every other record is also what keeps the runner's lifecycle lines out
// of the test output — a nil streamsup.Config.Logger would fall back to
// slog.Default(). Nothing it retains is a secret, and since #2042 that rests on
// one clause since #2043 took the no-mode RevokeBypass off the seam: the
// mode-carrying method this path now calls refuses an unsupported mode with a bare
// sentinel that does not echo the rejected string, and the pool logs the constant
// field name rather than the value. So there is no settings value the posture
// record could carry, which is the structural guarantee
// deliverSettingsInBand's doc already makes.
type revokeLogHandler struct {
	mu           *sync.Mutex
	spawns       *int
	notDelivered *[]string
}

// newRevokeLogRecorder returns the handler plus its two readers.
func newRevokeLogRecorder() (h slog.Handler, spawns func() int, notDelivered func() []string) {
	var (
		mu   sync.Mutex
		n    int
		recs []string
	)
	handler := revokeLogHandler{mu: &mu, spawns: &n, notDelivered: &recs}
	return handler, func() int {
			mu.Lock()
			defer mu.Unlock()
			return n
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), recs...)
		}
}

// Enabled returns true at every level: the target record is Info.
func (h revokeLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h revokeLogHandler) Handle(_ context.Context, rec slog.Record) error {
	switch rec.Message {
	case revokeSpawnMessage:
		h.mu.Lock()
		defer h.mu.Unlock()
		*h.spawns++
	case revokeNotDeliveredMessage:
		var b strings.Builder
		rec.Attrs(func(a slog.Attr) bool {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "%s=%v", a.Key, a.Value)
			return true
		})
		h.mu.Lock()
		defer h.mu.Unlock()
		*h.notDelivered = append(*h.notDelivered, b.String())
	}
	return nil
}

// WithAttrs and WithGroup return the receiver, mirroring inbandSpawnHandler. Safe
// because the pool installs cfg.Logger directly with no .With chain, and
// deliverSettingsInBand puts every attr inline on the record.
func (h revokeLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h revokeLogHandler) WithGroup(string) slog.Handler      { return h }

// --- the seeded registry -----------------------------------------------------

// revokeSeedEntry and revokeSeedFile mirror the json tags of internal/sessions'
// unexported registryEntry and registryFile. Only the keys the warm-start path
// reads are emitted: `label` decodes to empty and `lifecycle_state` is ignored for
// the bootstrap entry by construction (Pool.New forces stateActive there).
//
// YOLO carries NO omitempty, deliberately unlike registryEntry.YOLO. The tag is
// what puts a false posture on disk as a PRESENT key rather than an absent one,
// so the file itself says which posture was asked for — see seedBypassRegistry
// for why that distinction is the whole point. "Fixing" the asymmetry to match
// production makes the key vanish, and the only assertion that reddens on it is
// the presence clause in
// TestSeedBypassRegistry_StoresRequestedPostureUnderBothValues: a value-only
// check stays green, because an absent key decodes to exactly the false that
// posture wanted.
type revokeSeedEntry struct {
	ID           sessions.SessionID `json:"id"`
	CreatedAt    time.Time          `json:"created_at"`
	LastActiveAt time.Time          `json:"last_active_at"`
	Bootstrap    bool               `json:"bootstrap"`
	YOLO         bool               `json:"yolo"`
}

type revokeSeedFile struct {
	Version  int               `json:"version"`
	Sessions []revokeSeedEntry `json:"sessions"`
}

// seedBypassRegistry writes a one-entry sessions.json whose bootstrap entry
// carries the yolo posture the CALLER chooses, and returns the id it minted. That
// posture is the launch posture: Pool.New lifts it off the entry via
// pickBootstrap, canonicalises it, and claudeSettingsArgs turns the stored posture
// into the argv suffix. Since #2065 that suffix carries
// --dangerously-skip-permissions on EVERY arm and adds --permission-mode default for
// a false — the flag no longer says which posture the child runs in, only that the
// daemon composed one, and the posture is decided by a spawn-time in-band write.
//
// It MUST run before sessions.New: the model test points RegistryPath at a fresh
// t.TempDir() path that does not exist, which is the COLD-start shape —
// loadRegistry returns (nil, nil) and settings come out zero-valued, i.e. no
// bypass. That is also why a false written here is a STORED false and not the
// same thing as no file: both reach the Pool as YOLO false, and only the ENTRY'S
// EXISTENCE — `bootstrap` true and an id ValidID accepts — separates them. Pool
// state cannot tell them apart, so
// TestSeedBypassRegistry_StoresRequestedPostureUnderBothValues reads the file.
//
// Hand it a FRESH path. os.WriteFile applies its permission argument only when it
// CREATES the file; on a path that already exists it truncates and keeps the mode
// that was there, so a caller re-seeding one path across several arms silently
// inherits whatever the first write left.
//
// For the bypass posture, each of the three ways this seed can silently fail
// lands on instrument check B, before a single token is spent: `bootstrap`
// missing → pickBootstrap returns nil → cold start → YOLO false; a misspelled key
// → decodes to false; a MALFORMED yolo value → the whole loadRegistry parse fails
// closed and sessions.New errors. All three land on the test named above too, on
// BOTH postures and with no claude binary and no credentials at all.
//
// The id comes from sessions.NewID rather than a hand-written string:
// writeMCPSettings gates the warm-start id on ValidID and hard-errors on anything
// that is not a canonical UUIDv4, and claude receives it as --session-id. 0600
// because it is the same shape saveRegistryLocked writes.
func seedBypassRegistry(t *testing.T, path string, yolo bool) sessions.SessionID {
	t.Helper()
	id, err := sessions.NewID()
	if err != nil {
		t.Fatalf("#1622: mint bootstrap session id: %v", err)
	}
	now := time.Now().UTC()
	data, err := json.MarshalIndent(revokeSeedFile{
		Version: 1,
		Sessions: []revokeSeedEntry{{
			ID:           id,
			CreatedAt:    now,
			LastActiveAt: now,
			Bootstrap:    true,
			YOLO:         yolo,
		}},
	}, "", "  ")
	if err != nil {
		t.Fatalf("#1622: marshal seeded registry: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("#1622: write seeded registry %s: %v", path, err)
	}
	return id
}

// --- the test ----------------------------------------------------------------

// TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode drives one
// live claude session, launched in bypass from its STORED settings, through a
// turn, a bypass revocation delivered by the daemon's own Pool.UpdateSettings, and
// a further turn — then asserts from claude's own control_response and per-turn
// system/init announcement that the revocation reached the child, and that the
// same process served both turns.
func TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials

	workdir := filepath.Join(home, revokeWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#1622: create workdir: %v", err)
	}

	// BEFORE sessions.New — the whole warm-start seam depends on the file existing
	// at construction time.
	registryPath := filepath.Join(t.TempDir(), "sessions.json")
	seededID := seedBypassRegistry(t, registryPath, true)

	rec := newRevokeTap()
	logHandler, spawns, notDelivered := newRevokeLogRecorder()

	// The factory captures the concrete runner so the test body can read Stdin() and
	// ChildPID off the very object the pool will deliver the revocation to. It is
	// the one place Stdout is set, and it is set to the tap.
	var tap inbandRunner
	factory := func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
		r, err := streamsup.New(streamsup.Config{
			ClaudeBin: cfg.ClaudeBin,
			WorkDir:   cfg.WorkDir,
			SessionID: cfg.SessionID,
			Args:      cfg.ClaudeArgs,
			Stdout:    rec,
			Logger:    cfg.Logger,
		})
		if err != nil {
			return nil, fmt.Errorf("#1622: stream runner: %w", err)
		}
		tap = inbandRunner{Runner: r}
		return tap, nil
	}

	// Pool.Run is deliberately NOT called, for the reason the model test records:
	// the session lifecycle goroutine's only job here would be `go s.sup.Run(subCtx)`,
	// which the test does directly below, and skipping it also skips the
	// conversations sweep, the idle timer and the settings-file reaper.
	// UpdateSettings consults none of them.
	pool, err := sessions.New(sessions.Config{
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:  claudeBin,
			WorkDir:    workdir,
			ClaudeArgs: revokeBaseArgs,
		},
		RegistryPath:  registryPath,
		RunnerFactory: factory,
		Logger:        slog.New(logHandler),
	})
	if err != nil {
		t.Fatalf("#1622: sessions.New: %v", err)
	}

	// Instrument check A — the runner the test drives must be the runner
	// UpdateSettings will deliver the revocation to. Without it, a future refactor
	// that hands the pool a different runner would leave every assertion below
	// reading a bystander.
	sup := pool.Default().Runner()
	if sup != sessions.Runner(tap) {
		t.Fatalf("#1622: pool.Default().Runner() is not the runner the test captured; "+
			"the tap observes a different child than UpdateSettings drives (%T vs %T)", sup, tap)
	}

	// Instrument check B — the deterministic half of AC 2, and it fires BEFORE any
	// child is spawned, so every seed failure mode costs zero tokens.
	settings, ok := pool.DefaultSettings()
	if !ok || !settings.YOLO {
		t.Fatalf("#1622: pool.DefaultSettings() = %+v (ok=%v), want YOLO true: the registry "+
			"seeded at %s with bootstrap id %s did not reach the pool, so the child would "+
			"launch WITHOUT bypass and the run would measure a child nobody revoked",
			settings, ok, registryPath, seededID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = tap.Run(ctx)
	}()
	// Registered immediately after the Run goroutine starts, so a t.Fatalf anywhere
	// in the drive sequence still tears the (bypass-launched) child down.
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(inbandRunExitWait):
			t.Errorf("#1622: streamsup.Run did not return within %s of cancel", inbandRunExitWait)
		}
	})

	if !inbandWaitForChild(tap) {
		t.Fatalf("#1622: no live child within %s: claude never spawned", inbandSpawnWait)
	}
	pidBefore := tap.State().ChildPID
	if pidBefore == 0 {
		t.Fatalf("#1622: ChildPID is 0 with a live stdin handle; the instrument cannot answer AC 4")
	}

	inbandSendTurn(t, sup, rec, revokePromptOne)

	// Instrument check C — the LIVE half of AC 2. It sits BEFORE the revocation so
	// its red cannot be confused with a delivery failure: an empty or non-bypass
	// first init line means the child was never in bypass, so the seam did not take
	// and there is nothing to revoke.
	modes := rec.snapshotInitModes()
	if len(modes) == 0 || modes[0] != "bypassPermissions" {
		t.Fatalf("#1622: first init.permissionMode is %q (all: %q), want \"bypassPermissions\"; "+
			"the child did not launch in bypass despite a seeded yolo:true, so there is "+
			"nothing for the revocation to change", firstOrEmpty(modes), modes)
	}

	// The correlation baseline. Nothing else on this run issues a control request,
	// so this is expected to be 0 — but it is READ rather than assumed, because the
	// verdict is "strictly greater after", not "exactly one".
	baseline := rec.controlResponseCount()

	// YOLO ONLY. A non-nil Model or Effort would add a /model or /effort turn and,
	// if empty, would route the whole frame onto the restart path
	// (inBandDeliverable) — silently making this test measure the mechanism it
	// exists to distinguish itself from. The stored YOLO is true (instrument check
	// B), so this is a real change and UpdateSettings does not take its no-op early
	// return.
	no := false
	if err := pool.UpdateSettings(pool.Default().ID(), sessions.SettingsUpdate{YOLO: &no}); err != nil {
		t.Fatalf("#1622: UpdateSettings(YOLO=false): %v", err)
	}

	// The revocation is a CONTROL REQUEST, not a turn: it produces no result line,
	// so the settle waits on the ack rather than on a turn boundary. This is the ONE
	// wait whose timeout is tolerated — a tree that does not deliver produces no
	// response at all, and the run has to reach its assertions rather than dying
	// here.
	if !setModeWaitFor(rec.controlResponseCount, baseline+1, revokeControlBudget) {
		t.Logf("#1622: no control_response within %s of the revocation — expected on a tree "+
			"that does not deliver in-band; continuing to the assertions", revokeControlBudget)
	}

	inbandSendTurn(t, sup, rec, revokePromptTwo)

	modes = rec.snapshotInitModes()
	responses := rec.snapshotControlResponses()
	undelivered := notDelivered()
	spawnCount := spawns()
	pidAfter := tap.State().ChildPID
	t.Logf("#1622: init permissionModes %q, control_responses %d (baseline %d), spawns %d, "+
		"pid %d -> %d, results %d, dropped partials %d, non-JSON lines %d, "+
		"not-delivered records %d",
		modes, len(responses), baseline, spawnCount, pidBefore, pidAfter,
		rec.resultCount(), rec.droppedCount(), rec.nonJSONCount(), len(undelivered))
	for i, resp := range responses {
		t.Logf("#1622: control_response[%d] verbatim: %s", i, resp)
	}
	// deliverSettingsInBand's fire-and-forget swallow, made readable. It is LOGGED,
	// never asserted: its only reachable non-empty case already reddens A1, so an
	// assertion here would be a second red for one event.
	for i, line := range undelivered {
		t.Logf("#1622: deliverSettingsInBand did not deliver [%d]: %s", i, line)
	}

	if len(responses) <= baseline {
		t.Errorf("A1: %d control_response(s) before the revocation and %d after; claude never "+
			"answered the set_permission_mode request, so nothing shows the revocation "+
			"reached the live child", baseline, len(responses))
	}
	// FIRST and LAST, never a fixed index: a resent turn adds an init line and
	// changes no verdict, so do not assert len(modes) == 2. Instrument check C
	// already fixed the first at "bypassPermissions" and snapshotInitModes only ever
	// appends, so this index needs no second guard.
	last := modes[len(modes)-1]
	if last != "default" {
		t.Errorf("A2: the child reported init.permissionMode %q after the revocation, want "+
			"%q; the bypassPermissions -> default move claude echoes did not happen "+
			"(first %q, last %q, all %q)", last, "default", modes[0], last, modes)
	}
	if pidAfter != pidBefore {
		t.Errorf("A3: child pid %d served the first turn but %d served the last; "+
			"the child was torn down across the revocation", pidBefore, pidAfter)
	}
	if spawnCount != 1 {
		t.Errorf("A4: %d spawns over the whole run, want exactly 1; "+
			"the revocation respawned the child", spawnCount)
	}
}

// firstOrEmpty is instrument check C's message helper: it reports the first
// element, or "" for an empty slice, so the two failure modes that guard covers
// (no init line at all, and an init line reporting a non-bypass posture) can share
// one message without indexing an empty slice.
func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// --- #2064: the spawn-time posture gate, on the daemon's own spawn path --------

// TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows is #2064 AC 5,
// extended by #2065 AC 5 (see "What #2065 added" below — its two assertions are
// written but NOT yet measured against a live claude). It
// drives one live claude session whose STORED posture is the default one — the
// posture that arms the gate, unlike the bypass session the test above measures — and
// proves the gate OPENS: the daemon writes its stored posture to the child it just
// spawned, claude acks the daemon's OWN minted request_id, and a user turn completes.
//
// # Why a completed turn is the assertion
//
// With the gate armed, WriteUserTurn refuses with the retryable no-live-child error
// until the ack for THAT child's minted id lands. So a turn that completes is a
// released gate — there is no other way for those bytes to reach claude. That makes
// A2 below the strongest single assertion available here, and it is why this test does
// not reach for a private counter or a hand-built control line: it measures the
// production path end to end or it measures nothing.
//
// # Why it extends this rig rather than authoring a second one
//
// Everything it needs already exists here. seedBypassRegistry takes a `yolo bool`, so
// the one posture that arms the gate needs no new seeder — just `false`. revokeTap
// records the control_responses verbatim, newRevokeLogRecorder counts spawns, and
// inbandSendTurn drives a turn against a result-count oracle. The ONE thing the rig
// lacks is a release side: this file's factory installs the tap in Config.Stdout, and
// the tap is not a parser, so nothing would ever open the gate. A real
// streamsup.Parser is therefore teed in beside the tap and its own gate handed to the
// runner — which is exactly the production wiring newStreamRunnerFactory installs,
// where the parser mints the gate and the runner arms it.
//
// # What this does NOT claim
//
// That claude ENFORCES the acked posture. An echoed mode is the child's report of its
// own posture, the same scope boundary this file's header draws for the revocation it
// measures. What is proven is that the round trip closed on the daemon's own id.
//
// # Which assertion carries which proof
//
//   - A1 is the correlation: a control_response arrived at all. Without it A2 could
//     in principle pass on a tree where the gate was never armed.
//   - A2 is the gate opening, and it is the sole red for a broken correlation — a
//     mismatched id, a subtype check that never passes, a parser and runner holding
//     different gates. Under any of those inbandSendTurn dies waiting for a result
//     that the refused turn can never produce.
//   - A3 is the second confirmation #1595 and #2041 both read: the next turn's
//     init.permissionMode echo. Logged and asserted as `default`, the stored posture.
//   - A4 pins that this happened on ONE child, so no respawn is doing the work.
//
// # What #2065 added, and what is NOT yet measured
//
// #2065 made sessions.claudeSettingsArgs append --dangerously-skip-permissions to
// every argv, so the child this test spawns now LAUNCHES IN BYPASS and is walked
// back to its stored posture in-band. Two things were added for that ticket's AC 5:
// an instrument check that the pool-composed argv really carries the flag (without
// it every assertion here would pass for the pre-#2065 reason, measuring nothing),
// and A5, which reads the FIRST init line rather than the last.
//
// A5 is #2060's race-window verdict re-proven on the daemon's own spawn path. That
// ticket measured 3/3 consecutive spawns reporting the downgraded posture at their
// own first system/init line, on HAND-DRIVEN argvs; the claim that has to hold now
// is the same one where the argv is composed by the daemon.
//
// **THOSE TWO ASSERTIONS HAVE NOT BEEN EXECUTED AGAINST A LIVE CLAUDE.** The
// dispatch environment carries no Claude login, so this test SKIPS there and a skip
// exits 0. #2065 was built and verified with every hermetic gate green and this arm
// skipped; it needs an operator's `make e2e-realclaude` run to become a
// measurement. Until then, treat A5 as written-and-unproven, and read the count of
// `=== RUN` lines that reported PASS — never the exit code, and never the presence
// of the transcript below.
//
// If a live run finds modes[0] reporting the escalation, the window is REACHABLE on
// the daemon's spawn path. Do not ship a mitigation: comment the finding on #2065
// and apply needs-rework:refiner. A narrowed window is still a window, and a daemon
// that sometimes starts a child in bypass is worse than one that sometimes needs a
// respawn.
//
// # Measured 2026-09-03 for #2064, claude on the operator's Max plan — EXECUTED
//
// This transcript is from the PRE-#2065 tree: that child's argv carried no
// escalation flag, so it records the gate opening and nothing about the always-on
// flag. It is kept because A1-A4 are unchanged and it is their evidence.
//
// One `=== RUN` line, `--- PASS`, 3.72 s, one turn's tokens spent:
//
//	control_responses 1, init permissionModes ["default"], spawns 1,
//	pid 9000 -> 9000, results 1, dropped partials 0, non-JSON lines 0,
//	not-delivered records 0
//	control_response verbatim:
//	  {"type":"control_response","response":{"subtype":"success",
//	   "request_id":"1","response":{"mode":"default"}}}
//
// Two things in that ack are worth reading rather than skimming. `request_id` is "1",
// the daemon's OWN minted id off nextControlID, so the correlation closed on a value
// this process chose — the thing the sibling test above records as impossible to
// assert before this ticket existed. And `response.mode` is `default`, the stored
// posture, which is the ack agreeing with A3's init echo from a second direction.
// The id is logged as an OBSERVATION and deliberately not asserted: pinning a private
// counter's start value would make an implementation detail a test contract, exactly
// as that sibling's header argues.
//
// Read the count of `=== RUN` lines to know this executed; the exit code cannot tell a
// skipped run from a passing one — with no credentials every test here skips and the
// suite still exits 0.
func TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials

	workdir := filepath.Join(home, revokeWorkdirName+"-2064")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2064: create workdir: %v", err)
	}

	// yolo:false — the DEFAULT posture, which is what arms the gate. The sibling
	// test's yolo:true seeds bypassPermissions, which the runner's allow-list refuses
	// by non-membership, so that session is sent nothing and never gated at all.
	registryPath := filepath.Join(t.TempDir(), "sessions.json")
	seededID := seedBypassRegistry(t, registryPath, false)

	rec := newRevokeTap()
	logHandler, spawns, notDelivered := newRevokeLogRecorder()

	var tap inbandRunner
	var composedArgs []string
	var composedOperatorBypass bool
	factory := func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
		// #2065's instrument capture. The pool composed this argv, so it is the only
		// place in the run that can show the always-on escalation flag actually
		// reached a daemon-spawned child. Asserted after Pool.New returns, not here,
		// so a mismatch is a test failure rather than a factory error.
		composedArgs = slices.Clone(cfg.ClaudeArgs)
		composedOperatorBypass = cfg.OperatorBypass
		// The parser is the gate's minter, exactly as in production. It is teed in
		// beside the tap rather than replacing it: the tap is what makes the ack and
		// the init modes readable, and the parser is what RELEASES the gate. Its sink
		// is a no-op — this test reads claude's raw lines through the tap, not events.
		parser := streamsup.NewParser(func(turnevent.Event) {}, cfg.Logger)
		r, err := streamsup.New(streamsup.Config{
			ClaudeBin: cfg.ClaudeBin,
			WorkDir:   cfg.WorkDir,
			SessionID: cfg.SessionID,
			Args:      cfg.ClaudeArgs,
			Stdout:    io.MultiWriter(rec, parser),
			Logger:    cfg.Logger,
			// Read off the RunnerConfig rather than hard-coded, so the pool's own
			// plumbing of the stored posture and of #2065's provenance bit is part of
			// what this run measures.
			SpawnPermissionMode: cfg.PermissionMode,
			OperatorBypass:      cfg.OperatorBypass,
			PostureGate:         parser.PostureGate(),
		})
		if err != nil {
			return nil, fmt.Errorf("#2064: stream runner: %w", err)
		}
		tap = inbandRunner{Runner: r}
		return tap, nil
	}

	pool, err := sessions.New(sessions.Config{
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:  claudeBin,
			WorkDir:    workdir,
			ClaudeArgs: revokeBaseArgs,
		},
		RegistryPath:  registryPath,
		RunnerFactory: factory,
		Logger:        slog.New(logHandler),
	})
	if err != nil {
		t.Fatalf("#2064: sessions.New: %v", err)
	}

	sup := pool.Default().Runner()
	if sup != sessions.Runner(tap) {
		t.Fatalf("#2064: pool.Default().Runner() is not the runner the test captured (%T vs %T)", sup, tap)
	}

	// Instrument check — the deterministic half, BEFORE any child is spawned, so a
	// seed that did not reach the pool costs zero tokens. A bypass posture here would
	// mean the runner writes nothing and the gate is never armed, leaving every
	// assertion below passing against a session this ticket does not gate.
	settings, ok := pool.DefaultSettings()
	if !ok || settings.YOLO || settings.PermissionMode != "default" {
		t.Fatalf("#2064: pool.DefaultSettings() = %+v (ok=%v), want the default posture: the "+
			"registry seeded at %s with bootstrap id %s did not reach the pool, so the gate "+
			"would never be armed and this run would measure nothing",
			settings, ok, registryPath, seededID)
	}

	// #2065's instrument check, and the one that makes A5 below non-vacuous. On a
	// tree where the escalation flag never went unconditional, this child launches
	// WITHOUT it and reports "default" at its first init line for the pre-#2065
	// reason — every assertion here would pass while measuring nothing new. Also
	// deterministic and pre-spawn, so a tree that fails it costs zero tokens.
	t.Logf("#2065: pool-composed argv %q (OperatorBypass=%v)", composedArgs, composedOperatorBypass)
	if !slices.Contains(composedArgs, "--dangerously-skip-permissions") {
		t.Fatalf("#2065: the argv the pool composed is %q, which carries no "+
			"--dangerously-skip-permissions: this daemon does not launch its children in bypass, "+
			"so nothing below measures the always-on flag", composedArgs)
	}
	// revokeBaseArgs is empty, so the escalation on that argv can only have come from
	// claudeSettingsArgs. A true here would mean the daemon must NOT walk this child
	// back, and the gate would never arm — the run would measure nothing.
	if composedOperatorBypass {
		t.Fatalf("#2065: OperatorBypass is true for a daemon whose bootstrap ClaudeArgs are %q; "+
			"the provenance bit was derived from the assembled argv rather than the settings-free "+
			"base, so no child is ever written its stored posture", revokeBaseArgs)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = tap.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(inbandRunExitWait):
			t.Errorf("#2064: streamsup.Run did not return within %s of cancel", inbandRunExitWait)
		}
	})

	if !inbandWaitForChild(tap) {
		t.Fatalf("#2064: no live child within %s: claude never spawned", inbandSpawnWait)
	}
	pidBefore := tap.State().ChildPID

	// A2's driver. On a tree whose correlation is broken this call is where the run
	// dies: the gate never opens, every WriteUserTurn refuses, and no result arrives.
	inbandSendTurn(t, sup, rec, revokePromptOne)

	responses := rec.snapshotControlResponses()
	modes := rec.snapshotInitModes()
	pidAfter := tap.State().ChildPID
	spawnCount := spawns()
	t.Logf("#2064: control_responses %d, init permissionModes %q, spawns %d, pid %d -> %d, "+
		"results %d, dropped partials %d, non-JSON lines %d, not-delivered records %d",
		len(responses), modes, spawnCount, pidBefore, pidAfter,
		rec.resultCount(), rec.droppedCount(), rec.nonJSONCount(), len(notDelivered()))
	for i, resp := range responses {
		t.Logf("#2064: control_response[%d] verbatim: %s", i, resp)
	}

	if len(responses) == 0 {
		t.Errorf("A1: claude answered no control_request at all, so nothing shows the "+
			"spawn-time set_permission_mode write reached the child (pid %d)", pidBefore)
	}
	// A2 is carried by inbandSendTurn above: it fatals when no result arrives, and
	// with the gate armed a completed turn IS the released gate. Restated here rather
	// than left implicit, because a reader looking for "the gate opened" would
	// otherwise find no assertion naming it.
	if rec.resultCount() == 0 {
		t.Errorf("A2: no result line for the turn, so the posture gate never opened and " +
			"claude never saw it")
	}
	if len(modes) == 0 || modes[len(modes)-1] != "default" {
		t.Errorf("A3: the child reported init.permissionMode %q (all %q), want %q — the "+
			"second confirmation that the acked posture is the stored one",
			firstOrEmpty(modes), modes, "default")
	}
	if spawnCount != 1 {
		t.Errorf("A4: %d spawns over the whole run, want exactly 1; the gate must open on "+
			"the child that was spawned, not on a replacement", spawnCount)
	}
	// A5 is #2065's own: the FIRST init line, not the last. #2060 measured the
	// launch→downgrade window closed on hand-driven argvs — 3/3 spawns reported the
	// downgraded posture at their own first system/init line, i.e. before claude
	// published an opening posture at all. This re-proves that verdict where the argv
	// is composed by the daemon rather than by hand, which is where it now has to
	// hold: every child this daemon spawns launches in bypass.
	//
	// A3 above cannot carry it. It reads the LAST init line, so it would stay green
	// on a child that opened in bypassPermissions and was walked back afterwards —
	// which is exactly the window this assertion exists to rule out.
	if len(modes) == 0 || modes[0] != "default" {
		t.Errorf("A5: the child's FIRST init.permissionMode is %q (all %q), want %q. Its argv "+
			"carried --dangerously-skip-permissions, so a first line reporting the escalation "+
			"means the launch→downgrade window is REACHABLE on the daemon's own spawn path. "+
			"Do not mitigate: comment the finding on #2065, apply needs-rework:refiner, and "+
			"leave the current shape alone (a narrowed window is still a window)",
			firstOrEmpty(modes), modes, "default")
	}
}

// --- #2066: the escalation on the daemon's own routing path -------------------

// TestInteractiveStream_InBandBypassEscalate_LiveChildReportsBypassMode is #2066
// AC 5, and it is the mirror image of this file's first test: that one drives a
// bypass→default REVOCATION in band, this one drives a default→bypass ESCALATION,
// which until #2066 was the one posture transition that could only be granted by
// killing the child and relaunching it under a recomposed argv.
//
// # Why a live run is required at all
//
// The hermetic tests prove the daemon no longer respawns and that it writes the
// escalation, in both spellings, with a spawn count that does not move. They cannot
// prove the escalation TAKES: fakeclaude echoes whatever mode it is asked for
// (writeSetPermissionModeAck's doc records that as deliberate), so a green hermetic
// suite is equally consistent with claude refusing the request in words — which is
// exactly what it did before #2065 put --dangerously-skip-permissions on every
// launch argv. The two halves are one proof and neither is sufficient alone.
//
// # Why the full gate is wired here and not in the revocation arm
//
// The revocation arm leaves SpawnPermissionMode and PostureGate unset, because its
// session is seeded yolo:true and there is nothing to walk back. This arm is seeded
// yolo:false, so without the spawn-time write its child would simply STAY in the
// bypass its always-on launch flag put it in, the first init line would report
// bypassPermissions, and there would be no default posture to escalate FROM. The
// wiring is copied from the #2064 arm above: a real streamsup.Parser teed in beside
// the tap, minting the gate the runner arms — the production shape.
//
// # What the second turn proves that no assertion can state directly
//
// (*streamsup.Runner).SetPermissionMode RETARGETS the posture gate after a
// successful write (#2064), and until #2066 an escalation never reached that
// method. So the gate CLOSES on the escalation's own request id and every turn is
// refused until claude acks it. Turn 2 completing is therefore the ack, measured
// through the production path rather than read off a counter — the same argument
// the #2064 arm makes for its A2.
//
// The cost of that is stated rather than hidden: if claude NAKs the escalation, this
// run dies inside inbandSendTurn waiting for a result that a refused turn can never
// produce, and the control_response log below is what tells a reader whether the
// answer was a refusal or silence. #2060 measured claude ACCEPTING the escalation on
// a flag-launched child at 2.1.239 and #2065 makes every child flag-launched, so a
// NAK here would falsify this ticket's premise: comment the finding on #2066 rather
// than mitigating it, because a daemon that closes a gate no write can reopen is
// worse than one that needs a respawn.
//
// # The spelling this arm drives
//
// yolo:true, the ONLY spelling a client can put on the wire — internal/relay's
// validPermissionMode refuses bypassPermissions as a mode string on purpose. The
// mode spelling reaches the same delivery through Pool.UpdateSettings and is pinned
// hermetically by TestPool_UpdateSettings_InBand_Escalation_NoRespawn, which runs
// both. Spending a second live session on the second spelling would measure claude
// twice and the daemon not at all.
//
// # Which assertion carries which proof
//
//   - Instrument check C fixes the FIRST init line at `default`, so the child is
//     provably not already escalated when the enable is sent. Without it every
//     assertion below passes on a tree where the spawn-time downgrade never ran.
//   - A1 is the correlation: a control_response arrived after the enable.
//   - A2 is the escalation TAKING — the child's own init echo reporting
//     bypassPermissions. This is the assertion the hermetic suite cannot make.
//   - A3 and A4 are the no-respawn half: one pid, one spawn, across both turns.
//
// **NOT YET EXECUTED AGAINST A LIVE CLAUDE.** The dispatch environment carries no
// Claude login, so this test SKIPS there and a skip exits 0. Read the count of
// `=== RUN` lines that reported PASS — never the exit code.
func TestInteractiveStream_InBandBypassEscalate_LiveChildReportsBypassMode(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials

	workdir := filepath.Join(home, revokeWorkdirName+"-2066")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2066: create workdir: %v", err)
	}

	// yolo:false — the posture this arm escalates AWAY from, and the one that arms
	// the spawn gate so the child is walked back to `default` before turn 1.
	registryPath := filepath.Join(t.TempDir(), "sessions.json")
	seededID := seedBypassRegistry(t, registryPath, false)

	rec := newRevokeTap()
	logHandler, spawns, notDelivered := newRevokeLogRecorder()

	var tap inbandRunner
	var composedArgs []string
	var composedOperatorBypass bool
	factory := func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
		composedArgs = slices.Clone(cfg.ClaudeArgs)
		composedOperatorBypass = cfg.OperatorBypass
		parser := streamsup.NewParser(func(turnevent.Event) {}, cfg.Logger)
		r, err := streamsup.New(streamsup.Config{
			ClaudeBin:           cfg.ClaudeBin,
			WorkDir:             cfg.WorkDir,
			SessionID:           cfg.SessionID,
			Args:                cfg.ClaudeArgs,
			Stdout:              io.MultiWriter(rec, parser),
			Logger:              cfg.Logger,
			SpawnPermissionMode: cfg.PermissionMode,
			OperatorBypass:      cfg.OperatorBypass,
			PostureGate:         parser.PostureGate(),
		})
		if err != nil {
			return nil, fmt.Errorf("#2066: stream runner: %w", err)
		}
		tap = inbandRunner{Runner: r}
		return tap, nil
	}

	pool, err := sessions.New(sessions.Config{
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:  claudeBin,
			WorkDir:    workdir,
			ClaudeArgs: revokeBaseArgs,
		},
		RegistryPath:  registryPath,
		RunnerFactory: factory,
		Logger:        slog.New(logHandler),
	})
	if err != nil {
		t.Fatalf("#2066: sessions.New: %v", err)
	}

	// Instrument check A — the runner the test drives must be the runner
	// UpdateSettings will deliver the escalation to.
	sup := pool.Default().Runner()
	if sup != sessions.Runner(tap) {
		t.Fatalf("#2066: pool.Default().Runner() is not the runner the test captured; "+
			"the tap observes a different child than UpdateSettings drives (%T vs %T)", sup, tap)
	}

	// Instrument check B — deterministic and pre-spawn, so a seed that did not reach
	// the pool costs zero tokens. A stored bypass here would mean the enable below is
	// a no-op update that returns before the live-apply.
	settings, ok := pool.DefaultSettings()
	if !ok || settings.YOLO || settings.PermissionMode != "default" {
		t.Fatalf("#2066: pool.DefaultSettings() = %+v (ok=%v), want the default posture: the "+
			"registry seeded at %s with bootstrap id %s did not reach the pool, so there is "+
			"nothing for the escalation to change", settings, ok, registryPath, seededID)
	}
	if composedOperatorBypass {
		t.Fatalf("#2066: OperatorBypass is true for a daemon whose bootstrap ClaudeArgs are %q; "+
			"the daemon would refuse to walk this child back and turn 1 would run in bypass, "+
			"so the escalation would change nothing observable", revokeBaseArgs)
	}
	t.Logf("#2066: pool-composed argv %q (OperatorBypass=%v)", composedArgs, composedOperatorBypass)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = tap.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(inbandRunExitWait):
			t.Errorf("#2066: streamsup.Run did not return within %s of cancel", inbandRunExitWait)
		}
	})

	if !inbandWaitForChild(tap) {
		t.Fatalf("#2066: no live child within %s: claude never spawned", inbandSpawnWait)
	}
	pidBefore := tap.State().ChildPID
	if pidBefore == 0 {
		t.Fatalf("#2066: ChildPID is 0 with a live stdin handle; the instrument cannot answer A3")
	}

	inbandSendTurn(t, sup, rec, revokePromptOne)

	// Instrument check C — the LIVE half of the baseline, BEFORE the enable, so its
	// red cannot be confused with a delivery failure. A first init line reporting
	// anything but `default` means the spawn-time downgrade did not take and this run
	// would be escalating a child that was already escalated.
	modes := rec.snapshotInitModes()
	if len(modes) == 0 || modes[0] != "default" {
		t.Fatalf("#2066: first init.permissionMode is %q (all: %q), want %q; the child was "+
			"not walked back from its always-on launch flag, so there is no default posture "+
			"for the escalation to move away from", firstOrEmpty(modes), modes, "default")
	}

	// The correlation baseline. The spawn-time write already produced one ack, so
	// this is expected to be 1 — read rather than assumed, because the verdict is
	// "strictly greater after".
	baseline := rec.controlResponseCount()

	// YOLO ONLY, and true. A non-nil Model or Effort would add a /model or /effort
	// turn and, if empty, would route the whole frame onto the RESTART path
	// (inBandDeliverable's empty-value reject still outranks a posture change) —
	// silently making this test measure the mechanism it exists to replace.
	yes := true
	if err := pool.UpdateSettings(pool.Default().ID(), sessions.SettingsUpdate{YOLO: &yes}); err != nil {
		t.Fatalf("#2066: UpdateSettings(YOLO=true): %v", err)
	}

	// The escalation is a CONTROL REQUEST, not a turn: it produces no result line, so
	// the settle waits on the ack. The timeout is TOLERATED here and only here — a
	// tree that does not deliver produces no response at all, and the run has to reach
	// the assertions rather than dying on this wait.
	if !setModeWaitFor(rec.controlResponseCount, baseline+1, revokeControlBudget) {
		t.Logf("#2066: no control_response within %s of the escalation — expected on a tree "+
			"that does not deliver in-band; continuing to the assertions", revokeControlBudget)
	}

	// Turn 2 is also the gate assertion: SetPermissionMode retargeted the gate on the
	// escalation's own id, so these bytes reach claude only if it acked.
	inbandSendTurn(t, sup, rec, revokePromptTwo)

	modes = rec.snapshotInitModes()
	responses := rec.snapshotControlResponses()
	undelivered := notDelivered()
	spawnCount := spawns()
	pidAfter := tap.State().ChildPID
	t.Logf("#2066: init permissionModes %q, control_responses %d (baseline %d), spawns %d, "+
		"pid %d -> %d, results %d, dropped partials %d, non-JSON lines %d, "+
		"not-delivered records %d",
		modes, len(responses), baseline, spawnCount, pidBefore, pidAfter,
		rec.resultCount(), rec.droppedCount(), rec.nonJSONCount(), len(undelivered))
	for i, resp := range responses {
		t.Logf("#2066: control_response[%d] verbatim: %s", i, resp)
	}
	for i, line := range undelivered {
		t.Logf("#2066: deliverSettingsInBand did not deliver [%d]: %s", i, line)
	}

	if len(responses) <= baseline {
		t.Errorf("A1: %d control_response(s) before the escalation and %d after; claude never "+
			"answered the set_permission_mode request, so nothing shows the escalation "+
			"reached the live child", baseline, len(responses))
	}
	// A2 is the one the hermetic suite cannot make. FIRST and LAST, never a fixed
	// index: a resent turn adds an init line and changes no verdict. Instrument check
	// C already fixed the first at "default" and snapshotInitModes only appends.
	last := modes[len(modes)-1]
	if last != "bypassPermissions" {
		t.Errorf("A2: the child reported init.permissionMode %q after the enable, want %q; "+
			"the default -> bypassPermissions move did not happen, so the daemon wrote a line "+
			"and claude did not take it (first %q, last %q, all %q)",
			last, "bypassPermissions", modes[0], last, modes)
	}
	if pidAfter != pidBefore {
		t.Errorf("A3: child pid %d served the first turn but %d served the last; "+
			"the child was torn down across the escalation", pidBefore, pidAfter)
	}
	if spawnCount != 1 {
		t.Errorf("A4: %d spawns over the whole run, want exactly 1; "+
			"the escalation respawned the child", spawnCount)
	}
}
