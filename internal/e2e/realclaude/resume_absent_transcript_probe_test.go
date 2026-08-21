//go:build e2e_realclaude

package realclaude

// #1656 — how does a real claude answer `--resume <id>` when `<id>.jsonl` is
// absent? The suspected streamsup crash-loop (observed 2026-08-18) rests on that
// answer being "non-zero exit": a respawn emits `--resume <id>` for a session
// claude has no record of, claude fails, and the daemon retries forever on a
// widening backoff. #1655 measured the other half of the premise — a turnless
// `--session-id` launch establishes no transcript. This measures the half the
// loop actually turns on. ADR 032 decided the rule; #1630 carries it into
// streamsup and consumes both measurements.
//
// Three properties drive the design, each a way the naive "run it and read the
// exit code" version would record a lie:
//
//  1. A non-zero exit attributes to nothing on its own — bad credentials, a
//     rejected argv shape and a broken workdir all exit non-zero. Hence the
//     control arm: the SAME argv builder, the same workdir and the same deadline
//     against an id whose transcript DOES exist. A control that rejects a resume
//     of an existing transcript is measuring the environment, which is why it
//     short-circuits to INCONCLUSIVE regardless of what the absent arm did.
//  2. The outcome space is wider than the verdict space. An arm may exit zero,
//     exit non-zero, or never exit at all — #1655's turnless child was still alive
//     after 30 s under the sibling argv shape. The record carries all three
//     distinctly; the classifier collapses them to one derived boolean per arm
//     (armOutcome.rejected), which is what makes the nine-cell cross-product map
//     onto three verdicts with no gap.
//  3. The outcome must be snapshotted BEFORE we signal. An arm that does not exit
//     on its own is ended with SIGTERM, after which snapshotExit reports 143 — a
//     non-zero exit that would read as a rejection the child never made.
//
// Every child-behaviour outcome is recorded and the test passes; it fails only
// for a broken instrument (no child, no established transcript, a reserved id
// that already exists).

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

const (
	// Ticket-prefixed reserved stems, per the package's fixed-id convention.
	// Under WithWorktree's pinned $HOME they are unused by construction; the
	// pre-read of the absent id is what PROVES it rather than the naming.
	resumeProbePresentID = "16560000-0000-4000-8000-000000000001" // <A>, established by a real turn
	resumeProbeAbsentID  = "16560000-0000-4000-8000-000000000002" // <C>, never established

	// Deliberately this ticket's OWN values rather than aliases of #1655's: an
	// alias would let a future edit to that file silently change the parameters
	// this run records.
	resumeProbeModel    = "claude-haiku-4-5"
	resumeProbeMaxTurns = "2"
	resumeProbePrompt   = "Reply with the single word: ok"

	// Both resume arms share one deadline, and it is recorded per arm: a claude
	// that rejects more slowly than this would be recorded as "still running" and
	// read as FALSIFIED. ADR 032 measured the sibling refusal (`--session-id` on a
	// transcript that already exists) at ~220 ms, so 45 s is two orders of
	// magnitude of headroom while staying under #1655's 60 s exit window.
	resumeProbeArmDeadline = 45 * time.Second

	resumeProbeKillGrace     = 5 * time.Second   // mirrors streamsup's killGrace
	resumeProbeHardWait      = 15 * time.Second  // post-SIGKILL bound
	resumeProbeDirBudget     = 120 * time.Second // <A>.jsonl must appear
	resumeProbeEstablishExit = 60 * time.Second  // establish arm's exit after stdin close
	resumeProbePostArmWindow = 10 * time.Second  // bounded poll for a stub <C>.jsonl

	// Outer per-child leak guards, comfortably above deadline + grace + hard. A
	// context kill is a SIGKILL, so neither must ever be the thing that ends an
	// arm — if one fires, the arm records as did-not-exit.
	resumeProbeArmBudget       = 3 * time.Minute
	resumeProbeEstablishBudget = 4 * time.Minute
)

