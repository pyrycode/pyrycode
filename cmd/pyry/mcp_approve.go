package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/permbridge"
)

// mcpServerName is the MCP server name this subcommand advertises. It is
// load-bearing: the sibling #1106 registers this server as pyry_approve so the
// full tool reference claude is pointed at is mcp__pyry_approve__approve.
const mcpServerName = "pyry_approve"

// approveToolName is the single tool this server exposes. Load-bearing for the
// same reason as mcpServerName — it forms the __approve suffix of the tool
// reference — and the tools/call handler rejects any other name.
const approveToolName = "approve"

// defaultMCPProtocolVersion is the MCP protocol version returned from initialize
// when the client sends none. Our surface (initialize/tools/list/tools/call) is
// version-invariant across MCP revisions, so we echo the client's requested
// version when present (the MCP lifecycle "respond with the same version if
// supported" rule) and fall back to this pinned value otherwise.
const defaultMCPProtocolVersion = "2025-06-18"

// mcpApproveClientMargin is added to mcpApprovalTimeout to form the client read
// deadline, so the daemon's own approval timer fires first — its informative
// "approval request timed out" deny passes through — rather than the client
// read deadline (a generic transport error → our own "approval unavailable"
// deny). Both are denies; the margin just prefers the daemon's message. Inert
// until #1106 wires --permission-prompt-tool in production; tune then.
const mcpApproveClientMargin = 30 * time.Second

// approveServer is the MCP stdio approve tool host. It is a pure forwarder: each
// tools/call is re-framed into a control-socket mcp.approve request and the
// daemon's verdict is re-framed into the MCP tool result claude blocks on. The
// value is read-only after construction — no shared mutable state, no locks.
type approveServer struct {
	socketPath string        // resolved control socket to forward approvals to
	timeout    time.Duration // per-call client read deadline (approval window + margin)
	log        *slog.Logger  // stderr only — stdout is exclusively the JSON-RPC frame stream
}

// runMCPApprove implements `pyry mcp-approve`: an MCP server speaking JSON-RPC
// over stdio that exposes a single `approve` tool. claude (spawned with
// --permission-prompt-tool mcp__pyry_approve__approve) synchronously calls that
// tool for every non-allowlisted tool use and blocks on its allow/deny JSON.
// Each call is forwarded to the daemon over the control socket and the verdict
// returned as the tool result — fail-closed to deny on any error so claude's
// turn never hangs. It takes the shared client flags (-pyry-name/-pyry-socket)
// and no positionals.
func runMCPApprove(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry mcp-approve", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("mcp-approve: unexpected arguments: %s", strings.Join(rest, " "))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// stderr logger: stdout is exclusively the JSON-RPC frame stream claude
	// reads; a stray stdout write corrupts the MCP stream. Diagnostics stay off
	// stdout, mirroring runACP.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("mcp-approve: serving MCP approve tool over stdio")

	s := &approveServer{
		socketPath: socketPath,
		timeout:    mcpApprovalTimeout + mcpApproveClientMargin,
		log:        logger,
	}
	return serveJSONRPCStdio(ctx, os.Stdin, os.Stdout, logger, s.register)
}

// register binds the three MCP request handlers on the transport. Notifications
// (notifications/initialized and any other) need no handler — dispatchNotification
// drops an unregistered notification silently, writing no response frame.
func (s *approveServer) register(t *acp.Transport) {
	t.Register("initialize", s.initialize)
	t.Register("tools/list", s.toolsList)
	t.Register("tools/call", s.toolsCall)
}

// --- initialize -------------------------------------------------------------

// mcpInitializeParams is the subset of the MCP InitializeRequest we read. Only
// protocolVersion is consulted (echoed back when present); decoding also serves
// as a shape gate — a params that is not a JSON object is rejected.
type mcpInitializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// mcpInitializeResult is the MCP InitializeResponse. capabilities.tools is an
// empty object (we advertise the tools capability with no sub-flags);
// serverInfo.name is load-bearing (see mcpServerName).
type mcpInitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    mcpCapabilities `json:"capabilities"`
	ServerInfo      mcpServerInfo   `json:"serverInfo"`
}

type mcpCapabilities struct {
	Tools mcpToolsCapability `json:"tools"`
}

// mcpToolsCapability marshals to {} — the tools capability carries no sub-flags.
type mcpToolsCapability struct{}

type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initialize answers the MCP initialize handshake. It echoes the client's
// protocolVersion when present and non-empty, falling back to
// defaultMCPProtocolVersion otherwise. A non-object params is rejected with
// CodeInvalidParams (shape gate, like the ACP initializeHandler); absent/empty
// params is tolerated. No params bytes are logged.
func (s *approveServer) initialize(_ context.Context, params json.RawMessage) (any, error) {
	version := defaultMCPProtocolVersion
	if len(params) > 0 {
		var p mcpInitializeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, acp.NewError(acp.CodeInvalidParams, "invalid initialize params")
		}
		if p.ProtocolVersion != "" {
			version = p.ProtocolVersion
		}
	}
	return mcpInitializeResult{
		ProtocolVersion: version,
		Capabilities:    mcpCapabilities{},
		ServerInfo:      mcpServerInfo{Name: mcpServerName, Version: Version},
	}, nil
}

// --- tools/list -------------------------------------------------------------

// mcpTool is one entry of the tools/list result.
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpToolsListResult struct {
	Tools []mcpTool `json:"tools"`
}

