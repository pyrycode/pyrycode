package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- attachment-stream test helpers ---

const (
	// A canonical lowercase-UUIDv4 attachment id, the shape
	// docs/protocol-mobile.md § Attachments publishes and conversations.ValidID
	// enforces. The caller validates it; these tests only need it to be the
	// shape a real one has.
	testAttachmentID = "7c1d5e92-4a30-4b8f-9e21-6d4c3b0a8f55"
	// The envelope id of the committed request_attachment.json fixture, so the
	// correlation these tests assert is the one the protocol package's fixtures
	// already describe rather than an unrelated number.
	testRequestEnvID = uint64(91)
)

// writeAttachmentFile puts content on disk under name in a fresh temp directory
// and returns the path, standing in for what attachments.ResolvePath answers.
// 0o600 matches what Store creates.
func writeAttachmentFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write attachment fixture: %v", err)
	}
	return path
}

// decodeChunkPayloads decodes every envelope's payload, failing the test on a
// frame that is not an attachment_chunk or does not carry in_reply_to.
func decodeChunkPayloads(t *testing.T, envs []protocol.Envelope) []protocol.AttachmentChunkPayload {
	t.Helper()
	out := make([]protocol.AttachmentChunkPayload, 0, len(envs))
	for i, e := range envs {
		if e.Type != protocol.TypeAttachmentChunk {
			t.Fatalf("frame %d: Type = %q, want %q", i, e.Type, protocol.TypeAttachmentChunk)
		}
		if e.InReplyTo == nil {
			t.Fatalf("frame %d: InReplyTo is nil; every retrieval chunk answers a request", i)
		}
		var p protocol.AttachmentChunkPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("frame %d: decode payload: %v", i, err)
		}
		out = append(out, p)
	}
	return out
}

// reframe decodes every frame's payload, hands it to fn for mutation, and
// re-marshals. Error-case fixtures are built by mutating a real stream rather
// than hand-authoring one, so the size and digest of the happy path stay
// correct without being recomputed by hand in every row.
func reframe(t *testing.T, envs []protocol.Envelope, fn func(i int, p *protocol.AttachmentChunkPayload)) []protocol.Envelope {
	t.Helper()
	out := make([]protocol.Envelope, len(envs))
	for i, e := range envs {
		var p protocol.AttachmentChunkPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("frame %d: decode payload: %v", i, err)
		}
		fn(i, &p)
		payload, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("frame %d: re-marshal payload: %v", i, err)
		}
		out[i] = e
		out[i].Payload = payload
	}
	return out
}

// streamOf builds the frames for one transfer without touching the filesystem.
func streamOf(t *testing.T, blob []byte) []protocol.Envelope {
	t.Helper()
	envs, err := attachmentEnvelopes(testAttachmentID, "report.pdf", blob, testRequestEnvID)
	if err != nil {
		t.Fatalf("attachmentEnvelopes: %v", err)
	}
	return envs
}

func hexDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- attachmentEnvelopes (pure) ---

// TestAttachmentEnvelopes_ChunkShape pins AC#3's arithmetic across the sizes
// where it can go wrong. The bound is read from protocol.MaxAttachmentChunkBytes
// rather than a local 45000 for CheckDeclaration's stated reason: one place it
// can drift.
//
// The exact stride is the assertion that matters most and is the one a reader
// is most likely to think is stylistic. It is not: CheckDeclaration enforces
// total_chunks == max(1, ceil(size/bound)) as an EQUALITY, so a stream chunked
// at any other size is one this project's own receiver refuses.
func TestAttachmentEnvelopes_ChunkShape(t *testing.T) {
	t.Parallel()
	const bound = protocol.MaxAttachmentChunkBytes

	cases := []struct {
		name string
		size int
		want int
	}{
		{"zero-byte", 0, 1},
		{"one-byte", 1, 1},
		{"one-under-bound", bound - 1, 1},
		{"exactly-bound", bound, 1},
		{"one-over-bound", bound + 1, 2},
		{"exact-multiple", 3 * bound, 3},
		{"ragged-remainder", 3*bound + 4242, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			blob := patternBlob(tc.size)
			chunks := decodeChunkPayloads(t, streamOf(t, blob))

			if len(chunks) != tc.want {
				t.Fatalf("chunk count: got %d, want %d", len(chunks), tc.want)
			}
			var assembled []byte
			for i, p := range chunks {
				if p.Index != i {
					t.Errorf("chunk %d: Index = %d, want %d (0-based ascending)", i, p.Index, i)
				}
				// Identical on every chunk of one transfer — what lets a
				// receiver know the expected count from the first frame it sees.
				if p.TotalChunks != tc.want {
					t.Errorf("chunk %d: TotalChunks = %d, want %d on every chunk", i, p.TotalChunks, tc.want)
				}
				if i < len(chunks)-1 {
					if len(p.Data) != bound {
						t.Errorf("chunk %d: carries %d raw bytes, want exactly %d — every chunk but the last", i, len(p.Data), bound)
					}
				} else if want := tc.size - i*bound; len(p.Data) != want {
					t.Errorf("last chunk: carries %d raw bytes, want the remainder %d", len(p.Data), want)
				}
				assembled = append(assembled, p.Data...)
			}
			if !bytes.Equal(assembled, blob) {
				t.Errorf("concatenating the chunks in index order does not recover the file (%d bytes vs %d)", len(assembled), len(blob))
			}

			// The receiver-side equality this stride exists to satisfy, checked
			// against the project's own enforcement point rather than re-derived.
			if err := attachments.CheckDeclaration(chunks[0].TotalChunks, chunks[0].Size); err != nil {
				t.Errorf("the emitted declaration is one attachments.CheckDeclaration refuses: %v", err)
			}
		})
	}
}

