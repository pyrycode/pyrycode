//go:build e2e_realclaude

package realclaude

// #1582 — proof, from claude's own report, that an in-band settings change moves
// a RUNNING child's model and never tears that child down.
//
// #1581 changed how a model/effort-only settings change is delivered: instead of
// killing the child and respawning it with a rebuilt argv, Pool.UpdateSettings
// writes `/model <value>` as an ordinary user turn on the stream the daemon
// already holds open. That ticket is proven hermetically — its tests observe that
// no respawn happened and that the write was issued. Neither observes claude, and
// a hermetic suite cannot tell "the daemon wrote the right bytes" from "the
// running child actually changed model". This file supplies the second one, and
// changes no production code.
//
// # Where it taps, and why that is structural
//
// claude announces the model on a system/init line once per turn.
//
// CORRECTED 2026-08-19 (#1600): this paragraph used to say the line "reaches no
// client and no daemon event, and must not be made to", because
// streamsup.emitSystemSubtype had arms for task_started, task_updated,
// background_tasks_changed and thinking_tokens only and init fell through to the
// ignoredLineTypes["system"] drop. init has an arm now — it becomes a
// turnevent.ModelAnnounced — so the "no daemon event" half is false, and the bar on
// adding an arm was never entailed by the log posture in the first place: an event
// is not a log. What survives, and is the half that matters here, is the LOG half:
// the value reaches a daemon EVENT and still reaches no daemon LOG at any level,
// which is the #833 posture restated on handleRequestSessionSettings. It still
// reaches no CLIENT either — turnbridge.MapEvent's default drops the variant.
//
// CORRECTED 2026-08-19 (#1616): the clause above used to close "so no wire frame
// exists for it yet". A wire frame now EXISTS — protocol.TypeModelAnnounced /
// protocol.ModelAnnouncedPayload, declared by #1616 so a client can be written
// against the shape — and this repo draws the declared/emitted line sharply
// (docs/protocol-mobile.md § rate_limited). What survives is the half above:
// MapEvent still has no case for the variant, so nothing EMITS the frame until
// #1617 and no client receives one.
//
// This test's own assertions are unaffected in either direction: it taps
// streamsup.Config.Stdout, upstream of the parser, so what the parser does with the
// line downstream changes nothing it observes.
//
// The line is reachable without touching production because streamsup.Config's
// Stdout is an io.Writer that receives the child's stdout UPSTREAM of the parser.
// cmd/pyry's newStreamRunnerFactory installs streamsup.NewParser(...) in exactly
// that slot; inbandTapRecorder takes it instead. dropcapRecorder (in
// dropped_line_capture_test.go) already does this for the same reason — that is
// the precedent, and "upstream of the parser" is a fact about the wiring rather
// than an argument.
//
// The tap survives a respawn, which is what makes the pre-change evidence run a
// real red rather than a crash: spawnAndWait re-sets cmd.Stdout from the same
// Config on EVERY spawn, so the recorder installed once at construction also sees
// a post-respawn init line.
//
// # Effort has no such observable, and none is asserted
//
// The 2026-08-19 measurement dumped every scalar key on the init line: type,
// subtype, cwd, session_id, model, permissionMode, apiKeySource,
// claude_code_version, output_style, analytics_disabled,
// product_feedback_disabled, uuid, fast_mode_state, fast_mode_disabled_reason.
// There is no effort or thinking-level field, before or after `/effort low` —
// claude confirms that command in words and announces it nowhere. An effort
// assertion read from init is therefore impossible, not merely hard. This test
// asserts MODEL and does not send /effort at all.
//
// # Evidence: which assertion carries which proof
//
// The four assertions split into two pairs, and neither pair is redundant: each
// is the SOLE red for one mutant, and they are not the same assertion. Measured
// 2026-08-19 against claude 2.1.220, all three runs with -race. The starting
// model was claude-sonnet-5 (claude's own machine default — the base argv carries
// no --model) and the target alias `haiku`.
//
// THE CURRENT TREE — all four green.
//
//	init models [claude-sonnet-5, claude-sonnet-5, claude-haiku-4-5-20251001],
//	spawns 1, pid 44368 -> 44368, 3 results, 5.7 s.
//
// THREE init lines: the /model turn does emit its own, and that one still reports
// the OLD model. Measured, not assumed — and it is why the assertions read
// first-and-last rather than a fixed index.
//
// THE PRE-CHANGE TREE, b047b9e^ (= e282de4) — A3 and A4 RED, A1 and A2 green.
//
//	init models [claude-sonnet-5, claude-haiku-4-5-20251001],
//	spawns 2, pid 45068 -> 45181, 2 results.
//
// There UpdateSettings ends in an unconditional sup.Restart(newArgs), so the
// child was killed and relaunched — which A3 and A4 catch. The model still
// changed, via the respawn's recomposed argv, so the MODEL pair discriminates
// NOTHING on this tree. The file compiles there unchanged: every API it touches
// is exported and predates #1581, SetSpawnArgs included (#1580).
//
// THE CURRENT TREE + A MUTANT that drops the `if update.Model != nil` send from
// deliverSettingsInBand — A1 and A2 RED, A3 and A4 green.
//
//	init models [claude-sonnet-5, claude-sonnet-5],
//	spawns 1, pid 47027 -> 47027, 2 results.
//
// No /model ever reached the child, and nothing was torn down. Run with
// `go test -overlay=<abs>/overlay.json`, so no mutated source was ever written
// into the worktree.
//
// Both red runs took ~65 s rather than the green run's 5.7 s: neither produces a
// result for the /model turn, so the tolerated settle wait below burns its full
// budget. That is the wait working, not a hang.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -v \
//	  -run TestInteractiveStream_InBandModelChange ./internal/e2e/realclaude/
//
// It executes on any machine with claude credentials — the only skips are the
// package's standard absent-binary / absent-credentials guards.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

