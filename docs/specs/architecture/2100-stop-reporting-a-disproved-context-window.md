# #2100 — Stop reporting a context window the used count disproves

## Files read

- `internal/contextwindow/usage.go` → `defaultWindowTokens`, `Usage`, `Read` — the
  single place the pair (used, window) is constructed, and the only production
  site that decides what `WindowTokens` is. The collapse lands here.
- `internal/contextwindow/usage_test.go` → `TestRead_Fixtures`, `TestRead_EmptyPath`,
  `TestRead_OpenError` — `TestRead_Fixtures` asserts `got.WindowTokens != defaultWindowTokens`
  in shared code for every row, so an over-window fixture cannot be appended
  without a per-row want. `TestRead_EmptyPath` and `TestRead_OpenError` pin the
  two non-contradiction zero paths and must stay green untouched.
- `cmd/pyry/snapshot_usage.go` → `snapshotUsageFor`, `bootstrapSnapshotUsage` — the
  seam both wire payloads share. `snapshotUsageFor` discriminates its recovery on
  `err != nil`, never on the window value, and its recovery is `Read("")`; the
  contradiction is not an error, so the branch is undisturbed.
- `cmd/pyry/snapshot_usage_test.go` → `writeUsageTranscript`, `defaultWindow`,
  `TestSnapshotUsageFor_ReadsTheBoundTranscript`,
  `TestSnapshotUsageFor_UnresolvableIDsReportFreshSession`,
  `TestBootstrapSnapshotUsage` — `writeUsageTranscript` already writes a real
  transcript to disk with the four usage fields as arguments, which is exactly the
  "from disk, through the shared seam, not handed the answer" rig AC 1 asks for.
  The `defaultWindow` mirror comment repeats the stale "every current model"
  claim and is corrected with it.
- `internal/protocol/snapshot.go` → `ScreenSnapshotPayload` — carries the second
  production copy of the 200K claim and the `window_tokens == 0` contract.
- `internal/protocol/settings.go` → `SessionSettingsPayload` — carries the same
  zero contract in bullet form (no 200K claim of its own).
- `internal/relay/v2session_seams.go` → `V2SessionConfig.SnapshotUsage`,
  `V2SessionConfig.RunConfigFor` — both seams are primitive-typed pass-throughs;
  they document nil ⇒ zeros but assert nothing about the window's value, so
  neither needs a change.
- `docs/knowledge/features/contextwindow-package.md` § "Context-window size —
  single documented default" — records the deliberate no-per-model-map decision
  and names the seam this ticket does *not* take (reading `message.model`). This
  ticket adds no model knowledge; it only compares two integers already in hand.
- Repo-wide sweep `git grep -nE '200000|200_000|200K' -- '*.go' ':!*_test.go'
  ':!*/testdata/*'` → exactly the two production sites the ticket names, plus
  nothing else. Confirms AC 3's scope is closed.

## Context

`contextwindow.Read` reports `defaultWindowTokens` (200K) as the window for every
session, and the const's own comment asserts that every current Claude model
exposes 200K. A 1M-context session exists and was measured live on 2026-09-04:
the latest usage-bearing entry summed to 223075, which against the asserted
200000 is 111%. The client clamps to 100%, so the gauge sits at a confident
"full" precisely when the daemon's belief is provably wrong. The clamp is
correct; it is what hides the overflow.

The daemon can detect this without knowing anything about models: a used count
above the believed window is proof the belief is wrong. This ticket makes it stop
asserting the disproved window. It does **not** source a real window — that is
#2101 (decode it off claude's stream) and #2102 (report it).

The reporting value needs no protocol change. Both wire payloads already carry
`window_tokens`, and 0 already means "no reading available"; the desktop already
renders nothing on that arm rather than a degraded reading. The widening is from
"unwired" to "no trustworthy reading", which the existing "treat as unavailable"
behaviour already covers. The rejected alternative — a new wire field separating
"unwired" from "disproved" — buys a distinction no client renders differently at
the price of a protocol change plus tickets in two other repos.

No ADR is warranted. The decision this records (widen a zero's meaning rather
than add a field) is local to one package pair and is captured in the doc
comments it edits; the documentation phase can fold the lesson into
`contextwindow-package.md`.

## Design

### Where the collapse lands: inside `contextwindow.Read`

The ticket leaves the choice open between `Read` and the `cmd/pyry` seam. It
lands in `Read`, for three reasons:

1. `Usage` is the type that carries the pair, so the invariant "never report a
   window this used count disproves" is a property of the pair and belongs where
   the pair is constructed. At the seam it would be a rule about two ints that
   have already escaped the type that owns them.
2. Both wire payloads inherit it either way (they funnel through
   `snapshotUsageFor`), but at the seam `Read` would keep returning a
   self-contradictory `Usage` to every other caller — and #2101/#2102 add callers
   to this exact file.
3. `snapshotUsageFor`'s recovery branch discriminates on `err != nil`, not on the
   window value, so a contradiction branch inside `Read` cannot disturb it. Its
   recovery is `Read("")`, which reports `(0, defaultWindowTokens)` and is not a
   contradiction, so `cmd/pyry/snapshot_usage_test.go`'s recovery assertion —
   commented as the sole red for that branch — still discriminates.

### The change

In `Read`, after summing the latest usage-bearing entry's four fields:

