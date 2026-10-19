package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

type RunnerRole struct{ SessionID, Workdir string }
type RunnerProfile struct {
	Root  string
	Roles map[string]RunnerRole
}

func RunnerProfiles() map[string]RunnerProfile {
	return map[string]RunnerProfile{
		"runner-applied-settings": {Root: "TestRealClaudeAppliedSettingsBeforeFirstTurn", Roles: map[string]RunnerRole{
			"inherited": {SessionID: "25050000-0000-4000-8000-000000000001", Workdir: "applied-settings-inherited"},
			"explicit":  {SessionID: "25050000-0000-4000-8000-000000000002", Workdir: "applied-settings-explicit"},
		}},
		"runner-compaction": {Root: "TestRealClaude_CompactingEdges", Roles: map[string]RunnerRole{
			"compact": {SessionID: "6b2f70c1-48ad-4f92-9c15-2e83d604af77", Workdir: "cedge-work"},
		}},
	}
}

// RunnerSuite owns the compiled original-test binary containing the production
// runner/parser, rather than identifying an unused daemon executable as proof.
type RunnerSuite struct {
	Version      int              `json:"version"`
	Scenario     string           `json:"scenario"`
	RunnerSHA256 string           `json:"runner_sha256"`
	Producer     Producer         `json:"producer"`
	Roles        map[string]*Tape `json:"roles"`
}

func (s *RunnerSuite) expected() ([]string, error) {
	p, ok := RunnerProfiles()[s.Scenario]
	if !ok {
		return nil, errors.New("unknown runner profile")
	}
	roles := make([]string, 0, len(p.Roles))
	for role := range p.Roles {
		roles = append(roles, role)
	}
	return roles, nil
}
func (s *RunnerSuite) Validate() error {
	roles, err := s.expected()
	if err != nil {
		return err
	}
	if s.RunnerSHA256 != s.Producer.BinarySHA256 {
		return errors.New("runner binary provenance changed")
	}
	if err := (&Suite{Version: s.Version, Scenario: s.Scenario, DaemonSHA256: s.RunnerSHA256, Producer: s.Producer, Roles: s.Roles}).Validate(roles); err != nil {
		return err
	}
	for role, tape := range s.Roles {
		if err := validateRunnerSavedInputs(s.Scenario, role, tape); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxBytes {
		return errors.New("runner suite exceeded limit")
	}
	return Safe(raw)
}
func DecodeRunnerSuite(r io.Reader) (*RunnerSuite, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes {
		return nil, errors.New("runner suite exceeded limit")
	}
	var s RunnerSuite
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(raw) || d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid runner suite")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}
func (s *RunnerSuite) Save(path string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil || len(raw) > MaxBytes {
		return errors.New("runner suite serialization failed")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return err
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("runner suite close failed")
	}
	ok = true
	return nil
}
func SameRunnerSuiteContract(a, b *RunnerSuite) error {
	if a.Validate() != nil || b.Validate() != nil || a.Scenario != b.Scenario {
		return errors.New("runner suite inventory changed")
	}
	for role := range RunnerProfiles()[a.Scenario].Roles {
		if SameContract(a.Roles[role], b.Roles[role]) != nil {
			return errors.New("runner outgoing request changed")
		}
	}
	return nil
}
