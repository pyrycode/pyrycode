package transcript

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	uuidA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	uuidB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	uuidC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// writeJSONL writes dir/<uuid>.jsonl of n bytes, stamps its mtime, and returns
// the path. Mirrors the reconcile_test / interactive_turn_stream_v2_test idiom.
func writeJSONL(t *testing.T, dir, uuid string, n int, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, uuid+Ext)
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), n), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

// resolvedTempDir returns a t.TempDir() with symlinks resolved, so probed-path
// fixtures compare cleanly against the guard's canonicalDir on macOS (where
// /var -> /private/var).
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks tempdir: %v", err)
	}
	return dir
}

// probeResult is one scripted OpenJSONL answer.
type probeResult struct {
	path string
	err  error
}

// fakeProbe is a scriptable transcript.Probe: each OpenJSONL call pops the next
// result (repeating the last once exhausted) and records the probed pid + call
// count so "probe not called" is directly assertable.
type fakeProbe struct {
	results []probeResult
	calls   int
	pids    []int
}

func (f *fakeProbe) OpenJSONL(pid int) (string, error) {
	f.pids = append(f.pids, pid)
	i := f.calls
	f.calls++
	if len(f.results) == 0 {
		return "", nil
	}
	if i >= len(f.results) {
		i = len(f.results) - 1
	}
	return f.results[i].path, f.results[i].err
}

func TestValidStem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		stem string
		want bool
	}{
		{"canonical lowercase v4", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", true},
		{"real-shaped hex", "00000000-0000-4000-8000-00000000dead", true},
		{"uppercase rejected", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", false},
		{"too short", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa", false},
		{"too long", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaaa", false},
		{"non-hex", "gggggggg-gggg-4ggg-8ggg-gggggggggggg", false},
		{"carries jsonl suffix", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.jsonl", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidStem(c.stem); got != c.want {
				t.Errorf("ValidStem(%q) = %v, want %v", c.stem, got, c.want)
			}
		})
	}
}

func TestResultFound(t *testing.T) {
	t.Parallel()
	if (Result{}).Found() {
		t.Error("zero Result.Found() = true, want false")
	}
	if !(Result{Path: "x"}).Found() {
		t.Error("Result{Path:x}.Found() = false, want true")
	}
}

func TestCanonicalDir(t *testing.T) {
	t.Parallel()

	t.Run("resolves symlink", func(t *testing.T) {
		t.Parallel()
		real := resolvedTempDir(t)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if got := CanonicalDir(link); got != real {
			t.Fatalf("CanonicalDir(%q) = %q, want %q", link, got, real)
		}
	})

	t.Run("cleans on eval error", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "a", "..", "b")
		if got := CanonicalDir(missing); got != filepath.Clean(missing) {
			t.Fatalf("CanonicalDir(%q) = %q, want %q (Clean fallback)", missing, got, filepath.Clean(missing))
		}
	})
}

// TestProbed_ProbeWinsOverNewerSibling is the core-bug gate. Two claudes write
// into the same shared dir: the daemon's own (older, smaller) transcript and a
// newer + larger foreign sibling. Newest-by-mtime would pick the sibling; the
// probe-preferred core must return the file the daemon's own pid holds, with its
// real size — proving mtime is never consulted on the probe path.
func TestProbed_ProbeWinsOverNewerSibling(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)
	base := time.Now().Add(-time.Hour)
	own := writeJSONL(t, dir, uuidA, 30, base)              // daemon's own, older + smaller
	writeJSONL(t, dir, uuidB, 99, base.Add(10*time.Minute)) // foreign, newer + larger

	probe := &fakeProbe{results: []probeResult{{path: own}}}
	got, err := Probed(dir, CanonicalDir(dir), probe, 4242)
	if err != nil {
		t.Fatalf("Probed: %v", err)
	}
	if !got.Found() || got.Path != own {
		t.Fatalf("Path = %q (found=%v), want %q (own child's file, not newest by mtime)", got.Path, got.Found(), own)
	}
	if got.Size != 30 {
		t.Fatalf("Size = %d, want 30 (own file's real size)", got.Size)
	}
	if len(probe.pids) != 1 || probe.pids[0] != 4242 {
		t.Fatalf("probe pids = %v, want [4242]", probe.pids)
	}
}

// TestProbed_PidNonPositiveProbeNotCalled: a non-positive pid (child in restart
// backoff / pre-spawn) yields the zero Result with a nil error and NEVER calls
// the probe.
func TestProbed_PidNonPositiveProbeNotCalled(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)
	own := writeJSONL(t, dir, uuidA, 10, time.Now())
	for _, pid := range []int{0, -1} {
		probe := &fakeProbe{results: []probeResult{{path: own}}}
		got, err := Probed(dir, CanonicalDir(dir), probe, pid)
		if err != nil || got.Found() {
			t.Fatalf("pid %d: got (%+v, %v), want (Result{}, nil)", pid, got, err)
		}
		if len(probe.pids) != 0 {
			t.Fatalf("pid %d: probe recorded pids %v, want none (probe not called)", pid, probe.pids)
		}
	}
}

