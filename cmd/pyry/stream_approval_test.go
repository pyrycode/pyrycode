package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
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

	if n := bridgeLen(bridge); n != 1 {
		t.Errorf("byModal len = %d, want 1 (correlation stored)", n)
	}
	if _, ok := modal.Lookup(p.ModalID); !ok {
		t.Errorf("Outstanding %q not recorded in modalbridge", p.ModalID)
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

	if handled := bridge.ResolveStream(modalID, true, reasonRemoteDeny); !handled {
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

	if handled := bridge.ResolveStream(modalID, false, reasonRemoteDeny); !handled {
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

	if handled := bridge.ResolveStream("no-such-modal", true, reasonRemoteDeny); handled {
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

	perm := permbridge.New()
	modalReg := modalbridge.New()
	bcast := oneInteractiveConn("c1")
	bcast.pushErr = map[string]error{"c1": errors.New("push failed")}
	logger, logBuf := auditLogger()
	bridge := newStreamApprovalBridge(perm, modalReg, bcast, func() string { return "" }, context.Background(), logger)

	input := json.RawMessage(`{"cmd":"` + secretInput + `"}`)
	req, _ := parkApproval(t, perm, "tu-1", secretTool, input)
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
}

// TestStreamApproval_RoundTrip is the AC-4 component-level round-trip: request →
// Surface (modal_shown) → ResolveAnswer (modal_answer) → verdict, for both an
// allow and a deny answer, asserting the permbridge verdict returned to claude.
func TestStreamApproval_RoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		optionID     string
		wantBehavior string
		wantAllow    bool
	}{
		{"allow", string(turnevent.PermissionOptionKindAllowOnce), permbridge.BehaviorAllow, true},
		{"deny", string(turnevent.PermissionOptionKindRejectOnce), permbridge.BehaviorDeny, false},
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
			req, pending := parkApproval(t, perm, "tu-1", "Bash", input)

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
			d, ok := r.ResolveAnswer(shown.ModalID, tc.optionID, "tok", eligibleDevice(t))
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
		})
	}
}

// --- #1919: which conversation a parked approval belongs to -------------------

// testConvIDC is a third valid conversation id, distinct from testConvID
// (interactive_turn_v2_test.go) and testConvIDB (stream_turn_drain_test.go). The
// report fixture's resolver knows it and the tracker never sees it, which is the
// "known to the daemon, never observed" negative row.
const testConvIDC = "33333333-3333-4333-8333-333333333333"

// approvalReport is the #1919 fixture: the bridge whose byModal holds the parked
// correlations, the tracker that answers the membership question, and the two
// registries the park path runs through.
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