// TestAttachmentEnvelopes_ZeroByteFile pins the max(1, …) half of AC#3 on its
// own, including the wire form. json.Marshal renders a nil []byte as null and an
// empty non-nil one as "", and `data` is a base64 STRING that is always present
// — attachment_chunk_zero.json's "data":null is the zero-VALUE pin (it also
// carries total_chunks 0), not a conforming zero-byte transfer. Slicing a nil
// blob yields a nil slice, so without an explicit normalisation this frame would
// carry null.
func TestAttachmentEnvelopes_ZeroByteFile(t *testing.T) {
	t.Parallel()
	envs := streamOf(t, nil)
	if len(envs) != 1 {
		t.Fatalf("chunk count for a zero-byte file: got %d, want exactly 1", len(envs))
	}
	chunks := decodeChunkPayloads(t, envs)
	if chunks[0].TotalChunks != 1 {
		t.Errorf("TotalChunks = %d, want 1", chunks[0].TotalChunks)
	}
	if len(chunks[0].Data) != 0 {
		t.Errorf("Data carries %d bytes, want 0", len(chunks[0].Data))
	}
	if chunks[0].Size != 0 {
		t.Errorf("Size = %d, want 0", chunks[0].Size)
	}
	if got := string(envs[0].Payload); !strings.Contains(got, `"data":""`) {
		t.Errorf("zero-byte chunk must carry \"data\":\"\" (a base64 string), got: %s", got)
	}
	if strings.Contains(string(envs[0].Payload), `"data":null`) {
		t.Errorf("zero-byte chunk carries \"data\":null; the published field is a string that is always present")
	}
}

