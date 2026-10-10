package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// appFrameJob is one plaintext queued for a conn's appFrameWorker, carrying the
// routing decision dispatchAppFrame already made (#1897) rather than leaving the
// worker to re-derive it.
//
// The tag exists so the decision lives in ONE place. Re-probing the envelope type
// inside the worker would look equivalent and is not: it puts the same decision in
// two places that can silently disagree, and TestEveryInboundV2TypeHasHandler
// reads dispatchAppFrame's case selectors as THE registry of those decisions — so
// a worker-side copy would let a future edit delete the case while the frame kept
// routing, flipping the guard without changing behaviour.
type appFrameJob struct {
	// plaintext is the decrypted application frame, freshly allocated by
	// noise.CipherState.Decrypt and therefore aliasing nothing.
	plaintext []byte

	// kind is what dispatchAppFrame recognised the frame as, and therefore which
	// handler the worker runs. appFrameRoute — the zero value — is every v1
	// application frame, which routes through dispatch.Route unchanged. Every
	// construction site names its kind rather than leaning on that zero value, so
	// the producer and the worker's arms read as one enumeration.
	kind appFrameKind

	// multiAgent is the conn's negotiated multi_agent decision, copied from the
	// Run-owned V2Session.multiAgent when the job is built on Run, so a worker
	// handler reads it without touching session state (#2646). Only the
	// settings read consults it.
	multiAgent bool
	// thread is copied on Run so supplied providers never read negotiated state off Run.
	thread bool
}

// appFrameKind names the off-Run handlers, one member per dispatchAppFrame case
// that hands off rather than handling inline.
//
// A TYPED KIND RATHER THAN ONE BOOL PER TYPE (#2054 widened #1897's lone
// `attachment bool`): the members are mutually exclusive by construction, and a
// set of bools can express a state that is not — two set at once, with the
// worker's arm order silently deciding which wins. Widening rather than
// re-probing the envelope type inside the worker is what keeps the routing
// decision in ONE place, which is the property appFrameJob exists for.
type appFrameKind uint8

const (
	// appFrameRoute is the v1 application dispatch chain (dispatch.Route). The
	// zero value, so a job built without a kind routes the way it always did.
	appFrameRoute appFrameKind = iota
	// appFrameAttachmentChunk is the inbound upload leg (#1897).
	appFrameAttachmentChunk
	// appFrameAttachmentRequest is the inbound retrieval request (#2054).
	appFrameAttachmentRequest
	// appFrameHistoryRequest is the inbound conversation-history request (#2116).
	appFrameHistoryRequest
	// appFrameMintPairing is the inbound pairing-mint request (#2127) — the first
	// member of this set whose handler WRITES host state rather than reading it.
	appFrameMintPairing
	// appFrameSessionSettingsRequest is the effective-effort read (#2516), whose
	// optional provider may wait on a child round trip.
	appFrameSessionSettingsRequest
	// appFrameMCPStatusRequest is the live-status read (#2381). Its wait on a child
	// round trip runs off the worker (#2702), so its reply is not FIFO.
	appFrameMCPStatusRequest
	// appFrameMCPReconnect and appFrameMCPToggle are the two MCP actuations (#2419)
	// — blocking like the read above, but WRITES to a running child's configuration,
	// which is why their dispatch arms gate on a seam that stays nil until #2420.
	appFrameMCPReconnect
	appFrameMCPToggle
	// appFrameStopBackgroundTask isolates the stop seam wait from Run.
	appFrameStopBackgroundTask
	// appFrameContextUsageRequest is the on-demand context-window read (#2431) —
	// the longest-waiting member of this set, since it defers a mid-turn request
	// until the turn ends before it asks the child anything. Its wait runs off the
	// worker (#2563), so like the MCP status read its reply is not FIFO.
	appFrameContextUsageRequest
	// appFrameWorkspaceFileRead is the live workspace file read (#2598) —
	// the retrieval arm's shape, over a file read live rather than a stored copy.
	appFrameWorkspaceFileRead
	appFrameSendQueuedNow
)