```go
if usage.UsedTokens > usage.WindowTokens {
    usage.WindowTokens = 0
}
```

Compared against `usage.WindowTokens` rather than the const, so the guard reads
as "above the *believed* window" and stays correct when #2101 makes the believed
window per-session.

Three properties, all deliberate:

- **Strictly greater.** Equality is not a contradiction: a session exactly at its
  window is full, not evidence of a wrong belief. It keeps its window (AC 2).
- **Inside the `last != nil` arm only.** With no usage entry, `UsedTokens` is 0
  and `0 > 200000` is false anyway; keeping it inside the arm makes the "fresh
  session" report structurally unreachable from the collapse rather than
  arithmetically unreachable.
- **`UsedTokens` is untouched.** It still carries the true sum. Only the
  denominator disappears.

### Contracts restated (no signature changes)

- `defaultWindowTokens` — its comment stops asserting that every current model is
  200K. New meaning: the window the daemon assumes when it has nothing better,
  and a used count above it disproves the assumption.
- `Usage.WindowTokens` — the context-window size, or 0 when `UsedTokens` disproved
  it.
- `ScreenSnapshotPayload` (`internal/protocol/snapshot.go`) — drop "200000 for
  every current model today"; the `window_tokens == 0` sentence names both cases:
  the seam was not wired, and the used count disproved the window. State that
  `used_tokens` stays meaningful in the second case.
- `SessionSettingsPayload` (`internal/protocol/settings.go`) — same widening on
  its `WindowTokens 0` bullet.

Nothing in `internal/relay` changes: `SnapshotUsage` and `RunConfigFor` are
primitive-typed pass-throughs that assert nothing about the window's value.

## Concurrency model

Unchanged. `Read` stays a stateless open→scan→close call with no goroutines,
channels, or shared state; the added comparison is on two locals. Safe for
concurrent use, as before.

## Error handling

Unchanged, and deliberately so — the contradiction is **not** an error:

- `path == ""` → `(0, defaultWindowTokens)`, nil. Not a contradiction.
- Scanned, no usage entry → `(0, defaultWindowTokens)`, nil. Not a contradiction.
- `os.Open` / scan failure → zero `Usage` plus a wrapped error, as today.
- Scanned, latest sum > believed window → `(sum, 0)`, **nil error**. A wrong
  belief is a fact about the data, not a read failure, and reporting it as an
  error would route it into `snapshotUsageFor`'s recovery, which would report the
  disproved window right back.

## Testing strategy

Every case below is a red before the change and green after.

**AC 1 — from disk, through the shared seam** (`cmd/pyry/snapshot_usage_test.go`).
A new table-driven test over `snapshotUsageFor`, seeding a real transcript with
`writeUsageTranscript` and reading it back by id. This is the seam
`session_settings` and `screen_snapshot` share, and nothing hands it the answer.
Rows:

- the live-observed 2 / 950 / 221118 / 1005 → used 223075, window **0**
- a sum exactly at the believed window (150000 / 20000 / 25000 / 5000 = 200000) →
  used 200000, window 200000
- a sum just below it (199999 total) → used 199999, window 200000

The boundary trio is what makes the guard's `>` non-arbitrary: drop the strictness
and the equality row reddens; drop the collapse and the over-window row reddens.

**AC 1/2 unit level** (`internal/contextwindow/usage_test.go`). `TestRead_Fixtures`
gains a per-row `wantWindow` (its shared assertion currently pins
`defaultWindowTokens` for every row, so an over-window fixture cannot be
appended), plus two fixtures:

- `over_window.jsonl` — the live-observed entry, sum 223075 → window 0
- `exactly_window.jsonl` — sum exactly 200000 → window `defaultWindowTokens`

The three existing rows keep `defaultWindowTokens` as their want.

**AC 2 — the other two of the four cases are already pinned and stay untouched.**
A fresh session is `TestRead_EmptyPath` and the `no_usage.jsonl` row of
`TestRead_Fixtures`; an ordinary turn under the window is the `latest_turn.jsonl`
and `compaction_reset.jsonl` rows plus
`TestSnapshotUsageFor_ReadsTheBoundTranscript`; the unwired seam is
`TestSnapshotUsageFor_UnwiredReturnsNilSeam` and `TestBootstrapSnapshotUsage`'s
nil-seam subtests. That these keep passing with no edit *is* the byte-identical
proof. The recovery branch's `window != defaultWindow` assertion in
`TestSnapshotUsageFor_UnresolvableIDsReportFreshSession` is re-checked to confirm
it still discriminates.

**AC 3 — comment-only.** No test; verified by the repo-wide sweep recorded under
"Files read", re-run after the edits.

Gate: `go test -race ./internal/contextwindow/... ./internal/protocol/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Does `cmd/pyry/snapshot_usage_test.go`'s `defaultWindow` mirror comment
   count as in scope?** It repeats the exact stale claim AC 3 removes ("the
   window size every current model exposes"), in a file this ticket edits anyway.
   Resolution intent: correct it — it is a two-line comment in a file already
   being changed, and leaving the removed claim alive in a mirror of the const
   would defeat the sweep.
2. **Is `>` or `>=` right at the boundary?** Resolved by AC 2, which names
   equality explicitly: "equality is not a contradiction and keeps its window".
   Recorded here because the guard is one character and the ACs are the only
   thing pinning which one.
