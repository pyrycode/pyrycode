package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// --- #1475 rotation spawn-directory wiring -----------------------------------
//
// A new_session rotation re-reads the bound conversation's recorded workspace,
// re-confines it with the same validator the mint and revive paths use, and
// installs it so the successor comes up there. The runner-level proof — that a
// child actually chdirs into the installed directory — lives in
// internal/streamsup/runner_workdir_test.go, read off the spawned process itself.
// What is pinned HERE is the dispatch: which seam is asked, in what ORDER the
// install lands relative to the rotation and the respawn, and what happens when
// the recorded value is empty or refused.

const (
	rotationRecordedCwd = "/Users/someone/projects/moved"
	rotationResolvedCwd = "/Users/someone/projects/moved-resolved"
)

// movableRunner is restartFreshRunner plus the optional install capability, which
// is the production shape: streamRunner exposes both unconditionally. Every call
// appends to one steps slice, so the ORDER of rotate / install / respawn is a
// single readable observation rather than three independent ones.
type movableRunner struct {
	baseRunner
	steps      *[]string
	installed  []string
	installErr error
}

func (r *movableRunner) State() sessions.State {
	return sessions.State{Phase: sessions.PhaseRunning, ChildPID: starterLiveChildPID}
}

func (r *movableRunner) RestartFresh(sessionID string) {
	*r.steps = append(*r.steps, "restart_fresh:"+sessionID)
}

func (r *movableRunner) SetSpawnWorkDir(dir string) error {
	r.installed = append(r.installed, dir)
	if r.installErr != nil {
		*r.steps = append(*r.steps, "install_failed:"+dir)
		return r.installErr
	}
	*r.steps = append(*r.steps, "install:"+dir)
	return nil
}

// TestStartFreshRunner_InstallsBetweenRotateAndRespawn is the ordering AC: the
// directory lands AFTER the pool-side rotation committed and BEFORE the respawn
// that discards the child. Above rotate it would move a child a failed rotation
// never replaced; below RestartFresh it would race the successor's own spawn.
func TestStartFreshRunner_InstallsBetweenRotateAndRespawn(t *testing.T) {
	t.Parallel()

	var steps []string
	r := &movableRunner{steps: &steps}
	rotate := func(old sessions.SessionID) (sessions.SessionID, error) {
		steps = append(steps, "rotate:"+string(old))
		return sessions.SessionID("fresh-id"), nil
	}

	if err := startFreshRunner(r, sessions.SessionID("old-id"), rotationResolvedCwd, rotate, nil); err != nil {
		t.Fatalf("startFreshRunner = %v, want nil", err)
	}

	want := []string{"rotate:old-id", "install:" + rotationResolvedCwd, "restart_fresh:fresh-id"}
	if strings.Join(steps, "|") != strings.Join(want, "|") {
		t.Errorf("sequence = %v, want %v", steps, want)
	}
}

// A failed rotation must leave the runner's directory alone: it never replaced a
// child, so the next crash-respawn must not silently move the live one.
func TestStartFreshRunner_RotateError_InstallsNothing(t *testing.T) {
	t.Parallel()

	var steps []string
	r := &movableRunner{steps: &steps}
	wantErr := errors.New("rotation lost the race")
	rotate := func(sessions.SessionID) (sessions.SessionID, error) {
		steps = append(steps, "rotate")
		return "", wantErr
	}

	if err := startFreshRunner(r, sessions.SessionID("old-id"), rotationResolvedCwd, rotate, nil); !errors.Is(err, wantErr) {
		t.Fatalf("startFreshRunner = %v, want %v", err, wantErr)
	}
	if len(r.installed) != 0 {
		t.Errorf("installed %v after a failed rotation, want none", r.installed)
	}
}

// An empty resolved directory leaves the successor where the runner already was,
// and the rotation still completes — the "" both a never-set workspace and a
// refused one collapse to.
func TestStartFreshRunner_EmptyDir_RotatesWithoutInstalling(t *testing.T) {
	t.Parallel()

	var steps []string
	r := &movableRunner{steps: &steps}
	rotate := func(sessions.SessionID) (sessions.SessionID, error) { return sessions.SessionID("fresh-id"), nil }

	if err := startFreshRunner(r, sessions.SessionID("old-id"), "", rotate, nil); err != nil {
		t.Fatalf("startFreshRunner = %v, want nil", err)
	}
	if len(r.installed) != 0 {
		t.Errorf("installed %v for an empty directory, want none", r.installed)
	}
	if want := []string{"restart_fresh:fresh-id"}; strings.Join(steps, "|") != strings.Join(want, "|") {
		t.Errorf("sequence = %v, want %v", steps, want)
	}
}

