package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// methodSessionRequestPermission is the ACP agent→client method this adapter
// Calls to surface a permission decision to the host (ADR 027 divergence 2).
const methodSessionRequestPermission = "session/request_permission"

// The two ACP response outcome discriminants (host-facing wire strings). Every
// value other than outcomeSelected — including outcomeCancelled — routes deny.
const (
	outcomeSelected  = "selected"
	outcomeCancelled = "cancelled"
)

// permissionCaller is the outbound-request seam: a blocking JSON-RPC agent→client
// Call returning the raw result or the mapped error. *acp.Transport satisfies it.
// Defined at the consumer (accept-interfaces) and narrowed to the one method this
// adapter needs, so the Call can be scripted in tests without a live transport.
type permissionCaller interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// acpPermissionProxy answers claude's on-screen permission modal by asking the
// ACP host. On a permission-class modal it issues a blocking
// session/request_permission Call to the host, then routes the host's choice
// back into claude's live prompt as the correct keystroke (ADR 027 divergence
// 2). It is the ACP analogue of interactiveModalEmitterV2 but far thinner — one
// blocking Call in place of the mobile broadcast + nonce registry.
//
// Scope: the adapter ships UNWIRED and unit-tested in isolation, mirroring how
// interactiveModalEmitterV2 defers its live Session.Events() subscription to a
// separate wiring ticket (#708). The live modal-event subscription that drives
// Handle belongs to a follow-up (T7 #751 territory).
//
// SECURITY (ADR 025 default-safe): the only path to an allow keystroke is a
// decoded outcome:"selected" whose optionId is a member of the daemon-built
// option set; every other path — cancelled, transport error, ctx deadline
// (timeout), teardown-cancel, decode failure, forged/unknown optionId — routes
// deny (ESC). Deny is the failure mode, not a branch. No modal body / prompt /
// screen text and no host optionId is logged at any level; logs carry only
// content-free discriminants (event, session_id, the resolution result/reason)
// and, on a keystroke failure, the supervisor sentinel err.
type acpPermissionProxy struct {
	caller    permissionCaller
	kb        modalKeystroker
	sessionID string        // the ACP session id; a constructor param (adapter is unwired)
	timeout   time.Duration // Call deadline; the AC-3 "timeout ⇒ deny" source
	logger    *slog.Logger

	// inflight is the single outstanding round-trip, or nil. Touched ONLY by the
	// drain goroutine (Handle → handleShown/handleHidden), never by the
	// round-trip goroutine — so it needs no lock. The cross-goroutine arbiter is
	// the round-trip's atomic.Bool one-shot, not this field.
	inflight *permissionRoundTrip
}

// permissionRoundTrip is one surfaced modal's in-flight round-trip. resolved is
// the one-shot arbiter across the drain and round-trip goroutines:
// CompareAndSwap(false,true) wins exactly once, so the modal resolves at most
// once. cancel unblocks the in-flight Call on retirement; options is the surfaced
// set (membership + index→digit source of truth).
type permissionRoundTrip struct {
	resolved atomic.Bool
	cancel   context.CancelFunc
	options  []turnevent.PermissionOption
}

// newACPPermissionProxy constructs the adapter for sessionID. caller issues the
// session/request_permission Call; kb routes the resolving keystroke into the
// live prompt (*supervisor.Supervisor satisfies it, using only Answer/SendEsc).
// timeout bounds each Call (a never-answering host denies-and-unblocks after the
// window rather than wedging claude); logger is the ACP subcommand's stderr logger.
func newACPPermissionProxy(caller permissionCaller, kb modalKeystroker, sessionID string, timeout time.Duration, logger *slog.Logger) *acpPermissionProxy {
	return &acpPermissionProxy{
		caller:    caller,
		kb:        kb,
		sessionID: sessionID,
		timeout:   timeout,
		logger:    logger,
	}
}

