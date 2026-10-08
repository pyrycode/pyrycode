package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func TestSessionTransitionHistory_CapturedProvenance(t *testing.T) {
	t.Parallel()
	for _, reason := range []sessions.TransitionReason{sessions.ReasonClear, sessions.ReasonEviction} {
		for _, kind := range []string{"claude", "codex"} {
			t.Run(string(reason)+"/"+kind, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				store := history.New(dir)
				if _, err := store.Append(conversations.ConversationID(testConvID), "legacy", []byte(`{}`), occurred); err != nil {
					t.Fatal(err)
				}
				bcast := mixedSnapshot()
				e := newSessionTransitionEmitterV2(bcast, func(string) (string, bool) {
					t.Fatal("captured ownership must bypass replacement registry lookup")
					return "", false
				}, discardLogger())
				e.hist = store
				tr := sessions.SessionTransition{PreviousID: "previous", NewID: "successor", Reason: reason,
					ConversationID: testConvID, PreviousAgent: kind, NextAgent: kind, OccurredAt: occurred}
				wantID, wireReason := "successor", "clear"
				if reason == sessions.ReasonEviction {
					tr.NewID, tr.NextAgent = "", ""
					wantID, wireReason = "previous", "idle_evict"
				}
				e.broadcast(context.Background(), tr)
				if len(bcast.pushes) != 1 || bcast.pushes[0].connID != "i" {
					t.Fatalf("recipient gate: %+v", bcast.pushes)
				}
				env := bcast.pushes[0].env
				wantPayload := protocol.SessionTransitionPayload{ConversationID: testConvID, PreviousSessionID: "previous",
					NewSessionID: wantID, Reason: wireReason, OccurredAt: occurred}
				wantJSON, err := json.Marshal(wantPayload)
				if err != nil || !bytes.Equal(env.Payload, wantJSON) {
					t.Fatalf("legacy payload=%s, want %s; err=%v", env.Payload, wantJSON, err)
				}
				if bytes.Contains(env.Payload, []byte(`"session"`)) || bytes.Contains(env.Payload, []byte(`"kind"`)) {
					t.Fatal("provenance leaked into legacy payload")
				}
				for _, reader := range []*history.Store{store, history.New(dir)} {
					entries := historyEntries(t, reader, testConvID)
					if len(entries) != 2 || entries[0].Session != nil || entries[0].Shown != nil {
						t.Fatalf("legacy entry changed: %+v", entries)
					}
					got := entries[1]
					want := &history.SessionProvenance{Kind: kind, SessionID: wantID}
					if !reflect.DeepEqual(got.Session, want) || got.Shown == nil || *got.Shown != (reason == sessions.ReasonClear) {
						t.Fatalf("metadata=%+v shown=%v, want %+v", got.Session, got.Shown, want)
					}
					if got.ID != 2 || env.HistoryEntryID == nil || *env.HistoryEntryID != got.ID || !got.TS.Equal(env.TS) || !bytes.Equal(got.Payload, env.Payload) {
						t.Fatalf("durable identity/payload differs: %+v, %+v", got, env)
					}
					latest, err := reader.LatestEntryID(conversations.ConversationID(testConvID))
					if err != nil || latest != 2 {
						t.Fatalf("watermark=%d err=%v", latest, err)
					}
					shown, err := reader.LatestDisplayableEntryID(conversations.ConversationID(testConvID))
					wantShown := uint64(1)
					if reason == sessions.ReasonClear {
						wantShown = 2
					}
					if err != nil || shown != wantShown {
						t.Fatalf("shown watermark=%d want=%d err=%v", shown, wantShown, err)
					}
					page := newHistoryPager(reader, discardLogger())(testConvID, "", 10)
					if page.Outcome != relay.HistoryPageOK || len(page.Entries) != 1 || !bytes.Equal(page.Entries[0].Payload, env.Payload) {
						t.Fatalf("legacy page changed: %+v", page)
					}
				}
			})
		}
	}
}

