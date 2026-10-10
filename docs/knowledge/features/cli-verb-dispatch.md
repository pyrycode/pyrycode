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

## Conversation option and selector parsing

Go's `flag.FlagSet` accepts a following flag token as a string value. A guard
in `parseConversationNewArgs` or `parseConversationPostArgs` alone cannot catch
missing values for the leading instance/socket selectors: `parseClientFlags`
runs first and can consume
`-pyry-socket` as the value of `-pyry-name`, then attempt transport with exit 1
instead of the required syntax exit 2. `runConversation` checks leading
selectors before shared parsing, skips their separate or inline values, and
leaves conversation options to their own parser. Dash-prefixed values require
`=value`; selectors precede `new` or `post`.

`TestConversationNew_E2E_MissingSelectorValues` runs the CLI with no daemon
listening and requires exit 2, usage on stderr and empty stdout. This makes an
accidental transport attempt distinguishable from syntax rejection; a generic
nonzero-exit assertion would stay green with the bug. Keep successful selector
cases too: separate/inline values, an empty instance name and explicit socket
precedence must still reach the intended daemon. See the
[conversation creation contract](control-plane.md#conversation-create-conversationnew)
and [CLI spec](../../specs/architecture/2884-conversation-cli.md#revisions).

## Daemon shutdown and exit status

An ordinary SIGTERM, SIGINT, or `pyry stop` makes `runSupervisor` log
`pyrycode stopped` at INFO and return nil, so the daemon exits 0. A persistent
relay 4409 conflict instead logs `pyrycode fatal shutdown` at ERROR and returns
the conflict cause; `main` propagates the error as exit 1. This lets service
managers distinguish a planned stop from a failure. Launchd's
`KeepAlive` with `SuccessfulExit:false` restarts the fatal path; systemd's
restart policy remains controlled by the unit configuration.

`runSupervisor` derives a `context.WithCancelCause` context from the
`signal.NotifyContext` parent. The control stop calls `cancelCause(nil)`, which
records `context.Canceled`; an OS signal can carry a signal-specific error
instead. `fatalCause(ctx, sigCtx)` accepts nil, `context.Canceled`, and a cause
matching `context.Cause(sigCtx)` as clean stops. Checking only `sigCtx.Err()`
would lose an earlier fatal cause when a signal arrives during shutdown:
the first cancellation cause wins, including over cleanup's `cancelCause(nil)`.

See [relay supervisor wiring](relay-package.md#consumers-and-roadmap) for fatal
error propagation and [verification guidance](development-verification.md#prove-that-tests-distinguish-the-change)
for the real-signal regression pattern.

## Memory credential persistence

Updating a token in place before saving selection would change what an existing
reference resolves to even if the metadata save then failed. `memoryCredentialStore.set`
writes an immutable token generation and staged selection, syncs and closes
both, then commits with one atomic selection rename. The reference stays stable;
only the selected generation changes. Readers hold the same directory lock as
writers through resolution, so post-commit cleanup cannot remove a generation
while a reader still needs it. `TestMemoryCredentialFailedSave` injects a rename
failure and checks both unchanged selection bytes and resolution of the old
token; checking only the returned error would miss an in-place token overwrite.

Non-secret metadata still controls access to a secret. `memorySelected` requires
canonical file-backend selection and valid identifier formats, while
`memoryCredentialStore.resolve` requires equality with the committed reference.
Pinned no-follow directory traversal and opened-descriptor checks protect both
selection and token files. This applies the [selection trust rule](claude-account-source.md#the-selection-file-needed-the-same-trust-check-as-the-secret-it-points-to)
without allowing memory references to redirect to paths or Claude login entries.

The [Claude file reader's before/after context checks](claude-account-source.md#the-ten-second-bound-covers-reads-that-return-not-reads-that-hang)
alone cannot bound a stdin read waiting for EOF. `readMemoryInput` polls the
descriptor and checks context without a detached reader; directory lock waits
also check context. Saves remain synchronous and check context before publication,
so a cancelled operation leaves no worker that can publish later.
`TestMemoryCredentialCancellation` blocks stdin or the lock, then verifies the
old token still resolves after release. Resolution in a fresh process in
`TestMemoryCredentialProcess` prevents an in-memory cache from masking broken
persistence. See the [command contract](../../guide.md#memory-credentials),
[service-user storage requirements](../../deployment.md#memory-credentials) and
[design](../../specs/architecture/3112-memory-credentials.md).

## Memory configuration and effective roots

`runMemory` retains credential dispatch and routes configure/status to
`runMemoryConfiguration`. Callable `configureMemory`, `memoryStatus` and
`resolveEffectiveMemory` share `validateMemorySettings`; config parsing and atomic
replacement remain in the [config package](config-package.md#surface). Status
projects an explicit embedding shape with a freshly validated boolean instead
of serializing the saved embedding struct: a reference is non-secret metadata,
but returning that struct would still disclose it. Parser/load/save diagnostics
remain static so rejected arguments and malformed JSON cannot echo credentials.
See the [command contract](../../guide.md#memory-configuration).

### Reserved storage and index inputs

Derived roots need credential exclusions just as user-supplied roots do. A safe
vault alone cannot keep credentials out of `SearchRoots`: a `recent-transcripts`
symlink can point at credentials or an ancestor/descendant of them. `memoryReserved`
rejects that overlap before configure, status or effective resolution succeeds.
`TestMemoryTranscriptCredentialOverlap` changes the alias after an initial save,
then checks empty failed resolution, no status JSON and unchanged config bytes.

Absent reserved leaves still need ancestry checks. `memoryReservedPath` peels
missing components with `os.Lstat`, resolves the existing ancestor and requires
a directory before restoring the suffix. A following stat would mistake a
dangling symlink for a directory that has not been created yet, accepting an
unresolved boundary. This operation creates no transcript or credential storage.

### Path identity and normalization

`filepath.EvalSymlinks` can retain caller casing on case-insensitive volumes;
lexical exclusions and deduplication alone can therefore miss aliases to the same
directory. `memoryDirectory` and `memoryReservedPath` use
[`canonicalpath.Resolve`](canonicalpath-package.md#path-and-error-contract), whose
casing probes are best-effort. Execute-only ancestors can prevent those probes
from listing directory entries, so `memoryContains` also compares ancestor
filesystem identities with `os.SameFile` when component ancestry does not match.
Lowercasing all paths would wrongly collapse distinct case-sensitive siblings.

`normalizeMemoryRoots` retains containing parents and preserves siblings such as
`notes` and `notes-old`. `resolveEffectiveMemory` normalizes the search union while
keeping vault destination and transcript ownership independent: a read-only
parent root can subsume a vault subtree for indexing without becoming its write
destination. `TestMemoryRootFilesystemIdentity` exercises identity fallback on
every filesystem; `TestMemoryPathsCaseSensitiveSiblings` and
`TestMemoryPathsCaseInsensitive` cover their respective filesystem behavior and
skip when that prerequisite is absent. A skipped casing test does not establish
the case-insensitive boundary.

## Related

- [config-package.md](config-package.md) — `interactive_runner`'s `selectInteractiveRunner`, the loud-removal precedent this switch's arms mirror.
- [control-plane.md](control-plane.md) — server-side deletion of the attach/resize wire surface (#1535) the `attach` verb used to ride.
- [acp-package.md](acp-package.md) — the ACP JSON-RPC-over-stdio transport the `acp` verb used to serve.
- Spec [`docs/specs/architecture/1493-removed-verb-loud-error.md`](../../specs/architecture/1493-removed-verb-loud-error.md) — the `runArgs`/`helpText` seam design and full message-contract rationale.
