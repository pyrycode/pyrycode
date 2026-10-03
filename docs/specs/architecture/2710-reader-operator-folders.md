# #2710 — the markdown reader may open files under operator-named folders

## Files read

- `cmd/pyry/workspace_file.go` → `workspaceFileReader`, `isMarkdownName`: the reader this ticket widens; the two-leaf markdown rule and the one-`false` refusal contract stay as they are.
- `cmd/pyry/attach_file.go` → `confineFile`, `readChecked`: the confinement recipe and the checked read. `confineFile` resolves its root on every call; this ticket needs the same recipe against a root that was resolved once at startup.
- `cmd/pyry/attach_file.go` → `fileAttacher`: the `attach_file` verb, which must keep calling `confineFile` with the workspace only.
- `cmd/pyry/main.go` → `withinDir`: the containment test (`filepath.Rel`, never a prefix compare).
- `cmd/pyry/main.go` → `runSupervisor`, `pyryFlagValues`, `splitArgs`, `helpText`: where the flag is declared, where the argv splitter must learn it, and the usage text.
- `cmd/pyry/relay.go` → `relayWiring`, and the `WorkspaceFileRead` field of the `V2SessionConfig` literal in `startRelayV2`: the wiring that carries `convReg` to the reader.
- `internal/agentrun/workdir.go` → `ResolveWorkdir`: Abs, EvalSymlinks, case fold; its error names the path.
- `cmd/pyry/workspace_file_test.go`: the fixture `newWorkspaceReadFixture` builds the reader with two arguments; it must keep compiling unchanged.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md`: the seam's contract (registry read at call time, empty `Cwd` refused, nothing logged).

No other feature branch touches these four files.

## Context

Links Pyry sends mostly point into the operator's Obsidian vault, outside the conversation's workspace, so the reader refuses them as `attachment.not_found`. The operator names extra folders on the daemon command line; an absolute requested path that resolves inside one of them is readable under every existing reader rule. #2711 reads the same resolved list to name the folders in the session prompt, so the list is resolved once in the composition root and carried in `relayWiring`.

## Design

**Flag.** `-pyry-read-folder <abs path>`, repeatable, declared in `runSupervisor` with `fs.Var` over a small `folderList` type (`[]string` implementing `flag.Value`: `String`, `Set` appends). Added to `pyryFlagValues` so `splitArgs` keeps it on pyry's side, and listed in `helpText`.

**Startup resolution.** `resolveReadFolders(entries []string, log *slog.Logger) []string` in `workspace_file.go`. For each entry, in order: skip with one `Warn` if it is not absolute; skip with one `Warn` if `agentrun.ResolveWorkdir` fails; skip with one `Warn` if the resolved path is not a directory (a file root would make `withinDir(file, file)` true and turn the setting into a single-file grant nobody asked for). Otherwise keep the canonical path. The warning names the entry (operator configuration, not a client-named path) and a static reason; it never wraps `ResolveWorkdir`'s error. Called once in `runSupervisor` after the logger exists; the result is passed as `relayWiring.readFolders` and is never mutated afterwards.

**Confinement against a pre-resolved root.** `confineFile(root, path)` is split: it resolves `root` with `ResolveWorkdir` (unchanged refusal `errAttachUnresolved`) and delegates to a new unexported `confineToRoot(canonicalRoot, path) (string, os.FileInfo, error)` holding the rest of the recipe unchanged (relative join, resolve, `withinDir`, stat, regular-file check). `confineFile`'s behaviour and its callers do not change. A configured folder is passed straight to `confineToRoot`, so it is never re-resolved per request: a folder whose path component is swapped for a symlink after startup does not move the root.

**Reader.** `workspaceFileReader(convReg, maxBytes, folders ...string)`. Variadic so the existing fixture and every existing test compile and run unchanged with no folders. Order:

1. requested leaf must be markdown (unchanged);
2. the conversation must exist in the registry (unchanged — a miss is a refusal even with folders configured);
3. if `conv.Cwd != ""`, try `confineFile(conv.Cwd, path)`;
4. if that did not produce a file and `filepath.IsAbs(path)`, try `confineToRoot(folder, path)` for each folder in order, first success wins;
5. no root succeeded → `false`;
6. resolved leaf must be markdown, then `readChecked` (unchanged).

A relative path never reaches step 4, so it resolves against the workspace only. An empty `Cwd` skips step 3 and is never resolved against the process directory, but an absolute path may still be served from a folder. A symlink inside a folder that resolves outside every root fails `withinDir` for each root. The root that admitted the path is not reported; the reader still returns one `false` for every cause and logs nothing.

**Wiring.** `relayWiring` gains `readFolders []string`; `startRelayV2` passes `w.readFolders...` to `workspaceFileReader`. `fileAttacher` is untouched and never sees the list.

## Concurrency model

No goroutines. `readFolders` is built before `startRelay` and only read afterwards, from concurrent request goroutines; a slice that is never written after publication needs no lock.

## Error handling

Startup: every bad entry is skipped with a warning; the daemon always starts, with an empty list if every entry is bad. Requests: unchanged comma-ok, every refusal one `false`, errors from `confineFile`, `confineToRoot` and `readChecked` dropped.

## Testing strategy

New tests in `workspace_file_test.go`, the existing ones untouched:

- `TestWorkspaceFileReader_ConfiguredFolders`: a fixture with two configured folders (built via `resolveReadFolders`) serves an absolute path inside a folder; refuses a relative path naming a file that exists only in a folder; refuses an absolute path outside the workspace and every folder; refuses a folder symlink pointing outside all roots; refuses `notes.md` → `.env` inside a folder; refuses a directory, a FIFO and an over-bound file inside a folder; refuses traversal (`<folder>/../outside/x.md`); serves a folder path for a conversation with empty `Cwd`; refuses a folder path for an unknown conversation; still serves the workspace file. A folder reached through a symlinked spelling (`/link-to-vault/n.md`) is served, proving the requested path is resolved, not prefix-matched.
- `TestResolveReadFolders`: table of entries — relative, missing, a regular file, a symlink to a directory (kept, canonicalised), a valid directory — asserting the kept list and one warning per skipped entry, captured with a `slog` text handler into a buffer.
- `TestSplitArgs`-style check that `-pyry-read-folder /x` and `-pyry-read-folder=/x` stay on pyry's side, plus the help-text fragment; added beside the existing flag tests if a table exists, otherwise one small test.
- `readChecked`'s swap guard is already proven for `confineFile`'s output; `confineToRoot` returns the same pair, so the folder path is covered by the same read.

Checks: `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

