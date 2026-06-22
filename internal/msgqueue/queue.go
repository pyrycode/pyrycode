// Package msgqueue is the in-memory, daemon-resident inbound backlog for
// phone-originated send_message turns. It is the inbound counterpart of
// internal/eventring (the outbound per-conversation event store): where
// eventring buffers what the daemon pushes OUT to phones, msgqueue buffers what
// phones send IN while claude is busy.
//
// Today send_message delivers synchronously — the handler blocks the per-conn
// goroutine inside WriteUserTurn's idle gate while claude is mid-turn, and the
// turn fails if claude stays busy past a 30s cap (#594). A phone that types
// while claude is working therefore either blocks or loses the message, and
// concurrent messages race across handler goroutines with no defined order.
//
// Queue fixes that. Enqueue appends a message to its conversation's FIFO and
// returns immediately (non-blocking), and a single drain goroutine per
// conversation delivers the backlog one at a time, in enqueue order, through an
// injected reliable-delivery seam (DeliverFunc, the shape of
// Supervisor.WriteUserTurn — the #594 ready-gate → commit-confirm → recovery
// path). The drain is paced entirely by that seam: DeliverFunc blocks while
// claude is busy and returns only on a confirmed commit, so a message enqueued
// mid-turn is held until the turn ends, one enqueued while claude is idle drains
// promptly, and there is never more than one in-flight delivery per
// conversation. No separate turn-state detector is needed.
//
// The backlog lives above the claude child's lifecycle: an undelivered message
// is retried at the FIFO head until it is confirmed delivered, so it survives a
// claude *child* respawn and drains into the new child. It deliberately does NOT
// survive a full daemon-process restart — the queue is purely in-memory, the
// same loss boundary as eventring; reconnect/resync covers it. There is no
// on-disk persistence in this slice.
//
// This slice ships the engine unwired (#704): no package depends on it yet. The
// live wiring into the send_message handler and the cmd/pyry constructor, plus
// the queue_state / dequeue_message reporting types and any inbound-bound
// policy, are separate slices (#705 / the wiring slice). The single insertion
// point for a future bound is Enqueue.
//
// SECURITY: the queued text is untrusted, phone-originated content bound for
// claude's stdin verbatim. It is treated as opaque transit bytes — stored,
// never inspected or used in a control decision, and converted to []byte only at
// the DeliverFunc call. It is NEVER logged at any level (mirrors
// internal/relay/handlers/send_message.go's discipline); the drain's
// warn-on-error logs only conversation_id, the queued message id, and the
// enqueue timestamp. convID is used solely as a map key; validating/resolving it
// to a real session is the caller's job (upstream of Enqueue), so a hostile
// convID can at worst create an isolated FIFO that never drains — never reach
// another conversation's session.
package msgqueue

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// defaultRetryInterval is the poll cadence the drain uses to re-attempt a head
// message after a delivery failure (claude unavailable during a child respawn, a
// wedged/uncommitted turn, or a PTY write error). It bridges the claude-child
// respawn window: WriteUserTurn returns ErrNoLiveSession immediately while the
// child is down, and the drain re-attempts the same head until the new child is
// live. A tuning knob, not a contract.
const defaultRetryInterval = 1 * time.Second

// DeliverFunc is the injected reliable-delivery seam — the shape of
// supervisor.WriteUserTurn. It MUST block while claude is busy (the WaitReady
// idle gate) and return nil ONLY on a confirmed commit; that blocking IS the
// drain's turn-end pacing. A non-nil return means the turn was not delivered and
// the drain retries the same message at the FIFO head.
type DeliverFunc func(ctx context.Context, convID string, payload []byte) error

