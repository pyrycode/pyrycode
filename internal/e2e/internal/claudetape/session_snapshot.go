package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const snapshotSession = "${LIFECYCLE_SESSION}"
const snapshotHome = "${LIFECYCLE_HOME}"

// SessionSnapshot preserves native JSONL bytes and mode. Only top-level native
// session identity string tokens and declared HOME path string tokens may bind;
// spacing, key order, escaped Unicode, nested IDs and prose remain literal.
type SessionSnapshot struct {
	Version int    `json:"version"`
	Mode    uint32 `json:"mode"`
	Data    string `json:"data"`
}

type snapshotReplacement struct {
	start, end int
	raw        []byte
}

func bindSessionJSONL(raw []byte, session, home string, restore bool) ([]byte, error) {
	fail := errors.New("invalid typed session JSONL")
	if len(raw) == 0 || len(raw) > MaxBytes || !utf8.Valid(raw) || !poolUUID.MatchString(session) || !filepath.IsAbs(home) {
		return nil, fail
	}
	if !restore && (bytes.Contains(raw, []byte(snapshotSession)) || bytes.Contains(raw, []byte(snapshotHome))) {
		return nil, fail
	}
	from, to := session, snapshotSession
	pathFrom, pathTo := home, snapshotHome
	if restore {
		from, to = to, from
		pathFrom, pathTo = pathTo, pathFrom
	}
	var result bytes.Buffer
	appendBounded := func(raw []byte) error {
		if len(raw) > MaxBytes-result.Len() {
			return fail
		}
		_, err := result.Write(raw)
		return err
	}
	reserved := func(v string) bool { return strings.Contains(v, snapshotSession) || strings.Contains(v, snapshotHome) }
	identities := 0
	for _, line := range bytes.SplitAfter(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if !uniqueSuiteKeys(line) || safeCLIJSON(line) != nil {
			return nil, fail
		}
		d := json.NewDecoder(bytes.NewReader(line))
		d.UseNumber()
		var replacements []snapshotReplacement
		var walk func(int, string) error
		walk = func(depth int, key string) error {
			before := int(d.InputOffset())
			tok, e := d.Token()
			if e != nil {
				return fail
			}
			isIdentity := depth == 1 && (key == "sessionId" || key == "session_id")
			if isIdentity {
				if _, ok := tok.(string); !ok {
					return fail
				}
			}
			switch v := tok.(type) {
			case json.Delim:
				switch v {
				case '{':
					for d.More() {
						k, e := d.Token()
						if e != nil {
							return fail
						}
						name, ok := k.(string)
						if !ok {
							return fail
						}
						if !restore && reserved(name) {
							return fail
						}
						if e = walk(depth+1, name); e != nil {
							return e
						}
					}
					end, e := d.Token()
					if e != nil || end != json.Delim('}') {
						return fail
					}
				case '[':
					for d.More() {
						if e = walk(depth+1, ""); e != nil {
							return e
						}
					}
					end, e := d.Token()
					if e != nil || end != json.Delim(']') {
						return fail
					}
				default:
					return fail
				}
			case string:
				if !restore && reserved(v) {
					return fail
				}
				bound := v
				if depth == 1 && (key == "sessionId" || key == "session_id") {
					if v != from {
						return fail
					}
					bound = to
					identities++
				} else {
					growth := len(pathTo) - len(pathFrom)
					if growth > 0 && strings.Count(v, pathFrom) > (MaxBytes-len(v))/growth {
						return fail
					}
					bound = strings.ReplaceAll(v, pathFrom, pathTo)
				}
				if bound != v {
					after := int(d.InputOffset())
					quote := bytes.IndexByte(line[before:after], '"')
					if quote < 0 {
						return fail
					}
					var encoded []byte
					if isIdentity {
						encoded, _ = json.Marshal(bound)
					} else {
						// Replace only canonical known path bytes inside this
						// token, preserving every unrelated native escape.
						encoded = append([]byte(nil), line[before+quote:after]...)
						oldPath, _ := json.Marshal(pathFrom)
						newPath, _ := json.Marshal(pathTo)
						if !bytes.Contains(encoded, oldPath[1:len(oldPath)-1]) {
							return fail
						}
						encoded = bytes.ReplaceAll(encoded, oldPath[1:len(oldPath)-1], newPath[1:len(newPath)-1])
						var checked string
						if json.Unmarshal(encoded, &checked) != nil || checked != bound {
							return fail
						}
					}
					replacements = append(replacements, snapshotReplacement{before + quote, after, encoded})
				}
			default:
				if depth == 1 && (key == "sessionId" || key == "session_id") {
					return fail
				}
			}
			return nil
		}
		if trimmed := bytes.TrimSpace(line); len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, fail
		}
		if e := walk(0, ""); e != nil {
			return nil, e
		}
		if _, e := d.Token(); e != io.EOF {
			return nil, fail
		}
		offset := 0
		for _, r := range replacements {
			if appendBounded(line[offset:r.start]) != nil || appendBounded(r.raw) != nil {
				return nil, fail
			}
			offset = r.end
		}
		if appendBounded(line[offset:]) != nil {
			return nil, fail
		}
	}
	if identities == 0 {
		return nil, fail
	}
	return result.Bytes(), nil
}