// TestAttachmentEnvelopes_AllNineFieldsAndCorrelation pins #2053's AC#1, "all
// published fields, each correlated by in_reply_to" — nine of them since #2142
// added conversation_id. The correlation assertion is the one that distinguishes
// this stream from bundleEnvelopes, which leaves InReplyTo nil on every frame it
// builds — so the bundle stream is the wrong thing to copy here, and this is the
// test that says so.
//
// THE EMPTY conversation_id IS THE PRODUCER-SIDE HALF of #2142's contract, and it
// belongs here rather than only in internal/protocol's committed fixture. The
// field is meaningful on the upload leg only; retrieval emits it empty because
// in_reply_to already correlates the chunk to a request that named the
// conversation. attachmentEnvelopes needed no edit to satisfy that — its payload
// literal is keyed, so a new field arrives at its zero value — and an assertion
// that a producer emits nothing is exactly the kind that a keyed literal makes
// true by accident and a positional one would break silently. So it is asserted,
// not assumed.
func TestAttachmentEnvelopes_AllNineFieldsAndCorrelation(t *testing.T) {
	t.Parallel()
	blob := patternBlob(protocol.MaxAttachmentChunkBytes + 7) // 2 chunks
	envs := streamOf(t, blob)

	for i, e := range envs {
		if e.InReplyTo == nil {
			t.Fatalf("frame %d: InReplyTo is nil, want a pointer to %d", i, testRequestEnvID)
		}
		if *e.InReplyTo != testRequestEnvID {
			t.Errorf("frame %d: InReplyTo = %d, want %d", i, *e.InReplyTo, testRequestEnvID)
		}
		if e.EventID != nil {
			t.Errorf("frame %d: EventID must stay nil so the replay dedup guard is inert", i)
		}
		// All nine keys present on every chunk: no field carries omitempty, so
		// a decoder may rely on all nine in both directions.
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(e.Payload, &keys); err != nil {
			t.Fatalf("frame %d: decode payload keys: %v", i, err)
		}
		for _, k := range []string{"conversation_id", "attachment_id", "index", "total_chunks", "filename", "mime_type", "size", "sha256", "data"} {
			if _, ok := keys[k]; !ok {
				t.Errorf("frame %d: payload is missing the %q key", i, k)
			}
		}
		if len(keys) != 9 {
			t.Errorf("frame %d: payload carries %d keys, want exactly 9", i, len(keys))
		}
		if got := string(keys["conversation_id"]); got != `""` {
			t.Errorf("frame %d: conversation_id = %s, want an empty string — the retrieval leg emits it empty "+
				"and a receiver ignores it (in_reply_to already names the conversation)", i, got)
		}
	}

	chunks := decodeChunkPayloads(t, envs)
	for i, p := range chunks {
		if p.AttachmentID != testAttachmentID {
			t.Errorf("chunk %d: AttachmentID = %q, want %q on every chunk", i, p.AttachmentID, testAttachmentID)
		}
		if p.Size != int64(len(blob)) {
			t.Errorf("chunk %d: Size = %d, want the whole file's %d", i, p.Size, len(blob))
		}
		if want := hexDigest(blob); p.SHA256 != want {
			t.Errorf("chunk %d: SHA256 = %q, want the whole file's %q", i, p.SHA256, want)
		}
	}
}

// TestAttachmentEnvelopes_DerivedFromTheBytes pins AC#4's production half: the
// declared size, digest and media type come from the stored file, not from
// anything the uploading client declared. Intake.Receive calls Store(dir,
// chunk.Filename, data), which persists the bytes under the sanitised filename
// and nothing else — the declared mime_type is discarded outright, and the
// declared size and sha256 are checked at admission and then dropped — so there
// is no stored value to echo and the daemon derives all three.
//
// The name/content disagreement is the whole point: a file called photo.png
// holding PDF bytes is described as application/pdf. That is why the derivation
// is http.DetectContentType (content) rather than mime.TypeByExtension (name).
func TestAttachmentEnvelopes_DerivedFromTheBytes(t *testing.T) {
	t.Parallel()

	pdf := append([]byte("%PDF-1.4\n"), patternBlob(64)...)
	envs, err := attachmentEnvelopes(testAttachmentID, "photo.png", pdf, testRequestEnvID)
	if err != nil {
		t.Fatalf("attachmentEnvelopes: %v", err)
	}
	got := decodeChunkPayloads(t, envs)[0]

	if want := "application/pdf"; got.MimeType != want {
		t.Errorf("MimeType = %q, want %q — derived from the content, not from the .png name", got.MimeType, want)
	}
	if got.Size != int64(len(pdf)) {
		t.Errorf("Size = %d, want %d", got.Size, len(pdf))
	}
	if want := hexDigest(pdf); got.SHA256 != want {
		t.Errorf("SHA256 = %q, want %q", got.SHA256, want)
	}
	// Lowercase hex compared for exact equality is the published canonical form;
	// an uppercase rendering would be refused by a conforming receiver.
	if got.SHA256 != strings.ToLower(got.SHA256) {
		t.Errorf("SHA256 %q is not lowercase hex", got.SHA256)
	}

	// The zero-byte answer, pinned so it reads as decided rather than
	// accidental: http.DetectContentType is total and answers text/plain for an
	// empty input (no signature matches and no disqualifying byte is present).
	empty := decodeChunkPayloads(t, streamOf(t, nil))[0]
	if want := "text/plain; charset=utf-8"; empty.MimeType != want {
		t.Errorf("zero-byte MimeType = %q, want %q", empty.MimeType, want)
	}
}

