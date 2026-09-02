# #2043 — store a session's permission mode and deliver a change in-band

Ticket: <https://github.com/pyrycode/pyrycode/issues/2043> (parent #1687, `security-sensitive`,
`needs-real-claude`, `size:s`)

## Files read

- `internal/sessions/session.go` → `SessionSettings`, `SettingsUpdate`, `claudeSettingsArgs`,
  `Session.spawnArgs` — the stored triple, the presence contract, and the single place the YOLO
  fail-safe is spelled. The new field and the vocabulary land here.
- `internal/sessions/pool.go` → `Pool.UpdateSettings`, `inBandDeliverable`,
  `Pool.deliverSettingsInBand`, `Pool.New` (warm-start branch), `Pool.saveLocked`,
  `Pool.mintSettings`, `Pool.SettingsFor` — the merge/route/deliver chain this slice rewrites, plus
  the two sites that read and write the registry entry.
- `internal/sessions/registry.go` → `registryEntry`, `loadRegistry`, `parseLifecycleState` — the
  on-disk schema and the package's own precedent for a tolerant read of an unrecognised enum value.
- `internal/sessions/runner.go` → `Runner` (`RevokeBypass`, `SetPermissionMode`) — the seam whose
  revoke-only method collapses here; its doc names this ticket as where that happens.
- `internal/streamsup/envelope.go` → `permissionModeAllowed`, `WritePermissionMode`,
  `ErrUnsupportedPermissionMode` — the writer-side closed allow-list (five in-band modes), the
  vocabulary this package must mirror without importing, and the bare-sentinel refusal that makes
  the delivery-site log record safe.
- `internal/streamsup/runner.go` → `(*Runner).SetPermissionMode` — the concrete method behind the
  seam; untouched by this slice.
- `cmd/pyry/streamsup_runner.go` → `withApprovalArgs`, `newStreamRunnerFactory`, `streamRunner` —
  the construction-time approval-flag injection that would otherwise emit a second
  `--permission-mode`, and the adapter that loses its `RevokeBypass` forward.
- `cmd/pyry/mcp_config.go` → `permissionArgs` — the four-flag approval set, whose trailing
  `--permission-mode default` pair is the duplicate.
- `internal/sessions/runner_test.go` → `lifecycleRunner` (`revokes`, `modes`, `permissionModes`) —
  the double already records both methods; its comment names this ticket as the one that flips the
  expectation from `revokes` to `modes`.
- `internal/sessions/pool_update_settings_inband_test.go`,
  `pool_update_settings_restart_test.go`, `pool_update_settings_test.go` → `TestInBandDeliverable`,
  `TestPool_UpdateSettings_YOLORevoke_DropsBypassInBand`, `diskSettings` — the tests that pin
  today's routing and must be retargeted rather than duplicated.
- `cmd/pyry/streamsup_runner_test.go` → `TestWithApprovalArgs` — counts duplicate skip-permissions
  flags but not duplicate permission modes; the gap this slice closes.
