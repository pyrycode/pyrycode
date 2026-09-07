# #2206 — Per-workspace label on the conversations registry, keyed by conversation cwd

Storage half of the workspace-label family (#2207 write verb, #2208 list read, #2210 push
frames). This slice lands the persisted map and its two accessors; nothing sets it over the
wire and nothing reads it onto a payload yet.

## Files read

- `internal/conversations/registry.go` → `registryFile`, `Registry`, `Load`, `Save`,
  `SetSystemPrompt`, `SetArchived`, `Get` — the envelope struct the new key hangs off, the
  snapshot-copy line the map copy joins, and the two accessor shapes this slice mirrors
  (`Get`'s `(value, bool)` read, `SetSystemPrompt`'s nullable-pointer write that both sets
  and clears through one door).
- `internal/conversations/conversation.go` → `Conversation.Cwd`, `Conversation.IsArchived`,
  `Conversation.SystemPrompt` — `Cwd` is the key's origin and its doc records that it holds a
  `$HOME`-confined realpath set by `change_workspace`; the other two carry the `omitempty`
  contract prose this field's doc copies ("an absent key decodes as the default, with no
  migration step").
- `internal/conversations/registry_test.go` → `TestRegistry_Save_NoPromptOmitsKey`,
  `TestRegistry_Load_AbsentPromptKeyDecodesNone`, `TestRegistry_Save_ActiveOmitsArchivedKey`,
  `mustParseTime` — the byte-stability pair to model AC3's tests on, plus the shared helper.
  `strPtr` lives in `conversation_test.go`, same package.
- `docs/knowledge/features/conversations-registry.md` § *Save concurrency*, § *Sort
  discipline*, § *Load semantics*, § *`SetSystemPrompt`* — the `saveMu → mu` lock order the
  map copy must not disturb; the byte-identical-output discipline the new key must not break;
  and the measured fact that `encoding/json` substitutes U+FFFD when **decoding** a Go string,
  which is why the wire path cannot hand this layer invalid UTF-8 (bears on § Error handling).
- `docs/knowledge/features/conversations-registry.md` § `SetSystemPrompt`, the envelope-cap
  bullet — "Re-derive the escaped-worst-case arithmetic before reusing this bound in a
  list-shaped payload." #2208 embeds this label in exactly such a payload; carried as a
  finding rather than silently inherited.

## Context

The daemon knows nothing about a workspace beyond the `cwd` string on each conversation, so
every client derives a workspace's display name from that path's last segment. Juhana ruled on
2026-09-06 that a workspace's name travels with the workspace rather than living in one
client's local state, which makes the daemon the only correct owner.

The envelope's reserved room (the `registryFile` doc comment) is what this slice spends: a new
top-level key beside `conversations`, not a per-conversation field. That placement follows from
the ruling — the label belongs to the workspace, and a workspace has no row of its own; N
conversations can share one `cwd`, and a per-row copy would need N-way write fan-out and could
disagree with itself.

