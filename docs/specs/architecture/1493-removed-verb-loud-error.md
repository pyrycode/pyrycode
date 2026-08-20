# #1493 — Loud #1348-removal errors for `attach` and `acp`

**Size:** XS (confirmed; PO's `size:xs` stands). One production file, one new test file.
**Security-sensitive:** no label, so no § Security review pass.

## Files to read first

| Read | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/main.go` | `run` | The whole dispatch switch. This is the function being split; note that every arm returns and that there is **no `default`** — that absence is AC 4 and must survive. |
| `cmd/pyry/main.go` | `runSupervisor` | Read only as far as `trustMark`. It calls `confineWorkdirToHome` then `trustMark` **before** the control socket, the relay, or any claude spawn. This is the side-effect chain AC 1 forbids, and the ordering the new test's red-state guard relies on. |
| `cmd/pyry/main.go` | `printHelp` | The raw string literal, in full. You will extract it and delete three regions of it. |
| `cmd/pyry/main.go` | `selectInteractiveRunner` | The `case "pty"` arm — the exact message shape this ticket mirrors: `<thing> was removed in #1348: <what is gone>. <what to do instead>`. |
| `cmd/pyry/interactive_runner_test.go` | `TestSelectInteractiveRunner`, subtest `"pty is rejected, and the error says it was removed"` | The assertion set to mirror: error is non-nil, message carries `#1348`, and a **negative** assertion that the message does not hand the removed thing back as usable. |
| `cmd/pyry/sessions_test.go` | `TestRunSessions_UnknownVerb` | The house style for a verb-level error test (`strings.Contains` on `err.Error()`, one `t.Errorf` per fragment). Its "verified structurally" note is what this ticket deliberately upgrades — see § Why the seam. |
| `cmd/pyry/main.go` | `confineWorkdirToHome` | Its `$HOME` containment reject. Why `t.Setenv("HOME", t.TempDir())` makes the AC-5 red state harmless. |
| `cmd/pyry/jsonrpc_stdio.go` | `serveJSONRPCStdio` | Its doc comment's #1348 paragraph — the record of what the `acp` verb's deletion actually removed, and the source of the `acp` message's wording. |
| `README.md` | the paragraph beginning "The supervised `claude` has no terminal of its own" | The repo's own already-correct answer for what replaced `attach` — it ends "Watch a live session from the desktop or mobile client instead." The `attach` message reuses that. |

## Context

#1348 deleted the `attach` and `acp` verbs. It did not delete the advertising: the
`cmd/pyry` package doc still lists both under "Reserved control verbs", and `printHelp`
still prints a usage block for each plus an example line for `attach`.

`run`'s switch has no arm for either, and no `default` arm, so both fall through to
`runSupervisor`. `splitArgs` tips the first unrecognised non-`-pyry-*` token into claude's
argument list, so the verb string arrives as claude's initial prompt — and before that,
`runSupervisor` has already auto-trust-marked the cwd in `~/.claude.json` via `trustMark`,
bound the control socket, and contacted the relay. An operator who reads `pyry help` and
types `pyry attach` gets a full daemon.

This ticket is the CLI surface only. The stale prose in `docs/knowledge/features/control-plane.md`,
`docs/knowledge/features/acp-package.md` and `docs/knowledge/architecture/system-overview.md`,
and the comments that still name #1348-deleted functions (in `resolveSessionIDViaList`, in
`serveJSONRPCStdio`, in `runMCPApprove`, and in `args_test.go`'s
`TestParseClientFlags_ReturnsRest` doc comment) are explicitly out of scope and want their
own ticket.

**No ADR needed.** The `interactive_runner "pty"` arm in `selectInteractiveRunner` already
established the loud-removal pattern; this applies it to a second surface rather than
deciding anything new.

## Design

All production changes land in `cmd/pyry/main.go`. Nothing outside `cmd/pyry` moves, and
nothing exported is added.

### 1. Two sentinel errors

Declare them next to `run`, in a `var (...)` block with one shared doc comment:

```go
var (
	errAttachRemoved = errors.New(...)
	errACPRemoved    = errors.New(...)
)
```

`errors` is already imported. Sentinels rather than `fmt.Errorf` at the call site so the
test can assert identity with `errors.Is` instead of matching on prose, per CODING-STYLE's
"never compare error strings".

**Message contract.** Shape mirrors the `case "pty"` arm in `selectInteractiveRunner`:
`<verb> was removed in #1348: <what is gone>. <what to do instead>`. Both messages are
rendered by `main` as `pyry: <message>`, so they start lowercase with the bare verb and
read correctly after that prefix.

