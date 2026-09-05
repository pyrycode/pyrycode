# #2118 — the per-model window join tolerates one trailing variant group

The context gauge on a 1M-context session draws nothing. `contextwindow.Read`'s
join is an exact map lookup between two claude-authored channels that spell the
same model differently: the `result` line keys its `modelUsage` map
`claude-opus-5[1m]`, the transcript writes `"model": "claude-opus-5"`. The lookup
misses on every turn of every 1M session, falls back to `defaultWindowTokens`,
and the used count then disproves that fallback — so `Read` correctly reports
window 0, and 0 is the client's "no reading exists" signal.

This ticket makes the join fall back, after an exact match has missed, to a key
that is the transcript's id plus ONE trailing bracket group; and it fixes the
full-stack e2e that could not have caught the divergence because both of its
halves were written from one constant.

## Files read

- `internal/contextwindow/usage.go` → `Read`, `defaultWindowTokens`, `Usage` —
  the join itself, the "EXACT and VERBATIM" paragraph to amend rather than
  delete, and the empty-id guard that must survive the change.
- `internal/contextwindow/usage_test.go` → `TestRead_Fixtures`, `TestRead_Join`,
  `TestRead_DoesNotRetainOrMutateWindows` — `TestRead_Join` is the exact-match
  regression floor; every row of it must pass unchanged (AC 2), so the new rule
  gets its own table rather than rows appended here.
- `internal/contextwindow/testdata/over_window.jsonl` — names `claude-opus-5`
  and sums to 223075 (the live 2026-09-04 reading). It is already the transcript
  half of AC 1; no new fixture is needed.
- `internal/contextwindow/testdata/no_model.jsonl` — the entry with no
  `message.model`, which is how the empty-base rule is tested from the
  transcript side.
