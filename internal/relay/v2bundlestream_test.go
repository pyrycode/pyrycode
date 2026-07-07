package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- bundle-stream test helpers ---

// patternBlob returns n deterministic, varied bytes. The content is irrelevant
// to framing (chunk size and cap depend on length, not value); a deterministic
// pattern keeps round-trip failures reproducible.
func patternBlob(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + 7)
	}
	return b
}

// wantChunks mirrors bundleEnvelopes' ceil division so tests can predict the
// chunk count without hard-coding a number that drifts with bundleChunkBytes.
func wantChunks(blobLen int) int {
	return (blobLen + bundleChunkBytes - 1) / bundleChunkBytes
}

func chunkFrame(t *testing.T, seq int, data []byte) protocol.Envelope {
	t.Helper()
	p, err := json.Marshal(protocol.DebugBundleChunkPayload{Seq: seq, Data: data})
	if err != nil {
		t.Fatalf("marshal chunk payload: %v", err)
	}
	return protocol.Envelope{Type: protocol.TypeDebugBundleChunk, Payload: p}
}

func doneFrame(t *testing.T, total int) protocol.Envelope {
	t.Helper()
	p, err := json.Marshal(protocol.DebugBundleDonePayload{Total: total})
	if err != nil {
		t.Fatalf("marshal done payload: %v", err)
	}
	return protocol.Envelope{Type: protocol.TypeDebugBundleDone, Payload: p}
}

// otherFrame is a non-bundle envelope the transport may interleave into the
// stream (ReassembleBundle skips it).
func otherFrame() protocol.Envelope {
	return protocol.Envelope{Type: protocol.TypeMessage, Payload: json.RawMessage(`{}`)}
}

// openBundleSession drives a paired-device handshake to V2StateOpen with the
// supplied logger and returns the open session (cleanup registered). The rec's
// envs[0] is the handshake noise_resp; any later frames are the stream.
func openBundleSession(t *testing.T, logger *slog.Logger) *openSession {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     logger,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)
	return sess
}

// decryptStreamFrames decrypts every captured stream frame (all envs after the
// handshake noise_resp at index 0) in capture order under the phone's recv
// state and returns the inner bundle envelopes. A clean in-order decrypt is the
// nonce-integrity proof; decryptAppFrame t.Fatals on any AEAD failure.
func decryptStreamFrames(t *testing.T, sess *openSession, envs []protocol.RoutingEnvelope) []protocol.Envelope {
	t.Helper()
	inner := make([]protocol.Envelope, 0, len(envs)-1)
	for _, e := range envs[1:] {
		inner = append(inner, decryptAppFrame(t, e, sess.initRecv))
	}
	return inner
}

// --- ReassembleBundle (pure) ---

