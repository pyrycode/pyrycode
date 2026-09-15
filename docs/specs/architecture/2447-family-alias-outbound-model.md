# #2447 — send the family alias to claude so sessions follow the latest model

## Files read

- `internal/sessions/session.go` → `claudeSettingsArgs`, `spawnArgs`, `SettingsUpdate` — the argv
  sink this ticket rewrites at, and the one place the settings suffix is composed.
- `internal/sessions/pool.go` → `Pool.UpdateSettings`, `deliverSettingsInBand`, `Pool.SettingsFor` —
  the in-band `set_model` sink, and the store-then-deliver ordering that decides what AC 4 can assert.
- `internal/relay/v2session_settings.go` → `validModel`, `modelAlnumByte`, `modelWordByte` — the
  upstream grammar whose two sink properties this ticket's transform must not weaken.
- `internal/contextwindow/usage.go` → `variantBase`, `variantWordByte` — the bracket-splitting shape
  to follow, including its visibility and its "mirror, never import" note.
- `internal/protocol/interactive.go` → `ModelOption` — carries `ResolvedModel` beside `Value`, which
  is what makes the live arm's comparand readable from the same run's menu.
- `internal/e2e/realclaude/interactive_stream_model_rejection_test.go` → the whole file — the live
  analogue: menu request, live-derived candidate, `set_session_settings`, one turn, `model_announced`.
- `internal/e2e/realclaude/interactive_stream_model_announced_test.go` → `drainForAnnouncedModel`,
  `announcedTargetFor` — the announced-frame reader, and the precedent for sourcing a comparand
  from a measured table rather than a literal (which the live arm here deliberately does *not* copy).
- `internal/sessions/pool_update_settings_inband_test.go` → `TestPool_UpdateSettings_InBand_ModelAndEffort`,
  `installedArgv`, `waitRunning` — the harness the new pool-level test reuses verbatim.
- `internal/sessions/runner_test.go` → `lifecycleRunner.SetModel`, `modelRequests` — the double that
  records what AC 3 asserts.
- `docs/knowledge/features/sessions-package-key-types-sessionsettings-claudesettingsargs.md` —
  records that `claudeSettingsArgs` is the single composition point for the settings suffix and that
  both construction sites plus the live-apply route through it. That is why one edit there covers
  every spawn rather than three.

## Context

A session's stored model is the row value the client picked from the daemon's published model list,
held verbatim. Most rows are bare aliases; Fable and the "Haiku 4.5" row are published as exact ids,
so a session that picked one keeps asking for a superseded model after claude updates. The operator's
decision is that a pyry session always follows the latest model of its family.

The stored value must not move: the model menu highlights by exact equality, `internal/relay`'s
`validModel` and `cmd/pyry`'s `validateModelVocabulary` both check the value as picked. So the
rewrite happens only where pyry hands a model string *to claude* — the spawn argv and the in-band
`set_model` request.

No ADR is warranted. This is one transform at two existing sinks, not a boundary change; the two
package-overview updates named under Documentation handoff carry the rule.

## Design

One new unexported file, `internal/sessions/modelfamily.go`, holding three unexported functions and
no state:

```go
func familyAlias(model string) string      // the transform; identity when the shape does not match
func splitVariantGroup(m string) (base, group string)
func modelLetters(s string) bool           // non-empty, all [A-Za-z]
func modelDigits(s string) bool            // non-empty, all [0-9]
```

`familyAlias` splits off a trailing bracket group with `variantBase`'s three conditions (the group's
`]` is the value's last byte, the interior is non-empty, every interior byte is in `[A-Za-z0-9._-]`),
refusing an empty base. It then splits the base on `-` and rewrites only when the parts are exactly:
`claude`, a family segment of letters, and at least one further segment with every remaining segment
all-digits. The result is `family + group`. Every other input is returned unchanged, byte for byte.

