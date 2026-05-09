# Architecture spec — `pyry pair revoke` (ticket #215)

## Files to read first

- `cmd/pyry/pair.go` (entire file, 285 lines) — `runPair` dispatcher (`switch args[0]` with `case "list"`), `pairVerbList` const, `resolveDevicesPath`, sibling parsers (`parsePairListArgs`), and the `os.Exit(2)` direct-exit pattern for usage failures. The new sub-verb is a third leaf appended to this file.
  - Lines 22–25: `pairVerbList` const + the lockstep-update comment that mentions `revoke`.
  - Lines 115–128: `runPair` dispatcher — add one case here.
  - Lines 202–248: `runPairList` + `parsePairListArgs` — the structural template for `runPairRevoke` + `parsePairRevokeArgs` (same `-pyry-name`-only flag set, same exit-2 direct-exit pattern, same `fmt.Errorf("pair %v: %w", ...)` wrap on I/O errors).
- `internal/devices/registry.go` (entire file) — `Load(path)` returns `*Registry, nil` on missing/zero-byte file (cold-start is not an error), `Remove(name)` returns `bool` (true iff a match was removed; byte-exact name compare), `Save(path)` writes atomically (temp + fsync + rename). Note: `Remove` does NOT call `Save` — the caller persists.
- `cmd/pyry/main.go:155-177` — top-level verb switch dispatching `pair` → `runPair`. Unchanged for this ticket; included so the developer can locate the `pair`-line entrypoint without grepping.
- `cmd/pyry/main.go:1182-1245` — `printHelp`'s text block. Append a one-line entry under the existing `pyry pair list` line at line 1208.
- `cmd/pyry/main.go:600-660` — `main.run` error printer + `runSessions` shape. `main.run` prefixes returned errors with `pyry: `; this is why usage failures use `os.Exit(2)` directly and the not-found path uses `os.Exit(1)` directly (otherwise stderr would carry the duplicate `pyry: pyry pair revoke: …` prefix).
- `cmd/pyry/pair_test.go` (entire file, 297 lines) — table-driven flag-parse pattern (`TestParsePairListArgs` at lines 259–297), the `t.Setenv("PYRY_NAME", "")` pattern to neutralize ambient env, and the device-fixture shape used in `TestRenderPairList_*`.
- `internal/e2e/pair_test.go` (entire file, 159 lines) — `RunBareIn(t, home, "pair", …)` E2E pattern, the `~/.pyry/pyry/devices.json` default-instance path, and the post-call registry round-trip via `devices.Load`. `TestPairList_E2E` (lines 94–139) is the structural template for `TestPairRevoke_E2E`.
- `internal/e2e/harness.go` (around the `RunBareIn` declaration) — `RunBareIn(t, home, args...)` returns `Result{Stdout, Stderr, ExitCode}`. No new harness functions are needed.
- `docs/specs/architecture/214-pair-list.md` (sibling spec) — verb-dispatch shape rationale and the "do not factor" decision recorded in #214's open questions §1; this spec inherits those decisions verbatim.

## Context

Ticket #214 turned `runPair` into a sub-verb dispatcher (`runPair` peels `args[0]`, switches to `runPairList` or falls through to `runPairDefault`). This ticket appends a third leaf — the destructive `revoke` sub-verb — under the same dispatcher.

The work is wiring on top of existing primitives:

1. `Registry.Remove(name) bool` already does the byte-exact name lookup and slice splice (no caller-side index search needed).
2. `Registry.Save(path)` already does atomic writes (temp + fsync + rename), so the on-disk file is never partially-written on a `Save` failure.
3. `Registry.Load` already collapses missing-file / zero-byte-file / empty-array into a single empty-list result, so the not-found path needs no special-casing for cold start.

No new exported types. No new packages. No new dependencies. One sub-verb case + one parser + one runner, plus help text + tests.

## Design

### Dispatcher delta

