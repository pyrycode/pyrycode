//go:build e2e_realclaude

package realclaude

// #1655 — does a claude launched with `--session-id <id>` that then runs NO turn
// leave an `<id>.jsonl` on disk? ADR 032 decided the rule that fixes the resulting
// crash-loop; #1630 carries it into streamsup and consumes THIS measurement as its
// missing premise.
//
// Three properties drive the design, each a way the naive "stat the file" version
// would record a lie:
//
//  1. The fact is an ABSENCE, and every setup failure produces the same absence.
//     Hence the control arm, in the same run and the same directory, and hence the
//     directory found empirically (streamNewSessionTranscriptDir), not recomputed.
//  2. The two readings are NOT interchangeable. A transcript flushed at process exit
//     reads absent while the child is alive and present at respawn — where #1630's
//     probe reads. Both readings are taken.
//  3. A flush-at-exit does not run under SIGKILL, so an absence read after a
//     force-kill is an artefact of the signal — classifyTurnlessTranscript records
//     that as INCONCLUSIVE, never as the fact holding.
//
// A file that DOES appear is a falsification: recorded, and the test passes. The
// test fails only for a broken instrument.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/transcript"
)

const (
	// Ticket-prefixed reserved stems, per the package's fixed-id convention. ADR 032
	// records that claude REFUSES `--session-id <uuid>` when `<uuid>.jsonl` already
	// exists — harmless here: WithWorktree pins $HOME to a fresh t.TempDir().
	transcriptProbeControlID  = "16550000-0000-4000-8000-000000000001" // arm A, driven through one turn
	transcriptProbeTurnlessID = "16550000-0000-4000-8000-000000000002" // arm B, given no turn

	transcriptProbeModel    = "claude-haiku-4-5"
	transcriptProbeMaxTurns = "2"
	transcriptProbePrompt   = "Reply with the single word: ok"

	// Outer per-arm leak guards. A context kill is a SIGKILL, so if one of these
	// fires arm B records as did-not-exit rather than as a graceful exit.
	transcriptProbeControlBudget  = 4 * time.Minute
	transcriptProbeTurnlessBudget = 3 * time.Minute

	transcriptProbeDirBudget        = 120 * time.Second // <A>.jsonl must appear
	transcriptProbeControlExit      = 60 * time.Second  // arm A exit after stdin close
	transcriptProbeTurnlessSettle   = 30 * time.Second  // arm B settle before reading 1
	transcriptProbePostExitSettle   = 10 * time.Second  // reading 2's bounded flush window
	transcriptProbeKillGrace        = 5 * time.Second   // mirrors streamsup's killGrace
	transcriptProbeHardWait         = 15 * time.Second  // post-SIGKILL bound
	transcriptProbeStreamCap        = 1 << 20           // ingest cap per child stream
	transcriptProbeRecordCap        = 64 << 10          // record-time cap per stream
	transcriptProbeConsumedReading  = "after-exit"      // which reading #1630's probe consumes
	transcriptProbeStatPollInterval = 25 * time.Millisecond
)

// transcriptProbeArgs returns the argv shape buildArgs emits on a FIRST spawn —
// the fixed stream-json prefix, then the base args, then `--session-id <id>`. Never
// emits -p/--print; `--model`/`--max-turns` is the base runSetModeChild uses.
//
// BOTH arms call this one function, which is what makes arm B's liveness argument
// structural rather than asserted: the argvs are identical by construction bar the
// id, so the control arm having driven a turn through this shape IS proof that this
// claude version accepts arm B's flags. An assertion comparing the two would compare
// a function against itself; the record carries both slices verbatim instead.
func transcriptProbeArgs(sessionID string) []string {
	return []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", transcriptProbeModel,
		"--max-turns", transcriptProbeMaxTurns,
		"--session-id", sessionID,
	}
}

// --- capture, redaction, and the single egress -------------------------------

// boundedBuffer appends up to limit bytes and silently drops the rest. Bounding at
// INGEST is the point: truncateString runs at record time, after the buffer already
// holds everything, so a looping child would balloon memory before anything trimmed
// it. Mutex-guarded because os/exec writes from its own copying goroutine while the
// test reads — arm B's liveness Fatal reads stderr with the child still running.
type boundedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return n, nil
}

