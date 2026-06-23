package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// fakeArmer records every ArmModalTimeout call so a surfacer test can assert the
// deny-on-timeout was armed for the surfaced modal_id (#725). Handle runs
// single-goroutine in these tests, so no mutex is needed.
type fakeArmer struct {
	armed []string // modalID per ArmModalTimeout call, in order
}

func (f *fakeArmer) ArmModalTimeout(ctx context.Context, modalID string) {
	f.armed = append(f.armed, modalID)
}

func newModalEmitterTestDeps(conns []relay.ActiveConn) (*modalbridge.Registry, *fakeInteractiveBcast, *fakeArmer, *interactiveModalEmitterV2) {
	reg := modalbridge.New()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{conns}}
	armer := &fakeArmer{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return reg, bcast, armer, newInteractiveModalEmitterV2(reg, bcast, armer, logger)
}

func decodeModalShown(t *testing.T, env protocol.Envelope) protocol.ModalShownPayload {
	t.Helper()
	if env.Type != protocol.TypeModalShown {
		t.Fatalf("envelope type: got %q, want %q", env.Type, protocol.TypeModalShown)
	}
	var p protocol.ModalShownPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode modal_shown payload: %v", err)
	}
	return p
}

func TestModalEmitter_PermissionFanout(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{
		{ConnID: "c1", Interactive: true},
		{ConnID: "c2", Interactive: false},
		{ConnID: "c3", Interactive: true},
	}
	reg, bcast, armer, e := newModalEmitterTestDeps(conns)

	ev := tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassPermission}
	e.Handle(context.Background(), ev, "  a plain modal body  ")

	// Exactly one modal_shown per interactive conn; zero to the non-interactive.
	if got := len(bcast.pushes); got != 2 {
		t.Fatalf("push count: got %d, want 2 (one per interactive conn)", got)
	}
	if got := pushesFor(bcast.pushes, "c2"); len(got) != 0 {
		t.Errorf("non-interactive conn c2 received %d pushes, want 0", len(got))
	}

	first := bcast.pushes[0]
	payload := decodeModalShown(t, first.env)

	if payload.ModalID == "" {
		t.Fatal("ModalID must be non-empty")
	}
	if !conversations.ValidID(payload.ModalID) {
		t.Errorf("ModalID %q is not a canonical UUIDv4", payload.ModalID)
	}
	// #725: the deny-on-timeout is armed exactly once, for the surfaced modal_id.
	if len(armer.armed) != 1 || armer.armed[0] != payload.ModalID {
		t.Errorf("ArmModalTimeout calls = %v, want exactly [%s]", armer.armed, payload.ModalID)
	}
	// The minted id is recorded in the registry with the option list (AC2/AC4).
	got, ok := reg.Lookup(payload.ModalID)
	if !ok {
		t.Fatalf("registry.Lookup(%q): not found", payload.ModalID)
	}
	if len(got.Options) != len(payload.Options) || len(got.Options) == 0 {
		t.Errorf("registry option count %d != payload option count %d", len(got.Options), len(payload.Options))
	}

	// Plain-text body, no control bytes (AC3). 0x1b is the ESC byte.
	if payload.Prompt != "a plain modal body" {
		t.Errorf("Prompt: got %q, want trimmed plain body", payload.Prompt)
	}
	if strings.ContainsRune(payload.Prompt, 0x1b) {
		t.Error("Prompt contains an ESC control byte; want plain text only")
	}

	// Every conn observes the same modal_id; env.EventID is nil (control event,
	// not in the replay ring); per-conn env.ID is monotonically increasing.
	var lastID uint64
	for i, p := range bcast.pushes {
		pp := decodeModalShown(t, p.env)
		if pp.ModalID != payload.ModalID {
			t.Errorf("push %d ModalID %q != %q; the modal_id must be minted once per modal", i, pp.ModalID, payload.ModalID)
		}
		if p.env.EventID != nil {
			t.Errorf("push %d has non-nil EventID; modal_shown is a control event", i)
		}
		if p.env.ID <= lastID {
			t.Errorf("push %d env.ID %d not strictly greater than previous %d", i, p.env.ID, lastID)
		}
		lastID = p.env.ID
	}
}

