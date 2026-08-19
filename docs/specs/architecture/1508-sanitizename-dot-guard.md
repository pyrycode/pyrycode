# #1508 — `sanitizeName` must never return `.` or `..`

**Size:** XS (confirmed; PO sized XS). One production function in one file, four test files' worth of pins, one doc sentence.

## Files to read first

Symbols, not lines — resolve each with `codegraph_node` / `codegraph_search`.

| Where | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/main.go` | `sanitizeName` | The whole production change lives here. Note the two exits: the `b.Len() == 0` → `"_"` guard, and the plain `b.String()` return. |
| `cmd/pyry/main.go` | `resolveRegistryPath`, `resolveConversationsRegistryPath`, `resolveSocketPath` | The join shape. Registry-style resolvers pass the name as a **directory component**; `resolveSocketPath` concatenates a `.sock` suffix instead — that difference is why only the former four are vulnerable. |
| `cmd/pyry/pair.go` | `resolveDevicesPath`, `resolveServerIDPath` | Same directory-component shape as `resolveRegistryPath`. No change needed; they inherit the fix. |
| `cmd/pyry/args_test.go` | `TestSanitizeName` | The table you extend. Every existing row keeps its current `want` (AC 4). |
| `cmd/pyry/args_test.go` | `TestResolveConversationsRegistryPath`, `TestResolveSocketPath` | The two existing idioms: non-parallel + `t.Setenv("HOME", …)` for the registry-style test, parallel + no env mutation for the socket test. This distinction is load-bearing — see § Testing strategy. |
| `cmd/pyry/pair_test.go` | `TestResolveDevicesPath`, `TestResolveServerIDPath` | The `filepath.Rel` containment assertion that is about to be replaced by a stricter one. |
| `internal/keys/static_key.go` | `validDaemonName` | Confirms the fix does not change any `keys.LoadOrCreate` accept/reject outcome: every byte must be in `[a-z0-9_-]`, so `.` is rejected — and `._` / `.._` still contain `.`. |
| `docs/knowledge/features/keys-package.md` | § "Deliberately NOT shared with `cmd/pyry/main.go:sanitizeName`" | The one sentence AC 5 corrects. Read the whole section; the rationale around the example is preserved. |

## Context

`sanitizeName` is the only path-traversal defence in front of five per-instance path resolvers. Its allowlist includes `.`, so the two literal names `.` and `..` survive the transform unchanged and reach `filepath.Join` as live path components.

Measured on this worktree (`main` at `fc91fd3`) by running the five resolvers under `HOME=$(t.TempDir())` — an overlay probe, no worktree writes:

| `PYRY_NAME` | `sanitizeName` | `filepath.Rel(~/.pyry, resolveRegistryPath(name))` | components |
|---|---|---|---|
| `pyry` | `pyry` | `pyry/sessions.json` | 2 — correct |
| `../etc` | `.._etc` | `.._etc/sessions.json` | 2 — already defended |
| `.` | `.` | `sessions.json` | **1 — instance directory collapsed away; lands beside `config.json` in `~/.pyry`** |
| `..` | `..` | `../sessions.json` | **2, first is `..` — lands in `$HOME`** |

`conversations.json`, `devices.json` and `server-id` behave identically. `resolveSocketPath` is unaffected — for `.` and `..` it yields `~/.pyry/..sock` and `~/.pyry/...sock`: odd filenames, correct directory.

Two measurements from that probe drive the whole test design and are worth stating before anything else:

1. **For `.`, the containment check that the existing tests use returns "no escape."** `rel` is `sessions.json`, which neither equals `..` nor is prefixed by `../`. The bug is invisible to today's assertion shape.
2. **For `..`, that same check *does* fire** (`rel` is `../sessions.json`). The three existing containment tests would already be red if they were run with `..` — they only ever pass `../etc`.

So the two hostile inputs are caught by two *different* assertion clauses, and neither clause alone covers both. § Testing strategy turns that into an explicit matrix.

## Design

### The change

One guard inside `sanitizeName`, applied to the **built result**, not to the input:

```go
// sketch — after the existing empty-input guard
switch out := b.String(); out {
case ".", "..":
    return out + "_" // "._" and ".._"
default:
    return out
}
```

Checking the output rather than the input is the durable form: it states the function's postcondition directly, so it keeps holding if the allowlist ever changes. (Today the two are equivalent — the transform only ever maps a disallowed rune to `_`, so `.` and `..` are the sole inputs that can produce those outputs.)

### Why `_` appended, and not some other value

The mapping falls out of the function's existing behaviour rather than inventing a convention:

- `sanitizeName("./x") == "._x"` and `sanitizeName("../x") == ".._x"` today — the separator becomes `_`.
- So `.` → `._` and `..` → `.._` is exactly "as if a separator followed," which is what a bare `.` or `..` means to `filepath.Join` in the first place.

Rejected alternatives:

- **Collapse both to `"_"`** — loses the distinction between `.`, `..`, `""` and `"/"` (the latter two already both map to `"_"`). Three hostile names sharing one state directory is worse than two absurd names each having their own.
- **Prepend `_` (`"_."`, `"_.."`)** — equally correct, but breaks the "separator became `_`" mnemonic and reads as an unexplained prefix.
- **Strip leading dots** — would relocate legitimate names like `.staging`. AC 4 forbids moving any name that resolves today.

### Invariant this establishes

> `sanitizeName` returns a non-empty string containing no path separator, that is never `"."` and never `".."`. Therefore `filepath.Join(dir, sanitizeName(name), file)` always yields `dir/<one-component>/file`.

Put this in the doc comment on `sanitizeName`, naming the test that pins it (symbol, not line — `make cite-guard` is in `make check` and rejects line citations in comments the branch adds).

### What deliberately does not change

- **No resolver changes.** All five call sites are untouched; the fix is entirely upstream of them. `resolveSocketPath` keeps concatenating rather than joining — it was never vulnerable, and AC 3 pins that rather than altering it.
- **No error returns.** `sanitizeName` stays a total function `string → string`, so no caller grows an error path.
- **No `keys.LoadOrCreate` behaviour change.** `validDaemonName` requires every byte in `[a-z0-9_-]`; `._` and `.._` still contain `.`, so `PYRY_NAME=..` is still rejected with `ErrInvalidDaemonName` at exactly the same point in `runPairDefault`. The fix moves *where the pre-rejection write lands*, not *whether the rejection happens*.
- **No unification with `keys.validDaemonName`** — see the ticket's Technical Notes; two knowledge docs record the separation as deliberate, and adopting the stricter allowlist would orphan the on-disk state of working names like `MyBox` and `proj.v2` (both verified to resolve today).

### Operator-visible consequence, accepted

An operator who is running with `PYRY_NAME=.` or `PYRY_NAME=..` today has state at `~/.pyry/sessions.json` or `~/sessions.json`. After this change pyry looks in `~/.pyry/._/` or `~/.pyry/.._/` and finds nothing, starting fresh; the old files are orphaned, not deleted.

That is intended — the old location is the bug. No migration, no cleanup, no warning log: there is no evidence anyone runs either name, and every one of those additions costs an error path the ticket's scope bound explicitly excludes. Called out again under § Security review so it is a recorded decision rather than an oversight.

## Concurrency model

None. `sanitizeName` is a pure function over its argument with no shared state, no I/O, and no goroutines; the resolvers it feeds are equally pure. No lock, no context, no shutdown sequence is in play. The only concurrency consideration in this ticket is a *test* one — `t.Setenv` versus `t.Parallel` — handled in § Testing strategy.

## Error handling

No new failure mode exists to handle. The function is total: every input produces a valid single path component, and the two previously-dangerous outputs now produce valid ones. Degenerate inputs continue to degrade rather than fail — `""` and `"/"` still yield `"_"`, consistent with the existing contract that a bad `PYRY_NAME` produces a working (if odd) state directory instead of aborting startup.

Downstream rejection is unchanged: a name the keystore refuses still surfaces as `pyry: pair: keys: invalid daemon name "…"` and exit 1 from the pair verb.

## Testing strategy

All scenarios below are behaviour descriptions; write them in the package's existing table/subtest idiom.

### The mutation matrix (read this first)

Two hostile inputs, two assertion clauses, each clause the sole detector of one input. A developer who writes only one clause gets a green test that proves half the fix:

| Input | `rel` today | component count | first component | Detected only by |
|---|---|---|---|---|
| `.` | `sessions.json` | 1 | `sessions.json` | **"exactly two components"** — the first-component check passes |
| `..` | `../sessions.json` | 2 | `..` | **"first component is neither `.` nor `..`"** — the count check passes |

Both clauses must be present in the shared assertion. Verify by deleting each clause in turn: with the production fix reverted, clause-1-only must go red on `.` only, clause-2-only red on `..` only.

### Shared assertion helper

Define one helper in `cmd/pyry/args_test.go` beside `TestSanitizeName` (same package `main`, so `pair_test.go` uses it too — do not write a second copy):

- Signature shape: `assertInsideInstanceDir(t *testing.T, home, got, wantFile string)`, with `t.Helper()`.
- Computes `filepath.Rel(filepath.Join(home, ".pyry"), got)`, fails on error.
- Splits on `filepath.Separator` and asserts **exactly two** components.
- Asserts component 0 is neither `"."` nor `".."`.
- Asserts component 1 equals `wantFile`.
- Failure messages name the input path and the computed `rel` — the existing tests' messages are a good model.

### Test scenarios

- **`TestSanitizeName`** — add exactly two rows: `"."` → `"._"`, `".."` → `".._"`. Every existing row keeps its current `want` value untouched (AC 4). No row currently is `"."` or `".."`, so nothing else moves.
- **`TestResolveRegistryPath` (new)** — the resolver has no containment test at all today. Non-parallel top-level test, `home := t.TempDir()` + `t.Setenv("HOME", home)`, mirroring `TestResolveConversationsRegistryPath`. Cases: happy path `"test"` asserted by exact equality against `filepath.Join(home, ".pyry", "test", "sessions.json")`; then `"."`, `".."` and `"../etc"` through `assertInsideInstanceDir` with `wantFile` `"sessions.json"`.
- **`TestResolveConversationsRegistryPath`, `TestResolveDevicesPath`, `TestResolveServerIDPath`** — extend, don't duplicate. Keep the happy-path exact-equality assertion; replace the hand-rolled `rel == ".."` containment check with `assertInsideInstanceDir`, and run it over `"."`, `".."` and the existing `"../etc"`.
- **`TestResolveSocketPath` — AC 3 pin, as a new non-parallel sibling test**, e.g. `TestResolveSocketPath_DotNamesStayInPyryDir`. For `name` in `{".", ".."}`: assert `filepath.Dir(resolveSocketPath("", name))` equals `filepath.Join(home, ".pyry")` exactly.

  **Why a sibling and not a subtest**, despite the ticket saying "extend": `TestResolveSocketPath` and all four of its subtests call `t.Parallel()`, and `t.Setenv` panics in a parallel test. A non-parallel subtest under a parallel parent would avoid the panic but mutate `$HOME` *during* the parallel phase, racing every other parallel test that reads it. The four registry-style resolver tests are already non-parallel top-level tests for exactly this reason; the new pin joins them. It is a new pin for a property nothing covers, not a duplicate of an existing one.

### Gate

`make check` covers all of it (`cmd/pyry` unit tests are in the race-enabled tier, and `cite-guard` runs there too). No e2e tier is involved — no daemon, no live claude, no relay.

## Doc correction (AC 5)

`docs/knowledge/features/keys-package.md` § "Deliberately NOT shared with `cmd/pyry/main.go:sanitizeName`" currently justifies the separation with `sanitizeName("..") == ".."`. This ticket falsifies that example and nothing else in the section.

Constraints on the replacement, rather than prescribed wording:

- The surrounding rationale is preserved. The two surfaces still stay separate.
- The new example must be an input where the two helpers still genuinely disagree **after** this fix. `sanitizeName("../etc") == ".._etc"` and `sanitizeName("MyBox") == "MyBox"` both qualify — verified above; `validDaemonName` rejects both.
- It must not restate a traversal claim this ticket has closed. The honest post-fix framing is a **posture** difference, not a traversal one: `sanitizeName` transforms, so it always yields a usable name; `keys.LoadOrCreate` must *reject* and perform zero filesystem operations on reject (pinned by `TestLoadOrCreate_InvalidDaemonName`). A transformer can never satisfy a zero-write reject contract, whatever its allowlist.

Nothing else in that file changes.

## Out of scope

- **The write-before-reject ordering in `runPair`.** `identity.LoadOrCreate` writes `server-id` before `keys.LoadOrCreate` rejects the name. After this fix the write lands in a correctly contained instance directory, so this ticket removes the *escape* but not the *ordering*. Worth its own ticket; folding it in would pull in `runPair`'s error path.
- **Orphaned state from a pre-fix `.` / `..` run** — see § Design. No migration.
- **`docs/knowledge/INDEX.md` carries the same falsified `sanitizeName("..") == ".."` claim** in its `keys-package.md` row. `INDEX.md` is the documentation phase's file and no other agent writes it — flagged here so the documentation phase corrects that row when it writes `docs/knowledge/codebase/1508.md`. **Not a developer deliverable.**
- **Name length.** `sanitizeName` imposes no cap, so a 10 KB `PYRY_NAME` produces a `MkdirAll` failure at startup rather than a traversal. Not a new condition and not this ticket.

## Open questions

None blocking. One judgment call is recorded rather than asked: the sibling-vs-subtest placement of the AC 3 socket pin (§ Testing strategy) departs from the ticket's "extend `TestResolveSocketPath`" phrasing for a concrete `t.Setenv`/`t.Parallel` reason. If a reviewer prefers the subtest form, the cost is dropping `$HOME` isolation from that assertion.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** CLOSED BY THIS DESIGN (no residual MUST FIX). This ticket **is** the trust boundary: `PYRY_NAME` / `-pyry-name` is operator-supplied and untrusted; `sanitizeName` is the single, named place it becomes a trusted path component. Before this change the boundary leaked, because "no separator survives" was mistaken for "the result is a safe component" — `.` and `..` carry traversal semantics without carrying a separator. The design states the postcondition explicitly (§ Design → Invariant) and puts it in the function's doc comment, so the boundary is legible to the next reader rather than implied by the allowlist. Downstream callers hold a plain `string` with no type-level signal; that is accepted for a five-call-site, one-package surface, and the alternative (a `type instanceDir string`) is a larger refactor than the bug warrants.
- **[File operations]** CLOSED BY THIS DESIGN (no residual MUST FIX) — this is the ticket's own finding. Path traversal: four resolvers concatenate operator input into a filesystem path, and the boundary check was insufficient for two specific values. Fixed at the single choke point. Explicitly re-checked for a **second-order** hole: no other output of `sanitizeName` has traversal meaning to `filepath.Join` — `...` and longer dot runs are ordinary directory names, a trailing separator is impossible (separators become `_`), and empty is already mapped to `_`. No TOCTOU is introduced: `sanitizeName` performs zero filesystem operations, and the change adds no stat-then-open pair. File modes are unchanged and remain the owning packages' responsibility (`0700` dir / `0600` file, per `internal/keys` and the atomic-write convention in `docs/PROJECT-MEMORY.md`).
- **[Tokens, secrets, credentials]** No new findings, but naming the exposure this fix closes: with `PYRY_NAME=..`, `devices.json` (device token *hashes*) and `server-id` (daemon identity) were written directly into `$HOME` — a directory with far broader read exposure than `~/.pyry` and one that routinely gets backed up, synced, and shared. Plaintext pairing tokens never touch disk (`ADR 021` ordering; only hashes are persisted), so the leak was of identity and pairing metadata, not of a usable credential. **OUT OF SCOPE:** files already written to `$HOME` by a pre-fix run are left in place — deleting operator files is destructive and unrecoverable, and no evidence exists that any deployment used these names. Recorded in § Design so the residual is a decision, not an omission.
- **[Subprocess / external command execution]** N/A by construction, verified rather than assumed: the only production `exec.Command*` call sites in `cmd/pyry` are in `update.go` (the updater's own argv) and `agent_run_selfcheck.go` (`claudeBin --version`); neither takes an instance-name-derived value. The one argv-shaped hazard in this family — a leading `-` — is pre-existing in `sanitizeName`'s allowlist and unchanged here, and neither new output (`._`, `.._`) begins with `-`.
- **[Cryptographic primitives]** N/A — no randomness, no key material, no comparison of attacker-controlled input against a secret. The static-key path is reached only *after* `validDaemonName`, whose accept/reject decision this fix provably does not alter (both new outputs still contain `.`, still rejected).
- **[Error messages, logs, telemetry]** N/A — the change adds no log call, no error, and no message. The instance name already appears in `keys`' reject message (`invalid daemon name "…"`), which is operator-supplied data echoed to the operator's own terminal; unchanged.
- **[Network & I/O]** N/A — no socket, no reader, no size limit, no timeout in scope. `resolveSocketPath`'s *path* is touched only by an added assertion; the listener and its accept loop are untouched.
- **[Concurrency]** No findings in production code (pure function, no shared state). One *test*-side concurrency hazard was found and designed out: placing the AC 3 socket pin as a subtest of the parallel `TestResolveSocketPath` would either panic (`t.Setenv` in a parallel test) or mutate `$HOME` concurrently with every other parallel test in the package. § Testing strategy prescribes a non-parallel sibling and says why.
- **[Threat model alignment]** The relevant threat is local and pre-authentication: an operator-supplied environment variable steering daemon state, including pairing material, outside its intended directory. Sources are a shell alias, an inherited environment, or a launchd/systemd unit — not a remote attacker, so this is hardening against foot-gun and environment-poisoning rather than against the mobile protocol's threat model, none of whose entries this touches. `docs/threat-model.md` does not exist in this repo; `docs/protocol-mobile.md` § Security model is relay/transport-scoped and unaffected.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
