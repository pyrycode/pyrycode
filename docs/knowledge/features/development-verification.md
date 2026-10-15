# Development verification

Practices shared by Claude, Codex and the pipeline. These consolidate recurring
review findings. Historical ticket examples remain in the vault archive; current
source and the owning package documentation decide current behaviour.

## Establish the change surface

Count construction sites and consumers, not only type declarations. A payload field
can require several builders and narrow interfaces to change. Include test doubles
when widening a function type. A named configuration field may avoid breaking every
function literal, but check whether that shape suits the contract.

A matching method name does not prove two interfaces are the same dependency.
Trace the field's declared type and its assigned implementation before counting hits.
Codegraph has previously missed cross-package callers and selected the wrong symbol
when names collide. Confirm the package identity and supplement its edges with a
source search for qualified calls. The canonical index cannot see pending edits.

A new event variant has consumers beyond exhaustive test tables. Search both its
marker interface and the type switches. Default branches can silently ignore a new
variant even after the build passes. Check logging names, payload mapping, lifecycle
handling and internal busy-state tracking separately.

## Verify inherited premises

A merged dependency may have implemented more or less than the ticket predicted.
Find the production installation and the actual call site. Read existing tests before
rebuilding the same proof. Search specifications for explicitly deferred vocabulary,
missing producers and unwired callers before estimating the remaining work.

Read the validator's complete rejection behaviour before copying it. A membership
check and a live-session routing check serve different requests. Inspect the sibling
that deliberately avoids validation; its explanation often states the boundary of
an exception more clearly than the validating example.

Before copying a sibling's late guard, trace the target operation to its first side
effect. An operation that already returns without changing state may not need that
guard. A shared request shape does not establish identical side effects.

## Check searches and citations

An empty search is not proof of absence. Run a known-present control with the same
flags and paths. Use shell arrays for path lists. Use fixed-string matching for
literal text and check the regular-expression dialect before relying on boundaries.
Search a short fragment when a quoted comment may wrap across lines, then read the
named declaration. Check that cited symbols still exist and still support the claim.
Keep historical citations in historical documents unchanged.

