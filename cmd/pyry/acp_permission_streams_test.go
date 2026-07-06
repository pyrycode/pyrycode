package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// The tests below drive runPermissionModalStream — the live-wired drain (#801) —
// with a scriptedSubscriber and a proxy over the acp_permission_test doubles,
// injecting a permission-modal event with NO live claude modal. This scripted
// seam is exactly what the #754 conformance capstone consumes to observe ADR 027
// divergence 2. AC1 (round-trip) and AC2 (default-safe deny) prove the proxy
// resolves correctly through the wiring, not only in isolation — the analog of
// TestACPTurnStreams_ScriptedTurnEmitsOrderedFrames for the permission leg.

// TestACPPermissionStreams_ScriptedModalRoutesSelection proves AC1/AC3: a
// permission ModalShown fed through the wired drain issues exactly one
// session/request_permission Call carrying the session id and the four ACP
// options in fixed order, and the host's selected optionId routes back as the
// resolving keystroke (Answer(digit), digit = option index + 1) — the whole
// round-trip resolves exactly once through the wiring.
func TestACPPermissionStreams_ScriptedModalRoutesSelection(t *testing.T) {
	t.Parallel()

	// The host selects reject_once — the third surfaced option — so a passing
	// assertion proves the index→digit map is real, not a trivial "always 1".
	caller := &fakePermissionCaller{resp: selectedResp("reject_once")}
	kb := newSyncKeystroker()
	p := newACPPermissionProxy(caller, kb, "sess-acp-1", generousTimeout, discardLogger())

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drainDone := make(chan struct{})
	go func() { runPermissionModalStream(ctx, sub.subscribe, p); close(drainDone) }()

	ch <- permissionShown()          // blocking send: the drain received it and called Handle
	if got := kb.waitRouted(t); got != "answer:3" {
		t.Fatalf("routed %q, want answer:3 (reject_once is the 3rd option)", got)
	}

	if n := caller.callCount(); n != 1 {
		t.Fatalf("Call count = %d, want 1", n)
	}
	method, params := caller.request()
	if method != methodSessionRequestPermission {
		t.Errorf("method = %q, want %q", method, methodSessionRequestPermission)
	}
	if params.SessionID != "sess-acp-1" {
		t.Errorf("sessionId = %q, want sess-acp-1", params.SessionID)
	}
	want := []permissionOption{
		{OptionID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		{OptionID: "allow_always", Name: "Allow always", Kind: "allow_always"},
		{OptionID: "reject_once", Name: "Reject once", Kind: "reject_once"},
		{OptionID: "reject_always", Name: "Reject always", Kind: "reject_always"},
	}
	if !slices.Equal(params.Options, want) {
		t.Errorf("options = %+v, want %+v", params.Options, want)
	}
	for _, o := range params.Options {
		if !turnevent.PermissionOptionKind(o.Kind).Valid() {
			t.Errorf("option kind %q is not a valid ACP permission kind", o.Kind)
		}
	}
	if esc, _, trust := kb.snapshot(); esc != 0 || trust != 0 {
		t.Errorf("a non-answer keystroke was routed on the grant path: esc=%d trust=%d", esc, trust)
	}

	cancel()
	waitClosed(t, drainDone, "permission drain after ctx cancel")
}

// TestACPPermissionStreams_NeverAnsweringHostDenies proves AC2: a host that never
// answers within the Call deadline defaults to deny-and-unblock through the wired
// path — the default-safe-deny is preserved by the wiring, not only in the
// isolated adapter.
func TestACPPermissionStreams_NeverAnsweringHostDenies(t *testing.T) {
	t.Parallel()

	// Call blocks forever unless its ctx fires; the short per-Call timeout is the
	// only thing that unblocks it → the round-trip denies.
	caller := &fakePermissionCaller{block: make(chan struct{})}
	kb := newSyncKeystroker()
	p := newACPPermissionProxy(caller, kb, "sess", 20*time.Millisecond, discardLogger())

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drainDone := make(chan struct{})
	go func() { runPermissionModalStream(ctx, sub.subscribe, p); close(drainDone) }()

	ch <- permissionShown()
	if got := kb.waitRouted(t); got != "esc" {
		t.Fatalf("routed %q, want esc (deny on Call timeout)", got)
	}
	if esc, answers, trust := kb.snapshot(); esc != 1 || len(answers) != 0 || trust != 0 {
		t.Errorf("timeout did not deny cleanly through the wiring: esc=%d answers=%v trust=%d (silent grant!)", esc, answers, trust)
	}

	cancel()
	waitClosed(t, drainDone, "permission drain after ctx cancel")
}

// TestACPPermissionStreams_TeardownDeniesInflightAndJoins proves AC4: a drain
// ctx-cancel while a round-trip is blocked in the outbound Call unblocks that Call
// (its cctx descends from the drain ctx), routes the safe deny, and returns the
// drain — no in-flight goroutine outlives teardown.
func TestACPPermissionStreams_TeardownDeniesInflightAndJoins(t *testing.T) {
	t.Parallel()

	caller := &fakePermissionCaller{
		entered: make(chan struct{}, 1),
		block:   make(chan struct{}), // never closed: only ctx-cancel unblocks the Call
		resp:    selectedResp("allow_once"),
	}
	kb := newSyncKeystroker()
	p := newACPPermissionProxy(caller, kb, "sess", generousTimeout, discardLogger())

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}
	ctx, cancel := context.WithCancel(context.Background())
	drainDone := make(chan struct{})
	go func() { runPermissionModalStream(ctx, sub.subscribe, p); close(drainDone) }()

	ch <- permissionShown()
	<-caller.entered // the round-trip goroutine is now blocked inside Call

	cancel() // teardown: drain ctx cancel propagates to the round-trip's cctx

	if got := kb.waitRouted(t); got != "esc" {
		t.Fatalf("routed %q, want esc (teardown-cancel denies the in-flight round-trip)", got)
	}
	waitClosed(t, drainDone, "permission drain after teardown")
	if esc, answers, _ := kb.snapshot(); esc != 1 || len(answers) != 0 {
		t.Errorf("in-flight teardown did not deny cleanly: esc=%d answers=%v", esc, answers)
	}
}

