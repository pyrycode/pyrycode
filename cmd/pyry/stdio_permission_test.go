package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/streamsup"
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
		RequestID: "request-question",
		ToolUseID: "tool-use-question",
		ToolName:  "AskUserQuestion",
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
