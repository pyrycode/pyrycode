package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #845 inbound set_session_settings → SettingsUpdater fixtures ---

const settingsSessionID = "sess-abc-123"

// settingsCall records one SettingsUpdater.UpdateSettings invocation so a test can
// assert what the manager routed across the seam (the id and the presence
// pointers).
type settingsCall struct {
	sessionID string
	update    SettingsUpdate
}

// fakeSettingsUpdater is a relay-side test double for SettingsUpdater: it records
// every call and returns a programmable error. The mutex guards cross-goroutine
// access (the Run goroutine writes via UpdateSettings, the test goroutine reads
// via snapshot), mirroring fakeQueueRemover / fakeBundler.
type fakeSettingsUpdater struct {
	mu    sync.Mutex
	calls []settingsCall
	err   error
}

func (f *fakeSettingsUpdater) UpdateSettings(id string, u SettingsUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, settingsCall{sessionID: id, update: u})
	return f.err
}

func (f *fakeSettingsUpdater) snapshot() []settingsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]settingsCall(nil), f.calls...)
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// settingsManagerFor stands up a v2 manager paired for v2TestToken with the given
// SettingsUpdater seam (pass nil to exercise the unwired seam) and returns it plus
// the frames channel, recorder, and responder public key (for opening conns).
func settingsManagerFor(t *testing.T, updater SettingsUpdater, logger *slog.Logger) (mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub []byte) {
	t.Helper()
	var respPriv []byte
	respPriv, respPub = genV2Keypair(t)
	frames = make(chan protocol.RoutingEnvelope, 8)
	rec = &v2Recorder{}
	var stop func()
	mgr, stop = startManager(t, V2SessionConfig{
		Frames:          frames,
		Outbound:        rec.outbound,
		StaticPriv:      respPriv,
		Devices:         v2PairedRegistry(t, v2TestToken),
		ServerID:        v2TestServerID,
		Logger:          logger,
		SettingsUpdater: updater,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

// mustSettingsPayload marshals a SetSessionSettingsPayload into the raw JSON an
// envelope carries.
func mustSettingsPayload(t *testing.T, p protocol.SetSessionSettingsPayload) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal settings payload: %v", err)
	}
	return raw
}

// TestV2Session_SetSessionSettings_AppliesByCapability drives an inbound
// set_session_settings frame through the real Frames/Run loop for an interactive
// conn and asserts: the manager reaches UpdateSettings exactly once with the
// decoded presence pointers (any combination of model/effort/yolo, with omitted
// fields staying nil — AC #1), and emits exactly one session_settings_updated
// reply correlated to the request id whose payload echoes the session_id. A
// non-interactive conn is fully inert: no seam call and zero outbound frames
// (AC #6). A barrier conn opened after the request synchronises the single-FIFO
// Run loop, so the recorded calls/replies are final.
func TestV2Session_SetSessionSettings_AppliesByCapability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		caps       []string
		payload    protocol.SetSessionSettingsPayload
		wantCalled bool
		wantUpdate SettingsUpdate
	}{
		{
			name:       "interactive all three valid",
			caps:       []string{protocol.CapabilityInteractive},
			payload:    protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr("claude-opus-4-8"), Effort: strPtr("high"), YOLO: boolPtr(true)},
			wantCalled: true,
			wantUpdate: SettingsUpdate{Model: strPtr("claude-opus-4-8"), Effort: strPtr("high"), YOLO: boolPtr(true)},
		},
		{
			name:       "interactive only effort",
			caps:       []string{protocol.CapabilityInteractive},
			payload:    protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Effort: strPtr("medium")},
			wantCalled: true,
			wantUpdate: SettingsUpdate{Effort: strPtr("medium")},
		},
		{
			name:       "interactive only yolo true",
			caps:       []string{protocol.CapabilityInteractive},
			payload:    protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, YOLO: boolPtr(true)},
			wantCalled: true,
			wantUpdate: SettingsUpdate{YOLO: boolPtr(true)},
		},
		{
			name:       "interactive clear values (empty strings)",
			caps:       []string{protocol.CapabilityInteractive},
			payload:    protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr(""), Effort: strPtr("")},
			wantCalled: true,
			wantUpdate: SettingsUpdate{Model: strPtr(""), Effort: strPtr("")},
		},
		{
			name:       "interactive only permission mode",
			caps:       []string{protocol.CapabilityInteractive},
			payload:    protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, PermissionMode: strPtr("plan")},
			wantCalled: true,
			wantUpdate: SettingsUpdate{PermissionMode: strPtr("plan")},
		},
		{
			// A mode alongside model/effort is fine — those are orthogonal
			// settings. Only a mode alongside YOLO is refused, and that row lives
			// in the malformed table below (#1687).
			name:       "interactive permission mode with model and effort",
			caps:       []string{protocol.CapabilityInteractive},
			payload:    protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr("sonnet"), Effort: strPtr("low"), PermissionMode: strPtr("acceptEdits")},
			wantCalled: true,
			wantUpdate: SettingsUpdate{Model: strPtr("sonnet"), Effort: strPtr("low"), PermissionMode: strPtr("acceptEdits")},
		},
		{
			name:       "non-interactive is inert",
			caps:       nil,
			payload:    protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr("sonnet")},
			wantCalled: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeSettingsUpdater{}
			mgr, frames, rec, respPub := settingsManagerFor(t, fake, silentLogger())

			send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", tc.caps)
			const reqID uint64 = 55
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				ID:      reqID,
				Type:    protocol.TypeSetSessionSettings,
				TS:      time.Now().UTC(),
				Payload: mustSettingsPayload(t, tc.payload),
			})

			// Barrier: the request is enqueued before this conn's noise_init, so once
			// the barrier conn is open the request has been fully handled.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			got := fake.snapshot()
			if tc.wantCalled {
				if len(got) != 1 {
					t.Fatalf("UpdateSettings calls = %d, want 1", len(got))
				}
				if got[0].sessionID != settingsSessionID {
					t.Errorf("UpdateSettings sessionID = %q, want %q", got[0].sessionID, settingsSessionID)
				}
				if !reflect.DeepEqual(got[0].update, tc.wantUpdate) {
					t.Errorf("UpdateSettings update = %+v, want %+v", got[0].update, tc.wantUpdate)
				}
			} else if len(got) != 0 {
				t.Fatalf("UpdateSettings calls = %d, want 0 (non-interactive inert)", len(got))
			}

			msgs := noiseMsgsForConn(t, rec, "c-int")
			if !tc.wantCalled {
				if len(msgs) != 0 {
					t.Fatalf("non-interactive conn got %d app frame(s), want 0 (no reply)", len(msgs))
				}
				return
			}
			if len(msgs) != 1 {
				t.Fatalf("got %d app frame(s), want exactly 1 success reply", len(msgs))
			}
			reply := decryptAppFrame(t, msgs[0], recv)
			if reply.Type != protocol.TypeSessionSettingsUpdated {
				t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeSessionSettingsUpdated)
			}
			if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
				t.Errorf("reply InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
			}
			var up protocol.SessionSettingsUpdatedPayload
			if err := json.Unmarshal(reply.Payload, &up); err != nil {
				t.Fatalf("decode session_settings_updated payload: %v", err)
			}
			if up.SessionID != settingsSessionID {
				t.Errorf("reply session_id = %q, want %q", up.SessionID, settingsSessionID)
			}
		})
	}
}

