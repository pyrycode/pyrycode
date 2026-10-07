# Inbound `read_workspace_file` (#2598) — `WorkspaceFileRead` seam

`read_workspace_file` answers `handleRequestAttachment`'s own vocabulary —
an `attachment_chunk` stream correlated by `in_reply_to`, or one
`attachment.not_found` / `attachment.stream_aborted` — over bytes that never
touch the attachment store. It is
[`request_attachment`](v2-session-manager-state-machine-inbound-request-attachment-attachmentresolve.md)'s
twin in every step and every answer; what differs is only where the bytes come
from, and that difference is the whole ticket. See
[`docs/specs/architecture/2598-read-workspace-file.md`](../../specs/architecture/2598-read-workspace-file.md).

## The first path a client, not the model, names

`internal/control`'s `attachment.file` verb (#2164,
[control-plane-attachment-file-confine-and-store-a-claude-named-path.md](control-plane-attachment-file-confine-and-store-a-claude-named-path.md))
was the first wire path whose filesystem path is chosen by the **model**. This
is the first whose path is chosen by a **paired client** — a different
attacker, reachable without `claude` in the loop at all. The confinement
primitives are identical (`confineFile`/`readChecked` in `cmd/pyry/attach_file.go`,
reused unmodified per the #2164 rule: do the containment work once, in
`cmd/pyry`, never re-derive it in `internal/relay`), but this verb adds a
second gate `attachment.file` does not need, because `attachment.file`'s caller
is already the same trust tier as the workspace's owner: **a secret-name
denylist**, checked on both the requested and resolved leaves.

## The secret-name rule is checked twice, and neither check is redundant

`workspaceFileReader` (`cmd/pyry/workspace_file.go`) checks `isSecretName` on
the **requested** leaf first, before the registry or the filesystem is
touched — so a request for `.env` never reaches a `Stat`. It checks the same
predicate again on the **resolved** leaf, after `confineToAnyRoot` has followed
symlinks and proved containment. Since #2893, any regular file type is eligible:
markdown, scripts, archives, extensionless and binary files all use the same
bounded read.

The case-insensitive denylist refuses exact `.env`, any name beginning `.env.`,
exact `id_rsa`, `id_dsa`, `id_ecdsa`, `id_ed25519`, and any name ending in `.key`,
`.pem`, `.p12`, `.pfx`, `.keychain` or `.keychain-db`. `.env.example` is
deliberately denied; `.gitignore`, `.envoy` and `id_rsa.pub` remain eligible.
Only the leaf is checked, not parent-directory names or file contents. This is
a filename heuristic: allowed files, including archives, may still contain
secrets.

Containment (`withinDir`) cannot replace either check. An eligible `notes.py`
symlink to `.env` inside the same root passes containment and only the resolved
check refuses it. A denied `private.key` symlink to an eligible `notes.py` must
also be refused, which the resolved check alone would miss. Both checks apply
in the workspace, configured folders and admitted working folder.

This is the same shape as `attach_file`'s TOCTOU discipline (check, then open
with `O_NOFOLLOW|O_NONBLOCK`, then `os.SameFile` against the pre-open stat) —
**a check has to run against the thing that will actually be read, not just the
thing the caller named** — applied one level up, to the file's *name* rather
than its *identity*.

## `streamAttachmentBytes`: the same chunker, fed bytes instead of a path

`StreamAttachment` (`internal/relay/v2attachmentstream.go`) used to open its
own file by path with `os.ReadFile` — fine for `request_attachment`, whose
`path` comes from `attachments.ResolvePath` and names a file already proven
regular. It would be wrong here: `workspaceFileReader` already produced the
bytes through `readChecked`'s descriptor-identity check (`O_NOFOLLOW`,
`O_NONBLOCK`, `os.SameFile` against the pre-open stat), and re-opening the same
path by name would throw that proof away and reintroduce the swap window
`readChecked` exists to close. The fix is not a second stream implementation:
`StreamAttachment` was split into a thin read-then-call wrapper and
`streamAttachmentBytes(ctx, connID, attachmentID, filename string, blob []byte,
inReplyTo uint64) error`, which both callers now share — `attachmentEnvelopes`
+ the `Push` loop + the one debug log line, unchanged. The bytes are streamed
unchanged, with the resolved basename, a fresh transfer UUID and
`http.DetectContentType` MIME sniffed from bytes, regardless of extension.
There is no text conversion or inline/download discriminator; presentation
belongs to the client. `readChecked` retains the supplied `maxBytes` bound
(production `maxAttachFileBytes`, 16 MiB): exactly the bound is admitted and
one byte over is refused. **When a second caller
needs a function's tail but has already done its own version of the function's
head more safely, split the function at that boundary rather than duplicating
the tail or calling the head again.**

