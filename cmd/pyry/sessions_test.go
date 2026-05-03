package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/control"
)

// TestRunSessions_NoSubcommand pins the empty-rest error path: bare
// `pyry sessions` (or `pyry sessions -pyry-name foo`) returns a
// help-style error naming the implemented verb list.
func TestRunSessions_NoSubcommand(t *testing.T) {
	t.Setenv("PYRY_NAME", "")

	err := runSessions(nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing subcommand") {
		t.Errorf("error %q missing %q fragment", msg, "missing subcommand")
	}
	if !strings.Contains(msg, sessionsVerbList) {
		t.Errorf("error %q missing verb list %q", msg, sessionsVerbList)
	}
}

// TestRunSessions_UnknownVerb pins AC#3: an unknown sub-verb reports a
// help-style error and never reaches the supervisor / claude-forward
// path. Verified structurally because runSessions is called from
// run()'s switch and returns before runSupervisor runs.
func TestRunSessions_UnknownVerb(t *testing.T) {
	t.Setenv("PYRY_NAME", "")

	err := runSessions([]string{"list"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown verb") {
		t.Errorf("error %q missing %q fragment", msg, "unknown verb")
	}
	if !strings.Contains(msg, `"list"`) {
		t.Errorf("error %q missing offending verb %q", msg, "list")
	}
	if !strings.Contains(msg, sessionsVerbList) {
		t.Errorf("error %q missing verb list %q", msg, sessionsVerbList)
	}
}

// TestRunSessions_GlobalFlagAfterSubcommand_FailsCleanly documents and
// pins the convention: pyry global flags (-pyry-name, -pyry-socket)
// must precede the sub-verb. Placing them after produces a sub-verb
// FlagSet "unknown flag" error rather than silently shadowing.
func TestRunSessions_GlobalFlagAfterSubcommand_FailsCleanly(t *testing.T) {
	t.Setenv("PYRY_NAME", "")

	err := runSessions([]string{"new", "-pyry-name", "elli"})
	if err == nil {
		t.Fatal("expected error from sub-verb FlagSet, got nil")
	}
	if !strings.Contains(err.Error(), "pyry-name") {
		t.Errorf("error %q does not name the offending flag", err)
	}
}

// TestRunSessions_RmDispatch pins AC#1's router wiring: the
// `case "rm":` arm exists and routes to runSessionsRm. Verified by
// passing a deliberately-bogus socket path and observing the resulting
// error path is the dial failure, not the help-style "unknown verb"
// router error.
func TestRunSessions_RmDispatch(t *testing.T) {
	t.Setenv("PYRY_NAME", "")
	bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

	err := runSessions([]string{"-pyry-socket", bogusSock, "rm", "abc"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown verb") {
		t.Errorf("router did not dispatch rm: %v", err)
	}
	if !strings.Contains(msg, "sessions rm:") {
		t.Errorf("error %q missing %q wrap fragment", msg, "sessions rm:")
	}
}

func TestParseSessionsRmArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantID     string
		wantPolicy control.JSONLPolicy
		wantUsage  bool   // expect errors.Is(err, errSessionsRmUsage)
		wantErr    string // substring match against err.Error(); empty means no error
	}{
		{"no args", nil, "", "", true, "expected <id>"},
		{"only --archive flag", []string{"--archive"}, "", "", true, "expected <id>"},
		{"id only", []string{"abc"}, "abc", "", false, ""},
		{"--archive id", []string{"--archive", "abc"}, "abc", control.JSONLPolicyArchive, false, ""},
		{"--purge id", []string{"--purge", "abc"}, "abc", control.JSONLPolicyPurge, false, ""},
		{"--archive --purge id", []string{"--archive", "--purge", "abc"}, "", "", true, "mutually exclusive"},
		{"--purge --archive id", []string{"--purge", "--archive", "abc"}, "", "", true, "mutually exclusive"},
		{"id then extra positional", []string{"abc", "extra"}, "", "", true, "expected <id>"},
		{"flag after positional halts at id", []string{"abc", "--archive"}, "", "", true, "expected <id>"},
		{"unknown flag", []string{"--unknown", "abc"}, "", "", true, "flag provided but not defined"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, policy, err := parseSessionsRmArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (id=%q, policy=%q)",
						tt.wantErr, id, policy)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				if tt.wantUsage && !errors.Is(err, errSessionsRmUsage) {
					t.Errorf("error %q does not match errSessionsRmUsage sentinel", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
			if policy != tt.wantPolicy {
				t.Errorf("policy = %q, want %q", policy, tt.wantPolicy)
			}
		})
	}
}

func TestParseSessionsNewArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		wantLabel string
		wantErr   string // substring; empty means no error
	}{
		{"no args", nil, "", ""},
		{"empty slice", []string{}, "", ""},
		{"--name with value", []string{"--name", "feature-x"}, "feature-x", ""},
		{"--name= empty", []string{"--name="}, "", ""},
		{"-name single dash also accepted", []string{"-name", "elli"}, "elli", ""},
		{"--name= glued", []string{"--name=elli"}, "elli", ""},
		{"unexpected positional", []string{"--name", "foo", "bar"}, "", "unexpected positional"},
		{"bare positional with no flag", []string{"bar"}, "", "unexpected positional"},
		{"unknown flag", []string{"--unknown"}, "", "flag provided but not defined"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			label, err := parseSessionsNewArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (label=%q)", tt.wantErr, label)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if label != tt.wantLabel {
				t.Errorf("label = %q, want %q", label, tt.wantLabel)
			}
		})
	}
}
