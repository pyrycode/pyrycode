# Spec #917 — collapse `startRelay`/`startRelayV2` wiring into a `relayWiring` struct

**Ticket:** [#917](https://github.com/pyrycode/pyrycode/issues/917) · **Size:** S · **Security-sensitive:** no (behavior-preserving refactor; no new design surface — see § Security)

## Files to read first

- `cmd/pyry/relay.go:179-348` — `startRelay`: the 20-param positional signature, the `serverID`/`registry`/`conn` locals it produces, the v1/v2 branch, and the `startRelayV2` pass-through call at line 243. This is the primary edit site.
- `cmd/pyry/relay.go:350-635` — `startRelayV2`: the second 20-param signature and its function doc comment (lines 350-373), which holds the `claudeSessionsDir` gating knowledge and the `StaticPriv` security contract. **Keep both doc-comment blocks on the functions — they are function docs, not param docs; do not delete them when you shorten the signatures.**
- `cmd/pyry/main.go:872` — the single production call site inside `runSupervisor`; the one-line positional call that becomes a named-field struct literal. Read `main.go:820-870` for how the last few args (`qse`, `debugBundler`, `snapshotSettings`, `settingsUpdaterAdapter{pool}`) are built just above the call.
- `internal/relay/relay.go` (type `Config`), `internal/dispatch` (type `Config`), `internal/msgqueue` (type `Config`) — the codebase's existing `Config`-struct-as-named-fields idiom the new struct mirrors. No need to read deeply; just confirm the named-field-literal style before writing the struct literal.
- Not needed: no test file references `startRelay`/`startRelayV2` (confirmed by grep — only comment mentions in `assistant_turn.go`, `session_transition_v2.go`, and `main.go`, none of which call the functions). AC4's "no test edits" is structural.

## Context

`startRelay` and `startRelayV2` have each grown to ~20 positional parameters as every daemon seam (settings, snapshot usage, debug bundler, queue emitter, …) appended one more arg — forwarded by position from `startRelay` → `startRelayV2` and from the single `runSupervisor` call site. Two directory-path strings (`claudeSessionsDir`, `defaultCwd`) sit adjacent in the list; transposing them compiles clean and produces a subtle runtime misbehaviour (the JSONL resolver scans the wrong dir). Two adjacent bools (`allowInsecure`, `v2Enabled`) and three bare-string identifiers (`instanceName`, `relayURL`, `version`) have the same transposition hazard.

This is a mechanical, behaviour-preserving repackaging: only `cmd/pyry/relay.go` and `cmd/pyry/main.go` change. Named-field population at the call site turns each same-typed transposition into a visible, diff-reviewable edit instead of a clean-compiling runtime bug.

## Design

### One local struct, threaded whole

Introduce a single **unexported, cmd/pyry-local** struct `relayWiring` that carries **every call-site-supplied wiring value**. `startRelay` reads its own fields from it and forwards the **same struct value unchanged** to `startRelayV2`. This satisfies AC2's "every wiring argument is bound by field name, not position" literally — including the startRelay-only config values (`relayURL`, `version`, `allowInsecure`, `v2Enabled`, `shutdown`) that `startRelayV2` does not read but that are still transposition hazards at the call site.

**Why thread the whole struct through, rather than a shared-only subset?** AC2 requires *every* wiring argument bound by name — the three bare strings include `relayURL`/`version`, which are startRelay-only. Putting only the shared fields in the struct would leave `relayURL`/`version` positional at the call site and only partially satisfy AC2. Threading one struct is also strictly simpler than maintaining a shared struct plus a residual positional tail. `startRelayV2` receiving five fields it happens not to read is a normal, idiomatic wiring-struct shape (a sub-leg reads the subset it needs); it is not a smell.

`ctx`/`logger` stay as leading positional args (idiomatic Go). The three `startRelayV2`-only values that `startRelay` **produces internally** — `conn` (`relay.Connect`), `registry` (`devices.Load`), `serverID` (`identity.LoadOrCreate`) — are **not** wiring; they stay as trailing positional args to `startRelayV2`.

### Post-refactor signatures (contract)

```go
func startRelay(ctx context.Context, logger *slog.Logger, w relayWiring) (cleanup func(), err error)

func startRelayV2(ctx context.Context, logger *slog.Logger, w relayWiring,
    conn *relay.Connection, registry *devices.Registry, serverID identity.ServerID) (drain func(), err error)
```

The v2 branch call at `relay.go:243` becomes `startRelayV2(ctx, logger, w, conn, registry, serverID)`.

### `relayWiring` fields (contract)

All 21 fields below, in this order. Types are lifted verbatim from the current signatures. Each field carries a doc comment preserving the semantics currently implicit in the parameter name / function doc (AC3). The two directory-path fields **must** get the explicit distinguishing comments spelled out below — that is the specific knowledge AC3 protects.

| Field | Type | Call-site value (`main.go:872`) |
|---|---|---|
| `instanceName` | `string` | `*name` |
| `relayURL` | `string` | `relayURL` |
| `version` | `string` | `Version` |
| `allowInsecure` | `bool` | `allowInsecure` |
| `v2Enabled` | `bool` | `v2Enabled` |
| `shutdown` | `context.CancelFunc` | `cancel` |
| `convReg` | `*conversations.Registry` | `convReg` |
| `creator` | `handlers.SessionCreator` | `sessionMinter{pool}` |
| `router` | `handlers.SessionRouter` | `router` |
| `queue` | `*msgqueue.Queue` | `queue` |
| `active` | `*activeConversation` | `active` |
| `boundHost` | `boundHostFunc` | `boundHost` |
| `sup` | `*supervisor.Supervisor` | `bootstrap.Supervisor()` |
| `bridge` | `*supervisor.Bridge` | `bootstrap.Bridge()` |
| `claudeSessionsDir` | `string` | `claudeSessionsDir` |
| `defaultCwd` | `string` | `defaultCwd` |
| `transitions` | `transitionObserverSink` | `pool` |
| `qse` | `*queueStateEmitterV2` | `qse` |
| `debugBundler` | `func() ([]byte, error)` | `debugBundler` |
| `settings` | `relay.SettingsUpdater` | `settingsUpdaterAdapter{pool}` |
| `snapshotSettings` | `func() (model, effort string, yolo bool)` | `snapshotSettings` |

**Mandated doc comments (AC3) — the two dir-path fields must state their distinct roles, e.g.:**

- `claudeSessionsDir` — "Directory the rotation-following JSONL resolver scans to tail the daemon's own claude child's transcript (turn stream #633, snapshot-usage reader #857). Empty disables reconcile, the rotation watcher, and the interactive turn/modal streams."
- `defaultCwd` — "Default workspace directory stamped onto conversations created without an explicit cwd (the `CreateConversation` handler)."

For the remaining 19 fields, lift the one-line intent already encoded in the function doc comments / seam comments in `relay.go` (e.g. `debugBundler`, `settings`, `snapshotSettings` each already have a paragraph at their `V2SessionConfig` use site — condense to one line). Do not invent new semantics; this is a move, not a redocumentation.

### Call site (contract)

`main.go:872` changes from one positional line to a named-field literal:

```go
relayCleanup, err := startRelay(ctx, logger, relayWiring{
    instanceName:      *name,
    relayURL:          relayURL,
    // … all 21 fields, one per line, bound by name …
    snapshotSettings:  snapshotSettings,
})
```

The values on the right are exactly today's positional args in order (table above) — a straight transcription, which is what makes this behaviour-preserving.

### Internal reference updates

Inside both functions, each bare parameter reference becomes `w.<field>` (e.g. `relayURL` → `w.relayURL`, `claudeSessionsDir` → `w.claudeSessionsDir`, `shutdown()` → `w.shutdown()`). This is ~35 mechanical renames total across the two functions. The `conn`/`registry`/`serverID` references in `startRelayV2` stay bare (they remain direct params). No control flow, no branch, no handler wiring changes.

## Concurrency model

Unchanged. The goroutine structure (dispatcher/forwarder/waitDone in v1; manager Run + the turn/modal/transition/queue-state stream cleanups in v2), the cleanup ordering, and the `conn.Close()`-before-drain contract are all untouched. This refactor moves argument passing only.

## Error handling

Unchanged. Every `fmt.Errorf(... %w ...)` wrap, the fail-fast prologue (`identity.LoadOrCreate`, `devices.Load`, `keys.LoadOrCreate`, `relay.Connect`, `NewV2SessionManager`), and the `relayURL == ""` early return are preserved verbatim — they just read `w.relayURL` etc. No new failure modes, no new reject branches.

## Security

Not security-sensitive: a pure argument-repackaging refactor adds zero new design surface — no new input acceptance, no new validation, no crypto, no routing change. `StaticPriv`/`staticKey` is a **local loaded inside `startRelayV2`** (`keys.LoadOrCreate`, `relay.go:397`), **not** a wiring parameter, so it is out of scope for `relayWiring` and its no-log/no-wire contract is untouched. The `startRelayV2` function doc's SECURITY paragraph (lines 370-373) stays verbatim. No `security-sensitive` label on the ticket; no security-review pass required.

## Testing strategy

No new tests; no test edits. The acceptance bar is the unchanged existing suite:

- `go build ./...` — compiles with the new signatures and call site.
- `go vet ./...` — clean.
- `go test -race ./...` — passes unchanged (no test references these functions; confirmed by grep).
- Behaviour equivalence is guaranteed structurally: the call-site struct literal transcribes the current positional args field-for-field in the same order, and internal references are 1:1 renames. There is no code path whose behaviour can differ.

Manual reviewer check (belt-and-suspenders, matches the AC2 payoff): confirm in the diff that each `relayWiring{...}` field's value equals the arg previously at that position — the whole point is that this check is now a readable named-field diff rather than a positional count.

## Open questions

None. Field order, naming (`relayWiring`), and the whole-struct-threading decision are settled above. If the developer finds a 22nd wiring arg was added since this spec (unlikely on an S refactor), append it to the struct with a doc comment following the same rule.
