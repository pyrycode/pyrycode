# Architecture spec — `pyry pair list` (ticket #214)

## Files to read first

- `cmd/pyry/pair.go` (entire file, ~165 lines) — current `runPair` shape, helpers (`resolveDevicesPath`, `parsePairArgs`, exit-2 conventions), and the `os.Exit(2)` pattern for usage failures.
- `cmd/pyry/main.go:155-177` — top-level verb switch; the dispatcher entry for `pair` is unchanged.
- `cmd/pyry/main.go:629-660` — `runSessions` shape (the verb dispatcher to mirror): `parseClientFlags` + first-positional switch + per-verb `runSessions<Verb>` helpers + `errSessionsUsage` for unknown verbs.
- `cmd/pyry/main.go:616-627` — `sessionsVerbList` constant + `errSessionsUsage` formatter; reuse the same shape for a per-pair verb list.
- `cmd/pyry/main.go:1192-1214` — usage block; the `pyry pair` line needs a sub-verb addendum.
- `internal/devices/registry.go` (entire file) — `Load` returns `*Registry, nil` on missing/zero-byte file (cold start is not an error); `List()` returns a copy of the device slice; on-disk sort key is `(PairedAt, Name)` ascending.
- `internal/devices/device.go` — `Device` fields (`TokenHash`, `Name`, `PairedAt`, `LastSeenAt`); `TokenHash` is 64 lowercase hex chars; plain token is never on disk.
- `cmd/pyry/pair_test.go` (entire file) — table-driven flag-parse tests + `t.Setenv("HOME", …)` pattern for resolver tests; reuse the same shape.
- `internal/e2e/pair_test.go` (entire file) — `RunBareIn(t, home, "pair", …)` pattern + the `~/.pyry/pyry/devices.json` path convention used by the default instance name.
- `internal/e2e/harness.go` — `RunBareIn` signature (already exists from #213; do not modify).

## Context

Ticket #213 shipped `pyry pair` as a one-shot CLI verb whose `runPair` is a single bare action: parse flags → load registry → mint token → write QR. Phase 3 has two sibling read/write verbs queued — `pair list` (#214, this ticket) and `pair revoke` (#215) — so `pyry pair` must become a verb family. This spec restructures `runPair` into a verb dispatcher and adds the `list` sub-verb. The bare invocation (`pyry pair` with no positional) keeps its existing behavior.

The work is read-only on top of `internal/devices`: `devices.Load(path)` then `Registry.List()`, then a pure formatter that writes to `io.Writer`. No daemon interaction. No new exported types. No new packages.

## Design

### Dispatcher shape

`runPair(args []string)` peels the first positional. If absent, it dispatches to the existing bare-pair body (extracted into `runPairDefault`). If present and equal to `list`, it dispatches to `runPairList`. Anything else is a usage error (exit 2) listing the implemented verbs.

Mirrors `runSessions` exactly, with one deviation: `runPair` does NOT call `parseClientFlags`. The `pair` family does not dial the daemon, so there is no `-pyry-socket` to peel. Instance-name resolution stays inside the per-verb flag parser (`-pyry-name` is already an explicit `flag.String` in `parsePairArgs` — keep that pattern in `parsePairListArgs`).

Pseudocode (final shape — exact code is the developer's call):

```go
const pairVerbList = "list"

func errPairUsage(detail string) error {
    return fmt.Errorf("pair: %s\nverbs: %s (or omit for the default pair flow)", detail, pairVerbList)
}

func runPair(args []string) error {
    if len(args) > 0 {
        switch args[0] {
        case "list":
            return runPairList(args[1:])
        }
        // Fall through to runPairDefault for the bare invocation. Anything
        // that *looks* like a verb (non-flag first positional) but isn't
        // implemented is a usage error; flags (starts with "-") fall through
        // to runPairDefault so existing `pyry pair --name=foo` still works.
        if !strings.HasPrefix(args[0], "-") {
            fmt.Fprintln(os.Stderr, "pyry pair:", "unknown verb", strconv.Quote(args[0]))
            fmt.Fprintln(os.Stderr, "verbs:", pairVerbList, "(or omit for the default pair flow)")
            os.Exit(2)
        }
    }
    return runPairDefault(args)
}
```

`runPairDefault` is the rename of today's `runPair` body verbatim — no logic change. The signature stays `func(args []string) error`.

**Why the `strings.HasPrefix(..., "-")` check.** `pyry pair --name=foo` has `--name=foo` as `args[0]`. We must not treat that as an unknown verb. Real verbs are bare identifiers (no leading `-`). This is the same disambiguation the top-level CLI already does for `pyry [pyry-flags] -- claude-args` — only positionals beginning with a non-dash character can be Pyrycode verbs.

### `runPairList`

```go
func runPairList(args []string) error {
    parsed, err := parsePairListArgs(args)
    if err != nil {
        fmt.Fprintln(os.Stderr, "pyry pair list:", err)
        fmt.Fprintln(os.Stderr, "usage: pyry pair list [-pyry-name=<instance>]")
        os.Exit(2)
    }
    devicesPath := resolveDevicesPath(parsed.instanceName)
    registry, err := devices.Load(devicesPath)
    if err != nil {
        return fmt.Errorf("pair list: %w", err)
    }
    return renderPairList(registry.List(), os.Stdout)
}
```

`parsePairListArgs` is the analogue of `parsePairArgs`: only `-pyry-name` is accepted (no `--name`, no `--relay`). Unknown flags or any positional → error.

### Formatter

The formatter is pure: `func renderPairList(list []devices.Device, w io.Writer) error`. Pure means no globals, no `os.Stdout` access, no clock reads — the entire output is a deterministic function of `list`. This is what makes the formatter unit-testable byte-for-byte.

```go
// renderPairList writes one of two layouts:
//
//   • empty list  → "No paired devices.\n"
//   • non-empty   → header row + data rows, padded by text/tabwriter,
//                   sorted by (PairedAt, Name) ascending.
//
// All writes go through w; the function never touches os.Stdout. Returns
// w's error verbatim if any Fprintf or tabwriter Flush fails (caller
// wraps with "pair list: ...").
func renderPairList(list []devices.Device, w io.Writer) error {
    if len(list) == 0 {
        _, err := io.WriteString(w, "No paired devices.\n")
        return err
    }
    sorted := append([]devices.Device(nil), list...)
    sort.SliceStable(sorted, func(i, j int) bool {
        if !sorted[i].PairedAt.Equal(sorted[j].PairedAt) {
            return sorted[i].PairedAt.Before(sorted[j].PairedAt)
        }
        return sorted[i].Name < sorted[j].Name
    })
    tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
    fmt.Fprintln(tw, "NAME\tPAIRED\tLAST SEEN\tTOKEN-PREFIX")
    for _, d := range sorted {
        lastSeen := "never"
        if !d.LastSeenAt.IsZero() {
            lastSeen = d.LastSeenAt.Format(time.RFC3339)
        }
        prefix := ""
        if len(d.TokenHash) >= 8 {
            prefix = d.TokenHash[:8]
        } else {
            prefix = d.TokenHash
        }
        fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
            d.Name, d.PairedAt.Format(time.RFC3339), lastSeen, prefix)
    }
    return tw.Flush()
}
```

Three notes on the formatter:

1. **Sort defensively, even though `Save` already sorts on disk.** The AC names "matches on-disk sort order produced by `Save`," but the formatter's input is `Registry.List()`, which returns the in-memory slice. In the read-only path that slice is whatever `Load` decoded. If a future writer skips `Save`'s sort step (or the file was hand-edited), the formatter still produces the documented order. Cost: one `sort.SliceStable` on a tiny slice; benefit: the AC's determinism guarantee is local to the formatter, not coupled to a sibling package's behavior.
2. **`text/tabwriter` for column padding.** Stdlib only; matches CLAUDE.md's "Stdlib over dependencies" principle. `tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)` gives 2-space padding between columns and no minimum width. Header and data go through the same writer so columns align.
3. **Defensive token-prefix length check.** `Device.TokenHash` is documented as 64 hex chars, but the formatter is a pure UI function and shouldn't panic on malformed input. The `len(...) >= 8` guard is a one-line check; the alternative (assume valid) is a `runtime error: slice bounds out of range` on a corrupt registry, which is the wrong failure mode for a list command.

### Empty-registry path

`devices.Load` already collapses three cases — file missing, file zero-byte, parsed file with empty `devices` array — into "registry with empty `devices` slice, no error." `Registry.List()` returns an empty slice. The formatter's `len(list) == 0` branch handles all three uniformly.

The AC requires "exactly `No paired devices.\n`" — the formatter writes exactly that string and nothing else (no header, no trailing newline beyond the one in the literal). Test fixture: empty registry → assert `bytes.Equal(buf.Bytes(), []byte("No paired devices.\n"))`.

## Data flow

```
argv ──> runPair ──> runPairList ──> parsePairListArgs ──> instanceName
                          │
                          ├──> resolveDevicesPath(instanceName) ──> path
                          ├──> devices.Load(path)                ──> *Registry (or err)
                          ├──> Registry.List()                   ──> []Device
                          └──> renderPairList(list, os.Stdout)   ──> bytes / err
```

Single goroutine. No channels. No context. No daemon. No filesystem writes.

## Concurrency model

None. Read-only one-shot CLI verb. The registry's mutex is used internally by `Load`/`List` but the verb runs to completion before main returns, so there is no concurrent access to consider.

## Error handling

| Failure | Path | Exit | Stderr |
|---|---|---|---|
| Unknown sub-verb (e.g. `pyry pair revoke` before #215) | `runPair` switch fall-through | 2 (direct `os.Exit`) | `pyry pair: unknown verb "revoke"` + verb list |
| Flag parse error (`pyry pair list --bogus`) | `parsePairListArgs` | 2 (direct `os.Exit`) | `pyry pair list: <err>` + usage line |
| Unexpected positional (`pyry pair list extra`) | `parsePairListArgs` | 2 (direct `os.Exit`) | `pyry pair list: unexpected positional "extra"` + usage line |
| Registry I/O error (permission denied) | `devices.Load` returns err | 1 (returned from `runPair`) | `pyry: pair list: registry: read <path>: permission denied` |
| Malformed JSON | `devices.Load` returns err | 1 (returned from `runPair`) | `pyry: pair list: registry: parse <path>: <json err>` |
| Stdout write error (broken pipe) | `tabwriter.Flush` or `io.WriteString` | 1 (returned from `runPair`) | `pyry: pair list: <write err>` |
| Empty registry | `Registry.List()` returns `[]` | 0 | (none; stdout: `No paired devices.\n`) |

The exit-2 path uses `os.Exit(2)` directly (mirrors `runPair`'s existing flag-parse branch) so `main.go`'s top-level `pyry: ` prefix doesn't appear on usage failures. The exit-1 path returns a wrapped error; `main.go` handles the `pyry: ` prefix and the `os.Exit(1)`.

The registry path is included in the error chain because `devices.Load` already wraps with `fmt.Errorf("registry: read %s: %w", path, err)`. The architect's wrap (`fmt.Errorf("pair list: %w", err)`) preserves this. The AC's "including the registry path" requirement is satisfied transitively.

## Testing strategy

### Unit tests in `cmd/pyry/pair_test.go`

1. `TestRenderPairList_TwoDevices` — fixture with two devices (different `PairedAt`, both with non-zero `LastSeenAt`), assert exact stdout bytes match a golden string. The golden string includes the header, both rows in `(PairedAt, Name)` order, and `tabwriter`'s column padding. Build the golden by running the formatter once and capturing the output at code-write time, then committing it as a string literal.
2. `TestRenderPairList_NeverSeen` — single device with `LastSeenAt` zero-valued; assert the `LAST SEEN` cell is the literal `never` (not `0001-01-01T00:00:00Z`).
3. `TestRenderPairList_Empty` — empty slice; assert `bytes.Equal(buf.Bytes(), []byte("No paired devices.\n"))`.
4. `TestRenderPairList_SortOrder` — input slice with rows in reverse order (later `PairedAt` first); assert output rows come out in ascending `(PairedAt, Name)` order. This test guards the defensive sort independently of how `Load` happened to return the slice.
5. `TestParsePairListArgs` — table-driven, mirroring `TestParsePairArgs`: empty args (defaults), `-pyry-name=foo` (custom instance), `--bogus` (error), `extra-positional` (error).

The formatter tests use `bytes.Buffer` as `io.Writer` — no `t.TempDir`, no env, no filesystem.

### E2E test in `internal/e2e/pair_test.go`

6. `TestPairList_E2E` — pin `HOME` to `t.TempDir`, run `pyry pair --name=phone-a`, then run `pyry pair list`, assert exit 0 + `phone-a` and the 8-char token-hash prefix appear in stdout. Use the existing `RunBareIn` helper. One sub-test for the empty case (run `pyry pair list` with no prior pair) that asserts stdout is exactly `No paired devices.\n` and exit 0.

Tests stay stdlib-only (`testing`, `bytes`, `strings`, no testify).

### What is NOT tested

- `tabwriter`'s exact padding heuristic — covered transitively by the golden-string test in (1). If tabwriter's behavior changes across Go versions, the golden string updates with it; that's the right granularity.
- `devices.Load`'s cold-start path — already covered in `internal/devices/registry_test.go`. The list command's empty-registry behavior is tested at the formatter level (3) and end-to-end (6); duplicating the load-time tests here would be redundant.

## Open questions

1. **Verb-dispatch helper sharing with #215.** The technical notes ask whether the dispatcher should be factored for `pair revoke` to reuse. Recommendation: do NOT factor in this ticket. The dispatcher is ~10 lines (one switch + one usage helper). #215 will add one switch case and one helper function — the same diff size as it would be against a "factored" base. Premature abstraction (CLAUDE.md "Simplicity first"). #215 inherits a working two-verb dispatcher and adds the third case in one line.
2. **`pairVerbList` const.** Defined as `"list"` here. When #215 lands it becomes `"list, revoke"`. This is the same lockstep-update pattern documented in the `sessionsVerbList` comment block (`cmd/pyry/main.go:616-620`). No further coordination needed.
3. **Top-level help text.** `main.go:1204-1207` has the `pyry pair` usage block. Add a one-line addendum under it: `pyry pair list [flags]                         list paired devices`. The developer should append it at write time; it's a 1-line edit, not worth factoring.

## Files modified

| File | Change | Approx LoC |
|---|---|---|
| `cmd/pyry/pair.go` | Add `runPair` dispatcher, rename old `runPair` body to `runPairDefault`, add `runPairList`, `parsePairListArgs`, `renderPairList`, `pairVerbList` const | +75 prod |
| `cmd/pyry/main.go` | One usage line under the `pyry pair` block | +2 prod |
| `cmd/pyry/pair_test.go` | Add tests 1–5 above | +120 test |
| `internal/e2e/pair_test.go` | Add `TestPairList_E2E` (test 6) | +50 test |

Total production code: ~77 LoC across 2 files. No new files. No new exported types. No new packages. No new dependencies.
