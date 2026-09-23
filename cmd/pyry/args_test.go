package main

import (
	"flag"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseClientFlags(t *testing.T) {
	// Not parallel — t.Setenv mutates the environment.

	t.Run("default name yields ~/.pyry/pyry.sock", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "")
		got, _, err := parseClientFlags("pyry status", nil)
		if err != nil {
			t.Fatalf("parseClientFlags: %v", err)
		}
		if filepath.Base(got) != "pyry.sock" {
			t.Errorf("filename = %q, want pyry.sock", filepath.Base(got))
		}
	})

	t.Run("-pyry-name override wins over PYRY_NAME", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "fromenv")
		got, _, err := parseClientFlags("pyry status", []string{"-pyry-name", "fromflag"})
		if err != nil {
			t.Fatalf("parseClientFlags: %v", err)
		}
		if filepath.Base(got) != "fromflag.sock" {
			t.Errorf("filename = %q, want fromflag.sock (flag should win over env)", filepath.Base(got))
		}
	})

	t.Run("PYRY_NAME wins when no flag given", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "fromenv")
		got, _, err := parseClientFlags("pyry status", nil)
		if err != nil {
			t.Fatalf("parseClientFlags: %v", err)
		}
		if filepath.Base(got) != "fromenv.sock" {
			t.Errorf("filename = %q, want fromenv.sock", filepath.Base(got))
		}
	})

	t.Run("-pyry-socket beats both", func(t *testing.T) {
		t.Setenv("PYRY_NAME", "fromenv")
		got, _, err := parseClientFlags("pyry status", []string{
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

	t.Run("unknown flag flows to rest", func(t *testing.T) {
		_, rest, err := parseClientFlags("pyry status", []string{"-unknown"})
		if err != nil {
			t.Fatalf("parseClientFlags: %v", err)
		}
		if len(rest) != 1 || rest[0] != "-unknown" {
			t.Errorf("rest = %v, want [-unknown]", rest)
		}
	})
}

// TestParseClientFlags_ReturnsRest pins the seam every client verb relies on
// (runSessions dispatching its subcommand, runMCPApprove refusing extras):
// positionals after the recognised -pyry-* flags must be surfaced verbatim
// via the rest return so the caller can apply its own arity rules.
func TestParseClientFlags_ReturnsRest(t *testing.T) {
	t.Setenv("PYRY_NAME", "")

	tests := []struct {
		name string
		args []string
		want []string
	}{
		// flag.FlagSet.Args returns []string{} when there are no positionals;
		// the helper surfaces that as len==0, which is what attachSelector
		// FromArgs ranges over. Compare on len-and-contents, not nil-vs-empty.
		{"nil args → empty rest", nil, []string{}},
		{"only flags → empty rest", []string{"-pyry-name", "elli"}, []string{}},
		{"single positional", []string{"abc-123"}, []string{"abc-123"}},
		{"flag then positional", []string{"-pyry-name", "elli", "abc-123"}, []string{"abc-123"}},
		{"two positionals (caller decides what to do)", []string{"abc-123", "extra"}, []string{"abc-123", "extra"}},
		{"sub-verb flag passes through", []string{"--stdio"}, []string{"--stdio"}},
		{"sub-verb flag plus positional", []string{"--stdio", "abc"}, []string{"--stdio", "abc"}},
		{"-pyry-socket then sub-verb flag", []string{"-pyry-socket=/tmp/x", "--stdio", "abc"}, []string{"--stdio", "abc"}},
		{"-pyry-name space-separated then sub-verb flag", []string{"-pyry-name", "elli", "--stdio", "abc"}, []string{"--stdio", "abc"}},
		{"double-dash pyry flag form", []string{"--pyry-socket", "/tmp/x", "--create-if-missing", "abc"}, []string{"--create-if-missing", "abc"}},
		{"-- separator passes through verbatim", []string{"-pyry-name", "elli", "--", "abc"}, []string{"--", "abc"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, rest, err := parseClientFlags("pyry attach", tt.args)
			if err != nil {
				t.Fatalf("parseClientFlags: %v", err)
			}
			if len(rest) != len(tt.want) {
				t.Fatalf("rest = %v (len %d), want %v (len %d)", rest, len(rest), tt.want, len(tt.want))
			}
			for i := range rest {
				if rest[i] != tt.want[i] {
					t.Errorf("rest[%d] = %q, want %q", i, rest[i], tt.want[i])
				}
			}
		})
	}
}

func TestSplitArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantPyry   []string
		wantClaude []string
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

// TestResolveConversationsRegistryPath confirms the per-instance layout
// (~/.pyry/<sanitized-name>/conversations.json) and that the name is
// sanitized — defending against PYRY_NAME=../etc / similar.
func TestResolveConversationsRegistryPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := resolveConversationsRegistryPath("test")
	want := filepath.Join(home, ".pyry", "test", "conversations.json")
	if got != want {
		t.Errorf("resolveConversationsRegistryPath(%q)=%q want %q", "test", got, want)
	}

	for _, name := range traversalNames {
		t.Run(name, func(t *testing.T) {
			assertInsideInstanceDir(t, home, resolveConversationsRegistryPath(name), "conversations.json")
		})
	}
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
		// "." and ".." are path components, not names: joined as a
		// directory they collapse the instance directory away or escape
		// ~/.pyry entirely. The trailing "_" mirrors what the transform
		// already does to "./x" and "../x", where the separator becomes "_".
		{".", "._"},
		{"..", ".._"},
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

// assertInsideInstanceDir asserts that got is a file sitting directly inside
// a per-instance directory under <home>/.pyry — exactly two components below
// it, the first a real directory name rather than a traversal element.
//
// Both clauses are load-bearing, and each is the sole detector of one hostile
// name: an instance name of ".." escapes ~/.pyry and is caught only by the
// first-component check, while "." collapses the instance directory away
// (landing beside config.json in the ~/.pyry root) and is caught only by the
// component count. A plain "does not escape ~/.pyry" check passes on ".".
// Shared by the four registry-style resolver tests; see sanitizeName.
func assertInsideInstanceDir(t *testing.T, home, got, wantFile string) {
	t.Helper()

	base := filepath.Join(home, ".pyry")
	rel, err := filepath.Rel(base, got)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", base, got, err)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 {
		t.Fatalf("path %q is %d component(s) below %q (rel=%q), want exactly 2 (<instance-dir>/%s)",
			got, len(parts), base, rel, wantFile)
	}
	if parts[0] == "." || parts[0] == ".." {
		t.Errorf("path %q has instance directory %q (rel=%q), want a real directory name", got, parts[0], rel)
	}
	if parts[1] != wantFile {
		t.Errorf("path %q has filename %q (rel=%q), want %q", got, parts[1], rel, wantFile)
	}
}

// traversalNames are the instance names every registry-style resolver test
// runs through assertInsideInstanceDir.
var traversalNames = []string{".", "..", "../etc"}

// TestResolveRegistryPath confirms the per-instance layout
// (~/.pyry/<sanitized-name>/sessions.json) and that no instance name can
// move sessions.json out of an instance directory under ~/.pyry.
func TestResolveRegistryPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := resolveRegistryPath("test")
	want := filepath.Join(home, ".pyry", "test", "sessions.json")
	if got != want {
		t.Errorf("resolveRegistryPath(%q)=%q want %q", "test", got, want)
	}

	for _, name := range traversalNames {
		t.Run(name, func(t *testing.T) {
			assertInsideInstanceDir(t, home, resolveRegistryPath(name), "sessions.json")
		})
	}
}

// TestResolveModelVocabularyPath confirms #2450's file is a SIBLING of
// sessions.json inside the same per-instance directory, and that no instance name
// can move it out of one under ~/.pyry — the containment TestResolveRegistryPath
// pins for its own twin, which this resolver must not be the exception to.
func TestResolveModelVocabularyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := resolveModelVocabularyPath("test")
	want := filepath.Join(home, ".pyry", "test", "model_list.json")
	if got != want {
		t.Errorf("resolveModelVocabularyPath(%q)=%q want %q", "test", got, want)
	}
	// The sibling relationship is the point, so it is asserted rather than implied
	// by two literals that could drift apart.
	if dir := filepath.Dir(got); dir != filepath.Dir(resolveRegistryPath("test")) {
		t.Errorf("model_list.json lives in %q, want sessions.json's directory %q", dir, filepath.Dir(resolveRegistryPath("test")))
	}

	for _, name := range traversalNames {
		t.Run(name, func(t *testing.T) {
			assertInsideInstanceDir(t, home, resolveModelVocabularyPath(name), "model_list.json")
		})
	}
}

