# #1498 — `pyry update` refuses a non-pinned downgrade

`--version <tag>` stays the sanctioned downgrade route.

## Files to read first

| Path | Symbol | What to extract |
|------|--------|-----------------|
| `cmd/pyry/update.go` | `doUpdate` | The compare switch and its three exits, the `o.checkOnly` early return, and the `update: <step>: …` error-prefix idiom every step uses. This is the only production file that changes. |
| `internal/update/version.go` | `CompareVersions`, `Newer`, `Older`, `Same` | Polarity. `CompareVersions(current, latest)` returns `Newer` when **current is newer than latest** — i.e. the rollback case. Do not invert this. |
| `internal/update/checksum.go` | `AssetName`, `ParseChecksumsFile` | Why the tag string is bound into signature-verified material (see § Security review, Trust boundaries). Read-only — nothing changes here. |
| `cmd/pyry/update_test.go` | `TestUpdate_AlreadyAtLatest` | The shape the new refusal test mirrors: fake server + a `replace` closure that is a `t.Fatalf` sentinel + an assertion on the captured output. |
| `cmd/pyry/update_test.go` | `TestUpdate_CheckOnly` | The shape the new `--check` test mirrors, including the "must not download" negative assertion. |
| `cmd/pyry/update_test.go` | `TestUpdate_PinVersion` | **Already is a pinned downgrade** (current `0.9.1`, pin `v0.9.0`). It is the built-in scope regression guard — a refusal not scoped to the non-pinned path turns it red. Do not modify it. |
| `cmd/pyry/update_test.go` | `newFakeReleaseServer`, `fakeRelease`, `testSigningPriv`, `testSigningPub` | Existing fixtures. `newFakeReleaseServer` auto-signs whatever checksums it is handed. |
| `docs/knowledge/features/pyry-update-command.md` | § Flow steps 4–5, § Error contract table | Both go stale on merge. **The documentation phase owns that edit — the developer must not touch this file.** |

Everything under `cmd/pyry/update_e2e_test.go` is unaffected: all five of its `updateOptions` literals set `currentVersion: "0.0.1"` and serve `v999.0.0`, so `CompareVersions` returns `Older` on every one and the new guard never fires. No build-tagged suite needs re-running for this change beyond the usual `make preship` on the release checklist.

## Context

`doUpdate` treats "latest tag differs from current" as "install it". The compare switch exits on a dev build, on a compare error, and on `Same`; everything else — including `Newer`, which means *the advertised latest is older than what is running* — falls through to `AssetName` → download → verify → replace → restart.

The signature gate added in #776 cannot close this. It proves the downloaded bytes are the ones the release pipeline signed; it says nothing about *which* release. An attacker holding the compromised publishing token — the exact adversary named in the doc comment on `releaseSigningPublicKeyHex` — re-marks an old genuine release as latest, and every gate passes because that release was validly signed when it shipped. A maintainer yanking a bad release produces the same effect without any attacker at all.

The escape hatch already exists and is already documented: `--version <tag>` pins a target and skips the latest-release call entirely. This ticket only stops the *unasked-for* downgrade. No new flag.

## Design

One guard in `doUpdate`, in `cmd/pyry/update.go`. Nothing else changes: no new files, no new exported symbols, no signature change to `doUpdate` or `updateOptions`, no consumer cascade.

### Placement

The guard sits **between the `o.checkOnly` early return and the `AssetName` call** — on the install side of the check-only boundary. That position is an observable requirement, not a style preference:

- Above the `checkOnly` return (e.g. as a fourth arm of the compare switch), `pyry update --check` would start exiting non-zero against an older-than-current latest. `docs/release-tooling.md` runs `pyry update --check` on the release checklist as the "confirms it reaches the release" step, and the flag's documented contract is print-two-lines-and-exit-0. Turning it non-zero is a scope expansion this ticket does not want.
- Below `AssetName` or later, the refusal would already have issued network fetches, which AC 1 forbids.

`cmp` is declared by the `cmp, err := update.CompareVersions(...)` statement that precedes the switch, so it is still in scope at the guard's position. No restructuring of the switch is needed.

### Condition

Both halves are load-bearing:

```
o.pinVersion == "" && cmp == update.Newer
```

- `cmp == update.Newer` — `Newer` means *current is newer than latest*. Inverting the polarity would refuse every ordinary upgrade.
- `o.pinVersion == ""` — an explicit `--version <older-tag>` is the sanctioned downgrade and must keep working. Dropping this half turns `TestUpdate_PinVersion` red, which is exactly the signal that scope was lost.

This is the first consumer anywhere outside `internal/update` of the comparison's sign; `Newer` and `Older` were previously referenced only within their own package.

### Error shape

Return a wrapped error; do **not** introduce a sentinel in `internal/update`. Every other step in `doUpdate` returns a `fmt.Errorf("update: <step>: …")` string with no sentinel, and the existing tests match on substrings (`"download signature"`, `"verify signature"`). A sentinel would add an exported symbol for one call site with no `errors.Is` consumer, against the "no new exported types" scope and against the house pattern in this file.

