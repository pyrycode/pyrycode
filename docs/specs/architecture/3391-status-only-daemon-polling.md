# Status-only daemon thread polling

## Files read
- `cmd/pyry/thread_shadow.go` → `shadowStore`, `follow`: pre-delivery consumed boundary, private activity, load/retry and final draining.
- `cmd/pyry/daemon_thread_relay.go` → `installDaemonThreadRelay`, `publish`, `gap`: registry eligibility, observation cursors and retirement.
- `cmd/pyry/thread_shadow_test.go` → `testShadowUnavailable`, `TestThreadShadowFailedDrain`, `TestThreadShadowRetirementCommit`: explicit failed-read override and lifecycle fixtures.
- `cmd/pyry/daemon_thread_relay_test.go` → `TestDaemonThreadContinuity`, `TestDaemonThreadRetirement`, `TestDaemonThreadIdleCommit`, `TestDaemonThreadReadiness`: baseline replacement, held delivery and usable zero.
- `internal/thread/store.go` → `Status`, `Snapshot`: scalar capture under the publication mutex versus detached baseline copies.
- `internal/thread/observations_store.go` → `Observe`, `Changes`: captured progress and retained-range validation.
- `docs/knowledge/features/thread-package.md` § Shadow lifecycle and evidence: epoch reuse alone cannot establish continuity after unload.
- `docs/knowledge/features/thread-package-background-store.md` § Isolation and lifecycle: private activity and final producer draining precede shutdown.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Update messages: preserve full additions, patches, suffixes and revision metadata.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: focused behavioral and allocation evidence.

## Context
Repeated unchanged daemon metadata reads copy a complete loaded thread. Use the
merged metadata-only store API at all three checks without changing wire output
or shadow lifecycle. No decision record is needed. Unloaded certificates remain
with #3330. No fetched feature branch overlaps the planned production/test files.

## Design
Replace `shadowStore.Snapshot` with `Status` and retain the status capture before
`s.publish` can wait on delivery. Keep all load/retry/unload/drain decisions on
that captured publication. Update the unavailable test override explicitly.
The installed registered-conversation watermark lookup uses `Status`, preserving
both `LastShownVersion` and the usable flag, including usable zero.

Publisher status is only a hint: nonusable status retires prior consumers and an
unchanged epoch/version returns without item access. Initial publication reads
`Observe`; changed publication reads `Changes` from the saved cursor. A nonusable
or unprovable range retires peers before acquiring a replacement `Observe`.
Every baseline is checked for usability. Output metadata and the cursor come
exclusively from the captured observation that supplied its items/changes.
Use a narrow unexported publisher store interface for deterministic interleaving
tests, leaving relay installation bound to the concrete production store.

Sizing after plan: approximately 400 written lines, no exported types/interfaces,
three production metadata call sites plus one test override, three acceptance
criteria, no new reject branches. All five limits fit.

## Concurrency model
No new production goroutines, locks or lifecycle state. The coordinator continues
to own its publication cursor. Store reads lock only their own capture; delivery
and retirement remain outside store/shadow locks. Existing cancellation and joins
are unchanged. Test interleavings use synchronous callbacks and joined workers.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Repeated unchanged polling/list reads | `TestDaemonThreadMetadataAllocations`, `TestThreadMetadataReadSources` |
| Publication advances between hint and observation | `TestDaemonThreadStatusInterleaving` |
| Retirement/reload with same ID/epoch between reads | `TestDaemonThreadStatusInterleaving`, `TestDaemonThreadContinuity` |
| Initial baseline withdrawn before observation | `TestDaemonThreadStatusInterleaving` |
| Held delivery and later committed history | `TestDaemonThreadIdleCommit` |
| Quiescent unload, append during retirement and reload | `TestThreadShadowReload`, `TestThreadShadowRetirementCommit` |
| Unavailable retry and unsuccessful final drain | `TestThreadShadowFailure`, `TestThreadShadowFailedDrain` |
| Shutdown drains final commits; deletion/stop joins held delivery | `TestThreadShadowShutdown`, `TestDaemonThreadRetirement` |

