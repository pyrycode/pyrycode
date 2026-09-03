package protocol

// Published byte bounds for one attachment_chunk frame (AttachmentChunkPayload
// below). All four are producer-side CONTRACTS WITH NO VALIDATOR in this
// package — the same posture the payload type itself ships with. Inbound
// enforcement is #1741's, outbound is #1897's and #2053's; nothing here checks
// anything, so a reader must not mistake a declared bound for a checked one.
//
// The three metadata bounds count BYTES (len(s)), not runes. The escape ceiling
// the arithmetic below rests on composes directly on bytes — a one-byte input
// costs at most six on the wire, while a multi-byte rune is emitted raw at four
// bytes or fewer, so six-per-input-byte is the ceiling either way — and a
// filename's real-world limit is a byte limit too.
//
// The metadata bounds matter for a reason the inbound leg does not show. A
// filename arrives attacker-chosen on the upload leg, is stored, and is echoed
// back daemon-authored on the retrieval leg: unbounded, a 100 KB filename makes
// the DAEMON's OWN outbound frame exceed the envelope cap and be dropped.
const (
	// MaxAttachmentChunkBytes bounds the RAW, pre-base64 bytes of one chunk's
	// Data so the marshalled application envelope stays under the v2
	// application-envelope cap of 65519 B (docs/protocol-mobile.md
	// § Application-envelope size cap: the 65535-byte Noise transport message
	// minus the 16-byte AEAD tag). The unit is raw bytes of Data — not base64
	// characters, not payload bytes, not envelope bytes — and no other reading
	// composes with Size, which is a raw file length.
	//
	// The arithmetic, budgeted against 65519 B. Every metadata figure is its
	// bound × 6, encoding/json's HTML-escaping ceiling: SetEscapeHTML is on by
	// default, so '<', '>', '&' and every control byte without a short escape
	// cost six bytes each (docs/protocol-mobile.md § tool_result works the same
	// multiplier out for a bounded text field, with a measured worst case).
	//
	//	envelope wrapper, every optional key present      194
	//	payload braces, keys, quotes, colons, commas      104
	//	index + total_chunks + size, 3 × 20                60
	//	sha256, 64 × 6                                    384
	//	attachment_id, MaxAttachmentIDBytes × 6           384
	//	filename, MaxAttachmentFilenameBytes × 6         1530
	//	mime_type, MaxAttachmentMimeTypeBytes × 6        1530
	//	                                        fixed    4186
	//	data at the bound, 4 × ceil(45000 / 3)          60000
	//	                                        frame   64186   (1333 B spare)
	//
	// The ceiling is floor((65519 − 4186) / 4) × 3 = 45999. 45000 sits below it
	// and is a multiple of 3, so base64 lands on exactly 60000 bytes with no
	// padding and the table is checkable by eye. The invariant is ENFORCED by
	// TestAttachmentChunkPayload_FitV2EnvelopeCap, which measures the real total
	// rather than trusting this table; if that test ever fails, LOWER this
	// constant — never raise the cap. The conservative constant is the belt; the
	// deterministic per-frame test is the suspenders.
	//
	// Exported, where the sibling measurement constant maxV2AppEnvelope is
	// deliberately not: the envelope cap is enforced by the transport, so
	// exporting that one would imply an enforcement this package does not
	// perform. This bound is a producer-side contract every client must obey to
	// chunk a file at all, so it has to be readable from outside.
	//
	// THE CROSS-CHECK. AttachmentChunkPayload's NEVER ALLOCATE FROM A CLAIM
	// block cross-checks a claimed TotalChunks against a claimed Size using this
	// number, and the division is raw-over-raw: Size is raw file bytes and this
	// is raw chunk bytes, neither is base64 and neither is envelope bytes. Only
	// the EQUALITY form bounds TotalChunks from above. A maximum chunk size
	// yields a LOWER bound on the honest chunk count, so softening the rule to
	// TotalChunks >= ceil(Size / bound) for tolerance caps nothing and leaves
	// the make([][]byte, TotalChunks) allocation attack open.
	//
	// So the receiver requires TotalChunks == max(1, ceil(Size / bound)) — the
	// form docs/protocol-mobile.md § Attachments, "Chunking (the sender's
	// obligation)", already publishes as the sender's own, adopted here rather
	// than invented. The max(1, …) is what resolves Size == 0, where a bare
	// ceil(0 / bound) is 0 against the documented TotalChunks >= 1. The
	// equality's price is accepted rather than softened away: it mandates that
	// every chunk but the last carry exactly this many raw bytes, so a client
	// chunking at its own buffer size sends a conforming-looking transfer this
	// receiver refuses — and since the published contract already obliges that
	// stride, no client that reads the contract is broken by it.
	// attachments.CheckDeclaration is the enforcement point; this constant fixes
	// the units and enforces nothing itself.
	MaxAttachmentChunkBytes = 45000

	// MaxAttachmentIDBytes bounds AttachmentID. It is a CEILING FOR THE CAP
	// ARITHMETIC, not the canonical shape, and the shape is no longer open:
	// attachments.EnsureDir picked conversations.ValidID's 36-character UUIDv4
	// form — the precedent AttachmentChunkPayload's doc had named — and #1895
	// publishes it in docs/protocol-mobile.md § Attachments as the rule a client
	// must obey. 64 sits comfortably above it, so this constant never constrained
	// the choice and does not describe it: a value passing this bound and failing
	// that shape is refused at storage.
	//
	// A LENGTH CEILING IS NOT A SAFETY PROPERTY. 64 bytes accommodates
	// "../../../../etc/passwd" several times over, so this constant does nothing
	// about the traversal hazard AttachmentChunkPayload's doc assigns to the
	// canonical-shape check that must run before the id becomes a path
	// component.
	MaxAttachmentIDBytes = 64

	// MaxAttachmentFilenameBytes bounds Filename at POSIX NAME_MAX, the
	// single-path-component limit on ext4 and APFS. Filename is documented as a
	// display string and a sanitiser input, never a path, so one component is
	// the right ceiling.
	MaxAttachmentFilenameBytes = 255

	// MaxAttachmentMimeTypeBytes bounds MimeType. RFC 6838 § 4.2 bounds a type
	// name and a subtype name at 127 characters each, so "type/subtype" is at
	// most 255.
	MaxAttachmentMimeTypeBytes = 255
)

