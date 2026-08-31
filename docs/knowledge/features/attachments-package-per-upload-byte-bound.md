# Per-upload byte bound (#1777)

Two rungs enforce one constant, `maxUploadBytes` (16 MiB, `admission.go`), and
share one sentinel, `ErrUploadTooLarge`: `CheckDeclaredSize` refuses a
declared `size` above the bound on the first chunk, before any bytes are
held; `Add`'s step 5 — last in its fixed check order, after the duplicate
check, immediately before the store — refuses the chunk that would take the
held total past it, routed through the existing `reject` latch. The crossing
chunk's own bytes are the one transient "slack," bounded by the transport's
65519-byte envelope cap and never retained.

- **The declared rung is a sibling of `CheckDeclaration`, not a fold-in** —
  measured, not assumed, same as the admission-layer decisions above.
  `TestCheckDeclaration`'s `math.MaxInt64` row is the only row that pins the
  ceiling arithmetic's exact value; folding a magnitude check into
  `CheckDeclaration` would refuse that pair and destroy the sole pin on the
  wrapping form that silently admits the wire's largest declaration. Two
  sentinels with two different client-visible repairs — re-chunk vs. shrink
  the file — is also a worse contract from one function than from two.
- **`CheckDeclaredSize` admits a negative `size` on purpose.** Magnitude is
  its whole subject; `CheckDeclaration` already owns the negative-`size`
  refusal, and a second owner would make which sentinel a caller sees depend
  on call order. Both checks must run, on the first chunk, before
  `NewAccumulator` — `Registry.Admit`'s obligation (#1788), not either
  function's, since neither can enforce the other's presence: `NewAccumulator`
  stays exported and constructible without passing through `Admit`, so a
  second caller could still run one check or neither.
- **The pairing is what bounds the key space, not either check alone.**
  `CheckDeclaration` bounds `total_chunks` from above, `CheckDeclaredSize`
  bounds `size` from above; together an admitted transfer declares at most
  373 chunks. `CheckDeclaredSize` alone admits a declaration of 2³¹−1 declared
  zero-byte chunks, which no byte bound can refuse, since Σ `len(Data)` stays
  0 regardless of how many chunks are declared.
- **The accumulated rung is subtraction, not a sum**: `len(chunk.Data) >
  maxUploadBytes - a.received`, never `received + len > bound`. The
  invariant `0 <= received <= maxUploadBytes` is what keeps the subtraction's
  right operand non-negative; the addend is a materialised slice length
  rather than a claimed number, so the sum form's wrap isn't reachable in
  practice either, but the safe form is written anyway so a later reader
  doesn't "simplify" it back or read the discipline as cargo cult.
- **Code review correction: the daemon-wide worst-case *peak* is `2N ×
  bound`, not `(N+1) × bound`.** The landed doc's "Retained is not peak"
  paragraph states the worst case as one bound of retained memory across `N`
  concurrent uploads plus a single extra bound for one in-flight `Assemble`
  call — true only if `Assemble` calls across sessions were serialized. They
  are not: `appFrameWorker` (`internal/relay/v2session.go`) documents "no two
  handlers for **the same conn** run concurrently," which is a per-session
  guarantee, not a daemon-wide one — one worker is spawned per session, so
  `N` concurrent sessions can each be inside their own `Assemble` call at the
  same moment. The correct worst-case peak is `N` bounds retained plus up to
  `N` more in flight — 128 MiB at a concurrency of 4, not 80 MiB. The
  *resident* (non-peak) figure the constant's doc states — `bound ×
  concurrency` — is correct and is what `maxInFlightUploads` (#1796) inherits
  as its budget; it is only the peak-during-assembly refinement layered on
  top that undercounted. #1796 landed the correction in the code comment
  itself (128 MiB peak, citing `appFrameWorker` by symbol), closing the gap
  this entry originally flagged between this doc and the code comment.

The entry-count cap landed as `maxInFlightUploads` (#1796); see § "In-flight
upload registry" below for the gate and its shape.
