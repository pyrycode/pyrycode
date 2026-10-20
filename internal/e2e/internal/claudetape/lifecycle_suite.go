package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

const LifecycleBootstrapSession = "88888888-8888-4888-8888-888888888888"

func LifecycleProfiles() map[string]DaemonSuiteProfile {
	return map[string]DaemonSuiteProfile{
		"mcp-send-file":              {"TestInteractiveStreamSendFile", []string{"bootstrap"}},
		"stop-background-task":       {"TestRealClaudeStopBackgroundTaskCompletion", []string{"bootstrap"}},
		"composer-stop":              {"TestRealClaudeComposerStopPreservesBackgroundTask", []string{"bootstrap"}},
		"fallback-suggestion":        {"TestInteractiveStream_FallbackReplySuggestionSetThenClear", []string{"bootstrap"}},
		"conversation-active-switch": {"TestInteractivePerConversationLiveness_ActiveSwitch", []string{"bootstrap", "created-1", "created-2"}},
		"conversation-system-prompt": {"TestInteractiveConversationSystemPrompt_ReachesTheReply", []string{"bootstrap", "bare", "prompted"}},
		"conversation-lifecycle":     {"TestInteractiveConversationLifecycle", []string{"bootstrap", "created-1"}},
		"session-rotation":           {"TestInteractiveStreamNewSessionRotatesAndSpawnsFresh", []string{"bootstrap", "rotated"}},
		"session-resume":             {"TestInteractiveStreamResumeAfterEviction", []string{"bootstrap", "resumed"}},
		"clear-wrapup-skipped":       {"TestInteractiveStreamClearWrapUpSkipped", []string{"bootstrap", "bound", "rotated"}},
		"clear-reset":                {"TestInteractiveStreamClearRunsDaemonReset", []string{"bootstrap", "bound", "rotated"}},
	}
}

const LifecycleRotationSession = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
const LifecycleRotationConversation = "ffffffff-ffff-4fff-8fff-ffffffffffff"
const LifecycleResumeSession = "11770000-0000-4000-8000-000000000001"
const LifecycleResumeConversation = "11770000-0000-4000-8000-000000000002"
const LifecycleSkipBootstrap = "24860000-0000-4000-8000-000000000001"
const LifecycleSkipSession = "24860000-0000-4000-8000-000000000002"
const LifecycleSkipConversation = "24860000-0000-4000-8000-000000000003"

func LifecycleResetSeeds(scenario string) (bootstrap, initial, conversation string, ok bool) {
	switch scenario {
	case "clear-wrapup-skipped":
		return LifecycleSkipBootstrap, LifecycleSkipSession, LifecycleSkipConversation, true
	case "clear-reset":
		return "22222222-2222-4222-8222-222222222222", "00000000-2222-4222-8222-222222222222", "00000000-0000-4000-8000-000000000000", true
	}
	return "", "", "", false
}

func LifecycleBootstrapFor(scenario string) string {
	if bootstrap, _, _, ok := LifecycleResetSeeds(scenario); ok {
		return bootstrap
	}
	switch scenario {
	case "mcp-send-file":
		return "77777777-7777-4777-8777-777777777777"
	case "stop-background-task", "composer-stop":
		return "33333333-3333-4333-8333-333333333333"
	case "conversation-lifecycle", "conversation-system-prompt", "conversation-active-switch", "fallback-suggestion":
		return LifecycleBootstrapSession
	case "session-rotation":
		return LifecycleRotationSession
	case "session-resume":
		return LifecycleResumeSession
	}
	return ""
}
func LifecycleEnd(scenario, role string) string {
	switch scenario {
	case "mcp-send-file", "stop-background-task", "composer-stop":
		if role == "bootstrap" {
			return "seal"
		}
	case "fallback-suggestion":
		if role == "bootstrap" {
			return "seal"
		}
	case "conversation-active-switch":
		if role == "bootstrap" || role == "created-1" || role == "created-2" {
			return "seal"
		}
	case "conversation-system-prompt":
		if role == "bare" || role == "bootstrap" || role == "prompted" {
			return "seal"
		}
	case "session-resume":
		switch role {
		case "bootstrap":
			return "idle-evict"
		case "resumed":
			return "seal"
		}
	case "clear-wrapup-skipped", "clear-reset":
		switch role {
		case "bootstrap", "rotated":
			return "seal"
		case "bound":
			return "rotate-session"
		}
	case "conversation-lifecycle":
		switch role {
		case "bootstrap":
			return "seal"
		case "created-1":
			return "delete-conversation"
		}
	case "session-rotation":
		switch role {
		case "bootstrap":
			return "rotate-session"
		case "rotated":
			return "seal"
		}
	}
	return ""
}
func LifecycleSeals(end string) bool { return end == "seal" || end == "delete-conversation" }
func LifecycleAction(end string) string {
	switch end {
	case "delete-conversation":
		return "lifecycle_delete"
	case "rotate-session":
		return "lifecycle_rotate"
	case "idle-evict":
		return "lifecycle_idle_eviction"
	}
	return ""
}