## Error handling
Keep existing load/retry/unload failures and generic failed-drain result.
Status never certifies continuity: `Changes` errors preserve the cursor for retry;
nonusable/gapped results retire before baseline replacement. No new logging.

## Testing strategy
Write focused tests before production changes and observe their failure. Use a
real loaded long-thread fixture with fixed item/content size; exclude replay,
setup and initial publication from allocation measurements. Benchmark unchanged
publication, installed list lookup and shadow metadata read before/after on the
same fixture. Pair allocation assertions with source guards against item reads
or traversal at metadata checks; allocation counts alone miss traversal.
Force store advancement/retirement after the hint and verify captured wire
metadata/items, cursor progress and retirement. Reuse existing wire/lifecycle
tests. Run `go test -race ./cmd/pyry ./internal/relay`, `go vet ./...` and
`go build -o /tmp/builder-3391/pyry ./cmd/pyry`. The verifier owns `make check`
and full-module validation per the builder role; no live-Claude evidence needed.

## Open questions
None.

## Performance evidence
Measured on Linux/amd64, Intel Core i7-8700, Go 1.26.9, with
`go test ./cmd/pyry -run '^$' -bench '^BenchmarkDaemonThreadMetadata$' -benchmem -benchtime=100x -count=1`.
Before: production at plan commit `cdb522a2`, with the benchmark harness added
before implementation. After: this implementation, using the identical harness
and loaded fixture. The fixture contains 1,024 public items, 4,096 text bytes per
item and 4,219,904 total JSON content bytes. Setup, history writes, replay,
checkpoint creation and initial publication are outside measured subbenchmarks.

| Metadata check | Before B/op | Before allocs/op | After B/op | After allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Unchanged publisher | 5,177,346 | 1,025 | 0 | 0 |
| Installed list lookup | 5,177,344 | 1,025 | 0 | 0 |
| Shadow metadata read (`Snapshot` → `Status`) | 5,177,347 | 1,025 | 0 | 0 |

The shadow row compares `BaselineSnapshot` with `ShadowStatus` on that same
loaded store; the source guard pins the actual polling call to `Status`.
Complete baseline reads remain detached: after migration `BaselineSnapshot`
still reports 5,177,346 B/op and 1,025 allocs/op. Before implementation the new
allocation regressions failed with 1,025 allocations/read for both publisher and
installed list lookup, and the shadow/list source guards rejected baseline reads.

## Documentation handoff
Pending for the documentation stage:
- `docs/knowledge/features/thread-package-background-store.md`, **Isolation and lifecycle**: describe status-only shadow polling and unchanged final draining.
- `docs/knowledge/features/thread-package.md`, **Shadow lifecycle and evidence**: document unchanged-version/list metadata checks using status and full baseline reads occurring only when required.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] `installDaemonThreadRelay` retains the registry membership guard; publisher recipient selection remains after observation capture and relay enforces eligibility.
- [Tokens, secrets, credentials] No credential handling changes; status contains existing publication scalars only.
- [File operations] Metadata checks perform no file operations; existing store lifecycle persistence remains unchanged.
- [Subprocesses] No subprocess creation or environment changes.
- [Cryptography] No key, nonce or cryptographic operations change.
- [Network and I/O] `EncodeThreadUpdate` and existing relay delivery preserve framing limits and access gates; no new network reader.
- [Errors, logs, telemetry] Existing generic errors and payload-free logs remain; no content or status logging added.
- [Concurrency] Status cannot authorize a delta or seed its cursor. `Changes` validates continuity and captured observations alone supply output; deterministic interleaving tests cover withdrawal and advancement.
- [Threat model] Authenticated relay access and recipient gates remain intact. No new security boundary or deferred threat is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15
