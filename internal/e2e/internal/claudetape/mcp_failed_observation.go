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
