package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turncommit"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// replySuggestions owns each conversation's native suggested next reply (#2831):
// whether the turn that just ended may publish one, the text it published, and
// the per-conversation revision every set and clear advances. Daemon memory
// only; a fresh handshake discards a client's cached suggestions.
//
// Writers are the stream drain (turn start, main-agent text, TurnEnd, the
// suggestion, exit and teardown closes), the relay's accept paths, the msgqueue
// delivered hook and the pool transition observer. The relay Run goroutine reads
// it through current. Everything goes through one leaf mutex that is never held
// across a push: a changed conversation is marked dirty and Run, this owner's own
// goroutine, sends its CURRENT state. Coalescing to current state is what lets a
// wake never lose a clear, and the relay's per-conn revision guard drops a frame
// a newer revision has overtaken.
//
// SECURITY: the text is claude-authored and untrusted. It is never logged.
type replySuggestions struct {
	mu    sync.Mutex
	convs map[string]*replySuggestionConv
	dirty map[string]struct{}
	wake  chan struct{}

	// bcast is assigned by startReplySuggestionsV2 before Run starts, and
	// sessionFor by bindSessions under mu. sessionFor answers a conversation's
	// current session id, and false once the conversation is gone. It is a
	// registry read, so it is always called with mu released.
	bcast      interactiveBroadcaster
	sessionFor func(convID string) (sessionID string, ok bool)
	logger     *slog.Logger
	nextID     uint64 // Run-goroutine only
	fallback   func(context.Context, string, string) (string, error)
	waitNative func(context.Context) bool
	workers    sync.WaitGroup
	stopped    bool
	parent     context.Context
}

// replySuggestionConv is one conversation's suggestion state.
type replySuggestionConv struct {
	revision  uint64
	published bool    // a set or clear has been sent, so the state is real
	text      *string // nil is the clear
	sessionID string

	// Queue ids are monotone within a conversation. The commit gate identifies
	// the producing message before stdout can open its turn; OnDelivered may
	// confirm it after the result. A newer accept never gets erased by either.
	writeID, acceptedID                uint64
	awaitingStart                      bool
	assistant, invalidated             bool
	ended, turnOK, userOK              bool
	pending                            *string
	pendingSID                         string
	userText, assistantText, messageID string
	assistantFull                      bool
	generation                         uint64
	cancel                             context.CancelFunc
	attempt, windowDone                bool
	turnSID                            string
}

func newReplySuggestions(logger *slog.Logger) *replySuggestions {
	return &replySuggestions{
		convs:  make(map[string]*replySuggestionConv),
		dirty:  make(map[string]struct{}),
		wake:   make(chan struct{}, 1),
		logger: logger,
		parent: context.Background(),
	}
}

// bindSessions installs the conversation-to-session resolver. startRelayV2
// calls it once, before the drain or the relay can read the owner.
func (s *replySuggestions) bindSessions(fn func(convID string) (string, bool)) {
	s.mu.Lock()
	s.sessionFor = fn
	s.mu.Unlock()
}

// entry returns convID's state, creating it when create is set. Caller holds mu.
func (s *replySuggestions) entry(convID string, create bool) *replySuggestionConv {
	c := s.convs[convID]
	if c == nil && create && convID != "" {
		c = &replySuggestionConv{}
		s.convs[convID] = c
	}
	return c
}

// trackDelivery observes the existing turncommit gate, after idle and before
// the write. Metadata is the safe queued projection, never composed host paths.
// Failed/dropped attempts cannot credit user text. The gate and inner delivery
// run outside the leaf mutex. Nil owners preserve the delivery unchanged.
func (s *replySuggestions) trackDelivery(inner msgqueue.DeliverFunc) msgqueue.DeliverFunc {
	if s == nil {
		return inner
	}
	return func(ctx context.Context, convID string, payload []byte) error {
		msg, ok := msgqueue.DeliveryMessage(ctx)
		gate := turncommit.From(ctx)
		if !ok || gate == nil {
			return inner(ctx, convID, payload)
		}
		started := false
		ctx = turncommit.With(ctx, func() bool {
			if !gate() {
				return false
			}
			s.beginWrite(convID, msg.ID)
			started = true
			return true
		})
		err := inner(ctx, convID, payload)
		if err != nil && started {
			s.invalidateWrite(convID, msg.ID)
		}
		return err
	}
}

