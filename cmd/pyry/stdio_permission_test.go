package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type lockedBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) BytesCopy() []byte {
	b.Lock()
	defer b.Unlock()
	return slices.Clone(b.Buffer.Bytes())
}

func decodeStdioPermissionResponse(t *testing.T, raw []byte) (string, string, json.RawMessage) {
	t.Helper()
	var env struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior     string          `json:"behavior"`
				UpdatedInput json.RawMessage `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode control response %q: %v", raw, err)
	}
	if env.Type != "control_response" {
		t.Fatalf("response type = %q, want control_response", env.Type)
	}
	return env.Response.RequestID, env.Response.Response.Behavior, env.Response.Response.UpdatedInput
}

func TestStdioPermissionHandler_AllowAndDeny(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		verdict   func(json.RawMessage) permbridge.Verdict
		behavior  string
		wantInput json.RawMessage
	}{
		{
			name: "allow carries updated input",
			verdict: func(_ json.RawMessage) permbridge.Verdict {
				return permbridge.Allow(json.RawMessage(`{"questions":[],"answers":{"Pick":"A"}}`))
			},
			behavior:  permbridge.BehaviorAllow,
			wantInput: json.RawMessage(`{"questions":[],"answers":{"Pick":"A"}}`),
		},
		{
			name: "deny",
			verdict: func(_ json.RawMessage) permbridge.Verdict {
				return permbridge.Deny("denied")
			},
			behavior: permbridge.BehaviorDeny,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := permbridge.New()
			retired := make(chan struct{})
			surface := &approvalSurfaceReport{}
			surface.set(func(req permbridge.Request) func() {
				if !reg.Resolve(req.ToolUseID, tc.verdict(req.Input)) {
					t.Errorf("Resolve(%q) lost the live request", req.ToolUseID)
				}
				return func() { close(retired) }
			})
			h := newStdioPermissionHandler(reg, time.Minute, surface)
			var out lockedBuffer
			h.handle(streamsup.CanUseToolRequest{
				RequestID: "request-1",
				ToolUseID: "tool-use-1",
				ToolName:  "AskUserQuestion",
				Input:     json.RawMessage(`{"questions":[]}`),
			}, &out)

			select {
			case <-retired:
			case <-time.After(time.Second):
				t.Fatal("stdio permission waiter did not retire")
			}
			requestID, behavior, updatedInput := decodeStdioPermissionResponse(t, out.BytesCopy())
			if requestID != "request-1" || behavior != tc.behavior {
				t.Errorf("response = request_id %q behavior %q, want %q %q", requestID, behavior, "request-1", tc.behavior)
			}
			if !bytes.Equal(updatedInput, tc.wantInput) {
				t.Errorf("updatedInput = %s, want %s", updatedInput, tc.wantInput)
			}
		})
	}
}

func TestStdioPermissionHandler_CarriesAskContextFromCorrespondingFields(t *testing.T) {
	t.Parallel()
	reg := permbridge.New()
	surfaced := make(chan permbridge.Request, 1)
	retired := make(chan struct{})
	surface := &approvalSurfaceReport{}
	surface.set(func(req permbridge.Request) func() {
		surfaced <- req
		reg.Resolve(req.ToolUseID, permbridge.Deny("done"))
		return func() { close(retired) }
	})
	h := newStdioPermissionHandler(reg, time.Minute, surface)
	var out lockedBuffer
	reason := json.RawMessage(`{"source":"ask-field"}`)
	h.handle(streamsup.CanUseToolRequest{
		RequestID:               "request-context",
		ToolUseID:               "tool-context",
		ToolName:                "Bash",
		Input:                   json.RawMessage(`{"reason":"input-lookalike","reason_type":"input-type","blocked_path":"input-path","description":"input-description","default_to_no":false,"requires_user_interaction":false}`),
		DecisionReason:          reason,
		DecisionReasonType:      "future_reason_kind",
		BlockedPath:             "/ask/path",
		Description:             "ask description",
		DefaultToNo:             true,
		RequiresUserInteraction: true,
	}, &out)

	var got permbridge.Request
	select {
	case got = <-surfaced:
	case <-time.After(time.Second):
		t.Fatal("permission request was not surfaced")
	}
	if !bytes.Equal(got.DecisionReason, reason) || got.DecisionReasonType != "future_reason_kind" ||
		got.BlockedPath != "/ask/path" || got.Description != "ask description" || !got.DefaultToNo ||
		!got.RequiresUserInteraction {
		t.Errorf("surfaced context = %+v, want corresponding ask fields", got)
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("stdio permission waiter did not retire")
	}
}

func TestStdioPermissionHandler_ChildExitRetiresWithoutReplacementWrite(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	surfaced := make(chan permbridge.Request, 1)
	retired := make(chan struct{})
	surface := &approvalSurfaceReport{}
	surface.set(func(req permbridge.Request) func() {
		surfaced <- req
		return func() { close(retired) }
	})
	h := newStdioPermissionHandler(reg, time.Minute, surface)
	var origin, replacement lockedBuffer
	h.handle(streamsup.CanUseToolRequest{
		RequestID: "request-old",
		ToolUseID: "tool-use-old",
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"true"}`),
	}, &origin)

	req := <-surfaced
	h.childExited()
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("child exit did not terminate the permission waiter")
	}
	if reg.Resolve(req.ToolUseID, permbridge.Allow(req.Input)) {
		t.Fatal("late answer resolved an approval already denied on child exit")
	}
	if got := replacement.BytesCopy(); len(got) != 0 {
		t.Fatalf("replacement child received stale response %q", got)
	}
	requestID, behavior, _ := decodeStdioPermissionResponse(t, origin.BytesCopy())
	if requestID != "request-old" || behavior != permbridge.BehaviorDeny {
		t.Errorf("origin response = request_id %q behavior %q, want request-old deny", requestID, behavior)
	}
}