- `errAttachRemoved` — names `attach` and `#1348`, states that the terminal path it bridged
  no longer exists so there is nothing to attach to, and points at the desktop/mobile client
  as the way to watch a live session. That replacement is not invented here; it is what
  README already tells the reader.
- `errACPRemoved` — names `acp` and `#1348`, states that pyry no longer serves the ACP
  JSON-RPC transport over stdio, and tells the ACP host operator to remove it from the
  host's launch configuration. There is no replacement verb and the message must not
  imply one.

**Both messages must avoid the literal `pyry attach` / `pyry acp`.** That is AC 2's
"does not offer the verb as something to run", and the test asserts it directly. Naming
the verb bare (`attach was removed…`) satisfies "names the verb" without rendering an
invocation the reader can copy.

### 2. `run` splits into `run` + `runArgs`

```go
func run() error { return runArgs(os.Args) }

func runArgs(args []string) error { /* the existing body, args instead of os.Args */ }
```

Mechanically: move `run`'s current body into `runArgs` and replace every `os.Args` inside
it with `args`. Fifteen occurrences — `len(os.Args)`, `switch os.Args[1]`, twelve
`os.Args[2:]`, and the trailing `runSupervisor(os.Args[1:])`.

**Post-condition the developer must check before committing:** `os.Args` appears exactly
once in `runArgs`'s enclosing region — never inside `runArgs` itself, only in the one-line
`run`. A missed substitution still compiles (`os` stays imported) and silently reads the
real process argv, so the compiler will not catch it. Grep the function body.

Nothing else changes about the switch: same arm order, same handlers, still no `default`.

### 3. Two new switch arms

Add to `runArgs`'s switch, grouped together immediately after the `mcp-approve` arm and
before `help`, under one comment explaining that they exist because the fall-through is a
daemon start:

```go
case "attach":
	return errAttachRemoved
case "acp":
	return errACPRemoved
```

Explicit constant cases, not a pre-switch guard and not a `default`. Two reasons:

- A `default` arm would break AC 4 — an unrecognised first argument forwarding to claude
  verbatim is the documented near-drop-in design stated in the package doc's opening lines.
- Duplicate constant cases in a Go expression switch are a **compile error**. So a future
  edit that tries to add `attach` back as a live verb, or that adds a removed-verb string
  that collides with a live one, fails the build rather than silently shadowing a working
  route. That is a deterministic guarantee where a pre-switch guard would need a test to
  hold the same line.

### 4. `printHelp` gains a `const helpText` seam

`printHelp` currently inlines its raw string in `fmt.Print`, with no way for a test to read
it. Extract the literal to a package-level `const helpText = ...` and reduce `printHelp` to
`fmt.Print(helpText)`. The literal contains two `+ "`claude`" +`-style concatenations; both
operands are string constants, so the whole expression is still a valid constant. Keep it
`const`, not `var`.

Then delete three regions from `helpText`:

- the `pyry attach [flags] [--stdio] [<id>]` usage entry **and its six indented
  continuation lines** (the `Ctrl-B d to detach` / `--stdio` parenthetical), which sit
  between the `pyry logs` entry and the `pyry sessions <verb>` entry
- the `pyry acp` usage entry and its two continuation lines, which sit between the
  `pyry agent-run` block and the `pyry mcp-approve` block — leave `mcp-approve` intact
- the `pyry attach` line in the `Examples:` block

### 5. Package doc

Delete the `pyry attach` entry and the two-line `pyry acp` entry from the "Reserved control
verbs" list in the `cmd/pyry` package doc. Delete only; do not add a "removed in #1348"
note — the error message is where an operator learns this, and a doc line naming the verb
is the advertising AC 3 removes.

## Concurrency model

Unchanged. `runArgs` returns before any goroutine, socket, or child process exists on both
new arms — that is the whole point of AC 1.

## Error handling

Both arms return a sentinel; `main` prints `pyry: <message>` to stderr and calls
`os.Exit(1)`. That is AC 1's non-zero exit, reached through the existing path with no new
exit handling.

No wrapping: these errors originate here and have no cause. No `errors.Is` matching in
production either — the sentinels exist for the test and for a future caller that might
want to distinguish them.

## Testing strategy

One new file, `cmd/pyry/removed_verbs_test.go`. **Do not put these in
`dispatch_arms_test.go`** — despite the name it is about `interruptRunner` /
`startFreshRunner`, the *runner* dispatchers, and mixing CLI verb dispatch into it would
make both harder to find.

