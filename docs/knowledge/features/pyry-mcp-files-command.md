# `pyry mcp-files` — the `send_file` MCP stdio server, forwarding over the control socket

The caller half of `attachment.file` ([control-plane.md § Attachment.file](control-plane.md), #2164): a stdio MCP server, `pyry_files`, exposing exactly one tool, `send_file`, that forwards a `tools/call` to the daemon via `control.AttachFile` and returns its outcome as the tool result. Forked from the running `pyry` binary the same way `pyry mcp-approve` is ([pyry-mcp-approve-command.md](pyry-mcp-approve-command.md)) — same `serveJSONRPCStdio` loop, same `initialize`/`tools/list`/`tools/call` trio, same fail-closed posture.

Ships live but inert: nothing yet spawns this server, and until the sibling ticket **#2169** puts a session id on the claude child's environment, every call refuses. That is the intended state, the same "ships live but inert" shape #1104 used for `mcp.approve`.

## A second server, not a second tool

`mcp__pyry_approve__send_file` would actively mislead the tool reference's reader; and `approveToolName`'s contract — that server exposes exactly one tool, fail-closed against any other name — is worth keeping rather than relaxing to fit a second tool onto it. So this is its own server, its own MCP frame types reused (not re-declared) from `mcp_approve.go`, and its own copy of the twelve-line `initialize` handler body — duplicating that body was judged smaller than editing the permission bridge's own handler in service of this ticket.

## The leak barrier is a type, not a check

`sendFileArgs{Path string}` is the unmarshal target for the model-controlled `tools/call` arguments — never `control.AttachFilePayload`, which carries a `SessionID` field. Unmarshalling straight into the payload type would populate `SessionID` from the caller's own input, leaving "the session id comes from the environment, never the request" resting on an unconditional overwrite that a later edit can silently drop. `sendFileArgs` has no field a caller-supplied session id can land in, so the guarantee holds by construction. Mutation-tested: swapping the unmarshal target back to `control.AttachFilePayload` (overwrite dropped) reddens two independent tests — the session-never-from-input test and the plain round-trip test both catch it, which is the point of a type-level guarantee over an assignment-level one.

## Session id: constructor parameter, not a `runMCPFiles`-and-server-both `os.Getenv`

`filesServer` takes `sessionID` at construction (`newMCPFilesServer(socketPath, sessionID string, log)`); the single `os.Getenv(envSessionID)` lives in `runMCPFiles`, not in the constructor. This is the approve server's own pre-#1929 history repeating as a design constraint rather than a bug found fresh: an environment-reading constructor forces every test through `t.Setenv`, which bans `t.Parallel` — see [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md) for the fuller account of that cost. Applying the lesson here means all of `mcp_files_test.go` runs parallel.

An absent or empty `PYRY_SESSION_ID` does not abort startup — the server answers `initialize`/`tools/list` normally and refuses every `tools/call`. Exiting at startup would make claude report a broken MCP server; a per-call refusal is a sentence claude can read and (once #2169 lands) will never actually see, since the id will always be present by then.

## What is guarded locally vs. left to the daemon

Only the session id is checked before dialling (`s.sessionID == ""` refuses without a connection attempt — the empty-id-is-a-wildcard hazard `handleAttachFile` and `fileAttacher` each already guard, made a third guard here because this seam is the only one able to tell "absent from the environment" apart from "the caller supplied one"). The `path` is deliberately **not** pre-checked and is forwarded empty or otherwise as-is: `handleAttachFile`'s own refusal for it is the actionable one, and a local guard would only substitute a worse sentence for a better one. No client-side confinement check exists either — canonicalisation, the `withinDir` boundary test and the TOCTOU defence all live in `confineFile`/`readChecked` ([control-plane-attachment-file-confine-and-store-a-claude-named-path.md](control-plane-attachment-file-confine-and-store-a-claude-named-path.md)) and a second copy here could only diverge from the one that actually decides.

## The daemon's sentence passes through byte-exact

`control.AttachFile`'s error is returned as the refusal text verbatim — not re-worded, classified, or mapped to a code. A classifier at this seam would have to guess which errors are the daemon's static, correctable prose and would re-word the rest; the design declines to build one. Pinned by a test asserting the result text equals a daemon `Response.Error` string byte-for-byte, prefix included, so any re-wording, trimming, or classification reddens it.

## Tool description accuracy is load-bearing

For `approve`, the input schema is advisory — claude's permission path fills a fixed shape regardless of what the schema says. Here the description and schema **are** the entire contract: they are all claude reads before deciding what to pass, so the confinement rule leads and the byte bound is interpolated from `maxAttachFileBytes` rather than spelled as a literal (so the sentence can't drift from the constant enforcing it). The wording says "this conversation's workspace directory", not "the current directory" — the enforced root is `conv.Cwd`, which can lag the live child's actual working directory in the `change_workspace` window (#1475); worded as "current directory" the contract would be subtly false in that window and a refusal would read as a contradiction, when the documented remedy is exactly what claude then does — write the file into the recorded workspace and retry.

## Logging: the session id is the correlation key you must not reach for

`mcp-approve`'s decision log uses `tool_use_id` as its correlation key. The equivalent-looking move here — logging `s.sessionID` for the same reason — would put the one value that confers authority to file into a conversation onto a stderr the forking claude captures. `refuse` logs only a fixed stage word (`malformed` / `unknown_tool` / `no_session` / `daemon_refused` / `no_result`); a success logs only the minted `attachment_id` (the one value `control.AttachFile`'s doc calls safe to surface). `call.Name` is never logged on the unknown-tool branch either, despite identifying the branch just as well — it's model-controlled bytes, and the stage word already says which branch fired. No branch logs the requested path, the arguments bytes, the params bytes, or the socket path (the last of which the transport-refusal *result text* does carry — an accepted residual, not a logging gap, since the peer is claude running as the same user and the path is already in this process's own argv).

