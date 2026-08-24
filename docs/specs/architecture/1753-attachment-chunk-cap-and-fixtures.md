# #1753 — Publish the per-chunk attachment size cap and pin the frame with both-direction fixtures

Ticket: https://github.com/pyrycode/pyrycode/issues/1753 · Size: **S** · Blocker: #1752 (landed)

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

| Where | Symbol / section | What to extract |
|---|---|---|
| `internal/protocol/attachments.go` | `AttachmentChunkPayload` | The eight field contracts, and the **three obligations its doc comment writes onto this ticket**: the per-chunk bound is "#1753's, not a constant here"; "#1753's fixtures pin the full shape"; `TotalChunks == ceil(Size / bound)` where the bound counts **raw bytes**. |
| `internal/protocol/interactive_test.go` | `maxV2AppEnvelope` | The 65519 test-local constant to measure against, and the comment explaining why it is *not* exported. That reasoning does **not** carry over to this ticket's bound — see § Design. |
| `internal/protocol/interactive_test.go` | `TestToolUsePayload_FitV2EnvelopeCap` | The worst-case-fill shape this ticket's cap proof copies: `<` fill, hostile envelope ids, `t.Logf` of the measured size, one `>=` assertion. Its header paragraph is also the model for saying out loud what a hostile fill on an unbounded field does and does not prove. |
| `internal/protocol/interactive_test.go` | `TestModelListPayload_ZeroValue_RoundTrip` | The `bytes.Contains(canonical(t, raw), …)` byte-guard pattern — the only assertion shape that survives fixture regeneration. Also the "a frame no producer will ever emit; it exists for the encoding, not the scenario" sentence. |
| `internal/protocol/interactive_test.go` | `roundTripEnvelope` | The canonical byte-equality helper every round-trip test ends on. In-package — call it directly. |
| `internal/protocol/envelope_test.go` | `readFixture`, `canonical` | Fixture read + JSON compaction. Both in-package. |
| `internal/protocol/envelope.go` | `Envelope` | The seven wire keys and which three are `omitempty` — this is the wrapper whose worst case the cap arithmetic budgets for. |
| `internal/relay/v2bundlestream.go` | `bundleChunkBytes` | The **form** to copy for the constant's comment: the arithmetic spelled out, the test named as the enforcer, and "if that ever fails, LOWER this constant — never raise it". Copy the form, **not the value** — it measures sealed ciphertext against 65535, this ticket measures a marshalled envelope against 65519. |
| `internal/protocol/testdata/` | `conversations.json`, `ack.json` | The house shape for a **daemon → client response** frame: `in_reply_to` present. `send_message.json` is the client → daemon counterpart with no `in_reply_to`. This is what makes the two directional fixtures structurally different rather than merely differently-valued. |
| `internal/protocol/testdata/` | `debug_bundle_chunk.json` | The house shape for a fixture whose payload carries a `[]byte` field. |
| `internal/conversations/id.go` | `ValidID` | The canonical-shape precedent `AttachmentChunkPayload`'s doc names for `AttachmentID`: exactly 36 chars, UUIDv4 form. Anchors `MaxAttachmentIDBytes`. |
| `docs/knowledge/features/protocol-package.md` | § "Slash-command-list payload (#1727 shape, #1718 fixtures…)" | **Four traps recorded from the last fixture ticket, all of which apply here verbatim:** the `go test -overlay` generator recipe, the sole-redness claim trap, `readFixture`-once-outside-the-loop, and the zsh-vs-bash heredoc trap. Read all four bullets. |
| `docs/knowledge/features/protocol-package.md` | § "Model-list payload (#1704 shape, #1705 fixtures…)" | The populated-vs-zero fixture redundancy lesson: a populated fixture only earns distinct coverage on a key where one of its rows lands on that key's zero value. |
| `docs/protocol-mobile.md` | § Application-envelope size cap | Where 65519 comes from (65535 Noise transport message − 16-byte AEAD tag). |
| `docs/protocol-mobile.md` | § `tool_result` | The six-bytes-per-escaped-byte arithmetic already worked out for a bounded text field, with a measured worst case. The escape ceiling this ticket's arithmetic reuses. |

