package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// bridgeLen reads the bridge's live correlation-map size under its own lock — the
// regression guard for the unconditional-delete no-leak fix (#1080 MUST FIX).
func bridgeLen(b *streamApprovalBridge) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.byModal)
}

func bridgeAnswerable(t *testing.T, b *streamApprovalBridge, perm *permbridge.Registry, id string) bool {
	t.Helper()
	req, _ := perm.Lookup(id)
	return b.ApprovalAnswerable(id, req)
}

// lastModalShown decodes the most recent modal_shown envelope from a push log.
func lastModalShown(t *testing.T, pushes []recordedPush) protocol.ModalShownPayload {
	t.Helper()
	for i := len(pushes) - 1; i >= 0; i-- {
		if pushes[i].env.Type != protocol.TypeModalShown {
			continue
		}
		var p protocol.ModalShownPayload
		if err := json.Unmarshal(pushes[i].env.Payload, &p); err != nil {
			t.Fatalf("decode modal_shown payload: %v", err)
		}
		return p
	}
	t.Fatal("no modal_shown push found")
	return protocol.ModalShownPayload{}
}

type blockingModalResolve struct {
	*modalbridge.Registry
	entered chan struct{}
	release chan struct{}
}

func (r *blockingModalResolve) Resolve(id string) (modalbridge.Outstanding, bool) {
	close(r.entered)
	<-r.release
	return r.Registry.Resolve(id)
}

// parkApproval registers a parked approval in perm and returns the request and
// its Pending handle. A long timeout keeps permbridge's own timer from firing
// mid-test.
func parkApproval(t *testing.T, perm *permbridge.Registry, toolUseID, toolName string, input json.RawMessage) (permbridge.Request, *permbridge.Pending) {
	t.Helper()
	req := permbridge.Request{ToolName: toolName, Input: input, ToolUseID: toolUseID}
	p, err := perm.Register(toolUseID, req, time.Minute)
	if err != nil {
		t.Fatalf("Register(%q): %v", toolUseID, err)
	}
	return req, p
}

// TestStreamApprovalBridge_Surface_BroadcastsPermissionModal proves AC-1: Surface
// records the Outstanding, stores the correlation, and broadcasts exactly one
// modal_shown with the 4 permission option ids in claude display order and the
// reject-once deny-default — byte-compatible with today's clients by construction.
func TestStreamApprovalBridge_Surface_BroadcastsPermissionModal(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modal := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modal, bcast, func() string { return testConvID }, context.Background(), discardLogger())

	req, _ := parkApproval(t, perm, "tu-1", "Bash", json.RawMessage(`{"cmd":"ls"}`))
	req.DecisionReason = json.RawMessage(`{"rule":"outside_read_only"}`)
	req.DecisionReasonType = "future_reason_kind"
	req.BlockedPath = "/workspace/out"
	req.Description = "Write output"
	req.DefaultToNo = true
	req.AlwaysAllow = permbridge.ParseAlwaysAllow(json.RawMessage(`[`+
		`{"type":"addRules","behavior":"allow","rules":[{"toolName":"Bash"}]},`+
		`{"type":"addRules","behavior":"allow","rules":[{"toolName":"Read","ruleContent":"//src/**"}]}`+
		`]`), false)
	retire := bridge.Surface(req)
	if retire == nil {
		t.Fatal("Surface returned a nil retire closure")
	}

	if got := pushTypes(bcast.pushes); len(got) != 1 || got[0] != protocol.TypeModalShown {
		t.Fatalf("pushes = %v, want exactly one modal_shown", got)
	}
	p := lastModalShown(t, bcast.pushes)

	wantOpts := []string{
		string(turnevent.PermissionOptionKindAllowOnce),
		string(turnevent.PermissionOptionKindAllowAlways),
		string(turnevent.PermissionOptionKindRejectOnce),
		string(turnevent.PermissionOptionKindRejectAlways),
	}
	if len(p.Options) != len(wantOpts) {
		t.Fatalf("options = %d, want %d", len(p.Options), len(wantOpts))
	}
	for i, want := range wantOpts {
		if p.Options[i].ID != want {
			t.Errorf("option[%d].ID = %q, want %q", i, p.Options[i].ID, want)
		}
	}
	if p.DefaultOptionID != string(turnevent.PermissionOptionKindRejectOnce) {
		t.Errorf("DefaultOptionID = %q, want reject-once (fail-safe)", p.DefaultOptionID)
	}
	if p.Class != "permission" {
		t.Errorf("class = %q, want permission", p.Class)
	}
	if p.ConversationID != testConvID {
		t.Errorf("conversation_id = %q, want %q", p.ConversationID, testConvID)
	}
	if string(p.Reason) != string(req.DecisionReason) || p.ReasonType != req.DecisionReasonType ||
		p.BlockedPath != req.BlockedPath || p.Description != req.Description || p.DefaultToNo != req.DefaultToNo {
		t.Errorf("initial modal context = %+v, want parked request context %+v", p, req)
	}
	if !p.AlwaysAllow.Offered || !reflect.DeepEqual(p.AlwaysAllow.Rules, []string{"Bash", "Read(//src/**)"}) {
		t.Errorf("initial always_allow = %+v, want ordered offered rules", p.AlwaysAllow)
	}
	snapshot := modal.Snapshot()
	if len(snapshot) != 1 || !reflect.DeepEqual(snapshot[0], p) {
		t.Errorf("reconnect snapshot = %+v, want initial payload %+v", snapshot, p)
	}

	if n := bridgeLen(bridge); n != 1 {
		t.Errorf("byModal len = %d, want 1 (correlation stored)", n)
	}
	if _, ok := modal.Lookup(p.ModalID); !ok {
		t.Errorf("Outstanding %q not recorded in modalbridge", p.ModalID)
	}
}

func TestStreamApprovalBridge_Surface_EmptyContextIsNotDerivedFromInput(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modal := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modal, bcast, func() string { return testConvID }, context.Background(), discardLogger())
	req, _ := parkApproval(t, perm, "tu-empty-context", "Bash", json.RawMessage(
		`{"reason":{"from":"input"},"reason_type":"input-type","blocked_path":"input-path","description":"input-description","default_to_no":true}`,
	))
	retire := bridge.Surface(req)
	defer func() {
		perm.Resolve(req.ToolUseID, permbridge.Deny("test cleanup"))
		retire()
	}()

	payload := lastModalShown(t, bcast.pushes)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal modal_shown: %v", err)
	}
	for _, key := range []string{"reason", "reason_type", "blocked_path", "description", "default_to_no"} {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("zero-context modal derived %q from tool input: %s", key, raw)
		}
	}
	if payload.AlwaysAllow.Offered || payload.AlwaysAllow.Rules == nil || len(payload.AlwaysAllow.Rules) != 0 {
		t.Errorf("zero-context always_allow = %+v, want unavailable", payload.AlwaysAllow)
	}
}

// TestStreamApprovalBridge_Retire_NoopWhenNothingSurfaced proves the no-op retire
// the RNG-failure degrade returns is safe: a retire over a modalID that was never
// recorded (the crypto/rand drop path returns func(){}, or any absent id) deletes
// an absent key and finds no modalbridge entry to dismiss — no broadcast, no
// panic, clean state.
func TestStreamApprovalBridge_Retire_NoopWhenNothingSurfaced(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modal := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modal, bcast, func() string { return "" }, context.Background(), discardLogger())

	bridge.retire("never-surfaced")

	if len(bcast.pushes) != 0 {
		t.Errorf("pushes = %v, want none for an unsurfaced modal", pushTypes(bcast.pushes))
	}
	if n := bridgeLen(bridge); n != 0 {
		t.Errorf("byModal len = %d, want 0", n)
	}
}

// TestStreamApprovalBridge_ResolveStream_AllowEchoesInput proves AC-2's allow arm:
// ResolveStream(allow=true) resolves the parked completer with an Allow whose
// UpdatedInput equals the parked Input byte-for-byte.
func TestStreamApprovalBridge_ResolveStream_AllowEchoesInput(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modal := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modal, bcast, func() string { return "" }, context.Background(), discardLogger())

	input := json.RawMessage(`{"cmd":"ls -la","n":42}`)
	req, pending := parkApproval(t, perm, "tu-1", "Bash", input)
	bridge.Surface(req)
	modalID := lastModalShown(t, bcast.pushes).ModalID

	if handled := bridge.ResolveStream(modalID, true, false, reasonRemoteDeny); !handled {
		t.Fatal("ResolveStream(allow) handled = false, want true")
	}
	v := pending.Await()
	if v.Behavior != permbridge.BehaviorAllow {
		t.Errorf("behavior = %q, want allow", v.Behavior)
	}
	if !bytes.Equal(v.UpdatedInput, input) {
		t.Errorf("UpdatedInput = %s, want %s (byte-verbatim)", v.UpdatedInput, input)
	}
}

// TestStreamApprovalBridge_ResolveStream_Deny proves AC-2's deny arm:
// ResolveStream(allow=false) resolves the parked completer with a Deny carrying
// the fixed content-free reason and no leaked input.
func TestStreamApprovalBridge_ResolveStream_Deny(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modal := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modal, bcast, func() string { return "" }, context.Background(), discardLogger())

	req, pending := parkApproval(t, perm, "tu-1", "Bash", json.RawMessage(`{"cmd":"rm -rf /"}`))
	bridge.Surface(req)
	modalID := lastModalShown(t, bcast.pushes).ModalID

	if handled := bridge.ResolveStream(modalID, false, false, reasonRemoteDeny); !handled {
		t.Fatal("ResolveStream(deny) handled = false, want true")
	}
	v := pending.Await()
	if v.Behavior != permbridge.BehaviorDeny {
		t.Errorf("behavior = %q, want deny", v.Behavior)
	}
	if v.Message != reasonRemoteDeny {
		t.Errorf("message = %q, want %q", v.Message, reasonRemoteDeny)
	}
	if len(v.UpdatedInput) != 0 {
		t.Errorf("deny leaked UpdatedInput = %s, want empty", v.UpdatedInput)
	}
}

// TestStreamApprovalBridge_ResolveStream_UnknownModalID proves an unknown modalID
// is not a stream approval: handled=false (caller routes the keystroke arm) and no
// permbridge completer is mutated.
func TestStreamApprovalBridge_ResolveStream_UnknownModalID(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modal := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modal, bcast, func() string { return "" }, context.Background(), discardLogger())

	// Park an approval but never Surface it → no byModal entry.
	parkApproval(t, perm, "tu-1", "Bash", json.RawMessage(`{}`))

	if handled := bridge.ResolveStream("no-such-modal", true, false, reasonRemoteDeny); handled {
		t.Error("ResolveStream(unknown) handled = true, want false")
	}
	if _, ok := perm.Lookup("tu-1"); !ok {
		t.Error("permbridge completer resolved by an unknown-modalID ResolveStream")
	}
}

// TestStreamApprovalBridge_Retire_TimeoutPathBroadcastsDismissal proves AC-3's
// timeout/disconnect path: an unconsumed modal is retired with exactly one
// modal_dismissed broadcast, one fail-closed audit record (no modal body), the
// modalbridge entry consumed, and the correlation deleted (no leak).
func TestStreamApprovalBridge_Retire_TimeoutPathBroadcastsDismissal(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modal := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	logger, logBuf := auditLogger()
	bridge := newStreamApprovalBridge(perm, modal, bcast, func() string { return "" }, context.Background(), logger)

	req, _ := parkApproval(t, perm, "tu-1", "Bash", json.RawMessage(`{}`))
	retire := bridge.Surface(req)
	modalID := lastModalShown(t, bcast.pushes).ModalID
	bcast.pushes = nil // isolate the dismissal broadcast

	retire()

	if got := pushTypes(bcast.pushes); len(got) != 1 || got[0] != protocol.TypeModalDismissed {
		t.Fatalf("pushes = %v, want exactly one modal_dismissed", got)
	}
	if n := bridgeLen(bridge); n != 0 {
		t.Errorf("byModal len = %d after retire, want 0 (no leak)", n)
	}
	if _, ok := modal.Lookup(modalID); ok {
		t.Error("modal still outstanding after retire; Resolve must consume it")
	}
	recs := auditRecords(t, logBuf)
	if len(recs) != 1 {
		t.Fatalf("audit records = %d, want 1", len(recs))
	}
	if recs[0]["outcome"] != "denied_timeout" || recs[0]["source"] != "timeout" {
		t.Errorf("audit = {%v %v}, want {denied_timeout timeout}", recs[0]["outcome"], recs[0]["source"])
	}
}

