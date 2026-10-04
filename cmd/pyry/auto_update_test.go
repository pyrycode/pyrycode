package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
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
	latest    atomic.Value // latest-release JSON may change during the idle wait
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
	f.latest.Store(latestJSON)
	asset, tgz, checksums := fakeRelease(t, "v0.9.2", runtime.GOOS, runtime.GOARCH, f.newBytes)
	sig := ed25519.Sign(signer, []byte(checksums))

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/pyrycode/pyrycode/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(f.latest.Load().(string)))
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

// checkRecords returns the scheduled check records, excluding restart errors.
func (f *autoUpdateFixture) checkRecords(t *testing.T) []map[string]any {
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
	return found
}

func (f *autoUpdateFixture) onlyCheckRecord(t *testing.T) map[string]any {
	t.Helper()
	found := f.checkRecords(t)
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
	_, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	idle := false
	a.idle = func() bool { return idle }
	a.wait = func(_ context.Context, d time.Duration) bool {
		if d != a.restartPoll {
			t.Fatalf("idle wait = %v, want %v", d, a.restartPoll)
		}
		idle = true
		return true
	}
	if !a.check(t.Context()) {
		t.Fatal("busy check discarded the eligible release instead of polling idle")
	}
}

// The pending tag stays selected even if latest changes across a retry boundary.
func TestAutoUpdater_RetainsReleaseWhileBusy(t *testing.T) {
	t.Parallel()
	wrongKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x5c}, ed25519.SeedSize))
	for _, tc := range []struct {
		name    string
		signer  ed25519.PrivateKey
		outcome string
	}{
		{name: "installed", signer: testSigningPriv, outcome: "installed"},
		{name: "failed", signer: wrongKey, outcome: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, a := newAutoUpdateFixture(t, latestV092, tc.signer)
			a.startDelay, a.interval, a.restartPoll = autoUpdateStartDelay, autoUpdateInterval, autoUpdateRestartPoll
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			idle, polls, retries := false, 0, 0
			a.idle = func() bool { return idle }
			a.wait = func(_ context.Context, d time.Duration) bool {
				switch d {
				case autoUpdateStartDelay:
					if n := f.requests.Load(); n != 0 {
						t.Fatalf("requests before startup delay = %d", n)
					}
				case autoUpdateRestartPoll:
					polls++
					if n := f.requests.Load(); n != 1 || f.assetHits.Load() != 0 {
						t.Fatalf("busy requests = %d, assets = %d; want one metadata request only", n, f.assetHits.Load())
					}
					if rec := f.onlyCheckRecord(t); rec["outcome"] != "waiting_for_idle" {
						t.Fatalf("busy record = %v", rec)
					}
					f.assertTargetUntouched(t)
					f.latest.Store(`{"tag_name":"v0.9.3"}`)
					// Cross the four-hour boundary with the original release pending.
					idle = time.Duration(polls)*d > autoUpdateInterval
				case autoUpdateInterval:
					retries++
					if tc.outcome != "failed" || f.assetHits.Load() != 3 {
						t.Fatal("retry before the selected release's failed installation completed")
					}
					cancel()
					return false
				default:
					t.Fatalf("unexpected wait %v", d)
				}
				return true
			}
			if err := a.Run(ctx); err != nil {
				t.Fatal(err)
			}
			if time.Duration(polls)*autoUpdateRestartPoll != autoUpdateInterval+autoUpdateRestartPoll {
				t.Fatalf("idle polls = %d", polls)
			}
			recs := f.checkRecords(t)
			if len(recs) != 2 || recs[1]["outcome"] != tc.outcome || recs[1]["latest"] != "v0.9.2" {
				t.Fatalf("records = %v; want wait then %s for selected v0.9.2", recs, tc.outcome)
			}
			if f.requests.Load() != 4 || f.assetHits.Load() != 3 {
				t.Fatalf("requests = %d, assets = %d; want selected release only", f.requests.Load(), f.assetHits.Load())
			}
			if tc.outcome == "installed" {
				if retries != 0 || len(f.restarts) != 1 {
					t.Fatalf("retries = %d, restarts = %v", retries, f.restarts)
				}
				got, err := os.ReadFile(f.target)
				if err != nil || !bytes.Equal(got, f.newBytes) {
					t.Fatalf("target = %q (%v), want selected release", got, err)
				}
			} else {
				if retries != 1 || len(f.restarts) != 0 {
					t.Fatalf("retries = %d, restarts = %v", retries, f.restarts)
				}
				f.assertTargetUntouched(t)
			}
		})
	}
}

func TestAutoUpdater_CancelIdleWait(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"install", "restart"} {
		for _, ready := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/poll_ready_%v", phase, ready), func(t *testing.T) {
				t.Parallel()
				f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
				a.startDelay, a.restartPoll = autoUpdateStartDelay, autoUpdateRestartPoll
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				idle, polls := phase == "restart", 0
				a.idle = func() bool { return idle }
				replaces := 0
				a.opts.replace = func(target string, data []byte, mode os.FileMode) error {
					replaces++
					idle = false // a turn opens during installation
					return update.AtomicReplace(target, data, mode)
				}
				a.wait = func(_ context.Context, d time.Duration) bool {
					if d == autoUpdateStartDelay {
						return true
					}
					if d != autoUpdateRestartPoll {
						t.Fatalf("unexpected wait %v", d)
					}
					polls++
					idle = true
					cancel() // cancellation is already visible as the poll wakes
					return ready
				}
				if err := a.Run(ctx); err != nil {
					t.Fatal(err)
				}
				if polls != 1 || len(f.restarts) != 0 {
					t.Fatalf("polls = %d, restarts = %v", polls, f.restarts)
				}
				want := "installed"
				if phase == "install" {
					want = "waiting_for_idle"
					if f.requests.Load() != 1 || f.assetHits.Load() != 0 || replaces != 0 {
						t.Fatalf("cancelled wait: requests = %d, assets = %d, replaces = %d", f.requests.Load(), f.assetHits.Load(), replaces)
					}
					f.assertTargetUntouched(t)
				}
				if rec := f.onlyCheckRecord(t); rec["outcome"] != want {
					t.Fatalf("record = %v, want %s", rec, want)
				}
			})
		}
	}
}

