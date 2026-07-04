package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// promptDeliverTimeout bounds the delivery goroutine's WriteUserTurn. It is
// deliberately larger than the supervisor's ~10s WaitReady + commit-confirm
// budget so that a genuine supervisor failure (ErrNoLiveSession /
// ErrTurnNotCommitted / its own deadline) surfaces first and maps to a real
// "prompt delivery failed" — the timeout here is only the outer backstop against
// a wedged delivery path never returning at all.
const promptDeliverTimeout = 15 * time.Second

// promptDeliverer is the one-method prompt-delivery seam (accept-interfaces-at-
// the-consumer, mirroring interrupter). *sessions.Session satisfies it via
// WriteUserTurn → Supervisor.WriteUserTurn → the tui-driver DeliverPrompt path,
// which types the payload into the supervised interactive claude as a bracketed
// paste. Declared here so promptHandler / deliverPrompt are unit-testable with a
// recording double, without a live pool.
type promptDeliverer interface {
	WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
}

// promptResult is the session/prompt success payload: the turn's stopReason. T7
// (#751) supplies the real reason on TurnEnd via promptHolds.end; the interim
// placeholder-end path exercised by tests supplies a throwaway string.
type promptResult struct {
	StopReason string `json:"stopReason"`
}

// promptParams is the subset of the ACP session/prompt request pyry reads: the
// ordered list of content blocks. sessionId is decoded separately via the shared
// decodeSessionID helper (which rejects empty/missing ids before Pool.Lookup).
type promptParams struct {
	Prompt []contentBlock `json:"prompt"`
}

// contentBlock is one ACP prompt content block. Only text blocks are honoured
// today (promptCapabilities advertises image/audio/embeddedContext all false);
// Text is read only when Type == "text".
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// promptHolds is the per-session in-flight registry for held session/prompt
// calls: at most one held call per session id. It is the sole owner of held-call
// resolution — begin registers a hold inline on the read loop, and exactly one of
// end (T7 turn-end) or fail (delivery failure) later resolves it and frees the
// slot.
//
// mu is a leaf lock. begin holds it across an atomic check-and-insert; end/fail
// capture the *Responder under mu and RELEASE mu before calling Reply/ReplyError
// (those take the transport's writeMu, so resolving under mu would nest
// mu → writeMu). The store is never touched by the transport write path, so no
// writeMu → mu edge exists and no lock-order cycle is possible.
type promptHolds struct {
	mu     sync.Mutex
	held   map[string]*acp.Responder
	logger *slog.Logger
}

// newPromptHolds constructs an empty hold registry. logger records only the
// method/session-id and error sentinels — never prompt content.
func newPromptHolds(logger *slog.Logger) *promptHolds {
	return &promptHolds{
		held:   make(map[string]*acp.Responder),
		logger: logger,
	}
}

// begin registers resp as the held call for sessionID, returning false when a
// call is already in flight for that session (the caller then rejects the second
// prompt without registering a hold or spawning delivery). Called inline on the
// single read-loop goroutine, so the check-and-insert is atomic against itself;
// mu additionally guards against end/fail running on other goroutines.
func (h *promptHolds) begin(sessionID string, resp *acp.Responder) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, inFlight := h.held[sessionID]; inFlight {
		return false
	}
	h.held[sessionID] = resp
	return true
}

// end resolves the held call for sessionID with a success stopReason and frees
// the slot. It is the T7 (#751) seam: turnbridge calls it on TurnEnd with the
// mapped ACP stopReason. A missing entry is a no-op (idempotent), so end is safe
// to call even if fail already fired for the same session. The #765 Responder.done
// CAS guarantees exactly one frame across every resolve race; the delete here
// guarantees at most one of end/fail even finds the entry.
func (h *promptHolds) end(sessionID, stopReason string) {
	h.mu.Lock()
	resp := h.held[sessionID]
	delete(h.held, sessionID)
	h.mu.Unlock()
	if resp == nil {
		return
	}
	// Resolve outside mu: Reply takes the transport writeMu.
	_ = resp.Reply(promptResult{StopReason: stopReason})
}

// fail resolves the held call for sessionID with an error frame and frees the
// slot, so the next session/prompt for the same session is accepted. Called by
// the delivery goroutine when WriteUserTurn returns non-nil (no turn started). A
// missing entry is a no-op (idempotent).
func (h *promptHolds) fail(sessionID string, e *acp.Error) {
	h.mu.Lock()
	resp := h.held[sessionID]
	delete(h.held, sessionID)
	h.mu.Unlock()
	if resp == nil {
		return
	}
	// Resolve outside mu: ReplyError takes the transport writeMu.
	_ = resp.ReplyError(e)
}