## Accepted residual: a 5s client bound under a 35s daemon bound

`toolsCall` passes the `Serve` ctx to `control.AttachFile` unwrapped, with no per-call deadline added — matching `mcp_approve.go`'s `toolsCall`, and correct here for the opposite reason it's correct there: `mcp-approve` uses the patient, undeadlined `requestPatient` because it waits on a human with no natural ceiling; `mcp-files` uses the bounded `request` because there is no such wait, so an undeadlined ctx already bounds the whole exchange at `control.DialTimeout` (5s). `handleAttachFile`'s own bound is `sessionOpTimeout + sessionOpConnGrace` (35s). A file whose read-plus-store exceeded 5s would refuse client-side while the daemon went on to mint an attachment id — a filed attachment claude is told about as a refusal. Accepted because the bound is 16 MiB and the work is a local read plus a rename, orders of magnitude under 5s, and because the failure direction is fail-safe (a spurious refusal, never a spurious success) with an idempotent remedy (retry mints a fresh id). Generalizes: an undeadlined ctx into a `request`-based client verb only bounds at that verb's own dial timeout, which is not necessarily as generous as whatever the daemon-side handler gives itself — check the two numbers separately rather than assuming they match.

## Tests

`cmd/pyry/mcp_files_test.go`, stdlib `testing`, `t.Parallel()` throughout. Reuses this package's existing fake-control-socket-peer helpers (`startApprovePeer`, `replyPeer`, `hangUpPeer`, `shortTempDir`, `testLogger`) rather than declaring second copies — a comment at the top of the file records why a files test calls a helper still named for approve (they're peer/transport shapes, not approve semantics, and renaming a helper with a live caller in another file is out of scope). `driveMCP` could not be reused as-is (it's typed to `*approveServer`), so this file carries its own `driveFilesMCP`.

Covers: the handshake trio; `tools/list` advertising exactly one tool with the description naming the workspace rule and the `maxAttachFileBytes`-derived bound; a success round trip asserting the forwarded payload's session id and path; the session-id-never-from-input row (`{"path":"notes.md","sessionID":"attacker-chosen"}` against a server built with a different id); the empty-session-identity-refuses-without-dialling row (asserting **zero** connections accepted, not just a refusal result); the daemon-sentence-verbatim row; both transport termini (unreachable socket, and `hangUpPeer`'s accepted-then-silent conn); a cancelled ctx; malformed/misdirected calls (including a no-connection-accepted assertion on the wrong-tool-name row, which is what pins the fail-closed rejection rather than a forward that happens to fail); a no-byte-leak logging sweep across every branch (host path, filename, session id, socket path — none present in stderr, including on the two branches whose *result text* legitimately carries a path); and a dispatch test (`runArgs([]string{"pyry", "mcp-files", "bogus"})`) proving the arm exists without touching `os.Stdin`.

Mutation evidence (run over `go test -overlay`, no worktree writes) for the three load-bearing rows:

| Mutant | Died on |
|---|---|
| `sendFileArgs` replaced by `control.AttachFilePayload` as the unmarshal target | the session-never-from-input test **and** the round-trip test — two independent witnesses |
| the `s.sessionID == ""` guard deleted | the no-session-identity test, on both the refusal text and the connection count |
| the daemon's sentence prefixed with an added phrase | the daemon-refusal-verbatim test |

## Out of scope (deferred to #2169)

Registering this server on the interactive spawn (`--mcp-config`), writing `PYRY_SESSION_ID` onto the claude child, closing the shell-path bypass (nothing today stops `pyry mcp-files` being run directly with a hand-picked `PYRY_SESSION_ID`), and the live-claude proof. Until that lands, this server has no production caller and no session id ever reaches a real spawn's environment.

## Related

- [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md) — the sibling subcommand this one's shape, transport, and environment-reading-constructor lesson are drawn from.
- [control-plane-attachment-file-confine-and-store-a-claude-named-path.md](control-plane-attachment-file-confine-and-store-a-claude-named-path.md) — the daemon-side verb this forwards to: confinement, TOCTOU, and why its refusals are static sentences.
- [control-plane.md](control-plane.md) § Attachment.file — the wire verb and `SetFileAttacher` seam.
