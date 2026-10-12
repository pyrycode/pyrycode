package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
)

func TestPromptAnswerRelayRemainsResponsive(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "cancel", "answer", "refusal"} {
		t.Run(decision, func(t *testing.T) {
			b, m, q, bc := testPromptBridge(t, history.New(t.TempDir()), discardLogger())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b.ctx = ctx
			b.historyPublisher = &promptAnswerPublisher{ring: eventring.New(32)}
			req := permbridge.Request{ToolUseID: "tool", ToolName: "Bash", Input: json.RawMessage(`{}`), SessionID: "asking-session"}
			if decision == "answer" || decision == "refusal" {
				req.ToolName = questionbridge.ToolName
				req.Input = questionInput(t, "Pick", "Header", "rewrite", "meaning")
			}
			pending, id, _ := testPromptSurface(t, b, bc, req, time.Minute)
			bindings := testControlBindings(t, testConvID, "asking-session")
			w, mgr := testDaemonRelayWire(t, bindings, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) { cfg.ModalResolver = m; cfg.QuestionResolver = q }, func(_ context.Context, mgr *relay.V2SessionManager) func() { b.bcast = mgr; return func() { cancel() } })
			w.open("phone", false)
			typ := protocol.TypeModalAnswer
			var payload any = protocol.ModalAnswerPayload{ModalID: id, OptionID: "allow_once"}
			switch decision {
			case "deny":
				payload = protocol.ModalAnswerPayload{ModalID: id, OptionID: "reject_once"}
			case "cancel":
				typ = protocol.TypeModalCancel
				payload = protocol.ModalCancelPayload{ModalID: id}
			case "answer":
				typ = protocol.TypeQuestionAnswer
				payload = protocol.QuestionAnswerPayload{QuestionBatchID: id, Answers: []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite"}}, {QuestionIndex: 1, Values: []string{"main"}}}}
			case "refusal":
				typ = protocol.TypeQuestionRefused
				payload = protocol.QuestionRefusedPayload{QuestionBatchID: id}
			}
			testDaemonWireRequest(t, w, 10, typ, payload)
			verdict := make(chan string, 1)
			go func() { verdict <- pending.Await().Behavior }()
			select {
			case <-verdict:
			case <-time.After(time.Second):
				t.Fatal("answer did not reach child")
			}
			probe, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			if got := mgr.ActiveConns(probe); len(got) != 1 {
				t.Fatal("answer blocked relay dispatch")
			}
			seen := map[string]bool{}
			for !seen[protocol.TypeBanner] || !(seen[protocol.TypeModalDismissed] || seen[protocol.TypeQuestionDismissed]) {
				_, e := w.read()
				seen[e.Type] = true
			}
			w.open("again", false)
		})
	}
}

// Delivery is held at the real manager query boundary while later answers arrive.
// No lock needed by answer recording may remain held across that query.
type heldPromptConnections struct {
	*promptReceiptBroadcaster
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *heldPromptConnections) ActiveConns(ctx context.Context) []relay.ActiveConn {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return p.conns
	case <-ctx.Done():
		return nil
	}
}

func TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint("cancelled=", cancelled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b, m, _, bc := testPromptBridge(t, history.New(t.TempDir()), discardLogger())
			b.ctx = ctx
			b.historyPublisher = &promptAnswerPublisher{ring: eventring.New(32)}
			ids := make([]string, 2)
			for i := range ids {
				_, ids[i], _ = testPromptSurface(t, b, bc, permbridge.Request{ToolUseID: fmt.Sprint(i), ToolName: "Bash", Input: json.RawMessage(`{}`)}, time.Minute)
			}
			probe := &heldPromptConnections{promptReceiptBroadcaster: &promptReceiptBroadcaster{bridge: b, conns: []relay.ActiveConn{{ConnID: "phone", Interactive: true}}}, entered: make(chan struct{}), release: make(chan struct{})}
			b.bcast = probe
			device := eligibleDevice(t)
			resolved := make(chan bool, 1)
			go func() { _, ok := m.ResolveAnswer(ids[0], "allow_once", "", device); resolved <- ok }()
			select {
			case <-probe.entered:
			case <-time.After(time.Second):
				t.Fatal("delivery never started")
			}
			select {
			case ok := <-resolved:
				if !ok {
					t.Fatal("first answer rejected")
				}
			case <-time.After(time.Second):
				t.Fatal("delivery blocked first answer")
			}
			go func() { _, ok := m.ResolveAnswer(ids[1], "reject_once", "", device); resolved <- ok }()
			select {
			case ok := <-resolved:
				if !ok {
					t.Fatal("second answer rejected")
				}
			case <-time.After(time.Second):
				t.Fatal("delivery blocked next answer")
			}
			events, _ := b.historyPublisher.ring.After(testConvID, 0)
			if len(events) != 2 || len(historyEntries(t, b.hist, testConvID)) != 2 {
				t.Fatal("answers not retained before delivery")
			}
			if cancelled {
				cancel()
			} else {
				close(probe.release)
			}
			waitPromptReceipts(t, b.historyPublisher)
			probe.mu.Lock()
			defer probe.mu.Unlock()
			if cancelled {
				if len(probe.envs) != 0 {
					t.Fatal("cancelled delivery sent receipt")
				}
				return
			}
			if len(probe.envs) != 2 {
				t.Fatal("receipts missing")
			}
			for i, e := range probe.envs {
				if e.HistoryEntryID == nil || *e.HistoryEntryID != uint64(i+1) || e.EventID == nil || *e.EventID != events[i].ID {
					t.Fatal("receipt order or identity changed")
				}
			}
		})
	}
}
