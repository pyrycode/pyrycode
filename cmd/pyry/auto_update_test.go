package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/update"
)

func TestDaemonIdle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	quiet := 15 * time.Minute

	tests := []struct {
		name       string
		turnOpen   bool
		lastActive []time.Time
		want       bool
	}{
		{name: "no_sessions", want: true},
		{name: "all_quiet", lastActive: []time.Time{now.Add(-time.Hour), now.Add(-16 * time.Minute)}, want: true},
		{name: "exactly_at_window_edge", lastActive: []time.Time{now.Add(-quiet)}, want: true},
		{name: "one_inside_window", lastActive: []time.Time{now.Add(-time.Hour), now.Add(-time.Minute)}, want: false},
		{name: "turn_open_all_quiet", turnOpen: true, lastActive: []time.Time{now.Add(-time.Hour)}, want: false},
		{name: "turn_open_no_sessions", turnOpen: true, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := daemonIdle(tc.turnOpen, tc.lastActive, now, quiet); got != tc.want {
				t.Errorf("daemonIdle = %v, want %v", got, tc.want)
			}
		})
	}
}

// autoUpdateFixture is a signed fake release plus the counters and log buffer
// the check-level tests assert on.
type autoUpdateFixture struct {
	target    string
	newBytes  []byte
	requests  atomic.Int32 // every request to the release server
	assetHits atomic.Int32 // requests under /releases/download/
	logs      bytes.Buffer
	restarts  [][]string
	// logsAtRestart is the log buffer as it stood when runRestart was called.
	logsAtRestart string
}