const (
	// A fresh EMPTY directory under the test's temp $HOME, deliberately not a git
	// repo: less project context for claude to load, so the turns are cheaper.
	inbandWorkdirName = "inband-model-work"

	inbandPromptOne = "Reply with the single word: one."
	inbandPromptTwo = "Reply with the single word: two."
)

// inbandBaseArgs is everything this test adds to the bootstrap spawn; buildArgs
// supplies the fixed --input-format/--output-format/--verbose prefix and the id
// flag, and sessions.New appends the #943 --settings pair.
//
// It carries NO --model, on purpose. Session.spawnArgs recomposes as spawnBase +
// claudeSettingsArgs(merged), so a base --model would make the recomposed argv
// read `--model haiku … --model <target>`. On the current tree that argv is only
// INSTALLED and never executed, which is harmless — but evidence run A executes
// it, and whether claude last-wins on a duplicate flag is unmeasured. A crash
// there would turn a clean red into a muddy one. With no --model in the base the
// recompose emits exactly one, on both trees.
//
// --dangerously-skip-permissions suppresses the permission modal
// deterministically. It is the same YOLO interactive shape dropcapArgs uses, and
// the arm on which production's withApprovalArgs injects nothing. It does not
// affect inBandDeliverable, which keys on SettingsUpdate.YOLO, not on argv.
var inbandBaseArgs = []string{"--dangerously-skip-permissions"}

// inbandModelTarget is one candidate the test can switch TO. resolvedID is what
// claude's init line reports after `/model <alias>`.
//
// These are claude-VERSION facts, measured against 2.1.220 on 2026-08-19, and a
// family bump (Haiku 5, Sonnet 6) will red A2. That failure means THE ALIAS
// RESOLVES ELSEWHERE NOW, not that the in-band mechanism broke — A1, which asks
// only that the model changed, stays green in that case and is how a future
// operator tells the two apart. Update the row; do not weaken A2 to a substring
// match, which would stop testing what AC 1 names.
type inbandModelTarget struct {
	alias      string
	resolvedID string
}