The grammar is mirrored from `variantBase`, not imported — `internal/contextwindow` compares two
claude-authored strings for equality; this one produces an argv token and a control-request value out
of a network-originated string, so sharing the rule would make relaxing either package's a silent
change to the other's threat. The docblock names #2447 and states that the daemon's standing rule —
model values are opaque and never parsed for matching, indexing or keying — is unbroken: this parses
in exactly one place and only to produce the outbound string.

Two routing changes, one line each:

- `claudeSettingsArgs` appends `familyAlias(s.Model)` after `--model` instead of `s.Model`.
- `deliverSettingsInBand` passes `familyAlias(*update.Model)` to `Runner.SetModel`.

Nothing else moves. `Pool.UpdateSettings` still writes `merged` (the picked value) into
`sess.settings` and through `saveLocked`; `Pool.SettingsFor` still returns it; `cmd/pyry`'s
`resolveBoundRunSettings` still reads `SettingsFor(...).Model` verbatim into the run-config snapshot.

### Why the output cannot weaken `validModel`'s two sink properties

`validModel`'s docblock rests the argv sink on "no accepted value can pose as a claude flag, because
the base's first byte is alphanumeric", and the live sink on a closed byte class. Both survive, and
neither depends on `validModel` having run first:

- **Every output byte is an input byte.** The rewritten value is `parts[1] + group`, where `parts[1]`
  is a substring of the base and `group` is a suffix of the input. No byte is synthesised, so no
  shell metacharacter, whitespace, control byte or byte ≥ 0x80 can appear that the input did not
  already carry — and the identity arm returns the input itself.
- **A rewritten value is strictly narrower than its input.** The emitted base is non-empty and all
  ASCII letters, so its first byte is alphanumeric and can never be `-`; the group, when present, is
  balanced, non-empty, unnested and drawn from the closed class, because those are the three
  conditions that had to hold for it to be split off at all. A rewritten output is therefore a strict
  subset of what `validModel` accepts, regardless of what the input was.
- **Length never grows**, so the upstream 64-byte bound still holds.

The reject arms are what carry this: a segment containing a control byte, a quote or a high byte is
not all-digits and not all-letters, so such a value takes the identity arm and is passed through
exactly as today.

## Concurrency model

None. `familyAlias` is a pure function over a string, spawns nothing, and is called from two sites
that already hold whatever they need: `claudeSettingsArgs` runs under `Pool.mu` at construction and
in `spawnArgs`; `deliverSettingsInBand` runs with `p.mu` released, as its contract requires. No lock
ordering changes and no goroutine is introduced.

## Error handling

`familyAlias` has no error return and no failure mode: every input that does not match the shape is
returned unchanged, which is the safe direction — today's behaviour. There is deliberately no
"unrecognised model" signal: an unparseable value is not an error, it is a value this rule has
nothing to say about, and the sinks already handle it. Delivery errors at `Runner.SetModel` are
unchanged — `deliverSettingsInBand` keeps its fire-and-forget log-and-swallow contract, and the
rewritten value is never logged, so #833's rule that settings values stay out of the daemon log is
untouched.

## Testing strategy

- `internal/sessions/modelfamily_test.go`
  - Table test over AC 1's full list: rewritten (`claude-fable-5-1[1m]`, `claude-fable-5[1m]`,
    `claude-opus-5`, `claude-haiku-4-5`, `claude-haiku-4-5-20251001`, `claude-sonnet-5`) and
    unchanged (`sonnet`, `opus[1m]`, `default`, `""`, `claude-3-5-sonnet-20241022`, `claude--5`,
    `claude-fable-5-1[1m`, `[1m]`, `claude-fable`).
  - A byte-closure test in the shape of `TestValidModel_ByteSetIsClosed`: walk all 256 byte values in
    each of three positions of a rewritable template and assert that every output either equals its
    input or matches `[A-Za-z]+(\[[A-Za-z0-9._-]+\])?`. This is the machine-checked form of the
    security argument above, not a restatement of the table.
