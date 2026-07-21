# Spec #1152 — Adopt owned-fd `TailFromEnd` at the outbound-stream tailers

**Ticket:** pyrycode/pyrycode#1152 (split from #973)
**Size:** S (confirmed — 1 production file, 3 resolver bodies, no signature change ⇒ zero consumer cascade)
**Security-sensitive:** yes (see § Security review)

## Files to read first

- `cmd/pyry/interactive_turn_stream_v2.go:176-209` — `resolveLatestSessionJSONL`; the warm-offset assignment is `off := res.Size` at :200, cold override `off = 0` at :201-205. **The change site (1 of 3).**
- `cmd/pyry/interactive_turn_stream_v2.go:284-350` — `resolveOwnBootstrapJSONL`; warm offset `off := info.Size()` at :341, cold override at :342-346. **Change site (2 of 3).** Note the `os.Stat(candidate)` at :333 is *also* the vanish-race existence gate that latches `sawEmpty` — keep the stat; drop only the `.Size()` read.
- `cmd/pyry/interactive_turn_stream_v2.go:486-521` — `resolveBoundSessionJSONL`; warm offset `off := res.Size` at :512, cold override at :513-517. **Change site (3 of 3).** This is the resolver the two ACP tailers reuse verbatim, so changing it here fixes all three tailers at once.
- `internal/turnbridge/producer.go:288-370` — the tail seam. `sess.Events(subCtx, path, off, tr)` at :327 forwards `off` straight through; the `cold_start` diagnostic at :367-368 derives `off == 0` — still correct once warm is `-1`. **No edit here.**
- `internal/transcript/transcript.go:126-186` — `StatByID` / `Newest` return `Result{Path, Size}`. `Result.Size` stays (Family A / `internal/sessions` still reads it); Family B just stops reading it for the offset. **No edit here.**
- tui-driver v1.12.0 `pkg/tuidriver/jsonl.go:137-141` — `const TailFromEnd int64 = -1` and its doc; `:224-239` — `TailJSONL` rejects `< -1`, resolves `== TailFromEnd` to own-fd size, clamps positive to size. `pkg/tuidriver/events.go:258-259` — `Session.Events` forwards `startOffset` into `TailJSONL` unchanged. **Confirms the sentinel flows with no library or seam change (AC2).**
- `cmd/pyry/interactive_turn_stream_v2_test.go:248-269, 314-334, 406-423, 489-555, 822-913, 1234-1256` — the warm-offset assertions that flip from `want <size>` to `want tuidriver.TailFromEnd`; the three `_WarmStartTailsFromSize` functions rename to `_WarmStartTailsFromEnd`. The `_ColdStartTailsFromZero` functions and the `("", 0)`-on-not-found assertions stay at `0` (unchanged).
- `internal/turnbridge/producer_test.go:430-527` — `TestNewTargetSubscriber_SwitchDuringBackoffColdStartsBoundTail`'s local `newBoundResolve` double still returns `info.Size()` for warm, but the test asserts **only** the cold path (`gotOff == 0`, :525). Local test double, not the real resolver — **leave it unchanged** (scope discipline; the cold-start offset-0 property it pins is preserved).

## Context

The parent #973 sequenced this clause last, behind the tui-driver own-fd `TailJSONL` fix (v1.12.0, its #290) and the resolver consolidation onto `internal/transcript` (blocking ticket #1150, now merged). Today the outbound turn-stream resolvers compute the warm-resume offset by measuring the file's size with a caller `os.Stat` (`res.Size` / `info.Size()`), then hand that number down `producer.go:327` → `Session.Events` → `TailJSONL`, which seeks to it. That caller-stat is one half of a cross-fd TOCTOU: the caller stats file *A*, and by the time the tail opens its fd the on-disk file may be a rotated file *B* — the seek lands *A*'s byte offset into *B*. This is the #929/#930 tail-offset class.

