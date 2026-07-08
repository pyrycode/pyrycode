# Spec — #833: persist per-session model / effort / YOLO and launch claude with them

**Size:** S (PO-sized S; architect confirms — 4 production files, 1 new exported type, 2 `buildSession` call sites, ~60–70 production LOC + ~250 test LOC).

**Label:** `security-sensitive` — the YOLO/bypass-permissions field can enable `--dangerously-skip-permissions`. The security-review pass (§ Security review below) is part of this spec.

## Context

Today model / effort / YOLO are only expressible as **template-wide** claude flags captured once at `pyry` startup (`SessionConfig.ClaudeArgs`, threaded to `supervisor.Config.ClaudeArgs`). A session carries no per-session settings, and the on-disk registry (`~/.pyry/<name>/sessions.json`) stores none.

Before a client can change these over the wire (#826b) or read them for a Status sheet (#826c), a session must actually **hold** these settings, **persist** them, and **honour** them at spawn. This ticket is that storage + spawn primitive — **no wire message**. The setter verb is #826b; the reader verb is #826c.

The interactive supervised-spawn path (`buildSession` / `New`) currently omits `--dangerously-skip-permissions` **by design**. Turning YOLO on is a deliberate, per-session opt-in, never a default. (The `pyry agent-run` path — `internal/agentrun/ptyrunner` — deliberately *forbids* that flag and uses `--permission-mode dontAsk` + a deny-default settings file instead; it is a reference for the `--model` / `--effort` flag mapping, **not** a shared code path. See #538.)

### Key architectural fact that shapes the design

**Only the bootstrap session is reloaded on a daemon restart.** `Pool.New` reads the registry via `pickBootstrap` and constructs exactly one `*Session`. Non-bootstrap (minted) entries are persisted by `saveLocked` but are **not** re-materialised into `p.sessions` on restart. Consequences:

- The **daemon-restart round-trip** (AC #1: save → reload → identical) is exercised end-to-end for the **bootstrap** session (write registry → `New` reads settings → applied to argv → `saveLocked` re-persists identically). For minted sessions the round-trip is at the registry-serialization layer only — a pre-existing limitation, not this ticket's concern.
- Both spawn-config assembly sites therefore need the settings applied: **`New`** (bootstrap) and **`buildSession`** (minted). They share one small pure helper.

## Files to read first

- `internal/sessions/registry.go:17-29` — `registryFile` + `registryEntry`; the existing `omitempty` fields (`Bootstrap`, `LifecycleState`) are the exact pattern the three new fields follow. **What to extract:** field-tag style + the "new per-session fields can be added without breaking old pyry" contract.
- `internal/sessions/registry.go:31-51` — `loadRegistry`: lenient `json.Unmarshal` (missing keys → zero values) **and** hard-error on malformed JSON. **What to extract:** this is the fail-loud path that satisfies AC #2's "corrupt YOLO must never silently enable bypass" — a wrong-typed `yolo` fails the whole parse; no new reject branch is needed.
- `internal/sessions/pool.go:355-417` — `New`: the bootstrap warm-start read (`pickBootstrap`, lines 355-370) and the bootstrap `supCfg` assembly (lines 389-417). **What to extract:** where to read `entry.Model/Effort/YOLO` and where to apply the settings flags to `supCfg.ClaudeArgs`.
- `internal/sessions/pool.go:1035-1096` — `buildSession`: `args := append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))` (line 1037) and the `&Session{...}` literal. **What to extract:** the minted spawn-config site; where the settings param is applied and stored on the Session.
- `internal/sessions/pool.go:1185-1215` — `saveLocked`: builds `registryEntry` from `*Session` fields; note the `omitempty`/`LifecycleState` discipline (only written when non-default). **What to extract:** where to copy `s.settings` into the entry.
- `internal/sessions/get_or_create.go:58-71` — `GetOrCreateIn`: the **second** `buildSession` caller. **What to extract:** the 1-line call-site update.
- `internal/sessions/session.go:64-108` — `Session` struct; the "Persisted metadata … immutable post-New" block (lines 70-77). **What to extract:** where the `settings` field goes and the immutability/locking discipline it inherits (read under `Pool.mu`, like `label`).
- `internal/agentrun/ptyrunner/runner.go:595-604` — `buildArgs`: the canonical flag literals `--session-id` / `--model` / `--effort`. **Reference only — NOT a shared code path.** **What to extract:** exact flag spellings.
- `internal/sessions/pool_spawndir_test.go:17-42` — the argv-recorder test idiom: template `ClaudeArgs` is a `sh -c SCRIPT --` whose script observes its own positional params (the appended flags) and writes a side-effect file. **What to extract:** reuse this pattern for the end-to-end spawn-argv assertion (AC #3/#4/#5).
- `docs/specs/architecture/538-permission-mode-dontask-argv.md` — confirms `--dangerously-skip-permissions` is forbidden on the agent-run path; the interactive supervised path here is its sole legitimate user (bypass-mode reference).

## Design

Three moving parts, all in `internal/sessions`:

### 1. Storage — `registryEntry` gains three fields (`registry.go`)

Add to `registryEntry`, following the existing `omitempty` pattern:

```go
Model  string `json:"model,omitempty"`
Effort string `json:"effort,omitempty"`
YOLO   bool   `json:"yolo,omitempty"`
```

- `Model` / `Effort` empty (the default) → key omitted on disk → stable byte-shape for the dominant case (matches the idempotent-reload guarantee). Empty means **"inherit the daemon template"**.
- `YOLO` is a **plain `bool`** whose zero value is `false` = **permissions enforced (bypass OFF)** — the fail-safe default. `omitempty` omits it when false, so a default session's on-disk shape is byte-unchanged.
- **No `*bool`, no custom decoder.** A missing `yolo` key decodes to `false` (AC #2). A malformed `yolo` value (wrong JSON type, e.g. `"yolo": "maybe"`) fails the whole `json.Unmarshal` in `loadRegistry`, which returns a hard error → `New` fails loudly at startup. Neither absence nor corruption can ever yield `YOLO == true`. This is the entire fail-safe argument; it needs no new code, only the existing `loadRegistry` error path.

### 2. In-memory carrier — `SessionSettings` type + `Session.settings` field

New **exported** value type (the seam #826b/#826c consume):

```go
// SessionSettings is the per-session model / reasoning-effort / bypass-permissions
// triple persisted in the registry and applied to the claude spawn argv. The zero
// value inherits the daemon template for Model/Effort and enforces permissions
// (YOLO off) — the fail-safe default.
type SessionSettings struct {
    Model  string
    Effort string
    YOLO   bool
}
```

Add `settings SessionSettings` to the `Session` struct's "Persisted metadata … immutable post-New" block (`session.go:70-77`). **Immutable post-construction in this ticket** — set once in `New` (bootstrap) or `buildSession` (minted), read under `Pool.mu` by `saveLocked` (same discipline as `label`). Document that #826b's setter must revisit synchronization (it will mutate `settings` and re-persist, exactly as `Rename` does for `label`).

### 3. Spawn-argv mapping — one pure helper, two call sites

```go
// claudeSettingsArgs returns the extra claude flags implied by s. Empty Model /
// Effort emit no flag (inherit the template). YOLO==true appends
// --dangerously-skip-permissions; YOLO==false appends NOTHING — absence of the
// flag is what enforces permissions. Never emits a disabling flag.
func claudeSettingsArgs(s SessionSettings) []string
```

Behaviour (assert as a table test — see § Testing):
- `Model != ""` → append `--model`, `<model>`
- `Effort != ""` → append `--effort`, `<effort>`
- `YOLO == true` → append `--dangerously-skip-permissions`
- zero value → return `nil` (empty) → callers append nothing → **byte-identical argv** (AC #5)

Deterministic order (model, then effort, then bypass). Flag *position* in the final argv is irrelevant to claude; determinism is for testability.

**Apply at both sites** via `args = append(args, claudeSettingsArgs(s)...)`:

- **`buildSession`** (minted, `pool.go:1037`): after the `--session-id` append. `buildSession` gains a `settings SessionSettings` parameter; store it on the returned `Session` (`settings: settings`). The two callers pass the value:
  - `CreateIn` (`pool.go:988`) → passes `SessionSettings{}` (zero → byte-identical minted argv today).
  - `GetOrCreateIn` (`get_or_create.go:68`) → passes `SessionSettings{}`.
  - This keeps the public `Create` / `GetOrCreate` signatures **unchanged** (no external fan-out into `cmd/pyry` / `internal/acp`). #826b extends the mint path to plumb real settings; here the seam is live (exercised with the zero value in production, non-zero in same-package tests).
- **`New`** (bootstrap, `pool.go:389-417`): read `settings` from the warm-start entry (or zero on cold start), then
  `supCfg.ClaudeArgs = append(slices.Clone(cfg.Bootstrap.ClaudeArgs), claudeSettingsArgs(settings)...)`
  and store `settings` on the bootstrap `Session`. With zero settings the appended slice has identical elements → byte-identical (AC #5). (The clone is required because we now `append`; today's code aliases `cfg.Bootstrap.ClaudeArgs` directly.)

### 4. Warm-start read (`New`, `pool.go:355-370`)

In the `if entry := pickBootstrap(reg); entry != nil` branch, read:
```go
settings = SessionSettings{Model: entry.Model, Effort: entry.Effort, YOLO: entry.YOLO}
```
Cold-start branch leaves `settings` as the zero value. Note: the bootstrap **ignores** persisted `lifecycle_state` (existing #202 rule) but **honours** persisted settings — settings are the operator's spawn intent and must survive restart; lifecycle state is per-process. These are orthogonal; do not couple them.

### 5. Persist (`saveLocked`, `pool.go:1185-1215`)

After building `entry` from the session, copy settings (relying on `omitempty` to keep the default shape stable):
```go
entry.Model = s.settings.Model
entry.Effort = s.settings.Effort
entry.YOLO = s.settings.YOLO
```
Read `s.settings` under the already-held `Pool.mu` (grouped with the existing `s.label` read, **not** under `lcMu` — settings is immutable, guarded by `Pool.mu`).

## Data flow

```
DAEMON RESTART (bootstrap):
  sessions.json ──loadRegistry──> registryEntry{Model,Effort,YOLO}
       │
       ▼ New: settings = {entry.Model, entry.Effort, entry.YOLO}
  supCfg.ClaudeArgs = clone(Bootstrap.ClaudeArgs) + claudeSettingsArgs(settings)
       │                                    │
       ▼ Session{settings}                  ▼ supervisor.New → runOnce → exec claude ... --model X --effort Y [--dangerously-skip-permissions]
  saveLocked ──> registryEntry (identical, omitempty-stable)   [round-trip, AC #1]

MINTED (Create/GetOrCreate → buildSession):
  buildSession(id, label, spawnDir, settings)
       │  args = clone(tpl.ClaudeArgs) + [--session-id id] + claudeSettingsArgs(settings)
       ▼  (this ticket: callers pass SessionSettings{} → no extra flags → byte-identical; #826b passes real values)
  Session{settings} ──saveLocked──> registryEntry
```

## Concurrency model

No new goroutines, no new locks, no lock-order changes. `settings` is immutable post-construction and read under `Pool.mu` (write held by `saveLocked`; the field is set before the session is registered in `p.sessions`, giving a happens-before through `Pool.mu`). `claudeSettingsArgs` is a pure function. The forward note for #826b: a mutable setter must take `Pool.mu` (write) and re-`saveLocked`, exactly as `Pool.Rename` does for `label`.

## Error handling

- **Corrupt `yolo` (or any field) → fail loud.** Handled entirely by the existing `loadRegistry` `json.Unmarshal` error path (`registry.go:47-49`) → `New` returns `fmt.Errorf("sessions: load registry: %w", err)`. No new branch. The daemon does not start; bypass is never enabled.
- **Missing fields → zero values.** Lenient decode; `Model=""`, `Effort=""`, `YOLO=false`. No error.
- **Model/effort values are operator-trusted this ticket** — they arrive only from the local 0600 registry (or the zero value on the mint path). Values reach claude as **separate argv tokens** via `exec.CommandContext` (no shell), so a value that looks like a flag (`"--model", "x --dangerously-skip-permissions"`) is a single non-splittable argument claude rejects, not an injected flag. **Validation of untrusted (wire-supplied) model/effort belongs to #826b**, where the trust boundary is actually crossed. Note this explicitly in the spec's handoff so #826b does not assume this ticket validated them.

## Testing strategy

Same-package (`package sessions`), stdlib `testing`, table-driven, `-race`. Reuse existing fixtures — `ClaudeArgs: []string{...}` shell templates and the `pool_spawndir_test.go` recorder idiom.

- **`claudeSettingsArgs` table (unit):** zero → `nil`; model-only → `["--model","X"]`; effort-only → `["--effort","Y"]`; both; YOLO-on → `["--dangerously-skip-permissions"]`; model+effort+YOLO → all three in deterministic order; **YOLO-off never yields any bypass flag**. This is the primary AC #3/#4/#5 assertion at the logic layer.
- **Registry round-trip (`registry_test.go`):** a `registryFile` whose entry sets Model/Effort/YOLO → `saveRegistryLocked` → `loadRegistry` → identical values (AC #1). Assert the default entry (empty model/effort, YOLO false) serializes **without** those keys (omitempty byte-shape).
- **Lenient decode of a pre-existing file (AC #2):** a `sessions.json` with **no** model/effort/yolo keys → `loadRegistry` → no error; entry has `""`,`""`,`false`.
- **Corrupt YOLO fails loud, never enables (AC #2, security):** a `sessions.json` with `"yolo": "maybe"` → `loadRegistry` returns a non-nil error; assert the decode never produced `YOLO==true`. (Drive `New` with such a registry → `New` errors, no session spawns.)
- **Bootstrap warm-start applies settings end-to-end (AC #1/#3/#4):** pre-write a registry with the bootstrap entry carrying Model/Effort/YOLO, using a template `ClaudeArgs` shell script that records its own argv → `New` + `Run` → assert the recorded argv contains the settings flags. Cold start / no settings → recorded argv **byte-identical** to today (AC #5).
- **Minted spawn applies settings (AC #3/#4/#5):** call `buildSession` directly (same-package) with non-zero `SessionSettings`, spawn via the recorder template → assert flags present alongside `--session-id`. With `SessionSettings{}` → argv byte-identical to today (the existing `pool_create` / `pool_spawndir` tests must still pass unchanged).
- **Bootstrap settings survive `New`→persist→reload (AC #1):** pre-write registry with settings → `New` → trigger a `saveLocked` (e.g. a state-changing op or direct persist) → re-`loadRegistry` → identical values.

## Security review

*(Mandatory: ticket is `security-sensitive`. Pass performed on this spec before commit. Verdict below.)*

**Trust boundaries.** The only input surface this ticket adds is the on-disk registry `~/.pyry/<name>/sessions.json` (mode 0600, dir 0700 — `saveRegistryLocked`). It is operator-local, not remote. **No wire input** (that is #826b). The values flow: registry → `SessionSettings` → argv tokens → `exec.CommandContext` (no shell).

**Assets & threats walked:**

1. **YOLO silently enabling `--dangerously-skip-permissions`.** *Primary asset.* Mitigations, each deterministic:
   - Zero value of `bool` is `false` = enforced. Absence of the flag is what enforces permissions; the code never emits a "disable bypass" flag (§ 3).
   - Missing `yolo` key → `false` (lenient decode).
   - Malformed `yolo` value → whole-file `json.Unmarshal` error → `New` fails at startup (fail-closed); no partial-parse can set `YOLO=true` (`registry.go:47-49`).
   - Atomic write (`os.CreateTemp` → fsync → `os.Rename`, `registry.go:60-92`) guarantees no torn on-disk state — a SIGKILL mid-write leaves pre- or post-image, never a half-written `"yolo":tr`.
   - **Belt-and-suspenders is different fabric:** the safe default is *code* (`bool` zero value), and the corruption guard is *code* (`json.Unmarshal` strictness) — no stochastic component.
2. **argv injection via model/effort.** `exec.CommandContext` runs claude with no shell; each value is a distinct argv element. A crafted value cannot become a separate flag. Registry values are operator-trusted here. **Handoff:** #826b introduces the untrusted (wire) set-path and MUST validate model/effort there — explicitly out of scope here and flagged so #826b does not inherit a false assumption of validation.
3. **Information disclosure in logs.** Model/effort/YOLO are not secrets, but keep them out of noisy logs by default; no new log line is required by this ticket. (No finding.)
4. **Privilege / persistence.** Enabling YOLO is persisted and survives restart — that is the intended behaviour (the operator opted in). The corresponding *risk mitigation* — YOLO can only be turned on by an explicit, authenticated action — is enforced upstream (#826b's authz), not here; this ticket only makes an already-set flag take effect at spawn and defaults it OFF. (No finding for this ticket.)

**Decision criteria:** the one security-critical property (bypass fail-safe-OFF on absence/corruption) is enforced by deterministic code with no new branch; the untrusted-input concern is correctly deferred to the ticket that owns that boundary with an explicit handoff note. **Verdict: PASS.**

## Open questions

- **#826b setter shape (out of scope, flagged for the next architect):** whether the setter mutates `Session.settings` + rebuilds the bootstrap supervisor in place, or evict→rebuild via a `buildSession`-style path. This spec keeps `settings` immutable and the argv helper reusable so either shape is cheap. Not resolved here — no code depends on it.
- **On-disk field name `yolo` vs `bypass_permissions`.** Chosen `yolo` to match the ticket/PO/#826-family vocabulary; the security semantics live in the struct doc-comment. If #826c's Status sheet prefers the explicit spelling on the wire, that is a wire-layer alias, not an on-disk change.

## Out of scope

- Any wire message / verb (set → #826b, read → #826c).
- Reloading non-bootstrap sessions on daemon restart (pre-existing limitation).
- Rebuilding a running session's supervisor when settings change (#826b).
- Validating untrusted model/effort values (#826b — the wire boundary).
- The `docs/knowledge/codebase/833.md` note — owned by the documentation phase, written post-merge. Not a developer deliverable.
