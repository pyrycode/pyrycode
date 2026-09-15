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

// TestInteractiveStreamFullIDRowFollowsItsFamily is #2447's live arm: a real
// claude, asked for a family alias, runs the model that family currently means.
//
// The whole ticket rests on a claim only a live child can answer. Pyry now rewrites
// a stored exact model id to its family alias at the two sinks where it hands a
// model to claude, so a session that picked the published Fable row keeps up with
// Fable releases instead of pinning to the version it picked. If a real claude did
// not honour the alias — did not accept "fable[1m]" as a model, or resolved it to
// something else — the rewrite would silently change WHICH MODEL RUNS, which is
// strictly worse than the staleness it fixes.
//
// A control response cannot answer it. The set_model_v2.1.259_refuse capture shows
// claude replying success even for claude-no-such-model-2279, so the acknowledgement
// proves only that the request was parsed. The model_announced frame is the only
// witness that names what actually served the turn, which is why this test spends
// two turns rather than reading a reply.
//
// NOTHING IS HARD-CODED. The row is chosen from the menu this run's own child
// published, and the comparand is that same row's resolved_model, read from the same
// reply. A claude release that renames a family, ships a new Fable, or changes what
// an alias resolves to therefore moves the expectation with the measurement instead
// of turning the assertion into a false pass — the failure mode a literal here would
// reintroduce, and the one interactive_stream_model_rejection_test.go's
// live-derived candidate exists to avoid on the negative path.
//
// The baseline turn is the instrument check, not decoration. Without it, a tree that
// ignored the settings change entirely would still pass whenever the picked row
// happened to resolve to the model the daemon already ran; asserting that the
// announced model MOVED, and moved to the picked row's resolution, is what
// distinguishes the mechanism from a coincidence. The row is selected against that
// live baseline for the same reason.
func TestInteractiveStreamFullIDRowFollowsItsFamily(t *testing.T) {
	h, convID := startStreamModalResolutionHarness(t, permissionDaemonModel)

	menu := liveModelMenu2447(t, h, convID)

	// Baseline: what serves a turn before anything is changed. Read from this
	// run's own child rather than from inbandModelTargets, so a stale table
	// cannot decide whether the measurement below is non-vacuous.
	sealSendMessage(t, h.phone, h.initSend, 1000, convID, "m-2447-baseline",
		fmt.Sprintf("Reply with one short word and nothing else. run=%d", time.Now().UnixNano()))
	baseline, seen := drainForAnnouncedModel(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
	if !seen {
		t.Fatal("#2447: the baseline turn emitted no model_announced frame; the measurement has no starting point")
	}
	if baseline.Model == "" {
		t.Fatal("#2447: the baseline model_announced frame named no model")
	}

	row := pickRewritableRow2447(t, menu, baseline.Model)
	t.Logf("#2447: baseline model %q; picked published row value %q (display %q) resolving to %q",
		baseline.Model, row.Value, row.DisplayName, row.ResolvedModel)

	// Model ONLY. A present effort or permission mode would still route in-band,
	// but naming one more field than the ticket changes would let an unrelated
	// rejection end the run before the model is ever delivered.
	picked := row.Value
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   1001,
		Type: protocol.TypeSetSessionSettings,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{
			SessionID: streamModalBootstrapUUID,
			Model:     &picked,
		}),
	})
	accepted := drainForCorrelatedEnvelope2281(t, h.phone, h.initRecv, 1001, familyAliasMenuBudget)
	if accepted.Type != protocol.TypeSessionSettingsUpdated {
		t.Fatalf("#2447: set_session_settings(%q) reply = %q %s, want %q — the daemon refused a value it published itself",
			picked, accepted.Type, accepted.Payload, protocol.TypeSessionSettingsUpdated)
	}

	sealSendMessage(t, h.phone, h.initSend, 1002, convID, "m-2447-after-pick",
		fmt.Sprintf("Reply with one short word and nothing else. run=%d", time.Now().UnixNano()))
	announced, seen := drainForAnnouncedModel(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
	if !seen {
		t.Fatal("#2447: the turn after the model pick emitted no model_announced frame")
	}

	if announced.Model == baseline.Model {
		t.Errorf("#2447 A1: the child reported model %q both before and after picking %q; "+
			"the settings change reached no live child at all", baseline.Model, picked)
	}
	if announced.Model != row.ResolvedModel {
		t.Errorf("#2447 A2: after picking the published row %q the child reported model %q, want %q — "+
			"that row's own resolved_model from this run's menu.\n"+
			"If A1 passed, the delivery worked and claude resolved the family alias pyry sent ELSEWHERE. "+
			"That voids the ticket's design rather than being a stale expectation here: a rewrite that "+
			"changes which model runs must not ship. Report what was measured (picked %q, alias family, "+
			"announced %q) and route the ticket back.",
			picked, announced.Model, row.ResolvedModel, picked, announced.Model)
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

// pickRewritableRow2447 returns the published row this test drives: one whose
// value pyry rewrites to a family alias, preferring a fable-family row (the
// ticket's named case) and falling back to any other so a Fable rename cannot void
// the instrument. Rows resolving to the model already running are skipped — the
// measurement below asserts that the announced model MOVED.
//
// The shape test is written out here rather than called from internal/sessions,
// and that is the point: familyAlias is unexported, and a live arm that asked the
// production code which rows it rewrites would agree with itself by construction.
//
// No row at all is FATAL, never a skip. Every row being a bare alias would mean
// claude stopped publishing the exact-id rows this whole ticket exists for — a
// fact someone must see, and a skip reads as a pass in a suite whose health is
// judged by its executed count.
func pickRewritableRow2447(t *testing.T, menu []protocol.ModelOption, baseline string) protocol.ModelOption {
	t.Helper()
	var fallback *protocol.ModelOption
	for i := range menu {
		row := menu[i]
		family, ok := rewritableFamily2447(row.Value)
		if !ok || row.ResolvedModel == "" || row.ResolvedModel == baseline {
			continue
		}
		if family == "fable" {
			return row
		}
		if fallback == nil {
			fallback = &menu[i]
		}
	}
	if fallback != nil {
		t.Logf("#2447: no usable fable-family row; falling back to %q", fallback.Value)
		return *fallback
	}
	t.Fatalf("#2447: no published row carries an exact model id that pyry would rewrite, resolves to something, "+
		"and differs from the running model %q. Either claude stopped publishing exact-id rows — which is the "+
		"ticket's premise gone and a fact to record rather than absorb — or every such row already resolves to "+
		"what is running. Menu: %+v", baseline, menu)
	return protocol.ModelOption{}
}

// rewritableFamily2447 reports the family a value would be rewritten to, and
// whether pyry rewrites it at all. It restates internal/sessions' rule
// independently: after any trailing bracket group is split off, the base must
// divide on "-" into "claude", a family of letters, and at least one further
// segment with every remaining segment all-digits.
func rewritableFamily2447(value string) (string, bool) {
	base := value
	if i := strings.IndexByte(value, '['); i >= 0 {
		if i == 0 || value[len(value)-1] != ']' {
			return "", false
		}
		base = value[:i]
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
	return parts[1], true
}