// TestProbed_EmptyProbe: an empty probe answer (the child holds no JSONL fd yet)
// yields the zero Result + nil error and must NEVER fall back to the newest file
// by mtime — a newer decoy is present to catch a regression to mtime.
func TestProbed_EmptyProbe(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)
	writeJSONL(t, dir, uuidB, 50, time.Now()) // decoy the mtime path would pick
	probe := &fakeProbe{results: []probeResult{{path: ""}}}
	got, err := Probed(dir, CanonicalDir(dir), probe, 7)
	if err != nil || got.Found() {
		t.Fatalf("empty probe: got (%+v, %v), want (Result{}, nil) — must not tail the decoy", got, err)
	}
	if probe.calls != 1 {
		t.Fatalf("empty probe: calls = %d, want 1", probe.calls)
	}
}

// TestProbed_ErroredProbe: a probe error is the one genuinely-surfaced
// condition — Probed returns the raw error so a retry-on-error adapter can wrap
// it. The Result stays zero.
func TestProbed_ErroredProbe(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)
	boom := errors.New("lsof boom")
	probe := &fakeProbe{results: []probeResult{{err: boom}}}
	got, err := Probed(dir, CanonicalDir(dir), probe, 7)
	if !errors.Is(err, boom) {
		t.Fatalf("errored probe: err = %v, want %v (surfaced verbatim)", err, boom)
	}
	if got.Found() {
		t.Fatalf("errored probe: Result = %+v, want zero", got)
	}
}

// TestProbed_GuardRejections covers every guard/raced condition that must
// collapse to the zero Result + nil error (never mtime): a probed path outside
// the trusted dir, a non-UUID stem, a valid UUID without the .jsonl suffix, and
// an in-dir valid path that vanishes before the stat.
func TestProbed_GuardRejections(t *testing.T) {
	t.Parallel()
	const valid = "22222222-2222-4222-8222-222222222222"
	cases := []struct {
		name      string
		probePath func(t *testing.T, dir string) string
	}{
		{"outside dir", func(t *testing.T, _ string) string { return filepath.Join(resolvedTempDir(t), valid+Ext) }},
		{"non-uuid stem", func(_ *testing.T, dir string) string { return filepath.Join(dir, "not-a-uuid.jsonl") }},
		{"missing jsonl suffix", func(_ *testing.T, dir string) string { return filepath.Join(dir, valid) }},
		{"vanished before stat", func(_ *testing.T, dir string) string { return filepath.Join(dir, valid+Ext) }},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := resolvedTempDir(t)
			probe := &fakeProbe{results: []probeResult{{path: c.probePath(t, dir)}}}
			got, err := Probed(dir, CanonicalDir(dir), probe, 7)
			if err != nil || got.Found() {
				t.Fatalf("%s: got (%+v, %v), want (Result{}, nil)", c.name, got, err)
			}
			if probe.calls != 1 {
				t.Fatalf("%s: probe calls = %d, want 1", c.name, probe.calls)
			}
		})
	}
}

func TestStatByID(t *testing.T) {
	t.Parallel()

	t.Run("hit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		want := writeJSONL(t, dir, uuidA, 42, time.Now())
		got, err := StatByID(dir, uuidA)
		if err != nil {
			t.Fatalf("StatByID: %v", err)
		}
		if got.Path != want || got.Size != 42 {
			t.Fatalf("got %+v, want {Path:%q Size:42}", got, want)
		}
	})

	t.Run("miss", func(t *testing.T) {
		t.Parallel()
		got, err := StatByID(t.TempDir(), uuidA)
		if err == nil {
			t.Fatal("StatByID on missing file: got nil error, want os.Stat error")
		}
		if got.Found() {
			t.Fatalf("miss: Result = %+v, want zero", got)
		}
	})

	// An invalid stem is rejected structurally, BEFORE any filepath.Join — so a
	// traversal-shaped id can never escape dir. Assert the empty Result + error;
	// no filesystem access happens.
	t.Run("invalid stem rejected before join", func(t *testing.T) {
		t.Parallel()
		got, err := StatByID(t.TempDir(), "../../etc/passwd")
		if err == nil {
			t.Fatal("invalid stem: got nil error, want rejection")
		}
		if got.Found() {
			t.Fatalf("invalid stem: Result = %+v, want zero", got)
		}
	})
}