func TestAutoUpdater_CancelBusyTimer(t *testing.T) {
	t.Parallel()
	f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	a.startDelay, a.restartPoll = 0, autoUpdateRestartPoll
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	busy := make(chan struct{})
	a.idle = func() bool {
		close(busy)
		return false
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-busy:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not enter idle wait")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("busy timer did not stop on cancellation")
	}
	if f.requests.Load() != 1 || f.assetHits.Load() != 0 || len(f.restarts) != 0 {
		t.Fatalf("requests = %d, assets = %d, restarts = %v", f.requests.Load(), f.assetHits.Load(), f.restarts)
	}
	f.assertTargetUntouched(t)
}

func TestAutoUpdater_RetryAfterCompletedCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		outcome string
		latest  string
		setup   func(*autoUpdater)
	}{
		{name: "up_to_date", outcome: "up_to_date", setup: func(a *autoUpdater) { a.opts.currentVersion = "0.9.2" }},
		{name: "ineligible", outcome: "skipped", setup: func(a *autoUpdater) { a.opts.currentVersion = "dev" }},
		{name: "homebrew", outcome: "skipped", setup: func(a *autoUpdater) { a.opts.executablePath = func() string { return "/opt/homebrew/bin/pyry" } }},
		{name: "unmanaged", outcome: "skipped", setup: func(a *autoUpdater) {
			a.opts.probeRestart = func() update.RestartProbe { return update.RestartProbe{} }
		}},
		{name: "metadata_fetch", outcome: "failed", setup: func(a *autoUpdater) { a.opts.fetcher.BaseURL = ":invalid" }},
		{name: "metadata_parse", outcome: "failed", latest: "not json"},
		{name: "install", outcome: "failed", setup: func(a *autoUpdater) {
			a.opts.replace = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
			if tc.setup != nil {
				tc.setup(a)
			}
			if tc.latest != "" {
				f.latest.Store(tc.latest)
			}
			a.startDelay, a.interval = autoUpdateStartDelay, autoUpdateInterval
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			waits := 0
			a.wait = func(_ context.Context, d time.Duration) bool {
				waits++
				if waits == 1 {
					if d != autoUpdateStartDelay || f.requests.Load() != 0 || f.logs.Len() != 0 {
						t.Fatalf("startup: wait %v, requests %d, logs %s", d, f.requests.Load(), f.logs.String())
					}
					return true
				}
				if waits != 2 || d != autoUpdateInterval {
					t.Fatalf("retry wait %v (call %d), want four hours", d, waits)
				}
				// A terminal record proves the check completed before retry time starts.
				if rec := f.onlyCheckRecord(t); rec["outcome"] != tc.outcome {
					t.Fatalf("record = %v, want %s", rec, tc.outcome)
				}
				cancel()
				return false
			}
			if err := a.Run(ctx); err != nil || waits != 2 {
				t.Fatalf("Run = %v, waits = %d", err, waits)
			}
			f.assertTargetUntouched(t)
			if len(f.restarts) != 0 {
				t.Fatalf("restarts = %v", f.restarts)
			}
		})
	}
}

// A turn that opens while the release downloads holds the restart, not the
// install: the restart waits until the daemon is idle again.
func TestAutoUpdateCheck_RestartWaitsForIdle(t *testing.T) {
	t.Parallel()
	f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	waits := 0
	a.wait = func(_ context.Context, d time.Duration) bool {
		if d != a.restartPoll {
			t.Fatalf("restart wait = %v, want %v", d, a.restartPoll)
		}
		waits++
		return true
	}
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
	if calls != len(answers) || waits != 2 || len(f.restarts) != 1 {
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
	waits := 0
	a.wait = func(_ context.Context, d time.Duration) bool {
		waits++
		if waits != 1 || d != a.startDelay {
			t.Fatalf("wait after install: %v (call %d)", d, waits)
		}
		return true
	}
	restart := a.opts.runRestart
	a.opts.runRestart = func(ctx context.Context, argv []string) error {
		_ = restart(ctx, argv)
		return errors.New("restart failed")
	}
	if err := a.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.onlyCheckRecord(t)
	if waits != 1 || len(f.restarts) != 1 || f.requests.Load() != 4 {
		t.Fatalf("waits = %d, restarts = %d, requests = %d", waits, len(f.restarts), f.requests.Load())
	}
	if !strings.Contains(f.logs.String(), "auto-update restart failed") {
		t.Fatal("missing restart failure log")
	}
	prev, err := os.ReadFile(filepath.Join(filepath.Dir(f.target), "pyry.prev"))
	if err != nil || string(prev) != "OLD pyry bytes" {
		t.Fatalf("pyry.prev = %q (%v), want original binary", prev, err)
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
