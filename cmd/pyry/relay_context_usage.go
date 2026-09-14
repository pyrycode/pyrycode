package main

import (
	"context"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// This file holds the daemon side of the on-demand context-usage read (#2431): the
// producer behind internal/relay's ContextUsageFor seam. It is the only thing in this
// binary that calls streamsup.Runner.QueryContextUsage, which #2430 landed with no
// caller.
//
// TWO LAYERS, and the seam between them is where the CANONICAL ID appears.
// contextUsageResolve turns a client's conversation string into the bound runner and
// the registry's own id for it, touching no child. contextUsageResolver sits above and
// owns the two things the relay cannot do for itself: deferring a request that arrives
// mid-turn, and collapsing closely-spaced asks into one round trip.
//
// THE COLLAPSE IS PER CONVERSATION, NOT PER CONNECTION, which is why it lives here
// rather than in the relay handler. Two clients watching one conversation is the case
// it exists for, and internal/relay has no conversation-keyed state to hold it in. The
// relay's seam doc states the same division from the other side.
//
// IT COSTS REAL TOKENS, which is the whole reason the collapse exists rather than
// being an optimisation. The automatic post-turn reading is detail:"summary" — claude
// answers it from the last response's usage plus local estimates. This path asks for
// detail:"full", which counts each category through claude's token-count API, so a
// client that asked as fast as a finger moves would spend real money doing it.

const (
	// fullContextUsageDetail is the reading this path asks for, and asking for the
	// expensive one IS the verb. Its cheap peer, summaryContextUsageDetail, belongs to
	// the automatic post-turn requester; a request verb that asked for that would spend
	// a round trip re-delivering what the client already has.
	//
	// The DAEMON chooses it, never the client: the request payload carries no detail
	// key, because closely-spaced asks collapse below this point and are all answered
	// from one result — so a client-selected detail would let one client downgrade what
	// another is shown. streamsup's own allow-list is the other reason; this constant
	// must stay one of its two accepted values.
	fullContextUsageDetail = "full"

	// contextUsageCollapseWindow is how long one conversation's completed reading
	// answers further asks before a fresh round trip is made. It is NAMED rather than
	// inlined so a test can pin behaviour on both sides of it against an injected
	// clock, without sleeping on a wall clock.
	//
	// SIZED FOR A FINGER, not for a cache. It has to cover the burst a human generates
	// by tapping a refresh control a few times, and nothing longer: a reading this
	// verb exists to make FRESH stops being fresh if it is held. Two seconds is well
	// inside the round trip it is saving.
	//
	// A REFUSAL IS HELD FOR THIS WINDOW TOO. "Both are answered from its result" does
	// not except failures, and caching only successes would leave a conversation whose
	// child cannot answer free to be re-asked as fast as a client can type — the one
	// state where the re-asking is most likely. The wire code is retryable, and this
	// window is short enough that the two do not contradict.
	contextUsageCollapseWindow = 2 * time.Second

	// contextUsageQueryTimeout bounds ONE round trip to the child, and it is the
	// deadline QueryContextUsage's own doc requires of its caller: takeStdin and the
	// child-generation recheck cover a child that dies or is replaced, but NOT one that
	// stays alive and simply never answers. Without it, such a child would park the
	// asking connection's appFrameWorker until daemon shutdown.
	//
	// IT BOUNDS THE QUERY AND NOT THE TURN WAIT. A request arriving mid-turn must be
	// answered AFTER that turn ends however long it runs, so the WaitIdle above it is
	// deliberately bounded only by the daemon context.
	contextUsageQueryTimeout = 30 * time.Second
)

// contextUsageQuerier is the optional per-runner capability #2430 landed. It stays off
// sessions.Runner for QueryMCPStatus's stated reason: its only consumer is the
// resolver in this package, and widening that interface would pull every test double
// into the slice.
type contextUsageQuerier interface {
	QueryContextUsage(ctx context.Context, detail string) (turnevent.ContextUsage, bool)
}

// contextUsageResolveFunc answers "which live child do I ask about this conversation,
// and what is this daemon's own name for it". It touches no child and performs no
// wait, which is what lets the resolver call it BEFORE consulting the collapse map.
type contextUsageResolveFunc func(conversationID string) (contextUsageQuerier, conversations.ConversationID, bool)

// contextUsageResolve builds the registry-and-pool half over resolveBoundRunner, the
// shape resolveBoundMCPStatus already has. Every non-resolvable state returns false, so
// there is NO BOOTSTRAP FALLTHROUGH: resolveBoundRunner's CurrentSessionID == "" guard
// is the #678 cross-conversation isolation enforcement point, and an unbound
// conversation must never be answered with the shared bootstrap child's reading.
//
// nil when either dependency is missing, which leaves the seam unwired and the verb
// inert — the foreground / PTY posture.
func contextUsageResolve(convReg *conversations.Registry, pool *sessions.Pool) contextUsageResolveFunc {
	if convReg == nil || pool == nil {
		return nil
	}
	return func(convID string) (contextUsageQuerier, conversations.ConversationID, bool) {
		runner, canonicalID, ok := resolveBoundRunner(convReg, pool, convID)
		if !ok {
			return nil, "", false
		}
		querier, ok := runner.(contextUsageQuerier)
		if !ok {
			return nil, "", false
		}
		return querier, canonicalID, true
	}
}

// contextUsageFlight is one round trip to one conversation's child, shared by every
// ask that arrives while it runs and by every ask inside the collapse window after it
// settles.
//
// done IS THE MEMORY BARRIER. payload, ok and settled are written once by the goroutine
// that owns the flight and then done is closed; every reader gates on observing that
// close first, which gives it a happens-before edge to those writes. That is why
// readers do not need the resolver's mutex to read them, and why nothing may write them
// after the close.
type contextUsageFlight struct {
	done    chan struct{}
	payload protocol.ContextUsagePayload
	ok      bool
	settled time.Time
}

// await blocks until the flight settles or the CALLER leaves. A caller's departure
// takes it out of the wait and nothing else — the flight belongs to every asker, not to
// whichever one installed it, so it is not cancelled from here.
func (f *contextUsageFlight) await(ctx context.Context) (protocol.ContextUsagePayload, bool) {
	select {
	case <-f.done:
		return f.payload, f.ok
	case <-ctx.Done():
		return protocol.ContextUsagePayload{}, false
	}
}

// contextUsageResolver is the ContextUsageFor seam's production implementation: the
// mid-turn deferral, the per-conversation collapse, and one round trip to the child.
type contextUsageResolver struct {
	// base owns every flight. See fly for why it is not the caller's context.
	base    context.Context
	resolve contextUsageResolveFunc

	// busy is the mid-turn deferral. Optional: nil is the PTY wiring, where the
	// tracker is never constructed. WaitIdle carries no nil-receiver guard of its own,
	// so this type owns that check — the same bargain waitIdleForDelivery keeps.
	busy *turnBusyTracker

	// now is the injected clock; nil means time.Now. Tests supply their own so the
	// collapse window can be crossed by advancing it rather than by sleeping.
	now func() time.Time

	mu sync.Mutex
	// flights is keyed by the CANONICAL id contextUsageResolveFunc returned, never by
	// the client's string, and an entry is installed only past resolution. Both halves
	// are load-bearing. Keying on the request string would let any paired client mint
	// entries by naming conversations this daemon does not host, turning a map bounded
	// by hosted conversations into one bounded by request volume. And while
	// conversations.Registry.Get matches byte-exactly today — so the two strings happen
	// to coincide — that is a property of internal/conversations rather than of this
	// type, and a future normalising Get would otherwise split one conversation's
	// collapse across several keys.
	//
	// Entries are REPLACED rather than accumulated: an expired flight is overwritten by
	// its successor under the same key, so the map's size is bounded by the number of
	// conversations that have ever been asked about, never by how often.
	flights map[conversations.ConversationID]*contextUsageFlight
}

func newContextUsageResolver(
	base context.Context,
	resolve contextUsageResolveFunc,
	busy *turnBusyTracker,
	now func() time.Time,
) *contextUsageResolver {
	if base == nil {
		base = context.Background()
	}
	return &contextUsageResolver{
		base:    base,
		resolve: resolve,
		busy:    busy,
		now:     now,
		flights: make(map[conversations.ConversationID]*contextUsageFlight),
	}
}

func (r *contextUsageResolver) clock() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}

