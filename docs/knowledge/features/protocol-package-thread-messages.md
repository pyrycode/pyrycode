# Daemon thread messages

Wire vocabulary and encoding stay independent of `internal/thread`. The
[mobile contract](../../protocol-mobile.md#daemon-thread-updates-v2-supplied-delivery-contract)
and [ADR 042](../decisions/042-daemon-built-thread.md) own the schemas and design.

## Live updates

`TypeThreadItemAdded = "thread_item_added"`,
`TypeThreadItemChanged = "thread_item_changed"` and
`TypeThreadTextAppend = "thread_text_append"` are outbound v2 types,
production emitted by `daemonThreadPublisher` (#3164). They stay outside
`inboundAppTypeSet`; declaring outgoing vocabulary must not admit it into the
inbound handler chain.
`TestThreadKinds`, `TestTypeConstants_V1V2Partition` and
`TestEveryInboundV2TypeHasHandler` pin that boundary. See the
[full item and update schemas](../../protocol-mobile.md#daemon-thread-updates-v2-supplied-delivery-contract).

`ThreadItem` carries the fold's facts as pure data. Mapping stays in consumers:
the fold already imports protocol, so importing `internal/thread` here would
reverse the dependency and create a cycle. `Content` and `Changes` retain inert
raw JSON so unknown fields survive, and absent patch keys remain distinct from
explicit empty/false/zero/null replacements. A content patch replaces the whole
value; decoding it into a partial content struct would discard unknown facts.
Consumers own numeric bounds, stable identity/kind and patch applicability.
`TestThreadItemAddedRoundTrip`, `TestThreadItemOptionalFacts`,
`TestThreadChangesRoundTrip` and `TestThreadTextAppendRoundTrip` re-encode decoded
DTOs with distinct item/order/revision/version values and inspect emitted JSON.
Live negotiation/publication and relay catch-up handling are installed;
history-page handling remains #3235 and production query providers remain
\#3236. Bounded live encoding is available through
`EncodeThreadUpdate`. The DTO declarations perform no validation, fold I/O or
patch application.

Split the serialized logical update, not just kind-specific content: a large
`summary` or other metadata can exceed the cap even when content fits. This
also preserves unknown JSON and patch omission versus explicit clears without
a known-kind field list. `ThreadUpdatePart` fragments require ordered, complete
assembly before committing item fields, `rev` and `version`; changes/appends
still require the original `base_rev`. Fragment progress never becomes history
progress. See the
[assembly contract](../../protocol-mobile.md#bounded-thread-encoding-and-assembly).

Measure the whole escaped envelope: fragment strings escape logical JSON again,
so an ordinary-payload size check can leave continuations over cap. Reserve
later numeric/timestamp/boolean stamps and supply variable `ConversationID` and
`SessionID` before encoding. If repeated metadata leaves no fragment room,
return a content-free error and no partial sequence. See the
[measured budgets](../../protocol-mobile.md#thread-update-byte-budgets).

## Query replies and coverage

[`thread_catch_up`, `thread_page`, `thread_items` and `thread_reset`](../../protocol-mobile.md#thread-query-messages)
are declared v2 vocabulary outside `inboundAppTypeSet`. `TypeThreadCatchUp` is
intercepted by the authenticated relay switch; history pages remain #3235 and
production query provider installation remains #3236. Request limits normalize
through `NormalizeThreadQueryLimit`: omitted/zero defaults to 50 ordered rows,
positive clamps to 256, negative rejects even for nonzero catch-up. Nonzero
catch-up is never truncated; ties, active rows and parents can exceed window
limits. The [backend](thread-package-background-store.md#bounded-queries) ignores
limits on nonzero catch-up, so handlers must still normalize before selecting it.

**Dispatch registration and provider readiness are separate.** An implemented
switch arm must move its type from `excludedTypes` to `inboundTypes` as
`switch-intercepted`, even when production providers remain pending. Leaving
`TypeThreadCatchUp` excluded makes the command package's independent AST guard
fail while relay-only tests pass. `TestEveryInboundV2TypeHasHandler` checks
coverage, reverse registration and disjoint classification. See
[relay admission and delivery gates](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#thread-delivery-and-summary-projection).

Only the separate successful `thread_items` marker with `items: []` and
`final: true` certifies query coverage. Every item batch has `final: false`, the
same captured consumed version and no coverage fields. Nonzero catch-up markers
carry exclusive `from_version` for `(from_version, version]`; window/page markers
carry all four page fields, including zero/false. Preserve supplied
`[lower_order, upper_order)`, `continuation == lower_order` and authoritative
`older_exists`; deriving them from returned items would mistake active/parent
extras for certified ordered coverage. Correlated reset/error is terminal and
cannot certify partial items or advance a completed checkpoint.

`EncodeThreadReply` preserves request `InReplyTo` and supplies every full item
across bounded envelopes. An oversized item reuses `ThreadUpdatePart` and
`ThreadContinuation` under `thread_items`, serializing the whole single-item
batch with `final: false`. Detect object assembly continuation before decoding
the numeric page continuation. Fragment `continuation.final` completes only
validated assembly; an interrupted assembly followed by a request marker cannot
complete the query. Variable envelope conversation/session metadata must be
supplied before encoding and preserved, as must request correlation. Errors
return nil output, including failure after an earlier batch could fit.

## Testing

To prove encoding failure exposes no usable prefix, force an earlier batch to
flush before injecting invalid JSON/UTF-8. A bad item behind one unflushed batch
would leave a prefix-return bug unexercised. `TestThreadQueryErrors` uses two
otherwise encodable large items before the invalid one. See
[protocol boundary checks](development-verification.md#protocol-boundaries).