var inbandModelTargets = []inbandModelTarget{
	{alias: "haiku", resolvedID: "claude-haiku-4-5-20251001"},
	{alias: "sonnet", resolvedID: "claude-sonnet-5"},
}

const (
	inbandSpawnWait = 60 * time.Second
	inbandPoll      = 100 * time.Millisecond

	// One live turn's budget. A one-word reply lands in seconds; the headroom is
	// for evidence run A, where the turn has to outlive a kill and a respawn.
	inbandTurnBudget = 3 * time.Minute

	// How long a written turn may go unanswered before it is written again. The
	// resend exists for evidence run A, not for the shipped green path: a tree
	// that respawns instead of writing in-band can accept a write into the
	// outgoing child's still-open stdin and lose the turn with it, and no
	// ErrNoLiveChild is ever returned there. Comfortably longer than a healthy
	// one-word turn (~3-8 s measured), so the green path never duplicates.
	inbandResendAfter = 45 * time.Second

	// The wait for the /model turn to close, so turn 3 is not queued behind it —
	// which would be unmeasured behaviour. The whole three-turn green run takes
	// 5.7 s, so 60 s is an order of magnitude of headroom; it is also what each
	// red evidence run pays in full, since neither produces a /model result. This
	// is the ONE wait whose timeout is tolerated rather than fatal — see the call
	// site.
	inbandSettleBudget = 60 * time.Second

	inbandRunExitWait = 30 * time.Second
)

// inbandMaxPartial caps the partial-line accumulator — same value and same reason
// as streamsup's defaultMaxParseBuf and dropcapMaxPartial. This is claude's
// stdout read into the parent's memory, and the partial is the only unbounded
// accumulator here.
const inbandMaxPartial = 4 << 20

// --- the stdout tap ----------------------------------------------------------

// inbandTapRecorder is an io.Writer wired into streamsup.Config.Stdout, where
// production installs the turnevent parser. It extracts exactly two things and
// retains no payloads: the `model` field of every system/init line, in arrival
// order, and a count of top-level `result` lines (the turn boundary).
//
// Mutex-guarded because os/exec drives Write from its own stdout-copier goroutine
// while the test goroutine reads; that is a race under -race. dropcapRecorder
// states the same reason.
//
// It is deliberately NOT dropcapRecorder: that one retains every raw line up to
// 8 MB for a committed fixture and exposes a one-shot resultSeen channel, where
// this test needs three turn boundaries and retains nothing. Reusing it would
// couple a forensic capture instrument to an assertion instrument.
type inbandTapRecorder struct {
	mu      sync.Mutex
	partial []byte
	models  []string
	results int
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

func newInbandTapRecorder() *inbandTapRecorder {
	return &inbandTapRecorder{maxPartial: inbandMaxPartial}
}

// Write NEVER returns a non-nil error: this writer is an exec.Cmd's stdout sink,
// and an error there aborts os/exec's copy and can wedge the child.
func (r *inbandTapRecorder) Write(b []byte) (int, error) {
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
			r.skipUntilNewline = false
		} else {
			r.consume(rest[:i])
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

// consume classifies one complete line. Called under r.mu. The decode mirrors
// parseInitSessionID's idiom — an anonymous struct, and a decode failure skipped
// silently, because claude's stdout carries lines this test has no interest in.
func (r *inbandTapRecorder) consume(line []byte) {
	var env struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &env); err != nil {
		return
	}
	switch {
	case env.Type == "system" && env.Subtype == "init":
		r.models = append(r.models, env.Model)
	case env.Type == "result":
		r.results++
	}
}

// initModels returns a snapshot copy of the init-announced models in arrival
// order.
func (r *inbandTapRecorder) initModels() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.models...)
}

func (r *inbandTapRecorder) resultCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.results
}

func (r *inbandTapRecorder) droppedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// --- the spawn counter -------------------------------------------------------

