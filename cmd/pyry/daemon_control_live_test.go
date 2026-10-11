package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlLivePrompts(t *testing.T) {
	o := testLiveOwner()
	src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
	for _, id := range []string{"one", "two"} {
		if _, ok := o.admit(src, protocol.Envelope{Type: protocol.TypeModalShown, Payload: json.RawMessage(`{"modal_id":"` + id + `"}`)}, id); !ok {
			t.Fatal("prompt refused")
		}
	}
	o.admit(src, protocol.Envelope{Type: protocol.TypeModalDismissed, Payload: json.RawMessage(`{}`)}, "one")
	got := testLiveReadings(t, o, "conv")
	if _, ok := got[liveReadingKey{protocol.TypeModalShown, "one"}]; ok {
		t.Fatal("answered prompt remains")
	}
	if _, ok := got[liveReadingKey{protocol.TypeModalShown, "two"}]; !ok {
		t.Fatal("concurrent prompt lost")
	}
}

func TestControlLiveOperationOrdering(t *testing.T) {
	o := testLiveOwner()
	src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
	old := o.begin(src, protocol.TypeSessionSettings, "")
	next := o.begin(src, protocol.TypeSessionSettingsUpdated, "")
	correlation := uint64(41)
	env := protocol.Envelope{Type: protocol.TypeSessionSettingsUpdated, Payload: json.RawMessage(`{"model":"new"}`), InReplyTo: &correlation}
	if _, ok := next.complete(env); !ok {
		t.Fatal("new completion refused")
	}
	if _, ok := old.complete(protocol.Envelope{Type: protocol.TypeSessionSettings, Payload: json.RawMessage(`{"model":"old"}`)}); ok {
		t.Fatal("overtaken reply retained")
	}
	equal := o.begin(src, protocol.TypeSessionSettings, "")
	env.Type = protocol.TypeSessionSettings
	reply, ok := equal.complete(env)
	if !ok || *reply.Envelope.InReplyTo != 41 {
		t.Fatal("equal correlated request not answerable")
	}
	late := o.begin(src, protocol.TypeResetting, "")
	o.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "a", NextAgent: "claude"})
	if _, ok := late.complete(protocol.Envelope{Type: protocol.TypeResetting, Payload: json.RawMessage(`{}`)}); ok {
		t.Fatal("late reset restored")
	}
	got := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeSessionSettings, ""}]
	if !got.Envelope.SessionStateCleared {
		t.Fatal("settings not cleared")
	}
}

func testControlBindings(t *testing.T, conv, sid string) *daemonLiveBindings {
	t.Helper()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(conv), CurrentSessionID: sid})
	sink := newStreamTurnSink(8, nil)
	sink.live = newDaemonLiveState(func(id string) (string, bool) { return conversationForSession(reg, id) })
	return &daemonLiveBindings{sink: sink, reg: reg}
}

func TestControlLiveSources(t *testing.T) {
	for _, tt := range []struct {
		name, sid, want string
		none            bool
	}{{"unresolved", "", "", false}, {"no session", "", "null", true}, {"known", "a", `"a"`, false}} {
		t.Run(tt.name, func(t *testing.T) {
			b := testControlBindings(t, "conv", "a")
			src := b.capture("conv", tt.sid, tt.none)
			payload := json.RawMessage(`{"session_id":"payload-session"}`)
			n := uint64(123)
			reading, ok := b.sink.live.admit(src, protocol.Envelope{Type: protocol.TypeSessionSettings, Payload: payload, InReplyTo: &n}, "")
			if !ok || string(reading.Envelope.SessionID) != tt.want {
				t.Fatalf("source=%s admitted=%v", reading.Envelope.SessionID, ok)
			}
			wire, _ := json.Marshal(reading.Envelope)
			var fields map[string]json.RawMessage
			json.Unmarshal(wire, &fields)
			_, present := fields["session_id"]
			if present != (tt.want != "") {
				t.Fatalf("provenance presence: %s", wire)
			}
			cursor := b.sink.live.snapshot("conv")
			payload[0] = '!'
			*reading.Envelope.InReplyTo = 999
			reading.Envelope.Payload[0] = '!'
			pinned, ok := cursor.Next()
			if !ok || string(pinned.Envelope.Payload) != `{"session_id":"payload-session"}` || *pinned.Envelope.InReplyTo != 123 {
				t.Fatal("snapshot aliases caller")
			}
			b.reg.Create(conversations.Conversation{ID: "other", CurrentSessionID: "b"})
			b.offer(b.capture("other", "b", false), protocol.TypeSessionError, protocol.SessionErrorPayload{ConversationID: "other", Message: "other"}, "")
			if len(testLiveReadings(t, b.sink.live, "conv")) != 1 {
				t.Fatal("other conversation changed state")
			}
		})
	}
}

