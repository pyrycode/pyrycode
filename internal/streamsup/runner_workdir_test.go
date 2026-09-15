package streamsup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The spawn-workdir swap (#1475). SetSpawnWorkDir makes the child's working
// directory a spawn INPUT — snapshotted in beginSpawn beside args, the id pair
// and spawnMode — so a new_session rotation can bring the successor up in the
// workspace the operator recorded on the conversation.
//
// Every assertion here reads the SPAWNED PROCESS's own working directory rather
// than a Runner field: the "record_cwd_block" helper writes a marker at a
// relative path, so the marker's location IS the child's cwd and its content is
// the child's own os.Getwd(). A test that read r.workDir back would pass against
// a runner that stored the value and chdir'd nowhere, which is the whole failure
// this ticket exists to close on the change_workspace path.

const (
	workdirMarkerPrefix = "spawn-cwd"
	// freshRotationID is the successor id RestartFresh rotates to. Distinct from
	// testSessionID so a marker names WHICH spawn wrote it.
	freshRotationID = "99999999-8888-7777-6666-555555555555"
)

// cwdMarkerCfg wires a runner whose every child records its own working
// directory into dir-local markers named spawn-cwd-<session id>.txt.
func cwdMarkerCfg(t *testing.T, workDir string, stdout, stderr *safeBuffer) Config {
	t.Helper()
	cfg := helperRunCfg(t, "record_cwd_block", stdout, stderr,
		"GO_STREAMSUP_HELPER_CWD_MARKER="+workdirMarkerPrefix)
	cfg.WorkDir = workDir
	cfg.BackoffInitial = 10 * time.Millisecond
	cfg.BackoffMax = 10 * time.Millisecond
	return cfg
}

// waitForMarker polls dir for the marker spawn id wrote, returning the child's
// reported cwd. It fails the test rather than returning an error so the call
// site stays a statement.
func waitForMarker(t *testing.T, dir, id string, timeout time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, workdirMarkerPrefix+"-"+id+".txt")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, _ := os.ReadDir(dir)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Fatalf("timed out waiting for marker %q; %s holds %v", path, dir, names)
	return ""
}

// markerAbsent reports that no child has written spawn id's marker into dir.
func markerAbsent(t *testing.T, dir, id string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, workdirMarkerPrefix+"-"+id+".txt"))
	return os.IsNotExist(err)
}

// AC-1 at the runner level: after a workdir install the NEXT spawn's child is
// actually running in the installed directory, as the child itself reports.
func TestSetSpawnWorkDir_SuccessorSpawnsInInstalledDir(t *testing.T) {
	var stdout, stderr safeBuffer
	dirA, dirB := t.TempDir(), t.TempDir()
	r, err := New(cwdMarkerCfg(t, dirA, &stdout, &stderr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); _ = join() }()

	waitForMarker(t, dirA, testSessionID, 5*time.Second)

	if err := r.SetSpawnWorkDir(dirB, filepath.Join(dirB, "sessions")); err != nil {
		t.Fatalf("SetSpawnWorkDir(%q): %v", dirB, err)
	}
	r.RestartFresh(freshRotationID)

	got := waitForMarker(t, dirB, freshRotationID, 5*time.Second)
	// The child's own os.Getwd() must name the installed directory, not merely
	// have landed in it: EvalSymlinks on the expectation because macOS reports
	// /private/var for a /var TempDir.
	wantReal, err := filepath.EvalSymlinks(dirB)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dirB, err)
	}
	if got != wantReal {
		t.Errorf("successor child reported cwd %q, want %q", got, wantReal)
	}
	if !markerAbsent(t, dirA, freshRotationID) {
		t.Errorf("successor also wrote a marker into the pre-install directory %q", dirA)
	}
}

// AC-4 at the runner level: a rotation with no workdir install leaves the
// successor in the directory the runner already had.
func TestRestartFresh_WithoutInstall_KeepsExistingDir(t *testing.T) {
	var stdout, stderr safeBuffer
	dirA, dirB := t.TempDir(), t.TempDir()
	r, err := New(cwdMarkerCfg(t, dirA, &stdout, &stderr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); _ = join() }()

	waitForMarker(t, dirA, testSessionID, 5*time.Second)
	r.RestartFresh(freshRotationID)

	waitForMarker(t, dirA, freshRotationID, 5*time.Second)
	if !markerAbsent(t, dirB, freshRotationID) {
		t.Errorf("successor spawned in %q although nothing was installed", dirB)
	}
}

