# #1609 — one conversation-keyed run-configuration seam

Split from #1587. Blocked-by #1608 (landed, `bd64f5b`).

## Files to read first

Read these before writing anything. Each entry names the **symbol**, not a line — resolve with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `internal/relay/v2session_seams.go` | `V2SessionConfig` — its `SnapshotSettings`, `SnapshotUsage`, `BootstrapSessionID` fields | The three seams the new one replaces; the doc-comment idiom every field follows (what it reports → the nil/optional contract → why the type is primitive → a security-posture paragraph). The paragraph inside `BootstrapSessionID`'s doc that records the all-three-move-together decision is the one this ticket amends. |
| `cmd/pyry/main.go` | `resolveBoundSession` | The refusal to inherit **verbatim**: registry miss, then `CurrentSessionID == ""`. Its doc says why the second guard is load-bearing — `Pool.Lookup("")` returns the bootstrap. |
| `cmd/pyry/main.go` | `snapshotSettings` (the closure built in `runSupervisor`) | The precedent for decoding `sessions.SessionSettings` into primitives at the composition root so the value crossing into `relay.go` carries no `internal/sessions` type. |
| `internal/sessions/pool.go` | `SettingsFor` | The whole contract: `ErrSessionNotFound` for an unheld id; `""` deliberately **not** special-cased; one `RLock` acquisition; the read-modify-write warning; the "`DefaultSettings` must not be rewritten to call it" note. |
| `internal/sessions/pool.go` | `DefaultSettings` | Contrast only — why the bootstrap read stays its own single-acquisition method. |
| `cmd/pyry/snapshot_usage.go` | `snapshotUsageFor`, `bootstrapSnapshotUsage` | The by-id usage reader (`nil` iff no sessions dir) and the builder-time nil-guard pattern. §"Why the two nils differ" below diverges from `bootstrapSnapshotUsage` deliberately — read its doc so you can see the divergence is chosen, not copied wrong. |
| `cmd/pyry/relay.go` | `relayWiring`, `startRelayV2`, `boundSessionIDForActive` | Where a wiring field is declared and threaded; where the `V2SessionConfig` literal is populated; the in-file precedent for a named, unit-testable resolver pulled out of otherwise-untestable wiring. |
| `cmd/pyry/session_router_test.go` | `newRouterTestPool`, `stubRunner` | **Reuse.** A real `*sessions.Pool` that spawns nothing. Same package, so a new `_test.go` calls it directly. |
| `cmd/pyry/bound_session_active_test.go` | `TestBoundSessionIDForActive` | The registry fixture idiom (`&conversations.Registry{}` + `Create`) and the fail-closed subtest naming this spec's table should mirror. |
| `cmd/pyry/snapshot_usage_test.go` | `TestSnapshotUsageFor_SiblingTranscriptIsNeverRead` | The cross-session confidentiality assertion shape — one reader, asked for **both** ids, each returning its own figures. AC #3's matrix is the same shape one layer up. |
| `internal/transcript/transcript.go` | `StatByID`, `ValidStem` | The UUID-shape validation that happens **before** any `filepath.Join`. This is the pre-existing floor; the ticket's security property is a second, upstream guarantee stacked on it. |
| `docs/knowledge/codebase/1608.md` | — | The blocker's handoff, including the open question §"Why no authorisation layer" below answers. |
| `CODING-STYLE.md` | § "Comments — Citing Other Code" | Cite symbols, never `:NNN`. `make cite-guard` runs inside `make check`. |

## Context

`relay.V2SessionConfig` reports run configuration through three bootstrap-scoped seams — `SnapshotSettings`, `SnapshotUsage`, `BootstrapSessionID`. None takes a conversation. Their coupling is recorded, not accidental: they must describe the *same* session or a client reads one session's values and writes to another.

This ticket builds the conversation-keyed replacement and wires it. It changes no reported value and consults nothing new — nothing reads the new seam yet, so every reply is byte-identical. Making `request_session_settings` consult it, and retiring `BootstrapSessionID`, is #1610.

Keyed by conversation, three separate seams cannot express two things: that everything reported describes one session, and that a conversation did not resolve at all. The second matters because **an empty model or effort is a real reported value** meaning "inherited daemon default" — so zero values coming back from a settings seam are ambiguous with a conversation that failed to resolve. One seam returning the whole set alongside one `ok` resolves that once, and turns the AC #1 invariant from a warning in a comment into a property of a type.

