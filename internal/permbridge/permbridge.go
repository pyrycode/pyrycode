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
// path — a window that elapses with nobody able to answer, a lost caller, an
// unknown id — yields deny. Each entry arms a registry-owned window that resolves
// it to deny, and the window is spent only on approvals nobody can answer: an
// AnswerableFunc installed with SetAnswerable may report that somebody is still
// able to answer the approval, which buys exactly one more window and is re-asked
// at every expiry, so no entry outlives a window that reads unanswerable. With no
// report installed — the default — the window is the hard deadline it has always
// been. No allow can ever land after a deny.
//
// The registry therefore trusts two inputs: its caller (see Resolve), and the
// injected report's honesty about whether anybody is still waiting.
//
// The package is self-contained: it imports only the standard library and
// nothing from internal/, so no import cycle is possible (contrast modalbridge,
// which must document a relay-import hazard). It is also log-free — a pure data
// structure that emits nothing, so it cannot leak a Request's input, an id, or a
// deny message into logs; content-free decision logging is the consumer's job
// (the control-socket verb and the pyry mcp-approve subcommand).
package permbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Behavior discriminants claude accepts on a verdict (T1 spike contract).
const (
	BehaviorAllow = "allow"
	BehaviorDeny  = "deny"
)

const (
	maxAlwaysAllowBytes  = 16 << 10
	maxAlwaysAllowRules  = 16
	maxRenderedRuleBytes = 1024
)

// PermissionRule is one validated rule from claude's permission suggestions.
// RuleContent distinguishes an absent value from a present empty string because
// the latter renders as toolName().
type PermissionRule struct {
	ToolName    string  `json:"toolName"`
	RuleContent *string `json:"ruleContent,omitempty"`
}

// PermissionUpdate is one supported permission suggestion. ParseAlwaysAllow
// admits only addRules/allow updates, but the discriminants remain explicit for
// the grant path that will serialize the retained updates back to claude.
type PermissionUpdate struct {
	Type        string           `json:"type"`
	Rules       []PermissionRule `json:"rules"`
	Behavior    string           `json:"behavior"`
	Destination string           `json:"destination,omitempty"`
}

// AlwaysAllow is an immutable, validated always-allow offer. Its slices are
// package-owned; accessors return clones so outstanding modal state cannot be
// changed by a later caller.
type AlwaysAllow struct {
	updates []PermissionUpdate
	rules   []string
}

// Offered reports whether the value contains a complete validated offer.
func (a AlwaysAllow) Offered() bool {
	return len(a.updates) != 0
}

// Rules returns rendered display rules in source order.
func (a AlwaysAllow) Rules() []string {
	return append([]string(nil), a.rules...)
}

// Updates returns a deep copy of the validated updates in source order.
func (a AlwaysAllow) Updates() []PermissionUpdate {
	if a.updates == nil {
		return nil
	}
	out := make([]PermissionUpdate, len(a.updates))
	for i, update := range a.updates {
		out[i] = update
		out[i].Rules = make([]PermissionRule, len(update.Rules))
		for j, rule := range update.Rules {
			out[i].Rules[j] = rule
			if rule.RuleContent != nil {
				content := *rule.RuleContent
				out[i].Rules[j].RuleContent = &content
			}
		}
	}
	return out
}

type permissionUpdateInput struct {
	Type     string                `json:"type"`
	Rules    []permissionRuleInput `json:"rules"`
	Behavior string                `json:"behavior"`
}

type permissionRuleInput struct {
	ToolName    string          `json:"toolName"`
	RuleContent json.RawMessage `json:"ruleContent"`
}

// ParseAlwaysAllow validates the rule grants in a bounded Claude-authored
// suggestion batch. Directory and mode alternatives are omitted, never granted.
// Any invalid rule grant rejects the whole offer; rules are never truncated.
func ParseAlwaysAllow(raw json.RawMessage, suppressed bool) AlwaysAllow {
	offer, _ := parseAlwaysAllow(raw, suppressed)
	return offer
}