```go
// cmd/pyry/pair.go — runPair switch additions.

const pairVerbList = "list, revoke" // was "list"

func runPair(args []string) error {
    if len(args) > 0 {
        switch args[0] {
        case "list":
            return runPairList(args[1:])
        case "revoke":
            return runPairRevoke(args[1:])
        }
        if !strings.HasPrefix(args[0], "-") {
            fmt.Fprintf(os.Stderr, "pyry pair: unknown verb %q\n", args[0])
            fmt.Fprintln(os.Stderr, "verbs:", pairVerbList, "(or omit for the default pair flow)")
            os.Exit(2)
        }
    }
    return runPairDefault(args)
}
```

Two literal edits: append `, revoke` to `pairVerbList`, add the `case "revoke":` line. Existing `runPairDefault` and `runPairList` stay untouched. The leading-dash escape hatch already routes `pyry pair --flag-form` to `runPairDefault` correctly.

### Parser

```go
// pairRevokeArgs is the parsed shape of `pyry pair revoke <name>`'s flag set.
type pairRevokeArgs struct {
    instanceName string // -pyry-name
    deviceName   string // sole positional; the entry to remove
}

// parsePairRevokeArgs parses the flag set for `pyry pair revoke`. Only
// -pyry-name is accepted, and exactly one positional (the device Name) is
// required. Zero, two, or more positionals — or any unknown flag — is an
// error propagated to the caller; runPairRevoke maps these to exit 2.
func parsePairRevokeArgs(args []string) (pairRevokeArgs, error) {
    fs := flag.NewFlagSet("pyry pair revoke", flag.ContinueOnError)
    fs.SetOutput(io.Discard)
    instance := fs.String("pyry-name", defaultName(), "instance name (state dir: ~/.pyry/<name>/)")
    if err := fs.Parse(args); err != nil {
        return pairRevokeArgs{}, err
    }
    switch fs.NArg() {
    case 0:
        return pairRevokeArgs{}, fmt.Errorf("missing device name")
    case 1:
        return pairRevokeArgs{instanceName: *instance, deviceName: fs.Arg(0)}, nil
    default:
        return pairRevokeArgs{}, fmt.Errorf("unexpected positional %q", fs.Arg(1))
    }
}
```

The two error shapes (`missing device name` / `unexpected positional`) both flow into `runPairRevoke`'s exit-2 branch. The error fragments are stable and grep-able for table-driven tests.

The `-pyry-name=<instance>` flag must work in either order relative to the positional (`pyry pair revoke -pyry-name=foo phone` and `pyry pair revoke phone -pyry-name=foo`). Go's `flag` package handles flag/positional interleaving by stopping at the first non-flag token, so `parsePairRevokeArgs phone -pyry-name=foo` would treat `-pyry-name=foo` as a second positional and fail. To support flag-after-positional invocation we'd need `flag.ParseAll`-style handling. **Decision: do not support flag-after-positional**, matching the stdlib `flag` default and every sibling subcommand on the project (`runPairList`, `runSessions*`, `runPairDefault`). Documenting this here so the developer doesn't add a custom reorder pass.

### Runner

```go
// runPairRevoke implements `pyry pair revoke <name>`: load the device
// registry for the resolved instance, remove the entry whose Name equals
// <name>, and persist the change.
//
// Returns nil on success (writes "Revoked <name>.\n" to stdout, exit 0).
// Calls os.Exit(2) directly for usage failures (flag parse, missing or
// extra positional) — bypasses main's `pyry: ` prefix.
// Calls os.Exit(1) directly for the not-found case ("no device named
// <name>" stderr) — same prefix-bypass reason.
// Returns a wrapped `fmt.Errorf("pair revoke: %w", err)` for I/O errors
// (Load/Save) — main.run prefixes with `pyry: ` to give the full
// `pyry: pair revoke: …` chain expected by AC#6.
func runPairRevoke(args []string) error {
    parsed, err := parsePairRevokeArgs(args)
    if err != nil {
        fmt.Fprintln(os.Stderr, "pyry pair revoke:", err)
        fmt.Fprintln(os.Stderr, "usage: pyry pair revoke [-pyry-name=<instance>] <name>")
        os.Exit(2)
    }
    devicesPath := resolveDevicesPath(parsed.instanceName)
    registry, err := devices.Load(devicesPath)
    if err != nil {
        return fmt.Errorf("pair revoke: %w", err)
    }
    if !registry.Remove(parsed.deviceName) {
        fmt.Fprintf(os.Stderr, "pyry pair revoke: no device named %s\n", parsed.deviceName)
        os.Exit(1)
    }
    if err := registry.Save(devicesPath); err != nil {
        return fmt.Errorf("pair revoke: %w", err)
    }
    fmt.Printf("Revoked %s.\n", parsed.deviceName)
    return nil
}
```