func TestModalEmitter_Trust(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "c1", Interactive: true}}
	_, bcast, _, e := newModalEmitterTestDeps(conns)

	ev := tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassTrustFolder}
	e.Handle(context.Background(), ev, "trust this folder body")

	if len(bcast.pushes) != 1 {
		t.Fatalf("push count: got %d, want 1", len(bcast.pushes))
	}
	payload := decodeModalShown(t, bcast.pushes[0].env)
	if payload.Class != "trust" {
		t.Errorf("Class: got %q, want %q", payload.Class, "trust")
	}
	gotIDs := make([]string, len(payload.Options))
	for i, o := range payload.Options {
		gotIDs[i] = o.ID
	}
	want := []string{"proceed", "exit"}
	if len(gotIDs) != len(want) || gotIDs[0] != want[0] || gotIDs[1] != want[1] {
		t.Errorf("option ids: got %v, want %v", gotIDs, want)
	}
	if payload.DefaultOptionID != "exit" {
		t.Errorf("DefaultOptionID: got %q, want fail-safe %q", payload.DefaultOptionID, "exit")
	}
}

func TestModalEmitter_NonPermissionClass_NoOp(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "c1", Interactive: true}}
	_, bcast, armer, e := newModalEmitterTestDeps(conns)

	ev := tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassSlashPicker}
	e.Handle(context.Background(), ev, "/some picker")

	if len(bcast.pushes) != 0 {
		t.Errorf("non-permission/trust class produced %d pushes, want 0 (AC1)", len(bcast.pushes))
	}
	// #725: a non-permission class arms no deny-on-timeout.
	if len(armer.armed) != 0 {
		t.Errorf("non-permission class armed %d timeouts, want 0", len(armer.armed))
	}
}

func TestModalEmitter_NonModalEvent_NoOp(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "c1", Interactive: true}}
	_, bcast, armer, e := newModalEmitterTestDeps(conns)

	ev := tuidriver.Event{Kind: tuidriver.EventKindPtyIdle}
	e.Handle(context.Background(), ev, "")

	if len(bcast.pushes) != 0 {
		t.Errorf("non-modal event produced %d pushes, want 0", len(bcast.pushes))
	}
	// #725: a non-modal event arms no deny-on-timeout.
	if len(armer.armed) != 0 {
		t.Errorf("non-modal event armed %d timeouts, want 0", len(armer.armed))
	}
}

func TestModalEmitter_PushErrorContinues(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{
		{ConnID: "c1", Interactive: true},
		{ConnID: "c2", Interactive: true},
	}
	reg := modalbridge.New()
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{conns},
		pushErr:   map[string]error{"c1": errors.New("conn gone")},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := newInteractiveModalEmitterV2(reg, bcast, &fakeArmer{}, logger)

	ev := tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassPermission}
	e.Handle(context.Background(), ev, "body")

	// A failing conn does not stop the fan-out: both conns were attempted.
	if len(pushesFor(bcast.pushes, "c1")) != 1 {
		t.Errorf("c1 (failing) attempts: got %d, want 1", len(pushesFor(bcast.pushes, "c1")))
	}
	if len(pushesFor(bcast.pushes, "c2")) != 1 {
		t.Errorf("c2 attempts after c1 failed: got %d, want 1 (loop must continue)", len(pushesFor(bcast.pushes, "c2")))
	}
}

func decodeModalDismissed(t *testing.T, env protocol.Envelope) protocol.ModalDismissedPayload {
	t.Helper()
	if env.Type != protocol.TypeModalDismissed {
		t.Fatalf("envelope type: got %q, want %q", env.Type, protocol.TypeModalDismissed)
	}
	var p protocol.ModalDismissedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode modal_dismissed payload: %v", err)
	}
	return p
}

// dismissedPushes returns every recorded push that carried a modal_dismissed
// envelope, in call order.
func dismissedPushes(pushes []recordedPush) []recordedPush {
	var out []recordedPush
	for _, p := range pushes {
		if p.env.Type == protocol.TypeModalDismissed {
			out = append(out, p)
		}
	}
	return out
}

// newModalEmitterWithLogger builds an emitter wired to a fresh registry, a bcast
// scripted with conns, and a record-capturing JSON logger whose buffer is
// returned (so a test can assert the local-arm audit record). It returns the
// shared registry so a sibling resolver can be driven against it (the cross-head
// first-answer-wins tests).
func newModalEmitterWithLogger(conns []relay.ActiveConn) (*modalbridge.Registry, *fakeInteractiveBcast, *bytes.Buffer, *interactiveModalEmitterV2) {
	reg := modalbridge.New()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{conns}}
	logger, buf := auditLogger()
	e := newInteractiveModalEmitterV2(reg, bcast, &fakeArmer{}, logger)
	return reg, bcast, buf, e
}

