package debugbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readArchive untars-and-ungzips a bundle so assertions can read real member
// bodies. It fails the test on any structural error, so a caller that gets a
// map back knows the archive is a valid gzip->tar.
func readArchive(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	members := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read member %q: %v", hdr.Name, err)
		}
		members[hdr.Name] = body
	}
	return members
}

// writeCast writes a .cast file with the given body and mtime into dir.
func writeCast(t *testing.T, dir, name, body string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return path
}

// AC1: when recordings exist, exactly one — the newest by mtime — is included.
func TestAssemble_NewestRecordingIncluded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// An older recording under a different UTC-second stamp.
	writeCast(t, dir, "20260101T115900Z-old.cast", "OLD-RECORDING", base.Add(-time.Minute))
	// A same-second-stamp pair with differing mtimes. The lexically LAST name
	// carries the EARLIER mtime; the lexically FIRST name carries the newest
	// mtime. A lexical-stamp sort would pick "zzz"; mtime picks "aaa".
	writeCast(t, dir, "20260101T120001Z-zzz.cast", "SAME-SECOND-LOSER", base.Add(time.Second))
	const newestBody = "NEWEST-RECORDING-BYTES"
	writeCast(t, dir, "20260101T120001Z-aaa.cast", newestBody, base.Add(2*time.Second))

	archive, m, err := Assemble(dir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !m.RecordingPresent {
		t.Fatal("RecordingPresent = false, want true")
	}
	if m.RecordingName != "20260101T120001Z-aaa.cast" {
		t.Errorf("RecordingName = %q, want the newest-by-mtime basename", m.RecordingName)
	}
	if m.RecordingBytes != int64(len(newestBody)) {
		t.Errorf("RecordingBytes = %d, want %d", m.RecordingBytes, len(newestBody))
	}

	members := readArchive(t, archive)
	rec, ok := members[memberRecording]
	if !ok {
		t.Fatalf("archive missing %s member; has %v", memberRecording, keys(members))
	}
	if string(rec) != newestBody {
		t.Errorf("recording body = %q, want %q", rec, newestBody)
	}
}

// AC2: no .cast → recording marked absent, archive still valid.
func TestAssemble_NoRecordingMarkedAbsent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"empty dir", func(t *testing.T, dir string) {}},
		{"only non-cast files", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "foo.log"), []byte("y"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			tt.setup(t, dir)

			archive, m, err := Assemble(dir, []string{"log line"})
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if m.RecordingPresent {
				t.Error("RecordingPresent = true, want false")
			}
			if m.RecordingBytes != 0 {
				t.Errorf("RecordingBytes = %d, want 0", m.RecordingBytes)
			}
			if m.RecordingName != "" {
				t.Errorf("RecordingName = %q, want empty", m.RecordingName)
			}

			members := readArchive(t, archive)
			if _, ok := members[memberRecording]; ok {
				t.Error("archive has a recording member, want none")
			}
			if _, ok := members[memberManifest]; !ok {
				t.Error("archive missing manifest member")
			}
			if _, ok := members[memberLogs]; !ok {
				t.Error("archive missing logs member")
			}
		})
	}
}

// AC3: a log snapshot is always included, empty or not.
func TestAssemble_LogsAlwaysIncluded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		logs []string
	}{
		{"multi-line", []string{"first", "second", "third"}},
		{"single-line", []string{"only"}},
		{"empty", []string{}},
		{"nil", nil},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			archive, m, err := Assemble(dir, tt.logs)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			want := strings.Join(tt.logs, "\n")
			if m.LogLineCount != len(tt.logs) {
				t.Errorf("LogLineCount = %d, want %d", m.LogLineCount, len(tt.logs))
			}
			if m.LogBytes != len(want) {
				t.Errorf("LogBytes = %d, want %d", m.LogBytes, len(want))
			}
			members := readArchive(t, archive)
			body, ok := members[memberLogs]
			if !ok {
				t.Fatalf("archive missing %s member", memberLogs)
			}
			if string(body) != want {
				t.Errorf("logs body = %q, want %q", body, want)
			}
		})
	}
}

