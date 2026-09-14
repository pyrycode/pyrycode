package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	mcpActConv   = "conversation-2420"
	mcpActServer = "pyry_mcp_fresh"
	// mcpActPlainToken stands in for the secret a *devices.Device is paired with. It is
	// never assigned to a field the audit record can reach; the no-leak assertion holds
	// it alongside the entry exactly as internal/audit's own does.
	mcpActPlainToken = "PLAINTEXT-DEVICE-TOKEN-must-never-appear-in-audit"
)

// mcpActMembership and mcpActFresh are the two DIFFERENT reads an accepted actuation
// takes. They differ in Status so a test can tell which one an answer carries, which is
// the whole of AC-4: pending is also the reading a just-reconnected server commonly
// gives, and it must come back as read rather than being waited out.
var (
	mcpActMembership = protocol.MCPStatusPayload{
		ConversationID: mcpActConv,
		Servers:        []protocol.MCPServerStatus{{Name: mcpActServer, Status: "connected", Scope: "project", Version: "10.0.0-fresh"}},
	}
	mcpActFresh = protocol.MCPStatusPayload{
		ConversationID: mcpActConv,
		Servers:        []protocol.MCPServerStatus{{Name: mcpActServer, Status: "pending", Scope: "project", Version: "10.0.0-fresh"}},
	}
)

// fakeMCPChild records what crossed the actuation seam and answers programmably. It is
// the *streamsup.Runner stand-in: the daemon-private primitives that trust their caller.
type fakeMCPChild struct {
	accept      bool
	reconnects  []string
	toggles     []string
	enabled     []bool
	hadDeadline []bool
}

func (f *fakeMCPChild) ReconnectMCPServer(ctx context.Context, serverName string) bool {
	f.reconnects = append(f.reconnects, serverName)
	_, ok := ctx.Deadline()
	f.hadDeadline = append(f.hadDeadline, ok)
	return f.accept
}

func (f *fakeMCPChild) SetMCPServerEnabled(ctx context.Context, serverName string, enabled bool) bool {
	f.toggles = append(f.toggles, serverName)
	f.enabled = append(f.enabled, enabled)
	_, ok := ctx.Deadline()
	f.hadDeadline = append(f.hadDeadline, ok)
	return f.accept
}

func (f *fakeMCPChild) calls() int { return len(f.reconnects) + len(f.toggles) }

// mcpActHarness is one gate under test with every seam observable.
type mcpActHarness struct {
	actuator *mcpActuatorV2
	child    *fakeMCPChild
	log      *bytes.Buffer

	// reads is what statusFor answers, consumed in order: the first is the membership
	// read and the second the post-acknowledgement read. Exhausting it answers false,
	// which is how the "post-ack read failed" case is driven.
	reads      []protocol.MCPStatusPayload
	readCalls  int
	childCalls int
	resolvable bool
}

type mcpActOpts struct {
	unwired       bool
	noBoundChild  bool
	membershipErr bool
	childRefuses  bool
	postAckErr    bool
}

func newMCPActHarness(opts mcpActOpts) *mcpActHarness {
	h := &mcpActHarness{
		child:      &fakeMCPChild{accept: !opts.childRefuses},
		log:        &bytes.Buffer{},
		resolvable: !opts.noBoundChild,
	}
	switch {
	case opts.membershipErr:
		h.reads = nil
	case opts.postAckErr:
		h.reads = []protocol.MCPStatusPayload{mcpActMembership}
	default:
		h.reads = []protocol.MCPStatusPayload{mcpActMembership, mcpActFresh}
	}

	logger := slog.New(slog.NewJSONHandler(h.log, nil))
	if opts.unwired {
		h.actuator = newMCPActuatorV2(nil, nil, logger)
		return h
	}
	h.actuator = newMCPActuatorV2(
		func(ctx context.Context, convID string) (protocol.MCPStatusPayload, bool) {
			h.readCalls++
			if h.readCalls > len(h.reads) {
				return protocol.MCPStatusPayload{}, false
			}
			return h.reads[h.readCalls-1], true
		},
		func(convID string) (mcpChildActuator, bool) {
			h.childCalls++
			if !h.resolvable {
				return nil, false
			}
			return h.child, true
		},
		logger,
	)
	return h
}

