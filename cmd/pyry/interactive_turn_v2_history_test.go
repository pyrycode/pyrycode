package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// historyEntries reads convID's whole log back, oldest-first. Page answers
// newest-first, so the slice is reversed; AtStart is asserted so a page that
// silently truncated cannot pass as a complete log.
func historyEntries(t *testing.T, store *history.Store, convID string) []history.Entry {
	t.Helper()
	page, err := store.Page(conversations.ConversationID(convID), "", 128)
	if err != nil {
		t.Fatalf("read log for %q: %v", convID, err)
	}
	if !page.AtStart {
		t.Fatalf("log for %q did not fit in one page; the fixture is too large to compare whole", convID)
	}
	out := make([]history.Entry, len(page.Entries))
	for i, e := range page.Entries {
		out[len(page.Entries)-1-i] = e
	}
	return out
}

// assertLogMatchesRing is AC-1's comparison: for a conversation whose traffic
// fits inside the ring's per-conversation bound, the log holds the same events
// in the same order, with the same wire type, the same payload BYTES and the
// same timestamp the fan-out carried. The durable ids differ by construction —
// the ring's restart at 1 on every daemon start is the whole reason the log
// exists — so they are not compared.
func assertLogMatchesRing(t *testing.T, entries []history.Entry, evs []eventring.Event) {
	t.Helper()
	if len(entries) != len(evs) {
		t.Fatalf("log holds %d entries, ring holds %d events", len(entries), len(evs))
	}
	if len(evs) == 0 {
		t.Fatal("fixture produced no events; the comparison would be vacuous")
	}
	for i := range evs {
		if entries[i].Type != evs[i].Type {
			t.Fatalf("entry %d type = %q, ring = %q", i, entries[i].Type, evs[i].Type)
		}
		if !bytes.Equal(entries[i].Payload, evs[i].Payload) {
			t.Fatalf("entry %d payload differs from the ring's:\n log %s\nring %s", i, entries[i].Payload, evs[i].Payload)
		}
		if !entries[i].TS.Equal(evs[i].TS) {
			t.Fatalf("entry %d ts = %s, ring = %s", i, entries[i].TS, evs[i].TS)
		}
	}
}

