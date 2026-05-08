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
