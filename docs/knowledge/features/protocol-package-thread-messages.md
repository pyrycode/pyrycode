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
Live negotiation/publication and production bounded catch-up/page providers
are installed. Bounded live encoding is available through `EncodeThreadUpdate`.
The DTO declarations perform no validation, fold I/O or patch application.

Every logical changed/append update requires the held revision to equal
`base_rev` and `rev > base_rev`. A fold transition combining fields and a suffix
becomes one `ThreadItemChangedPayload` with all replacements and complete final
content; only suffix-only transitions use `ThreadTextAppendPayload`. Splitting
the combined transition into a patch followed by a same-revision append makes
strict clients reject the suffix, leaving truncated text at the final revision.
Preserve the fold's actual revisions so subsequent updates and full reads agree;
see [daemon publication](thread-package-shadow-lifecycle-and-evidence-publication.md#background-discovery-and-publication).

Split the serialized logical update, not just kind-specific content: a large
`summary` or other metadata can exceed the cap even when content fits. This
also preserves unknown JSON and patch omission versus explicit clears without
a known-kind field list. `ThreadUpdatePart` fragments require ordered, complete
assembly before committing item fields, `rev` and `version`; changes/appends
still require the original `base_rev` and strict revision advancement after
complete assembly. Fragment progress never becomes history progress. See the
[assembly contract](../../protocol-mobile.md#bounded-thread-encoding-and-assembly).

Measure the whole escaped envelope: fragment strings escape logical JSON again,
so an ordinary-payload size check can leave continuations over cap. Reserve
later numeric/timestamp/boolean stamps and supply variable `ConversationID` and
`SessionID` before encoding. Compact `Projection` must also repeat on
`ThreadUpdatePart` before sizing: stamping only the logical batch leaves
oversized fragments ambiguous and underbudgets their envelopes. If repeated
metadata leaves no fragment room, return a content-free error and no partial
sequence. See the
[measured budgets](../../protocol-mobile.md#thread-update-byte-budgets).

## Query replies and coverage

[`thread_catch_up`, `thread_page`, `thread_items` and `thread_reset`](../../protocol-mobile.md#thread-query-messages)
are declared v2 vocabulary outside `inboundAppTypeSet`. `TypeThreadCatchUp` and
`TypeThreadPage` are intercepted by the authenticated relay switch; production
providers forward to the real publisher store's bounded reads. Request limits
normalize through `NormalizeThreadQueryLimit`: omitted/zero defaults to 50 ordered rows,
positive clamps to 256, negative rejects even for nonzero catch-up. Nonzero
catch-up is never truncated; ties, active rows and parents can exceed window
limits. The [backend](thread-package-background-store-queries.md#bounded-queries) ignores
limits on nonzero catch-up, so handlers must still normalize before selecting it.

**Dispatch registration and provider readiness are separate.** An implemented
switch arm must move its type from `excludedTypes` to `inboundTypes` as
`switch-intercepted`, even when production providers remain pending. Leaving
either query type excluded makes the command package's independent AST guard
fail while relay-only tests pass. `TestEveryInboundV2TypeHasHandler` checks
coverage, reverse registration and disjoint classification. See
[relay admission and delivery gates](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md#thread-delivery-and-summary-projection).

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
batch with `final: false`. Inspect `continuation`'s JSON type before DTO decoding:
decoding every reply as `ThreadUpdatePart` rejects a valid page final, whose
continuation is numeric rather than an assembly object. Conversely, decoding
every reply as `ThreadItemsPayload` rejects the object continuation of a
fragmented row. Gates needing only shared epoch/version/projection/query-final
fields should decode those fields independently of continuation shape;
`threadQueryGate` must establish its temporary checkpoint for the selected reply
even from the first fragment, without treating it as a completed join. Ordinary
rows alone cannot test that boundary; see the
[held-delivery regression](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md).
Fragment `continuation.final` completes only validated assembly; an interrupted assembly
followed by a request marker cannot
complete the query. Variable envelope conversation/session metadata must be
supplied before encoding and preserved, as must request correlation. Errors
return nil output, including failure after an earlier batch could fit.

The [bounded expansion reads](../../protocol-mobile.md#bounded-expansion-reads)
are installed: #3523 binds both real Store providers, and #3549/#3550 supply
authenticated switch-intercepted body/child handling. `thread_expansion`
negotiation requires both providers, existing thread readiness and client
`thread_expansion`, `thread` and `interactive`. `ThreadSummary` explicitly omits
detail; its bounded headline and metadata cannot stand in for a full item.
An available empty body is distinct from omitted detail or a refused read.

`ThreadSummary.TaskID` is an optional exact association string: nil is unknown,
empty is known none, and nonempty identifies the fold-selected task for this
creating row. `NewThreadSummary` deliberately leaves it unknown: arbitrary
`ThreadItem.Content` can contain a forged `task_id` and is no authority to infer
one. Only prepared Store facts supply it through the query/live translators;
see [association preparation and checkpoint reconstruction](thread-package-background-store-queries.md#revision-scoped-direct-child-summaries).
Unlike headline/error text, task identity must never be normalized or truncated.
A replacement with empty clears association; omission leaves it unknown rather
than certifying an older value. `TestSummaryTaskUnknown` pins the constructor
boundary, while relay `TestSummaryTaskWire` and daemon `TestDaemonSummaryTaskLive`
check encoded unknown/empty/exact facts without content or task-report payloads.
`TestDaemonSummaryTaskProducer` exercises actual encoded initial/older roots and
direct-child replies from the installed producer. The association grants neither
detail access nor coverage; existing row metadata owns placement and lifecycle.

The separate [compact projection contract](../../protocol-mobile.md#compact-projection-declared)
is production-active (#3563): the daemon installs both compact queries on the
publisher Store, with readiness requiring complete query/live wiring and both
expansion reads. Authenticated relay selection gates `thread_compact_update`
per connection and conversation. Complete query/live delivery stays the default.
Compact publication coalesces final root replacements on the existing 50 ms
pass, or emits consumed progress; oversized full roots retain bounded assembly.
Client adoption remains downstream. Compact checkpoints
are scoped by conversation/projection/epoch and certify only root windows or
source intervals. Complete-view evidence cannot initialize compact coverage;
incompatible scope requires version-zero initialization. Expansion proves only
its requested body/page, and assembly final proves only assembly. None substitutes
for a successful query marker or certifies downloaded subtree detail.

Ancestor `subtree_version` can change while source `rev` stays fixed: using only
revision equality would keep stale child knowledge open. Invalidate cached
subtree knowledge on changed freshness and explicitly refresh demanded bodies
and pages; absent freshness proves nothing. `ThreadSummary.NoChild` records
attribution, so it cannot replace optional public-child existence/count facts.
The same header facts fit explicitly requested child summaries. Expansion does
not select a view or subscribe descendants to full live updates. Body cache
identity includes conversation/epoch/item/source revision, while child cursors
also bind the consumed capture version. See the linked compact and expansion
contracts for omission, text bounds and stale refusal.

Expansion completion certifies only the requested body or child page at its
captured epoch/version and item/parent revision. A fragment final completes
assembly only; require all data and the separate success marker. Only an
authoritative child marker with `more: false` finishes that parent's enumeration.
No expansion result advances full-thread recovery checkpoints or live join
watermarks. Capture-bound child cursors resume exclusively after `(order, id)`,
including zero orders and ties; scope/revision/epoch/consumed-version mismatch
requires a fresh first page, not a retained snapshot.

`EncodeThreadChildren` budgets the **whole page**, not just the summary batch:
the separate marker repeats variable routing metadata and needs its own maximum
stamp reservation. A batch can fit while its page exceeds 65519 bytes.
`TestThreadExpansionBudgets` measures both envelopes at the exact aggregate
ceiling and proves that a byte-excluded suffix still sets `more`. If children
remain but no row fits, refuse instead of certifying empty success.
`EncodeThreadToolBody` instead uses the per-envelope cap and existing whole-payload
assembly, preserving unknown fields and upstream content limits.

## Testing

To prove encoding failure exposes no usable prefix, force an earlier batch to
flush before injecting invalid JSON/UTF-8. A bad item behind one unflushed batch
would leave a prefix-return bug unexercised. `TestThreadQueryErrors` uses two
otherwise encodable large items before the invalid one. See
[protocol boundary checks](development-verification-protocol-boundaries.md#protocol-boundaries).