func TestSessionTransitionHandoff_CapturesBeforeDelay(t *testing.T) {
	t.Parallel()
	for _, reason := range []sessions.TransitionReason{sessions.ReasonClear, sessions.ReasonEviction} {
		for _, kind := range []string{"claude", "codex"} {
			for _, captured := range []bool{false, true} {
				t.Run(string(reason)+"/"+kind+"/captured="+strconv.FormatBool(captured), func(t *testing.T) {
					t.Parallel()
					owner, agent := testConvID, kind
					available := true
					tr := sessions.SessionTransition{PreviousID: "previous", NewID: "successor", Reason: reason}
					sid := "successor"
					if reason == sessions.ReasonEviction {
						tr.NewID, sid = "", "previous"
					}
					if captured {
						tr.ConversationID, tr.NextAgent, tr.PreviousAgent = owner, kind, kind
					}
					bcast := mixedSnapshot()
					e := newSessionTransitionEmitterV2(bcast, func(id string) (string, bool) {
						if captured || id != sid {
							t.Fatalf("unexpected owner lookup for %q", id)
						}
						return owner, available
					}, discardLogger())
					e.hist = history.New(t.TempDir())
					e.resolveAgent = func(id string) (string, bool) {
						if captured || id != sid {
							t.Fatalf("unexpected agent lookup for %q", id)
						}
						return agent, available
					}
					e.Enqueue(tr)
					queued := <-e.in
					// Replace the binding, then remove all exact-session lookup facts.
					owner, agent, available = "later-owner", "replacement-agent", false
					e.broadcast(context.Background(), queued)
					entries := historyEntries(t, e.hist, testConvID)
					want := history.SessionProvenance{Kind: kind, SessionID: sid}
					if len(entries) != 1 || entries[0].Session == nil || *entries[0].Session != want || len(bcast.pushes) != 1 {
						t.Fatalf("delayed attribution: entries=%+v pushes=%+v", entries, bcast.pushes)
					}
					if got := decodeSessionTransition(t, bcast.pushes[0].env); got.ConversationID != testConvID {
						t.Fatalf("delayed routing: %+v", got)
					}
				})
			}
		}
	}
}

func TestSessionTransitionHandoff_LookupMissStaysUntagged(t *testing.T) {
	t.Parallel()
	bcast := mixedSnapshot()
	e := newSessionTransitionEmitterV2(bcast, constResolver(testConvID, true), discardLogger())
	e.hist = history.New(t.TempDir())
	available := false
	e.resolveAgent = func(string) (string, bool) { return "codex", available }
	e.Enqueue(sessions.SessionTransition{Reason: sessions.ReasonEviction, PreviousID: "evicted"})
	queued := <-e.in
	available = true
	e.broadcast(context.Background(), queued)
	entries := historyEntries(t, e.hist, testConvID)
	if len(entries) != 1 || entries[0].Session != nil || len(bcast.pushes) != 1 {
		t.Fatalf("late lookup invented provenance: %+v", entries)
	}
}

func TestSessionTransitionHandoff_RemovedExactSession(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			pool, reg, _ := relaySwitchFixture(t)
			runPoolReady(t, pool)
			sid, err := pool.MintAs("source", t.TempDir(), kind)
			if err != nil {
				t.Fatal(err)
			}
			reg.Update(switchConvID, func(c *conversations.Conversation) { c.CurrentSessionID = string(sid) })
			bcast := mixedSnapshot()
			e := newSessionTransitionEmitterV2(bcast, func(id string) (string, bool) { return conversationForSession(reg, id) }, discardLogger())
			e.resolveAgent, e.hist = sessionHarness(pool), history.New(t.TempDir())
			e.Enqueue(sessions.SessionTransition{Reason: sessions.ReasonClear, PreviousID: "predecessor", NewID: sid, ConversationID: switchConvID})
			queued := <-e.in
			reg.RebindSession(string(sid), "later-successor")
			if err := pool.Remove(context.Background(), sid, sessions.RemoveOptions{}); err != nil {
				t.Fatal(err)
			}
			reg.Delete(switchConvID)
			reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID), CurrentSessionID: string(sid)})
			e.broadcast(context.Background(), queued)
			entries := historyEntries(t, e.hist, string(switchConvID))
			want := history.SessionProvenance{Kind: kind, SessionID: string(sid)}
			if len(entries) != 1 || entries[0].Session == nil || *entries[0].Session != want {
				t.Fatalf("removed source=%+v, want %+v", entries, want)
			}
			if got := decodeSessionTransition(t, bcast.pushes[0].env); got.ConversationID != switchConvID {
				t.Fatalf("replacement owner overrode captured routing: %+v", got)
			}
		})
	}
}