// appFrameWorker is the per-conn sub-actor that runs application handlers
// off the Run goroutine (#965). Exactly one is spawned per session in
// handleNoiseInit's open tail; it processes s.appFrames in strict FIFO
// arrival order — one frame fully routed (Route returned, all its replies
// forwarded to Run) before the next is dequeued — so no two handlers for
// the same conn run concurrently and their sealed replies emit in arrival
// order (AC-2). It terminates when s.done is closed (per-session teardown
// in closeWith) or ctx (runCtx) is cancelled (Run exit) — leaving no
// goroutine behind under conn churn or shutdown.
//
// TWO EXCEPTIONS to that ordering: a request_context_usage (#2563) and an
// mcp_status_request (#2702) are checked here but their seam wait and reply
// run on a goroutine of their own, so that reply can emit after replies to
// frames that arrived later. Clients correlate it on in_reply_to. connCtx is
// what ends those goroutines: it is cancelled when this worker returns, which
// is exactly on s.done or ctx. Each verb has its own semaphore bounding how
// many one conn can hold: asks (maxContextUsageAsksPerConn) and mcpAsks
// (maxMCPStatusAsksPerConn).
//
// The worker NEVER touches s.send / s.recv / keys / session state: it only
// runs Route → handler → c.Send (a marshal + channel push, no AEAD) and
// posts replies to m.appReply for Run to seal. That is the load-bearing
// single-owner-cipher invariant (AC-3).
func (m *V2SessionManager) appFrameWorker(ctx context.Context, s *V2Session) {
	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()
	asks := make(chan struct{}, maxContextUsageAsksPerConn)
	mcpAsks := make(chan struct{}, maxMCPStatusAsksPerConn)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case job := <-s.appFrames:
			// closeWith may have closed s.done while this frame sat in the
			// buffer (select picks randomly when both are ready). Re-check and
			// abandon queued-but-unstarted frames for a torn-down conn rather
			// than spawn a handler/child for a conn being closed.
			select {
			case <-s.done:
				return
			default:
			}
			switch job.kind {
			case appFrameSendQueuedNow:
				m.handleSendQueuedNow(s, job.plaintext)
			case appFrameAttachmentChunk:
				// The upload path (#1897). Runs here rather than in
				// routeAppFrame because it neither builds an outbound channel
				// nor calls dispatch.Route — it emits its own replies straight
				// through forwardToRun. Being on this goroutine is what
				// discharges attachments.Intake.Receive's single-feeder
				// precondition: strictly FIFO, one frame fully handled before
				// the next is dequeued, one worker per conn.
				m.handleAttachmentChunk(ctx, s, job.plaintext)
			case appFrameAttachmentRequest:
				// The retrieval path (#2054), here for the same reason: it
				// reads a stored file and enqueues one envelope per chunk,
				// which must not run on Run. Its chunks leave through Push
				// (safe from any goroutine) and its rejects through
				// forwardToRun, so like the arm above it never touches s.send.
				m.handleRequestAttachment(ctx, s, job.plaintext)
			case appFrameWorkspaceFileRead:
				// The live workspace read (#2598), the retrieval arm's twin:
				// chunks through Push, rejects through forwardToRun.
				m.handleReadWorkspaceFile(ctx, s, job.plaintext)
			case appFrameHistoryRequest:
				// The conversation-history path (#2116), here because it reads log
				// segments off disk. UNLIKE the two arms above it has only ONE
				// emission route: a page is a single envelope rather than a stream,
				// so both the page and every reject leave through forwardToRun and
				// nothing on this path ever touches s.send.
				m.handleRequestHistory(ctx, s, job.plaintext)
			case appFrameMintPairing:
				// The pairing-mint path (#2127), here because it takes the
				// devices.json flock(2) and rewrites the file. Its single emission
				// route is the history arm's — one envelope, reply and rejects alike
				// through forwardToRun — and the FIFO shape of this worker is what
				// bounds a conn to one mint in flight, which is why no per-verb
				// concurrency limit exists for a verb that writes.
				m.handleMintPairing(ctx, s, job.plaintext)
			case appFrameSessionSettingsRequest:
				// The optional effective-effort provider may wait on a child round
				// trip. Saved settings resolution and reply composition share this
				// worker placement, and the unsealed reply returns through
				// forwardToRun so the worker never touches s.send.
				m.handleRequestSessionSettings(connCtx, s, job.plaintext, job.multiAgent, job.thread)
			case appFrameMCPStatusRequest:
				// The resolver may wait on a child round trip. That wait does NOT
				// stall this conn's later frames (#2702): the handler checks
				// membership here and hands the wait to a goroutine of its own. Its
				// reply and every reject return through forwardToRun, so nothing
				// seals under s.send off Run.
				m.handleMCPStatusRequest(connCtx, s, mcpAsks, job.plaintext, job.thread)
			case appFrameMCPReconnect:
				// The actuator waits on a child round trip, so this stalls only the
				// addressed conn's later frames. Its reply and every reject return
				// through forwardToRun, so the worker seals nothing under s.send.
				m.handleMCPReconnect(ctx, s, job.plaintext)
			case appFrameMCPToggle:
				// The arm above's twin; same placement for the same reason.
				m.handleMCPToggle(ctx, s, job.plaintext)
			case appFrameStopBackgroundTask:
				// Replies return through forwardToRun for Run-owned sealing.
				m.handleStopBackgroundTask(ctx, s, job.plaintext)
			case appFrameContextUsageRequest:
				// The resolver may wait for an open turn to end and then for a child
				// round trip. Like the MCP status arm, that wait does NOT stall this
				// conn's later frames (#2563): the handler checks membership here and
				// hands the wait to a goroutine of its own, so its reply may emit
				// after later frames' replies. Its reply and every reject return
				// through forwardToRun, so nothing seals under s.send off Run.
				m.handleRequestContextUsage(connCtx, s, asks, job.plaintext, job.thread)
			case appFrameRoute:
				// The v1 application dispatch chain, unchanged: build the outbound
				// channel, call dispatch.Route, forward its replies to Run.
				m.routeAppFrame(ctx, s, job.plaintext)
			default:
				// Unreachable — every appFrameKind has an arm above, and Go cannot
				// check that for us. A kind added without one lands here and takes
				// the v1 chain, which answers an unrecognised type with
				// protocol.unsupported; the alternative to keeping this arm is
				// dropping such a frame in silence.
				m.routeAppFrame(ctx, s, job.plaintext)
			}
		}
	}
}

