package main

import (
	"context"
	"strings"
	"sync"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// wrapUpCapture accumulates one turn's assistant text so the reset routine can
// write it as the conversation's handoff note (#2477). One per runner, chained by
// newStreamRunnerFactory.
//
// # Why it sits where it sits
//
// It is a sink DECORATOR on the parser's side of the fan-in channel, beside
// newSessionResetFollower and modelVocabularyStore.sinkFor. That placement is the
// correctness condition rather than a convention, and session_reset_follow.go's
// doc block states the general rule this is the second instance of. Two gates sit
// between a TextChunk and anything downstream, and a capture has to clear BOTH:
//
//   - the ACTIVE-SESSION GATE inside startStreamTurnDrainV2, which drops every
//     event whose producing session is not the active conversation's. A reset
//     names background conversations at least as often as the foreground one — an
//     operator resetting a chat is not thereby looking at it — so a capture behind
//     that gate would collect nothing for the common case.
//   - the DROPPABLE FAN-IN SEND inside streamTurnSink.sinkForTag, which refuses a
//     droppable event at droppableCap and records the loss at Debug. turnMarkFor
//     answers turnMarkOpen for TextChunk, so assistant text is droppable class.
//
// turnBusyTracker.observe clears the first and sits BELOW the second: it reads
// envelopes already received from the fan-in channel. A capture there loses
// assistant text under load and writes a silently TRUNCATED note — worse than no
// note, because nothing downstream can tell a truncated handoff from a short one.
// TestWrapUpCapture_AccumulatesPastASaturatedSink is the pin.
//
// # Why it is not a fifth sessionRetentions member
//
// It retains nothing across turns and holds no answer anyone may ask for: it is
// armed for exactly one turn and hands that turn's text to the one caller that
// armed it. Minting it inside newSessionParser would also gain nothing — unlike
// the two decorators beside it, it needs no per-runner object that function lacks
// — while spending the call sites #2106 paid to make a fifth hold free.
//
// # Why the capture does not live on turnBusyTracker
//
// That type's doc block says it holds membership and one ordering token, "never
// the event, its content". Accumulating assistant text into it falsifies exactly
// that sentence, which is a worse cost than a new type.
//
// SECURITY: the accumulated text is claude-authored and crosses the subprocess
// boundary. This type judges its SIZE and nothing else — no trimming, no repair,
// no filtering — because what may appear in a composed prompt belongs to the
// composing site, which is Pool.HandoffNote's stated division and the one #2475
// implements. It holds no logger and takes none, so no fragment can reach a log
// line here by construction rather than by discipline.
type wrapUpCapture struct {
	// next is the downstream sink every event is forwarded to, unchanged. nil
	// forwards nothing — a test convenience; production always supplies the next
	// link in newStreamRunnerFactory's chain.
	next func(turnevent.Event)

	// mu guards active alone and is a LEAF: Sink forwards to next outside it, so
	// no call-out ever runs under it. It is taken from two goroutines — the
	// runner's stdout forwarder (Sink) and the reset goroutine (begin / stop).
	mu     sync.Mutex
	active *wrapUpReply
}

// newWrapUpCapture mints a capture that forwards to next.
func newWrapUpCapture(next func(turnevent.Event)) *wrapUpCapture {
	return &wrapUpCapture{next: next}
}

// Sink is the decorator, used as a method value: capture.Sink is a
// func(turnevent.Event) of exactly the shape streamsup.NewParser takes.
//
// It accumulates while armed and then forwards EVERY event of EVERY variant
// unchanged — the contract each link in this chain states, and here the TurnEnd
// arm is the one that makes it load-bearing rather than tidy: turnMarkClose is the
// only thing that clears a conversation's busy mark, so a TurnEnd swallowed here
// would wedge the conversation for the rest of the session.
//
// A nil receiver forwards nothing and captures nothing, which keeps a hand-built
// streamRunner{} inert.
func (c *wrapUpCapture) Sink(ev turnevent.Event) {
	if c == nil {
		return
	}
	c.observe(ev)
	if c.next != nil {
		c.next(ev)
	}
}

// observe is the capture half, split out so the forward above is unconditional by
// shape rather than by every arm remembering to fall through.
//
// ONLY turnevent.TextChunk contributes, which is AC 1's enumeration read
// literally: the text chunks, not the thought chunks and not the tool events. A
// note carrying the outgoing session's reasoning, or a tool's output, is not what
// the successor is owed. TextChunk.ParentToolCallID is deliberately NOT consulted:
// the wrap-up prompt forbids tool calls, so a subagent's text here would be a
// prompt violation nobody has observed, and a branch defending it would be a
// defence with no evidence behind it.
//
// TurnEnd finishes the armed reply and disarms, so the next reset finds the
// capture free. It is the only finish this path has — a child that never produces
// one is the deadline's problem, not this type's.
func (c *wrapUpCapture) observe(ev turnevent.Event) {
	c.mu.Lock()
	reply := c.active
	if reply == nil {
		c.mu.Unlock()
		return
	}
	if _, ends := ev.(turnevent.TurnEnd); ends {
		c.active = nil
	}
	c.mu.Unlock()

	switch e := ev.(type) {
	case turnevent.TextChunk:
		reply.append(e.Text)
	case turnevent.TurnEnd:
		reply.finish()
	}
}

// begin arms a capture for the next turn and returns the reply, the disarm the
// caller must run, and whether it armed at all.
//
// It refuses a SECOND concurrent arm, and that refusal is the runner-level half of
// AC 4's "one reset at a time" — conversationReset.begin is the conversation-level
// half, and neither subsumes the other: the guard above is keyed by conversation
// while a runner is keyed by session, and a rebinding moves one without the other.
// Admitting two would split one turn's text between them and write two half-notes.
//
// A nil receiver refuses, which is what keeps a hand-built streamRunner{} inert
// rather than crashing on a remotely-driven path.
//
// The returned stop is IDEMPOTENT and disarms whether or not the turn ended, so
// the reset goroutine can defer it unconditionally over a body whose ordinary exit
// is the turn end having already disarmed. It never finishes the reply: a disarm
// is "stop listening", not "the turn ended", and a wait racing it must answer
// "no reply" rather than "an empty one".
func (c *wrapUpCapture) begin() (*wrapUpReply, func(), bool) {
	if c == nil {
		return nil, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return nil, nil, false
	}
	reply := &wrapUpReply{done: make(chan struct{})}
	c.active = reply
	return reply, func() { c.release(reply) }, true
}

// release disarms iff the capture is still armed with reply. The condition is what
// keeps a late stop from disarming a SUCCESSOR's capture: the turn-end path may
// have disarmed already and another reset may have armed in between, and an
// unconditional clear would silently steal that reset's text.
func (c *wrapUpCapture) release(reply *wrapUpReply) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == reply {
		c.active = nil
	}
}

