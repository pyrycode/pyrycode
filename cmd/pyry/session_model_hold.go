package main

import (
	"log/slog"
	"slices"
	"sync"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// sessionModelHold is one session's retained turnevent.ModelList plus the sink
// decorator that fills it (#1840). claude answers one initialize control request
// per child (#1839) and the parser decodes its models array into exactly one
// ModelList; nothing kept it, so the list existed for a microsecond and was gone.
// This keeps it for the session's life so #1837 can publish it to a client that
// connects without waiting for a turn.
//
// IT SITS UPSTREAM OF THE FAN-IN SEND, and that placement is the design's whole
// point rather than an implementation detail. `turnMarkFor`'s default arm answers
// turnMarkNone for ModelList, so `sinkFor` refuses it at droppableCap under load —
// and the drop policy's "one event of transcript fidelity, self-healing on the
// next event" justification does not cover this variant, because one initialize
// exchange per child produces exactly one of these and no later event replaces it.
// A retention point below that send could lose the list for the whole life of the
// child. Retaining above it means the refusal can still discard the EVENT and
// never the RETENTION.
//
// Its lifetime is the session's with no registry and no removal hook: the hold is
// a field of the per-session `streamRunner`, and `Session.Runner` states that a
// session's runner is assigned once at construction and never reassigned. There
// is deliberately no daemon-global session-keyed map — `streamTurnSink`'s own doc
// rules one out for the fan-in, and a map here would outlive every session it
// keyed with nothing to prune it.
//
// SECURITY: it has no *slog.Logger field and its constructor takes none, so there
// is no path by which Value, ResolvedModel, DisplayName or an effort level can
// reach a log record — the #833 posture that `internal/relay`'s
// v2session_settings.go and `internal/sessions`' pool.go restate as "model /
// effort / YOLO values are NEVER logged at any level", enforced by construction
// rather than by care. Do not give it one to write a retention diagnostic: that
// would reopen exactly the channel the posture closes.
type sessionModelHold struct {
	mu   sync.Mutex
	list turnevent.ModelList
	have bool
	// next is the downstream sink every event is forwarded to, unchanged. nil
	// forwards nothing — a test convenience; production always supplies
	// streamTurnSink.sinkFor's closure.
	next func(turnevent.Event)
}

// newSessionModelHold mints a hold that forwards to next.
func newSessionModelHold(next func(turnevent.Event)) *sessionModelHold {
	return &sessionModelHold{next: next}
}

// Sink is the decorator, used as a method value: hold.Sink is a
// func(turnevent.Event) of exactly the shape streamsup.NewParser takes.
//
// ORDER IS THE CONTRACT. A ModelList is stored — replacing any prior value, so a
// respawn's report supersedes rather than accumulates — BEFORE ev is forwarded,
// which is what puts the retention upstream of the droppable send (see the type's
// doc). Every event of every variant is then forwarded unchanged, ModelList
// included: swallowing it here would change what the fan-in and the drain observe,
// which this ticket has no reason to do.
//
// The mutex is a LEAF lock and is never held across the call to next. Holding it
// across a channel send would put a new edge into the daemon's lock order for no
// benefit; as written it participates in no ordering with Pool.mu, Session.lcMu or
// capMu. The lock is needed rather than defensive: across a respawn a new stdout
// forwarder goroutine writes to the same hold and the two can briefly overlap
// during teardown, and #1837's publisher reads on a relay-leg goroutine.
func (h *sessionModelHold) Sink(ev turnevent.Event) {
	if list, ok := ev.(turnevent.ModelList); ok {
		h.mu.Lock()
		// Stored without copying: streamsup's emitModelList allocates Models fresh per
		// emit and the parser retains no reference, so the hold takes sole ownership of
		// what it is handed. The COPY is made on the read side instead.
		h.list = list
		h.have = true
		h.mu.Unlock()
	}
	if h.next != nil {
		h.next(ev)
	}
}

// ModelList returns the retained list and true, or the zero value and false when
// nothing has been reported for this session. The bool is the ONLY spelling of the
// unreported state: turnevent.ModelList.Models is documented "Never empty", so an
// empty retained list is not a value any reader can be handed and cannot stand in
// for it.
//
// The returned list is a DEEP copy, so a reader may mutate it freely without
// corrupting the retained value or another reader's copy — the contract Pool.List
// states for its snapshot, and worth the clone here because this value lives for
// the session's whole life and will be read repeatedly by different consumers.
//
// Nil-receiver-safe, mirroring `clearForSession`'s precedent: a runner whose hold
// was never minted answers the unreported state rather than panicking.
func (h *sessionModelHold) ModelList() (turnevent.ModelList, bool) {
	if h == nil {
		return turnevent.ModelList{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.have {
		return turnevent.ModelList{}, false
	}
	return cloneModelList(h.list), true
}

// cloneModelList deep-copies a ModelList: the Models slice, and within each entry
// both []string fields. DroppedModels and the scalar strings copy by assignment.
//
// slices.Clone returns nil for a nil input, which preserves turnevent's convention
// that these fields are nil when there is nothing to report and never an empty
// non-nil slice (see turnevent.ModelOption.TruncatedFields).
func cloneModelList(list turnevent.ModelList) turnevent.ModelList {
	out := list
	out.Models = slices.Clone(list.Models)
	for i := range out.Models {
		out.Models[i].EffortLevels = slices.Clone(out.Models[i].EffortLevels)
		out.Models[i].TruncatedFields = slices.Clone(out.Models[i].TruncatedFields)
	}
	return out
}

// newSessionParser mints the hold and the parser bound to it in ONE call, and
// returns both. It is the only production caller of streamsup.NewParser.
//
// The single call is what makes the two halves impossible to wire to different
// holds, and what makes the wiring testable: a test can call this, write a real
// control_response initialize line into the returned parser, and read the returned
// hold — proving the production composition through the real decoder rather than
// by inspection. next is the per-session downstream sink; logger is the parser's.
func newSessionParser(next func(turnevent.Event), logger *slog.Logger) (*streamsup.Parser, *sessionModelHold) {
	hold := newSessionModelHold(next)
	return streamsup.NewParser(hold.Sink, logger), hold
}