type LifecycleRoleBinding struct{ Role, ConversationID, SessionID, WorkDir string }

func ReadLifecycleRole(home, scenario, role string) (LifecycleRoleBinding, error) {
	fail := errors.New("invalid current lifecycle role")
	if LifecycleEnd(scenario, role) == "" {
		return LifecycleRoleBinding{}, fail
	}
	bootstrap := LifecycleBootstrapFor(scenario)
	if role == "bootstrap" {
		conv := ""
		if PhysicalFamily(scenario) != "" {
			conv = "44444444-4444-4444-8444-444444444444"
		}
		if MCPScenario(scenario) {
			conv = "55555555-5555-4555-8555-555555555555"
		}
		if scenario == "fallback-suggestion" {
			conv = "28310000-0000-4000-8000-000000000002"
		}
		if scenario == "session-rotation" {
			conv = LifecycleRotationConversation
		}
		if scenario == "session-resume" {
			conv = LifecycleResumeConversation
		}
		return LifecycleRoleBinding{Role: role, SessionID: bootstrap, ConversationID: conv, WorkDir: filepath.Join(home, "work")}, nil
	}
	var b LifecycleRoleBinding
	if ReadLifecycleControl(filepath.Join(home, "recording-roles", role+".json"), 16384, &b) != nil || !validLifecycleRoleBinding(home, scenario, role, b) {
		return b, fail
	}

	return b, nil
}

func validLifecycleRoleBinding(home, scenario, role string, b LifecycleRoleBinding) bool {
	if scenario == "conversation-system-prompt" && !validPromptRole(b) {
		return false
	}
	if scenario == "session-resume" && (role != "resumed" || b.SessionID != LifecycleResumeSession || b.ConversationID != LifecycleResumeConversation) {
		return false
	}
	if _, initial, conversation, ok := LifecycleResetSeeds(scenario); ok && (b.ConversationID != conversation || role == "bound" && b.SessionID != initial || role == "rotated" && b.SessionID == initial) {
		return false
	}
	return LifecycleEnd(scenario, role) != "" && role != "bootstrap" && b.Role == role && poolUUID.MatchString(b.SessionID) && (b.SessionID != LifecycleBootstrapFor(scenario) || scenario == "session-resume" && role == "resumed" && b.SessionID == LifecycleResumeSession) && poolUUID.MatchString(b.ConversationID) && b.WorkDir == filepath.Join(home, "work") && (scenario != "session-rotation" || b.ConversationID == LifecycleRotationConversation)
}

type LifecycleSuite struct {
	MCP          map[string]*MCPExchange    `json:"mcp,omitempty"`
	Physical     *PhysicalScenarioContract  `json:"physical,omitempty"`
	Settlement   *ConversationSettlement    `json:"settlement,omitempty"`
	Version      int                        `json:"version"`
	Scenario     string                     `json:"scenario"`
	Producer     Producer                   `json:"producer"`
	DaemonSHA256 string                     `json:"daemon_sha256"`
	Roles        map[string]*LifecycleTape  `json:"roles"`
	Snapshots    map[string]SessionSnapshot `json:"snapshots,omitempty"`
	Workers      []LifecycleWorker          `json:"workers,omitempty"`
}