// TestAttachmentEnvelopes_FrameWithinCapAtWorstCaseMetadata is the pure half of
// AC#2, measuring the marshalled envelope at the escape worst case the cap
// arithmetic budgets for: 255 filename bytes that each cost six on the wire,
// since encoding/json HTML-escapes '<' by default.
//
// Deliberately NOT the sanitised worst case. In practice the filename is
// SanitizeFilename's rendering and its allowlist cannot produce an escaping
// byte, so the budget is slack this leg never spends — but that holds only
// under StreamAttachment's precondition, and a cap test leaning on the
// precondition tests the caller rather than the frame.
func TestAttachmentEnvelopes_FrameWithinCapAtWorstCaseMetadata(t *testing.T) {
	t.Parallel()
	// The v2 application-envelope cap: the 65535-byte Noise transport message
	// minus the 16-byte AEAD tag (docs/protocol-mobile.md § Application-envelope
	// size cap). Spelled here because internal/protocol's own constant is
	// unexported.
	const appEnvelopeCap = maxNoisePayloadBytes - 16

	name := strings.Repeat("<", protocol.MaxAttachmentFilenameBytes)
	blob := patternBlob(2 * protocol.MaxAttachmentChunkBytes) // 2 chunks, both full
	envs, err := attachmentEnvelopes(testAttachmentID, name, blob, testRequestEnvID)
	if err != nil {
		t.Fatalf("attachmentEnvelopes: %v", err)
	}
	for i, e := range envs {
		out, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("frame %d: marshal envelope: %v", i, err)
		}
		if len(out) >= appEnvelopeCap {
			t.Errorf("frame %d: serialised envelope = %d B, want < %d B — LOWER protocol.MaxAttachmentChunkBytes, never raise the cap",
				i, len(out), appEnvelopeCap)
		}
	}
}

// --- ReassembleAttachment (pure) ---