// parseAlwaysAllow returns a content-free status naming the first validation
// condition that rejected the batch. ParseAlwaysAllow deliberately discards it;
// the e2e_realclaude diagnostic shim exposes it only to the authenticated live
// test so untrusted suggestion bytes never enter ordinary logs.
func parseAlwaysAllow(raw json.RawMessage, suppressed bool) (AlwaysAllow, string) {
	if suppressed {
		return AlwaysAllow{}, "suppressed"
	}
	if len(raw) == 0 {
		return AlwaysAllow{}, "absent"
	}
	if len(raw) > maxAlwaysAllowBytes {
		return AlwaysAllow{}, "raw_over_16_kib"
	}
	var input []permissionUpdateInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return AlwaysAllow{}, "decode_failed"
	}
	if len(input) == 0 {
		return AlwaysAllow{}, "empty_batch"
	}

	updates := make([]PermissionUpdate, 0, len(input))
	rendered := make([]string, 0)
	totalRules := 0
	for i, update := range input {
		// Claude offers these alongside command rules. They are separate,
		// broader choices that this command-rule approval does not grant.
		if update.Type == "addDirectories" || update.Type == "setMode" {
			continue
		}
		if update.Type != "addRules" {
			return AlwaysAllow{}, fmt.Sprintf("update_%d_type_unsupported", i)
		}
		if update.Behavior == "" {
			return AlwaysAllow{}, fmt.Sprintf("update_%d_behavior_empty", i)
		}
		if update.Behavior != BehaviorAllow {
			return AlwaysAllow{}, fmt.Sprintf("update_%d_behavior_unsupported", i)
		}
		if len(update.Rules) == 0 {
			return AlwaysAllow{}, fmt.Sprintf("update_%d_rules_empty", i)
		}
		totalRules += len(update.Rules)
		if totalRules > maxAlwaysAllowRules {
			return AlwaysAllow{}, "rule_count_over_16"
		}
		validated := PermissionUpdate{Type: update.Type, Behavior: update.Behavior, Rules: make([]PermissionRule, len(update.Rules))}
		for j, rule := range update.Rules {
			if rule.ToolName == "" {
				return AlwaysAllow{}, fmt.Sprintf("update_%d_rule_%d_tool_name_empty", i, j)
			}
			stored := PermissionRule{ToolName: rule.ToolName}
			text := rule.ToolName
			if rule.RuleContent != nil {
				if bytes.Equal(bytes.TrimSpace(rule.RuleContent), []byte("null")) {
					return AlwaysAllow{}, fmt.Sprintf("update_%d_rule_%d_content_null", i, j)
				}
				var content string
				if err := json.Unmarshal(rule.RuleContent, &content); err != nil {
					return AlwaysAllow{}, fmt.Sprintf("update_%d_rule_%d_content_not_string", i, j)
				}
				stored.RuleContent = &content
				text += "(" + content + ")"
			}
			if len(text) > maxRenderedRuleBytes {
				return AlwaysAllow{}, fmt.Sprintf("update_%d_rule_%d_rendered_over_1024_bytes", i, j)
			}
			validated.Rules[j] = stored
			rendered = append(rendered, text)
		}
		updates = append(updates, validated)
	}
	if len(updates) == 0 {
		return AlwaysAllow{}, "no_rule_grants"
	}
	return AlwaysAllow{updates: updates, rules: rendered}, "offered"
}

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
// byte-verbatim — an allow echoes it back as Verdict.UpdatedInput. The remaining
// optional fields are copied only from the corresponding can_use_tool ask fields;
// they are never derived from Input. RequiresUserInteraction is daemon-internal
// answer eligibility rather than display data and is therefore excluded from
// JSON. None of these fields changes the registry's verdict arbitration.
type Request struct {
	ToolName                string          `json:"tool_name"`
	Input                   json.RawMessage `json:"input"`
	ToolUseID               string          `json:"tool_use_id"`
	DecisionReason          json.RawMessage `json:"decision_reason,omitempty"`
	DecisionReasonType      string          `json:"decision_reason_type,omitempty"`
	BlockedPath             string          `json:"blocked_path,omitempty"`
	Description             string          `json:"description,omitempty"`
	DefaultToNo             bool            `json:"default_to_no,omitempty"`
	RequiresUserInteraction bool            `json:"-"`
	AlwaysAllow             AlwaysAllow     `json:"-"`
}