// wrapUpReply is one armed turn's accumulated assistant text.
//
// done IS THE MEMORY BARRIER, contextUsageFlight's discipline: text is written by
// the runner's stdout forwarder goroutine and read by the reset goroutine, and
// every reader gates on observing the close first. The mutex covers the
// accumulation itself, which is many writes rather than one.
type wrapUpReply struct {
	done chan struct{}

	mu   sync.Mutex
	text strings.Builder
	// ended records that finish already ran, so a second TurnEnd — or a finish
	// racing one — closes done exactly once.
	ended bool
}

// append adds one chunk, BOUNDED at sessions.MaxHandoffNoteBytes.
//
// The bound is the store's own, named rather than re-derived, because the store
// truncates to it anyway: buffering past it buys the successor nothing and would
// let a child that never stops writing grow the daemon's heap as far as it likes.
// Past the bound the TAIL is dropped and the note stands, which is
// MaxHandoffNoteBytes' own direction — "the head of a long note is still a usable
// handoff" — and the opposite of refusing the reply.
//
// The cut is a byte cut and may split a rune. That is deliberate and not an
// oversight: truncateHandoffNote walks a cut back to a rune start on the way to
// disk, so the repair belongs there and doing it twice would put a second opinion
// in a second place.
func (r *wrapUpReply) append(chunk string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room := sessions.MaxHandoffNoteBytes - r.text.Len()
	if room <= 0 {
		return
	}
	if len(chunk) > room {
		chunk = chunk[:room]
	}
	r.text.WriteString(chunk)
}

// finish closes the reply to further accumulation and releases every waiter. Idempotent.
func (r *wrapUpReply) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	r.ended = true
	close(r.done)
}

// wait blocks until the turn ends or ctx is done, answering the accumulated text
// and whether the turn actually ended.
//
// The bool is not "is the text non-empty": a turn that ended having produced no
// text answers ("", true), and the caller treats that exactly as it treats a
// blank reply. What the bool separates is a reply from a DEADLINE, which the
// caller records differently even though both leave the previous note standing.
//
// A departing caller takes itself out of the wait and nothing else — it does not
// disarm the capture, because the disarm is the caller's deferred stop and running
// it from here would make the two orderings disagree.
func (r *wrapUpReply) wait(ctx context.Context) (string, bool) {
	select {
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.text.String(), true
	case <-ctx.Done():
		return "", false
	}
}
