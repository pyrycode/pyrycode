package claudetape

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func physicalObservedStop(t *testing.T, suffix string) (*PhysicalReplayProtocol, []PhysicalProtocolFact) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "not-created")
	binding := LifecycleRoleBinding{Role: "bootstrap", SessionID: "33333333-3333-4333-8333-333333333333", ConversationID: "44444444-4444-4444-8444-444444444444", WorkDir: filepath.Join(home, "work")}
	p, err := NewPhysicalNativeObserver(context.Background(), home, "stop-hold", binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	spec, _ := physicalSpec("stop-hold")
	d := PhysicalOperationDeclaration{Version: 1, UserSequence: 1, Owner: binding, Spec: spec, UserText: "Original source user"}
	if p.Arm(d) != nil {
		t.Fatal("native declaration shape")
	}
	raw, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": d.UserText}}}})
	if p.BeforeInput(raw) != nil {
		t.Fatal("native user identity")
	}
	physicalProtocolJSON(t, p, physicalProtocolTool("tool", physicalQuote(filepath.Join(home, "work", "stop-hold")), true))
	physicalProtocolJSON(t, p, physicalProtocolRoster("task"))
	physicalProtocolJSON(t, p, physicalProtocolStart("tool", "task"))
	if suffix == "premature" {
		return p, nil
	}
	if p.BeforeInput([]byte(`{"type":"control_request","request_id":"stop","request":{"subtype":"stop_task","task_id":"task"}}`)) != nil {
		t.Fatal("native control identity")
	}
	if suffix == "omission" {
		physicalProtocolJSON(t, p, physicalProtocolRoster(""))
	} else {
		physicalProtocolJSON(t, p, physicalProtocolTerminal("task", "stopped"))
	}
	facts, err := p.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatal("native observer created original rig or witness")
	}
	for _, operation := range p.operations {
		if operation.owned != nil {
			t.Fatal("native observer launched a second operation")
		}
	}
	return p, facts
}
func TestPhysicalNativeObserverDoesNotManufacturePhysicalProof(t *testing.T) {
	for _, suffix := range []string{"notification", "omission"} {
		t.Run(suffix, func(t *testing.T) {
			_, facts := physicalObservedStop(t, suffix)
			for _, fact := range facts {
				if fact.Status != nil {
					t.Fatal("native frame substituted for actual OS Wait")
				}
			}
			kinds := []string{}
			for _, fact := range facts {
				kinds = append(kinds, fact.Kind)
			}
			last := "native-task-terminal-observed"
			if suffix == "omission" {
				last = "native-roster-omission-observed"
			}
			want := []string{"actual-user-forwarded", "native-tool-observed", "native-task-joined", "native-stop_task-request-observed", last}
			if !reflect.DeepEqual(kinds, want) {
				t.Fatalf("native observer claimed replay proof: %v", kinds)
			}
		})
	}
}
func TestPhysicalNativeObserverStillRejectsPrematureOrChangedControls(t *testing.T) {
	for _, mutation := range []string{"terminal-before-control", "foreign-task", "extra-selector", "missing-terminal"} {
		t.Run(mutation, func(t *testing.T) {
			p, _ := physicalObservedStop(t, "premature")
			var err error
			switch mutation {
			case "terminal-before-control":
				raw, _ := json.Marshal(physicalProtocolTerminal("task", "stopped"))
				err = p.BeforeOutput(raw)
			case "foreign-task":
				err = p.BeforeInput([]byte(`{"type":"control_request","request_id":"stop","request":{"subtype":"stop_task","task_id":"foreign"}}`))
			case "extra-selector":
				err = p.BeforeInput([]byte(`{"type":"control_request","request_id":"stop","request":{"subtype":"stop_task","task_id":"task","pid":123}}`))
			case "missing-terminal":
				_, err = p.Finish()
			}
			if err == nil {
				t.Fatal("native shape observer accepted an incomplete/foreign trace")
			}
		})
	}
}