func TestSessionTransitionHandoff_CommittedSwitch(t *testing.T) {
	t.Parallel()
	for _, captured := range []bool{false, true} {
		t.Run(strconv.FormatBool(captured), func(t *testing.T) {
			t.Parallel()
			bcast := mixedSnapshot()
			e := newSessionTransitionEmitterV2(bcast, constResolver(testConvID, true), discardLogger())
			e.hist = history.New(t.TempDir())
			calls := 0
			e.resolveAgent = func(id string) (string, bool) {
				calls++
				return "codex", id == "successor"
			}
			tr := sessions.SessionTransition{Reason: sessions.ReasonClear, AgentSwitch: true,
				ConversationID: testConvID, PreviousID: "previous", NewID: "successor", PreviousAgent: "claude"}
			if captured {
				tr.NextAgent = "claude" // captured facts win even if lookup would disagree
			}
			e.Enqueue(tr)
			if len(e.in) != 0 {
				t.Fatal("switch entered ordinary lane")
			}
			done := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { defer close(done); e.publishSwitch(ctx, tr) }()
			req := <-e.switches
			wantCalls, wantKind := 1, "codex"
			if captured {
				wantCalls, wantKind = 0, "claude"
			}
			if calls != wantCalls {
				t.Fatalf("handoff lookups=%d, want %d", calls, wantCalls)
			}
			e.resolveAgent = func(string) (string, bool) { t.Error("drain-time agent lookup"); return "", false }
			e.broadcast(ctx, req.transition)
			close(req.done)
			awaitSwitchPublication(t, done)
			entries := historyEntries(t, e.hist, testConvID)
			want := history.SessionProvenance{Kind: wantKind, SessionID: "successor"}
			if len(entries) != 1 || entries[0].Session == nil || *entries[0].Session != want {
				t.Fatalf("switch provenance=%+v, want %+v", entries, want)
			}
		})
	}
}