// beginWrite establishes turn identity before its first content event. An
// accept for a newer id still invalidates it even when it raced the queue's
// return to the relay adapter. Only a fresh write can re-arm eligibility.
func (s *replySuggestions) beginWrite(convID string, id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.entry(convID, true)
	if c == nil {
		return
	}
	s.resetFallbackLocked(c)
	c.writeID, c.awaitingStart = id, true
	c.assistant, c.ended, c.turnOK, c.userOK, c.pending = false, false, false, false, nil
	c.invalidated = c.acceptedID > id
	s.clearLocked(convID, c)
}

// noteDelivered credits only the producing message. It never resets an
// invalidation, and send-now joins an invalidated running turn.
func (s *replySuggestions) noteDelivered(convID string, msg msgqueue.QueuedMessage) {
	if s == nil || msg.SentNow {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.entry(convID, false)
	if c == nil || c.writeID == 0 || c.writeID != msg.ID {
		return
	}
	c.userText = replyExchangePrefix(msg.Text)
	c.userOK = strings.TrimSpace(msg.Text) != ""
	if c.pending != nil && c.turnOK && c.userOK && !c.invalidated {
		s.setLocked(convID, c, *c.pending, c.pendingSID)
	}
	s.startFallbackLocked(convID, c)
}

// turnStarted keeps the identity and invalidations already recorded at the
// write gate. A spontaneous turn has no producing queued message to credit.
func (s *replySuggestions) turnStarted(convID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.entry(convID, true)
	if c == nil {
		return
	}
	if c.awaitingStart {
		c.awaitingStart = false
	} else {
		s.resetFallbackLocked(c)
		c.writeID = 0
		c.assistant, c.ended, c.turnOK, c.userOK, c.pending = false, false, false, false, nil
	}
	s.clearLocked(convID, c)
}

// noteAssistantText records main-agent prose in convID's open turn. Nil-safe.
func (s *replySuggestions) noteAssistantText(convID string, ev turnevent.TextChunk) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if c := s.entry(convID, false); c != nil {
		if ev.ParentToolCallID != "" || c.ended {
			s.mu.Unlock()
			return
		}
		if c.messageID != ev.MessageID {
			c.messageID, c.assistantText, c.assistantFull = ev.MessageID, "", false
			c.assistant = false
		}
		if !c.assistantFull {
			combined := c.assistantText + ev.Text
			c.assistantText = replyExchangePrefix(combined)
			c.assistantFull = len(combined) > replyExchangeBytes
		}
		// Eligibility considers all chunks, even after bounded retention fills.
		c.assistant = c.assistant || strings.TrimSpace(ev.Text) != ""
	}
	s.mu.Unlock()
}

// turnEnded decides whether convID's just-ended turn may publish a suggestion,
// using the identity recorded before the write. Nil-safe.
func (s *replySuggestions) turnEnded(convID string, ev turnevent.TurnEnd) {
	if s == nil {
		return
	}
	sessionID := ""
	if fn := s.resolver(); fn != nil {
		sessionID, _ = fn(convID)
	}
	success := ev.Reason == turnevent.TurnEndReasonEndTurn && ev.Outcome == "success" && !ev.IsError
	s.mu.Lock()
	if c := s.entry(convID, false); c != nil {
		if c.ended {
			s.mu.Unlock()
			return
		}
		c.ended = true
		c.turnOK = success && c.assistant && c.writeID != 0 && !c.invalidated
		c.turnSID = sessionID
		if c.turnOK && s.fallback != nil && !s.stopped {
			ctx, cancel := context.WithCancel(s.parent)
			c.cancel = cancel
			generation := c.generation
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				wait := s.waitNative
				if wait == nil {
					wait = waitReplyNative
				}
				if !wait(ctx) {
					return
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if c.generation != generation || ctx.Err() != nil || s.stopped {
					return
				}
				c.windowDone = true
				s.startFallbackLocked(convID, c)
			}()
		}
	}
	s.mu.Unlock()
}