// inbandSpawnHandler is a slog.Handler that WRITES NOTHING and counts records
// whose message is "spawning claude" — one per spawn, emitted by streamsup's Run.
// That count is the deterministic half of AC 2's "exactly one spawn"; ChildPID is
// the direct half. newDropcapArgvHandler is the working precedent for this shape.
//
// Config.onSpawn is unexported and unusable from this package.
//
// It doubles as the explicit discard handler the runner needs: a nil
// Config.Logger falls back to slog.Default(), which would put the runner's
// lifecycle lines into the test output. It is installed as sessions.Config.Logger
// and threaded through RunnerConfig.Logger, so the pool's own records pass
// through it too — harmlessly, since it filters by message.
type inbandSpawnHandler struct {
	mu *sync.Mutex
	n  *int
}

func newInbandSpawnCounter() (slog.Handler, func() int) {
	var (
		mu sync.Mutex
		n  int
	)
	h := inbandSpawnHandler{mu: &mu, n: &n}
	return h, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

func (h inbandSpawnHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h inbandSpawnHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "spawning claude" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.n++
	return nil
}

func (h inbandSpawnHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h inbandSpawnHandler) WithGroup(string) slog.Handler      { return h }

// --- the runner adapter ------------------------------------------------------

// inbandRunner adapts *streamsup.Runner to sessions.Runner. Go has no covariant
// return on interface satisfaction and the concrete runner's State returns
// streamsup.State, not sessions.State, so the one method has to be mapped;
// the other six are promoted from the embedded runner. This is cmd/pyry's
// streamRunner with the field-for-field State conversion and nothing else — no
// turnevent Parser (the recorder owns that slot) and no withApprovalArgs (the
// base argv is already YOLO, the arm on which it injects nothing).
//
// The embedded runner is left exported-by-promotion on purpose: the test body
// reads Stdin() through it, which sessions.Runner does not carry.
type inbandRunner struct{ *streamsup.Runner }

func (a inbandRunner) State() sessions.State {
	s := a.Runner.State()
	return sessions.State{
		Phase:        sessions.Phase(string(s.Phase)),
		ChildPID:     s.ChildPID,
		StartedAt:    s.StartedAt,
		RestartCount: s.RestartCount,
		LastUptime:   s.LastUptime,
		NextBackoff:  s.NextBackoff,
	}
}

var _ sessions.Runner = inbandRunner{}

// --- the test ----------------------------------------------------------------

// TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel drives one
// live claude session through a turn, a model change delivered by the daemon's
// own Pool.UpdateSettings, and a further turn — then asserts from claude's own
// per-turn system/init announcement that the model changed to the requested one,
// and that the same child served both turns.
func TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials

	workdir := filepath.Join(home, inbandWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#1582: create workdir: %v", err)
	}

	rec := newInbandTapRecorder()
	spawnHandler, spawns := newInbandSpawnCounter()

	// The factory captures the concrete runner so the test body can read Stdin()
	// and ChildPID off the very object the pool will deliver the /model command
	// to. It is the one place Stdout is set, and it is set to the recorder.
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
			return nil, fmt.Errorf("#1582: stream runner: %w", err)
		}
		tap = inbandRunner{Runner: r}
		return tap, nil
	}

	// RegistryPath is set, not empty, so saveLocked inside UpdateSettings performs
	// a real write and writeMCPSettings lands the #943 --settings file in the
	// production location. Both live under t.TempDir().
	//
	// Pool.Run is deliberately NOT called: the session lifecycle goroutine's only
	// job here would be `go s.sup.Run(subCtx)`, which the test does directly
	// below, and skipping it also skips the conversations sweep, the idle timer
	// and the settings-file reaper. UpdateSettings consults none of them — it
	// reads sess.sup and calls methods on it.
	pool, err := sessions.New(sessions.Config{
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:  claudeBin,
			WorkDir:    workdir,
			ClaudeArgs: inbandBaseArgs,
		},
		RegistryPath:  filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: factory,
		Logger:        slog.New(spawnHandler),
	})
	if err != nil {
		t.Fatalf("#1582: sessions.New: %v", err)
	}

	// The instrument check that makes the whole measurement non-vacuous: the
	// runner the test drives must be the runner UpdateSettings will write the
	// /model command to. Without it, a future refactor that hands the pool a
	// different runner would leave every assertion below reading a bystander.
	sup := pool.Default().Runner()
	if sup != sessions.Runner(tap) {
		t.Fatalf("#1582: pool.Default().Runner() is not the runner the test captured; "+
			"the tap observes a different child than UpdateSettings drives (%T vs %T)", sup, tap)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = tap.Run(ctx)
	}()
	// Registered immediately after the Run goroutine starts, so a t.Fatalf
	// anywhere in the drive sequence still tears the child down.
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(inbandRunExitWait):
			t.Errorf("#1582: streamsup.Run did not return within %s of cancel", inbandRunExitWait)
		}
	})

	if !inbandWaitForChild(tap) {
		t.Fatalf("#1582: no live child within %s: claude never spawned", inbandSpawnWait)
	}
	pidBefore := tap.State().ChildPID
	if pidBefore == 0 {
		t.Fatalf("#1582: ChildPID is 0 with a live stdin handle; the instrument cannot answer AC 2")
	}

	inbandSendTurn(t, sup, rec, inbandPromptOne)

	models := rec.initModels()
	if len(models) == 0 || models[0] == "" {
		t.Fatalf("#1582: the tap saw no system/init model after turn 1 (models=%q); "+
			"the whole measurement is void", models)
	}
	target := inbandPickTarget(t, models[0])
	t.Logf("#1582: starting model %q, changing to alias %q (expecting %q)",
		models[0], target.alias, target.resolvedID)

	// Model ONLY. A non-nil YOLO — even one equal to the stored value — or a
	// present-but-empty Model routes onto the restart path (inBandDeliverable),
	// which would silently make this test measure the mechanism it exists to
	// distinguish itself from. The alias genuinely differs from the stored ""
	// (the base argv carries no --model), so UpdateSettings does not take its
	// no-op early return either.
	alias := target.alias
	if err := pool.UpdateSettings(pool.Default().ID(), sessions.SettingsUpdate{Model: &alias}); err != nil {
		t.Fatalf("#1582: UpdateSettings(model=%q): %v", alias, err)
	}

	// Wait for the /model turn to close so turn 3 is not queued behind it. This is
	// the ONE wait whose timeout is tolerated: on a tree that restarts instead of
	// writing in-band there is no /model turn to close, no second result ever
	// comes, and the test must still reach its assertions rather than dying here.
	if !inbandWaitResults(rec, 2, inbandSettleBudget) {
		t.Logf("#1582: no result for the /model turn within %s — expected on a tree that "+
			"restarts instead of delivering in-band; continuing to the assertions",
			inbandSettleBudget)
	}

	inbandSendTurn(t, sup, rec, inbandPromptTwo)

	models = rec.initModels()
	spawnCount := spawns()
	pidAfter := tap.State().ChildPID
	t.Logf("#1582: init models %q, spawns %d, pid %d -> %d, results %d, dropped partials %d",
		models, spawnCount, pidBefore, pidAfter, rec.resultCount(), rec.droppedCount())

	if len(models) < 2 {
		t.Fatalf("#1582: only %d system/init line(s) captured (%q); a before and an after "+
			"are both needed", len(models), models)
	}
	// FIRST and LAST, never a fixed index: the /model turn emits its own init line
	// reporting the OLD model (measured — see the header's § Evidence), and a
	// resent turn adds another. Neither changes the verdict, so do not assert
	// len(models) == 3.
	first, last := models[0], models[len(models)-1]

	if last == first {
		t.Errorf("A1: the child reported model %q both before and after the change; "+
			"no /model reached the live child", first)
	}
	if last != target.resolvedID {
		t.Errorf("A2: the child reported model %q after `/model %s`, want %q. "+
			"If A1 passed, the mechanism worked and this row of inbandModelTargets is stale "+
			"(the alias resolves elsewhere on this claude version) — update it",
			last, target.alias, target.resolvedID)
	}
	if pidAfter != pidBefore {
		t.Errorf("A3: child pid %d served the first turn but %d served the last; "+
			"the child was torn down across the settings change", pidBefore, pidAfter)
	}
	if spawnCount != 1 {
		t.Errorf("A4: %d spawns over the whole run, want exactly 1; "+
			"the settings change respawned the child", spawnCount)
	}
}

