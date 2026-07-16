//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_DebugBundle proves request_debug_bundle end-to-end over the
// encrypted v2 wire, not just at the internal/relay unit tier: the happy path
// streams + decodes a known archive as debug_bundle_chunk* + debug_bundle_done
// (AC-1), and the error path returns the FIXED deterministic unavailable reply
// while leaking neither a recording path nor the wrapped assemble error onto the
// wire or into the daemon logs (AC-2).
//
// The daemon's real DebugBundler is non-deterministic (it tees the whole log ring
// plus a timestamped recording), so both subtests wire the env-gated fake bundler
// seam (fakeDebugBundler in cmd/pyry): happy mode serves a fixed file's bytes as
// the "known archive"; error mode returns a path-quoting error the no-leak
// subtest asserts never surfaces. AC-3 (#911 in-flight gate) is covered
// deterministically in-package by internal/relay/v2session_debugbundle_test.go —
// forcing a stable "prior bundle undrained" window over a real websocket is racy,
// so per the AC's explicit defer clause the e2e omits it.
func TestRelayV2_DebugBundle(t *testing.T) {
	t.Run("stream_decode_known_archive", testV2DebugBundleStreamDecode)
	t.Run("error_path_no_leak", testV2DebugBundleErrorNoLeak)
}

// testV2DebugBundleStreamDecode wires the fake bundler to serve a known,
// multi-chunk archive, sends a bare request_debug_bundle, and reasserts the
// receiver contract from the wire: chunks arrive with 0-based contiguous Seq in
// arrival order, done.Total equals the count received, and concatenating the
// chunk payloads IN ARRIVAL ORDER (never sorted by Seq) reproduces the exact
// archive bytes (AC-1).
func testV2DebugBundleStreamDecode(t *testing.T) {
	// Known archive: > 48000 B (relay.bundleChunkBytes) so the stream is
	// multi-chunk (3 chunks + done at 120000 B). Fill with position-dependent
	// bytes so any chunk reorder changes the concatenation — the reassembly
	// assertion must not be maskable by a sort.
	const archiveLen = 120000
	known := make([]byte, archiveLen)
	for i := range known {
		known[i] = byte(i*7 + 3)
	}
	archivePath := filepath.Join(t.TempDir(), "known-bundle.bin")
	if err := os.WriteFile(archivePath, known, 0o600); err != nil {
		t.Fatalf("write known archive: %v", err)
	}

	_, phone, initSend, initRecv := bundleHarness(t,
		[]string{"PYRY_FAKE_DEBUG_BUNDLE_FILE=" + archivePath})

	const reqID uint64 = 88
	sendBundleRequest(t, phone, initSend, reqID)

	// Read frames in strict arrival order (each decrypt advances initRecv once),
	// classify by Type, and append chunk payloads in arrival order until done.
	// A missing chunk or missing done fails loudly: readInnerFrame Fatals on its
	// 3s timeout, which is the loop's termination guarantee.
	var chunks [][]byte
	var doneTotal int
	haveDone := false
	for !haveDone {
		frame := decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)
		switch frame.Type {
		case protocol.TypeDebugBundleChunk:
			var p protocol.DebugBundleChunkPayload
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				t.Fatalf("decode debug_bundle_chunk: %v", err)
			}
			// Non-maskable: Seq must equal the count already seen, checked in
			// arrival order BEFORE the append. An emission-order bug makes this
			// fail rather than being hidden by a later sort.
			if p.Seq != len(chunks) {
				t.Fatalf("chunk Seq = %d, want %d (0-based contiguous in arrival order)", p.Seq, len(chunks))
			}
			// Chunk/done are pushes, not replies — do not assert InReplyTo on them.
			if frame.InReplyTo != nil {
				t.Errorf("debug_bundle_chunk InReplyTo = %v, want nil (push, not reply)", *frame.InReplyTo)
			}
			chunks = append(chunks, p.Data)
		case protocol.TypeDebugBundleDone:
			var p protocol.DebugBundleDonePayload
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				t.Fatalf("decode debug_bundle_done: %v", err)
			}
			doneTotal = p.Total
			haveDone = true
		default:
			// No unsolicited frame is expected on an idle open conn; classify
			// after decrypt (keeping nonce lockstep) and tolerate a stray, as
			// ReassembleBundle does.
		}
	}

	if len(chunks) < 2 {
		t.Errorf("received %d chunks, want a multi-chunk stream (>1)", len(chunks))
	}
	if doneTotal != len(chunks) {
		t.Errorf("done.Total = %d, want %d (count of chunks received)", doneTotal, len(chunks))
	}
	if got := bytes.Join(chunks, nil); !bytes.Equal(got, known) {
		t.Errorf("reassembled bundle mismatch: got %d bytes, want %d (arrival-order concat must equal the known archive)",
			len(got), len(known))
	}
}

