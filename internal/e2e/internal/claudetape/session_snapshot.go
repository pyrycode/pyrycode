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
				bound := v
				if depth == 1 && (key == "sessionId" || key == "session_id") {
					if v != from {
						return fail
					}
					bound = to
					identities++
				} else {
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
			result.Write(line[offset:r.start])
			result.Write(r.raw)
			offset = r.end
		}
		result.Write(line[offset:])
	}
	if identities == 0 {
		return nil, fail
	}
	return result.Bytes(), nil
}

// ReadSessionSnapshot accepts only a stable, private regular native file below
// the controlled HOME. An append, replacement or symlink during the read fails.
func ReadSessionSnapshot(path, session, home string) (SessionSnapshot, error) {
	fail := errors.New("unstable session snapshot")
	rel, e := filepath.Rel(home, path)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || snapshotAncestors(home, path) != nil {
		return SessionSnapshot{}, fail
	}
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() > MaxBytes {
		return SessionSnapshot{}, fail
	}
	f, e := os.Open(path)
	if e != nil {
		return SessionSnapshot{}, fail
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(before, opened) {
		return SessionSnapshot{}, fail
	}
	raw, e := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if e != nil || len(raw) > MaxBytes {
		return SessionSnapshot{}, fail
	}
	after, e := f.Stat()
	now, ne := os.Lstat(path)
	if e != nil || ne != nil || !os.SameFile(before, after) || !os.SameFile(before, now) || after.Size() != before.Size() || now.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !now.ModTime().Equal(before.ModTime()) {
		return SessionSnapshot{}, fail
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

// snapshotAncestors refuses directory symlinks inside the owned HOME. The HOME
// itself is already supplied by the original test's authenticated-home seam.
func snapshotAncestors(home, path string) error {
	rel, err := filepath.Rel(home, filepath.Dir(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("foreign snapshot parent")
	}
	current := home
	components := []string{}
	if rel != "." {
		components = strings.Split(rel, string(filepath.Separator))
	}
	for _, part := range append([]string{""}, components...) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() {
			return errors.New("snapshot parent is not an owned directory")
		}
	}
	return nil
}

// InstallSessionSnapshot creates a missing native session file. It cannot
// replace an existing transcript, and the caller must create the exact projects
// directory first. A replay hook calls this only after matching the causal input.
func InstallSessionSnapshot(s SessionSnapshot, path, session, home string) error {
	rel, err := filepath.Rel(filepath.Join(home, ".claude", "projects"), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Base(path) != session+".jsonl" || snapshotAncestors(home, path) != nil {
		return errors.New("invalid native transcript destination")
	}
	raw, err := s.Bytes(session, home)
	if err != nil {
		return err
	}
	return publishClosedFile(filepath.Dir(path), filepath.Base(path), raw)
}