// AttachmentChunkPayload is the body of an Envelope whose Type ==
// TypeAttachmentChunk (docs/protocol-mobile.md § Attachments, published by
// #1751). One slice of one attachment's bytes, plus the whole transfer's
// metadata repeated on every chunk. Wire vocabulary only — no producer, no
// consumer and no validator ship with it; TypeAttachmentChunk's block names the
// slice that builds each leg.
//
// BOTH DIRECTIONS RIDE THIS ONE TYPE: upload (client → daemon) and retrieval
// (daemon → client) share it. That is the settled mechanism that stops the two
// legs drifting once they are built months apart — there is no second shape to
// keep in step — and it is what makes the SECURITY block below load-bearing,
// because the trust level of every field depends on a direction the struct
// cannot report.
//
// Field contracts, which are the whole surface the blocked slices code against:
//
//   - AttachmentID identifies the attachment this chunk belongs to. Every chunk
//     of one transfer repeats it: it is the key an in-flight upload accumulates
//     under (#1741) and the identifier that later resolves to a path on the host
//     (#1743, #2037).
//   - Index is the 0-based position of this chunk within the attachment, in
//     [0, TotalChunks). It decides WHERE THE BYTES LAND, so a receiver addresses
//     by it rather than appending.
//   - TotalChunks is how many chunks the whole attachment splits into: >= 1, and
//     identical on every chunk of one transfer. A receiver knows the expected
//     count from the first chunk, which is why this frame needs no completion
//     peer of DebugBundleDonePayload. ">= 1" is a shape constraint and NOT a
//     sufficient bound — see the allocation rule below.
//   - Filename is a display string and a sanitiser input, never a path, on
//     either leg. Inbound it is the client's own name for the file; outbound it
//     is the sanitised single path component the bytes are stored under, since
//     attachments.Store hands the client's string to SanitizeFilename and keeps
//     only the result — never the client's own bytes.
//   - MimeType is a display hint and not a verified property of the bytes, on
//     either leg, and never something to dispatch on in a way that grants the
//     content privileges. Inbound it is the client's declared media type;
//     outbound it is sniffed from the stored bytes, because the declared value
//     is never stored at all — attachments.Store takes a directory, a filename
//     and the data, so there is nothing to echo.
//   - Size is the declared byte length of the WHOLE FILE, not of this chunk.
//     int64 mirrors os.FileInfo.Size(); a receiver checks it against the
//     assembled length.
//   - SHA256 is the lowercase hex sha256 of the WHOLE FILE, not of this chunk —
//     always 64 hex characters, the representation internal/devices' HashToken
//     already produces and documents.
//   - Data is this chunk's raw bytes. []byte auto-encodes as standard base64 via
//     encoding/json, exactly as DebugBundleChunkPayload.Data does. The per-chunk
//     size bound that keeps a frame inside the AEAD cap is #1753's, not a
//     constant here.
//
// No field carries omitempty, for SessionErrorPayload's recorded reason: every
// field is always present in both directions, so #1753's fixtures pin the full
// shape. An omitempty added later for tidiness would silently change the wire.
//
// Index and TotalChunks are plain int, not uint32, deliberately. Unsigned would
// make a negative index structurally impossible, but it would move that
// rejection into encoding/json's decode error and split ONE reject class
// ("out-of-range chunk index", #1741) across two layers and two wire codes.
// Plain int keeps the whole range check in one place — #1741's guard is
// Index < 0 || Index >= TotalChunks, table-drivable over negative and too-large
// alike — and it matches the package convention (DebugBundleChunkPayload.Seq,
// DebugBundleDonePayload.Total). The same holds for a negative Size.
//
// The fields are named Index / TotalChunks, not Seq / Total. Seq on the bundle
// stream carries a strict succession contract (the next chunk's Seq must equal
// the count of chunks already seen); this frame does not — a receiver addresses
// by index and rejects duplicates and out-of-range values rather than requiring
// succession — so naming the field Seq would import that contract by
// association. TotalChunks rather than Total because Size sits on the same
// struct, and a bare "total" beside a "size" reads as a byte count.
//
// There is NO conversation_id, and the omission is a security property rather
// than an oversight — the reasoning TypeRequestDebugBundle records: no field an
// attacker could use to select another session's data. An upload lands in the
// conversation the authenticated v2 session is already on, decided daemon-side
// by #1744 from session context, so a client cannot steer bytes into another
// conversation's directory by naming one. Retrieval's request verb does name a
// conversation — RequestAttachmentPayload below, declared by #2052 — but that is
// a different frame, and validating the id it names against the daemon's registry
// is #2054's problem.
//
// SECURITY: on the INBOUND leg every field is an unverified CLAIM, not a fact.
// OUTBOUND every field but one is daemon-authored, and none of the content
// metadata is a stored client string echoed back verbatim — nothing a client
// declares is kept,
// so the daemon derives what it did not store. That improves their PROVENANCE,
// not their TRUSTWORTHINESS, and the difference is the whole of this paragraph:
// docs/protocol-mobile.md § Attachments, "Trust and content hygiene", draws it
// the same way. One type carries both legs and nothing in it reports which
// direction a value came from, so a consumer must decide that from where it
// received the frame.
//
// AttachmentID IS THE EXCEPTION to "daemon-authored". Nothing daemon-side mints
// one — the client supplies it on every chunk of an upload,
// attachments.EnsureDir makes it a directory name, and retrieval echoes that
// same value back byte-identical (internal/relay's attachmentEnvelopes copies
// what StreamAttachment was handed). So it is client-chosen text on BOTH legs,
// constrained only by the canonical-shape check below, and that check therefore
// binds A RECEIVING CLIENT TOO: an id read off an outbound frame is no more
// path-safe than one read off an inbound one.
//
// PROVENANCE DOES NOT MAKE A VALUE SAFE TO ACT ON. A sniffed text/html is
// exactly as dangerous to render as a declared one, and a sanitised filename is
// still attacker-shaped text, so § Attachments keeps every client obligation
// binding on both legs: sanitise Filename before rendering it, never use it as a
// path or a filesystem name unsanitised, and NEVER DISPATCH ON MimeType in any
// way that grants the content privileges. What the derived MimeType does buy is
// narrower — it is drawn from a closed set of daemon-authored constants
// (attachmentEnvelopes carries the reasoning), so it can carry neither the
// log-injection nor the envelope-budget hazard an arbitrary declared string can.
// That is a property of the value and NOT a slackening of
// MaxAttachmentMimeTypeBytes, which budgets the inbound leg regardless.
//
// Filename, Size and SHA256 are the claims a reader expects. AttachmentID, Index
// and TotalChunks are claims too, and theirs is the falsification that actually
// costs something: TotalChunks is what a receiver would size an accumulator
// from, Index decides where bytes land (a duplicate or out-of-range value
// corrupts or escapes the buffer), and AttachmentID keys the in-flight upload
// and later resolves to a path on the host.
//
// NEVER ALLOCATE FROM A CLAIM. TotalChunks and Size are attacker-chosen
// integers: make([][]byte, TotalChunks) or make([]byte, Size) on a claimed
// TotalChunks of 2^31-1 is a multi-gigabyte allocation driven by a single ~60 KB
// frame, and it is the cheapest attack this frame offers. A receiver
// range-checks both BEFORE sizing anything, and the two numbers cross-check each
// other for free: given #1753's published per-chunk raw bound, TotalChunks must
// equal max(1, ceil(Size / bound)), and both must be within the receiver's own
// limits. The max(1, …) is not decoration — a bare ceil(Size / bound) is WRONG
// at Size == 0, where it demands 0 chunks against the documented
// TotalChunks >= 1. That check is available from the FIRST chunk, before a
// single byte is accumulated, and attachments.CheckDeclaration is where it runs.
//
// THE RULE DOES NOT LAPSE OUTBOUND, where the two integers are daemon-authored:
// § Attachments obliges a client to bound what it allocates from a received Size
// and TotalChunks against its own memory budget and to refuse a transfer larger
// than it can hold rather than attempt it. The daemon is trusted there, but a
// fixed-budget client still has a budget.
//
// AttachmentID is validated for canonical shape BEFORE it is used as a path
// component. It resolves to a file on the host in #1743 and #2037, and a
// client-chosen id reaching filepath.Join unvalidated is a traversal;
// conversations.ValidID is the existing canonical-shape precedent, and the
// deterministic check lands in #1741 / #1743. The id is also NOT a capability:
// not secret, not unguessable, and never the only thing standing between a
// caller and a file.
//
// Data is CONTENT-BEARING and NEVER LOGGED — a stronger rule than the one
// DebugBundleChunkPayload carries, because those are daemon-authored diagnostics
// and these are a user's own private file bytes. Filename gets the same
// treatment for two independent reasons: a filename is often private in itself,
// and a client-supplied string in a line-oriented log is a log-injection shape.
// Log the attachment id, the index and the total; never the bytes, and never a
// raw filename. #1744 enforces it. SANITISING DOES NOT LIFT THE RULE, so the
// retrieval leg's filename is no more loggable than the upload leg's:
// attachments.SanitizeFilename's own doc block records that its output can forge
// no log line but that the privacy reason stands untouched.
//
// SHA256 is INTEGRITY, NOT AUTHENTICITY. The same party supplies the bytes and
// the digest, so a match proves the transfer was not corrupted and proves
// nothing about whether the content is safe. It must not become an access token
// either: content-addressed retrieval ("know the hash, fetch the blob") would
// promote a non-secret claim into a capability. The canonical form is lowercase
// hex compared for EXACT EQUALITY against a digest the receiver renders the same
// way — a case-insensitive or prefix comparison is a hole, and a comparison that
// rejects an uppercase-sending client is an availability bug.
type AttachmentChunkPayload struct {
	AttachmentID string `json:"attachment_id"` // the transfer this chunk belongs to; repeated on every chunk
	Index        int    `json:"index"`         // 0-based position in [0, TotalChunks); decides where the bytes land
	TotalChunks  int    `json:"total_chunks"`  // chunk count for the whole attachment, >= 1, identical on every chunk
	Filename     string `json:"filename"`      // inbound the client's own name, outbound the sanitised component the bytes are stored under; display + sanitiser input, never a path
	MimeType     string `json:"mime_type"`     // inbound the client's declared type, outbound sniffed from the stored bytes; a hint, never a verified property
	Size         int64  `json:"size"`          // declared byte length of the WHOLE file, not of this chunk
	SHA256       string `json:"sha256"`        // lowercase hex sha256 of the WHOLE file; always 64 hex characters
	Data         []byte `json:"data"`          // this chunk's raw bytes; base64 on the wire, content-bearing, never logged
}

