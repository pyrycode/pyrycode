# #2720 — the markdown reader can always open the daemon's working folder

## Files read

- `cmd/pyry/main.go` → `runSupervisor`: `workdirReal` from `confineWorkdirToHome` (EvalSymlinks realpath, confined to `$HOME`) and the `readFolders` slice that feeds both `sessions.Config.ReadFolders` and the relay wiring's `readFolders`. The one wiring point.
- `cmd/pyry/workspace_file.go` → `resolveReadFolders`: the #2710 canonicalisation (`agentrun.ResolveWorkdir`) and its startup-warning style; `workspaceFileReader` / `confineToAnyRoot`: the reader rules, unchanged.
- `cmd/pyry/attach_file.go` → `confineToRoot`: a folder root is resolved once at startup and never re-resolved; `fileAttacher` never receives read folders, so `attach_file` stays workspace-only by construction.
- `internal/agentrun/workdir.go` → `ResolveWorkdir`: Abs + EvalSymlinks + `canonicalCase`. `workdirReal` lacks the case step, so it is re-run through this.
- `internal/sessions/systemprompt.go` → `readFolderSentence`: lists the slice verbatim with no dedup, so the dedup must happen in the slice.
- `cmd/pyry/workspace_file_test.go`, `cmd/pyry/attach_file_test.go`: fixture helpers `writeFile`, `mustSymlink`, `newAttachFixture`, `bindConversation`.

No other feature branch touches these files.

## Change

New `withWorkdirReadFolder(folders []string, workdir, home string, log *slog.Logger) []string` in `workspace_file.go`. It resolves `workdir` and `home` with `agentrun.ResolveWorkdir` (so a symlinked home or workdir spelling is still compared by realpath, and the folder is canonicalised the way #2710 canonicalises entries). It returns `folders` unchanged, logging one Info line `pyry: working folder not made readable` with the folder and a static reason, when the working folder is the home folder or `/`, or when either side does not resolve (an empty `home` counts as unresolvable, since `ResolveWorkdir("")` would mean the process directory). When the canonical working folder is already in `folders` it returns `folders` unchanged, which is what keeps the #2711 prompt sentence listing it once. Otherwise it returns the working folder followed by `folders`.

`runSupervisor` calls it on the `resolveReadFolders` output with `workdirReal` and `os.UserHomeDir()` (an error there returns from startup, as `confineWorkdirToHome` already does for the same call). Both consumers read the one slice, so the reader and the prompt sentence change together. The reader's rules, `confineToAnyRoot` and `fileAttacher` do not move.

## Testing strategy

- `TestWithWorkdirReadFolder` (table): added first when absent; not duplicated when a configured entry is the same folder, including through a symlinked spelling; not added and logged once when the workdir is home, a symlink to home, or home is given by a symlinked spelling; not added for `/`; not added when home is empty.
- `TestWorkspaceFileReader_WorkdirFolder`: no configured folders, conversation workspace `wd/default`; an absolute path to `wd/BEHAVIOR.md` is served; a `.md` symlink in `wd` to a markdown file outside every root, and a non-markdown file in `wd`, are refused with the same `false`. The reader takes no logger, so nothing about the path can be logged.
- `TestFileAttacher_RefusesWorkdirFile`: a conversation whose workspace is `wd/default` cannot `attach_file` `wd/BEHAVIOR.md`.

Checks: `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The only untrusted input is the client-named path. It still enters through `workspaceFileReader` and is confined by `confineToAnyRoot` with its rules unchanged. This ticket adds one root, chosen by the operator's own `-pyry-workdir` or process directory, never by a client. A paired client could already open every conversation's workspace through that conversation's id, so making a working folder readable when it holds several workspaces gives no new reach across conversations.
- [Trust boundaries] SHOULD FIX: the guard compared for equality with home and `/` only, so `withWorkdirReadFolder` on its own would admit a working folder that is an ancestor of home. That is unreachable today, because `confineWorkdirToHome` refuses such a folder earlier in `runSupervisor`, but the safety then depends on call order. The guard now also refuses any working folder that contains home (`withinDir(resolved, homeReal)`), and a test row covers it.
- [Tokens] No findings. No token is created, stored or compared. The reader's transfer id is the existing `conversations.NewID` (`crypto/rand`).
- [File operations] No findings. The new root is canonicalised once at startup with `agentrun.ResolveWorkdir`, so later swapping the folder for a symlink cannot move the boundary (`confineToRoot` never re-resolves it). Symlinks inside it are resolved and then checked against every root, as the new test's `escape.md` row shows. `readChecked` opens with `O_NOFOLLOW` and checks `os.SameFile` against the file that was checked, which closes the gap between the stat and the open. Nothing is written.
- [Subprocesses] No findings. None are started.
- [Cryptography] No findings. None is used.
- [Network and I/O] No findings. Reads stay capped at `maxAttachFileBytes` in `readChecked`.
- [Errors, logs] No findings. A refusal is still the one `false`, and the reader logs nothing. The startup line names only the operator's working folder and a static reason, the same as the `resolveReadFolders` warning.
- [Concurrency] No findings. The slice is built before any goroutine starts and is only read after that. `withWorkdirReadFolder` returns a new slice when it adds the folder, so the caller's slice is never aliased.
- [Threat model] OUT OF SCOPE: under `docs/protocol-mobile.md` § Security model threat 4 (token leak via phone), a compromised paired device can now read markdown in the working folder as well as in workspaces and configured folders. The mitigation stays per-device revocation. The operator decided on 2026-10-03, in the ticket's Context, that the working folder should always be readable. Excluding home keeps the blast radius from spreading to the whole home folder. No follow-up ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03

## Revisions

- 2026-10-03, security review: the guard now also refuses a working folder that contains the home folder, not only one equal to home or `/`. The new reason is `contains the home folder`. The contract is otherwise unchanged. The review ran after the plan was first committed, because the `security-sensitive` label was missed at planning time.