## Comma-ok, once more, and the field it costs

`V2SessionConfig.WorkspaceFileRead func(conversationID, path string)
(WorkspaceFile, bool)` mirrors `AttachmentResolve`'s shape for the identical
reason: every refusal cause — denied name (either leaf), a registry miss,
no admitting root, a missing or non-regular file, a checked-read failure,
over the size bound — collapses into the same `false` inside
`workspaceFileReader`, so `handleReadWorkspaceFile` cannot branch on what the
wire's one `attachment.not_found` must not distinguish, and never holds a
`confineFile`/`readChecked` error that could print a host path. The cost is the
same one `AttachmentResolve`'s doc block already names: the seam discharges no
registry check of its own — `KnownConversation` is what the handler consults
before the conversation id ever reaches this seam, and the registry read inside
`workspaceFileReader` exists only to fetch that conversation's current `Cwd`,
not to re-validate membership.

## `Cwd` read at call time, not at wiring time

`workspaceFileReader` calls `convReg.Get` inside the closure it returns, not
once when `cmd/pyry/relay.go` builds the `V2SessionConfig` literal — the same
rule `fileAttacher` already follows for `attach_file`. A `change_workspace`
between two `read_workspace_file` requests is visible on the very next one, with
no reconnect and no re-wiring. An empty `Cwd` is skipped rather than resolved,
because `confineFile("", path)` would otherwise canonicalise against the
daemon's own process directory — the daemon's own layout, not any conversation's
workspace. Absolute paths can still be admitted by an extra root when `Cwd`
is empty or unresolvable; relative paths cannot.

## Operator-named folders widen the absolute case only (#2710)