// TestStreamApprovalBridge_Retire_AfterConsumeNoSecondDismissal proves AC-3's
// single-arbiter answer path: when the modalbridge entry was already consumed (as
// ResolveAnswer does before resolving the completer), retire deletes the
// correlation UNCONDITIONALLY (no leak) but broadcasts NO second dismissal and
// writes no audit — the modalbridge one-shot is the single dismissal arbiter.
func TestStreamApprovalBridge_Retire_AfterConsumeNoSecondDismissal(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modal := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	logger, logBuf := auditLogger()
	bridge := newStreamApprovalBridge(perm, modal, bcast, func() string { return "" }, context.Background(), logger)

	req, _ := parkApproval(t, perm, "tu-1", "Bash", json.RawMessage(`{}`))
	retire := bridge.Surface(req)
	modalID := lastModalShown(t, bcast.pushes).ModalID
	bcast.pushes = nil

	// Simulate ResolveAnswer's modalbridge consume before the completer resolves.
	if _, ok := modal.Resolve(modalID); !ok {
		t.Fatal("modal not present to consume")
	}

	retire()

	if got := pushTypes(bcast.pushes); len(got) != 0 {
		t.Errorf("pushes = %v after consumed-then-retire, want none (single arbiter)", got)
	}
	if n := bridgeLen(bridge); n != 0 {
		t.Errorf("byModal len = %d, want 0 (unconditional delete guards the leak)", n)
	}
	if recs := auditRecords(t, logBuf); len(recs) != 0 {
		t.Errorf("audit records = %d, want 0 on the answer path", len(recs))
	}
}

func TestStreamApprovalBridge_InteractionRequiredRefusalSurvivesConcurrentRetire(t *testing.T) {
	perm := permbridge.New()
	modalReg := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modalReg, bcast, func() string { return "" }, context.Background(), discardLogger())

	req, _ := parkApproval(t, perm, "tu-interaction-race", "Bash", json.RawMessage(`{"cmd":"true"}`))
	req.RequiresUserInteraction = true
	retire := bridge.Surface(req)
	modalID := lastModalShown(t, bcast.pushes).ModalID
	blocked := &blockingModalResolve{Registry: modalReg, entered: make(chan struct{}), release: make(chan struct{})}
	bridge.modal = blocked

	retired := make(chan struct{})
	go func() {
		retire()
		close(retired)
	}()
	<-blocked.entered

	resolver := newModalResolverV2(modalReg, &fakeKeystroker{}, discardLogger())
	resolver.streamApprovals = bridge
	if dismissal, ok := resolver.ResolveAnswer(modalID, string(turnevent.PermissionOptionKindAllowOnce), "token", eligibleDevice(t)); ok {
		t.Errorf("concurrent answer accepted during timeout retirement: %+v", dismissal)
	}

	close(blocked.release)
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("retire did not complete")
	}
	if got := pushTypes(bcast.pushes); len(got) != 2 || got[1] != protocol.TypeModalDismissed {
		t.Errorf("pushes = %v, want modal_shown then timeout modal_dismissed", got)
	}
}

// TestModalResolverV2_Answer_StreamAllow proves the ResolveAnswer stream arm: an
// eligible allow answer resolves the parked completer (Allow echoing the input),
// routes NO keystroke, audits allowed, and — with the modalbridge consumed by
// ResolveAnswer — leaves retire a no-op that adds no dismissal and leaks nothing.
func TestModalResolverV2_Answer_StreamAllow(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modalReg := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modalReg, bcast, func() string { return "" }, context.Background(), discardLogger())

	input := json.RawMessage(`{"cmd":"ls"}`)
	req, pending := parkApproval(t, perm, "tu-1", "Bash", input)
	retire := bridge.Surface(req)
	modalID := lastModalShown(t, bcast.pushes).ModalID

	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()
	r := newModalResolverV2(modalReg, kb, logger)
	r.streamApprovals = bridge

	allowOpt := string(turnevent.PermissionOptionKindAllowOnce)
	d, ok := r.ResolveAnswer(modalID, allowOpt, "tok", eligibleDevice(t))
	if !ok {
		t.Fatal("ResolveAnswer ok = false, want true")
	}
	if d.Outcome != allowOpt || d.Source != string(audit.SourceRemote) {
		t.Errorf("dismissal = %+v, want {%s remote}", d, allowOpt)
	}
	if !kb.routedNothing() {
		t.Errorf("keystroke routed on a stream answer: esc=%d trust=%d answers=%v", kb.escCalls, kb.trustCalls, kb.answerCalls)
	}

	v := pending.Await()
	if v.Behavior != permbridge.BehaviorAllow || !bytes.Equal(v.UpdatedInput, input) {
		t.Errorf("verdict = %+v, want allow echoing %s", v, input)
	}
	if recs := auditRecords(t, logBuf); len(recs) != 1 || recs[0]["outcome"] != "allowed" {
		t.Errorf("audit = %v, want one allowed record", recs)
	}

	// retire on the answered path: modalbridge already consumed → no second
	// dismissal, correlation still deleted.
	retire()
	if got := pushTypes(bcast.pushes); len(got) != 1 || got[0] != protocol.TypeModalShown {
		t.Errorf("pushes = %v, want only the modal_shown (no retire dismissal)", got)
	}
	if n := bridgeLen(bridge); n != 0 {
		t.Errorf("byModal len = %d after answer+retire, want 0 (no leak)", n)
	}
}

// TestModalResolverV2_Answer_StreamDeny proves the stream arm's deny path: an
// eligible reject answer resolves the completer to Deny with the fixed reason,
// routes no keystroke, and audits denied.
func TestModalResolverV2_Answer_StreamDeny(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modalReg := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modalReg, bcast, func() string { return "" }, context.Background(), discardLogger())

	req, pending := parkApproval(t, perm, "tu-1", "Bash", json.RawMessage(`{"cmd":"rm -rf /"}`))
	bridge.Surface(req)
	modalID := lastModalShown(t, bcast.pushes).ModalID

	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()
	r := newModalResolverV2(modalReg, kb, logger)
	r.streamApprovals = bridge

	rejectOpt := string(turnevent.PermissionOptionKindRejectOnce)
	if _, ok := r.ResolveAnswer(modalID, rejectOpt, "tok", eligibleDevice(t)); !ok {
		t.Fatal("ResolveAnswer ok = false, want true")
	}
	if !kb.routedNothing() {
		t.Errorf("keystroke routed on a stream deny: esc=%d answers=%v", kb.escCalls, kb.answerCalls)
	}

	v := pending.Await()
	if v.Behavior != permbridge.BehaviorDeny || v.Message != reasonRemoteDeny {
		t.Errorf("verdict = %+v, want deny with %q", v, reasonRemoteDeny)
	}
	if recs := auditRecords(t, logBuf); len(recs) != 1 || recs[0]["outcome"] != "denied" {
		t.Errorf("audit = %v, want one denied record", recs)
	}
}

// TestModalResolverV2_Answer_NonStreamRoutesKeystroke proves the tui path is
// preserved when a stream bridge is wired but the answered modal is NOT a stream
// approval (absent from byModal): ResolveStream reports false, so the safe-answer
// keystroke routes exactly as before #1080.
func TestModalResolverV2_Answer_NonStreamRoutesKeystroke(t *testing.T) {
	t.Parallel()

	perm := permbridge.New()
	modalReg := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modalReg, bcast, func() string { return "" }, context.Background(), discardLogger())

	// A modal recorded directly (interactive PTY path), NOT via Surface.
	modalID := recordPermissionModal(t, modalReg, secretModalBody)

	kb := &fakeKeystroker{}
	r := newModalResolverV2(modalReg, kb, discardLogger())
	r.streamApprovals = bridge

	allowOpt := string(turnevent.PermissionOptionKindAllowOnce)
	if _, ok := r.ResolveAnswer(modalID, allowOpt, "", eligibleDevice(t)); !ok {
		t.Fatal("ResolveAnswer ok = false, want true")
	}
	// allow-once is index 0 → 1-based keystroke "1".
	if len(kb.answerCalls) != 1 || kb.answerCalls[0] != "1" {
		t.Errorf("answerCalls = %v, want [\"1\"] (keystroke routed)", kb.answerCalls)
	}
}

// TestModalResolverV2_Answer_StreamGateDeniesBeforePermbridge proves the
// load-bearing security ordering: an ineligible (ungated or nil) device is denied
// BEFORE any permbridge contact — no completer mutation, no keystroke, the modal
// left outstanding for a legitimate local answer / deny-on-timeout, and the
// correlation untouched. Audited denied_unauthorized.
func TestModalResolverV2_Answer_StreamGateDeniesBeforePermbridge(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		dev  func(*testing.T) *devices.Device
	}{
		{"ungated", func(t *testing.T) *devices.Device { return testDevice(t) }},
		{"nil", func(*testing.T) *devices.Device { return nil }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			perm := permbridge.New()
			modalReg := modalbridge.New()
			bcast := oneInteractiveConn("c1")
			bridge := newStreamApprovalBridge(perm, modalReg, bcast, func() string { return "" }, context.Background(), discardLogger())

			req, _ := parkApproval(t, perm, "tu-1", "Bash", json.RawMessage(`{"cmd":"ls"}`))
			bridge.Surface(req)
			modalID := lastModalShown(t, bcast.pushes).ModalID

			kb := &fakeKeystroker{}
			logger, logBuf := auditLogger()
			r := newModalResolverV2(modalReg, kb, logger)
			r.streamApprovals = bridge

			allowOpt := string(turnevent.PermissionOptionKindAllowOnce)
			d, ok := r.ResolveAnswer(modalID, allowOpt, "", tc.dev(t))
			if ok {
				t.Error("ResolveAnswer ok = true for an ineligible device, want false")
			}
			if d != (relay.ModalDismissal{}) {
				t.Errorf("dismissal = %+v, want zero", d)
			}
			if !kb.routedNothing() {
				t.Error("keystroke routed for an ineligible device")
			}
			if _, stillParked := perm.Lookup("tu-1"); !stillParked {
				t.Error("permbridge completer resolved by an ineligible answer (gate must precede permbridge)")
			}
			if _, ok := modalReg.Lookup(modalID); !ok {
				t.Error("modal consumed despite gate denial (gate must precede consume)")
			}
			if n := bridgeLen(bridge); n != 1 {
				t.Errorf("byModal len = %d, want 1 (gate denial leaves the correlation)", n)
			}
			recs := auditRecords(t, logBuf)
			if len(recs) != 1 || recs[0]["outcome"] != "denied_unauthorized" {
				t.Errorf("audit = %v, want one denied_unauthorized record", recs)
			}
		})
	}
}