## Context

#1752 shipped `AttachmentChunkPayload` as wire vocabulary — eight fields, no producer, no consumer, no validator. It also wrote three obligations into its doc comment that are the specification for this slice: publish the per-chunk raw-byte bound, pin the full shape with fixtures in both directions, and make `TotalChunks == ceil(Size / bound)` a checkable relationship for #1741's allocation guard.

Two things follow that are not obvious from the ticket title.

**The bound is unprovable without bounding the metadata.** Three of the seven metadata fields (`attachment_id`, `filename`, `mime_type`) have no declared length anywhere in the tree, and `sha256`'s "always 64 hex characters" is prose, not enforcement. `encoding/json` escapes HTML by default, so one hostile input byte costs six on the wire. A proof that fills `data` to the cap beside a short ASCII filename measures the friendly case and says nothing about the hostile one. This slice therefore declares byte bounds for the three unbounded strings — the ticket's Technical Notes option 1, chosen over option 2 for the reason the ticket itself gives: on the inbound leg **every** field of this frame is attacker-chosen, so an "assumption" about a field's length is a hole rather than an assumption.

**The cap it publishes is this package's first exported byte bound, and that is deliberate.** `maxV2AppEnvelope` is test-local because nothing in `internal/protocol` enforces the envelope cap — exporting it would imply an enforcement that lives in the transport. That reasoning does not extend here: a per-chunk bound is a **producer-side contract every client must obey to chunk a file at all**, and it is the number #1741's allocation guard cross-checks `TotalChunks` against. It has to be readable from outside the package.

**ADR?** No. This is a bound plus fixtures, not a decision with alternatives worth preserving. The precedent worth recording — *why one exported byte bound sits beside a test-local one in the same package* — belongs as a paragraph in `docs/knowledge/features/protocol-package.md`, which the documentation phase owns. Do not write it here.

**Out of scope, explicitly:** no producer, no consumer, no validator, no daemon behaviour, no dispatch, no storage, and **no `docs/protocol-mobile.md` edit** — publishing the number in client-facing prose is #1751's job.

## Design

### One production change: an exported bounds block in `attachments.go`

Four constants in one `const` block, placed above `AttachmentChunkPayload`. Contract only — the values and what each comment must record:

```go
const (
	// Maximum raw, pre-base64 bytes of one chunk's Data.
	MaxAttachmentChunkBytes = 45000

	// Byte ceilings for the three variable-length metadata strings.
	MaxAttachmentIDBytes       = 64
	MaxAttachmentFilenameBytes = 255
	MaxAttachmentMimeTypeBytes = 255
)
```

**Units, stated first and unambiguously.** `MaxAttachmentChunkBytes` counts **raw bytes of `Data`** — not base64 characters, not payload bytes, not envelope bytes. `AttachmentChunkPayload`'s doc comment already commits #1741 to `TotalChunks == ceil(Size / bound)` with `Size` a raw file length, so any other unit silently breaks that guard.

The three metadata bounds are **byte** bounds (`len(s)`), not rune bounds. Two reasons, both worth a line in the comment: the escape ceiling composes directly on bytes (a one-byte input costs at most six on the wire; a multi-byte rune is emitted raw at four bytes or fewer, so six-per-input-byte is the ceiling either way), and a filename's real-world limit is a byte limit.

**What each comment must record.** Follow `bundleChunkBytes`'s form.

