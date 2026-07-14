# Spec #950 — Guard test: every inbound v2 wire type must have a registered handler

**Ticket:** pyrycode/pyrycode#950
**Size:** S (test-only; zero production changes)
**Security-sensitive:** No — read-only structural test, no production code touched, no refactor of the live `dispatchAppFrame` switch. (No `security-sensitive` label on the ticket; confirmed the guard neither handles untrusted content, sets policy, nor touches the live inbound-routing switch.)

## Context

#949 was the motivating bug: `promote_conversation` had its type constant
(`internal/protocol/codes.go`), its payload (`conversations_write.go`), and its
registry op (`conversations/registry.go` `Promote`) — but **no dispatch entry**
in either inbound v2 surface, so it fell through `dispatch.Route` to
`protocol.unsupported` and save-as-channel silently failed. No test caught it
because definition and registration live in unrelated files with nothing tying
them together.

This spec adds a single **structural guard test** that fails whenever an inbound
client→daemon v2 wire type has no handler on **either** dispatch surface. It
closes the whole class permanently, for every future verb, at `make check` time.

### The two inbound dispatch surfaces (the core constraint)

A verb is correctly handled if it is registered in **either** of these; a verb in
**neither** is the `promote_conversation` failure mode:

1. **Map-dispatched** — the `V2SessionConfig.Handlers` map literal in
   `cmd/pyry/relay.go` (12 keys today), consulted by `dispatch.Route`.
2. **Switch-intercepted** — the `switch probeEnv.Type` in
   `V2SessionManager.dispatchAppFrame` (`internal/relay/v2session.go`, 9 cases
   today), which runs *before* `dispatch.Route`.

A guard that inspects only surface #1 would false-positive on all 9 switch verbs
and ship red on `main`. The guard must union both.

**Out of scope (verified, not assumed):**
- The outer **Noise transport** switches at `v2session.go:1230` and `:1267`
  route `noise_init`/`noise_msg`/`noise_resp` — the encrypted-tunnel frame
  discriminator, defined in `internal/protocol/v2envelope.go`, a *separate*
  vocabulary from the application `Envelope.Type` constants in `codes.go`. They
  are not application-verb dispatch and are correctly excluded.