## Design

Three pieces: the seam (in `internal/relay`), the settings half (at the composition root), the composer (at the wiring point).

### 1. The seam — `internal/relay/v2session_seams.go`

One new exported type and one new `V2SessionConfig` field. Both carry primitives only, so `internal/relay`'s import block gains nothing (AC #4).

```go
// RunConfig is one conversation's complete run configuration ...
type RunConfig struct {
	SessionID    string
	Model        string
	Effort       string
	YOLO         bool
	UsedTokens   int
	WindowTokens int
}

// RunConfigFor reports the named conversation's own run configuration ...
RunConfigFor func(conversationID string) (RunConfig, bool)
```

Contract the doc comment must fix:

- **Comma-ok, not an in-struct flag.** `false` ⇒ the conversation is not addressable and every field of the returned `RunConfig` is at its zero. A caller must not read the struct on `false`. `true` with an empty `Model`/`Effort` is a *real* answer — "inherited daemon default, no per-session override", the same meaning `SnapshotSettings` already carries.
- **One session, all six values.** The doc must state that `SessionID` names the session the other five describe, and *why* it is guaranteed (§"Why one pool acquisition"). This is the sentence that replaces the warning currently sitting in `BootstrapSessionID`'s doc.
- **Optional, per house idiom.** `nil` ⇒ no consumer can resolve any conversation. Foreground / v1 / unwired. Since nothing consults it in this ticket, `nil` changes no wire byte either way.
- **Security posture paragraph.** A session id is a routing key, not a secret — it already crosses the wire both directions. The seam performs no mutation and parses no input; `conversationID` is used only as a lookup key. Note that the refusal *is* the control: `false` means the caller addresses nothing, never the bootstrap.

Also in this file: amend the paragraph in `BootstrapSessionID`'s doc that says all three seams "move together" when the decision is revisited. It is now stale in one direction — a conversation-keyed seam exists next to it. Two or three sentences: `RunConfigFor` is the replacement; the three stay bootstrap-scoped with their current readers and values; #1610 retires this one. Do not rewrite the surrounding contract.

### 2. The settings half — `cmd/pyry/main.go`, beside `resolveBoundSession`

`internal/sessions` stays at the composition root. Add:

```go
// boundRunSettings is the settings half of a conversation's run configuration ...
type boundRunSettings struct {
	sessionID string
	model     string
	effort    string
	yolo      bool
}

// sessionSettingsReader reads one pool session's persisted run settings ...
type sessionSettingsReader interface {
	SettingsFor(id sessions.SessionID) (sessions.SessionSettings, error)
}

// resolveBoundRunSettings is the run-configuration twin of resolveBoundSession ...
func resolveBoundRunSettings(convReg *conversations.Registry, pool sessionSettingsReader, convID string) (boundRunSettings, bool)
```

Behaviour, in order — three rejects, one accept, no `Pool.Lookup` anywhere:

1. `convReg.Get(conversations.ConversationID(convID))` misses ⇒ `(zero, false)`. An empty `convID` lands here: no conversation carries an empty id, so the pool is never touched.
2. `conv.CurrentSessionID == ""` ⇒ `(zero, false)`. Inherited verbatim from `resolveBoundSession`; do not re-derive a weaker guard. Reject **before** the pool.
3. `pool.SettingsFor(sessions.SessionID(conv.CurrentSessionID))` returns non-nil ⇒ `(zero, false)`. Covers the pool-no-longer-holds-it case (idle eviction, `Pool.Remove`).
4. Otherwise `(boundRunSettings{sessionID: conv.CurrentSessionID, model: s.Model, effort: s.Effort, yolo: s.YOLO}, true)`.

Three design points the doc comment must record:

- **A struct, not a 4-tuple.** Three adjacent same-typed strings in a return list transpose silently; a named-field construction makes the swap a visible edit. Same reasoning `relayWiring`'s own doc gives for named-field wiring (#917).
- **Why the 1-method interface.** Declared at the consumer per CODING-STYLE. `*sessions.Pool` satisfies it with no adapter. It exists for a stated testing need — AC #2's "no pool session lookup is performed at all" is an observation about *calls*, and zero values cannot make it, because a resolved-but-default session reports zeros too. Do not widen it beyond `SettingsFor`.
- **The error is swallowed, not wrapped.** `SettingsFor` returns its sentinel bare precisely so a hostile id cannot be reflected into a caller-built log line or wire frame. Discard it. **Do not add a logger to this resolver** — `conversationID` is untrusted network input and must never reach a log line, an error string, or a filesystem path.

At the `relayWiring` literal in `runSupervisor`, bind the closure:

```go
runSettings: func(convID string) (boundRunSettings, bool) {
	return resolveBoundRunSettings(convReg, pool, convID)
},
```

### 3. The composer — `cmd/pyry/relay.go`, beside `boundSessionIDForActive`

New `relayWiring` field (doc it like its neighbours):

```go
runSettings func(convID string) (boundRunSettings, bool)
```

The value never crosses into `internal/relay` — only the composed result does — so a `cmd/pyry` type in this field is free.

New builder:

```go
// runConfigFor composes the two halves of the conversation-keyed run-configuration seam ...
func runConfigFor(
	resolve func(convID string) (boundRunSettings, bool),
	usage func(sessionID string) (usedTokens, windowTokens int),
) func(convID string) (relay.RunConfig, bool)
```

