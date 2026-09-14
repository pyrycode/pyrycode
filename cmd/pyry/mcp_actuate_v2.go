package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// mcpChildActuator is the narrow consumer-side view of the two actuation methods
// #2418 landed on *streamsup.Runner. Asserted at the call site exactly as
// mcpStatusQuerier is, and for that seam's reason: the methods are on the concrete
// runner rather than on sessions.Runner, and widening that interface would pull every
// implementation and test double into this slice.
//
// NEITHER METHOD IS SELF-GUARDING. Both say so in their own doc blocks: membership and
// authorization belong to the caller boundary, and possession of a server name is
// treated as sufficient below this seam. What makes that safe is this file — nothing
// else in the daemon reaches these methods from the wire.
type mcpChildActuator interface {
	ReconnectMCPServer(ctx context.Context, serverName string) bool
	SetMCPServerEnabled(ctx context.Context, serverName string, enabled bool) bool
}

const (
	// classMCPReconnect / classMCPToggle are the audit ModalClass values that tell the
	// two actuation arms apart in a forensic record, and they are valued as the wire
	// type constants rather than as new spellings so a reader joins a record to a frame
	// without a mapping table. Both are daemon-authored compile-time strings.
	classMCPReconnect = protocol.TypeMCPReconnect
	classMCPToggle    = protocol.TypeMCPToggle

	// mcpAuditTargetMax bounds the asked-for server name where it enters the audit
	// record — the only place it goes. See auditActuation for the threat; 128 bytes is
	// chosen against what an MCP server name actually is (a short identifier), not
	// against what the transport permits.
	mcpAuditTargetMax = 128

	// mcpActuationTimeout bounds ONE WHOLE ACTUATION, not one child round trip.
	// actuateMCP's block states that a child which stays alive and simply never answers
	// is bounded by the caller's context alone — child death and replacement are
	// covered, silence is not — and an accepted action makes three separate round trips
	// (membership read, actuation, post-acknowledgement read). One figure for all three
	// bounds the relay worker's occupancy at one number rather than three, and a budget
	// consumed by a slow earlier step fails in the refusing direction: nothing is
	// actuated that was not reached.
	mcpActuationTimeout = 30 * time.Second
)

// mcpActuatorV2 is the cmd/pyry implementation of relay.MCPActuator: the per-device
// authorization boundary for an inbound mcp_reconnect / mcp_toggle, the record of every
// decision it makes, and the reader of the post-acknowledgement status the accepted
// answer carries. It is the composition-root binding that discharges that seam's
// written ordering obligation — nothing may be wired to V2SessionConfig.MCPActuator
// until this gate exists, because the relay handler applies no authorization at all.
//
// It is questionResolverV2's sibling and copies that gate's shape: a fail-closed
// eligibility predicate, the allow conjunction behind it as defence in depth, exactly
// one audit record per decision, and no log record of its own. It departs from it in
// ONE place — ORDERING — and the departure is the substance of the slice. See actuate.
//
// Both methods run on the addressed connection's appFrameWorker rather than on the
// manager's Run goroutine, because they wait on a child. They take no lock, so this
// file establishes no lock ordering, and they spawn no goroutine.
//
// SECURITY: the two remote-authored strings that reach this type are not equally
// checked anywhere upstream. ConversationID has already passed KnownConversation, so it
// names a conversation this daemon hosts, and it stays a lookup key — never returned as
// the answer's id, never logged, never joined into a path. ServerName has passed
// nothing, anywhere: this type is its sole validator. Every string in the returned
// payload is claude-authored and reaches no record.
type mcpActuatorV2 struct {
	// statusFor reads the servers the conversation's bound eligible child CURRENTLY
	// reports. It is a control request written to that child's stdin, not a cached
	// inventory read — resolveBoundMCPStatus states there is no retained-status
	// fallback — which is the whole reason the gate below runs ahead of it.
	//
	// nil ⇒ every actuation refuses. The daemon has no live-child resolution
	// (foreground / PTY), so there is nothing to actuate against.
	statusFor func(ctx context.Context, convID string) (protocol.MCPStatusPayload, bool)

	// actuatorFor resolves the conversation's bound eligible child as the two
	// primitives above. A daemon-local registry and pool read: it contacts no child,
	// which is what lets it sit ahead of the status read and refuse an unbound
	// conversation without making any child work.
	//
	// nil ⇒ every actuation refuses, statusFor's reason.
	actuatorFor func(convID string) (mcpChildActuator, bool)

	logger *slog.Logger
}

