# #2149 — a conversation carries a bounded, persisted system prompt

Store-only slice split from #2094: `internal/conversations` gains a per-conversation
operator-set system prompt, its validated setter, and its on-disk representation.
Nothing reads it and nothing writes it over the wire in this slice — #2150 reads it at
spawn, #2151 sets it over the wire, #2152 reads it back.

## Files read

- `internal/conversations/conversation.go` → `Conversation`, `Conversation.Name`,
  `Conversation.IsArchived` — `Name` is the tri-state `*string` precedent (nil = absent,
  non-nil `""` = explicitly empty) this field copies; `IsArchived`'s doc block is the
  `omitempty` byte-identity contract AC 4 needs, and states why that contract is the
  opposite of `IsPromoted`'s.
- `internal/conversations/registry.go` → `Registry.SetArchived` — the single-field
  setter shape (scan + one assignment under `r.mu`, no implicit `Save`); `Registry.Promote`
  — the validate-before-lock shape returning a distinct naked sentinel per refusal, and
  the `ErrConversationNotFound` arm AC 2 reuses; `Registry.Get` / `Registry.List` — the
  shallow-copy read path that already shares `Name`'s pointer, which the new field joins
  unchanged; `Registry.Save` / `Load` — the encode/decode path AC 3 and AC 4 run through;
  `Registry.Update` — the documented, deliberately-unvalidated escape hatch this ticket
  does not close.
- `internal/conversations/registry_test.go` → `TestRegistry_SetArchived_HitSetsAndClears`,
  `TestRegistry_Load_AbsentArchivedKeyDecodesActive`,
  `TestRegistry_Save_ActiveOmitsArchivedKey`, `TestRegistry_Promote_DoesNotPersist` — the
  four test shapes this slice mirrors; `strPtr` and `mustParseTime` are the existing
  helpers, no new ones needed.
- `internal/conversations/conversation_test.go` → `TestConversation_JSONRoundTrip`,
  `TestConversation_OmitemptyAbsentForUnpromoted` — the type-level round-trip and
  key-absence assertions the three-state test extends.
- `internal/conversations/sweep_loop.go` → `sweepOnce` — the package's **only** logging
  site. It logs a count and a `Save` error, never a record field; AC 5's "nothing in the
  package logs it" therefore holds without a change here, and the new setter takes no
  logger.
- `docs/knowledge/features/conversations-registry.md` § `SetArchived`, § `Promote` —
  the package's stated idiom: named-semantic single-field mutators are dedicated ~10-line
  methods, `Update` stays the ad-hoc escape hatch; sentinels are exported and returned
  naked because the primitive has no context to add.
- `docs/knowledge/features/conversations-package.md` § *Types*, § *`Name` is `*string`,
  not `string`* — the field table and the tri-state rationale.
- `internal/relay/handlers/list_conversations.go`, `promote_conversation.go`,
  `rename_conversation.go` → every outbound reply projects the record into a hand-built
  `protocol.ConversationSummary` / `protocol.ConversationUpdatedPayload` with named
  fields. That projection is the leak barrier: adding a field to `Conversation` cannot
  reach the wire until a sibling slice adds it to a payload type deliberately.

## Context

The only way to shape a session's behaviour today is the workspace instructions file,
which is per-directory — two conversations on the same repository cannot differ. This
slice lands the storage half: a conversation record can hold its own system prompt.