// TestModalEmitter_LocalFirst_WinsThenRemoteRejected is AC3-(a): a local Hidden
// resolves the outstanding modal and emits exactly one modal_dismissed{local};
// a following remote modal_answer for the same modal_id is rejected — no
// keystroke, no second modal_dismissed. The capability gate (#607) is exercised
// too: a non-interactive conn receives neither the shown nor the dismissed event.
func TestModalEmitter_LocalFirst_WinsThenRemoteRejected(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{
		{ConnID: "c1", Interactive: true},
		{ConnID: "c2", Interactive: false},
		{ConnID: "c3", Interactive: true},
	}
	reg, bcast, eBuf, e := newModalEmitterWithLogger(conns)
	ctx := context.Background()

	// Surface a permission modal locally and capture the minted modal_id.
	e.Handle(ctx, tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassPermission}, "permission body")
	if len(bcast.pushes) != 2 {
		t.Fatalf("modal_shown push count: got %d, want 2 (one per interactive conn)", len(bcast.pushes))
	}
	modalID := decodeModalShown(t, bcast.pushes[0].env).ModalID
	if modalID == "" {
		t.Fatal("ModalID must be non-empty")
	}

	// Local Hidden for the outstanding modal -> exactly one modal_dismissed{local}
	// per interactive conn, carrying the same modal_id.
	e.Handle(ctx, tuidriver.Event{Kind: tuidriver.EventKindPtyModalHidden, Modal: tuidriver.ModalClassPermission}, "")

	dis := dismissedPushes(bcast.pushes)
	if len(dis) != 2 {
		t.Fatalf("modal_dismissed push count: got %d, want 2 (one per interactive conn)", len(dis))
	}
	for _, p := range dis {
		got := decodeModalDismissed(t, p.env)
		if got.ModalID != modalID {
			t.Errorf("modal_dismissed modal_id = %q, want %q", got.ModalID, modalID)
		}
		if got.Outcome != string(audit.OutcomeDismissedLocal) {
			t.Errorf("modal_dismissed outcome = %q, want %q", got.Outcome, audit.OutcomeDismissedLocal)
		}
		if got.Source != string(audit.SourceLocal) {
			t.Errorf("modal_dismissed source = %q, want %q", got.Source, audit.SourceLocal)
		}
	}
	// The non-interactive conn received nothing (capability gate, #607).
	if got := len(pushesFor(bcast.pushes, "c2")); got != 0 {
		t.Errorf("non-interactive conn c2 received %d pushes, want 0", got)
	}
	// The local arm consumed the registry entry.
	if _, ok := reg.Lookup(modalID); ok {
		t.Error("modal still in registry after local dismissal; Resolve must consume it")
	}

	// Exactly one audit record from the emitter: {dismissed_local, local}, no
	// answering device.
	recs := auditRecords(t, eBuf)
	if len(recs) != 1 {
		t.Fatalf("emitter audit records = %d, want 1", len(recs))
	}
	wantAudit := map[string]string{
		"outcome":      "dismissed_local",
		"source":       "local",
		"modal_id":     modalID,
		"modal_class":  "permission",
		"device_hash":  "",
		"device_label": "",
	}
	for k, want := range wantAudit {
		if got, _ := recs[0][k].(string); got != want {
			t.Errorf("audit field %q = %q, want %q", k, got, want)
		}
	}
	// A following remote modal_answer for the same id is the first-answer-wins
	// loser: rejected, no keystroke, no second modal_dismissed.
	kb := &fakeKeystroker{}
	rLogger, _ := auditLogger()
	r := newModalResolverV2(reg, kb, rLogger)
	d, ok := r.ResolveAnswer(modalID, "allow_once", "tok-1", eligibleDevice(t))
	if ok {
		t.Error("remote ResolveAnswer ok = true after a local dismissal, want false (already resolved)")
	}
	if d != (relay.ModalDismissal{}) {
		t.Errorf("remote dismissal = %+v, want zero", d)
	}
	if !kb.routedNothing() {
		t.Errorf("remote answer routed a keystroke after a local dismissal: esc=%d answer=%v trust=%d", kb.escCalls, kb.answerCalls, kb.trustCalls)
	}
	if got := len(dismissedPushes(bcast.pushes)); got != 2 {
		t.Errorf("modal_dismissed push count after rejected remote answer = %d, want still 2", got)
	}
}