// AC-2 at the runner level: installing a directory does not move a LIVE child.
// The swap is a promise about the next spawn only, so the running child keeps
// writing nothing new and its marker stays where it was.
func TestSetSpawnWorkDir_DoesNotMoveLiveChild(t *testing.T) {
	var stdout, stderr safeBuffer
	dirA, dirB := t.TempDir(), t.TempDir()
	r, err := New(cwdMarkerCfg(t, dirA, &stdout, &stderr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); _ = join() }()

	waitForMarker(t, dirA, testSessionID, 5*time.Second)
	pidBefore := r.State().ChildPID

	if err := r.SetSpawnWorkDir(dirB, filepath.Join(dirB, "sessions")); err != nil {
		t.Fatalf("SetSpawnWorkDir(%q): %v", dirB, err)
	}
	// Long enough for a respawn to have happened had the install triggered one;
	// the helper child blocks until SIGTERM, so it never self-exits.
	time.Sleep(300 * time.Millisecond)

	if got := r.State().ChildPID; got != pidBefore {
		t.Errorf("live child pid changed %d → %d; the install restarted it", pidBefore, got)
	}
	entries, err := os.ReadDir(dirB)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dirB, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), workdirMarkerPrefix) {
			t.Errorf("a child ran in the installed directory %q before any rotation (%s)", dirB, e.Name())
		}
	}
}

// The transcript folder moves WITH the directory, in one restartMu section.
// Left behind it would name the pre-move projects folder, which is both the
// create/resume probe's input (a permanent respawn loop) and the context-usage
// reader's (a zero-token reading for exactly the moved conversations).
func TestSetSpawnWorkDir_SwapsClaudeSessionsDirToo(t *testing.T) {
	var stdout, stderr safeBuffer
	dirA, dirB := t.TempDir(), t.TempDir()
	cfg := cwdMarkerCfg(t, dirA, &stdout, &stderr)
	sessionsA := filepath.Join(dirA, "projects")
	cfg.ClaudeSessionsDir = sessionsA
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.ClaudeSessionsDir(); got != sessionsA {
		t.Fatalf("ClaudeSessionsDir before install = %q, want %q", got, sessionsA)
	}

	sessionsB := filepath.Join(dirB, "projects")
	if err := r.SetSpawnWorkDir(dirB, sessionsB); err != nil {
		t.Fatalf("SetSpawnWorkDir: %v", err)
	}
	if got := r.ClaudeSessionsDir(); got != sessionsB {
		t.Errorf("ClaudeSessionsDir after install = %q, want %q", got, sessionsB)
	}
}

// Refusals leave BOTH fields untouched — fail-closed on the directory the
// runner already had, never a half-applied pair.
func TestSetSpawnWorkDir_Rejected(t *testing.T) {
	var stdout, stderr safeBuffer
	dirA := t.TempDir()
	cfg := cwdMarkerCfg(t, dirA, &stdout, &stderr)
	sessionsA := filepath.Join(dirA, "projects")
	cfg.ClaudeSessionsDir = sessionsA
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tests := []struct {
		name    string
		workDir string
		wantErr bool
	}{
		{"non-existent directory", filepath.Join(dirA, "does-not-exist"), true},
		{"empty is an ignored no-op", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.SetSpawnWorkDir(tt.workDir, filepath.Join(tt.workDir, "projects"))
			if (err != nil) != tt.wantErr {
				t.Fatalf("SetSpawnWorkDir(%q) error = %v, wantErr %v", tt.workDir, err, tt.wantErr)
			}
			if got := r.ClaudeSessionsDir(); got != sessionsA {
				t.Errorf("ClaudeSessionsDir = %q, want the pre-call %q — a refused install moved a field", got, sessionsA)
			}
		})
	}

	// The directory field is unreadable from outside, so the spawn itself is the
	// assertion: the child still comes up in dirA.
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); _ = join() }()
	waitForMarker(t, dirA, testSessionID, 5*time.Second)
}
