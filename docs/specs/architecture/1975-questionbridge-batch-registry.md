# #1975 — record a surfaced question batch under a crypto/rand nonce

## Files read

- `internal/modalbridge/modal.go` → `Registry`, `Record`, `Lookup`, `Resolve`,
  `Snapshot`, `newModalID` — the entry-point-for-entry-point analogue: leaf
  mutex, single mint site, one-shot consume, pure-read snapshot.
- `internal/modalbridge/modal_test.go` → `TestRecord_MintsAndStores`,
  `TestRecord_NonceUniqueness`, `TestResolve`,
  `TestSnapshot_PureReadLeavesStateUndisturbed` — the test analogue.
- `internal/questionbridge/questionbridge.go` → package doc, `Parse` — the
  producer of the payload this registry parks; its package doc still names
  `#1927` as the minter and must be corrected here.
- `internal/questionbridge/questionbridge_test.go` → `parseCases`,
  `TestParse_EmitsNoLog` — the no-log pin's shape (handler at
  `slog.LevelDebug` on purpose), and the `#1927` comment this slice corrects.
- `internal/protocol/questions.go` → `QuestionShownPayload`, `Question`,
  `QuestionOption` — the parked shape. `Question.Options` is a nested slice,
  which is why clone-on-read goes one level deeper than the analogue.
- `docs/protocol-mobile.md` § Question (`question_batch_id` row) and
  § `question_dismissed` (`question_batch_id` row) — the published obligation:
  one-time, opaque, unguessable, resolved server-side, `crypto/rand`; a retired
  batch resolves nothing.
- `docs/knowledge/features/questionbridge-package.md` — records that this
  package has no consumer yet and that `#1975` mints the nonce; its lessons
  cover the parse's guard ordering, not this slice.

## Context

