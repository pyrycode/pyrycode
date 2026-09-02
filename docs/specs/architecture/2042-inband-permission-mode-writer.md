# #2042 — write a caller-named permission mode in-band, keeping the bypass escalation off the surface

## Files read

- `internal/streamsup/envelope.go` → `controlRequest`, `controlRequestInner`,
  `marshalBypassRevocationEnvelope`, `WriteBypassRevocation`, `marshalInterruptEnvelope`,
  `WriteInterrupt`, `marshalInitializeEnvelope`, `WriteInitialize`, `ErrNoLiveChild` — the file
  this ticket widens. The three `marshal*`/`Write*` pairs establish the shape every new writer
  copies: the `marshal*` half is pure encoding, the `Write*` half holds the policy (today: the
  nil-writer refusal). `controlRequestInner`'s `omitempty` on `Mode` is load-bearing and must
  not be relaxed.
- `internal/streamsup/runner.go` → `RevokeBypass`, `Interrupt`, `RequestInitialize`,
  `nextControlID`, `controlSeq`, `Stdin` — the three sibling control-request methods, the shared
  locally-minted id counter, and the placement rule each one's doc states (a method goes ON
  `sessions.Runner` when its consumer sits inside `internal/sessions`).
- `internal/streamsup/watchdog.go` → the delivery-tracker doc naming `Runner.Interrupt` and
  `Runner.RevokeBypass` as control requests that do NOT arm it. Read to confirm this design
  leaves that claim true; it is why `watchdog.go` is not in the touched set.
- `internal/sessions/runner.go` → `Runner`, `RunnerFactory` — the seam AC4/AC5 widen, and the
  doc paragraph asserting there is no mode parameter anywhere on it.
- `internal/sessions/pool.go` → `deliverSettingsInBand`, `inBandDeliverable` — the one production
  consumer, its fire-and-forget contract, and the never-log paragraph AC3 protects.
- `cmd/pyry/streamsup_runner.go` → `streamRunner`, its `RevokeBypass`/`SetSpawnArgs` forwards and
  the `var _ sessions.Runner = streamRunner{}` assertion — the sole production implementer, and
  the compile-time proof that catches a missed widening.
- The five test doubles the widening drags in: `fakeRunner` and `lifecycleRunner`
  (`internal/sessions/runner_test.go`, the latter with its `revokes` counter and `revokeCount`
  reader), `raceRunner` (`internal/sessions/session_evict_race_test.go`), `stubRunner`
  (`cmd/pyry/session_router_test.go`), `baseRunner` (`cmd/pyry/inbound_deliver_rotation_test.go`).
- `internal/streamsup/envelope_test.go` → `TestMarshalBypassRevocationEnvelope` (AC1's byte-exact
  literal), `TestMarshalBypassRevocationEnvelope_RequestIDInjectionResistance` (whose comment
  AC5 corrects), `TestWriteBypassRevocation_NilRefusal` / `_WritesEnvelope` / `_WriteError`,
  `errWriter`.
- `internal/streamsup/interface_test.go` → `TestRunner_RevokeBypass_NoLiveChild` — the pattern
  AC4's runner-level test copies.
- `internal/sessions/pool_update_settings_restart_test.go` → the `revokeCount() == 1` assertion
  that pins today's wire behaviour; AC5's "unchanged" is measured there.
- `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go` → the file header and
  `revokeLogHandler`'s doc, which restate the never-log guarantee in a comment. Build-tagged, so
  `make check` never compiles it.
- `docs/knowledge/features/permission-mode-switch-inband-probe.md` — **#2041's landed finding**,
  merged into `main` today. It changes what this plan can assert (see Context).
- `docs/knowledge/features/streamsup-package-content-blocks-are-held-as-json-rawmessage.md`
  § "Bypass revocation send primitive (#1603)" — the package overview's account of the writer,
  including the "neither takes a mode" sentence the documentation phase will need to revise.
