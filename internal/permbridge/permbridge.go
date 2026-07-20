// Package permbridge is an in-memory registry of pending tool-approval requests
// for the Streamrunner interactive permission bridge. A non-YOLO headless claude
// spawned with --permission-prompt-tool synchronously blocks on a registered MCP
// tool's allow/deny JSON for the full duration of a pending approval, so a parked
// request is real owed-silence of unbounded length. The registry parks such a
// request under an opaque id, hands the blocked caller a Pending completer to
// Await, and lets a resolver satisfy it with an allow or deny Verdict.
//
// Fail-closed is the security-critical core: allow is reachable only through an
// explicit Resolve(id, Allow(...)) that wins the one-shot; every other terminal
// path — a deadline that elapses, a lost caller, an unknown id — yields deny.
// Each entry arms a registry-owned timeout that resolves it to deny, so no entry
// outlives its deadline and no allow can ever land after a deny.
//
// The package is self-contained: it imports only the standard library and
// nothing from internal/, so no import cycle is possible (contrast modalbridge,
// which must document a relay-import hazard). It is also log-free — a pure data
// structure that emits nothing, so it cannot leak a Request's input, an id, or a
// deny message into logs; content-free decision logging is the consumer's job
// (the control-socket verb and the pyry mcp-approve subcommand).
package permbridge

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// Behavior discriminants claude accepts on a verdict (T1 spike contract).
const (
	BehaviorAllow = "allow"
	BehaviorDeny  = "deny"
)

// reasonTimeout is the deny message the fail-closed timer path uses. A fixed
// constant — never host-derived content — so the timeout deny leaks nothing.
const reasonTimeout = "approval request timed out"

// ErrDuplicateID rejects a Register whose id cannot open a fresh pending slot:
// an id already live in the registry, or an empty id (not a usable correlation
// key). Matchable with errors.Is; the caller default-denies. Mapping it to a
// wire error is the consumer's job.
var ErrDuplicateID = errors.New("permbridge: duplicate or empty approval id")

// Request is the tool-approval request claude sends (T1 spike contract). Input
// is the opaque tool-input object, carried as json.RawMessage so it round-trips
// byte-verbatim — an allow echoes it back as Verdict.UpdatedInput.
type Request struct {
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

// Verdict is the allow/deny decision claude accepts. The omitempty tags give the
// two disjoint wire shapes: allow → {"behavior":"allow","updatedInput":{…}};
// deny → {"behavior":"deny","message":"…"}. Construct via Allow / Deny.
type Verdict struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"` // allow only
	Message      string          `json:"message,omitempty"`      // deny only
}

// Allow builds an allow verdict echoing updatedInput (the request's Input,
// possibly modified by the resolver — a #1080 policy decision).
func Allow(updatedInput json.RawMessage) Verdict {
	return Verdict{Behavior: BehaviorAllow, UpdatedInput: updatedInput}
}

// Deny builds a deny verdict carrying a human-readable reason.
func Deny(message string) Verdict {
	return Verdict{Behavior: BehaviorDeny, Message: message}
}

// pending is one parked approval request. ch is buffered(1) and receives EXACTLY
// ONE verdict, written by whichever of {Resolve, timer} wins the delete-under-
// lock one-shot; the loser writes nothing. timer is the fail-closed deadline,
// stopped by a winning Resolve.
type pending struct {
	req   Request
	ch    chan Verdict
	timer *time.Timer
}

// Pending is the caller's handle on a parked request: block on Await for the
// verdict.
type Pending struct {
	ch <-chan Verdict
}

// Await blocks until the request is resolved and returns its verdict. It is
// guaranteed to return within the Register timeout, because the registry-owned
// timer always delivers a deny if nothing else resolves the entry first.
func (p *Pending) Await() Verdict {
	return <-p.ch
}

// Registry is the in-memory pending-approval store, keyed by an opaque id (a
// tool-use id — a correlation key, not a credential). mu is a leaf lock: held
// only around O(1) map ops, never nested with another lock, and never across the
// channel send.
type Registry struct {
	mu      sync.Mutex
	pending map[string]*pending
}

// New returns an empty Registry ready for use.
func New() *Registry {
	return &Registry{pending: make(map[string]*pending)}
}

// Register parks req under id, arms the fail-closed deadline, and returns the
// caller's handle. It rejects an empty id and a duplicate live id with
// ErrDuplicateID (the caller then default-denies). A non-positive timeout fires
// the timer ≈immediately, which is safe: the entry simply resolves to deny.
//
// The whole insert — arming the timer and writing the map — happens under mu, so
// even a 0-duration timer that fires instantly blocks in resolve on mu.Lock()
// until the entry is fully installed: there is no fire-before-install race, and
// the lock establishes the happens-before for the timer's later Stop.
func (r *Registry) Register(id string, req Request, timeout time.Duration) (*Pending, error) {
	if id == "" {
		return nil, ErrDuplicateID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pending[id]; exists {
		return nil, ErrDuplicateID
	}
	p := &pending{req: req, ch: make(chan Verdict, 1)}
	p.timer = time.AfterFunc(timeout, func() { r.resolve(id, Deny(reasonTimeout)) })
	r.pending[id] = p
	return &Pending{ch: p.ch}, nil
}

// Resolve satisfies the pending request id with v and returns true if it
// resolved a live entry, or false if the id is unknown or already resolved (a
// safe no-op — AC-4). Delegates to the shared one-shot; the winning caller is the
// sole writer of the verdict.
func (r *Registry) Resolve(id string, v Verdict) bool {
	return r.resolve(id, v)
}

// Lookup returns the parked Request for id without resolving it — the read seam
// #1080 uses to build Allow(req.Input). An unknown id yields (Request{}, false),
// a safe no-op.
func (r *Registry) Lookup(id string) (Request, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pending[id]
	if !ok {
		return Request{}, false
	}
	return p.req, true
}

// resolve is the security core: the single internal one-shot both Resolve and
// the timer callback funnel through. delete(pending, id) under mu is the sole
// arbiter — exactly one caller finds the entry present, deletes it, and becomes
// the single writer of p.ch; every other caller finds it gone and is a no-op.
// Because ch is buffered(1) with a provably single writer, the send never blocks,
// so it is safe outside the lock (leaf mutex; never held across a channel send).
func (r *Registry) resolve(id string, v Verdict) bool {
	r.mu.Lock()
	p, ok := r.pending[id]
	if !ok {
		r.mu.Unlock()
		return false
	}
	delete(r.pending, id)
	r.mu.Unlock()

	p.timer.Stop() // idempotent; harmless when called from the timer's own goroutine
	p.ch <- v
	return true
}