func TestControlLivePromptProducers(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(fmt.Sprint(question), func(t *testing.T) {
			b, _, _, bc := testPromptBridge(t, nil, discardLogger())
			b.live = testControlBindings(t, testConvID, "a")
			tool, input := "Bash", json.RawMessage(`{"command":"true"}`)
			family, dismiss := protocol.TypeModalShown, protocol.TypeModalDismissed
			if question {
				tool = "AskUserQuestion"
				input = json.RawMessage(`{"questions":[{"question":"Choose?","header":"Choice","options":[{"label":"A","description":"A"},{"label":"B","description":"B"}],"multiSelect":false}]}`)
				family, dismiss = protocol.TypeQuestionShown, protocol.TypeQuestionDismissed
			}
			ids := []string{}
			retire := []func(){}
			for i := range 2 {
				req := permbridge.Request{SessionID: "a", ToolName: tool, ToolUseID: fmt.Sprint(i), Input: input, RequiresUserInteraction: true}
				_, id, cleanup := testPromptSurface(t, b, bc, req, time.Minute)
				ids = append(ids, id)
				retire = append(retire, cleanup)
			}
			got := testLiveReadings(t, b.live.sink.live, testConvID)
			for _, id := range ids {
				if r, ok := got[liveReadingKey{family, id}]; !ok || string(r.Envelope.SessionID) != `"a"` || r.Answerable == nil || *r.Answerable != question {
					t.Fatal("producer prompt missing source or ID")
				}
			}
			if question {
				if !b.RefuseQuestion(ids[0]) {
					t.Fatal("refusal failed")
				}
			} else {
				if !b.ResolveStream(ids[0], false, false, "deny") {
					t.Fatal("answer failed")
				}
			}
			got = testLiveReadings(t, b.live.sink.live, testConvID)
			if _, ok := got[liveReadingKey{family, ids[0]}]; ok {
				t.Fatal("answer did not retire prompt")
			}
			if _, ok := got[liveReadingKey{family, ids[1]}]; !ok {
				t.Fatal("concurrent prompt retired")
			}
			retire[1]()
			if _, ok := testLiveReadings(t, b.live.sink.live, testConvID)[liveReadingKey{family, ids[1]}]; ok {
				t.Fatal("timeout did not retire")
			}
			old := b.live.capture(testConvID, "a", false)
			b.live.sink.live.transition(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", NewID: "a", NextAgent: "claude"})
			b.live.dismiss(old, dismiss, ids[0])
			r := testLiveReadings(t, b.live.sink.live, testConvID)[liveReadingKey{family, ""}]
			if !r.Envelope.SessionStateCleared {
				t.Fatal("old dismissal changed successor")
			}
		})
	}
}

func TestControlLiveDelayedProducers(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	ch := make(chan giveUpNotice, 1)
	notify := sessionErrorNotify(ch, discardLogger(), b)
	notify("conv", "first")
	notify("conv", "second") // retention survives a full delivery channel
	r := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeSessionError, ""}]
	if len(r.Envelope.SessionID) != 0 || !bytes.Contains(r.Envelope.Payload, []byte("second")) {
		t.Fatal("conversation-only notice invented a source or dropped retention")
	}
	e := newResettingEmitterV2(context.Background(), discardLogger())
	e.live = b
	operation := e.bound("conv", "a")
	operation.wrappingUp("conv")
	reset := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeResetting, ""}]
	if string(reset.Envelope.SessionID) != `"a"` {
		t.Fatal("reset not retained without broadcaster")
	}
	suggestions := newReplySuggestions(discardLogger())
	suggestions.live = b
	src := b.capture("conv", "a", false)
	suggestions.mu.Lock()
	c := suggestions.entry("conv", true)
	c.live = src
	suggestions.setLocked("conv", c, "text", "a")
	suggestions.mu.Unlock()
	suggestion := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeReplySuggestion, ""}]
	if !bytes.Contains(suggestion.Envelope.Payload, []byte("text")) {
		t.Fatal("retention waited for publisher")
	}
	b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "a", NextAgent: "claude"})
	operation.done("conv")
	newSessionErrorEmitterV2(ch, discardLogger()).broadcast(context.Background(), historyOnlyBroadcaster{}, <-ch)
	suggestions.mu.Lock()
	suggestions.setLocked("conv", c, "late fallback", "a")
	suggestions.mu.Unlock()
	for _, family := range []string{protocol.TypeResetting, protocol.TypeSessionError, protocol.TypeReplySuggestion} {
		if !testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}].Envelope.SessionStateCleared {
			t.Fatalf("old %s restored state", family)
		}
	}
}

