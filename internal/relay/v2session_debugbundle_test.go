package relay

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/debugbundle"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #813 inbound request_debug_bundle → stream fixtures ---

// Distinctive sentinels seeded into the bundle fixture. AC #4 asserts neither
// ever appears on the raw (un-decrypted) wire or in any log line.
const (
	bundleRecordingSentinel = "SENTINEL-RECORDING-do-not-leak-4a7f2b9e"
	bundleLogSentinel       = "SENTINEL-LOGLINE-do-not-leak-9c3e1d55"
)

// Fixed archive member names, duplicated from internal/debugbundle (unexported
// there) so these tests assert the exact tar structure the bundle promises.
const (
	bundleMemberManifest  = "manifest.json"
	bundleMemberLogs      = "logs.txt"
	bundleMemberRecording = "recording.cast"
)

// fakeBundler is a relay-side test double for the DebugBundler seam: it records
// call count and returns an injectable (archive, err). The mutex guards the
// cross-goroutine access (the Run goroutine calls fn, the test goroutine reads
// callCount), mirroring fakeInterrupter.
type fakeBundler struct {
	mu      sync.Mutex
	calls   int
	archive []byte
	err     error
}

func (f *fakeBundler) fn() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.archive, f.err
}

func (f *fakeBundler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// setResult swaps what the next invocation returns, so one manager can serve a
// failing assembly followed by a succeeding one (the #1491 anti-lockout case).
func (f *fakeBundler) setResult(archive []byte, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archive, f.err = archive, err
}

// assembleFixture builds a real debug-bundle archive via the production
// debugbundle.Assemble from a temp recordings dir. A non-empty recordingSentinel
// writes exactly one .cast holding those bytes (capture-on); an empty
// recordingSentinel leaves the dir empty (capture-off). Returns the archive plus
// the recording basename ("" when absent). The archive is asserted to fit in a
// single debug_bundle_chunk so the streamed frame count is deterministically two
// (one chunk + one done).
func assembleFixture(t *testing.T, recordingSentinel string, logs []string) (archive []byte, recName string) {
	t.Helper()
	dir := t.TempDir()
	if recordingSentinel != "" {
		recName = "2026-01-02T03-04-05-sessABC-ok.cast"
		if err := os.WriteFile(filepath.Join(dir, recName), []byte(recordingSentinel), 0o600); err != nil {
			t.Fatalf("write fixture recording: %v", err)
		}
	}
	archive, _, err := debugbundle.Assemble(dir, logs)
	if err != nil {
		t.Fatalf("assemble fixture: %v", err)
	}
	if len(archive) >= bundleChunkBytes {
		t.Fatalf("fixture archive %d bytes >= chunk size %d; these tests assume a single chunk", len(archive), bundleChunkBytes)
	}
	return archive, recName
}

// bundleManagerFor stands up a v2 manager paired for v2TestToken with the given
// DebugBundler seam and returns it plus the frames channel, recorder, and the
// responder public key (for opening conns). bundler may be nil to exercise the
// unwired seam.
func bundleManagerFor(t *testing.T, bundler func() ([]byte, error), logger *slog.Logger) (mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub []byte) {
	t.Helper()
	var respPriv []byte
	respPriv, respPub = genV2Keypair(t)
	frames = make(chan protocol.RoutingEnvelope, 8)
	rec = &v2Recorder{}
	var stop func()
	mgr, stop = startManager(t, V2SessionConfig{
		Frames:       frames,
		Outbound:     rec.outbound,
		StaticPriv:   respPriv,
		Devices:      v2PairedRegistry(t, v2TestToken),
		ServerID:     v2TestServerID,
		Logger:       logger,
		DebugBundler: bundler,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

// waitNoiseMsgs polls until at least n noise_msg frames are recorded for connID
// or the deadline expires, returning them in recorded order. Bundle frames drain
// asynchronously (StreamBundle → Push → drainOnce), so a fixed-count wait — not a
// single snapshot — is the sync knob for the streamed reply.
func waitNoiseMsgs(t *testing.T, rec *v2Recorder, connID string, n int) []protocol.RoutingEnvelope {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msgs := noiseMsgsForConn(t, rec, connID)
		if len(msgs) >= n {
			return msgs
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitNoiseMsgs(%s): got %d, want >= %d", connID, len(noiseMsgsForConn(t, rec, connID)), n)
	return nil
}

// reassembleFromFrames decrypts the two captured bundle frames (chunk + done) in
// order under recv — each exactly once, since Decrypt advances the recv nonce —
// then reconstructs the archive via the production ReassembleBundle contract.
func reassembleFromFrames(t *testing.T, msgs []protocol.RoutingEnvelope, recv *noise.CipherState) []byte {
	t.Helper()
	chunk := decryptAppFrame(t, msgs[0], recv)
	done := decryptAppFrame(t, msgs[1], recv)
	blob, err := ReassembleBundle([]protocol.Envelope{chunk, done})
	if err != nil {
		t.Fatalf("reassemble bundle: %v", err)
	}
	return blob
}

// untarGz gunzips then untars blob into a member-name → body map, reading real
// member bodies so callers can byte-compare recording.cast and decode
// manifest.json (a garbage archive fails here rather than passing vacuously).
func untarGz(t *testing.T, blob []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("gunzip bundle: %v", err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar member %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = body
	}
	return out
}

func memberNames(members map[string][]byte) []string {
	out := make([]string, 0, len(members))
	for k := range members {
		out = append(out, k)
	}
	return out
}

// TestV2Session_DebugBundle_CaptureOn drives a paired handshake to open, feeds a
// bare request_debug_bundle, reassembles the streamed chunk+done frames, untars
// the archive, and asserts it carries the recording (with sentinel bytes), the
// logs, and a manifest marking the recording present (AC #1). A non-interactive
// paired conn is used deliberately: the bundle is authorized by pairing, not by
// the interactive capability.
func TestV2Session_DebugBundle_CaptureOn(t *testing.T) {
	t.Parallel()

	logs := []string{"first log line", "second log line"}
	archive, recName := assembleFixture(t, bundleRecordingSentinel, logs)
	fake := &fakeBundler{archive: archive}

	mgr, frames, rec, respPub := bundleManagerFor(t, fake.fn, silentLogger())

	const connID = "c-bundle-on"
	send, recv := openModalConn(t, mgr, frames, rec, respPub, connID, nil) // non-interactive, paired
	const reqID uint64 = 88
	frames <- sealAppFrameConn(t, send, connID, protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRequestDebugBundle,
		TS:   time.Now().UTC(),
	})

	msgs := waitNoiseMsgs(t, rec, connID, 2)
	members := untarGz(t, reassembleFromFrames(t, msgs, recv))

	if !bytes.Equal(members[bundleMemberRecording], []byte(bundleRecordingSentinel)) {
		t.Errorf("recording.cast = %q, want the sentinel %q", members[bundleMemberRecording], bundleRecordingSentinel)
	}
	if _, ok := members[bundleMemberLogs]; !ok {
		t.Errorf("logs.txt member missing; members present: %v", memberNames(members))
	}
	var m debugbundle.Manifest
	if err := json.Unmarshal(members[bundleMemberManifest], &m); err != nil {
		t.Fatalf("decode manifest.json: %v", err)
	}
	if !m.RecordingPresent {
		t.Error("manifest recording_present = false, want true (capture on)")
	}
	if m.RecordingName != recName {
		t.Errorf("manifest recording_name = %q, want %q", m.RecordingName, recName)
	}
	if m.RecordingBytes != int64(len(bundleRecordingSentinel)) {
		t.Errorf("manifest recording_bytes = %d, want %d", m.RecordingBytes, len(bundleRecordingSentinel))
	}
}

// TestV2Session_DebugBundle_CaptureOff proves that with no recording present the
// same request returns logs only and the archive marks the recording absent —
// structurally: there is no recording.cast member to withhold, not merely a flag
// (AC #2).
func TestV2Session_DebugBundle_CaptureOff(t *testing.T) {
	t.Parallel()

	logs := []string{"only-logs line one", "only-logs line two"}
	archive, _ := assembleFixture(t, "", logs) // empty recordings dir → capture off
	fake := &fakeBundler{archive: archive}

	mgr, frames, rec, respPub := bundleManagerFor(t, fake.fn, silentLogger())

	const connID = "c-bundle-off"
	send, recv := openModalConn(t, mgr, frames, rec, respPub, connID, nil)
	const reqID uint64 = 91
	frames <- sealAppFrameConn(t, send, connID, protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRequestDebugBundle,
		TS:   time.Now().UTC(),
	})

	msgs := waitNoiseMsgs(t, rec, connID, 2)
	members := untarGz(t, reassembleFromFrames(t, msgs, recv))

	if _, ok := members[bundleMemberRecording]; ok {
		t.Error("recording.cast member present with capture off; the recording must be withheld structurally")
	}
	if _, ok := members[bundleMemberLogs]; !ok {
		t.Errorf("logs.txt member missing; members present: %v", memberNames(members))
	}
	var m debugbundle.Manifest
	if err := json.Unmarshal(members[bundleMemberManifest], &m); err != nil {
		t.Fatalf("decode manifest.json: %v", err)
	}
	if m.RecordingPresent {
		t.Error("manifest recording_present = true, want false (capture off)")
	}
	if m.RecordingBytes != 0 {
		t.Errorf("manifest recording_bytes = %d, want 0", m.RecordingBytes)
	}
}

// TestV2Session_DebugBundle_UnpairedRefused proves an unpaired device cannot
// obtain a bundle: its handshake is refused at 4401 and it never reaches
// dispatchAppFrame, so a request smuggled through the closed session never
// invokes the DebugBundler (AC #3). The gate is the inherited Noise handshake,
// not a new authorization check — a paired barrier conn opens fine and the
// bundler stays wired, yet callCount stays zero.
func TestV2Session_DebugBundle_UnpairedRefused(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	// v2TestToken is paired (so the barrier conn opens); the unpaired conn below
	// presents a different, unknown token.
	reg := v2PairedRegistry(t, v2TestToken)
	fake := &fakeBundler{archive: []byte("must-never-be-streamed")}

	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:       frames,
		Outbound:     rec.outbound,
		StaticPriv:   respPriv,
		Devices:      reg,
		ServerID:     v2TestServerID,
		Logger:       silentLogger(),
		DebugBundler: fake.fn,
	})
	t.Cleanup(stop)

	// Drive an UNPAIRED handshake (unknown token): the manager emits noise_resp
	// then an AEAD-sealed auth.invalid_token error + 4401 close, and never
	// advances the conn to open.
	const unpaired = "c-unpaired"
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyData(t, "unknown-unpaired-token"))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, unpaired, protocol.TypeNoiseInit, initMsg)

	// Wait for the 4401 reject (the structural gate); this also guarantees the
	// noise_resp for the unpaired conn is recorded.
	waitForConnClose(t, rec, unpaired, uint16(StatusUnauthorized))

	// The Noise handshake itself completes even for an unpaired token, so the
	// phone holds send/recv cipher states. Attempt to smuggle a request through
	// the now-closed session — it must never reach the handler.
	respRaw := findNoiseRespForConn(t, rec, unpaired)
	_, initSend, _, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("ReadResp: %v", err)
	}
	frames <- sealAppFrameConn(t, initSend, unpaired, protocol.Envelope{
		ID:   5,
		Type: protocol.TypeRequestDebugBundle,
		TS:   time.Now().UTC(),
	})

	// Barrier: a PAIRED conn opens fine (the manager is healthy and the bundler
	// IS wired). Because Frames is one FIFO drained by the single Run goroutine,
	// the smuggled request was fully handled before this conn opened.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", nil)

	if got := fake.callCount(); got != 0 {
		t.Errorf("DebugBundler called %d times for an unpaired request; want 0 (the handshake gate makes the handler unreachable)", got)
	}
}

