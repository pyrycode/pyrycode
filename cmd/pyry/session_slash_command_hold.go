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
	mu   sync.Mutex
	list turnevent.SlashCommandList
	have bool
	// next is the downstream sink every event is forwarded to, unchanged. nil
	// forwards nothing — a test convenience; production always supplies the next
	// link of newSessionParser's chain.
	next func(turnevent.Event)
}

// newSessionSlashCommandHold mints a hold that forwards to next.
func newSessionSlashCommandHold(next func(turnevent.Event)) *sessionSlashCommandHold {
	return &sessionSlashCommandHold{next: next}
}

// Sink is the decorator, used as a method value: hold.Sink is a func(turnevent.Event)
// of exactly the shape streamsup.NewParser takes and of exactly the shape the next
// link of the chain takes.
//
// A SlashCommandList is stored — replacing any prior value, so a respawn's inventory
// supersedes rather than accumulates — and every event of every variant is then
// forwarded unchanged, this variant included: swallowing it here would change what the
// fan-in and the drain observe, which this ticket has no reason to do.
//
// Stored WITHOUT copying: emitSlashCommandList allocates its entry slice fresh per
// emit and its boundAliases closure documents that its result is always a fresh
// allocation and never the resliced input, so the hold takes sole ownership of what it
// is handed. The COPY is made on the read side instead.
//
// The mutex is a LEAF lock and is never held across the call to next. Holding it
// across a channel send would put a new edge into the daemon's lock order for no
// benefit; as written it participates in no ordering with Pool.mu, Session.lcMu or
// capMu. The lock is needed rather than defensive, and the pair it is needed for is
// ONE WRITER AND MANY READERS — not two writers. Writes are serial across every
// respawn, because spawnAndWait blocks on cmd.Wait, which os/exec documents as joining
// the goroutine copying the child's stdout into a non-*os.File Stdout, so forwarder
// N+1 cannot start until forwarder N has finished; streamsup.Parser's own doc asserts
// that serialisation. What the lock protects against is #2005's resolver reading on a
// relay-leg goroutine while that one writer runs.
func (h *sessionSlashCommandHold) Sink(ev turnevent.Event) {
	if list, ok := ev.(turnevent.SlashCommandList); ok {
		h.mu.Lock()
		h.list = list
		h.have = true
		h.mu.Unlock()
	}
	if h.next != nil {
		h.next(ev)
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
