# #2711 — the daemon's session prompt names the folders the markdown reader serves

## Files read

- `internal/sessions/systemprompt.go` → `systemPromptText`, `composeSystemPrompt`, `composeSystemPromptFor`, `Pool.writeComposedPrompt`: the daemon-wide text, the two composers (18 test call sites between them, so their signatures stay), and the recompose site.
- `internal/sessions/pool.go` → `Config`, `Pool`, `New` (bootstrap prompt write), `buildSession` (per-session prompt write): where the resolved folders enter the pool and the two construction-time writes.
- `cmd/pyry/main.go` → `readFolders := resolveReadFolders(...)` and the `sessions.New(sessions.Config{...})` literal, which runs after it: the one wiring line.
- `cmd/pyry/workspace_file.go` → `resolveReadFolders`: the folders it returns are exactly the ones the reader accepts, so naming its output can never name a folder the reader refuses.
- `internal/sessions/pool_system_prompt_test.go` → `TestSystemPromptText_Pinned`, `helperPoolWithConversations`, `conversationWithPrompt`: the pin pattern and the pool fixtures to copy.
- `internal/sessions/systemprompt_client_test.go` → `clientResolverHolder`, `TestPool_Activate_NamesAttachedClient`: how a test drives `writeComposedPrompt` through `Activate` with a client attached.

No other feature branch touches these three files.

## Change

`sessions.Config` gains `ReadFolders []string`, the resolved folders, which `New` copies onto the pool. `cmd/pyry/main.go` passes its existing `readFolders`.

`systemprompt.go` gains `readFolderSentence(folders) string`: "" for no folders, otherwise one line, "This daemon serves markdown files under `/a`, `/b` and `/c` to a client that asks for one by absolute path.\n". Each folder is wrapped in backticks so a path containing a comma or the word "and" still reads as one path; the folders are operator configuration, trusted like the operator prompt, so they are not passed through `admissibleClientField`. `daemonPromptText(folders)` returns `systemPromptText` when the sentence is empty and `systemPromptText + "\n" + sentence` otherwise, keeping the blank-line separator every other section uses.

The two composers keep their signatures. Each body moves into a sibling that takes the daemon text as its first argument — `composeSystemPromptOn(daemon, operator)` and `composeSystemPromptForOn(daemon, operator, clients, note)` — and the old names call them with `systemPromptText`. `composeSystemPromptForOn` keeps the delegation to `composeSystemPromptOn` when it has no client or handoff section, so with no folders every composition is the bytes it is today. The three production writes use `daemonPromptText(p.readFolders)` (`New` uses `cfg.ReadFolders`): the bootstrap file in `New`, the session file in `buildSession`, and `writeComposedPrompt`. The sentence therefore sits directly after `systemPromptText` and before the client, handoff-note and operator sections. `SystemPromptFor` reports the operator's text only and is untouched.

Computing `daemonPromptText` per compose rather than storing it on the pool keeps a zero-valued `readFolders` (any `Pool` built without it) composing the constant, so no test fixture can drift into dropping `systemPromptText`.

## Testing strategy

- `TestReadFolderSentence_Pinned`: one, two and three folders against independently transcribed strings, plus nil and empty giving "".
- `TestDaemonPromptText`: nil folders return `systemPromptText` byte for byte; one folder returns the constant, a blank line and the transcribed sentence.
- `TestPool_ReadFolders_NamedInEveryComposition`: a pool built with two `ReadFolders`, a conversation holding an operator prompt and a resolver naming one client. The bootstrap file, the minted session's file read before `Activate` (the `buildSession` write) and the file after `Activate` (the `writeComposedPrompt` write) each hold the expected bytes, assembled in the test from the order rule: constant, sentence, client section, operator text.
- The unconfigured case is covered by every existing composition test and `TestSystemPromptText_Pinned`, which must pass unchanged.

## Documentation handoff

Pending for the documentation stage: `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md`, under `## Composition and resolution` — record the read-folder sentence, that it sits directly after `systemPromptText` and before the client, handoff-note and operator sections, that it names only the folders `resolveReadFolders` resolved at startup, and that with none it adds nothing.