Three branch decisions worth flagging:

1. **Not-found uses `os.Exit(1)` directly, not a sentinel error.** The AC offers either mechanism; direct exit is the smaller-diff choice and avoids introducing a `var errNoDevice = errors.New(...)` sentinel that would only have one consumer. `main.run`'s `pyry: <err>` prefix is suppressed by exiting before the return, which is required by AC#5's "exactly `pyry pair revoke: no device named <name>\n`" stderr contract.
2. **`Save` is called only on successful `Remove`.** This is necessary for AC#5's "the on-disk registry is not rewritten" guarantee. Even though `Save` is idempotent for unchanged content, on an empty cold-start registry it would create the file at `~/.pyry/<name>/devices.json` (`MkdirAll` + atomic rename of an empty `{"devices":[]}\n`). The not-found branch must be byte-identical to the pre-call state, which means short-circuiting before `Save`.
3. **`stdout` write goes through `fmt.Printf`, not a pure formatter.** Unlike #214's `renderPairList` (where the formatter has interesting layout logic worth unit-testing), the success message is a single `Printf` with one substitution. There is no benefit to extracting it; the AC's exact-string check is verified by the E2E test against `os.Stdout`.

### Help text

In `cmd/pyry/main.go`'s `printHelp`, append one line under the existing `pyry pair list` line:

```
  pyry pair revoke <name> [flags]                revoke a paired device by Name
```

Match the column alignment of `pyry pair list` (lead with `  `, two-space gap before description). One-line edit.

## Data flow

```
argv ──> runPair ──> case "revoke" ──> runPairRevoke
                                            │
                                            ├──> parsePairRevokeArgs       ──> instanceName + deviceName
                                            ├──> resolveDevicesPath(name)  ──> path
                                            ├──> devices.Load(path)        ──> *Registry (or err)
                                            ├──> Registry.Remove(devName)  ──> bool
                                            │      ├──> false → stderr + os.Exit(1)
                                            │      └──> true  → continue
                                            ├──> Registry.Save(path)       ──> nil (or err)
                                            └──> fmt.Printf "Revoked …\n"  ──> stdout
```

Single goroutine. No channels. No `context.Context`. No daemon. The only filesystem write is `Registry.Save` on the success path.

## Concurrency model

None. One-shot CLI verb, runs to completion before `main` returns. The registry's internal `sync.Mutex` (`internal/devices/registry.go:26`) is taken inside `Load`/`Remove`/`Save` for the project's broader concurrency contract, but in this verb's lifetime the registry is held by exactly one caller — the `runPairRevoke` goroutine — so no contention is observable.

A second `pyry` process running `pair revoke` concurrently against the same `devices.json` is theoretically possible but not in scope: the two processes would race at the `Load` → `Remove` → `Save` boundary, with the later `Save` overwriting the earlier one (last-writer-wins). This is the same race the existing `pair` and `sessions` verbs already have; #215 inherits the parent contract without making it worse. No file lock is added — out of scope per the ticket's "thin wiring" framing.

## Error handling

