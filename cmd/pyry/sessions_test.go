package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	err := runSessions([]string{"bogus"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown verb") {
		t.Errorf("error %q missing %q fragment", msg, "unknown verb")
	}
	if !strings.Contains(msg, `"bogus"`) {
		t.Errorf("error %q missing offending verb %q", msg, "bogus")
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

// TestRunSessions_RenameDispatch pins AC#4's router wiring: the
// `case "rename":` arm exists and routes to runSessionsRename. Verified
// by passing a deliberately-bogus socket path and observing the
// resulting error path is the dial failure (wrapped as
// "sessions rename:"), not the help-style "unknown verb" router error.
func TestRunSessions_RenameDispatch(t *testing.T) {
	t.Setenv("PYRY_NAME", "")
	bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

	err := runSessions([]string{"-pyry-socket", bogusSock, "rename", "abc", "alpha"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown verb") {
		t.Errorf("router did not dispatch rename: %v", err)
	}
	if !strings.Contains(msg, "sessions rename:") {
		t.Errorf("error %q missing %q wrap fragment", msg, "sessions rename:")
	}
}

func TestParseSessionsRenameArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		wantID    string
		wantLabel string
		wantUsage bool   // expect errors.Is(err, errSessionsRenameUsage)
		wantErr   string // substring match against err.Error(); empty means no error
	}{
		{"no args", nil, "", "", true, "expected <id> <new-label>"},
		{"only id", []string{"abc"}, "", "", true, "expected <id> <new-label>"},
		{"id and label", []string{"abc", "alpha"}, "abc", "alpha", false, ""},
		{"empty label clears", []string{"abc", ""}, "abc", "", false, ""},
		{"too many positionals", []string{"abc", "alpha", "extra"}, "", "", true, "expected <id> <new-label>"},
		{"label with spaces", []string{"abc", "hello world"}, "abc", "hello world", false, ""},
		{"unknown flag", []string{"--unknown", "abc", "alpha"}, "", "", true, "flag provided but not defined"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, label, err := parseSessionsRenameArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (id=%q, label=%q)",
						tt.wantErr, id, label)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				if tt.wantUsage && !errors.Is(err, errSessionsRenameUsage) {
					t.Errorf("error %q does not match errSessionsRenameUsage sentinel", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
			if label != tt.wantLabel {
				t.Errorf("label = %q, want %q", label, tt.wantLabel)
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

// TestRunSessions_ListDispatch pins AC#4's router wiring: the
// `case "list":` arm exists and routes to runSessionsList. Verified by
// passing a deliberately-bogus socket path and observing the resulting
// error path is the dial failure (wrapped as "sessions list:"), not the
// help-style "unknown verb" router error.
func TestRunSessions_ListDispatch(t *testing.T) {
	t.Setenv("PYRY_NAME", "")
	bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

	err := runSessions([]string{"-pyry-socket", bogusSock, "list"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown verb") {
		t.Errorf("router did not dispatch list: %v", err)
	}
	if !strings.Contains(msg, "sessions list:") {
		t.Errorf("error %q missing %q wrap fragment", msg, "sessions list:")
	}
}

func TestParseSessionsListArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantJSON bool
		wantErr  string // substring; empty means no error
	}{
		{"no args", nil, false, ""},
		{"empty slice", []string{}, false, ""},
		{"--json", []string{"--json"}, true, ""},
		{"-json single dash", []string{"-json"}, true, ""},
		{"--json=true", []string{"--json=true"}, true, ""},
		{"--json=false", []string{"--json=false"}, false, ""},
		{"unknown flag", []string{"--unknown"}, false, "flag provided but not defined"},
		{"unexpected positional", []string{"extra"}, false, "unexpected positional"},
		{"--json then positional", []string{"--json", "extra"}, false, "unexpected positional"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jsonOut, err := parseSessionsListArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (jsonOut=%v)", tt.wantErr, jsonOut)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if jsonOut != tt.wantJSON {
				t.Errorf("jsonOut = %v, want %v", jsonOut, tt.wantJSON)
			}
		})
	}
}

// listFixture is the deterministic 3-entry slice shared by the
// renderer/sort tests. LastActive timestamps are chosen so that
// sort-by-last-active-desc with ID-asc tiebreak produces a known order
// (id "bb..." most recent, then "aa..." and "cc..." with equal stamps —
// "aa..." wins the tiebreak).
func listFixture() []control.SessionInfo {
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	return []control.SessionInfo{
		{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Label: "alpha", State: "active", LastActive: t0},
		{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Label: "", State: "active", LastActive: t0.Add(time.Minute), Bootstrap: true},
		{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Label: "gamma", State: "evicted", LastActive: t0},
	}
}

func TestWriteSessionsTable(t *testing.T) {
	t.Parallel()
	list := listFixture()
	sortSessionsForDisplay(list)

	var buf bytes.Buffer
	if err := writeSessionsTable(&buf, list); err != nil {
		t.Fatalf("writeSessionsTable: %v", err)
	}

	out := buf.String()
	lines := strings.Split(out, "\n")
	// header + 3 rows + trailing empty (single trailing newline → split
	// produces 5 elements, last is empty).
	if len(lines) != 5 {
		t.Fatalf("expected 4 lines + trailing newline (5 split parts), got %d:\n%s", len(lines), out)
	}
	if lines[4] != "" {
		t.Errorf("expected single trailing newline, got trailing data %q", lines[4])
	}

	// Header check: tabwriter pads with two spaces between columns. The
	// widest UUID is 36 chars; "alpha"/"gamma" both 5 wide; "active"/
	// "evicted" max 7.
	wantHeader := "UUID                                  LABEL  STATE    LAST-ACTIVE"
	if lines[0] != wantHeader {
		t.Errorf("header mismatch:\n got: %q\nwant: %q", lines[0], wantHeader)
	}

	// UUIDs appear unmodified at column 0 (full 36 chars; no truncation).
	wantOrder := []string{
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
	}
	for i, want := range wantOrder {
		if !strings.HasPrefix(lines[i+1], want) {
			t.Errorf("row %d: expected to start with %q, got %q", i+1, want, lines[i+1])
		}
	}

	// Bootstrap row (line 1, since bb is most recent) has empty label —
	// verify the row contains the state and didn't crash column alignment.
	if !strings.Contains(lines[1], "active") {
		t.Errorf("bootstrap row missing state %q: %q", "active", lines[1])
	}
}

func TestWriteSessionsJSON(t *testing.T) {
	t.Parallel()
	list := listFixture()
	sortSessionsForDisplay(list)

	var buf bytes.Buffer
	if err := writeSessionsJSON(&buf, list); err != nil {
		t.Fatalf("writeSessionsJSON: %v", err)
	}

	var got struct {
		Sessions []control.SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbytes: %s", err, buf.String())
	}
	if len(got.Sessions) != 3 {
		t.Fatalf("len(sessions) = %d, want 3", len(got.Sessions))
	}

	for i, want := range list {
		have := got.Sessions[i]
		if have.ID != want.ID {
			t.Errorf("session[%d].id = %q, want %q", i, have.ID, want.ID)
		}
		if have.Label != want.Label {
			t.Errorf("session[%d].label = %q, want %q", i, have.Label, want.Label)
		}
		if have.State != want.State {
			t.Errorf("session[%d].state = %q, want %q", i, have.State, want.State)
		}
		if !have.LastActive.Equal(want.LastActive) {
			t.Errorf("session[%d].last_active = %v, want %v", i, have.LastActive, want.LastActive)
		}
		if have.Bootstrap != want.Bootstrap {
			t.Errorf("session[%d].bootstrap = %v, want %v", i, have.Bootstrap, want.Bootstrap)
		}
	}

	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("expected trailing newline; output: %q", buf.String())
	}
}

func TestSortSessionsForDisplay(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	t2 := t0.Add(2 * time.Minute)

	tests := []struct {
		name string
		in   []control.SessionInfo
		want []string // expected ID order after sort
	}{
		{
			name: "already sorted descending",
			in: []control.SessionInfo{
				{ID: "a", LastActive: t2},
				{ID: "b", LastActive: t1},
				{ID: "c", LastActive: t0},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "reverse-sorted ascending flipped",
			in: []control.SessionInfo{
				{ID: "a", LastActive: t0},
				{ID: "b", LastActive: t1},
				{ID: "c", LastActive: t2},
			},
			want: []string{"c", "b", "a"},
		},
		{
			name: "equal timestamps tiebreak by ID asc",
			in: []control.SessionInfo{
				{ID: "c", LastActive: t0},
				{ID: "a", LastActive: t0},
				{ID: "b", LastActive: t0},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "empty slice",
			in:   []control.SessionInfo{},
			want: nil,
		},
		{
			name: "single element",
			in:   []control.SessionInfo{{ID: "x", LastActive: t0}},
			want: []string{"x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sortSessionsForDisplay(tt.in)
			if len(tt.in) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(tt.in), len(tt.want))
			}
			for i, want := range tt.want {
				if tt.in[i].ID != want {
					t.Errorf("[%d].ID = %q, want %q", i, tt.in[i].ID, want)
				}
			}
		})
	}
}
