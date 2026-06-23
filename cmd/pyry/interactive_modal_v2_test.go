package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

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