// mapPromptContent converts session/prompt params into the user-turn payload
// bytes. Blocks are concatenated in order, text blocks joined with "\n" between
// them (bracketed-paste-safe per tui-driver v1.3.0, so an embedded newline does
// not submit the turn prematurely). It fails closed:
//
//   - a non-text block (image / audio / resource / …) → CodeInvalidParams naming
//     the kind. pyry advertises text-only promptCapabilities, so any non-text
//     block is a host protocol violation; rejecting never drops host content
//     silently and never reinterprets it.
//   - a C0/DEL control byte other than \t \n \r → CodeInvalidParams. The delivery
//     path frames the payload as a bracketed paste, so a raw ESC (e.g. an embedded
//     paste terminator ESC[201~) could break out of paste framing and reach
//     claude's TUI as keystrokes / control sequences rather than message text.
//     This deterministic boundary guard closes that terminal-escape injection
//     vector (security-sensitive; the code half of belt-and-suspenders).
//   - an assembled payload that is empty, or absent/empty prompt → CodeInvalidParams.
//   - malformed params JSON → CodeInvalidParams.
//
// Otherwise the text passes through verbatim — no escaping, no transformation.
// Prompt size is bounded upstream by the transport's 16 MiB per-line cap, so no
// additional length cap is imposed here.
func mapPromptContent(params json.RawMessage) ([]byte, *acp.Error) {
	var p promptParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, acp.NewError(acp.CodeInvalidParams, "invalid params")
	}
	var b strings.Builder
	for i, block := range p.Prompt {
		if block.Type != "text" {
			return nil, acp.NewError(acp.CodeInvalidParams, fmt.Sprintf("unsupported content block type %q", block.Type))
		}
		if hasDisallowedControl(block.Text) {
			return nil, acp.NewError(acp.CodeInvalidParams, "control character in prompt text")
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(block.Text)
	}
	if b.Len() == 0 {
		return nil, acp.NewError(acp.CodeInvalidParams, "empty prompt")
	}
	return []byte(b.String()), nil
}

// hasDisallowedControl reports whether s contains a C0 (0x00–0x1f) or DEL (0x7f)
// control byte other than tab (\t), newline (\n), or carriage return (\r). It
// scans bytes, not runes, which is correct: every disallowed control byte is a
// single ASCII byte, and valid multi-byte UTF-8 sequences use only bytes ≥ 0x80,
// so a byte < 0x20 or == 0x7f can never be a UTF-8 continuation byte — no
// false positive on legitimate multi-byte text.
func hasDisallowedControl(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

// promptHandler returns the acp.Handler for session/prompt. It resolves the
// target session, maps the content blocks to a user-turn payload, holds its own
// JSON-RPC call open (returning ErrDeferred), and delivers the payload on a
// spawned goroutine.
//
// resolve maps a validated session id onto its promptDeliverer (the composition
// root wires it to pool.Lookup). It must return a true nil interface on error —
// a typed nil *sessions.Session would defeat the err short-circuit below (the
// same nil-interface trap the cancel handler documents).
//
// Delivery must not run on the read loop: WriteUserTurn blocks for seconds
// (WaitReady + commit-confirm), and stalling the loop would break the very
// concurrent-second-prompt rejection and session/cancel this handler must keep
// live. The cheap begin check stays inline (race-free on the single read-loop
// goroutine); only the blocking delivery moves to the goroutine, and the handler
// returns ErrDeferred so the loop proceeds immediately.
func promptHandler(holds *promptHolds, resolve func(sessions.SessionID) (promptDeliverer, error), logger *slog.Logger) acp.Handler {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		id, aerr := decodeSessionID(params)
		if aerr != nil {
			return nil, aerr
		}
		payload, aerr := mapPromptContent(params)
		if aerr != nil {
			return nil, aerr
		}
		dlv, err := resolve(id)
		if err != nil {
			if errors.Is(err, sessions.ErrSessionNotFound) {
				return nil, acp.NewError(acp.CodeInvalidParams, "unknown session")
			}
			return nil, fmt.Errorf("session/prompt: %w", err)
		}

		// A held call needs a Responder; a session/prompt sent as a notification
		// (no id) carries none. Refuse to deliver rather than start a turn whose
		// stopReason can never be returned. dispatchNotification logs the plain
		// error and writes no frame (none is owed).
		resp := acp.ResponderFrom(ctx)
		if resp == nil {
			return nil, fmt.Errorf("session/prompt: missing request id (sent as notification)")
		}

		// Register the hold inline (race-free on the read loop) BEFORE spawning:
		// a concurrent second prompt for the same session is rejected here, and no
		// delivery goroutine is spawned for it.
		if !holds.begin(string(id), resp) {
			return nil, acp.NewError(acp.CodeInvalidRequest, "a prompt is already in flight for this session")
		}
		go deliverPrompt(ctx, holds, id, dlv, payload, logger)
		return nil, acp.ErrDeferred
	}
}

// deliverPrompt is the delivery goroutine body: it delivers payload into the
// supervised interactive claude via dlv.WriteUserTurn under a promptDeliverTimeout
// bound, off the read loop. On success the call stays held (T7 resolves it on
// TurnEnd). On failure it resolves the held call with a generic error frame and
// frees the slot; the supervisor error sentinel is logged at Warn — never the
// payload. ctx is the handler ctx (derived from the Serve loop ctx), so Serve
// shutdown cancels an in-flight delivery.
func deliverPrompt(ctx context.Context, holds *promptHolds, id sessions.SessionID, dlv promptDeliverer, payload []byte, logger *slog.Logger) {
	tctx, cancel := context.WithTimeout(ctx, promptDeliverTimeout)
	defer cancel()
	if err := dlv.WriteUserTurn(tctx, string(id), payload); err != nil {
		logger.Warn("acp: prompt delivery failed",
			"event", "acp_prompt.deliver_err",
			"session_id", string(id),
			"err", err.Error())
		holds.fail(string(id), acp.NewError(acp.CodeInternalError, "prompt delivery failed"))
	}
}