// TestV2Session_SetSessionSettings_ErrorReplies covers the three deterministic
// failure branches: a nil seam (unwired), an unknown session id, and a persist
// failure. Each yields exactly one sealed TypeError reply with the fixed
// code/message correlated to the request id — never a success and never a chunk.
// For the persist-failure case a distinctive error sentinel proves the never-echo
// discipline: neither the reply message nor any log line quotes the seam error
// text (which could contain a registry path) (AC #3).
func TestV2Session_SetSessionSettings_ErrorReplies(t *testing.T) {
	t.Parallel()

	const persistErrSentinel = "persist boom: /var/lib/pyry/registry-secretXYZ.json"
	cases := []struct {
		name      string
		nilSeam   bool
		seamErr   error
		wantCode  string
		wantMsg   string
		wantRetry bool
	}{
		{name: "nil seam replies unavailable", nilSeam: true, wantCode: protocol.CodeServerBinaryOffline, wantMsg: msgSettingsUnavailable, wantRetry: true},
		{name: "unknown session id", seamErr: ErrSessionUnknown, wantCode: protocol.CodeSessionNotFound, wantMsg: msgSettingsNotFound, wantRetry: false},
		{name: "persist failure replies unavailable", seamErr: errors.New(persistErrSentinel), wantCode: protocol.CodeServerBinaryOffline, wantMsg: msgSettingsUnavailable, wantRetry: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var updater SettingsUpdater
			if !tc.nilSeam {
				updater = &fakeSettingsUpdater{err: tc.seamErr}
			}
			logger, logBuf := bufferLogger()
			mgr, frames, rec, respPub := settingsManagerFor(t, updater, logger)

			send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
			const reqID uint64 = 64
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				ID:      reqID,
				Type:    protocol.TypeSetSessionSettings,
				TS:      time.Now().UTC(),
				Payload: mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr("sonnet")}),
			})

			// Barrier: the error reply is forwarded synchronously (forwardEnvelope),
			// so once a later paired conn opens the reply is already recorded.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			msgs := noiseMsgsForConn(t, rec, "c-int")
			if len(msgs) != 1 {
				t.Fatalf("got %d app frame(s), want exactly 1 error reply", len(msgs))
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
			if p.Code != tc.wantCode {
				t.Errorf("error Code = %q, want %q", p.Code, tc.wantCode)
			}
			if p.Message != tc.wantMsg {
				t.Errorf("error Message = %q, want the static %q", p.Message, tc.wantMsg)
			}
			if p.Retryable != tc.wantRetry {
				t.Errorf("error Retryable = %v, want %v", p.Retryable, tc.wantRetry)
			}

			if tc.seamErr != nil && strings.Contains(tc.seamErr.Error(), "boom") {
				// The seam error text (and any path it quotes) must never reach the
				// wire or the log.
				if strings.Contains(p.Message, "boom") || strings.Contains(p.Message, "secret") {
					t.Errorf("error Message leaked the seam error text: %q", p.Message)
				}
				if s := logBuf.String(); strings.Contains(s, "boom") || strings.Contains(s, "secret") || strings.Contains(s, ".json") {
					t.Errorf("seam error text leaked into logs:\n%s", s)
				}
			}
		})
	}
}

