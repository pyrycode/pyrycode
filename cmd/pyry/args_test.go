package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseClientFlags(t *testing.T) {
	// Not parallel — t.Setenv mutates the environment.

	t.Run("default name yields ~/.pyry/pyry.sock", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "")
		got, err := parseClientFlags("pyry status", nil)
		if err != nil {
			t.Fatalf("parseClientFlags: %v", err)
		}
		if filepath.Base(got) != "pyry.sock" {
			t.Errorf("filename = %q, want pyry.sock", filepath.Base(got))
		}
	})

	t.Run("-pyry-name override wins over PYRY_NAME", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "fromenv")
		got, err := parseClientFlags("pyry status", []string{"-pyry-name", "fromflag"})
		if err != nil {
			t.Fatalf("parseClientFlags: %v", err)
		}
		if filepath.Base(got) != "fromflag.sock" {
			t.Errorf("filename = %q, want fromflag.sock (flag should win over env)", filepath.Base(got))
		}
	})

	t.Run("PYRY_NAME wins when no flag given", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "fromenv")
		got, err := parseClientFlags("pyry status", nil)
		if err != nil {
			t.Fatalf("parseClientFlags: %v", err)
		}
		if filepath.Base(got) != "fromenv.sock" {
			t.Errorf("filename = %q, want fromenv.sock", filepath.Base(got))
		}
	})

	t.Run("-pyry-socket beats both", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "fromenv")
		got, err := parseClientFlags("pyry status", []string{
			"-pyry-name", "fromflag",
			"-pyry-socket", "/custom/explicit.sock",
		})
		if err != nil {
			t.Fatalf("parseClientFlags: %v", err)
		}
		if got != "/custom/explicit.sock" {
			t.Errorf("got = %q, want /custom/explicit.sock", got)
		}
	})

	t.Run("unknown flag returns error", func(t *testing.T) {
		_, err := parseClientFlags("pyry status", []string{"-unknown"})
		if err == nil {
			t.Fatal("expected error on unknown flag")
		}
	})
}

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
		{
			name:       "pyry-name with separate value",
			args:       []string{"-pyry-name", "elli", "summarize"},
			wantPyry:   []string{"-pyry-name", "elli"},
			wantClaude: []string{"summarize"},
		},
		{
			name:       "pyry-name with glued value",
			args:       []string{"-pyry-name=elli", "summarize"},
			wantPyry:   []string{"-pyry-name=elli"},
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

func TestResolveSocketPath(t *testing.T) {
	t.Parallel()

	t.Run("explicit -pyry-socket wins over name", func(t *testing.T) {
		t.Parallel()
		got := resolveSocketPath("/custom/path.sock", "elli")
		if got != "/custom/path.sock" {
			t.Errorf("got %q, want /custom/path.sock", got)
		}
	})

	t.Run("name with no socket flag uses ~/.pyry/<name>.sock pattern", func(t *testing.T) {
		t.Parallel()
		got := resolveSocketPath("", "elli")
		// Don't assert the home prefix — that depends on the test runner's
		// $HOME. Just check the filename and the .pyry component.
		if filepath.Base(got) != "elli.sock" {
			t.Errorf("filename = %q, want elli.sock", filepath.Base(got))
		}
		if !strings.Contains(got, ".pyry") {
			t.Errorf("path %q should contain .pyry", got)
		}
	})

	t.Run("default name yields pyry.sock", func(t *testing.T) {
		t.Parallel()
		got := resolveSocketPath("", DefaultName)
		if filepath.Base(got) != "pyry.sock" {
			t.Errorf("filename = %q, want pyry.sock", filepath.Base(got))
		}
	})

	t.Run("unsafe name is sanitised", func(t *testing.T) {
		t.Parallel()
		got := resolveSocketPath("", "../etc/passwd")
		base := filepath.Base(got)
		// Path traversal must not survive — no slashes, no colons.
		for _, bad := range []string{"/", ":", " "} {
			if strings.Contains(base, bad) {
				t.Errorf("sanitised filename %q still contains %q", base, bad)
			}
		}
	})
}

func TestSanitizeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"pyry", "pyry"},
		{"foo-bar_baz.txt", "foo-bar_baz.txt"},
		{"with spaces", "with_spaces"},
		{"emoji-🎯-here", "emoji-_-here"},
		{"slash/inside", "slash_inside"},
		{"colons:and;semis", "colons_and_semis"},
		{"", "_"},
		{"/", "_"},
		{"../etc/passwd", ".._etc_passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got := sanitizeName(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDefaultName(t *testing.T) {
	// Not parallel — mutates the environment.

	t.Run("returns DefaultName when PYRY_NAME is unset", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "")
		if got := defaultName(); got != DefaultName {
			t.Errorf("got %q, want %q", got, DefaultName)
		}
	})

	t.Run("returns PYRY_NAME when set", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "elli")
		if got := defaultName(); got != "elli" {
			t.Errorf("got %q, want elli", got)
		}
	})
}
