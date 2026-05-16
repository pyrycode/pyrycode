# #359 — Remove stale "scaffold only — no spawn yet" disclaimer from `pyry agent-run --help`

## Files to read first

- `cmd/pyry/agent_run.go:78-82` — the `fs.Usage` callback containing the stale
  one-liner. This is the only production-code line that changes.
- `cmd/pyry/agent_run.go:179-188` — the `runAgentRun` doc comment. AC #2 names
  this as the canonical prose source for the new help text; adapt (don't
  copy verbatim — it's slightly too long for `--help`).
- `cmd/pyry/agent_run.go:234-266` — `buildClaudeArgs` and its preceding doc
  comment. Ground truth for what the help line is summarising: `--max-turns`,
  `--dangerously-skip-permissions`, `--allowed-tools`, `--input-format
  stream-json`, `--output-format stream-json --verbose`. Use this to check
  that the help prose stays accurate, but do NOT enumerate flags in the
  help text (the AC says "describes behaviour, not flags").
- `cmd/pyry/agent_run_test.go` — sibling table tests for `parseAgentRunArgs`.
  The new unit test goes here; follow the file's existing testing idioms
  (stdlib `testing`, table-driven where relevant, no testify).

No QMD / lessons / decisions lookup needed — the issue body cites the
authoritative file:line ranges and the change is scoped to one string
literal plus one test.

## Context

`pyry agent-run --help` currently prints:

> Drive a single supervised claude turn headlessly (scaffold only — no
> spawn yet).

The "(scaffold only — no spawn yet)" parenthetical is stale: the verb has
shipped since v0.12.0 and PR #391 (2026-05-15) refactored it onto the
stream-json subprocess pipeline backed by `internal/agentrun/streamrunner`.
Anyone running `--help` to learn whether the verb is usable is misled into
thinking it is an unimplemented stub.

This ticket replaces the disclaimer with prose accurately summarising what
`pyry agent-run` does today, and locks the new text with a unit test so a
future stale-disclaimer regression fails CI.

## Design

### The new help text

In `cmd/pyry/agent_run.go`, replace the single-line `fmt.Fprintln` at
line 80 with a short description of current behaviour. The behaviour to
summarise (verbatim from AC #2, and consistent with `runAgentRun`'s doc
comment at lines 179–188 and `buildClaudeArgs`'s doc comment at lines
237–253) is:

- spawns `claude` as a stream-json subprocess (no PTY, no JSONL watcher),
- sends the user prompt as a stream-json envelope on claude's stdin,
- forwards claude's stream-json stdout (its canonical `system init` /
  assistant deltas / `result` events) byte-for-byte to pyry's stdout for
  the dispatcher to consume,
- passes `--max-turns` through to claude (which enforces it),
- uses `--dangerously-skip-permissions` paired with `--allowed-tools` as
  the authoritative tool gate.

Length target: roughly 5–8 lines of prose (excluding the `Usage:` header
and `fs.PrintDefaults()` output). The `runAgentRun` doc comment is the
canonical source; adapt for brevity since `--help` is signage, not
documentation. Describe behaviour; do NOT enumerate flag names — the
flags themselves are already documented by `fs.PrintDefaults()` directly
below.

The first line of the description must remain a one-sentence summary
("Drive a single supervised claude turn headlessly." — or equivalent),
with the disclaimer parenthetical removed entirely. Subsequent lines
expand into the behavioural summary above.

### Mechanism for the unit test (extract a constant)

To lock the help text without changing `parseAgentRunArgs`'s signature
(AC #4 forbids that), extract the description prose to a package-level
constant in `cmd/pyry/agent_run.go`:

```go
const agentRunUsageDescription = `<the new multi-line description>`
```

The `fs.Usage` callback then references the constant:

```go
fs.Usage = func() {
    fmt.Fprintln(fs.Output(), "Usage: pyry agent-run [flags]")
    fmt.Fprintln(fs.Output(), agentRunUsageDescription)
    fs.PrintDefaults()
}
```

The constant is unexported; same-package tests can read it directly.
This satisfies AC #4 ("`runAgentRun`, `parseAgentRunArgs`, and
`buildClaudeArgs` are untouched aside from the help-string literal") —
moving the literal into a constant IS touching the literal, which the
AC explicitly permits, and the surrounding function bodies stay
behaviourally identical.

### The unit test

Add a single test function in `cmd/pyry/agent_run_test.go`. The test
asserts on `agentRunUsageDescription` directly (the constant is the
"same path" `--help` triggers — the `fs.Usage` callback prints exactly
this string between the `Usage:` header and the flag defaults).

Scenarios to cover (bullet form — developer writes the test code in the
file's idiom):

- **Must NOT contain the stale disclaimer.** Assert
  `!strings.Contains(agentRunUsageDescription, "scaffold only")`. This
  is the regression-guard against AC #1.
- **Must contain a stable substring from the new prose.** Pick a single
  word or short phrase that's unambiguously from the new description and
  unlikely to change in a normal copy-edit — `"stream-json"` is the best
  candidate (it appears multiple times in the canonical doc comment and
  is the load-bearing protocol name; the prose cannot describe the
  current behaviour without it). The test asserts
  `strings.Contains(agentRunUsageDescription, "stream-json")`.
- **(Optional) Must contain `"--max-turns"` and `"--allowed-tools"`** —
  these are the two behavioural anchors AC #2 calls out. Locking them
  catches a future copy-edit that accidentally drops one of the
  load-bearing behaviours from the prose. Recommended as a single
  table-driven assertion over the required substrings.

The test does NOT need to invoke the flag parser or capture stderr.
Asserting on the constant is sufficient: the constant is the *only*
source of the description prose, and flipping it flips both production
and the test in lockstep. (An alternative — capturing `fs.Output()` via
a redirected writer — would require either changing `parseAgentRunArgs`'s
signature, which AC #4 forbids, or mutating `os.Stderr` globally during
the test, which is brittle. Constant-level assertion is the
minimum-friction shape that satisfies the AC.)

### Out of scope

- No change to `runAgentRun`, `parseAgentRunArgs`, `buildClaudeArgs`,
  `splitAllowedTools`, `requireRegularFile`, or `requireDir`.
- No change to the flag set: name, types, default values, per-flag help
  strings (the strings passed as the third arg to `fs.String` / `fs.Int`
  at lines 69–76) all stay.
- No update to `runAgentRun`'s doc comment — its prose is already
  accurate and is the source for the new help text, not the other way
  around.
- No new exported types or constants — `agentRunUsageDescription` is
  unexported (lowercase first letter).

## Concurrency model

N/A — text edit plus one synchronous test.

## Error handling

N/A — text edit. The error paths in `parseAgentRunArgs` are untouched.

## Testing strategy

- The new unit test (described above) is the only test added.
- Run `go test ./cmd/pyry/...` to verify the test passes.
- Run `go test -race ./...` per project convention.
- No manual `pyry agent-run --help` invocation is needed for CI, but the
  developer should run it once locally as a sanity check that the
  rendered output reads cleanly.

## Open questions

None. The acceptance criteria are concrete, the help-text content is
sourced verbatim from AC #2 (and corroborated by the existing
`runAgentRun` doc comment), and the test mechanism is the only shape
compatible with AC #4's "untouched signatures" constraint.
