# Spec #685 — Spawn a conversation's session in its canonicalised, trust-marked Cwd

**Ticket:** #685 (split from #681) · **Size:** S · **Label:** `security-sensitive`

Make a conversation's bound claude session spawn in that conversation's own
recorded `Cwd` — validated (canonicalised + confined to `$HOME`) and pre-marked
workspace-trusted at the cmd layer — instead of always spawning in the daemon's
shared trusted workdir. A `Cwd` that escapes `$HOME` is rejected as a
`create_conversation` error with no half-bound row.

This reverses the deferral documented in `create_conversation.go:74-85`: `Cwd`
stops being inert stored metadata and becomes a validated spawn input.

---

## Files to read first

- `internal/relay/handlers/create_conversation.go:46-180` — the handler this ticket edits. **`SessionCreator` interface (57-59)** = the seam to widen; **cwd resolution (101-104)** = where null→`defaultCwd`; **call site (124)** = `creator.Create(mintCtx, string(id))`; **error mapping (126-137)** = all-failures→retryable `server.binary_offline`; **SECURITY comment (74-85)** = the deferral text this ticket must rewrite.
- `cmd/pyry/main.go:660-669` — `sessionMinter` adapter (the `SessionCreator` impl to extend). Note the `poolResolver`/`sessionRouter` precedent for type-narrowing seams that live here.
- `cmd/pyry/main.go:420-468` — `confineWorkdirToHome` + `withinDir`: canonicalises candidate **and** `$HOME` via `EvalSymlinks`, boundary-checks with `filepath.Rel`, returns the realpath. **Reuse verbatim.**
- `cmd/pyry/main.go:502-519` — the daemon bootstrap's `confineWorkdirToHome → trustMark → spawn-in-realpath` sequence. **This slice mirrors it for the per-conversation `Cwd`.**
- `cmd/pyry/agent_run.go:24-32` — `trustMark` (= `trust.MarkWorkdirTrusted`), the overridable test-seam `var`. Reuse; tests override it.
- `internal/sessions/pool.go:904-950` — `Pool.CreateIn(ctx, label, spawnDir)` (#684). `spawnDir==""` → shared `tpl.WorkDir` (today's behaviour); non-empty → used **verbatim**, NOT validated. Callers pass a pre-resolved realpath.
- `internal/agentrun/trust/trust.go:28-46` — `MarkWorkdirTrusted` contract: idempotent, atomic, **returns the resolved realpath** (`agentrun.ResolveWorkdir` = `Abs`+`EvalSymlinks`). Best-effort, no file lock.
- `internal/conversations/conversation.go:40-42` — `Cwd` field: absolute, captured at create time, never updated, phone-influenceable.
- `internal/protocol/codes.go:7-31` — error codes. `CodeProtocolMalformed` (non-retryable in this handler's existing usage) vs `CodeServerBinaryOffline` (retryable).
- `cmd/pyry/workdir_trust_test.go` — the exact test pattern for confine+trust (under-home accept, outside-home reject, symlinked-HOME accept, escaping-symlink reject). **Mirror it for the new helper.**
- `internal/relay/handlers/create_conversation_test.go:16-60` — `stubSessionCreator` (extend to record `spawnDir` + return a configurable rejection) and `newCreateConvConn` helper.
- `docs/protocol-mobile.md:612-633` — error-code table; application codes are **"unchanged from v1"** (the constraint behind reusing `protocol.malformed` rather than minting a new code).

---

## Context

`create_conversation` already mints and binds a dedicated session per
conversation (#681 lineage), but spawns it in the daemon's shared trusted
workdir; the conversation's `Cwd` is stored inert. The #684 primitive
`Pool.CreateIn(ctx, label, spawnDir)` accepts an explicit per-session spawn dir
but does no validation — by design, validation is a cmd-layer job (lesson
`architect-trust-premark-is-cmd-layer-job`; ADR
`025-mobile-remote-head-interactive-session.md`). The trust pre-mark mechanism
(`trustMark`, #670) and the confine-to-`$HOME` boundary (`confineWorkdirToHome`,
#670) both already exist. This slice is the wiring that connects them: thread the
conversation's `Cwd` from the handler to the cmd-layer adapter, validate + trust
there, and hand the realpath to `Pool.CreateIn`.

Because `Cwd` is phone-influenced, it is an **untrusted spawn input**. The
security posture is identical to the daemon's own bootstrap workdir: canonicalise
both sides, confine to `$HOME`, reject escapes, trust-mark the realpath, spawn in
the realpath.

---

## Design

### Data flow

```
phone create_conversation {cwd: "/home/me/proj"?}        (cwd nullable)
        │
        ▼
handlers.CreateConversation                              internal/relay/handlers
        │  spawnDir := (p.Cwd==nil ? "" : *p.Cwd)         raw requested dir
        ▼
creator.Create(ctx, label, spawnDir)                     SessionCreator seam
        │
        ▼
sessionMinter.Create  ──►  resolveSpawnDir(spawnDir)      cmd/pyry (trust+confine)
        │                        │  ""  → ("", nil)
        │                        │  set → confineWorkdirToHome → trustMark → realpath
        ▼                        └─ escape → wrap ErrSpawnDirRejected
Pool.CreateIn(ctx, label, realpath|"")                   internal/sessions
        │  ""  → tpl.WorkDir (shared, today's behaviour)
        ▼  set → spawns claude with WorkDir = realpath (== trust-marked path)
claude in the validated, trusted Cwd
```

### 1. Widen the `SessionCreator` seam (interface contract)

```go
// internal/relay/handlers/create_conversation.go
type SessionCreator interface {
    // Create mints+binds+activates a session whose claude spawns in spawnDir.
    // spawnDir == "" → the daemon's shared trusted workdir (default, unchanged).
    // A non-empty spawnDir is the phone's *requested* working directory; the
    // implementation validates (confine to $HOME, symlink-resolve) + trust-marks
    // it before spawning. A requested dir that escapes $HOME is rejected with an
    // error wrapping ErrSpawnDirRejected.
    Create(ctx context.Context, label, spawnDir string) (string, error)
}
```

Bounded cascade — the only sites that change (`startRelay`/`relay.go` take the
**interface** type and never call `.Create`, so threading stops here):

1. interface decl `create_conversation.go:57-59`
2. impl `sessionMinter.Create` `main.go:666`
3. call site `create_conversation.go:124`
4. test fake `stubSessionCreator.Create` `create_conversation_test.go:28`

### 2. Handler: derive `spawnDir` from the raw nullable `Cwd`

Resolves the "empty-Cwd not visible past the handler" wrinkle (the ticket's
design wrinkle) **structurally**, by reading `p.Cwd` (nullable) rather than the
already-defaulted `cwd`:

- Keep the existing `cwd` resolution (101-104) unchanged — it still feeds the
  recorded row + the `conversation_created` reply. The row records the
  **phone-requested value** (raw) for a set `Cwd`, `defaultCwd` for null — exactly
  today's values (round-trips the phone's own input; see Open Questions).
- Add: `spawnDir := ""; if p.Cwd != nil { spawnDir = *p.Cwd }`. Null → `""`
  (default → shared workdir). Set → the raw requested path (validated downstream).
- Call `creator.Create(mintCtx, string(id), spawnDir)`.

`spawnDir` is derived from `p.Cwd` directly, **not** from `cwd` — this keeps
"what to record" (`cwd`) cleanly separate from "where to spawn" (`spawnDir`), so a
default conversation records `defaultCwd` but spawns in `tpl.WorkDir` (the
trusted bootstrap realpath), byte-identical to today (AC#4).

### 3. cmd-layer: `resolveSpawnDir` + thin `sessionMinter.Create`

New unexported helper in `cmd/pyry/main.go` (sibling of `confineWorkdirToHome`),
mirroring the bootstrap `confine → trust` pair at `main.go:512-519`:

```go
// resolveSpawnDir validates a phone-requested per-conversation spawn workdir.
//   ""  → ("", nil): the pool spawns in the shared trusted template workdir.
//   set → confine to $HOME (symlink-resolve both sides) then trust-mark the
//         realpath; returns trustMark's realpath so claude's cwd and the
//         trust-marked path are byte-identical (AC#3).
// A requested dir that fails confinement (escapes $HOME, or is unresolvable)
// wraps handlers.ErrSpawnDirRejected — a deterministic, non-retryable rejection.
// A trust-mark failure (a transient ~/.claude.json write error) is returned
// plain, so the handler classifies it retryable.
func resolveSpawnDir(requested string) (string, error)
```

Behavior contract (asserted by the cmd/pyry tests below):

- `""` → `("", nil)`; `trustMark` is **not** called.
- set, within `$HOME` → `confineWorkdirToHome(requested)` → realpath →
  `trustMark(realpath)` → return trustMark's realpath. **Order is load-bearing:
  confine gates the `$HOME` bound *before* trust — `trustMark` has no `$HOME`
  bound, so trust-marking first would mark a path outside `$HOME`.**
- set, escaping `$HOME` (incl. via a symlink resolving outside) → confine errors
  → return `fmt.Errorf("…: %w", handlers.ErrSpawnDirRejected)` (the confine error
  detail is content-free and may be wrapped for logs; `errors.Is` must match the
  sentinel).
- set, `trustMark` errors → return the trust error **plain** (no sentinel).

`sessionMinter.Create` collapses to: `resolved, err := resolveSpawnDir(spawnDir)`;
on error return `("", err)`; else `id, err := m.p.CreateIn(ctx, label, resolved)`.
Pass trustMark's return value to `CreateIn` (exactly as bootstrap passes
`trustedWorkdir` to `Bootstrap.WorkDir`) — this is what guarantees AC#3
byte-identity, independent of whether `trusted == confined-realpath`.

### 4. New sentinel + error mapping

- New exported sentinel in `handlers` (consumer/mapper package owns it; cmd/pyry
  wraps it — no import cycle, cmd/pyry already imports handlers):
  ```go
  // ErrSpawnDirRejected marks a deterministic rejection of a requested spawn
  // workdir (escapes $HOME / unresolvable). SessionCreator implementations wrap
  // it; the handler maps it to a non-retryable reply rather than a retryable one.
  var ErrSpawnDirRejected = errors.New("conversation spawn directory rejected")
  ```
- Handler error mapping (replaces the single branch at 126-137):
  - `errors.Is(err, ErrSpawnDirRejected)` → `replyError(…, protocol.CodeProtocolMalformed, msgCreateConversationCwdRejected, false)` — **non-retryable**; re-issuing the same `Cwd` fails identically, so retrying is pointless. New static message const (does **not** echo the path or `~/.claude.json`).
  - any other error → existing retryable `CodeServerBinaryOffline` / `msgCreateConversationMintFailed` path, unchanged.
  - Both branches still `return` **before** `reg.Create` → no half-bound row, no `reg.Save` (AC#2).
- Add `"errors"` to the handler's import set.

**Why reuse `protocol.malformed` (non-retryable) over a new code:** `docs/protocol-mobile.md:614` pins application error codes as "unchanged from v1"; minting `conversation.cwd_rejected` is a v2 protocol-vocabulary change (codes.go + spec table + `compat_test.go` drift detector + mobile-client handling) — out of an S slice's lane and a cross-repo contract change. `protocol.malformed` is already this handler's non-retryable "bad request" channel and the phone already treats it as non-retryable. The semantic stretch ("malformed" vs "well-formed-but-refused") is acceptable; the correct retry behaviour is what matters. A dedicated code is noted as a deferred protocol follow-up.

### 5. Rewrite the SECURITY comment (`create_conversation.go:74-85`)

The current comment states `Cwd` is "structurally excluded" as a spawn input and
defers it. This ticket reverses that. The rewrite must state the new posture:
`Cwd` **is** now a spawn input, threaded through `creator.Create`'s `spawnDir`
parameter, validated at the cmd-layer adapter (`sessionMinter` → `resolveSpawnDir`)
by `confineWorkdirToHome` (canonicalise + confine to `$HOME`, reject escapes) and
`trustMark` (pre-mark the realpath trusted) before reaching `Pool.CreateIn`; the
handler itself does no path handling and stays free of `internal/sessions` /
cmd-layer imports. Leaving the stale comment is a security-doc regression.

---

## Concurrency model

No new goroutines, locks, or channels. `resolveSpawnDir` is synchronous on the
per-conn handler goroutine, inside the existing `createConversationMintTimeout`
budget. `trustMark` gains a new caller (per-conversation create, in addition to
bootstrap startup and `agent-run`) but inherits the existing best-effort,
single-writer-axis-atomic posture (tempfile+rename, no file lock): a concurrent
`~/.claude.json` write may lose an update, and tui-driver's `HasTrustModal(snap)`
runtime safety net dismisses the modal if the pre-mark lost the race. The
per-conversation sessions are tui-driver-hosted (same host loop as bootstrap,
#593), so they get that backstop. No new lock is warranted (belt-and-suspenders:
the deterministic backstop is tui-driver, not another stochastic layer).

---

## Error handling

| Failure | Where | Surfaces as | Retryable | Row recorded? |
|---|---|---|---|---|
| `Cwd` escapes `$HOME` / unresolvable | `confineWorkdirToHome` in `resolveSpawnDir` | `protocol.malformed` | **no** | no |
| `~/.claude.json` write fails | `trustMark` in `resolveSpawnDir` | `server.binary_offline` | yes | no |
| pool not running / activate timeout / save failure | `Pool.CreateIn` | `server.binary_offline` | yes | no |
| inaccessible dir that passes confinement | spawn-time chdir (supervisor) | (existing `CreateIn` spawn-time path) | — | row already recorded if mint returned a sessionID |

All `resolveSpawnDir`/`CreateIn` failures return **before** `reg.Create`, so no
escape ever produces a half-bound conversation row (AC#2). The confine/trust
errors are logged with the existing content-free discipline (`confineWorkdirToHome`
names only the path + boundary; `trust` never logs file contents).

---

## Testing strategy

Scenarios (developer writes them table-driven in the project idiom; not full
bodies here).

**Handler — `internal/relay/handlers/create_conversation_test.go`** (extend
`stubSessionCreator` to record the `spawnDir` arg and to optionally return a
`fmt.Errorf("…: %w", ErrSpawnDirRejected)`):

- **set `Cwd` threads spawnDir** — payload `Cwd="/home/x/proj"` → stub records
  `spawnDir == "/home/x/proj"`; row `Cwd == "/home/x/proj"`; reply `Cwd` matches;
  no validation happens in the handler (stub is the seam).
- **null `Cwd` → empty spawnDir, default row** — payload `Cwd=nil` → stub records
  `spawnDir == ""`; row `Cwd == defaultCwd`; reply `Cwd == defaultCwd`
  (byte-identical to today, AC#4).
- **spawn-dir rejection** — stub returns wrapped `ErrSpawnDirRejected` → reply is
  `protocol.malformed` with `retryable == false`; `reg.Create` **not** called (no
  row); `reg.Save` **not** called; no `conversation_created` envelope (AC#2).
- **regression: generic mint failure stays retryable** — stub returns a plain
  error (not the sentinel) → reply is `server.binary_offline`, `retryable == true`
  (guards the existing path against the new branch).

**cmd/pyry adapter — new `cmd/pyry/conversation_spawndir_test.go`** (mirror
`workdir_trust_test.go`: `t.Setenv("HOME", …)` so non-parallel; override the
`trustMark` var with a recording stub):

- `resolveSpawnDir("")` → `("", nil)`; trustMark **not** called.
- within-`$HOME` dir → returns trustMark's realpath; trustMark called once with
  the confined realpath; result is byte-identical to trustMark's return (AC#3).
- outside-`$HOME` dir → error with `errors.Is(err, handlers.ErrSpawnDirRejected)`;
  trustMark **not** called.
- symlink under `$HOME` pointing outside → rejected (`errors.Is` sentinel) — the
  symlink-escape case AC#2/AC#5 name.
- trustMark stub returns an error → result error does **not** match
  `ErrSpawnDirRejected` (so the handler classifies it retryable).

Run `go test -race ./...` (handler + cmd/pyry). No e2e change required — the
existing two-phone harness gap (lesson `architect-structured-stream-e2e-harness-gap`)
does not block this; unit coverage at the two seams is sufficient and matches how
#684's primitive and #670's confine/trust were verified.

---

## Open questions

1. **Recorded row `Cwd`: raw vs realpath.** Decision: record the
   **phone-requested raw value** (today's behaviour) and spawn in the realpath.
   Rationale: round-trips the phone's own input in `conversation_created`
   (a macOS `/var`→`/private/var` realpath in the reply would surprise the
   phone); the row's `Cwd` drives nothing downstream (respawns read
   `supervisor.Config.WorkDir`, captured as the realpath at `CreateIn` time, not
   re-derived from the row). If a future reader needs the row to equal claude's
   actual cwd, the seam would have to return the realpath — deferred; not needed
   by any current consumer. Developer should not change this without a consumer.
2. **Dedicated error code.** Reusing `protocol.malformed` (non-retryable) per the
   v1-codes-frozen constraint. If the mobile team wants finer UX (e.g. "directory
   not allowed" vs generic malformed), a `conversation.cwd_rejected` code is a
   separate protocol ticket spanning this repo + mobile.
3. **Non-existent requested path.** `confineWorkdirToHome` fails at `EvalSymlinks`
   for a path that does not exist → treated as `ErrSpawnDirRejected`
   (non-retryable), same as an escape. This is a deliberate consequence (a
   phone-named dir that isn't there is a deterministic client error), not in the
   ACs but a natural and correct outcome.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX — the untrusted→trusted boundary is explicit
  and single: the phone-supplied `Cwd` crosses into spawn-input trust **only** at
  `resolveSpawnDir` (cmd/pyry), via `confineWorkdirToHome` (canonicalise both
  sides + `$HOME` confinement, the same boundary the daemon's own bootstrap
  workdir uses) then `trustMark`. The `internal/relay/handlers` layer never
  touches the path — it forwards the raw value through the typed seam and maps the
  sentinel. `Pool.CreateIn` (#684) is documented to use `spawnDir` verbatim and
  trusts the caller, so the cmd layer is the sole validator; this matches the
  pool's stated contract (`pool.go:904-909`). The rewritten SECURITY comment
  (§5) documents the reversed posture so the next reader isn't misled.
- **[File operations — path traversal]** No MUST FIX — this is the core threat and
  it is mitigated by reuse, not reinvention. `confineWorkdirToHome` resolves the
  candidate **and** `$HOME` with `EvalSymlinks` before a boundary-aware
  `filepath.Rel` check (`withinDir`), so a symlink under `$HOME` that resolves
  outside is rejected (proven by `TestConfineWorkdirToHome_RejectsSymlinkEscapingHome`
  and re-asserted by the new cmd/pyry test). The confine-then-trust **order** is
  load-bearing and called out in §3: `trustMark` has no `$HOME` bound, so a
  reversed order would auto-trust an out-of-`$HOME` path.
- **[File operations — TOCTOU]** SHOULD FIX (developer-aware, recoverable) — there
  is a window between `confineWorkdirToHome`'s `EvalSymlinks` and claude's
  eventual `chdir`: a path that resolved inside `$HOME` could be swapped to a
  symlink escaping `$HOME` before the child opens it. This is the **same residual
  window the daemon's own bootstrap workdir already accepts** (`main.go:512-519`
  does confine→trust→spawn with the identical gap), and the attacker must already
  control the operator's home directory to win it. No new mitigation is in scope;
  the design does not widen the existing window. Named here so code-review doesn't
  treat it as novel.
- **[File operations — permissions/atomic]** No findings — `trustMark` writes
  `~/.claude.json` via tempfile+`Chmod`+fsync+rename preserving the existing mode
  (`trust.go:103-129`); this ticket adds a caller, not a new write path.
- **[Subprocess execution]** No MUST FIX — the validated realpath becomes the
  child's working directory (`supervisor.Config.WorkDir`), not an `exec` argument;
  it is never shell-interpreted. The session id (`--session-id`) remains
  server-minted (`sessions.NewID`, crypto/rand); the conversation `Cwd` does not
  reach claude's argv.
- **[Error messages / logs]** No findings — the rejection reply carries a static
  message (`msgCreateConversationCwdRejected`) that does **not** echo the path or
  `~/.claude.json`; `confineWorkdirToHome`'s error names only the path + `$HOME`
  boundary (content-free, asserted by `TestConfineWorkdirToHome_RejectsOutsideHome`)
  and is logged, never sent on the wire; `trust` never logs file contents.
- **[Concurrency]** No MUST FIX — `trustMark` gains a new caller but no new lock
  requirement: it stays best-effort, single-writer-axis-atomic (no file lock), and
  a lost `~/.claude.json` update is backstopped deterministically by tui-driver's
  `HasTrustModal` dismissal for the per-conversation (tui-driver-hosted) session —
  the same belt-and-suspenders the bootstrap path relies on. No new goroutines.
- **[Threat model alignment]** Prompt injection (`protocol-mobile.md` § Security
  model, threat #1) is **unchanged and out of scope** — letting the phone choose
  the spawn directory does not broaden claude's capabilities beyond what the
  already-paired phone has; the `$HOME` confinement is precisely what keeps a
  phone-chosen workdir from escaping the operator's home into system paths or other
  users' spaces, which is the new-surface concern this ticket introduces and
  closes.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-19
