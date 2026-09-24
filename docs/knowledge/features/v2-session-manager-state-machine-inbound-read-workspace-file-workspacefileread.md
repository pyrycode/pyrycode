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
is already the same trust tier as the workspace's owner: **a fixed extension
allowlist**, checked before either primitive runs.

## The markdown rule is checked twice, and neither check is redundant

`workspaceFileReader` (`cmd/pyry/workspace_file.go`) checks `isMarkdownName` on
the **requested** leaf first, before the registry or the filesystem is
touched — so a request for `.env` never reaches a `Stat`. It checks the same
predicate again on the **resolved** leaf, after `confineFile` has followed
every symlink in the path. Confinement's containment test (`withinDir`) says
nothing about extensions; a file named `notes.md` that is actually a symlink to
`.env` inside the same workspace passes confinement cleanly, and only the
second check refuses it. Dropping either check reopens a hole the other cannot
see: the first check alone lets a symlinked `.env` through under a markdown
name, and the second check alone would touch the filesystem — including a stat
on a path outside `$HOME`-confined territory — before refusing a request that a
pure string check could have rejected for free.

This is the same shape as `attach_file`'s TOCTOU discipline (check, then open
with `O_NOFOLLOW|O_NONBLOCK`, then `os.SameFile` against the pre-open stat) —
**a check has to run against the thing that will actually be read, not just the
thing the caller named** — applied one level up, to the file's *kind* rather
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
+ the `Push` loop + the one debug log line, unchanged. **When a second caller
needs a function's tail but has already done its own version of the function's
head more safely, split the function at that boundary rather than duplicating
the tail or calling the head again.**

## Comma-ok, once more, and the field it costs

`V2SessionConfig.WorkspaceFileRead func(conversationID, path string)
(WorkspaceFile, bool)` mirrors `AttachmentResolve`'s shape for the identical
reason: every refusal cause — wrong extension (either leaf), no recorded
workspace, an empty `Cwd`, a registry miss, an out-of-tree path, a non-regular
file, over the size bound — collapses into the same `false` inside
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
no reconnect and no re-wiring. An empty `Cwd` is refused rather than resolved,
because `confineFile("", path)` would otherwise canonicalise against the
daemon's own process directory — the daemon's own layout, not any conversation's
workspace.

## Related

- [Inbound `request_attachment` (#2054)](v2-session-manager-state-machine-inbound-request-attachment-attachmentresolve.md) — the handler this one copies step for step: nil-seam-inert, reject-not-tolerate decode, the `KnownConversation`-before-path-component ordering, and the two-code (`not_found` vs. `stream_aborted`) partition on the stream error's identity.
- [Outbound attachment stream (#2053)](v2-session-manager-state-machine-outbound-attachment-stream-streamattachm.md) — `attachmentEnvelopes` + the `Push` loop, now factored as `streamAttachmentBytes` and shared by both callers.
- [`attachment.file`: confine and store a claude-named path (#2164)](control-plane-attachment-file-confine-and-store-a-claude-named-path.md) — the first model-named path on this daemon, and the `confineFile`/`readChecked` primitives this verb reuses unmodified.
- [Attachment envelope types § `read_workspace_file`](protocol-package-constants-codes-go-envelope-types-attachments.md) — `TypeReadWorkspaceFile`, `ReadWorkspaceFilePayload`, and the wire vocabulary this handler answers.
- `docs/protocol-mobile.md` § Attachments, `#### read_workspace_file` — the published client contract: the two fields, the markdown-only rule on both leaves, the live-read (no stored copy) semantics, and the single undistinguished refusal.
