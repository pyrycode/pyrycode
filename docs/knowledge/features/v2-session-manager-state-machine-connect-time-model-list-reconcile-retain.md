# Connect-time model-list reconcile (#1863) — `RetainedModelLists` seam + `reconcileModelLists`

Catalog recovery has two delivery paths: supplied thread reconciliation and
legacy `RetainedModelLists` reconciliation. Both can restore menus before a turn
using the same vocabulary precedence; their provenance and ordering contracts
differ. See the [wire specification](../../protocol-mobile.md#model_list).

## Supplied thread reconciliation

`handleNoiseInit` selects `ThreadLiveState` for thread peers and returns before
legacy `reconcileModelLists`. Reusing only the retained owner cursor therefore
loses catalogs for targets whose vocabulary is borrowed: those bytes have no
current-session retention authority. `installDaemonLiveRelay` instead composes
`daemonRelayCatalogSnapshot`, retaining the other families and resolving catalogs
lazily for a finite captured registry target set. It uses
`daemonModelListReadingFor` / `modelListResultFor`, preserving bound-session →
bootstrap → saved precedence. Recipient access and basic/`multi_agent` projection
still apply. A registered target needs neither its own reading nor an assistant
turn or request. No usable vocabulary skips inventory and completes the handshake
without an empty catalog or legacy-provider retry.

Each target generation's first catalog delivery clears with uncorrelated
`model_list`, `session_state_cleared: true`, payload `{}`, then sends fresh
uncorrelated inventory. Both omit ring/history IDs and route by top-level
`conversation_id`. Producer `session_id` stays omitted, null or the captured
producer string, even for bootstrap/predecessor inventory or colliding target/
session identities. A delivered same-generation source replacement replaces the
whole catalog without a second clear, session admission or settings changes.
Borrowed bytes stay outside target retention and cannot mutate another
conversation's state.

**Rejecting stale inventory must leave a way to recover current inventory.**
A second peer's successful borrowed request advances accepted catalog ordering
without publishing replacement bytes. If a reconnect catalog is parked behind
its clear, rejecting it and exhausting the finite cursor would leave only the
clear on the phone. `drainLiveOnce` calls `refreshLiveCatalog` for an overtaken
unsolicited catalog: resolve once through `ModelListReadingFor` after recipient
capability/access gates, require the same target and captured generation, clear
correlation, then use ordinary `forwardLiveState` validation, projection and
freshness checks. Refusal or a different generation leaves the capture withheld.
There is no retry loop or extension of the captured target set. Removal/recreation
and reused-session transitions still invalidate old captures; an independent
fresh resolution after a clear can acquire current target ordering.

`TestDaemonLiveRelayCatalogConcurrentRequest` exercises that two-peer hold after
the clear with unchanged/replaced bootstrap vocabulary and both projections.
`TestDaemonLiveRelayCatalogSnapshotOvertaken` distinguishes a new resolution from
an old capture across catalog updates, transitions and removal/recreation.
`TestDaemonLiveRelayCatalogReconnect` inspects raw producer presence and full
decoded payloads for persisted/bootstrap fallback on connect and reconnect.
The authenticated `switchWire` fixture must preserve the phone's paired static
key across reconnects while creating fresh connection ciphers: generating a new
key under the same token correctly fails pairing before catalog recovery runs.
`TestDaemonLiveRelayCatalogReconnectGates` proves no-vocabulary completion,
capability/access withholding with an accessible positive control, and legacy
reconciliation. See [accepted catalog ordering versus retention](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#relay-delivery).

## Legacy reconciliation

Legacy live delivery is opportunistic: fan-in can drop inventory, and a later
connection cannot rely on a live frame emitted before it attached. Bootstrap and
rotated-child events now route to their own bound conversations (#2739), so the
old foreground-cursor loss no longer applies. Connect-time reconciliation reads
current vocabulary independently of live delivery; event-ring replay is retired.
It opens no turn and sends snapshots without `event_id`.

**`RetainedModelLists func(multiAgent bool) []protocol.ModelListPayload`** is an optional `V2SessionConfig` seam in the family (nil ⇒ no reconcile, byte-identical to pre-#1863). Production wires a non-nil closure as of #1867: `cmd/pyry/session_model_list.go`'s `retainedModelLists(convReg, pool)`, built at the composition root in `main.go` and assigned straight through the `relayWiring` struct (§ below). Same closure-not-concrete-import idiom as `OutstandingModals`: `internal/relay` imports neither `internal/turnevent` nor `internal/sessions` directly (`internal/sessions` reaches it only transitively, via `internal/control` — a `go list -deps` reading that looks contradictory and isn't), while `internal/protocol` is already a direct import, so the payload crosses with no new import and no cycle. **Enumerate-all, not conversation-keyed** — `V2Session` carries no conversation id, so there is no "this conn's conversation" to join on at connect time; the former queue reconcile's one-per-conversation shape was the precedent, and `RunConfigFor` (conversation-keyed) is the wrong shape here. The enumerator (`retainedModelLists`) walks `conversations.Registry.List()` unfiltered — including archived rows, since `Registry.SetArchived` writes one field without unbinding `CurrentSessionID` — and appends only the payloads `resolveBoundModelList` (#1857) answers `true` for; the comma-ok is its only filter, and the resolver decides refusal on its own. **As of #2124, that refusal surface is one arm, not several**: an unbound conversation, a binding the pool cannot resolve, a runner without the `ModelList` method and an empty hold all used to refuse here and now fall back to a daemon-wide copy instead (below); with usable vocabulary available, only a registry miss on the conversation id still refuses. **The bound is the producer's**: this path applies no cap of its own to either the number of payloads or any entry's text — it relies entirely on the construction-time bounds upstream (`DroppedModels` on the aggregate, `TruncatedFields` per entry, frozen by #1704/#1705), and the seam's doc comment says so explicitly. #2124 widens who reaches those bounds (every registry row once any vocabulary is retained, not just rows with a live, reporting session) without changing the bounds themselves.

**The daemon-wide fallback, now two sources deep (#2124, #2450).** The vocabulary is machine- and account-scoped, not conversation-scoped — the committed capture `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json`, taken under an `argv` carrying `--model claude-haiku-4-5`, still reports six models with `default → claude-sonnet-5` — so `resolveBoundModelList` answers a binding that yields nothing from a daemon-wide copy rather than refusing. Two new unexported helpers carry the first fallback: `sessionRetainedModelList(sess *sessions.Session)` does the type assertion off `Session.Runner()` plus the hold's comma-ok, with an explicit `sess == nil` guard in front; `retainedModelVocabulary(pool, saved, boundSessionID)` tries the bound session's hold first, `pool.Default()`'s hold second. **A live child's own report always wins** — a daemon-wide source is read only when the binding above it yields nothing, never used to override one. Neighbouring resolvers (`resolveBoundRunner`, `resolveBoundSession`, `resolveBoundRunSettings`, `resolveBoundSlashCommandList`, `resolveBoundBackgroundTaskRoster`, the `cmd/pyry/main.go` twins) keep refusing on every one of these arms — the fallback is vocabulary-only, and each twin's doc now says so explicitly rather than leaving the family's refusal inventories to read as if they had all moved together.

**#2124's own premise stopped holding, and its docstring said so for a while before anyone fixed it.** It justified the bootstrap fallback with "`RequestInitializeOnSpawn` is true for every child, so the bootstrap child asks at daemon start" — true when #2124 shipped, false from #2085 onward, which deleted the spawn-at-daemon-start that made it true. Nothing spawns at start any more, so a **restarted** daemon has no vocabulary in either hold until a turn runs in the conversation bound to the bootstrap session — and since every minted conversation gets its own session, on a daemon whose bootstrap-bound conversation stays idle that is never, for the whole process lifetime. #2450 closes exactly that window, not a wider one: it adds a **third** source, `savedModelVocabulary` — a one-method comma-ok interface, `*cmd/pyry.modelVocabularyStore` the only production implementation — backed by `model_list.json`, one file per daemon instance beside `sessions.json`, rewritten (async, single-flight, coalescing, write-skip when the encoded bytes already match) every time *any* child's `initialize` reply lands on a `sessionModelHold`, and read once at daemon start, never on the request path. Order is bound → bootstrap → file, for the same reason applied a third time: a hold is this process's own observation, the file is a previous process's. The file changes which answers a **cold** daemon gives and changes no answer a warm one gives.

**A `multi_agent` conn gets Codex's vocabulary merged in too, chosen by the same helper both seams share (#2651).** `RetainedModelLists`' closure takes the opening conn's negotiated `multiAgent` decision as its argument and passes it straight to `modelListResolver(multiAgent)`, which chooses `resolveBoundMergedModelList` over the unmerged `resolveBoundModelList` — the identical choice [`ModelListFor` makes for the on-demand request](v2-session-manager-state-machine-inbound-request-model-list-modellistfor-seam.md), through the same helper, so the two paths can never answer one conn differently. `resolveBoundMergedModelList` keeps this file's three-source fallback for Claude's half unforked — it still resolves through `retainedModelVocabulary` — and appends Codex's newest-per-family entries from the vocabulary store's `CodexModels()` (#2627) after them, each row tagged `agent`/`family`. `DroppedModels` still counts only Claude's cut; a conn that did not negotiate `multi_agent` reaches the unmerged resolver exactly as before, byte-identical. See [Model-list payload](protocol-package-model-list-payload.md) for the tagged rows themselves.

**The push path (#2652) reuses #2651's tagging rather than a second copy of it, because two independent taggers cannot be kept from disagreeing.** `mergedModelOptions`'s two tagging loops are factored into `claudeModelOptions` and `codexModelOptions(saved)`; `mergedModelOptions` itself becomes those two calls in sequence, behaviour unchanged (its own tests pin it), and a new `pushedModelOptions(saved)` builds a `func(claude []protocol.ModelOption) []protocol.ModelOption` closure over the same two helpers for `V2SessionConfig.MergedModelOptions` (the seam [`mergedForConn` calls](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md)). The reply, the connect-time reconcile, and the live push now all tag through the identical two functions — the alternative (writing the push path's own tagging inline against the seam) would let a future change to one class's tag rows drift out of sync with the other two without either test noticing until a client saw the mismatch.

**A "substitute the parameter's type so call sites compile unchanged" plan needs the call sites' bodies read, not their call lines.** The design considered replacing `resolveBoundModelList`/`retainedModelVocabulary`/`modelListFor`/`retainedModelLists`'s `pool *sessions.Pool` parameter with a struct carrying the pool plus the new source, measured on the grounds that ~24 call lines shaped `resolveBoundModelList(reg, pool, …)` would stay byte-identical. They would have — but the same `pool` variable is also used **as** a pool (`pool.Default()`, `pool.BootstrapID()`, `pool.Create`, `pool.SettingsFor`) at roughly twenty further sites in the same test file, invisible to a search for the resolver's call lines. The only struct shape that keeps both sets compiling embeds `*sessions.Pool`, which promotes the pool's whole method set onto a value named "sources" — worse than the parameter it was meant to avoid. The shipped shape is an explicit second parameter, `saved savedModelVocabulary`, added beside `pool` at every one of the now-~39 call sites instead. General shape: counting call *lines* measures the wrong thing when a plan claims a substitution is free; count every use of the variable being replaced.

**A three-source precedence table cannot prove its own bottom rung from one fixture.** A test that arms all three sources and asserts the top one wins proves ordering among the *armed* sources; it can never exercise "neither hold answers, only the file does," because a fixture with the bootstrap hold armed can't also express the bootstrap having nothing. `TestRetainedModelVocabulary_OrdersItsThreeSources` (all three armed, contents asserted) and `TestRetainedModelVocabulary_FileAnswersWhenNoHoldDoes` (a second, emptier fixture) are two separate tests for exactly this reason. Worth expecting whenever a precedence table's last entry reads "and otherwise."

**The publisher's vocabulary is also the model-selection authority (#2281).**
Using a second catalog would let a client select a row the daemon published but
the settings path refused, or accept a value the client was never offered.
`settingsUpdaterAdapter.UpdateSettings` therefore reads
`retainedModelVocabulary` before mutation. An exact untruncated `Value` match is
positive evidence even if another row was lost; absence is conclusive only when
the retained list is non-empty, has no dropped rows, and has no value-truncated
row. Treating incomplete evidence as absence would permanently reject a model
whose full row may arrive on a later initialize response, so that state is the
retryable `model_list.unavailable` outcome instead.

**`Pool.Lookup("")` is not a safe way to reach the bootstrap.** It special-cases the empty id and returns `p.sessions[p.bootstrap], nil` — a **nil error**, not a signal that anything went wrong — which on a pool with no bootstrap is `(nil, nil)`. The naive shape of this fallback (drop the `CurrentSessionID != ""` guard and let `Lookup` fall through) both re-opens the #678 isolation hole (see below) and hands back a possibly-nil `*Session` with no signal. `Session.Runner()` has a pointer receiver that dereferences (`func (s *Session) Runner() Runner { return s.sup }`), so that nil **panics** rather than failing a type assertion cleanly — the family's usual "a nil `Runner` fails the assertion cleanly, so no nil check is needed" reasoning does not carry over to a session that is itself nil. `retainedModelVocabulary` keeps the `boundSessionID != ""` check so `Lookup("")` is never called at all, and reaches the bootstrap only through the named `Pool.Default()` accessor with `sessionRetainedModelList`'s explicit nil guard in front of it.

**The #678 isolation guard is about routing turns, not about vocabulary — and that distinction is now load-bearing rather than assumed.** The empty-`CurrentSessionID` check was the #678 enforcement point precisely because `Pool.Lookup("")` resolves to the bootstrap, so an unbound conversation reaching a lookup would be handed the shared bootstrap child's *state*. #2124 reads only the bootstrap's model *list* on a resolved conversation's behalf, which is a different data class: it carries no conversation state, varies by machine and account rather than by conversation, and is already published to any interactive client the moment its own child answers `initialize`. `resolveBoundSlashCommandList` is the control that shows the split is real rather than semantic: its inventory is workspace-scoped (51 entries in one repository, 74 counted in another), so the identical fallback would hand back a **wrong** menu there, not a borrowed one — which is why it, and every other resolver in the family, keeps refusing on all these arms while this one alone falls back.

**`reconcileModelLists(ctx, s)`** reconciles retained model vocabulary keyed by `conversation_id` (the former queue reconcile is retired in #3727):

- **Guard:** `if !s.interactive || m.cfg.RetainedModelLists == nil { return }` — identical shape to the other reconciles. This is the one guard in the family with a sole-red mutant reachable by a *single-conn* test: dropping `!s.interactive` only reddens if a second, genuinely-interactive conn is opened after the non-interactive one under test — without that control conn, "zero frames" is indistinguishable from an empty seam and the mutant survives green. The other family members' non-interactive rows carry the same shape for the same reason.
- **Snapshot once**, empty ⇒ nothing sent. This half of the guard has **no** sole-red mutant: with a zero-length slice the loop body never executes, so deleting the `len(retained) == 0` check is an equivalent mutant, not an untested branch. The "zero payloads" test row is pinning the *contract* (a real producer returning `nil` must not error), not a reachable code path — worth knowing before it reads as coverage that was skipped.
- **Per payload:** marshal an envelope with a shared batch timestamp, `ID: 1`
  (non-load-bearing; the phone correlates on `conversation_id`) and no
  `EventID`. Queue through `pushSource` with `modelListProjected: true`, because
  the provider has already selected and projected rows for this recipient.
  The queue retains that provenance through draining; all ordinary delivery,
  capability and size checks still apply. A marshal failure logs content-free
  discriminants, never model text or the marshal error, then continues. Push
  failures may log only the context/connection sentinel; canceled context stops
  the batch.

### Projection provenance

Model projection must not depend on `EventID`. A canonical unsolicited model
push is now ID-free, and `forwardEnvelopeSource` passes explicit eligibility to
`mergedForConn`. Supplied readings use `ModelListUnprojected`; canonical readings
can therefore be projected per recipient even when correlated. Ordinary
correlated replies and provider-projected supplied readings preserve their bytes.
Connect-time `RetainedModelLists` values are already projected yet uncorrelated,
so correlation alone cannot distinguish them from canonical pushes:
`queuedEnv.modelListProjected` carries that distinction under the existing queue
lock. Clears remain `{}` and conversation/dropped-model fields survive projection.

An artificial event ID in a fixture would keep the old merge gate green while
real ID-free live pushes silently lost multi-agent rows.
`TestV2Session_PushedModelList_MergedForMultiAgentConn` uses an ID-free push and
checks basic/capable recipients, correlated replies and the unwired seam.
`TestV2Session_ModelListReconcile_PassesTheConnsMultiAgentDecision` separately
pins provider projection without a second merge; live-push coverage alone would
miss duplicate rows on reconnect. See [supplied-state projection](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#supplied-live-state-ordering).

**No test can redden the defensive marshal branch, and none was written.** `ModelListPayload` is `string` + `[]ModelOption` + `int`, `ModelOption` is three strings / two `[]string` / one bool, and both custom `MarshalJSON`s delegate to `json.Marshal` over closed types — no value can fail to marshal. `reconcileModals` has no marshal test either, for the identical reason; the branch is defensive dead-in-practice code kept for the same reason the twins keep theirs, and the test file says so in its header rather than leaving the gap to read as an oversight.

**Call site:** `handleNoiseInit`'s authenticated success tail reconciles retained
model lists for legacy peers; thread peers use supplied reconciliation above.
There is no event replay hook. See [explicit reconnect recovery](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md).
The former `reconcileQueues` call is removed (#3727). `staticcheck`'s default U1000 forbids shipping the seam + `reconcileModelLists` without this call site — an uncalled unexported method fails `make check`, which is why this and #1864's producer wiring could not be split further than they already were (from #1858, prerequisite-split once already).

**Pure read, no turn opened.** `RetainedModelLists` mints nothing and retires nothing, so re-connecting re-sends the same snapshot idempotently. Nothing on this path touches turn state — the frame goes straight onto the push queue via `pushSource`, never through the interactive-turn lane, and `TypeModelList` is not a turn-boundary type by the same #1704/#1705 freeze the live-lane frame obeys.

**Security review verdict: PASS** (`docs/specs/architecture/1863-model-list-connect-reconcile.md` § Security review has the full pass). Same two gates as the other two Mode B instances — authentication (post-handshake, post-token-validation only) and capability (`!s.interactive`) — plus the resource-bound discharge above. No new goroutine, no new lock; `s.interactive`/`s.connID` are read lock-free under the Run-goroutine single-owner invariant, the caller's guarantee, restated (not merely implied) in the function's own doc comment per the modal/queue precedent.

Tests: `internal/relay/v2session_modelreconcile_test.go` (new; **table-driven, not one-function-per-arm** — the house idiom this package already uses in `v2session_dequeue_test.go`/`v2session_interrupt_test.go`/`v2session_modal_test.go`, and a deliberate divergence from #878's file layout for the same coverage in materially fewer lines) — one/two retained lists delivered and `reflect.DeepEqual`'d against the seam's own payloads including `DroppedModels`/`TruncatedFields` survival; unicast-only (opening conn B does not cause a second delivery to already-open conn A); the three AC2 no-frame arms (nil seam, zero-payload seam, non-interactive conn — the last needs a second interactive conn opened afterward, per the guard note above); content-free logging asserted via three distinct sentinel literals (`Value`, `ResolvedModel`, `DisplayName`) rather than one, since the three are separate struct fields a partial leak could miss. `ModelListPayload` has no `time.Time` field, so it is safely `reflect.DeepEqual`-able and neither `#878`'s `equalQueued` comparator nor its `sampleQueueTS` has a counterpart here.

**A `reflect.DeepEqual` against a *decoded* `model_list` needs `EffortLevels: []string{}` in the expected value, not `nil` (#2651).** `ModelOption.MarshalJSON` normalises a nil `EffortLevels` to `[]` on the wire (this file's payload doc explains why), and decoding `[]` back yields a non-nil, zero-length slice — `reflect.DeepEqual(nil, []string{})` is `false`. A table built by hand with `EffortLevels: nil` and compared against a round-tripped (marshal-then-unmarshal) payload reddens on that field alone, for every row, even when the merge and tagging logic under test is correct. This bit both the request and the reconcile tests once the multi-agent table added Codex rows through the same decode path the Claude rows already took.

**The daemon-side producer (#1867):** `retainedModelLists(convReg, pool)` (`cmd/pyry/session_model_list.go`), tested in `cmd/pyry/session_model_list_test.go` against the existing `resolveBoundModelList` (#1857) fixtures — no new fixture type. Two properties of a skip-and-continue enumerator turned out to need a specific fixture *order*, not just fixture *presence*, to be reachable by mutation:

- **A `break`-instead-of-`continue` mutant needed the contributing row LAST, until #2124 made the refusal it exploited unreachable.** `Registry.List` returns insertion order, and the enumerator loop's only lever a test has over which mutants are reachable is that create order; with `TestRetainedModelLists_SkipsEachRefusalAndKeepsGoing`'s four rows built refusal-refusal-refusal-contributor, `break` and `continue` produced the same one-payload result unless the contributor came last. #2124 narrowed `resolveBoundModelList`'s refusal surface to a single arm (registry miss) and gave every other arm a fallback, so with a vocabulary retained no row `List` yields can refuse at all — the `continue` branch is now unreachable in that test and the `break` mutant is **equivalent**, not survived. The coverage was re-aimed rather than deleted: the test became a four-row enumeration where every row contributes (bound-and-armed, unbound, dangling binding, bound-to-a-silent-session), asserting a count of four and one payload per id, which is sole-red for the mutant that still matters — `append` followed by an early exit. **Worth checking whenever a change turns refusals into successes**: a loop-skip test built around create order can keep passing while pinning nothing once the skips it relied on stop happening.
- **"Archived rows contribute" has exactly one home.** Every other fixture in the file builds unarchived rows, so a `List(ListFilter{IsArchived: &f})` narrowing that would silently exclude archived conversations is invisible to all of them — only the one test that arms an archived, list-bound row can catch it. Confirmed sole-red by overlay mutation. Worth stating explicitly because the property reads like an incidental choice in the diff otherwise — nothing at the call site says `List()` was chosen over a filter on purpose.

`retainedModelLists` is `resolveBoundModelList`'s first production caller — the resolver shipped in #1857 with none, reachable only from its own tests. #1868 is the cross-process e2e capstone, the historical modal/queue split used for #903/#904; see [e2e-harness.md](e2e-harness.md) for the test's own design (including the live-lane race noted above, which its mutation run surfaced).

**`resolveBoundModelList` gained a second production caller in #2125**: `modelListFor(convReg, pool)`, the conversation-keyed adapter behind the on-demand [`ModelListFor` seam](v2-session-manager-state-machine-inbound-request-model-list-modellistfor-seam.md), sits beside `retainedModelLists` in the same file and forwards the resolver's comma-ok untouched — no filter, no second lookup, no re-derivation of which vocabulary answers, the same discipline `retainedModelLists` itself holds to. It is a **named function, not the inline closure #2125's plan specified**, and the reason generalises past this pair: `cmd/pyry/main.go` does not import `internal/protocol`, so an inline `func(string) (protocol.ModelListPayload, bool)` closure at the composition root would not compile without adding that import for one type annotation. `retainedModelLists` already had to make this same choice; `modelListFor` is the second data point that a seam of this shape belongs beside its twin in `session_model_list.go`, not inline in `main.go` — check the import graph before sizing a wiring line as "one line."