- `docs/knowledge/features/sessions-package-key-types-sessionsettings-claudesettingsargs.md` —
  records that **only the bootstrap round-trips settings across a daemon restart** (`Pool.New`
  re-materialises the bootstrap entry only; `Pool.Revive` deliberately passes the zero value,
  #1487). That is what scopes AC2's restart claim to the bootstrap path plus the serialization
  layer, and it is why the mode's registry read is a testable helper rather than a line inside
  `Pool.New`.
- `docs/knowledge/features/streamsup-package-satisfying-sessions-runner.md` § `SetSpawnArgs` —
  construction-time shaping (`stripSessionIDFlags`, `withApprovalArgs`) is **not** reapplied on any
  post-construction install path, so the duplicate-mode question is a construction-path question
  only.

## Context

A session's stored settings are `Model`, `Effort` and `YOLO bool`. A boolean expresses two of
claude's six permission modes, so four have nowhere to live. #2041 measured `default`,
`acceptEdits`, `plan`, `auto` and `dontAsk` all switching on a running child in-band at claude
2.1.239; #2042 encoded exactly those five as `internal/streamsup`'s closed allow-list behind
`Runner.SetPermissionMode`, refusing `bypassPermissions` by non-membership. This slice makes the
mode a first-class stored setting and routes it through `Pool.UpdateSettings`' existing split: the
five in-band modes take `deliverSettingsInBand`, `bypassPermissions` keeps `sup.Restart(newArgs)`.

`yolo` is not removed — the mobile client speaks it, and #1687 settles what the two mean together
on the wire. Inside the daemon they must not be able to disagree, which the derivation rule below
enforces.

No ADR is warranted: this extends #833's stored-settings primitive and ADR 030/031's live-apply
split rather than reversing either. If the documentation phase disagrees, the argument to record is
"one posture, two stored fields, `yolo` authoritative for the escalation" (§ Design, invariant).

## Design

### The stored field and its vocabulary (`session.go`)

```go
type SessionSettings struct {
	Model          string
	Effort         string
	YOLO           bool
	PermissionMode string
}

type SettingsUpdate struct {
	Model          *string
	Effort         *string
	YOLO           *bool
	PermissionMode *string
}
```

Two unexported predicates, both **`switch` statements, never package-level slices or maps** — the
reasoning `permissionModeAllowed` records applies verbatim here: a `var` holding a security
vocabulary is mutable package state anything in the package, a test included, could append the
escalation onto. Control flow cannot be appended to.

- `permissionModeInBand(mode) bool` — `default`, `acceptEdits`, `plan`, `auto`, `dontAsk`. Mirrors
  #2042's allow-list, which `internal/sessions` may not import (that would invert the `Runner`
  seam). It is a **vocabulary** gate, not an authorisation one: three of its members genuinely
  loosen a child launched behind the daemon's approval flags, and who may set what is #1687's
  decision.
- `permissionModeKnown(mode) bool` — the five above plus `bypassPermissions`. This is the storable
  set; `UpdateSettings` rejects anything outside it. `""` is outside it: unlike `Model`/`Effort`,
  where empty means "omit the flag, run at claude's own default", the default posture is a
  *nameable* mode, so an explicit empty string has no reading.

Two exported sentinels, per PROJECT-MEMORY's "primitives return Go sentinels, consumers map to wire
codes":

- `ErrUnsupportedPermissionMode` — "sessions: unsupported permission mode". Bare; **never echoes
  the rejected value** (AC5).
- `ErrPermissionModeConflict` — "sessions: permission mode contradicts yolo". Bare, same reason.

### The invariant, and the canonical in-memory form

`s.YOLO == (s.PermissionMode == "bypassPermissions")` holds for every `Session.settings` a `Pool`
constructs or updates. `canonicalPermissionMode(mode string, yolo bool) string` is the one function
that establishes it, and it is total:

- `yolo` → `bypassPermissions` (the bit is authoritative for the escalation — one fail-safe, one
  place).
- otherwise an in-band member → itself.
- otherwise (`""`, an unknown string, or `bypassPermissions` paired with `yolo:false`) → `default`.

The last arm is a **normalisation of trusted input**, not a validation path: its callers are the
construction sites and the registry read, never operator input. Operator input is rejected in
`UpdateSettings`, loudly, before it reaches storage. Following `parseLifecycleState`'s precedent —
"empty input or any unrecognised value defaults to the conservative default" — a hand-edited
registry degrades to the safe posture rather than bricking the daemon, and the degradation can only
ever move *away* from bypass.