### `TestRemovedVerbs_RouterRejects` — AC 1, 2, 5

Not parallel; it calls `t.Setenv`. Table over `{"attach", errAttachRemoved}` and
`{"acp", errACPRemoved}`, driving `runArgs([]string{"pyry", verb})`:

- `err != nil` — the non-zero exit AC 1 asks for, since `main` exits 1 on any non-nil return
- `errors.Is(err, want)` — the arm returned *this* sentinel, so the two verbs cannot collapse
  onto one message
- `strings.Contains(err.Error(), verb)` — AC 2 names the verb
- `strings.Contains(err.Error(), "#1348")` — AC 2 names the removal, mirroring the `"pty"`
  subtest's assertion
- `!strings.Contains(err.Error(), "pyry "+verb)` — AC 2's second clause. The message never
  renders the verb as an invocation. Assert against `err.Error()`, not against `main`'s
  rendering: `main` emits `pyry: attach`, which is not the banned `pyry attach`.

**The `t.Setenv("HOME", t.TempDir())` line is load-bearing and needs a comment saying so.**
It is inert in the shipped tree — the arm returns before `runSupervisor` is reached. It
exists for AC 5's red state: with the arm deleted, `runArgs` falls through to
`runSupervisor`, whose first substantive step is `confineWorkdirToHome`. With `HOME`
pointing at a fresh temp dir, the test binary's cwd (the package source directory) is
outside it, so the containment check rejects and `runSupervisor` returns an error *before*
`trustMark` writes `~/.claude.json`, before the socket binds, before the relay is
contacted, and before claude is spawned. The mutant therefore fails on `errors.Is` with no
side effects at all — which is exactly the constraint AC 5 states.

### `TestHelpTextDropsRemovedVerbs` — AC 3

Parallel, no fixtures. Walk `helpText` line by line; for each line take `strings.Fields`
and, where the first field is `pyry`, record the second field in a `map[string]bool`. Then:

- **Controls first.** Assert `status` and `mcp-approve` are both present. Without these the
  scan is vacuous — a broken predicate or an empty `helpText` would report "attach absent"
  unconditionally. `mcp-approve` is the specific control the ticket asks for: it proves the
  check is not a substring match that would also swallow the `acp` in `mcp-approve`.
- Assert `attach` and `acp` are both absent.

Field-splitting rather than `strings.Contains` is deliberate. The `attach` continuation
lines and the `Examples:` entry are all caught by the same predicate, and the surviving
`pyry mcp-approve` and `pyry sessions` entries are not.

The package-doc half of AC 3 is not machine-checkable — a doc comment is not reachable from
a test without parsing the source, which is not worth it for a three-line deletion. It is
verified by review.

### AC 4 — no new test

AC 4 is preserved by construction and does not get one:

- The switch gains no `default`, so a first argument that is not a reserved verb still
  reaches `runSupervisor` and forwards to claude. `TestSplitArgs` already pins the
  forwarding half.
- The other arms are untouched, and the compiler's duplicate-case rule (§ Design 3)
  guarantees the two new constants cannot shadow a live one.

Driving `runArgs` for a live verb would actually execute `runStatus` / `runPair` / etc.
against the developer's environment, which is worse than the gap it would close.

### Gate

`make check`. No new e2e coverage — nothing here touches a daemon path.

## Why the seam

`TestRunSessions_UnknownVerb` establishes the cheaper house pattern: test the handler
directly and note that it "is called from `run()`'s switch and returns before runSupervisor
runs" — the wiring verified structurally, by reading. That pattern is not enough here,
because the regression this ticket exists to prevent *is* the missing wiring. A test that
only inspects sentinel strings stays green when someone deletes the arm, and green is
precisely the state where `pyry attach` starts a daemon again.

So `runArgs` is extracted, and the test drives the router rather than the message. The
ticket's own Technical Notes point at this: it observes that `run` and `printHelp` have no
injectable seam and that "nothing currently pins the fall-through", then leaves the seam to
the architect. Both seams here — `runArgs` and `const helpText` — are the minimum that makes
the fall-through and the advertising testable, and neither adds an exported symbol.

## Open questions

- The `attach` message points at the desktop/mobile client because README already does. If
  the developer finds that client surface has itself changed, keep the sentence factual and
  say so in the PR rather than inventing a replacement — "there is no replacement" is an
  acceptable message, and is what `errACPRemoved` says.
- Placement of the two arms (after `mcp-approve`, before `help`) is a readability call, not
  a contract. Anywhere in the switch behaves identically; keep them adjacent and commented.
