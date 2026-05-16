# Spec — cmd/pyry: `pyry pair preflight` empty-registry gate for v2 cutover (#436)

## Files to read first

The developer's turn-1 reading list. Lift these into context before writing any code.

- `cmd/pyry/pair.go:106-129` — `runPair` dispatch. New `case "preflight":` arm goes alongside `list` / `revoke`. Also update `pairVerbList` (line 24) to `"list, revoke, preflight"`.
- `cmd/pyry/pair.go:203-249` — `pairListArgs` / `parsePairListArgs` / `runPairList`. The preflight verb's parser is structurally identical (only `-pyry-name` accepted, no positionals); clone the shape with rename, do not extract a shared parser. The `devices.Load` + `registry.List()` read path used by `runPairList` is the same read path this ticket reuses verbatim (Technical Note in the issue body pins this).
- `cmd/pyry/pair.go:319-348` — `runPairRevoke`. **Two patterns to mirror exactly:**
  (a) `os.Exit(1)` direct call at line 341 for the not-found case — bypasses main's `pyry: ` prefix so the stderr line reads `pyry pair revoke: …` not `pyry: pair revoke: …`. The preflight verb uses the same trick for its exit-2 gate-fail message.
  (b) `fmt.Errorf("pair revoke: %w", err)` for I/O errors at lines 337 and 344 — returns up to `main.run`, which prefixes with `pyry: ` and `os.Exit(1)`s. This is the corrupt-registry path (exit 1).
- `cmd/pyry/pair_test.go:261-299` — `TestParsePairListArgs`. Clone-and-rename for the preflight parser. Same four cases (empty, instance, positional rejected, unknown flag rejected).
- `cmd/pyry/pair_test.go:349-453` — `TestRunPairRevoke_RemovesEntry` and `TestRunPairRevoke_SaveFailure`. The harness pattern (`t.Setenv("HOME", …)`, `resolveDevicesPath(defaultName())`, write a registry via the public `devices` API, then call the run-function under test) is the template for the preflight-success-path and preflight-corrupt-registry tests.
- `cmd/pyry/main.go:156-194` — `main` + `run`. Pins the contract: `run` returns nil for success, returns an error which `main` prints as `pyry: <err>` then `os.Exit(1)`. Confirms why direct `os.Exit(N)` calls in the runX functions are the right escape hatch for any non-1 exit code or any message that should not be prefixed by `pyry: `.
- `internal/devices/registry.go:37-53` — `devices.Load` contract: missing file → empty `*Registry`, nil error (cold start). Zero-byte file → empty `*Registry`, nil error. Malformed JSON → wrapped error, nil `*Registry`. The preflight verb depends on this contract: missing/empty file → exit 0, malformed JSON → exit 1.
- `internal/devices/registry.go:131-139` — `Registry.List()` returns a copied slice. `len(registry.List())` is the count we gate on.
- `docs/protocol-mobile.md:561-565` — § *Pre-flight: `pyry pair list` empty check*. The paragraph the developer updates per AC #3: replace the bare prose "run `pyry pair list` and confirm it is empty" with the concrete verb invocation `pyry pair preflight` and document the exit-code contract (0 / 1 / 2) so release tooling can copy it verbatim.
- `docs/protocol-mobile.md:9` — § *Version negotiation* opening paragraph. Already cross-references #436 and the *Pre-flight* anchor — leave intact, do not edit. (Pinned by issue body AC #3 parenthetical.)

## Context

