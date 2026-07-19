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
// The package is wired into the daemon: cmd/pyry constructs the queue via
// msgqueue.New (cmd/pyry/main.go), drives delivery through the DeliverFunc seam
// (newInboundDeliver), reports backlog changes through the queue-state emitter
// (newQueueStateEmitterV2), and the send_message handler
// (internal/relay/handlers/send_message.go) is the enqueue caller (*msgqueue.Queue
// satisfies its Enqueuer interface). Enqueue is the single insertion point for the
// per-conversation backlog bound (#869): an Enqueue past the cap is rejected
// (returns 0) — never dropped, never evicting the oldest.
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
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/turncommit"
)

// defaultRetryInterval is the poll cadence the drain uses to re-attempt a head
// message after a delivery failure (claude unavailable during a child respawn, a
// wedged/uncommitted turn, or a PTY write error). It bridges the claude-child
// respawn window: WriteUserTurn returns ErrNoLiveSession immediately while the
// child is down, and the drain re-attempts the same head until the new child is
// live. A tuning knob, not a contract.
const defaultRetryInterval = 1 * time.Second

// defaultMaxQueuedPerConversation caps a single conversation's in-memory inbound
// backlog. A phone flooding send_message while claude is wedged is rejected past
// this many not-yet-delivered messages (reject, never drop) — bounding the
// in-memory DoS surface #704's security review flagged (each queued message can
// be up to the transport's 1 MiB frame ceiling). A tuning knob, not a contract —
// same posture as defaultRetryInterval; not load-tested.
const defaultMaxQueuedPerConversation = 100

// defaultGiveUpAfter bounds how long the drain retries a persistently-failing
// head before abandoning it. Chosen to exceed the supervisor's max backoff
// window with margin: BackoffMax is 30s and BackoffReset 60s
// (internal/supervisor/supervisor.go), so a transient failure spanning a full
// claude-child respawn/backoff cycle clears well inside this bound and never
// trips give-up. A tuning knob, not a contract.
const defaultGiveUpAfter = 2 * time.Minute

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

// GiveUpFunc is the injected give-up-notification seam. It mirrors ChangeFunc:
// invoked NEVER while holding q.mu, with the conversation whose head the drain
// abandoned after persistent delivery failure, plus a daemon-generated
// human-readable reason (the elapsed retry window and the last delivery error)
// that NEVER contains the queued message text. It MUST NOT block and MUST be
// safe for concurrent invocation. nil disables notification.
//
// A later ticket routes this seam to a typed, client-visible error frame over
// the v2 wire exactly as OnChange routes to the queue_state producer; until then
// it ships disabled (nil), so the bound + head-drop + clean exit are the only
// production-visible effect — the give-up is real even when unobserved.
type GiveUpFunc func(convID, reason string)

// PendingFunc classifies a delivery error as a legitimate hold — the head is
// being deliberately withheld awaiting an external decision (e.g. claude's
// startup trust-folder modal is up, so DeliverFunc declined before touching the
// consent gate) — rather than a delivery failure. It is an injected predicate
// because msgqueue is a leaf that must not import the delivery seam's package
// (internal/supervisor); the composition root wires it as
// errors.Is(err, supervisor.ErrTrustModalPending). It MUST be a pure, non-blocking
// function (it is called on the drain path). nil ⇒ every non-nil delivery error
// counts toward the give-up bound (pre-#1014 behaviour). #1014 AC-1.
type PendingFunc func(error) bool