// suggest publishes text for convID when its just-ended turn qualifies, holds it
// when only the delivery confirmation is missing, and does nothing otherwise.
// One suggestion per turn. Nil-safe.
func (s *replySuggestions) suggest(convID, text string) {
	if s == nil {
		return
	}
	sessionID := ""
	if fn := s.resolver(); fn != nil {
		sessionID, _ = fn(convID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.entry(convID, false)
	if c == nil || !c.ended || !c.turnOK || c.invalidated || c.pending != nil {
		return
	}
	if !c.userOK {
		// Only this turn's own confirmation may release the pending text.
		c.pending, c.pendingSID = &text, sessionID
		return
	}
	s.setLocked(convID, c, text, sessionID)
}

// setLocked publishes text and closes the turn to further suggestions. Caller
// holds mu.
func (s *replySuggestions) setLocked(convID string, c *replySuggestionConv, text, sessionID string) {
	if s.stopped {
		return
	}
	s.cancelLocked(c)
	c.turnOK, c.pending = false, nil
	c.sessionID = sessionID
	c.text = &text
	s.publishLocked(convID, c)
}

// invalidate drops convID's current turn so a later suggestion for it is
// ignored, and clears a held suggestion. Send-now, /clear,
// resets, evictions and exits all land here. A conversation this owner has
// never seen has nothing to invalidate. Nil-safe and safe from any goroutine.
func (s *replySuggestions) invalidate(convID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.entry(convID, false)
	if c == nil {
		return
	}
	s.invalidateLocked(convID, c)
}

// accepted records the accepted queue id even if its write raced EnqueueSent's
// return. An own-message accept must not invalidate the turn it already opened.
func (s *replySuggestions) accepted(convID string, id uint64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.entry(convID, true)
	if c == nil {
		return
	}
	c.acceptedID = max(c.acceptedID, id)
	if id > c.writeID {
		s.invalidateLocked(convID, c)
	}
}

func (s *replySuggestions) invalidateWrite(convID string, id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.entry(convID, false); c != nil && c.writeID == id {
		s.invalidateLocked(convID, c)
	}
}

func (s *replySuggestions) invalidateLocked(convID string, c *replySuggestionConv) {
	s.resetFallbackLocked(c)
	c.invalidated, c.turnOK, c.pending = true, false, nil
	s.clearLocked(convID, c)
}

// clearLocked publishes the null for a conversation holding text. Caller holds mu.
func (s *replySuggestions) clearLocked(convID string, c *replySuggestionConv) {
	if c.text == nil {
		return
	}
	c.text = nil
	s.publishLocked(convID, c)
}

// publishLocked advances the revision and wakes Run. Caller holds mu.
func (s *replySuggestions) publishLocked(convID string, c *replySuggestionConv) {
	c.revision++
	c.published = true
	s.dirty[convID] = struct{}{}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// payloadLocked is c's current state as a wire payload. Caller holds mu.
func payloadLocked(convID string, c *replySuggestionConv) protocol.ReplySuggestionPayload {
	p := protocol.ReplySuggestionPayload{ConversationID: convID, SessionID: c.sessionID, Revision: c.revision}
	if c.text != nil {
		text := *c.text
		p.SuggestedReply = &text
	}
	return p
}

// current is the V2SessionConfig.ReplySuggestions seam: one payload per
// conversation with published state, the clear included, ordered by
// conversation id. A deleted conversation is forgotten here. It runs on the
// relay Run goroutine, so it takes only the leaf mutex and, with it released,
// the registry read behind sessionFor; it never calls ActiveConns.
func (s *replySuggestions) current() []protocol.ReplySuggestionPayload {
	s.mu.Lock()
	out := make([]protocol.ReplySuggestionPayload, 0, len(s.convs))
	ids := make([]string, 0, len(s.convs))
	for convID, c := range s.convs {
		ids = append(ids, convID)
		if c.published {
			out = append(out, payloadLocked(convID, c))
		}
	}
	s.mu.Unlock()
	if fn := s.resolver(); fn != nil {
		for _, id := range ids {
			if _, ok := fn(id); !ok {
				s.forget(id)
			}
		}
		out = slices.DeleteFunc(out, func(p protocol.ReplySuggestionPayload) bool {
			if _, ok := fn(p.ConversationID); ok {
				return false
			}
			s.forget(p.ConversationID)
			return true
		})
	}
	if len(out) == 0 {
		return nil
	}
	slices.SortFunc(out, func(a, b protocol.ReplySuggestionPayload) int {
		return strings.Compare(a.ConversationID, b.ConversationID)
	})
	return out
}

// resolver returns the bound session resolver.
func (s *replySuggestions) resolver() func(string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionFor
}

// forget drops a deleted conversation's state.
func (s *replySuggestions) forget(convID string) {
	s.mu.Lock()
	if c := s.convs[convID]; c != nil {
		s.resetFallbackLocked(c)
	}
	delete(s.convs, convID)
	delete(s.dirty, convID)
	s.mu.Unlock()
}

// takeDirty snapshots the current payload of every changed conversation.
func (s *replySuggestions) takeDirty() []protocol.ReplySuggestionPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]protocol.ReplySuggestionPayload, 0, len(s.dirty))
	for convID := range s.dirty {
		delete(s.dirty, convID)
		if c := s.convs[convID]; c != nil {
			out = append(out, payloadLocked(convID, c))
		}
	}
	return out
}

