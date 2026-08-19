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
// Pool.deliverSettingsInBand → Runner.RevokeBypass — and proved it hermetically
// through a fake runner. That proves the daemon ASKS. Nothing proved pyry emits
// those bytes, on the stream it already holds open, to a real claude.
//
// The gap matters because the failure is silent in both directions. A child that
// keeps auto-approving after a revocation is indistinguishable in the daemon log
// from one that was never in bypass; and delivery is fire-and-forget —
// deliverSettingsInBand logs a failed RevokeBypass at Info and swallows it, so
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
// pickBootstrap and turns into --dangerously-skip-permissions through
// claudeSettingsArgs. Both shortcuts the ticket names would yield a run that
// measures a child nobody revoked: a stored posture already equal to the update
// returns at UpdateSettings' no-change check having delivered nothing, and a base
// argv carrying the flag survives Session.spawnArgs' recompose, so the installed
// next-spawn argv keeps the bypass too. The seam gets TWO guards rather than a
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
// Runner.RevokeBypass mints its request_id through nextControlID, an unexported
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

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
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
// inbandBaseArgs is []string{"--dangerously-skip-permissions"}; copied here it
// survives Session.spawnArgs' recompose, so the installed next-spawn argv keeps
// the bypass and the run measures a child nobody revoked. The bypass posture on
// this run comes from the seeded registry entry and from nowhere else, which is
// also what makes --dangerously-skip-permissions traceable to exactly one place.
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
// slog.Default(). Nothing it retains is a secret: RevokeBypass takes no mode, so
// there is no settings value the bypass record could carry, which is the
// structural guarantee deliverSettingsInBand's doc already makes.
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
// carries yolo:true, and returns the id it minted. It MUST run before
// sessions.New: the model test points RegistryPath at a fresh t.TempDir() path
// that does not exist, which is the COLD-start shape — loadRegistry returns
// (nil, nil) and settings come out zero-valued, i.e. no bypass.
//
// Each of the three ways this seed can silently fail lands on instrument check B,
// before a single token is spent: `bootstrap` missing → pickBootstrap returns nil
// → cold start → YOLO false; a misspelled key → decodes to false; a MALFORMED
// yolo value → the whole loadRegistry parse fails closed and sessions.New errors.
//
// The id comes from sessions.NewID rather than a hand-written string:
// writeMCPSettings gates the warm-start id on ValidID and hard-errors on anything
// that is not a canonical UUIDv4, and claude receives it as --session-id. 0600
// because it is the same shape saveRegistryLocked writes.
func seedBypassRegistry(t *testing.T, path string) sessions.SessionID {
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
			YOLO:         true,
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
	seededID := seedBypassRegistry(t, registryPath)

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
