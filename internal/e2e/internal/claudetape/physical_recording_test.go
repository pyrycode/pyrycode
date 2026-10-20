package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func physicalRecordingControl(t *testing.T) (*PhysicalScenarioContract, *Tape) {
	t.Helper()
	spec, _ := physicalSpec("stop-hold")
	d := PhysicalOperationDeclaration{Version: 1, UserSequence: 1, Owner: LifecycleRoleBinding{Role: "bootstrap", SessionID: "33333333-3333-4333-8333-333333333333", ConversationID: "44444444-4444-4444-8444-444444444444", WorkDir: physicalHome + "/work"}, Spec: spec, UserText: "Run exactly " + physicalHome + "/work/stop-hold in the background."}
	contract := &PhysicalScenarioContract{Kind: "original-rig-physical-v1", Declarations: []PhysicalOperationDeclaration{d}}
	tape := &Tape{Scenario: "stop-background-task-bootstrap"}
	add := func(direction string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		tape.Frames = append(tape.Frames, Frame{Direction: direction, Data: raw})
	}
	add("stdin", map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": d.UserText}}}})
	add("stdout", physicalProtocolTool("tool", physicalQuote(physicalHome+"/work/stop-hold"), true))
	add("stdout", physicalProtocolRoster("task"))
	add("stdout", physicalProtocolStart("tool", "task"))
	add("stdin", map[string]any{"type": "control_request", "request_id": "stop", "request": map[string]string{"subtype": "stop_task", "task_id": "task"}})
	add("stdout", physicalProtocolTerminal("task", "stopped"))
	return contract, tape
}
func physicalRecordingProofControl(t *testing.T) (PhysicalRecordingProof, *PhysicalScenarioContract, *Tape, string) {
	t.Helper()
	contract, tape := physicalRecordingControl(t)
	facts, err := physicalContractFacts("stop-background-task", contract, tape)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "current-home")
	if physicalReplaceHome(&facts, physicalHome, home) != nil {
		t.Fatal("restore actual current proof")
	}
	for i := range facts {
		if facts[i].Declaration != nil {
			facts[i].Declaration.Spec = contract.Declarations[facts[i].Declaration.UserSequence-1].Spec
		}
		if facts[i].Input != nil {
			facts[i].InputSHA256 = physicalProtocolDigest(facts[i].Input)
		}
	}
	raw, _ := json.Marshal(tape)
	proof := PhysicalRecordingProof{Version: 1, Mode: "capture", Revision: strings.Repeat("a", 40), SourceBinarySHA256: strings.Repeat("b", 64), TapeSHA256: physicalProtocolDigest(raw), OwnerPID: 1001, BrokerPID: 1002, Facts: facts}
	return proof, contract, tape, home
}
func TestPhysicalRecordingProofBindsEveryNativeFactAndCurrentOwner(t *testing.T) {
	for _, mutation := range []string{"none", "revision", "source", "owner", "broker", "mode", "tape", "tool", "task", "missing", "extra", "input-digest", "native-status", "declaration", "spec"} {
		t.Run(mutation, func(t *testing.T) {
			proof, contract, tape, home := physicalRecordingProofControl(t)
			revision, source := proof.Revision, proof.SourceBinarySHA256
			switch mutation {
			case "revision":
				proof.Revision = strings.Repeat("c", 40)
			case "source":
				proof.SourceBinarySHA256 = strings.Repeat("c", 64)
			case "owner":
				proof.OwnerPID++
			case "broker":
				proof.BrokerPID++
			case "mode":
				proof.Mode = "replay"
			case "tape":
				proof.TapeSHA256 = strings.Repeat("c", 64)
			case "tool":
				proof.Facts[1].ToolID = "foreign"
			case "task":
				proof.Facts[2].TaskID = "foreign"
			case "missing":
				proof.Facts = proof.Facts[:len(proof.Facts)-1]
			case "extra":
				proof.Facts = append(proof.Facts, proof.Facts[len(proof.Facts)-1])
			case "input-digest":
				proof.Facts[0].InputSHA256 = strings.Repeat("c", 64)
			case "native-status":
				proof.Facts[3].Status = &FallbackWorkerStatus{ExitCode: -1, Signal: 9}
			case "declaration":
				proof.Facts[0].Declaration.UserText = "foreign"
			case "spec":
				proof.Facts[1].Declaration.Spec.Kind = "composer-background"
			}
			err := ValidatePhysicalRecordingProof(proof, "stop-background-task", home, revision, source, "capture", 1001, 1002, contract, tape)
			if (err == nil) != (mutation == "none") {
				t.Fatalf("physical proof mutation %s: %v", mutation, err)
			}
		})
	}
}
func TestPhysicalScenarioContractRejectsMissingOriginalSourceAndTerminal(t *testing.T) {
	for _, mutation := range []string{"none", "user", "owner", "sequence", "spec", "no-terminal", "premature-terminal", "extra-tool", "extra-user", "unknown-direction"} {
		t.Run(mutation, func(t *testing.T) {
			contract, tape := physicalRecordingControl(t)
			switch mutation {
			case "user":
				contract.Declarations[0].UserText = "foreign"
			case "owner":
				contract.Declarations[0].Owner.SessionID = "55555555-5555-4555-8555-555555555555"
			case "sequence":
				contract.Declarations[0].UserSequence = 2
			case "spec":
				contract.Declarations[0].Spec.Script += "\n"
			case "no-terminal":
				tape.Frames = tape.Frames[:len(tape.Frames)-1]
			case "premature-terminal":
				tape.Frames[4], tape.Frames[5] = tape.Frames[5], tape.Frames[4]
			case "extra-tool":
				tape.Frames = append(tape.Frames, tape.Frames[1])
			case "extra-user":
				tape.Frames = append(tape.Frames, tape.Frames[0])
			case "unknown-direction":
				tape.Frames[1].Direction = "unknown"
			}
			err := ValidatePhysicalScenarioContract("stop-background-task", contract, tape)
			if (err == nil) != (mutation == "none") {
				t.Fatalf("physical contract mutation %s: %v", mutation, err)
			}
		})
	}
}
func TestPhysicalReplayProjectionRequiresActualWaitLabelsAndStatus(t *testing.T) {
	native := []PhysicalProtocolFact{{Kind: "native-stop_task-request-observed"}}
	if _, err := physicalNativeFacts(native, "replay"); err == nil {
		t.Fatal("native frame passed as actual replay cancellation")
	}
	for _, status := range []*FallbackWorkerStatus{nil, {ExitCode: 0}, {ExitCode: -1, Signal: 15}} {
		if _, err := physicalNativeFacts([]PhysicalProtocolFact{{Kind: "actual-stop_task", Status: status}}, "replay"); err == nil {
			t.Fatal("missing or wrong actual Wait accepted")
		}
	}
	got, err := physicalNativeFacts([]PhysicalProtocolFact{{Kind: "actual-stop_task", Status: &FallbackWorkerStatus{ExitCode: -1, Signal: 9}}}, "replay")
	if err != nil || len(got) != 1 || got[0].Kind != "native-stop_task-request-observed" || got[0].Status != nil {
		t.Fatal("closed actual-to-native projection", err)
	}
}

