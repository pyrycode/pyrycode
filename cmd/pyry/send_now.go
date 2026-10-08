package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// errSendNowNotClaude refuses a send-now write to a session that is not Claude's:
// Codex's write path starts a new turn rather than feeding the running one.
var errSendNowNotClaude = errors.New("send-now: session does not take input mid-turn")

// errSendNowIdle refuses a send-now write when the conversation's turn is idle:
// the ordinary drain delivers the message, in FIFO order.
var errSendNowIdle = errors.New("send-now: turn is idle")

// newSendNowDeliver builds the msgqueue.SendNowFunc seam (#2729): it writes a
// queued message's payload into the conversation's running claude turn.
//
// It differs from newInboundDeliver in exactly the places the two jobs differ.
// It never waits for idle — an idle turn is a refusal, since the drain owns that
// case. It never Activates: a running turn has a live child, and a send-now that
// found none must fail and leave the message queued, not spawn one. It refuses a
// non-Claude session before resolving anything. And it is NOT wrapped in
// channelCarry.carryPending or markApprovalHolds: pending channel posts ride the
// waiting head's own delivery, and a send-now write is never a hold.
//
// The tracker's openForSendNow is the busy check, atomic with the carry that
// keeps the conversation busy through a turn end the write may outlive; its undo
// runs when the write fails. The write's own error is returned verbatim, and
// msgqueue puts the message back where it was.
//
// Before the write it registers the payload with place (#2730), so claude's echo
// of it — which can arrive before msgqueue fires OnDelivered — finds its entry; a
// failed write takes the registration back. A nil place registers nothing.
//
// Nothing here logs. msgqueue.SendNow's caller logs content-free fields only.
func newSendNowDeliver(resolve func(string) (handlers.TurnWriter, error), isClaude func(string) bool, busy *turnBusyTracker, place *sendNowPlacement) msgqueue.SendNowFunc {
	return func(ctx context.Context, convID string, id uint64, payload []byte) error {
		if !isClaude(convID) {
			return errSendNowNotClaude
		}
		// The busy check comes before resolve, so an idle conversation never
		// reaches resolve's revive of a dropped session.
		ok, undo, finished := busy.beginSendNow(convID)
		defer finished()
		if !ok {
			return errSendNowIdle
		}
		w, err := resolve(convID)
		if err != nil {
			undo()
			return err
		}
		if _, ok := msgqueue.DeliveryMessage(ctx); ok && place != nil && place.record != nil {
			err := writeOperatorTurn(ctx, convID, payload, w, place, true)
			if err != nil {
				undo()
			}
			return err
		}
		cancel := place.expect(convID, id, payload)
		if err := w.WriteUserTurn(ctx, convID, payload); err != nil {
			cancel()
			undo()
			return err
		}
		return nil
	}
}

// sendNowPlacement places queued Claude messages at digest-only user echoes.
// Queue-backed writes prepare their safe commit before writing, so the drain
// commits at the echo independently of OnDelivered. Ordinary entries without
// echoes commit at the answering turn's idle boundary; send-now entries retain
// the carried-close grace fallback. See write and idle in queued_message_placement.go.
//
// Legacy callers without queue metadata use expect/attach: whichever of the
// echo and attach comes second commits. A nil placement commits at attach.
// Matching is conversation-scoped and follows registration/write order.
type sendNowPlacement struct {
	ctx context.Context
	// resolve maps the echo's producing session to its conversation, daemon-side;
	// the conversation is never read from the line.
	resolve  func(sessionID string) (conversationID string, ok bool)
	waitIdle func(ctx context.Context, conversationID string) error

	record   func(string, msgqueue.QueuedMessage, ...history.SessionProvenance)
	dispatch func(func())
	isClaude func(string) bool
	managed  map[string]map[uint64]*placedMessage
	writes   map[string]*placementWriteLock
	mu       sync.Mutex
	pending  map[string][]*placedMessage
	sources  map[string]map[uint64]history.SessionProvenance
}

type placedMessage struct {
	id       uint64
	digest   [sha256.Size]byte
	echoed   bool
	commit   func()
	queued   bool
	sentNow  bool
	outcome  chan struct{}
	writeErr error
	notified bool
	placed   bool
	source   history.SessionProvenance
}

func newSendNowPlacement(ctx context.Context, resolve func(string) (string, bool), waitIdle func(context.Context, string) error) *sendNowPlacement {
	return &sendNowPlacement{ctx: ctx, resolve: resolve, waitIdle: waitIdle, pending: make(map[string][]*placedMessage)}
}

