# #1575 — A newly-minted session starts at the operator's configured model and effort level

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `internal/sessions/pool.go` | `DefaultSettings` | the source of the inherited values; note it takes `p.mu.RLock()` and resolves `p.bootstrap` fresh |
| `internal/sessions/pool.go` | `CreateIn` | mint site 1 — the `SessionSettings{}` argument and the comment above it; note it builds **before** `p.mu.Lock()` |
| `internal/sessions/pool.go` | `buildSession` | where `settings` reaches the argv, and the `#826b` sentence in its docstring |
| `internal/sessions/pool.go` | `saveLocked` | how `s.settings` is copied into `registryEntry` (the `omitempty` side effect, § Ripple) |
| `internal/sessions/get_or_create.go` | `materialise` | mint site 2 — the combined zero-value comment covering *both* mint and the #1487 fail-closed reason |
| `internal/sessions/get_or_create.go` | `GetOrCreateIn` | the caller that must inherit; it passes nothing today |
| `internal/sessions/revive.go` | `Revive` | the contract paragraph that says a revived session carries zero settings — this is what must stay true |
| `internal/sessions/session.go` | `SessionSettings`, `claudeSettingsArgs` | field set + the empty-string-emits-no-flag rule that makes AC 4 fall out for free |
| `internal/sessions/pool_settings_test.go` | `helperPoolArgvRecorder`, `waitArgv`, `spawnMintedWithSettings` | the argv-capture rig; `spawnMintedWithSettings` calls `buildSession` **directly** and must NOT be the basis of the new tests |
| `internal/sessions/pool_settings_test.go` | `TestPool_BootstrapWarmStart_AppliesSettingsToArgv` | the exact recipe for "pre-write a registry with settings, then `helperPoolArgvRecorder` on the same path" — the fixture every new test needs |
| `internal/sessions/pool_spawndir_test.go` | `TestPool_CreateIn_SpawnsInGivenDir`, `TestPool_GetOrCreateIn_SpawnsInGivenDir` | the public-entry-point drive shape |
| `internal/sessions/pool_revive_test.go` | `TestPool_Revive_ActivatesNormally` | how to get a revived session to actually spawn, so its argv can be observed |
| `internal/sessions/pool_default_settings_test.go` | `TestPool_DefaultSettings_NoBootstrap` | the `&Pool{}` shape AC 4's second case is really about (§ AC 4 second case) |
| `docs/knowledge/features/sessions-package.md` | § *Two spawn sites* and § `Pool.Revive` | the two evergreen claims to correct |

## Context

`Pool.buildSession` already turns a `SessionSettings` into `--model` / `--effort` /
`--dangerously-skip-permissions` argv tokens via `claudeSettingsArgs`, and the bootstrap
session already warm-starts from its persisted values. The gap is on the **mint** path:
both sites that build a client-minted session hand `buildSession` the zero value, so a new
conversation silently falls back to claude's own defaults instead of the operator's
configuration.

The two sites are `CreateIn` and `materialise`. `materialise` is the trap: it is shared by
`GetOrCreateIn` (mint — must inherit) and `Revive` (must not, per the #1487 fail-closed
contract). A one-line change there takes revive with it, and nothing currently catches that.

## Design

### The policy lives in one new unexported accessor

Add to `internal/sessions/pool.go`, immediately after `DefaultSettings`:

```go
// mintSettings returns the SessionSettings a freshly-minted session starts with.
func (p *Pool) mintSettings() SessionSettings
```

Behaviour: read the bootstrap's persisted settings via `DefaultSettings`, and return a
`SessionSettings` built **field by field** from `Model` and `Effort` only.

Three properties, each load-bearing:

1. **Field-by-field construction, not copy-then-clear.** The returned literal never
   mentions `YOLO`, so bypass cannot be inherited by construction rather than by a
   clearing statement someone could later delete. A field added to `SessionSettings`
   in future is likewise *not* inherited until someone opts it in — the fail-closed
   direction. This is what AC 2 is asking for.