// Handle is the modal-event sink: the deferred wiring drains the session's
// Session.Events() and calls it serially on one goroutine (mirrors
// interactiveModalEmitterV2.Handle). A permission-class ModalShown starts a
// round-trip; a ModalHidden retires the outstanding one; everything else — a
// non-permission ModalShown (trust/slash-picker/…) included — is a no-op.
//
// screenText is intentionally not a parameter: session/request_permission
// carries no prompt body and the option set is class-fixed, so the rendered
// screen text is unused (contrast the mobile surfacer, which sends the body).
// This keeps the deferred wiring from having to source Supervisor.ScreenSnapshot.
//
// Not safe for concurrent use — designed for the drain goroutine.
func (p *acpPermissionProxy) Handle(ctx context.Context, ev tuidriver.Event) {
	switch {
	case ev.Kind == tuidriver.EventKindPtyModalShown && ev.Modal == tuidriver.ModalClassPermission:
		p.handleShown(ctx, ev)
	case ev.Kind == tuidriver.EventKindPtyModalHidden:
		p.retireInflight()
	}
}

// handleShown starts a round-trip for a permission modal: build the four-option
// set (reusing PermissionRequestForClass so option order and the ACP kinds share
// one source of truth), retire any stale inflight (defensive — tui-driver's
// single-modal invariant normally leaves it nil), then spawn the round-trip
// goroutine. Spawning the Call on a fresh goroutine is the "off the read loop"
// guarantee (AC-5): the transport Serve read loop is the only reader of the Call
// response, so a Call on it would deadlock — this goroutine is neither.
func (p *acpPermissionProxy) handleShown(ctx context.Context, ev tuidriver.Event) {
	req, _, ok := modalbridge.PermissionRequestForClass(ev.Modal, "")
	if !ok {
		// Defensive: ok is always true for ModalClassPermission (the Handle gate).
		return
	}

	// tui-driver emits Hidden(old) before Shown(new) and shows one modal at a
	// time, so inflight is normally nil here; retire defensively so no prior
	// round-trip is ever orphaned.
	p.retireInflight()

	params := requestPermissionParams{
		SessionID: p.sessionID,
		Options:   toACPOptions(req.Options),
	}

	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	rt := &permissionRoundTrip{cancel: cancel, options: req.Options}
	p.inflight = rt

	p.logger.Debug("acp: issuing session/request_permission",
		"event", "acp_permission.request",
		"session_id", p.sessionID)

	go p.runRoundTrip(cctx, rt, params)
}

// runRoundTrip issues the blocking Call, then claims the one-shot and routes at
// most one keystroke. If retirement (a Hidden, or a superseding Shown) already
// claimed the round-trip, this routes nothing — the modal is gone. The cctx timer
// is always released on exit (a normal resolution never triggers retirement's
// cancel, so without this the deadline timer would linger until it fires).
func (p *acpPermissionProxy) runRoundTrip(cctx context.Context, rt *permissionRoundTrip, params requestPermissionParams) {
	defer rt.cancel()

	resp, err := p.caller.Call(cctx, methodSessionRequestPermission, params)
	if !rt.resolved.CompareAndSwap(false, true) {
		// Retirement won the one-shot (Hidden cancelled this Call). The modal is
		// already gone; routing a keystroke now would inject into a non-modal
		// claude. Route nothing (AC-4's "reply for an already-retired modal").
		return
	}
	p.route(rt, resp, err)
}

// route is the default-safe resolution switch, run only by the goroutine that
// won the one-shot. Exactly one keystroke is routed: Answer(digit) solely for a
// decoded selected with a membership-valid optionId; deny (ESC) for every other
// outcome. See the type doc for the full safety argument.
func (p *acpPermissionProxy) route(rt *permissionRoundTrip, resp json.RawMessage, callErr error) {
	if callErr != nil {
		// Timeout / transport error / teardown-cancel — deny is the failure mode.
		p.deny("call_err")
		return
	}

	var r requestPermissionResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		// Any decode ambiguity fails closed (never a grant).
		p.deny("decode_err")
		return
	}

	switch r.Outcome.Outcome {
	case outcomeSelected:
		idx := slices.IndexFunc(rt.options, func(o turnevent.PermissionOption) bool {
			return o.ID == r.Outcome.OptionID
		})
		if idx < 0 {
			// Forged / wrong-class option id: not locatable in the surfaced set,
			// so it can never route an allow keystroke.
			p.deny("forged_option")
			return
		}
		// digit = 1-based position in claude's display order; the index is the one
		// source of truth for both option order (on the wire) and keystroke digit.
		p.answer(strconv.Itoa(idx + 1))
	case outcomeCancelled:
		p.deny("cancelled")
	default:
		// Unknown / empty outcome (e.g. a null result) — fail closed.
		p.deny("unknown_outcome")
	}
}

