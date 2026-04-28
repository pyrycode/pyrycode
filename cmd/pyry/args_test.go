package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		wantPyry    []string
		wantClaude  []string
	}{
		{
			name:       "empty",
			args:       nil,
			wantPyry:   nil,
			wantClaude: nil,
		},
		{
			name:       "no pyry flags, just a prompt",
			args:       []string{"summarize this"},
			wantPyry:   nil,
			wantClaude: []string{"summarize this"},
		},
		{
			name:       "claude flags only — pass through verbatim",
			args:       []string{"--model", "sonnet", "-p", "hello"},
			wantPyry:   nil,
			wantClaude: []string{"--model", "sonnet", "-p", "hello"},
		},
		{
			name:       "single pyry boolean flag",
			args:       []string{"-pyry-verbose"},
			wantPyry:   []string{"-pyry-verbose"},
			wantClaude: nil,
		},
		{
			name:       "pyry value flag with separate value",
			args:       []string{"-pyry-claude", "/usr/bin/claude"},
			wantPyry:   []string{"-pyry-claude", "/usr/bin/claude"},
			wantClaude: nil,
		},
		{
			name:       "pyry value flag with glued value",
			args:       []string{"-pyry-claude=/usr/bin/claude"},
			wantPyry:   []string{"-pyry-claude=/usr/bin/claude"},
			wantClaude: nil,
		},
		{
			name:       "pyry flag then claude flags",
			args:       []string{"-pyry-verbose", "--model", "sonnet"},
			wantPyry:   []string{"-pyry-verbose"},
			wantClaude: []string{"--model", "sonnet"},
		},
		{
			name:       "pyry value flag then claude args",
			args:       []string{"-pyry-workdir", "/tmp", "summarize", "foo"},
			wantPyry:   []string{"-pyry-workdir", "/tmp"},
			wantClaude: []string{"summarize", "foo"},
		},
		{
			name:       "claude flag first means pyry flags are forwarded too",
			args:       []string{"--model", "sonnet", "-pyry-verbose"},
			wantPyry:   nil,
			wantClaude: []string{"--model", "sonnet", "-pyry-verbose"},
		},
		{
			name:       "explicit -- separator with pyry flags before",
			args:       []string{"-pyry-verbose", "--", "--model", "sonnet"},
			wantPyry:   []string{"-pyry-verbose"},
			wantClaude: []string{"--model", "sonnet"},
		},
		{
			name:       "explicit -- separator with empty pyry side",
			args:       []string{"--", "--model", "sonnet"},
			wantPyry:   nil,
			wantClaude: []string{"--model", "sonnet"},
		},
		{
			name:       "double-dash form for pyry flags",
			args:       []string{"--pyry-verbose", "--model", "sonnet"},
			wantPyry:   []string{"--pyry-verbose"},
			wantClaude: []string{"--model", "sonnet"},
		},
		{
			name:       "boolean flag with =false",
			args:       []string{"-pyry-resume=false", "summarize"},
			wantPyry:   []string{"-pyry-resume=false"},
			wantClaude: []string{"summarize"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotPyry, gotClaude := splitArgs(tt.args)
			if !reflect.DeepEqual(gotPyry, tt.wantPyry) {
				t.Errorf("pyry args = %v, want %v", gotPyry, tt.wantPyry)
			}
			if !reflect.DeepEqual(gotClaude, tt.wantClaude) {
				t.Errorf("claude args = %v, want %v", gotClaude, tt.wantClaude)
			}
		})
	}
}

func TestParseFlagSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in       string
		wantName string
		wantVal  string
		wantHas  bool
	}{
		{"-foo", "foo", "", false},
		{"--foo", "foo", "", false},
		{"-foo=bar", "foo", "bar", true},
		{"--foo=bar", "foo", "bar", true},
		{"-pyry-claude=/usr/bin/claude", "pyry-claude", "/usr/bin/claude", true},
		{"summarize", "", "", false},
		{"", "", "", false},
		{"-foo=", "foo", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			name, val, has := parseFlagSyntax(tt.in)
			if name != tt.wantName || val != tt.wantVal || has != tt.wantHas {
				t.Errorf("parseFlagSyntax(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.in, name, val, has, tt.wantName, tt.wantVal, tt.wantHas)
			}
		})
	}
}

func TestSocketPathForCwd(t *testing.T) {
	t.Parallel()

	const home = "/Users/me"

	t.Run("lives under ~/.pyry/sockets", func(t *testing.T) {
		t.Parallel()
		got := socketPathForCwd(home, "/Users/me/Projects/foo")
		want := filepath.Join(home, ".pyry", "sockets")
		if !strings.HasPrefix(got, want) {
			t.Errorf("socket path %q does not live under %q", got, want)
		}
		if !strings.HasSuffix(got, ".sock") {
			t.Errorf("socket path %q does not end in .sock", got)
		}
	})

	t.Run("includes the basename for human readability", func(t *testing.T) {
		t.Parallel()
		got := filepath.Base(socketPathForCwd(home, "/Users/me/Projects/foo"))
		if !strings.HasPrefix(got, "foo-") {
			t.Errorf("filename %q should start with foo-", got)
		}
	})

	t.Run("deterministic for the same cwd", func(t *testing.T) {
		t.Parallel()
		a := socketPathForCwd(home, "/Users/me/Projects/foo")
		b := socketPathForCwd(home, "/Users/me/Projects/foo")
		if a != b {
			t.Errorf("same cwd produced different paths: %q vs %q", a, b)
		}
	})

	t.Run("different cwds produce different paths", func(t *testing.T) {
		t.Parallel()
		a := socketPathForCwd(home, "/Users/me/Projects/foo")
		b := socketPathForCwd(home, "/Users/me/Projects/bar")
		if a == b {
			t.Errorf("different cwds produced the same path: %q", a)
		}
	})

	t.Run("same basename in different parent dirs does not collide", func(t *testing.T) {
		t.Parallel()
		// Both end in /pyrycode but live under different parents — the
		// hash should disambiguate.
		a := socketPathForCwd(home, "/Users/me/Workspace/Projects/pyrycode")
		b := socketPathForCwd(home, "/Users/me/Backups/pyrycode")
		if a == b {
			t.Errorf("same-basename different-parent collided: %q", a)
		}
		if !strings.Contains(filepath.Base(a), "pyrycode-") {
			t.Errorf("expected basename in filename: %q", a)
		}
	})

	t.Run("unsafe characters in basename are sanitised", func(t *testing.T) {
		t.Parallel()
		got := filepath.Base(socketPathForCwd(home, "/tmp/with spaces/and:colons"))
		// Spaces, colons, etc. should not appear in the filename portion
		// before the hash. The basename portion should be sanitised.
		for _, bad := range []string{" ", ":", "/"} {
			if strings.Contains(got, bad) {
				t.Errorf("filename %q contains unsafe character %q", got, bad)
			}
		}
	})

	t.Run("root cwd does not crash", func(t *testing.T) {
		t.Parallel()
		got := socketPathForCwd(home, "/")
		if got == "" {
			t.Errorf("empty result for root cwd")
		}
	})
}

func TestSanitizeBasename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"foo", "foo"},
		{"foo-bar_baz.txt", "foo-bar_baz.txt"},
		{"with spaces", "with_spaces"},
		{"emoji-🎯-here", "emoji-_-here"},
		{"slash/inside", "slash_inside"},
		{"colons:and;semis", "colons_and_semis"},
		{"", "_"},
		{"/", "_"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got := sanitizeBasename(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeBasename(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