- `internal/sessions/session_settings_test.go` — rows on the existing `TestClaudeSettingsArgs` table
  proving `--model` carries the alias for a full-id stored value and the value itself for a bare
  alias (AC 2).
- `internal/sessions/pool_update_settings_inband_test.go` — one test in the shape of
  `TestPool_UpdateSettings_InBand_ModelAndEffort`: update to `claude-fable-5-1[1m]`, then assert
  `modelRequests()` is `["fable[1m]"]` (AC 3), the installed argv carries `--model fable[1m]` (AC 2),
  and both `SettingsFor` and the on-disk entry read back `claude-fable-5-1[1m]` byte for byte (AC 4).
  One run proves all three because they are three readings of the same store-then-deliver call.
- `internal/e2e/realclaude/interactive_stream_family_alias_test.go`, behind `e2e_realclaude` (AC 5) —
  reuses `startStreamModalResolutionHarness`, requests the live menu, picks a published row that
  `familyAlias` would rewrite (preferring a `fable` family row, falling back to any rewritable row
  and logging which), records that row's `ResolvedModel` from the same reply, sends
  `set_session_settings` naming the row's `Value`, sends one turn, and asserts
  `drainForAnnouncedModel` reports exactly that `ResolvedModel`. No literal appears in the assertion,
  so a claude release cannot turn it into a false pass. A menu with no rewritable row fails loudly
  rather than skipping — the instrument would be void, and that is a fact to surface, not absorb.

`cmd/pyry`'s run-config snapshot gets no new test: `resolveBoundRunSettings` reads `SettingsFor`
verbatim and is not in this diff, so the pool-level round trip above is the whole of AC 4's claim.

## Open questions