The step name is `refuse downgrade`, so the error slots into the documented `update: <step>: …` family. The message must carry three things: the two versions, and the literal sanctioned invocation including the `--version` flag and the tag to pass it. Under `main`'s `pyry: <err>` wrapper the operator sees a single line of the shape:

```
pyry: update: refuse downgrade: latest release v0.9.0 is older than the running version 0.12.0; run 'pyry update --version v0.9.0' to downgrade intentionally
```

Naming the tag inside the suggested command matters: an operator who genuinely wants the rollback can copy the line verbatim. AC 2 is satisfied by the presence of `--version` in the operator-visible text, not by any particular wording — the developer may adjust phrasing, but the flag name and both version strings must appear.

### Output channel

Error only. Do not also print a `==>` progress line to `o.out`. The two version lines have already printed by the time the guard runs, so the operator has the full picture; adding a stdout line would put the same fact on two channels and give the tests two things to pin. Every other refusal in this file is error-only.

### What the guard deliberately does not do

- It does not persist a highest-version-ever-seen floor. The comparison is against the running binary only. See § Security review for the residual case this leaves open.
- It does not touch the dev-build exit. `currentVersion == "dev"` still fails `CompareVersions` with `ErrInvalidVersion` and returns nil before the guard is reached — correct, since that path installs nothing.

## Concurrency model

None. `doUpdate` is a sequential one-shot CLI verb on the calling goroutine, with no locks, channels, or goroutine fan-out. The guard is a comparison on two locals. Nothing about the concurrency picture in `docs/knowledge/features/pyry-update-command.md` § Concurrency changes.

## Error handling

| Situation | Behaviour after this change |
|-----------|----------------------------|
| No pin, latest older than current (`Newer`) | **New.** Return `update: refuse downgrade: …`. Nothing is fetched beyond the already-completed latest-release call; no `replace`, no `runRestart`. Exit 1 under `main`'s wrapper. |
| No pin, latest newer than current (`Older`) | Unchanged — installs. |
| Same version (`Same`) | Unchanged — "already at latest", nil. |
| Dev build (`ErrInvalidVersion`) | Unchanged — "skipping update", nil. |
| Malformed tag from the release API | Unchanged — `update: compare versions: …`, exit 1. Fails closed; the guard is not reached. |
| `--check` with an older-than-current latest | Unchanged — both version lines print, returns nil, exits 0. The guard is below the early return. |
| `--version <older-tag>` | Unchanged — downloads, verifies, replaces. The sanctioned route. |

The refusal is a hard stop with no retry and no fallback, matching the checksum- and signature-mismatch precedent in this file: a rollback is either a yanked release or a hostile publisher, and silently proceeding masks both.

## Testing strategy

Two new tests in `cmd/pyry/update_test.go`, plus one small server constructor. No existing test is modified — the three named in AC 3 and AC 5 (`TestUpdate_PinVersion`, `TestUpdate_AlreadyAtLatest`, `TestUpdate_DevBuildSkips`) and `TestUpdate_Success` all stay green untouched, and their staying green *unmodified* is itself the scope pin.

**Server constructor — `newDowngradeReleaseServer(t, latest)` (or equivalent).** Follows the #261 precedent of a parallel constructor rather than a failure-injection knob on `newFakeReleaseServer`, which has four happy-path callers to keep clean. It registers the `/repos/pyrycode/pyrycode/releases/latest` route returning `{"tag_name": latest}`, plus a catch-all root handler that **records** the requested path and returns 500.

Record, do not `t.Fatalf`. The catch-all runs on an `httptest` handler goroutine, and `t.Fatalf` off the test goroutine calls `runtime.Goexit` on the wrong goroutine — the handler dies, the client sees a transport error, and the test fails somewhere misleading. Use a mutex-guarded slice of paths or an `atomic.Int32`, and assert on it after `doUpdate` returns. The `t.Fatalf`-sentinel idiom stays correct for the `replace` and `runRestart` seams, which *are* called on the test goroutine — keep using it there.

**Test 1 — unpinned downgrade is refused (AC 1, AC 2).**

