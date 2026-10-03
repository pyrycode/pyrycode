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