// TestV2Session_DebugBundle_NeverLogsContentEncryptedOnly pins the two strict
// security requirements (AC #4): bundle content never reaches any log, and it
// travels only as AEAD ciphertext. The archive + logs are seeded with sentinels;
// the flow runs under a capturing logger. The wire frames are asserted opaque
// (the inner envelope type is hidden until decryption), and the decrypted archive
// is asserted to actually carry the sentinels — so the no-log assertion is not
// vacuous.
func TestV2Session_DebugBundle_NeverLogsContentEncryptedOnly(t *testing.T) {
	t.Parallel()

	logs := []string{bundleLogSentinel, "trailing line"}
	archive, _ := assembleFixture(t, bundleRecordingSentinel, logs)
	fake := &fakeBundler{archive: archive}

	logger, logBuf := bufferLogger()
	mgr, frames, rec, respPub := bundleManagerFor(t, fake.fn, logger)

	const connID = "c-bundle-sec"
	send, recv := openModalConn(t, mgr, frames, rec, respPub, connID, nil)
	const reqID uint64 = 73
	frames <- sealAppFrameConn(t, send, connID, protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRequestDebugBundle,
		TS:   time.Now().UTC(),
	})

	msgs := waitNoiseMsgs(t, rec, connID, 2)

	// Encrypted-only: the wire frame carries opaque AEAD ciphertext, not a
	// readable plaintext envelope. The inner envelope Type ("debug_bundle_chunk")
	// is NOT gzip-compressed, so it would appear literally in the raw frame if the
	// bytes were not sealed — its absence is a gzip-independent encryption proof.
	// (decodeNoiseMsg is nonce-free base64, safe to call before decryptAppFrame.)
	for i, msg := range msgs[:2] {
		if bytes.Contains(msg.Frame, []byte(protocol.TypeDebugBundleChunk)) ||
			bytes.Contains(msg.Frame, []byte(protocol.TypeDebugBundleDone)) {
			t.Errorf("frame %d raw wire bytes expose the inner envelope type; expected AEAD ciphertext", i)
		}
		if bytes.Contains(msg.Frame, []byte(bundleRecordingSentinel)) || bytes.Contains(msg.Frame, []byte(bundleLogSentinel)) {
			t.Errorf("frame %d raw wire bytes contain a plaintext sentinel", i)
		}
		ct := decodeNoiseMsg(t, msg)
		var probe protocol.Envelope
		if json.Unmarshal(ct, &probe) == nil {
			t.Errorf("frame %d wire bytes decode as a plaintext envelope; expected opaque ciphertext", i)
		}
	}

	// Decrypt once each (advancing the recv nonce in order) and confirm the
	// sentinels really traveled — otherwise the no-log assertion below is vacuous.
	members := untarGz(t, reassembleFromFrames(t, msgs, recv))
	if !bytes.Equal(members[bundleMemberRecording], []byte(bundleRecordingSentinel)) {
		t.Fatalf("recording.cast = %q, want the sentinel (flow must carry it for a non-vacuous no-log check)", members[bundleMemberRecording])
	}
	if !bytes.Contains(members[bundleMemberLogs], []byte(bundleLogSentinel)) {
		t.Fatalf("logs.txt missing the log sentinel; no-log check would be vacuous")
	}

	// The served log line is written synchronously in the handler (before the
	// frames drain), so it is present now. Use it as the sync point, then assert
	// no bundle content leaked into any log line.
	waitForLogContains(t, logBuf, "v2.bundle.served")
	logs2 := logBuf.String()
	if strings.Contains(logs2, bundleRecordingSentinel) || strings.Contains(logs2, bundleLogSentinel) {
		t.Errorf("bundle content leaked into logs:\n%s", logs2)
	}
	if !strings.Contains(logs2, "bytes=") {
		t.Errorf("served log line missing byte count; got:\n%s", logs2)
	}
}