// TestModalEmitter_RemoteFirst_LocalEmitsNothing is AC3-(b): a remote answer
// resolves and dismisses; a following local Hidden for the same modal_id emits
// no second modal_dismissed and writes no source=local audit record.
func TestModalEmitter_RemoteFirst_LocalEmitsNothing(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "c1", Interactive: true}}
	reg, bcast, eBuf, e := newModalEmitterWithLogger(conns)
	ctx := context.Background()

	e.Handle(ctx, tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassPermission}, "permission body")
	modalID := decodeModalShown(t, bcast.pushes[0].env).ModalID

	// Remote answer wins the race and consumes the modal.
	kb := &fakeKeystroker{}
	rLogger, _ := auditLogger()
	r := newModalResolverV2(reg, kb, rLogger)
	if _, ok := r.ResolveAnswer(modalID, "allow_once", "tok-1", eligibleDevice(t)); !ok {
		t.Fatal("remote ResolveAnswer ok = false, want true")
	}

	// Local Hidden now finds the modal already consumed: emitter emits nothing.
	e.Handle(ctx, tuidriver.Event{Kind: tuidriver.EventKindPtyModalHidden, Modal: tuidriver.ModalClassPermission}, "")

	if got := len(dismissedPushes(bcast.pushes)); got != 0 {
		t.Errorf("emitter pushed %d modal_dismissed after a remote win, want 0", got)
	}
	if recs := auditRecords(t, eBuf); len(recs) != 0 {
		t.Errorf("emitter wrote %d audit records after a remote win, want 0 (loser path)", len(recs))
	}
}

// TestModalEmitter_Hidden_NothingOutstanding_NoOp proves a local Hidden with no
// modal this emitter surfaced is a no-op: no push, no audit.
func TestModalEmitter_Hidden_NothingOutstanding_NoOp(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "c1", Interactive: true}}
	_, bcast, eBuf, e := newModalEmitterWithLogger(conns)

	e.Handle(context.Background(), tuidriver.Event{Kind: tuidriver.EventKindPtyModalHidden, Modal: tuidriver.ModalClassPermission}, "")

	if len(bcast.pushes) != 0 {
		t.Errorf("Hidden with nothing outstanding produced %d pushes, want 0", len(bcast.pushes))
	}
	if recs := auditRecords(t, eBuf); len(recs) != 0 {
		t.Errorf("Hidden with nothing outstanding wrote %d audit records, want 0", len(recs))
	}
}

// TestModalEmitter_Hidden_NonPermissionNeverSurfaced_NoOp proves a Hidden whose
// preceding Shown was a non-permission class (never surfaced, never tracked) is a
// no-op: outstandingID stayed "".
func TestModalEmitter_Hidden_NonPermissionNeverSurfaced_NoOp(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "c1", Interactive: true}}
	_, bcast, _, e := newModalEmitterWithLogger(conns)
	ctx := context.Background()

	// A slash-picker Shown is a no-op (AC1) and records nothing to track.
	e.Handle(ctx, tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassSlashPicker}, "/picker")
	e.Handle(ctx, tuidriver.Event{Kind: tuidriver.EventKindPtyModalHidden, Modal: tuidriver.ModalClassSlashPicker}, "")

	if len(bcast.pushes) != 0 {
		t.Errorf("non-permission Shown/Hidden produced %d pushes, want 0", len(bcast.pushes))
	}
}

// TestModalEmitter_Hidden_ClassMismatch_KeepsTracking proves the defensive
// class-mismatch branch: a Hidden for a different class than the outstanding one
// neither resolves nor clears the tracking, so the correct Hidden still resolves
// the modal afterwards. (Unreachable under tui-driver's single-modal invariant,
// but the branch must fail safe.)
func TestModalEmitter_Hidden_ClassMismatch_KeepsTracking(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "c1", Interactive: true}}
	reg, bcast, _, e := newModalEmitterWithLogger(conns)
	ctx := context.Background()

	e.Handle(ctx, tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassPermission}, "permission body")
	modalID := decodeModalShown(t, bcast.pushes[0].env).ModalID

	// A Hidden for a different class: no dismissal, modal still outstanding.
	e.Handle(ctx, tuidriver.Event{Kind: tuidriver.EventKindPtyModalHidden, Modal: tuidriver.ModalClassTrustFolder}, "")
	if got := len(dismissedPushes(bcast.pushes)); got != 0 {
		t.Errorf("class-mismatch Hidden pushed %d modal_dismissed, want 0", got)
	}
	if _, ok := reg.Lookup(modalID); !ok {
		t.Error("class-mismatch Hidden consumed the modal; it must stay outstanding")
	}

	// The correct Hidden still resolves it.
	e.Handle(ctx, tuidriver.Event{Kind: tuidriver.EventKindPtyModalHidden, Modal: tuidriver.ModalClassPermission}, "")
	if got := len(dismissedPushes(bcast.pushes)); got != 1 {
		t.Errorf("matching Hidden after a mismatch pushed %d modal_dismissed, want 1", got)
	}
	if _, ok := reg.Lookup(modalID); ok {
		t.Error("matching Hidden did not consume the modal")
	}
}
