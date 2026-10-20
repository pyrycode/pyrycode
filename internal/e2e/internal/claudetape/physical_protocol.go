package claudetape

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// PhysicalReplayProtocol is an unregistered replay-only operation coordinator.
// Callers must first validate the actual incoming frame against their strict
// Player, then run BeforeInput/BeforeOutput BEFORE forwarding any continuation.
// It neither generates provider frames nor creates test rig inputs or witnesses.
// One broker loop owns this coordinator; cancel its context before joining it,
// then Close every owned group from that same loop. A future recording profile
// must separately audit declarations and native proof.
type PhysicalReplayProtocol struct {
	sealed, closed bool
	observeOnly    bool
	cancelledSeen  bool
	ctx            context.Context
	binding        LifecycleRoleBinding
	roster         *turnevent.BackgroundTaskRoster
	home, family   string
	next           int
	users          int
	setupCompleted bool
	armed          *PhysicalOperationDeclaration
	operations     []*physicalProtocolOperation
	byTool         map[string]*physicalProtocolOperation
	byTask         map[string]*physicalProtocolOperation
	controlIDs     map[string]bool
	parser         *streamsup.Parser
	events         []turnevent.Event
	failed         error
	interrupted    bool
	facts          []PhysicalProtocolFact
}

type PhysicalOperationDeclaration struct {
	Version, UserSequence int
	Owner                 LifecycleRoleBinding
	Spec                  PhysicalOperationSpec
	UserText              string
}

type PhysicalProtocolFact struct {
	Kind, ToolID, TaskID, ControlID string
	Declaration                     *PhysicalOperationDeclaration `json:",omitempty"`
	Input                           json.RawMessage               `json:",omitempty"`
	InputSHA256                     string                        `json:",omitempty"`
	Status                          *FallbackWorkerStatus         `json:",omitempty"`
}
type physicalProtocolOperation struct {
	spec                           PhysicalOperationSpec
	toolID, taskID                 string
	owned                          *OwnedPhysicalOperation
	rosterSeen, stopped, completed bool
	terminalSeen                   bool
}

func NewPhysicalReplayProtocol(ctx context.Context, home, family string, binding LifecycleRoleBinding) (*PhysicalReplayProtocol, error) {
	return newPhysicalProtocol(ctx, home, family, binding, false)
}

