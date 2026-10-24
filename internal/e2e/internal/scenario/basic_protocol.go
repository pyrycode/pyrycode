package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// AnnouncedTurn retains the live first-announcement, nonempty reply and terminal
// idle milestones. A missing announcement is returned explicitly to the caller.
func AnnouncedTurn(t *testing.T, next Next, convID string, timeout time.Duration, sources ...SourceFacts) (protocol.ModelAnnouncedPayload, bool) {
	t.Helper()
	var (
		announced protocol.ModelAnnouncedPayload
		seen      bool
	)
	checked := func(deadline time.Time) protocol.Envelope {
		env := next(deadline)
		if env.SessionStateCleared {
			return env
		}
		switch env.Type {
		case protocol.TypeModelAnnounced:
			if seen {
				return env
			}
			var p protocol.ModelAnnouncedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode model_announced payload: %v", err)
			}
			announced, seen = p, true
			t.Logf("captured %s for conversation %q", protocol.TypeModelAnnounced, p.ConversationID)
		}
		return env
	}
	CompletedTurn(t, checked, convID, timeout, false, sources...)
	return announced, seen
}

// RequireAnnouncedModel checks exact resolution, the independent alias inequality,
// conversation ownership and the live daemon's verbose log privacy boundary.
func RequireAnnouncedModel(t *testing.T, announced protocol.ModelAnnouncedPayload, seen bool, convID, alias, expected, daemonLog string) {
	t.Helper()
	if !seen {
		t.Fatal("turn completed without model_announced")
	}
	if announced.ConversationID != convID {
		t.Error("model announcement belongs to another conversation")
	}
	if announced.Model != expected {
		t.Errorf("announced model = %q, want exact resolved model %q", announced.Model, expected)
	}
	if announced.Model == alias {
		t.Error("model announcement echoed the bare spawn alias")
	}
	if announced.Model != "" && strings.Contains(daemonLog, announced.Model) {
		t.Error("announced model appeared in verbose daemon log")
	}
}

// ModelAnnouncement drives the same one-turn announcement and log assertions in
// live and recorded callers. The log reader is evaluated after terminal idle.
func ModelAnnouncement(t *testing.T, w Wire, convID, prompt, alias, expected string, budget time.Duration, daemonLog func() string) {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: convID, MessageID: "m-1", Text: prompt})
	announced, seen := AnnouncedTurn(t, w.Next, convID, budget, w.Source)
	RequireAnnouncedModel(t, announced, seen, convID, alias, expected, daemonLog())
}

// ToolTurn retains the full completed-turn parser/rate-limit alarms. Actual file
// effects and whether current Claude performs the requested tools remain live.
func ToolTurn(t *testing.T, w Wire, convID, prompt string, budget time.Duration) {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: convID, MessageID: "m-1", Text: prompt})
	tools := map[string]string{}
	completed := map[string]bool{}
	CompletedTurn(t, w.Next, convID, budget, false, w.Source)
	for _, env := range w.Source(convID) {
		switch env.Type {
		case protocol.TypeToolUse:
			var p protocol.ToolUsePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid tool-use frame")
			}
			if p.ConversationID == convID && p.ParentToolUseID == "" && p.ToolUseID != "" {
				tools[p.ToolUseID] = p.Name
			}
		case protocol.TypeToolResult:
			var p protocol.ToolResultPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid tool-result frame")
			}
			if p.ConversationID == convID && p.ParentToolUseID == "" && tools[p.ToolUseID] != "" {
				completed[tools[p.ToolUseID]] = true
			}
		}
	}
	if !completed["Write"] || !completed["Read"] {
		t.Fatal("parser sample lacked complete owned Write and Read round trips")
	}

}

// ModelRejection rejects an absent menu value, including co-submitted effort and
// permission posture, then requires the original model to complete another turn.
func ModelRejection(t *testing.T, w Wire, convID, sessionID, prompt, expected string, menuBudget, turnBudget time.Duration) {
	t.Helper()
	menuDeadline := time.Now().Add(menuBudget)
	var menuEnv protocol.Envelope
	for {
		id := w.Send(protocol.TypeRequestModelList, protocol.RequestModelListPayload{ConversationID: convID})
		menuEnv = Correlated(t, w.Next, id, time.Until(menuDeadline))
		if menuEnv.Type == protocol.TypeModelList {
			break
		}
		var unavailable protocol.ErrorPayload
		if menuEnv.Type != protocol.TypeError || json.Unmarshal(menuEnv.Payload, &unavailable) != nil || unavailable.Code != protocol.CodeModelListUnavailable || !unavailable.Retryable {
			t.Fatal("model menu reply was not a list or retryable unavailable")
		}
		if !time.Now().Before(menuDeadline) {
			t.Fatal("model menu remained unavailable")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var menu protocol.ModelListPayload
	if json.Unmarshal(menuEnv.Payload, &menu) != nil || len(menu.Models) == 0 || menu.DroppedModels != 0 {
		t.Fatal("cannot prove absence from an empty, malformed or dropped model menu")
	}
	offered := map[string]bool{}
	for _, option := range menu.Models {
		for _, field := range option.TruncatedFields {
			if field == "value" {
				t.Fatal("cannot prove absence from truncated model values")
			}
		}
		offered[option.Value] = true
	}
	candidate := ""
	for i := 0; ; i++ {
		candidate = fmt.Sprintf("claude-no-such-model-2281-%d", i)
		if !offered[candidate] {
			break
		}
	}
	effort, mode := "high", "plan"
	id := w.Send(protocol.TypeSetSessionSettings, protocol.SetSessionSettingsPayload{SessionID: sessionID, Model: &candidate, Effort: &effort, PermissionMode: &mode})
	rejected := Correlated(t, w.Next, id, menuBudget)
	var rejection protocol.ErrorPayload
	if rejected.Type != protocol.TypeError || json.Unmarshal(rejected.Payload, &rejection) != nil || rejection.Code != protocol.CodeProtocolMalformed || rejection.Retryable {
		t.Fatal("unknown model was not rejected as non-retryable protocol.malformed")
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: convID, MessageID: "m-after-model-reject", Text: prompt})
	announced, seen := AnnouncedTurn(t, w.Next, convID, turnBudget, w.Source)
	if !seen || announced.ConversationID != convID || announced.Model != expected {
		t.Fatal("post-rejection turn did not retain the original resolved model")
	}
}

// Correlated preserves receive order while selecting the exact request reply.
func Correlated(t *testing.T, next Next, id uint64, budget time.Duration) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		env := next(deadline)
		if env.InReplyTo != nil && *env.InReplyTo == id {
			return env
		}
	}
	t.Fatal("no correlated daemon reply within budget")
	return protocol.Envelope{}
}