// ChangeFunc is the injected change-notification seam — it mirrors the
// DeliverFunc seam style. It is invoked, NEVER while holding q.mu, with the id
// of the conversation whose backlog changed (on enqueue, on delivery-advance,
// and on a successful Remove). It carries only convID; the consumer re-reads the
// current backlog via Snapshot (the seam is edge-triggered — coalescing is the
// consumer's choice). nil disables notification.
//
// It MUST NOT block (a blocking ChangeFunc on the drain path stalls that
// conversation's drain) and MUST be safe for concurrent invocation: it fires
// from the Enqueue caller's goroutine, from each drain goroutine, and from the
// Remove caller's goroutine. The consumer owns its own synchronization.
type ChangeFunc func(convID string)

// Config configures a Queue.
type Config struct {
	// Deliver is the reliable-delivery seam; required. New errors if it is nil.
	Deliver DeliverFunc
	// RetryInterval is the poll cadence while delivery keeps failing.
	// <= 0 ⇒ defaultRetryInterval.
	RetryInterval time.Duration
	// OnChange is the optional change-notification seam; nil ⇒ disabled.
	OnChange ChangeFunc
	// Logger; nil ⇒ slog.Default().
	Logger *slog.Logger
}

// QueuedMessage is the engine-side projection of ADR 025's
// {queued_msg_id, text, ts} record (the producer maps ID -> queued_msg_id). It
// is the ordered element Snapshot returns. Text is untrusted, phone-originated
// transit content: the consumer must never log it and must only surface it to
// the authorized conversation it belongs to.
type QueuedMessage struct {
	ID   uint64
	Text string
	TS   time.Time
}

// queued is one buffered inbound message: the stable per-conversation id, the
// untrusted text bound for claude's stdin, and the enqueue timestamp (ADR 025's
// {queued_msg_id, text, ts}). text is opaque transit and is never logged.
type queued struct {
	id   uint64
	text string
	ts   time.Time
}

// convQueue is one conversation's FIFO plus its id counter and a flag tracking
// whether a drain goroutine is currently servicing it.
type convQueue struct {
	items    []queued
	nextID   uint64 // next id to assign for this conversation; starts at 1
	draining bool
}

// Queue is a per-conversation, in-memory inbound message backlog with one serial
// drain goroutine per active conversation. The zero value is not usable —
// construct with New. Enqueue is safe for concurrent use; Run is called once.
type Queue struct {
	deliver  DeliverFunc
	retry    time.Duration
	onChange ChangeFunc // nil ⇒ change notification disabled
	log      *slog.Logger

	mu      sync.Mutex
	convs   map[string]*convQueue
	ctx     context.Context // lifecycle ctx; set once by Run
	started bool            // true once Run has bound ctx
	closed  bool            // true once Run observed ctx.Done; gates new drain spawns
	wg      sync.WaitGroup  // joins drain goroutines on shutdown
}

