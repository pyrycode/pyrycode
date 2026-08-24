package protocol

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
//     (#1743, #1746).
//   - Index is the 0-based position of this chunk within the attachment, in
//     [0, TotalChunks). It decides WHERE THE BYTES LAND, so a receiver addresses
//     by it rather than appending.
//   - TotalChunks is how many chunks the whole attachment splits into: >= 1, and
//     identical on every chunk of one transfer. A receiver knows the expected
//     count from the first chunk, which is why this frame needs no completion
//     peer of DebugBundleDonePayload. ">= 1" is a shape constraint and NOT a
//     sufficient bound — see the allocation rule below.
//   - Filename is the client's own name for the file: a display string and a
//     sanitiser input, never a path.
//   - MimeType is the client's declared media type: a display and dispatch hint,
//     not a verified property of the bytes.
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
// conversation (#1746), but that is a different frame and its validation is
// #1746's problem.
//
// SECURITY: on the INBOUND leg every field is an unverified CLAIM, not a fact;
// on the outbound leg the same fields are daemon-authored and trustworthy. One
// type carries both and nothing in it reports which direction a value came from,
// so a consumer must decide that from where it received the frame.
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
// equal ceil(Size / bound), and both must be within the receiver's own limits.
// That check is available from the FIRST chunk, before a single byte is
// accumulated. #1741 owns the enforcement; this block is what tells #1741 the
// check exists and is cheap.
//
// AttachmentID is validated for canonical shape BEFORE it is used as a path
// component. It resolves to a file on the host in #1743 and #1746, and a
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
// raw filename. #1744 enforces it.
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
	Filename     string `json:"filename"`      // the client's own name for the file; display + sanitiser input, never a path
	MimeType     string `json:"mime_type"`     // the client's declared media type; a hint, not a verified property
	Size         int64  `json:"size"`          // declared byte length of the WHOLE file, not of this chunk
	SHA256       string `json:"sha256"`        // lowercase hex sha256 of the WHOLE file; always 64 hex characters
	Data         []byte `json:"data"`          // this chunk's raw bytes; base64 on the wire, content-bearing, never logged
}
