# #1979 — Connect-time question-batch reconcile (relay half)

`security-sensitive`. The fourth Mode B instance of reconcile-on-connect, after
`reconcileModals` (#877), `reconcileQueues` (#878) and `reconcileModelLists` (#1863).
This slice is the relay seam plus the reconcile; the daemon-side producer wiring and
the client contract are #1980.

## Files read

- `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists` — the shape this
  slice mirrors whole: guard, snapshot-once, per-payload marshal/Push, the two reject
  branches and their logging discipline.
- `internal/relay/v2session_seams.go` → `RetainedModelLists`, `OutstandingModals`,
  `OutstandingQueues` — the three existing Mode B seam declarations. Their shared
  contract (closure-not-pointer, nil ⇒ no reconcile, enumerate-all) is the one this
  seam joins.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — the success tail. Where
  `s.interactive` is stamped, where the push queue is created, and the three existing
  reconcile calls in order before the `replayMissed` hook.
- `internal/relay/v2session_modelreconcile_test.go` → `sampleModelListPayload`,
  `reconciledModelLists`, and its four tests — the test pattern, including its recorded
  decision not to test the unreachable marshal branch.
- `internal/protocol/questions.go` → `QuestionShownPayload`, `Question`,
  `QuestionOption`, and both `MarshalJSON` methods — the payload's field set, its
  no-omitempty discipline, and the SECURITY paragraph naming the four claude-authored
  strings.
- `internal/protocol/codes.go` → `TypeQuestionShown` — the envelope type this path emits.
- `internal/questionbridge/registry.go` → `Snapshot`, `cloneQuestions` — #1980's
  production source for this seam. Read to confirm two things the design leans on: it is
  a pure read, and its clone reaches **both** nesting levels (`slices.Clone` on the
  question slice, then `slices.Clone` on each question's `Options`), so a reconciled
  payload shares no backing array with live registry state.
- `internal/relay/v2session_test.go` → `decryptAppFrame`, `waitForEnvelopes`,
  `silentLogger` — harness entry points the tests ride.
- `internal/relay/v2session_modal_test.go` → `openModalConn`, `noiseMsgsForConn` — the
  per-conn handshake driver and frame filter.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md`
  — #1863's lessons. Three change how this ticket is built and are carried into
  § Testing strategy: the `!s.interactive` mutant needs a **second interactive conn** to
  be sole-red; the `len(...) == 0` check is an **equivalent mutant**, not missing
  coverage; and staticcheck's U1000 is why the seam and the call site cannot be split
  apart into separate tickets.
- `docs/knowledge/features/questionbridge-package.md`, `.../protocol-package-question-batch-payload.md`
  — the batch family's trust tier and the `question_batch_id`-as-correlation-key rule.

## Context

The raise-time broadcast (#1973) reaches only whoever is connected at the instant claude
asks. `question_shown` carries no event id, so it is not in the #647 turn-event replay
ring, and there is no other path to it — a client that connects afterwards never learns
the ask exists. `reconcileModals`' doc block records the twist that makes the miss cost
more than a plain miss: the daemon counts an approval answerable while **any** interactive
conn is open, so a reconnected client that was never sent the batch re-arms the window at
every expiry while being structurally unable to answer it.

`docs/protocol-mobile.md` § Reconnect / Backfill semantics binds the mechanism to the data
class, not to how long the client was away: transcript content takes a cursor backfill,
control state takes a connect-time snapshot. An outstanding question batch is control
state, so this is Mode B, and the design is the family's — deliberately not a new one.

**Sizing.** This lands over the 400-line size-S bound (~500 lines of written work plus
this plan) and no split is available, for two independent reasons the refiner recorded and
this run re-verified. (1) The depth gate: `gh api graphql` on the parent chain returns
`parent 1928 grandparent 1906`, so #1979 is already a grandchild and the gate closes at
two. (2) The floor: `cmd/pyry` wiring is already cut away into #1980, and cutting again
leaves either a seam consumed by exactly one sibling or a reconcile no production path
calls — and the second is not merely a floor violation but a build failure, since
staticcheck's U1000 rejects an uncalled unexported method (the same constraint that
capped #1863's split). `needs-human:sizing` is already on the ticket. Per § A1's
depth-capped path this run builds rather than stops. The other five boundaries hold: 3
production files, 0 new exported types, 1 consumer call site, 4 acceptance criteria, 2
reject branches.

**No ADR.** This adds a fourth instance of a mechanism whose decision record already
exists; nothing here re-decides anything.

## Design

Three production files, mirroring #1863's three exactly.

### 1. The seam — `OutstandingQuestions` on `V2SessionConfig` (`v2session_seams.go`)

```go
// OutstandingQuestions enumerates the daemon's currently-outstanding question
// batches as marshal-ready question_shown payloads for connect-time reconcile.
OutstandingQuestions func() []protocol.QuestionShownPayload
```

A closure over `[]protocol.QuestionShownPayload`, **not** a `*questionbridge.Registry`:
`internal/relay` imports neither `internal/questionbridge` nor `internal/modalbridge`, and
`internal/protocol` is already a direct import, so the payload crosses the boundary with
no new import and no cycle. Same idiom as the three siblings.

Optional — nil ⇒ no reconcile, which is what keeps every foreground / v1 / unwired /
existing-test wiring byte-identical. Production wires `questionbridge.Registry.Snapshot`
in #1980; that method's doc block already names this work as its consumer.

**Enumerate-all, not conversation-keyed.** A `V2Session` carries `connID`, `state`,
`resp`, `send`, `recv`, `device`, `interactive` and `peerStatic` — no conversation id — so
there is no "this conn's conversation" to key on at connect time. `RetainedModelLists`'
doc block states the same reasoning; `RunConfigFor` is the conversation-keyed variant and
is the wrong shape here.

### 2. The reconcile — `reconcileQuestions` (`v2session_questionreconcile.go`, new)

```go
func (m *V2SessionManager) reconcileQuestions(ctx context.Context, s *V2Session)
```

Structural twin of `reconcileModelLists` with `protocol.TypeQuestionShown` /
`QuestionShownPayload` substituted. Its own file for the reason `v2session_modelreconcile.go`
gives for its own: the test files are already one-per-reconcile, and a question reconcile
living in a modal-named file is a readability hazard.

Behaviour, in order:

1. **Guard** — `!s.interactive || m.cfg.OutstandingQuestions == nil` ⇒ return. The
   capability gate plus the unwired/foreground opt-out, identical in shape to all three
   siblings.
2. **Snapshot once**; a zero-length result ⇒ return, nothing sent.
3. **One `ts`** shared by the batch, matching the three siblings.
4. **Per payload** — marshal, then `m.Push` a `protocol.Envelope{ID: 1, Type:
   protocol.TypeQuestionShown, TS: ts, Payload: payload}` with `EventID` left nil.

`ID: 1` is non-load-bearing; the client correlates on `question_batch_id`. `EventID` nil
is load-bearing twice, as in the siblings: it keeps the frame out of the #647/#777
turn-event replay ring, and it makes `forwardEnvelope`'s `last_event_id` dedup inert for
the frame. No turn is opened — the envelope goes straight onto the conn's push queue via
`Push`, never through `forwardEnvelope`, and `TypeQuestionShown` is not a turn-boundary
type.

**No bound of its own.** `questionbridge.Parse` already bounds a batch at 1–4 questions
and 2–4 options per question, and `maxInputBytes` bounds the tool input it parses. A
second bound here would be a second place the limit is decided and the two could disagree
silently, so the obligation stays the producer's. This is the same discharge
`RetainedModelLists`' SECURITY paragraph makes.

**Call ordering — a decision, not an accident.** Correctness does not depend on where in
the tail the call lands: the four reconciles carry distinct payload types and none reads
another's effect. It is placed **beside the modal reconcile, before the queue reconcile**,
because an outstanding batch rides the daemon's approval window — the same
time-sensitivity that put the modal reconcile first and the model-list reconcile last.

### 3. The call site — `handleNoiseInit`'s success tail (`v2session_handshake.go`)

One line, `m.reconcileQuestions(ctx, s)`, immediately after `m.reconcileModals(ctx, s)`,
with a comment stating the ordering rationale above. Still before the `replayMissed` hook.

## Concurrency model

No new goroutine and no new lock. `reconcileQuestions` runs on the manager's Run
goroutine only — called from `handleNoiseInit`, which Run drives — so `s.interactive` and
`s.connID` are read lock-free under the package's single-owner invariant. That guarantee
is the **caller's**, not this function's, and the doc comment restates it explicitly
rather than leaving it implied, per the family precedent.

`m.Push` takes `pushMu` internally and is the only lock this path acquires directly. The
seam's closure will take `questionbridge.Registry`'s own mutex in #1980; that is a bounded
map walk with no I/O under a plain `sync.Mutex`, so the Run-goroutine hold is short and no
lock is held across it from this side — there is no ordering hazard to document because
this path holds nothing when it calls the seam.

The reconcile enqueues **unsealed**: `Push` appends to `pushQueue`, and `drainOnce`
consults `Connected` before sealing. A conn whose transport leg is down therefore burns no
Noise send-nonce for these frames — the #874 invariant this path inherits unchanged and
must not break.

## Error handling

Two reject branches, both non-fatal — skip the payload, keep sending the rest:

- **Marshal failure** (`Warn`, `event=v2.question.reconcile.marshal_err`) — carries
  `conn_id`, `question_batch_id` and `conversation_id` and **never** `err`, because
  `encoding/json` quotes the offending input bytes into its message and a batch's text
  would land in the record. Unreachable in practice: `QuestionShownPayload` is two strings
  plus `[]Question`, `Question` is two strings plus `[]QuestionOption` plus a bool,
  `QuestionOption` is two strings, and both custom `MarshalJSON`s delegate to
  `json.Marshal` over those closed types.
- **Push failure** (`Debug`, `event=v2.question.reconcile.push_err`) — may echo `err`,
  and only because `Push` returns `ctx.Err()` or `ErrConnNotFound` and nothing else. That
  is an inherited contract, named in the comment so a later change to `Push`'s error
  values is visibly load-bearing. `ErrConnNotFound` is unreachable here (the push queue
  was created a few statements earlier on this same goroutine), which is why this is
  `Debug` rather than `Warn`. `ctx.Err() != nil` ⇒ return early: the session is going away
  and the remaining payloads have nowhere to land.

The success path logs nothing at all — a connect-time reconcile fires on every handshake,
the routine-read cadence `handleRequestSessionSettings` cites when it logs `conn_id` and
nothing else.

## Testing strategy

New file `internal/relay/v2session_questionreconcile_test.go`, riding the existing
in-package harness (`v2Recorder`, `openModalConn`, `noiseMsgsForConn`, `decryptAppFrame`,
`waitForEnvelopes`, `waitConnOpen`, `lockedBuffer`). Table-driven, not
one-function-per-arm — the house idiom, and #1863's deliberate divergence from #878's
file layout for the same coverage in materially fewer lines.

Two helpers:

- `sampleQuestionPayload(convID, batchID string, ...)` — a fully-populated payload whose
  every field is distinct and non-zero, with `MultiSelect` true on one question only so
  the bool is carried rather than defaulted, and ≥2 options per question so the nested
  level is non-trivial. `QuestionShownPayload` has no `time.Time` field, so
  `reflect.DeepEqual` is safe and #878's `equalQueued` has no counterpart.
- `reconciledQuestions(...)` — decrypts every `noise_msg` for a conn and returns the
  payloads **keyed by `question_batch_id`**, never by position (`Snapshot` walks a map and
  its doc states the order is unspecified; a position-coupled assertion becomes a flake the
  moment #1980 wires the real read). It fails on any non-`question_shown` type — that is
  where "no turn opened" is asserted — on a non-nil `EventID`, and on a repeated batch id.

Scenarios:

- **Delivery (AC1)** — rows for one batch, two batches in the same conversation (which a
  conversation-keyed map would silently collapse), and two batches across two
  conversations. Each row `reflect.DeepEqual`s against the seam's own payloads, covering
  `question_batch_id`, `conversation_id` and both nesting levels unchanged.
- **Unicast + idempotence (AC2, AC3)** — with A open, opening B delivers to B only; A
  receives no second frame. B's decoded set is then compared to A's, which is the "run it
  twice over the same outstanding set, same frames both times" half of AC3; and the seam's
  backing payloads are compared against a pristine copy taken before either open, which is
  the "changes nothing it read" half.
- **No frame (AC2)** — three rows: nil seam, zero-payload seam, and capability not
  negotiated. The third **needs a second, genuinely-interactive control conn opened after
  the conn under test**, or A's zero is indistinguishable from an empty seam and the
  dropped-`!s.interactive` mutant survives green (#1863's recorded finding). Each row also
  asserts the session is still enumerable-open — no frame **and** no error.
- **Content-free logging (AC4)** — four distinct sentinels, one per claude-authored
  string (`Text`, `Header`, `Label`, `Description`), captured at `slog.LevelDebug` so any
  leak at any level surfaces, with the handshake-accept line proving the capture is live.
  Four rather than one because they are separate struct members and a branch echoing only
  one must not pass.

**Not tested, by decision:** the defensive marshal branch — no value of a
`QuestionShownPayload` can fail to marshal (see § Error handling), so no fixture reddens
it. None of the three sibling test files carries a marshal test either. The file header
states this so the gap does not read as an oversight.

**Also not a coverage gap:** the `len(...) == 0` check has no sole-red mutant — with a
zero-length slice the loop body never executes, so deleting the check is an *equivalent*
mutant. The "zero payloads" row pins the contract (a real producer returning `nil` must not
error), not a reachable branch.

## Open questions

1. **Log `question_batch_id` on the marshal branch, or only `conversation_id`?** The batch
   id is an unguessable one-time nonce, which reads as "secret" — but `reconcileModals`
   logs `modal_id`, an identically-minted nonce with the identical answer-capability role.
   Resolved in § Security review (finding 2): log both. Every paired interactive conn
   already receives every batch id by design via #1973's broadcast, so there is no
   confinement for a log line to break, and a marshal failure that named neither id would
   be unpinnable.
2. **Does `Snapshot`'s clone reach the nested `Options`?** AC1 requires both nesting levels
   unchanged and AC3 requires the path change nothing it read. Resolved during § Files
   read: `cloneQuestions` clones the question slice and then each question's `Options`, so
   isolation holds at both levels. This slice's own seam is a fake, so the tests assert the
   property against the fake's backing payloads rather than relying on it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — this path is a *carrier*, not a boundary. The four
  claude-authored strings crossed the subprocess boundary upstream at
  `questionbridge.Parse`; `reconcileQuestions` re-emits already-parsed, already-bounded
  values onto the AEAD-sealed push path. Concretely it reads only `s.interactive`,
  `s.connID` and `m.cfg.OutstandingQuestions` — **no field of the phone's `hello` payload**,
  in deliberate contrast to `replayMissed`, the one thing in the same tail that does take
  untrusted remote input (`helloPayload.LastEventID`). This slice declares no inbound verb,
  so nothing from the wire reaches it. The payloads are forwarded **unsanitised** — no
  control-character or terminal-escape stripping — which is `Question`'s documented
  decision, not an omission here: the render boundary owing sanitisation is the client's,
  and a silent transform on this path would present altered text as claude's own.
- **[Tokens, secrets, credentials]** No findings, and the reasoning is recorded because
  "unguessable nonce" invites the opposite conclusion. `QuestionBatchID` is minted by
  `questionbridge.Record` via `crypto/rand`; this path mints none, rotates none and
  retires none. Re-sending a batch id to a reconnecting conn **does** hand that conn the
  capability #1907's inbound answer will be resolved against — but the grant is gated
  exactly as the original broadcast was: the conn is post-Noise_IK-handshake with its peer
  static pinned, and post-token-validation, i.e. a paired device, and #1973 already
  broadcasts every batch to every such conn. There is no confinement to widen. The same
  argument is what makes logging the id safe (Open question 1), matching
  `reconcileModals`' `modal_id`.
- **[File operations]** No findings — the design forms no path and touches no file. The
  seam is a closure over in-memory registry state and the only sink is `m.Push` onto an
  in-memory queue, so path traversal, TOCTOU, file modes, symlinks and atomic writes have
  no surface to apply to.
- **[Subprocess / external command execution]** No findings — no `exec.Command`, and this
  path routes **no keystroke** into the supervised claude. The contrast is load-bearing:
  `ModalResolver`'s methods do route keystrokes and are security-sensitive for it, whereas
  `OutstandingQuestions` is a read seam whose closure the reconcile calls once. Nothing
  here can reach the child process.
- **[Cryptographic primitives]** No findings — this code constructs a plaintext
  `protocol.Envelope` and hands it to `Push`; it touches no key, no nonce and no AEAD
  directly. The family's one real crypto footgun is nonce burn (#874: a burned send-nonce
  gaps the phone's recv nonce into a 4421 close), and this slice adds frames to the queue
  that footgun lives on. It is discharged by construction rather than by care:
  `Push` enqueues unsealed and `drainOnce` consults `Connected` before sealing, so frames
  enqueued to a transport-down conn burn nothing.
- **[Network & I/O]** SHOULD FIX, and it is the producer's, not this slice's. Per-batch
  size is bounded upstream (`questionbridge.Parse`: 1–4 questions, 2–4 options,
  `maxInputBytes` on the tool input), but the registry's `outstanding` map carries **no
  cardinality cap** — nothing in `questionbridge` bounds how many batches can be
  outstanding at once, so `Snapshot`'s length is bounded only by claude's own ask
  concurrency and by #1973 retiring a batch on every terminal path. This path must
  nonetheless add no cap of its own (a second place deciding the limit, silently
  disagreeing with the first), and the backstop on *this* path is real: `pushQueue`'s byte
  ceiling drops rather than growing without bound. Phase B action is a sentence in the
  seam's doc comment naming the aggregate bound as the producer's obligation, exactly as
  `RetainedModelLists`' SECURITY paragraph does; a registry-side cap belongs to
  `questionbridge`, not to #1979 or #1980.
- **[Error messages, logs, telemetry]** No findings — AC4 is the requirement and
  § Error handling is the discharge. The marshal branch's refusal to echo `err` is the
  substantive control (json quotes input bytes, so `err` alone would leak a question's
  text); the push branch's permission to echo `err` rests on `Push`'s narrow error set and
  says so. The success path emits nothing, so the routine per-handshake cadence adds no
  record at all. No telemetry or metric is added.
- **[Concurrency]** No findings — no new goroutine (so no lifecycle or leak to analyse)
  and no new lock, hence no ordering to document: this path holds no lock when it calls the
  seam. The lock-free reads of `s.interactive` / `s.connID` are safe only under the Run
  goroutine's single-owner invariant, which is the caller's guarantee and is restated in
  the function's own doc comment rather than left implied. There is no check-then-mutate:
  the path mutates nothing. Mid-shutdown is handled — `ctx.Err() != nil` returns early, and
  an interrupted reconcile leaves no partial state because each frame is independent and
  the registry is untouched.
- **[Threat model alignment]** No findings — `docs/protocol-mobile.md` § Security model's
  two relevant gates are both present and are the family's: authentication (the reconcile
  runs only in `handleNoiseInit`'s success tail, i.e. post-handshake and
  post-token-validation) and capability (`!s.interactive`). § Reconnect / Backfill
  semantics is satisfied in the affirmative rather than merely not violated: control state
  takes a connect-time snapshot, which is what this is. Out of scope and named: the
  inbound answer verb and its server-side resolution against `question_batch_id` are
  #1907's; the daemon-side producer wiring is #1980's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-01
