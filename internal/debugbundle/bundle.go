// Package debugbundle assembles a session's debug evidence — the most-recent
// terminal recording (when session capture was on) plus the recent daemon log
// ring — into a single in-memory tar+gzip archive with a content-free manifest.
//
// It is a pure leaf: it reads two already-existing content sources (a
// recordings directory and a caller-supplied log-line snapshot) and returns
// the archive bytes. It never writes to disk, never imports the control plane,
// and emits no logs — so no recording byte or log-line value can leak through
// a log emission from this path. Serving the bytes to a paired client is a
// sibling concern (#812 transport, #813 request verb); this package ships
// unwired.
package debugbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Fixed archive member names. These are hard-coded constants, never derived
// from an on-disk filename: a crafted name in the recordings directory can
// never influence the tar structure. The real recording basename travels only
// as a JSON value in Manifest.RecordingName, never as a path.
const (
	memberManifest  = "manifest.json"
	memberLogs      = "logs.txt"
	memberRecording = "recording.cast"
)

// Manifest is the content-free description of a bundle. Every field is
// metadata (name, size, count) — never recording bytes or log-line values —
// so the manifest can be inspected without exposing the packaged secrets.
type Manifest struct {
	RecordingPresent bool `json:"recording_present"`
	// RecordingName is the on-disk basename of the included recording (UTC
	// stamp + session id + optional -ok/-err). A data string, never a path.
	RecordingName string `json:"recording_name,omitempty"`
	// RecordingBytes is the size of the included .cast, 0 when absent.
	RecordingBytes int64 `json:"recording_bytes"`
	LogLineCount   int   `json:"log_line_count"`
	// LogBytes is the byte length of the joined log text.
	LogBytes int `json:"log_bytes"`
}

// DefaultRecordingsDir resolves the compile-time-fixed recordings location
// under $HOME (~/.local/share/pyry-recordings), mirroring #802's recorder.
// The ticket directs duplicating the join rather than importing the recorder's
// unexported package-main helper. Callers feed the result to Assemble; Assemble
// itself takes an explicit dir so tests can drive it with a temp directory.
func DefaultRecordingsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("debugbundle: resolve home: %w", err)
	}
	return filepath.Join(home, ".local", "share", "pyry-recordings"), nil
}

// Assemble reads the newest .cast in recordingsDir and the caller-supplied log
// snapshot, streams them plus a JSON manifest into a gzip-wrapped tar archive
// held entirely in memory, and returns the archive bytes alongside the
// content-free Manifest. When no recording exists the archive contains only
// the manifest and logs, and the manifest marks the recording absent.
//
// Nothing in this path opens a file for writing: the only disk touch is
// read-only (glob, stat, and a read-only open of the chosen .cast). A read
// failure on a recording that was selected is returned as a wrapped error
// rather than silently reported as absent — "absent" means no recording
// exists, and a manifest must never lie about that.
func Assemble(recordingsDir string, logs []string) (archive []byte, m Manifest, err error) {
	name, size, mtime, present, err := newestRecording(recordingsDir)
	if err != nil {
		return nil, Manifest{}, err
	}

	logText := strings.Join(logs, "\n")
	m = Manifest{
		RecordingPresent: present,
		LogLineCount:     len(logs),
		LogBytes:         len(logText),
	}
	if present {
		m.RecordingName = name
		m.RecordingBytes = size
	}

	manifestJSON, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("debugbundle: marshal manifest: %w", err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if err := writeBytesMember(tw, memberManifest, manifestJSON); err != nil {
		return nil, Manifest{}, err
	}
	if err := writeBytesMember(tw, memberLogs, []byte(logText)); err != nil {
		return nil, Manifest{}, err
	}
	if present {
		if err := writeRecordingMember(tw, filepath.Join(recordingsDir, name), size, mtime); err != nil {
			return nil, Manifest{}, err
		}
	}

	// Close tar then gzip explicitly (not deferred) so their trailers are
	// flushed into buf before we read its bytes.
	if err := tw.Close(); err != nil {
		return nil, Manifest{}, fmt.Errorf("debugbundle: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, Manifest{}, fmt.Errorf("debugbundle: close gzip: %w", err)
	}
	return buf.Bytes(), m, nil
}

// newestRecording selects the most-recent .cast in dir by modification time.
// Newest-by-mtime is the robust selector: two sessions started in the same UTC
// second share a stamp prefix, so a lexical-stamp sort would need a tiebreak;
// mtime does not. Entries whose stat fails are skipped (a file finalized and
// renamed between glob and stat is a benign race, not a bundle failure). Zero
// matches — or all skipped — yields present == false with no error.
func newestRecording(dir string) (name string, size int64, mtime time.Time, present bool, err error) {
	// filepath.Glob's "*" never crosses a path separator, so this matches only
	// the directory's top level and cannot recurse or escape dir.
	matches, err := filepath.Glob(filepath.Join(dir, "*.cast"))
	if err != nil {
		return "", 0, time.Time{}, false, fmt.Errorf("debugbundle: glob recordings: %w", err)
	}
	for _, match := range matches {
		info, serr := os.Stat(match)
		if serr != nil {
			continue
		}
		if !present || info.ModTime().After(mtime) {
			name = filepath.Base(match)
			size = info.Size()
			mtime = info.ModTime()
			present = true
		}
	}
	return name, size, mtime, present, nil
}

// writeBytesMember writes an in-memory member (manifest.json, logs.txt). Its
// tar header carries a zero-value ModTime so Assemble reads no clock and is
// fully deterministic in tests; the bundle's timing lives in RecordingName and
// the recording member's mtime. Members use mode 0600, mirroring the on-disk
// recording posture.
func writeBytesMember(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Mode:     0o600,
		Size:     int64(len(body)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("debugbundle: write %s header: %w", name, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("debugbundle: write %s: %w", name, err)
	}
	return nil
}

// writeRecordingMember streams the chosen .cast into the archive without ever
// holding it whole in a second []byte.
//
// The read open uses syscall.O_NOFOLLOW: the final path component is opened
// only if it is a regular file, so a same-user symlink swapped into the
// recordings dir between selection and read (the stat->open TOCTOU) fails the
// open rather than exfiltrating the symlink target's bytes into the bundle.
// tui-driver creates real files with O_EXCL and never symlinks, so a
// legitimate recording is never rejected. O_NOFOLLOW is present on Linux and
// macOS.
//
// The copy is bounded to exactly size — the byte count the tar header declares
// and stat reported at selection. The spec sketches a plain io.Copy, but a
// plain copy over the in-flight (unsuffixed) recording is unsafe: tui-driver
// may append PTY bytes between the selection stat and this read, and io.Copy
// would then write past the declared header size and fail the tar writer.
// io.CopyN takes the flushed prefix and writes exactly size, preserving the
// spec's header-size-from-stat invariant. Reading the in-flight file is
// otherwise safe: O_EXCL constrains only the create, and a read-only open sees
// a valid asciinema-v2 prefix (header first).
func writeRecordingMember(tw *tar.Writer, path string, size int64, mtime time.Time) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("debugbundle: open recording: %w", err)
	}
	defer f.Close()

	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     memberRecording,
		Mode:     0o600,
		Size:     size,
		ModTime:  mtime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("debugbundle: write recording header: %w", err)
	}
	if _, err := io.CopyN(tw, f, size); err != nil {
		return fmt.Errorf("debugbundle: copy recording: %w", err)
	}
	return nil
}