Use the [source reference inventory](../../specs/README.md#source-reference-inventory)
after source moves or deletions to locate historical architecture-spec paths
absent from the current checkout. A missing path identifies a review candidate,
not a false current-state claim; an existing path alone does not validate the
historical claim either. Check current code and knowledge docs before reuse.

Use [scaffolding maintenance](../../specs/README.md#scaffolding-maintenance) only
with verified merged-PR closing links and an unambiguous ticket identity. A closed
issue can have no merged implementation, and a merged implementation can leave
explicitly deferred questions. Require an entire trivial resolution body or an
individual approval bound to the document hash and heading ordinal; a leading
"None" followed by prose and repeated headings cannot safely be classified by
title alone. Preserve historical designs and citations.

Markdown deletion needs block boundaries, not just heading-shaped lines.
Setext titles can span paragraph lines; thematic breaks, containers and reference
definitions change those bounds. `parse` in `cmd/spec-scaffolding-prune` retains
unsupported documents rather than guessing, and `run` prevents removal ranges
from overlapping retained questions. Structural blanks and fence endings accept
only spaces/tabs: Go's `TrimSpace` treats Unicode content as blank and can hide a
boundary or close a fence early. `markdownBlank` separates those checks from
title/resolution trimming; `markdownLines` preserves LF, CRLF and lone CR offsets.
`TestCommonMarkStructuralWhitespace`, `TestCommonMarkWhitespaceAmbiguity` and
`TestCommonMarkLineEndings` exercise preview, exact surviving bytes and repeated
apply. A parser regression can otherwise erase durable design while ordinary
LF/ASCII fixtures stay green.

Reusable GitHub evidence needs completeness and consistency across the corpus.
`gh api graphql --paginate` can stop at a nested connection while exiting zero;
verify final outer pages and nested `totalCount` values against returned nodes.
Validate PR identity facts across every issue association, since one PR can close
several issues: per-issue checks alone miss contradictory merge timestamps.
`snapshot.validate` and `TestCrossIssuePRIdentity` pin that shared-evidence boundary.

Reference extraction must validate whole tokens: splitting quotes inside a path
can turn a quoted placeholder into a root-directory citation, while stripping
periods after a slash can turn traversal or ellipsis placeholders into accepted
directories. Link extraction and tokenization must share boundaries; protecting
pipes only during tokenization still lets link-shaped glob components yield
false interior paths. In `cmd/spec-reference-inventory`, `separateLinks` and
`referenceBoundaries` share delimiter rules, including `markdownEscaped`'s
odd/even backslash parity. Balancing escaped brackets as syntax can omit both
link references and table citations on later lines. Keep accepted-context
regressions alongside whole-reference exclusions: `TestReferences`,
`TestInventoryMarkdownBoundaries` and `TestInventoryMarkdownEscapes` cover nested
labels, adjacent code citations, Unicode whitespace and escaped delimiters.

When correcting a behavioural claim, search prose as well as symbols. Counts, key
lists, test headers and comments can encode the same claim without sharing its words.
Run dependent tests and compile relevant build-tagged packages. A green assertion
can retain a false rationale after the behaviour it described has changed.

## Prove that tests distinguish the change

A real capture can already exercise an older matching branch. Check the test against
the pre-change implementation. Remove the older discriminator while preserving the
new one, and confirm that the input mutation actually changed the bytes.

For combined predicates, exercise each branch independently. A shared log site cannot
prove which branch was taken if both inputs satisfy the first condition. Use an
independent observable result of the path under test.

`reset.wrapup.write_failed` can mean the shared context expired before
`writeResetWrapUp` called the injected writer. Stale-note persistence consumes
that same budget before resolution. A matching event alone can therefore let
a delivery-error test pass without exercising its injected failure.
`TestResetThenRotate_FailedHandoffIsStale` checks resolution, dormant activation
and the first delivery attempt before expiry, and checks which callbacks ran.
Its idle case retains the busy state and observes the interrupt. Give immediate
failure classifications finite headroom separately from deliberate readiness,
idle and completion expiry; retain the stale bytes, warning/fence, secret
exclusion, bounded rotation and successful-recovery assertions. Permission-based
persistence skips must remain visible as skips.

A FIFO barrier proves that preceding queued work drained only after the
asynchronous producer has finished enqueueing the work being asserted. Join
producer completion before inserting the barrier, for both positive delivery
and zero-frame assertions. Receipt by one fanout recipient does not establish
that another recipient's frame was queued: a direct barrier push can interleave
with publication. `TestRelayAgentSwitchEncryptedFrames` awaits bounded adapter
completion, including fanout and `FlushPushes`, before the legacy-peer barrier
in both directions. Without that ordering, Claude restoration can falsely fail
and Codex withholding can falsely pass. See
[agent-switch handover testing](conversation-session-binding.md#testing-agent-switch-handover).

Shared relay output must preserve established peers' frames while awaiting a
new connection's handshake; consuming or reordering them can lose assertions or
break receive-nonce order. See the `switchWire.open` regression in
[agent-switch handover testing](conversation-session-binding.md#testing-agent-switch-handover).

Receiving the Noise handshake response establishes neither push-queue registration
nor supplied live-state snapshot capture: `V2SessionManager.handleNoiseInit` sends
the response before registering the queue and calling `ThreadLiveState`, while
`switchWire.open` returns after `ReadResp`. An immediate off-`Run` `Push` can
therefore return `ErrConnNotFound` even after successful Noise response receipt;
that alone does not prove an admitted connection was retired.
Synchronize admission and held delivery on bounded signals from the snapshot
callback itself. Otherwise a valid initial reading can arrive before the
replacement and falsely fail a fresh-only assertion. Sleeping or discarding stale
frames would obscure the delivery proof. See the
[connect-gap regression](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#relay-delivery).

A retired-snapshot absence assertion must establish snapshot evaluation completion
before queueing its identified wire barrier: `drainOnce` lets queued pushes
overtake snapshot reads in `drainLiveOnce`, so a marker can otherwise arrive with
the cursor still unvisited. `TestDaemonLiveRelayRetiredSnapshot` uses bounded
handshake and capture gates, captures the real cursor before removing its owner,
then waits for cursor exhaustion and validates that the removed model reading
was visited. An empty cursor cannot prove stale-state rejection. Only then does
it verify the same phone remains admitted, push the trailing barrier and require
receipt on that phone of the decrypted barrier with its exact type, ID and
payload, rejecting every `model_announced` frame and unrelated output.
Preserve Noise receive order; sleeps or retrying `Push` until
success cannot establish these preconditions. See the
[retired-snapshot fixture plan](../../specs/architecture/3420-retired-snapshot-fixture.md).

A later frame on `Frames` cannot prove that `V2SessionManager.Run` consumed an
asynchronous result on a separate buffered channel: either ready select arm can
win. For a silent completion, await an observation emitted after the completion
handler's reply decision, then take a Run barrier before counting replies.
`TestV2Session_SwitchAgentLateOutcomes` waits for `v2.switch_agent.completed`
before opening its barrier connection; without that wait, a forbidden late
acknowledgement could arrive after the zero-reply assertion. To prove teardown
escapes a completion handoff, fill its channel first so sending cannot also win;
`TestV2Session_SwitchAgentHandoffEscapes` forces requester-close and manager-cancel
paths separately. See [relay concurrency](v2-session-manager-concurrency.md).

The same ordering trap applies to a withheld supplied live reply. A later
inbound frame cannot prove an asynchronous provider has queued its result.
`TestSuppliedLiveReplyWithheld` first observes the content-free
`v2.live_reply.queued` event, then pushes a permitted reading into that same
FIFO delivery queue and awaits its decrypted clear/fresh output. Only that
barrier proves the withheld predecessor passed delivery without emitting;
otherwise a forbidden late clear could arrive after a zero-frame assertion.

An already-cancelled context does not force an error when a `select` can also
observe completion: either ready case can win. Hold completion behind a
test-owned gate when proving the cancellation path. A runner gated during
teardown must be released before cleanup cancels and waits for its pool.
`t.Cleanup` runs in reverse registration order, so register the gate release
after the pool helper's cleanup; otherwise shutdown waits on a runner whose
release cannot run until that wait ends. See [session eviction fixtures](sessions-package-testing.md#cancellation-and-completed-eviction)
for the `gatedRunner` and `runPoolReady` example.

A custom cancellation fixture must propagate its declared error through derived
contexts. Overriding only `Err` on a wrapped cancel context can leave
`context.WithTimeout` using the underlying `context.Canceled` through Go's
cancel-context optimization. `testReplyFallbackContext` hides the wrapped
cancel-context value with `Value` returning nil and returns the triggered cause
from `Err`, so derived contexts observe `context.DeadlineExceeded` in the
deadline case. This test-only wrapper lets the stimulus follow child readiness;
retain a real timer-driven deadline case separately. See
[reply fallback lifecycle evidence](streamsup-package-draining-turnevents-into-the-interactive-emitter-native-reply-suggestions.md#native-reply-suggestions-after-the-result-2831).

A `context.WithCancel` stand-in cannot prove OS-signal shutdown classification:
`signal.NotifyContext` can record a signal-specific cause instead of
`context.Canceled`. Send real SIGTERM and SIGINT in isolated helper subprocesses,
since signal handlers are process-wide. Exercise both an operator-only stop and
a fatal cause recorded before the signal, asserting that the fatal sentinel
survives. `TestFatalCauseSignals` covers these four cases: removing signal-cause
matching fails the operator cases, while replacing it with `sigCtx.Err() != nil`
fails the fatal-first cases. Either half alone leaves the other bug undetected.
`TestRelay_OperatorSignalExitsZero` also signals actual daemons connected to the
fake relay, asserting exit 0, INFO `pyrycode stopped`, and absence of
`pyrycode fatal shutdown`; a control-plane `pyry stop` test never exercises the
signal parent's cause. See [daemon shutdown and exit status](cli-verb-dispatch.md#daemon-shutdown-and-exit-status).

Read the consumer above a mapper. A pure payload mapper does not decide whether a
frame is emitted. A whole message family can be ignored before subtype handling, so
absence of an error frame cannot prove which subtype branch ran. Child stdout,
client-visible frames and internal turn state are separate observations.

For an event specified as lifecycle-neutral, test both an open turn and idle state.
An open-turn assertion can prove the event did not transition or close that turn but
still stays green if the handler opens a new turn from idle. The idle case must assert
the lifecycle fields and that no synthetic lifecycle frame was emitted.

Confidentiality assertions do not prove lifecycle neutrality. A frame can reveal
no thinking text yet incorrectly publish `turn_state: thinking` for a subagent.
`TestStreamTurnDrainV2_AttributedTextExcludesThinkingAndSignature` also pins the
allowed envelope types: its earlier expectation admitted the unwanted thinking
frame while every secrecy assertion passed. Check frame types/order and state
alongside secret exclusion; see [main-turn classification](streamsup-package-per-conversation-turn-busy-tracking.md).

A verifier can use Go's `-overlay` option with replacement files outside the worktree
to test a mutation without editing the branch. Assert that each replacement applied.
Run the named test being evaluated so a neighbouring assertion cannot mask its weakness.

For asynchronous state resets, acknowledge completion of result processing before
releasing the next phase. Entry into delivery or classification can precede the
reset itself. Also observe terminal failure while awaiting progress, so a mutation
that abandons work fails on abandonment rather than a coordination timeout. The
[queue pending-reset proof](msgqueue-package-give-up.md#testing) combines that
ordering with controlled elapsed time while retaining real retry timers.

A content-free log assertion must not search for a short, common fragment of the
secret. Timestamp and metadata text can contain the same digits and make an unrelated
wall-clock value look like a disclosure. Use a distinctive sentinel or inspect the
structured attribute being protected rather than substring-matching the whole log line.

A fixture asserting that a count is independent of a list — an explicit dropped or
omitted count, never inferred by measuring what was retained — must make the count
differ from that list's own length, not merely from its sibling counts. "Mutually
distinct" is the weaker property and passes even when a count equals `len(list)`,
which is exactly the shape indistinguishable from inference; #2370's first fixture
picked `dropped_categories: 2` beside two retained rows and had to be corrected once
a stronger assertion caught it.

A test proving a check runs *before* a side effect must not stop at "the side effect
didn't happen" — a check that moved to run *after* that side effect, for an unrelated
reason, would leave the same empty end state. Make the ordering itself the
observable: give the request a second flaw that a later step would catch by a
*different*, distinguishable error, and confirm the refusal names the check under
test, not the later step. #2665's refusal cases pass `spawnDir: "/"` — which
`resolveSpawnDir` rejects — alongside the model/effort value under test; getting the
settings sentinel back, not `ErrSpawnDirRejected`, is what shows the membership
check ran first (see
[conversation-session-binding-create.md § Requested model and effort](conversation-session-binding-create.md#requested-model-and-effort-2665)).

When every refusal collapses to the same boolean or wire code, a valid registry
cannot distinguish an early filename rejection from one after lookup.
`TestWorkspaceFileReader_SecretNames` also supplies a nil registry with a denied
requested leaf: moving the check after `Registry.Get` becomes observable rather
than returning the same generic refusal. See [the live reader's filename checks](v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md#testing-the-filename-checks).

Case-variation filesystem fixtures need paths that differ by more than letter
case. On macOS's case-insensitive filesystem, upper/lowercase names can share a
file or symlink and fail during setup before exercising the predicate.
`TestWorkspaceFileReader_SecretNames` uses distinct `-lower-case` and
`-upper-case` directories while preserving the actual leaf spellings under test.

No-follow filesystem tests need a canonical fixture root before deriving paths
and exact-root references. `t.TempDir()` can inherit a symlinked `TMPDIR`; then an
ancestor rejection prevents valid-state checks from reaching the operation and
can make containment refusals pass for the wrong reason. Resolve only the
fixture root, retain deliberate unsafe leaves, and exercise successful states
alongside refusal cases under a controlled alias. See
[the apps snapshot fixture](apps-package-concurrency-and-testing.md#testing).

Unix-socket fixtures also need a short, test-owned root independent of inherited
`TMPDIR` length. Canonicalize the allocated root before deriving the socket path,
then assert the complete path is fewer than 104 bytes to leave room for termination
in macOS's `sun_path`. A real missing-socket classification fixture must require
`errors.Is(err, syscall.ENOENT)` from `net.Dial` before testing the classifier:
an oversized address can produce `EINVAL` instead, exercising path rejection
rather than the intended missing-socket case. Keep the real dial; a synthetic
ENOENT would bypass this fixture precondition. See
[control socket paths and cleanup](control-plane-testing.md#socket-paths-and-cleanup).

For a socket-path regression, run the named test in separate processes with a
deliberately long physical `TMPDIR` and a symlink alias to it, using `-race -count=1`.
Both roots must make the old fixture's canonical socket path exceed the bound;
an alias alone is not evidence of an overlong canonical path. Record both roots,
canonical old-fixture paths, commands and results, including the old allocation's
failure under the new bound assertion.

A test whose premise is "past a named cap" must build its fixture by computing from
that constant, not by restating a literal believed to be past it. A hardcoded number
keeps passing for the wrong reason after the cap changes, or silently stops testing
the boundary at all — [`maxModelVocabularyFile`'s oversized-file
test](streamsup-package-retaining-the-decoded-model-list-for-the-session.md#holding-codexs-model-families-beside-claudes-2627)
derives its fixture from the constant for exactly this reason.

Before removing a log record a test syncs on, grep for the record's field values, not
only its event name. A test can wait on one discriminant of a structured log line
(`kind=mcp_status`, a session id, a conversation id) without the event name itself
ever appearing in the test file, so a name-only grep for the record being removed
misses it. #2739 removed the stream drain's `stream_turn.not_active` drop and its
list of affected tests named every call site that synced on the record's name, but
missed four e2e tests that synced on `kind=mcp_status` specifically — found only once
`make check` turned red on the rework.

Queue placement tests must compare history payloads/timestamps, live arrival
and replay/event-id order through the wired drain and operator broadcaster.
Sequential ring appends cannot expose a reply overtaking an asynchronous user
push. When withholding `OnDelivered`, await a callback-completed signal after
the recording call before asserting that release added nothing: a barrier on
the stream drain proves only that drain's progress, not the queue goroutine's.
See [history producers](history-package-producers.md#producers-2114-2115) and
`TestOrdinaryQueuePlacement_OrderWithDelayedConfirmation`; its foreign-conversation
barrier alone leaves this late-callback assertion under-synchronized (#2820).

## Protocol boundaries

Round-trip tests must marshal the decoded payload back into the envelope. Comparing
an untouched raw payload with itself does not check struct tags or new fields.
Explicitly assert decoded field values. Distinct fixture values detect accidental
field swaps. To distinguish an absent key from a present null or empty string,
inspect the JSON produced by marshalling the decoded DTO too; decoded zero
values alone cannot prove a required key was emitted.

Pin public request keys in raw JSON, independently of production DTOs. A test
and handler can agree on the same wrong struct tag while the required client
key is ignored and an optional guard acts unbound. For `accepted_item_id`,
`TestQueueCommandItemBinding` uses literal requests for both Drop and Send now;
`TestQueueActionItemBindingWire` separately checks encoding. Range tests must
make every other predicate pass: bind the target to the supplied out-of-range
ID so identity mismatch cannot hide missing numeric validation, then test the
maximum valid identity and first invalid identity through each action.

A failed `json.Unmarshal` can leave earlier valid target fields populated.
Duplicate keys can reproduce this: a valid queue id followed by a wrong-typed
queue id returns an error while retaining the first value. Rejection tests must
target a live entry and assert no action, write, change notification, terminal
callback or wire output; assuming zero values would miss the bypass. Exercise
malformed unbound requests separately from invalid bindings so one early-return
branch cannot mask the other. See `TestQueueBindingDecodeFailure` and the
authenticated `TestV2Session_DequeueMessage_DecodeFailureIsInert`.

For optional pointer fields, check omission on the original wire bytes too:
both a missing key and JSON `null` decode to nil, and re-marshalling hides an
incorrect explicit null. For encrypted delivery, expose the authenticated
decrypted bytes rather than assert only the decoded envelope.
`TestV2Session_Reconnect_HistoryEntryID` uses this seam to pin omitted history
metadata and distinct durable/ring ids; see [envelope identities](protocol-package-types-envelope.md#replay-cursors-and-durable-read-marks).

The current protocol test helper uses `json.Compact`, which removes whitespace but
preserves key order; do not describe it as sorting keys. It also does not normalise
string escaping — `encoding/json.Marshal` escapes a literal `<`, `>` or `&` byte to
its `\u00XX` form, and `json.Compact` leaves a fixture's own bytes exactly as
written, so a fixture carrying hostile-looking markup must already spell those three
characters as `\u00XX` or a byte-exact round-trip fails for a reason that looks like
a struct-tag bug rather than an unescaped fixture.

An aggregate envelope-fit test and a producer's single-field limit prove different
things. Reference the constant in the package that owns it. A second literal in
another package can remain green after the real limit changes.

Derive hostile fixtures from the producer's actual accepted boundary and encode
the complete envelope, including source/correlation metadata and its clear.
Raw question input below a 16384-byte cap can expand beyond the envelope cap when
`<` becomes `\u003c`; an owner-rejection test alone would stay green while supported
producer output is unretainable. `TestParseEnvelopeBound` binary-searches acceptance
for literal `<`, `>` and `&` in each content field and a four-question/four-option
batch. It checks the largest accepted input and first rejected input, asserts that
the raw guard did not mask encoded rejection, and marshals actual shown/clear
envelopes with worst-case metadata. `TestControlLiveQuestionEnvelopeBound` is
enabled: it checks accepted-boundary owner admission and authenticated answer/refusal
through the original batch ID, with repeated snapshots and one-shot resolution.
`TestParseEnvelopeBoundaryRegistryIdentity` checks lookup/snapshot identity and
content retention at the boundary. See
[question producer bounds](questionbridge-package.md#bounds) and
[daemon envelope admission](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#control-and-on-demand-readings).

An outer `Encoder.SetEscapeHTML(false)` does not prevent a protocol value's custom
`MarshalJSON` from pre-escaping its strings via `json.Marshal`. A boundary fixture
must contain literal HTML-sensitive characters in the raw tool input to expose
sixfold expansion; already escaped input can reach the raw cap first and leave
the encoded guard untested. `testEscapingQuestionInput` restores those literals
after marshalling its protocol-shaped fixture (#3109).

Compare a decoded payload whole, with `reflect.DeepEqual` against a literal expected
struct, rather than field by field. That also catches a nil-vs-`[]` difference in a
slice field that field-by-field equality checks can miss. Print the mismatch with
`%#v`: `%v` and `%+v` both render a nil slice and an empty one as `[]` and hide the
distinction being tested.

Check every field before widening a provenance or logging claim. Metadata derived
by the daemon does not make an echoed client identifier daemon-authored. A queue's
client-visible text can differ from the delivered prompt, which may contain a host
path. Use the client-safe projection when persisting or serving conversation history.

When declaring a frame ahead of its producer, record that state in both the protocol
table row and the section text. Update both when emission lands. Review current group
counts and their other mentions, using the last correction as a locator. Leave dated
changelog statements historical.

## Captures and live evidence

An acceptance criterion requiring a capture must be reachable under the gate's normal
invocation. An operator-only environment switch cannot be its sole trigger. Missing
fixtures can arm a capture, with an explicit force switch for later recapture. Validate
the evidence before writing it, and keep the observed version consistent with the
reader's path and pin.

A committed-fixture reader must fail when its fixture is missing. Do not copy a skip
state from a probe that was written before evidence could exist. Conversely, a capture
that stops running after its fixture exists can retain stale assertions indefinitely.
When changing the parser's result for its frames, inspect the disarmed probe and compile
the tagged suite even if no new live capture is needed.

Do not equate a non-zero parser event count with reaching an unrecognized lane. That
shortcut becomes false when a previously silent matched frame gains a legitimate event.
State the expected event cardinality for each captured marker or subtype, and keep the
unrecognized census tied to the discriminator that actually selects that lane.

A ticket or plan can misstate which identifier a cited capture fixture actually
carries — paraphrase drifts from the bytes it describes. Verify a fed value's
identifier against the capture file itself before writing the assertion, and assert
the value actually fed, not the prose's claim about it.

Record enough redacted context to diagnose an empty capture. Count other line types,
subtypes, tool outcomes and timing without retaining secret-bearing inputs. On an
assertion failure, retain the relevant redacted response and selected result fields:
a temporary transcript path loses its evidence when fixture cleanup runs. A passing
rerun cannot establish what the failed response said. The
[allowed-tools refusal test](e2e-realclaude-allowed-tools-enforcement-test-go.md#refusal-wording-and-diagnostic-evidence)
needed this distinction to separate a missed explicit refusal from a missing signal.
A witness controlled by the test rig is stronger than a model being asked to wait.
Ensure its release path cannot hang when no reader starts.

Read the evidence and the expression behind a conclusive flag. A boolean can omit the
very observation that changes its interpretation. Reusing a decoder does not validate
an unobserved envelope key; a wrong key can silently produce an empty result.

When a capture's evidence is an explicit zero, assert the raw key and numeric JSON type
before asserting the decoded zero. Missing, null, wrong-typed, and explicit-zero input
can deliberately collapse to the same downstream zero value, so parser output alone
cannot prove which source shape the fixture retained.

A full-stack proof of a rate-bounded delivery path needs two runs on the identical
drive helper, not one. A below-bound feed producing zero client-visible frames does
not by itself show the feed arrived: a wrong env name, a malformed line, or a drop
before the parser all read as the same zero. Pair it with a feed that crosses the
bound by one unit and must produce exactly one frame carrying that crossing line's
own values. The crossing run is what kills the false-pass causes the zero alone
cannot rule out, and together the pair pins the bound from both sides — a lowered
bound reds the silent run, a raised bound or an off-by-one comparison reds the
crossing run. Compute both feeds' expected total from the fed lines themselves
rather than restating it as a literal, so a feed that drifts off the edge of the
bound fails as that, not as a wrong frame count.

## Test execution and artifact survival

An exit code alone does not prove tests ran. Read named results, counts and skip reasons.
A local authentication failure says nothing about the separately launched live gate.
Use the gate's own results to establish what it exercised. Current role instructions
control whether agents or the dispatcher run each test tier.

Before accepting a baseline comparison as attribution, compare suite composition and
fixture state. Re-running one failing reader on the base without its fixture-producing
sibling is not the same experiment. Check for an existing issue before filing another.

### Diagnosing API response gate failures

An unexpected `ErrUnavailable` in a response-limit or capacity arm establishes
target unavailability, not what initiated it. The historical #3399 log recorded
unavailable errors across overflow, capacity, truncation and the final request,
without probe or target-state evidence establishing the trigger. Baseline race
repetitions passed 360/360 records; passing physical and symlink TMPDIR reruns
therefore cannot assign a TMPDIR or timeout cause. Retain a
[controlled failure-class experiment](apps-package-testing.md#other-package-fixtures)
that separates rejection, header/body I/O and invalidation using exact errors,
reservation/read phases, target validity, requests and retained budget. Injected
invalidation reproduces the error pattern while leaving historical attribution
open; shared fixture registry roots already canonicalize symlinks.

Keep scoped API results separate from aggregate acceptance. The
[final #3399 verifier verdict](https://github.com/pyrycode/pyrycode/pull/3446#issuecomment-6196362331)
records dispatcher cite/docs guards, `make check` and build green at
`a4f94aedb3f5c704427b1ca4b65d8043728159a8`, with cached unit packages and no
per-test counts. The additional uncached, normal-concurrency `make check` at
that head remained **failing/incomplete**: 26984 executed / 26969 passed /
2 failed / 13 skipped test/subtest records. Main baseline `6ab3c311` also
failed: 26978 / 26963 / 2 / 13. Both failures were
`TestRelayAdmissionStorage/setgid_parent` and its parent, with leaf diagnostic
`valid=false, error=<nil>`, tracked in [#3458](https://github.com/pyrycode/pyrycode/issues/3458).
Those observations establish neither actual setgid-parent acceptance nor a
filesystem setup cause. Vet passed, but both uncached attempts stopped in the
race unit tier; staticcheck, substrate-guard and fake-daemon e2e ran only in the
separate dispatcher gate. The subsequent head build passed. No uncached e2e
count or green uncached aggregate is implied by the dispatcher result.

The verdict retains exact commands, supplied/resolved physical TMPDIR,
supported Node/npm selections, counts and thirteen skip reasons. All 24 API
response/diagnostic records passed without skips. Merged #3418's positive
retirement confirmation cleared the earlier retirement finding: all eighteen
`TestPublishRetirement` arms and parent passed (19/19, no skips). Earlier
`health/stop` and `health/intent` failures remain failed historical results;
they are distinct from #3458. The
[two-root rework evidence](../../specs/architecture/3399-api-response-verification.md#rework-scoped-verification-evidence)
at `8f543e1a` records 360/360 response records per root and complete apps suites
of 1029 executed / 1025 passed / 0 failed / 4 filesystem EILSEQ skips per root.
These focused successes do not complete the red aggregate. A separately
interrupted sandbox attempt with listener-bind `operation not permitted` errors
also supplied no completed gate or product attribution.

### Diagnosing startup recovery runtime

A test active when the package times out need not account for the package's
elapsed time. Retain its active interval separately from package runtime, with
phase-start diagnostics and durations for fixture construction, raw paging,
recovery, reopened reads and repeated recovery. Preserve timeout stacks when a
timeout occurs. Later passes establish non-reproduction, not the historical
cause. The [startup comparison](../../specs/architecture/3431-startup-recovery-runtime.md#revisions)
reduced isolated startup time from 28.48 to 5.78 seconds, while complete package
runs took 303.756 and 291.762 seconds. Neither ordinary runs nor four prebuilt
race binaries pinned to one CPU reproduced the historical timeout.

Compare baseline and candidate under identical bounded contention, retaining
each process's settings, revision, command, JSON results and status. Join the
controlled workload before ordinary package and combined-gate measurements;
record any incidental overlap separately. Focused passes cannot certify the
complete gate. Retain named execution/pass/fail/skip counts and skip reasons for
each test tier; aggregate green gates without named results cannot prove that a
particular regression ran.

Fixture cost depends on encoded bytes. JSON escaping can multiply padding size,
and paging decodes whole segments repeatedly. Measure on-disk bytes and assert
raw pagination and physical rollover independently before accepting smaller
padding. See the [startup fixture coverage](history-package-producers-legacy-compatibility.md)
for the independent failure controls and retained recovery assertions.

### Diagnosing reply timeouts

For reset classification, record elapsed post-persistence resolution, first
delivery, rotation entry and completion using durations and counts without
content. The [stale-handoff comparison](../../specs/architecture/3428-stale-handoff-timing.md#revisions)
used identical path assertions in four concurrent race binaries pinned to one
CPU per policy. Both the old 35 ms policy and the repaired policy passed all
600 tests/subtests without skips: the historical failure was not reproduced.
The two-second classification allowance matches the fixture default; 500 ms
leaves over twenty times the largest measured pre-delivery delay while keeping
expiry real, and three seconds bounds reset and rotation. These finite regression bounds
are not product latency guarantees. Retain revisions, commands, phase timings
and execution/pass/fail/skip counts; later passes cannot establish historical
cause or completion of earlier gates.

A fast provider does not imply a fast first reply. Relay `runThreadQuery` completes
`EncodeThreadReply` before serial delivery, so a reply wait includes whole-result
encoding and scheduling under race instrumentation. Measure content-free provider
readiness, first transport entry and final reconstruction before changing a wait or
fixture. `v2Recorder.snapshot` copies routing-envelope headers, not accumulated
ciphertext; `outbound` copies each frame once on capture. Treating polling as repeated
frame copying would target the wrong cost.

Compare old and repaired waits under identical controlled contention, retaining the
escaped/multibyte fixture and complete protocol assertions. The
[large-page reproduction](../../specs/architecture/3364-large-thread-page-reply-wait.md#revisions)
records four race-test processes pinned to one CPU: all four old waits expired before
first transport, while all four repaired runs reconstructed the same reply in about
11.6 seconds, with first transport after four seconds. This establishes insufficient
scheduling headroom in that experiment; unavailable historical phase logs do not
establish a production regression or exact attribution for earlier gate failures.
Use one measured absolute deadline across all fragments and the separate final
coverage marker, checking expiry even with buffered replies. Resetting the budget
per fragment would let partial progress extend the missing-completion wait. The
15-second bound retains headroom over the measured reconstruction; scratch overlays
dropping all replies or only the final marker each failed at that bound. A focused
pass alone proves neither attribution nor bounded failure.

Recovery readiness and peer admission are separate from query completion.
`testDaemonThreadLoad` waits for a usable exact-version query before opening
peers; receiving both handshake responses still needs a bounded `ActiveConns`
snapshot to establish admission. Record recovery, handshake/admission, provider
entry/completion, first transport and complete reconstruction separately, using
durations and counts without content. The
[recovery-wire comparison](../../specs/architecture/3468-recovery-wire-wait.md#matched-contention-evidence)
ran four race binaries with 32 CPU-only contenders pinned to CPU 0. All four
baseline three-second waits expired after recovery and handshakes, before the
first provider returned; all four repaired runs passed with the full escaped
fixture. Recovery preparation took up to 13 seconds. Candidate admission took
0.657–0.765 seconds after wire setup, first provider completion 5.283–5.370
seconds and first transport 12.906–13.138 seconds. Separately timed from reply
collection, the first catch-up frame took 12.249–12.425 seconds and complete
catch-up/page reconstruction took 33.765–35.359 seconds. These measurements
establish scheduling headroom for this controlled experiment. The truncated
historical #3453 evidence cannot identify the cause of its earlier failure.

`testDaemonThreadReply` starts one 60-second absolute budget at reply collection
immediately after request submission, covering provider work, encoding, every
fragment and the separate final coverage marker. This leaves about 70% headroom
over the largest measured complete reply. `testDaemonThreadFrame` checks expiry
before consuming ordered pending or buffered output; the reply helper checks
again after decryption and final reconstruction. Progress never restarts the
budget. Keep shared `switchWire.receive` and handshake waits at their existing
three-second defaults, using `switchWire.decode` for the same authenticated,
nonce-ordered decoding under the query budget. This is a test workload bound,
not a product latency guarantee.

Preserve full rows and parent closure, saved epoch reuse, old-revision first
appearance, page bounds, correlation and legacy history when testing the wait.
`TestDaemonThreadQueriesRecoveryDelayedProvider` holds the first provider beyond
the old frame wait, then requires the identical complete recovered reply with
bounded, joined gate cleanup. The
[missing/late completion controls](../../specs/architecture/3468-recovery-wire-wait.md#focused-checks-and-negative-controls)
retain the fixture: suppressing all transport, withholding final completion
after 16 frames and delaying buffered final completion each failed at about
60 seconds. A larger allowance alone does not establish reliable completion or
finite failure.

Deadline controls must also avoid short wall-clock success assumptions.
`TestDaemonThreadReplyDeadline` originally required its first buffered receive
within 30 ms; contention could consume that budget during setup. A later passing
rerun did not erase the observed failure. Use `testing/synctest` for these local
timer/channel scenarios: consume 20 ms of the original 30 ms budget, receive the
buffered prefix, then require missing output to expire exactly at the original
deadline. Already-expired pending/buffered output must fail immediately. The
[deterministic controls](../../specs/architecture/3468-recovery-wire-wait.md#rework-validation-at-662b97de)
show that a real 60 ms setup pause fails the old test and passes the virtual-clock
test, while restarting the timer fails at virtual 50 ms instead of 30 ms. This
checks the deadline policy independently of OS scheduling.

`TestLiveStateRun` is a hermetic manager test despite its name. Its complete
snapshot needs `pushQueueCap*2+2` routing envelopes: the handshake, one generated
clear and every reading. Four race-test processes pinned to one CPU alone still
passed the old two-second wait; adding 32 CPU-only contenders on that CPU made
all four old waits expire after partial output. Under identical contention the
repaired workload completed all application decryption within 4.382 seconds in
the provisional measurement, establishing the eight-second bound with about 83%
headroom. This measures scheduling headroom, without attributing the historical
491/514 result to a cause its missing phase logs cannot establish. Keep ordinary
`waitForEnvelopes` callers on their existing default.

Start that single absolute budget before manager startup and snapshot-producing
connection admission. Log first, halfway and final transport, plus halfway and
complete decryption, using elapsed time and received/expected and
decrypted/expected counts only. Check expiry before processing buffered frames
and after each decryption, including the last: a timer checked only while idle
would let a buffered workload finish late. Progress must never restart the budget.
The [live-state negative controls](../../specs/architecture/3430-live-state-workload-deadline.md#revisions)
suppressed output after a partial prefix and failed at 8.0003 seconds with
257/514 received and 256/513 decrypted; delaying the final buffered decryption's
completion past the bound failed at 8.0102 seconds even with 514/514 received and
513/513 decrypted. Both executed once, failed once and skipped zero times.
Keep the full expected count and the
[authenticated connection-survival proof](v2-session-manager-test-surface-same-package-unit-tests-internal-relay-delivery-and-control.md#complete-live-state-workload);
reducing the fixture or accepting missing output as a skip would erase the proof.

### Test inputs and retained artifacts

Record the source of the daemon actually executed, not only the checkout's ancestry.
The historical #3071 candidate's `spawnPermissionDaemon` honored
`PYRY_E2E_READINESS_BIN` and `PYRY_E2E_READINESS_MCP_CONFIG`, changing the binary
and MCP fixture independently of the tested tree; the current harness builds
through `ensurePyryBuilt` without those overrides. A merged repair can coexist
with a stale overridden daemon when comparing historical runs;
retain the override inputs, actual binary source, Claude version and exact named
leaf outcomes alongside suite counts. See
[the prompt-answer recovery evidence](../../specs/architecture/3256-shared-live-repair-evidence.md#context).

A temporary worktree is not durable storage. For an agent-produced artifact, commit it.
For a gate-produced capture, retain an external recovery copy and use the documented
promotion step. Staging alone does not survive worktree removal. Require the committed
reader to reject present-but-unpinned evidence. Read the current release runbook before
claiming that a gate automatically commits generated files.
