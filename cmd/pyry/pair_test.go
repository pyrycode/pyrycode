package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/config"
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