// newAutoUpdateFixture serves latestJSON at the latest-release endpoint and a
// v0.9.2 release whose checksums.txt is signed by signer. The target binary
// holds "OLD pyry bytes".
func newAutoUpdateFixture(t *testing.T, latestJSON string, signer ed25519.PrivateKey) (*autoUpdateFixture, *autoUpdater) {
	t.Helper()
	f := &autoUpdateFixture{newBytes: []byte("\x7fELF...new pyry bytes...")}
	asset, tgz, checksums := fakeRelease(t, "v0.9.2", runtime.GOOS, runtime.GOARCH, f.newBytes)
	sig := ed25519.Sign(signer, []byte(checksums))

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/pyrycode/pyrycode/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(latestJSON))
	})
	files := map[string][]byte{asset: tgz, "checksums.txt": []byte(checksums), "checksums.txt.sig": sig}
	mux.HandleFunc("/releases/download/v0.9.2/", func(w http.ResponseWriter, r *http.Request) {
		f.assetHits.Add(1)
		data, ok := files[strings.TrimPrefix(r.URL.Path, "/releases/download/v0.9.2/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	f.target = filepath.Join(t.TempDir(), "pyry")
	if err := os.WriteFile(f.target, []byte("OLD pyry bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &autoUpdater{
		opts: updateOptions{
			currentVersion: "0.9.1",
			goos:           runtime.GOOS,
			goarch:         runtime.GOARCH,
			repo:           "pyrycode/pyrycode",
			releaseBaseURL: srv.URL + "/releases/download",
			fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
			executablePath: func() string { return f.target },
			replace:        update.AtomicReplace,
			signingPubKey:  testSigningPub,
			out:            &bytes.Buffer{},
			probeRestart:   func() update.RestartProbe { return update.RestartProbe{SystemdUnitExists: true} },
			runRestart: func(_ context.Context, argv []string) error {
				f.restarts = append(f.restarts, argv)
				f.logsAtRestart = f.logs.String()
				return nil
			},
		},
		idle:        func() bool { return true },
		logger:      slog.New(slog.NewJSONHandler(&f.logs, nil)),
		startDelay:  time.Hour,
		interval:    time.Hour,
		restartPoll: time.Millisecond,
	}
	return f, a
}

// onlyCheckRecord returns the one "auto-update check" record written so far, failing
// unless there is exactly one.
func (f *autoUpdateFixture) onlyCheckRecord(t *testing.T) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(f.logs.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["msg"] == "auto-update check" {
			found = append(found, rec)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d auto-update check records, want exactly 1; logs:\n%s", len(found), f.logs.String())
	}
	return found[0]
}

func (f *autoUpdateFixture) assertTargetUntouched(t *testing.T) {
	t.Helper()
	got, err := os.ReadFile(f.target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "OLD pyry bytes" {
		t.Errorf("target = %q, want it untouched", got)
	}
}

const latestV092 = `{"tag_name":"v0.9.2","draft":false,"prerelease":false}`

func TestAutoUpdateCheck_InstallsWhenIdle(t *testing.T) {
	t.Parallel()
	f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)

	if !a.check(t.Context()) {
		t.Fatalf("check = false, want installed")
	}
	rec := f.onlyCheckRecord(t)
	if rec["outcome"] != "installed" || rec["latest"] != "v0.9.2" || rec["current"] != "0.9.1" {
		t.Errorf("record = %v, want outcome installed, latest v0.9.2, current 0.9.1", rec)
	}
	got, _ := os.ReadFile(f.target)
	if !bytes.Equal(got, f.newBytes) {
		t.Errorf("target = %q, want the new release's binary", got)
	}
	prev, err := os.ReadFile(filepath.Join(filepath.Dir(f.target), "pyry.prev"))
	if err != nil || string(prev) != "OLD pyry bytes" {
		t.Errorf("pyry.prev = %q (%v), want the replaced binary", prev, err)
	}
	if len(f.restarts) != 1 || strings.Join(f.restarts[0], " ") != "systemctl --user restart pyry" {
		t.Fatalf("restarts = %v, want one systemctl restart", f.restarts)
	}
	if !strings.Contains(f.logsAtRestart, `"outcome":"installed"`) {
		t.Errorf("installed line not logged before the restart was issued; logs then:\n%s", f.logsAtRestart)
	}
}

func TestAutoUpdateCheck_BadSignatureInstallsNothing(t *testing.T) {
	t.Parallel()
	wrongKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x5c}, ed25519.SeedSize))
	f, a := newAutoUpdateFixture(t, latestV092, wrongKey)
	prevPath := filepath.Join(filepath.Dir(f.target), "pyry.prev")
	if err := os.WriteFile(prevPath, []byte("EARLIER prev"), 0o755); err != nil {
		t.Fatal(err)
	}

	if a.check(t.Context()) {
		t.Fatalf("check = true, want nothing installed")
	}
	rec := f.onlyCheckRecord(t)
	if rec["outcome"] != "failed" || !strings.Contains(fmt.Sprint(rec["err"]), "verify signature") {
		t.Errorf("record = %v, want outcome failed with a signature error", rec)
	}
	f.assertTargetUntouched(t)
	if prev, _ := os.ReadFile(prevPath); string(prev) != "EARLIER prev" {
		t.Errorf("pyry.prev = %q, want it untouched", prev)
	}
	if len(f.restarts) != 0 {
		t.Errorf("restarts = %v, want none", f.restarts)
	}
}

func TestAutoUpdateCheck_WaitsForIdle(t *testing.T) {
	t.Parallel()
	f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	a.idle = func() bool { return false }

	if a.check(t.Context()) {
		t.Fatalf("check = true, want nothing installed")
	}
	if rec := f.onlyCheckRecord(t); rec["outcome"] != "waiting_for_idle" {
		t.Errorf("outcome = %v, want waiting_for_idle", rec["outcome"])
	}
	if n := f.assetHits.Load(); n != 0 {
		t.Errorf("asset requests = %d, want none while busy", n)
	}
	f.assertTargetUntouched(t)
	if len(f.restarts) != 0 {
		t.Errorf("restarts = %v, want none", f.restarts)
	}
}

// A turn that opens while the release downloads holds the restart, not the
// install: the restart waits until the daemon is idle again.
func TestAutoUpdateCheck_RestartWaitsForIdle(t *testing.T) {
	t.Parallel()
	f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	answers := []bool{true, false, false, true}
	var calls int
	a.idle = func() bool {
		v := answers[min(calls, len(answers)-1)]
		calls++
		return v
	}

	if !a.check(t.Context()) {
		t.Fatalf("check = false, want installed")
	}
	if calls != len(answers) || len(f.restarts) != 1 {
		t.Errorf("idle asked %d times, restarts %d; want %d asks then one restart", calls, len(f.restarts), len(answers))
	}
}

func TestAutoUpdateCheck_Outcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		latest       string
		current      string
		exe          string
		probe        update.RestartProbe
		wantOutcome  string
		wantReason   string
		wantRequests bool
	}{
		{name: "up_to_date", latest: latestV092, current: "0.9.2", probe: update.RestartProbe{SystemdUnitExists: true},
			wantOutcome: "up_to_date", wantRequests: true},
		{name: "older_latest", latest: latestV092, current: "0.9.3", probe: update.RestartProbe{SystemdUnitExists: true},
			wantOutcome: "skipped", wantReason: "downgrade", wantRequests: true},
		{name: "draft", latest: `{"tag_name":"v0.9.2","draft":true}`, current: "0.9.1", probe: update.RestartProbe{SystemdUnitExists: true},
			wantOutcome: "skipped", wantReason: "draft", wantRequests: true},
		{name: "dev_build", latest: latestV092, current: "dev", probe: update.RestartProbe{SystemdUnitExists: true},
			wantOutcome: "skipped", wantReason: "not a release", wantRequests: true},
		{name: "no_managed_unit", latest: latestV092, current: "0.9.1",
			wantOutcome: "skipped", wantReason: "no managed unit"},
		{name: "homebrew", latest: latestV092, current: "0.9.1", exe: "/opt/homebrew/bin/pyry", probe: update.RestartProbe{SystemdUnitExists: true},
			wantOutcome: "skipped", wantReason: "homebrew"},
		{name: "malformed_release", latest: `not json`, current: "0.9.1", probe: update.RestartProbe{SystemdUnitExists: true},
			wantOutcome: "failed", wantRequests: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, a := newAutoUpdateFixture(t, tc.latest, testSigningPriv)
			a.opts.currentVersion = tc.current
			a.opts.probeRestart = func() update.RestartProbe { return tc.probe }
			if tc.exe != "" {
				a.opts.executablePath = func() string { return tc.exe }
			}

			if a.check(t.Context()) {
				t.Fatalf("check = true, want nothing installed")
			}
			rec := f.onlyCheckRecord(t)
			if rec["outcome"] != tc.wantOutcome || !strings.Contains(fmt.Sprint(rec["reason"]), tc.wantReason) {
				t.Errorf("record = %v, want outcome %q with reason containing %q", rec, tc.wantOutcome, tc.wantReason)
			}
			if got := f.requests.Load() > 0; got != tc.wantRequests {
				t.Errorf("made requests = %v, want %v", got, tc.wantRequests)
			}
			if n := f.assetHits.Load(); n != 0 {
				t.Errorf("asset requests = %d, want none", n)
			}
			f.assertTargetUntouched(t)
			if len(f.restarts) != 0 {
				t.Errorf("restarts = %v, want none", f.restarts)
			}
		})
	}
}