// TestStreamApproval_NoBodyLeakInLogs proves the bridge's log discipline: driving
// a full park → surface (with a failing Push to exercise the push-drop log) →
// answer → retire leaks neither the tool name nor the tool input into any log
// field.
func TestStreamApproval_NoBodyLeakInLogs(t *testing.T) {
	t.Parallel()

	const secretTool = "SECRET-TOOL-NAME-9999"
	const secretInput = "SECRET-INPUT-BYTES-7777"
	const secretContext = "SECRET-CONTEXT-BYTES-5555"
	const secretSuggestion = "SECRET-SUGGESTION-BYTES-3333"

	perm := permbridge.New()
	modalReg := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bcast.pushErr = map[string]error{"c1": errors.New("push failed")}
	logger, logBuf := auditLogger()
	bridge := newStreamApprovalBridge(perm, modalReg, bcast, func() string { return "" }, context.Background(), logger)

	input := json.RawMessage(`{"cmd":"` + secretInput + `"}`)
	req, _ := parkApproval(t, perm, "tu-1", secretTool, input)
	req.DecisionReason = json.RawMessage(`{"detail":"` + secretContext + `"}`)
	req.DecisionReasonType = secretContext + "-type"
	req.BlockedPath = "/" + secretContext
	req.Description = secretContext + "-description"
	req.AlwaysAllow = permbridge.ParseAlwaysAllow(json.RawMessage(`[{"type":"addRules","behavior":"allow","rules":[{"toolName":"`+secretSuggestion+`"}]}]`), false)
	retire := bridge.Surface(req)
	modalID := lastModalShown(t, bcast.pushes).ModalID

	kb := &fakeKeystroker{}
	r := newModalResolverV2(modalReg, kb, logger)
	r.streamApprovals = bridge
	if _, ok := r.ResolveAnswer(modalID, string(turnevent.PermissionOptionKindRejectOnce), "", eligibleDevice(t)); !ok {
		t.Fatal("ResolveAnswer ok = false, want true")
	}
	retire()

	s := logBuf.String()
	if strings.Contains(s, secretTool) {
		t.Error("tool name leaked into a log field")
	}
	if strings.Contains(s, secretInput) {
		t.Error("tool input leaked into a log field")
	}
	if strings.Contains(s, secretContext) {
		t.Error("permission context leaked into a log field")
	}
	if strings.Contains(s, secretSuggestion) {
		t.Error("permission suggestion leaked into a log field")
	}
}

// TestStreamApproval_RoundTrip is the AC-4 component-level round-trip: request →
// Surface (modal_shown) → ResolveAnswer (modal_answer) → verdict, for both an
// allow and a deny answer, asserting the permbridge verdict returned to claude.
func TestStreamApproval_RoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		optionID     string
		alwaysAllow  bool
		offer        json.RawMessage
		wantBehavior string
		wantAllow    bool
		wantUpdates  string
	}{
		{name: "plain allow", optionID: string(turnevent.PermissionOptionKindAllowOnce), wantBehavior: permbridge.BehaviorAllow, wantAllow: true},
		{
			name:        "always allow offered rules",
			optionID:    string(turnevent.PermissionOptionKindAllowAlways),
			alwaysAllow: true,
			offer: json.RawMessage(`[
				{"type":"setMode","mode":"acceptEdits","destination":"session"},
				{"type":"addRules","rules":[{"toolName":"Bash"},{"toolName":"Read","ruleContent":"//src/**"}],"behavior":"allow","destination":"userSettings"},
				{"type":"addDirectories","directories":["/test"],"destination":"session"},
				{"type":"addRules","rules":[{"toolName":"Write","ruleContent":"//tmp/**"}],"behavior":"allow","destination":"localSettings"}
			]`),
			wantBehavior: permbridge.BehaviorAllow,
			wantAllow:    true,
			wantUpdates:  `[{"type":"addRules","rules":[{"toolName":"Bash"},{"toolName":"Read","ruleContent":"//src/**"}],"behavior":"allow","destination":"session"},{"type":"addRules","rules":[{"toolName":"Write","ruleContent":"//tmp/**"}],"behavior":"allow","destination":"session"}]`,
		},
		{name: "always allow unavailable is plain allow", optionID: string(turnevent.PermissionOptionKindAllowAlways), alwaysAllow: true, wantBehavior: permbridge.BehaviorAllow, wantAllow: true},
		{name: "deny ignores always allow", optionID: string(turnevent.PermissionOptionKindRejectOnce), alwaysAllow: true, offer: json.RawMessage(`[{"type":"addRules","rules":[{"toolName":"Bash"}],"behavior":"allow"}]`), wantBehavior: permbridge.BehaviorDeny},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			perm := permbridge.New()
			modalReg := modalbridge.New()
			bcast := oneInteractiveConn("c1")
			bridge := newStreamApprovalBridge(perm, modalReg, bcast, func() string { return "" }, context.Background(), discardLogger())

			input := json.RawMessage(`{"cmd":"ls -la"}`)
			req := permbridge.Request{
				ToolName:    "Bash",
				Input:       input,
				ToolUseID:   "tu-1",
				AlwaysAllow: permbridge.ParseAlwaysAllow(tc.offer, false),
			}
			pending, err := perm.Register(req.ToolUseID, req, time.Minute)
			if err != nil {
				t.Fatalf("Register: %v", err)
			}

			// PARK → modal_shown
			retire := bridge.Surface(req)
			shown := lastModalShown(t, bcast.pushes)
			if len(shown.Options) != 4 {
				t.Fatalf("modal_shown options = %d, want 4", len(shown.Options))
			}

			// ANSWER → verdict
			kb := &fakeKeystroker{}
			r := newModalResolverV2(modalReg, kb, discardLogger())
			r.streamApprovals = bridge
			d, ok := r.ResolveAnswerWithAlwaysAllow(shown.ModalID, tc.optionID, "tok", tc.alwaysAllow, eligibleDevice(t))
			if !ok {
				t.Fatal("ResolveAnswer ok = false, want true")
			}
			if d.Outcome != tc.optionID {
				t.Errorf("dismissal outcome = %q, want the answered option %q", d.Outcome, tc.optionID)
			}
			if !kb.routedNothing() {
				t.Error("keystroke routed on a stream round-trip answer")
			}

			v := pending.Await()
			if v.Behavior != tc.wantBehavior {
				t.Errorf("verdict behavior = %q, want %q", v.Behavior, tc.wantBehavior)
			}
			if tc.wantAllow {
				if !bytes.Equal(v.UpdatedInput, input) {
					t.Errorf("allow UpdatedInput = %s, want %s (byte-verbatim)", v.UpdatedInput, input)
				}
				if string(v.UpdatedPermissions) != tc.wantUpdates {
					t.Errorf("allow UpdatedPermissions = %s, want %s", v.UpdatedPermissions, tc.wantUpdates)
				}
			} else if v.Message != reasonRemoteDeny {
				t.Errorf("deny message = %q, want %q", v.Message, reasonRemoteDeny)
			}

			// retire on the answered path is a no-op: the modalbridge entry was
			// consumed by ResolveAnswer, and the correlation is unconditionally
			// deleted.
			retire()
			if n := bridgeLen(bridge); n != 0 {
				t.Errorf("byModal len = %d after answer+retire, want 0 (no leak)", n)
			}
			if _, still := modalReg.Lookup(shown.ModalID); still {
				t.Error("modal still outstanding after the round-trip")
			}
			if _, ok := r.ResolveAnswerWithAlwaysAllow(shown.ModalID, tc.optionID, "tok-replay", true, eligibleDevice(t)); ok {
				t.Error("replayed answer resolved a consumed modal")
			}
		})
	}
}

// --- #1919: which conversation a parked approval belongs to -------------------

// testConvIDC is a third valid conversation id, distinct from testConvID
// (interactive_turn_v2_test.go) and testConvIDB (stream_turn_drain_test.go). The
// report fixture's resolver knows it and the tracker never sees it, which is the
// "known to the daemon, never observed" negative row.
const testConvIDC = "33333333-3333-4333-8333-333333333333"

// approvalReport is the #1919 fixture, now serving both parked-approval reports:
// the bridge whose byModal holds the parked correlations, the tracker that answers
// ApprovalParked's membership question, and the two registries the park path runs
// through. #1915's report reads the correlation and the broadcaster instead, so
// bcast is the only knob its tests turn (see conns) and the tracker half is inert
// for them.
//
// The two halves are joined the way relay.go joins them — the method value
// assigned AFTER construction — so newStreamApprovalBridge's 14 call sites stay
// untouched. Note what that assignment is not: it is not a nil-receiver guard on
// ToolCallInFlight, which #1917 refused on the record. Here the tracker is always
// live; the absent-dependency arm is the separate PTY-mode case.
type approvalReport struct {
	bridge *streamApprovalBridge
	tr     *turnBusyTracker
	perm   *permbridge.Registry
	bcast  *fakeInteractiveBcast
}

// newApprovalReport builds that fixture. activeConv is the follow-active cursor
// value the bridge stamps on the modals it raises; the report must not read it,
// which is what TestStreamApprovalBridge_ApprovalParked_IsConversationKeyedNotCursorKeyed
// drives it with.
func newApprovalReport(t *testing.T, activeConv string, logger *slog.Logger) approvalReport {
	t.Helper()
	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{
		"sess-a": testConvID,
		"sess-b": testConvIDB,
		"sess-c": testConvIDC,
	}), discardLogger())
	perm := permbridge.New()
	bcast := oneInteractiveConn("c1")
	bridge := newStreamApprovalBridge(perm, modalbridge.New(), bcast, func() string { return activeConv }, context.Background(), logger)
	bridge.toolCallInFlight = tr.ToolCallInFlight
	return approvalReport{bridge: bridge, tr: tr, perm: perm, bcast: bcast}
}

// park runs the production park path for toolUseID — permbridge.Register, then
// the bridge's Surface, which is what writes the modal_id ⇄ tool_use_id
// correlation the report reads — and returns the retire closure the control
// server defers.
func (f approvalReport) park(t *testing.T, toolUseID string) (retire func()) {
	t.Helper()
	req, _ := parkApproval(t, f.perm, toolUseID, "Read", json.RawMessage(`{"file":"x"}`))
	return f.bridge.Surface(req)
}

// AC1: the report is keyed by the conversation the parked call is in flight on,
// and that key is NOT the follow-active cursor.
//
// The cursor is pointed at B, the conversation that must report negative, and B
// is itself busy with its own turn and its own tool call — it simply has no
// approval parked. Both details are load-bearing. An implementation that stamped
// activeConv() at Surface time reports these two answers exactly inverted, and one
// that answered len(byModal) > 0 reports B positive; without this fixture both
// rejected designs pass.
func TestStreamApprovalBridge_ApprovalParked_IsConversationKeyedNotCursorKeyed(t *testing.T) {
	t.Parallel()

	f := newApprovalReport(t, testConvIDB, discardLogger())
	f.tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})
	f.tr.observe("sess-b", turnevent.ToolStart{ToolCallID: "tu-b1", Title: "Read"})
	f.park(t, "tu-a1")

	if !f.bridge.ApprovalParked(testConvID) {
		t.Error("ApprovalParked(A) = false with A's own tool call parked on a human")
	}
	if f.bridge.ApprovalParked(testConvIDB) {
		t.Error("ApprovalParked(B) = true; B is busy with its own turn but has no approval parked")
	}
}

// AC2: the conversation reports negative again once its approvals resolve, and
// only after the LAST of them does.
//
// It drives retire and deliberately does NOT reconstruct the four terminal paths
// the criterion names. A client's answer, the deadline, a lost caller and daemon
// shutdown are four CALLERS of one deleter: retire is the sole correlation
// deleter, its delete is unconditional, and it takes no path parameter — so there
// is no mutant a per-caller fixture reddens that this one does not.
func TestStreamApprovalBridge_ApprovalParked_NegativeOnlyAfterTheLastRetire(t *testing.T) {
	t.Parallel()

	f := newApprovalReport(t, "", discardLogger())
	f.tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})
	f.tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a2", Title: "Read"})
	retireFirst := f.park(t, "tu-a1")
	retireSecond := f.park(t, "tu-a2")

	if !f.bridge.ApprovalParked(testConvID) {
		t.Fatal("ApprovalParked(A) = false with two approvals parked")
	}
	retireFirst()
	if !f.bridge.ApprovalParked(testConvID) {
		t.Error("ApprovalParked(A) = false after one of two approvals resolved; the second is still parked")
	}
	retireSecond()
	if f.bridge.ApprovalParked(testConvID) {
		t.Error("ApprovalParked(A) = true after the last approval resolved")
	}
}

