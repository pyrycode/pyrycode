package claudetape

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"regexp"
)

// Suite keeps independently supervised children in named, closed process roles.
// A role is selected from actual session ownership, never launch timing. Each
// child retains a complete strict tape; frames cannot migrate between roles.
// It is used only by test harnesses with explicit multi-child inventories.
type Suite struct {
	Version      int              `json:"version"`
	Scenario     string           `json:"scenario"`
	DaemonSHA256 string           `json:"daemon_sha256"`
	Producer     Producer         `json:"producer"`
	Roles        map[string]*Tape `json:"roles"`
}

var suiteRole = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func (s *Suite) Validate(expected []string) error {
	fail := errors.New("incomplete or changed process role inventory")
	if s.Version != 1 || s.Scenario == "" || len(expected) == 0 || len(expected) > 8 || len(s.Roles) != len(expected) {
		return fail
	}
	if raw, err := hex.DecodeString(s.DaemonSHA256); err != nil || len(raw) != 32 {
		return fail
	}
	seen := map[string]bool{}
	frames := 0
	for _, role := range expected {
		if !suiteRole.MatchString(role) || seen[role] {
			return fail
		}
		seen[role] = true
		t, ok := s.Roles[role]
		if !ok || t == nil || t.Scenario != s.Scenario+"-"+role || !reflect.DeepEqual(t.Producer, s.Producer) || t.Validate() != nil {
			return fail
		}
		frames += len(t.Frames)
	}
	if frames > MaxFrames {
		return fail
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxBytes {
		return fail
	}
	return Safe(raw)
}

func DecodeSuite(r io.Reader, expected []string) (*Suite, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes {
		return nil, errors.New("suite read failed or exceeded limit")
	}
	var s Suite
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(raw) || d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid process suite JSON")
	}
	if err = s.Validate(expected); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Suite) Save(path string, expected []string) error {
	if err := s.Validate(expected); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil || len(raw) > MaxBytes {
		return errors.New("suite serialization exceeded limit")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("suite candidate exists or cannot be created")
	}
	success := false
	defer func() {
		_ = f.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()
	if _, err = f.Write(append(raw, '\n')); err != nil {
		return errors.New("suite write failed")
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("suite close failed")
	}
	success = true
	return nil
}

func SameSuiteContract(a, b *Suite, expected []string) error {
	if a.Validate(expected) != nil || b.Validate(expected) != nil || a.Scenario != b.Scenario {
		return errors.New("process suite inventory changed")
	}
	for _, role := range expected {
		if err := SameContract(a.Roles[role], b.Roles[role]); err != nil {
			return errors.New("process role request contract changed")
		}
	}
	return nil
}

// Reject duplicate keys before decoding into maps; a repeated role must not
// silently replace its first child inventory. Nesting is bounded independently
// of the serialized-byte limit.
func uniqueSuiteKeys(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var visit func(int) bool
	visit = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !visit(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case json.Delim('['):
			for d.More() {
				if !visit(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return true
		}
	}
	if !visit(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