// AC4: one in-memory archive carrying both content sources and a manifest whose
// decoded fields match the returned Manifest.
func TestAssemble_SingleArchiveWithManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeCast(t, dir, "20260101T120000Z-sid.cast", "REC", time.Now())

	archive, m, err := Assemble(dir, []string{"one", "two"})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if archive == nil {
		t.Fatal("archive is nil")
	}

	members := readArchive(t, archive)
	raw, ok := members[memberManifest]
	if !ok {
		t.Fatalf("archive missing %s member", memberManifest)
	}
	var decoded Manifest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("manifest.json not parseable: %v", err)
	}
	if decoded != m {
		t.Errorf("decoded manifest %+v != returned manifest %+v", decoded, m)
	}
	// Both content sources present inside the one archive.
	if _, ok := members[memberLogs]; !ok {
		t.Error("archive missing logs member")
	}
	if _, ok := members[memberRecording]; !ok {
		t.Error("archive missing recording member")
	}
}

// AC5: the manifest carries no content — seeded secret sentinels never appear
// in manifest.json (they belong only in the logs/recording member bodies).
func TestAssemble_ManifestCarriesNoContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const recSecret = "SECRET-PTY-BYTES-9f3a"
	const logSecret = "SECRET-LOG-VALUE-7b2c"
	writeCast(t, dir, "20260101T120000Z-sid.cast", "prefix "+recSecret+" suffix", time.Now())

	archive, _, err := Assemble(dir, []string{"info: " + logSecret})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	members := readArchive(t, archive)
	manifest := members[memberManifest]
	if bytes.Contains(manifest, []byte(recSecret)) {
		t.Error("recording secret leaked into manifest.json")
	}
	if bytes.Contains(manifest, []byte(logSecret)) {
		t.Error("log secret leaked into manifest.json")
	}
	// Sanity: the secrets DO live in their member bodies (the content is in the
	// bundle, just not the manifest).
	if !bytes.Contains(members[memberRecording], []byte(recSecret)) {
		t.Error("recording secret missing from recording member")
	}
	if !bytes.Contains(members[memberLogs], []byte(logSecret)) {
		t.Error("log secret missing from logs member")
	}
}

// The currently-running session's recording is the in-flight, still-open file.
// Reading it concurrently yields the flushed prefix without error.
func TestAssemble_InFlightRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const partial = "[2, \"o\", \"partial-header-and-a-frame\"]\n"
	path := filepath.Join(dir, "20260101T120000Z-live.cast")
	// Model tui-driver's open: O_EXCL create, write a prefix, keep it open.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open in-flight recording: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(partial); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	archive, m, err := Assemble(dir, nil)
	if err != nil {
		t.Fatalf("Assemble over in-flight recording: %v", err)
	}
	if !m.RecordingPresent {
		t.Fatal("RecordingPresent = false, want true")
	}
	members := readArchive(t, archive)
	if got := string(members[memberRecording]); got != partial {
		t.Errorf("recording body = %q, want the flushed prefix %q", got, partial)
	}
}

// A recording that exists but cannot be read is an honest error, never a false
// "absent" — a manifest must not claim no recording when one is present.
func TestAssemble_ReadFailureIsError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root ignores file mode; unreadable-file path not exercisable")
	}
	dir := t.TempDir()
	path := writeCast(t, dir, "20260101T120000Z-sid.cast", "unreadable", time.Now())
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod 0000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	archive, _, err := Assemble(dir, []string{"x"})
	if err == nil {
		t.Fatal("Assemble returned nil error for an unreadable recording, want a read failure")
	}
	if archive != nil {
		t.Error("Assemble returned a non-nil archive alongside the error")
	}
}

// A symlink swapped in as the newest match is rejected by O_NOFOLLOW: the
// target's bytes are never read into the bundle.
func TestAssemble_SymlinkRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target-secret")
	if err := os.WriteFile(target, []byte("SYMLINK-TARGET-SECRET"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "20260101T120000Z-sid.cast")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	archive, _, err := Assemble(dir, nil)
	if err == nil {
		t.Fatal("Assemble followed a symlink recording, want a rejected-open error")
	}
	if archive != nil {
		t.Error("Assemble returned a non-nil archive for a rejected symlink")
	}
}

func TestDefaultRecordingsDir(t *testing.T) {
	t.Parallel()
	got, err := DefaultRecordingsDir()
	if err != nil {
		t.Fatalf("DefaultRecordingsDir: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join(".local", "share", "pyry-recordings")) {
		t.Errorf("DefaultRecordingsDir = %q, want a ~/.local/share/pyry-recordings suffix", got)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