// Verdict is the allow/deny decision claude accepts. The omitempty tags give the
// two disjoint wire shapes: allow → {"behavior":"allow","updatedInput":{…}}
// with optional session-scoped updatedPermissions; deny →
// {"behavior":"deny","message":"…"}. Construct via Allow, AllowAlways, or Deny.
type Verdict struct {
	Behavior           string          `json:"behavior"`
	UpdatedInput       json.RawMessage `json:"updatedInput,omitempty"`       // allow only
	UpdatedPermissions json.RawMessage `json:"updatedPermissions,omitempty"` // session allow only
	Message            string          `json:"message,omitempty"`            // deny only
}

// Allow builds an allow verdict echoing updatedInput (the request's Input,
// possibly modified by the resolver — a #1080 policy decision).
func Allow(updatedInput json.RawMessage) Verdict {
	return Verdict{Behavior: BehaviorAllow, UpdatedInput: updatedInput}
}

// AllowAlways builds an allow verdict from a daemon-retained validated offer.
// Every destination is rewritten to session; an unavailable offer preserves the
// plain allow shape. The typed updates contain only strings, so marshal failure
// is unreachable with the current value, but falling back grants no rule if that
// contract changes.
func AllowAlways(updatedInput json.RawMessage, offer AlwaysAllow) Verdict {
	updates := offer.Updates()
	if len(updates) == 0 {
		return Allow(updatedInput)
	}
	for i := range updates {
		updates[i].Destination = "session"
	}
	raw, err := json.Marshal(updates)
	if err != nil {
		return Allow(updatedInput)
	}
	return Verdict{
		Behavior:           BehaviorAllow,
		UpdatedInput:       updatedInput,
		UpdatedPermissions: raw,
	}
}

// Deny builds a deny verdict carrying a human-readable reason.
func Deny(message string) Verdict {
	return Verdict{Behavior: BehaviorDeny, Message: message}
}

// pending is one parked approval request. ch is buffered(1) and receives EXACTLY
// ONE verdict, written by whichever of {Resolve, timer} wins the delete-under-
// lock one-shot; the loser writes nothing. timer is the fail-closed window,
// stopped by a winning Resolve and re-armed in place by expire for as long as the
// report says somebody can still answer. The timer field is written exactly once,
// in Register under mu, and never reassigned — expire calls Reset on the same
// object — which is why resolve may read it outside mu.
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

// Await blocks until the request is resolved and returns its verdict. With no
// AnswerableFunc installed — the default — it is guaranteed to return within the
// Register timeout, because the registry-owned timer always delivers a deny if
// nothing else resolves the entry first. With one installed the bound is
// conditional on that report: Await returns within one window of the first
// reading that says nobody can answer this approval.
func (p *Pending) Await() Verdict {
	return <-p.ch
}

// AnswerableFunc reports whether the exact approval generation parked under id
// still has somebody able to answer it. req is the immutable request captured by
// that registration. It is consulted only when a window elapses on a live entry,
// and a true reading buys exactly one more window before the question is asked
// again — so it is read as a level, never latched as an edge.
//
// The contract a report must satisfy: answer false once nobody is waiting on this
// approval. A report that always answers true removes the fail-closed bound
// entirely, including for a caller that registered and never Awaited.
//
// It is called with no registry lock held and must not panic; the registry has no
// recovery, and mapping a panic to "not answerable" would make a wiring bug read
// as every approval silently denying. Guarding a not-yet-wired report is the
// injection site's job.
type AnswerableFunc func(id string, req Request) bool

// Registry is the in-memory pending-approval store, keyed by an opaque id (a
// tool-use id — a correlation key, not a credential). mu is a leaf lock: held
// only around O(1) map ops and the answerable read, never nested with another
// lock, and never across the channel send or the AnswerableFunc call.
type Registry struct {
	mu         sync.Mutex
	pending    map[string]*pending
	answerable AnswerableFunc
}

// New returns an empty Registry ready for use, with no AnswerableFunc installed:
// every window is a hard fail-closed deadline until SetAnswerable says otherwise.
func New() *Registry {
	return &Registry{pending: make(map[string]*pending)}
}

// SetAnswerable installs the liveness report, or clears it with nil. It is a
// setter rather than a New option because a composition root builds the registry
// long before whatever can answer the question exists; the report is read at
// expiry, not captured at Register, so calling it while entries are already
// parked is safe and takes effect on their next window. nil is a meaningful
// value, not an error — an absent report reads as "nobody can answer", which is
// the fail-closed direction.
func (r *Registry) SetAnswerable(ask AnswerableFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answerable = ask
}

