# `cmd/pyry`'s verb dispatch (`run`/`runArgs`) and `printHelp`

`run()` is a one-line wrapper (`return runArgs(os.Args)`) around `runArgs(args []string)`, which switches on `args[1]`. The switch deliberately has **no `default` arm**: an unrecognised first argument falls through to `runSupervisor(args[1:])`, and `splitArgs` tips it into claude's initial prompt. That fall-through is the package doc's stated design — pyry is "a near-drop-in replacement for `claude`" — not an oversight, so a future change must not add a catch-all `default` case to this switch.

`printHelp` prints a package-level `const helpText` (extracted from an inline `fmt.Print` literal by #1493) rather than building the string inline, so a test can read the advertised verb list without capturing `os.Stdout`.

## Removed-verb convention: loud sentinel, no `default`

A verb whose implementation is deleted (`attach`/`acp`, #1348) gets an **explicit `case` in the switch returning a sentinel error** (`errAttachRemoved`, `errACPRemoved`, #1493) — never a pre-switch guard, and never folded into a `default`. Two reasons, not just style:

- A `default` arm would break the near-drop-in fall-through above.
- A duplicate `case` constant is a **compile error** in Go. If a future ticket tries to revive a removed verb, or picks a new verb name that collides with a still-reserved one, the build fails instead of one case silently shadowing the other.

The message shape mirrors `internal/config`'s `interactive_runner` selector — see [config-package.md](config-package.md), whose `selectInteractiveRunner` `case "pty"` arm established the pattern first: name what was removed, name the ticket, never render the dead thing as something the reader can copy and run. `errAttachRemoved`/`errACPRemoved` additionally state the replacement (or that there isn't one) rather than leaving the reader to guess.

Server-side, `internal/control` deleted the wire surface these verbs rode (`VerbAttach`/`VerbResize`, #1535) — see [control-plane.md](control-plane.md). This doc covers the CLI-side advertising and dispatch only; the two removals happened in different tickets and can drift independently.

## Testing patterns for this switch

`cmd/pyry` had no test on `run`/`printHelp` before #1493 — `os.Args` and `os.Stdout` gave neither an injectable seam. Two things worth reusing for the next change here:

- **Proving a removed-verb arm's absence is caught before it does damage, not after.** Deleting an arm makes `runArgs` fall through to `runSupervisor`, whose first substantive step (`confineWorkdirToHome`) rejects a cwd outside `$HOME`. A test that wants "arm deleted ⇒ red, with zero side effects" needs `t.Setenv("HOME", t.TempDir())` even though that line does nothing in the passing, shipped-code run — it only fires once the arm is mutated away. An unexplained line like that reads as dead code to the next editor; comment why it stays.
- **A help-text absence check must not be a bare substring match.** `strings.Contains(helpText, "acp")` is vacuously true forever, because the surviving `pyry mcp-approve` entry contains the letters `acp`. Split each line on whitespace and match the token that follows a leading `pyry` field instead — and assert a couple of *surviving* entries are still found first, so a broken predicate can't report an absence unconditionally.

## Related

- [config-package.md](config-package.md) — `interactive_runner`'s `selectInteractiveRunner`, the loud-removal precedent this switch's arms mirror.
- [control-plane.md](control-plane.md) — server-side deletion of the attach/resize wire surface (#1535) the `attach` verb used to ride.
- [acp-package.md](acp-package.md) — the ACP JSON-RPC-over-stdio transport the `acp` verb used to serve.
- Spec [`docs/specs/architecture/1493-removed-verb-loud-error.md`](../../specs/architecture/1493-removed-verb-loud-error.md) — the `runArgs`/`helpText` seam design and full message-contract rationale.