// TestV2Session_SetSessionSettings_MalformedRejected proves the wire-boundary
// validation: a malformed payload (bad JSON / type mismatch) and an invalid
// model/effort value each yield a single protocol.malformed reply with the fixed
// message, and NEVER reach the seam — so no bad setting is persisted (AC #4/#5).
// A distinctive marker in the malformed body must not appear in the reply message
// or any log line (never-echo).
func TestV2Session_SetSessionSettings_MalformedRejected(t *testing.T) {
	t.Parallel()

	const marker = "MARKER-do-not-leak-9c3e1d55"
	longModel := strings.Repeat("a", 65)
	cases := []struct {
		name    string
		payload json.RawMessage
	}{
		{"yolo type mismatch", json.RawMessage(`{"session_id":"` + marker + `","yolo":"nope"}`)},
		{"payload is a json array", json.RawMessage(`[1,2,3]`)},
		{"invalid model leading dash", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr("--dangerously-skip-permissions")})},
		{"invalid model embedded space", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr("claude opus")})},
		{"invalid model control byte", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr("claude\x00opus")})},
		{"invalid model over length", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr(longModel)})},
		{"invalid effort ultra", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Effort: strPtr("ultra")})},

		// Permission mode (#1687). Every row here must leave the seam untouched:
		// a refused posture is one that never reached the registry, and so never
		// reached a spawn argv either.
		{"permission mode empty string", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, PermissionMode: strPtr("")})},
		{"permission mode unknown value", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, PermissionMode: strPtr("yolo")})},
		{"permission mode wrong case", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, PermissionMode: strPtr("Plan")})},
		// The escalation is refused on the mode field so bypass keeps exactly one
		// spelling (the yolo bit). This row is AC #4 in one line.
		{"permission mode bypassPermissions", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, PermissionMode: strPtr("bypassPermissions")})},
		// Both spellings of the posture in one frame: refused whichever way they
		// point, so neither can win over the other. The yolo:false row is the one
		// that proves this is the RELAY's rule and not an inherited one — the pool
		// validator downstream accepts that exact pair as consistent.
		{"permission mode with yolo true", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, PermissionMode: strPtr("plan"), YOLO: boolPtr(true)})},
		{"permission mode with yolo false", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, PermissionMode: strPtr("plan"), YOLO: boolPtr(false)})},
		// A garbage mode paired with a yolo is still refused: the conflict rule
		// does not depend on the mode being one this daemon recognises.
		{"unknown permission mode with yolo", mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, PermissionMode: strPtr("nonsense"), YOLO: boolPtr(true)})},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeSettingsUpdater{}
			logger, logBuf := bufferLogger()
			mgr, frames, rec, respPub := settingsManagerFor(t, fake, logger)

			send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
			const reqID uint64 = 77
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				ID:      reqID,
				Type:    protocol.TypeSetSessionSettings,
				TS:      time.Now().UTC(),
				Payload: tc.payload,
			})

			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			if got := fake.snapshot(); len(got) != 0 {
				t.Fatalf("UpdateSettings calls = %d, want 0 (rejected before persistence)", len(got))
			}

			msgs := noiseMsgsForConn(t, rec, "c-int")
			if len(msgs) != 1 {
				t.Fatalf("got %d app frame(s), want exactly 1 malformed reply", len(msgs))
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
			if p.Code != protocol.CodeProtocolMalformed {
				t.Errorf("error Code = %q, want %q", p.Code, protocol.CodeProtocolMalformed)
			}
			if p.Message != msgSettingsMalformed {
				t.Errorf("error Message = %q, want the static %q", p.Message, msgSettingsMalformed)
			}
			if strings.Contains(p.Message, marker) {
				t.Errorf("reply message leaked request bytes (marker present): %q", p.Message)
			}
			if s := logBuf.String(); strings.Contains(s, marker) {
				t.Errorf("log output leaked request bytes (marker present):\n%s", s)
			}
		})
	}
}

