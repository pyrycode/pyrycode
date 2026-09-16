package main

import (
	"context"
	"log/slog"
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

// contextUsageRecorder is the registry memory behind #2460: the ONE place a
// reading's summary becomes durable, shared by both producers of a reading — the
// post-turn emitter arm (#2371, reached through interactiveTurnEmitterV2.usageRec)
// and the on-demand flight below. It lives in this file rather than in a sixth of
// its own because the second producer is here; nothing about it is specific to
// the on-demand lane.
//
// ONE MEMORY, TWO PRODUCERS is the whole point. A value written from only one arm
// goes stale the moment a client uses the other, so the recorder takes the raw
// turnevent.ContextUsage both lanes carry rather than either lane's mapped form.
//
// THE PROJECTION DROPS THE THREE INVENTORIES BY NEVER NAMING THEM. Categories,
// MCPTools and MemoryFiles carry workspace memory-file paths and MCP server names
// the frame's no-log rule keeps out of records; record's composite literal lists
// exactly the five stored fields, so there is no field to forget to strip and no
// ordering in which they reach disk. Do not "complete" it from the event.
type contextUsageRecorder struct {
	// reg is the conversation registry. nil leaves the recorder inert, which is
	// the foreground / PTY posture and the reason every emitter and resolver test
	// that constructs without one stays green.
	reg *conversations.Registry

	// path is the registry file, resolveConversationsRegistryPath's answer for
	// this instance — the same path every other registry-writing handler saves to.
	path string

	// logger records a save failure and nothing else. See record.
	logger *slog.Logger

	// now is the injected clock; nil means time.Now. Tests pin as_of with it.
	now func() time.Time
}

// record stores the summary of u on id's registry row and persists the registry
// best-effort — appendConversationHistory's shape, for the registry rather than
// for the durable log.
//
// A nil receiver or a nil registry is inert: no write, no save, no log. An id with
// no registry row writes nothing AND saves nothing, which is structural rather
// than incidental — the Save sits behind SetLastContextUsage's bool, so there is
// no path on which an unknown id reaches the disk.
//
// ONE Save PER SETTLE, SYNCHRONOUSLY, and no background saver. The post-turn
// reading arrives after the turn it describes has closed, so the fsync sits in an
// idle gap on the emitter's lane; the on-demand one runs on its flight's own
// goroutine after the flight has already settled, so no client waits behind it.
//
// LAST WRITE WINS. The two producers can settle seconds apart and both save; the
// registry's own saveMu serializes the snapshot→rename sequence, and because Save
// snapshots in-memory state at save time, whichever save renames last writes the
// NEWEST row rather than its own caller's. No interleaving leaves the file holding
// an older reading than memory. Ordering machinery here would be a second answer
// to a question the registry has already answered.
//
// THE ONLY LOG LINE IS THE SAVE FAILURE, and it carries the event name, the
// conversation id and the registry error — never a field of the reading. The
// conversation id is safe to record for a structural reason worth keeping: this
// branch is inside the one SetLastContextUsage's true opened, so an id that
// matched no row never reaches a logger at all, and only registry-canonical ids
// are ever written here. The registry error names the registry path and an OS
// error; its encode arm cannot carry the reading, since this struct holds no
// floats, no channels and no custom marshaller, and encoding/json substitutes
// invalid UTF-8 rather than reporting it.
func (r *contextUsageRecorder) record(id conversations.ConversationID, u turnevent.ContextUsage) {
	if r == nil || r.reg == nil {
		return
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	if !r.reg.SetLastContextUsage(id, conversations.ContextUsageReading{
		Model:       u.Model,
		TotalTokens: u.TotalTokens,
		MaxTokens:   u.MaxTokens,
		Percentage:  u.Percentage,
		AsOf:        now(),
	}) {
		return
	}
	if err := r.reg.Save(r.path); err != nil {
		// Warn for appendConversationHistory's reason: the in-memory row keeps the
		// new reading and the next settle retries, but until then a restart reads
		// back a stale one — a durable-record loss rather than one dropped frame.
		r.logger.Warn("relay: context-usage registry save failed",
			"event", "context_usage.registry_save_err",
			"conversation_id", string(id),
			"err", err)
	}
}

// last reads back what record wrote: the stored summary for id, or false when the
// registry holds no row for it or claude has never reported on it. It is the other half
// of the #2460 memory, added by #2461 so a conversation that cannot yield a fresh
// reading still has an answer.
//
// ON THIS TYPE rather than on the resolver, because the write door already is: keeping
// both directions here leaves the registry handle in one place, and means
// contextUsageResolver still holds no registry of its own. A nil receiver or a nil
// registry answers false, record's inert posture, so the unwired daemon simply has
// nothing to remember.
//
// THE DEREFERENCE IS RACE-FREE FOR A STRUCTURAL REASON, not by luck.
// SetLastContextUsage copies its argument into a fresh local and replaces the row's
// POINTER under the registry's mutex; it never mutates a pointee that has already been
// published. Registry.Get returns the row under that same mutex, so the pointer this
// reads was fully written before it became visible, and dereferencing yields a value
// copy nothing else holds.
//
// IT TAKES NO LOGGER AND RETURNS NO ERROR, which is what makes #2461's no-log criterion
// structural: there is no error value for a caller to wrap a model string or a
// timestamp into, and no branch here that could grow a record.
func (r *contextUsageRecorder) last(id conversations.ConversationID) (conversations.ContextUsageReading, bool) {
	if r == nil || r.reg == nil {
		return conversations.ContextUsageReading{}, false
	}
	conv, ok := r.reg.Get(id)
	if !ok || conv.LastContextUsage == nil {
		return conversations.ContextUsageReading{}, false
	}
	return *conv.LastContextUsage, true
}

// contextUsageQuerier is the optional per-runner capability #2430 landed. It stays off
// sessions.Runner for QueryMCPStatus's stated reason: its only consumer is the
// resolver in this package, and widening that interface would pull every test double
// into the slice.
type contextUsageQuerier interface {
	QueryContextUsage(ctx context.Context, detail string) (turnevent.ContextUsage, bool)
}

// contextUsageResolveFunc answers "is this conversation one this daemon hosts, what is
// the daemon's own name for it, and which live child — if any — can be asked about it".
// It touches no child and performs no wait, which is what lets the resolver call it
// BEFORE consulting the collapse map.
//
// THE BOOL AND THE QUERIER ANSWER DIFFERENT QUESTIONS, and #2461 is what separated
// them. The bool is membership: false means the registry holds no row, which is the
// one state with neither a canonical id to key the collapse map on nor a row that
// could hold a remembered reading, and it stays a permanent refusal. A true with a NIL
// querier is "hosted, but there is nothing to ask" — the state a remembered answer
// exists for. Callers MUST check the querier for nil separately; see fresh, where that
// check is load-bearing rather than defensive.
type contextUsageResolveFunc func(conversationID string) (contextUsageQuerier, conversations.ConversationID, bool)

// contextUsageResolve builds the registry-and-pool half over resolveBoundRunner, the
// shape resolveBoundMCPStatus already has. There is NO BOOTSTRAP FALLTHROUGH:
// resolveBoundRunner's CurrentSessionID == "" guard is the #678 cross-conversation
// isolation enforcement point, and an unbound conversation must never be answered with
// the shared bootstrap child's reading. That guard is untouched here and stays the only
// place "bound" is decided.
//
// WHAT #2461 CHANGED IS WHICH OUTCOMES REFUSE. resolveBoundRunner collapses three into
// one false — no registry row, no bound session or no live child, and a lookup that
// fails — and only the FIRST may remain a hard refusal. The other two describe a
// conversation this daemon hosts, which is exactly the conversation a stored reading
// belongs to, so they return the canonical id with a nil querier and a true. A runner
// that does not implement contextUsageQuerier joins them for the same reason.
//
// THE MEMBERSHIP LOOKUP IS THIS FUNCTION'S OWN rather than inferred from
// resolveBoundRunner's bool, because the two questions genuinely differ and inferring
// one from the other is what produced the refusal this ticket removes.
//
// nil when either dependency is missing, which leaves the seam unwired and the verb
// inert — the foreground / PTY posture.
func contextUsageResolve(convReg *conversations.Registry, pool *sessions.Pool) contextUsageResolveFunc {
	if convReg == nil || pool == nil {
		return nil
	}
	return func(convID string) (contextUsageQuerier, conversations.ConversationID, bool) {
		conv, hosted := convReg.Get(conversations.ConversationID(convID))
		if !hosted {
			return nil, "", false
		}
		// From here the id is REGISTRY-CANONICAL: conv.ID is the daemon's own
		// record, never the client's string, so nothing downstream keys a map or
		// stamps a payload with remote-authored bytes even if a future Get
		// normalises rather than matching byte-exactly.
		runner, canonicalID, bound := resolveBoundRunner(convReg, pool, convID)
		if !bound {
			return nil, conv.ID, true
		}
		querier, isQuerier := runner.(contextUsageQuerier)
		if !isQuerier {
			return nil, canonicalID, true
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

	// rec is the #2460 registry memory, written when a flight settles with ok.
	// nil leaves this lane's write inert, which is what keeps every existing test
	// here constructing through newContextUsageResolver unchanged.
	//
	// Assigned after construction rather than passed, the shape
	// interactiveTurnEmitterV2.hist uses. The call-site count argument is weaker
	// here — six, not eighty-six — but a fifth positional parameter on a
	// four-parameter constructor is worse either way, and ctxUsageResolverFor
	// would have to grow one too.
	rec *contextUsageRecorder

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
// THREE STEPS, WITH EXACTLY ONE FALLBACK POINT (#2461). Membership refuses or yields a
// canonical id; fresh tries for a reading claude produces now; remembered answers from
// what the registry stored if it could not. The split exists because fresh has FOUR
// exits that can report no-reading — a cached settled refusal, a joined flight, an
// awaited one, and the nothing-to-ask arm — and bolting the fallback onto each would be
// four places to keep in agreement about a decision that is one decision.
//
// A DEPARTED CALLER IS ANSWERED FROM MEMORY LIKE ANY OTHER. fresh returns false both
// when a flight settled not-ok and when the caller's own context ended, and the two are
// deliberately not separated: telling them apart would mean await reporting three
// states, and this seam's contract is about whether there is an answer, not about who
// is still listening.
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
	if payload, fresh := r.fresh(ctx, querier, canonicalID); fresh {
		return payload, true
	}
	return r.remembered(canonicalID)
}

// remembered answers from the summary #2460 stored on the conversation's registry row:
// the four numbers claude last reported, stamped with when the daemon recorded them.
// No stored reading is the retryable refusal the relay was already producing.
//
// IT IS A READ, AND NOTHING ABOUT IT MAY BECOME A WRITE. A fallback expressed as a
// flight settling ok would hand this reading to fly's deferred recorder, writing it
// straight back with a fresh AsOf — a stale figure that renews itself on every ask and
// can never look stale again. Only a reading claude actually produced may reach
// contextUsageRecorder.record.
//
// THE THREE INVENTORIES ARE LEFT NIL ON PURPOSE. ContextUsagePayload.MarshalJSON
// normalises them to [], and AsOf is what tells a client that those empty lists mean
// "never stored" rather than "claude reported none" — the two are byte-identical
// without it. Filling them from anywhere would be inventing a breakdown.
//
// The id is stamped from the CANONICAL value resolution returned, so the reported
// conversation_id is the daemon's own record rather than an echo of the request, and
// the address taken is of this frame's own copy of the stored time.
func (r *contextUsageResolver) remembered(canonicalID conversations.ConversationID) (protocol.ContextUsagePayload, bool) {
	reading, ok := r.rec.last(canonicalID)
	if !ok {
		return protocol.ContextUsagePayload{}, false
	}
	asOf := reading.AsOf
	return protocol.ContextUsagePayload{
		ConversationID: string(canonicalID),
		Model:          reading.Model,
		TotalTokens:    reading.TotalTokens,
		MaxTokens:      reading.MaxTokens,
		Percentage:     reading.Percentage,
		AsOf:           &asOf,
	}, true
}

// fresh is the collapse map and the round trip: one reading claude produces now, or
// false. It is Get's body from #2431 with one gate in front of it.
//
// THE NIL-QUERIER CHECK IS THE FIRST STATEMENT AND MUST STAY THERE, before the mutex
// and before any map access. Since #2461 the resolve seam returns true with a nil
// querier for a hosted conversation with nothing to ask, so this is the only thing
// standing between that state and fly, which dereferences the querier unconditionally
// — a nil interface call, a panic on a goroutine, on a path a paired client reaches by
// asking about any dormant conversation. A flight installed here would also be keyed to
// a conversation no round trip can ever settle.
// TestContextUsageResolver_MemoryInstallsNoFlight asserts the map stays empty rather
// than asserting the reply, because the reply looks the same either way.
func (r *contextUsageResolver) fresh(ctx context.Context, querier contextUsageQuerier, canonicalID conversations.ConversationID) (protocol.ContextUsagePayload, bool) {
	if querier == nil {
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
// NOTHING HERE LOGS THE READING, and nothing may be added that does.
// QueryContextUsage's own block states the prohibition and its reason: the values in
// scope include a reading carrying memory-file paths off the operator's filesystem.
// Every refusal below is silent by construction, and the relay's handler records the
// outcome with the merged wire code instead.
//
// THE BOUNDARY #2460 ADDED, stated rather than broken silently: the recorder called
// from the settle below can emit ONE record, and only when persisting the reading
// fails. It carries the conversation id and the registry error — neither of which is
// a string of the reading — and record's own block owns that argument. The
// prohibition above is about the reading's strings, and it is intact.
func (r *contextUsageResolver) fly(f *contextUsageFlight, querier contextUsageQuerier, canonicalID conversations.ConversationID) {
	var (
		payload protocol.ContextUsagePayload
		ok      bool
		// usage is hoisted out of the query below so the settle can hand it to the
		// registry memory. Meaningful only when ok.
		usage turnevent.ContextUsage
	)
	// Deferred so a panic below cannot strand joiners waiting on a channel that never
	// closes. The writes precede the close, which is the barrier every reader gates on.
	defer func() {
		f.payload, f.ok, f.settled = payload, ok, r.clock()
		close(f.done)
		// #2460: remember this reading. AFTER the close, deliberately — every asker
		// joined to this flight is released before the registry's fsync rather than
		// behind it. Gated on ok, so the memory holds readings claude produced and
		// never the absence of one: a refusal that wrote would blank a client's
		// display on a transient child failure. Nothing touches f past the close.
		if ok {
			r.rec.record(canonicalID, usage)
		}
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
	var queried bool
	usage, queried = querier.QueryContextUsage(queryCtx, fullContextUsageDetail)
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
