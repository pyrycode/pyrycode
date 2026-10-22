package scenario

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type PermissionSettingsConfig struct {
	ConversationID, SessionID, Model string
	TurnBudget, ReadBudget           time.Duration
	ChildPID                         func() int
	ReadFixture                      func() (string, string)
}

func ConfirmedPermissionSettings(t *testing.T, w Wire, c PermissionSettingsConfig) {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "settings-warm", Text: "Reply with the single word ready. Do not use tools."})
	CompletedTurn(t, w.Next, c.ConversationID, c.TurnBudget, false, w.Source)
	pid := c.ChildPID()
	if pid <= 0 {
		t.Fatal("no live child to compare")
	}
	request := func() protocol.SessionSettingsPayload {
		id := w.Send(protocol.TypeRequestSessionSettings, protocol.RequestSessionSettingsPayload{ConversationID: c.ConversationID})
		env := Correlated(t, w.Next, id, 15*time.Second)
		var p protocol.SessionSettingsPayload
		if env.Type != protocol.TypeSessionSettings || json.Unmarshal(env.Payload, &p) != nil {
			t.Fatal("invalid session settings reply")
		}
		return p
	}
	check := func(p protocol.SessionSettingsPayload, model, mode string, yolo bool) {
		if p.SessionID != c.SessionID || p.Model != model || p.Effort != "" || p.PermissionMode != mode || p.YOLO != yolo {
			t.Fatal("session settings do not report the confirmed child posture")
		}
	}
	check(request(), "", "bypassPermissions", true)
	mode := "default"
	model := c.Model
	id := w.Send(protocol.TypeSetSessionSettings, protocol.SetSessionSettingsPayload{SessionID: c.SessionID, Model: &model, PermissionMode: &mode})
	env := Correlated(t, w.Next, id, 120*time.Second)
	var updated protocol.SessionSettingsUpdatedPayload
	if env.Type != protocol.TypeSessionSettingsUpdated || json.Unmarshal(env.Payload, &updated) != nil || updated.SessionID != c.SessionID {
		t.Fatal("settings update lost session/request correlation")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		p := request()
		if p.PermissionMode == "default" && !p.YOLO {
			check(p, c.Model, "default", false)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("confirmed default posture did not refresh")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if c.ChildPID() != pid {
		t.Fatal("permission switch respawned the child")
	}
	prompt, token := c.ReadFixture()
	if token == "" || strings.Contains(prompt, token) {
		t.Fatal("outside read prompt disclosed its witness")
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "settings-read", Text: prompt})
	reply, tools, modal, _ := OutsideRead(t, w, c.ConversationID, time.Now().Add(c.ReadBudget))
	if modal == nil {
		t.Fatal("confirmed default child did not gate the outside Read")
	}
	if !slices.Equal(tools, []string{"Read"}) {
		t.Fatal("outside read used a different tool")
	}
	RequireReadWitness(t, reply, token)
	if c.ChildPID() != pid {
		t.Fatal("outside read ran on a replacement child")
	}
}