| Failure | Path | Exit | Stderr |
|---|---|---|---|
| Missing positional (`pyry pair revoke`) | `parsePairRevokeArgs` returns `missing device name` | 2 (direct) | `pyry pair revoke: missing device name` + usage line |
| Extra positional (`pyry pair revoke a b`) | `parsePairRevokeArgs` returns `unexpected positional "b"` | 2 (direct) | `pyry pair revoke: unexpected positional "b"` + usage line |
| Unknown flag (`pyry pair revoke --bogus phone`) | `flag.Parse` error | 2 (direct) | `pyry pair revoke: flag provided but not defined: -bogus` + usage line |
| Registry I/O error on Load (permission denied) | `devices.Load` returns err | 1 (returned) | `pyry: pair revoke: registry: read <path>: permission denied` |
| Malformed JSON | `devices.Load` returns err | 1 (returned) | `pyry: pair revoke: registry: parse <path>: <json err>` |
| No device matches `<name>` (incl. missing/empty registry) | `Registry.Remove` returns false | 1 (direct) | `pyry pair revoke: no device named <name>` |
| Save fails after successful Remove (permission denied) | `Registry.Save` returns err | 1 (returned) | `pyry: pair revoke: registry: <step>: <err>` |
| Successful removal | (none) | 0 | (none; stdout: `Revoked <name>.\n`) |