- `currentVersion` a released version, served `tag_name` a strictly lower one (e.g. current `0.9.1`, latest `v0.9.0` — mirrors `TestUpdate_PinVersion`'s numbers with the pin removed).
- No `pinVersion`.
- `replace` and `runRestart` are `t.Fatalf` sentinels.
- `executablePath` returns a path that must never be touched, as `TestUpdate_AlreadyAtLatest` does.
- Assert: error is non-nil; its text contains `update:` and `--version`; the recorded asset-request list is empty (proves no tarball, no `checksums.txt`, no `checksums.txt.sig` was fetched); the `==> Updated to` success line is absent from the captured output.
- The empty-request-list assertion is the one that would catch a guard accidentally placed below `AssetName`; the sentinels alone would not.

**Test 2 — `--check` against an older latest still exits 0 (AC 4).**

- Same version pairing, `checkOnly: true`.
- Assert: nil error; both `==> Current version:` and `==> Latest version:` lines print; no `Downloading` in the output. Mirrors `TestUpdate_CheckOnly`'s assertions with the served tag flipped older.
- This test is what fails if the guard drifts above the `checkOnly` return, and it is the reason AC 4 is written as an ordering constraint.

**Mutation check for the developer.** Two independent mutations, each of which must redden a distinct test: dropping the `o.pinVersion == ""` half of the condition must turn `TestUpdate_PinVersion` red, and moving the guard above the `checkOnly` return must turn test 2 red. If either mutation leaves the suite green, the corresponding test is not pinning what it claims.

Standard `make check` covers all of this; the `e2e_update`-tagged suite needs no new case, since the unit-level test already pins the pre-`AssetName` exit and every e2e literal is on the upgrade path.

## Open questions

- **Message wording.** The exact sentence is the developer's call within the constraint that `--version`, the served tag and the running version all appear. If a future ticket adds a machine-readable output mode, the message becomes a candidate for a sentinel; not now.
- **Nothing else.** Scope, placement and polarity are all fixed by the AC.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and one non-obvious property worth recording.** The untrusted-to-trusted crossing is `Fetcher.FetchLatestRelease` → `ParseLatestRelease` → `targetVer`. The guard reads `targetVer` *before* any signature has been verified, which invites the question: can an attacker who controls the API response bypass the guard by advertising a fake *higher* tag? No — and the reason is structural, not accidental. `AssetName(targetVer, …)` bakes the version string into the tarball filename, and `ParseChecksumsFile` looks that exact filename up inside the signature-verified `checksums.txt`. A forged `v99.0.0` therefore needs a signed checksums file listing `pyry_99.0.0_<OS>_<arch>.tar.gz`, which does not exist and cannot be produced without the private key. The tag is bound into signed material downstream, so an unauthenticated tag is a safe input to the guard. This property must not be broken by a future change that derives the asset name from anything other than the compared tag.
- **[Tokens, secrets, credentials] No findings.** `pyry update` holds no credential; the compromised *publishing* token is the adversary, not an asset of this process. The guard adds no storage, no lifecycle, no revocation surface. Revocation of a yanked release is precisely what this guard makes safe: yanking now causes a refusal instead of a rollback.
- **[File operations] No findings.** The refusal returns before `AssetName`, so it is upstream of every filesystem operation in the flow. `AtomicReplace` is not reached, no temp file is created, and no straggler can be left behind — the same structural argument `TestUpdate_FetchFailure_E2E` already makes for the pre-replace failure paths. No path is built from the untrusted tag on the refusal path.
- **[Subprocess / external command execution] No findings.** The refusal returns before the `!o.noRestart` block, so `runRestart` — the only `exec.CommandContext` in the flow — is structurally unreachable. AC 1 pins this, and the test's `t.Fatalf` sentinel enforces it.
- **[Cryptographic primitives] No findings.** No new primitive, no key, no comparison against a secret. The guard's comparison is on integers parsed from version strings, not on attacker-controlled bytes versus a secret, so constant-time comparison is not applicable. Ordering note: the guard runs *before* `VerifySignature`, which is correct — refusing earlier is strictly safer than refusing later, and the trust-boundary finding above shows the earlier decision is not exploitable.
- **[Network & I/O] No findings.** The change strictly *removes* network calls on the refusal path (three fetches that would otherwise have been issued). No new reads, no new caps to set; the existing 60-second `http.Client` timeout is untouched.
- **[Error messages, logs, telemetry] No findings.** The message contains exactly two version tags and a literal flag name. It does not include the target executable path, the release base URL, HTTP response bodies, or any internal state. Note that the *absence* of the target path is deliberate — `==> Replacing <target>...` is the only line that prints it, and the refusal never reaches it.
- **[Concurrency] No findings.** Single goroutine, no shared state, no locks; the guard is a comparison on function-local values. The one concurrency hazard this change *introduces* is on the test side, and the spec addresses it explicitly: the request-recording server handler runs on an `httptest` goroutine, so it must record under a mutex (or an atomic) and be asserted after `doUpdate` returns, never via `t.Fatalf` from the handler.
- **[Threat model alignment] Two residual cases, both OUT OF SCOPE and both named here rather than silently left open.**
  1. **Intermediate-version pinning.** An attacker who re-points `/releases/latest` at a release that is *newer than the running binary but older than the true latest* — running `v0.10.0`, true latest `v0.12.0`, advertised `v0.11.0` — still gets an install, because the comparison is against the running binary only. Closing this needs a persisted monotonic version floor or a TOFU record of the highest tag ever observed, which is a separate feature with its own storage and reset-path design. Not this ticket; file a follow-up if the threat is ever observed in the wild.
  2. **`--version <older-tag>` remains an unguarded downgrade.** By design — it is the sanctioned route and AC 3 requires it keep working. An adversary who can choose the operator's command line already has code execution on the host, so the guard would buy nothing.

  The primary threat named in the `releaseSigningPublicKeyHex` doc comment — a compromised publishing token installing an unwanted binary — is now closed for its *older-genuine-release* half by this ticket and for its *attacker-chosen-bytes* half by #776. The doc comment's claim stays accurate; no edit needed there.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