// records decodes every audit record the gate wrote.
func (h *mcpActHarness) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(h.log.Bytes()))
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("decoding audit record: %v (raw: %s)", err, h.log.String())
		}
		out = append(out, m)
	}
}

// only asserts exactly one record and returns it — AC-3's "exactly one" half, which
// every case below carries.
func (h *mcpActHarness) only(t *testing.T) map[string]any {
	t.Helper()
	recs := h.records(t)
	if len(recs) != 1 {
		t.Fatalf("audit records = %d, want exactly 1", len(recs))
	}
	return recs[0]
}

func mcpActEligibleDevice() *devices.Device {
	return &devices.Device{TokenHash: "hash-eligible", Name: "phone-a", AllowRemotePermissions: true}
}

func mcpActIneligibleDevice() *devices.Device {
	return &devices.Device{TokenHash: "hash-ineligible", Name: "phone-b"}
}

// mcpActVerbs drives both arms through one body. do calls the arm under test; both arms name
// the same conversation and server so a case reads identically on either.
var mcpActVerbs = []struct {
	name  string
	class string
	do    func(a *mcpActuatorV2, ctx context.Context, dev *devices.Device, server string) (protocol.MCPStatusPayload, bool)
}{
	{"reconnect", classMCPReconnect, func(a *mcpActuatorV2, ctx context.Context, dev *devices.Device, server string) (protocol.MCPStatusPayload, bool) {
		return a.Reconnect(ctx, protocol.MCPReconnectPayload{ConversationID: mcpActConv, ServerName: server}, dev)
	}},
	{"toggle", classMCPToggle, func(a *mcpActuatorV2, ctx context.Context, dev *devices.Device, server string) (protocol.MCPStatusPayload, bool) {
		return a.SetEnabled(ctx, protocol.MCPTogglePayload{ConversationID: mcpActConv, ServerName: server, Enabled: true}, dev)
	}},
}

// TestMCPActuatorV2_GateRefusesBeforeTouchingAnySeam is AC-1. The assertion that
// carries the security property is not the false return — it is that BOTH seam counters
// stay at zero, so a refused device causes no control request of any kind, membership
// look-up included.
func TestMCPActuatorV2_GateRefusesBeforeTouchingAnySeam(t *testing.T) {
	t.Parallel()

	devs := []struct {
		name string
		dev  *devices.Device
	}{
		{"nil-device", nil},
		{"paired-but-not-permitted", mcpActIneligibleDevice()},
	}
	for _, verb := range mcpActVerbs {
		for _, d := range devs {
			t.Run(verb.name+"/"+d.name, func(t *testing.T) {
				t.Parallel()

				h := newMCPActHarness(mcpActOpts{})
				payload, ok := verb.do(h.actuator, context.Background(), d.dev, mcpActServer)
				if ok {
					t.Fatal("an ineligible device was allowed to actuate")
				}
				if payload.ConversationID != "" || payload.Servers != nil {
					t.Errorf("refusal payload = %+v, want the zero value", payload)
				}
				if h.readCalls != 0 {
					t.Errorf("statusFor calls = %d, want 0: a refused device must reach no child, "+
						"and the membership read IS a control request written to one", h.readCalls)
				}
				if h.childCalls != 0 {
					t.Errorf("actuatorFor calls = %d, want 0", h.childCalls)
				}
				if h.child.calls() != 0 {
					t.Errorf("child actuations = %d, want 0", h.child.calls())
				}

				rec := h.only(t)
				if rec["outcome"] != "denied_unauthorized" {
					t.Errorf("outcome = %v, want denied_unauthorized: the record must keep a refusal of "+
						"the DEVICE apart from a refusal of the ACTION", rec["outcome"])
				}
				if rec["modal_class"] != verb.class {
					t.Errorf("modal_class = %v, want %q", rec["modal_class"], verb.class)
				}
				var wantHash, wantLabel string
				if d.dev != nil {
					wantHash, wantLabel = d.dev.TokenHash, d.dev.Name
				}
				if rec["device_hash"] != wantHash || rec["device_label"] != wantLabel {
					t.Errorf("device identity = (%v, %v), want (%q, %q)",
						rec["device_hash"], rec["device_label"], wantHash, wantLabel)
				}
			})
		}
	}
}

