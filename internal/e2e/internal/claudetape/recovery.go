package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const RecoveryScenario = "daemon-session-recovery"
const RecoverySessionID = "77777777-7777-4777-8777-777777777777"
const RecoveryRoot = "TestInteractiveSessionErrorRecovery"
const RecoveryMetadataDirectory = ".pyry-recovery-recording"

func RecoveryRoles() []string { return []string{"retained", "dropped"} }

type RecoveryTranscript struct {
	SessionID string      `json:"session_id"`
	Snapshot  CLISnapshot `json:"snapshot"`
}
type RecoveryRole struct {
	Tape       *Tape              `json:"tape"`
	Transcript RecoveryTranscript `json:"transcript"`
}
type RecoverySuite struct {
	Version      int                     `json:"version"`
	Scenario     string                  `json:"scenario"`
	Producer     Producer                `json:"producer"`
	DaemonSHA256 string                  `json:"daemon_sha256"`
	Roles        map[string]RecoveryRole `json:"roles"`
}

func (t RecoveryTranscript) Validate() error {
	fail := errors.New("invalid recovered session transcript")
	if t.SessionID != RecoverySessionID || !t.Snapshot.Exists || t.Snapshot.Mode == 0 || t.Snapshot.Mode > 0777 || len(t.Snapshot.Data) == 0 || len(t.Snapshot.Data) > MaxBytes || !utf8.ValidString(t.Snapshot.Data) {
		return fail
	}
	raw, err := json.Marshal(t)
	if err != nil || len(raw) > MaxBytes {
		return fail
	}
	sessions := 0
	for _, line := range strings.Split(strings.TrimSuffix(t.Snapshot.Data, "\n"), "\n") {
		value, err := object([]byte(line))
		if err != nil {
			return fail
		}
		for _, key := range []string{"sessionId", "session_id"} {
			if id, exists := value[key]; exists {
				if id != t.SessionID {
					return fail
				}
				sessions++
			}
		}
		if safeCLIJSON([]byte(line)) != nil {
			return fail
		}
	}
	if sessions == 0 {
		return fail
	}
	return nil
}
func (s *RecoverySuite) Validate() error {
	if s.Scenario != RecoveryScenario {
		return errors.New("unknown recovery recording profile")
	}
	tapes := map[string]*Tape{}
	for name, role := range s.Roles {
		if role.Transcript.Validate() != nil {
			return errors.New("incomplete recovery witness")
		}
		tapes[name] = role.Tape
	}
	if err := (&Suite{Version: s.Version, Scenario: s.Scenario, Producer: s.Producer, DaemonSHA256: s.DaemonSHA256, Roles: tapes}).Validate(RecoveryRoles()); err != nil {
		return err
	}
	for _, role := range s.Roles {
		var cwd string
		if json.Unmarshal(role.Tape.Files["$cwd"], &cwd) != nil || cwd != "work" {
			return errors.New("recovery workdir changed")
		}
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxBytes {
		return errors.New("recovery suite exceeded limit")
	}
	return nil
}
func DecodeRecoverySuite(r io.Reader) (*RecoverySuite, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes {
		return nil, errors.New("recovery suite exceeded limit")
	}
	var s RecoverySuite
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(raw) || d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid recovery suite")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}
func (s *RecoverySuite) Save(path string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil || len(raw) > MaxBytes {
		return errors.New("recovery suite serialization failed")
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
		return errors.New("recovery suite close failed")
	}
	ok = true
	return nil
}
func SameRecoveryContract(a, b *RecoverySuite) error {
	if a.Validate() != nil || b.Validate() != nil || a.Scenario != b.Scenario {
		return errors.New("recovery suite inventory changed")
	}
	for _, role := range RecoveryRoles() {
		if SameContract(a.Roles[role].Tape, b.Roles[role].Tape) != nil {
			return errors.New("recovery outgoing contract changed")
		}
	}
	return nil
}