`Parse` leaves `ConversationID` and `QuestionBatchID` zero because both are
daemon-asserted, and nothing in the tree mints either or remembers a batch once
surfaced. Three tickets need that store and none owns it: the producer (#1973)
mints and broadcasts, the answer path (#1907) must resolve against the daemon's
own parked copy and must be the *only* dismissal broadcaster for an answered
batch, and the connect-time reconcile (#1928) re-sends current truth without
minting or retiring. The one-shot consume is what makes "exactly one
broadcaster" structural rather than an agreement between two tickets.

No ADR is warranted: this repeats `modalbridge`'s registry decision rather than
taking a new one.

## Design

One new file, `internal/questionbridge/registry.go`, in the existing package.

```go
type Registry struct {
	mu          sync.Mutex
	outstanding map[string]protocol.QuestionShownPayload
}

func New() *Registry
func (r *Registry) Record(p protocol.QuestionShownPayload, convID string) (protocol.QuestionShownPayload, error)
func (r *Registry) Lookup(batchID string) (protocol.QuestionShownPayload, bool)
func (r *Registry) Resolve(batchID string) (protocol.QuestionShownPayload, bool)
func (r *Registry) Snapshot() []protocol.QuestionShownPayload
```

Unexported: `cloneQuestions([]protocol.Question) []protocol.Question` and
`newQuestionBatchID() (string, error)` (`newModalID`'s shape — UUIDv4 from
`crypto/rand`, error only on RNG failure).

**No stored-entry type.** The parked thing *is* the stamped payload, so the map
value is `protocol.QuestionShownPayload` itself; a second type would buy a
field-by-field copy in and a field-by-field rebuild out and nothing else. This
is the one place the analogue is deliberately not followed.

`Record` is the single mint site. It mints, stamps `QuestionBatchID` and
`ConversationID` onto the payload, and writes the entry inside the same critical
section, so a stored batch is never snapshot-able without its scope key. Both
ids are overwritten unconditionally — an id already on the input is never
trusted, since the input came from claude's tool call. On a mint failure it
stores nothing and returns the zero payload plus a wrapped error; the caller
drops the batch rather than broadcasting an id-less payload.

**Clone-on-read nests one level deeper than the analogue.** `slices.Clone` over
`[]protocol.Question` copies the question structs but leaves every
`Question.Options` pointing at the stored backing array, so `cloneQuestions`
clones the outer slice *and* each element's `Options`. Applied at:

- `Record` — clone-on-write into the map, so what it returns (the caller's own
  slice, stamped) shares nothing with the kept copy;
- `Lookup` and `Snapshot` — clone-on-read;
- `Resolve` — **no clone, deliberately**: the entry is deleted in the same
  critical section, so there is no kept copy left to isolate the return from.

`Snapshot` is a pure read: it mints nothing, retires nothing, mutates nothing,
and re-emits each batch's own nonce and conversation id. Map-walk order is
unspecified; callers reconcile by `question_batch_id`, never by position. An
empty registry yields a non-nil, zero-length slice.

Nothing calls this yet, deliberately — as #1965 / #1963 / #1962 landed.

No bound is re-decided: `maxInputBytes` stays the only length bound in the
package (a second cap in a second place could disagree with it silently).

## Concurrency model

No goroutines are spawned. `mu` is a leaf lock held only around O(1) map ops and
the clone, never nested with any other lock; two real goroutines touch the
registry (the surfacer that records, and the relay dispatch that
looks up / resolves), so it is not confined by convention. `Resolve`'s
read-and-delete is a single critical section, which is what makes the one-shot
consume race-free: exactly one caller of concurrent `Resolve`s sees `true`.

## Error handling

One error branch: RNG failure in `newQuestionBatchID`, wrapped
`fmt.Errorf("read random: %w", err)`. Mirrors the analogue's and stays
unexercised — `crypto/rand.Read` does not fail on a supported platform, and no
injectable RNG seam is minted to make the branch testable. A missing batch is
`ok == false`, not an error. The package stays log-free.

## Testing strategy

`internal/questionbridge/registry_test.go`, table-driven where the rows differ
only in data:

- Record mints, stamps both ids and stores: the returned payload carries a
  fresh nonce and the caller's conversation id, and `Lookup` by that nonce
  returns an equal payload.
- Two records of identical content get different nonces, and both stay
  outstanding.
- `Lookup` does not consume: repeated lookups keep returning the batch, and a
  later `Resolve` still succeeds.
- One-shot consume: the first `Resolve` returns the batch, the second reports
  absent, and afterwards `Lookup` is absent and `Snapshot` omits it.
- `Snapshot` re-emits nonce and conversation id per batch, keyed by id across
  two batches under different conversations; empty registry → non-nil,
  zero-length; pure read (everything still resolvable afterwards).
- Isolation at **either nesting level**, over all three hand-out sites (Record's
  return, `Lookup`, `Snapshot`): mutating a returned question, and mutating a
  returned question's nested `Options` element, leaves the parked batch
  unchanged when read back.
- No-log pin over the whole registry surface, mirroring `TestParse_EmitsNoLog`
  including its `slog.LevelDebug` handler — at the default level an empty buffer
  would prove nothing about a Debug line.

Gate: `go test -race ./internal/questionbridge/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Open questions

- How much of the UUIDv4 shape the mint test should pin (non-empty + uniqueness,
  versus the full 36-char dashed shape). Resolve against
  `TestRecord_MintsAndStores` when writing the test; record the choice under
  `## Revisions` only if it changes the design.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The boundary is `Parse`'s, upstream of this
  slice: what `Record` accepts is already-parsed, already-bounded
  `protocol.QuestionShownPayload`, and the registry re-decides no bound.
  `Record` asserts the two daemon-owned fields itself and overwrites whatever
  arrived in them, so no claude-authored byte can become a routing key. The
  claude-authored strings stay untrusted text; `Question`'s SECURITY paragraph
  places the sanitization duty on the client render surface, and nothing here
  moves it.
- [Tokens] No findings on generation, one design decision recorded. The nonce is
  `crypto/rand` (122 bits, `newModalID`'s shape) — `math/rand` would make the
  server-side resolution forgeable, which is the whole property
  `docs/protocol-mobile.md`'s `question_batch_id` row publishes. It is not a
  credential: it correlates a batch and, per § `question_dismissed`'s row, a
  retired batch resolves nothing server-side. Lifecycle: created in `Record`,
  held in memory only (never written to disk), never rotated, revoked by
  `Resolve`'s delete. **No expiry, and that is this slice's deliberate
  omission** — a batch outstanding forever is an unbounded map keyed by a
  never-consumed nonce. OUT OF SCOPE, owned by #1973: its terminal no-answer
  paths (caller disconnect, daemon shutdown, timeout) are the retire backstop
  that calls `Resolve`, and a TTL invented here would be a second retire
  authority disagreeing with that one.
- [Tokens / logging] No findings. The package imports no logger, structurally,
  and this slice keeps it that way — so neither the nonce nor a question string
  can reach a log from here. The one error value wraps `crypto/rand`'s own error
  and carries no payload byte and no id.
- [File operations] N/A by design — the registry is in-memory only; no path is
  built, opened, or written.
- [Subprocess execution] N/A by design — no `exec` surface; the input is a
  decoded struct handed over by the caller.
- [Cryptographic primitives] No findings. `crypto/rand.Read` only; no
  hand-rolled primitive, no key material, no reuse (each `Record` mints its own
  nonce and the map key is that nonce). No constant-time comparison is owed:
  lookup is a map hit on an opaque correlation id, not a secret comparison, and
  the analogue's `Lookup`/`Resolve` are the precedent. Timing tells an attacker
  only present-versus-absent, which an unguessable 122-bit key makes
  unreachable without already holding it.
- [Network & I/O] N/A at this layer — no socket is read here. The relevant cap
  is `maxInputBytes`, taken pre-decode by `Parse`; this slice deliberately adds
  none, since a second cap could disagree with it silently. Unbounded *growth*
  of the map is the real I/O-shaped risk and is the expiry finding above.
- [Error messages] No findings. One error, wrapping the RNG failure; a
  not-found is `ok == false`, so no id is echoed back in an error string.
- [Concurrency] No findings. One leaf mutex, never nested, so no lock order to
  document. `Resolve`'s check-then-delete is inside one critical section, which
  is what makes the one-shot guarantee hold under concurrent callers — the
  TOCTOU shape this category asks about is exactly what a read-then-delete
  outside the lock would introduce. No goroutine is spawned, so none can leak.
  Clone-on-read closes the other shared-state hazard: a consumer mutating a
  handed-out batch cannot reach the parked copy at either nesting level.
- [Threat model alignment] § Security model's threat 1 (claude-authored text
  reaching a remote render surface) lands on the batch this registry parks, and
  is unchanged by parking it: no field is re-bounded, re-sanitized, or newly
  exposed here. No inbound capability is granted — this slice declares no verb
  and nothing calls it yet.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-01