func TestStdioPermissionHandler_ReusedIDRemainsTrackedForChildExit(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	firstSurfaced := make(chan struct{})
	releaseFirst := make(chan struct{})
	retired := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var surfaceMu sync.Mutex
	surfaceCount := 0
	surface := &approvalSurfaceReport{}
	surface.set(func(permbridge.Request) func() {
		surfaceMu.Lock()
		index := surfaceCount
		surfaceCount++
		surfaceMu.Unlock()
		if index == 0 {
			close(firstSurfaced)
			<-releaseFirst
		}
		return func() { close(retired[index]) }
	})
	h := newStdioPermissionHandler(reg, time.Minute, surface)
	var firstOut, secondOut lockedBuffer
	request := streamsup.CanUseToolRequest{
		RequestID: "request-first",
		ToolUseID: "reused-tool-use",
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"true"}`),
	}
	h.handle(request, &firstOut)
	<-firstSurfaced
	if !reg.Resolve(request.ToolUseID, permbridge.Allow(request.Input)) {
		t.Fatal("first request was not live")
	}

	request.RequestID = "request-second"
	h.handle(request, &secondOut)
	close(releaseFirst)
	select {
	case <-retired[0]:
	case <-time.After(time.Second):
		t.Fatal("first waiter did not retire after resolution")
	}

	h.childExited()
	lateAnswerWon := reg.Resolve(request.ToolUseID, permbridge.Allow(request.Input))
	select {
	case <-retired[1]:
	case <-time.After(time.Second):
		t.Fatal("reused-ID waiter did not retire after child exit")
	}
	if lateAnswerWon {
		t.Fatal("late answer resolved the reused ID; older waiter cleanup removed its child-exit tracking")
	}
	requestID, behavior, _ := decodeStdioPermissionResponse(t, secondOut.BytesCopy())
	if requestID != "request-second" || behavior != permbridge.BehaviorDeny {
		t.Errorf("second response = request_id %q behavior %q, want request-second deny", requestID, behavior)
	}
}

func TestStdioPermissionHandler_AskUserQuestionUsesExistingSurface(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	surfaced := make(chan struct{})
	retired := make(chan struct{})
	surface := &approvalSurfaceReport{}
	surface.set(func(req permbridge.Request) func() {
		retire := f.bridge.Surface(req)
		close(surfaced)
		return func() {
			retire()
			close(retired)
		}
	})
	h := newStdioPermissionHandler(f.perm, time.Minute, surface)
	var out lockedBuffer
	h.handle(streamsup.CanUseToolRequest{
		RequestID:               "request-question",
		ToolUseID:               "tool-use-question",
		ToolName:                "AskUserQuestion",
		RequiresUserInteraction: true,
		Input: questionInput(t, multiQuestionText, "Write strategy", "rewrite",
			"replace the file wholesale"),
	}, &out)

	select {
	case <-surfaced:
	case <-time.After(time.Second):
		t.Fatal("stdio question did not reach the existing surfacer")
	}
	shown := lastQuestionShown(t, f.bcast.pushes)
	if !f.bridge.AnswerQuestion(shown.QuestionBatchID, goodAnswers()) {
		t.Fatal("AnswerQuestion rejected a valid answer")
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("answered stdio question did not retire")
	}
	requestID, behavior, updatedInput := decodeStdioPermissionResponse(t, out.BytesCopy())
	if requestID != "request-question" || behavior != permbridge.BehaviorAllow {
		t.Fatalf("response = request_id %q behavior %q, want request-question allow", requestID, behavior)
	}
	var answer struct {
		Answers map[string]any `json:"answers"`
	}
	if err := json.Unmarshal(updatedInput, &answer); err != nil {
		t.Fatalf("decode updatedInput: %v", err)
	}
	if len(answer.Answers) == 0 {
		t.Fatal("updatedInput carried no question answers")
	}
}

func TestStdioPermissionHandler_UnansweredDenies(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	retired := make(chan struct{})
	surface := &approvalSurfaceReport{}
	surface.set(func(permbridge.Request) func() { return func() { close(retired) } })
	h := newStdioPermissionHandler(reg, 10*time.Millisecond, surface)
	var out lockedBuffer
	h.handle(streamsup.CanUseToolRequest{
		RequestID: "request-timeout",
		ToolUseID: "tool-use-timeout",
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"true"}`),
	}, &out)

	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("unanswered stdio request did not terminate")
	}
	requestID, behavior, _ := decodeStdioPermissionResponse(t, out.BytesCopy())
	if requestID != "request-timeout" || behavior != permbridge.BehaviorDeny {
		t.Errorf("response = request_id %q behavior %q, want request-timeout deny", requestID, behavior)
	}
}

