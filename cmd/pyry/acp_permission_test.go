package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// generousTimeout is long enough that the Call deadline never fires in tests that
// resolve via a scripted response; only the explicit timeout test uses a short one.
const generousTimeout = 5 * time.Second

// --- test doubles -----------------------------------------------------------

// fakePermissionCaller scripts the outbound session/request_permission Call: it
// records the method + params (so the request shape can be asserted) and returns
// a scripted response body or error. When block is non-nil the Call blocks until
// block is closed OR ctx is cancelled (the timeout / retirement paths); entered,
// if non-nil, is signalled once the Call is running. Scripted fields (resp / err
// / block / entered) are set before Handle spawns the round-trip goroutine, so
// the goroutine reads them across the go-statement happens-before edge; the
// mutating fields (calls / method / params) are mutex-guarded.
type fakePermissionCaller struct {
	mu     sync.Mutex
	calls  int
	method string
	params requestPermissionParams

	resp    json.RawMessage
	err     error
	block   chan struct{}
	entered chan struct{}
}

func (c *fakePermissionCaller) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.calls++
	c.method = method
	if p, ok := params.(requestPermissionParams); ok {
		c.params = p
	}
	c.mu.Unlock()

	if c.entered != nil {
		c.entered <- struct{}{}
	}
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.resp, c.err
}

func (c *fakePermissionCaller) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *fakePermissionCaller) request() (method string, params requestPermissionParams) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.method, c.params
}

// syncKeystroker is the race-safe keystroke recorder for the two-goroutine
// adapter: the round-trip goroutine actuates a keystroke while the test goroutine
// reads the recorded calls. Every keystroke both records (under mu) and signals
// routed, so a test can block until the round-trip resolves. routed is buffered
// so a keystroke never blocks the goroutine. It satisfies modalKeystroker.
type syncKeystroker struct {
	mu      sync.Mutex
	esc     int
	answers []string
	trust   int
	err     error
	routed  chan string
}

func newSyncKeystroker() *syncKeystroker {
	return &syncKeystroker{routed: make(chan string, 8)}
}

func (k *syncKeystroker) SendEsc() error {
	k.mu.Lock()
	k.esc++
	k.mu.Unlock()
	k.routed <- "esc"
	return k.err
}

func (k *syncKeystroker) Answer(choice string) error {
	k.mu.Lock()
	k.answers = append(k.answers, choice)
	k.mu.Unlock()
	k.routed <- "answer:" + choice
	return k.err
}

func (k *syncKeystroker) AcceptTrust() error {
	k.mu.Lock()
	k.trust++
	k.mu.Unlock()
	k.routed <- "trust"
	return k.err
}

// waitRouted blocks until the round-trip goroutine actuates one keystroke and
// returns its tag ("esc", "answer:1", "trust"). This receive establishes
// happens-before with everything the goroutine did up to (and including) the
// keystroke, so the test can then read the caller/keystroker/logs race-free.
func (k *syncKeystroker) waitRouted(t *testing.T) string {
	t.Helper()
	select {
	case v := <-k.routed:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a keystroke")
		return ""
	}
}

// assertQuiet asserts no keystroke is routed within a short window — the negative
// assertion for the retired / already-resolved paths. It is sound because a
// round-trip that lost the one-shot can NEVER route (the CAS deterministically
// fails), so timing only affects how long we wait to confirm, not the outcome.
func (k *syncKeystroker) assertQuiet(t *testing.T) {
	t.Helper()
	select {
	case v := <-k.routed:
		t.Fatalf("expected no keystroke, but %q was routed", v)
	case <-time.After(100 * time.Millisecond):
	}
}

func (k *syncKeystroker) snapshot() (esc int, answers []string, trust int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.esc, slices.Clone(k.answers), k.trust
}

// --- event / response builders ----------------------------------------------

func permissionShown() tuidriver.Event {
	return tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassPermission}
}

func modalHidden() tuidriver.Event {
	return tuidriver.Event{Kind: tuidriver.EventKindPtyModalHidden, Modal: tuidriver.ModalClassPermission}
}