- Whether `TestSplitArgs` is table-driven enough to take a row; decide when reading it.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`, the `read_workspace_file` row in the message-type table and the `path` field under `#### read_workspace_file`: the path is confined to the conversation's workspace **or** to a folder the operator configured on the daemon (`-pyry-read-folder`). A relative path resolves against the workspace only. Every refusal is still the single `attachment.not_found`. The markdown residual paragraph should say a paired client can also read any `.md` file under the configured folders.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md`: record the configured folders, their resolution once at startup (`resolveReadFolders`), the skip-and-warn rule (not absolute, unresolvable, not a directory), and that a folder root is never re-resolved per request (`confineToRoot`).

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The client-named `path` crosses into the daemon at `workspaceFileReader` and is only ever handed to `confineFile` / `confineToRoot`, which canonicalise before the `withinDir` boundary test. The folder list is operator input from argv, trusted as configuration but still canonicalised and shape-checked once in `resolveReadFolders`; downstream code holds only canonical, existing, absolute directories.
- [Trust boundaries] No findings. The widening cannot reach `attach_file`: `fileAttacher` keeps calling `confineFile(conv.Cwd, path)`, its signature takes no folder list, and `confineFile`'s behaviour is unchanged by the split, so the attacher's workspace-only rule holds structurally whatever is configured.
- [Tokens] No findings. No secrets are created or handled; the transfer id is still `conversations.NewID` (crypto/rand).
- [File operations — traversal] No findings. Relative paths join only against the workspace root; absolute paths are resolved with EvalSymlinks before `withinDir`, so `..` and symlinks are evaluated before the boundary test, and `withinDir` is `filepath.Rel`-based, never a prefix compare (`/vault-other` is not inside `/vault`).
- [File operations — symlinks] No findings. A symlink inside a folder resolving outside every root fails containment for each root; `notes.md` → `.env` inside a folder fails the resolved-leaf markdown check; tests prove both. A folder root is resolved once at startup and passed to `confineToRoot` without re-resolution, so replacing a root component with a symlink after startup cannot move the root.
- [File operations — check-then-use] No findings. The read is `readChecked`, comparing the stat that passed confinement with the opened descriptor (`os.SameFile`), with `O_NOFOLLOW|O_NONBLOCK`; unchanged and shared.
- [File operations — non-regular files] No findings. The regular-file check in the shared recipe runs before any open; a FIFO inside a folder is refused (tested).
- [File operations — scope of the grant] OUT OF SCOPE: an operator may name a very broad folder (`/` or `$HOME`), which exposes every `.md` file beneath it to paired clients. That is the operator's configuration choice, bounded by the markdown rule; the documentation handoff records it in the protocol's markdown residual. No ticket needed unless the operator asks for a guard.
- [Subprocesses] Not applicable: no process is started; the change is a read path and a flag.
- [Cryptography] Not applicable: no keys, nonces or comparisons against secrets.
- [Network and I/O] No findings. The per-read size bound is unchanged (`maxAttachFileBytes`, enforced twice in `readChecked`). Trying up to N folders costs N `EvalSymlinks` calls per request on the configured list, which is operator-sized.
- [Errors, logs] No findings. Every refusal is the one `false`; no request path logs. The only new log line is the startup warning, which names the operator's own entry and a static reason, never a client path and never `ResolveWorkdir`'s wrapped error.
- [Concurrency] No findings. The folder slice is written once before `startRelay` and read-only afterwards.
- [Threat model] No findings. `docs/protocol-mobile.md` § Security model treats a paired client as authenticated but limits what it can read; this ticket widens the readable set only by an explicit operator action, keeps the markdown-only and regular-file rules, and keeps the single `attachment.not_found` so a client cannot probe which root, or whether a non-markdown file, exists.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03

## Revisions

- 2026-10-03 (build): steps 3–5 of the reader's order live in a named helper, `confineToAnyRoot(workspace, folders, path)`, rather than inline in `workspaceFileReader`, so the root-trying rule reads as one unit. Contract unchanged. Open question settled: `TestSplitArgs` is table-driven and took a row for `-pyry-read-folder` in both spellings.