// TestV2Session_SetSessionSettings_InterceptedNotRouted proves the v2-control
// discriminator in dispatchAppFrame routes set_session_settings to
// handleSetSessionSettings (the SettingsUpdater seam fires) BEFORE dispatch.Route,
// so a handler registered under the same type in the application table is never
// consulted.
func TestV2Session_SetSessionSettings_InterceptedNotRouted(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	fake := &fakeSettingsUpdater{}
	var routedToTable atomic.Bool
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:          frames,
		Outbound:        rec.outbound,
		StaticPriv:      respPriv,
		Devices:         v2PairedRegistry(t, v2TestToken),
		ServerID:        v2TestServerID,
		Logger:          silentLogger(),
		SettingsUpdater: fake,
		Handlers: map[string]dispatch.Handler{
			protocol.TypeSetSessionSettings: func(_ context.Context, _ *dispatch.Conn, _ protocol.Envelope) error {
				routedToTable.Store(true)
				return nil
			},
		},
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:      9,
		Type:    protocol.TypeSetSessionSettings,
		TS:      time.Now().UTC(),
		Payload: mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Effort: strPtr("low")}),
	})

	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	if got := fake.snapshot(); len(got) != 1 {
		t.Errorf("UpdateSettings calls = %d, want 1 (interception reached the handler)", len(got))
	}
	if routedToTable.Load() {
		t.Error("set_session_settings reached the dispatch handler table; it must be intercepted before dispatch.Route")
	}
}