func (b *boundedBuffer) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// recordStream is the ONLY way a captured child stream becomes a string this test
// emits — record field, t.Logf and t.Fatalf message alike. A bare snapshot() call
// anywhere else in this file is a review finding: the liveness Fatal is the
// highest-risk of the five sites, firing precisely on an argv rejection or auth
// failure, which is the stderr most likely to echo a credential. Redacts BEFORE
// truncating, so a truncation boundary cannot slice a token into a form the
// redactor no longer matches.
func recordStream(b *boundedBuffer) string {
	return truncateString(redactCredentials(b.snapshot()), transcriptProbeRecordCap)
}

// redactCredentials replaces each non-empty ANTHROPIC_API_KEY /
// CLAUDE_CODE_OAUTH_TOKEN value with <redacted:NAME>. An EMPTY value is SKIPPED:
// strings.ReplaceAll with an empty old string inserts the replacement between every
// rune, corrupting the record rather than protecting it — and the empty case is the
// common one, since WithWorktreeAuthenticated needs only one of the two and leaves
// the other unset rather than set-empty.
func redactCredentials(s string) string {
	for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		value := os.Getenv(name)
		if value == "" {
			continue
		}
		s = strings.ReplaceAll(s, value, "<redacted:"+name+">")
	}
	return s
}

// --- the child ----------------------------------------------------------------

type probeChild struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *boundedBuffer
	stderr  *boundedBuffer
	exited  chan struct{}
	waitErr error // written before close(exited), read only after it
}

// startProbeChild launches one claude child and starts a single waiter goroutine
// whose only job is to store cmd.Wait's error and close exited.
//
// The waiter MUST NOT touch *testing.T. On the did-not-exit path it outlives the
// test function, and a t.Logf from a goroutine after the test completes panics with
// "Log in goroutine after test has completed", destroying the record this ticket
// exists to produce on exactly the outcome that is hardest to reproduce.
//
// cmd.Env stays nil so the child inherits WithWorktree's t.Setenv-pinned HOME and
// WithWorktreeAuthenticated's credential re-pin. Assigning cmd.Env drops the pinned
// HOME (the child writes into the operator's real projects tree and the empirical
// scan finds nothing) AND drops the credential.
func startProbeChild(ctx context.Context, claudeBin, cwd string, argv []string) (*probeChild, error) {
	cmd := exec.CommandContext(ctx, claudeBin, argv...)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	c := &probeChild{
		cmd:    cmd,
		stdin:  stdin,
		stdout: &boundedBuffer{limit: transcriptProbeStreamCap},
		stderr: &boundedBuffer{limit: transcriptProbeStreamCap},
		exited: make(chan struct{}),
	}
	cmd.Stdout = c.stdout
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}
	go func() {
		c.waitErr = cmd.Wait()
		close(c.exited)
	}()
	return c, nil
}

// alive reports a point-in-time "has not exited yet". exited is the single liveness
// oracle in this file.
func (c *probeChild) alive() bool {
	select {
	case <-c.exited:
		return false
	default:
		return true
	}
}

// waitExit blocks until the child exits or d elapses, reporting whether it exited.
func (c *probeChild) waitExit(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.exited:
		return true
	case <-timer.C:
		return false
	}
}

// snapshotExit returns (exit code, wait error text), or (-1, "") when the child has
// not exited. The channel check is what makes the read race-free: both fields are
// written by the waiter goroutine before it closes exited.
func (c *probeChild) snapshotExit() (int, string) {
	select {
	case <-c.exited:
	default:
		return -1, ""
	}
	code := -1
	if c.cmd.ProcessState != nil {
		code = c.cmd.ProcessState.ExitCode()
	}
	if c.waitErr != nil {
		return code, c.waitErr.Error()
	}
	return code, ""
}

// --- termination ---------------------------------------------------------------

// terminationMode names how arm B actually ended, observed directly rather than
// deduced from os/exec error semantics: errors.Is(waitErr, exec.ErrWaitDelay) would
// couple a recorded fact to a subtle WaitDelay interaction, and the recorded fact is
// the whole deliverable.
type terminationMode string

const (
	terminationSIGTERM    terminationMode = "sigterm"
	terminationSIGKILL    terminationMode = "sigkill"
	terminationDidNotExit terminationMode = "did-not-exit"
)

