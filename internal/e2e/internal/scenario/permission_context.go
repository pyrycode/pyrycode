package scenario

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type PermissionContextSource struct {
	Type      string
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype   string
		ToolName  string `json:"tool_name"`
		ToolUseID string `json:"tool_use_id"`
		Input     struct {
			FilePath string `json:"file_path"`
			Command  string
		}
		Reason      json.RawMessage `json:"decision_reason,omitempty"`
		ReasonType  json.RawMessage `json:"decision_reason_type,omitempty"`
		BlockedPath string          `json:"blocked_path"`
		Description string
		DefaultToNo bool `json:"default_to_no"`
	}
	PermissionDenials []struct {
		ToolUseID string `json:"tool_use_id"`
	} `json:"permission_denials"`
}

func RequirePermissionContext(t *testing.T, source PermissionContextSource, raw json.RawMessage) {
	t.Helper()
	var reasonType string
	if len(source.Request.ReasonType) != 0 && json.Unmarshal(source.Request.ReasonType, &reasonType) != nil {
		t.Fatal("source reason type could not be decoded")
	}
	expected := protocol.ModalShownPayload{Reason: source.Request.Reason, ReasonType: reasonType,
		BlockedPath: source.Request.BlockedPath, Description: source.Request.Description, DefaultToNo: source.Request.DefaultToNo}
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal("source context could not be encoded")
	}
	var actual, want map[string]json.RawMessage
	if json.Unmarshal(raw, &actual) != nil || json.Unmarshal(encoded, &want) != nil {
		t.Fatal("context JSON could not be decoded")
	}
	for _, key := range []string{"reason", "reason_type", "blocked_path", "description", "default_to_no"} {
		gotValue, gotPresent := actual[key]
		wantValue, wantPresent := want[key]
		if gotPresent != wantPresent {
			t.Fatalf("phone context presence differs from source for %s", key)
		}
		if !wantPresent {
			continue
		}
		var gotJSON, wantJSON any
		if json.Unmarshal(gotValue, &gotJSON) != nil || json.Unmarshal(wantValue, &wantJSON) != nil || !reflect.DeepEqual(gotJSON, wantJSON) {
			t.Fatalf("phone context value differs from source for %s", key)
		}
	}
}

type PermissionContextConfig struct {
	ConversationID, Target, Prompt string
	Outside                        bool
	SurfaceBudget, TurnBudget      time.Duration
	Source                         func(string) PermissionContextSource
	Reconnect                      func()
	RequireAbsent                  func()
}

func PermissionContext(t *testing.T, w Wire, c PermissionContextConfig) {
	t.Helper()
	wait := func(kind string) protocol.Envelope {
		deadline := time.Now().Add(c.SurfaceBudget)
		for time.Now().Before(deadline) {
			env := w.Next(deadline)
			if env.Type == kind {
				return env
			}
		}
		t.Fatal("permission context wire event missing")
		return protocol.Envelope{}
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "permission-context", Text: c.Prompt})
	initial := wait(protocol.TypeModalShown)
	var shown protocol.ModalShownPayload
	if json.Unmarshal(initial.Payload, &shown) != nil || shown.Class != "permission" || shown.ModalID == "" || shown.ConversationID != c.ConversationID {
		t.Fatal("permission context did not surface an owned permission")
	}
	source := c.Source("control_request")
	matches := source.Request.ToolName == "Bash" && source.Request.Input.Command == "printf hello > permission-context-witness.txt"
	if c.Outside {
		matches = source.Request.ToolName == "Write" && source.Request.Input.FilePath == c.Target
	}
	if source.RequestID == "" || source.Request.ToolUseID == "" || !matches {
		t.Fatal("native permission request did not match the authored target")
	}
	if c.Outside {
		var reason, kind string
		if json.Unmarshal(source.Request.ReasonType, &kind) != nil || kind != "workingDir" || json.Unmarshal(source.Request.Reason, &reason) != nil || reason == "" {
			t.Fatal("outside native permission omitted workingDir reason")
		}
	} else if len(source.Request.ReasonType) != 0 || len(source.Request.Reason) != 0 {
		t.Fatal("ordinary native permission no longer omits both reason fields")
	}
	RequirePermissionContext(t, source, initial.Payload)
	c.RequireAbsent()
	c.Reconnect()
	reconciled := wait(protocol.TypeModalShown)
	var again protocol.ModalShownPayload
	if json.Unmarshal(reconciled.Payload, &again) != nil || !reflect.DeepEqual(shown, again) {
		t.Fatal("reconnected permission differs from the initial modal")
	}
	RequirePermissionContext(t, source, reconciled.Payload)
	w.Send(protocol.TypeModalAnswer, protocol.ModalAnswerPayload{ModalID: shown.ModalID, OptionID: "reject_once", AnswerToken: "permission-context-deny"})
	env := wait(protocol.TypeModalDismissed)
	var dismissed protocol.ModalDismissedPayload
	if json.Unmarshal(env.Payload, &dismissed) != nil || dismissed.ModalID != shown.ModalID || dismissed.Source != "remote" || dismissed.Outcome != "reject_once" {
		t.Fatal("permission context dismissal lost answer attribution")
	}
	DenyRetries(t, w, c.ConversationID, 4, c.TurnBudget)
	result := c.Source("result")
	confirmed := false
	for _, denial := range result.PermissionDenials {
		confirmed = confirmed || denial.ToolUseID == source.Request.ToolUseID
	}
	if !confirmed {
		t.Fatal("native result did not confirm the correlated denial")
	}
	c.RequireAbsent()
}