- `MaxAttachmentChunkBytes`: the arithmetic table below, in prose; the name of the test that enforces it; and the direction — *if `TestAttachmentChunkPayload_FitV2EnvelopeCap` ever fails, LOWER this constant, never raise it.* Also: why it is exported when `maxV2AppEnvelope` is not (§ Context), and the `ceil(Size / bound)` relationship as § The cross-check states it — **not** as #1752's one-liner states it.
- `MaxAttachmentIDBytes = 64`: a **ceiling for the cap arithmetic, not a canonical shape.** `conversations.ValidID`'s 36-char UUIDv4 form is the precedent `AttachmentChunkPayload`'s doc names, and 64 sits comfortably above it and above a 64-hex token. #1741 / #1743 may narrow the shape below this without touching the constant; they must not exceed it. **A length ceiling is not a safety property**: 64 bytes accommodates `../../../../etc/passwd` several times over, so this constant does nothing about the traversal hazard `AttachmentChunkPayload`'s doc assigns to the canonical-shape check. Say that in the comment — a reader who sees a bound where a traversal was expected is the reader this ticket can mislead.
- `MaxAttachmentFilenameBytes = 255`: POSIX `NAME_MAX`, the single-path-component limit on ext4 and APFS. The field is documented as "never a path", so one component is the right ceiling.
- `MaxAttachmentMimeTypeBytes = 255`: RFC 6838 §4.2 bounds type and subtree names at 127 characters each, so `type/subtype` is at most 255.

**State who enforces.** All four are published contracts with no validator in this package — the same posture #1752 shipped. Inbound enforcement is #1741's; outbound is #1744 / #1746's. Say so, so the next reader does not mistake a declared bound for a checked one.

**The outbound leg is why the metadata bounds matter, and it is worth one sentence in the block comment.** A filename arrives attacker-chosen on the upload leg, gets stored, and is echoed back daemon-authored on the retrieval leg. Unbounded, an attacker's 100 KB filename makes the *daemon's own* outbound frame exceed the envelope cap and be dropped. That is the hazard the bounds close, and it is not visible from the inbound leg alone.

### The cap arithmetic

Budget against 65519 (`docs/protocol-mobile.md` § Application-envelope size cap). Every metadata figure is `bound × 6`, the JSON escape ceiling.

| Component | Worst case (B) | Where the number comes from |
|---|---|---|
| Envelope wrapper, every optional key present | 194 | `Envelope`'s seven keys: max-uint64 `id`, `"attachment_chunk"`, a full-nanosecond RFC3339 `ts`, `payload`, max-uint64 `in_reply_to`, max-uint64 `event_id`, `payload_encrypted` |
| Payload braces, keys, quotes, colons, commas | 104 | the eight `json:` tags on `AttachmentChunkPayload` |
| `index` + `total_chunks` + `size` | 60 | 3 × 20; `-9223372036854775808` is the widest decimal the declared `int` / `int64` types admit |
| `sha256` | 384 | 64 × 6 |
| `attachment_id` | 384 | `MaxAttachmentIDBytes` × 6 |
| `filename` | 1530 | `MaxAttachmentFilenameBytes` × 6 |
| `mime_type` | 1530 | `MaxAttachmentMimeTypeBytes` × 6 |
| **Fixed total** | **4186** | |
| `data` at the bound | 60000 | 4 × ⌈45000 / 3⌉ — base64's alphabet (`A–Z a–z 0–9 + / =`) never escapes |
| **Frame total** | **64186** | **1333 B under the cap** |

Ceiling: ⌊(65519 − 4186) / 4⌋ × 3 = **45999**. 45000 is chosen below it for `bundleChunkBytes`'s reason — a conservative constant is the belt, the deterministic per-frame test is the suspenders — and because it is a multiple of 3, so base64 lands on exactly 60000 bytes with no padding and the arithmetic is checkable by eye.

These numbers are the **design's budget**, not assertions. The test measures the real total and asserts one thing: that it is under 65519.

### The cross-check: what `TotalChunks == ceil(Size / bound)` actually says

`AttachmentChunkPayload`'s doc comment hands #1741 this rule and hands **this** ticket the job of fixing its units. Do not transcribe the one-liner into the exported constant's comment — it is ambiguous in a way that decides whether #1741's allocation guard works, and this is the slice that can say so cheaply.

**Units, which is the part this ticket owns and settles.** `Size` is raw file bytes and `MaxAttachmentChunkBytes` is raw chunk bytes, so the division is raw-over-raw. Neither number is base64 and neither is envelope bytes.