// AttachmentStoredPayload is the body of an Envelope whose Type ==
// TypeAttachmentStored (docs/protocol-mobile.md § Attachments, published by
// #1895). The upload leg's success reply: the transfer completed, its claims were
// checked, and the bytes are on the host addressable by this id. Wire vocabulary
// only — nothing constructs, decodes or validates it here; #1897 emits it.
//
// ONE DIRECTION ONLY, binary → phone, which is the whole difference from the
// frame it answers. AttachmentChunkPayload rides both legs and nothing in it
// reports which direction a value came from, so its SECURITY block has to make a
// consumer decide trust from where the frame arrived. Here there is nothing to
// decide: every field is daemon-asserted, and § Attachments publishes a
// provenance column saying so.
//
// ONE FIELD, and the id is the CLIENT'S OWN, echoed back. Nothing daemon-side
// mints an attachment id — the client supplies it on every chunk,
// attachments.EnsureDir validates its shape before it becomes a path component,
// and storage keys by it. So "tie the reply to the upload" and "name the
// attachment that was stored" are the same value, carried once. A second
// daemon-side handle would be a new identifier with no minting site, no lifecycle
// and no consumer. The daemon asserts nothing here it did not verify.
//
// Correlation to the request rides the envelope's InReplyTo, not a field, which
// is why there is no request-id key: TypeAttachmentStored's block has that
// decision, and the sibling shape is SessionSettingsUpdatedPayload, which
// likewise carries only the id it confirms. That block also records WHICH chunk
// InReplyTo names — the one whose arrival completed the transfer, not the highest
// index — and therefore why this id is the match key a client can actually
// predict.
//
// WHAT IS DELIBERATELY ABSENT, since every omission here is a decision:
//
//   - No host path, no directory component and no on-disk filename. EnsureDir
//     returns a symlink-resolved absolute path and the natural implementation of
//     "name the attachment that was stored" reaches for it; docs/protocol-mobile.md
//     § Error codes already forbids attachment.storage_failed from carrying the
//     host path or the underlying filesystem error, either of which discloses the
//     daemon's layout, and a success frame leaking what the failure frame is
//     guarded against would undo that mitigation from the other side.
//     TestAttachmentStoredPayload_WireKeys pins the key set so this is checked
//     rather than reviewed.
//   - No stored filename in particular. attachments.SanitizeFilename's doc block
//     records that its result is neither unique nor an identifier — distinct
//     client names collide, and a case-insensitive host folds them further — so
//     echoing it would hand a client something it cannot rely on. Retrieval
//     addresses by attachment id — RequestAttachmentPayload names one, #2052 — and
//     the client already knows the name it sent. Keeping it out also keeps this frame clear of the NEVER LOGGED rule
//     AttachmentChunkPayload puts on Filename, SHA256 and Data: this payload
//     carries none of the three and is safe to log whole.
//   - No conversation_id, for AttachmentChunkPayload's reason: the upload landed
//     in the conversation the authenticated v2 session is already on, and a client
//     holding the transfer already knows it.
//   - No size, sha256 or total_chunks. The client sent all three and they were
//     checked against the assembled bytes before this frame can be emitted;
//     echoing them back confirms nothing a client could act on.
//
// No omitempty and no MarshalJSON. The field is always present in the one
// direction this frame travels, matching QuestionDismissedPayload; and there is
// no slice field, so there is no nil→[] normalisation to perform and
// QuestionShownPayload.MarshalJSON's backing-array data-race argument does not
// transfer. Do not add one by analogy.
//
// SECURITY: receiving this frame IS NOT A CAPABILITY. The id is not secret, not
// unguessable, and never the only thing standing between a caller and a file —
// AttachmentChunkPayload's doc states that, and CodeAttachmentNotFound's
// deliberate merge is what keeps retrieval from becoming a path-existence oracle.
// It is echoed to exactly the authenticated session that uploaded the bytes, so
// disclosure widens nothing. A decode of a truncated or hostile payload yields
// the zero value rather than an error, because every key is optional to
// encoding/json; the empty string is not a valid attachment id under any
// published shape, so a consumer matching on it resolves nothing. A client
// receiving an id it does not recognise ignores the frame — the reading
// QuestionDismissedPayload publishes for an unrecognised batch — and never
// presents bytes it did not upload.
type AttachmentStoredPayload struct {
	// AttachmentID is the attachment that was stored: the client's own id,
	// repeated on every chunk of the upload and echoed back here. A client
	// matches on it and concludes nothing when it does not recognise the value.
	// Its canonical shape is conversations.ValidID's, published by #1895 —
	// MaxAttachmentIDBytes is a ceiling for the cap arithmetic and not that
	// shape.
	AttachmentID string `json:"attachment_id"`
}

