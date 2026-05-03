# ADR 010: `pyry sessions <verb>` sub-router shape

## Status

Accepted (ticket #76, Phase 1.1a-B2).

## Context

Phase 1.1 introduces a verb family on the operator-facing CLI: `pyry sessions new`, `pyry sessions list` (#61), `pyry sessions rename` (#63), `pyry sessions rm` (#65), and the `sessions`-adjacent `pyry attach <id>` refactor (#49). Each is a wire-level verb whose `internal/control` server-side handler and typed client wrapper already shipped (#75/#87/#90/#98). The CLI consumer is the missing half.

Two design questions had to be answered *now* (in the first slice that adds the family) so the four follow-on tickets are mechanical:

1. **Where does the `sessions <verb>` dispatch live?** The pre-existing top-level `switch` on `os.Args[1]` already houses `status`, `stop`, `logs`, `attach`, `install-service`. Adding five `case "sessions <verb>":` arms inline would force every follow-on ticket to grep the top-level switch.
2. **How are sub-verb flags parsed?** Future verbs need their own surface (`--new-name` for `rename`, `--archive`/`--purge` for `rm`, …). Threading them through the top-level `flag.FlagSet` would namespace-pollute every other CLI invocation.

## Decision

**Sub-router lives in `cmd/pyry/main.go runSessions(args []string) error`.** The top-level switch gains exactly one case (`case "sessions": return runSessions(os.Args[2:])`); `runSessions` owns the rest.

**The sub-router peels the global pyry flags via the existing `parseClientFlags` helper, then dispatches on the first positional.** Each sub-verb gets its own `flag.NewFlagSet("pyry sessions <verb>", flag.ContinueOnError)` for sub-verb-specific options.

**The list of implemented verbs lives in a constant `sessionsVerbList`.** Each follow-on ticket appends one token in the same edit that adds the switch case — duplication is one token in two places, not a derived list with sort/iteration overhead.

**The convention is: `-pyry-socket` / `-pyry-name` come *before* the sub-verb.** A global flag placed after the sub-verb reaches the sub-verb's own FlagSet and produces "flag provided but not defined" — failing loud, no silent shadowing.

## Rationale

### Why the sub-router takes a parsed `socketPath`, not raw args

Two reasons:

1. **Single canonical parse path for every `sessions.*` verb.** Every sub-verb needs the same global flags resolved the same way. Centralising the parse in `runSessions` (one call to `parseClientFlags`) means `runSessionsNew` / `runSessionsList` / `runSessionsRename` / `runSessionsRm` all receive a ready socket path and never see `-pyry-socket` themselves.
2. **Forces the global-flags-first convention structurally.** If each sub-verb's own `FlagSet` registered `-pyry-socket` independently, a stray `-pyry-name` placed after the sub-verb would silently shadow the global value. With the parsed-`socketPath` design, that argv lands inside the sub-verb's FlagSet and fails loud as an unknown flag.

The cost is one operator-facing convention (documented in help text and ADR). The win is one canonical parse path and one structural guarantee against silent shadowing.

### Why `sessionsVerbList` is a constant, not derived

A `map[string]func` would derive the list from `range m` but force a sort (map iteration order is randomised) and pay an iteration cost that only amortises at ≥3 verbs. With one verb today and four 1-line additions in 1.1b/c/d/e, the duplication is one token per verb in two places (switch case + constant). Dead-simple beats indirection here. The 1.1b ticket extends both in the same edit — no new pattern, no map.

### Why each sub-verb gets its own `flag.NewFlagSet`

Mirrors `runInstallService`'s precedent (it already runs its own `flag.NewFlagSet` for `-systemd`/`-launchd`/etc). Threading `--new-name`, `--archive`, `--purge`, etc. through the top-level FlagSet would force every other CLI invocation through a parser that knows about flags it doesn't care about, and risks namespace collision across sub-verbs (e.g. a hypothetical `--name` on `rename` colliding with `new`'s `--name`). Per-verb FlagSet keeps each verb's surface self-contained.

### Why no per-positional-arity helper for `new` (contrast `attachSelectorFromArgs`)

`runSessionsNew` *does* extract a `parseSessionsNewArgs(args) (label string, err error)` helper — but only to keep the unit test network-free, not to encode a complex arity rule. The arity rule is trivial (zero positionals). The helper exists so `cmd/pyry/sessions_test.go` can table-test flag forms without dialling the control socket.

## Consequences

### Positive

- **Adding a verb is mechanical.** 1.1b/c/d/e each add three things: one switch case in `runSessions`, one `runSessions<Verb>` helper, one token in `sessionsVerbList` (and one corresponding word in the help text). No top-level switch edit. No `parseClientFlags` change. No new shared state.
- **Unknown-verb path cannot regress to "forward to claude".** `runSessions` is reached from the top-level switch and returns through `errSessionsUsage` — control never falls through to `runSupervisor`. Pinned by `TestRunSessions_UnknownVerb` (unit) and `TestSessionsNew_E2E_UnknownVerb` (e2e: registry session count unchanged before/after).
- **Operator-facing convention is enforced, not just documented.** A misplaced `-pyry-name` after the sub-verb fails fast with a clear "unknown flag" message rather than silently shadowing.

### Negative

- **The "globals before sub-verb" convention is a new operator-visible rule.** It mirrors the pre-existing top-level rule ("pyry flags must come before claude args") so the cognitive load is small, but it does need documenting in `--help` output and onboarding.
- **`sessionsVerbList` and the switch can drift.** If a follow-on ticket adds a switch case but forgets the constant (or vice versa), the unknown-verb / missing-verb error message becomes wrong. Mitigated by reviewer attention; not enforced by the type system. Per evidence-based fix selection, no test-level enforcement until observed.

### Neutral

- **`runSessions` cannot return a typed exit code.** `os.Exit(2)` for usage errors (the convention `runAttach` uses for "too many positionals" — see [control-plane.md § Attach: CLI Surface](../features/control-plane.md#attach-cli-surface-11e-d)) is not used here; `errSessionsUsage` returns an error, which `main` prints and exits 1. The split was deliberate for `runAttach` (positionals are typed wrong = user error = exit 2); for the sub-verb router the missing/unknown-verb cases are arguably also user error, but exit 1 keeps `runSessions` returning a single error type and matches the rest of the file's error-propagation shape. Re-examine if shell scripts need to discriminate the cases.

## Alternatives considered

- **Inline `case "sessions list":`, `case "sessions rename":`, … in the top-level switch.** Rejected — every follow-on ticket would touch the top-level switch, and the switch would carry namespace-specific arms mixed with `status` / `stop` / `logs`.
- **A `map[string]func(socket, args) error` dispatch table.** Rejected for the reasons above (sort overhead, derived-list cost not amortised at the family's expected size of 5 verbs).
- **Sub-verb `FlagSet`s register `-pyry-socket` / `-pyry-name` themselves.** Rejected — silent shadowing risk, and duplicates the global-flag surface across every sub-verb.

## References

- [`features/control-plane.md` § Sessions: CLI Router (1.1a-B2)](../features/control-plane.md#sessions-cli-router-11a-b2) — implementation walkthrough.
- [ADR 003](003-session-addressable-runtime.md) — `internal/sessions` Pool wraps the supervisor (the seam this CLI consumes).
- `docs/specs/architecture/76-cli-sessions-new.md` — full architect's spec.