// AC2, the tracker half: the report is a conjunction across two membership sets,
// so it goes negative when the CALL side empties even while the correlation side
// is still populated.
//
// The turn's close sweeps A's in-flight calls (inflight moves with busy under one
// setBusy acquisition) while nothing retires the approval, and negative is the
// fail-closed direction for a delivery hold. Nothing else in the suite pins this
// half — the other fixtures all move the byModal side — and asserting bridgeLen is
// still non-zero is what stops the expectation being met by an accidental
// correlation delete.
func TestStreamApprovalBridge_ApprovalParked_GoesNegativeWhenTheCallEnds(t *testing.T) {
	t.Parallel()

	f := newApprovalReport(t, "", discardLogger())
	f.tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})
	f.park(t, "tu-a1")
	if !f.bridge.ApprovalParked(testConvID) {
		t.Fatal("ApprovalParked(A) = false with the call in flight and the approval parked")
	}

	f.tr.observe("sess-a", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	if f.bridge.ApprovalParked(testConvID) {
		t.Error("ApprovalParked(A) = true after A's turn closed and swept its in-flight calls")
	}
	if n := bridgeLen(f.bridge); n == 0 {
		t.Error("byModal is empty; the negative above came from the correlation side, not the tracker side")
	}
}

// AC5: the report emits nothing. Every diagnostic worth logging from it would
// carry either a conversation id — which clearForSession deliberately withholds as
// a routing key treated as sensitive — or a tool_use id, so the correct count of
// log statements inside ApprovalParked is zero.
//
// The buffer is reset after Surface so the assertion is scoped to the report and
// cannot be reddened by the surrounding wiring, and both a positive and a negative
// conversation are asked so neither arm can log.
func TestStreamApprovalBridge_ApprovalParked_LogsNothing(t *testing.T) {
	t.Parallel()

	logger, logBuf := auditLogger()
	f := newApprovalReport(t, "", logger)
	f.tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})
	f.park(t, "tu-a1")

	logBuf.Reset()
	if !f.bridge.ApprovalParked(testConvID) {
		t.Fatal("ApprovalParked(A) = false with the approval parked; the assertion below would be vacuous")
	}
	if f.bridge.ApprovalParked(testConvIDB) {
		t.Fatal("ApprovalParked(B) = true with nothing parked for B")
	}

	if s := logBuf.String(); s != "" {
		t.Errorf("ApprovalParked wrote to the log: %s", s)
	}
}

// AC3: unknown, never-seen and empty values of EITHER id reach the same negative
// answer, so the report is an existence oracle for neither conversation nor
// approval ids.
//
// One fixture holds every row, including a LIVE POSITIVE CONTROL on A, so no row
// can pass merely by the report being universally negative. B carries the two
// approval-id negatives: it is busy with its own turn, and the correlations parked
// alongside A's name a call the tracker never observed and a call with an empty
// tool_use id.
//
// Two honesty notes. The empty-ToolUseID correlation is NOT production-reachable
// through the approve lane — permbridge.Register refuses an empty id with
// ErrDuplicateID and the control server returns on that before Surface runs — so
// it is built by calling Surface directly and is a POSTURE PIN: it asserts the
// report cannot be turned into an oracle if that entry ever becomes reachable, not
// that a live bug exists. And the "by the same path" half of the criterion is a
// structural claim about the implementation — the absence of an id-specific branch
// — which no fixture can distinguish, because inserting
// `if conversationID == "" { return false }` is an equivalent mutant that changes
// no answer below. The rows pin the answers; the absent branch is read in
// ApprovalParked.
func TestStreamApprovalBridge_ApprovalParked_NegativesCollapse(t *testing.T) {
	t.Parallel()

	const foreignConv = "99999999-9999-4999-8999-999999999999"

	f := newApprovalReport(t, "", discardLogger())
	f.tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})
	f.tr.observe("sess-b", turnevent.ToolStart{ToolCallID: "tu-b1", Title: "Read"})
	f.park(t, "tu-a1")
	f.park(t, "tu-never-observed")
	f.bridge.Surface(permbridge.Request{ToolName: "Read", ToolUseID: ""})

	tests := []struct {
		name string
		conv string
		want bool
	}{
		{"the control: parked, and the call is in flight", testConvID, true},
		{"a conversation neither the resolver nor the tracker ever saw", foreignConv, false},
		{"a conversation the resolver knows but the tracker never saw", testConvIDC, false},
		{"the empty conversation id", "", false},
		{"busy with its own turn; the parked ids name a never-observed call and an empty one", testConvIDB, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := f.bridge.ApprovalParked(tc.conv); got != tc.want {
				t.Errorf("ApprovalParked(%q) = %v, want %v", tc.conv, got, tc.want)
			}
		})
	}
}

// The absent dependency answers negative WITHOUT calling out, which is what keeps
// the daemon's PTY mode semantically unchanged: the bridge is built under
// w.approvals != nil and approvals is minted unconditionally at the composition
// root, while the tracker exists only alongside streamSink — two different
// discriminants, so the bridge is live there and holds no tracker.
func TestStreamApprovalBridge_ApprovalParked_NoTrackerReportsNegative(t *testing.T) {
	t.Parallel()

	f := newApprovalReport(t, "", discardLogger())
	f.tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})
	f.park(t, "tu-a1")
	if !f.bridge.ApprovalParked(testConvID) {
		t.Fatal("ApprovalParked(A) = false with the tracker wired; the assertion below would be vacuous")
	}

	f.bridge.toolCallInFlight = nil

	if f.bridge.ApprovalParked(testConvID) {
		t.Error("ApprovalParked(A) = true with no tracker wired; PTY mode must report negative")
	}
}

// --- #1915: whether a parked approval still has anyone able to answer it ------

// conns replaces the fixture broadcaster's scripted snapshot with one fresh entry,
// so the next ActiveConns call sees exactly cs (no argument ⇒ nobody connected).
//
// fakeInteractiveBcast consumes one entry per call and reuses the last once the
// sequence is exhausted, and Surface's own broadcast has already consumed one
// before any report call — so a report test assigns the steady-state entry
// immediately before the call it is about. A scripted multi-entry sequence would
// be off by one and drift further with every park.
func (f approvalReport) conns(cs ...relay.ActiveConn) {
	f.bcast.snapshots = [][]relay.ActiveConn{cs}
}

// AC1: for one still-parked approval the report is positive while an
// interactive-capable client is connected and negative for that SAME approval
// once none is — which is what tells "a human has not got to it yet" apart from
// "nobody is connected".
//
// Asserting the correlation is still populated after the negative is load-bearing:
// it proves the negative came from the connectivity conjunct rather than from the
// correlation quietly going away. Nothing is retired here, and without that
// assertion an implementation that dropped the correlation would pass.
func TestStreamApprovalBridge_ApprovalAnswerable_NegativeOnlyWhenNobodyIsConnected(t *testing.T) {
	t.Parallel()

	f := newApprovalReport(t, "", discardLogger())
	f.park(t, "tu-a1")

	if !bridgeAnswerable(t, f.bridge, f.perm, "tu-a1") {
		t.Error("ApprovalAnswerable = false with the approval parked and an interactive client connected")
	}

	f.conns() // everybody disconnected; nothing retired

	if bridgeAnswerable(t, f.bridge, f.perm, "tu-a1") {
		t.Error("ApprovalAnswerable = true for a still-parked approval with nobody connected")
	}
	if n := bridgeLen(f.bridge); n == 0 {
		t.Error("byModal is empty; the negative above came from the correlation side, not the connectivity side")
	}
}

func TestStreamApprovalBridge_ApprovalAnswerable_UsesCurrentReusedIDEligibility(t *testing.T) {
	for _, tc := range []struct {
		name           string
		firstRequired  bool
		secondRequired bool
		want           bool
	}{
		{"ordinary then interaction-required", false, true, false},
		{"interaction-required then ordinary", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApprovalReport(t, "", discardLogger())
			first := permbridge.Request{ToolName: "Bash", ToolUseID: "reused", RequiresUserInteraction: tc.firstRequired}
			firstPending, err := f.perm.Register(first.ToolUseID, first, time.Minute)
			if err != nil {
				t.Fatalf("register first: %v", err)
			}
			firstRetire := f.bridge.Surface(first)
			if !f.perm.Resolve(first.ToolUseID, permbridge.Deny("first done")) {
				t.Fatal("resolve first = false")
			}
			if got := firstPending.Await(); got.Behavior != permbridge.BehaviorDeny {
				t.Fatalf("first verdict = %+v", got)
			}

			second := permbridge.Request{ToolName: "Bash", ToolUseID: "reused", RequiresUserInteraction: tc.secondRequired}
			if _, err := f.perm.Register(second.ToolUseID, second, time.Minute); err != nil {
				t.Fatalf("register second: %v", err)
			}
			secondRetire := f.bridge.Surface(second)

			if got := f.bridge.ApprovalAnswerable(second.ToolUseID, second); got != tc.want {
				t.Errorf("ApprovalAnswerable current generation = %v, want %v", got, tc.want)
			}
			f.perm.Resolve(second.ToolUseID, permbridge.Deny("cleanup"))
			secondRetire()
			firstRetire()
		})
	}
}

