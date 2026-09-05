# #2112 — a versioned, segmented per-conversation log with backward paging

`internal/history`: the durable, append-only, per-conversation message log the
daemon writes as it fans envelopes out, read newest-first by walking backwards
on demand. Storage floor only — **no producer and no consumer ship here**
(#2114/#2115 append, #2116 serves, #2113 declares the wire verb).

## Files read

- `internal/eventring/ring.go` → `Ring`, `Event`, `Append`, `After`, `NewestID` —
  the retained-triple shape (`Type`/`Payload`/`TS` plus a minted id) this package
  copies, and the internally-synchronised-leaf-mutex posture a producer must not
  have to lock around. Also the counterexample: its `nextID` restarts at 1 on
  every daemon start, which is why its `event_id` cannot be the durable id here.
- `internal/attachments/storage.go` → `EnsureDir`, `ResolvePath`,
  `ErrInvalidID`, `ErrNotContained`, `ErrNotFound`, `Store` — the
  `<instanceDir>/conversations/<conversationID>/…` layout, the root-as-argument
  style, the resolve-anchor-then-compare-for-equality containment refusal (and
  why equality, not `filepath.Rel`, is the test), the 0o700/0o600 modes, and the
  single-sentinel non-oracle posture this package copies for cursor refusals.
- `internal/conversations/id.go` → `ValidID`, `ConversationID` — the
  canonical-shape predicate (36 chars, lowercase hex, dashes at fixed offsets;
  admits neither `/` nor `.` nor `..`) and the typed id that makes an
  id-argument swap a compile error.
- `docs/knowledge/features/eventring-package.md` § "Durability boundary",
  § "`After` — the three-way replay contract", § "Concurrency" — the lesson that
  carried the most weight here: **a boundary inferred from what an id sequence
  implies is only as sound as the contiguity assumption behind it** (#2022's
  `afterID+1` defect). This design never does arithmetic on an untrusted cursor;
  the cursor's offset is checked against the entry boundaries actually found in
  the segment, not computed from one.
- `CODING-STYLE.md` § Error Handling, § Concurrency, § Testing — sentinels +
  `errors.Is`, mutex-for-state, table-driven stdlib tests.

## Context

A client that opens an existing conversation sees nothing that happened before
it connected, because nothing on disk holds it. `replayMissed` over
`internal/eventring` is catch-up across a dropped connection — bounded at
`MaxEventsPerConversation`, in-memory, empty after a restart — and cannot become
history. This ticket is the storage floor.

The daemon owns the log; nothing here reads a file claude wrote (rejected
2026-09-04: claude's transcript format is not a contract this repo versions, it
binds history to one harness, and the daemon already produces the wire mapping
every turn). Scope is forward-only: no import, no backfill, no migration.
Retention is **none, deliberately** — endless for now — and the access pattern is
**newest-first, walking backwards on demand**. Those two decisions are what make
segmentation the shape: the newest segment is small, older ones are opened only
when the operator scrolls into them, segment boundaries are the index so no
sidecar file exists, and a future retention policy becomes a file delete rather
than a rewrite.

**No ADR is warranted.** The two decisions that would justify one — reject
claude's transcripts, segment rather than single-file — are already recorded in
the ticket body with their reasoning, and the package overview
`docs/knowledge/features/history-package.md` (documentation phase's to write) is
their evergreen home. This design introduces no cross-cutting decision beyond
them.

### Sizing: deliberately over two lines of the table

| Boundary | Limit | This plan |
|---|---|---|
| Production source files | ≤ 5 | **3** |
| Total written work | ≤ 800 | **~1600** ✗ |
| New exported types/interfaces | ≤ 5 | **3** (`Store`, `Entry`, `Page`) |
| Consumer call sites to update | ≤ 10 | **0** (new package, no producer, no consumer) |
| Acceptance criteria | ≤ 5 | **6** ✗ |
| Reject branches | ≤ 10 | **7** |

Two lines are exceeded. Split depth is `parent 2091 / grandparent none`, so a
split is *permitted* — this is a judgement call, not a depth cap, and it is made
here rather than deferred. I re-derived the refiner's candidate cuts and reached
the same answer: **the floor rules them all out, and when the floor and the
ceiling disagree the floor wins.**

- *Writer from reader.* A log nothing reads back is checkable only by a test-only
  reader; a reader with no writer has only hand-forged fixtures. AC 2 ("a store
  opened over a directory an earlier store wrote") is literally a statement about
  both halves at once and cannot be asserted from either alone.
- *Newest page from backward paging.* One walk, not two. `MaxSegmentBytes`
  bounds the newest segment, so it can hold **fewer** entries than a requested
  page — serving the newest page already crosses a segment boundary walking
  backwards. A "backward paging" slice on top would add only the cursor's
  encoding and the terminal marker, re-touching the same three files.
- *The large-log bound, the version arm, the cursor's validation.* Each is one
  property of the same read path, not a deliverable that lands or reddens a gate
  by itself.

The line ceiling protects against a budget miss, which costs one continuation
leg. The floor protects against a ticket that cannot be verified on its own,
which no resume fixes. Building.

## Design

### Package shape

`internal/history`, a leaf: it imports `internal/conversations` (for `ValidID` /
`ConversationID`) and stdlib, and **nothing else**. It never imports the
wire-payload types and never decodes a payload — the same leaf shape
`eventring` has, which is what keeps both producers (#2114 on the emitter's Run
goroutine, #2115 on the delivery path) and the consumer (#2116 on the per-conn
app-frame worker) free of a dependency cycle.

Three production files:

| File | Holds |
|---|---|
| `log.go` | package doc, `Store`, `New`, `Append`, `Page`, the seven sentinels, the per-conversation cached state, directory resolution + containment |
| `segment.go` | segment naming and ordering, the versioned header, the entry-line codec, whole-segment read |
| `cursor.go` | cursor mint + parse + validation |

### Exported surface

```go
const MaxSegmentBytes int64 = 1 << 20 // named segment bound; roll threshold
const MaxPageEntries  int   = 4096    // ceiling a requested page size is clamped to

type Entry struct {
    ID      uint64          `json:"id"`      // per-conversation, monotonic across restarts, >= 1
    Type    string          `json:"type"`    // protocol.Type* wire type, stored as an opaque string
    Payload json.RawMessage `json:"payload"` // already-marshalled envelope payload, never decoded here
    TS      time.Time       `json:"ts"`
}

type Page struct {
    Entries []Entry // newest-first
    Cursor  string  // opaque; feed to the next Page call for the next-older page. "" when AtStart
    AtStart bool    // the start of the log was reached while filling this page
}

type Store struct{ /* sync.Mutex + instanceDir + maxSegmentBytes + map[ConversationID]*convLog + read counters */ }

func New(instanceDir string) *Store
func (s *Store) Append(convID conversations.ConversationID, typ string, payload json.RawMessage, ts time.Time) (uint64, error)
func (s *Store) Page(convID conversations.ConversationID, cursor string, limit int) (Page, error)
```

`New` takes `instanceDir` and builds `conversations/<id>/history/` beneath it,
exactly as `attachments.EnsureDir` does — the root is an argument, never resolved
inside the package. Tests construct through the unexported
`newStore(instanceDir string, maxSegmentBytes int64) *Store` that `New`
delegates to; that is how the segment bound is settable without growing the
exported surface (AC 1's boundary crossing and AC 4's many-segment log would
otherwise need fixtures of thousands of entries).

`convID` is typed and `typ` is not, on `EnsureDir`'s reasoning: the id becomes a
path component, so typing it makes an argument swap a compile error.

**PRECONDITION on both methods, and it carries the whole authorisation
property:** `convID` MUST be the conversation the authenticated session is
already on, never one a client asserted. This package cannot check that —
`ValidID` is a *shape* predicate, and a client-supplied id of canonical shape
genuinely does resolve inside the conversation it names, defeating every check
below it. It is `ResolvePath`'s precondition verbatim, for the same reason, and
`Intake`'s resolver-callback shape (reach the conversation through the session,
not off the wire) is the one #2116 must copy. It is stated on both doc comments.

`limit` is **clamped** to `MaxPageEntries`, not refused above it: a page is "up
to `limit` entries" and a clamped answer still returns a cursor, so an
over-large ask pages rather than fails and #2116 can never be broken by the
ceiling. The clamp is what makes `Page`'s work bounded by a package constant
rather than by its caller's arithmetic. `limit < 1` is `ErrInvalidPageSize`. The
result slice is **never pre-allocated from `limit`** — it grows to what is
actually on disk, so a large ask pins no memory of its own.

### On-disk layout

```
<instanceDir>/conversations/<conversationID>/history/segment-00000000000000000001.jsonl
                                                     segment-00000000000000000002.jsonl
```

A sibling of `attachments/` under the same per-conversation root. Directories
0o700, files 0o600 — the mode every registry under the instance directory uses.
Twenty zero-padded digits hold any `uint64`, so lexicographic order **is**
numeric order and `os.ReadDir`'s sorted answer needs no re-sort.

Each segment is JSON-lines. The first line is the versioned header (AC 3), then
one entry per line:

```
{"format":"pyrycode.history","version":1}
{"id":1,"type":"assistant_delta","payload":{…},"ts":"2026-09-05T…Z"}
```

The header is a JSON object rather than a magic string so a future v2 is a field
change, not a format change; a first line that fails to decode, or decodes with
an unrecognised `format`/`version`, is `ErrUnknownVersion` and **no entry from
that segment is decoded**. Its byte length is fixed for v1, which is what makes
cursor offsets stable.

Entries are written with a `json.Encoder` at `SetEscapeHTML(false)` — which
already terminates each value with `\n`, so the JSON-lines invariant comes from
the encoder rather than from string concatenation. `Payload` round-trips as an
opaque `json.RawMessage`: this package never unmarshals it into a typed value.
It is re-encoded, so JSON-insignificant whitespace does not survive; producers
pass the output of a `json.Marshal`, which is already compact, so in practice
the bytes are identical. `Append` refuses a payload that is not valid JSON
(`ErrInvalidPayload`) before touching the filesystem — that check is also what
guarantees no entry line can contain a raw newline, since valid JSON escapes
them.

`Append` also refuses an **encoded line longer than the segment bound**, same
sentinel. Nothing real is rejected: the v2 application envelope is 65519 bytes,
so a 1 MiB entry cannot arrive from either producer. What it buys is an upper
bound on a well-formed segment — header plus the bound plus at most one entry —
which is what lets the read side cap its allocation. The read reads through an
`io.LimitReader` at that ceiling and reports `ErrCorruptSegment` if the file
reaches it, so a segment enlarged out of band (or a symlink that slipped past
the leaf check) cannot pin an arbitrary allocation.

### Entry ids

A **per-conversation** counter starting at 1, recovered from disk on the first
touch of a conversation in this process: read the newest segment, take its last
entry's id, resume at `+1`. That satisfies AC 2's second clause without a
sidecar file and without an fsync.

Deliberately **not** `eventring`'s shape. Its counter is ring-wide (#2022) to
stop a single scalar cursor taken from one conversation muting another's live
stream. That defect cannot arise here: every cursor this package mints names its
own conversation and is refused on any other (AC 6), so there is no shared scalar
to confuse and no reason to pay for a global counter that would have to be
recovered by reading *every* conversation's newest segment at startup.

### Backward paging

`Page(convID, cursor, limit)` collects newest-first:

1. Resolve the conversation's history directory (below). A directory that does
   not exist ⇒ `Page{AtStart: true}`, **no error** — AC 1's empty conversation.
2. `os.ReadDir`; keep names matching the segment pattern; walk them from highest
   number down.
3. When `cursor` is non-empty, validate it (below) and start at the segment it
   names, taking only that segment's entries **strictly before** its offset.
4. Read each segment whole, take its entries from the back until `limit` is
   reached, then stop.
5. If segments ran out before the page filled ⇒ `AtStart: true`, `Cursor: ""`.
   Otherwise `Cursor` = a fresh cursor at the position of the **oldest entry
   returned**, `AtStart: false`.

A page that fills exactly at the log's first entry reports `AtStart: false`; the
next call then returns zero entries with `AtStart: true`. That is the contract
AC 1 asks for — "a full page that may have more behind it" is never confused
with the terminal answer — and a walk terminates on `AtStart`, never on an empty
`Entries`.

`os.ReadDir` is chosen over probing `segment-N` upward from 1 precisely because
retention is left open: the moment a policy deletes old segments the numbering is
no longer contiguous and any probe-based search silently stops early. A listing
tolerates gaps by construction.

**The bound (AC 4).** Bytes read and segments opened are
`O(⌈limit / entries-per-segment⌉ + 1) × MaxSegmentBytes` — bounded by the page
size and the segment constant, never by the log's size. The `os.ReadDir` is one
directory listing whose cost grows with the segment count; it opens no segment
and decodes no entry, and it is the honest price of tolerating a future
retention delete. AC 4 names exactly two numbers, and both are instrumented:
`Store` carries unexported `readBytes` / `segmentsOpened` counters, incremented
at the one place a segment file is read and readable from an in-package test.

### Cursor

Opaque on the wire (#2113 declares it that way; #2116 "passes it through and
never parses it"), so **this package is the only place a cursor is ever
validated** and it arrives from an untrusted client.

Mint: `base64url-nopad("1." + convID + "." + segment + "." + offset)`, where
`offset` is the byte offset at which the oldest returned entry's line begins.
`.` is a safe separator because `ValidID` admits neither it nor any other
character outside lowercase hex and `-`. Base64 is not obfuscation — it is what
makes the string visibly opaque so no client is tempted to parse it.

Parse refuses, all as one `ErrInvalidCursor`, returning **no entries**:

- not valid base64url, or not exactly four `.`-separated fields, or version ≠ `1`
- a segment or offset field that `strconv.ParseUint` refuses (unsigned
  explicitly, so no sign can reach the `%020d` that builds a filename)
- a conversation id that fails `ValidID`, **or that is not the conversation being
  read** — the different-conversation forgery
- a segment number that is not present in this conversation's directory
- an offset that is not the start of an entry line in that segment

The last check is the one that matters and it is deliberately **not arithmetic**.
The segment is read (bounded by `MaxSegmentBytes`), its entry-line start offsets
are collected, and the cursor's offset must be one of them or the header's
length. Nothing is computed from the untrusted number — the #2022 lesson from
`eventring`, where a boundary inferred from an assumed id sequence wrapped at
`math.MaxUint64`. Here `math.MaxUint64` is simply not an entry boundary.

One sentinel covers all six causes on `attachments.ErrNotFound`'s reasoning: the
distinctions are exactly the ones a traversal probe would want, so a consumer
that cannot branch on them cannot leak them. **No refusal echoes the cursor
bytes** — the cursor is client-supplied text, so a message carrying it would put
attacker-chosen characters into whatever the consumer logs. Refusals name the
conversation id and the reason, never the input.

**No HMAC.** Considered and rejected: authenticating the cursor would need a key
with a lifecycle, and it buys nothing the checks above do not. The only thing a
forged cursor could name is `(conversation, segment, offset)`; the conversation
must equal the one the caller is already authorised for, the segment is a
`uint64` formatted into a fixed-width filename (no traversal is expressible), and
the offset must be a real entry boundary. What is left is "read your own
conversation's own entries from a position you could have reached by scrolling",
which is not a privilege gain.

### Directory resolution and containment

`resolveDir(convID, create bool) (string, error)` — `attachments.EnsureDir` /
`ResolvePath`'s discipline, minus the attachment-id level:

1. `ValidID(convID)` before the filesystem is touched ⇒ `ErrInvalidID`.
2. `filepath.Abs(instanceDir)`; on the create path `MkdirAll` it 0o700 (every
   registry under it creates it lazily), then `EvalSymlinks` it. **The resolved
   instance directory is the anchor** — anchoring on the resolved *conversation*
   directory would defeat the check, since a conversation directory symlinked out
   resolves first and everything beneath it is then trivially "contained".
3. Build `want = <root>/conversations/<convID>/history` **textually** beneath the
   resolved root; resolve the destination and require **equality** with `want`.
   Equality, not a `filepath.Rel` "is it under the root" test: equality is
   strictly stronger and is what refuses a history directory symlinked at a
   *sibling conversation*, which stays inside the instance directory and passes
   any containment test. Mismatch ⇒ `ErrNotContained`.
4. Read path only: a destination that does not exist is **not** an error — it is
   the empty conversation.

**The resolved path is deliberately not cached**, and that is a change from the
first draft of this plan. Caching it per conversation would save ~4 `lstat`
calls per operation and would widen `EnsureDir`'s accepted check-then-use window
from *within one call* to *the whole process lifetime*: a symlink planted after
the first resolve would redirect every later append and read for as long as the
daemon runs. `EnsureDir`'s bound ("exploiting it needs write access inside the
daemon's own 0o700 state directory") justifies the narrow window, not a
process-long one, and re-resolving is cheaper than the argument for keeping it.
`convLog` therefore caches only the append cursor — active segment number, its
size, and the next entry id.

Two leaf protections beyond the directory check, because containment of the
*directory* is not containment of the *file* it holds:

- The append open carries `syscall.O_NOFOLLOW`, so a segment name replaced by a
  symlink is refused rather than written through. (Linux and macOS both define
  it; Windows is out of scope per `CLAUDE.md`. If it fails to build, the
  fallback is an `os.Lstat` regular-file check before the open, accepting the
  narrower window.)
- The read side skips any directory entry that is not a regular file, which is
  `ResolvePath`'s leaf discipline: a symlink parked in the history directory is
  stepped over rather than followed.

## Concurrency model

**No goroutine is spawned; there is nothing to shut down and no `Close`.** The
store is passive, like `Ring`. No file handle is held open between calls: each
append opens `O_WRONLY|O_APPEND` (plus `O_CREATE|O_EXCL` when starting a new
segment), writes one buffer, closes. That is what removes the lifecycle
question entirely, and it is affordable because durability here is
process-restart durability: the bytes are in the page cache the moment `write`
returns, and they survive the process without an fsync.

One `sync.Mutex` on `Store` guards the per-conversation state map, the read
counters, **and** both `Append` and `Page` end to end. A leaf lock: held only
around map lookups and bounded file I/O, never across a channel operation and
never nested, so no lock-ordering rule is needed. Producers take no lock of
their own, which is the #2114/#2115 requirement.

Reads take the lock rather than racing appends, and that is a deliberate trade.
The alternative — lock-free reads plus a "ignore a trailing line with no
terminating newline" rule — buys a shorter hold at the cost of reasoning about
whether a partial `write(2)` is observable on every filesystem the daemon runs
on. Holding a leaf mutex across a read bounded by `MaxSegmentBytes` is cheaper
than being wrong about that. It makes AC 5 structural: an append and a page never
interleave *within* a call, and across calls the cursor names a fixed byte
position while appends only ever add at the far end, so a walk in progress
cannot have its entries or their order changed.

Concurrent `Append` calls for **different** conversations serialise on the same
mutex. That is accepted: the work under the lock is one bounded `write` per
call, the same shape `eventring` already imposes on the emitter.

## Error handling

Seven sentinels, matched with `errors.Is`, never by string:

| Sentinel | Raised when |
|---|---|
| `ErrInvalidID` | the conversation id fails `ValidID` |
| `ErrNotContained` | the history directory resolves somewhere other than where the id maps to |
| `ErrInvalidPayload` | `Append` was handed bytes that are not valid JSON, or an entry whose encoded line exceeds the segment bound |
| `ErrInvalidPageSize` | `Page` was asked for fewer than one entry |
| `ErrInvalidCursor` | any of the six cursor refusals above |
| `ErrUnknownVersion` | a segment's first line is not a header this build recognises |
| `ErrCorruptSegment` | a line in a recognised segment does not decode as an entry, or the segment exceeds its structural ceiling |

Filesystem failures wrap the OS error with the operation and the directory,
without a sentinel — a caller that needs to branch has the seven above, and
#2116 maps anything else to one internal-error wire code.

**Payload bytes are conversation content.** This log is the one place they are
written. No sentinel's message, no wrapped error and no log call in this package
names an entry's payload, its type or its byte length; errors name the
conversation id, the segment file and the failing operation only. The package
makes **no `slog` call at all** — it takes no logger, which is the strongest
form of that guarantee, and matches `eventring`'s and `attachments`'s posture
(the consumer logs, the primitive does not).

A truncated tail — a final line with no terminating newline — is
`ErrCorruptSegment`, not a silent skip. It is unreachable while the process
lives (writes are serialised under the mutex and each is one `write` call) and
only a machine crash mid-append can produce it, which the ticket puts explicitly
out of scope. Surfacing it as an explicit error beats silently dropping data or
appending after it.

## Testing strategy

Table-driven, stdlib `testing`, same-package (the unexported `newStore` and the
read counters are the test seam), `t.TempDir()` for every fixture, `-race`
throughout. Test files: `log_test.go`, `segment_test.go`, `cursor_test.go`.

Per acceptance criterion:

- **AC 1 — paging.** `newStore` with a bound small enough that ~4 entries fill a
  segment; append ~25 entries; walk from the newest page with `limit` 3, feeding
  each page's cursor back, until `AtStart`. Assert the concatenation equals the
  appended entries reversed exactly — which is "no entry repeated and none
  skipped" and "crosses a segment boundary" in one assertion, since the walk
  spans ~6 segments. Separate case: `Page` on a conversation with no directory
  ⇒ zero entries, `AtStart` true, nil error. Separate case: the page that fills
  exactly at the log's first entry reports `AtStart` false and its successor
  reports `AtStart` true with zero entries.
- **AC 2 — from disk.** Store A appends N entries across segments; a **fresh**
  `newStore` over the same directory walks them all back in the same order; an
  append through the second store returns an id `> max(ids on disk)` and a third
  store reads it back last. The ids are compared as values, so the assertion
  fails if the counter restarts at 1.
- **AC 3 — version arm.** Hand-write a segment whose header line is (a) a
  different `version`, (b) a different `format`, (c) not JSON at all; each ⇒
  `errors.Is(err, ErrUnknownVersion)` and zero entries returned. A fourth case
  writes a *valid* v1 header followed by a garbage line ⇒ `ErrCorruptSegment`,
  proving the two arms are distinguishable.
- **AC 4 — bounded work.** Build a log of exactly *k* full segments (identical
  entries, so the roll lands at the same entry count every time); read the
  counters; serve the newest page; record the delta. Grow to `101k` full
  segments; reset and repeat. Assert both deltas are byte-for-byte equal, and
  separately that `segmentsOpened` is 1 for a page smaller than one segment's
  entry count.
- **AC 5 — appends during a walk.** Take page 1; append several entries; take
  page 2 with page 1's cursor; append more; continue to `AtStart`. Assert the
  walk's output equals the entries that existed *before* the walk started, in
  order, and that no appended entry appears in any page. A second test drives
  concurrent `Append` calls from several goroutines while a walk runs, under
  `-race`, asserting the walk's answer is a prefix (newest-first) of some valid
  ordering and that every appended id is distinct.
- **AC 6 — forged cursors.** Table over: not base64; base64 of the wrong field
  count; wrong version tag; non-numeric segment; non-numeric offset; a
  syntactically valid id that fails `ValidID`; a cursor minted while reading
  conversation A replayed against conversation B; a segment number no file
  exists for; an offset one byte past a real boundary; an offset of
  `math.MaxUint64`; an offset of 0 (inside the header). Each ⇒
  `errors.Is(err, ErrInvalidCursor)`, `Entries` nil, `AtStart` false. The
  different-conversation case additionally asserts B's answer contains none of
  A's entries and that A's directory was never opened (`segmentsOpened`
  unchanged) — the observable form of "no read outside that conversation's own
  log directory".

Plus, not tied to one criterion: `ValidID` refusal on `Append` and `Page`;
`ErrInvalidPayload` on non-JSON bytes **and** on an entry whose line exceeds the
segment bound; `ErrInvalidPageSize` on `limit` 0 and negative; a `limit` above
`MaxPageEntries` clamped rather than refused, still answering a usable cursor;
`ErrNotContained` on a history directory symlinked at a sibling conversation (the
case `filepath.Rel` containment would pass); a payload containing `<`, `>` and
`&` round-tripping unescaped; a segment file replaced by a symlink being skipped
by the reader rather than followed; `ErrCorruptSegment` on a segment padded past
its structural ceiling.

## Open questions

1. **Does `#2116` need the entry id on the wire, or only the cursor?** `Entry.ID`
   is exported either way because AC 2 pins it, but whether it is part of the
   page reply is #2113's to declare. No action here.
2. **Segment bound value.** `1 << 20` is a starting point, not load-tested —
   the same posture `MaxEventsPerConversation` documents for itself. Named
   constant, one edit to change.
3. **Does the `os.ReadDir` per page need a cached ceiling?** Deferred: it is one
   listing, no segment opened, and caching a max-segment number reintroduces an
   invalidation question the append path would have to answer. Revisit only if a
   real conversation reaches thousands of segments.
4. **Does `syscall.O_NOFOLLOW` build cleanly on both target platforms?**
   Resolved in Phase B by `go build`; the fallback if not is named in
   § Directory resolution.

## Security review

**Verdict:** PASS (second pass — the first failed on three MUST FIX findings,
all revised into the design above before this commit)

**Findings:**

- [Trust boundaries] **MUST FIX — fixed.** Three untrusted inputs cross into this
  package: the cursor, the conversation id, and the page size, all reaching it
  from a remote client through #2116. The first draft validated the conversation
  id's *shape* with `ValidID` and stopped there, which is not an authorisation
  check — a client-asserted id of canonical shape genuinely resolves inside the
  conversation it names and defeats every check beneath it. `ResolvePath` states
  this as a doc-comment PRECONDITION for exactly this reason and the draft
  borrowed the layout without borrowing the precondition. Fixed: § Exported
  surface now carries it, and it goes on both `Append` and `Page` doc comments in
  Phase B.
- [Trust boundaries] **MUST FIX — fixed.** `Page`'s work was bounded by its
  caller's `limit` argument, so `Page(conv, "", math.MaxInt)` read and decoded
  every segment of the log — an unbounded read driven by a number computed one
  package away from an untrusted envelope budget. Fixed: `MaxPageEntries` clamps
  it, and the result slice is never pre-allocated from it.
- [File operations] **MUST FIX — fixed.** The draft cached the symlink-resolved
  history directory per conversation. That borrows `EnsureDir`'s accepted
  check-then-use window while silently widening it from one call to the daemon's
  whole lifetime: one symlink planted after the first resolve redirects every
  later append and read. Fixed: the directory is re-resolved on every call.
- [File operations] SHOULD FIX — containment of the directory is not containment
  of the file inside it. The append open takes `syscall.O_NOFOLLOW` and the
  reader skips non-regular directory entries (`ResolvePath`'s leaf discipline).
  The verifier should check both landed.
- [File operations] No further findings — `ValidID` admits no `/`, `.` or `..`;
  the segment component is a `strconv.ParseUint` value formatted `%020d`, so no
  traversal is expressible; the containment test is full-path **equality**, not
  `filepath.Rel`, which is what refuses a history directory symlinked at a
  *sibling* conversation; modes are 0o700 / 0o600. Atomic temp-and-rename is
  deliberately absent — the file is append-only and the ticket rules crash
  consistency out of scope.
- [Network & I/O] SHOULD FIX — resource exhaustion through the file, not a
  socket. `Append` refuses an entry line longer than the segment bound, which
  gives a well-formed segment a structural ceiling, and the read caps its
  allocation with an `io.LimitReader` at that ceiling. Without both, one
  out-of-band-enlarged segment pins an arbitrary allocation on the read path.
- [Error messages, logs] SHOULD FIX — `ErrInvalidCursor` must not echo the
  cursor, which is attacker-chosen text that would reach whatever the consumer
  logs. Refusals name the conversation id and the reason only. Separately, the
  package takes no `*slog.Logger` at all: payload bytes are conversation content
  and this log is the one place they may be written, so the structural guarantee
  (no logger to call) is stronger than a discipline about what to pass it.
- [Tokens, secrets] Not applicable, and stated in the design rather than assumed:
  no token is minted or stored, and **the cursor is not a capability** — it names
  a position inside a conversation the caller must already be authorised for, so
  possessing one grants nothing the precondition above has not already granted.
  Recorded because `docs/protocol-mobile.md` makes the same point about
  `attachment_id`, and an identifier repeatedly described as "not a capability"
  is exactly the kind that later gets treated as one.
- [Cryptographic primitives] Not applicable by an explicit design decision, not
  by omission: the cursor is deliberately unauthenticated (§ Cursor records why
  an HMAC buys nothing the four checks do not), and the base64url wrapper is an
  opacity convention, not a security primitive. No RNG is used anywhere.
- [Subprocess] Not applicable — this package executes nothing and builds no
  argv.
- [Concurrency] No findings. One leaf mutex guards the per-conversation map, the
  read counters, and both public methods end to end; it is never held across a
  channel operation and never nested, so there is no lock order to get wrong. No
  goroutine is spawned, so none can leak, and there is no `Close` to forget.
  Signalled mid-write leaves at worst a truncated final line, which is
  `ErrCorruptSegment` rather than a silent skip.
- [Threat model alignment] `docs/protocol-mobile.md` § Security model applies
  through #2116, not here: this package emits no wire code and holds no wire
  type. The one threat it must answer itself — a client naming another
  conversation — is the trust-boundary precondition above. Mapping these seven
  sentinels to wire codes is **out of scope, and #2116's**; retention and disk
  growth are out of scope by the ticket's own decision (none, deliberately).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05

**Extended 2026-09-05** — the `[Network & I/O]` finding above examined the read
direction only. See § Revisions for the write direction it did not open.

## Revisions

### 2026-09-05 — a failed write to a fresh segment (verifier MUST FIX, rework 1)

**Finding.** `writeSegment` creates a fresh segment with `O_CREATE|O_EXCL` before
it writes, and cleaned up neither the file nor the in-memory roll state when the
write lost. The residue is a zero-length segment, and the consequence is not a
lost append: `load` reaches it first and `decodeSegment` refuses it as
`ErrUnknownVersion`, so `Page` **and** `Append` fail for that conversation
forever — including the entries written successfully before the failure. In
process, `c.seg`/`c.segBytes` kept their pre-write values, so every later append
retargeted the same segment number and collided on `O_EXCL`. The trigger is an
ordinary `write(2)` error (ENOSPC, EDQUOT, EIO), not a machine crash, so the
ticket's "crash consistency is out of scope" does not cover it. § Design's
tolerance of a *header-only* newest segment was aimed one case short of the one
this package's own create-then-write sequence produces.

**Design change**, three parts, all in § Design's append path:

1. `writeSegment` removes a fresh segment whose write or close fails, so a
   failed append is a failed append and leaves the log where it was.
2. `Append` clears the conversation's `loaded` flag on any write error. How much
   of the buffer landed is exactly what the error does not report, so the belief
   about where the log ends is dropped rather than appended against: the next
   call re-derives the active segment, its size and the next id from disk.
3. `load` reports a zero-length newest segment as **full**, so the next append
   rolls past it rather than writing an entry line where the version belongs.
   The gap that leaves is already a supported shape — `listSegments` is a
   listing rather than a probe precisely so that a retention delete can punch
   holes in the numbering.

**Why both a cleanup and a tolerance.** They are different fabric, not the same
fix twice: (1) prevents the state, (3) makes it survivable. The cleanup is
best-effort — `os.Remove` can itself fail, and a crash between create and write
produces the same residue with no cleanup to run at all — so the guarantee that
matters, *a conversation is never permanently unreadable*, rests on (3), which
holds whatever produced the file. That split is also what makes the fix
testable: an ENOSPC on a freshly created file cannot be induced from a unit test
without a seam in production code, but the state it leaves can simply be planted
on disk, and that is what `TestZeroLengthSegmentIsToleratedAndRolledPast` does.

**Why not reuse the zero-length segment.** The alternative shape — adopt it as
the active segment and write the header into it — was rejected. It would have to
open a file this call did not create without `O_EXCL`, and it splits "does this
segment need a header" from "is this segment new", two conditions currently
carried by one flag. Rolling past is one rule (*a segment with no header is
never appended to*) and needs no new state.

**§ Security review, `[Network & I/O]`, extended.** That finding examined
resource exhaustion through the file in the read direction — an
out-of-band-enlarged segment pinning an allocation — and stopped there. The
write direction belonged under the same heading and was not opened: a failed
write can leave state that permanently denies the feature, which is a
denial-of-service against one conversation reachable from an ordinary full disk.
Both halves are now closed, and the general form is worth carrying into
`#2114`/`#2115`: for an append-only store the durability question is not only
"did the bytes land" but "what does a partial failure leave behind, and can the
next reader still make progress past it".

**Carried forward, not changed here.** The review also observed that `Append`
holds the store-wide mutex across `resolveDir` — roughly seven syscalls — plus
the open/write/close, and that every conversation pays it on one lock per
envelope. Re-resolving on every call is the trade § Directory resolution argues
for and it stands; but the emit chokepoint fans deltas out at token rate, so
`#2114` should measure the hold there rather than discover it later. Recorded
because the reason for the cost lives in this plan, not in the producer's.

**Also in this rework**, neither a design change:

- The `bytes.Clone` on entries leaving `Page` is removed. Its comment claimed the
  decoded payload aliases the segment buffer; `json.RawMessage.UnmarshalJSON` is
  documented to set its receiver to a *copy*, so the clone bought an allocation
  per entry per page against a premise that does not hold.
- `Append`'s doc comment claimed both `ErrInvalidPayload` refusals are checked
  "before anything is created". True of the payload's own length; the encoded
  line carries the minted id, so that check necessarily runs after the log
  directory exists. The comment now says which is which, and the test asserts
  what actually holds — that no segment file is created.
- Reject branches § Testing strategy named but no test reached: the encoded-line
  bound (a payload of *exactly* the segment bound passes the pre-check and
  reaches it), `decodeSegment`'s unterminated-final-line and no-newline-at-all
  arms, and `Page`'s refusal of a cursor for a conversation with no log
  directory. Each mutant was confirmed to redden the new test.
