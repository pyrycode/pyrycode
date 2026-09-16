package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
)

// channelCarryHeader introduces the daemon-authored block naming what was posted
// into a channel since claude's last turn. It is placed BEFORE the operator's own
// text, which is the one place this departs from #2038's attachment block: that one
// appends, because an instruction should be the most recent thing claude reads,
// while this one prepends, because the ticket asks for the order the two things
// actually happened in — the question, then the answer.
//
// SECURITY: this line is the whole of the design's answer to the trust boundary
// this file opens. A post is caller-authored text that now enters claude's INPUT
// rather than only the durable log and the wire, so the block has to carry its own
// provenance — claude reads it as quoted material that arrived from somewhere else,
// not as the operator speaking. The posted text itself is carried VERBATIM, never
// quoted or escaped, for composeAttachmentPrompt's recorded reason: quoting mangles
// a text containing the quote character and buys nothing against a caller who
// controls all the bytes anyway. A post that contains this line can spoof the
// boundary inside the prompt and gains nothing by it — both halves are one prompt
// from one principal, and the control socket is host-local to a user who can run
// claude directly.
const channelCarryHeader = "Messages posted into this channel since your last turn (the operator's own reply follows them):"

// channelCarry is the #2499 carry-forward: text posted into a channel reaches
// claude on the next user turn the daemon delivers for that conversation.
//
// It is contextUsageRecorder's shape — a registry-writing recorder that is inert
// without one, saves best-effort, and logs nothing but a failure — because it does
// the same job for a different field. The conversations registry is already the
// daemon's durable per-conversation store and SetLastContextUsage is the shipped
// precedent for a daemon-written field that persists there, so this needs no store
// of its own and no migration step.
//
// THREE SEAMS, AND THE SPLIT BETWEEN THEM IS THE DESIGN.
//
//   - record is the channel.post hook: the posted text becomes durable pending
//     state.
//   - carryPending decorates the queue's delivery seam, composing the pending text
//     ahead of the payload the drain is about to write.
//   - clearDelivered hangs off msgqueue's OnDelivered and drops exactly what was
//     composed.
//
// COMPOSE AND CLEAR ARE NOT THE SAME MOMENT, which is why there are two of them
// rather than one. The drain retries a failed head at the same position, so text
// cleared at composition time is lost when the write then fails; OnDelivered fires
// once per CONFIRMED delivery, which is the only moment at which a post has
// genuinely been said.
//
// WHY A DECORATOR RATHER THAN A PARAMETER on newInboundDeliver. That function has
// seventeen call sites, sixteen of them tests, and this needs nothing from its
// interior. markApprovalHolds is the same shape in the same package for the same
// reason: a msgqueue.DeliverFunc in, a msgqueue.DeliverFunc out. The consequence
// worth naming is that composition happens BEFORE the seam's idle-gate wait, since
// that wait lives inside the wrapped function — so a post landing during the wait
// is carried by the NEXT turn rather than this one. Never lost, never doubled.
//
// AC5 — "what the clients see does not change" — is structural rather than a filter
// here. The composed value exists only as the []byte this decorator hands the
// wrapped seam. The durable and wire-facing producer hangs off OnDelivered, whose
// msgqueue.QueuedMessage declares no delivery field, so the composed text is left
// behind at that boundary by the type's own shape. newOperatorMessageHistory's doc
// block argues the same barrier from the other side; this file inherits it rather
// than restating it. It matters concretely because the payload composed onto may
// itself name an attachment's ON-HOST PATH (#2038), which
// docs/protocol-mobile.md § Error codes forbids putting on the wire.
type channelCarry struct {
	// reg is the conversation registry. nil leaves the carry inert, which is the
	// foreground / PTY posture and the reason a decorator built over a zero-value
	// carry still delivers exactly as it did before this existed.
	reg *conversations.Registry

	// path is the registry file, resolveConversationsRegistryPath's answer for this
	// instance — the same path every other registry-writing handler saves to.
	path string

	// logger records a refusal and a save failure, and nothing else. See record.
	logger *slog.Logger

	// mu guards composed. It is a LEAF: taken, used to read or write one map entry,
	// and released. It is never held across a registry call, across a Save, or
	// across the wrapped delivery — which can block for a whole claude turn.
	mu sync.Mutex

	// composed records, per conversation, how many pending entries the last delivery
	// attempt carried, so clearDelivered drops exactly those and no more. It is a
	// count rather than the entries themselves because a count is what
	// Registry.ClearPendingChannelPosts can act on atomically, and because the
	// entries would be a second copy of conversation content held in memory.
	//
	// A COUNT IS SAFE ONLY BECAUSE OF TWO INVARIANTS, both of them somebody else's.
	// msgqueue runs one drain goroutine per conversation and one delivery at a time
	// within it, so the value written by an attempt is always the one read by that
	// attempt's confirmation — a stale value from a failed attempt is overwritten by
	// the next attempt before OnDelivered can fire. And
	// Registry.AppendPendingChannelPost refuses the newest post when its bound is
	// full rather than evicting the oldest, so nothing but a clear ever removes an
	// entry from the HEAD of the record, which is what makes the leading n the same
	// entries this attempt read. Both are load-bearing; neither is local to this
	// file.
	composed map[string]int
}

