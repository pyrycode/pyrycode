package claudetape

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

func TestMCPReplayNativeResultWaitsForActualResponseOrCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		x, tape := mcpSendFileControl()
		facts, e := ValidateMCPSendFile(x, tape)
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		session := &MCPRecordingSession{ctx: ctx, cancel: cancel, config: LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "replay"}}, facts: facts, idReady: make(chan struct{}), outputIndexes: []int{2}}
		config := LifecycleBrokerConfig{}
		if session.Configure(&config) != nil {
			t.Fatal("configure")
		}
		type result struct {
			raw []byte
			err error
		}
		done := make(chan result, 1)
		go func() { raw, e := config.ReplayOutput(tape.Frames[2].Data); done <- result{raw, e} }()
		select {
		case <-done:
			t.Fatal("result escaped before actual attachment UUID")
		case <-time.After(10 * time.Millisecond):
		}
		if cancelled {
			cancel()
		} else {
			if session.setCurrentID("ffffffff-ffff-4fff-8fff-ffffffffffff") != nil {
				t.Fatal("current response binding")
			}
		}
		select {
		case actual := <-done:
			if (actual.err != nil) != cancelled {
				t.Fatal("actual response/cancellation boundary")
			}
			if !cancelled && (!bytes.Contains(actual.raw, []byte("ffffffff-ffff-4fff-8fff-ffffffffffff")) || bytes.Contains(actual.raw, []byte(facts.AttachmentID))) {
				t.Fatal("joined current result not forwarded")
			}
		case <-time.After(time.Second):
			t.Fatal("response/cancellation did not wake result")
		}
		cancel()
	}
}
func TestMCPPrivacyRejectsPrivateCapabilitiesInEscapedUnknownFields(t *testing.T) {
	token := strings.Repeat("a", 64)
	for _, raw := range [][]byte{
		[]byte(`{"unknown":"` + token + `"}`),
		[]byte(`{"unknown":"` + strings.Repeat(`\u0061`, 64) + `"}`),
		[]byte(`{"unknown":{"\u0050YRY_E2E_MCP_TOKEN":"hidden"}}`),
		[]byte("actual private socket /tmp/controlled-private-mcp.sock"),
		[]byte{0xff},
	} {
		if mcpPrivateSafe(raw, token, "/tmp/controlled-private-mcp.sock") == nil {
			t.Fatal("private/invalid bytes became persistable")
		}
	}
	for _, inner := range []string{
		`{"private":"` + strings.Repeat(`\u0061`, 64) + `"}`,
		`{"private":"/tmp/controlled-private-mcp\u002esock"}`,
		`{"private":"\u0050YRY_E2E_MCP_TOKEN"}`,
	} {
		outer, _ := json.Marshal(map[string]string{"unknown": inner})
		if mcpPrivateSafe(outer, token, "/tmp/controlled-private-mcp.sock") == nil {
			t.Fatal("nested JSON-valued private string accepted")
		}
	}
	if mcpPrivateSafe([]byte(`{"unknown":"complete literal <>& text"}`), token) != nil {
		t.Fatal("unrelated unknown field rejected")
	}
}