func TestSessionTransitionHistory_NoRelayObserver(t *testing.T) {
	t.Parallel()
	pool, reg, _ := relaySwitchFixture(t)
	runPoolReady(t, pool)
	sid, err := pool.MintAs("codex", t.TempDir(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	reg.Update(switchConvID, func(c *conversations.Conversation) { c.CurrentSessionID = string(sid) })
	sink := &captureObserverSink{}
	store := history.New(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	stop, _, _, _, _, _, err := startRelay(ctx, discardLogger(), relayWiring{
		streamSink: newStreamTurnSink(4, discardLogger()), transitions: sink, convReg: reg,
		hist: store, sessionHarness: sessionHarness(pool),
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); stop() }()
	sink.obs(sessions.SessionTransition{Reason: sessions.ReasonEviction, PreviousID: sid, ConversationID: switchConvID})
	if err := pool.Remove(ctx, sid, sessions.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		entries := historyEntries(t, store, switchConvID)
		if len(entries) == 1 {
			want := history.SessionProvenance{Kind: "codex", SessionID: string(sid)}
			if entries[0].Session == nil || *entries[0].Session != want {
				t.Fatalf("history-only observer provenance=%+v, want %+v", entries[0].Session, want)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("history-only observer did not append")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSessionTransitionHistory_UnavailableProvenance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, agent, sid string
	}{
		{"unknown agent", "", "successor"}, {"unsupported agent", "other", "successor"},
		{"none is not a session agent", "none", "successor"}, {"missing session", "codex", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bcast := mixedSnapshot()
			e := newSessionTransitionEmitterV2(bcast, constResolver(testConvID, true), discardLogger())
			e.hist = history.New(t.TempDir())
			e.broadcast(context.Background(), sessions.SessionTransition{Reason: sessions.ReasonClear,
				ConversationID: testConvID, NewID: sessions.SessionID(tc.sid), NextAgent: tc.agent})
			entries := historyEntries(t, e.hist, testConvID)
			if len(bcast.pushes) != 1 || len(entries) != 1 || entries[0].Session != nil {
				t.Fatalf("eligible untagged delivery: pushes=%d entries=%+v", len(bcast.pushes), entries)
			}
		})
	}
}

func TestSessionTransitionHistory_UnavailableStorage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, store := range []*history.Store{nil, history.New(path)} {
		bcast := mixedSnapshot()
		e := newSessionTransitionEmitterV2(bcast, constResolver("", false), discardLogger())
		e.hist = store
		e.broadcast(context.Background(), sessions.SessionTransition{Reason: sessions.ReasonClear, ConversationID: testConvID,
			NewID: "successor", NextAgent: "codex"})
		if len(bcast.pushes) != 1 || bcast.pushes[0].env.HistoryEntryID != nil {
			t.Fatalf("unavailable store suppressed delivery or invented identity: %+v", bcast.pushes)
		}
	}
}

// AC-3: a session transition is appended ONCE per transition, carrying one
// timestamp shared with the fan-out — not one entry and not one timestamp per
// connected conn. Three interactive conns is what makes the difference visible:
// before the hoist, broadcast minted time.Now() inside the per-conn loop, so the
// three envelopes carried three distinct timestamps and no single log entry
// could match them all.
//
// This is also the only place a session boundary is retained at all: this
// producer deliberately skips the #647 replay ring.
func TestSessionTransitionEmitterV2_HistoryOncePerTransition(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "i1", Interactive: true},
		{ConnID: "i2", Interactive: true},
		{ConnID: "i3", Interactive: true},
	}}}
	store := history.New(t.TempDir())
	e := newSessionTransitionEmitterV2(bcast, constResolver(testConvID, true), discardLogger())
	e.hist = store

	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred,
	})

	if len(bcast.pushes) != 3 {
		t.Fatalf("fanned out %d envelopes, want one per interactive conn (3)", len(bcast.pushes))
	}
	entries := historyEntries(t, store, testConvID)
	if len(entries) != 1 {
		t.Fatalf("log holds %d entries for one transition, want exactly 1", len(entries))
	}
	got := entries[0]
	if got.Type != protocol.TypeSessionTransition {
		t.Fatalf("entry type = %q, want %q", got.Type, protocol.TypeSessionTransition)
	}
	for i, p := range bcast.pushes {
		if !got.TS.Equal(p.env.TS) {
			t.Fatalf("entry ts = %s but conn %d's envelope carries %s; the fan-out timestamp is not hoisted",
				got.TS, i, p.env.TS)
		}
		if !bytes.Equal(got.Payload, p.env.Payload) {
			t.Fatalf("entry payload differs from conn %d's envelope:\n log %s\nwire %s", i, got.Payload, p.env.Payload)
		}
	}
	if decoded := decodeSessionTransition(t, bcast.pushes[0].env); decoded.ConversationID != testConvID {
		t.Fatalf("stamped conversation_id = %q, want %q", decoded.ConversationID, testConvID)
	}
}

// A transition that never reaches the wire never reaches the log either: both
// existing drops return before the append, so the log records what was fanned
// out and never what was refused.
func TestSessionTransitionEmitterV2_DroppedTransitionsWriteNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		resolve func(string) (string, bool)
		trans   sessions.SessionTransition
	}{
		{
			name:    "internal recovery",
			resolve: constResolver(testConvID, true),
			trans:   sessions.SessionTransition{PreviousID: "sess-a", NewID: "sess-b", Cause: sessions.CauseRecovery, ConversationID: testConvID, OccurredAt: occurred},
		},
		{
			name:    "unknown reason",
			resolve: constResolver(testConvID, true),
			trans: sessions.SessionTransition{
				PreviousID: "sess-a", Reason: sessions.TransitionReason("frobnicate"), OccurredAt: occurred,
			},
		},
		{
			name:    "unresolvable conversation",
			resolve: constResolver("", false),
			trans: sessions.SessionTransition{
				PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "i1", Interactive: true}}}}
			store := history.New(t.TempDir())
			var buf bytes.Buffer
			e := newSessionTransitionEmitterV2(bcast, tt.resolve, bufLogger(&buf))
			e.hist = store

			e.broadcast(context.Background(), tt.trans)

			if len(bcast.pushes) != 0 {
				t.Fatalf("fanned out %d envelopes for a dropped transition; want 0", len(bcast.pushes))
			}
			if entries := historyEntries(t, store, testConvID); len(entries) != 0 {
				t.Fatalf("log holds %d entries for a dropped transition; want 0", len(entries))
			}
			// The drop's own Debug line is below the buffer logger's level; a Warn
			// here would mean an append was attempted and failed.
			if buf.Len() != 0 {
				t.Fatalf("dropped transition logged at Warn: %q", buf.String())
			}
		})
	}
}
