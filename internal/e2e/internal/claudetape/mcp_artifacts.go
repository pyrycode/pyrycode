package claudetape

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

func ValidateMCPRecordingProof(proof MCPRecordingProof, job LifecycleJob, tape *Tape, source *MCPExchange, revision, sourceSHA, daemonSHA string, broker int) error {
	config := job.Config
	if proof.Version != 1 || proof.Mode != config.Mode || proof.Scenario != config.Scenario || proof.Revision != revision || proof.SourceBinarySHA256 != sourceSHA || proof.DaemonSHA256 != daemonSHA || proof.OwnerPID != config.OwnerPID || proof.BrokerPID != broker || broker <= 1 || broker == config.OwnerPID || proof.Binding != (LifecycleRoleBinding{Role: job.Role, SessionID: job.SessionID, ConversationID: job.ConversationID, WorkDir: job.WorkDir}) {
		return mcpError()
	}
	start, e := time.Parse(time.RFC3339Nano, proof.StartedAt)
	if e != nil {
		return e
	}
	end, e := time.Parse(time.RFC3339Nano, proof.SealedAt)
	if e != nil || start.After(end) || end.Sub(start) > 20*time.Minute {
		return mcpError()
	}
	tapeSHA, e := mcpHash(tape)
	if e != nil || tapeSHA != proof.SourceTapeSHA256 {
		return mcpError()
	}
	sourceSHA256, e := mcpHash(source)
	if e != nil || sourceSHA256 != proof.SourceExchangeSHA256 {
		return mcpError()
	}
	facts, e := ValidateMCPSendFile(source, tape)
	if e != nil {
		return e
	}
	if proof.Mode == "capture" {
		if !reflect.DeepEqual(source, &proof.Actual) {
			return mcpError()
		}
	} else if proof.Mode != "replay" {
		return mcpError()
	}
	if len(proof.Actual.Events) != len(source.Events) {
		return mcpError()
	}
	index := facts.FileResponseSequence - 1
	if index < 0 || index >= len(proof.Actual.Events) {
		return mcpError()
	}
	response, e := e2emcp.ParseRPC([]byte(proof.Actual.Events[index].Raw))
	if e != nil {
		return e
	}
	id, e := mcpAttachmentID(response.Result)
	if e != nil {
		return e
	}
	actualView, e := mcpCurrentValidationTape(tape, facts, id, start)
	if e != nil {
		return e
	}
	actualFacts, e := ValidateMCPSendFile(&proof.Actual, actualView)
	if e != nil {
		return e
	}
	if ValidateMCPSendFileStderr(proof.Actual.Stderr, actualFacts, start.Add(-time.Second), end.Add(time.Second)) != nil {
		return mcpError()
	}
	for i, event := range proof.Actual.Events {
		expected := source.Events[i]
		if i == index {
			raw, e := e2emcp.BindJSONString([]byte(event.Raw), []string{"result", "content", "0", "text"}, mcpAttachmentText(id), mcpAttachmentText(facts.AttachmentID))
			if e != nil {
				return e
			}
			event.Raw = string(raw)
		}
		if event != expected {
			return mcpError()
		}
	}
	if ValidateMCPSendFileEffect(config.Home, job.ConversationID, id) != nil {
		return mcpError()
	}
	return nil
}
func readMCPArtifactValues(values map[string][]byte, job LifecycleJob, tape *Tape, revision, sourceSHA, daemonSHA string, broker int, complete bool) error {
	var prefix MCPRecordingProof
	if decodeMCPArtifact(values[".mcp-prefix"], &prefix) != nil {
		return mcpError()
	}
	source := &prefix.Actual
	if job.Config.Mode == "replay" {
		source = new(MCPExchange)
		if decodeMCPArtifact(values[".mcp-input"], source) != nil {
			return mcpError()
		}
	}
	if ValidateMCPRecordingProof(prefix, job, tape, source, revision, sourceSHA, daemonSHA, broker) != nil {
		return mcpError()
	}
	if complete {
		var terminal MCPRecordingTerminal
		if decodeMCPArtifact(values[".mcp"], &terminal) != nil || !reflect.DeepEqual(terminal.Prefix, prefix) || validateMCPServerProofs(terminal) != nil {
			return mcpError()
		}
	}
	return nil
}
func decodeMCPArtifact(raw []byte, value any) error {
	if len(raw) == 0 || len(raw) > MaxBytes || !uniqueSuiteKeys(raw) || safeCLIJSON(raw) != nil {
		return mcpError()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return mcpError()
	}
	return nil
}
func ReadMCPScenarioExchange(home, scenario string) (map[string]*MCPExchange, error) {
	if !MCPScenario(scenario) {
		return nil, nil
	}
	prefixPath := filepath.Join(home, "recording-children", "bootstrap.mcp-prefix")
	var prefix MCPRecordingProof
	if ReadLifecycleControl(prefixPath, MaxBytes, &prefix) != nil {
		return nil, mcpError()
	}
	source := &prefix.Actual
	if prefix.Mode == "replay" {
		var e error
		source, e = ReadMCPExchange(filepath.Join(home, "recording-children", "bootstrap.mcp-input"))
		if e != nil {
			return nil, e
		}
	} else if prefix.Mode != "capture" {
		return nil, mcpError()
	}
	return map[string]*MCPExchange{"bootstrap": source}, nil
}
func ValidateMCPBrokerOwnership(home, scenario string, brokers map[string]int) error {
	if !MCPScenario(scenario) {
		return nil
	}
	if len(brokers) != 1 || brokers["bootstrap"] <= 1 {
		return mcpError()
	}
	var prefix MCPRecordingProof
	if ReadLifecycleControl(filepath.Join(home, "recording-children", "bootstrap.mcp-prefix"), MaxBytes, &prefix) != nil || prefix.BrokerPID != brokers["bootstrap"] {
		return mcpError()
	}
	return nil
}