// TestV2Session_DebugBundle_ErrorReplies covers the two deterministic-error
// branches: a nil DebugBundler (unwired) and an assembly failure. Each yields
// exactly one sealed TypeError reply with the static message — never the
// assembly error text — correlated to the request id, and no bundle chunks. The
// assembly-error text (which could quote a recording path) never reaches the log.
func TestV2Session_DebugBundle_ErrorReplies(t *testing.T) {
	t.Parallel()

	const secretErr = "assemble boom: /private/pyry-recordings/2026-07-secret-sessXYZ.cast"
	cases := []struct {
		name       string
		nilBundler bool
		errText    string // non-empty ⇒ bundler returns this error; must NOT reach wire or log
	}{
		{name: "nil bundler replies unavailable", nilBundler: true},
		{name: "assembly error replies unavailable", errText: secretErr},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var bundler func() ([]byte, error)
			if !tc.nilBundler {
				errText := tc.errText
				bundler = func() ([]byte, error) { return nil, errors.New(errText) }
			}

			logger, logBuf := bufferLogger()
			mgr, frames, rec, respPub := bundleManagerFor(t, bundler, logger)

			const connID = "c-bundle-err"
			send, recv := openModalConn(t, mgr, frames, rec, respPub, connID, nil)
			const reqID uint64 = 64
			frames <- sealAppFrameConn(t, send, connID, protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeRequestDebugBundle,
				TS:   time.Now().UTC(),
			})

			// Poll for the reply rather than snapshotting: since #1491 the
			// assembly runs off Run and its failure comes back through
			// m.bundleReady, so the assembly-error reply is sealed one Run pass
			// after the request. (The nil-bundler branch still replies inline on
			// Run and satisfies this poll immediately; both branches share this
			// body.)
			msgs := waitNoiseMsgs(t, rec, connID, 1)

			// Barrier: a later paired conn opening proves Run drained every
			// earlier frame, so a second reply — if the change had grown one —
			// would already be recorded by the exact-count check below.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", nil)

			if got := noiseMsgsForConn(t, rec, connID); len(got) != 1 {
				t.Fatalf("got %d noise_msg for %s, want exactly 1 (a single error reply, no chunks)", len(got), connID)
			}
			reply := decryptAppFrame(t, msgs[0], recv)
			if reply.Type != protocol.TypeError {
				t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeError)
			}
			if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
				t.Errorf("reply InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
			}
			var p protocol.ErrorPayload
			if err := json.Unmarshal(reply.Payload, &p); err != nil {
				t.Fatalf("decode error payload: %v", err)
			}
			if p.Code != protocol.CodeServerBinaryOffline {
				t.Errorf("error Code = %q, want %q", p.Code, protocol.CodeServerBinaryOffline)
			}
			if p.Message != msgDebugBundleUnavailable {
				t.Errorf("error Message = %q, want the static %q", p.Message, msgDebugBundleUnavailable)
			}
			if !p.Retryable {
				t.Error("error Retryable = false, want true")
			}

			if tc.errText != "" {
				// The assembly error text (and any path it quotes) must never reach
				// the wire or the log.
				if strings.Contains(p.Message, "boom") || strings.Contains(p.Message, "recordings") {
					t.Errorf("error Message leaked the assembly error text: %q", p.Message)
				}
				if s := logBuf.String(); strings.Contains(s, "boom") || strings.Contains(s, "secret") || strings.Contains(s, ".cast") {
					t.Errorf("assembly error text leaked into logs:\n%s", s)
				}
			}
		})
	}
}

// --- #911 per-conn in-flight gate fixtures ---

