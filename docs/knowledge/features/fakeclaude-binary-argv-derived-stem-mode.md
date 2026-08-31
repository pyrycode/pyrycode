# Argv-derived stem mode (#1195)

Every mode above binds its transcript stem to `PYRY_FAKE_CLAUDE_INITIAL_UUID` — a
single **process-wide** value, so every child of one daemon opens the *same*
`<uuid>.jsonl`. That is fine for a bootstrap-only e2e, but for a **minted**
per-conversation session the daemon tails `<convDir>/<mintedSessionID>.jsonl`
(`resolveBoundSessionJSONL`), and the two stems can never agree — the
subscription never opens, and conversation-scoped turn lifecycle
(`turn_state`/`turn_end`) was observable on **zero** PTY-tier tests. The fix
is not a new env value carrying the id (that's still process-wide); it's
reading the id the daemon already pinned this **specific** spawn to, off argv:
`sessions.buildSession` bakes `--session-id <pool id>` into a minted child's
argv, and `supervisor.buildClaudeArgs` appends `--session-id`/`--resume
<id>` to every bootstrap spawn too (#1164 picks the flag based on whether the
transcript already exists).

When `PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV` is non-empty, `main()` resolves
the initial stem via `argvSessionID(os.Args[1:])` before calling `openSession`,
falling back to `PYRY_FAKE_CLAUDE_INITIAL_UUID` on no match:

```go
// argvSessionID returns the value after the LAST "--session-id" or "--resume"
// in args, and whether it is present and safe to use as a filename stem.
func argvSessionID(args []string) (string, bool)
```

- **Both flags, last occurrence wins.** `--session-id` (create) and `--resume`
  (warm reattach, #1164) name the same stem; `buildClaudeArgs` always appends
  the flag last, so the spawn-time value beats anything a template
  contributed. Two-token form only — neither call site emits `--flag=value`.
- **Stem guard (security).** The returned value reaches `filepath.Join` in
  `openSession` and in the per-child JSONL trigger path below, so an unguarded
  value could steer a write outside the sessions dir. `argvSessionID` refuses
  anything empty or containing `/`, `\`, or `.` and reports not-found instead —
  a **laxer** predicate than the daemon's `transcript.ValidStem` (a full UUID
  regex), so it is not literally that function's mirror, but the no-separator/
  no-dot rule is enough on its own to make `filepath.Join` incapable of
  escaping the sessions dir. See [codebase/1195.md](../codebase/1195.md) for
  why the doc comment originally overstated this as a "mirror".
- **The flag's presence is NOT a minted/bootstrap discriminator.** Since #839
  the bootstrap spawn is pinned too, so a design that keyed the stem on "argv
  carries a session id" unconditionally would re-point *every* existing
  bootstrap child's transcript. Inertness comes from the explicit env knob,
  like every other mode here — with both effectively pinned in the harness's
  seeded-bootstrap setup (`seedBootstrapRegistry` makes the bootstrap pool id
  *equal* `PYRY_FAKE_CLAUDE_INITIAL_UUID`), the knob resolves to the same
  value the env would have given for the bootstrap child, byte-identical by
  construction.
- **Fallback, never fatal.** No flag, or a value the guard rejects, falls back
  to `mustEnv(envInitialUUID)` exactly as today — a legacy unpinned spawn keeps
  working, and a malformed value degrades to today's behaviour instead of
  writing somewhere unexpected. `rotateSession` is untouched: a later `/clear`
  still mints a fresh random UUID.
- **When unset, byte-identical to today**, including for a child whose argv
  carries `--session-id`/`--resume` (every bootstrap spawn does) — `os.Args`
  is never read at all.

Pinned by the untagged `TestArgvSessionID` (`argv_session_id_test.go`, no
build tag, mirroring `esc_detect_test.go`): both flags, last-occurrence-wins
in both orders, the unhandled `=` form, and every stem-guard rejection
(`../escape`, `a/b`, `a\b`, `x.jsonl`, empty).