// AC2: a connected client that never negotiated the interactive capability does
// not count as able to answer — the same #607 gate broadcast applies before it
// pushes a modal_shown anyone could answer.
//
// The three snapshots are asked in sequence on ONE goroutine, not as parallel
// subtests: fakeInteractiveBcast carries no mutex and its scripted snapshot is
// rewritten between the calls. Row 2 is what makes row 1 non-vacuous — the only
// thing that changed is the flag on the same conn id — and row 3 kills an inverted
// gate that answers negative on the first non-interactive conn, which rows 1 and 2
// alone leave green.
func TestStreamApprovalBridge_ApprovalAnswerable_NonInteractiveClientCannotAnswer(t *testing.T) {
	t.Parallel()

	f := newApprovalReport(t, "", discardLogger())
	f.park(t, "tu-a1")

	tests := []struct {
		name     string
		snapshot []relay.ActiveConn
		want     bool
	}{
		{"one connected client, interactive never negotiated", []relay.ActiveConn{{ConnID: "c1"}}, false},
		{"the same client, interactive negotiated", []relay.ActiveConn{{ConnID: "c1", Interactive: true}}, true},
		{"a non-interactive client alongside an interactive one", []relay.ActiveConn{{ConnID: "c1"}, {ConnID: "c2", Interactive: true}}, true},
	}
	for _, tc := range tests {
		f.conns(tc.snapshot...)
		if got := bridgeAnswerable(t, f.bridge, f.perm, "tu-a1"); got != tc.want {
			t.Errorf("%s: ApprovalAnswerable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// AC3: unknown, never-surfaced, already-resolved and empty approval ids all reach
// the same negative answer, so the report does not become an existence oracle for
// approval ids.
//
// One fixture holds every row with the interactive conn connected throughout (the
// fixture's default snapshot, reused in steady state) and a LIVE POSITIVE CONTROL,
// so no row can pass merely by the report being universally negative — a negative
// reached with nobody connected would prove nothing about id handling. The rows run
// sequentially on one goroutine for the reason given on conns.
//
// Three honesty notes. The never-surfaced row is production-reachable: it is
// exactly the state Surface's modal.Record failure path leaves behind — no
// correlation stored, nothing broadcast, claude left to time out to deny — so
// simply not calling Surface reproduces it, with no RNG injection. The
// already-resolved row drives retire and deliberately does NOT reconstruct the four
// terminal paths: a client's answer, the deadline, a lost caller and shutdown are
// four CALLERS of one deleter, retire's delete is unconditional and takes no path
// parameter, so there is no mutant a per-caller fixture reddens that this one does
// not. And the empty row asserts against a fixture with NO empty-ToolUseID
// correlation planted, unlike ApprovalParked's table, which plants one harmlessly
// because ToolCallInFlight refuses an empty tool-call id anyway. Here a planted
// empty correlation would MATCH the scan and flip this row positive;
// permbridge.Register is what holds the invariant, refusing an empty id before the
// control server can reach Surface.
//
// The "by the same path" half of the criterion is a structural claim about the
// implementation — the absence of an id-specific branch — which no fixture can
// distinguish, because inserting `if approvalID == "" { return false }` is an
// equivalent mutant that changes no answer below. The rows pin the answers; the
// absent branch is read in ApprovalAnswerable.
func TestStreamApprovalBridge_ApprovalAnswerable_NegativesCollapse(t *testing.T) {
	t.Parallel()

	f := newApprovalReport(t, "", discardLogger())
	f.park(t, "tu-a1")
	parkApproval(t, f.perm, "tu-unsurfaced", "Read", json.RawMessage(`{"file":"x"}`))
	retireResolved := f.park(t, "tu-a2")
	retireResolved()

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"the control: parked and surfaced", "tu-a1", true},
		{"an id that was never registered anywhere", "tu-never-registered", false},
		{"parked in permbridge but never surfaced", "tu-unsurfaced", false},
		{"already resolved", "tu-a2", false},
		{"the empty approval id", "", false},
	}
	for _, tc := range tests {
		if got := bridgeAnswerable(t, f.bridge, f.perm, tc.id); got != tc.want {
			t.Errorf("%s: ApprovalAnswerable(%q) = %v, want %v", tc.name, tc.id, got, tc.want)
		}
	}
}

// AC5: the report emits nothing at any level. Every diagnostic worth emitting from
// it would carry a tool_use id, a conn id, or the fact that a specific approval is
// outstanding — all withheld by the bridge's own SECURITY block — so the correct
// count of log statements inside ApprovalAnswerable is zero.
//
// The buffer is reset after the park so the assertion is scoped to the report and
// cannot be reddened by Surface's own broadcast, and auditLogger captures Debug, so
// a line added at the lowest level still reddens it. The positive arm comes first:
// a universally-negative implementation would make the assertion vacuous.
func TestStreamApprovalBridge_ApprovalAnswerable_LogsNothing(t *testing.T) {
	t.Parallel()

	logger, logBuf := auditLogger()
	f := newApprovalReport(t, "", logger)
	f.park(t, "tu-a1")

	logBuf.Reset()
	if !bridgeAnswerable(t, f.bridge, f.perm, "tu-a1") {
		t.Fatal("ApprovalAnswerable = false with the approval parked and a client connected; the assertion below would be vacuous")
	}
	if bridgeAnswerable(t, f.bridge, f.perm, "tu-never-registered") {
		t.Fatal("ApprovalAnswerable = true for an id that was never parked")
	}
	f.conns()
	if bridgeAnswerable(t, f.bridge, f.perm, "tu-a1") {
		t.Fatal("ApprovalAnswerable = true with nobody connected")
	}

	if s := logBuf.String(); s != "" {
		t.Errorf("ApprovalAnswerable wrote to the log: %s", s)
	}
}

// --- #1973: the question arm of the shared approval surfacer ------------------

// questionInput builds one in-contract AskUserQuestion tool input whose four
// claude-authored strings are the caller's, so a content assertion and the leak
// probe drive the same shape with different values. Two questions — the first
// multi-select with three options, the second single-select with two — sit inside
// questionbridge's 1-4 questions / 2-4 options bounds and keep a flattened,
// reordered or multi-select-dropping rebuild distinguishable from a faithful one.
func questionInput(t *testing.T, text, header, label, desc string) json.RawMessage {
	t.Helper()
	in := map[string]any{
		"questions": []any{
			map[string]any{
				"question":    text,
				"header":      header,
				"multiSelect": true,
				"options": []any{
					map[string]any{"label": label, "description": desc},
					map[string]any{"label": "second", "description": "the second option"},
					map[string]any{"label": "third", "description": "the third option"},
				},
			},
			map[string]any{
				"question":    "which branch should it target?",
				"header":      "Branch",
				"multiSelect": false,
				"options": []any{
					map[string]any{"label": "main", "description": "the default branch"},
					map[string]any{"label": "next", "description": "the release train"},
				},
			},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal question input: %v", err)
	}
	return b
}

// questionFixture is the question arm's bridge fixture: a bridge with the batch
// registry wired the way relay.go's single production site assigns it — AFTER
// construction, so newStreamApprovalBridge's call sites stay untouched. It hands
// back the registry so a test can read what was parked without going through the
// wire.
type questionFixture struct {
	bridge *streamApprovalBridge
	perm   *permbridge.Registry
	modal  *modalbridge.Registry
	qreg   *questionbridge.Registry
	bcast  *fakeInteractiveBcast
	pushed chan struct{} // one signal per recorded push; see dismissalRecorder
}

func newQuestionFixture(t *testing.T, convID string, logger *slog.Logger) questionFixture {
	t.Helper()
	perm := permbridge.New()
	modal := modalbridge.New()
	qreg := questionbridge.New()
	bcast := oneInteractiveConn("c1")
	rec := &dismissalRecorder{inner: bcast, pushed: make(chan struct{}, 8)}
	bridge := newStreamApprovalBridge(perm, modal, rec, func() string { return convID }, context.Background(), logger)
	bridge.questions = qreg
	return questionFixture{bridge: bridge, perm: perm, modal: modal, qreg: qreg, bcast: bcast, pushed: rec.pushed}
}

// dismissalRecorder wraps the fixture's broadcaster so a test can wait for a
// fan-out RefuseQuestion runs on its OWN goroutine (#1990). The signal is sent
// after the inner Push returns, so a test that receives it has a happens-before
// edge covering everything that goroutine did — which is what makes the inner
// recorder's plain slice safe to read afterwards. The send is non-blocking: the
// synchronous Surface/retire paths push through here too and no test waits on
// their signals.
type dismissalRecorder struct {
	inner  *fakeInteractiveBcast
	pushed chan struct{}
}

func (d *dismissalRecorder) ActiveConns(ctx context.Context) []relay.ActiveConn {
	return d.inner.ActiveConns(ctx)
}

func (d *dismissalRecorder) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	err := d.inner.Push(ctx, connID, env)
	select {
	case d.pushed <- struct{}{}:
	default:
	}
	return err
}

// isolate drops the pushes recorded so far AND any pending fan-out signal, so a
// later waitPush cannot be satisfied by an earlier frame's push.
func (f questionFixture) isolate() {
	f.bcast.pushes = nil
	for {
		select {
		case <-f.pushed:
		default:
			return
		}
	}
}

// waitPush blocks until the detached dismissal fan-out has pushed, or fails the
// test. The generous deadline is a hang-catcher, not a timing assumption.
func waitPush(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the dismissal fan-out")
	}
}

// questionLen reads the bridge's live batch-correlation size under its own lock,
// bridgeLen's sibling for byQuestion.
func questionLen(b *streamApprovalBridge) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.byQuestion)
}

// lastQuestionShown decodes the most recent question_shown envelope, mirroring
// lastModalShown.
func lastQuestionShown(t *testing.T, pushes []recordedPush) protocol.QuestionShownPayload {
	t.Helper()
	for i := len(pushes) - 1; i >= 0; i-- {
		if pushes[i].env.Type != protocol.TypeQuestionShown {
			continue
		}
		var p protocol.QuestionShownPayload
		if err := json.Unmarshal(pushes[i].env.Payload, &p); err != nil {
			t.Fatalf("decode question_shown payload: %v", err)
		}
		return p
	}
	t.Fatal("no question_shown push found")
	return protocol.QuestionShownPayload{}
}

// lastQuestionDismissed decodes the most recent question_dismissed envelope.
func lastQuestionDismissed(t *testing.T, pushes []recordedPush) protocol.QuestionDismissedPayload {
	t.Helper()
	for i := len(pushes) - 1; i >= 0; i-- {
		if pushes[i].env.Type != protocol.TypeQuestionDismissed {
			continue
		}
		var p protocol.QuestionDismissedPayload
		if err := json.Unmarshal(pushes[i].env.Payload, &p); err != nil {
			t.Fatalf("decode question_dismissed payload: %v", err)
		}
		return p
	}
	t.Fatal("no question_dismissed push found")
	return protocol.QuestionDismissedPayload{}
}

// AC-1: the whole batch reaches interactive clients in ONE frame carrying the
// daemon-asserted conversation id and a batch id that is the daemon's own minted
// nonce rather than anything read out of claude's tool input.
//
// The nonce check is the substring scan against the raw input, not just a
// non-empty assertion: an implementation that adopted an id claude supplied would
// pass "non-empty" and fail here. Both nesting levels are asserted in claude's own
// array order, and the two questions carry OPPOSITE multi_select values, so a
// producer hard-coding either one is caught.
func TestStreamApprovalBridge_Surface_QuestionBroadcastsBatch(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	input := questionInput(t, "how should it write?", "Write strategy", "rewrite", "replace the file wholesale")
	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName, input)

	if retire := f.bridge.Surface(req); retire == nil {
		t.Fatal("Surface returned a nil retire closure")
	}

	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeQuestionShown {
		t.Fatalf("pushes = %v, want exactly one question_shown", got)
	}
	p := lastQuestionShown(t, f.bcast.pushes)

	if p.ConversationID != testConvID {
		t.Errorf("conversation_id = %q, want the daemon-asserted %q", p.ConversationID, testConvID)
	}
	if p.QuestionBatchID == "" {
		t.Fatal("question_batch_id is empty; the broadcast frame must be the STAMPED payload")
	}
	if strings.Contains(string(input), p.QuestionBatchID) {
		t.Errorf("question_batch_id %q appears in claude's tool input; it must be the daemon's own nonce", p.QuestionBatchID)
	}

	if len(p.Questions) != 2 {
		t.Fatalf("questions = %d, want 2", len(p.Questions))
	}
	if p.Questions[0].Text != "how should it write?" || p.Questions[0].Header != "Write strategy" {
		t.Errorf("question[0] = {%q %q}, want claude's own text and header", p.Questions[0].Text, p.Questions[0].Header)
	}
	if !p.Questions[0].MultiSelect || p.Questions[1].MultiSelect {
		t.Errorf("multi_select = {%v %v}, want {true false} (claude's own per-question flag)", p.Questions[0].MultiSelect, p.Questions[1].MultiSelect)
	}
	if len(p.Questions[0].Options) != 3 || len(p.Questions[1].Options) != 2 {
		t.Fatalf("options = {%d %d}, want {3 2}", len(p.Questions[0].Options), len(p.Questions[1].Options))
	}
	if p.Questions[0].Options[0].Label != "rewrite" || p.Questions[0].Options[0].Description != "replace the file wholesale" {
		t.Errorf("option[0][0] = {%q %q}, want claude's own label and description in array order",
			p.Questions[0].Options[0].Label, p.Questions[0].Options[0].Description)
	}

	if _, ok := f.qreg.Lookup(p.QuestionBatchID); !ok {
		t.Errorf("batch %q not parked in the registry", p.QuestionBatchID)
	}
	if n := questionLen(f.bridge); n != 1 {
		t.Errorf("byQuestion len = %d, want 1 (correlation stored)", n)
	}
}

