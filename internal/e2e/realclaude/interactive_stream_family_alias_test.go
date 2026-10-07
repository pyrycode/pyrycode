//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const familyAliasMenuBudget = 30 * time.Second

// TestInteractiveStreamPinnedModelFollowsItsFamily is the live arm of the family
// rule (#2447, widened 2026-10-07): a real claude, sent a pinned model id, runs the
// model that id's family currently means.
//
// Two claims only a live child can answer. First, the menu a real claude publishes
// reaches the client as one row per family: Claude Code 2.1.289 publishes pinned
// rows (claude-opus-5, claude-opus-4-7) beside its family rows, and the daemon must
// have reduced them away. Second, a pinned id a client sends anyway is accepted and
// run as its family. If a real claude did not honour the alias pyry sends, the
// rewrite would silently change WHICH MODEL RUNS, which is strictly worse than the
// staleness it fixes.
//
// A control response cannot answer the second. The set_model_v2.1.259_refuse
// capture shows claude replying success even for claude-no-such-model-2279, so the
// acknowledgement proves only that the request was parsed. The model_announced
// frame is the only witness that names what actually served the turn, which is why
// this test spends two turns rather than reading a reply.
//
// NOTHING IS HARD-CODED. The family row is chosen from the menu this run's own
// child published, the pinned id is that row's own resolution with its last
// version segment dropped (claude-fable-5-1 becomes claude-fable-5, an older
// model of the same family), and the comparand is the row's resolved_model. Sent
// verbatim, that id would run the older model or fail; resolved to its family it
// runs the row's model. The baseline turn is the instrument check: asserting that
// the announced model MOVED, and moved to the row's resolution, is what tells the
// mechanism from a coincidence.
func TestInteractiveStreamPinnedModelFollowsItsFamily(t *testing.T) {
	runParallel(t)
	h, convID := startStreamModalResolutionHarness(t, permissionDaemonModel)

	menu := liveModelMenu2447(t, h, convID)
	assertOneRowPerFamily2447(t, menu)

	// Baseline: what serves a turn before anything is changed.
	sealSendMessage(t, h.phone, h.initSend, 1000, convID, "m-2447-baseline",
		fmt.Sprintf("Reply with one short word and nothing else. run=%d", time.Now().UnixNano()))
	baseline, seen := drainForAnnouncedModel(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
	if !seen {
		t.Fatal("#2447: the baseline turn emitted no model_announced frame; the measurement has no starting point")
	}
	if baseline.Model == "" {
		t.Fatal("#2447: the baseline model_announced frame named no model")
	}

	row, pinned := pickFamilyRow2447(t, menu, baseline.Model)
	t.Logf("#2447: baseline model %q; family row %q (display %q) resolving to %q; sending the pinned id %q",
		baseline.Model, row.Value, row.DisplayName, row.ResolvedModel, pinned)

	// Model ONLY. A present effort or permission mode would still route in-band,
	// but naming one more field than the rule changes would let an unrelated
	// rejection end the run before the model is ever delivered.
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   1001,
		Type: protocol.TypeSetSessionSettings,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{
			SessionID: streamModalBootstrapUUID,
			Model:     &pinned,
		}),
	})
	accepted := drainForCorrelatedEnvelope2281(t, h.phone, h.initRecv, 1001, familyAliasMenuBudget)
	if accepted.Type != protocol.TypeSessionSettingsUpdated {
		t.Fatalf("#2447: set_session_settings(%q) reply = %q %s, want %q: the daemon refused a pinned id whose family it offers",
			pinned, accepted.Type, accepted.Payload, protocol.TypeSessionSettingsUpdated)
	}

	sealSendMessage(t, h.phone, h.initSend, 1002, convID, "m-2447-after-pick",
		fmt.Sprintf("Reply with one short word and nothing else. run=%d", time.Now().UnixNano()))
	announced, seen := drainForAnnouncedModel(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
	if !seen {
		t.Fatal("#2447: the turn after the model pick emitted no model_announced frame")
	}

	if announced.Model == baseline.Model {
		t.Errorf("#2447 A1: the child reported model %q both before and after sending %q; "+
			"the settings change reached no live child at all", baseline.Model, pinned)
	}
	if announced.Model != row.ResolvedModel {
		t.Errorf("#2447 A2: after sending the pinned id %q the child reported model %q, want %q, "+
			"the %q row's own resolved_model from this run's menu.\n"+
			"If A1 passed, the delivery worked but claude ran something other than the family's current model: "+
			"either pyry sent the pinned id verbatim, or claude resolved the alias elsewhere. Report what was "+
			"measured (sent %q, family %q, announced %q).",
			pinned, announced.Model, row.ResolvedModel, row.Value, pinned, row.Value, announced.Model)
	}
}

