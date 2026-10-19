package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const LifecycleBootstrapSession = "88888888-8888-4888-8888-888888888888"

func LifecycleProfiles() map[string]DaemonSuiteProfile {
	return map[string]DaemonSuiteProfile{
		"conversation-lifecycle": {"TestInteractiveConversationLifecycle", []string{"bootstrap", "created-1"}},
	}
}
func LifecycleEnd(scenario, role string) string {
	if scenario != "conversation-lifecycle" {
		return ""
	}
	switch role {
	case "bootstrap":
		return "seal"
	case "created-1":
		return "delete-conversation"
	}
	return ""
}

type LifecycleSuite struct {
	Version      int                       `json:"version"`
	Scenario     string                    `json:"scenario"`
	Producer     Producer                  `json:"producer"`
	DaemonSHA256 string                    `json:"daemon_sha256"`
	Roles        map[string]*LifecycleTape `json:"roles"`
}

func (s *LifecycleSuite) Validate(expected []string) error {
	fail := errors.New("invalid lifecycle suite")
	if s == nil {
		return fail
	}
	profile, ok := LifecycleProfiles()[s.Scenario]
	if !ok || len(expected) != len(profile.Roles) {
		return fail
	}
	for i, role := range expected {
		if role != profile.Roles[i] {
			return fail
		}
	}
	tapes := map[string]*Tape{}
	for role, t := range s.Roles {
		if t == nil || t.Validate() != nil || t.End != LifecycleEnd(s.Scenario, role) {
			return fail
		}
		if t.End == "delete-conversation" && t.Tape.Frames[len(t.Tape.Frames)-1].Direction != ClientCheckpointDirection {
			return fail
		}

		var cwd string
		if json.Unmarshal(t.Tape.Files["$cwd"], &cwd) != nil || cwd != "work" || len(t.Tape.Files["--settings"]) == 0 || len(t.Tape.Files["--append-system-prompt-file"]) == 0 {
			return fail
		}
		tapes[role] = t.Tape
	}
	if err := (&Suite{Version: s.Version, Scenario: s.Scenario, Producer: s.Producer, DaemonSHA256: s.DaemonSHA256, Roles: tapes}).Validate(expected); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxBytes || safeCLIJSON(raw) != nil {
		return fail
	}
	return nil
}
func DecodeLifecycleSuite(r io.Reader, expected []string) (*LifecycleSuite, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes {
		return nil, errors.New("lifecycle suite exceeded limit")
	}
	var s LifecycleSuite
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(raw) || d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid lifecycle suite")
	}
	if err := s.Validate(expected); err != nil {
		return nil, err
	}
	return &s, nil
}
func (s *LifecycleSuite) Save(path string, expected []string) error {
	if err := s.Validate(expected); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil || len(raw) > MaxBytes {
		return errors.New("lifecycle suite serialization failed")
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
	if _, err = f.Write(append(raw, '\n')); err != nil {
		return err
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("lifecycle suite close failed")
	}
	ok = true
	return nil
}
func SameLifecycleSuiteContract(a, b *LifecycleSuite, expected []string) error {
	fail := errors.New("lifecycle outgoing contract changed")
	if a.Validate(expected) != nil || b.Validate(expected) != nil || a.Scenario != b.Scenario {
		return fail
	}
	for _, role := range expected {
		if a.Roles[role].End != b.Roles[role].End || SameContract(a.Roles[role].Tape, b.Roles[role].Tape) != nil {
			return fail
		}
	}
	return nil
}

// LifecycleJob is private per-launch evidence, never serialized into fixtures.
// The PID is the exec-preserved provider/stand-in PID; the broker has its own
// separately claimed PID. Original action witnesses refer only to this launch.
type LifecycleJob struct {
	Config                                   LifecycleBrokerConfig
	Args                                     []string
	Role, ConversationID, SessionID, WorkDir string
	Daemon, Socket, SourceBinarySHA256       string
}
type LifecycleReceipt struct {
	Success                     bool
	Frames, OwnerPID, BrokerPID int
	Error                       string `json:",omitempty"`
}

type LifecycleStopObservation struct {
	PID  int
	Kind string
}
