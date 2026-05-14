# 337 — `pyry agent-run` subcommand scaffold + flag parsing

## Files to read first

- `cmd/pyry/main.go:161-190` — top-level verb dispatch switch (`run()`); the new `case "agent-run":` slots in here next to the existing verbs.
- `cmd/pyry/main.go:1275-1342` — `printHelp()`; add a one-line entry for `agent-run` so `pyry help` mentions the new verb (mirrors how `update` / `pair` appear).
- `cmd/pyry/update.go:25-53` — closest sibling pattern: a verb that does NOT use `parseClientFlags` (no control-socket dial), creates its own `flag.NewFlagSet`, and returns errors that main maps to exit 1. Copy this shape.
- `cmd/pyry/pair.go:58-85` — example of a verb-local unexported args struct (`pairArgs`) + a `parsePairArgs` helper that returns it. Same split: parse → validate → return struct.
- `cmd/pyry/main.go:753-768` — `parseSessionsNewArgs` shows the canonical small-helper shape: build `flag.NewFlagSet` with `flag.ContinueOnError`, `fs.SetOutput(os.Stderr)`, return parsed struct + error. Match this.
- Parent #329 issue body — Phase A spike's "Phase B status: GREENLIT" block enumerates the eventual spawn arg list (`--settings`, `--permission-mode default`, `--model`, `--append-system-prompt-file`, `--effort`). This ticket lands ONLY the flag surface; subsequent tickets consume the parsed struct.
- Sibling ticket bodies (the trust-merge and settings-file split-offs from #331) — they will read the parsed `agentRunArgs` struct, so keep field names stable.

## Context

Phase A spike (#329) verified the path from `claude -p` to a pyry-supervised interactive claude. Before any side-effecting tickets (trust-state merge, per-spawn settings file, claude spawn, JSONL watch) land, the verb itself must exist with a complete, validated flag surface. This ticket lands ONLY:

1. The `agent-run` case in the top-level dispatch switch.
2. A new file `cmd/pyry/agent_run.go` with the flag set, parse function, validation, and an empty `runAgentRun` body that exits 0 with a single confirmation line on stdout.
3. Tests covering each missing-required and bad-value path, plus a happy-path row.

No file I/O effects beyond what flag validation already implies (existence checks on `--prompt-file`, `--system-prompt-file`, `--workdir`). No claude spawn. No trust-state writes. No settings-file emission. Those land in sibling tickets that consume the parsed `agentRunArgs` struct.

## Design

### Package boundary

All new code lives in `package main` under `cmd/pyry`. No new `internal/` package; this is dispatcher-facing wiring, not a reusable primitive. Matches `update.go` and `pair.go`'s shape.

### Files touched

| File | Change |
| --- | --- |
| `cmd/pyry/main.go` | Add `case "agent-run": return runAgentRun(os.Args[2:])` in the `run()` switch (alphabetically: between `install-service` and `update` is fine, but match the existing partial-alpha order — place after `update` is acceptable). Add one help-text line in `printHelp()` describing the verb. Add `agent-run` to the package-doc comment block at the top of the file (lines 13-24) listing reserved verbs. |
| `cmd/pyry/agent_run.go` | NEW. Contains the args struct, the `parseAgentRunArgs` helper, the `validateAgentRunArgs` helper (or fold validation into parse), and `runAgentRun`. |
| `cmd/pyry/agent_run_test.go` | NEW. Table-driven tests for parse + validate. Test the verb-level entry through `parseAgentRunArgs` (not `runAgentRun`), so the tests don't depend on stdout/exit behaviour. |

### Args struct

Unexported, local to `cmd/pyry`. Field names chosen so sibling tickets can read them without renames. Use Go zero-value semantics where it doesn't muddle validation (string zero is `""`; int zero is treated as "unset" only because `--max-turns > 0` validation rejects it).

Sketch (contract only — fields and types, not the body):

```go
type agentRunArgs struct {
    promptFile       string   // --prompt-file       (path, must exist)
    systemPromptFile string   // --system-prompt-file (path, must exist)
    allowedTools     []string // --allowed-tools     (split-and-trimmed)
    maxTurns         int      // --max-turns         (>0)
    effort           string   // --effort            (enum)
    model            string   // --model             (non-empty)
    workdir          string   // --workdir           (dir, must exist)
    outputFormat     string   // --output-format     (literal "stream-json")
}
```

Effort enum lives next to the struct as an unexported package-level slice or set:

```go
var validEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
```

(Slice + `slices.Contains` is equivalent; pick whichever matches the local style. Both `internal/config` and `internal/control` use slices in this shape.)

### Parse + validate

A single function `parseAgentRunArgs(args []string) (agentRunArgs, error)`:

- Build `fs := flag.NewFlagSet("pyry agent-run", flag.ContinueOnError)`, `fs.SetOutput(os.Stderr)` so `flag`'s own usage-printing for unknown flags is friendly.
- Register each flag with `fs.String` / `fs.Int`. Defaults are empty / zero. Required-ness is enforced in the validation pass below, not by `flag`.
- After `fs.Parse(args)`:
  - Reject positionals: `if fs.NArg() > 0 { return ..., fmt.Errorf("unexpected positional %q", fs.Arg(0)) }`.
  - For each required string flag, reject empty after trim.
  - For `--prompt-file` and `--system-prompt-file`: `os.Stat` and require regular file (`info.Mode().IsRegular()`); error message names the flag.
  - For `--workdir`: `os.Stat` and require `info.IsDir()`.
  - For `--allowed-tools`: tokenise via a small helper `splitAllowedTools(raw string) []string` that walks the string, treats both `,` and any ASCII whitespace as a separator, trims each token, drops empties. Reject if the result is empty. Return the slice.
  - For `--max-turns`: reject `<= 0`.
  - For `--effort`: reject if not in `validEfforts`.
  - For `--output-format`: reject anything other than literal `"stream-json"`.
- On any validation failure, return a wrapped error of the form `fmt.Errorf("agent-run: --<flag>: <reason>")`. The top-level `main` then prints `pyry: agent-run: --max-turns: must be > 0 (got 0)` — single line, names the offending flag, satisfies AC.

Tokeniser contract (one-line behaviour summary — pure function):

- `splitAllowedTools("Read,Bash")` → `["Read", "Bash"]`
- `splitAllowedTools("Read Bash")` → `["Read", "Bash"]`
- `splitAllowedTools("Read, Bash , Edit")` → `["Read", "Bash", "Edit"]`
- `splitAllowedTools("")` → `[]` (caller treats empty result as a parse error; helper itself does not error)
- `splitAllowedTools(",,Read,,")` → `["Read"]`

`strings.FieldsFunc` with `r == ',' || unicode.IsSpace(r)` collapses to a one-liner. The test rows above are the spec; developer writes them as table cases.

### `runAgentRun`

Body sketch (contract only):

```go
func runAgentRun(args []string) error {
    parsed, err := parseAgentRunArgs(args)
    if err != nil {
        return err
    }
    _ = parsed // consumed by sibling tickets (trust-merge, settings-file, spawn)
    fmt.Println("pyry agent-run: flag set valid; scaffold-only ticket #337 — no spawn yet")
    return nil
}
```

The confirmation line text is not load-bearing — siblings will overwrite this when they add behaviour. Pick anything that satisfies AC#3 ("a single confirmation line on stdout").

Do NOT use `parseClientFlags` here. `agent-run` does not dial the daemon's control socket. Compare: `runUpdate` (no client flags) vs. `runStatus` / `runStop` / `runAttach` / `runSessions` (all client-flag verbs). The former is the right shape.

### Help text

Add to `printHelp()` in the verb table:

```
pyry agent-run [flags]                         spawn an interactive claude under
                                               supervision and drive a single turn
                                               headlessly; replaces `claude -p` in
                                               the dispatcher (see --help on the
                                               verb for the full flag list)
```

(One-liner; matches the existing entries' tone. The detailed flag list per AC is the verb's own `--help` once `flag.ContinueOnError` is wired — but note that with `flag.ContinueOnError`, `-h` / `--help` returns `flag.ErrHelp` from `Parse` rather than printing anything. If you want `pyry agent-run --help` to print usage, register `fs.Usage = func() { ... }` and call it on `flag.ErrHelp`. This is optional for #337; the AC doesn't require help output. If you skip it, `--help` will surface as a flag-parse error, which is acceptable but unfriendly. Recommendation: write `fs.Usage` for parity with `pair.go`'s usage line.)

### Error contract

Two failure modes:

1. **Parse-level (unknown flag, bad int syntax):** `flag.Parse` returns an error. Propagate via `fmt.Errorf("agent-run: %w", err)`. Top-level prints `pyry: agent-run: flag provided but not defined: -foo` — single line, exit 1.
2. **Validation-level (missing required, bad value):** `fmt.Errorf("agent-run: --<flag>: <reason>")`. Same exit-1 path.

The AC says "exits non-zero with a one-line message naming the offending flag". Exit 1 via `return err` from `runAgentRun` is fine — `runPair` uses `os.Exit(2)` for usage errors, but that's a stylistic choice for the pair verb's own UX. AC doesn't require a specific non-zero code; pick exit 1 for consistency with `runUpdate`.

## Concurrency model

None for this ticket. `runAgentRun` is purely sequential parse → validate → print → return. No goroutines, no context, no channels.

## Error handling

See "Error contract" above. Two paths, both wrapped through `fmt.Errorf("agent-run: ...")` so the top-level `pyry: <err>` prefix renders correctly.

`os.Stat` errors on `--prompt-file` / `--system-prompt-file` / `--workdir` use `fmt.Errorf("agent-run: --prompt-file: %w", err)` so the underlying OS error (ENOENT, EACCES, …) flows through. Tests assert the message contains the flag name, not the full underlying-error text — leaves the OS-error string free to vary across platforms.

## Testing strategy

`cmd/pyry/agent_run_test.go` — table-driven, stdlib `testing`, no testify (per `CODING-STYLE.md`).

Test approach: call `parseAgentRunArgs` directly, NOT `runAgentRun`. Verb-level entrypoint adds nothing testable beyond what `parseAgentRunArgs` already covers; testing the parsed struct + error is enough.

Table rows (one per AC bullet, plus happy path):

- **happy path** — all flags valid; assert returned struct fields match inputs (including `allowedTools` split correctly).
- **--prompt-file missing** — flag omitted; expect error mentioning `--prompt-file`.
- **--prompt-file not found** — path points at nonexistent file; expect error mentioning `--prompt-file`.
- **--prompt-file is a directory** — path points at a dir (use `t.TempDir()`); expect error mentioning `--prompt-file`.
- **--system-prompt-file missing** — same shape as prompt-file.
- **--system-prompt-file not found** — same shape.
- **--allowed-tools missing** — flag omitted; error mentions `--allowed-tools`.
- **--allowed-tools empty after split** — `--allowed-tools ", ,,"`; error mentions `--allowed-tools`.
- **--allowed-tools comma form** — `--allowed-tools "Read,Bash"` → `[]string{"Read", "Bash"}`.
- **--allowed-tools space form** — `--allowed-tools "Read Bash"` → `[]string{"Read", "Bash"}`.
- **--allowed-tools mixed form** — `--allowed-tools "Read, Bash , Edit"` → `[]string{"Read", "Bash", "Edit"}`.
- **--max-turns missing** — flag omitted (default int 0); error mentions `--max-turns`.
- **--max-turns zero** — `--max-turns 0`; error mentions `--max-turns`.
- **--max-turns negative** — `--max-turns -1`; error mentions `--max-turns`.
- **--effort missing** — error mentions `--effort`.
- **--effort bad value** — `--effort wat`; error mentions `--effort` (and ideally lists the valid set, but the AC only requires the flag name).
- **--effort each valid value** — sub-cases for `low / medium / high / xhigh / max`; assert no error.
- **--model missing** — error mentions `--model`.
- **--workdir missing** — error mentions `--workdir`.
- **--workdir not found** — error mentions `--workdir`.
- **--workdir is a file** — path points at a temp file; error mentions `--workdir`.
- **--output-format missing** — error mentions `--output-format`.
- **--output-format wrong value** — `--output-format json`; error mentions `--output-format`.
- **--output-format stream-json** — accepted.
- **unexpected positional** — trailing `foo` after the flags; error mentions "unexpected positional".

Helper: a small `validArgs(t *testing.T) []string` that builds a fully-valid argv (using `t.TempDir()` to create real prompt-file / system-prompt-file / workdir on disk) so each error row can override one flag and reuse the rest. Pattern: same as how `update_test.go` sets up its tempdir scaffold.

`splitAllowedTools` should also have its own focused unit test independent of the full parse path — five rows from the tokeniser contract above. Pure function, no setup, no tempdir.

### What NOT to test in #337

- The behaviour of `runAgentRun`'s confirmation-line printout. Siblings replace this; testing it would pin an implementation detail that's about to change.
- The integration with `cmd/pyry/main.go`'s switch. A separate quick smoke through `internal/e2e/cli_verbs_test.go` (in the style of `TestVersion_E2E`) could verify `pyry agent-run` is reachable end-to-end, but that's optional polish — the unit test on `parseAgentRunArgs` already proves the verb's contract.

## Open questions

- **`pyry agent-run --help` behaviour.** AC doesn't require it. Recommended: register `fs.Usage` so `-h` / `--help` prints a flag listing instead of falling through as a parse error. Cost is ~10 lines of `fmt.Fprintln` in a `fs.Usage = func() { ... }` closure. Developer's call — either is acceptable for this ticket.
- **Effort enum value ordering / aliasing.** `xhigh` is unusual; the spike ticket states it. If the developer discovers during implementation that the upstream claude CLI's `--effort` flag uses different value names, file a follow-up issue — DON'T silently rename in this ticket (#337 freezes the surface for sibling tickets).
- **Whether `agent-run` should accept `-pyry-*` flags.** No, not in this ticket. The verb is standalone (like `update`), not daemon-attached. If sibling tickets later need `-pyry-name` (e.g. to log under a specific instance's log file), add it then. AC says nothing about pyry flags here.