- `docs/knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md` — the
  interface-placement rule and the authoritative roster of implementers.
- `CODING-STYLE.md`, `docs/PROJECT-MEMORY.md`,
  `docs/knowledge/architecture/system-overview.md` — conventions and current state.

## Sizing

Re-counted against this written plan, all six size-S boundaries hold:

| Boundary | Limit | This plan |
|---|---|---|
| Production source files created or modified | ≤ 5 | **5** — `internal/streamsup/envelope.go`, `internal/streamsup/runner.go`, `internal/sessions/runner.go`, `internal/sessions/pool.go`, `cmd/pyry/streamsup_runner.go` |
| Total written work | ≤ 800 | **~700** (this plan ~290, production ~150, tests ~250) |
| New exported types or interfaces | ≤ 5 | **0** — one exported func, one method, one sentinel error var; no new type |
| Consumer call sites needing simultaneous update | ≤ 10 | **7** — five test doubles, the `streamRunner` adapter, one pool test assertion |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **4** — unsupported mode, no live child, write failure, defensive marshal failure |

The refiner estimated ~670 lines across 4–5 production files against #1603 (585 lines, 4 files)
as the analogue. The estimate holds. The one thing that moved is the production file count, and
the Design section below is where the choice that fixes it at 5 rather than 6 is argued.

This work is refactor-shaped (a seam method is added, so every implementer must change), so the
fan-out was counted concretely rather than estimated: `git grep -n -F 'RevokeBypass' -- '*.go'`
plus the roster in `sessions-package-key-types-runner-interface-runnerfactory.md` give six
implementers, of which one is production. Seven sites, against a boundary of ten.

## Context

The in-band `set_permission_mode` writer exists but emits one literal. `#1603` fixed
`Mode: "default"` inside `marshalBypassRevocationEnvelope` deliberately: the opposite direction
is a privilege escalation reachable over the daemon's own stdin, so it was kept structurally
absent rather than one argument away. `#1687` (carry any permission mode in
`set_session_settings`) and `#1686` (launch bypassed, downgrade immediately to the operator's
chosen mode, which is `auto`) both need a mode that is neither `default` nor bypass, and neither
can be built on a writer that can only say `default`.