// Run sends each changed conversation's current state to every interactive
// conn until ctx ends. The frame carries no EventID: suggestion state is
// control state, never part of the replay ring or the history.
func (s *replySuggestions) Run(ctx context.Context) {
	defer s.stopFallbacks()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		pending := s.takeDirty()
		if len(pending) == 0 {
			continue
		}
		conns := s.bcast.ActiveConns(ctx)
		for _, p := range pending {
			payload, err := json.Marshal(p)
			if err != nil {
				// Strings, a uint64 and a *string cannot fail. Never echo err.
				s.logger.Debug("relay: reply_suggestion marshal failed",
					"event", "reply_suggestion.marshal_err",
					"conversation_id", p.ConversationID)
				continue
			}
			ts := time.Now().UTC()
			for _, c := range conns {
				if !c.Interactive {
					continue
				}
				s.nextID++
				env := protocol.Envelope{ID: s.nextID, Type: protocol.TypeReplySuggestion, TS: ts, Payload: payload}
				if err := s.bcast.Push(ctx, c.ConnID, env); err != nil {
					if ctx.Err() != nil {
						return
					}
					s.logger.Debug("relay: reply_suggestion push dropped",
						"event", "reply_suggestion.push_err",
						"conn_id", c.ConnID,
						"conversation_id", p.ConversationID,
						"err", err)
				}
			}
		}
	}
}

// startReplySuggestionsV2 binds bcast and starts Run, returning a cleanup that
// waits for it after ctx is cancelled. A nil owner starts nothing.
func startReplySuggestionsV2(ctx context.Context, s *replySuggestions, bcast interactiveBroadcaster) func() {
	if s == nil {
		return func() {}
	}
	s.bcast = bcast
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	return func() { <-done }
}