// AC-2: a question no longer surfaces as a permission modal named after the tool.
//
// The ResolveStream arm is the structural half of the same claim: the batch id
// lives in byQuestion and NOT in byModal, so a modal_answer naming a batch id
// resolves nothing — it cannot allow claude's AskUserQuestion call with nobody
// having answered it.
func TestStreamApprovalBridge_Surface_QuestionIsNotAPermissionModal(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, "how should it write?", "Write strategy", "rewrite", "replace the file wholesale"))
	f.bridge.Surface(req)

	for _, ty := range pushTypes(f.bcast.pushes) {
		if ty == protocol.TypeModalShown {
			t.Fatal("a question surfaced as a modal_shown")
		}
	}
	if n := bridgeLen(f.bridge); n != 0 {
		t.Errorf("byModal len = %d, want 0; a batch id must never be a modal correlation", n)
	}
	batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
	if _, ok := f.modal.Lookup(batchID); ok {
		t.Error("the batch id is recorded in modalbridge; a question is not a modal")
	}
	if handled := f.bridge.ResolveStream(batchID, true, false, reasonRemoteDeny); handled {
		t.Error("ResolveStream(batchID) handled = true; a modal_answer must not resolve a question's completer")
	}
}

// AC-3: every no-answer terminal path broadcasts exactly one dismissal naming the
// same batch id the batch frame carried.
//
// retire is ONE closure the control server defers on every Await return, so the
// three paths the criterion names — the window elapsing, the caller disconnecting,
// the daemon shutting down — are three CALLERS of this deleter rather than three
// branches to reconstruct. What is asserted here is what the arbiter does when it
// runs. The sentinels are asserted by value because the vocabulary is published:
// source is deliberately NOT modal_dismissed's `timeout`, which would name a cause
// wrong on two of the three paths.
func TestStreamApprovalBridge_Retire_QuestionBroadcastsDismissal(t *testing.T) {
	t.Parallel()

	logger, logBuf := auditLogger()
	f := newQuestionFixture(t, testConvID, logger)
	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, "how should it write?", "Write strategy", "rewrite", "replace the file wholesale"))
	retire := f.bridge.Surface(req)
	batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
	f.bcast.pushes = nil // isolate the dismissal broadcast

	retire()

	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeQuestionDismissed {
		t.Fatalf("pushes = %v, want exactly one question_dismissed", got)
	}
	d := lastQuestionDismissed(t, f.bcast.pushes)
	if d.QuestionBatchID != batchID {
		t.Errorf("question_batch_id = %q, want the batch frame's %q", d.QuestionBatchID, batchID)
	}
	if d.Outcome != outcomeQuestionUnanswered || d.Source != sourceQuestionNoAnswer {
		t.Errorf("dismissal = {%q %q}, want {%q %q}", d.Outcome, d.Source, outcomeQuestionUnanswered, sourceQuestionNoAnswer)
	}
	if _, ok := f.qreg.Lookup(batchID); ok {
		t.Error("batch still outstanding after retire; Resolve must consume it")
	}
	if n := questionLen(f.bridge); n != 0 {
		t.Errorf("byQuestion len = %d after retire, want 0 (no leak)", n)
	}
	if recs := auditRecords(t, logBuf); len(recs) != 0 {
		t.Errorf("audit records = %d, want 0; audit.Entry is the MODAL vocabulary", len(recs))
	}
}

// AC-3's "exactly one broadcaster per outstanding question": when the answer path
// (#1907) has already consumed the batch, retire deletes the correlation
// UNCONDITIONALLY but broadcasts no second dismissal. The registry's one-shot
// Resolve is the single arbiter, which is what makes this structural rather than
// an agreement between two tickets.
func TestStreamApprovalBridge_Retire_QuestionAfterResolveNoSecondDismissal(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, "how should it write?", "Write strategy", "rewrite", "replace the file wholesale"))
	retire := f.bridge.Surface(req)
	batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
	f.bcast.pushes = nil

	// Stand in for #1907's answer path consuming the batch before retire runs.
	if _, ok := f.qreg.Resolve(batchID); !ok {
		t.Fatal("batch not present to consume")
	}

	retire()

	if got := pushTypes(f.bcast.pushes); len(got) != 0 {
		t.Errorf("pushes = %v after consumed-then-retire, want none (single arbiter)", got)
	}
	if n := questionLen(f.bridge); n != 0 {
		t.Errorf("byQuestion len = %d, want 0 (unconditional delete guards the leak)", n)
	}
}

// retire over a batch id that was never recorded — the crypto/rand drop path's
// no-op closure, or any absent id — deletes an absent key, finds nothing to
// resolve, and broadcasts nothing.
func TestStreamApprovalBridge_Retire_QuestionNoopWhenNothingSurfaced(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())

	f.bridge.retireQuestion("never-surfaced")

	if len(f.bcast.pushes) != 0 {
		t.Errorf("pushes = %v, want none for an unsurfaced batch", pushTypes(f.bcast.pushes))
	}
	if n := questionLen(f.bridge); n != 0 {
		t.Errorf("byQuestion len = %d, want 0", n)
	}
}

// An AskUserQuestion call whose input fails questionbridge's bounds is NOT a batch
// anybody can render, so it falls through to the fail-closed permission modal —
// the only prompt that still gets claude an allow/deny decision. Parse
// deliberately does not distinguish "not the question tool" from "the question
// tool, rejected", and this is why that costs nothing.
func TestStreamApprovalBridge_Surface_RejectedQuestionFallsBackToPermissionModal(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	// Five questions — outside questionbridge's documented 1-4.
	oversized := json.RawMessage(`{"questions":[` +
		`{"question":"a","header":"A","multiSelect":false,"options":[{"label":"x","description":"1"},{"label":"y","description":"2"}]},` +
		`{"question":"b","header":"B","multiSelect":false,"options":[{"label":"x","description":"1"},{"label":"y","description":"2"}]},` +
		`{"question":"c","header":"C","multiSelect":false,"options":[{"label":"x","description":"1"},{"label":"y","description":"2"}]},` +
		`{"question":"d","header":"D","multiSelect":false,"options":[{"label":"x","description":"1"},{"label":"y","description":"2"}]},` +
		`{"question":"e","header":"E","multiSelect":false,"options":[{"label":"x","description":"1"},{"label":"y","description":"2"}]}]}`)
	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName, oversized)

	f.bridge.Surface(req)

	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeModalShown {
		t.Fatalf("pushes = %v, want exactly one modal_shown for a batch that failed the parse", got)
	}
	if n := questionLen(f.bridge); n != 0 {
		t.Errorf("byQuestion len = %d, want 0 for a rejected batch", n)
	}
	if n := bridgeLen(f.bridge); n != 1 {
		t.Errorf("byModal len = %d, want 1 (the fail-closed permission fallback)", n)
	}
}

// AC-4: a permission approval surfaces and retires exactly as it does today even
// with the question registry wired. The permission tests above additionally cover
// the nil-registry construction, which is every other call site in the tree.
func TestStreamApprovalBridge_Surface_PermissionUnchangedWithQuestionRegistry(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	req, _ := parkApproval(t, f.perm, "tu-1", "Bash", json.RawMessage(`{"cmd":"ls"}`))
	retire := f.bridge.Surface(req)

	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeModalShown {
		t.Fatalf("pushes = %v, want exactly one modal_shown", got)
	}
	shown := lastModalShown(t, f.bcast.pushes)
	if len(shown.Options) != 4 || shown.Class != "permission" {
		t.Errorf("modal = {%d options, class %q}, want the unchanged 4-option permission modal", len(shown.Options), shown.Class)
	}
	if n := questionLen(f.bridge); n != 0 {
		t.Errorf("byQuestion len = %d, want 0 for a permission approval", n)
	}
	f.bcast.pushes = nil

	retire()

	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeModalDismissed {
		t.Fatalf("pushes = %v, want exactly one modal_dismissed", got)
	}
}

// AC-5: no log line on this path carries question text, a header, an option label
// or description, or any other byte of the parked input.
//
// FOUR distinct sentinels, one per claude-authored string, because the input
// CONTAINS all four: a single input-wide sentinel would pass while a log line
// echoed only the header, only a label or only a description. The push is forced
// to fail so broadcast's error arm runs on both frames, and the dismissal payload
// itself is scanned — question_dismissed carries no claude-authored byte, and the
// natural implementation of an answer path violates that by reaching for the
// chosen option's label.
func TestStreamApproval_NoQuestionBodyLeakInLogs(t *testing.T) {
	t.Parallel()

	const (
		secretText   = "SECRET-QUESTION-TEXT-1111"
		secretHeader = "SECRET-HEADER-2222"
		secretLabel  = "SECRET-LABEL-3333"
		secretDesc   = "SECRET-DESCRIPTION-4444"
	)

	logger, logBuf := auditLogger()
	f := newQuestionFixture(t, testConvID, logger)
	f.bcast.pushErr = map[string]error{"c1": errors.New("push failed")}

	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, secretText, secretHeader, secretLabel, secretDesc))
	retire := f.bridge.Surface(req)
	retire()

	s := logBuf.String()
	for _, secret := range []struct{ name, value string }{
		{"question text", secretText},
		{"header", secretHeader},
		{"option label", secretLabel},
		{"option description", secretDesc},
		{"tool name", questionbridge.ToolName},
	} {
		if strings.Contains(s, secret.value) {
			t.Errorf("%s leaked into a log field", secret.name)
		}
	}

	d := lastQuestionDismissed(t, f.bcast.pushes)
	for _, secret := range []string{secretText, secretHeader, secretLabel, secretDesc} {
		if strings.Contains(d.Outcome, secret) || strings.Contains(d.Source, secret) {
			t.Errorf("question_dismissed carries the claude-authored %q; both fields are daemon-asserted sentinels", secret)
		}
	}
}

// A parked question is parked on a human exactly as a permission is, so #1912's
// re-arm must see it: permbridge asks ApprovalAnswerable on every elapsed window,
// and a batch invisible to that report would be denied at the first one instead of
// waiting for the operator walking to their desk.
func TestStreamApprovalBridge_ApprovalAnswerable_CoversAParkedQuestion(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, "how should it write?", "Write strategy", "rewrite", "replace the file wholesale"))
	retire := f.bridge.Surface(req)

	if !bridgeAnswerable(t, f.bridge, f.perm, "tu-q1") {
		t.Error("ApprovalAnswerable = false with a question parked and an interactive client connected")
	}
	if bridgeAnswerable(t, f.bridge, f.perm, "tu-never-parked") {
		t.Error("ApprovalAnswerable = true for an id that was never parked")
	}

	retire()

	if bridgeAnswerable(t, f.bridge, f.perm, "tu-q1") {
		t.Error("ApprovalAnswerable = true after the batch retired; the correlation is gone")
	}
}

// ApprovalParked's half of the same claim: the conversation whose call is in
// flight reports positive while its question sits on a human, and negative once
// the batch retires. Keyed by the conversation the parked call is in flight on,
// never by the follow-active cursor — so the cursor is pointed at B while A is the
// conversation with the question.
func TestStreamApprovalBridge_ApprovalParked_CoversAParkedQuestion(t *testing.T) {
	t.Parallel()

	f := newApprovalReport(t, testConvIDB, discardLogger())
	f.bridge.questions = questionbridge.New()
	f.tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: questionbridge.ToolName})

	req, _ := parkApproval(t, f.perm, "tu-a1", questionbridge.ToolName,
		questionInput(t, "how should it write?", "Write strategy", "rewrite", "replace the file wholesale"))
	retire := f.bridge.Surface(req)

	if !f.bridge.ApprovalParked(testConvID) {
		t.Error("ApprovalParked(A) = false with A's own question parked on a human")
	}
	if f.bridge.ApprovalParked(testConvIDB) {
		t.Error("ApprovalParked(B) = true; B has no approval parked")
	}

	retire()

	if f.bridge.ApprovalParked(testConvID) {
		t.Error("ApprovalParked(A) = true after the batch retired")
	}
}

// --- #1990: refusing an outstanding batch --------------------------------------