// resumeProbeArgs returns the argv shape buildArgs (internal/streamsup) emits on a
// RESPAWN: the same fixed stream-json prefix and the same base args as
// transcriptProbeArgs, with only the trailing pair swapped to `--resume <id>`.
//
// A sibling rather than a parameter on transcriptProbeArgs, which #1655's test
// depends on unchanged. BOTH resume arms call this one function, so their argvs
// are identical by construction bar the id — which is what makes the control's
// contrast structural rather than asserted.
func resumeProbeArgs(sessionID string) []string {
	return []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", resumeProbeModel,
		"--max-turns", resumeProbeMaxTurns,
		"--resume", sessionID,
	}
}

// --- the one property the verdict reads --------------------------------------

// armOutcome is one resume arm's answer, snapshotted BEFORE any signal from us.
type armOutcome struct {
	Exited   bool // exited on its own, within the deadline
	ExitCode int  // meaningful only when Exited
}

// rejected reports whether claude REFUSED the resume. An arm still running at its
// deadline has NOT rejected it — claude accepted the argv and is sitting on stdin,
// exactly as #1655's turnless child did. The `Exited &&` guard is load-bearing:
// snapshotExit reports -1 for a child that has not exited, and -1 != 0.
func (o armOutcome) rejected() bool { return o.Exited && o.ExitCode != 0 }

// --- the record ----------------------------------------------------------------

// resumeArm is one `--resume <id>` launch as recorded. Exited/ExitCode are the
// pre-termination snapshot and the only fields the classifier reads;
// TerminationMode / TermToExitMs / PostTermExitCode are cleanup detail from the
// did-not-exit path and are empty/zero when the child exited on its own, because
// no signal was sent at all in that case.
type resumeArm struct {
	SessionID        string          `json:"session_id"`
	Argv             []string        `json:"argv"`
	DeadlineMs       int64           `json:"deadline_ms"`
	Exited           bool            `json:"exited"`
	ExitCode         int             `json:"exit_code"`
	WaitError        string          `json:"wait_error"`
	TerminationMode  terminationMode `json:"termination_mode"`
	TermToExitMs     int64           `json:"term_to_exit_ms"`
	PostTermExitCode int             `json:"post_term_exit_code"`
	Stdout           string          `json:"stdout"`
	Stderr           string          `json:"stderr"`
	CarryingStream   string          `json:"carrying_stream"`
}

// outcome is the arm as the classifier reads it — the pre-termination snapshot
// only, never the post-signal exit code.
func (a resumeArm) outcome() armOutcome {
	return armOutcome{Exited: a.Exited, ExitCode: a.ExitCode}
}

// establishArm is the turnful `--session-id <A>` launch that creates the control's
// transcript. Its stdout is NOT recorded: it is one very long stream-json line
// whose only content is "this claude version accepts the argv and completes a
// turn", which #1655's record elided for the same reason.
type establishArm struct {
	SessionID      string   `json:"session_id"`
	Argv           []string `json:"argv"`
	Prompt         string   `json:"prompt"`
	ExitCode       int      `json:"exit_code"`
	WaitError      string   `json:"wait_error"`
	TranscriptPath string   `json:"transcript_path"`
	TranscriptSize int64    `json:"transcript_size"`
	Stderr         string   `json:"stderr"`
}

type resumeProbeRecord struct {
	ClaudeVersionRaw   string `json:"claude_version_raw"`
	ClaudeVersionToken string `json:"claude_version_token"`
	ChildCwd           string `json:"child_cwd"`

	TranscriptDirEmpirical  string `json:"transcript_dir_empirical"`
	TranscriptDirRecomputed string `json:"transcript_dir_recomputed"`
	TranscriptDirMatch      bool   `json:"transcript_dir_match"`

	EstablishArm establishArm `json:"establish_arm"`

	AbsentPreReading  probeReading `json:"absent_pre_reading"`
	AbsentArm         resumeArm    `json:"absent_arm"`
	AbsentPostReading probeReading `json:"absent_post_reading"`

	ControlArm resumeArm `json:"control_arm"`

	Verdict         string `json:"verdict"`
	VerdictSentence string `json:"verdict_sentence"`
	StubSentence    string `json:"stub_sentence"`
}

// --- the arm runner --------------------------------------------------------------