**Which direction bounds the allocation, which is the part a reader gets wrong.** The rule exists to stop `make([][]byte, TotalChunks)` on an attacker-chosen count. A *maximum* chunk size yields only a **lower** bound on the honest chunk count — `ceil(Size / bound)` is the fewest chunks a file can arrive in — so relaxing the rule to `TotalChunks >= ceil(Size / bound)` caps nothing and leaves the allocation attack open. Only the **equality** bounds `TotalChunks` from above. Anyone tempted to soften it to an inequality for tolerance is removing the security property, and the comment should say that in one sentence.

**And equality has a price nobody has priced yet.** It silently mandates that every chunk but the last carries *exactly* `MaxAttachmentChunkBytes` raw bytes — a client that chunks a file into 16 KB pieces because that is its buffer size sends a conforming-looking transfer that a strict guard rejects. That is a real protocol constraint this ticket's published number creates, and it is a choice between "the bound is a maximum" and "the bound is the mandated chunk size". Bounding `TotalChunks` from above without it needs a declared *minimum* chunk size, which no ticket has.

**It is also already false at the boundary.** `Size == 0` gives `ceil(0 / bound) == 0`, against `AttachmentChunkPayload`'s documented `TotalChunks >= 1`. So an empty attachment is either unrepresentable or is one chunk carrying empty `Data` — the opposite of `DebugBundleDonePayload`, where an empty blob is a valid `0 chunks + done{total:0}` stream. Whichever #1741 picks, the equality needs a stated carve-out at zero or it rejects the empty file.

**What the constant's comment must therefore do:** state the units; state that equality — not `>=` — is the form that bounds `TotalChunks` from above, and why; name the every-chunk-but-the-last consequence and the `Size == 0` carve-out as **#1741's to decide**; and stop there. This ticket ships no guard and tests none of it, so the comment records what the number means and hands the two decisions on rather than asserting a rule nothing here verifies. Carry both to the ticket's open questions too — #1741 reads this comment to build the guard.

### Two deliberate over-budgets in the fill

Both cost a few hundred bytes against 1333 B of headroom, and both buy a proof that holds for frames that violate a prose contract:

1. **`sha256` is filled with 64 `<`, not 64 hex characters.** Hex never escapes, so the contract-respecting worst case is 64 B; the hostile fill costs 384 B. The extra 320 B buys a proof that holds for a length-respecting but non-hex value, which is exactly what an inbound attacker sends.
2. **`index`, `total_chunks` and `size` are filled at the widest decimal their declared Go types admit** (`-9223372036854775808`), even though `AttachmentChunkPayload`'s contract says `Index >= 0`, `TotalChunks >= 1` and `Size` is a byte length. Nothing enforces those yet, and the declared type *is* the bound recorded in shipped code — which is what AC 2 asks the fill to trace to.

Both need a sentence in the test's header comment. A reviewer reading a negative `size` in a "does it fit" test will otherwise read it as a mistake.

### Fixtures

Three files under `internal/protocol/testdata/`, all generated by marshalling through the package (§ Testing strategy), never hand-typed.

| File | Direction | Envelope | Payload |
|---|---|---|---|
| `attachment_chunk_upload.json` | client → daemon | plain `id`, **no** `in_reply_to` | first chunk of a two-chunk upload: `index: 0`, `total_chunks: 2`, a UUIDv4-shaped `attachment_id`, `mime_type: "application/pdf"`, a real `size` and `sha256`, a short `data` blob |
| `attachment_chunk_retrieval.json` | daemon → client | `id` **plus `in_reply_to`** | second chunk of a two-chunk retrieval: `index: 1`, `total_chunks: 2`, a different attachment, `mime_type: "image/png"`, a short `data` blob |
| `attachment_chunk_zero.json` | neither | ordinary `id` / `ts` | every payload field at its Go zero value; `Data` nil, so `"data":null` |

Three design points behind that table:

**The direction difference is `in_reply_to`, and it is the house convention rather than an invention.** `conversations.json` answers `list_conversations` with `in_reply_to` set; `send_message.json` carries none. Retrieval is a response to #1746's request verb and takes the response shape. Pin the *envelope* shape a daemon → client frame rides — #1746 still owns whether and how that verb correlates.

