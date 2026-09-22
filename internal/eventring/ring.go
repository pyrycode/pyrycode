// Package eventring is the in-memory, daemon-resident store of recent
// structured turn events that the mid-turn-reconnect replay path (#647) reads
// to catch a returning phone up without a gap.
//
// The interactive emitter (cmd/pyry) appends every envelope it fans out, once
// per logical event (before the per-conn fan-out), keyed by a durable event id
// that is unique daemon-wide: one counter serves the whole ring, so no two
// events share an id whatever conversation they belong to (#2022). The id is
// connection-independent — the same logical event carries the same id
// regardless of how many phones receive it —
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
	ID      uint64          // durable event id, unique ring-wide (>= 1, strictly increasing in append order)
	Type    string          // protocol.Type* wire type
	Payload json.RawMessage // the already-marshalled envelope payload
	TS      time.Time       // the logical event's timestamp
}

// Ring is a bounded, per-conversation store of recent Events keyed by a durable
// event id drawn from a single ring-wide counter. The zero value is not usable —
// construct with New. All methods are safe for concurrent use.
//
// Retention is per conversation (each keeps at most maxPerConv events, evicted
// under its own policy); the id space is not. Splitting the two is the #2022
// fix: the two cursors that consume these ids — hello.last_event_id on the wire
// and V2Session.replayThrough in the daemon — are both single scalars carrying
// no conversation tag, so an id space that restarted per conversation let a
// watermark taken for one conversation silently mute another's live stream.
type Ring struct {
	mu         sync.Mutex
	nextID     uint64 // the next id to assign, ring-wide; starts at 1
	maxPerConv int
	convs      map[string]*convRing
}

// convRing holds one conversation's view of the ring-wide id space and its
// retained events. events is always kept in ascending id order: appends are
// strictly increasing, and middle-deletion of an evicted delta preserves order,
// so no re-sort is ever needed. Ascending, but since #2022 not contiguous even
// without eviction — another conversation's appends take ids in between.
type convRing struct {
	// latestID is the highest id assigned to this conversation, 0 if none. It is
	// the classification boundary After compares against and the value NewestID
	// reports; it advances on every Append for this conversation, independent of
	// retention, so it is always the highest RETAINED id too (the newest event is
	// never evicted).
	latestID uint64

	// evictedThrough is the highest id removed from the FRONT of this
	// conversation's retained window, 0 if none — so every id of this
	// conversation at or below it is gone, and nothing above it has been removed
	// except middle deltas. It is monotonic: position 0 always holds the smallest
	// retained id and the slice stays ascending, so successive front removals
	// remove ascending ids. After's aged-out branch reads it instead of inferring
	// the back edge from contiguity, which a sparse id space breaks.
	evictedThrough uint64

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
		nextID:     1,
		maxPerConv: maxPerConversation,
		convs:      make(map[string]*convRing),
	}
}

// Append assigns the next durable id, stores the event under convID, and returns
// the assigned id. Ids come from one ring-wide counter starting at 1, so they
// strictly increase in append order across the whole ring and are never shared
// by two conversations (#2022); within any one conversation they still strictly
// increase, but ascending-not-contiguous, since another conversation's appends
// take ids in between. The counter advances on every call, independent of how
// many events are currently retained — so an id is never reused even after
// eviction.
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
		c = &convRing{}
		r.convs[convID] = c
	}

	id := r.nextID
	r.nextID++
	c.latestID = id

	if len(c.events) >= r.maxPerConv {
		c.evictOldest()
	}
	c.events = append(c.events, Event{ID: id, Type: typ, Payload: payload, TS: ts})
	return id
}

// evictOldest removes one event to make room: the oldest assistant_delta if any
// is retained (smallest id, hence earliest in the ascending slice), otherwise
// the oldest event overall (a control event). The slice stays ascending in id.
//
// A removal at position 0 moves the window's back edge — the removed event is
// the oldest RETAINED one, whatever its class — so it advances evictedThrough,
// which is what After's aged-out branch classifies against. A removal from any
// other position leaves an older event in place, so the back edge has not moved:
// that is the middle-delta case the ring has always refused to call a gap.
func (c *convRing) evictOldest() {
	idx := 0 // default: the oldest event overall (the all-control case)
	for i := range c.events {
		if c.events[i].Type == protocol.TypeAssistantDelta {
			idx = i
			break
		}
	}
	if idx == 0 {
		c.evictedThrough = c.events[0].ID
	}
	c.events = append(c.events[:idx], c.events[idx+1:]...)
}

