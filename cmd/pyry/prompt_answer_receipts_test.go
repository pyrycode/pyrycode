package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
)

func testPromptReceiptRaw(t *testing.T, ts time.Time, edit func(map[string]any)) json.RawMessage {
	t.Helper()
	f := map[string]any{"conversation_id": testConvID, "correlation_id": "private-correlation", "session_id": "private-session", "resolved_at": ts, "source": "remote", "decision": "allow", "behavior": "allow", "session_grant": false, "context": map[string]any{"tool": "Bash", "class": "permission"}, "truncated": false}
	if edit != nil {
		edit(f)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testPromptReceipt(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"conversation_id": testConvID, "level": "info", "text": "", "truncated": false, "stops_turn": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt = %#v, want %#v", got, want)
	}
}

func TestPromptAnswerReceiptsValidation(t *testing.T) {
	ts := time.Date(2026, 10, 12, 1, 2, 3, 4, time.UTC)
	tests := []struct {
		name  string
		edit  func(map[string]any)
		valid bool
	}{
		{"allow", nil, true},
		{"answer", func(f map[string]any) {
			f["decision"] = "answer"
			f["context"] = map[string]any{"questions": []any{map[string]any{"index": 0, "text": "Private question", "multi_select": false, "values": []any{map[string]any{"text": "private answer", "meaning": "private meaning"}}}}}
		}, true},
		{"deny", func(f map[string]any) { f["decision"] = "deny"; f["behavior"] = "deny" }, true},
		{"cancel", func(f map[string]any) { f["decision"] = "cancel"; f["behavior"] = "deny" }, true},
		{"refusal", func(f map[string]any) { f["decision"] = "refusal"; f["behavior"] = "deny" }, true},
		{"grant", func(f map[string]any) { f["session_grant"] = true }, true},
		{"optional-absent", func(f map[string]any) { delete(f, "session_id"); f["context"] = map[string]any{} }, true},
		{"foreign", func(f map[string]any) { f["conversation_id"] = testConvIDB }, false},
		{"source", func(f map[string]any) { f["source"] = "local" }, false},
		{"decision", func(f map[string]any) { f["decision"] = "unknown" }, false},
		{"behavior", func(f map[string]any) { f["behavior"] = "deny" }, false},
		{"denied-grant", func(f map[string]any) { f["decision"] = "deny"; f["behavior"] = "deny"; f["session_grant"] = true }, false},
		{"correlation", func(f map[string]any) { f["correlation_id"] = "" }, false},
		{"zero-time", func(f map[string]any) { f["resolved_at"] = time.Time{} }, false},
		{"different-time", func(f map[string]any) { f["resolved_at"] = ts.Add(time.Second) }, false},
		{"null-context", func(f map[string]any) { f["context"] = nil }, false},
		{"array-context", func(f map[string]any) { f["context"] = []any{} }, false},
		{"unknown-context", func(f map[string]any) { f["context"] = map[string]any{"input": "private"} }, false},
		{"wrong-tool", func(f map[string]any) { f["context"] = map[string]any{"tool": 42} }, false},
		{"oversize", func(f map[string]any) {
			f["context"] = map[string]any{"tool": strings.Repeat("x", maxPromptAnswerProjection)}
		}, false},
		{"question-fields", func(f map[string]any) { f["context"] = map[string]any{"questions": []any{map[string]any{"index": 0}}} }, false},
		{"question-index", func(f map[string]any) {
			f["context"] = map[string]any{"questions": []any{map[string]any{"index": -1, "text": "q", "multi_select": false}}}
		}, false},
		{"value-fields", func(f map[string]any) {
			f["context"] = map[string]any{"questions": []any{map[string]any{"index": 0, "text": "q", "multi_select": false, "values": []any{map[string]any{"meaning": "missing text"}}}}}
		}, false},
	}
	for _, key := range []string{"conversation_id", "correlation_id", "resolved_at", "source", "decision", "behavior", "session_grant", "context", "truncated"} {
		key := key
		tests = append(tests, struct {
			name  string
			edit  func(map[string]any)
			valid bool
		}{"missing-" + key, func(f map[string]any) { delete(f, key) }, false}, struct {
			name  string
			edit  func(map[string]any)
			valid bool
		}{"null-" + key, func(f map[string]any) { f[key] = nil }, false})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := history.New(t.TempDir())
			raw := testPromptReceiptRaw(t, ts, tt.edit)
			shown := true
			id, err := store.AppendWithMetadata(conversations.ConversationID(testConvID), historyPromptAnswered, raw, ts, history.Metadata{Shown: &shown})
			if err != nil {
				t.Fatal(err)
			}
			page := newHistoryPager(store, discardLogger())(testConvID, "", 1)
			if (len(page.Entries) == 1) != tt.valid {
				t.Fatalf("entries=%d valid=%v", len(page.Entries), tt.valid)
			}
			if tt.valid {
				e := page.Entries[0]
				if e.ID != id || !e.TS.Equal(ts) || e.Type != protocol.TypeBanner {
					t.Fatalf("identity %#v", e)
				}
				testPromptReceipt(t, e.Payload)
			}
		})
	}
	raw := testPromptReceiptRaw(t, ts, nil)
	id := uint64(1)
	zero := uint64(0)
	for _, identity := range []*uint64{nil, &zero} {
		if _, ok := legacyRuntimeReceipt(testConvID, historyPromptAnswered, raw, identity, ts); ok {
			t.Fatal("unidentified receipt")
		}
	}
	for _, bad := range []json.RawMessage{json.RawMessage(`{`), json.RawMessage(`[]`)} {
		if _, ok := legacyRuntimeReceipt(testConvID, historyPromptAnswered, bad, &id, ts); ok {
			t.Fatal("malformed receipt")
		}
	}
	if _, ok := legacyRuntimeReceipt(testConvID, historyPromptAnswered, raw, &id, time.Time{}); ok {
		t.Fatal("zero timestamp receipt")
	}
	if legacyHistoryType(historyPromptAnswered) {
		t.Fatal("raw answer admitted")
	}
}