// record stores text as pending carry-forward state on id's registry row and
// persists the registry best-effort. It is the hook channelPoster calls once the
// post's own durable entry has landed.
//
// A nil receiver or a nil registry is inert: no write, no save, no log.
//
// IT CANNOT FAIL THE POST, and returns nothing so that no caller can make it.
// #2497 and #2498 own what a post delivers — a durable entry and a live push — and
// a carry that could not be recorded must not turn a recorded post into a refusal a
// cron reads as a non-zero exit. A Save that fails leaves the pending text in
// memory, where the next registry save persists it.
//
// The Save sits behind the append's bool, so there is no path on which an unknown
// id reaches the disk — contextUsageRecorder.record's structure exactly.
//
// THE LOG LINES CARRY NO CONTENT AND NO COUNT. Never the text, for
// channelPoster's stated discipline; and never how many entries are pending or were
// refused, because across a bounded record that is a partial proxy for a post's
// length — the same reason channelPoster refuses to log its chunk count. The
// conversation id is safe for the reason contextUsageRecorder.record gives: the
// refusal branch and the save branch both sit inside a lookup that already matched,
// or report an id the daemon minted itself.
func (c *channelCarry) record(id conversations.ConversationID, text string) {
	if c == nil || c.reg == nil {
		return
	}
	if !c.reg.AppendPendingChannelPost(id, text) {
		// Either the row is gone or the accumulation bound is full; the two are one
		// answer here because the response to both is this line and nothing else.
		c.logger.Warn("control: channel.post not carried forward",
			"event", "channel_carry.not_recorded",
			"conversation_id", string(id))
		return
	}
	if err := c.reg.Save(c.path); err != nil {
		// Warn for contextUsageRecorder.record's reason: the in-memory row keeps the
		// pending text and the next registry save persists it, but until then a
		// restart reads back a record missing this post — a durable-record loss
		// rather than one dropped frame.
		c.logger.Warn("control: channel-carry registry save failed",
			"event", "channel_carry.registry_save_err",
			"conversation_id", string(id),
			"err", err)
	}
}

// carryPending decorates deliver so the conversation's pending posted text leads
// the payload the drain is about to write. A nil receiver or a nil registry returns
// deliver UNCHANGED — not a wrapper that happens to be an identity — so the PTY
// posture pays nothing and the pre-#2499 delivery is the same function value.
//
// The count is recorded BEFORE the delegation and on every attempt, including the
// ones that fail: a retry recomposes from the record as it stands then, so a post
// that arrived during a failed attempt is picked up by the retry, and the count
// clearDelivered reads is always the one the succeeding attempt composed.
//
// The delivery error is returned VERBATIM. msgqueue classifies ErrNoLiveSession,
// ErrTrustModalPending and turncommit.ErrDropped by errors.Is, and markApprovalHolds
// marks its own hold sentinel inside the wrapped seam — a wrap here would be a
// second answer to a question two other layers have already answered.
func (c *channelCarry) carryPending(deliver msgqueue.DeliverFunc) msgqueue.DeliverFunc {
	if c == nil || c.reg == nil {
		return deliver
	}
	return func(ctx context.Context, convID string, payload []byte) error {
		pending := c.reg.PendingChannelPosts(conversations.ConversationID(convID))
		c.mu.Lock()
		if c.composed == nil {
			c.composed = make(map[string]int)
		}
		c.composed[convID] = len(pending)
		c.mu.Unlock()
		return deliver(ctx, convID, composeChannelCarry(pending, payload))
	}
}

