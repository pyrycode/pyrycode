# Attachment.file: file a claude-named host file under the calling session's conversation (#2164)

`VerbAttachFile` ("attachment.file") is the first control verb whose payload names a
**filesystem path chosen by the model** rather than a client-declared filename. Every
earlier attachment path component was daemon-minted or sanitised from a client-declared
string (see [attachments-package.md](attachments-package.md)); this one names a real path
and reads it, so it is the family's first verb that must *confine*, not just *sanitise*.
It shipped live but inert (the #1104 `mcp.approve` shape — see below); #2168 shipped the MCP
tool that calls it, `pyry mcp-files` ([pyry-mcp-files-command.md](pyry-mcp-files-command.md)),
and #2169 wired that tool onto a real claude child, so this verb now has a live caller. The
daemon maps the caller's session to its conversation, confines the path to that
conversation's recorded `Cwd`, and stores the bytes through the existing
`attachments.EnsureDir` / `Store` primitives with a daemon-minted id — no new storage
primitive was needed.

## Dependency shape: `SetFileAttacher`, not `SetApprovalRegistry`

The verb needed a destination lookup built from `conversations.Registry`, `sessions.Pool`
and the instance directory — all composition-root state that lives in `cmd/pyry` — plus
`withinDir`, the boundary test `cmd/pyry/main.go` already owns and which is unexported and
unimportable from `internal/control`. `SetApprovalRegistry` (#1104) installs a typed
registry the handler calls into; that shape doesn't fit here because there's no natural
`internal/control`-side type to hold "resolve a session to a conversation and confine a
path" — the work has to live where `withinDir` already does. `SetApprovalSurfacer`
(#1080) is the closer precedent: a plain `func` installed post-construction, letting the
dependency's implementation live entirely at the `cmd/pyry` composition root while
`internal/control` only knows its signature. `SetFileAttacher` copies that shape. The
nil-dependency-refuses-with-`Response.Error` posture is copied from `SetApprovalRegistry`
regardless of which shape backs it — that's the part of #1104 this ticket is explicitly
asked to reuse (ships live but inert until #2165 wires a caller).

## Confinement: canonicalise both ends the same way, then a boundary test — never a prefix compare

The root (`conv.Cwd`) and the target (the model-chosen path, joined against the root first
if relative — never against the daemon's own process directory) both go through
`agentrun.ResolveWorkdir` before comparison: `Abs` → `EvalSymlinks` → on-disk case fold.
**Both ends, not just the target.** APFS is case-insensitive by default, so a raw string
compare between two differently-cased spellings of the same directory is unsound in either
direction — canonicalising only the target and comparing it against a raw `conv.Cwd` would
have reintroduced the exact class of bug #910 fixed for `ResolveWorkdir`'s original
consumer. The boundary test itself is `withinDir` (`cmd/pyry/main.go`), reused rather than
re-derived — it already carries the #118/#221 fix (a `filepath.Rel`-based test, not a
prefix compare, so `/home/userfoo` isn't read as inside `/home/user`). Mutation-tested: swap
`withinDir` for a naive prefix compare and **only** the prefix-sibling row of the
confinement table reddens — evidence the other eight rows were exercising something other
than string-prefix containment.

**`agentrun.ResolveWorkdir` canonicalises a file leaf correctly, not just a directory,
despite its workdir-shaped name.** `canonicalCase` walks path components and `ReadDir`s
the accumulated *prefix* for each, so a leaf file component is folded against its parent
directory exactly as a leaf directory would be — confirmed rather than assumed (open
question in the spec, resolved in Phase B). Nothing about the function needed to change;
the name is just narrower than its actual contract. See
[agentrun-package.md § Consumers](agentrun-package.md) — this is now a second production
caller alongside `internal/agentrun/trust`.

## TOCTOU: the file that is read is the file that was checked

The regular-file check runs on the `os.Stat` result **before** any `open(2)`, not after —
load-bearing ordering, because opening a FIFO blocks until a writer appears and the check
is what's supposed to catch a FIFO before the daemon ever tries. That ordering alone isn't
enough against a swap introduced *between* the check and the open, so the open itself uses
`O_NONBLOCK` (a post-check swap to a FIFO then returns immediately instead of wedging the
handler goroutine) and `O_NOFOLLOW` (kernel-enforced refusal of a symlink at the final
component — a no-op on the legitimate case, since the path opened is already
`EvalSymlinks`' symlink-free output). The guarantee that actually closes the swap is
`os.SameFile` between the pre-open `Stat` and the opened descriptor's own `Stat`: it covers
a swap at *any* path component, not just the final one, which is strictly more than
`O_NOFOLLOW` alone catches. Mutation-tested: removing the `SameFile` check makes the swap
test serve the decoy's bytes verbatim. The swap test itself performs a real `os.Rename` of
a pre-created second file over the checked path between check and read, so the proof is
deterministic rather than a race that might not fire.

## Refusal reasons are static sentences — no path, filename, or workspace, ever

Every refusal is a fixed string naming nothing content-derived — the same fixed-constant
discipline `mcp.approve` uses (`docs/knowledge/features/control-plane-approve-mcp-approve-verb-forward-to-permbridge.md`).
This isn't optional here the way it might look: `attachments.Store`'s rename failure is an
`*os.LinkError` whose `Error()` prints the destination — and therefore the sanitised,
claude-authored filename — so propagating a storage error verbatim to `Response.Error`
would put a filename on the wire, the exact thing `docs/protocol-mobile.md` § Attachments
already bans logging. `Store`/`EnsureDir` errors collapse to one static "storing the file
failed"; every other refusal (bad session, unresolvable conversation, empty `Cwd`,
outside-workspace, oversized, not-a-regular-file) is likewise content-free. Refusals are
not logged at all — the reason returns only to the caller that needs it.

**Accepted residual: the refusal reason is a narrow existence oracle for paths outside the
workspace.** `EvalSymlinks` has to run before the containment test (that's what makes the
test sound), so a path outside the tree that exists answers "outside this conversation's
workspace" while one that doesn't answers "no readable file at that path" — two different
static sentences that between them leak one bit about whether something exists outside the
confined tree. Collapsing them was considered and declined: it would cost the
actionability the design is built around (an explicit tool call needs the caller able to
tell "the file isn't where I think it is" from "I need to write it into the workspace and
retry"), and the ordering can't be reversed without breaking the containment test itself.
Accepted because it opens no new capability tier — the caller is claude, which can already
read the filesystem directly with its own tools, and the socket's `0600` mode already
bounds every other peer to the same user.

## Confinement root is `conv.Cwd`, not the live child's actual workdir — a deliberate, scoped gap

The precise root would be the running claude child's actual working directory, and it is
not reachable without a real widening: it lives as an unexported field on the concrete
`streamsup` runner, no accessor exists anywhere in the tree, and `sessions.Session` /
`SessionInfo` / the registry entry all carry no such field. Reaching it would mean widening
the `sessions.Runner` interface across `internal/streamsup`, `cmd/pyry`, `internal/sessions`
and four test fakes — roughly eight production files against this ticket's five, which is
exactly the kind of one-consumer widening the sizing rules exist to keep out of a single
ticket. `conv.Cwd` is the value that's actually reachable: documented always-present,
absolute, and `$HOME`-confined at every write site. The two can diverge in one window
(`change_workspace` updates the row; only a *fresh* spawn or a revive-after-eviction
re-reads it — see `cmd/pyry/main.go`'s `resolveSpawnDir` call in the revive path). In that
window a file claude wrote in its older directory is refused with the actionable reason,
and claude re-writes it into the recorded workspace — the design's stated remedy rather
than a special case. #1475 is an open, unresolved question about whether that divergence
should be closed elsewhere in the daemon; this verb depends only on `Cwd` being present,
absolute and confined, which holds under either resolution.

## Announcing the store: `attachment_offered`, and the name it must not derive from (#2166)

A successful store now fans one `attachment_offered` frame to every interactive-capable
client, copying `streamApprovalBridge.broadcast`'s shape — monotonic per-emitter envelope
id, the #607 interactive gate, torn-down-conn tolerance (see
[v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md § Stream-json approval bridge](v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md#stream-json-approval-bridge--the-verdict-arm-1080)
for that fan-out's own account). `fileAttacher` gained a fourth dependency, a bare
`func(conversationID, attachmentID, filename string)` rather than an interface — nil is a
silent no-op, the same tolerance `SetApprovalSurfacer(nil)` already has above, so a daemon
with no relay leg (`startRelay`'s empty-`relayURL` early return) stores exactly as before.

**The announced name is the leaf of `Store`'s returned path, never the leaf of `resolved`
— the two sit side by side in scope at the call site but are not interchangeable.**
`resolved` is the *pre-sanitisation* path the model named; `attachments.Store` sanitises
the filename before writing and returns the path it actually wrote. For any input
`SanitizeFilename` doesn't touch — which is most inputs — `filepath.Base(resolved)` and
`filepath.Base(stored)` agree, which is exactly why the wrong derivation is easy to
introduce and not notice in review or in a casually-chosen test fixture: it only reddens
on a name carrying a separator, a control character, or a leading dot. Announcing
`resolved`'s leaf would leak the pre-sanitisation string past the sanitiser whose entire
job is keeping that string off the wire.

Refusals still announce nothing — the hook fires once, after `Store` returns nil and after
the existing content-free `log.Info`, immediately before the verb returns the minted id —
and a failed push never turns a successful store into a refusal. `filename` reaches no log
field on any branch of the announce path either, matching this verb's refusal-side
discipline above. See [protocol-mobile.md § Attachments](../protocol-mobile.md#attachments)
for the client-facing contract this closes: the announced name is the stored name, and the
offer is live-only (no registry, no replay).

## See also

- [Inbound `read_workspace_file` (#2598)](v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md) — the first wire path whose filesystem path a **paired client**, rather than the model, names. It reuses `confineFile`/`readChecked` unmodified and adds a markdown-only allowlist this verb does not need, since this verb's caller is already the model.
- [pyry-mcp-files-command.md](pyry-mcp-files-command.md) — `pyry mcp-files`, the `send_file`
  MCP tool (#2168) that calls this verb, registered on the interactive spawn by #2169. A `/clear`
  rotation still makes this verb refuse for a live child (see that document's identity section) —
  fail-closed, not a misfile, and not something this verb's own confinement logic can see.
- [control-plane.md § Approve](control-plane.md) — the `mcp.approve` precedent this verb's
  dependency-injection and fail-closed shapes both draw from.
- [attachments-package.md](attachments-package.md) — `EnsureDir`, `Store`, `ResolvePath`,
  and the filename-sanitisation/never-log discipline this verb's error mapping extends to
  host paths.
- [agentrun-package.md](agentrun-package.md) — `ResolveWorkdir`, now with a second
  production caller.