func TestReassembleBundle(t *testing.T) {
	t.Parallel()

	aa, bb, cc := []byte("aa"), []byte("bb"), []byte("cc")
	cases := []struct {
		name    string
		frames  []protocol.Envelope
		want    []byte
		wantErr bool
	}{
		{
			name:   "happy",
			frames: []protocol.Envelope{chunkFrame(t, 0, aa), chunkFrame(t, 1, bb), doneFrame(t, 2)},
			want:   []byte("aabb"),
		},
		{
			name:   "single-chunk",
			frames: []protocol.Envelope{chunkFrame(t, 0, aa), doneFrame(t, 1)},
			want:   []byte("aa"),
		},
		{
			name:   "empty-blob",
			frames: []protocol.Envelope{doneFrame(t, 0)},
			want:   nil,
		},
		{
			name:   "interleaved-non-bundle-frame",
			frames: []protocol.Envelope{chunkFrame(t, 0, aa), otherFrame(), chunkFrame(t, 1, bb), doneFrame(t, 2)},
			want:   []byte("aabb"),
		},
		{
			name:    "out-of-order",
			frames:  []protocol.Envelope{chunkFrame(t, 1, bb), chunkFrame(t, 0, aa), doneFrame(t, 2)},
			wantErr: true,
		},
		{
			name:    "gap",
			frames:  []protocol.Envelope{chunkFrame(t, 0, aa), chunkFrame(t, 2, cc), doneFrame(t, 3)},
			wantErr: true,
		},
		{
			name:    "duplicate-seq",
			frames:  []protocol.Envelope{chunkFrame(t, 0, aa), chunkFrame(t, 0, aa), doneFrame(t, 2)},
			wantErr: true,
		},
		{
			name:    "total-too-high",
			frames:  []protocol.Envelope{chunkFrame(t, 0, aa), chunkFrame(t, 1, bb), doneFrame(t, 3)},
			wantErr: true,
		},
		{
			name:    "total-too-low",
			frames:  []protocol.Envelope{chunkFrame(t, 0, aa), chunkFrame(t, 1, bb), doneFrame(t, 1)},
			wantErr: true,
		},
		{
			name:    "missing-completion-marker",
			frames:  []protocol.Envelope{chunkFrame(t, 0, aa), chunkFrame(t, 1, bb)},
			wantErr: true,
		},
		{
			name:    "malformed-chunk-payload",
			frames:  []protocol.Envelope{{Type: protocol.TypeDebugBundleChunk, Payload: json.RawMessage(`not-json`)}, doneFrame(t, 1)},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReassembleBundle(tc.frames)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (bytes=%q)", got)
				}
				if got != nil {
					t.Errorf("want nil bytes on failure, got %d bytes", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// --- bundleEnvelopes (pure) ---

func TestBundleEnvelopes_RoundTrip(t *testing.T) {
	t.Parallel()

	sizes := []int{0, 1, bundleChunkBytes - 1, bundleChunkBytes, bundleChunkBytes + 1, 3*bundleChunkBytes + 7}
	for _, size := range sizes {
		blob := patternBlob(size)
		envs, err := bundleEnvelopes(blob)
		if err != nil {
			t.Fatalf("size %d: bundleEnvelopes: %v", size, err)
		}
		n := wantChunks(size)
		if len(envs) != n+1 {
			t.Fatalf("size %d: got %d envelopes, want %d (chunks) + 1 (done)", size, len(envs), n)
		}
		for i := 0; i < n; i++ {
			if envs[i].Type != protocol.TypeDebugBundleChunk {
				t.Fatalf("size %d: envs[%d].Type = %q, want %q", size, i, envs[i].Type, protocol.TypeDebugBundleChunk)
			}
			var p protocol.DebugBundleChunkPayload
			if err := json.Unmarshal(envs[i].Payload, &p); err != nil {
				t.Fatalf("size %d: decode chunk %d: %v", size, i, err)
			}
			if p.Seq != i {
				t.Errorf("size %d: chunk %d Seq = %d, want %d", size, i, p.Seq, i)
			}
			if len(p.Data) > bundleChunkBytes {
				t.Errorf("size %d: chunk %d Data len = %d, want <= %d", size, i, len(p.Data), bundleChunkBytes)
			}
		}
		last := envs[n]
		if last.Type != protocol.TypeDebugBundleDone {
			t.Fatalf("size %d: last Type = %q, want %q", size, last.Type, protocol.TypeDebugBundleDone)
		}
		var d protocol.DebugBundleDonePayload
		if err := json.Unmarshal(last.Payload, &d); err != nil {
			t.Fatalf("size %d: decode done: %v", size, err)
		}
		if d.Total != n {
			t.Errorf("size %d: done Total = %d, want %d", size, d.Total, n)
		}
		got, err := ReassembleBundle(envs)
		if err != nil {
			t.Fatalf("size %d: ReassembleBundle: %v", size, err)
		}
		if !bytes.Equal(got, blob) {
			t.Errorf("size %d: reassembled bytes differ (got %d, want %d)", size, len(got), len(blob))
		}
	}
}

// --- StreamBundle (integration) ---

// TestStreamBundle_MultiChunk pins AC#1/#2: a blob larger than a single frame is
// delivered as multiple ordered chunks over the encrypted channel followed by a
// completion marker, and the decrypted chunks reassemble to exactly the input.
func TestStreamBundle_MultiChunk(t *testing.T) {
	t.Parallel()
	sess := openBundleSession(t, silentLogger())

	blob := patternBlob(3*bundleChunkBytes + 1) // 4 chunks (> a single frame)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sess.mgr.StreamBundle(ctx, v2TestConnID, blob); err != nil {
		t.Fatalf("StreamBundle: %v", err)
	}

	n := wantChunks(len(blob))
	want := 1 + n + 1 // noise_resp + chunks + done
	envs := waitForEnvelopes(t, sess.rec, want)
	if len(envs) != want {
		t.Fatalf("frames: got %d, want %d (noise_resp + %d chunks + done)", len(envs), want, n)
	}

	inner := decryptStreamFrames(t, sess, envs)
	got, err := ReassembleBundle(inner)
	if err != nil {
		t.Fatalf("ReassembleBundle: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("reassembled bytes differ (got %d, want %d)", len(got), len(blob))
	}

	last := inner[len(inner)-1]
	if last.Type != protocol.TypeDebugBundleDone {
		t.Fatalf("last inner Type = %q, want %q", last.Type, protocol.TypeDebugBundleDone)
	}
	var done protocol.DebugBundleDonePayload
	if err := json.Unmarshal(last.Payload, &done); err != nil {
		t.Fatalf("decode done: %v", err)
	}
	if done.Total != n {
		t.Errorf("done Total = %d, want %d", done.Total, n)
	}
}

// TestStreamBundle_EveryFrameWithinCap pins AC#1's frame-cap requirement: every
// sealed noise_msg ciphertext stays within maxNoisePayloadBytes. This is the
// deterministic net that enforces bundleChunkBytes' headroom.
func TestStreamBundle_EveryFrameWithinCap(t *testing.T) {
	t.Parallel()
	sess := openBundleSession(t, silentLogger())

	blob := patternBlob(3*bundleChunkBytes + 4242)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sess.mgr.StreamBundle(ctx, v2TestConnID, blob); err != nil {
		t.Fatalf("StreamBundle: %v", err)
	}

	n := wantChunks(len(blob))
	envs := waitForEnvelopes(t, sess.rec, 1+n+1)
	for i, e := range envs[1:] {
		if got := len(decodeNoiseMsg(t, e)); got > maxNoisePayloadBytes {
			t.Errorf("stream frame %d ciphertext = %d bytes, want <= %d", i, got, maxNoisePayloadBytes)
		}
	}
}

// TestStreamBundle_SingleFrame pins AC#3: a blob that fits within a single frame
// still terminates with the completion marker (a one-chunk stream is valid).
func TestStreamBundle_SingleFrame(t *testing.T) {
	t.Parallel()
	sess := openBundleSession(t, silentLogger())

	blob := patternBlob(bundleChunkBytes - 1) // fits one chunk
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sess.mgr.StreamBundle(ctx, v2TestConnID, blob); err != nil {
		t.Fatalf("StreamBundle: %v", err)
	}

	envs := waitForEnvelopes(t, sess.rec, 1+1+1) // noise_resp + 1 chunk + done
	if len(envs) != 3 {
		t.Fatalf("frames: got %d, want 3 (noise_resp + 1 chunk + done)", len(envs))
	}

	inner := decryptStreamFrames(t, sess, envs)
	if len(inner) != 2 {
		t.Fatalf("inner frames: got %d, want 2", len(inner))
	}
	if inner[0].Type != protocol.TypeDebugBundleChunk {
		t.Errorf("inner[0].Type = %q, want %q", inner[0].Type, protocol.TypeDebugBundleChunk)
	}
	if inner[1].Type != protocol.TypeDebugBundleDone {
		t.Errorf("inner[1].Type = %q, want %q", inner[1].Type, protocol.TypeDebugBundleDone)
	}
	got, err := ReassembleBundle(inner)
	if err != nil {
		t.Fatalf("ReassembleBundle: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("reassembled bytes differ (got %d, want %d)", len(got), len(blob))
	}
}

// TestStreamBundle_NoBytesLogged pins AC#4: no chunk or completion-marker
// emission logs the streamed bytes, in raw or base64 form. A recognizable
// marker is streamed under a debug-level capturing handler (the level matters —
// StreamBundle logs at debug; an Info-only handler would make this vacuous).
func TestStreamBundle_NoBytesLogged(t *testing.T) {
	t.Parallel()
	lb := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(lb, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sess := openBundleSession(t, logger)

	const rawMarker = "PYRY-SECRET-"              // 12 bytes; divisible by 3 => clean base64 repetition
	blob := bytes.Repeat([]byte(rawMarker), 5000) // 60000 bytes => 2 chunks
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sess.mgr.StreamBundle(ctx, v2TestConnID, blob); err != nil {
		t.Fatalf("StreamBundle: %v", err)
	}
	// Wait for the frames so any drain-side logging has also fired.
	waitForEnvelopes(t, sess.rec, 1+wantChunks(len(blob))+1)

	logText := lb.String()
	// Non-vacuous: the debug capture must actually contain the stream line, else
	// the absence checks below prove nothing.
	if !strings.Contains(logText, "v2.bundle.stream") {
		t.Fatalf("capturing handler recorded no v2.bundle.stream line; log:\n%s", logText)
	}
	if strings.Contains(logText, rawMarker) {
		t.Errorf("raw blob bytes leaked into a log record")
	}
	b64Marker := base64.StdEncoding.EncodeToString([]byte(rawMarker))
	if strings.Contains(logText, b64Marker) {
		t.Errorf("base64 blob bytes leaked into a log record")
	}
}

// TestStreamBundle_ConnNotOpen pins the fail-closed contract: streaming to a
// conn that is not open returns ErrConnNotFound and emits no frames.
func TestStreamBundle_ConnNotOpen(t *testing.T) {
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

	err := mgr.StreamBundle(context.Background(), "c-never-opened", patternBlob(3*bundleChunkBytes))
	if !errors.Is(err, ErrConnNotFound) {
		t.Fatalf("StreamBundle to unknown conn: got %v, want ErrConnNotFound", err)
	}
	if envs := rec.snapshot(); len(envs) != 0 {
		t.Errorf("frames emitted to unknown conn: got %d, want 0", len(envs))
	}
}