// expect registers a send-now write about to happen and returns its undo, for a
// write that failed.
func (p *sendNowPlacement) expect(convID string, id uint64, payload []byte) (cancel func()) {
	if p == nil {
		return func() {}
	}
	e := &placedMessage{id: id, digest: sha256.Sum256(payload)}
	p.mu.Lock()
	p.pending[convID] = append(p.pending[convID], e)
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		p.removeLocked(convID, e)
		p.mu.Unlock()
	}
}

// attach hands over message id's commit. It commits at once when the echo has
// already arrived or nothing was registered; otherwise it holds the commit for the
// echo and starts the idle waiter.
func (p *sendNowPlacement) attach(convID string, id uint64, commit func()) {
	if p == nil {
		commit()
		return
	}
	p.mu.Lock()
	if e := p.managed[convID][id]; e != nil {
		e.notified = true
		if e.placed {
			p.forgetLocked(convID, id)
		}
		p.mu.Unlock()
		return
	}
	var e *placedMessage
	for _, m := range p.pending[convID] {
		if m.id == id && m.commit == nil {
			e = m
			break
		}
	}
	if e == nil || e.echoed {
		if e != nil {
			p.removeLocked(convID, e)
		}
		p.mu.Unlock()
		commit()
		return
	}
	e.commit = commit
	p.mu.Unlock()
	go func() {
		// Exits on idle or daemon shutdown, and commits on either: a held message
		// is still the operator's turn, and the entry may already be taken.
		_ = p.waitIdle(p.ctx, convID)
		p.take(convID, e)
	}()
}

// echo applies one claude echo produced by sessionID. Runs on the stream drain
// goroutine, after the drain has handled every event claude emitted before it.
func (p *sendNowPlacement) echo(sessionID string, ev turnevent.UserEcho) {
	if p == nil {
		return
	}
	convID, ok := p.resolve(sessionID)
	if !ok {
		return
	}
	p.mu.Lock()
	for _, e := range p.pending[convID] {
		if e.echoed || e.digest != ev.TextSHA256 {
			continue
		}
		if e.queued {
			p.mu.Unlock()
			p.takeQueued(convID, e)
			return
		}
		if e.commit == nil {
			e.echoed = true
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		p.take(convID, e)
		return
	}
	p.mu.Unlock()
}

// take commits e if it is still pending; a second taker finds it gone.
// Queue-backed entries retain only callback bookkeeping until OnDelivered.
func (p *sendNowPlacement) take(convID string, e *placedMessage) {
	p.mu.Lock()
	ok := p.removeLocked(convID, e)
	if ok && e.queued {
		e.placed = true
		if e.notified {
			p.forgetLocked(convID, e.id)
		}
	}
	p.mu.Unlock()
	if ok && e.commit != nil {
		e.commit()
	}
}

func (p *sendNowPlacement) removeLocked(convID string, e *placedMessage) bool {
	list := p.pending[convID]
	i := slices.Index(list, e)
	if i < 0 {
		return false
	}
	list = slices.Delete(list, i, i+1)
	if len(list) == 0 {
		delete(p.pending, convID)
	} else {
		p.pending[convID] = list
	}
	return true
}

// isClaude reports whether conversationID is bound to a Claude session. An
// unknown or unbound conversation, or a session the pool does not know, is
// false. It reads the same binding resolve does, without resolving a writer.
func (r sessionRouter) isClaude(conversationID string) bool {
	conv, ok := r.convReg.Get(conversations.ConversationID(conversationID))
	if !ok || conv.CurrentSessionID == "" {
		return false
	}
	harness, err := r.pool.HarnessFor(sessions.SessionID(conv.CurrentSessionID))
	return err == nil && harness == sessions.HarnessClaude
}

// sendNowGrace bounds how long a turn end that follows a send-now write keeps the
// conversation busy, waiting for the second turn the write may have opened (the
// #2728 capture's no-tool and after-last-tool arms). When claude folded the write
// into the turn that just ended, no opener comes and the next queued message
// waits this long; when the write opened a second turn whose first event takes
// longer than this to stream, the drain can see a brief idle that is not real. A
// tuning knob, not a contract: claude streams a turn's first event within a few
// seconds of reading its input.
const sendNowGrace = 10 * time.Second

// scheduleCarryRelease releases the tracker's carried close of generation gen
// once after has passed. It lives here rather than in stream_turn_busy.go, which
// reads no clock (TestTurnBusyTracker_ImportsStayMinimal): the tracker owns the
// generation, this feature owns the bound. The timer fires once and holds no
// resource, so it needs no shutdown path; a release for a retired generation is
// a no-op.
func scheduleCarryRelease(t *turnBusyTracker, conversationID string, gen uint64, after time.Duration) {
	time.AfterFunc(after, func() { t.releaseCarried(conversationID, gen) })
}
