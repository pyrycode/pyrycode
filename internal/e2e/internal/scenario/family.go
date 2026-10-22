package scenario

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"strings"
	"testing"
	"time"
)

// ModelFamily retains the complete menu reduction, baseline, pinned-id rewrite
// and independently announced transition from the original live test.
func ModelFamily(t *testing.T, w Wire, convID, sessionID string, prompt func(int) string, menuBudget, turnBudget time.Duration) {
	t.Helper()
	menu := FamilyMenu(t, w, convID, menuBudget)
	RequireOneRowPerFamily(t, menu)
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: convID, MessageID: "m-2447-baseline", Text: prompt(1)})
	baseline, seen := AnnouncedTurn(t, w.Next, convID, turnBudget, w.Source)
	if !seen || baseline.Model == "" || baseline.ConversationID != convID {
		t.Fatal("model family baseline lacks owned announcement")
	}
	row, pinned := PickFamilyRow(t, menu, baseline.Model)
	id := w.Send(protocol.TypeSetSessionSettings, protocol.SetSessionSettingsPayload{SessionID: sessionID, Model: &pinned})
	accepted := Correlated(t, w.Next, id, menuBudget)
	if accepted.Type != protocol.TypeSessionSettingsUpdated {
		t.Fatal("pinned model from offered family was refused")
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: convID, MessageID: "m-2447-after-pick", Text: prompt(2)})
	announced, seen := AnnouncedTurn(t, w.Next, convID, turnBudget, w.Source)
	if !seen || announced.ConversationID != convID {
		t.Fatal("model family follow-up lacks owned announcement")
	}
	if announced.Model == baseline.Model {
		t.Error("pinned-model request did not move the live model")
	}
	if announced.Model != row.ResolvedModel {
		t.Error("pinned-model request did not resolve to the offered family model")
	}
}

// FamilyMenu reads the complete live model menu without weakening its value
// or resolution completeness requirements.
// A truncated value or resolved_model on any row is fatal: this test compares both
// fields by exact equality, and a cut string would compare unequal for a reason
// that has nothing to do with the rewrite.
func FamilyMenu(t *testing.T, w Wire, convID string, menuBudget time.Duration) []protocol.ModelOption {
	t.Helper()
	deadline := time.Now().Add(menuBudget)
	var env protocol.Envelope
	for {
		id := w.Send(protocol.TypeRequestModelList, protocol.RequestModelListPayload{ConversationID: convID})
		env = Correlated(t, w.Next, id, time.Until(deadline))
		if env.Type == protocol.TypeModelList {
			break
		}
		var unavailable protocol.ErrorPayload
		if env.Type != protocol.TypeError || json.Unmarshal(env.Payload, &unavailable) != nil ||
			unavailable.Code != protocol.CodeModelListUnavailable || !unavailable.Retryable {
			t.Fatalf("#2447: request_model_list reply = %q %s, want model_list or retryable model_list.unavailable",
				env.Type, env.Payload)
		}
		if time.Now().After(deadline) {
			t.Fatal("#2447: the child's model vocabulary remained unavailable past the initialize deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var menu protocol.ModelListPayload
	if err := json.Unmarshal(env.Payload, &menu); err != nil {
		t.Fatalf("#2447: decode model_list: %v", err)
	}
	if len(menu.Models) == 0 {
		t.Fatal("#2447: the child published no models; there is no row to pick")
	}
	if menu.DroppedModels != 0 {
		t.Fatalf("#2447: the menu dropped %d rows; the row this ticket is about may be one of them", menu.DroppedModels)
	}
	for _, option := range menu.Models {
		for _, field := range option.TruncatedFields {
			if field == "value" || field == "resolved_model" {
				t.Fatalf("#2447: row %q has a truncated %s; both are compared by exact equality here", option.DisplayName, field)
			}
		}
	}
	return menu.Models
}

// RequireOneRowPerFamily fails when the menu still offers a pinned row beside
// its own family row, which is what the daemon's reduction exists to remove.
func RequireOneRowPerFamily(t *testing.T, menu []protocol.ModelOption) {
	t.Helper()
	values := make(map[string]bool, len(menu))
	for _, row := range menu {
		values[row.Value] = true
	}
	for _, row := range menu {
		if family, ok := RewritableFamily(row.Value); ok && values[family] {
			t.Errorf("#2447: the menu offers the pinned row %q beside its family row %q; it must be one row per family. Menu: %+v",
				row.Value, family, menu)
		}
	}
}

// PickFamilyRow returns a family row to drive and the older pinned id to send
// for it. The row's value is a bare family name, and its resolved_model is
// claude-<that family>-<major>-<minor>, so dropping the minor names an older model
// of the same family. A fable row is preferred, falling back to any other, and a
// row resolving to the model already running is skipped: the measurement asserts
// that the announced model MOVED.
//
// No row at all is FATAL, never a skip: a skip reads as a pass in a suite whose
// health is judged by its executed count.
func PickFamilyRow(t *testing.T, menu []protocol.ModelOption, baseline string) (protocol.ModelOption, string) {
	t.Helper()
	var fallback *protocol.ModelOption
	var fallbackPinned string
	for i := range menu {
		row := menu[i]
		pinned, ok := OlderPinnedID(row)
		if !ok || row.ResolvedModel == baseline {
			continue
		}
		if row.Value == "fable" {
			return row, pinned
		}
		if fallback == nil {
			fallback, fallbackPinned = &menu[i], pinned
		}
	}
	if fallback != nil {
		t.Logf("#2447: no usable fable row; falling back to %q", fallback.Value)
		return *fallback, fallbackPinned
	}
	t.Fatalf("#2447: no family row resolves to a claude-<family>-<major>-<minor> id that differs from the running "+
		"model %q, so no older pinned id can be derived. Menu: %+v", baseline, menu)
	return protocol.ModelOption{}, ""
}

// OlderPinnedID derives an older pinned id from a family row: the row's value
// must be the family named by its resolved_model, which must carry exactly two
// version segments; the result drops the second. claude-fable-5-1 under the fable
// row gives claude-fable-5.
func OlderPinnedID(row protocol.ModelOption) (string, bool) {
	family, ok := RewritableFamily(row.ResolvedModel)
	if !ok || family != row.Value {
		return "", false
	}
	parts := strings.Split(row.ResolvedModel, "-")
	if len(parts) != 4 {
		return "", false
	}
	return strings.Join(parts[:3], "-"), true
}

// RewritableFamily reports the family a value would be rewritten to, and
// whether pyry rewrites it at all. It restates internal/modelfamily's rule
// independently, so this live arm does not ask the production code which values
// it rewrites and agree with itself by construction: after any trailing bracket
// group is split off, the base must divide on "-" into "claude", a family of
// letters, and at least one further segment with every remaining segment all
// digits. The group is carried onto the family, as the rule does.
func RewritableFamily(value string) (string, bool) {
	base, group := value, ""
	if i := strings.IndexByte(value, '['); i >= 0 {
		if i == 0 || value[len(value)-1] != ']' {
			return "", false
		}
		base, group = value[:i], value[i:]
	}
	parts := strings.Split(base, "-")
	if len(parts) < 3 || parts[0] != "claude" {
		return "", false
	}
	if strings.Trim(parts[1], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" || parts[1] == "" {
		return "", false
	}
	for _, segment := range parts[2:] {
		if segment == "" || strings.Trim(segment, "0123456789") != "" {
			return "", false
		}
	}
	return parts[1] + group, true
}