func TestPromptAnswerReceiptsPages(t *testing.T) {
	for _, mode := range []string{"completed-turn", "receipt-only", "mixed"} {
		for _, visibility := range []string{"absent", "hidden", "shown"} {
			t.Run(mode+"/"+visibility, func(t *testing.T) {
				dir := t.TempDir()
				store := history.New(dir)
				conv := conversations.ConversationID(testConvID)
				var shown *bool
				if visibility != "absent" {
					v := visibility == "shown"
					shown = &v
				}
				ts := time.Date(2026, 10, 12, 1, 2, 3, 4, time.UTC)
				appendEntry := func(typ string, p any) {
					t.Helper()
					raw, err := json.Marshal(p)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = store.AppendWithMetadata(conv, typ, raw, ts, history.Metadata{Shown: shown}); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "completed-turn" {
					appendEntry(protocol.TypeToolUse, protocol.ToolUsePayload{ConversationID: testConvID, TurnID: "turn", ToolUseID: "tool", Name: "Bash"})
				}
				appendEntry(historyPromptAnswered, testPromptReceiptRaw(t, ts, nil))
				if mode == "mixed" {
					appendEntry(historyPromptAnswered, testPromptReceiptRaw(t, ts, func(f map[string]any) { delete(f, "correlation_id") }))
				}
				if mode == "completed-turn" {
					appendEntry(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: testConvID, TurnID: "turn", Text: "private reply"})
					appendEntry(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: testConvID, TurnID: "turn", StopReason: "end_turn"})
				}
				rawBefore := historyEntries(t, store, testConvID)
				for _, reader := range []*history.Store{store, history.New(dir)} {
					for _, limit := range []int{1, 2, 10} {
						cursor := ""
						accounted := map[uint64]bool{}
						for {
							raw, err := reader.Page(conv, cursor, limit)
							if err != nil {
								t.Fatal(err)
							}
							page := newHistoryPager(reader, discardLogger())(testConvID, cursor, limit)
							if page.Cursor != raw.Cursor || page.AtStart != raw.AtStart || len(page.Entries) > len(raw.Entries) {
								t.Fatal("raw boundary changed")
							}
							expected := make([]uint64, 0, len(raw.Entries))
							for _, e := range raw.Entries {
								if mode != "mixed" || e.ID != 2 {
									expected = append(expected, e.ID)
								}
							}
							ids := make([]uint64, 0, len(page.Entries))
							for _, e := range page.Entries {
								ids = append(ids, e.ID)
							}
							if !reflect.DeepEqual(ids, expected) {
								t.Fatalf("page IDs=%v, raw projection=%v", ids, expected)
							}
							for _, e := range page.Entries {
								accounted[e.ID] = true
								if e.Type == protocol.TypeBanner {
									testPromptReceipt(t, e.Payload)
								}
								if !e.TS.Equal(ts) {
									t.Fatal("timestamp changed")
								}
							}
							if page.AtStart {
								break
							}
							if page.Cursor == "" || page.Cursor == cursor {
								t.Fatal("cursor did not advance")
							}
							cursor = page.Cursor
						}
						for _, e := range rawBefore {
							want := !(mode == "mixed" && e.ID == 2)
							if accounted[e.ID] != want {
								t.Fatalf("ID %d accounted=%v want=%v (limit %d)", e.ID, accounted[e.ID], want, limit)
							}
						}
					}
				}
				if !reflect.DeepEqual(rawBefore, historyEntries(t, store, testConvID)) {
					t.Fatal("raw history mutated")
				}
				if mode == "receipt-only" {
					entries := newHistoryPager(store, discardLogger())(testConvID, "", 10).Entries
					if testLegacyCheckpoint(entries, 0, 0, false) != 0 {
						t.Fatal("receipt established presentation")
					}
					if testLegacyLatest(entries) != 1 {
						t.Fatal("receipt did not count toward legacy target")
					}
					latest, err := (legacyHistoryReader{store}).LatestDisplayableEntryID(conv)
					if err != nil || latest != 1 {
						t.Fatalf("legacy latest=%d %v", latest, err)
					}
				}
			})
		}
	}
}