// inbandPickTarget returns the first target whose resolved id differs from the
// session's starting model. The base argv carries no --model, so the start is
// claude's own machine default and is READ rather than assumed; because the two
// table entries resolve differently, one always qualifies, so the test never
// skips and never asserts a change that was already true.
func inbandPickTarget(t *testing.T, start string) inbandModelTarget {
	t.Helper()
	for _, tgt := range inbandModelTargets {
		if tgt.resolvedID != start {
			return tgt
		}
	}
	// Unreachable unless the table's two resolved ids were edited to be equal —
	// a broken instrument, not a condition to skip on.
	t.Fatalf("#1582: no target in %v differs from the starting model %q", inbandModelTargets, start)
	return inbandModelTarget{}
}

// inbandWaitForChild polls Runner.Stdin() until a child is live. WriteUserTurn
// maps a nil writer to ErrNoLiveChild, so this is the pre-spawn window a turn
// would otherwise have to poll through. dropcapWaitForChild is the same loop; it
// takes a *streamsup.Runner, which this adapter embeds rather than exposes.
func inbandWaitForChild(tap inbandRunner) bool {
	deadline := time.Now().Add(inbandSpawnWait)
	for {
		if tap.Stdin() != nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(inbandPoll)
	}
}

// inbandWaitResults polls until the recorder has seen at least want result lines,
// reporting whether it got there within budget.
func inbandWaitResults(rec *inbandTapRecorder, want int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if rec.resultCount() >= want {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(inbandPoll)
	}
}

