# #1980 — Wire the daemon's outstanding question batches into the connect-time reconcile

`security-sensitive`. Fills the seam #1979 landed: bind `V2SessionConfig.OutstandingQuestions`
to the daemon-singleton `questionbridge.Registry`'s current-truth read, guard the binding
structurally, and publish the resulting behaviour in the client contract.

## Files read

- `cmd/pyry/relay.go` → `startRelayV2` — the composition root. Mints `modalReg` and
  `questionReg`, assembles the `relay.V2SessionConfig` literal, and hands `questionReg` to
  the stream-approval bridge's question arm. All three facts the guard checks live here.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.OutstandingQuestions` — the seam's
  contract: `func() []protocol.QuestionShownPayload`, optional (nil ⇒ no reconcile), pure
  read, enumerate-all, already-bounded payloads only, never logged. Its doc block names
  this ticket as the filler.
- `internal/relay/v2session_questionreconcile.go` → `reconcileQuestions` — the consumer.
  Gated on `s.interactive` and on a non-nil seam; unicasts to the just-opened conn only.
- `internal/questionbridge/registry.go` → `Snapshot`, `Lookup`, `Resolve`, `Record`,
  `cloneQuestions` — the three reads. `Snapshot` is the non-retiring current-truth one and
  its signature is already exactly the seam's type, so no adapter is needed. `Resolve` is
  the one-shot consume the reconcile must never take.
- `cmd/pyry/relay_guard_test.go` → `parseGoFile`, `mapLiteralKeys`, `switchCaseSelectors`,
  `protocolTypeSelName`, `relayPath` — the AST-guard home. `parseGoFile` and `relayPath`
  are reusable; `mapLiteralKeys` is not (it wants a composite map literal whose keys are
  `protocol.Type*` selectors, and this assignment is a plain selector expression).
- `cmd/pyry/snapshot_usage_test.go` → `TestBootstrapSnapshotUsage`'s doc comment — records
  in prose that `startRelayV2` has no test and cannot cheaply get one. This is why AC3
  needs a gate that does not construct it.
- `docs/protocol-mobile.md` → § Modal (v2), § Question (v2), § Queue (v2),
  § Reconnect / Backfill semantics, § Changelog — the reconcile-note shape to copy, the
  Mode B list to extend, and the changelog convention.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-question-reconcile-outstanding.md`
  — #1979's overview. Carries the cardinality-cap SHOULD FIX it flagged as a
  `questionbridge`-side obligation (explicitly *not* this ticket's), and the note that
  #1980 is where `Snapshot`'s two-level clone isolation becomes observable end to end.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-modal-reconcile-outstanding.md`
  — the twin's wiring half: production wires `OutstandingModals: modalReg.Snapshot` over
  the same daemon-singleton the raise-time producer `Record`s into. The sentence this
  ticket reproduces one family over.

## Context