- `internal/relay/v2session_settings.go` → `validModel`, `modelWordByte`,
  `modelAlnumByte` — the repo's existing machine-checked grammar for exactly
  this shape (#1838). Its structure is the model to mirror; its code is not
  shared (different package, different sink — see Design).
- `cmd/pyry/session_model_window_lookup.go` → `sessionModelWindows` — states
  outright that the comma-ok is the ONLY filter and that every rule lives in
  `Read`. This is why no normalisation may be added at the lookup seam.
- `internal/streamsup/parser.go` → `maxModelWindowID`, `maxModelWindowEntries`,
  and the `modelUsage` decode loop — establishes that the key reaches `Read`
  VERBATIM (only a 256-byte id bound and `contextWindow > 0` are applied), so a
  bracketed key does survive the parser; and that the map is capped at 16
  entries, which bounds the new scan in production.
- `internal/e2e/relay_v2_stream_model_window_test.go` →
  `TestRelayV2_StreamSessionSettingsReportsTheObservedWindow`, `transcriptModel`
  — the e2e whose two halves were written from one constant (AC 4).
- `internal/e2e/internal/fakeclaude/main.go` → `riderModelWindowSonnetID`,
  `riderModelUsage`, `outModelUsage`, `envStreamModelWindows` — the rider that
  supplies the window half of that e2e.
- `internal/e2e/internal/fakeclaude/stream_detect_test.go` →
  `TestRunStreamJSON_ModelWindowRider` — the rider's own unit test, the only
  other reader of the renamed constant.
- `docs/knowledge/features/contextwindow-package.md` § "Context-window size — a
  believed default, not an asserted fact" — records the join's exact-match rule
  and the evidence behind it (26 of 30 captures are an alias pair for one model;
  no rule relating a dated to an undated spelling survives). That evidence is
  what keeps the tolerance to a bracket group and nothing wider.

## Context

`Read`'s join is the meeting point of two channels that are individually correct
and were never checked against each other. #2086 recorded that the standard and
1M variants "write the identical string" — true of the TRANSCRIPT, and still
true. #2107 correctly moved the window onto claude's stream, which DOES carry
the variant suffix. Nobody put the two facts side by side, and the one session
shape the whole family exists to serve is the one where they differ.

The alternative shape — sourcing the used count from the same `modelUsage` entry
the window comes from, so numerator and denominator share one key — is
DISPROVED, and the ticket carries the measurement: `modelUsage`'s token counts
are a running total across the session (72659 → 146699 → 221733 across three
consecutive `result` lines of one session in
`internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_control_bypass.json`),
while the transcript's are the latest entry's CURRENT context size. A running
total is not a numerator for a context gauge: after a compaction it keeps
climbing while the real context shrinks, which would destroy the
last-usage-wins compaction behaviour `Read` gets for free. So the join stays,
and the key is normalised.

**No ADR is warranted.** This narrows one documented rule inside one function;
it introduces no new decision the package overview cannot carry. The
documentation phase should fold the amended rule into
`docs/knowledge/features/contextwindow-package.md` § "Context-window size — a
believed default, not an asserted fact", replacing the flat "exact, verbatim
string comparison" sentence with exact-first / one-variant-group-second.

## Design

### The rule

Inside `Read`, after the existing exact lookup misses, one fallback:

> A windows key matches when it is the transcript's model id followed by exactly
> one trailing bracket group. If EXACTLY ONE such key carries a positive window,
> that window is reported. Zero such keys, or two or more, is a MISS — the same
> fallback to `defaultWindowTokens` a miss has always taken.

Ordering is load-bearing and unchanged in shape from #2107/#2100: exact match,
then variant match, then the contradiction check against whatever was resolved.
The check still runs LAST, so a 223075-used session on a variant-resolved 1M
window reads 22% rather than "no window".

### Two unexported helpers in `internal/contextwindow`

```go
// variantBase reports the base of a windows key carrying one trailing bracket
// group, and whether the key has that shape at all.
func variantBase(key string) (string, bool)

// variantWindow reports the single positive window observed under a key that is
// model plus one trailing bracket group, and whether exactly one such key exists.
func variantWindow(windows map[string]int, model string) (int, bool)
```

`variantBase` mirrors `validModel`'s grammar (#1838) — index of the first `[`,
the value's LAST byte must be `]`, the interior non-empty and drawn from the
closed class `[A-Za-z0-9._-]`, and a base of `""` refused. Between them those
conditions rule out a nested group, a second group, and a trailing suffix after
the group, without any of the three needing a check of its own: no interior byte
can be a bracket, so nothing inside can open or close another group.

**It is a MIRROR, not a shared function.** `validModel` lives in
`internal/relay` for argv and turn-text safety on a value inbound from a phone;
this one lives in `internal/contextwindow` and decides whether two
claude-authored strings name one model. Different package, different threat,
different failure meaning — sharing would couple a wire validator to a join rule
and make either one's evolution a change to the other. It also deliberately does
NOT copy `validModel`'s checks on the BASE's own bytes or its 64-byte bound:
here the base is only ever compared for equality against the transcript's id and
never reaches a sink, and the id is already bounded at 256 bytes by
`maxModelWindowID` upstream. Adding a byte-class check on the base would refuse
a legal exact-equal match for a reason this package has no stake in.

`variantWindow` is a linear scan that returns on the SECOND match. Two
properties follow, and the second is why "refuse on ambiguity" is the right rule
independent of the AC that asks for it:

- A non-positive value is skipped BEFORE it is counted, so it can neither be
  reported (`Read`'s existing "a non-positive observed value is not a window and
  is treated as absent") nor manufacture an ambiguity out of an entry that is
  absent by contract.
- Go randomises map iteration. "Report the first match" would make the reported
  window differ between two runs on identical input. Requiring exactly one is
  what makes the answer a function of the data alone.

### The join, after the change

```go
if lastModel != "" {
    if observed, ok := windows[lastModel]; ok && observed > 0 {
        usage.WindowTokens = observed
    } else if variant, ok := variantWindow(windows, lastModel); ok {
        usage.WindowTokens = variant
    }
}
```

The `lastModel != ""` guard is untouched and stays the whole of the "empty is a
miss on either side" rule. `variantBase`'s own refusal of an empty base is a
second, independent closure of the same path: a key `[1m]` has base `""`, and
`""` can never equal a `lastModel` that reached this line. Both are deterministic
code, and the helper's contract is then checkable in isolation rather than by
reading its caller.

### Direction: one-way, deliberately

The tolerance runs transcript-base → windows-suffixed ONLY. The reverse — a
transcript naming `claude-opus-5[1m]` matching a windows key `claude-opus-5` —
is not implemented. The measured divergence is one-directional (the stream
carries the suffix, the transcript does not), and a rule for a direction nobody
has observed is a defense for a failure mode that has not happened.

### What is NOT touched

- **`Usage` gains no model field.** The doc's argument for that absence is
  unchanged: an integer is still the only thing that leaves this package.
- **`sessionModelWindows` gains no filter.** Its doc states the reason — a
  second spelling of the join's rules is a second place they can drift.
- **`Read`'s signature is unchanged**, so there are zero consumer call sites to
  update.
- **The "EXACT and VERBATIM" paragraph is AMENDED, not deleted.** Its stated
  evidence — one model under a dated and an undated id at one window, a
  genuinely different model at another — still forbids lowercasing, date
  stripping and alias expansion. Only the trailing bracket group is tolerated,
  and only after an exact match has missed.

### The e2e fake (AC 4)

`internal/e2e/internal/fakeclaude/main.go`: `riderModelWindowSonnetID` becomes
two constants — `riderModelWindowSonnetBase` (`claude-sonnet-5`, what a
transcript names) and `riderModelWindowSonnetKey` (the base plus `[1m]`, what
the `modelUsage` map is keyed by). `riderModelUsage`'s `CanonicalModel` field
keeps the BASE, since that is what the field means; only the map key carries the
group. The rider's numeric fields stay copied field-for-field from
`permission_mode_switch_v2.1.239_plan.json`; the key's SUFFIXED SHAPE is
provenance #2118 — a live probe recorded on that issue, since no committed
capture in the tree carries a bracketed `modelUsage` key.

`TestRelayV2_StreamSessionSettingsReportsTheObservedWindow`'s `transcriptModel`
stays `claude-sonnet-5`. The two halves are then written from two different
strings, which is the defect the harness was reproducing. The test goes RED on
current code (exact miss → default 200000 → 223075 disproves it → window 0
against an expected 1000000) and green after.

## Concurrency model

None added. `variantBase` is pure. `variantWindow` reads a caller-supplied map
and returns two values; it neither mutates nor retains it, so
`TestRead_DoesNotRetainOrMutateWindows`' contract and `Read`'s "safe for
concurrent use over one shared map" claim both hold unchanged. No goroutine, no
lock, no shared state.

## Error handling

No new failure mode and no new error return. Every rejection the new rule can
make — no bracket group, a group that is not final, an empty group, an interior
byte outside the closed class, an empty base, two or more candidates — resolves
to the SAME outcome the join has always had for a miss: `defaultWindowTokens`,
which the contradiction check then collapses to 0 if the used count disproves
it. `Read`'s four-way error split (empty path, no usage entry, disproved window,
I/O failure) is untouched.

## Testing strategy

`TestRead_Join` is NOT edited — it is the exact-match regression floor, and AC 2
requires every one of its rows to pass unchanged.

**New `TestRead_VariantJoin`**, table-driven over the committed fixtures (all
rows sum to 223075, so `wantWindow` alone says whether the join fired):

- AC 1 — `claude-opus-5[1m]` at 1M against `over_window.jsonl` reports 1000000.
- AC 2 — both `claude-opus-5` at 400000 and `claude-opus-5[1m]` at 1M present;
  reports 400000. The exact key's window is deliberately ABOVE the used count so
  the row distinguishes three outcomes rather than two: exact-wins (400000),
  variant-wins (1000000), miss (0).
- AC 3, still a miss — a date suffix (`claude-opus-5-20260101`), a case change
  (`Claude-Opus-5[1m]`), an alias (`opus[1m]`).
- AC 3, ambiguity — `claude-opus-5[1m]` and `claude-opus-5[2m]` both present is
  a miss, not a guess.
- Malformed groups, each a miss — a group that is not final
  (`claude-opus-5[1m]x`), an empty group (`claude-opus-5[]`), an interior byte
  outside the class (`claude-opus-5[1 m]`), a nested group
  (`claude-opus-5[a[b]]`).
- Non-positive — `claude-opus-5[1m]` at 0 is a miss; and its PAIR,
  `claude-opus-5[1m]` at 0 beside `claude-opus-5[2m]` at 1M, reports 1000000,
  pinning that an absent entry cannot manufacture an ambiguity.
- Empty base — `[1m]` against `no_model.jsonl` (transcript side empty) and
  against `over_window.jsonl` (windows side base empty) are both misses.

**New `TestRead_MeasuredChannelDivergence`** (AC 5), one test pinning BOTH
halves against the strings measured on 2026-09-05 and naming #2118 as their
provenance: it reads `over_window.jsonl`'s bytes and asserts the literal
substring `"model":"claude-opus-5"` is present (the transcript half — a
non-empty needle, so the containment check cannot go vacuous), then joins that
fixture against a map keyed by the literal `claude-opus-5[1m]` (the result-line
half) and asserts 223075 / 1000000. A future divergence between the two channels
then fails a test rather than blanking a gauge.

**`TestRunStreamJSON_ModelWindowRider`** — three references follow the constant
rename; its `len(got) != 2` and `contextWindow == 1000000` assertions are
unchanged.

**`TestRelayV2_StreamSessionSettingsReportsTheObservedWindow`** — no assertion
changes; the fake's key change alone is what makes it discriminating, and its
doc gains the sentence naming why the two halves are now two strings.

Gate: `go test -race ./internal/contextwindow/... ./internal/e2e/...`,
`go vet ./...`, `go build ./cmd/pyry`. The e2e half needs `-tags e2e`.

## Open questions

1. **Does a bracketed key actually survive `internal/streamsup`'s decode?**
   RESOLVED at plan time by reading the `modelUsage` loop: the key is taken
   VERBATIM, with only `maxModelWindowID` (256 bytes) and `contextWindow > 0`
   applied. A bracketed key reaches `Read` intact, so the fix is reachable
   end-to-end and needs no parser change.
2. **Should `variantBase` also bound the base's length or byte class?** RESOLVED
   as no — see Design. Record the decision in a `## Revisions` entry if
   implementation contradicts it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The design adds no boundary and moves
  none. Both halves of the join were ALREADY claude-authored, unsanitized text:
  the windows keys arrive through `internal/streamsup`'s `modelUsage` decode
  (verbatim, 256-byte bounded) and `cmd/pyry`'s `sessionModelWindows`, and the
  transcript id through `jsonl.Event.Model`. What changes is the PREDICATE
  relating them, not their provenance. The narrowing that makes this safe is
  unchanged and explicitly preserved: `Usage` gains no model field, so the only
  value crossing out of the package is still an integer — no id reaches a log
  line, an argv token, or a wire frame. The boundary stays a single function,
  `Read`, per `sessionModelWindows`' stated rule that no filter is duplicated at
  the lookup seam.
- **[Trust boundaries — the ticket's named threat: a hostile key claiming
  another model's base]** No MUST FIX, and the ambiguity rule is what discharges
  it. A key `M[anything]` can now influence the window reported for a transcript
  naming `M`. Three properties bound that: (a) the base must equal the
  transcript's id EXACTLY — no prefix, no fold, no substring, so a key cannot
  claim a base it does not spell; (b) two or more candidate keys REFUSE rather
  than guess, so an added key cannot displace an existing one's answer, it can
  only collapse both to the documented miss; (c) an exact key still wins
  outright, so a variant key can never override a base key that is present. The
  worst reachable outcome is a wrong integer denominator on a gauge — the same
  class of outcome an exact-match key already had, since claude authors both
  channels and a wrong `contextWindow` on an exactly-matching key was always
  believed. No new class of consequence is opened.
- **[Trust boundaries — empty base]** No MUST FIX. Two independent closures, per
  the technical note that no stripping rule may open a path where an empty base
  matches: `Read`'s `lastModel != ""` guard means the lookup never runs for an
  empty transcript id, and `variantBase` refuses a base of `""` outright, so a
  windows key `[1m]` is unreachable from either side. `internal/streamsup` does
  retain an entry keyed `""` when its window is positive, and it sorts first.
- **[Tokens, secrets, credentials]** Not applicable, and by construction rather
  than by omission: this package handles no credential and its inputs are a
  transcript path and a map of ints. Model ids are non-secret, and the #833
  posture that keeps them out of logs is enforced here by there being no logger
  in `Read`, `variantBase` or `variantWindow` — and none is added.
- **[File operations]** No findings. No path is constructed, joined, or
  canonicalised; `Read` opens the path it is given and the plan does not change
  that. No file is created, so no mode question arises. The new fixtures are
  table rows in existing test files, not new testdata.
- **[Subprocess / external command execution]** No findings for the join. Worth
  stating explicitly because it is the reason `validModel` is MIRRORED rather
  than SHARED: `validModel`'s byte class exists for an argv sink and a turn-text
  sink, and a value it accepts is interpolated onto a live child's stdin. Nothing
  in `internal/contextwindow` reaches either sink — a matched key is compared and
  discarded, and only an int leaves. Sharing the function would create a coupling
  where relaxing one package's rule silently relaxes the other's.
- **[Cryptographic primitives]** Not applicable. No randomness, no comparison
  against a secret, no key material. The string comparisons are `==` on
  non-secret, non-credential model ids, so `crypto/subtle` is not owed —
  timing-safety would protect nothing here.
- **[Network & I/O — resource exhaustion, the one new cost]** SHOULD FIX,
  addressed in the design and re-checked in Phase B. The variant fallback adds a
  LINEAR SCAN over `windows` where an exact miss previously cost one hash lookup.
  Bounds: in production the map is capped at `maxModelWindowEntries` (16) and
  each key at `maxModelWindowID` (256 bytes), so the scan is at most 16 × 256
  byte-comparisons per `Read` — and `Read` fires on a settings read or a
  snapshot, not per turn. On the exported signature a caller may supply a larger
  map, but the map is already fully materialised in the caller's memory before
  the call, so the scan is proportional to an allocation that already exists and
  amplifies nothing. The scan is also reached ONLY after an exact miss. No cap is
  added inside `Read`: capping a caller-supplied map would silently discard
  entries and give the join a second, undocumented miss reason. Phase B must not
  introduce a per-key allocation in the scan — `variantBase` returns a substring
  of the key and must keep doing so.
- **[Error messages, logs, telemetry]** No findings. No error is added, no log
  line is added, and the failure of every new rejection is the SAME silent
  fallback the join already had — so no rejection is distinguishable from
  another by an observable, which is the posture the sibling validators in
  `internal/relay` hold deliberately. A "why did this key not match" debug line
  is exactly the channel #833 closes and MUST NOT be added for debuggability.
- **[Concurrency]** No findings. Both helpers are pure reads. No lock is taken,
  so no lock-ordering question arises; no goroutine is spawned, so no lifecycle
  question arises. `Read`'s "windows is READ ONLY, neither mutated nor retained"
  contract is preserved — `variantWindow` takes the map, ranges it, and returns
  two scalars, retaining nothing — and `TestRead_DoesNotRetainOrMutateWindows`
  continues to pin it.
- **[Threat model alignment]** No new relay-surface exposure, so
  `docs/protocol-mobile.md` § Security model needs no re-derivation: the value
  crossing to `session_settings` and `screen_snapshot` is an integer whose
  contract (`0` means no trustworthy reading) is unchanged, and this ticket only
  changes which integer is chosen. OUT OF SCOPE and named as such: persisting the
  observed window across a daemon restart, which the package overview already
  records as a deliberately open gap with no successor ticket — this ticket does
  not narrow or widen it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05