func TestNewest(t *testing.T) {
	t.Parallel()

	t.Run("newest by mtime", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		base := time.Now().Add(-time.Hour)
		writeJSONL(t, dir, uuidA, 10, base)
		want := writeJSONL(t, dir, uuidB, 20, base.Add(2*time.Minute)) // newest
		writeJSONL(t, dir, uuidC, 30, base.Add(time.Minute))
		got, err := Newest(dir)
		if err != nil {
			t.Fatalf("Newest: %v", err)
		}
		if got.Path != want || got.Size != 20 {
			t.Fatalf("got %+v, want {Path:%q Size:20}", got, want)
		}
	})

	t.Run("mtime tie breaks on larger stem", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		tie := time.Now().Add(-time.Minute)
		writeJSONL(t, dir, uuidA, 5, tie)
		writeJSONL(t, dir, uuidB, 6, tie)
		writeJSONL(t, dir, uuidC, 7, tie) // lexicographically-largest stem
		got, err := Newest(dir)
		if err != nil {
			t.Fatalf("Newest: %v", err)
		}
		if want := filepath.Join(dir, uuidC+Ext); got.Path != want {
			t.Fatalf("tie: Path = %q, want %q (lexicographically-larger stem)", got.Path, want)
		}
	})

	t.Run("ignores non-uuid, wrong-ext, and subdirs", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		want := writeJSONL(t, dir, uuidA, 8, time.Now())
		if err := os.WriteFile(filepath.Join(dir, "not-a-uuid.jsonl"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, uuidB+".txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		// A directory whose name looks like a transcript must be skipped even
		// though it is the most recently created entry.
		if err := os.Mkdir(filepath.Join(dir, uuidC+Ext), 0o700); err != nil {
			t.Fatal(err)
		}
		got, err := Newest(dir)
		if err != nil {
			t.Fatalf("Newest: %v", err)
		}
		if got.Path != want {
			t.Fatalf("Path = %q, want %q (non-uuid/.txt/dir skipped)", got.Path, want)
		}
	})

	t.Run("empty dir", func(t *testing.T) {
		t.Parallel()
		got, err := Newest(t.TempDir())
		if err != nil || got.Found() {
			t.Fatalf("empty dir: got (%+v, %v), want (Result{}, nil)", got, err)
		}
	})

	t.Run("missing dir", func(t *testing.T) {
		t.Parallel()
		got, err := Newest(filepath.Join(t.TempDir(), "does-not-exist"))
		if err == nil {
			t.Fatal("missing dir: got nil error, want os.ReadDir error")
		}
		if got.Found() {
			t.Fatalf("missing dir: Result = %+v, want zero", got)
		}
	})
}

func TestGuardProbedPath(t *testing.T) {
	t.Parallel()

	t.Run("accept in-dir uuid jsonl", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		probed := writeJSONL(t, dir, uuidA, 1, time.Now())
		base, ok := GuardProbedPath(probed, CanonicalDir(dir))
		if !ok {
			t.Fatalf("GuardProbedPath(%q) rejected, want accept", probed)
		}
		if base != uuidA+Ext {
			t.Fatalf("base = %q, want %q", base, uuidA+Ext)
		}
	})

	// An in-dir symlink whose target resolves to an in-dir <uuid>.jsonl is
	// accepted, returning the RESOLVED base — both sides are canonicalised.
	t.Run("accept via in-dir symlink", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		target := writeJSONL(t, dir, uuidA, 1, time.Now())
		link := filepath.Join(dir, "current-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		base, ok := GuardProbedPath(link, CanonicalDir(dir))
		if !ok {
			t.Fatalf("GuardProbedPath(%q) rejected, want accept (symlink resolves in-dir)", link)
		}
		if base != uuidA+Ext {
			t.Fatalf("base = %q, want %q (resolved base)", base, uuidA+Ext)
		}
	})

	t.Run("reject path outside dir", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		outside := filepath.Join(resolvedTempDir(t), uuidA+Ext)
		if _, ok := GuardProbedPath(outside, CanonicalDir(dir)); ok {
			t.Fatalf("GuardProbedPath accepted %q, want reject (outside dir)", outside)
		}
	})

	t.Run("reject non-uuid stem in dir", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		if _, ok := GuardProbedPath(filepath.Join(dir, "not-a-uuid.jsonl"), CanonicalDir(dir)); ok {
			t.Fatal("GuardProbedPath accepted non-uuid stem, want reject")
		}
	})

	t.Run("reject valid uuid without jsonl suffix", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		if _, ok := GuardProbedPath(filepath.Join(dir, uuidA), CanonicalDir(dir)); ok {
			t.Fatal("GuardProbedPath accepted stem without .jsonl suffix, want reject")
		}
	})
}
