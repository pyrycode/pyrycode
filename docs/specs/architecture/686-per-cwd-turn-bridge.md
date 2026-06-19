# Spec #686 — Turn bridge resolves a conversation's transcript from its per-`Cwd` JSONL dir

**Ticket:** #686 (split from #681) · **Size:** S · **Label:** `security-sensitive`
**Blocked-by (both merged):** #685 (sessions spawn in per-conversation `Cwd`) · #679 (the bound-session reply resolver)

Make the outbound turn-bridge reply stream tail a conversation's transcript from
**that conversation's own per-`Cwd` JSONL directory** (`~/.claude/projects/<encoded-cwd>/`),
not the daemon's single shared sessions directory. Since #685 spawns each
conversation's session in its own `Cwd`, the transcript moved; the bound-session
resolver (#679) must follow it. A default (shared-workdir) conversation keeps
resolving from the startup-computed shared dir, unchanged.

---

## Files to read first

- `cmd/pyry/interactive_turn_stream_v2.go:222-324` — the two seams this ticket
  edits plus the resolver they feed: **`boundHostFunc` type (230)** = the lookup
  whose return arity grows by one (`dir`); **`resolveTarget` (251-271)** = uses
  the returned `dir` for the bound branch, keeps the `dir` param for the bootstrap
  branch; **`resolveBoundSessionJSONL` (292-324)** = the consumer — unchanged, it
  already takes `(dir, sessionID)` and tails `<sessionID>.jsonl` under `dir`.
- `cmd/pyry/main.go:618-641` — the `boundHost` closure (`convReg.Get` →
  `CurrentSessionID` guard → `pool.Lookup` → `sess.Supervisor()`). This is where
  the per-conversation `dir` is computed and added to the return. Note it is
  defined inside `runSupervisor`, so it can capture `claudeSessionsDir` and the
  bootstrap `trustedWorkdir` from scope.
- `cmd/pyry/main.go:524-555` — `claudeSessionsDir` (527, the startup shared dir,
  `= DefaultClaudeSessionsDir(filepath.Abs(*workdir))`) and `trustedWorkdir`
  (551, `= trustMark(confineWorkdirToHome(*workdir))` = the bootstrap realpath).
  These are the two values the discriminator keys on.
- `internal/sessions/pool.go:961-981` — `buildSession`: `spawnDir != "" → workDir
  = spawnDir`, else `workDir = tpl.WorkDir`; `workDir` becomes
  `supervisor.Config.WorkDir`. **Proof that `sess.Supervisor().WorkDir()` is
  byte-identical to where claude writes** — the realpath for a per-`Cwd` session,
  `tpl.WorkDir` (= `trustedWorkdir`) for a default one.
- `internal/supervisor/supervisor.go:85-90, 147-190` — `Config.WorkDir` field, the
  `Supervisor.cfg` (immutable post-`New`) holder, and the `State()` accessor whose
  shape the new `WorkDir()` accessor mirrors (but lock-free — `cfg` never mutates).
- `internal/sessions/reconcile.go:21-49` — `encodeWorkdir` (`/` and `.` → `-`) +
  **`DefaultClaudeSessionsDir(workdir)`** — the single source of truth for the
  `~/.claude/projects/<encoded-cwd>/` encoding. **Call it; do not hand-roll the
  replace rule.** Returns `""` if workdir is empty or `$HOME` is unresolvable.
- `internal/turnbridge/producer.go:38-63` — `Target` / `TargetResolver` contract
  (`Host`, `Resolve`, `Switch`); a resolver error is retried with backoff. The
  ticket changes only which `dir` `Resolve` closes over.
- `cmd/pyry/interactive_turn_stream_v2_test.go:368-473` — `resolveTarget` tests +
  `fakeSessionHost` double + the `writeJSONL` / `uuidA`/`uuidB` fixtures. The three
  `boundHost` fakes (387, 425, 467) gain a `dir` return; add a bound-branch
  assertion that the by-id resolve targets the boundHost-returned dir.
- `docs/knowledge/codebase/685.md` § "Patterns established" — the load-bearing
  invariant this design rests on: *"The row's `Cwd` drives nothing downstream —
  respawns read `supervisor.Config.WorkDir`, captured as the realpath at
  `CreateIn` time, not re-derived from the row."* This ticket extends the same
  rule to the bridge (read `WorkDir`, not the row).
- `docs/specs/architecture/685-conversation-spawn-in-cwd.md` § Design / Open
  questions — confirms the recorded `conv.Cwd` is the **raw** phone value (set) or
  `defaultCwd` (null), NOT the realpath claude spawns in.

---

## Context

After #685, `create_conversation` spawns a conversation's bound claude session in
that conversation's own validated, trust-marked `Cwd` (when set) instead of the
daemon's shared workdir. claude writes each session's transcript under
`~/.claude/projects/<encoded-spawn-workdir>/<session-id>.jsonl`, so a
per-`Cwd` conversation's transcript no longer lives in the daemon's shared
`claudeSessionsDir`.

The outbound reply stream (#679) maps the **active** conversation to a
`resolveBoundSessionJSONL(dir, sessionID)` closure that tails `<sessionID>.jsonl`
within `dir`. Today `dir` is always the single startup `claudeSessionsDir`. For a
per-`Cwd` conversation that's the wrong directory: `<sessionID>.jsonl` isn't
there, so `os.Stat` fails forever and the reply never streams. This slice makes
`dir` per-conversation.

The cross-conversation **confidentiality** property #679 established must hold
under per-`Cwd` directories: a conversation's reply must come from its own
directory and never from another `Cwd`'s directory (AC2). The bound resolver
already keys the *filename* off the bound session-id; this ticket keys the
*directory* off the bound session's spawn workdir, closing the remaining axis.

---

## Design

### Decision: derive `dir` from the session's spawn `WorkDir`, not from `conv.Cwd`

The directory claude writes to is `DefaultClaudeSessionsDir(W)` where `W` is the
cwd claude was launched with — i.e. `supervisor.Config.WorkDir`, the realpath
captured at `CreateIn` time. There are two ways to obtain `W`:

- **(A) Re-canonicalise `conv.Cwd`** at resolve time via `confineWorkdirToHome`
  (in-package, no accessor).
- **(B) Read the session's captured `supervisor.Config.WorkDir`** via a new
  accessor.

**This spec chooses (B).** Rationale:

1. **Single source of truth, byte-exact.** `sess.Supervisor().WorkDir()` is the
   *exact* string claude spawned with (`buildSession`, `pool.go:974-981`), so
   `DefaultClaudeSessionsDir(WorkDir())` is byte-identical to where claude writes
   — for both per-`Cwd` and default sessions. (A) re-derives it by re-running
   `Abs`+`EvalSymlinks` at resolve time, which can drift from the spawn-time
   realpath if a symlink changed in between (a TOCTOU re-resolution the design
   would have to reason about).
2. **Consistency with the #685 invariant.** #685 deliberately established that the
   recorded `conv.Cwd` "drives nothing downstream — respawns read
   `supervisor.Config.WorkDir` … not re-derived from the row"
   (`codebase/685.md`). (A) would make the raw row `Cwd` suddenly drive the
   bridge — reversing that invariant. (B) reads the same authoritative `WorkDir`
   the respawn path uses, keeping the row inert.
3. **No re-validation on the hot path.** (A) re-runs filesystem syscalls
   (`EvalSymlinks`) on every (re)subscription; (B) reads an immutable string.

The cost of (B) is a single additive accessor on `*supervisor.Supervisor`. That
is **not** a cross-package interface widening (no shared interface changes; the
`turnbridge.SessionHost` interface is untouched) and **not** a consumer cascade
(`codegraph_impact` on the new method is empty — one caller, `boundHost`). It
lands at **3 production files**, within the S budget and the ticket's ≤3-file /
no-cross-package-interface-widening constraint.

### Data flow

```
active conversation id ─► resolveTarget (interactive_turn_stream_v2.go)
                              │  convID == ""  ─► bootstrap host + resolveLatestSessionJSONL(claudeSessionsDir)   (AC4, unchanged)
                              │  convID != ""  ─► boundHost(convID)
                              ▼
boundHost (main.go, closure in runSupervisor):
   convReg.Get → CurrentSessionID guard → pool.Lookup → sess
   W   := sess.Supervisor().WorkDir()                         realpath claude spawned in
   dir := perConversationSessionsDir(W, trustedWorkdir, claudeSessionsDir)
              │  W == "" || W == trustedWorkdir  ─► claudeSessionsDir   (AC3: default/shared, unchanged)
              │  else                            ─► DefaultClaudeSessionsDir(W)   (AC1/AC2: per-Cwd)
   dir == ""  ─► ok=false (unresolvable → retry, never fall back; AC4)
   return host, CurrentSessionID, dir, true
                              ▼
resolveBoundSessionJSONL(dir, sessionID)   tails <sessionID>.jsonl under the per-conv dir   (unchanged)
```

### 1. `WorkDir()` accessor on `*supervisor.Supervisor`

Contract: returns `s.cfg.WorkDir` — the working directory the supervised claude
was spawned in (immutable after `New`, so no lock; mirror `State()`'s shape minus
the mutex). Empty when the supervisor was built with no `WorkDir` (inherits the
process cwd). One caller: `boundHost`.

```go
// WorkDir returns the working directory the supervised claude was spawned in
// (supervisor.Config.WorkDir, immutable post-New). The turn bridge derives a
// conversation's per-Cwd transcript directory from it (#686).
func (s *Supervisor) WorkDir() string
```

### 2. `perConversationSessionsDir` pure helper (cmd/pyry)

New unexported helper in `interactive_turn_stream_v2.go` (beside `boundHostFunc`
/ `resolveTarget`, so its test sits with the resolver tests). Encapsulates the
discriminate-and-encode logic so it is unit-testable without `runSupervisor`.

```go
// perConversationSessionsDir returns the directory claude writes a bound
// session's <id>.jsonl into, given that session's spawn workdir.
//   sessionWorkDir == "" or == bootstrapWorkDir → sharedDir   (AC3: default, unchanged)
//   otherwise                                    → sessions.DefaultClaudeSessionsDir(sessionWorkDir)  (AC1/AC2)
// Returns "" only when DefaultClaudeSessionsDir can't encode (no $HOME) for a
// per-Cwd workdir; the caller treats "" as unresolvable (retry, never fall back).
func perConversationSessionsDir(sessionWorkDir, bootstrapWorkDir, sharedDir string) string
```

- **Discriminator = `sessionWorkDir == bootstrapWorkDir`.** A default (null-`Cwd`)
  conversation's session spawns in `tpl.WorkDir` (= `trustedWorkdir` = the
  bootstrap realpath), so its `WorkDir()` equals `bootstrapWorkDir` exactly →
  routed to `sharedDir` (the startup `claudeSessionsDir`), satisfying AC3
  "unchanged" byte-for-byte. A per-`Cwd` conversation's `WorkDir()` is its own
  realpath → encoded via `DefaultClaudeSessionsDir`.
- **No re-canonicalisation, no `$HOME`/symlink syscalls beyond the one
  `DefaultClaudeSessionsDir` does for `os.UserHomeDir`.** Pure string logic over
  three inputs → a `t.Setenv`-free table test (see Testing).

### 3. `boundHostFunc` return arity + `boundHost` closure

`boundHostFunc` gains a `dir string` return (the bound resolver's directory):

```go
type boundHostFunc func(convID string) (host turnbridge.SessionHost, sessionID, dir string, ok bool)
```

`boundHost` (main.go) — after the existing `Get`/guard/`Lookup`, compute the dir
from the session's `WorkDir` and return it; an unresolvable dir is a miss
(`ok=false`), never a silent fallback:

- capture `claudeSessionsDir` + `trustedWorkdir` from `runSupervisor` scope (both
  already computed above `boundHost`);
- `dir := perConversationSessionsDir(sess.Supervisor().WorkDir(), trustedWorkdir, claudeSessionsDir)`;
- `if dir == "" { return nil, "", "", false }` (AC4 — no fallback);
- `return sess.Supervisor(), conv.CurrentSessionID, dir, true`.

The existing `(nil, "", false)` miss paths gain the extra `""` dir return:
`(nil, "", "", false)`.

### 4. `resolveTarget` uses the returned dir for the bound branch

`resolveTarget` keeps its `dir` parameter (= `claudeSessionsDir`) for the
**bootstrap** branch (`convID == ""` → `resolveLatestSessionJSONL(dir)`, AC4
unchanged). The **bound** branch destructures the new return and feeds it to the
resolver:

```go
host, sessionID, convDir, ok := boundHost(convID)
if !ok { return turnbridge.Target{}, fmt.Errorf("no bound session for active conversation %q", convID) }
return turnbridge.Target{Host: host, Resolve: resolveBoundSessionJSONL(convDir, sessionID), Switch: switchCh}, nil
```

`resolveBoundSessionJSONL`, `startInteractiveTurnStreamV2`, `startRelayV2`, and
`relay.go` are **unchanged**: `claudeSessionsDir` is still threaded as the
bootstrap `dir`; `boundHostFunc` is referenced by its (unchanged) type name so
the threading sites need no edit.

---

## Concurrency model

No new goroutines, locks, or channels. `WorkDir()` reads an immutable field
(`cfg` is set once in `supervisor.New`, never mutated) → lock-free, race-free,
unlike `State()` which guards mutable `state`. `perConversationSessionsDir` is
pure. `boundHost` is invoked from the producer's single `Run` goroutine
(`resolveTarget` → `boundHost`), the same single-goroutine path the existing
resolver closures rely on (`interactive_turn_stream_v2.go:149-153`); adding the
`WorkDir` read + dir computation there introduces no new sharing. The
`DefaultClaudeSessionsDir` call does a benign `os.UserHomeDir` read.

---

## Error handling

| Failure | Where | Behaviour |
|---|---|---|
| Conversation unknown / unbound / `Lookup` miss | `boundHost` | `ok=false` → `resolveTarget` returns error → subscriber retries (existing #679 path; no fallback). |
| `$HOME` unresolvable for a per-`Cwd` workdir (`DefaultClaudeSessionsDir` → `""`) | `perConversationSessionsDir` → `boundHost` | `dir==""` → `ok=false` → retry, **never** fall back to another dir (AC4). Practically unreachable — `$HOME` resolved at startup — but guarded. |
| Per-`Cwd` directory or `<id>.jsonl` absent on disk (deleted `Cwd`) | `resolveBoundSessionJSONL` `os.Stat` | wraps a path/errno error → retried with backoff; the by-id path can never resolve to another conversation's file (AC2/AC4). |

**AC4 mapping note (for code-review):** AC4 says "a `Cwd` that no longer resolves
yields a retryable resolve error, never a fall back." Under design (B) the dir is
derived from a captured string (always present), so "no longer resolves"
manifests as the *directory being absent on disk* → `resolveBoundSessionJSONL`'s
`os.Stat` failure → retryable error, no fallback. The intent (deleted directory →
retry, never cross-stream) holds; it is enforced at the `Stat` layer rather than
an `EvalSymlinks` layer. The dir-derivation itself cannot fall back: every
non-default branch goes through `DefaultClaudeSessionsDir(WorkDir())`, and a
non-derivable dir is a hard miss.

---

## Testing strategy

`go test -race ./...`. Unit coverage at the seams is sufficient and matches how
#679/#685 were verified; a live two-phone confidentiality e2e remains infeasible
(lesson [[architect-structured-stream-e2e-harness-gap]]) and is **not** required
here. Scenarios (developer writes them table-driven in the project idiom):

**`perConversationSessionsDir` — new table test (`interactive_turn_stream_v2_test.go`).**
Pure-function, no `t.Setenv` needed (pass `sharedDir`/`bootstrapWorkDir`
explicitly; for the per-`Cwd` case, assert against `sessions.DefaultClaudeSessionsDir(W)`):
- `sessionWorkDir == bootstrapWorkDir` → returns `sharedDir` (AC3 default).
- `sessionWorkDir == ""` → returns `sharedDir` (defensive default).
- per-`Cwd` `sessionWorkDir` (≠ bootstrap, within `$HOME`) → returns
  `DefaultClaudeSessionsDir(sessionWorkDir)`, and that value ≠ `sharedDir` (AC1).
- two distinct per-`Cwd` workdirs → two distinct dirs, neither equal to the other
  nor to `sharedDir` (AC2 isolation at the derivation layer).

**`resolveTarget` — extend the existing three tests + one assertion.**
The `boundHost` fakes (387, 425, 467) gain a `dir` return:
- `TestResolveTarget_BootstrapWhenNoRoute` — unchanged behaviour: bootstrap host
  + recency resolver over the `dir` param; boundHost still not consulted.
- `TestResolveTarget_BoundSessionWhenRouted` — fake returns a *distinct* per-conv
  `dir` (a second `t.TempDir()` holding the bound `<id>.jsonl`); assert the
  resolved path is under **that** dir, not the `dir` param — proving the bound
  branch follows the boundHost dir (AC1). Keep the existing "newer sibling file
  doesn't redirect" assertion (AC2 confidentiality at the resolveTarget seam).
- `TestResolveTarget_UnresolvableConversationErrors` — fake returns
  `(nil,"","",false)` → error, no fallback (unchanged intent, new arity).

**`WorkDir()` accessor (`internal/supervisor/supervisor_test.go`).**
A supervisor built with `Config{WorkDir: "/x/y"}` returns `"/x/y"`; empty config
returns `""`. (Trivial; may fold into an existing constructor test.)

---

## Open questions

1. **Latent abs-vs-realpath skew on the *default* path (out of scope).** The
   startup `claudeSessionsDir = DefaultClaudeSessionsDir(filepath.Abs(*workdir))`
   uses `Abs` (no symlink resolution), while the bootstrap session actually
   spawns in `trustedWorkdir = realpath(*workdir)`. On a symlinked `-pyry-workdir`
   these differ, so the *default/bootstrap* stream already looks in the wrong dir
   today. This ticket **deliberately preserves** that (AC3 "unchanged": default
   conversations route to the exact `claudeSessionsDir` variable) and **does not
   inherit** it on the per-`Cwd` path (which uses `DefaultClaudeSessionsDir(realpath)`
   end-to-end). A fix would canonicalise `resolveClaudeSessionsDir` to the
   realpath — a separate ticket touching the bootstrap path, explicitly out of
   scope here.
2. **Recorded `conv.Cwd` stays inert.** Consistent with #685, the row `Cwd` is
   neither read nor written by this slice. If a future consumer needs the row to
   equal claude's actual cwd, that's #685 Open-Question 1's deferred seam, not
   this ticket.

---

## Security review

**Verdict:** PASS

The crux is AC2 — cross-conversation confidentiality on the internet-exposed
phone reply surface. The structural guarantee survives this change: the bound
resolver tails `<bound-sessionID>.jsonl` (a server-minted, per-session UUID,
validated by `jsonlStemPattern`), so the **filename** is the confidentiality
boundary. This slice changes only the **directory** the filename is sought in. A
misderived directory therefore fails *closed* — `os.Stat` misses → retryable
error → no stream — and can never surface another conversation's content, because
no other conversation's transcript shares this session's UUID filename.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The phone-supplied `Cwd` is untrusted, but
  this slice does **not** re-cross the untrusted→trusted boundary: it reads
  `supervisor.Config.WorkDir` — the realpath already confined-to-`$HOME` +
  trust-marked by #685's `resolveSpawnDir` at spawn time — never the raw
  `conv.Cwd`. Choosing design (B) over (A) is itself the security-relevant call:
  (A) would re-introduce the raw row `Cwd` as a resolve-time path input;
  (B) consumes the already-validated value, keeping #685's `resolveSpawnDir` the
  single validation boundary. The active `convID` arrives pre-validated (exists +
  bound) from `sessionRouter.Route` (#678).
- **[File operations — path traversal]** No MUST FIX. The dir is
  `DefaultClaudeSessionsDir(WorkDir())`; `WorkDir()` is a `$HOME`-confined realpath
  (#685 rejects out-of-`$HOME` spawn dirs, so no bound session has an escaping
  WorkDir) and `encodeWorkdir` *removes* path separators (`/`,`.` → `-`) rather
  than introducing them. The filename stem keeps `resolveBoundSessionJSONL`'s
  existing `jsonlStemPattern` guard (`interactive_turn_stream_v2.go:303`) so a
  malformed id can't escape the dir. Neither component concatenates raw user input.
- **[File operations — TOCTOU/symlink]** SHOULD FIX (pre-existing, accepted).
  Design (B) deliberately avoids a resolve-time `EvalSymlinks` re-check, so it adds
  no new check-then-use gap on the derivation. The tail (`resolveBoundSessionJSONL`
  `os.Stat`→open) is read-only and unchanged from #679; an attacker who planted a
  symlink at `~/.claude/projects/<encoded>/<bound-uuid>.jsonl` would need to
  already control the operator's home **and** know the server-minted bound UUID —
  the same home-control assumption #685's confine→chdir window accepts. This ticket
  moves the directory per-conversation but does not change the open semantics or
  widen the window. No new mitigation in scope; named so code-review doesn't treat
  it as novel.
- **[File operations — permissions/atomic]** No findings — this slice creates no
  files (read-only transcript tail).
- **[Subprocess execution]** No findings — `WorkDir()` is read as a string for
  path encoding only; it is never passed to `exec`, a shell, or claude's argv.
  claude's cwd was fixed at #685 spawn time, not here.
- **[Tokens / Cryptographic primitives]** No findings — no tokens, secrets, or
  crypto in this slice. The bound sessionID is a non-secret server-minted UUID
  (`sessions.NewID`, already in the registry), used only as a filename stem.
- **[Network & I/O]** No findings — no change to relay framing, size caps,
  timeouts, or the offset discipline (warm→EOF / cold→0, #671); the only change is
  which local file the producer tails. The per-conversation directory *strengthens*
  on-disk isolation between conversations.
- **[Error messages / logs]** No MUST FIX — resolve errors wrap an internal
  `~/.claude/projects/<encoded-cwd>/…` path + errno (never file bytes), matching
  the #679 discipline, and surface to the producer as a retry — they are **not**
  emitted in any phone-facing envelope, so no path reaches the wire. The encoded
  path appears only in local daemon logs (operator-visible, acceptable, same class
  as #685's confine errors).
- **[Concurrency]** No MUST FIX — no new goroutines/locks/channels; `WorkDir()`
  reads an immutable post-`New` field (lock-free); `boundHost` + the dir derivation
  run on the producer's single `Run` goroutine, the existing resolver invariant.
- **[Threat model alignment]** The relevant `protocol-mobile.md` § Security-model
  threat — a paired phone (or compromised relay) reading another conversation's
  transcript — is addressed by the by-id filename boundary above; the no-fallback-
  under-non-empty-cursor guard (#679, re-asserted) prevents cross-stream on an
  unresolvable target. Out of scope (named): the symlink-plant-in-home attack
  (pre-existing, requires home control) and the abs-vs-realpath skew on the
  *default* path (Open Question 1 — a separate bootstrap-path ticket).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-19
