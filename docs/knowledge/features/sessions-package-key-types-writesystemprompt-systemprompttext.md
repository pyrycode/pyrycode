# `writeSystemPrompt` + `systemPromptText` (#2093)

```go
func writeSystemPrompt(registryPath, text string) (string, error)
```

Writes the daemon-appended system prompt file every interactive claude is
spawned with via `--append-system-prompt-file`, and returns its absolute path.
`systemPromptText` is the constant it is called with in production: three
architecture facts (no terminal, a separate client renders the reply and may
be on a different machine, more than one client can attach) and nothing else,
pinned byte-for-byte by `TestSystemPromptText_Pinned`. It states no claim about
what any client can render or do — a capability claim here would rot the day a
client's rendering changes, the way `last_seen_ts` and `last_event_id` rotted
in [#2090](https://github.com/pyrycode/pyrycode/issues/2090) and
pyrycode-desktop#1068, while an architecture fact does not.

**`text` is a parameter, not a read of the const — that is the join seam.**
[#2094](https://github.com/pyrycode/pyrycode/issues/2094) (a per-conversation
operator prompt) and #2148 (the client's own name and version, blocked on this
ticket) both want to append to the same session's system prompt. Whichever
lands second changes what its caller composes and passes in, not this
function's signature.

**Daemon-scoped, not session-scoped — the first file in this package with
that lifecycle, and it does not share `writeMCPSettings`'s cleanup sites.**
The text is identical for every session, so one file serves the whole daemon:
fixed name `<dataDir>/system-prompt.txt` (no id in the name, unlike the
per-session `--settings` file), written once in `Pool.New`, the resulting path
kept on `Pool.systemPromptPath` (read-only after construction, no lock), and
removed once by the shutdown `defer` in `Pool.Run`. It is **not** touched by
`Pool.Remove`: that removes a session's own `--settings` file, and doing the
same here would delete a file every other live session still depends on.
Anything else added to `spawnBase` in future should ask "per-session or
per-daemon?" before copying either cleanup site verbatim — the two now
diverge on purpose.

Otherwise it mirrors [`writeMCPSettings`](sessions-package-key-types-writemcpsettings-session-settingspath.md)
structurally: the same `registryPath`-selected branch (data-dir vs
`os.TempDir()`), the same atomic `CreateTemp`-in-target → write → `Sync` →
`Close` → `Rename` recipe, the same dotted `.tmp` scratch pattern, the same
0600 mode (integrity, not confidentiality — the payload is a public constant,
but anyone who could write this file controls text pyry hands claude as a
system prompt). A write failure at `Pool.New` is fatal for the settings
file's reason inverted: a daemon that started anyway would silently spawn
every session reasoning about the wrong surface — a quiet wrong answer rather
than a loud one.

**Wired into `spawnBase`, immediately after the `--settings` pair, for the
same reason that pair is there.** `Session.spawnArgs` recomposes
`spawnBase + claudeSettingsArgs(settings)` on every settings change, so
placing the flag anywhere else (`claudeSettingsArgs`, `streamsup.Config.Args`,
the `cmd/pyry` mapper) would drop it on the first `Pool.UpdateSettings` or
backoff restart. `operatorBypass(base)` and the ACP-embedded pool's argv
assertions were checked against the two new tokens the same way #943's
`--settings` pair was checked: `operatorBypass` is an exact-element
`slices.Contains` match, so it cannot answer differently, and
`stripSystemPrompt` joined `stripMCPSettings` in `waitArgv`/`installedArgv` —
the same two-site test-helper edit #943 made, not a re-audit of the ~30
exact-argv assertions that depend on them, which stay green because they
compare only the flags they own and now double as a regression check that
this flag reached that path too. That pattern is worth repeating for any
future third addition to `spawnBase`.

See [docs/specs/architecture/2093-remote-client-system-prompt.md](../../specs/architecture/2093-remote-client-system-prompt.md)
for the full design and its `## Revisions` (confirms the flag is inert to
every reader in `internal/e2e/realclaude` that keys on `--append-system-prompt-file`).
