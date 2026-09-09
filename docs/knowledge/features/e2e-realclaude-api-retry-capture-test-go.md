## api_retry_capture_test.go (#2262)

Live capture that stages a failing upstream — a loopback listener answering every request 529,
reached via `streamsup.Config.Env`'s one-entry additive `ANTHROPIC_BASE_URL` override — because a
`system/api_retry` line cannot be provoked by a prompt. The captured record is committed as
`testdata/api_retry_v2.1.259.json`. `streamLine` is untouched; nothing in `internal/streamsup` maps
this subtype yet.

### The verbatim field set, and why the mapping needs no `permission_denied`-style gate

Ten `system/api_retry` lines fired, all decoding cleanly into `streamLine`'s segmentation shape with
`message` **absent** — unlike `system/permission_denied`, whose `message` arrives as a string where
`streamLine` declares an object and needed `consumePermissionDeniedLine` as a separate gate.
`api_retry` can be read the same way the other eight `system` subtypes are, directly in
`emitSystemSubtype`. The observed keys: `attempt` (1-indexed), `max_retries`, `retry_delay_ms`
(585ms rising to 37.7s across the ten attempts), `error_status`, `error` (`"overloaded"` in this
run), `session_id`, `uuid`. `session_id` and `uuid` appear in no docs page — the reason this
package's rule is to declare a subtype's field set from a committed capture, never from
documentation. `no_response` never appeared in this run and its shape is unconfirmed.

### A turn that exhausts its retries closes as `result`/`success` — the finding nobody asked for

The retried turn's terminal line is `result` with subtype `success`, carrying
`terminal_reason: "api_error"` and a `<synthetic>`-model assistant message holding the error prose.
Subtype alone reads a fully-failed, retried-out turn as having succeeded. Any future code mapping
`result` lines — the downstream `api_retry` mapping ticket foremost, but any other `result` consumer
too — has to read `terminal_reason` beside `subtype`, not `subtype` alone, or it will misclassify
this shape as an ordinary success.

### A request count is not a retry count, twice over

Two upstream requests do not by themselves prove claude retried. First: `streamsup` restarts a
crashed child, and a broken upstream is exactly what crashes one, so a respawn ladder (N children,
one request each) reads identically to a retry ladder unless the request count is checked against
the observed spawn count (`arcapSpawnLog` counts `spawning claude` log records; only requests above
spawns license a retry claim). Second, and caught only in code review: even within a single spawn,
one child calling two *different* endpoints (a token-counting probe, then the messages endpoint)
produces two requests that are not retries of each other — the real signal is repeats within one
census key (`arcapRecord.busiestEndpoint`), not the raw total. Both corrections publish in the same
direction (toward under-claiming), which is the right failure mode for a record whose finding is
meant to be replayed as a mapping's field source. Any future probe that infers retry/respawn
behavior from a supervised child's request count needs the same two checks — the spawn floor and
same-endpoint repetition — not the raw count.

### The double-write survived where a single in-repo write would not have

Built from the outset per [the compaction capture's lesson](e2e-realclaude-compaction-capture-test-go.md)
that a gate-only run's in-repo write dies with its throwaway worktree: this probe writes the record
to an `os.MkdirTemp` artifact directory *and* the in-repo fixture path. The prediction held — the
gate's in-repo write did die with the worktree, and the artifact-directory copy is what a
repair-leg commit (`2ef92186`) recovered and landed. Where compaction's fixture needed an
opportunistic rescue riding an unrelated ticket (#2236) to ever land, this one's recovery path was
already built in and needed only a plain commit. Any new capture probe in this family should default
to the double-write rather than treat it as optional hardening.

### Turn budget sized against the shared binary deadline, not a per-test constant

The first pass sized the turn (6-minute budget, 90s quiet window) against itself and starved a
sibling capture under the dispatcher's shared 20-minute `-timeout`: this probe spent 182s of it on a
gate run where the fixture didn't yet exist, and `TestRealClaude_TaskNotificationCapture` was still
running when the binary timeout fired. Confirms
[the same lesson already on record for the `initialize`-control family](e2e-realclaude-initialize-control-probe-test-go.md):
a per-step or per-test budget raise needs checking against the invocation's own timeout
(`t.Deadline()`), not just against itself. `arcapTurnBudgetWithin`/`arcapBudgetFor` reserve time for
teardown and the record write and skip outright below a floor rather than starting a turn a
`-timeout` kill would discard along with every cleanup — but note the committed fixture is what
actually closes the gate regression here (the probe's absence-gate skips it entirely once the record
exists); the deadline guard only defends the forced-recapture path.

### Related

- [`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) — the gate-only
  worktree fixture-loss pattern this probe's double-write was built to survive, and the
  opportunistic-commit rescue this probe didn't need.
- [`initialize-control-probe-test.go`](e2e-realclaude-initialize-control-probe-test-go.md) — the
  prior record of the invocation-deadline-vs-per-step-sum budget lesson this probe applied.
- `dropped_line_capture_test.go` — no package overview exists for it yet; it is the source of the
  `dropcapRedactor`/`dropcapScanner`/census machinery this probe composes rather than reimplements.