// approveInputSchema is the advisory JSON Schema advertised for the approve
// tool. It is advisory only — claude's permission path fills the fixed
// {tool_name,input,tool_use_id} shape regardless — so a static literal is
// clearer than a nest of schema structs.
var approveInputSchema = json.RawMessage(`{"type":"object","properties":{"tool_name":{"type":"string"},"input":{"type":"object"},"tool_use_id":{"type":"string"}}}`)

// toolsList advertises the single approve tool.
func (s *approveServer) toolsList(_ context.Context, _ json.RawMessage) (any, error) {
	return mcpToolsListResult{
		Tools: []mcpTool{{
			Name:        approveToolName,
			Description: "Gate a claude tool use; returns an allow or deny verdict.",
			InputSchema: approveInputSchema,
		}},
	}, nil
}

// --- tools/call -------------------------------------------------------------

// mcpToolCallParams is the MCP tools/call params. arguments is opaque
// (json.RawMessage) and only unmarshalled into the approve payload once name is
// verified; it is never dispatched on.
type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// mcpTextContent is one MCP content block.
type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// mcpToolResult is the MCP tools/call result. isError is ALWAYS false, even for
// a fail-closed deny: a deny verdict is a *successful* tool result claude must
// honour; isError:true risks claude discarding the content and mishandling the
// turn.
type mcpToolResult struct {
	Content []mcpTextContent `json:"content"`
	IsError bool             `json:"isError"`
}

// toolsCall is the fail-closed core. It ALWAYS returns a well-formed tool result
// (isError:false) whose first text block is a verdict JSON — never a JSON-RPC
// error, never a hang. The only verdict this subcommand self-originates is deny;
// allow passes through solely from the daemon's trusted resolver
// (resp.Approve). The model-controlled `input` is carried as json.RawMessage and
// never parsed, dispatched on, or logged.
func (s *approveServer) toolsCall(ctx context.Context, params json.RawMessage) (any, error) {
	var call mcpToolCallParams
	if err := json.Unmarshal(params, &call); err != nil {
		// Deliberately a deny result, NOT CodeInvalidParams: a JSON-RPC error on
		// the permission path risks confusing/hanging claude's turn. Fail-closed.
		return s.deny("", "malformed approval request"), nil
	}
	if call.Name != approveToolName {
		return s.deny("", "unknown tool"), nil
	}

	// input stays json.RawMessage — never parsed or dispatched on. A malformed
	// arguments object fails here (well-formed-JSON gate) → deny, never a crash.
	var payload control.ApprovePayload
	if err := json.Unmarshal(call.Arguments, &payload); err != nil {
		return s.deny("", "malformed approval request"), nil
	}

	// cctx carries both the per-call deadline and signal cancellation (ctx is the
	// Serve ctx), so a mid-approval SIGTERM unblocks the control read → deny.
	cctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	res, err := control.Approve(cctx, s.socketPath, payload)
	switch {
	case err != nil:
		// Socket unreachable, daemon Response.Error (nil registry / missing id),
		// or a client read-deadline timeout — all fail-closed to deny.
		return s.deny(payload.ToolUseID, "approval unavailable"), nil
	case res == nil:
		// Belt-and-suspenders; control.Approve already errors on a nil verdict.
		return s.deny(payload.ToolUseID, "no verdict"), nil
	}

	// allow and daemon-deny both pass through here: marshal the daemon's verdict
	// verbatim (updatedInput byte-preserved) into the first text block. The whole
	// result is emitted by one outer json.Marshal (in the transport), so any
	// control byte inside the echoed updatedInput is escaped in the outer string
	// and the MCP stdout frame stays a single physical line.
	verdict, err := json.Marshal(res)
	if err != nil {
		// A *control.ApproveResult always marshals; a failure here would be a Go
		// bug, not adversarial input. Fail-closed rather than leak an error frame.
		return s.deny(payload.ToolUseID, "no verdict"), nil
	}
	s.logVerdict(payload.ToolUseID, res.Behavior)
	return mcpToolResult{
		Content: []mcpTextContent{{Type: "text", Text: string(verdict)}},
		IsError: false,
	}, nil
}

// deny logs a content-free decision line and returns a fail-closed deny tool
// result. toolUseID is "" on the pre-parse branches (malformed params / unknown
// tool); reason is always a fixed constant — never model-controlled bytes.
func (s *approveServer) deny(toolUseID, reason string) mcpToolResult {
	s.logVerdict(toolUseID, permbridge.BehaviorDeny)
	return denyResult(reason)
}

// logVerdict is the sole decision log. It emits only the correlation key and the
// behaviour — never `input`, `tool_name`, or the raw params/arguments bytes,
// which are model-controlled and potentially secret-bearing on EVERY branch.
func (s *approveServer) logVerdict(toolUseID, behavior string) {
	s.log.Info("mcp-approve: verdict", "tool_use_id", toolUseID, "behavior", behavior)
}

// denyResult builds a deny tool result end-to-end: a control.ApproveResult with
// the canonical permbridge deny behavior and the fixed reason, marshalled into
// the first text block, isError:false. reason is always a fixed constant, so the
// marshal cannot fail on adversarial input.
func denyResult(reason string) mcpToolResult {
	verdict, err := json.Marshal(control.ApproveResult{
		Behavior: permbridge.BehaviorDeny,
		Message:  reason,
	})
	if err != nil {
		// Unreachable: a fixed-constant struct always marshals. Keep the invariant
		// (a well-formed verdict JSON) even in the impossible case.
		verdict = []byte(`{"behavior":"deny","message":"approval unavailable"}`)
	}
	return mcpToolResult{
		Content: []mcpTextContent{{Type: "text", Text: string(verdict)}},
		IsError: false,
	}
}