**`filename` on the upload fixture must contain a character `encoding/json` escapes** — e.g. `report <draft>.pdf`, committed as `report <draft>.pdf`. This is the one place the committed bytes visibly demonstrate that HTML escaping is on, which is the property the entire cap arithmetic rests on. It is also the strongest argument for generating fixtures rather than typing them: a hand-typed `<` would be wrong and the round trip would say so, but only after someone debugged it.

**The `data` blobs are deliberately short — a handful of bytes, `debug_bundle_chunk.json`'s size.** A chunk filled to 45000 raw bytes is a 60 KB committed line. Division of labour, and it belongs in the test comments: **the fixtures pin the encoding at small size; `TestAttachmentChunkPayload_FitV2EnvelopeCap` measures a full-size chunk.** Neither one alone covers both.

**`index: 0` on the upload fixture is chosen, not incidental.** Per the populated-vs-zero lesson in `protocol-package.md` § Model-list payload, a populated fixture only earns distinct omitempty coverage on a key where its row lands on that key's zero value. `index: 0` is both the realistic first chunk and the one key the populated fixtures cover independently of the zero fixture. Every other key's omitempty coverage rests on `attachment_chunk_zero.json`; say that in the zero test's comment rather than implying the directional fixtures share the load.

## Concurrency model

None. `internal/protocol` is a stdlib-only leaf of pure data types: no goroutines, no I/O, no context. Nothing in this ticket changes that. `t.Parallel()` on the new tests per `CODING-STYLE.md`, matching whatever the neighbouring tests in this package already do.

## Error handling

No runtime failure modes — the ticket adds constants and tests, not code paths. The failure modes that matter are build-time and test-time:

- **The cap proof fails.** Someone raised `MaxAttachmentChunkBytes` or a metadata bound past what the envelope admits. The recovery direction is in the constant's comment: **lower it, never raise the cap.**
- **A round trip fails.** A `json:` tag changed, a field was reordered, or a fixture was hand-edited. `roundTripEnvelope` prints both byte strings.
- **A byte guard fails while the round trip passes.** This is the `omitempty` signal AC 4 is built for, including after regeneration. The guard's failure message must print the fixture bytes — call `readFixture` **once outside the loop** and print that one `raw`, per the trap recorded in `protocol-package.md` § Slash-command-list payload.

## Testing strategy

One new file, `internal/protocol/attachments_test.go` (`foo.go` → `foo_test.go`, same package). All helpers — `roundTripEnvelope`, `readFixture`, `canonical` — are in-package; call them directly, add nothing new.

**`TestAttachmentChunkPayload_Upload_RoundTrip`** (AC 3)
- Read `attachment_chunk_upload.json`; unmarshal the envelope; assert `Type == TypeAttachmentChunk` and `InReplyTo == nil`.
- Unmarshal the payload; assert each of the eight fields against its fixture value, `Data` compared with `bytes.Equal`.
- Assert `Filename` decodes back to the literal `<` form — the escaped-and-unescaped pair is the point of that fixture.
- End on `roundTripEnvelope`.

**`TestAttachmentChunkPayload_Retrieval_RoundTrip`** (AC 3)
- Same shape against `attachment_chunk_retrieval.json`, plus: assert `InReplyTo != nil` and its value. That assertion is the only thing distinguishing this test from its sibling; without it "both directions" is two value sets.
- End on `roundTripEnvelope`.

**`TestAttachmentChunkPayload_ZeroValue_RoundTrip`** (AC 4)
- `raw := readFixture(t, "attachment_chunk_zero.json")` — **once**, before the loop.
- Loop over all eight literals, each `bytes.Contains(canonical(t, raw), []byte(want))`:
  `"attachment_id":""`, `"index":0`, `"total_chunks":0`, `"filename":""`, `"mime_type":""`, `"size":0`, `"sha256":""`, `"data":null`.
  All eight, because no field carries `omitempty` and this is the fixture `AttachmentChunkPayload`'s doc means when it says "#1753's fixtures pin the full shape". These are the assertions that survive regeneration: with an `omitempty` in place, regenerating this fixture elides the key and the guard stays red where the round trip would go green.