// testV2DebugBundleErrorNoLeak wires the fake bundler to fail with a distinctive
// recording-path sentinel, sends a bare request_debug_bundle, and asserts the
// reply is the FIXED deterministic TypeError (CodeServerBinaryOffline /
// "debug bundle unavailable" / retryable) AND that the sentinel appears in
// neither the reply frame nor the captured daemon logs (AC-2). Non-vacuous: a
// regression that logged the wrapped err or put dynamic text on the wire would
// surface the sentinel and fail.
func testV2DebugBundleErrorNoLeak(t *testing.T) {
	// A recording-path-shaped sentinel: what the real assembler could quote in a
	// read error, and exactly what must never reach the client or the logs.
	const sentinel = "/pyry-e2e-canary/recording-DO-NOT-LEAK-4f9c2a.cast"

	h, phone, initSend, initRecv := bundleHarness(t,
		[]string{"PYRY_FAKE_DEBUG_BUNDLE_ERR=" + sentinel})

	const reqID uint64 = 91
	sendBundleRequest(t, phone, initSend, reqID)

	// A non-nil fake that errors drives the assemble-error branch (the stronger
	// wrapped-err path). That branch sends exactly one TypeError reply and never
	// calls StreamBundle, so this single frame is the whole response.
	reply := decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)
	if reply.Type != protocol.TypeError {
		t.Fatalf("reply Type = %q, want %q (payload=%s)", reply.Type, protocol.TypeError, string(reply.Payload))
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
	}
	var ep protocol.ErrorPayload
	if err := json.Unmarshal(reply.Payload, &ep); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if ep.Code != protocol.CodeServerBinaryOffline {
		t.Errorf("error Code = %q, want %q", ep.Code, protocol.CodeServerBinaryOffline)
	}
	// msgDebugBundleUnavailable is unexported in internal/relay — hardcode the
	// literal the const holds.
	if ep.Message != "debug bundle unavailable" {
		t.Errorf("error Message = %q, want %q", ep.Message, "debug bundle unavailable")
	}
	if !ep.Retryable {
		t.Errorf("error Retryable = false, want true")
	}

	// The handler writes the assemble_err Warn (event only, no wrapped err)
	// BEFORE enqueuing this reply, so the log line is already on the daemon's
	// stderr fd. Poll to absorb os/exec pipe-copy lag: seeing the event confirms
	// the assemble-error branch ran and that log capture is live — which makes the
	// negative sentinel check below non-vacuous (the logs are not merely empty).
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(h.Stderr.String(), "v2.bundle.assemble_err") {
		if time.Now().After(deadline) {
			t.Fatalf("daemon never logged v2.bundle.assemble_err event; stderr:\n%s", h.Stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// No-leak (load-bearing, AC-2): the sentinel path must appear in neither the
	// decrypted reply frame NOR the captured daemon logs.
	if raw, err := json.Marshal(reply); err != nil {
		t.Fatalf("re-marshal reply for leak scan: %v", err)
	} else if bytes.Contains(raw, []byte(sentinel)) {
		t.Errorf("sentinel leaked into reply frame: %s", raw)
	}
	if logs := h.Stderr.String(); strings.Contains(logs, sentinel) {
		t.Errorf("sentinel leaked into daemon logs:\n%s", logs)
	}
}

// bundleHarness pairs a device, spawns a v2 daemon with the given extra env, and
// drives a Noise_IK handshake to an open, paired conn. It returns the harness
// (for daemon-log capture), the dialed phone, and the initiator cipher states
// (initSend seals phone→daemon, initRecv opens daemon→phone). The conn is
// non-interactive: the bundle is authorized by pairing, matching the unit tier's
// deliberate choice.
func bundleHarness(t *testing.T, extraEnv []string) (*Harness, *fakephone.Client, *noise.CipherState, *noise.CipherState) {
	t.Helper()
	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartInWithEnv(t, home,
		append([]string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"}, extraEnv...),
		"-pyry-relay="+fr.URL()+"/v2/server",
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeToOpenDaemon(t, phone, pubKey, payload.Token)
	return h, phone, initSend, initRecv
}

// sendBundleRequest seals and sends the bare request_debug_bundle frame — no
// payload, the daemon-global handler ignores it (mirrors TypeInterrupt).
func sendBundleRequest(t *testing.T, phone *fakephone.Client, initSend *noise.CipherState, reqID uint64) {
	t.Helper()
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRequestDebugBundle,
		TS:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal request envelope: %v", err)
	}
	ciphertext, err := initSend.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal request envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)
}