// runResumeArm launches one `--resume <id>` child, waits out the deadline,
// snapshots its outcome BEFORE any signal from us, then ends whatever is left.
// One helper called twice rather than two inline copies: a launch-wait-snapshot-
// terminate sequence written twice is where the snapshot-before-signal rule gets
// applied in one copy and forgotten in the other.
//
// Never fails the test for the child's behaviour — did-not-exit is a recorded
// outcome, not a hang. Fatals only when startProbeChild fails, which is a broken
// instrument rather than a measurement.
func runResumeArm(t *testing.T, claudeBin, cwd, sessionID string, deadline time.Duration) resumeArm {
	t.Helper()
	argv := resumeProbeArgs(sessionID)
	arm := resumeArm{SessionID: sessionID, Argv: argv, DeadlineMs: deadline.Milliseconds()}

	ctx, cancel := context.WithTimeout(context.Background(), resumeProbeArmBudget)
	defer cancel()
	c, err := startProbeChild(ctx, claudeBin, cwd, argv)
	if err != nil {
		t.Fatalf("#1656[resume %s]: %v — no child, no answer to record", sessionID, err)
	}
	t.Cleanup(func() { _ = c.cmd.Process.Kill() })
	// Deferred, so stdin closes only AFTER termination. Closing it early sends
	// EOF, and a child that exits on EOF would be recorded as having answered the
	// resume when it answered the EOF.
	defer func() { _ = c.stdin.Close() }()

	if c.waitExit(deadline) {
		arm.Exited = true
		arm.ExitCode, arm.WaitError = c.snapshotExit()
	} else {
		// Still on stdin at its deadline: claude accepted the resume. Only now may
		// a signal be sent, and its exit code is cleanup detail, never the answer.
		arm.ExitCode = -1
		mode, termToExit := endTurnlessChild(c, resumeProbeKillGrace, resumeProbeHardWait)
		arm.TerminationMode = mode
		arm.TermToExitMs = termToExit.Milliseconds()
		arm.PostTermExitCode, _ = c.snapshotExit()
	}

	arm.Stdout = recordStream(c.stdout)
	arm.Stderr = recordStream(c.stderr)
	arm.CarryingStream = carryingStream(arm.Stdout, arm.Stderr)
	return arm
}

// carryingStream names which stream carried the operator-visible message, read off
// the two RECORDED strings so the name always describes what the record contains.
func carryingStream(stdout, stderr string) string {
	switch {
	case stdout != "" && stderr != "":
		return "both"
	case stderr != "":
		return "stderr"
	case stdout != "":
		return "stdout"
	default:
		return "neither"
	}
}

// --- the verdict ---------------------------------------------------------------

// classifyResumeAbsent maps the two arms' outcomes to the one sentence a reader
// must not have to infer.
//
// The control arm is checked FIRST and short-circuits: a shape that rejects a
// resume whose transcript EXISTS rejects resumes generally, so the absent arm's
// answer attributes to the environment rather than to the absence, whatever that
// answer was. The remaining two rows read the absent arm alone, where "did not
// exit within the deadline" and "exited zero" mean the same thing to the verdict
// (no rejection) and different things to the reader — which is why the record
// carries the exit code, the wait error and the did-not-exit flag verbatim while
// the classifier reads only rejected().
func classifyResumeAbsent(control, absent armOutcome) (verdict, sentence string) {
	switch {
	case control.rejected():
		return "INCONCLUSIVE", "INCONCLUSIVE: the control arm — the identical `--resume` argv against an id whose " +
			"transcript EXISTS — itself exited non-zero, so this argv shape, workdir or credential rejects resumes " +
			"generally and the absent arm's answer cannot be attributed to the missing transcript."
	case absent.rejected():
		return "HOLDS", "HOLDS: `--resume <absent-id>` exited non-zero while the identical `--resume` against an id " +
			"whose transcript exists did not, so claude rejects a resume of a transcript it has no record of and the " +
			"suspected respawn crash-loop's premise is confirmed."
	default:
		return "FALSIFIED", "FALSIFIED: `--resume <absent-id>` did not exit non-zero — it either exited zero or was " +
			"still running at its deadline — so claude does not reject a resume of a transcript it has no record of, " +
			"and the suspected respawn crash-loop cannot be explained by that rejection."
	}
}