// suggestionEnqueuer invalidates a conversation's suggestion when a client's
// send_message is accepted into its queue; a rejected send (id 0) changes
// nothing.
type suggestionEnqueuer struct {
	inner handlers.Enqueuer
	s     *replySuggestions
}

func (q suggestionEnqueuer) EnqueueSent(conversationID, messageID, text, delivery string, attachmentIDs []string, deviceName, clientVersion string, clientSentAt time.Time) uint64 {
	id := q.inner.EnqueueSent(conversationID, messageID, text, delivery, attachmentIDs, deviceName, clientVersion, clientSentAt)
	if id != 0 {
		q.s.accepted(conversationID, id)
	}
	return id
}

// suggestionQueueSender invalidates on an accepted send-now.
type suggestionQueueSender struct {
	inner relay.QueueSender
	s     *replySuggestions
}

func (q suggestionQueueSender) SendNow(conversationID string, queuedMsgID uint64) bool {
	ok := q.inner.SendNow(conversationID, queuedMsgID)
	if ok {
		q.s.invalidate(conversationID)
	}
	return ok
}

// suggestionResetter invalidates when a client's "/clear" reaches the
// conversation reset succeeds. A refused reset preserves suggestion state.
type suggestionResetter struct {
	inner handlers.ConversationResetter
	s     *replySuggestions
}

func (r suggestionResetter) StartNewSession(conversationID string) error {
	if err := r.inner.StartNewSession(conversationID); err != nil {
		return err
	}
	r.s.invalidate(conversationID)
	return nil
}

// suggestionTransitionSink composes the suggestion invalidation onto the pool's
// single-valued transition observer: a reset or eviction invalidates the
// conversation the transition tears down, after the incumbent observer ran.
type suggestionTransitionSink struct {
	inner   transitionObserverSink
	s       *replySuggestions
	resolve func(sessionID string) (string, bool)
}

func (k suggestionTransitionSink) SetTransitionObserver(fn sessions.TransitionObserver) {
	k.inner.SetTransitionObserver(func(t sessions.SessionTransition) {
		fn(t)
		if sid, ok := transitionClearsTurn(t); ok {
			if convID, ok := k.resolve(sid); ok {
				k.s.invalidate(convID)
			}
		}
	})
}

func waitReplyNative(ctx context.Context) bool {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *replySuggestions) cancelLocked(c *replySuggestionConv) {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

func (s *replySuggestions) resetFallbackLocked(c *replySuggestionConv) {
	s.cancelLocked(c)
	c.generation++
	c.userText, c.assistantText, c.messageID = "", "", ""
	c.assistantFull, c.attempt, c.windowDone = false, false, false
}

func (s *replySuggestions) startFallbackLocked(convID string, c *replySuggestionConv) {
	if s.fallback == nil || s.stopped || !c.windowDone || c.attempt || !c.turnOK || !c.userOK || c.invalidated || c.pending != nil {
		return
	}
	c.attempt = true
	ctx, cancel := context.WithCancel(s.parent)
	s.cancelLocked(c)
	c.cancel = cancel
	generation, user, assistant, sid := c.generation, c.userText, c.assistantText, c.turnSID
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer cancel()
		text, err := s.fallback(ctx, user, assistant)
		if err != nil || ctx.Err() != nil {
			return
		}
		if fn := s.resolver(); fn != nil {
			current, ok := fn(convID)
			if !ok || current != sid {
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.stopped || s.convs[convID] != c || c.generation != generation || !c.turnOK || c.invalidated || ctx.Err() != nil {
			return
		}
		s.setLocked(convID, c, text, sid)
	}()
}

func (s *replySuggestions) stopFallbacks() {
	s.mu.Lock()
	s.stopped = true
	for _, c := range s.convs {
		s.cancelLocked(c)
	}
	s.mu.Unlock()
	s.workers.Wait()
}
