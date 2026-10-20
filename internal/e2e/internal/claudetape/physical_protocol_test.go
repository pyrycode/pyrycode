package claudetape

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func physicalProtocolControl(t *testing.T, home, family string, ctx context.Context) *PhysicalReplayProtocol {
	t.Helper()
	binding := LifecycleRoleBinding{Role: "bootstrap", SessionID: "33333333-3333-4333-8333-333333333333", ConversationID: "44444444-4444-4444-8444-444444444444", WorkDir: filepath.Join(home, "work")}
	p, err := NewPhysicalReplayProtocol(ctx, home, family, binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	return p
}
func physicalProtocolJSON(t *testing.T, p *PhysicalReplayProtocol, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil || p.BeforeOutput(raw) != nil {
		t.Fatal("physical provider continuation was not proved")
	}
}
func physicalProtocolTool(id, command string, background bool) any {
	return map[string]any{"type": "assistant", "parent_tool_use_id": nil, "message": map[string]any{"id": "physical-tool-message-" + id, "role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": json.RawMessage(physicalControlInput(command, background))}}}}
}
func physicalProtocolRoster(task string) any {
	tasks := []any{}
	if task != "" {
		tasks = append(tasks, map[string]string{"task_id": task, "task_type": "local_bash", "description": "A fixed controlled held task"})
	}
	return map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": tasks}
}
func physicalProtocolStart(id, task string) any {
	return map[string]any{"type": "system", "subtype": "task_started", "tool_use_id": id, "task_id": task, "task_type": "local_bash", "description": "A fixed controlled held task"}
}
func physicalProtocolTerminal(task, status string) any {
	return map[string]any{"type": "system", "subtype": "task_notification", "task_id": task, "status": status, "summary": "Controlled terminal status"}
}
func physicalProtocolArm(t *testing.T, p *PhysicalReplayProtocol, kind string) {
	t.Helper()
	spec, err := InspectPhysicalOperation(p.home, kind)
	if err != nil || p.Arm(PhysicalOperationDeclaration{Version: 1, UserSequence: p.next + 1, Owner: p.binding, Spec: spec, UserText: "A controlled original user for " + kind}) != nil {
		t.Fatal("current original rig declaration rejected", err)
	}
	raw, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "A controlled original user for " + kind}}}})
	if p.BeforeInput(raw) != nil {
		t.Fatal("actual source user was not joined")
	}
}
func physicalProtocolArrived(t *testing.T, arrived <-chan struct{}) {
	t.Helper()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("owned operation did not open actual FIFO")
	}
}
func physicalProtocolStopRig(t *testing.T) (*PhysicalReplayProtocol, <-chan struct{}, func()) {
	t.Helper()
	home := physicalControlHome(t)
	arrived, release := physicalControlFIFO(t, home, "stop-task.fifo")
	spec, _ := physicalSpec("stop-hold")
	if os.WriteFile(filepath.Join(home, "work", "stop-hold"), []byte(strings.ReplaceAll(spec.Script, physicalHome, home)), 0700) != nil {
		t.Fatal("original hold script")
	}
	p := physicalProtocolControl(t, home, "stop-hold", context.Background())
	physicalProtocolArm(t, p, "stop-hold")
	physicalProtocolJSON(t, p, physicalProtocolTool("hold-tool", physicalQuote(filepath.Join(home, "work", "stop-hold")), true))
	physicalProtocolArrived(t, arrived)
	if string(physicalControlWaitFile(t, filepath.Join(home, "work", "stop-hold.executed"))) != "x" {
		t.Fatal("original hold was not actually executed")
	}
	return p, arrived, release
}
func TestPhysicalProtocolStopRequiresActualWaitAndNativeTerminal(t *testing.T) {
	for _, terminal := range []string{"notification", "roster-omission"} {
		t.Run(terminal, func(t *testing.T) {
			p, _, release := physicalProtocolStopRig(t)
			defer release()
			// The original drain permits this roster to arrive before its start join.
			physicalProtocolJSON(t, p, physicalProtocolRoster("held-task"))
			physicalProtocolJSON(t, p, physicalProtocolStart("hold-tool", "held-task"))
			if p.BeforeInput([]byte(`{"type":"control_request","request_id":"actual-stop","request":{"subtype":"stop_task","task_id":"held-task"}}`)) != nil {
				t.Fatal("actual current stop rejected")
			}
			status, err := p.operations[0].owned.Wait(time.Second)
			if err != nil || status != (FallbackWorkerStatus{ExitCode: -1, Signal: 9}) {
				t.Fatal("native text substituted for actual owned Wait9", err)
			}
			if _, err := p.Finish(); err == nil {
				t.Fatal("control acceptance substituted for native terminal evidence")
			}
			// Finish failure is sticky; use the successful path in a fresh actual rig.
		})
	}
}
func TestPhysicalProtocolStopTerminalAllowsClosedFinish(t *testing.T) {
	for _, terminal := range []string{"notification", "roster-omission"} {
		t.Run(terminal, func(t *testing.T) {
			p, _, release := physicalProtocolStopRig(t)
			defer release()
			physicalProtocolJSON(t, p, physicalProtocolRoster("held-task"))
			physicalProtocolJSON(t, p, physicalProtocolStart("hold-tool", "held-task"))
			if p.BeforeInput([]byte(`{"type":"control_request","request_id":"actual-stop","request":{"subtype":"stop_task","task_id":"held-task"}}`)) != nil {
				t.Fatal("actual current stop rejected")
			}
			if terminal == "notification" {
				physicalProtocolJSON(t, p, physicalProtocolTerminal("held-task", "stopped"))
			} else {
				physicalProtocolJSON(t, p, physicalProtocolRoster(""))
			}
			facts, err := p.Finish()
			if err != nil || len(facts) < 4 || facts[3].Status == nil || *facts[3].Status != (FallbackWorkerStatus{ExitCode: -1, Signal: 9}) {
				t.Fatal("complete physical stop proof missing", err)
			}
			facts[3].Status.Signal = 15
			if p.facts[3].Status.Signal != 9 {
				t.Fatal("returned evidence mutated retained actual Wait")
			}
			if p.BeforeOutput([]byte(`{"type":"rate_limit_event"}`)) == nil {
				t.Fatal("post-seal provider event admitted")
			}
		})
	}
}
func TestPhysicalProtocolComposerForegroundStopsBeforeActualBackgroundRelease(t *testing.T) {
	home := physicalControlHome(t)
	arrived, release := physicalControlFIFO(t, home, "composer-background.fifo")
	defer release()
	p := physicalProtocolControl(t, home, "composer", context.Background())
	physicalProtocolArm(t, p, "composer-background")
	physicalProtocolJSON(t, p, physicalProtocolTool("background-tool", "cat "+filepath.Join(home, "work", "composer-background.fifo"), true))
	physicalProtocolArrived(t, arrived)
	physicalProtocolJSON(t, p, physicalProtocolRoster("background-task"))
	physicalProtocolJSON(t, p, physicalProtocolStart("background-tool", "background-task"))
	physicalProtocolJSON(t, p, map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "The controlled background task started."})
	beforeContext := len(p.facts)
	var contextRequest bytes.Buffer
	if err := streamsup.WriteContextUsage(&contextRequest, "native-context-after-setup", "summary"); err != nil {
		t.Fatal(err)
	}
	if p.BeforeInput(contextRequest.Bytes()) != nil || len(p.facts) != beforeContext || len(p.controlIDs) != 0 || !p.operations[0].owned.Running() || !p.setupCompleted {
		t.Fatal("automatic context request changed physical proof")
	}
	physicalProtocolArm(t, p, "composer-foreground")
	foreground := physicalQuote(filepath.Join(home, "work", "foreground-started"))
	released := physicalQuote(filepath.Join(home, "work", "foreground-released"))
	command := `printf '%s\n' "$$" > ` + foreground + `; while [ ! -f ` + released + ` ]; do sleep 0.1; done`
	physicalProtocolJSON(t, p, physicalProtocolTool("foreground-tool", command, false))
	physicalControlWaitFile(t, filepath.Join(home, "work", "foreground-started"))
	if p.BeforeInput([]byte(`{"type":"control_request","request_id":"actual-interrupt","request":{"subtype":"interrupt"}}`)) != nil {
		t.Fatal("actual foreground cancellation rejected")
	}
	background, fg := p.composerOperations()
	if !background.owned.Running() || !fg.stopped {
		t.Fatal("foreground cancellation did not preserve real background")
	}
	physicalProtocolJSON(t, p, map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true})
	if !background.owned.Running() || !p.cancelledSeen {
		t.Fatal("cancelled native turn waited for background release")
	}
	release()
	physicalProtocolJSON(t, p, physicalProtocolTerminal("background-task", "completed"))
	if status, err := background.owned.Wait(time.Second); err != nil || status != (FallbackWorkerStatus{ExitCode: 0}) {
		t.Fatal("actual rig release did not produce Wait0", err)
	}
	if facts, err := p.Finish(); err != nil || len(facts) < 8 {
		t.Fatal("full Composer physical proof missing", err, len(facts))
	}
}
func TestPhysicalProtocolRejectsUnownedOrPrematureEvidence(t *testing.T) {
	for _, kind := range []string{"foreign-task", "terminal-before-stop", "omission-before-stop", "duplicate-tool", "child-tool", "malformed-parent", "duplicate-task", "changed-tool-join", "extra-control-selector", "context-usage-selector", "context-usage-detail", "abort", "post-close"} {
		t.Run(kind, func(t *testing.T) {
			p, _, release := physicalProtocolStopRig(t)
			defer release()
			physicalProtocolJSON(t, p, physicalProtocolStart("hold-tool", "held-task"))
			physicalProtocolJSON(t, p, physicalProtocolRoster("held-task"))
			var err error
			switch kind {
			case "foreign-task":
				err = p.BeforeInput([]byte(`{"type":"control_request","request_id":"stop","request":{"subtype":"stop_task","task_id":"foreign"}}`))
			case "terminal-before-stop":
				raw, _ := json.Marshal(physicalProtocolTerminal("held-task", "stopped"))
				err = p.BeforeOutput(raw)
			case "omission-before-stop":
				raw, _ := json.Marshal(physicalProtocolRoster(""))
				err = p.BeforeOutput(raw)
			case "duplicate-tool":
				raw, _ := json.Marshal(physicalProtocolTool("hold-tool", physicalQuote(filepath.Join(p.home, "work", "stop-hold")), true))
				err = p.BeforeOutput(raw)
			case "child-tool", "malformed-parent":
				raw, _ := json.Marshal(physicalProtocolTool("foreign-tool", physicalQuote(filepath.Join(p.home, "work", "stop-hold")), true))
				var object map[string]any
				_ = json.Unmarshal(raw, &object)
				object["parent_tool_use_id"] = "foreign-agent"
				if kind == "malformed-parent" {
					object["parent_tool_use_id"] = 32
				}
				raw, _ = json.Marshal(object)
				err = p.BeforeOutput(raw)
			case "duplicate-task":
				raw, _ := json.Marshal(physicalProtocolStart("hold-tool", "held-task"))
				err = p.BeforeOutput(raw)
			case "changed-tool-join":
				raw, _ := json.Marshal(physicalProtocolStart("foreign-tool", "other-task"))
				err = p.BeforeOutput(raw)
			case "extra-control-selector":
				err = p.BeforeInput([]byte(`{"type":"control_request","request_id":"stop","request":{"subtype":"stop_task","task_id":"held-task","pid":1}}`))
			case "context-usage-selector":
				err = p.BeforeInput([]byte(`{"type":"control_request","request_id":"context","request":{"subtype":"get_context_usage","detail":"summary","task_id":"held-task"}}`))
			case "context-usage-detail":
				var request bytes.Buffer
				if err := streamsup.WriteContextUsage(&request, "context-full", "full"); err != nil {
					t.Fatal(err)
				}
				err = p.BeforeInput(request.Bytes())
			case "abort":
				ctx, cancel := context.WithCancel(context.Background())
				p.ctx = ctx
				cancel()
				err = p.BeforeInput([]byte(`{"type":"control_request","request_id":"stop","request":{"subtype":"stop_task","task_id":"held-task"}}`))
			case "post-close":
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
				err = p.Arm(PhysicalOperationDeclaration{Version: 1, UserSequence: 2, Owner: p.binding, Spec: p.operations[0].spec})
			}
			if err == nil {
				t.Fatal("unsupported protocol evidence accepted")
			}
			if _, err := p.Finish(); err == nil {
				t.Fatal("failed external proof became coverage")
			}
		})
	}
}
func TestPhysicalProtocolRejectsForeignDeclarationBeforeExecution(t *testing.T) {
	for _, kind := range []string{"role", "conversation", "session", "user", "version", "kind", "script", "missing-current-input"} {
		t.Run(kind, func(t *testing.T) {
			home := physicalControlHome(t)
			p := physicalProtocolControl(t, home, "stop-hold", context.Background())
			spec, _ := physicalSpec("stop-hold")
			declaration := PhysicalOperationDeclaration{Version: 1, UserSequence: 1, Owner: p.binding, Spec: spec, UserText: "A controlled original user for " + kind}
			switch kind {
			case "role":
				declaration.Owner.Role = "foreign"
			case "conversation":
				declaration.Owner.ConversationID = "55555555-5555-4555-8555-555555555555"
			case "session":
				declaration.Owner.SessionID = "55555555-5555-4555-8555-555555555555"
			case "user":
				declaration.UserSequence = 2
			case "version":
				declaration.Version = 2
			case "kind":
				declaration.Spec.Kind = "composer-background"
			case "script":
				declaration.Spec.Script += "echo injected\n"
			}
			if p.Arm(declaration) == nil || len(p.operations) != 0 {
				t.Fatal("foreign declaration started an operation")
			}
		})
	}
}