tui-driver v1.12.0's `TailJSONL` closes that race on its own fd: it opens the file, stats *that same fd*, and derives the seek target itself. The exported sentinel `TailFromEnd` (`-1`) means "start at the current end of my own fd — skip existing content, stream only future appends," without the caller measuring size. This ticket makes the outbound resolvers pass `TailFromEnd` on the warm path instead of a caller-stat size, so the tail owns its fd end. The cold-start rule (a brand-new file whose whole content *is* the in-flight reply → offset `0` so the reply streams from the top) is preserved unchanged.

## Design

### The change, in one sentence

In each of the three resolvers' warm branch, replace the caller-measured size with `tuidriver.TailFromEnd`; leave the cold-start `off = 0` override and the existence/`sawEmpty` discrimination exactly as they are.

Concretely, each resolver's offset block goes from:

```go
off := res.Size            // (or info.Size())  — caller-stat warm baseline
if !resolvedOnce && sawEmpty {
    off = 0                // cold start: fresh file, stream the in-flight reply from the top
}
```

to:

```go
off := int64(tuidriver.TailFromEnd)   // warm: tail owns its fd end (no caller os.Stat)
if !resolvedOnce && sawEmpty {
    off = 0                            // cold start: unchanged
}
```

`interactive_turn_stream_v2.go` already imports `tuidriver` (used for `NewTracker`), so no new import.

Per-resolver mechanical notes:

- **`resolveLatestSessionJSONL`** — `res` is still used for `res.Path` / `res.Found()`; only the `res.Size` *read* is dropped. `transcript.Newest` still runs (existence gate + path + `sawEmpty` latch).
- **`resolveBoundSessionJSONL`** — same: keep `res.Path`, drop `res.Size`. `transcript.StatByID` + the `ValidStem` branch-selector pre-check are untouched.
- **`resolveOwnBootstrapJSONL`** — the inlined `os.Stat(candidate)` stays (it is the vanish-race retry gate that latches `sawEmpty`), but its result is no longer read for size. The developer will change `info, err := os.Stat(candidate)` to discard the unused `info` (e.g. `if _, err := os.Stat(candidate); err != nil { … }`), then set the offset from the sentinel. The pid-probe, `GuardProbedPath`, and `CanonicalDir` confidentiality path are untouched.

The resolver doc-comments that currently say "offset = size (tail from EOF)" should be reworded to "`TailFromEnd` — own-fd EOF, tail owns its fd" so the prose matches the sentinel. Same semantic ("don't replay history to the phone"), stronger mechanism.

### Why this touches nothing else

- **Resolver signatures are unchanged** (`func(ctx) (path string, startOffset int64, err error)`), so the three tailers — `startInteractiveTurnStreamV2` (turn stream), `acpTurnStreams.start` (ACP turn stream), `acpTurnStreams.startPermissionProxy` (ACP permission stream) — consume the resolvers as-is and inherit the new offset for free. No call-site cascade.
- **The seam is unchanged.** `producer.go:327` passes `off` verbatim; `Session.Events` (v1.12.0, `events.go:259`) forwards `startOffset` into `TailJSONL` unchanged; `TailJSONL` resolves `-1` against its own fd. `-1 < 0`, but `TailJSONL` only rejects `< -1`, so the sentinel is legal (AC2). No tui-driver bump.
- **The `cold_start` diagnostic** at `producer.go:368` (`"cold_start", off == 0`) stays correct: warm is now `-1`, cold is `0`, so `off == 0` still means cold-start.
- **`internal/transcript` is untouched.** `Result.Size` is still produced and still read by Family A (`internal/sessions` growth-confirm); only Family B stops consuming it.

### Data flow (unchanged except the value of `off`)

```
resolve(ctx) ─► (path, off)          off = TailFromEnd (warm) | 0 (cold)
      │
      ▼
producer: WaitForSessionJSONL(path)  (existence gate, unchanged)
      │
      ▼
sess.Events(ctx, path, off, tr) ──► TailJSONL(ctx, path, off)
                                        off == -1  → seek to own-fd size (skip existing)
                                        off == 0   → seek to 0 (stream from top)
```

## Concurrency model

No change. The `resolvedOnce` / `sawEmpty` cold/warm state is read and written only inside the returned closure, which `NewTargetSubscriber` invokes from the single `Producer.Run` goroutine (the documented single-Run-goroutine invariant). No new goroutines, no new shared state, no lock changes.