`OutstandingQuestions` exists and `reconcileQuestions` drains it, but nothing in production
assigns the field, so the reconcile is a no-op on every real daemon. The source is already
constructed: `startRelayV2` mints `questionReg` unconditionally, beside `modalReg`, and its
own comment says why — the reconcile reads its `Snapshot` from the manager config assembled
below, which is built before the bridge exists. That same instance is what the surfacer
`Record`s into (`bridge.questions`) and what the answer path (#1907) will resolve against,
so binding the seam to it is what makes enumerate-current-truth reflect live control state
rather than a second, empty registry.

Two halves ship together. The wiring is one assignment; the client contract is the other
half, because a behaviour that is not published in § Question is one no client has reason
to expect — the failure mode the model menu's three undocumented loss points already
demonstrate.

No ADR is warranted: this reproduces a mechanism ADR 025 and § Reconnect / Backfill
semantics already describe, for a fourth frame family.

## Design

### Production change — `cmd/pyry/relay.go`

One field added to the `relay.V2SessionConfig` literal in `startRelayV2`, beside
`OutstandingModals`/`OutstandingQueues`/`RetainedModelLists`:

```go
OutstandingQuestions: questionReg.Snapshot,
```

with a wiring comment at the density its three twins carry. The comment states: what the
seam enumerates and why the reconcile exists at all (the raise-time broadcast reaches only
whoever is connected, and the frame carries no event id so it is not in the #647 replay
ring); that `questionReg` is the same daemon-singleton the surfacer records into and the
answer path resolves against; and that `Snapshot` and not `Resolve` is deliberate — the read
retires nothing, so a re-sent batch stays answerable exactly once and a batch resolved while
the client was away is absent rather than re-sent.

**Method value, not a closure**, matching `OutstandingModals: modalReg.Snapshot`.
`RetainedModelLists`' stated rationale for the same choice is deliberately **not** carried
over: that comment explains a wrapper would be non-nil even when the underlying field is
nil and would silently defeat the nil ⇒ no-reconcile contract, which is true there because
`w.retainedModelLists` is nil in foreground/v1. `questionReg` is minted unconditionally a
few statements earlier, so no nil is reachable at this site and repeating that sentence
would state something false. The reason that does apply is narrower: the closure buys
nothing and the modal twin reads the same way.

**No adapter.** Unlike #1867's model-list producer there is no enumerator to build:
`Registry.Snapshot` already returns `[]protocol.QuestionShownPayload`, which is exactly the
seam's type.

### Guard — `cmd/pyry/relay_guard_test.go`

`startRelayV2` has no test and cannot cheaply get one, so AC3 needs a gate that does not
construct it. This is a **first-of-its-kind** guard, not a copy: none of the three existing
twins' assignments is pinned anywhere today. The `TestOutstandingQueues_*` /
`TestRetainedModelLists_*` families exercise the adapter functions `outstandingQueues` and
`retainedModelLists` directly and stay green if the matching line is deleted from the config
literal.

One new test, `TestOutstandingQuestionsWiredToSurfacerRegistry`, AST-reads three structural
facts from `relay.go` and ties them together. Bare presence is not enough — a gate that only
checks the field appears as a key stays green against a freshly-minted second registry,
which is exactly the failure AC1 names.

| Fact | Extracted by | What its absence means |
|---|---|---|
| The sole `OutstandingQuestions:` element of a composite literal binds a plain-identifier selector, and the selected method is `Snapshot` | new `configSeamSelector` | field deleted (AC3), or bound to a consuming read like `Resolve`/`Lookup` (AC2), or bound to an inline `questionbridge.New().Snapshot` whose receiver is a call rather than an identifier (AC1) |
| `questionbridge.New()` is called **exactly once** in the file, and the identifier it defines is the seam's receiver | new `soleShortVarDeclFor` | a second registry exists in this file at all (AC1) |
| The sole `<x>.questions = <ident>` assignment names that same identifier | new `fieldAssignIdent` | the seam and the surfacer are pointed at different instances (AC1) |

Every extractor `Fatal`s with an "update the guard" message when the shape it expects is
absent, following the file's established convention — a moved surface must be loud, never
vacuously green.

### Contract — `docs/protocol-mobile.md`

1. **§ Question (v2)** gains a bolded **Reconcile on (re)connect** paragraph in the shape
   § Modal's and § Queue's already carry, placed after the section's framing paragraphs and
   before `#### question_shown`. It states: every still-outstanding batch is unicast with
   its **original `question_batch_id`**; this is match-and-replace by stable id, so a
   re-send never double-shows; answerability is unchanged under re-delivery (the nonce is
   minted once at raise time, the parked copy is consumed exactly once, and the reconcile
   mints nothing, retires nothing and re-arms no approval window); a batch resolved or
   dismissed while the client was away is simply absent; and the path is **enumerate-all**,
   not conversation-scoped — every outstanding batch reaches the opening connection, each
   carrying its own `conversation_id` to filter on.
2. **§ Reconnect / Backfill semantics**' Mode B list names `question_shown` alongside the
   outstanding modal and the queued backlog, and adds `question_batch_id` to the
   match-and-replace stable-id list.
3. **§ Changelog** gains a `2026-09-01` entry at the head, per the convention the three
   entries above it follow.

The § `model_list` staleness ("no connect-time snapshot today", contradicted by
#1863/#1867) is **not** touched: it is that pair's debt, and it is not precedent for
omitting `question_shown` here.

## Concurrency model

No new goroutines, no new locks, no new lock ordering. The assignment publishes a method
value on `*questionbridge.Registry` before `mgr.Run`'s goroutine starts, exactly as
`bridge.questions` and `bridge.streamApprovals` are, so there is no data race on the seam
itself. The read that crosses goroutines is `Snapshot`, called on the manager's `Run`
goroutine from `handleNoiseInit`'s interactive-open tail while the surfacer's producer
goroutine may be recording; `Registry`'s own `sync.Mutex` — a leaf lock held only around
O(1) map ops and the clone — already covers that, and it is proven by `internal/questionbridge`'s
own tests. Nothing in this ticket adds a second lock to order against it.

## Error handling

The seam has no error return and this wiring introduces no failure mode of its own.
`Snapshot` cannot fail: it takes a mutex, walks a map and clones. An empty registry yields
a non-nil, zero-length slice, which `reconcileQuestions` already treats as "nothing to
send". Marshal failure on the reconcile path is `reconcileQuestions`' own concern and is
handled there (and never logs the payload).

## Testing strategy

- **RED first.** `TestOutstandingQuestionsWiredToSurfacerRegistry` is written and run before
  the `relay.go` assignment exists; it must `Fatal` on the missing `OutstandingQuestions`
  key, for that reason and no other.
- **AC3 mutants**, each run against the finished tree to confirm it reddens:
  1. delete the `OutstandingQuestions` line → seam extractor finds zero elements;
  2. repoint at a second registry (`otherReg := questionbridge.New()`) → the mint-count
     assertion trips;
  3. repoint at `modalReg.Snapshot` → the co-reference assertion trips;
  4. swap `Snapshot` for `Resolve` → the method-name assertion trips (AC2).
- **AC2 needs no new registry test.** `Snapshot`'s purity and its two-level clone isolation
  are already proven by `TestSnapshot_PureRead` and `TestRegistry_HandsOutIsolatedCopies`
  in `internal/questionbridge/registry_test.go` (#1975). AC2 is about *which* of the three
  reads is wired, which is what mutant 4 pins.
- **Touched-scope gate:** `go test -race ./cmd/pyry/...` plus `go test -race
  ./internal/questionbridge/... ./internal/relay/...` (the packages the wiring reads
  across), `go vet ./...`, `go build ./cmd/pyry`. The full-module race suite is the
  verifier's gate.

## Open questions

1. **Does the guard assert the mint count, or only co-reference?** Co-reference alone leaves
   a second `questionbridge.New()` in the file legal as long as nothing uses it, which is
   dead code rather than a wiring bug — but the mint-count assertion is two lines and makes
   "there is exactly one registry in this composition root" structural rather than
   conventional. Resolve during implementation; if the count assertion proves brittle
   against a legitimate future second registry, drop it and keep co-reference.
2. **Where exactly in § Question does the reconcile note land?** § Modal puts its note
   immediately after the section intro and before `#### modal_shown`; § Question has two
   framing paragraphs (the intro and the "modelled whole" argument). Placing it after both
   keeps the frame's shape argument adjacent to the frame; placing it after the intro
   matches § Modal literally. Resolve when editing.
3. **Does the Mode B "Mechanism internals live in #877 / #878" sentence get #1979 added?**
   It sits inside the section AC4 names and would otherwise be immediately stale. Add it
   only if it stays a single clause.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, but the audience widens and that is deliberate.** The
  single boundary is `questionbridge.Parse`, which decodes and bounds claude's tool input
  before `Record` parks anything; this wiring adds no decode, no second boundary, and
  re-emits only post-`Parse` payloads. What it *does* change is reach: a client that was
  not connected when the batch was raised now receives it, so claude-authored text lands on
  a render surface it previously never reached. That is the established posture and not a
  new one — `OutstandingModals` has had exactly this property since #877, and § Security
  model treats a user's paired devices as one trust domain (§ Queue states it outright).
  The audience is still gated twice: `reconcileQuestions` runs only from `handleNoiseInit`'s
  post-handshake, post-token-validation tail, and returns early on `!s.interactive`. This
  ticket changes neither gate.
- **[Tokens, secrets, credentials] No findings — re-sending the nonce does not re-grant
  anything, and this is the whole content of AC2.** `question_batch_id` is minted once by
  `Record` via `crypto/rand` (#1975); this path mints none. Answerability is governed
  entirely by `Registry.Resolve`'s one-shot consume, which the reconcile never calls, so a
  batch re-sent across N reconnects stays answerable exactly once and a batch already
  resolved is absent from the read rather than re-offered. `Snapshot` is the only one of the
  registry's three reads with both properties — `Resolve` retires (breaking the "still
  outstanding" read), and `Lookup` neither retires nor enumerates. Binding the wrong one is
  the failure this design guards against structurally, via the guard's method-name
  assertion, rather than by review.
- **[File operations] Not applicable — no path, no file, no `os` call anywhere in the
  change.** The whole production diff is one struct-literal field.
- **[Subprocess / external command execution] Not applicable — no `exec.Command`, no
  environment handling, no signal path.** The subprocess relationship is upstream: claude's
  tool call is already parked by the time anything here runs.
- **[Cryptographic primitives] No findings — no primitive is introduced, chosen, or
  re-used.** The one security-relevant random value on this path (`question_batch_id`) is
  generated upstream by `Record` from `crypto/rand`.
- **[Network & I/O] SHOULD FIX — this ticket makes #1979's cardinality gap live, and the
  gap stays `questionbridge`'s to close.** Per-batch bounds are enforced upstream
  (`questionbridge.Parse`: 1–4 questions, 2–4 options, a byte cap on the raw tool input),
  but `Registry` holds no cap on how many batches may be outstanding at once. While the
  seam was nil that was theoretical; binding it means one connect can now emit a burst
  sized by the outstanding count. Assessed as SHOULD FIX and not MUST FIX because the count
  is not reachable from the network at all: only claude's own ask concurrency creates
  batches, each holds a `permbridge` approval that times out at `mcpApprovalTimeout`, and
  #1973 retires on every terminal path — a remote client cannot cause accumulation. The
  backstop on this path is `pushQueue`'s byte ceiling, which drops rather than growing
  unbounded. **Do not add a cap here**: the seam's own doc block rejects a second place
  deciding the limit, because the two could silently disagree. Carry the obligation to
  whichever ticket caps `Registry.outstanding`, as #1979's review already recorded.
- **[Error messages, logs, telemetry] No findings, with one discipline item for Phase B.**
  The four claude-authored strings (`Question.Text`, `Question.Header`, `QuestionOption.Label`,
  `QuestionOption.Description`) must never reach a log line, and this change adds no logging
  of any kind. The hazard is a future debugging instinct at this wiring site: a reconciled
  *count* is safe to log, a reconciled *payload* is not. The rule is already stated in the
  seam's doc block and in § Question's SECURITY paragraph, so Phase B restates it nowhere —
  it simply adds no log call. The doc edit publishes a behaviour, not a secret; § Modal
  publishes the identical thing.
- **[Concurrency] No findings — and the isolation property becomes load-bearing here for
  the first time.** No goroutine, no lock, no lock ordering is added. The method value is
  bound at config-construction time and stored in `cfg` before `mgr.Run`'s goroutine exists,
  so the field itself cannot race (the same publish-before-Run discipline `bridge.questions`
  and `bridge.streamApprovals` follow). The cross-goroutine read — `Snapshot` on the manager's
  Run goroutine while the surfacer's producer goroutine may `Record` — is covered by
  `Registry`'s own leaf mutex. Verified rather than assumed: `QuestionShownPayload`'s only
  reference-typed field is `Questions`, and `Question`'s only one is `Options`;
  `cloneQuestions` clones both levels, so the payload marshalled on the Run goroutine
  aliases no live registry state and cannot observe a torn write. #1979 could only assert
  this against a fake seam; this is the ticket where the real guarantee is exercised.
- **[Threat model alignment] Named, not fixed here.** § Security model threat 1 (prompt
  injection, `severity: high`, `mitigation: partial`) lands on this frame's four
  claude-authored strings, and this ticket extends where they can be rendered. The
  mitigation is unchanged and unchanged-by-design: the strings stay untrusted text all the
  way out, and the render boundary that owes sanitization is the client's, exactly as
  § Question already publishes. No threat is newly in scope; none is newly deferred.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-01