// TestMCPActuatorV2_RefusalsShareOneWireAnswerAndStayApartInTheRecord is AC-2 and the
// audit half of AC-3: four distinct reasons, one indistinguishable refusal, and each
// one recorded exactly once with the conversation, the server and the device on it.
// Each case also pins WHERE the ordering stopped, which is what keeps a later edit from
// making a child work on a path that should have refused before reaching it.
func TestMCPActuatorV2_RefusalsShareOneWireAnswerAndStayApartInTheRecord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		opts       mcpActOpts
		server     string
		wantReads  int
		wantChild  int
		wantActive int
	}{
		{name: "seams-unwired", opts: mcpActOpts{unwired: true}, server: mcpActServer},
		{name: "no-bound-eligible-child", opts: mcpActOpts{noBoundChild: true}, server: mcpActServer, wantChild: 1},
		{name: "membership-read-failed", opts: mcpActOpts{membershipErr: true}, server: mcpActServer, wantChild: 1, wantReads: 1},
		{name: "server-not-in-current-report", opts: mcpActOpts{}, server: "pyry_mcp_absent", wantChild: 1, wantReads: 1},
		{name: "child-refused-or-never-answered", opts: mcpActOpts{childRefuses: true}, server: mcpActServer, wantChild: 1, wantReads: 1, wantActive: 1},
	}
	for _, verb := range mcpActVerbs {
		for _, tc := range cases {
			t.Run(verb.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				h := newMCPActHarness(tc.opts)
				payload, ok := verb.do(h.actuator, context.Background(), mcpActEligibleDevice(), tc.server)
				if ok {
					t.Fatal("a refused actuation reported acceptance")
				}
				if payload.ConversationID != "" || payload.Servers != nil {
					t.Errorf("refusal payload = %+v, want the zero value", payload)
				}
				if h.readCalls != tc.wantReads {
					t.Errorf("statusFor calls = %d, want %d", h.readCalls, tc.wantReads)
				}
				if h.childCalls != tc.wantChild {
					t.Errorf("actuatorFor calls = %d, want %d", h.childCalls, tc.wantChild)
				}
				if h.child.calls() != tc.wantActive {
					t.Errorf("child actuations = %d, want %d", h.child.calls(), tc.wantActive)
				}

				rec := h.only(t)
				if rec["outcome"] != "denied" {
					t.Errorf("outcome = %v, want denied: an eligible device whose action could not be "+
						"performed was not refused for lack of privilege", rec["outcome"])
				}
				if rec["conversation_id"] != mcpActConv || rec["target"] != tc.server {
					t.Errorf("record named (%v, %v), want (%q, %q)",
						rec["conversation_id"], rec["target"], mcpActConv, tc.server)
				}
				if rec["modal_class"] != verb.class || rec["source"] != "remote" {
					t.Errorf("record = %v, want modal_class %q and source remote", rec, verb.class)
				}
			})
		}
	}
}

