# #2056 — Correct `AttachmentChunkPayload`'s doc block where it calls the outbound fields trustworthy

**Ticket:** [#2056](https://github.com/pyrycode/pyrycode/issues/2056)
**Size:** XS (downgraded from the refiner's S — see [Size](#size))
**Scope:** `internal/protocol/attachments.go`, comment text only. No test changes.

## Files read

- `internal/protocol/attachments.go` → `AttachmentChunkPayload` — the edit target: its
  SECURITY block's inbound/outbound sentence, the `Filename` and `MimeType`
  field-contract bullets, and the two matching struct field trailing comments.
- `internal/protocol/attachments.go` → `MaxAttachmentFilenameBytes`, and the const block's
  own preamble — the preamble already says a filename "arrives attacker-chosen on the
  upload leg, is stored, and is echoed back **daemon-authored** on the retrieval leg". It
  names both legs and uses the correct provenance word, so it is not an instance of the
  defect; it is the phrasing precedent for the fix. Not edited — see
  [Open questions](#open-questions).
- `internal/protocol/attachments.go` → `AttachmentStoredPayload`, `RequestAttachmentPayload` —
  the two blocks in this file that must NOT change. `AttachmentStoredPayload`'s restatement
  was corrected by #2053's AC#3; `RequestAttachmentPayload` is inbound-only and says so
  correctly ("EVERY FIELD IS AN UNVERIFIED CLAIM, ALWAYS"). Both quote
  `AttachmentChunkPayload`'s "nothing in it reports which direction a value came from" —
  which is why that clause survives the rewrite verbatim.
- `docs/protocol-mobile.md` § Attachments, *Trust and content hygiene* — the authority to
  match. Carries the provenance/trustworthiness distinction and the three client
  obligations (sanitise `filename`, never a path unsanitised, MUST NOT dispatch on
  `mime_type`).
- `internal/relay/v2attachmentstream.go` → `attachmentEnvelopes` — two things at once. It is
  the in-repo wording precedent the ticket names (the closed-set-of-constants nuance), and
  it is the code that actually mints the outbound values: `MimeType` is
  `http.DetectContentType(blob)` and `Filename` is the leaf of the path `ResolvePath`
  answered, i.e. `SanitizeFilename`'s rendering.
- `internal/attachments/storage.go` → `Store` — signature is `Store(dir, filename, data)`,
  three args, no media type, and its first statement is `SanitizeFilename(filename)`. This
  is the proof for both halves of the correction: the declared type has no storage site,
  and the stored name is the sanitised one.
- `internal/attachments/intake.go` → `Intake.Receive` — the sole call,
  `Store(dir, chunk.Filename, data)`. Confirms `chunk.MimeType` reaches no writer.
- `internal/attachments/accumulator.go` → `Accumulator.Add` — "Filename and MimeType are
  never read at all". Independent second confirmation on the accumulation path.
- `internal/attachments/filename.go` → `SanitizeFilename` — its own doc block settles the
  never-log paragraph: sanitising removes the log-injection half, the privacy half binds
  both legs, so that paragraph needs no change. Also records that the result is neither
  unique nor an identifier, which is why the fix calls it a *path component the bytes are
  stored under* rather than a name a client can rely on.
- `docs/knowledge/features/protocol-package-constants-codes-go-envelope-types-attachments.md`
  — already records this defect and names #2056. Read-only; the documentation phase owns it.
  Its lesson ("grep the corrected phrase across the repo, including production doc comments")
  is what drove the in-file sweep in [Design § 4](#4-the-in-file-sweep).
- `CODING-STYLE.md` § Comments — Citing Other Code — the fix cites `Store`,
  `SanitizeFilename` and `attachmentEnvelopes` by symbol, never by line.

## Context

`AttachmentChunkPayload`'s SECURITY block says the outbound fields are "daemon-authored and
**trustworthy**". `docs/protocol-mobile.md` § Attachments says the outbound fields are
daemon-authored and that this improves their *provenance*, not their *trustworthiness*, and
keeps **MUST NOT dispatch on `mime_type`** in force on both legs. The two published
contracts contradict each other, and the one in the source file is the more dangerous
reading: a client author implementing the retrieval leg reads "trustworthy" as a licence to
dispatch on a `mime_type` the protocol document explicitly forbids dispatching on.

The defect is pre-existing rather than introduced by #2053, but #2053 is what makes it
bite: now that `mime_type` is genuinely daemon-derived (`http.DetectContentType` over the
stored bytes, in `attachmentEnvelopes`), "daemon-authored **and trustworthy**" reads as a
conclusion the derivation supports. It does not — a *sniffed* `text/html` is exactly as
dangerous to render as a declared one.

Four further sentences in the same file carry the older, narrower form of the same error:
they describe `Filename` and `MimeType` only as the client's, as though the inbound leg were
the only one. That is false in a way the code can be pointed at — `Store` takes no media
type at all, so outbound there is nothing to echo — and leaving it would repeat exactly the
failure this ticket exists to correct.

**No ADR is warranted.** This changes no decision; it makes one source file agree with a
contract that is already published and already correct.

## Design

Five comment edits in one file, in one commit. Nothing else in the repo changes.

### 1. The SECURITY block's inbound/outbound sentence (AC1)

`AttachmentChunkPayload`'s SECURITY paragraph currently pairs "on the INBOUND leg every
field is an unverified CLAIM" with "on the outbound leg the same fields are daemon-authored
and trustworthy". Replace the second clause with § Attachments' own formulation, and carry
the two things the ticket's Technical Notes insist on:

- **Keep the leg distinction.** The block's opening sentence — "the trust level of every
  field depends on a direction the struct cannot report" — and the closing clause
  ("nothing in it reports which direction a value came from, so a consumer must decide that
  from where it received the frame") both stand unchanged. Two sibling blocks in this file
  quote that closing clause; changing it would strand them.
- **Keep the asymmetry.** The derived `MimeType` is drawn from a closed set of
  daemon-authored constants (`attachmentEnvelopes` records why), so it carries neither the
  log-injection nor the envelope-budget hazard an arbitrary 255-byte client string does.
  "Outbound is just as untrustworthy in every respect" would be a new error, not a fix.

Contract of the replacement: it asserts *daemon-authored*, denies *echoed back verbatim*,
names the provenance/trustworthiness split explicitly, restates the client obligations as
binding on both legs, and cites § Attachments as the authority rather than restating it as
though it were new here.

**`AttachmentID` is the one field the blanket outbound statement does not cover, and the
replacement must say so** (security review, MUST FIX #1). § Attachments' "none is a stored
client string echoed back verbatim" is written about the content metadata it has just
enumerated — `size`, `sha256`, `mime_type`, `filename`. It is false of `attachment_id`:
nothing daemon-side mints one (`AttachmentStoredPayload`'s block records that), the client
supplies it on every chunk of the upload, it is stored as the directory name `EnsureDir`
builds, and on the retrieval leg it is echoed back byte-identical —
`V2SessionManager.StreamAttachment` takes it as a parameter and `attachmentEnvelopes` copies
it onto every frame. Copying the blanket sentence into a block that enumerates all eight
fields would ship a *new* false claim in the very paragraph whose job is per-field precision,
and the misreading it invites is the expensive one: a client that reads the received
`attachment_id` as daemon-minted has no reason to shape-check it before using it as a cache
directory component, which is the traversal this block's own `AttachmentID` paragraph exists
to prevent. So the outbound half is stated as *daemon-authored, except `AttachmentID`, which
is the client's own id echoed back under the canonical shape check that let it become a path
component in the first place* — and the shape check binds a receiving client too.

Three further obligations the replacement must keep alive on the outbound leg, all
security-review SHOULD FIX items, because each is one a reader could drop precisely
*because* the leg is daemon-authored:

- **Sanitised is not loggable.** Introducing "sanitised" above the never-log paragraph makes
  that paragraph's "never a raw filename" newly readable as permitting a sanitised one.
  `SanitizeFilename`'s doc block settles it — sanitising removes the log-injection half, the
  privacy half binds both legs — so the new text points there rather than leaving the gap.
- **The allocation rule survives outbound.** NEVER ALLOCATE FROM A CLAIM is written about
  attacker-chosen integers on the inbound leg. § Attachments separately obliges a client to
  bound what it allocates from an *outbound* `size` / `total_chunks` against its own memory
  budget: the daemon is trusted here, but a fixed-budget client still has a budget.
- **The metadata bounds are not relaxable.** The derived `MimeType` does not *carry* the
  envelope-budget hazard, which is not the same as the budget being unnecessary — the const
  block's arithmetic budgets both metadata fields at their bounds and the inbound leg still
  supplies arbitrary 255-byte strings. The asymmetry is stated about the value, never about
  the bound.

### 2. The two field-contract bullets (AC2)

Both currently open with the client's leg and stop there. Both become both-legs statements
whose outbound half names the code that mints the value:

- **`Filename`** — inbound the client's own name; outbound the sanitised single path
  component the bytes are stored under, because `Store` hands the client's string to
  `SanitizeFilename` and keeps only the result. "A display string and a sanitiser input,
  never a path" survives, unweakened, as the property that binds on both legs.
- **`MimeType`** — inbound the client's declared media type; outbound sniffed from the
  stored bytes, because the declared value is never stored at all: `Store` takes a
  directory, a filename and the data, and `Intake.Receive` passes it nothing else. The
  present bullet's "a display and dispatch hint" becomes "a display hint … and never
  something to dispatch on in a way that grants the content privileges" — § Attachments'
  own rule. That is a strengthening, not a weakening, and it is made because "dispatch
  hint" is the exact phrase the corrected SECURITY block would otherwise contradict.

### 3. The two struct field trailing comments (AC2)

`AttachmentChunkPayload.Filename` and `.MimeType` carry the identical narrow claim as
one-line trailing comments. AC2 permits a compressed both-legs form; that is what they get
(inbound X, outbound Y, then the surviving property). **The code half of each line is
untouched** — same name, same type, same struct tag, same alignment column. Only the text
after `//` changes, which is what AC3's "comment lines only" means for a trailing comment
that shares a physical line with a field. `gofmt` aligns the comment *start* column, driven
by the tag width, so lengthening the text triggers no realignment of the block.

### 4. The in-file sweep

The package overview's recorded lesson is that a doc-correction AC naming specific sentences
does not imply it found every instance. So the fix is verified by sweeping the whole file
rather than by visiting five known sites: `git grep -n -i trustworth` and `git grep -n -F`
on each of the two quoted phrases, scoped to the file, must each come back either empty or
naming only corrected lines. See [Testing strategy](#testing-strategy).

Two near-instances were found by that sweep and are deliberately **not** edited:

- The const block's preamble ("arrives attacker-chosen on the upload leg, is stored, and is
  echoed back **daemon-authored** on the retrieval leg"). It already names both legs and
  already uses the correct provenance word. It is making an envelope-budget argument, not a
  trust claim.
- `AttachmentStoredPayload`'s and `RequestAttachmentPayload`'s SECURITY blocks. #2053's AC#3
  corrected the first; the second is a one-direction inbound frame whose "EVERY FIELD IS AN
  UNVERIFIED CLAIM, ALWAYS" is correct as written. The ticket puts both out of scope.

### 5. What does not change

The never-log paragraph, the NEVER ALLOCATE FROM A CLAIM paragraph, the AttachmentID
canonical-shape paragraph, the SHA256 integrity-not-authenticity paragraph, the "Filename,
Size and SHA256 are the claims a reader expects" sentence (an inbound-leg statement, correct
as written), every `type`, field, struct tag and constant in the file, and every other file
in the repo.

## Concurrency model

None. Comment text; no runtime surface.

## Error handling

None. No new failure mode, no behaviour change, no reject branch.

## Testing strategy

Comment text has no assertion surface, and inventing a test for it would assert nothing.
#2010 is the precedent: a standalone doc-correction ticket that shipped with zero test
changes. Verification is three deterministic greps plus a build gate:

1. `git grep -n -i trustworth -- internal/protocol/attachments.go` — every hit must be a line
   that *denies* outbound trustworthiness (AC1's own checkable).
2. `git grep -n -F "the client's own name for the file" -- internal/protocol/attachments.go`
   and the same for `"the client's declared media type"` — every hit must be on a bullet or
   comment that also names the outbound value (AC2's own checkable).
3. `git diff` on the file shows only comment text changed: no `type`, field, struct-tag or
   code line, and no line whose non-comment half differs (AC3).
4. `go vet ./...` and `go build ./cmd/pyry` green; `go test -race ./internal/protocol/...`
   green (the package's existing wire-key and envelope-cap pins are untouched and must stay
   green). `make check` is the verifier's gate.

## Open questions

1. **Does the const-block preamble need the same correction?** Resolved during planning: no.
   It names both legs and calls the outbound value daemon-authored, which is the correct
   word; it claims no trustworthiness and does not contain either of AC2's quoted phrases.
   Editing it would be out-of-scope churn in a paragraph whose subject is the envelope
   budget. Recorded here so the absence reads as a decision rather than a miss.
2. **Does dropping "dispatch" from the `MimeType` bullet weaken a client obligation?**
   Resolved during planning: no — it strengthens. The current bullet calls `MimeType` a
   "dispatch hint"; § Attachments says a client MUST NOT dispatch on it in any way that
   grants the content privileges. The replacement states the prohibition instead of the
   invitation. AC3 forbids *weakening* an obligation, which this is the opposite of.
3. **How much of § Attachments should the block restate versus cite?** Resolved during
   implementation if the edit runs long: the block cites § Attachments *Trust and content
   hygiene* as the authority and restates only the parts a reader of this struct needs to
   act correctly without leaving the file — the provenance/trustworthiness split and the
   three client obligations.

## Size

Re-counted against this written plan, per the size-S boundary:

| Limit | Boundary | This ticket |
|---|---|---|
| Production source files created or modified | ≤ 5 | **1** |
| Total written work | ≤ 800 lines | **~330** (this spec + ~55 changed comment lines) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** |
| Acceptance criteria | ≤ 5 | **3** |
| Distinct error/reject branches | ≤ 10 | **0** |

Not refactor-shaped: no signature changes, no renames, no type replacement, so no edit
fan-out to count.

**Stays at the refiner's `size:s`; the XS downgrade is declined.** The estimate permits XS
"if the comment edit lands under 30 changed lines", and before the security review that
looked right. The review added one MUST FIX and three SHOULD FIX clauses the corrected
SECURITY block has to carry, which puts the edit at roughly 55 changed lines. Every boundary
still holds with a wide margin, so this is a size label being reported honestly rather than a
constraint being approached.

## Security review

**Verdict:** PASS (first pass FAILED on MUST FIX #1; the plan above was revised and the
checklist re-walked from the top)

**Findings:**

- **[Trust boundaries] MUST FIX #1 — fixed in the plan before commit.** The correction's
  obvious move is to copy § Attachments' "Outbound every field is daemon-authored, and none
  is a stored client string echoed back verbatim" into `AttachmentChunkPayload`'s SECURITY
  block. That sentence is scoped to content metadata in its home document and is **false of
  `AttachmentID`** in a block that enumerates all eight fields: nothing daemon-side mints an
  attachment id (`AttachmentStoredPayload`'s block records it), the client supplies it on
  every chunk, `EnsureDir` makes it the directory name, and
  `V2SessionManager.StreamAttachment` takes it as a parameter which `attachmentEnvelopes`
  copies verbatim onto every outbound frame. Exploit path: a client author who reads the
  received `attachment_id` as daemon-minted has no reason to shape-check it before using it
  as a path component of its own cache — the exact traversal this block's `AttachmentID`
  paragraph exists to prevent, reintroduced by a comment intended to improve safety. Design § 1
  now carries the exception and states that the canonical-shape check binds a receiving
  client too.
- **[Error messages, logs] SHOULD FIX — addressed in the plan.** The fix introduces the word
  "sanitised" directly above a never-log paragraph that says "never a **raw** filename",
  making a previously unavailable reading available: that a sanitised filename is loggable.
  `SanitizeFilename`'s doc block forecloses it (the privacy half of the hazard binds
  regardless of sanitising); Design § 1 points there rather than leaving the inference open.
- **[Threat model alignment] SHOULD FIX — addressed in the plan.** NEVER ALLOCATE FROM A
  CLAIM is framed for the inbound leg's attacker-chosen integers, so declaring the outbound
  leg daemon-authored invites a client to drop its own memory bound. § Attachments obliges a
  client to bound what it allocates from an outbound `size`/`total_chunks` against its own
  budget; AC3 forbids weakening a client obligation, so the corrected text keeps it alive on
  both legs.
- **[Network & I/O] SHOULD FIX — addressed in the plan.** Carrying `attachmentEnvelopes`'
  closed-set nuance risks implying `MaxAttachmentMimeTypeBytes` is unnecessary. The derived
  value not *carrying* the envelope-budget hazard is not the bound being relaxable — the
  const block's arithmetic budgets both metadata fields, and the inbound leg still supplies
  arbitrary 255-byte strings. Stated about the value, never about the bound.
- **[Trust boundaries, second look] No further findings.** The other seven fields are
  genuinely daemon-authored outbound: `Index` is the loop counter, `TotalChunks` the computed
  chunk count, `Size` `len(blob)`, `SHA256` a `crypto/sha256` digest of the stored bytes,
  `Data` the stored bytes, `MimeType` `http.DetectContentType`'s answer, and `Filename` the
  leaf `ResolvePath` answered — all minted inside `attachmentEnvelopes`.
- **[Tokens, secrets, credentials] No findings.** Both capability denials in this block —
  SHA256 must not become a content-addressed retrieval token, and the attachment id is not
  secret, not unguessable and never the only thing between a caller and a file — are outside
  every edit site and are preserved explicitly by Design § 5.
- **[File operations] No findings beyond MUST FIX #1.** No code path changes, so no
  traversal, TOCTOU, permission-mode or symlink surface moves. The `AttachmentID`
  canonical-shape-before-path-component paragraph is untouched.
- **[Subprocess / external command execution] Not applicable.** `internal/protocol` declares
  wire shapes and executes nothing; there is no `exec.Command` in the package.
- **[Cryptographic primitives] No findings.** The block's EXACT EQUALITY rule on lowercase
  hex is deliberately not constant-time, and that is correct as designed: the digest is not a
  secret and is explicitly barred from becoming a capability, so a timing side channel
  discloses only what the attacker supplied. Untouched by every edit.
- **[Concurrency] Not applicable.** Comment text only — no goroutine, no lock, no shared
  state. The file's one concurrency-adjacent claim (`QuestionShownPayload.MarshalJSON`'s
  backing-array argument, cited by two sibling blocks) is outside every edit site.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
