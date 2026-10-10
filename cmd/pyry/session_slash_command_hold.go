package main

import (
	"slices"
	"sync"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// sessionSlashCommandHold is one session's retained turnevent.SlashCommandList plus
// the sink decorator that fills it (#2004). claude answers one initialize control
// request per child and streamsup's emitSlashCommandList decodes its commands array
// into exactly one SlashCommandList; nothing kept it, so the workspace's inventory
// existed for a microsecond and was gone. This keeps it for the session's life so
// #2005 can answer a client that connects without waiting for a turn.
//
// IT SITS UPSTREAM OF THE FAN-IN SEND, and that placement is the design's whole point
// rather than an implementation detail. `turnMarkFor`'s default arm answers
// turnMarkNone for this variant, so `sinkFor` refuses it at droppableCap under load —
// and the drop policy's "one event of transcript fidelity, self-healing on the next
// event" justification does not cover it, because one initialize exchange per child
// produces exactly one of these and no later event replaces it. A retention point
// below that send could lose the inventory for the whole life of the child. Retaining
// above it means the refusal can still discard the EVENT and never the RETENTION.
//
// IT IS A SIBLING OF sessionModelHold RATHER THAN A SECOND RETENTION INSIDE IT, and
// `newSessionParser` chains the two. That keeps each type's name and doc honest about
// the one value it holds, and gives each its own leaf mutex over that value. It costs
// nothing structurally: the property that matters is being a decorator on the parser's
// side of the channel at all, and a second link is the existing idea applied twice
// rather than a new one. The chain's ORDER carries no meaning — both links store
// unconditionally and forward unconditionally — so nothing should be inferred from it.
//
// Its lifetime is the session's with no registry and no removal hook, for the reason
// sessionModelHold states in full: the hold is a field of the per-session
// `streamRunner`, whose `Session.Runner` is assigned once at construction and never
// reassigned, and a daemon-global session-keyed map would outlive every session it
// keyed with nothing to prune it.
//
// SECURITY: it has no *slog.Logger field and its constructor takes none, so there is
// no path by which a Name, ArgumentHint, Description or alias can reach a log record.
// That is sessionModelHold's #833 posture with the threat one step sharper: those are
// claude's own strings, where every string here is the WORKSPACE's, so whoever
// controls a repository controls them. Enforced by construction rather than by care —
// do not give this type a logger to write a retention diagnostic, which is the one
// edit that would reopen the channel.
type sessionSlashCommandHold struct {
	live   *daemonInventoryIngress
	source daemonLiveSource
	mu     sync.Mutex
	list   turnevent.SlashCommandList
	have   bool
	// next is the downstream sink every event is forwarded to, unchanged. nil
	// forwards nothing — a test convenience; production always supplies the next
	// link of newSessionParser's chain.
	next func(turnevent.Event)
}

// newSessionSlashCommandHold mints a hold that forwards to next.
func newSessionSlashCommandHold(next func(turnevent.Event)) *sessionSlashCommandHold {
	return &sessionSlashCommandHold{next: next}
}

// Sink atomically replaces the slash-command inventory and its source before
// forwarding every event unchanged. A live attachment admits the inventory under
// the transition boundary and carries its captured envelope to fan-in. The parser
// owns the single writer; readers receive detached copies under the hold mutex.
// No hold mutex is held across downstream decorators or delivery.
func (h *sessionSlashCommandHold) Sink(ev turnevent.Event) {
	var captured *streamTurnEnvelope
	if list, ok := ev.(turnevent.SlashCommandList); ok {
		store := func(source daemonLiveSource) {
			h.mu.Lock()
			h.source = source
			h.list = list
			h.have = true
			h.mu.Unlock()
		}
		if h.live != nil {
			env := h.live.prepare(ev, store)
			captured = &env
		} else {
			store(daemonLiveSource{})
		}
	}
	if h.next != nil {
		h.next(ev)
	}
	if captured != nil {
		h.live.sink.forwardEvent(*captured)
	}
}

// SlashCommandList returns the retained list and true, or the zero value and false
// when nothing has been reported for this session. The bool is the ONLY spelling of
// the unreported state, and the reason is the PRODUCER's rather than the type's —
// which is where it differs from sessionModelHold's, whose ModelList.Models is
// documented "Never empty". Here streamsup's emitSlashCommandList returns early on a
// zero-length entry list (#1877), so nothing with zero entries ever reaches the
// retention and an empty list is not a value any reader can be handed.
//
// The returned list is a DEEP copy, so a reader may mutate it freely without
// corrupting the retained value or another reader's copy. It is worth the clone
// because this value lives for the session's whole life and will be read repeatedly by
// different consumers on different goroutines.
//
// Nil-receiver-safe, mirroring sessionModelHold's precedent: a runner whose hold was
// never minted answers the unreported state rather than panicking.
func (h *sessionSlashCommandHold) SlashCommandList() (turnevent.SlashCommandList, bool) {
	if h == nil {
		return turnevent.SlashCommandList{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.have {
		return turnevent.SlashCommandList{}, false
	}
	return cloneSlashCommandList(h.list), true
}

// cloneSlashCommandList deep-copies a SlashCommandList. It is THREE levels where
// cloneModelList is two: the Commands slice, and within each entry both Aliases and
// TruncatedFields. A clone that stops at Commands leaves two readers sharing backing
// arrays. DroppedCommands is an int and copies by assignment, and this variant has no
// top-level TruncatedFields — the count dimension reports per entry instead — so the
// field the sibling clones at that level does not exist here.
//
// slices.Clone returns nil for a nil input, which preserves turnevent's convention
// that these fields are nil when there is nothing to report and never an empty non-nil
// slice (see turnevent.SlashCommand.Aliases, which decides that collapse for the
// alias list, and BackgroundTask.TruncatedFields, the convention's single source).
func cloneSlashCommandList(list turnevent.SlashCommandList) turnevent.SlashCommandList {
	out := list
	out.Commands = slices.Clone(list.Commands)
	for i := range out.Commands {
		out.Commands[i].Aliases = slices.Clone(out.Commands[i].Aliases)
		out.Commands[i].TruncatedFields = slices.Clone(out.Commands[i].TruncatedFields)
	}
	return out
}

// SlashCommandListLive returns the menu and its producing capture as one snapshot.
func (h *sessionSlashCommandHold) SlashCommandListLive() (turnevent.SlashCommandList, daemonLiveSource, bool) {
	if h == nil {
		return turnevent.SlashCommandList{}, daemonLiveSource{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneSlashCommandList(h.list), h.source, h.have
}