// newMCPActuatorV2 wires the gate to the two live-child seams and the daemon logger.
// Both seams are constructor parameters rather than post-construction fields — the
// split questionResolverV2 needs for its bridge does not apply here, because both
// closures are built at the composition root before the manager exists.
//
// It returns a CONCRETE NON-NIL pointer and is called unconditionally, which is the
// posture V2SessionConfig.MCPActuator's own block requires: assigning a nil-valued
// concrete type would leave that interface field non-nil, so the relay would admit the
// frame and call a method on a nil pointer — and internal/relay has no recover(), so
// that is a remote-triggerable crash rather than a refusal. Inertness therefore lives
// inside this type, where a nil seam is a plain field compare.
func newMCPActuatorV2(
	statusFor func(ctx context.Context, convID string) (protocol.MCPStatusPayload, bool),
	actuatorFor func(convID string) (mcpChildActuator, bool),
	logger *slog.Logger,
) *mcpActuatorV2 {
	return &mcpActuatorV2{statusFor: statusFor, actuatorFor: actuatorFor, logger: logger}
}

// Reconnect reconnects the named MCP server in the named conversation for an eligible
// device, answering with the status its child reports AFTER acknowledging the
// reconnect. Every refusal returns the zero payload and false, which is the whole
// refusal vocabulary relay.MCPActuator publishes.
func (a *mcpActuatorV2) Reconnect(
	ctx context.Context,
	p protocol.MCPReconnectPayload,
	dev *devices.Device,
) (protocol.MCPStatusPayload, bool) {
	return a.actuate(ctx, dev, classMCPReconnect, p.ConversationID, p.ServerName,
		func(ctx context.Context, child mcpChildActuator, serverName string) bool {
			return child.ReconnectMCPServer(ctx, serverName)
		})
}

// SetEnabled moves the named MCP server to p.Enabled. Same ordering, same audit and
// same two-way answer as Reconnect, which this block does not restate.
//
// p.Enabled IS PASSED THROUGH AND NEVER INTERPRETED. It is not compared against the
// current state and false is not treated specially: the request carries the state to
// move TO, which is why #2419 named the method for the flag rather than for the wire's
// `mcp_toggle`. An absent key having decoded as false is protocol.MCPTogglePayload's
// wire-shape property, and it decodes to the NON-ESCALATING direction — a truncation or
// a client that forgot the field can only ever turn a server off.
//
// Reading the current state to derive a flip is the one thing this method must not do.
// The value the gate authorized is the one the device sent; substituting a value read
// from the child would authorize one action and perform another.
func (a *mcpActuatorV2) SetEnabled(
	ctx context.Context,
	p protocol.MCPTogglePayload,
	dev *devices.Device,
) (protocol.MCPStatusPayload, bool) {
	return a.actuate(ctx, dev, classMCPToggle, p.ConversationID, p.ServerName,
		func(ctx context.Context, child mcpChildActuator, serverName string) bool {
			return child.SetMCPServerEnabled(ctx, serverName, p.Enabled)
		})
}