// NewPhysicalNativeObserver validates native protocol shape only. It never
// launches or signals a process, returns no OS Wait status, and cannot supply
// the original test's physical witnesses. Those assertions must run unchanged.
func NewPhysicalNativeObserver(ctx context.Context, home, family string, binding LifecycleRoleBinding) (*PhysicalReplayProtocol, error) {
	return newPhysicalProtocol(ctx, home, family, binding, true)
}
func newPhysicalProtocol(ctx context.Context, home, family string, binding LifecycleRoleBinding, observeOnly bool) (*PhysicalReplayProtocol, error) {
	if ctx == nil || ctx.Err() != nil || binding.Role != "bootstrap" || !poolUUID.MatchString(binding.SessionID) || !poolUUID.MatchString(binding.ConversationID) || binding.WorkDir != filepath.Join(home, "work") {
		return nil, errors.New("unowned physical protocol binding")
	}
	if _, err := physicalWork(home); err != nil && !observeOnly {
		return nil, err
	}
	if family != "stop-hold" && family != "composer" {
		return nil, errors.New("unknown physical protocol family")
	}
	p := &PhysicalReplayProtocol{observeOnly: observeOnly, ctx: ctx, binding: binding, home: home, family: family, byTool: map[string]*physicalProtocolOperation{}, byTask: map[string]*physicalProtocolOperation{}, controlIDs: map[string]bool{}}
	p.parser = streamsup.NewParser(func(event turnevent.Event) { p.events = append(p.events, event) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return p, nil
}
func (p *PhysicalReplayProtocol) reject() error {
	if p.failed == nil {
		p.failed = errors.New("physical replay protocol proof failed")
	}
	return p.failed
}

// Arm requires the original rig's current input, after its actual constructor
// and before that user's native tool call. It never stages recorded file bytes.
func (p *PhysicalReplayProtocol) Arm(declaration PhysicalOperationDeclaration) error {
	if p.sealed || p.closed {
		return p.reject()
	}
	spec := declaration.Spec
	if declaration.Version != 1 || declaration.UserSequence != p.next+1 || declaration.Owner != p.binding || declaration.UserText == "" || len(declaration.UserText) > 32768 || !utf8.ValidString(declaration.UserText) {
		return p.reject()
	}
	if p.ctx.Err() != nil {
		return p.reject()
	}
	if p.failed != nil {
		return p.failed
	}
	expected := []string{"stop-hold"}
	if p.family == "composer" {
		expected = []string{"composer-background", "composer-foreground"}
	}
	if p.armed != nil || p.next >= len(expected) || spec.Kind != expected[p.next] || spec.Validate() != nil || (!p.observeOnly && spec.CheckCurrentInput(p.home) != nil) {
		return p.reject()
	}
	if p.family == "composer" && p.next == 1 && !p.setupCompleted {
		return p.reject()
	}
	value := declaration
	p.armed = &value
	p.next++
	return nil
}
func (p *PhysicalReplayProtocol) BeforeOutput(raw []byte) error {
	if p.sealed || p.closed {
		return p.reject()
	}
	if p.ctx.Err() != nil {
		return p.reject()
	}
	if p.failed != nil {
		return p.failed
	}
	if !uniqueSuiteKeys(raw) || safeCLIJSON(raw) != nil {
		return p.reject()
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return p.reject()
	}
	if parent, present := envelope["parent_tool_use_id"]; present && !bytes.Equal(bytes.TrimSpace(parent), []byte("null")) {
		var id string
		if json.Unmarshal(parent, &id) != nil || id != "" {
			return p.reject()
		}
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return p.reject()
	}
	p.events = nil
	if _, err := p.parser.Write(append(compact.Bytes(), '\n')); err != nil {
		return p.reject()
	}
	events := p.events
	p.events = nil
	for _, event := range events {
		switch ev := event.(type) {
		case turnevent.ToolStart:
			if p.armed == nil || p.users != p.armed.UserSequence || ev.Title != "Bash" || ev.ParentToolCallID != "" || ev.ToolCallID == "" || len(ev.ToolCallID) > 256 || p.byTool[ev.ToolCallID] != nil {
				return p.reject()
			}
			var owned *OwnedPhysicalOperation
			if _, err := PhysicalInputCommand(p.home, p.armed.Spec, ev.RawInput); err != nil {
				return p.reject()
			}
			if !p.observeOnly {
				var err error
				owned, err = StartPhysicalOperation(p.home, p.armed.Spec, ev.RawInput)
				if err != nil {
					return p.reject()
				}
			}
			operation := &physicalProtocolOperation{spec: p.armed.Spec, toolID: ev.ToolCallID, owned: owned}
			declaration := *p.armed
			p.armed = nil
			p.operations = append(p.operations, operation)
			p.byTool[ev.ToolCallID] = operation
			p.facts = append(p.facts, PhysicalProtocolFact{Kind: p.factKind("actual-operation-started", "native-tool-observed"), ToolID: ev.ToolCallID, Declaration: &declaration, Input: physicalCanonicalJSON(ev.RawInput), InputSHA256: physicalProtocolDigest(physicalCanonicalJSON(ev.RawInput))})
		case turnevent.BackgroundTaskStarted:
			operation := p.byTool[ev.ToolCallID]
			if operation == nil || operation.spec.Kind == "composer-foreground" || operation.taskID != "" || ev.TaskID == "" || len(ev.TaskID) > 256 || p.byTask[ev.TaskID] != nil || ev.TaskType != "local_bash" || len(ev.TruncatedFields) > 0 || !operation.running() {
				return p.reject()
			}
			operation.taskID = ev.TaskID
			p.byTask[ev.TaskID] = operation
			if err := p.checkRoster(); err != nil {
				return err
			}
			p.facts = append(p.facts, PhysicalProtocolFact{Kind: "native-task-joined", ToolID: ev.ToolCallID, TaskID: ev.TaskID})
		case turnevent.BackgroundTaskUpdated:
			operation := p.byTask[ev.TaskID]
			if operation == nil || len(ev.TruncatedFields) > 0 {
				return p.reject()
			}
			status := ev.Status
			if status == "" && ev.Patch != "" {
				var patch struct {
					Status string `json:"status"`
				}
				if json.Unmarshal([]byte(ev.Patch), &patch) != nil {
					return p.reject()
				}
				status = patch.Status
			}
			if status == "killed" {
				// Claude 2.1.280 publishes this task_updated patch after the
				// observed stop. The original root requires stopped/omission;
				// killed is not allowed to satisfy that terminal assertion.
				if p.family != "stop-hold" || !operation.stopped || operation.completed {
					return p.reject()
				}
				if operation.owned != nil {
					actual, err := operation.owned.WaitContext(p.ctx, 100*time.Millisecond)
					if err != nil || actual != (FallbackWorkerStatus{ExitCode: -1, Signal: 9}) {
						return p.reject()
					}
				}
				continue
			}
			if status == "running" {
				if !operation.running() || operation.stopped || operation.completed || operation.terminalSeen {
					return p.reject()
				}
				// This is progress only. It cannot satisfy a terminal assertion.
				continue
			}
			if status != "" {
				if p.requireTerminal(operation, status) != nil {
					return p.reject()
				}
				if !operation.terminalSeen {
					p.facts = append(p.facts, PhysicalProtocolFact{Kind: p.factKind("native-task-terminal-after-actual-wait", "native-task-terminal-observed"), ToolID: operation.toolID, TaskID: operation.taskID})
				}
				operation.terminalSeen = true
			}
		case turnevent.BackgroundTaskRoster:
			if ev.DroppedTasks != 0 || len(ev.Tasks) > 1 {
				return p.reject()
			}
			p.roster = &ev
			if err := p.checkRoster(); err != nil {
				return err
			}

		case turnevent.TurnEnd:
			if p.family == "composer" && !p.interrupted && p.users == 1 {
				background, _ := p.composerOperations()
				if p.setupCompleted || ev.Reason != turnevent.TurnEndReasonEndTurn || ev.Outcome != "success" || ev.IsError || background == nil || background.taskID == "" || !background.rosterSeen || !background.running() {
					return p.reject()
				}
				p.setupCompleted = true
			}
			if p.interrupted {
				background, foreground := p.composerOperations()
				if p.cancelledSeen || ev.Reason != turnevent.TurnEndReasonCancelled || foreground == nil || !foreground.stopped || background == nil || !background.running() {
					return p.reject()
				}
				p.cancelledSeen = true
				p.facts = append(p.facts, PhysicalProtocolFact{Kind: p.factKind("cancelled-turn-with-live-background", "native-cancelled-turn-with-background-state"), ToolID: foreground.toolID, TaskID: background.taskID})
			}
		}
	}
	return nil
}
func (p *PhysicalReplayProtocol) checkRoster() error {
	if p.roster == nil {
		return nil
	}
	present := map[string]bool{}
	for _, task := range p.roster.Tasks {
		if task.TaskID == "" || len(task.TaskID) > 256 || present[task.TaskID] || task.TaskType != "local_bash" || len(task.TruncatedFields) > 0 {
			return p.reject()
		}
		present[task.TaskID] = true
		operation := p.byTask[task.TaskID]
		if operation == nil {
			// A roster can legitimately precede its matching task-start. Retain it;
			// only an exact later TaskID+ToolCallID join can lend operation identity.
			if len(p.byTask) > 0 {
				return p.reject()
			}
			if task.ToolCallID != "" && p.byTool[task.ToolCallID] == nil {
				return p.reject()
			}
			continue
		}
		if task.ToolCallID != "" && task.ToolCallID != operation.toolID {
			return p.reject()
		}
		operation.rosterSeen = true
	}
	for taskID, operation := range p.byTask {
		if operation.rosterSeen && !present[taskID] {
			if operation.spec.Kind == "composer-background" {
				if err := p.requireTerminal(operation, "completed"); err != nil {
					return err
				}
			} else {
				if !operation.stopped {
					return p.reject()
				}
				if !operation.terminalSeen {
					p.facts = append(p.facts, PhysicalProtocolFact{Kind: p.factKind("native-roster-omission-after-actual-wait", "native-roster-omission-observed"), ToolID: operation.toolID, TaskID: operation.taskID})
				}
				operation.terminalSeen = true
			}
			if operation.owned != nil {
				if _, err := operation.owned.WaitContext(p.ctx, 100*time.Millisecond); err != nil {
					return p.reject()
				}
			}
		}
	}
	return nil
}

func (p *PhysicalReplayProtocol) composerOperations() (background, foreground *physicalProtocolOperation) {
	for _, operation := range p.operations {
		if operation.spec.Kind == "composer-background" {
			background = operation
		}
		if operation.spec.Kind == "composer-foreground" {
			foreground = operation
		}
	}
	return
}
func (p *PhysicalReplayProtocol) requireTerminal(operation *physicalProtocolOperation, status string) error {
	switch status {
	case "stopped":
		if p.family != "stop-hold" || !operation.stopped || operation.completed {
			return p.reject()
		}
		if operation.owned != nil {
			actual, err := operation.owned.WaitContext(p.ctx, 100*time.Millisecond)
			if err != nil || actual != (FallbackWorkerStatus{ExitCode: -1, Signal: 9}) {
				return p.reject()
			}
		}
	case "completed":
		background, foreground := p.composerOperations()
		if p.family != "composer" || operation != background || !p.interrupted || !p.cancelledSeen || foreground == nil || !foreground.stopped || operation.stopped {
			return p.reject()
		}
		if operation.completed {
			return nil
		}
		var status *FallbackWorkerStatus
		if operation.owned != nil {
			actual, err := operation.owned.WaitContext(p.ctx, 5*time.Second)
			if err != nil || actual != (FallbackWorkerStatus{ExitCode: 0}) {
				return p.reject()
			}
			status = &actual
		}
		operation.completed = true
		p.facts = append(p.facts, PhysicalProtocolFact{Kind: p.factKind("actual-background-completed", "native-background-completion-observed"), ToolID: operation.toolID, TaskID: operation.taskID, Status: status})
	default:
		// An unreviewed or failed provider terminal cannot lend a successful local
		// process witness to the original completion assertion.
		return p.reject()
	}
	return nil
}
func (p *PhysicalReplayProtocol) BeforeInput(raw []byte) error {
	if p.sealed || p.closed {
		return p.reject()
	}
	if p.ctx.Err() != nil {
		return p.reject()
	}
	if p.failed != nil {
		return p.failed
	}
	if !uniqueSuiteKeys(raw) || safeCLIJSON(raw) != nil {
		return p.reject()
	}
	var header struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
			TaskID  string `json:"task_id"`
		} `json:"request"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return p.reject()
	}
	if header.Type == "user" {
		var user struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&user) != nil || decoder.Decode(new(any)) != io.EOF || user.Message.Role != "user" || len(user.Message.Content) != 1 || user.Message.Content[0].Type != "text" || p.armed == nil || p.armed.UserSequence != p.users+1 || p.armed.UserText != user.Message.Content[0].Text {
			return p.reject()
		}
		p.users++
		declaration := *p.armed
		p.facts = append(p.facts, PhysicalProtocolFact{Kind: "actual-user-forwarded", Declaration: &declaration, Input: physicalCanonicalJSON(raw), InputSHA256: physicalProtocolDigest(physicalCanonicalJSON(raw))})
		return nil
	}
	if header.Type != "control_request" {
		return nil
	}
	if header.Request.Subtype == "get_context_usage" {
		// Production requests this after each TurnEnd. Its exact non-actuating
		// envelope must pass without changing operation ownership or status.
		expected := map[string]any{"type": "control_request", "request_id": header.RequestID, "request": map[string]any{"subtype": "get_context_usage", "detail": "summary"}}
		var actual map[string]any
		if header.RequestID == "" || len(header.RequestID) > 256 || json.Unmarshal(raw, &actual) != nil || !reflect.DeepEqual(expected, actual) {
			return p.reject()
		}
		return nil
	}
	if header.Request.Subtype != "stop_task" && header.Request.Subtype != "interrupt" {
		if p.users > 0 {
			return p.reject()
		}
		return nil
	}
	if header.RequestID == "" || len(header.RequestID) > 256 || p.controlIDs[header.RequestID] {
		return p.reject()
	}
	// The actuator envelope is closed even if a future strict Player accepted a
	// broader shape. Extra selectors must never change a local signal target.
	expected := map[string]any{"type": "control_request", "request_id": header.RequestID, "request": map[string]any{"subtype": header.Request.Subtype}}
	if header.Request.Subtype == "stop_task" {
		expected["request"].(map[string]any)["task_id"] = header.Request.TaskID
	}
	var actual map[string]any
	if json.Unmarshal(raw, &actual) != nil || !reflect.DeepEqual(expected, actual) {
		return p.reject()
	}
	p.controlIDs[header.RequestID] = true
	var operation *physicalProtocolOperation
	if header.Request.Subtype == "stop_task" {
		if p.family != "stop-hold" || len(p.operations) != 1 {
			return p.reject()
		}
		operation = p.byTask[header.Request.TaskID]
		if operation == nil || !operation.rosterSeen || operation.stopped || operation.completed || !operation.running() {
			return p.reject()
		}
	} else {
		background, foreground := p.composerOperations()
		if p.family != "composer" || p.interrupted || background == nil || background.taskID == "" || !background.rosterSeen || !background.running() || foreground == nil || (!p.observeOnly && foreground.owned.RequireForegroundWitness(p.home) != nil) {
			return p.reject()
		}
		operation = foreground
	}
	var status *FallbackWorkerStatus
	if operation.owned != nil {
		actual, err := operation.owned.StopContext(p.ctx)
		if err != nil || actual != (FallbackWorkerStatus{ExitCode: -1, Signal: 9}) {
			return p.reject()
		}
		status = &actual
	}
	operation.stopped = true
	if header.Request.Subtype == "interrupt" {
		background, _ := p.composerOperations()
		if !background.running() {
			return p.reject()
		}
		p.interrupted = true
	}
	p.facts = append(p.facts, PhysicalProtocolFact{Kind: p.factKind("actual-"+header.Request.Subtype, "native-"+header.Request.Subtype+"-request-observed"), ToolID: operation.toolID, TaskID: operation.taskID, ControlID: header.RequestID, Status: status})
	return nil
}
func (p *PhysicalReplayProtocol) Finish() ([]PhysicalProtocolFact, error) {
	if p.closed {
		return nil, p.reject()
	}
	if p.ctx.Err() != nil {
		return nil, p.reject()
	}
	if p.failed != nil {
		return nil, p.failed
	}
	expected := 1
	if p.family == "composer" {
		expected = 2
	}
	if p.armed != nil || p.next != expected || p.users != expected || len(p.operations) != expected || len(p.controlIDs) != 1 {
		return nil, p.reject()
	}
	for _, operation := range p.operations {
		if operation.spec.Kind == "composer-background" {
			if !operation.completed || operation.stopped || !operation.terminalSeen || !p.cancelledSeen {
				return nil, p.reject()
			}
		} else if !operation.stopped || operation.completed || (operation.spec.Kind == "stop-hold" && !operation.terminalSeen) {
			return nil, p.reject()
		}
		if operation.running() {
			return nil, p.reject()
		}
	}
	// Copy the status pointers as well; retained facts cannot mutate the live
	// controller's proof after an accepted boundary.
	raw, err := json.Marshal(p.facts)
	if err != nil {
		return nil, p.reject()
	}
	var facts []PhysicalProtocolFact
	if json.Unmarshal(raw, &facts) != nil {
		return nil, p.reject()
	}
	p.sealed = true
	return facts, nil
}
func (p *PhysicalReplayProtocol) Close() error {
	p.closed = true
	var failures []string
	for _, operation := range p.operations {
		if operation.owned != nil {
			if err := operation.owned.Close(); err != nil {
				failures = append(failures, "owned operation group did not drain")
			}
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func physicalProtocolDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (p *PhysicalReplayProtocol) factKind(replay, native string) string {
	if p.observeOnly {
		return native
	}
	return replay
}

// running in observation mode is protocol state, never an OS liveness claim.
func (o *physicalProtocolOperation) running() bool {
	if o.owned != nil {
		return o.owned.Running()
	}
	return !o.stopped && !o.completed
}

func physicalCanonicalJSON(raw []byte) json.RawMessage {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF {
		return nil
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return canonical
}
