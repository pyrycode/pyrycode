//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConversationNew_E2E(t *testing.T) {
	home, sessionsPath := newRegistryHome(t)
	// Retained published vocabulary makes explicit model creation possible
	// without a relay, a paired device, or any live Claude discovery.
	vocab := `{"models":[{"value":"sonnet","resolved_model":"sonnet-version","effort_levels":["low","max"]}]}`
	if err := os.WriteFile(filepath.Join(home, ".pyry", "test", "model_list.json"), []byte(vocab), 0o600); err != nil {
		t.Fatal(err)
	}
	h := StartIn(t, home, "-pyry-relay=")
	proj, real := projectDir(t, home, "project")
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(proj, alias); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name                   string
		args                   []string
		promoted               bool
		display, model, effort string
	}{
		{name: "default chat"},
		{name: "empty chat name", args: []string{"--name="}},
		{name: "named chat settings", args: []string{"--name", "Chat", "--model", "sonnet", "--effort", "max"}, display: "Chat", model: "sonnet", effort: "max"},
		{name: "default channel", args: []string{"--type", "channel"}, promoted: true, display: "project"},
		{name: "empty channel name", args: []string{"--type=channel", "--name="}, promoted: true, display: "project"},
		{name: "named channel reset", args: []string{"--type=channel", "--name=Channel", "--model=", "--effort="}, promoted: true, display: "Channel"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"new"}, tt.args...)
			r := runVerbIn(t, h.SocketPath, home, alias, "conversation", args...)
			if r.ExitCode != 0 || len(r.Stderr) != 0 || !canonicalUUIDLine.Match(r.Stdout) {
				t.Fatalf("create = %+v", r)
			}
			id := strings.TrimSpace(string(r.Stdout))
			row := waitForConversation(t, home, id)
			if row.Cwd != real || row.IsPromoted != tt.promoted || row.CurrentSessionID == "" {
				t.Fatalf("row = %+v", row)
			}
			if tt.display == "" && row.Name != nil || tt.display != "" && (row.Name == nil || *row.Name != tt.display) {
				t.Fatalf("name = %v, want %q", row.Name, tt.display)
			}
			raw, err := os.ReadFile(sessionsPath)
			if err != nil {
				t.Fatal(err)
			}
			var file struct {
				Sessions []struct {
					ID, Model, Effort string
					LifecycleState    string `json:"lifecycle_state"`
				}
			}
			if err := json.Unmarshal(raw, &file); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, s := range file.Sessions {
				if s.ID != row.CurrentSessionID {
					continue
				}
				found = true
				if s.Model != tt.model || s.Effort != tt.effort || s.LifecycleState != "evicted" {
					t.Fatalf("bound session = %+v", s)
				}
			}
			if !found {
				t.Fatalf("no bound session in %s", raw)
			}
		})
	}
	before, err := os.ReadFile(sessionsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"new", "--model=absent-private-model"}, {"new", "--effort=ultra"}, {"new", "--model=sonnet", "--effort=medium"}} {
		r := runVerbIn(t, h.SocketPath, home, proj, "conversation", args...)
		if r.ExitCode != 1 || len(r.Stdout) != 0 || len(r.Stderr) == 0 || bytes.Contains(r.Stderr, []byte("absent-private-model")) {
			t.Fatalf("refusal = %+v", r)
		}
	}
	after, err := os.ReadFile(sessionsPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refusal changed session registry: %v", err)
	}
	// Instance selector and explicit socket retain the shared override contract.
	r := runVerbIn(t, h.SocketPath, home, proj, "conversation", "-pyry-name=other", "new", "--model=")
	if r.ExitCode != 0 || !canonicalUUIDLine.Match(r.Stdout) || len(r.Stderr) != 0 {
		t.Fatalf("selector did not reach this daemon: %+v", r)
	}
}

func TestConversationNew_E2E_SyntaxAndTransport(t *testing.T) {
	home, _ := newRegistryHome(t)
	missingSocket := filepath.Join(home, "missing.sock")
	for _, args := range [][]string{
		{}, {"other"}, {"new", "extra"}, {"new", "--unknown"},
		{"new", "--type="}, {"new", "--type=other"},
		{"new", "--type"}, {"new", "--name"}, {"new", "--model"}, {"new", "--effort"},
		{"new", "--model", "--effort=low"},
		{"-pyry-socket"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			full := append([]string{"conversation", "-pyry-socket=" + missingSocket}, args...)
			r := RunBareIn(t, home, full...)
			if r.ExitCode != 2 || len(r.Stdout) != 0 || !bytes.Contains(r.Stderr, []byte("usage: pyry conversation")) {
				t.Fatalf("syntax = %+v", r)
			}
		})
	}
	r := RunBareIn(t, home, "conversation", "-pyry-socket="+missingSocket, "new")
	if r.ExitCode != 1 || len(r.Stdout) != 0 || len(r.Stderr) == 0 {
		t.Fatalf("transport = %+v", r)
	}
	r = RunBareIn(t, home, "help")
	if r.ExitCode != 0 || !bytes.Contains(r.Stdout, []byte("pyry conversation new")) || !bytes.Contains(r.Stdout, []byte("--type chat|channel")) {
		t.Fatalf("help = %+v", r)
	}
}