2. **The existence bool is discarded.** `DefaultSettings` already returns the zero
   `SessionSettings` when there is no bootstrap, so an `if !ok { return SessionSettings{} }`
   arm would be a second return site no fixture can distinguish from the first — the shape
   that breaks totality claims for no benefit. Read the bool into `_` and let the zero value
   flow through. AC 4's second case is then true by construction, not by a branch.
3. **It must be called off `p.mu`.** `DefaultSettings` takes `p.mu.RLock()` and Go's
   `RWMutex` is not reentrant. Both call sites below already build the session before
   taking `p.mu` (deliberately — "so the critical section stays small"), so both are safe.
   State this in the docstring so a future caller inside a critical section is warned.

### The two mint sites

- **`CreateIn`** — replace the `SessionSettings{}` argument to `buildSession` with
  `p.mintSettings()`. The call is already above `p.mu.Lock()`. Rewrite the comment above it:
  it currently says "This ticket passes zero settings (byte-identical minted argv); #826b
  plumbs real per-session settings through this mint path", which is exactly the claim this
  ticket falsifies.

- **`GetOrCreateIn`** — pass `p.mintSettings()` into `materialise` (see next). The call
  happens before `materialise` takes `p.mu`, so lock discipline holds.

### `materialise` gains a `settings` parameter

```go
func (p *Pool) materialise(id SessionID, label, spawnDir string, settings SessionSettings) (*Session, bool, error)
```

It forwards `settings` verbatim to `buildSession` and makes no policy decision of its own.

This is the crux of the design. The alternative — reading `mintSettings()` *inside*
`materialise` — is the bug AC 3 exists to prevent: it would silently re-point revive at the
bootstrap's model and effort. Making the argument explicit forces each of the two callers to
state its choice at its own call site, where the contract that justifies the choice is
already documented:

| Caller | Argument | Why |
|---|---|---|
| `GetOrCreateIn` | `p.mintSettings()` | mint — inherits the operator's configuration |
| `Revive` | `SessionSettings{}` | #1487: a phone-granted bypass must not survive a restart, and a revived session's *own* settings were deliberately dropped |

`materialise` has exactly two callers (confirmed via `codegraph_impact`), so the signature
change costs two edits.

### Comment surgery — the existing zero-value comment splits in two

The comment above `materialise`'s `buildSession` call currently fuses two unrelated reasons
into one sentence: mint-path byte-identity **and** the #1487 revive fail-close. After this
change they belong at different places:

- The **mint** half is deleted — it is no longer true.
- The **#1487 fail-closed** half moves to `Revive`'s call site, next to the
  `SessionSettings{}` it now explains, preserving the reasoning in substance. `Revive`'s
  docstring paragraph ("A revived session carries zero `SessionSettings` …") stays as-is.
- `materialise`'s docstring gains a line saying the settings argument is the caller's
  decision, and names both choices.

### Ripple — a minted session now persists `model` / `effort`

