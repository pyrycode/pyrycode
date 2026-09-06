# 037. Capability strings, not version numbers, are how clients detect daemon features

## Status

Accepted (#2020)

## Context

`pyry --version` reports a commit sha, which carries no ordering. A client that
needs to know whether the daemon it just connected to implements a given wire
feature has no way to ask, short of trying the feature and inferring support
from the response — or, as happened here, not being able to ask at all.

The inbound-question feature (#1979 and siblings) shipped without adding
anything to `supportedV2Capabilities`
([`internal/relay`](../features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md)),
so a client had no positive signal to check. pyrycode-desktop#928, a
real-claude test that drives the question panel against a live daemon, could
only gate on a `pyry` binary existing; its skip message stated the real
requirement in prose ("built from a #820+#854-inclusive tree") but nothing
checked it. The Mac's installed daemon predated the question work, and the
test could not detect that — it cost a day.

ADR 025's 2026-06-22 amendment retired *old-phone interop* as a requirement
for the `capabilities` field: this tool is self-hosted with a single operator
who controls both ends, so there is no old-app install base to support, and
the field "carries no backward-compatibility obligation." That amendment does
not close the field's other use. Daemon and app ship from the same repo
family but are *installed* separately, so a machine can run a daemon that
predates a wire feature its client already implements — a build-recency
question, not a compatibility one.

## Decision

A user-facing, cross-repo wire feature adds a capability string to
`supportedV2Capabilities`, rather than the project inventing an orderable
protocol/feature version number. A client that needs to detect the feature
advertises the string in its own `hello` and reads it back out of the
`hello_ack`; negotiation is already a pure intersection
(`negotiateCapabilities` iterates the daemon's supported set, never the
client's advertised one), so a daemon built before the string existed drops
it, and that absence is the positive, checkable stale-daemon signal.

The string is detection only. It grants no access by itself — whether a
feature also becomes gated behind a capability (as `interactive` is) is a
separate, per-feature decision the ticket adding it makes explicitly, not a
default.

## Rationale

A string survives cherry-picks and backports; an ordered version number
assumes a monotonic build history the project doesn't have. A string also
says what is actually supported, not when it was built — which is the
question a cross-repo consumer is really asking. The mechanism this rides on
already existed and cost nothing to extend: `negotiateCapabilities`'s
intersection was built (#626) exactly to make a spoofed or unsupported string
unable to be granted, so adding a purely-advertised entry needs no new trust
machinery, only a new constant and one slice element.

## Consequences

- A cross-repo wire feature that ships without a capability string leaves its
  clients unable to tell which daemon they are talking to — as #1979 did, and
  as #2124/#2125's on-demand model list did a second time, at a real cost:
  pyrycode-desktop#1169 burned three rework cycles because a stale daemon and
  a broken feature both present identically to a client as "nothing to show."
  #2172 paid that debt late. Adding the string is part of shipping the
  feature, not a follow-up.
- The capability set is a cross-repo contract compared against a literal
  outside this module, so — unlike an ordinary internal constant — it gets its
  own spec-match drift detector once a wire feature needs one to be trusted;
  see `TestCapability_Constants_MatchSpec` in
  [protocol-package-drift-detectors.md](../features/protocol-package-drift-detectors.md).
  Every existing capability-negotiation test passes the constant symbolically
  on both the advertise and the expect side, so a fat-fingered wire value is
  otherwise self-consistent and green.
- `negotiateCapabilities` is O(k·n) in the size of the supported set k; at
  k=2 a linear scan is the right call, but the next capability added is a
  reasonable point to reconsider a set lookup if k keeps growing. #2172 asked
  the question at k=3 and answered no — a `map[string]struct{}` would cost a
  package-level allocation and an init-order dependency to save three string
  comparisons on a once-per-connection path, and would lose the ordering
  guarantee both handshake test tables' `slices.Equal` depend on.
- Whether a new capability string also becomes a gate (like `interactive`) is
  decided per feature, not inherited from this convention — see the
  fragility note in
  [capability negotiation](../features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md)
  about `s.interactive`'s value-specific reduction.

## Related

- [features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md](../features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md) — the intersection this decision rides on, and the `CapabilityQuestion` addition.
- [features/protocol-package-handshake-control-payloads.md](../features/protocol-package-handshake-control-payloads.md) — the `capabilities` wire field.
- [025-mobile-remote-head-interactive-session.md](025-mobile-remote-head-interactive-session.md) — the interactive-session decision the 2026-06-22 amendment revised.
- `docs/protocol-mobile.md` § Capability negotiation — the wire spec, including the "Still live: build detection" note this decision formalizes.