// resumeStubSentence states what the post-arm reading of <C> means for #1630. It
// does NOT enter the verdict, which AC 2 defines purely on the two arms' exits: a
// stub is a finding about whether #1630's fix converges, not about whether claude
// rejects the resume.
func resumeStubSentence(found bool) string {
	if found {
		return "STUB PRESENT: the resume of an absent id left a <C>.jsonl behind, so #1630's by-id existence probe " +
			"would read that stub as an established transcript, choose `--resume` on every later respawn, and never " +
			"converge — the fix needs more than the by-id read."
	}
	return "NO STUB: the resume of an absent id left no <C>.jsonl behind, so #1630's by-id existence probe still " +
		"reads absent after a rejection and would fall back to `--session-id` rather than looping on `--resume`."
}

// --- the live measurement --------------------------------------------------------

func TestRealClaude_ResumeAbsentTranscript(t *testing.T) {
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("#1656: resolve claude: %v", err)
	}
	versionRaw, versionToken := captureClaudeVersion(t)

	// All three children share one workdir and therefore one test function:
	// WithWorktree pins $HOME with t.Setenv, so a second test function gets a
	// different $HOME and the directory the absence is asserted against no longer
	// exists. claude maps its transcript folder from the child's cwd, so an arm
	// launched elsewhere asks a different directory whether the id exists.
	workdir := filepath.Join(home, "resume-absent-probe-work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#1656: mkdir workdir: %v", err)
	}
	// The child's cwd gets the SAME canonicalisation production applies: streamsup
	// resolves Config.WorkDir through agentrun.ResolveWorkdir and only then assigns
	// cmd.Dir.
	childCwd, err := agentrun.ResolveWorkdir(workdir)
	if err != nil {
		t.Fatalf("#1656: resolve workdir %s: %v", workdir, err)
	}

	rec := resumeProbeRecord{
		ClaudeVersionRaw:   versionRaw,
		ClaudeVersionToken: versionToken,
		ChildCwd:           childCwd,
	}

	// --- 1. establish <A> -------------------------------------------------------
	//
	// ADR 032 records that claude REFUSES `--session-id <uuid>` when <uuid>.jsonl
	// already exists, so the turnful establish must precede anything that resumes
	// <A>. It calls #1655's transcriptProbeArgs unchanged — the first-spawn shape.
	establishArgv := transcriptProbeArgs(resumeProbePresentID)
	rec.EstablishArm = establishArm{
		SessionID: resumeProbePresentID,
		Argv:      establishArgv,
		Prompt:    resumeProbePrompt,
	}
	establishCtx, cancelEstablish := context.WithTimeout(context.Background(), resumeProbeEstablishBudget)
	defer cancelEstablish()
	establish, err := startProbeChild(establishCtx, claudeBin, childCwd, establishArgv)
	if err != nil {
		t.Fatalf("#1656[establish]: %v — without a real transcript there is no control, "+
			"and without a control a non-zero exit attributes to nothing", err)
	}
	t.Cleanup(func() { _ = establish.cmd.Process.Kill() })

	turn, err := setModeTurnLine(resumeProbePrompt)
	if err != nil {
		t.Fatalf("#1656[establish]: %v", err)
	}
	if _, err := establish.stdin.Write(turn); err != nil {
		t.Fatalf("#1656[establish]: write turn: %v\nstderr:\n%s", err, recordStream(establish.stderr))
	}

	// --- 2. locate the directory empirically ------------------------------------
	//
	// Authoritative, and AC 2's "if <A>.jsonl never appears the run fails rather
	// than recording a verdict" — streamNewSessionTranscriptDir Fatals with a full
	// projects-tree listing on timeout. DefaultClaudeSessionsDir is only ever the
	// compared-against value: it applies EvalSymlinks where the child's cwd went
	// through ResolveWorkdir's canonicalCase (the #989 hazard), so a recomputation
	// risks asserting absence against a folder claude never wrote.
	dir := streamNewSessionTranscriptDir(t, home, resumeProbePresentID, resumeProbeDirBudget)
	established := statByID(dir, resumeProbePresentID)
	rec.EstablishArm.TranscriptPath = established.Path
	rec.EstablishArm.TranscriptSize = established.Size

	recomputed := sessions.DefaultClaudeSessionsDir(childCwd)
	rec.TranscriptDirEmpirical = dir
	rec.TranscriptDirRecomputed = recomputed
	rec.TranscriptDirMatch = dir == recomputed
	// A divergence is a RECORDED OUTCOME, not a failure: it is the finding the
	// follow-up that supplies this directory on the daemon's production path needs.
	if !rec.TranscriptDirMatch {
		t.Logf("#1656: transcript dir DIVERGENCE — empirical %s vs recomputed %s (cwd %s)",
			dir, recomputed, childCwd)
	}

	_ = establish.stdin.Close()
	if !establish.waitExit(resumeProbeEstablishExit) {
		_ = establish.cmd.Process.Kill()
		establish.waitExit(resumeProbeHardWait)
	}
	rec.EstablishArm.ExitCode, rec.EstablishArm.WaitError = establish.snapshotExit()
	rec.EstablishArm.Stderr = recordStream(establish.stderr)

	// --- 3. pre-read <C> --------------------------------------------------------
	//
	// Same directory, same by-id instrument as every other read in this run. A
	// reserved id that already exists means the fixture is not what the design
	// assumes and no verdict is readable.
	rec.AbsentPreReading = statByID(dir, resumeProbeAbsentID)
	if rec.AbsentPreReading.Found {
		t.Fatalf("#1656: reserved id %s ALREADY has a transcript at %s (%d bytes) in %s before the absent "+
			"arm launched — the arm would be resuming a session that exists, so its exit code would measure "+
			"nothing about an absent transcript",
			resumeProbeAbsentID, rec.AbsentPreReading.Path, rec.AbsentPreReading.Size, dir)
	}

	// --- 4. the absent arm ------------------------------------------------------
	rec.AbsentArm = runResumeArm(t, claudeBin, childCwd, resumeProbeAbsentID, resumeProbeArmDeadline)

	// --- 5. post-read <C> -------------------------------------------------------
	//
	// Taken before the control arm runs, so no other claude process has run since
	// the absent arm ended. A file here is the stub #1630's by-id probe would flip
	// to `--resume` forever on; it gets its own sentence and does not enter the
	// verdict.
	rec.AbsentPostReading = statByIDPolled(dir, resumeProbeAbsentID, resumeProbePostArmWindow)

	// --- 6. the control resume arm ----------------------------------------------
	//
	// Same builder, same workdir, same deadline. It runs LAST so that nothing
	// between the pre-read and the absent arm can create <C>; AC 2's rule is
	// order-independent, so this costs nothing.
	rec.ControlArm = runResumeArm(t, claudeBin, childCwd, resumeProbePresentID, resumeProbeArmDeadline)

	// --- verdict and record -------------------------------------------------------
	rec.Verdict, rec.VerdictSentence = classifyResumeAbsent(rec.ControlArm.outcome(), rec.AbsentArm.outcome())
	rec.StubSentence = resumeStubSentence(rec.AbsentPostReading.Found)

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("#1656: marshal record: %v", err)
	}
	// The plain-language lines first, so the verdict survives a truncated log.
	t.Logf("#1656 VERDICT: %s", rec.VerdictSentence)
	t.Logf("#1656 STUB: %s", rec.StubSentence)
	t.Logf("#1656 absent arm (%s, deadline %s): exited=%t exit_code=%d carried on %s",
		resumeProbeAbsentID, resumeProbeArmDeadline, rec.AbsentArm.Exited, rec.AbsentArm.ExitCode,
		rec.AbsentArm.CarryingStream)
	t.Logf("#1656 control arm (%s, deadline %s): exited=%t exit_code=%d carried on %s",
		resumeProbePresentID, resumeProbeArmDeadline, rec.ControlArm.Exited, rec.ControlArm.ExitCode,
		rec.ControlArm.CarryingStream)
	t.Logf("#1656 RECORD:\n%s", data)
}

