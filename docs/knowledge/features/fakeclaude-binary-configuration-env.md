# Configuration — env

**Required** (a missing or empty value prints `fakeclaude: missing env
<NAME>` to stderr and exits 1):

```
PYRY_FAKE_CLAUDE_SESSIONS_DIR  directory that must already exist
PYRY_FAKE_CLAUDE_INITIAL_UUID  stem for the first <uuid>.jsonl
PYRY_FAKE_CLAUDE_TRIGGER       path watched for the rotation signal
```

**Optional** (each unset by default; together they layer behaviour on top
of the bare rotation primitive):

```
PYRY_FAKE_CLAUDE_STDIN_LOG          append every stdin byte to this file,
                                    fsynced per read (#323; lets a sibling
                                    test process observe the prompt)
PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER  path watched; on appearance, write the
                                    file's bytes to stdout as a scripted
                                    assistant chunk (#311)
PYRY_FAKE_CLAUDE_JSONL_TRIGGER      path watched; on appearance, append the
                                    file's bytes (capped) verbatim to the
                                    live <uuid>.jsonl, fsync, remove the
                                    trigger (#642; see § JSONL-trigger mode)
PYRY_FAKE_CLAUDE_TUI                when non-empty, emit the idle/thinking
                                    glyphs (#603; see § TUI mode)
PYRY_FAKE_CLAUDE_IDLE_TRIGGER       path watched; start BUSY (no startup idle
                                    glyph, never the spinner) until it appears,
                                    then emit the idle glyph ONCE and remove the
                                    trigger — a controllable busy→free window
                                    (#792; see § Idle-trigger mode). Mutually
                                    exclusive with PYRY_FAKE_CLAUDE_TUI.
PYRY_FAKE_CLAUDE_ESC_ENDS_TURN      when non-empty, enter raw mode and watch stdin
                                    for a bare ESC (the remote interrupt keystroke,
                                    supervisor.SendEsc's lone 0x1b); on the first
                                    one append a canned assistant end_turn line to
                                    the live JSONL so the Esc CAUSES the turn to
                                    stop (#794; see § Esc-ends-turn mode). A flag,
                                    not a path. Coexists with PYRY_FAKE_CLAUDE_TUI.
PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER  when non-empty AND MODAL_TRIGGER is set (a
                                    no-op otherwise), clear the permission modal on
                                    the first post-modal stdin byte (the local pyry
                                    attach head's answer keystroke) — write a
                                    modal-clearing screen once so tui-driver's class
                                    transitions Permission->Unknown and the daemon's
                                    #706 local arm resolves it (#793; see § Modal-
                                    clear-on-answer mode). A flag, not a path. EXTENDS
                                    modal mode; byte-identical when unset.
PYRY_FAKE_CLAUDE_STREAM_JSON        when non-empty, skip the PTY/TUI surface
                                    entirely and speak line-delimited stream-json
                                    instead (#1140; see § Stream-json mode). Checked
                                    FIRST in main(), above every other env var read —
                                    structurally mutually exclusive with every mode
                                    above (if both are set, stream wins) and binds no
                                    sessions dir / transcript.
PYRY_FAKE_CLAUDE_STREAM_HOLD        path to a trigger file; when set, the stream-mode
                                    child blocks BEFORE consuming any stdin until the
                                    path appears, then proceeds normally (#1138; see
                                    § Stream-path startup hold). A no-op outside stream
                                    mode. Default-off; unset ⟹ byte-identical.
PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV  when non-empty, take the INITIAL <uuid>.jsonl
                                    stem from this spawn's own argv — the value
                                    after the last --session-id/--resume
                                    (argvSessionID) — instead of
                                    PYRY_FAKE_CLAUDE_INITIAL_UUID (#1195; see
                                    § Argv-derived stem mode). No flag found, or a
                                    value that fails the filename-stem guard, falls
                                    back to the env var. A flag, not a path.
PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR  directory watched in parallel with
                                    PYRY_FAKE_CLAUDE_JSONL_TRIGGER for
                                    <dir>/<this child's own initial stem>.jsonl.trig
                                    (#1195; see § Per-child JSONL trigger mode).
                                    Computed once from the same stem
                                    SESSION_ID_FROM_ARGV resolves, so the two are
                                    typically set together, but neither requires
                                    the other.
PYRY_FAKE_CLAUDE_REJECT_ABSENT_RESUME  directory; when set, a stream-mode spawn
                                    whose winning id flag is --resume against an
                                    id with no <dir>/<id>.jsonl is refused like
                                    real claude (stderr + exit 1) instead of
                                    served (#1631; see § Reject-absent-resume
                                    mode). Stream mode only — checked inside the
                                    STREAM_JSON branch. Default off; unset is
                                    byte-identical.
```

env is the entire configuration surface, matching how the harness consumer
configures the child via `cmd.Env`. fakeclaude reads stdin only when
`STDIN_LOG`, `TUI`, `MODAL_TRIGGER`, `ESC_ENDS_TURN`, `MODAL_CLEAR_ON_ANSWER`,
or `STREAM_JSON` is set; otherwise it ignores stdin entirely. (`CLEAR_ROTATES`
was one of these arms until #2456 retired it — stranded since #1348 deleted the
terminal supervisor that typed `/clear` into a PTY.)
`STREAM_JSON` never inspects `os.Args` either — the daemon's injected
`--input-format`/`--output-format`/`--verbose`/`--session-id`/`--resume` flags
are silently tolerated by construction, not parsed.

`SESSION_ID_FROM_ARGV` (#1195, § Argv-derived stem mode below) is the one
exception to "no positional args": when set, it is the sole mode that reads
`os.Args`, and only to extract the value following `--session-id`/`--resume` —
never to reject an unrecognised flag. When unset (every caller before #1195),
`os.Args` is never read at all.