func selectedResp(optionID string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"outcome":{"outcome":"selected","optionId":%q}}`, optionID))
}

func cancelledResp() json.RawMessage {
	return json.RawMessage(`{"outcome":{"outcome":"cancelled"}}`)
}

// --- tests ------------------------------------------------------------------

// TestACPPermissionProxy_RequestShape proves AC-1: a permission ModalShown issues
// a session/request_permission Call carrying the session id, no toolCall, and
// exactly the four ACP kinds (allow_once, allow_always, reject_once,
// reject_always) in claude's display order, each with optionId/name/kind.
func TestACPPermissionProxy_RequestShape(t *testing.T) {
	t.Parallel()

	caller := &fakePermissionCaller{resp: selectedResp("allow_once")}
	kb := newSyncKeystroker()
	logger, _ := auditLogger()
	p := newACPPermissionProxy(caller, kb, "sess-1", generousTimeout, logger)

	p.Handle(context.Background(), permissionShown())
	kb.waitRouted(t) // barrier: the round-trip has recorded the request and resolved

	if n := caller.callCount(); n != 1 {
		t.Fatalf("Call count = %d, want 1", n)
	}
	method, params := caller.request()
	if method != methodSessionRequestPermission {
		t.Errorf("method = %q, want %q", method, methodSessionRequestPermission)
	}
	if params.SessionID != "sess-1" {
		t.Errorf("sessionId = %q, want sess-1", params.SessionID)
	}
	if params.ToolCall != nil {
		t.Errorf("toolCall = %+v, want nil (modal event carries no tool-call id)", params.ToolCall)
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
	// Every emitted kind is a valid ACP PermissionOptionKind.
	for _, o := range params.Options {
		if !turnevent.PermissionOptionKind(o.Kind).Valid() {
			t.Errorf("option kind %q is not a valid ACP permission kind", o.Kind)
		}
	}

	// The omitted toolCall must not appear on the wire.
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if strings.Contains(string(raw), "toolCall") {
		t.Errorf("marshalled params carry a toolCall key: %s", raw)
	}
}

// TestACPPermissionProxy_SelectedRoutesKeystroke proves AC-2: a host reply of
// outcome:"selected" with each of the four optionIds routes Answer(digit) with
// the digit = the option's 1-based position in display order, and never ESC.
func TestACPPermissionProxy_SelectedRoutesKeystroke(t *testing.T) {
	t.Parallel()

	tests := []struct {
		optionID  string
		wantDigit string
	}{
		{"allow_once", "1"},
		{"allow_always", "2"},
		{"reject_once", "3"},
		{"reject_always", "4"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.optionID, func(t *testing.T) {
			t.Parallel()

			caller := &fakePermissionCaller{resp: selectedResp(tt.optionID)}
			kb := newSyncKeystroker()
			logger, _ := auditLogger()
			p := newACPPermissionProxy(caller, kb, "sess", generousTimeout, logger)

			p.Handle(context.Background(), permissionShown())
			if got := kb.waitRouted(t); got != "answer:"+tt.wantDigit {
				t.Fatalf("routed %q, want answer:%s", got, tt.wantDigit)
			}
			esc, answers, trust := kb.snapshot()
			if esc != 0 || trust != 0 {
				t.Errorf("unexpected non-answer keystroke: esc=%d trust=%d", esc, trust)
			}
			if !slices.Equal(answers, []string{tt.wantDigit}) {
				t.Errorf("answers = %v, want [%s]", answers, tt.wantDigit)
			}
		})
	}
}

// TestACPPermissionProxy_DefaultSafeDeny proves AC-3: cancelled, a Call error, a
// Call timeout, and an undecodable response ALL resolve to deny (ESC) and NEVER
// route an allow keystroke ("no silent grant").
func TestACPPermissionProxy_DefaultSafeDeny(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		caller  *fakePermissionCaller
		timeout time.Duration
	}{
		{
			name:    "cancelled",
			caller:  &fakePermissionCaller{resp: cancelledResp()},
			timeout: generousTimeout,
		},
		{
			name:    "call error",
			caller:  &fakePermissionCaller{err: errors.New("transport boom")},
			timeout: generousTimeout,
		},
		{
			name:    "undecodable response",
			caller:  &fakePermissionCaller{resp: json.RawMessage(`{"outcome":`)},
			timeout: generousTimeout,
		},
		{
			// block is never closed, but Call honours ctx.Done(), so the short
			// Call deadline fires and returns DeadlineExceeded → deny.
			name:    "timeout",
			caller:  &fakePermissionCaller{block: make(chan struct{})},
			timeout: 20 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			kb := newSyncKeystroker()
			logger, _ := auditLogger()
			p := newACPPermissionProxy(tt.caller, kb, "sess", tt.timeout, logger)

			p.Handle(context.Background(), permissionShown())
			if got := kb.waitRouted(t); got != "esc" {
				t.Fatalf("routed %q, want esc (deny)", got)
			}
			esc, answers, trust := kb.snapshot()
			if esc != 1 {
				t.Errorf("esc = %d, want 1", esc)
			}
			if len(answers) != 0 || trust != 0 {
				t.Errorf("a non-deny keystroke was routed: answers=%v trust=%d (silent grant!)", answers, trust)
			}
		})
	}
}

// TestACPPermissionProxy_ForgedOptionDenies proves AC-3/AC-4: a host reply that
// selects an optionId which is NOT a member of the surfaced set cannot route an
// allow keystroke — it denies (ESC).
func TestACPPermissionProxy_ForgedOptionDenies(t *testing.T) {
	t.Parallel()

	caller := &fakePermissionCaller{resp: selectedResp("i-was-never-offered")}
	kb := newSyncKeystroker()
	logger, _ := auditLogger()
	p := newACPPermissionProxy(caller, kb, "sess", generousTimeout, logger)

	p.Handle(context.Background(), permissionShown())
	if got := kb.waitRouted(t); got != "esc" {
		t.Fatalf("routed %q, want esc (forged option denies)", got)
	}
	if esc, answers, _ := kb.snapshot(); esc != 1 || len(answers) != 0 {
		t.Errorf("forged option did not deny cleanly: esc=%d answers=%v", esc, answers)
	}
}

// TestACPPermissionProxy_RetiredModalRoutesNothing proves AC-4 (and AC-5): when a
// ModalHidden retires the round-trip before the host answers, the one-shot is
// claimed by the retirement, the in-flight Call is cancelled, and the late reply
// routes NOTHING (no keystroke into a modal that is already gone). It also proves
// the Call is off the drain goroutine: Handle processes the Hidden while the Call
// is still outstanding.
func TestACPPermissionProxy_RetiredModalRoutesNothing(t *testing.T) {
	t.Parallel()

	caller := &fakePermissionCaller{
		entered: make(chan struct{}, 1),
		block:   make(chan struct{}), // Call blocks until ctx cancel (retirement)
		resp:    selectedResp("allow_once"),
	}
	kb := newSyncKeystroker()
	logger, _ := auditLogger()
	p := newACPPermissionProxy(caller, kb, "sess", generousTimeout, logger)
	ctx := context.Background()

	p.Handle(ctx, permissionShown())
	<-caller.entered // the round-trip goroutine is now blocked inside Call

	// The drain goroutine is free to process the Hidden while the Call is
	// outstanding — this returns promptly (AC-5: off the read loop).
	p.Handle(ctx, modalHidden())

	// Retirement cancelled the Call's ctx; the late reply must route nothing.
	kb.assertQuiet(t)
	if esc, answers, trust := kb.snapshot(); esc != 0 || len(answers) != 0 || trust != 0 {
		t.Errorf("retired modal routed a keystroke: esc=%d answers=%v trust=%d", esc, answers, trust)
	}
	if n := caller.callCount(); n != 1 {
		t.Errorf("Call count = %d, want 1", n)
	}
}

// TestACPPermissionProxy_SecondResolutionIgnored proves the one-shot rejects a
// second resolution attempt (AC-4): once the host's answer resolves a modal, a
// later ModalHidden for that (already-resolved) round-trip routes no second
// keystroke.
func TestACPPermissionProxy_SecondResolutionIgnored(t *testing.T) {
	t.Parallel()

	caller := &fakePermissionCaller{resp: selectedResp("allow_once")}
	kb := newSyncKeystroker()
	logger, _ := auditLogger()
	p := newACPPermissionProxy(caller, kb, "sess", generousTimeout, logger)
	ctx := context.Background()

	p.Handle(ctx, permissionShown())
	if got := kb.waitRouted(t); got != "answer:1" {
		t.Fatalf("routed %q, want answer:1", got)
	}

	// A Hidden after the modal already resolved is a clean no-op.
	p.Handle(ctx, modalHidden())
	kb.assertQuiet(t)
	if esc, answers, _ := kb.snapshot(); esc != 0 || !slices.Equal(answers, []string{"1"}) {
		t.Errorf("second resolution leaked a keystroke: esc=%d answers=%v", esc, answers)
	}
}

// TestACPPermissionProxy_NonPermissionModalNoOp proves a non-permission modal
// (and a bare Hidden with nothing outstanding) issues no Call and routes no
// keystroke: the adapter gates strictly on ModalClassPermission.
func TestACPPermissionProxy_NonPermissionModalNoOp(t *testing.T) {
	t.Parallel()

	events := []tuidriver.Event{
		{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassTrustFolder},
		{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassSlashPicker},
		{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassUnknown},
		{Kind: tuidriver.EventKindPtyIdle},
		modalHidden(), // Hidden with nothing outstanding
	}
	for _, ev := range events {
		ev := ev
		t.Run(fmt.Sprintf("%d_%s", ev.Kind, ev.Modal), func(t *testing.T) {
			t.Parallel()

			caller := &fakePermissionCaller{resp: selectedResp("allow_once")}
			kb := newSyncKeystroker()
			logger, _ := auditLogger()
			p := newACPPermissionProxy(caller, kb, "sess", generousTimeout, logger)

			p.Handle(context.Background(), ev)

			// No round-trip is spawned for a non-permission event, so no Call and
			// no keystroke ever occur.
			kb.assertQuiet(t)
			if n := caller.callCount(); n != 0 {
				t.Errorf("Call count = %d, want 0 for %v", n, ev.Kind)
			}
			if esc, answers, trust := kb.snapshot(); esc != 0 || len(answers) != 0 || trust != 0 {
				t.Errorf("non-permission event routed a keystroke: esc=%d answers=%v trust=%d", esc, answers, trust)
			}
		})
	}
}

// TestACPPermissionProxy_KeystrokeErrorTolerated proves a keystroke error on the
// resolution path is best-effort: the round-trip still attempts exactly one
// keystroke and the adapter does not panic (there is nothing to roll back).
func TestACPPermissionProxy_KeystrokeErrorTolerated(t *testing.T) {
	t.Parallel()

	caller := &fakePermissionCaller{resp: selectedResp("allow_once")}
	kb := newSyncKeystroker()
	kb.err = supervisor.ErrNoLiveSession
	logger, _ := auditLogger()
	p := newACPPermissionProxy(caller, kb, "sess", generousTimeout, logger)

	p.Handle(context.Background(), permissionShown())
	if got := kb.waitRouted(t); got != "answer:1" {
		t.Fatalf("routed %q, want answer:1 (attempted despite error)", got)
	}
	if esc, answers, _ := kb.snapshot(); esc != 0 || !slices.Equal(answers, []string{"1"}) {
		t.Errorf("keystroke not attempted exactly once: esc=%d answers=%v", esc, answers)
	}
}

// TestACPPermissionProxy_NoOptionIDLeak proves the security content discipline: an
// attacker-controlled optionId (the one host value that reaches the adapter) is
// never written to any log field. A forged optionId denies and the canary must
// not appear in the logs.
func TestACPPermissionProxy_NoOptionIDLeak(t *testing.T) {
	t.Parallel()

	const canary = "OPTION-ID-LEAK-CANARY-9f3a"
	caller := &fakePermissionCaller{resp: selectedResp(canary)}
	kb := newSyncKeystroker()
	logger, logBuf := auditLogger()
	p := newACPPermissionProxy(caller, kb, "sess", generousTimeout, logger)

	p.Handle(context.Background(), permissionShown())
	if got := kb.waitRouted(t); got != "esc" {
		t.Fatalf("routed %q, want esc (forged canary denies)", got)
	}
	// The keystroke is the goroutine's last observable action on this path (the
	// decision Debug is logged BEFORE actuating), so the log buffer is complete.
	if strings.Contains(logBuf.String(), canary) {
		t.Errorf("host optionId leaked into a log field:\n%s", logBuf.String())
	}
}
