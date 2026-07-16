# Spec #1041 — re-scope the `v1TypeSet` / `IsV1Compatible` inbound-frame allowlist to its v2-only meaning

**Ticket:** [#1041](https://github.com/pyrycode/pyrycode/issues/1041) (split from #966)
**Size:** S — behaviour-preserving rename + doc-comment edits. Not `security-sensitive` (behaviour-preserving refactor + documentation edits — both explicit security-label carve-outs; no design surface to audit; the accepted/rejected type partition is unchanged and pinned by two deterministic guards).

## Files to read first

- `internal/protocol/envelope.go:79-142` — the two symbols to rename: `IsV1Compatible` (func + doc, `:79-109`) and `v1TypeSet` (var + doc, `:111-142`). This is the only production file where either identifier is a *behaviour-bearing* token (function body at `:101-109`, set literal at `:118-142`). The error contract (check order, sentinels) lives here — preserve it exactly.
- `internal/dispatch/dispatch.go:1-31, 137-184` — the **sole live code caller**: `Route` calls `protocol.IsV1Compatible(env)` at `:162`. Codegraph's index predates the #1039 merge and does not show this caller — confirmed by grep. Also 3 doc-comment references (`:3, :16, :138`). Nothing else in the routing logic changes.
- `internal/protocol/codes.go:58-466` — **~38 doc-comment references** to `v1TypeSet` (two shapes: "It is a v1TypeSet member" on v1-app-type constants `:58-142`; "MUST NOT be added to v1TypeSet … partitions … between v1TypeSet and v2OnlyTypes" on v2-only constants `:145-466`). Comment-only; no code. `v2OnlyTypes` is a **test-local** name (compat_test.go) — do **not** rename it.
- `internal/protocol/compat_test.go` (whole file, 288 lines) — asserts the accepted set (`TestIsV1Compatible`, `:8-103`), the size/coverage tie (`TestV1TypeSet_CoversAllExportedTypeConstants`, `:105-131`), and the disjoint v1/v2 partition (`TestTypeConstants_V1V2Partition`, `:179-244`). Both identifiers appear here in code and comments; two test function names embed them.
- `cmd/pyry/relay.go:330-335` — the stale comment "registering the same three relay handlers as the v1 path" fronting `startRelayV2`. The actual `Handlers` map (`:430-443`) registers **12** handlers, and no v1 path exists. Neither renamed identifier appears here — this is an independent comment fix (AC #5).
- `cmd/pyry/relay_guard_test.go` (whole file) — **read to confirm it needs no edit.** The classification-totality guard AST-reads `codes.go` (Type\* constants), `relay.go` (`Handlers` map keys), and `v2session.go` (`dispatchAppFrame` switch). It references *neither* `v1TypeSet` nor `IsV1Compatible`. The rename does not touch any surface it parses, so it compiles and stays green unchanged (AC #4 is a verification gate, not an edit target).
- `docs/PROJECT-MEMORY.md` § "Refusal-to-wire-code mapping is the consumer's job" — the reason the gate returns Go sentinels (`ErrUnsupported`/`ErrUnknownType`) and the caller maps them to `Code*` wire strings. Unchanged by this ticket; do not move that mapping.

## Context

`internal/protocol.IsV1Compatible` and its backing `v1TypeSet` were named for the Mobile Protocol **v1** path, which was removed (#913, #966, both closed). They are now load-bearing for **v2 only**: after the v2 session manager AEAD-decrypts an inner app frame, `dispatch.Route` validates that frame's `Envelope.Type` against `v1TypeSet` before dispatching it to a handler. The v1 naming — and the doc comment "acceptable under wire-protocol v1" — is stale and misleading. This ticket renames the gate to say what it does today (validate a decrypted inbound app-frame type) **without changing which types it accepts or rejects.**

## Design

Pure rename plus doc edits. No new types, no new files, no logic change, no goroutines. The fan-out is small and confined: codegraph + grep confirm the renamed **function** has exactly one non-test code caller (`dispatch.Route`) and the renamed **var** has zero out-of-file code callers. All identifier-bearing code lives in 3 files; the rest is comment substitution in 2 more.

### Chosen names

- `IsV1Compatible` → **`IsKnownAppType`**
- `v1TypeSet` → **`inboundAppTypeSet`**

Rationale: `inboundAppTypeSet` matches the vocabulary the existing guard already speaks (`relay_guard_test.go`: `inboundTypes`, `allAppTypes`, `appTypeConstNames`), and `IsKnownAppType` keeps the `Is…`-returns-`error` precedent the current `IsV1Compatible` already sets, so the error-contract doc comment reads naturally with no shape change. The developer may substitute an equivalent pair (e.g. `ValidateInboundAppFrame` / `inboundAppTypeSet`) **provided** they are applied consistently and AC #1's grep-zero holds — but do not rename `ErrUnknownType`, `ErrUnsupported`, `v2OnlyTypes`, or any `Type*` constant (all out of scope).

### What changes, per file

1. **`internal/protocol/envelope.go`** — rename both identifiers (function + var). This file gets *substantive doc rewrites*, not just token swaps, because the ticket's whole point is that these comments are stale:
   - The `IsKnownAppType` doc (currently `:90-100`): rewrite "reports whether env is acceptable under wire-protocol v1" → describe the actual role (reports whether a **decrypted inbound app frame** is acceptable — its `Type` is a known inbound app-frame type and `PayloadEncrypted` is false). **Preserve verbatim** the documented error contract: `PayloadEncrypted` true → `ErrUnsupported`; empty/unknown `Type` → `ErrUnknownType`; **check order PayloadEncrypted-first, Type-second**, "stricter rejection wins"; and the "does not validate Payload/ID/TS" narrowness note.
   - The `inboundAppTypeSet` doc (currently `:111-117`): rewrite "closed enumeration of envelope types accepted by wire-protocol v1" → "closed enumeration of inbound app-frame types accepted after AEAD decrypt". Keep the load-bearing warning that v2 control types (e.g. `TypeRekeyRequest`) MUST NOT be added here because the v2 session manager intercepts them before `dispatch.Route`, and keep the pointer to `compat_test.go`.
   - The sentinel doc (currently `:79`): "Sentinel errors returned by IsV1Compatible" → new function name (token swap).
   - **Do not change the function body's logic or the set literal's membership.** The map keeps exactly its 23 entries (`TypeHello` … `TypeRegisterPushToken`).

2. **`internal/dispatch/dispatch.go`** — rename the call at `:162` and the 3 doc-comment references (`:3, :16, :138`). Optionally soften the package-doc phrasing "refuses non-v1 frames" → "refuses frames whose type is not a known inbound app-frame type", but keep it minimal — the routing behaviour is unchanged.

3. **`internal/protocol/codes.go`** — substitute the ~38 comment references `v1TypeSet` → `inboundAppTypeSet` (a single `replace_all` of the identifier token suffices). **Scope discipline:** rename only the identifier token. Leave the conceptual "v1 application type" / "v2 control frame" / "v1/v2 partition" framing intact — it accurately describes the real architectural asymmetry (app-frame types that flow through `Route` vs. types intercepted or push-only), which this rename does not change. Do **not** touch `v2OnlyTypes`. A vowel-grammar cleanup (`a inboundAppTypeSet` → `an inboundAppTypeSet`) is optional polish, not required.

4. **`internal/protocol/compat_test.go`** — substitute both identifiers in code and comments (`replace_all` each), and rename the two test functions to match (`TestIsV1Compatible` → e.g. `TestIsKnownAppType`; `TestV1TypeSet_CoversAllExportedTypeConstants` → e.g. `TestInboundAppTypeSet_CoversAllExportedTypeConstants`). The assertions themselves — accepted set, rejected set (v2-only + encrypted), size tie, disjoint partition — stay exactly as they are. `v2OnlyTypes` stays.

5. **`cmd/pyry/relay.go`** — correct the `startRelayV2` doc comment (`:332`): drop the "same three relay handlers as the v1 path" clause (both wrong: 12 handlers, no v1 path) and describe the actual set, e.g. "registering the conversation / messaging / workspace / push-token handler set that `dispatch.Route` consults". Neither renamed identifier appears here; this is the independent AC #5 fix.

### Out of scope (do not edit)

- `docs/protocol-mobile.md`, `docs/archive/**`, `docs/specs/architecture/**` — these contain prose references to `` `v1TypeSet` `` / `IsV1Compatible` but are **markdown owned by the documentation phase**, not code. AC #1 ("code or comments") means Go source and Go comments. The developer's worktree mutates only code, tests, and this spec file; do not chase the `.md` hits. A reviewer seeing `git grep v1TypeSet -- '*.md'` return matches is **not** an AC violation — flag for documentation phase, do not rework.
- `relay_guard_test.go` — verify-only (see Files to read first).

## Concurrency model

None. This ticket adds no goroutines, channels, or context plumbing and changes no existing concurrency. `IsKnownAppType` remains a pure, allocation-free-on-rejection predicate called synchronously from `Route`.

## Error handling

The error contract is **preserved bit-for-bit** and must not drift:

- `PayloadEncrypted == true` → `ErrUnsupported` (checked first).
- `Type` empty or not in `inboundAppTypeSet` → `ErrUnknownType` (checked second).
- A frame failing both axes reports `ErrUnsupported` — the stricter rejection wins (pinned by the `encrypted-with-unknown-type` / `encrypted-with-empty-type` rows).
- Sentinels stay in `internal/protocol`; the wire-code mapping (`ErrUnsupported` → `CodeProtocolUnsupported`, `ErrUnknownType` → `CodeProtocolUnknownType`) stays at `Route` via `errors.Is`. No mapping moves.

The only failure mode this ticket could introduce is a membership slip during the rename (an accepted type dropped, or a rejected type let in). That is caught deterministically — see Testing strategy.

## Testing strategy

No new test functions; the existing guards are the safety net and must stay green:

- **`compat_test.go`** (renamed identifiers only): re-asserts the full accepted set (23 types → nil), the rejected set (every v2-only `Type*` + all three encrypted rows → the right sentinel), the size tie (`len(inboundAppTypeSet) == 23`), and the disjoint `inboundAppTypeSet`/`v2OnlyTypes` partition. A membership slip fails one of these immediately.
- **`relay_guard_test.go`**: unchanged; the classification-totality guard still holds because the rename touches none of the surfaces it parses. Confirm it compiles and passes (AC #4).
- **AC #1 grep-zero check** (developer runs before signalling done): `git grep -nE 'IsV1Compatible|v1TypeSet' -- '*.go'` must return **nothing**. (Scope the grep to `*.go`; `.md` hits are out of scope — see above.)
- **`make check`** green (vet + staticcheck + `go test -race ./...`) — AC #6. Note: staticcheck would flag the renamed exported symbol if any caller were missed, so a clean `make check` is corroborating evidence the fan-out was fully caught.

## Open questions

- **Grammar polish in `codes.go`** ("a inboundAppTypeSet member" → "an …"): optional. A blind `replace_all` leaves the article un-agreed; fixing it is a readability nicety, not an AC. Developer's call — do not spend turns perfecting it.
- **Function-name idiom** (`IsKnownAppType` returning `error` vs. a `Check…`/`Validate…` name): resolved above in favour of `IsKnownAppType` to minimise churn against the existing `Is…`-returns-`error` precedent. Left as an explicit developer escape hatch only if a strong local preference exists; consistency + grep-zero are the hard constraints.