// routeAppFrame runs dispatch.Route for one application frame on the
// worker goroutine and forwards each reply the handler emits back to Run
// (via m.appReply) for sealing. Preserves #909's concurrent-drain shape:
// Route runs on its own short-lived goroutine while this goroutine drains
// the per-frame outbound channel, so a handler emitting more than
// handlerOutboundBuf replies cannot fill outbound and deadlock Route inside
// c.Send. A single sender (the handler) + this single receiver preserves
// FIFO emission order, and forwardToRun's blocking send onto the FIFO
// m.appReply channel preserves it across the seam.
//
// Only Route (→ handler → c.Send: a marshal + channel push, no AEAD) runs
// here; the seal (forwardAppReply → s.send.Encrypt) happens on Run. The
// outbound channel is deliberately NOT closed — a misbehaving handler that
// forks a sender after Route returns writes into a leaked but
// capacity-bounded channel the GC reclaims once the goroutine exits;
// closing here would panic such a sender (#446).
//
// ctx.Done() is not an arm of the Route-drain select for the same reason
// as #909: the handler observes cancellation through c.Send's own ctx arm
// (ctx is runCtx), so a shutdown drives Route to return and fires routeDone
// naturally. forwardToRun DOES honor ctx / s.done so a worker parked on a
// reply send unblocks on teardown.
func (m *V2SessionManager) routeAppFrame(ctx context.Context, s *V2Session, plaintext []byte) {
	outbound := make(chan protocol.RoutingEnvelope, handlerOutboundBuf)
	conn := dispatch.NewConn(s.connID, outbound, s.device)
	conn.SetMultiAgent(s.multiAgent)

	routeDone := make(chan struct{})
	go func() {
		defer close(routeDone)
		dispatch.Route(ctx, m.cfg.Logger, conn, m.cfg.Handlers, plaintext)
	}()
	for {
		select {
		case reply := <-outbound:
			if !m.forwardToRun(ctx, s, reply) {
				return
			}
		case <-routeDone:
			// Route returned (no further c.Send in flight); drain any residual
			// buffered replies in FIFO order, then return.
			for {
				select {
				case reply := <-outbound:
					if !m.forwardToRun(ctx, s, reply) {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// forwardToRun blocks until reply is handed to Run's m.appReply arm (which
// seals it under s.send), or the session/manager tears down. Returns true
// on a successful hand-off, false if ctx (runCtx, Run exiting) or s.done
// (this conn's closeWith) fired first — in which case routeAppFrame
// abandons the remaining drain (the Route goroutine still unwinds via
// c.Send's own ctx arm, and any reply already past this point is dropped by
// forwardAppReply's V2StateOpen gate). The blocking send is what applies
// backpressure onto the worker so replies never accumulate unbounded.
func (m *V2SessionManager) forwardToRun(ctx context.Context, s *V2Session, reply protocol.RoutingEnvelope) bool {
	select {
	case m.appReply <- appReplyMsg{s: s, reply: reply}:
		return true
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	}
}

// forwardAppReply seals one handler reply under s.send and forwards it as
// a noise_msg via m.send. MUST run only on the manager's Run goroutine —
// s.send is the single-owner Noise send CipherState, and a concurrent
// Encrypt would reuse a nonce / corrupt the send counter on the encrypted
// wire. Reached from Run's m.appReply arm, where a per-conn worker posts
// the not-yet-sealed reply. Drops the reply (WARN, no wire emission) on the
// realistically-unreachable seal/marshal error, exactly as #446: never
// emit an unsealed frame. Also drops it unsealed when transportDown reports
// the relay leg down (#1525, see the branch comment).
func (m *V2SessionManager) forwardAppReply(s *V2Session, reply protocol.RoutingEnvelope) {
	if s.state != V2StateOpen {
		// The worker was mid-handler when closeWith tore this session down
		// (#965). Drop the reply — sealing under a dead session would burn a
		// send-nonce for a frame no live peer awaits. Reading s.state is safe:
		// forwardAppReply runs on Run, the sole writer of s.state. Mirrors
		// forwardEnvelope's V2StateOpen gate.
		m.cfg.Logger.Debug("relay: v2 app reply dropped; session not open",
			"conn_id", s.connID)
		return
	}
	if m.transportDown() {
		// Transport-down DROP (#1525), the same shape drainOnce holds the push
		// head with and handleWake defers the rekey emit with. Seal nothing:
		// m.send swallows its Outbound error at Debug, so reacting to that error
		// is structurally too late — the send-nonce is already spent. And a
		// burned nonce is not a lost message: relay↔binary carries no per-conn
		// disconnect frame, so the phone leaves its recv CipherState untouched,
		// the next delivered frame fails AEAD, and the session dies at
		// StatusProtocolMismatch without self-healing. #965 is why this seal
		// needs the guard that #874 judged unnecessary: the reply now returns to
		// Run up to a whole handler duration after its inbound frame, wide
		// enough to hold an entire blip.
		//
		// Dropped, not parked. The reply is undeliverable either way today —
		// m.send already discards it — so this only stops paying a nonce for
		// that non-delivery. Post-recovery delivery would need a buffer, an
		// eviction policy and an ordering rule against the push queue; out of
		// scope. Nothing is held, so the down→up edge needs no flush.
		//
		// Debug and content-free (event slug + conn-id, no payload, plaintext,
		// ciphertext or key bytes): transport-down is expected during reconnect,
		// and a blip on a chatty conn emits one line per reply.
		//
		// The probe is a plain read of a construction-time func on the Run
		// goroutine — no lock, no atomic. The single-frame TOCTOU at the up→down
		// instant carries over from #874 and #912, documented on the Connected
		// seam itself.
		m.cfg.Logger.Debug("relay: v2 app reply dropped; transport down",
			"event", "v2.app_reply.dropped_transport_down",
			"conn_id", s.connID)
		return
	}
	frameJSON, withheld := m.threadReply(s, m.agentTaggedReply(s, reply.Frame))
	if withheld {
		return
	}
	ciphertext, err := s.send.Encrypt(frameJSON)
	if err != nil {
		// Realistically unreachable under correct flynn/noise. Drop the
		// reply rather than emit the unencrypted frame.
		m.cfg.Logger.Warn("relay: v2 seal app reply failed; reply dropped",
			"conn_id", s.connID)
		return
	}
	frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 marshal app reply failed; reply dropped",
			"conn_id", s.connID)
		return
	}
	// The reply's CloseCode is ignored: handlers do not signal closes
	// through c.Send/c.Reply; the close-code field on the routing envelope
	// is reserved for the manager's own close-intent emissions (closeWith).
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
}

// sealError builds a TypeError envelope, AEAD-seals it under s.send,
// and returns the wrapped noise_msg inner-frame JSON ready for the
// Frame slot of a RoutingEnvelope. Returns a non-nil error only if the
// AEAD seal itself failed; JSON marshal failures of static
// well-typed values are wrapped but practically unreachable.
func (m *V2SessionManager) sealError(s *V2Session, code, message string, inReplyTo uint64) (json.RawMessage, error) {
	return m.sealErrorPayload(s, protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: false,
	}, inReplyTo)
}

// sealErrorPayload is sealError for a caller that sets an optional payload
// field, such as the client.update_required reply's MinClientVersion (#2578).
func (m *V2SessionManager) sealErrorPayload(s *V2Session, payload protocol.ErrorPayload, inReplyTo uint64) (json.RawMessage, error) {
	errPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal error payload: %w", err)
	}
	envelope := protocol.Envelope{
		ID:        2,
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   errPayload,
		InReplyTo: &inReplyTo,
	}
	envJSON, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal error envelope: %w", err)
	}
	ciphertext, err := s.send.Encrypt(envJSON)
	if err != nil {
		return nil, fmt.Errorf("aead seal error envelope: %w", err)
	}
	return marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
}

// marshalInnerFrameV2 wraps rawBytes as an InnerFrameV2 of the given
// type, base64-encoding rawBytes for the wire.
func marshalInnerFrameV2(frameType string, rawBytes []byte) (json.RawMessage, error) {
	out, err := json.Marshal(protocol.InnerFrameV2{
		Version: protocol.V2Version,
		Type:    frameType,
		Data:    base64.StdEncoding.EncodeToString(rawBytes),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal inner frame %s: %w", frameType, err)
	}
	return out, nil
}