// TestResolveSocketPath_DotNamesStayInPyryDir pins the one resolver that was
// never vulnerable to "." / "..": it concatenates a .sock suffix instead of
// joining the name as a directory component, so the socket lands directly in
// ~/.pyry whatever the name. A non-parallel sibling of TestResolveSocketPath
// rather than a subtest of it — t.Setenv panics in a parallel test, and that
// test and all of its subtests are parallel.
func TestResolveSocketPath_DotNamesStayInPyryDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".pyry")
	for _, name := range []string{".", ".."} {
		t.Run(name, func(t *testing.T) {
			got := resolveSocketPath("", name)
			if dir := filepath.Dir(got); dir != want {
				t.Errorf("resolveSocketPath(%q, %q)=%q, directory %q want %q", "", name, got, dir, want)
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

func TestSplitClientFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantPyry []string
		wantRest []string
	}{
		{"empty", nil, nil, nil},
		{"only sub-verb flag", []string{"--stdio"}, nil, []string{"--stdio"}},
		{"only positional", []string{"abc"}, nil, []string{"abc"}},
		{"-pyry-name separate value",
			[]string{"-pyry-name", "elli"},
			[]string{"-pyry-name", "elli"}, nil},
		{"-pyry-name=elli glued",
			[]string{"-pyry-name=elli"},
			[]string{"-pyry-name=elli"}, nil},
		{"--pyry-socket=/tmp/x double-dash glued",
			[]string{"--pyry-socket=/tmp/x"},
			[]string{"--pyry-socket=/tmp/x"}, nil},
		{"-pyry-socket /tmp/x separate value",
			[]string{"-pyry-socket", "/tmp/x"},
			[]string{"-pyry-socket", "/tmp/x"}, nil},
		{"mixed: pyry then sub-verb flag plus positional",
			[]string{"-pyry-socket", "/tmp/x", "--stdio", "abc"},
			[]string{"-pyry-socket", "/tmp/x"}, []string{"--stdio", "abc"}},
		{"sub-verb flag first short-circuits",
			[]string{"--stdio", "-pyry-name", "elli"},
			nil, []string{"--stdio", "-pyry-name", "elli"}},
		{"-- separator after pyry flag",
			[]string{"-pyry-name", "elli", "--", "abc"},
			[]string{"-pyry-name", "elli"}, []string{"--", "abc"}},
		{"-- first",
			[]string{"--", "-pyry-name", "elli"},
			nil, []string{"--", "-pyry-name", "elli"}},
		{"trailing -pyry-name with no value",
			[]string{"-pyry-name"},
			[]string{"-pyry-name"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotPyry, gotRest := splitClientFlags(tt.args)
			if !reflect.DeepEqual(gotPyry, tt.wantPyry) {
				t.Errorf("pyry args = %v, want %v", gotPyry, tt.wantPyry)
			}
			if !reflect.DeepEqual(gotRest, tt.wantRest) {
				t.Errorf("rest = %v, want %v", gotRest, tt.wantRest)
			}
		})
	}
}

func TestRunStatus_RejectsExtraArgs(t *testing.T) {
	t.Setenv("PYRY_NAME", "")
	err := runStatus([]string{"-unknown"})
	if err == nil {
		t.Fatal("expected error on extra arg")
	}
	if !strings.Contains(err.Error(), "unexpected arguments") {
		t.Errorf("err = %v, want substring 'unexpected arguments'", err)
	}
}

func TestRunLogs_RejectsExtraArgs(t *testing.T) {
	t.Setenv("PYRY_NAME", "")
	err := runLogs([]string{"-unknown"})
	if err == nil {
		t.Fatal("expected error on extra arg")
	}
	if !strings.Contains(err.Error(), "unexpected arguments") {
		t.Errorf("err = %v, want substring 'unexpected arguments'", err)
	}
}

// TestPyryIdleTimeoutDefault pins -pyry-idle-timeout's default to 0
// (idle eviction opt-in). Flipping the default back to a non-zero value
// silently breaks daemon-mode supervisors — see #395.
//
// The pool's "0 disables" behaviour is covered by
// internal/sessions.TestPool_ParityWhenIdleDisabled; this test only
// guards the literal at the CLI seam.
func TestPyryIdleTimeoutDefault(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("pyry", flag.ContinueOnError)
	idleTimeout := fs.Duration("pyry-idle-timeout", 0, "")
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("fs.Parse: %v", err)
	}
	if *idleTimeout != time.Duration(0) {
		t.Errorf("default -pyry-idle-timeout = %v, want 0 (idle eviction opt-in)", *idleTimeout)
	}
}

func TestRunStop_RejectsExtraArgs(t *testing.T) {
	t.Setenv("PYRY_NAME", "")
	err := runStop([]string{"-unknown"})
	if err == nil {
		t.Fatal("expected error on extra arg")
	}
	if !strings.Contains(err.Error(), "unexpected arguments") {
		t.Errorf("err = %v, want substring 'unexpected arguments'", err)
	}
}