func TestControlLiveContextFlights(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	first := newFakeContextUsageQuerier(true)
	first.block = make(chan struct{})
	second := newFakeContextUsageQuerier(true)
	second.usage.Model = "successor"
	var current contextUsageQuerier = first
	resolver := newContextUsageResolver(context.Background(), func(string) (contextUsageQuerier, conversations.ConversationID, bool) { return current, "conv", true }, nil, nil)
	resolver.live = b
	oldDone := make(chan daemonLiveReading, 1)
	go func() {
		r, ok := resolver.GetLive(context.Background(), "conv", protocol.Envelope{})
		if !ok {
			r = daemonLiveReading{}
		}
		oldDone <- r
	}()
	<-first.details
	b.sink.offerMu.Lock()
	b.reg.RebindSession("a", "b")
	b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "b", NextAgent: "claude"})
	current = second
	b.sink.offerMu.Unlock()
	n := uint64(81)
	reply, ok := resolver.GetLive(context.Background(), "conv", protocol.Envelope{ID: 9, InReplyTo: &n})
	if !ok || string(reply.Envelope.SessionID) != `"b"` || *reply.Envelope.InReplyTo != 81 || second.calls.Load() != 1 {
		t.Fatal("successor joined or retagged predecessor")
	}
	close(first.block)
	old := <-oldDone
	if string(old.Envelope.SessionID) != `"a"` {
		t.Fatal("old reply lost its source")
	}
	r := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeContextUsage, ""}]
	if !bytes.Contains(r.Envelope.Payload, []byte("successor")) {
		t.Fatal("old query replaced successor")
	}
	n = 82
	equal, ok := resolver.GetLive(context.Background(), "conv", protocol.Envelope{InReplyTo: &n})
	if !ok || *equal.Envelope.InReplyTo != 82 || second.calls.Load() != 1 {
		t.Fatal("equal-state collapse lost correlated answer")
	}
}

func TestControlLiveStoredAndConvergedReadings(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	src := b.capture("conv", "a", false)
	payload := protocol.SlashCommandListPayload{ConversationID: "conv"}
	b.sink.live.acceptEvent(src, turnevent.SlashCommandList{})
	n := uint64(17)
	_, ok, reading := liveInventoryReading(b, "conv", protocol.TypeSlashCommandList, protocol.Envelope{InReplyTo: &n}, func() (protocol.SlashCommandListPayload, bool) { return payload, true })
	if !ok || string(reading.Envelope.SessionID) != `"a"` || *reading.Envelope.InReplyTo != 17 {
		t.Fatal("cached source/correlation lost")
	}
	if len(testLiveReadings(t, b.sink.live, "conv")) != 1 {
		t.Fatal("request created a new identity")
	}
	models := protocol.ModelListPayload{ConversationID: "conv", Models: []protocol.ModelOption{{Value: "stored"}}}
	_, ok, stored := liveInventoryReading(b, "conv", protocol.TypeModelList, protocol.Envelope{}, func() (protocol.ModelListPayload, bool) { return models, true })
	if !ok || len(stored.Envelope.SessionID) != 0 {
		t.Fatal("stored menu assigned current session")
	}
	rec := &contextUsageRecorder{reg: b.reg, path: filepath.Join(t.TempDir(), "registry.json"), logger: discardLogger()}
	usage := turnevent.ContextUsage{Model: "memory", TotalTokens: 2, MaxTokens: 10, Percentage: 20}
	rec.record("conv", usage)
	resolver := newContextUsageResolver(context.Background(), func(string) (contextUsageQuerier, conversations.ConversationID, bool) { return nil, "conv", true }, nil, nil)
	resolver.rec = rec
	resolver.live = b
	remembered, ok := resolver.GetLive(context.Background(), "conv", protocol.Envelope{InReplyTo: &n})
	if !ok || len(remembered.Envelope.SessionID) != 0 {
		t.Fatal("stored context assigned a producing session")
	}
	rec.record("conv", usage, src)
	remembered, ok = resolver.GetLive(context.Background(), "conv", protocol.Envelope{})
	if !ok || string(remembered.Envelope.SessionID) != `"a"` {
		t.Fatal("captured context evidence lost")
	}
}