func TestStdioPermissionHandler_InteractionRequiredRefusesRemoteAnswers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		optionID string
	}{
		{"allow", string(turnevent.PermissionOptionKindAllowOnce)},
		{"deny", string(turnevent.PermissionOptionKindRejectOnce)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := permbridge.New()
			modal := modalbridge.New()
			bcast := oneInteractiveConn("c1")
			bridge := newStreamApprovalBridge(reg, modal, bcast, func() string { return testConvID }, context.Background(), discardLogger())
			reg.SetAnswerable(bridge.ApprovalAnswerable)

			surfaced := make(chan struct{})
			retired := make(chan struct{})
			surface := &approvalSurfaceReport{}
			surface.set(func(req permbridge.Request) func() {
				retire := bridge.Surface(req)
				close(surfaced)
				return func() {
					retire()
					close(retired)
				}
			})
			h := newStdioPermissionHandler(reg, 250*time.Millisecond, surface)
			var out lockedBuffer
			h.handle(streamsup.CanUseToolRequest{
				RequestID:               "request-interaction",
				ToolUseID:               "tool-interaction",
				ToolName:                "Bash",
				Input:                   json.RawMessage(`{"command":"true"}`),
				RequiresUserInteraction: true,
			}, &out)

			select {
			case <-surfaced:
			case <-time.After(time.Second):
				t.Fatal("interaction-required permission was not surfaced")
			}
			shown := lastModalShown(t, bcast.pushes)
			resolver := newModalResolverV2(modal, &fakeKeystroker{}, discardLogger())
			resolver.streamApprovals = bridge

			if dismissal, ok := resolver.ResolveAnswer(shown.ModalID, tc.optionID, "answer-token", eligibleDevice(t)); ok {
				t.Errorf("ResolveAnswer(%s) accepted interaction-required permission: %+v", tc.name, dismissal)
			}
			if got := out.BytesCopy(); len(got) != 0 {
				t.Errorf("pre-deadline control response = %q, want none", got)
			}
			if _, ok := reg.Lookup("tool-interaction"); !ok {
				t.Error("remote answer consumed the parked approval")
			}
			if _, ok := modal.Lookup(shown.ModalID); !ok {
				t.Error("remote answer consumed the permission modal")
			}
			if n := bridgeLen(bridge); n != 1 {
				t.Errorf("permission correlations = %d, want 1 before timeout", n)
			}
			parkedReq, _ := reg.Lookup("tool-interaction")
			if bridge.ApprovalAnswerable("tool-interaction", parkedReq) {
				t.Error("interaction-required permission reported answerable with an interactive client connected")
			}

			select {
			case <-retired:
			case <-time.After(2 * time.Second):
				t.Fatal("interaction-required permission did not retire after its deadline")
			}
			raw := out.BytesCopy()
			if got := bytes.Count(raw, []byte(`"type":"control_response"`)); got != 1 {
				t.Fatalf("control_response count = %d, want 1 in %q", got, raw)
			}
			requestID, behavior, _ := decodeStdioPermissionResponse(t, raw)
			if requestID != "request-interaction" || behavior != permbridge.BehaviorDeny {
				t.Errorf("timeout response = request_id %q behavior %q, want request-interaction deny", requestID, behavior)
			}
			if bytes.Contains(raw, []byte(`"behavior":"allow"`)) {
				t.Errorf("timeout response contains allow: %q", raw)
			}
			if _, ok := reg.Lookup("tool-interaction"); ok {
				t.Error("approval remains parked after timeout")
			}
			if _, ok := modal.Lookup(shown.ModalID); ok {
				t.Error("modal remains outstanding after timeout retirement")
			}
			if n := bridgeLen(bridge); n != 0 {
				t.Errorf("permission correlations = %d after timeout, want 0", n)
			}
			var dismissed int
			for _, typ := range pushTypes(bcast.pushes) {
				if typ == protocol.TypeModalDismissed {
					dismissed++
				}
			}
			if dismissed != 1 {
				t.Errorf("modal_dismissed count = %d, want 1; pushes = %v", dismissed, pushTypes(bcast.pushes))
			}
		})
	}
}
