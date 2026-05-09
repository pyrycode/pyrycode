package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/devices"
)

// TestParsePairArgs covers the flag-set surface of `pyry pair`: the
// happy paths (empty, --name, --relay, both) plus the two error shapes
// runPair maps to exit 2 (unexpected positional, unknown flag).
func TestParsePairArgs(t *testing.T) {
	t.Setenv("PYRY_NAME", "")

	tests := []struct {
		name       string
		args       []string
		wantDevice string
		wantRelay  string
		wantErr    string
	}{
		{name: "empty", args: nil, wantDevice: "", wantRelay: ""},
		{name: "name only", args: []string{"--name=phone"}, wantDevice: "phone", wantRelay: ""},
		{name: "relay only", args: []string{"--relay=wss://x"}, wantDevice: "", wantRelay: "wss://x"},
		{name: "both", args: []string{"--name=phone", "--relay=wss://x"}, wantDevice: "phone", wantRelay: "wss://x"},
		{name: "name space form", args: []string{"--name", "phone"}, wantDevice: "phone"},
		{name: "positional rejected", args: []string{"--name=phone", "extra"}, wantErr: "unexpected positional"},
		{name: "unknown flag rejected", args: []string{"--bogus"}, wantErr: "flag provided but not defined"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePairArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q missing fragment %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.deviceName != tc.wantDevice {
				t.Errorf("deviceName=%q want %q", got.deviceName, tc.wantDevice)
			}
			if got.relay != tc.wantRelay {
				t.Errorf("relay=%q want %q", got.relay, tc.wantRelay)
			}
		})
	}
}

// TestResolveRelay pins the three-leg precedence: --relay > config >
// built-in default. The fourth case (all empty) is the only path AC#5
// names that reaches exit 2 through resolveRelay.
func TestResolveRelay(t *testing.T) {
	tests := []struct {
		name      string
		flag      string
		cfg       config.Config
		want      string
	}{
		{name: "flag wins", flag: "wss://flag", cfg: config.Config{RelayURL: "wss://cfg"}, want: "wss://flag"},
		{name: "config wins when flag empty", flag: "", cfg: config.Config{RelayURL: "wss://cfg"}, want: "wss://cfg"},
		{name: "default wins when flag and cfg empty", flag: "", cfg: config.Config{}, want: config.DefaultConfig().RelayURL},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveRelay(tc.flag, tc.cfg); got != tc.want {
				t.Errorf("resolveRelay=%q want %q", got, tc.want)
			}
		})
	}
}

// TestResolveDevicesPath confirms the per-instance layout
// (~/.pyry/<sanitized-name>/devices.json) and that the name is
// sanitized — defending against PYRY_NAME=../etc / similar.
func TestResolveDevicesPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := resolveDevicesPath("test")
	want := filepath.Join(home, ".pyry", "test", "devices.json")
	if got != want {
		t.Errorf("resolveDevicesPath(%q)=%q want %q", "test", got, want)
	}

	// Path-traversal input must be neutralized: the instance segment
	// must not contain a path separator that would let it escape
	// ~/.pyry/<name>/.
	traversed := resolveDevicesPath("../etc")
	rel, err := filepath.Rel(filepath.Join(home, ".pyry"), traversed)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Errorf("resolveDevicesPath(%q)=%q escapes ~/.pyry (rel=%q) — sanitizeName not applied", "../etc", traversed, rel)
	}
}

// TestResolveServerIDPath mirrors TestResolveDevicesPath.
func TestResolveServerIDPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := resolveServerIDPath("test")
	want := filepath.Join(home, ".pyry", "test", "server-id")
	if got != want {
		t.Errorf("resolveServerIDPath(%q)=%q want %q", "test", got, want)
	}

	traversed := resolveServerIDPath("../etc")
	rel, err := filepath.Rel(filepath.Join(home, ".pyry"), traversed)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Errorf("resolveServerIDPath(%q)=%q escapes ~/.pyry (rel=%q)", "../etc", traversed, rel)
	}
}

// TestResolveConfigPath confirms the per-user path; no instance-name
// interpolation, so no sanitization required.
func TestResolveConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := resolveConfigPath()
	want := filepath.Join(home, ".pyry", "config.json")
	if got != want {
		t.Errorf("resolveConfigPath()=%q want %q", got, want)
	}
}