// Register parks req under id, arms the fail-closed deadline, and returns the
// caller's handle. It rejects an empty id and a duplicate live id with
// ErrDuplicateID (the caller then default-denies). A non-positive timeout fires
// the timer ≈immediately, which is safe: the entry simply resolves to deny.
//
// The whole insert — arming the timer and writing the map — happens under mu, so
// even a 0-duration timer that fires instantly blocks in expire on mu.Lock()
// until the entry is fully installed: there is no fire-before-install race, and
// the lock establishes the happens-before for the timer's later Stop.
//
// timeout is a re-check interval rather than a hard deadline whenever an
// AnswerableFunc is installed: expire re-arms this same window for as long as the
// report says the approval can still be answered. It stays the only knob — an
// extension re-arms the duration Register was handed, never a different one.
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
	p.timer = time.AfterFunc(timeout, func() { r.expire(id, timeout) })
	r.pending[id] = p
	return &Pending{ch: p.ch}, nil
}

// Resolve satisfies the pending request id with v and returns true if it
// resolved a live entry, or false if the id is unknown or already resolved (a
// safe no-op — AC-4). Delegates to the shared one-shot; the winning caller is the
// sole writer of the verdict.
func (r *Registry) Resolve(id string, v Verdict) bool {
	return r.resolve(id, nil, v)
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

// expire runs on the entry's timer goroutine when its window elapses. It decides
// between denying and buying one more window; it never writes a verdict itself,
// so resolve stays the sole arbiter of who satisfies p.ch.
//
// The snapshot-release-ask ordering is deliberate. mu is released before ask,
// because a production report is a blocking cross-goroutine round-trip and
// holding a leaf lock across it would stall every concurrent Register, Resolve
// and Lookup. That makes the check-then-mutate non-atomic, which the re-check
// under mu in the final step makes safe: a Resolve landing during the ask deletes
// the entry, this finds it gone, and no timer is ever re-armed for a dead entry.
// The reverse interleaving is equally safe — the Reset lands under mu before the
// delete, and resolve's Stop cancels it.
//
// Reset on the same timer, rather than a fresh AfterFunc, is what keeps p.timer
// written once: assigning it here would race resolve's read of that field, which
// happens outside mu. The Reset also stays strictly after ask returns, so the
// next firing cannot be scheduled while this callback is still inside the report
// — at most one expire is ever in flight per entry, and a slow report stretches
// the effective window instead of overlapping with itself.
func (r *Registry) expire(id string, window time.Duration) {
	r.mu.Lock()
	p, live := r.pending[id]
	ask := r.answerable
	r.mu.Unlock()

	// A dead entry is the common negative: it was already resolved, so the report
	// cannot change the outcome and must not be paid for.
	if !live {
		return
	}
	// A non-positive window is never extended: Register promises it denies
	// ≈immediately, and re-arming one would spin on the report as fast as the
	// scheduler allows.
	if ask == nil || window <= 0 {
		r.resolve(id, p, Deny(reasonTimeout))
		return
	}
	if !ask(id, p.req) {
		r.resolve(id, p, Deny(reasonTimeout))
		return
	}

	r.mu.Lock()
	if current, still := r.pending[id]; still && current == p {
		current.timer.Reset(window)
	}
	r.mu.Unlock()
}

// resolve is the security core: the single internal one-shot both Resolve and
// the timer callback funnel through. A timer supplies its expected entry identity
// so a stale callback cannot resolve a newer registration that reused id; an
// explicit Resolve supplies nil to target whichever generation is currently live.
// delete(pending, id) under mu is the sole arbiter — exactly one eligible caller
// finds the entry present, deletes it, and becomes the single writer of p.ch.
// Because ch is buffered(1) with a provably single writer, the send never blocks,
// so it is safe outside the lock (leaf mutex; never held across a channel send).
func (r *Registry) resolve(id string, expected *pending, v Verdict) bool {
	r.mu.Lock()
	p, ok := r.pending[id]
	if !ok || expected != nil && p != expected {
		r.mu.Unlock()
		return false
	}
	delete(r.pending, id)
	r.mu.Unlock()

	p.timer.Stop() // idempotent; harmless when called from the timer's own goroutine
	p.ch <- v
	return true
}