// TestMCPActuatorV2_AcceptedAnswersWithThePostAcknowledgementRead is AC-4. Two reads
// are taken and they differ; the answer must be the SECOND. The pending row is the
// difference and it is returned as read, because that is what a just-reconnected server
// commonly reports and this path neither waits nor polls for it to settle.
func TestMCPActuatorV2_AcceptedAnswersWithThePostAcknowledgementRead(t *testing.T) {
	t.Parallel()

	for _, verb := range mcpActVerbs {
		t.Run(verb.name, func(t *testing.T) {
			t.Parallel()

			h := newMCPActHarness(mcpActOpts{})
			payload, ok := verb.do(h.actuator, context.Background(), mcpActEligibleDevice(), mcpActServer)
			if !ok {
				t.Fatal("an eligible device's accepted actuation was refused")
			}
			if h.readCalls != 2 {
				t.Fatalf("statusFor calls = %d, want 2 (membership, then post-acknowledgement)", h.readCalls)
			}
			if len(payload.Servers) != 1 || payload.Servers[0].Status != "pending" {
				t.Fatalf("answer = %+v, want the post-acknowledgement read's pending row, not the "+
					"membership read's connected one", payload)
			}
			if h.child.calls() != 1 {
				t.Errorf("child actuations = %d, want exactly 1", h.child.calls())
			}
			if len(h.child.hadDeadline) != 1 || !h.child.hadDeadline[0] {
				t.Errorf("the actuation ran on a context with no deadline; a child that stays alive "+
					"and never answers would be bounded by nothing (hadDeadline=%v)", h.child.hadDeadline)
			}

			rec := h.only(t)
			if rec["outcome"] != "allowed" {
				t.Errorf("outcome = %v, want allowed", rec["outcome"])
			}
			if rec["conversation_id"] != mcpActConv || rec["target"] != mcpActServer {
				t.Errorf("record named (%v, %v), want (%q, %q)",
					rec["conversation_id"], rec["target"], mcpActConv, mcpActServer)
			}
			if rec["modal_id"] != "" {
				t.Errorf("modal_id = %v, want empty: an actuation has no one-time nonce to name",
					rec["modal_id"])
			}
		})
	}
}

// TestMCPActuatorV2_AcceptedButUnreadableAnswersRefusedAndRecordsAllowed pins the one
// place the wire and the record deliberately disagree. The child performed the action,
// so the decision was allowed; the daemon could not report its result, so the wire gets
// the merged reject. Exactly one record either way.
func TestMCPActuatorV2_AcceptedButUnreadableAnswersRefusedAndRecordsAllowed(t *testing.T) {
	t.Parallel()

	for _, verb := range mcpActVerbs {
		t.Run(verb.name, func(t *testing.T) {
			t.Parallel()

			h := newMCPActHarness(mcpActOpts{postAckErr: true})
			payload, ok := verb.do(h.actuator, context.Background(), mcpActEligibleDevice(), mcpActServer)
			if ok {
				t.Fatal("an unreadable post-acknowledgement status was answered as an accepted actuation")
			}
			if payload.ConversationID != "" || payload.Servers != nil {
				t.Errorf("payload = %+v, want the zero value — the relay must not read it on false", payload)
			}
			if h.child.calls() != 1 {
				t.Errorf("child actuations = %d, want 1: the action did happen", h.child.calls())
			}
			if rec := h.only(t); rec["outcome"] != "allowed" {
				t.Errorf("outcome = %v, want allowed: the record reports the DECISION, and the "+
					"decision was made when the child accepted", rec["outcome"])
			}
		})
	}
}

// TestMCPActuatorV2_SetEnabledPassesTheRequestedStateThrough covers the one asymmetry
// between the arms. The request carries the state to move TO, so both directions must
// arrive at the child unexamined and neither may be derived from a state this gate read.
func TestMCPActuatorV2_SetEnabledPassesTheRequestedStateThrough(t *testing.T) {
	t.Parallel()

	for _, want := range []bool{true, false} {
		t.Run(map[bool]string{true: "enable", false: "disable"}[want], func(t *testing.T) {
			t.Parallel()

			h := newMCPActHarness(mcpActOpts{})
			if _, ok := h.actuator.SetEnabled(context.Background(), protocol.MCPTogglePayload{
				ConversationID: mcpActConv, ServerName: mcpActServer, Enabled: want,
			}, mcpActEligibleDevice()); !ok {
				t.Fatal("toggle refused")
			}
			if len(h.child.enabled) != 1 || h.child.enabled[0] != want {
				t.Fatalf("child received enabled=%v, want exactly one call with %v", h.child.enabled, want)
			}
			if len(h.child.toggles) != 1 || h.child.toggles[0] != mcpActServer {
				t.Errorf("child acted on %v, want the name its own report carried (%q)",
					h.child.toggles, mcpActServer)
			}
		})
	}
}