func TestControlLiveSettingsProvider(t *testing.T) {
	pool, _ := newModelListTestPool(t)
	sid := string(pool.BootstrapID())
	b := testControlBindings(t, "conv", sid)
	resolve := runSettingsFor(b.reg, pool, nil, b)
	bound, ok := resolve("conv")
	if !ok {
		t.Fatal("settings resolution failed")
	}
	n := uint64(10)
	reading, ok := runSettingsReading(bound, relay.RunConfig{SessionID: sid, Model: bound.model, Effort: bound.effort}, protocol.Envelope{InReplyTo: &n})
	if !ok || string(reading.Envelope.SessionID) != `"`+sid+`"` || *reading.Envelope.InReplyTo != 10 {
		t.Fatal("settings source result lost")
	}
	updater := liveSettingsUpdater{settingsUpdaterAdapter: settingsUpdaterAdapter{pool, nil}, live: b}
	empty := ""
	updated, err := updater.UpdateLive(sid, relay.SettingsUpdate{Model: &empty}, protocol.Envelope{InReplyTo: &n})
	if err != nil || updated.Envelope.Type != protocol.TypeSessionSettingsUpdated || *updated.Envelope.InReplyTo != 10 {
		t.Fatalf("update=%+v err=%v", updated, err)
	}

	before := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeSessionSettingsUpdated, ""}]
	if before.Revision != updated.Revision || before.Revision == 0 {
		t.Fatal("successful update not retained in settings family")
	}
	_, err = updater.UpdateLive("unknown", relay.SettingsUpdate{Model: &empty}, protocol.Envelope{})
	if err == nil {
		t.Fatal("unknown settings update succeeded")
	}
	after := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeSessionSettingsUpdated, ""}]
	if string(before.Envelope.Payload) != string(after.Envelope.Payload) {
		t.Fatal("failed update retained")
	}
}

func TestControlLiveQuestionEnvelopeBound(t *testing.T) {
	t.Skip("blocked on #3109: supported question input exceeds the full envelope byte budget")
	// Find the parser's supported boundary so a changed producer cap changes this proof.
	input := func(n int) json.RawMessage {
		return json.RawMessage(`{"questions":[{"question":"` + strings.Repeat("<", n) + `","header":"H","options":[{"label":"A","description":"A"},{"label":"B","description":"B"}],"multiSelect":false}]}`)
	}
	low, high := 0, protocol.MaxThreadEnvelopeBytes
	for low+1 < high {
		middle := (low + high) / 2
		if _, ok := questionbridge.Parse(questionbridge.ToolName, input(middle)); ok {
			low = middle
		} else {
			high = middle
		}
	}
	batch, ok := questionbridge.Parse(questionbridge.ToolName, input(low))
	if !ok {
		t.Fatal("supported input refused")
	}
	batch.ConversationID = strings.Repeat("<", 36)
	batch.QuestionBatchID = strings.Repeat("<", 36)
	o := testLiveOwner()
	src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: strings.Repeat("<", 256)}, true)
	raw, _ := json.Marshal(batch)
	n := uint64(math.MaxUint64)
	if _, ok := o.admit(src, protocol.Envelope{Type: protocol.TypeQuestionShown, Payload: raw, InReplyTo: &n, ID: n}, batch.QuestionBatchID); !ok {
		t.Fatalf("supported question envelope rejected: payload=%d input=%d", len(raw), len(input(low)))
	}
}

func TestControlLiveFallbackSource(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	s := newReplySuggestions(discardLogger())
	s.live = b
	entered, release := make(chan struct{}), make(chan struct{})
	s.fallback = func(ctx context.Context, user, assistant string) (string, error) {
		close(entered)
		select {
		case <-release:
			return "late", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	s.bindSessions(func(string) (string, bool) { return "a", true })
	source := b.capture("conv", "a", false)
	s.mu.Lock()
	c := s.entry("conv", true)
	c.live = source
	c.turnSID = "a"
	c.turnOK = true
	c.userOK = true
	c.windowDone = true
	c.userText = "user"
	c.assistantText = "assistant"
	s.startFallbackLocked("conv", c)
	s.mu.Unlock()
	<-entered
	b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "a", NextAgent: "claude"})
	close(release)
	s.workers.Wait()
	s.stopFallbacks()
	reading := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeReplySuggestion, ""}]
	if !reading.Envelope.SessionStateCleared {
		t.Fatal("fallback worker was retagged to reused successor ID")
	}
}