The operator can name extra absolute folders on the daemon command line with a
repeatable `-pyry-read-folder` flag, such as an Obsidian vault most of the
links `claude` sends point into. `resolveReadFolders` (`cmd/pyry/workspace_file.go`)
canonicalises the list **once, at daemon startup**, with the same
[`canonicalpath.Resolve`](canonicalpath-package.md#path-and-error-contract)
recipe `confineFile` applies to the workspace. An
entry is skipped with one `slog.Warn` — naming the operator's own entry and a
static reason, never `canonicalpath.Resolve`'s own path-bearing error — when it is
not absolute, does not resolve, or does not name a directory; the daemon
still starts. The directory check is not in the confinement recipe `confineFile`
already had: a configured **file** resolves fine through `canonicalpath.Resolve`, and
`withinDir(file, file)` is true, so without it a mistyped entry would quietly
grant that one file rather than being skipped.

`workspaceFileReader` tries the workspace first, then each resolved folder in
order (`confineToAnyRoot`), and only for a path that is already absolute — a
relative path never reaches the folder loop, so it still resolves against the
workspace only. Every existing rule (both-leaf secret-name check, regular-file-only,
`readChecked`'s identity proof, the single undistinguished `false`) applies
inside a folder exactly as inside the workspace, because a folder is confined
with the same `confineToRoot` helper `confineFile` now delegates to.

**A folder root is resolved once and never re-resolved per request.**
`confineFile` was split into itself (resolve `root`, then delegate) and
`confineToRoot(canonicalRoot, path)`, which assumes its root is already
canonical. A configured folder goes straight to `confineToRoot` with the
startup-resolved path. Re-resolving a folder on every request, the way a
workspace root is resolved, would have reopened the swap window the split
exists to close: an operator's folder whose path component is later replaced
by a symlink would move the boundary on the next request if the root were
canonicalised again then. The resolved list is handed to the reader through
`relayWiring.readFolders` and never mutated afterwards, so no lock is needed.
[`#2711`](https://github.com/pyrycode/pyrycode/issues/2711) reads this same
resolved list to name the folders in the session prompt, which is why the
list is resolved once in the daemon's composition root rather than inside the
reader.

Configured-folder result and refusal tests do not replace a root after startup.
`TestConfineFile_SwapBetweenCheckAndRead` proves checked-file identity, a different
boundary; it would stay green if folder roots were re-resolved per request.
For startup-root stability, check that `confineToAnyRoot` passes the stored root
directly to `confineToRoot`, which resolves only the target.

`fileAttacher` (the `attach_file` verb) never sees the folder list — its
signature takes none, and `confineFile`'s own behaviour and callers are
unchanged by the split — so it keeps its workspace-only rule regardless of
what is configured.

## The daemon's own working folder is always a read root too (#2720)

`withWorkdirReadFolder` (`cmd/pyry/workspace_file.go`) runs once in
`runSupervisor`, after `resolveReadFolders`, and prepends the daemon's own
working folder (`-pyry-workdir`, or the process directory when that is unset
— `workdirReal`) to the same slice, canonicalised with the same
`canonicalpath.Resolve` recipe. This is what lets a client open
`BEHAVIOR.md`, `FEEDBACK.md` and the rest from any conversation, including
one whose workspace is a subfolder of the working folder, with no
`-pyry-read-folder` set. The folder enters the reader exactly like a
configured one — same `confineToAnyRoot`, same both-leaf secret-name check, same
`readChecked` identity proof, same undistinguished `false` — because it is
folded into the one slice both consumers read; no reader code changed.

**The guard excludes home, not just the exact match.** The working folder is
left out, and one `slog.Info` line names it with a static reason (never a
client-named path), when it resolves to the operator's home folder, to `/`,
or to any folder that *contains* home (`withinDir(resolved, homeReal)`) —
otherwise starting the daemon from `$HOME` would expose every eligible file
in it. `confineWorkdirToHome` already refuses a working folder outside home
earlier in `runSupervisor`, so only the exact-home case is reachable in the
daemon as shipped; the broader "contains home" check holds on its own rather
than depending on that call order, per the plan's security review. An
unresolvable home (including an empty one, since `canonicalpath.Resolve("")`
would otherwise mean the process directory) also leaves the folder out. Both
sides are compared as realpaths, so a symlinked home or working folder is
still caught.

**Dedup, not a second entry.** When the canonical working folder is already
in the configured list — including through a symlinked spelling, since both
go through the same resolution — `withWorkdirReadFolder` returns the slice
unchanged rather than adding a duplicate. This is also what keeps the #2711
prompt sentence (below) naming the folder once.

`fileAttacher` does not take the folder list (see above), so `attach_file`
still refuses a file in the working folder from a conversation whose
workspace is elsewhere — the extra roots apply only to the live file reader.

## Testing the filename checks

A generic refusal with a valid registry cannot prove the requested-name check
runs before lookup: moving the check later leaves the same `false`.
`TestWorkspaceFileReader_SecretNames` also calls the reader with a nil registry
and a denied requested leaf, making an incorrectly ordered `Registry.Get`
observable. Direct refusals and both symlink directions prove the two checks
independently; case variants use distinct `-lower-case` and `-upper-case`
fixture directories so macOS's case-insensitive filesystem cannot alias them.
See [verification practices](development-verification.md#prove-that-tests-distinguish-the-change).

## Related

- [Inbound `request_attachment` (#2054)](v2-session-manager-state-machine-inbound-request-attachment-attachmentresolve.md) — the handler this one copies step for step: nil-seam-inert, reject-not-tolerate decode, the `KnownConversation`-before-path-component ordering, and the two-code (`not_found` vs. `stream_aborted`) partition on the stream error's identity.
- [Outbound attachment stream (#2053)](v2-session-manager-state-machine-outbound-attachment-stream-streamattachm.md) — `attachmentEnvelopes` + the `Push` loop, now factored as `streamAttachmentBytes` and shared by both callers.
- [`attachment.file`: confine and store a claude-named path (#2164)](control-plane-attachment-file-confine-and-store-a-claude-named-path.md) — the first model-named path on this daemon, and the `confineFile`/`readChecked` primitives this verb reuses unmodified.
- [Attachment envelope types § `read_workspace_file`](protocol-package-constants-codes-go-envelope-types-attachments.md) — `TypeReadWorkspaceFile`, `ReadWorkspaceFilePayload`, and the wire vocabulary this handler answers.
- [Mobile protocol § `read_workspace_file`](../../protocol-mobile.md#read_workspace_file) — the published client contract: the two fields, the secret-name rule on both leaves, admitted roots, the live-read (no stored copy) semantics, and the single undistinguished refusal.
