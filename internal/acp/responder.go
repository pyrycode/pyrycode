package acp

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
)

// ErrDeferred is the sentinel a request Handler returns to tell dispatchRequest
// not to write a response now: the handler has taken ownership of its Responder
// (obtained via ResponderFrom) and will resolve it later, from any goroutine —
// e.g. a session/prompt handler holding its call open for the duration of a
// turn. A handler that returns ErrDeferred without stashing its Responder leaves
// the request unanswered forever; that ownership contract is the consumer's, not
// the transport's.
var ErrDeferred = errors.New("acp: response deferred")

// ErrAlreadyResolved is returned by Responder.Reply / Responder.ReplyError when
// the response has already been written (a double-resolve, or a resolve losing a
// race with another goroutine). The losing call writes no frame — it only
// reports that it did not answer.
var ErrAlreadyResolved = errors.New("acp: response already resolved")

// responderKey is the unexported context key under which dispatchRequest injects
// the per-request *Responder. A zero-size struct key type is the collision-free
// idiom for request-scoped context values (see net/http).
type responderKey struct{}

// Responder answers exactly one inbound request id, once, from any goroutine.
// dispatchRequest builds one per request and injects it into the handler's
// context; a handler that defers (returns ErrDeferred) keeps its Responder and
// resolves it later — e.g. from the outbound event goroutine when a turn ends.
//
// Every answer for a given id — the synchronous success/error path taken by
// dispatchRequest and any deferred resolve — funnels through this one guarded
// handle, so "exactly one frame per id" is a structural invariant rather than a
// matter of handler discipline. Resolution goes through the transport's existing
// writeMu-guarded write path (writeSuccess / writeErrorWithData); no second write
// path is introduced.
type Responder struct {
	t *Transport
	// id is an owned copy of the request id bytes. The scanner reuses its buffer
	// on the next Scan and a deferred resolve may run many Scans later, so the id
	// must be copied at construction — never aliased from the inbound frame.
	id json.RawMessage
	// done elects the single resolver: CompareAndSwap(false, true) picks the one
	// goroutine that writes the frame. Leaf-level; never held across the write.
	done atomic.Bool
}

// Reply resolves the request with a success result. The first call writes one
// success frame through the serialized write path and returns nil; any later
// call writes nothing and returns ErrAlreadyResolved. A result that fails to
// marshal still yields exactly one frame (an internal-error frame), matching
// writeSuccess's existing fallback.
func (r *Responder) Reply(result any) error {
	if !r.done.CompareAndSwap(false, true) {
		return ErrAlreadyResolved
	}
	r.t.writeSuccess(r.id, result)
	return nil
}

// ReplyError resolves the request with an error frame. The first call writes one
// error frame and returns nil; any later call writes nothing and returns
// ErrAlreadyResolved. A nil e is a consumer bug: it resolves as a generic
// internal error rather than panicking, preserving both never-panic and
// exactly-one-frame.
func (r *Responder) ReplyError(e *Error) error {
	if !r.done.CompareAndSwap(false, true) {
		return ErrAlreadyResolved
	}
	if e == nil {
		r.t.writeError(r.id, CodeInternalError, "internal error")
		return nil
	}
	r.t.writeErrorWithData(r.id, e.Code, e.Message, e.Data)
	return nil
}

// ResponderFrom returns the *Responder dispatchRequest injected into ctx, or nil
// when ctx carries none (a notification handler, or a context not derived from
// dispatchRequest). A handler that intends to defer must nil-check before
// stashing.
func ResponderFrom(ctx context.Context) *Responder {
	r, _ := ctx.Value(responderKey{}).(*Responder)
	return r
}
