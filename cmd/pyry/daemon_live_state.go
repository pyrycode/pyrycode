package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"sync"
	"time"

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
	readings    map[liveReadingKey]*daemonLiveReading
	revisions   map[liveRevisionKey]uint64
	scopes      map[liveReadingKey]string
}

// The owner has no delivery, history or filesystem dependencies. Its mutex is
// never held while resolving a conversation, publishing or operating fan-in.
type daemonLiveState struct {
	mu            sync.Mutex
	resolve       func(string) (string, bool)
	conversations map[string]*liveConversation
	sources       map[streamProducerKey]daemonLiveSource
	turns         map[daemonLiveSource]*liveSourceTurn
	stopped       map[streamProducerKey]bool
}

var streamLiveFamilies = []string{
	protocol.TypeTurnState, protocol.TypeStall, protocol.TypeApiRetry,
	protocol.TypeCompacting, protocol.TypeThinkingProgress, protocol.TypeToolProgress,
	protocol.TypeBackgroundTaskProgress, protocol.TypeRateLimited, protocol.TypeContextUsage,
	protocol.TypeModelAnnounced, protocol.TypeSessionFacts, protocol.TypeMCPStatus,
	protocol.TypeSlashCommandList, protocol.TypeModelList,
}

func newDaemonLiveState(resolve func(string) (string, bool)) *daemonLiveState {
	return &daemonLiveState{resolve: resolve, conversations: make(map[string]*liveConversation), sources: make(map[streamProducerKey]daemonLiveSource), turns: make(map[daemonLiveSource]*liveSourceTurn), stopped: make(map[streamProducerKey]bool)}
}
func (o *daemonLiveState) capture(sid string, incarnation uint64, p history.SessionProvenance, activation bool) daemonLiveSource {
	if o == nil || o.resolve == nil {
		return daemonLiveSource{}
	}
	key := streamProducerKey{sid, incarnation}
	o.mu.Lock()
	if bound, ok := o.sources[key]; ok {
		if o.stopped[key] && p.Kind != "" {
			c := o.conversations[bound.ConversationID]
			if c != nil && c.generation == bound.SessionGeneration {
				o.advanceLocked(bound.ConversationID, c, sid, p)
				bound.SessionGeneration = c.generation
				c.incarnation = incarnation
			}
			delete(o.stopped, key)
		}
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
	} else if activation && (c.session != sid || (c.incarnation != 0 && c.incarnation != incarnation)) {
		if incarnation < c.incarnation {
			return daemonLiveSource{}
		}
		o.advanceLocked(conv, c, sid, p)
	}
	src := daemonLiveSource{ConversationID: conv, SessionGeneration: c.generation, provenance: p, producer: key}
	if c.session != sid {
		src.SessionGeneration = 0
	} else {
		c.incarnation = max(c.incarnation, incarnation)
	}
	o.sources[key] = src
	return src
}
func (o *daemonLiveState) newConversationLocked(conv, sid string) *liveConversation {
	c := &liveConversation{generation: 1, session: sid, readings: make(map[liveReadingKey]*daemonLiveReading), revisions: make(map[liveRevisionKey]uint64), scopes: make(map[liveReadingKey]string)}
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
		c = o.newConversationLocked(t.ConversationID, string(t.PreviousID))
	}
	incarnation := c.incarnation
	o.advanceLocked(t.ConversationID, c, string(t.NewID), p)
	if t.PreviousID == t.NewID && t.NewID != "" {
		key := streamProducerKey{string(t.NewID), incarnation}
		if src, ok := o.sources[key]; ok {
			src.SessionGeneration = c.generation
			src.provenance = p
			o.sources[key] = src
			c.incarnation = incarnation
		}
	}
	return o.snapshotLocked(t.ConversationID)
}

func (o *daemonLiveState) advanceLocked(conv string, c *liveConversation, sid string, p history.SessionProvenance) {
	c.generation++
	c.session = sid
	c.incarnation = 0
	c.readings = make(map[liveReadingKey]*daemonLiveReading)
	c.scopes = make(map[liveReadingKey]string)
	src := daemonLiveSource{ConversationID: conv, SessionGeneration: c.generation, provenance: p}
	for _, family := range streamLiveFamilies {
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
	return r
}
func validDaemonLiveEnvelope(e protocol.Envelope) bool {
	if len(e.Payload) > protocol.MaxThreadEnvelopeBytes || len(e.SessionID) > protocol.MaxThreadEnvelopeBytes || !slices.Contains(streamLiveFamilies, e.Type) || !json.Valid(e.Payload) {
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
	if c == nil || src.SessionGeneration == 0 {
		return daemonLiveReading{}, false
	}
	e.SessionID = liveSessionTag(src.provenance)
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if !validDaemonLiveEnvelope(e) {
		return daemonLiveReading{}, false
	}
	key := liveReadingKey{e.Type, id}
	rk := liveRevisionKey{src.SessionGeneration, key}
	c.revisions[rk]++
	r := daemonLiveReading{detachLiveEnvelope(e), src.ConversationID, src.SessionGeneration, id, c.revisions[rk]}
	if src.SessionGeneration == c.generation {
		c.readings[key] = &r
		c.scopes[key] = scope
	}
	return detachLiveReading(r), true
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
	if c == nil || r.SessionGeneration != c.generation {
		return false
	}
	key := liveReadingKey{r.Envelope.Type, r.ReadingID}
	rk := liveRevisionKey{r.SessionGeneration, key}
	if r.Revision <= c.revisions[rk] {
		return false
	}
	r = detachLiveReading(r)
	c.revisions[rk] = r.Revision
	c.readings[key] = &r
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