1. **Does a real claude honour `fable[1m]` as `--model` and as a `set_model` value?** The design is
   void if not, and only the live arm can answer — a control response proves nothing here, since the
   `set_model_v2.1.259_refuse.json` capture shows claude answering `success` for a model that does
   not exist. Resolution: the live arm is the answer, and this builder run does not execute it (live
   tests belong to the dispatcher's `needs-real-claude` stage). The test is written to fail loudly
   and name the measurement if claude resolves the alias elsewhere.
2. **Does the current published menu still carry a full-id Fable row?** The captures read at
   `32dcaf96` say yes. The live arm derives the row from the run's own menu rather than assuming it,
   and fails with a named reason if no rewritable row is published at all.

## Documentation handoff

Pending, owned by the documentation stage — not written in this ticket:

- `docs/knowledge/features/sessions-package-key-types-sessionsettings-claudesettingsargs.md` — record
  that the `--model` value is the family alias, not the stored value, and that the two differ by
  design.
- `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md` — the same for the
  in-band `set_model` delivery, under the section covering that path.

Both must state the observable rule in the body text: pyry stores the row the user picked and sends
claude the family alias. The intended consequence — a session on the `claude-haiku-4-5` row is sent
`haiku` and follows whatever that alias resolves to, while the menu still highlights "Haiku 4.5" —
belongs in the first of the two so it is not later read as a bug.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and the reason is not the validator.** The transform sits between
  `internal/relay`'s `validModel` and the two sinks that validator protects, but `validModel` is not
  on every path into it: `settingsFromEntry` copies `registryEntry.Model` off `sessions.json` with no
  shape check at all, and the operator's own `--model` in the bootstrap template argv is never
  validated either. So "the input already passed `validModel`" would have been a false premise. The
  design instead makes the transform safe on its own terms — every output byte is an input byte, and
  a *rewritten* output is `[A-Za-z]+` plus at most one balanced closed-class bracket group, a strict
  subset of what `validModel` accepts. Both properties hold for an input that passed nothing.
- **[Trust boundaries] No findings — the two sinks cannot disagree.** `claudeSettingsArgs` rewrites
  `merged.Model` and `deliverSettingsInBand` rewrites `*update.Model`. On the in-band path
  `Pool.UpdateSettings` has already assigned the frame's value into `merged`, so the two are the same
  string; when `update.Model` is nil the delivery sends no model at all. The live child and the
  installed next-spawn argv therefore always name the same model — a divergence there would be
  precisely the silent wrong-model class this ticket exists to close.
- **[Subprocess] SHOULD FIX — pin "non-empty in, non-empty out" as an assertion, not as a reading of
  the code.** `claudeSettingsArgs` guards on `s.Model != ""` and then appends the transform's output
  as the element after `--model`. If `familyAlias` ever returned `""` for a non-empty input, the argv
  would carry an empty `--model` value. It cannot today — the rewrite arm returns a `modelLetters`
  segment, which is non-empty by definition, and the identity arm returns the caller's own non-empty
  string — but that is a property of two arms a later edit could add a third beside. Phase B adds a
  per-row assertion in the table test: a non-empty input yields a non-empty output.
- **[Subprocess] No findings on flag injection.** `--model` and its value are two separate elements
  of an argv slice handed to `exec.Command`; no `sh -c` is involved, and the environment is untouched
  by this ticket. A rewritten value's first byte is an ASCII letter, so it can never pose as a flag;
  an unrewritten value reaches `exec` exactly as it does today.
- **[Network & I/O] No findings — and no input bound is added here, deliberately.** An adversarially
  long value would make `strings.Split` allocate proportionally, but that same string already reaches
  `exec.Command` unchanged on today's code and is bounded by `validModel`'s 64 bytes on the only
  network-originated path. A length gate here would be a second place the limit is decided — the
  hazard `variantBase`'s own docblock names — for a failure mode nothing has observed. Declined, not
  overlooked.
- **[Error messages, logs] No findings, with one clarification worth recording.** No log call and no
  error return is added; `deliverSettingsInBand`'s `notDelivered` still records the field *name* and
  the seam's value-free error. The model does already appear in one log line — the `spawning claude`
  debug record logs the whole argv — and under this ticket that line carries the alias instead of the
  stored id, which is strictly less specific. `TestInteractiveStream…`'s absence assertion in
  `interactive_stream_model_announced_test.go` searches for the *announced* (resolved) identifier and
  is unaffected: an alias in the argv is what it already expects to find there.
- **[Concurrency] No findings.** `familyAlias` is pure, holds no state and takes no lock, so calling
  it from `spawnArgs` under `Pool.mu` (write) and from `deliverSettingsInBand` with `p.mu` released
  introduces no ordering and cannot deadlock. No goroutine is spawned.
- **[File operations] Not applicable, by a design decision worth naming.** The transform's output is
  never stored: it reaches an argv slice and a control-request value and nothing else. Only the
  picked value is persisted, by `saveLocked`, unchanged by this ticket — so no path, filename or key
  is ever built from either string.
- **[Tokens, secrets, credentials] Not applicable.** No token, secret or credential is read,
  produced or compared. The one comparison the function makes is against the constant `"claude"`, not
  against a secret, so constant-time comparison would buy nothing.
- **[Cryptographic primitives] Not applicable.** No randomness and no primitive is involved.
- **[Threat model] Addressed for the relevant threat, with one adjacent case named.** The
  `docs/protocol-mobile.md` § Security model threat this touches is a hostile phone steering an argv
  token through `set_session_settings`; the transform cannot widen what reaches argv, per the first
  finding. One consequence deserves stating rather than discovering later: `ModelOption`
  `SupportsAutoMode` is per model, so a session stored with permission mode `auto` whose alias
  resolves to a model that does not support it will have that mode refused by claude — which fails
  *closed*, toward the stricter posture, and is not a privilege gain. Whether a phone may change the
  model at all remains `set_session_settings`' existing authorisation question (#1687's territory),
  out of scope here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15