// TestRenderPairList_TwoDevices fixes the entire byte-for-byte shape of
// the formatter on a known input: header row, two data rows in
// (PairedAt, Name) ascending order, padded by text/tabwriter.
func TestRenderPairList_TwoDevices(t *testing.T) {
	list := []devices.Device{
		{
			Name:       "alpha",
			PairedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			LastSeenAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			TokenHash:  "aaaaaaaa11111111111111111111111111111111111111111111111111111111",
		},
		{
			Name:       "bravo",
			PairedAt:   time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
			LastSeenAt: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
			TokenHash:  "bbbbbbbb22222222222222222222222222222222222222222222222222222222",
		},
	}
	want := "NAME   PAIRED                LAST SEEN             TOKEN-PREFIX\n" +
		"alpha  2026-01-01T00:00:00Z  2026-01-02T00:00:00Z  aaaaaaaa\n" +
		"bravo  2026-01-03T00:00:00Z  2026-01-04T00:00:00Z  bbbbbbbb\n"

	var buf bytes.Buffer
	if err := renderPairList(list, &buf); err != nil {
		t.Fatalf("renderPairList: %v", err)
	}
	if got := buf.String(); got != want {
		t.Errorf("renderPairList output mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderPairList_NeverSeen pins the LAST SEEN column's
// zero-LastSeenAt rendering: the literal string "never", not the
// formatted zero time.
func TestRenderPairList_NeverSeen(t *testing.T) {
	list := []devices.Device{
		{
			Name:      "phone",
			PairedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			TokenHash: "cccccccc33333333333333333333333333333333333333333333333333333333",
		},
	}
	var buf bytes.Buffer
	if err := renderPairList(list, &buf); err != nil {
		t.Fatalf("renderPairList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "never") {
		t.Errorf("output missing 'never' for zero LastSeenAt:\n%s", out)
	}
	if strings.Contains(out, "0001-01-01") {
		t.Errorf("output rendered zero time instead of 'never':\n%s", out)
	}
}

// TestRenderPairList_Empty asserts the empty-registry output is
// exactly the contract string — no header, no trailing whitespace.
func TestRenderPairList_Empty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderPairList(nil, &buf); err != nil {
		t.Fatalf("renderPairList: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), []byte("No paired devices.\n")) {
		t.Errorf("empty output=%q want %q", buf.String(), "No paired devices.\n")
	}
}

// TestRenderPairList_SortOrder feeds rows in reverse-chronological
// order and asserts the formatter sorts ascending by (PairedAt, Name).
// Independent of how Load happens to return the slice.
func TestRenderPairList_SortOrder(t *testing.T) {
	list := []devices.Device{
		{
			Name:      "zulu",
			PairedAt:  time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			TokenHash: "ffffffff44444444444444444444444444444444444444444444444444444444",
		},
		{
			Name:      "alpha",
			PairedAt:  time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
			TokenHash: "11111111ddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		},
		{
			Name:      "bravo",
			PairedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			TokenHash: "22222222eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		},
		// Same PairedAt as bravo; expected to follow bravo (Name asc).
		{
			Name:      "charlie",
			PairedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			TokenHash: "33333333ffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		},
	}
	var buf bytes.Buffer
	if err := renderPairList(list, &buf); err != nil {
		t.Fatalf("renderPairList: %v", err)
	}
	out := buf.String()
	wantOrder := []string{"bravo", "charlie", "alpha", "zulu"}
	last := -1
	for _, name := range wantOrder {
		idx := strings.Index(out, name)
		if idx < 0 {
			t.Fatalf("output missing %q:\n%s", name, out)
		}
		if idx <= last {
			t.Errorf("name %q at byte %d came before previous expected position %d:\n%s",
				name, idx, last, out)
		}
		last = idx
	}
}

// TestParsePairListArgs covers the flag-set surface of `pyry pair
// list`: the empty path (defaults), -pyry-name (custom instance), and
// the two error shapes runPairList maps to exit 2.
func TestParsePairListArgs(t *testing.T) {
	t.Setenv("PYRY_NAME", "")

	tests := []struct {
		name         string
		args         []string
		wantInstance string
		wantErr      string
	}{
		{name: "empty", args: nil, wantInstance: defaultName()},
		{name: "instance", args: []string{"-pyry-name=foo"}, wantInstance: "foo"},
		{name: "positional rejected", args: []string{"extra"}, wantErr: "unexpected positional"},
		{name: "unknown flag rejected", args: []string{"--bogus"}, wantErr: "flag provided but not defined"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePairListArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q missing fragment %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.instanceName != tc.wantInstance {
				t.Errorf("instanceName=%q want %q", got.instanceName, tc.wantInstance)
			}
		})
	}
}

// TestParsePairRevokeArgs covers the flag-set surface of `pyry pair
// revoke`: the happy paths (default instance, custom -pyry-name) and the
// three error shapes runPairRevoke maps to exit 2.
func TestParsePairRevokeArgs(t *testing.T) {
	t.Setenv("PYRY_NAME", "")

	tests := []struct {
		name           string
		args           []string
		wantInstance   string
		wantDeviceName string
		wantErr        string
	}{
		{name: "happy", args: []string{"phone"}, wantInstance: defaultName(), wantDeviceName: "phone"},
		{name: "with instance", args: []string{"-pyry-name=foo", "phone"}, wantInstance: "foo", wantDeviceName: "phone"},
		{name: "missing positional", args: nil, wantErr: "missing device name"},
		{name: "extra positional", args: []string{"a", "b"}, wantErr: "unexpected positional"},
		{name: "unknown flag rejected", args: []string{"--bogus", "phone"}, wantErr: "flag provided but not defined"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePairRevokeArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q missing fragment %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.instanceName != tc.wantInstance {
				t.Errorf("instanceName=%q want %q", got.instanceName, tc.wantInstance)
			}
			if got.deviceName != tc.wantDeviceName {
				t.Errorf("deviceName=%q want %q", got.deviceName, tc.wantDeviceName)
			}
		})
	}
}

// TestRunPairRevoke_RemovesEntry verifies the success path: the matching
// device is removed and the surviving entry is preserved byte-for-byte
// across the round trip via devices.Load.
func TestRunPairRevoke_RemovesEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PYRY_NAME", "")

	path := resolveDevicesPath(defaultName())
	registry, err := devices.Load(path)
	if err != nil {
		t.Fatalf("devices.Load: %v", err)
	}
	alpha := devices.Device{
		Name:       "alpha",
		TokenHash:  "aaaaaaaa11111111111111111111111111111111111111111111111111111111",
		PairedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	bravo := devices.Device{
		Name:       "bravo",
		TokenHash:  "bbbbbbbb22222222222222222222222222222222222222222222222222222222",
		PairedAt:   time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
	}
	registry.Add(alpha)
	registry.Add(bravo)
	if err := registry.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := runPairRevoke([]string{"alpha"}); err != nil {
		t.Fatalf("runPairRevoke: %v", err)
	}

	reloaded, err := devices.Load(path)
	if err != nil {
		t.Fatalf("devices.Load after revoke: %v", err)
	}
	list := reloaded.List()
	if len(list) != 1 {
		t.Fatalf("registry has %d entries after revoke, want 1", len(list))
	}
	got := list[0]
	if got.Name != bravo.Name {
		t.Errorf("survivor.Name=%q want %q", got.Name, bravo.Name)
	}
	if got.TokenHash != bravo.TokenHash {
		t.Errorf("survivor.TokenHash=%q want %q", got.TokenHash, bravo.TokenHash)
	}
	if !got.PairedAt.Equal(bravo.PairedAt) {
		t.Errorf("survivor.PairedAt=%v want %v", got.PairedAt, bravo.PairedAt)
	}
	if !got.LastSeenAt.Equal(bravo.LastSeenAt) {
		t.Errorf("survivor.LastSeenAt=%v want %v", got.LastSeenAt, bravo.LastSeenAt)
	}
}

// TestRunPairRevoke_SaveFailure confirms the I/O-error path returns a
// wrapped error with the `pair revoke:` prefix when Save can't persist.
// Skipped on Windows (we're Linux+macOS only) and skipped if chmod 0500
// on the parent dir doesn't actually block writes for the test user
// (e.g. running as root).
func TestRunPairRevoke_SaveFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-only permission test")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PYRY_NAME", "")

	path := resolveDevicesPath(defaultName())
	registry, err := devices.Load(path)
	if err != nil {
		t.Fatalf("devices.Load: %v", err)
	}
	registry.Add(devices.Device{
		Name:      "alpha",
		TokenHash: "aaaaaaaa11111111111111111111111111111111111111111111111111111111",
		PairedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := registry.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Pre-flight: ensure 0500 actually blocks writes for this user (root
	// bypasses DAC). If a probe write succeeds, Save will too — skip.
	probe := filepath.Join(dir, ".probe.tmp")
	if f, perr := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600); perr == nil {
		_ = f.Close()
		_ = os.Remove(probe)
		t.Skip("chmod 0500 did not block writes for this user (running as root?)")
	}

	err = runPairRevoke([]string{"alpha"})
	if err == nil {
		t.Fatalf("expected error from runPairRevoke, got nil")
	}
	if !strings.Contains(err.Error(), "pair revoke:") {
		t.Errorf("error %q missing prefix %q", err.Error(), "pair revoke:")
	}
}
