package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/update"
)

// buildTarGzForTest is a local copy of internal/update/install_test.go's
// helper. Inline-copied (rather than shared) — the test surface is ~10 lines
// and an internal/testutil package would be heavier than the duplication.
func buildTarGzForTest(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, data := range files {
		hdr := &tar.Header{
			Name:     name,
			Size:     int64(len(data)),
			Mode:     0o755,
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%q): %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("Write(%q): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar.Close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip.Close: %v", err)
	}
	return buf.Bytes()
}

// fakeRelease produces a tar.gz containing a single "pyry" entry, plus a
// matching checksums.txt body that lists the produced asset's sha256.
func fakeRelease(t *testing.T, version, goos, goarch string, pyryBytes []byte) (asset string, tgz []byte, checksums string) {
	t.Helper()
	a, err := update.AssetName(version, goos, goarch)
	if err != nil {
		t.Fatalf("AssetName: %v", err)
	}
	tgz = buildTarGzForTest(t, map[string][]byte{"pyry": pyryBytes})
	sum := sha256.Sum256(tgz)
	return a, tgz, fmt.Sprintf("%x  %s\n", sum[:], a)
}

// newFakeReleaseServer hosts the GitHub-API latest-release endpoint plus the
// download URLs the wiring code derives from releaseBaseURL. Routes match the
// production templates exactly so test failures localize cleanly.
func newFakeReleaseServer(t *testing.T, latest, asset string, tgz, checksums []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/pyrycode/pyrycode/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q}`, latest)
	})
	mux.HandleFunc("/releases/download/"+latest+"/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tgz)
	})
	mux.HandleFunc("/releases/download/"+latest+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(checksums)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// TestUpdate_Success exercises AC #2: fetch + verify + extract + replace.
// The on-disk binary at the tempdir target is overwritten with the new
// bytes, and the documented "==> Updated to <v>." progress line prints.
func TestUpdate_Success(t *testing.T) {
	newBytes := []byte("\x7fELF...new pyry bytes...")
	asset, tgz, checksums := fakeRelease(t, "v0.9.2", runtime.GOOS, runtime.GOARCH, newBytes)
	srv := newFakeReleaseServer(t, "v0.9.2", asset, tgz, []byte(checksums))

	tmp := t.TempDir()
	targetPath := filepath.Join(tmp, "pyry")
	if err := os.WriteFile(targetPath, []byte("OLD pyry bytes"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := doUpdate(t.Context(), updateOptions{
		currentVersion: "0.9.1",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return targetPath },
		replace:        update.AtomicReplace,
		out:            &out,
		probeRestart:   func() update.RestartProbe { return update.RestartProbe{} },
		runRestart: func(context.Context, []string) error {
			t.Fatalf("runRestart must not be called when no managed unit is detected")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("doUpdate: %v\n--- output ---\n%s", err, out.String())
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(got, newBytes) {
		t.Errorf("on-disk binary not replaced: got %q, want %q", got, newBytes)
	}

	output := out.String()
	for _, want := range []string{
		"==> Current version: 0.9.1",
		"==> Latest version:  v0.9.2",
		"==> Downloading " + asset + "...",
		"==> Verifying SHA-256... ok",
		"==> Replacing " + targetPath + "...",
		"==> Updated to v0.9.2.",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q; full output:\n%s", want, output)
		}
	}
}

// TestUpdate_AlreadyAtLatest pins AC #2's short-circuit: when current ==
// latest, AtomicReplace must not run and the "already at latest" line must
// print.
func TestUpdate_AlreadyAtLatest(t *testing.T) {
	asset, tgz, checksums := fakeRelease(t, "v0.9.1", runtime.GOOS, runtime.GOARCH, []byte("ignored"))
	srv := newFakeReleaseServer(t, "v0.9.1", asset, tgz, []byte(checksums))

	var out bytes.Buffer
	err := doUpdate(t.Context(), updateOptions{
		currentVersion: "0.9.1",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return "/dev/null/never-touched" },
		replace: func(string, []byte, os.FileMode) error {
			t.Fatalf("AtomicReplace must not run on already-at-latest path")
			return nil
		},
		out: &out,
	})
	if err != nil {
		t.Fatalf("doUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "already at latest") {
		t.Errorf("missing already-at-latest line; output:\n%s", out.String())
	}
}

// TestUpdate_CheckOnly pins AC #3: --check prints current + latest and exits
// without downloading.
func TestUpdate_CheckOnly(t *testing.T) {
	asset, tgz, checksums := fakeRelease(t, "v0.9.2", runtime.GOOS, runtime.GOARCH, []byte("ignored"))
	srv := newFakeReleaseServer(t, "v0.9.2", asset, tgz, []byte(checksums))

	var out bytes.Buffer
	err := doUpdate(t.Context(), updateOptions{
		currentVersion: "0.9.1",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return "/dev/null/never-touched" },
		replace: func(string, []byte, os.FileMode) error {
			t.Fatalf("AtomicReplace must not run on --check path")
			return nil
		},
		out:       &out,
		checkOnly: true,
	})
	if err != nil {
		t.Fatalf("doUpdate: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "==> Current version: 0.9.1") {
		t.Errorf("missing current-version line; output:\n%s", output)
	}
	if !strings.Contains(output, "==> Latest version:  v0.9.2") {
		t.Errorf("missing latest-version line; output:\n%s", output)
	}
	if strings.Contains(output, "Downloading") {
		t.Errorf("--check must not download; output:\n%s", output)
	}
}

// TestUpdate_PinVersion pins AC #3: --version <v> skips the latest-release
// API call and downloads the pinned tag directly. The fake server has no
// /repos/...releases/latest handler match for this case (we verify the
// pinned URLs are hit instead).
func TestUpdate_PinVersion(t *testing.T) {
	newBytes := []byte("\x7fELF...pinned bytes...")
	asset, tgz, checksums := fakeRelease(t, "v0.9.0", runtime.GOOS, runtime.GOARCH, newBytes)

	var latestCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/pyrycode/pyrycode/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		latestCalled = true
		http.Error(w, "should not be called when --version pins a tag", http.StatusInternalServerError)
	})
	mux.HandleFunc("/releases/download/v0.9.0/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tgz)
	})
	mux.HandleFunc("/releases/download/v0.9.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	targetPath := filepath.Join(tmp, "pyry")
	if err := os.WriteFile(targetPath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := doUpdate(t.Context(), updateOptions{
		currentVersion: "0.9.1",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return targetPath },
		replace:        update.AtomicReplace,
		out:            &out,
		pinVersion:     "v0.9.0",
		probeRestart:   func() update.RestartProbe { return update.RestartProbe{} },
		runRestart: func(context.Context, []string) error {
			t.Fatalf("runRestart must not be called when no managed unit is detected")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("doUpdate: %v\n--- output ---\n%s", err, out.String())
	}
	if latestCalled {
		t.Errorf("--version <v> must skip the latest-release API call")
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(got, newBytes) {
		t.Errorf("on-disk binary not replaced with pinned version's bytes")
	}
	if !strings.Contains(out.String(), "==> Updated to v0.9.0.") {
		t.Errorf("missing pinned-version success line; output:\n%s", out.String())
	}
}

// TestUpdate_DevBuildSkips pins the Version == "dev" branch: CompareVersions
// returns ErrInvalidVersion, the wiring prints "skipping update" and exits 0
// without calling AtomicReplace.
func TestUpdate_DevBuildSkips(t *testing.T) {
	asset, tgz, checksums := fakeRelease(t, "v0.9.2", runtime.GOOS, runtime.GOARCH, []byte("ignored"))
	srv := newFakeReleaseServer(t, "v0.9.2", asset, tgz, []byte(checksums))

	var out bytes.Buffer
	err := doUpdate(t.Context(), updateOptions{
		currentVersion: "dev",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return "/dev/null/never-touched" },
		replace: func(string, []byte, os.FileMode) error {
			t.Fatalf("AtomicReplace must not run on dev-build path")
			return nil
		},
		out: &out,
	})
	if err != nil {
		t.Fatalf("doUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "skipping update") {
		t.Errorf("missing dev-build skip line; output:\n%s", out.String())
	}
}

// restartUpdateOptions builds an updateOptions wired to a fake release server
// for the restart-focused tests. The probe and runRestart seams are filled by
// the caller; the rest is the same boilerplate as TestUpdate_Success.
func restartUpdateOptions(t *testing.T, targetPath string, out *bytes.Buffer, probe update.RestartProbe, runRestart func(context.Context, []string) error, noRestart bool) updateOptions {
	t.Helper()
	newBytes := []byte("\x7fELF...new pyry bytes...")
	asset, tgz, checksums := fakeRelease(t, "v0.9.2", runtime.GOOS, runtime.GOARCH, newBytes)
	srv := newFakeReleaseServer(t, "v0.9.2", asset, tgz, []byte(checksums))
	if err := os.WriteFile(targetPath, []byte("OLD pyry bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	return updateOptions{
		currentVersion: "0.9.1",
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: srv.URL + "/releases/download",
		fetcher:        &update.Fetcher{BaseURL: srv.URL, UserAgent: "pyry/test"},
		executablePath: func() string { return targetPath },
		replace:        update.AtomicReplace,
		out:            out,
		noRestart:      noRestart,
		probeRestart:   func() update.RestartProbe { return probe },
		runRestart:     runRestart,
	}
}

// TestUpdate_RestartLaunchd asserts the launchd argv shape and progress line
// when a launchd plist is detected.
func TestUpdate_RestartLaunchd(t *testing.T) {
	var got []string
	var out bytes.Buffer
	probe := update.RestartProbe{LaunchdPlistExists: true, UID: "501"}
	opts := restartUpdateOptions(t, filepath.Join(t.TempDir(), "pyry"), &out, probe,
		func(_ context.Context, argv []string) error {
			got = argv
			return nil
		}, false)

	if err := doUpdate(t.Context(), opts); err != nil {
		t.Fatalf("doUpdate: %v\n--- output ---\n%s", err, out.String())
	}
	want := []string{"launchctl", "kickstart", "-k", "gui/501/dev.pyrycode.pyry"}
	if !slices.Equal(got, want) {
		t.Errorf("runRestart argv = %v, want %v", got, want)
	}
	output := out.String()
	if !strings.Contains(output, "==> Restarting daemon (launchd: gui/501/dev.pyrycode.pyry)...") {
		t.Errorf("missing launchd restart progress line; output:\n%s", output)
	}
	if !strings.Contains(output, "==> Updated to v0.9.2.") {
		t.Errorf("missing success line; output:\n%s", output)
	}
}

// TestUpdate_RestartSystemd asserts the systemd argv shape and progress line
// when a systemd user unit is detected.
func TestUpdate_RestartSystemd(t *testing.T) {
	var got []string
	var out bytes.Buffer
	probe := update.RestartProbe{SystemdUnitExists: true}
	opts := restartUpdateOptions(t, filepath.Join(t.TempDir(), "pyry"), &out, probe,
		func(_ context.Context, argv []string) error {
			got = argv
			return nil
		}, false)

	if err := doUpdate(t.Context(), opts); err != nil {
		t.Fatalf("doUpdate: %v\n--- output ---\n%s", err, out.String())
	}
	want := []string{"systemctl", "--user", "restart", "pyry"}
	if !slices.Equal(got, want) {
		t.Errorf("runRestart argv = %v, want %v", got, want)
	}
	output := out.String()
	if !strings.Contains(output, "==> Restarting daemon (systemd: pyry)...") {
		t.Errorf("missing systemd restart progress line; output:\n%s", output)
	}
	if strings.Contains(output, "launchd:") {
		t.Errorf("manager label should be systemd, not launchd; output:\n%s", output)
	}
}

// TestUpdate_NoRestartFlag pins AC #1: --no-restart skips the restart step
// even when a managed unit is present.
func TestUpdate_NoRestartFlag(t *testing.T) {
	var out bytes.Buffer
	probe := update.RestartProbe{LaunchdPlistExists: true, UID: "501"}
	opts := restartUpdateOptions(t, filepath.Join(t.TempDir(), "pyry"), &out, probe,
		func(context.Context, []string) error {
			t.Fatalf("runRestart must not be called when --no-restart is set")
			return nil
		}, true)

	if err := doUpdate(t.Context(), opts); err != nil {
		t.Fatalf("doUpdate: %v\n--- output ---\n%s", err, out.String())
	}
	output := out.String()
	if !strings.Contains(output, "==> Updated to v0.9.2.") {
		t.Errorf("missing success line; output:\n%s", output)
	}
	if strings.Contains(output, "Restarting daemon") {
		t.Errorf("--no-restart must suppress restart progress line; output:\n%s", output)
	}
}

// TestUpdate_NoManagedUnit pins the silent-skip behaviour: zero-value probe
// → DetectRestartCommand returns nil → no restart line, runRestart not called.
func TestUpdate_NoManagedUnit(t *testing.T) {
	var out bytes.Buffer
	opts := restartUpdateOptions(t, filepath.Join(t.TempDir(), "pyry"), &out, update.RestartProbe{},
		func(context.Context, []string) error {
			t.Fatalf("runRestart must not be called when no managed unit is detected")
			return nil
		}, false)

	if err := doUpdate(t.Context(), opts); err != nil {
		t.Fatalf("doUpdate: %v\n--- output ---\n%s", err, out.String())
	}
	output := out.String()
	if !strings.Contains(output, "==> Updated to v0.9.2.") {
		t.Errorf("missing success line; output:\n%s", output)
	}
	if strings.Contains(output, "Restarting daemon") {
		t.Errorf("no managed unit must skip restart silently; output:\n%s", output)
	}
}

// TestUpdate_RestartFailure pins AC #4: a restart-command failure surfaces a
// clear error mentioning that the binary was already replaced, and the
// success line does not print.
func TestUpdate_RestartFailure(t *testing.T) {
	var out bytes.Buffer
	probe := update.RestartProbe{LaunchdPlistExists: true, UID: "501"}
	opts := restartUpdateOptions(t, filepath.Join(t.TempDir(), "pyry"), &out, probe,
		func(context.Context, []string) error {
			return errors.New("exit status 1")
		}, false)

	err := doUpdate(t.Context(), opts)
	if err == nil {
		t.Fatalf("doUpdate: expected error, got nil; output:\n%s", out.String())
	}
	msg := err.Error()
	if !strings.Contains(msg, "binary replaced to v0.9.2") {
		t.Errorf("error must mention binary replaced to v0.9.2: %v", err)
	}
	if !strings.Contains(msg, "daemon restart failed") {
		t.Errorf("error must mention daemon restart failed: %v", err)
	}
	if strings.Contains(out.String(), "==> Updated to v0.9.2.") {
		t.Errorf("success line must NOT print on restart failure; output:\n%s", out.String())
	}
}