- Decode-side: assert every field at its zero value, and `Data == nil` specifically (nil marshals to `null`, `[]byte{}` marshals to `""` — the fixture commits to `null`).
- End on `roundTripEnvelope`.
- Header comment must carry two sentences: that `total_chunks: 0` contradicts the documented `>= 1` **on purpose** — this is a frame no producer will ever emit, it exists for the encoding and not the scenario, exactly as `TestModelListPayload_ZeroValue_RoundTrip`'s fixture does — and that the eight guards, not the round trip, are what make "explicit zero, not elided" checkable at all.

**`TestAttachmentChunkPayload_FitV2EnvelopeCap`** (AC 1, AC 2) — `TestToolUsePayload_FitV2EnvelopeCap`'s shape.
- `fill := func(n int) string { return strings.Repeat("<", n) }`.
- Test-local `const attachmentSHA256HexLen = 64`, commented as mirroring `AttachmentChunkPayload.SHA256`'s declared "always 64 hex characters" — the `capInputValueRunes` mirror precedent, so the number traces to a bound recorded in shipped code rather than existing only in the test.
- Payload: `AttachmentID: fill(MaxAttachmentIDBytes)`, `Filename: fill(MaxAttachmentFilenameBytes)`, `MimeType: fill(MaxAttachmentMimeTypeBytes)`, `SHA256: fill(attachmentSHA256HexLen)`, `Index` / `TotalChunks` / `Size` at `math.MinInt64`, `Data: bytes.Repeat([]byte{0xFF}, MaxAttachmentChunkBytes)`.
- Envelope worst case: `ID` and `InReplyTo` and `EventID` all max-uint64, `PayloadEncrypted: true`, `TS` at full nanosecond width. This deviates from the `tool_use` precedent, which sets only `ID` and `EventID` — deliberately, because this frame really does ride `in_reply_to` on the retrieval leg. `PayloadEncrypted` is a ceiling and not a scenario (`IsKnownAppType` rejects an inbound frame carrying it); 25 bytes is cheaper than the argument, and one line of comment says so. **`EventID` is set for the same byte-ceiling reason and is not a claim that this frame rides the event ring** — say so, because at 45000 raw bytes a frame the ring retains is a per-conversation memory multiplier, not just a per-frame cap. Ring membership is #1744's call.
- `t.Logf` the measured size and its percentage of the cap, then the single assertion: `len(out) >= maxV2AppEnvelope` is a failure.
- **Neither the `Logf` nor the failure message may print `out` or `body`** — lengths only, exactly as the `tool_use` precedent does. Two reasons: a 64 KB dump in CI output is unreadable, and `Data` is the field `AttachmentChunkPayload`'s doc marks content-bearing and NEVER LOGGED. A test that dumps it is the house pattern #1744 will copy.
- Header comment carries the arithmetic table's conclusion, the two over-budget choices from § Design, and — following the `tool_use` header's honesty — the statement that the three metadata bounds are **declared here and enforced nowhere yet**, so this is a proof about frames that respect them, with #1741 / #1744 / #1746 named as the slices that make them true.

### Generating the fixtures

Use the `go test -overlay` generator recipe recorded in `protocol-package.md` § Slash-command-list payload: map a non-existent path (`internal/protocol/genfixtures_test.go`) to a generator in the scratchpad, run it, write the three files. That gives the generator full access to the package's own encoder, so the committed escaping is provably the encoder's own, and it leaves nothing in the worktree. Pass the overlay JSON by absolute path. Compute each fixture's `sha256` and `size` in the generator from the blob itself so the committed frames are internally coherent. Do **not** hand-type the `<` escapes.

Two constraints on the generator, both cheap and both with a failure this ticket would otherwise walk into:

- **Deterministic — no RNG, no `time.Now()`.** Every `ts`, `id` and `in_reply_to` is a literal. AC 4's check is *regenerate the fixture under the mutant and confirm the guard is still red*; if regeneration also churns the timestamp, a diff that should be one elided key is a diff in three, and the signal is unreadable.
- **The `data` blobs are obviously synthetic short ASCII** — `debug_bundle_chunk.json`'s `"hello, bundle"` is the model. Never bytes read from a real file on the developer's machine, and nothing that could carry credential material into committed testdata (the failure class #1748 just landed a guard for). Same rule for `filename`: a synthetic name, not a real path.

If a shell driver is involved, run it under an explicit `bash -c` — this session's shell is `zsh`, whose word-splitting silently no-ops a `set -- $pair`-style loop instead of erroring.

### Mutation check before you call AC 4 done

Run all eight `,omitempty` mutants (one per wire key) through `go test -overlay=` against a scratch copy and confirm each reddens at least one test — the practice #1705 and #1718 both used. Two rules from those tickets:

- **Regenerate the fixtures under each mutant** before declaring it caught. That is the half AC 4 names explicitly and the half a round-trip-only design fails.
- **Do not write a "this test is the only one that reddens key X" claim into a comment from the prediction table.** Both #1718 comments that did this measured false in code review. Either read the sole-redness off the actual run output, or do not make the claim.

### Gate

`make check`. `internal/protocol` carries no build tag, so the standard gate compiles and runs everything this ticket adds.

## Open questions

The first two are #1741's to answer and are the reason § The cross-check exists. Both are consequences of a number this ticket publishes, so they are named here rather than left for #1741 to rediscover from the arithmetic.

1. **Is `MaxAttachmentChunkBytes` a maximum, or the mandated size of every chunk but the last?** Only the equality form of `TotalChunks == ceil(Size / bound)` bounds `TotalChunks` from above, and the equality forbids a client that chunks smaller than the bound. Bounding the count from above any other way needs a declared *minimum* chunk size, which no ticket has. #1741 picks; this ticket publishes the number and states the trade rather than deciding it.
2. **What the cross-check does at `Size == 0`.** `ceil(0 / bound) == 0` against the documented `TotalChunks >= 1`, so an empty attachment is either unrepresentable or one chunk with empty `Data`. `DebugBundleDonePayload` took the opposite position for its own stream (`0 chunks + done{total:0}` is valid), so there is no precedent to inherit silently.
3. **`MaxAttachmentIDBytes = 64` versus 36.** 64 is a ceiling chosen so #1741 can pick the canonical shape without this constant constraining it; 36 would pin `conversations.ValidID`'s UUIDv4 form now. 64 costs 168 bytes of headroom against 1333 available. Resolved in favour of 64 for this ticket; #1741 may narrow the *shape* below it and must not exceed it. Flagged because #1741 is the slice that finds out whether the looser ceiling was worth its bytes.
2. **Whether `sha256`'s 64 should become a fifth exported constant.** Left test-local here, mirroring `AttachmentChunkPayload`'s prose contract in the `capInputValueRunes` style. #1741 writes the hex validator and is the natural place for it to become exported, if it should.
5. **`Data` zero value committed as `null` rather than `""`.** `null` is the struct's true zero; both round-trip and both defeat an `omitempty` equally. Noted so a later reader does not read the choice as accidental.

## Security review

**Verdict:** PASS (one MUST FIX found and fixed inline before this section was written; see § The cross-check)

**Findings:**

- **[Trust boundaries] MUST FIX — fixed.** The spec's first draft had the developer transcribe `AttachmentChunkPayload`'s `TotalChunks == ceil(Size / bound)` one-liner into the **exported** constant's comment — i.e. into the contract clients and #1741 code against — while this ticket is the one fixing that rule's units. The rule is ambiguous in a security-relevant direction: a *maximum* chunk size yields only a **lower** bound on the honest chunk count, so a reader who softens the equality to `>=` for tolerance silently removes the only thing bounding `TotalChunks` from above and reopens the `make([][]byte, TotalChunks)` attack `AttachmentChunkPayload`'s NEVER ALLOCATE FROM A CLAIM block names. The equality in turn mandates that every chunk but the last is exactly `MaxAttachmentChunkBytes`, and is already false at `Size == 0` against the documented `TotalChunks >= 1`. Fixed by adding § The cross-check, which makes the comment state the units and the direction and hand both consequences to #1741 as named open questions rather than assert a rule this ticket does not test.
- **[Trust boundaries] No further findings.** Nothing in this ticket crosses a trust boundary at runtime: `internal/protocol` stays a pure-data stdlib leaf with no producer, consumer or validator, and the boundary that matters — inbound frames from an attacker-chosen leg — is `IsKnownAppType`'s and #1741's. The bounds published here are **declared and enforced nowhere**, which is a hazard only if a reader mistakes one for the other; each constant's comment names its enforcing slice (#1741 inbound, #1744 / #1746 outbound) for exactly that reason.
- **[File operations] SHOULD FIX — fixed.** `MaxAttachmentIDBytes` is a length ceiling and could read as a safety property on a field `AttachmentChunkPayload`'s doc says becomes a path component in #1743 / #1746. 64 bytes accommodates `../../../../etc/passwd` several times over. The comment now has to say the constant does nothing about traversal. Otherwise not applicable: the only file operations are `readFixture`'s `os.ReadFile` on a hardcoded `testdata/` name and the generator's three writes — no caller-supplied path, no TOCTOU, no symlink surface, no mode-sensitive file.
- **[Error messages, logs] SHOULD FIX — fixed.** `TestAttachmentChunkPayload_FitV2EnvelopeCap` marshals a 64 KB envelope whose `Data` is the field marked content-bearing and NEVER LOGGED. A failure message or `Logf` printing `out` or `body` would both flood CI and establish the wrong house pattern for #1744, which enforces that rule. The spec now requires lengths only, matching `TestToolUsePayload_FitV2EnvelopeCap`.
- **[Tokens, secrets, credentials] SHOULD FIX — fixed.** No credential material is introduced, and `SHA256`'s "integrity, not authenticity — must not become an access token" contract is untouched. The live risk was committed testdata: a developer generating fixtures could reach for a real file's bytes and a real path as `filename`. The generator section now requires obviously synthetic short ASCII blobs and synthetic filenames — the class #1748 landed a guard for.
- **[Cryptographic primitives] SHOULD FIX — fixed.** Only `crypto/sha256` over synthetic fixture blobs; no key material, no comparison of attacker-controlled values against secrets, no RNG needed. The finding is the inverse: the generator must use **no** RNG and no `time.Now()`, or regenerating a fixture under an `omitempty` mutant churns fields AC 4's check has to read a one-key diff against.
- **[Network & I/O] No findings.** The ticket's whole subject is an input size limit, and it is published rather than assumed. Two adversarial checks came back clean: the metadata bounds are byte bounds on the *decoded* string, which is what a receiver can check and what a producer must respect, so the two legs measure the same quantity; and publishing the exact chunk size leaks nothing, since the envelope cap is already published and the arithmetic derivable. No sockets, deadlines, TLS or connection accounting are in scope.
- **[Concurrency] Not applicable.** No goroutines, no shared mutable state, no locks — `t.Parallel()` tests over `readFixture`'s read-only `os.ReadFile` and local values. `internal/protocol` has none by design and this ticket does not change that.
- **[Subprocess / external command execution] Not applicable.** The only process spawned is `go test` under an overlay, with no caller-supplied argv. No `sh -c`; the shell driver, if any, runs under an explicit `bash -c` for the `zsh` word-splitting reason recorded in `protocol-package.md`.
- **[Threat model alignment] OUT OF SCOPE, named.** The relevant threat is resource exhaustion from attacker-chosen counts (`docs/protocol-mobile.md` § Security model; `AttachmentChunkPayload`'s NEVER ALLOCATE FROM A CLAIM block). This ticket's contribution is the published number that guard's arithmetic needs; the guard itself is #1741's, path-component validation is #1741 / #1743's, and never-log enforcement is #1744's. One further deferral surfaced during the walk and is now written into the cap test's requirements: at 45000 raw bytes a frame the event ring retains is a per-conversation memory multiplier rather than a per-frame cap, so whether `attachment_chunk` is ring-class is #1744's decision and the test's non-nil `EventID` must not read as a claim that it is.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