// TestReassembleAttachment walks the receiver rules docs/protocol-mobile.md
// § Attachments publishes, including the one that is deliberately WEAKER than
// the bundle stream's: chunks may arrive in any order, because a receiver
// addresses by index and never appends. Copying ReassembleBundle's strict-
// succession Seq rule here would be the obvious thing and the wrong one.
func TestReassembleAttachment(t *testing.T) {
	t.Parallel()

	blob := patternBlob(2*protocol.MaxAttachmentChunkBytes + 11) // 3 chunks
	happy := streamOf(t, blob)
	single := streamOf(t, patternBlob(16))
	zero := streamOf(t, nil)

	reordered := []protocol.Envelope{happy[2], happy[0], happy[1]}

	// A frame answering this request but carrying another transfer's id: the
	// daemon answered the right ask with the wrong bytes. Rejected rather than
	// skipped — this is the failure the payload id catches and in_reply_to
	// cannot, which is why the two correlators are not redundant.
	wrongTransfer := reframe(t, happy, func(i int, p *protocol.AttachmentChunkPayload) {
		if i == 1 {
			p.AttachmentID = "3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67"
		}
	})

	cases := []struct {
		name    string
		frames  []protocol.Envelope
		want    []byte
		wantErr bool
	}{
		{name: "happy", frames: happy, want: blob},
		{name: "single-chunk", frames: single, want: patternBlob(16)},
		{name: "zero-byte", frames: zero, want: []byte{}},
		{name: "out-of-order-accepted", frames: reordered, want: blob},
		{
			name:   "interleaved-non-attachment-frame",
			frames: []protocol.Envelope{happy[0], otherFrame(), happy[1], happy[2]},
			want:   blob,
		},
		{name: "wrong-transfer-under-this-request", frames: wrongTransfer, wantErr: true},
		{
			name: "duplicate-index",
			frames: []protocol.Envelope{happy[0], happy[1], reframe(t, happy[2:], func(_ int, p *protocol.AttachmentChunkPayload) {
				p.Index = 1
			})[0]},
			wantErr: true,
		},
		{
			name: "index-out-of-range",
			frames: reframe(t, happy, func(i int, p *protocol.AttachmentChunkPayload) {
				if i == 2 {
					p.Index = 7
				}
			}),
			wantErr: true,
		},
		{
			name: "negative-index",
			frames: reframe(t, happy, func(i int, p *protocol.AttachmentChunkPayload) {
				if i == 2 {
					p.Index = -1
				}
			}),
			wantErr: true,
		},
		{
			name: "total-chunks-disagrees-mid-stream",
			frames: reframe(t, happy, func(i int, p *protocol.AttachmentChunkPayload) {
				if i == 1 {
					p.TotalChunks = 4
				}
			}),
			wantErr: true,
		},
		{
			name: "digest-disagrees-mid-stream",
			frames: reframe(t, happy, func(i int, p *protocol.AttachmentChunkPayload) {
				if i == 1 {
					p.SHA256 = hexDigest([]byte("something else"))
				}
			}),
			wantErr: true,
		},
		{name: "truncated-stream", frames: happy[:2], wantErr: true},
		{name: "no-frames-at-all", frames: nil, wantErr: true},
		{
			// The declaration cross-check runs BEFORE anything is sized, so a
			// hostile total_chunks/size pair never reaches an allocation.
			name: "declaration-the-receiver-refuses",
			frames: reframe(t, single, func(_ int, p *protocol.AttachmentChunkPayload) {
				p.TotalChunks = 1 << 20
			}),
			wantErr: true,
		},
		{
			// The bytes no longer match the digest that rides every chunk.
			name: "corrupted-bytes",
			frames: reframe(t, single, func(_ int, p *protocol.AttachmentChunkPayload) {
				p.Data = append([]byte("tampered"), p.Data...)
			}),
			wantErr: true,
		},
		{
			name: "malformed-payload",
			frames: []protocol.Envelope{{
				Type:      protocol.TypeAttachmentChunk,
				InReplyTo: func() *uint64 { v := testRequestEnvID; return &v }(),
				Payload:   json.RawMessage(`not-json`),
			}},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReassembleAttachment(tc.frames, testAttachmentID, testRequestEnvID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got nil error and %d bytes, want an error", len(got))
				}
				// Never partial or corrupted bytes on any failure path.
				if got != nil {
					t.Errorf("got %d bytes alongside the error, want nil", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("ReassembleAttachment: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("reassembled bytes differ (got %d, want %d)", len(got), len(tc.want))
			}
		})
	}
}

// TestReassembleAttachment_ConcurrentRetrievals pins the division § attachment_stored
// records and § Attachments relies on: in_reply_to says WHICH FRAME THIS ANSWERS
// and the payload id says WHICH TRANSFER IT BELONGS TO. Two retrievals
// interleaved on one connection need both, and this is the test that shows a
// receiver can separate them.
func TestReassembleAttachment_ConcurrentRetrievals(t *testing.T) {
	t.Parallel()

	const otherID = "3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67"
	const otherReq = uint64(92)

	blobA := patternBlob(protocol.MaxAttachmentChunkBytes + 5) // 2 chunks
	blobB := []byte("the other transfer's bytes")

	a, err := attachmentEnvelopes(testAttachmentID, "a.bin", blobA, testRequestEnvID)
	if err != nil {
		t.Fatalf("attachmentEnvelopes A: %v", err)
	}
	b, err := attachmentEnvelopes(otherID, "b.bin", blobB, otherReq)
	if err != nil {
		t.Fatalf("attachmentEnvelopes B: %v", err)
	}

	// Interleaved exactly as the push queue would deliver two concurrent streams.
	mixed := []protocol.Envelope{a[0], b[0], a[1]}

	gotA, err := ReassembleAttachment(mixed, testAttachmentID, testRequestEnvID)
	if err != nil {
		t.Fatalf("reassemble A out of the interleaved stream: %v", err)
	}
	if !bytes.Equal(gotA, blobA) {
		t.Errorf("transfer A: reassembled %d bytes, want %d", len(gotA), len(blobA))
	}
	gotB, err := ReassembleAttachment(mixed, otherID, otherReq)
	if err != nil {
		t.Fatalf("reassemble B out of the interleaved stream: %v", err)
	}
	if !bytes.Equal(gotB, blobB) {
		t.Errorf("transfer B: reassembled %q, want %q", gotB, blobB)
	}
}

// --- StreamAttachment (through a real open session) ---

// TestStreamAttachment_RoundTrip pins AC#1 end to end: the daemon emits the
// stored bytes as attachment_chunk frames and reassembling them in index order
// recovers the file byte for byte, through the real handshake, the real Push
// path and a real AEAD seal.
func TestStreamAttachment_RoundTrip(t *testing.T) {
	t.Parallel()
	sess := openBundleSession(t, silentLogger())

	blob := patternBlob(2*protocol.MaxAttachmentChunkBytes + 4242) // 3 chunks
	path := writeAttachmentFile(t, "quarterly_report.pdf", blob)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sess.mgr.StreamAttachment(ctx, v2TestConnID, testAttachmentID, path, testRequestEnvID); err != nil {
		t.Fatalf("StreamAttachment: %v", err)
	}

	envs := waitForEnvelopes(t, sess.rec, 1+3) // noise_resp + 3 chunks
	inner := decryptStreamFrames(t, sess, envs)
	if len(inner) != 3 {
		t.Fatalf("inner frames: got %d, want 3", len(inner))
	}
	// No completion frame, and none is coming: total_chunks rides every chunk.
	for i, e := range inner {
		if e.Type != protocol.TypeAttachmentChunk {
			t.Errorf("inner[%d].Type = %q, want %q — this stream has no terminal marker", i, e.Type, protocol.TypeAttachmentChunk)
		}
	}

	got, err := ReassembleAttachment(inner, testAttachmentID, testRequestEnvID)
	if err != nil {
		t.Fatalf("ReassembleAttachment: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("reassembled bytes differ (got %d, want %d)", len(got), len(blob))
	}
	// The filename the retrieval leg carries is the leaf of the resolved path —
	// SanitizeFilename's rendering, not the client's own bytes. This is the
	// production half of the § Attachments correction: there is no stored
	// client filename to echo verbatim.
	if got, want := decodeChunkPayloads(t, inner)[0].Filename, "quarterly_report.pdf"; got != want {
		t.Errorf("Filename = %q, want the resolved path's leaf %q", got, want)
	}
}

// TestStreamAttachment_EveryFrameWithinCap pins AC#2 on the real sealed frame:
// every emitted ciphertext stays within maxNoisePayloadBytes for a file large
// enough to need many chunks, measured rather than checked against the
// arithmetic that chose the bound.
//
// Belt-and-suspenders, different fabric: protocol.MaxAttachmentChunkBytes'
// conservative arithmetic is the belt, this deterministic per-frame measurement
// is the suspenders. If it ever fails, LOWER that constant — never raise the cap.
func TestStreamAttachment_EveryFrameWithinCap(t *testing.T) {
	t.Parallel()
	sess := openBundleSession(t, silentLogger())

	// A 255-byte filename: POSIX NAME_MAX, and the ceiling
	// protocol.MaxAttachmentFilenameBytes budgets for. The escape worst case is
	// measured by the pure sibling test above; a real on-disk name is used here
	// because this test streams from the filesystem.
	name := strings.Repeat("r", protocol.MaxAttachmentFilenameBytes-4) + ".bin"
	blob := patternBlob(4*protocol.MaxAttachmentChunkBytes + 1) // 5 chunks
	path := writeAttachmentFile(t, name, blob)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sess.mgr.StreamAttachment(ctx, v2TestConnID, testAttachmentID, path, testRequestEnvID); err != nil {
		t.Fatalf("StreamAttachment: %v", err)
	}

	envs := waitForEnvelopes(t, sess.rec, 1+5)
	for i, e := range envs[1:] {
		if got := len(decodeNoiseMsg(t, e)); got > maxNoisePayloadBytes {
			t.Errorf("stream frame %d ciphertext = %d bytes, want <= %d", i, got, maxNoisePayloadBytes)
		}
	}
}

// TestStreamAttachment_ZeroByteFile pins the max(1, …) rule through the real
// send path: bundleEnvelopes yields zero chunks for an empty blob, which is
// right there and wrong here, so a zero-byte retrieval must still put one frame
// on the wire or the client waits forever for a stream that never starts.
func TestStreamAttachment_ZeroByteFile(t *testing.T) {
	t.Parallel()
	sess := openBundleSession(t, silentLogger())

	path := writeAttachmentFile(t, "empty.txt", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sess.mgr.StreamAttachment(ctx, v2TestConnID, testAttachmentID, path, testRequestEnvID); err != nil {
		t.Fatalf("StreamAttachment: %v", err)
	}

	envs := waitForEnvelopes(t, sess.rec, 1+1)
	inner := decryptStreamFrames(t, sess, envs)
	if len(inner) != 1 {
		t.Fatalf("frames for a zero-byte file: got %d, want exactly 1", len(inner))
	}
	p := decodeChunkPayloads(t, inner)[0]
	if p.TotalChunks != 1 || len(p.Data) != 0 {
		t.Errorf("got TotalChunks=%d Data=%d bytes, want 1 and 0", p.TotalChunks, len(p.Data))
	}
	got, err := ReassembleAttachment(inner, testAttachmentID, testRequestEnvID)
	if err != nil {
		t.Fatalf("ReassembleAttachment: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("reassembled %d bytes, want 0", len(got))
	}
}

// TestStreamAttachment_NothingSensitiveLogged pins AC#5: no streamed byte, no
// filename and no host path reaches a log line at any level. The capturing
// handler is at LevelDebug — the lowest — because StreamAttachment logs at
// debug and an Info-only handler would make every absence check below vacuous.
func TestStreamAttachment_NothingSensitiveLogged(t *testing.T) {
	t.Parallel()
	lb := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(lb, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sess := openBundleSession(t, logger)

	const rawMarker = "PYRY-SECRET-" // 12 bytes; divisible by 3 => clean base64 repetition
	const filename = "zqxjv_private_medical_record.pdf"
	blob := bytes.Repeat([]byte(rawMarker), 5000) // 60000 bytes => 2 chunks
	path := writeAttachmentFile(t, filename, blob)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sess.mgr.StreamAttachment(ctx, v2TestConnID, testAttachmentID, path, testRequestEnvID); err != nil {
		t.Fatalf("StreamAttachment: %v", err)
	}
	// Wait for the WHOLE stream so any drain-side logging has also fired; a
	// short wait would let a later frame's log line land after the capture is
	// read and pass this test without having examined it.
	waitForEnvelopes(t, sess.rec, 1+2)

	logText := lb.String()
	// Non-vacuous: the debug capture must actually contain the stream line, else
	// the absence checks below prove nothing at all.
	if !strings.Contains(logText, "v2.attachment.stream") {
		t.Fatalf("capturing handler recorded no v2.attachment.stream line; log:\n%s", logText)
	}
	if strings.Contains(logText, rawMarker) {
		t.Errorf("raw file bytes leaked into a log record")
	}
	if b64 := base64.StdEncoding.EncodeToString([]byte(rawMarker)); strings.Contains(logText, b64) {
		t.Errorf("base64 file bytes leaked into a log record")
	}
	if strings.Contains(logText, filename) {
		t.Errorf("the filename leaked into a log record")
	}
	if strings.Contains(logText, filepath.Dir(path)) {
		t.Errorf("the host path leaked into a log record")
	}
	// The digest is banned alongside the bytes and the filename.
	if strings.Contains(logText, hexDigest(blob)) {
		t.Errorf("the file digest leaked into a log record")
	}
}

// TestStreamAttachment_UnreadableFile pins the error contract #2054 codes
// against: the failure is recognisable with errors.Is, and its text names no
// host path. os.ReadFile returns an *fs.PathError whose Error() prints the path,
// and ResolvePath's doc block bans logging the path it answered because its leaf
// is a sanitised client filename — so a caller that logged this error verbatim
// would defeat AC#5 through it.
func TestStreamAttachment_UnreadableFile(t *testing.T) {
	t.Parallel()
	sess := openBundleSession(t, silentLogger())

	const filename = "zqxjv_absent_attachment.bin"
	path := filepath.Join(t.TempDir(), filename)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := sess.mgr.StreamAttachment(ctx, v2TestConnID, testAttachmentID, path, testRequestEnvID)
	if err == nil {
		t.Fatalf("StreamAttachment on an absent file: got nil, want an error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(err, fs.ErrNotExist) = false; got %v", err)
	}
	if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), filename) {
		t.Errorf("the error names the host path or filename: %v", err)
	}
	if envs := sess.rec.snapshot(); len(envs) != 1 { // the handshake noise_resp only
		t.Errorf("frames emitted for an unreadable file: got %d, want 0", len(envs)-1)
	}
}

// TestStreamAttachment_ConnNotOpen pins the fail-closed contract: streaming to a
// conn that is not open returns ErrConnNotFound and emits no frames.
func TestStreamAttachment_ConnNotOpen(t *testing.T) {
	t.Parallel()
	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 1)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	path := writeAttachmentFile(t, "report.bin", patternBlob(128))
	err := mgr.StreamAttachment(context.Background(), "c-never-opened", testAttachmentID, path, testRequestEnvID)
	if !errors.Is(err, ErrConnNotFound) {
		t.Fatalf("StreamAttachment to unknown conn: got %v, want ErrConnNotFound", err)
	}
	if envs := rec.snapshot(); len(envs) != 0 {
		t.Errorf("frames emitted to unknown conn: got %d, want 0", len(envs))
	}
}