**#2041's answer landed on `main` before this ticket started, and it changes one premise.** The
ticket body was written expecting a measurement still in flight and says the following slice may
have to narrow the allow-list to #2041's answer. `permission-mode-switch-inband-probe.md`
records the measurement, taken 2026-09-02 against claude 2.1.239: `acceptEdits`, `dontAsk`,
`plan` and `auto` **all** switch a running child in-band, each acked `success` with the next
turn's `init.permissionMode` echoing the new mode. So the five-mode set this slice allow-lists
is exactly the measured-accepted set plus `default` (#1595's own arm), and no narrowing is
pending. One caveat from that finding that this design must not mis-handle: `auto` is refused
**per model** — a model publishing no `supportsAutoMode` answers
`Cannot set permission mode to auto: auto mode unavailable for this model`. That is claude's
refusal on the far side of a well-formed line, not a reason to drop `auto` from the writer's
allow-list; the writer's job is to refuse what it knows claude will reject as a *vocabulary*
error, and to deliver what claude will answer for itself.

**Refuse by allow-list, not deny-list.** `#1603` left behind a structural property:
`git grep -F '"bypassPermissions"' -- 'internal/streamsup/*.go'` returns nothing today, and
`docs/knowledge/codebase/1603.md` records the enable direction as "structurally absent, not
merely undocumented". A deny-list entry naming the escalation would break that property *and*
fail open on any spelling the list failed to anticipate (`BypassPermissions`,
`bypassPermissions `, a Unicode homoglyph). A closed allow-list refuses the escalation by
non-membership, which keeps the literal out of production source and refuses every other
unanticipated string too — strictly stronger than what #1603 pinned.

No ADR is warranted. This ships no new decision: it applies #1603's stated safety property
("no escalation reachable from this surface") to a wider parameter, and the placement rule for
`sessions.Runner` is already recorded in
`sessions-package-key-types-runner-interface-runnerfactory.md`.

## Design

### 1. The allow-list and its refusal, in `internal/streamsup/envelope.go`

```go
const permissionModeDefault = "default"

// permissionModeAllowed is the CLOSED allow-list; membership is the whole gate.
func permissionModeAllowed(mode string) bool {
	switch mode {
	case permissionModeDefault, "acceptEdits", "plan", "auto", "dontAsk":
		return true
	}
	return false
}

var ErrUnsupportedPermissionMode = errors.New("streamsup: unsupported permission mode")
```

The set is a **`switch`, not a package-level slice or map**. A `var permissionModes = []string{…}`
reads more like a list, but it is mutable package state holding a security allow-list: anything
in `internal/streamsup` — a test included — can `append` the escalation onto it and dissolve the
carve-out globally, and a test doing so before its parallel siblings run would not reliably trip
`-race`. Control flow cannot be appended to. It also drops a `slices` import and lets the tests
carry their own expected set, which is what a test should do anyway: one that read the
production list would pass against a corrupted list.

**The gate is a vocabulary check, not an authorisation check**, and the distinction is stated
here and in `WritePermissionMode`'s own doc because the next slices read this plan. It answers
"is this a permission mode claude will parse?" and nothing else. It does **not** answer "may this
caller change this session's posture?" — #1687 routes an operator-named mode from a wire frame,
and the decision about who may name one belongs at that frame handler. Reading this allow-list as
"the writer refuses unsafe modes" would be a real error: `acceptEdits`, `auto` and `dontAsk` all
loosen a child launched in `default` behind the daemon's approval flags (`withApprovalArgs`), and
#2041 measured claude accepting each of them in-band on exactly such a child. `bypassPermissions`
is the one mode claude refuses for itself; the other four arrive because this daemon asked.

`ErrUnsupportedPermissionMode` is a bare sentinel and is returned **unwrapped**. Two properties
ride on that, both acceptance criteria:

- It is `errors.Is`-distinguishable from `ErrNoLiveChild` (AC3), so a caller can tell a
  permanent vocabulary refusal from a retryable "no live child".
- Its text is a constant, so it **cannot echo the rejected mode string** (AC3). Wrapping it with
  `fmt.Errorf("… %q", mode)` would be the obvious and wrong move:
  `Pool.deliverSettingsInBand` logs this error verbatim, and #833 keeps settings values out of
  the daemon log. The refusal reaching the log tells an operator *that* a mode was refused; the
  value is recoverable from the request the operator sent, not from pyry's log.

### 2. The writer

`marshalBypassRevocationEnvelope` becomes `marshalPermissionModeEnvelope(requestID, mode string)`
— the same body with `Mode: mode` in place of the literal. It stays a **pure encoder** with no
gate of its own, matching its three siblings in this file exactly; the gate lives in the
`Write*` half, where the nil-writer policy already lives. Its doc carries the rule and the
review trigger explicitly, because that placement is the design's one residual: an unexported
encoder that will mint a `bypassPermissions` line for anyone in the package who asks it to. It
has exactly one caller, `WritePermissionMode`, and a second caller is the moment the gate has to
move down into the encoder. Duplicating the check in both halves now would be two copies of the
same fabric rather than a second defence, so the single-caller rule is stated where a future
author reads it.

`WriteBypassRevocation` is **replaced** by:

```go
func WritePermissionMode(w io.Writer, requestID, mode string) error
```

with this refusal order, which is the design's one non-obvious choice:

1. `mode` not in `permissionModes` → return `ErrUnsupportedPermissionMode`, bare.
2. `w == nil` → return `ErrNoLiveChild`.
3. marshal (defensive error wrap), then one `Write`, never a close.

The allow-list check comes **before** the nil-writer check on purpose. Reversed, a caller that
asked for `bypassPermissions` while no child was bound would get back the *retryable*
`ErrNoLiveChild` and could reasonably retry forever; AC3's "distinguishable by the caller" would
hold only in the live-child case. A vocabulary refusal is permanent and must read as permanent
whatever the child is doing. Both orderings write zero bytes, so AC2 is indifferent — AC3 is
what decides it.

The gate has a second effect worth naming, because it is load-bearing and easy to lose in a
later refactor: **it bounds the line's length.** The longest member is `acceptEdits`, so the
emitted envelope stays around 110 bytes — far under `PIPE_BUF` (512 on macOS, 4096 on Linux),
which is what makes one `write(2)` atomic and keeps a control line from interleaving with a
concurrent `WriteTurn` line on the same fd. That single-writer-per-syscall property is the
interrupt writer's existing argument, and it is inherited here only because the mode is drawn
from a closed set: an unbounded caller-supplied mode could push the line past `PIPE_BUF` and
tear it against a concurrent turn. A future widening of the vocabulary inherits this constraint.

### 3. `(*streamsup.Runner).SetPermissionMode`, and why `RevokeBypass` survives

```go
func (r *Runner) SetPermissionMode(mode string) error {
	return WritePermissionMode(r.Stdin(), r.nextControlID(), mode)
}

func (r *Runner) RevokeBypass() error { return r.SetPermissionMode(permissionModeDefault) }
```

`RevokeBypass` is **kept, and re-expressed on top of the new method**, rather than subsumed.
The ticket left that call to this slice. Three reasons, in order of weight:

- **The consumer's migration belongs with the consumer.** `#2043` rewrites
  `Pool.deliverSettingsInBand` to send the operator's chosen mode. That is where the last
  `RevokeBypass` call disappears, and deleting a method in the slice that removes its final
  caller is one coherent move; deleting it here would mean rewriting the caller in a slice whose
  ACs say the caller's wire behaviour must be *unchanged*. This is the Strangler Fig shape:
  introduce the general form beside the special one, migrate the consumer where the consumer is
  owned, remove the special one there.
- **Subsuming costs a sixth production file and buys nothing this slice needs.** Renaming
  `RevokeBypass` out of existence drags `internal/sessions/pool.go`'s call site *and*
  `internal/streamsup/watchdog.go`, whose delivery-tracker doc names `Runner.RevokeBypass` as a
  control request that does not arm the tracker. Left unedited that becomes a citation to a
  symbol that no longer exists; edited, the touched production set is six files and the size-S
  boundary is broken for a comment.
- **The wire proof gets easier, not harder.** AC5 asks that today's revocation be unchanged on
  the wire. With `RevokeBypass` intact and delegating, that is provable by construction
  (`permissionModeDefault` is the only value it can pass) and by an existing test that keeps
  passing untouched.

There is no duplicated encoding path: `WritePermissionMode` is the only writer, and the mode
`RevokeBypass` names is the allow-list's own `default` entry.

One knock-on the plan states rather than hides: `RevokeBypass`'s write failures now wrap as
`streamsup: write permission mode: …` instead of `streamsup: write bypass revocation: …`. That
is an error string, not the wire, and one test assertion moves with it.

### 4. The `sessions.Runner` seam

`SetPermissionMode(mode string) error` is **added** to the interface; `RevokeBypass() error`
stays. Both, transitionally — the doc says so, names #2043 as where the pair collapses, and
states the placement rule that puts the new method on the interface rather than behind a type
assertion: its consumers (#1686, #1687) sit inside `internal/sessions`, where a structural
assertion fails *open* — the unmatched arm is a silent no-op that leaves the child in the wrong
posture while the update reports success. That is the same argument `SetSpawnArgs` and
`RevokeBypass` already carry, and it is the reason the widening is worth its fan-out: six
implementers, all in this repo, all compile-checked.

`cmd/pyry`'s `streamRunner` forwards it; `var _ sessions.Runner = streamRunner{}` is the
compile-time proof (AC5). The five test doubles gain the method.

### 5. `Pool.deliverSettingsInBand` — call site unchanged, guarantee restated

The call site keeps calling `RevokeBypass()`, so the delivered bytes are identical (AC5). Its
never-log paragraph currently justifies the guarantee as *"RevokeBypass takes no mode, so there
is no value it could leak"*. That sentence stays true of this call site but stops being the
whole story the moment the seam it dispatches through carries a mode-taking sibling, so it is
corrected to name both halves: the shorthand this site calls takes no mode, **and** the seam's
mode-carrying sibling refuses without echoing the rejected string. The second half is the clause
that still holds after #2043 rewrites this site — the first half is the one that expires.

### 6. Comment corrections (AC5)

| File | Claim | Disposition |
|---|---|---|
| `internal/streamsup/envelope.go` | "The emitted mode is fixed at default and is deliberately NOT a parameter" | Replaced: the mode is a parameter; the escalation is refused by non-membership in `permissionModes`. |
| `internal/streamsup/runner.go` | `RevokeBypass`: "the mode is fixed in the writer — there is no enable direction on this surface" | Rewritten: the mode is fixed *at this method*, and the enable direction is absent from the surface because the allow-list does not contain it. |
| `internal/sessions/runner.go` | "There is no mode parameter here or on `(*streamsup.Runner).RevokeBypass`" | Rewritten for the pair; the seam now has one method that takes a mode and one that does not. |
| `internal/sessions/pool.go` | "RevokeBypass takes no mode, so there is no value it could leak" | Corrected per § 5. |
| `internal/streamsup/envelope_test.go` | "naming it in a Go string literal anywhere under internal/streamsup would trip the ticket's grep" | Corrected: AC2 narrows the grep to non-test source, and a test now names the escalation directly. |
| `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go` | the same never-log sentence, in `revokeLogHandler`'s doc | Corrected to the two-clause form. Comment only, no code change. |

`internal/streamsup/watchdog.go` is deliberately **not** in this table: both methods its
delivery-tracker doc names still exist and still do not arm the tracker.

`controlRequestInner`'s `mode,omitempty` tag is untouched, and
`TestMarshalInterruptEnvelope` / `TestMarshalInitializeEnvelope` keep their byte-exact `want`
literals — they are the sole detector if that tag is ever dropped.

## Concurrency model

No goroutine is created and no lock is added. `SetPermissionMode` mirrors `Interrupt`,
`RevokeBypass` and `RequestInitialize` exactly: `Stdin()` takes and **releases** `r.mu` before
returning the handle, so the potentially-blocking write never holds the runner's leaf mutex, and
`nextControlID` is an `atomic.Uint64` increment. Safe from any goroutine.

The teardown race is unchanged and already handled: `Stdin()` can hand back a handle that
teardown then closes, in which case the write returns a wrapped error rather than panicking. The
allow-list check touches no shared state — `permissionModes` is written once at package
initialisation and only ever read.

`RevokeBypass` delegating to `SetPermissionMode` adds one stack frame and no synchronisation;
in particular it does **not** mint two ids, because the id is minted once inside
`SetPermissionMode`.

## Error handling

| Condition | Result | Bytes written |
|---|---|---|
| `mode` outside `permissionModes` (incl. `bypassPermissions`, `""`, wrong case, trailing space) | `ErrUnsupportedPermissionMode`, bare, no mode echoed | 0 |
| No live child (`Stdin()` nil: before first spawn, between spawns, mid-restart) | `ErrNoLiveChild` — retryable, unchanged | 0 |
| `json.Marshal` failure | wrapped `streamsup: marshal permission mode: %w` (defensive; unreachable for two strings) | 0 |
| `Write` failure (EPIPE on a pipe closed mid-teardown) | wrapped `streamsup: write permission mode: %w`, never mis-reported as `ErrNoLiveChild` | possibly partial, by the OS — the same contract the three siblings hold |

`Pool.deliverSettingsInBand` keeps its fire-and-forget contract: every one of these is logged at
`Info` with the setting *name* and swallowed. It cannot classify them — `internal/sessions` must
not import `internal/streamsup`, which would invert the `Runner` seam — and does not need to:
the whole reachable set warrants the same response.

## Testing strategy

RED first: every test below is written and observed failing before the production change.

**`internal/streamsup/envelope_test.go`**

- `TestMarshalBypassRevocationEnvelope` keeps its name and its `want` literal verbatim, now
  calling `marshalPermissionModeEnvelope("fixed-id", "default")`. This is AC1's byte-exactness,
  and keeping the name keeps the three in-repo comments that cite it true.
- `TestMarshalPermissionModeEnvelope_AllowedModes` — table over all five modes: byte-exact line
  per mode (subtype before mode), exactly one raw newline and it is the terminator, and a
  round-trip decode. The `default` row's `want` is the identical literal, so the two tests agree
  by construction.
- `TestWritePermissionMode_RefusesUnknownMode` — table whose rows **name `bypassPermissions`
  directly**, alongside `""`, `Default`, `default ` (trailing space), `acceptedits`, and a
  JSON-shaped string. Each asserts four things: `errors.Is(err, ErrUnsupportedPermissionMode)`;
  `!errors.Is(err, ErrNoLiveChild)`; the buffer is **empty**; and `err.Error()` does not contain
  the rejected mode. The `bypassPermissions` row is what stops the carve-out from dissolving
  silently — a future narrowing of the allow-list leaves it red-if-broken.
- `TestWritePermissionMode_RefusalOutranksNilWriter` — `WritePermissionMode(nil, "id",
  "bypassPermissions")` is the unsupported-mode error, not `ErrNoLiveChild`. This is the § 2
  ordering decision, and it is the assertion that fails if a later edit "tidies" the nil-check
  back to the top.
- `TestWritePermissionMode_NilRefusal` / `_WritesEnvelope` / `_WriteError` — the three existing
  `WriteBypassRevocation` tests, carried across with a mode argument. `_WriteError`'s expected
  substring moves to `write permission mode` per § 3, and it drives a **distinctive allowed
  mode** (`acceptEdits`, not `default`) so it can also assert the wrapped write error does not
  echo the mode. The refusal path is not the only one an operator's value could leak through,
  and `default` is too common a word to make that assertion mean anything.
- `TestMarshalBypassRevocationEnvelope_RequestIDInjectionResistance` keeps every hostile
  request_id row and its assertions; only the grep sentence in its doc changes. Its property is
  unchanged and now stronger to state: a hostile id still cannot rewrite `mode`, because the
  mode is a marshalled struct field and not text spliced into a string — and the value that
  field can hold is now bounded by the allow-list as well.

**`internal/streamsup/interface_test.go`**

- `TestRunner_SetPermissionMode_NoLiveChild` — with no child spawned, `SetPermissionMode("default")`
  returns `ErrNoLiveChild` without writing and without panicking (AC4).
- `TestRunner_SetPermissionMode_RefusesUnknownMode` — on that same childless runner,
  `SetPermissionMode("bypassPermissions")` returns `ErrUnsupportedPermissionMode` and not
  `ErrNoLiveChild`: the refusal reaches the caller through the runner, not just the free
  function.
- `TestRunner_RevokeBypass_NoLiveChild` is kept unchanged — the delegation must not have
  changed the no-live-child contract.

**`internal/sessions` + `cmd/pyry`**

- The five doubles gain `SetPermissionMode(mode string) error`. `lifecycleRunner`'s records the
  mode under its existing mutex (`modes []string`, read back by a deep-copying
  `permissionModes()` accessor, matching `restartArgs`/`spawnArgSets`/`userTurns`/`revokeCount`).
- `pool_update_settings_restart_test.go`'s YOLO-revoke case keeps `revokeCount() == 1` **and**
  gains `len(r.permissionModes()) == 0`. That pair is AC5's "unchanged on the wire" as an
  assertion rather than a claim: the pool still reaches the child through the revoke shorthand
  and has not silently started routing through the new method.
- `cmd/pyry`'s `var _ sessions.Runner = streamRunner{}` is the adapter's proof; a missed forward
  is a build failure, not a test failure.

**Verification (§ B2 scope):**

```
go test -race ./internal/streamsup/... ./internal/sessions/... ./cmd/pyry/...
go vet ./...
go vet -tags e2e_realclaude ./internal/e2e/realclaude/    # the comment edit; make check can't see it
go build ./cmd/pyry
git grep -F '"bypassPermissions"' -- 'internal/streamsup/*.go' ':!internal/streamsup/*_test.go'   # must be empty (AC2)
```

The whole-module race suite is the verifier's gate, not this run's.

## Open questions

1. **Does anything outside the writer need the allow-list's membership?** #2043 has to decide
   which modes an operator may name, and could reasonably want to validate before it reaches
   the runner. Resolved here as *no*: `permissionModes` stays unexported, because
   `internal/sessions` must not import `internal/streamsup` and the refusal already propagates
   as a typed error. Exporting it is #2043's call if its own vocabulary needs it, and adding an
   exported identifier there is cheaper than removing one.
2. **Should `SetPermissionMode` return the minted `request_id`?** No, for `RequestInitialize`'s
   stated reason: nothing correlates the ack yet, and a return value with no reader is a seam
   with nothing on the far side of it.
3. **Does `auto`'s per-model refusal (#2041) belong in the writer?** No — the writer cannot know
   which model the live child is running, and `permission-mode-switch-inband-probe.md` records
   the refusal as arriving in a `control_response` with `subtype: "error"`, which nothing on
   this path reads. Whoever surfaces that refusal to an operator needs an ack reader first;
   named here as out of scope for this slice.

## Security review

**Verdict:** PASS (three MUST FIX findings were addressed in this plan, before the plan commit).

**Findings:**

- **[Trust boundaries] MUST FIX — addressed above.** This slice *builds* the boundary a wire
  value will cross: nothing calls the writer with a non-`default` mode today, but #1687 routes
  an operator-named mode from a relay frame straight into `SetPermissionMode`. The first draft
  left the gate's meaning implicit, and the plausible misreading is dangerous in the direction
  that matters — a downstream author reads "the writer refuses unsafe modes" and skips the
  authorisation check at the frame handler. The gate answers *"is this a permission mode claude
  will parse?"* and nothing more. Three of its five members (`acceptEdits`, `auto`, `dontAsk`)
  genuinely loosen a child launched in `default` behind `withApprovalArgs`' approval flags, and
  #2041 measured claude accepting each of them in-band on exactly such a child — they arrive
  because this daemon asked, not because claude vetted them. The vocabulary-vs-authorisation
  contract is now stated in Design § 1 and goes into `WritePermissionMode`'s doc comment.
  Otherwise the boundary is single-sited and explicit, which is the property worth having: one
  predicate, one caller of the encoder, no second parse.
- **[Concurrency] MUST FIX — addressed above.** The allow-list was drafted as
  `var permissionModes = []string{…}`. A package-level slice is mutable state, and this one holds
  a security allow-list: anything in `internal/streamsup`, a test included, can `append` the
  escalation onto it and dissolve the carve-out for the whole package — and a test that did so
  before its parallel siblings ran would not reliably trip `-race`. It is now
  `permissionModeAllowed`, a `switch`: control flow cannot be appended to. This is also why the
  tests carry their own expected set rather than reading the production one. Beyond that, no
  finding: no goroutine is spawned, no lock is added, `Stdin()` releases `r.mu` before the
  potentially-blocking write, and `nextControlID` is an atomic increment. `RevokeBypass`
  delegating adds a stack frame and mints exactly one id, not two.
- **[Error messages, logs, telemetry] MUST FIX — addressed above.** `Pool.deliverSettingsInBand`
  logs this error verbatim, and #833 keeps settings values out of the daemon log; a permission
  mode is a settings value. The obvious and wrong implementation is
  `fmt.Errorf("unsupported permission mode %q", mode)`, which would leak the operator's value
  into the daemon log through the error return — AC3 exists for exactly this. The refusal is a
  bare constant sentinel, and the plan now also pins the *write*-error path, which the first
  draft left unasserted: `_WriteError` drives `acceptEdits` rather than `default` so it can
  assert no echo with a needle that means something. The standing decision a later author will
  be tempted to break: **do not add a "helpful" streamsup-side diagnostic naming the rejected
  mode.** That defeats AC3 through the back door, and nothing on this path logs today.
- **[Network & I/O] No findings, and one property to keep.** No socket, no server, no read. The
  one write is a single line onto a pipe the runner already holds open, and the allow-list bounds
  it: the longest member is `acceptEdits`, so the envelope stays near 110 bytes, far under
  `PIPE_BUF`. That is what keeps one `write(2)` atomic so a control line cannot interleave with
  a concurrent `WriteTurn` on the same fd — the interrupt writer's existing argument, inherited
  here *because* the mode is drawn from a closed set. An unbounded mode string would have
  reintroduced the tear. Recorded in Design § 2 so a future widening inherits the constraint.
- **[Subprocess execution] No findings, and a false analogy worth killing.** No `exec` in this
  diff. The mode does **not** become an argv element: it is a JSON string field that
  `json.Marshal` escapes onto an open stdin. #2041's headline hazard — a claude-published value
  round-tripped into `--model <value>`, where a leading `-` is parsed as a flag — has no analogue
  here, and a reader coming from that finding should not go looking for one. The path where a
  mode *would* reach an argv is the respawn (`withApprovalArgs` / `claudeSettingsArgs`), which
  this ticket does not touch.
- **[Tokens, secrets, credentials] No findings.** No credential is read, stored or compared.
  `request_id` is not a token: it is a per-runner `atomic.Uint64` correlating a reply on a pipe
  this process owns, deliberately not `crypto/rand` (a random id would rewrite the byte-exact
  fixtures every run, and there is no adversary to guess it — the id never leaves the daemon and
  nothing authenticates on it). Its injection resistance is already pinned and is kept:
  `json.Marshal` escaping means no id can open a second physical line or rewrite `mode`.
- **[Cryptographic primitives] Not applicable by design.** The ticket adds no primitive, no key
  and no comparison against a secret. The allow-list comparison is plain string equality on
  purpose: constant-time comparison defends a secret from a party that does not know it, and this
  vocabulary is a compile-time constant published in this plan and in claude's own docs.
- **[File operations] Not applicable by design.** No path is constructed, no file is created,
  opened, stat'd or renamed. The only handle involved is the child's stdin, already owned by the
  runner and obtained through `Stdin()`.
- **[Threat model alignment] OUT OF SCOPE, named.** This slice adds no wire vocabulary and no
  frame handler, so `docs/protocol-mobile.md` § Security model is untouched. Whether a client may
  name a permission mode at all is **#1687**'s; the bypass row's dependency on the launch argv is
  **#1686**'s; reaching this writer from a wire frame is **#2043**'s. #1595's recorded caveat —
  anything that can write a child's stdin can change its posture mid-session — is unchanged, the
  daemon being the party that already holds that stdin. What this slice must not do is re-open
  the bypass escalation, and three independent things keep it shut: non-membership in
  `permissionModeAllowed`; claude's own launch-argv refusal (#1595, and recorded there as a
  claude-version fact rather than a guarantee, which is why it is the backstop and not the
  defence); and no production caller passing a non-`default` mode after this slice. The first two
  are different fabric — a Go predicate and claude's own parser — so neither one failing takes
  the other with it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