func TestPhysicalProtocolRejectsChildPartialAndForeignToolBeforeExecution(t *testing.T) {
	for _, kind := range []string{"child", "malformed-parent", "partial-input", "foreign-command", "foreground-posture", "unknown-field", "oversized-tool-id", "missing-user", "changed-user"} {
		t.Run(kind, func(t *testing.T) {
			home := physicalControlHome(t)
			spec, _ := physicalSpec("stop-hold")
			if os.WriteFile(filepath.Join(home, "work", "stop-hold"), []byte(strings.ReplaceAll(spec.Script, physicalHome, home)), 0700) != nil || syscall.Mkfifo(filepath.Join(home, "work", "stop-task.fifo"), 0600) != nil {
				t.Fatal("actual current rig input")
			}
			p := physicalProtocolControl(t, home, "stop-hold", context.Background())
			declaration := PhysicalOperationDeclaration{Version: 1, UserSequence: 1, Owner: p.binding, Spec: spec, UserText: "The complete original source user"}
			if p.Arm(declaration) != nil {
				t.Fatal("actual declaration")
			}
			if kind != "missing-user" {
				text := declaration.UserText
				if kind == "changed-user" {
					text = "A different undeclared source user"
				}
				user, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": text}}}})
				if err := p.BeforeInput(user); err != nil && kind != "changed-user" {
					t.Fatal(err)
				}
			}
			id := "physical-tool"
			if kind == "oversized-tool-id" {
				id = strings.Repeat("x", 257)
			}
			value := physicalProtocolTool(id, physicalQuote(filepath.Join(home, "work", "stop-hold")), true)
			raw, _ := json.Marshal(value)
			var object map[string]any
			_ = json.Unmarshal(raw, &object)
			block := object["message"].(map[string]any)["content"].([]any)[0].(map[string]any)
			switch kind {
			case "child":
				object["parent_tool_use_id"] = "foreign-parent"
			case "malformed-parent":
				object["parent_tool_use_id"] = true
			case "partial-input":
				block["input"] = map[string]any{}
			case "foreign-command":
				block["input"].(map[string]any)["command"] = "cat /foreign/fifo"
			case "foreground-posture":
				block["input"].(map[string]any)["run_in_background"] = false
			case "unknown-field":
				block["input"].(map[string]any)["allow_injected_commands"] = true
			}
			raw, _ = json.Marshal(object)
			if p.BeforeOutput(raw) == nil || len(p.operations) != 0 {
				t.Fatal("unsupported native selector created an owned operation")
			}
		})
	}
}
func TestPhysicalOperationContextAbortPreservesOwnershipUntilClose(t *testing.T) {
	home := physicalControlHome(t)
	arrived, release := physicalControlFIFO(t, home, "composer-background.fifo")
	defer release()
	spec, _ := InspectPhysicalOperation(home, "composer-background")
	operation, err := StartPhysicalOperation(home, spec, physicalControlInput("cat "+filepath.Join(home, "work", "composer-background.fifo"), true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := operation.Close(); err != nil {
			t.Error(err)
		}
	}()
	physicalProtocolArrived(t, arrived)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := operation.StopContext(ctx); err == nil || !operation.Running() {
		t.Fatal("aborted control changed physical process")
	}
	started := time.Now()
	if _, err := operation.WaitContext(ctx, time.Minute); err == nil || time.Since(started) > time.Second {
		t.Fatal("wait did not abort while preserving its owned capability")
	}
	if !operation.Running() {
		t.Fatal("aborted wait claimed a physical completion")
	}
}