// The probe reacquires bridge and registry locks during delivery. Publication
// cannot retain either lock while invoking a peer.
type promptReceiptBroadcaster struct {
	conns      []relay.ActiveConn
	bridge     *streamApprovalBridge
	mu         sync.Mutex
	envs       []protocol.Envelope
	recipients []string
	dismissed  chan struct{}
}

func (p *promptReceiptBroadcaster) ActiveConns(context.Context) []relay.ActiveConn { return p.conns }
func (p *promptReceiptBroadcaster) Push(_ context.Context, id string, env protocol.Envelope) error {
	p.bridge.mu.Lock()
	p.bridge.mu.Unlock()
	p.bridge.perm.Lookup("probe")
	p.mu.Lock()
	defer p.mu.Unlock()
	p.envs = append(p.envs, env)
	p.recipients = append(p.recipients, id)
	if env.Type == protocol.TypeQuestionDismissed && p.dismissed != nil {
		p.dismissed <- struct{}{}
	}
	return nil
}

func TestPromptAnswerReceiptsPublication(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "cancel", "answer", "refusal"} {
		for _, connected := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/connected=%v", decision, connected), func(t *testing.T) {
				store := history.New(t.TempDir())
				b, m, q, bc := testPromptBridge(t, store, discardLogger())
				ring := eventring.New(32)
				b.historyPublisher = &promptAnswerPublisher{ring: ring}
				req := permbridge.Request{ToolUseID: "tool", ToolName: "Bash", Input: json.RawMessage(`{}`), SessionID: "asking-session"}
				if decision == "answer" || decision == "refusal" {
					req.ToolName = questionbridge.ToolName
					req.Input = questionInput(t, "Pick", "Header", "rewrite", "meaning")
				}
				pending, id, retire := testPromptSurface(t, b, bc, req, time.Hour)
				probe := &promptReceiptBroadcaster{bridge: b, dismissed: make(chan struct{}, 1)}
				if connected {
					probe.conns = []relay.ActiveConn{{ConnID: "phone", Interactive: true}, {ConnID: "noninteractive"}}
				}
				b.bcast = probe
				b.activeConv = func() string { return testConvIDB }
				b.sessionConv = func(string) (string, bool) { return testConvIDB, true }
				switch decision {
				case "allow", "deny":
					option := "allow_once"
					if decision == "deny" {
						option = "reject_once"
					}
					if _, ok := m.ResolveAnswer(id, option, "", eligibleDevice(t)); !ok {
						t.Fatal("resolution failed")
					}
				case "cancel":
					if _, ok := m.ResolveCancel(id, eligibleDevice(t)); !ok {
						t.Fatal("cancel failed")
					}
				case "answer":
					if !q.ResolveAnswer(protocol.QuestionAnswerPayload{QuestionBatchID: id, Answers: []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite"}}, {QuestionIndex: 1, Values: []string{"main"}}}}, eligibleDevice(t)) {
						t.Fatal("answer failed")
					}
				case "refusal":
					if !q.ResolveRefusal(protocol.QuestionRefusedPayload{QuestionBatchID: id}, eligibleDevice(t)) {
						t.Fatal("refusal failed")
					}
				}
				if connected && (decision == "answer" || decision == "refusal") {
					select {
					case <-probe.dismissed:
					case <-time.After(5 * time.Second):
						t.Fatal("question dismissal missing")
					}
				}
				verdict := pending.Await()
				wantBehavior := "deny"
				if decision == "allow" || decision == "answer" {
					wantBehavior = "allow"
				}
				if verdict.Behavior != wantBehavior {
					t.Fatal("child verdict changed")
				}
				retire()
				m.ResolveAnswer(id, "allow_once", "", eligibleDevice(t))
				q.ResolveRefusal(protocol.QuestionRefusedPayload{QuestionBatchID: id}, eligibleDevice(t))
				page := newHistoryPager(store, discardLogger())(testConvID, "", 10)
				events, gap := ring.After(testConvID, 0)
				if gap || len(events) != 1 || len(page.Entries) != 1 || len(historyEntries(t, store, testConvID)) != 1 {
					t.Fatal("not exactly one durable/replay fact")
				}
				e := events[0]
				h := page.Entries[0]
				testPromptReceipt(t, e.Payload)
				if e.Type != protocol.TypeBanner || e.HistoryEntryID != h.ID || !e.TS.Equal(h.TS) || !bytes.Equal(e.Payload, h.Payload) {
					t.Fatal("replay/history mismatch")
				}
				receipts := 0
				probe.mu.Lock()
				defer probe.mu.Unlock()
				for i, env := range probe.envs {
					if env.Type != protocol.TypeBanner {
						continue
					}
					receipts++
					if probe.recipients[i] != "phone" || env.HistoryEntryID == nil || *env.HistoryEntryID != h.ID || env.EventID == nil || *env.EventID != e.ID || !env.TS.Equal(h.TS) || !bytes.Equal(env.Payload, h.Payload) {
						t.Fatal("live/history/replay mismatch")
					}
				}
				expected := 0
				if connected {
					expected = 1
				}
				if receipts != expected {
					t.Fatalf("receipts=%d want=%d", receipts, expected)
				}
				if foreign, _ := ring.After(testConvIDB, 0); len(foreign) != 0 {
					t.Fatal("successor received replay")
				}
			})
		}
	}
}