- The **v1 dispatch leg** (retiring under #913) — the guard scopes to v2 only.

## Files to read first

- `internal/protocol/codes.go` — **the guard's constant universe.** Every
  application `Type*` constant lives here (50 total: the `v1TypeSet` ∪
  `v2OnlyTypes` partition). The per-constant prose comments already annotate
  direction (`phone → binary … dispatch.Route write/read verb`; `inbound v2
  control (intercepted pre-dispatch.Route)`; `binary → phone, outbound …`) —
  legible to a human, the source for the classification lists below.
- `cmd/pyry/relay.go:404-417` — the real `Handlers` map literal (surface #1).
  Each key is a `protocol.Type*` selector; values carry runtime deps, so only the
  **keys** are static/enumerable.
- `internal/relay/v2session.go:1811-1846` — `dispatchAppFrame`'s
  `switch probeEnv.Type` (surface #2); each `case protocol.Type*:` is one
  intercepted verb. The switch is nested inside an `if json.Unmarshal(...) == nil`
  block — an `ast.Inspect` walk finds it regardless of nesting.
- `internal/protocol/compat_test.go` — **prior art to model on.** It already
  enumerates every wire type and partitions v1/v2 with a totality drift-check
  (`len(all) == 26`, union-size assertions). This guard is a sibling concern
  (direction/registration, not v1/v2) using the same drift-check idiom.
- `internal/protocol/envelope.go:111-145` — `v1TypeSet` (production enumeration
  of v1 types) for cross-reference; confirms `codes.go` is the single home of
  `Type*` app constants.
- `Makefile` — `check: vet test staticcheck substrate-guard e2e`; `test` is
  `go test -race ./...`, which includes `cmd/pyry` and runs plain (untagged)
  tests. AC #6 is satisfied with **no Makefile change**.

## Design

### Where the guard lives

`cmd/pyry/relay_guard_test.go`, **package `main`**, plain `go test` (no build tag).

`cmd/pyry` is the only package that can see all three inputs: it imports
`internal/protocol` and `internal/relay`, and it *constructs* the Handlers map.
Dependency direction forbids the guard living lower (`internal/protocol` and
`internal/relay` cannot import `cmd/pyry`).

### Mechanism: AST-read the real surfaces; hand-classify direction with a totality tie

The guard reads the **actual wiring** structurally (via `go/parser` + `go/ast`)
rather than trusting a re-declared shadow of it — a re-declared handler list
could drift exactly the way the dispatch map did (#949). It compares that real
wiring against a hand-classified inbound set whose totality is drift-guarded, so
adding a type without classifying it is itself a failure. **No production code
changes** — the switch is read, never refactored (keeping the work non-sec and S).

Everything is keyed by **constant name** (the AST identifier, e.g.
`"TypePromoteConversation"`) — no name↔value bridge needed, because all three
extractors and both hand-lists speak names.

The guard derives four sets:

| Set | Source | Drift behaviour |
|-----|--------|-----------------|
| `allAppTypes` | AST: every `Type*` const ident in `codes.go` | New app type auto-appears; forces classification below |
| `mapHandlerTypes` | AST: keys of the `Handlers:` map literal in `relay.go` | Reflects real surface #1 |
| `switchTypes` | AST: `case protocol.Type*:` labels in `dispatchAppFrame` | Reflects real surface #2 |
| `registeredTypes` | `mapHandlerTypes ∪ switchTypes` | The real coverage |

…and two hand-classified lists (in the test, grouped with legible reasons):

- `inboundTypes` — the 21 client→daemon request verbs (see § Classification).
- `excludedTypes` — the 29 non-inbound constants, each tagged with its reason
  (`reply` / `push` / `handshake` / `v1-legacy`).

### Assertions (the contract)

1. **Coverage guard (the point, AC #1).** `inboundTypes ⊆ registeredTypes`.
   A member not in the union fails, naming it:
   *"TypeX is an inbound v2 type but is registered in neither the Handlers map nor
   the dispatchAppFrame switch — the #949 promote_conversation failure class."*

2. **Reverse tie (bidirectional pin).** `registeredTypes ⊆ inboundTypes`.
   Anything wired into either surface *is* by definition an inbound request, so a
   wired type absent from `inboundTypes` fails, forcing the classification list to
   track new wiring. (Together with #1 this pins `inboundTypes == registeredTypes`
   on a healthy `main`, but the two are derived independently — hand-list vs
   AST-of-real-code — so any divergence is a real bug or a real misclassification.)

3. **Totality tie (AC #2/#3 — no silent new types).** Every name in `allAppTypes`
   is in exactly one of `inboundTypes` / `excludedTypes`. An unclassified constant
   fails: *"TypeX is unclassified — mark it inbound (needs a handler) or excluded
   (with a reason)."* A classified name not in `allAppTypes` also fails (catches
   typos / removed constants).

4. **Surface-located, or loud (AC #2 — third surface is a visible gap).** Assert
   `mapHandlerTypes` and `switchTypes` are each non-empty and that exactly one
   `Handlers` map literal and one `dispatchAppFrame` switch were found. If an
   extractor locates nothing (wiring moved/renamed) or finds an unrecognized key
   expression, fail loudly: *"could not locate <surface> — did the wiring move?
   Update the guard."* A future **third** inbound surface makes any verb dispatched
   only there fail assertion #1 (false-positive → forces teaching the guard),
   *and* the test's doc comment documents how to add an extractor. Both: loud +
   documented.

### AST extraction sketches (behaviour, not bodies)

- `appTypeConstNames(path) []string` — parse file, walk `GenDecl{Tok: CONST}`
  value specs, collect `Name`s with prefix `"Type"`. (`codes.go` only — the Noise
  transport consts live in `v2envelope.go` and are excluded by construction.)
- `mapLiteralKeys(path, fieldName) []string` — parse file; `ast.Inspect` for a
  `KeyValueExpr` whose key ident == `fieldName` ("Handlers") and whose value is a
  `*ast.CompositeLit`; return each element key's `SelectorExpr.Sel.Name`. Assert
  exactly one such literal; flag any non-`protocol.Type*` key shape.
- `switchCaseSelectors(path, funcName) []string` — parse file; find
  `FuncDecl{Name: funcName}` ("dispatchAppFrame"); `ast.Inspect` its body for the
  single `*ast.SwitchStmt`; collect each `CaseClause` expr's
  `SelectorExpr.Sel.Name`. Assert exactly one switch; flag non-`protocol.Type*`
  exprs or multi-expr cases.

Each is a small stdlib helper (`go/parser.ParseFile` with `token.NewFileSet()`);
no third-party deps. Source paths are resolved relative to the test's package
directory (`go test` runs with CWD = package dir): `"relay.go"`,
`"../../internal/relay/v2session.go"`, `"../../internal/protocol/codes.go"`.
Encoding the two surface paths is inherent to the guard's job; if a surface moves,
the path breaks → loud failure → update the guard (consistent with assertion #4).

## Classification (audit deliverable, AC #4 — done here)

Ran the full enumeration against current `main`. **50 app constants → 21 inbound,
29 excluded. Every inbound type is covered (12 map + 9 switch). No gaps beyond
`promote_conversation`, already fixed by #949 (`da96790`). No follow-up tickets
needed.**

**Inbound — map-dispatched (12), surface #1:** `TypeSendMessage`,
`TypeListConversations`, `TypeCreateConversation`, `TypePromoteConversation`,
`TypeRenameConversation`, `TypeDeleteConversation`, `TypeArchiveConversation`,
`TypeUnarchiveConversation`, `TypeChangeWorkspace`, `TypeCreateWorkspaceFolder`,
`TypeRecentWorkspaces`, `TypeRegisterPushToken`.

**Inbound — switch-intercepted (9), surface #2:** `TypeRekeyRequest`,
`TypeRequestSnapshot`, `TypeModalAnswer`, `TypeModalCancel`, `TypeInterrupt`,
`TypeNewSession`, `TypeDequeueMessage`, `TypeRequestDebugBundle`,
`TypeSetSessionSettings`.

**Excluded (29), with reason:**
- **handshake (consumed at the auth first-frame gate, not `dispatchAppFrame`):**
  `TypeHello`. *(Borderline: `hello` is phone→binary but never reaches app-frame
  dispatch — it is consumed by `AuthenticateFirstFrame`. Classifying it inbound
  would false-positive. Called out explicitly per AC #4.)*
- **outbound reply (`in_reply_to` correlated):** `TypeHelloAck`, `TypeAck`,
  `TypeError`, `TypeConversations`, `TypeConversationCreated`,
  `TypeConversationUpdated`, `TypeConversationDeleted`,
  `TypeWorkspaceFolderCreated`, `TypeRecentWorkspacesList`,
  `TypeSessionSettingsUpdated`.
- **outbound push / event:** `TypeMessage`, `TypeTurnState`, `TypeAssistantDelta`,
  `TypeToolUse`, `TypeToolResult`, `TypeTurnEnd`, `TypeStall`,
  `TypeScreenSnapshot`, `TypeResync`, `TypeSessionTransition`, `TypeModalShown`,
  `TypeModalDismissed`, `TypeQueueState`, `TypeDebugBundleChunk`,
  `TypeDebugBundleDone`.
- **v1-legacy (not served by v2 dispatch; retiring with #913):**
  `TypeBackfillSince`, `TypeMessageChunk`, `TypeBackfillDone`. *(Borderline
  `backfill_since` is a v1 inbound verb but has no v2 handler — excluded as
  v1-legacy, not flagged as a v2 gap. Called out explicitly per AC #4.)*

The developer still **runs the guard against `main` and posts the AC #4 comment**
confirming green + the two borderline classifications above (this table is the
source; no independent sweep required).

## Concurrency model

None. The test is single-goroutine AST parsing — trivially `-race`-clean under
`make test`'s `go test -race ./...`.

## Error handling

The test has no runtime error paths beyond assertion failures. File-open /
parse errors from the AST helpers must `t.Fatalf` with the path and the surface
name (a parse failure means the guard cannot verify — treat as failure, never a
silent skip). Every failure message names the offending constant and the action
(classify it / wire it / update the guard).

## Testing strategy

The deliverable **is** the test. Verify it three ways:

- **Positive (green on `main`):** `go test ./cmd/pyry/ -run TestEveryInboundV2TypeHasHandler`
  passes as-is (all 21 inbound types covered).
- **Negative — coverage (proves it catches #949):** temporarily delete the
  `TypePromoteConversation` line from `relay.go`'s map (or comment the
  `TypeInterrupt` switch case) and confirm the guard fails naming that type;
  revert. Do this manually during development — do **not** commit a fault-injection
  fixture.
- **Negative — totality:** temporarily add a throwaway `TypeFrobnicate = "frob"`
  const to `codes.go` and confirm the guard fails "unclassified"; revert.
- **`make check` green** with the test in place (AC #6).

Assertions are expressed as table/set comparisons producing a **sorted, deduped**
list of offenders per failure mode, so a multi-type regression reports all
offenders at once, not one-per-run.

## Open questions / known limitations

- **Residual hole (accepted).** A developer who adds an inbound verb but
  *deliberately misclassifies* it as `excluded` **and** forgets to wire it would
  pass the guard. This is inherent to any direction-hand-list (the ticket accepts
  hand-lists whose totality is tied). Mitigations already in the design: (a) the
  reverse tie (assertion #2) catches the far more common "wired but unclassified";
  (b) each excluded entry carries a one-word reason, making a request verb
  mis-filed as `reply`/`push` visually obvious in review. The guard forces a
  *decision* (AC #3) — it does not attempt to police a deliberately wrong one.
- **`codes.go`-home convention.** `allAppTypes` assumes every application
  `Type*` constant lives in `codes.go` (as it does today, and as `compat_test.go`
  already assumes). A new app type added in a *different* file would escape the
  totality tie. Acceptable: it matches existing practice; if the convention ever
  breaks, `compat_test.go`'s own `all` list breaks first. Note it in the test's
  doc comment so a future maintainer keeps app constants in `codes.go`.
