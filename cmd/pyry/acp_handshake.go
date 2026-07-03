package main

import (
	"context"
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/acp"
)

// SupportedProtocolVersion is the single ACP protocol version pyry speaks. ACP
// protocolVersion is an integer (u16 on the wire); v1 is current. The initialize
// handler returns it unconditionally — with exactly one supported version there
// is nothing to negotiate, and every real client advertises >= 1, so returning 1
// is always a valid (<= client) response. A min(client, agent) clamp lands only
// when pyry speaks a second version. Reconcile the literal with the ACP mapping
// ADR (#745) when it merges (interim source: the vault ACP-mapping doc).
const SupportedProtocolVersion = 1

// initializeParams is the subset of the ACP InitializeRequest pyry reads.
// Decoding it serves purely as a shape gate: a params that is not a JSON object
// (e.g. an array) fails to unmarshal and is rejected with CodeInvalidParams. The
// host's clientCapabilities (fs/terminal) are intentionally NOT modelled — an
// unmodelled field is silently ignored on decode, which is exactly divergence 5's
// "accept a host that offers fs/terminal and never use them".
type initializeParams struct {
	ProtocolVersion int `json:"protocolVersion"`
}

// initializeResult is the ACP InitializeResponse pyry emits. It carries only the
// protocol version, the agent capabilities pyry actually backs today, and the
// (empty) auth-method list. ACP's InitializeResponse has no "agent requires
// client capability X" field, so divergence 5 ("pyry needs no fs/terminal") is
// structural: there is simply no place here to request a host capability, and
// pyry issues no fs/* or terminal/* outbound Calls.
type initializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
	AuthMethods       []authMethod      `json:"authMethods"` // always [], never nil
}

// agentCapabilities advertises only the OPTIONAL agent capabilities whose backing
// methods this epic has delivered. Core methods (session/new, session/prompt) are
// baseline-mandatory and are NOT capability-gated. Everything is false today:
// loadSession's richer resume semantics and rich prompt content land in the
// session-lifecycle tickets, which amend this struct when they do.
type agentCapabilities struct {
	LoadSession        bool               `json:"loadSession"`        // session/load resume — #748
	PromptCapabilities promptCapabilities `json:"promptCapabilities"` // rich prompt content — #748
}

// promptCapabilities flags the non-text prompt content pyry accepts. All false:
// pyry drives a text-only interactive claude turn today.
type promptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

// authMethod is one entry of initializeResult.AuthMethods. None are advertised
// today; the type is kept so authMethods marshals as a typed empty array (see
// initializeHandler) and so a future real auth method has a shape to populate.
type authMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// authenticateResult is the ACP AuthenticateResponse. It has no fields today —
// it marshals to {}, the spec-compliant all-optional-fields object, chosen over
// null for forward-compatibility with the object-shaped response.
type authenticateResult struct{}

// initializeHandler answers the ACP initialize request. It is stateless: it holds
// no session state and succeeds before any session exists, so it is safe to
// register without a pool. A non-empty params that is not an object is rejected
// (CodeInvalidParams); an empty/absent params is tolerated. The returned result
// declares pyry's protocol version and its minimal agent capability set.
func initializeHandler(_ context.Context, params json.RawMessage) (any, error) {
	if len(params) > 0 {
		// Decode as a shape gate only — the client's requested protocolVersion is
		// not read (no negotiation with one supported version); unmodelled
		// clientCapabilities are ignored (divergence 5).
		var p initializeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, acp.NewError(acp.CodeInvalidParams, "invalid initialize params")
		}
	}
	return initializeResult{
		ProtocolVersion:   SupportedProtocolVersion,
		AgentCapabilities: agentCapabilities{},
		AuthMethods:       []authMethod{},
	}, nil
}

// authenticateHandler answers the ACP authenticate request. Local pyry acp speaks
// to a co-located, user-launched host over stdio; there is no token, secret, or
// credential in play, so authentication is a deliberate no-op that always
// succeeds. It ignores params and returns the empty AuthenticateResponse.
func authenticateHandler(_ context.Context, _ json.RawMessage) (any, error) {
	return authenticateResult{}, nil
}

// registerHandshake binds the ACP handshake methods (initialize + authenticate)
// on the transport. Called once before Serve, alongside the session/* handlers,
// so the composition root reads as a list of capability registrations. Both
// handlers are stateless, so they take no pool.
func registerHandshake(t *acp.Transport) {
	t.Register("initialize", initializeHandler)
	t.Register("authenticate", authenticateHandler)
}