// actuate is both arms: the ordering, the audit and the answer. Only the class constant
// and the one child call differ above, so they live in the arms and everything a later
// third verb would have to re-derive lives here.
//
// THE GATE RUNS FIRST, AHEAD OF THE MEMBERSHIP LOOK-UP, and that single fact is the
// substance of this file. questionResolverV2.admit looks up and then gates; this
// reverses it deliberately. That resolver's look-up is a map read of daemon-local state
// — free and invisible — where this one is a control request written to a live child's
// stdin. Ordering it ahead of the gate would let ANY paired device make a child work on
// demand, and would widen the timing signal MCPActuator's block flags as the merged
// reject's weak edge. Nothing is lost by the reversal: every refusal here audits, so
// there is no no-security-decision-was-made arm for a prior look-up to protect.
//
//  1. FAIL-CLOSED ELIGIBILITY GATE. A nil device (no authenticated device on the
//     connection), an unauthenticated one and one whose opt-in bit is unset all deny.
//     Both predicates are total over a nil receiver. NO SEAM IS TOUCHED, so a refused
//     device causes no control request of any kind — membership look-up included.
//  2. THE ALLOW CONJUNCTION, defence in depth, at the point ResolveAnswer applies it
//     and for its reason: it keeps the fail-closed conjunction in its single
//     unit-tested place, and it is the call that would keep denying correctly if step 1
//     were ever moved.
//  3. SEAMS. A nil seam is a daemon with no live-child resolution. It sits BELOW the
//     gate — the inverse of admit's step 1 — so that every refusal past the gate writes
//     a record and the audit obligation is total rather than conditional on wiring.
//  4. THE DEADLINE, derived from the caller's ctx and never from context.Background():
//     the seam requires that manager shutdown terminate the wait.
//  5. BOUND ELIGIBLE CHILD, a registry and pool read that contacts nothing.
//  6. MEMBERSHIP. The first control request. A failed read and a name the report does
//     not contain are one refusal apart from each other only in the record.
//  7. ACTUATE, then record, then READ AGAIN. The answer is the second read.
//
// Steps 3 and 5-7 all audit denied and answer with the same merged reject. That the
// wire cannot tell them apart is #2419's published contract, and the record is the only
// place they stay apart — which is what keeps the verb from reporting whether a named
// server exists or whether the asking device is privileged.
func (a *mcpActuatorV2) actuate(
	ctx context.Context,
	dev *devices.Device,
	class string,
	convID string,
	serverName string,
	do func(ctx context.Context, child mcpChildActuator, serverName string) bool,
) (protocol.MCPStatusPayload, bool) {
	// Steps 1 and 2. Before every seam, so an ineligible device reaches no child.
	if !dev.MayAnswerRemotePermission() ||
		!devices.AuthorizeRemotePermission(dev, devices.OutcomeAllow) {
		a.auditActuation(dev, class, convID, serverName, audit.OutcomeDeniedUnauthorized)
		return protocol.MCPStatusPayload{}, false
	}

	// Step 3.
	if a.statusFor == nil || a.actuatorFor == nil {
		a.auditActuation(dev, class, convID, serverName, audit.OutcomeDenied)
		return protocol.MCPStatusPayload{}, false
	}

	// Step 4.
	ctx, cancel := context.WithTimeout(ctx, mcpActuationTimeout)
	defer cancel()

	// Step 5.
	child, ok := a.actuatorFor(convID)
	if !ok {
		a.auditActuation(dev, class, convID, serverName, audit.OutcomeDenied)
		return protocol.MCPStatusPayload{}, false
	}

	// Step 6. The matched row's Name is what the child is asked to act on — equal to
	// serverName by construction, so this is provenance rather than transformation: the
	// value that reaches the child's control stream is one the child itself authored.
	reported, ok := a.boundServerName(ctx, convID, serverName)
	if !ok {
		a.auditActuation(dev, class, convID, serverName, audit.OutcomeDenied)
		return protocol.MCPStatusPayload{}, false
	}

	// Step 7. One bool collapses the child refusing, erroring and never answering,
	// which is actuateMCP's published contract and needs no richer reader here.
	if !do(ctx, child, reported) {
		a.auditActuation(dev, class, convID, serverName, audit.OutcomeDenied)
		return protocol.MCPStatusPayload{}, false
	}

	// THE RECORD IS WRITTEN BEFORE THE POST-ACKNOWLEDGEMENT READ, not after. The
	// decision was made the moment the child accepted, and a read that burns whatever
	// is left of the budget must not delay recording it. The consequence is deliberate:
	// a read that then fails answers the wire with the merged reject while this record
	// says allowed, and that pair is the truth — the action happened and the daemon
	// could not report its result. Recording after the read would lose the record for
	// an action that happened; recording before the actuation would claim one that
	// never did, which is the worse lie.
	a.auditActuation(dev, class, convID, serverName, audit.OutcomeAllowed)

	// The answer is A SECOND READ, taken after the acknowledgement, and never the one
	// step 6 took: MCPActuator requires it, and the relay deliberately cannot take it
	// itself because a read it issued could not be the post-ack one. What comes back is
	// a SNAPSHOT AT THAT MOMENT and is returned as read — a server reconnected an
	// instant earlier commonly reports pending rather than connected, both of which
	// appear in the committed mcp_status capture, and this path neither waits nor polls
	// for one to settle.
	payload, ok := a.statusFor(ctx, convID)
	if !ok {
		return protocol.MCPStatusPayload{}, false
	}
	return payload, true
}