func (s *LifecycleSuite) Validate(expected []string) error {
	fail := errors.New("invalid lifecycle suite")
	if s == nil {
		return fail
	}
	if MCPScenario(s.Scenario) {
		if len(s.MCP) != 1 || s.Roles["bootstrap"] == nil {
			return fail
		}
		if _, e := ValidateMCPSendFile(s.MCP["bootstrap"], s.Roles["bootstrap"].Tape); e != nil {
			return e
		}
	} else if len(s.MCP) != 0 {
		return fail
	}
	if !reflect.DeepEqual(s.Settlement, RequiredConversationSettlement(s.Scenario)) {
		return fail
	}
	if PhysicalFamily(s.Scenario) != "" {
		if s.Roles["bootstrap"] == nil || ValidatePhysicalScenarioContract(s.Scenario, s.Physical, s.Roles["bootstrap"].Tape) != nil {
			return fail
		}
	} else if s.Physical != nil {
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
	snapshotRoles := LifecycleSnapshotRoles(s.Scenario)
	if len(snapshotRoles) > 0 {
		if len(s.Snapshots) != len(snapshotRoles) {
			return fail
		}
		for _, role := range snapshotRoles {
			snapshot, ok := s.Snapshots[role]
			if !ok {
				return fail
			}
			if _, err := snapshot.Bytes(LifecycleRotationSession, "/tmp/owned-snapshot-validation"); err != nil {
				return fail
			}
		}
	} else if len(s.Snapshots) != 0 {
		return fail
	}
	tapes := map[string]*Tape{}
	for role, t := range s.Roles {
		if t == nil || t.Validate() != nil || t.End != LifecycleEnd(s.Scenario, role) {
			return fail
		}
		if LifecycleAction(t.End) != "" {
			actions := 0
			for _, frame := range t.Tape.Frames {
				if frame.Direction == ClientCheckpointDirection {
					actions++
				}
			}
			if actions != 1 {
				return fail
			}
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
	if s.Scenario == "clear-reset" {
		if _, err := lifecycleHandoffContract(s.Roles["rotated"].Tape, s.Roles["bound"].Tape); err != nil {
			return err
		}
	}
	if PhysicalFamily(s.Scenario) != "" || s.Scenario == "fallback-suggestion" || s.Scenario == "conversation-system-prompt" || s.Scenario == "conversation-active-switch" {
		for _, role := range profile.Roles {
			if s.Roles[role].Timeline == nil {
				return fail
			}
		}
	}
	if s.Scenario == "session-resume" {
		if s.Roles["bootstrap"].Timeline == nil {
			return fail
		}
		if _, exists := s.Roles["bootstrap"].Tape.Files["$resume-input"]; exists {
			return fail
		}
		if err := validateLifecycleResumeInput(s.Roles["resumed"].Tape, s.Snapshots["bootstrap"]); err != nil {
			return err
		}
	}
	if s.Scenario == "fallback-suggestion" {
		// This root disables native suggestions to prove fallback publication. A
		// successful worker alone cannot attribute a competing native set revision.
		for _, frame := range s.Roles["bootstrap"].Tape.Frames {
			if frame.Direction != "stdout" {
				continue
			}
			var header struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(frame.Data, &header) != nil || header.Type == "prompt_suggestion" {
				return fail
			}
		}
		first := 0
		for _, worker := range s.Workers {
			if worker.SourceRole == "bootstrap" && worker.UserSequence == 1 && worker.Tape != nil && worker.Tape.Status == (FallbackWorkerStatus{ExitCode: 0}) {
				first++
			}
		}
		if first != 1 {
			return fail
		}
	}
	for _, worker := range s.Workers {
		if worker.Tape == nil || !reflect.DeepEqual(worker.Tape.Producer, s.Producer) {
			return fail
		}
	}
	if ConversationWorkerScenario(s.Scenario) {
		if _, err := conversationWorkerFacts(s); err != nil {
			return err
		}
	}
	if err := ValidateLifecycleWorkers(s.Scenario, s.Roles, s.Workers); err != nil {
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
	if err := sameLifecycleProviderContract(a, b, expected); err != nil {
		return err
	}
	return SameLifecycleWorkers(a, b)
}
func sameLifecycleProviderContract(a, b *LifecycleSuite, expected []string) error {
	fail := errors.New("lifecycle outgoing contract changed")
	if a.Validate(expected) != nil || b.Validate(expected) != nil || a.Scenario != b.Scenario || !reflect.DeepEqual(a.Physical, b.Physical) {
		return fail
	}
	if MCPScenario(a.Scenario) && SameMCPSendFileRequests(a.MCP["bootstrap"], b.MCP["bootstrap"], a.Roles["bootstrap"].Tape, b.Roles["bootstrap"].Tape) != nil {
		return fail
	}
	for _, role := range expected {
		left, right := a.Roles[role].Tape, b.Roles[role].Tape
		if a.Scenario == "session-resume" && role == "resumed" {
			var err error
			left, err = lifecycleResumeContract(left, a.Snapshots["bootstrap"])
			if err != nil {
				return err
			}
			right, err = lifecycleResumeContract(right, b.Snapshots["bootstrap"])
			if err != nil {
				return err
			}
		}
		if a.Scenario == "clear-reset" && role == "rotated" {
			var err error
			left, err = lifecycleHandoffContract(left, a.Roles["bound"].Tape)
			if err != nil {
				return err
			}
			right, err = lifecycleHandoffContract(right, b.Roles["bound"].Tape)
			if err != nil {
				return err
			}
		}
		if PhysicalFamily(a.Scenario) != "" {
			var err error
			left, err = physicalOutgoingContract(a.Scenario, left)
			if err != nil {
				return err
			}
			right, err = physicalOutgoingContract(b.Scenario, right)
			if err != nil {
				return err
			}
		}
		if a.Roles[role].End != b.Roles[role].End || SameContract(left, right) != nil {
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
	Timing                      *LifecycleTimingError `json:",omitempty"`
	Success                     bool
	Frames, OwnerPID, BrokerPID int
	Error                       string `json:",omitempty"`
}

type LifecycleStopObservation struct {
	PID  int
	Kind string
}
type LifecycleDelivery struct {
	Version, OwnerPID, BrokerPID, Sequence int
	Kind                                   string
}
