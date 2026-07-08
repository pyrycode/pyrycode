package sessions

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEncodeWorkdir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/", "-"},
		{"/foo/bar", "-foo-bar"},
		{"/foo/.bar", "-foo--bar"},
		{"/Users/x/Workspace/Projects/.pyrycode-worktrees/architect-38",
			"-Users-x-Workspace-Projects--pyrycode-worktrees-architect-38"},
		{"foo.bar", "foo-bar"},
		{"a..b", "a--b"},
		{"a//b", "a--b"},
		// Spaces (and any other non-alphanumeric) must map to '-' too, matching
		// claude. The '/'-and-'.'-only encoder left spaces intact and could not
		// find the transcript for a workdir like the vault's "Second Brain".
		{"a b c", "a-b-c"},
		{"/foo/Second Brain", "-foo-Second-Brain"},
		{"/Users/juhanailmoniemi/obsidian-vault/Second Brain",
			"-Users-juhanailmoniemi-obsidian-vault-Second-Brain"},
	}
	for _, c := range cases {
		if got := encodeWorkdir(c.in); got != c.want {
			t.Errorf("encodeWorkdir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// touchJSONL creates dir/<id>.jsonl and stamps it with mtime.
func touchJSONL(t *testing.T, dir string, id string, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestMostRecentJSONL_PicksLatestMtime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	older := SessionID("00000000-0000-4000-8000-000000000001")
	middle := SessionID("00000000-0000-4000-8000-000000000002")
	newest := SessionID("00000000-0000-4000-8000-000000000003")

	base := time.Now().Add(-1 * time.Hour)
	touchJSONL(t, dir, string(older), base)
	touchJSONL(t, dir, string(middle), base.Add(10*time.Minute))
	touchJSONL(t, dir, string(newest), base.Add(20*time.Minute))

	got, err := mostRecentJSONL(dir)
	if err != nil {
		t.Fatalf("mostRecentJSONL: %v", err)
	}
	if got != newest {
		t.Errorf("got %q, want %q", got, newest)
	}
}

func TestMostRecentJSONL_IgnoresNonJSONL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	valid := SessionID("11111111-1111-4111-8111-111111111111")
	now := time.Now()
	touchJSONL(t, dir, string(valid), now.Add(-time.Hour))

	// noise: non-jsonl extensions, malformed UUID stems, wrong-length stems,
	// uppercase (non-canonical), backup files, and a subdirectory.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl.bak"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "not-a-uuid.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stamp the noise files with a much-newer mtime so they would win if
	// they were not filtered out.
	for _, name := range []string{"notes.txt", "session.jsonl.bak", "not-a-uuid.jsonl", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA.jsonl"} {
		if err := os.Chtimes(filepath.Join(dir, name), now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := mostRecentJSONL(dir)
	if err != nil {
		t.Fatalf("mostRecentJSONL: %v", err)
	}
	if got != valid {
		t.Errorf("got %q, want %q (noise files should be ignored)", got, valid)
	}
}

func TestMostRecentJSONL_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := mostRecentJSONL(dir)
	if err != nil {
		t.Fatalf("mostRecentJSONL: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestMostRecentJSONL_SingleEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := SessionID("22222222-2222-4222-8222-222222222222")
	touchJSONL(t, dir, string(id), time.Now())
	got, err := mostRecentJSONL(dir)
	if err != nil {
		t.Fatalf("mostRecentJSONL: %v", err)
	}
	if got != id {
		t.Errorf("got %q, want %q", got, id)
	}
}

func TestMostRecentJSONL_TieBreakDeterministic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Same mtime; lex-larger ID should win deterministically.
	a := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	b := SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	when := time.Now()
	touchJSONL(t, dir, string(a), when)
	touchJSONL(t, dir, string(b), when)
	for i := 0; i < 5; i++ {
		got, err := mostRecentJSONL(dir)
		if err != nil {
			t.Fatalf("mostRecentJSONL: %v", err)
		}
		if got != b {
			t.Errorf("iter %d: got %q, want %q (lex-larger on tie)", i, got, b)
		}
	}
}

func TestMostRecentJSONL_MissingDir(t *testing.T) {
	t.Parallel()
	got, err := mostRecentJSONL(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Errorf("got %q nil err, want a read error", got)
	}
}

// --- #668: newTranscriptResolver ----------------------------------------------

// TestNewTranscriptResolver_PicksNewestWithSize confirms the resolver returns
// the newest <uuid>.jsonl path (the same file mostRecentJSONL selects) and its
// real current byte size — the baseline/growth signal the commit-confirm reads.
func TestNewTranscriptResolver_PicksNewestWithSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	older := "00000000-0000-4000-8000-000000000001"
	newest := "00000000-0000-4000-8000-000000000002"
	base := time.Now().Add(-time.Hour)
	touchJSONL(t, dir, older, base)

	newestPath := filepath.Join(dir, newest+".jsonl")
	content := []byte(`{"type":"user"}` + "\n")
	if err := os.WriteFile(newestPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newestPath, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	path, size, err := newTranscriptResolver(dir)(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if path != newestPath {
		t.Errorf("path = %q, want %q", path, newestPath)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
}

// TestNewTranscriptResolver_EmptyDir: a dir with no <uuid>.jsonl resolves to the
// no-transcript-yet sentinel ("", 0, nil) — a valid baseline, not an error.
func TestNewTranscriptResolver_EmptyDir(t *testing.T) {
	t.Parallel()
	path, size, err := newTranscriptResolver(t.TempDir())(context.Background())
	if err != nil || path != "" || size != 0 {
		t.Errorf("resolve = (%q, %d, %v), want (\"\", 0, nil)", path, size, err)
	}
}

// TestNewTranscriptResolver_MissingDir: an unreadable dir propagates the ReadDir
// error so the confirm path can fall back rather than treat it as no-growth.
func TestNewTranscriptResolver_MissingDir(t *testing.T) {
	t.Parallel()
	_, _, err := newTranscriptResolver(filepath.Join(t.TempDir(), "does-not-exist"))(context.Background())
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want errors.Is(err, fs.ErrNotExist)", err)
	}
}

// TestNewTranscriptResolver_IgnoresNonMatching: non-UUID / non-.jsonl noise is
// skipped (inherited from mostRecentJSONL), even when newer than the real one.
func TestNewTranscriptResolver_IgnoresNonMatching(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	valid := "11111111-1111-4111-8111-111111111111"
	touchJSONL(t, dir, valid, time.Now().Add(-time.Hour))
	if err := os.WriteFile(filepath.Join(dir, "not-a-uuid.jsonl"), []byte("xxxx"), 0o600); err != nil {
		t.Fatal(err)
	}

	path, _, err := newTranscriptResolver(dir)(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(dir, valid+".jsonl")
	if path != want {
		t.Errorf("path = %q, want %q (noise ignored)", path, want)
	}
}

// --- #838: newProbePreferredTranscriptResolver --------------------------------

// stubProbe is a scriptable rotation.Probe: OpenJSONL returns (path, err) and
// counts calls. It omits Available() so probeUsable treats it as usable — the
// same "usable by omission" signal the real darwinProbe / linuxProbe give.
type stubProbe struct {
	path  string
	err   error
	calls int
}

func (p *stubProbe) OpenJSONL(int) (string, error) {
	p.calls++
	return p.path, p.err
}

// unavailableProbe mirrors the no-lsof noopProbe: Available() == false, so
// probeUsable reports it unusable and the resolver delegates to
// newTranscriptResolver (AC5).
type unavailableProbe struct{}

func (unavailableProbe) OpenJSONL(int) (string, error) { return "", nil }
func (unavailableProbe) Available() bool               { return false }

func constPID(pid int) func() int { return func() int { return pid } }

// resolvedTempDir returns a t.TempDir() with symlinks resolved, so probed-path
// fixtures compare cleanly against the resolver's EvalSymlinks(dir) guard base
// on macOS (where /var -> /private/var).
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks tempdir: %v", err)
	}
	return dir
}

// TestProbePreferredResolver_TailsOwnChildNotNewestByMtime: the resolver returns
// the transcript the probe reports for the daemon's own child, with its real byte
// size, even when a foreign <uuid>.jsonl in the same dir has a NEWER mtime (AC1 +
// AC2). Proves mtime is never consulted on the probe path.
func TestProbePreferredResolver_TailsOwnChildNotNewestByMtime(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)

	own := "00000000-0000-4000-8000-00000000dead"
	foreign := "11111111-1111-4111-8111-111111111111"
	base := time.Now().Add(-time.Hour)

	ownPath := filepath.Join(dir, own+".jsonl")
	content := []byte(`{"type":"user"}` + "\n")
	if err := os.WriteFile(ownPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(ownPath, base, base); err != nil {
		t.Fatal(err)
	}
	// Foreign sibling written more recently — would win newest-by-mtime.
	touchJSONL(t, dir, foreign, base.Add(30*time.Minute))

	probe := &stubProbe{path: ownPath}
	resolve := newProbePreferredTranscriptResolver(dir, probe, constPID(4321))
	path, size, err := resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if path != ownPath {
		t.Errorf("path = %q, want %q (own child's file, not newest by mtime)", path, ownPath)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
}

// TestProbePreferredResolver_NoBaseline covers every probe-path condition that
// must yield the ("", 0, nil) no-baseline sentinel — a NIL error, never a non-nil
// error and never an mtime fallback (AC3, the load-bearing convention inversion
// vs the cmd/pyry sibling) — plus the AC4 confidentiality-guard rejections.
func TestProbePreferredResolver_NoBaseline(t *testing.T) {
	t.Parallel()

	const valid = "22222222-2222-4222-8222-222222222222"

	cases := []struct {
		name      string
		pid       int
		probePath func(t *testing.T, dir string) string // nil / "" -> empty probe result
		probeErr  error
		wantCalls int
	}{
		{name: "pid zero", pid: 0, wantCalls: 0},
		{name: "pid negative", pid: -1, wantCalls: 0},
		{name: "empty probe", pid: 7, wantCalls: 1},
		{
			name: "probe error", pid: 7, wantCalls: 1,
			probePath: func(_ *testing.T, dir string) string { return filepath.Join(dir, valid+".jsonl") },
			probeErr:  errors.New("lsof boom"),
		},
		{
			name: "outside dir", pid: 7, wantCalls: 1,
			probePath: func(t *testing.T, _ string) string { return filepath.Join(resolvedTempDir(t), valid+".jsonl") },
		},
		{
			name: "non-uuid stem", pid: 7, wantCalls: 1,
			probePath: func(_ *testing.T, dir string) string { return filepath.Join(dir, "not-a-uuid.jsonl") },
		},
		{
			name: "missing jsonl suffix", pid: 7, wantCalls: 1,
			probePath: func(_ *testing.T, dir string) string { return filepath.Join(dir, valid) },
		},
		{
			name: "vanished before stat", pid: 7, wantCalls: 1,
			probePath: func(_ *testing.T, dir string) string { return filepath.Join(dir, valid+".jsonl") },
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := resolvedTempDir(t)
			var path string
			if c.probePath != nil {
				path = c.probePath(t, dir)
			}
			probe := &stubProbe{path: path, err: c.probeErr}
			resolve := newProbePreferredTranscriptResolver(dir, probe, constPID(c.pid))
			gotPath, gotSize, err := resolve(context.Background())
			if gotPath != "" || gotSize != 0 || err != nil {
				t.Errorf("resolve = (%q, %d, %v), want (\"\", 0, nil)", gotPath, gotSize, err)
			}
			if probe.calls != c.wantCalls {
				t.Errorf("probe calls = %d, want %d", probe.calls, c.wantCalls)
			}
		})
	}
}

// TestProbePreferredResolver_NoLsofMatchesMtimeBaseline: when the probe is
// unavailable (no lsof), the constructor delegates to newTranscriptResolver, so
// its output is byte-identical to today's newest-by-mtime baseline across the
// newest-pick, empty-dir, and missing-dir fixtures (AC5). pidFn returns a live
// PID to prove the unusable-probe branch ignores it entirely.
func TestProbePreferredResolver_NoLsofMatchesMtimeBaseline(t *testing.T) {
	t.Parallel()

	newestDir := t.TempDir()
	older := "00000000-0000-4000-8000-000000000001"
	newest := "00000000-0000-4000-8000-000000000002"
	b := time.Now().Add(-time.Hour)
	touchJSONL(t, newestDir, older, b)
	touchJSONL(t, newestDir, newest, b.Add(time.Minute))

	emptyDir := t.TempDir()
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")

	for _, dir := range []string{newestDir, emptyDir, missingDir} {
		got := newProbePreferredTranscriptResolver(dir, unavailableProbe{}, constPID(999))
		want := newTranscriptResolver(dir)
		gp, gs, ge := got(context.Background())
		wp, ws, we := want(context.Background())
		if gp != wp || gs != ws || (ge == nil) != (we == nil) {
			t.Errorf("dir %s: probe-preferred = (%q,%d,%v), newTranscriptResolver = (%q,%d,%v)",
				dir, gp, gs, ge, wp, ws, we)
		}
	}
}