// bundleGatedManagerFor stands up a paired v2 manager wired with the DebugBundler
// seam plus an independent transport-down probe, so the #911 gate tests can hold
// the push drain down to keep a bundle's chunks queued. Connected reads probeUp
// (probeUp=false ⇒ drainOnce holds the head un-popped/unsealed, so the queued
// bundle stays put); Outbound records UNCONDITIONALLY (independent of probeUp).
// While probeUp is false a busy-reject reply is dropped unsealed (#1526), so a
// test observes it through waitBusyRejectDrops rather than on the wire. This
// mirrors TestV2Session_Push_HoldGatedOnProbeNotSendError's
// independent probe/send wiring; bundleManagerFor omits Connected entirely, which
// would drain the queue instantly and make the busy window non-deterministic. The
// returned reconnect channel re-signals the drain on recovery (AC #4).
func bundleGatedManagerFor(t *testing.T, bundler func() ([]byte, error), logger *slog.Logger) (mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, probeUp *atomic.Bool, reconnect chan struct{}, respPub []byte) {
	t.Helper()
	var respPriv []byte
	respPriv, respPub = genV2Keypair(t)
	frames = make(chan protocol.RoutingEnvelope, 8)
	rec = &v2Recorder{}
	probeUp = &atomic.Bool{}
	probeUp.Store(true)
	reconnect = make(chan struct{}, 1)
	var stop func()
	mgr, stop = startManager(t, V2SessionConfig{
		Frames:       frames,
		Outbound:     rec.outbound,
		Connected:    probeUp.Load,
		Reconnect:    reconnect,
		StaticPriv:   respPriv,
		Devices:      v2PairedRegistry(t, v2TestToken),
		ServerID:     v2TestServerID,
		Logger:       logger,
		DebugBundler: bundler,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, probeUp, reconnect, respPub
}

// waitQueueLen polls until connID's push buffer reaches exactly want (a positive
// settle, so poll-until is deterministic and fast). Used to sync on a held bundle
// finishing its enqueue before the next request is fired.
func waitQueueLen(t *testing.T, mgr *V2SessionManager, connID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if queueLen(mgr, connID) == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("queue for %s did not reach depth %d (got %d)", connID, want, queueLen(mgr, connID))
}

// waitCallCount polls until the bundler has been invoked at least n times.
func waitCallCount(t *testing.T, f *fakeBundler, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.callCount() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("bundler callCount = %d, want >= %d", f.callCount(), n)
}

// decryptFrames decrypts msgs in recorded (nonce) order under recv — each exactly
// once, advancing the recv nonce — and returns the inner envelopes. Splitting the
// result and feeding slices to ReassembleBundle lets a caller round-trip an
// arbitrary multi-chunk bundle (reassembleFromFrames hardcodes a single chunk).
func decryptFrames(t *testing.T, msgs []protocol.RoutingEnvelope, recv *noise.CipherState) []protocol.Envelope {
	t.Helper()
	inner := make([]protocol.Envelope, len(msgs))
	for i, msg := range msgs {
		inner[i] = decryptAppFrame(t, msg, recv)
	}
	return inner
}

// assertBusyReject decrypts a captured noise_msg and asserts it is the
// deterministic busy-reject reply: a retryable TypeError correlated to reqID
// carrying the static server.binary_offline / msgDebugBundleUnavailable shape —
// byte-identical to the nil-bundler and assembly-error branches, so the phone
// cannot distinguish "busy" from "offline" (no queue-depth oracle).
func assertBusyReject(t *testing.T, msg protocol.RoutingEnvelope, recv *noise.CipherState, reqID uint64) {
	t.Helper()
	reply := decryptAppFrame(t, msg, recv)
	if reply.Type != protocol.TypeError {
		t.Fatalf("reject reply Type = %q, want %q", reply.Type, protocol.TypeError)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Errorf("reject reply InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
	}
	var p protocol.ErrorPayload
	if err := json.Unmarshal(reply.Payload, &p); err != nil {
		t.Fatalf("decode reject payload: %v", err)
	}
	if p.Code != protocol.CodeServerBinaryOffline {
		t.Errorf("reject Code = %q, want %q", p.Code, protocol.CodeServerBinaryOffline)
	}
	if p.Message != msgDebugBundleUnavailable {
		t.Errorf("reject Message = %q, want the static %q", p.Message, msgDebugBundleUnavailable)
	}
	if !p.Retryable {
		t.Error("reject Retryable = false, want true")
	}
}

// waitBusyRejectDrops polls until buf holds n transport-down drops of the
// debug-bundle error reply (#1526). With the probe held down, that drop line is
// the only observable edge of a busy reject: the reply is dropped unsealed, so
// it never reaches the wire. The reply's shape with the leg up is pinned by
// TestV2Session_DebugBundle_RejectsSecondWhileAssembling.
func waitBusyRejectDrops(t *testing.T, buf *syncLogBuffer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(dropLinesContaining(buf, "event=v2.bundle.err_dropped_transport_down")) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("busy-reject drops = %d, want >= %d", len(dropLinesContaining(buf, "event=v2.bundle.err_dropped_transport_down")), n)
}

func requestBundle(t *testing.T, frames chan protocol.RoutingEnvelope, send *noise.CipherState, connID string, reqID uint64) {
	t.Helper()
	frames <- sealAppFrameConn(t, send, connID, protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRequestDebugBundle,
		TS:   time.Now().UTC(),
	})
}

// TestV2Session_DebugBundle_RejectsSecondWhileQueued pins AC #1 + AC #2: while a
// conn's push queue still holds a prior bundle's chunks (held down by the drain),
// every further request_debug_bundle on that conn is rejected with the
// deterministic error reply BEFORE any assembly or enqueue — so the queued-frame
// count never grows across retries (per-retry stacking eliminated) and the
// bundler is never re-invoked. Run under -race.
func TestV2Session_DebugBundle_RejectsSecondWhileQueued(t *testing.T) {
	t.Parallel()

	// A multi-chunk archive so the queued bundle is visibly > 2 frames (3 chunks
	// + done). Any bytes round-trip through StreamBundle/ReassembleBundle; these
	// need not be a valid tar.gz for the queue mechanics under test.
	archive := patternBlob(3 * bundleChunkBytes)
	fake := &fakeBundler{archive: archive}
	wantFrames := wantChunks(len(archive)) + 1 // N chunks + done

	logger, logBuf := bufferLogger()
	mgr, frames, rec, probeUp, _, respPub := bundleGatedManagerFor(t, fake.fn, logger)

	const connID = "c-bundle-busy"
	send, _ := openModalConn(t, mgr, frames, rec, respPub, connID, nil)

	// Hold the drain down so the first bundle's frames stay queued (unsealed).
	probeUp.Store(false)

	// Request #1: assembles + streams a full bundle; the held drain keeps every
	// frame queued. Settle on the full queue depth.
	requestBundle(t, frames, send, connID, 101)
	waitQueueLen(t, mgr, connID, wantFrames)
	if got := fake.callCount(); got != 1 {
		t.Fatalf("bundler callCount after first request = %d, want 1", got)
	}

	// Requests #2 and #3 while the first bundle is still queued: each is rejected
	// before StreamBundle. The queued count must not grow, the bundler must not be
	// re-invoked, and each request takes the reject branch exactly once. With the
	// probe down the reject reply is dropped unsealed (#1526), so the drop line is
	// the observable edge and nothing reaches the wire for this conn.
	for i, reqID := range []uint64{202, 303} {
		requestBundle(t, frames, send, connID, reqID)
		waitBusyRejectDrops(t, logBuf, i+1)
		if got := len(noiseMsgsForConn(t, rec, connID)); got != 0 {
			t.Fatalf("noise_msgs after reject #%d = %d, want 0 (nothing sealed while down)", i+1, got)
		}

		if got := queueLen(mgr, connID); got != wantFrames {
			t.Fatalf("queue depth after reject #%d = %d, want %d (a rejected retry must not stack a second bundle)", i+1, got, wantFrames)
		}
		if got := fake.callCount(); got != 1 {
			t.Fatalf("bundler callCount after reject #%d = %d, want 1 (assembly must be skipped for a busy conn)", i+1, got)
		}
	}
}

// TestV2Session_DebugBundle_ServedAfterDrain pins AC #4: once a conn's prior
// bundle fully drains (all chunks PLUS the debug_bundle_done marker forwarded),
// the gate clears and a subsequent request_debug_bundle on that conn is served
// normally — a fresh assembly and a full second bundle over the wire. Run under
// -race.
func TestV2Session_DebugBundle_ServedAfterDrain(t *testing.T) {
	t.Parallel()

	archive := patternBlob(3 * bundleChunkBytes)
	fake := &fakeBundler{archive: archive}
	framesPerBundle := wantChunks(len(archive)) + 1

	mgr, frames, rec, probeUp, reconnect, respPub := bundleGatedManagerFor(t, fake.fn, silentLogger())

	const connID = "c-bundle-recover"
	send, recv := openModalConn(t, mgr, frames, rec, respPub, connID, nil)

	// Hold the drain down and request a first bundle; it stays fully queued.
	probeUp.Store(false)
	requestBundle(t, frames, send, connID, 111)
	waitQueueLen(t, mgr, connID, framesPerBundle)

	// Recover the transport and fire the reconnect edge (no further Push): the
	// held bundle drains FIFO to empty — chunks and the trailing done marker.
	probeUp.Store(true)
	reconnect <- struct{}{}
	assertQueueDrains(t, mgr, connID)

	// The prior bundle (including done) has fully drained, so the gate is clear:
	// a second request is served with a fresh assembly.
	requestBundle(t, frames, send, connID, 222)
	waitCallCount(t, fake, 2)

	// Both bundles reached the wire, in nonce order, and each round-trips to the
	// source archive — a non-vacuous proof the second request streamed a real,
	// fresh bundle (not a reject).
	msgs := waitNoiseMsgs(t, rec, connID, 2*framesPerBundle)
	inner := decryptFrames(t, msgs, recv)
	first, err := ReassembleBundle(inner[:framesPerBundle])
	if err != nil {
		t.Fatalf("reassemble first bundle: %v", err)
	}
	second, err := ReassembleBundle(inner[framesPerBundle:])
	if err != nil {
		t.Fatalf("reassemble second bundle: %v", err)
	}
	if !bytes.Equal(first, archive) || !bytes.Equal(second, archive) {
		t.Fatalf("bundles did not both round-trip to the source archive (first=%d second=%d want=%d bytes)", len(first), len(second), len(archive))
	}
}

// TestV2Session_DebugBundle_PerConnIsolation pins AC #3: an in-flight bundle on
// conn A does not cause conn B's request_debug_bundle to be rejected — the gate
// scans only the requesting conn's own queue. B is served (a fresh assembly, its
// own queued bundle) while A is busy, and a repeat request on A IS rejected. Run
// under -race.
func TestV2Session_DebugBundle_PerConnIsolation(t *testing.T) {
	t.Parallel()

	archive := patternBlob(3 * bundleChunkBytes)
	fake := &fakeBundler{archive: archive}
	wantFrames := wantChunks(len(archive)) + 1

	logger, logBuf := bufferLogger()
	mgr, frames, rec, probeUp, _, respPub := bundleGatedManagerFor(t, fake.fn, logger)

	const connA = "c-bundle-A"
	const connB = "c-bundle-B"
	sendA, _ := openModalConn(t, mgr, frames, rec, respPub, connA, nil)
	sendB, _ := openModalConn(t, mgr, frames, rec, respPub, connB, nil)

	// Hold the drain down for both conns.
	probeUp.Store(false)

	// A requests a bundle → A's queue holds a full bundle (A is now busy).
	requestBundle(t, frames, sendA, connA, 11)
	waitQueueLen(t, mgr, connA, wantFrames)
	if got := fake.callCount(); got != 1 {
		t.Fatalf("callCount after A's request = %d, want 1", got)
	}

	// B requests a bundle while A is busy → B is SERVED, not rejected: the gate
	// scans B's own (empty) queue. Proven by a fresh assembly (callCount 1→2) and
	// B's queue filling to a full bundle.
	requestBundle(t, frames, sendB, connB, 22)
	waitQueueLen(t, mgr, connB, wantFrames)
	if got := fake.callCount(); got != 2 {
		t.Fatalf("callCount after B's request = %d, want 2 (B must be served, not rejected, while A is busy)", got)
	}

	// A repeat request on A IS rejected (A still busy): the reject branch runs
	// (its reply dropped unsealed while the probe is down, #1526), no fresh
	// assembly, no growth of A's queue. B's queue is untouched.
	requestBundle(t, frames, sendA, connA, 33)
	waitBusyRejectDrops(t, logBuf, 1)

	if got := fake.callCount(); got != 2 {
		t.Fatalf("callCount after A's repeat = %d, want 2 (A's repeat must be rejected, not assembled)", got)
	}
	if got := queueLen(mgr, connA); got != wantFrames {
		t.Fatalf("A queue depth after repeat = %d, want %d (no stacking on the busy conn)", got, wantFrames)
	}
	if got := queueLen(mgr, connB); got != wantFrames {
		t.Fatalf("B queue depth = %d, want %d (an in-flight bundle on A must not disturb B)", got, wantFrames)
	}
}

// --- #1505 byte-ceiling fixtures ---

// bundleCeilingPoll is the deadline for the near-ceiling bundle waits below.
// The 2-second sibling helpers are sized for the kilobyte fixtures every other
// test in this file uses; these two move ~32 MiB of payload through
// bundleEnvelopes, the push queue, and (in the under-ceiling case) a per-frame
// AEAD seal + base64 wrap on the Run goroutine, which is minutes-safe but not
// 2-seconds-safe under -race.
const bundleCeilingPoll = 90 * time.Second

// bundleChunksUnderCeiling returns the largest whole-chunk count whose
// StreamBundle payload total (Σ len(env.Payload) — what pushQueue.bytes
// measures) stays under pushQueueByteCeiling. Derived by measuring one REAL
// full chunk rather than hardcoding a number that would silently drift if
// bundleChunkBytes or the chunk payload shape moved; the 8 B allowance per
// chunk covers the seq field widening from one digit to four across the stream
// plus the trailing done marker.
func bundleChunksUnderCeiling(t *testing.T) int {
	t.Helper()
	envs, err := bundleEnvelopes(patternBlob(bundleChunkBytes))
	if err != nil {
		t.Fatalf("size one bundle chunk: %v", err)
	}
	return pushQueueByteCeiling / (len(envs[0].Payload) + 8)
}

// waitQueueLenBy is waitQueueLen with a caller-chosen deadline.
func waitQueueLenBy(t *testing.T, mgr *V2SessionManager, connID string, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if queueLen(mgr, connID) == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("queue for %s did not reach depth %d within %v (got %d)", connID, want, within, queueLen(mgr, connID))
}

// waitQueueDrainsBy is assertQueueDrains with a caller-chosen deadline.
func waitQueueDrainsBy(t *testing.T, mgr *V2SessionManager, connID string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if queueLen(mgr, connID) == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("queue for %s did not drain to empty within %v (depth %d)", connID, within, queueLen(mgr, connID))
}

// waitNoiseMsgsBy is waitNoiseMsgs with a caller-chosen deadline.
func waitNoiseMsgsBy(t *testing.T, rec *v2Recorder, connID string, n int, within time.Duration) []protocol.RoutingEnvelope {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		msgs := noiseMsgsForConn(t, rec, connID)
		if len(msgs) >= n {
			return msgs
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitNoiseMsgsBy(%s): got %d, want >= %d within %v", connID, len(noiseMsgsForConn(t, rec, connID)), n, within)
	return nil
}

// TestV2Session_DebugBundle_JustUnderCeilingStreams pins the admitted side of
// AC#5: an archive whose streamed frames land just under pushQueueByteCeiling
// is enqueued whole while the drain is held — no teardown, no truncation — and
// once the transport recovers it drains and round-trips to the source archive.
// The ceiling bounds the bundle path; it does not break it. Run under -race.
func TestV2Session_DebugBundle_JustUnderCeilingStreams(t *testing.T) {
	t.Parallel()

	archive := patternBlob(bundleChunksUnderCeiling(t) * bundleChunkBytes)
	fake := &fakeBundler{archive: archive}
	wantFrames := wantChunks(len(archive)) + 1 // N chunks + done

	mgr, frames, rec, probeUp, reconnect, respPub := bundleGatedManagerFor(t, fake.fn, silentLogger())

	const connID = "c-bundle-under-ceiling"
	send, recv := openModalConn(t, mgr, frames, rec, respPub, connID, nil)

	// Hold the drain down so the whole bundle piles up in the queue at once —
	// the worst case the ceiling is there to bound.
	probeUp.Store(false)
	requestBundle(t, frames, send, connID, 101)
	waitQueueLenBy(t, mgr, connID, wantFrames, bundleCeilingPoll)

	// Non-vacuity: the fixture really is NEAR the ceiling, not trivially under
	// it. A fixture that drifted small would pass every assertion below while
	// proving nothing about the boundary.
	retained := queueBytes(mgr, connID)
	if retained <= pushQueueByteCeiling*9/10 {
		t.Fatalf("retained %d B is only %d%% of the ceiling %d; the fixture drifted away from the boundary",
			retained, 100*retained/pushQueueByteCeiling, pushQueueByteCeiling)
	}
	if retained > pushQueueByteCeiling {
		t.Fatalf("retained %d B exceeds the ceiling %d; the fixture is on the wrong side of the boundary", retained, pushQueueByteCeiling)
	}

	// No teardown: the conn is still open and its queue is still present.
	waitConnOpen(t, mgr, connID)

	// Recover the transport and fire the reconnect edge: the held bundle drains
	// FIFO and reassembles to the exact source archive.
	probeUp.Store(true)
	reconnect <- struct{}{}
	waitQueueDrainsBy(t, mgr, connID, bundleCeilingPoll)

	msgs := waitNoiseMsgsBy(t, rec, connID, wantFrames, bundleCeilingPoll)
	got, err := ReassembleBundle(decryptFrames(t, msgs, recv))
	if err != nil {
		t.Fatalf("reassemble under-ceiling bundle: %v", err)
	}
	if !bytes.Equal(got, archive) {
		t.Fatalf("under-ceiling bundle did not round-trip (got %d bytes, want %d)", len(got), len(archive))
	}
}

// TestV2Session_DebugBundle_PastCeilingTearsDownConn pins the rejected side of
// AC#5, and makes the outcome a chosen-and-tested one rather than something a
// future operator discovers: debugbundle.Assemble caps nothing, so a large
// enough archive turns into more never-droppable control envelopes than
// pushQueueByteCeiling admits. The conn is torn down at StatusQueueOverflow and
// its queue freed, instead of retaining the whole archive until the idle sweep.
//
// Nothing is asserted about StreamBundle's return value: it returns the FIRST
// Push error, and which push first observes the teardown depends on Run's
// scheduling. Run under -race.
func TestV2Session_DebugBundle_PastCeilingTearsDownConn(t *testing.T) {
	t.Parallel()

	// Two whole chunks past the largest admissible bundle: comfortably over the
	// boundary while staying as cheap as possible to build.
	archive := patternBlob((bundleChunksUnderCeiling(t) + 2) * bundleChunkBytes)
	fake := &fakeBundler{archive: archive}

	mgr, frames, rec, probeUp, _, respPub := bundleGatedManagerFor(t, fake.fn, silentLogger())

	const connID = "c-bundle-past-ceiling"
	send, _ := openModalConn(t, mgr, frames, rec, respPub, connID, nil)

	probeUp.Store(false)
	requestBundle(t, frames, send, connID, 202)

	// The ceiling's outcome: a 4413 close with a nil frame (nothing sealed onto
	// a transport that is down), and the queue deleted rather than retained.
	closeEnv := waitForConnCloseBy(t, rec, connID, uint16(StatusQueueOverflow), bundleCeilingPoll)
	if closeEnv.Frame != nil {
		t.Errorf("close frame = %d bytes, want nil (the ceiling teardown seals nothing)", len(closeEnv.Frame))
	}
	if got := queueLen(mgr, connID); got != -1 {
		t.Fatalf("queue depth for %s = %d after the teardown, want -1 (deleted, every retained byte freed)", connID, got)
	}
}

// --- #1491 off-Run assembly fixtures ---

// blockingBundler is a DebugBundler double that parks INSIDE the seam, so a test
// can observe what the manager does while an assembly is provably in flight.
// Each invocation bumps the call count, signals entered, blocks until release is
// called, then returns the injectable (archive, err). The relay-side twin of
// blockingHandler, which does the same for an application handler.
//
// The DebugBundler seam takes no context, so a parked assembly cannot be
// cancelled from outside — every user MUST arrange for release to run at
// cleanup. Register that cleanup AFTER the manager's stop cleanup: cleanups run
// LIFO, so release then unparks the seam BEFORE stop waits for Run to exit. That
// ordering is what makes this fixture usable as a pre-fix RED probe — on a tree
// where the seam still runs inline on Run, a stop that ran first would wait
// forever on a Run parked in the seam, turning an expected failure into a hang.
type blockingBundler struct {
	entered chan struct{}
	rel     chan struct{}
	relOnce sync.Once

	mu      sync.Mutex
	calls   int
	archive []byte
	err     error
}

func newBlockingBundler(archive []byte, err error) *blockingBundler {
	return &blockingBundler{
		// Buffered: fn's send is non-blocking, so an unexpected second
		// invocation can never park before it reaches the release gate — the
		// call-count assertions, not a lost signal, are what catch it.
		entered: make(chan struct{}, 4),
		rel:     make(chan struct{}),
		archive: archive,
		err:     err,
	}
}

// fn is the DebugBundler seam value to wire into V2SessionConfig.
func (b *blockingBundler) fn() ([]byte, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.rel
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.archive, b.err
}

func (b *blockingBundler) release() { b.relOnce.Do(func() { close(b.rel) }) }

func (b *blockingBundler) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// waitEntered blocks until the seam has been entered — the sync point past which
// an assembly is in flight and the manager's behaviour during it is observable.
func (b *blockingBundler) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("DebugBundler seam was never entered")
	}
}

// TestV2Session_DebugBundle_AssemblyDoesNotStallRun pins AC #1: while one conn's
// bundle assembly is in flight, Run keeps servicing its other select arms. Three
// are exercised with the assembly parked inside the seam — another conn's
// application frame (m.cfg.Frames → the per-conn worker → m.appReply), the push
// drain (m.drainCh), and a manual rekey (m.manualRekey, which blocks on Run's
// reply) — and each completes without waiting on the assembly. Released at the
// end, the bundle still streams and round-trips, so the offload loses nothing.
//
// Against the pre-fix tree the seam runs inline on Run, so Run is parked inside
// one select arm for the whole assembly: none of the three arms advance and every
// wait below times out.
func TestV2Session_DebugBundle_AssemblyDoesNotStallRun(t *testing.T) {
	t.Parallel()

	const (
		connA = "c-bundle-stall-A" // its assembly blocks
		connB = "c-bundle-stall-B" // application frame — the Frames arm
		connC = "c-bundle-stall-C" // push target — the drain arm
		connD = "c-bundle-stall-D" // rekey target — the manualRekey arm
	)

	archive, _ := assembleFixture(t, bundleRecordingSentinel, []string{"during-assembly"})
	bundler := newBlockingBundler(archive, nil)

	respPriv, respPub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers: map[string]dispatch.Handler{
			protocol.TypeListConversations: prolificHandler(),
		},
		DebugBundler: bundler.fn,
	})
	t.Cleanup(stop)
	t.Cleanup(bundler.release) // LIFO: unpark the seam before stop awaits Run.

	sendA, recvA := openModalConn(t, mgr, frames, rec, respPub, connA, nil)
	sendB, recvB := openModalConn(t, mgr, frames, rec, respPub, connB, nil)
	_, recvC := openModalConn(t, mgr, frames, rec, respPub, connC, nil)
	_, recvD := openModalConn(t, mgr, frames, rec, respPub, connD, nil)

	// A requests a bundle; the seam parks inside the assembly. Every assertion
	// below runs with that assembly in flight.
	requestBundle(t, frames, sendA, connA, 501)
	bundler.waitEntered(t)

	// (1) The Frames arm: B's application frame is routed and its reply sealed.
	frames <- sealAppFrameConn(t, sendB, connB, protocol.Envelope{
		ID:      502,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"count":1}`),
	})
	bMsgs := waitForConnNoiseMsg(t, rec, connB, 1)
	if inner := decryptAppFrame(t, bMsgs[0], recvB); inner.Type != protocol.TypeConversations {
		t.Errorf("conn B reply Type = %q, want %q", inner.Type, protocol.TypeConversations)
	}

	// (2) The drain arm: a queued push to C is popped, sealed and forwarded.
	if err := mgr.Push(context.Background(), connC, buildMessageEnvelope(t, 503, "during-assembly")); err != nil {
		t.Fatalf("Push to %s: %v", connC, err)
	}
	cMsgs := waitForConnNoiseMsg(t, rec, connC, 1)
	if inner := decryptAppFrame(t, cMsgs[0], recvC); inner.Type != protocol.TypeMessage {
		t.Errorf("conn C push Type = %q, want %q", inner.Type, protocol.TypeMessage)
	}

	// (3) The manualRekey arm: Rekey blocks on Run's reply, which comes only
	// after the rekey_request is sealed and sent to D.
	rekeyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mgr.Rekey(rekeyCtx, connD); err != nil {
		t.Fatalf("Rekey %s during assembly: %v", connD, err)
	}
	dMsgs := waitForConnNoiseMsg(t, rec, connD, 1)
	if inner := decryptAppFrame(t, dMsgs[0], recvD); inner.Type != protocol.TypeRekeyRequest {
		t.Errorf("conn D emit Type = %q, want %q", inner.Type, protocol.TypeRekeyRequest)
	}

	// Non-vacuity: all three completed while the assembly was still parked, so
	// nothing has been sealed for A yet.
	if got := noiseMsgsForConn(t, rec, connA); len(got) != 0 {
		t.Fatalf("conn A got %d noise_msg while its assembly is still blocked, want 0", len(got))
	}
	if got := bundler.callCount(); got != 1 {
		t.Fatalf("bundler callCount = %d, want 1", got)
	}

	// Release: A's bundle streams and round-trips — the offload did not lose it.
	bundler.release()
	aMsgs := waitNoiseMsgs(t, rec, connA, 2)
	members := untarGz(t, reassembleFromFrames(t, aMsgs, recvA))
	if !bytes.Equal(members[bundleMemberRecording], []byte(bundleRecordingSentinel)) {
		t.Errorf("streamed recording.cast = %q, want the sentinel %q", members[bundleMemberRecording], bundleRecordingSentinel)
	}
}

// TestV2Session_DebugBundle_RejectsSecondWhileAssembling pins the marker half of
// AC #2 — the window the queue-derived gate cannot see. With the assembly parked
// off Run the conn's push queue is still EMPTY, so bundleInFlight reads false; a
// second request_debug_bundle must nonetheless be rejected with the same
// deterministic reply and must not start a second assembly. The queue-empty
// assertions are what make this non-vacuous: a fix relying on bundleInFlight
// alone fails here, and would otherwise admit two concurrent full-size archives.
func TestV2Session_DebugBundle_RejectsSecondWhileAssembling(t *testing.T) {
	t.Parallel()

	archive, _ := assembleFixture(t, bundleRecordingSentinel, []string{"gate-during-assembly"})
	bundler := newBlockingBundler(archive, nil)

	mgr, frames, rec, respPub := bundleManagerFor(t, bundler.fn, silentLogger())
	// bundleManagerFor already registered the manager's stop cleanup; this runs
	// first (LIFO) so the seam unparks before Run's exit is awaited.
	t.Cleanup(bundler.release)

	const connID = "c-bundle-assembling"
	send, recv := openModalConn(t, mgr, frames, rec, respPub, connID, nil)

	requestBundle(t, frames, send, connID, 601)
	bundler.waitEntered(t)

	// Non-vacuity: the queue-derived half of the gate is blind right now —
	// nothing has been enqueued, so only the explicit marker can reject below.
	if got := queueLen(mgr, connID); got != 0 {
		t.Fatalf("queue depth during assembly = %d, want 0 (the queue-derived gate must be blind here)", got)
	}
	if mgr.bundleInFlight(connID) {
		t.Fatal("bundleInFlight is true during assembly; the marker half of the gate would go untested")
	}

	// The second request is rejected deterministically and never reaches the seam.
	requestBundle(t, frames, send, connID, 602)
	msgs := waitNoiseMsgs(t, rec, connID, 1)
	assertBusyReject(t, msgs[0], recv, 602)
	if got := bundler.callCount(); got != 1 {
		t.Fatalf("bundler callCount = %d, want 1 (a rejected retry must not start a second assembly)", got)
	}

	// Release: exactly ONE bundle streams — the reject reply plus chunk + done.
	bundler.release()
	after := waitNoiseMsgs(t, rec, connID, 3)
	openModalConn(t, mgr, frames, rec, respPub, "c-bundle-assembling-barrier", nil)
	if got := noiseMsgsForConn(t, rec, connID); len(got) != 3 {
		t.Fatalf("conn %s got %d noise_msg, want exactly 3 (one reject + one bundle: chunk + done)", connID, len(got))
	}
	if got := bundler.callCount(); got != 1 {
		t.Fatalf("bundler callCount after release = %d, want 1", got)
	}
	// assertBusyReject consumed the recv nonce for after[0]; the bundle is the
	// next two frames, decrypted in order.
	blob, err := ReassembleBundle(decryptFrames(t, after[1:3], recv))
	if err != nil {
		t.Fatalf("reassemble the served bundle: %v", err)
	}
	if !bytes.Equal(blob, archive) {
		t.Fatalf("served bundle did not round-trip (got %d bytes, want %d)", len(blob), len(archive))
	}
}

// TestV2Session_DebugBundle_ServedAfterFailedAssembly pins AC #3 plus AC #2's
// anti-lockout tail. An assembly that fails OFF Run still yields exactly one
// deterministic error reply, correlated on InReplyTo, carrying only the static
// msgDebugBundleUnavailable — the assembly error text (which quotes a recording
// path) reaches neither the wire nor the log. And because the gate marker is
// cleared on the failure path too, the very next request on that conn is served
// with a fresh assembly: a missed clear would lock the conn out permanently.
func TestV2Session_DebugBundle_ServedAfterFailedAssembly(t *testing.T) {
	t.Parallel()

	const secretErr = "assemble boom: /private/pyry-recordings/2026-08-secret-sessQRS.cast"
	archive, _ := assembleFixture(t, bundleRecordingSentinel, []string{"after-failure"})
	fake := &fakeBundler{err: errors.New(secretErr)}

	logger, logBuf := bufferLogger()
	mgr, frames, rec, respPub := bundleManagerFor(t, fake.fn, logger)

	const connID = "c-bundle-after-fail"
	send, recv := openModalConn(t, mgr, frames, rec, respPub, connID, nil)

	const failID uint64 = 701
	requestBundle(t, frames, send, connID, failID)
	msgs := waitNoiseMsgs(t, rec, connID, 1)
	if bytes.Contains(msgs[0].Frame, []byte("boom")) || bytes.Contains(msgs[0].Frame, []byte(".cast")) {
		t.Errorf("reply wire bytes carry the assembly error text")
	}
	// Byte-identical to the busy-reject and nil-bundler shapes: the phone cannot
	// tell an assembly failure from either.
	assertBusyReject(t, msgs[0], recv, failID)

	// Barrier: exactly one reply, no chunks.
	openModalConn(t, mgr, frames, rec, respPub, "c-bundle-after-fail-barrier", nil)
	if got := noiseMsgsForConn(t, rec, connID); len(got) != 1 {
		t.Fatalf("got %d noise_msg for %s, want exactly 1 (a single error reply, no chunks)", len(got), connID)
	}

	waitForLogContains(t, logBuf, "v2.bundle.assemble_err")
	if s := logBuf.String(); strings.Contains(s, "boom") || strings.Contains(s, "secret") || strings.Contains(s, ".cast") {
		t.Errorf("assembly error text leaked into logs:\n%s", s)
	}

	// Anti-lockout: the next request is served with a FRESH assembly.
	fake.setResult(archive, nil)
	requestBundle(t, frames, send, connID, 702)
	waitCallCount(t, fake, 2)
	after := waitNoiseMsgs(t, rec, connID, 3)
	blob, err := ReassembleBundle(decryptFrames(t, after[1:3], recv))
	if err != nil {
		t.Fatalf("reassemble the post-failure bundle: %v", err)
	}
	members := untarGz(t, blob)
	if !bytes.Equal(members[bundleMemberRecording], []byte(bundleRecordingSentinel)) {
		t.Errorf("post-failure recording.cast = %q, want the sentinel %q", members[bundleMemberRecording], bundleRecordingSentinel)
	}
}