`saveLocked` copies `s.settings` into `registryEntry`, so on a daemon whose bootstrap
carries a model or effort, a minted session's on-disk entry now carries those fields where
before `omitempty` dropped them. That is correct and wanted — `Pool.UpdateSettings`'s live
restart (#842) recomposes argv from the stored value, so a minted session that inherits at
spawn must store what it inherited or the first settings change would silently reset it.
`yolo` stays absent (`false` + `omitempty`).

Registry-bytes assertions in existing tests are unaffected because they all cold-start
(zero bootstrap settings ⇒ `mintSettings` returns zero ⇒ nothing new to persist). Confirm
with a run of the package rather than by inspection.

### Accepted: the snapshot can be one update stale

`mintSettings()` is read under `RLock`, released before the register critical section takes
the write lock. A concurrent `UpdateSettings` between the two can leave the new session
carrying the value from just before the change. This matches how `label` and `spawnDir`
already behave on this path, the window is sub-millisecond, and no failure has been
observed — no defence is specified.

Per the ticket's sibling note: #1572 must keep `sess.settings` updated when it changes a
running session's model/effort, or `DefaultSettings` reports a stale value here. That is
#1572's obligation; this spec assumes nothing about it.

## Concurrency model

No new goroutines, channels, or shutdown steps. One new lock acquisition
(`DefaultSettings`'s `RLock`) per mint, taken strictly outside `p.mu`'s write section at
both call sites. No lock ordering is introduced because only one lock is ever held.

## Error handling

No new failure modes. `mintSettings` has no error path — the absent-bootstrap case is a
value (the zero `SessionSettings`), not an error, which is `DefaultSettings`'s existing
contract. Every pre-existing error path in `CreateIn` and `materialise` is untouched.

## Testing strategy

Fixture recipe for all argv tests: pre-write a registry with a bootstrap entry carrying the
settings under test, then build the pool with `helperPoolArgvRecorder` **on the same
registry path** so `New` warm-starts from it, then `runPoolInBackground`. This is exactly
what `TestPool_BootstrapWarmStart_AppliesSettingsToArgv` does. Assert with `waitArgv(dir)`,
which strips the #943 `--settings <path>` pair.

Do **not** build on `spawnMintedWithSettings` — it calls `buildSession` directly and would
stay green even if neither entry point were changed.

Scenarios:

1. **`CreateIn` inherits (AC 1).** Bootstrap `{Model: "opus", Effort: "high"}`; drive
   `pool.CreateIn(ctx, "", spawnDir)`. Expect the spawnDir argv to be exactly
   `--session-id <id>`, `--model opus`, `--effort high`.
2. **`GetOrCreateIn` inherits on the register path (AC 1).** Same bootstrap; drive
   `pool.GetOrCreateIn(ctx, <NewID>, "", spawnDir)`. Same expected argv.
3. **Bypass is never inherited (AC 2).** Bootstrap `{Model: "opus", Effort: "high", YOLO: true}`;
   mint through `CreateIn`. Expect model and effort present and
   `--dangerously-skip-permissions` **absent**. Assert the bootstrap's own argv in the same
   test (or rely on the existing warm-start test) so the fixture is proven to carry bypass —
   otherwise the absence assertion is vacuous.
4. **`Revive` is unaffected (AC 3).** Bootstrap `{Model: "opus", Effort: "high"}`; `Revive`
   a fresh id into `spawnDir`, then `Activate` it (Revive does not spawn — see
   `TestPool_Revive_ActivatesNormally`). Expect the revived argv to be exactly
   `--session-id <id>` — no `--model`, no `--effort`. Assert in the same test that the
   *bootstrap's* argv does carry both, so the test cannot pass by the bootstrap fixture
   silently failing to take.
   **This is the test that must go red if `materialise` reads `mintSettings()` itself.**
   Verify that: run it against a tree where `materialise` inherits unconditionally, using
   `go test -overlay=<abs-path json>` so no worktree write is needed. A test that does not
   redden under that mutation does not satisfy AC 3.
5. **No configuration ⇒ byte-identical argv (AC 4, first case).** Bootstrap with empty
   model and empty effort; mint through `CreateIn`. Expect exactly `--session-id <id>` —
   no flag at all, not an empty-valued flag. The existing
   `TestPool_MintedSession_ZeroSettings_ArgvByteIdentical` does not cover this: it goes
   through `buildSession` directly.
6. **No bootstrap ⇒ no flags (AC 4, second case).** See below — assert
   `claudeSettingsArgs(p.mintSettings())` is nil for `p := &Pool{}`.

### AC 4's second case cannot be driven through the mint entry point

The AC asks for the no-bootstrap case to be observed on "the minted argv". It cannot be,
and the developer should not spend turns trying.

`Pool.New` constructs `p.sessions` as `map[SessionID]*Session{bootstrapID: sess}`
unconditionally, so **every** pool `New` returns has a bootstrap and `DefaultSettings`
returns `ok == true`. `Config.BootstrapEvicted` only changes the bootstrap's lifecycle
state, not its registration. Verified empirically at `5ebc48c` with a throwaway overlay
test: a `helperPoolEvicted` pool reports `DefaultSettings() = ({Model: Effort: YOLO:false}, true)`.
The only `ok == false` shape is the bare `&Pool{}` literal that
`TestPool_DefaultSettings_NoBootstrap` already uses — and that pool has a nil `newRunner`
and no `runGroup`, so it can never reach `buildSession`.

So `DefaultSettings`'s own docstring is wrong where it names "the embedded
evicted-bootstrap host" as a source of `false`. **That is out of scope for this ticket** —
do not fix it here; it is a claim about `DefaultSettings`, not about the mint path, so AC 5
does not reach it. Note it in `docs/knowledge/codebase/1575.md` at documentation time.

The honest, non-vacuous form of AC 4's second case is therefore a direct assertion on the
new accessor: for `p := &Pool{}`, `claudeSettingsArgs(p.mintSettings())` returns nil. That
*is* the argv contribution the AC is about — the remaining tokens are unrelated to this
change — measured at the composing helper rather than through a pool that cannot exist.
Mirror `TestPool_DefaultSettings_NoBootstrap`'s `&Pool{}` fixture.

### Claim corrections (AC 5)

Re-run both sweeps rather than trusting these lists.

```
git grep -F '#826b' -- '*.go'
```

Four hits at `5ebc48c`, all in scope:

- `pool.go` — the comment above `CreateIn`'s `buildSession` call (deleted; see above)
- `pool.go` — `buildSession`'s docstring sentence "CreateIn/GetOrCreateIn pass the zero
  value in this ticket; #826b plumbs real values through the mint path" (now false: both
  pass real values; only `Revive` passes zero)
- `get_or_create.go` — `materialise`'s fused comment (split; see above)
- `pool_settings_test.go` — `spawnMintedWithSettings`'s docstring "#826b will plumb settings
  through the public Create path; here we drive buildSession directly". The second half
  stays true and is now the *reason the helper must not be used for these ACs* — say so.

`#826b` was never filed and #826/#833 are closed. Replace each reference with the concrete
statement of what is true, not with `#1575` — a reference to a closed issue is what created
this confusion.

Evergreen prose, two sites in `docs/knowledge/features/sessions-package.md`:

- § *Two spawn sites* — "`CreateIn` / `GetOrCreateIn` … both still pass `SessionSettings{}`;
  plumbing real values through the *mint* path … remains unaddressed." Now false in both
  clauses.
- § `Pool.Revive` — "**Zero settings — fail-closed by construction.** `materialise` passes
  `SessionSettings{}`" is no longer accurate: `materialise` forwards its caller's argument,
  and `Revive` is the caller that passes zero. The same section's `materialise` signature and
  its "The **only** difference between the two callers is what happens after" sentence both
  need updating — the settings argument is now a second difference, and it is the one that
  keeps the fail-closed property.

`docs/knowledge/features/conversation-session-binding.md`'s "A revived session carries zero
`SessionSettings`" stays true and unchanged.

Leave `docs/specs/architecture/*.md`, `docs/knowledge/codebase/833.md`, and
`docs/knowledge/decisions/030-*.md` alone — those are dated build artifacts and ADRs
recording what was true when written, not live claims.

## Open questions

- Should an operator be able to mint a session at settings *other* than the bootstrap's
  (a per-mint override on the wire verb)? Out of scope: no verb carries the fields today,
  and the fail-closed reasoning for `YOLO` would need redoing for an untrusted source.
- `docs/knowledge/features/sessions-package.md` § *Two spawn sites* also carries the stale
  "no wire verb yet (setter: #826b, reader: #826c)" phrasing about a *wire* verb, which is a
  different claim from the mint path. Left alone deliberately; flag at documentation time
  if it has also gone stale.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] Raised as MUST FIX on the first pass; closed by a spec revision
  before this verdict.** The inherited
  values cross from the on-disk registry into a `exec.Command` argv. The registry is
  operator-trusted (local, `0600`) *for the bootstrap entry*, which is the only entry
  `mintSettings` reads — the same trust class `sessions-package.md` already adjudicates for
  the bootstrap warm-start spawn ("delivered to `exec.CommandContext` as discrete argv
  tokens — no shell, no injection surface"). The boundary is a single named function,
  `mintSettings`, and it is the only place mint-path inheritance is decided. **The risk this
  spec had to close is `YOLO`**: `SessionSettings` carries a phone-settable
  `--dangerously-skip-permissions` flag in the same struct as the two operator-configured
  fields, so any design that copies the struct inherits bypass too. The spec requires
  field-by-field construction (§ Design property 1) so bypass is excluded structurally, and
  AC 2's test pins it against a bypass-enabled bootstrap. A copy-then-clear design would
  have been a MUST FIX; it is specified out.
- **[Tokens, secrets, credentials]** Not applicable — no token, secret, or credential is
  created, read, stored, or compared. The nearest analogue is the permission bypass, handled
  under Trust boundaries above.
- **[File operations]** No findings. The only file this change reaches is `sessions.json`,
  and only through the existing `saveLocked` → atomic temp-plus-rename path; no new path is
  constructed, no caller-supplied string reaches a filesystem path. The § Ripple section
  documents the one content change (`model`/`effort` now persisted for minted entries) so it
  is a reviewed consequence, not a surprise.
- **[Subprocess / external command execution] SHOULD FIX — flagged for code-review, not
  gated.** The inherited strings become argv tokens. They are not shell-interpreted
  (`exec.CommandContext` with discrete tokens) and the source is the operator-trusted
  bootstrap entry, so no validation is specified — consistent with how the identical values
  already reach the bootstrap's own spawn. The residual concern is *scope creep of the
  source*: if a future ticket lets an untrusted party write the bootstrap's model/effort,
  this becomes an injection-adjacent path with no validator. Code-review should verify
  `mintSettings` reads only `DefaultSettings` and gains no second, wire-fed source.
- **[Cryptographic primitives]** Not applicable — no randomness, hashing, key material, or
  comparison of attacker-controlled values to secrets. `NewID`'s existing `crypto/rand` use
  is untouched.
- **[Network & I/O]** Not applicable — no socket, no reader, no size cap, no timeout, and
  no server surface is added or altered. Nothing on this path is reachable from the network
  except by triggering a mint, which is already an authenticated verb.
- **[Error messages, logs, telemetry]** No findings, and one thing deliberately not added:
  the spec specifies **no logging** on the new path. A log line naming the inherited model
  and effort would be benign, but `Revive`'s docstring establishes that this call site must
  not log its phone-influenced inputs (the #741 precedent), and adding the pipeline's first
  log statement here invites a later one that includes `spawnDir`. `mintSettings` has no
  error path, so there is no message to leak through either.
- **[Concurrency] No findings, one requirement made explicit.** Exactly one lock is ever
  held, so no ordering can be inverted. The hazard is self-deadlock, not a race:
  `DefaultSettings` takes `p.mu.RLock()` and Go's `RWMutex` is not reentrant, so calling
  `mintSettings` inside the register critical section hangs the pool — a trivially reachable
  DoS if a future caller gets it wrong. § Design property 3 requires the constraint in the
  docstring and both specified call sites sit above `p.mu.Lock()`. The read-then-write gap
  (§ Accepted) is a staleness window, not a TOCTOU on a security decision: `YOLO` is never
  read through this path at all, so no bypass state can be raced into a minted session.
- **[Threat model alignment]** The relevant threat is `protocol-mobile.md`'s: a compromised
  or malicious phone escalating its own privileges. This design cannot advance it — the only
  privilege-bearing field is structurally excluded from inheritance, and the #1487 revocation
  point (a daemon restart drops phone-granted bypass) is preserved by keeping `Revive` on the
  zero value. Out of scope and named: per-mint operator overrides from the wire (§ Open
  questions), which would introduce an untrusted source for these fields and needs its own
  review.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
