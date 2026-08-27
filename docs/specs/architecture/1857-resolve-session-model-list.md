# #1857 — Resolve a session's retained model list into a wire-ready payload

**Size:** s (re-checked against this spec in § Scope check)
**Labels:** `enhancement`, `size:s`, `security-sensitive`

## Files to read first

Read these before writing anything. This is the turn-1 data load; the design below assumes you have read all of it.

- `cmd/pyry/main.go` → `resolveBoundRunner`, `resolveBoundSession`, `resolveBoundRunSettings` — **the three existing twins.** Extract: the exact five-line refusal body (`Get` → `!ok || CurrentSessionID == ""` → `Lookup` → `err != nil`), and the doc-comment posture each carries. `resolveBoundRunner`'s doc states why the empty-`CurrentSessionID` guard is the isolation enforcement point; `resolveBoundSession`'s doc states why the duplication between twins is *accepted* rather than folded. This ticket adds the fourth twin and inherits both.
- `cmd/pyry/main.go` → `interruptRunner` — the type-assertion-off-`Session.Runner` idiom, including its "optional capability, a runner without it is inert (never a panic)" framing. Copy the shape.
- `cmd/pyry/session_model_hold.go` → `sessionModelHold.ModelList` — the accessor being composed. Extract: it returns a **deep copy**, it is **nil-receiver-safe**, and the bool is the only spelling of "nothing reported". Also read the type's `SECURITY:` paragraph — the no-logger-by-construction posture this spec preserves.
- `cmd/pyry/streamsup_runner.go` → `streamRunner.ModelList` — the per-session runner's one-line delegate, and the doc explaining why the method is deliberately **off** the `sessions.Runner` interface. Do not widen that interface.
- `internal/turnbridge/outbound.go` → `MapEvent`, `TurnContext` — extract the `turnevent.ModelList` arm (what it fills, what it ignores) and that the function returns `payload any`.
- `internal/protocol/interactive.go` → `ModelListPayload`, `ModelListPayload.MarshalJSON` — extract the three wire fields and the nil-`Models` normalisation, so you do not re-normalise here.
- `internal/sessions/pool.go` → `Pool.Lookup` — extract the one hazard sentence: **an empty id resolves to the bootstrap session**, not to an error. That is the whole reason for the second guard.
- `cmd/pyry/queue_state_v2.go` → `outstandingQueues` — the nearest analogue: an adapter composing an existing enumeration with an existing mapping into marshal-ready payloads. Match its scale and its "pure read, mints nothing, logs nothing" framing.
- `cmd/pyry/session_router_test.go` → `newRouterTestPool`, `stubRunner`, `TestSessionRouter_Route` — the test scaffolding to reuse: a real `*sessions.Pool` built without spawning claude, a minimal runner double, and the precedent for pinning the empty-binding guard against a real pool.
- `cmd/pyry/interactive_turn_v2_test.go` → `modelAnnouncedFixture`, `TestInteractiveTurnEmitterV2_ModelAnnouncedEventKindNamesTheVariant` — the established shape for a log-leak negative, and the measured reason a **conspicuous sentinel** is used instead of a realistic value like `"model"`.
- `internal/relay/v2session_seams.go` → `RunConfigFor` — read its `SECURITY:` paragraph. It is the posture this spec adopts verbatim for the untrusted id and for the "reported id comes out of the daemon's own record" rule.
- `docs/knowledge/features/turnbridge-package.md` § the `ModelList` row of the mapping table — extract: `TurnID`/`Seq` are ignored for this variant, `DroppedModels` is carried and never recomputed, and the note about `EffortLevels`/`TruncatedFields` sharing a backing array with the source event.
- `docs/knowledge/features/sessions-package.md` — the pool's lookup and bootstrap semantics in prose.

**Citation rule:** this spec names symbols, never line numbers, and your code comments must do the same. `make cite-guard` fails any `//`-comment citation resolving to a declaration, at any depth, with **no range exemption and no depth exemption**. Write ``the guard in `resolveBoundRunner` ``, never ``main.go:1277`` and never ``main.go:1277-1290``.

## Context