The exit-2 path uses `os.Exit(2)` directly (mirrors `runPairList`'s flag-parse branch). The not-found exit-1 path uses `os.Exit(1)` directly (so the stderr message is byte-exact `pyry pair revoke: …` with no `pyry:` doubling). The I/O error exit-1 path returns a wrapped error and lets `main.run` add the standard `pyry: ` prefix — the result is `pyry: pair revoke: …`, matching `runPairList`'s I/O-error format exactly.

The asymmetry between "not-found exits via `os.Exit(1)`" (no main prefix) and "I/O error returns" (with main prefix) is intentional and called out in AC#5/AC#6: not-found is a user-visible operational result with a curated message; I/O error is an internal failure where the standard prefix carries diagnostic value.

## Testing strategy

### Unit tests in `cmd/pyry/pair_test.go`

1. **`TestParsePairRevokeArgs`** — table-driven, mirroring `TestParsePairListArgs`:
   - `{name: "happy", args: []string{"phone"}, wantInstance: defaultName(), wantDeviceName: "phone"}`
   - `{name: "with instance", args: []string{"-pyry-name=foo", "phone"}, wantInstance: "foo", wantDeviceName: "phone"}`
   - `{name: "missing positional", args: nil, wantErr: "missing device name"}`
   - `{name: "extra positional", args: []string{"a", "b"}, wantErr: "unexpected positional"}`
   - `{name: "unknown flag", args: []string{"--bogus", "phone"}, wantErr: "flag provided but not defined"}`

   Pin `t.Setenv("PYRY_NAME", "")` at the top so `defaultName()` is deterministic.

### Integration tests in `cmd/pyry/pair_test.go`

These tests exercise `runPairRevoke` directly (not through the binary) using a fixture `devices.json` written via `devices.Load` + `Registry.Add` + `Registry.Save`, then read back after revoke. They run under `t.Setenv("HOME", t.TempDir())` so `resolveDevicesPath` resolves under the test sandbox.

Because `runPairRevoke` calls `os.Exit` on the not-found and usage paths, the tests for those branches go in the E2E file (where the binary is forked). The tests below cover only the non-exit paths:

2. **`TestRunPairRevoke_RemovesEntry`** — fixture with two devices (`alpha`, `bravo`); call `runPairRevoke([]string{"alpha"})`; assert returned `nil`; reload `devices.Load(path)`; assert `len(List()) == 1` and the surviving entry is `bravo` byte-for-byte (compare `Name`, `TokenHash`, `PairedAt.Equal`, `LastSeenAt.Equal`).
3. **`TestRunPairRevoke_SaveFailure`** — fixture as above, then `os.Chmod(<dir>, 0o500)` to make the parent unwritable; call `runPairRevoke([]string{"alpha"})`; assert returned error wraps with `pair revoke:` prefix (`strings.Contains(err.Error(), "pair revoke:")`). Skip on Windows (we're Linux+macOS only). On macOS root, `chmod 0500` may not block atomic rename inside a tempdir owned by the test user; gate the assertion on a pre-flight `os.WriteFile` attempt that errors out, and `t.Skip()` if it doesn't.

The not-found and missing-positional paths can't be unit-tested without `os.Exit` capture machinery (which is not present in this repo) — they're covered by the E2E test below, which is the project's standard escape valve for `os.Exit`-bearing flows (same pattern `runPairList`'s exit-2 paths use).

### E2E test in `internal/e2e/pair_test.go`

4. **`TestPairRevoke_E2E`** — three sub-tests, all using `RunBareIn(t, home, "pair", "revoke", …)`. Each pins `HOME` to a fresh `t.TempDir()` so the registry path is `<home>/.pyry/pyry/devices.json` for the default instance.

   - **`removes one of two`** — pair `phone-a`, pair `phone-b`, run `pyry pair revoke phone-a`, assert exit 0 + stdout exactly `Revoked phone-a.\n` + stderr empty. Round-trip via `devices.Load(<path>)`: assert `len(List()) == 1` and surviving entry's `Name == "phone-b"`. Assert the surviving entry's `TokenHash` and `PairedAt` are byte-identical to the value captured before the revoke (read once after `pair phone-b`, compare after revoke).
   - **`not found`** — pair `phone-a`, run `pyry pair revoke ghost`, assert exit 1 + stderr exactly `pyry pair revoke: no device named ghost\n` + stdout empty. Round-trip via `devices.Load`: assert the on-disk file bytes are byte-identical to the pre-revoke snapshot (read the file's bytes before and after, compare with `bytes.Equal`).
   - **`missing registry`** — fresh `HOME` with no prior pair, run `pyry pair revoke ghost`, assert exit 1 + stderr exactly `pyry pair revoke: no device named ghost\n`. Assert `<home>/.pyry/pyry/devices.json` does NOT exist after the call (the AC's "no file created" requirement; verify with `os.Stat` returning `fs.ErrNotExist`).

   Stdlib only: `bytes`, `errors`, `io/fs`, `os`, `path/filepath`, `strings`, `testing`.

### What is NOT tested

- `Registry.Remove`'s byte-exact comparison and `Registry.Save`'s atomic rename — already covered in `internal/devices/registry_test.go` (per #209).
- Concurrent-revoke last-writer-wins behavior — out of scope per "concurrency model" above.
- `printHelp` output containing the new `pair revoke` line — follows the project's pattern (no help-text test exists for `pair list` either; help text is human-eyeballed during PR review).

## Open questions

1. **Sentinel error vs. direct `os.Exit(1)` for not-found.** AC#5 explicitly leaves this to the architect. Picked direct `os.Exit(1)` because (a) a sentinel + `errors.Is` check in `main.run` would touch a second file (`cmd/pyry/main.go`'s error printer) for one consumer, (b) the existing exit-2 paths already use direct exit so the pattern is established, and (c) AC#5 only constrains the byte-exact stderr text, which is identical either way.
2. **No file lock around Load → Remove → Save.** Concurrent `pair revoke` against the same `devices.json` last-writer-wins. Same contract as the existing `pair` and `sessions` verbs. Out of scope for this XS ticket; if a real workflow surfaces the race, a follow-up would add `flock` at the `internal/devices` boundary so all callers benefit.
3. **`pairVerbList` becomes `"list, revoke"`** — same lockstep-update pattern documented in the const's source comment (`cmd/pyry/pair.go:22-25`). No further coordination needed; #214's spec already anticipated this exact edit.

## Files modified

| File | Change | Approx LoC |
|---|---|---|
| `cmd/pyry/pair.go` | Append `case "revoke":` to `runPair`, update `pairVerbList` literal, add `pairRevokeArgs` type + `parsePairRevokeArgs` + `runPairRevoke` | +45 prod |
| `cmd/pyry/main.go` | One usage line under the `pyry pair list` block | +1 prod |
| `cmd/pyry/pair_test.go` | Add `TestParsePairRevokeArgs`, `TestRunPairRevoke_RemovesEntry`, `TestRunPairRevoke_SaveFailure` | +90 test |
| `internal/e2e/pair_test.go` | Add `TestPairRevoke_E2E` with three sub-tests | +75 test |

Total production code: ~46 LoC across 2 files. No new files. No new exported types. No new packages. No new dependencies.