// Controlled private-proof structs exercise audit rejection only. They are not
// launched process observations and must never be counted as native evidence.
func TestMCPPrivateProofBindsWholeSourceAndCurrentEffect(t *testing.T) {
	for _, mutation := range []string{"valid", "owner", "broker", "source", "daemon", "tape-hash", "exchange-hash", "actual-response", "actual-counter", "actual-log", "missing-file"} {
		t.Run(mutation, func(t *testing.T) {
			x, tape := mcpSendFileControl()
			home := t.TempDir()
			id := "12345678-1234-4123-8123-123456789012"
			conv := "55555555-5555-4555-8555-555555555555"
			if mutation != "missing-file" {
				seedMCPUnitEffect(t, home, conv, id)
			}
			stamp := tape.Producer.CapturedAt
			proof := MCPRecordingProof{Version: 1, Mode: "capture", Scenario: "mcp-send-file-bootstrap", Revision: tape.Producer.Revision, SourceBinarySHA256: strings.Repeat("a", 64), DaemonSHA256: strings.Repeat("b", 64), OwnerPID: 12001, BrokerPID: 12002, Binding: LifecycleRoleBinding{"bootstrap", conv, "77777777-7777-4777-8777-777777777777", home + "/work"}, StartedAt: stamp, SealedAt: stamp, Actual: *x}
			proof.SourceTapeSHA256, _ = mcpHash(tape)
			proof.SourceExchangeSHA256, _ = mcpHash(x)
			raw, _ := json.Marshal(proof)
			var current MCPRecordingProof
			if json.Unmarshal(raw, &current) != nil {
				t.Fatal("unit copy")
			}
			job := LifecycleJob{Config: LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "capture", Scenario: proof.Scenario, Home: home}, OwnerPID: proof.OwnerPID}, Role: proof.Binding.Role, SessionID: proof.Binding.SessionID, ConversationID: conv, WorkDir: proof.Binding.WorkDir}
			switch mutation {
			case "owner":
				current.OwnerPID++
			case "broker":
				current.BrokerPID++
			case "source":
				current.SourceBinarySHA256 = strings.Repeat("c", 64)
			case "daemon":
				current.DaemonSHA256 = strings.Repeat("c", 64)
			case "tape-hash":
				current.SourceTapeSHA256 = strings.Repeat("c", 64)
			case "exchange-hash":
				current.SourceExchangeSHA256 = strings.Repeat("c", 64)
			case "actual-response":
				current.Actual.Events[9].Raw = strings.Replace(current.Actual.Events[9].Raw, "<>&", "foreign", 1)
			case "actual-counter":
				current.Actual.Events[9].NativeInputCount++
			case "actual-log":
				current.Actual.Stderr["mcp-files"] += "foreign\n"
			}
			e := ValidateMCPRecordingProof(current, job, tape, x, proof.Revision, proof.SourceBinarySHA256, proof.DaemonSHA256, proof.BrokerPID)
			if (e == nil) != (mutation == "valid") {
				t.Fatal("private proof audit mismatch", e)
			}
		})
	}
}
func TestMCPTerminalProofCannotInventMissingOrForeignProcessFacts(t *testing.T) {
	for _, mutation := range []string{"valid", "missing-server", "nonzero-exit", "missing-EOF", "foreign-owner", "duplicate-server-pid", "foreign-native-proxy", "wrong-frame-count", "stderr-changed"} {
		x, _ := mcpSendFileControl()
		terminal := MCPRecordingTerminal{Prefix: MCPRecordingProof{Mode: "capture", OwnerPID: 12001, BrokerPID: 12002, Actual: *x}}
		for i, kind := range []string{"mcp-files", "mcp-approve"} {
			terminal.Servers = append(terminal.Servers, e2emcp.ServerProof{Kind: kind, NativeOwnerPID: 12001, NativeProxyPID: 13000 + i, ServerPID: 14000 + i, InputEOF: true, OutputEOF: true, StderrEOF: true, ExitCode: 0, InputFrames: 3, OutputFrames: 2, Stderr: x.Stderr[kind]})
		}
		switch mutation {
		case "missing-server":
			terminal.Servers = terminal.Servers[:1]
		case "nonzero-exit":
			terminal.Servers[0].ExitCode = 9
		case "missing-EOF":
			terminal.Servers[0].InputEOF = false
		case "foreign-owner":
			terminal.Servers[0].NativeOwnerPID++
		case "duplicate-server-pid":
			terminal.Servers[1].ServerPID = terminal.Servers[0].ServerPID
		case "foreign-native-proxy":
			terminal.Servers[0].NativeProxyPID = 12002
		case "wrong-frame-count":
			terminal.Servers[0].InputFrames++
		case "stderr-changed":
			terminal.Servers[0].Stderr = "different\n"
		}
		if (validateMCPServerProofs(terminal) == nil) != (mutation == "valid") {
			t.Fatal("actual terminal fact boundary mismatch", mutation)
		}
	}
}