func TestPhysicalFailedCaptureIsPrivateExclusiveAndUnpromotable(t *testing.T) {
	home := physicalControlHome(t)
	if os.Mkdir(filepath.Join(home, "recording-children"), 0700) != nil {
		t.Fatal("private children")
	}
	path := filepath.Join(home, "recording-children", "bootstrap.json")
	config := LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "capture", Scenario: "stop-background-task-bootstrap", Path: path, Producer: Producer{Revision: strings.Repeat("a", 40)}}}
	_, tape := physicalRecordingControl(t)
	// A real stream prefix routinely exceeds the authorization control limit.
	large, _ := json.Marshal(map[string]string{"type": "assistant", "text": strings.Repeat("x", 32768)})
	tape.Frames = append(tape.Frames, Frame{Direction: "stdout", Data: large})
	failed := &LifecycleTape{Tape: tape}
	diagnostic := strings.TrimSuffix(path, ".json") + ".failed-native"
	if RetainPhysicalFailedCapture(diagnostic, config, strings.Repeat("b", 64), failed, 7) != nil {
		t.Fatal("failed observation retention")
	}
	info, err := os.Lstat(diagnostic)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("failed observation is not private")
	}
	if RetainPhysicalFailedCapture(diagnostic, config, strings.Repeat("b", 64), failed, 7) == nil {
		t.Fatal("failed observation overwritten")
	}
	if RetainPhysicalFailedCapture(path, config, strings.Repeat("b", 64), failed, 7) == nil {
		t.Fatal("failed observation published as native fixture")
	}
	raw, err := os.ReadFile(diagnostic)
	if len(raw) <= 16384 {
		t.Fatal("diagnostic control never exercised the native carrier bound")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLifecycleSuite(bytes.NewReader(raw), []string{"bootstrap"}); err == nil {
		t.Fatal("failed native diagnostic promoted as coverage")
	}
	var stored PhysicalFailedCapture
	if ReadLifecycleControl(diagnostic, MaxBytes, &stored) != nil || stored.Kind != "failed-unpromotable-native-observation-v1" || stored.Frames != 7 || len(stored.Tape.Frames) != len(tape.Frames) {
		t.Fatal("failed evidence lost the actual rejecting observation")
	}
	destination := filepath.Join(home, "stop-background-task-failed-unpromotable-native.json")
	if PublishPhysicalFailedCapture(destination, stored) != nil {
		t.Fatal("outer retention lost large failed native evidence")
	}
	copied, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(copied, raw) {
		t.Fatal("outer retention changed failed native evidence")
	}

}

func TestPhysicalFailedCaptureRejectsOversizePrivateAndWrongDestination(t *testing.T) {
	for _, mutation := range []string{"oversize", "private", "wrong-path"} {
		t.Run(mutation, func(t *testing.T) {
			home := physicalControlHome(t)
			_, tape := physicalRecordingControl(t)
			text := strings.Repeat("x", MaxBytes)
			if mutation == "private" {
				text = "sk-ant-controlled-test-secret"
			}
			if mutation == "wrong-path" {
				text = "controlled safe text"
			}
			raw, _ := json.Marshal(map[string]string{"type": "assistant", "text": text})
			tape.Frames = append(tape.Frames, Frame{Direction: "stdout", Data: raw})
			value := PhysicalFailedCapture{Kind: "failed-unpromotable-native-observation-v1", Scenario: tape.Scenario, Revision: strings.Repeat("a", 40), SourceBinarySHA256: strings.Repeat("b", 64), Frames: len(tape.Frames), Tape: tape}
			path := filepath.Join(home, "stop-background-task-failed-unpromotable-native.json")
			if mutation == "wrong-path" {
				path = filepath.Join(home, "stop-background-task.json")
			}
			if PublishPhysicalFailedCapture(path, value) == nil {
				t.Fatal("unsafe failed diagnostic publication", mutation)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("rejected diagnostic left a ready file", mutation)
			}
		})
	}
}