// endTurnlessChild ends the child the way the production path ends one: SIGTERM, a
// grace window mirroring streamsup's killGrace, then SIGKILL and a hard bound.
// Returns which signal the child actually exited under and how long that took.
// Never Fatals — did-not-exit is a recordable outcome that makes the after-exit
// reading INCONCLUSIVE.
//
// A child that had already exited on its own records as sigterm: no force-kill
// occurred, so a flush-at-exit would have run, which is the only property the
// classifier reads the mode for. The record's alive_after_reading carries the rest.
//
// Signals a single pid, never a negative-pid process group: under some `go test`
// invocations the test binary shares the child's group, so a group signal can take
// out the runner and the record with it.
func endTurnlessChild(c *probeChild, grace, hard time.Duration) (terminationMode, time.Duration) {
	start := time.Now()
	// Best-effort: the child may have exited between the liveness read and here,
	// in which case the wait below returns immediately.
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	if c.waitExit(grace) {
		return terminationSIGTERM, time.Since(start)
	}
	_ = c.cmd.Process.Kill()
	if c.waitExit(hard) {
		return terminationSIGKILL, time.Since(start)
	}
	return terminationDidNotExit, time.Since(start)
}

// --- readings -------------------------------------------------------------------

// probeReading is one by-id stat of <dir>/<id>.jsonl.
type probeReading struct {
	Found     bool   `json:"found"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	StatError string `json:"stat_error"` // raw os.Stat error text; absence is the EXPECTED answer
}

// statByID reads through transcript.StatByID — the primitive #1630's probe will
// call, so evidence and consumer read with one instrument. An absent file comes back
// as (Result{}, err) wrapping the raw os.Stat error: stored, never branched on
// beyond Result.Found().
func statByID(dir, id string) probeReading {
	res, err := transcript.StatByID(dir, id)
	r := probeReading{Found: res.Found(), Path: res.Path, Size: res.Size}
	if err != nil {
		r.StatError = err.Error()
	}
	return r
}

// statByIDPolled polls for up to window and returns on the first hit, so a slow
// flush is a bounded wait rather than a race that would misreport FALSIFIED as
// HOLDS.
func statByIDPolled(dir, id string, window time.Duration) probeReading {
	deadline := time.Now().Add(window)
	for {
		r := statByID(dir, id)
		if r.Found || !time.Now().Before(deadline) {
			return r
		}
		time.Sleep(transcriptProbeStatPollInterval)
	}
}

// --- the verdict ------------------------------------------------------------------

// classifyTurnlessTranscript maps the two readings and the termination mode to the
// one sentence a reader must not have to infer.
//
// The two INCONCLUSIVE arms are the SIGKILL rule in code: an absence read after a
// force-kill (or with no exit at all) is an artefact of the signal and must never be
// recorded as the fact holding. A present file at EITHER reading short-circuits
// every termination consideration — no signal confound conjures a transcript into
// existence.
func classifyTurnlessTranscript(aliveFound, afterExitFound bool, ended terminationMode) (verdict, sentence string) {
	switch {
	case aliveFound:
		return "FALSIFIED", "FALSIFIED: the turnless session's transcript was present on disk while the " +
			"child was still running, so a `--session-id` launch that runs no turn DOES leave an <id>.jsonl."
	case afterExitFound:
		return "FALSIFIED", "FALSIFIED: the turnless session's transcript was absent while the child ran but " +
			"present after it ended, so by the time #1630's respawn probe reads, a turnless `--session-id` " +
			"launch HAS left an <id>.jsonl."
	case ended == terminationSIGKILL:
		return "INCONCLUSIVE", "INCONCLUSIVE: the turnless session's transcript was absent at both readings, " +
			"but the child had to be force-killed (SIGKILL), so a flush-at-exit would not have run and the " +
			"after-exit absence is an artefact of the signal rather than a measurement."
	case ended == terminationDidNotExit:
		return "INCONCLUSIVE", "INCONCLUSIVE: the turnless session's transcript was absent at both readings, " +
			"but the child never exited within the grace and hard bounds, so no after-exit reading was taken " +
			"under a completed exit."
	default:
		return "HOLDS", "HOLDS: the turnless session's transcript was absent both while the child ran and " +
			"after it exited gracefully under SIGTERM, so a `--session-id` launch that runs no turn leaves no " +
			"<id>.jsonl on disk."
	}
}

func readingAliveVerdict(found bool) string {
	if found {
		return "present while alive"
	}
	return "absent while alive"
}

func readingAfterExitVerdict(found bool, ended terminationMode) string {
	switch {
	case found:
		return "present after exit"
	case ended == terminationSIGKILL:
		return "INCONCLUSIVE — the child was force-killed, so a flush-at-exit would not have run"
	case ended == terminationDidNotExit:
		return "INCONCLUSIVE — the child never exited, so no after-exit reading was taken"
	default:
		return "absent after a graceful (SIGTERM) exit"
	}
}

// --- the record --------------------------------------------------------------------

type transcriptProbeControlArm struct {
	SessionID      string   `json:"session_id"`
	Argv           []string `json:"argv"`
	Prompt         string   `json:"prompt"`
	ExitCode       int      `json:"exit_code"`
	WaitError      string   `json:"wait_error"`
	TranscriptPath string   `json:"transcript_path"`
	TranscriptSize int64    `json:"transcript_size"`
	Stdout         string   `json:"stdout"`
	Stderr         string   `json:"stderr"`
}

type transcriptProbeTurnlessArm struct {
	SessionID          string          `json:"session_id"`
	Argv               []string        `json:"argv"`
	SettleMs           int64           `json:"settle_ms"`
	AliveBeforeReading bool            `json:"alive_before_reading"`
	AliveAfterReading  bool            `json:"alive_after_reading"`
	ReadingAlive       probeReading    `json:"reading_alive"`
	TerminationMode    terminationMode `json:"termination_mode"`
	TermToExitMs       int64           `json:"term_to_exit_ms"`
	ExitCode           int             `json:"exit_code"`
	WaitError          string          `json:"wait_error"`
	ReadingAfterExit   probeReading    `json:"reading_after_exit"`
	Stdout             string          `json:"stdout"`
	Stderr             string          `json:"stderr"`
}

type transcriptProbeRecord struct {
	ClaudeVersionRaw   string `json:"claude_version_raw"`
	ClaudeVersionToken string `json:"claude_version_token"`
	ChildCwd           string `json:"child_cwd"`

	TranscriptDirEmpirical  string `json:"transcript_dir_empirical"`
	TranscriptDirRecomputed string `json:"transcript_dir_recomputed"`
	TranscriptDirMatch      bool   `json:"transcript_dir_match"`

	ControlArm  transcriptProbeControlArm  `json:"control_arm"`
	TurnlessArm transcriptProbeTurnlessArm `json:"turnless_arm"`

	Verdict                 string `json:"verdict"`
	VerdictSentence         string `json:"verdict_sentence"`
	ReadingAliveVerdict     string `json:"reading_alive_verdict"`
	ReadingAfterExitVerdict string `json:"reading_after_exit_verdict"`
	ReadingConsumedBy1630   string `json:"reading_consumed_by_1630"`
}

// --- the live measurement ------------------------------------------------------------

func TestRealClaude_TurnlessSessionIDTranscript(t *testing.T) {
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("#1655: resolve claude: %v", err)
	}
	versionRaw, versionToken := captureClaudeVersion(t)

	// Both arms share one workdir and therefore one test function: WithWorktree pins
	// $HOME with t.Setenv, so a second test function gets a different $HOME and the
	// control arm's pinned directory no longer exists.
	workdir := filepath.Join(home, "session-transcript-probe-work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#1655: mkdir workdir: %v", err)
	}
	// The child's cwd gets the SAME canonicalisation production applies: streamsup
	// resolves Config.WorkDir through agentrun.ResolveWorkdir and only then assigns
	// cmd.Dir. The literal path would make the directory comparison below measure
	// the test's cwd choice instead of production's.
	childCwd, err := agentrun.ResolveWorkdir(workdir)
	if err != nil {
		t.Fatalf("#1655: resolve workdir %s: %v", workdir, err)
	}

	rec := transcriptProbeRecord{
		ClaudeVersionRaw:      versionRaw,
		ClaudeVersionToken:    versionToken,
		ChildCwd:              childCwd,
		ReadingConsumedBy1630: transcriptProbeConsumedReading,
	}

	// --- arm A: the control, which pins the directory --------------------------
	//
	// It must complete before arm B starts, twice over: the control transcript has
	// to be established before reading 1, and arm B's liveness argument needs the
	// control turn to have already proved the argv shape.
	controlArgv := transcriptProbeArgs(transcriptProbeControlID)
	rec.ControlArm = transcriptProbeControlArm{
		SessionID: transcriptProbeControlID,
		Argv:      controlArgv,
		Prompt:    transcriptProbePrompt,
	}
	controlCtx, cancelControl := context.WithTimeout(context.Background(), transcriptProbeControlBudget)
	defer cancelControl()
	control, err := startProbeChild(controlCtx, claudeBin, childCwd, controlArgv)
	if err != nil {
		t.Fatalf("#1655[control]: %v — the positive control is what makes an absence readable", err)
	}
	t.Cleanup(func() { _ = control.cmd.Process.Kill() })

	turn, err := setModeTurnLine(transcriptProbePrompt)
	if err != nil {
		t.Fatalf("#1655[control]: %v", err)
	}
	if _, err := control.stdin.Write(turn); err != nil {
		t.Fatalf("#1655[control]: write turn: %v\nstderr:\n%s", err, recordStream(control.stderr))
	}

	// The empirical find IS the control's success signal and AC 1's non-vacuity
	// guard. Recomputing the encoded folder name would record HOLDS on a broken
	// fixture, so this is authoritative and DefaultClaudeSessionsDir is only ever
	// the compared-against value. streamNewSessionTranscriptDir Fatals with a full
	// projects-tree listing on timeout — AC 1's "the run fails rather than
	// recording a verdict".
	dir := streamNewSessionTranscriptDir(t, home, transcriptProbeControlID, transcriptProbeDirBudget)
	controlReading := statByID(dir, transcriptProbeControlID)
	rec.ControlArm.TranscriptPath = controlReading.Path
	rec.ControlArm.TranscriptSize = controlReading.Size

	recomputed := sessions.DefaultClaudeSessionsDir(childCwd)
	rec.TranscriptDirEmpirical = dir
	rec.TranscriptDirRecomputed = recomputed
	rec.TranscriptDirMatch = dir == recomputed
	// A divergence is a RECORDED OUTCOME, not a test failure: it is the finding the
	// follow-up that supplies this directory on the daemon's production path needs.
	// ResolveWorkdir applies canonicalCase where DefaultClaudeSessionsDir applies
	// EvalSymlinks alone, which is the plausible source.
	if !rec.TranscriptDirMatch {
		t.Logf("#1655: transcript dir DIVERGENCE — empirical %s vs recomputed %s (cwd %s)",
			dir, recomputed, childCwd)
	}

	_ = control.stdin.Close()
	if !control.waitExit(transcriptProbeControlExit) {
		_ = control.cmd.Process.Kill()
		control.waitExit(transcriptProbeHardWait)
	}
	rec.ControlArm.ExitCode, rec.ControlArm.WaitError = control.snapshotExit()
	rec.ControlArm.Stdout = recordStream(control.stdout)
	rec.ControlArm.Stderr = recordStream(control.stderr)

	// --- arm B: the turnless child ---------------------------------------------
	turnlessArgv := transcriptProbeArgs(transcriptProbeTurnlessID)
	rec.TurnlessArm = transcriptProbeTurnlessArm{
		SessionID: transcriptProbeTurnlessID,
		Argv:      turnlessArgv,
		SettleMs:  transcriptProbeTurnlessSettle.Milliseconds(),
	}
	turnlessCtx, cancelTurnless := context.WithTimeout(context.Background(), transcriptProbeTurnlessBudget)
	defer cancelTurnless()
	turnless, err := startProbeChild(turnlessCtx, claudeBin, childCwd, turnlessArgv)
	if err != nil {
		t.Fatalf("#1655[turnless]: %v", err)
	}
	t.Cleanup(func() { _ = turnless.cmd.Process.Kill() })
	// stdin is opened and NEVER written to, and never closed before reading 1.
	// Holding it open mirrors streamsup, which keeps the handle for the child's
	// whole life; closing it would end the session, conflating "ended by us" with
	// "ended by EOF" and possibly triggering the very flush reading 2 exists to
	// attribute.
	defer func() { _ = turnless.stdin.Close() }()

	turnless.waitExit(transcriptProbeTurnlessSettle)

	// AC 2's liveness gate. `init` is emitted per turn rather than at spawn (#1595,
	// recorded in set-permission-mode-inband-probe.md), so a turnless child never
	// emits one and it cannot be the liveness signal. Liveness rests instead on the
	// control arm having already driven the identical argv shape through a turn,
	// plus this child still being alive when reading 1 is taken.
	rec.TurnlessArm.AliveBeforeReading = turnless.alive()
	if !rec.TurnlessArm.AliveBeforeReading {
		code, waitErr := turnless.snapshotExit()
		t.Fatalf("#1655[turnless]: the turnless child had already exited before reading 1 "+
			"(exit %d, waitErr %q) — a child that died contributes no absence, so no verdict is "+
			"readable. The control arm drove the identical argv shape through a turn, so an argv "+
			"rejection here would be a claude-version change; stderr distinguishes that from a "+
			"crash.\nargv: %v\nstderr:\n%s",
			code, waitErr, turnlessArgv, recordStream(turnless.stderr))
	}

	rec.TurnlessArm.ReadingAlive = statByID(dir, transcriptProbeTurnlessID)
	rec.TurnlessArm.AliveAfterReading = turnless.alive()

	mode, termToExit := endTurnlessChild(turnless, transcriptProbeKillGrace, transcriptProbeHardWait)
	rec.TurnlessArm.TerminationMode = mode
	rec.TurnlessArm.TermToExitMs = termToExit.Milliseconds()
	rec.TurnlessArm.ExitCode, rec.TurnlessArm.WaitError = turnless.snapshotExit()

	rec.TurnlessArm.ReadingAfterExit = statByIDPolled(dir, transcriptProbeTurnlessID, transcriptProbePostExitSettle)
	rec.TurnlessArm.Stdout = recordStream(turnless.stdout)
	rec.TurnlessArm.Stderr = recordStream(turnless.stderr)

	// --- verdict and record ------------------------------------------------------
	rec.Verdict, rec.VerdictSentence = classifyTurnlessTranscript(
		rec.TurnlessArm.ReadingAlive.Found, rec.TurnlessArm.ReadingAfterExit.Found, mode)
	rec.ReadingAliveVerdict = readingAliveVerdict(rec.TurnlessArm.ReadingAlive.Found)
	rec.ReadingAfterExitVerdict = readingAfterExitVerdict(rec.TurnlessArm.ReadingAfterExit.Found, mode)

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("#1655: marshal record: %v", err)
	}
	// The plain-language line first, so the verdict survives a truncated log.
	t.Logf("#1655 VERDICT: %s", rec.VerdictSentence)
	t.Logf("#1655 reading taken alive: %s", rec.ReadingAliveVerdict)
	t.Logf("#1655 reading taken after exit: %s (#1630's probe consumes the %s reading)",
		rec.ReadingAfterExitVerdict, transcriptProbeConsumedReading)
	t.Logf("#1655 RECORD:\n%s", data)
}

// --- the classifier, without a subprocess ---------------------------------------------

// TestTurnlessTranscriptVerdict is the only non-live proof that the SIGKILL ⇒
// INCONCLUSIVE rule is wired rather than merely described. No subprocess, no
// credentials: it passes on a machine with no claude at all.
func TestTurnlessTranscriptVerdict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		aliveFound     bool
		afterExitFound bool
		ended          terminationMode
		want           string
	}{
		// A present file short-circuits every termination consideration — including
		// the SIGKILL row, which must not swallow a falsification.
		{"present-while-alive-beats-sigkill", true, false, terminationSIGKILL, "FALSIFIED"},
		{"present-while-alive-after-sigterm", true, false, terminationSIGTERM, "FALSIFIED"},
		{"present-only-after-exit", false, true, terminationSIGTERM, "FALSIFIED"},
		{"absent-both-graceful-exit", false, false, terminationSIGTERM, "HOLDS"},
		{"absent-both-force-killed", false, false, terminationSIGKILL, "INCONCLUSIVE"},
		{"absent-both-never-exited", false, false, terminationDidNotExit, "INCONCLUSIVE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, sentence := classifyTurnlessTranscript(tc.aliveFound, tc.afterExitFound, tc.ended)
			if got != tc.want {
				t.Errorf("classifyTurnlessTranscript(%t, %t, %q) verdict = %q, want %q",
					tc.aliveFound, tc.afterExitFound, tc.ended, got, tc.want)
			}
			// The sentence is the deliverable AC 4 forbids a reader from having to
			// infer, so it has to state the verdict rather than merely accompany it.
			if !strings.HasPrefix(sentence, tc.want+":") {
				t.Errorf("classifyTurnlessTranscript(%t, %t, %q) sentence = %q, want it to open with %q",
					tc.aliveFound, tc.afterExitFound, tc.ended, sentence, tc.want+":")
			}
		})
	}
}