func TestControlLiveResetRevision(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	e := newResettingEmitterV2(context.Background(), discardLogger())
	e.live = b
	old := e.bound("conv", "a")
	old.wrappingUp("conv")
	next := e.bound("conv", "a")
	next.wrappingUp("conv")
	old.done("conv")
	reading := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeResetting, ""}]
	var payload protocol.ResettingPayload
	if json.Unmarshal(reading.Envelope.Payload, &payload) != nil || !payload.Active {
		t.Fatal("overtaken reset completion replaced active reset")
	}
	next.restarting("conv", true)
	next.done("conv")
	reading = testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeResetting, ""}]
	if json.Unmarshal(reading.Envelope.Payload, &payload) != nil || payload.Active {
		t.Fatal("current reset completion was lost")
	}
}

func TestControlLiveNoRecipientPrompt(t *testing.T) {
	bridge, _, _, _ := testPromptBridge(t, nil, discardLogger())
	bridge.bcast = historyOnlyBroadcaster{}
	bridge.live = testControlBindings(t, testConvID, "a")
	req := permbridge.Request{SessionID: "a", ToolName: "Bash", ToolUseID: "no-recipient", Input: json.RawMessage(`{"command":"true"}`), RequiresUserInteraction: false}
	pending, err := bridge.perm.Register(req.ToolUseID, req, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_ = pending
	retire := bridge.Surface(req)
	defer retire()
	defer bridge.perm.Resolve(req.ToolUseID, permbridge.Deny("cleanup"))
	records := testLiveReadings(t, bridge.live.sink.live, testConvID)
	if len(records) != 1 {
		t.Fatalf("retained prompts=%d", len(records))
	}
	for _, reading := range records {
		if reading.Answerable == nil || !*reading.Answerable || !bridge.RemoteAnswerable(reading.ReadingID) {
			t.Fatal("ordinary permission lost remote answerability")
		}
		reading.Envelope.Payload[0] = '!'
		*reading.Answerable = false
	}
	for _, reading := range testLiveReadings(t, bridge.live.sink.live, testConvID) {
		if !json.Valid(reading.Envelope.Payload) || !*reading.Answerable {
			t.Fatal("detached prompt alias")
		}
	}
}

func TestControlLiveStreamRequestOrdering(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	src := b.capture("conv", "a", false)
	request := b.sink.live.begin(src, protocol.TypeContextUsage, "")
	cap := b.sink.live.acceptEvent(src, turnevent.ContextUsage{Model: "stream", TotalTokens: 3, MaxTokens: 10, Percentage: 30})
	if cap == nil {
		t.Fatal("stream not captured")
	}
	if _, ok := request.result(protocol.TypeContextUsage, protocol.ContextUsagePayload{ConversationID: "conv", Model: "request"}); ok {
		t.Fatal("query overtook newer stream reading")
	}
	reply := b.sink.live.begin(src, protocol.TypeContextUsage, "")
	if _, ok := reply.result(protocol.TypeContextUsage, protocol.ContextUsagePayload{ConversationID: "conv", Model: "request-after-stream"}); !ok {
		t.Fatal("later request not retained")
	}
	b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "a", NextAgent: "claude"})
	successor := b.capture("conv", "a", false)
	cap = b.sink.live.acceptEvent(successor, turnevent.ContextUsage{Model: "new", TotalTokens: 4, MaxTokens: 10, Percentage: 40})
	clears := map[string]bool{}
	for _, update := range cap.updates {
		if update.Envelope.SessionStateCleared {
			clears[liveFamily(update.Envelope.Type)] = true
		} else if update.Envelope.Type == protocol.TypeContextUsage && !clears[protocol.TypeContextUsage] {
			t.Fatal("fresh reading preceded its clear")
		}
	}
	for _, family := range daemonLiveFamilies {
		if !clears[family] {
			t.Fatalf("missing transition clear for %s", family)
		}
	}
}
