package devices

import "time"

// ValidateResult is why Validate accepted or refused a presented token. The
// zero value denies, so a default-constructed result — or a future Validate
// path that returns before deciding — fails closed, the same discipline
// RemotePermissionOutcome uses below.
type ValidateResult int

const (
	// ValidateUnknownToken: no device matches the presented token (this also
	// covers the empty plain). The fail-closed zero value.
	ValidateUnknownToken ValidateResult = iota
	// ValidateAccepted: a device matches and its redemption window has not
	// elapsed. The ONLY result that authenticates.
	ValidateAccepted
	// ValidateWindowElapsed: a device matches, but its pairing token was
	// never redeemed and RedeemBy has passed. Distinct from
	// ValidateUnknownToken so the caller can log the two apart; callers MUST
	// NOT let the distinction reach the client (#1529 — a distinct wire
	// signal would confirm to a token's holder that it was once real).
	ValidateWindowElapsed
)

// redemptionWindowElapsed reports whether d's redemption deadline has passed at
// now. A zero RedeemBy never elapses — that is both a record minted before the
// field existed and, since #1528's redemption clear, a record that has already
// been redeemed. The deadline is exclusive: at exactly RedeemBy the record has
// stopped being acceptable, matching the field's "the instant at which an
// UNREDEEMED pairing record stops being acceptable".
//
// Split out of Validate so the boundary is table-testable at a caller-supplied
// instant without injecting a clock into Registry.
func (d Device) redemptionWindowElapsed(now time.Time) bool {
	return !d.RedeemBy.IsZero() && !now.Before(d.RedeemBy)
}

// Validate is the WS-perimeter auth predicate. It hashes plain, looks up the
// matching device by hash, checks that device's redemption window, and — only
// on acceptance — advances its LastSeenAt to time.Now() in the in-memory
// registry. Returns the matched Device and ValidateAccepted on a hit. Every
// refusal returns the zero Device (never the matched record, which would invite
// a caller to read it) alongside the reason: ValidateUnknownToken when no
// device matches or plain is the empty string, ValidateWindowElapsed when a
// device matches but its unredeemed pairing token is past RedeemBy (#1529).
//
// SECURITY: the LastSeenAt stamp happens strictly AFTER the window check, so a
// rejected attempt mutates nothing at all. That is what keeps `pyry pair list`
// an honest witness — an expired token that kept refreshing LastSeenAt would
// look exactly like a device in daily use. Validate likewise never removes the
// expired record: Remove is the only deleter, and `pyry pair revoke` is its
// only caller.
//
// Validate does NOT persist the LastSeenAt update — disk persistence is the
// caller's responsibility (Validate runs once per WS connect; fsync on the
// auth hot path is undesirable). Callers that want LastSeenAt durability
// schedule a periodic Save (e.g. every N minutes, or on graceful shutdown);
// the in-memory state is the source of truth for runtime decisions.
//
// SECURITY: the empty plain returns ValidateUnknownToken without computing
// HashToken or taking the registry lock. This prevents an attacker who
// omits the token from triggering a registry scan, and it defends against
// the (unreachable today, but cheap-to-defend) case of a Device persisted
// with TokenHash == HashToken("").
//
// SECURITY: Validate never logs the plain, never logs the hash, never logs
// the matched device name, and never returns any of these in an error (the
// predicate has no error path today). The returned (Device, ValidateResult)
// is the only signal the caller receives.
//
// Concurrency: the lookup-check-and-mutate is one critical section under
// Registry.mu — concurrent Validate calls of the same token observe a
// monotonically-non-decreasing LastSeenAt, the window check cannot be
// interleaved with the stamp it guards, and the mutation never races with
// Add / Remove / List / FindByTokenHash / ClearRedeemBy / Save snapshots.
func (r *Registry) Validate(plain string) (Device, ValidateResult) {
	if plain == "" {
		return Device{}, ValidateUnknownToken
	}
	hash := HashToken(plain)
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.devices {
		if r.devices[i].TokenHash == hash {
			if r.devices[i].redemptionWindowElapsed(time.Now()) {
				return Device{}, ValidateWindowElapsed
			}
			r.devices[i].LastSeenAt = time.Now()
			return r.devices[i], ValidateAccepted
		}
	}
	return Device{}, ValidateUnknownToken
}

// MayAnswerRemotePermission reports whether this device is authorized to answer
// a remote permission / trust / destructive modal. Fail-closed: returns true
// ONLY when the per-device opt-in bit is set. A nil receiver (no authenticated
// device on the connection) and a bit-OFF device both return false = denied.
// The nil-guard makes the safe default structural, so the predicate is total:
// the modal control loop (#703) calls it off dispatch.Conn.Auth() — typed
// *Device, nil before the first-frame gate accepts — to reject a non-permitted
// phone's modal answer with an error envelope BEFORE resolving any answer
// (ADR 025 § "Security model").
//
// Pure predicate: no side effects (no logging, no I/O, no token handling).
// Audit-writing on a decision is #712's primitive; it is not invoked here.
func (d *Device) MayAnswerRemotePermission() bool {
	return d != nil && d.AllowRemotePermissions
}

// RemotePermissionOutcome is what the modal control loop (#703) observed for a
// surfaced remote-permission modal. The zero value (OutcomeNoAnswer) is the
// safe default and resolves to DENY, so a default-constructed call denies.
type RemotePermissionOutcome int

const (
	OutcomeNoAnswer RemotePermissionOutcome = iota // no answer observed (default -> DENY)
	OutcomeAllow                                   // phone explicitly chose an allow option
	OutcomeDeny                                    // phone explicitly chose a deny option
	OutcomeTimeout                                 // deny-on-timeout window elapsed (#703's timer)
	OutcomeCancel                                  // phone cancelled / dismissed (ESC)
)

// AuthorizeRemotePermission resolves the final grant decision, fail-closed.
// Returns true (ALLOW) ONLY when the device is eligible AND the outcome is an
// explicit allow. Every other (device, outcome) — ineligible / nil device, no
// answer, timeout, cancel, explicit deny — returns false (DENY). #703 applies
// this on timeout (OutcomeTimeout -> false).
//
// It re-checks MayAnswerRemotePermission (defense in depth) so it denies
// correctly even if a caller skips the upfront eligibility gate. The single
// ALLOW conjunction keeps the safe default in one unit-tested place: any future
// outcome added to the enum defaults to DENY unless explicitly mapped here.
//
// Pure predicate: no side effects (audit is #712's, deliberately separate).
func AuthorizeRemotePermission(d *Device, outcome RemotePermissionOutcome) bool {
	return d.MayAnswerRemotePermission() && outcome == OutcomeAllow
}
