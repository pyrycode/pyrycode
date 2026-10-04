# 040. Eligible release and safe-to-restart idle are independent predicates

## Status

Accepted (#2716)

## Context

`pyry update` only runs when an operator logs in and runs it by hand, so a
daemon reached only to update it falls behind. #2716 adds an opt-in daemon
loop that checks the latest release on a schedule and installs it by itself.
Two properties of the existing release process shaped how that loop is
allowed to pick a release and when it may act.

Releases are only ever published by hand, through the release script, which
creates a draft and later promotes it to the GitHub "latest" release. There
is no other path to "latest" — a draft or a pre-release never reaches it, and
GitHub's `/releases/latest` endpoint never returns one. So "follow latest"
already means "follow only a release an operator chose to make," provided
the daemon also refuses a release the endpoint should not have returned but
might: a stale cache, a tag carrying a pre-release suffix that
`update.CompareVersions` would otherwise strip before comparing, or a
genuinely-signed but older release re-marked as latest (a yanked release, or
a compromised publishing token — `pyry update`'s own non-pinned-downgrade
guard, #1498, exists for the same reason).

The daemon also supervises live Claude and Codex sessions, each potentially
mid-turn — a restart cuts a response off mid-stream. Deciding which release
is eligible and deciding whether it is safe to restart right now are two
different questions, answered from two different sources: the GitHub
release JSON for the first, the session pool's and turn tracker's live state
for the second.

## Decision

Keep "which release is eligible" and "is it safe to restart now" as two
independent, pure, table-tested functions — `update.Eligible` and
`daemonIdle` — and require both to hold before anything is downloaded or
installed. `Eligible` refuses a draft, a pre-release, a tag carrying a `-` or
`+` suffix, a non-release (`dev`) running build, and anything not strictly
newer than the running version — not just the common case, but every way a
hand-curated "latest" could still fail to be one. `daemonIdle` refuses to
act while any conversation, Claude or Codex, has an open turn, or while any
session was touched within a 15-minute quiet window; a connected phone is
deliberately not part of that signal, since a phone left connected overnight
would otherwise hold every update off forever. After a scheduled check selects
an eligible release, it retains that tag and polls `daemonIdle` once a minute
until idle or cancelled (#2756). Waiting performs no downloads or repeated
latest-release requests, even across four-hour boundaries. At the first idle
poll it downloads and installs the selected release; a later change to latest
does not change the selection. Eligibility and idle remain independent.

## Rationale

Merging the two checks into one decision — for example, "install if idle and
the release looks newer" — would let a looser version comparison stand in
for eligibility, and #2716's security review is exactly why that would be
unsafe: `CompareVersions` strips a `-rc1` suffix before comparing, and
`parseSemver`'s integer parser accepts a leading sign, so either check alone
would pass a tag no hand-made release carries. Keeping `Eligible` as its own
function, exercised by its own table test, is what lets it enumerate every
one of those cases explicitly rather than relying on a comparison written
for a different purpose.

Keeping idleness independent of eligibility is what lets the restart step
re-ask `idle()` a second time, immediately before handing off to the service
manager, without re-deciding eligibility at all: the release already chosen
stays chosen, and only "is now still a safe moment" needs re-asking, because
a turn can open during the download.

## Consequences

- A future change to what counts as an installable release (a channel
  selector, say) extends `Eligible`'s table test without touching
  `daemonIdle`, and a future idleness signal (an attached-but-mid-command CLI
  session, say) extends `daemonIdle` without touching `Eligible` — the two
  stay independently testable as long as neither folds the other's concern
  in.
- Because both are pure, the auto-updater's own tests substitute fakes for
  the network and the clock directly; only the install path itself needs the
  signed httptest fixture `cmd/pyry/update_test.go` already built for
  `pyry update`.
- Timing uses a two-minute startup delay, a 15-minute session quiet window,
  one-minute idle polls before installation and restart, and a four-hour retry
  delay after an unsuccessful check completes. There is no backoff or jitter.
  A fleet of daemons started near the same time checks near the same time —
  accepted because the GitHub request is anonymous and a once-per-four-hours
  check stays far under the rate limit (see the plan's Security review).

## Related

- [`pyry-update-command.md`](../features/pyry-update-command.md) § Automatic
  update — the implementation this decision governs.
- [ADR 028](028-ed25519-checksums-signature.md) — the signature gate that
  trusts a release's *bytes*; this decision is about which release is even
  considered, answered before any bytes are fetched.
- `docs/specs/architecture/2716-daemon-auto-update.md` — the ticket's plan
  and security review.