// liveModelMenu2447 returns the model rows this run's child published, retrying
// while the vocabulary is still coming up. It is a near-copy of the menu request
// in interactive_stream_model_rejection_test.go rather than an extraction from it:
// no hermetic gate compiles this package — `make check` never sees the build tag —
// so restructuring a helper a live test already depends on would surface only as a
// failing live run, and spawnBootstrapDaemonVerbose set that precedent for the same
// reason.
//
// A truncated value or resolved_model on any row is fatal: this test compares both
// fields by exact equality, and a cut string would compare unequal for a reason
// that has nothing to do with the rewrite.
func liveModelMenu2447(t *testing.T, h *perConvHarness, convID string) []protocol.ModelOption {
	t.Helper()
	deadline := time.Now().Add(familyAliasMenuBudget)
	var env protocol.Envelope
	for attempt := uint64(1); ; attempt++ {
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
			ID:      attempt,
			Type:    protocol.TypeRequestModelList,
			TS:      time.Now().UTC(),
			Payload: mustJSON(t, protocol.RequestModelListPayload{ConversationID: convID}),
		})
		env = drainForCorrelatedEnvelope2281(t, h.phone, h.initRecv, attempt, time.Until(deadline))
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

// assertOneRowPerFamily2447 fails when the menu still offers a pinned row beside
// its own family row, which is what the daemon's reduction exists to remove.
func assertOneRowPerFamily2447(t *testing.T, menu []protocol.ModelOption) {
	t.Helper()
	values := make(map[string]bool, len(menu))
	for _, row := range menu {
		values[row.Value] = true
	}
	for _, row := range menu {
		if family, ok := rewritableFamily2447(row.Value); ok && values[family] {
			t.Errorf("#2447: the menu offers the pinned row %q beside its family row %q; it must be one row per family. Menu: %+v",
				row.Value, family, menu)
		}
	}
}

// pickFamilyRow2447 returns a family row to drive and the older pinned id to send
// for it. The row's value is a bare family name, and its resolved_model is
// claude-<that family>-<major>-<minor>, so dropping the minor names an older model
// of the same family. A fable row is preferred, falling back to any other, and a
// row resolving to the model already running is skipped: the measurement asserts
// that the announced model MOVED.
//
// No row at all is FATAL, never a skip: a skip reads as a pass in a suite whose
// health is judged by its executed count.
func pickFamilyRow2447(t *testing.T, menu []protocol.ModelOption, baseline string) (protocol.ModelOption, string) {
	t.Helper()
	var fallback *protocol.ModelOption
	var fallbackPinned string
	for i := range menu {
		row := menu[i]
		pinned, ok := olderPinnedID2447(row)
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

// olderPinnedID2447 derives an older pinned id from a family row: the row's value
// must be the family named by its resolved_model, which must carry exactly two
// version segments; the result drops the second. claude-fable-5-1 under the fable
// row gives claude-fable-5.
func olderPinnedID2447(row protocol.ModelOption) (string, bool) {
	family, ok := rewritableFamily2447(row.ResolvedModel)
	if !ok || family != row.Value {
		return "", false
	}
	parts := strings.Split(row.ResolvedModel, "-")
	if len(parts) != 4 {
		return "", false
	}
	return strings.Join(parts[:3], "-"), true
}

// rewritableFamily2447 reports the family a value would be rewritten to, and
// whether pyry rewrites it at all. It restates internal/modelfamily's rule
// independently, so this live arm does not ask the production code which values
// it rewrites and agree with itself by construction: after any trailing bracket
// group is split off, the base must divide on "-" into "claude", a family of
// letters, and at least one further segment with every remaining segment all
// digits. The group is carried onto the family, as the rule does.
func rewritableFamily2447(value string) (string, bool) {
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
