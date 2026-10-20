package claudetape

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

// MCPFailedObservation is an incomplete attempt diagnostic, never a fixture,
// terminal receipt, process status, native server proof or coverage authority.
// It retains safe observed native data and only complete successful MCP/log
// forwards. Unobserved bytes, EOF and exit codes are deliberately not invented.
type MCPFailedObservation struct {
	Kind                                   string
	Scenario, Revision, SourceBinarySHA256 string
	OwnerPID, BrokerPID, Frames            int
	NativeObservation                      *LifecycleTape
	MCPCompleteForwards                    []e2emcp.RPCEvent
	StderrSuccessfullyForwarded            map[string]string
}

func (s *MCPRecordingSession) RetainFailure(tape *LifecycleTape, frames int) error {
	if s.config.Mode != "capture" || tape == nil || tape.Tape == nil || frames < 0 || len(tape.Tape.Frames) > MaxFrames {
		return mcpError()
	}
	_ = s.Close(false)
	logs, e := s.stderr()
	if e != nil {
		return e
	}
	diagnostic := MCPFailedObservation{Kind: "failed-unpromotable-mcp-observation-v1", Scenario: s.config.Scenario, Revision: s.config.Producer.Revision, SourceBinarySHA256: s.source, OwnerPID: s.config.OwnerPID, BrokerPID: s.broker, Frames: frames, NativeObservation: tape, MCPCompleteForwards: s.coordinator.DiagnosticSnapshot(), StderrSuccessfullyForwarded: logs}
	raw, e := json.Marshal(diagnostic)
	if e != nil || s.privateSafe(raw) != nil {
		return mcpError()
	}
	return PublishMCPFailedObservation(s.prefix+".failed-native", diagnostic)
}
func PublishMCPFailedObservation(path string, x MCPFailedObservation) error {
	if x.Kind != "failed-unpromotable-mcp-observation-v1" || x.Scenario != "mcp-send-file-bootstrap" || len(x.Revision) != 40 || !physicalSHA256.MatchString(x.SourceBinarySHA256) || x.OwnerPID <= 1 || x.BrokerPID <= 1 || x.OwnerPID == x.BrokerPID || x.Frames < 0 || x.NativeObservation == nil || x.NativeObservation.Tape == nil || len(x.MCPCompleteForwards) > e2emcp.MaxRPCFrames || len(x.NativeObservation.Tape.Frames) > MaxFrames {
		return mcpError()
	}
	return publishMCP(path, x)
}

// MCPInvocationFailure diagnoses a rejected current launch. These are actual
// observed argv/cwd/PIDs only, never process authority or native SDK coverage.
type MCPInvocationFailure struct {
	Kind, Stage, Mode, Scenario, Revision, SourceBinarySHA256 string
	PID, ParentPID                                            int
	Args                                                      []string
	Cwd                                                       string
	HomeMatches                                               bool
}

func PublishMCPInvocationFailure(path string, x MCPInvocationFailure) error {
	stages := map[string]bool{"endpoint": true, "session-flags": true, "role-binding": true, "unmapped-session": true, "empty-session": true, "home": true, "cwd": true, "job-publish": true, "ready": true}
	if x.Kind != "failed-unpromotable-mcp-invocation-v1" || !stages[x.Stage] || (x.Mode != "capture" && x.Mode != "replay") || x.Scenario != "mcp-send-file" || len(x.Revision) != 40 || !physicalSHA256.MatchString(x.SourceBinarySHA256) || x.PID <= 1 || x.ParentPID <= 1 || x.PID == x.ParentPID || len(x.Args) == 0 || len(x.Args) > 128 {
		return mcpError()
	}
	raw, e := json.Marshal(x)
	if e != nil || len(raw) > 16384 || mcpPrivateSafe(raw) != nil {
		return mcpError()
	}
	return publishMCP(path, x)
}