func TestPromptAnswerReceiptsConcurrent(t *testing.T) {
	store := history.New(t.TempDir())
	b, m, _, bc := testPromptBridge(t, store, discardLogger())
	b.historyPublisher = &promptAnswerPublisher{ring: eventring.New(64)}
	const count = 24
	ids := make([]string, count)
	pending := make([]*permbridge.Pending, count)
	for i := range ids {
		p, id, _ := testPromptSurface(t, b, bc, permbridge.Request{ToolUseID: fmt.Sprint(i), ToolName: "Bash", Input: json.RawMessage(`{}`)}, time.Hour)
		ids[i], pending[i] = id, p
	}
	probe := &promptReceiptBroadcaster{bridge: b, conns: []relay.ActiveConn{{ConnID: "phone", Interactive: true}}}
	b.bcast = probe
	device := eligibleDevice(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, id := range ids {
		for range 2 {
			wg.Add(1)
			go func(id string) { defer wg.Done(); <-start; m.ResolveAnswer(id, "reject_once", "", device) }(id)
		}
	}
	close(start)
	wg.Wait()
	for _, p := range pending {
		if p.Await().Behavior != "deny" {
			t.Fatal("verdict changed")
		}
	}
	events, gap := b.historyPublisher.ring.After(testConvID, 0)
	if gap || len(events) != count || len(historyEntries(t, store, testConvID)) != count {
		t.Fatal("concurrent answer lost or duplicated")
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	receipts := []protocol.Envelope{}
	for _, env := range probe.envs {
		if env.Type == protocol.TypeBanner {
			receipts = append(receipts, env)
		}
	}
	if len(receipts) != count {
		t.Fatalf("receipts=%d", len(receipts))
	}
	for i, e := range events {
		env := receipts[i]
		if e.HistoryEntryID != uint64(i+1) || env.HistoryEntryID == nil || *env.HistoryEntryID != e.HistoryEntryID || env.EventID == nil || *env.EventID != e.ID || !env.TS.Equal(e.TS) || !bytes.Equal(env.Payload, e.Payload) {
			t.Fatal("concurrent publication order/identity changed")
		}
	}
}