// inbandSendTurn writes prompt through the sessions.Runner seam — the same method
// deliverSettingsInBand uses, so the test's own turns and the daemon's in-band
// command travel one path — and waits for the child to close the turn with a
// result line.
//
// It re-sends while no new result has landed. That is for evidence run A, not for
// the shipped green path where the first attempt lands: a tree that respawns
// instead of writing in-band leaves the stdin handle either briefly dead
// (ErrNoLiveChild, retried immediately) or, worse, still pointing at the outgoing
// child, where the write succeeds and the turn dies with the process. Retrying on
// ErrNoLiveChild mirrors what msgqueue does in production. A duplicate send is
// harmless to the verdict: the assertions read the FIRST and LAST init model, so
// an extra turn costs tokens, not truth.
func inbandSendTurn(t *testing.T, sup sessions.Runner, rec *inbandTapRecorder, prompt string) {
	t.Helper()
	baseline := rec.resultCount()
	deadline := time.Now().Add(inbandTurnBudget)
	var lastSend time.Time
	for {
		if lastSend.IsZero() || time.Since(lastSend) >= inbandResendAfter {
			err := sup.WriteUserTurn(context.Background(), "", []byte(prompt))
			switch {
			case err == nil:
				lastSend = time.Now()
			case errors.Is(err, streamsup.ErrNoLiveChild):
				// No child to write to yet — a spawn or a respawn is in flight.
			default:
				t.Fatalf("#1582: WriteUserTurn(%q): %v", prompt, err)
			}
		}
		if rec.resultCount() > baseline {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("#1582: turn %q produced no result line within %s", prompt, inbandTurnBudget)
		}
		time.Sleep(inbandPoll)
	}
}