// Config configures a Queue.
type Config struct {
	// Deliver is the reliable-delivery seam; required. New errors if it is nil.
	Deliver DeliverFunc
	// RetryInterval is the poll cadence while delivery keeps failing.
	// <= 0 ⇒ defaultRetryInterval.
	RetryInterval time.Duration
	// MaxQueuedPerConversation caps each conversation's not-yet-delivered
	// backlog; an Enqueue past it is rejected (reject, never drop).
	// <= 0 ⇒ defaultMaxQueuedPerConversation. A per-conversation bound,
	// independent across conversations.
	MaxQueuedPerConversation int
	// OnChange is the optional change-notification seam; nil ⇒ disabled.
	OnChange ChangeFunc
	// GiveUpAfter bounds how long the drain retries a persistently-failing head
	// before abandoning it. <= 0 ⇒ defaultGiveUpAfter. Measured as elapsed
	// wall-clock since the head's FIRST consecutive delivery failure; a
	// successful delivery resets the clock for the next head (per-head, not
	// per-session).
	GiveUpAfter time.Duration
	// OnGiveUp is the optional give-up-notification seam; nil ⇒ disabled.
	OnGiveUp GiveUpFunc
	// Pending classifies a delivery error as a legitimate hold rather than a
	// failure. While it returns true the drain retries the head WITHOUT counting
	// the elapsed window toward GiveUpAfter and RESETS the give-up streak, so a
	// slow-but-valid remote decision (e.g. a folder-trust accept) never races a
	// premature give-up, and a transient real failure preceding the hold cannot
	// leak into the held window. nil ⇒ every non-nil delivery error counts
	// (pre-#1014 behaviour). #1014 AC-1.
	Pending PendingFunc
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

// convQueue is one conversation's FIFO plus its id counter and the drain-state
// flags that gate removal of the head.
//
// draining is true while a drain goroutine is servicing this conversation. It
// does NOT mean the head is being written: the drain spends most of its time
// blocked inside the delivery seam WAITING for claude to go idle. committing is
// the finer flag that gates head removal (#487): it is set, via commitGate, only
// once the seam is past that wait and is actually writing the head, and it is the
// only state in which the head is un-droppable. A head that is merely waiting
// (draining && !committing) IS droppable — that is the message a user queued
// behind a running turn precisely so they could still cancel it. deliverCancel
// cancels the in-flight delivery's context so dropping the waiting head unblocks
// its idle-gate wait at once; it is nil whenever no delivery is in flight.
type convQueue struct {
	items         []queued
	nextID        uint64 // next id to assign for this conversation; starts at 1
	draining      bool
	committing    bool               // the head is past the idle-gate wait and is being written; un-droppable
	deliverCancel context.CancelFunc // cancels the in-flight delivery ctx; nil when no delivery is in flight
}

// Queue is a per-conversation, in-memory inbound message backlog with one serial
// drain goroutine per active conversation. The zero value is not usable —
// construct with New. Enqueue is safe for concurrent use; Run is called once.
type Queue struct {
	deliver     DeliverFunc
	retry       time.Duration
	giveUpAfter time.Duration // bounds persistent-failure retry before give-up
	max         int           // per-conversation backlog cap; > 0 always in practice
	onChange    ChangeFunc    // nil ⇒ change notification disabled
	onGiveUp    GiveUpFunc    // nil ⇒ give-up notification disabled
	pending     PendingFunc   // nil ⇒ no delivery error is treated as a hold
	log         *slog.Logger

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
	giveUpAfter := cfg.GiveUpAfter
	if giveUpAfter <= 0 {
		giveUpAfter = defaultGiveUpAfter
	}
	max := cfg.MaxQueuedPerConversation
	if max <= 0 {
		max = defaultMaxQueuedPerConversation
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Queue{
		deliver:     cfg.Deliver,
		retry:       retry,
		giveUpAfter: giveUpAfter,
		max:         max,
		onChange:    cfg.OnChange,
		onGiveUp:    cfg.OnGiveUp,
		pending:     cfg.Pending,
		log:         log,
		convs:       make(map[string]*convQueue),
	}, nil
}

// Enqueue appends text to convID's FIFO and returns the stable id assigned to it
// (>= 1, monotonic per conversation, constant until the message is delivered).
// It never blocks on delivery: if the lifecycle is running and no drain is
// already servicing the conversation, it spawns one and returns. Conversations'
// queues are independent — appending to one never blocks or reorders another.
//
// If the conversation's backlog already holds the per-conversation cap
// (Config.MaxQueuedPerConversation, default defaultMaxQueuedPerConversation),
// Enqueue REJECTS the message: it returns 0 (never a valid id) without appending,
// consumes no id, and leaves the existing backlog — including the in-flight head
// — untouched (reject, never drop). The one caller (the send_message handler)
// maps a 0 return to a retryable "backlog full" reply so the phone re-issues
// later.
func (q *Queue) Enqueue(convID, text string) uint64 {
	q.mu.Lock()
	c := q.convs[convID]
	if c == nil {
		c = &convQueue{nextID: 1}
		q.convs[convID] = c
	}
	// Cap the backlog at the single insertion point (#869). len(c.items) is the
	// whole in-memory backlog INCLUDING the in-flight (draining) head — exactly
	// what Snapshot reports and the memory-DoS surface — so the cap, Snapshot,
	// and queue_state all agree on "backlog size". At capacity, reject with a
	// pure early return: no id bump (a rejected message consumes no id, keeping
	// accepted ids dense), no append, no notify (no backlog change), no drain
	// spawn. Reject, never drop: the already-queued messages keep draining in
	// enqueue order. text is untrusted phone content and is never touched here.
	if q.max > 0 && len(c.items) >= q.max {
		q.mu.Unlock()
		return 0
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

// SnapshotAll returns every conversation's not-yet-confirmed-delivered backlog
// keyed by conversation id, omitting any conversation whose backlog is empty.
// Each value is a freshly allocated slice of value copies (identical semantics to
// Snapshot, including the in-flight head), so a caller cannot mutate engine state
// through it. A pure read: it mints no id and dequeues nothing. The empty
// (non-nil) map means no conversation holds a backlog. It is the connect-time
// queue reconcile's enumeration seam (#878) — Snapshot reads one named
// conversation, but convs is private, so enumerating the non-empty ones needs
// this read.
//
// The whole read runs under q.mu, so the returned set is a single consistent
// instant with no window between enumerating the conversations and reading each
// one. A drained-but-retained convQueue (advanceLocked/shrinkLocked leaves the
// entry with items == nil) is skipped, so an empty conversation contributes no
// map entry and therefore no queue_state re-send.
func (q *Queue) SnapshotAll() map[string][]QueuedMessage {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make(map[string][]QueuedMessage, len(q.convs))
	for convID, c := range q.convs {
		if len(c.items) == 0 {
			continue // drained-but-retained (items == nil) ⇒ no entry (AC3).
		}
		msgs := make([]QueuedMessage, len(c.items))
		for i := range c.items {
			msgs[i] = QueuedMessage{ID: c.items[i].id, Text: c.items[i].text, TS: c.items[i].ts}
		}
		out[convID] = msgs
	}
	return out
}

// Remove drops a queued, not-yet-committing message by id from convID's FIFO,
// preserving the surviving order, and returns true iff it removed one. An unknown
// conversation, an unknown or already-delivered id, or the head while it is
// committing (the write to claude has begun) is a safe no-op that returns false —
// no panic, no reorder. This is the engine op behind dequeue_message.
//
// The head is removable while it is only WAITING for claude to go idle
// (draining && !committing): that is the message a user queued behind a running
// turn precisely so they could still cancel it (#487). Removing the waiting head
// also cancels its in-flight delivery so the seam's idle-gate wait unblocks at
// once and the drain advances without writing it. Only once the seam claims the
// head via commitGate (committing) does the drop no-op, so dequeue_message can
// never cancel a write already in progress. The committing flag is set/cleared
// under q.mu, the same lock this method takes, so the claim-versus-drop decision
// is atomic: either the claim wins and the drop no-ops, or the drop wins and the
// claim reports the head dropped.
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
	if idx == -1 || (idx == 0 && c.committing) {
		q.mu.Unlock()
		return false
	}
	// Dropping the current head must cancel its in-flight delivery so the seam's
	// idle-gate wait returns at once and the drain re-loops without writing the
	// dropped message. deliverCancel is nil when no delivery is in flight (the
	// drain is between attempts), in which case the drain simply re-peeks and finds
	// the head gone. A non-head removal (idx >= 1) never touches the in-flight head.
	cancel := c.deliverCancel
	isHead := idx == 0
	c.items = append(c.items[:idx], c.items[idx+1:]...)
	c.shrinkLocked()
	q.mu.Unlock()

	if isHead && cancel != nil {
		cancel()
	}
	q.notify(convID)
	return true
}

// commitGate returns the turncommit.Gate the drain passes down the delivery ctx
// for the head identified by headID. The delivery seam calls it once, after the
// idle-gate wait and before the write. Under q.mu it CLAIMS the head — marking it
// committing so a concurrent Remove of it no-ops — and returns true so the write
// proceeds, unless the head was already dropped during the wait (it is no longer
// at the front of the FIFO), in which case it returns false so the seam aborts
// without writing. Taking q.mu makes the claim atomic with Remove.
func (q *Queue) commitGate(convID string, headID uint64) turncommit.Gate {
	return func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		c := q.convs[convID]
		if c == nil || len(c.items) == 0 || c.items[0].id != headID {
			return false
		}
		c.committing = true
		return true
	}
}

// notify fires the change seam for convID if one is configured. The caller MUST
// have released q.mu — OnChange is a caller-supplied seam that may block or
// re-enter Snapshot/Remove/Enqueue, so it is never called under the lock.
func (q *Queue) notify(convID string) {
	if q.onChange != nil {
		q.onChange(convID)
	}
}

// notifyGiveUp fires the give-up seam for convID if one is configured. Like
// notify, the caller MUST have released q.mu — OnGiveUp is a caller-supplied
// seam that may block or re-enter, so it is never called under the lock. reason
// is daemon-generated and never carries the queued message text.
func (q *Queue) notifyGiveUp(convID, reason string) {
	if q.onGiveUp != nil {
		q.onGiveUp(convID, reason)
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
	// firstFailedAt is the start of the current head's consecutive-failure streak
	// (zero ⇒ the head has not yet failed). It bounds persistent-failure retry
	// per head: a successful delivery resets it, so each head gets a fresh
	// give-up window and a transient failure that clears on a respawn never
	// trips give-up. Drain-local: one goroutine per conversation owns it, so it
	// needs no synchronization.
	var firstFailedAt time.Time
	for {
		q.mu.Lock()
		c := q.convs[convID]
		if len(c.items) == 0 {
			c.draining = false
			q.mu.Unlock()
			return
		}
		head := c.items[0]
		// Per-attempt cancelable ctx so dropping this head while it waits for claude
		// to go idle unblocks the delivery at once (#487). committing starts false —
		// the head is droppable until the seam claims it via commitGate — and
		// deliverCancel lets Remove cancel this exact attempt.
		deliverCtx, cancelDeliver := context.WithCancel(ctx)
		c.committing = false
		c.deliverCancel = cancelDeliver
		q.mu.Unlock()

		gate := q.commitGate(convID, head.id)
		err := q.deliver(turncommit.With(deliverCtx, gate), convID, []byte(head.text))
		cancelDeliver()

		q.mu.Lock()
		c.deliverCancel = nil
		c.committing = false
		// A drop during the idle-gate wait removed this head from the FIFO (and
		// canceled the delivery above), so it is neither advanced nor retried.
		dropped := len(c.items) == 0 || c.items[0].id != head.id
		q.mu.Unlock()

		if ctx.Err() != nil {
			// Shutdown raced the delivery. Leave the head queued (the in-memory
			// daemon-restart loss boundary) and exit so Run's wg.Wait unblocks.
			q.mu.Lock()
			c.draining = false
			q.mu.Unlock()
			return
		}

		if err == nil {
			q.mu.Lock()
			c.advanceLocked()
			q.mu.Unlock()

			// A confirmed-delivered head left the backlog. Reset the give-up clock so
			// the next head starts with a fresh bound. Fire after unlock; do NOT fire
			// on the empty-exit, drop, or delivery-error/retry paths — those aren't
			// confirmed deliveries.
			firstFailedAt = time.Time{}
			q.notify(convID)
			continue
		}

		if dropped {
			// The head was dropped before it committed: a clean cancellation, not a
			// delivery failure. It is already gone from the FIFO, so do not retry it
			// or count it toward give-up — advance to the next head with a fresh clock.
			firstFailedAt = time.Time{}
			continue
		}

		// err != nil and the head is still queued: a real delivery outcome.
		// A legitimate hold (#1014 AC-1): the head was deliberately withheld
		// awaiting an external decision (claude's startup trust modal is up), not a
		// delivery failure. Reset the give-up streak — identical to the
		// confirmed-delivery reset above — so the pending window never counts toward
		// the bound AND a prior real-failure streak cannot survive into it, then
		// retry the same head after q.retry. Debug (not Warn) and content-free: a
		// long legitimate wait must not spam the operator log, and NEVER log
		// head.text (untrusted phone content). The ctx.Err() check above stays first,
		// so shutdown always wins over a hold.
		if q.pending != nil && q.pending(err) {
			firstFailedAt = time.Time{}
			q.log.Debug("msgqueue: delivery held (awaiting external decision), will retry",
				"conversation_id", convID,
				"queued_msg_id", head.id,
				"queued_at", head.ts)
			if !sleepCtx(ctx, q.retry) {
				q.mu.Lock()
				c.draining = false
				q.mu.Unlock()
				return
			}
			continue
		}
		// Claude unavailable (child respawn / wedged turn / PTY write error).
		// Retry the SAME head — lossless, and what makes a message survive a
		// child respawn. NEVER log head.text: it is untrusted phone content.
		if firstFailedAt.IsZero() {
			firstFailedAt = time.Now()
		}
		q.log.Warn("msgqueue: delivery failed, will retry",
			"conversation_id", convID,
			"queued_msg_id", head.id,
			"queued_at", head.ts,
			"err", err)
		if elapsed := time.Since(firstFailedAt); elapsed >= q.giveUpAfter {
			q.giveUp(convID, c, head, elapsed, err)
			return
		}
		if !sleepCtx(ctx, q.retry) {
			q.mu.Lock()
			c.draining = false
			q.mu.Unlock()
			return
		}
	}
}

// giveUp abandons a head that has failed delivery for at least q.giveUpAfter (a
// claude session wedged at startup, not merely respawning): it drops the head,
// clears draining, and fires both seams off-lock, then the caller exits the
// drain. Any items behind the dropped head stay queued — draining == false with
// a non-empty backlog is exactly maybeSpawnDrainLocked's respawn precondition,
// so the next Enqueue respawns the drain, the same lifecycle as the empty-exit
// path. The caller must hold NO lock. reason is built only from daemon-generated
// values (the elapsed window and the delivery err); it NEVER carries head.text.
func (q *Queue) giveUp(convID string, c *convQueue, head queued, elapsed time.Duration, err error) {
	elapsed = elapsed.Round(time.Second)
	reason := fmt.Sprintf("delivery failed persistently for %s; claude session may be wedged (last error: %v)", elapsed, err)
	q.log.Warn("msgqueue: giving up on head after persistent delivery failure",
		"conversation_id", convID,
		"queued_msg_id", head.id,
		"elapsed", elapsed,
		"err", err)

	// Drop the abandoned head and clear draining under q.mu. Clearing draining
	// BEFORE firing the seams means a give-up observer can safely re-Enqueue
	// without racing a still-true draining flag.
	q.mu.Lock()
	c.advanceLocked()
	c.draining = false
	q.mu.Unlock()

	// notify fires because the backlog shrank (the dropped head), keeping the
	// wired queue_state view correct; notifyGiveUp carries the give-up event to
	// its (currently nil) consumer. Both fire strictly after q.mu is released.
	q.notify(convID)
	q.notifyGiveUp(convID, reason)
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