// --- the classifier, without a subprocess --------------------------------------------

// TestResumeAbsentVerdict is the only non-live proof that the control-first rule is
// wired rather than merely described. No subprocess, no credentials: it passes on a
// machine with no claude at all.
//
// All nine cells of {exit 0, exit non-zero, did-not-exit}² are present, because the
// outcome space is wider than the verdict space and every cell must have a home.
func TestResumeAbsentVerdict(t *testing.T) {
	t.Parallel()
	var (
		// ExitCode -1 mirrors what snapshotExit returns for a child that has not
		// exited, so the did-not-exit rows genuinely exercise rejected()'s
		// `Exited &&` guard rather than a conveniently zeroed code.
		didNotExit  = armOutcome{Exited: false, ExitCode: -1}
		exitedZero  = armOutcome{Exited: true, ExitCode: 0}
		exitedError = armOutcome{Exited: true, ExitCode: 1}
	)
	tests := []struct {
		name    string
		control armOutcome
		absent  armOutcome
		want    string
	}{
		// The absent arm rejected and the control did not: the contrast the whole
		// design exists to produce.
		{"control-did-not-exit-absent-rejected", didNotExit, exitedError, "HOLDS"},
		{"control-exited-zero-absent-rejected", exitedZero, exitedError, "HOLDS"},

		// The control did not reject and neither did the absent arm.
		{"control-did-not-exit-absent-exited-zero", didNotExit, exitedZero, "FALSIFIED"},
		{"control-exited-zero-absent-exited-zero", exitedZero, exitedZero, "FALSIFIED"},
		{"control-did-not-exit-absent-did-not-exit", didNotExit, didNotExit, "FALSIFIED"},
		{"control-exited-zero-absent-did-not-exit", exitedZero, didNotExit, "FALSIFIED"},

		// A control that rejects a resume whose transcript EXISTS short-circuits
		// every absent-arm answer. The first row is the one that must not read
		// HOLDS: both arms exited non-zero, which is exactly what a bad credential
		// or a rejected argv shape looks like.
		{"control-rejected-absent-rejected", exitedError, exitedError, "INCONCLUSIVE"},
		{"control-rejected-absent-exited-zero", exitedError, exitedZero, "INCONCLUSIVE"},
		{"control-rejected-absent-did-not-exit", exitedError, didNotExit, "INCONCLUSIVE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, sentence := classifyResumeAbsent(tc.control, tc.absent)
			if got != tc.want {
				t.Errorf("classifyResumeAbsent(control=%+v, absent=%+v) verdict = %q, want %q",
					tc.control, tc.absent, got, tc.want)
			}
			// The sentence is the deliverable AC 4 forbids a reader from having to
			// infer, so it has to state the verdict rather than merely accompany it.
			if !strings.HasPrefix(sentence, tc.want+":") {
				t.Errorf("classifyResumeAbsent(control=%+v, absent=%+v) sentence = %q, want it to open with %q",
					tc.control, tc.absent, sentence, tc.want+":")
			}
		})
	}
}

// TestResumeProbeArgsIsRespawnShape pins the one structural claim the two resume
// arms rest on: they differ from the establishing spawn only in the trailing pair.
// Credential-free — it compares two builders, no subprocess.
func TestResumeProbeArgsIsRespawnShape(t *testing.T) {
	t.Parallel()
	const id = resumeProbeAbsentID
	first := transcriptProbeArgs(id)
	respawn := resumeProbeArgs(id)
	if len(first) != len(respawn) {
		t.Fatalf("resumeProbeArgs(%q) = %v (len %d), want the same length as transcriptProbeArgs = %v (len %d)",
			id, respawn, len(respawn), first, len(first))
	}
	head := len(first) - 2
	for i := 0; i < head; i++ {
		if first[i] != respawn[i] {
			t.Errorf("argv[%d] = %q, want %q — only the trailing id-flag pair may differ",
				i, respawn[i], first[i])
		}
	}
	if got, want := respawn[head], "--resume"; got != want {
		t.Errorf("resumeProbeArgs id flag = %q, want %q", got, want)
	}
	if got := respawn[head+1]; got != id {
		t.Errorf("resumeProbeArgs id = %q, want %q", got, id)
	}
}