// TestACPPermissionStreams_NoOptionIDLogLeak proves the content-free posture holds
// through the wired chain (preservation): the one attacker-controlled datum — the
// host optionId — never reaches any log field. A forged optionId denies; the
// canary must not appear in a debug-level log buffer. Mirrors the proxy's
// TestACPPermissionProxy_NoOptionIDLeak, but driven through runPermissionModalStream.
func TestACPPermissionStreams_NoOptionIDLogLeak(t *testing.T) {
	t.Parallel()

	const canary = "OPTION-ID-LEAK-CANARY-801"
	caller := &fakePermissionCaller{resp: selectedResp(canary)}
	kb := newSyncKeystroker()
	logger, logBuf := auditLogger()
	p := newACPPermissionProxy(caller, kb, "sess", generousTimeout, logger)

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drainDone := make(chan struct{})
	go func() { runPermissionModalStream(ctx, sub.subscribe, p); close(drainDone) }()

	ch <- permissionShown()
	if got := kb.waitRouted(t); got != "esc" {
		t.Fatalf("routed %q, want esc (forged canary optionId denies)", got)
	}
	// The keystroke is the goroutine's last observable action (the decision Debug
	// is logged before actuating), so the buffer is complete once waitRouted returns.
	if strings.Contains(logBuf.String(), canary) {
		t.Errorf("host optionId leaked into a log field via the wiring:\n%s", logBuf.String())
	}

	cancel()
	waitClosed(t, drainDone, "permission drain after ctx cancel")
}
