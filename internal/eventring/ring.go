// Package eventring is the in-memory, daemon-resident store of recent
// structured turn events that the mid-turn-reconnect replay path (#647) reads
// to catch a returning phone up without a gap.
//
// The interactive emitter (cmd/pyry) appends every envelope it fans out, once
// per logical event (before the per-conn fan-out), keyed by a durable
// per-conversation event id. The id is connection-independent — the same
// logical event carries the same id regardless of how many phones receive it —
// and lives in the long-lived pyry daemon, so it survives a supervised
// claude-child respawn and phone reconnects (the daemon stays up across both).
// It deliberately does NOT survive a full daemon-process restart: the ring is
// purely in-memory, and the AC-5 "you missed some" gap signal lets #647 fall
// back to a full resync across that boundary.
//
// Storage is bounded: each conversation retains at most MaxEventsPerConversation
// events. When the bound is reached, the oldest assistant_delta is evicted
// first — deltas are lossy/coalescable per ADR 025 — and control-class events
// (turn_state, tool_use, tool_result, turn_end, stall) are retained in
// preference. Memory therefore does not grow without limit across a long
// session.
//
// The ring is the only shared object on the structured path: it is internally
// synchronised by a sync.Mutex, so the producer (append, on the emitter's
// single Run goroutine) and the future reconnect path (query, on the manager's
// goroutine) can both touch it without the emitter taking on any locking of its
// own. The emitter's other state stays unguarded and single-goroutine.
package eventring

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// MaxEventsPerConversation is the AC-2 named bound on retained events per
// conversation. It is a tunable starting point, not load-tested — ADR 025
// § Roadmap flags the droppable-delta policy as needing a real load test; the
// #647 reconnect e2e (or a Phase 2 load test) is the right place to calibrate
// it. Keeping it a named constant makes that tuning one edit.
const MaxEventsPerConversation = 1024

// Event is one retained structured event: the durable id plus the three
// replay-relevant fields of the envelope it came from. The per-conn envelope ID
// is deliberately NOT stored — it is meaningless for replay across connections
// (#647 reconstructs a fresh protocol.Envelope per reconnecting conn from the
// stored Type/TS/Payload).
//
// Payload is treated as immutable: the appender owns the bytes and does not
// mutate them after Append, and After returns the reference without copying the
// bytes.
type Event struct {
	ID      uint64          // durable per-conversation event id (>= 1, strictly increasing within a conversation)
	Type    string          // protocol.Type* wire type
	Payload json.RawMessage // the already-marshalled envelope payload
	TS      time.Time       // the logical event's timestamp
}

// Ring is a bounded, per-conversation store of recent Events keyed by a durable
// per-conversation event id. The zero value is not usable — construct with New.
// All methods are safe for concurrent use.
type Ring struct {
	mu         sync.Mutex
	maxPerConv int
	convs      map[string]*convRing
}

// convRing holds one conversation's durable counter and its retained events.
// events is always kept in ascending id order: appends are strictly increasing,
// and middle-deletion of an evicted delta preserves order, so no re-sort is
// ever needed.
type convRing struct {
	nextID uint64 // the next id to assign for this conversation; starts at 1
	events []Event
}

// New returns a Ring that retains at most maxPerConversation events per
// conversation. It panics if maxPerConversation < 1 — a programmer error,
// matching the panic-on-misconfig style of dispatch.New / V2SessionManager for
// required invariants.
func New(maxPerConversation int) *Ring {
	if maxPerConversation < 1 {
		panic("eventring: maxPerConversation must be >= 1")
	}
	return &Ring{
		maxPerConv: maxPerConversation,
		convs:      make(map[string]*convRing),
	}
}

// Append assigns the next durable id for convID, stores the event, and returns
// the assigned id. Ids strictly increase within a conversation and start at 1;
// each conversation has its own counter. The counter advances on every call,
// independent of how many events are currently retained — so an id is never
// reused even after eviction.
//
// When the conversation is already at the bound, the oldest assistant_delta is
// evicted first (control events are retained in preference); if no delta is
// retained, the oldest event overall is evicted to honour the hard memory
// bound. Eviction happens before the new event is appended, so the retained
// count never exceeds the bound.
//
// payload is stored by reference and must not be mutated by the caller after
// the call returns.
func (r *Ring) Append(convID, typ string, payload json.RawMessage, ts time.Time) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	c := r.convs[convID]
	if c == nil {
		c = &convRing{nextID: 1}
		r.convs[convID] = c
	}

	id := c.nextID
	c.nextID++

	if len(c.events) >= r.maxPerConv {
		c.evictOldest()
	}
	c.events = append(c.events, Event{ID: id, Type: typ, Payload: payload, TS: ts})
	return id
}

// evictOldest removes one event to make room: the oldest assistant_delta if any
// is retained (smallest id, hence earliest in the ascending slice), otherwise
// the oldest event overall (a control event). The slice stays ascending in id.
func (c *convRing) evictOldest() {
	idx := 0 // default: the oldest event overall (the all-control case)
	for i := range c.events {
		if c.events[i].Type == protocol.TypeAssistantDelta {
			idx = i
			break
		}
	}
	c.events = append(c.events[:idx], c.events[idx+1:]...)
}

// After returns the retained events whose id is greater than afterID for
// convID, in ascending id order, never returning another conversation's events.
// The (events, gap) pair distinguishes three outcomes for the #647 consumer:
//
//   - Caught up — afterID >= the latest id assigned: (nil, false). Nothing to
//     replay.
//   - Gap — the consumer's next-expected event (afterID+1) fell off the back of
//     the ring (the oldest retained id is newer than it): (nil, true). The
//     consumer missed some events and must resync. An unknown conversation
//     queried with afterID > 0 is also a gap — the consumer references events
//     this daemon never had (e.g. after a daemon restart wiped the ring).
//   - Replay — otherwise: (events with id > afterID, false).
//
// A missing middle delta (evicted while older events are still retained) is NOT
// a gap: gap is signalled only by the oldest retained id passing the consumer's
// cursor, never by a hole left behind by delta eviction.
func (r *Ring) After(convID string, afterID uint64) (events []Event, gap bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	c := r.convs[convID]
	if c == nil || len(c.events) == 0 {
		// No events retained for this conversation. A fresh consumer
		// (afterID == 0) is caught up; one naming a prior id references events
		// this daemon never had → gap, so the consumer resyncs.
		return nil, afterID > 0
	}

	latestID := c.nextID - 1
	if afterID >= latestID {
		return nil, false // caught up
	}
	if c.events[0].ID > afterID+1 {
		return nil, true // next-expected event fell off the back → gap
	}
	for i := range c.events {
		if c.events[i].ID > afterID {
			out := make([]Event, len(c.events)-i)
			copy(out, c.events[i:])
			return out, false
		}
	}
	return nil, false // unreachable: afterID < latestID guarantees a match
}
