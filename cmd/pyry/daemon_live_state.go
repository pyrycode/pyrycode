package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// daemonLiveReading is a detached source-bound update. Ordering is independent
// of connections, history IDs and the spelling of a producing session ID.
type daemonLiveReading struct {
	Envelope          protocol.Envelope
	ConversationID    string
	SessionGeneration uint64
	ReadingID         string
	Revision          uint64
	Answerable        *bool
}
type liveReadingKey struct{ family, id string }
type liveRevisionKey struct {
	generation uint64
	liveReadingKey
}
type daemonLiveSource struct {
	ConversationID    string
	SessionGeneration uint64
	provenance        history.SessionProvenance
	producer          streamProducerKey
}
type liveConversation struct {
	generation  uint64
	session     string
	incarnation uint64
	stopped     bool
	readings    map[liveReadingKey]*daemonLiveReading
	revisions   map[liveRevisionKey]uint64
	scopes      map[liveReadingKey]string
}

// The owner has no delivery, history or filesystem dependencies. Its mutex is
// never held while resolving a conversation, publishing or operating fan-in.
type daemonLiveState struct {
	mu             sync.Mutex
	resolve        func(string) (string, bool)
	conversations  map[string]*liveConversation
	sources        map[streamProducerKey]daemonLiveSource
	turns          map[daemonLiveSource]*liveSourceTurn
	nextGeneration uint64 // seeds recreated conversations without deletion tombstones
}

var streamLiveFamilies = []string{
	protocol.TypeTurnState, protocol.TypeStall, protocol.TypeApiRetry,
	protocol.TypeCompacting, protocol.TypeThinkingProgress, protocol.TypeToolProgress,
	protocol.TypeBackgroundTaskProgress, protocol.TypeRateLimited, protocol.TypeContextUsage,
	protocol.TypeModelAnnounced, protocol.TypeSessionFacts, protocol.TypeMCPStatus,
	protocol.TypeSlashCommandList, protocol.TypeModelList,
}