// answer routes the host's chosen option into claude's prompt. The content-free
// decision trace is logged BEFORE actuating so the keystroke is the goroutine's
// last observable action on the success path. A keystroke error (no live session
// / mid-teardown) is best-effort: the round-trip is already resolved and there is
// nothing to roll back, so it is content-free Warn-logged and tolerated (mirrors
// modalResolverV2's best-effort actuation). digit derives from the daemon-built
// option order, never from host content, so it is safe to log.
func (p *acpPermissionProxy) answer(digit string) {
	p.logger.Debug("acp: permission resolved",
		"event", "acp_permission.resolved",
		"session_id", p.sessionID,
		"result", "answered")
	if err := p.kb.Answer(digit); err != nil {
		p.logger.Warn("acp: permission answer keystroke failed",
			"event", "acp_permission.keystroke_err",
			"session_id", p.sessionID,
			"err", err)
	}
}

// deny routes the safe default (ESC) into claude's prompt. reason is a
// content-free discriminant (call_err / decode_err / forged_option / cancelled /
// unknown_outcome) — never a host value. Log-before-actuate + best-effort
// keystroke error, mirroring answer.
func (p *acpPermissionProxy) deny(reason string) {
	p.logger.Debug("acp: permission resolved",
		"event", "acp_permission.resolved",
		"session_id", p.sessionID,
		"result", "denied",
		"reason", reason)
	if err := p.kb.SendEsc(); err != nil {
		p.logger.Warn("acp: permission deny keystroke failed",
			"event", "acp_permission.keystroke_err",
			"session_id", p.sessionID,
			"reason", reason,
			"err", err)
	}
}

// retireInflight claims and clears the outstanding round-trip. If it wins the
// one-shot it cancels the in-flight Call so runRoundTrip unblocks, sees the
// failed CompareAndSwap, and routes nothing. Runs only on the drain goroutine
// (so the inflight read/clear is race-free); the cross-goroutine handoff is the
// atomic one-shot.
func (p *acpPermissionProxy) retireInflight() {
	rt := p.inflight
	p.inflight = nil
	if rt != nil && rt.resolved.CompareAndSwap(false, true) {
		rt.cancel()
	}
}

// toACPOptions maps the neutral surfaced options onto ACP session/request_permission
// options. For the permission class every Kind is a valid ACP kind string
// (turnevent.PermissionOptionKind values ARE the ACP wire strings), so every
// emitted option carries a valid kind.
func toACPOptions(opts []turnevent.PermissionOption) []permissionOption {
	out := make([]permissionOption, len(opts))
	for i, o := range opts {
		out[i] = permissionOption{OptionID: o.ID, Name: o.Label, Kind: string(o.Kind)}
	}
	return out
}

// requestPermissionParams is the session/request_permission request params. It is
// consumer-owned (mirroring acp_turn_stream.go's locally-owned
// sessionUpdateParams), not shared in acpbridge.
//
// ToolCall is omitted: the modal-event path yields no gating tool-call id
// (PermissionRequestForClass leaves ToolCallID empty; ADR 027 divergence 2). The
// field is omitempty-reserved for a future ticket that correlates the gating
// ToolStart from the turn stream.
type requestPermissionParams struct {
	SessionID string              `json:"sessionId"`
	ToolCall  *permissionToolCall `json:"toolCall,omitempty"`
	Options   []permissionOption  `json:"options"`
}

// permissionOption is one ACP request-permission option: an optionId the host
// echoes back on selection, a human-readable name, and one of the four ACP kinds.
type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// permissionToolCall references the gating tool call by id. Reserved: never
// populated from the modal-event path (see requestPermissionParams.ToolCall).
type permissionToolCall struct {
	ToolCallID string `json:"toolCallId"`
}

// requestPermissionResponse decodes the session/request_permission reply. ACP
// nests the tagged outcome under an outer "outcome" object; the decode is
// default-safe, so any shape mismatch leaves Outcome zero and denies (never a
// silent grant).
type requestPermissionResponse struct {
	Outcome permissionOutcome `json:"outcome"`
}

// permissionOutcome is the nested outcome union: Outcome is "selected" |
// "cancelled"; OptionID names the chosen option on "selected".
type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}
