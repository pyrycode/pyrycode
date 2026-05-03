package main

import (
	"strings"
	"testing"
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