func newDaemonLiveState(resolve func(string) (string, bool)) *daemonLiveState {
	return &daemonLiveState{resolve: resolve, conversations: make(map[string]*liveConversation), sources: make(map[streamProducerKey]daemonLiveSource), turns: make(map[daemonLiveSource]*liveSourceTurn)}
}
func (o *daemonLiveState) capture(sid string, incarnation uint64, p history.SessionProvenance, activation bool) daemonLiveSource {
	if o == nil || o.resolve == nil {
		return daemonLiveSource{}
	}
	key := streamProducerKey{sid, incarnation}
	o.mu.Lock()
	if bound, ok := o.sources[key]; ok {
		if p.Kind != "" {
			bound.provenance = p
			o.sources[key] = bound
		}
		o.mu.Unlock()
		return bound
	}
	o.mu.Unlock()
	conv, ok := o.resolve(sid)
	if !ok || conv == "" {
		return daemonLiveSource{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if bound, ok := o.sources[key]; ok {
		bound.provenance = p
		return bound
	}
	c := o.conversations[conv]
	if c == nil {
		c = o.newConversationLocked(conv, sid)
	} else if incarnation < c.incarnation {
		return daemonLiveSource{}
	} else if (activation && (c.session != sid || c.incarnation != incarnation || c.stopped)) || (c.stopped && p.Kind != "") {
		o.advanceLocked(conv, c, sid, p)
	}
	src := daemonLiveSource{ConversationID: conv, SessionGeneration: c.generation, provenance: p, producer: key}
	if c.session != sid {
		return daemonLiveSource{}
	}
	c.incarnation = max(c.incarnation, incarnation)
	if !c.stopped {
		o.sources[key] = src
	}
	return src
}
func (o *daemonLiveState) newConversationLocked(conv, sid string) *liveConversation {
	o.nextGeneration++
	c := &liveConversation{generation: o.nextGeneration, session: sid, readings: make(map[liveReadingKey]*daemonLiveReading), revisions: make(map[liveRevisionKey]uint64), scopes: make(map[liveReadingKey]string)}
	o.conversations[conv] = c
	return c
}
func (o *daemonLiveState) transition(t sessions.SessionTransition) *daemonLiveCursor {
	if o == nil || t.ConversationID == "" {
		return &daemonLiveCursor{}
	}
	p := history.SessionProvenance{Kind: t.NextAgent, SessionID: string(t.NewID)}
	if t.NewID == "" {
		p = history.SessionProvenance{Kind: "none"}
	} else if p.Kind != "claude" && p.Kind != "codex" {
		p = history.SessionProvenance{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.conversations[t.ConversationID]
	if c == nil {
		// Resolve outside the owner lock so a late deletion notification cannot
		// recreate an already removed conversation.
		o.mu.Unlock()
		sid := string(t.PreviousID)
		if sid == "" {
			sid = string(t.NewID)
		}
		conv, ok := "", false
		if o.resolve != nil {
			conv, ok = o.resolve(sid)
		}
		o.mu.Lock()
		if !ok || conv != t.ConversationID {
			return &daemonLiveCursor{}
		}
		c = o.conversations[t.ConversationID]
		if c == nil {
			c = o.newConversationLocked(t.ConversationID, string(t.PreviousID))
		}
	}
	o.advanceLocked(t.ConversationID, c, string(t.NewID), p)
	return o.snapshotLocked(t.ConversationID)
}

func (o *daemonLiveState) advanceLocked(conv string, c *liveConversation, sid string, p history.SessionProvenance) {
	c.generation++
	o.nextGeneration = max(o.nextGeneration, c.generation)
	c.session = sid
	c.stopped = false
	o.releaseSourcesLocked(conv)
	c.readings = make(map[liveReadingKey]*daemonLiveReading)
	c.revisions = make(map[liveRevisionKey]uint64)
	c.scopes = make(map[liveReadingKey]string)
	src := daemonLiveSource{ConversationID: conv, SessionGeneration: c.generation, provenance: p}
	for _, family := range daemonLiveFamilies {
		o.admitLocked(src, protocol.Envelope{Type: family, Payload: json.RawMessage(`{}`), SessionStateCleared: true}, "", "")
	}
}
func liveSessionTag(p history.SessionProvenance) json.RawMessage {
	if p.Kind == "none" {
		return json.RawMessage(`null`)
	}
	if p.SessionID == "" {
		return nil
	}
	b, _ := json.Marshal(p.SessionID)
	return b
}
func detachLiveEnvelope(e protocol.Envelope) protocol.Envelope {
	e.Payload = bytes.Clone(e.Payload)
	e.SessionID = bytes.Clone(e.SessionID)
	if e.InReplyTo != nil {
		v := *e.InReplyTo
		e.InReplyTo = &v
	}
	if e.EventID != nil {
		v := *e.EventID
		e.EventID = &v
	}
	if e.HistoryEntryID != nil {
		v := *e.HistoryEntryID
		e.HistoryEntryID = &v
	}
	return e
}
func detachLiveReading(r daemonLiveReading) daemonLiveReading {
	r.Envelope = detachLiveEnvelope(r.Envelope)
	if r.Answerable != nil {
		v := *r.Answerable
		r.Answerable = &v
	}
	return r
}
func validDaemonLiveEnvelope(e protocol.Envelope) bool {
	if len(e.Payload) > protocol.MaxThreadEnvelopeBytes || len(e.SessionID) > protocol.MaxThreadEnvelopeBytes || !slices.Contains(daemonLiveFamilies, liveFamily(e.Type)) || !json.Valid(e.Payload) {
		return false
	}
	if len(e.SessionID) > 0 && !bytes.Equal(e.SessionID, []byte("null")) {
		var sid string
		if json.Unmarshal(e.SessionID, &sid) != nil || sid == "" {
			return false
		}
	}
	if e.SessionStateCleared {
		var p map[string]json.RawMessage
		if json.Unmarshal(e.Payload, &p) != nil || p == nil || len(p) != 0 {
			return false
		}
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > protocol.MaxThreadEnvelopeBytes {
		return false
	}
	e.Payload = json.RawMessage(`{}`)
	e.SessionStateCleared = true
	raw, err = json.Marshal(e)
	return err == nil && len(raw) <= protocol.MaxThreadEnvelopeBytes
}

// admit accepts an envelope at its producing boundary, preserving correlation
// fields. The returned update and retained copy share no mutable envelope data.
func (o *daemonLiveState) admit(src daemonLiveSource, e protocol.Envelope, id string) (daemonLiveReading, bool) {
	if o == nil {
		return daemonLiveReading{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.admitLocked(src, e, id, "")
}
func (o *daemonLiveState) admitLocked(src daemonLiveSource, e protocol.Envelope, id, scope string) (daemonLiveReading, bool) {
	c := o.conversations[src.ConversationID]
	if c == nil || src.SessionGeneration == 0 || src.SessionGeneration != c.generation || (c.stopped && !liveControlFamily(e.Type)) {
		return daemonLiveReading{}, false
	}
	e.SessionID = liveSessionTag(src.provenance)
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if !validDaemonLiveEnvelope(e) {
		return daemonLiveReading{}, false
	}
	key := liveReadingKey{liveFamily(e.Type), id}
	rk := liveRevisionKey{src.SessionGeneration, key}
	c.revisions[rk]++
	r := daemonLiveReading{Envelope: detachLiveEnvelope(e), ConversationID: src.ConversationID, SessionGeneration: src.SessionGeneration, ReadingID: id, Revision: c.revisions[rk]}
	if src.SessionGeneration == c.generation {
		if liveDismissal(e.Type) {
			delete(c.readings, key)
			delete(c.scopes, key)
		} else {
			c.readings[key] = &r
			c.scopes[key] = scope
		}
	}
	return detachLiveReading(r), true
}

// releaseSourcesLocked drops owner bookkeeping; queued captures and cursors own
// their values independently. The conversation's incarnation rejects old runners.
func (o *daemonLiveState) releaseSourcesLocked(conv string) {
	for key, src := range o.sources {
		if src.ConversationID == conv {
			delete(o.sources, key)
		}
	}
	for src := range o.turns {
		if src.ConversationID == conv {
			delete(o.turns, src)
		}
	}
}

type conversationRemovalOwner interface {
	remove(conversations.ConversationID)
}

func (s *streamTurnSink) remove(id conversations.ConversationID) {
	if s == nil || s.live == nil {
		return
	}
	// Same lock order as capture/transition, including registry resolution.
	s.offerMu.Lock()
	defer s.offerMu.Unlock()
	o := s.live
	o.mu.Lock()
	defer o.mu.Unlock()
	o.releaseSourcesLocked(string(id))
	delete(o.conversations, string(id))
}

// retain is the delayed-admission seam: an older generation or an overtaken
// revision cannot replace the current record, including after retirement.
func (o *daemonLiveState) retain(r daemonLiveReading) bool {
	if o == nil || r.Revision == 0 || !validDaemonLiveEnvelope(r.Envelope) {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.conversations[r.ConversationID]
	if c == nil || r.SessionGeneration != c.generation || c.stopped {
		return false
	}
	key := liveReadingKey{liveFamily(r.Envelope.Type), r.ReadingID}
	rk := liveRevisionKey{r.SessionGeneration, key}
	if r.Revision <= c.revisions[rk] {
		return false
	}
	r = detachLiveReading(r)
	c.revisions[rk] = r.Revision
	if liveDismissal(r.Envelope.Type) {
		delete(c.readings, key)
	} else {
		c.readings[key] = &r
	}
	return true
}
func (o *daemonLiveState) retireLocked(src daemonLiveSource, key liveReadingKey) {
	c := o.conversations[src.ConversationID]
	if c == nil || c.generation != src.SessionGeneration {
		return
	}
	if _, ok := c.readings[key]; ok {
		delete(c.readings, key)
		delete(c.scopes, key)
		c.revisions[liveRevisionKey{c.generation, key}]++
	}
}

type daemonLiveCursor struct {
	records []*daemonLiveReading
	next    int
}

// snapshot pins immutable record references, not a batch of copied payloads.
// Updates replace records, so the finite capture never follows concurrent work.
func (o *daemonLiveState) snapshot(conv string) *daemonLiveCursor {
	if o == nil {
		return &daemonLiveCursor{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.snapshotLocked(conv)
}
func (o *daemonLiveState) snapshotLocked(conv string) *daemonLiveCursor {
	cursor := &daemonLiveCursor{}
	if c := o.conversations[conv]; c != nil {
		for _, r := range c.readings {
			cursor.records = append(cursor.records, r)
		}
	}
	slices.SortFunc(cursor.records, func(a, b *daemonLiveReading) int {
		if a.Envelope.Type != b.Envelope.Type {
			if a.Envelope.Type < b.Envelope.Type {
				return -1
			}
			return 1
		}
		if a.ReadingID < b.ReadingID {
			return -1
		}
		if a.ReadingID > b.ReadingID {
			return 1
		}
		return 0
	})
	return cursor
}

func (c *daemonLiveCursor) Next() (daemonLiveReading, bool) {
	if c == nil || c.next == len(c.records) {
		return daemonLiveReading{}, false
	}
	r := detachLiveReading(*c.records[c.next])
	c.next++
	return r, true
}
