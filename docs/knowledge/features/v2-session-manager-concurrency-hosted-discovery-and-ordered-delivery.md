# Hosted discovery and ordered delivery

Part of [session-manager concurrency](v2-session-manager-concurrency.md).

`routeHosted` admits one pending list page per connection, including registry
read, queued reply and delivery. `Registry.List` can block under persistence;
cancel retires the page and continuation but keeps the original reader's slot
until it returns. Releasing that slot on cancel would allow repeated list/cancel
cycles to accumulate abandoned readers. `handleHostedDone` checks the original
session and pending pointers before using detached results, including after a
same-ID reconnect. A delivered page is finished work; its cursor holds no worker.
`TestHostedDiscoveryBlockedRead` requires retirement and `app.busy` responses
before releasing the read, then verifies capacity recovers without a late page.

`HostedAppsChanged` runs under the registry's commit mutex. It holds only the
leaf `hostedMu` for bounded inbox admission and signals a coalesced wake; it never
accesses mutable session/cipher state, performs peer I/O, creates workers or
calls the registry/unsubscribe. Waiting for transport here would block both
mutations and registry readers. `Run` drains distinct generation inboxes and
rechecks paired authorization before sealing. Handshake installs the inbox
before any list request; list cancellation retires list work without removing
lifecycle eligibility. See [registry callback ownership](apps-package-concurrency-and-testing.md#concurrency-and-persistence).

The control/ordinary output lane and callback handoff share at most 16 envelopes
per connection, including active delivery, queued output, pending lifecycle callbacks
and temporary ordinary-push reservations. Separate inbox/queue limits would
exceed that total; `reserveHostedPush`, `hostedEnqueue` and `HostedAppsChanged`
use the same counter under `hostedMu`. Lifecycle callback overflow still cancels
only the affected generation and signals `Run` to close it at 4413, allowing
fresh discovery to recover without silently skipping a committed revision.
Hosted controls and rekey retain terminal admission on overflow. Completed and
cancelled queue entries are cleared as well as releasing their reservations:
slice backing arrays would otherwise retain envelopes beyond the logical queue.

Asset/API metadata/chunks have eight separate destination slots per connection,
including active delivery, under `hostedInbox.resourceUsed`. They share the same
FIFO and single writer with control/ordinary output, but cannot consume its
16-envelope capacity. `TestHostedAssetControlCapacity` holds a control writer,
queues eight resource envelopes and fills all remaining control slots, then
decrypts the resumed traffic. Separate capacity keeps control admission runnable;
it does not let a later envelope pass an already sealed writer.

`startHostedResource` captures the original session/inbox, request ID and app ID;
`HostedAppsAssets.PrepareAsset` binds the exact backend process generation through
`Owner.PrepareAsset`, and `HostedAppsAPI.ForwardAPI` does so through
`Owner.ForwardAPI`. `runHostedResource` selects asset/API preparation with an
8 MiB/1 MiB response ceiling, checks metadata, reads, waits for credit/delivery
and disposes off `Run`. `Run` alone owns active operation maps,
credit indices, sender IDs and Noise. One worker sends metadata, then reserves,
copies and encodes at most one decoded chunk and waits for its transport outcome
before producing another. Each chunk yields to other producers. A blocked
backend, credit wait or writer leaves manager control and other connections
runnable. Hosted routing stays at `routeHosted`, outside generic `dispatch.Route`;
the relay server parses no app content. Production readiness remains withheld
for [#3248](https://github.com/pyrycode/pyrycode/issues/3248).

`parseHostedRequest` requires API `body` strings and string-valued `headers`
objects, including empty values, under the shared exact JSON rules.
`hostedBodySize` validates the complete standard padded base64 alphabet, padding
and unused bits and computes the exact decoded length before decoded allocation.
`base64.Encoding.DecodedLen` is an upper bound: using it would reject a valid
32768-byte padded body. Go's strict decoder still accepts CR/LF, so strict decode
alone would also admit forbidden encodings. Malformed input is
`app.invalid_request`; valid encoding over the decoded limit is
`app.limit_exceeded`. Neither reaches the API provider. See
[API body validation](../../protocol-mobile.md#api-request-validation).

`hostedResourceBudget` uses a leaf mutex for counts (8 per connection generation,
32 per app across connections, 128 per daemon) and decoded bytes (16 MiB per
connection, 64 MiB per daemon), shared by assets and APIs. Admission precedes
preparation; `runHostedResource` reserves the exact decoded API request size
before decoding or invoking the provider. Request bytes, staged responses and
queued decoded copies all count together. Reservations precede staging and
decoded-copy allocation/read, including incremental growth through EOF. Current
staging uses memory, with the same budget required for any future spool.
EOF retains the stable-body reservation until disposal, and its temporary probe
byte also needs capacity. Two full
8 MiB staged bodies therefore cannot bypass the connection ceiling just because
their advertised sizes sum to 16 MiB. Count/reservation exhaustion returns
retryable `app.busy` with `retry_after_s: 1`; body/chunk overflow is
`app.limit_exceeded`. Cancellation removes queued output before releasing its
copy reservation, and workers hold admission until backend disposal returns.
`resourceOutput` waits for delivery/discard or the original inbox's retirement:
backend `Done` alone cannot release a copy still queued for transport.
API request ownership survives cancellation or failed preparation until worker
disposal. Cleanup disposes the response first, releases request bytes next and
releases admission last, exactly once. Releasing request bytes when forwarding
returns would undercount the retained operation; releasing admission on cancellation
would allow abandoned cleanup to accumulate outside the operation ceiling.
`TestHostedAssetAdmission` holds cancelled preparation cleanup to prove that
cancellation cannot accumulate replacement workers; `TestHostedResourceBudgets`
and `TestHostedAssetByteCapacity` check exact ceilings and recovery.
`TestHostedAPIMixedCapacity` holds asset/API cleanup and checks their combined
request/staging ownership. `TestHostedAPIByteCapacity` checks connection/daemon
exhaustion, request reservation refusal before provider invocation and the EOF
probe at exact aggregate capacity; even an empty response needs probe headroom.

Initial credit grants chunk 0 only. `creditHostedResource` requires the captured
connection/request/app identity and exactly the next index after its predecessor
has delivered. Duplicate granted credit is silent and grants nothing; wrong-app,
future/skipped or malformed active credit retires the original request with
`app.invalid_request`. Finished/retired credit is silent, including while final
cleanup still occupies admission. `Run` tracks `finalDelivered` separately from
backend `Done`: successful `PreparedResponse.Complete` closes that signal too.
The trailing control sentinel in `TestHostedAssetTransfer` detects a spurious
unavailable error after complete bytes, which a digest-only assertion misses.

The fixed deadline is 30 seconds from daemon acceptance, including preparation
and resource writes. Five-second inactivity measures monotonic elapsed time
since a positive-byte preparation/response read or new valid credit. Reservation
activity, zero-byte reads, EOF without bytes and duplicate credit do not reset it.
Optional
[preparation progress](apps-package-resource-serving.md#stable-response-ownership)
observes individual reads inside `io.ReadFull`; a five-second preparation timer
alone would reject a continuously reading source. Nothing extends the acceptance
deadline or revives already expired work. Sweep, credit and output guards check
expiry; timeout cancels backend I/O and returns `app.timeout` while the generation
is usable. `TestHostedAssetTimeouts` and `TestHostedAssetReadProgress` distinguish
progressive/stalled preparation and reads, held credit and a fixed deadline
despite progress. `prepareHostedAPI` supplies `APIRequest.Progress`, which
`Owner.ForwardAPI` carries through `prepareAPI` to source staging reads.
`TestHostedAPIProgress` distinguishes progressive/stalled API preparation and
the fixed deadline despite progress; decoded request reservation is not progress.

App-scoped cancellation matches the original request and app on that connection;
unscoped cancellation still addresses list work. Usable-generation acknowledgements
reply to the cancel ID and echo target/scope, including unknown, duplicate,
finished and mismatched-scope targets. Disconnect, revocation, release/process
retirement and shutdown prevent later admission or rebinding. Remove retired
unsealed output; cancellation/failure of active sealed output retires the
connection to avoid a nonce gap. Cleanup stays off `Run` and late results check
original pointers, so reused connection/request IDs cannot revive old work.
Transport completion establishes delivery, not app success or rollback of an
uncertain mutation. Each API request is forwarded once: cancellation, timeout
and disconnect neither retry it nor assert rollback of a possibly committed
write. `TestHostedAPIMutationReconnect` checks invocation counts and suppresses
late completion after reconnect reuses IDs; `TestHostedAPIRealTransport`'s
`blocked_true` subtest checks connection retirement when cancellation targets
sealed output. Native assembly/view retirement remains client-owned; see
the [wire contract](../../protocol-mobile.md#asset-transfers-credit-and-cancellation).

Ordinary bounded pushes wait unsealed upstream when this lane is full. A legal
reconnect batch can exceed the destination's 16 slots even for an
`interactive`-only client whose handshake installs hosted delivery; popping the
batch without destination admission would turn delayed transport completion
into an overflow close. `drainOnce` reserves one slot before popping, transfers
it once into `hostedEnqueue`, and releases unused capacity if forwarding filters
or rejects the head. Callback admission competes atomically with that reservation
and may still retire the generation on genuine overflow. `pushMu` and `hostedMu`
are taken separately. Candidate selection alone cannot protect quarantine across
that off-lock admission: the final pop must reread the queue's retirement latch
under `pushMu`, or `RetireLiveState` or byte-ceiling retirement could leave a
quarantined head eligible for delivery. `TestHostedPushAdmissionRetirement`
forces both races and checks that declined output returns its reservation.

The same boundary applies to `drainLiveOnce`: after `reserveHostedPush`, recheck
`pushRetired` before consuming `livePending` or invoking `liveSnapshot`, and
release the unused reservation on retirement. An entry-only check can pass
before admission blocks on `hostedMu`, then consume a quarantined reading after
the producer retires. `TestHostedLiveAdmissionRetirement` holds that lock across
retirement for both sources and checks unchanged pending state, zero snapshot
reads, no retained output and no retained capacity. A test that retires before
entering the drain cannot expose this race.

`releaseHosted` signals the drain when capacity returns, including delivery
completion through `handleHostedDone` and cancelled queued pages. Capacity-held
connections do not re-signal themselves; another connection and manager control
work can progress, and held output resumes without another producer push.
Inline ordinary replies use the same reservation or enter the bounded upstream
FIFO as already-projected plaintext. The queue's `ready` tag prevents projection
and revision checks from being applied twice. Supplying a fresh reading behind
a deferred clear preserves their upstream order; see
[supplied current-state delivery](v2-session-manager-concurrency.md#supplied-current-state-delivery).

A successful synchronous `Conn.Reply` fences a subsequent handler announcement
at Run processing (#3545). For legacy output, forwarding finishes before the
receipt closes; for hosted output, `hostedEnvelope` admits plaintext or defers it
to the bounded upstream FIFO before acknowledging. Subsequent `Push` output
follows in that same lane, so the connected requester sees the correlated reply
before its uncorrelated broadcast, with unchanged payloads and sibling fanout.
Waiting for transport receipt instead would stall the handler behind a slow
client; admission preserves order while delivery remains pending. Neither
channel admission nor an unbuffered receive alone establishes this boundary.

`TestV2Session_ReplyBeforeAnnouncement` holds the off-Run pre-handoff event,
observes synchronous Send blocking or announcement completion, then authenticates
requester and sibling output in arrival order. Its ordered arm holds transport
until the handler finishes announcing, distinguishing processing from receipt.
Restoring buffered dispatch makes both legacy and ordered arms fail: a
race-free, source-level Reply-before-announce assertion would miss the defect.
See [ordering proof conventions][verification-tests].

Off-Run `DeliverThread` callers wait for this same inbox's capacity and terminal
transport outcome; a full lane must defer admission rather than report success
or overflow for a legal codec sequence. Keep the capacity notification tied to
the captured inbox generation, so a reused connection ID cannot admit old work
into its replacement. Buffered result publication never blocks Run. The caller
retains its own result-channel reference: `hostedOutput.finish` clears the
output's mutable field, so reading that field again could strand the waiter.
Enqueue, `forwardToRun` handoff and source flush do not prove callback acceptance;
see [delivery outcomes](../../protocol-mobile.md#ordered-delivery-and-eligibility).

`DeliverThread`'s JSON round-trip detaches input only when decoding into a fresh
zero-value `Envelope`. Decoding into a shallow copy can reuse borrowed
`json.RawMessage` backing arrays and pointer fields, rewriting producer bytes
and allowing later mutations to change queued output. `TestThreadDeliveryInputIsolation`
uses whitespace-bearing JSON and shared metadata, authenticates reuse for a
second recipient, and mutates producer data after admission to verify the queued
snapshot stays independent.

`hostedEnvelope` admits plaintext; `hostedDeliver` assigns a fresh connection-local
sender ID and seals only the selected head on `Run`. Mixing pre-sealed legacy
pushes/replies with queued hosted plaintext would spend Noise nonces in a different
order from delivery despite race-free cipher ownership. `forwardAppReply`,
`forwardEnvelopeSource` and `emitRekeyRequest` therefore share this lane once
installed. IDs start above `hello_ack` and earlier legacy output. Rekey response
delivery is a barrier: old-key output drains, `noise_resp` installs the send
cipher, then new-key output follows. A second rekey during that barrier closes
the connection. `TestHostedDiscoveryOrderedDelivery` and
`TestHostedDiscoveryRekeyOrdering` decrypt resumed output and check IDs; merely
running the race detector would miss ordering defects.

Every fully serialized hosted plaintext envelope is checked against 65519 bytes
before encryption, including discovery/control and asset output. There remains
exactly one active writer across queued resource retirement: `retireHostedResource`
may enqueue a terminal error through `hostedEnqueue`, recursively starting the
next queued writer. `hostedDeliver` must recheck active delivery and connection
state after retirement before selecting another head, or the outer call can
overwrite that writer's output/context/cancellation and reorder sealed frames.
A pre-selection sweep and exclusive cipher ownership cannot prove this invariant.
`TestHostedAssetQueuedRetirementSingleWriter` retires the queued head by timeout
or backend signal, holds the next control writer, and checks both authenticated
publication order and connection retirement on writer failure.

Encrypted real-transport fixtures must allow legal chunk framing after both
inner and outer base64 expansion; the default WebSocket read limit can reject a
valid 32768-byte decoded chunk before the transfer is tested.
`TestHostedAssetRealTransportBlockedWriter` and
`TestHostedAPIRealTransport/blocked_true` hold pump acceptance before writing,
cancel while the gate is held, and require session retirement and idle resource
reservations before release. After release, peer socket retirement fences all
prior publications before checking for late sealed output. A manager snapshot
immediately after writing cancellation can precede its processing; session
removal or sender completion alone cannot prove that a blocked transport has
stopped publishing. An already-cancelled WebSocket write context can still allow
publication, so the [transport pump](transport-package.md#lifecycle-and-goroutines)
must check cancellation before writing and retire the affected socket generation.

`TestReadinessLegacyReconnectSnapshotCapacity` holds the real transport's
acknowledgment through handshake completion, proves `FlushPushes` is still
waiting, releases delivery, then decrypts 64 distinct retained model lists once
in FIFO order with increasing sender IDs. Transport entry alone is too early
to fence handshake reconciliation: wait for the separate Run barrier before
starting flush, or an empty source queue could make a broken drain appear sound.
Awaiting successful flush before releasing acknowledgment would deadlock the
correct capacity hold. Fixture observation channels must accommodate the full
batch or be consumed concurrently; a 32-entry tap can block a 64-frame proof
independently of production capacity. A subsequent transport receipt and wire
marker check for trailing duplicate or terminal output beyond the source fence.
`TestHostedPushCapacityPendingFence` additionally prevents a marker passing an
admitted clear while its fresh reading still waits outside the lane; ordinary
FIFO ordering alone would miss that violation. `TestHostedPushCapacityDelivery`
and `TestHostedPushCapacityTeardown` cover resume, disconnect and caller
cancellation. See [completion-test barriers][verification-tests].

Checking the original session on completion does not fence a delayed terminal
close addressed by connection ID. `hostedCloseContext` gives it a cancellable
lifetime; `handleFrame` calls `cancelHostedClose` when creating a replacement,
before the new handshake emits bytes. Completion removes only its own close
context, so it cannot cancel a successor's close. Distinct inboxes likewise keep
old events and overflow signals out of a reused connection. `TestHostedLifecycleReconnect`
holds both predecessor delivery and terminal close through the replacement
handshake, then releases them and decrypts new discovery/notification output.
An admission or completion-only assertion would miss a late predecessor close.
A down/up fixture must also await terminal-close publication before recording
the next handshake's output offset: session deletion alone can leave the old
close looking like the new Noise response. `TestThreadDeliveryFailure` uses
`testHostedClose` to fence both publication and retirement before reopening;
`testThreadQueryInterrupted` uses the same fence in its transport-down arm.
Peer-close reuse remains silent and has no terminal envelope to await.

An interruption fixture's sender-ready signal must follow accepted prefix
delivery. Signalling before `OutboundContext` accepts the prefix allows a live
update or peer cancellation to abort that first send, leaving the test waiting
for progress it never established. Register sender-exit notification before any
return so failed/cancelled prefix sends cannot strand the observer. The shared
catch-up/page fixture in `testThreadQueryInterrupted` applies these two barriers
before interrupting and awaiting sender completion.

`pushSource` cancels an active thread query before looking up its push queue.
Cancellation of a sealed page can retire the connection before that lookup, so
the interrupting live push may return `ErrConnNotFound`. Requiring successful
admission races the required retirement. `TestThreadCatchUpInterrupted` and
`TestThreadPageInterrupted` accept only success or that error for live interruption;
their `live-retired` arms await retirement first and require the error. Keep prefix,
sender-exit, teardown and no-successful-final-marker assertions, and continue
requiring successful admission in ordinary live-delivery tests.

`hostedSender` selects `HostedAppsSend` or `OutboundContext`, never context-free
`Outbound`. A thread handshake initializes this lane only with `OutboundContext`
(or the existing hosted configuration); installing it on an outbound-only legacy
connection would retire otherwise valid traffic. `DeliverThread` returns
`ErrConnNotFound` when no ordered lane exists. The callback must honor
cancellation/deadlines through actual I/O
and suppress publication after retirement. A goroutine around a synchronous
callback cannot establish that guarantee. Missing safe adapters retire locally
before sealing or worker creation, including unsupported controls while
discovery is disabled. Delivery has a five-second deadline; terminal-close
contexts inherit Run cancellation, including `Frames` closure. Queue overflow,
failed sealed delivery and cancellation of an active sealed page close the
affected connection because continuing would leave a Noise nonce gap. Queued
unsealed pages can be removed and acknowledged without consuming a nonce.
Other connections remain runnable during blocked reads or delivery. The
[transport pump](transport-package.md#lifecycle-and-goroutines) must preserve
admission order across legacy, initial-handshake and cancellable output too.
See the [wire behavior](../../protocol-mobile.md#paging-cancellation-and-validation).

[verification-tests]: development-verification-prove-that-tests-distinguish-the-change.md#prove-that-tests-distinguish-the-change