// clearDelivered is the msgqueue.DeliveredFunc that drops the pending text a
// confirmed delivery just carried. It fires once per confirmed delivery, from the
// drain goroutine, which is the same goroutine that ran the composition above.
//
// msg is read for NOTHING. The parameter exists because the seam's shape does, and
// reading msg.Text here would be a second record of a turn newOperatorMessageHistory
// already writes. Naming it keeps the signature legible against that seam's doc.
//
// TAKE-AND-DELETE, not read-then-clear. A confirmed delivery that carried nothing —
// a later reply in the same conversation, or one whose composition found the record
// empty — must not reach for whatever happens to be pending by the time it fires.
// Deleting the entry makes every clear with no composition behind it a no-op rather
// than a repeat of the last one.
//
// A count of zero still reaches the registry, where ClearPendingChannelPosts treats
// n <= 0 as "mutate nothing" — so a delivery that carried nothing performs no save
// either, because the save below sits behind a positive count.
func (c *channelCarry) clearDelivered(convID string, _ msgqueue.QueuedMessage) {
	if c == nil || c.reg == nil {
		return
	}
	c.mu.Lock()
	n := c.composed[convID]
	delete(c.composed, convID)
	c.mu.Unlock()
	if n <= 0 {
		return
	}
	if !c.reg.ClearPendingChannelPosts(conversations.ConversationID(convID), n) {
		return
	}
	if err := c.reg.Save(c.path); err != nil {
		// Warn, not Error, and for the mirror of record's reason: in-memory state is
		// already correct, and the cost of the gap is that a daemon restarted before
		// the next save carries these posts ONCE more. That is the same class as
		// msgqueue's own in-memory restart boundary, and re-showing a question is a
		// far cheaper failure than losing one.
		c.logger.Warn("control: channel-carry registry save failed",
			"event", "channel_carry.registry_save_err",
			"conversation_id", convID,
			"err", err)
	}
}

// composeChannelCarry builds what claude receives: a daemon-authored block naming
// the messages posted into this channel since its last turn, followed by the
// payload the queue is delivering.
//
// AN EMPTY LIST IS THE IDENTITY, and it returns the caller's own slice rather than
// an equal copy. composeAttachmentPrompt's precedent, and the whole of the ticket's
// "a reply in a conversation with nothing pending reaches claude byte-for-byte as it
// does today" — an early return says that, where a formatting rule that happens to
// produce the same bytes only promises it.
//
// payload arrives LAST and verbatim; it is already whatever send_message composed,
// which for a message naming attachments is a prompt naming their on-host paths. It
// is neither re-parsed nor re-escaped here. An empty payload yields the block alone,
// with no trailing separator.
//
// One blank line between every part, and no delimiter a post could not contain. That
// is deliberate rather than an oversight: there is nothing to defend by picking a
// rarer separator, because the caller controls all these bytes either way, and the
// header — not the separator — is what carries the provenance. See
// channelCarryHeader.
func composeChannelCarry(posts []string, payload []byte) []byte {
	if len(posts) == 0 {
		return payload
	}
	var b strings.Builder
	b.WriteString(channelCarryHeader)
	for _, p := range posts {
		b.WriteString("\n\n")
		b.WriteString(p)
	}
	if len(payload) > 0 {
		b.WriteString("\n\n")
		b.Write(payload)
	}
	return []byte(b.String())
}

// deliveredFuncs fans msgqueue's single-valued OnDelivered seam out to several
// consumers, in the order given. #2115's durable operator-message record held that
// field alone until #2499 needed to clear the carry at the same moment.
//
// ORDER IS THE CONTRACT. The history record goes first, because its own doc block
// states that it is written as close to the commit as possible, and that is an
// existing property this must not quietly reorder.
//
// nil members are skipped, and an all-nil set answers nil rather than an empty
// fan-out, so a daemon with neither consumer wired leaves the queue's seam DISABLED
// instead of paying a call per delivery to do nothing.
//
// No recover() around the members. A panicking consumer takes the drain goroutine
// down, which is exactly what it did when the field held one function, and swallowing
// it here would hide a daemon bug behind a stalled conversation.
func deliveredFuncs(fns ...msgqueue.DeliveredFunc) msgqueue.DeliveredFunc {
	live := make([]msgqueue.DeliveredFunc, 0, len(fns))
	for _, fn := range fns {
		if fn != nil {
			live = append(live, fn)
		}
	}
	if len(live) == 0 {
		return nil
	}
	return func(convID string, msg msgqueue.QueuedMessage) {
		for _, fn := range live {
			fn(convID, msg)
		}
	}
}
