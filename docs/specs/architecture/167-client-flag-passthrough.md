# 167 — `parseClientFlags` must pass through verb-specific flags

## Files to read first

- `cmd/pyry/main.go:469-482` — current `parseClientFlags`; the `flag.FlagSet` it builds is what rejects `--stdio` before `parseAttachArgs` gets a chance to see it.
- `cmd/pyry/main.go:537-583` — `parseAttachArgs` and `attachSelectorFromArgs`; both already work in isolation. Do NOT touch them — the bug is upstream.
- `cmd/pyry/main.go:598-616` — `runAttach` arg path, the dispatch site this ticket exists to unblock. Composes `parseClientFlags` → `parseAttachArgs` → `control.AttachStdio`.
- `cmd/pyry/main.go:222-262` — existing `splitArgs` for the top-level CLI's pyry/claude split. The new `splitClientFlags` mirrors its shape (walk left-to-right, stop at first non-recognised token, support `=`-glued and space-separated values, both `-` and `--` prefixes).
- `cmd/pyry/main.go:331-340` — `parseFlagSyntax`. Reuse it; do not re-implement the dash/= parsing.
- `cmd/pyry/main.go:484-535` — `runStatus` and `runLogs` (currently discard `rest`; must reject non-empty `rest` post-fix or unknown flags would be silently swallowed).
- `cmd/pyry/main.go:1066-1083` — `runStop` (same shape as runStatus/runLogs).
- `cmd/pyry/main.go:649-680` — `runSessions` (already handles `rest`; verifies that the new contract fits the existing split).
- `cmd/pyry/args_test.go:10-105` — `TestParseClientFlags` and `TestParseClientFlags_ReturnsRest`. The `unknown flag returns error` subtest at lines 60-65 encodes the OLD contract and must be updated. `TestParseClientFlags_ReturnsRest` is extended with new cases.
- `cmd/pyry/args_test.go:145-197` — `TestParseAttachArgs`. Untouched; pinned as the in-isolation contract for `parseAttachArgs`.
- `cmd/pyry/args_test.go:199-312` — `TestSplitArgs`. The new `TestSplitClientFlags` mirrors its table-driven shape.
- `internal/e2e/attach_stdio_test.go:26-56` — the e2e test currently `t.Skip`'d. Skip line at 31 is removed; harness body is untouched.
- `docs/specs/architecture/154-attach-stdio-mode.md` — original `attach --stdio` design; line 122 documents the invocation shape this ticket restores.

## Context

`pyry attach --stdio <id>` fails with `flag provided but not defined: -stdio` before `parseAttachArgs` runs. The cause is `parseClientFlags` (`cmd/pyry/main.go:474-482`): it builds a `flag.FlagSet` registering only `-pyry-name` / `-pyry-socket` and calls `fs.Parse(args)`, so any unknown flag (`--stdio`, `--create-if-missing`, the future `pyry sessions new --name`, etc.) is rejected at the wrong layer.

The same parser is shared by every client verb (`status`, `logs`, `stop`, `attach`, `sessions`). The fix must pass verb-specific flags through to the verb's own parser without breaking the existing `-pyry-name` / `-pyry-socket` recognition or the existing rejection-of-malformed-invocations behaviour for verbs that take no extra args (`status`, `logs`, `stop`).

