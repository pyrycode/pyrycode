package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestDaemonThreadOffscreenShownRecovery(t *testing.T) {
	s, h, reg, _ := testShadow(t)
	path := filepath.Join(t.TempDir(), "conversations.json")
	create := conversationCreator(reg, conversationMinterFunc(func(_ context.Context, id, cwd, _ string, _ handlers.CreateSettings) (string, string, error) {
		return "session-" + id, cwd, nil
	}), path, nil, discardLogger())
	ids := make([]string, 2)
	for i, name := range []string{"A", "B"} {
		var err error
		ids[i], err = create(t.TempDir(), name, "chat", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	a, b := conversations.ConversationID(ids[0]), conversations.ConversationID(ids[1])
	pinned := time.Now().UTC()
	label := "Workspace A"
	reg.Update(a, func(c *conversations.Conversation) { c.IsMuted, c.PinnedAt = true, &pinned })
	storedA, _ := reg.Get(a)
	reg.SetWorkspaceLabel(storedA.Cwd, &label)

	// A hidden producer may append after the follower's quiet-boundary capture.
	// Hold that tail chunk through cancellation to expose real Store retirement.
	var holdTail atomic.Bool
	tailEntered, retiring, releaseTail := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var tailOnce sync.Once
	unblockTail := func() { tailOnce.Do(func() { close(releaseTail) }) }
	defer unblockTail()
	store := thread.NewStore(h, func(ctx context.Context, id conversations.ConversationID) error {
		if id != a || !holdTail.CompareAndSwap(true, false) {
			return nil
		}
		close(tailEntered)
		<-ctx.Done()
		close(retiring)
		<-releaseTail
		return ctx.Err()
	})
	s.store = store
	quietEntered, releaseQuiet := make(chan struct{}), make(chan struct{})
	var idle atomic.Bool
	var quietOnce, releaseQuietOnce sync.Once
	unblockQuiet := func() { releaseQuietOnce.Do(func() { close(releaseQuiet) }) }
	defer unblockQuiet()
	s.quiet = func(id conversations.ConversationID) bool {
		if id != a || !idle.Load() {
			return false
		}
		quietOnce.Do(func() { close(quietEntered); <-releaseQuiet })
		return true
	}

	delivered := make(chan struct{}, 1)
	q, err := msgqueue.New(msgqueue.Config{
		Deliver:     func(context.Context, string, []byte) error { return nil },
		OnDelivered: operatorMessageHistory(h, func(operatorMessage) { delivered <- struct{}{} }, nil, discardLogger(), nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	queueCtx, cancelQueue := context.WithCancel(t.Context())
	queueDone := make(chan struct{})
	go func() { defer close(queueDone); _ = q.Run(queueCtx) }()
	defer func() { cancelQueue(); <-queueDone }()
	var cfg relay.V2SessionConfig
	w, mgr := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
		c.Handlers = map[string]dispatch.Handler{
			protocol.TypeListConversations: handlers.ListConversationsWithAgents(reg, nil, legacyHistoryReader{h}),
			protocol.TypeSendMessage:       handlers.SendMessage(testCompactSend{}, q, nil, reg, path, nil, nil, discardLogger()),
		}
		cfg = *c
	})
	w.open("phone", true, protocol.CapabilityStopBackgroundTask, protocol.CapabilityThread)
	w.open("legacy", true, protocol.CapabilityStopBackgroundTask)
	conns := mgr.ActiveConns(t.Context())
	for _, c := range conns {
		if !c.Interactive || (c.ConnID == "phone") != c.Thread {
			t.Fatal("negotiated ownership mismatch")
		}
	}
	if len(conns) != 2 {
		t.Fatal("missing authenticated peers")
	}
	// Providers and publisher retain the actual Store. Only retirement return is
	// held, after real joining, to inspect the intermediate certificate boundary.
	joined := &testShadowRetirement{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	var joinOnce sync.Once
	unblockJoin := func() { joinOnce.Do(func() { close(joined.release) }) }
	defer unblockJoin()
	s.store = joined
	s.start()

	read := func(peer, typ string, correlation uint64) protocol.Envelope {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			routing, err := testDaemonThreadFrame(w, deadline)
			if err != nil {
				t.Fatalf("missing %s for %s: %v", typ, peer, err)
			}
			gotPeer, e := w.decode(routing)
			if gotPeer == "legacy" && e.Type == protocol.TypeConversationUpdated {
				t.Fatal("thread retirement changed non-thread publication")
			}
			if gotPeer == peer && e.Type == typ && (correlation == 0 || e.InReplyTo != nil && *e.InReplyTo == correlation) {
				return e
			}
		}
	}
	list := func(peer string, request uint64) map[string]protocol.ConversationSummary {
		t.Helper()
		testDaemonThreadRequest(t, w, peer, request, protocol.TypeListConversations, struct{}{})
		e := read(peer, protocol.TypeConversations, request)
		var p protocol.ConversationsPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		rows := map[string]protocol.ConversationSummary{}
		for _, row := range p.Conversations {
			if row.Agent != protocol.AgentClaude {
				t.Fatal("list lost negotiated multi-agent ownership")
			}
			rows[row.ID] = row
		}
		if len(rows) != 2 || rows[string(b)].ReadUpTo != 0 || rows[string(b)].LastShownVersion != nil && *rows[string(b)].LastShownVersion > 0 {
			t.Fatal("untouched B became unread or list lost a row")
		}
		return rows
	}
	list("phone", 10)
	testDaemonThreadRequest(t, w, "legacy", 11, protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: string(a), MessageID: "peer-ping", Text: "ping"})
	read("legacy", protocol.TypeAck, 11)
	awaitSwitchPublication(t, delivered)
	e := newInteractiveTurnEmitterV2(nil, mgr, discardLogger())
	e.hist = h
	source := history.SessionProvenance{Kind: "claude", SessionID: "session-" + string(a)}
	e.HandleFor(t.Context(), string(a), turnevent.TextChunk{MessageID: "reply", Text: "pong"}, source)
	e.HandleFor(t.Context(), string(a), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, source)
	read("legacy", protocol.TypeTurnEnd, 0)
	raw, err := h.LatestEntryID(a)
	if err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(a).Version == raw })
	shown, present := cfg.ThreadLastShownVersion(string(a))
	if !present || shown == 0 || shown >= raw {
		t.Fatal("completion did not preserve a separate shown boundary")
	}
	testDaemonThreadRequest(t, w, "phone", 12, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: string(a), Limit: 50})
	sawUser, sawAssistant := false, false
	for {
		reply := read("phone", protocol.TypeThreadItems, 12)
		var part protocol.ThreadItemsPayload
		if err := json.Unmarshal(reply.Payload, &part); err != nil {
			t.Fatal(err)
		}
		if part.ConversationID != string(a) || part.Version != raw {
			t.Fatal("catch-up captured the wrong boundary")
		}
		for _, item := range part.Items {
			sawUser = sawUser || item.Kind == "user_message" && item.Summary == "ping"
			sawAssistant = sawAssistant || item.Kind == "assistant_message" && item.Shown && item.Status == "done" && item.Summary == "pong" && !item.Active && item.Rev > shown
		}
		if part.Final {
			if len(part.Items) != 0 || !sawUser || !sawAssistant {
				t.Fatal("missing delivered user, done assistant or separate completion marker")
			}
			break
		}
	}
	idle.Store(true)
	awaitSwitchPublication(t, quietEntered)
	holdTail.Store(true)
	e.emit(t.Context(), string(a), protocol.TypeContextUsage, protocol.ContextUsagePayload{ConversationID: string(a)})
	awaitSwitchPublication(t, tailEntered)
	unblockQuiet()
	awaitSwitchPublication(t, retiring)
	s.quietMu.Lock()
	demand, retirement := s.queryDemands[a] != nil, s.queryRetiring[a] != nil
	s.quietMu.Unlock()
	_, registered := reg.Get(a)
	high, known, progressErr := h.CachedLatestEntryID(a)
	_, eligible := store.UnloadedLastShownVersion(a)
	t.Logf("retiring: registered=%t state=%d demand=%t retiring=%t raw_known=%t raw=%d certificate_eligible=%t", registered, store.Status(a).State, demand, retirement, known, high, eligible)
	if !registered || store.Status(a).State != thread.StateNotLoaded || demand || !retirement || !known || progressErr != nil || eligible {
		t.Fatal("did not establish genuine retiring-worker rejection")
	}
	if list("phone", 13)[string(a)].LastShownVersion != nil {
		t.Fatal("retiring list invented a shown watermark")
	}
	unblockTail()
	awaitSwitchPublication(t, joined.entered)
	dir, err := h.LogDir(a)
	if err != nil {
		t.Fatal("joined history directory unavailable")
	}
	certificate, err := os.ReadFile(filepath.Join(dir, "thread-certificate.json"))
	var evidence struct {
		Version, LastShownVersion uint64
		Clean                     bool
	}
	if err != nil || json.Unmarshal(certificate, &evidence) != nil || !evidence.Clean || evidence.Version != raw || evidence.LastShownVersion != shown {
		t.Fatal("joined retirement did not persist its clean consumed boundary")
	}
	_, eligible = store.UnloadedLastShownVersion(a)
	t.Logf("joined: state=%d consumed=%d raw=%d certificate_clean=%t certificate_version=%d certificate_eligible=%t", store.Status(a).State, s.consumed(a), high, evidence.Clean, evidence.Version, eligible)
	if eligible || s.consumed(a) >= high {
		t.Fatal("retirement certificate ignored a racing hidden append")
	}
	unblockJoin()
	// Recovery must be producer-driven: no opening, catch-up or read follows the
	// temporarily absent list. A stale retirement may first refresh with absence.
	for {
		update := read("phone", protocol.TypeConversationUpdated, 0)
		var p struct {
			protocol.ConversationUpdatedPayload
			LastShownVersion *uint64 `json:"last_shown_version"`
		}
		if err := json.Unmarshal(update.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.ID != string(a) || update.InReplyTo != nil {
			t.Fatal("refresh changed conversation ownership or correlation")
		}
		if p.LastShownVersion == nil {
			continue
		}
		stored, _ := reg.Get(a)
		if *p.LastShownVersion != shown || *p.LastShownVersion <= p.ReadUpTo || p.ReadUpTo != 0 || p.Cwd != stored.Cwd || p.Name == nil || *p.Name != *stored.Name || !p.LastUsedAt.Equal(stored.LastUsedAt) || p.IsArchived != stored.IsArchived || p.IsMuted != stored.IsMuted || p.IsPromoted != stored.IsPromoted || p.PinnedAt == nil || !p.PinnedAt.Equal(pinned) || p.WorkspaceLabel == nil || *p.WorkspaceLabel != label {
			t.Fatal("refresh lost authoritative watermark or stored summary fields")
		}
		break
	}
	recovered, eligible := store.UnloadedLastShownVersion(a)
	t.Logf("recovered: state=%d consumed=%d raw=%d certificate_eligible=%t shown=%d", store.Status(a).State, s.consumed(a), high, eligible, recovered)
	if !eligible || recovered != shown || store.Status(a).State != thread.StateNotLoaded || s.consumed(a) != high {
		t.Fatal("refresh did not follow authoritative joined recovery")
	}
	rows := list("phone", 14)
	if rows[string(a)].LastShownVersion == nil || *rows[string(a)].LastShownVersion != shown {
		t.Fatal("restored list lost the shown watermark")
	}
	for _, row := range list("legacy", 15) {
		if row.LastShownVersion != nil {
			t.Fatal("non-thread list gained thread metadata")
		}
	}
	for _, id := range []conversations.ConversationID{a, b} {
		row, _ := reg.Get(id)
		if row.ReadUpTo != 0 {
			t.Fatal("producer refresh changed stored read marks")
		}
	}
}