// Get answers one inbound request_context_usage. It satisfies internal/relay's
// ContextUsageFor seam, whose doc block carries the contract this implements.
//
// RESOLUTION PRECEDES COLLAPSE, and the order is a containment property rather than a
// style choice: the map is keyed by the id resolution returns, and an unresolvable
// conversation must leave no trace in it at all. See the flights field.
//
// A nil receiver and an unwired resolve seam both refuse, so an unwired daemon is inert
// rather than a crash — the posture every optional seam in this binary keeps.
func (r *contextUsageResolver) Get(ctx context.Context, conversationID string) (protocol.ContextUsagePayload, bool) {
	if r == nil || r.resolve == nil {
		return protocol.ContextUsagePayload{}, false
	}
	querier, canonicalID, ok := r.resolve(conversationID)
	if !ok {
		return protocol.ContextUsagePayload{}, false
	}

	r.mu.Lock()
	if f, exists := r.flights[canonicalID]; exists {
		select {
		case <-f.done:
			// Settled. Inside the window its result answers this ask too; past the
			// window it is stale and we fall through to install a successor.
			if r.clock().Sub(f.settled) < contextUsageCollapseWindow {
				payload, settledOK := f.payload, f.ok
				r.mu.Unlock()
				return payload, settledOK
			}
		default:
			// Still in flight: join it. This is AC-2's in-flight side, and the whole
			// reason the check-and-install below happens under ONE lock acquisition —
			// splitting them would let two asks each install a flight and each write to
			// the child.
			r.mu.Unlock()
			return f.await(ctx)
		}
	}
	f := &contextUsageFlight{done: make(chan struct{})}
	r.flights[canonicalID] = f
	r.mu.Unlock()

	// THE INSTALLER AWAITS LIKE ANY OTHER CALLER rather than running the round trip
	// inline, and the uniformity is the point. A flight belongs to every ask that
	// joins it, so no caller — including the one that happened to install it — may
	// hold it open or be held open by it. Running it inline here would leave the
	// installer unable to leave on its own context while joiners could, so one
	// client's disconnect would be answered differently depending on which of them
	// asked first. The goroutine's exit is bounded twice over: WaitIdle by the daemon
	// context and the query by contextUsageQueryTimeout, so it cannot outlive either.
	go r.fly(f, querier, canonicalID)
	return f.await(ctx)
}