// historyFixtureEvents is the scripted turn every test below drives: it exercises
// the state machine's transitions plus a content envelope and a turn end, so the
// comparison covers more than one wire type. Well inside
// eventring.MaxEventsPerConversation, which is what makes the ring a complete
// reference rather than a truncated one.
func historyFixtureEvents() []turnevent.Event {
	return []turnevent.Event{
		turnevent.ThoughtChunk{Text: "reasoning"},
		turnevent.TextChunk{Text: "hello"},
		turnevent.ToolStart{ToolCallID: "t1", Title: "Read"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	}
}

// AC-1: every envelope the interactive chokepoint fans out is appended to its
// conversation's log with the same wire type, payload bytes and timestamp.
//
// The conversation id MUST be canonical: Append refuses any other shape with
// ErrInvalidID, and a log that reads empty for that reason is indistinguishable
// from a producer that was never wired at all.
func TestInteractiveTurnEmitterV2_HistoryMatchesRing(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	store := history.New(t.TempDir())
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
	e.hist = store

	for _, ev := range historyFixtureEvents() {
		e.Handle(context.Background(), ev)
	}

	evs, gap := e.ring.After(testConvID, 0)
	if gap {
		t.Fatal("ring reports a gap for a conversation well inside its bound")
	}
	assertLogMatchesRing(t, historyEntries(t, store, testConvID), evs)

	// The fan-out itself is unchanged: the same envelopes still reached the conn.
	if got, want := len(bcast.pushes), len(evs); got != want {
		t.Fatalf("pushed %d envelopes, ring recorded %d", got, want)
	}
}

// AC-2: the append happens once per logical event, BEFORE the per-conn fan-out,
// so a conversation with no interactive connection open still accumulates
// history. Zero conns is the proof: the ring already behaves this way and the
// log must too.
func TestInteractiveTurnEmitterV2_HistoryWithNoConnsOpen(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{} // no snapshots ⇒ ActiveConns returns nil
	store := history.New(t.TempDir())
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
	e.hist = store

	for _, ev := range historyFixtureEvents() {
		e.Handle(context.Background(), ev)
	}

	if len(bcast.pushes) != 0 {
		t.Fatalf("pushed %d envelopes with no conns open; want 0", len(bcast.pushes))
	}
	evs, _ := e.ring.After(testConvID, 0)
	assertLogMatchesRing(t, historyEntries(t, store, testConvID), evs)
}

// AC-4: a failing log append suppresses neither the wire emit nor the ring
// append, and the failure is reported with the conversation id and the error
// identity — never the payload bytes.
//
// A non-canonical cursor is the cheap induction, and it is the shape several
// existing tests here already run under.
func TestInteractiveTurnEmitterV2_HistoryFailureDoesNotSuppressEmit(t *testing.T) {
	t.Parallel()
	const badConv = "conv-x"
	cur := &stubCursor{}
	cur.set(badConv)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	store := history.New(t.TempDir())
	var buf bytes.Buffer
	e := newInteractiveTurnEmitterV2(cur, bcast, bufLogger(&buf))
	e.hist = store

	for _, ev := range historyFixtureEvents() {
		e.Handle(context.Background(), ev)
	}

	// The wire emit stands.
	wantTypes := []string{
		protocol.TypeTurnState,      // thinking
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // hello
		protocol.TypeToolUse,        // Read
		protocol.TypeTurnEnd,        // end_turn
		protocol.TypeTurnState,      // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("envelope type order:\n got %v\nwant %v", got, wantTypes)
	}
	// The ring append stands.
	evs, _ := e.ring.After(badConv, 0)
	if len(evs) != len(wantTypes) {
		t.Fatalf("ring holds %d events, want %d", len(evs), len(wantTypes))
	}
	// The log refuses the id, and says so without echoing content.
	if _, err := store.Page(conversations.ConversationID(badConv), "", 16); !errors.Is(err, history.ErrInvalidID) {
		t.Fatalf("Page(%q) error = %v, want ErrInvalidID", badConv, err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "reason=invalid_id") {
		t.Fatalf("failure log %q does not carry reason=invalid_id", logged)
	}
	if !strings.Contains(logged, badConv) {
		t.Fatalf("failure log %q does not carry the conversation id", logged)
	}
	if strings.Contains(logged, "hello") || strings.Contains(logged, "Read") {
		t.Fatalf("failure log leaked payload content: %q", logged)
	}
}

// BenchmarkInteractiveEmitHistoryAppend is AC-5: the cost the append adds in
// FRONT of the fan-out, measured at the chokepoint with a store wired and with
// none. Zero conns, so the number is the append's own cost and not the fan-out's.
// The delta and the decision it justifies are recorded in the ticket spec.
func BenchmarkInteractiveEmitHistoryAppend(b *testing.B) {
	payload := protocol.AssistantDeltaPayload{
		ConversationID: testConvID,
		TurnID:         "turn-1",
		Seq:            1,
		Text:           strings.Repeat("assistant text ", 16),
	}

	run := func(b *testing.B, store *history.Store) {
		cur := &stubCursor{}
		cur.set(testConvID)
		e := newInteractiveTurnEmitterV2(cur, &fakeInteractiveBcast{}, discardLogger())
		e.hist = store
		ctx := context.Background()
		b.ReportAllocs()
		for b.Loop() {
			e.emit(ctx, testConvID, protocol.TypeAssistantDelta, payload)
		}
	}

	b.Run("no_store", func(b *testing.B) { run(b, nil) })
	b.Run("store", func(b *testing.B) { run(b, history.New(b.TempDir())) })
}