Applied at: `Pool.New` (both warm and cold branches), `Pool.buildSession` (so `mintSettings`' and
`Revive`'s zero values become `default` rather than `""`), and `settingsFromEntry`.

`Pool.mintSettings` keeps its **two-field literal** and does not gain a `PermissionMode` line. It
rebuilds field by field precisely so a posture is not inherited by a freshly-minted session, the
same structural fail-safe #1575/#1487 record for `YOLO`; `buildSession`'s canonicalisation then
gives the minted session `default`. Inheriting the mode while excluding `YOLO` would in fact still
be safe — `canonicalPermissionMode("bypassPermissions", false)` is `default` — but the fail-safe
should hold because the field is absent, not because a downgrade rescues it.

### Registry (`registry.go`)

`registryEntry` gains `PermissionMode string \`json:"permission_mode,omitempty"\``.

Write side — `permissionModeForDisk(s SessionSettings) string`: the mode when
`permissionModeInBand(mode) && mode != default`, and `""` for everything else — `default`,
`bypassPermissions`, and anything a bug could otherwise have left in memory. Write and read are
gated by the *same* predicate, so the disk vocabulary is closed to the four non-default in-band
modes by construction rather than by relying on the in-memory canonical form being intact. Two consequences, both wanted: a default session's on-disk shape stays
byte-stable (the `omitempty` property #833's entry doc claims), and the disk can never carry a mode
that contradicts `yolo`, because the escalation has exactly one on-disk spelling — the `yolo` key.

Read side — `settingsFromEntry(e registryEntry) SessionSettings`, the whole
entry→`SessionSettings` mapping (Model, Effort, YOLO, and `canonicalPermissionMode(e.PermissionMode,
e.YOLO)`). A pre-change entry carries no `permission_mode` key at all and decodes to `""`, which
canonicalises to the mode its `yolo` already implies: `bypassPermissions` when `yolo:true`,
`default` otherwise. Default-tolerant at the read, not defaulted at the write — an operator's
existing registry predates the key. `Pool.New`'s warm-start branch calls it instead of building the
literal inline, which is what makes the tolerance unit-testable without a pool.

### Spawn argv (`claudeSettingsArgs`)

Order stays model → effort → posture, and the posture slot is mutually exclusive:

- `YOLO` → `--dangerously-skip-permissions` alone. **Never** `--permission-mode bypassPermissions`
  — bypass keeps exactly one spelling so the fail-safe stays enforced in one place, as this
  function's doc claims (AC4).
- else a non-`default` in-band mode → `--permission-mode <mode>`.
- else (`default`, `""`) → nothing. `default` **is** claude's own default, so emitting no flag is
  the equivalent of an empty `Model`, and it is what keeps AC2's byte-for-byte clause true: every
  argv this function composes today is unchanged.

### Merge, derivation and rejection (`Pool.UpdateSettings`)

Under `p.mu`, before any mutation. Two rejections, each returning its bare sentinel with **nothing
persisted** — `UpdateSettings` already returns errors without partially persisting:

1. `update.PermissionMode != nil && !permissionModeKnown(*update.PermissionMode)` →
   `ErrUnsupportedPermissionMode`. This is what keeps an unrecognised value off the spawn argv and
   away from the child (AC1).
2. `update.PermissionMode != nil && update.YOLO != nil` and the two disagree
   (`(*mode == bypassPermissions) != *yolo`) → `ErrPermissionModeConflict`. Rejection rather than a
   precedence rule: letting the mode win downgrades an escalation silently, letting `yolo` win
   *grants* one from a frame that said `false`. Neither is fail-safe in both directions.

Then the merge, exactly the ticket's pinned table (do not invent a different one):

| update carries | stored mode becomes | stored `yolo` becomes |
|---|---|---|
| `yolo:true` | `bypassPermissions` | `true` |
| `yolo:false`, stored mode is `bypassPermissions` | `default` | `false` |
| `yolo:false`, stored mode is anything else | unchanged | `false` |
| mode `bypassPermissions` | `bypassPermissions` | `true` |
| any other known mode | that mode | `false` |
| a mode and a `yolo` that contradict | rejected — nothing persisted | rejected |

Row three is the one to get right: a `yolo:false` must not drag an operator out of `plan` into
`default` as a side effect of naming a field it was not asked about. When both fields are present
and consistent, the mode arm and the `yolo` arm produce the same result, so the mode arm runs first
and the `yolo` arm is skipped.

Everything downstream is unchanged: `merged == sess.settings` still short-circuits as a no-op (the
new field is a comparable string), `saveLocked` still persists before the live-apply, and the argv
is still recomposed under the lock and installed outside it.

### Routing (`inBandDeliverable`) — still keyed on the frame

The predicate keeps its rule: what the wire carried, never a per-field diff against stored state.
One clause is added, refusing by **non-membership** rather than by naming the escalation:

```go
if update.PermissionMode != nil && !permissionModeInBand(*update.PermissionMode) {
	return false
}
```

`bypassPermissions` falls out of that clause (AC4: it takes the existing restart, is not dropped),
and so does every unanticipated spelling — though an unknown mode is already rejected upstream, so
that arm is defence, not a reachable route. The nothing-present clause grows a `PermissionMode ==
nil` conjunct.

What the mode changes is not the routing rule but the *delivered value*: one posture is now
expressed by two fields, so an update naming **either** is an update to the posture, and the value
delivered is the posture that results.

### Delivery (`Pool.deliverSettingsInBand`) — exactly one posture send

Signature gains the merged settings: `deliverSettingsInBand(id, sup, update, merged
SessionSettings)`. `UpdateSettings` already has `merged` under the lock, so the delivery site reads
no pool state.

The `/model` and `/effort` clauses are unchanged. The bypass clause is **replaced**, not
supplemented:

```go
if update.PermissionMode != nil || update.YOLO != nil {
	if merged.PermissionMode == permissionModeBypass { // fail-safe, unreachable via inBandDeliverable
		return
	}
	if err := sup.SetPermissionMode(merged.PermissionMode); err != nil {
		notDelivered("permission_mode", err)
	}
}
```

One clause, one send — which is AC3's arithmetic. Keeping the old `!*update.YOLO → RevokeBypass()`
clause beside it would emit **two identical control requests** for one revocation, because the
derivation makes a `yolo:false` update also carry a non-bypass mode; dropping it without this
replacement would emit **zero**, silently ending the revocation `deliverSettingsInBand` performs
today. `RevokeBypass` is `SetPermissionMode("default")`, so the wire bytes of a revocation are
unchanged by the collapse — what changes is that one send makes them.

The bypass guard is retained as the enable-direction fail-safe for the reason its predecessor
records: this site stays independently correct rather than dependent on a caller-side invariant,
and `TestPool_DeliverSettingsInBand_EnableWritesNothing` already asserts it directly rather than
leaving it untested defence.

A `yolo:false` against a stored `plan` re-sends `plan` to a child already in it. That is the same
redundancy this path already tolerates for an unchanged model re-sent alongside a new effort, and
`inBandDeliverable`'s own doc calls it out for the revoke case in as many words.

### The seam collapse (`sessions.Runner`, `cmd/pyry`)

`RevokeBypass()` is removed from the `Runner` interface, from `streamRunner`'s forward, and from
the four test doubles (`fakeRunner`, `lifecycleRunner`, `raceRunner`, `baseRunner`, `stubRunner`).
`(*streamsup.Runner).RevokeBypass` and its own tests are **out of scope and stay** — the ticket
scopes `internal/streamsup` as untouched, and the concrete method keeps its package-level coverage.
`lifecycleRunner.revokes`/`revokeCount` go with the interface method; `modes`/`permissionModes` is
the record every posture assertion now reads.

### Construction-time duplicate (`cmd/pyry/withApprovalArgs`)

`withApprovalArgs` runs at runner *construction*, on top of the argv `Pool.buildSession`/`Pool.New`
composed from persisted settings — which is exactly the path a daemon restart takes to rebuild a
session out of the registry. A session storing `plan` would otherwise be spawned with
`--permission-mode plan … --permission-mode default`, and the last flag would win, silently
reverting AC2's headline promise.

The yolo early-return is unchanged. On the non-yolo path, when the incoming args already name a
permission mode, the injected set drops **only** its own `--permission-mode default` pair;
`--permission-prompt-tool`, `--mcp-config` and `--strict-mcp-config` land exactly as they do today.

**Do not implement this as a second early return.** `if namesPermissionMode(args) { return args }`
would spawn every mode-carrying session with no permission-prompt tool and no mcp-config — i.e.
with the daemon's approval gate entirely absent — which is a privilege escalation reachable from a
stored setting. The yolo early-return is safe only because a bypass child has no approval gate to
lose. A test asserts the three flags survive a mode-carrying spawn. Two tiny helpers in the same file: `namesPermissionMode(args)`
(both the two-token and the `--permission-mode=` joined forms, mirroring `stripSessionIDFlags`'
two-form handling — the operator's bootstrap pass-through args can carry either) and
`dropPermissionMode(args)`, which removes the pair by scanning rather than by slicing a known
position, so `permissionArgs`' ordering is not load-bearing.

## Concurrency model

No new goroutines, no new locks, no lock-order change. `Session.settings` stays `p.mu`-guarded
(writer `UpdateSettings` holds the write lock; readers `saveLocked`, `SettingsFor`,
`DefaultSettings` hold it too); the new field is a plain string inside that same value. Validation,
derivation and the `merged`/argv capture all happen inside the existing single `p.mu` section, so
there is no check-then-mutate window: a concurrent update either serialises before this one (and
its posture is what row three reads as "stored") or after. The live-apply still runs entirely
outside `p.mu`, and `SetPermissionMode` — like `RevokeBypass` before it — touches only
runner-internal state, so `Pool.mu → Session.lcMu` is untouched.

## Error handling

- Both rejections are pre-mutation and return bare sentinels; the in-memory settings, the registry
  file and the running child are all byte-identical to their prior state.
- A `saveLocked` failure still rolls `sess.settings` back and returns the error before any
  live-apply — unchanged.
- Delivery stays **fire-and-forget**: `SetPermissionMode`'s error (the retryable no-live-child, or
  the seam's permanent unsupported-mode refusal) is logged at Info under `"setting",
  "permission_mode"` and swallowed. The settings are already persisted and the argv already
  installed, so a failed write loses nothing and the caller still sees success; the dominant case
  is an evicted session or one between spawns. `internal/sessions` cannot classify the error anyway
  without importing `internal/streamsup`.
- No error on this path carries a mode value: ours are bare, and #2042's refusal does not echo the
  rejected string — which is why `deliverSettingsInBand` may keep logging the seam's error
  verbatim.

## Testing strategy

`internal/sessions`:

- **Derivation table** (`pool_settings_test.go` or a new `pool_permission_mode_test.go`) — one
  table over the six rows above, asserting both stored fields plus "nothing persisted" on the two
  reject rows; plus the unknown-mode and `""` rejections, each checked with `errors.Is`.
- **Registry round-trip and tolerance** (`registry_test.go`) — a `plan` entry survives
  save→load→`settingsFromEntry`; an entry with no `permission_mode` key and `yolo:false` yields
  `default`; the same with `yolo:true` yields `bypassPermissions`; a garbage value and an on-disk
  `bypassPermissions` with `yolo:false` both degrade to `default`; a `default` session writes **no**
  `permission_mode` key (byte-stable shape); a bypass session writes none either.
- **Argv composition** (`session_settings_test.go`) — `--permission-mode <mode>` for the four
  non-default in-band modes; bypass emits the skip flag and no `--permission-mode`; `default` and
  `""` emit nothing at all (the byte-for-byte clause); order stays model → effort → posture.
- **Restart-path rebuild** — a pool warm-started from a `plan` registry spawns under
  `--permission-mode plan` (read off the argv recorder).
- **Routing** — `TestInBandDeliverable` gains rows: each in-band mode `true`, `bypassPermissions`
  `false`, mode + contradictory-but-unreachable shapes, mode alongside a cleared model `false`.
- **Exactly one send** — `TestPool_UpdateSettings_YOLORevoke_DropsBypassInBand` retargeted:
  `permissionModes() == ["default"]` (exactly one, not "at least one"), no restart, bypass-free
  installed argv. This is AC3's neither-two-nor-zero red: the two-send mutant (keeping both
  clauses) and the zero-send mutant (dropping both) each fail it.
- **Mode goes in-band and into the argv** — an `acceptEdits` update delivers `acceptEdits` through
  `SetPermissionMode`, does not restart, and installs an argv carrying `--permission-mode
  acceptEdits`.
- **Row three** — a `yolo:false` against a stored `plan` keeps `plan` stored and sends `plan`, not
  `default`.
- **Bypass takes the restart** — a `bypassPermissions` update restarts with an argv carrying the
  skip flag and no `--permission-mode`, and delivers nothing in-band.
- **No-log** — a `slog` handler writing to a buffer across an accepted mode update *and* both
  rejections; the buffer must not contain any mode literal. Mirrors #833's rule for model/effort.

`cmd/pyry`:

- `TestWithApprovalArgs` gains a case: args already naming `--permission-mode plan` yield exactly
  one `--permission-mode` in the output, still carrying the other three approval flags; the joined
  `--permission-mode=plan` form counts as naming one; a yolo spawn is still returned unchanged.

Gate: `go test -race ./internal/sessions/... ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.
The full-module race suite and the `needs-real-claude` live gate are the verifier's.

## Open questions

1. **Does anything outside the writer need the allow-list's membership exported?** (#2042 left this
   as this slice's call.) Resolved in the design: no. This package needs its own vocabulary because
   it may not import `internal/streamsup`, but the constants stay **unexported** — this slice's only
   caller is in-package, and #1687's wire decode passes a string straight into `SettingsUpdate`,
   where `UpdateSettings` rejects what it does not know. Exporting a mode list now would publish a
   second vocabulary to keep in step with the writer's for no consumer.
2. **Should an unrecognised on-disk `permission_mode` be a hard `loadRegistry` error?** Resolved:
   no — degrade to `default`, following `parseLifecycleState`. The degradation is fail-safe in the
   only direction that matters, and a hard error would brick a daemon over a hand-edit that has a
   safe reading. The malformed-`yolo` fail-closed posture is untouched: that is a JSON *type* error
   and still fails the whole parse.
3. **Do the `internal/e2e/realclaude` comments naming `RevokeBypass` get updated?** Yes — the live
   gate (`interactive_stream_inband_bypass_revoke_test.go`, `inband_bypass_revoke_arms_test.go`)
   sits behind a build tag `make check` never compiles, and its comments assert `RevokeBypass` is
   "the method this path still calls", which this slice makes false. Comment-only edits, no
   behaviour change; the suite still drives `Pool.UpdateSettings` and still measures the same wire
   bytes. Any divergence found while editing goes in a `## Revisions` entry.

## Size check (§ A4, re-counted against this written plan)

| Boundary | Limit | This plan |
|---|---|---|
| Production source files created or modified | ≤ 5 | 5 — `internal/sessions/{session,pool,registry,runner}.go`, `cmd/pyry/streamsup_runner.go` |
| Total written work | ≤ 800 | ~780 est. (≈190 production, ≈400 test, ≈190 spec) |
| New exported types or interfaces | ≤ 5 | 0 types (2 error sentinels, 2 struct fields) |
| Consumer call sites needing simultaneous update | ≤ 10 | 6 — 1 adapter + 5 test doubles for the `RevokeBypass` removal |
| Acceptance criteria | ≤ 5 | 5 |
| Distinct error/reject branches | ≤ 10 | 2 |

Within boundary. The refiner's `Estimate:` line said ~700 lines of code and tests over 5 production
files against #2042 (measured 602 lines of code and tests, 13 files, 5 production, same seam, same
package pair, one slice earlier); this sketch lands ~590 code-and-test, and the spec is the lever
if it runs long.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings.** There are exactly three writers of `Session.settings` and
  every one of them normalises or rejects: `Pool.UpdateSettings` (rejects an unknown mode with
  `ErrUnsupportedPermissionMode` and a contradictory pair with `ErrPermissionModeConflict`, both
  before any mutation), the two construction sites (`Pool.New`, `Pool.buildSession`, via
  `canonicalPermissionMode`), and the registry read (`settingsFromEntry`, same function). No
  fourth path exists — `mintSettings` and `Revive` reach storage through `buildSession`. The
  boundary is a named total function, not a check scattered across call sites, and the invariant it
  establishes (`YOLO == (mode == bypassPermissions)`) is stated on `SessionSettings` so a
  downstream reader knows what it holds.
- **[Subprocess execution] No findings, and this is the category that matters.** The mode becomes
  an `exec` argv element (`--permission-mode <mode>`), so the allow-list is what stands between a
  wire string and a flag. It is checked at update time, *before storage*, so the registry cannot
  come to hold a value that later becomes an argv token: at most six literals are storable and only
  four ever emit the flag. No `sh -c` anywhere on the path. An argument-injection attempt
  (`--dangerously-skip-permissions` as a "mode") is refused by non-membership, not by escaping.
- **[Subprocess execution] MUST FIX — addressed in the plan before this pass concluded.** The
  duplicate-flag fix in `withApprovalArgs` is one keystroke away from a privilege escalation:
  implemented as a second early return (`if namesPermissionMode(args) { return args }`) it would
  strip `--permission-prompt-tool`, `--mcp-config` and `--strict-mcp-config` from every
  mode-carrying spawn, leaving a `plan` session running with no daemon approval gate at all. The
  yolo early-return is safe only because a bypass child has no gate to lose; a mode-carrying child
  does. § Construction-time duplicate now names the shortcut and forbids it, and the test asserts
  the three flags survive.
- **[Threat model alignment] OUT OF SCOPE — #1687.** The allow-list is a **vocabulary** gate, not
  an authorisation one. Three of its members (`acceptEdits`, `auto`, `dontAsk`) genuinely loosen a
  child launched behind the daemon's approval flags — `dontAsk` in particular means the
  permission-prompt tool never fires, so the approval registry is bypassed in effect even though
  the flag is present. Nothing in this slice decides *who may ask for that*: its only caller is
  in-package and its own unit tests. Whoever accepts a mode from a phone frame owns the decision,
  and that is #1687. Reading `permissionModeKnown` as "refuses unsafe modes" is the wrong takeaway
  to carry anywhere.
- **[Threat model alignment] No findings on restart survival.** #1487's rule — a phone-granted
  bypass must not survive a daemon restart through `Pool.Revive` — is preserved: `Revive` passes
  the zero value, which canonicalises to `default`, never to the stored posture. The bootstrap's
  posture does round-trip, which is this ticket's headline promise and is exactly how `YOLO`
  already behaves; no new exposure.
- **[File operations] No findings.** The registry write is unchanged: `saveRegistryLocked`'s
  temp-file + `chmod 0600` + fsync + rename, so a SIGKILL mid-write leaves the pre- or post-update
  document, never a partial one. If the mode key fails to land, the entry reads as pre-change and
  canonicalises from `yolo` — a degradation that can only move *away* from bypass. The mode names
  no path and reaches no `filepath.Join`. `permissionModeForDisk` and `settingsFromEntry` are
  gated by the same predicate, so the on-disk vocabulary is closed independently of the in-memory
  form being intact.
- **[Errors, logs, telemetry] No findings on the paths this slice owns.** Both new sentinels are
  bare `errors.New` values that never echo the rejected mode, so a caller may log them verbatim;
  `deliverSettingsInBand` logs the constant field name `"permission_mode"` and the seam's error,
  which #2042 also keeps value-free; `UpdateSettings` adds no logging and wraps neither rejection.
- **[Errors, logs, telemetry] OUT OF SCOPE — pre-existing, and it constrains the test.**
  `(*streamsup.Runner)`'s spawn record (`"spawning claude", "args", args`) logs the full argv at
  Info, so it already exposes every `--model` and `--effort` value today and would expose
  `--permission-mode plan` identically. AC5 scopes its rule to "a delivery record" and "a rejection
  error that some caller logs", "matching the rule #833 set for model and effort values on this
  path" — and on the spawn path model and effort are logged, so the mode inherits the same
  treatment rather than a weaker one. Narrowing that record is a #833-scoped change to production
  code outside this ticket (§ Scope Discipline), so it is not fixed here. Consequence for Phase B:
  the no-log test must capture the **pool's** logger across `UpdateSettings` and the two
  rejections, never a logger a spawning runner also writes to — otherwise it fails for a
  pre-existing reason and invites exactly the out-of-scope fix.
- **[Network & I/O] No findings.** `permissionModeInBand` mirrors #2042's five members, so the
  emitted control line stays near 110 bytes — far under `PIPE_BUF`, which is what keeps the write
  atomic against a concurrent `WriteTurn` on the same fd. An unbounded caller-supplied mode can
  never reach the pipe: it is refused at update time, again at the delivery guard, and a third time
  by the writer's own allow-list — three stops, the last of them in a different package and by a
  different mechanism (non-membership rather than a rule about direction).
- **[Concurrency] No findings.** Validation, derivation, persist and the argv/runner capture all
  happen inside the one existing `p.mu` write section, so there is no check-then-mutate window and
  the invariant is re-established atomically per update; a concurrent reader sees a consistent
  before or after. No new goroutine, no new lock, no change to `Pool.mu → Session.lcMu`. The
  live-apply still runs off-lock and touches only runner-internal state.
- **[Tokens, secrets, credentials] Not applicable.** No credential, token or key is created, read
  or compared on this path; the control-request `request_id` is still minted by
  `Runner.nextControlID` in `internal/streamsup`, untouched.
- **[Cryptographic primitives] Not applicable.** No randomness and no comparison against a secret
  is introduced; the mode is compared against a literal vocabulary, where timing carries nothing an
  attacker does not already know.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