- `resolve == nil` ⇒ return `nil` at **build** time, before any closure exists (the #1608 structural-not-a-promise pattern). Foreground / v1.
- `usage == nil` ⇒ return a **working** closure whose resolved answers carry `UsedTokens`/`WindowTokens` at zero. See §"Why the two nils differ".
- The returned closure: `resolve(convID)`; on `false` return `(relay.RunConfig{}, false)` and **call `usage` not at all**; on `true` build the `relay.RunConfig`, calling `usage(b.sessionID)` only when `usage != nil`.

In `startRelayV2`, beside the existing `snapshotUsage` build:

```go
runConfig := runConfigFor(w.runSettings, snapshotUsageFor(w.claudeSessionsDir))
```

then `RunConfigFor: runConfig,` in the `V2SessionConfig` literal, with a wiring comment in the house style: what it reports, that nothing consults it yet, and that #1610 makes `request_session_settings` read it.

`bootstrapSnapshotUsage(w.claudeSessionsDir, w.bootstrapIDFn)` and the `SnapshotUsage:` line stay **exactly** as they are. Calling `snapshotUsageFor` a second time over the same dir builds a second stateless closure; that is cheaper than re-shaping a seam this ticket is not allowed to change.

### Why one pool acquisition

`SettingsFor` alone answers both questions this resolver asks — "does the pool hold this id" and "what are its settings" — under one `RLock`. A shape that resolves through `Pool.Lookup` first and reads settings through a second call opens a window (idle eviction is live: `Pool.IdleTimeout`) in which the reported id names a session the reported settings no longer describe. That window *is* the AC #1 invariant, so the single acquisition is the design, not an optimisation.

Two consequences worth stating in the doc comment, because a later reader will otherwise "simplify" them away:

- **`Pool.Lookup` is absent on purpose.** `Lookup("")` returns the bootstrap; `SettingsFor("")` is an ordinary map miss. Building on `SettingsFor` means the fall-through-to-bootstrap failure mode has no expression in this code path at all — a second, structural guarantee on top of the step-2 guard, not a replacement for it.
- **`DefaultSettings` is untouched.** Its doc forbids rewriting it as `SettingsFor(BootstrapID())` — two acquisitions with a rotation window between them. This ticket adds a caller of `SettingsFor`; it changes nothing about how the bootstrap's settings are read.

### Why the two nils differ

`bootstrapSnapshotUsage` returns `nil` when *either* half is unwired, because both halves are required to report anything at all. `runConfigFor` must not copy that: AC #3's last clause says a daemon with no sessions directory still resolves both conversations, reporting their own ids and settings with the context figures at zero. An unwired usage half degrades two integers; it does not make a resolved conversation unresolvable. State the contrast in the doc comment — the neighbouring shape is close enough to be pattern-matched wrong.

### Concurrency model

No goroutines, no channels, no shutdown sequence. Every piece is a synchronous read on the caller's goroutine.

- `conversations.Registry.Get` takes the registry mutex and linear-scans; `Pool.SettingsFor` takes `p.mu.RLock` once. **They are taken in that order and never nested** — `Get` has returned a value copy before the pool is touched. No lock is held across the transcript read.
- `sessions.SessionSettings` is a value type; the returned copy aliases no pool state and may be one concurrent `UpdateSettings` stale. That staleness is the contract `DefaultSettings` and `SnapshotSettings` already carry.
- The usage read (`transcript.StatByID` + `contextwindow.Read`) is filesystem I/O outside every lock. It can race a rotation and report a transcript that has just moved; the outcome is the deterministic fresh-session report, never an error and never another session's figures.
- Callers today: none. #1610 will call it from an app-frame worker goroutine; nothing here assumes a particular goroutine.

### Error handling

No error crosses this seam, by design — the sole failure signal is `ok == false`.

| Condition | Result |
|---|---|
| `convID == ""` | `(zero, false)`, pool untouched |
| unknown conversation | `(zero, false)`, pool untouched |
| `CurrentSessionID == ""` | `(zero, false)`, pool untouched |
| pool no longer holds the id (`ErrSessionNotFound`) | `(zero, false)`, usage untouched |
| resolved, no sessions dir (`usage == nil`) | `(RunConfig{id, model, effort, yolo, 0, 0}, true)` |
| resolved, transcript missing / malformed / unreadable | `(RunConfig{…, 0, defaultWindow}, true)` — `snapshotUsageFor`'s existing fresh-session report, unchanged |

Nothing here logs. Nothing here wraps an error with a caller-supplied id.

## Testing strategy

New file `cmd/pyry/run_config_test.go`. Scenarios as bullets — write them in the package's table-driven idiom, `t.Parallel()`, stdlib only.

**A. `resolveBoundRunSettings` against a counting double.** A `sessionSettingsReader` double holding a map of id → `sessions.SessionSettings`, recording every id it is asked for. Registry fixture per `TestBoundSessionIDForActive`: conversations bound to session A, bound to session B, unbound (`CurrentSessionID: ""`), and bound to an id absent from the double.

- empty conversation id ⇒ `ok == false`, zero struct, **and the double recorded zero calls**
- unknown conversation id ⇒ same, zero calls
- unbound conversation ⇒ same, zero calls — the #678 isolation guard, restated at this seam
- across the whole table, **the double is never asked for `""`** — this is the assertion that maps to "can never reach the lookup that resolves `""` to the bootstrap"
- dangling binding (pool dropped the session) ⇒ `ok == false`, zero struct, and the double recorded **exactly one** call, with the non-empty id
- conversation A ⇒ A's id and A's model/effort/YOLO; conversation B ⇒ B's, with A and B given deliberately different settings including one `YOLO: true` and one empty `Model`. Neither reports the other's. This is AC #3's core and the sole red for an argument-ignoring resolver.
- a bound conversation whose session carries the zero `SessionSettings` ⇒ `ok == true` with empty model/effort and `yolo == false` — the row that proves zeros are a real answer, not a refusal

**B. `resolveBoundRunSettings` against a real `*sessions.Pool`.** Reuse `newRouterTestPool`. Two rows, pinning that the double models the real contract:

- a conversation bound to `pool.Default().ID()` ⇒ resolves, reporting that id
- a conversation bound to a freshly-minted `sessions.NewID()` the pool never held ⇒ `ok == false` (real `ErrSessionNotFound`)

**C. `runConfigFor` composition.** A stub resolver and a recording usage func.

- `resolve == nil` ⇒ the builder returns a nil seam (assert `== nil`, before calling anything)
- `usage == nil`, resolver says yes ⇒ `ok == true`, id/model/effort/YOLO carried through, `UsedTokens == 0 && WindowTokens == 0`. AC #3's no-sessions-directory clause; sole red for a builder that copies `bootstrapSnapshotUsage`'s either-half-nil rule.
- `usage` wired, resolver says yes ⇒ the usage func was called with **the resolved session id** (not the conversation id, not `""`), and its figures land on the returned struct
- resolver says no ⇒ `ok == false`, zero struct, and the usage func recorded **zero** calls — an unresolvable conversation reaches no transcript path at all
- two conversations resolving to different session ids, one usage func ⇒ each gets its own figures. Same shape as `TestSnapshotUsageFor_SiblingTranscriptIsNeverRead`, one layer up.

**D. Import discipline (AC #4).** Not a Go test — a command whose output goes in the PR body:

```
go list -f '{{range .Imports}}{{println .}}{{end}}' ./internal/relay | grep pyrycode
```

Must list exactly: control, devices, dispatch, eventring, identity, noise, protocol, transport — and neither `internal/sessions` nor `internal/contextwindow`. **Do not use `go list -deps`**: it matches `internal/sessions` today, before this change, through `internal/control` (an edge from #29, unrelated to this work). Spending turns chasing that transitive edge is out of scope.

**E. No wire change (AC #5).** `make check` green, including the fake-daemon e2e suite. `internal/e2e.TestRelayV2_StreamRequestSessionSettings` stays green **unmodified** — nothing consults the new seam, so `session_settings` and `screen_snapshot` replies are byte-identical by construction.

**F. Gates.** `go test -race ./cmd/pyry/`, `go vet ./...`, `make cite-guard`, then `make check`.

Mutation-verify via `go test -overlay=<abs-path json>` (no worktree writes) that each of these is killed by a predicted, distinct assertion: dropping the `CurrentSessionID == ""` guard; calling `usage` before checking `ok`; making `runConfigFor` return nil when `usage == nil`; ignoring the `convID` argument.

## What this ticket does NOT do

- No handler reads `RunConfigFor`. `request_session_settings` and `request_snapshot` are untouched.
- `SnapshotSettings`, `SnapshotUsage`, `BootstrapSessionID` keep their current readers, wiring, and values. Retiring or re-keying any of them is #1610.
- `bootstrapSnapshotUsage`, `snapshotUsageFor`, `Pool.SettingsFor`, `Pool.DefaultSettings`, `resolveBoundSession` are all called, not modified.
- No new protocol field, no wire-contract change, no `docs/knowledge/` file (the documentation phase writes `codebase/1609.md` after merge).

## Open questions

- **Naming.** `RunConfig` / `RunConfigFor` sit in a file whose other `Config` is `V2SessionConfig` (manager parameterisation). The doc comment disambiguates; if the developer finds a genuinely clearer pair while writing, take it and say why in the PR body. `RunConfig` currently has no collision anywhere in the repo.
- **staticcheck on a set-but-unread field.** `RunConfigFor` is written by `cmd/pyry` and read by nobody until #1610. Exported identifiers in a non-`main` package are treated as used by `unused`'s default mode, so this should be clean — but `make check` runs `staticcheck ./...` with no config, so confirm rather than assume. If it does fire, the fix is a `V2SessionConfig` construction test in `internal/relay` that reads all six fields back through the seam — which also pins AC #4's "primitives only" at compile time.
- **`Registry.Get` is a linear scan under a mutex**, not a map lookup. At today's conversation counts this is noise, and #1610's consumer is one frame per request. If a future consumer calls the seam per event, that scan is the first thing to measure. Not this ticket's problem; recorded so it is not rediscovered.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and this ticket narrows one.** `conversationID` is untrusted network input. It crosses exactly one boundary, in `resolveBoundRunSettings` step 1, where it is used **only** as a lookup key into the daemon's own `conversations.Registry` and is never returned, joined into a path, logged, or wrapped into an error. Everything downstream of that lookup is daemon-owned data: the session id comes out of the daemon's own registry record, and `Pool.SettingsFor` must confirm the pool holds it before anything is reported. So the id that will reach `transcript.StatByID` under #1610 is always one the pool holds — never a caller-supplied string. That answers the question `docs/knowledge/codebase/1608.md` left open, and answers it **structurally rather than with an authorisation layer**: there is no per-device conversation ownership in this daemon (`list_conversations` returns every conversation to any paired device), so "the daemon hosts it" is the whole of the existing posture. This ticket neither widens nor narrows it. Adding a per-device check here would be a new access-control model smuggled in under a read seam — out of scope, and it belongs with whoever introduces conversation ownership.
- **[Trust boundaries] No findings — the refusal is fail-closed in the only direction that matters.** This seam is the resolution point for a control set that includes `YOLO` (bypass-permissions). Every unresolvable state returns `ok == false` with a zero struct, so the failure mode is "the client is told it can address nothing", never "the client is silently pointed at the bootstrap session". The step-2 `CurrentSessionID == ""` guard is inherited verbatim from `resolveBoundSession` rather than re-derived, and building on `SettingsFor` (no `""`-is-bootstrap convention) rather than `Pool.Lookup` means the fall-through-to-bootstrap failure has no expression in this path at all. AC #2's call-counting assertions are what keep that structural, since zero values alone cannot distinguish a refusal from a resolved default session.
- **[File operations] No findings.** The only filesystem touch is the existing `snapshotUsageFor` reader, unmodified. `transcript.StatByID` calls `ValidStem` (a UUID-shape regexp) **before** `filepath.Join`, so a malformed or hostile id is rejected with zero filesystem access — path traversal is blocked at that floor regardless of what this ticket does. The stat-then-open gap is a pre-existing TOCTOU whose worst outcome is bounded by design: `contextwindow.Read`'s error path collapses to `Read("")`, the same deterministic fresh-session report, and the error — which wraps the full path — is discarded rather than propagated, so no resolved path reaches any surface. `snapshotUsageFor`'s `dir == ""` guard, which prevents a *relative* `<id>.jsonl` join against the daemon's cwd, stays inside the reader and is not relocated. This ticket creates no files and sets no modes.
- **[Error messages, logs, telemetry] SHOULD FIX — enforce it in review.** The resolver takes no logger and must not grow one; `Pool.SettingsFor` returns `ErrSessionNotFound` bare specifically so a caller cannot reflect a hostile id into a log line or wire frame, and this spec discards it rather than wrapping it with `convID`. The risk is a developer adding a "debug why it didn't resolve" log line with `"conversation_id", convID` — the shape `handleInterrupt` and the v2 dispatch path legitimately use elsewhere, so it will look idiomatic. It is not, here: this is a pure read seam with no operational event to record. **Code-review must check that no new `slog` call appears in `resolveBoundRunSettings` or `runConfigFor`.** Not a MUST FIX because the spec prescribes no logger and nothing in the design needs one.
- **[Concurrency] No findings.** Two locks, never nested: `Registry.Get` returns a value copy and releases before `Pool.SettingsFor` takes `p.mu.RLock` once. No lock is held across the transcript read. The single `SettingsFor` acquisition is itself the mitigation for the read-tearing hazard the technical notes flag — a Lookup-then-SettingsFor shape would let idle eviction (`Pool.IdleTimeout`) land between the two calls and report an id whose settings describe a session that is gone. The returned `SessionSettings` is a value copy, so no caller can mutate pool state through it. No goroutines are spawned, so none can leak.
- **[Tokens, secrets, credentials] Not applicable — nothing here handles either.** A session id is a routing key, not a secret: it already crosses the wire outbound on `session_transition` and inbound on `set_session_settings`. The seam holds no token, no key material, and no credential; `StaticPriv` and the Noise state are in a different file and untouched.
- **[Subprocess execution] Not applicable.** No `exec.Command`, no argv composition, no environment handling. `Pool.SettingsFor` is a map read; the argv-recompose path lives in `UpdateSettings`, which this ticket does not call. Notably the seam is read-only — it cannot enable `YOLO`, only report it.
- **[Cryptographic primitives] Not applicable.** No randomness, no comparison against a secret, no key or nonce. `sessions.NewID` appears only in test fixtures.
- **[Network & I/O] Not applicable to this slice.** The seam is never reached from a frame in this ticket (AC #5). Its future caller's input caps, timeouts, and per-conn frame budget are the v2 manager's, unchanged. Worth recording for #1610: `Registry.Get` is a linear scan under a mutex, so a per-frame caller makes this seam's cost O(conversations) — see Open questions.
- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model's relevant threat here is cross-session leakage — one conversation reading another's state. AC #3's independence matrix and AC #2's never-`""` assertion are the direct tests for it, and the single-acquisition design is what makes the AC #1 invariant hold under concurrent eviction. Device-scoped conversation ownership is named above as out of scope with no owning ticket, because it does not exist anywhere in this daemon today.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
