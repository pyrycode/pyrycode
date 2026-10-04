package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

func TestResolveStartupWorkspaceBase(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "private-home-sentinel")
	service := filepath.Join(home, "elli-workspace")
	legacy := filepath.Join(home, "pyry-workspace")
	outside := filepath.Join(parent, "private-home-sentinel-sibling")
	freshHome := filepath.Join(parent, "home-without-legacy-folder")
	for _, dir := range []string{service, legacy, outside, freshHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	homeLink := filepath.Join(parent, "home-link")
	insideLink := filepath.Join(home, "inside-link")
	escapeLink := filepath.Join(home, "escape-link")
	for link, target := range map[string]string{homeLink: home, insideLink: service, escapeLink: outside} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	rawErr := errors.New("private-getwd-error-sentinel")
	for _, tc := range []struct {
		name, home, cwd, want string
		err                   error
		fallback              bool
	}{
		{name: "service", home: home, cwd: service, want: filepath.Join(homeReal, "elli-workspace")},
		{name: "home itself", home: home, cwd: home, want: homeReal},
		{name: "legacy", home: home, cwd: legacy, want: filepath.Join(homeReal, "pyry-workspace")},
		{name: "symlink home", home: homeLink, cwd: filepath.Join(homeLink, "elli-workspace"), want: filepath.Join(homeReal, "elli-workspace")},
		{name: "symlink cwd", home: home, cwd: insideLink, want: filepath.Join(homeReal, "elli-workspace")},
		{name: "escaping symlink", home: home, cwd: escapeLink, fallback: true},
		{name: "prefix sibling", home: home, cwd: outside, fallback: true},
		{name: "unavailable cwd", home: home, cwd: service, err: rawErr, fallback: true},
		{name: "empty cwd", home: home, fallback: true},
		{name: "relative cwd", home: home, cwd: "elli-workspace", fallback: true},
		{name: "missing cwd", home: home, cwd: filepath.Join(home, "missing-cwd"), fallback: true},
		{name: "absent fallback", home: freshHome, cwd: outside, fallback: true},
		{name: "missing home", home: filepath.Join(parent, "absent-home"), cwd: service, fallback: true},
		{name: "unresolvable home", home: filepath.Join(home, "loop"), cwd: service, fallback: true},
		{name: "unavailable home", cwd: service, fallback: true},
		{name: "relative home", home: "relative-home", cwd: service, fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home)
			if tc.name == "unresolvable home" {
				if err := os.Symlink(tc.home, tc.home); err != nil {
					t.Fatal(err)
				}
			}
			// A regression to trust marking must fail before touching configuration.
			oldTrust := trustMark
			trustMark = func(string) (string, error) { t.Error("resolver trust-marked a folder"); return "", rawErr }
			t.Cleanup(func() { trustMark = oldTrust })
			var logs bytes.Buffer
			calls := 0
			got := resolveStartupWorkspaceBase(func() (string, error) { calls++; return tc.cwd, tc.err }, slog.New(slog.NewJSONHandler(&logs, nil)))
			want := tc.want
			if tc.fallback && filepath.IsAbs(tc.home) {
				want = filepath.Join(tc.home, "pyry-workspace")
			}
			if got != want {
				t.Errorf("base = %q, want %q", got, want)
			}
			wantCalls := 0
			if filepath.IsAbs(tc.home) {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Errorf("getwd calls = %d, want %d", calls, wantCalls)
			}
			if !tc.fallback {
				if logs.Len() != 0 {
					t.Errorf("successful resolution logged: %s", logs.String())
				}
				return
			}
			var record map[string]any
			dec := json.NewDecoder(&logs)
			if err := dec.Decode(&record); err != nil {
				t.Fatal(err)
			}
			delete(record, "time")
			expected := map[string]any{"level": "WARN", "msg": "workspace base: using startup fallback", "event": "workspace_base.fallback"}
			if !reflect.DeepEqual(record, expected) {
				t.Errorf("fallback log = %#v, want static %#v", record, expected)
			}
			if err := dec.Decode(&record); err != io.EOF {
				t.Errorf("after one startup event: %v, want EOF", err)
			}
			if tc.name == "missing home" {
				if _, err := os.Stat(tc.home); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("fallback created HOME: %v", err)
				}
			}
		})
	}
	if _, err := os.Stat(filepath.Join(home, "missing-cwd")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("resolver created cwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("resolver wrote trust configuration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(freshHome, "pyry-workspace")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("resolver created fallback: %v", err)
	}
}

func TestStartupWorkspaceBaseWiring(t *testing.T) {
	source := formattedGoFunc(t, "main.go", "runSupervisor")
	for _, fragment := range []string{
		"workspaceBase := resolveStartupWorkspaceBase(os.Getwd, logger)",
		"workspaceBase: workspaceBase,",
		"seedDefaultWorkspace(convReg, convRegistryPath, workspaceBase, createChannel, logger)",
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("daemon wiring lacks %q", fragment)
		}
	}
	if strings.Count(source, "resolveStartupWorkspaceBase(") != 1 {
		t.Error("startup base must be resolved exactly once")
	}
	if !strings.Contains(formattedGoFunc(t, "relay.go", "startRelayV2"), "WorkspaceBase: &w.workspaceBase,") {
		t.Error("relay must explicitly supply the base even when empty")
	}
}

func TestSeedDefaultWorkspace_ChangedBasePreservesExisting(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(map[bool]string{false: "unseeded", true: "seeded"}[marked], func(t *testing.T) {
			home, reg, path, _, mint, create, logs := newSeedHarness(t)
			name, label := "General", "Elli's existing workspace"
			cwd := filepath.Join(home, "old-workspace", "default")
			reg.Create(conversations.Conversation{ID: "11111111-2222-4333-8444-555555555555", Cwd: cwd, Name: &name, IsPromoted: true})
			reg.SetWorkspaceLabel(cwd, &label)
			if marked {
				reg.MarkSeeded()
			}
			if err := reg.Save(path); err != nil {
				t.Fatal(err)
			}
			before := reg.List()
			newBase := filepath.Join(home, "elli-workspace")
			seedDefaultWorkspace(reg, path, newBase, create, seedLogger(logs))
			back := loadSeeded(t, path)
			if !reflect.DeepEqual(back.List(), before) {
				t.Errorf("existing rows changed: %#v", back.List())
			}
			if got, ok := back.WorkspaceLabel(cwd); !ok || got != label {
				t.Errorf("existing label = %q, %v", got, ok)
			}
			if !back.Seeded() || mint.calls != 0 {
				t.Errorf("seeded=%v mint calls=%d", back.Seeded(), mint.calls)
			}
			if _, err := os.Stat(newBase); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("created new base: %v", err)
			}
		})
	}
}