// AC-1/AC-2: refusing an outstanding batch consumes the registry's one-shot,
// hands claude a deny carrying the daemon's own instruction to wait, and
// broadcasts exactly one question_dismissed with the refusal sentinels.
//
// The verdict is read off the parked approval's OWN handle, so what is asserted
// is what claude receives rather than what this file believes was sent. The
// constant is additionally asserted to tell claude to wait: an edit trimming it
// to a bare "denied" satisfies every equality check here while letting claude
// guess an answer and carry on, which is the failure the message exists to stop.
func TestStreamApprovalBridge_RefuseQuestion_DeniesAndDismisses(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	req, pending := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, "how should it write?", "Write strategy", "rewrite", "replace the file wholesale"))
	f.bridge.Surface(req)
	batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
	f.isolate()

	if !f.bridge.RefuseQuestion(batchID) {
		t.Fatal("RefuseQuestion = false for an outstanding batch")
	}

	v := pending.Await()
	if v.Behavior != permbridge.BehaviorDeny {
		t.Errorf("verdict behavior = %q, want deny", v.Behavior)
	}
	if v.Message != reasonQuestionRefused {
		t.Errorf("deny message = %q, want the daemon constant", v.Message)
	}
	if !strings.Contains(reasonQuestionRefused, "wait") {
		t.Error("the deny message must tell claude to wait for the user's message")
	}

	waitPush(t, f.pushed)
	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeQuestionDismissed {
		t.Fatalf("pushes = %v, want exactly one question_dismissed", got)
	}
	d := lastQuestionDismissed(t, f.bcast.pushes)
	if d.QuestionBatchID != batchID {
		t.Errorf("question_batch_id = %q, want the batch frame's %q", d.QuestionBatchID, batchID)
	}
	if d.Outcome != outcomeQuestionRefused || d.Source != sourceQuestionRemote {
		t.Errorf("dismissal = {%q %q}, want {%q %q}", d.Outcome, d.Source, outcomeQuestionRefused, sourceQuestionRemote)
	}
	if outcomeQuestionRefused == outcomeQuestionUnanswered {
		t.Error("the refusal outcome must be distinct from the no-answer one")
	}
	if _, ok := f.qreg.Lookup(batchID); ok {
		t.Error("batch still outstanding; the refusal must consume the one-shot")
	}
	if n := questionLen(f.bridge); n != 1 {
		t.Errorf("byQuestion len = %d, want 1; retireQuestion stays the sole correlation deleter", n)
	}
}

// AC-3, one direction: a batch this path consumed leaves the no-answer backstop
// broadcasting nothing when it later runs — which it always does, since the
// control server defers it on every Await return and the refusal is what makes
// Await return. The registry's one-shot is the single arbiter, so this holds
// structurally rather than by agreement between the two call sites.
func TestStreamApprovalBridge_RefuseQuestion_ThenRetireNoSecondDismissal(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, "how should it write?", "Write strategy", "rewrite", "replace the file wholesale"))
	retire := f.bridge.Surface(req)
	batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
	f.isolate()

	if !f.bridge.RefuseQuestion(batchID) {
		t.Fatal("RefuseQuestion = false for an outstanding batch")
	}
	waitPush(t, f.pushed)
	f.isolate()

	retire()

	if got := pushTypes(f.bcast.pushes); len(got) != 0 {
		t.Errorf("pushes = %v after refuse-then-retire, want none (single arbiter)", got)
	}
	if n := questionLen(f.bridge); n != 0 {
		t.Errorf("byQuestion len = %d after retire, want 0 (no leak)", n)
	}
}

// AC-3's other direction plus the unknown id: a batch the no-answer backstop
// already consumed refuses nothing, and an unknown batch id is inert on both
// counts. Each row additionally asserts the parked approval is STILL parked — a
// refusal that lost the one-shot must not hand claude a verdict anyway, which is
// the whole reason the correlation read comes before the consume.
func TestStreamApprovalBridge_RefuseQuestion_InertPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		runRetire bool // let the no-answer backstop consume the batch first
		batchID   func(surfaced string) string
	}{
		{name: "backstop already consumed", runRetire: true, batchID: func(s string) string { return s }},
		{name: "unknown batch id", batchID: func(string) string { return "never-surfaced" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newQuestionFixture(t, testConvID, discardLogger())
			req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
				questionInput(t, "how should it write?", "Write strategy", "rewrite", "replace the file wholesale"))
			retire := f.bridge.Surface(req)
			surfaced := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
			if tt.runRetire {
				retire()
			}
			f.isolate()

			if f.bridge.RefuseQuestion(tt.batchID(surfaced)) {
				t.Error("RefuseQuestion = true; there was no outstanding batch to consume")
			}
			if got := pushTypes(f.bcast.pushes); len(got) != 0 {
				t.Errorf("pushes = %v, want none", got)
			}
			if _, ok := f.perm.Lookup("tu-q1"); !ok {
				t.Error("the parked approval was resolved by a refusal that consumed nothing")
			}
		})
	}
}

// AC-4: no byte of the batch and no byte of the refusal message reaches a log
// field on this path. The strongest form of the claim is available here because
// the refusal emits NO record of its own — the buffer is asserted EMPTY, so any
// diagnostic later added anywhere on this path reddens whether or not it happens
// to carry a secret. broadcast's push-error arm is shared, content-free and
// already probed by the no-answer leak test above.
//
// FOUR distinct sentinels, one per claude-authored string, for that test's
// reason: the input contains all four, so an input-wide sentinel would pass
// while a field echoed only the header or only a label.
func TestStreamApproval_NoQuestionBodyLeakOnRefusal(t *testing.T) {
	t.Parallel()

	const (
		secretText   = "SECRET-QUESTION-TEXT-1111"
		secretHeader = "SECRET-HEADER-2222"
		secretLabel  = "SECRET-LABEL-3333"
		secretDesc   = "SECRET-DESCRIPTION-4444"
	)

	logger, logBuf := auditLogger()
	f := newQuestionFixture(t, testConvID, logger)
	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, secretText, secretHeader, secretLabel, secretDesc))
	f.bridge.Surface(req)
	batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
	f.isolate()

	if !f.bridge.RefuseQuestion(batchID) {
		t.Fatal("RefuseQuestion = false for an outstanding batch")
	}
	waitPush(t, f.pushed)

	if s := logBuf.String(); s != "" {
		t.Errorf("the refusal logged %q; this path emits no record of its own", s)
	}
	d := lastQuestionDismissed(t, f.bcast.pushes)
	for _, secret := range []string{secretText, secretHeader, secretLabel, secretDesc, reasonQuestionRefused} {
		if strings.Contains(d.Outcome, secret) || strings.Contains(d.Source, secret) {
			t.Errorf("question_dismissed carries %q; both fields are daemon-asserted sentinels", secret)
		}
	}
}

// The two question texts questionInput parks, in claude's own array order. The
// first question is multiSelect and carries three options; the second is
// single-select and carries two — the pair both emitted answer shapes need.
const (
	multiQuestionText  = "how should it write?"
	singleQuestionText = "which branch should it target?"
)

// goodAnswers covers the parked batch exactly once: several values for the
// multiSelect question, one for the single-select one.
func goodAnswers() []protocol.QuestionAnswerEntry {
	return []protocol.QuestionAnswerEntry{
		{QuestionIndex: 0, Values: []string{"rewrite", "second"}},
		{QuestionIndex: 1, Values: []string{"main"}},
	}
}

// surfacedQuestion parks an AskUserQuestion call, surfaces it, and returns the
// minted batch id with the fixture isolated — the four lines every test below
// opens with.
func surfacedQuestion(t *testing.T, f questionFixture, text string) (permbridge.Request, *permbridge.Pending, string) {
	t.Helper()
	req, pending := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, text, "Write strategy", "rewrite", "replace the file wholesale"))
	f.bridge.Surface(req)
	batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
	f.isolate()
	return req, pending, batchID
}

// updatedInput decodes an allow verdict's updated tool input into its two
// halves, asserting it carries EXACTLY the two keys claude's contract names —
// the passthrough questions array and the answers object — and nothing else.
func updatedInput(t *testing.T, v permbridge.Verdict) (json.RawMessage, map[string]any) {
	t.Helper()
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(v.UpdatedInput, &keys); err != nil {
		t.Fatalf("decode updated input: %v", err)
	}
	if len(keys) != 2 || keys["questions"] == nil || keys["answers"] == nil {
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		t.Fatalf("updated input keys = %v, want exactly questions + answers", names)
	}
	var answers map[string]any
	if err := json.Unmarshal(keys["answers"], &answers); err != nil {
		t.Fatalf("decode answers: %v", err)
	}
	return keys["questions"], answers
}

// AC-1: an answer to an outstanding batch resolves claude's parked call as an
// ALLOW whose updated input maps each question's text to its value — a bare
// string for the single-select question and an ARRAY for the multiSelect one,
// which is decided from the parked multi_select rather than from how many values
// arrived. The multiSelect question here receives two values and the
// single-select one receives client free text matching no offered label, so an
// implementation keying the shape off arrival count, or one validating values
// against the batch's labels, fails.
func TestStreamApprovalBridge_AnswerQuestion_AllowsWithAssembledInput(t *testing.T) {
	t.Parallel()

	const freeText = "a branch nobody offered"
	f := newQuestionFixture(t, testConvID, discardLogger())
	_, pending, batchID := surfacedQuestion(t, f, multiQuestionText)

	if !f.bridge.AnswerQuestion(batchID, []protocol.QuestionAnswerEntry{
		{QuestionIndex: 0, Values: []string{"rewrite", "second"}},
		{QuestionIndex: 1, Values: []string{freeText}},
	}) {
		t.Fatal("AnswerQuestion = false for an outstanding batch")
	}

	v := pending.Await()
	if v.Behavior != permbridge.BehaviorAllow {
		t.Fatalf("verdict behavior = %q, want allow", v.Behavior)
	}
	_, answers := updatedInput(t, v)

	if got, ok := answers[multiQuestionText].([]any); !ok || len(got) != 2 || got[0] != "rewrite" || got[1] != "second" {
		t.Errorf("answers[multiSelect] = %#v, want the ordered array [rewrite second]", answers[multiQuestionText])
	}
	if got, ok := answers[singleQuestionText].(string); !ok || got != freeText {
		t.Errorf("answers[single-select] = %#v, want the bare free-text string %q", answers[singleQuestionText], freeText)
	}
	if len(answers) != 2 {
		t.Errorf("answers has %d keys, want one per parked question", len(answers))
	}

	waitPush(t, f.pushed)
	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeQuestionDismissed {
		t.Fatalf("pushes = %v, want exactly one question_dismissed", got)
	}
	if _, ok := f.qreg.Lookup(batchID); ok {
		t.Error("batch still outstanding; the answer must consume the one-shot")
	}
	if n := questionLen(f.bridge); n != 1 {
		t.Errorf("byQuestion len = %d, want 1; retireQuestion stays the sole correlation deleter", n)
	}
}