// The install's own failure — the directory vanished between the confinement and
// this call — is Warned and swallowed. The rotation is already committed, so
// withholding the respawn would leave the conversation with no child at all.
func TestStartFreshRunner_InstallError_StillRespawns(t *testing.T) {
	t.Parallel()

	var steps []string
	var logs bytes.Buffer
	r := &movableRunner{steps: &steps, installErr: errors.New("vanished")}
	rotate := func(sessions.SessionID) (sessions.SessionID, error) { return sessions.SessionID("fresh-id"), nil }
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if err := startFreshRunner(r, sessions.SessionID("old-id"), rotationResolvedCwd, rotate, log); err != nil {
		t.Fatalf("startFreshRunner = %v, want nil — an install failure must not fail the rotation", err)
	}
	if want := "restart_fresh:fresh-id"; !strings.Contains(strings.Join(steps, "|"), want) {
		t.Errorf("sequence = %v, want it to contain %q", steps, want)
	}
	if !strings.Contains(logs.String(), "v2.new_session.spawn_dir_install_failed") {
		t.Errorf("no install-failure record; log was:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), rotationResolvedCwd) {
		t.Errorf("the record names the workspace path; it must not:\n%s", logs.String())
	}
}

// A runner that can restart but cannot be moved still rotates. Folding the
// install into startFreshRunner's RestartFresh assertion would send it to the
// inert path, where it would rotate nothing.
func TestStartFreshRunner_RunnerWithoutInstall_StillRotates(t *testing.T) {
	t.Parallel()

	r := liveRunner()
	rotated := false
	rotate := func(sessions.SessionID) (sessions.SessionID, error) {
		rotated = true
		return sessions.SessionID("fresh-id"), nil
	}

	if err := startFreshRunner(r, sessions.SessionID("old-id"), rotationResolvedCwd, rotate, nil); err != nil {
		t.Fatalf("startFreshRunner = %v, want nil", err)
	}
	if !rotated {
		t.Error("a runner without SetSpawnWorkDir did not rotate; the install must stay an optional capability")
	}
	if want := []string{"fresh-id"}; len(r.restarts) != 1 || r.restarts[0] != want[0] {
		t.Errorf("restarts = %v, want %v", r.restarts, want)
	}
}

// spawnDirProbe records what the confinement seam was asked, so a test can assert
// that an inert frame never reaches the filesystem at all.
type spawnDirProbe struct {
	asked []string
	dir   string
	err   error
}

func (p *spawnDirProbe) resolve(recorded string) (string, error) {
	p.asked = append(p.asked, recorded)
	return p.dir, p.err
}

// starterWithWorkspace builds the composition with a recorded workspace on the
// bound conversation and a confinement seam under the probe's control.
func starterWithWorkspace(t *testing.T, runner sessions.Runner, recorded string,
	sd *spawnDirProbe, logs *bytes.Buffer) activeSessionStarter {
	t.Helper()
	return activeSessionStarter{
		currentConv: func() string { return starterConvA },
		resolveBound: func(convID string) (sessions.Runner, sessions.SessionID, string, bool) {
			if runner == nil || convID != starterConvB {
				return nil, "", "", false
			}
			return runner, sessions.SessionID("session-of-" + convID), recorded, true
		},
		rotate:      func(sessions.SessionID) (sessions.SessionID, error) { return sessions.SessionID("fresh-id"), nil },
		spawnDirFor: sd.resolve,
		log:         slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// AC-1 at the composition level: the RECORDED value reaches the validator, and the
// validator's output — never the recorded text — reaches the runner.
func TestStartNewSession_ConfinesRecordedWorkspaceBeforeInstalling(t *testing.T) {
	t.Parallel()

	var steps []string
	var logs bytes.Buffer
	r := &movableRunner{steps: &steps}
	sd := &spawnDirProbe{dir: rotationResolvedCwd}
	s := starterWithWorkspace(t, r, rotationRecordedCwd, sd, &logs)

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("StartNewSession = %v, want nil", err)
	}

	if len(sd.asked) != 1 || sd.asked[0] != rotationRecordedCwd {
		t.Fatalf("confinement asked about %v, want exactly [%q]", sd.asked, rotationRecordedCwd)
	}
	if len(r.installed) != 1 || r.installed[0] != rotationResolvedCwd {
		t.Errorf("installed %v, want [%q] — the runner must receive the CONFINED path, "+
			"never the recorded one", r.installed, rotationResolvedCwd)
	}
}

// AC-4: a refused workspace leaves the successor where it was, the rotation still
// completes, and the refusal is recorded with the conversation id and no path.
func TestStartNewSession_RefusedWorkspace_RotatesAndRecords(t *testing.T) {
	t.Parallel()

	var steps []string
	var logs bytes.Buffer
	r := &movableRunner{steps: &steps}
	sd := &spawnDirProbe{err: handlers.ErrSpawnDirRejected}
	s := starterWithWorkspace(t, r, rotationRecordedCwd, sd, &logs)

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("StartNewSession = %v, want nil — a refused workspace must not fail the rotation", err)
	}

	if len(r.installed) != 0 {
		t.Errorf("installed %v after a refusal, want none — fail-closed keeps the old directory", r.installed)
	}
	if want := "restart_fresh:fresh-id"; !strings.Contains(strings.Join(steps, "|"), want) {
		t.Errorf("sequence = %v, want the rotation to complete (%q)", steps, want)
	}
	if !strings.Contains(logs.String(), "v2.new_session.spawn_dir_rejected") {
		t.Errorf("no refusal record; log was:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), starterConvB) {
		t.Errorf("the refusal record does not carry the conversation id:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), rotationRecordedCwd) {
		t.Errorf("the refusal record names the workspace path; it must not:\n%s", logs.String())
	}
}

// An unset workspace is not a refusal: nothing installs, nothing is recorded, and
// the validator is never asked — there is no path to confine.
func TestStartNewSession_EmptyRecordedWorkspace_IsSilent(t *testing.T) {
	t.Parallel()

	var steps []string
	var logs bytes.Buffer
	r := &movableRunner{steps: &steps}
	sd := &spawnDirProbe{dir: rotationResolvedCwd}
	s := starterWithWorkspace(t, r, "", sd, &logs)

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("StartNewSession = %v, want nil", err)
	}
	if len(sd.asked) != 0 {
		t.Errorf("confinement asked about %v for an unset workspace, want no call", sd.asked)
	}
	if len(r.installed) != 0 {
		t.Errorf("installed %v for an unset workspace, want none", r.installed)
	}
	if strings.Contains(logs.String(), "spawn_dir_rejected") {
		t.Errorf("an unset workspace was recorded as a refusal:\n%s", logs.String())
	}
}

// Every inert arm must reach the filesystem zero times. resolveSpawnDir creates a
// directory and writes ~/.claude.json, so a frame that will not rotate must never
// drive it — which is why the resolve sits BELOW the inert returns.
func TestStartNewSession_InertArms_NeverConfine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		runner sessions.Runner
		convID string
	}{
		{"named conversation is not canonical", liveRunner(), "not-a-uuid"},
		{"named conversation has no bound session", nil, starterConvB},
		{"bound runner cannot restart", &baseRunner{}, starterConvB},
		{"named conversation has no live child", &restartFreshRunner{}, starterConvB},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			sd := &spawnDirProbe{dir: rotationResolvedCwd}
			s := starterWithWorkspace(t, tc.runner, rotationRecordedCwd, sd, &logs)

			if err := s.StartNewSession(tc.convID); err != nil {
				t.Fatalf("StartNewSession = %v, want nil (inert)", err)
			}
			if len(sd.asked) != 0 {
				t.Errorf("confinement asked about %v on an inert arm, want no call", sd.asked)
			}
		})
	}
}

// A nil spawnDirFor leaves every successor where its runner already is — the
// pre-#1475 behaviour an unwired literal degrades to, rather than a panic on a
// remotely-driven path.
func TestStartNewSession_NilSpawnDirSeam_RotatesWithoutInstalling(t *testing.T) {
	t.Parallel()

	var steps []string
	var logs bytes.Buffer
	r := &movableRunner{steps: &steps}
	s := starterWithWorkspace(t, r, rotationRecordedCwd, &spawnDirProbe{}, &logs)
	s.spawnDirFor = nil

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("StartNewSession = %v, want nil", err)
	}
	if len(r.installed) != 0 {
		t.Errorf("installed %v with no confinement seam wired, want none", r.installed)
	}
	if want := "restart_fresh:fresh-id"; !strings.Contains(strings.Join(steps, "|"), want) {
		t.Errorf("sequence = %v, want the rotation to complete (%q)", steps, want)
	}
}