This is the last blocker on `TestE2E_AttachStdio_BytesRoundTrip` (added in #161, `t.Skip`'d pending this ticket).

## Design

The fix is a two-part change in `cmd/pyry/main.go`, with caller-side post-checks.

### 1. New helper: `splitClientFlags`

Add a helper next to `splitArgs` (around `cmd/pyry/main.go:222`) that mirrors `splitArgs`'s walk-and-extract shape, but recognises only the two client-side `-pyry-*` flags:

```go
// clientPyryValueFlags lists the -pyry-* flags every control client accepts.
// Both take a value (string). Walk-based extraction needs this map so it can
// decide whether to consume the next token as the value (for the
// space-separated form: `-pyry-name elli`).
var clientPyryValueFlags = map[string]bool{
    "pyry-name":   true,
    "pyry-socket": true,
}

// splitClientFlags peels recognised -pyry-name / -pyry-socket tokens off the
// front of args and returns them as pyryArgs, leaving everything else in
// rest verbatim. Stops at the first non-pyry-* token: subsequent -pyry-*
// tokens are not extracted. Mirrors splitArgs's shape; differs only in the
// recognised flag set.
//
// Both `-pyry-name=elli` and `-pyry-name elli` forms are supported, as are
// the `-` and `--` dash prefixes (parseFlagSyntax normalises both).
//
// `--` is treated as a verb-side token: it and everything after go into
// rest. The verb's own FlagSet is the one that should interpret `--`.
func splitClientFlags(args []string) (pyryArgs, rest []string) {
    i := 0
    for i < len(args) {
        a := args[i]
        if a == "--" {
            rest = append(rest, args[i:]...)
            return
        }
        name, _, hasVal := parseFlagSyntax(a)
        if !clientPyryValueFlags[name] {
            rest = append(rest, args[i:]...)
            return
        }
        pyryArgs = append(pyryArgs, a)
        if !hasVal && i+1 < len(args) {
            pyryArgs = append(pyryArgs, args[i+1])
            i += 2
            continue
        }
        i++
    }
    return
}
```

Notes:

- `parseFlagSyntax` already strips the leading `-`/`--` and splits on `=`. Reuse it; do not re-derive the rules.
- The "stop at first non-pyry-* token" rule matches the existing convention for client verbs ("`-pyry-*` flags must come before sub-verb flags"; see the comment on `runSessions` at `cmd/pyry/main.go:649-654`). It is also the simplest rule that makes `-pyry-socket=… --stdio … <id>` work without introducing a "pyry flags can appear anywhere" feature that would interact poorly with sub-verb parsers.
- A trailing `-pyry-name` with no value (i.e. last token of args, no `=`) is left in `pyryArgs` as a single token. The downstream `flag.FlagSet` errors on it the same way it does today — keeps the error path unchanged.

### 2. Rewrite `parseClientFlags`

Replace the body of `parseClientFlags` (`cmd/pyry/main.go:474-482`) with:

```go
func parseClientFlags(name string, args []string) (socketPath string, rest []string, err error) {
    pyryArgs, rest := splitClientFlags(args)
    fs := flag.NewFlagSet(name, flag.ContinueOnError)
    fs.SetOutput(io.Discard)
    nameFlag := fs.String("pyry-name", defaultName(), "instance name (socket: ~/.pyry/<name>.sock)")
    socketFlag := fs.String("pyry-socket", "", "explicit socket path (overrides -pyry-name)")
    if err := fs.Parse(pyryArgs); err != nil {
        return "", nil, err
    }
    return resolveSocketPath(*socketFlag, *nameFlag), rest, nil
}
```

Behavioural changes vs. today:

- Unknown flags (`--stdio`, `--create-if-missing`, `--bogus`, …) and verb-positionals are no longer rejected here; they appear in `rest` in original order.
- `-pyry-name` / `-pyry-socket` continue to be parsed and validated by the existing `flag.FlagSet` (it sees only the extracted `pyryArgs`, so its error messages stay sane — no spurious "flag provided but not defined" for the wrong layer).
- `fs.SetOutput(io.Discard)` is added (today it's not set, so flag parse errors print to stderr automatically). This matches `parseAttachArgs`'s posture; it suppresses double-printing when the caller wraps and prints the error itself.

### 3. Caller-side `rest` validation

`runAttach` (`cmd/pyry/main.go:598`) and `runSessions` (`cmd/pyry/main.go:659`) already consume `rest` and pass it to a verb-specific parser, so their rejection of unknown flags is preserved by `parseAttachArgs` / `parseSessionsNewArgs` / `parseSessionsRmArgs` / etc. No change needed.

`runStatus`, `runLogs`, `runStop` discard `rest` today (`_, _, err := parseClientFlags(...)`). After the fix, an unknown flag like `pyry status -unknown` would land in `rest` and be silently ignored. To preserve the AC ("continue to … reject genuinely-malformed invocations"), each of these three callers must add a post-check:

```go
socketPath, rest, err := parseClientFlags("pyry status", args)
if err != nil {
    return err
}
if len(rest) > 0 {
    return fmt.Errorf("status: unexpected arguments: %s", strings.Join(rest, " "))
}
```

Apply identically to `runLogs` (verb name `"logs"`) and `runStop` (verb name `"stop"`). Three sites. Inline the check; do not extract a helper for three call sites.

`strings` is already imported (used elsewhere in main.go — verify with `goimports`).

### 4. Remove the e2e skip

Delete `internal/e2e/attach_stdio_test.go:27-31` (the `// Blocked on #167…` block plus the `t.Skip` line). The existing test body is unchanged.

## Concurrency model

None. This is a pure-function CLI argument-parsing change. No goroutines, no shared state, no context.

## Error handling

- `splitClientFlags` cannot fail; it returns two slices.
- `parseClientFlags` returns the error path unchanged: the only error source is `flag.FlagSet.Parse(pyryArgs)`, which fires today for `-pyry-name` with a missing value or `-pyry-socket=` without a path. Same behaviour, just narrower input.
- `runStatus` / `runLogs` / `runStop` produce a plain `fmt.Errorf` for unexpected `rest`. The `main.go` top-level handler prints these as `pyry: <err>` and exits 1 — same shape as every other CLI error today.
- `runAttach` already produces a usage line + `os.Exit(2)` on `parseAttachArgs` error (`cmd/pyry/main.go:605-609`); that path now becomes reachable for `--stdio` / `--create-if-missing`, which is the whole point of the ticket.

## Testing strategy

### Update existing tests

`cmd/pyry/args_test.go`:

1. **`TestParseClientFlags` — replace the `unknown flag returns error` subtest** (lines 60-65). New contract: unknown flags pass through to `rest`. Replace with:
   ```go
   t.Run("unknown flag flows to rest", func(t *testing.T) {
       _, rest, err := parseClientFlags("pyry status", []string{"-unknown"})
       if err != nil {
           t.Fatalf("parseClientFlags: %v", err)
       }
       if len(rest) != 1 || rest[0] != "-unknown" {
           t.Errorf("rest = %v, want [-unknown]", rest)
       }
   })
   ```

2. **Extend `TestParseClientFlags_ReturnsRest`** (lines 71-105) with new cases that cover the bug's shape:
   - `"sub-verb flag passes through"`: `[]string{"--stdio"}` → `["--stdio"]`
   - `"sub-verb flag plus positional"`: `[]string{"--stdio", "abc"}` → `["--stdio", "abc"]`
   - `"-pyry-socket then sub-verb flag"`: `[]string{"-pyry-socket=/tmp/x", "--stdio", "abc"}` → `["--stdio", "abc"]`
   - `"-pyry-name space-separated then sub-verb flag"`: `[]string{"-pyry-name", "elli", "--stdio", "abc"}` → `["--stdio", "abc"]`
   - `"double-dash pyry flag form"`: `[]string{"--pyry-socket", "/tmp/x", "--create-if-missing", "abc"}` → `["--create-if-missing", "abc"]`
   - `"-- separator passes through verbatim"`: `[]string{"-pyry-name", "elli", "--", "abc"}` → `["--", "abc"]`

### New tests

`cmd/pyry/args_test.go`:

3. **`TestSplitClientFlags`** — table-driven, mirrors `TestSplitArgs`'s shape (lines 199-312). Cases:
   - empty / nil → both nil
   - only sub-verb flag: `["--stdio"]` → pyry=nil, rest=`["--stdio"]`
   - only positional: `["abc"]` → pyry=nil, rest=`["abc"]`
   - `-pyry-name` separate value: `["-pyry-name", "elli"]` → pyry=`["-pyry-name", "elli"]`, rest=nil
   - `-pyry-name=elli` glued: `["-pyry-name=elli"]` → pyry=`["-pyry-name=elli"]`, rest=nil
   - `--pyry-socket=/tmp/x` (double-dash + glued): `["--pyry-socket=/tmp/x"]` → pyry=`["--pyry-socket=/tmp/x"]`, rest=nil
   - `-pyry-socket /tmp/x` separate value: `["-pyry-socket", "/tmp/x"]` → pyry=`["-pyry-socket", "/tmp/x"]`, rest=nil
   - mixed: `["-pyry-socket", "/tmp/x", "--stdio", "abc"]` → pyry=`["-pyry-socket", "/tmp/x"]`, rest=`["--stdio", "abc"]`
   - sub-verb flag first short-circuits: `["--stdio", "-pyry-name", "elli"]` → pyry=nil, rest=`["--stdio", "-pyry-name", "elli"]`. **This is intentional** — verb flags must precede pyry flags is not a valid ordering; the convention is the inverse. The test pins the rule.
   - `--` separator: `["-pyry-name", "elli", "--", "abc"]` → pyry=`["-pyry-name", "elli"]`, rest=`["--", "abc"]`
   - `--` first: `["--", "-pyry-name", "elli"]` → pyry=nil, rest=`["--", "-pyry-name", "elli"]`
   - trailing `-pyry-name` with no value: `["-pyry-name"]` → pyry=`["-pyry-name"]`, rest=nil. (FlagSet downstream errors on it; that error path is covered by `TestParseClientFlags` already.)

4. **`TestRunAttachArgPath`** — the AC-mandated test: drives the full `runAttach` arg-parse composition (`parseClientFlags` → `parseAttachArgs`) and asserts the dispatch tuple. **This test is the regression guard for the bug**; without it, the e2e test in `internal/e2e/attach_stdio_test.go` is the only thing that catches a regression, which is exactly the gap the AC names.

   Shape (uses `t.Setenv("PYRY_NAME", "")` because it derives socket paths from `defaultName()`; therefore not `t.Parallel()`):

   ```go
   func TestRunAttachArgPath(t *testing.T) {
       t.Setenv("PYRY_NAME", "")

       tests := []struct {
           name                string
           args                []string
           wantSocketBase      string
           wantSocketExplicit  string // non-empty → exact match (overrides Base)
           wantSel             string
           wantStdio           bool
           wantCreateIfMissing bool
       }{
           {"--stdio plus id (the bug shape)",
            []string{"--stdio", "some-id"},
            "pyry.sock", "", "some-id", true, false},
           {"-pyry-socket=… then --stdio plus id (also the bug shape)",
            []string{"-pyry-socket=/tmp/foo", "--stdio", "some-id"},
            "", "/tmp/foo", "some-id", true, false},
           {"-pyry-socket space-separated, --stdio, id",
            []string{"-pyry-socket", "/tmp/foo", "--stdio", "some-id"},
            "", "/tmp/foo", "some-id", true, false},
           {"--create-if-missing alone reaches parser",
            []string{"--create-if-missing", "some-id"},
            "pyry.sock", "", "some-id", false, true},
           {"--stdio --create-if-missing plus id (SDK shape)",
            []string{"--stdio", "--create-if-missing", "some-id"},
            "pyry.sock", "", "some-id", true, true},
           {"-pyry-name then --stdio composes",
            []string{"-pyry-name", "elli", "--stdio", "some-id"},
            "elli.sock", "", "some-id", true, false},
           {"no sub-verb flags: bare id still works",
            []string{"some-id"},
            "pyry.sock", "", "some-id", false, false},
           {"no args at all: bootstrap shape",
            nil, "pyry.sock", "", "", false, false},
       }

       for _, tt := range tests {
           t.Run(tt.name, func(t *testing.T) {
               socketPath, rest, err := parseClientFlags("pyry attach", tt.args)
               if err != nil {
                   t.Fatalf("parseClientFlags: %v", err)
               }
               sel, stdio, cim, err := parseAttachArgs(rest)
               if err != nil {
                   t.Fatalf("parseAttachArgs: %v", err)
               }
               if tt.wantSocketExplicit != "" {
                   if socketPath != tt.wantSocketExplicit {
                       t.Errorf("socket = %q, want %q", socketPath, tt.wantSocketExplicit)
                   }
               } else if filepath.Base(socketPath) != tt.wantSocketBase {
                   t.Errorf("socket basename = %q, want %q", filepath.Base(socketPath), tt.wantSocketBase)
               }
               if sel != tt.wantSel {
                   t.Errorf("selector = %q, want %q", sel, tt.wantSel)
               }
               if stdio != tt.wantStdio {
                   t.Errorf("stdio = %v, want %v", stdio, tt.wantStdio)
               }
               if cim != tt.wantCreateIfMissing {
                   t.Errorf("createIfMissing = %v, want %v", cim, tt.wantCreateIfMissing)
               }
           })
       }
   }
   ```

5. **`TestRunStatus_RejectsExtraArgs`** (and one each for logs, stop) — small unit asserting the post-check. Inline `runStatus` doesn't expose a pure parser, but the post-check is a one-liner; pin its presence with a single call:
   ```go
   func TestRunStatus_RejectsExtraArgs(t *testing.T) {
       t.Setenv("PYRY_NAME", "")
       err := runStatus([]string{"-unknown"})
       if err == nil {
           t.Fatal("expected error on extra arg")
       }
       if !strings.Contains(err.Error(), "unexpected arguments") {
           t.Errorf("err = %v, want substring 'unexpected arguments'", err)
       }
   }
   ```
   `runStatus` will dial the socket only after the rest-check, and the dial against `~/.pyry/pyry.sock` (almost certainly nonexistent in test) produces a different error class. Be defensive: assert the error IS the rest-check error specifically (substring match) rather than any error. Mirror the same shape for `runLogs` (`"unexpected arguments"`) and `runStop` (same).

   Alternative if the dial-vs-rest-check ordering is fragile in practice: extract a tiny pure helper `errIfExtraArgs(verb string, rest []string) error` and unit-test that directly. The architect's preferred approach is the inline check (three sites; helper would be premature) — but the developer can switch to the helper if the test ordering bites. **Document the choice in the commit message either way.**

### E2E removal

`internal/e2e/attach_stdio_test.go`: delete lines 27-31 (the `// Blocked on #167…` comment block plus `t.Skip`). Everything else in the test stays. Run with the e2e build tag:

```
go test -race -tags e2e ./internal/e2e/...
```

The test should pass against the fixed binary. If it doesn't, the failure points at the harness (`startStdioAttach`), not at this ticket — surface it as a finding rather than fixing here.

### Verification commands

```
go test -race ./cmd/pyry/...
go test -race -tags e2e ./internal/e2e/...
go vet ./...
staticcheck ./...
```

All four must pass. The unit tests in `cmd/pyry/...` are the regression guard the AC requires; the e2e run proves the skip-removal landed correctly.

## Open questions

None blocking. Two small judgement calls left to the developer:

1. **Three-site `len(rest) > 0` post-check** — keep inline, or extract `errIfExtraArgs(verb, rest)`. Architect's preference: inline (three sites, tiny pattern, no abstraction value). Switch to the helper only if `runStatus` / `runLogs` / `runStop` post-check tests turn out fragile (see Testing § 5).

2. **`runStop` error wording** — runStop's current convention prints "pyry: stop requested" on success. The rejection-of-extra-args path is new; align its `fmt.Errorf` shape with `runStatus`/`runLogs`. (`stop: unexpected arguments: …`.)