Mobile Protocol v2 (#430) ships as a hard cutover. v1 pair records lack `server_static_pubkey` (the field added in #432); a v2 binary cannot complete the Noise_IK handshake with a v1-paired phone — the connection closes with WS code `4426` (handshake failure) and the user has no recovery path other than `pyry pair revoke && pyry pair` per device.

The release-engineering remedy is a pre-flight check at the binary side: a non-zero CLI exit when any paired device exists, wired into the release workflow ahead of the v2 release-flag flip. This ticket lands the **CLI side**. The v2 release-flag mechanism itself does not yet exist (Technical Note in the issue body); AC #4 is satisfied by documenting the intended invocation in the protocol spec — the exit-code behaviour is the load-bearing part.

The existing `pyry pair list` verb has a stable human-facing tabular output that out-of-tree scripts may already parse (Technical Note: out-of-tree consumers of `pair list` must not break). Therefore the gate is opt-in — a strictly additive new surface, not a behavioural change to `pair list`.

## Design

### Shape: dedicated verb, not a flag on `pair list`

The issue body leaves shape to architect: `pyry pair list --quiet --fail-if-any` versus a dedicated `pyry pair preflight` verb. **Choose the dedicated verb.** Three reasons:

1. **Output divergence.** `pair list` writes a table to stdout. The preflight gate writes nothing to stdout on either branch. Smuggling completely different output behaviour through a flag combo on the same verb is more surprising than a clearly-named sibling verb.
2. **CI legibility.** Release tooling will literally contain the string `pyry pair preflight`. A reader who has never seen this codebase understands what it does. `pyry pair list --quiet --fail-if-any` requires reading `pair.go` to disambiguate (`--quiet` could plausibly mean "suppress the human header but emit data lines").
3. **No ambiguous flag interactions.** A `--fail-if-any` flag interacts with the existing `-pyry-name` and would invite future flags (`--max-count`, `--exclude-name`, …); a dedicated verb pins the surface at exactly its current behaviour and pushes future variants onto their own verb if they ever materialise.

The verb list constant `pairVerbList` in `cmd/pyry/pair.go:24` updates to `"list, revoke, preflight"` in lockstep — the doc-comment on line 22-23 already names the convention.

### Verb contract

```
pyry pair preflight [-pyry-name=<instance>]
```

- **Flags:** only `-pyry-name` (mirrors `pair list`). No positionals.
- **Stdout:** silent on every branch. (Allows callers to redirect stdout to `/dev/null` without losing the gate-failure message, which goes to stderr.)
- **Stderr:** silent on exit 0. On exit 2, exactly one line, format frozen:

  ```
  pyry pair preflight: <N> paired device(s); v2 release gate requires zero.
  ```

  where `<N>` is the integer count (`%d`, no padding). The `device(s)` form is deliberate — singular/plural pluralisation in CI output is not worth the branch.

  On exit 1, the standard wrapped-error path: `pyry: pair preflight: <wrapped err>` from main's printer.
- **Exit codes:**
  - `0` — registry empty (gate passes; no paired devices).
  - `1` — registry I/O error or malformed JSON. Standard `pyry: pair preflight: …` wrapped error via `main.run`'s existing handler. **Distinct from the gate-failure code so CI can distinguish "the check itself failed" from "there are devices and you should not flip the flag".**
  - `2` — registry non-empty (gate fails; one or more paired devices).

### Code surface (single file: `cmd/pyry/pair.go`)

Three additions, no other file edits except the protocol-mobile.md doc update.

#### 1. Add to dispatch (`runPair`)

In the switch at `cmd/pyry/pair.go:115-121`, add a new arm:

```go
case "preflight":
    return runPairPreflight(args[1:])
```

Update `pairVerbList` (line 24) to `"list, revoke, preflight"`. The doc-comment above already pins the lockstep rule.

#### 2. Parser (`pairPreflightArgs` + `parsePairPreflightArgs`)

Same shape as `pairListArgs` / `parsePairListArgs`. The `FlagSet` name string becomes `"pyry pair preflight"`. Only `-pyry-name` is accepted; no positionals.

The args struct holds one field (`instanceName string`). The doc-comment notes:

> `parsePairPreflightArgs` parses the flag set for `pyry pair preflight`. Only `-pyry-name` is accepted. Unknown flags or unexpected positionals produce errors propagated to the caller; `runPairPreflight` maps these to exit 2.

#### 3. Runner (`runPairPreflight`)

Pseudo-shape, names final:

```go
func runPairPreflight(args []string) error {
    parsed, err := parsePairPreflightArgs(args)
    if err != nil {
        // usage failure → exit 2, bypass `pyry: ` prefix
        fmt.Fprintln(os.Stderr, "pyry pair preflight:", err)
        fmt.Fprintln(os.Stderr, "usage: pyry pair preflight [-pyry-name=<instance>]")
        os.Exit(2)
    }
    devicesPath := resolveDevicesPath(parsed.instanceName)
    registry, err := devices.Load(devicesPath)
    if err != nil {
        // I/O / malformed JSON → return wrapped, becomes exit 1
        return fmt.Errorf("pair preflight: %w", err)
    }
    count := len(registry.List())
    if exitCode, line := preflightVerdict(count); exitCode != 0 {
        fmt.Fprintln(os.Stderr, line)
        os.Exit(exitCode)
    }
    return nil
}
```

The doc-comment on `runPairPreflight` mirrors `runPairRevoke`'s contract paragraph: returns nil on the gate-pass path (exit 0), returns wrapped error for I/O failures (exit 1), calls `os.Exit(2)` directly for the gate-fail and usage-failure paths.

#### 4. Pure verdict helper (`preflightVerdict`)

Single-purpose pure function — extracted so the exit-2 message contract is unit-testable byte-for-byte without subprocess re-exec:

```go
// preflightVerdict returns (exitCode, stderrLine) for the v2 release gate.
// exitCode == 0 means the gate passed (count == 0); stderrLine is "".
// exitCode == 2 means the gate failed (count > 0); stderrLine is the exact
// message to emit before os.Exit(2). Pure: deterministic on count.
func preflightVerdict(count int) (exitCode int, stderrLine string)
```

The format string lives inside `preflightVerdict` so the test's `want` string is the canonical contract.

### Why we don't refactor `runPairList`

The `devices.Load` + `registry.List()` two-line preamble of `runPairPreflight` is identical to `runPairList`'s preamble. **Do not extract a shared helper.** The duplication is two lines; a shared helper would either (a) take a function parameter for the post-load action and lose readability, or (b) leak the loaded `*Registry` and split the lifecycle. Per [Pyrycode-wide simplicity-first](../../../CLAUDE.md) and the project's per-registry duplication policy in `docs/PROJECT-MEMORY.md` § *Atomic-write recipe*, "duplicated until a Nth instance forces extraction" — this is the first sibling reader; not enough.

## Concurrency model

Single-process synchronous CLI invocation. No goroutines, no context plumbing, no shutdown sequence beyond `os.Exit`. The verb is a one-shot read of `devices.json` followed by an integer comparison and an exit-code dispatch.

The on-disk file is read once; concurrent writers (a `pyry pair` or `pyry pair revoke` running simultaneously on the same machine) are out of scope — the existing `pair list` verb has the same property and the v2 release workflow runs in a controlled environment.

## Error handling

| Failure mode | Detection point | Exit code | Stream / message |
|---|---|---|---|
| Unknown flag, unexpected positional | `parsePairPreflightArgs` returns error | 2 | stderr `pyry pair preflight: <err>` + usage line, direct `os.Exit(2)` |
| Missing `devices.json` | `devices.Load` returns empty registry, nil error (cold-start contract; pinned at `internal/devices/registry.go:40-42`) | 0 | silent |
| Zero-byte `devices.json` | Same — `devices.Load` returns empty registry, nil error | 0 | silent |
| Malformed JSON | `devices.Load` returns wrapped error | 1 | `runPairPreflight` returns `fmt.Errorf("pair preflight: %w", err)`; main prefixes with `pyry: ` and `os.Exit(1)` |
| Permission denied on read | `devices.Load` returns wrapped error | 1 | Same wrapped-error path |
| Registry non-empty (≥1 device) | `len(registry.List()) > 0` | 2 | stderr exact-format line via `preflightVerdict`, direct `os.Exit(2)` |

**No new error sentinels.** All error shapes are existing `fmt.Errorf` wraps of `devices.Load` failures. The exit-2 gate failure is not surfaced as a Go error — it is a CLI-level policy outcome printed directly to stderr and exited.

**Why exit 1 vs 2 distinction matters.** The release workflow needs to distinguish "the check itself failed" (exit 1 — retry, investigate the binary's state) from "the gate caught what it was supposed to catch" (exit 2 — devices exist, do not flip the flag, operator must `pyry pair revoke` first). Same convention as standard Unix utilities (`grep` uses 0/1/2 in the same pattern).

## Testing strategy

All tests live in `cmd/pyry/pair_test.go` alongside the existing pair tests. Same-package (white-box), stdlib `testing` only, table-driven where multiple cases share shape.

### Required test cases

1. **`TestParsePairPreflightArgs`** — clone of `TestParsePairListArgs`. Cases:
   - empty args → instance == `defaultName()`
   - `-pyry-name=foo` → instance == `foo`
   - unexpected positional → error containing `"unexpected positional"`
   - unknown flag → error containing `"flag provided but not defined"`

2. **`TestPreflightVerdict`** — table-driven, pure-function exit-code matrix:
   - `count=0` → `(0, "")`
   - `count=1` → `(2, "pyry pair preflight: 1 paired device(s); v2 release gate requires zero.")`
   - `count=2` → `(2, "pyry pair preflight: 2 paired device(s); v2 release gate requires zero.")`
   - `count=10` → `(2, "pyry pair preflight: 10 paired device(s); v2 release gate requires zero.")`

   The exact stderr-line strings are part of the AC #1 contract and are pinned byte-for-byte here.

3. **`TestRunPairPreflight_EmptyRegistry`** — sets up `HOME`, calls `runPairPreflight(nil)` against a path with no `devices.json` on disk, asserts `err == nil` (gate-pass path; cold-start is the empty-registry case).

4. **`TestRunPairPreflight_ZeroByteRegistry`** — sets up `HOME`, writes an empty (zero-byte) `devices.json`, calls `runPairPreflight(nil)`, asserts `err == nil`. Mirrors the `devices.Load` contract for zero-byte files (`internal/devices/registry.go:45-47`).

5. **`TestRunPairPreflight_CorruptRegistry`** — sets up `HOME`, writes `[}` (malformed JSON) to `devices.json`, calls `runPairPreflight(nil)`, asserts the returned error is non-nil and contains the prefix `"pair preflight:"`. Mirrors `TestRunPairRevoke_SaveFailure`'s assertion shape.

### Path NOT directly tested

The exit-2 non-empty-registry path (`os.Exit(2)` from inside `runPairPreflight`) is not driven through the `runPairPreflight` function in unit tests, because `os.Exit` terminates the test binary. The contract for that path is fully covered by:

- `TestPreflightVerdict` (count → exit code + stderr line, byte-for-byte)
- Manual / integration: an operator running `pyry pair preflight` after `pyry pair` against a real home dir.

This split mirrors how `runPairRevoke`'s `os.Exit(1)` not-found path is unit-tested today (it isn't — the policy decision lives in the unconditional code at line 339-342, with no extracted helper to test). The preflight verb is structured cleaner: the policy decision is in `preflightVerdict`, which is fully tested.

If the developer prefers a `TestHelperProcess` subprocess test for `os.Exit(2)`, that is acceptable but not required. The byte-for-byte contract is already covered by `TestPreflightVerdict`; a subprocess test would only re-verify the dispatch (`os.Exit(exitCode)` after `fmt.Fprintln(os.Stderr, line)`), which is two-line trivial wiring.

### Updating existing tests

None of `TestParsePairListArgs`, `TestParsePairRevokeArgs`, `TestRenderPairList_*`, or `TestRunPairRevoke_*` change. The pair-list output contract is preserved verbatim (AC #1: existing default output unchanged).

## Documentation update (`docs/protocol-mobile.md`)

Replace the body of § *Pre-flight: `pyry pair list` empty check* (lines 561-565) with the concrete verb invocation and exit-code contract. The section heading anchor stays the same so the cross-reference at line 9 (`see [Pre-flight](#pre-flight-pyry-pair-list-empty-check) and #436`) still resolves; only the body of the section changes.

The replacement body must:

1. Name the verb: `pyry pair preflight`.
2. Document the three exit codes (0 / 1 / 2) and what each means for release tooling.
3. Show the intended CI shell line (e.g. an `if ! pyry pair preflight; then exit 1; fi` or equivalent `case $?` block) so a release engineer can copy it directly.
4. Note the conditional satisfaction of AC #4 — no release-flag mechanism exists yet, so this section is the load-bearing artefact until one does.

Keep the surrounding paragraphs (rationale: why v1 pair records are not v2-compatible, why there is no migration) intact.

**Final byte form of the replacement is left to the developer**, with the constraint that the four points above are covered. The `pair preflight` verb name, the exit-code triple, and the gate-failure stderr format are pinned by this spec; the prose around them is not.

## Open questions

None. Issue body resolves the conditional AC #4 path (document the invocation; no release flag yet). Shape decision (dedicated verb) is made above with rationale. Exit-code matrix is fixed.

## Size verification

Production code (single file `cmd/pyry/pair.go`):
- `pairPreflightArgs` struct + `parsePairPreflightArgs` ≈ 18 lines
- `preflightVerdict` ≈ 8 lines
- `runPairPreflight` ≈ 18 lines
- `pairVerbList` constant edit + `runPair` switch arm ≈ 3 lines

**Total: ~47 lines of production code in one file. No new exported types. No new packages.**

Test code (single file `cmd/pyry/pair_test.go`):
- `TestParsePairPreflightArgs` ≈ 35 lines
- `TestPreflightVerdict` ≈ 25 lines
- `TestRunPairPreflight_EmptyRegistry` ≈ 12 lines
- `TestRunPairPreflight_ZeroByteRegistry` ≈ 18 lines
- `TestRunPairPreflight_CorruptRegistry` ≈ 20 lines

**Total: ~110 lines of test code, also in one file.**

Files touched: `cmd/pyry/pair.go`, `cmd/pyry/pair_test.go`, `docs/protocol-mobile.md` — 1 production source file, 1 test file, 1 doc file. Production-source count = 1. Well under the ≥5 split threshold. Consumer call sites = 0 (new surface only). Sized **XS** by the PO; architect confirms XS — no override.