// RequestAttachmentPayload is the body of an Envelope whose Type ==
// TypeRequestAttachment (docs/protocol-mobile.md § Attachments, published by
// #2052). The frame a client sends to ask the daemon for a stored attachment.
// Wire vocabulary only — nothing constructs, decodes or validates it here; #2054
// answers it and #2053 streams the bytes back as AttachmentChunkPayload frames.
//
// ONE DIRECTION ONLY, phone → binary, and that is the whole difference from the
// frame it is answered with. AttachmentChunkPayload rides both legs and nothing in
// it reports which direction a value came from, so its SECURITY block has to make a
// consumer decide trust from where the frame arrived. Here there is nothing to
// decide: EVERY FIELD IS AN UNVERIFIED CLAIM, ALWAYS.
//
// TWO FIELDS, and no third. It names a conversation and an attachment; correlation
// to the answer rides the envelope's InReplyTo, which is why there is no request-id
// key — TypeAttachmentStored's block has that decision, and both terminals of this
// leg already correlate that way (the answering chunks, and the
// CodeAttachmentStreamAborted TypeError). The committed
// testdata/attachment_chunk_retrieval.json rides in_reply_to 91 and
// testdata/request_attachment.json is the envelope 91 it answers, so a request-id
// key added here would leave a landed fixture describing a different scheme.
// TestRequestAttachmentPayload_WireKeys pins the key set so this is checked rather
// than reviewed.
//
// WHY THERE IS A conversation_id HERE when AttachmentChunkPayload deliberately has
// none: an upload lands in the conversation the authenticated v2 session is already
// on, so naming one there would only let a client steer bytes into another
// conversation's directory; a retrieval has to be able to say which conversation's
// file it wants. § Attachments committed to that asymmetry before this type existed.
//
// THE CONVERSATION ID IS A LOOKUP KEY, NEVER A VALUE TRUSTED AS SENT, and this is
// the security property the whole frame rests on. It is validated against the
// daemon's own registry BEFORE IT REACHES A PATH JOIN — not merely before bytes go
// out — and NAMING A CONVERSATION IS NOT AUTHORIZATION. § Naming a message's
// attachments states the same rule for send_message's attachment_ids and says where
// the safety lives: "Nothing about the id's shape or its randomness does this work —
// confinement does." A reader who takes this field for a free-form selector has been
// handed exactly the capability the rest of that section spends pages denying. A
// client must not read "UUIDv4" as a claim of unguessability.
//
// BOTH IDS OBEY THE SAME SHAPE, the lowercase UUIDv4 § Attachments publishes under
// "The attachment_id shape" — 36 bytes, '-' at 8/13/18/23, '4' at 14, one of 89ab at
// 19 — which is conversations.ValidID's shape byte for byte. Lowercase is
// load-bearing rather than cosmetic: an id becomes a directory name, and a
// case-insensitive host (APFS by default) folds two ids into one directory
// otherwise. Containment follows from that shape and NEVER from a length ceiling,
// which is also why this type adds no Max* constant of its own: MaxAttachmentIDBytes
// exists for attachment_chunk's envelope arithmetic, a ceiling there was read as the
// shape once already, and a second one here would enforce nothing while inviting the
// same mistake.
//
// NOTHING HERE ENFORCES ANY OF IT. internal/protocol declares shapes and validates
// none, the posture both siblings ship with. The canonical-shape check on either id
// and the registry validation the published contract promises are #2054's, which
// owns the reject path and the CodeAttachmentNotFound code to answer with — a
// deliberately merged code whose message is static and never echoes the requested id
// or the resolved path, because two distinguishable answers would make this verb a
// path-existence oracle for a traversal probe.
//
// A HOSTILE OR TRUNCATED PAYLOAD DECODES TO THE ZERO VALUE, NOT TO AN ERROR, since
// every key is optional to encoding/json — AttachmentStoredPayload records the same
// property. The result is two empty strings, and the empty string is not a valid id
// under any published shape, so a consumer must resolve NOTHING from them. The
// failure this warns about is silent and specific: filepath.Join(dir, "", "") is dir,
// so a consumer that skips the shape check and joins the zero value addresses the
// conversation directory root rather than erroring. QuestionAnswerPayload's published
// obligation is the shape to follow — a decode failure is a rejected frame, never an
// empty-but-successful request — and discharging it is #2054's.
//
// NO omitempty AND NO MarshalJSON. Both fields are always present in the one
// direction this frame travels, so a decoder may rely on both;
// TestRequestAttachmentPayload_ZeroValue_KeysPresent is the pin, and an omitempty
// added later for tidiness would silently change the wire. There is no slice field,
// so QuestionShownPayload.MarshalJSON's nil→[] argument does not transfer.
//
// THE ALLOCATION HAZARD AttachmentChunkPayload WARNS ABOUT IS ABSENT BY SHAPE: there
// is no count and no length field here, so NEVER ALLOCATE FROM A CLAIM has nothing
// to bite on. Stated so the absence reads as a property of the shape rather than as
// an omission. This type declares no bound of its own either — the transport's
// envelope cap is the only byte limit, and a client learns any receiver limit by
// being rejected rather than by reading a figure published ahead of the code that
// enforces it.
//
// SECURITY: SENDING THIS FRAME IS NOT A CAPABILITY, and neither is receiving an
// answer to it. Authorization is pairing, enforced structurally at the Noise IK
// handshake, exactly as § Attachments records for both existing legs; there is no
// per-verb gate on this one and none is invented here. What bounds a paired but
// hostile client is confinement, not secrecy. Both fields are client-supplied strings
// and are LOGGABLE ONLY AFTER THEIR SHAPE IS VALIDATED — raw, either is the
// log-injection shape § Attachments already forbids for Filename. The payload carries
// no content-bearing bytes, so once validated it is safe to log whole. Nothing here
// is claude-authored and nothing here becomes prompt content, so § Security model's
// threat 1 does not land on this frame — the opposite of SendMessagePayload's
// AttachmentIDs, which does reach claude.
type RequestAttachmentPayload struct {
	// ConversationID names the conversation whose attachment is wanted. A lookup
	// key validated against the daemon's registry before it resolves anything, and
	// not authorization; the empty string names nothing and resolves nothing.
	ConversationID string `json:"conversation_id"`

	// AttachmentID names the attachment wanted within that conversation. The
	// client's own id, the one it repeated on every chunk of the upload; the empty
	// string is not a valid id under any published shape.
	AttachmentID string `json:"attachment_id"`
}