// New constructs a Queue. It returns an error if cfg.Deliver is nil — the seam
// is caller-supplied, so a missing one is a wiring error to surface, not a
// programmer-constant to panic on (contrast eventring.New's bound). RetryInterval
// and Logger fall back to their defaults.
func New(cfg Config) (*Queue, error) {
	if cfg.Deliver == nil {
		return nil, errors.New("msgqueue: Config.Deliver is required")
	}
	retry := cfg.RetryInterval
	if retry <= 0 {
		retry = defaultRetryInterval
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Queue{
		deliver:  cfg.Deliver,
		retry:    retry,
		onChange: cfg.OnChange,
		log:      log,
		convs:    make(map[string]*convQueue),
	}, nil
}

// Enqueue appends text to convID's FIFO and returns the stable id assigned to it
// (>= 1, monotonic per conversation, constant until the message is delivered).
// It never blocks on delivery: if the lifecycle is running and no drain is
// already servicing the conversation, it spawns one and returns. Conversations'
// queues are independent — appending to one never blocks or reorders another.
func (q *Queue) Enqueue(convID, text string) uint64 {
	q.mu.Lock()
	c := q.convs[convID]
	if c == nil {
		c = &convQueue{nextID: 1}
		q.convs[convID] = c
	}
	id := c.nextID
	c.nextID++
	c.items = append(c.items, queued{id: id, text: text, ts: time.Now()})

	q.maybeSpawnDrainLocked(convID, c)
	q.mu.Unlock()

	// Fire the change seam after releasing q.mu: a re-entrant OnChange can call
	// Snapshot/Remove/Enqueue without deadlocking against the lock.
	q.notify(convID)
	return id
}

// Snapshot returns convID's not-yet-confirmed-delivered backlog as an ordered
// copy (the data queue_state reports). An unknown conversation yields an empty
// snapshot, not an error. The returned slice is freshly allocated and the
// elements are value copies, so a caller cannot mutate engine state through it.
//
// The snapshot includes the head even while it is mid-delivery: an item is in
// the backlog until advanceLocked drops it on a confirmed commit, so "current
// items" is exactly "not-yet-delivered". The snapshot does not flag which entry
// is in-flight — Remove returning false is how a consumer learns the head is
// non-removable.
func (q *Queue) Snapshot(convID string) []QueuedMessage {
	q.mu.Lock()
	defer q.mu.Unlock()

	c := q.convs[convID]
	if c == nil {
		return nil
	}
	out := make([]QueuedMessage, len(c.items))
	for i := range c.items {
		out[i] = QueuedMessage{ID: c.items[i].id, Text: c.items[i].text, TS: c.items[i].ts}
	}
	return out
}

// Remove drops a queued, not-in-flight message by id from convID's FIFO,
// preserving the surviving order, and returns true iff it removed one. An
// unknown conversation, an unknown or already-delivered id, or the in-flight
// (draining) head is a safe no-op that returns false — no panic, no reorder.
// This is the engine op behind dequeue_message; the in-flight-head no-op is what
// guarantees dequeue_message cannot cancel an in-flight delivery.
func (q *Queue) Remove(convID string, id uint64) bool {
	q.mu.Lock()
	c := q.convs[convID]
	if c == nil {
		q.mu.Unlock()
		return false
	}
	idx := -1
	for i := range c.items {
		if c.items[i].id == id {
			idx = i
			break
		}
	}
	// Not found, or the in-flight head: a no-op. The draining flag is set/cleared
	// under q.mu, the same lock the drain peeks and advances under, so the
	// in-flight-head decision is atomic w.r.t. the drain — removing index 0 only
	// when !draining (no goroutine owns items) can never make advanceLocked drop
	// the wrong message. Non-head removal (idx >= 1) is always safe: the drain
	// only ever touches index 0 and holds a value copy of the head.
	if idx == -1 || (idx == 0 && c.draining) {
		q.mu.Unlock()
		return false
	}
	c.items = append(c.items[:idx], c.items[idx+1:]...)
	c.shrinkLocked()
	q.mu.Unlock()

	q.notify(convID)
	return true
}

// notify fires the change seam for convID if one is configured. The caller MUST
// have released q.mu — OnChange is a caller-supplied seam that may block or
// re-enter Snapshot/Remove/Enqueue, so it is never called under the lock.
func (q *Queue) notify(convID string) {
	if q.onChange != nil {
		q.onChange(convID)
	}
}

// Run binds the lifecycle ctx, starts a drain for any conversation that already
// holds a backlog (covering Enqueue-before-Run, with no lost wakeup), then blocks
// until ctx is done and joins every drain goroutine before returning ctx.Err().
// It is called once; the daemon adds it to its errgroup in the wiring slice.
func (q *Queue) Run(ctx context.Context) error {
	q.mu.Lock()
	q.ctx = ctx
	q.started = true
	for convID, c := range q.convs {
		q.maybeSpawnDrainLocked(convID, c)
	}
	q.mu.Unlock()

	<-ctx.Done()

	// Stop spawning new drains, then join the in-flight ones. Setting closed
	// under q.mu before wg.Wait — taking each spawn's wg.Add under the same lock
	// — guarantees every Add happens-before this Wait: a concurrent Enqueue
	// either adds before closed is observed (so before Wait) or sees closed and
	// does not add at all.
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()

	q.wg.Wait()
	return ctx.Err()
}

// maybeSpawnDrainLocked starts a drain for c when the lifecycle is running, not
// shutting down, c has a backlog, and no drain is already servicing it. The
// caller must hold q.mu. The single lock hold makes the lazy-spawn race safe: a
// concurrent Enqueue either appends before a draining goroutine takes the lock
// (it sees len > 0 and keeps going) or after that goroutine cleared draining (so
// this respawns) — no interleave drops a message.
func (q *Queue) maybeSpawnDrainLocked(convID string, c *convQueue) {
	if !q.started || q.closed || c.draining || len(c.items) == 0 {
		return
	}
	c.draining = true
	q.wg.Add(1)
	go q.drain(q.ctx, convID)
}

// drain delivers convID's FIFO one message at a time, in order, until the queue
// empties (then it exits and a later Enqueue respawns it) or ctx is cancelled. It
// peeks the head under the lock and advances only after a confirmed delivery, so
// no message is lost: a failure leaves the head in place to be retried after
// q.retry, which is what bridges a claude-child respawn. q.mu is never held
// across deliver, which can block for a whole claude turn.
func (q *Queue) drain(ctx context.Context, convID string) {
	defer q.wg.Done()
	for {
		q.mu.Lock()
		c := q.convs[convID]
		if len(c.items) == 0 {
			c.draining = false
			q.mu.Unlock()
			return
		}
		head := c.items[0]
		q.mu.Unlock()

		err := q.deliver(ctx, convID, []byte(head.text))
		if ctx.Err() != nil {
			// Shutdown raced the delivery. Leave the head queued (the in-memory
			// daemon-restart loss boundary) and exit so Run's wg.Wait unblocks.
			q.mu.Lock()
			c.draining = false
			q.mu.Unlock()
			return
		}
		if err != nil {
			// Claude unavailable (child respawn / wedged turn / PTY write error).
			// Retry the SAME head — lossless, and what makes a message survive a
			// child respawn. NEVER log head.text: it is untrusted phone content.
			q.log.Warn("msgqueue: delivery failed, will retry",
				"conversation_id", convID,
				"queued_msg_id", head.id,
				"queued_at", head.ts,
				"err", err)
			if !sleepCtx(ctx, q.retry) {
				q.mu.Lock()
				c.draining = false
				q.mu.Unlock()
				return
			}
			continue
		}

		q.mu.Lock()
		c.advanceLocked()
		q.mu.Unlock()

		// A confirmed-delivered head left the backlog. Fire after unlock; do NOT
		// fire on the empty-exit or delivery-error/retry paths — those aren't
		// backlog changes.
		q.notify(convID)
	}
}

// advanceLocked drops the just-delivered head and runs the backing-array
// hygiene. The caller must hold q.mu.
func (c *convQueue) advanceLocked() {
	c.items = c.items[1:]
	c.shrinkLocked()
}

// shrinkLocked releases the backing array when the FIFO empties and compacts
// when capacity dwarfs the live length, so a long-lived conversation's slice
// does not retain an ever-growing backing array from past bursts. Shared by
// advanceLocked (head drop) and Remove (mid-FIFO drop). Queues are expected
// shallow, so the compaction rarely fires. The caller must hold q.mu.
func (c *convQueue) shrinkLocked() {
	switch {
	case len(c.items) == 0:
		c.items = nil
	case cap(c.items) > 2*len(c.items):
		compact := make([]queued, len(c.items))
		copy(compact, c.items)
		c.items = compact
	}
}

// sleepCtx blocks for d or until ctx is done. Returns true if the full delay
// elapsed, false if ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