No ADR is warranted. The design adds no new pattern: it is the third instance of the
`omitempty` absent-key-is-the-default contract this file already enforces for `is_archived`
(#880) and `system_prompt` (#2149), and the accessor pair copies shapes already documented in
the package overview.

## Design

One production file: `internal/conversations/registry.go`.

**Envelope.** `registryFile` gains

```go
WorkspaceLabels map[string]string `json:"workspace_labels,omitempty"`
```

`Registry` gains a matching unexported `workspaceLabels map[string]string`, guarded by the
existing `mu`. `Load` passes `rf.WorkspaceLabels` straight through — a file with no key yields
`nil`, which reads correctly as "no labels" without an allocation or a migration step.

`omitempty` on a map tests **length, not nilness**, so both a nil map and an allocated-then-
emptied one serialise to nothing. That is what makes the set-then-cleared case indistinguishable
on disk from the never-set case, and it is why `Save` may copy unconditionally (below) without
breaking AC3.

`encoding/json` sorts map keys when marshalling, so the new key needs no counterpart to the
existing `sort.SliceStable` discipline: output stays byte-identical for the same logical
content regardless of insertion order.

**Read accessor.**

```go
func (r *Registry) WorkspaceLabel(cwd string) (string, bool)
```

Returns the label stored for `cwd` and whether one is set — `Get`'s shape, one key per call.
It deliberately does **not** hand back the map: a shared map ranged over outside `r.mu` while a
writer mutates it is not a data race the detector reports but a fatal `concurrent map iteration
and map write` runtime throw that kills the daemon, and no test written without concurrency in
mind would trip it. The per-key signature makes the escape structurally impossible rather than
forbidding it in prose. Both consuming slices (#2208, #2210) call it once per conversation
behind a consumer-named narrow interface that `*conversations.Registry` satisfies structurally,
so per-key is also what the family expects.

**Write accessor.**

```go
func (r *Registry) SetWorkspaceLabel(cwd string, label *string)
```

Nullable so one door both sets and clears, mirroring `SetSystemPrompt`'s `*string` and
`SetArchived`'s single `bool`. A non-nil pointer stores its pointee (copied into a fresh local
first, so the map never aliases a caller-held variable — the idiom `Promote` uses for `Name` and
`SetSystemPrompt` for its prompt); `nil` **deletes** the key, so the read reports absent rather
than a present empty string. The map is lazily allocated on the first store, so a registry that
never sets a label never allocates one.

No return value and no failure mode. In particular it does not check `cwd` against the
conversation list — #2207's handler owns the not-found refusal — and it does not validate the
label. Non-blank and length bounds belong to the wire handler; this layer stores what it is
given, under the key it is given. It does not call `Save`, matching the
`Create`/`Update`/`Promote`/`Delete`/`RebindSession`/`SetArchived`/`SetSystemPrompt` convention.

**Save.** The existing snapshot critical section gains a map copy beside the slice copy, then
encodes `&registryFile{Conversations: snapshot, WorkspaceLabels: labels}`. The copy is
unconditional; an empty copy still omits the key, per the `omitempty` semantics above.

## Concurrency model

No new goroutine, no new lock, no change to lock order. Both accessors take only `r.mu`;
`Save` keeps `saveMu → mu`, taking `mu` for the snapshot copy alone and releasing it before any
I/O, exactly as documented on the `saveMu` field.

The load-bearing decision is that `Save` copies the map **inside** the existing `r.mu` section
rather than encoding the live map. Encoding the live map would let `json.Marshal` iterate it
while a concurrent `SetWorkspaceLabel` writes — the fatal throw described above, not a
recoverable race. The existing snapshot line copies only the slice, so the map copy is a second
thing to remember in that function; the field doc says so at the declaration.

Unlike `SessionHistory`, the map needs a genuine copy rather than a shallow share: a map header
copy aliases the same buckets, so `delete` or an insert-triggered rehash during the encode would
still be a concurrent-mutation throw. The slice's shallow copy is safe only because appends
write at indices at or past the snapshot's length; maps have no such disjointness.

## Error handling

Neither accessor can fail. The read is a map lookup; the write has no rejectable input at this
layer by design. `Load` and `Save` keep their existing wrapped-error contract unchanged — a
malformed `workspace_labels` value (say a JSON number where a string is expected) surfaces
through the same `registry: parse %s: %w` path as any other malformed field, with a nil
`*Registry`, no new branch.

No error message, log line, or sentinel can carry a label: nothing here returns an error, the
package's one logging site (`sweepOnce`) logs a count and a `Save` error, and `Save`'s errors
interpolate the file path only. `json` encode errors do not embed string values.

One documented precondition rather than a check: a label containing invalid UTF-8 would not
survive `Save` → `Load` byte-identically, because `encoding/json` substitutes U+FFFD on marshal
— the same hazard #2149 met and refused with a sentinel. Adding a sentinel here would contradict
AC's "no failure mode", and the wire path cannot produce the input: the package overview records
the measured fact that `encoding/json` substitutes U+FFFD while *decoding* into a Go string, so
whatever reaches #2207's handler is already valid UTF-8. The setter's doc comment states the
precondition and names the wire-side guarantee it rests on.

## Testing strategy

All in `internal/conversations/registry_test.go`, same-package, table-driven, `t.Parallel()`,
stdlib only.

- `TestRegistry_WorkspaceLabel_SetReadClear` (AC1) — table: read an unset key (absent); set then
  read (exact value); set twice (last wins, not appended); set then clear with `nil` (absent,
  not present-and-empty); clear an unset key (no-op, still absent); set an explicitly empty
  string (present, `""` — distinguishable from cleared). Byte-exact key rows: `/a`, `/a/`, `/A`
  and a trailing-space variant are four distinct keys. One row asserts the conversation list is
  byte-identical across a set, pinning "no other registry state changes".
- `TestRegistry_WorkspaceLabel_RoundTrip` (AC2) — two labels plus a conversation, `Save`, `Load`
  into a fresh registry, both labels read back identical and the conversations unchanged.
  Includes a permutation arm: the same labels inserted in the opposite order produce
  byte-identical files, pinning the sorted-map-key claim the byte-stability discipline rests on.
  A second arm asserts a non-ASCII and newline-carrying label survives verbatim.
- `TestRegistry_WorkspaceLabel_DoesNotPersist` (AC2) — `Save` → `SetWorkspaceLabel` → `Load`
  from the same path shows no label, mirroring `TestRegistry_SetSystemPrompt_DoesNotPersist`.
- `TestRegistry_Save_NoLabelsOmitsKey` (AC3) — modelled on `TestRegistry_Save_NoPromptOmitsKey`.
  Two arms, because the two states are reached differently: a registry that never set a label,
  and one that set and then cleared every label. Both must produce output containing no
  `workspace_labels` substring, and `Save → Load → Save` must be byte-identical. The second arm
  is the one the existing byte-stability tests structurally cannot cover — they construct a
  registry with no labels at all, so a key emitted unconditionally as `null` would round-trip as
  a fixed point while no longer matching a pre-ticket file.
- `TestRegistry_Load_AbsentLabelKeyDecodesEmpty` (AC3) — a hand-written pre-ticket fixture with
  no `workspace_labels` key loads with no error, and a read for the row's own `cwd` reports
  absent. Mirrors `TestRegistry_Load_AbsentPromptKeyDecodesNone`.
- `TestRegistry_WorkspaceLabel_ConcurrentAccess` (AC4) — goroutines mixing
  `SetWorkspaceLabel` (set and clear), `WorkspaceLabel`, `Create` and `Save` against one
  registry. Its job is the fatal-throw path, which `-race` alone does not report: it is the only
  test that exercises `Save`'s encode against a live concurrent map writer.

`go test -race ./internal/conversations/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Accessor names.** No sibling ticket names them; #2207/#2208/#2210 all say only "the
   registry's label read method". Chosen: `WorkspaceLabel` / `SetWorkspaceLabel` — Go's
   no-`Get`-prefix idiom for the reader, `Set…` matching `SetArchived`/`SetSystemPrompt`.
   Resolve by confirming nothing in the three consumers assumes another name.
2. **Does `omitempty` omit a non-nil empty map?** The design depends on it for AC3's
   set-then-cleared arm and for the unconditional `Save` copy. Confirm empirically in the
   AC3 test rather than trusting the reading of `isEmptyValue`; if it did not hold, the fallback
   is to nil the field when the last key is deleted and to skip the copy when nil.

Each is recorded as resolved in `## Revisions` if implementation changes the answer.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX — the two setters in this file will have opposite contracts,
  and nothing but a doc comment says so.** The *key* crosses its boundary elsewhere and
  arrives clean: `Conversation.Cwd` holds a `$HOME`-confined realpath by the time this layer
  sees it, confined by `change_workspace` (#823). The *value* has no boundary in this slice at
  all — `SetWorkspaceLabel` is an unvalidated door by explicit design, with non-blank and
  length bounds assigned to #2207's handler. That is safe only while that handler is the sole
  caller, and the hazard is the confused developer, not the attacker: `SetSystemPrompt` sits
  adjacent in the same file, validating length and UTF-8 behind a sentinel, and a future caller
  (a `pyry conv label` CLI, a second wire verb) could reasonably assume its neighbour does the
  same. Phase B must state "unvalidated by design; the caller owns non-blank and length bounds"
  at the symbol and name #2207 as the owner, rather than leaving the asymmetry to be inferred.
- **[Tokens, secrets, credentials] No findings — by design decision, not absence of thought.**
  The stored value is an opaque operator-chosen display string and the key is a path the daemon
  already holds on the conversation row; no credential material enters this layer and nothing is
  derived from one. Plaintext storage is correct for a display name, and the file's existing
  `0600` mode (set in `Save`) is unchanged by this slice.
- **[File operations] No findings — the map key is a filesystem path, which is this category's
  classic trap, and the design's answer is that it is never treated as one.** At this layer the
  `cwd` is only a map key compared as bytes and a JSON object key that `encoding/json` escapes:
  never resolved, joined, `Stat`-ed, opened, or passed to a process. So there is no traversal
  sink, no check-then-use gap, and no symlink decision to get wrong. `Save`'s temp-file →
  `Chmod 0600` → fsync → rename sequence and its `MkdirAll 0700` are untouched; this slice adds
  a field to the encoded value and changes no file operation. Residual, accepted: a hostile path
  string appears verbatim in `conversations.json`, which is already true of the conversation row
  it was copied from.
- **[Subprocess / external command execution] No findings — by design decision.** Nothing in
  this package executes anything. Worth stating rather than skipping because the adjacent
  `MaxSystemPromptBytes` doc explicitly disclaims being an argv constraint, inviting the
  assumption that some value in this file does reach argv. The label does not, in this slice or
  in the consuming ones: #2208 and #2210 place it on a payload, never on a command line.
- **[Cryptographic primitives] No findings — not applicable, and the absence of constant-time
  comparison is deliberate.** No randomness is generated and nothing attacker-controlled is
  compared against a secret. The byte-exact map lookup is correct precisely *because* neither key
  nor value is secret; `crypto/subtle` would be both impossible for a map lookup and wrong here.
- **[Network & I/O] OUT OF SCOPE, named for #2207 — this layer bounds neither the per-label
  length nor the key count, and both matter downstream.** (a) #2208 embeds a label per
  conversation in a `list_conversations` reply inside a v2 application envelope capped at 65519
  bytes. The package overview's #2149 bullet warns in terms: re-derive the escaped-worst-case
  arithmetic before reusing a bound in a list-shaped payload. So #2207's bound must be chosen
  against N labels in one reply, not against one label in isolation — flagging it here because
  #2207 is where the bound gets picked and this is the slice that hands it the unbounded field.
  (b) The map is unbounded in key count and nothing garbage-collects it. The ticket's assertion
  that nothing needs to is right for *correctness* — an orphaned key is invisible, since a
  workspace with no conversations has no row and no client shows it — but it is a monotonic
  growth surface: an authenticated paired device that repeatedly moves a conversation's cwd and
  relabels leaves one entry per distinct cwd forever, re-encoded on every `Save`. Not exploitable
  in this slice, which ships no caller; the refusal that bounds it (#2207's not-found check
  against the conversation list) lives with the only writer.
- **[Error messages, logs, telemetry] No findings — every path that could carry a label
  fragment is structurally closed.** The write returns nothing, so no error exists to embed one;
  the read returns only to its caller; `Save`'s wrapped errors interpolate the file path alone
  and `json` encode errors embed no string values; the package's one logging site, `sweepOnce`,
  logs a count and a `Save` error, never a record field. The adjacent hazard found while walking
  this category is a correctness bug rather than a leak — invalid UTF-8 breaks the round-trip
  guarantee via `encoding/json`'s U+FFFD substitution on marshal, the same trap #2149 refused
  with a sentinel — and is handled as a documented precondition in § Error handling, because a
  sentinel here would contradict the AC's "no failure mode" and the wire decoder cannot produce
  the input.
- **[Concurrency] SHOULD FIX (all three already in the design; recorded so the verifier checks
  they landed) — the failure mode here is worse than a data race.** (a) Encoding the live map
  while a writer mutates it is a fatal `concurrent map iteration and map write` runtime throw
  that kills the daemon, not a detector-reported race that corrupts output; the element-wise copy
  inside `Save`'s existing `r.mu` section is the fix, and the risk is that the existing snapshot
  line copies only the slice, so the map copy is easy to omit. (b) Returning the map from the
  reader hands a caller the same throw outside the lock, and a test not written for concurrency
  would leave `-race` green over it — the per-key `(string, bool)` signature is the structural
  fix. (c) A map-header copy is not enough: it aliases the same buckets, so a `delete` or an
  insert-triggered rehash during the encode still throws. The `SessionHistory` shallow-copy
  precedent does not transfer — it is safe only because appends write past the snapshot's
  length, and maps have no such disjointness. Lock order is unchanged (`saveMu → mu`), no new
  lock is introduced, and no goroutine is spawned.
- **[Threat model alignment] Addressed by assignment, not by silence.** The relevant threat is
  an authenticated paired device writing unbounded data into daemon-owned persistent state. This
  slice ships no wire surface and no caller, so the threat is unreachable here; it becomes live
  with #2207, whose handler owns the not-found, non-blank and length refusals that bound it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