// TestValidModel unit-tests the relay-local model shape validator: "" (clear) and
// well-formed names pass; a leading dash (flag-injection), whitespace, a control
// byte, and an over-length value are rejected.
//
// #1838 added the bracket rows. The grammar admits ONE trailing bracket group
// drawn from the same closed byte class as the base, so the accept rows carry the
// two values claude publishes and the reject rows carry every other bracket shape
// — leading, unbalanced, empty, nested, doubled, or followed by a suffix — plus
// each argv-injection shape re-checked WITH a bracket present, so the widening
// cannot have opened a side door through the group's interior.
func TestValidModel(t *testing.T) {
	t.Parallel()

	// A value at exactly the bound whose tail is a bracket group: the bound is
	// measured over the WHOLE value, group included.
	atBound := strings.Repeat("a", 60) + "[1m]"

	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"a", true},
		{"sonnet", true},
		{"claude-opus-4-8", true},
		{"opus.plan", true},
		{"model_v2", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"-foo", false},
		{".foo", false},
		{"--dangerously-skip-permissions", false},
		{"claude opus", false},
		{"claude\topus", false},
		{"claude\x00opus", false},
		{"opus/plan", false},
		{"café", false}, // multi-byte UTF-8 continuation bytes are >= 0x80

		// The two values claude publishes in its own model menu (#1704's
		// vocabulary, committed in internal/protocol/testdata/model_list.json),
		// plus the minimal well-formed variant and the bound.
		{"opus[1m]", true},
		{"claude-fable-5[1m]", true},
		{"a[b]", true},
		{atBound, true},
		{atBound + "x", false},

		// Bracket shapes the grammar rejects.
		{"[1m]", false},         // leading: base is empty
		{"[1m]opus", false},     // leading, and the group is not final
		{"opus[1m", false},      // unbalanced: last byte is not ]
		{"opus1m]", false},      // unbalanced: ] outside any group
		{"opus]1m[", false},     // inverted
		{"opus[]", false},       // empty group
		{"opus[[1m]]", false},   // nested: the interior holds [
		{"opus[a[b]c]", false},  // nested
		{"opus[1m][2m]", false}, // second group: the interior holds ]
		{"a[b]c[d]", false},     // second group
		{"opus[1m]x", false},    // not final
		{"opus[1m]-beta", false},

		// Every argv-injection shape AC 2 names, re-checked with a bracket
		// present so the group's interior is held to the base's byte class.
		{"-foo[1m]", false},
		{"opus[1;rm]", false},
		{"opus[$x]", false},
		{"opus[`x`]", false},
		{"opus[a|b]", false},
		{"opus[1 m]", false},
		{"opus[1\tm]", false},
		{"opus[1\x00m]", false},
		{"opus[café]", false},
		{"opus[a/b]", false},
	}
	for _, tc := range cases {
		if got := validModel(tc.in); got != tc.want {
			t.Errorf("validModel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestValidModel_ByteSetIsClosed proves the byte class is CLOSED, which the table
// above can only exemplify. Three loops, each walking all 256 byte values in one
// position of the grammar and asserting an iff — so a byte admitted anywhere it
// should not be fails here even if no example row names it.
//
// Each loop is the sole red for a distinct mutant: adding [ and ] to the flat
// byte class reddens loop 2 (the widening is NOT two extra charset bytes),
// dropping the first-byte-alphanumeric rule reddens loop 1, admitting a byte
// >= 0x80 inside the group reddens loop 3. The empty-group and unbalanced
// rejections are covered by the table above and not here — every probe below uses
// a non-empty, balanced group — which is why both tests exist.
//
// Every probe is built with string([]byte{...}), never string(rune(b)): the
// latter UTF-8-encodes any b >= 0x80 into two bytes and would silently test a
// different input than the one named.
func TestValidModel_ByteSetIsClosed(t *testing.T) {
	t.Parallel()

	alnum := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	word := func(b byte) bool {
		return alnum(b) || b == '.' || b == '_' || b == '-'
	}

	// Loop 1 — the value's FIRST byte. This is the leading-dash bar, and it is
	// also why a leading [ cannot pass.
	for i := 0; i < 256; i++ {
		b := byte(i)
		in := string([]byte{b})
		if got, want := validModel(in), alnum(b); got != want {
			t.Errorf("first byte 0x%02x: validModel(%q) = %v, want %v (only [A-Za-z0-9] may lead)", b, in, got, want)
		}
	}

	// Loop 2 — the BASE's byte class. Both brackets are rejected here (a[ is
	// unbalanced, a] has ] outside any group), which is the positive statement
	// that #1838 did not simply add two bytes to the charset.
	for i := 0; i < 256; i++ {
		b := byte(i)
		in := string([]byte{'a', b})
		if got, want := validModel(in), word(b); got != want {
			t.Errorf("base byte 0x%02x: validModel(%q) = %v, want %v (base admits only [A-Za-z0-9._-])", b, in, got, want)
		}
	}

	// Loop 3 — the GROUP INTERIOR's byte class, which must be the base's.
	for i := 0; i < 256; i++ {
		b := byte(i)
		in := "a[" + string([]byte{b}) + "]"
		if got, want := validModel(in), word(b); got != want {
			t.Errorf("group byte 0x%02x: validModel(%q) = %v, want %v (the group admits nothing the base does not)", b, in, got, want)
		}
	}

	// The boundary remains deliberately narrower than JSON requires: an accepted
	// value is a bounded whitespace-free identifier, and the future-spawn argv
	// element cannot pose as another token. Structured set_model marshalling is
	// independently responsible for keeping live delivery to one physical line.
	for _, tc := range []struct {
		name string
		b    byte
	}{
		{"newline", '\n'},
		{"carriage return", '\r'},
		{"tab", '\t'},
		{"space", ' '},
		{"slash", '/'},
	} {
		for _, in := range []string{
			string([]byte{tc.b}),
			string([]byte{'a', tc.b}),
			"a[" + string([]byte{tc.b}) + "]",
		} {
			if validModel(in) {
				t.Errorf("validModel(%q) accepted a %s; the network boundary must remain a bounded identifier grammar", in, tc.name)
			}
		}
	}
}

// TestValidEffort unit-tests the relay-local effort enum: "" (clear) and the
// closed set pass; anything else — including a case variant — is rejected.
func TestValidEffort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"low", true},
		{"medium", true},
		{"high", true},
		{"xhigh", true},
		{"max", true},
		{"ultra", false},
		{"LOW", false},
		{"lowx", false},
		{" ", false},
	}
	for _, tc := range cases {
		if got := validEffort(tc.in); got != tc.want {
			t.Errorf("validEffort(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestValidPermissionMode unit-tests the relay-local posture enum (#1687). It
// enumerates the whole accepted set, so a member silently lost in a refactor
// fails here rather than only as a client's option vanishing.
//
// The two named refusals carry the ticket's decisions and are the rows to keep:
//
//   - "" is refused, and this is the one place the three settings validators in
//     this file DISAGREE on purpose. validModel and validEffort accept "" as
//     "emit no flag, run at claude's own default" — a real value. The default
//     posture is a NAMEABLE mode, so an explicit "" here names nothing;
//     sessions.Pool.UpdateSettings refuses it too, and refusing at the wire keeps
//     the two layers agreeing rather than leaning on the inner one. Do not "fix"
//     this by copying the siblings' first case.
//   - bypassPermissions is refused, so the escalation is unreachable through this
//     field and keeps exactly one spelling on the wire (the yolo bit). A mode
//     string cannot grant bypass at any layer below either: the stored posture
//     escalates on the yolo bit alone, and claudeSettingsArgs derives
//     --dangerously-skip-permissions from that bit and never from the mode.
func TestValidPermissionMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want bool
	}{
		{"default", true},
		{"acceptEdits", true},
		{"plan", true},
		{"auto", true},
		{"dontAsk", true},
		{"", false},
		{"bypassPermissions", false},
		{"Plan", false},
		{"plan ", false},
		{"acceptedits", false},
		{"yolo", false},
	}
	for _, tc := range cases {
		if got := validPermissionMode(tc.in); got != tc.want {
			t.Errorf("validPermissionMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