## Error handling

No new failure modes. The only offset values the resolvers now emit are `TailFromEnd` (`-1`), `0` (cold-start), or a wrapped stat/not-found error (offset `0`, unchanged). `TailJSONL` accepts `-1` and `0`; it would reject `< -1`, which the resolvers never emit. The not-found → retry path and the confidentiality guard rejections are byte-for-byte unchanged.

## Testing strategy

All in `cmd/pyry/interactive_turn_stream_v2_test.go` (one file).

**Flip the warm-offset assertions** (resolver returns the sentinel, not a size). Each is a one-line `want <size>` → `want tuidriver.TailFromEnd` edit plus a message reword; the surrounding table/scaffold is unchanged:

- `TestResolveLatestSessionJSONL_NewestWinsWithSizeOffset` (:78) — rename to `…NewestWinsWithTailFromEnd`; assert `-1`.
- `TestResolveLatestSessionJSONL_ReEvaluatesPerCall` (:98) — the resolve-#2 rotation assertion (`want 7`) → `-1`.
- `TestResolveLatestSessionJSONL_IgnoresNonSessionEntries` (:144) — `want 9` → `-1`.
- `TestResolveLatestSessionJSONL_ColdStartTailsFromZero` (:206) — the **post-cold-start rotation** assertion (`want 17`, :244) → `-1`; the cold assertion (`want 0`, :228) **stays**.
- `TestResolveLatestSessionJSONL_WarmStartTailsFromSize` (:253) → rename `…WarmStartTailsFromEnd`; `want 128` → `-1`.
- `TestResolveOwnBootstrapJSONL_ProbeWinsOverNewerSibling` (:318) — `want 30` → `-1`.
- `TestResolveOwnBootstrapJSONL_WarmStartTailsFromSize` (:409) → rename `…WarmStartTailsFromEnd`; `want 128` → `-1`.
- `TestResolveOwnBootstrapJSONL_NoopProbeFallsBackToMtime` (:458) — `want 20` → `-1` (the fallback is `resolveLatestSessionJSONL`, warm).
- `TestResolveBoundSessionJSONL_KeysOffIDNotMtime` (:489) — `want 40` → `-1`.
- `TestResolveBoundSessionJSONL_WarmStartTailsFromSize` (:542) → rename `…WarmStartTailsFromEnd`; `want 128` → `-1`.
- `TestResolveTarget_BoundSessionWhenRouted` (:822) — `want 30` → `-1`.
- `TestResolveTarget_BootstrapBoundUsesProbeResolver` (:874) — `want 30` → `-1`.
- `TestResolveBootstrapJSONL_PinnedIDPrefersByID` (:1236) — `want len(content)` → `-1`.

**Stays at `0` (do NOT touch):** the three `_ColdStartTailsFromZero` cold assertions and every `("", 0)`-on-not-found / invalid-id assertion (an error path returns offset `0`).

**New behavioral test — AC3.** Add one focused test that proves the sentinel actually skips content that appears *between resolve and tail-open* (the TOCTOU-closing property), driving the resolver into the real tail:

- Scenario (warm): write a `<id>.jsonl` with two entries `[a, b]`; call the resolver once → assert it returns `tuidriver.TailFromEnd`; **append a third entry `c` before opening the tail**; open `tuidriver.TailJSONL(ctx, path, off)`; append `d`; drain and assert the stream yields **only `d`** — neither the history `[a, b]` nor the mid-resolve growth `c` is replayed. (A test may call `TailJSONL` directly; the "no direct production caller" rule is about production code.)
- Scenario (cold, counterpart): a fresh file that appeared after a not-found → resolver returns `0`; `TailJSONL(ctx, path, 0)` streams the file from the top (`[a, b, …]`). This can be a sub-case of the same test or an assertion added alongside an existing `_ColdStartTailsFromZero` test.

Naming/idiom left to the developer (project convention is table-driven where it fits; this one is scenario-shaped, so a single `t.Run`-free function per scenario is fine — mirror the existing resolver tests). Keep the `writeJSONL` / `uuidA` helpers already in the file.