// openSnapshotDirectory anchors every component below the authenticated HOME.
// O_NOFOLLOW on every openat prevents a checked directory from being replaced by
// a symlink between traversal and the actual leaf read or publication.
func openSnapshotDirectory(path, home string) (*os.File, string, error) {
	fail := errors.New("invalid anchored snapshot directory")
	if !filepath.IsAbs(home) || !filepath.IsAbs(path) {
		return nil, "", fail
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, "", fail
	}
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", fail
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if err != nil {
			return nil, "", fail
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "owned-snapshot-directory"), parts[len(parts)-1], nil
}
func snapshotDirectoryStillCurrent(dir *os.File, path, home string) bool {
	current, _, err := openSnapshotDirectory(path, home)
	if err != nil {
		return false
	}
	defer current.Close()
	before, e := dir.Stat()
	after, ae := current.Stat()
	return e == nil && ae == nil && os.SameFile(before, after)
}
func openSnapshotLeaf(dir *os.File, leaf string) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), leaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("invalid anchored native snapshot")
	}
	return os.NewFile(uintptr(fd), "owned-native-snapshot"), nil
}

func readStableSessionFile(path, home string, readContents func(io.Reader) ([]byte, error)) ([]byte, error) {
	fail := errors.New("unstable session snapshot")
	dir, leaf, e := openSnapshotDirectory(path, home)
	if e != nil {
		return nil, fail
	}
	defer dir.Close()
	f, e := openSnapshotLeaf(dir, leaf)
	if e != nil {
		return nil, fail
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() > MaxBytes {
		return nil, fail
	}
	raw, e := readContents(io.LimitReader(f, MaxBytes+1))
	if e != nil || len(raw) > MaxBytes {
		return nil, fail
	}
	after, e := f.Stat()
	current, ce := openSnapshotLeaf(dir, leaf)
	if ce != nil {
		return nil, fail
	}
	defer current.Close()
	now, ne := current.Stat()
	if e != nil || ne != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0600 || !now.Mode().IsRegular() || now.Mode().Perm() != 0600 || !os.SameFile(before, after) || !os.SameFile(before, now) || after.Size() != before.Size() || now.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !now.ModTime().Equal(before.ModTime()) || !snapshotDirectoryStillCurrent(dir, path, home) {
		return nil, fail
	}
	return raw, nil
}

// ReadSessionSnapshot accepts a stable private native file through anchored
// no-follow directory descriptors. Bytes and final mode/identity are rechecked.
func ReadSessionSnapshot(path, session, home string) (SessionSnapshot, error) {
	raw, e := readStableSessionFile(path, home, io.ReadAll)
	if e != nil {
		return SessionSnapshot{}, e
	}
	bound, e := bindSessionJSONL(raw, session, home, false)
	if e != nil {
		return SessionSnapshot{}, e
	}
	return SessionSnapshot{Version: 1, Mode: 0600, Data: string(bound)}, nil
}
func (s SessionSnapshot) Bytes(session, home string) ([]byte, error) {
	if s.Version != 1 || s.Mode != 0600 {
		return nil, errors.New("invalid session snapshot mode or version")
	}
	return bindSessionJSONL([]byte(s.Data), session, home, true)
}

// publishSnapshotAt never follows a replacement parent or pending-file symlink.
// The writer parameter permits deterministic actual-filesystem fault controls;
// callers use the ordinary bounded file Write.
func publishSnapshotAt(path, home string, raw []byte, write func(io.Writer, []byte) (int, error)) error {
	fail := errors.New("anchored snapshot publication failed")
	if len(raw) == 0 || len(raw) > MaxBytes {
		return fail
	}
	dir, leaf, err := openSnapshotDirectory(path, home)
	if err != nil {
		return fail
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || info.Mode().Perm() != 0700 {
		return fail
	}
	pending := leaf + ".pending"
	fd, err := unix.Openat(int(dir.Fd()), pending, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return fail
	}
	f := os.NewFile(uintptr(fd), "owned-pending-snapshot")
	if f.Chmod(0600) != nil {
		f.Close()
		return fail
	}
	n, we := write(f, raw)
	written, se := f.Stat()
	ce := f.Close()
	if we != nil || n != len(raw) || se != nil || ce != nil || !snapshotDirectoryStillCurrent(dir, path, home) {
		return fail
	}
	check, err := openSnapshotLeaf(dir, pending)
	if err != nil {
		return fail
	}
	before, be := check.Stat()
	check.Close()
	if be != nil || !os.SameFile(written, before) || before.Mode().Perm() != 0600 {
		return fail
	}
	if unix.Linkat(int(dir.Fd()), pending, int(dir.Fd()), leaf, 0) != nil {
		return fail
	}
	final, err := openSnapshotLeaf(dir, leaf)
	if err != nil {
		return fail
	}
	after, ae := final.Stat()
	final.Close()
	if ae != nil || !os.SameFile(written, after) || after.Mode().Perm() != 0600 || after.Size() != int64(len(raw)) || !snapshotDirectoryStillCurrent(dir, path, home) {
		return fail
	}
	if unix.Unlinkat(int(dir.Fd()), pending, 0) != nil {
		return fail
	}
	return nil
}

// InstallSessionSnapshot creates only a missing native file after the caller's
// matched-input barrier. It never replaces an existing native transcript.
func InstallSessionSnapshot(s SessionSnapshot, path, session, home string) error {
	rel, err := filepath.Rel(filepath.Join(home, ".claude", "projects"), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Base(path) != session+".jsonl" {
		return errors.New("invalid native transcript destination")
	}
	raw, err := s.Bytes(session, home)
	if err != nil {
		return err
	}
	return publishSnapshotAt(path, home, raw, func(w io.Writer, p []byte) (int, error) { return w.Write(p) })
}
