package main

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonSummaryTaskProducer(t *testing.T) {
	s, store, _ := testDaemonExpansionFixture(t, 0, 0, 1)
	testDaemonThreadAppend(t, s.history, "background_task_started", `{"task_id":"root task 🐈","tool_call_id":"root"}`)
	v := testDaemonThreadAppend(t, s.history, "background_task_started", `{"task_id":"nested-task","tool_call_id":"nested"}`)
	testShadowWait(t, func() bool { return store.Status(testConvID).Version == v })
	w, mgr, _ := testCompactBurstWire(t, s)
	w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact)
	window, _, _ := testCompactQuery(t, w, mgr, "phone", 1, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 256, Projection: protocol.ThreadProjectionCompact})
	checkRoot := func(p protocol.ThreadItemsPayload) protocol.ThreadSummary {
		t.Helper()
		for _, h := range p.Headers {
			if h.ID == 1 {
				if h.TaskID == nil || *h.TaskID != "root task 🐈" || h.Parent != 0 || h.Order != 1 || h.Rev == 1 {
					t.Fatal("producer root association lost", h)
				}
				return h
			}
		}
		t.Fatal("root absent")
		return protocol.ThreadSummary{}
	}
	root := checkRoot(window)
	page, _, _ := testCompactQuery(t, w, mgr, "phone", 2, protocol.TypeThreadPage, protocol.ThreadPagePayload{ConversationID: testConvID, Epoch: window.Epoch, BeforeOrder: *window.UpperOrder, Limit: 256, Projection: protocol.ThreadProjectionCompact})
	checkRoot(page)
	r := protocol.ThreadExpansionRequest{ConversationID: testConvID, Epoch: window.Epoch, ItemID: root.ID, ExpectedRev: root.Rev, Limit: 10}
	testDaemonThreadRequest(t, w, "phone", 3, protocol.TypeThreadReadChildren, r)
	env, _, _ := testDaemonExpansionReply(t, w, "phone", 3, protocol.TypeThreadChildren)
	var children protocol.ThreadChildrenPayload
	if err := json.Unmarshal(env.Payload, &children); err != nil {
		t.Fatal(err)
	}
	if len(children.Children) != 1 || children.Children[0].TaskID == nil || *children.Children[0].TaskID != "nested-task" || children.Children[0].Parent != root.ID {
		t.Fatal("producer child association lost", children)
	}
}

func TestDaemonSummaryTaskLive(t *testing.T) {
	for _, value := range []string{"null", `""`, `"exact task 🐈"`} {
		t.Run(value, func(t *testing.T) {
			h := thread.ChildSummary{ID: 2, Order: 2, Rev: 3, Kind: "agent", DetailOmitted: true}
			if err := json.Unmarshal([]byte(`{"TaskID":`+value+`}`), &h); err != nil {
				t.Fatal(err)
			}
			updates := daemonThreadCompactUpdates(testConvID, 2, thread.CompactQueryResult{Epoch: "epoch", Version: 3, Headers: []thread.ChildSummary{h}})
			if len(updates) != 1 {
				t.Fatal(updates)
			}
			raw, err := json.Marshal(updates[0])
			if err != nil {
				t.Fatal(err)
			}
			var update struct {
				Header map[string]json.RawMessage `json:"header"`
			}
			if err := json.Unmarshal(raw, &update); err != nil {
				t.Fatal(err)
			}
			if value == "null" {
				if _, ok := update.Header["task_id"]; ok {
					t.Fatal("unknown association emitted", string(raw))
				}
			} else if string(update.Header["task_id"]) != value {
				t.Fatal("live association lost", string(raw))
			}
			if _, ok := update.Header["content"]; ok {
				t.Fatal("closed content leaked")
			}
		})
	}
}
