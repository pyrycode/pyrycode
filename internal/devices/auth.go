package devices

import "time"

// Validate is the WS-perimeter auth predicate. It hashes plain, looks up the
// matching device by hash, and advances that device's LastSeenAt to
// time.Now() in the in-memory registry. Returns the matched Device and true
// on a hit; (Device{}, false) on any miss (no device matches, or plain is
// the empty string).
//
// Validate does NOT persist the LastSeenAt update — disk persistence is the
// caller's responsibility (Validate runs once per WS connect; fsync on the
// auth hot path is undesirable). Callers that want LastSeenAt durability
// schedule a periodic Save (e.g. every N minutes, or on graceful shutdown);
// the in-memory state is the source of truth for runtime decisions.
//
// SECURITY: the empty plain returns (Device{}, false) without computing
// HashToken or taking the registry lock. This prevents an attacker who
// omits the token from triggering a registry scan, and it defends against
// the (unreachable today, but cheap-to-defend) case of a Device persisted
// with TokenHash == HashToken("").
//
// SECURITY: Validate never logs the plain, never logs the hash, never logs
// the matched device name, and never returns any of these in an error (the
// predicate has no error path today). The returned (Device, bool) is the
// only signal the caller receives.
//
// Concurrency: the lookup-and-mutate is one critical section under
// Registry.mu — concurrent Validate calls of the same token observe a
// monotonically-non-decreasing LastSeenAt, and the mutation never races
// with Add / Remove / List / FindByTokenHash / Save snapshots.
func (r *Registry) Validate(plain string) (Device, bool) {
	if plain == "" {
		return Device{}, false
	}
	hash := HashToken(plain)
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.devices {
		if r.devices[i].TokenHash == hash {
			r.devices[i].LastSeenAt = time.Now()
			return r.devices[i], true
		}
	}
	return Device{}, false
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