The daemon already asks each spawned child for its `initialize` reply (#1839), decodes the model menu, and retains it for the session's life (#1840). #1848 maps a `turnevent.ModelList` onto the wire shape and #1849 emits it on the live turn lane. What is missing is a way to answer, for a list **already held**, "which model menu does this conversation's session hold, as something a wire path can send".

Today that retention is reachable only from inside `cmd/pyry`, through a type assertion off `Session.Runner`, and there is no function turning it into a payload. #1858 — connect-time delivery — needs exactly that function and must not re-derive either the mapping (#1848 owns it) or the untrusted-id rules (the resolver family owns those).

This slice is **composition and refusal**, not construction. Every part exists: the accessor, the mapping, and three sibling resolvers whose refusal body is copied verbatim.

**No ADR is warranted.** This introduces no new pattern — it is the fourth member of an existing conversation-keyed resolver family, and the transport question it feeds is already settled in writing by `docs/protocol-mobile.md` § Reconnect / Backfill semantics (control state is a connect-time snapshot). The documentation phase should fold this into `docs/knowledge/features/sessions-package.md` or the `cmd/pyry` composition-root prose alongside the other three twins; it does not need a decision record.

## Design

### One new file, one new function

Create **`cmd/pyry/session_model_list.go`**. Do **not** add the function to `cmd/pyry/main.go`, for two independent reasons:

1. `outstandingQueues` — the analogue — lives in its own topic file, not in the wiring file.
2. `origin/feature/1493` is in flight and modifies `cmd/pyry/main.go`. A new file has no overlap; adding to `main.go` would manufacture a merge conflict at integration time. (This is why the § 1.5 overlap check did not block: the design touches no file a sibling branch touches.)

The function:

```go
// resolveBoundModelList answers the model menu the conversation's bound session
// currently holds, already shaped as a marshal-ready protocol.ModelListPayload.
func resolveBoundModelList(convReg *conversations.Registry, pool *sessions.Pool, convID string) (protocol.ModelListPayload, bool)
```

Unexported, `package main`, concrete `*conversations.Registry` + `*sessions.Pool` parameters — matching `resolveBoundRunner` and `resolveBoundSession` (only `resolveBoundRunSettings` narrows its pool parameter to an interface, and only because it needed one method that a real pool makes awkward to arrange in a test; here a real pool is straightforward, see § Testing strategy).

**It has no production caller in this ticket.** #1858 owns delivery. This is fine and does not need a placeholder wiring: staticcheck's `unused` check counts test-file usage as usage (verified empirically against `staticcheck 2026.1 (v0.7.0)`, the version `make check` installs — a `package main` function referenced only from `_test.go` produces no U1000 and exit 0). **Do not invent a production call site to keep the linter quiet.**

### Data flow

```
convID (untrusted, from a client)
   │
   ├─ convReg.Get(ConversationID(convID))          ── miss ─────────────► (zero, false)
   │        │ hit → conv
   ├─ conv.CurrentSessionID == ""                  ── unbound ──────────► (zero, false)
   │        │ non-empty
   ├─ pool.Lookup(SessionID(conv.CurrentSessionID))── err ──────────────► (zero, false)
   │        │ sess
   ├─ sess.Runner().(interface{ ModelList() … })   ── not implemented ──► (zero, false)
   │        │ lister
   ├─ lister.ModelList()                           ── nothing reported ─► (zero, false)
   │        │ list (deep copy, owned by us)
   ├─ turnbridge.MapEvent(list, TurnContext{ConversationID: string(conv.ID)})
   │        │                                      ── !ok ─────────────► (zero, false)
   ├─ payload.(protocol.ModelListPayload)           ── !ok ─────────────► (zero, false)
   │        │
   └────────► (payload, true)
```

Every arrow that leaves the chain returns the **zero payload and false**. There is no arm that returns a partially-filled payload and none that returns `true` with an empty `Models`.

### The six decisions worth stating

**1. The refusal is inherited verbatim; there is no third guard.** The body is `resolveBoundRunner`'s five lines with a different tail. Specifically:

- `convReg.Get` misses ⇒ refuse.
- `conv.CurrentSessionID == ""` ⇒ refuse, **before** `Pool.Lookup`.
- **Do not add a `convID == ""` pre-check.** `Pool.Lookup("")` returning the bootstrap session is the #678 hazard, and the `CurrentSessionID == ""` guard is what closes it: no empty string ever reaches `Lookup`, whatever the registry happens to hold. A third guard would be a second spelling of a check the family already makes, in only one of four twins, closing nothing. (See § Security review for the one residual this leaves and why it is not this ticket's to close.)

**2. The reported conversation id comes from the resolved record.** Fill `TurnContext.ConversationID` from `string(conv.ID)` — the value `Get` returned — **not** from the `convID` parameter. `Registry.Get` compares byte-exactly today, so the two spellings are currently identical and no test can separate them; the point is that the provenance stays correct if the lookup ever loosens (prefix match, case folding, normalisation). This is `RunConfigFor`'s stated posture: the reported id comes out of the daemon's own registry record. Record it in the doc comment as a deliberate choice, so a later reader does not "simplify" it back to the parameter.

**3. `TurnID` and `Seq` stay zero.** `MapEvent`'s `ModelList` arm ignores both — one `initialize` exchange per child is not even per-turn, and `turnMarkFor` answers `turnMarkNone` for the variant. Do not invent a turn id to fill them; there is no turn here.

**4. `MapEvent`'s `typ` return is discarded, and the payload assertion is the only discriminant.** `MapEvent` returns `payload any` because the payloads share no marker interface, so reuse costs one assertion back to `protocol.ModelListPayload`. Assert on the payload and discard `typ` with `_` — comparing `typ == protocol.TypeModelList` as well would be a strictly weaker second spelling of the same check. The consumer (#1858) names `protocol.TypeModelList` itself when it builds the envelope, exactly as `OutstandingQueues` and `OutstandingModals` carry no type field.

**5. An assertion failure answers "no list".** The `ok == false` arm and the failed payload assertion are unreachable from this call site — `MapEvent` maps every `turnevent.ModelList` and returns that concrete payload type — but both are reachable in the type system, so both need an answer. "No list" is the only answer that keeps AC 2's contract: a caller must never be handed a payload with an empty `Models`. Do not panic and do not log; write the branch, and say in the comment that it is the type system's arm rather than a state the daemon can reach.

**6. Reach the retention by type assertion, never by widening `sessions.Runner`.** Assert for `interface{ ModelList() (turnevent.ModelList, bool) }` off `sess.Runner()`, the way `interruptRunner` asserts for `Interrupt`. `internal/sessions`' `Runner` doc states the rule: the interface carries a method when its consumer sits *inside* `internal/sessions`; this consumer is in `cmd/pyry`, so widening buys no compile-time guarantee and drags every fake runner in two packages into the diff. A runner that does not implement it is a **refusal, not a panic** — and a nil `Runner` fails the assertion cleanly, so no nil check is needed.

### Value ownership

`sessionModelHold.ModelList` hands back a deep copy that this function is the sole owner of. `MapEvent` allocates a fresh `[]protocol.ModelOption`, but each row's `EffortLevels` and `TruncatedFields` cross by reference from that copy. Because the copy is per-call and never retained, the returned payload owns its slices outright — no aliasing back into the hold, and two callers never share a backing array. Do not add a second clone.

`DroppedModels` is carried through `MapEvent` from the decode. Never recompute it from `len(Models)` and never zero it.

## Concurrency model

**No goroutines.** This is a synchronous read called on the caller's goroutine — for #1858 that is a relay-leg goroutine handling an interactive open, which is why the underlying hold takes a lock at all.

**Three locks, acquired sequentially and never nested:**

| Step | Lock | Held across |
|---|---|---|
| `Registry.Get` | `Registry.mu` | nothing — released before `Lookup` |
| `Pool.Lookup` | `Pool.mu` (RLock) | nothing — released before `Runner()` |
| `sessionModelHold.ModelList` | the hold's leaf `mu` | nothing — released before `MapEvent` |

`Session.Runner()` takes no lock at all (`sup` is set once at construction). Because no lock is held while another is acquired, this function **introduces no edge into the daemon's lock order**. Preserve that: do not restructure the body so a lookup happens inside another lock's scope.

**The `Get` → `Lookup` window is a benign TOCTOU** and is the same window all three sibling twins have. A rotation landing between the two either leaves the id resolvable (we answer the menu of the session bound a moment ago) or makes it unresolvable (we refuse). Both are correct: a model list is a property of one child's `initialize` reply, and a rotation's fresh child reports its own. Do not add a re-read or a retry.

## Error handling

There are no `error` returns and no sentinels — every failure mode is the comma-ok `false`, and every one of them is **ordinary**, not exceptional:

| Mode | Cause | Answer |
|---|---|---|
| Unknown conversation | client named an id the registry lacks; stale client state | `(zero, false)` |
| Unbound conversation | conversation exists, no session bound yet | `(zero, false)` |
| Session gone | binding points at an id the pool lacks (daemon restart, eviction) | `(zero, false)` |
| Runner lacks the method | non-stream-json runner, or a test double | `(zero, false)` |
| Nothing reported | child never answered `initialize`, or has not yet | `(zero, false)` |
| Mapping declines / wrong payload type | unreachable from here; type-system arm | `(zero, false)` |

`Pool.Lookup`'s error is **discarded, not wrapped** — `resolveBoundRunSettings`'s stated reason: returning it bare is what keeps a hostile or malformed id from being reflected into a log line or a wire frame a caller builds from the error. Do not wrap it, do not return it, do not log it.

**No logger.** The function takes no `*slog.Logger`, the file declares no package-level logger, and nothing on this path calls `slog` at any level — including `slog.Default()`. There is no operational event here to record: the only thing a "why did it not resolve" line could add is the caller's untrusted id, which is exactly the channel #833 closes. `sessionModelHold` enforces this by construction and `resolveBoundRunSettings` says the same of itself ("takes no logger and must not grow one"). Preserve the property.

## Testing strategy

One new file, `cmd/pyry/session_model_list_test.go`. Reuse `newRouterTestPool`'s shape — `sessions.New` builds the bootstrap entry without spawning claude, so `Pool.Lookup` works against the in-memory map — and build the registry as a bare `&conversations.Registry{}` with `Create`, exactly as `TestSessionRouter_Route` does.

**Test double.** `stubRunner` deliberately does *not* implement `ModelList`, which makes it the ready-made fixture for the not-implemented refusal. Add a second double embedding or mirroring it that *does* implement `ModelList() (turnevent.ModelList, bool)`, returning a per-instance configured list and bool, so different sessions in one pool answer differently. Wire it through `sessions.Config.RunnerFactory`.

**Fixture values must be conspicuous sentinels, not realistic model names.** `modelAnnouncedFixture`'s measured reason applies directly: the log negative below is a `strings.Contains` over a whole captured log, and a natural value like `"sonnet"` or `"model"` risks being a substring of unrelated text, making the negative red against a correct implementation. Use something like `ZZMODELVALUEZZ` / `ZZEFFORTZZ` / `ZZDISPLAYZZ`.

Scenarios, as bullets — write them in the project's table-driven idiom where they share a shape:

**Happy path (AC 1)**
- A conversation bound to a session whose runner holds a two-entry list resolves to `ok == true`, `ConversationID` equal to that conversation's id, `Models` matching the held entries field-for-field (all six `ModelOption` fields, including `SupportsAutoMode` and both slice fields), and `DroppedModels` carrying the held value.
- Give the two entries **distinguishable** values in every field — the turnbridge overview records that same-shaped rows let a transposition pass. Set a non-zero `DroppedModels` so a hard-coded `0` reddens.
- Assert the payload's `EffortLevels` and `TruncatedFields` are the held values, and that `TruncatedFields` being nil stays nil (normalisation is `MarshalJSON`'s job, not this function's).

**Unreported (AC 2)**
- A conversation bound to a session whose runner implements `ModelList` but reports `false` resolves to `ok == false` and a zero payload. Assert `ok == false` *and* that the returned `Models` is empty — an empty payload with `ok == true` is the exact thing AC 2 forbids.

**Refusals (AC 3)** — the load-bearing group. **Arm the bootstrap session's runner with a distinguishable sentinel list in every one of these**, so that a missing guard produces a *visible* wrong answer rather than an empty one:
- Unknown conversation id ⇒ `ok == false`.
- Conversation exists with `CurrentSessionID == ""` ⇒ `ok == false`. **This is the sole-red mutant for the isolation guard**: delete the `CurrentSessionID == ""` clause and `Pool.Lookup("")` hands back the bootstrap session, so this case flips to `ok == true` carrying the bootstrap's sentinel models. Assert both `!ok` and (on the failure message) that the bootstrap sentinel is not present, so the failure output names what leaked.
- `convID == ""` against a registry holding no empty-id conversation ⇒ `ok == false`, same reasoning, one step earlier.
- Binding points at a session id the pool does not hold ⇒ `ok == false`.
- Runner does not implement `ModelList` (plain `stubRunner`) ⇒ `ok == false`, and the test must not panic.

**Cross-conversation isolation (AC 3, second half)**
- Two conversations bound to two different pool sessions, each runner holding a *different* sentinel list. Resolving conversation A returns A's session's models and never B's; resolving B returns B's. This is the pin that a hard-coded or shared lookup cannot pass.

**No logging (AC 4)**
- Drive a sentinel-valued list through the happy path with `slog.SetDefault` pointed at a `bytes.Buffer` (restore the previous default in `t.Cleanup`), at `slog.LevelDebug`, and assert the buffer is **empty** and in particular contains none of the sentinel values.
- **This test must not call `t.Parallel()`** — `slog.SetDefault` is process-global and would race every other parallel test in the package. Say so in a comment.
- Be honest about what it pins: the function takes no logger parameter, so the *structural* half of AC 4 is enforced by the signature and reviewed, not tested. What this test does catch is the one real regression shape — someone reaching for the package-level `slog.Info` / `slog.Default()` instead of adding a parameter. A mutant adding `slog.Info("resolved", "models", list.Models)` to the happy path reddens it; nothing else in the package does.

**Do not test**
- `ModelListPayload.MarshalJSON`'s nil-to-`[]` normalisation — `internal/protocol` owns and pins it.
- `MapEvent`'s field-by-field mapping as such — `internal/turnbridge` owns and pins it. This file asserts the composition end-to-end, which covers the fields incidentally; it does not re-table the mapping.

**Gate:** `make check` (this touches nothing under `internal/e2e/realclaude`, so `make preship` is not required for this ticket).

## Scope check

Re-applied to this written spec, not to the sketch:

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **1** (`cmd/pyry/session_model_list.go`, new) |
| Total written work (production + tests + helpers + per-branch logs + spec-doc edits) | ≤ 400 | **~300** (~30 production incl. doc comment, ~250 test incl. the runner double, 0 log calls, 0 spec edits) |
| New exported types or interfaces | ≤ 5 | **0** (`package main`, unexported; the asserted interface is anonymous and inline) |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** (delivery is #1858; no existing symbol changes signature) |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches | ≤ 10 | **6** |

Not refactor-shaped: no interface widening (explicitly forbidden by § Design decision 6), no type replacement, no import flip. Nearest analogue re-derived at `878dbd88`: `outstandingQueues` shipped as **+18 production / +104 test = 122 insertions**, and this adds the conversation-keyed resolution and five more refusal branches on top of a comparable core. Within the table on every line.

## Open questions

- **None blocking.** The signature `(convReg, pool, convID) → (protocol.ModelListPayload, bool)` is fixed by #1858's stated seam shape (a conversation-keyed closure returning a marshal-ready `protocol.*Payload`, matching `OutstandingModals` / `OutstandingQueues` / `RunConfigFor`). If #1858's spec ends up wanting a different arity, it wraps this function in a closure at the `cmd/pyry` wiring point rather than changing it here.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The design has exactly one untrusted input, `convID`, and one explicit boundary: `convReg.Get`. Past that point every value is daemon-authored — the conversation record, the session id, the retained list from the daemon's own child. `convID` is used as a map-style lookup key and for nothing else: it is never returned (`conv.ID` is, per § Design decision 2), never joined into a path, never passed to a subprocess, never wrapped into an error, never logged. The boundary is a single function rather than scattered, and the returned `bool` is the type-system signal that the payload must not be read on refusal — the comma-ok discipline `RunConfigFor` documents. Callers hold only `protocol.ModelListPayload`, a closed struct of strings, ints and a bool.

- **[Trust boundaries — residual, SHOULD FIX at a different layer]** `conversations.Registry.Create` appends **unvalidated**, so a registry could in principle hold `Conversation{ID: "", CurrentSessionID: "sess-x"}`. A client sending `conversation_id: ""` would then resolve that record and receive `sess-x`'s menu in a payload whose `conversation_id` is `""`. This is bounded and is **not** the #678 bootstrap hazard: `CurrentSessionID` is non-empty and real, so `Pool.Lookup("")` is never reached and the shared bootstrap session is never addressed. The ids in question are daemon-minted at `Create`'s call sites, not client-supplied. Critically, **all four twins in the family have identical exposure**; closing it here alone would make one resolver inconsistent with three siblings while leaving the actual gap open everywhere else. If it ever needs closing, it belongs at `Registry.Create` — once, for all four. Explicitly **not** fixed by a `convID == ""` guard in this function, per the ticket's own warning that a redundant third guard is the likelier failure than none.

- **[Tokens, secrets, credentials]** Not applicable by design. This path handles no token, key or credential. The model menu is claude-authored configuration data, not a secret — `ModelListPayload` already crosses the wire outbound on the live turn lane (#1849). Nothing is generated, stored, rotated or revoked here.

- **[File operations]** Not applicable by design. The function performs no filesystem access of any kind: no path is constructed, no file opened, no `os.Stat`, no write. The two registries it reads are in-memory (`Registry.Get` reads a slice under a mutex; `Pool.Lookup` reads a map under an RLock) — neither touches disk on this path, so there is no traversal, TOCTOU-on-path, symlink, permission-mode or atomic-write question to answer.

- **[Subprocess / external command execution]** Not applicable by design. No `exec.Command`, no process signalling, no environment manipulation. `sess.Runner()` returns an already-constructed runner and this path calls exactly one method on it — `ModelList()`, an accessor over a mutex-guarded field that spawns nothing and writes nothing to the child's stdin.

- **[Cryptographic primitives]** Not applicable by design. No randomness (security-relevant or otherwise), no hashing, no comparison against a secret, no key material. The one equality comparison on the path is `conv.CurrentSessionID == ""` against a constant — not attacker-controlled-vs-secret, so constant-time comparison is not indicated.

- **[Network & I/O]** No findings. The function performs no I/O and reads no socket, so input-size caps, header validation, timeouts and slow-loris are all properties of the caller's transport (#1858) rather than of this resolver. The one size question that *is* this path's — how large a payload can it hand back — is already bounded upstream and not re-bounded here: `streamsup`'s `maxModelListEntries` / `maxModelResolved` / `maxModelEffortLevel*` cap the decode, and `DroppedModels` reports what those caps discarded. The retained list is therefore bounded at the moment it enters `sessionModelHold`, and this function returns a copy of a bounded value. **Noted for #1858:** the resolver imposes no additional cap, so envelope-size discipline for the connect-time frame is the delivery ticket's to state.

- **[Error messages, logs, telemetry]** No findings, and this is AC 4's category. Nothing is logged at any level. `Pool.Lookup`'s error is discarded rather than wrapped, specifically so a malformed or hostile id cannot be reflected into a caller-built log line or wire frame — `resolveBoundRunSettings`'s stated reason. The function takes no logger and must not grow one; the #833 posture (model / effort / YOLO values are NEVER logged at any level) is preserved **by construction**, matching `sessionModelHold`, which has no logger field and whose constructor takes none. The residual regression channel — a developer reaching for the package-level `slog.Default()` rather than adding a parameter — is the one thing the § Testing strategy log-negative actually pins, and it is pinned with a whole-buffer emptiness assertion plus per-sentinel checks. No telemetry or metrics are emitted.

- **[Concurrency]** No findings. Three locks are acquired **sequentially and never nested** (`Registry.mu`, then `Pool.mu` RLock, then the hold's leaf `mu`), each released before the next is taken, so the function adds no edge to the daemon's lock order — the property `sessionModelHold`'s doc states for its own leaf lock. No goroutine is spawned, so there is no lifecycle or leak question. There is no check-then-mutate anywhere: the function is a pure read that mints nothing and mutates nothing, so shutdown mid-call leaves no partial state. The `Get` → `Lookup` TOCTOU window is real, is shared verbatim with all three sibling twins, and is benign in both directions (§ Concurrency model) — a rotation racing the read yields either the previously-bound session's menu or a refusal, never another conversation's menu, because the session id is read once from the record and threaded as one value.

- **[Threat model alignment]** The relevant threat is `docs/protocol-mobile.md` § Security model's cross-conversation isolation, restated as #678 AC #4: a client-driven verb must never resolve to the shared bootstrap session or to a session the caller did not legitimately address. The design addresses it at the `conv.CurrentSessionID == ""` guard, placed **before** `Pool.Lookup` precisely because `Pool.Lookup("")` returns the bootstrap session rather than an error, and it is pinned by a sole-red test in which the bootstrap runner holds a distinguishable sentinel list (§ Testing strategy, "Refusals"). The complementary half — that a resolvable conversation gets *its own* session's menu and never a sibling's — is pinned by the two-conversation isolation test. **Out of scope, named:** the transport-level questions (who may open an interactive conn, envelope sealing, replay behaviour) belong to #1858 and to the already-shipped Noise/relay layer, not to this resolver.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