**`make check` green** (AC5): `go vet`, `staticcheck` (watch for the now-unused `info` in `resolveOwnBootstrapJSONL`), `go test -race ./...`.

## Out of scope (explicitly)

- `internal/turnbridge/producer_test.go` — its `newBoundResolve` is a local test double, and the test asserts only the cold-start offset `0` (preserved). Unchanged.
- `internal/transcript` — `Result.Size` stays (Family A consumer).
- The interactive **modal** stream (`interactive_modal_stream_v2.go`, `NewScreenTargetSubscriber` → `ScreenEvents`) — not a tailer, opens no JSONL, has no offset.
- The **contextwindow** reader (full-scan from 0) and **growth-confirm** (size baseline, no tail) — neither consumes a tail offset.
- The **agent-run** PTY path (`internal/agentrun/ptyrunner/runner.go`, hardcoded offset 0) — a separate single-session reader, not part of this consolidation.

## Open questions

None. The sentinel, the seam forwarding, and the consumer set are all verified at refinement (see Files to read first).

## Security review

**Verdict:** PASS

The change *reduces* attack surface: it removes a caller-measured cross-fd offset (the #929/#930 TOCTOU root) and replaces it with an own-fd resolution inside `TailJSONL`. Walked each applicable category:

- **[Trust boundaries]** No findings. The one untrusted→trusted crossing on this path — the probe-reported file path validated by `transcript.GuardProbedPath` + `CanonicalDir` + `ValidStem` — is untouched; the change never widens what path is tailed, only the offset within an already-guarded path. The offset is not attacker-influenced: it is either the fixed sentinel `-1` or `0`, chosen by the resolver's own `resolvedOnce`/`sawEmpty` state.
- **[File operations / TOCTOU]** No findings — this is the category the ticket *improves*. Old: `os.Stat(path)` at resolve-time, seek that size into whatever fd the tail later opens (cross-fd swap window = the bug). New: `TailJSONL` stats and seeks on the *same* fd (`jsonl.go` § "same open fd, so the window between them is not a TOCTOU"), so the swap-during-the-gap attack is structurally gone. The resolver's own `os.Stat` calls remain only as existence/vanish-race gates (their result no longer drives a seek), so no new check-then-use gap is introduced. No path concatenation, permission mode, or symlink handling changes.
- **[Error messages, logs, telemetry]** No findings. The only log touched is `producer.go`'s debug diagnostic, which logs `path` (a UUID filename under the trusted dir) + `offset` + a derived `cold_start` bool — never JSONL bytes. The substrate seal is unchanged; no new field logs content.
- **[Confidentiality — no-history-replay to the internet-exposed phone]** This is the security property the warm branch enforces, and it is **preserved and strengthened**. Warm still resolves to the file's current end (`TailFromEnd` → own-fd size), so a resumed/switch-back transcript's history is never streamed to the phone — and now content written in the resolve→open window is also skipped (own-fd end at open time ≥ caller-stat size at resolve time), closing a small replay window the old scheme left open. The offset-`0` "stream from the top" case remains confined to a genuine cold-start file (fresh, appeared after a not-found), whose whole content *is* the current turn — no prior transcript exists to leak. The mandatory `_WarmStart…` guard tests (renamed, asserting `-1`) continue to pin this; the new AC3 behavioral test adds an end-to-end proof that mid-resolve growth is not replayed.
- **[Concurrency]** No findings. No new goroutines, locks, or shared state; the single-Run-goroutine invariant that already governs `resolvedOnce`/`sawEmpty` is unchanged.
- **[Threat model alignment]** Aligned with `docs/protocol-mobile.md` § Security model's confidentiality posture for the outbound stream (never expose a conversation's prior/other-conversation transcript to the phone). No threat is newly in-scope or newly deferred by this change.
- **[Tokens/secrets], [Subprocess], [Cryptographic primitives], [Network & I/O]** — not applicable: this change touches only an integer offset value on an existing local-filesystem tail path; it introduces no tokens, subprocess execution, crypto, or network I/O.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