// TestMCPActuatorV2_RecordCarriesNoSecretAndNoClaudeAuthoredValue is AC-3's negative
// half. The record must name the decision and nothing else: no plain device token, and
// none of the server config, argv, environment, version or error text a claude-authored
// status payload carries.
func TestMCPActuatorV2_RecordCarriesNoSecretAndNoClaudeAuthoredValue(t *testing.T) {
	t.Parallel()

	h := newMCPActHarness(mcpActOpts{})
	dev := mcpActEligibleDevice()
	if _, ok := h.actuator.Reconnect(context.Background(), protocol.MCPReconnectPayload{
		ConversationID: mcpActConv, ServerName: mcpActServer,
	}, dev); !ok {
		t.Fatal("reconnect refused")
	}

	written := h.log.String()
	for _, banned := range []string{
		mcpActPlainToken,       // the device's secret
		"10.0.0-fresh",         // claude-authored serverInfo.version
		"project",              // claude-authored scope
		"pending", "connected", // claude-authored status
	} {
		if strings.Contains(written, banned) {
			t.Errorf("audit record leaked %q: %s", banned, written)
		}
	}
	if !strings.Contains(written, dev.TokenHash) {
		t.Errorf("audit record missing the device hash (the recorded identity): %s", written)
	}
}

// TestMCPActuatorV2_BoundsTheAskedForNameInTheRecord is the security review's MUST FIX.
// ServerName is remote-authored and capped only by the transport frame, this path is
// reachable by a device the gate refuses, and control.SlogTee tees every record into a
// bounded ring — so an unbounded field is an eviction attack on the operator's own
// forensic history rather than mere noise.
func TestMCPActuatorV2_BoundsTheAskedForNameInTheRecord(t *testing.T) {
	t.Parallel()

	// A multi-byte rune repeated past the bound, so a naive byte slice would also
	// split one and emit invalid UTF-8.
	padded := strings.Repeat("ä", 8000)

	h := newMCPActHarness(mcpActOpts{})
	if _, ok := h.actuator.Reconnect(context.Background(), protocol.MCPReconnectPayload{
		ConversationID: mcpActConv, ServerName: padded,
	}, mcpActIneligibleDevice()); ok {
		t.Fatal("an ineligible device was allowed to actuate")
	}

	target, _ := h.only(t)["target"].(string)
	if len(target) > mcpAuditTargetMax {
		t.Errorf("recorded target = %d bytes, want at most %d", len(target), mcpAuditTargetMax)
	}
	if target == "" {
		t.Error("recorded target is empty; the record must still say what was asked for")
	}
	if !utf8.ValidString(target) {
		t.Errorf("recorded target is not valid UTF-8: %q", target)
	}
	if !strings.HasPrefix(padded, target) {
		t.Errorf("recorded target %q is not a prefix of what the device asked for", target)
	}
}

// TestBoundMCPChildActuator_NilCompositionYieldsANilSeam pins the seam's nil contract:
// a daemon with no session composition leaves the gate refusing rather than
// dereferencing, and the two live-child seams are nil together.
func TestBoundMCPChildActuator_NilCompositionYieldsANilSeam(t *testing.T) {
	t.Parallel()

	if got := boundMCPChildActuator(nil, nil); got != nil {
		t.Error("boundMCPChildActuator(nil, nil) returned a non-nil seam")
	}
}
