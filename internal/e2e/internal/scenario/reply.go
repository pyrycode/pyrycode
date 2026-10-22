// Package scenario contains test-only daemon assertions shared by live and recorded tests.
package scenario

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Next preserves encrypted receive order and returns an empty envelope on deadline.
type Next func(time.Time) protocol.Envelope

type Receiver interface {
	ReceiveBytes(time.Duration) ([]byte, error)
}

func Receive(t *testing.T, phone Receiver, cs *noise.CipherState) Next {
	t.Helper()
	return func(deadline time.Time) protocol.Envelope {
		t.Helper()
		for time.Now().Before(deadline) {
			raw, err := phone.ReceiveBytes(time.Until(deadline))
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				return protocol.Envelope{}
			}
			if err != nil {
				t.Fatal("phone receive:", err)
			}
			var inner protocol.InnerFrameV2
			if json.Unmarshal(raw, &inner) != nil {
				t.Fatal("invalid phone frame")
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			cipher, err := base64.StdEncoding.DecodeString(inner.Data)
			if err != nil {
				t.Fatal("invalid encrypted phone frame")
			}
			plain, err := cs.Decrypt(cipher)
			if err != nil {
				t.Fatal("phone decrypt:", err)
			}
			var env protocol.Envelope
			if json.Unmarshal(plain, &env) != nil {
				t.Fatal("invalid daemon envelope")
			}
			return env
		}
		return protocol.Envelope{}
	}
}

// PollingReceive retains one pending socket read across observation deadlines.
// A short source poll must never cancel coder/websocket's read context, which
// would close the connection. Cleanup closes and joins the outstanding read.
func PollingReceive(t *testing.T, phone interface {
	Receiver
	Close() error
}, cs *noise.CipherState) Next {
	t.Helper()
	next, _ := PollingReceiveWithStop(t, phone, cs)
	return next
}

// PollingReceiveWithStop also permits a reconnecting harness to close and join
// the old connection's pending read before it installs a successor reader.
// Stop is idempotent. Call Next and Stop serially from the test goroutine.
func PollingReceiveWithStop(t *testing.T, phone interface {
	Receiver
	Close() error
}, cs *noise.CipherState) (Next, func()) {
	t.Helper()
	type result struct {
		raw []byte
		err error
	}
	var pending chan result
	stop := func() {
		_ = phone.Close()
		if pending != nil {
			<-pending
			pending = nil
		}
	}
	t.Cleanup(stop)
	return func(deadline time.Time) protocol.Envelope {
		for time.Now().Before(deadline) {
			if pending == nil {
				pending = make(chan result, 1)
				ch := pending
				go func() { raw, err := phone.ReceiveBytes(30 * time.Minute); ch <- result{raw, err} }()
			}
			timer := time.NewTimer(time.Until(deadline))
			var r result
			select {
			case r = <-pending:
				pending = nil
				timer.Stop()
			case <-timer.C:
				return protocol.Envelope{}
			}
			if r.err != nil {
				t.Fatal("phone receive: ", r.err)
			}
			var inner protocol.InnerFrameV2
			if json.Unmarshal(r.raw, &inner) != nil {
				t.Fatal("invalid phone frame")
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			cipher, err := base64.StdEncoding.DecodeString(inner.Data)
			if err != nil {
				t.Fatal("invalid encrypted phone frame")
			}
			plain, err := cs.Decrypt(cipher)
			if err != nil {
				t.Fatal("phone decrypt: ", err)
			}
			var env protocol.Envelope
			if json.Unmarshal(plain, &env) != nil {
				t.Fatal("invalid daemon envelope")
			}
			return env
		}
		return protocol.Envelope{}
	}, stop
}

const WarnRateLimitStatus = "allowed_warning"

// CompletedTurn requires attributed nonempty text followed by terminal idle.
// Provider wording and chunk counts are unconstrained; parser gaps and unknown
// rate-limit statuses remain global alarms, including on foreign conversations.
func CompletedTurn(t *testing.T, next Next, convID string, timeout time.Duration, rejectModal bool, sources ...SourceFacts) string {
	t.Helper()
	var turn sourceTurn
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if strings.TrimSpace(turn.text.String()) == "" {
				t.Fatalf("M1: never observed a non-empty assistant_delta for %q within %s — the turn never drained "+
					"end-to-end (delivery never reached the child, or the parser / drain gate / emitter dropped it — "+
					"most likely a UUID mismatch between seedBootstrapRegistry and seedBoundConversation)", convID, timeout)
			}
			t.Fatalf("M2: observed the assistant_delta for %q but never a terminal turn_state{idle} within %s — "+
				"the turn opened but never closed", convID, timeout)
		}
		env := next(pollDeadline(deadline))
		turn.feed(t, convID, sourceFacts(t, sources, convID))
		switch env.Type {
		case protocol.TypeModalShown:
			if rejectModal {
				t.Fatalf("unexpected second modal_shown while draining the session-granted turn: %s", env.Payload)
			}
		case protocol.TypeUnrecognizedMessage:
			// A normal live turn must not expose parser gaps. This global alarm
			// requires a mapping or a measured known-ignored classification.
			var p protocol.UnrecognizedMessagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode unrecognized_message payload: %v", err)
			}
			t.Fatalf("a NORMAL turn produced an unrecognized_message: site=%q type=%q truncated=%v\n"+
				"raw: %s\n\n"+
				"claude emitted output the stream parser has no mapping for. Either it needs a "+
				"mapping, or it belongs on streamsup.ignoredLineTypes — re-run the line-inventory "+
				"measurement before deciding.",
				p.Site, p.MessageType, p.Truncated, p.Raw)
		case protocol.TypeRateLimited:
			// Rate limits affect the account, so there is no conversation filter.
			// Tolerate only the measured warning status; unknown statuses require
			// checking account limits or remeasuring the provider's vocabulary.
			var p protocol.RateLimitedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode rate_limited payload: %v", err)
			}
			if p.Status == WarnRateLimitStatus {
				t.Logf("rate_limited frame TOLERATED (not a failure): status=%q limit_type=%q "+
					"resets_at=%d conversation_id=%q — the account is inside its usage-limit "+
					"warning band and the turn was still allowed, so the daemon is reporting "+
					"this correctly. Expect it on every run until the window resets.",
					p.Status, p.LimitType, p.ResetsAt, p.ConversationID)
				continue
			}
			t.Fatalf("a NORMAL turn produced an UNRECOGNISED rate_limited frame: status=%q "+
				"limit_type=%q resets_at=%d truncated_fields=%v conversation_id=%q\n\n"+
				"Either a usage limit is genuinely in force on this account (re-run after the "+
				"window resets), or claude changed the benign status value and "+
				"streamsup.benignRateLimitStatus is stale, or claude grew another "+
				"allowed-but-notable status this drain has not measured — status above is what "+
				"tells those apart.",
				p.Status, p.LimitType, p.ResetsAt, p.TruncatedFields, p.ConversationID)
		case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd:
			t.Fatal("retired interactive content delivered on the wire")
		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State != "idle" || st.ConversationID != convID {
				continue // the leading responding state precedes the delta
			}
			if strings.TrimSpace(turn.text.String()) == "" || !turn.idle(t, env, convID) {
				// A prior cancelled turn can leave its terminal idle queued after
				// drainForCancelledTurnEnd returns at turn_end. Without a delta,
				// this idle cannot be attributed to the turn being drained.
				continue
			}
			// M2: after matching nonempty text, terminal idle closes the turn.
			t.Logf("M2: terminal turn_state{idle} for %q — the turn closed", convID)
			return turn.text.String()
		}
	}
}
