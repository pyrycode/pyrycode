package relay

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// inlineReplyRow is one of the six inline reply seals on Run (#1526). Each row
// reaches exactly one site and waits for that site's own drop slug, so a row
// fails only when its own site loses the guard.
type inlineReplyRow struct {
	name  string
	event string // the site's transport-down drop slug
	caps  []string
	cfg   func(*V2SessionConfig)
	// setup runs on the started manager before the handshake.
	setup func(*V2SessionManager)
	// lastEventID, when set, is advertised in the hello (the resync row).
	lastEventID *uint64
	// downAtOpen flips the leg down right after the noise_resp is recorded, so a
	// site that fires at the handshake tail (emitResync) runs with the leg down.
	downAtOpen bool
	// trigger is sealed and sent with the leg down; nil when the site fires at
	// the handshake tail.
	trigger *protocol.Envelope
}

// TestV2Session_InlineReply_TransportDown_BurnsNoNonce is AC1–AC4 of #1526. With
// Connected reporting down, each of the six inline reply seals must consume no
// send-nonce and hand nothing to Outbound. The nonce oracle is decryptAppFrame
// under the phone's untouched initRecv, as in
// TestV2Session_AppReply_TransportDown_BurnsNoNonce: after recovery the next
// frame the daemon seals must land at the nonce initRecv still expects, which
// is only true if the down path sealed nothing. Run under -race.
func TestV2Session_InlineReply_TransportDown_BurnsNoNonce(t *testing.T) {
	t.Parallel()

	gapID := uint64(1000)
	settingsTrigger := func(t *testing.T) *protocol.Envelope {
		return &protocol.Envelope{
			ID:      11,
			Type:    protocol.TypeSetSessionSettings,
			TS:      time.Now().UTC(),
			Payload: mustSettingsPayload(t, protocol.SetSessionSettingsPayload{SessionID: settingsSessionID, Model: strPtr("sonnet")}),
		}
	}
	snapshotTrigger := &protocol.Envelope{
		ID:      11,
		Type:    protocol.TypeRequestSnapshot,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"conversation_id":"conv-inline"}`),
	}

	rows := []struct {
		inlineReplyRow
		triggerFn func(*testing.T) *protocol.Envelope
	}{
		{inlineReplyRow: inlineReplyRow{
			name:    "snapshot error",
			event:   "v2.snapshot.err_dropped_transport_down",
			trigger: snapshotTrigger, // nil KnownConversation ⇒ not-found error reply
		}},
		{inlineReplyRow: inlineReplyRow{
			name:  "resync marker",
			event: "v2.replay.resync_dropped_transport_down",
			setup: func(mgr *V2SessionManager) {
				ring := eventring.New(eventring.MaxEventsPerConversation)
				appendRingEvents(ring, v2TestConvID, protocol.TypeAssistantDelta, 3)
				mgr.SetReplaySource(ring, func() string { return v2TestConvID })
			},
			lastEventID: &gapID, // beyond the id space ⇒ gap ⇒ resync (#1494)
			downAtOpen:  true,
		}},
		{inlineReplyRow: inlineReplyRow{
			name:  "session_settings_updated",
			event: "v2.settings.updated_dropped_transport_down",
			caps:  []string{protocol.CapabilityInteractive},
			cfg: func(c *V2SessionConfig) {
				c.SettingsUpdater = &fakeSettingsUpdater{}
			},
		}, triggerFn: settingsTrigger},
		{inlineReplyRow: inlineReplyRow{
			name:  "settings error",
			event: "v2.settings.err_dropped_transport_down",
			caps:  []string{protocol.CapabilityInteractive},
			// nil SettingsUpdater ⇒ unavailable error reply
		}, triggerFn: settingsTrigger},
		{inlineReplyRow: inlineReplyRow{
			name:  "debug bundle error",
			event: "v2.bundle.err_dropped_transport_down",
			cfg: func(c *V2SessionConfig) {
				// Assembly fails off Run; handleBundleReady then replies on Run —
				// the widened window the ticket names.
				c.DebugBundler = func() ([]byte, error) { return nil, errors.New("assemble boom") }
			},
			trigger: &protocol.Envelope{ID: 11, Type: protocol.TypeRequestDebugBundle, TS: time.Now().UTC()},
		}},
	}

	for _, r := range rows {
		row := r.inlineReplyRow
		triggerFn := r.triggerFn
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if triggerFn != nil {
				row.trigger = triggerFn(t)
			}
			runInlineReplyDownRow(t, row)
		})
	}
}

// runInlineReplyDownRow drives one row: handshake with the leg up, fire the
// site with the leg down, assert the drop, then recover and prove the nonce
// chain is intact.
func runInlineReplyDownRow(t *testing.T, row inlineReplyRow) {
	t.Helper()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)

	gated := newGatedRecorder()
	// attempts counts every Outbound call, up OR down: gatedRecorder records
	// nothing while down, so the recorder alone cannot tell "sealed nothing"
	// from "handed something to Outbound" (the #1525 non-vacuity rule). Only Run
	// calls Outbound, so the downAtOpen flip is ordered before the rest of the
	// handshake's Run pass.
	var attempts atomic.Int64
	outbound := func(env protocol.RoutingEnvelope) error {
		n := attempts.Add(1)
		err := gated.outbound(env)
		if row.downAtOpen && n == 1 {
			gated.up.Store(false)
		}
		return err
	}

	logger, logBuf := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 4)
	cfg := V2SessionConfig{
		Frames:     frames,
		Outbound:   outbound,
		Connected:  gated.connected,
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     logger,
		Handlers:   map[string]dispatch.Handler{protocol.TypeListConversations: prolificHandler()},
	}
	if row.cfg != nil {
		row.cfg(&cfg)
	}
	mgr, stop := startManager(t, cfg)
	t.Cleanup(stop)
	if row.setup != nil {
		row.setup(mgr)
	}

	initSend, initRecv := inlineReplyHandshake(t, frames, gated.rec, row, respPub, initPriv)

	if row.trigger != nil {
		gated.up.Store(false)
		frames <- sealAppFrame(t, initSend, *row.trigger)
	}

	// The drop emits no envelope, so its log line is the only observable edge.
	waitForLogContains(t, logBuf, "event="+row.event)
	lines := dropLinesContaining(logBuf, row.event)
	if len(lines) != 1 {
		t.Fatalf("drop log lines = %d, want exactly 1:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, want := range []string{"conn_id=" + v2TestConnID, "reason=transport_down"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("drop line missing %q: %s", want, lines[0])
		}
	}
	// Content-free (AC4): slug, conn-id and reason only — no payload, settings
	// value (#833), snapshot text, ciphertext or error text.
	for _, forbidden := range []string{"payload", "in_reply_to", "type=", "sonnet", "model", "effort", "yolo", "permission", "inline-snap-sentinel", "ciphertext", "err=", "boom"} {
		if strings.Contains(strings.ToLower(lines[0]), forbidden) {
			t.Errorf("drop line carries %q: %s", forbidden, lines[0])
		}
	}

	// Nothing handed to Outbound on the down path: only the noise_resp.
	if got := attempts.Load(); got != 1 {
		t.Errorf("Outbound attempts = %d, want 1 (the noise_resp only)", got)
	}

	// Recover, then emit. The recovery reply must be the first seal, landing at
	// the nonce initRecv still expects.
	gated.up.Store(true)
	frames <- sealAppFrame(t, initSend, protocol.Envelope{
		ID:      99,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"count":1}`),
	})
	msgs := waitForConnNoiseMsg(t, gated.rec, v2TestConnID, 1)
	inner := decryptAppFrame(t, msgs[0], initRecv)
	if inner.InReplyTo == nil || *inner.InReplyTo != 99 {
		t.Errorf("recovery reply InReplyTo = %v, want 99", inner.InReplyTo)
	}
}

// inlineReplyHandshake opens v2TestConnID with a hello built for row (caps, or a
// last_event_id for the resync row) and returns the phone's CipherStates. It
// mirrors driveToOpen, which cannot carry either hello variant.
func inlineReplyHandshake(t *testing.T, frames chan protocol.RoutingEnvelope, rec *v2Recorder, row inlineReplyRow, respPub, initPriv []byte) (initSend, initRecv *noise.CipherState) {
	t.Helper()
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	hello := buildHelloEarlyDataCaps(t, v2TestToken, row.caps)
	if row.lastEventID != nil {
		hello = buildHelloEarlyDataReplay(t, v2TestToken, row.lastEventID)
	}
	initMsg, err := initiator.WriteInit(hello)
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)

	envs := waitForEnvelopes(t, rec, 1)
	_, initSend, initRecv, err = initiator.ReadResp(decodeRespFrame(t, envs[0]))
	if err != nil {
		t.Fatalf("ReadResp: %v", err)
	}
	return initSend, initRecv
}
