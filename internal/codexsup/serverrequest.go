package codexsup

import (
	"context"
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/acp"
)

// ServerRequest is one request the app-server sent to the client (a frame
// carrying both id and method), such as an approval prompt. It must be
// answered exactly once, by Respond or Decline, from any goroutine; until it
// is, the Codex turn that raised it waits.
type ServerRequest struct {
	Method string
	// Params is the request's params as the app-server sent them. It is
	// untrusted input and may carry commands, paths and file contents.
	Params json.RawMessage

	resp *acp.Responder
}

// Respond answers the request with result. A second answer writes nothing
// and returns acp.ErrAlreadyResolved.
func (r *ServerRequest) Respond(result any) error {
	return r.resp.Reply(result)
}

// Decline answers the request with the package's default: a decline for the
// approval methods, a JSON-RPC error for everything else. It never accepts.
func (r *ServerRequest) Decline() error {
	result, rpcErr := declineFor(r.Method)
	if rpcErr != nil {
		return r.resp.ReplyError(rpcErr)
	}
	return r.resp.Reply(result)
}

// declineRejection is the reason carried by a ReviewDecision denial.
const declineRejection = "declined by pyrycode: no approval was given"

// declineFor is the default answer to a server request, shaped by the 0.156.1
// schema's response definitions; TestDefaultDeclinesMatchSchema validates each
// one against them. PermissionsRequestApprovalResponse has no decision field,
// so its decline is an empty grant. ReviewDecision (the legacy approvals) has
// no plain-string denial: its deny arm is the object DeniedReviewDecision.
// cancel/abort are not used: they also interrupt the turn.
func declineFor(method string) (result any, rpcErr *acp.Error) {
	switch method {
	case methodCommandApproval, methodFileChangeApproval:
		return map[string]string{"decision": "decline"}, nil
	case methodPermissionsApproval:
		return map[string]any{"permissions": map[string]any{}}, nil
	case methodApplyPatchApproval, methodExecCommandApproval:
		return map[string]any{"decision": map[string]any{
			"denied": map[string]string{"rejection": declineRejection},
		}}, nil
	default:
		return nil, acp.NewError(acp.CodeMethodNotFound, "unsupported server request")
	}
}

// serverRequestHandler routes one server request method to onRequest, or to
// the default decline when onRequest is nil. It runs inline on the read loop.
func serverRequestHandler(method string, onRequest func(*ServerRequest)) acp.Handler {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		if onRequest == nil {
			result, rpcErr := declineFor(method)
			if rpcErr != nil {
				return nil, rpcErr
			}
			return result, nil
		}
		onRequest(&ServerRequest{Method: method, Params: params, resp: acp.ResponderFrom(ctx)})
		return nil, acp.ErrDeferred
	}
}
