# Model-list payload (#1704 shape, #1705 fixtures + docs; producer #1848/#1849, second production path #1857)

The v2 wire shape for claude's **model inventory** — the `models` array a `control_request` with subtype `initialize` returns on the child's held-open stdin. Declared ahead of its producer, the sequencing #1405 used ahead of #1410 and #1616 ahead of #1638: the shape landed so [pyrycode-desktop#561](https://github.com/pyrycode/pyrycode-desktop/issues/561) (model menu, blocked since 2026-08-19) and [pyrycode-desktop#682](https://github.com/pyrycode/pyrycode-desktop/issues/682) (permission-mode menu) could be written against it before a producer existed. `internal/turnbridge`'s `MapEvent` constructs it (#1848, mapping `turnevent.ModelList` 1:1) and `interactiveTurnEmitterV2.Handle` carries the arm that calls it (#1849) — a decoded list now reaches every interactive conn, proven end to end by #1845. See [turnbridge-package.md](turnbridge-package.md)'s per-variant table for the mapping.

**One filler, two production paths — don't call it "two constructors."** `cmd/pyry`'s `resolveBoundModelList` (#1857) is a second route that yields this type: it resolves a conversation's bound session and reads its retained `turnevent.ModelList`, but it routes that list back through `turnbridge.MapEvent` rather than filling `ModelListPayload`'s fields itself. Six of its seven returns are the zero-value refusal `protocol.ModelListPayload{}`; only the one that reaches `MapEvent` is non-zero. #1848's arm remains the only site that actually fills the fields.

Legacy live delivery is best-effort: the frame is droppable at the turn-busy
fan-in (`turnMarkFor` answers `turnMarkNone`). `V2SessionConfig.RetainedModelLists`
and `cmd/pyry`'s `retainedModelLists` provide legacy connect-time reconciliation;
on-demand requests use the same inventory precedence. Supplied thread delivery
uses retained current readings and source-bearing request results instead. Its
request-driven catalog admission is described below; reconnect fallback
resolution remains [#3611](https://github.com/pyrycode/pyrycode/issues/3611).

```go
type ModelListPayload struct {
    ConversationID string        `json:"conversation_id"`
    Models         []ModelOption `json:"models"`
    DroppedModels  int           `json:"dropped_models"`
}

type ModelOption struct {
    ResolvedModel    string   `json:"resolved_model"`
    Value            string   `json:"value"`
    DisplayName      string   `json:"display_name"`
    EffortLevels     []string `json:"effort_levels"`
    SupportsAutoMode bool     `json:"supports_auto_mode"`
    TruncatedFields  []string `json:"truncated_fields"`
    Agent            string   `json:"agent,omitempty"`
    Family           string   `json:"family,omitempty"`
}
```

- **`Agent` and `Family` (#2651) are set only on the merged list a `multi_agent` client receives, and `omitempty` is the whole compatibility story.** Every other producer leaves both fields zero, so the key never reaches the wire and the frame stays byte-identical to what it was before this ticket — no second payload shape, no version branch in `MarshalJSON`. `cmd/pyry`'s `mergedModelOptions` (`session_model_list.go`) is the one place that sets them: a Claude row keeps `MapEvent`'s translation of the retained list and stamps `Agent = protocol.AgentClaude`, `Family = Value`; a Codex row comes from the vocabulary store's `CodexModels()` (#2627, family as `Value`, no display name) and is stamped `Agent = protocol.AgentCodex`, `Family = Value`. **`DisplayName` and `SupportsAutoMode` (#2667):** Codex publishes no display string of its own, so `DisplayName` is derived from `ResolvedModel` by `codexDisplayName` (`session_model_list.go`) — `gpt-` becomes `GPT-`, the version stays, and each hyphen part of the family is capitalised (`gpt-6-luna` → `GPT-6 Luna`, `gpt-5.1-codex-mini` → `GPT-5.1 Codex Mini`); an id not shaped `gpt-<version>-<family>` falls back to `Family`, reachable only through a hand-edited store file since `codexsup`'s `parseFamilyID` gates what a live spawn writes there. Every Codex row sets `SupportsAutoMode = true`: the daemon already accepts `auto` for Codex (`validPermissionMode`, `codexTurnOverrides` mapping it to `on-request` with the `auto_review` reviewer), so the flag stops the menu from hiding a posture the daemon was already willing to take — it does not open a new one. The merged slice is Claude's held entries first, in their held order, then Codex's in the store's order; `DroppedModels` still counts only Claude's tail cut, with no combined cap of its own (reasoned in the ticket's security review under Network & I/O). `mergedModelOptions` returns `ok == false` exactly when neither agent has an entry, preserving the never-empty-`Models` contract this file states above. See [Connect-time model-list reconcile](v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md) and [Inbound `request_model_list`](v2-session-manager-state-machine-inbound-request-model-list-modellistfor-seam.md) for the two seams that now choose between this list and the unmerged one per conn.

- **`Value` is the first field in this payload family a client is meant to send back, and precedent-matching would have hidden that its direction is new.** Every sibling payload's SECURITY paragraph ends "it is a REPORT, never a control input" (`ModelAnnouncedPayload`'s included); copying that sentence unchanged onto `Value` would have shipped a doc making a promise the daemon does not keep. The only inbound path that accepts a model is `set_session_settings`: `validModel` first enforces its closed grammar, then #2281 checks exact membership against the same retained vocabulary used to publish this payload. Only an untruncated `Value` matches — never `ResolvedModel` or `DisplayName` — and incomplete vocabulary produces a retryable unavailable result instead of pretending to prove absence. Measured against the five values claude returned on 2026-08-21 (`default`, `sonnet`, `haiku`, `opus[1m]`, `claude-fable-5[1m]`), the two bracketed variants originally failed — the bracket sat outside the flat charset — the same defect class desktop#682 reports for permission modes. **#1838 closed that gap**: `validModel` now accepts one optional trailing bracket group drawn from the same closed byte class as the base, so every complete value measured to date passes the shape gate and round-trips through membership; see [Inbound `set_session_settings`](v2-session-manager-state-machine-inbound-set-session-settings-settingsupd.md) for both gates. `EffortLevels` carries the identical direction hazard and is **not** widened alongside it — `internal/relay`'s `validEffort` is a closed enum that happens to accept all five levels claude returns today (measured 2026-08-22), so a level claude adds later would be published and refused inbound the same way; #1838 deliberately left it alone, evidence-based rather than widening against a hypothetical.
- **`ResolvedModel` closes a question desktop#561 raised and could not.** That ticket's shape sends a family name that resolves to the newest member, moving the operator to a new model without choosing to; publishing the resolved identifier before the first turn removes the need to infer it from a later `model_announced`. **A lookup can miss, and that is ordinary**: #1601 established claude announces an identifier at least as specific as the one it was given, so a client resolving a `model_announced` identifier against this list may find nothing — `DisplayName` is the intended join between a `model_announced` row and a client's alias-family row, not `ResolvedModel`.
- **The three list-typed fields encode three different ways for two different reasons — read them separately, don't pattern-match one onto another.** `Models` nil → `[]`, `BackgroundTaskRosterPayload`'s reasoning transfers whole (an empty list is a positive statement). `EffortLevels` nil → `[]` too, but for a different reason: an empty effort list is a *collapse*, not a positive statement — Haiku's entry omits `supportedEffortLevels` entirely and a client behaves identically for absent vs. empty, so the wire states one position. `TruncatedFields` stays un-normalised, `null` when nothing was cut — the roster's carve-out, unchanged. Two value-receiver `MarshalJSON` methods result (payload-level, entry-level); the entry-level one cannot fold into the payload's because normalising through `p.Models[i]` would mutate the caller's backing array and wouldn't fire when a `ModelOption` marshals alone. Code review's 9-mutant matrix measured this rather than asserting it: the in-place-normalisation mutant produces **byte-identical JSON** to the correct implementation — only the post-marshal `p.Models[0].EffortLevels != nil` assertion catches it, confirming the assertion is load-bearing rather than decorative.
- **`DroppedModels` was declared with nothing counting it, and the doc said so plainly rather than implying a producer existed — since #1812, that is no longer true.** `internal/streamsup/parser.go`'s `maxModelListEntries = 10` bounds the entry count, truncating from the tail, and the decode records the cut as `DroppedModels`. `internal/turnbridge`'s `MapEvent` carries the count verbatim (#1848, never recomputed from `len(models)`) and `cmd/pyry` emits it (#1849), so `len(Models) + DroppedModels` **is** the menu's true size all the way to the wire — `ModelListPayload`'s Go doc comment was right about this the whole time; `docs/protocol-mobile.md` § `model_list` was the side that had it backwards, and #1860 corrected it there.
- **The wire-name naming pin has the same trap the sibling `model_announced` block documents for itself.** `TestModelListType_IsNotClaudesVocabulary` checks `TypeModelList` against claude's own words on this path (`initialize`, `models`) with negative `strings.Contains` checks, plus a positive exact-equality pin — the pin is required because a `strings.Contains(…, "model")` check would be **red against the correct name** (`model` is this frame's own subject noun), so the negative checks alone would leave every wrong name green.
- **Fixed by #1848, the same class already on record for `BackgroundTask` (#1393):** `ModelOption.TruncatedFields`'s doc comment enumerated the cut-field vocabulary as `("value", "display_name")` — 2-of-4 against the four names the producer (`streamsup`'s `emitModelList`) actually records. Every other `TruncatedFields` doc in this file enumerates exhaustively; this one didn't. Nothing broke at runtime — the field is a free-form `[]string` — but it was the line a reader would pick names from, and #1848 is what read it to build the `turnbridge` mapping, catching the gap in the same ticket that would otherwise have under-covered `ResolvedModel` and `EffortLevels`. Now enumerates all four in producer order (`resolved_model`, `value`, `display_name`, `effort_levels`), with `effort_levels` called out as the one reporting on a list rather than a scalar.
- **Live emission and recovery have different guarantees.** `turnbridge.MapEvent`
  maps an admitted `turnevent.ModelList` verbatim, and
  `interactiveTurnEmitterV2.Handle` publishes it without opening or closing a
  turn. The legacy fan-in can drop the frame, so clients must also support
  reconciliation or on-demand resolution. Bootstrap and rotated-child events
  route to their own bound conversation; the bootstrap menu no longer depends
  on a foreground cursor (#2739). A supplied thread catalog reply carries no
  ring/history identity and does not establish current-session ownership. See
  [the delivery contract](../../protocol-mobile.md#model_list).


Six tests in `interactive_test.go` total. The #1704 trio — `TestModelListPayload_NilModelsNormalises` and `TestModelOption_NilSliceEncodings` — follow `TestBackgroundTaskRosterPayload_NilTasksNormalises`'s value/pointer-subtest shape, the only way to reach the nil branch on `Models`/`EffortLevels` since decoding `[]` always yields a non-nil slice; `TestModelListType_IsNotClaudesVocabulary` follows `TestModelAnnouncedType_IsNotClaudesSubtype`'s shape. #1705 added three committed fixtures under `testdata/` (`model_list.json` populated, `model_list_empty.json`, `model_list_zero.json` — a zero-value payload whose single all-zero entry is the only route to five of the nine wire keys) with round-trip tests, closing the six scalar wire keys the #1704 pair doesn't reach. All nine `,omitempty` mutants (one per wire key on `ModelListPayload`/`ModelOption`) were run through `go test -overlay=` against a scratch copy and each reddened at least one test — re-run and confirmed independently in code review.

**A populated fixture can be redundant with a zero-value fixture unless at least one of its rows is itself the zero value.** `effort_levels`' and `supports_auto_mode`'s `omitempty` mutants reddened `TestModelListPayload_RoundTrip` (the populated fixture) *only* through Haiku's row — the one measured row whose values (`[]`, `false`) happen to equal the zero value. Had every row in the populated fixture been "interesting" (non-zero), those two mutants would have rested entirely on the zero fixture, and the populated fixture's coverage of those two keys would have been decorative. The lesson generalizes past this payload: when a zero-value fixture already exists, a populated fixture only earns distinct coverage on a key where at least one of its rows lands on that key's zero value.

**`RequestModelListPayload` (#2125)** is the one-field inbound request this reply now answers on demand, in addition to the live turn lane and the connect-time reconcile: `ConversationID string \`json:"conversation_id"\`` in `interactive.go`, no `omitempty` (absent and empty are the same case, and keeping the key on the wire lets a fixture pin the full shape), no `MarshalJSON`. Correlation rides `Envelope.InReplyTo`, so there is no request-id key — `TypeAttachmentStored`'s decision, transferred. It is declared after `ModelOption.MarshalJSON` rather than immediately beside `ModelListPayload`, keeping the *reply* family (`ModelListPayload` + `ModelOption` + both `MarshalJSON`s) contiguous and the new *request* type adjacent to what it asks for. `ModelListPayload` and `ModelOption` are otherwise **unchanged** by this ticket: no second outbound shape is minted and `turnbridge.MapEvent`'s mapping is not forked, so "the same payload the reconcile would send" means the same *source*, not fields copied. See [Inbound `request_model_list`](v2-session-manager-state-machine-inbound-request-model-list-modellistfor-seam.md) for the handler and the two coded rejects (`conversation.not_found`, `model_list.unavailable`) it answers with instead of a degraded, partially-empty `model_list`.

## Catalog admission and source evidence

`type: "model_list"` identifies catalog inventory, including its `{}` clear form.
The [supplied delivery contract](../../protocol-mobile.md#session-scoped-live-state-v2-supplied-delivery-contract)
routes by authoritative top-level `conversation_id`, matching any payload
identity. Clients may admit that catalog on the active authenticated connection
before a first turn even when `session_id` differs from the current session or is
unresolved. Omitted means unresolved evidence, `null` positively means no producing
session, and a string names the actual captured producer, possibly bootstrap or
predecessor. Stamping the target session onto borrowed bytes would invent
ownership. The tag does not identify the source of every capability-merged row.
Connection/incarnation and ordering checks remain required.

Current-session admission is still required for other live reading families,
including slash-command inventory. A catalog neither admits a session nor
authorizes settings or prompts. Full same-generation source replacement changes
only the catalog, with no additional clear or current model/effort changes.
Uncorrelated target-only clears invalidate the catalog once per target generation
before fresh inventory; later resolution can repopulate it. Equal-revision
correlated answers remain usable, while older revisions and overtaken generations
are suppressed. These counters are internal ordering inputs, not wire fields.

A new resolution of retained predecessor vocabulary owns the request's target
ordering; an old captured reply keeps its earlier ordering. Checking only the
producer conversation would miss a predecessor in the same conversation, and
matching session strings cannot distinguish a reused-ID transition. Borrowed
inventory remains reply-only and cannot change target retention or cache evidence.
See [daemon retention and regression proofs](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#control-and-on-demand-readings)
and [emitted wire examples](../../protocol-mobile.md#asking-for-a-model-list-on-demand).

See [`model_list`](../../protocol-mobile.md#model_list) for selection validation:
`value` is the offered choice, a lookup against an announcement can miss, and
published values and effort levels still require independent validation against
the target session's agent. Inventory strings remain untrusted rendering data.