The store is the conversations registry (`~/.pyry/<instance>/conversations.json`) and
deliberately not `sessions.json`: `sessions.Pool.Revive` zeroes `SessionSettings` after a
daemon restart (#1487's bypass revocation), so a prompt held there would fail the
"survives a daemon restart" requirement by design.

No ADR is warranted. The tri-state-pointer decision is already recorded in
`conversations-package.md` § *`Name` is `*string`, not `string`*, and this field is an
application of it, not a new decision. The documentation phase should extend the
`conversations-registry.md` § *Surface* table and add a `SetSystemPrompt` subsection under
*CRUD*.

## Design

Two additive changes to `internal/conversations`. No existing symbol changes signature; no
consumer needs a simultaneous edit.

### 1. `Conversation.SystemPrompt *string` (`conversation.go`)

```go
SystemPrompt *string `json:"system_prompt,omitempty"`
```

Placed after `IsArchived` and before `LastUsedAt`, so `LastUsedAt` stays the last key and
the existing key order is undisturbed. Three states, exactly mirroring `Name`:

| In memory | On disk | Meaning |
|---|---|---|
| `nil` | key absent | no prompt — the default (AC 1, AC 4) |
| non-nil → `""` | `"system_prompt": ""` | explicitly empty |
| non-nil → `"text"` | `"system_prompt": "text"` | operator-set |

`omitempty` on a `*string` tests the **pointer**, not the pointee, so a non-nil pointer to
`""` still serialises its key — which is what makes the empty state distinguishable on
disk. A plain `string` with `omitempty` cannot express this: both states serialise away and
both decode to `""`.

A registry whose rows all hold `nil` emits no `system_prompt` key anywhere, so it is
byte-identical to its pre-change form, and a pre-existing row without the key decodes to
`nil` with no migration step (AC 4) — the same mechanism, and the same rationale, as
`IsArchived`.

Nothing mints the explicitly-empty state today. It exists so the storage contract mirrors
`Name` and so AC 4 holds; no verb or behaviour in this slice depends on it.

### 2. `Registry.SetSystemPrompt` (`registry.go`)

```go
const MaxSystemPromptBytes = 8192

var (
    ErrSystemPromptTooLong     = errors.New("conversations: system prompt exceeds the maximum byte length")
    ErrSystemPromptInvalidUTF8 = errors.New("conversations: system prompt is not valid UTF-8")
)

func (r *Registry) SetSystemPrompt(id ConversationID, prompt *string) error
```

Behaviour: validate the value, then locate the row and assign exactly one field under
`r.mu`. Returns `nil` on success, one of three sentinels on refusal. Does not call `Save` —
persistence stays with the caller, matching `Create` / `Update` / `Promote` / `Delete` /
`RebindSession` / `SetArchived`.

**The `*string` parameter spans all three states through one door.** `nil` returns the row
to "no prompt"; a non-nil pointer stores its pointee after validation. This mirrors
`SetArchived`'s single argument that both sets and clears, and it means #2151's clear path
is the same validated entry point as its set path — the ticket's "every wire verb goes
through one validated door" is only true if the door can also express "none". A
`prompt string` signature would leave clearing reachable only through `Update`, i.e.
outside the door.

**Validation order is value-first, then identity**, matching `Promote`, whose
`ErrPromotionNameEmpty` check likewise precedes the not-found scan:

1. `prompt == nil` → skip to step 4 (clearing needs no value validation).
2. `len(*prompt) > MaxSystemPromptBytes` → `ErrSystemPromptTooLong`. `len` on a Go string
   is bytes, which is the unit AC 2 specifies; `MaxSystemPromptBytes` is inclusive, so a
   value of exactly 8192 bytes is accepted.
3. `!utf8.ValidString(*prompt)` → `ErrSystemPromptInvalidUTF8`.
4. Under `r.mu`: scan for `id`; miss → `ErrConversationNotFound`; hit → assign.

A value that is both over-length and invalid UTF-8 returns `ErrSystemPromptTooLong` —
length is the O(1) gate and must not require scanning an arbitrarily large input first.
A refusal on an unknown id with an over-length value likewise returns the length sentinel;
identical to `Promote`'s ordering, and pinned by a test row so it cannot drift silently.

**Pointer ownership.** The implementation copies the pointee into a fresh local before
taking its address, so the stored pointer never aliases a caller-held variable — the same
defensive idiom `Promote` uses for `Name`. `Get` and `List` continue to copy records
shallowly and share the stored pointer, exactly as they already do for `Name`; no
deep-copying is added here (#2150 and #2152 read through `Get`, and strings are immutable,
so the shared pointer is safe).

**Why the bound is 8192 bytes.** The value must fit inside a v2 application envelope,
capped at 65519 bytes (`docs/protocol-mobile.md` § *Application-envelope size cap*), with
room for the rest of a payload — and it is far above any hand-written channel instruction
(this repo's whole `CLAUDE.md` is under 10 KB). It is deliberately not an argv constraint:
the value reaches claude through a file, never as a command-line value. Bytes, not runes,
because both the registry file and the frame are byte-budgeted.

**Why invalid UTF-8 is refused rather than sanitised.** `encoding/json` substitutes U+FFFD
on marshal, so an invalid value would not survive `Save` → `Load` unchanged (AC 3) and the
substitution can grow it past the bound after admission (a 12-byte input persists as 16).
Refusing at the door is what keeps "stored verbatim" and "round-trips unchanged"
simultaneously true.

### Out of scope for this slice

- No getter. `Registry.Get` already returns a record copy; that is how #2150 and #2152
  reach the value.
- No wire verb, no payload field, no CLI binding — #2151 and #2152.
- `Registry.Update` still bypasses this validation, exactly as it bypasses `Promote`'s.
  Its doc block says so explicitly ("the registry does not validate post-mutation state");
  that escape hatch is for in-daemon callers and is accepted as-is.
- No `LastUsedAt` bump on set — `SetArchived` and `Promote` set neither; a caller that
  wants one calls `Update`.

## Concurrency model

No goroutines, no channels. `SetSystemPrompt` holds `r.mu` across the scan and the single
assignment — one critical section, so there is no find-then-mutate window a concurrent
`Create` / `Delete` could redirect. This is the same no-TOCTOU posture as `Update`,
`Promote`, `RebindSession` and `SetArchived`.

Validation runs **before** the lock: it touches only the caller's value, so holding the
mutex across a scan of up to 8 KB would serialise mutators for no benefit.

Lock order is unchanged and remains one-directional (`saveMu` → `mu`). `SetSystemPrompt`
never takes `saveMu`. The `Save` snapshot copies records shallowly and thereby copies the
`*string` header; because every assignment to the field happens under `r.mu` and the
pointee is an immutable string, a concurrent `Save` either sees the old pointer or the new
one, never a torn value — identical to the existing `Name` situation, and unlike
`SessionHistory`, which needed the append-only argument documented in
`conversations-registry.md` § *Save concurrency*.

## Error handling

| Refusal | Sentinel | Registry state |
|---|---|---|
| `len(*prompt) > MaxSystemPromptBytes` | `ErrSystemPromptTooLong` | untouched — returns before the lock |
| `*prompt` is not valid UTF-8 | `ErrSystemPromptInvalidUTF8` | untouched — returns before the lock |
| `id` not present | `ErrConversationNotFound` (existing) | untouched — returns after the scan, before any assignment |

All three are **static package-level sentinels returned naked**, matching the four
`Promote` sentinels. None is constructed with `fmt.Errorf` and none interpolates the
value, the value's length, or the id — so no refusal path can carry a fragment of the
prompt into a caller's log, a wire error message, or a stack trace (AC 5). Callers
distinguish via `errors.Is`; #2151 maps each to its own reply code.

The happy path has no partial-mutation window: every refusal returns before the single
field assignment, which is the last statement.

`SetSystemPrompt` takes no `*slog.Logger` and the package's only logging site
(`sweepOnce`) logs a count and a `Save` error — never a record field. Nothing in the
package can log the value.

## Testing strategy

Same-package, stdlib only, `t.Parallel()`, table-driven where the cases are homogeneous.
Existing helpers `strPtr` and `mustParseTime` are reused; no new helper.

`internal/conversations/conversation_test.go`:

- **`TestConversation_SystemPromptThreeStates`** — marshal/unmarshal one record per state
  (nil / `strPtr("")` / `strPtr("be terse")`): the `system_prompt` key is absent for nil
  and present-and-empty for the explicitly-empty pointer, and each round-trips
  `reflect.DeepEqual`-equal. Pins AC 1's on-disk *and* in-memory distinguishability at the
  type level. `time.Date(...)` inputs, per the monotonic-clock rule.

`internal/conversations/registry_test.go`:

- **`TestRegistry_SetSystemPrompt_HitStoresVerbatim`** — a seeded fully-populated row
  takes a prompt; asserts the stored value is byte-equal to the input, that every other
  field (`Name`, `Cwd`, `CurrentSessionID`, `SessionHistory`, `IsPromoted`, `IsArchived`,
  `LastUsedAt`) is unchanged, and that the stored pointer is **not** the caller's pointer
  (pins the fresh-local copy).
- **`TestRegistry_SetSystemPrompt_Refusals`** — table, one row per refusal plus the
  ordering case: over-length by one byte; invalid UTF-8 (`"\xff\xfe"`); unknown id;
  over-length **and** invalid UTF-8 → expects `ErrSystemPromptTooLong` (pins length-first);
  unknown id **and** over-length → expects `ErrSystemPromptTooLong`. Each row asserts
  `errors.Is`, that the seeded row's `SystemPrompt` is still nil and its other fields
  unchanged, and that `err.Error()` contains no fragment of the input value (AC 5) — the
  inputs embed a distinctive marker so the containment check is non-vacuous.
- **`TestRegistry_SetSystemPrompt_BoundaryIsBytes`** — exactly `MaxSystemPromptBytes`
  bytes of ASCII is accepted; one more is refused; 4096 two-byte runes (8192 bytes) is
  accepted and 4097 (8194 bytes) is refused. Pins the inclusive bound and bytes-not-runes.
- **`TestRegistry_SetSystemPrompt_ClearAndExplicitlyEmpty`** — `nil` on a row holding a
  prompt returns `nil` and leaves `SystemPrompt == nil`; `strPtr("")` stores a non-nil
  pointer to `""`; `nil` on an unknown id returns `ErrConversationNotFound` (the clear path
  skips value validation but not the identity check).
- **`TestRegistry_SetSystemPrompt_RoundTrip`** — three rows (none / explicitly empty /
  a prompt carrying newlines and non-ASCII text) survive `Save` → `Load`: the raw file
  shows the key absent, present-and-empty, and present-with-the-value respectively, and
  each row's in-memory state after `Load` matches what it had before `Save` (AC 3).
- **`TestRegistry_Save_NoPromptOmitsKey`** — an all-nil registry's `Save` output contains
  no `system_prompt` substring and `Save` → `Load` → `Save` is byte-identical (AC 4,
  mirrors `TestRegistry_Save_ActiveOmitsArchivedKey`).
- **`TestRegistry_Load_AbsentPromptKeyDecodesNone`** — a hand-written registry row with no
  `system_prompt` key loads with `SystemPrompt == nil`, no migration step (AC 4).
- **`TestRegistry_SetSystemPrompt_DoesNotPersist`** — `Save` → `SetSystemPrompt` → `Load`
  from the same path shows `SystemPrompt == nil` on disk (mirrors
  `TestRegistry_Promote_DoesNotPersist`).
- **`TestRegistry_SetSystemPrompt_RefusedValueNeverReachesDisk`** — seed, `Save`, attempt a
  refused store carrying a distinctive marker, `Save` again, assert the marker appears
  nowhere in either file's bytes (AC 5).

RED is established by running the new tests before the production edits: they fail to
compile on the missing field and method, then fail on assertions once the symbols exist
but the validation does not.

Gate: `go test -race ./internal/conversations/...`, `go vet ./...`, `go build ./cmd/pyry`.
The full-module race suite is the verifier's gate.

## Open questions

1. **Does the setter need to express "clear"?** Resolved in the Design section: yes, via
   the `*string` parameter, because #2151 has a clear path and the ticket's "one validated
   door" only holds if clearing goes through it too. No separate `ClearSystemPrompt` verb.
2. **Which sentinel wins when a value is both over-length and invalid UTF-8?**
   Resolved: `ErrSystemPromptTooLong`, pinned by a test row.
3. **Should `MaxSystemPromptBytes` be exported?** Resolved: yes. #2151 must describe the
   bound in its refusal reply, and an exported named constant is better than the wire layer
   hardcoding 8192. It is a constant, not a type, so it does not count against the
   exported-type boundary.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The prompt is operator-supplied and, from #2151 onward, arrives
  over the relay — untrusted at the door. This slice makes `Registry.SetSystemPrompt` the
  single explicit boundary: length and UTF-8 validity are checked in one place, before the
  lock, before any mutation. Downstream holders (`Registry.Get`'s record copy, read by
  #2150 and #2152) receive a value already proven to be ≤ 8192 bytes and valid UTF-8, so
  the invariant is established once rather than re-checked per consumer. The boundary is
  *not* total, and the plan says so in § *Out of scope*: `Registry.Update` can still write
  the field unvalidated. That hole is pre-existing and documented on `Update` itself
  (`Promote` has the identical exposure today); it is reachable only from in-daemon Go
  code, never from a wire frame, and closing it is explicitly not this ticket's problem.
- **[Tokens, secrets, credentials]** Not applicable — the field holds no credential and
  the design mints nothing. Worth stating because the value is nonetheless *operator
  content* and could contain a secret the operator typed: that is what makes the "no
  fragment in any refusal, no logging" requirement (AC 5) a security property and not a
  tidiness one. Both halves are enforced structurally rather than by convention — the
  three sentinels are package-level `errors.New` values returned naked, so no call path can
  interpolate the value into an error, and `SetSystemPrompt` accepts no logger.
- **[File operations]** The value lands only in `conversations.json`, written through the
  existing `Registry.Save` recipe: temp file in the same directory, `0600` explicitly
  chmod'd, fsync, atomic rename; parent directory `0700`. This slice adds no path
  construction, no caller-controlled path component, and no new file. A crash mid-`Save`
  leaves the pre-existing file untouched (rename is the commit point), so a partially
  written prompt is unreachable on disk. **SHOULD FIX / already satisfied:** the file mode
  matters more now that the file can hold operator content — it is `0600` today and the
  Phase-B change must not alter `Save`; `TestRegistry_SaveFilePermissions` already pins it,
  and no new test is needed.
- **[Subprocess / external command execution]** No finding, and this is the category the
  design most deliberately avoids: the value is **never** passed to `exec.Command`. #2150
  will deliver it to claude through a file, so no argv, no `sh -c`, no shell
  interpretation, and no quoting or escaping problem at any layer. The 8192-byte bound is
  therefore sized against the wire envelope rather than `ARG_MAX`, and the design records
  that reasoning so a future slice cannot quietly repurpose the value as a command-line
  argument on the assumption that the bound was an argv constraint.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison against a
  secret, no key material. The one comparison the code performs (`ConversationID` equality
  in the scan) is on a non-secret identifier the caller already supplied, so a
  non-constant-time `==` leaks nothing an attacker does not already hold; this matches
  every other scan in the registry.
- **[Network & I/O]** Input size is capped at 8192 bytes at the only entry point, and the
  cap is inclusive, exported and named. This is the category's core requirement and it is
  met before the value is retained: an over-length prompt is refused without being copied
  into the registry, so an attacker who can reach #2151's verb cannot grow the daemon's
  heap or the registry file past a bounded amount per conversation. The bound sits an order
  of magnitude below the 65519-byte application-envelope cap, so a maximal prompt cannot
  crowd out the rest of a payload. Aggregate growth is bounded by the conversation count,
  which is governed elsewhere (creation verb + auto-archive sweep) and unchanged here.
  Note the deliberate ordering consequence: length is checked before UTF-8 validity, so a
  hostile 10 MB body is rejected in O(1) rather than after a full validity scan.
- **[Error messages, logs, telemetry]** The three refusals are static sentinels with fixed
  text; none names the value, its length, or the conversation id. The package's sole
  logging site (`sweepOnce`) logs an archived count and a `Save` error and is untouched.
  `SetSystemPrompt` takes no logger, so there is no seam through which a future edit could
  add value logging without also changing the signature. Two tests make this checkable
  rather than asserted — the refusal table proves `err.Error()` carries no fragment of a
  marked input, and `_RefusedValueNeverReachesDisk` proves a refused value reaches no file.
  Wire-side exposure is bounded by the projection barrier: every outbound reply builds a
  `protocol.ConversationSummary` / `protocol.ConversationUpdatedPayload` with named fields,
  so this field cannot reach a phone until #2152 adds it to a payload type on purpose.
- **[Concurrency]** Lock order is unchanged and one-directional (`saveMu` → `mu`); the new
  method takes only `mu` and never `saveMu`. Scan and mutation are one critical section, so
  there is no check-then-mutate window a concurrent `Delete` could redirect into the wrong
  row. Validation deliberately runs outside the lock — it reads only the caller's value, so
  it cannot observe registry state and cannot be raced into admitting an over-length value.
  A concurrent `Save` sees either the old or the new pointer, never a torn value, because
  the pointee is an immutable string and every write to the field is under `mu`. No
  goroutine is spawned, so there is nothing to leak.
- **[Threat model alignment]** The relevant `docs/protocol-mobile.md` § *Security model*
  threat — a paired-but-hostile phone sending an oversized or malformed application payload
  — is addressed by the byte cap and the UTF-8 check at the storage door, which is
  upstream of every wire verb the sibling slices will add. The threat this slice explicitly
  does **not** address is authorisation: nothing here decides *who* may set a prompt, and
  nothing here rate-limits how often. Both belong to #2151, which owns the verb, its
  pairing/authorisation posture, and its reply codes; this slice deliberately ships no
  reachable-from-the-network path at all.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