// After returns the retained events whose id is greater than afterID for
// convID, in ascending id order, never returning another conversation's events.
// The (events, gap) pair distinguishes three outcomes for the #647 consumer:
//
//   - Caught up — afterID == the latest id assigned: (nil, false). Nothing to
//     replay.
//   - Gap — an event of this conversation newer than afterID fell off the back
//     of the ring (afterID is below evictedThrough): (nil, true). The consumer
//     missed some events and must resync. An afterID beyond the latest id
//     assigned is a gap too, on different reasoning: it names an id this daemon
//     never issued to this conversation, whether the conversation is unknown
//     (queried with afterID > 0) or known but younger than the cursor — the
//     shape a daemon restart leaves behind, wiping the ring so ids restart at 1
//     while the consumer still holds a high cursor (#1494).
//   - Replay — otherwise: (events with id > afterID, false).
//
// A missing middle delta (evicted while older events are still retained) is NOT
// a gap: gap is signalled only by the window's back edge passing the consumer's
// cursor, never by a hole left behind by delta eviction.
//
// The aged-out test reads evictedThrough rather than inferring the back edge
// from contiguity (#2022). The old test — "the oldest retained id is newer than
// afterID+1" — assumed a conversation's ids ran 1, 2, 3 …, which stopped being
// true when ids became ring-wide: a conversation whose retained window is
// complete but starts at 41 would be read as having lost everything below it,
// and a spurious gap is a spurious resync, i.e. a full client reload. It also
// computed afterID+1, which wraps to 0 at math.MaxUint64; nothing here does
// arithmetic on the untrusted cursor any more.
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

	latestID := c.latestID
	if afterID > latestID {
		return nil, true // an id this daemon never issued → gap
	}
	if afterID == latestID {
		return nil, false // caught up: exactly at the latest id
	}
	if afterID < c.evictedThrough {
		return nil, true // an event newer than the cursor fell off the back → gap
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

// Drop removes every retained event of convID, freeing its entry (#1502). The
// daemon calls it when the conversation itself is removed — a delete_conversation
// or an idle sweep — so a removed conversation's events stop pinning memory
// until the next restart. Dropping an unknown conversation is a no-op.
//
// Afterwards convID reads exactly like a conversation the ring never saw:
// NewestID reports 0, After(convID, 0) is caught up, and a cursor naming one of
// its old ids is a gap — the right answer, since the conversation no longer
// exists. The ring-wide counter is deliberately left alone: ids are never
// reissued, so a later Append (for this conversation too) gets an id above
// every one issued before, and no scalar cursor can alias a dropped id (#2022).
func (r *Ring) Drop(convID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.convs, convID)
}

// NewestID returns the newest durable event id retained for convID — the id the
// most recent Append for it assigned (convRing.latestID, the same boundary After
// classifies against) — or 0 if the conversation is unknown / has had no events.
// Because the newest event is never evicted (eviction takes the oldest first)
// and latestID advances on every Append for this conversation independent of
// retention, this equals the highest retained event's id. Since #2022 the id
// comes from the ring-wide counter, so this is NOT a count of the conversation's
// events and NOT comparable across conversations as an ordering of their
// activity — it is only this conversation's own high-water mark.
//
// It is the #647-MUST-FIX (#663) caught-up-watermark clamp source: replayMissed
// bounds the per-conn dedup watermark to min(afterID, NewestID). Since #1494 an
// untrusted remote last_event_id beyond this conversation's id space classifies
// as a gap and returns before that clamp, so what the clamp still defends is the
// window between this read and the After call: an Append landing in it can leave
// this value one or more ids behind the latestID After then classifies against,
// and min keeps the watermark at the stale-but-real id so the concurrently
// appended event still reaches the wire. Locked by the ring's existing mutex —
// same safe-off-the-emitter-goroutine guarantee as After.
func (r *Ring) NewestID(convID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	c := r.convs[convID]
	if c == nil {
		return 0
	}
	return c.latestID
}