func TestAutoUpdater_RunStopsOnCancel(t *testing.T) {
	t.Parallel()
	f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("requests = %d, want none before the start delay", n)
	}
}

// Run checks after the start delay and stops after a check that installed.
func TestAutoUpdater_RunStopsAfterInstall(t *testing.T) {
	t.Parallel()
	f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	a.startDelay = time.Millisecond
	a.interval = time.Millisecond

	done := make(chan error, 1)
	go func() { done <- a.Run(t.Context()) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after installing")
	}
	f.onlyCheckRecord(t)
	if len(f.restarts) != 1 {
		t.Errorf("restarts = %d, want 1", len(f.restarts))
	}
}

// A binary already holding the release (a second daemon sharing it installed
// first) is not written again, so pyry.prev keeps the real previous build.
func TestAutoUpdateCheck_AlreadyOnDiskKeepsPrev(t *testing.T) {
	t.Parallel()
	f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	if err := os.WriteFile(f.target, f.newBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	prevPath := filepath.Join(filepath.Dir(f.target), "pyry.prev")
	if err := os.WriteFile(prevPath, []byte("EARLIER prev"), 0o755); err != nil {
		t.Fatal(err)
	}

	if !a.check(t.Context()) {
		t.Fatalf("check = false, want installed")
	}
	if prev, _ := os.ReadFile(prevPath); string(prev) != "EARLIER prev" {
		t.Errorf("pyry.prev = %q, want it kept", prev)
	}
	if len(f.restarts) != 1 {
		t.Errorf("restarts = %d, want 1: the running process is still the old build", len(f.restarts))
	}
}