// boundServerName reports the child's own spelling of serverName, or false when the
// read failed or the server is not in what that child CURRENTLY reports.
//
// EXACT MEMBERSHIP IS THE SOLE VALIDATOR of a string that has passed nothing anywhere,
// and it is an allow-list rather than a shape guess. A charset or length rule would be
// a rule invented here about names claude owns, and it would refuse a legitimately
// named server; equality against names the child itself just reported cannot. There is
// deliberately no separate empty-name branch: an absent server_name key decodes to "",
// which no real report contains, so membership already refuses it.
//
// The name never becomes a path component, never reaches a shell and never reaches a
// log — the audit record is its only sink, and auditActuation bounds it there.
func (a *mcpActuatorV2) boundServerName(ctx context.Context, convID, serverName string) (string, bool) {
	status, ok := a.statusFor(ctx, convID)
	if !ok {
		return "", false
	}
	for _, server := range status.Servers {
		if server.Name == serverName {
			return server.Name, true
		}
	}
	return "", false
}

// auditActuation writes exactly one terminal-decision record for one actuation,
// carrying only the conversation, the asked-for server, the non-secret device identity
// (empty for a nil device — auditQuestion's pattern, which this is the actuation-family
// twin of) and the decided outcome. Source is always remote: every frame this file
// judges arrived from a client.
//
// ModalID IS DELIBERATELY EMPTY, auditMint's posture. An actuation has no one-time
// nonce to name, and the conversation rides the field that means a conversation rather
// than being stuffed into a field an operator reads as a modal id.
//
// TARGET IS THE NAME THE DEVICE ASKED FOR, never the resolved one — on a refusal there
// is no resolved one, and a record saying a device was refused "something" is not
// forensically useful. That makes it the one remote-authored value in the record, so it
// is BOUNDED here through truncateForLog: the transport's AEAD frame cap is the only
// upstream bound on its length, this path is reachable before the gate's refusal is
// recorded, and control.SlogTee tees every record into internal/control's bounded ring
// — so an unbounded field would let any paired device, privileged or not, evict the
// operator's own recent forensic history by padding it and retrying. The sibling
// injection threat needs no code: the daemon's handler is slog.TextHandler, which
// quotes any value carrying a space, an `=` or a control character, so the field cannot
// forge a second attribute.
//
// SECURITY: audit.Entry has no field that can hold a plain device token, and this call
// supplies none. Nor does it supply a claude-authored value: the server config, argv,
// environment, status, scope, version and error text are all out of reach here — the
// only server-shaped string in scope is the one the device sent.
func (a *mcpActuatorV2) auditActuation(
	dev *devices.Device,
	class string,
	convID string,
	serverName string,
	outcome audit.Outcome,
) {
	var deviceHash, deviceLabel string
	if dev != nil {
		deviceHash = dev.TokenHash
		deviceLabel = dev.Name
	}
	audit.Log(a.logger, audit.Entry{
		DeviceHash:     deviceHash,
		DeviceLabel:    deviceLabel,
		ModalClass:     class,
		ConversationID: convID,
		Target:         truncateForLog(serverName, mcpAuditTargetMax),
		Outcome:        outcome,
		Source:         audit.SourceRemote,
	})
}

// boundMCPChildActuator builds the actuatorFor seam over the daemon's conversation
// registry and session pool, the shape mcpStatusFor already has and beside it at the
// composition root. It reaches resolveBoundRunner unchanged, so the cross-conversation
// isolation that function enforces — an unbound conversation never falls through to the
// bootstrap session — covers this path without a second copy of the rule.
//
// The assertion is the narrow consumer-side interface rather than sessions.Runner, and
// a runner that does not satisfy it refuses: a non-streamsup runner has no actuation to
// perform, which is the same reading resolveBoundMCPStatus gives a runner that is not
// an mcpStatusQuerier.
//
// nil convReg or pool returns a nil seam, so a daemon with no session composition
// leaves the gate refusing rather than dereferencing — mcpStatusFor's contract, kept
// identical so the two seams are nil together.
func boundMCPChildActuator(
	convReg *conversations.Registry,
	pool *sessions.Pool,
) func(convID string) (mcpChildActuator, bool) {
	if convReg == nil || pool == nil {
		return nil
	}
	return func(convID string) (mcpChildActuator, bool) {
		runner, _, ok := resolveBoundRunner(convReg, pool, convID)
		if !ok {
			return nil, false
		}
		actuator, ok := runner.(mcpChildActuator)
		if !ok {
			return nil, false
		}
		return actuator, true
	}
}