// fly performs one round trip and settles the flight, on its OWN goroutine — see the
// call site for why it is not run inline on the installing caller's. It terminates
// unconditionally: WaitIdle is bounded by the daemon context and the query by
// contextUsageQueryTimeout, so daemon shutdown reaps it and a silent child cannot
// strand it.
//
// THE FLIGHT RUNS UNDER THE DAEMON CONTEXT, NOT ANY CALLER'S, and that is the one
// thing here most likely to be "simplified" back into a defect. A flight is shared:
// every ask that joins it is answered from its result. Under the installing caller's
// context, that client disconnecting would cancel a round trip other clients are still
// waiting on, settle the shared flight as a refusal, and — refusals being held for the
// window — deny them for as long as it lasts. One client's departure must not answer
// for another's request. Callers still leave on their own contexts; see await.
//
// The deadline that detachment costs is restored at the step that needs it, and only
// there: WaitIdle is bounded by the daemon context alone, because AC-4 requires a
// mid-turn request to be answered after the turn ends however long it runs, while the
// child round trip gets contextUsageQueryTimeout.
//
// NOTHING HERE LOGS, and nothing may be added that does. QueryContextUsage's own block
// states the prohibition and its reason: the values in scope include a reading carrying
// memory-file paths off the operator's filesystem. Every refusal below is silent by
// construction, and the relay's handler records the outcome with the merged wire code
// instead.
func (r *contextUsageResolver) fly(f *contextUsageFlight, querier contextUsageQuerier, canonicalID conversations.ConversationID) {
	var (
		payload protocol.ContextUsagePayload
		ok      bool
	)
	// Deferred so a panic below cannot strand joiners waiting on a channel that never
	// closes. The writes precede the close, which is the barrier every reader gates on.
	defer func() {
		f.payload, f.ok, f.settled = payload, ok, r.clock()
		close(f.done)
	}()

	flightCtx, cancel := context.WithCancel(r.base)
	defer cancel()

	// The mid-turn deferral (AC-4). WaitIdle returns at once when the conversation is
	// idle, unknown or unbound, so this costs nothing in the ordinary case; when a turn
	// IS open it blocks, and NOTHING has been written to the child at that point.
	if r.busy != nil {
		if err := r.busy.WaitIdle(flightCtx, string(canonicalID)); err != nil {
			return
		}
	}

	queryCtx, cancelQuery := context.WithTimeout(flightCtx, contextUsageQueryTimeout)
	defer cancelQuery()
	usage, queried := querier.QueryContextUsage(queryCtx, fullContextUsageDetail)
	if !queried {
		return
	}

	// The SAME mapper the automatic post-turn path uses, stamped with the registry's
	// own id rather than the client's string — so the reported conversation_id can
	// never be an echo of untrusted input. Forking the mapping here would be a second
	// place the payload's shape is decided.
	typ, mapped, mappedOK := turnbridge.MapEvent(usage, turnbridge.TurnContext{ConversationID: string(canonicalID)})
	if !mappedOK || typ != protocol.TypeContextUsage {
		return
	}
	reading, cast := mapped.(protocol.ContextUsagePayload)
	if !cast {
		return
	}
	payload, ok = reading, true
}