// AC-2: the questions array claude gets back is claude's OWN bytes, spliced out
// of the parked tool input, not a re-marshal of the daemon's wire-shaped batch.
// The two routes are indistinguishable to a shape assertion and differ by one
// key spelling: protocol.Question tags MultiSelect as multi_select where claude's
// tool input uses multiSelect, so a re-marshal hands claude a key it does not
// read — silently, since the call is allowed either way. Both spellings are
// asserted, and the value is compared byte-for-byte against the parked input's.
func TestStreamApprovalBridge_AnswerQuestion_PassesClaudesOwnQuestionBytes(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	req, pending, batchID := surfacedQuestion(t, f, multiQuestionText)

	if !f.bridge.AnswerQuestion(batchID, goodAnswers()) {
		t.Fatal("AnswerQuestion = false for an outstanding batch")
	}
	questions, _ := updatedInput(t, pending.Await())

	var parked struct {
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(req.Input, &parked); err != nil {
		t.Fatalf("decode the parked tool input: %v", err)
	}
	var want, got bytes.Buffer
	if err := json.Compact(&want, parked.Questions); err != nil {
		t.Fatalf("compact the parked questions: %v", err)
	}
	if err := json.Compact(&got, questions); err != nil {
		t.Fatalf("compact the passed-through questions: %v", err)
	}
	if got.String() != want.String() {
		t.Errorf("questions passthrough =\n%s\nwant claude's own bytes:\n%s", got.String(), want.String())
	}
	if !strings.Contains(got.String(), `"multiSelect"`) {
		t.Error(`the passthrough lost claude's "multiSelect" key`)
	}
	if strings.Contains(got.String(), `"multi_select"`) {
		t.Error(`the passthrough carries the wire key "multi_select"; it re-marshalled the parked batch instead of splicing claude's bytes`)
	}
}

// AC-3: an answer that does not name every parked question exactly once, or
// whose values are unusable, resolves NOTHING — no verdict reaches claude,
// nothing is broadcast, and the batch stays outstanding for a corrected answer or
// for the no-answer backstop. Each row asserts all three, so a partial or
// last-write-wins resolution fails whichever half it got wrong.
//
// No row rejects on a value the batch does not offer: claude's contract permits
// free text anywhere, which the allow test above pins from the other side.
func TestStreamApprovalBridge_AnswerQuestion_RejectPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		answers []protocol.QuestionAnswerEntry
	}{
		{"index over-large", []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite"}}, {QuestionIndex: 2, Values: []string{"main"}}}},
		{"index negative", []protocol.QuestionAnswerEntry{{QuestionIndex: -1, Values: []string{"rewrite"}}, {QuestionIndex: 1, Values: []string{"main"}}}},
		{"index repeated", []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite"}}, {QuestionIndex: 0, Values: []string{"second"}}}},
		{"question missing", []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite"}}}},
		{"no entries at all", nil},
		{"more entries than questions", []protocol.QuestionAnswerEntry{
			{QuestionIndex: 0, Values: []string{"rewrite"}},
			{QuestionIndex: 1, Values: []string{"main"}},
			{QuestionIndex: 1, Values: []string{"next"}},
		}},
		{"no value for a question", []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite"}}, {QuestionIndex: 1, Values: nil}}},
		{"two values for a single-select question", []protocol.QuestionAnswerEntry{
			{QuestionIndex: 0, Values: []string{"rewrite"}},
			{QuestionIndex: 1, Values: []string{"main", "next"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newQuestionFixture(t, testConvID, discardLogger())
			_, _, batchID := surfacedQuestion(t, f, multiQuestionText)

			if f.bridge.AnswerQuestion(batchID, tt.answers) {
				t.Error("AnswerQuestion = true; the answer does not name every parked question exactly once")
			}
			if got := pushTypes(f.bcast.pushes); len(got) != 0 {
				t.Errorf("pushes = %v, want none", got)
			}
			if _, ok := f.perm.Lookup("tu-q1"); !ok {
				t.Error("the parked approval was resolved by a rejected answer")
			}
			if _, ok := f.qreg.Lookup(batchID); !ok {
				t.Error("the batch was consumed by a rejected answer; it must stay outstanding for a corrected one")
			}
		})
	}
}

// AC-3's sharpest row, and the reason the parked-input read comes BEFORE the
// consume: permbridge resolved this approval on its own timer, so there are no
// input bytes to pass through. The answer must do nothing at all — consuming and
// dismissing without a verdict would silence the deferred retireQuestion, which
// owes every client its `unanswered` dismissal, and leave claude denied by the
// timer with no client told why.
func TestStreamApprovalBridge_AnswerQuestion_ParkedInputGone(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	_, _, batchID := surfacedQuestion(t, f, multiQuestionText)
	f.perm.Resolve("tu-q1", permbridge.Deny("the approval window elapsed"))

	if f.bridge.AnswerQuestion(batchID, goodAnswers()) {
		t.Error("AnswerQuestion = true; claude's parked input was no longer retrievable")
	}
	if got := pushTypes(f.bcast.pushes); len(got) != 0 {
		t.Errorf("pushes = %v, want none", got)
	}
	if _, ok := f.qreg.Lookup(batchID); !ok {
		t.Fatal("the batch was consumed with no verdict; the no-answer backstop can no longer dismiss it")
	}

	// The backstop still owes its dismissal, and can still pay it.
	f.bridge.retireQuestion(batchID)
	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeQuestionDismissed {
		t.Fatalf("pushes after the backstop ran = %v, want its one question_dismissed", got)
	}
	if d := lastQuestionDismissed(t, f.bcast.pushes); d.Outcome != outcomeQuestionUnanswered {
		t.Errorf("backstop outcome = %q, want %q", d.Outcome, outcomeQuestionUnanswered)
	}
}

// AC-4: the three verdicts share one arbiter — the registry's one-shot — so a
// batch resolved by any of them is inert to the other two, in both directions.
// This holds structurally rather than by agreement between the call sites, which
// is why every pairing is asserted rather than a representative one.
func TestStreamApprovalBridge_AnswerQuestion_SingleArbiter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// first consumes the batch; second must then find nothing.
		first  func(f questionFixture, retire func(), batchID string)
		second func(f questionFixture, retire func(), batchID string) bool
		// pushes the first verdict is expected to emit before the second runs.
		firstPushes int
	}{
		{
			name:        "answer then retire",
			first:       func(f questionFixture, _ func(), id string) { f.bridge.AnswerQuestion(id, goodAnswers()) },
			second:      func(_ questionFixture, retire func(), _ string) bool { retire(); return false },
			firstPushes: 1,
		},
		{
			name:        "answer then refuse",
			first:       func(f questionFixture, _ func(), id string) { f.bridge.AnswerQuestion(id, goodAnswers()) },
			second:      func(f questionFixture, _ func(), id string) bool { return f.bridge.RefuseQuestion(id) },
			firstPushes: 1,
		},
		{
			name:        "retire then answer",
			first:       func(_ questionFixture, retire func(), _ string) { retire() },
			second:      func(f questionFixture, _ func(), id string) bool { return f.bridge.AnswerQuestion(id, goodAnswers()) },
			firstPushes: 1,
		},
		{
			name:        "refuse then answer",
			first:       func(f questionFixture, _ func(), id string) { f.bridge.RefuseQuestion(id) },
			second:      func(f questionFixture, _ func(), id string) bool { return f.bridge.AnswerQuestion(id, goodAnswers()) },
			firstPushes: 1,
		},
		{
			name:        "answer twice",
			first:       func(f questionFixture, _ func(), id string) { f.bridge.AnswerQuestion(id, goodAnswers()) },
			second:      func(f questionFixture, _ func(), id string) bool { return f.bridge.AnswerQuestion(id, goodAnswers()) },
			firstPushes: 1,
		},
		{
			name:  "unknown batch id",
			first: func(questionFixture, func(), string) {},
			second: func(f questionFixture, _ func(), _ string) bool {
				return f.bridge.AnswerQuestion("never-surfaced", goodAnswers())
			},
			firstPushes: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newQuestionFixture(t, testConvID, discardLogger())
			req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
				questionInput(t, multiQuestionText, "Write strategy", "rewrite", "replace the file wholesale"))
			retire := f.bridge.Surface(req)
			batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
			f.isolate()

			tt.first(f, retire, batchID)
			for range tt.firstPushes {
				waitPush(t, f.pushed)
			}
			f.isolate()

			if tt.second(f, retire, batchID) {
				t.Error("the second verdict consumed a batch the first one had already resolved")
			}
			if got := pushTypes(f.bcast.pushes); len(got) != 0 {
				t.Errorf("pushes = %v after the second verdict, want none (single arbiter)", got)
			}
		})
	}
}

// AC-4: the dismissal an answer emits carries source `remote` and an outcome
// sentinel distinct from BOTH the refusal's and the no-answer one, and carries no
// claude-authored string. The last clause is the trust-tier rule from
// § question_dismissed rather than a style preference: options carry no id and
// claude selects by LABEL, so the natural implementation reports the chosen
// label here and moves a daemon-asserted frame into the batch's trust tier. Every
// offered label is asserted absent from both fields.
func TestStreamApprovalBridge_AnswerQuestion_DismissalCarriesNoClaudeString(t *testing.T) {
	t.Parallel()

	f := newQuestionFixture(t, testConvID, discardLogger())
	_, _, batchID := surfacedQuestion(t, f, multiQuestionText)

	if !f.bridge.AnswerQuestion(batchID, goodAnswers()) {
		t.Fatal("AnswerQuestion = false for an outstanding batch")
	}
	waitPush(t, f.pushed)

	d := lastQuestionDismissed(t, f.bcast.pushes)
	if d.QuestionBatchID != batchID {
		t.Errorf("question_batch_id = %q, want the batch frame's %q", d.QuestionBatchID, batchID)
	}
	if d.Source != sourceQuestionRemote {
		t.Errorf("source = %q, want %q", d.Source, sourceQuestionRemote)
	}
	if d.Outcome != outcomeQuestionAnswered {
		t.Errorf("outcome = %q, want %q", d.Outcome, outcomeQuestionAnswered)
	}
	if outcomeQuestionAnswered == outcomeQuestionUnanswered || outcomeQuestionAnswered == outcomeQuestionRefused {
		t.Error("the answered outcome must be distinct from both the no-answer and the refusal sentinels")
	}
	for _, label := range []string{"rewrite", "second", "third", "main", "next"} {
		if d.Outcome == label || d.Source == label {
			t.Errorf("dismissal carries the claude-authored option label %q; both fields are daemon-defined sentinels", label)
		}
	}
}

// AC-5: no byte of the batch and no byte of the client's answer reaches a log
// field on this path. The buffer is asserted EMPTY rather than merely
// sentinel-free — the answer emits no record of its own, so any diagnostic later
// added anywhere on this path reddens whether or not it happens to carry a
// secret. FOUR claude-authored sentinels, one per string the input carries, plus
// a client-authored one: an input-wide sentinel would pass while a field echoed
// only the header, only a label, or only the answer.
//
// Pushes are NOT forced to fail here, unlike the permission leak probe.
// broadcast's push-error arm is shared, content-free and already probed by the
// no-answer test; running it would put its Debug line in the buffer and cost
// this test its empty-buffer assertion, which is the stronger claim. It would
// also race the buffer — that line is written by the detached fan-out AFTER the
// Push that waitPush observes, so no happens-before edge covers it.
func TestStreamApproval_NoQuestionBodyLeakOnAnswer(t *testing.T) {
	t.Parallel()

	const (
		secretText   = "SECRET-QUESTION-TEXT-5555"
		secretHeader = "SECRET-HEADER-6666"
		secretLabel  = "SECRET-LABEL-7777"
		secretDesc   = "SECRET-DESCRIPTION-8888"
		secretAnswer = "SECRET-ANSWER-VALUE-9999"
	)

	logger, logBuf := auditLogger()
	f := newQuestionFixture(t, testConvID, logger)
	req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
		questionInput(t, secretText, secretHeader, secretLabel, secretDesc))
	f.bridge.Surface(req)
	batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
	f.isolate()

	if !f.bridge.AnswerQuestion(batchID, []protocol.QuestionAnswerEntry{
		{QuestionIndex: 0, Values: []string{secretAnswer}},
		{QuestionIndex: 1, Values: []string{secretAnswer}},
	}) {
		t.Fatal("AnswerQuestion = false for an outstanding batch")
	}
	waitPush(t, f.pushed)

	if s := logBuf.String(); s != "" {
		t.Errorf("the answer logged %q; this path emits no record of its own", s)
	}
	d := lastQuestionDismissed(t, f.bcast.pushes)
	for _, secret := range []string{secretText, secretHeader, secretLabel, secretDesc, secretAnswer} {
		if strings.Contains(d.Outcome, secret) || strings.Contains(d.Source, secret) {
			t.Errorf("question_dismissed carries %q; both fields are daemon-asserted sentinels", secret)
		}
	}
}
